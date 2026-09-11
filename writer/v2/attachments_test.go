package writer

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	outlookpst "github.com/grokify/outlook-pst-go"

	"github.com/grokify/outlook-pst-go/writer/v2/interop"
)

// 1x1 transparent PNG.
var png1x1 = []byte{
	0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52,
	0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01, 0x08, 0x06, 0x00, 0x00, 0x00, 0x1f, 0x15, 0xc4,
	0x89, 0x00, 0x00, 0x00, 0x0d, 0x49, 0x44, 0x41, 0x54, 0x78, 0x9c, 0x63, 0x00, 0x01, 0x00, 0x00,
	0x05, 0x00, 0x01, 0x0d, 0x0a, 0x2d, 0xb4, 0x00, 0x00, 0x00, 0x00, 0x49, 0x45, 0x4e, 0x44, 0xae,
	0x42, 0x60, 0x82,
}

func collectAttach(t *testing.T, m *outlookpst.Message) []*outlookpst.Attachment {
	t.Helper()
	var out []*outlookpst.Attachment
	for a, err := range m.Attachments() {
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, a)
	}
	return out
}

func TestAttachmentFixturesRoundTrip(t *testing.T) {
	n, tree, path := blankTree(t)
	inbox, err := tree.Create(tree.IPM(), "Inbox")
	if err != nil {
		t.Fatal(err)
	}
	large := bytes.Repeat([]byte{'A'}, MaxDataBlockCB+64)
	largeSum := sha256.Sum256(large)
	unicodeName := "テスト.txt"
	_, err = tree.CreateMessage(inbox, MessageWrite{
		Subject:  "atts",
		BodyHTML: `<img src="cid:img1">`,
		Read:     true,
		Attachments: []AttachmentWrite{
			{Filename: "empty.bin", MIMEType: "application/octet-stream", Data: []byte{}},
			{Filename: unicodeName, MIMEType: "text/plain", Data: []byte("こんにちは")},
			{Filename: "pixel.png", MIMEType: "image/png", ContentID: "img1", Inline: true, Data: png1x1},
			{Filename: "note.txt", MIMEType: "text/plain", Data: []byte("one")},
			{Filename: "note.txt", MIMEType: "text/plain", Data: []byte("two")},
			{Filename: "large.bin", MIMEType: "application/octet-stream", Data: large},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	pc, err := OpenPC(n, RelatedNID(inbox, NIDTypeContentsTable))
	if err == nil {
		_ = pc
	}
	ct, err := OpenTC(n, RelatedNID(inbox, NIDTypeContentsTable))
	if err != nil {
		t.Fatal(err)
	}
	ids, err := ct.RowIDs()
	if err != nil || len(ids) != 1 {
		t.Fatalf("contents rows %v %v", ids, err)
	}
	has, err := ct.GetBool(ids[0], PidTagHasAttachments)
	if err != nil || !has {
		t.Fatalf("HasAttachments %v %v", has, err)
	}
	msgPC, err := OpenPC(n, ids[0])
	if err != nil {
		t.Fatal(err)
	}
	flags, err := msgPC.GetInt32(PidTagMessageFlags)
	if err != nil || flags&MsgFlagHasAttach == 0 {
		t.Fatalf("MSGFLAG_HASATTACH %#x %v", flags, err)
	}
	commitTree(t, n, path)

	pst, err := outlookpst.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer pst.Close()
	root, err := pst.RootFolder()
	if err != nil {
		t.Fatal(err)
	}
	in := findFolder(t, root, "Min Store", "Inbox")
	msgs := collectMessages(t, in)
	if len(msgs) != 1 {
		t.Fatalf("msgs %d", len(msgs))
	}
	atts := collectAttach(t, msgs[0])
	if len(atts) != 6 {
		t.Fatalf("atts %d", len(atts))
	}

	zero := atts[0]
	if n, err := zero.Size(); err != nil || n != 0 {
		t.Fatalf("zero size %d %v", n, err)
	}
	if data, err := zero.Data(); err != nil || len(data) != 0 {
		t.Fatalf("zero data %d %v", len(data), err)
	}

	uni := atts[1]
	if name, err := uni.Filename(); err != nil || name != unicodeName {
		t.Fatalf("unicode name %q %v", name, err)
	}
	if data, err := uni.Data(); err != nil || string(data) != "こんにちは" {
		t.Fatalf("unicode data %q %v", data, err)
	}

	img := atts[2]
	if cid, err := img.ContentID(); err != nil || cid != "img1" {
		t.Fatalf("cid %q %v", cid, err)
	}
	if mime, err := img.MimeType(); err != nil || mime != "image/png" {
		t.Fatalf("mime %q %v", mime, err)
	}
	if data, err := img.Data(); err != nil || !bytes.Equal(data, png1x1) {
		t.Fatalf("png %v", err)
	}

	if n1, _ := atts[3].Filename(); n1 != "note.txt" {
		t.Fatalf("dup1 %q", n1)
	}
	if n2, _ := atts[4].Filename(); n2 != "note.txt" {
		t.Fatalf("dup2 %q", n2)
	}
	d1, _ := atts[3].Data()
	d2, _ := atts[4].Data()
	if string(d1) != "one" || string(d2) != "two" {
		t.Fatalf("dup bodies %q %q", d1, d2)
	}

	lg := atts[5]
	got, err := lg.Data()
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(got)
	if sum != largeSum {
		t.Fatalf("large hash %x want %x (len %d)", sum, largeSum, len(got))
	}
}

func TestEmbeddedMessageRoundTrip(t *testing.T) {
	n, tree, path := blankTree(t)
	inbox, err := tree.Create(tree.IPM(), "Inbox")
	if err != nil {
		t.Fatal(err)
	}
	inner := &MessageWrite{
		Subject:  "inner",
		BodyText: "nested body",
		From:     Recipient{Name: "Pat", Email: "pat@example.com"},
		To:       []Recipient{{Name: "Sam", Email: "sam@example.com"}},
		Read:     true,
		Attachments: []AttachmentWrite{
			{Filename: "inner.txt", MIMEType: "text/plain", Data: []byte("leaf")},
		},
	}
	_, err = tree.CreateMessage(inbox, MessageWrite{
		Subject:  "outer",
		BodyText: "see attached message",
		Read:     true,
		Attachments: []AttachmentWrite{
			{Filename: "forwarded.msg", MIMEType: "message/rfc822", Embedded: inner},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	commitTree(t, n, path)
	pst, err := outlookpst.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer pst.Close()
	root, err := pst.RootFolder()
	if err != nil {
		t.Fatal(err)
	}
	in := findFolder(t, root, "Min Store", "Inbox")
	msgs := collectMessages(t, in)
	atts := collectAttach(t, msgs[0])
	if len(atts) != 1 {
		t.Fatalf("atts %d", len(atts))
	}
	ok, err := atts[0].IsEmbeddedMessage()
	if err != nil || !ok {
		t.Fatalf("embedded %v %v", ok, err)
	}
	emb, err := atts[0].OpenAsMessage()
	if err != nil {
		t.Fatal(err)
	}
	if s, _ := emb.Subject(); s != "inner" {
		t.Fatalf("subject %q", s)
	}
	if b, _ := emb.Body(); b != "nested body" {
		t.Fatalf("body %q", b)
	}
	innerAtts := collectAttach(t, emb)
	if len(innerAtts) != 1 {
		t.Fatalf("inner atts %d", len(innerAtts))
	}
	if data, err := innerAtts[0].Data(); err != nil || string(data) != "leaf" {
		t.Fatalf("leaf %q %v", data, err)
	}
}

func TestEmbeddedCycleAndDepthRejected(t *testing.T) {
	_, tree, _ := blankTree(t)
	inbox, err := tree.Create(tree.IPM(), "Inbox")
	if err != nil {
		t.Fatal(err)
	}
	outer := &MessageWrite{Subject: "o"}
	inner := &MessageWrite{Subject: "i"}
	outer.Attachments = []AttachmentWrite{{Filename: "i.msg", Embedded: inner}}
	inner.Attachments = []AttachmentWrite{{Filename: "o.msg", Embedded: outer}}
	if _, err := tree.CreateMessage(inbox, *outer); !errors.Is(err, ErrInvalidArg) {
		t.Fatalf("cycle: %v", err)
	}

	cur := &MessageWrite{Subject: "leaf", BodyText: "x"}
	for i := 0; i < MaxEmbeddedDepthDefault+1; i++ {
		next := &MessageWrite{Subject: "n", Attachments: []AttachmentWrite{{Filename: "e.msg", Embedded: cur}}}
		cur = next
	}
	if _, err := tree.CreateMessage(inbox, *cur); !errors.Is(err, ErrLimit) {
		t.Fatalf("depth: %v", err)
	}
}

func TestAttachmentExporterFinalizeRoundTrip(t *testing.T) {
	sink := NewMemSink("mail")
	exp, err := New(Options{
		Sink:        sink,
		DisplayName: "Min Store",
		Clock:       FixedClock{T: time.Unix(1_700_000_000, 0).UTC()},
		IDs:         NewSequentialIDs(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer exp.Close()
	if err := exp.CreateMailbox(Mailbox{DisplayName: "Min Store"}); err != nil {
		t.Fatal(err)
	}
	inbox, err := exp.CreateFolder(FolderSpec{Name: "Inbox"})
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte("attach-body")
	inner := &MessageSpec{Subject: "inner", BodyText: "nested"}
	_, err = exp.CreateMessage(inbox, MessageSpec{
		Subject:  "exported-att",
		BodyText: "plain",
		Read:     true,
		Attachments: []AttachmentSpec{
			{Filename: "note.txt", MIMEType: "text/plain", Size: int64(len(payload)), Body: bytes.NewReader(payload)},
			{Filename: "fwd.msg", MIMEType: "message/rfc822", Embedded: inner},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := exp.Finalize(context.Background()); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "export.pst")
	if err := os.WriteFile(path, sink.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	pst, err := outlookpst.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer pst.Close()
	root, err := pst.RootFolder()
	if err != nil {
		t.Fatal(err)
	}
	in := findFolder(t, root, "Min Store", "Inbox")
	msgs := collectMessages(t, in)
	atts := collectAttach(t, msgs[0])
	if len(atts) != 2 {
		t.Fatalf("atts %d", len(atts))
	}
	data, err := atts[0].Data()
	if err != nil || !bytes.Equal(data, payload) {
		t.Fatalf("bytes %q %v", data, err)
	}
	ok, err := atts[1].IsEmbeddedMessage()
	if err != nil || !ok {
		t.Fatalf("embedded %v %v", ok, err)
	}
	emb, err := atts[1].OpenAsMessage()
	if err != nil {
		t.Fatal(err)
	}
	if s, _ := emb.Subject(); s != "inner" {
		t.Fatalf("inner %q", s)
	}
}

func TestAttachmentTreeInterop(t *testing.T) {
	n, tree, path := blankTree(t)
	inbox, err := tree.Create(tree.IPM(), "Inbox")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tree.CreateMessage(inbox, MessageWrite{
		Subject: "interop-att",
		Read:    true,
		Attachments: []AttachmentWrite{
			{Filename: "a.txt", MIMEType: "text/plain", Data: []byte("hello")},
		},
	}); err != nil {
		t.Fatal(err)
	}
	commitTree(t, n, path)
	m, err := interop.Run(context.Background(), path, interop.Options{})
	if err != nil {
		t.Fatal(err)
	}
	var self bool
	for _, r := range m.Results {
		switch r.Tool {
		case interop.ToolSelfReader:
			self = true
			if r.Status != interop.StatusPass {
				t.Fatalf("self reader %s %s", r.Status, r.Log)
			}
		case interop.ToolLibpff:
			if r.Status == interop.StatusFail || r.Status == interop.StatusError {
				t.Fatalf("libpff %s %s", r.Status, r.Log)
			}
		case interop.ToolOutlook, interop.ToolScanPST:
			if r.Status != interop.StatusSkipped && r.Status != interop.StatusPass {
				t.Fatalf("%s %s %s", r.Tool, r.Status, r.Log)
			}
		}
	}
	if !self {
		t.Fatal("missing self-reader result")
	}
	if bin, err := exec.LookPath("pffinfo"); err == nil {
		out, err := exec.Command(bin, path).CombinedOutput()
		if err != nil {
			t.Fatalf("pffinfo: %v %s", err, out)
		}
		if !strings.Contains(string(out), "Inbox") {
			t.Logf("pffinfo output: %s", out)
		}
	}
}

func TestAttachmentPlanGoldenUnchanged(t *testing.T) {
	plan, raw, err := Generate(CaseAttachment, testOpts())
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Messages) != 1 || len(plan.Messages[0].Attachments) != 1 {
		t.Fatalf("plan %+v", plan.Messages)
	}
	sum := sha256.Sum256(raw)
	want, err := os.ReadFile(filepath.Join("testdata", "goldens", "attachment.sha256"))
	if err != nil {
		t.Fatal(err)
	}
	if hex.EncodeToString(sum[:]) != string(bytes.TrimSpace(want)) {
		t.Fatalf("attachment golden sha %x want %s", sum, bytes.TrimSpace(want))
	}
}

func hasProp(cols []ColumnView, id uint16) bool {
	for _, c := range cols {
		if c.PropID == id {
			return true
		}
	}
	return false
}

func TestAttachmentTableTemplate(t *testing.T) {
	_, n := writeBlank(t, MinimumSpec{DisplayName: "Min Store"})
	if _, ok := n.LookupNode(uint64(NIDAttachmentTable)); !ok {
		t.Fatal("missing NID_ATTACHMENT_TABLE 0x671")
	}
	tc, err := OpenTC(n, NIDAttachmentTable)
	if err != nil {
		t.Fatal(err)
	}
	if tc.RowCount() != 0 {
		t.Fatalf("template rows %d, want 0", tc.RowCount())
	}
	cols := tc.Info().Columns
	for _, id := range []uint16{PidTagAttachSize, PidTagAttachFilename, PidTagAttachMethod, PidTagRenderingPosition, PidTagLtpRowId, PidTagLtpRowVer} {
		if !hasProp(cols, id) {
			t.Fatalf("template missing column 0x%04x", id)
		}
	}
}

func TestPerMessageAttachmentTableRenderingPosition(t *testing.T) {
	n, tree, path := blankTree(t)
	inbox, err := tree.Create(tree.IPM(), "Inbox")
	if err != nil {
		t.Fatal(err)
	}
	nid, err := tree.CreateMessage(inbox, MessageWrite{
		Subject: "pos",
		Read:    true,
		Attachments: []AttachmentWrite{
			{Filename: "file.bin", Data: []byte("x")},
			{Filename: "in.png", Inline: true, Data: png1x1},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	tbl, err := openSubTC(n, nid, RelatedNID(nid, NIDTypeAttachmentTable))
	if err != nil {
		t.Fatal(err)
	}
	if !hasProp(tbl.Info().Columns, PidTagRenderingPosition) {
		t.Fatal("per-message table missing PidTagRenderingPosition")
	}
	ids, err := tbl.RowIDs()
	if err != nil || len(ids) != 2 {
		t.Fatalf("rows %v %v", ids, err)
	}
	pos0, err := tbl.GetInt32(ids[0], PidTagRenderingPosition)
	if err != nil || pos0 != RenderingPositionNone {
		t.Fatalf("file pos %d %v", pos0, err)
	}
	pos1, err := tbl.GetInt32(ids[1], PidTagRenderingPosition)
	if err != nil || pos1 != 0 {
		t.Fatalf("inline pos %d %v", pos1, err)
	}
	commitTree(t, n, path)
}

func TestNoAttachmentTableWithoutAttachments(t *testing.T) {
	n, tree, _ := blankTree(t)
	inbox, err := tree.Create(tree.IPM(), "Inbox")
	if err != nil {
		t.Fatal(err)
	}
	nid, err := tree.CreateMessage(inbox, MessageWrite{Subject: "plain", BodyText: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	e, ok := n.LookupNode(uint64(nid))
	if !ok {
		t.Fatal("missing message")
	}
	want := RelatedNID(nid, NIDTypeAttachmentTable)
	err = n.WalkSubnodes(e.SubBID, func(s SLEntry) error {
		if uint32(s.NID) == want {
			t.Fatalf("empty message has attachment table subnode 0x%x", s.NID)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestAttachmentStreamingDoesNotRetainBytes(t *testing.T) {
	sink := NewMemSink("mail")
	exp, err := New(Options{
		Sink:        sink,
		DisplayName: "Min Store",
		Clock:       FixedClock{T: time.Unix(1_700_000_000, 0).UTC()},
		IDs:         NewSequentialIDs(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer exp.Close()
	if err := exp.CreateMailbox(Mailbox{DisplayName: "Min Store"}); err != nil {
		t.Fatal(err)
	}
	inbox, err := exp.CreateFolder(FolderSpec{Name: "Inbox"})
	if err != nil {
		t.Fatal(err)
	}
	payload := bytes.Repeat([]byte{'Q'}, MaxDataBlockCB+64)
	want := sha256.Sum256(payload)
	ref, err := exp.CreateMessage(inbox, MessageSpec{
		Subject: "stream",
		Attachments: []AttachmentSpec{{
			Filename: "large.bin",
			MIMEType: "application/octet-stream",
			Size:     int64(len(payload)),
			Body:     bytes.NewReader(payload),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := exp.MessageContent(ref)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Attachments) != 1 {
		t.Fatal(got.Attachments)
	}
	if len(got.Attachments[0].Bytes) != 0 {
		t.Fatalf("retained %d bytes in AttachmentContent.Bytes", len(got.Attachments[0].Bytes))
	}
	if got.Attachments[0].SHA256 != hex.EncodeToString(want[:]) {
		t.Fatalf("sha %s", got.Attachments[0].SHA256)
	}
	if err := exp.Finalize(context.Background()); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "stream.pst")
	if err := os.WriteFile(path, sink.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	pst, err := outlookpst.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer pst.Close()
	root, err := pst.RootFolder()
	if err != nil {
		t.Fatal(err)
	}
	in := findFolder(t, root, "Min Store", "Inbox")
	atts := collectAttach(t, collectMessages(t, in)[0])
	data, err := atts[0].Data()
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	if sum != want {
		t.Fatalf("reopen hash %x want %x", sum, want)
	}
}

func TestAllocateStreamDoesNotRetainPayload(t *testing.T) {
	n := NewNDB(nil)
	h := NewHeap(n, HeapSigPC)
	large := bytes.Repeat([]byte{'Z'}, HeapMaxAlloc+100)
	if _, err := h.AllocateStream(bytes.NewReader(large), int64(len(large))); err != nil {
		t.Fatal(err)
	}
	if len(h.subs) != 1 {
		t.Fatalf("subs %d", len(h.subs))
	}
	if len(h.subs[0].data) != 0 {
		t.Fatalf("heap retained %d payload bytes", len(h.subs[0].data))
	}
	if h.subs[0].dataBID == 0 {
		t.Fatal("missing streamed data BID")
	}
}

func TestNonSeekableAttachmentSpoolsNotBytes(t *testing.T) {
	exp, err := New(Options{
		DisplayName: "Min Store",
		Clock:       FixedClock{T: time.Unix(1_700_000_000, 0).UTC()},
		IDs:         NewSequentialIDs(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer exp.Close()
	inbox, err := exp.CreateFolder(FolderSpec{Name: "Inbox"})
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte("spooled-bytes")
	r := &countingReader{max: int64(len(payload))}
	ref, err := exp.CreateMessage(inbox, MessageSpec{
		Subject: "spool",
		Attachments: []AttachmentSpec{{
			Filename: "s.bin",
			Size:     int64(len(payload)),
			Body:     r,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := exp.MessageContent(ref)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Attachments[0].Bytes) != 0 {
		t.Fatalf("retained bytes %d", len(got.Attachments[0].Bytes))
	}
	if got.Attachments[0].Body == nil {
		t.Fatal("missing spool Body")
	}
}
