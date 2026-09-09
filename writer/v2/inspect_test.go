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

func TestInspectTableMalformedSignature(t *testing.T) {
	raw := EncodeTCINFO(TableDraft{Columns: []ColumnView{
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
	raw := EncodeTCINFO(TableDraft{Columns: []ColumnView{
		{PropType: 0x001F, PropID: 0x0037, Size: 4, Bit: 0},
		{PropType: 0x0040, PropID: 0x0E06, Size: 8, Bit: 1},
	}})
	_, err := InspectTable(raw[:TCINFOFixedSize]) // drop rgTCOLDESC
	mustInvariant(t, err, SectionTCINFO, "rgTCOLDESC")
}

func TestInspectTableRgIBNotMonotonic(t *testing.T) {
	raw := EncodeTCINFO(TableDraft{Columns: []ColumnView{
		{PropType: 0x0003, PropID: 0x0E08, Size: 4, Bit: 0},
	}})
	raw[2] = 8
	raw[3] = 0
	raw[4] = 4
	raw[5] = 0
	_, err := InspectTable(raw)
	mustInvariant(t, err, SectionTCINFO, "rgib")
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
