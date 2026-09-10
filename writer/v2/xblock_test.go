package writer

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
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

func payloadBytes(n *NDB) int {
	var s int
	for _, p := range n.payloads {
		s += len(p)
	}
	return s
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
	if held := payloadBytes(n); held != 0 {
		t.Fatalf("XXBLOCK PutDataTree retained %d payload bytes", held)
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
	const nBytes = int64(16 << 20)
	n := NewNDB(nil)
	root, err := n.PutDataTree(io.LimitReader(repeatByte(0x7E), nBytes), nBytes)
	if err != nil {
		t.Fatal(err)
	}
	if held := payloadBytes(n); held != 0 {
		t.Fatalf("PutDataTree retained %d payload bytes", held)
	}
	if n.Store().residentImageBytes() != 0 {
		t.Fatalf("resident image %d; want FileSink spool only", n.Store().residentImageBytes())
	}
	if _, ok := n.Store().spool.(*FileSink); !ok {
		t.Fatalf("spool %T, want FileSink", n.Store().spool)
	}
	mustNode(t, n, 0x21, root.BID, 0, 0)
	path := filepath.Join(t.TempDir(), "big.pst")
	dst, err := CreateFileSink(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := n.CommitTo(dst); err != nil {
		t.Fatal(err)
	}
	if n.Store().residentImageBytes() != 0 {
		t.Fatalf("resident image after CommitTo %d", n.Store().residentImageBytes())
	}
	if err := n.Close(); err != nil {
		t.Fatal(err)
	}
	n2, err := OpenNDBFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer n2.Close()
	if n2.Store().residentImageBytes() != 0 {
		t.Fatalf("reopen resident image %d", n2.Store().residentImageBytes())
	}
	got, ok := n2.LookupNode(0x21)
	if !ok || got.DataBID != root.BID {
		t.Fatalf("reopen node %+v ok=%v", got, ok)
	}
	rd, err := n2.OpenDataTree(root.BID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rd.leaves)+len(rd.xbs) > MaxXBlockEntries+1 {
		t.Fatalf("reader materialized %d+%d BIDs", len(rd.leaves), len(rd.xbs))
	}
	h1 := sha256.New()
	nread, err := io.Copy(h1, rd)
	if err != nil {
		t.Fatal(err)
	}
	if nread != nBytes {
		t.Fatalf("read %d want %d", nread, nBytes)
	}
	want := sha256.New()
	if _, err := io.Copy(want, io.LimitReader(repeatByte(0x7E), nBytes)); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(h1.Sum(nil), want.Sum(nil)) {
		t.Fatal("commit/reopen hash mismatch")
	}
	if n2.Store().residentImageBytes() != 0 {
		t.Fatalf("resident image after readback %d", n2.Store().residentImageBytes())
	}
}

func TestRollbackErrSurfacesCleanup(t *testing.T) {
	op := ioErr("reader", "boom")
	rb := invariant(SectionAMap, "ib", "cleanup failed")
	err := rollbackErr(op, rb)
	if !errors.Is(err, ErrIO) {
		t.Fatalf("got %v", err)
	}
	if !strings.Contains(err.Error(), "rollback") {
		t.Fatalf("cleanup not visible: %v", err)
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

func TestXXBlockMalformedChildCycleDuplicate(t *testing.T) {
	t.Run("xx-child-is-xx", func(t *testing.T) {
		n := NewNDB(nil)
		leaf := mustAlloc(t, n, 8)
		if err := n.putPayload(leaf, bytes.Repeat([]byte{1}, 8)); err != nil {
			t.Fatal(err)
		}
		xp, err := EncodeXBlock(XBlockLevel, 8, []uint64{leaf.BID})
		if err != nil {
			t.Fatal(err)
		}
		xb, err := n.AllocInternalBlock(uint16(len(xp)))
		if err != nil {
			t.Fatal(err)
		}
		if err := n.putPayload(xb, xp); err != nil {
			t.Fatal(err)
		}
		if err := n.AddDataTreeRef(leaf.BID); err != nil {
			t.Fatal(err)
		}
		innerp, err := EncodeXBlock(XXBlockLevel, 8, []uint64{xb.BID})
		if err != nil {
			t.Fatal(err)
		}
		inner, err := n.AllocInternalBlock(uint16(len(innerp)))
		if err != nil {
			t.Fatal(err)
		}
		if err := n.putPayload(inner, innerp); err != nil {
			t.Fatal(err)
		}
		if err := n.AddDataTreeRef(xb.BID); err != nil {
			t.Fatal(err)
		}
		outerp, err := EncodeXBlock(XXBlockLevel, 8, []uint64{inner.BID})
		if err != nil {
			t.Fatal(err)
		}
		outer, err := n.AllocInternalBlock(uint16(len(outerp)))
		if err != nil {
			t.Fatal(err)
		}
		if err := n.putPayload(outer, outerp); err != nil {
			t.Fatal(err)
		}
		if err := n.AddDataTreeRef(inner.BID); err != nil {
			t.Fatal(err)
		}
		if _, _, err := n.flattenDataTree(outer.BID); !errors.Is(err, ErrInvariant) {
			t.Fatalf("xx->xx: %v", err)
		}
		if err := n.dropBlock(outer.BID); err != nil {
			t.Fatal(err)
		}
		if _, ok := n.LookupBlock(inner.BID); ok {
			t.Fatal("inner XX leaked after drop")
		}
		if _, ok := n.LookupBlock(xb.BID); ok {
			t.Fatal("XBLOCK leaked after drop")
		}
		if _, ok := n.LookupBlock(leaf.BID); ok {
			t.Fatal("leaf leaked after drop")
		}
	})
	t.Run("self-cycle", func(t *testing.T) {
		n := NewNDB(nil)
		xx, err := n.AllocInternalBlock(8 + 8)
		if err != nil {
			t.Fatal(err)
		}
		payload, err := EncodeXBlock(XXBlockLevel, 1, []uint64{xx.BID})
		if err != nil {
			t.Fatal(err)
		}
		if uint16(len(payload)) != xx.CB {
			xx2, err := n.AllocInternalBlock(uint16(len(payload)))
			if err != nil {
				t.Fatal(err)
			}
			_ = n.dropBlock(xx.BID)
			xx = xx2
			payload, err = EncodeXBlock(XXBlockLevel, 1, []uint64{xx.BID})
			if err != nil {
				t.Fatal(err)
			}
		}
		if err := n.putPayload(xx, payload); err != nil {
			t.Fatal(err)
		}
		if _, _, err := n.flattenDataTree(xx.BID); !errors.Is(err, ErrInvariant) {
			t.Fatalf("cycle: %v", err)
		}
		if err := n.dropBlock(xx.BID); err != nil {
			t.Fatal(err)
		}
		if _, ok := n.LookupBlock(xx.BID); ok {
			t.Fatal("self-cycle block leaked")
		}
	})
	t.Run("duplicate-leaf", func(t *testing.T) {
		n := NewNDB(nil)
		leaf := mustAlloc(t, n, 8)
		if err := n.putPayload(leaf, bytes.Repeat([]byte{9}, 8)); err != nil {
			t.Fatal(err)
		}
		xp, err := EncodeXBlock(XBlockLevel, 8, []uint64{leaf.BID})
		if err != nil {
			t.Fatal(err)
		}
		xb1, err := n.AllocInternalBlock(uint16(len(xp)))
		if err != nil {
			t.Fatal(err)
		}
		if err := n.putPayload(xb1, xp); err != nil {
			t.Fatal(err)
		}
		xb2, err := n.AllocInternalBlock(uint16(len(xp)))
		if err != nil {
			t.Fatal(err)
		}
		if err := n.putPayload(xb2, xp); err != nil {
			t.Fatal(err)
		}
		if err := n.AddDataTreeRef(leaf.BID); err != nil {
			t.Fatal(err)
		}
		if err := n.AddDataTreeRef(leaf.BID); err != nil {
			t.Fatal(err)
		}
		xxp, err := EncodeXBlock(XXBlockLevel, 16, []uint64{xb1.BID, xb2.BID})
		if err != nil {
			t.Fatal(err)
		}
		xx, err := n.AllocInternalBlock(uint16(len(xxp)))
		if err != nil {
			t.Fatal(err)
		}
		if err := n.putPayload(xx, xxp); err != nil {
			t.Fatal(err)
		}
		if err := n.AddDataTreeRef(xb1.BID); err != nil {
			t.Fatal(err)
		}
		if err := n.AddDataTreeRef(xb2.BID); err != nil {
			t.Fatal(err)
		}
		if _, _, err := n.flattenDataTree(xx.BID); !errors.Is(err, ErrInvariant) {
			t.Fatalf("dup leaf: %v", err)
		}
		if err := n.dropBlock(xx.BID); err != nil {
			t.Fatal(err)
		}
		if _, ok := n.LookupBlock(xb1.BID); ok {
			t.Fatal("xb1 leaked")
		}
		if _, ok := n.LookupBlock(xb2.BID); ok {
			t.Fatal("xb2 leaked")
		}
		if _, ok := n.LookupBlock(leaf.BID); ok {
			t.Fatal("shared leaf leaked")
		}
	})
}

func TestPutDataTreeFailureShrinksGeometry(t *testing.T) {
	n := NewNDB(nil)
	beforeR := n.Store().RegionCount()
	beforeEOF := n.Store().FileEOF()
	beforeBid := n.Store().bidNextB
	beforeIDs := n.ids.nextBlock
	_, err := n.PutDataTree(&boomReader{left: 2 << 20}, 0)
	if !errors.Is(err, ErrIO) {
		t.Fatalf("got %v", err)
	}
	if len(n.blocks) != 0 {
		t.Fatalf("leaked %d blocks", len(n.blocks))
	}
	if n.Store().RegionCount() != beforeR {
		t.Fatalf("regions %d want %d", n.Store().RegionCount(), beforeR)
	}
	if n.Store().FileEOF() != beforeEOF {
		t.Fatalf("eof 0x%x want 0x%x", n.Store().FileEOF(), beforeEOF)
	}
	if n.Store().bidNextB != beforeBid {
		t.Fatalf("bidNextB 0x%x want 0x%x", n.Store().bidNextB, beforeBid)
	}
	if n.ids.nextBlock != beforeIDs {
		t.Fatalf("ids.nextBlock 0x%x want 0x%x", n.ids.nextBlock, beforeIDs)
	}
	var spoolName string
	if n.Store().spool != nil {
		spoolName = n.Store().spool.Name()
	}
	if err := n.Close(); err != nil {
		t.Fatal(err)
	}
	if spoolName != "" {
		if _, statErr := os.Stat(spoolName); !os.IsNotExist(statErr) {
			t.Fatalf("leaked spool %s: %v", spoolName, statErr)
		}
	}
}

type onlyReaderAt struct{ r io.ReaderAt }

func (o onlyReaderAt) ReadAt(p []byte, off int64) (int, error) { return o.r.ReadAt(p, off) }

type failTruncate struct {
	Sink
}

func (f failTruncate) Truncate(int64) error { return io.ErrClosedPipe }

func TestOpenNDBFromReaderAtOnly(t *testing.T) {
	n := NewNDB(nil)
	payload := bytes.Repeat([]byte{0x5A}, 200)
	root, err := n.PutDataTree(bytes.NewReader(payload), int64(len(payload)))
	if err != nil {
		t.Fatal(err)
	}
	mustNode(t, n, 0x21, root.BID, 0, 0)
	file, err := n.Commit()
	if err != nil {
		t.Fatal(err)
	}
	if err := n.Close(); err != nil {
		t.Fatal(err)
	}
	n2, err := OpenNDBFrom(onlyReaderAt{bytes.NewReader(file)}, int64(len(file)))
	if err != nil {
		t.Fatal(err)
	}
	defer n2.Close()
	if n2.Store().spool != nil {
		t.Fatalf("ReaderAt-only open adopted spool %T", n2.Store().spool)
	}
	rd, err := n2.OpenDataTree(root.BID)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(rd)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("got %d bytes", len(got))
	}
}

func TestCommitToDoesNotCloseBorrowedSink(t *testing.T) {
	n := NewNDB(nil)
	root, err := n.PutDataTree(bytes.NewReader([]byte("abc")), 3)
	if err != nil {
		t.Fatal(err)
	}
	mustNode(t, n, 0x21, root.BID, 0, 0)
	path := filepath.Join(t.TempDir(), "borrow.pst")
	dst, err := CreateFileSink(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := n.CommitTo(dst); err != nil {
		t.Fatal(err)
	}
	if err := n.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := dst.WriteAt([]byte{0}, 0); err != nil {
		t.Fatalf("Close closed borrowed dest: %v", err)
	}
	_ = dst.Close()
}

func TestRollbackCleanupFailureIsVisible(t *testing.T) {
	n := NewNDB(nil)
	if err := n.Store().ensureSpool(); err != nil {
		t.Fatal(err)
	}
	n.Store().spool = failTruncate{Sink: n.Store().spool}
	n.Store().src = n.Store().spool
	_, err := n.PutDataTree(&boomReader{left: 2 << 20}, 0)
	if !errors.Is(err, ErrIO) {
		t.Fatalf("got %v", err)
	}
	if !strings.Contains(err.Error(), "rollback") {
		t.Fatalf("cleanup not visible: %v", err)
	}
}
