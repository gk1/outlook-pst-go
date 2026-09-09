package writer

import (
	"bytes"
	"testing"
	"time"
)

func testOpts() Options {
	return Options{
		Clock:       FixedClock{T: time.Unix(1_700_000_000, 0).UTC()},
		IDs:         NewSequentialIDs(),
		DisplayName: "Fixture",
	}
}

func TestFixtureCatalogDeterministic(t *testing.T) {
	for _, c := range AllCases() {
		c := c
		t.Run(string(c), func(t *testing.T) {
			_, a, err := Generate(c, testOpts())
			if err != nil {
				t.Fatal(err)
			}
			_, b, err := Generate(c, testOpts())
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(a, b) {
				t.Fatalf("non-deterministic fixture %s", c)
			}
			if len(a) == 0 {
				t.Fatal("empty plan")
			}
		})
	}
}

func TestFixtureClockAffectsBytes(t *testing.T) {
	_, a, err := Generate(CaseOneMessage, testOpts())
	if err != nil {
		t.Fatal(err)
	}
	opts := testOpts()
	opts.Clock = FixedClock{T: time.Unix(1_800_000_000, 0).UTC()}
	_, b, err := Generate(CaseOneMessage, opts)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(a, b) {
		t.Fatal("clock change did not affect plan bytes")
	}
}

func TestLargeMessageExceedsSingleBlock(t *testing.T) {
	p, _, err := Generate(CaseLargeMessage, testOpts())
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Messages) != 1 {
		t.Fatalf("messages=%d", len(p.Messages))
	}
	if p.Messages[0].BodyTextLen <= MaxDataBlockCB {
		t.Fatalf("large fixture body %d does not exceed one data block", p.Messages[0].BodyTextLen)
	}
}

func TestAttachmentFixtureHashesContent(t *testing.T) {
	p, _, err := Generate(CaseAttachment, testOpts())
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Messages[0].Attachments) != 1 {
		t.Fatal("missing attachment")
	}
	if p.Messages[0].Attachments[0].SHA256 == "" {
		t.Fatal("missing sha256")
	}
	if p.Messages[0].Attachments[0].Size != int64(len("attachment-bytes")) {
		t.Fatalf("size %d", p.Messages[0].Attachments[0].Size)
	}
}
