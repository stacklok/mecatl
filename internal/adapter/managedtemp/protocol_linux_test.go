//go:build linux

package managedtemp

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkspaceKeyUsesFirst128BitsOfRawURLBase64SHA256(t *testing.T) {
	const backend = "osfs"
	const identity = "/canonical/workspace"
	sum := sha256.Sum256([]byte("mecatl/managed-temp/workspace/v1\x00" + backend + "\x00" + identity))
	want := base64.RawURLEncoding.EncodeToString(sum[:16])
	if got := workspaceKey(backend, identity); got != want || len(got) != 22 {
		t.Fatalf("workspaceKey() = %q, want first 128 bits as raw URL base64 %q", got, want)
	}
	if !validWorkspaceKey(want) || validWorkspaceKey("######################") {
		t.Fatal("workspace key format validation accepted an invalid key")
	}
}

func TestADR_0281_ManagedRootAndWorkspaceFailClosed(t *testing.T) {
	base := t.TempDir()
	outsideRoot := filepath.Join(base, "outside-root")
	if err := os.Mkdir(outsideRoot, 0o700); err != nil {
		t.Fatalf("Mkdir outside root: %v", err)
	}
	linkedRoot := filepath.Join(base, "linked-root")
	if err := os.Symlink(outsideRoot, linkedRoot); err != nil {
		t.Fatalf("Symlink root: %v", err)
	}
	if _, err := Open(linkedRoot); err == nil {
		t.Fatal("Open accepted a symlinked managed root")
	}
	ns, err := Open(filepath.Join(base, "mecatl"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = ns.Close() })

	identity := "/private/repository"
	workspace, err := ns.OpenWorkspace("osfs", identity, identity)
	if err != nil {
		t.Fatalf("OpenWorkspace: %v", err)
	}
	t.Cleanup(func() { _ = workspace.Close() })
	if strings.Contains(workspace.Path(), identity) {
		t.Fatalf("workspace path exposes raw identity: %q", workspace.Path())
	}
	manifestPath := filepath.Join(workspace.Path(), "workspace.manifest")
	manifest, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("ReadFile workspace manifest: %v", err)
	}
	var manifestFields workspaceManifest
	if err := json.Unmarshal(manifest, &manifestFields); err != nil || manifestFields.Version != manifestVersion || manifestFields.Key != filepath.Base(workspace.Path()) || manifestFields.CurrentPath != identity {
		t.Fatalf("workspace manifest = %s; want version, key, and canonical current path", manifest)
	}
	if err := os.WriteFile(manifestPath, []byte(`{"version":1,"key":"wrong","current_path":"/private/repository"}`), 0o600); err != nil {
		t.Fatalf("WriteFile malformed workspace manifest: %v", err)
	}
	if _, err := ns.OpenWorkspace("osfs", identity, identity); err == nil {
		t.Fatal("OpenWorkspace accepted a mismatched manifest key")
	}
	if err := os.WriteFile(manifestPath, manifest, 0o600); err != nil {
		t.Fatalf("restore workspace manifest: %v", err)
	}
	if err := os.WriteFile(manifestPath, []byte(`{"version":1,"key":"`+manifestFields.Key+`"}`), 0o600); err != nil {
		t.Fatalf("WriteFile workspace manifest without current path: %v", err)
	}
	if _, err := ns.OpenWorkspace("osfs", identity, identity); err == nil {
		t.Fatal("OpenWorkspace accepted a manifest without a canonical current path")
	}
	if err := os.WriteFile(manifestPath, manifest, 0o600); err != nil {
		t.Fatalf("restore workspace manifest with current path: %v", err)
	}
	movedPath := "/private/repository-renamed"
	refreshed, err := ns.OpenWorkspace("osfs", identity, movedPath)
	if err != nil {
		t.Fatalf("refresh workspace manifest: %v", err)
	}
	if err := refreshed.Close(); err != nil {
		t.Fatalf("close refreshed workspace: %v", err)
	}
	refreshedManifest, err := os.ReadFile(manifestPath)
	if err != nil || !strings.Contains(string(refreshedManifest), `"current_path":"`+movedPath+`"`) {
		t.Fatalf("workspace manifest did not refresh the current path: %q, %v", refreshedManifest, err)
	}
	if got := filepath.Base(workspace.Path()); len(got) != 22 || strings.ContainsAny(got, "+/=") {
		t.Fatalf("workspace key = %q, want 22-char raw URL-safe base64", got)
	}

	lease, err := workspace.Allocate("cmd")
	if err != nil {
		t.Fatalf("Allocate: %v", err)
	}
	t.Cleanup(func() { _ = lease.Close() })
	if !strings.HasPrefix(filepath.Base(lease.Path()), "cmd-") || len(lease.ID()) != 22 || !validAllocationID(lease.ID()) {
		t.Fatalf("lease identity = %q / %q, want cmd- plus 22-char raw URL-safe base64 128-bit ID", lease.Path(), lease.ID())
	}

	outside := filepath.Join(base, "outside")
	if err := os.Mkdir(outside, 0o700); err != nil {
		t.Fatalf("Mkdir outside: %v", err)
	}
	if err := os.WriteFile(filepath.Join(outside, "keep"), []byte("must survive"), 0o600); err != nil {
		t.Fatalf("WriteFile outside: %v", err)
	}
	if err := os.RemoveAll(lease.Path()); err != nil {
		t.Fatalf("RemoveAll lease replacement: %v", err)
	}
	if err := os.Symlink(outside, lease.Path()); err != nil {
		t.Fatalf("Symlink lease replacement: %v", err)
	}
	if err := lease.Remove(); err == nil {
		t.Fatal("Remove accepted replaced lease")
	}
	if info, err := os.Lstat(lease.Path()); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("replaced lease was altered instead of retained: %v, %v", info, err)
	}
	if _, err := os.Stat(filepath.Join(outside, "keep")); err != nil {
		t.Fatalf("outside content was deleted: %v", err)
	}

}

// TestADR_0281_LeaseRemovalRetainsReplacedParentEntry pins the final unlink
// against a same-UID replacement after the retained lease handle was validated.
func TestADR_0281_LeaseRemovalRetainsReplacedParentEntry(t *testing.T) {
	ns, err := Open(filepath.Join(t.TempDir(), "managed"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ns.Close() })
	workspace, err := ns.OpenWorkspace("osfs", "replacement", "/workspace/replacement")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = workspace.Close() })
	lease, err := workspace.Allocate("cmd")
	if err != nil {
		t.Fatal(err)
	}
	original := lease.Path()
	replacement := original + ".old"
	if err := os.Rename(original, replacement); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(original, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := lease.Remove(); err == nil {
		t.Fatal("Remove accepted a replaced parent entry")
	}
	if info, err := os.Lstat(original); err != nil || !info.IsDir() {
		t.Fatalf("replacement was removed: %v, %v", info, err)
	}
	_ = lease.Close()
}

func TestADR_0281_UnknownManifestVersionRetained(t *testing.T) {
	base := t.TempDir()
	ns, err := Open(filepath.Join(base, "mecatl"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = ns.Close() })
	workspace, err := ns.OpenWorkspace("osfs", "workspace", "/workspace/current")
	if err != nil {
		t.Fatalf("OpenWorkspace: %v", err)
	}
	t.Cleanup(func() { _ = workspace.Close() })
	lease, err := workspace.Allocate("cmd")
	if err != nil {
		t.Fatalf("Allocate: %v", err)
	}
	t.Cleanup(func() { _ = lease.Close() })

	for _, test := range []struct {
		name    string
		version any
	}{
		{name: "missing", version: nil},
		{name: "malformed", version: "one"},
		{name: "newer", version: 2},
	} {
		t.Run("allocation_"+test.name, func(t *testing.T) {
			manifest := map[string]any{"id": lease.ID(), "kind": "cmd"}
			if test.version != nil {
				manifest["version"] = test.version
			}
			data, err := json.Marshal(manifest)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(lease.Path(), "manifest.json")
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatal(err)
			}
			before, _ := os.ReadFile(path)
			if err := lease.Remove(); err == nil {
				t.Fatal("Remove accepted unsupported manifest version")
			}
			after, err := os.ReadFile(path)
			if err != nil || string(after) != string(before) {
				t.Fatalf("unsupported manifest was changed or removed: %v, %q", err, after)
			}
		})
	}

	for _, test := range []struct {
		name    string
		version any
	}{
		{name: "missing", version: nil},
		{name: "malformed", version: "one"},
		{name: "newer", version: 2},
	} {
		t.Run("completion_"+test.name, func(t *testing.T) {
			record := map[string]any{}
			if test.version != nil {
				record["version"] = test.version
			}
			data, err := json.Marshal(record)
			if err != nil {
				t.Fatal(err)
			}
			completion := filepath.Join(ns.Path(), "last-successful-sweep.json")
			if err := os.WriteFile(completion, data, 0o600); err != nil {
				t.Fatal(err)
			}
			before, _ := os.ReadFile(completion)
			if _, err := ns.ReadSweepCompletion(); err == nil {
				t.Fatal("ReadSweepCompletion accepted unsupported completion version")
			}
			after, err := os.ReadFile(completion)
			if err != nil || string(after) != string(before) {
				t.Fatalf("unsupported completion was changed or removed: %v, %q", err, after)
			}
		})
	}
}
