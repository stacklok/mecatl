package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const darwinUnixSocketPathBytes = 104

func shortPrivateMicrovmdSocketPath(t *testing.T, name string) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "mecatl-microvmd-test-")
	if err != nil {
		t.Fatalf("create short private microvmd test directory: %v", err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(dir); err != nil {
			t.Errorf("remove short private microvmd test directory: %v", err)
		}
	})
	return filepath.Join(dir, name)
}

func TestInvariant_microvmd_test_socket_path_is_darwin_safe_private_and_cleaned(t *testing.T) {
	var dir string
	t.Run("allocate", func(t *testing.T) {
		path := shortPrivateMicrovmdSocketPath(t, "microvmd.sock")
		dir = filepath.Dir(path)
		if len(path) >= darwinUnixSocketPathBytes {
			t.Fatalf("microvmd test socket path is %d bytes, want < %d: %q", len(path), darwinUnixSocketPathBytes, path)
		}
		if !strings.HasPrefix(path, filepath.Clean("/tmp")+string(filepath.Separator)) {
			t.Fatalf("microvmd test socket escaped short temporary base: %q", path)
		}
		info, err := os.Stat(dir)
		if err != nil {
			t.Fatalf("stat microvmd test socket directory: %v", err)
		}
		if info.Mode().Perm()&0o077 != 0 {
			t.Fatalf("microvmd test socket directory mode = %o, want owner-only", info.Mode().Perm())
		}
		if err := os.WriteFile(filepath.Join(dir, "cleanup-marker"), []byte("owned"), 0o600); err != nil {
			t.Fatalf("create cleanup marker: %v", err)
		}
	})
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("microvmd test socket directory survived cleanup: %v", err)
	}
}
