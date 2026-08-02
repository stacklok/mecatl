package rulesfs

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/sourceconformance"
	"github.com/stacklok/mecatl/engine/prompt"
)

// TestFSSourceConformance runs the shared RulesSource conformance suite over
// FSSource reading the canonical fixture WRITTEN OUT as real <name>.md
// frontmatter files in a t.TempDir — the filesystem backend answering for the
// same rules the in-memory reference (and a future wire driver) answer for.
// The canonical fixture is authored to round-trip this parser (trimmed bodies,
// globs as a YAML sequence).
func TestFSSourceConformance(t *testing.T) {
	sourceconformance.RunRulesSource(t, func(t *testing.T) prompt.RulesSource {
		dir := writeRuleFixtureTree(t)
		src, skips, err := NewFSSource(context.Background(), DirSource{Dir: dir})
		if err != nil {
			t.Fatalf("NewFSSource: %v", err)
		}
		if len(skips) != 0 {
			t.Fatalf("unexpected discovery skips: %v", skips)
		}
		return src
	})
}

// writeRuleFixtureTree lays the canonical sourceconformance.RuleFixture out as
// <dir>/<name>.md frontmatter files, returning the tree root.
func writeRuleFixtureTree(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, r := range sourceconformance.RuleFixture {
		content := renderRuleMarkdown(r)
		if err := os.WriteFile(filepath.Join(dir, r.Name+RuleFileExt), []byte(content), 0o644); err != nil {
			t.Fatalf("write %s.md: %v", r.Name, err)
		}
	}
	return dir
}

// renderRuleMarkdown renders a fixture rule as the YAML-frontmatter markdown
// layout DirSource discovers, exercising the sequence form the parser accepts.
func renderRuleMarkdown(r prompt.Rule) string {
	var b strings.Builder
	if len(r.Paths) > 0 {
		b.WriteString("---\npaths:\n")
		for _, p := range r.Paths {
			fmt.Fprintf(&b, "  - %q\n", p)
		}
		b.WriteString("---\n")
	}
	b.WriteString(r.Body)
	b.WriteString("\n")
	return b.String()
}
