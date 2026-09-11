package writer

import (
	"encoding/binary"
	"strings"
	"time"
)

// FolderTree mutates folder PCs and their related hierarchy/contents/FAI
// tables as one object. See MS-PST 2.4.4.
type FolderTree struct {
	n         *NDB
	recordKey [16]byte
	now       time.Time
	ipm       uint32
	waste     uint32
	finder    uint32
	limits    Limits
}

// OpenFolderTree reads special-folder EntryIDs from the message store.
func OpenFolderTree(n *NDB, now time.Time) (*FolderTree, error) {
	if n == nil {
		return nil, invalidArg("ndb", "nil NDB")
	}
	if now.IsZero() {
		now = time.Unix(0, 0).UTC()
	}
	pc, err := OpenPC(n, NIDMessageStore)
	if err != nil {
		return nil, err
	}
	key, err := pc.GetBinary(PidTagRecordKey)
	if err != nil {
		return nil, err
	}
	var rk [16]byte
	copy(rk[:], key)
	ipmEID, err := pc.GetBinary(PidTagIpmSubTreeEntryId)
	if err != nil || len(ipmEID) != EntryIDSize {
		return nil, invalidArg("PidTagIpmSubTreeEntryId", "missing IPM EntryID")
	}
	wasteEID, err := pc.GetBinary(PidTagIpmWastebasketEntryId)
	if err != nil || len(wasteEID) != EntryIDSize {
		return nil, invalidArg("PidTagIpmWastebasketEntryId", "missing wastebasket EntryID")
	}
	finderEID, err := pc.GetBinary(PidTagFinderEntryId)
	if err != nil || len(finderEID) != EntryIDSize {
		return nil, invalidArg("PidTagFinderEntryId", "missing finder EntryID")
	}
	return &FolderTree{
		n:         n,
		recordKey: rk,
		now:       now,
		ipm:       binary.LittleEndian.Uint32(ipmEID[20:]),
		waste:     binary.LittleEndian.Uint32(wasteEID[20:]),
		finder:    binary.LittleEndian.Uint32(finderEID[20:]),
		limits:    DefaultLimits(),
	}, nil
}

func (t *FolderTree) IPM() uint32    { return t.ipm }
func (t *FolderTree) Waste() uint32  { return t.waste }
func (t *FolderTree) Finder() uint32 { return t.finder }

func validateFolderName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", invalidArg("name", "folder name is required")
	}
	if strings.ContainsAny(name, `/\`) {
		return "", invalidArg("name", "folder name %q contains a path separator", name)
	}
	return name, nil
}

func (t *FolderTree) parentOf(nid uint32) uint32 {
	e, ok := t.n.LookupNode(uint64(nid))
	if !ok {
		return 0
	}
	return e.ParentNID
}

func (t *FolderTree) protected(nid uint32) bool {
	if nid == NIDRootFolder || nid == t.ipm || nid == t.waste || nid == t.finder {
		return true
	}
	return NIDTypeOf(nid) == NIDTypeSearchFolder
}

func (t *FolderTree) canHoldChildren(nid uint32) error {
	if _, ok := t.n.LookupNode(uint64(nid)); !ok {
		return invalidArg("parent", "missing folder 0x%x", nid)
	}
	if nid == t.finder || NIDTypeOf(nid) == NIDTypeSearchFolder {
		return unsupported(FeatureSearchFolder, "cannot place folders under a search folder")
	}
	if nid != NIDRootFolder && NIDTypeOf(nid) != NIDTypeNormalFolder {
		return invalidArg("parent", "NID 0x%x is not a folder", nid)
	}
	return nil
}

func (t *FolderTree) contains(ancestor, nid uint32) bool {
	seen := map[uint32]struct{}{}
	for cur := nid; cur != 0; {
		if cur == ancestor {
			return true
		}
		if _, ok := seen[cur]; ok {
			return false
		}
		seen[cur] = struct{}{}
		next := t.parentOf(cur)
		if next == cur {
			return false
		}
		cur = next
	}
	return false
}

func (t *FolderTree) depthOf(nid uint32) int {
	d := 0
	seen := map[uint32]struct{}{}
	for cur := nid; cur != 0; {
		if _, ok := seen[cur]; ok {
			break
		}
		seen[cur] = struct{}{}
		d++
		cur = t.parentOf(cur)
	}
	return d
}

func (t *FolderTree) folderCount() int {
	n := 0
	for nid := range t.n.nodes {
		typ := NIDTypeOf(uint32(nid))
		if typ == NIDTypeNormalFolder || typ == NIDTypeSearchFolder {
			n++
		}
	}
	return n
}

func (t *FolderTree) collision(parent uint32, name string, except uint32) error {
	hv, err := OpenTC(t.n, RelatedNID(parent, NIDTypeHierarchyTable))
	if err != nil {
		return err
	}
	ids, err := hv.RowIDs()
	if err != nil {
		return err
	}
	for _, id := range ids {
		if id == except {
			continue
		}
		got, err := hv.GetString(id, PidTagDisplayName)
		if err != nil {
			return err
		}
		if strings.EqualFold(got, name) {
			return invalidArg("name", "folder %q already exists under parent 0x%x (MS-PST %s)", name, parent, SectionFolder)
		}
	}
	return nil
}

func (t *FolderTree) patchPC(nid uint32, fn func(*PC) error) error {
	e, ok := t.n.LookupNode(uint64(nid))
	if !ok {
		return invalidArg("nid", "missing NID 0x%x", nid)
	}
	v, err := OpenPC(t.n, nid)
	if err != nil {
		return err
	}
	p, err := v.Load()
	if err != nil {
		return err
	}
	if err := fn(p); err != nil {
		return err
	}
	if err := p.Commit(nid); err != nil {
		return err
	}
	if e.ParentNID != 0 {
		return t.n.SetParent(nid, e.ParentNID)
	}
	return nil
}

func (t *FolderTree) patchTC(nid uint32, fn func(*TC) error) error {
	e, ok := t.n.LookupNode(uint64(nid))
	if !ok {
		return invalidArg("nid", "missing table 0x%x", nid)
	}
	v, err := OpenTC(t.n, nid)
	if err != nil {
		return err
	}
	tc, err := v.Load()
	if err != nil {
		return err
	}
	if err := fn(tc); err != nil {
		return err
	}
	if err := tc.Commit(nid); err != nil {
		return err
	}
	if e.ParentNID != 0 {
		return t.n.SetParent(nid, e.ParentNID)
	}
	return nil
}

func (t *FolderTree) rowState(nid uint32) (minFolder, error) {
	v, err := OpenPC(t.n, nid)
	if err != nil {
		return minFolder{}, err
	}
	name, err := v.GetString(PidTagDisplayName)
	if err != nil {
		return minFolder{}, err
	}
	content, err := v.GetInt32(PidTagContentCount)
	if err != nil {
		return minFolder{}, err
	}
	unread, err := v.GetInt32(PidTagContentUnreadCount)
	if err != nil {
		return minFolder{}, err
	}
	hv, err := OpenTC(t.n, RelatedNID(nid, NIDTypeHierarchyTable))
	if err != nil {
		return minFolder{}, err
	}
	eid, err := v.GetBinary(PidTagEntryId)
	if err != nil {
		return minFolder{}, err
	}
	created, err := v.GetTime(PidTagCreationTime)
	if err != nil {
		return minFolder{}, err
	}
	modified, err := v.GetTime(PidTagLastModificationTime)
	if err != nil {
		return minFolder{}, err
	}
	return minFolder{
		nid:        nid,
		parent:     t.parentOf(nid),
		name:       name,
		content:    content,
		unread:     unread,
		subfolders: hv.RowCount() > 0,
		eid:        eid,
		created:    created,
		modified:   modified,
	}, nil
}

func (t *FolderTree) upsertChildRow(parent, child uint32) error {
	st, err := t.rowState(child)
	if err != nil {
		return err
	}
	return t.patchTC(RelatedNID(parent, NIDTypeHierarchyTable), func(tc *TC) error {
		return writeHierarchyRow(tc, st)
	})
}

// mutatePC updates a folder PC then rewrites its parent hierarchy row from
// that PC so EntryID, timestamps, name, and counts stay in lockstep.
func (t *FolderTree) mutatePC(nid uint32, fn func(*PC) error) error {
	if err := t.patchPC(nid, fn); err != nil {
		return err
	}
	parent := t.parentOf(nid)
	if parent == 0 {
		return nil
	}
	return t.upsertChildRow(parent, nid)
}

func (t *FolderTree) removeChildRow(parent, child uint32) error {
	return t.patchTC(RelatedNID(parent, NIDTypeHierarchyTable), func(tc *TC) error {
		return tc.Delete(child)
	})
}

func (t *FolderTree) syncFlags(nid uint32) error {
	hv, err := OpenTC(t.n, RelatedNID(nid, NIDTypeHierarchyTable))
	if err != nil {
		return err
	}
	has := hv.RowCount() > 0
	if err := t.patchPC(nid, func(p *PC) error {
		if err := p.SetBool(PidTagSubfolders, has); err != nil {
			return err
		}
		return p.SetTime(PidTagLastModificationTime, filetimeOf(t.now))
	}); err != nil {
		return err
	}
	parent := t.parentOf(nid)
	if parent == 0 {
		return nil
	}
	return t.upsertChildRow(parent, nid)
}

// Create allocates a normal folder under parent with related tables and a
// hierarchy row. Related NIDs share nidIndex (MS-PST 2.4.4.6).
func (t *FolderTree) Create(parent uint32, name string) (uint32, error) {
	var nid uint32
	err := t.n.runTxn(func() error {
		var err error
		nid, err = t.createLocked(parent, name)
		return err
	})
	return nid, err
}

func (t *FolderTree) createLocked(parent uint32, name string) (uint32, error) {
	name, err := validateFolderName(name)
	if err != nil {
		return 0, err
	}
	if err := t.canHoldChildren(parent); err != nil {
		return 0, err
	}
	if err := t.collision(parent, name, 0); err != nil {
		return 0, err
	}
	if t.depthOf(parent)+1 > t.limits.MaxFolderDepth {
		return 0, limitErr("MaxFolderDepth", "depth %d exceeds %d", t.depthOf(parent)+1, t.limits.MaxFolderDepth)
	}
	if t.folderCount() >= t.limits.MaxFolders {
		return 0, limitErr("MaxFolders", "folder count exceeds %d", t.limits.MaxFolders)
	}
	ids := t.n.ids
	if ids == nil {
		return 0, invalidArg("ids", "NDB has no SequentialIDs")
	}
	nid := ids.allocFolder(NIDTypeNormalFolder)
	f := minFolder{nid: nid, parent: parent, name: name, class: "IPF.Note"}
	if err := writeFolderPC(t.n, f, encodeEntryID(t.recordKey, nid), filetimeOf(t.now)); err != nil {
		return 0, err
	}
	if err := writeEmptyTC(t.n, RelatedNID(nid, NIDTypeHierarchyTable), nid, hierarchyColumns()); err != nil {
		return 0, err
	}
	if err := writeEmptyTC(t.n, RelatedNID(nid, NIDTypeContentsTable), nid, contentsColumns()); err != nil {
		return 0, err
	}
	if err := writeEmptyTC(t.n, RelatedNID(nid, NIDTypeAssocContentsTable), nid, contentsColumns()); err != nil {
		return 0, err
	}
	if err := t.upsertChildRow(parent, nid); err != nil {
		return 0, err
	}
	if err := t.syncFlags(parent); err != nil {
		return 0, err
	}
	return nid, nil
}

// Rename changes PidTagDisplayName on the folder PC and its parent hierarchy
// row. Unknown PC properties are preserved.
func (t *FolderTree) Rename(nid uint32, name string) error {
	return t.n.runTxn(func() error { return t.renameLocked(nid, name) })
}

func (t *FolderTree) renameLocked(nid uint32, name string) error {
	name, err := validateFolderName(name)
	if err != nil {
		return err
	}
	if t.protected(nid) {
		return invalidArg("nid", "cannot rename special folder 0x%x", nid)
	}
	if _, ok := t.n.LookupNode(uint64(nid)); !ok {
		return invalidArg("nid", "missing folder 0x%x", nid)
	}
	parent := t.parentOf(nid)
	if parent == 0 {
		return invalidArg("nid", "folder 0x%x has no parent", nid)
	}
	if err := t.collision(parent, name, nid); err != nil {
		return err
	}
	return t.mutatePC(nid, func(p *PC) error {
		if err := p.SetString(PidTagDisplayName, name); err != nil {
			return err
		}
		return p.SetTime(PidTagLastModificationTime, filetimeOf(t.now))
	})
}

// Move reparents a folder, updating both hierarchy tables and subfolder flags.
func (t *FolderTree) Move(nid, newParent uint32) error {
	return t.n.runTxn(func() error { return t.moveLocked(nid, newParent) })
}

func (t *FolderTree) moveLocked(nid, newParent uint32) error {
	if t.protected(nid) {
		return invalidArg("nid", "cannot move special folder 0x%x", nid)
	}
	if _, ok := t.n.LookupNode(uint64(nid)); !ok {
		return invalidArg("nid", "missing folder 0x%x", nid)
	}
	if err := t.canHoldChildren(newParent); err != nil {
		return err
	}
	if t.contains(nid, newParent) {
		return invalidArg("parent", "cannot move folder 0x%x under itself or a descendant 0x%x", nid, newParent)
	}
	oldParent := t.parentOf(nid)
	if oldParent == 0 {
		return invalidArg("nid", "folder 0x%x has no parent", nid)
	}
	if oldParent == newParent {
		return nil
	}
	st, err := t.rowState(nid)
	if err != nil {
		return err
	}
	if err := t.collision(newParent, st.name, 0); err != nil {
		return err
	}
	if t.depthOf(newParent)+1 > t.limits.MaxFolderDepth {
		return limitErr("MaxFolderDepth", "move would exceed depth %d", t.limits.MaxFolderDepth)
	}
	if err := t.removeChildRow(oldParent, nid); err != nil {
		return err
	}
	if err := t.n.SetParent(nid, newParent); err != nil {
		return err
	}
	if err := t.mutatePC(nid, func(p *PC) error {
		return p.SetTime(PidTagLastModificationTime, filetimeOf(t.now))
	}); err != nil {
		return err
	}
	if err := t.syncFlags(oldParent); err != nil {
		return err
	}
	return t.syncFlags(newParent)
}

// Delete removes a folder, its descendants, and related table nodes.
// Non-empty contents or FAI tables are rejected (messages are PST-013).
func (t *FolderTree) Delete(nid uint32) error {
	return t.n.runTxn(func() error { return t.deleteLocked(nid) })
}

func (t *FolderTree) deleteLocked(nid uint32) error {
	if t.protected(nid) {
		return invalidArg("nid", "cannot delete special folder 0x%x", nid)
	}
	if _, ok := t.n.LookupNode(uint64(nid)); !ok {
		return invalidArg("nid", "missing folder 0x%x", nid)
	}
	parent := t.parentOf(nid)
	hv, err := OpenTC(t.n, RelatedNID(nid, NIDTypeHierarchyTable))
	if err != nil {
		return err
	}
	kids, err := hv.RowIDs()
	if err != nil {
		return err
	}
	for _, child := range kids {
		if err := t.deleteLocked(child); err != nil {
			return err
		}
	}
	cv, err := OpenTC(t.n, RelatedNID(nid, NIDTypeContentsTable))
	if err != nil {
		return err
	}
	if cv.RowCount() != 0 {
		return invalidArg("contents", "folder 0x%x still has %d message rows", nid, cv.RowCount())
	}
	fv, err := OpenTC(t.n, RelatedNID(nid, NIDTypeAssocContentsTable))
	if err != nil {
		return err
	}
	if fv.RowCount() != 0 {
		return invalidArg("fai", "folder 0x%x still has %d FAI rows", nid, fv.RowCount())
	}
	if parent != 0 {
		if err := t.removeChildRow(parent, nid); err != nil {
			return err
		}
	}
	for _, rel := range []byte{NIDTypeHierarchyTable, NIDTypeContentsTable, NIDTypeAssocContentsTable} {
		if err := t.n.DeleteNode(uint64(RelatedNID(nid, rel))); err != nil {
			return err
		}
	}
	if err := t.n.DeleteNode(uint64(nid)); err != nil {
		return err
	}
	if parent != 0 {
		return t.syncFlags(parent)
	}
	return nil
}

// CreateFromPlan materializes user folders from a v2 export plan onto a
// WriteMinimum skeleton. Root and IPM already exist.
func (t *FolderTree) CreateFromPlan(folders []PlannedFolder) error {
	nids := map[FolderRef]uint32{
		RootFolderRef: NIDRootFolder,
		IPMSubtreeRef: t.ipm,
	}
	pending := make([]PlannedFolder, 0, len(folders))
	for _, f := range folders {
		if f.Ref == RootFolderRef || f.Ref == IPMSubtreeRef {
			continue
		}
		pending = append(pending, f)
	}
	for len(pending) > 0 {
		progress := 0
		next := pending[:0]
		for _, f := range pending {
			parent, ok := nids[f.Parent]
			if !ok {
				next = append(next, f)
				continue
			}
			nid, err := t.Create(parent, f.Name)
			if err != nil {
				return err
			}
			nids[f.Ref] = nid
			progress++
		}
		if progress == 0 {
			return invalidArg("parent", "folder plan has a missing parent or cycle")
		}
		pending = next
	}
	return nil
}
