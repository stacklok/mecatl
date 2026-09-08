package osfs

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stacklok/mecatl/engine/tool"
)

func newAliasWorkspaces(t *testing.T) (*Workspace, *Workspace, string) {
	t.Helper()
	root := t.TempDir()
	first, err := NewWorkspace(root)
	if err != nil {
		t.Fatalf("NewWorkspace(first): %v", err)
	}
	second, err := NewWorkspace(root)
	if err != nil {
		t.Fatalf("NewWorkspace(second): %v", err)
	}
	return first, second, root
}

func assertSharedMutationIdentity(t *testing.T, first *Workspace, firstPath string, second *Workspace, secondPath string) {
	t.Helper()
	firstCanon, _, firstRoot, _, err := first.resolveWriteAbs(firstPath)
	if err != nil {
		t.Fatalf("resolveWriteAbs(%q): %v", firstPath, err)
	}
	if firstRoot != nil {
		defer func() { _ = firstRoot.Close() }()
	}
	secondCanon, _, secondRoot, _, err := second.resolveWriteAbs(secondPath)
	if err != nil {
		t.Fatalf("resolveWriteAbs(%q): %v", secondPath, err)
	}
	if secondRoot != nil {
		defer func() { _ = secondRoot.Close() }()
	}
	if firstCanon != secondCanon {
		t.Fatalf("mutation identities differ: %q -> %q, %q -> %q", firstPath, firstCanon, secondPath, secondCanon)
	}
	if pathLock(firstCanon) != pathLock(secondCanon) {
		t.Fatalf("physical aliases %q and %q do not share a mutation lock", firstPath, secondPath)
	}
}

func TestCreateFileSerializesParentSymlinkAliasAcrossWorkspaces(t *testing.T) {
	first, second, root := newAliasWorkspaces(t)
	if err := os.Mkdir(filepath.Join(root, "real"), 0o755); err != nil {
		t.Fatalf("Mkdir(real): %v", err)
	}
	if err := os.Symlink("real", filepath.Join(root, "alias")); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}
	assertSharedMutationIdentity(t, first, "alias/new.txt", second, "real/new.txt")

	type outcome struct {
		path    string
		content string
		version tool.FileVersion
		err     error
	}
	start := make(chan struct{})
	out := make(chan outcome, 2)
	var ready sync.WaitGroup
	ready.Add(2)
	for _, call := range []struct {
		ws      *Workspace
		path    string
		content string
	}{
		{first, "alias/new.txt", "alias-winner"},
		{second, "real/new.txt", "target-winner"},
	} {
		go func() {
			ready.Done()
			<-start
			version, err := call.ws.CreateFile(context.Background(), call.path, []byte(call.content))
			out <- outcome{path: call.path, content: call.content, version: version, err: err}
		}()
	}
	ready.Wait()
	close(start)
	one, two := <-out, <-out

	var winner, loser outcome
	switch {
	case one.err == nil && errors.Is(two.err, fs.ErrExist):
		winner, loser = one, two
	case two.err == nil && errors.Is(one.err, fs.ErrExist):
		winner, loser = two, one
	default:
		t.Fatalf("CreateFile outcomes = (%v, %v), want one success and one fs.ErrExist", one.err, two.err)
	}
	if !os.IsExist(loser.err) {
		t.Fatalf("create conflict %q is not recognized by os.IsExist", loser.err)
	}
	if !strings.Contains(loser.err.Error(), loser.path) {
		t.Fatalf("create conflict %q does not name requested path %q", loser.err, loser.path)
	}
	data, version, err := first.ReadVersion(context.Background(), "real/new.txt")
	if err != nil {
		t.Fatalf("ReadVersion: %v", err)
	}
	if string(data) != winner.content || !version.Equal(winner.version) {
		t.Fatalf("created file = %q with winner-version=%v, want %q and matching version", data, version.Equal(winner.version), winner.content)
	}
}

func TestReplaceFileSerializesLeafSymlinkAliasAcrossWorkspaces(t *testing.T) {
	first, second, root := newAliasWorkspaces(t)
	if err := os.Mkdir(filepath.Join(root, "real"), 0o755); err != nil {
		t.Fatalf("Mkdir(real): %v", err)
	}
	target := filepath.Join(root, "real", "target.txt")
	if err := os.WriteFile(target, []byte("base"), 0o644); err != nil {
		t.Fatalf("WriteFile(target): %v", err)
	}
	if err := os.Symlink(filepath.Join("real", "target.txt"), filepath.Join(root, "alias.txt")); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}
	assertSharedMutationIdentity(t, first, "alias.txt", second, "real/target.txt")
	_, old, err := first.ReadVersion(context.Background(), "real/target.txt")
	if err != nil {
		t.Fatalf("ReadVersion(old): %v", err)
	}

	type outcome struct {
		path    string
		content string
		version tool.FileVersion
		err     error
	}
	start := make(chan struct{})
	out := make(chan outcome, 2)
	var ready sync.WaitGroup
	ready.Add(2)
	for _, call := range []struct {
		ws      *Workspace
		path    string
		content string
	}{
		{first, "alias.txt", "alias-winner"},
		{second, "real/target.txt", "target-winner"},
	} {
		go func() {
			ready.Done()
			<-start
			version, err := call.ws.ReplaceFile(context.Background(), call.path, old, []byte(call.content))
			out <- outcome{path: call.path, content: call.content, version: version, err: err}
		}()
	}
	ready.Wait()
	close(start)
	one, two := <-out, <-out

	isMismatch := func(err error) bool {
		var mismatch *tool.VersionMismatchError
		return errors.As(err, &mismatch)
	}
	var winner outcome
	switch {
	case one.err == nil && isMismatch(two.err):
		winner = one
	case two.err == nil && isMismatch(one.err):
		winner = two
	default:
		t.Fatalf("ReplaceFile outcomes = (%v, %v), want one success and one version mismatch", one.err, two.err)
	}
	data, version, err := second.ReadVersion(context.Background(), "real/target.txt")
	if err != nil {
		t.Fatalf("ReadVersion(final): %v", err)
	}
	if string(data) != winner.content || !version.Equal(winner.version) {
		t.Fatalf("replaced file = %q with winner-version=%v, want %q and matching version", data, version.Equal(winner.version), winner.content)
	}
}

// Ledger keying is lexical by contract (tool.LedgerKey, tested comprehensively
// in engine/tool/ledgerkey_test.go). This pins the osfs adapter's WIRING of
// that function: RecordRead/RecordedVersion converge ordinary in-root forms
// after construction, and the lookup performs NO filesystem I/O even when the
// root pathname is later replaced by a symlink (a symlinked root would change
// any Lstat/EvalSymlinks-based key, but tool.LedgerKey is purely lexical, so
// the ledger is unaffected).
func TestLedgerKeyIsLexicalAndIOFree(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "root")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatalf("Mkdir(root): %v", err)
	}
	ws, err := NewWorkspace(root)
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}
	moved := filepath.Join(base, "moved")
	if err := os.Rename(root, moved); err != nil {
		t.Fatalf("Rename(root): %v", err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, root); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}

	ctx := context.Background()
	version := tool.NewFileVersion("lexical")
	if err := testRecordRead(ctx, ws, "dir/../file.txt", version); err != nil {
		t.Fatalf("RecordRead: %v", err)
	}
	got, ok, err := testRecordedVersion(ctx, ws, filepath.Join(ws.Root(), "file.txt"))
	if err != nil {
		t.Fatalf("RecordedVersion: %v", err)
	}
	if !ok || !got.Equal(version) {
		t.Fatalf("ordinary absolute/relative ledger forms did not converge (ok=%v)", ok)
	}

	outsidePath := filepath.Join(outside, "dir", "..", "other.txt")
	if err := testRecordRead(ctx, ws, outsidePath, version); err != nil {
		t.Fatalf("RecordRead(outsidePath): %v", err)
	}
	got, ok, err = testRecordedVersion(ctx, ws, filepath.Clean(outsidePath))
	if err != nil {
		t.Fatalf("RecordedVersion(outsidePath): %v", err)
	}
	if !ok || !got.Equal(version) {
		t.Fatalf("cleaned out-of-root absolute ledger forms did not converge (ok=%v)", ok)
	}
}
