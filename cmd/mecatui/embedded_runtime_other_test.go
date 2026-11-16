//go:build !linux

package main

import (
	"os"
	"testing"
)

// A per-user TMPDIR overflows sun_path, which would move the socket out of this dir.
func shortEmbeddedRuntimeDir(t *testing.T) {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "mrt-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	t.Setenv("XDG_RUNTIME_DIR", dir)
}
