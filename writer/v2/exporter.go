package writer

import (
	"context"
	"crypto/sha256"
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
	if spec.Name == "" {
		return 0, invalidArg("name", "folder name is required")
	}
	parent := spec.Parent
	if parent == 0 {
		parent = IPMSubtreeRef
	}
	p, ok := s.folders[parent]
	if !ok {
		return 0, invalidArg("parent", "unknown folder ref %d", parent)
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
	atts, attBytes, err := snapshotAttachments(msg.Attachments, s.opts.Limits)
	if err != nil {
		return 0, err
	}
	bodyBytes := int64(len(msg.BodyText) + len(msg.BodyHTML) + len(msg.InternetHeaders))
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
		Ref:          ref,
		NID:          s.opts.IDs.NextNID(NIDTypeNormalMessage),
		Folder:       folder,
		Subject:      msg.Subject,
		Class:        class,
		BodyTextLen:  len(msg.BodyText),
		BodyHTMLLen:  len(msg.BodyHTML),
		From:         planRecip(msg.From),
		To:           planRecips(msg.To),
		Cc:           planRecips(msg.Cc),
		Bcc:          planRecips(msg.Bcc),
		SentNano:     ts(msg.Sent),
		RecvNano:     ts(msg.Received),
		CreatedNano:  ts(msg.Created),
		ModifiedNano: ts(msg.Modified),
		Read:         msg.Read,
		Draft:        msg.Draft,
		Importance:   msg.Importance,
		InternetID:   msg.InternetMessageID,
		SearchKey:    keyHex(s.opts.IDs.NextSearchKey()),
		RecordKey:    keyHex(s.opts.IDs.NextRecordKey()),
		Attachments:  atts,
	}
	s.plan.Messages = append(s.plan.Messages, pm)
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

func snapshotAttachments(in []AttachmentSpec, lim Limits) ([]PlannedAttachment, int64, error) {
	if len(in) == 0 {
		return []PlannedAttachment{}, 0, nil
	}
	out := make([]PlannedAttachment, 0, len(in))
	var total int64
	for i, a := range in {
		if a.OLE {
			return nil, 0, unsupported(FeatureOLE, fmt.Sprintf("attachment %d", i))
		}
		if a.Embedded != nil {
			return nil, 0, unsupported(FeatureEmbeddedMessage, fmt.Sprintf("attachment %d", i))
		}
		if a.Body == nil {
			return nil, 0, invalidArg("body", "attachment %d has nil Body", i)
		}
		sum := sha256.New()
		n, err := io.Copy(sum, a.Body)
		if err != nil {
			return nil, 0, &Error{Code: CodeIO, Detail: err.Error(), Err: err}
		}
		if a.Size > 0 && n != a.Size {
			return nil, 0, invalidArg("size", "attachment %d size %d != read %d", i, a.Size, n)
		}
		if n > lim.MaxAttachmentBytes {
			return nil, 0, limitErr("MaxAttachmentBytes", "attachment %d is %d bytes", i, n)
		}
		total += n
		out = append(out, PlannedAttachment{
			Filename:  a.Filename,
			MIMEType:  a.MIMEType,
			ContentID: a.ContentID,
			Inline:    a.Inline,
			Size:      n,
			SHA256:    fmt.Sprintf("%x", sum.Sum(nil)),
		})
	}
	return out, total, nil
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

func (s *session) Finalize(ctx context.Context) error {
	if err := s.check(); err != nil {
		return err
	}
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	return &Error{
		Code:   CodeNotImplemented,
		Detail: "Unicode PST codecs are implemented by PST-002 and later; this card only locks the contract and oracle",
		Err:    ErrNotImplemented,
	}
}

func (s *session) Close() error {
	s.closed = true
	if s.opts.Sink != nil {
		return s.opts.Sink.Close()
	}
	return nil
}
