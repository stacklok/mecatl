//go:build unix

package managedtemp

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestADR_0281_ManagedObjectsCreatedAtomicallyPrivate(t *testing.T) {
	oldUmask := syscall.Umask(0)
	t.Cleanup(func() { syscall.Umask(oldUmask) })

	base := t.TempDir()
	ns, err := Open(filepath.Join(base, "mecatl"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = ns.Close() })
	workspace, err := ns.OpenWorkspace("osfs", "workspace-identity", filepath.Join(base, "workspace"))
	if err != nil {
		t.Fatalf("OpenWorkspace: %v", err)
	}
	t.Cleanup(func() { _ = workspace.Close() })
	if err := workspace.CreatePrivateFile("commands/cmd-allocation/lease.lock", nil); err != nil {
		t.Fatalf("CreatePrivateFile lock: %v", err)
	}
	if err := workspace.WritePrivateFile("commands/cmd-allocation/manifest.json", []byte(`{"version":1}`)); err != nil {
		t.Fatalf("WritePrivateFile manifest: %v", err)
	}
	if err := workspace.CreatePrivateFile("commands/cmd-allocation/tmp/write", nil); err != nil {
		t.Fatalf("CreatePrivateFile temporary write: %v", err)
	}
	if err := ns.WritePrivateFile("last-successful-sweep.json", []byte(`{"version":1}`)); err != nil {
		t.Fatalf("WritePrivateFile completion: %v", err)
	}

	for _, path := range []string{
		filepath.Join(base, "mecatl"),
		filepath.Join(base, "mecatl", "gc.lock"),
		filepath.Join(base, "mecatl", "workspaces"),
		filepath.Join(workspace.Path(), "workspace.lock"),
		filepath.Join(workspace.Path(), "workspace.manifest"),
		filepath.Join(workspace.Path(), "commands", "cmd-allocation"),
		filepath.Join(workspace.Path(), "commands", "cmd-allocation", "tmp"),
		filepath.Join(workspace.Path(), "commands", "cmd-allocation", "tmp", "write"),
		filepath.Join(workspace.Path(), "commands", "cmd-allocation", "lease.lock"),
		filepath.Join(workspace.Path(), "commands", "cmd-allocation", "manifest.json"),
		filepath.Join(base, "mecatl", "last-successful-sweep.json"),
	} {
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatalf("Lstat(%q): %v", path, err)
		}
		want := os.FileMode(0o600)
		if info.IsDir() {
			want = 0o700
		}
		if got := info.Mode().Perm(); got != want {
			t.Errorf("%s mode = %o, want %o", path, got, want)
		}
	}

	if err := os.Chmod(filepath.Join(base, "mecatl", "workspaces"), 0o755); err != nil {
		t.Fatalf("Chmod workspaces: %v", err)
	}
	if _, err := ns.OpenWorkspace("osfs", "replacement-identity", filepath.Join(base, "replacement")); err == nil {
		t.Fatal("OpenWorkspace accepted an existing permissive namespace component")
	}

	permissive := filepath.Join(base, "permissive")
	if err := os.Mkdir(permissive, 0o755); err != nil {
		t.Fatalf("Mkdir permissive: %v", err)
	}
	if _, err := Open(permissive); err == nil {
		t.Fatal("Open accepted an existing permissive managed root")
	}
}
