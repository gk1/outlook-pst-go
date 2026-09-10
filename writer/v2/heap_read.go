package writer

import (
	"encoding/binary"
	"io"
)

func InspectHNPage(index int, data []byte) (*HNPageView, error) {
	need := hnHeaderSize(index)
	if len(data) < need {
		return nil, invariant(SectionHN, "size", "HN page %d is %d bytes, need header %d", index, len(data), need)
	}
	v := &HNPageView{
		Index:  index,
		IbHnpm: binary.LittleEndian.Uint16(data[0:2]),
		Raw:    data,
	}
	if index == 0 {
		v.Signature = data[2]
		v.ClientSig = data[3]
		v.UserRoot = binary.LittleEndian.Uint32(data[4:8])
		v.Fill = append([]byte(nil), data[8:12]...)
		if v.Signature != HeapSignature {
			return nil, invariant(SectionHN, "bSig", "got 0x%02x want 0x%02x", v.Signature, HeapSignature)
		}
	} else if hnBitmapPage(index) {
		v.Fill = append([]byte(nil), data[2:66]...)
	}
	if int(v.IbHnpm)+4 > len(data) {
		return nil, invariant(SectionHN, "ibHnpm", "page map at %d exceeds page %d", v.IbHnpm, len(data))
	}
	pm := data[v.IbHnpm:]
	v.CAlloc = binary.LittleEndian.Uint16(pm[0:2])
	v.CFree = binary.LittleEndian.Uint16(pm[2:4])
	nOff := int(v.CAlloc) + 1
	if len(pm) < 4+2*nOff {
		return nil, invariant(SectionHN, "rgibAlloc", "page map truncated: cAlloc=%d have %d", v.CAlloc, len(pm))
	}
	v.Offsets = make([]uint16, nOff)
	for i := 0; i < nOff; i++ {
		v.Offsets[i] = binary.LittleEndian.Uint16(pm[4+2*i:])
	}
	if v.Offsets[v.CAlloc] != v.IbHnpm {
		return nil, invariant(SectionHN, "rgibAlloc", "last offset %d want ibHnpm %d", v.Offsets[v.CAlloc], v.IbHnpm)
	}
	for i := 1; i < nOff; i++ {
		if v.Offsets[i] < v.Offsets[i-1] {
			return nil, invariant(SectionHN, "rgibAlloc", "offsets not nondecreasing")
		}
	}
	return v, nil
}

func (v *HNPageView) Allocation(hidIndex1 uint16) ([]byte, error) {
	if hidIndex1 == 0 || int(hidIndex1) > int(v.CAlloc) {
		return nil, invalidArg("hidIndex", "hidIndex %d cAlloc %d", hidIndex1, v.CAlloc)
	}
	start := v.Offsets[hidIndex1-1]
	end := v.Offsets[hidIndex1]
	if int(end) > len(v.Raw) || start > end {
		return nil, invariant(SectionHN, "rgibAlloc", "HID %d bounds %d:%d", hidIndex1, start, end)
	}
	return v.Raw[start:end], nil
}

// HeapView is a committed heap reopened from an NBT node.
type HeapView struct {
	n         *NDB
	nid       uint32
	dataBID   uint64
	subBID    uint64
	clientSig byte
	root      uint32
	pages     []*HNPageView
	fills     []byte
}

// OpenHeap reads a committed Heap-on-Node. Pages are inspected from the
// data tree; oversized HNIDs are resolved through the subnode tree.
func OpenHeap(n *NDB, nid uint32) (*HeapView, error) {
	if n == nil {
		return nil, invalidArg("ndb", "nil NDB")
	}
	e, ok := n.LookupNode(uint64(nid))
	if !ok {
		return nil, invalidArg("nid", "missing heap node 0x%x", nid)
	}
	h := &HeapView{n: n, nid: nid, dataBID: e.DataBID, subBID: e.SubBID}
	var idx int
	_, err := n.walkDataTree(e.DataBID, func(bid uint64, _ uint16) error {
		blk, ok := n.LookupBlock(bid)
		if !ok {
			return invariant(SectionHN, "page", "missing HN page BID 0x%x", bid)
		}
		raw, err := n.blockPayload(blk)
		if err != nil {
			return err
		}
		pg, err := InspectHNPage(idx, raw)
		if err != nil {
			return err
		}
		if idx == 0 {
			h.clientSig = pg.ClientSig
			h.root = pg.UserRoot
		}
		h.pages = append(h.pages, pg)
		h.fills = append(h.fills, hnFillLevel(MaxDataBlockCB-len(raw)))
		idx++
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(h.pages) == 0 {
		return nil, invariant(SectionHN, "page", "heap has no pages")
	}
	if err := h.checkFill(); err != nil {
		return nil, err
	}
	return h, nil
}

func (h *HeapView) ClientSig() byte { return h.clientSig }
func (h *HeapView) Root() uint32    { return h.root }
func (h *HeapView) Pages() int      { return len(h.pages) }

func (h *HeapView) checkFill() error {
	hdr := h.pages[0]
	for i := 0; i < 8 && i < len(h.fills); i++ {
		got := unpackFill(hdr.Fill, i)
		if got != h.fills[i] {
			return invariant(SectionHN, "rgbFillLevel", "page %d fill %d want %d", i, got, h.fills[i])
		}
	}
	for i, pg := range h.pages {
		if !hnBitmapPage(i) {
			continue
		}
		for j := 0; j < 128; j++ {
			pi := i + j
			if pi >= len(h.fills) {
				break
			}
			got := unpackFill(pg.Fill, j)
			if got != h.fills[pi] {
				return invariant(SectionHN, "rgbFillLevel", "bitmap page %d slot %d fill %d want %d", i, pi, got, h.fills[pi])
			}
		}
	}
	return nil
}

// Read returns the allocation for a HID or the subnode bytes for an HNID.
func (h *HeapView) Read(hnid uint32) ([]byte, error) {
	if h == nil {
		return nil, invalidArg("heap", "nil heap")
	}
	if hnid == 0 {
		return nil, nil
	}
	if !IsHID(hnid) {
		return h.readSub(hnid)
	}
	block := int(HIDBlock(hnid))
	idx := HIDIndex(hnid)
	if block >= len(h.pages) {
		return nil, invalidArg("hidBlockIndex", "page %d of %d", block, len(h.pages))
	}
	return h.pages[block].Allocation(idx)
}

func (h *HeapView) readSub(nid uint32) ([]byte, error) {
	var found *SLEntry
	err := h.n.WalkSubnodes(h.subBID, func(e SLEntry) error {
		if uint32(e.NID) == nid {
			cp := e
			found = &cp
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if found == nil {
		return nil, invalidArg("hnid", "missing subnode 0x%x", nid)
	}
	rd, err := h.n.OpenDataTree(found.DataBID)
	if err != nil {
		return nil, err
	}
	return io.ReadAll(rd)
}
