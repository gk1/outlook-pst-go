package writer

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"
)

func TestFinalizeNotImplemented(t *testing.T) {
	exp, err := New(Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer exp.Close()
	err = exp.Finalize(context.Background())
	if !errors.Is(err, ErrNotImplemented) {
		t.Fatalf("got %v", err)
	}
}

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
