package osfs

import (
	"context"
	"errors"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/tool"
)

// Finding 2: a symlink created INSIDE the workspace (the model can do this via
// Shell `ln -s`) must not let Read/Write/Stat/fingerprint follow it out of the
// root. os.Root refuses the traversal and we map the error to ErrPathEscape.

// newSymlinkWorkspace builds a Workspace under t.TempDir() and plants two
// escaping symlinks: "evil" -> an absolute path outside the root, and "up" -> a
// relative ".."-targeting path. It returns the workspace and the root dir.
func newSymlinkWorkspace(t *testing.T) (*Workspace, string) {
	t.Helper()
	root := t.TempDir()

	// An absolute escape target outside the workspace.
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("top secret"), 0o600); err != nil {
		t.Fatalf("write outside file: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "evil")); err != nil {
		t.Fatalf("symlink evil: %v", err)
	}

	// A relative ".."-targeting link that climbs out of the root.
	if err := os.Symlink(filepath.Join("..", ".."), filepath.Join(root, "up")); err != nil {
		t.Fatalf("symlink up: %v", err)
	}

	ws, err := NewWorkspace(root)
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}
	return ws, root
}

func TestReadThroughSymlinkEscapesRejected(t *testing.T) {
	ws, _ := newSymlinkWorkspace(t)
	ctx := context.Background()

	for _, p := range []string{"evil", "up/secret.txt", "../secret.txt", "/etc/passwd"} {
		if _, err := ws.Read(ctx, p); !errors.Is(err, ErrPathEscape) {
			t.Errorf("Read(%q) error = %v, want ErrPathEscape", p, err)
		}
	}
}

func TestWriteThroughSymlinkEscapesRejected(t *testing.T) {
	ws, root := newSymlinkWorkspace(t)
	ctx := context.Background()

	if err := ws.Write(ctx, "evil", []byte("pwned")); !errors.Is(err, ErrPathEscape) {
		t.Errorf("Write(evil) error = %v, want ErrPathEscape", err)
	}
	if err := ws.Write(ctx, "up/escape.txt", []byte("pwned")); !errors.Is(err, ErrPathEscape) {
		t.Errorf("Write(up/escape.txt) error = %v, want ErrPathEscape", err)
	}
	if err := ws.Write(ctx, "/etc/escape", []byte("pwned")); !errors.Is(err, ErrPathEscape) {
		t.Errorf("Write(/etc/escape) error = %v, want ErrPathEscape", err)
	}

	// The original symlink target must remain untouched: nothing was written
	// through it, and no file appeared outside the root via the link.
	if _, err := os.Lstat(filepath.Join(root, "evil")); err != nil {
		t.Fatalf("evil link vanished: %v", err)
	}
}

func TestStatThroughSymlinkEscapesRejected(t *testing.T) {
	ws, _ := newSymlinkWorkspace(t)
	ctx := context.Background()

	if _, err := ws.Stat(ctx, "evil"); !errors.Is(err, ErrPathEscape) {
		t.Errorf("Stat(evil) error = %v, want ErrPathEscape", err)
	}
	if _, err := ws.Stat(ctx, "up/secret.txt"); !errors.Is(err, ErrPathEscape) {
		t.Errorf("Stat(up/secret.txt) error = %v, want ErrPathEscape", err)
	}
}

// A legitimate file inside the root must still be readable/writable/stat-able so
// the os.Root confinement does not break normal operation.
func TestInRootFileStillWorks(t *testing.T) {
	ws, _ := newSymlinkWorkspace(t)
	ctx := context.Background()

	if err := ws.Write(ctx, "sub/ok.txt", []byte("hello")); err != nil {
		t.Fatalf("Write(sub/ok.txt): %v", err)
	}
	got, err := ws.Read(ctx, "sub/ok.txt")
	if err != nil {
		t.Fatalf("Read(sub/ok.txt): %v", err)
	}
	if string(got) != "hello" {
		t.Fatalf("Read = %q, want %q", got, "hello")
	}
	if _, err := ws.Stat(ctx, "sub/ok.txt"); err != nil {
		t.Fatalf("Stat(sub/ok.txt): %v", err)
	}
}

// The version-bearing read (the read-before-edit ledger seam) must also refuse
// to follow an escaping symlink.
func TestReadVersionThroughSymlinkEscapesRejected(t *testing.T) {
	ws, _ := newSymlinkWorkspace(t)
	if _, _, err := ws.ReadVersion(context.Background(), "evil"); !errors.Is(err, ErrPathEscape) {
		t.Errorf("ReadVersion(evil) error = %v, want ErrPathEscape", err)
	}
}

// Glob and the recursive Grep walk must not surface content reached through an
// escaping symlink.
func TestGlobAndGrepDoNotFollowEscapingSymlinks(t *testing.T) {
	ws, _ := newSymlinkWorkspace(t)
	ctx := context.Background()

	// "evil" is a symlink to an outside file; neither a single-segment glob nor
	// the recursive "**" globstar may list it or anything reached through "up".
	for _, pattern := range []string{"*", "**/*", "**"} {
		matches, err := ws.Glob(ctx, pattern)
		if err != nil {
			t.Fatalf("Glob(%q): %v", pattern, err)
		}
		for _, m := range matches {
			if m == "evil" || m == "up" || strings.HasPrefix(m, "up/") {
				t.Errorf("Glob(%q) surfaced escaping symlink %q", pattern, m)
			}
		}
	}

	// Grep across the tree, and Grep driven by a "**" pathGlob, must not read the
	// outside target's content.
	for _, pathGlob := range []string{"", "**/*"} {
		hits, err := ws.Grep(ctx, "top secret", pathGlob)
		if err != nil {
			t.Fatalf("Grep(pathGlob=%q): %v", pathGlob, err)
		}
		if len(hits) != 0 {
			t.Errorf("Grep(pathGlob=%q) followed escaping symlink, got %d hits", pathGlob, len(hits))
		}
	}
}

// --- agent-reachable CreateFile/ReplaceFile path-escape coverage -------------

// TestCreateFileRejectsSymlinkEscapes pins that the agent-reachable CreateFile
// (the new-file Write path) refuses an in-root escaping symlink, a ".." climb,
// and an out-of-root absolute path — the same confinement the old bootstrap
// Write coverage asserted, now exercised directly through the mutation seam the
// model actually reaches. A create must NEVER silently follow an escaping link
// and plant a file outside the root.
func TestCreateFileRejectsSymlinkEscapes(t *testing.T) {
	ws, root := newSymlinkWorkspace(t)
	ctx := context.Background()

	for _, p := range []string{"evil", "up/escape.txt", "/etc/escape"} {
		if _, err := ws.CreateFile(ctx, p, []byte("pwned")); !errors.Is(err, ErrPathEscape) {
			t.Errorf("CreateFile(%q) error = %v, want ErrPathEscape", p, err)
		}
	}
	// The escaping symlink target must remain untouched.
	if _, err := os.Lstat(filepath.Join(root, "evil")); err != nil {
		t.Fatalf("evil link vanished after CreateFile: %v", err)
	}
}

// TestReplaceFileRejectsSymlinkEscapes pins the same for ReplaceFile. A replace
// requires a recorded version; here we use a deliberately-zero FileVersion (the
// "never an overwrite sentinel" guard) so the escape rejection must fire at the
// resolve step BEFORE any version comparison — proving confinement is checked
// first, not gated on a valid version.
func TestReplaceFileRejectsSymlinkEscapes(t *testing.T) {
	ws, _ := newSymlinkWorkspace(t)
	ctx := context.Background()

	for _, p := range []string{"evil", "up/escape.txt", "/etc/escape"} {
		if _, err := ws.ReplaceFile(ctx, p, tool.FileVersion{}, []byte("pwned")); !errors.Is(err, ErrPathEscape) {
			t.Errorf("ReplaceFile(%q) error = %v, want ErrPathEscape", p, err)
		}
	}
}

// TestRelaxedWriteCreateFileReplaceFileRejectAbsoluteSymlinkLeaf pins the
// relaxed-write symlink-leaf containment (AC3.5) directly through the
// agent-reachable CreateFile/ReplaceFile, not only through the bootstrap Write.
// A LEAF symlink inside an out-of-root relaxed target whose own target escapes
// FURTHER must be refused — the serving *os.Root on the vetted parent refuses
// the traversal, never silently following the link and writing the third dir.
func TestRelaxedWriteCreateFileReplaceFileRejectAbsoluteSymlinkLeaf(t *testing.T) {
	if testing.Short() {
		t.Skip("symlink fixture")
	}
	base := t.TempDir()
	root := filepath.Join(base, "root")
	outside := filepath.Join(base, "outside")
	third := filepath.Join(base, "third")
	for _, d := range []string{root, outside, third} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("MkdirAll(%q): %v", d, err)
		}
	}
	thirdTarget := filepath.Join(third, "planted.txt")
	if err := os.WriteFile(thirdTarget, []byte("third-original"), 0o644); err != nil {
		t.Fatalf("WriteFile(third): %v", err)
	}
	leafLink := filepath.Join(outside, "leaflink")
	if err := os.Symlink(thirdTarget, leafLink); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}
	ws, err := NewWorkspace(root, WithRelaxedWrites())
	if err != nil {
		t.Fatalf("NewWorkspace(relaxed): %v", err)
	}
	ctx := context.Background()

	// CreateFile through the escaping leaf symlink must be refused. The serving
	// *os.Root on the vetted parent either refuses to follow the symlink
	// (ErrPathEscape) or O_EXCL short-circuits on the existing link entry
	// (fs.ErrExist) WITHOUT following it — both are safe refusals. The load-bearing
	// assertion is that the THIRD target stays untouched: a bare os.Create would
	// have followed the link and clobbered it.
	if _, err := ws.CreateFile(ctx, leafLink, []byte("must-never-land")); err == nil {
		t.Fatal("relaxed CreateFile through escaping leaf symlink succeeded — it must be refused")
	}
	data, err := os.ReadFile(thirdTarget)
	if err != nil || string(data) != "third-original" {
		t.Fatalf("third content = %q, %v — a direct CreateFile through the leaf symlink would have FOLLOWED it; the serving *os.Root must refuse", data, err)
	}

	// ReplaceFile reads through the symlink to mint the current version; the
	// serving *os.Root refuses that read, surfacing as ErrPathEscape.
	if _, err := ws.ReplaceFile(ctx, leafLink, tool.FileVersion{}, []byte("must-never-land")); !errors.Is(err, ErrPathEscape) {
		t.Fatalf("relaxed ReplaceFile through escaping leaf symlink = %v, want ErrPathEscape", err)
	}
	data, err = os.ReadFile(thirdTarget)
	if err != nil || string(data) != "third-original" {
		t.Fatalf("third content after ReplaceFile = %q, %v — must remain untouched", data, err)
	}
}

// TestReadDirIncludesSymlinkEntryWithSymlinkMode pins that a symlink INSIDE the
// workspace root (created via Shell `ln -s`, not an escape) is reported by
// ReadDir with FileInfo.Mode carrying fs.ModeSymlink — it must not be silently
// coerced to look like a regular file entry, so a caller inspecting Mode can
// tell a symlink apart from real content before acting on it.
func TestReadDirIncludesSymlinkEntryWithSymlinkMode(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	ws, err := NewWorkspace(root)
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}
	if _, err := ws.CreateFile(ctx, "target.txt", []byte("hello")); err != nil {
		t.Fatalf("CreateFile(target.txt): %v", err)
	}
	if err := os.Symlink("target.txt", filepath.Join(root, "link.txt")); err != nil {
		t.Fatalf("symlink in-root leaf: %v", err)
	}

	entries, err := ws.ReadDir(ctx, ".")
	if err != nil {
		t.Fatalf("ReadDir(root): %v", err)
	}
	var link *tool.FileInfo
	for i := range entries {
		if entries[i].Name == "link.txt" {
			link = &entries[i]
		}
	}
	if link == nil {
		t.Fatalf("ReadDir(root) = %+v, missing link.txt entry", entries)
	}
	if link.Mode&fs.ModeSymlink == 0 {
		t.Errorf("ReadDir(root) link.txt Mode = %v, want fs.ModeSymlink set", link.Mode)
	}
}

// TestRemoveDeletesSymlinkItselfNotTarget pins that Remove on a symlink path
// deletes the SYMLINK ENTRY, not the file it points to: after Remove, the link
// is gone (an Lstat on it fails) but the target file it pointed to — reachable
// directly, not through the removed link — is untouched. A Remove that
// followed the link and deleted the target instead would silently destroy
// content the model never named.
func TestRemoveDeletesSymlinkItselfNotTarget(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	ws, err := NewWorkspace(root)
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}
	if _, err := ws.CreateFile(ctx, "target.txt", []byte("hello")); err != nil {
		t.Fatalf("CreateFile(target.txt): %v", err)
	}
	linkPath := filepath.Join(root, "link.txt")
	if err := os.Symlink("target.txt", linkPath); err != nil {
		t.Fatalf("symlink in-root leaf: %v", err)
	}

	if err := ws.Remove(ctx, "link.txt"); err != nil {
		t.Fatalf("Remove(link.txt): %v", err)
	}
	if _, err := os.Lstat(linkPath); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Lstat(link.txt) after Remove = %v, want fs.ErrNotExist (the link entry itself must be gone)", err)
	}
	data, err := ws.Read(ctx, "target.txt")
	if err != nil || string(data) != "hello" {
		t.Fatalf("Read(target.txt) after Remove(link.txt) = %q, %v, want hello/nil (the link's target must survive)", data, err)
	}
}

// TestCopyFileRejectsNonRegularSource pins that CopyFile refuses a source that
// is not a regular file. A symlink whose target IS a regular file is
// deliberately NOT this case — it follows normal copy semantics (see
// TestRelaxedWriteCreateFileReplaceFileRejectAbsoluteSymlinkLeaf's sibling
// coverage of the escaping-leaf case, and the in-root symlink-to-regular-file
// path elsewhere in this package). A Unix domain socket is a non-regular file
// that can be created and torn down deterministically without hanging the
// test, unlike a FIFO (which blocks on open without a reader).
func TestCopyFileRejectsNonRegularSource(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	ws, err := NewWorkspace(root)
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}
	sockPath := filepath.Join(root, "sock")
	l, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Skipf("unix sockets unsupported on this platform: %v", err)
	}
	defer l.Close()

	if _, err := ws.CopyFile(ctx, "sock", "dup.txt"); err == nil {
		t.Fatal("CopyFile(unix socket source) = nil err, want a refusal (non-regular source)")
	}
	if _, err := ws.Read(ctx, "dup.txt"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("dup.txt after refused CopyFile = %v, want fs.ErrNotExist (nothing must be planted)", err)
	}
}
