package main

import "testing"

func shortEmbeddedRuntimeDir(t *testing.T) {
	t.Helper()
	t.Chdir(t.TempDir())
	t.Setenv("XDG_RUNTIME_DIR", "/proc/self/cwd")
}
