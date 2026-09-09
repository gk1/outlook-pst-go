package writer

import (
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

func TestSubnodeThreeLevelSynthetic(t *testing.T) {
	// Build 3-level SI without filling 170k leaves: encode two SI level-1
	// blocks as children of a level-2 SIBLOCK and inspect first-key.
	l1a, err := EncodeSIBlock(1, []SIEntry{{Key: 1, Ref: MakeInternalBID(8)}, {Key: 10, Ref: MakeInternalBID(12)}})
	if err != nil {
		t.Fatal(err)
	}
	l1b, err := EncodeSIBlock(1, []SIEntry{{Key: 100, Ref: MakeInternalBID(16)}, {Key: 110, Ref: MakeInternalBID(20)}})
	if err != nil {
		t.Fatal(err)
	}
	_ = l1a
	_ = l1b
	raw, err := EncodeSIBlock(2, []SIEntry{{Key: 1, Ref: MakeInternalBID(24)}, {Key: 100, Ref: MakeInternalBID(28)}})
	if err != nil {
		t.Fatal(err)
	}
	v, err := InspectSubnodeBlock(raw)
	if err != nil {
		t.Fatal(err)
	}
	if v.Level != 2 || v.Kids[0].Key != 1 || v.Kids[1].Key != 100 {
		t.Fatalf("%+v", v)
	}
}

func TestPutSubnodeTreeRollsBackOnMissingChild(t *testing.T) {
	n := NewNDB(nil)
	_, err := n.PutSubnodeTree([]SLEntry{{NID: 0x21, DataBID: 4}})
	if err == nil {
		t.Fatal("missing DataBID accepted")
	}
	if len(n.blocks) != 0 {
		t.Fatalf("leaked %d blocks", len(n.blocks))
	}
}
