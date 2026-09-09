package writer

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"io"
	"testing"

	"github.com/grokify/outlook-pst-go/pkg/disk"
)

type boomReader struct {
	left int
}

func (b *boomReader) Read(p []byte) (int, error) {
	if b.left <= 0 {
		return 0, errors.New("boom")
	}
	n := len(p)
	if n > b.left {
		n = b.left
	}
	for i := 0; i < n; i++ {
		p[i] = 0x11
	}
	b.left -= n
	return n, nil
}

type repeatByte byte

func (r repeatByte) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = byte(r)
	}
	return len(p), nil
}

func TestXBlockLayoutRoundTrip(t *testing.T) {
	bids := []uint64{4, 8, 12}
	raw, err := EncodeXBlock(XBlockLevel, 24, bids)
	if err != nil {
		t.Fatal(err)
	}
	if raw[0] != BlockTypeXBlock || raw[1] != XBlockLevel {
		t.Fatalf("header %x %x", raw[0], raw[1])
	}
	if binary.LittleEndian.Uint16(raw[2:4]) != 3 || binary.LittleEndian.Uint32(raw[4:8]) != 24 {
		t.Fatalf("cEnt/lcbTotal %x", raw[2:8])
	}
	v, err := InspectXBlock(raw)
	if err != nil {
		t.Fatal(err)
	}
	if v.Count != 3 || v.Total != 24 || len(v.BIDs) != 3 || v.BIDs[2] != 12 {
		t.Fatalf("%+v", v)
	}
	parsed, err := disk.ParseExtendedBlock(raw, disk.FormatUnicode)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Level != 1 || parsed.Count != 3 || parsed.TotalSize != 24 || parsed.BIDs[0] != 4 {
		t.Fatalf("legacy %+v", parsed)
	}
}

func TestXBlockRejectsDuplicateAndLevel(t *testing.T) {
	if _, err := EncodeXBlock(XBlockLevel, 8, []uint64{4, 4}); !errors.Is(err, ErrInvalidArg) {
		t.Fatalf("dup: %v", err)
	}
	if _, err := EncodeXBlock(3, 8, []uint64{4}); !errors.Is(err, ErrInvalidArg) {
		t.Fatalf("level: %v", err)
	}
	raw, err := EncodeXBlock(XBlockLevel, 8, []uint64{4, 8})
	if err != nil {
		t.Fatal(err)
	}
	binary.LittleEndian.PutUint64(raw[16:], 4)
	if _, err := InspectXBlock(raw); !errors.Is(err, ErrInvariant) {
		t.Fatalf("inspect dup: %v", err)
	}
}

func TestDirectAndXBlockTransitions(t *testing.T) {
	n := NewNDB(nil)
	small := bytes.NewReader([]byte("hello"))
	root, err := n.PutDataTree(small, 5)
	if err != nil {
		t.Fatal(err)
	}
	if BIDIsInternal(root.BID) || root.CB != 5 {
		t.Fatalf("direct %+v", root)
	}
	mustNode(t, n, 0x21, root.BID, 0, 0)

	big := io.LimitReader(repeatByte(0xA5), int64(MaxDataBlockCB)+1)
	xr, err := n.PutDataTree(big, int64(MaxDataBlockCB)+1)
	if err != nil {
		t.Fatal(err)
	}
	if !BIDIsInternal(xr.BID) {
		t.Fatal("expected XBLOCK root")
	}
	data, err := n.blockPayload(xr)
	if err != nil {
		t.Fatal(err)
	}
	xb, err := InspectXBlock(data)
	if err != nil {
		t.Fatal(err)
	}
	if xb.Level != XBlockLevel || xb.Count != 2 || xb.Total != uint32(MaxDataBlockCB)+1 {
		t.Fatalf("%+v", xb)
	}
	mustNode(t, n, 0x41, xr.BID, 0, 0)
	file, err := n.Commit()
	if err != nil {
		t.Fatal(err)
	}
	n2, err := OpenNDB(file)
	if err != nil {
		t.Fatal(err)
	}
	r, err := n2.OpenDataTree(xr.BID)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != MaxDataBlockCB+1 || got[0] != 0xA5 || got[MaxDataBlockCB] != 0xA5 {
		t.Fatalf("len %d first %x last %x", len(got), got[0], got[len(got)-1])
	}
	if n2.dataTreeRefs[xb.BIDs[0]] != 1 || n2.opaqueRefs[xb.BIDs[0]] != 0 {
		t.Fatalf("leaf extras data=%d opaque=%d", n2.dataTreeRefs[xb.BIDs[0]], n2.opaqueRefs[xb.BIDs[0]])
	}
}

func TestXXBlockTransition(t *testing.T) {
	n := NewNDB(nil)
	need := int64(MaxXBlockEntries+1) * int64(MaxDataBlockCB)
	root, err := n.PutDataTree(io.LimitReader(repeatByte(0x3C), need), need)
	if err != nil {
		t.Fatal(err)
	}
	data, err := n.blockPayload(root)
	if err != nil {
		t.Fatal(err)
	}
	xb, err := InspectXBlock(data)
	if err != nil {
		t.Fatal(err)
	}
	if xb.Level != XXBlockLevel || xb.Count != 2 {
		t.Fatalf("xx %+v", xb)
	}
	mustNode(t, n, 0x21, root.BID, 0, 0)
	file, err := n.Commit()
	if err != nil {
		t.Fatal(err)
	}
	if err := CheckAllocation(file); err != nil {
		t.Fatal(err)
	}
	n2, err := OpenNDB(file)
	if err != nil {
		t.Fatal(err)
	}
	r, err := n2.OpenDataTree(root.BID)
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.New()
	got, err := io.Copy(h, r)
	if err != nil {
		t.Fatal(err)
	}
	if got != need {
		t.Fatalf("read %d want %d", got, need)
	}
	want := sha256.New()
	_, _ = io.Copy(want, io.LimitReader(repeatByte(0x3C), need))
	if !bytes.Equal(h.Sum(nil), want.Sum(nil)) {
		t.Fatal("hash mismatch after reopen")
	}
}

func TestDataTreeHashReopenBounded(t *testing.T) {
	const nBytes = int64(3*MaxDataBlockCB + 100)
	n := NewNDB(nil)
	root, err := n.PutDataTree(io.LimitReader(repeatByte(0x7E), nBytes), nBytes)
	if err != nil {
		t.Fatal(err)
	}
	mustNode(t, n, 0x21, root.BID, 0, 0)
	r1, err := n.OpenDataTree(root.BID)
	if err != nil {
		t.Fatal(err)
	}
	h1 := sha256.New()
	if _, err := io.Copy(h1, r1); err != nil {
		t.Fatal(err)
	}
	file, err := n.Commit()
	if err != nil {
		t.Fatal(err)
	}
	n2, err := OpenNDB(file)
	if err != nil {
		t.Fatal(err)
	}
	r2, err := n2.OpenDataTree(root.BID)
	if err != nil {
		t.Fatal(err)
	}
	h2 := sha256.New()
	if _, err := io.Copy(h2, r2); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(h1.Sum(nil), h2.Sum(nil)) {
		t.Fatal("reopen hash mismatch")
	}
}

func TestPutDataTreeTruncationRollsBack(t *testing.T) {
	n := NewNDB(nil)
	_, err := n.PutDataTree(&boomReader{left: 100}, 0)
	if !errors.Is(err, ErrIO) {
		t.Fatalf("got %v", err)
	}
	if len(n.blocks) != 0 {
		t.Fatalf("leaked %d blocks", len(n.blocks))
	}
}

func TestPutDataTreeLengthMismatchRollsBack(t *testing.T) {
	n := NewNDB(nil)
	_, err := n.PutDataTree(bytes.NewReader([]byte("ab")), 5)
	if !errors.Is(err, ErrInvalidArg) {
		t.Fatalf("got %v", err)
	}
	if len(n.blocks) != 0 {
		t.Fatalf("leaked %d blocks", len(n.blocks))
	}
}

func TestXBlockLcbTotalMismatch(t *testing.T) {
	n := NewNDB(nil)
	a := mustAlloc(t, n, 8)
	b := mustAlloc(t, n, 8)
	if err := n.putPayload(a, bytes.Repeat([]byte{1}, 8)); err != nil {
		t.Fatal(err)
	}
	if err := n.putPayload(b, bytes.Repeat([]byte{2}, 8)); err != nil {
		t.Fatal(err)
	}
	payload, err := EncodeXBlock(XBlockLevel, 99, []uint64{a.BID, b.BID})
	if err != nil {
		t.Fatal(err)
	}
	blk, err := n.AllocInternalBlock(uint16(len(payload)))
	if err != nil {
		t.Fatal(err)
	}
	if err := n.putPayload(blk, payload); err != nil {
		t.Fatal(err)
	}
	if err := n.AddDataTreeRef(a.BID); err != nil {
		t.Fatal(err)
	}
	if err := n.AddDataTreeRef(b.BID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := n.flattenDataTree(blk.BID); !errors.Is(err, ErrInvariant) {
		t.Fatalf("lcbTotal: %v", err)
	}
}
