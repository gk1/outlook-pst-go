package writer

import (
	"context"
	"io"
	"time"
)

// FolderRef is a stable handle assigned by the exporter.
type FolderRef uint32

// MessageRef is a stable handle assigned by the exporter.
type MessageRef uint32

const (
	// RootFolderRef is the PST root. User folders hang off IPMSubtreeRef.
	RootFolderRef FolderRef = 1
	// IPMSubtreeRef is the default parent for exported mail folders.
	IPMSubtreeRef FolderRef = 2
)

// RecipType is a MAPI recipient type (To/Cc/Bcc).
type RecipType int

const (
	RecipTo  RecipType = 1
	RecipCc  RecipType = 2
	RecipBcc RecipType = 3
)

// Importance is PidTagImportance (MS-OXCMSG / MS-OXPROPS).
// These are the on-wire MAPI values: 0=low, 1=normal, 2=high.
type Importance int

const (
	ImportanceLow    Importance = 0
	ImportanceNormal Importance = 1
	ImportanceHigh   Importance = 2
)

// Sensitivity is PidTagSensitivity, a distinct MAPI property from Importance.
// On-wire: 0=normal, 1=personal, 2=private, 3=confidential.
type Sensitivity int

const (
	SensitivityNormal       Sensitivity = 0
	SensitivityPersonal     Sensitivity = 1
	SensitivityPrivate      Sensitivity = 2
	SensitivityConfidential Sensitivity = 3
)

// Mailbox is the top-level export description.
type Mailbox struct {
	DisplayName string
}

// FolderSpec describes one mail folder.
type FolderSpec struct {
	Parent FolderRef // 0 means IPM subtree
	Name   string
}

// Recipient is a single SMTP recipient.
type Recipient struct {
	Name  string
	Email string
	Type  RecipType
}

// AttachmentSpec is a by-value attachment. Body is read once during CreateMessage
// with a bounded LimitReader; bytes are retained for Finalize.
// Size, if > 0, is the expected byte count; a mismatch is ErrInvalidArg.
type AttachmentSpec struct {
	Filename  string
	MIMEType  string
	ContentID string
	Inline    bool
	Size      int64
	Body      io.Reader
	// Embedded is a nested IPM.Note (ATTACH_EMBEDDED_MSG). Body must be nil.
	Embedded *MessageSpec
	// OLE is reserved; true is rejected as unsupported.
	OLE bool
}

// MessageSpec is a single IPM.Note. Missing timestamps use Clock.Now().
type MessageSpec struct {
	Subject           string
	BodyText          string
	BodyHTML          string
	InternetHeaders   string
	InternetMessageID string
	From              Recipient
	To                []Recipient
	Cc                []Recipient
	Bcc               []Recipient
	Sent              time.Time
	Received          time.Time
	Created           time.Time
	Modified          time.Time
	Read              bool
	Draft             bool
	Importance        Importance
	Sensitivity       Sensitivity
	Class             string
	Attachments       []AttachmentSpec
}

// AttachmentContent is the retained by-value payload for later codecs.
type AttachmentContent struct {
	Filename  string
	MIMEType  string
	ContentID string
	Inline    bool
	SHA256    string
	Bytes     []byte
	Embedded  *MessageContent
}

// MessageContent is the retained body/headers/attachments for a message.
// CreateMessage stores this so Finalize (PST-002+) can materialize PST bytes.
type MessageContent struct {
	BodyText        string
	BodyHTML        string
	InternetHeaders string
	Attachments     []AttachmentContent
}

// Exporter is the minimal email-export contract. It records a deterministic
// Plan and retains message content for the write lifecycle. PST codecs
// (PST-002+) materialize bytes on Finalize.
type Exporter interface {
	CreateMailbox(Mailbox) error
	CreateFolder(FolderSpec) (FolderRef, error)
	RenameFolder(ref FolderRef, name string) error
	MoveFolder(ref, parent FolderRef) error
	DeleteFolder(ref FolderRef) error
	CreateMessage(folder FolderRef, msg MessageSpec) (MessageRef, error)
	Plan() Plan
	EncodePlan() ([]byte, error)
	MessageContent(MessageRef) (MessageContent, error)
	Finalize(ctx context.Context) error
	Close() error
}
