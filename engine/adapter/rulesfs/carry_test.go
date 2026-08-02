package rulesfs

import (
	"os"
	"strings"
	"testing"
)

// TestCarryParity pins the carried helpers byte-identical to their agentfs
// twin: the #328 panel rejected a shared helpers package, so agentfs and
// rulesfs each carry a copy of the SAME bodies — a silent divergence would
// fork the frontmatter semantics two adapters share. The test reads BOTH
// carry.go files, strips the package clause and the leading header comment
// block (the only intentional difference), and asserts the remaining function
// bodies are byte-identical.
func TestCarryParity(t *testing.T) {
	strip := func(path string) string {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		s := string(data)
		// Drop the leading "// Package ..." header comment block (contiguous
		// comment lines at the very top) so the name-the-package difference
		// does not fail the parity check.
		for strings.HasPrefix(s, "//") {
			idx := strings.IndexByte(s, '\n')
			if idx < 0 {
				t.Fatalf("%s: header comment never terminates", path)
			}
			s = s[idx+1:]
		}
		// Drop the package clause line.
		if !strings.HasPrefix(s, "package ") {
			head := s
			if len(head) > 40 {
				head = head[:40]
			}
			t.Fatalf("%s: expected the package clause after the header comment, got %q", path, head)
		}
		idx := strings.IndexByte(s, '\n')
		s = s[idx+1:]
		return strings.TrimSpace(s)
	}

	agentfs := strip("../agentfs/carry.go")
	rulesfs := strip("carry.go")
	if agentfs != rulesfs {
		t.Fatalf("carry.go bodies diverged from agentfs/carry.go — keep them byte-identical (the #328 carry contract)\n--- agentfs ---\n%s\n--- rulesfs ---\n%s", agentfs, rulesfs)
	}
}

// TestSplitFrontmatter unit-tests the carried splitter across the shapes a
// rule file can take.
func TestSplitFrontmatter(t *testing.T) {
	cases := []struct {
		name     string
		in       string
		wantFM   string
		wantBody string
		wantOK   bool
	}{
		{"present", "---\npaths: [a]\n---\nbody", "paths: [a]\n", "body", true},
		{"absent", "just a body, no delimiters", "", "", false},
		{"malformed no closing", "---\npaths: [a]\nbody with no closing delim", "", "", false},
		{"bom tolerated", "\ufeff---\npaths: [a]\n---\nbody", "paths: [a]\n", "body", true},
		{"crlf normalised", "---\r\npaths: [a]\r\n---\r\nbody", "paths: [a]\n", "body", true},
		{"empty frontmatter", "---\n---\nbody", "", "body", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fm, body, ok := SplitFrontmatter(tc.in)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if fm != tc.wantFM {
				t.Errorf("fm = %q, want %q", fm, tc.wantFM)
			}
			if body != tc.wantBody {
				t.Errorf("body = %q, want %q", body, tc.wantBody)
			}
		})
	}
}
