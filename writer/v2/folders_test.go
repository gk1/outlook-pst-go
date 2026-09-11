package writer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	outlookpst "github.com/grokify/outlook-pst-go"

	"github.com/grokify/outlook-pst-go/writer/v2/interop"
)

func blankTree(t *testing.T) (*NDB, *FolderTree, string) {
	t.Helper()
	spec := MinimumSpec{DisplayName: "Min Store", Now: time.Unix(1_700_000_000, 0).UTC(), RecordKey: [16]byte{1, 2, 3, 4}}
	n := NewNDB(nil)
	if err := WriteMinimum(n, spec); err != nil {
		t.Fatal(err)
	}
	tree, err := OpenFolderTree(n, spec.Now)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "folders.pst")
	return n, tree, path
}

func commitTree(t *testing.T, n *NDB, path string) {
	t.Helper()
	if err := n.CommitFile(path); err != nil {
		t.Fatal(err)
	}
}

func relatedPresent(t *testing.T, n *NDB, folder uint32) {
	t.Helper()
	idx := NIDIndexOf(folder)
	for _, typ := range []byte{NIDTypeHierarchyTable, NIDTypeContentsTable, NIDTypeAssocContentsTable} {
		rel := RelatedNID(folder, typ)
		if NIDIndexOf(rel) != idx {
			t.Fatalf("related 0x%x nidIndex %d want %d", rel, NIDIndexOf(rel), idx)
		}
		e, ok := n.LookupNode(uint64(rel))
		if !ok {
			t.Fatalf("missing related 0x%x for folder 0x%x", rel, folder)
		}
		if e.ParentNID != folder {
			t.Fatalf("related 0x%x parent 0x%x want 0x%x", rel, e.ParentNID, folder)
		}
	}
}

func noStaleHierarchy(t *testing.T, n *NDB) {
	t.Helper()
	for nid := range n.nodes {
		if NIDTypeOf(uint32(nid)) != NIDTypeHierarchyTable {
			continue
		}
		v, err := OpenTC(n, uint32(nid))
		if err != nil {
			t.Fatal(err)
		}
		ids, err := v.RowIDs()
		if err != nil {
			t.Fatal(err)
		}
		for _, id := range ids {
			if _, ok := n.LookupNode(uint64(id)); !ok {
				t.Fatalf("hierarchy 0x%x row 0x%x has no folder node", nid, id)
			}
		}
	}
}

func TestFolderCreateMultiLevelRoundTrip(t *testing.T) {
	n, tree, path := blankTree(t)
	inbox, err := tree.Create(tree.IPM(), "Inbox")
	if err != nil {
		t.Fatal(err)
	}
	projects, err := tree.Create(inbox, "Projects")
	if err != nil {
		t.Fatal(err)
	}
	alpha, err := tree.Create(projects, "Alpha")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tree.Create(projects, "Beta"); err != nil {
		t.Fatal(err)
	}
	relatedPresent(t, n, inbox)
	relatedPresent(t, n, projects)
	relatedPresent(t, n, alpha)

	ipc, err := OpenPC(n, tree.IPM())
	if err != nil {
		t.Fatal(err)
	}
	has, err := ipc.GetBool(PidTagSubfolders)
	if err != nil || !has {
		t.Fatalf("IPM subfolders %v %v", has, err)
	}
	ih, err := OpenTC(n, RelatedNID(tree.IPM(), NIDTypeHierarchyTable))
	if err != nil {
		t.Fatal(err)
	}
	if ih.RowCount() != 1 {
		t.Fatalf("IPM hierarchy rows %d", ih.RowCount())
	}
	name, err := ih.GetString(inbox, PidTagDisplayName)
	if err != nil || name != "Inbox" {
		t.Fatalf("hierarchy name %q %v", name, err)
	}
	sub, err := ih.GetBool(inbox, PidTagSubfolders)
	if err != nil || !sub {
		t.Fatalf("Inbox row subfolders %v %v", sub, err)
	}
	rootRow, err := OpenTC(n, RelatedNID(NIDRootFolder, NIDTypeHierarchyTable))
	if err != nil {
		t.Fatal(err)
	}
	ipmSub, err := rootRow.GetBool(tree.IPM(), PidTagSubfolders)
	if err != nil || !ipmSub {
		t.Fatalf("root's IPM row subfolders %v %v", ipmSub, err)
	}

	e, ok := n.LookupNode(uint64(alpha))
	if !ok || e.ParentNID != projects {
		t.Fatalf("alpha parent 0x%x", e.ParentNID)
	}
	noStaleHierarchy(t, n)
	commitTree(t, n, path)

	p, err := outlookpst.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	root, err := p.RootFolder()
	if err != nil {
		t.Fatal(err)
	}
	ipm, err := root.FindSubfolder("Min Store")
	if err != nil {
		t.Fatal(err)
	}
	if n, err := ipm.SubfolderCount(); err != nil || n != 1 {
		t.Fatalf("IPM children %d %v", n, err)
	}
	in, err := ipm.FindSubfolder("Inbox")
	if err != nil {
		t.Fatal(err)
	}
	pr, err := in.FindSubfolder("Projects")
	if err != nil {
		t.Fatal(err)
	}
	if n, err := pr.SubfolderCount(); err != nil || n != 2 {
		t.Fatalf("Projects children %d %v", n, err)
	}
	if _, err := pr.FindSubfolder("Alpha"); err != nil {
		t.Fatal(err)
	}
	if _, err := pr.FindSubfolder("Beta"); err != nil {
		t.Fatal(err)
	}
}

func TestFolderRenamePreservesUnknownProperties(t *testing.T) {
	n, tree, path := blankTree(t)
	inbox, err := tree.Create(tree.IPM(), "Inbox")
	if err != nil {
		t.Fatal(err)
	}
	if err := tree.patchPC(inbox, func(p *PC) error {
		return p.SetString(PidTagComment, "keep-me")
	}); err != nil {
		t.Fatal(err)
	}
	eidPC, err := OpenPC(n, inbox)
	if err != nil {
		t.Fatal(err)
	}
	beforeEID, err := eidPC.GetBinary(PidTagEntryId)
	if err != nil {
		t.Fatal(err)
	}
	created, err := eidPC.GetTime(PidTagCreationTime)
	if err != nil {
		t.Fatal(err)
	}
	if err := tree.Rename(inbox, "Primary"); err != nil {
		t.Fatal(err)
	}
	pc, err := OpenPC(n, inbox)
	if err != nil {
		t.Fatal(err)
	}
	got, err := pc.GetString(PidTagComment)
	if err != nil || got != "keep-me" {
		t.Fatalf("comment %q %v", got, err)
	}
	name, err := pc.GetString(PidTagDisplayName)
	if err != nil || name != "Primary" {
		t.Fatalf("name %q %v", name, err)
	}
	afterEID, err := pc.GetBinary(PidTagEntryId)
	if err != nil {
		t.Fatal(err)
	}
	if string(beforeEID) != string(afterEID) {
		t.Fatal("EntryID changed on rename")
	}
	stillCreated, err := pc.GetTime(PidTagCreationTime)
	if err != nil || stillCreated != created {
		t.Fatalf("creation time mutated")
	}
	ih, err := OpenTC(n, RelatedNID(tree.IPM(), NIDTypeHierarchyTable))
	if err != nil {
		t.Fatal(err)
	}
	rowName, err := ih.GetString(inbox, PidTagDisplayName)
	if err != nil || rowName != "Primary" {
		t.Fatalf("hierarchy name %q %v", rowName, err)
	}
	commitTree(t, n, path)
	p, err := outlookpst.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	root, err := p.RootFolder()
	if err != nil {
		t.Fatal(err)
	}
	ipm, err := root.FindSubfolder("Min Store")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ipm.FindSubfolder("Primary"); err != nil {
		t.Fatal(err)
	}
	if _, err := ipm.FindSubfolder("Inbox"); err == nil {
		t.Fatal("old name still visible")
	}
}

func TestFolderMoveAndDeleteNoOrphans(t *testing.T) {
	n, tree, path := blankTree(t)
	inbox, err := tree.Create(tree.IPM(), "Inbox")
	if err != nil {
		t.Fatal(err)
	}
	archive, err := tree.Create(tree.IPM(), "Archive")
	if err != nil {
		t.Fatal(err)
	}
	proj, err := tree.Create(inbox, "Projects")
	if err != nil {
		t.Fatal(err)
	}
	alpha, err := tree.Create(proj, "Alpha")
	if err != nil {
		t.Fatal(err)
	}
	if err := tree.Move(proj, archive); err != nil {
		t.Fatal(err)
	}
	if e, _ := n.LookupNode(uint64(proj)); e.ParentNID != archive {
		t.Fatalf("projects parent 0x%x", e.ParentNID)
	}
	if e, _ := n.LookupNode(uint64(alpha)); e.ParentNID != proj {
		t.Fatalf("alpha parent 0x%x after ancestor move", e.ParentNID)
	}
	ih, err := OpenTC(n, RelatedNID(inbox, NIDTypeHierarchyTable))
	if err != nil {
		t.Fatal(err)
	}
	if ih.RowCount() != 0 {
		t.Fatalf("inbox still has %d children", ih.RowCount())
	}
	ipc, err := OpenPC(n, inbox)
	if err != nil {
		t.Fatal(err)
	}
	has, err := ipc.GetBool(PidTagSubfolders)
	if err != nil || has {
		t.Fatalf("inbox subfolders still %v", has)
	}
	ah, err := OpenTC(n, RelatedNID(archive, NIDTypeHierarchyTable))
	if err != nil {
		t.Fatal(err)
	}
	if ah.RowCount() != 1 {
		t.Fatalf("archive children %d", ah.RowCount())
	}
	noStaleHierarchy(t, n)

	if err := tree.Delete(archive); err != nil {
		t.Fatal(err)
	}
	for _, id := range []uint32{archive, proj, alpha} {
		if _, ok := n.LookupNode(uint64(id)); ok {
			t.Fatalf("folder 0x%x survived delete", id)
		}
		for _, typ := range []byte{NIDTypeHierarchyTable, NIDTypeContentsTable, NIDTypeAssocContentsTable} {
			if _, ok := n.LookupNode(uint64(RelatedNID(id, typ))); ok {
				t.Fatalf("related 0x%x survived delete", RelatedNID(id, typ))
			}
		}
	}
	if _, ok := n.LookupNode(uint64(inbox)); !ok {
		t.Fatal("inbox was deleted")
	}
	noStaleHierarchy(t, n)
	commitTree(t, n, path)
	p, err := outlookpst.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	root, err := p.RootFolder()
	if err != nil {
		t.Fatal(err)
	}
	ipm, err := root.FindSubfolder("Min Store")
	if err != nil {
		t.Fatal(err)
	}
	if n, err := ipm.SubfolderCount(); err != nil || n != 1 {
		t.Fatalf("IPM children after delete %d %v", n, err)
	}
	if _, err := ipm.FindSubfolder("Inbox"); err != nil {
		t.Fatal(err)
	}
	if _, err := ipm.FindSubfolder("Archive"); err == nil {
		t.Fatal("Archive still visible")
	}
}

func TestFolderInvalidOpsAndRollback(t *testing.T) {
	n, tree, _ := blankTree(t)
	inbox, err := tree.Create(tree.IPM(), "Inbox")
	if err != nil {
		t.Fatal(err)
	}
	child, err := tree.Create(inbox, "Child")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tree.Create(tree.IPM(), "inbox"); !errors.Is(err, ErrInvalidArg) {
		t.Fatalf("collision: %v", err)
	}
	if _, err := tree.Create(tree.Finder(), "Nope"); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("search parent: %v", err)
	}
	if err := tree.Rename(tree.IPM(), "X"); !errors.Is(err, ErrInvalidArg) {
		t.Fatalf("rename IPM: %v", err)
	}
	if err := tree.Rename(NIDRootFolder, "X"); !errors.Is(err, ErrInvalidArg) {
		t.Fatalf("rename root: %v", err)
	}
	if err := tree.Delete(tree.Waste()); !errors.Is(err, ErrInvalidArg) {
		t.Fatalf("delete waste: %v", err)
	}
	if err := tree.Move(inbox, child); !errors.Is(err, ErrInvalidArg) {
		t.Fatalf("move into descendant: %v", err)
	}
	if err := tree.Move(inbox, inbox); !errors.Is(err, ErrInvalidArg) {
		t.Fatalf("move onto self: %v", err)
	}
	if e, _ := n.LookupNode(uint64(inbox)); e.ParentNID != tree.IPM() {
		t.Fatalf("failed move mutated parent 0x%x", e.ParentNID)
	}
	ih, err := OpenTC(n, RelatedNID(tree.IPM(), NIDTypeHierarchyTable))
	if err != nil {
		t.Fatal(err)
	}
	if ih.RowCount() != 1 {
		t.Fatalf("IPM rows after failed move %d", ih.RowCount())
	}
	if _, err := tree.Create(tree.IPM(), "Inbox/Bad"); !errors.Is(err, ErrInvalidArg) {
		t.Fatalf("separator: %v", err)
	}
	before := tree.folderCount()
	if _, err := tree.Create(tree.IPM(), ""); !errors.Is(err, ErrInvalidArg) {
		t.Fatalf("empty name: %v", err)
	}
	if tree.folderCount() != before {
		t.Fatalf("empty create leaked a folder")
	}
}

func TestFolderExporterFinalizeVisibleTree(t *testing.T) {
	sink := NewMemSink("tree")
	exp, err := New(Options{Sink: sink, DisplayName: "Min Store", Clock: FixedClock{T: time.Unix(1_700_000_000, 0).UTC()}})
	if err != nil {
		t.Fatal(err)
	}
	defer exp.Close()
	if err := exp.CreateMailbox(Mailbox{DisplayName: "Min Store"}); err != nil {
		t.Fatal(err)
	}
	inbox, err := exp.CreateFolder(FolderSpec{Name: "Inbox"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := exp.CreateFolder(FolderSpec{Parent: inbox, Name: "Projects"}); err != nil {
		t.Fatal(err)
	}
	sent, err := exp.CreateFolder(FolderSpec{Name: "Sent"})
	if err != nil {
		t.Fatal(err)
	}
	if err := exp.RenameFolder(sent, "Sent Items"); err != nil {
		t.Fatal(err)
	}
	if _, err := exp.CreateFolder(FolderSpec{Name: "Inbox"}); !errors.Is(err, ErrInvalidArg) {
		t.Fatalf("plan collision: %v", err)
	}
	if err := exp.Finalize(context.Background()); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "export.pst")
	if err := os.WriteFile(path, sink.Bytes(), 0o600); err != nil {
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
	ipm, err := root.FindSubfolder("Min Store")
	if err != nil {
		t.Fatal(err)
	}
	if n, err := ipm.SubfolderCount(); err != nil || n != 2 {
		t.Fatalf("IPM children %d %v", n, err)
	}
	in, err := ipm.FindSubfolder("Inbox")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := in.FindSubfolder("Projects"); err != nil {
		t.Fatal(err)
	}
	if _, err := ipm.FindSubfolder("Sent Items"); err != nil {
		t.Fatal(err)
	}
}

func TestFolderTreeInterop(t *testing.T) {
	n, tree, path := blankTree(t)
	inbox, err := tree.Create(tree.IPM(), "Inbox")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tree.Create(inbox, "Projects"); err != nil {
		t.Fatal(err)
	}
	commitTree(t, n, path)
	m, err := interop.Run(context.Background(), path, interop.Options{})
	if err != nil {
		t.Fatal(err)
	}
	var self bool
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
		case interop.ToolOutlook, interop.ToolScanPST:
			if r.Status != interop.StatusSkipped && r.Status != interop.StatusPass {
				t.Fatalf("%s %s %s", r.Tool, r.Status, r.Log)
			}
		}
	}
	if !self {
		t.Fatal("missing self-reader result")
	}
}

func TestExporterDeleteAndMovePlan(t *testing.T) {
	exp, err := New(Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer exp.Close()
	a, err := exp.CreateFolder(FolderSpec{Name: "A"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := exp.CreateFolder(FolderSpec{Parent: a, Name: "B"})
	if err != nil {
		t.Fatal(err)
	}
	c, err := exp.CreateFolder(FolderSpec{Name: "C"})
	if err != nil {
		t.Fatal(err)
	}
	if err := exp.MoveFolder(b, c); err != nil {
		t.Fatal(err)
	}
	plan := exp.Plan()
	var got PlannedFolder
	for _, f := range plan.Folders {
		if f.Ref == b {
			got = f
		}
	}
	if got.Parent != c || got.Depth != 3 {
		t.Fatalf("moved folder %+v", got)
	}
	if err := exp.DeleteFolder(c); err != nil {
		t.Fatal(err)
	}
	for _, f := range exp.Plan().Folders {
		if f.Ref == c || f.Ref == b {
			t.Fatalf("deleted folder still in plan %+v", f)
		}
	}
	if err := exp.DeleteFolder(IPMSubtreeRef); !errors.Is(err, ErrInvalidArg) {
		t.Fatalf("delete IPM: %v", err)
	}
}
