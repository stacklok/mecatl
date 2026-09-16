package mcpcredential

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

type fakeKeyring struct{ values map[string]string }

func (f *fakeKeyring) Get(service, account string) (string, error) {
	v, ok := f.values[service+"\x00"+account]
	if !ok {
		return "", errors.New("missing")
	}
	return v, nil
}
func (f *fakeKeyring) Set(service, account, value string) error {
	f.values[service+"\x00"+account] = value
	return nil
}

func TestFileCustodyPinsLocatorAndPermissions(t *testing.T) {
	root := filepath.Join(t.TempDir(), "credentials")
	keyPath := filepath.Join(t.TempDir(), "key")
	first, err := Resolve(context.Background(), root, Options{Requested: BackendFile, FilePath: keyPath, Platform: "linux"})
	if err != nil {
		t.Fatal(err)
	}
	defer clear(first.Key)
	if first.Locator != mustEvalSymlinks(t, keyPath) || len(first.Key) != 32 {
		t.Fatalf("selection = %#v", first)
	}
	info, err := os.Stat(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("key mode = %o", info.Mode().Perm())
	}
	second, err := Open(context.Background(), root, BackendFile, keyPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(second.Key)
	if string(first.Key) != string(second.Key) {
		t.Fatal("file key changed after reopen")
	}
}

func TestKeyringCustodyUsesRootDerivedAccountAndReopens(t *testing.T) {
	kr := &fakeKeyring{values: map[string]string{}}
	root := filepath.Join(t.TempDir(), "credentials")
	first, err := Resolve(context.Background(), root, Options{Requested: BackendKeyring, Platform: "darwin", Keyring: kr})
	if err != nil {
		t.Fatal(err)
	}
	defer clear(first.Key)
	second, err := Open(context.Background(), root, BackendKeyring, "", kr)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(second.Key)
	if first.Locator != second.Locator || string(first.Key) != string(second.Key) {
		t.Fatal("keyring selection did not reopen")
	}
	if len(first.Locator) != 64 {
		t.Fatalf("locator = %q", first.Locator)
	}
}

func mustEvalSymlinks(t *testing.T, path string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

func TestFileKeyRejectsSymlinkModeAndHardlink(t *testing.T) {
	root := filepath.Join(t.TempDir(), "credentials")
	keyPath := filepath.Join(t.TempDir(), "key")
	selection, err := Resolve(context.Background(), root, Options{Requested: BackendFile, FilePath: keyPath})
	if err != nil {
		t.Fatal(err)
	}
	clear(selection.Key)
	for _, mutate := range []func(t *testing.T, path string){
		func(t *testing.T, path string) {
			if err := os.Chmod(path, 0o644); err != nil {
				t.Fatal(err)
			}
		},
		func(t *testing.T, path string) {
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(t.TempDir(), "other"), path); err != nil {
				t.Fatal(err)
			}
		},
	} {
		if err := os.RemoveAll(keyPath); err != nil {
			t.Fatal(err)
		}
		if _, err := Resolve(context.Background(), root, Options{Requested: BackendFile, FilePath: keyPath}); err != nil {
			t.Fatal(err)
		}
		mutate(t, keyPath)
		if _, err := Open(context.Background(), root, BackendFile, keyPath, nil); err == nil {
			t.Fatal("Open accepted unsafe key")
		}
	}
	if err := os.RemoveAll(keyPath); err != nil {
		t.Fatal(err)
	}
	if _, err := Resolve(context.Background(), root, Options{Requested: BackendFile, FilePath: keyPath}); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(keyPath, keyPath+".link"); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(context.Background(), root, BackendFile, keyPath, nil); err == nil {
		t.Fatal("Open accepted multiply linked key")
	}
}

func TestResolveRejectsSymlinkedRoot(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(base, "root")
	if err := os.Symlink(target, root); err != nil {
		t.Fatal(err)
	}
	if _, err := Resolve(context.Background(), root, Options{Requested: BackendKeyring, Keyring: &fakeKeyring{values: map[string]string{}}}); err == nil {
		t.Fatal("Resolve accepted symlinked root")
	}
}

func TestOpenRejectsSymlinkedMarker(t *testing.T) {
	root := filepath.Join(t.TempDir(), "credentials")
	keyPath := filepath.Join(t.TempDir(), "key")
	selection, err := Resolve(context.Background(), root, Options{Requested: BackendFile, FilePath: keyPath})
	if err != nil {
		t.Fatal(err)
	}
	clear(selection.Key)
	marker := filepath.Join(mustEvalSymlinks(t, root), markerName)
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(keyPath, marker); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(context.Background(), root, BackendFile, keyPath, nil); err == nil {
		t.Fatal("Open accepted symlinked marker")
	}
}
