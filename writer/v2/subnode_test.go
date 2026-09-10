package writer

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"

	"github.com/grokify/outlook-pst-go/pkg/disk"
)

func TestSLBlockLayoutRoundTrip(t *testing.T) {
	ents := []SLEntry{
		{NID: 0x21, DataBID: 4, SubBID: 0},
		{NID: 0x41, DataBID: 8, SubBID: 12},
	}
	raw, err := EncodeSLBlock(ents)
	if err != nil {
		t.Fatal(err)
	}
	if raw[0] != BlockTypeSubnode || raw[1] != 0 {
		t.Fatalf("header %x %x", raw[0], raw[1])
	}
	if pad := binary.LittleEndian.Uint32(raw[4:8]); pad != 0 {
		t.Fatalf("dwPadding 0x%08x", pad)
	}
	v, err := InspectSubnodeBlock(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Leaves) != 2 || v.Leaves[1].SubBID != 12 {
		t.Fatalf("%+v", v)
	}
	parsed, err := disk.ParseSubnodeBlock(raw, disk.FormatUnicode)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Level != 0 || parsed.Count != 2 || parsed.LeafEntries[0].NID != 0x21 {
		t.Fatalf("legacy %+v", parsed)
	}
}

func TestSLBlockRejectsUnsorted(t *testing.T) {
	_, err := EncodeSLBlock([]SLEntry{{NID: 0x41, DataBID: 4}, {NID: 0x21, DataBID: 8}})
	if !errors.Is(err, ErrInvalidArg) {
		t.Fatalf("got %v", err)
	}
}

func TestSIBlockFirstKey(t *testing.T) {
	kids := []SIEntry{{Key: 0x21, Ref: MakeInternalBID(8)}, {Key: 0x401, Ref: MakeInternalBID(12)}}
	raw, err := EncodeSIBlock(1, kids)
	if err != nil {
		t.Fatal(err)
	}
	v, err := InspectSubnodeBlock(raw)
	if err != nil {
		t.Fatal(err)
	}
	if v.Level != 1 || v.Kids[0].Key != 0x21 || v.Kids[1].Key != 0x401 {
		t.Fatalf("%+v", v)
	}
	parsed, err := disk.ParseSubnodeBlock(raw, disk.FormatUnicode)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Level != 1 || parsed.NonleafEntries[1].Key != 0x401 {
		t.Fatalf("legacy %+v", parsed)
	}
}

func TestSubnodeLeafAndSIBlockRoundTrip(t *testing.T) {
	n := NewNDB(nil)
	a := mustAlloc(t, n, 8)
	b := mustAlloc(t, n, 8)
	if err := n.putPayload(a, make([]byte, 8)); err != nil {
		t.Fatal(err)
	}
	if err := n.putPayload(b, make([]byte, 8)); err != nil {
		t.Fatal(err)
	}
	root, err := n.PutSubnodeTree([]SLEntry{
		{NID: 0x21, DataBID: a.BID},
		{NID: 0x41, DataBID: b.BID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if BIDIsInternal(root.BID) == false {
		t.Fatal("SLBLOCK root must be internal")
	}
	mustNode(t, n, 0x61, a.BID, root.BID, 0)
	var seen []uint64
	if err := n.WalkSubnodes(root.BID, func(e SLEntry) error {
		seen = append(seen, e.NID)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 2 || seen[0] != 0x21 || seen[1] != 0x41 {
		t.Fatalf("walk %v", seen)
	}
	file, err := n.Commit()
	if err != nil {
		t.Fatal(err)
	}
	n2, err := OpenNDB(file)
	if err != nil {
		t.Fatal(err)
	}
	if n2.subnodeRefs[a.BID] < 1 || n2.opaqueRefs[a.BID] != 0 {
		t.Fatalf("typed subnode ref a data=%d sub=%d opaque=%d", n2.dataTreeRefs[a.BID], n2.subnodeRefs[a.BID], n2.opaqueRefs[a.BID])
	}
	var seen2 []uint64
	if err := n2.WalkSubnodes(root.BID, func(e SLEntry) error {
		seen2 = append(seen2, e.NID)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(seen2) != 2 {
		t.Fatalf("reopen walk %v", seen2)
	}
}

func TestSubnodeForcesSIBlock(t *testing.T) {
	n := NewNDB(nil)
	need := MaxSLBlockEntries + 1
	ents := make([]SLEntry, need)
	var blocks []BBTEntry
	for i := 0; i < need; i++ {
		blk := mustAlloc(t, n, 8)
		if err := n.putPayload(blk, make([]byte, 8)); err != nil {
			t.Fatal(err)
		}
		blocks = append(blocks, blk)
		ents[i] = SLEntry{NID: uint64(i + 1), DataBID: blk.BID}
	}
	root, err := n.PutSubnodeTree(ents)
	if err != nil {
		t.Fatal(err)
	}
	data, err := n.blockPayload(root)
	if err != nil {
		t.Fatal(err)
	}
	v, err := InspectSubnodeBlock(data)
	if err != nil {
		t.Fatal(err)
	}
	if v.Level != 1 || len(v.Kids) != 2 {
		t.Fatalf("expected SIBLOCK level 1 with 2 kids, got %+v", v)
	}
	mustNode(t, n, 0x21, blocks[0].BID, root.BID, 0)
	var count int
	if err := n.WalkSubnodes(root.BID, func(e SLEntry) error {
		count++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if count != need {
		t.Fatalf("walked %d want %d", count, need)
	}
	file, err := n.Commit()
	if err != nil {
		t.Fatal(err)
	}
	n2, err := OpenNDB(file)
	if err != nil {
		t.Fatal(err)
	}
	count = 0
	if err := n2.WalkSubnodes(root.BID, func(e SLEntry) error {
		count++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if count != need {
		t.Fatalf("reopen walked %d", count)
	}
}

func TestSIBlockRejectsLevelNotOne(t *testing.T) {
	kids := []SIEntry{{Key: 0x21, Ref: MakeInternalBID(8)}}
	if _, err := EncodeSIBlock(2, kids); !errors.Is(err, ErrInvalidArg) {
		t.Fatalf("cLevel 2: %v", err)
	}
	if _, err := EncodeSIBlock(0, kids); !errors.Is(err, ErrInvalidArg) {
		t.Fatalf("cLevel 0: %v", err)
	}
	raw, err := EncodeSIBlock(1, kids)
	if err != nil {
		t.Fatal(err)
	}
	raw[1] = 2
	if _, err := InspectSubnodeBlock(raw); !errors.Is(err, ErrInvariant) {
		t.Fatalf("inspect cLevel 2: %v", err)
	}
}

func TestSIBlockCapacityExceeded(t *testing.T) {
	kids := make([]SIEntry, MaxSIBlockEntries+1)
	for i := range kids {
		kids[i] = SIEntry{Key: uint64(i + 1), Ref: MakeInternalBID(uint64(8 + 4*i))}
	}
	if _, err := EncodeSIBlock(SIBlockLevel, kids); !errors.Is(err, ErrLimit) {
		t.Fatalf("over capacity: %v", err)
	}
}

func TestPutSubnodeTreeRollsBackOnMissingChild(t *testing.T) {
	n := NewNDB(nil)
	fillRegionZero(t, n)
	beforeR := n.Store().RegionCount()
	beforeEOF := n.Store().FileEOF()
	beforeBid := n.Store().bidNextB
	beforeIDs := n.ids.nextBlock
	beforeAMap := n.Store().lastAllocAMap
	_, err := n.PutSubnodeTree([]SLEntry{{NID: 0x21, DataBID: 4}})
	if err == nil {
		t.Fatal("missing DataBID accepted")
	}
	assertAllocatorRestored(t, n, beforeR, beforeEOF, beforeBid, beforeIDs, beforeAMap)
}

func TestSLEntryRejectsReversedRoles(t *testing.T) {
	n := NewNDB(nil)
	data := mustAlloc(t, n, 8)
	if err := n.putPayload(data, make([]byte, 8)); err != nil {
		t.Fatal(err)
	}
	inner, err := n.PutSubnodeTree([]SLEntry{{NID: 0x21, DataBID: data.BID}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = n.PutSubnodeTree([]SLEntry{{NID: 0x41, DataBID: inner.BID}})
	if !errors.Is(err, ErrInvalidArg) && !errors.Is(err, ErrInvariant) {
		t.Fatalf("subnode as bidData: %v", err)
	}
	_, err = n.PutSubnodeTree([]SLEntry{{NID: 0x41, DataBID: data.BID, SubBID: data.BID}})
	if !errors.Is(err, ErrInvalidArg) && !errors.Is(err, ErrInvariant) {
		t.Fatalf("data as bidSub: %v", err)
	}
}

func TestNestedSubnodeBidSubRoundTrip(t *testing.T) {
	n := NewNDB(nil)
	innerData := mustAlloc(t, n, 8)
	if err := n.putPayload(innerData, []byte("innersub")); err != nil {
		t.Fatal(err)
	}
	inner, err := n.PutSubnodeTree([]SLEntry{{NID: 0x21, DataBID: innerData.BID}})
	if err != nil {
		t.Fatal(err)
	}
	outerData := mustAlloc(t, n, 8)
	if err := n.putPayload(outerData, []byte("outerdat")); err != nil {
		t.Fatal(err)
	}
	outer, err := n.PutSubnodeTree([]SLEntry{{NID: 0x41, DataBID: outerData.BID, SubBID: inner.BID}})
	if err != nil {
		t.Fatal(err)
	}
	mustNode(t, n, 0x61, outerData.BID, outer.BID, 0)
	var kids []SLEntry
	if err := n.WalkSubnodes(outer.BID, func(e SLEntry) error {
		kids = append(kids, e)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(kids) != 1 || kids[0].SubBID != inner.BID || kids[0].DataBID != outerData.BID {
		t.Fatalf("outer walk %+v", kids)
	}
	var innerKids []SLEntry
	if err := n.WalkSubnodes(inner.BID, func(e SLEntry) error {
		innerKids = append(innerKids, e)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(innerKids) != 1 || innerKids[0].DataBID != innerData.BID {
		t.Fatalf("inner walk %+v", innerKids)
	}
	file, err := n.Commit()
	if err != nil {
		t.Fatal(err)
	}
	n2, err := OpenNDB(file)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := n2.LookupNode(0x61)
	if !ok || got.SubBID != outer.BID {
		t.Fatalf("reopen node %+v ok=%v", got, ok)
	}
	var kids2 []SLEntry
	if err := n2.WalkSubnodes(got.SubBID, func(e SLEntry) error {
		kids2 = append(kids2, e)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(kids2) != 1 || kids2[0].SubBID != inner.BID {
		t.Fatalf("reopen walk %+v", kids2)
	}
	var inner2 []SLEntry
	if err := n2.WalkSubnodes(kids2[0].SubBID, func(e SLEntry) error {
		inner2 = append(inner2, e)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(inner2) != 1 || inner2[0].DataBID != innerData.BID {
		t.Fatalf("reopen nested %+v", inner2)
	}
	if n2.subnodeRefs[inner.BID] < 1 || n2.subnodeRefs[innerData.BID] < 1 {
		t.Fatalf("typed nested refs inner=%d data=%d", n2.subnodeRefs[inner.BID], n2.subnodeRefs[innerData.BID])
	}
	if err := n2.DeleteNode(0x61); err != nil {
		t.Fatal(err)
	}
	if _, ok := n2.LookupBlock(outer.BID); ok {
		t.Fatal("outer subnode tree leaked")
	}
	if _, ok := n2.LookupBlock(inner.BID); ok {
		t.Fatal("inner subnode tree leaked")
	}
	if _, ok := n2.LookupBlock(innerData.BID); ok {
		t.Fatal("inner data leaked")
	}
	if _, ok := n2.LookupBlock(outerData.BID); ok {
		t.Fatal("outer data leaked")
	}
}

func TestPutSubnodeTreeRejectsMalformedDataTree(t *testing.T) {
	n := NewNDB(nil)
	leaf := mustAlloc(t, n, 8)
	if err := n.putPayload(leaf, bytes.Repeat([]byte{1}, 8)); err != nil {
		t.Fatal(err)
	}
	xp, err := EncodeXBlock(XBlockLevel, 8, []uint64{leaf.BID})
	if err != nil {
		t.Fatal(err)
	}
	xb, err := n.AllocInternalBlock(uint16(len(xp)))
	if err != nil {
		t.Fatal(err)
	}
	if err := n.putPayload(xb, xp); err != nil {
		t.Fatal(err)
	}
	if err := n.AddDataTreeRef(leaf.BID); err != nil {
		t.Fatal(err)
	}
	innerp, err := EncodeXBlock(XXBlockLevel, 8, []uint64{xb.BID})
	if err != nil {
		t.Fatal(err)
	}
	inner, err := n.AllocInternalBlock(uint16(len(innerp)))
	if err != nil {
		t.Fatal(err)
	}
	if err := n.putPayload(inner, innerp); err != nil {
		t.Fatal(err)
	}
	if err := n.AddDataTreeRef(xb.BID); err != nil {
		t.Fatal(err)
	}
	outerp, err := EncodeXBlock(XXBlockLevel, 8, []uint64{inner.BID})
	if err != nil {
		t.Fatal(err)
	}
	outer, err := n.AllocInternalBlock(uint16(len(outerp)))
	if err != nil {
		t.Fatal(err)
	}
	if err := n.putPayload(outer, outerp); err != nil {
		t.Fatal(err)
	}
	if err := n.AddDataTreeRef(inner.BID); err != nil {
		t.Fatal(err)
	}
	_, err = n.PutSubnodeTree([]SLEntry{{NID: 0x21, DataBID: outer.BID}})
	if !errors.Is(err, ErrInvariant) {
		t.Fatalf("xx->xx bidData: %v", err)
	}
}

func TestPutSubnodeTreeRejectsCyclicNestedSub(t *testing.T) {
	n := NewNDB(nil)
	data := mustAlloc(t, n, 8)
	if err := n.putPayload(data, make([]byte, 8)); err != nil {
		t.Fatal(err)
	}
	blk, err := n.AllocInternalBlock(8)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := EncodeSLBlock([]SLEntry{{NID: 0x21, DataBID: data.BID, SubBID: blk.BID}})
	if err != nil {
		t.Fatal(err)
	}
	if uint16(len(payload)) != blk.CB {
		_ = n.dropBlock(blk.BID)
		blk, err = n.AllocInternalBlock(uint16(len(payload)))
		if err != nil {
			t.Fatal(err)
		}
		payload, err = EncodeSLBlock([]SLEntry{{NID: 0x21, DataBID: data.BID, SubBID: blk.BID}})
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := n.putPayload(blk, payload); err != nil {
		t.Fatal(err)
	}
	_, err = n.PutSubnodeTree([]SLEntry{{NID: 0x41, DataBID: data.BID, SubBID: blk.BID}})
	if !errors.Is(err, ErrInvariant) {
		t.Fatalf("cyclic bidSub: %v", err)
	}
}

func TestOpenNDBRejectsMalformedDataTree(t *testing.T) {
	n := NewNDB(nil)
	leaf := mustAlloc(t, n, 8)
	if err := n.putPayload(leaf, bytes.Repeat([]byte{1}, 8)); err != nil {
		t.Fatal(err)
	}
	xp, err := EncodeXBlock(XBlockLevel, 8, []uint64{leaf.BID})
	if err != nil {
		t.Fatal(err)
	}
	xb, err := n.AllocInternalBlock(uint16(len(xp)))
	if err != nil {
		t.Fatal(err)
	}
	if err := n.putPayload(xb, xp); err != nil {
		t.Fatal(err)
	}
	if err := n.AddDataTreeRef(leaf.BID); err != nil {
		t.Fatal(err)
	}
	innerp, err := EncodeXBlock(XXBlockLevel, 8, []uint64{xb.BID})
	if err != nil {
		t.Fatal(err)
	}
	inner, err := n.AllocInternalBlock(uint16(len(innerp)))
	if err != nil {
		t.Fatal(err)
	}
	if err := n.putPayload(inner, innerp); err != nil {
		t.Fatal(err)
	}
	if err := n.AddDataTreeRef(xb.BID); err != nil {
		t.Fatal(err)
	}
	outerp, err := EncodeXBlock(XXBlockLevel, 8, []uint64{inner.BID})
	if err != nil {
		t.Fatal(err)
	}
	outer, err := n.AllocInternalBlock(uint16(len(outerp)))
	if err != nil {
		t.Fatal(err)
	}
	if err := n.putPayload(outer, outerp); err != nil {
		t.Fatal(err)
	}
	if err := n.AddDataTreeRef(inner.BID); err != nil {
		t.Fatal(err)
	}
	mustNode(t, n, 0x21, outer.BID, 0, 0)
	file, err := n.Commit()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := OpenNDB(file); !errors.Is(err, ErrInvariant) {
		t.Fatalf("reopen xx->xx: %v", err)
	}
}

func TestOpenNDBRejectsMalformedSubnode(t *testing.T) {
	t.Run("si-child-is-si", func(t *testing.T) {
		n := NewNDB(nil)
		data := mustAlloc(t, n, 8)
		if err := n.putPayload(data, bytes.Repeat([]byte{1}, 8)); err != nil {
			t.Fatal(err)
		}
		sl, err := n.PutSubnodeTree([]SLEntry{{NID: 0x21, DataBID: data.BID}})
		if err != nil {
			t.Fatal(err)
		}
		innerp, err := EncodeSIBlock(SIBlockLevel, []SIEntry{{Key: 0x21, Ref: sl.BID}})
		if err != nil {
			t.Fatal(err)
		}
		inner, err := n.AllocInternalBlock(uint16(len(innerp)))
		if err != nil {
			t.Fatal(err)
		}
		if err := n.putPayload(inner, innerp); err != nil {
			t.Fatal(err)
		}
		if err := n.AddSubnodeRef(sl.BID); err != nil {
			t.Fatal(err)
		}
		outerp, err := EncodeSIBlock(SIBlockLevel, []SIEntry{{Key: 0x21, Ref: inner.BID}})
		if err != nil {
			t.Fatal(err)
		}
		outer, err := n.AllocInternalBlock(uint16(len(outerp)))
		if err != nil {
			t.Fatal(err)
		}
		if err := n.putPayload(outer, outerp); err != nil {
			t.Fatal(err)
		}
		if err := n.AddSubnodeRef(inner.BID); err != nil {
			t.Fatal(err)
		}
		mustNode(t, n, 0x61, 0, outer.BID, 0)
		file, err := n.Commit()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := OpenNDB(file); !errors.Is(err, ErrInvariant) {
			t.Fatalf("reopen SI->SI: %v", err)
		}
	})
	t.Run("missing-child", func(t *testing.T) {
		n := NewNDB(nil)
		fake := MakeInternalBID(0x1000)
		payload, err := EncodeSIBlock(SIBlockLevel, []SIEntry{{Key: 0x21, Ref: fake}})
		if err != nil {
			t.Fatal(err)
		}
		blk, err := n.AllocInternalBlock(uint16(len(payload)))
		if err != nil {
			t.Fatal(err)
		}
		if err := n.putPayload(blk, payload); err != nil {
			t.Fatal(err)
		}
		mustNode(t, n, 0x61, 0, blk.BID, 0)
		file, err := n.Commit()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := OpenNDB(file); !errors.Is(err, ErrInvariant) && !errors.Is(err, ErrInvalidArg) {
			t.Fatalf("reopen missing SIENTRY: %v", err)
		}
	})
	t.Run("cyclic-si", func(t *testing.T) {
		n := NewNDB(nil)
		blk, err := n.AllocInternalBlock(SIBlockHeaderSize + SIEntrySize)
		if err != nil {
			t.Fatal(err)
		}
		payload, err := EncodeSIBlock(SIBlockLevel, []SIEntry{{Key: 0x21, Ref: blk.BID}})
		if err != nil {
			t.Fatal(err)
		}
		if uint16(len(payload)) != blk.CB {
			_ = n.dropBlock(blk.BID)
			blk, err = n.AllocInternalBlock(uint16(len(payload)))
			if err != nil {
				t.Fatal(err)
			}
			payload, err = EncodeSIBlock(SIBlockLevel, []SIEntry{{Key: 0x21, Ref: blk.BID}})
			if err != nil {
				t.Fatal(err)
			}
		}
		if err := n.putPayload(blk, payload); err != nil {
			t.Fatal(err)
		}
		mustNode(t, n, 0x61, 0, blk.BID, 0)
		file, err := n.Commit()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := OpenNDB(file); !errors.Is(err, ErrInvariant) {
			t.Fatalf("reopen cyclic SI: %v", err)
		}
	})
}
