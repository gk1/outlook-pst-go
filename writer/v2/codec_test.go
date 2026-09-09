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

type codecGoldenFile struct {
	Name   string `json:"name"`
	Kind   string `json:"kind"`
	BID    string `json:"bid"`
	IB     string `json:"ib"`
	CB     int    `json:"cb"`
	Disk   int    `json:"disk"`
	CRC    string `json:"crc"`
	Sig    string `json:"sig"`
	SHA256 string `json:"sha256"`
	Hex    string `json:"hex"`
}

func writeOrCheckGolden(t *testing.T, name string, got codecGoldenFile) {
	t.Helper()
	path := filepath.Join("testdata", "goldens", name)
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
		t.Fatalf("golden %s diverged\ngot %s\nwant %s", name, body, want)
	}
}

func TestSequentialIDsPageVsBlockIncrement(t *testing.T) {
	ids := NewSequentialIDs()
	p1, err := ids.TakePageBID()
	if err != nil {
		t.Fatal(err)
	}
	p2, err := ids.TakePageBID()
	if err != nil {
		t.Fatal(err)
	}
	if p1 != FirstAllocBID || p2 != FirstAllocBID+PageBIDIncrement {
		t.Fatalf("page BIDs %d %d want 4 then 5", p1, p2)
	}
	b1, err := ids.TakeBlockBID()
	if err != nil {
		t.Fatal(err)
	}
	b2, err := ids.TakeBlockBID()
	if err != nil {
		t.Fatal(err)
	}
	if b1 != FirstAllocBID || b2 != FirstAllocBID+BlockBIDIncrement {
		t.Fatalf("block BIDs %d %d want 4 then 8", b1, b2)
	}
	if BIDIsInternal(b1) || BIDHasReserved(b1) {
		t.Fatalf("external BID bits 0x%x", b1)
	}
	in, err := ids.TakeInternalBlockBID()
	if err != nil {
		t.Fatal(err)
	}
	if !BIDIsInternal(in) || BIDHasReserved(in) || in != FirstAllocBID+2*BlockBIDIncrement+BIDInternal {
		t.Fatalf("internal BID 0x%x", in)
	}
}

func TestSequentialIDsOverflow(t *testing.T) {
	ids := NewSequentialIDs()
	ids.nextPage = ^uint64(0)
	if _, err := ids.TakePageBID(); !errors.Is(err, ErrLimit) {
		t.Fatalf("page overflow: %v", err)
	}
	ids.nextBlock = ^uint64(0) - 3
	if _, err := ids.TakeBlockBID(); !errors.Is(err, ErrLimit) {
		t.Fatalf("block overflow: %v", err)
	}
	if ids.NextPageBID() != 0 || ids.NextBlockBID() != 0 {
		t.Fatal("Next* must return 0 after overflow")
	}
}

func TestEncodeNBTPageBIDIsNotFileOffset(t *testing.T) {
	payload := bytes.Repeat([]byte{0x11}, 16)
	const bid, ib = uint64(4), uint64(0x2000)
	raw, err := EncodePage(payload, PageNBT, bid, ib)
	if err != nil {
		t.Fatal(err)
	}
	pg, err := InspectPage(raw, ib)
	if err != nil {
		t.Fatal(err)
	}
	if pg.BID != bid {
		t.Fatalf("page BID 0x%x derived from offset?", pg.BID)
	}
	if pg.BID == ib {
		t.Fatal("NBT BID equals file offset")
	}
	if pg.Sig != signature(bid, ib) {
		t.Fatalf("sig 0x%04x", pg.Sig)
	}
	if _, err := EncodePage(payload, PageNBT, 0, ib); !errors.Is(err, ErrInvalidArg) {
		t.Fatalf("null NBT BID: %v", err)
	}
}

func TestEncodeAMapPageStillUsesIB(t *testing.T) {
	raw, err := EncodePage([]byte{0xFF}, PageAMap, 4, FirstAMapPageOffset)
	if err != nil {
		t.Fatal(err)
	}
	pg, err := InspectPage(raw, FirstAMapPageOffset)
	if err != nil {
		t.Fatal(err)
	}
	if pg.BID != FirstAMapPageOffset || pg.Sig != 0 {
		t.Fatalf("AMap trailer bid=0x%x sig=%d", pg.BID, pg.Sig)
	}
}

func TestPageCRCCoversPayloadNotTrailer(t *testing.T) {
	raw, err := EncodePage(bytes.Repeat([]byte{0xAA}, 16), PageNBT, 4, 0x2000)
	if err != nil {
		t.Fatal(err)
	}
	_, err = InspectPage(raw, 0x2000)
	if err != nil {
		t.Fatal(err)
	}
	raw[0] ^= 0x01
	_, err = InspectPage(raw, 0x2000)
	mustInvariant(t, err, SectionPageTrailer, "dwCRC")
	raw[0] ^= 0x01
	raw[PageSize-UnicodePageTrailer] ^= 0x01 // ptype
	_, err = InspectPage(raw, 0x2000)
	if err == nil {
		t.Fatal("trailer mutation passed")
	}
}

func TestBlockRoundTripAndPaddingExcludedFromCRC(t *testing.T) {
	data := []byte("hello")
	const bid, ib = uint64(4), uint64(0x5000)
	raw, err := EncodeBlock(data, bid, ib)
	if err != nil {
		t.Fatal(err)
	}
	if uint64(len(raw)) != BlockDiskSize(uint64(len(data))) || len(raw)%int(BytesPerSlot) != 0 {
		t.Fatalf("disk size %d", len(raw))
	}
	if raw[5] != 0 {
		t.Fatal("padding not zero")
	}
	view, err := InspectBlock(raw, ib)
	if err != nil {
		t.Fatal(err)
	}
	if view.CB != uint16(len(data)) || !bytes.Equal(view.Data, data) {
		t.Fatalf("view %+v", view)
	}
	if view.Internal {
		t.Fatal("external marked internal")
	}
	raw[5] ^= 0xFF // padding after cb
	if _, err := InspectBlock(raw, ib); err != nil {
		t.Fatalf("padding must not be in CRC: %v", err)
	}
	raw[5] ^= 0xFF
	raw[0] ^= 0x01
	_, err = InspectBlock(raw, ib)
	mustInvariant(t, err, SectionBlockTrailer, "dwCRC")
}

func TestBlockCorruptOneField(t *testing.T) {
	data := []byte("hello")
	const bid, ib = uint64(4), uint64(0x5000)
	raw, err := EncodeBlock(data, bid, ib)
	if err != nil {
		t.Fatal(err)
	}
	tr := PageSize // not used; trailer is last 16 of block
	_ = tr
	cases := []struct {
		name, section, field string
		mut                  func([]byte)
	}{
		{"cb", SectionBlockTrailer, "size", func(b []byte) {
			// 49 bytes of payload would occupy a 128-byte disk slot.
			binary.LittleEndian.PutUint16(b[len(b)-UnicodeBlockTrailer:], 49)
		}},
		{"wSig", SectionSignature, "wSig", func(b []byte) {
			b[len(b)-UnicodeBlockTrailer+2] ^= 0x01
		}},
		{"dwCRC", SectionBlockTrailer, "dwCRC", func(b []byte) {
			b[len(b)-UnicodeBlockTrailer+4] ^= 0x01
		}},
		{"bid", SectionSignature, "wSig", func(b []byte) {
			binary.LittleEndian.PutUint64(b[len(b)-UnicodeBlockTrailer+8:], bid+BlockBIDIncrement)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mut := append([]byte(nil), raw...)
			tc.mut(mut)
			_, err := InspectBlock(mut, ib)
			mustInvariant(t, err, tc.section, tc.field)
		})
	}
}

func TestPageCorruptOneField(t *testing.T) {
	raw, err := EncodePage([]byte{0xAA}, PageNBT, 4, 0x2000)
	if err != nil {
		t.Fatal(err)
	}
	max := PageSize - UnicodePageTrailer
	cases := []struct {
		name, section, field string
		mut                  func([]byte)
	}{
		{"ptypeRepeat", SectionPageTrailer, "ptypeRepeat", func(b []byte) { b[max+1] = PageBBT }},
		{"wSig", SectionSignature, "wSig", func(b []byte) { b[max+2] ^= 0x01 }},
		{"dwCRC", SectionPageTrailer, "dwCRC", func(b []byte) { b[max+4] ^= 0x01 }},
		{"bid", SectionSignature, "wSig", func(b []byte) {
			binary.LittleEndian.PutUint64(b[max+8:], 8)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mut := append([]byte(nil), raw...)
			tc.mut(mut)
			_, err := InspectPage(mut, 0x2000)
			mustInvariant(t, err, tc.section, tc.field)
		})
	}
}

func TestExternalBlockEncryptionBoundary(t *testing.T) {
	plain := []byte("hello")
	const bid, ib = uint64(4), uint64(0x5000)
	raw, err := EncodeExternalBlock(plain, bid, ib, CryptPermute)
	if err != nil {
		t.Fatal(err)
	}
	cipher := disk.PermuteEncode(plain)
	if bytes.Equal(raw[:len(plain)], plain) {
		t.Fatal("external permute left plaintext on disk")
	}
	if !bytes.Equal(raw[:len(plain)], cipher) {
		t.Fatal("on-disk bytes are not permute ciphertext")
	}
	plainCRC := crc32PST(plain)
	gotCRC := binary.LittleEndian.Uint32(raw[len(raw)-UnicodeBlockTrailer+4:])
	if gotCRC == plainCRC {
		t.Fatal("CRC hashed plaintext instead of ciphertext")
	}
	if gotCRC != crc32PST(cipher) {
		t.Fatalf("CRC 0x%08x want ciphertext", gotCRC)
	}
	view, err := InspectBlockCrypt(raw, ib, CryptPermute)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(view.Plain, plain) || !bytes.Equal(view.Data, cipher) {
		t.Fatalf("decrypt mismatch plain=%q data=%q", view.Plain, view.Data)
	}
	if _, err := EncodeExternalBlock(plain, bid, ib, CryptWIP); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("WIP: %v", err)
	}
}

func TestInternalBlockNeverEncrypted(t *testing.T) {
	data := []byte{0x01, 0x00, 0x00, 0x00} // dummy XBLOCK header fragment
	ids := NewSequentialIDs()
	bid, err := ids.TakeInternalBlockBID()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := EncodeInternalBlock(data, bid, 0x6000)
	if err != nil {
		t.Fatal(err)
	}
	view, err := InspectBlockCrypt(raw, 0x6000, CryptPermute)
	if err != nil {
		t.Fatal(err)
	}
	if !view.Internal || !BIDIsInternal(view.BID) {
		t.Fatalf("internal bits %+v", view)
	}
	if !bytes.Equal(view.Data, data) || !bytes.Equal(view.Plain, data) {
		t.Fatal("internal payload was encrypted")
	}
	if _, err := EncodeExternalBlock(data, bid, 0x6000, CryptNone); !errors.Is(err, ErrInvalidArg) {
		t.Fatalf("external with internal BID: %v", err)
	}
}

func TestBlockMaxCBAndDiskAlignment(t *testing.T) {
	if _, err := EncodeBlock(make([]byte, MaxDataBlockCB), 4, 0x5000); err != nil {
		t.Fatal(err)
	}
	if _, err := EncodeBlock(make([]byte, MaxDataBlockCB+1), 4, 0x5000); !errors.Is(err, ErrInvalidArg) {
		t.Fatalf("8177: %v", err)
	}
	raw, err := EncodeBlock(make([]byte, 1), 4, 0x5000)
	if err != nil {
		t.Fatal(err)
	}
	if uint64(len(raw)) != 64 {
		t.Fatalf("1-byte block disk %d", len(raw))
	}
}

func TestSpecDerivedPageAndBlockGoldens(t *testing.T) {
	page, err := EncodePage([]byte("NBT"), PageNBT, 4, 0x2000)
	if err != nil {
		t.Fatal(err)
	}
	pg, err := InspectPage(page, 0x2000)
	if err != nil {
		t.Fatal(err)
	}
	psum := sha256.Sum256(page)
	writeOrCheckGolden(t, "page.nbt.json", codecGoldenFile{
		Name:   "nbt-page",
		Kind:   "page",
		BID:    hex.EncodeToString(page[PageSize-8:]),
		IB:     "0000000000002000",
		CB:     PagePayloadMax(),
		Disk:   PageSize,
		CRC:    hex.EncodeToString(page[PageSize-UnicodePageTrailer+4 : PageSize-UnicodePageTrailer+8]),
		Sig:    hex.EncodeToString(page[PageSize-UnicodePageTrailer+2 : PageSize-UnicodePageTrailer+4]),
		SHA256: hex.EncodeToString(psum[:]),
		Hex:    hex.EncodeToString(page),
	})
	if pg.BID != 4 {
		t.Fatalf("golden page BID %d", pg.BID)
	}

	block, err := EncodeBlock([]byte("hello"), 4, 0x5000)
	if err != nil {
		t.Fatal(err)
	}
	_, err = InspectBlock(block, 0x5000)
	if err != nil {
		t.Fatal(err)
	}
	bsum := sha256.Sum256(block)
	tr := block[len(block)-UnicodeBlockTrailer:]
	writeOrCheckGolden(t, "block.external.json", codecGoldenFile{
		Name:   "external-hello",
		Kind:   "block",
		BID:    hex.EncodeToString(tr[8:16]),
		IB:     "0000000000005000",
		CB:     5,
		Disk:   len(block),
		CRC:    hex.EncodeToString(tr[4:8]),
		Sig:    hex.EncodeToString(tr[2:4]),
		SHA256: hex.EncodeToString(bsum[:]),
		Hex:    hex.EncodeToString(block),
	})
}

func TestEncodeBlockRejectsNullAndReservedBID(t *testing.T) {
	if _, err := EncodeBlock([]byte("x"), 0, 0x5000); !errors.Is(err, ErrInvalidArg) {
		t.Fatalf("null: %v", err)
	}
	if _, err := EncodeBlock([]byte("x"), 5, 0x5000); !errors.Is(err, ErrInvalidArg) {
		t.Fatalf("reserved bit: %v", err)
	}
}
