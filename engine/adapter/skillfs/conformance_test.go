package skillfs

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/sourceconformance"
	"github.com/stacklok/mecatl/engine/tool"
)

// TestFSSourceConformance runs the shared SkillSource conformance suite over
// FSSource reading the canonical fixture WRITTEN OUT as a real skills tree in
// a t.TempDir — the filesystem backend answering for the same logical bundles
// the in-memory reference and the wire driver answer for.
func TestFSSourceConformance(t *testing.T) {
	sourceconformance.RunSkillSource(t, func(t *testing.T) tool.SkillSource {
		dir := writeFixtureTree(t)
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

// writeFixtureTree lays the canonical sourceconformance.Fixture out as a
// <dir>/<name>/SKILL.md skills tree with its asset files (executable bits
// honored), returning the tree root.
func writeFixtureTree(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, f := range sourceconformance.Fixture {
		skillDir := filepath.Join(dir, f.Name)
		if err := os.MkdirAll(skillDir, 0o755); err != nil {
			t.Fatalf("mkdir %q: %v", skillDir, err)
		}
		// Compose the YAML frontmatter: name + description (required) plus the
		// optional advisory license/compatibility/metadata fields, in the same
		// shape ParseSkill reads. The metadata map is emitted as a YAML block
		// map so goccy parses it back into a map[string]string.
		front := "---\nname: " + f.Name + "\ndescription: " + f.Description + "\n"
		if f.License != "" {
			front += "license: " + f.License + "\n"
		}
		if f.Compatibility != "" {
			front += "compatibility: " + f.Compatibility + "\n"
		}
		if len(f.Metadata) > 0 {
			// Emit a sorted block map so the round-trip is deterministic.
			keys := make([]string, 0, len(f.Metadata))
			for k := range f.Metadata {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			front += "metadata:\n"
			for _, k := range keys {
				front += "  " + k + ": " + f.Metadata[k] + "\n"
			}
		}
		if len(f.AllowedTools) > 0 {
			// Emit the canonical space-separated string form.
			front += "allowed-tools: \"" + strings.Join(f.AllowedTools, " ") + "\"\n"
		}
		content := front + "---\n" + f.Body + "\n"
		if err := os.WriteFile(filepath.Join(skillDir, SkillFileName), []byte(content), 0o644); err != nil {
			t.Fatalf("write SKILL.md: %v", err)
		}
		for _, a := range f.Assets {
			dst := filepath.Join(skillDir, filepath.FromSlash(a.Name))
			if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
				t.Fatalf("mkdir asset dir: %v", err)
			}
			mode := os.FileMode(0o644)
			if a.Executable {
				mode = 0o755
			}
			if err := os.WriteFile(dst, []byte(a.Content), mode); err != nil {
				t.Fatalf("write asset %q: %v", a.Name, err)
			}
		}
	}
	return dir
}
