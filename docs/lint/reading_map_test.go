package lint

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// TestReadingMapGuard validates the reading map integrity:
//  1. Every human-authored architecture/*.md page is listed in READING.md's
//     foundation spine or topic-branch table.
//  2. Every page listed in the map exists.
//  3. Every human-authored architecture page carries "What this covers:",
//     "Prerequisites:", and "Follow-on:" orientation labels.
func TestReadingMapGuard(t *testing.T) {
	root := repoRoot(t) // from citations_test.go

	// Read docs/READING.md
	readingPath := filepath.Join(root, "docs", "READING.md")
	readingData, err := os.ReadFile(readingPath)
	if err != nil {
		t.Fatalf("read READING.md: %v", err)
	}

	// Read all docs/architecture/*.md pages
	matches, err := filepath.Glob(filepath.Join(root, "docs", "architecture", "*.md"))
	if err != nil {
		t.Fatalf("glob architecture: %v", err)
	}
	sort.Strings(matches)

	archPages := make(map[string]string, len(matches))
	for _, p := range matches {
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("read %s: %v", p, err)
		}
		archPages[filepath.Base(p)] = string(data)
	}

	problems := CheckReadingMap(string(readingData), archPages)

	if len(problems) > 0 {
		var b strings.Builder
		b.WriteString("reading map integrity failures (fix the doc, not this test):\n")
		for _, p := range problems {
			b.WriteString("  - " + p.Error() + "\n")
		}
		t.Fatal(b.String())
	}
}

// TestCheckReadingMap_Fixtures verifies the pure CheckReadingMap function with
// synthetic fixtures, proving the gate catches each gap independently.
func TestCheckReadingMap_Fixtures(t *testing.T) {
	tests := []struct {
		name     string
		reading  string
		pages    map[string]string
		wantMsgs []string // substrings to find in error messages; nil = clean
	}{
		{
			name: "all present and labeled",
			reading: `### Topic branches

| Page | What it answers | Prerequisite |
[domain-model](architecture/domain-model.md) | x | y |
[ports](architecture/ports.md) | x | y |
`,
			pages: map[string]string{
				"domain-model.md": "What this covers: the Session aggregate.\nPrerequisites: architecture.\nFollow-on: ports.\n",
				"ports.md":        "What this covers: the port interfaces.\nPrerequisites: domain model.\nFollow-on: agent loop.\n",
			},
			wantMsgs: nil,
		},
		{
			name: "missing label in page",
			reading: `### Topic branches
| [domain-model](architecture/domain-model.md) | x |
`,
			pages: map[string]string{
				"domain-model.md": "What this covers: the Session aggregate.\nFollow-on: ports.\n",
			},
			wantMsgs: []string{`"Prerequisites:"`},
		},
		{
			name: "page not listed in reading map",
			reading: `### Topic branches
`,
			pages: map[string]string{
				"domain-model.md": "What this covers: the Session aggregate.\nPrerequisites: architecture.\nFollow-on: ports.\n",
			},
			wantMsgs: []string{"listed in docs/READING.md"},
		},
		{
			name: "listed page does not exist",
			reading: `### Topic branches
| [ghost](architecture/ghost.md) | x |
`,
			pages:    map[string]string{},
			wantMsgs: []string{"file exists in docs/architecture/"},
		},
		{
			name: "generated page skipped both ways",
			reading: `### Topic branches
`,
			pages: map[string]string{
				"mecatl.modelith.md": "Generated content.",
			},
			wantMsgs: nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := CheckReadingMap(tc.reading, tc.pages)

			if tc.wantMsgs == nil {
				if len(got) > 0 {
					t.Fatalf("expected zero problems, got %d: %v", len(got), got)
				}
				return
			}

			for _, want := range tc.wantMsgs {
				found := false
				for _, p := range got {
					if strings.Contains(p.Error(), want) {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("expected problem containing %q, got %v", want, got)
				}
			}
		})
	}
}
