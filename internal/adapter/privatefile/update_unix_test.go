//go:build linux || darwin

package privatefile

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUpdateProtocol(t *testing.T) {
	mutate := func(data []byte) ([]byte, bool, error) {
		return append(bytes.Clone(data), []byte("new\n")...), false, nil
	}
	newTarget := func(t *testing.T) (string, string) {
		t.Helper()
		dir := t.TempDir()
		if err := os.Chmod(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, "settings.yaml")
		if err := os.WriteFile(path, []byte("old\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		return dir, path
	}

	t.Run("durable atomic replacement", func(t *testing.T) {
		_, path := newTarget(t)
		state, err := Update(t.Context(), path, "", 1024, mutate)
		if err != nil || state != CommitDurable {
			t.Fatalf("Update = (%v, %v)", state, err)
		}
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("target mode = %v, %v", info.Mode(), err)
		}
	})

	t.Run("changed target", func(t *testing.T) {
		_, path := newTarget(t)
		testHook = func(stage string) error {
			if stage == "before-compare" {
				return os.WriteFile(path, []byte("changed\n"), 0o600)
			}
			return nil
		}
		t.Cleanup(func() { testHook = nil })
		state, err := Update(t.Context(), path, "", 1024, mutate)
		if state != CommitNotApplied || err != ErrConfigurationChanged {
			t.Fatalf("Update = (%v, %v), want changed target", state, err)
		}
		data, _ := os.ReadFile(path)
		if string(data) != "changed\n" {
			t.Fatalf("changed target overwritten: %q", data)
		}
	})

	t.Run("cancellation removes temporary", func(t *testing.T) {
		dir, path := newTarget(t)
		ctx, cancel := context.WithCancel(t.Context())
		testHook = func(stage string) error {
			if stage == "after-temp-sync" {
				cancel()
			}
			return nil
		}
		t.Cleanup(func() { testHook = nil })
		state, err := Update(ctx, path, "", 1024, mutate)
		if state != CommitNotApplied || !errors.Is(err, context.Canceled) {
			t.Fatalf("Update = (%v, %v), want cancellation", state, err)
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			if strings.HasSuffix(entry.Name(), ".tmp") {
				t.Fatalf("temporary remains: %s", entry.Name())
			}
		}
	})

	t.Run("post-rename failure is uncertain", func(t *testing.T) {
		_, path := newTarget(t)
		testHook = func(stage string) error {
			if stage == "after-rename" {
				return errors.New("injected")
			}
			return nil
		}
		t.Cleanup(func() { testHook = nil })
		state, err := Update(t.Context(), path, "", 1024, mutate)
		if state != CommitReplacementAppliedDurabilityUnknown || err == nil {
			t.Fatalf("Update = (%v, %v), want durability unknown", state, err)
		}
		data, _ := os.ReadFile(path)
		if string(data) != "old\nnew\n" {
			t.Fatalf("replacement not applied: %q", data)
		}
	})
}

func TestUpdateRejectsUnsafeTargetAndParent(t *testing.T) {
	t.Run("public parent", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.Chmod(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		state, err := Update(t.Context(), filepath.Join(dir, "x"), "", 10, func(_ []byte) ([]byte, bool, error) { return []byte("x"), false, nil })
		if state != CommitNotApplied || err == nil {
			t.Fatalf("Update = (%v, %v)", state, err)
		}
	})
	t.Run("symlink target", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.Chmod(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(dir, "real")
		if err := os.WriteFile(target, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(dir, "link")
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		state, err := Update(t.Context(), link, "", 10, func(_ []byte) ([]byte, bool, error) { return []byte("x"), false, nil })
		if state != CommitNotApplied || err == nil {
			t.Fatalf("Update = (%v, %v)", state, err)
		}
	})
}
