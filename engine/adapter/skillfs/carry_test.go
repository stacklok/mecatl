package skillfs

import (
	"os"
	"strings"
	"testing"
)

// TestCarryParity pins the SHARED carried helpers byte-identical to their
// agentfs twin: the #328 panel rejected a shared helpers package, so agentfs,
// skillfs, and rulesfs each carry a copy of the SAME bodies — a silent
// divergence would fork the frontmatter semantics three adapters share.
// skillfs's carry.go is a SUPERSET (it also carries the tool-output cluster),
// so the pin is scoped to the six shared declarations, extracted line-wise and
// compared body-for-body.
// rulesfs/carry_test.go holds the same pin for rulesfs; the two tests close
// the four-way ring (toolkit is the root-module origin the pinning comments
// already name).
func TestCarryParity(t *testing.T) {
	// decl extracts one declaration (contiguous doc-comment block + body) from
	// source by its signature anchor line. The doc block is the maximal run of
	// "//" comment lines immediately above the anchor (blank lines inside the
	// block are "\n//\n" — still comment lines, so they are included); the body
	// runs to the first line that starts a new top-level declaration.
	decl := func(src, anchor string) string {
		lines := strings.Split(src, "\n")
		anchorIdx := -1
		for i, l := range lines {
			if strings.HasPrefix(l, anchor) {
				anchorIdx = i
				break
			}
		}
		if anchorIdx < 0 {
			t.Fatalf("anchor %q not found in carry.go", anchor)
		}
		start := anchorIdx
		for start > 0 && strings.HasPrefix(lines[start-1], "//") {
			start--
		}
		end := len(lines)
		for i := anchorIdx + 1; i < len(lines); i++ {
			l := lines[i]
			// A declaration ends at a BLANK line followed by a new top-level
			// declaration — a blank line followed by a "//" line is the NEXT
			// declaration's doc comment, which belongs to the next decl, not
			// this one.
			if l == "" && i+1 < len(lines) {
				n := lines[i+1]
				if strings.HasPrefix(n, "func ") || strings.HasPrefix(n, "const ") ||
					strings.HasPrefix(n, "var ") || strings.HasPrefix(n, "type ") ||
					strings.HasPrefix(n, "//") {
					end = i
					break
				}
			}
		}
		return strings.TrimSpace(strings.Join(lines[start:end], "\n"))
	}

	read := func(path string) string {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		return string(data)
	}
	agentfs := read("../agentfs/carry.go")
	skillfs := read("carry.go")

	shared := []string{
		"func SplitFrontmatter(",
		"func IndexClosingDelim(",
		"func TruncateRunes(",
		"type ResolveEnv struct",
		"var OSEnv =",
		"func UserConfigDir(",
	}
	for _, anchor := range shared {
		if got, want := decl(skillfs, anchor), decl(agentfs, anchor); got != want {
			t.Errorf("carried %s diverged from agentfs/carry.go — keep byte-identical (the #328 carry contract)\n--- agentfs ---\n%s\n--- skillfs ---\n%s", anchor, want, got)
		}
	}
}
