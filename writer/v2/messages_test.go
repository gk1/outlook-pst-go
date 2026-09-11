package writer

import (
	"bytes"
	"context"
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

func findFolder(t *testing.T, root *outlookpst.Folder, names ...string) *outlookpst.Folder {
	t.Helper()
	cur := root
	for _, name := range names {
		next, err := cur.FindSubfolder(name)
		if err != nil {
			t.Fatalf("find %q under %v: %v", name, names, err)
		}
		cur = next
	}
	return cur
}

func collectMessages(t *testing.T, folder *outlookpst.Folder) []*outlookpst.Message {
	t.Helper()
	var out []*outlookpst.Message
	for m, err := range folder.Messages() {
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, m)
	}
	return out
}

func collectRecips(t *testing.T, m *outlookpst.Message) []outlookpst.Recipient {
	t.Helper()
	var out []outlookpst.Recipient
	for r, err := range m.Recipients() {
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, *r)
	}
	return out
}

func TestMessageZeroOneManyRecipientsRoundTrip(t *testing.T) {
	n, tree, path := blankTree(t)
	inbox, err := tree.Create(tree.IPM(), "Inbox")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_700_000_000, 0).UTC()
	zero, err := tree.CreateMessage(inbox, MessageWrite{
		Subject:  "empty-to",
		BodyText: "nobody",
		Read:     true,
		Sent:     now,
		Received: now,
		Created:  now,
		Modified: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	one, err := tree.CreateMessage(inbox, MessageWrite{
		Subject:  "one-to",
		BodyText: "hi",
		From:     Recipient{Name: "Alice", Email: "alice@example.com"},
		To:       []Recipient{{Name: "Bob", Email: "bob@example.com"}},
		Read:     true,
		Sent:     now,
		Received: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	many, err := tree.CreateMessage(inbox, MessageWrite{
		Subject:    "many",
		BodyHTML:   "<p>html</p>",
		Headers:    "From: alice@example.com\r\nTo: bob@example.com\r\n",
		InternetID: "<id@example.com>",
		From:       Recipient{Name: "Alice", Email: "alice@example.com"},
		To: []Recipient{
			{Name: "Bob", Email: "bob@example.com"},
			{Name: "Carol", Email: "carol@example.com"},
		},
		Cc:          []Recipient{{Name: "Dan", Email: "dan@example.com"}},
		Bcc:         []Recipient{{Email: "eve@example.com"}},
		Read:        true,
		Importance:  ImportanceHigh,
		Sensitivity: SensitivityPrivate,
		Sent:        now.Add(time.Minute),
		Received:    now.Add(2 * time.Minute),
		SearchKey:   bytes.Repeat([]byte{0x11}, 16),
		RecordKey:   bytes.Repeat([]byte{0x22}, 16),
	})
	if err != nil {
		t.Fatal(err)
	}

	pc, err := OpenPC(n, zero)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pc.GetString(PidTagDisplayTo); err != nil {
		t.Fatal(err)
	}
	sk, err := pc.GetBinary(PidTagSearchKey)
	if err != nil || len(sk) != 16 {
		t.Fatalf("search key %v %v", sk, err)
	}
	var recipSub bool
	if e, ok := n.LookupNode(uint64(zero)); !ok {
		t.Fatal("missing zero message")
	} else if err := n.WalkSubnodes(e.SubBID, func(ent SLEntry) error {
		if uint32(ent.NID) == RelatedNID(zero, NIDTypeRecipientTable) {
			recipSub = true
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !recipSub {
		t.Fatal("zero-recipient message missing Recipient Table subnode")
	}

	ct, err := OpenTC(n, RelatedNID(inbox, NIDTypeContentsTable))
	if err != nil {
		t.Fatal(err)
	}
	if ct.RowCount() != 3 {
		t.Fatalf("contents rows %d", ct.RowCount())
	}
	for _, id := range []uint32{zero, one, many} {
		if _, err := ct.GetString(id, PidTagSubject); err != nil {
			t.Fatalf("contents row 0x%x: %v", id, err)
		}
	}
	fpc, err := OpenPC(n, inbox)
	if err != nil {
		t.Fatal(err)
	}
	if c, err := fpc.GetInt32(PidTagContentCount); err != nil || c != 3 {
		t.Fatalf("content count %v %v", c, err)
	}
	if u, err := fpc.GetInt32(PidTagContentUnreadCount); err != nil || u != 0 {
		t.Fatalf("unread %v %v", u, err)
	}

	commitTree(t, n, path)
	p, err := outlookpst.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	root, err := p.RootFolder()
	if err != nil {
		t.Fatal(err)
	}
	in := findFolder(t, root, "Min Store", "Inbox")
	if n, err := in.MessageCount(); err != nil || n != 3 {
		t.Fatalf("reader count %d %v", n, err)
	}
	if c, err := in.ContentCount(); err != nil || c != 3 {
		t.Fatalf("folder content count %d %v", c, err)
	}
	bySubj := map[string]*outlookpst.Message{}
	for _, m := range collectMessages(t, in) {
		s, err := m.Subject()
		if err != nil {
			t.Fatal(err)
		}
		bySubj[s] = m
	}
	z := bySubj["empty-to"]
	if z == nil {
		t.Fatal("missing empty-to")
	}
	if n, err := z.RecipientCount(); err != nil || n != 0 {
		t.Fatalf("zero recip count %d %v", n, err)
	}
	if tbl, err := z.RecipientTable(); err != nil || tbl == nil {
		t.Fatalf("zero recip table %v %v", tbl, err)
	}
	body, err := z.Body()
	if err != nil || body != "nobody" {
		t.Fatalf("zero body %q %v", body, err)
	}

	o := bySubj["one-to"]
	if o == nil {
		t.Fatal("missing one-to")
	}
	recips := collectRecips(t, o)
	if len(recips) != 1 {
		t.Fatalf("one recip %d", len(recips))
	}
	if name, err := recips[0].Name(); err != nil || name != "Bob" {
		t.Fatalf("name %q %v", name, err)
	}
	if email, err := recips[0].Email(); err != nil || email != "bob@example.com" {
		t.Fatalf("email %q %v", email, err)
	}
	if typ, err := recips[0].Type(); err != nil || typ != outlookpst.RecipientTo {
		t.Fatalf("type %v %v", typ, err)
	}
	if from, err := o.SenderName(); err != nil || from != "Alice" {
		t.Fatalf("from %q %v", from, err)
	}

	m := bySubj["many"]
	if m == nil {
		t.Fatal("missing many")
	}
	html, err := m.HTMLBody()
	if err != nil || html != "<p>html</p>" {
		t.Fatalf("html %q %v", html, err)
	}
	if id, err := m.InternetMessageID(); err != nil || id != "<id@example.com>" {
		t.Fatalf("msgid %q %v", id, err)
	}
	if imp, err := m.Importance(); err != nil || imp != 2 {
		t.Fatalf("importance %d %v", imp, err)
	}
	if sen, err := m.Sensitivity(); err != nil || sen != 2 {
		t.Fatalf("sensitivity %d %v", sen, err)
	}
	got := collectRecips(t, m)
	if len(got) != 4 {
		t.Fatalf("many recip %d", len(got))
	}
	types := map[outlookpst.RecipientType]int{}
	for i := range got {
		typ, err := got[i].Type()
		if err != nil {
			t.Fatal(err)
		}
		types[typ]++
	}
	if types[outlookpst.RecipientTo] != 2 || types[outlookpst.RecipientCc] != 1 || types[outlookpst.RecipientBcc] != 1 {
		t.Fatalf("recip types %+v", types)
	}
	dt, err := m.DeliveryTime()
	if err != nil {
		t.Fatal(err)
	}
	if dt.Unix() != now.Add(2*time.Minute).Unix() {
		t.Fatalf("delivery %s", dt)
	}
}

func TestMessageFlagsUnicodeMissingAndLargeBodies(t *testing.T) {
	n, tree, path := blankTree(t)
	inbox, err := tree.Create(tree.IPM(), "Inbox")
	if err != nil {
		t.Fatal(err)
	}
	unread, err := tree.CreateMessage(inbox, MessageWrite{
		Subject:  "こんにちは",
		BodyText: "Unicode 本文",
		Draft:    true,
		Read:     false,
	})
	if err != nil {
		t.Fatal(err)
	}
	large := strings.Repeat("Z", 5000)
	_, err = tree.CreateMessage(inbox, MessageWrite{
		Subject:    "large",
		BodyText:   large,
		BodyHTML:   strings.Repeat("<p>x</p>", 800),
		Read:       true,
		Importance: ImportanceLow,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tree.CreateMessage(inbox, MessageWrite{
		Subject: "nobody",
		Read:    true,
	}); err != nil {
		t.Fatal(err)
	}

	pc, err := OpenPC(n, unread)
	if err != nil {
		t.Fatal(err)
	}
	flags, err := pc.GetInt32(PidTagMessageFlags)
	if err != nil {
		t.Fatal(err)
	}
	if flags&MsgFlagRead != 0 {
		t.Fatalf("unread still has READ %#x", flags)
	}
	if flags&MsgFlagUnsent == 0 {
		t.Fatalf("draft missing UNSENT %#x", flags)
	}
	fpc, err := OpenPC(n, inbox)
	if err != nil {
		t.Fatal(err)
	}
	if u, err := fpc.GetInt32(PidTagContentUnreadCount); err != nil || u != 1 {
		t.Fatalf("unread count %v %v", u, err)
	}

	commitTree(t, n, path)
	p, err := outlookpst.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	root, err := p.RootFolder()
	if err != nil {
		t.Fatal(err)
	}
	in := findFolder(t, root, "Min Store", "Inbox")
	if u, err := in.UnreadCount(); err != nil || u != 1 {
		t.Fatalf("reader unread %d %v", u, err)
	}
	bySubj := map[string]*outlookpst.Message{}
	for _, m := range collectMessages(t, in) {
		s, err := m.Subject()
		if err != nil {
			t.Fatal(err)
		}
		bySubj[s] = m
	}
	u := bySubj["こんにちは"]
	if u == nil {
		t.Fatal("missing unicode subject")
	}
	body, err := u.Body()
	if err != nil || body != "Unicode 本文" {
		t.Fatalf("unicode body %q %v", body, err)
	}
	lg := bySubj["large"]
	if lg == nil {
		t.Fatal("missing large")
	}
	got, err := lg.Body()
	if err != nil || got != large {
		t.Fatalf("large body len %d err %v", len(got), err)
	}
	html, err := lg.HTMLBody()
	if err != nil || !strings.Contains(html, "<p>x</p>") {
		t.Fatalf("large html %v", err)
	}
	nb := bySubj["nobody"]
	if nb == nil {
		t.Fatal("missing nobody")
	}
	if _, err := nb.Body(); err == nil {
		t.Fatal("optional body should be absent")
	}
}

func TestMessageDeleteUpdatesCountsAndContents(t *testing.T) {
	n, tree, path := blankTree(t)
	inbox, err := tree.Create(tree.IPM(), "Inbox")
	if err != nil {
		t.Fatal(err)
	}
	keep, err := tree.CreateMessage(inbox, MessageWrite{Subject: "keep", Read: true})
	if err != nil {
		t.Fatal(err)
	}
	drop, err := tree.CreateMessage(inbox, MessageWrite{Subject: "drop", Read: false})
	if err != nil {
		t.Fatal(err)
	}
	if err := tree.DeleteMessage(drop); err != nil {
		t.Fatal(err)
	}
	if _, ok := n.LookupNode(uint64(drop)); ok {
		t.Fatal("deleted message node remains")
	}
	if _, ok := n.LookupNode(uint64(keep)); !ok {
		t.Fatal("kept message was deleted")
	}
	ct, err := OpenTC(n, RelatedNID(inbox, NIDTypeContentsTable))
	if err != nil {
		t.Fatal(err)
	}
	if ct.RowCount() != 1 {
		t.Fatalf("contents rows %d", ct.RowCount())
	}
	if _, err := ct.GetString(drop, PidTagSubject); err == nil {
		t.Fatal("stale contents row")
	}
	fpc, err := OpenPC(n, inbox)
	if err != nil {
		t.Fatal(err)
	}
	if c, err := fpc.GetInt32(PidTagContentCount); err != nil || c != 1 {
		t.Fatalf("count %v %v", c, err)
	}
	if u, err := fpc.GetInt32(PidTagContentUnreadCount); err != nil || u != 0 {
		t.Fatalf("unread after delete %v %v", u, err)
	}
	commitTree(t, n, path)
	p, err := outlookpst.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	root, err := p.RootFolder()
	if err != nil {
		t.Fatal(err)
	}
	in := findFolder(t, root, "Min Store", "Inbox")
	msgs := collectMessages(t, in)
	if len(msgs) != 1 {
		t.Fatalf("reader msgs %d", len(msgs))
	}
	if s, _ := msgs[0].Subject(); s != "keep" {
		t.Fatalf("subject %q", s)
	}
}

func TestMessageInvalidOpsAndRollback(t *testing.T) {
	n, tree, _ := blankTree(t)
	inbox, err := tree.Create(tree.IPM(), "Inbox")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tree.CreateMessage(tree.Finder(), MessageWrite{Subject: "nope"}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("search folder: %v", err)
	}
	if _, err := tree.CreateMessage(inbox, MessageWrite{Subject: "bad", Importance: 9}); !errors.Is(err, ErrInvalidArg) {
		t.Fatalf("importance: %v", err)
	}
	before := 0
	for nid := range n.nodes {
		if NIDTypeOf(uint32(nid)) == NIDTypeNormalMessage {
			before++
		}
	}
	if _, err := tree.CreateMessage(0xdead, MessageWrite{Subject: "gone"}); !errors.Is(err, ErrInvalidArg) {
		t.Fatalf("missing folder: %v", err)
	}
	after := 0
	for nid := range n.nodes {
		if NIDTypeOf(uint32(nid)) == NIDTypeNormalMessage {
			after++
		}
	}
	if after != before {
		t.Fatalf("failed create leaked messages %d -> %d", before, after)
	}
}

func TestMessageExporterFinalizeRoundTrip(t *testing.T) {
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
	ref, err := exp.CreateMessage(inbox, MessageSpec{
		Subject:           "exported",
		BodyText:          "plain",
		BodyHTML:          "<b>html</b>",
		From:              Recipient{Name: "Pat", Email: "pat@example.com"},
		To:                []Recipient{{Name: "Sam", Email: "sam@example.com"}},
		Cc:                []Recipient{{Email: "cc@example.com"}},
		Read:              true,
		Sent:              time.Unix(1_700_000_100, 0).UTC(),
		Received:          time.Unix(1_700_000_200, 0).UTC(),
		InternetMessageID: "<exp@example.com>",
		InternetHeaders:   "Subject: exported\r\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	plan := exp.Plan()
	var pm PlannedMessage
	for _, m := range plan.Messages {
		if m.Ref == ref {
			pm = m
		}
	}
	if pm.SearchKey == "" || pm.RecordKey == "" {
		t.Fatal("plan missing deterministic keys")
	}
	if _, err := hex.DecodeString(pm.SearchKey); err != nil {
		t.Fatal(err)
	}
	if err := exp.Finalize(context.Background()); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "export.pst")
	if err := os.WriteFile(path, sink.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := outlookpst.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	root, err := p.RootFolder()
	if err != nil {
		t.Fatal(err)
	}
	in := findFolder(t, root, "Min Store", "Inbox")
	msgs := collectMessages(t, in)
	if len(msgs) != 1 {
		t.Fatalf("exported msgs %d", len(msgs))
	}
	m := msgs[0]
	if s, _ := m.Subject(); s != "exported" {
		t.Fatalf("subject %q", s)
	}
	if b, _ := m.Body(); b != "plain" {
		t.Fatalf("body %q", b)
	}
	if h, _ := m.HTMLBody(); h != "<b>html</b>" {
		t.Fatalf("html %q", h)
	}
	recips := collectRecips(t, m)
	if len(recips) != 2 {
		t.Fatalf("recips %d", len(recips))
	}
}

func TestMessageTreeInterop(t *testing.T) {
	n, tree, path := blankTree(t)
	inbox, err := tree.Create(tree.IPM(), "Inbox")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tree.CreateMessage(inbox, MessageWrite{
		Subject:  "interop",
		BodyText: "hello",
		To:       []Recipient{{Name: "Bob", Email: "bob@example.com"}},
		Read:     true,
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
		if !strings.Contains(string(out), "interop") && !strings.Contains(string(out), "Inbox") {
			t.Logf("pffinfo output did not mention message/folder names (accepted): %s", out)
		}
	}
}
