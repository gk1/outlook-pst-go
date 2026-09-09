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

func TestImportOutlookScanpstPass(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.pst")
	if err := os.WriteFile(path, []byte("not a pst"), 0o600); err != nil {
		t.Fatal(err)
	}
	m, err := Run(context.Background(), path, Options{
		Now:        time.Unix(1_700_000_000, 0).UTC(),
		LookPath:   func(string) (string, error) { return "", os.ErrNotExist },
		ImportPath: filepath.Join("testdata", "outlook-scanpst-pass.json"),
	})
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]Result{}
	for _, r := range m.Results {
		by[r.Tool] = r
	}
	for _, tool := range []string{ToolOutlook, ToolScanPST} {
		r, ok := by[tool]
		if !ok {
			t.Fatalf("missing %s", tool)
		}
		if r.Status != StatusPass {
			t.Fatalf("%s status=%s log=%s", tool, r.Status, r.Log)
		}
		if r.Source != SourceImported {
			t.Fatalf("%s source=%s", tool, r.Source)
		}
		if !r.Available {
			t.Fatalf("%s not available", tool)
		}
	}
	if by[ToolLibpff].Source != SourceProbe {
		t.Fatalf("libpff source=%s", by[ToolLibpff].Source)
	}
}

func TestImportOutlookScanpstFailArray(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.pst")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	m, err := Run(context.Background(), path, Options{
		LookPath:   func(string) (string, error) { return "", os.ErrNotExist },
		ImportPath: filepath.Join("testdata", "outlook-scanpst-fail.json"),
	})
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]Result{}
	for _, r := range m.Results {
		by[r.Tool] = r
	}
	if by[ToolOutlook].Status != StatusFail || by[ToolScanPST].Status != StatusFail {
		t.Fatalf("want imported fail, got outlook=%s scanpst=%s", by[ToolOutlook].Status, by[ToolScanPST].Status)
	}
}

func TestRecordActualPassFail(t *testing.T) {
	m := Manifest{File: "sample.pst"}
	m.Record(Result{Tool: ToolOutlook, Available: true, Status: StatusPass, Log: "opened"})
	m.Record(Result{Tool: ToolScanPST, Available: true, Status: StatusFail, Log: "errors"})
	m.Record(Result{Tool: ToolOutlook, Available: true, Status: StatusFail, Log: "later fail"})
	if len(m.Results) != 2 {
		t.Fatalf("results=%d", len(m.Results))
	}
	if m.Results[0].Status != StatusFail || m.Results[0].Source != SourceImported {
		t.Fatalf("%+v", m.Results[0])
	}
	if m.Results[1].Status != StatusFail {
		t.Fatalf("%+v", m.Results[1])
	}
	round, err := DecodeManifest([]byte(`[{"tool":"outlook","status":"pass"}]`))
	if err != nil {
		t.Fatal(err)
	}
	if round.Results[0].Source != SourceImported || round.Results[0].Status != StatusPass {
		t.Fatalf("%+v", round.Results[0])
	}
	out := filepath.Join(t.TempDir(), "m.json")
	if err := WriteManifest(out, m); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadManifest(out)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Results[0].Tool != ToolOutlook {
		t.Fatalf("%+v", loaded)
	}
}

func TestImportedOptionOverlaysProbeSkip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.pst")
	_ = os.WriteFile(path, []byte("x"), 0o600)
	m, err := Run(context.Background(), path, Options{
		LookPath: func(string) (string, error) { return "", os.ErrNotExist },
		Imported: []Result{{
			Tool:      ToolOutlook,
			Available: true,
			Status:    StatusPass,
			Log:       "manual Windows run",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range m.Results {
		if r.Tool == ToolOutlook {
			if r.Status != StatusPass || r.Source != SourceImported {
				t.Fatalf("%+v", r)
			}
			return
		}
	}
	t.Fatal("missing outlook")
}
