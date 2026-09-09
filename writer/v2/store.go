package writer

import (
	"encoding/binary"
	"fmt"
	"sort"
)

// Store is the in-memory Unicode allocation map. AMap is authoritative.
// Growth is in whole AMap regions with periodic PMap/FMap/FPMap pages.
// See MS-PST 2.2.2.7.2 and 2.6.1.1.2.
type Store struct {
	regions       []region
	lastAllocAMap uint32
	valid         byte
}

type region struct {
	bitmap [AMapBitmapBytes]byte
}

// NewStore creates a Unicode PST with the first AMap at 0x4400, the first
// PMap at 0x4600, and a DList at 0x4200. Header/DList live in the unmapped
// prefix before 0x4400.
func NewStore() *Store {
	s := &Store{valid: AMapValid2}
	s.mustGrow()
	return s
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
	return d
}

// SetAMapValid records fAMapValid for the next header encode.
func (s *Store) SetAMapValid(v byte) error {
	switch v {
	case AMapValid2, AMapInvalid, AMapValid1:
		s.valid = v
		return nil
	default:
		return invalidArg("fAMapValid", "unknown 0x%02x (MS-PST %s)", v, SectionRoot)
	}
}

// Allocate first-fits size bytes (64-byte multiple) at the lowest offset.
// Grows by whole AMap regions when the request does not fit. The allocation
// never spans an AMap region. See MS-PST 2.6.1.1.2.
func (s *Store) Allocate(size uint64) (uint64, error) {
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

// Reserve marks [ib, ib+size) allocated. ib and size must be 64-byte aligned
// and lie inside mapped space. Fails if any slot is already allocated.
func (s *Store) Reserve(ib, size uint64) error {
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
	return nil
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
	payload := bytesFilled(AMapBitmapBytes, 0xFF)
	return EncodePage(payload, PageFPMap, off, off)
}

func bytesFilled(n int, v byte) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = v
	}
	return b
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
			pageNum: uint32(AMapOffset(uint64(i)) / uint64(PageSize)),
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
	return EncodePage(payload, PageDList, 0, DListPageOffset)
}

// EncodeFile writes a Unicode PST image: HEADER, DList at 0x4200, and every
// periodic map page. Data slots are left zero. ROOT matches the maps.
func (s *Store) EncodeFile() ([]byte, error) {
	hdr, err := EncodeUnicodeHeader(s.HeaderDraft())
	if err != nil {
		return nil, err
	}
	buf := make([]byte, s.FileEOF())
	copy(buf, hdr)
	dlist, err := s.encodeDList()
	if err != nil {
		return nil, err
	}
	copy(buf[DListPageOffset:], dlist)
	for i := range s.regions {
		pages, err := s.encodeRegionPages(uint64(i))
		if err != nil {
			return nil, err
		}
		for _, p := range pages {
			if int(p.off)+len(p.raw) > len(buf) {
				return nil, invariant(SectionAMap, "ib", "map page at 0x%x exceeds ibFileEof", p.off)
			}
			copy(buf[p.off:], p.raw)
		}
	}
	return buf, nil
}

// LoadStore reconstructs a Store from a Unicode PST image by reading AMaps.
func LoadStore(file []byte) (*Store, error) {
	if len(file) < UnicodeHeaderSize {
		return nil, invariant(SectionHeader, "size", "file is %d bytes, need header", len(file))
	}
	h, err := InspectHeader(file[:UnicodeHeaderSize])
	if err != nil {
		return nil, err
	}
	if uint64(len(file)) != h.Root.FileEOF {
		return nil, invariant(SectionRoot, "ibFileEof", "file length %d != ibFileEof %d", len(file), h.Root.FileEOF)
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
	if len(file) < int(DListPageOffset)+PageSize {
		return nil, invariant(SectionDList, "size", "file too small for DList at 0x%x", DListPageOffset)
	}
	dl, err := InspectPage(file[DListPageOffset:DListPageOffset+PageSize], DListPageOffset)
	if err != nil {
		return nil, err
	}
	if dl.Type != PageDList {
		return nil, invariant(SectionDList, "ptype", "got 0x%02x want DList", dl.Type)
	}
	s := &Store{valid: h.Root.AMapValid, lastAllocAMap: binary.LittleEndian.Uint32(dl.Payload[4:8])}
	s.regions = make([]region, last+1)
	for i := uint64(0); i <= last; i++ {
		off := AMapOffset(i)
		end := off + PageSize
		if uint64(len(file)) < end {
			return nil, invariant(SectionAMap, "size", "truncated AMap at 0x%x", off)
		}
		pg, err := InspectPage(file[off:end], off)
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
			if uint64(len(file)) < p.Offset+PageSize {
				return nil, invariant(sectionForPage(p.Type), "size", "truncated %s at 0x%x", pageName(p.Type), p.Offset)
			}
			mp, err := InspectPage(file[p.Offset:p.Offset+PageSize], p.Offset)
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
