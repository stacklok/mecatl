package osfs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// osfs_readroots_test.go covers the explicit READ-ONLY allowed roots
// (WithReadRoots): the out-of-workspace skills carve-out. The security
// invariants pinned here:
//
//   - only Read/Stat consult the allowlist (Write/Glob/Grep stay workspace-only);
//   - matching is exact-root containment; any other out-of-root absolute path
//     fails with ErrPathEscape (in-root absolutes are accepted via resolveInRoot,
//     so the rejection reason is "escapes the workspace root", not "is absolute");
//   - each allowed root is served through its own os.Root, so a symlink inside
//     an allowed root that escapes it is refused like a workspace escape.

// newReadRootsWorkspace builds a workspace plus a fake out-of-workspace skill
// layout (<skills>/demo/SKILL.md + references/guide.md) registered as a read
// root, and an unregistered sibling skill dir holding a file that must stay
// unreachable. It returns the workspace, the ALLOWED skill dir, and the sibling
// dir (all canonical, as resolveRoot would emit).
func newReadRootsWorkspace(t *testing.T) (*Workspace, string, string) {
	t.Helper()
	root := t.TempDir()
	base := t.TempDir()

	allowed := filepath.Join(base, "skills", "demo")
	sibling := filepath.Join(base, "skills", "other")
	for _, d := range []string{filepath.Join(allowed, "references"), sibling} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir %q: %v", d, err)
		}
	}
	writeAbs(t, filepath.Join(allowed, "SKILL.md"), "---\nname: demo\n---\nskill body")
	writeAbs(t, filepath.Join(allowed, "references", "guide.md"), "bundled guide")
	writeAbs(t, filepath.Join(sibling, "SKILL.md"), "shadowed sibling skill")

	ws, err := NewWorkspace(root, WithReadRoots(allowed))
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}
	return ws, canon(t, allowed), canon(t, sibling)
}

// writeAbs writes a file at an absolute path, failing the test on error.
func writeAbs(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %q: %v", path, err)
	}
}

// canon resolves a path through the same canonicalization the allowlist keys on.
func canon(t *testing.T, path string) string {
	t.Helper()
	resolved, err := ResolveRoot(path)
	if err != nil {
		t.Fatalf("resolve %q: %v", path, err)
	}
	return resolved
}

// The regression mirror: an absolute Read/Stat of a discovered skill's SKILL.md
// and of its bundled references must succeed through the allowed root.
func TestReadRootsAbsoluteReadAndStat(t *testing.T) {
	ws, allowed, _ := newReadRootsWorkspace(t)
	ctx := context.Background()

	got, err := ws.Read(ctx, filepath.Join(allowed, "SKILL.md"))
	if err != nil {
		t.Fatalf("Read(allowed SKILL.md): %v", err)
	}
	if !strings.Contains(string(got), "skill body") {
		t.Errorf("Read returned %q, want the skill body", got)
	}

	got, err = ws.Read(ctx, filepath.Join(allowed, "references", "guide.md"))
	if err != nil {
		t.Fatalf("Read(bundled reference): %v", err)
	}
	if string(got) != "bundled guide" {
		t.Errorf("Read = %q, want %q", got, "bundled guide")
	}

	fi, err := ws.Stat(ctx, filepath.Join(allowed, "SKILL.md"))
	if err != nil {
		t.Fatalf("Stat(allowed SKILL.md): %v", err)
	}
	if fi.Name != "SKILL.md" {
		t.Errorf("Stat.Name = %q, want SKILL.md", fi.Name)
	}
}

// Any absolute path NOT under an allowed root AND NOT inside the workspace root
// must keep failing with ErrPathEscape: a sibling skill dir, an unrelated
// system-shaped path, and a prefix-sharing directory name (allowed root + suffix
// without a separator). These are out-of-root absolute paths; resolveInRoot
// rejects them with ErrPathEscape (the message now states the path escapes the
// workspace root, not merely that it "is absolute" — in-root absolutes are now
// accepted, so "is absolute" would be a lie).
func TestReadRootsNonAllowedAbsoluteStillEscapes(t *testing.T) {
	ws, allowed, sibling := newReadRootsWorkspace(t)
	ctx := context.Background()

	// A directory whose name extends the allowed root without a path separator
	// must not match (prefix-boundary check).
	prefixTrap := allowed + "-extra"
	if err := os.MkdirAll(prefixTrap, 0o755); err != nil {
		t.Fatalf("mkdir prefix trap: %v", err)
	}
	writeAbs(t, filepath.Join(prefixTrap, "leak.txt"), "must not leak")

	for _, p := range []string{
		filepath.Join(sibling, "SKILL.md"),
		filepath.Join(prefixTrap, "leak.txt"),
		"/nonexistent/system/config.txt",
	} {
		_, err := ws.Read(ctx, p)
		if !errors.Is(err, ErrPathEscape) {
			t.Errorf("Read(%q) error = %v, want ErrPathEscape", p, err)
			continue
		}
		if _, err := ws.Stat(ctx, p); !errors.Is(err, ErrPathEscape) {
			t.Errorf("Stat(%q) error = %v, want ErrPathEscape", p, err)
		}
	}
}

// A symlink INSIDE an allowed root that points outside it must be refused by
// that root's own os.Root — the same containment the workspace root has.
func TestReadRootsSymlinkEscapeRejected(t *testing.T) {
	ws, allowed, _ := newReadRootsWorkspace(t)
	ctx := context.Background()

	outside := filepath.Join(t.TempDir(), "secret.txt")
	writeAbs(t, outside, "top secret")
	if err := os.Symlink(outside, filepath.Join(allowed, "evil")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	if _, err := ws.Read(ctx, filepath.Join(allowed, "evil")); !errors.Is(err, ErrPathEscape) {
		t.Errorf("Read(escaping symlink in allowed root) error = %v, want ErrPathEscape", err)
	}
	if _, err := ws.Stat(ctx, filepath.Join(allowed, "evil")); !errors.Is(err, ErrPathEscape) {
		t.Errorf("Stat(escaping symlink in allowed root) error = %v, want ErrPathEscape", err)
	}
}

// The carve-out is READ-ONLY: Write to an absolute path under an allowed root
// must keep failing (only Read/Stat consult the allowlist), and Glob/Grep must
// not enumerate or search allowed-root content.
func TestReadRootsWriteGlobGrepStayWorkspaceOnly(t *testing.T) {
	ws, allowed, _ := newReadRootsWorkspace(t)
	ctx := context.Background()

	target := filepath.Join(allowed, "SKILL.md")
	if err := ws.Write(ctx, target, []byte("mutated")); !errors.Is(err, ErrPathEscape) {
		t.Fatalf("Write(allowed root) error = %v, want ErrPathEscape (read-only carve-out)", err)
	}
	// The skill file is untouched.
	got, err := ws.Read(ctx, target)
	if err != nil || !strings.Contains(string(got), "skill body") {
		t.Fatalf("skill file changed or unreadable after refused Write: %q, %v", got, err)
	}

	matches, err := ws.Glob(ctx, "**/*.md")
	if err != nil {
		t.Fatalf("Glob: %v", err)
	}
	if len(matches) != 0 {
		t.Errorf("Glob enumerated allowed-root files: %v (enumeration stays workspace-only)", matches)
	}
	hits, err := ws.Grep(ctx, "skill body", "")
	if err != nil {
		t.Fatalf("Grep: %v", err)
	}
	if len(hits) != 0 {
		t.Errorf("Grep searched allowed-root content: %v (search stays workspace-only)", hits)
	}
}

// Construction behavior at the edges: an ABSENT read root is skipped (a skill
// dir deleted between discovery and construction must not brick the workspace;
// reads under it then fail with the ordinary escape), while a present-but-
// unopenable one (a regular file where a directory is required) is a loud
// construction error. An empty entry is ignored.
func TestReadRootsConstructionAbsentSkippedUnopenableFatal(t *testing.T) {
	root := t.TempDir()
	gone := filepath.Join(t.TempDir(), "deleted-skill")

	ws, err := NewWorkspace(root, WithReadRoots(gone, ""))
	if err != nil {
		t.Fatalf("NewWorkspace with absent read root: %v (absent dirs must be skipped)", err)
	}
	if _, err := ws.Read(context.Background(), filepath.Join(gone, "SKILL.md")); !errors.Is(err, ErrPathEscape) {
		t.Errorf("Read under skipped absent root error = %v, want ErrPathEscape", err)
	}

	notADir := filepath.Join(t.TempDir(), "file.txt")
	writeAbs(t, notADir, "not a dir")
	if _, err := NewWorkspace(root, WithReadRoots(notADir)); err == nil {
		t.Error("NewWorkspace with a non-directory read root should fail loudly")
	}
}

// The Bash channel the skills design depends on: the CommandRunner is
// cwd-rooted, not FS-confined, so a bundled skill script OUTSIDE the workspace
// runs by absolute path with no allowlist involved (governance gates it — the
// deliberate non-loosening).
func TestCommandRunnerExecutesAbsoluteScript(t *testing.T) {
	script := filepath.Join(t.TempDir(), "hello.sh")
	writeAbs(t, script, "#!/bin/sh\necho bundled-script-ran\n")

	runner, err := NewCommandRunner(t.TempDir())
	if err != nil {
		t.Fatalf("NewCommandRunner: %v", err)
	}
	res, err := runner.Run(context.Background(), "sh "+script)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.ExitCode != 0 || !strings.Contains(res.Stdout, "bundled-script-ran") {
		t.Errorf("absolute script run = exit %d, stdout %q; want success with marker", res.ExitCode, res.Stdout)
	}
}

// An absolute path that resolves INSIDE the workspace root is now accepted (it
// is the same physical file a relative path reaches, addressed by its absolute
// alias — see resolveInRoot). Passing the workspace root as a read root still
// does not create an allowlist entry (it is dedup'd at construction), but the
// absolute in-root path no longer needs one: resolveInRoot reduces it to the
// root-relative form and serves it through the workspace os.Root.
func TestReadRootsWorkspaceRootAbsoluteAccepted(t *testing.T) {
	root := t.TempDir()
	ws, err := NewWorkspace(root, WithReadRoots(root))
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}
	ctx := context.Background()
	if err := ws.Write(ctx, "inside.txt", []byte("x")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	abs := filepath.Join(canon(t, root), "inside.txt")
	got, err := ws.Read(ctx, abs)
	if err != nil {
		t.Fatalf("Read(absolute workspace path) error = %v, want success (in-root absolutes are accepted)", err)
	}
	if string(got) != "x" {
		t.Errorf("Read(absolute workspace path) = %q, want %q", got, "x")
	}
}
