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
	if n.Store().allocatedRange(0x5000, BlockDiskSize(16)) {
		t.Fatal("reclaimed PutBlock extent still allocated")
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
	blocks := make([]uint64, 17)
	for i := range blocks {
		blocks[i] = mustAlloc(t, n, 24).BID
	}
	for i, p := range nids {
		mustNode(t, n, uint64(p+1), blocks[p%17], 0, uint32(i))
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
		var alt uint64
		for _, bid := range blocks {
			if bid != e.DataBID {
				alt = bid
				break
			}
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
	wantB := FirstAllocBID + uint64(MaxNBTLeafEntries+1)*BlockBIDIncrement
	if h.BidNextB != wantB {
		t.Fatalf("bidNextB %d want %d", h.BidNextB, wantB)
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

func TestCommitPersistsBidNextB(t *testing.T) {
	n := NewNDB(nil)
	var last uint64
	for i := 0; i < 3; i++ {
		blk := mustAlloc(t, n, 16)
		mustNode(t, n, uint64(0x21+i*0x20), blk.BID, 0, 0)
		last = blk.BID
	}
	file, err := n.Commit()
	if err != nil {
		t.Fatal(err)
	}
	h, err := InspectHeader(file[:UnicodeHeaderSize])
	if err != nil {
		t.Fatal(err)
	}
	want := last + BlockBIDIncrement
	if h.BidNextB != want {
		t.Fatalf("bidNextB %d want %d (last block %d)", h.BidNextB, want, last)
	}
	st, err := LoadStore(file)
	if err != nil {
		t.Fatal(err)
	}
	if st.bidNextB != want || st.HeaderDraft().BidNextB != want {
		t.Fatalf("loaded bidNextB %d want %d", st.bidNextB, want)
	}
}

func TestOpenNDBMutateCommitReopen(t *testing.T) {
	n := NewNDB(nil)
	a := mustAlloc(t, n, 16)
	mustNode(t, n, 0x21, a.BID, 0, 0)
	file1, err := n.Commit()
	if err != nil {
		t.Fatal(err)
	}
	h1, err := InspectHeader(file1[:UnicodeHeaderSize])
	if err != nil {
		t.Fatal(err)
	}
	n2, err := OpenNDB(file1)
	if err != nil {
		t.Fatal(err)
	}
	if n2.Store().nbtRoot.BID != h1.Root.NBTBID || n2.Store().bbtRoot.BID != h1.Root.BBTBID {
		t.Fatalf("reopen lost header roots store=%+v header=%+v", n2.Store().HeaderDraft().Root, h1.Root)
	}
	if n2.ids.nextBlock != h1.BidNextB || n2.Store().bidNextB != h1.BidNextB {
		t.Fatalf("reopen bidNextB ids=%d store=%d header=%d", n2.ids.nextBlock, n2.Store().bidNextB, h1.BidNextB)
	}
	if _, ok := n2.LookupNode(0x21); !ok {
		t.Fatal("reopen missing nid 0x21")
	}
	b := mustAlloc(t, n2, 16)
	if b.BID < h1.BidNextB {
		t.Fatalf("continuation reused block BID %d < bidNextB %d", b.BID, h1.BidNextB)
	}
	mustNode(t, n2, 0x61, b.BID, 0, 0)
	if err := n2.DeleteNode(0x21); err != nil {
		t.Fatal(err)
	}
	file2, err := n2.Commit()
	if err != nil {
		t.Fatal(err)
	}
	if err := CheckAllocation(file2); err != nil {
		t.Fatal(err)
	}
	n3, err := OpenNDB(file2)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := n3.LookupNode(0x21); ok {
		t.Fatal("deleted nid 0x21 survived reopen")
	}
	got, ok := n3.LookupNode(0x61)
	if !ok || got.DataBID != b.BID {
		t.Fatalf("nid 0x61 %+v ok=%v", got, ok)
	}
	if _, ok := n3.LookupBlock(a.BID); ok {
		t.Fatal("unreferenced block a survived reopen")
	}
	if n3.Store().allocatedRange(a.IB, BlockDiskSize(16)) {
		t.Fatal("deleted block a still allocated after reopen")
	}
	c := mustAlloc(t, n3, 16)
	if c.BID == a.BID || c.BID == b.BID {
		t.Fatalf("reopened catalog reused BID %d", c.BID)
	}
	file3, err := n3.Commit()
	if err != nil {
		t.Fatal(err)
	}
	h3, err := InspectHeader(file3[:UnicodeHeaderSize])
	if err != nil {
		t.Fatal(err)
	}
	if h3.BidNextB != c.BID+BlockBIDIncrement {
		t.Fatalf("final bidNextB %d want %d", h3.BidNextB, c.BID+BlockBIDIncrement)
	}
}

func TestPutBlockReservesAndReclaims(t *testing.T) {
	n := NewNDB(nil)
	const ib uint64 = 0x5000
	mustBlock(t, n, 4, ib, 16)
	if !n.Store().allocatedRange(ib, BlockDiskSize(16)) {
		t.Fatal("PutBlock did not reserve Store extent")
	}
	mustNode(t, n, 0x21, 4, 0, 0)
	file, err := n.Commit()
	if err != nil {
		t.Fatal(err)
	}
	if err := CheckAllocation(file); err != nil {
		t.Fatal(err)
	}
	img, err := OpenTrees(file)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := img.LookupBlock(4); err != nil {
		t.Fatal(err)
	}
	if err := n.DeleteNode(0x21); err != nil {
		t.Fatal(err)
	}
	if _, ok := n.LookupBlock(4); ok {
		t.Fatal("final owner did not reclaim PutBlock record")
	}
	if n.Store().allocatedRange(ib, BlockDiskSize(16)) {
		t.Fatal("final owner did not free PutBlock extent")
	}
}

func TestPutBlockRejectsUnmappedOverlapAndMetadata(t *testing.T) {
	n := NewNDB(nil)
	if err := n.PutBlock(BBTEntry{BID: 4, IB: 0x1000, CB: 16}); err == nil {
		t.Fatal("unmapped IB accepted")
	}
	if err := n.PutBlock(BBTEntry{BID: 4, IB: FirstAMapPageOffset, CB: 16}); err == nil {
		t.Fatal("metadata IB accepted")
	}
	if err := n.PutBlock(BBTEntry{BID: 4, IB: 0x5001, CB: 16}); err == nil {
		t.Fatal("unaligned IB accepted")
	}
	live := mustAlloc(t, n, 16)
	if err := n.PutBlock(BBTEntry{BID: live.BID + 8, IB: live.IB, CB: 16}); err == nil {
		t.Fatal("overlapping IB accepted")
	}
	mustBlock(t, n, live.BID+16, 0x5200, 16)
	if err := n.PutBlock(BBTEntry{BID: live.BID + 24, IB: 0x5200, CB: 16}); err == nil {
		t.Fatal("duplicate extent accepted")
	}
}

func TestReopenPreservesAllocatedPayload(t *testing.T) {
	n := NewNDB(nil)
	blk := mustAlloc(t, n, 16)
	mustNode(t, n, 0x21, blk.BID, 0, 0)
	file, err := n.Commit()
	if err != nil {
		t.Fatal(err)
	}
	file[blk.IB] = 0xA5
	n2, err := OpenNDB(file)
	if err != nil {
		t.Fatal(err)
	}
	file2, err := n2.Commit()
	if err != nil {
		t.Fatal(err)
	}
	if file2[blk.IB] != 0xA5 {
		t.Fatalf("reopen commit wiped payload 0x%02x", file2[blk.IB])
	}
	other := mustAlloc(t, n2, 16)
	mustNode(t, n2, 0x61, other.BID, 0, 0)
	file3, err := n2.Commit()
	if err != nil {
		t.Fatal(err)
	}
	if file3[blk.IB] != 0xA5 {
		t.Fatalf("mutate commit wiped payload 0x%02x", file3[blk.IB])
	}
	n3, err := OpenNDB(file3)
	if err != nil {
		t.Fatal(err)
	}
	file4, err := n3.Commit()
	if err != nil {
		t.Fatal(err)
	}
	if file4[blk.IB] != 0xA5 {
		t.Fatalf("second reopen commit wiped payload 0x%02x", file4[blk.IB])
	}
	if err := n3.DeleteNode(0x21); err != nil {
		t.Fatal(err)
	}
	file5, err := n3.Commit()
	if err != nil {
		t.Fatal(err)
	}
	if n3.Store().allocatedRange(blk.IB, BlockDiskSize(16)) {
		t.Fatal("reclaimed block still allocated")
	}
	if file5[blk.IB] != 0 {
		t.Fatalf("freed slot kept payload 0x%02x", file5[blk.IB])
	}
}

func TestReopenExtraRefOwnershipRelease(t *testing.T) {
	n := NewNDB(nil)
	shared := mustAlloc(t, n, 16)
	onlySub := mustAlloc(t, n, 16)
	onlyData := mustAlloc(t, n, 16)
	mustNode(t, n, 0x21, shared.BID, 0, 0)
	if err := n.AddDataTreeRef(shared.BID); err != nil {
		t.Fatal(err)
	}
	if err := n.AddSubnodeRef(shared.BID); err != nil {
		t.Fatal(err)
	}
	if err := n.AddSubnodeRef(onlySub.BID); err != nil {
		t.Fatal(err)
	}
	if err := n.AddDataTreeRef(onlyData.BID); err != nil {
		t.Fatal(err)
	}
	file, err := n.Commit()
	if err != nil {
		t.Fatal(err)
	}
	n2, err := OpenNDB(file)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := n2.LookupBlock(shared.BID)
	if !ok || got.RefCount != 4 {
		t.Fatalf("shared cRef %+v ok=%v", got, ok)
	}
	if n2.dataTreeRefs[shared.BID] != 1 || n2.subnodeRefs[shared.BID] != 1 {
		t.Fatalf("shared extra data=%d sub=%d", n2.dataTreeRefs[shared.BID], n2.subnodeRefs[shared.BID])
	}
	if n2.subnodeRefs[onlySub.BID] != 1 || n2.dataTreeRefs[onlySub.BID] != 0 {
		t.Fatalf("sub-only extra data=%d sub=%d", n2.dataTreeRefs[onlySub.BID], n2.subnodeRefs[onlySub.BID])
	}
	if n2.dataTreeRefs[onlyData.BID] != 1 || n2.subnodeRefs[onlyData.BID] != 0 {
		t.Fatalf("data-only extra data=%d sub=%d", n2.dataTreeRefs[onlyData.BID], n2.subnodeRefs[onlyData.BID])
	}
	if err := n2.ReleaseSubnodeRef(onlySub.BID); err != nil {
		t.Fatal(err)
	}
	if _, ok := n2.LookupBlock(onlySub.BID); ok {
		t.Fatal("subnode-only block not reclaimed after reopen release")
	}
	if err := n2.ReleaseDataTreeRef(onlyData.BID); err != nil {
		t.Fatal(err)
	}
	if _, ok := n2.LookupBlock(onlyData.BID); ok {
		t.Fatal("data-tree-only block not reclaimed after reopen release")
	}
	if err := n2.ReleaseSubnodeRef(shared.BID); err != nil {
		t.Fatal(err)
	}
	got, ok = n2.LookupBlock(shared.BID)
	if !ok || got.RefCount != 3 {
		t.Fatalf("after sub drop %+v ok=%v", got, ok)
	}
	if err := n2.ReleaseDataTreeRef(shared.BID); err != nil {
		t.Fatal(err)
	}
	got, ok = n2.LookupBlock(shared.BID)
	if !ok || got.RefCount != 2 {
		t.Fatalf("after data drop %+v ok=%v", got, ok)
	}
	file2, err := n2.Commit()
	if err != nil {
		t.Fatal(err)
	}
	n3, err := OpenNDB(file2)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := n3.LookupBlock(onlySub.BID); ok {
		t.Fatal("reclaimed subnode block survived second reopen")
	}
	if _, ok := n3.LookupBlock(onlyData.BID); ok {
		t.Fatal("reclaimed data-tree block survived second reopen")
	}
	got, ok = n3.LookupBlock(shared.BID)
	if !ok || got.RefCount != 2 {
		t.Fatalf("shared after reopen %+v ok=%v", got, ok)
	}
	if n3.dataTreeRefs[shared.BID] != 0 || n3.subnodeRefs[shared.BID] != 0 {
		t.Fatalf("shared extras after typed release data=%d sub=%d", n3.dataTreeRefs[shared.BID], n3.subnodeRefs[shared.BID])
	}
}
