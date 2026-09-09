package writer

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"

	"github.com/grokify/outlook-pst-go/pkg/disk"
)

func TestBTPageHeaderLayoutMatchesSpec(t *testing.T) {
	raw, err := EncodeNBTLeaf(nil, 4, 0x8000)
	if err != nil {
		t.Fatal(err)
	}
	if raw[UnicodeBTHeaderOff] != 0 || raw[UnicodeBTHeaderOff+1] != MaxNBTLeafEntries || raw[UnicodeBTHeaderOff+2] != NBTLeafEntrySize || raw[UnicodeBTHeaderOff+3] != 0 {
		t.Fatalf("header %x %x %x %x", raw[UnicodeBTHeaderOff], raw[UnicodeBTHeaderOff+1], raw[UnicodeBTHeaderOff+2], raw[UnicodeBTHeaderOff+3])
	}
	v, err := InspectBTPage(raw, 0x8000)
	if err != nil {
		t.Fatal(err)
	}
	if v.Count != 0 || v.Max != MaxNBTLeafEntries || v.Level != 0 {
		t.Fatalf("%+v", v)
	}
	parsed, err := disk.ParseBTPage(raw, disk.FormatUnicode, disk.PageTypeNBT)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Header.NumEntries != 0 || parsed.Header.EntrySize != NBTLeafEntrySize || parsed.Header.Level != 0 {
		t.Fatalf("reader header %+v", parsed.Header)
	}
}

func TestNBTLeafRoundTripAndCorruptField(t *testing.T) {
	ents := []NBTEntry{
		{NID: 0x21, DataBID: 4, ParentNID: 0},
		{NID: 0x61, DataBID: 8, ParentNID: 0},
	}
	raw, err := EncodeNBTLeaf(ents, 5, 0x8200)
	if err != nil {
		t.Fatal(err)
	}
	v, err := InspectBTPage(raw, 0x8200)
	if err != nil {
		t.Fatal(err)
	}
	if v.Page.BID != 5 || v.Page.BID == 0x8200 {
		t.Fatalf("page BID %d", v.Page.BID)
	}
	if len(v.NBT) != 2 || v.NBT[0].NID != 0x21 || v.NBT[1].DataBID != 8 {
		t.Fatalf("%+v", v.NBT)
	}
	parsed, err := disk.ParseBTPage(raw, disk.FormatUnicode, disk.PageTypeNBT)
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed.NBTEntries) != 2 || parsed.NBTEntries[1].NID != 0x61 {
		t.Fatalf("reader %+v", parsed.NBTEntries)
	}

	bad := append([]byte(nil), raw...)
	binary.LittleEndian.PutUint64(bad[0:], 0x80) // first NID > second
	recrcBT(bad)
	_, err = InspectBTPage(bad, 0x8200)
	mustInvariant(t, err, SectionBTPAGE, "btkey")

	bad = append([]byte(nil), raw...)
	bad[UnicodeBTHeaderOff] = 3 // cEnt too large vs remaining keys
	recrcBT(bad)
	_, err = InspectBTPage(bad, 0x8200)
	if err == nil {
		t.Fatal("cEnt=3 passed")
	}
}

func recrcBT(page []byte) {
	max := PageSize - UnicodePageTrailer
	crc := crc32PST(page[:max])
	binary.LittleEndian.PutUint32(page[max+4:max+8], crc)
}

func TestBBTLeafRejectsZeroRefCount(t *testing.T) {
	_, err := EncodeBBTLeaf([]BBTEntry{{BID: 4, IB: 0x5000, CB: 16, RefCount: 0}}, 4, 0x8400)
	if !errors.Is(err, ErrInvalidArg) {
		t.Fatalf("cRef 0: %v", err)
	}
}

func TestBTNonleafFirstKeyNotMaxKey(t *testing.T) {
	// Two NBT leaves of 15 and 1: intermediate btkey must be the first NID
	// of each child, not the last NID of the left child.
	left := make([]NBTEntry, MaxNBTLeafEntries)
	for i := range left {
		left[i] = NBTEntry{NID: uint64(i + 1)}
	}
	right := []NBTEntry{{NID: uint64(MaxNBTLeafEntries + 1)}}
	leftPage, err := EncodeNBTLeaf(left, 4, 0x8000)
	if err != nil {
		t.Fatal(err)
	}
	rightPage, err := EncodeNBTLeaf(right, 5, 0x8200)
	if err != nil {
		t.Fatal(err)
	}
	lv, err := InspectBTPage(leftPage, 0x8000)
	if err != nil {
		t.Fatal(err)
	}
	rv, err := InspectBTPage(rightPage, 0x8200)
	if err != nil {
		t.Fatal(err)
	}
	leftFirst, _ := firstKey(lv)
	rightFirst, _ := firstKey(rv)
	leftLast := left[len(left)-1].NID
	if leftFirst == leftLast {
		t.Fatal("test setup: first==last")
	}
	kids := []BTEntry{
		{Key: leftFirst, Ref: BREF{BID: 4, IB: 0x8000}},
		{Key: rightFirst, Ref: BREF{BID: 5, IB: 0x8200}},
	}
	raw, err := EncodeBTNonleaf(PageNBT, 1, kids, 6, 0x8400)
	if err != nil {
		t.Fatal(err)
	}
	v, err := InspectBTPage(raw, 0x8400)
	if err != nil {
		t.Fatal(err)
	}
	if v.Kids[0].Key != leftFirst || v.Kids[1].Key != rightFirst {
		t.Fatalf("keys %+v", v.Kids)
	}
	if v.Kids[0].Key == leftLast {
		t.Fatal("used max key of left child as separator")
	}
	parsed, err := disk.ParseBTPage(raw, disk.FormatUnicode, disk.PageTypeNBT)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Header.Level != 1 || parsed.NonleafEntries[0].Key != leftFirst {
		t.Fatalf("reader %+v", parsed)
	}
}

func TestEncodeNBTRejectsUnsorted(t *testing.T) {
	_, err := EncodeNBTLeaf([]NBTEntry{{NID: 2}, {NID: 1}}, 4, 0x8000)
	if !errors.Is(err, ErrInvalidArg) {
		t.Fatalf("unsorted: %v", err)
	}
}

func TestPageBIDNotFileOffsetOnBTPage(t *testing.T) {
	raw, err := EncodeNBTLeaf([]NBTEntry{{NID: 0x21}}, 4, 0x8000)
	if err != nil {
		t.Fatal(err)
	}
	v, err := InspectBTPage(raw, 0x8000)
	if err != nil {
		t.Fatal(err)
	}
	if v.Page.BID == 0x8000 {
		t.Fatal("NBT BID derived from offset")
	}
	if bytes.Equal(raw[PageSize-8:], raw[:8]) {
		t.Fatal("trailer BID looks like payload")
	}
}
