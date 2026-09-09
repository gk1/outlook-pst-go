package writer

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/grokify/outlook-pst-go/pkg/disk"
)

type headerOffsetGolden struct {
	Name string `json:"name"`
	Off  int    `json:"off"`
	Size int    `json:"size"`
	Hex  string `json:"hex"`
}

type headerGoldenFile struct {
	Size       int                  `json:"size"`
	SHA256     string               `json:"sha256"`
	CRCPartial string               `json:"crc_partial"`
	CRCFull    string               `json:"crc_full"`
	Offsets    []headerOffsetGolden `json:"offsets"`
}

func headerOffsetTable(raw []byte) []headerOffsetGolden {
	u16 := func(off int) []byte { return raw[off : off+2] }
	u32 := func(off int) []byte { return raw[off : off+4] }
	u64 := func(off int) []byte { return raw[off : off+8] }
	b1 := func(off int) []byte { return raw[off : off+1] }
	return []headerOffsetGolden{
		{Name: "dwMagic", Off: OffMagic, Size: 4, Hex: hex.EncodeToString(u32(OffMagic))},
		{Name: "dwCRCPartial", Off: OffCRCPartial, Size: 4, Hex: hex.EncodeToString(u32(OffCRCPartial))},
		{Name: "wMagicClient", Off: OffMagicClient, Size: 2, Hex: hex.EncodeToString(u16(OffMagicClient))},
		{Name: "wVer", Off: OffWVer, Size: 2, Hex: hex.EncodeToString(u16(OffWVer))},
		{Name: "wVerClient", Off: OffWVerClient, Size: 2, Hex: hex.EncodeToString(u16(OffWVerClient))},
		{Name: "bPlatformCreate", Off: OffPlatformCreate, Size: 1, Hex: hex.EncodeToString(b1(OffPlatformCreate))},
		{Name: "bPlatformAccess", Off: OffPlatformAccess, Size: 1, Hex: hex.EncodeToString(b1(OffPlatformAccess))},
		{Name: "dwReserved1", Off: OffReserved1, Size: 4, Hex: hex.EncodeToString(u32(OffReserved1))},
		{Name: "dwReserved2", Off: OffReserved2, Size: 4, Hex: hex.EncodeToString(u32(OffReserved2))},
		{Name: "bidUnused", Off: OffBidUnused, Size: 8, Hex: hex.EncodeToString(u64(OffBidUnused))},
		{Name: "bidNextP", Off: OffBidNextP, Size: 8, Hex: hex.EncodeToString(u64(OffBidNextP))},
		{Name: "dwUnique", Off: OffUnique, Size: 4, Hex: hex.EncodeToString(u32(OffUnique))},
		{Name: "rgnid", Off: OffRgNID, Size: 128, Hex: hex.EncodeToString(raw[OffRgNID : OffRgNID+128])},
		{Name: "qwUnused", Off: OffQWUnused, Size: 8, Hex: hex.EncodeToString(u64(OffQWUnused))},
		{Name: "root", Off: OffRoot, Size: UnicodeRootSize, Hex: hex.EncodeToString(raw[OffRoot : OffRoot+UnicodeRootSize])},
		{Name: "dwAlign", Off: OffAlign, Size: 4, Hex: hex.EncodeToString(u32(OffAlign))},
		{Name: "rgbFM", Off: OffRgbFM, Size: 128, Hex: hex.EncodeToString(raw[OffRgbFM:OffRgbFP])},
		{Name: "rgbFP", Off: OffRgbFP, Size: 128, Hex: hex.EncodeToString(raw[OffRgbFP:OffSentinel])},
		{Name: "bSentinel", Off: OffSentinel, Size: 1, Hex: hex.EncodeToString(b1(OffSentinel))},
		{Name: "bCryptMethod", Off: OffCrypt, Size: 1, Hex: hex.EncodeToString(b1(OffCrypt))},
		{Name: "rgbReserved", Off: OffRgbReserved, Size: 2, Hex: hex.EncodeToString(u16(OffRgbReserved))},
		{Name: "bidNextB", Off: OffBidNextB, Size: 8, Hex: hex.EncodeToString(u64(OffBidNextB))},
		{Name: "dwCRCFull", Off: OffCRCFull, Size: 4, Hex: hex.EncodeToString(u32(OffCRCFull))},
		{Name: "rgbReserved2", Off: OffRgbReserved2, Size: 3, Hex: hex.EncodeToString(raw[OffRgbReserved2:OffBReserved])},
		{Name: "bReserved", Off: OffBReserved, Size: 1, Hex: hex.EncodeToString(b1(OffBReserved))},
		{Name: "rgbReserved3", Off: OffRgbReserved3, Size: 32, Hex: hex.EncodeToString(raw[OffRgbReserved3:UnicodeHeaderSize])},
	}
}

func TestUnicodeHeaderByteOffsetGoldens(t *testing.T) {
	raw := mustHeader(t, DefaultHeaderDraft())
	if len(raw) != UnicodeHeaderSize {
		t.Fatalf("size %d", len(raw))
	}
	sum := sha256.Sum256(raw)
	got := headerGoldenFile{
		Size:       UnicodeHeaderSize,
		SHA256:     hex.EncodeToString(sum[:]),
		CRCPartial: hex.EncodeToString(raw[OffCRCPartial : OffCRCPartial+4]),
		CRCFull:    hex.EncodeToString(raw[OffCRCFull : OffCRCFull+4]),
		Offsets:    headerOffsetTable(raw),
	}
	path := filepath.Join("testdata", "goldens", "header.unicode.json")
	body, err := json.MarshalIndent(got, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	body = append(body, '\n')
	if *updateGoldens {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, body, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("missing golden %s (go test ./writer/v2 -update-goldens): %v", path, err)
	}
	if !bytes.Equal(body, want) {
		t.Fatalf("header offset golden diverged\ngot %s\nwant %s", body, want)
	}
}

func TestUnicodeHeaderRoundTripExistingReader(t *testing.T) {
	d := DefaultHeaderDraft()
	raw := mustHeader(t, d)
	h, err := disk.ReadHeader(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if h.Format != disk.FormatUnicode {
		t.Fatalf("format %v", h.Format)
	}
	if h.DWMagic != PSTMagic || h.WMagicClient != disk.ClientMagicPST {
		t.Fatalf("magic 0x%08x client 0x%04x", h.DWMagic, h.WMagicClient)
	}
	if h.WVer != UnicodeWVer || h.WVerClient != ClientVerPST {
		t.Fatalf("wVer=%d wVerClient=%d", h.WVer, h.WVerClient)
	}
	if h.BPlatformCreate != PlatformWin32 || h.BPlatformAccess != PlatformWin32 {
		t.Fatalf("platform %d/%d", h.BPlatformCreate, h.BPlatformAccess)
	}
	if h.DWOpenDBID != 0 || h.DWOpenClaimID != 0 {
		t.Fatalf("reserved %d %d", h.DWOpenDBID, h.DWOpenClaimID)
	}
	if h.BidNextP != d.BidNextP || h.BidNextB != d.BidNextB || h.DWUnique != d.Unique {
		t.Fatalf("counters p=%d b=%d u=%d", h.BidNextP, h.BidNextB, h.DWUnique)
	}
	if h.BCryptMethod != disk.CryptMethod(d.Crypt) {
		t.Fatalf("crypt %d", h.BCryptMethod)
	}
	r := h.Root
	if r.COrphans != 0 || r.BARVec != 0 || r.CARVec != 0 {
		t.Fatalf("root reserved %+v", r)
	}
	if r.IBFileEOF != d.Root.FileEOF || r.IBAMapLast != d.Root.AMapLast {
		t.Fatalf("eof/amap %+v", r)
	}
	if r.CBAMapFree != d.Root.AMapFree || r.CBPMapFree != d.Root.PMapFree {
		t.Fatalf("free %+v", r)
	}
	if r.BRefNBT.BID != d.Root.NBTBID || r.BRefNBT.IB != d.Root.NBTIB {
		t.Fatalf("nbt %+v", r.BRefNBT)
	}
	if r.BRefBBT.BID != d.Root.BBTBID || r.BRefBBT.IB != d.Root.BBTIB {
		t.Fatalf("bbt %+v", r.BRefBBT)
	}
	if r.FAMapValid != AMapValid2 {
		t.Fatalf("fAMapValid %d", r.FAMapValid)
	}
	view, err := InspectHeader(raw)
	if err != nil {
		t.Fatal(err)
	}
	if h.DWCRCPartial != view.CRCPartial || h.DWCRCFull != view.CRCFull {
		t.Fatalf("crc reader=%08x/%08x view=%08x/%08x", h.DWCRCPartial, h.DWCRCFull, view.CRCPartial, view.CRCFull)
	}
}

func TestUnicodeHeaderDeterministic(t *testing.T) {
	a := mustHeader(t, DefaultHeaderDraft())
	b := mustHeader(t, DefaultHeaderDraft())
	if !bytes.Equal(a, b) {
		t.Fatal("header bytes depend on host state")
	}
}

func TestDefaultRgNIDMatchesSpecTable(t *testing.T) {
	nids := DefaultRgNID()
	if nids[NIDTypeNormalFolder] != NIDIndexDefault {
		t.Fatalf("folder %d", nids[NIDTypeNormalFolder])
	}
	if nids[NIDTypeSearchFolder] != NIDIndexSearchFolder {
		t.Fatalf("search %d", nids[NIDTypeSearchFolder])
	}
	if nids[NIDTypeNormalMessage] != NIDIndexNormalMessage {
		t.Fatalf("message %d", nids[NIDTypeNormalMessage])
	}
	if nids[NIDTypeAssocMessage] != NIDIndexAssocMessage {
		t.Fatalf("assoc %d", nids[NIDTypeAssocMessage])
	}
	for i, n := range nids {
		switch i {
		case NIDTypeSearchFolder, NIDTypeNormalMessage, NIDTypeAssocMessage:
		default:
			if n != NIDIndexDefault {
				t.Fatalf("rgnid[%d]=0x%x want 0x400", i, n)
			}
		}
	}
	raw := mustHeader(t, DefaultHeaderDraft())
	for i := 0; i < 32; i++ {
		got := binary.LittleEndian.Uint32(raw[OffRgNID+i*4:])
		if got != nids[i] {
			t.Fatalf("encoded rgnid[%d]=0x%x want 0x%x", i, got, nids[i])
		}
	}
}

func TestEncodeUnicodeRootSizeAndReserved(t *testing.T) {
	raw, err := EncodeUnicodeRoot(DefaultHeaderDraft().Root)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) != UnicodeRootSize {
		t.Fatalf("size %d", len(raw))
	}
	view, err := InspectRoot(raw)
	if err != nil {
		t.Fatal(err)
	}
	if view.Reserved != 0 || view.ARVec != 0 || view.CARVec != 0 {
		t.Fatalf("reserved %+v", view)
	}
	if view.AMapValid != AMapValid2 {
		t.Fatalf("valid %d", view.AMapValid)
	}
}

func TestEncodeUnicodeHeaderRejectsANSI(t *testing.T) {
	d := DefaultHeaderDraft()
	d.WVer = 15
	_, err := EncodeUnicodeHeader(d)
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("got %v", err)
	}
	var we *Error
	if !errors.As(err, &we) || we.Feature != FeatureANSI {
		t.Fatalf("want FeatureANSI, got %#v", err)
	}
}

func TestEncodeUnicodeHeaderRejectsOST(t *testing.T) {
	d := DefaultHeaderDraft()
	d.MagicClient = ClientMagicOST
	_, err := EncodeUnicodeHeader(d)
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("got %v", err)
	}
}

func TestEncodeUnicodeHeaderRejectsWIP(t *testing.T) {
	d := DefaultHeaderDraft()
	d.WVer = UnicodeWVerWIP
	_, err := EncodeUnicodeHeader(d)
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("got %v", err)
	}
	d = DefaultHeaderDraft()
	d.Crypt = CryptWIP
	_, err = EncodeUnicodeHeader(d)
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("got %v", err)
	}
}

func TestInspectHeaderMutations(t *testing.T) {
	type mut struct {
		name    string
		section string
		field   string
		patch   func([]byte)
		recrc   bool
	}
	cases := []mut{
		{name: "too-small", section: SectionHeader, field: "size", patch: func(b []byte) { /* handled below */ }},
		{name: "dwMagic", section: SectionHeader, field: "dwMagic", patch: func(b []byte) { b[OffMagic] ^= 0xFF }},
		{name: "wMagicClient", section: SectionHeader, field: "wMagicClient", patch: func(b []byte) {
			binary.LittleEndian.PutUint16(b[OffMagicClient:], 0x0000)
		}, recrc: true},
		{name: "wVer-low", section: SectionHeader, field: "wVer", patch: func(b []byte) {
			binary.LittleEndian.PutUint16(b[OffWVer:], 1)
		}, recrc: true},
		{name: "wVer-high", section: SectionHeader, field: "wVer", patch: func(b []byte) {
			binary.LittleEndian.PutUint16(b[OffWVer:], 24)
		}, recrc: true},
		{name: "wVerClient", section: SectionHeader, field: "wVerClient", patch: func(b []byte) {
			binary.LittleEndian.PutUint16(b[OffWVerClient:], 12)
		}, recrc: true},
		{name: "bPlatformCreate", section: SectionHeader, field: "bPlatformCreate", patch: func(b []byte) { b[OffPlatformCreate] = 0 }, recrc: true},
		{name: "bPlatformAccess", section: SectionHeader, field: "bPlatformAccess", patch: func(b []byte) { b[OffPlatformAccess] = 2 }, recrc: true},
		{name: "qwUnused", section: SectionHeader, field: "qwUnused", patch: func(b []byte) { b[OffQWUnused] = 1 }, recrc: true},
		{name: "dwAlign", section: SectionHeader, field: "dwAlign", patch: func(b []byte) { b[OffAlign] = 1 }, recrc: true},
		{name: "rgbReserved", section: SectionHeader, field: "rgbReserved", patch: func(b []byte) { b[OffRgbReserved] = 1 }, recrc: true},
		{name: "rgbFM", section: SectionHeader, field: "rgbFM", patch: func(b []byte) { b[OffRgbFM] = 0x00 }, recrc: true},
		{name: "rgbFP", section: SectionHeader, field: "rgbFP", patch: func(b []byte) { b[OffRgbFP] = 0x00 }, recrc: true},
		{name: "bSentinel", section: SectionHeader, field: "bSentinel", patch: func(b []byte) { b[OffSentinel] = 0 }, recrc: true},
		{name: "bCryptMethod", section: SectionHeader, field: "bCryptMethod", patch: func(b []byte) { b[OffCrypt] = 0x03 }, recrc: true},
		{name: "dwCRCPartial", section: SectionHeader, field: "dwCRCPartial", patch: func(b []byte) { b[OffCRCPartial] ^= 0x01 }},
		{name: "dwCRCFull", section: SectionHeader, field: "dwCRCFull", patch: func(b []byte) {
			// bidNextB is inside the 516-byte full CRC and outside the 471-byte partial CRC.
			b[OffBidNextB] ^= 0x01
		}},
		{name: "root-dwReserved", section: SectionRoot, field: "dwReserved", patch: func(b []byte) { b[OffRoot+OffRootReserved] = 1 }, recrc: true},
		{name: "root-bReserved", section: SectionRoot, field: "bReserved", patch: func(b []byte) { b[OffRoot+OffRootARVec] = 1 }, recrc: true},
		{name: "root-wReserved", section: SectionRoot, field: "wReserved", patch: func(b []byte) { b[OffRoot+OffRootCARVec] = 1 }, recrc: true},
		{name: "fAMapValid1", section: SectionRoot, field: "fAMapValid", patch: func(b []byte) { b[OffRoot+OffRootAMapValid] = AMapValid1 }, recrc: true},
		{name: "fAMapValidUnknown", section: SectionRoot, field: "fAMapValid", patch: func(b []byte) { b[OffRoot+OffRootAMapValid] = 0x03 }, recrc: true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			if tc.name == "too-small" {
				_, err := InspectHeader(make([]byte, 16))
				mustInvariant(t, err, SectionHeader, "size")
				return
			}
			raw := mustHeader(t, DefaultHeaderDraft())
			tc.patch(raw)
			if tc.recrc {
				recrcHeader(raw)
			}
			_, err := InspectHeader(raw)
			mustInvariant(t, err, tc.section, tc.field)
		})
	}
}

func TestInspectHeaderWIPAndOST(t *testing.T) {
	raw := mustHeader(t, DefaultHeaderDraft())
	binary.LittleEndian.PutUint16(raw[OffWVer:], UnicodeWVerWIP)
	recrcHeader(raw)
	_, err := InspectHeader(raw)
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("wVer 37: %v", err)
	}
	raw = mustHeader(t, DefaultHeaderDraft())
	binary.LittleEndian.PutUint16(raw[OffMagicClient:], ClientMagicOST)
	recrcHeader(raw)
	_, err = InspectHeader(raw)
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("OST: %v", err)
	}
	raw = mustHeader(t, DefaultHeaderDraft())
	raw[OffCrypt] = CryptWIP
	recrcHeader(raw)
	_, err = InspectHeader(raw)
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("WIP crypt: %v", err)
	}
}

func TestInspectHeaderCRCSpans(t *testing.T) {
	raw := mustHeader(t, DefaultHeaderDraft())
	if OffMagicClient+HeaderPartialCRCLen != 479 {
		t.Fatalf("partial end %d", OffMagicClient+HeaderPartialCRCLen)
	}
	if OffMagicClient+HeaderFullCRCLen != OffCRCFull {
		t.Fatalf("full end %d want %d", OffMagicClient+HeaderFullCRCLen, OffCRCFull)
	}
	// Mutating rgbReserved3 (after dwCRCFull) must not change either CRC.
	raw[OffRgbReserved3] = 0xAA
	view, err := InspectHeader(raw)
	if err != nil {
		t.Fatal(err)
	}
	if view.CRCPartial == 0 || view.CRCFull == 0 {
		t.Fatal("crc zero")
	}
}

func TestEncodeUnicodeHeaderRejectsHighWVer(t *testing.T) {
	d := DefaultHeaderDraft()
	d.WVer = 24
	_, err := EncodeUnicodeHeader(d)
	if !errors.Is(err, ErrInvalidArg) {
		t.Fatalf("got %v, want ErrInvalidArg", err)
	}
	var we *Error
	if !errors.As(err, &we) || we.Field != "wVer" {
		t.Fatalf("field=%q err=%v", we.Field, err)
	}
}

func TestEncodeUnicodeHeaderRequiresWVerClient19(t *testing.T) {
	d := DefaultHeaderDraft()
	d.WVerClient = 12
	_, err := EncodeUnicodeHeader(d)
	if !errors.Is(err, ErrInvalidArg) {
		t.Fatalf("got %v, want ErrInvalidArg", err)
	}
	d = DefaultHeaderDraft()
	d.WVer = 0
	d.WVerClient = 0
	raw := mustHeader(t, d)
	if binary.LittleEndian.Uint16(raw[OffWVer:]) != UnicodeWVer {
		t.Fatalf("wVer %d", binary.LittleEndian.Uint16(raw[OffWVer:]))
	}
	if binary.LittleEndian.Uint16(raw[OffWVerClient:]) != ClientVerPST {
		t.Fatalf("wVerClient %d", binary.LittleEndian.Uint16(raw[OffWVerClient:]))
	}
}

func TestDefaultHeaderUnassignedBTrees(t *testing.T) {
	d := DefaultHeaderDraft()
	if d.Root.NBTBID != 0 || d.Root.NBTIB != 0 || d.Root.BBTBID != 0 || d.Root.BBTIB != 0 {
		t.Fatalf("placeholder BREFs %+v", d.Root)
	}
	if d.BidNextP != FirstAllocBID || d.BidNextB != FirstAllocBID {
		t.Fatalf("BID counters p=%d b=%d", d.BidNextP, d.BidNextB)
	}
	raw := mustHeader(t, d)
	h, err := disk.ReadHeader(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if h.Root.BRefNBT.BID != 0 || h.Root.BRefNBT.IB != 0 {
		t.Fatalf("nbt %+v", h.Root.BRefNBT)
	}
	if h.Root.BRefBBT.BID != 0 || h.Root.BRefBBT.IB != 0 {
		t.Fatalf("bbt %+v", h.Root.BRefBBT)
	}
	if h.BidNextP != FirstAllocBID || h.BidNextB != FirstAllocBID {
		t.Fatalf("reader counters p=%d b=%d", h.BidNextP, h.BidNextB)
	}
}

func TestEncodeUnicodeRootPreservesInvalidAMap(t *testing.T) {
	r := DefaultHeaderDraft().Root
	r.AMapValid = AMapInvalid
	raw, err := EncodeUnicodeRoot(r)
	if err != nil {
		t.Fatal(err)
	}
	if raw[OffRootAMapValid] != AMapInvalid {
		t.Fatalf("encoded 0x%02x, coerced away from INVALID_AMAP", raw[OffRootAMapValid])
	}
	view, err := InspectRoot(raw)
	if err != nil {
		t.Fatal(err)
	}
	if view.AMapValid != AMapInvalid {
		t.Fatalf("round-trip %d", view.AMapValid)
	}

	r.AMapValid = AMapValid1
	raw, err = EncodeUnicodeRoot(r)
	if err != nil {
		t.Fatal(err)
	}
	if raw[OffRootAMapValid] != AMapValid1 {
		t.Fatalf("VALID_AMAP1 encoded 0x%02x", raw[OffRootAMapValid])
	}
	view, err = InspectRoot(raw)
	if err != nil {
		t.Fatal(err)
	}
	if view.AMapValid != AMapValid1 {
		t.Fatalf("VALID_AMAP1 round-trip %d", view.AMapValid)
	}
}

func TestEncodeUnicodeHeaderTransactionInvalidAMap(t *testing.T) {
	d := DefaultHeaderDraft()
	d.Root.AMapValid = AMapInvalid
	raw := mustHeader(t, d)
	if raw[OffRoot+OffRootAMapValid] != AMapInvalid {
		t.Fatalf("header coerced fAMapValid to 0x%02x", raw[OffRoot+OffRootAMapValid])
	}
	_, err := InspectHeader(raw)
	mustInvariant(t, err, SectionRoot, "fAMapValid")
	view, err := InspectRoot(raw[OffRoot : OffRoot+UnicodeRootSize])
	if err != nil {
		t.Fatal(err)
	}
	if view.AMapValid != AMapInvalid {
		t.Fatalf("root view %d", view.AMapValid)
	}
	h, err := disk.ReadHeader(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if h.Root.FAMapValid != AMapInvalid {
		t.Fatalf("reader fAMapValid %d", h.Root.FAMapValid)
	}
}
