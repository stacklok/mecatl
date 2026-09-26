package microvm

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func testUnixSocketPath(t *testing.T) string {
	t.Helper()
	return testUnixSocketPathForOS(t, runtime.GOOS)
}

func testUnixSocketPathForOS(t *testing.T, _ string) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "mecatl-microvm-")
	if err != nil {
		t.Fatalf("create short private Darwin socket directory: %v", err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(dir); err != nil {
			t.Errorf("remove short private Darwin socket directory: %v", err)
		}
	})
	return filepath.Join(dir, "microvmd.sock")
}

func TestInvariant_microvm_darwin_test_socket_path_is_short_and_private(t *testing.T) {
	path := testUnixSocketPathForOS(t, "darwin")
	if len(path) >= 104 {
		t.Fatalf("Darwin test socket path is %d bytes, want < 104: %q", len(path), path)
	}
	if !strings.HasPrefix(path, filepath.Clean("/tmp")+string(filepath.Separator)) {
		t.Fatalf("Darwin test socket escaped private short base: %q", path)
	}
	info, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatalf("stat Darwin test socket directory: %v", err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("Darwin test socket directory mode = %o, want owner-only", info.Mode().Perm())
	}
	resolved, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil {
		t.Fatalf("resolve Darwin test socket directory: %v", err)
	}
	if !strings.HasPrefix(resolved, filepath.Clean("/private/tmp")+string(filepath.Separator)) &&
		!strings.HasPrefix(resolved, filepath.Clean("/tmp")+string(filepath.Separator)) {
		t.Fatalf("Darwin test socket resolved outside short temporary base: %q", resolved)
	}
}
