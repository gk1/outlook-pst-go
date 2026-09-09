package writer

import "bytes"

// DListEntry is one Density List record. See MS-PST 2.2.2.7.4.1.
type DListEntry struct {
	PageNum uint32
	Free    uint32
}

// DListView is a decoded Unicode DLISTPAGE. See MS-PST 2.2.2.7.4.2.
type DListView struct {
	Flags   byte
	Count   byte
	Current uint32
	Entries []DListEntry
	Page    *PageView
}

// InspectDList validates the Density List page at 0x4200.
func InspectDList(raw []byte) (*DListView, error) {
	pg, err := InspectPage(raw, DListPageOffset)
	if err != nil {
		return nil, err
	}
	if pg.Type != PageDList {
		return nil, invariant(SectionDList, "ptype", "got 0x%02x want DList", pg.Type)
	}
	b := pg.Payload
	n := int(b[1])
	if n > DListMaxEntries {
		return nil, invariant(SectionDList, "cEntDList", "got %d max %d", n, DListMaxEntries)
	}
	ents := make([]DListEntry, n)
	for i := 0; i < n; i++ {
		v := binaryLEUint32(b[8+i*4:])
		ents[i] = DListEntry{PageNum: v & 0xFFFFF, Free: v >> 20}
	}
	return &DListView{
		Flags:   b[0],
		Count:   b[1],
		Current: binaryLEUint32(b[4:8]),
		Entries: ents,
		Page:    pg,
	}, nil
}

func binaryLEUint32(b []byte) uint32 {
	return uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24
}

// InspectAMap validates an AMap page at offset (must be a real AMap location).
func InspectAMap(raw []byte, offset uint64) (*PageView, error) {
	if offset < FirstAMapPageOffset || (offset-FirstAMapPageOffset)%AMapCoverageBytes != 0 {
		return nil, invariant(SectionAMap, "ib", "0x%x is not an AMap page offset", offset)
	}
	pg, err := InspectPage(raw, offset)
	if err != nil {
		return nil, err
	}
	if pg.Type != PageAMap {
		return nil, invariant(SectionAMap, "ptype", "got 0x%02x want AMap", pg.Type)
	}
	if pg.Payload[0] != 0xFF {
		return nil, invariant(SectionAMap, "rgbAMap", "AMap at 0x%x does not self-map (first byte 0x%02x)", offset, pg.Payload[0])
	}
	return pg, nil
}

// CheckAllocation walks a serialized Unicode PST and fails if a metadata page
// is marked free, if an AMap-allocated slot is unmarked, or if map pages at
// the required periodic locations are missing or stale.
func CheckAllocation(file []byte) error {
	s, err := LoadStore(file)
	if err != nil {
		return err
	}
	for _, off := range s.metadataOffsets() {
		if !s.allocatedRange(off, PageSize) {
			return invariant(SectionAMap, "slot", "metadata page at 0x%x marked free", off)
		}
		idx, _ := AMapIndexForOffset(off)
		for slot := 0; slot < int(PageSize/BytesPerSlot); slot++ {
			ib := off + uint64(slot)*BytesPerSlot
			if !bitIsSet(s.regions[idx].bitmap[:], slotIndex(ib, idx)) {
				return invariant(SectionAMap, "slot", "allocated byte at 0x%x marked free", ib)
			}
		}
	}
	// Every free slot must not be a metadata page.
	meta := make(map[uint64]struct{})
	for _, off := range s.metadataOffsets() {
		meta[off] = struct{}{}
	}
	for i := range s.regions {
		for slot := 0; slot < SlotsPerAMap; slot++ {
			if bitIsSet(s.regions[i].bitmap[:], slot) {
				continue
			}
			ib := slotOffset(uint64(i), slot)
			page := ib - (ib % uint64(PageSize))
			if _, ok := meta[page]; ok {
				return invariant(SectionAMap, "slot", "metadata page 0x%x has free slot 0x%x", page, ib)
			}
		}
	}
	// Re-encode maps and require a byte-identical round trip of map pages.
	fresh, err := s.EncodeFile()
	if err != nil {
		return err
	}
	if uint64(len(fresh)) != uint64(len(file)) {
		return invariant(SectionGrow, "ibFileEof", "re-encode length %d != %d", len(fresh), len(file))
	}
	if !bytes.Equal(fresh[DListPageOffset:DListPageOffset+PageSize], file[DListPageOffset:DListPageOffset+PageSize]) {
		return invariant(SectionDList, "page", "DList round-trip mismatch")
	}
	for i := range s.regions {
		for _, p := range RegionMapPages(uint64(i)) {
			if !bytes.Equal(fresh[p.Offset:p.Offset+PageSize], file[p.Offset:p.Offset+PageSize]) {
				return invariant(sectionForPage(p.Type), "page", "%s at 0x%x round-trip mismatch", pageName(p.Type), p.Offset)
			}
		}
	}
	return nil
}
