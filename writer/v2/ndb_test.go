package writer

import (
	"errors"
	"math/rand"
	"testing"
)

func mustBlock(t *testing.T, n *NDB, bid, ib uint64, cb uint16) {
	t.Helper()
	if err := n.PutBlock(BBTEntry{BID: bid, IB: ib, CB: cb}); err != nil {
		t.Fatal(err)
	}
}

func mustNode(t *testing.T, n *NDB, nid, data, sub uint64, parent uint32) {
	t.Helper()
	if err := n.PutNode(NBTEntry{NID: nid, DataBID: data, SubBID: sub, ParentNID: parent}); err != nil {
		t.Fatal(err)
	}
}

func TestNBTFirstKeyOnEncodedTree(t *testing.T) {
	n := NewNDB(NewSequentialIDs())
	mustBlock(t, n, 4, 0x5000, 16)
	for i := 1; i <= MaxNBTLeafEntries+1; i++ {
		mustNode(t, n, uint64(i), 4, 0, 0)
	}
	img, err := n.Encode(0x8000)
	if err != nil {
		t.Fatal(err)
	}
	if err := img.CheckTrees(); err != nil {
		t.Fatal(err)
	}
	lvl, err := img.RootLevel(img.NBTRoot)
	if err != nil {
		t.Fatal(err)
	}
	if lvl != 1 {
		t.Fatalf("level %d want 1", lvl)
	}
	root, err := img.page(img.NBTRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(root.Kids) != 2 {
		t.Fatalf("kids %d", len(root.Kids))
	}
	if root.Kids[0].Key != 1 || root.Kids[1].Key != uint64(MaxNBTLeafEntries+1) {
		t.Fatalf("first-key separators %+v", root.Kids)
	}
	leftLast := uint64(MaxNBTLeafEntries)
	if root.Kids[1].Key == leftLast {
		t.Fatal("separator is left child max key")
	}
}

func TestForcedThreeLevelNBTAndBBT(t *testing.T) {
	n := NewNDB(NewSequentialIDs())
	mustBlock(t, n, 4, 0x5000, 32)
	// 21 NBT leaves => 2 L1 pages => L2 root.
	need := MaxNBTLeafEntries*MaxBTNonleafEntries + 1
	for i := 1; i <= need; i++ {
		mustNode(t, n, uint64(i), 4, 0, 0)
	}
	// 21 BBT leaves as well (extra unused staged blocks).
	needBBT := MaxBBTLeafEntries*MaxBTNonleafEntries + 1
	next := uint64(8)
	for len(n.blocks) < needBBT {
		if _, ok := n.blocks[next]; !ok {
			mustBlock(t, n, next, 0x6000+next, 8)
		}
		next += BlockBIDIncrement
	}
	img, err := n.Encode(0x9000)
	if err != nil {
		t.Fatal(err)
	}
	if err := img.CheckTrees(); err != nil {
		t.Fatal(err)
	}
	nl, err := img.RootLevel(img.NBTRoot)
	if err != nil {
		t.Fatal(err)
	}
	bl, err := img.RootLevel(img.BBTRoot)
	if err != nil {
		t.Fatal(err)
	}
	if nl < 2 || bl < 2 {
		t.Fatalf("levels nbt=%d bbt=%d want >=2 (three levels including leaf)", nl, bl)
	}
	if _, err := img.LookupNode(1); err != nil {
		t.Fatal(err)
	}
	if _, err := img.LookupNode(uint64(need)); err != nil {
		t.Fatal(err)
	}
	if _, err := img.LookupBlock(4); err != nil {
		t.Fatal(err)
	}
}

func TestPageBIDsIncrementByOneNotOffset(t *testing.T) {
	ids := NewSequentialIDs()
	n := NewNDB(ids)
	mustBlock(t, n, 4, 0x5000, 8)
	mustNode(t, n, 0x21, 4, 0, 0)
	img, err := n.Encode(0x8000)
	if err != nil {
		t.Fatal(err)
	}
	var bids []uint64
	for ib, raw := range img.Pages {
		v, err := InspectBTPage(raw, ib)
		if err != nil {
			t.Fatal(err)
		}
		if v.Page.BID == ib {
			t.Fatalf("page BID equals IB 0x%x", ib)
		}
		bids = append(bids, v.Page.BID)
	}
	if len(bids) < 2 {
		t.Fatal("expected NBT and BBT pages")
	}
}

func TestRefCountOwnershipAndReclaim(t *testing.T) {
	n := NewNDB(NewSequentialIDs())
	mustBlock(t, n, 4, 0x5000, 16)
	mustBlock(t, n, 8, 0x5100, 16)
	e, _ := n.LookupBlock(4)
	if e.RefCount != 1 {
		t.Fatalf("staged cRef %d", e.RefCount)
	}
	mustNode(t, n, 0x21, 4, 0, 0)
	e, _ = n.LookupBlock(4)
	if e.RefCount != 2 {
		t.Fatalf("after node cRef %d want 2", e.RefCount)
	}
	mustNode(t, n, 0x61, 4, 0, 0) // share
	e, _ = n.LookupBlock(4)
	if e.RefCount != 3 {
		t.Fatalf("shared cRef %d want 3", e.RefCount)
	}
	if err := n.DeleteNode(0x61); err != nil {
		t.Fatal(err)
	}
	e, _ = n.LookupBlock(4)
	if e.RefCount != 2 {
		t.Fatalf("after unshare cRef %d", e.RefCount)
	}
	if err := n.DeleteNode(0x21); err != nil {
		t.Fatal(err)
	}
	if _, ok := n.LookupBlock(4); ok {
		t.Fatal("block 4 should be reclaimed at cRef==1")
	}
	mustNode(t, n, 0x122, 8, 0, 0)
	if err := n.AddDataTreeRef(8); err != nil {
		t.Fatal(err)
	}
	e, _ = n.LookupBlock(8)
	if e.RefCount != 3 { // BBTENTRY + NBT + data-tree
		t.Fatalf("data-tree cRef %d want 3", e.RefCount)
	}
	if err := n.CheckRefCounts(); err != nil {
		t.Fatal(err)
	}
	if err := n.ReleaseDataTreeRef(8); err != nil {
		t.Fatal(err)
	}
	e, _ = n.LookupBlock(8)
	if e.RefCount != 2 {
		t.Fatalf("after data-tree drop cRef %d", e.RefCount)
	}
}

func TestReplaceNodeKeepsSharedBlock(t *testing.T) {
	n := NewNDB(NewSequentialIDs())
	mustBlock(t, n, 4, 0x5000, 8)
	mustBlock(t, n, 8, 0x5100, 8)
	mustNode(t, n, 0x21, 4, 0, 0)
	mustNode(t, n, 0x61, 4, 0, 0)
	mustNode(t, n, 0x21, 8, 0, 0) // replace 21 onto block 8
	e4, ok := n.LookupBlock(4)
	if !ok || e4.RefCount != 2 {
		t.Fatalf("shared original %+v ok=%v", e4, ok)
	}
	e8, ok := n.LookupBlock(8)
	if !ok || e8.RefCount != 2 {
		t.Fatalf("replacement %+v ok=%v", e8, ok)
	}
}

func TestRandomizedTreeRoundTrip(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	n := NewNDB(NewSequentialIDs())
	const N = 80
	nids := rng.Perm(N)
	for i, p := range nids {
		bid := uint64(4 + 4*(p%17)) // 17 blocks, some shared
		if _, ok := n.LookupBlock(bid); !ok {
			mustBlock(t, n, bid, 0x4000+bid, 24)
		}
		mustNode(t, n, uint64(p+1), bid, 0, uint32(i))
	}
	if err := n.CheckRefCounts(); err != nil {
		t.Fatal(err)
	}
	img, err := n.Encode(0xA000)
	if err != nil {
		t.Fatal(err)
	}
	if err := img.CheckTrees(); err != nil {
		t.Fatal(err)
	}
	var ordered []uint64
	if err := img.WalkNBT(func(e NBTEntry) error {
		if len(ordered) > 0 && e.NID <= ordered[len(ordered)-1] {
			t.Fatalf("walk not ordered %d after %d", e.NID, ordered[len(ordered)-1])
		}
		ordered = append(ordered, e.NID)
		got, err := img.LookupNode(e.NID)
		if err != nil {
			return err
		}
		if got.DataBID != e.DataBID {
			return errors.New("lookup mismatch")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(ordered) != N {
		t.Fatalf("walked %d want %d", len(ordered), N)
	}

	// Updates and deletes.
	for _, nid := range []uint64{1, 7, 40} {
		e, _ := n.LookupNode(nid)
		alt := e.DataBID + 4
		if alt > 4+4*16 {
			alt = 4
		}
		if _, ok := n.LookupBlock(alt); !ok {
			mustBlock(t, n, alt, 0x4000+alt, 24)
		}
		mustNode(t, n, nid, alt, 0, e.ParentNID)
	}
	for _, nid := range []uint64{3, 11, 22, 50} {
		if err := n.DeleteNode(nid); err != nil {
			t.Fatal(err)
		}
	}
	if err := n.CheckRefCounts(); err != nil {
		t.Fatal(err)
	}
	img2, err := n.Encode(0xB000)
	if err != nil {
		t.Fatal(err)
	}
	if err := img2.CheckTrees(); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadTrees(img2, NewSequentialIDs())
	if err != nil {
		t.Fatal(err)
	}
	for nid := uint64(1); nid <= N; nid++ {
		_, live := n.LookupNode(nid)
		got, err := img2.LookupNode(nid)
		if live {
			if err != nil {
				t.Fatalf("live %d: %v", nid, err)
			}
			want, _ := n.LookupNode(nid)
			if got.DataBID != want.DataBID {
				t.Fatalf("nid %d data %d want %d", nid, got.DataBID, want.DataBID)
			}
			if _, ok := loaded.LookupNode(nid); !ok {
				t.Fatalf("loaded missing %d", nid)
			}
		} else if err == nil {
			t.Fatalf("deleted %d still present", nid)
		}
	}
	if err := loaded.CheckRefCounts(); err != nil {
		t.Fatal(err)
	}
}

func TestEmptyTreesEncode(t *testing.T) {
	n := NewNDB(NewSequentialIDs())
	img, err := n.Encode(0x8000)
	if err != nil {
		t.Fatal(err)
	}
	if err := img.CheckTrees(); err != nil {
		t.Fatal(err)
	}
	nl, _ := img.RootLevel(img.NBTRoot)
	bl, _ := img.RootLevel(img.BBTRoot)
	if nl != 0 || bl != 0 {
		t.Fatalf("empty roots level nbt=%d bbt=%d", nl, bl)
	}
}
