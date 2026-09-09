package writer

import (
	"fmt"
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
	ids          *SequentialIDs
	nodes        map[uint64]NBTEntry
	blocks       map[uint64]BBTEntry
	dataTreeRefs map[uint64]int
	subnodeRefs  map[uint64]int
}

// NewNDB returns an empty catalog. Page BIDs come from ids (or SequentialIDs).
func NewNDB(ids *SequentialIDs) *NDB {
	if ids == nil {
		ids = NewSequentialIDs()
	}
	return &NDB{
		ids:          ids,
		nodes:        make(map[uint64]NBTEntry),
		blocks:       make(map[uint64]BBTEntry),
		dataTreeRefs: make(map[uint64]int),
		subnodeRefs:  make(map[uint64]int),
	}
}

// PutBlock registers a data/internal block. cRef starts at 1 (the BBTENTRY).
func (n *NDB) PutBlock(e BBTEntry) error {
	if e.BID == 0 || BIDHasReserved(e.BID) {
		return invalidArg("bid", "invalid block BID 0x%x (MS-PST %s)", e.BID, SectionBID)
	}
	if _, exists := n.blocks[e.BID]; exists {
		return invalidArg("bid", "duplicate BBT BID 0x%x", e.BID)
	}
	e.RefCount = 1
	n.blocks[e.BID] = e
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
		return invalidArg("bid", "no extra ref to drop for BID 0x%x", bid)
	}
	m[bid]--
	if m[bid] == 0 {
		delete(m, bid)
	}
	return n.resyncRef(bid)
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
	return n.dataTreeRefs[bid] + n.subnodeRefs[bid]
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
	if err := n.resyncRef(bid); err != nil {
		return err
	}
	e := n.blocks[bid]
	if e.RefCount <= 1 && n.nbtLiveRefs(bid) == 0 && n.extraLiveRefs(bid) == 0 {
		delete(n.blocks, bid)
	}
	return nil
}

// CheckRefCounts verifies every BBT cRef equals 1 + NBT + data-tree + subnode refs.
func (n *NDB) CheckRefCounts() error {
	seen := make(map[uint64]struct{})
	for bid, e := range n.blocks {
		want := n.computeRef(bid)
		if e.RefCount != want {
			return invariant(SectionRefCount, "cRef", "BID 0x%x cRef %d want %d", bid, e.RefCount, want)
		}
		if e.BID != bid {
			return invariant(SectionBBTENTRY, "bid", "map key 0x%x != entry 0x%x", bid, e.BID)
		}
		seen[bid] = struct{}{}
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

func (n *NDB) sortedBlocks() []BBTEntry {
	out := make([]BBTEntry, 0, len(n.blocks))
	for _, e := range n.blocks {
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

// TreeImage is a serialized NBT+BBT with pages keyed by IB.
type TreeImage struct {
	NBTRoot BREF
	BBTRoot BREF
	Pages   map[uint64][]byte // IB -> 512-byte page
}

// Encode rebuilds Unicode NBT and BBT pages. pageBase is the IB of the first page.
// Page BIDs come from bidNextP (increment 1) and are never the file offset.
func (n *NDB) Encode(pageBase uint64) (*TreeImage, error) {
	if err := n.CheckRefCounts(); err != nil {
		return nil, err
	}
	img := &TreeImage{Pages: make(map[uint64][]byte)}
	nextIB := pageBase
	alloc := func(payload []byte, ptype byte) (BREF, error) {
		bid, err := n.ids.TakePageBID()
		if err != nil {
			return BREF{}, err
		}
		ib := nextIB
		nextIB += uint64(PageSize)
		if bid == ib {
			return BREF{}, invariant(SectionBID, "bid", "page BID 0x%x equals file offset (MS-PST %s)", bid, SectionBID)
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
		return nil, err
	}
	bbtRoot, err := encodeTree(PageBBT, n.sortedBlocks(), alloc, func(chunk []BBTEntry) ([]byte, error) {
		return encodeBTPayload(PageBBT, 0, nil, chunk, nil)
	}, MaxBBTLeafEntries)
	if err != nil {
		return nil, err
	}
	img.NBTRoot = nbtRoot
	img.BBTRoot = bbtRoot
	return img, nil
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

func (img *TreeImage) page(ref BREF) (*BTPageView, error) {
	raw, ok := img.Pages[ref.IB]
	if !ok {
		return nil, invariant(SectionBTPAGE, "ib", "missing page at IB 0x%x", ref.IB)
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

// CheckTrees validates first-key separators, unique page BIDs, and BBT coverage.
func (img *TreeImage) CheckTrees() error {
	seenBID := make(map[uint64]uint64)
	var check func(BREF, byte) (uint64, error)
	check = func(ref BREF, wantType byte) (uint64, error) {
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

// LoadTrees reconstructs an NDB from encoded NBT/BBT leaves.
func LoadTrees(img *TreeImage, ids *SequentialIDs) (*NDB, error) {
	if err := img.CheckTrees(); err != nil {
		return nil, err
	}
	n := NewNDB(ids)
	if err := img.WalkBBT(func(e BBTEntry) error {
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
	// Extra refs are whatever cRef remains after BBTENTRY + NBT refs.
	for bid, e := range n.blocks {
		nbt := n.nbtLiveRefs(bid)
		extra := int(e.RefCount) - 1 - nbt
		if extra < 0 {
			return nil, invariant(SectionRefCount, "cRef", "BID 0x%x cRef %d < 1+NBT %d", bid, e.RefCount, nbt)
		}
		if extra > 0 {
			n.dataTreeRefs[bid] = extra
		}
	}
	return n, nil
}

func (n *NDB) String() string {
	return fmt.Sprintf("ndb nodes=%d blocks=%d", len(n.nodes), len(n.blocks))
}
