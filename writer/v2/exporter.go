package writer

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

// session is the concrete Exporter.
type session struct {
	opts     Options
	closed   bool
	mailbox  bool
	nextFold FolderRef
	nextMsg  MessageRef
	folders  map[FolderRef]PlannedFolder
	content  map[MessageRef]MessageContent
	plan     Plan
}

// New constructs a v2 exporter. CryptWIP and ANSI are rejected here.
func New(opts Options) (Exporter, error) {
	opts = opts.withDefaults()
	switch opts.Crypt {
	case CryptNone, CryptPermute, CryptCyclic:
	case CryptWIP:
		return nil, unsupported(FeatureWIPCrypt, "WIP crypt 0x10 is out of scope")
	default:
		return nil, invalidArg("crypt", "unknown bCryptMethod 0x%02x", opts.Crypt)
	}
	s := &session{
		opts:     opts,
		nextFold: IPMSubtreeRef + 1,
		nextMsg:  1,
		folders:  make(map[FolderRef]PlannedFolder),
		content:  make(map[MessageRef]MessageContent),
		plan: Plan{
			Version:     1,
			DisplayName: opts.DisplayName,
			CreatedNano: opts.Clock.Now().UnixNano(),
			Crypt:       opts.Crypt,
		},
	}
	s.seedSystemFolders()
	return s, nil
}

func (s *session) seedSystemFolders() {
	root := PlannedFolder{Ref: RootFolderRef, NID: NIDRootFolder, Parent: 0, Name: "Root", Depth: 0}
	ipm := PlannedFolder{
		Ref:    IPMSubtreeRef,
		NID:    s.opts.IDs.NextNID(NIDTypeNormalFolder),
		Parent: RootFolderRef,
		Name:   "IPM_SUBTREE",
		Depth:  1,
	}
	s.folders[RootFolderRef] = root
	s.folders[IPMSubtreeRef] = ipm
	s.plan.Folders = append(s.plan.Folders, root, ipm)
}

func (s *session) check() error {
	if s.closed {
		return &Error{Code: CodeClosed, Err: ErrClosed, Detail: "exporter closed"}
	}
	return nil
}

func (s *session) CreateMailbox(m Mailbox) error {
	if err := s.check(); err != nil {
		return err
	}
	if s.mailbox {
		return invalidArg("mailbox", "CreateMailbox already called")
	}
	if m.DisplayName != "" {
		s.plan.DisplayName = m.DisplayName
	}
	s.mailbox = true
	return nil
}

func (s *session) CreateFolder(spec FolderSpec) (FolderRef, error) {
	if err := s.check(); err != nil {
		return 0, err
	}
	name, err := validateFolderName(spec.Name)
	if err != nil {
		return 0, err
	}
	spec.Name = name
	parent := spec.Parent
	if parent == 0 {
		parent = IPMSubtreeRef
	}
	p, ok := s.folders[parent]
	if !ok {
		return 0, invalidArg("parent", "unknown folder ref %d", parent)
	}
	if err := s.planCollision(parent, spec.Name, 0); err != nil {
		return 0, err
	}
	depth := p.Depth + 1
	if depth > s.opts.Limits.MaxFolderDepth {
		return 0, limitErr("MaxFolderDepth", "depth %d exceeds %d", depth, s.opts.Limits.MaxFolderDepth)
	}
	if len(s.plan.Folders) >= s.opts.Limits.MaxFolders {
		return 0, limitErr("MaxFolders", "folder count exceeds %d", s.opts.Limits.MaxFolders)
	}
	ref := s.nextFold
	s.nextFold++
	f := PlannedFolder{
		Ref:    ref,
		NID:    s.opts.IDs.NextNID(NIDTypeNormalFolder),
		Parent: parent,
		Name:   spec.Name,
		Depth:  depth,
	}
	s.folders[ref] = f
	s.plan.Folders = append(s.plan.Folders, f)
	return ref, nil
}

func (s *session) planCollision(parent FolderRef, name string, except FolderRef) error {
	for _, f := range s.folders {
		if f.Parent == parent && f.Ref != except && strings.EqualFold(f.Name, name) {
			return invalidArg("name", "folder %q already exists under parent %d", name, parent)
		}
	}
	return nil
}

func (s *session) updatePlanFolder(f PlannedFolder) {
	s.folders[f.Ref] = f
	for i := range s.plan.Folders {
		if s.plan.Folders[i].Ref == f.Ref {
			s.plan.Folders[i] = f
			return
		}
	}
}

func (s *session) systemFolder(ref FolderRef) bool {
	return ref == RootFolderRef || ref == IPMSubtreeRef
}

func (s *session) RenameFolder(ref FolderRef, name string) error {
	if err := s.check(); err != nil {
		return err
	}
	name, err := validateFolderName(name)
	if err != nil {
		return err
	}
	if s.systemFolder(ref) {
		return invalidArg("ref", "cannot rename system folder %d", ref)
	}
	f, ok := s.folders[ref]
	if !ok {
		return invalidArg("ref", "unknown folder ref %d", ref)
	}
	if err := s.planCollision(f.Parent, name, ref); err != nil {
		return err
	}
	f.Name = name
	s.updatePlanFolder(f)
	return nil
}

func (s *session) MoveFolder(ref, parent FolderRef) error {
	if err := s.check(); err != nil {
		return err
	}
	if s.systemFolder(ref) {
		return invalidArg("ref", "cannot move system folder %d", ref)
	}
	if parent == 0 {
		parent = IPMSubtreeRef
	}
	f, ok := s.folders[ref]
	if !ok {
		return invalidArg("ref", "unknown folder ref %d", ref)
	}
	p, ok := s.folders[parent]
	if !ok {
		return invalidArg("parent", "unknown folder ref %d", parent)
	}
	for cur := parent; cur != 0; {
		if cur == ref {
			return invalidArg("parent", "cannot move folder %d under itself or a descendant", ref)
		}
		pf, ok := s.folders[cur]
		if !ok {
			break
		}
		if pf.Parent == cur {
			break
		}
		cur = pf.Parent
	}
	if err := s.planCollision(parent, f.Name, ref); err != nil {
		return err
	}
	depth := p.Depth + 1
	if depth > s.opts.Limits.MaxFolderDepth {
		return limitErr("MaxFolderDepth", "depth %d exceeds %d", depth, s.opts.Limits.MaxFolderDepth)
	}
	f.Parent = parent
	s.updatePlanFolder(f)
	s.recomputeDepth(ref)
	return nil
}

func (s *session) recomputeDepth(ref FolderRef) {
	f := s.folders[ref]
	p, ok := s.folders[f.Parent]
	if !ok {
		return
	}
	f.Depth = p.Depth + 1
	s.updatePlanFolder(f)
	for _, c := range s.folders {
		if c.Parent == ref {
			s.recomputeDepth(c.Ref)
		}
	}
}

func (s *session) DeleteFolder(ref FolderRef) error {
	if err := s.check(); err != nil {
		return err
	}
	if s.systemFolder(ref) {
		return invalidArg("ref", "cannot delete system folder %d", ref)
	}
	if _, ok := s.folders[ref]; !ok {
		return invalidArg("ref", "unknown folder ref %d", ref)
	}
	drop := map[FolderRef]struct{}{}
	var walk func(FolderRef)
	walk = func(r FolderRef) {
		drop[r] = struct{}{}
		for _, c := range s.folders {
			if c.Parent == r {
				walk(c.Ref)
			}
		}
	}
	walk(ref)
	for _, m := range s.plan.Messages {
		if _, ok := drop[m.Folder]; ok {
			return invalidArg("folder", "folder %d still contains messages", m.Folder)
		}
	}
	for r := range drop {
		delete(s.folders, r)
	}
	out := s.plan.Folders[:0]
	for _, f := range s.plan.Folders {
		if _, ok := drop[f.Ref]; !ok {
			out = append(out, f)
		}
	}
	s.plan.Folders = out
	return nil
}

func (s *session) CreateMessage(folder FolderRef, msg MessageSpec) (MessageRef, error) {
	if err := s.check(); err != nil {
		return 0, err
	}
	if folder == 0 {
		folder = IPMSubtreeRef
	}
	if _, ok := s.folders[folder]; !ok {
		return 0, invalidArg("folder", "unknown folder ref %d", folder)
	}
	if len(s.plan.Messages) >= s.opts.Limits.MaxMessages {
		return 0, limitErr("MaxMessages", "message count exceeds %d", s.opts.Limits.MaxMessages)
	}
	if err := rejectUnsupportedMessage(msg); err != nil {
		return 0, err
	}
	if err := validateImportance(msg.Importance); err != nil {
		return 0, err
	}
	if err := validateSensitivity(msg.Sensitivity); err != nil {
		return 0, err
	}
	class := msg.Class
	if class == "" {
		class = "IPM.Note"
	}
	recips := append(append([]Recipient{}, msg.To...), append(msg.Cc, msg.Bcc...)...)
	if len(recips) > s.opts.Limits.MaxRecipientsPerMessage {
		return 0, limitErr("MaxRecipientsPerMessage", "%d recipients exceeds %d", len(recips), s.opts.Limits.MaxRecipientsPerMessage)
	}
	if len(msg.Attachments) > s.opts.Limits.MaxAttachmentsPerMessage {
		return 0, limitErr("MaxAttachmentsPerMessage", "%d attachments exceeds %d", len(msg.Attachments), s.opts.Limits.MaxAttachmentsPerMessage)
	}
	bodyBytes := int64(len(msg.BodyText) + len(msg.BodyHTML) + len(msg.InternetHeaders))
	if bodyBytes > s.opts.Limits.MaxMessageBytes {
		return 0, limitErr("MaxMessageBytes", "message body %d exceeds %d", bodyBytes, s.opts.Limits.MaxMessageBytes)
	}
	atts, stored, attBytes, err := snapshotAttachments(msg.Attachments, s.opts.Limits, s.opts.Limits.MaxMessageBytes-bodyBytes)
	if err != nil {
		return 0, err
	}
	if bodyBytes+attBytes > s.opts.Limits.MaxMessageBytes {
		return 0, limitErr("MaxMessageBytes", "message payload %d exceeds %d", bodyBytes+attBytes, s.opts.Limits.MaxMessageBytes)
	}
	now := s.opts.Clock.Now()
	ts := func(t time.Time) int64 {
		if t.IsZero() {
			return now.UnixNano()
		}
		return t.UTC().UnixNano()
	}
	ref := s.nextMsg
	s.nextMsg++
	pm := PlannedMessage{
		Ref:            ref,
		NID:            s.opts.IDs.NextNID(NIDTypeNormalMessage),
		Folder:         folder,
		Subject:        msg.Subject,
		Class:          class,
		BodyTextLen:    len(msg.BodyText),
		BodyHTMLLen:    len(msg.BodyHTML),
		HeadersLen:     len(msg.InternetHeaders),
		BodyTextSHA256: sha256Hex(msg.BodyText),
		BodyHTMLSHA256: sha256Hex(msg.BodyHTML),
		HeadersSHA256:  sha256Hex(msg.InternetHeaders),
		From:           planRecip(msg.From),
		To:             planRecips(msg.To),
		Cc:             planRecips(msg.Cc),
		Bcc:            planRecips(msg.Bcc),
		SentNano:       ts(msg.Sent),
		RecvNano:       ts(msg.Received),
		CreatedNano:    ts(msg.Created),
		ModifiedNano:   ts(msg.Modified),
		Read:           msg.Read,
		Draft:          msg.Draft,
		Importance:     msg.Importance,
		Sensitivity:    msg.Sensitivity,
		InternetID:     msg.InternetMessageID,
		SearchKey:      keyHex(s.opts.IDs.NextSearchKey()),
		RecordKey:      keyHex(s.opts.IDs.NextRecordKey()),
		Attachments:    atts,
	}
	s.plan.Messages = append(s.plan.Messages, pm)
	s.content[ref] = MessageContent{
		BodyText:        msg.BodyText,
		BodyHTML:        msg.BodyHTML,
		InternetHeaders: msg.InternetHeaders,
		Attachments:     stored,
	}
	return ref, nil
}

func planRecip(r Recipient) PlannedRecipient {
	return PlannedRecipient{Name: r.Name, Email: r.Email, Type: r.Type}
}

func planRecips(in []Recipient) []PlannedRecipient {
	if len(in) == 0 {
		return []PlannedRecipient{}
	}
	out := make([]PlannedRecipient, len(in))
	for i, r := range in {
		out[i] = planRecip(r)
	}
	return out
}

func validateImportance(v Importance) error {
	switch v {
	case ImportanceLow, ImportanceNormal, ImportanceHigh:
		return nil
	default:
		return invalidArg("importance", "PidTagImportance must be 0=low, 1=normal, or 2=high; got %d", v)
	}
}

func validateSensitivity(v Sensitivity) error {
	switch v {
	case SensitivityNormal, SensitivityPersonal, SensitivityPrivate, SensitivityConfidential:
		return nil
	default:
		return invalidArg("sensitivity", "PidTagSensitivity must be 0=normal, 1=personal, 2=private, or 3=confidential; got %d", v)
	}
}

func snapshotAttachments(in []AttachmentSpec, lim Limits, remain int64) ([]PlannedAttachment, []AttachmentContent, int64, error) {
	if len(in) == 0 {
		return []PlannedAttachment{}, []AttachmentContent{}, 0, nil
	}
	out := make([]PlannedAttachment, 0, len(in))
	stored := make([]AttachmentContent, 0, len(in))
	var total int64
	for i, a := range in {
		if a.OLE {
			return nil, nil, 0, unsupported(FeatureOLE, fmt.Sprintf("attachment %d", i))
		}
		if a.Embedded != nil {
			return nil, nil, 0, unsupported(FeatureEmbeddedMessage, fmt.Sprintf("attachment %d", i))
		}
		if a.Body == nil {
			return nil, nil, 0, invalidArg("body", "attachment %d has nil Body", i)
		}
		budget := lim.MaxAttachmentBytes
		left := remain - total
		if left < budget {
			budget = left
		}
		if budget < 0 {
			budget = 0
		}
		if a.Size > 0 {
			if a.Size > lim.MaxAttachmentBytes {
				return nil, nil, 0, limitErr("MaxAttachmentBytes", "attachment %d declared size %d exceeds %d", i, a.Size, lim.MaxAttachmentBytes)
			}
			if a.Size > left {
				return nil, nil, 0, limitErr("MaxMessageBytes", "attachment %d declared size %d exceeds remaining %d", i, a.Size, left)
			}
			budget = a.Size
		}
		data, err := readBounded(a.Body, budget)
		if err != nil {
			if errors.Is(err, ErrLimit) {
				if a.Size > 0 {
					return nil, nil, 0, invalidArg("size", "attachment %d read more than declared size %d", i, a.Size)
				}
				if left < lim.MaxAttachmentBytes {
					return nil, nil, 0, limitErr("MaxMessageBytes", "attachment %d exceeds remaining message budget %d", i, left)
				}
				return nil, nil, 0, limitErr("MaxAttachmentBytes", "attachment %d exceeds %d bytes (stopped after %d)", i, lim.MaxAttachmentBytes, budget+1)
			}
			return nil, nil, 0, err
		}
		n := int64(len(data))
		if a.Size > 0 && n != a.Size {
			return nil, nil, 0, invalidArg("size", "attachment %d size %d != read %d", i, a.Size, n)
		}
		sum := sha256.Sum256(data)
		hex := fmt.Sprintf("%x", sum[:])
		total += n
		out = append(out, PlannedAttachment{
			Filename:  a.Filename,
			MIMEType:  a.MIMEType,
			ContentID: a.ContentID,
			Inline:    a.Inline,
			Size:      n,
			SHA256:    hex,
		})
		stored = append(stored, AttachmentContent{
			Filename:  a.Filename,
			MIMEType:  a.MIMEType,
			ContentID: a.ContentID,
			Inline:    a.Inline,
			SHA256:    hex,
			Bytes:     data,
		})
	}
	return out, stored, total, nil
}

// readBounded copies at most cap bytes. Reading cap+1 bytes is a limit error
// so a huge/unbounded stream is not consumed to EOF.
func readBounded(r io.Reader, capn int64) ([]byte, error) {
	if capn < 0 {
		capn = 0
	}
	var buf bytes.Buffer
	n, err := buf.ReadFrom(io.LimitReader(r, capn+1))
	if err != nil {
		return nil, &Error{Code: CodeIO, Detail: err.Error(), Err: err}
	}
	if n > capn {
		return nil, limitErr("read", "payload exceeds %d bytes", capn)
	}
	return buf.Bytes(), nil
}

func rejectUnsupportedMessage(msg MessageSpec) error {
	class := msg.Class
	if class == "" {
		class = "IPM.Note"
	}
	switch {
	case strings.HasPrefix(class, "IPM.Appointment"), strings.HasPrefix(class, "IPM.Schedule"):
		return unsupported(FeatureCalendar, class)
	case strings.HasPrefix(class, "IPM.Contact"):
		return unsupported(FeatureContact, class)
	case strings.HasPrefix(class, "IPM.Task"):
		return unsupported(FeatureTask, class)
	case strings.HasPrefix(class, "IPM.Note"):
		return nil
	default:
		return unsupported(Feature(class), "message class "+class+" is not in the email-export contract")
	}
}

func (s *session) Plan() Plan { return s.plan }

func (s *session) EncodePlan() ([]byte, error) { return EncodePlan(s.plan) }

func (s *session) MessageContent(ref MessageRef) (MessageContent, error) {
	if err := s.check(); err != nil {
		return MessageContent{}, err
	}
	c, ok := s.content[ref]
	if !ok {
		return MessageContent{}, invalidArg("ref", "unknown message ref %d", ref)
	}
	return c, nil
}

func (s *session) Finalize(ctx context.Context) error {
	if err := s.check(); err != nil {
		return err
	}
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	n := NewNDB(nil)
	spec := MinimumSpec{DisplayName: s.plan.DisplayName, Now: time.Unix(0, s.plan.CreatedNano).UTC()}
	if err := WriteMinimum(n, spec); err != nil {
		return err
	}
	tree, err := OpenFolderTree(n, spec.Now)
	if err != nil {
		return err
	}
	nids, err := tree.CreateFromPlan(s.plan.Folders)
	if err != nil {
		return err
	}
	if err := tree.CreateMessagesFromPlan(nids, s.plan.Messages, s.content); err != nil {
		return err
	}
	return n.CommitTo(s.opts.Sink)
}

func (s *session) Close() error {
	s.closed = true
	if s.opts.Sink != nil {
		return s.opts.Sink.Close()
	}
	return nil
}
