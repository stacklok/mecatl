package mcpsecretfile

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestRead(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, data []byte) string {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	fifo := filepath.Join(dir, "pipe")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, path, want string }{
		{"missing", filepath.Join(dir, "missing"), "open client secret file"},
		{"empty", write("empty", []byte(" \n")), "empty"},
		{"oversized", write("oversized", make([]byte, MaxBytes+1)), "exceeds"},
		{"fifo", fifo, "not a regular file"},
		{"directory", dir, "not a regular file"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			value, err := Read(tc.path)
			if err == nil || value != "" || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Read = %q, %v; want %q", value, err, tc.want)
			}
		})
	}
	path := write("valid", []byte("  fixture-secret\n"))
	value, err := Read(path)
	if err != nil || value != "fixture-secret" {
		t.Fatalf("Read = %q, %v", value, err)
	}
	if _, err := Read(filepath.Join(dir, "missing")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing file error does not wrap os.ErrNotExist: %v", err)
	}
}
