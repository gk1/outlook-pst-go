package writer

import (
	"encoding/binary"
	"io"
)

// XBlockView is a decoded Unicode XBLOCK or XXBLOCK payload (no trailer).
// See MS-PST 2.2.2.8.3.1 / 2.2.2.8.3.2.
type XBlockView struct {
	Type  byte
	Level byte
	Count uint16
	Total uint32
	BIDs  []uint64
}

// EncodeXBlock encodes an XBLOCK (level 1) or XXBLOCK (level 2) payload.
func EncodeXBlock(level byte, total uint32, bids []uint64) ([]byte, error) {
	if level != XBlockLevel && level != XXBlockLevel {
		return nil, invalidArg("cLevel", "extended block level %d want 1 or 2 (MS-PST %s)", level, SectionXBlock)
	}
	if len(bids) == 0 {
		return nil, invalidArg("cEnt", "extended block has no entries (MS-PST %s)", SectionXBlock)
	}
	if len(bids) > MaxXBlockEntries {
		return nil, limitErr("cEnt", "extended block has %d BIDs, max %d (MS-PST %s)", len(bids), MaxXBlockEntries, SectionXBlock)
	}
	seen := make(map[uint64]struct{}, len(bids))
	for i, bid := range bids {
		if bid == 0 {
			return nil, invalidArg("rgbid", "null BID at %d (MS-PST %s)", i, SectionXBlock)
		}
		if BIDHasReserved(bid) {
			return nil, invalidArg("rgbid", "reserved bit set on BID 0x%x (MS-PST %s)", bid, SectionBID)
		}
		if level == XBlockLevel && BIDIsInternal(bid) {
			return nil, invalidArg("rgbid", "XBLOCK child 0x%x is internal (MS-PST %s)", bid, SectionXBlock)
		}
		if level == XXBlockLevel && !BIDIsInternal(bid) {
			return nil, invalidArg("rgbid", "XXBLOCK child 0x%x is not internal (MS-PST %s)", bid, SectionXXBlock)
		}
		if _, dup := seen[bid]; dup {
			return nil, invalidArg("rgbid", "duplicate BID 0x%x (MS-PST %s)", bid, SectionXBlock)
		}
		seen[bid] = struct{}{}
	}
	buf := make([]byte, XBlockHeaderSize+8*len(bids))
	buf[0] = BlockTypeXBlock
	buf[1] = level
	binary.LittleEndian.PutUint16(buf[2:4], uint16(len(bids)))
	binary.LittleEndian.PutUint32(buf[4:8], total)
	for i, bid := range bids {
		binary.LittleEndian.PutUint64(buf[8+i*8:], bid)
	}
	return buf, nil
}

// InspectXBlock validates a Unicode XBLOCK/XXBLOCK payload.
func InspectXBlock(data []byte) (*XBlockView, error) {
	if len(data) < XBlockHeaderSize {
		return nil, invariant(SectionXBlock, "size", "payload is %d bytes, need header", len(data))
	}
	v := &XBlockView{
		Type:  data[0],
		Level: data[1],
		Count: binary.LittleEndian.Uint16(data[2:4]),
		Total: binary.LittleEndian.Uint32(data[4:8]),
	}
	if v.Type != BlockTypeXBlock {
		return nil, invariant(SectionXBlock, "btype", "got 0x%02x want 0x%02x", v.Type, BlockTypeXBlock)
	}
	if v.Level != XBlockLevel && v.Level != XXBlockLevel {
		return nil, invariant(SectionXBlock, "cLevel", "got %d want 1 or 2 (MS-PST %s)", v.Level, SectionXBlock)
	}
	if v.Count == 0 || int(v.Count) > MaxXBlockEntries {
		return nil, invariant(SectionXBlock, "cEnt", "got %d, want 1..%d", v.Count, MaxXBlockEntries)
	}
	need := XBlockHeaderSize + 8*int(v.Count)
	if len(data) < need {
		return nil, invariant(SectionXBlock, "size", "payload %d bytes, need %d for cEnt=%d", len(data), need, v.Count)
	}
	v.BIDs = make([]uint64, v.Count)
	seen := make(map[uint64]struct{}, v.Count)
	for i := 0; i < int(v.Count); i++ {
		bid := binary.LittleEndian.Uint64(data[8+i*8:])
		if bid == 0 || BIDHasReserved(bid) {
			return nil, invariant(SectionXBlock, "rgbid", "invalid BID 0x%x at %d", bid, i)
		}
		if v.Level == XBlockLevel && BIDIsInternal(bid) {
			return nil, invariant(SectionXBlock, "rgbid", "XBLOCK child 0x%x is internal", bid)
		}
		if v.Level == XXBlockLevel && !BIDIsInternal(bid) {
			return nil, invariant(SectionXXBlock, "rgbid", "XXBLOCK child 0x%x is not internal", bid)
		}
		if _, dup := seen[bid]; dup {
			return nil, invariant(SectionXBlock, "rgbid", "duplicate BID 0x%x", bid)
		}
		seen[bid] = struct{}{}
		v.BIDs[i] = bid
	}
	return v, nil
}

// PutDataTree streams r into direct, XBLOCK, or XXBLOCK form.
// expected==0 means unknown length; a positive expected must match bytes read.
// On any error, newly allocated blocks are dropped (no partial commit).
func (n *NDB) PutDataTree(r io.Reader, expected int64) (BBTEntry, error) {
	if r == nil {
		return BBTEntry{}, invalidArg("reader", "data tree reader is nil")
	}
	if expected > int64(^uint32(0)) {
		return BBTEntry{}, limitErr("lcbTotal", "logical size %d exceeds uint32 lcbTotal (MS-PST %s)", expected, SectionXBlock)
	}
	var staged []uint64
	var fail error
	rollback := func() {
		for i := len(staged) - 1; i >= 0; i-- {
			_ = n.dropBlock(staged[i])
		}
	}
	note := func(e BBTEntry) { staged = append(staged, e.BID) }

	var leaves []BBTEntry
	var total uint64
	buf := make([]byte, MaxDataBlockCB)
	for {
		nr, err := io.ReadFull(r, buf)
		if nr > 0 {
			chunk := append([]byte(nil), buf[:nr]...)
			e, aerr := n.AllocBlock(uint16(len(chunk)))
			if aerr != nil {
				fail = aerr
				break
			}
			note(e)
			if err := n.putPayload(e, chunk); err != nil {
				fail = err
				break
			}
			leaves = append(leaves, e)
			total += uint64(len(chunk))
			if total > uint64(^uint32(0)) {
				fail = limitErr("lcbTotal", "logical size exceeds uint32 lcbTotal (MS-PST %s)", SectionXBlock)
				break
			}
		}
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			break
		}
		if err != nil {
			fail = ioErr("reader", "data tree read: %v", err)
			break
		}
	}
	if fail != nil {
		rollback()
		return BBTEntry{}, fail
	}
	if expected > 0 && int64(total) != expected {
		rollback()
		return BBTEntry{}, invalidArg("size", "read %d bytes, expected %d", total, expected)
	}
	if len(leaves) == 0 {
		return BBTEntry{}, nil
	}
	root, err := n.buildDataTree(leaves, uint32(total), note)
	if err != nil {
		rollback()
		return BBTEntry{}, err
	}
	return root, nil
}

func (n *NDB) buildDataTree(leaves []BBTEntry, total uint32, note func(BBTEntry)) (BBTEntry, error) {
	if len(leaves) == 1 {
		return leaves[0], nil
	}
	var xb []BBTEntry
	for i := 0; i < len(leaves); i += MaxXBlockEntries {
		end := i + MaxXBlockEntries
		if end > len(leaves) {
			end = len(leaves)
		}
		chunk := leaves[i:end]
		bids := make([]uint64, len(chunk))
		var sub uint32
		for j, e := range chunk {
			bids[j] = e.BID
			sub += uint32(e.CB)
		}
		payload, err := EncodeXBlock(XBlockLevel, sub, bids)
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
		for _, bid := range bids {
			if err := n.AddDataTreeRef(bid); err != nil {
				return BBTEntry{}, err
			}
		}
		xb = append(xb, blk)
	}
	if len(xb) == 1 {
		return xb[0], nil
	}
	if len(xb) > MaxXBlockEntries {
		return BBTEntry{}, limitErr("cEnt", "need %d XBLOCKs, max %d (XXXBLOCK unsupported, MS-PST %s)", len(xb), MaxXBlockEntries, SectionXXBlock)
	}
	bids := make([]uint64, len(xb))
	for i, e := range xb {
		bids[i] = e.BID
	}
	payload, err := EncodeXBlock(XXBlockLevel, total, bids)
	if err != nil {
		return BBTEntry{}, err
	}
	root, err := n.AllocInternalBlock(uint16(len(payload)))
	if err != nil {
		return BBTEntry{}, err
	}
	note(root)
	if err := n.putPayload(root, payload); err != nil {
		return BBTEntry{}, err
	}
	for _, bid := range bids {
		if err := n.AddDataTreeRef(bid); err != nil {
			return BBTEntry{}, err
		}
	}
	return root, nil
}

// DataTreeReader streams a data tree's leaf bytes in order.
type DataTreeReader struct {
	n     *NDB
	leaf  []byte
	off   int
	bids  []uint64
	idx   int
	total uint64
	read  uint64
	err   error
}

// OpenDataTree returns a reader over root's logical bytes. Memory is one leaf.
func (n *NDB) OpenDataTree(root uint64) (*DataTreeReader, error) {
	total, bids, err := n.flattenDataTree(root)
	if err != nil {
		return nil, err
	}
	return &DataTreeReader{n: n, bids: bids, total: total}, nil
}

func (r *DataTreeReader) Read(p []byte) (int, error) {
	if r.err != nil {
		return 0, r.err
	}
	if r.off >= len(r.leaf) {
		if r.idx >= len(r.bids) {
			if r.read != r.total {
				r.err = invariant(SectionXBlock, "lcbTotal", "read %d bytes, lcbTotal %d", r.read, r.total)
				return 0, r.err
			}
			return 0, io.EOF
		}
		e, ok := r.n.LookupBlock(r.bids[r.idx])
		if !ok {
			r.err = invariant(SectionXBlock, "rgbid", "missing leaf BID 0x%x", r.bids[r.idx])
			return 0, r.err
		}
		data, err := r.n.blockPayload(e)
		if err != nil {
			r.err = err
			return 0, err
		}
		r.leaf = data
		r.off = 0
		r.idx++
	}
	n := copy(p, r.leaf[r.off:])
	r.off += n
	r.read += uint64(n)
	return n, nil
}

func (n *NDB) flattenDataTree(root uint64) (uint64, []uint64, error) {
	return n.flattenDataTreeAt(root, map[uint64]struct{}{}, 0)
}

func (n *NDB) flattenDataTreeAt(root uint64, seen map[uint64]struct{}, depth int) (uint64, []uint64, error) {
	if root == 0 {
		return 0, nil, nil
	}
	if depth > 2 {
		return 0, nil, invariant(SectionXXBlock, "cLevel", "data tree deeper than XXBLOCK->XBLOCK->data (MS-PST %s)", SectionXXBlock)
	}
	if _, ok := seen[root]; ok {
		return 0, nil, invariant(SectionXBlock, "rgbid", "cycle or duplicate BID 0x%x", root)
	}
	seen[root] = struct{}{}
	e, ok := n.LookupBlock(root)
	if !ok {
		return 0, nil, invalidArg("bid", "missing data-tree root 0x%x", root)
	}
	if !BIDIsInternal(root) {
		return uint64(e.CB), []uint64{root}, nil
	}
	data, err := n.blockPayload(e)
	if err != nil {
		return 0, nil, err
	}
	xb, err := InspectXBlock(data)
	if err != nil {
		return 0, nil, err
	}
	if xb.Level == XBlockLevel {
		if depth > 1 {
			return 0, nil, invariant(SectionXXBlock, "cLevel", "XBLOCK nested too deep")
		}
		var leaves []uint64
		var sum uint64
		for _, bid := range xb.BIDs {
			if BIDIsInternal(bid) {
				return 0, nil, invariant(SectionXBlock, "rgbid", "XBLOCK child 0x%x is internal", bid)
			}
			if _, dup := seen[bid]; dup {
				return 0, nil, invariant(SectionXBlock, "rgbid", "duplicate BID 0x%x", bid)
			}
			seen[bid] = struct{}{}
			child, ok := n.LookupBlock(bid)
			if !ok {
				return 0, nil, invariant(SectionXBlock, "rgbid", "missing data BID 0x%x", bid)
			}
			leaves = append(leaves, bid)
			sum += uint64(child.CB)
		}
		if sum != uint64(xb.Total) {
			return 0, nil, invariant(SectionXBlock, "lcbTotal", "walked %d want lcbTotal %d", sum, xb.Total)
		}
		return sum, leaves, nil
	}
	if depth != 0 {
		return 0, nil, invariant(SectionXXBlock, "cLevel", "XXBLOCK 0x%x is not at tree root", root)
	}
	var leaves []uint64
	var sum uint64
	for _, bid := range xb.BIDs {
		if _, dup := seen[bid]; dup {
			return 0, nil, invariant(SectionXXBlock, "rgbid", "cycle or duplicate BID 0x%x", bid)
		}
		child, ok := n.LookupBlock(bid)
		if !ok {
			return 0, nil, invariant(SectionXXBlock, "rgbid", "missing XBLOCK BID 0x%x", bid)
		}
		if !BIDIsInternal(bid) {
			return 0, nil, invariant(SectionXXBlock, "rgbid", "XXBLOCK child 0x%x is not internal", bid)
		}
		cdata, err := n.blockPayload(child)
		if err != nil {
			return 0, nil, err
		}
		cx, err := InspectXBlock(cdata)
		if err != nil {
			return 0, nil, err
		}
		if cx.Level != XBlockLevel {
			return 0, nil, invariant(SectionXXBlock, "cLevel", "XXBLOCK child 0x%x has cLevel %d, want 1", bid, cx.Level)
		}
		sub, kids, err := n.flattenDataTreeAt(bid, seen, depth+1)
		if err != nil {
			return 0, nil, err
		}
		leaves = append(leaves, kids...)
		sum += sub
	}
	if sum != uint64(xb.Total) {
		return 0, nil, invariant(SectionXXBlock, "lcbTotal", "walked %d want lcbTotal %d", sum, xb.Total)
	}
	return sum, leaves, nil
}
