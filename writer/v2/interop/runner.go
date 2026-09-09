// Package interop records PST qualification results from this library, libpff,
// Outlook, and scanpst.exe. Missing tools are skipped so ordinary unit tests
// do not require Windows.
package interop

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	outlookpst "github.com/grokify/outlook-pst-go"
)

// Tool names recorded in the manifest.
const (
	ToolSelfReader = "outlook-pst-go"
	ToolLibpff     = "libpff"
	ToolOutlook    = "outlook"
	ToolScanPST    = "scanpst"
)

// Status is one tool's outcome.
type Status string

const (
	StatusPass    Status = "pass"
	StatusFail    Status = "fail"
	StatusError   Status = "error"
	StatusSkipped Status = "skipped"
)

// How a Result was produced.
const (
	SourceProbe    = "probe"    // this process looked up / executed the tool
	SourceImported = "imported" // recorded from an actual Outlook/scanpst run
)

// Result is one tool's record.
type Result struct {
	Tool      string `json:"tool"`
	Available bool   `json:"available"`
	Status    Status `json:"status"`
	Version   string `json:"version,omitempty"`
	Path      string `json:"path,omitempty"`
	Log       string `json:"log,omitempty"`
	Source    string `json:"source,omitempty"`
}

// Manifest is the interoperability report for one file.
type Manifest struct {
	File    string    `json:"file"`
	When    time.Time `json:"when"`
	GOOS    string    `json:"goos"`
	Results []Result  `json:"results"`
}

// Options control Lookups. All fields optional.
// Imported and ImportPath overlay probe results so actual Outlook/scanpst
// pass/fail evidence can be checked in without requiring Windows here.
type Options struct {
	Now          time.Time
	LookPath     func(string) (string, error)
	LibpffNames  []string
	OutlookNames []string
	ScanpstNames []string
	Imported     []Result
	ImportPath   string
}

func (o Options) withDefaults() Options {
	if o.LookPath == nil {
		o.LookPath = exec.LookPath
	}
	if len(o.LibpffNames) == 0 {
		o.LibpffNames = []string{"pffinfo", "pffexport", "readpst"}
	}
	if len(o.OutlookNames) == 0 {
		o.OutlookNames = []string{"outlook", "OUTLOOK.EXE", "outlook.exe"}
	}
	if len(o.ScanpstNames) == 0 {
		o.ScanpstNames = []string{"scanpst", "SCANPST.EXE", "scanpst.exe"}
	}
	if o.Now.IsZero() {
		o.Now = time.Now().UTC()
	}
	return o
}

// Run records results for path. Missing tools are StatusSkipped.
func Run(ctx context.Context, path string, opts Options) (Manifest, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	opts = opts.withDefaults()
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	m := Manifest{File: abs, When: opts.Now.UTC(), GOOS: runtime.GOOS}
	m.Results = append(m.Results, withSource(runSelf(path), SourceProbe))
	for _, r := range runExternal(ctx, opts, ToolLibpff, opts.LibpffNames, path) {
		m.Results = append(m.Results, withSource(r, SourceProbe))
	}
	m.Results = append(m.Results, withSource(skipOrFound(opts, ToolOutlook, opts.OutlookNames), SourceProbe))
	m.Results = append(m.Results, withSource(skipOrFound(opts, ToolScanPST, opts.ScanpstNames), SourceProbe))
	if err := m.applyImports(opts); err != nil {
		return Manifest{}, err
	}
	return m, nil
}

// EncodeManifest writes canonical JSON.
func EncodeManifest(m Manifest) ([]byte, error) {
	return json.MarshalIndent(m, "", "  ")
}

func runSelf(path string) (r Result) {
	r = Result{Tool: ToolSelfReader, Available: true}
	defer func() {
		if rec := recover(); rec != nil {
			r.Status = StatusError
			r.Log = fmt.Sprintf("panic: %v", rec)
		}
	}()
	p, err := outlookpst.Open(path)
	if err != nil {
		r.Status = StatusFail
		r.Log = err.Error()
		return r
	}
	defer func() { _ = p.Close() }()
	r.Status = StatusPass
	r.Log = "Open succeeded"
	return r
}

func runExternal(ctx context.Context, opts Options, tool string, names []string, file string) []Result {
	path, name := firstOnPath(opts, names)
	if path == "" {
		return []Result{{Tool: tool, Available: false, Status: StatusSkipped, Log: "not found in PATH"}}
	}
	r := Result{Tool: tool, Available: true, Path: path, Version: name}
	cmd := exec.CommandContext(ctx, path, file)
	out, err := cmd.CombinedOutput()
	r.Log = trimLog(string(out))
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			r.Status = StatusFail
			r.Log += err.Error()
			return []Result{r}
		}
		r.Status = StatusError
		r.Log += err.Error()
		return []Result{r}
	}
	r.Status = StatusPass
	return []Result{r}
}

func skipOrFound(opts Options, tool string, names []string) Result {
	path, name := firstOnPath(opts, names)
	if path == "" {
		return Result{Tool: tool, Available: false, Status: StatusSkipped, Log: "not found in PATH; Windows qualification is optional for unit tests"}
	}
	return Result{Tool: tool, Available: true, Status: StatusSkipped, Path: path, Version: name, Log: "present but not executed (no automation host)"}
}

func firstOnPath(opts Options, names []string) (string, string) {
	for _, n := range names {
		p, err := opts.LookPath(n)
		if err == nil && p != "" {
			if _, err := os.Stat(p); err == nil {
				return p, n
			}
			return p, n
		}
	}
	return "", ""
}

func trimLog(s string) string {
	const max = 4096
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}

func withSource(r Result, src string) Result {
	if r.Source == "" {
		r.Source = src
	}
	return r
}

// Record stores an actual tool outcome, replacing any earlier result for the
// same tool. Empty Source is treated as imported (Outlook/scanpst evidence).
func (m *Manifest) Record(r Result) {
	if r.Source == "" {
		r.Source = SourceImported
	}
	for i := range m.Results {
		if m.Results[i].Tool == r.Tool {
			m.Results[i] = r
			return
		}
	}
	m.Results = append(m.Results, r)
}

// Merge overlays other.Results onto m by tool name.
func (m *Manifest) Merge(other Manifest) {
	for _, r := range other.Results {
		m.Record(r)
	}
	if m.File == "" {
		m.File = other.File
	}
}

func (m *Manifest) applyImports(opts Options) error {
	for _, r := range opts.Imported {
		m.Record(r)
	}
	if opts.ImportPath == "" {
		return nil
	}
	other, err := LoadManifest(opts.ImportPath)
	if err != nil {
		return err
	}
	m.Merge(other)
	return nil
}

// DecodeManifest parses a Manifest object or a bare []Result array.
func DecodeManifest(raw []byte) (Manifest, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return Manifest{}, fmt.Errorf("empty interop manifest")
	}
	if raw[0] == '[' {
		var results []Result
		if err := json.Unmarshal(raw, &results); err != nil {
			return Manifest{}, err
		}
		m := Manifest{Results: results}
		for i := range m.Results {
			if m.Results[i].Source == "" {
				m.Results[i].Source = SourceImported
			}
		}
		return m, nil
	}
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return Manifest{}, err
	}
	for i := range m.Results {
		if m.Results[i].Source == "" {
			m.Results[i].Source = SourceImported
		}
	}
	return m, nil
}

// LoadManifest reads a checked-in Outlook/scanpst evidence file.
func LoadManifest(path string) (Manifest, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Manifest{}, err
	}
	return DecodeManifest(raw)
}

// WriteManifest records a manifest as indented JSON for later ImportPath use.
func WriteManifest(path string, m Manifest) error {
	raw, err := EncodeManifest(m)
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	return os.WriteFile(path, raw, 0o644)
}
