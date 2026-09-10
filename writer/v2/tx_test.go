package writer

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
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

type opaqueNC struct {
	*MemSink
	_ [0]func()
}

type opaqueFileWrap struct {
	Sink
	_ [0]func()
}

type sliceAliasSink struct {
	inner []Sink
	Sink
	_ [0]func()
}

type mapAliasSink struct {
	inner map[string]Sink
	Sink
	_ [0]func()
}

type closAliasSink struct {
	at func([]byte, int64) (int, error)
	Sink
	_ [0]func()
}

func (c closAliasSink) WriteAt(p []byte, off int64) (int, error) {
	return c.at(p, off)
}

type deepBox struct {
	next Sink
	_    [0]func()
}

func (d deepBox) Write(p []byte) (int, error)              { return d.next.Write(p) }
func (d deepBox) WriteAt(p []byte, off int64) (int, error) { return d.next.WriteAt(p, off) }
func (d deepBox) ReadAt(p []byte, off int64) (int, error)  { return d.next.ReadAt(p, off) }
func (d deepBox) Seek(offset int64, whence int) (int64, error) {
	return d.next.Seek(offset, whence)
}
func (d deepBox) Sync() error  { return d.next.Sync() }
func (d deepBox) Close() error { return d.next.Close() }
func (d deepBox) Name() string { return d.next.Name() }

func nestDeep(s Sink, n int) Sink {
	cur := s
	for i := 0; i < n; i++ {
		cur = deepBox{next: cur}
	}
	return cur
}

type sharedPtrSink struct {
	*MemSink
	shared *int
	_      [0]func()
}

type opaqueReader struct {
	r io.ReaderAt
	_ [0]func()
}

func (o opaqueReader) ReadAt(p []byte, off int64) (int, error) {
	return o.r.ReadAt(p, off)
}

type failReadAt struct {
	io.ReaderAt
}

func (f failReadAt) ReadAt([]byte, int64) (int, error) { return 0, io.ErrClosedPipe }

type shortSink struct {
	Sink
	n int
}

func (s shortSink) WriteAt(p []byte, off int64) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	n := s.n
	if n <= 0 {
		n = 1
	}
	if n > len(p) {
		n = len(p)
	}
	_, _ = s.Sink.WriteAt(p[:n], off)
	return n, nil
}

func (s shortSink) UnwrapSink() Sink { return s.Sink }

type shortOnceSink struct {
	Sink
	left int
}

func (s *shortOnceSink) WriteAt(p []byte, off int64) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if s.left > 0 {
		s.left--
		n := 1
		if n > len(p) {
			n = len(p)
		}
		_, _ = s.Sink.WriteAt(p[:n], off)
		return n, nil
	}
	return s.Sink.WriteAt(p, off)
}

type eofReader struct{}

func (eofReader) ReadAt([]byte, int64) (int, error) { return 0, io.EOF }

type partialReader struct {
	io.ReaderAt
	n int
}

func (p partialReader) ReadAt(b []byte, off int64) (int, error) {
	if p.n <= 0 {
		return 0, io.EOF
	}
	if len(b) > p.n {
		b = b[:p.n]
	}
	n, err := p.ReaderAt.ReadAt(b, off)
	if n > p.n {
		n = p.n
	}
	if err == nil {
		err = io.EOF
	}
	return n, err
}

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

func assertNDBReadable(t *testing.T, n *NDB, nid uint64, want string) {
	t.Helper()
	got, ok := n.LookupNode(nid)
	if !ok {
		t.Fatalf("missing node 0x%x", nid)
	}
	rd, err := n.OpenDataTree(got.DataBID)
	if err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(rd)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != want {
		t.Fatalf("payload %q want %q", b, want)
	}
}

func destIsIO(n *NDB, dst Sink) bool {
	if n == nil || n.store == nil || n.store.io == nil {
		return false
	}
	return sameIO(n.store.io.w, dst) || sameIO(n.store.io.r, dst)
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
	skipStore := map[string]bool{"io": true, "work": true, "hold": true, "undo": true}
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
	snap := n2.capture()
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

func TestCommitToDoesNotTouchDestUntilStageComplete(t *testing.T) {
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
	old := newStageSink
	newStageSink = func() (Sink, error) {
		return &faultSink{Sink: NewMemSink("stage"), failWrite: 1}, nil
	}
	defer func() { newStageSink = old }()
	if err := n.CommitTo(fs); err == nil {
		t.Fatal("expected staging failure")
	}
	if fs.writes != writes || fs.off0 != off0 {
		t.Fatalf("dest mutated before stage completed writes=%d->%d off0=%d->%d", writes, fs.writes, off0, fs.off0)
	}
}

func TestCommitToSameMemSinkPublishesCompleteImage(t *testing.T) {
	n, _, _ := seedNDB(t, []byte("first"), false)
	ms := NewMemSink("same")
	if err := n.CommitTo(ms); err != nil {
		t.Fatal(err)
	}
	extra, err := n.PutDataTree(bytes.NewReader([]byte("more")), 4)
	if err != nil {
		t.Fatal(err)
	}
	mustNode(t, n, 0x61, extra.BID, 0, 0)
	if err := n.CommitTo(ms); err != nil {
		t.Fatal(err)
	}
	n2, err := OpenNDBFrom(onlyReaderAt{bytes.NewReader(ms.Bytes())}, int64(len(ms.Bytes())))
	if err != nil {
		t.Fatal(err)
	}
	defer n2.Close()
	rd, err := n2.OpenDataTree(n2.nodes[0x21].DataBID)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(rd)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "first" {
		t.Fatalf("payload %q", got)
	}
	if _, ok := n2.LookupNode(0x61); !ok {
		t.Fatal("missing extra node")
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
	if err := n.CommitTo(dst); err != nil {
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
	ms := NewMemSink("dst")
	fs := &faultSink{Sink: ms, failWrite: 1}
	if err := n.CommitTo(fs); err == nil {
		t.Fatal("expected write fault")
	}
	if destIsIO(n, fs) {
		t.Fatal("adopted a failed destination")
	}
	if n.store.io == nil {
		t.Fatal("lost complete stage after dest publish failure")
	}
	assertNDBReadable(t, n, 0x21, "src")
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
			if destIsIO(n, fs) {
				t.Fatal("adopted failed dest")
			}
			if n.store.io == nil {
				t.Fatal("lost complete stage after dest publish failure")
			}
			assertNDBReadable(t, n, 0x21, "inj")
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
		if destIsIO(n, fs) {
			t.Fatal("adopted failed dest")
		}
		if n.store.io == nil {
			t.Fatal("lost complete stage after dest publish failure")
		}
		assertNDBReadable(t, n, 0x21, "inj")
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
	old := truncWork
	truncWork = func(Sink, int64) error { return io.ErrClosedPipe }
	defer func() { truncWork = old }()
	_, err := n.PutDataTree(&boomReader{left: 2 << 20}, 0)
	if err == nil {
		t.Fatal("expected boom")
	}
	if !errors.Is(err, ErrIO) && !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("missing op error: %v", err)
	}
	if !strings.Contains(err.Error(), "rollback") {
		t.Fatalf("restore not joined: %v", err)
	}
}

func TestOpaqueNonComparableSinkCommitTo(t *testing.T) {
	n, _, _ := seedNDB(t, []byte("opaque"), false)
	dst := opaqueNC{MemSink: NewMemSink("opaque")}
	if reflect.TypeOf(dst).Comparable() {
		t.Fatal("opaqueNC must not be comparable")
	}
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("CommitTo panicked: %v", r)
		}
	}()
	if err := n.CommitTo(dst); err != nil {
		t.Fatal(err)
	}
	if err := n.CommitTo(dst); err != nil {
		t.Fatalf("second opaque CommitTo: %v", err)
	}
	n2, err := OpenNDBFrom(onlyReaderAt{bytes.NewReader(dst.Bytes())}, int64(len(dst.Bytes())))
	if err != nil {
		t.Fatal(err)
	}
	defer n2.Close()
	rd, err := n2.OpenDataTree(n2.nodes[0x21].DataBID)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(rd)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "opaque" {
		t.Fatalf("payload %q", got)
	}
}

func TestNoteUndoDoesNotInventZeros(t *testing.T) {
	path := filepath.Join(t.TempDir(), "undo-read.pst")
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
	if err := n2.store.ensureSpool(); err != nil {
		t.Fatal(err)
	}
	n2.store.beginUndo()
	defer n2.store.endUndo()
	n2.store.work.r = failReadAt{}
	if n2.store.io != nil {
		n2.store.io.r = failReadAt{}
	}
	err = n2.store.writeExtent(e.IB, bytes.Repeat([]byte{0xA5}, size))
	if err == nil {
		t.Fatal("expected undo capture failure")
	}
	n2.store.work.r = n2.store.work.w
	sk, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer sk.Close()
	n2.store.io.r = sk
	got, err := n2.store.readExtent(e.IB, size)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, orig) {
		t.Fatal("rollback manufactured zeros")
	}
}

func TestClearBackingPropagatesShortWrite(t *testing.T) {
	n, _, _ := seedNDB(t, []byte("clr"), false)
	blk := mustAlloc(t, n, 64)
	n.store.setWriter(shortSink{Sink: n.store.writer(), n: 1})
	err := n.store.Free(blk.IB, 64)
	if err == nil {
		t.Fatal("expected short write")
	}
	if !errors.Is(err, ErrIO) && !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("got %v", err)
	}
}

func TestWriteExtentPropagatesShortWrite(t *testing.T) {
	n, _, _ := seedNDB(t, []byte("short"), false)
	e, ok := n.LookupBlock(n.nodes[0x21].DataBID)
	if !ok {
		t.Fatal("missing block")
	}
	n.store.beginUndo()
	defer n.store.endUndo()
	n.store.setWriter(shortSink{Sink: n.store.writer(), n: 1})
	raw := bytes.Repeat([]byte{0xA5}, int(BlockDiskSize(uint64(e.CB))))
	err := n.store.writeExtent(e.IB, raw)
	if err == nil {
		t.Fatal("expected short write")
	}
	if !errors.Is(err, ErrIO) && !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("got %v", err)
	}
}

func TestCommitFileJoinsCleanupOnWriteFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cleanup.pst")
	n, _, _ := seedNDB(t, []byte("tmp"), false)
	errWrite := io.ErrClosedPipe
	errRemove := errors.New("tmp-remove")
	oldC := createCommitTemp
	createCommitTemp = func(p string) (Sink, error) {
		sk, err := CreateFileSink(p)
		if err != nil {
			return nil, err
		}
		return &faultSink{Sink: sk, failWrite: 1}, nil
	}
	oldR := removeFile
	removeFile = func(string) error { return errRemove }
	defer func() {
		createCommitTemp = oldC
		removeFile = oldR
	}()
	err := n.CommitFile(path)
	if err == nil {
		t.Fatal("expected joined cleanup")
	}
	if !errors.Is(err, ErrIO) && !errors.Is(err, errWrite) {
		t.Fatalf("missing write error: %v", err)
	}
	if !errors.Is(err, errRemove) {
		t.Fatalf("missing remove error: %v", err)
	}
}

func TestCommitFileCreateTempFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "create.pst")
	n, _, _ := seedNDB(t, []byte("tmp"), false)
	errCreate := errors.New("tmp-create")
	oldC := createCommitTemp
	createCommitTemp = func(string) (Sink, error) { return nil, errCreate }
	defer func() { createCommitTemp = oldC }()
	err := n.CommitFile(path)
	if err == nil {
		t.Fatal("expected create failure")
	}
	if !errors.Is(err, ErrIO) || !errors.Is(err, errCreate) {
		t.Fatalf("missing create error: %v", err)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatalf("created dest after create failure: %v", statErr)
	}
}

func TestCommitFileJoinsCloseAndRemove(t *testing.T) {
	path := filepath.Join(t.TempDir(), "close-remove.pst")
	n, _, _ := seedNDB(t, []byte("tmp"), false)
	errClose := errors.New("tmp-close")
	errRemove := errors.New("tmp-remove")
	oldC := createCommitTemp
	createCommitTemp = func(p string) (Sink, error) {
		sk, err := CreateFileSink(p)
		if err != nil {
			return nil, err
		}
		return &faultSink{Sink: sk, failWrite: 1}, nil
	}
	oldClose := closeSink
	closeSink = func(s Sink) error {
		_ = s.Close()
		return errClose
	}
	oldR := removeFile
	removeFile = func(string) error { return errRemove }
	defer func() {
		createCommitTemp = oldC
		closeSink = oldClose
		removeFile = oldR
	}()
	err := n.CommitFile(path)
	if err == nil {
		t.Fatal("expected joined close/remove")
	}
	if !errors.Is(err, ErrIO) {
		t.Fatalf("missing write error: %v", err)
	}
	if !errors.Is(err, errClose) {
		t.Fatalf("missing close error: %v", err)
	}
	if !errors.Is(err, errRemove) {
		t.Fatalf("missing remove error: %v", err)
	}
}

func TestRollbackJoinsWorkSpoolRemove(t *testing.T) {
	n := NewNDB(nil)
	errRemove := errors.New("spool-remove")
	old := removeFile
	removeFile = func(string) error { return errRemove }
	defer func() { removeFile = old }()
	_, err := n.PutDataTree(&boomReader{left: 2 << 20}, 0)
	if err == nil {
		t.Fatal("expected boom")
	}
	if !errors.Is(err, ErrIO) {
		t.Fatalf("missing boom error: %v", err)
	}
	if !errors.Is(err, errRemove) {
		t.Fatalf("spool remove not joined: %v", err)
	}
}

func TestRollbackJoinsWorkSpoolClose(t *testing.T) {
	n := NewNDB(nil)
	errClose := errors.New("spool-close")
	old := closeWork
	closeWork = func(s Sink) error {
		_ = s.Close()
		return errClose
	}
	defer func() { closeWork = old }()
	_, err := n.PutDataTree(&boomReader{left: 2 << 20}, 0)
	if err == nil {
		t.Fatal("expected boom")
	}
	if !errors.Is(err, ErrIO) {
		t.Fatalf("missing boom error: %v", err)
	}
	if !errors.Is(err, errClose) {
		t.Fatalf("spool close not joined: %v", err)
	}
}

func TestRestoreUndoWriteFailure(t *testing.T) {
	n, _, _ := seedNDB(t, []byte("undo-w"), false)
	e, ok := n.LookupBlock(n.nodes[0x21].DataBID)
	if !ok {
		t.Fatal("missing")
	}
	size := int(BlockDiskSize(uint64(e.CB)))
	if _, err := n.store.readExtent(e.IB, size); err != nil {
		t.Fatal(err)
	}
	n.store.beginUndo()
	if err := n.store.writeExtent(e.IB, bytes.Repeat([]byte{0xA5}, size)); err != nil {
		t.Fatal(err)
	}
	n.store.setWriter(&faultSink{Sink: n.store.writer(), failWrite: 1})
	err := n.store.restoreUndo()
	n.store.endUndo()
	if err == nil {
		t.Fatal("expected undo write failure")
	}
}

func TestOpaqueNonComparableSinkSameIONoPanic(t *testing.T) {
	a := opaqueNC{MemSink: NewMemSink("a")}
	b := opaqueNC{MemSink: NewMemSink("b")}
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("sameIO panicked: %v", r)
		}
	}()
	if sameIO(a, a) || sameIO(a, b) {
		t.Fatal("unknown types must not match by pointer-graph inference")
	}
}

func TestOpenNDBFromUnknownReaderAtDistinctUnknownSink(t *testing.T) {
	n, _, _ := seedNDB(t, []byte("unknown-src"), false)
	raw, err := n.Commit()
	if err != nil {
		t.Fatal(err)
	}
	_ = n.Close()
	src := opaqueReader{r: bytes.NewReader(raw)}
	if reflect.TypeOf(src).Comparable() {
		t.Fatal("opaqueReader must not be comparable")
	}
	n2, err := OpenNDBFrom(src, int64(len(raw)))
	if err != nil {
		t.Fatal(err)
	}
	defer n2.Close()
	dst := opaqueNC{MemSink: NewMemSink("unknown-dst")}
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("CommitTo panicked: %v", r)
		}
	}()
	if err := n2.CommitTo(dst); err != nil {
		t.Fatal(err)
	}
	n3, err := OpenNDBFrom(onlyReaderAt{bytes.NewReader(dst.Bytes())}, int64(len(dst.Bytes())))
	if err != nil {
		t.Fatal(err)
	}
	defer n3.Close()
	rd, err := n3.OpenDataTree(n3.nodes[0x21].DataBID)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(rd)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "unknown-src" {
		t.Fatalf("payload %q", got)
	}
}

func TestUnknownSourceSecondDistinctUnknownSink(t *testing.T) {
	n, _, _ := seedNDB(t, []byte("u1"), false)
	a := opaqueNC{MemSink: NewMemSink("a")}
	if err := n.CommitTo(a); err != nil {
		t.Fatal(err)
	}
	first := append([]byte(nil), a.Bytes()...)
	b := opaqueNC{MemSink: NewMemSink("b")}
	if err := n.CommitTo(b); err != nil {
		t.Fatal(err)
	}
	if len(b.Bytes()) == 0 {
		t.Fatal("second dest empty")
	}
	if !bytes.Equal(a.Bytes(), first) {
		t.Fatal("first unknown dest mutated by later CommitTo")
	}
}

func TestHiddenAliasNotMutatedWhenStageFails(t *testing.T) {
	cases := []struct {
		name string
		wrap func(Sink) Sink
	}{
		{"slice", func(s Sink) Sink { return sliceAliasSink{inner: []Sink{s}, Sink: s} }},
		{"map", func(s Sink) Sink { return mapAliasSink{inner: map[string]Sink{"d": s}, Sink: s} }},
		{"closure", func(s Sink) Sink {
			return closAliasSink{at: s.WriteAt, Sink: s}
		}},
		{"deep", func(s Sink) Sink { return nestDeep(s, 12) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			n, _, _ := seedNDB(t, []byte("alias"), false)
			ms := NewMemSink("known")
			if err := n.CommitTo(ms); err != nil {
				t.Fatal(err)
			}
			before := append([]byte(nil), ms.Bytes()...)
			wrap := tc.wrap(ms)
			if reflect.TypeOf(wrap).Comparable() {
				t.Fatal("alias wrapper must not be comparable")
			}
			old := newStageSink
			newStageSink = func() (Sink, error) {
				return &faultSink{Sink: NewMemSink("stage"), failWrite: 1}, nil
			}
			defer func() { newStageSink = old }()
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("CommitTo panicked: %v", r)
				}
			}()
			if err := n.CommitTo(wrap); err == nil {
				t.Fatal("expected staging failure")
			}
			if !bytes.Equal(ms.Bytes(), before) {
				t.Fatal("hidden alias mutated in place")
			}
		})
	}
}

func TestDistinctSinksSharingUnrelatedPointer(t *testing.T) {
	shared := new(int)
	n, _, _ := seedNDB(t, []byte("share"), false)
	a := sharedPtrSink{MemSink: NewMemSink("a"), shared: shared}
	b := sharedPtrSink{MemSink: NewMemSink("b"), shared: shared}
	if reflect.TypeOf(a).Comparable() {
		t.Fatal("sharedPtrSink must not be comparable")
	}
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("CommitTo panicked: %v", r)
		}
	}()
	if err := n.CommitTo(a); err != nil {
		t.Fatal(err)
	}
	first := append([]byte(nil), a.Bytes()...)
	if err := n.CommitTo(b); err != nil {
		t.Fatal(err)
	}
	if len(b.Bytes()) == 0 {
		t.Fatal("second dest empty")
	}
	if !bytes.Equal(a.Bytes(), first) {
		t.Fatal("distinct sink sharing an unrelated pointer was rejected or mutated")
	}
	n2, err := OpenNDBFrom(onlyReaderAt{bytes.NewReader(b.Bytes())}, int64(len(b.Bytes())))
	if err != nil {
		t.Fatal(err)
	}
	defer n2.Close()
	rd, err := n2.OpenDataTree(n2.nodes[0x21].DataBID)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(rd)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "share" {
		t.Fatalf("payload %q", got)
	}
}

func TestUndoZeroEOFIsErrorForAllocated(t *testing.T) {
	n, root, _ := seedNDB(t, []byte("eof0"), false)
	e, ok := n.LookupBlock(root.BID)
	if !ok {
		t.Fatal("missing block")
	}
	if err := n.store.ensureSpool(); err != nil {
		t.Fatal(err)
	}
	n.store.beginUndo()
	defer n.store.endUndo()
	n.store.work.r = eofReader{}
	err := n.store.writeExtent(e.IB, bytes.Repeat([]byte{0xA5}, int(BlockDiskSize(uint64(e.CB)))))
	if err == nil {
		t.Fatal("expected zero-EOF undo error")
	}
}

func TestUndoPartialEOFIsErrorForAllocated(t *testing.T) {
	n, root, _ := seedNDB(t, []byte("part"), false)
	e, ok := n.LookupBlock(root.BID)
	if !ok {
		t.Fatal("missing block")
	}
	if err := n.store.ensureSpool(); err != nil {
		t.Fatal(err)
	}
	size := int(BlockDiskSize(uint64(e.CB)))
	n.store.beginUndo()
	defer n.store.endUndo()
	n.store.work.r = partialReader{ReaderAt: n.store.work.w, n: 3}
	err := n.store.writeExtent(e.IB, bytes.Repeat([]byte{0xA5}, size))
	if err == nil {
		t.Fatal("expected partial-EOF undo error")
	}
}

func TestUndoDoesNotFallBackToCommittedBytes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "divergent.pst")
	n, root, _ := seedNDB(t, []byte("committed"), false)
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
	if err := n2.store.ensureSpool(); err != nil {
		t.Fatal(err)
	}
	workBytes := bytes.Repeat([]byte{0xA5}, size)
	if err := writeAtFull(n2.store.work.w, workBytes, int64(e.IB)); err != nil {
		t.Fatal(err)
	}
	n2.store.beginUndo()
	defer n2.store.endUndo()
	orig, err := n2.store.readUndoBytes(e.IB, size)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(orig, workBytes) {
		t.Fatal("undo captured committed bytes instead of work")
	}
}

func TestRunTxnShortWriteRestoresState(t *testing.T) {
	n, root, _ := seedNDB(t, []byte("keep-me"), false)
	keep, err := n.PutDataTree(bytes.NewReader([]byte("keep2")), 5)
	if err != nil {
		t.Fatal(err)
	}
	mustNode(t, n, 0x61, keep.BID, 0, 0)
	before := n.capture()
	eof := n.store.FileEOF()
	payload := func(bid uint64) []byte {
		t.Helper()
		rd, err := n.OpenDataTree(bid)
		if err != nil {
			t.Fatal(err)
		}
		b, err := io.ReadAll(rd)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	origRoot := payload(root.BID)
	origKeep := payload(keep.BID)
	origW := n.store.writer()
	n.store.setWriter(&shortOnceSink{Sink: origW, left: 1})
	_, err = n.PutDataTree(bytes.NewReader(bytes.Repeat([]byte("z"), 200)), 200)
	if err == nil {
		t.Fatal("expected short write through runTxn")
	}
	if n.store.FileEOF() != eof {
		t.Fatalf("EOF %d want %d", n.store.FileEOF(), eof)
	}
	if n.store.lastAllocAMap != before.lastAllocAMap {
		t.Fatalf("lastAllocAMap %d want %d", n.store.lastAllocAMap, before.lastAllocAMap)
	}
	if n.store.unique != before.unique {
		t.Fatalf("unique %d want %d", n.store.unique, before.unique)
	}
	if n.store.bidNextP != before.bidNextP || n.store.bidNextB != before.bidNextB {
		t.Fatalf("counters p=%d/%d b=%d/%d", n.store.bidNextP, before.bidNextP, n.store.bidNextB, before.bidNextB)
	}
	got, ok := n.LookupNode(0x61)
	if !ok || got.DataBID != keep.BID {
		t.Fatalf("keep node %+v", got)
	}
	if !bytes.Equal(payload(root.BID), origRoot) || !bytes.Equal(payload(keep.BID), origKeep) {
		t.Fatal("payloads not restored")
	}
	if !reflect.DeepEqual(n.dataTreeRefs, before.dataTreeRefs) || !reflect.DeepEqual(n.subnodeRefs, before.subnodeRefs) || !reflect.DeepEqual(n.opaqueRefs, before.opaqueRefs) {
		t.Fatal("refs not restored")
	}
	if !n.store.BitmapEqual(&Store{regions: before.regions}) {
		t.Fatal("allocation map not restored")
	}
}

func TestHiddenAliasPublishFailureKeepsSource(t *testing.T) {
	cases := []struct {
		name string
		wrap func(Sink) Sink
	}{
		{"slice", func(s Sink) Sink { return sliceAliasSink{inner: []Sink{s}, Sink: s} }},
		{"map", func(s Sink) Sink { return mapAliasSink{inner: map[string]Sink{"d": s}, Sink: s} }},
		{"closure", func(s Sink) Sink { return closAliasSink{at: s.WriteAt, Sink: s} }},
		{"deep", func(s Sink) Sink { return nestDeep(s, 12) }},
	}
	count := &faultSink{Sink: NewMemSink("count")}
	n0, _, _ := seedNDB(t, []byte("alias-src"), false)
	if err := n0.CommitTo(count); err != nil {
		t.Fatal(err)
	}
	writes, syncs, truncs := count.writes, count.syncs, count.truncs
	if writes < 1 {
		t.Fatal("no dest writes")
	}
	for _, tc := range cases {
		for i := 1; i <= writes; i++ {
			i := i
			t.Run(tc.name+"/write", func(t *testing.T) {
				n, _, _ := seedNDB(t, []byte("alias-src"), false)
				ms := NewMemSink("known")
				if err := n.CommitTo(ms); err != nil {
					t.Fatal(err)
				}
				extra, err := n.PutDataTree(bytes.NewReader([]byte("more")), 4)
				if err != nil {
					t.Fatal(err)
				}
				mustNode(t, n, 0x61, extra.BID, 0, 0)
				fs := &faultSink{Sink: ms, failWrite: i}
				wrap := tc.wrap(fs)
				err = n.CommitTo(wrap)
				if err == nil {
					assertNDBReadable(t, n, 0x21, "alias-src")
					return
				}
				if destIsIO(n, fs) || destIsIO(n, wrap) {
					t.Fatal("adopted failed dest")
				}
				if n.store.io == nil {
					t.Fatal("lost complete stage")
				}
				assertNDBReadable(t, n, 0x21, "alias-src")
				assertNDBReadable(t, n, 0x61, "more")
			})
		}
		for i := 1; i <= syncs; i++ {
			i := i
			t.Run(tc.name+"/sync", func(t *testing.T) {
				n, _, _ := seedNDB(t, []byte("alias-src"), false)
				ms := NewMemSink("known")
				if err := n.CommitTo(ms); err != nil {
					t.Fatal(err)
				}
				fs := &faultSink{Sink: ms, failSync: i}
				wrap := tc.wrap(fs)
				if err := n.CommitTo(wrap); err == nil {
					assertNDBReadable(t, n, 0x21, "alias-src")
					return
				}
				if n.store.io == nil {
					t.Fatal("lost complete stage")
				}
				assertNDBReadable(t, n, 0x21, "alias-src")
			})
		}
		for i := 1; i <= truncs; i++ {
			i := i
			t.Run(tc.name+"/trunc", func(t *testing.T) {
				n, _, _ := seedNDB(t, []byte("alias-src"), false)
				ms := NewMemSink("known")
				if err := n.CommitTo(ms); err != nil {
					t.Fatal(err)
				}
				fs := &faultSink{Sink: ms, failTrunc: i}
				wrap := tc.wrap(fs)
				if err := n.CommitTo(wrap); err == nil {
					assertNDBReadable(t, n, 0x21, "alias-src")
					return
				}
				if n.store.io == nil {
					t.Fatal("lost complete stage")
				}
				assertNDBReadable(t, n, 0x21, "alias-src")
			})
		}
	}
}

func TestOwnedFileOpaqueAliasCommitTo(t *testing.T) {
	path := filepath.Join(t.TempDir(), "owned-alias.pst")
	n, _, _ := seedNDB(t, []byte("owned-src"), false)
	if err := n.CommitFile(path); err != nil {
		t.Fatal(err)
	}
	_ = n.Close()
	n2, err := OpenNDBFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer n2.Close()
	fs, ok := n2.store.io.w.(*FileSink)
	if !ok {
		t.Fatalf("owned source %T", n2.store.io.w)
	}
	extra, err := n2.PutDataTree(bytes.NewReader([]byte("more")), 4)
	if err != nil {
		t.Fatal(err)
	}
	mustNode(t, n2, 0x61, extra.BID, 0, 0)
	wrap := opaqueFileWrap{Sink: fs}
	if reflect.TypeOf(wrap).Comparable() {
		t.Fatal("opaqueFileWrap must not be comparable")
	}
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("CommitTo panicked: %v", r)
		}
	}()
	if err := n2.CommitTo(wrap); err != nil && !errors.Is(err, ErrCleanup) {
		t.Fatal(err)
	}
	assertNDBReadable(t, n2, 0x21, "owned-src")
	assertNDBReadable(t, n2, 0x61, "more")
}

func TestStageFileBackedNotMemSink(t *testing.T) {
	n, _, _ := seedNDB(t, []byte("stage-file"), false)
	calls := 0
	old := createTemp
	createTemp = func(dir, pattern string) (*os.File, error) {
		calls++
		return old(dir, pattern)
	}
	defer func() { createTemp = old }()
	dst := NewMemSink("out")
	if err := n.CommitTo(dst); err != nil {
		t.Fatal(err)
	}
	if calls == 0 {
		t.Fatal("CommitTo used RAM stage instead of file-backed temp")
	}
	assertNDBReadable(t, n, 0x21, "stage-file")
}

func TestStageInjectedBoundaries(t *testing.T) {
	probe := &faultSink{Sink: NewMemSink("probe")}
	old := newStageSink
	newStageSink = func() (Sink, error) { return probe, nil }
	n0, _, _ := seedNDB(t, []byte("stg"), false)
	if err := n0.CommitTo(NewMemSink("discard")); err != nil {
		t.Fatal(err)
	}
	writes, syncs, truncs := probe.writes, probe.syncs, probe.truncs
	newStageSink = old
	if writes < 3 || syncs < 3 {
		t.Fatalf("too few stage ops writes=%d syncs=%d", writes, syncs)
	}
	check := func(t *testing.T, failWrite, failSync, failTrunc int) {
		t.Helper()
		n, _, _ := seedNDB(t, []byte("stg"), false)
		ms := NewMemSink("dst")
		fs := &faultSink{Sink: NewMemSink("stage"), failWrite: failWrite, failSync: failSync, failTrunc: failTrunc}
		newStageSink = func() (Sink, error) { return fs, nil }
		defer func() { newStageSink = old }()
		writes0 := 0
		err := n.CommitTo(ms)
		if err == nil {
			t.Fatal("expected stage failure")
		}
		if destIsIO(n, ms) {
			t.Fatal("adopted dest after stage failure")
		}
		if len(ms.Bytes()) != writes0 {
			t.Fatal("dest mutated after stage failure")
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

func TestStageCreateFailure(t *testing.T) {
	n, _, _ := seedNDB(t, []byte("create"), false)
	ms := NewMemSink("dst")
	errCreate := errors.New("stage-create")
	old := newStageSink
	newStageSink = func() (Sink, error) { return nil, errCreate }
	defer func() { newStageSink = old }()
	err := n.CommitTo(ms)
	if err == nil {
		t.Fatal("expected stage create failure")
	}
	if !errors.Is(err, ErrIO) || !errors.Is(err, errCreate) {
		t.Fatalf("missing create error: %v", err)
	}
	if destIsIO(n, ms) {
		t.Fatal("adopted dest")
	}
}

func TestStageCloseRemoveAfterDestSync(t *testing.T) {
	n, _, _ := seedNDB(t, []byte("cleanup-stage"), false)
	errClose := errors.New("stage-close")
	errRemove := errors.New("stage-remove")
	oldC := closeWork
	closeWork = func(s Sink) error {
		_ = s.Close()
		return errClose
	}
	oldR := removeFile
	removeFile = func(string) error { return errRemove }
	defer func() {
		closeWork = oldC
		removeFile = oldR
	}()
	dst := NewMemSink("out")
	err := n.CommitTo(dst)
	if err == nil {
		t.Fatal("expected stage cleanup error")
	}
	if !errors.Is(err, ErrCleanup) {
		t.Fatalf("want cleanup: %v", err)
	}
	if !errors.Is(err, errClose) {
		t.Fatalf("missing close: %v", err)
	}
	if !errors.Is(err, errRemove) {
		t.Fatalf("missing remove: %v", err)
	}
	if !destIsIO(n, dst) {
		t.Fatal("dest not adopted after dest sync")
	}
	assertNDBReadable(t, n, 0x21, "cleanup-stage")
}

func trackSinkCloses(t *testing.T, want Sink) *int {
	t.Helper()
	n := 0
	old := closeWork
	closeWork = func(s Sink) error {
		if sameIO(s, want) {
			n++
		}
		return old(s)
	}
	t.Cleanup(func() { closeWork = old })
	return &n
}

func openOwnedFileNDB(t *testing.T, payload string) (*NDB, *FileSink) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "owned.pst")
	n, _, _ := seedNDB(t, []byte(payload), false)
	if err := n.CommitFile(path); err != nil {
		t.Fatal(err)
	}
	_ = n.Close()
	n2, err := OpenNDBFile(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = n2.Close() })
	fs, ok := n2.store.io.w.(*FileSink)
	if !ok {
		t.Fatalf("owned source %T", n2.store.io.w)
	}
	return n2, fs
}

func TestRepeatedSuccessfulOpaqueOwnedAlias(t *testing.T) {
	n, fs := openOwnedFileNDB(t, "owned-src")
	closes := trackSinkCloses(t, fs)
	wrap := opaqueFileWrap{Sink: fs}
	if reflect.TypeOf(wrap).Comparable() {
		t.Fatal("opaqueFileWrap must not be comparable")
	}
	for i := 0; i < 3; i++ {
		payload := string([]byte{byte('a' + i)})
		extra, err := n.PutDataTree(bytes.NewReader([]byte(payload)), int64(len(payload)))
		if err != nil {
			t.Fatal(err)
		}
		nid := uint64(0x61 + i*0x20)
		mustNode(t, n, nid, extra.BID, 0, 0)
		if err := n.CommitTo(wrap); err != nil && !errors.Is(err, ErrCleanup) {
			t.Fatal(err)
		}
		if *closes != 0 {
			t.Fatalf("owned file closed %d times before Close", *closes)
		}
		assertNDBReadable(t, n, 0x21, "owned-src")
		assertNDBReadable(t, n, nid, payload)
		if n.store.hold == nil || !sameIO(n.store.hold.w, fs) {
			t.Fatal("lost held owned file")
		}
	}
	if err := n.Close(); err != nil {
		t.Fatal(err)
	}
	if *closes != 1 {
		t.Fatalf("owned file closed %d times, want 1", *closes)
	}
}

func TestRepeatedPublishFailureKeepsHold(t *testing.T) {
	n, fs := openOwnedFileNDB(t, "hold-src")
	closes := trackSinkCloses(t, fs)
	for i := 0; i < 2; i++ {
		fsink := &faultSink{Sink: fs, failWrite: 1}
		wrap := opaqueFileWrap{Sink: fsink}
		if err := n.CommitTo(wrap); err == nil {
			t.Fatal("expected publish failure")
		}
		if destIsIO(n, fs) || destIsIO(n, wrap) || destIsIO(n, fsink) {
			t.Fatal("adopted failed dest")
		}
		if n.store.io == nil {
			t.Fatal("lost complete stage")
		}
		assertNDBReadable(t, n, 0x21, "hold-src")
		if n.store.hold == nil || !sameIO(n.store.hold.w, fs) {
			t.Fatal("hold overwritten or leaked")
		}
		if *closes != 0 {
			t.Fatalf("owned file closed during failed publish: %d", *closes)
		}
	}
	if err := n.Close(); err != nil {
		t.Fatal(err)
	}
	if *closes != 1 {
		t.Fatalf("owned file closed %d times, want 1", *closes)
	}
}

func TestDestinationAliasesExistingHold(t *testing.T) {
	n, fs := openOwnedFileNDB(t, "alias-hold")
	closes := trackSinkCloses(t, fs)
	wrap1 := opaqueFileWrap{Sink: fs}
	if err := n.CommitTo(wrap1); err != nil && !errors.Is(err, ErrCleanup) {
		t.Fatal(err)
	}
	if n.store.hold == nil || !sameIO(n.store.hold.w, fs) {
		t.Fatal("expected hold after first opaque commit")
	}
	extra, err := n.PutDataTree(bytes.NewReader([]byte("more")), 4)
	if err != nil {
		t.Fatal(err)
	}
	mustNode(t, n, 0x61, extra.BID, 0, 0)
	wrap2 := opaqueFileWrap{Sink: fs}
	if err := n.CommitTo(wrap2); err != nil && !errors.Is(err, ErrCleanup) {
		t.Fatal(err)
	}
	if *closes != 0 {
		t.Fatalf("closed held file on dest-alias commit: %d", *closes)
	}
	assertNDBReadable(t, n, 0x21, "alias-hold")
	assertNDBReadable(t, n, 0x61, "more")
	if n.store.hold == nil || !sameIO(n.store.hold.w, fs) {
		t.Fatal("hold lost after dest-alias commit")
	}
	if err := n.Close(); err != nil {
		t.Fatal(err)
	}
	if *closes != 1 {
		t.Fatalf("owned file closed %d times, want 1", *closes)
	}
}

func TestDiscardStageJoinedOnPrePublish(t *testing.T) {
	errEncode := errors.New("encode-fail")
	errClose := errors.New("stage-close")
	errRemove := errors.New("stage-remove")
	install := func(t *testing.T) {
		oldC := closeSink
		closeSink = func(s Sink) error {
			_ = s.Close()
			return errClose
		}
		oldR := removeFile
		removeFile = func(string) error { return errRemove }
		t.Cleanup(func() {
			closeSink = oldC
			removeFile = oldR
		})
	}
	t.Run("encode", func(t *testing.T) {
		n, _, _ := seedNDB(t, []byte("enc"), false)
		ms := NewMemSink("dst")
		install(t)
		oldE := encodeImage
		encodeImage = func(*NDB) (*TreeImage, error) { return nil, errEncode }
		t.Cleanup(func() { encodeImage = oldE })
		err := n.CommitTo(ms)
		if err == nil {
			t.Fatal("expected encode failure")
		}
		if !errors.Is(err, errEncode) {
			t.Fatalf("missing encode: %v", err)
		}
		if !errors.Is(err, errClose) {
			t.Fatalf("missing close: %v", err)
		}
		if !errors.Is(err, errRemove) {
			t.Fatalf("missing remove: %v", err)
		}
		if destIsIO(n, ms) {
			t.Fatal("adopted dest after encode failure")
		}
		if len(ms.Bytes()) != 0 {
			t.Fatal("dest mutated after encode failure")
		}
	})
	t.Run("writeCommit", func(t *testing.T) {
		n, _, _ := seedNDB(t, []byte("wr"), false)
		ms := NewMemSink("dst")
		install(t)
		oldS := newStageSink
		newStageSink = func() (Sink, error) {
			s, err := oldS()
			if err != nil {
				return nil, err
			}
			return &faultSink{Sink: s, failWrite: 1}, nil
		}
		t.Cleanup(func() { newStageSink = oldS })
		err := n.CommitTo(ms)
		if err == nil {
			t.Fatal("expected writeCommit failure")
		}
		if !errors.Is(err, io.ErrClosedPipe) {
			t.Fatalf("missing write: %v", err)
		}
		if !errors.Is(err, errClose) {
			t.Fatalf("missing close: %v", err)
		}
		if !errors.Is(err, errRemove) {
			t.Fatalf("missing remove: %v", err)
		}
		if destIsIO(n, ms) {
			t.Fatal("adopted dest after stage write failure")
		}
		if len(ms.Bytes()) != 0 {
			t.Fatal("dest mutated after stage write failure")
		}
	})
}
