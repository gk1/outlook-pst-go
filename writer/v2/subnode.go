package writer

import (
	"encoding/binary"
	"sort"
)

// SLEntry is a Unicode SLENTRY. See MS-PST 2.2.2.8.3.3.1.
type SLEntry struct {
	NID     uint64
	DataBID uint64
	SubBID  uint64
}

// SIEntry is a Unicode SIENTRY. Key is the first NID of the child. See MS-PST 2.2.2.8.3.3.2.
type SIEntry struct {
	Key uint64
	Ref uint64
}

// SubnodeView is a decoded SLBLOCK or SIBLOCK payload.
type SubnodeView struct {
	Type   byte
	Level  byte
	Count  uint16
	Leaves []SLEntry
	Kids   []SIEntry
}

func EncodeSLBlock(entries []SLEntry) ([]byte, error) {
	if len(entries) == 0 {
		return nil, invalidArg("cEnt", "SLBLOCK has no entries (MS-PST %s)", SectionSLBlock)
	}
	if len(entries) > MaxSLBlockEntries {
		return nil, limitErr("cEnt", "SLBLOCK has %d entries, max %d (MS-PST %s)", len(entries), MaxSLBlockEntries, SectionSLBlock)
	}
	var prev uint64
	buf := make([]byte, SLBlockHeaderSize+SLEntrySize*len(entries))
	buf[0] = BlockTypeSubnode
	buf[1] = 0
	binary.LittleEndian.PutUint16(buf[2:4], uint16(len(entries)))
	for i, e := range entries {
		if err := checkSLEntry(e); err != nil {
			return nil, err
		}
		if i > 0 && e.NID <= prev {
			return nil, invalidArg("nid", "SLENTRY keys must be strictly increasing (prev=0x%x key=0x%x, MS-PST %s)", prev, e.NID, SectionSLBlock)
		}
		prev = e.NID
		off := SLBlockHeaderSize + i*SLEntrySize
		binary.LittleEndian.PutUint64(buf[off:], e.NID)
		binary.LittleEndian.PutUint64(buf[off+8:], e.DataBID)
		binary.LittleEndian.PutUint64(buf[off+16:], e.SubBID)
	}
	return buf, nil
}

func EncodeSIBlock(level byte, kids []SIEntry) ([]byte, error) {
	if level != SIBlockLevel {
		return nil, invalidArg("cLevel", "SIBLOCK cLevel must be 0x01 (MS-PST %s)", SectionSIBlock)
	}
	if len(kids) == 0 {
		return nil, invalidArg("cEnt", "SIBLOCK has no entries (MS-PST %s)", SectionSIBlock)
	}
	if len(kids) > MaxSIBlockEntries {
		return nil, limitErr("cEnt", "SIBLOCK has %d entries, max %d (MS-PST %s)", len(kids), MaxSIBlockEntries, SectionSIBlock)
	}
	var prev uint64
	buf := make([]byte, SIBlockHeaderSize+SIEntrySize*len(kids))
	buf[0] = BlockTypeSubnode
	buf[1] = level
	binary.LittleEndian.PutUint16(buf[2:4], uint16(len(kids)))
	for i, e := range kids {
		if e.Key == 0 || e.Key>>32 != 0 {
			return nil, invalidArg("nid", "invalid SIENTRY key 0x%x (MS-PST %s)", e.Key, SectionSIBlock)
		}
		if e.Ref == 0 || BIDHasReserved(e.Ref) || !BIDIsInternal(e.Ref) {
			return nil, invalidArg("bid", "SIENTRY child 0x%x must be an internal BID (MS-PST %s)", e.Ref, SectionSIBlock)
		}
		if i > 0 && e.Key <= prev {
			return nil, invalidArg("nid", "SIENTRY keys must be strictly increasing (prev=0x%x key=0x%x)", prev, e.Key)
		}
		prev = e.Key
		off := SIBlockHeaderSize + i*SIEntrySize
		binary.LittleEndian.PutUint64(buf[off:], e.Key)
		binary.LittleEndian.PutUint64(buf[off+8:], e.Ref)
	}
	return buf, nil
}

func checkSLEntry(e SLEntry) error {
	if e.NID == 0 || e.NID>>32 != 0 {
		return invalidArg("nid", "invalid SLENTRY NID 0x%x (MS-PST %s)", e.NID, SectionSLBlock)
	}
	if BIDHasReserved(e.DataBID) || BIDHasReserved(e.SubBID) {
		return invalidArg("bid", "reserved bit set on SLENTRY BID (MS-PST %s)", SectionBID)
	}
	return nil
}

// InspectSubnodeBlock validates a Unicode SLBLOCK or SIBLOCK payload.
func InspectSubnodeBlock(data []byte) (*SubnodeView, error) {
	if len(data) < 4 {
		return nil, invariant(SectionSubnode, "size", "payload is %d bytes, need header", len(data))
	}
	v := &SubnodeView{
		Type:  data[0],
		Level: data[1],
		Count: binary.LittleEndian.Uint16(data[2:4]),
	}
	if v.Type != BlockTypeSubnode {
		return nil, invariant(SectionSubnode, "btype", "got 0x%02x want 0x%02x", v.Type, BlockTypeSubnode)
	}
	if v.Count == 0 {
		return nil, invariant(SectionSubnode, "cEnt", "subnode block is empty")
	}
	if len(data) < 8 {
		return nil, invariant(SectionSubnode, "dwPadding", "Unicode subnode header is 8 bytes")
	}
	if pad := binary.LittleEndian.Uint32(data[4:8]); pad != 0 {
		return nil, invariant(SectionSubnode, "dwPadding", "got 0x%08x want 0", pad)
	}
	var prev uint64
	if v.Level == 0 {
		if int(v.Count) > MaxSLBlockEntries {
			return nil, invariant(SectionSLBlock, "cEnt", "got %d max %d", v.Count, MaxSLBlockEntries)
		}
		need := SLBlockHeaderSize + SLEntrySize*int(v.Count)
		if len(data) < need {
			return nil, invariant(SectionSLBlock, "size", "payload %d need %d", len(data), need)
		}
		v.Leaves = make([]SLEntry, v.Count)
		for i := 0; i < int(v.Count); i++ {
			off := SLBlockHeaderSize + i*SLEntrySize
			e := SLEntry{
				NID:     binary.LittleEndian.Uint64(data[off:]),
				DataBID: binary.LittleEndian.Uint64(data[off+8:]),
				SubBID:  binary.LittleEndian.Uint64(data[off+16:]),
			}
			if e.NID == 0 || e.NID>>32 != 0 || BIDHasReserved(e.DataBID) || BIDHasReserved(e.SubBID) {
				return nil, invariant(SectionSLBlock, "nid", "invalid SLENTRY %+v", e)
			}
			if i > 0 && e.NID <= prev {
				return nil, invariant(SectionSLBlock, "nid", "keys not strictly increasing")
			}
			prev = e.NID
			v.Leaves[i] = e
		}
		return v, nil
	}
	if v.Level != SIBlockLevel {
		return nil, invariant(SectionSIBlock, "cLevel", "got %d want 0x01 (MS-PST %s)", v.Level, SectionSIBlock)
	}
	if int(v.Count) > MaxSIBlockEntries {
		return nil, invariant(SectionSIBlock, "cEnt", "got %d max %d", v.Count, MaxSIBlockEntries)
	}
	need := SIBlockHeaderSize + SIEntrySize*int(v.Count)
	if len(data) < need {
		return nil, invariant(SectionSIBlock, "size", "payload %d need %d", len(data), need)
	}
	v.Kids = make([]SIEntry, v.Count)
	for i := 0; i < int(v.Count); i++ {
		off := SIBlockHeaderSize + i*SIEntrySize
		e := SIEntry{
			Key: binary.LittleEndian.Uint64(data[off:]),
			Ref: binary.LittleEndian.Uint64(data[off+8:]),
		}
		if e.Key == 0 || e.Key>>32 != 0 || e.Ref == 0 || BIDHasReserved(e.Ref) || !BIDIsInternal(e.Ref) {
			return nil, invariant(SectionSIBlock, "nid", "invalid SIENTRY %+v", e)
		}
		if i > 0 && e.Key <= prev {
			return nil, invariant(SectionSIBlock, "nid", "keys not strictly increasing")
		}
		prev = e.Key
		v.Kids[i] = e
	}
	return v, nil
}

// PutSubnodeTree builds an SLBLOCK/SIBLOCK tree. Entries are sorted by NID.
// On error, newly allocated tree blocks are dropped.
func (n *NDB) PutSubnodeTree(entries []SLEntry) (BBTEntry, error) {
	if len(entries) == 0 {
		return BBTEntry{}, nil
	}
	sorted := append([]SLEntry(nil), entries...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].NID < sorted[j].NID })
	var staged []uint64
	rollback := func() {
		for i := len(staged) - 1; i >= 0; i-- {
			_ = n.dropBlock(staged[i])
		}
	}
	note := func(e BBTEntry) { staged = append(staged, e.BID) }
	root, err := n.buildSubnodeTree(sorted, note)
	if err != nil {
		rollback()
		return BBTEntry{}, err
	}
	return root, nil
}

func (n *NDB) buildSubnodeTree(entries []SLEntry, note func(BBTEntry)) (BBTEntry, error) {
	var leaves []SIEntry
	for i := 0; i < len(entries); i += MaxSLBlockEntries {
		end := i + MaxSLBlockEntries
		if end > len(entries) {
			end = len(entries)
		}
		chunk := entries[i:end]
		payload, err := EncodeSLBlock(chunk)
		if err != nil {
			return BBTEntry{}, err
		}
		blk, err := n.AllocInternalBlock(uint16(len(payload)))
		if err != nil {
			return BBTEntry{}, err
		}
		note(blk)
		if err := n.putPayload(blk, payload); err != nil {
			return BBTEntry{}, err
		}
		for _, e := range chunk {
			if e.DataBID != 0 {
				if err := n.AddSubnodeRef(e.DataBID); err != nil {
					return BBTEntry{}, err
				}
			}
			if e.SubBID != 0 {
				if err := n.AddSubnodeRef(e.SubBID); err != nil {
					return BBTEntry{}, err
				}
			}
		}
		leaves = append(leaves, SIEntry{Key: chunk[0].NID, Ref: blk.BID})
	}
	return n.buildSIRoot(leaves, note)
}

func (n *NDB) buildSIRoot(kids []SIEntry, note func(BBTEntry)) (BBTEntry, error) {
	if len(kids) == 1 {
		e, _ := n.LookupBlock(kids[0].Ref)
		return e, nil
	}
	if len(kids) > MaxSIBlockEntries {
		return BBTEntry{}, limitErr("cEnt", "need %d SLBLOCKs, max %d in one SIBLOCK (MS-PST %s)", len(kids), MaxSIBlockEntries, SectionSIBlock)
	}
	payload, err := EncodeSIBlock(SIBlockLevel, kids)
	if err != nil {
		return BBTEntry{}, err
	}
	blk, err := n.AllocInternalBlock(uint16(len(payload)))
	if err != nil {
		return BBTEntry{}, err
	}
	note(blk)
	if err := n.putPayload(blk, payload); err != nil {
		return BBTEntry{}, err
	}
	for _, k := range kids {
		if err := n.AddSubnodeRef(k.Ref); err != nil {
			return BBTEntry{}, err
		}
	}
	return blk, nil
}

// WalkSubnodes visits every SLENTRY in NID order.
func (n *NDB) WalkSubnodes(root uint64, fn func(SLEntry) error) error {
	if root == 0 {
		return nil
	}
	e, ok := n.LookupBlock(root)
	if !ok {
		return invalidArg("bid", "missing subnode root 0x%x", root)
	}
	return n.walkSubnode(e, fn, 0)
}

func (n *NDB) walkSubnode(e BBTEntry, fn func(SLEntry) error, depth int) error {
	if depth > 1 {
		return invariant(SectionSIBlock, "cLevel", "subnode walk exceeded SIBLOCK->SLBLOCK (MS-PST %s)", SectionSIBlock)
	}
	data, err := n.blockPayload(e)
	if err != nil {
		return err
	}
	v, err := InspectSubnodeBlock(data)
	if err != nil {
		return err
	}
	if v.Level == 0 {
		for _, ent := range v.Leaves {
			if err := fn(ent); err != nil {
				return err
			}
		}
		return nil
	}
	if v.Level != SIBlockLevel {
		return invariant(SectionSIBlock, "cLevel", "got %d want 0x01", v.Level)
	}
	for _, k := range v.Kids {
		child, ok := n.LookupBlock(k.Ref)
		if !ok {
			return invariant(SectionSIBlock, "bid", "missing child 0x%x", k.Ref)
		}
		fk, err := n.subnodeFirstKey(child)
		if err != nil {
			return err
		}
		if k.Key != fk {
			return invariant(SectionSIBlock, "nid", "separator 0x%x is not child first key 0x%x", k.Key, fk)
		}
		cdata, err := n.blockPayload(child)
		if err != nil {
			return err
		}
		cv, err := InspectSubnodeBlock(cdata)
		if err != nil {
			return err
		}
		if cv.Level != 0 {
			return invariant(SectionSIBlock, "bid", "SIENTRY 0x%x must point to an SLBLOCK (MS-PST %s)", k.Ref, SectionSIBlock)
		}
		if err := n.walkSubnode(child, fn, depth+1); err != nil {
			return err
		}
	}
	return nil
}

func (n *NDB) subnodeFirstKey(e BBTEntry) (uint64, error) {
	data, err := n.blockPayload(e)
	if err != nil {
		return 0, err
	}
	v, err := InspectSubnodeBlock(data)
	if err != nil {
		return 0, err
	}
	if v.Level != 0 {
		return 0, invariant(SectionSIBlock, "cLevel", "SIENTRY child must be an SLBLOCK, got cLevel %d", v.Level)
	}
	if len(v.Leaves) == 0 {
		return 0, invariant(SectionSLBlock, "cEnt", "empty SLBLOCK")
	}
	return v.Leaves[0].NID, nil
}
