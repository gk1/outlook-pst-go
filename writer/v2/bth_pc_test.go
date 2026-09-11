package writer

import (
	"bytes"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestBTHEmptyAndSingleLevel(t *testing.T) {
	n := NewNDB(nil)
	h := NewHeap(n, HeapSigBTH)
	b, err := NewBTH(h, 2, 4)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Build(); err != nil {
		t.Fatal(err)
	}
	nid := MakeNID(NIDTypeInternal, 0x20)
	if err := h.Commit(nid); err != nil {
		t.Fatal(err)
	}
	hv, err := OpenHeap(n, nid)
	if err != nil {
		t.Fatal(err)
	}
	bv, err := OpenBTH(hv, hv.Root())
	if err != nil {
		t.Fatal(err)
	}
	if bv.Levels() != 0 {
		t.Fatalf("empty levels %d", bv.Levels())
	}
	ents, err := bv.Entries()
	if err != nil {
		t.Fatal(err)
	}
	if len(ents) != 0 {
		t.Fatalf("empty entries %d", len(ents))
	}

	n = NewNDB(nil)
	h = NewHeap(n, HeapSigBTH)
	b, err = NewBTH(h, 2, 4)
	if err != nil {
		t.Fatal(err)
	}
	for i, k := range []uint16{0x0037, 0x001A, 0x0E07} {
		key := make([]byte, 2)
		val := make([]byte, 4)
		binary.LittleEndian.PutUint16(key, k)
		binary.LittleEndian.PutUint32(val, uint32(i+1))
		if err := b.Insert(key, val); err != nil {
			t.Fatal(err)
		}
	}
	if err := b.Insert([]byte{0x1A, 0x00}, make([]byte, 4)); !errors.Is(err, ErrInvalidArg) {
		t.Fatalf("duplicate: %v", err)
	}
	nid = MakeNID(NIDTypeInternal, 0x21)
	if _, err := b.Build(); err != nil {
		t.Fatal(err)
	}
	if err := h.Commit(nid); err != nil {
		t.Fatal(err)
	}
	hv, err = OpenHeap(n, nid)
	if err != nil {
		t.Fatal(err)
	}
	bv, err = OpenBTH(hv, hv.Root())
	if err != nil {
		t.Fatal(err)
	}
	ents, err = bv.Entries()
	if err != nil {
		t.Fatal(err)
	}
	if len(ents) != 3 {
		t.Fatalf("entries %d", len(ents))
	}
	var prev []byte
	for i, e := range ents {
		if i > 0 && compareBTHKey(prev, e.key) >= 0 {
			t.Fatalf("unsorted %x then %x", prev, e.key)
		}
		prev = e.key
	}
	got, err := bv.Lookup([]byte{0x1A, 0x00})
	if err != nil {
		t.Fatal(err)
	}
	if binary.LittleEndian.Uint32(got) != 2 {
		t.Fatalf("lookup %x", got)
	}
}

func TestBTHMultiLevel(t *testing.T) {
	n := NewNDB(nil)
	h := NewHeap(n, HeapSigBTH)
	b, err := NewBTH(h, 2, 6)
	if err != nil {
		t.Fatal(err)
	}
	const nEnt = 900
	for i := 0; i < nEnt; i++ {
		key := make([]byte, 2)
		val := make([]byte, 6)
		binary.LittleEndian.PutUint16(key, uint16(i+1))
		binary.LittleEndian.PutUint32(val[2:], uint32(i+1))
		if err := b.Insert(key, val); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := b.Build(); err != nil {
		t.Fatal(err)
	}
	nid := MakeNID(NIDTypeInternal, 0x22)
	if err := h.Commit(nid); err != nil {
		t.Fatal(err)
	}
	hv, err := OpenHeap(n, nid)
	if err != nil {
		t.Fatal(err)
	}
	bv, err := OpenBTH(hv, hv.Root())
	if err != nil {
		t.Fatal(err)
	}
	if bv.Levels() < 1 {
		t.Fatalf("want multi-level, got %d", bv.Levels())
	}
	ents, err := bv.Entries()
	if err != nil {
		t.Fatal(err)
	}
	if len(ents) != nEnt {
		t.Fatalf("entries %d", len(ents))
	}
	for i := 0; i < nEnt; i++ {
		key := make([]byte, 2)
		binary.LittleEndian.PutUint16(key, uint16(i+1))
		got, err := bv.Lookup(key)
		if err != nil {
			t.Fatal(err)
		}
		if binary.LittleEndian.Uint32(got[2:]) != uint32(i+1) {
			t.Fatalf("key %d: %x", i+1, got)
		}
	}
}

func TestPCTypesRoundTrip(t *testing.T) {
	n := NewNDB(nil)
	pc := NewPC(n)
	if err := pc.SetInt16(0x0036, 2); err != nil {
		t.Fatal(err)
	}
	if err := pc.SetInt32(0x0E07, 0x11); err != nil {
		t.Fatal(err)
	}
	if err := pc.SetBool(0x0E1B, true); err != nil {
		t.Fatal(err)
	}
	if err := pc.SetInt64(0x0E08, 99); err != nil {
		t.Fatal(err)
	}
	if err := pc.SetTime(0x0039, 0x01D00000DEADBEEF); err != nil {
		t.Fatal(err)
	}
	if err := pc.SetString(0x0037, "café 日本語"); err != nil {
		t.Fatal(err)
	}
	if err := pc.SetString(0x001A, ""); err != nil {
		t.Fatal(err)
	}
	if err := pc.SetBinary(0x1009, nil); err != nil {
		t.Fatal(err)
	}
	if err := pc.SetBinary(0x0102, []byte{0, 1, 2, 3}); err != nil {
		t.Fatal(err)
	}
	guid := [16]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}
	if err := pc.SetGUID(0x0048, guid); err != nil {
		t.Fatal(err)
	}
	obj := bytes.Repeat([]byte("OBJ"), 100)
	if err := pc.SetObject(0x0EA3, obj); err != nil {
		t.Fatal(err)
	}
	if err := pc.SetMVInt32(0x1003, []int32{1, -2, 3}); err != nil {
		t.Fatal(err)
	}
	if err := pc.SetMVString(0x101F, []string{"", "α", "beta"}); err != nil {
		t.Fatal(err)
	}
	if err := pc.SetMVBinary(0x1102, [][]byte{nil, {9, 8}}); err != nil {
		t.Fatal(err)
	}
	if err := pc.Add(0x8001, 0x00FB, []byte{9, 8, 7, 6, 5}); err != nil {
		t.Fatal(err)
	}
	if err := pc.Add(0x0037, PtypString, encodeUnicodePC("dup")); !errors.Is(err, ErrInvalidArg) {
		t.Fatalf("duplicate: %v", err)
	}
	nid := MakeNID(NIDTypeInternal, 0x23)
	if err := pc.Commit(nid); err != nil {
		t.Fatal(err)
	}
	v, err := OpenPC(n, nid)
	if err != nil {
		t.Fatal(err)
	}
	ids, err := v.IDs()
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i < len(ids); i++ {
		if ids[i] <= ids[i-1] {
			t.Fatalf("IDs not sorted: %x", ids)
		}
	}
	s, err := v.GetString(0x0037)
	if err != nil {
		t.Fatal(err)
	}
	if s != "café 日本語" {
		t.Fatalf("string %q", s)
	}
	s, err = v.GetString(0x001A)
	if err != nil {
		t.Fatal(err)
	}
	if s != "" {
		t.Fatalf("empty string %q", s)
	}
	typ, data, err := v.Get(0x0102)
	if err != nil {
		t.Fatal(err)
	}
	if typ != PtypBinary || !bytes.Equal(data, []byte{0, 1, 2, 3}) {
		t.Fatalf("bin %x %x", typ, data)
	}
	typ, data, err = v.Get(0x1009)
	if err != nil {
		t.Fatal(err)
	}
	if typ != PtypBinary || len(data) != 0 {
		t.Fatalf("empty bin %x %d", typ, len(data))
	}
	typ, data, err = v.Get(0x0EA3)
	if err != nil {
		t.Fatal(err)
	}
	if typ != PtypObject || !bytes.Equal(data, obj) {
		t.Fatalf("object %x %d", typ, len(data))
	}
	typ, data, err = v.Get(0x8001)
	if err != nil {
		t.Fatal(err)
	}
	if typ != 0x00FB || !bytes.Equal(data, []byte{9, 8, 7, 6, 5}) {
		t.Fatalf("unknown %x %x", typ, data)
	}
	mv, err := v.GetMVString(0x101F)
	if err != nil {
		t.Fatal(err)
	}
	if len(mv) != 3 || mv[0] != "" || mv[1] != "α" || mv[2] != "beta" {
		t.Fatalf("mv string %#v", mv)
	}
	mb, err := v.GetMVBinary(0x1102)
	if err != nil {
		t.Fatal(err)
	}
	if len(mb) != 2 || len(mb[0]) != 0 || !bytes.Equal(mb[1], []byte{9, 8}) {
		t.Fatalf("mv bin %#v", mb)
	}

	typ, data, err = v.Get(0x1003)
	if err != nil {
		t.Fatal(err)
	}
	if typ != PtypMVInteger32 || binary.LittleEndian.Uint32(data) != 3 {
		t.Fatalf("mv int32 %x %x", typ, data)
	}
	if int32(binary.LittleEndian.Uint32(data[8:8+4])) != -2 {
		t.Fatalf("mv int32 item1 %x", data)
	}
	loaded, err := v.Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := loaded.SetInt32(0x0E17, 7); err != nil {
		t.Fatal(err)
	}
	if err := loaded.SetString(0x0037, "mutated"); err != nil {
		t.Fatal(err)
	}
	nid2 := MakeNID(NIDTypeInternal, 0x24)
	if err := loaded.Commit(nid2); err != nil {
		t.Fatal(err)
	}
	v2, err := OpenPC(n, nid2)
	if err != nil {
		t.Fatal(err)
	}
	typ, data, err = v2.Get(0x8001)
	if err != nil {
		t.Fatal(err)
	}
	if typ != 0x00FB || !bytes.Equal(data, []byte{9, 8, 7, 6, 5}) {
		t.Fatalf("unknown dropped after mutation: %x %x", typ, data)
	}
	s, err = v2.GetString(0x0037)
	if err != nil {
		t.Fatal(err)
	}
	if s != "mutated" {
		t.Fatalf("mutated string %q", s)
	}
}

func TestPCMultiLevelAndLarge(t *testing.T) {
	n := NewNDB(nil)
	pc := NewPC(n)
	for i := 0; i < 600; i++ {
		if err := pc.SetInt32(uint16(0x1000+i), int32(i)); err != nil {
			t.Fatal(err)
		}
	}
	big := bytes.Repeat([]byte("L"), HeapMaxAlloc+40)
	if err := pc.SetBinary(0x0102, big); err != nil {
		t.Fatal(err)
	}
	pad := bytes.Repeat([]byte("P"), 200)
	for i := 0; i < 80; i++ {
		if err := pc.SetBinary(uint16(0x2000+i), pad); err != nil {
			t.Fatal(err)
		}
	}
	nid := MakeNID(NIDTypeInternal, 0x25)
	if err := pc.Commit(nid); err != nil {
		t.Fatal(err)
	}
	v, err := OpenPC(n, nid)
	if err != nil {
		t.Fatal(err)
	}
	if v.bth.Levels() < 1 {
		t.Fatalf("PC BTH levels %d", v.bth.Levels())
	}
	if v.heap.Pages() < 2 {
		t.Fatalf("want multi-page HN, got %d", v.heap.Pages())
	}
	key := make([]byte, 2)
	binary.LittleEndian.PutUint16(key, 0x0102)
	rec, err := v.bth.Lookup(key)
	if err != nil {
		t.Fatal(err)
	}
	hnid := binary.LittleEndian.Uint32(rec[2:6])
	if IsHID(hnid) {
		t.Fatalf("large value hid 0x%x, want subnode", hnid)
	}
	for i := 0; i < 600; i++ {
		typ, data, err := v.Get(uint16(0x1000 + i))
		if err != nil {
			t.Fatal(err)
		}
		if typ != PtypInteger32 || binary.LittleEndian.Uint32(data) != uint32(i) {
			t.Fatalf("prop %d: %x %x", i, typ, data)
		}
	}
	typ, data, err := v.Get(0x0102)
	if err != nil {
		t.Fatal(err)
	}
	if typ != PtypBinary || !bytes.Equal(data, big) {
		t.Fatalf("large %d", len(data))
	}
}

func TestPCDeterministicOrder(t *testing.T) {
	build := func(ids []uint16) []uint16 {
		n := NewNDB(nil)
		pc := NewPC(n)
		for _, id := range ids {
			if err := pc.SetInt32(id, int32(id)); err != nil {
				t.Fatal(err)
			}
		}
		nid := MakeNID(NIDTypeInternal, 0x26)
		if err := pc.Commit(nid); err != nil {
			t.Fatal(err)
		}
		v, err := OpenPC(n, nid)
		if err != nil {
			t.Fatal(err)
		}
		got, err := v.IDs()
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	a := build([]uint16{0x0037, 0x001A, 0x0E07, 0x0E1B})
	b := build([]uint16{0x0E1B, 0x0E07, 0x001A, 0x0037})
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("order %v vs %v", a, b)
	}
}

func TestSnapshotValueStateIsolation(t *testing.T) {
	n := NewNDB(nil)
	root, err := n.PutDataTree(bytes.NewReader([]byte("abc")), 3)
	if err != nil {
		t.Fatal(err)
	}
	mustNode(t, n, 0x21, root.BID, 0, 0)
	snap := n.capture()
	origLast := n.store.lastAllocAMap
	origBid := n.store.bidNextB
	origNodes := len(n.nodes)
	n.store.lastAllocAMap = origLast + 3
	n.store.bidNextB += 16
	n.nodes[0x99] = NBTEntry{NID: 0x99}
	origNid := n.ids.nid[NIDTypeInternal]
	n.ids.nid[NIDTypeInternal]++
	if snap.store.lastAllocAMap != origLast {
		t.Fatal("snapshot store not isolated")
	}
	if snap.store.bidNextB != origBid {
		t.Fatal("snapshot bidNextB not isolated")
	}
	if _, ok := snap.catalog.nodes[0x99]; ok {
		t.Fatal("snapshot catalog not isolated")
	}
	if snap.catalog.ids.nid[NIDTypeInternal] != origNid {
		t.Fatal("snapshot SequentialIDs not isolated")
	}
	if err := n.restore(snap); err != nil {
		t.Fatal(err)
	}
	if n.store.lastAllocAMap != origLast || n.store.bidNextB != origBid {
		t.Fatal("restore missed storeState")
	}
	if len(n.nodes) != origNodes {
		t.Fatalf("restore nodes %d", len(n.nodes))
	}
	if _, ok := n.nodes[0x99]; ok {
		t.Fatal("mutated node survived restore")
	}
}

func TestOwnershipLifecycleTable(t *testing.T) {
	t.Run("commit-to-mem-closes-stage", func(t *testing.T) {
		n, _, _ := seedNDB(t, []byte("own-mem"), false)
		var removed []string
		oldR := removeFile
		removeFile = func(p string) error {
			removed = append(removed, p)
			return oldR(p)
		}
		t.Cleanup(func() { removeFile = oldR })
		dst := NewMemSink("dst")
		if err := n.CommitTo(dst); err != nil {
			t.Fatal(err)
		}
		if len(removed) == 0 {
			t.Fatal("stage file not removed")
		}
		if err := dst.Close(); err != nil {
			t.Fatal(err)
		}
		assertNDBReadable(t, n, 0x21, "own-mem")
	})
	t.Run("borrowed-dest-stays-open", func(t *testing.T) {
		n, _, _ := seedNDB(t, []byte("own-file"), false)
		path := filepath.Join(t.TempDir(), "dst.pst")
		fs, err := CreateFileSink(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := n.CommitTo(fs); err != nil && !errors.Is(err, ErrCleanup) {
			t.Fatal(err)
		}
		if _, err := fs.WriteAt([]byte{1}, 0); err != nil {
			t.Fatalf("borrowed dest closed: %v", err)
		}
		_ = fs.Close()
	})
	t.Run("owned-file-closed-once", func(t *testing.T) {
		n, fs := openOwnedFileNDB(t, "owned-src")
		closes := trackSinkCloses(t, fs)
		if err := n.Close(); err != nil {
			t.Fatal(err)
		}
		if *closes != 1 {
			t.Fatalf("closed %d", *closes)
		}
	})
	t.Run("rollback-removes-work-spool", func(t *testing.T) {
		n := NewNDB(nil)
		var removed []string
		oldR := removeFile
		removeFile = func(p string) error {
			removed = append(removed, p)
			return oldR(p)
		}
		t.Cleanup(func() { removeFile = oldR })
		_, err := n.PutDataTree(&boomReader{left: 8}, 1<<20)
		if err == nil {
			t.Fatal("expected fail")
		}
		if n.store.work != nil && n.store.work.w != nil {
			t.Fatal("work spool survived rollback of new work")
		}
		if len(removed) == 0 {
			t.Fatal("created work spool not removed")
		}
	})
	t.Run("publish-failure-keeps-stage", func(t *testing.T) {
		n, _, _ := seedNDB(t, []byte("own-mem"), false)
		dst := NewMemSink("dst")
		err := n.CommitTo(&faultSink{Sink: dst, failWrite: 1})
		if err == nil {
			t.Fatal("expected publish failure")
		}
		if destIsIO(n, dst) {
			t.Fatal("adopted dest after publish failure")
		}
		assertNDBReadable(t, n, 0x21, "own-mem")
	})
	t.Run("cleanup-failure-discoverable", func(t *testing.T) {
		n, _, _ := seedNDB(t, []byte("own-mem"), false)
		errRemove := errors.New("rm-fail")
		oldR := removeFile
		removeFile = func(string) error { return errRemove }
		t.Cleanup(func() { removeFile = oldR })
		dst := NewMemSink("dst")
		err := n.CommitTo(dst)
		if !errors.Is(err, ErrCleanup) || !errors.Is(err, errRemove) {
			t.Fatalf("cleanup not discoverable: %v", err)
		}
		if !destIsIO(n, dst) {
			t.Fatal("dest not adopted after dest sync")
		}
		assertNDBReadable(t, n, 0x21, "own-mem")
	})
	t.Run("reopen-owned-closed-once", func(t *testing.T) {
		n, fs := openOwnedFileNDB(t, "reopen-src")
		closes := trackSinkCloses(t, fs)
		assertNDBReadable(t, n, 0x21, "reopen-src")
		if err := n.Close(); err != nil {
			t.Fatal(err)
		}
		if *closes != 1 {
			t.Fatalf("closed %d", *closes)
		}
	})
	t.Run("explicit-metadata-not-name", func(t *testing.T) {
		n, _, _ := seedNDB(t, []byte("meta"), false)
		path := filepath.Join(t.TempDir(), "stage.bin")
		f, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		stage := &metaSink{
			FileSink: newOwnedFile(f, sinkOwn{Life: lifeTemp, Remove: true, Path: path}),
			name:     "not-a-path",
		}
		old := newStageSink
		newStageSink = func() (Sink, error) { return stage, nil }
		t.Cleanup(func() { newStageSink = old })
		var removed []string
		oldR := removeFile
		removeFile = func(p string) error {
			removed = append(removed, p)
			return oldR(p)
		}
		t.Cleanup(func() { removeFile = oldR })
		oldE := encodeImage
		encodeImage = func(*NDB) (*TreeImage, error) { return nil, errors.New("encode-fail") }
		t.Cleanup(func() { encodeImage = oldE })
		err = n.CommitTo(NewMemSink("dst"))
		if err == nil {
			t.Fatal("expected encode failure")
		}
		found := false
		for _, p := range removed {
			if p == path {
				found = true
			}
		}
		if !found {
			t.Fatalf("removed %v, want %s (not Name %q)", removed, path, stage.Name())
		}
	})
}

type metaSink struct {
	*FileSink
	name string
}

func (s *metaSink) Name() string { return s.name }
func (s *metaSink) SinkOwn() sinkOwn {
	return s.FileSink.SinkOwn()
}

func (f *faultSink) SinkOwn() sinkOwn {
	return ownershipOf(f.Sink)
}
