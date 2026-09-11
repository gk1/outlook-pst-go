package writer

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
	"time"
)

func TestUnsupportedCalendar(t *testing.T) {
	exp, _ := New(Options{})
	defer exp.Close()
	inbox, err := exp.CreateFolder(FolderSpec{Name: "Cal"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = exp.CreateMessage(inbox, MessageSpec{Class: "IPM.Appointment", Subject: "meet"})
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("got %v", err)
	}
}

func TestUnsupportedOLE(t *testing.T) {
	exp, _ := New(Options{})
	defer exp.Close()
	_, err := exp.CreateMessage(IPMSubtreeRef, MessageSpec{
		Subject: "x",
		Attachments: []AttachmentSpec{{
			Filename: "x.bin",
			Body:     bytes.NewReader([]byte("a")),
			OLE:      true,
		}},
	})
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("got %v", err)
	}
}

func TestRecipientLimit(t *testing.T) {
	exp, _ := New(Options{Limits: Limits{MaxRecipientsPerMessage: 1, MaxMessages: 10, MaxFolders: 10}})
	defer exp.Close()
	_, err := exp.CreateMessage(IPMSubtreeRef, MessageSpec{
		Subject: "x",
		To:      []Recipient{{Email: "a@b.c", Type: RecipTo}, {Email: "d@e.f", Type: RecipTo}},
	})
	if !errors.Is(err, ErrLimit) {
		t.Fatalf("got %v", err)
	}
}

func TestWIPCryptRejectedAtNew(t *testing.T) {
	_, err := New(Options{Crypt: CryptWIP})
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("got %v", err)
	}
}

func TestClosed(t *testing.T) {
	exp, _ := New(Options{})
	_ = exp.Close()
	if err := exp.CreateMailbox(Mailbox{}); !errors.Is(err, ErrClosed) {
		t.Fatalf("got %v", err)
	}
}

func TestDeterministicIDsAndClock(t *testing.T) {
	opts := func() Options {
		return Options{
			Clock: FixedClock{T: time.Unix(1_700_000_000, 0).UTC()},
			IDs:   NewSequentialIDs(),
		}
	}
	a, err := New(opts())
	if err != nil {
		t.Fatal(err)
	}
	b, _ := New(opts())
	foldA, _ := a.CreateFolder(FolderSpec{Name: "Inbox"})
	foldB, _ := b.CreateFolder(FolderSpec{Name: "Inbox"})
	if foldA != foldB {
		t.Fatalf("refs %d vs %d", foldA, foldB)
	}
	_, _ = a.CreateMessage(foldA, MessageSpec{Subject: "s", BodyText: "b"})
	_, _ = b.CreateMessage(foldB, MessageSpec{Subject: "s", BodyText: "b"})
	pa, _ := a.EncodePlan()
	pb, _ := b.EncodePlan()
	if !bytes.Equal(pa, pb) {
		t.Fatalf("plans differ:\n%s\n%s", pa, pb)
	}
}

func TestMemSinkSeekWriteAt(t *testing.T) {
	s := NewMemSink("t")
	if _, err := s.WriteAt([]byte("abcd"), 10); err != nil {
		t.Fatal(err)
	}
	got := s.Bytes()
	if len(got) != 14 || string(got[10:]) != "abcd" {
		t.Fatalf("%q", got)
	}
}

func TestImportanceSplitFromSensitivity(t *testing.T) {
	exp, err := New(Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer exp.Close()
	inbox, err := exp.CreateFolder(FolderSpec{Name: "Inbox"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = exp.CreateMessage(inbox, MessageSpec{
		Subject:     "prio",
		Importance:  ImportanceHigh,
		Sensitivity: SensitivityConfidential,
	})
	if err != nil {
		t.Fatal(err)
	}
	p := exp.Plan()
	if p.Messages[0].Importance != ImportanceHigh {
		t.Fatalf("importance=%d", p.Messages[0].Importance)
	}
	if p.Messages[0].Sensitivity != SensitivityConfidential {
		t.Fatalf("sensitivity=%d", p.Messages[0].Sensitivity)
	}
	_, err = exp.CreateMessage(inbox, MessageSpec{Subject: "bad-imp", Importance: Importance(9)})
	if !errors.Is(err, ErrInvalidArg) {
		t.Fatalf("importance: %v", err)
	}
	_, err = exp.CreateMessage(inbox, MessageSpec{Subject: "bad-sens", Sensitivity: Sensitivity(9)})
	if !errors.Is(err, ErrInvalidArg) {
		t.Fatalf("sensitivity: %v", err)
	}
}

func TestMessageContentRetainedForFinalize(t *testing.T) {
	exp, err := New(Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer exp.Close()
	inbox, err := exp.CreateFolder(FolderSpec{Name: "Inbox"})
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte("attach-body")
	ref, err := exp.CreateMessage(inbox, MessageSpec{
		Subject:         "keep",
		BodyText:        "plain body",
		BodyHTML:        "<p>html</p>",
		InternetHeaders: "X-Test: 1\r\n",
		Importance:      ImportanceNormal,
		Sensitivity:     SensitivityPersonal,
		Attachments: []AttachmentSpec{{
			Filename: "note.txt",
			MIMEType: "text/plain",
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
	if got.BodyText != "plain body" || got.BodyHTML != "<p>html</p>" || got.InternetHeaders != "X-Test: 1\r\n" {
		t.Fatalf("content %+v", got)
	}
	if len(got.Attachments) != 1 || !bytes.Equal(got.Attachments[0].Bytes, payload) {
		t.Fatalf("attachment %+v", got.Attachments)
	}
	p := exp.Plan()
	if p.Messages[0].BodyTextSHA256 == "" || p.Messages[0].HeadersSHA256 == "" {
		t.Fatal("plan missing content hashes")
	}
	if err := exp.Finalize(context.Background()); err != nil {
		t.Fatalf("finalize: %v", err)
	}
	still, err := exp.MessageContent(ref)
	if err != nil {
		t.Fatal(err)
	}
	if still.BodyText != got.BodyText {
		t.Fatal("Finalize discarded retained content")
	}
}

type countingReader struct {
	n, max int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	if c.max > 0 && c.n >= c.max {
		return 0, io.EOF
	}
	for i := range p {
		if c.max > 0 && c.n >= c.max {
			return i, io.EOF
		}
		p[i] = 'x'
		c.n++
	}
	return len(p), nil
}

func TestAttachmentLimitStopsBeforeEOF(t *testing.T) {
	exp, err := New(Options{Limits: Limits{MaxAttachmentBytes: 32}})
	if err != nil {
		t.Fatal(err)
	}
	defer exp.Close()
	inbox, err := exp.CreateFolder(FolderSpec{Name: "Inbox"})
	if err != nil {
		t.Fatal(err)
	}
	r := &countingReader{}
	_, err = exp.CreateMessage(inbox, MessageSpec{
		Subject: "huge",
		Attachments: []AttachmentSpec{{
			Filename: "big.bin",
			Body:     r,
		}},
	})
	if !errors.Is(err, ErrLimit) {
		t.Fatalf("got %v", err)
	}
	if r.n != 33 {
		t.Fatalf("read %d bytes, want 33 (cap+1); must not consume to EOF", r.n)
	}
}

func TestDeclaredAttachmentSizeCheckedBeforeRead(t *testing.T) {
	exp, err := New(Options{Limits: Limits{MaxAttachmentBytes: 32}})
	if err != nil {
		t.Fatal(err)
	}
	defer exp.Close()
	inbox, err := exp.CreateFolder(FolderSpec{Name: "Inbox"})
	if err != nil {
		t.Fatal(err)
	}
	r := &countingReader{}
	_, err = exp.CreateMessage(inbox, MessageSpec{
		Subject: "declared",
		Attachments: []AttachmentSpec{{
			Filename: "big.bin",
			Size:     1000,
			Body:     r,
		}},
	})
	if !errors.Is(err, ErrLimit) {
		t.Fatalf("got %v", err)
	}
	if r.n != 0 {
		t.Fatalf("declared oversize should fail before read, read %d", r.n)
	}
}
