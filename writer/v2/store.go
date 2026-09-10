package writer

import (
	"encoding/binary"
	"fmt"
	"io"
	"sort"
)

// Store is the in-memory Unicode allocation map. AMap is authoritative.
// Growth is in whole AMap regions with periodic PMap/FMap/FPMap pages.
// See MS-PST 2.2.2.7.2 and 2.6.1.1.2.
type Store struct {
	regions       []region
	lastAllocAMap uint32
	valid         byte
	unique        uint32
	bidNextP      uint64
	bidNextB      uint64
	dlistBID      uint64
	nbtRoot       BREF
	bbtRoot       BREF
	io            *ioHandle // last committed source
	work          *ioHandle // uncommitted writable spool
	undo          *undoLog  // extent journal for the active transaction
}

// srcLife is who may Close/remove the writable handle.
type srcLife uint8

const (
	lifeBorrowed srcLife = iota // caller-owned; never close
	lifeClose                   // we opened it; close, do not remove
	lifeTemp                    // pst-v2-*.spool; close and remove
)

type region struct {
	bitmap [AMapBitmapBytes]byte
}

// NewStore creates a Unicode PST with the first AMap at 0x4400, the first
// PMap at 0x4600, and a DList at 0x4200. Header/DList live in the unmapped
// prefix before 0x4400.
func NewStore() *Store {
	s := &Store{valid: AMapValid2, unique: 1, bidNextP: FirstAllocBID, bidNextB: FirstAllocBID}
	s.dlistBID = s.takePageBID()
	s.mustGrow()
	return s
}

// takePageBID assigns the next page BID from bidNextP. Page BIDs use all
// bits and increment by 1 (MS-PST 2.2.2.2). Block BIDs still advance by 4.
func (s *Store) takePageBID() uint64 {
	bid, err := takeMonotonic(&s.bidNextP, PageBIDIncrement)
	if err != nil {
		panic(err)
	}
	return bid
}

func (s *Store) mustGrow() {
	if err := s.Grow(); err != nil {
		panic(err)
	}
}

// Grow appends one AMap region and reserves its metadata pages.
func (s *Store) Grow() error {
	idx := uint64(len(s.regions))
	s.regions = append(s.regions, region{})
	for _, p := range RegionMapPages(idx) {
		if err := s.mark(p.Offset, PageSize, true); err != nil {
			s.regions = s.regions[:len(s.regions)-1]
			return err
		}
	}
	return nil
}

// FileEOF is ibFileEof: end of the last AMap region. See MS-PST 2.2.2.5.
func (s *Store) FileEOF() uint64 {
	if len(s.regions) == 0 {
		return FirstAMapPageOffset
	}
	return AMapRegionEnd(uint64(len(s.regions) - 1))
}

// AMapLast is ibAMapLast: offset of the last AMap page.
func (s *Store) AMapLast() uint64 {
	if len(s.regions) == 0 {
		return 0
	}
	return AMapOffset(uint64(len(s.regions) - 1))
}

// AMapFree is cbAMapFree: free 64-byte slots across all AMaps, in bytes.
func (s *Store) AMapFree() uint64 {
	var n int
	for i := range s.regions {
		n += countFree(s.regions[i].bitmap[:])
	}
	return uint64(n) * BytesPerSlot
}

// RegionCount is the number of AMap pages / coverage regions.
func (s *Store) RegionCount() int { return len(s.regions) }

// HeaderDraft returns a Unicode header whose ROOT matches this store.
// cbPMapFree is 0 (PMap is deprecated for new files). rgbFM/rgbFP stay 0xFF.
func (s *Store) HeaderDraft() HeaderDraft {
	d := DefaultHeaderDraft()
	d.Root.FileEOF = s.FileEOF()
	d.Root.AMapLast = s.AMapLast()
	d.Root.AMapFree = s.AMapFree()
	d.Root.PMapFree = 0
	d.Root.AMapValid = s.valid
	d.Root.NBTBID = s.nbtRoot.BID
	d.Root.NBTIB = s.nbtRoot.IB
	d.Root.BBTBID = s.bbtRoot.BID
	d.Root.BBTIB = s.bbtRoot.IB
	d.BidNextP = s.bidNextP
	d.BidNextB = s.bidNextB
	d.Unique = s.unique
	if d.Unique == 0 {
		d.Unique = 1
	}
	return d
}

// takeBlockBID assigns the next data-block BID from bidNextB (increment 4).
func (s *Store) takeBlockBID() (uint64, error) {
	return takeMonotonic(&s.bidNextB, BlockBIDIncrement)
}

// noteBlockBID raises bidNextB so it stays strictly beyond bid.
func (s *Store) noteBlockBID(bid uint64) error {
	if bid == 0 || BIDHasReserved(bid) {
		return invalidArg("bid", "invalid block BID 0x%x (MS-PST %s)", bid, SectionBID)
	}
	next := (bid &^ BIDInternal) + BlockBIDIncrement
	if next > s.bidNextB {
		s.bidNextB = next
	}
	return nil
}

// SetTreeRoots records ROOT BREFNBT/BREFBBT for the next header encode.
func (s *Store) SetTreeRoots(nbt, bbt BREF) {
	s.nbtRoot = nbt
	s.bbtRoot = bbt
}

// SetAMapValid records fAMapValid for the next header encode.
func (s *Store) SetAMapValid(v byte) error {
	switch v {
	case AMapValid2, AMapInvalid:
		s.valid = v
		return nil
	case AMapValid1:
		return invalidArg("fAMapValid", "VALID_AMAP1 (0x01) is deprecated (MS-PST %s)", SectionAMapTxn)
	default:
		return invalidArg("fAMapValid", "unknown 0x%02x (MS-PST %s)", v, SectionRoot)
	}
}

// Allocate first-fits size bytes (64-byte multiple) at the lowest offset.
// Grows by whole AMap regions when the request does not fit. The allocation
// never spans an AMap region. See MS-PST 2.6.1.1.2.
func (s *Store) Allocate(size uint64) (uint64, error) {
	if size > MaxAllocBytes {
		return 0, limitErr("size", "allocation %d exceeds MS-PST max %d", size, MaxAllocBytes)
	}
	if err := s.ensureSpool(); err != nil {
		return 0, err
	}
	slots, err := slotsForSize(size)
	if err != nil {
		return 0, err
	}
	if ib, ok := s.firstFit(slots); ok {
		if err := s.mark(ib, size, true); err != nil {
			return 0, err
		}
		idx, _ := AMapIndexForOffset(ib)
		s.lastAllocAMap = uint32(idx)
		return ib, nil
	}
	if err := s.Grow(); err != nil {
		return 0, err
	}
	ib, ok := s.firstFit(slots)
	if !ok {
		return 0, limitErr("size", "no contiguous %d-byte run in an AMap region (MS-PST %s)", size, SectionAMap)
	}
	if err := s.mark(ib, size, true); err != nil {
		return 0, err
	}
	idx, _ := AMapIndexForOffset(ib)
	s.lastAllocAMap = uint32(idx)
	return ib, nil
}

// AllocatePage first-fits a 512-byte page on a PageSize boundary so NBT/BBT
// IBs cannot collide with each other or with 64-byte data-block packing.
func (s *Store) AllocatePage() (uint64, error) {
	slots := int(PageSize / BytesPerSlot)
	if ib, ok := s.firstFitAligned(slots, uint64(PageSize)); ok {
		if err := s.mark(ib, PageSize, true); err != nil {
			return 0, err
		}
		idx, _ := AMapIndexForOffset(ib)
		s.lastAllocAMap = uint32(idx)
		return ib, nil
	}
	if err := s.Grow(); err != nil {
		return 0, err
	}
	ib, ok := s.firstFitAligned(slots, uint64(PageSize))
	if !ok {
		return 0, limitErr("size", "no 512-aligned page slot in an AMap region (MS-PST %s)", SectionAMap)
	}
	if err := s.mark(ib, PageSize, true); err != nil {
		return 0, err
	}
	idx, _ := AMapIndexForOffset(ib)
	s.lastAllocAMap = uint32(idx)
	return ib, nil
}

func (s *Store) firstFitAligned(slots int, align uint64) (uint64, bool) {
	for i := range s.regions {
		bm := s.regions[i].bitmap[:]
		n := SlotsPerAMap
		slot := 0
		for slot+slots <= n {
			ib := slotOffset(uint64(i), slot)
			if ib%align != 0 {
				slot++
				continue
			}
			fit := true
			for k := 0; k < slots; k++ {
				if bitIsSet(bm, slot+k) {
					fit = false
					slot = slot + k + 1
					break
				}
			}
			if fit {
				return ib, true
			}
		}
	}
	return 0, false
}

// Reserve marks [ib, ib+size) allocated. ib and size must be 64-byte aligned
// and lie inside mapped space. Fails if any slot is already allocated.
func (s *Store) Reserve(ib, size uint64) error {
	if size > MaxAllocBytes {
		return limitErr("size", "reserve %d exceeds MS-PST max %d", size, MaxAllocBytes)
	}
	if ib%BytesPerSlot != 0 {
		return invalidArg("ib", "offset 0x%x is not %d-byte aligned", ib, BytesPerSlot)
	}
	slots, err := slotsForSize(size)
	if err != nil {
		return err
	}
	idx, ok := AMapIndexForOffset(ib)
	if !ok {
		return invalidArg("ib", "offset 0x%x is before first AMap (MS-PST %s)", ib, SectionAMap)
	}
	if int(idx) >= len(s.regions) {
		return invalidArg("ib", "offset 0x%x is beyond ibAMapLast", ib)
	}
	if ib+size > AMapRegionEnd(idx) {
		return invalidArg("size", "reserve 0x%x+%d crosses AMap region (MS-PST %s)", ib, size, SectionAMap)
	}
	_ = slots
	return s.mark(ib, size, true)
}

// Free marks a previously allocated non-metadata run free.
func (s *Store) Free(ib, size uint64) error {
	if ib%BytesPerSlot != 0 {
		return invalidArg("ib", "offset 0x%x is not %d-byte aligned", ib, BytesPerSlot)
	}
	if _, err := slotsForSize(size); err != nil {
		return err
	}
	if s.coversMetadata(ib, size) {
		return invalidArg("ib", "cannot free metadata page at 0x%x (MS-PST %s)", ib, SectionAMap)
	}
	if err := s.ensureSpool(); err != nil {
		return err
	}
	return s.mark(ib, size, false)
}

func (s *Store) firstFit(slots int) (uint64, bool) {
	for i := range s.regions {
		start, ok := findContiguous(s.regions[i].bitmap[:], slots)
		if ok {
			return slotOffset(uint64(i), start), true
		}
	}
	return 0, false
}

func (s *Store) mark(ib, size uint64, alloc bool) error {
	idx, ok := AMapIndexForOffset(ib)
	if !ok {
		return invariant(SectionAMap, "ib", "offset 0x%x is not AMap-mapped", ib)
	}
	if int(idx) >= len(s.regions) {
		return invariant(SectionAMap, "ib", "offset 0x%x has no AMap page", ib)
	}
	if ib+size > AMapRegionEnd(idx) {
		return invariant(SectionAMap, "size", "range 0x%x+%d crosses AMap boundary 0x%x", ib, size, AMapRegionEnd(idx))
	}
	if ib%BytesPerSlot != 0 || size%BytesPerSlot != 0 {
		return invalidArg("size", "range 0x%x+%d is not slot-aligned", ib, size)
	}
	start := slotIndex(ib, idx)
	n := int(size / BytesPerSlot)
	bm := s.regions[idx].bitmap[:]
	for i := 0; i < n; i++ {
		set := bitIsSet(bm, start+i)
		if alloc && set {
			return invariant(SectionAMap, "slot", "slot at 0x%x already allocated", slotOffset(idx, start+i))
		}
		if !alloc && !set {
			return invariant(SectionAMap, "slot", "slot at 0x%x already free", slotOffset(idx, start+i))
		}
	}
	for i := 0; i < n; i++ {
		if alloc {
			setBit(bm, start+i)
		} else {
			clearBit(bm, start+i)
		}
	}
	s.clearBacking(ib, size)
	return nil
}

func (s *Store) clearBacking(ib, size uint64) {
	if size == 0 {
		return
	}
	if w := s.writer(); w != nil {
		_ = s.noteUndo(ib, int(size))
		z := make([]byte, size)
		_, _ = w.WriteAt(z, int64(ib))
	}
}

func (s *Store) allocatedRange(ib, size uint64) bool {
	idx, ok := AMapIndexForOffset(ib)
	if !ok || int(idx) >= len(s.regions) {
		return false
	}
	if ib+size > AMapRegionEnd(idx) || ib%BytesPerSlot != 0 || size%BytesPerSlot != 0 {
		return false
	}
	start := slotIndex(ib, idx)
	n := int(size / BytesPerSlot)
	bm := s.regions[idx].bitmap[:]
	for i := 0; i < n; i++ {
		if !bitIsSet(bm, start+i) {
			return false
		}
	}
	return true
}

func (s *Store) anyAllocated(ib, size uint64) bool {
	idx, ok := AMapIndexForOffset(ib)
	if !ok || int(idx) >= len(s.regions) {
		return false
	}
	end := ib + size
	if end > AMapRegionEnd(idx) {
		end = AMapRegionEnd(idx)
	}
	if ib < AMapRegionStart(idx) {
		ib = AMapRegionStart(idx)
	}
	bm := s.regions[idx].bitmap[:]
	for off := ib; off < end; off += BytesPerSlot {
		if bitIsSet(bm, slotIndex(off, idx)) {
			return true
		}
	}
	return false
}

func (s *Store) coversMetadata(ib, size uint64) bool {
	end := ib + size
	for i := range s.regions {
		for _, p := range RegionMapPages(uint64(i)) {
			if p.Offset < end && p.Offset+PageSize > ib {
				return true
			}
		}
	}
	return false
}

func (s *Store) metadataOffsets() []uint64 {
	var out []uint64
	for i := range s.regions {
		for _, p := range RegionMapPages(uint64(i)) {
			out = append(out, p.Offset)
		}
	}
	return out
}

// BitmapEqual reports whether both stores have identical AMap coverage.
func (s *Store) BitmapEqual(o *Store) bool {
	if s == nil || o == nil || len(s.regions) != len(o.regions) {
		return false
	}
	for i := range s.regions {
		if s.regions[i].bitmap != o.regions[i].bitmap {
			return false
		}
	}
	return true
}

func bitIsSet(b []byte, i int) bool {
	return b[i/8]&(1<<(i%8)) != 0
}

func setBit(b []byte, i int) {
	b[i/8] |= 1 << (i % 8)
}

func clearBit(b []byte, i int) {
	b[i/8] &^= 1 << (i % 8)
}

func countFree(b []byte) int {
	n := 0
	for _, x := range b {
		for i := 0; i < 8; i++ {
			if x&(1<<i) == 0 {
				n++
			}
		}
	}
	return n
}

func longestFree(b []byte) int {
	max, cur := 0, 0
	for i := 0; i < len(b)*8; i++ {
		if !bitIsSet(b, i) {
			cur++
			if cur > max {
				max = cur
			}
		} else {
			cur = 0
		}
	}
	return max
}

func findContiguous(b []byte, slots int) (int, bool) {
	run, start := 0, 0
	for i := 0; i < len(b)*8; i++ {
		if !bitIsSet(b, i) {
			if run == 0 {
				start = i
			}
			run++
			if run >= slots {
				return start, true
			}
		} else {
			run = 0
		}
	}
	return 0, false
}

type encodedPage struct {
	off uint64
	raw []byte
}

func (s *Store) encodeRegionPages(index uint64) ([]encodedPage, error) {
	var out []encodedPage
	for _, p := range RegionMapPages(index) {
		raw, err := s.encodeMapPage(p)
		if err != nil {
			return nil, err
		}
		out = append(out, encodedPage{off: p.Offset, raw: raw})
	}
	return out, nil
}

func (s *Store) encodeMapPage(p MapPage) ([]byte, error) {
	idx, ok := AMapIndexForOffset(p.Offset)
	if !ok {
		return nil, invalidArg("ib", "map page at 0x%x is outside AMap coverage", p.Offset)
	}
	switch p.Type {
	case PageAMap:
		return EncodePage(s.regions[idx].bitmap[:], PageAMap, p.Offset, p.Offset)
	case PagePMap:
		return s.encodePMap(PMapIndexForAMap(idx))
	case PageFMap:
		k := (idx - FMapHeaderAMaps) / FMapPageAMaps
		return s.encodeFMap(k)
	case PageFPMap:
		return s.encodeFPMapAt(p.Offset)
	default:
		return nil, invalidArg("ptype", "not a map page 0x%02x", p.Type)
	}
}

func (s *Store) encodePMap(pmapIndex uint64) ([]byte, error) {
	var bm [AMapBitmapBytes]byte
	start := FirstAMapPageOffset + pmapIndex*PMapCoverageBytes
	for bit := 0; bit < SlotsPerAMap; bit++ {
		ib := start + uint64(bit)*uint64(PageSize)
		if s.anyAllocated(ib, PageSize) {
			setBit(bm[:], bit)
		}
	}
	off := PMapOffset(pmapIndex)
	return EncodePage(bm[:], PagePMap, off, off)
}

func (s *Store) encodeFMap(k uint64) ([]byte, error) {
	payload := make([]byte, AMapBitmapBytes)
	base := FMapHeaderAMaps + k*FMapPageAMaps
	for i := 0; i < FMapPageAMaps; i++ {
		idx := int(base) + i
		if idx < 0 || idx >= len(s.regions) {
			continue
		}
		n := longestFree(s.regions[idx].bitmap[:])
		if n > 255 {
			n = 255
		}
		payload[i] = byte(n)
	}
	off := FMapPageOffset(k)
	return EncodePage(payload, PageFMap, off, off)
}

func (s *Store) encodeFPMapAt(off uint64) ([]byte, error) {
	idx, ok := AMapIndexForOffset(off)
	if !ok {
		return nil, invalidArg("ib", "FPMap at 0x%x is outside AMap coverage", off)
	}
	pmap := PMapIndexForAMap(idx)
	if pmap < FPMapHeaderPMaps {
		return nil, invalidArg("ib", "FPMap at 0x%x is in HEADER.rgbFP range", off)
	}
	k := (pmap - FPMapHeaderPMaps) / FPMapPagePMaps
	start := FPMapHeaderPMaps + k*FPMapPagePMaps
	return EncodePage(s.fpMapPayload(start), PageFPMap, off, off)
}

// pMapHasFreePages reports whether PMap index still has a fully free 512-byte
// page inside current file coverage. Pages beyond ibFileEof and PMaps that do
// not exist are not free: FPMap bit 1. See MS-PST 2.2.2.7.6.
func (s *Store) pMapHasFreePages(pmapIndex uint64) bool {
	start := FirstAMapPageOffset + pmapIndex*PMapCoverageBytes
	eof := s.FileEOF()
	for bit := 0; bit < SlotsPerAMap; bit++ {
		ib := start + uint64(bit)*uint64(PageSize)
		if ib+uint64(PageSize) > eof {
			continue
		}
		if !s.anyAllocated(ib, PageSize) {
			return true
		}
	}
	return false
}

func (s *Store) fpMapPayload(startPMap uint64) []byte {
	payload := make([]byte, AMapBitmapBytes)
	for bit := 0; bit < SlotsPerAMap; bit++ {
		if !s.pMapHasFreePages(startPMap + uint64(bit)) {
			setBit(payload, bit)
		}
	}
	return payload
}

func (s *Store) encodeDList() ([]byte, error) {
	type ent struct {
		pageNum uint32
		free    int
	}
	ents := make([]ent, 0, len(s.regions))
	for i := range s.regions {
		free := countFree(s.regions[i].bitmap[:])
		if free == 0 {
			continue
		}
		ents = append(ents, ent{
			pageNum: uint32(i), // zero-based AMap index (MS-PST 2.2.2.7.4.1)
			free:    free,
		})
	}
	sort.SliceStable(ents, func(i, j int) bool {
		if ents[i].free != ents[j].free {
			return ents[i].free > ents[j].free
		}
		return ents[i].pageNum < ents[j].pageNum
	})
	if len(ents) > DListMaxEntries {
		ents = ents[:DListMaxEntries]
	}
	payload := make([]byte, AMapBitmapBytes)
	payload[0] = DFLBackfillComplete
	payload[1] = byte(len(ents))
	binary.LittleEndian.PutUint32(payload[4:8], s.lastAllocAMap)
	for i, e := range ents {
		v := (e.pageNum & 0xFFFFF) | uint32(e.free)<<20
		binary.LittleEndian.PutUint32(payload[8+i*4:], v)
	}
	return EncodePage(payload, PageDList, s.dlistBID, DListPageOffset)
}

type undoExtent struct {
	ib   uint64
	orig []byte
}

type undoLog struct {
	eof       uint64
	extents   []undoExtent
	restoring bool
}

func (s *Store) beginUndo() {
	if s == nil {
		return
	}
	s.undo = &undoLog{eof: s.FileEOF()}
}

func (s *Store) endUndo() {
	if s != nil {
		s.undo = nil
	}
}

func (u *undoLog) skipCovered(pos, end uint64) (covered bool, next uint64) {
	next = end
	for _, e := range u.extents {
		eEnd := e.ib + uint64(len(e.orig))
		if pos >= e.ib && pos < eEnd {
			return true, eEnd
		}
		if e.ib > pos && e.ib < next {
			next = e.ib
		}
	}
	return false, next
}

func (s *Store) noteUndo(off uint64, n int) error {
	if s == nil || s.undo == nil || s.undo.restoring || n <= 0 {
		return nil
	}
	start := off
	end := off + uint64(n)
	if start >= s.undo.eof {
		return nil
	}
	if end > s.undo.eof {
		end = s.undo.eof
	}
	for pos := start; pos < end; {
		covered, next := s.undo.skipCovered(pos, end)
		if covered {
			pos = next
			continue
		}
		orig, err := s.readExtent(pos, int(next-pos))
		if err != nil {
			orig = make([]byte, int(next-pos))
		}
		s.undo.extents = append(s.undo.extents, undoExtent{ib: pos, orig: orig})
		pos = next
	}
	return nil
}

func (s *Store) restoreUndo() error {
	if s == nil || s.undo == nil {
		return nil
	}
	w := s.writer()
	if w == nil {
		return nil
	}
	s.undo.restoring = true
	defer func() { s.undo.restoring = false }()
	for _, e := range s.undo.extents {
		if _, err := w.WriteAt(e.orig, int64(e.ib)); err != nil {
			return ioErr("snapshot", "undo 0x%x: %v", e.ib, err)
		}
	}
	return nil
}

func (s *Store) writeExtent(ib uint64, raw []byte) error {
	if len(raw) == 0 {
		return nil
	}
	end := ib + uint64(len(raw))
	if end > s.FileEOF() {
		return invariant(SectionAMap, "ib", "write 0x%x+%d exceeds ibFileEof 0x%x", ib, len(raw), s.FileEOF())
	}
	if err := s.ensureSpool(); err != nil {
		return err
	}
	if err := s.noteUndo(ib, len(raw)); err != nil {
		return err
	}
	_, err := s.writer().WriteAt(raw, int64(ib))
	return err
}

func (s *Store) ensureSpool() error {
	if s.work != nil && s.work.w != nil {
		return nil
	}
	f, err := createTemp("", "pst-v2-*.spool")
	if err != nil {
		return ioErr("spool", "create: %v", err)
	}
	sk := &FileSink{f: f}
	var src io.ReaderAt
	if s.io != nil {
		src = s.io.r
	}
	if src != nil && !sameIO(src, sk) {
		if err := copyWork(sk, src, int64(s.FileEOF())); err != nil {
			_ = f.Close()
			_ = removeFile(f.Name())
			return ioErr("spool", "seed: %v", err)
		}
	}
	s.work = &ioHandle{r: sk, w: sk, life: lifeTemp}
	return nil
}

func (s *Store) closeOwned() error {
	var err error
	if s.work != nil {
		err = s.work.close()
		s.work = nil
	}
	if s.io != nil {
		err = errorsJoin(err, s.io.close())
	}
	return err
}

func (s *Store) ownClose() {
	if s.io != nil && s.io.w != nil {
		s.io.life = lifeClose
	}
}

func (s *Store) attachSource(r io.ReaderAt) {
	h := &ioHandle{r: r, life: lifeBorrowed}
	if sk, ok := r.(Sink); ok {
		h.w = sk
	}
	s.io = h
}

func (s *Store) readExtent(ib uint64, size int) ([]byte, error) {
	if size == 0 {
		return []byte{}, nil
	}
	raw := make([]byte, size)
	try := func(r io.ReaderAt) bool {
		if r == nil {
			return false
		}
		nr, err := r.ReadAt(raw, int64(ib))
		return nr >= size && (err == nil || err == io.EOF)
	}
	if s.work != nil && try(s.work.r) {
		return raw, nil
	}
	if s.io != nil && try(s.io.r) {
		return raw, nil
	}
	return nil, invariant(SectionBlockTrailer, "ib", "missing payload at 0x%x", ib)
}

// Close closes/removes only owned handles. Borrowed caller sinks stay open.
func (s *Store) Close() error {
	return s.closeOwned()
}

func errorsJoin(a, b error) error {
	if a == nil {
		return b
	}
	if b == nil {
		return a
	}
	return fmt.Errorf("%w; %v", a, b)
}

func readAtFull(r io.ReaderAt, off int64, n int) ([]byte, error) {
	buf := make([]byte, n)
	got := 0
	for got < n {
		nr, err := r.ReadAt(buf[got:], off+int64(got))
		got += nr
		if got >= n {
			return buf, nil
		}
		if err != nil {
			return nil, err
		}
		if nr == 0 {
			return nil, io.ErrUnexpectedEOF
		}
	}
	return buf, nil
}

func copyReaderAt(dst io.WriterAt, src io.ReaderAt, n int64) error {
	return copyReaderAtFrom(dst, src, 0, n)
}

func copyReaderAtFrom(dst io.WriterAt, src io.ReaderAt, from, n int64) error {
	if from < 0 || n < 0 {
		return invalidArg("offset", "copy range from=%d n=%d", from, n)
	}
	if from >= n {
		return nil
	}
	const chunk = 64 << 10
	buf := make([]byte, chunk)
	for off := from; off < n; off += int64(chunk) {
		c := chunk
		if rem := n - off; rem < int64(c) {
			c = int(rem)
		}
		nr, err := src.ReadAt(buf[:c], off)
		if nr > 0 {
			if _, werr := dst.WriteAt(buf[:nr], off); werr != nil {
				return werr
			}
		}
		if err != nil && err != io.EOF {
			return err
		}
		if nr == 0 {
			break
		}
	}
	return nil
}

func (s *Store) zeroFreeSlotsAt(w io.WriterAt) error {
	for i := range s.regions {
		bm := s.regions[i].bitmap[:]
		slot := 0
		for slot < SlotsPerAMap {
			if bitIsSet(bm, slot) {
				slot++
				continue
			}
			start := slot
			for slot < SlotsPerAMap && !bitIsSet(bm, slot) {
				slot++
			}
			z := make([]byte, uint64(slot-start)*BytesPerSlot)
			off := slotOffset(uint64(i), start)
			if _, err := w.WriteAt(z, int64(off)); err != nil {
				return err
			}
		}
	}
	return nil
}

// WriteTo materializes a complete VALID_AMAP2 PST onto dst without allocating FileEOF.
func (s *Store) WriteTo(dst Sink) error {
	if err := s.writeHeader(dst, AMapInvalid); err != nil {
		return err
	}
	if err := syncSink(dst); err != nil {
		return err
	}
	if err := s.writeBody(dst); err != nil {
		return err
	}
	if err := syncSink(dst); err != nil {
		return err
	}
	if err := s.writeHeader(dst, AMapValid2); err != nil {
		return err
	}
	if err := syncSink(dst); err != nil {
		return err
	}
	s.valid = AMapValid2
	return nil
}

// residentImageBytes is in-process image memory (MemSink), not FileSink.
func (s *Store) residentImageBytes() int {
	if m, ok := s.writer().(*MemSink); ok {
		return len(m.buf)
	}
	if m, ok := s.reader().(*MemSink); ok {
		return len(m.buf)
	}
	return 0
}

func (s *Store) ShrinkTrailingEmpty(minRegions int) error {
	if minRegions < 1 {
		minRegions = 1
	}
	for len(s.regions) > minRegions {
		idx := len(s.regions) - 1
		if s.regionHasUserAlloc(idx) {
			break
		}
		s.regions = s.regions[:idx]
	}
	if uint32(len(s.regions)) > 0 && s.lastAllocAMap >= uint32(len(s.regions)) {
		s.lastAllocAMap = uint32(len(s.regions) - 1)
	}
	if tr, ok := s.writer().(interface{ Truncate(int64) error }); ok {
		if err := tr.Truncate(int64(s.FileEOF())); err != nil {
			return ioErr("spool", "truncate: %v", err)
		}
	}
	return nil
}

func (s *Store) regionHasUserAlloc(idx int) bool {
	if idx < 0 || idx >= len(s.regions) {
		return false
	}
	meta := make(map[uint64]struct{})
	for _, p := range RegionMapPages(uint64(idx)) {
		for off := p.Offset; off < p.Offset+PageSize; off += BytesPerSlot {
			meta[off] = struct{}{}
		}
	}
	bm := s.regions[idx].bitmap[:]
	for slot := 0; slot < SlotsPerAMap; slot++ {
		if !bitIsSet(bm, slot) {
			continue
		}
		off := slotOffset(uint64(idx), slot)
		if _, ok := meta[off]; !ok {
			return true
		}
	}
	return false
}

func (s *Store) zeroFreeSlots(buf []byte) {
	for i := range s.regions {
		bm := s.regions[i].bitmap[:]
		for slot := 0; slot < SlotsPerAMap; slot++ {
			if bitIsSet(bm, slot) {
				continue
			}
			off := slotOffset(uint64(i), slot)
			if off >= uint64(len(buf)) {
				continue
			}
			end := off + BytesPerSlot
			if end > uint64(len(buf)) {
				end = uint64(len(buf))
			}
			clear := buf[off:end]
			for j := range clear {
				clear[j] = 0
			}
		}
	}
}

// EncodeFile writes a Unicode PST image: HEADER, DList at 0x4200, and every
// periodic map page. Allocated non-metadata payloads from a loaded/previous
// image are preserved; freed or freshly allocated slots are zeroed so reuse
// cannot inherit stale bytes. ROOT matches the maps.
func (s *Store) EncodeFile() ([]byte, error) {
	ms := NewMemSink("encode")
	if err := s.WriteTo(ms); err != nil {
		return nil, err
	}
	return ms.Bytes(), nil
}

// LoadStore reconstructs a Store from a Unicode PST image by reading AMaps.
func LoadStore(file []byte) (*Store, error) {
	return LoadStoreFrom(&MemSink{name: "load", buf: file}, int64(len(file)))
}

func LoadStoreFrom(r io.ReaderAt, size int64) (*Store, error) {
	if r == nil {
		return nil, invalidArg("reader", "nil store reader")
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
	if uint64(size) != h.Root.FileEOF {
		return nil, invariant(SectionRoot, "ibFileEof", "file length %d != ibFileEof %d", size, h.Root.FileEOF)
	}
	if h.Root.AMapLast < FirstAMapPageOffset || (h.Root.AMapLast-FirstAMapPageOffset)%AMapCoverageBytes != 0 {
		return nil, invariant(SectionRoot, "ibAMapLast", "0x%x is not an AMap page offset (MS-PST %s)", h.Root.AMapLast, SectionAMap)
	}
	last := (h.Root.AMapLast - FirstAMapPageOffset) / AMapCoverageBytes
	wantEOF := AMapRegionEnd(last)
	if h.Root.FileEOF != wantEOF {
		return nil, invariant(SectionGrow, "ibFileEof", "got 0x%x want 0x%x (growth is whole AMap regions, MS-PST %s)", h.Root.FileEOF, wantEOF, SectionGrow)
	}
	if h.Root.PMapFree != 0 {
		return nil, invariant(SectionRoot, "cbPMapFree", "got %d want 0 for new Unicode PST", h.Root.PMapFree)
	}
	if size < int64(DListPageOffset)+int64(PageSize) {
		return nil, invariant(SectionDList, "size", "file too small for DList at 0x%x", DListPageOffset)
	}
	dlistRaw, err := readAtFull(r, int64(DListPageOffset), PageSize)
	if err != nil {
		return nil, invariant(SectionDList, "size", "DList read: %v", err)
	}
	dl, err := InspectDList(dlistRaw)
	if err != nil {
		return nil, err
	}
	if h.BidNextP <= dl.Page.BID {
		return nil, invariant(SectionBID, "bidNextP", "got %d, not strictly beyond DList BID %d (MS-PST %s)", h.BidNextP, dl.Page.BID, SectionBID)
	}
	for _, e := range dl.Entries {
		if uint64(e.PageNum) > last {
			return nil, invariant(SectionDList, "dwPageNum", "AMap index %d is beyond last AMap %d", e.PageNum, last)
		}
	}
	if uint64(dl.Current) > last {
		return nil, invariant(SectionDList, "ulCurrentPage", "current AMap %d is beyond last AMap %d", dl.Current, last)
	}
	if h.BidNextB < FirstAllocBID || h.BidNextB&3 != 0 {
		return nil, invariant(SectionBID, "bidNextB", "got %d, want >= %d and 4-aligned (MS-PST %s)", h.BidNextB, FirstAllocBID, SectionBID)
	}
	s := &Store{
		valid:         h.Root.AMapValid,
		lastAllocAMap: dl.Current,
		bidNextP:      h.BidNextP,
		bidNextB:      h.BidNextB,
		dlistBID:      dl.Page.BID,
		unique:        h.Unique,
		nbtRoot:       BREF{BID: h.Root.NBTBID, IB: h.Root.NBTIB},
		bbtRoot:       BREF{BID: h.Root.BBTBID, IB: h.Root.BBTIB},
	}
	s.regions = make([]region, last+1)
	for i := uint64(0); i <= last; i++ {
		off := AMapOffset(i)
		end := off + PageSize
		if uint64(size) < end {
			return nil, invariant(SectionAMap, "size", "truncated AMap at 0x%x", off)
		}
		amapRaw, err := readAtFull(r, int64(off), PageSize)
		if err != nil {
			return nil, invariant(SectionAMap, "size", "AMap read at 0x%x: %v", off, err)
		}
		pg, err := InspectPage(amapRaw, off)
		if err != nil {
			return nil, err
		}
		if pg.Type != PageAMap {
			return nil, invariant(SectionAMap, "ptype", "page at 0x%x is 0x%02x, want AMap", off, pg.Type)
		}
		copy(s.regions[i].bitmap[:], pg.Payload[:AMapBitmapBytes])
		if pg.Payload[0] != 0xFF {
			return nil, invariant(SectionAMap, "rgbAMap", "AMap at 0x%x does not self-map (first byte 0x%02x want 0xFF)", off, pg.Payload[0])
		}
		for _, p := range RegionMapPages(i) {
			if uint64(size) < p.Offset+PageSize {
				return nil, invariant(sectionForPage(p.Type), "size", "truncated %s at 0x%x", pageName(p.Type), p.Offset)
			}
			mapRaw, err := readAtFull(r, int64(p.Offset), PageSize)
			if err != nil {
				return nil, invariant(sectionForPage(p.Type), "size", "%s read at 0x%x: %v", pageName(p.Type), p.Offset, err)
			}
			mp, err := InspectPage(mapRaw, p.Offset)
			if err != nil {
				return nil, err
			}
			if mp.Type != p.Type {
				return nil, invariant(sectionForPage(p.Type), "ptype", "page at 0x%x is 0x%02x want 0x%02x", p.Offset, mp.Type, p.Type)
			}
			if !s.allocatedRange(p.Offset, PageSize) {
				return nil, invariant(SectionAMap, "slot", "metadata %s at 0x%x marked free", pageName(p.Type), p.Offset)
			}
		}
	}
	if s.AMapFree() != h.Root.AMapFree {
		return nil, invariant(SectionRoot, "cbAMapFree", "bitmap free %d != ROOT %d", s.AMapFree(), h.Root.AMapFree)
	}
	s.attachSource(r)
	return s, nil
}

func sectionForPage(t byte) string {
	switch t {
	case PageAMap:
		return SectionAMap
	case PagePMap:
		return SectionPMap
	case PageFMap:
		return SectionFMap
	case PageFPMap:
		return SectionFPMap
	case PageDList:
		return SectionDList
	default:
		return SectionPageTrailer
	}
}

func pageName(t byte) string {
	switch t {
	case PageAMap:
		return "AMap"
	case PagePMap:
		return "PMap"
	case PageFMap:
		return "FMap"
	case PageFPMap:
		return "FPMap"
	case PageDList:
		return "DList"
	default:
		return fmt.Sprintf("0x%02x", t)
	}
}
