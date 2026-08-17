//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly

package credentialstore

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestEncryptedFileCreationSyncsContainingDirectories(t *testing.T) {
	root := filepath.Join(t.TempDir(), "parent", "root")
	var rootSyncs int
	if err := ensurePrivateRoot(root, func(*os.File) error {
		rootSyncs++
		return nil
	}); err != nil {
		t.Fatalf("ensurePrivateRoot: %v", err)
	}
	if rootSyncs != 2 {
		t.Fatalf("root containing-directory syncs = %d, want 2", rootSyncs)
	}

	rootHandle, err := os.OpenRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	defer rootHandle.Close()
	var namespaceSyncs int
	if err := ensurePrivateDir(rootHandle, "namespace", func(*os.File) error {
		namespaceSyncs++
		return nil
	}); err != nil {
		t.Fatalf("ensurePrivateDir: %v", err)
	}
	if namespaceSyncs != 1 {
		t.Fatalf("namespace containing-directory syncs = %d, want 1", namespaceSyncs)
	}
}

func TestEncryptedFileCreationSyncFailureIsExplicit(t *testing.T) {
	root := filepath.Join(t.TempDir(), "root")
	injected := errors.New("injected sync failure")
	if err := ensurePrivateRoot(root, func(*os.File) error { return injected }); !errors.Is(err, ErrUnavailable) || !errors.Is(err, injected) {
		t.Fatalf("ensurePrivateRoot sync failure = %v", err)
	}
	rootHandle, err := os.OpenRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	defer rootHandle.Close()
	if err := ensurePrivateDir(rootHandle, "namespace", func(*os.File) error { return injected }); !errors.Is(err, ErrUnavailable) || !errors.Is(err, injected) {
		t.Fatalf("ensurePrivateDir sync failure = %v", err)
	}
}

func TestEncryptedFileRejectsInvalidInputsWithoutSideEffects(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "must-not-exist")
	for name, test := range map[string]struct {
		namespace string
		key       []byte
	}{
		"namespace": {namespace: "", key: make([]byte, 32)},
		"short key": {namespace: "valid", key: make([]byte, 31)},
		"long key":  {namespace: "valid", key: make([]byte, 33)},
	} {
		if _, err := NewEncryptedFile(root, test.namespace, test.key); err == nil {
			t.Errorf("%s unexpectedly succeeded", name)
		}
		if _, err := os.Lstat(root); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s created root: %v", name, err)
		}
	}
	if _, err := NewEncryptedFile("relative", "valid", make([]byte, 32)); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("relative root = %v", err)
	}
}

func TestEncryptedFileRejectsUnsafeRootAndNamespace(t *testing.T) {
	key := bytes.Repeat([]byte{1}, 32)
	t.Run("root mode", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "root")
		if err := os.Mkdir(root, 0o755); err != nil {
			t.Fatal(err)
		}
		if _, err := NewEncryptedFile(root, "namespace", key); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("unsafe root = %v", err)
		}
	})
	t.Run("root symlink", func(t *testing.T) {
		parent := t.TempDir()
		realPath := filepath.Join(parent, "real")
		if err := os.Mkdir(realPath, 0o700); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(parent, "link")
		if err := os.Symlink(realPath, link); err != nil {
			t.Fatal(err)
		}
		if _, err := NewEncryptedFile(link, "namespace", key); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("symlink root = %v", err)
		}
	})
	t.Run("symlinked ancestor", func(t *testing.T) {
		parent := t.TempDir()
		realPath := filepath.Join(parent, "real")
		if err := os.Mkdir(realPath, 0o700); err != nil {
			t.Fatal(err)
		}
		ancestor := filepath.Join(parent, "ancestor")
		if err := os.Symlink(realPath, ancestor); err != nil {
			t.Fatal(err)
		}
		store := openFileStore(t, filepath.Join(ancestor, "root"), "namespace", key)
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("namespace mode", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "root")
		store := openFileStore(t, root, "namespace", key)
		nsPath := store.nsPath
		_ = store.Close()
		if err := os.Chmod(nsPath, 0o755); err != nil {
			t.Fatal(err)
		}
		if _, err := NewEncryptedFile(root, "namespace", key); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("unsafe namespace = %v", err)
		}
	})
}

func TestEncryptedFileRejectsUnsafeRecordAndLockTargets(t *testing.T) {
	for _, target := range []string{"data", "lock"} {
		for _, kind := range []string{"symlink", "hardlink", "mode", "directory"} {
			t.Run(target+"/"+kind, func(t *testing.T) {
				store := openFileStore(t, filepath.Join(t.TempDir(), "root"), "namespace", bytes.Repeat([]byte{2}, 32))
				key := []byte("key")
				record, err := store.Put(context.Background(), key, []byte("value"), nil)
				if err != nil {
					t.Fatal(err)
				}
				names := store.recordNames(key)
				name := names.data
				if target == "lock" {
					name = names.lock
				}
				path := filepath.Join(store.nsPath, name)
				switch kind {
				case "symlink":
					if err := os.Remove(path); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink(names.data, path); err != nil {
						t.Fatal(err)
					}
				case "hardlink":
					if err := os.Link(path, path+".other"); err != nil {
						t.Fatal(err)
					}
				case "mode":
					if err := os.Chmod(path, 0o640); err != nil {
						t.Fatal(err)
					}
				case "directory":
					if err := os.Remove(path); err != nil {
						t.Fatal(err)
					}
					if err := os.Mkdir(path, 0o700); err != nil {
						t.Fatal(err)
					}
				}
				if _, err := store.Get(context.Background(), key); !errors.Is(err, ErrUnavailable) {
					t.Fatalf("Get unsafe target = %v", err)
				}
				if target == "data" {
					if _, err := store.Put(context.Background(), key, []byte("new"), &record.Version); !errors.Is(err, ErrUnavailable) {
						t.Fatalf("Put unsafe data = %v", err)
					}
				}
			})
		}
	}
}

func TestEncryptedFileRejectsReplacedRootPathDuringLock(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "root")
	key := bytes.Repeat([]byte{4}, 32)
	original := openFileStore(t, root, "namespace", key)
	if _, err := original.Put(context.Background(), []byte("key"), []byte("original"), nil); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(root, filepath.Join(parent, "moved")); err != nil {
		t.Fatal(err)
	}
	replacement := openFileStore(t, root, "namespace", key)
	if _, err := replacement.Put(context.Background(), []byte("key"), []byte("replacement"), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := original.Get(context.Background(), []byte("key")); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Get through replaced absolute lock path = %v", err)
	}
}

func TestEncryptedFileLocationBoundAAD(t *testing.T) {
	store := openFileStore(t, filepath.Join(t.TempDir(), "root"), "namespace", bytes.Repeat([]byte{3}, 32))
	ctx := context.Background()
	if _, err := store.Put(ctx, []byte("one"), []byte("secret"), nil); err != nil {
		t.Fatal(err)
	}
	one := filepath.Join(store.nsPath, store.recordNames([]byte("one")).data)
	two := filepath.Join(store.nsPath, store.recordNames([]byte("two")).data)
	data, err := os.ReadFile(one)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(two, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(ctx, []byte("two")); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("copied envelope = %v", err)
	}
}
