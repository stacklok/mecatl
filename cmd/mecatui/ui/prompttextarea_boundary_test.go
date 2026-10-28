package ui

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestUIProductionUsesTextareaOnlyInOwnedEditors(t *testing.T) {
	const upstream = "charm.land/bubbles/v2/textarea"

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, filepath.Join(".", name), nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, imp := range file.Imports {
			path, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				t.Fatalf("unquote import in %s: %v", name, err)
			}
			// The out-of-band schedule form deliberately uses upstream textarea,
			// not the session editor's attachments/history/submission state.
			if path == upstream && name != "schedule.go" && name != "schedule_create.go" {
				t.Errorf("%s imports %q; only prompttextarea and the schedule form may own the upstream model", name, upstream)
			}
		}
	}
}
