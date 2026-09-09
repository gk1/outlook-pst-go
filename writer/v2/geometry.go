package writer

// AMapOffset returns the file offset of AMap page index (0-based).
// See MS-PST 2.2.2.7.2: first AMap at 0x4400, then every 253,952 bytes.
func AMapOffset(index uint64) uint64 {
	return FirstAMapPageOffset + index*AMapCoverageBytes
}

// AMapIndexForOffset returns the AMap page index that covers ib, if ib is
// inside AMap-mapped space (ib >= 0x4400).
func AMapIndexForOffset(ib uint64) (uint64, bool) {
	if ib < FirstAMapPageOffset {
		return 0, false
	}
	return (ib - FirstAMapPageOffset) / AMapCoverageBytes, true
}

// AMapRegionStart is the first byte mapped by AMap index (the AMap page itself).
func AMapRegionStart(index uint64) uint64 {
	return AMapOffset(index)
}

// AMapRegionEnd is the first byte not mapped by AMap index.
func AMapRegionEnd(index uint64) uint64 {
	return AMapOffset(index) + AMapCoverageBytes
}

// PMapOffset returns the file offset of PMap page index.
// First PMap is at 0x4600; interval is 2,031,616 bytes (8 AMaps).
// PMap bit 0 maps 512 bytes at 0x4400, not 0x4600. See MS-PST 2.2.2.7.3.
func PMapOffset(index uint64) uint64 {
	return FirstPMapPageOffset + index*PMapCoverageBytes
}

// PMapIndexForAMap returns the PMap that covers AMap index.
func PMapIndexForAMap(amapIndex uint64) uint64 {
	return amapIndex / AMapsPerPMap
}

// slotIndex is the 0-based 64-byte slot inside an AMap region.
func slotIndex(ib, amapIndex uint64) int {
	return int((ib - AMapRegionStart(amapIndex)) / BytesPerSlot)
}

func slotOffset(amapIndex uint64, slot int) uint64 {
	return AMapRegionStart(amapIndex) + uint64(slot)*BytesPerSlot
}

func slotsForSize(size uint64) (int, error) {
	if size == 0 {
		return 0, invalidArg("size", "cannot allocate zero bytes")
	}
	if size%BytesPerSlot != 0 {
		return 0, invalidArg("size", "allocation %d is not a multiple of %d (MS-PST %s)", size, BytesPerSlot, SectionBlockAlign)
	}
	n := size / BytesPerSlot
	if n > uint64(SlotsPerAMap) {
		return 0, limitErr("size", "allocation %d exceeds one AMap region (%d bytes)", size, AMapCoverageBytes)
	}
	return int(n), nil
}

// MapPage holds a required metadata page at a periodic location.
type MapPage struct {
	Type   byte
	Offset uint64
}

// RegionMapPages returns the metadata pages that occupy the start of AMap
// region index, in on-disk order. Every region has an AMap. A PMap is placed
// when index is a multiple of 8. An extra FMap page is placed at AMap 128 +
// k*496 (HEADER.rgbFM covers the first 128 AMaps). An extra FPMap page is
// placed at PMap 1024 + k*3968 (HEADER.rgbFP covers the first 1024 PMaps).
// See MS-PST 2.2.2.7.2–2.2.2.7.6.
func RegionMapPages(index uint64) []MapPage {
	off := AMapOffset(index)
	pages := []MapPage{{Type: PageAMap, Offset: off}}
	shift := uint64(PageSize)
	if index%AMapsPerPMap == 0 {
		pages = append(pages, MapPage{Type: PagePMap, Offset: off + shift})
		shift += PageSize
	}
	if fmapPageIndex(index) {
		pages = append(pages, MapPage{Type: PageFMap, Offset: off + shift})
		shift += PageSize
	}
	if fpmapPageIndex(index) {
		pages = append(pages, MapPage{Type: PageFPMap, Offset: off + shift})
	}
	return pages
}

func fmapPageIndex(amapIndex uint64) bool {
	if amapIndex < FMapHeaderAMaps {
		return false
	}
	return (amapIndex-FMapHeaderAMaps)%FMapPageAMaps == 0
}

func fpmapPageIndex(amapIndex uint64) bool {
	if amapIndex%AMapsPerPMap != 0 {
		return false
	}
	pmap := amapIndex / AMapsPerPMap
	if pmap < FPMapHeaderPMaps {
		return false
	}
	return (pmap-FPMapHeaderPMaps)%FPMapPagePMaps == 0
}

// FMapPageOffset returns the file offset of extra FMap page k (k=0 is AMap 128).
func FMapPageOffset(k uint64) uint64 {
	idx := FMapHeaderAMaps + k*FMapPageAMaps
	for _, p := range RegionMapPages(idx) {
		if p.Type == PageFMap {
			return p.Offset
		}
	}
	return 0
}

// FPMapPageOffset returns the file offset of extra FPMap page k (k=0 is PMap 1024).
func FPMapPageOffset(k uint64) uint64 {
	pmap := FPMapHeaderPMaps + k*FPMapPagePMaps
	idx := pmap * AMapsPerPMap
	for _, p := range RegionMapPages(idx) {
		if p.Type == PageFPMap {
			return p.Offset
		}
	}
	return 0
}

func zeroSigPageType(pageType byte) bool {
	switch pageType {
	case PageAMap, PagePMap, PageFMap, PageFPMap:
		return true
	default:
		return false
	}
}
