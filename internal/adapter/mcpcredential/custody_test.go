package mcpcredential

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	keyringapi "github.com/zalando/go-keyring"
	"golang.org/x/sys/unix"

	"github.com/stacklok/mecatl/internal/adapter/credentialstore"
)

type fakeKeyring struct {
	values    map[string]string
	deleteErr error
}

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
	if f.deleteErr != nil {
		return f.deleteErr
	}
	key := service + "\x00" + account
	if _, ok := f.values[key]; !ok {
		return keyringapi.ErrNotFound
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

func TestKeyringReadyArtifactRecoveryErrorsAreClassified(t *testing.T) {
	for _, tc := range []struct {
		name string
		want error
	}{
		{name: "missing", want: ErrArtifactMissing},
		{name: "identity mismatch", want: ErrArtifactMismatch},
	} {
		t.Run(tc.name, func(t *testing.T) {
			kr := &fakeKeyring{values: map[string]string{}}
			root := filepath.Join(t.TempDir(), "credentials")
			selection, err := Resolve(context.Background(), root, Options{Requested: BackendKeyring, Keyring: kr})
			if err != nil {
				t.Fatal(err)
			}
			clear(selection.Key)
			account := keyringService + "\x00" + selection.Locator
			if tc.want == ErrArtifactMissing {
				delete(kr.values, account)
			} else {
				kr.values[account] = base64.RawStdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32))
			}
			_, err = Open(context.Background(), root, BackendKeyring, "", kr)
			if !errors.Is(err, tc.want) {
				t.Fatalf("Open error = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestDirectMCPOnboarding_Scenario7_ResetRotatesFileKeyAndPreservesLegacyNamespace(t *testing.T) {
	root := filepath.Join(t.TempDir(), "credentials")
	keyPath := filepath.Join(t.TempDir(), "key")
	first, err := Resolve(context.Background(), root, Options{Requested: BackendFile, FilePath: keyPath})
	if err != nil {
		t.Fatal(err)
	}
	firstKey := string(first.Key)
	clear(first.Key)
	native, err := credentialstore.NewEncryptedFile(root, NativeNamespace, bytes.Repeat([]byte{8}, 32))
	if err != nil {
		t.Fatal(err)
	}
	_ = native.Close()
	legacy, err := credentialstore.NewEncryptedFile(root, "mecatl-mcp-oauth", bytes.Repeat([]byte{9}, 32))
	if err != nil {
		t.Fatal(err)
	}
	_ = legacy.Close()
	if err := Reset(context.Background(), root, BackendFile, keyPath, nil); err != nil {
		t.Fatal(err)
	}
	second, err := Resolve(context.Background(), root, Options{Requested: BackendFile, FilePath: keyPath})
	if err != nil {
		t.Fatal(err)
	}
	defer clear(second.Key)
	if string(second.Key) == firstKey {
		t.Fatal("reset retained the old wrapping key")
	}
	if _, err := os.Stat(filepath.Join(root, credentialstore.NamespacePhysicalName("mecatl-mcp-oauth"))); err != nil {
		t.Fatalf("legacy namespace was removed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, credentialstore.NamespacePhysicalName(NativeNamespace))); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("reset did not retire the native namespace: %v", err)
	}
}

// TestDirectMCPOnboarding_Scenario7_ResetResumesEveryInterruptionPoint proves
// Reset resumes after each committed step by interrupting the actual reset path.
func TestDirectMCPOnboarding_Scenario7_ResetResumesEveryInterruptionPoint(t *testing.T) {
	cases := []struct {
		name             string
		stopAt           resetStage
		artifactPresent  bool
		namespacePresent bool
	}{
		{name: "after intent", stopAt: resetIntentRecorded, artifactPresent: true, namespacePresent: true},
		{name: "after artifact removal", stopAt: resetArtifactRemoved, namespacePresent: true},
		{name: "after namespace retirement", stopAt: resetNamespaceRetired},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "credentials")
			keyPath := filepath.Join(t.TempDir(), "key")
			first, err := Resolve(context.Background(), root, Options{Requested: BackendFile, FilePath: keyPath})
			if err != nil {
				t.Fatal(err)
			}
			firstKey := string(first.Key)
			native, err := credentialstore.NewEncryptedFile(root, NativeNamespace, first.Key)
			clear(first.Key)
			if err != nil {
				t.Fatal(err)
			}
			if err := native.Close(); err != nil {
				t.Fatal(err)
			}

			stop := errors.New("simulated reset interruption")
			_, err = resetWithResult(context.Background(), root, BackendFile, keyPath, nil, false, func(stage resetStage) error {
				if stage == tc.stopAt {
					return stop
				}
				return nil
			})
			if !errors.Is(err, stop) {
				t.Fatalf("reset interruption error = %v, want %v", err, stop)
			}
			marker, err := readMarker(root)
			if err != nil || marker.State != markerResetting {
				t.Fatalf("interrupted reset marker = %#v, %v, want resetting", marker, err)
			}
			if _, err := os.Stat(keyPath); (err == nil) != tc.artifactPresent {
				t.Fatalf("artifact present=%t after %s, err=%v", err == nil, tc.name, err)
			}
			nativePath := filepath.Join(root, credentialstore.NamespacePhysicalName(NativeNamespace))
			if _, err := os.Stat(nativePath); (err == nil) != tc.namespacePresent {
				t.Fatalf("namespace present=%t after %s, err=%v", err == nil, tc.name, err)
			}
			retired := filepath.Join(root, marker.Retired)

			if err := Reset(context.Background(), root, BackendFile, keyPath, nil); err != nil {
				t.Fatalf("resume from %s: %v", tc.name, err)
			}
			if _, err := os.Stat(filepath.Join(root, markerName)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("resume from %s left a marker: %v", tc.name, err)
			}
			if _, err := os.Stat(nativePath); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("resume from %s left the native namespace in place: %v", tc.name, err)
			}
			if _, err := os.Stat(retired); err != nil {
				t.Fatalf("resume from %s did not retain the retired namespace: %v", tc.name, err)
			}
			second, err := Resolve(context.Background(), root, Options{Requested: BackendFile, FilePath: keyPath})
			if err != nil {
				t.Fatalf("resume from %s did not leave usable custody: %v", tc.name, err)
			}
			defer clear(second.Key)
			if string(second.Key) == firstKey {
				t.Fatalf("resume from %s retained the old wrapping key", tc.name)
			}
		})
	}
}

// TestDirectMCPOnboarding_Scenario7_ResetTwiceInARow is the direct regression
// test for the bug this reset redesign fixes: resuming an interrupted reset
// must not depend on how many completed resets, and how many retired
// directories, already exist for this root. The old directory-count
// heuristic stuck forever once a second reset was interrupted after a first
// one had already completed.
func TestDirectMCPOnboarding_Scenario7_ResetTwiceInARow(t *testing.T) {
	root := filepath.Join(t.TempDir(), "credentials")
	keyPath := filepath.Join(t.TempDir(), "key")
	if _, err := Resolve(context.Background(), root, Options{Requested: BackendFile, FilePath: keyPath}); err != nil {
		t.Fatal(err)
	}
	if err := Reset(context.Background(), root, BackendFile, keyPath, nil); err != nil {
		t.Fatalf("first reset failed: %v", err)
	}
	second, err := Resolve(context.Background(), root, Options{Requested: BackendFile, FilePath: keyPath})
	if err != nil {
		t.Fatal(err)
	}
	keyTwo := string(second.Key)
	clear(second.Key)

	// Interrupt the second reset after namespace retirement but before marker
	// removal. One retired directory from the first reset is already on disk.
	stop := errors.New("simulated second-reset interruption")
	_, err = resetWithResult(context.Background(), root, BackendFile, keyPath, nil, false, func(stage resetStage) error {
		if stage == resetNamespaceRetired {
			return stop
		}
		return nil
	})
	if !errors.Is(err, stop) {
		t.Fatalf("second reset interruption error = %v, want %v", err, stop)
	}
	if err := Reset(context.Background(), root, BackendFile, keyPath, nil); err != nil {
		t.Fatalf("resuming the second reset failed: %v", err)
	}
	third, err := Resolve(context.Background(), root, Options{Requested: BackendFile, FilePath: keyPath})
	if err != nil {
		t.Fatal(err)
	}
	defer clear(third.Key)
	if string(third.Key) == keyTwo {
		t.Fatal("resumed second reset retained the old wrapping key")
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	retiredCount := 0
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), retiredNamespacePrefix()) {
			retiredCount++
		}
	}
	if retiredCount != 2 {
		t.Fatalf("two resets left %d retired directories, want 2", retiredCount)
	}
}

// TestDirectMCPOnboarding_Scenario7_ResetLeavesUnrelatedStateUntouched plants
// a second, unrelated custody root and an unrelated raw keyring entry, then
// asserts both survive a reset of a different root byte-for-byte.
func TestDirectMCPOnboarding_Scenario7_ResetLeavesUnrelatedStateUntouched(t *testing.T) {
	kr := &fakeKeyring{values: map[string]string{}}
	rootA := filepath.Join(t.TempDir(), "credentials-a")
	rootB := filepath.Join(t.TempDir(), "credentials-b")
	firstA, err := Resolve(context.Background(), rootA, Options{Requested: BackendKeyring, Platform: "darwin", Keyring: kr})
	if err != nil {
		t.Fatal(err)
	}
	clear(firstA.Key)
	firstB, err := Resolve(context.Background(), rootB, Options{Requested: BackendKeyring, Platform: "darwin", Keyring: kr})
	if err != nil {
		t.Fatal(err)
	}
	keyB := string(firstB.Key)
	clear(firstB.Key)
	kr.values["other-service\x00other-account"] = "unrelated-value"

	if err := Reset(context.Background(), rootA, BackendKeyring, "", kr); err != nil {
		t.Fatal(err)
	}

	reopenedB, err := Open(context.Background(), rootB, BackendKeyring, "", kr)
	if err != nil {
		t.Fatalf("unrelated root broken by reset of another root: %v", err)
	}
	if string(reopenedB.Key) != keyB {
		t.Fatal("unrelated root's key changed")
	}
	clear(reopenedB.Key)
	if kr.values["other-service\x00other-account"] != "unrelated-value" {
		t.Fatal("unrelated keyring entry was touched")
	}
}

func TestDirectMCPOnboarding_Scenario7_ResetRecognizesPrototypeMarker(t *testing.T) {
	root := filepath.Join(t.TempDir(), "credentials")
	keyPath := filepath.Join(t.TempDir(), "key")
	if _, err := prepareRoot(root); err != nil {
		t.Fatal(err)
	}
	key := bytes.Repeat([]byte{4}, 32)
	if err := os.WriteFile(keyPath, []byte(base64.StdEncoding.EncodeToString(key)), 0o600); err != nil {
		t.Fatal(err)
	}
	locator, err := fileLocator(root, keyPath)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(locator))
	marker := fmt.Sprintf(`{"version":1,"backend":"file","locator_sha256":"%s"}`, hex.EncodeToString(digest[:]))
	legacy, err := credentialstore.NewEncryptedFile(root, "mecatl-mcp-oauth", bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	_ = legacy.Close()
	if err := os.WriteFile(filepath.Join(root, markerName), []byte(marker), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Reset(context.Background(), root, BackendFile, keyPath, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := Resolve(context.Background(), root, Options{Requested: BackendFile, FilePath: keyPath}); err != nil {
		t.Fatalf("prototype reset did not create current custody: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, credentialstore.NamespacePhysicalName("mecatl-mcp-oauth"))); err != nil {
		t.Fatalf("prototype reset removed the pre-namespace data: %v", err)
	}
}

// TestDirectMCPOnboarding_Scenario7_ResetPrototypeRootKeepsKeyEnvRecords
// proves the legacy namespace is left byte-for-byte usable: a key_env
// profile's own record, encrypted with its own independently supplied key,
// still decrypts after a prototype-marker root sharing that namespace is
// reset. This is what makes it safe for reset to no longer refuse when a
// key_env profile shares the root.
func TestDirectMCPOnboarding_Scenario7_ResetPrototypeMarkerWithMissingArtifact(t *testing.T) {
	root := filepath.Join(t.TempDir(), "credentials")
	keyPath := filepath.Join(t.TempDir(), "missing-key")
	if _, err := prepareRoot(root); err != nil {
		t.Fatal(err)
	}
	locator, err := CanonicalFileKeyPath(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	legacyDigest := sha256.Sum256([]byte(locator))
	marker := fmt.Sprintf(`{"version":1,"backend":"file","locator_sha256":"%s"}`, hex.EncodeToString(legacyDigest[:]))
	if err := os.WriteFile(filepath.Join(root, markerName), []byte(marker), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Reset(context.Background(), root, BackendFile, keyPath, nil); err != nil {
		t.Fatalf("reset with already-missing prototype key: %v", err)
	}
	fresh, err := Resolve(context.Background(), root, Options{Requested: BackendFile, FilePath: keyPath})
	if err != nil {
		t.Fatalf("fresh custody after prototype cleanup: %v", err)
	}
	defer clear(fresh.Key)
}

func TestDirectMCPOnboarding_Scenario7_ResetPrototypeRootKeepsKeyEnvRecords(t *testing.T) {
	root := filepath.Join(t.TempDir(), "credentials")
	keyPath := filepath.Join(t.TempDir(), "key")
	if _, err := prepareRoot(root); err != nil {
		t.Fatal(err)
	}
	key := bytes.Repeat([]byte{4}, 32)
	if err := os.WriteFile(keyPath, []byte(base64.StdEncoding.EncodeToString(key)), 0o600); err != nil {
		t.Fatal(err)
	}
	locator, err := fileLocator(root, keyPath)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(locator))
	marker := fmt.Sprintf(`{"version":1,"backend":"file","locator_sha256":"%s"}`, hex.EncodeToString(digest[:]))
	if err := os.WriteFile(filepath.Join(root, markerName), []byte(marker), 0o600); err != nil {
		t.Fatal(err)
	}

	keyEnvKey := bytes.Repeat([]byte{9}, 32)
	store, err := credentialstore.NewEncryptedFile(root, "mecatl-mcp-oauth", keyEnvKey)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Put(context.Background(), []byte("token"), []byte("secret-value"), nil); err != nil {
		t.Fatal(err)
	}
	_ = store.Close()

	if err := Reset(context.Background(), root, BackendFile, keyPath, nil); err != nil {
		t.Fatal(err)
	}

	reopened, err := credentialstore.NewEncryptedFile(root, "mecatl-mcp-oauth", keyEnvKey)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	rec, err := reopened.Get(context.Background(), []byte("token"))
	if err != nil || string(rec.Value) != "secret-value" {
		t.Fatalf("key_env record after reset = %q, %v, want %q", rec.Value, err, "secret-value")
	}
}

// TestDirectMCPOnboarding_Scenario7_ResetRefusesRootWithoutMarker asserts
// Reset never deletes or adopts an artifact that has no marker at all: a
// missing marker means there is nothing for Reset to identify or resume.
func TestDirectMCPOnboarding_Scenario7_ResetRefusesRootWithoutMarker(t *testing.T) {
	root := filepath.Join(t.TempDir(), "credentials")
	keyPath := filepath.Join(t.TempDir(), "key")
	if _, err := prepareRoot(root); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, []byte(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{3}, 32))), 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := Reset(context.Background(), root, BackendFile, keyPath, nil); err == nil {
		t.Fatal("expected reset with no marker to refuse")
	}
	after, err := os.ReadFile(keyPath)
	if err != nil || string(after) != string(before) {
		t.Fatal("reset with no marker deleted the unmarked artifact")
	}
}

// TestDirectMCPOnboarding_Scenario7_ResetRejectsLocatorDrift plants a marker
// whose LocatorSHA256 no longer matches the pinned artifact and asserts Reset
// refuses without touching the artifact or the marker.
func TestDirectMCPOnboarding_Scenario7_ResetRejectsLocatorDrift(t *testing.T) {
	root := filepath.Join(t.TempDir(), "credentials")
	keyPath := filepath.Join(t.TempDir(), "key")
	sel, err := Resolve(context.Background(), root, Options{Requested: BackendFile, FilePath: keyPath})
	if err != nil {
		t.Fatal(err)
	}
	clear(sel.Key)
	marker, err := readMarker(root)
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	marker.LocatorSHA256 = strings.Repeat("a", sha256.Size*2)
	if err := publishMarker(root, marker, true); err != nil {
		t.Fatal(err)
	}
	if err := Reset(context.Background(), root, BackendFile, keyPath, nil); err == nil || !strings.Contains(err.Error(), "unproven artifact") {
		t.Fatalf("locator-drift reset error = %v, want unproven artifact", err)
	}
	after, err := os.ReadFile(keyPath)
	if err != nil || string(after) != string(before) {
		t.Fatal("locator-drift reset mutated the file artifact")
	}
	if _, err := os.Stat(filepath.Join(root, markerName)); err != nil {
		t.Fatalf("locator-drift reset removed the marker: %v", err)
	}
}

// TestDirectMCPOnboarding_Scenario7_ResetRejectsResettingLocatorDrift is the
// resuming-side counterpart: a resetting marker whose locator no longer
// matches the profile's current key path (edited between runs) must still
// refuse, not blindly trust the digest it proved on the earlier run.
func TestDirectMCPOnboarding_Scenario7_ResetRejectsResettingLocatorDrift(t *testing.T) {
	root := filepath.Join(t.TempDir(), "credentials")
	keyPath := filepath.Join(t.TempDir(), "key")
	if _, err := Resolve(context.Background(), root, Options{Requested: BackendFile, FilePath: keyPath}); err != nil {
		t.Fatal(err)
	}
	current, err := readMarker(root)
	if err != nil {
		t.Fatal(err)
	}
	intent := backendMarker{
		Version: 1, State: markerResetting, StoreNamespace: NativeNamespace,
		Backend: BackendFile, LocatorSHA256: strings.Repeat("a", sha256.Size*2),
		InitSHA256: current.InitSHA256,
		Retired:    newRetiredNamespaceName([]byte("seed")),
	}
	if err := publishMarker(root, intent, true); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := Reset(context.Background(), root, BackendFile, keyPath, nil); err == nil || !strings.Contains(err.Error(), "unproven artifact") {
		t.Fatalf("resetting-marker locator-drift error = %v, want unproven artifact", err)
	}
	after, err := os.ReadFile(keyPath)
	if err != nil || string(after) != string(before) {
		t.Fatal("resetting-marker locator-drift mutated the file artifact")
	}
}

// TestDirectMCPOnboarding_Scenario7_ResetRejectsBackendMismatch asserts Reset
// refuses when the caller-supplied backend disagrees with the marker's
// pinned backend, leaving existing custody usable.
func TestDirectMCPOnboarding_Scenario7_ResetRejectsBackendMismatch(t *testing.T) {
	root := filepath.Join(t.TempDir(), "credentials")
	keyPath := filepath.Join(t.TempDir(), "key")
	sel, err := Resolve(context.Background(), root, Options{Requested: BackendFile, FilePath: keyPath})
	if err != nil {
		t.Fatal(err)
	}
	clear(sel.Key)
	if err := Reset(context.Background(), root, BackendKeyring, "", &fakeKeyring{values: map[string]string{}}); err == nil || !strings.Contains(err.Error(), "does not match the selected profile") {
		t.Fatalf("backend-mismatch reset error = %v", err)
	}
	reopened, err := Open(context.Background(), root, BackendFile, keyPath, nil)
	if err != nil {
		t.Fatalf("backend-mismatch reset broke existing custody: %v", err)
	}
	clear(reopened.Key)
}

// TestDirectMCPOnboarding_Scenario7_ResetRejectsMalformedMarker asserts a
// marker that decodes as neither the current nor the prototype shape fails
// with an actionable, specific error rather than a generic one.
func TestDirectMCPOnboarding_Scenario7_ResetRejectsMalformedMarker(t *testing.T) {
	root := filepath.Join(t.TempDir(), "credentials")
	keyPath := filepath.Join(t.TempDir(), "key")
	if _, err := prepareRoot(root); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, markerName), []byte("not-json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Reset(context.Background(), root, BackendFile, keyPath, nil); err == nil || !strings.Contains(err.Error(), "identifiable native marker") {
		t.Fatalf("malformed-marker reset error = %v, want an identifiable-marker message", err)
	}
}

// TestDirectMCPOnboarding_Scenario7_ResetRejectsUnsafeFileArtifact asserts
// Reset fails closed before changing the marker or namespace for unsafe artifacts.
func TestDirectMCPOnboarding_Scenario7_ResetRejectsUnsafeFileArtifact(t *testing.T) {
	for _, kind := range []string{"mode", "hard link", "symlink", "directory"} {
		t.Run(kind, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "credentials")
			keyPath := filepath.Join(t.TempDir(), "key")
			selection, err := Resolve(context.Background(), root, Options{Requested: BackendFile, FilePath: keyPath})
			if err != nil {
				t.Fatal(err)
			}
			clear(selection.Key)
			originalKey, err := os.ReadFile(keyPath)
			if err != nil {
				t.Fatal(err)
			}
			nativePath := filepath.Join(root, credentialstore.NamespacePhysicalName(NativeNamespace))
			if err := os.MkdirAll(nativePath, 0o700); err != nil {
				t.Fatal(err)
			}
			markerPath := filepath.Join(root, markerName)
			beforeMarker, err := os.ReadFile(markerPath)
			if err != nil {
				t.Fatal(err)
			}

			extraPath := keyPath + ".extra"
			switch kind {
			case "mode":
				if err := os.Chmod(keyPath, 0o644); err != nil {
					t.Fatal(err)
				}
			case "hard link":
				if err := os.Link(keyPath, extraPath); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Rename(keyPath, extraPath); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(extraPath, keyPath); err != nil {
					t.Fatal(err)
				}
			case "directory":
				if err := os.Rename(keyPath, extraPath); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(keyPath, 0o700); err != nil {
					t.Fatal(err)
				}
			}

			resetErr := Reset(context.Background(), root, BackendFile, keyPath, nil)
			if resetErr == nil || (kind != "symlink" && !strings.Contains(resetErr.Error(), "unsafe or unavailable artifact")) {
				t.Fatalf("unsafe-artifact reset error = %v", resetErr)
			}
			afterMarker, err := os.ReadFile(markerPath)
			if err != nil || string(afterMarker) != string(beforeMarker) {
				t.Fatalf("unsafe-artifact reset changed marker: %q, err = %v", afterMarker, err)
			}
			if _, err := os.Stat(nativePath); err != nil {
				t.Fatalf("unsafe-artifact reset retired the namespace: %v", err)
			}
			switch kind {
			case "mode":
				info, err := os.Stat(keyPath)
				if err != nil || info.Mode().Perm() != 0o644 {
					t.Fatalf("unsafe-artifact reset changed mode, info=%v err=%v", info, err)
				}
			case "hard link":
				for _, path := range []string{keyPath, extraPath} {
					got, err := os.ReadFile(path)
					if err != nil || string(got) != string(originalKey) {
						t.Fatalf("unsafe-artifact reset changed hard link %q: err=%v", path, err)
					}
				}
			case "symlink":
				info, err := os.Lstat(keyPath)
				if err != nil || info.Mode()&os.ModeSymlink == 0 {
					t.Fatalf("unsafe-artifact reset changed symlink, info=%v err=%v", info, err)
				}
				got, err := os.ReadFile(extraPath)
				if err != nil || string(got) != string(originalKey) {
					t.Fatalf("unsafe-artifact reset changed symlink target: %v", err)
				}
			case "directory":
				info, err := os.Stat(keyPath)
				if err != nil || !info.IsDir() {
					t.Fatalf("unsafe-artifact reset changed directory, info=%v err=%v", info, err)
				}
				got, err := os.ReadFile(extraPath)
				if err != nil || string(got) != string(originalKey) {
					t.Fatalf("unsafe-artifact reset changed original key: %v", err)
				}
			}
		})
	}
}

func TestResetPreservesFileArtifactOwnedByAnotherRoot(t *testing.T) {
	sharedKeyPath := filepath.Join(t.TempDir(), "shared-key")
	rootA := filepath.Join(t.TempDir(), "credentials-a")
	rootB := filepath.Join(t.TempDir(), "credentials-b")
	owner, err := Resolve(context.Background(), rootA, Options{Requested: BackendFile, FilePath: sharedKeyPath})
	if err != nil {
		t.Fatal(err)
	}
	ownerKey := string(owner.Key)
	clear(owner.Key)

	if _, err := Resolve(context.Background(), rootB, Options{Requested: BackendFile, FilePath: sharedKeyPath}); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("second root resolve error = %v, want existing-artifact refusal", err)
	}
	result, err := ResetWithResult(context.Background(), rootB, BackendFile, sharedKeyPath, nil, false)
	if err != nil || !result.ArtifactPreserved {
		t.Fatalf("second root reset result=%#v error=%v, want retained shared artifact", result, err)
	}
	if _, err := os.Stat(filepath.Join(rootB, markerName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("second root reset did not retire its own marker: %v", err)
	}
	if _, err := os.Stat(sharedKeyPath); err != nil {
		t.Fatalf("second root reset deleted the first root's key: %v", err)
	}
	stillOwned, err := Open(context.Background(), rootA, BackendFile, sharedKeyPath, nil)
	if err != nil {
		t.Fatalf("first root custody was damaged: %v", err)
	}
	defer clear(stillOwned.Key)
	if string(stillOwned.Key) != ownerKey {
		t.Fatal("first root key changed after second root reset")
	}
}

func TestResetResumesPreservingForeignFileArtifact(t *testing.T) {
	sharedKeyPath := filepath.Join(t.TempDir(), "shared-key")
	rootA := filepath.Join(t.TempDir(), "credentials-a")
	rootB := filepath.Join(t.TempDir(), "credentials-b")
	owner, err := Resolve(context.Background(), rootA, Options{Requested: BackendFile, FilePath: sharedKeyPath})
	if err != nil {
		t.Fatal(err)
	}
	ownerKey := string(owner.Key)
	clear(owner.Key)
	if _, err := Resolve(context.Background(), rootB, Options{Requested: BackendFile, FilePath: sharedKeyPath}); err == nil {
		t.Fatal("second root unexpectedly adopted the shared file key")
	}
	raw, err := readPrivateFile(filepath.Join(rootB, markerName), maxMarkerBytes)
	if err != nil {
		t.Fatal(err)
	}
	marker, err := decodeMarker(raw)
	if err != nil {
		t.Fatal(err)
	}
	marker.State = markerResetting
	marker.Retired = newRetiredNamespaceName(raw)
	marker.KeepArtifact = true
	if err := publishMarker(rootB, marker, true); err != nil {
		t.Fatal(err)
	}
	result, err := ResetWithResult(context.Background(), rootB, BackendFile, sharedKeyPath, nil, false)
	if err != nil || !result.ArtifactPreserved {
		t.Fatalf("resumed reset result=%#v error=%v, want preserved artifact", result, err)
	}
	stillOwned, err := Open(context.Background(), rootA, BackendFile, sharedKeyPath, nil)
	if err != nil {
		t.Fatalf("resumed reset damaged the first root: %v", err)
	}
	defer clear(stillOwned.Key)
	if string(stillOwned.Key) != ownerKey {
		t.Fatal("resumed reset changed the first root's key")
	}
}

func TestResetResumesFileKeyInsideRetiredNamespace(t *testing.T) {
	root := filepath.Join(t.TempDir(), "credentials")
	keyPath := filepath.Join(root, credentialstore.NamespacePhysicalName(NativeNamespace), "key")
	first, err := Resolve(context.Background(), root, Options{Requested: BackendFile, FilePath: keyPath})
	if err != nil {
		t.Fatal(err)
	}
	firstKey := string(first.Key)
	clear(first.Key)

	stop := errors.New("simulated reset interruption")
	_, err = resetWithResult(context.Background(), root, BackendFile, keyPath, nil, false, func(stage resetStage) error {
		if stage == resetNamespaceRetired {
			return stop
		}
		return nil
	})
	if !errors.Is(err, stop) {
		t.Fatalf("reset interruption error = %v, want %v", err, stop)
	}
	marker, err := readMarker(root)
	if err != nil || marker.State != markerResetting {
		t.Fatalf("interrupted reset marker = %#v, %v, want resetting", marker, err)
	}
	retired := filepath.Join(root, marker.Retired)
	if _, err := os.Stat(filepath.Join(retired, "key")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("reset left the old file key inside retired custody: %v", err)
	}
	if err := Reset(context.Background(), root, BackendFile, keyPath, nil); err != nil {
		t.Fatalf("resume after namespace rename: %v", err)
	}
	second, err := Resolve(context.Background(), root, Options{Requested: BackendFile, FilePath: keyPath})
	if err != nil {
		t.Fatalf("fresh custody after resumed reset: %v", err)
	}
	defer clear(second.Key)
	if string(second.Key) == firstKey {
		t.Fatal("resumed reset retained the previous key")
	}
}

// TestDirectMCPOnboarding_Scenario7_ResetKeyringBackend proves Reset also
// rotates keyring-backed custody; every prior Reset test used BackendFile.
func TestDirectMCPOnboarding_Scenario7_ResetKeyringBackend(t *testing.T) {
	root := filepath.Join(t.TempDir(), "credentials")
	kr := &fakeKeyring{values: map[string]string{}}
	first, err := Resolve(context.Background(), root, Options{Requested: BackendKeyring, Platform: "darwin", Keyring: kr})
	if err != nil {
		t.Fatal(err)
	}
	firstKey := string(first.Key)
	clear(first.Key)
	if err := Reset(context.Background(), root, BackendKeyring, "", kr); err != nil {
		t.Fatal(err)
	}
	second, err := Resolve(context.Background(), root, Options{Requested: BackendKeyring, Platform: "darwin", Keyring: kr})
	if err != nil {
		t.Fatal(err)
	}
	defer clear(second.Key)
	if string(second.Key) == firstKey {
		t.Fatal("keyring reset retained the old wrapping key")
	}
}

// TestDirectMCPOnboarding_Scenario7_ResetToleratesMissingKeyringArtifact
// covers deleteArtifact's keyringapi.ErrNotFound tolerance under Reset: an
// externally cleared keyring entry must not fail the reset. The root is
// resolved through EvalSymlinks before computing the locator so the digest
// matches what Reset itself computes from the canonicalized root.
func TestDirectMCPOnboarding_Scenario7_ResetToleratesMissingKeyringArtifact(t *testing.T) {
	root := filepath.Join(t.TempDir(), "credentials")
	kr := &fakeKeyring{values: map[string]string{}}
	first, err := Resolve(context.Background(), root, Options{Requested: BackendKeyring, Platform: "darwin", Keyring: kr})
	if err != nil {
		t.Fatal(err)
	}
	clear(first.Key)
	loc, err := pinnedLocator(mustEvalSymlinks(t, root), BackendKeyring, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := kr.values[keyringService+"\x00"+loc]; !ok {
		t.Fatal("test setup did not compute the same locator Resolve used")
	}
	delete(kr.values, keyringService+"\x00"+loc)
	if err := Reset(context.Background(), root, BackendKeyring, "", kr); err != nil {
		t.Fatal(err)
	}
	reopened, err := Resolve(context.Background(), root, Options{Requested: BackendKeyring, Platform: "darwin", Keyring: kr})
	if err != nil {
		t.Fatalf("reset over a missing keyring artifact did not leave usable custody: %v", err)
	}
	clear(reopened.Key)
}

func TestResetRefusesWhenKeyringArtifactCannotBeVerified(t *testing.T) {
	root := filepath.Join(t.TempDir(), "credentials")
	keyring := &fakeKeyring{values: map[string]string{}}
	selection, err := Resolve(context.Background(), root, Options{Requested: BackendKeyring, Keyring: keyring})
	if err != nil {
		t.Fatal(err)
	}
	clear(selection.Key)
	account := keyringService + "\x00" + selection.Locator
	artifactBefore := keyring.values[account]
	nativePath := filepath.Join(root, credentialstore.NamespacePhysicalName(NativeNamespace))
	if err := os.MkdirAll(nativePath, 0o700); err != nil {
		t.Fatal(err)
	}
	markerPath := filepath.Join(root, markerName)
	markerBefore, err := os.ReadFile(markerPath)
	if err != nil {
		t.Fatal(err)
	}

	if err := Reset(context.Background(), root, BackendKeyring, "", failingKeyring{}); err == nil || !strings.Contains(err.Error(), "unsafe or unavailable artifact") {
		t.Fatalf("reset with unavailable keyring error = %v", err)
	}
	markerAfter, err := os.ReadFile(markerPath)
	if err != nil || string(markerAfter) != string(markerBefore) {
		t.Fatalf("unverifiable artifact reset changed marker: %q, err=%v", markerAfter, err)
	}
	if _, err := os.Stat(nativePath); err != nil {
		t.Fatalf("unverifiable artifact reset retired namespace: %v", err)
	}
	if keyring.values[account] != artifactBefore {
		t.Fatal("unverifiable artifact reset changed the keyring entry")
	}
}

func TestResetRetriesAfterKeyringDeleteFailureBeforeNamespaceRetirement(t *testing.T) {
	root := filepath.Join(t.TempDir(), "credentials")
	kr := &fakeKeyring{values: map[string]string{}}
	selection, err := Resolve(context.Background(), root, Options{Requested: BackendKeyring, Keyring: kr})
	if err != nil {
		t.Fatal(err)
	}
	clear(selection.Key)
	nativePath := filepath.Join(root, credentialstore.NamespacePhysicalName(NativeNamespace))
	if err := os.MkdirAll(nativePath, 0o700); err != nil {
		t.Fatal(err)
	}
	kr.deleteErr = errors.New("transient delete failure")
	if err := Reset(context.Background(), root, BackendKeyring, "", kr); err == nil || !strings.Contains(err.Error(), "could not remove the keyring artifact") {
		t.Fatalf("first reset error = %v, want keyring deletion failure", err)
	}
	if _, err := os.Stat(nativePath); err != nil {
		t.Fatalf("failed key deletion retired the namespace: %v", err)
	}
	if marker, err := readMarker(root); err != nil || marker.State != markerResetting {
		t.Fatalf("failed reset marker = %#v, %v, want resetting", marker, err)
	}
	kr.deleteErr = nil
	if err := Reset(context.Background(), root, BackendKeyring, "", kr); err != nil {
		t.Fatalf("retry reset: %v", err)
	}
	if _, err := os.Stat(nativePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("retry left native namespace: %v", err)
	}
}

// TestResolveAndOpenRefuseResettingMarker asserts a marker left mid-reset is
// never treated as ready or recovered by Resolve/Open: only Reset itself may
// resume it.
func TestResolveAndOpenRefuseResettingMarker(t *testing.T) {
	root := filepath.Join(t.TempDir(), "credentials")
	keyPath := filepath.Join(t.TempDir(), "key")
	if _, err := Resolve(context.Background(), root, Options{Requested: BackendFile, FilePath: keyPath}); err != nil {
		t.Fatal(err)
	}
	locator, err := pinnedLocator(root, BackendFile, keyPath)
	if err != nil {
		t.Fatal(err)
	}
	current, err := readMarker(root)
	if err != nil {
		t.Fatal(err)
	}
	intent := backendMarker{
		Version: 1, State: markerResetting, StoreNamespace: NativeNamespace,
		Backend: BackendFile, LocatorSHA256: locatorDigest(BackendFile, locator),
		InitSHA256: current.InitSHA256,
		Retired:    newRetiredNamespaceName([]byte("seed")),
	}
	if err := publishMarker(root, intent, true); err != nil {
		t.Fatal(err)
	}
	if got := InspectMarker(root); got != MarkerRecovery {
		t.Fatalf("resetting marker inspection = %q, want %q", got, MarkerRecovery)
	}
	if _, err := Resolve(context.Background(), root, Options{Requested: BackendFile, FilePath: keyPath}); err == nil || !strings.Contains(err.Error(), "reset is in progress") {
		t.Fatalf("Resolve over resetting marker = %v, want reset-in-progress", err)
	}
	if _, err := Open(context.Background(), root, BackendFile, keyPath, nil); err == nil || !strings.Contains(err.Error(), "reset is in progress") {
		t.Fatalf("Open over resetting marker = %v, want reset-in-progress", err)
	}
	if _, err := os.ReadFile(keyPath); err != nil {
		t.Fatalf("resetting-marker refusal touched the artifact: %v", err)
	}
}

func TestUnlinkPrivateFileFsyncsDirectoryWhenFileIsAbsent(t *testing.T) {
	dirPath := t.TempDir()
	dir, err := unix.Open(dirPath, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = unix.Close(dir) }()
	wantErr := errors.New("sync sentinel")
	calls := 0
	err = unlinkPrivateFileWithSync(dir, "already-absent", func(got int) error {
		calls++
		if got != dir {
			t.Fatalf("sync fd = %d, want %d", got, dir)
		}
		return wantErr
	})
	if !errors.Is(err, wantErr) || calls != 1 {
		t.Fatalf("absent-file unlink error=%v sync calls=%d, want sentinel and one sync", err, calls)
	}
}

// TestDecodeMarkerRejectsMalformedResettingShape asserts the resetting state
// retains a valid artifact identity and retired name, while pending/ready
// states never carry a retired name.
func TestDecodeMarkerRejectsMalformedResettingShape(t *testing.T) {
	base := backendMarker{
		Version: 1, State: markerResetting, StoreNamespace: NativeNamespace,
		Backend: BackendFile, LocatorSHA256: strings.Repeat("a", sha256.Size*2),
		InitSHA256: strings.Repeat("b", sha256.Size*2), Retired: newRetiredNamespaceName([]byte("valid")),
	}
	cases := map[string]backendMarker{
		"missingRetired": func() backendMarker { m := base; m.Retired = ""; return m }(),
		"badRetiredName": func() backendMarker { m := base; m.Retired = "not-a-retired-name"; return m }(),
		"shortSuffix":    func() backendMarker { m := base; m.Retired = retiredNamespacePrefix() + "ab"; return m }(),
		"missingInit":    func() backendMarker { m := base; m.InitSHA256 = ""; return m }(),
	}
	for name, marker := range cases {
		t.Run(name, func(t *testing.T) {
			data, err := json.Marshal(marker)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := decodeMarker(data); err == nil {
				t.Fatalf("decodeMarker accepted malformed resetting marker: %#v", marker)
			}
		})
	}
	ready := backendMarker{Version: 1, State: markerReady, StoreNamespace: NativeNamespace, Backend: BackendFile, LocatorSHA256: strings.Repeat("a", sha256.Size*2), InitSHA256: strings.Repeat("b", sha256.Size*2), Retired: newRetiredNamespaceName([]byte("x"))}
	data, err := json.Marshal(ready)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeMarker(data); err == nil {
		t.Fatal("decodeMarker accepted a ready marker carrying a Retired name")
	}
}

// TestMarkerSchemaPinnedToVersion is a golden list of every field version:1
// carries. It fails if a field is added without a version decision, the exact
// failure mode #1922 traced back to: 9e9f7cf91 made new fields mandatory
// under the unchanged version: 1.
func TestMarkerSchemaPinnedToVersion(t *testing.T) {
	data, err := json.Marshal(backendMarker{Version: 1, State: markerReady, StoreNamespace: NativeNamespace, Backend: BackendFile, LocatorSHA256: strings.Repeat("a", sha256.Size*2), InitSHA256: strings.Repeat("b", sha256.Size*2)})
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"version": true, "state": true, "store_namespace": true, "backend": true, "locator_sha256": true, "init_sha256": true}
	if len(fields) != len(want) {
		t.Fatalf("version:1 marker fields = %v, want exactly %v", fields, want)
	}
	for name := range want {
		if _, ok := fields[name]; !ok {
			t.Fatalf("version:1 marker is missing field %q", name)
		}
	}

	resetting := backendMarker{
		Version: 1, State: markerResetting, StoreNamespace: NativeNamespace, Backend: BackendFile,
		LocatorSHA256: strings.Repeat("a", sha256.Size*2), InitSHA256: strings.Repeat("b", sha256.Size*2),
		Retired: newRetiredNamespaceName([]byte("seed")), KeepArtifact: true,
	}
	data, err = json.Marshal(resetting)
	if err != nil {
		t.Fatal(err)
	}
	fields = nil
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	wantResetting := map[string]bool{"version": true, "state": true, "store_namespace": true, "backend": true, "locator_sha256": true, "init_sha256": true, "retired": true, "keep_artifact": true}
	if len(fields) != len(wantResetting) {
		t.Fatalf("resetting marker fields = %v, want exactly %v", fields, wantResetting)
	}
	for name := range wantResetting {
		if _, ok := fields[name]; !ok {
			t.Fatalf("resetting marker is missing field %q", name)
		}
	}
	if _, err := decodeMarker(data); err != nil {
		t.Fatalf("decodeMarker rejected artifact-preserving reset intent: %v", err)
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

func TestResolveRecoversLinkatPublishedMarkerTemp(t *testing.T) {
	root := filepath.Join(t.TempDir(), "credentials")
	keyPath := filepath.Join(t.TempDir(), "key")
	if _, err := prepareRoot(root); err != nil {
		t.Fatal(err)
	}
	locator, err := fileLocator(root, keyPath)
	if err != nil {
		t.Fatal(err)
	}
	pending := backendMarker{
		Version: 1, State: markerPending, StoreNamespace: NativeNamespace,
		Backend: BackendFile, LocatorSHA256: locatorDigest(BackendFile, locator),
		InitSHA256: keyDigest(bytes.Repeat([]byte{1}, 32)),
	}
	if err := publishMarker(root, pending, false); err != nil {
		t.Fatal(err)
	}
	markerData, err := os.ReadFile(filepath.Join(root, markerName))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, markerName)); err != nil {
		t.Fatal(err)
	}
	tempName := "." + markerName + ".0123456789abcdef01234567.tmp"
	tempPath := filepath.Join(root, tempName)
	if err := os.WriteFile(tempPath, markerData, 0o600); err != nil {
		t.Fatal(err)
	}
	dir, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := unix.Linkat(dir, tempName, dir, markerName, 0); err != nil {
		_ = unix.Close(dir)
		t.Fatal(err)
	}
	var st unix.Stat_t
	fd, err := unix.Openat(dir, markerName, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		_ = unix.Close(dir)
		t.Fatal(err)
	}
	if err := unix.Fstat(fd, &st); err != nil {
		_ = unix.Close(fd)
		_ = unix.Close(dir)
		t.Fatal(err)
	}
	_ = unix.Close(fd)
	_ = unix.Close(dir)
	if st.Nlink != 2 {
		t.Fatalf("simulated Linkat publication nlink = %d, want 2", st.Nlink)
	}

	selection, err := Resolve(context.Background(), root, Options{Requested: BackendFile, FilePath: keyPath})
	if err != nil {
		t.Fatal(err)
	}
	defer clear(selection.Key)
	if _, err := os.Lstat(tempPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("published marker temp was not safely removed: %v", err)
	}
	marker, err := readMarker(root)
	if err != nil || marker.State != markerReady {
		t.Fatalf("recovered marker = %#v, %v", marker, err)
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
