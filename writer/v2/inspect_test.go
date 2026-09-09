package writer

import (
	"bytes"
	"errors"
	"testing"

	"github.com/grokify/outlook-pst-go/pkg/disk"
)

func TestInspectHeaderAcceptsSpecEncoding(t *testing.T) {
	raw := EncodeUnicodeHeader(DefaultHeaderDraft())
	if len(raw) != UnicodeHeaderSize {
		t.Fatalf("header size %d, want %d", len(raw), UnicodeHeaderSize)
	}
	h, err := InspectHeader(raw)
	if err != nil {
		t.Fatalf("InspectHeader: %v", err)
	}
	if h.WVer != UnicodeWVer {
		t.Fatalf("wVer=%d", h.WVer)
	}
	if h.AMapValid != AMapValid2 {
		t.Fatalf("fAMapValid=%d", h.AMapValid)
	}
	if h.Sentinel != Sentinel {
		t.Fatalf("sentinel=%d", h.Sentinel)
	}
}

func TestInspectHeaderMalformedMagic(t *testing.T) {
	raw := EncodeUnicodeHeader(DefaultHeaderDraft())
	raw[0] ^= 0xFF
	_, err := InspectHeader(raw)
	mustInvariant(t, err, SectionHeader, "dwMagic")
}

func TestInspectHeaderMalformedCRC(t *testing.T) {
	raw := EncodeUnicodeHeader(DefaultHeaderDraft())
	raw[OffWVerClient] ^= 0x01 // covered by both CRC spans; recompute not done
	_, err := InspectHeader(raw)
	mustInvariant(t, err, SectionHeader, "dwCRCPartial")
}

func TestInspectHeaderANSIRejected(t *testing.T) {
	d := DefaultHeaderDraft()
	d.WVer = 15
	raw := EncodeUnicodeHeader(d)
	_, err := InspectHeader(raw)
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("got %v, want ErrUnsupported", err)
	}
	var we *Error
	if !errors.As(err, &we) || we.Feature != FeatureANSI {
		t.Fatalf("want FeatureANSI, got %#v", err)
	}
}

func TestInspectHeaderInvalidAMapStatus(t *testing.T) {
	d := DefaultHeaderDraft()
	d.AMapValid = AMapInvalid
	raw := EncodeUnicodeHeader(d)
	_, err := InspectHeader(raw)
	mustInvariant(t, err, SectionRoot, "fAMapValid")
}

func TestInspectHeaderRejectsLegacySerializer(t *testing.T) {
	h := disk.NewHeader(disk.FormatUnicode, disk.ClientMagicPST)
	raw, err := disk.SerializeHeaderUnicode(h)
	if err != nil {
		t.Fatal(err)
	}
	_, err = InspectHeader(raw)
	if err == nil {
		t.Fatal("legacy Unicode header serializer unexpectedly passed the oracle")
	}
	if !errors.Is(err, ErrInvariant) {
		t.Fatalf("got %v, want invariant (wrong offsets/CRC spans)", err)
	}
}

func TestInspectPageMalformedTypeRepeat(t *testing.T) {
	raw, err := EncodePage([]byte{1, 2, 3}, PageAMap, 4, FirstAMapPageOffset)
	if err != nil {
		t.Fatal(err)
	}
	_, err = InspectPage(raw, FirstAMapPageOffset)
	if err != nil {
		t.Fatalf("valid page: %v", err)
	}
	raw[PageSize-UnicodePageTrailer+1] = PageNBT
	_, err = InspectPage(raw, FirstAMapPageOffset)
	mustInvariant(t, err, SectionPageTrailer, "ptypeRepeat")
}

func TestInspectPageMalformedCRC(t *testing.T) {
	raw, err := EncodePage(bytes.Repeat([]byte{0xAA}, 16), PageNBT, 4, 0x2000)
	if err != nil {
		t.Fatal(err)
	}
	raw[0] ^= 0x01
	_, err = InspectPage(raw, 0x2000)
	mustInvariant(t, err, SectionPageTrailer, "dwCRC")
}

func TestInspectBlockMalformedCRCUsesDataNotPadding(t *testing.T) {
	data := []byte("hello")
	raw, err := EncodeBlock(data, 4, 0x5000)
	if err != nil {
		t.Fatal(err)
	}
	if uint64(len(raw)) != BlockDiskSize(uint64(len(data))) {
		t.Fatalf("disk size %d", len(raw))
	}
	_, err = InspectBlock(raw, 0x5000)
	if err != nil {
		t.Fatalf("valid block: %v", err)
	}
	// Flip padding (byte after cb). CRC must still cover only cb bytes,
	// but size/layout stays valid — padding is not hashed, so inspect still
	// passes. Flip a data byte instead.
	raw[0] ^= 0x01
	_, err = InspectBlock(raw, 0x5000)
	mustInvariant(t, err, SectionBlockTrailer, "dwCRC")
}

func TestInspectBlockRejectsLegacyAlignment(t *testing.T) {
	data := []byte("hello")
	// Legacy writer: AlignDisk(cb)+trailer, CRC over padded data.
	legacy := diskAlignPlusTrailer(data)
	_, err := InspectBlock(legacy, 0x5000)
	if err == nil {
		t.Fatal("legacy block layout unexpectedly passed")
	}
	if !errors.Is(err, ErrInvariant) {
		t.Fatalf("got %v", err)
	}
}

func diskAlignPlusTrailer(data []byte) []byte {
	// Recreate the incorrect BuildBlock geometry: aligned data THEN trailer.
	aligned := int(Align64(uint64(len(data))))
	buf := make([]byte, aligned+UnicodeBlockTrailer)
	copy(buf, data)
	crc := crc32PST(buf[:aligned])
	tr := buf[aligned:]
	// cb at 0, dummy sig/crc/bid
	tr[0] = byte(len(data))
	tr[1] = 0
	// leave sig 0
	tr[4] = byte(crc)
	tr[5] = byte(crc >> 8)
	tr[6] = byte(crc >> 16)
	tr[7] = byte(crc >> 24)
	return buf
}

func mustEncodeTC(t *testing.T, d TableDraft) []byte {
	t.Helper()
	raw, err := EncodeTCINFO(d)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestInspectTableMalformedSignature(t *testing.T) {
	raw := mustEncodeTC(t, TableDraft{Columns: []ColumnView{
		{PropType: 0x001F, PropID: 0x0037, Offset: 0, Size: 4, Bit: 0},
	}})
	_, err := InspectTable(raw)
	if err != nil {
		t.Fatalf("valid table: %v", err)
	}
	raw[0] = 0x00
	_, err = InspectTable(raw)
	mustInvariant(t, err, SectionTCINFO, "bType")
}

func TestInspectTableMissingColumnArray(t *testing.T) {
	raw := mustEncodeTC(t, TableDraft{Columns: []ColumnView{
		{PropType: 0x001F, PropID: 0x0037, Size: 4, Bit: 0},
		{PropType: 0x0040, PropID: 0x0E06, Size: 8, Bit: 1},
	}})
	_, err := InspectTable(raw[:TCINFOFixedSize]) // drop rgTCOLDESC
	mustInvariant(t, err, SectionTCINFO, "rgTCOLDESC")
}

func TestInspectTableRgIBNotMonotonic(t *testing.T) {
	raw := mustEncodeTC(t, TableDraft{Columns: []ColumnView{
		{PropType: 0x0003, PropID: 0x0E08, Size: 4, Bit: 0},
	}})
	raw[2] = 8
	raw[3] = 0
	raw[4] = 4
	raw[5] = 0
	_, err := InspectTable(raw)
	mustInvariant(t, err, SectionTCINFO, "rgib")
}

func swapTCOLDESC(raw []byte, i, j int) {
	a := TCINFOFixedSize + TCOLDESCSize*i
	b := TCINFOFixedSize + TCOLDESCSize*j
	tmp := append([]byte(nil), raw[a:a+TCOLDESCSize]...)
	copy(raw[a:a+TCOLDESCSize], raw[b:b+TCOLDESCSize])
	copy(raw[b:b+TCOLDESCSize], tmp)
}

func TestInspectTableRejectsUnsortedDescriptors(t *testing.T) {
	raw := mustEncodeTC(t, TableDraft{Columns: []ColumnView{
		{PropType: 0x0040, PropID: 0x0E06, Size: 8},
		{PropType: 0x0003, PropID: 0x0E08, Size: 4},
	}})
	swapTCOLDESC(raw, 0, 1)
	_, err := InspectTable(raw)
	mustInvariant(t, err, SectionTCINFO, "tag")
}

func TestInspectTableRejectsEqualWidthUnsortedTags(t *testing.T) {
	raw := mustEncodeTC(t, TableDraft{Columns: []ColumnView{
		{PropType: 0x0003, PropID: 0x0001, Size: 4},
		{PropType: 0x0003, PropID: 0x0002, Size: 4},
	}})
	swapTCOLDESC(raw, 0, 1)
	_, err := InspectTable(raw)
	mustInvariant(t, err, SectionTCINFO, "tag")
}

func TestInspectTableAcceptsMixedWidthTagOrder(t *testing.T) {
	raw := mustEncodeTC(t, TableDraft{Columns: []ColumnView{
		{PropType: 0x0003, PropID: 0x0001, Size: 4},
		{PropType: 0x0040, PropID: 0x0002, Size: 8},
		{PropType: 0x0002, PropID: 0x0003, Size: 2},
	}})
	view, err := InspectTable(raw)
	if err != nil {
		t.Fatal(err)
	}
	if view.Columns[0].PropID != 0x0001 || view.Columns[0].Size != 4 {
		t.Fatalf("want smaller tag first, got %+v", view.Columns)
	}
	if view.Columns[1].Size != 8 || view.Columns[2].Size != 2 {
		t.Fatalf("mixed-width tag order %+v", view.Columns)
	}
	if view.Columns[0].Offset != 16 || view.Columns[1].Offset != 8 || view.Columns[2].Offset != 20 {
		t.Fatalf("ibData not assigned by 8/4 then 2-byte groups after LTP pair: %+v", view.Columns)
	}
	if view.Columns[0].Offset <= view.Columns[1].Offset {
		t.Fatal("ibData must not be required to increase in descriptor order")
	}
}

func TestInspectTableRejectsSimplifiedRgIB(t *testing.T) {
	raw := mustEncodeTC(t, TableDraft{Columns: []ColumnView{
		{PropType: 0x0003, PropID: 0x0E08, Size: 4},
		{PropType: 0x0002, PropID: 0x0E17, Size: 2},
	}})
	view, err := InspectTable(raw)
	if err != nil {
		t.Fatal(err)
	}
	if view.RgIB != ([4]uint16{12, 14, 14, 15}) {
		t.Fatalf("grouped rgib=%v", view.RgIB)
	}
	// Old simplified encoder set TCI_4b=TCI_2b=TCI_1b=data-end.
	raw[2], raw[3] = 6, 0
	_, err = InspectTable(raw)
	mustInvariant(t, err, SectionTCINFO, "rgib")
}

func TestInspectTableRejectsInvalidColumnSize(t *testing.T) {
	raw := mustEncodeTC(t, TableDraft{Columns: []ColumnView{
		{PropType: 0x0003, PropID: 0x0E08, Size: 4},
	}})
	raw[TCINFOFixedSize+6] = 3
	_, err := InspectTable(raw)
	mustInvariant(t, err, SectionTCOLDESC, "cbData")
}

func TestInspectTableRejectsUnpackedIbData(t *testing.T) {
	raw := mustEncodeTC(t, TableDraft{Columns: []ColumnView{
		{PropType: 0x0003, PropID: 1, Size: 4},
		{PropType: 0x0003, PropID: 2, Size: 4},
	}})
	off := TCINFOFixedSize + TCOLDESCSize + 4
	raw[off] = 16
	raw[off+1] = 0 // ibData=16, not packed 12
	_, err := InspectTable(raw)
	mustInvariant(t, err, SectionTCOLDESC, "ibData")
}

func TestEncodeTCINFORejectsInvalidSize(t *testing.T) {
	_, err := EncodeTCINFO(TableDraft{Columns: []ColumnView{{Size: 3, PropID: 1}}})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestEncodeTCINFOGroupsEightBeforeFour(t *testing.T) {
	raw, err := EncodeTCINFO(TableDraft{Columns: []ColumnView{
		{PropType: 0x0003, PropID: 0x0001, Size: 4},
		{PropType: 0x0040, PropID: 0x0002, Size: 8},
	}})
	if err != nil {
		t.Fatal(err)
	}
	view, err := InspectTable(raw)
	if err != nil {
		t.Fatal(err)
	}
	bySize := map[byte]ColumnView{}
	for _, c := range view.Columns {
		bySize[c.Size] = c
	}
	eight := colByID(t, view.Columns, 0x0002)
	four := colByID(t, view.Columns, 0x0001)
	if eight.Offset != 8 || four.Offset != 16 {
		t.Fatalf("8-byte ibData must precede remaining 4-byte after LTP pair: 8=%d 4=%d", eight.Offset, four.Offset)
	}
}

func TestEncodeTCINFOSortsByPropertyTag(t *testing.T) {
	raw := mustEncodeTC(t, TableDraft{Columns: []ColumnView{
		{PropType: 0x0002, PropID: 0x0E17, Size: 2},
		{PropType: 0x0040, PropID: 0x0E06, Size: 8},
		{PropType: 0x0003, PropID: 0x0E08, Size: 4},
	}})
	view, err := InspectTable(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Columns) != 5 {
		t.Fatalf("cols=%d want 3 ordinary + 2 required LTP", len(view.Columns))
	}
	if view.Columns[0].PropID != 0x0E06 || view.Columns[1].PropID != 0x0E08 || view.Columns[2].PropID != 0x0E17 {
		t.Fatalf("encoder did not sort rgTCOLDESC by tag: %+v", view.Columns)
	}
	if view.Columns[3].Tag() != TagLtpRowId || view.Columns[4].Tag() != TagLtpRowVer {
		t.Fatalf("required specials last by tag: %+v", view.Columns)
	}
	if view.Columns[0].Offset != 8 || view.Columns[1].Offset != 16 || view.Columns[2].Offset != 20 {
		t.Fatalf("ibData %+v", view.Columns)
	}
}

func colByID(t *testing.T, cols []ColumnView, id uint16) ColumnView {
	t.Helper()
	for _, c := range cols {
		if c.PropID == id {
			return c
		}
	}
	t.Fatalf("missing prop 0x%04x", id)
	return ColumnView{}
}

func TestEncodeInspectLtpRowIdAndRowVer(t *testing.T) {
	raw := mustEncodeTC(t, TableDraft{Columns: []ColumnView{
		{PropType: 0x0003, PropID: 0x0E08, Size: 4},
		{PropType: 0x0040, PropID: 0x0E06, Size: 8},
		{PropType: 0x0003, PropID: PidTagLtpRowVer, Size: 4},
		{PropType: 0x0003, PropID: PidTagLtpRowId, Size: 4},
		{PropType: 0x0002, PropID: 0x0E17, Size: 2},
	}})
	view, err := InspectTable(raw)
	if err != nil {
		t.Fatal(err)
	}
	id := colByID(t, view.Columns, PidTagLtpRowId)
	ver := colByID(t, view.Columns, PidTagLtpRowVer)
	if id.Bit != 0 || id.Offset != 0 || id.Size != 4 {
		t.Fatalf("PidTagLtpRowId %+v", id)
	}
	if ver.Bit != 1 || ver.Offset != 4 || ver.Size != 4 {
		t.Fatalf("PidTagLtpRowVer %+v", ver)
	}
	eight := colByID(t, view.Columns, 0x0E06)
	four := colByID(t, view.Columns, 0x0E08)
	two := colByID(t, view.Columns, 0x0E17)
	if eight.Offset != 8 || four.Offset != 16 || two.Offset != 20 {
		t.Fatalf("row-data groups with LTP pair: 8=%d 4=%d 2=%d", eight.Offset, four.Offset, two.Offset)
	}
	for i := 1; i < len(view.Columns); i++ {
		if view.Columns[i].Tag() <= view.Columns[i-1].Tag() {
			t.Fatalf("not tag-sorted %+v", view.Columns)
		}
	}
	if view.RgIB != ([4]uint16{20, 22, 22, 23}) {
		t.Fatalf("rgib=%v", view.RgIB)
	}
}

func TestEncodeTCINFOInjectsSpecialPair(t *testing.T) {
	raw := mustEncodeTC(t, TableDraft{Columns: []ColumnView{
		{PropType: 0x0003, PropID: 0x0E08, Size: 4},
	}})
	view, err := InspectTable(raw)
	if err != nil {
		t.Fatal(err)
	}
	id := colByID(t, view.Columns, PidTagLtpRowId)
	ver := colByID(t, view.Columns, PidTagLtpRowVer)
	if id.Tag() != TagLtpRowId || id.Bit != 0 || id.Offset != 0 {
		t.Fatalf("injected RowId %+v", id)
	}
	if ver.Tag() != TagLtpRowVer || ver.Bit != 1 || ver.Offset != 4 {
		t.Fatalf("injected RowVer %+v", ver)
	}
	if view.NumCols != 3 {
		t.Fatalf("cCols=%d want 3 (CEB is cCols-based)", view.NumCols)
	}
	if view.RgIB[3]-view.RgIB[2] != 1 {
		t.Fatalf("CEB width %d, want 1 for cCols=3", view.RgIB[3]-view.RgIB[2])
	}
}

func TestInspectTableRejectsMissingSpecialColumns(t *testing.T) {
	raw := mustEncodeTC(t, TableDraft{Columns: []ColumnView{
		{PropType: 0x0003, PropID: 0x0E08, Size: 4},
	}})
	n := int(raw[1])
	raw[1] = byte(n - 2)
	raw = append([]byte(nil), raw[:TCINFOFixedSize+TCOLDESCSize*(n-2)]...)
	// Remaining ordinary used iBit 2; clamp so parse does not fail iBit>=cCols
	// before the required-pair check.
	raw[TCINFOFixedSize+7] = 0
	_, err := InspectTable(raw)
	mustInvariant(t, err, SectionTCRowID, "PidTagLtpRowId")
}

func TestInspectTableRejectsOrdinarySlotReuse(t *testing.T) {
	raw := mustEncodeTC(t, TableDraft{Columns: []ColumnView{
		{PropType: 0x0003, PropID: 0x0E08, Size: 4},
	}})
	// Ordinary is first by tag; iBit was 2. Steal reserved iBit 0 and
	// move RowId (second descriptor) to iBit 2 so bits stay unique.
	raw[TCINFOFixedSize+7] = 0
	raw[TCINFOFixedSize+TCOLDESCSize+7] = 2
	_, err := InspectTable(raw)
	mustInvariant(t, err, SectionTCRowID, "iBit")
}

func TestInspectTableRejectsWrongTypeSpecialTags(t *testing.T) {
	raw := mustEncodeTC(t, TableDraft{Columns: []ColumnView{
		{PropType: 0x0003, PropID: 0x0E08, Size: 4},
		{PropType: PtypInteger32, PropID: PidTagLtpRowId, Size: 4},
		{PropType: PtypInteger32, PropID: PidTagLtpRowVer, Size: 4},
	}})
	// RowId is the second-to-last descriptor; smash wPropType to PtypString.
	rowID := TCINFOFixedSize + TCOLDESCSize*int(raw[1]-2)
	raw[rowID] = 0x1F
	raw[rowID+1] = 0
	_, err := InspectTable(raw)
	mustInvariant(t, err, SectionTCRowID, "tag")
}

func TestEncodeTCINFORejectsWrongTypeSpecialTags(t *testing.T) {
	_, err := EncodeTCINFO(TableDraft{Columns: []ColumnView{
		{PropType: 0x001F, PropID: PidTagLtpRowId, Size: 4},
		{PropType: PtypInteger32, PropID: PidTagLtpRowVer, Size: 4},
	}})
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, ErrInvalidArg) {
		t.Fatalf("got %v, want ErrInvalidArg", err)
	}
}

func ordinaryColumns(n int) []ColumnView {
	out := make([]ColumnView, 0, n)
	id := uint16(1)
	for len(out) < n {
		if id == PidTagLtpRowId || id == PidTagLtpRowVer {
			id++
			continue
		}
		out = append(out, ColumnView{PropType: PtypInteger32, PropID: id, Size: 4})
		id++
	}
	return out
}

func TestEncodeTCINFOAccepts255Columns(t *testing.T) {
	cols := append(ordinaryColumns(253),
		ColumnView{PropType: PtypInteger32, PropID: PidTagLtpRowId, Size: 4},
		ColumnView{PropType: PtypInteger32, PropID: PidTagLtpRowVer, Size: 4},
	)
	raw, err := EncodeTCINFO(TableDraft{Columns: cols})
	if err != nil {
		t.Fatal(err)
	}
	view, err := InspectTable(raw)
	if err != nil {
		t.Fatal(err)
	}
	if view.NumCols != 255 {
		t.Fatalf("cCols=%d want 255", view.NumCols)
	}
	if got := view.RgIB[3] - view.RgIB[2]; got != 32 {
		t.Fatalf("CEB width %d, want 32 for cCols=255", got)
	}
	id := colByID(t, view.Columns, PidTagLtpRowId)
	ver := colByID(t, view.Columns, PidTagLtpRowVer)
	if id.Bit != 0 || id.Offset != 0 || ver.Bit != 1 || ver.Offset != 4 {
		t.Fatalf("specials %+v %+v", id, ver)
	}
}

func TestEncodeTCINFORejects256Columns(t *testing.T) {
	cols := append(ordinaryColumns(254),
		ColumnView{PropType: PtypInteger32, PropID: PidTagLtpRowId, Size: 4},
		ColumnView{PropType: PtypInteger32, PropID: PidTagLtpRowVer, Size: 4},
	)
	raw, err := EncodeTCINFO(TableDraft{Columns: cols})
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, ErrInvalidArg) {
		t.Fatalf("got %v, want ErrInvalidArg", err)
	}
	if raw != nil {
		t.Fatalf("wrapped table leaked %d bytes", len(raw))
	}
}

func TestInspectTableRejectsBadLtpRowId(t *testing.T) {
	raw := mustEncodeTC(t, TableDraft{Columns: []ColumnView{
		{PropType: 0x0003, PropID: PidTagLtpRowId, Size: 4},
		{PropType: 0x0003, PropID: PidTagLtpRowVer, Size: 4},
	}})
	idOff := TCINFOFixedSize // smaller tag 0x67F2 comes first
	raw[idOff+4] = 8         // ibData=8, not 0
	raw[idOff+5] = 0
	_, err := InspectTable(raw)
	mustInvariant(t, err, SectionTCRowID, "PidTagLtpRowId")

	raw = mustEncodeTC(t, TableDraft{Columns: []ColumnView{
		{PropType: 0x0003, PropID: PidTagLtpRowId, Size: 4},
		{PropType: 0x0003, PropID: PidTagLtpRowVer, Size: 4},
	}})
	verOff := TCINFOFixedSize + TCOLDESCSize
	raw[verOff+4] = 0
	raw[verOff+5] = 0 // ibData=0
	_, err = InspectTable(raw)
	mustInvariant(t, err, SectionTCRowID, "PidTagLtpRowVer")
}

func mustInvariant(t *testing.T, err error, section, field string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected invariant %s %s", section, field)
	}
	if !errors.Is(err, ErrInvariant) {
		t.Fatalf("got %v, want ErrInvariant", err)
	}
	var we *Error
	if !errors.As(err, &we) {
		t.Fatalf("got %T %v", err, err)
	}
	if we.Section != section {
		t.Fatalf("section=%q want %q (err=%v)", we.Section, section, err)
	}
	if we.Field != field {
		t.Fatalf("field=%q want %q (err=%v)", we.Field, field, err)
	}
	if we.Section == "" {
		t.Fatal("missing MS-PST citation")
	}
}
