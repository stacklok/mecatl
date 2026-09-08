package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/osfs"
)

// escapeworkspace_namespace_test.go pins the ADR-0314 namespace-operation
// wrapping in escapeWorkspace (internal/app/escapepolicy.go): ReadDir, Remove,
// Rename, and CopyFile must delegate for genuine in-root paths, and must
// reject an ordinary out-of-root path and a pseudo-filesystem path (/proc) on
// every operand. Every negative assertion also confirms the rejection is not
// ErrFileOperationUnsupported, since the underlying relaxed osfs.Workspace
// does implement tool.WorkspaceNamespace (proven by the in-root positive
// case) — a bare "unsupported" would itself be a bug the test must not paper
// over.
//
// MUTATION-VERIFIED (planted: deleted escapeWorkspace's refuseNamespacePath
// call from all four methods, confirmed red, reverted): the relaxed base
// workspace independently refuses ReadDir/Remove/Rename outside its root
// regardless of the wrapper, so the wrapper's guard is defense-in-depth
// there. CopyFile is the load-bearing case (see its RejectsOutOfRootSource
// doc comment below) — its two out-of-root sub-tests are the only ones that
// actually went red under the plant.

// newNamespaceTestWorkspace builds the SAME shape osfsWorkspaceFactory builds
// for the main session: a posture-relaxed *osfs.Workspace (WithRelaxedReads +
// WithRelaxedWrites) wrapped by escapeWorkspace. The relaxed base can itself
// serve an out-of-root path — the escape decision lives in the wrapper, not
// the content backend — so a non-relaxed base would make the negative tests
// below pass for the wrong reason.
func newNamespaceTestWorkspace(t *testing.T) (ws *escapeWorkspace, root string) {
	t.Helper()
	root = t.TempDir()
	clf, err := newEscapeClassifier(root)
	if err != nil {
		t.Fatalf("newEscapeClassifier: %v", err)
	}
	base, err := osfs.NewWorkspace(root, osfs.WithRelaxedReads(), osfs.WithRelaxedWrites())
	if err != nil {
		t.Fatalf("osfs.NewWorkspace: %v", err)
	}
	return newEscapeWorkspace(base, clf).(*escapeWorkspace), root
}

func TestADR_0314_EscapeWorkspace_ReadDir_InRootDelegates(t *testing.T) {
	ws, root := newNamespaceTestWorkspace(t)
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	entries, err := ws.ReadDir(context.Background(), ".")
	if err != nil {
		t.Fatalf("ReadDir(in-root) = %v, want success", err)
	}
	if len(entries) != 1 || entries[0].Name != "a.txt" {
		t.Fatalf("ReadDir(in-root) entries = %+v, want [a.txt]", entries)
	}
}

func TestADR_0314_EscapeWorkspace_ReadDir_RejectsOutOfRoot(t *testing.T) {
	ws, root := newNamespaceTestWorkspace(t)
	outside := filepath.Join(filepath.Dir(root), "outside")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatalf("MkdirAll(outside): %v", err)
	}
	_, err := ws.ReadDir(context.Background(), outside)
	if err == nil {
		t.Fatal("ReadDir(out-of-root) = nil error, want confinement refusal")
	}
	if errors.Is(err, tool.ErrFileOperationUnsupported) {
		t.Fatalf("ReadDir(out-of-root) = %v; rejection must come from the wrapper's confinement check, not ErrFileOperationUnsupported", err)
	}
	if !errors.Is(err, osfs.ErrPathEscape) {
		t.Fatalf("ReadDir(out-of-root) = %v, want errors.Is(_, osfs.ErrPathEscape)", err)
	}
}

func TestADR_0314_EscapeWorkspace_ReadDir_RejectsPseudoFS(t *testing.T) {
	ws, _ := newNamespaceTestWorkspace(t)
	_, err := ws.ReadDir(context.Background(), "/proc")
	if err == nil {
		t.Fatal("ReadDir(/proc) = nil error, want pseudo-fs refusal")
	}
	if errors.Is(err, tool.ErrFileOperationUnsupported) {
		t.Fatalf("ReadDir(/proc) = %v; rejection must come from the wrapper's pseudo-fs check, not ErrFileOperationUnsupported", err)
	}
}

func TestADR_0314_EscapeWorkspace_Remove_InRootDelegates(t *testing.T) {
	ws, root := newNamespaceTestWorkspace(t)
	target := filepath.Join(root, "gone.txt")
	if err := os.WriteFile(target, []byte("x"), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	if err := ws.Remove(context.Background(), "gone.txt"); err != nil {
		t.Fatalf("Remove(in-root) = %v, want success", err)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("file still exists after Remove: stat err = %v", err)
	}
}

func TestADR_0314_EscapeWorkspace_Remove_RejectsOutOfRoot(t *testing.T) {
	ws, root := newNamespaceTestWorkspace(t)
	outside := filepath.Join(filepath.Dir(root), "outside-remove.txt")
	if err := os.WriteFile(outside, []byte("keep-me"), 0o644); err != nil {
		t.Fatalf("seed outside file: %v", err)
	}
	err := ws.Remove(context.Background(), outside)
	if err == nil {
		t.Fatal("Remove(out-of-root) = nil error, want confinement refusal")
	}
	if errors.Is(err, tool.ErrFileOperationUnsupported) {
		t.Fatalf("Remove(out-of-root) = %v; rejection must come from the wrapper, not ErrFileOperationUnsupported", err)
	}
	if _, statErr := os.Stat(outside); statErr != nil {
		t.Fatalf("outside file was removed despite refusal: stat err = %v", statErr)
	}
}

func TestADR_0314_EscapeWorkspace_Remove_RejectsPseudoFS(t *testing.T) {
	ws, _ := newNamespaceTestWorkspace(t)
	err := ws.Remove(context.Background(), "/proc/version")
	if err == nil {
		t.Fatal("Remove(/proc/version) = nil error, want pseudo-fs refusal")
	}
	if errors.Is(err, tool.ErrFileOperationUnsupported) {
		t.Fatalf("Remove(/proc/version) = %v; rejection must come from the wrapper's pseudo-fs check, not ErrFileOperationUnsupported", err)
	}
}

func TestADR_0314_EscapeWorkspace_Rename_InRootDelegates(t *testing.T) {
	ws, root := newNamespaceTestWorkspace(t)
	if err := os.WriteFile(filepath.Join(root, "old.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	if err := ws.Rename(context.Background(), "old.txt", "new.txt"); err != nil {
		t.Fatalf("Rename(in-root, in-root) = %v, want success", err)
	}
	if _, err := os.Stat(filepath.Join(root, "new.txt")); err != nil {
		t.Fatalf("renamed file missing: %v", err)
	}
}

func TestADR_0314_EscapeWorkspace_Rename_RejectsOutOfRootOldPath(t *testing.T) {
	ws, root := newNamespaceTestWorkspace(t)
	outside := filepath.Join(filepath.Dir(root), "outside-old.txt")
	if err := os.WriteFile(outside, []byte("keep-me"), 0o644); err != nil {
		t.Fatalf("seed outside file: %v", err)
	}
	err := ws.Rename(context.Background(), outside, filepath.Join(root, "new.txt"))
	if err == nil {
		t.Fatal("Rename(out-of-root old, in-root new) = nil error, want confinement refusal")
	}
	if errors.Is(err, tool.ErrFileOperationUnsupported) {
		t.Fatalf("Rename(out-of-root old) = %v; rejection must come from the wrapper, not ErrFileOperationUnsupported", err)
	}
	if _, statErr := os.Stat(outside); statErr != nil {
		t.Fatalf("outside old file disturbed despite refusal: stat err = %v", statErr)
	}
}

// TestADR_0314_EscapeWorkspace_Rename_RejectsOutOfRootNewPath additionally pins
// that the NEW-path operand is checked independently of the old path.
func TestADR_0314_EscapeWorkspace_Rename_RejectsOutOfRootNewPath(t *testing.T) {
	ws, root := newNamespaceTestWorkspace(t)
	if err := os.WriteFile(filepath.Join(root, "old2.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	outsideDest := filepath.Join(filepath.Dir(root), "outside-new.txt")
	err := ws.Rename(context.Background(), "old2.txt", outsideDest)
	if err == nil {
		t.Fatal("Rename(in-root old, out-of-root new) = nil error, want confinement refusal")
	}
	if errors.Is(err, tool.ErrFileOperationUnsupported) {
		t.Fatalf("Rename(out-of-root new) = %v; rejection must come from the wrapper, not ErrFileOperationUnsupported", err)
	}
	if _, statErr := os.Stat(outsideDest); !os.IsNotExist(statErr) {
		t.Fatalf("outside destination was created despite refusal: stat err = %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(root, "old2.txt")); statErr != nil {
		t.Fatalf("in-root source disturbed despite refusal: stat err = %v", statErr)
	}
}

func TestADR_0314_EscapeWorkspace_Rename_RejectsPseudoFS(t *testing.T) {
	ws, root := newNamespaceTestWorkspace(t)
	if err := os.WriteFile(filepath.Join(root, "src.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	if err := ws.Rename(context.Background(), "/proc/version", "dst.txt"); err == nil {
		t.Fatal("Rename(/proc/version -> dst.txt) = nil error, want pseudo-fs refusal on old path")
	}
	if err := ws.Rename(context.Background(), "src.txt", "/proc/evil"); err == nil {
		t.Fatal("Rename(src.txt -> /proc/evil) = nil error, want pseudo-fs refusal on new path")
	}
}

func TestADR_0314_EscapeWorkspace_CopyFile_InRootDelegates(t *testing.T) {
	ws, root := newNamespaceTestWorkspace(t)
	if err := os.WriteFile(filepath.Join(root, "src.txt"), []byte("payload"), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	if _, err := ws.CopyFile(context.Background(), "src.txt", "dst.txt"); err != nil {
		t.Fatalf("CopyFile(in-root, in-root) = %v, want success", err)
	}
	got, err := os.ReadFile(filepath.Join(root, "dst.txt"))
	if err != nil || string(got) != "payload" {
		t.Fatalf("copied file = (%q, %v), want (\"payload\", nil)", got, err)
	}
}

// TestADR_0314_EscapeWorkspace_CopyFile_RejectsOutOfRootSource pins the
// load-bearing case: CopyFile is implemented via ReadVersion+CreateFile, both
// relaxed-serving, so ONLY the wrapper's confinement check stands between an
// out-of-root source and exfiltration into the workspace.
func TestADR_0314_EscapeWorkspace_CopyFile_RejectsOutOfRootSource(t *testing.T) {
	ws, root := newNamespaceTestWorkspace(t)
	outsideSrc := filepath.Join(filepath.Dir(root), "outside-src.txt")
	if err := os.WriteFile(outsideSrc, []byte("secret"), 0o644); err != nil {
		t.Fatalf("seed outside file: %v", err)
	}
	_, err := ws.CopyFile(context.Background(), outsideSrc, filepath.Join(root, "leaked.txt"))
	if err == nil {
		t.Fatal("CopyFile(out-of-root source, in-root dest) = nil error, want confinement refusal")
	}
	if errors.Is(err, tool.ErrFileOperationUnsupported) {
		t.Fatalf("CopyFile(out-of-root source) = %v; rejection must come from the wrapper, not ErrFileOperationUnsupported", err)
	}
	if _, statErr := os.Stat(filepath.Join(root, "leaked.txt")); !os.IsNotExist(statErr) {
		t.Fatalf("destination was created despite refusal: stat err = %v", statErr)
	}
}

// TestADR_0314_EscapeWorkspace_CopyFile_RejectsOutOfRootDestination is the
// symmetric case: the destination write must be independently confined too.
func TestADR_0314_EscapeWorkspace_CopyFile_RejectsOutOfRootDestination(t *testing.T) {
	ws, root := newNamespaceTestWorkspace(t)
	if err := os.WriteFile(filepath.Join(root, "in.txt"), []byte("payload"), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	outsideDst := filepath.Join(filepath.Dir(root), "outside-dst.txt")
	_, err := ws.CopyFile(context.Background(), "in.txt", outsideDst)
	if err == nil {
		t.Fatal("CopyFile(in-root source, out-of-root dest) = nil error, want confinement refusal")
	}
	if errors.Is(err, tool.ErrFileOperationUnsupported) {
		t.Fatalf("CopyFile(out-of-root destination) = %v; rejection must come from the wrapper, not ErrFileOperationUnsupported", err)
	}
	if _, statErr := os.Stat(outsideDst); !os.IsNotExist(statErr) {
		t.Fatalf("outside destination was created despite refusal: stat err = %v", statErr)
	}
}

func TestADR_0314_EscapeWorkspace_CopyFile_RejectsPseudoFS(t *testing.T) {
	ws, root := newNamespaceTestWorkspace(t)
	if err := os.WriteFile(filepath.Join(root, "src2.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	if _, err := ws.CopyFile(context.Background(), "/proc/version", "dst2.txt"); err == nil {
		t.Fatal("CopyFile(/proc/version -> dst2.txt) = nil error, want pseudo-fs refusal on source")
	}
	if _, err := ws.CopyFile(context.Background(), "src2.txt", "/proc/evil"); err == nil {
		t.Fatal("CopyFile(src2.txt -> /proc/evil) = nil error, want pseudo-fs refusal on destination")
	}
}
