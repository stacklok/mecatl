package osfs_test

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stacklok/mecatl/internal/adapter/osfs"
)

// relaxed_read_test.go pins the path-escape-posture Scenario 2 osfs half
// (docs/acceptance/path-escape-posture.md): Read/Stat serve a canonicalized
// out-of-root absolute path ONLY under the explicit WithRelaxedReads
// construction option (the zero-value workspace stays deny), serving the leaf
// through a FRESH *os.Root opened on the target's canonical parent so a
// symlink inside the target dir that escapes further is refused by that
// root's containment (never a bare os.Open). Write/Edit and Glob/Grep stay
// workspace-confined under the option.

// setupRelaxedFS builds the shared fixture:
//
//	root/         the workspace root
//	outside/      an out-of-root directory with a regular file
//	outside/deep/ a deeper out-of-root directory with files
//	third/        a THIRD out-of-root directory (outside AND root's sibling)
//	outside/link -> third (a symlink INSIDE the target dir escaping FURTHER
//	               out, never back into the workspace)
func setupRelaxedFS(t *testing.T) (root, outside, third string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("symlink fixtures require a POSIX filesystem")
	}
	base := t.TempDir()
	root = filepath.Join(base, "root")
	outside = filepath.Join(base, "outside")
	third = filepath.Join(base, "third")
	for _, d := range []string{root, outside, filepath.Join(outside, "deep"), third} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("MkdirAll(%q): %v", d, err)
		}
	}
	for name, content := range map[string]string{
		filepath.Join(root, "in.txt"):           "in-root",
		filepath.Join(outside, "out.txt"):       "relaxed-read",
		filepath.Join(outside, "deep", "d.txt"): "relaxed-deep",
		filepath.Join(outside, "deep", "e.txt"): "relaxed-deep-2",
		filepath.Join(third, "secret.txt"):      "third-secret",
	} {
		if err := os.WriteFile(name, []byte(content), 0o644); err != nil {
			t.Fatalf("WriteFile(%q): %v", name, err)
		}
	}
	// A symlink INSIDE the out-of-root target dir whose target escapes FURTHER
	// (to a THIRD out-of-root dir): the fresh os.Root serving the relaxed read
	// must refuse it (containment survives the relax). The target is a SIBLING
	// of the workspace root, never inside it, so resolveInRoot's in-root
	// acceptance cannot launder it (an in-root symlink whose target resolves
	// back INTO the workspace is served in-root by design — the relax must
	// refuse only escapes the in-root path would ALSO reject).
	if err := os.Symlink(third, filepath.Join(outside, "link")); err != nil {
		t.Fatalf("Symlink(outside/link): %v", err)
	}
	return root, outside, third
}

// TestRelaxedReads_OutOfRootReadServed pins the positive half: under
// WithRelaxedReads an out-of-root absolute Read and Stat succeed, returning
// the real bytes — while the DEFAULT (zero-value) workspace still denies the
// same path with ErrPathEscape.
func TestRelaxedReads_OutOfRootReadServed(t *testing.T) {
	t.Parallel()
	root, outside, _ := setupRelaxedFS(t)
	target := filepath.Join(outside, "out.txt")

	// DEFAULT OFF: the zero-value workspace denies.
	plain, err := osfs.NewWorkspace(root)
	if err != nil {
		t.Fatalf("NewWorkspace(plain): %v", err)
	}
	if _, err := plain.Read(t.Context(), target); !errors.Is(err, osfs.ErrPathEscape) {
		t.Fatalf("default workspace Read(%q) = %v, want ErrPathEscape (the default stays deny)", target, err)
	}

	relaxed, err := osfs.NewWorkspace(root, osfs.WithRelaxedReads())
	if err != nil {
		t.Fatalf("NewWorkspace(relaxed): %v", err)
	}
	data, err := relaxed.Read(t.Context(), target)
	if err != nil {
		t.Fatalf("relaxed Read(%q): %v", target, err)
	}
	if string(data) != "relaxed-read" {
		t.Fatalf("relaxed Read(%q) = %q, want %q", target, string(data), "relaxed-read")
	}
	// Stat serves the same path identically.
	fi, err := relaxed.Stat(t.Context(), target)
	if err != nil {
		t.Fatalf("relaxed Stat(%q): %v", target, err)
	}
	if fi.Name != "out.txt" || fi.Size != int64(len("relaxed-read")) {
		t.Fatalf("relaxed Stat(%q) = %+v, want out.txt of %d bytes", target, fi, len("relaxed-read"))
	}
	// A canonicalized path with ".." components lands on the same file.
	dirty := filepath.Join(outside, "deep", "..", "out.txt")
	data, err = relaxed.Read(t.Context(), dirty)
	if err != nil {
		t.Fatalf("relaxed Read(%q): %v", dirty, err)
	}
	if string(data) != "relaxed-read" {
		t.Fatalf("relaxed Read(%q) = %q, want %q", dirty, string(data), "relaxed-read")
	}
	// A deep out-of-root path reads its own bytes (not a hardcoded fixture).
	data, err = relaxed.Read(t.Context(), filepath.Join(outside, "deep", "d.txt"))
	if err != nil {
		t.Fatalf("relaxed Read(deep): %v", err)
	}
	if string(data) != "relaxed-deep" {
		t.Fatalf("relaxed Read(deep) = %q, want %q", string(data), "relaxed-deep")
	}
	// A nonexistent out-of-root path errors as not-exist, never as a silent
	// empty read.
	if _, err := relaxed.Read(t.Context(), filepath.Join(outside, "nope.txt")); err == nil {
		t.Fatal("relaxed Read of a nonexistent out-of-root path must error")
	}
}

// TestRelaxedReads_NestedSymlinkEscapeRejected pins the containment half
// (AC2.6's adapter half): a symlink INSIDE an allowed out-of-root target dir
// whose target escapes further is refused by the serving *os.Root — the relax
// opens a fresh root on the target's parent, so a symlink that escapes THAT
// parent is refused by that root's containment, never followed.
func TestRelaxedReads_NestedSymlinkEscapeRejected(t *testing.T) {
	t.Parallel()
	root, outside, third := setupRelaxedFS(t)
	relaxed, err := osfs.NewWorkspace(root, osfs.WithRelaxedReads())
	if err != nil {
		t.Fatalf("NewWorkspace(relaxed): %v", err)
	}
	// outside/link -> third: reading through the symlink would escape the
	// serving root (outside/) further out to a THIRD out-of-root dir — refused.
	linkTarget := filepath.Join(outside, "link", "secret.txt")
	if _, err := relaxed.Read(t.Context(), linkTarget); err == nil {
		t.Fatalf("relaxed Read(%q) succeeded — the nested symlink escape was followed (containment lost)", linkTarget)
	}
	if _, err := relaxed.Stat(t.Context(), linkTarget); err == nil {
		t.Fatalf("relaxed Stat(%q) succeeded — the nested symlink escape was followed (containment lost)", linkTarget)
	}
	// The THIRD dir's file is NOT reachable through the symlink, but the relax
	// DOES serve the same file by its own direct out-of-root path (the relax
	// grants out-of-root reads broadly — the refusal above is specifically the
	// CONTAINMENT on a symlinked component, not a blanket denial of the third
	// dir). This is the discrimination that pins the os.Root containment: the
	// direct path reads, the symlinked path is refused.
	data, err := relaxed.Read(t.Context(), filepath.Join(third, "secret.txt"))
	if err != nil || string(data) != "third-secret" {
		t.Fatalf("relaxed Read(third/secret.txt) = %q, %v — the relax must serve the direct out-of-root path", string(data), err)
	}
	// Sanity: an in-root path still reads, so the refusal above is not a
	// broken workspace.
	data, err = relaxed.Read(t.Context(), "in.txt")
	if err != nil || string(data) != "in-root" {
		t.Fatalf("in-root Read(in.txt) = %q, %v — the workspace must still serve in-root paths", string(data), err)
	}
}

// TestRelaxedReads_WritesStayConfined pins that the relax is READ-ONLY:
// Write (and the Edit mutation path, via resolvePath) to an out-of-root
// absolute path still fails with ErrPathEscape under WithRelaxedReads, and
// Glob never enumerates outside the root.
func TestRelaxedReads_WritesStayConfined(t *testing.T) {
	t.Parallel()
	root, outside, _ := setupRelaxedFS(t)
	relaxed, err := osfs.NewWorkspace(root, osfs.WithRelaxedReads())
	if err != nil {
		t.Fatalf("NewWorkspace(relaxed): %v", err)
	}
	target := filepath.Join(outside, "written.txt")
	if err := relaxed.Write(t.Context(), target, []byte("nope")); !errors.Is(err, osfs.ErrPathEscape) {
		t.Fatalf("relaxed Write(%q) = %v, want ErrPathEscape (the relax is read-only)", target, err)
	}
	if _, err := os.Stat(target); err == nil {
		t.Fatalf("relaxed Write created %q — writes must stay workspace-confined", target)
	}
	// Glob stays workspace-confined: an out-of-root pattern is a pattern, not
	// a path, and never enumerates outside the root.
	matches, err := relaxed.Glob(t.Context(), filepath.Join(outside, "**", "*.txt"))
	if err != nil {
		t.Fatalf("relaxed Glob: %v", err)
	}
	for _, m := range matches {
		if filepath.IsAbs(m) {
			t.Fatalf("relaxed Glob returned out-of-root match %q — Glob stays workspace-confined", m)
		}
	}
}
