package writer

import (
	"fmt"
	"io"
	"os"
	"sort"
)

// extraRef is a BBT reference besides the BBTENTRY itself and NBT bidData/bidSub.
// Data-tree (XBLOCK rgbid) and subnode (SLENTRY) refs are owned here so PST-006
// can increment them without changing BBT layout.
type extraRef byte

const (
	refDataTree extraRef = 1
	refSubnode  extraRef = 2
)

// NDB is an in-memory Node/Block B-tree catalog. Trees are rebuilt as a whole
// on Encode (copy-on-write optimization is later). See MS-PST 2.2.2.7.7.
type NDB struct {
	store        *Store
	ids          *SequentialIDs
	nodes        map[uint64]NBTEntry
	blocks       map[uint64]BBTEntry
	dataTreeRefs map[uint64]int
	subnodeRefs  map[uint64]int
	opaqueRefs   map[uint64]int
	livePages    []uint64
	pendingPath  string
}

// NewNDB returns an empty catalog. Page BIDs and IBs come from store (or a
// new Store). Block BIDs come from ids (or SequentialIDs).
func NewNDB(ids *SequentialIDs) *NDB {
	if ids == nil {
		ids = NewSequentialIDs()
	}
	return &NDB{
		store:        NewStore(),
		ids:          ids,
		nodes:        make(map[uint64]NBTEntry),
		blocks:       make(map[uint64]BBTEntry),
		dataTreeRefs: make(map[uint64]int),
		subnodeRefs:  make(map[uint64]int),
		opaqueRefs:   make(map[uint64]int),
	}
}

// Store returns the allocation map backing this catalog.
func (n *NDB) Store() *Store { return n.store }

// PutBlock registers a data/internal block. cRef starts at 1 (the BBTENTRY).
// The block may be staged with no external owner; Encode/Commit reclaim it.
// IB/CB must name an aligned, non-overlapping Store extent; PutBlock
// Reserves it when free so reclamation can Free it later.
func (n *NDB) PutBlock(e BBTEntry) error {
	if e.BID == 0 || BIDHasReserved(e.BID) {
		return invalidArg("bid", "invalid block BID 0x%x (MS-PST %s)", e.BID, SectionBID)
	}
	if _, exists := n.blocks[e.BID]; exists {
		return invalidArg("bid", "duplicate BBT BID 0x%x", e.BID)
	}
	if err := n.occupyExtent(e); err != nil {
		return err
	}
	e.RefCount = 1
	n.blocks[e.BID] = e
	return nil
}

// AllocBlock allocates a data block in the Store and stages a BBTENTRY.
func (n *NDB) AllocBlock(cb uint16) (BBTEntry, error) {
	size := BlockDiskSize(uint64(cb))
	ib, err := n.store.Allocate(size)
	if err != nil {
		return BBTEntry{}, err
	}
	bid, err := n.ids.TakeBlockBID()
	if err != nil {
		_ = n.store.Free(ib, size)
		return BBTEntry{}, err
	}
	if err := n.store.noteBlockBID(bid); err != nil {
		_ = n.store.Free(ib, size)
		return BBTEntry{}, err
	}
	n.ids.nextBlock = n.store.bidNextB
	e := BBTEntry{BID: bid, IB: ib, CB: cb, RefCount: 1}
	n.blocks[bid] = e
	return e, nil
}

// AllocInternalBlock allocates an XBLOCK/SLBLOCK-style block (bidInternal set).
func (n *NDB) AllocInternalBlock(cb uint16) (BBTEntry, error) {
	size := BlockDiskSize(uint64(cb))
	ib, err := n.store.Allocate(size)
	if err != nil {
		return BBTEntry{}, err
	}
	bid, err := n.ids.TakeInternalBlockBID()
	if err != nil {
		_ = n.store.Free(ib, size)
		return BBTEntry{}, err
	}
	if err := n.store.noteBlockBID(bid); err != nil {
		_ = n.store.Free(ib, size)
		return BBTEntry{}, err
	}
	n.ids.nextBlock = n.store.bidNextB
	e := BBTEntry{BID: bid, IB: ib, CB: cb, RefCount: 1}
	n.blocks[bid] = e
	return e, nil
}

func (n *NDB) putPayload(e BBTEntry, data []byte) error {
	if int(e.CB) != len(data) {
		return invalidArg("cb", "payload %d bytes, BBT cb %d", len(data), e.CB)
	}
	var raw []byte
	var err error
	if BIDIsInternal(e.BID) {
		raw, err = EncodeInternalBlock(data, e.BID, e.IB)
	} else {
		raw, err = EncodeBlock(data, e.BID, e.IB)
	}
	if err != nil {
		return err
	}
	return n.store.writeExtent(e.IB, raw)
}

func (n *NDB) blockPayload(e BBTEntry) ([]byte, error) {
	size := BlockDiskSize(uint64(e.CB))
	if n.store == nil || e.IB == 0 {
		return nil, invariant(SectionBlockTrailer, "ib", "missing payload for BID 0x%x at 0x%x", e.BID, e.IB)
	}
	raw, err := n.store.readExtent(e.IB, int(size))
	if err != nil {
		return nil, err
	}
	v, err := InspectBlock(raw, e.IB)
	if err != nil {
		return nil, err
	}
	if v.BID != e.BID {
		return nil, invariant(SectionBID, "bid", "payload BID 0x%x want 0x%x", v.BID, e.BID)
	}
	return v.Plain, nil
}

func (n *NDB) occupyExtent(e BBTEntry) error {
	if e.IB == 0 {
		return invalidArg("ib", "block IB is null (MS-PST %s)", SectionBBTENTRY)
	}
	if e.IB%BytesPerSlot != 0 {
		return invalidArg("ib", "offset 0x%x is not %d-byte aligned", e.IB, BytesPerSlot)
	}
	size := BlockDiskSize(uint64(e.CB))
	if size == 0 || size > MaxAllocBytes {
		return invalidArg("cb", "block disk size %d is invalid (cb=%d)", size, e.CB)
	}
	if n.store.coversMetadata(e.IB, size) {
		return invalidArg("ib", "block at 0x%x overlaps metadata (MS-PST %s)", e.IB, SectionAMap)
	}
	for _, p := range n.livePages {
		if e.IB < p+uint64(PageSize) && e.IB+size > p {
			return invalidArg("ib", "block at 0x%x overlaps tree page 0x%x", e.IB, p)
		}
	}
	for bid, other := range n.blocks {
		if bid == e.BID {
			continue
		}
		osize := BlockDiskSize(uint64(other.CB))
		if e.IB < other.IB+osize && e.IB+size > other.IB {
			return invalidArg("ib", "block 0x%x at 0x%x overlaps BID 0x%x at 0x%x", e.BID, e.IB, other.BID, other.IB)
		}
	}
	if !n.store.allocatedRange(e.IB, size) {
		if err := n.store.Reserve(e.IB, size); err != nil {
			return err
		}
	}
	if err := n.store.noteBlockBID(e.BID); err != nil {
		return err
	}
	n.ids.nextBlock = n.store.bidNextB
	return nil
}

// PutNode inserts or replaces an NBT entry and adjusts BBT reference counts.
func (n *NDB) PutNode(e NBTEntry) error {
	if e.NID == 0 || e.NID>>32 != 0 {
		return invalidArg("nid", "invalid NID 0x%x (MS-PST %s)", e.NID, SectionNBTENTRY)
	}
	for _, bid := range []uint64{e.DataBID, e.SubBID} {
		if bid == 0 {
			continue
		}
		if _, ok := n.blocks[bid]; !ok {
			return invalidArg("bid", "NBT references missing BID 0x%x (MS-PST %s)", bid, SectionRefCount)
		}
	}
	old, exists := n.nodes[e.NID]
	n.nodes[e.NID] = e
	if exists {
		if err := n.resyncAfterDrop(old.DataBID); err != nil {
			return err
		}
		if err := n.resyncAfterDrop(old.SubBID); err != nil {
			return err
		}
	}
	if err := n.resyncRef(e.DataBID); err != nil {
		return err
	}
	return n.resyncRef(e.SubBID)
}

// DeleteNode removes an NBT entry and releases its block references.
func (n *NDB) DeleteNode(nid uint64) error {
	old, ok := n.nodes[nid]
	if !ok {
		return invalidArg("nid", "missing NID 0x%x", nid)
	}
	delete(n.nodes, nid)
	if err := n.resyncAfterDrop(old.DataBID); err != nil {
		return err
	}
	return n.resyncAfterDrop(old.SubBID)
}

// LookupNode returns the in-memory NBT entry.
func (n *NDB) LookupNode(nid uint64) (NBTEntry, bool) {
	e, ok := n.nodes[nid]
	return e, ok
}

// LookupBlock returns the in-memory BBT entry.
func (n *NDB) LookupBlock(bid uint64) (BBTEntry, bool) {
	e, ok := n.blocks[bid]
	return e, ok
}

// AddDataTreeRef records an XBLOCK/XXBLOCK rgbid reference (MS-PST 2.2.2.7.7.3.1).
func (n *NDB) AddDataTreeRef(bid uint64) error { return n.addExtra(bid, refDataTree) }

// AddSubnodeRef records an SLENTRY bidData/bidSub reference.
func (n *NDB) AddSubnodeRef(bid uint64) error { return n.addExtra(bid, refSubnode) }

// ReleaseDataTreeRef drops an XBLOCK rgbid reference and reclaims at cRef==1.
func (n *NDB) ReleaseDataTreeRef(bid uint64) error { return n.releaseExtra(bid, refDataTree) }

// ReleaseSubnodeRef drops an SLENTRY reference and reclaims at cRef==1.
func (n *NDB) ReleaseSubnodeRef(bid uint64) error { return n.releaseExtra(bid, refSubnode) }

func (n *NDB) extraMap(kind extraRef) map[uint64]int {
	if kind == refDataTree {
		return n.dataTreeRefs
	}
	return n.subnodeRefs
}

func (n *NDB) addExtra(bid uint64, kind extraRef) error {
	if _, ok := n.blocks[bid]; !ok {
		return invalidArg("bid", "data-tree/subnode ref to missing BID 0x%x", bid)
	}
	n.extraMap(kind)[bid]++
	return n.resyncRef(bid)
}

func (n *NDB) releaseExtra(bid uint64, kind extraRef) error {
	m := n.extraMap(kind)
	if m[bid] <= 0 {
		if n.opaqueRefs[bid] > 0 {
			return invalidArg("bid", "reopened extra refs for BID 0x%x are opaque until PST-006 reconstructs XBLOCK/SLENTRY (MS-PST %s)", bid, SectionRefCount)
		}
		return invalidArg("bid", "no extra ref to drop for BID 0x%x", bid)
	}
	m[bid]--
	if m[bid] == 0 {
		delete(m, bid)
	}
	return n.resyncAfterDrop(bid)
}

func (n *NDB) nbtLiveRefs(bid uint64) int {
	if bid == 0 {
		return 0
	}
	var c int
	for _, e := range n.nodes {
		if e.DataBID == bid {
			c++
		}
		if e.SubBID == bid {
			c++
		}
	}
	return c
}

func (n *NDB) extraLiveRefs(bid uint64) int {
	return n.dataTreeRefs[bid] + n.subnodeRefs[bid] + n.opaqueRefs[bid]
}

func (n *NDB) computeRef(bid uint64) uint16 {
	// BBTENTRY holds 1; NBT bidData/bidSub, SLENTRY, and XBLOCK rgbid add more.
	return uint16(1 + n.nbtLiveRefs(bid) + n.extraLiveRefs(bid))
}

func (n *NDB) resyncRef(bid uint64) error {
	if bid == 0 {
		return nil
	}
	e, ok := n.blocks[bid]
	if !ok {
		return invalidArg("bid", "missing BID 0x%x", bid)
	}
	e.RefCount = n.computeRef(bid)
	n.blocks[bid] = e
	return nil
}

func (n *NDB) resyncAfterDrop(bid uint64) error {
	if bid == 0 {
		return nil
	}
	if _, ok := n.blocks[bid]; !ok {
		return nil
	}
	if err := n.resyncRef(bid); err != nil {
		return err
	}
	if n.nbtLiveRefs(bid) == 0 && n.extraLiveRefs(bid) == 0 {
		return n.dropBlock(bid)
	}
	return nil
}

func (n *NDB) dropBlock(bid uint64) error {
	e, ok := n.blocks[bid]
	if !ok {
		return nil
	}
	kids, kerr := n.treeChildRefs(e)
	delete(n.blocks, bid)
	delete(n.dataTreeRefs, bid)
	delete(n.subnodeRefs, bid)
	delete(n.opaqueRefs, bid)
	if err := n.freeBlockIB(e); err != nil {
		return err
	}
	if kerr != nil {
		return kerr
	}
	for _, k := range kids {
		if n.extraMap(k.kind)[k.bid] <= 0 {
			continue
		}
		if err := n.releaseExtra(k.bid, k.kind); err != nil {
			return err
		}
	}
	return nil
}

type treeChild struct {
	bid  uint64
	kind extraRef
}

func (n *NDB) treeChildRefs(e BBTEntry) ([]treeChild, error) {
	if !BIDIsInternal(e.BID) {
		return nil, nil
	}
	data, err := n.blockPayload(e)
	if err != nil {
		return nil, nil
	}
	if len(data) < 2 {
		return nil, nil
	}
	switch data[0] {
	case BlockTypeXBlock:
		xb, err := InspectXBlock(data)
		if err != nil {
			return nil, err
		}
		out := make([]treeChild, len(xb.BIDs))
		for i, bid := range xb.BIDs {
			out[i] = treeChild{bid: bid, kind: refDataTree}
		}
		return out, nil
	case BlockTypeSubnode:
		sn, err := InspectSubnodeBlock(data)
		if err != nil {
			return nil, err
		}
		var out []treeChild
		if sn.Level == 0 {
			for _, ent := range sn.Leaves {
				if ent.DataBID != 0 {
					out = append(out, treeChild{bid: ent.DataBID, kind: refSubnode})
				}
				if ent.SubBID != 0 {
					out = append(out, treeChild{bid: ent.SubBID, kind: refSubnode})
				}
			}
		} else {
			for _, k := range sn.Kids {
				out = append(out, treeChild{bid: k.Ref, kind: refSubnode})
			}
		}
		return out, nil
	default:
		return nil, nil
	}
}

func (n *NDB) freeBlockIB(e BBTEntry) error {
	if n.store == nil || e.IB == 0 {
		return nil
	}
	size := BlockDiskSize(uint64(e.CB))
	if size == 0 {
		return nil
	}
	if !n.store.allocatedRange(e.IB, size) {
		return invariant(SectionAMap, "ib", "reclaim of BID 0x%x at 0x%x+%d is not allocated", e.BID, e.IB, size)
	}
	return n.store.Free(e.IB, size)
}

func (n *NDB) reclaimOrphans() error {
	var drop []uint64
	for bid := range n.blocks {
		if n.nbtLiveRefs(bid) == 0 && n.extraLiveRefs(bid) == 0 {
			drop = append(drop, bid)
		}
	}
	for _, bid := range drop {
		if err := n.dropBlock(bid); err != nil {
			return err
		}
	}
	return nil
}

// CheckRefCounts verifies every BBT cRef equals 1 + NBT + data-tree + subnode refs.
func (n *NDB) CheckRefCounts() error {
	for bid, e := range n.blocks {
		want := n.computeRef(bid)
		if e.RefCount != want {
			return invariant(SectionRefCount, "cRef", "BID 0x%x cRef %d want %d", bid, e.RefCount, want)
		}
		if e.BID != bid {
			return invariant(SectionBBTENTRY, "bid", "map key 0x%x != entry 0x%x", bid, e.BID)
		}
	}
	for _, e := range n.nodes {
		for _, bid := range []uint64{e.DataBID, e.SubBID} {
			if bid == 0 {
				continue
			}
			if _, ok := n.blocks[bid]; !ok {
				return invariant(SectionRefCount, "bid", "NBT 0x%x references missing BID 0x%x", e.NID, bid)
			}
		}
	}
	return nil
}

func (n *NDB) sortedNodes() []NBTEntry {
	out := make([]NBTEntry, 0, len(n.nodes))
	for _, e := range n.nodes {
		out = append(out, e)
	}
	sortNBT(out)
	return out
}

func (n *NDB) ownedBlocks() []BBTEntry {
	out := make([]BBTEntry, 0, len(n.blocks))
	for _, e := range n.blocks {
		if n.nbtLiveRefs(e.BID)+n.extraLiveRefs(e.BID) == 0 {
			continue
		}
		out = append(out, e)
	}
	sortBBT(out)
	return out
}

func sortNBT(s []NBTEntry) {
	sort.Slice(s, func(i, j int) bool { return s[i].NID < s[j].NID })
}

func sortBBT(s []BBTEntry) {
	sort.Slice(s, func(i, j int) bool { return s[i].BID < s[j].BID })
}

// TreeImage is a serialized NBT+BBT. Pages is filled by Encode; File is the
// persisted PST used by OpenTrees (walk follows header ROOT BREFs).
type TreeImage struct {
	NBTRoot BREF
	BBTRoot BREF
	Pages   map[uint64][]byte // IB -> 512-byte page
	File    []byte
	src     io.ReaderAt
	srcSize uint64
}

func metadataPageIB(ib uint64) bool {
	if ib == DListPageOffset {
		return true
	}
	idx, ok := AMapIndexForOffset(ib)
	if !ok {
		return true
	}
	for _, p := range RegionMapPages(idx) {
		if p.Offset == ib {
			return true
		}
	}
	return false
}

// Encode rebuilds Unicode NBT and BBT pages. Each page is reserved in the
// AMap; page BIDs come from bidNextP (increment 1) and are never the file
// offset. Superseded tree pages are freed after the new tree is committed.
// Unowned (cRef==1) BBT records are reclaimed and not serialized.
func (n *NDB) Encode() (*TreeImage, error) {
	if err := n.reclaimOrphans(); err != nil {
		return nil, err
	}
	if err := n.CheckRefCounts(); err != nil {
		return nil, err
	}
	img := &TreeImage{Pages: make(map[uint64][]byte)}
	var newPages []uint64
	rollback := func() {
		for _, ib := range newPages {
			_ = n.store.Free(ib, PageSize)
		}
	}
	alloc := func(payload []byte, ptype byte) (BREF, error) {
		ib, err := n.store.AllocatePage()
		if err != nil {
			return BREF{}, err
		}
		newPages = append(newPages, ib)
		bid := n.store.takePageBID()
		if bid == ib {
			return BREF{}, invariant(SectionBID, "bid", "page BID 0x%x equals file offset (MS-PST %s)", bid, SectionBID)
		}
		if ib%uint64(PageSize) != 0 {
			return BREF{}, invariant(SectionBTPAGE, "ib", "page IB 0x%x is not 512-byte aligned", ib)
		}
		if metadataPageIB(ib) {
			return BREF{}, invariant(SectionAMap, "ib", "tree page IB 0x%x collides with metadata", ib)
		}
		raw, err := EncodePage(payload, ptype, bid, ib)
		if err != nil {
			return BREF{}, err
		}
		img.Pages[ib] = raw
		return BREF{BID: bid, IB: ib}, nil
	}

	nbtRoot, err := encodeTree(PageNBT, n.sortedNodes(), alloc, func(chunk []NBTEntry) ([]byte, error) {
		return encodeBTPayload(PageNBT, 0, chunk, nil, nil)
	}, MaxNBTLeafEntries)
	if err != nil {
		rollback()
		return nil, err
	}
	bbtRoot, err := encodeTree(PageBBT, n.ownedBlocks(), alloc, func(chunk []BBTEntry) ([]byte, error) {
		return encodeBTPayload(PageBBT, 0, nil, chunk, nil)
	}, MaxBBTLeafEntries)
	if err != nil {
		rollback()
		return nil, err
	}
	img.NBTRoot = nbtRoot
	img.BBTRoot = bbtRoot

	for _, ib := range n.livePages {
		if err := n.store.Free(ib, PageSize); err != nil {
			rollback()
			return nil, err
		}
	}
	n.livePages = newPages
	n.store.SetTreeRoots(img.NBTRoot, img.BBTRoot)
	return img, nil
}

// Commit writes a Unicode PST: maps, HEADER ROOT BREFNBT/BREFBBT, bidNextP,
// bidNextB, and the encoded NBT/BBT pages. Reopen with OpenNDB.
func (n *NDB) Commit() ([]byte, error) {
	ms := NewMemSink("commit")
	if err := n.CommitTo(ms); err != nil {
		return nil, err
	}
	return ms.Bytes(), nil
}

func (n *NDB) CommitTo(dst Sink) error {
	if err := n.rejectInPlace(dst); err != nil {
		return err
	}
	snap, err := n.capture()
	if err != nil {
		return err
	}
	defer snap.release()
	img, err := n.Encode()
	if err != nil {
		_ = n.restore(snap)
		return err
	}
	if err := n.writeCommit(dst, img); err != nil {
		_ = n.restore(snap)
		return err
	}
	return n.adopt(dst, lifeBorrowed)
}

// PendingPath is a CommitFile path that is durable on disk but not yet
// adopted as store.io (dirsync/reopen failure). Empty after a successful adopt.
func (n *NDB) PendingPath() string {
	if n == nil {
		return ""
	}
	return n.pendingPath
}

// CommitFile writes a two-phase PST to a sibling temp file, syncs, renames,
// then fsyncs the parent directory. A crash before rename leaves the previous
// file. After rename the new file is the committed image even if reopen fails.
func (n *NDB) CommitFile(path string) error {
	if path == "" {
		return invalidArg("path", "empty commit path")
	}
	tmp := path + ".tmp"
	dst, err := CreateFileSink(tmp)
	if err != nil {
		return ioErr("file", "create %s: %v", tmp, err)
	}
	snap, err := n.capture()
	if err != nil {
		_ = dst.Close()
		_ = os.Remove(tmp)
		return err
	}
	defer snap.release()
	img, err := n.Encode()
	if err != nil {
		_ = n.restore(snap)
		_ = dst.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := n.writeCommit(dst, img); err != nil {
		_ = n.restore(snap)
		_ = dst.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := closeSink(dst); err != nil {
		_ = n.restore(snap)
		_ = os.Remove(tmp)
		return ioErr("file", "close %s: %v", tmp, err)
	}
	if err := replacePath(tmp, path); err != nil {
		_ = n.restore(snap)
		_ = os.Remove(tmp)
		return ioErr("file", "rename %s -> %s: %v", tmp, path, err)
	}
	if err := syncDir(path); err != nil {
		return n.failAdopt(path, err)
	}
	sk, err := openCommitted(path)
	if err != nil {
		return n.failAdopt(path, err)
	}
	return n.adopt(sk, lifeClose)
}

func encodeTree[T interface{ key() uint64 }](ptype byte, leaves []T, alloc func([]byte, byte) (BREF, error), encodeLeaf func([]T) ([]byte, error), maxLeaf int) (BREF, error) {
	if maxLeaf < 1 {
		return BREF{}, invalidArg("cEntMax", "max leaf entries %d", maxLeaf)
	}
	var childRefs []BTEntry
	if len(leaves) == 0 {
		payload, err := encodeLeaf(nil)
		if err != nil {
			return BREF{}, err
		}
		return alloc(payload, ptype)
	}
	for i := 0; i < len(leaves); i += maxLeaf {
		end := i + maxLeaf
		if end > len(leaves) {
			end = len(leaves)
		}
		chunk := leaves[i:end]
		payload, err := encodeLeaf(chunk)
		if err != nil {
			return BREF{}, err
		}
		ref, err := alloc(payload, ptype)
		if err != nil {
			return BREF{}, err
		}
		childRefs = append(childRefs, BTEntry{Key: chunk[0].key(), Ref: ref})
	}
	level := byte(0)
	for len(childRefs) > 1 {
		level++
		var next []BTEntry
		for i := 0; i < len(childRefs); i += MaxBTNonleafEntries {
			end := i + MaxBTNonleafEntries
			if end > len(childRefs) {
				end = len(childRefs)
			}
			chunk := childRefs[i:end]
			payload, err := encodeBTPayload(ptype, level, nil, nil, chunk)
			if err != nil {
				return BREF{}, err
			}
			ref, err := alloc(payload, ptype)
			if err != nil {
				return BREF{}, err
			}
			next = append(next, BTEntry{Key: chunk[0].Key, Ref: ref})
		}
		childRefs = next
	}
	return childRefs[0].Ref, nil
}

func (img *TreeImage) rawAt(ib uint64) ([]byte, error) {
	if raw, ok := img.Pages[ib]; ok {
		return raw, nil
	}
	if img.File != nil && ib+uint64(PageSize) <= uint64(len(img.File)) {
		return img.File[ib : ib+uint64(PageSize)], nil
	}
	if img.src != nil && ib+uint64(PageSize) <= img.srcSize {
		raw, err := readAtFull(img.src, int64(ib), PageSize)
		if err != nil {
			return nil, invariant(SectionBTPAGE, "ib", "read page at 0x%x: %v", ib, err)
		}
		return raw, nil
	}
	return nil, invariant(SectionBTPAGE, "ib", "missing page at IB 0x%x", ib)
}

func (img *TreeImage) page(ref BREF) (*BTPageView, error) {
	raw, err := img.rawAt(ref.IB)
	if err != nil {
		return nil, err
	}
	v, err := InspectBTPage(raw, ref.IB)
	if err != nil {
		return nil, err
	}
	if v.Page.BID != ref.BID {
		return nil, invariant(SectionBID, "bid", "page BID 0x%x want 0x%x", v.Page.BID, ref.BID)
	}
	return v, nil
}

// LookupNode walks the encoded NBT.
func (img *TreeImage) LookupNode(nid uint64) (NBTEntry, error) {
	v, err := img.walk(img.NBTRoot, nid)
	if err != nil {
		return NBTEntry{}, err
	}
	for _, e := range v.NBT {
		if e.NID == nid {
			return e, nil
		}
	}
	return NBTEntry{}, invariant(SectionNBTENTRY, "nid", "NID 0x%x not found", nid)
}

// LookupBlock walks the encoded BBT.
func (img *TreeImage) LookupBlock(bid uint64) (BBTEntry, error) {
	v, err := img.walk(img.BBTRoot, bid)
	if err != nil {
		return BBTEntry{}, err
	}
	for _, e := range v.BBT {
		if e.BID == bid {
			return e, nil
		}
	}
	return BBTEntry{}, invariant(SectionBBTENTRY, "bid", "BID 0x%x not found", bid)
}

func (img *TreeImage) walk(root BREF, key uint64) (*BTPageView, error) {
	v, err := img.page(root)
	if err != nil {
		return nil, err
	}
	for v.Level > 0 {
		i, ok := chooseChild(v.Kids, key)
		if !ok {
			return nil, invariant(SectionBTENTRY, "btkey", "key 0x%x is below first child key", key)
		}
		v, err = img.page(v.Kids[i].Ref)
		if err != nil {
			return nil, err
		}
	}
	return v, nil
}

// WalkNBT visits encoded NBT leaves in key order.
func (img *TreeImage) WalkNBT(fn func(NBTEntry) error) error {
	return img.walkLeaves(img.NBTRoot, func(v *BTPageView) error {
		for _, e := range v.NBT {
			if err := fn(e); err != nil {
				return err
			}
		}
		return nil
	})
}

// WalkBBT visits encoded BBT leaves in key order.
func (img *TreeImage) WalkBBT(fn func(BBTEntry) error) error {
	return img.walkLeaves(img.BBTRoot, func(v *BTPageView) error {
		for _, e := range v.BBT {
			if err := fn(e); err != nil {
				return err
			}
		}
		return nil
	})
}

func (img *TreeImage) walkLeaves(root BREF, fn func(*BTPageView) error) error {
	v, err := img.page(root)
	if err != nil {
		return err
	}
	if v.Level == 0 {
		return fn(v)
	}
	for _, k := range v.Kids {
		if err := img.walkLeaves(k.Ref, fn); err != nil {
			return err
		}
	}
	return nil
}

// CheckTrees validates first-key separators, unique aligned page IBs/BIDs, and BBT coverage.
func (img *TreeImage) CheckTrees() error {
	seenBID := make(map[uint64]uint64)
	seenIB := make(map[uint64]struct{})
	var check func(BREF, byte) (uint64, error)
	check = func(ref BREF, wantType byte) (uint64, error) {
		if ref.IB%uint64(PageSize) != 0 {
			return 0, invariant(SectionBTPAGE, "ib", "page IB 0x%x is not 512-byte aligned", ref.IB)
		}
		if metadataPageIB(ref.IB) {
			return 0, invariant(SectionAMap, "ib", "tree page IB 0x%x collides with metadata", ref.IB)
		}
		if _, dup := seenIB[ref.IB]; dup {
			return 0, invariant(SectionBTPAGE, "ib", "duplicate page IB 0x%x", ref.IB)
		}
		seenIB[ref.IB] = struct{}{}
		v, err := img.page(ref)
		if err != nil {
			return 0, err
		}
		if v.Type != wantType {
			return 0, invariant(SectionBTPAGE, "ptype", "got 0x%02x want 0x%02x", v.Type, wantType)
		}
		if v.Page.BID == ref.IB {
			return 0, invariant(SectionBID, "bid", "page BID equals IB 0x%x", ref.IB)
		}
		if prev, ok := seenBID[v.Page.BID]; ok {
			return 0, invariant(SectionBID, "bid", "duplicate page BID 0x%x (IB 0x%x and 0x%x)", v.Page.BID, prev, ref.IB)
		}
		seenBID[v.Page.BID] = ref.IB
		if v.Level == 0 {
			if v.Count == 0 {
				return 0, nil
			}
			return firstKey(v)
		}
		var childFirst uint64
		for i, k := range v.Kids {
			fk, err := check(k.Ref, wantType)
			if err != nil {
				return 0, err
			}
			if k.Key != fk {
				return 0, invariant(SectionBTENTRY, "btkey", "separator 0x%x is not child first key 0x%x (MS-PST %s)", k.Key, fk, SectionBTENTRY)
			}
			if i == 0 {
				childFirst = fk
			}
		}
		return childFirst, nil
	}
	if _, err := check(img.NBTRoot, PageNBT); err != nil {
		return err
	}
	if _, err := check(img.BBTRoot, PageBBT); err != nil {
		return err
	}

	bbt := make(map[uint64]BBTEntry)
	if err := img.WalkBBT(func(e BBTEntry) error {
		if _, dup := bbt[e.BID]; dup {
			return invariant(SectionBBTENTRY, "bid", "duplicate BBT BID 0x%x", e.BID)
		}
		if e.RefCount < 2 {
			return invariant(SectionRefCount, "cRef", "orphan BBT cRef=%d for BID 0x%x (MS-PST %s)", e.RefCount, e.BID, SectionRefCount)
		}
		bbt[e.BID] = e
		return nil
	}); err != nil {
		return err
	}
	nbtRefs := make(map[uint64]int)
	if err := img.WalkNBT(func(e NBTEntry) error {
		for _, bid := range []uint64{e.DataBID, e.SubBID} {
			if bid == 0 {
				continue
			}
			if _, ok := bbt[bid]; !ok {
				return invariant(SectionRefCount, "bid", "NBT 0x%x references missing BID 0x%x", e.NID, bid)
			}
			nbtRefs[bid]++
		}
		return nil
	}); err != nil {
		return err
	}
	for bid, e := range bbt {
		min := uint16(1 + nbtRefs[bid])
		if e.RefCount < min {
			return invariant(SectionRefCount, "cRef", "BID 0x%x cRef %d < 1+NBT %d", bid, e.RefCount, nbtRefs[bid])
		}
	}
	return nil
}

// RootLevel returns the cLevel of the encoded root page.
func (img *TreeImage) RootLevel(root BREF) (byte, error) {
	v, err := img.page(root)
	if err != nil {
		return 0, err
	}
	return v.Level, nil
}

// OpenTrees reconstructs a TreeImage by following HEADER ROOT BREFNBT/BREFBBT.
func OpenTrees(file []byte) (*TreeImage, error) {
	return OpenTreesFrom(&MemSink{name: "trees", buf: file}, int64(len(file)))
}

func OpenTreesFrom(r io.ReaderAt, size int64) (*TreeImage, error) {
	if r == nil {
		return nil, invalidArg("reader", "nil tree reader")
	}
	if size < UnicodeHeaderSize {
		return nil, invariant(SectionHeader, "size", "file is %d bytes, need header", size)
	}
	hdr, err := readAtFull(r, 0, UnicodeHeaderSize)
	if err != nil {
		return nil, invariant(SectionHeader, "size", "header read: %v", err)
	}
	h, err := InspectHeader(hdr)
	if err != nil {
		return nil, err
	}
	if h.Root.NBTBID == 0 || h.Root.BBTBID == 0 {
		return nil, invariant(SectionRoot, "bref", "header NBT/BBT BREF is null (MS-PST %s)", SectionRoot)
	}
	img := &TreeImage{
		NBTRoot: BREF{BID: h.Root.NBTBID, IB: h.Root.NBTIB},
		BBTRoot: BREF{BID: h.Root.BBTBID, IB: h.Root.BBTIB},
		src:     r,
		srcSize: uint64(size),
	}
	if err := img.CheckTrees(); err != nil {
		return nil, err
	}
	return img, nil
}

func (img *TreeImage) collectPageIBs() ([]uint64, error) {
	var ibs []uint64
	var rec func(BREF) error
	rec = func(ref BREF) error {
		v, err := img.page(ref)
		if err != nil {
			return err
		}
		ibs = append(ibs, ref.IB)
		if v.Level == 0 {
			return nil
		}
		for _, k := range v.Kids {
			if err := rec(k.Ref); err != nil {
				return err
			}
		}
		return nil
	}
	if err := rec(img.NBTRoot); err != nil {
		return nil, err
	}
	if err := rec(img.BBTRoot); err != nil {
		return nil, err
	}
	return ibs, nil
}

func idsFromStore(s *Store) *SequentialIDs {
	ids := NewSequentialIDs()
	ids.nextBlock = s.bidNextB
	ids.nextPage = s.bidNextP
	return ids
}

func hydrateNDB(n *NDB, img *TreeImage) error {
	if err := img.WalkBBT(func(e BBTEntry) error {
		n.blocks[e.BID] = e
		return n.store.noteBlockBID(e.BID)
	}); err != nil {
		return err
	}
	if err := img.WalkNBT(func(e NBTEntry) error {
		n.nodes[e.NID] = e
		return nil
	}); err != nil {
		return err
	}
	if err := n.reconstructTreeRefs(); err != nil {
		return err
	}
	if err := n.applyExtraRefs(); err != nil {
		return err
	}
	pages, err := img.collectPageIBs()
	if err != nil {
		return err
	}
	n.livePages = pages
	n.ids.nextBlock = n.store.bidNextB
	n.ids.nextPage = n.store.bidNextP
	return n.CheckRefCounts()
}

// OpenNDB reopens a committed Unicode PST for continued mutation.
// Store maps, ROOT BREFs, live tree pages, and bidNextP/bidNextB
// are retained. Extra cRef is reconstructed from XBLOCK/SLENTRY when present.
func OpenNDB(file []byte) (*NDB, error) {
	return OpenNDBFrom(&MemSink{name: "load", buf: file}, int64(len(file)))
}

func OpenNDBFile(path string) (*NDB, error) {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return nil, ioErr("file", "open %s: %v", path, err)
	}
	st, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, ioErr("file", "stat %s: %v", path, err)
	}
	n, err := OpenNDBFrom(&FileSink{f: f}, st.Size())
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	n.store.ownClose()
	return n, nil
}

func OpenNDBFrom(r io.ReaderAt, size int64) (*NDB, error) {
	store, err := LoadStoreFrom(r, size)
	if err != nil {
		return nil, err
	}
	img, err := OpenTreesFrom(r, size)
	if err != nil {
		return nil, err
	}
	n := &NDB{
		store:        store,
		ids:          idsFromStore(store),
		nodes:        make(map[uint64]NBTEntry),
		blocks:       make(map[uint64]BBTEntry),
		dataTreeRefs: make(map[uint64]int),
		subnodeRefs:  make(map[uint64]int),
		opaqueRefs:   make(map[uint64]int),
	}
	if err := hydrateNDB(n, img); err != nil {
		return nil, err
	}
	return n, nil
}

func (n *NDB) Close() error {
	if n == nil || n.store == nil {
		return nil
	}
	return n.store.Close()
}

// LoadTrees reconstructs an NDB from encoded NBT/BBT leaves.
// A persisted img.File reopens through OpenNDB so Store maps survive.
func LoadTrees(img *TreeImage, ids *SequentialIDs) (*NDB, error) {
	if img != nil && img.File != nil {
		return OpenNDB(img.File)
	}
	if err := img.CheckTrees(); err != nil {
		return nil, err
	}
	n := NewNDB(ids)
	if err := img.WalkBBT(func(e BBTEntry) error {
		if err := n.occupyExtent(e); err != nil {
			return err
		}
		n.blocks[e.BID] = e
		return nil
	}); err != nil {
		return nil, err
	}
	if err := img.WalkNBT(func(e NBTEntry) error {
		n.nodes[e.NID] = e
		return nil
	}); err != nil {
		return nil, err
	}
	if err := n.applyExtraRefs(); err != nil {
		return nil, err
	}
	return n, n.CheckRefCounts()
}

func (n *NDB) reconstructTreeRefs() error {
	for bid, e := range n.blocks {
		if !BIDIsInternal(bid) {
			continue
		}
		data, err := n.blockPayload(e)
		if err != nil {
			continue
		}
		if len(data) < 1 {
			continue
		}
		switch data[0] {
		case BlockTypeXBlock:
			xb, err := InspectXBlock(data)
			if err != nil {
				return err
			}
			if err := n.validateDataTree(bid); err != nil {
				return err
			}
			for _, child := range xb.BIDs {
				n.dataTreeRefs[child]++
			}
		case BlockTypeSubnode:
			if err := n.validateSubnodeTree(bid); err != nil {
				return err
			}
			sn, err := InspectSubnodeBlock(data)
			if err != nil {
				return err
			}
			if sn.Level == 0 {
				for _, ent := range sn.Leaves {
					if ent.DataBID != 0 {
						n.subnodeRefs[ent.DataBID]++
					}
					if ent.SubBID != 0 {
						n.subnodeRefs[ent.SubBID]++
					}
				}
			} else {
				for _, k := range sn.Kids {
					n.subnodeRefs[k.Ref]++
				}
			}
		}
	}
	return nil
}

func (n *NDB) applyExtraRefs() error {
	for bid, e := range n.blocks {
		nbt := n.nbtLiveRefs(bid)
		extra := int(e.RefCount) - 1 - nbt
		if extra < 0 {
			return invariant(SectionRefCount, "cRef", "BID 0x%x cRef %d < 1+NBT %d", bid, e.RefCount, nbt)
		}
		typed := n.dataTreeRefs[bid] + n.subnodeRefs[bid]
		if typed > extra {
			return invariant(SectionRefCount, "cRef", "BID 0x%x typed extra %d > leftover %d", bid, typed, extra)
		}
		if opaque := extra - typed; opaque > 0 {
			n.opaqueRefs[bid] = opaque
		}
	}
	return nil
}

func (n *NDB) String() string {
	return fmt.Sprintf("ndb nodes=%d blocks=%d", len(n.nodes), len(n.blocks))
}
