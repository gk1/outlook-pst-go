package writer

import (
	"io"
	"path"
	"strings"
	"unicode"
	"unicode/utf8"
)

// AttachmentWrite is one by-value or embedded-message attachment.
// Body, when set, is streamed into an HNID data tree; Data is the in-memory
// alternative used by the FolderTree object-model API.
type AttachmentWrite struct {
	Filename  string
	MIMEType  string
	ContentID string
	Inline    bool
	Data      []byte
	Body      io.Reader
	Size      int64
	Embedded  *MessageWrite
}

// attachmentTemplateColumns is the PST-wide Attachment Table Template
// (MS-PST 2.4.6.1.1). PidTagLtpRowId / PidTagLtpRowVer are injected by NewTC.
func attachmentTemplateColumns() []ColumnView {
	return []ColumnView{
		{PropType: PtypInteger32, PropID: PidTagAttachSize},
		{PropType: PtypString, PropID: PidTagAttachFilename},
		{PropType: PtypInteger32, PropID: PidTagAttachMethod},
		{PropType: PtypInteger32, PropID: PidTagRenderingPosition},
	}
}

func attachmentColumns() []ColumnView {
	return []ColumnView{
		{PropType: PtypInteger32, PropID: PidTagAttachNumber},
		{PropType: PtypInteger32, PropID: PidTagAttachSize},
		{PropType: PtypInteger32, PropID: PidTagAttachMethod},
		{PropType: PtypInteger32, PropID: PidTagRenderingPosition},
		{PropType: PtypString, PropID: PidTagAttachLongFilename},
		{PropType: PtypString, PropID: PidTagAttachFilename},
		{PropType: PtypString, PropID: PidTagAttachMimeTag},
		{PropType: PtypString, PropID: PidTagAttachContentId},
	}
}

func renderingPosition(a AttachmentWrite) int32 {
	if a.Inline {
		return 0
	}
	return RenderingPositionNone
}

func attachExtension(name string) string {
	ext := path.Ext(strings.ReplaceAll(name, "\\", "/"))
	return strings.ToLower(ext)
}

func shortFilename(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "FILE"
	}
	base := path.Base(strings.ReplaceAll(name, "\\", "/"))
	ext := path.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	stem = asciiUpper83(stem, 8)
	ext = asciiUpper83(strings.TrimPrefix(ext, "."), 3)
	if ext == "" {
		return stem
	}
	return stem + "." + ext
}

func asciiUpper83(s string, max int) string {
	var b strings.Builder
	for _, r := range s {
		if unicode.IsSpace(r) {
			continue
		}
		if r > unicode.MaxASCII || r < 0x21 || strings.ContainsRune(`<>:"/\|?*`, r) {
			b.WriteByte('_')
		} else {
			b.WriteRune(unicode.ToUpper(r))
		}
		if utf8.RuneCountInString(b.String()) >= max {
			break
		}
	}
	if b.Len() == 0 {
		return "X"
	}
	return b.String()
}

func attachSizeOf(a AttachmentWrite) int32 {
	if a.Embedded != nil {
		return messageSizeOf(*a.Embedded)
	}
	if a.Body != nil {
		return int32(a.Size)
	}
	return int32(len(a.Data))
}

func validateAttachmentTree(w MessageWrite, lim Limits, depth int, seen map[*MessageWrite]struct{}) error {
	if len(w.Attachments) > lim.MaxAttachmentsPerMessage {
		return limitErr("MaxAttachmentsPerMessage", "%d attachments exceeds %d", len(w.Attachments), lim.MaxAttachmentsPerMessage)
	}
	if seen == nil {
		seen = map[*MessageWrite]struct{}{}
	}
	for i := range w.Attachments {
		a := &w.Attachments[i]
		n := int64(len(a.Data))
		if a.Body != nil {
			n = a.Size
		}
		if n > lim.MaxAttachmentBytes {
			return limitErr("MaxAttachmentBytes", "attachment %d is %d bytes, cap %d", i, n, lim.MaxAttachmentBytes)
		}
		if a.Embedded == nil {
			continue
		}
		if len(a.Data) > 0 || a.Body != nil {
			return invalidArg("body", "embedded attachment %d must not also have Data", i)
		}
		if depth+1 > lim.MaxEmbeddedDepth {
			return limitErr("MaxEmbeddedDepth", "embedded message depth %d exceeds %d", depth+1, lim.MaxEmbeddedDepth)
		}
		if _, ok := seen[a.Embedded]; ok {
			return invalidArg("embedded", "cycle in embedded messages")
		}
		seen[a.Embedded] = struct{}{}
		if err := validateImportance(a.Embedded.Importance); err != nil {
			return err
		}
		if err := validateSensitivity(a.Embedded.Sensitivity); err != nil {
			return err
		}
		if err := validateAttachmentTree(*a.Embedded, lim, depth+1, seen); err != nil {
			delete(seen, a.Embedded)
			return err
		}
		delete(seen, a.Embedded)
	}
	return nil
}

func (t *FolderTree) attachAttachments(parent *Heap, msgNID uint32, w MessageWrite, depth int, seen map[*MessageWrite]struct{}) error {
	// MS-PST 2.4.6: the per-message Attachment Table exists only when the
	// message has at least one Attachment object. The PST-wide empty
	// template at NID_ATTACHMENT_TABLE (0x671) is the zero-row schema.
	if len(w.Attachments) == 0 {
		return nil
	}
	tc, err := NewTC(t.n, attachmentColumns())
	if err != nil {
		return err
	}
	ids := t.n.ids
	if ids == nil {
		return invalidArg("ids", "NDB has no SequentialIDs")
	}
	for i, a := range w.Attachments {
		nid := ids.NextNID(NIDTypeAttachment)
		if err := writeAttachmentRow(tc, nid, i, a); err != nil {
			return err
		}
		pc, err := t.buildAttachmentPC(nid, i, a, depth, seen)
		if err != nil {
			return err
		}
		if err := pc.AttachTo(parent, nid); err != nil {
			return err
		}
	}
	return tc.AttachTo(parent, RelatedNID(msgNID, NIDTypeAttachmentTable))
}

func writeAttachmentRow(tc *TC, nid uint32, number int, a AttachmentWrite) error {
	if err := tc.AddID(nid); err != nil {
		return err
	}
	method := AttachMethodByValue
	if a.Embedded != nil {
		method = AttachMethodEmbedded
	}
	name := strings.TrimSpace(a.Filename)
	if err := tc.SetInt32(nid, PidTagAttachNumber, int32(number)); err != nil {
		return err
	}
	if err := tc.SetInt32(nid, PidTagAttachSize, attachSizeOf(a)); err != nil {
		return err
	}
	if err := tc.SetInt32(nid, PidTagAttachMethod, method); err != nil {
		return err
	}
	if err := tc.SetInt32(nid, PidTagRenderingPosition, renderingPosition(a)); err != nil {
		return err
	}
	if err := tc.SetString(nid, PidTagAttachLongFilename, name); err != nil {
		return err
	}
	if err := tc.SetString(nid, PidTagAttachFilename, shortFilename(name)); err != nil {
		return err
	}
	if err := tc.SetString(nid, PidTagAttachMimeTag, a.MIMEType); err != nil {
		return err
	}
	return tc.SetString(nid, PidTagAttachContentId, a.ContentID)
}

func (t *FolderTree) buildAttachmentPC(nid uint32, number int, a AttachmentWrite, depth int, seen map[*MessageWrite]struct{}) (*PC, error) {
	pc := NewPC(t.n)
	name := strings.TrimSpace(a.Filename)
	method := AttachMethodByValue
	if a.Embedded != nil {
		method = AttachMethodEmbedded
	}
	if err := pc.SetInt32(PidTagAttachNumber, int32(number)); err != nil {
		return nil, err
	}
	if err := pc.SetInt32(PidTagAttachSize, attachSizeOf(a)); err != nil {
		return nil, err
	}
	if err := pc.SetInt32(PidTagAttachMethod, method); err != nil {
		return nil, err
	}
	if err := pc.SetString(PidTagAttachLongFilename, name); err != nil {
		return nil, err
	}
	if err := pc.SetString(PidTagAttachFilename, shortFilename(name)); err != nil {
		return nil, err
	}
	if ext := attachExtension(name); ext != "" {
		if err := pc.SetString(PidTagAttachExtension, ext); err != nil {
			return nil, err
		}
	}
	if a.MIMEType != "" {
		if err := pc.SetString(PidTagAttachMimeTag, a.MIMEType); err != nil {
			return nil, err
		}
	}
	if a.ContentID != "" {
		if err := pc.SetString(PidTagAttachContentId, a.ContentID); err != nil {
			return nil, err
		}
	}
	if name != "" {
		if err := pc.SetString(PidTagDisplayName, name); err != nil {
			return nil, err
		}
	}
	pos := RenderingPositionNone
	flags := int32(0)
	if a.Inline {
		pos = 0
		flags |= AttachFlagMHTMLRef
	}
	if err := pc.SetInt32(PidTagRenderingPosition, pos); err != nil {
		return nil, err
	}
	if flags != 0 {
		if err := pc.SetInt32(PidTagAttachFlags, flags); err != nil {
			return nil, err
		}
	}
	if a.Embedded != nil {
		embedNID := RelatedNID(nid, NIDTypeNormalMessage)
		if err := t.writeEmbeddedMessage(pc.heap, embedNID, a.Embedded, depth+1, seen); err != nil {
			return nil, err
		}
		if err := pc.SetObjectNID(PidTagAttachDataBinary, embedNID); err != nil {
			return nil, err
		}
		return pc, nil
	}
	if a.Body != nil {
		if err := pc.SetBinaryStream(PidTagAttachDataBinary, a.Body, a.Size); err != nil {
			return nil, err
		}
		return pc, nil
	}
	if err := pc.SetBinary(PidTagAttachDataBinary, a.Data); err != nil {
		return nil, err
	}
	return pc, nil
}

func (t *FolderTree) writeEmbeddedMessage(parent *Heap, nid uint32, w *MessageWrite, depth int, seen map[*MessageWrite]struct{}) error {
	if w == nil {
		return invalidArg("embedded", "nil MessageWrite")
	}
	if seen == nil {
		seen = map[*MessageWrite]struct{}{}
	}
	if _, ok := seen[w]; ok {
		return invalidArg("embedded", "cycle in embedded messages")
	}
	seen[w] = struct{}{}
	defer delete(seen, w)

	ids := t.n.ids
	if ids == nil {
		return invalidArg("ids", "NDB has no SequentialIDs")
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
	flags := messageFlags(*w)
	size := messageSizeOf(*w)
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
	if err := t.attachAttachments(pc.heap, nid, *w, depth, seen); err != nil {
		return err
	}
	return pc.AttachTo(parent, nid)
}
