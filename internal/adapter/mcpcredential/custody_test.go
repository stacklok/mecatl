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

type failingKeyring struct{}

func (failingKeyring) Get(string, string) (string, error) { return "", errors.New("unavailable") }
func (failingKeyring) Set(string, string, string) error   { return errors.New("unavailable") }

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
		root := filepath.Join(t.TempDir(), "credentials")
		keyPath := filepath.Join(t.TempDir(), "key")
		selection, err := Resolve(context.Background(), root, Options{Requested: BackendFile, FilePath: keyPath})
		if err != nil {
			t.Fatal(err)
		}
		clear(selection.Key)
		mutate(t, keyPath)
		if _, err := Open(context.Background(), root, BackendFile, keyPath, nil); err == nil {
			t.Fatal("Open accepted unsafe key")
		}
	}
	root := filepath.Join(t.TempDir(), "credentials")
	keyPath := filepath.Join(t.TempDir(), "key")
	selection, err := Resolve(context.Background(), root, Options{Requested: BackendFile, FilePath: keyPath})
	if err != nil {
		t.Fatal(err)
	}
	clear(selection.Key)
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
func TestPinnedFileNeverCreatesMissingKeyOrReadsDriftedLocator(t *testing.T) {
	root := filepath.Join(t.TempDir(), "credentials")
	keyPath := filepath.Join(t.TempDir(), "key")
	selection, err := Resolve(context.Background(), root, Options{Requested: BackendFile, FilePath: keyPath})
	if err != nil {
		t.Fatal(err)
	}
	clear(selection.Key)
	if err := os.Remove(keyPath); err != nil {
		t.Fatal(err)
	}
	if _, err := Resolve(context.Background(), root, Options{Requested: "auto", FilePath: keyPath}); err == nil {
		t.Fatal("Resolve recreated a pinned file key")
	}
	if _, err := os.Stat(keyPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing key was recreated: %v", err)
	}
	other := filepath.Join(t.TempDir(), "other-key")
	if _, err := Open(context.Background(), root, BackendFile, other, nil); err == nil {
		t.Fatal("Open accepted locator drift")
	}
	if _, err := os.Stat(other); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("drifted locator was created: %v", err)
	}
}

func TestLinuxAutoSelectionRequiresAttendedConfirmationForFile(t *testing.T) {
	newOptions := func(confirm ConfirmFile, attended bool) Options {
		return Options{Requested: "auto", Platform: "linux", Detect: func(context.Context) (bool, error) { return false, nil }, ConfirmFile: confirm, Attended: attended}
	}
	for name, opts := range map[string]Options{
		"nonTTY":   newOptions(nil, false),
		"declined": newOptions(func(context.Context) (bool, error) { return false, nil }, true),
	} {
		t.Run(name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "credentials")
			if _, err := Resolve(context.Background(), root, opts); err == nil {
				t.Fatal("absent Secret Service did not fail")
			}
			if _, err := os.Stat(filepath.Join(root, markerName)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("selection left marker: %v", err)
			}
		})
	}
	root := filepath.Join(t.TempDir(), "credentials")
	keyPath := filepath.Join(t.TempDir(), "key")
	sel, err := Resolve(context.Background(), root, Options{Requested: "auto", Platform: "linux", FilePath: keyPath, Detect: func(context.Context) (bool, error) { return false, nil }, Attended: true, ConfirmFile: func(context.Context) (bool, error) { return true, nil }})
	if err != nil || sel.Backend != BackendFile {
		t.Fatalf("confirmed file selection = %#v, %v", sel, err)
	}
	clear(sel.Key)
}

func TestLinuxAutoPresentKeyringDoesNotFallBackWhenUnusable(t *testing.T) {
	root := filepath.Join(t.TempDir(), "credentials")
	_, err := Resolve(context.Background(), root, Options{Requested: "auto", Platform: "linux", Keyring: failingKeyring{}, Detect: func(context.Context) (bool, error) { return true, nil }, Attended: true, ConfirmFile: func(context.Context) (bool, error) { return true, nil }})
	if err == nil {
		t.Fatal("unusable present keyring unexpectedly fell back")
	}
	if _, statErr := os.Stat(filepath.Join(root, markerName)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("failed keyring selection left marker: %v", statErr)
	}
}

func TestKeyringFailureOnPinnedOpenDoesNotFallback(t *testing.T) {
	root := filepath.Join(t.TempDir(), "credentials")
	kr := &fakeKeyring{values: map[string]string{}}
	selection, err := Resolve(context.Background(), root, Options{Requested: BackendKeyring, Platform: "darwin", Keyring: kr})
	if err != nil {
		t.Fatal(err)
	}
	clear(selection.Key)
	if _, err := Open(context.Background(), root, BackendKeyring, "", failingKeyring{}); err == nil {
		t.Fatal("Open fell back after pinned keyring failure")
	}
}

func TestSharedRootReusesPinnedCustody(t *testing.T) {
	root := filepath.Join(t.TempDir(), "credentials")
	kr := &fakeKeyring{values: map[string]string{}}
	first, err := Resolve(context.Background(), root, Options{Requested: BackendKeyring, Platform: "darwin", Keyring: kr})
	if err != nil {
		t.Fatal(err)
	}
	second, err := Resolve(context.Background(), root, Options{Requested: "auto", Platform: "darwin", Keyring: kr})
	if err != nil {
		t.Fatal(err)
	}
	defer clear(first.Key)
	defer clear(second.Key)
	if first.Locator != second.Locator || string(first.Key) != string(second.Key) {
		t.Fatal("shared root did not reuse pinned custody")
	}
}
