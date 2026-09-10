package writer

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"

	"github.com/grokify/outlook-pst-go/pkg/disk"
)

func spoolByte(s *Store, ib uint64) byte {
	var b [1]byte
	r := s.reader()
	if r == nil {
		r = s.writer()
	}
	if r != nil {
		n, _ := r.ReadAt(b[:], int64(ib))
		if n == 1 {
			return b[0]
		}
	}
	return 0
}

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
	if h.BidNextP != FirstAllocBID+PageBIDIncrement {
		t.Fatalf("bidNextP %d want %d", h.BidNextP, FirstAllocBID+PageBIDIncrement)
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
	if dl.Flags != DFLBackfillComplete || dl.Count != 1 || dl.Entries[0].PageNum != 0 {
		t.Fatalf("DList %+v", dl)
	}
	if dl.Page.BID != FirstAllocBID {
		t.Fatalf("DList BID 0x%x want 0x%x", dl.Page.BID, FirstAllocBID)
	}
	if dl.Page.Sig != signature(FirstAllocBID, DListPageOffset) {
		t.Fatalf("DList sig 0x%04x want 0x%04x", dl.Page.Sig, signature(FirstAllocBID, DListPageOffset))
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
	// Leave one 64-byte hole at the end of region 0. Public Allocate is capped
	// at 8192, so the bulk fill uses the unexported mark helper.
	fillStart := first + BytesPerSlot
	fillSize := (AMapRegionEnd(0) - BytesPerSlot) - fillStart
	if err := s.mark(fillStart, fillSize, true); err != nil {
		t.Fatal(err)
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

func TestAllocate8192Boundary(t *testing.T) {
	s := NewStore()
	ib, err := s.Allocate(MaxAllocBytes)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Allocate(MaxAllocBytes + BytesPerSlot)
	if !errors.Is(err, ErrLimit) {
		t.Fatalf("8256 allocate: %v", err)
	}
	if err := s.Reserve(ib+MaxAllocBytes, MaxAllocBytes+BytesPerSlot); !errors.Is(err, ErrLimit) {
		t.Fatalf("8256 reserve: %v", err)
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
		t.Fatal("reopen after 8192 alloc mismatch")
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

func TestDListPageNumIsAMapIndex(t *testing.T) {
	s := NewStore()
	if err := s.Grow(); err != nil {
		t.Fatal(err)
	}
	raw, err := s.EncodeFile()
	if err != nil {
		t.Fatal(err)
	}
	dl, err := InspectDList(raw[DListPageOffset : DListPageOffset+PageSize])
	if err != nil {
		t.Fatal(err)
	}
	if dl.Count != 2 {
		t.Fatalf("count %d entries %+v", dl.Count, dl.Entries)
	}
	seen := map[uint32]bool{}
	for _, e := range dl.Entries {
		if e.PageNum > 1 {
			t.Fatalf("dwPageNum %d is not a zero-based AMap index", e.PageNum)
		}
		seen[e.PageNum] = true
	}
	if !seen[0] || !seen[1] {
		t.Fatalf("missing AMap index in %+v", dl.Entries)
	}
}

func TestDListBIDSignatureAndCounterRoundTrip(t *testing.T) {
	s := NewStore()
	raw, err := s.EncodeFile()
	if err != nil {
		t.Fatal(err)
	}
	dl, err := InspectDList(raw[DListPageOffset : DListPageOffset+PageSize])
	if err != nil {
		t.Fatal(err)
	}
	if dl.Page.BID != FirstAllocBID {
		t.Fatalf("BID 0x%x want 0x%x", dl.Page.BID, FirstAllocBID)
	}
	wantSig := signature(dl.Page.BID, DListPageOffset)
	if dl.Page.Sig != wantSig {
		t.Fatalf("sig 0x%04x want 0x%04x", dl.Page.Sig, wantSig)
	}
	h, err := InspectHeader(raw[:UnicodeHeaderSize])
	if err != nil {
		t.Fatal(err)
	}
	if h.BidNextP != FirstAllocBID+PageBIDIncrement {
		t.Fatalf("bidNextP %d want %d", h.BidNextP, FirstAllocBID+PageBIDIncrement)
	}
	re, err := LoadStore(raw)
	if err != nil {
		t.Fatal(err)
	}
	if re.dlistBID != dl.Page.BID || re.bidNextP != h.BidNextP {
		t.Fatalf("loaded bid=0x%x next=%d", re.dlistBID, re.bidNextP)
	}
	raw2, err := re.EncodeFile()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw[DListPageOffset:DListPageOffset+PageSize], raw2[DListPageOffset:DListPageOffset+PageSize]) {
		t.Fatal("DList changed after reopen encode")
	}
	if !bytes.Equal(raw[:UnicodeHeaderSize], raw2[:UnicodeHeaderSize]) {
		t.Fatal("header changed after reopen encode")
	}
	bad := append([]byte(nil), raw[DListPageOffset:DListPageOffset+PageSize]...)
	bad[PageSize-UnicodePageTrailer+2] ^= 0x01
	_, err = InspectDList(bad)
	mustInvariant(t, err, SectionSignature, "wSig")
}

func TestFPMapFreeAndFullTransitions(t *testing.T) {
	s := NewStore()
	if !s.pMapHasFreePages(0) {
		t.Fatal("new PMap 0 should have free pages")
	}
	bits := s.fpMapPayload(0)
	if bitIsSet(bits, 0) {
		t.Fatal("FPMap bit 0 should be 0 (has free pages)")
	}
	for s.RegionCount() < int(AMapsPerPMap) {
		if err := s.Grow(); err != nil {
			t.Fatal(err)
		}
	}
	if !s.pMapHasFreePages(0) {
		t.Fatal("grown PMap 0 still has free pages")
	}
	for i := 0; i < int(AMapsPerPMap); i++ {
		for slot := 0; slot < SlotsPerAMap; slot++ {
			if bitIsSet(s.regions[i].bitmap[:], slot) {
				continue
			}
			if err := s.mark(slotOffset(uint64(i), slot), BytesPerSlot, true); err != nil {
				t.Fatal(err)
			}
		}
	}
	if s.pMapHasFreePages(0) {
		t.Fatal("filled PMap 0 should have no free pages")
	}
	bits = s.fpMapPayload(0)
	if !bitIsSet(bits, 0) {
		t.Fatal("FPMap bit 0 should be 1 (no free pages)")
	}
	if !bitIsSet(bits, 1) {
		t.Fatal("absent PMap 1 should be bit 1 (no free pages / not in file)")
	}
}

func TestFPMapPartialFinalAndBeyondEOF(t *testing.T) {
	s := NewStore()
	bits := s.fpMapPayload(0)
	if bitIsSet(bits, 0) {
		t.Fatal("partial PMap 0 has free pages")
	}
	if !bitIsSet(bits, 1) {
		t.Fatal("PMap 1 is beyond EOF; FPMap bit must be 1")
	}
	for s.RegionCount() < 9 {
		if err := s.Grow(); err != nil {
			t.Fatal(err)
		}
	}
	bits = s.fpMapPayload(0)
	if bitIsSet(bits, 0) {
		t.Fatal("full PMap 0 still has free pages")
	}
	if bitIsSet(bits, 1) {
		t.Fatal("partial PMap 1 (AMap 8 only) still has free pages")
	}
	if !bitIsSet(bits, 2) {
		t.Fatal("PMap 2 is beyond EOF; FPMap bit must be 1")
	}
	// Fill the existing coverage of partial PMap 1. Beyond-EOF 512-byte
	// pages must not count as free.
	for slot := 0; slot < SlotsPerAMap; slot++ {
		if bitIsSet(s.regions[8].bitmap[:], slot) {
			continue
		}
		if err := s.mark(slotOffset(8, slot), BytesPerSlot, true); err != nil {
			t.Fatal(err)
		}
	}
	if s.pMapHasFreePages(1) {
		t.Fatal("filled partial PMap 1 should have no free in-file pages")
	}
	bits = s.fpMapPayload(0)
	if !bitIsSet(bits, 1) {
		t.Fatal("filled partial PMap 1 should be bit 1")
	}
}

func resignDList(page []byte, bid uint64) {
	max := PageSize - UnicodePageTrailer
	sig := signature(bid, DListPageOffset)
	binary.LittleEndian.PutUint16(page[max+2:max+4], sig)
	binary.LittleEndian.PutUint64(page[max+8:max+16], bid)
	recrcPage(page)
}

func TestDListLoadMutations(t *testing.T) {
	s := NewStore()
	if err := s.Grow(); err != nil {
		t.Fatal(err)
	}
	raw, err := s.EncodeFile()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := LoadStore(raw); err != nil {
		t.Fatal(err)
	}

	t.Run("dwPageNum-beyond-last", func(t *testing.T) {
		mut := append([]byte(nil), raw...)
		page := mut[DListPageOffset : DListPageOffset+PageSize]
		v := binary.LittleEndian.Uint32(page[8:])
		free := v >> 20
		binary.LittleEndian.PutUint32(page[8:], (99&0xFFFFF)|free<<20)
		recrcPage(page)
		_, err := LoadStore(mut)
		mustInvariant(t, err, SectionDList, "dwPageNum")
	})

	t.Run("dwPageNum-duplicate", func(t *testing.T) {
		mut := append([]byte(nil), raw...)
		page := mut[DListPageOffset : DListPageOffset+PageSize]
		v0 := binary.LittleEndian.Uint32(page[8:])
		v1 := binary.LittleEndian.Uint32(page[12:])
		// force entry 1 to the same AMap index as entry 0
		binary.LittleEndian.PutUint32(page[12:], (v0&0xFFFFF)|(v1>>20)<<20)
		recrcPage(page)
		_, err := LoadStore(mut)
		mustInvariant(t, err, SectionDList, "dwPageNum")
	})

	t.Run("bid-and-signature-beyond-counter", func(t *testing.T) {
		mut := append([]byte(nil), raw...)
		page := mut[DListPageOffset : DListPageOffset+PageSize]
		resignDList(page, FirstAllocBID+PageBIDIncrement) // BID == bidNextP
		_, err := LoadStore(mut)
		mustInvariant(t, err, SectionBID, "bidNextP")
	})

	t.Run("header-counter-not-beyond-bid", func(t *testing.T) {
		mut := append([]byte(nil), raw...)
		binary.LittleEndian.PutUint64(mut[OffBidNextP:], FirstAllocBID)
		recrcHeader(mut[:UnicodeHeaderSize])
		_, err := LoadStore(mut)
		mustInvariant(t, err, SectionBID, "bidNextP")
	})

	t.Run("null-dlist-bid", func(t *testing.T) {
		mut := append([]byte(nil), raw...)
		page := mut[DListPageOffset : DListPageOffset+PageSize]
		resignDList(page, 0)
		_, err := LoadStore(mut)
		mustInvariant(t, err, SectionBID, "bid")
	})
}

func TestReallocDoesNotInheritFreedBacking(t *testing.T) {
	s := NewStore()
	ib, err := s.Allocate(64)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := s.EncodeFile()
	if err != nil {
		t.Fatal(err)
	}
	raw[ib] = 0xA5
	s2, err := LoadStore(raw)
	if err != nil {
		t.Fatal(err)
	}
	if b := spoolByte(s2, ib); b != 0xA5 {
		t.Fatalf("LoadStore dropped payload 0x%02x", b)
	}
	if err := s2.Free(ib, 64); err != nil {
		t.Fatal(err)
	}
	if b := spoolByte(s2, ib); b != 0 {
		t.Fatalf("Free left payload 0x%02x", b)
	}
	ib2, err := s2.Allocate(64)
	if err != nil {
		t.Fatal(err)
	}
	if ib2 != ib {
		t.Fatalf("first-fit reuse want 0x%x got 0x%x", ib, ib2)
	}
	out, err := s2.EncodeFile()
	if err != nil {
		t.Fatal(err)
	}
	if out[ib] != 0 {
		t.Fatalf("reused IB inherited 0x%02x", out[ib])
	}
}
