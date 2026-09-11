//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly

package clientauth

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stacklok/mecatl/internal/adapter/credentialstore"
)

func TestHeadlessCredentialStorage_Scenario1_LinuxAutoSelection(t *testing.T) {
	for _, state := range []secretServiceState{secretServicePresent, secretServiceAbsent, 0} {
		for _, platform := range []string{"linux", "darwin"} {
			calls := 0
			backend, err := chooseFreshCredentialBackend(t.Context(), CredentialStoreAuto, platform, func(context.Context) (secretServiceState, error) { calls++; return state, nil })
			if platform == "darwin" {
				if calls != 0 || backend != CredentialBackendKeyring || err != nil {
					t.Fatal("macOS probed")
				}
				continue
			}
			if calls != 1 {
				t.Fatal("Linux did not detect")
			}
			switch state {
			case secretServicePresent:
				if err != nil || backend != CredentialBackendKeyring {
					t.Fatal("present misrouted")
				}
			case secretServiceAbsent:
				if err != nil || backend != CredentialBackendFile {
					t.Fatal("absent misrouted")
				}
			default:
				if err == nil {
					t.Fatal("unknown state accepted")
				}
			}
		}
	}
	failure := errors.New("detection failure")
	if _, err := chooseFreshCredentialBackend(t.Context(), CredentialStoreAuto, "linux", func(context.Context) (secretServiceState, error) { return 0, failure }); !errors.Is(err, failure) {
		t.Fatal("failure downgraded")
	}
	for _, mode := range []CredentialStoreMode{CredentialStoreFile, CredentialStoreKeyring} {
		_, err := chooseFreshCredentialBackend(t.Context(), mode, "linux", func(context.Context) (secretServiceState, error) {
			t.Fatal("explicit selector detected")
			return 0, nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestHeadlessCredentialStorage_Scenario4_LegacyKeyringPin(t *testing.T) {
	root := filepath.Join(t.TempDir(), "root")
	registry, err := OpenRegistry(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Upsert(Connection{Identity: identity("example.test:443")}); err != nil {
		t.Fatal(err)
	}
	selection, err := ResolveCredentialStore(t.Context(), root, CredentialStoreAuto)
	if err != nil || selection.Backend != CredentialBackendKeyring || !selection.NewlyPinned {
		t.Fatalf("legacy pin: %#v %v", selection, err)
	}
	if _, err := os.Stat(filepath.Join(root, "clientauth-plaintext")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("legacy created plaintext state")
	}
	if _, err := ResolveCredentialStore(t.Context(), root, CredentialStoreFile); err == nil {
		t.Fatal("legacy pin downgraded")
	}
}

func TestHeadlessCredentialStorage_Scenario2_IdentityAndRotation(t *testing.T) {
	root := filepath.Join(t.TempDir(), "root")
	sel, err := ResolveCredentialStore(t.Context(), root, CredentialStoreFile)
	if err != nil {
		t.Fatal(err)
	}
	store, err := OpenCredentialStore(t.Context(), root, sel.Backend)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	creds, err := NewCredentials(store)
	if err != nil {
		t.Fatal(err)
	}
	id := identity("example.test:443")
	first, err := creds.Save(t.Context(), id, Token{AccessToken: "first", RefreshToken: "refresh", TokenType: "Bearer"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := creds.Save(t.Context(), id, Token{AccessToken: "second", RefreshToken: "rotated", TokenType: "Bearer"}, &first.Version)
	if err != nil {
		t.Fatal(err)
	}
	if err := creds.Delete(t.Context(), id, first.Version); !errors.Is(err, credentialstore.ErrConflict) {
		t.Fatal("stale logout removed rotated credential")
	}
	if second.Version.Equal(first.Version) {
		t.Fatal("rotation reused version")
	}
	corruptEncryptedCredential(t, root)
	if _, err := creds.replaceUnusable(t.Context(), id, Token{AccessToken: "repaired", TokenType: "Bearer"}); err != nil {
		t.Fatal(err)
	}
}

func TestHeadlessCredentialStorage_Scenario1_PersistBeforeOAuth(t *testing.T) {
	root := filepath.Join(t.TempDir(), "root")
	var wg sync.WaitGroup
	var mu sync.Mutex
	pins := 0
	for range 8 {
		wg.Go(func() {
			sel, err := ResolveCredentialStore(t.Context(), root, CredentialStoreFile)
			if err != nil {
				t.Error(err)
				return
			}
			if sel.Backend != CredentialBackendFile {
				t.Error("wrong backend")
			}
			if sel.NewlyPinned {
				mu.Lock()
				pins++
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	if pins != 1 {
		t.Fatalf("new pins = %d", pins)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := ResolveCredentialStore(ctx, root, CredentialStoreAuto); !errors.Is(err, context.Canceled) {
		t.Fatal("cancel not honored")
	}
	sel, err := ResolveCredentialStore(t.Context(), root, CredentialStoreAuto)
	if err != nil || sel.NewlyPinned || sel.Backend != CredentialBackendFile {
		t.Fatal("pin not retained")
	}
}

func TestHeadlessCredentialStorage_Scenario2_NoImplicitMigration(t *testing.T) {
	root := filepath.Join(t.TempDir(), "root")
	sel, err := ResolveCredentialStore(t.Context(), root, CredentialStoreFile)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveCredentialStore(t.Context(), root, CredentialStoreKeyring); err == nil {
		t.Fatal("conflicting backend accepted")
	}
	store, err := OpenCredentialStore(t.Context(), root, sel.Backend)
	if err != nil {
		t.Fatal(err)
	}
	rec, err := store.Put(t.Context(), []byte("fixture-key"), []byte("fixture-value"), nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = store.Close()
	store, backend, err := OpenExistingCredentialStore(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	if backend != CredentialBackendFile {
		t.Fatal("pin changed")
	}
	got, err := store.Get(t.Context(), []byte("fixture-key"))
	if err != nil || !got.Version.Equal(rec.Version) {
		t.Fatal("credential not retained")
	}
}

func TestHeadlessCredentialStorage_Scenario4_FailClosedLegacyEvidence(t *testing.T) {
	for _, data := range []string{`{}`, `{"version":2,"backend":"file"}`, `{"version":1,"backend":"unknown"}`, `{"version":1,"backend":"file","extra":0}`, `{"version":1,"backend":"file","backend":"file"}`} {
		root := filepath.Join(t.TempDir(), "root")
		if err := os.Mkdir(root, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "clientauth-credential-backend.json"), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := ResolveCredentialStore(t.Context(), root, CredentialStoreFile); err == nil {
			t.Fatal("invalid metadata accepted")
		}
	}
	root := filepath.Join(t.TempDir(), "root")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "clientauth-connections.json"), []byte(`{"version":1,"connections":[{}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveCredentialStore(t.Context(), root, CredentialStoreFile); err == nil {
		t.Fatal("invalid legacy evidence accepted")
	}
	if _, err := os.Stat(filepath.Join(root, "clientauth-credential-backend.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("pin written on invalid evidence")
	}
}

func TestHeadlessCredentialStorage_Scenario1_NoFallback(t *testing.T) {
	root := filepath.Join(t.TempDir(), "root")
	sel, err := ResolveCredentialStore(t.Context(), root, CredentialStoreFile)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := OpenExistingCredentialStore(t.Context(), root); err == nil {
		t.Fatal("missing pinned store silently initialized")
	}
	if _, err := OpenCredentialStore(t.Context(), root, CredentialBackendKeyring); err == nil {
		t.Fatal("opener bypassed pin")
	}
	s, err := OpenCredentialStore(t.Context(), root, sel.Backend)
	if err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	if err := os.Chmod(filepath.Join(root, "clientauth-plaintext"), 0755); err != nil {
		t.Fatal(err)
	}
	if _, _, err := OpenExistingCredentialStore(t.Context(), root); !errors.Is(err, credentialstore.ErrUnavailable) {
		t.Fatal("unsafe store accepted")
	}
}
