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

// AttachmentSpec is a by-value attachment. Body is read once during CreateMessage.
// Size, if > 0, is the expected byte count; a mismatch is ErrInvalidArg.
type AttachmentSpec struct {
	Filename  string
	MIMEType  string
	ContentID string
	Inline    bool
	Size      int64
	Body      io.Reader
	// Embedded is reserved for PST-014. Non-nil is rejected as unsupported.
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
	Importance        int // 0=normal, 1=personal, 2=private, 3=confidential — stored as-is
	Class             string
	Attachments       []AttachmentSpec
}

// Exporter is the minimal email-export contract. It records a deterministic
// Plan; PST codecs (PST-002+) materialize bytes on Finalize.
type Exporter interface {
	CreateMailbox(Mailbox) error
	CreateFolder(FolderSpec) (FolderRef, error)
	CreateMessage(folder FolderRef, msg MessageSpec) (MessageRef, error)
	Plan() Plan
	EncodePlan() ([]byte, error)
	Finalize(ctx context.Context) error
	Close() error
}
