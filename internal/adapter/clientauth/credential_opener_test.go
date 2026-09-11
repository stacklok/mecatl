//go:build linux || darwin

package clientauth

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

type failingCredentialKeyring struct{ calls int }

func (k *failingCredentialKeyring) Get(string, string) (string, error) {
	k.calls++
	return "", errors.New("fixture keyring unavailable")
}
func (k *failingCredentialKeyring) Set(string, string, string) error {
	k.calls++
	return errors.New("fixture keyring unavailable")
}

func TestCredentialOpenersNeverFallback(t *testing.T) {
	old := credentialKeyringBackend
	spy := &failingCredentialKeyring{}
	credentialKeyringBackend = spy
	t.Cleanup(func() { credentialKeyringBackend = old })
	for _, mode := range []CredentialStoreMode{CredentialStoreFile, CredentialStoreKeyring} {
		t.Run(string(mode), func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "root")
			selection, err := ResolveCredentialStore(t.Context(), root, mode)
			if err != nil {
				t.Fatal(err)
			}
			before := spy.calls
			store, err := OpenCredentialStore(t.Context(), root, selection.Backend)
			if mode == CredentialStoreFile {
				if err != nil {
					t.Fatal(err)
				}
				_ = store.Close()
				store, backend, err := OpenExistingCredentialStore(t.Context(), root)
				if err != nil {
					t.Fatal(err)
				}
				_ = store.Close()
				if backend != CredentialBackendFile || spy.calls != before {
					t.Fatal("file opener contacted keyring")
				}
			} else {
				if err == nil {
					_ = store.Close()
					t.Fatal("explicit keyring failure was hidden")
				}
				if spy.calls == before {
					t.Fatal("test did not reach real keyring boundary")
				}
				before = spy.calls
				store, backend, err := OpenExistingCredentialStore(t.Context(), root)
				if err == nil {
					_ = store.Close()
					t.Fatal("pinned keyring failure was hidden")
				}
				if backend != CredentialBackendKeyring || spy.calls == before {
					t.Fatal("pinned opener did not reach keyring")
				}
				if _, err := os.Lstat(filepath.Join(root, "clientauth-plaintext")); !os.IsNotExist(err) {
					t.Fatal("failed keyring created plaintext")
				}
			}
		})
	}
}
