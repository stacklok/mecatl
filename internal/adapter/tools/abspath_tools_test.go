package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/osfs"
)

// abspath_tools_test.go is the tool-level e2e of absolute-path resolution
// (issue #154 / ADR 0047) over a REAL osfs.Workspace: it drives absolute in-root
// paths through the REAL Read/Edit/Write/Glob tools, not just the Workspace
// methods. A regression in how the tools thread the path, or in the ledger seam
// under the real Edit flow, would not be caught by the osfs unit tests alone.

// newOSFSWorkspace builds a real osfs.Workspace rooted at a fresh tempdir.
func newOSFSWorkspace(t *testing.T) *osfs.Workspace {
	t.Helper()
	ws, err := osfs.NewWorkspace(t.TempDir())
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}
	return ws
}

// TestAbsoluteInRootThroughRealTools covers the full path-resolution surface the
// model drives: Write by relative, Read by absolute, Edit by absolute, the
// cross-form read-before-edit invariant, Write of a brand-new leaf by absolute
// (the not-yet-existing-leaf ancestor walk), and Glob enumeration of an
// absolute-path write under its RELATIVE name.
func TestAbsoluteInRootThroughRealTools(t *testing.T) {
	ws := newOSFSWorkspace(t)
	root := ws.Root()

	// 1. Write a file by RELATIVE path.
	res := exec(t, WriteTool{}, call(t, "Write", map[string]any{
		"path": "src/main.go", "content": "package main\nvar A = 1\n",
	}), ws)
	if res.IsError {
		t.Fatalf("Write(relative): %s", res.Content)
	}

	// 2. Read it back by its ABSOLUTE in-root path — line-numbered content returns.
	abs := filepath.Join(root, "src", "main.go")
	res = exec(t, ReadTool{}, call(t, "Read", map[string]any{"path": abs}), ws)
	if res.IsError {
		t.Fatalf("Read(absolute): %s", res.Content)
	}
	if !strings.Contains(res.Content, "1\tpackage main") || !strings.Contains(res.Content, "2\tvar A = 1") {
		t.Errorf("Read(absolute) missing line-numbered content, got %q", res.Content)
	}

	// 3. Read by absolute then Edit by absolute (old_string → new_string) — lands.
	res = exec(t, EditTool{}, call(t, "Edit", map[string]any{
		"path": abs, "old_string": "var A = 1", "new_string": "var A = 2",
	}), ws)
	if res.IsError {
		t.Fatalf("Edit(absolute): %s", res.Content)
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		t.Fatalf("ReadFile after edit: %v", err)
	}
	if !strings.Contains(string(data), "var A = 2") {
		t.Errorf("edit by absolute did not land on disk: %q", data)
	}

	// 4. Cross-form: Read by RELATIVE, Edit by ABSOLUTE — the read-before-edit
	//    invariant MUST hold (the real-loop analog of the ledger unit test, and
	//    the one most likely to silently break).
	exec(t, ReadTool{}, call(t, "Read", map[string]any{"path": "src/main.go"}), ws)
	res = exec(t, EditTool{}, call(t, "Edit", map[string]any{
		"path": abs, "old_string": "var A = 2", "new_string": "var A = 3",
	}), ws)
	if res.IsError {
		t.Fatalf("Edit(absolute after Read relative) should succeed via cross-form ledger: %s", res.Content)
	}

	// 5. Write a brand-NEW file by its absolute in-root path (no prior relative
	//    write) — exercises resolveInRoot's not-yet-existing-leaf ancestor walk.
	newAbs := filepath.Join(root, "newdir", "created.go")
	res = exec(t, WriteTool{}, call(t, "Write", map[string]any{
		"path": newAbs, "content": "package newdir\n",
	}), ws)
	if res.IsError {
		t.Fatalf("Write(absolute new leaf): %s", res.Content)
	}
	// Read it back by relative.
	res = exec(t, ReadTool{}, call(t, "Read", map[string]any{"path": "newdir/created.go"}), ws)
	if res.IsError {
		t.Fatalf("Read(relative after absolute Write of new leaf): %s", res.Content)
	}
	if !strings.Contains(res.Content, "package newdir") {
		t.Errorf("new-leaf content mismatch: %q", res.Content)
	}

	// 6. After writing by absolute, Glob by a relative pattern must surface the
	//    file under its RELATIVE name — pins that absolute-path writes land at the
	//    relative-form location Glob/Grep enumerate.
	res = exec(t, GlobTool{}, call(t, "Glob", map[string]any{"pattern": "**/*.go"}), ws)
	if res.IsError {
		t.Fatalf("Glob: %s", res.Content)
	}
	for _, want := range []string{"src/main.go", "newdir/created.go"} {
		if !strings.Contains(res.Content, want) {
			t.Errorf("Glob did not surface %q (absolute write must land at the relative form): %q", want, res.Content)
		}
	}

	// Sanity: the absolute form must NOT appear in Glob output (Glob returns
	// session-relative paths).
	if strings.Contains(res.Content, root) {
		t.Errorf("Glob surfaced the absolute root path; it must enumerate relative names only: %q", res.Content)
	}
}

// TestAbsoluteOutOfRootThroughRealTools pins that an out-of-root absolute path
// is rejected by the real Read/Write/Edit tools — the escape must surface, not
// silently read/write outside the workspace.
func TestAbsoluteOutOfRootThroughRealTools(t *testing.T) {
	ws := newOSFSWorkspace(t)

	other := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(other, []byte("outside"), 0o644); err != nil {
		t.Fatalf("seed outside: %v", err)
	}

	// Read of an out-of-root absolute is a tool error.
	res := exec(t, ReadTool{}, call(t, "Read", map[string]any{"path": other}), ws)
	if !res.IsError {
		t.Fatalf("Read(out-of-root absolute) must error")
	}

	// Write to an out-of-root absolute is rejected. The Write tool surfaces a
	// workspace escape as a hard Go error (it wraps the osfs error), so call
	// Execute directly to capture it rather than the exec harness which fails
	// the test on a harness-level error.
	writeCall := call(t, "Write", map[string]any{"path": other, "content": "x"})
	writeTool := WriteTool{}
	env := tool.MustEnvironment(session.EnvironmentRef{Kind: session.EnvKindLocal, ID: ws.Root()}, ws, nil)
	if _, err := writeTool.Execute(context.Background(), writeCall, env); err == nil || !strings.Contains(err.Error(), "escapes workspace root") {
		t.Fatalf("Write(out-of-root absolute) must surface the escape error, got %v", err)
	}

	// The outside file is untouched.
	if data, _ := os.ReadFile(other); string(data) != "outside" {
		t.Fatalf("outside file was modified: %q", data)
	}
}
