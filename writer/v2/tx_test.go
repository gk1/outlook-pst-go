package writer

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
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

func (s ncSink) UnwrapSink() Sink { return s.MemSink }

type failClose struct {
	Sink
}

func (f failClose) Close() error { return io.ErrClosedPipe }

func (f failClose) UnwrapSink() Sink { return f.Sink }

func (f *faultSink) UnwrapSink() Sink { return f.Sink }

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

func destHeader(t *testing.T, ms *MemSink) error {
	t.Helper()
	b := ms.Bytes()
	if len(b) < UnicodeHeaderSize {
		return io.ErrUnexpectedEOF
	}
	_, err := InspectHeader(b[:UnicodeHeaderSize])
	return err
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

func TestSnapshotCoversMutableFields(t *testing.T) {
	snap := reflect.TypeOf(snapshot{})
	names := make(map[string]bool, snap.NumField())
	for i := 0; i < snap.NumField(); i++ {
		names[snap.Field(i).Name] = true
	}
	skipStore := map[string]bool{"io": true, "work": true, "undo": true}
	st := reflect.TypeOf(Store{})
	for i := 0; i < st.NumField(); i++ {
		f := st.Field(i)
		if skipStore[f.Name] {
			continue
		}
		if !names[f.Name] {
			t.Errorf("Store.%s missing from snapshot", f.Name)
		}
	}
	skipNDB := map[string]bool{"store": true, "pendingPath": true}
	nd := reflect.TypeOf(NDB{})
	for i := 0; i < nd.NumField(); i++ {
		f := nd.Field(i)
		if skipNDB[f.Name] {
			continue
		}
		if !names[f.Name] {
			t.Errorf("NDB.%s missing from snapshot", f.Name)
		}
	}
}

func TestRestoreRevertsRetainedExtentMutation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "extent.pst")
	n, root, _ := seedNDB(t, []byte("keep-bytes"), false)
	if err := n.CommitFile(path); err != nil {
		t.Fatal(err)
	}
	_ = n.Close()
	n2, err := OpenNDBFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer n2.Close()
	e, ok := n2.LookupBlock(root.BID)
	if !ok {
		t.Fatal("missing block")
	}
	size := int(BlockDiskSize(uint64(e.CB)))
	orig, err := n2.store.readExtent(e.IB, size)
	if err != nil {
		t.Fatal(err)
	}
	eof := n2.store.FileEOF()
	if err := n2.store.ensureSpool(); err != nil {
		t.Fatal(err)
	}
	snap, err := n2.capture()
	if err != nil {
		t.Fatal(err)
	}
	defer snap.release()
	if !snap.hadWork {
		t.Fatal("expected work spool")
	}
	n2.store.beginUndo()
	defer n2.store.endUndo()
	z := bytes.Repeat([]byte{0xA5}, size)
	if err := n2.store.writeExtent(e.IB, z); err != nil {
		t.Fatal(err)
	}
	if n2.store.undo == nil || len(n2.store.undo.extents) == 0 {
		t.Fatal("expected bounded undo extents, not a full-image copy")
	}
	mut, err := n2.store.readExtent(e.IB, size)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(mut, orig) {
		t.Fatal("mutation did not land on work spool")
	}
	if err := n2.store.Grow(); err != nil {
		t.Fatal(err)
	}
	if n2.store.FileEOF() <= eof {
		t.Fatal("grow did not extend EOF")
	}
	if err := n2.restore(snap); err != nil {
		t.Fatal(err)
	}
	if n2.store.FileEOF() != eof {
		t.Fatalf("EOF %d want %d", n2.store.FileEOF(), eof)
	}
	got, err := n2.store.readExtent(e.IB, size)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, orig) {
		t.Fatal("retained extent not restored from undo journal")
	}
}

func TestCommitToRejectsInPlaceBeforeWrite(t *testing.T) {
	n, _, _ := seedNDB(t, []byte("first"), false)
	ms := NewMemSink("same")
	fs := &faultSink{Sink: ms}
	if err := n.CommitTo(fs); err != nil {
		t.Fatal(err)
	}
	writes := fs.writes
	off0 := fs.off0
	extra, err := n.PutDataTree(bytes.NewReader([]byte("more")), 4)
	if err != nil {
		t.Fatal(err)
	}
	mustNode(t, n, 0x61, extra.BID, 0, 0)
	if err := n.CommitTo(fs); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("in-place CommitTo: %v", err)
	}
	if fs.writes != writes || fs.off0 != off0 {
		t.Fatalf("in-place CommitTo mutated dest writes=%d->%d off0=%d->%d", writes, fs.writes, off0, fs.off0)
	}
}

func TestFailedTreeKeepsPriorUncommittedWork(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keep-work.pst")
	n, orig, _ := seedNDB(t, []byte("orig-payload"), false)
	if err := n.CommitFile(path); err != nil {
		t.Fatal(err)
	}
	_ = n.Close()
	n2, err := OpenNDBFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer n2.Close()
	keep, err := n2.PutDataTree(bytes.NewReader([]byte("keep2")), 5)
	if err != nil {
		t.Fatal(err)
	}
	mustNode(t, n2, 0x61, keep.BID, 0, 0)
	_, err = n2.PutDataTree(&boomReader{left: 2 << 20}, 0)
	if !errors.Is(err, ErrIO) {
		t.Fatalf("got %v", err)
	}
	got, ok := n2.LookupNode(0x61)
	if !ok || got.DataBID != keep.BID {
		t.Fatalf("uncommitted node lost: %+v ok=%v", got, ok)
	}
	rd, err := n2.OpenDataTree(keep.BID)
	if err != nil {
		t.Fatal(err)
	}
	gotb, err := io.ReadAll(rd)
	if err != nil {
		t.Fatal(err)
	}
	if string(gotb) != "keep2" {
		t.Fatalf("work payload %q", gotb)
	}
	out := filepath.Join(t.TempDir(), "keep-work-out.pst")
	if err := n2.CommitFile(out); err != nil {
		t.Fatal(err)
	}
	n3, err := OpenNDBFile(out)
	if err != nil {
		t.Fatal(err)
	}
	defer n3.Close()
	if _, ok := n3.LookupNode(0x21); !ok {
		t.Fatal("original node missing")
	}
	got, ok = n3.LookupNode(0x61)
	if !ok || got.DataBID != keep.BID {
		t.Fatalf("keep2 missing after commit %+v", got)
	}
	rd, err = n3.OpenDataTree(orig.BID)
	if err != nil {
		t.Fatal(err)
	}
	gotb, err = io.ReadAll(rd)
	if err != nil {
		t.Fatal(err)
	}
	if string(gotb) != "orig-payload" {
		t.Fatalf("original payload %q", gotb)
	}
}

func TestCommitToRejectsWorkSpool(t *testing.T) {
	n, _, _ := seedNDB(t, []byte("work"), false)
	w := n.store.writer()
	if w == nil {
		t.Fatal("expected work spool")
	}
	if err := n.CommitTo(w); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("work-spool CommitTo: %v", err)
	}
}

func TestCommitTwoPhaseRejectsInvalidHeader(t *testing.T) {
	n, _, _ := seedNDB(t, []byte("phase"), false)
	ms := NewMemSink("partial")
	fs := &faultSink{Sink: ms, failSync: 1} // after INVALID_AMAP header
	if err := n.CommitTo(fs); err == nil {
		t.Fatal("expected header-transition failure")
	}
	if err := destHeader(t, ms); err == nil {
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

func TestCommitCleanupRemoveFailureKeepsNewSource(t *testing.T) {
	n, _, _ := seedNDB(t, []byte("rm"), false)
	if err := n.store.ensureSpool(); err != nil {
		t.Fatal(err)
	}
	old := removeFile
	removeFile = func(string) error { return io.ErrClosedPipe }
	defer func() { removeFile = old }()
	dst := NewMemSink("new")
	err := n.CommitTo(dst)
	if !errors.Is(err, ErrCleanup) {
		t.Fatalf("got %v", err)
	}
	if n.store.io == nil || !sameIO(n.store.io.w, dst) {
		t.Fatal("NDB lost the successfully written destination")
	}
}

func TestNonComparableSinkCommit(t *testing.T) {
	n, _, _ := seedNDB(t, []byte("nc"), false)
	dst := ncSink{MemSink: NewMemSink("nc")}
	if reflect.TypeOf(dst).Comparable() {
		t.Fatal("ncSink dynamic type must not be comparable")
	}
	if err := n.CommitTo(dst); err != nil {
		t.Fatal(err)
	}
	if err := n.CommitTo(dst); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("second ncSink CommitTo: %v", err)
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
		{"xxblock", int((MaxXBlockEntries + 1) * MaxDataBlockCB)},
	}
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
	before, err := n.capture()
	if err != nil {
		t.Fatal(err)
	}
	defer before.release()
	ms := NewMemSink("dst")
	fs := &faultSink{Sink: ms, failWrite: 1}
	if err := n.CommitTo(fs); err == nil {
		t.Fatal("expected write fault")
	}
	if n.store.bidNextB != before.bidNextB || len(n.nodes) != len(before.nodes) {
		t.Fatal("in-memory state not restored after write fault")
	}
	if sameIO(n.store.writer(), fs) || (n.store.io != nil && sameIO(n.store.io.w, fs)) {
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

func TestCommitToInjectedBoundaries(t *testing.T) {
	cases := []struct {
		name      string
		failWrite int
		failOff0  int
		failSync  int
		failTrunc int
		accept    bool
	}{
		{name: "invalid-header", failOff0: 1},
		{name: "sync-invalid", failSync: 1},
		{name: "body-write", failWrite: 2},
		{name: "truncate", failTrunc: 1},
		{name: "valid-header", failOff0: 2},
		{name: "sync-body", failSync: 2},
		{name: "sync-valid", failSync: 3, accept: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			n, _, _ := seedNDB(t, []byte("inj"), false)
			ms := NewMemSink(tc.name)
			fs := &faultSink{
				Sink:      ms,
				failWrite: tc.failWrite,
				failOff0:  tc.failOff0,
				failSync:  tc.failSync,
				failTrunc: tc.failTrunc,
			}
			err := n.CommitTo(fs)
			if err == nil {
				t.Fatal("expected injected failure")
			}
			if n.store.io != nil && sameIO(n.store.io.w, fs) {
				t.Fatal("adopted failed dest")
			}
			herr := destHeader(t, ms)
			if tc.accept {
				if herr != nil {
					t.Fatalf("dest should be new-valid after %s: %v", tc.name, herr)
				}
				return
			}
			if herr == nil {
				t.Fatal("mixed/VALID dest accepted after injected failure")
			}
			if tc.failOff0 == 0 && tc.failTrunc == 0 && tc.failWrite == 0 && tc.failSync == 0 {
				t.Fatal("no fault configured")
			}
			if tc.failOff0 > 0 && fs.off0 < tc.failOff0 && tc.failWrite == 0 {
				t.Fatalf("failOff0 unused: off0=%d", fs.off0)
			}
			if tc.failTrunc > 0 && fs.truncs < tc.failTrunc {
				t.Fatalf("failTrunc unused: truncs=%d", fs.truncs)
			}
		})
	}
}

func TestCommitFileInjectedBoundaries(t *testing.T) {
	type kind int
	const (
		kClose kind = iota
		kRename
		kDirSync
		kReopen
	)
	cases := []struct {
		name    string
		kind    kind
		prior   bool
		adopt   bool
		keepOld bool
	}{
		{name: "close", kind: kClose, prior: true, keepOld: true},
		{name: "rename", kind: kRename, prior: true, keepOld: true},
		{name: "dirsync", kind: kDirSync, prior: true, adopt: true},
		{name: "reopen", kind: kReopen, prior: true, adopt: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), tc.name+".pst")
			n, _, _ := seedNDB(t, []byte("old"), false)
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
			defer n2.Close()
			extra, err := n2.PutDataTree(bytes.NewReader([]byte("newx")), 4)
			if err != nil {
				t.Fatal(err)
			}
			mustNode(t, n2, 0x61, extra.BID, 0, 0)

			switch tc.kind {
			case kClose:
				old := closeSink
				closeSink = func(Sink) error { return io.ErrClosedPipe }
				defer func() { closeSink = old }()
			case kRename:
				old := replacePath
				replacePath = func(string, string) error { return io.ErrClosedPipe }
				defer func() { replacePath = old }()
			case kDirSync:
				old := syncDir
				syncDir = func(string) error { return io.ErrClosedPipe }
				defer func() { syncDir = old }()
			case kReopen:
				old := openCommitted
				openCommitted = func(string) (Sink, error) { return nil, io.ErrClosedPipe }
				defer func() { openCommitted = old }()
			}

			commitErr := n2.CommitFile(path)
			if commitErr == nil {
				t.Fatal("expected injected failure")
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if tc.keepOld {
				if !bytes.Equal(got, prior) {
					t.Fatal("prior file mutated")
				}
				n3, err := OpenNDBFile(path)
				if err != nil {
					t.Fatal(err)
				}
				defer n3.Close()
				if _, ok := n3.LookupNode(0x61); ok {
					t.Fatal("uncommitted node in prior file")
				}
				return
			}
			if !tc.adopt {
				return
			}
			if !errors.Is(commitErr, ErrAdopt) {
				t.Fatalf("got %v want ErrAdopt", commitErr)
			}
			if n2.PendingPath() != path {
				t.Fatalf("PendingPath %q", n2.PendingPath())
			}
			if n2.store.io == nil {
				t.Fatal("cleared store.io after adopt failure")
			}
			if n2.store.work == nil || n2.store.work.r == nil {
				t.Fatal("lost work spool after adopt failure")
			}
			n3, err := OpenNDBFile(path)
			if err != nil {
				t.Fatal(err)
			}
			defer n3.Close()
			if _, ok := n3.LookupNode(0x61); !ok {
				t.Fatal("committed file missing new node")
			}
			rd, err := n2.OpenDataTree(extra.BID)
			if err != nil {
				t.Fatal(err)
			}
			gotb, err := io.ReadAll(rd)
			if err != nil {
				t.Fatal(err)
			}
			if string(gotb) != "newx" {
				t.Fatalf("work spool unreadable: %q", gotb)
			}
		})
	}
}

func TestTreeTxnDoesNotCopyFullImage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "no-copy.pst")
	n, _, _ := seedNDB(t, []byte("orig-payload"), false)
	if err := n.CommitFile(path); err != nil {
		t.Fatal(err)
	}
	_ = n.Close()
	n2, err := OpenNDBFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer n2.Close()
	keep, err := n2.PutDataTree(bytes.NewReader([]byte("keep2")), 5)
	if err != nil {
		t.Fatal(err)
	}
	mustNode(t, n2, 0x61, keep.BID, 0, 0)
	calls := 0
	old := createTemp
	createTemp = func(dir, pattern string) (*os.File, error) {
		calls++
		return old(dir, pattern)
	}
	defer func() { createTemp = old }()
	_, err = n2.PutDataTree(&boomReader{left: 2 << 20}, 0)
	if !errors.Is(err, ErrIO) {
		t.Fatalf("got %v", err)
	}
	if calls != 0 {
		t.Fatalf("tree txn created %d full-image temps", calls)
	}
	got, ok := n2.LookupNode(0x61)
	if !ok || got.DataBID != keep.BID {
		t.Fatalf("uncommitted node lost: %+v", got)
	}
}

func TestCommitToInjectsEveryWriteAndSync(t *testing.T) {
	n, _, _ := seedNDB(t, []byte("count"), false)
	ms := NewMemSink("count")
	fs := &faultSink{Sink: ms}
	if err := n.CommitTo(fs); err != nil {
		t.Fatal(err)
	}
	writes, syncs, truncs := fs.writes, fs.syncs, fs.truncs
	if writes < 3 || syncs < 3 {
		t.Fatalf("too few commit ops writes=%d syncs=%d", writes, syncs)
	}
	check := func(t *testing.T, failWrite, failSync, failTrunc int) {
		t.Helper()
		n, _, _ := seedNDB(t, []byte("inj"), false)
		ms := NewMemSink("every")
		fs := &faultSink{Sink: ms, failWrite: failWrite, failSync: failSync, failTrunc: failTrunc}
		err := n.CommitTo(fs)
		if err == nil {
			t.Fatal("expected injected failure")
		}
		if n.store.io != nil && sameIO(n.store.io.w, fs) {
			t.Fatal("adopted failed dest")
		}
		herr := destHeader(t, ms)
		if herr == nil {
			if err := CheckAllocation(ms.Bytes()); err != nil {
				t.Fatalf("VALID mixed dest: %v", err)
			}
		}
	}
	for i := 1; i <= writes; i++ {
		i := i
		t.Run("write", func(t *testing.T) { check(t, i, 0, 0) })
	}
	for i := 1; i <= syncs; i++ {
		i := i
		t.Run("sync", func(t *testing.T) { check(t, 0, i, 0) })
	}
	for i := 1; i <= truncs; i++ {
		i := i
		t.Run("trunc", func(t *testing.T) { check(t, 0, 0, i) })
	}
}

func TestSpoolCreateAndSeedInjected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "seed.pst")
	n, _, _ := seedNDB(t, []byte("seeded"), false)
	if err := n.CommitFile(path); err != nil {
		t.Fatal(err)
	}
	_ = n.Close()
	n2, err := OpenNDBFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer n2.Close()
	oldC := createTemp
	createTemp = func(string, string) (*os.File, error) { return nil, io.ErrClosedPipe }
	_, err = n2.PutDataTree(bytes.NewReader([]byte("x")), 1)
	createTemp = oldC
	if !errors.Is(err, ErrIO) {
		t.Fatalf("createTemp: %v", err)
	}
	n3, err := OpenNDBFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer n3.Close()
	oldCopy := copyWork
	copyWork = func(io.WriterAt, io.ReaderAt, int64) error { return io.ErrClosedPipe }
	_, err = n3.PutDataTree(bytes.NewReader([]byte("y")), 1)
	copyWork = oldCopy
	if !errors.Is(err, ErrIO) {
		t.Fatalf("copyWork: %v", err)
	}
}

func TestCommitRestoreErrorIsJoined(t *testing.T) {
	n, _, _ := seedNDB(t, []byte("join"), false)
	ms := NewMemSink("dst")
	fs := &faultSink{Sink: ms, failWrite: 1}
	err := n.CommitTo(fs)
	if err == nil {
		t.Fatal("expected fault")
	}
	if errors.Is(err, ErrIO) || errors.Is(err, io.ErrClosedPipe) {
		return
	}
	t.Fatalf("missing op error: %v", err)
}
