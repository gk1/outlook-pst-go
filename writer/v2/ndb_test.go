package writer

import (
	"errors"
	"math/rand"
	"testing"

	"github.com/grokify/outlook-pst-go/pkg/disk"
)

func mustBlock(t *testing.T, n *NDB, bid, ib uint64, cb uint16) {
	t.Helper()
	if err := n.PutBlock(BBTEntry{BID: bid, IB: ib, CB: cb}); err != nil {
		t.Fatal(err)
	}
}

func mustAlloc(t *testing.T, n *NDB, cb uint16) BBTEntry {
	t.Helper()
	e, err := n.AllocBlock(cb)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func mustNode(t *testing.T, n *NDB, nid, data, sub uint64, parent uint32) {
	t.Helper()
	if err := n.PutNode(NBTEntry{NID: nid, DataBID: data, SubBID: sub, ParentNID: parent}); err != nil {
		t.Fatal(err)
	}
}

func TestNBTFirstKeyOnEncodedTree(t *testing.T) {
	n := NewNDB(NewSequentialIDs())
	blk := mustAlloc(t, n, 16)
	for i := 1; i <= MaxNBTLeafEntries+1; i++ {
		mustNode(t, n, uint64(i), blk.BID, 0, 0)
	}
	img, err := n.Encode()
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
	if root.Kids[1].Key == uint64(MaxNBTLeafEntries) {
		t.Fatal("separator is left child max key")
	}
}

func TestForcedThreeLevelNBTAndBBT(t *testing.T) {
	n := NewNDB(NewSequentialIDs())
	needNBT := MaxNBTLeafEntries*MaxBTNonleafEntries + 1
	needBBT := MaxBBTLeafEntries*MaxBTNonleafEntries + 1
	need := needBBT
	if needNBT > need {
		need = needNBT
	}
	for i := 1; i <= need; i++ {
		blk := mustAlloc(t, n, 8)
		mustNode(t, n, uint64(i), blk.BID, 0, 0)
	}
	img, err := n.Encode()
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
	first, ok := n.LookupNode(1)
	if !ok {
		t.Fatal("missing nid 1")
	}
	if _, err := img.LookupBlock(first.DataBID); err != nil {
		t.Fatal(err)
	}
}

func TestPageBIDsIncrementByOneNotOffset(t *testing.T) {
	n := NewNDB(NewSequentialIDs())
	blk := mustAlloc(t, n, 8)
	mustNode(t, n, 0x21, blk.BID, 0, 0)
	img, err := n.Encode()
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
		if ib%uint64(PageSize) != 0 {
			t.Fatalf("page IB 0x%x not 512-aligned", ib)
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
	mustNode(t, n, 0x61, 4, 0, 0)
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
	if e.RefCount != 3 {
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
	mustNode(t, n, 0x21, 8, 0, 0)
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
		bid := uint64(4 + 4*(p%17))
		if _, ok := n.LookupBlock(bid); !ok {
			mustBlock(t, n, bid, 0x4000+bid, 24)
		}
		mustNode(t, n, uint64(p+1), bid, 0, uint32(i))
	}
	if err := n.CheckRefCounts(); err != nil {
		t.Fatal(err)
	}
	img, err := n.Encode()
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
	img2, err := n.Encode()
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
	img, err := n.Encode()
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

func TestFinalOwnerReleaseFreesStore(t *testing.T) {
	n := NewNDB(nil)
	e := mustAlloc(t, n, 16)
	size := BlockDiskSize(16)
	if !n.Store().allocatedRange(e.IB, size) {
		t.Fatal("block not allocated")
	}
	if err := n.AddDataTreeRef(e.BID); err != nil {
		t.Fatal(err)
	}
	if err := n.AddSubnodeRef(e.BID); err != nil {
		t.Fatal(err)
	}
	got, _ := n.LookupBlock(e.BID)
	if got.RefCount != 3 {
		t.Fatalf("cRef %d want 3", got.RefCount)
	}
	if err := n.ReleaseSubnodeRef(e.BID); err != nil {
		t.Fatal(err)
	}
	got, ok := n.LookupBlock(e.BID)
	if !ok || got.RefCount != 2 {
		t.Fatalf("after subnode drop %+v ok=%v", got, ok)
	}
	if err := n.ReleaseDataTreeRef(e.BID); err != nil {
		t.Fatal(err)
	}
	if _, ok := n.LookupBlock(e.BID); ok {
		t.Fatal("final extra-ref owner must reclaim the BBTENTRY")
	}
	if n.Store().allocatedRange(e.IB, size) {
		t.Fatal("Store leaked reclaimed block")
	}
}

func TestOrphanCommitDropsUnownedBlock(t *testing.T) {
	n := NewNDB(nil)
	orphan := mustAlloc(t, n, 16)
	live := mustAlloc(t, n, 16)
	mustNode(t, n, 0x21, live.BID, 0, 0)
	file, err := n.Commit()
	if err != nil {
		t.Fatal(err)
	}
	img, err := OpenTrees(file)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := img.LookupBlock(orphan.BID); err == nil {
		t.Fatal("orphan cRef=1 BBTENTRY was serialized")
	}
	if _, err := img.LookupBlock(live.BID); err != nil {
		t.Fatal(err)
	}
	if n.Store().allocatedRange(orphan.IB, BlockDiskSize(16)) {
		t.Fatal("orphan Store allocation was not freed")
	}
	if _, ok := n.LookupBlock(orphan.BID); ok {
		t.Fatal("orphan remains in catalog after commit")
	}
}

func TestPersistedReopenFollowsHeaderBREFs(t *testing.T) {
	n := NewNDB(nil)
	for i := 1; i <= MaxNBTLeafEntries+1; i++ {
		blk := mustAlloc(t, n, 8)
		mustNode(t, n, uint64(i), blk.BID, 0, 0)
	}
	file, err := n.Commit()
	if err != nil {
		t.Fatal(err)
	}
	if err := CheckAllocation(file); err != nil {
		t.Fatal(err)
	}
	h, err := InspectHeader(file[:UnicodeHeaderSize])
	if err != nil {
		t.Fatal(err)
	}
	if h.Root.NBTBID == 0 || h.Root.BBTBID == 0 || h.Root.NBTIB == 0 || h.Root.BBTIB == 0 {
		t.Fatalf("header roots %+v", h.Root)
	}
	if h.BidNextP <= h.Root.NBTBID || h.BidNextP <= h.Root.BBTBID {
		t.Fatalf("bidNextP %d not beyond roots nbt=%d bbt=%d", h.BidNextP, h.Root.NBTBID, h.Root.BBTBID)
	}
	img, err := OpenTrees(file)
	if err != nil {
		t.Fatal(err)
	}
	if img.Pages != nil {
		t.Fatal("OpenTrees must walk the file, not an in-memory page map")
	}
	if img.NBTRoot.BID != h.Root.NBTBID || img.NBTRoot.IB != h.Root.NBTIB {
		t.Fatalf("NBT root %+v header %+v", img.NBTRoot, h.Root)
	}
	if img.BBTRoot.BID != h.Root.BBTBID || img.BBTRoot.IB != h.Root.BBTIB {
		t.Fatalf("BBT root %+v header %+v", img.BBTRoot, h.Root)
	}
	if _, err := img.LookupNode(1); err != nil {
		t.Fatal(err)
	}
	if _, err := img.LookupNode(uint64(MaxNBTLeafEntries + 1)); err != nil {
		t.Fatal(err)
	}
	st, err := LoadStore(file)
	if err != nil {
		t.Fatal(err)
	}
	for _, ref := range []BREF{img.NBTRoot, img.BBTRoot} {
		if !st.allocatedRange(ref.IB, PageSize) {
			t.Fatalf("root IB 0x%x marked free", ref.IB)
		}
		if metadataPageIB(ref.IB) {
			t.Fatalf("root IB 0x%x collides with metadata", ref.IB)
		}
		if ref.IB%uint64(PageSize) != 0 {
			t.Fatalf("root IB 0x%x not aligned", ref.IB)
		}
	}
	parsed, err := disk.ParseBTPage(file[h.Root.NBTIB:h.Root.NBTIB+uint64(PageSize)], disk.FormatUnicode, disk.PageTypeNBT)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Header.Level != 1 {
		t.Fatalf("legacy NBT root level %d", parsed.Header.Level)
	}
	var nids []uint64
	if err := img.WalkNBT(func(e NBTEntry) error {
		nids = append(nids, e.NID)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	legacy := walkLegacyNBT(t, file, img.NBTRoot)
	if len(legacy) != len(nids) {
		t.Fatalf("legacy walk %d nids want %d", len(legacy), len(nids))
	}
	for i := range nids {
		if legacy[i] != nids[i] {
			t.Fatalf("legacy nid[%d]=0x%x want 0x%x", i, legacy[i], nids[i])
		}
	}
}

func TestTreeRebuildFreesSupersededPages(t *testing.T) {
	n := NewNDB(nil)
	blk := mustAlloc(t, n, 8)
	mustNode(t, n, 0x21, blk.BID, 0, 0)
	img1, err := n.Encode()
	if err != nil {
		t.Fatal(err)
	}
	old := make([]uint64, 0, len(img1.Pages))
	for ib := range img1.Pages {
		old = append(old, ib)
	}
	mustNode(t, n, 0x61, blk.BID, 0, 0)
	img2, err := n.Encode()
	if err != nil {
		t.Fatal(err)
	}
	for _, ib := range old {
		if _, keep := img2.Pages[ib]; keep {
			continue
		}
		if n.Store().allocatedRange(ib, PageSize) {
			t.Fatalf("superseded tree page 0x%x leaked", ib)
		}
	}
	file, err := n.Commit()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := OpenTrees(file); err != nil {
		t.Fatal(err)
	}
}

func walkLegacyNBT(t *testing.T, file []byte, root BREF) []uint64 {
	t.Helper()
	var nids []uint64
	var rec func(BREF)
	rec = func(ref BREF) {
		raw := file[ref.IB : ref.IB+uint64(PageSize)]
		p, err := disk.ParseBTPage(raw, disk.FormatUnicode, disk.PageTypeNBT)
		if err != nil {
			t.Fatal(err)
		}
		if p.Header.Level == 0 {
			for _, e := range p.NBTEntries {
				nids = append(nids, e.NID)
			}
			return
		}
		for _, e := range p.NonleafEntries {
			rec(BREF{BID: e.Ref.BID, IB: e.Ref.IB})
		}
	}
	rec(root)
	return nids
}
