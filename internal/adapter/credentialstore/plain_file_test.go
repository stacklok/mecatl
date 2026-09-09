//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly

package credentialstore

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestPlainFileStrictRecordsAndVersions(t *testing.T) {
	root := filepath.Join(t.TempDir(), "credentials")
	s, err := NewPlainFile(root, "plain")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	key := []byte("key")
	first, err := s.Put(t.Context(), key, []byte("fixture-value"), nil)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(s.nsPath, s.recordNames(key).data)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte("fixture-value")) {
		t.Fatal("record is not plaintext")
	}
	for _, mutate := range []func([]byte) []byte{
		func(b []byte) []byte { b[8]++; return b },
		func(b []byte) []byte { b[9]++; return b },
		func(b []byte) []byte { return b[:len(b)-1] },
		func(b []byte) []byte { return append(b, 0) },
	} {
		if _, _, err := openPlainRecord(s.namespace, key, mutate(bytes.Clone(data))); !errors.Is(err, ErrCorrupt) {
			t.Fatal("malformed record accepted")
		}
	}
	changed := bytes.Clone(data)
	changed[plainHeaderBytes] ^= 1
	_, version, err := openPlainRecord(s.namespace, key, changed)
	if err != nil || version.Equal(first.Version) {
		t.Fatal("generation not covered by version")
	}
	changed = bytes.Clone(data)
	changed[len(changed)-1] ^= 1
	_, version, err = openPlainRecord(s.namespace, key, changed)
	if err != nil || version.Equal(first.Version) {
		t.Fatal("value not covered by version")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenExistingPlainFile(root, "plain")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	got, err := reopened.Get(t.Context(), key)
	if err != nil || !got.Version.Equal(first.Version) {
		t.Fatal("existing opener lost persisted version")
	}
}

func TestHeadlessCredentialStorage_Scenario3_PrivateFilesystemBoundary(t *testing.T) {
	for _, kind := range []string{"unsafe-root", "symlink-root", "unsafe-namespace", "symlink-namespace", "hardlink-record", "symlink-record", "unsafe-record", "unsafe-lock"} {
		t.Run(kind, func(t *testing.T) {
			parent := t.TempDir()
			root := filepath.Join(parent, "store")
			if err := os.Mkdir(root, 0700); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "unsafe-root":
				if err := os.Chmod(root, 0755); err != nil {
					t.Fatal(err)
				}
			case "symlink-root":
				root = filepath.Join(parent, "alias")
				if err := os.Symlink(filepath.Join(parent, "store"), root); err != nil {
					t.Fatal(err)
				}
			case "unsafe-namespace":
				if err := os.Mkdir(filepath.Join(root, plainDirectory), 0755); err != nil {
					t.Fatal(err)
				}
			case "symlink-namespace":
				if err := os.Symlink(parent, filepath.Join(root, plainDirectory)); err != nil {
					t.Fatal(err)
				}
			}
			s, err := NewPlainFile(root, "plain")
			switch kind {
			case "unsafe-root", "symlink-root", "unsafe-namespace", "symlink-namespace":
				if err == nil {
					_ = s.Close()
					t.Fatal("unsafe boundary accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = s.Close() }()
			key := []byte("key")
			rec, err := s.Put(t.Context(), key, []byte("fixture"), nil)
			if err != nil {
				t.Fatal(err)
			}
			name := filepath.Join(s.nsPath, s.recordNames(key).data)
			switch kind {
			case "hardlink-record":
				err = os.Link(name, filepath.Join(parent, "link"))
			case "symlink-record":
				old := filepath.Join(parent, "old")
				if err = os.Rename(name, old); err == nil {
					err = os.Symlink(old, name)
				}
			case "unsafe-record":
				err = os.Chmod(name, 0644)
			case "unsafe-lock":
				err = os.Chmod(filepath.Join(s.nsPath, s.recordNames(key).lock), 0644)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.Get(t.Context(), key); !errors.Is(err, ErrUnavailable) {
				t.Fatal("unsafe read accepted")
			}
			if _, err := s.Put(t.Context(), key, []byte("replacement"), &rec.Version); !errors.Is(err, ErrUnavailable) {
				t.Fatal("unsafe replacement accepted")
			}
			if err := s.Delete(t.Context(), key, rec.Version); !errors.Is(err, ErrUnavailable) {
				t.Fatal("unsafe delete accepted")
			}
		})
	}
}

func TestPlainFileExistingDoesNotCreate(t *testing.T) {
	root := filepath.Join(t.TempDir(), "missing")
	if s, err := OpenExistingPlainFile(root, "plain"); err == nil {
		_ = s.Close()
		t.Fatal("missing root accepted")
	}
	if _, err := os.Lstat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("existing opener created root")
	}
}
