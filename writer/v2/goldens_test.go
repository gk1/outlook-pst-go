package writer

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"
)

var updateGoldens = flag.Bool("update-goldens", false, "rewrite locked fixture goldens")

type goldenMeta struct {
	Case             string        `json:"case"`
	Folders          int           `json:"folders"`
	Messages         int           `json:"messages"`
	PlanSHA256       string        `json:"plan_sha256"`
	Subjects         []string      `json:"subjects,omitempty"`
	Importance       []Importance  `json:"importance,omitempty"`
	Sensitivity      []Sensitivity `json:"sensitivity,omitempty"`
	BodyTextSHA256   []string      `json:"body_text_sha256,omitempty"`
	AttachmentSHA256 []string      `json:"attachment_sha256,omitempty"`
}

func TestLockedGoldens(t *testing.T) {
	dir := filepath.Join("testdata", "goldens")
	if *updateGoldens {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range AllCases() {
		c := c
		t.Run(string(c), func(t *testing.T) {
			plan, raw, err := Generate(c, testOpts())
			if err != nil {
				t.Fatal(err)
			}
			sum := sha256.Sum256(raw)
			hexSum := hex.EncodeToString(sum[:])
			meta := goldenMeta{
				Case:       string(c),
				Folders:    len(plan.Folders),
				Messages:   len(plan.Messages),
				PlanSHA256: hexSum,
			}
			for _, m := range plan.Messages {
				meta.Subjects = append(meta.Subjects, m.Subject)
				meta.Importance = append(meta.Importance, m.Importance)
				meta.Sensitivity = append(meta.Sensitivity, m.Sensitivity)
				meta.BodyTextSHA256 = append(meta.BodyTextSHA256, m.BodyTextSHA256)
				for _, a := range m.Attachments {
					meta.AttachmentSHA256 = append(meta.AttachmentSHA256, a.SHA256)
				}
			}
			metaBytes, err := json.MarshalIndent(meta, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			metaBytes = append(metaBytes, '\n')

			planPath := filepath.Join(dir, string(c)+".plan.json")
			shaPath := filepath.Join(dir, string(c)+".sha256")
			metaPath := filepath.Join(dir, string(c)+".meta.json")
			if *updateGoldens {
				if err := os.WriteFile(planPath, raw, 0o644); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(shaPath, []byte(hexSum+"\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(metaPath, metaBytes, 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			wantPlan, err := os.ReadFile(planPath)
			if err != nil {
				t.Fatalf("missing golden %s (go test ./writer/v2 -update-goldens): %v", planPath, err)
			}
			if !bytes.Equal(raw, wantPlan) {
				t.Fatalf("plan bytes diverged from locked golden %s", planPath)
			}
			wantSHA, err := os.ReadFile(shaPath)
			if err != nil {
				t.Fatal(err)
			}
			if hexSum != string(bytes.TrimSpace(wantSHA)) {
				t.Fatalf("sha256 %s want %s", hexSum, bytes.TrimSpace(wantSHA))
			}
			wantMeta, err := os.ReadFile(metaPath)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(metaBytes, wantMeta) {
				t.Fatalf("meta diverged\ngot %s\nwant %s", metaBytes, wantMeta)
			}
		})
	}
}
