package writer

import (
	"bytes"
	"strings"
)

// Case is a named golden export scenario.
type Case string

const (
	CaseEmpty        Case = "empty"
	CaseOneFolder    Case = "one-folder"
	CaseOneMessage   Case = "one-message"
	CaseAttachment   Case = "attachment"
	CaseLargeMessage Case = "large-message"
)

// AllCases is the PST-001 fixture set.
func AllCases() []Case {
	return []Case{CaseEmpty, CaseOneFolder, CaseOneMessage, CaseAttachment, CaseLargeMessage}
}

// Generate builds a deterministic Plan for c using opts.Clock and opts.IDs.
func Generate(c Case, opts Options) (Plan, []byte, error) {
	exp, err := New(opts)
	if err != nil {
		return Plan{}, nil, err
	}
	defer func() { _ = exp.Close() }()
	if err := exp.CreateMailbox(Mailbox{DisplayName: opts.withDefaults().DisplayName}); err != nil {
		return Plan{}, nil, err
	}
	switch c {
	case CaseEmpty:
		// mailbox + system folders only
	case CaseOneFolder:
		if _, err := exp.CreateFolder(FolderSpec{Name: "Inbox"}); err != nil {
			return Plan{}, nil, err
		}
	case CaseOneMessage:
		inbox, err := exp.CreateFolder(FolderSpec{Name: "Inbox"})
		if err != nil {
			return Plan{}, nil, err
		}
		_, err = exp.CreateMessage(inbox, MessageSpec{
			Subject:           "Hello",
			BodyText:          "plain body",
			From:              Recipient{Name: "Alice", Email: "alice@example.com", Type: RecipTo},
			To:                []Recipient{{Name: "Bob", Email: "bob@example.com", Type: RecipTo}},
			InternetMessageID: "<hello@example.com>",
			Importance:        ImportanceNormal,
			Sensitivity:       SensitivityNormal,
		})
		if err != nil {
			return Plan{}, nil, err
		}
	case CaseAttachment:
		inbox, err := exp.CreateFolder(FolderSpec{Name: "Inbox"})
		if err != nil {
			return Plan{}, nil, err
		}
		payload := []byte("attachment-bytes")
		_, err = exp.CreateMessage(inbox, MessageSpec{
			Subject:     "with attachment",
			BodyText:    "see attached",
			From:        Recipient{Name: "Alice", Email: "alice@example.com"},
			To:          []Recipient{{Name: "Bob", Email: "bob@example.com", Type: RecipTo}},
			Importance:  ImportanceNormal,
			Sensitivity: SensitivityNormal,
			Attachments: []AttachmentSpec{{
				Filename: "note.txt",
				MIMEType: "text/plain",
				Size:     int64(len(payload)),
				Body:     bytes.NewReader(payload),
			}},
		})
		if err != nil {
			return Plan{}, nil, err
		}
	case CaseLargeMessage:
		inbox, err := exp.CreateFolder(FolderSpec{Name: "Inbox"})
		if err != nil {
			return Plan{}, nil, err
		}
		body := strings.Repeat("A", MaxDataBlockCB+64) // forces XBLOCK in later codecs
		_, err = exp.CreateMessage(inbox, MessageSpec{
			Subject:     "large",
			BodyText:    body,
			From:        Recipient{Name: "Alice", Email: "alice@example.com"},
			To:          []Recipient{{Name: "Bob", Email: "bob@example.com", Type: RecipTo}},
			Importance:  ImportanceHigh,
			Sensitivity: SensitivityPrivate,
		})
		if err != nil {
			return Plan{}, nil, err
		}
	default:
		return Plan{}, nil, invalidArg("case", "unknown fixture %q", c)
	}
	raw, err := exp.EncodePlan()
	if err != nil {
		return Plan{}, nil, err
	}
	return exp.Plan(), raw, nil
}
