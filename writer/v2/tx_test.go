package writer

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

type faultSink struct {
	Sink
	failWrite int
	failSync  int
	failTrunc int
	failOff0  int
	writes    int
	syncs     int
	truncs    int
	off0      int
}

func (f *faultSink) WriteAt(p []byte, off int64) (int, error) {
	f.writes++
	if off == 0 {
		f.off0++
		if f.failOff0 > 0 && f.off0 == f.failOff0 {
			return 0, io.ErrClosedPipe
		}
	}
	if f.failWrite > 0 && f.writes == f.failWrite {
		return 0, io.ErrClosedPipe
	}
	return f.Sink.WriteAt(p, off)
}

func (f *faultSink) Sync() error {
	f.syncs++
	if f.failSync > 0 && f.syncs == f.failSync {
		return io.ErrClosedPipe
	}
	return f.Sink.Sync()
}

func (f *faultSink) Truncate(n int64) error {
	f.truncs++
	if f.failTrunc > 0 && f.truncs == f.failTrunc {
		return io.ErrClosedPipe
	}
	if tr, ok := f.Sink.(interface{ Truncate(int64) error }); ok {
		return tr.Truncate(n)
	}
	return nil
}

type ncSink struct {
	*MemSink
	_ [0]func()
}

type failClose struct {
	Sink
}

func (f failClose) Close() error { return io.ErrClosedPipe }

func seedNDB(t *testing.T, payload []byte, withSub bool) (*NDB, BBTEntry, BBTEntry) {
	t.Helper()
	n := NewNDB(nil)
	root, err := n.PutDataTree(bytes.NewReader(payload), int64(len(payload)))
	if err != nil {
		t.Fatal(err)
	}
	var sub BBTEntry
	if withSub {
		inner := mustAlloc(t, n, 8)
		if err := n.putPayload(inner, []byte("innersub")); err != nil {
			t.Fatal(err)
		}
		sub, err = n.PutSubnodeTree([]SLEntry{{NID: 0x21, DataBID: inner.BID}})
		if err != nil {
			t.Fatal(err)
		}
		mustNode(t, n, 0x41, root.BID, sub.BID, 0)
	} else {
		mustNode(t, n, 0x21, root.BID, 0, 0)
	}
	return n, root, sub
}

func TestSnapshotRestoresCompleteCatalog(t *testing.T) {
	n := NewNDB(nil)
	root, err := n.PutDataTree(bytes.NewReader([]byte("abc")), 3)
	if err != nil {
		t.Fatal(err)
	}
	mustNode(t, n, 0x21, root.BID, 0, 0)
	beforeNodes := len(n.nodes)
	beforeBlocks := len(n.blocks)
	beforeBid := n.store.bidNextB
	beforePages := len(n.livePages)
	_, err = n.PutDataTree(&boomReader{left: 2 << 20}, 0)
	if !errors.Is(err, ErrIO) {
		t.Fatalf("got %v", err)
	}
	if len(n.nodes) != beforeNodes || len(n.blocks) != beforeBlocks {
		t.Fatalf("catalog leaked nodes=%d blocks=%d", len(n.nodes), len(n.blocks))
	}
	if n.store.bidNextB != beforeBid || len(n.livePages) != beforePages {
		t.Fatalf("allocator/pages not restored")
	}
	if n.dataTreeRefs[root.BID] != 0 && n.nodes[0x21].DataBID != root.BID {
		t.Fatal("node lost")
	}
	file, err := n.Commit()
	if err != nil {
		t.Fatal(err)
	}
	if err := CheckAllocation(file); err != nil {
		t.Fatal(err)
	}
}

func TestCommitTwoPhaseRejectsInvalidHeader(t *testing.T) {
	n, _, _ := seedNDB(t, []byte("phase"), false)
	ms := NewMemSink("partial")
	fs := &faultSink{Sink: ms, failSync: 1} // after INVALID_AMAP header
	if err := n.CommitTo(fs); err == nil {
		t.Fatal("expected header-transition failure")
	}
	if _, err := InspectHeader(ms.Bytes()[:UnicodeHeaderSize]); err == nil {
		t.Fatal("INVALID_AMAP (or truncated header) accepted")
	}
}

func TestCommitCrashKeepsPriorFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "keep.pst")
	n, _, _ := seedNDB(t, []byte("prior"), false)
	if err := n.CommitFile(path); err != nil {
		t.Fatal(err)
	}
	_ = n.Close()
	prior, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	n2, err := OpenNDBFile(path)
	if err != nil {
		t.Fatal(err)
	}
	extra, err := n2.PutDataTree(bytes.NewReader(bytes.Repeat([]byte{0x22}, 200)), 200)
	if err != nil {
		t.Fatal(err)
	}
	mustNode(t, n2, 0x61, extra.BID, 0, 0)
	old := replacePath
	replacePath = func(string, string) error { return io.ErrClosedPipe }
	defer func() { replacePath = old }()
	if err := n2.CommitFile(path); err == nil {
		t.Fatal("expected rename failure")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, prior) {
		t.Fatal("rename failure mutated the prior file")
	}
	n3, err := OpenNDBFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer n3.Close()
	if _, ok := n3.LookupNode(0x61); ok {
		t.Fatal("uncommitted node visible in prior file")
	}
}

func TestCommitFileRenameSuccess(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ok.pst")
	n, root, _ := seedNDB(t, []byte("renamed"), false)
	if err := n.CommitFile(path); err != nil {
		t.Fatal(err)
	}
	n2, err := OpenNDBFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer n2.Close()
	got, ok := n2.LookupNode(0x21)
	if !ok || got.DataBID != root.BID {
		t.Fatalf("reopen %+v ok=%v", got, ok)
	}
}

func TestCommitCleanupFailureKeepsNewSource(t *testing.T) {
	n, _, _ := seedNDB(t, []byte("cleanup"), false)
	if err := n.store.ensureSpool(); err != nil {
		t.Fatal(err)
	}
	n.store.setWriter(failClose{Sink: n.store.writer()})
	dst := NewMemSink("new")
	err := n.CommitTo(dst)
	if !errors.Is(err, ErrCleanup) {
		t.Fatalf("got %v", err)
	}
	if n.store.io == nil || !sameIO(n.store.io.w, dst) {
		t.Fatal("NDB lost the successfully written destination")
	}
	rd, err := n.OpenDataTree(n.nodes[0x21].DataBID)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(rd)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "cleanup" {
		t.Fatalf("got %q", got)
	}
}

func TestNonComparableSinkCommit(t *testing.T) {
	n, _, _ := seedNDB(t, []byte("nc"), false)
	dst := &ncSink{MemSink: NewMemSink("nc")}
	if err := n.CommitTo(dst); err != nil {
		t.Fatal(err)
	}
	n2, err := OpenNDBFrom(onlyReaderAt{bytes.NewReader(dst.Bytes())}, int64(len(dst.Bytes())))
	if err != nil {
		t.Fatal(err)
	}
	defer n2.Close()
	if n2.Store().writer() != nil {
		t.Fatalf("ReaderAt-only adopted writer %T", n2.Store().writer())
	}
}

func TestReaderAtOnlyMutateCommitReopen(t *testing.T) {
	cases := []struct {
		name string
		n    int
	}{
		{"direct", 200},
		{"xblock", MaxDataBlockCB + 100},
		{"xxblock", MaxXBlockEntries + 1}, // tiny synthetic via repeated 1-byte? no: use real size
	}
	// xxblock uses full leaves; keep it last and smaller-named run uses PutDataTree size.
	cases[2].n = int((MaxXBlockEntries + 1) * MaxDataBlockCB)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			n := NewNDB(nil)
			root, err := n.PutDataTree(io.LimitReader(repeatByte(0x5A), int64(tc.n)), int64(tc.n))
			if err != nil {
				t.Fatal(err)
			}
			inner := mustAlloc(t, n, 8)
			if err := n.putPayload(inner, []byte("innersub")); err != nil {
				t.Fatal(err)
			}
			sub, err := n.PutSubnodeTree([]SLEntry{{NID: 0x21, DataBID: inner.BID}})
			if err != nil {
				t.Fatal(err)
			}
			mustNode(t, n, 0x41, root.BID, sub.BID, 0)
			path := filepath.Join(t.TempDir(), tc.name+".pst")
			if err := n.CommitFile(path); err != nil {
				t.Fatal(err)
			}
			_ = n.Close()
			f, err := os.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			st, err := f.Stat()
			if err != nil {
				t.Fatal(err)
			}
			n2, err := OpenNDBFrom(onlyReaderAt{f}, st.Size())
			if err != nil {
				t.Fatal(err)
			}
			if n2.Store().writer() != nil {
				t.Fatalf("adopted writer %T", n2.Store().writer())
			}
			added, err := n2.PutDataTree(bytes.NewReader([]byte("more")), 4)
			if err != nil {
				t.Fatal(err)
			}
			mustNode(t, n2, 0x61, added.BID, 0, 0)
			out := filepath.Join(t.TempDir(), tc.name+"-mut.pst")
			dst, err := CreateFileSink(out)
			if err != nil {
				t.Fatal(err)
			}
			if err := n2.CommitTo(dst); err != nil {
				t.Fatal(err)
			}
			_ = n2.Close()
			_ = dst.Close()
			_ = f.Close()
			n3, err := OpenNDBFile(out)
			if err != nil {
				t.Fatal(err)
			}
			defer n3.Close()
			got, ok := n3.LookupNode(0x41)
			if !ok || got.DataBID != root.BID || got.SubBID != sub.BID {
				t.Fatalf("original node %+v ok=%v", got, ok)
			}
			if _, ok := n3.LookupNode(0x61); !ok {
				t.Fatal("mutated node missing")
			}
			rd, err := n3.OpenDataTree(root.BID)
			if err != nil {
				t.Fatal(err)
			}
			h := sha256.New()
			gotN, err := io.Copy(h, rd)
			if err != nil {
				t.Fatal(err)
			}
			if gotN != int64(tc.n) {
				t.Fatalf("read %d want %d", gotN, tc.n)
			}
			want := sha256.New()
			_, _ = io.Copy(want, io.LimitReader(repeatByte(0x5A), int64(tc.n)))
			if !bytes.Equal(h.Sum(nil), want.Sum(nil)) {
				t.Fatal("hash mismatch")
			}
		})
	}
}

func TestFaultWriteDoesNotAdoptDestination(t *testing.T) {
	n, _, _ := seedNDB(t, []byte("src"), false)
	before := n.capture()
	ms := NewMemSink("dst")
	fs := &faultSink{Sink: ms, failWrite: 1}
	if err := n.CommitTo(fs); err == nil {
		t.Fatal("expected write fault")
	}
	if n.store.bidNextB != before.bidNextB || len(n.nodes) != len(before.nodes) {
		t.Fatal("in-memory state not restored after write fault")
	}
	if sameIO(n.store.writer(), fs) {
		t.Fatal("adopted a failed destination")
	}
}

func TestFailedTransactionDoesNotLeakIntoNextCommit(t *testing.T) {
	n := NewNDB(nil)
	root, err := n.PutDataTree(bytes.NewReader([]byte("keep")), 4)
	if err != nil {
		t.Fatal(err)
	}
	mustNode(t, n, 0x21, root.BID, 0, 0)
	_, err = n.PutDataTree(&boomReader{left: 2 << 20}, 0)
	if err == nil {
		t.Fatal("expected boom")
	}
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
	defer n2.Close()
	if len(n2.blocks) != 1 {
		t.Fatalf("leaked %d blocks into committed file", len(n2.blocks))
	}
}

func TestOwnedFileClosedBorrowedLeftOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "owned.pst")
	n, _, _ := seedNDB(t, []byte("life"), false)
	if err := n.CommitFile(path); err != nil {
		t.Fatal(err)
	}
	_ = n.Close()
	n2, err := OpenNDBFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if n2.store.io == nil || n2.store.io.life != lifeClose {
		t.Fatalf("owned life %v", n2.store.io)
	}
	if err := n2.Close(); err != nil {
		t.Fatal(err)
	}
	ms := NewMemSink("borrow")
	n3, _, _ := seedNDB(t, []byte("b"), false)
	if err := n3.CommitTo(ms); err != nil {
		t.Fatal(err)
	}
	if err := n3.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := ms.WriteAt([]byte{0}, 0); err != nil {
		t.Fatalf("Close closed borrowed sink: %v", err)
	}
}
