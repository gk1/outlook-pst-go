package writer

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
	"time"

	outlookpst "github.com/grokify/outlook-pst-go"

	"github.com/grokify/outlook-pst-go/writer/v2/interop"
)

func requiredNIDs(ipm, waste, finder uint32) []uint32 {
	out := []uint32{
		NIDMessageStore,
		NIDNameToIDMap,
		NIDNormalFolderTemplate,
		NIDSearchFolderTemplate,
		NIDRootFolder,
		NIDSearchManagementQueue,
		NIDSearchActivityList,
		NIDSearchDomainObject,
		NIDSearchGathererQueue,
		NIDSearchGathererDescriptor,
		NIDSearchGathererFolderQueue,
		RelatedNID(NIDMessageStore, NIDTypeReceiveFolderTable),
		RelatedNID(NIDMessageStore, NIDTypeOutgoingQueueTable),
	}
	for _, f := range []uint32{NIDRootFolder, ipm, waste, finder} {
		out = append(out, f,
			RelatedNID(f, NIDTypeHierarchyTable),
			RelatedNID(f, NIDTypeContentsTable),
			RelatedNID(f, NIDTypeAssocContentsTable),
		)
	}
	return out
}

func writeBlank(t *testing.T, spec MinimumSpec) (string, *NDB) {
	t.Helper()
	n := NewNDB(nil)
	if err := WriteMinimum(n, spec); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "blank.pst")
	if err := n.CommitFile(path); err != nil {
		t.Fatal(err)
	}
	return path, n
}

func TestMinimumPSTRequiredNodesAndRelatedIndex(t *testing.T) {
	spec := MinimumSpec{DisplayName: "Min Store", Now: time.Unix(1_700_000_000, 0).UTC()}
	path, n := writeBlank(t, spec)
	ipm := MakeNID(NIDTypeNormalFolder, NIDIndexDefault)
	waste := MakeNID(NIDTypeNormalFolder, NIDIndexDefault+1)
	finder := MakeNID(NIDTypeSearchFolder, NIDIndexSearchFolder)
	if NIDTypeOf(finder) != NIDTypeSearchFolder {
		t.Fatalf("finder type 0x%x", NIDTypeOf(finder))
	}
	for _, nid := range requiredNIDs(ipm, waste, finder) {
		if _, ok := n.LookupNode(uint64(nid)); !ok {
			t.Fatalf("missing NID 0x%x", nid)
		}
	}
	for _, f := range []uint32{NIDRootFolder, ipm, waste, finder} {
		idx := NIDIndexOf(f)
		if NIDIndexOf(RelatedNID(f, NIDTypeHierarchyTable)) != idx {
			t.Fatalf("hierarchy nidIndex for 0x%x", f)
		}
		if NIDIndexOf(RelatedNID(f, NIDTypeContentsTable)) != idx {
			t.Fatalf("contents nidIndex for 0x%x", f)
		}
		if NIDIndexOf(RelatedNID(f, NIDTypeAssocContentsTable)) != idx {
			t.Fatalf("FAI nidIndex for 0x%x", f)
		}
		e, _ := n.LookupNode(uint64(RelatedNID(f, NIDTypeHierarchyTable)))
		if e.ParentNID != f {
			t.Fatalf("hierarchy parent 0x%x want 0x%x", e.ParentNID, f)
		}
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	h, err := InspectHeader(raw[:UnicodeHeaderSize])
	if err != nil {
		t.Fatal(err)
	}
	if h.NIDs[NIDTypeNormalFolder] != NIDIndexDefault+2 {
		t.Fatalf("rgnid folder %d want %d", h.NIDs[NIDTypeNormalFolder], NIDIndexDefault+2)
	}
	if h.NIDs[NIDTypeSearchFolder] != NIDIndexSearchFolder+1 {
		t.Fatalf("rgnid search-folder %d want %d", h.NIDs[NIDTypeSearchFolder], NIDIndexSearchFolder+1)
	}
	if h.NIDs[NIDTypeHierarchyTable] != NIDIndexSearchFolder+1 {
		t.Fatalf("rgnid hierarchy %d want %d", h.NIDs[NIDTypeHierarchyTable], NIDIndexSearchFolder+1)
	}
	if h.NIDs[NIDTypeNormalMessage] != NIDIndexNormalMessage {
		t.Fatalf("rgnid message %d", h.NIDs[NIDTypeNormalMessage])
	}
}

func TestMinimumPSTMessageStoreAndReaderHierarchy(t *testing.T) {
	spec := MinimumSpec{DisplayName: "Min Store", Now: time.Unix(1_700_000_000, 0).UTC()}
	path, n := writeBlank(t, spec)
	pc, err := OpenPC(n, NIDMessageStore)
	if err != nil {
		t.Fatal(err)
	}
	name, err := pc.GetString(PidTagDisplayName)
	if err != nil || name != spec.DisplayName {
		t.Fatalf("store name %q %v", name, err)
	}
	_, ipmEID, err := pc.Get(PidTagIpmSubTreeEntryId)
	if err != nil || len(ipmEID) != EntryIDSize {
		t.Fatalf("ipm eid %d %v", len(ipmEID), err)
	}
	ipm := binary.LittleEndian.Uint32(ipmEID[20:])
	if NIDTypeOf(ipm) != NIDTypeNormalFolder {
		t.Fatalf("ipm nid 0x%x", ipm)
	}
	_, wasteEID, err := pc.Get(PidTagIpmWastebasketEntryId)
	if err != nil {
		t.Fatal(err)
	}
	waste := binary.LittleEndian.Uint32(wasteEID[20:])
	if NIDTypeOf(waste) != NIDTypeNormalFolder {
		t.Fatalf("waste nid 0x%x", waste)
	}
	_, finderEID, err := pc.Get(PidTagFinderEntryId)
	if err != nil {
		t.Fatal(err)
	}
	finder := binary.LittleEndian.Uint32(finderEID[20:])
	if NIDTypeOf(finder) != NIDTypeSearchFolder || NIDIndexOf(finder) != NIDIndexSearchFolder {
		t.Fatalf("finder nid 0x%x type 0x%x index 0x%x", finder, NIDTypeOf(finder), NIDIndexOf(finder))
	}
	if bytes.Equal(ipmEID, wasteEID) || bytes.Equal(ipmEID, finderEID) {
		t.Fatal("EntryIDs must name distinct folders")
	}

	p, err := outlookpst.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	gotName, err := p.Name()
	if err != nil || gotName != spec.DisplayName {
		t.Fatalf("reader name %q %v", gotName, err)
	}
	root, err := p.RootFolder()
	if err != nil {
		t.Fatal(err)
	}
	nSub, err := root.SubfolderCount()
	if err != nil || nSub != 3 {
		t.Fatalf("root children %d %v", nSub, err)
	}
	ipmF, err := root.FindSubfolder(spec.DisplayName)
	if err != nil {
		t.Fatal(err)
	}
	wasteF, err := root.FindSubfolder("Deleted Items")
	if err != nil {
		t.Fatal(err)
	}
	finderF, err := root.FindSubfolder("Search Root")
	if err != nil {
		t.Fatal(err)
	}
	if ipmF.IsSearchFolder() || wasteF.IsSearchFolder() {
		t.Fatal("IPM and Deleted Items must stay normal folders")
	}
	if !finderF.IsSearchFolder() {
		t.Fatal("Search Root must be NIDTypeSearchFolder")
	}
	if _, err := ipmF.SubfolderCount(); err != nil {
		t.Fatal(err)
	}
	if _, err := p.NamedPropertyMap(); err != nil {
		t.Fatal(err)
	}
}

func TestMinimumPSTDeterministic(t *testing.T) {
	spec := MinimumSpec{DisplayName: "Min Store", Now: time.Unix(1_700_000_000, 0).UTC(), RecordKey: [16]byte{1, 2, 3, 4}}
	a := NewMemSink("a")
	b := NewMemSink("b")
	na, nb := NewNDB(nil), NewNDB(nil)
	if err := WriteMinimum(na, spec); err != nil {
		t.Fatal(err)
	}
	if err := WriteMinimum(nb, spec); err != nil {
		t.Fatal(err)
	}
	if err := na.CommitTo(a); err != nil {
		t.Fatal(err)
	}
	if err := nb.CommitTo(b); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a.Bytes(), b.Bytes()) {
		t.Fatalf("blank PST bytes differ %d vs %d", len(a.Bytes()), len(b.Bytes()))
	}
}

func TestFinalizeWritesBlankPST(t *testing.T) {
	sink := NewMemSink("finalize")
	exp, err := New(Options{Sink: sink, DisplayName: "Min Store", Clock: FixedClock{T: time.Unix(1_700_000_000, 0).UTC()}})
	if err != nil {
		t.Fatal(err)
	}
	defer exp.Close()
	if err := exp.CreateMailbox(Mailbox{DisplayName: "Min Store"}); err != nil {
		t.Fatal(err)
	}
	if err := exp.Finalize(context.Background()); err != nil {
		t.Fatal(err)
	}
	raw := sink.Bytes()
	if len(raw) < UnicodeHeaderSize {
		t.Fatalf("short file %d", len(raw))
	}
	path := filepath.Join(t.TempDir(), "finalize.pst")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := outlookpst.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	root, err := p.RootFolder()
	if err != nil {
		t.Fatal(err)
	}
	nSub, err := root.SubfolderCount()
	if err != nil || nSub != 3 {
		t.Fatalf("root children %d %v", nSub, err)
	}
}

func TestMinimumPSTInterop(t *testing.T) {
	spec := MinimumSpec{DisplayName: "Min Store", Now: time.Unix(1_700_000_000, 0).UTC()}
	path, _ := writeBlank(t, spec)
	m, err := interop.Run(context.Background(), path, interop.Options{})
	if err != nil {
		t.Fatal(err)
	}
	var self, libpff bool
	for _, r := range m.Results {
		switch r.Tool {
		case interop.ToolSelfReader:
			self = true
			if r.Status != interop.StatusPass {
				t.Fatalf("self reader %s %s", r.Status, r.Log)
			}
		case interop.ToolLibpff:
			if r.Status == interop.StatusFail || r.Status == interop.StatusError {
				t.Fatalf("libpff %s %s", r.Status, r.Log)
			}
			libpff = r.Status == interop.StatusPass
		case interop.ToolOutlook, interop.ToolScanPST:
			if r.Status != interop.StatusSkipped && r.Status != interop.StatusPass {
				t.Fatalf("%s %s %s", r.Tool, r.Status, r.Log)
			}
		}
	}
	if !self {
		t.Fatal("missing self-reader result")
	}
	t.Logf("libpff pass=%v outlook/scanpst recorded as skip unless present", libpff)
}
