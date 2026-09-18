package mcpcredential

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	keyringapi "github.com/zalando/go-keyring"

	"github.com/stacklok/mecatl/internal/adapter/credentialstore"
)

type fakeKeyring struct{ values map[string]string }

func (f *fakeKeyring) Get(service, account string) (string, error) {
	v, ok := f.values[service+"\x00"+account]
	if !ok {
		return "", keyringapi.ErrNotFound
	}
	return v, nil
}

func (f *fakeKeyring) Set(service, account, value string) error {
	f.values[service+"\x00"+account] = value
	return nil
}

func (f *fakeKeyring) Delete(service, account string) error {
	key := service + "\x00" + account
	if _, ok := f.values[key]; !ok {
		return errors.New("missing")
	}
	delete(f.values, key)
	return nil
}

type failingKeyring struct{}

func (failingKeyring) Get(string, string) (string, error) { return "", errors.New("unavailable") }
func (failingKeyring) Set(string, string, string) error   { return errors.New("unavailable") }
func (failingKeyring) Delete(string, string) error        { return errors.New("unavailable") }

func TestDirectMCPOnboarding_Scenario2_PrivatePinnedCustody(t *testing.T) {
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
	keyring := &fakeKeyring{values: map[string]string{}}
	keyringRoot := filepath.Join(t.TempDir(), "keyring-credentials")
	keyringSelection, err := Resolve(context.Background(), keyringRoot, Options{Requested: BackendKeyring, Platform: "darwin", Keyring: keyring})
	if err != nil {
		t.Fatal(err)
	}
	defer clear(keyringSelection.Key)
	if len(keyringSelection.Key) != 32 || len(keyringSelection.Locator) != 64 {
		t.Fatalf("keyring selection = %#v", keyringSelection)
	}
}

func TestNativeCustodyRejectsUnmarkedArtifactsWithoutMutation(t *testing.T) {
	t.Run("file", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "credentials")
		keyPath := filepath.Join(t.TempDir(), "key")
		if err := os.MkdirAll(filepath.Dir(keyPath), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(keyPath, []byte(base64.StdEncoding.EncodeToString(make([]byte, 32))), 0600); err != nil {
			t.Fatal(err)
		}
		before, err := os.ReadFile(keyPath)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Resolve(context.Background(), root, Options{Requested: BackendFile, FilePath: keyPath}); err == nil {
			t.Fatal("Resolve adopted an unmarked file key")
		}
		after, err := os.ReadFile(keyPath)
		if err != nil || string(after) != string(before) {
			t.Fatal("unmarked file key was changed")
		}
	})

	t.Run("keyring", func(t *testing.T) {
		kr := &fakeKeyring{values: map[string]string{}}
		root := filepath.Join(t.TempDir(), "credentials")
		if err := os.MkdirAll(root, 0700); err != nil {
			t.Fatal(err)
		}
		loc, err := canonicalPath(root)
		if err != nil {
			t.Fatal(err)
		}
		loc = keyringAccount(loc)
		kr.values[keyringService+"\x00"+loc] = base64.RawStdEncoding.EncodeToString(make([]byte, 32))
		if _, err := Resolve(context.Background(), root, Options{Requested: BackendKeyring, Platform: "darwin", Keyring: kr}); err == nil {
			t.Fatal("Resolve adopted an unmarked keyring key")
		}
		if got := kr.values[keyringService+"\x00"+loc]; got == "" {
			t.Fatal("unmarked keyring key was deleted")
		}
	})
}

func TestNativeAndLegacyCredentialNamespacesShareRootWithoutCollision(t *testing.T) {
	root := filepath.Join(t.TempDir(), "credentials")
	keyPath := filepath.Join(t.TempDir(), "key")
	sel, err := Resolve(context.Background(), root, Options{Requested: BackendFile, FilePath: keyPath})
	if err != nil {
		t.Fatal(err)
	}
	clear(sel.Key)
	nativeKey := bytes.Repeat([]byte{8}, 32)
	native, err := credentialstore.NewEncryptedFile(root, NativeNamespace, nativeKey)
	if err != nil {
		t.Fatal(err)
	}
	defer native.Close()
	legacyKey := bytes.Repeat([]byte{7}, 32)
	legacy, err := credentialstore.NewEncryptedFile(root, "mecatl-mcp-oauth", legacyKey)
	if err != nil {
		t.Fatal(err)
	}
	defer legacy.Close()
	if _, err := os.Stat(filepath.Join(root, credentialstore.NamespacePhysicalName(NativeNamespace))); err != nil {
		t.Fatal("native namespace missing")
	}
	if _, err := os.Stat(filepath.Join(root, credentialstore.NamespacePhysicalName("mecatl-mcp-oauth"))); err != nil {
		t.Fatal("legacy namespace missing")
	}
}

func TestPendingMarkerRecoversCreatedFileAndPublishesReady(t *testing.T) {
	root := filepath.Join(t.TempDir(), "credentials")
	keyPath := filepath.Join(t.TempDir(), "key")
	if err := os.MkdirAll(filepath.Dir(keyPath), 0o700); err != nil {
		t.Fatal(err)
	}
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
	if err := os.WriteFile(keyPath, []byte(base64.StdEncoding.EncodeToString(key)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := prepareRoot(root); err != nil {
		t.Fatal(err)
	}
	locator, err := fileLocator(root, keyPath)
	if err != nil {
		t.Fatal(err)
	}
	pending := backendMarker{Version: 1, State: markerPending, StoreNamespace: NativeNamespace, Backend: BackendFile, LocatorSHA256: locatorDigest(BackendFile, locator), InitSHA256: keyDigest(key)}
	if err := publishMarker(root, pending, false); err != nil {
		t.Fatal(err)
	}
	if got := InspectMarker(root); got != MarkerRecovery {
		t.Fatalf("pending marker inspection = %q, want %q", got, MarkerRecovery)
	}
	if _, err := Open(context.Background(), root, BackendFile, keyPath, nil); err == nil || !strings.Contains(err.Error(), "requires recovery") {
		t.Fatalf("Open pending error = %v, want recovery-required", err)
	}
	if err := os.WriteFile(keyPath, []byte(base64.StdEncoding.EncodeToString(make([]byte, 32))), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Resolve(context.Background(), root, Options{Requested: BackendFile, FilePath: keyPath}); err == nil {
		t.Fatal("pending recovery adopted an artifact with the wrong identity")
	}
	if err := os.WriteFile(keyPath, []byte(base64.StdEncoding.EncodeToString(key)), 0o600); err != nil {
		t.Fatal(err)
	}
	pending.InitSHA256 = keyDigest(key)
	// The marker remains pending after the failed recovery and cancellation.
	if err := publishMarker(root, pending, true); err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Resolve(cancelled, root, Options{Requested: BackendFile, FilePath: keyPath}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled recovery error = %v", err)
	}
	marker, err := readMarker(root)
	if err != nil || marker.State != markerPending {
		t.Fatalf("cancelled recovery finalized marker: %#v, %v", marker, err)
	}
	got, err := Resolve(context.Background(), root, Options{Requested: BackendFile, FilePath: keyPath})
	if err != nil {
		t.Fatal(err)
	}
	defer clear(got.Key)
	if string(got.Key) != string(key) {
		t.Fatal("pending recovery changed the file key")
	}
	marker, err = readMarker(root)
	if err != nil || marker.State != markerReady {
		t.Fatalf("marker = %#v, %v", marker, err)
	}
}

func TestAttendedConfirmationCancellationDoesNotHoldCustodyLock(t *testing.T) {
	root := filepath.Join(t.TempDir(), "credentials")
	ctx, cancel := context.WithCancel(context.Background())
	_, err := Resolve(ctx, root, Options{Requested: "auto", Platform: "linux", Detect: func(context.Context) (bool, error) { return false, nil }, Attended: true, ConfirmFile: func(ctx context.Context) (bool, error) {
		cancel()
		return false, ctx.Err()
	}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Resolve error = %v", err)
	}
	// A cancelled confirmation must not strand the root lock.
	if _, err := Resolve(context.Background(), root, Options{Requested: BackendFile, FilePath: filepath.Join(t.TempDir(), "key")}); err != nil {
		t.Fatalf("root remained locked: %v", err)
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
func TestDirectMCPOnboarding_Scenario2_NoFallbackOrLocatorDrift(t *testing.T) {
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

func TestDirectMCPOnboarding_Scenario2_PlatformSelectionMatrix(t *testing.T) {
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
	if marker, readErr := readMarker(root); readErr != nil || marker.State != markerPending {
		t.Fatalf("failed keyring selection did not leave recoverable pending marker: %v", readErr)
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

func TestCrashLeftMarkerTempDoesNotWedgeNewKeyring(t *testing.T) {
	root := filepath.Join(t.TempDir(), "credentials")
	kr := &fakeKeyring{values: map[string]string{}}
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(root, "."+markerName+".crash-left.tmp")
	if err := os.WriteFile(stale, []byte("occupied"), 0o600); err != nil {
		t.Fatal(err)
	}
	selection, err := Resolve(context.Background(), root, Options{Requested: BackendKeyring, Platform: "darwin", Keyring: kr})
	if err != nil {
		t.Fatal(err)
	}
	clear(selection.Key)
	if _, err := os.Stat(stale); err != nil {
		t.Fatalf("stale marker temp was removed: %v", err)
	}
}

func TestPendingMarkerRestartsWhenArtifactIsAbsent(t *testing.T) {
	root := filepath.Join(t.TempDir(), "credentials")
	keyPath := filepath.Join(t.TempDir(), "key")
	if _, err := prepareRoot(root); err != nil {
		t.Fatal(err)
	}
	locator, err := fileLocator(root, keyPath)
	if err != nil {
		t.Fatal(err)
	}
	pending := backendMarker{Version: 1, State: markerPending, StoreNamespace: NativeNamespace, Backend: BackendFile, LocatorSHA256: locatorDigest(BackendFile, locator), InitSHA256: keyDigest(bytes.Repeat([]byte{1}, 32))}
	if err := publishMarker(root, pending, false); err != nil {
		t.Fatal(err)
	}
	selection, err := Resolve(context.Background(), root, Options{Requested: BackendFile, FilePath: keyPath})
	if err != nil {
		t.Fatal(err)
	}
	defer clear(selection.Key)
	if len(selection.Key) != 32 {
		t.Fatalf("restarted key length = %d", len(selection.Key))
	}
	marker, err := readMarker(root)
	if err != nil || marker.State != markerReady || marker.InitSHA256 != keyDigest(selection.Key) {
		t.Fatalf("restarted marker = %#v, %v", marker, err)
	}
}
