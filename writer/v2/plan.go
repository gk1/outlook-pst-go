package writer

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

// Plan is the canonical, JSON-encoded description of an export. Fixture
// generation is this document: same Clock + IDSource => identical bytes.
// Bodies and attachment payloads are hashed here and retained separately
// via MessageContent.
type Plan struct {
	Version     int              `json:"version"`
	DisplayName string           `json:"display_name"`
	CreatedNano int64            `json:"created_unix_nano"`
	Crypt       byte             `json:"crypt_method"`
	Folders     []PlannedFolder  `json:"folders"`
	Messages    []PlannedMessage `json:"messages"`
}

// PlannedFolder is one folder in the export plan.
type PlannedFolder struct {
	Ref    FolderRef `json:"ref"`
	NID    uint32    `json:"nid"`
	Parent FolderRef `json:"parent"`
	Name   string    `json:"name"`
	Depth  int       `json:"depth"`
}

// PlannedRecipient is a recipient snapshot.
type PlannedRecipient struct {
	Name  string    `json:"name"`
	Email string    `json:"email"`
	Type  RecipType `json:"type"`
}

// PlannedAttachment is an attachment snapshot (content hashed, bytes retained
// on the exporter until Finalize).
type PlannedAttachment struct {
	Filename  string          `json:"filename"`
	MIMEType  string          `json:"mime_type"`
	ContentID string          `json:"content_id,omitempty"`
	Inline    bool            `json:"inline,omitempty"`
	Size      int64           `json:"size"`
	SHA256    string          `json:"sha256"`
	Embedded  *PlannedMessage `json:"embedded,omitempty"`
}

// PlannedMessage is one message in the export plan.
type PlannedMessage struct {
	Ref            MessageRef          `json:"ref"`
	NID            uint32              `json:"nid"`
	Folder         FolderRef           `json:"folder"`
	Subject        string              `json:"subject"`
	Class          string              `json:"class"`
	BodyTextLen    int                 `json:"body_text_len"`
	BodyHTMLLen    int                 `json:"body_html_len"`
	HeadersLen     int                 `json:"internet_headers_len"`
	BodyTextSHA256 string              `json:"body_text_sha256"`
	BodyHTMLSHA256 string              `json:"body_html_sha256"`
	HeadersSHA256  string              `json:"internet_headers_sha256"`
	From           PlannedRecipient    `json:"from"`
	To             []PlannedRecipient  `json:"to"`
	Cc             []PlannedRecipient  `json:"cc"`
	Bcc            []PlannedRecipient  `json:"bcc"`
	SentNano       int64               `json:"sent_unix_nano"`
	RecvNano       int64               `json:"received_unix_nano"`
	CreatedNano    int64               `json:"created_unix_nano"`
	ModifiedNano   int64               `json:"modified_unix_nano"`
	Read           bool                `json:"read"`
	Draft          bool                `json:"draft"`
	Importance     Importance          `json:"importance"`
	Sensitivity    Sensitivity         `json:"sensitivity"`
	InternetID     string              `json:"internet_message_id,omitempty"`
	SearchKey      string              `json:"search_key"`
	RecordKey      string              `json:"record_key"`
	Attachments    []PlannedAttachment `json:"attachments"`
}

// EncodePlan marshals p with a trailing newline. Field order is the struct
// order; slices are insertion order; there are no maps.
func EncodePlan(p Plan) ([]byte, error) {
	buf := &bytes.Buffer{}
	enc := json.NewEncoder(buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "")
	if err := enc.Encode(p); err != nil {
		return nil, &Error{Code: CodeInvalidArg, Detail: err.Error(), Err: err}
	}
	return buf.Bytes(), nil
}

func keyHex(k [16]byte) string { return hex.EncodeToString(k[:]) }

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return fmt.Sprintf("%x", sum[:])
}
