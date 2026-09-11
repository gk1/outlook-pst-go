package writer

import (
	"bytes"
	"encoding/binary"
	"io"
	"sort"
)

// HID / HNID packing. See MS-PST 2.3.1.1.
const (
	hidIndexMask  = 0x7FF
	hidIndexShift = 5
	hidBlockShift = 16
)

// MakeHID packs hidBlockIndex and a 0-based allocation index into a HID.
// alloc0 must be in [0, HNMaxAllocsPerPage). hidIndex is 11-bit and 1-based.
func MakeHID(block, alloc0 uint16) uint32 {
	return uint32(block)<<hidBlockShift | uint32(alloc0+1)<<hidIndexShift
}

// HIDBlock returns hidBlockIndex.
func HIDBlock(hid uint32) uint16 { return uint16(hid >> hidBlockShift) }

// HIDIndex returns the 1-based hidIndex.
func HIDIndex(hid uint32) uint16 { return uint16((hid >> hidIndexShift) & hidIndexMask) }

// IsHID reports whether hnid is a HID (nidType == NID_TYPE_HID).
func IsHID(hnid uint32) bool { return hnid&0x1F == 0 }

func hnBitmapPage(i int) bool { return i >= 8 && (i-8)%128 == 0 }

func hnHeaderSize(i int) int {
	if i == 0 {
		return HNHDRSize
	}
	if hnBitmapPage(i) {
		return HNBITMAPHDRSize
	}
	return HNPAGEHDRSize
}

func hnFillLevel(cbFree int) byte {
	switch {
	case cbFree >= HNFillEmpty:
		return FillLevelEmpty
	case cbFree >= HNFillLevel1:
		return FillLevel1
	case cbFree >= HNFillLevel2:
		return FillLevel2
	default:
		return FillLevelFull
	}
}

func packFill(dst []byte, page int, level byte) {
	if page < 0 || page/4 >= len(dst) {
		return
	}
	shift := uint((page % 4) * 2)
	dst[page/4] &^= 0x3 << shift
	dst[page/4] |= (level & 0x3) << shift
}

func unpackFill(src []byte, page int) byte {
	if page < 0 || page/4 >= len(src) {
		return 0
	}
	return (src[page/4] >> uint((page%4)*2)) & 0x3
}

type hnPage struct {
	allocs [][]byte
	hids   []uint32
}

type heapSub struct {
	nid     uint32
	data    []byte
	leafCB  int // 0 = MaxDataBlockCB; otherwise each data-tree leaf is at most leafCB
	dataBID uint64
	subBID  uint64
}

// Heap is a Unicode Heap-on-Node builder. See MS-PST 2.3.1.
type Heap struct {
	n         *NDB
	clientSig byte
	root      uint32
	pages     []hnPage
	subs      []heapSub
	nextSub   uint32
}

// NewHeap starts an empty heap with the given client signature (PC/TC/BTH).
func NewHeap(n *NDB, clientSig byte) *Heap {
	return &Heap{n: n, clientSig: clientSig, nextSub: 1}
}

func (h *Heap) SetRoot(hid uint32) { h.root = hid }

func (h *Heap) Root() uint32 { return h.root }

func (h *Heap) newSub(data []byte) uint32 {
	return h.newSubChunked(data, 0)
}

func (h *Heap) newSubChunked(data []byte, leafCB int) uint32 {
	nid := MakeNID(NIDTypeLTP, h.nextSub)
	h.nextSub++
	h.subs = append(h.subs, heapSub{nid: nid, data: append([]byte(nil), data...), leafCB: leafCB})
	return nid
}

func (h *Heap) fits(i int, size int) bool {
	nAlloc := len(h.pages[i].allocs)
	if nAlloc >= HNMaxAllocsPerPage {
		return false
	}
	used := hnHeaderSize(i) + 4 + 2*(nAlloc+1)
	for _, a := range h.pages[i].allocs {
		used += len(a)
	}
	need := size + 2
	return used+need <= MaxDataBlockCB
}

// Allocate places data in the heap and returns a HID, or a subnode HNID when
// the value exceeds the in-heap maximum (MS-PST 2.3.1).
func (h *Heap) Allocate(data []byte) (uint32, error) {
	if h == nil || h.n == nil {
		return 0, invalidArg("heap", "nil heap")
	}
	if len(data) > HeapMaxAlloc {
		return h.newSub(data), nil
	}
	if len(h.pages) == 0 {
		h.pages = append(h.pages, hnPage{})
	}
	i := len(h.pages) - 1
	if !h.fits(i, len(data)) {
		if len(h.pages[i].allocs) == 0 && len(data)+hnHeaderSize(i)+6 > MaxDataBlockCB {
			return 0, limitErr("cb", "allocation %d does not fit an empty HN page (MS-PST %s)", len(data), SectionHN)
		}
		if i >= HNMaxBlockIndex {
			return 0, limitErr("hidBlockIndex", "HN page index %d exceeds 16-bit hidBlockIndex (MS-PST %s)", i+1, SectionHN)
		}
		h.pages = append(h.pages, hnPage{})
		i = len(h.pages) - 1
		if !h.fits(i, len(data)) {
			return 0, limitErr("cb", "allocation %d does not fit HN page %d (MS-PST %s)", len(data), i, SectionHN)
		}
	}
	if i > HNMaxBlockIndex {
		return 0, limitErr("hidBlockIndex", "HN page index %d exceeds 16-bit hidBlockIndex (MS-PST %s)", i, SectionHN)
	}
	nAlloc := len(h.pages[i].allocs)
	if nAlloc >= HNMaxAllocsPerPage {
		return 0, limitErr("hidIndex", "HN page %d already has %d allocations (MS-PST %s)", i, nAlloc, SectionHN)
	}
	hid := MakeHID(uint16(i), uint16(nAlloc))
	h.pages[i].allocs = append(h.pages[i].allocs, append([]byte(nil), data...))
	h.pages[i].hids = append(h.pages[i].hids, hid)
	return hid, nil
}

// AllocateStream places r as an HN allocation or a data-tree HNID.
// Values larger than HeapMaxAlloc are streamed into the NDB immediately
// and are not retained on the Heap.
func (h *Heap) AllocateStream(r io.Reader, size int64) (uint32, error) {
	if h == nil || h.n == nil {
		return 0, invalidArg("heap", "nil heap")
	}
	if size < 0 {
		return 0, invalidArg("size", "negative stream size %d", size)
	}
	if size <= int64(HeapMaxAlloc) {
		buf := make([]byte, int(size))
		if size > 0 {
			if r == nil {
				return 0, invalidArg("reader", "data tree reader is nil")
			}
			if _, err := io.ReadFull(r, buf); err != nil {
				return 0, ioErr("reader", "heap stream: %v", err)
			}
		}
		return h.Allocate(buf)
	}
	if r == nil {
		return 0, invalidArg("reader", "data tree reader is nil")
	}
	blk, err := h.n.streamDataTree(io.LimitReader(r, size), size)
	if err != nil {
		return 0, err
	}
	nid := MakeNID(NIDTypeLTP, h.nextSub)
	h.nextSub++
	h.subs = append(h.subs, heapSub{nid: nid, dataBID: blk.BID})
	return nid, nil
}

func (h *Heap) encodePages() ([][]byte, []byte, error) {
	if len(h.pages) == 0 {
		h.pages = []hnPage{{}}
	}
	fills := make([]byte, len(h.pages))
	raws := make([][]byte, len(h.pages))
	for i := range h.pages {
		raw, err := encodeHNPage(i, h.clientSig, h.root, h.pages[i].allocs, nil)
		if err != nil {
			return nil, nil, err
		}
		raws[i] = raw
		fills[i] = hnFillLevel(MaxDataBlockCB - len(raw))
	}
	hdrFill := make([]byte, 4)
	for i := 0; i < 8 && i < len(fills); i++ {
		packFill(hdrFill, i, fills[i])
	}
	for i := range h.pages {
		var extra []byte
		if i == 0 {
			extra = hdrFill
		} else if hnBitmapPage(i) {
			extra = make([]byte, 64)
			base := i
			for j := 0; j < 128; j++ {
				pi := base + j
				if pi < len(fills) {
					packFill(extra, j, fills[pi])
				}
			}
		}
		raw, err := encodeHNPage(i, h.clientSig, h.root, h.pages[i].allocs, extra)
		if err != nil {
			return nil, nil, err
		}
		raws[i] = raw
	}
	return raws, fills, nil
}

func encodeHNPage(index int, clientSig byte, userRoot uint32, allocs [][]byte, fill []byte) ([]byte, error) {
	hdr := hnHeaderSize(index)
	mapSize := 4 + 2*(len(allocs)+1)
	size := hdr
	for _, a := range allocs {
		size += len(a)
	}
	size += mapSize
	if size > MaxDataBlockCB {
		return nil, limitErr("cb", "HN page %d is %d bytes, max %d (MS-PST %s)", index, size, MaxDataBlockCB, SectionHN)
	}
	buf := make([]byte, size)
	ib := uint16(hdr)
	for _, a := range allocs {
		ib += uint16(len(a))
	}
	binary.LittleEndian.PutUint16(buf[0:2], ib)
	if index == 0 {
		buf[2] = HeapSignature
		buf[3] = clientSig
		binary.LittleEndian.PutUint32(buf[4:8], userRoot)
		if len(fill) >= 4 {
			copy(buf[8:12], fill[:4])
		}
	} else if hnBitmapPage(index) && len(fill) >= 64 {
		copy(buf[2:66], fill[:64])
	}
	off := hdr
	rg := make([]uint16, len(allocs)+1)
	for i, a := range allocs {
		rg[i] = uint16(off)
		copy(buf[off:], a)
		off += len(a)
	}
	rg[len(allocs)] = uint16(off)
	binary.LittleEndian.PutUint16(buf[off:], uint16(len(allocs)))
	binary.LittleEndian.PutUint16(buf[off+2:], 0)
	for i, v := range rg {
		binary.LittleEndian.PutUint16(buf[off+4+2*i:], v)
	}
	return buf, nil
}

// Commit writes the heap as the node's data tree (and subnode tree for
// oversized HNIDs).
func (h *Heap) Commit(nid uint32) error {
	if h == nil || h.n == nil {
		return invalidArg("heap", "nil heap")
	}
	if nid == 0 || NIDTypeOf(nid) == NIDTypeHID {
		return invalidArg("nid", "heap node 0x%x must not be a HID", nid)
	}
	return h.n.runTxn(func() error { return h.commitLocked(nid) })
}

func (h *Heap) commitLocked(nid uint32) error {
	data, sub, err := h.materialize()
	if err != nil {
		return err
	}
	return h.n.PutNode(NBTEntry{NID: uint64(nid), DataBID: data.BID, SubBID: sub.BID})
}

// AttachHeap adds child's data tree as a subnode of this heap (MS-PST 2.4.5.3
// recipient tables hang off the message PC this way).
func (h *Heap) AttachHeap(nid uint32, child *Heap) error {
	if h == nil || h.n == nil || child == nil {
		return invalidArg("heap", "nil heap")
	}
	if nid == 0 {
		return invalidArg("nid", "subnode NID is 0")
	}
	data, sub, err := child.materialize()
	if err != nil {
		return err
	}
	h.subs = append(h.subs, heapSub{nid: nid, dataBID: data.BID, subBID: sub.BID})
	return nil
}

func (h *Heap) materialize() (BBTEntry, BBTEntry, error) {
	raws, _, err := h.encodePages()
	if err != nil {
		return BBTEntry{}, BBTEntry{}, err
	}
	leaves := make([]BBTEntry, 0, len(raws))
	var total uint32
	for _, raw := range raws {
		e, err := h.n.AllocBlock(uint16(len(raw)))
		if err != nil {
			return BBTEntry{}, BBTEntry{}, err
		}
		if err := h.n.putPayload(e, raw); err != nil {
			return BBTEntry{}, BBTEntry{}, err
		}
		leaves = append(leaves, e)
		total += uint32(len(raw))
	}
	data, err := h.n.buildDataTree(leaves, total)
	if err != nil {
		return BBTEntry{}, BBTEntry{}, err
	}
	if len(h.subs) == 0 {
		return data, BBTEntry{}, nil
	}
	ents := make([]SLEntry, 0, len(h.subs))
	for _, s := range h.subs {
		if s.dataBID != 0 {
			ents = append(ents, SLEntry{NID: uint64(s.nid), DataBID: s.dataBID, SubBID: s.subBID})
			continue
		}
		var blk BBTEntry
		if s.leafCB > 0 {
			blk, err = putDataLeaves(h.n, s.data, s.leafCB)
		} else {
			blk, err = h.n.streamDataTree(bytes.NewReader(s.data), int64(len(s.data)))
		}
		if err != nil {
			return BBTEntry{}, BBTEntry{}, err
		}
		ents = append(ents, SLEntry{NID: uint64(s.nid), DataBID: blk.BID})
	}
	sort.Slice(ents, func(i, j int) bool { return ents[i].NID < ents[j].NID })
	sub, err := h.n.buildSubnodeTree(ents)
	if err != nil {
		return BBTEntry{}, BBTEntry{}, err
	}
	return data, sub, nil
}

// putDataLeaves writes data as a data tree whose leaves are at most leafCB
// bytes. The last leaf may be shorter. leafCB must be in 1..MaxDataBlockCB.
func putDataLeaves(n *NDB, data []byte, leafCB int) (BBTEntry, error) {
	if n == nil {
		return BBTEntry{}, invalidArg("ndb", "nil NDB")
	}
	if leafCB < 1 || leafCB > MaxDataBlockCB {
		return BBTEntry{}, invalidArg("cb", "leaf size %d, want 1..%d", leafCB, MaxDataBlockCB)
	}
	if len(data) == 0 {
		return BBTEntry{}, nil
	}
	var leaves []BBTEntry
	var total uint32
	for off := 0; off < len(data); {
		ncb := leafCB
		if off+ncb > len(data) {
			ncb = len(data) - off
		}
		e, err := n.AllocBlock(uint16(ncb))
		if err != nil {
			return BBTEntry{}, err
		}
		if err := n.putPayload(e, data[off:off+ncb]); err != nil {
			return BBTEntry{}, err
		}
		leaves = append(leaves, e)
		total += uint32(ncb)
		off += ncb
	}
	return n.buildDataTree(leaves, total)
}

// HNPageView is a decoded HN page. See MS-PST 2.3.1.2–2.3.1.5.
type HNPageView struct {
	Index     int
	IbHnpm    uint16
	Signature byte
	ClientSig byte
	UserRoot  uint32
	Fill      []byte
	CAlloc    uint16
	CFree     uint16
	Offsets   []uint16
	Raw       []byte
}
