package interop

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRunSkipsMissingWindowsTools(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.pst")
	if err := os.WriteFile(path, []byte("not a pst"), 0o600); err != nil {
		t.Fatal(err)
	}
	look := func(string) (string, error) { return "", os.ErrNotExist }
	m, err := Run(context.Background(), path, Options{
		Now:      time.Unix(1_700_000_000, 0).UTC(),
		LookPath: look,
	})
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]Result{}
	for _, r := range m.Results {
		by[r.Tool] = r
	}
	for _, tool := range []string{ToolLibpff, ToolOutlook, ToolScanPST} {
		r, ok := by[tool]
		if !ok {
			t.Fatalf("missing %s", tool)
		}
		if r.Status != StatusSkipped {
			t.Fatalf("%s status=%s, want skipped", tool, r.Status)
		}
		if r.Available {
			t.Fatalf("%s marked available", tool)
		}
	}
	self, ok := by[ToolSelfReader]
	if !ok {
		t.Fatal("missing self reader")
	}
	if self.Status != StatusFail && self.Status != StatusError {
		t.Fatalf("self reader on garbage: %s %s", self.Status, self.Log)
	}
	raw, err := EncodeManifest(m)
	if err != nil {
		t.Fatal(err)
	}
	var round Manifest
	if err := json.Unmarshal(raw, &round); err != nil {
		t.Fatal(err)
	}
}

func TestRunDoesNotRequireGOOSWindows(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.pst")
	_ = os.WriteFile(path, []byte("x"), 0o600)
	m, err := Run(context.Background(), path, Options{
		LookPath: func(string) (string, error) { return "", os.ErrNotExist },
	})
	if err != nil {
		t.Fatal(err)
	}
	if m.GOOS == "windows" {
		t.Skip("actually running on windows")
	}
	if len(m.Results) < 4 {
		t.Fatalf("want self+libpff+outlook+scanpst, got %d", len(m.Results))
	}
}
