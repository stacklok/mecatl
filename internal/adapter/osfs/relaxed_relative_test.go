package osfs

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestRelaxedRelativeEscapes(t *testing.T) {
	base := t.TempDir()
	root, outside := filepath.Join(base, "workspace"), filepath.Join(base, "outside")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "existing.txt"), []byte("before"), 0o644); err != nil {
		t.Fatal(err)
	}
	plain, err := NewWorkspace(root, WithRelaxedReads(), WithRelaxedWrites())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := plain.Read(t.Context(), "../outside/existing.txt"); !errors.Is(err, ErrPathEscape) {
		t.Fatalf("plain relative read: %v", err)
	}
	if _, err := plain.CreateFile(t.Context(), "../outside/new.txt", []byte("no")); !errors.Is(err, ErrPathEscape) {
		t.Fatalf("plain relative create: %v", err)
	}
	ws, err := NewWorkspace(root, WithRelaxedReads(), WithRelaxedWrites(), WithRelaxedRelativeEscapes())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "inside.txt"), []byte("inside"), 0o644); err != nil {
		t.Fatal(err)
	}
	if data, err := ws.Read(t.Context(), "../workspace/inside.txt"); err != nil || string(data) != "inside" {
		t.Fatalf("re-entering relative Read = %q, %v", data, err)
	}
	if _, version, err := ws.ReadVersion(t.Context(), "../workspace/inside.txt"); err != nil {
		t.Fatalf("re-entering relative ReadVersion: %v", err)
	} else if _, err := ws.ReplaceFile(t.Context(), filepath.Join(root, "inside.txt"), version, []byte("updated")); err != nil {
		t.Fatalf("re-entering relative then absolute ReplaceFile: %v", err)
	}
	for _, path := range []string{"../outside/existing.txt", "./../outside/existing.txt"} {
		data, err := ws.Read(t.Context(), path)
		if err != nil || string(data) != "before" {
			t.Fatalf("Read(%q) = %q, %v", path, data, err)
		}
	}
	var opened []*os.Root
	ws.fs.openReadRoot = func(path string) (*os.Root, error) {
		r, err := os.OpenRoot(path)
		if err == nil {
			opened = append(opened, r)
		}
		return r, err
	}
	if data, _, err := ws.ReadVersionBounded(t.Context(), "../outside/existing.txt", 6); err != nil || string(data) != "before" {
		t.Fatalf("bounded read = %q, %v", data, err)
	}
	if data, _, _, err := ws.ReadVersionRangeBounded(t.Context(), "../outside/existing.txt", 1, 3, 6); err != nil || string(data) != "efo" {
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
	ws.fs.openReadRoot = nil
	if entries, err := ws.ReadDir(t.Context(), "../outside"); err != nil || len(entries) != 1 {
		t.Fatalf("ReadDir = %v, %v", entries, err)
	}
	if target, _, err := ws.RelaxedAuthorityResourcePath("../outside/existing.txt"); err != nil || target != filepath.Join(outside, "existing.txt") {
		t.Fatalf("identity = %q, %v", target, err)
	}
	if _, err := ws.CreateFile(t.Context(), "../outside/new.txt", []byte("new")); err != nil {
		t.Fatal(err)
	}
	_, version, err := ws.ReadVersion(t.Context(), "../outside/existing.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ws.ReplaceFile(t.Context(), "../outside/existing.txt", version, []byte("after")); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(filepath.Join(outside, "existing.txt")); err != nil || string(data) != "after" {
		t.Fatalf("replace = %q, %v", data, err)
	}
	if err := os.Symlink(base, filepath.Join(outside, "link")); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"../outside/link/workspace/../outside/existing.txt", "../outside/link/../existing.txt"} {
		if _, err := ws.Read(t.Context(), path); !errors.Is(err, ErrPathEscape) {
			t.Errorf("Read symlink/.. %q: %v", path, err)
		}
		if _, err := ws.CreateFile(t.Context(), path, []byte("bad")); !errors.Is(err, ErrPathEscape) {
			t.Errorf("Create symlink/.. %q: %v", path, err)
		}
	}
	if err := os.Symlink(outside, filepath.Join(root, "alias")); err != nil {
		t.Fatal(err)
	}
	if _, err := ws.Read(t.Context(), "alias/existing.txt"); !errors.Is(err, ErrPathEscape) {
		t.Fatalf("in-root symlink read: %v", err)
	}
	if _, _, err := ws.RelaxedAuthorityResourcePath("alias/existing.txt"); !errors.Is(err, ErrPathEscape) {
		t.Fatalf("in-root symlink identity: %v", err)
	}
}
