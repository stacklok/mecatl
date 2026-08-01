package agentfs

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/sourceconformance"
	"github.com/stacklok/mecatl/engine/tool"
)

// TestFSSourceConformance runs the shared AgentDefSource conformance suite
// over FSSource reading the canonical fixture WRITTEN OUT as real <name>.md
// frontmatter files in a t.TempDir — the filesystem backend answering for the
// same definitions the in-memory reference and the wire driver answer for.
// The canonical fixture is authored to round-trip this parser (trimmed
// bodies, single-line descriptions, pre-normalized hooks/headers).
func TestFSSourceConformance(t *testing.T) {
	sourceconformance.RunAgentSource(t, func(t *testing.T) tool.AgentDefSource {
		dir := writeAgentFixtureTree(t)
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

// writeAgentFixtureTree lays the canonical sourceconformance.AgentFixture out
// as <dir>/<name>.md frontmatter files, returning the tree root.
func writeAgentFixtureTree(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, d := range sourceconformance.AgentFixture {
		content := renderAgentDefMarkdown(d)
		if err := os.WriteFile(filepath.Join(dir, d.Name+AgentFileExt), []byte(content), 0o644); err != nil {
			t.Fatalf("write %s.md: %v", d.Name, err)
		}
	}
	return dir
}

// renderAgentDefMarkdown renders a fixture def as the YAML-frontmatter
// markdown layout DirSource discovers, exercising the array forms the parser
// accepts.
func renderAgentDefMarkdown(d tool.AgentDef) string {
	var b strings.Builder
	b.WriteString("---\n")
	fmt.Fprintf(&b, "name: %s\n", d.Name)
	fmt.Fprintf(&b, "description: %s\n", d.Description)
	if len(d.Tools) > 0 {
		fmt.Fprintf(&b, "tools: [%s]\n", strings.Join(d.Tools, ", "))
	}
	if len(d.DisallowedTools) > 0 {
		fmt.Fprintf(&b, "disallowedTools: [%s]\n", strings.Join(d.DisallowedTools, ", "))
	}
	if d.Model != "" {
		fmt.Fprintf(&b, "model: %s\n", d.Model)
	}
	if d.Provider != "" {
		fmt.Fprintf(&b, "provider: %s\n", d.Provider)
	}
	if d.PermissionMode != "" {
		fmt.Fprintf(&b, "permissionMode: %s\n", d.PermissionMode)
	}
	if d.MaxTurns != 0 {
		fmt.Fprintf(&b, "maxTurns: %d\n", d.MaxTurns)
	}
	if d.MaxToolCalls != 0 {
		fmt.Fprintf(&b, "maxToolCalls: %d\n", d.MaxToolCalls)
	}
	if d.Color != "" {
		fmt.Fprintf(&b, "color: %s\n", d.Color)
	}
	if len(d.Skills) > 0 {
		fmt.Fprintf(&b, "skills: [%s]\n", strings.Join(d.Skills, ", "))
	}
	if len(d.MCPServers) > 0 {
		b.WriteString("mcpServers:\n")
		for _, s := range d.MCPServers {
			if s.IsReference() {
				fmt.Fprintf(&b, "  - %s\n", s.Name)
				continue
			}
			fmt.Fprintf(&b, "  - name: %s\n    url: %s\n", s.Name, s.URL)
			if len(s.Headers) > 0 {
				b.WriteString("    headers:\n")
				for k, v := range s.Headers {
					fmt.Fprintf(&b, "      %s: %q\n", k, v)
				}
			}
		}
	}
	if len(d.Hooks) > 0 {
		b.WriteString("hooks:\n")
		// Deterministic order is irrelevant to the parser (a map), so plain
		// range is fine.
		for k, v := range d.Hooks {
			fmt.Fprintf(&b, "  %s: %q\n", k, v)
		}
	}
	b.WriteString("---\n")
	b.WriteString(d.Body)
	b.WriteString("\n")
	return b.String()
}
