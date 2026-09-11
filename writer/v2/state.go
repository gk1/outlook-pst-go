package writer

// storeState is the one cloneable value-state for Store's mutable allocator
// fields. Capture and restore copy this struct; io/work/hold/undo stay
// outside the snapshot. See PST-009.
type storeState struct {
	regions       []region
	lastAllocAMap uint32
	valid         byte
	unique        uint32
	bidNextP      uint64
	bidNextB      uint64
	dlistBID      uint64
	nbtRoot       BREF
	bbtRoot       BREF
	nids          [32]uint32
}

func (s storeState) clone() storeState {
	s.regions = cloneRegions(s.regions)
	return s
}

// catalogState is the one cloneable value-state for NDB's mutable catalog.
// Capture and restore copy this struct; store pointer and pendingPath stay
// outside the snapshot.
type catalogState struct {
	ids          SequentialIDs
	nodes        map[uint64]NBTEntry
	blocks       map[uint64]BBTEntry
	dataTreeRefs map[uint64]int
	subnodeRefs  map[uint64]int
	opaqueRefs   map[uint64]int
	livePages    []uint64
}

func (c catalogState) clone() catalogState {
	c.nodes = cloneMap(c.nodes)
	c.blocks = cloneMap(c.blocks)
	c.dataTreeRefs = cloneMap(c.dataTreeRefs)
	c.subnodeRefs = cloneMap(c.subnodeRefs)
	c.opaqueRefs = cloneMap(c.opaqueRefs)
	c.livePages = cloneU64(c.livePages)
	return c
}

func (n *NDB) catalogSnapshot() catalogState {
	c := catalogState{
		nodes:        n.nodes,
		blocks:       n.blocks,
		dataTreeRefs: n.dataTreeRefs,
		subnodeRefs:  n.subnodeRefs,
		opaqueRefs:   n.opaqueRefs,
		livePages:    n.livePages,
	}
	if n.ids != nil {
		c.ids = *n.ids
	}
	return c.clone()
}

func (n *NDB) applyCatalog(c catalogState) {
	c = c.clone()
	if n.ids != nil {
		*n.ids = c.ids
	} else {
		ids := c.ids
		n.ids = &ids
	}
	n.nodes = c.nodes
	n.blocks = c.blocks
	n.dataTreeRefs = c.dataTreeRefs
	n.subnodeRefs = c.subnodeRefs
	n.opaqueRefs = c.opaqueRefs
	n.livePages = c.livePages
}
