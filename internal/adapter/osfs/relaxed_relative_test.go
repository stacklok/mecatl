package osfs

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestRelaxedOnlyStillRejectsRelativeEscapes(t *testing.T) {
	base := t.TempDir()
	root, outside := filepath.Join(base, "workspace"), filepath.Join(base, "outside")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(outside, "existing.txt")
	if err := os.WriteFile(target, []byte("before"), 0o644); err != nil {
		t.Fatal(err)
	}
	ws, err := NewWorkspace(root, WithRelaxedReads(), WithRelaxedWrites())
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"../outside/existing.txt", "./../outside/existing.txt", "../workspace/inside.txt"} {
		if _, err := ws.Read(t.Context(), path); !errors.Is(err, ErrPathEscape) {
			t.Errorf("Read(%q): %v", path, err)
		}
		if _, _, err := ws.RelaxedAuthorityResourcePath(path); !errors.Is(err, ErrPathEscape) {
			t.Errorf("identity(%q): %v", path, err)
		}
	}
	if _, err := ws.CreateFile(t.Context(), "../outside/new.txt", []byte("no")); !errors.Is(err, ErrPathEscape) {
		t.Fatalf("relative create: %v", err)
	}
	if data, err := ws.Read(t.Context(), target); err != nil || string(data) != "before" {
		t.Fatalf("absolute relaxed read = %q, %v", data, err)
	}
	var opened []*os.Root
	ws.fs.openReadRoot = func(path string) (*os.Root, error) {
		r, err := os.OpenRoot(path)
		if err == nil {
			opened = append(opened, r)
		}
		return r, err
	}
	if data, _, err := ws.ReadVersionBounded(t.Context(), target, 6); err != nil || string(data) != "before" {
		t.Fatalf("bounded read = %q, %v", data, err)
	}
	if data, _, _, err := ws.ReadVersionRangeBounded(t.Context(), target, 1, 3, 6); err != nil || string(data) != "efo" {
		t.Fatalf("range read = %q, %v", data, err)
	}
	if len(opened) != 2 {
		t.Fatalf("opened roots = %d, want 2", len(opened))
	}
	for _, r := range opened {
		if _, err := r.Stat("."); !errors.Is(err, os.ErrClosed) {
			t.Errorf("bounded root left open: %v", err)
		}
	}
}
