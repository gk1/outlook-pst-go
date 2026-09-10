package writer

import (
	"bytes"
	"errors"
	"io"
	"path/filepath"
	"testing"
)

func TestHIDPacking(t *testing.T) {
	hid := MakeHID(3, 0)
	if HIDBlock(hid) != 3 || HIDIndex(hid) != 1 {
		t.Fatalf("hid 0x%x block %d index %d", hid, HIDBlock(hid), HIDIndex(hid))
	}
	if !IsHID(hid) {
		t.Fatal("HID not recognized")
	}
	nid := MakeNID(NIDTypeLTP, 1)
	if IsHID(nid) {
		t.Fatal("LTP NID treated as HID")
	}
}

func TestHNFirstPageRoundTrip(t *testing.T) {
	n := NewNDB(nil)
	h := NewHeap(n, HeapSigPC)
	hid, err := h.Allocate([]byte("root-bth"))
	if err != nil {
		t.Fatal(err)
	}
	h.SetRoot(hid)
	a, err := h.Allocate([]byte{1, 2, 3, 4})
	if err != nil {
		t.Fatal(err)
	}
	if HIDBlock(hid) != 0 || HIDIndex(hid) != 1 {
		t.Fatalf("root hid 0x%x", hid)
	}
	if HIDBlock(a) != 0 || HIDIndex(a) != 2 {
		t.Fatalf("second hid 0x%x", a)
	}
	if err := h.Commit(MakeNID(NIDTypeInternal, 0x10)); err != nil {
		t.Fatal(err)
	}
	v, err := OpenHeap(n, MakeNID(NIDTypeInternal, 0x10))
	if err != nil {
		t.Fatal(err)
	}
	if v.Pages() != 1 || v.ClientSig() != HeapSigPC || v.Root() != hid {
		t.Fatalf("view pages=%d sig=%x root=0x%x", v.Pages(), v.ClientSig(), v.Root())
	}
	if v.pages[0].CAlloc != 2 || v.pages[0].CFree != 0 {
		t.Fatalf("pagemap %+v", v.pages[0])
	}
	got, err := v.Read(hid)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "root-bth" {
		t.Fatalf("root %q", got)
	}
	got, err = v.Read(a)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, []byte{1, 2, 3, 4}) {
		t.Fatalf("alloc %x", got)
	}
}

func TestHNPageTransitionsAndBitmap(t *testing.T) {
	n := NewNDB(nil)
	h := NewHeap(n, HeapSigTC)
	chunk := bytes.Repeat([]byte{0xAB}, 3500)
	var hids []uint32
	for i := 0; i < 18; i++ {
		hid, err := h.Allocate(bytes.Repeat([]byte{byte(i + 1)}, 3500))
		if err != nil {
			t.Fatal(err)
		}
		hids = append(hids, hid)
	}
	if HIDBlock(hids[0]) != 0 || HIDBlock(hids[2]) != 1 {
		t.Fatalf("expected page transition, got block %d then %d", HIDBlock(hids[0]), HIDBlock(hids[2]))
	}
	last := hids[len(hids)-1]
	if HIDBlock(last) < 8 {
		t.Fatalf("want bitmap page, last block %d", HIDBlock(last))
	}
	nid := MakeNID(NIDTypeInternal, 0x11)
	h.SetRoot(hids[0])
	if err := h.Commit(nid); err != nil {
		t.Fatal(err)
	}
	v, err := OpenHeap(n, nid)
	if err != nil {
		t.Fatal(err)
	}
	if v.Pages() < 9 {
		t.Fatalf("pages %d want >=9", v.Pages())
	}
	if v.pages[0].IbHnpm < HNHDRSize {
		t.Fatal("first page missing HNHDR")
	}
	if v.pages[1].IbHnpm < HNPAGEHDRSize {
		t.Fatal("normal page missing HNPAGEHDR")
	}
	if !hnBitmapPage(8) || v.pages[8].Fill == nil || len(v.pages[8].Fill) != 64 {
		t.Fatalf("page 8 is not HNBITMAPHDR: fill=%d", len(v.pages[8].Fill))
	}
	for i, hid := range hids {
		got, err := v.Read(hid)
		if err != nil {
			t.Fatal(err)
		}
		want := bytes.Repeat([]byte{byte(i + 1)}, 3500)
		if !bytes.Equal(got, want) {
			t.Fatalf("hid %d mismatch", i)
		}
	}
	_ = chunk
	if v.pages[0].CFree != 0 {
		t.Fatalf("cFree %d", v.pages[0].CFree)
	}
}

func TestHNOversizedSubnode(t *testing.T) {
	n := NewNDB(nil)
	h := NewHeap(n, HeapSigPC)
	small, err := h.Allocate([]byte("tiny"))
	if err != nil {
		t.Fatal(err)
	}
	h.SetRoot(small)
	big := bytes.Repeat([]byte("Z"), HeapMaxAlloc+1)
	hnid, err := h.Allocate(big)
	if err != nil {
		t.Fatal(err)
	}
	if IsHID(hnid) {
		t.Fatalf("oversized stayed in-heap: 0x%x", hnid)
	}
	nid := MakeNID(NIDTypeInternal, 0x12)
	if err := h.Commit(nid); err != nil {
		t.Fatal(err)
	}
	v, err := OpenHeap(n, nid)
	if err != nil {
		t.Fatal(err)
	}
	got, err := v.Read(hnid)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, big) {
		t.Fatalf("subnode len %d", len(got))
	}
	tiny, err := v.Read(small)
	if err != nil {
		t.Fatal(err)
	}
	if string(tiny) != "tiny" {
		t.Fatalf("%q", tiny)
	}
}

func TestHNCommitReopenFile(t *testing.T) {
	n := NewNDB(nil)
	h := NewHeap(n, HeapSigBTH)
	var hids []uint32
	for i := 0; i < 10; i++ {
		hid, err := h.Allocate(bytes.Repeat([]byte{byte('A' + i)}, 3000))
		if err != nil {
			t.Fatal(err)
		}
		hids = append(hids, hid)
	}
	h.SetRoot(hids[0])
	big := bytes.Repeat([]byte{0x5A}, 9000)
	hnid, err := h.Allocate(big)
	if err != nil {
		t.Fatal(err)
	}
	nid := MakeNID(NIDTypeInternal, 0x13)
	if err := h.Commit(nid); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "hn.pst")
	if err := n.CommitFile(path); err != nil {
		t.Fatal(err)
	}
	_ = n.Close()
	n2, err := OpenNDBFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer n2.Close()
	v, err := OpenHeap(n2, nid)
	if err != nil {
		t.Fatal(err)
	}
	for i, hid := range hids {
		got, err := v.Read(hid)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, bytes.Repeat([]byte{byte('A' + i)}, 3000)) {
			t.Fatalf("page alloc %d", i)
		}
	}
	got, err := v.Read(hnid)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, big) {
		t.Fatal("large subnode mismatch after reopen")
	}
}

func TestDataTreeWalkerShared(t *testing.T) {
	n := NewNDB(nil)
	root, err := n.PutDataTree(bytes.NewReader(bytes.Repeat([]byte{0x11}, MaxDataBlockCB+200)), int64(MaxDataBlockCB+200))
	if err != nil {
		t.Fatal(err)
	}
	var walkLeaves []uint64
	total, err := n.walkDataTree(root.BID, func(bid uint64, _ uint16) error {
		walkLeaves = append(walkLeaves, bid)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	flatTotal, flatLeaves, err := n.flattenDataTree(root.BID)
	if err != nil {
		t.Fatal(err)
	}
	if total != flatTotal || len(walkLeaves) != len(flatLeaves) {
		t.Fatalf("walk %d/%d flatten %d/%d", total, len(walkLeaves), flatTotal, len(flatLeaves))
	}
	for i := range walkLeaves {
		if walkLeaves[i] != flatLeaves[i] {
			t.Fatalf("order %d: walk 0x%x flatten 0x%x", i, walkLeaves[i], flatLeaves[i])
		}
	}
	if err := n.validateDataTree(root.BID); err != nil {
		t.Fatal(err)
	}
	rd, err := n.OpenDataTree(root.BID)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.Copy(io.Discard, rd)
	if err != nil {
		t.Fatal(err)
	}
	if uint64(got) != total {
		t.Fatalf("read %d want %d", got, total)
	}

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
		if _, err := n.walkDataTree(xx.BID, nil); !errors.Is(err, ErrInvariant) {
			t.Fatalf("walk: %v", err)
		}
		if _, _, err := n.flattenDataTree(xx.BID); !errors.Is(err, ErrInvariant) {
			t.Fatalf("flatten: %v", err)
		}
		if err := n.validateDataTree(xx.BID); !errors.Is(err, ErrInvariant) {
			t.Fatalf("validate: %v", err)
		}
		rd, err := n.OpenDataTree(xx.BID)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.Copy(io.Discard, rd); !errors.Is(err, ErrInvariant) {
			t.Fatalf("readback: %v", err)
		}
	})
}
