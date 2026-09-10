package writer

import (
	"io"
	"os"
	"path/filepath"
)

// replacePath is the commit-file rename. Tests inject failures here.
var replacePath = os.Rename

// removeFile removes a temp spool or failed commit temp. Tests inject failures.
var removeFile = os.Remove

// createTemp creates the work spool. Tests inject failures.
var createTemp = os.CreateTemp

// copyWork seeds a new work spool from the committed source. Tests inject failures.
var copyWork = copyReaderAt

// syncDir fsyncs the parent directory after rename. Tests inject failures.
var syncDir = syncParentDir

func syncParentDir(path string) error {
	d, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// openCommitted reopens a renamed CommitFile path. Tests inject failures.
var openCommitted = openCommittedFile

// closeSink closes a commit temp. Tests inject failures.
var closeSink = func(s Sink) error { return s.Close() }

// closeWork closes a work spool or owned file. Tests inject failures.
var closeWork = func(s Sink) error {
	if s == nil {
		return nil
	}
	return s.Close()
}

// createCommitTemp creates the CommitFile sibling. Tests inject failures.
var createCommitTemp = func(path string) (Sink, error) { return CreateFileSink(path) }

// newStageSink is the owned CommitTo staging sink. Tests inject failures.
var newStageSink = func() (Sink, error) { return NewMemSink("stage"), nil }

// truncWork truncates the work spool to the restored EOF. Tests inject failures.
var truncWork = truncSink

func openCommittedFile(path string) (Sink, error) {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return nil, err
	}
	return &FileSink{f: f}, nil
}

func truncSink(w Sink, eof int64) error {
	if w == nil {
		return nil
	}
	tr, ok := w.(interface{ Truncate(int64) error })
	if !ok {
		return nil
	}
	return tr.Truncate(eof)
}

func writeAtFull(w io.WriterAt, p []byte, off int64) error {
	if w == nil {
		return invalidArg("sink", "nil writer")
	}
	n, err := w.WriteAt(p, off)
	if err != nil {
		return err
	}
	if n != len(p) {
		return io.ErrShortWrite
	}
	return nil
}

// ioHandle is the single owned/borrowed payload source.
// r is the authoritative byte source. w is set when the handle is writable.
// Borrowed handles are never closed. Owned files are closed. Owned temp
// spools are closed and removed.
type ioHandle struct {
	r    io.ReaderAt
	w    Sink
	life srcLife
}

func (h *ioHandle) close() error {
	if h == nil || h.life == lifeBorrowed || h.w == nil {
		return nil
	}
	name := ""
	if h.life == lifeTemp {
		name = h.w.Name()
	}
	err := closeWork(h.w)
	life := h.life
	h.r = nil
	h.w = nil
	h.life = lifeBorrowed
	if life == lifeTemp && name != "" {
		if rerr := removeFile(name); rerr != nil && !os.IsNotExist(rerr) {
			return errorsJoin(err, ioErr("spool", "remove %s: %v", name, rerr))
		}
	}
	return err
}

func (s *Store) reader() io.ReaderAt {
	if s == nil {
		return nil
	}
	if s.work != nil && s.work.r != nil {
		return s.work.r
	}
	if s.io != nil {
		return s.io.r
	}
	return nil
}

func (s *Store) writer() Sink {
	if s == nil || s.work == nil {
		return nil
	}
	return s.work.w
}

func (s *Store) setWriter(w Sink) {
	if s.work == nil {
		s.work = &ioHandle{life: lifeTemp}
	}
	s.work.w = w
	if w != nil {
		s.work.r = w
	}
}

// UnwrapSink is an optional wrapper hint. CommitTo does not use it for
// identity: dest is never the in-progress write target.
type UnwrapSink interface {
	UnwrapSink() Sink
}

// UnwrapReaderAt is an optional ReaderAt wrapper hint.
type UnwrapReaderAt interface {
	UnwrapReaderAt() io.ReaderAt
}

// sameIO reports whether a and b are the same *MemSink, *FileSink, or
// *os.File pointer. Interface values are never compared with ==. Unknown
// and wrapper types do not match; CommitTo does not discover backing alias.
func sameIO(a, b any) bool {
	switch x := a.(type) {
	case *MemSink:
		y, ok := b.(*MemSink)
		return ok && x != nil && y != nil && x == y
	case *FileSink:
		y, ok := b.(*FileSink)
		return ok && x != nil && y != nil && x == y
	case *os.File:
		y, ok := b.(*os.File)
		return ok && x != nil && y != nil && x == y
	}
	return false
}

func (s *Store) publishSink(dst, stage Sink) error {
	if dst == nil || stage == nil {
		return invalidArg("sink", "nil publish sink")
	}
	if sameIO(dst, stage) {
		return nil
	}
	eof := int64(s.FileEOF())
	if err := s.writeHeader(dst, AMapInvalid); err != nil {
		return err
	}
	if err := syncSink(dst); err != nil {
		return err
	}
	if err := copyReaderAtFrom(dst, stage, int64(UnicodeHeaderSize), eof); err != nil {
		return ioErr("spool", "publish: %v", err)
	}
	if tr, ok := dst.(interface{ Truncate(int64) error }); ok {
		if err := tr.Truncate(eof); err != nil {
			return ioErr("sink", "truncate: %v", err)
		}
	}
	if err := syncSink(dst); err != nil {
		return err
	}
	if err := s.writeHeader(dst, AMapValid2); err != nil {
		return err
	}
	if err := syncSink(dst); err != nil {
		return err
	}
	return nil
}

// snapshot is the one transaction boundary. Field names for Store/NDB value
// state match the live structs so TestSnapshotCoversMutableFields can catch
// omissions. io/work handles are represented by hadWork. In-place work
// mutations are reverted from Store.undo (extent journal), not a FileEOF copy.
type snapshot struct {
	regions       []region
	lastAllocAMap uint32
	valid         byte
	unique        uint32
	bidNextP      uint64
	bidNextB      uint64
	dlistBID      uint64
	nbtRoot       BREF
	bbtRoot       BREF
	ids           SequentialIDs
	nodes         map[uint64]NBTEntry
	blocks        map[uint64]BBTEntry
	dataTreeRefs  map[uint64]int
	subnodeRefs   map[uint64]int
	opaqueRefs    map[uint64]int
	livePages     []uint64
	hadWork       bool
}

func cloneRegions(in []region) []region {
	out := make([]region, len(in))
	copy(out, in)
	return out
}

func cloneMap[K comparable, V any](m map[K]V) map[K]V {
	if m == nil {
		return nil
	}
	out := make(map[K]V, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func cloneU64(in []uint64) []uint64 {
	if in == nil {
		return nil
	}
	out := make([]uint64, len(in))
	copy(out, in)
	return out
}

func (n *NDB) capture() *snapshot {
	if n == nil || n.store == nil {
		return &snapshot{}
	}
	s := n.store
	snap := &snapshot{
		regions:       cloneRegions(s.regions),
		lastAllocAMap: s.lastAllocAMap,
		valid:         s.valid,
		unique:        s.unique,
		bidNextP:      s.bidNextP,
		bidNextB:      s.bidNextB,
		dlistBID:      s.dlistBID,
		nbtRoot:       s.nbtRoot,
		bbtRoot:       s.bbtRoot,
		nodes:         cloneMap(n.nodes),
		blocks:        cloneMap(n.blocks),
		dataTreeRefs:  cloneMap(n.dataTreeRefs),
		subnodeRefs:   cloneMap(n.subnodeRefs),
		opaqueRefs:    cloneMap(n.opaqueRefs),
		livePages:     cloneU64(n.livePages),
	}
	if n.ids != nil {
		snap.ids = *n.ids
	}
	snap.hadWork = s.work != nil && s.work.w != nil
	return snap
}

func (n *NDB) restore(snap *snapshot) error {
	if n == nil || n.store == nil || snap == nil {
		return nil
	}
	s := n.store
	s.regions = cloneRegions(snap.regions)
	s.lastAllocAMap = snap.lastAllocAMap
	s.valid = snap.valid
	s.unique = snap.unique
	s.bidNextP = snap.bidNextP
	s.bidNextB = snap.bidNextB
	s.dlistBID = snap.dlistBID
	s.nbtRoot = snap.nbtRoot
	s.bbtRoot = snap.bbtRoot
	if n.ids != nil {
		*n.ids = snap.ids
	} else {
		ids := snap.ids
		n.ids = &ids
	}
	n.nodes = cloneMap(snap.nodes)
	n.blocks = cloneMap(snap.blocks)
	n.dataTreeRefs = cloneMap(snap.dataTreeRefs)
	n.subnodeRefs = cloneMap(snap.subnodeRefs)
	n.opaqueRefs = cloneMap(snap.opaqueRefs)
	n.livePages = cloneU64(snap.livePages)
	return n.restoreSource(snap)
}

func (n *NDB) restoreSource(snap *snapshot) error {
	s := n.store
	if !snap.hadWork {
		if s.work != nil {
			err := s.work.close()
			s.work = nil
			return err
		}
		return nil
	}
	if err := s.ensureSpool(); err != nil {
		return err
	}
	if err := s.restoreUndo(); err != nil {
		return err
	}
	if err := truncWork(s.work.w, int64(s.FileEOF())); err != nil {
		return ioErr("spool", "truncate: %v", err)
	}
	return nil
}

func (n *NDB) abortTxn(snap *snapshot, err error) error {
	return rollbackErr(err, n.restore(snap))
}

func (n *NDB) beginTxn(snap *snapshot) {
	if snap != nil && snap.hadWork && n != nil && n.store != nil {
		n.store.beginUndo()
	}
}

func cleanupErr(err error) *Error {
	return &Error{
		Code:   CodeCleanup,
		Field:  "cleanup",
		Detail: err.Error(),
		Err:    ErrCleanup,
	}
}

func (n *NDB) runTxn(fn func() error) error {
	if n == nil || n.store == nil {
		return fn()
	}
	snap := n.capture()
	n.beginTxn(snap)
	defer n.store.endUndo()
	if err := fn(); err != nil {
		return n.abortTxn(snap, err)
	}
	return nil
}

func syncSink(dst Sink) error {
	if dst == nil {
		return invalidArg("sink", "nil commit sink")
	}
	if err := dst.Sync(); err != nil {
		return ioErr("sink", "sync: %v", err)
	}
	return nil
}

func (n *NDB) rejectInPlace(dst Sink) error {
	if dst == nil {
		return invalidArg("sink", "nil commit sink")
	}
	if n == nil || n.store == nil {
		return nil
	}
	if n.store.work != nil && sameIO(dst, n.store.work.w) {
		return unsupported(FeatureInPlaceMutation, "CommitTo destination is the work spool; use CommitFile")
	}
	return nil
}

func (s *Store) writeHeader(dst Sink, valid byte) error {
	prev := s.valid
	s.valid = valid
	hdr, err := EncodeUnicodeHeader(s.HeaderDraft())
	s.valid = prev
	if err != nil {
		return err
	}
	if err := writeAtFull(dst, hdr, 0); err != nil {
		return ioErr("header", "write: %v", err)
	}
	return nil
}

func (s *Store) writeBody(dst Sink) error {
	if dst == nil {
		return invalidArg("sink", "nil commit sink")
	}
	eof := int64(s.FileEOF())
	src := s.reader()
	if src != nil && !sameIO(dst, src) && !sameIO(dst, s.writer()) {
		if err := copyReaderAtFrom(dst, src, int64(UnicodeHeaderSize), eof); err != nil {
			return ioErr("spool", "copy: %v", err)
		}
	}
	if err := s.zeroFreeSlotsAt(dst); err != nil {
		return ioErr("spool", "zero free slots: %v", err)
	}
	dlist, err := s.encodeDList()
	if err != nil {
		return err
	}
	if err := writeAtFull(dst, dlist, int64(DListPageOffset)); err != nil {
		return ioErr("dlist", "write: %v", err)
	}
	for i := range s.regions {
		pages, err := s.encodeRegionPages(uint64(i))
		if err != nil {
			return err
		}
		for _, p := range pages {
			if err := writeAtFull(dst, p.raw, int64(p.off)); err != nil {
				return ioErr("amap", "write at 0x%x: %v", p.off, err)
			}
		}
	}
	if tr, ok := dst.(interface{ Truncate(int64) error }); ok {
		if err := tr.Truncate(eof); err != nil {
			return ioErr("sink", "truncate: %v", err)
		}
	}
	return nil
}

func (n *NDB) writeCommit(dst Sink, img *TreeImage) error {
	n.store.unique++
	if n.store.unique == 0 {
		n.store.unique = 1
	}
	// MS-PST 2.6.1.3.7: INVALID + sync, body/pages + sync, VALID + sync.
	// A crash cannot expose mixed bytes under a VALID header.
	if err := n.store.writeHeader(dst, AMapInvalid); err != nil {
		return err
	}
	if err := syncSink(dst); err != nil {
		return err
	}
	if err := n.store.writeBody(dst); err != nil {
		return err
	}
	for ib, raw := range img.Pages {
		if err := writeAtFull(dst, raw, int64(ib)); err != nil {
			return ioErr("page", "write at 0x%x: %v", ib, err)
		}
	}
	// Body/pages must be durable before VALID reaches storage (MS-PST 2.6.1.3.7).
	if err := syncSink(dst); err != nil {
		return err
	}
	if err := n.store.writeHeader(dst, AMapValid2); err != nil {
		return err
	}
	if err := syncSink(dst); err != nil {
		return err
	}
	n.store.valid = AMapValid2
	return nil
}

func (n *NDB) failAdopt(path string, cause error) error {
	n.pendingPath = path
	return adoptErr("file", "committed %s but source not adopted: %v", path, cause)
}

func (n *NDB) adopt(dst Sink, life srcLife) error {
	if dst == nil {
		return invalidArg("sink", "nil commit sink")
	}
	if n.store.io != nil && (sameIO(dst, n.store.io.w) || sameIO(dst, n.store.io.r)) {
		oldWork := n.store.work
		n.store.work = nil
		n.pendingPath = ""
		if oldWork != nil {
			if err := oldWork.close(); err != nil {
				return cleanupErr(err)
			}
		}
		return nil
	}
	oldWork := n.store.work
	oldIO := n.store.io
	n.store.work = nil
	n.store.io = &ioHandle{r: dst, w: dst, life: life}
	n.pendingPath = ""
	var err error
	if oldWork != nil {
		err = oldWork.close()
	}
	if oldIO != nil {
		err = errorsJoin(err, oldIO.close())
	}
	if err != nil {
		return cleanupErr(err)
	}
	return nil
}
