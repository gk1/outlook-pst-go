package writer

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"

	"github.com/grokify/outlook-pst-go/pkg/disk"
)

func TestGeometryIntervals(t *testing.T) {
	if AMapCoverageBytes != 496*8*64 {
		t.Fatalf("AMap coverage %d", AMapCoverageBytes)
	}
	if PMapCoverageBytes != 8*AMapCoverageBytes {
		t.Fatalf("PMap coverage %d", PMapCoverageBytes)
	}
	if AMapOffset(0) != FirstAMapPageOffset {
		t.Fatalf("AMap0 0x%x", AMapOffset(0))
	}
	if AMapOffset(1) != FirstAMapPageOffset+AMapCoverageBytes {
		t.Fatalf("AMap1 0x%x", AMapOffset(1))
	}
	if PMapOffset(0) != FirstPMapPageOffset {
		t.Fatalf("PMap0 0x%x", PMapOffset(0))
	}
	if PMapOffset(1) != FirstPMapPageOffset+PMapCoverageBytes {
		t.Fatalf("PMap1 0x%x", PMapOffset(1))
	}
	if FMapPageOffset(0) != AMapOffset(128)+2*PageSize {
		t.Fatalf("FMap0 0x%x want 0x%x", FMapPageOffset(0), AMapOffset(128)+2*PageSize)
	}
	if FPMapPageOffset(0) != AMapOffset(FPMapHeaderPMaps*AMapsPerPMap)+2*PageSize {
		t.Fatalf("FPMap0 0x%x", FPMapPageOffset(0))
	}
}

func TestRegionMapPages(t *testing.T) {
	p0 := RegionMapPages(0)
	if len(p0) != 2 || p0[0].Type != PageAMap || p0[1].Type != PagePMap {
		t.Fatalf("region 0: %+v", p0)
	}
	p1 := RegionMapPages(1)
	if len(p1) != 1 || p1[0].Type != PageAMap {
		t.Fatalf("region 1: %+v", p1)
	}
	p8 := RegionMapPages(8)
	if len(p8) != 2 || p8[1].Type != PagePMap {
		t.Fatalf("region 8: %+v", p8)
	}
	p128 := RegionMapPages(128)
	if len(p128) != 3 || p128[2].Type != PageFMap {
		t.Fatalf("region 128: %+v", p128)
	}
	p8192 := RegionMapPages(FPMapHeaderPMaps * AMapsPerPMap)
	foundFP := false
	for _, p := range p8192 {
		if p.Type == PageFPMap {
			foundFP = true
		}
	}
	if !foundFP {
		t.Fatalf("region 8192 missing FPMap: %+v", p8192)
	}
}

func TestEmptyStoreMapsAndHeader(t *testing.T) {
	s := NewStore()
	if s.RegionCount() != 1 {
		t.Fatalf("regions %d", s.RegionCount())
	}
	if s.AMapLast() != FirstAMapPageOffset {
		t.Fatalf("ibAMapLast 0x%x", s.AMapLast())
	}
	if s.FileEOF() != AMapRegionEnd(0) {
		t.Fatalf("ibFileEof 0x%x want 0x%x", s.FileEOF(), AMapRegionEnd(0))
	}
	wantFree := uint64(SlotsPerAMap-16) * BytesPerSlot
	if s.AMapFree() != wantFree {
		t.Fatalf("cbAMapFree %d want %d", s.AMapFree(), wantFree)
	}
	raw, err := s.EncodeFile()
	if err != nil {
		t.Fatal(err)
	}
	if err := CheckAllocation(raw); err != nil {
		t.Fatal(err)
	}
	h, err := InspectHeader(raw[:UnicodeHeaderSize])
	if err != nil {
		t.Fatal(err)
	}
	if h.Root.AMapValid != AMapValid2 || h.Root.PMapFree != 0 {
		t.Fatalf("ROOT %+v", h.Root)
	}
	amap, err := InspectAMap(raw[FirstAMapPageOffset:FirstAMapPageOffset+PageSize], FirstAMapPageOffset)
	if err != nil {
		t.Fatal(err)
	}
	if amap.Sig != 0 || amap.BID != FirstAMapPageOffset {
		t.Fatalf("AMap trailer sig=%d bid=0x%x", amap.Sig, amap.BID)
	}
	pmap, err := InspectPage(raw[FirstPMapPageOffset:FirstPMapPageOffset+PageSize], FirstPMapPageOffset)
	if err != nil {
		t.Fatal(err)
	}
	if pmap.Type != PagePMap || pmap.Sig != 0 || pmap.BID != FirstPMapPageOffset {
		t.Fatalf("PMap %+v", pmap)
	}
	if pmap.Payload[0]&0x03 != 0x03 {
		t.Fatalf("PMap first bits 0x%02x", pmap.Payload[0])
	}
	dl, err := InspectDList(raw[DListPageOffset : DListPageOffset+PageSize])
	if err != nil {
		t.Fatal(err)
	}
	if dl.Flags != DFLBackfillComplete || dl.Count != 1 || dl.Entries[0].PageNum != uint32(FirstAMapPageOffset/PageSize) {
		t.Fatalf("DList %+v", dl)
	}
	loaded, err := disk.ReadHeader(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Root.IBFileEOF != s.FileEOF() || loaded.Root.IBAMapLast != s.AMapLast() {
		t.Fatalf("reader ROOT eof=%d last=%d", loaded.Root.IBFileEOF, loaded.Root.IBAMapLast)
	}
}

func TestAllocateFirstAndLastSlot(t *testing.T) {
	s := NewStore()
	first, err := s.Allocate(BytesPerSlot)
	if err != nil {
		t.Fatal(err)
	}
	if first != FirstPMapPageOffset+PageSize {
		t.Fatalf("first slot 0x%x want 0x%x", first, FirstPMapPageOffset+PageSize)
	}
	lastIB := AMapRegionEnd(0) - BytesPerSlot
	if err := s.Reserve(lastIB, BytesPerSlot); err != nil {
		t.Fatal(err)
	}
	raw, err := s.EncodeFile()
	if err != nil {
		t.Fatal(err)
	}
	if err := CheckAllocation(raw); err != nil {
		t.Fatal(err)
	}
	re, err := LoadStore(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !s.BitmapEqual(re) {
		t.Fatal("reopen bitmap mismatch")
	}
}

func TestAllocateCrossesAMapRegion(t *testing.T) {
	s := NewStore()
	first, err := s.Allocate(BytesPerSlot)
	if err != nil {
		t.Fatal(err)
	}
	fill := uint64(SlotsPerAMap-18) * BytesPerSlot
	ib, err := s.Allocate(fill)
	if err != nil {
		t.Fatal(err)
	}
	if ib != first+BytesPerSlot {
		t.Fatalf("fill at 0x%x", ib)
	}
	if s.RegionCount() != 1 {
		t.Fatalf("grew early: %d", s.RegionCount())
	}
	grew, err := s.Allocate(2 * BytesPerSlot)
	if err != nil {
		t.Fatal(err)
	}
	if s.RegionCount() != 2 {
		t.Fatalf("want 2 regions, got %d", s.RegionCount())
	}
	want := AMapOffset(1) + PageSize
	if grew != want {
		t.Fatalf("grew alloc 0x%x want 0x%x", grew, want)
	}
	raw, err := s.EncodeFile()
	if err != nil {
		t.Fatal(err)
	}
	if err := CheckAllocation(raw); err != nil {
		t.Fatal(err)
	}
}

func TestFragmentedAllocateFreeReuse(t *testing.T) {
	s := NewStore()
	a, err := s.Allocate(BytesPerSlot)
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.Allocate(BytesPerSlot)
	if err != nil {
		t.Fatal(err)
	}
	c, err := s.Allocate(BytesPerSlot)
	if err != nil {
		t.Fatal(err)
	}
	if b != a+BytesPerSlot || c != b+BytesPerSlot {
		t.Fatalf("contiguous 0x%x 0x%x 0x%x", a, b, c)
	}
	if err := s.Free(b, BytesPerSlot); err != nil {
		t.Fatal(err)
	}
	reuse, err := s.Allocate(BytesPerSlot)
	if err != nil {
		t.Fatal(err)
	}
	if reuse != b {
		t.Fatalf("reuse 0x%x want hole 0x%x", reuse, b)
	}
	two, err := s.Allocate(2 * BytesPerSlot)
	if err != nil {
		t.Fatal(err)
	}
	if two != c+BytesPerSlot {
		t.Fatalf("128-byte 0x%x want 0x%x", two, c+BytesPerSlot)
	}
	if err := s.Free(a, BytesPerSlot); err != nil {
		t.Fatal(err)
	}
	raw, err := s.EncodeFile()
	if err != nil {
		t.Fatal(err)
	}
	if err := CheckAllocation(raw); err != nil {
		t.Fatal(err)
	}
	re, err := LoadStore(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !s.BitmapEqual(re) {
		t.Fatal("reopen after free/reuse mismatch")
	}
	again, err := re.Allocate(BytesPerSlot)
	if err != nil {
		t.Fatal(err)
	}
	if again != a {
		t.Fatalf("loaded store reuse 0x%x want 0x%x", again, a)
	}
}

func TestMultiRegionGrowthPlacesPMap(t *testing.T) {
	s := NewStore()
	for s.RegionCount() < 9 {
		if err := s.Grow(); err != nil {
			t.Fatal(err)
		}
	}
	if s.AMapLast() != AMapOffset(8) {
		t.Fatalf("ibAMapLast 0x%x", s.AMapLast())
	}
	raw, err := s.EncodeFile()
	if err != nil {
		t.Fatal(err)
	}
	if err := CheckAllocation(raw); err != nil {
		t.Fatal(err)
	}
	off := PMapOffset(1)
	pg, err := InspectPage(raw[off:off+PageSize], off)
	if err != nil {
		t.Fatal(err)
	}
	if pg.Type != PagePMap {
		t.Fatalf("ptype %d", pg.Type)
	}
	re, err := LoadStore(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !s.BitmapEqual(re) || re.RegionCount() != 9 {
		t.Fatalf("reopen regions %d equal %v", re.RegionCount(), s.BitmapEqual(re))
	}
}

func TestCannotFreeMetadata(t *testing.T) {
	s := NewStore()
	err := s.Free(FirstAMapPageOffset, PageSize)
	if err == nil {
		t.Fatal("freed AMap")
	}
	if !errors.Is(err, ErrInvalidArg) {
		t.Fatalf("got %v", err)
	}
	err = s.Free(FirstPMapPageOffset, PageSize)
	if !errors.Is(err, ErrInvalidArg) {
		t.Fatalf("got %v", err)
	}
}

func TestReserveRejectsOverlapAndUnaligned(t *testing.T) {
	s := NewStore()
	ib, err := s.Allocate(2 * BytesPerSlot)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Reserve(ib, BytesPerSlot); err == nil {
		t.Fatal("overlap")
	}
	if err := s.Reserve(ib+1, BytesPerSlot); !errors.Is(err, ErrInvalidArg) {
		t.Fatalf("unaligned: %v", err)
	}
}

func TestAllocateRejectsTooLarge(t *testing.T) {
	s := NewStore()
	_, err := s.Allocate(AMapCoverageBytes)
	if !errors.Is(err, ErrLimit) {
		t.Fatalf("got %v", err)
	}
}

func TestEncodePageMapUsesIBAndZeroSig(t *testing.T) {
	raw, err := EncodePage([]byte{0xFF}, PageAMap, 4, FirstAMapPageOffset)
	if err != nil {
		t.Fatal(err)
	}
	pg, err := InspectPage(raw, FirstAMapPageOffset)
	if err != nil {
		t.Fatal(err)
	}
	if pg.Sig != 0 || pg.BID != FirstAMapPageOffset {
		t.Fatalf("sig=%d bid=0x%x", pg.Sig, pg.BID)
	}
	raw[PageSize-UnicodePageTrailer+2] = 1
	_, err = InspectPage(raw, FirstAMapPageOffset)
	mustInvariant(t, err, SectionPageTrailer, "wSig")
}

func recrcPage(page []byte) {
	max := PageSize - UnicodePageTrailer
	crc := crc32PST(page[:max])
	binary.LittleEndian.PutUint32(page[max+4:max+8], crc)
}

func TestCheckAllocationDetectsFreedAMapBit(t *testing.T) {
	s := NewStore()
	raw, err := s.EncodeFile()
	if err != nil {
		t.Fatal(err)
	}
	raw[FirstAMapPageOffset] = 0xFE
	recrcPage(raw[FirstAMapPageOffset : FirstAMapPageOffset+PageSize])
	err = CheckAllocation(raw)
	if err == nil {
		t.Fatal("expected metadata-free invariant")
	}
	if !errors.Is(err, ErrInvariant) {
		t.Fatalf("got %v", err)
	}
}

func TestFMapPageAfter128Regions(t *testing.T) {
	s := NewStore()
	for s.RegionCount() < 129 {
		if err := s.Grow(); err != nil {
			t.Fatal(err)
		}
	}
	pages := RegionMapPages(128)
	var fmap MapPage
	for _, p := range pages {
		if p.Type == PageFMap {
			fmap = p
		}
	}
	if fmap.Offset == 0 {
		t.Fatal("no FMap at AMap 128")
	}
	raw, err := s.encodeMapPage(fmap)
	if err != nil {
		t.Fatal(err)
	}
	pg, err := InspectPage(raw, fmap.Offset)
	if err != nil {
		t.Fatal(err)
	}
	if pg.Type != PageFMap || pg.Sig != 0 {
		t.Fatalf("%+v", pg)
	}
	if pg.Payload[0] != 0xFF {
		t.Fatalf("FMap[0]=0x%02x", pg.Payload[0])
	}
}
