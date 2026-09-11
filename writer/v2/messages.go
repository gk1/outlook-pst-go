package writer

import (
	"encoding/hex"
	"strings"
	"time"
)

// MessageWrite is the on-disk IPM.Note input. Missing timestamps use the
// FolderTree clock. Empty SearchKey/RecordKey are allocated from SequentialIDs.
type MessageWrite struct {
	NID         uint32 // if set, used as the on-disk NID (export-plan materialization)
	Subject     string
	Class       string
	BodyText    string
	BodyHTML    string
	Headers     string
	InternetID  string
	From        Recipient
	To          []Recipient
	Cc          []Recipient
	Bcc         []Recipient
	Sent        time.Time
	Received    time.Time
	Created     time.Time
	Modified    time.Time
	Read        bool
	Draft       bool
	Importance  Importance
	Sensitivity Sensitivity
	SearchKey   []byte
	RecordKey   []byte
	Attachments []AttachmentWrite
}

func recipientColumns() []ColumnView {
	return []ColumnView{
		{PropType: PtypInteger32, PropID: PidTagRecipientType},
		{PropType: PtypString, PropID: PidTagDisplayName},
		{PropType: PtypString, PropID: PidTagAddressType},
		{PropType: PtypString, PropID: PidTagEmailAddress},
		{PropType: PtypString, PropID: PidTagSmtpAddress},
		{PropType: PtypString, PropID: PidTagRecipientDisplayName},
		{PropType: PtypBinary, PropID: PidTagSearchKey},
		{PropType: PtypInteger32, PropID: PidTagObjectType},
		{PropType: PtypInteger32, PropID: PidTagDisplayType},
	}
}

func orTime(t, fallback time.Time) time.Time {
	if t.IsZero() {
		return fallback
	}
	return t.UTC()
}

func displayList(rs []Recipient) string {
	parts := make([]string, 0, len(rs))
	for _, r := range rs {
		n := strings.TrimSpace(r.Name)
		if n == "" {
			n = strings.TrimSpace(r.Email)
		}
		if n != "" {
			parts = append(parts, n)
		}
	}
	return strings.Join(parts, "; ")
}

func recipDisplay(r Recipient) string {
	n := strings.TrimSpace(r.Name)
	if n != "" {
		return n
	}
	return strings.TrimSpace(r.Email)
}

func messageFlags(w MessageWrite) int32 {
	var f int32
	if w.Read {
		f |= MsgFlagRead
	}
	if w.Draft {
		f |= MsgFlagUnsent
	} else {
		f |= MsgFlagFromMe
	}
	if len(w.Attachments) > 0 {
		f |= MsgFlagHasAttach
	}
	return f
}

func priorityOf(imp Importance) int32 {
	switch imp {
	case ImportanceLow:
		return -1
	case ImportanceHigh:
		return 1
	default:
		return 0
	}
}

func messageSizeOf(w MessageWrite) int32 {
	n := 512 + len(w.BodyText) + len(w.BodyHTML) + len(w.Headers) + len(w.Subject)
	for _, r := range w.To {
		n += len(r.Name) + len(r.Email) + 64
	}
	for _, r := range w.Cc {
		n += len(r.Name) + len(r.Email) + 64
	}
	for _, r := range w.Bcc {
		n += len(r.Name) + len(r.Email) + 64
	}
	for _, a := range w.Attachments {
		n += len(a.Filename) + int(attachSizeOf(a)) + 64
		if a.Embedded != nil {
			n += int(messageSizeOf(*a.Embedded))
		}
	}
	return int32(n)
}

func (t *FolderTree) canHoldMessages(folder uint32) error {
	if _, ok := t.n.LookupNode(uint64(folder)); !ok {
		return invalidArg("folder", "missing folder 0x%x", folder)
	}
	if folder == t.finder || NIDTypeOf(folder) == NIDTypeSearchFolder {
		return unsupported(FeatureSearchFolder, "cannot place messages in a search folder")
	}
	if folder != NIDRootFolder && NIDTypeOf(folder) != NIDTypeNormalFolder {
		return invalidArg("folder", "NID 0x%x is not a folder", folder)
	}
	return nil
}

func (t *FolderTree) adjustCounts(folder uint32, dContent, dUnread int32) error {
	v, err := OpenPC(t.n, folder)
	if err != nil {
		return err
	}
	content, err := v.GetInt32(PidTagContentCount)
	if err != nil {
		return err
	}
	unread, err := v.GetInt32(PidTagContentUnreadCount)
	if err != nil {
		return err
	}
	content += dContent
	unread += dUnread
	if content < 0 {
		content = 0
	}
	if unread < 0 {
		unread = 0
	}
	return t.mutatePC(folder, func(p *PC) error {
		if err := p.SetInt32(PidTagContentCount, content); err != nil {
			return err
		}
		if err := p.SetInt32(PidTagContentUnreadCount, unread); err != nil {
			return err
		}
		return p.SetTime(PidTagLastModificationTime, filetimeOf(t.now))
	})
}

func writeRecipientRows(tc *TC, recips []Recipient, typ RecipType) error {
	for _, r := range recips {
		id, err := tc.Add()
		if err != nil {
			return err
		}
		name := recipDisplay(r)
		email := strings.TrimSpace(r.Email)
		if err := tc.SetInt32(id, PidTagRecipientType, int32(typ)); err != nil {
			return err
		}
		if err := tc.SetString(id, PidTagDisplayName, name); err != nil {
			return err
		}
		if err := tc.SetString(id, PidTagAddressType, "SMTP"); err != nil {
			return err
		}
		if err := tc.SetString(id, PidTagEmailAddress, email); err != nil {
			return err
		}
		if err := tc.SetString(id, PidTagSmtpAddress, email); err != nil {
			return err
		}
		if err := tc.SetString(id, PidTagRecipientDisplayName, name); err != nil {
			return err
		}
		key := []byte("SMTP:" + strings.ToUpper(email))
		if err := tc.SetBinary(id, PidTagSearchKey, key); err != nil {
			return err
		}
		if err := tc.SetInt32(id, PidTagObjectType, ObjectTypeMailUser); err != nil {
			return err
		}
		if err := tc.SetInt32(id, PidTagDisplayType, DisplayTypeMailUser); err != nil {
			return err
		}
	}
	return nil
}

func writeContentsRow(tc *TC, nid uint32, w MessageWrite, flags int32, size int32, sent, recv uint64) error {
	if _, err := tc.row(nid); err != nil {
		if err := tc.AddID(nid); err != nil {
			return err
		}
	}
	class := w.Class
	if class == "" {
		class = "IPM.Note"
	}
	if err := tc.SetInt32(nid, PidTagImportance, int32(w.Importance)); err != nil {
		return err
	}
	if err := tc.SetString(nid, PidTagMessageClass, class); err != nil {
		return err
	}
	if err := tc.SetString(nid, PidTagSubject, w.Subject); err != nil {
		return err
	}
	if err := tc.SetTime(nid, PidTagClientSubmitTime, sent); err != nil {
		return err
	}
	if err := tc.SetString(nid, PidTagSentRepresentingName, recipDisplay(w.From)); err != nil {
		return err
	}
	if err := tc.SetString(nid, PidTagDisplayTo, displayList(w.To)); err != nil {
		return err
	}
	if err := tc.SetTime(nid, PidTagMessageDeliveryTime, recv); err != nil {
		return err
	}
	if err := tc.SetInt32(nid, PidTagMessageFlags, flags); err != nil {
		return err
	}
	if err := tc.SetInt32(nid, PidTagMessageSize, size); err != nil {
		return err
	}
	if err := tc.SetInt32(nid, PidTagMessageStatus, 0); err != nil {
		return err
	}
	return tc.SetBool(nid, PidTagHasAttachments, len(w.Attachments) > 0)
}

func (t *FolderTree) writeMessagePC(nid, folder uint32, w MessageWrite, flags, size int32, sent, recv, created, modified uint64, search, record []byte) error {
	pc := NewPC(t.n)
	class := w.Class
	if class == "" {
		class = "IPM.Note"
	}
	if err := pc.SetString(PidTagMessageClass, class); err != nil {
		return err
	}
	if err := pc.SetString(PidTagSubject, w.Subject); err != nil {
		return err
	}
	if err := pc.SetString(PidTagNormalizedSubject, w.Subject); err != nil {
		return err
	}
	if err := pc.SetString(PidTagConversationTopic, w.Subject); err != nil {
		return err
	}
	if w.BodyText != "" {
		if err := pc.SetString(PidTagBody, w.BodyText); err != nil {
			return err
		}
	}
	if w.BodyHTML != "" {
		if err := pc.SetString(PidTagHtmlBody, w.BodyHTML); err != nil {
			return err
		}
	}
	if w.Headers != "" {
		if err := pc.SetString(PidTagTransportMessageHeaders, w.Headers); err != nil {
			return err
		}
	}
	if w.InternetID != "" {
		if err := pc.SetString(PidTagInternetMessageId, w.InternetID); err != nil {
			return err
		}
	}
	if err := pc.SetInt32(PidTagImportance, int32(w.Importance)); err != nil {
		return err
	}
	if err := pc.SetInt32(PidTagPriority, priorityOf(w.Importance)); err != nil {
		return err
	}
	if err := pc.SetInt32(PidTagSensitivity, int32(w.Sensitivity)); err != nil {
		return err
	}
	if err := pc.SetInt32(PidTagMessageFlags, flags); err != nil {
		return err
	}
	if err := pc.SetInt32(PidTagMessageSize, size); err != nil {
		return err
	}
	if err := pc.SetInt32(PidTagMessageStatus, 0); err != nil {
		return err
	}
	if err := pc.SetBool(PidTagHasAttachments, len(w.Attachments) > 0); err != nil {
		return err
	}
	if err := pc.SetTime(PidTagClientSubmitTime, sent); err != nil {
		return err
	}
	if err := pc.SetTime(PidTagMessageDeliveryTime, recv); err != nil {
		return err
	}
	if err := pc.SetTime(PidTagCreationTime, created); err != nil {
		return err
	}
	if err := pc.SetTime(PidTagLastModificationTime, modified); err != nil {
		return err
	}
	fromName := recipDisplay(w.From)
	fromEmail := strings.TrimSpace(w.From.Email)
	if fromName != "" {
		if err := pc.SetString(PidTagSenderName, fromName); err != nil {
			return err
		}
		if err := pc.SetString(PidTagSentRepresentingName, fromName); err != nil {
			return err
		}
	}
	if fromEmail != "" {
		if err := pc.SetString(PidTagSenderEmailAddress, fromEmail); err != nil {
			return err
		}
		if err := pc.SetString(PidTagSentRepresentingEmailAddress, fromEmail); err != nil {
			return err
		}
		if err := pc.SetString(PidTagSenderAddressType, "SMTP"); err != nil {
			return err
		}
	}
	if err := pc.SetString(PidTagDisplayTo, displayList(w.To)); err != nil {
		return err
	}
	if err := pc.SetString(PidTagDisplayCc, displayList(w.Cc)); err != nil {
		return err
	}
	if err := pc.SetString(PidTagDisplayBcc, displayList(w.Bcc)); err != nil {
		return err
	}
	if err := pc.SetBinary(PidTagSearchKey, search); err != nil {
		return err
	}
	if err := pc.SetBinary(PidTagRecordKey, record); err != nil {
		return err
	}
	if err := pc.SetBinary(PidTagEntryId, encodeEntryID(t.recordKey, nid)); err != nil {
		return err
	}
	recip, err := NewTC(t.n, recipientColumns())
	if err != nil {
		return err
	}
	if err := writeRecipientRows(recip, w.To, RecipTo); err != nil {
		return err
	}
	if err := writeRecipientRows(recip, w.Cc, RecipCc); err != nil {
		return err
	}
	if err := writeRecipientRows(recip, w.Bcc, RecipBcc); err != nil {
		return err
	}
	if err := recip.AttachTo(pc.heap, RelatedNID(nid, NIDTypeRecipientTable)); err != nil {
		return err
	}
	if err := t.attachAttachments(pc.heap, nid, w, 0, map[*MessageWrite]struct{}{}); err != nil {
		return err
	}
	if err := pc.Commit(nid); err != nil {
		return err
	}
	return t.n.SetParent(nid, folder)
}

// CreateMessage allocates an IPM.Note under folder with a Recipient Table
// subnode (even when empty) and a contents-table row (MS-PST 2.4.5).
func (t *FolderTree) CreateMessage(folder uint32, w MessageWrite) (uint32, error) {
	var nid uint32
	err := t.n.runTxn(func() error {
		var err error
		nid, err = t.createMessageLocked(folder, w)
		return err
	})
	return nid, err
}

func (t *FolderTree) createMessageLocked(folder uint32, w MessageWrite) (uint32, error) {
	if err := t.canHoldMessages(folder); err != nil {
		return 0, err
	}
	if err := validateImportance(w.Importance); err != nil {
		return 0, err
	}
	if err := validateSensitivity(w.Sensitivity); err != nil {
		return 0, err
	}
	nRecip := len(w.To) + len(w.Cc) + len(w.Bcc)
	if nRecip > t.limits.MaxRecipientsPerMessage {
		return 0, limitErr("MaxRecipientsPerMessage", "%d recipients exceeds %d", nRecip, t.limits.MaxRecipientsPerMessage)
	}
	if err := validateAttachmentTree(w, t.limits, 0, map[*MessageWrite]struct{}{}); err != nil {
		return 0, err
	}
	ids := t.n.ids
	if ids == nil {
		return 0, invalidArg("ids", "NDB has no SequentialIDs")
	}
	nid := w.NID
	if nid == 0 {
		nid = ids.NextNID(NIDTypeNormalMessage)
	} else {
		if NIDTypeOf(nid) != NIDTypeNormalMessage {
			return 0, invalidArg("nid", "planned NID 0x%x is not a normal message", nid)
		}
		if _, ok := t.n.LookupNode(uint64(nid)); ok {
			return 0, invalidArg("nid", "message NID 0x%x already exists", nid)
		}
		ids.EnsureIndex(NIDTypeNormalMessage, NIDIndexOf(nid)+1)
	}
	sentT := orTime(w.Sent, t.now)
	recvT := orTime(w.Received, t.now)
	createdT := orTime(w.Created, t.now)
	modifiedT := orTime(w.Modified, t.now)
	sent, recv := filetimeOf(sentT), filetimeOf(recvT)
	created, modified := filetimeOf(createdT), filetimeOf(modifiedT)
	search := append([]byte(nil), w.SearchKey...)
	if len(search) != 16 {
		k := ids.NextSearchKey()
		search = k[:]
	}
	record := append([]byte(nil), w.RecordKey...)
	if len(record) != 16 {
		k := ids.NextRecordKey()
		record = k[:]
	}
	flags := messageFlags(w)
	size := messageSizeOf(w)
	if err := t.writeMessagePC(nid, folder, w, flags, size, sent, recv, created, modified, search, record); err != nil {
		return 0, err
	}
	if err := t.patchTC(RelatedNID(folder, NIDTypeContentsTable), func(tc *TC) error {
		return writeContentsRow(tc, nid, w, flags, size, sent, recv)
	}); err != nil {
		return 0, err
	}
	dUnread := int32(0)
	if !w.Read {
		dUnread = 1
	}
	if err := t.adjustCounts(folder, 1, dUnread); err != nil {
		return 0, err
	}
	return nid, nil
}

// DeleteMessage removes the message node, its contents-table row, and updates
// folder counts. The Recipient Table lives in the message subnode tree.
func (t *FolderTree) DeleteMessage(nid uint32) error {
	return t.n.runTxn(func() error { return t.deleteMessageLocked(nid) })
}

func (t *FolderTree) deleteMessageLocked(nid uint32) error {
	if NIDTypeOf(nid) != NIDTypeNormalMessage {
		return invalidArg("nid", "NID 0x%x is not a message", nid)
	}
	e, ok := t.n.LookupNode(uint64(nid))
	if !ok {
		return invalidArg("nid", "missing message 0x%x", nid)
	}
	folder := e.ParentNID
	pc, err := OpenPC(t.n, nid)
	if err != nil {
		return err
	}
	flags, err := pc.GetInt32(PidTagMessageFlags)
	if err != nil {
		return err
	}
	unread := int32(0)
	if flags&MsgFlagRead == 0 {
		unread = 1
	}
	if folder != 0 {
		if err := t.patchTC(RelatedNID(folder, NIDTypeContentsTable), func(tc *TC) error {
			return tc.Delete(nid)
		}); err != nil {
			return err
		}
	}
	if err := t.n.DeleteNode(uint64(nid)); err != nil {
		return err
	}
	if folder != 0 {
		return t.adjustCounts(folder, -1, -unread)
	}
	return nil
}

func decodePlanKey(s string) []byte {
	b, err := hex.DecodeString(s)
	if err != nil || len(b) != 16 {
		return nil
	}
	return b
}

func timeFromNano(n int64) time.Time {
	if n == 0 {
		return time.Time{}
	}
	return time.Unix(0, n).UTC()
}

func plannedToWrite(pm PlannedMessage, c MessageContent) MessageWrite {
	to := make([]Recipient, len(pm.To))
	for i, r := range pm.To {
		to[i] = Recipient{Name: r.Name, Email: r.Email, Type: RecipTo}
	}
	cc := make([]Recipient, len(pm.Cc))
	for i, r := range pm.Cc {
		cc[i] = Recipient{Name: r.Name, Email: r.Email, Type: RecipCc}
	}
	bcc := make([]Recipient, len(pm.Bcc))
	for i, r := range pm.Bcc {
		bcc[i] = Recipient{Name: r.Name, Email: r.Email, Type: RecipBcc}
	}
	return MessageWrite{
		NID:         pm.NID,
		Subject:     pm.Subject,
		Class:       pm.Class,
		BodyText:    c.BodyText,
		BodyHTML:    c.BodyHTML,
		Headers:     c.InternetHeaders,
		InternetID:  pm.InternetID,
		From:        Recipient{Name: pm.From.Name, Email: pm.From.Email},
		To:          to,
		Cc:          cc,
		Bcc:         bcc,
		Sent:        timeFromNano(pm.SentNano),
		Received:    timeFromNano(pm.RecvNano),
		Created:     timeFromNano(pm.CreatedNano),
		Modified:    timeFromNano(pm.ModifiedNano),
		Read:        pm.Read,
		Draft:       pm.Draft,
		Importance:  pm.Importance,
		Sensitivity: pm.Sensitivity,
		SearchKey:   decodePlanKey(pm.SearchKey),
		RecordKey:   decodePlanKey(pm.RecordKey),
		Attachments: plannedAttachments(pm.Attachments, c.Attachments),
	}
}

func plannedAttachments(plan []PlannedAttachment, stored []AttachmentContent) []AttachmentWrite {
	n := len(stored)
	if len(plan) < n {
		n = len(plan)
	}
	out := make([]AttachmentWrite, 0, n)
	for i := 0; i < n; i++ {
		a := AttachmentWrite{
			Filename:  stored[i].Filename,
			MIMEType:  stored[i].MIMEType,
			ContentID: stored[i].ContentID,
			Inline:    stored[i].Inline,
			Data:      stored[i].Bytes,
			Body:      stored[i].Body,
			Size:      stored[i].Size,
		}
		if a.Size == 0 && len(a.Data) > 0 {
			a.Size = int64(len(a.Data))
		}
		if stored[i].Embedded != nil && plan[i].Embedded != nil {
			nested := plannedToWrite(*plan[i].Embedded, *stored[i].Embedded)
			a.Embedded = &nested
		}
		out = append(out, a)
	}
	return out
}

// CreateMessagesFromPlan writes planned messages into already-materialized
// folders, including by-value and embedded attachments (MS-PST 2.4.6).
func (t *FolderTree) CreateMessagesFromPlan(nids map[FolderRef]uint32, msgs []PlannedMessage, content map[MessageRef]MessageContent) error {
	for _, pm := range msgs {
		folder, ok := nids[pm.Folder]
		if !ok {
			return invalidArg("folder", "unknown folder ref %d for message %d", pm.Folder, pm.Ref)
		}
		c := content[pm.Ref]
		if _, err := t.CreateMessage(folder, plannedToWrite(pm, c)); err != nil {
			return err
		}
	}
	return nil
}
