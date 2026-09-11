package writer

import (
	"encoding/binary"
	"time"
)

// MinimumSpec is the deterministic input for a blank Unicode PST.
type MinimumSpec struct {
	DisplayName string
	Now         time.Time
	RecordKey   [16]byte
}

const filetimeEpochDiff int64 = 116444736000000000

func filetimeOf(t time.Time) uint64 {
	if t.IsZero() {
		return 0
	}
	return uint64(t.UnixNano()/100 + filetimeEpochDiff)
}

func encodeEntryID(uid [16]byte, nid uint32) []byte {
	b := make([]byte, EntryIDSize)
	copy(b[4:20], uid[:])
	binary.LittleEndian.PutUint32(b[20:], nid)
	return b
}

func (s *SequentialIDs) allocFolder(nidType byte) uint32 {
	nid := s.NextNID(nidType)
	next := NIDIndexOf(nid) + 1
	s.EnsureIndex(NIDTypeHierarchyTable, next)
	s.EnsureIndex(NIDTypeContentsTable, next)
	s.EnsureIndex(NIDTypeAssocContentsTable, next)
	return nid
}

func hierarchyColumns() []ColumnView {
	return []ColumnView{
		{PropType: PtypString, PropID: PidTagDisplayName},
		{PropType: PtypInteger32, PropID: PidTagContentCount},
		{PropType: PtypInteger32, PropID: PidTagContentUnreadCount},
		{PropType: PtypBoolean, PropID: PidTagSubfolders},
	}
}

func contentsColumns() []ColumnView {
	return []ColumnView{
		{PropType: PtypInteger32, PropID: PidTagImportance},
		{PropType: PtypString, PropID: PidTagMessageClass},
		{PropType: PtypString, PropID: PidTagSubject},
		{PropType: PtypTime, PropID: PidTagClientSubmitTime},
		{PropType: PtypString, PropID: PidTagSentRepresentingName},
		{PropType: PtypString, PropID: PidTagDisplayTo},
		{PropType: PtypTime, PropID: PidTagMessageDeliveryTime},
		{PropType: PtypInteger32, PropID: PidTagMessageFlags},
		{PropType: PtypInteger32, PropID: PidTagMessageSize},
		{PropType: PtypInteger32, PropID: PidTagMessageStatus},
		{PropType: PtypBoolean, PropID: PidTagHasAttachments},
	}
}

func receiveFolderColumns() []ColumnView {
	return []ColumnView{
		{PropType: PtypString, PropID: PidTagMessageClass},
		{PropType: PtypBinary, PropID: PidTagEntryId},
	}
}

type minFolder struct {
	nid        uint32
	parent     uint32
	name       string
	class      string
	subfolders bool
	content    int32
	unread     int32
	folderType int32
}

// WriteMinimum creates the MS-PST 2.4.1 / 2.7.1 nodes for a blank Unicode PST
// on n. The caller commits with a single CommitTo / CommitFile.
func WriteMinimum(n *NDB, spec MinimumSpec) error {
	if n == nil {
		return invalidArg("ndb", "nil NDB")
	}
	if spec.DisplayName == "" {
		spec.DisplayName = "openBackup Export"
	}
	if spec.Now.IsZero() {
		spec.Now = time.Unix(0, 0).UTC()
	}
	if spec.RecordKey == [16]byte{} {
		if n.ids != nil {
			spec.RecordKey = n.ids.NextRecordKey()
		} else {
			spec.RecordKey[15] = 1
		}
	}
	ft := filetimeOf(spec.Now)
	ids := n.ids
	if ids == nil {
		ids = NewSequentialIDs()
		n.ids = ids
	}

	ipm := ids.allocFolder(NIDTypeNormalFolder)
	waste := ids.allocFolder(NIDTypeNormalFolder)
	finder := ids.allocFolder(NIDTypeSearchFolder)

	ipmEID := encodeEntryID(spec.RecordKey, ipm)
	wasteEID := encodeEntryID(spec.RecordKey, waste)
	finderEID := encodeEntryID(spec.RecordKey, finder)
	rootEID := encodeEntryID(spec.RecordKey, NIDRootFolder)

	if err := writeNameToIDMap(n); err != nil {
		return err
	}
	if err := writeMessageStore(n, spec, ipmEID, wasteEID, finderEID); err != nil {
		return err
	}
	if err := writeEmptyTC(n, NIDNormalFolderTemplate, 0, hierarchyColumns()); err != nil {
		return err
	}
	if err := writeEmptyTC(n, NIDSearchFolderTemplate, 0, hierarchyColumns()); err != nil {
		return err
	}
	for _, nid := range []uint32{
		NIDSearchManagementQueue,
		NIDSearchActivityList,
		NIDSearchGathererQueue,
		NIDSearchGathererFolderQueue,
	} {
		if err := writeEmptyTC(n, nid, 0, nil); err != nil {
			return err
		}
	}
	for _, nid := range []uint32{NIDSearchDomainObject, NIDSearchGathererDescriptor} {
		if err := writeEmptyPC(n, nid, 0); err != nil {
			return err
		}
	}

	recv, err := NewTC(n, receiveFolderColumns())
	if err != nil {
		return err
	}
	if err := recv.AddID(1); err != nil {
		return err
	}
	if err := recv.SetString(1, PidTagMessageClass, "IPM.Note"); err != nil {
		return err
	}
	if err := recv.SetBinary(1, PidTagEntryId, ipmEID); err != nil {
		return err
	}
	if err := recv.Commit(RelatedNID(NIDMessageStore, NIDTypeReceiveFolderTable)); err != nil {
		return err
	}
	if err := n.SetParent(RelatedNID(NIDMessageStore, NIDTypeReceiveFolderTable), NIDMessageStore); err != nil {
		return err
	}
	if err := writeEmptyTC(n, RelatedNID(NIDMessageStore, NIDTypeOutgoingQueueTable), NIDMessageStore, contentsColumns()); err != nil {
		return err
	}

	folders := []minFolder{
		{nid: NIDRootFolder, parent: 0, name: "", class: "IPF.Note", subfolders: true},
		{nid: ipm, parent: NIDRootFolder, name: spec.DisplayName, class: "IPF.Note"},
		{nid: waste, parent: NIDRootFolder, name: "Deleted Items", class: "IPF.Note"},
		{nid: finder, parent: NIDRootFolder, name: "Search Root", class: "IPF.Note", folderType: FolderTypeSearch},
	}
	for _, f := range folders {
		eid := encodeEntryID(spec.RecordKey, f.nid)
		if f.nid == NIDRootFolder {
			eid = rootEID
		}
		if err := writeFolderPC(n, f, eid, ft); err != nil {
			return err
		}
		hier, err := NewTC(n, hierarchyColumns())
		if err != nil {
			return err
		}
		if f.nid == NIDRootFolder {
			for _, child := range []minFolder{folders[1], folders[2], folders[3]} {
				if err := addHierarchyRow(hier, child); err != nil {
					return err
				}
			}
		}
		if err := hier.Commit(RelatedNID(f.nid, NIDTypeHierarchyTable)); err != nil {
			return err
		}
		if err := n.SetParent(RelatedNID(f.nid, NIDTypeHierarchyTable), f.nid); err != nil {
			return err
		}
		if err := writeEmptyTC(n, RelatedNID(f.nid, NIDTypeContentsTable), f.nid, contentsColumns()); err != nil {
			return err
		}
		if err := writeEmptyTC(n, RelatedNID(f.nid, NIDTypeAssocContentsTable), f.nid, contentsColumns()); err != nil {
			return err
		}
	}
	return nil
}

func writeEmptyPC(n *NDB, nid, parent uint32) error {
	pc := NewPC(n)
	if err := pc.Commit(nid); err != nil {
		return err
	}
	if parent != 0 {
		return n.SetParent(nid, parent)
	}
	return nil
}

func writeEmptyTC(n *NDB, nid, parent uint32, cols []ColumnView) error {
	tc, err := NewTC(n, cols)
	if err != nil {
		return err
	}
	if err := tc.Commit(nid); err != nil {
		return err
	}
	if parent != 0 {
		return n.SetParent(nid, parent)
	}
	return nil
}

func writeNameToIDMap(n *NDB) error {
	pc := NewPC(n)
	if err := pc.SetBinary(PidTagNameidStreamGuid, nil); err != nil {
		return err
	}
	if err := pc.SetBinary(PidTagNameidStreamEntry, nil); err != nil {
		return err
	}
	if err := pc.SetBinary(PidTagNameidStreamString, nil); err != nil {
		return err
	}
	return pc.Commit(NIDNameToIDMap)
}

func writeMessageStore(n *NDB, spec MinimumSpec, ipm, waste, finder []byte) error {
	pc := NewPC(n)
	if err := pc.SetBinary(PidTagRecordKey, spec.RecordKey[:]); err != nil {
		return err
	}
	if err := pc.SetString(PidTagDisplayName, spec.DisplayName); err != nil {
		return err
	}
	if err := pc.SetBinary(PidTagIpmSubTreeEntryId, ipm); err != nil {
		return err
	}
	if err := pc.SetBinary(PidTagIpmWastebasketEntryId, waste); err != nil {
		return err
	}
	if err := pc.SetBinary(PidTagFinderEntryId, finder); err != nil {
		return err
	}
	if err := pc.SetInt32(PidTagStoreSupportMask, StoreSupportMaskUnicode); err != nil {
		return err
	}
	return pc.Commit(NIDMessageStore)
}

func writeFolderPC(n *NDB, f minFolder, eid []byte, ft uint64) error {
	pc := NewPC(n)
	if err := pc.SetString(PidTagDisplayName, f.name); err != nil {
		return err
	}
	if err := pc.SetInt32(PidTagContentCount, f.content); err != nil {
		return err
	}
	if err := pc.SetInt32(PidTagContentUnreadCount, f.unread); err != nil {
		return err
	}
	if err := pc.SetBool(PidTagSubfolders, f.subfolders); err != nil {
		return err
	}
	if err := pc.SetString(PidTagContainerClass, f.class); err != nil {
		return err
	}
	folderType := f.folderType
	if folderType == 0 {
		folderType = FolderTypeGeneric
	}
	if err := pc.SetInt32(PidTagFolderType, folderType); err != nil {
		return err
	}
	if err := pc.SetTime(PidTagCreationTime, ft); err != nil {
		return err
	}
	if err := pc.SetTime(PidTagLastModificationTime, ft); err != nil {
		return err
	}
	if err := pc.SetBinary(PidTagEntryId, eid); err != nil {
		return err
	}
	if err := pc.Commit(f.nid); err != nil {
		return err
	}
	if f.parent != 0 {
		return n.SetParent(f.nid, f.parent)
	}
	return nil
}

func addHierarchyRow(tc *TC, child minFolder) error {
	if err := tc.AddID(child.nid); err != nil {
		return err
	}
	return writeHierarchyRow(tc, child)
}

func writeHierarchyRow(tc *TC, child minFolder) error {
	if _, err := tc.row(child.nid); err != nil {
		if err := tc.AddID(child.nid); err != nil {
			return err
		}
	}
	if err := tc.SetString(child.nid, PidTagDisplayName, child.name); err != nil {
		return err
	}
	if err := tc.SetInt32(child.nid, PidTagContentCount, child.content); err != nil {
		return err
	}
	if err := tc.SetInt32(child.nid, PidTagContentUnreadCount, child.unread); err != nil {
		return err
	}
	return tc.SetBool(child.nid, PidTagSubfolders, child.subfolders)
}
