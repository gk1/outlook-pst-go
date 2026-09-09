package writer

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestV2DoesNotImportRootCreateAPI(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		f, err := parser.ParseFile(fset, name, src, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, im := range f.Imports {
			path := strings.Trim(im.Path.Value, `"`)
			if path == "github.com/grokify/outlook-pst-go" {
				t.Errorf("%s imports the root module (Create API must stay unwired)", name)
			}
		}
	}
}

func TestV2IsOwnPackage(t *testing.T) {
	abs, _ := filepath.Abs(".")
	if !strings.Contains(abs, filepath.Join("writer", "v2")) {
		t.Fatalf("unexpected package dir %s", abs)
	}
}
