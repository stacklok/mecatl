//go:build linux || darwin

package clientauth

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stacklok/mecatl/internal/adapter/credentialstore"
)

func lifecycleStore(t *testing.T, root, backend string, existing bool) credentialstore.Store {
	t.Helper()
	if backend == "file" {
		if existing {
			s, _, err := OpenExistingCredentialStore(t.Context(), root)
			if err != nil {
				t.Fatal(err)
			}
			return s
		}
		if _, err := ResolveCredentialStore(t.Context(), root, CredentialStoreFile); err != nil {
			t.Fatal(err)
		}
		s, err := OpenCredentialStore(t.Context(), root, CredentialBackendFile)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	// Exercise the production encrypted route with injected fixture key material;
	// never consult the real desktop keyring, including in the subprocess.
	keys := fakeKeys{key: bytes.Repeat([]byte{7}, 32)}
	if existing {
		s, err := OpenExistingStore(t.Context(), root, keys)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	if _, err := ResolveCredentialStore(t.Context(), root, CredentialStoreKeyring); err != nil {
		t.Fatal(err)
	}
	s, err := OpenStore(t.Context(), root, keys)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestBackendLifecycleProcess(t *testing.T) {
	root := os.Getenv("MECATL_TEST_LIFECYCLE_ROOT")
	if root == "" {
		return
	}
	store := lifecycleStore(t, root, os.Getenv("MECATL_TEST_LIFECYCLE_BACKEND"), true)
	defer func() { _ = store.Close() }()
	creds, err := NewCredentials(store)
	if err != nil {
		t.Fatal(err)
	}
	id := identity("remote.example:443")
	id.Issuer = os.Getenv("MECATL_TEST_LIFECYCLE_ISSUER")
	before, err := creds.Load(t.Context(), id)
	if err != nil {
		t.Fatal("child load failed")
	}
	fmt.Println("ready")
	if _, err := io.ReadFull(os.Stdin, make([]byte, 1)); err != nil {
		t.Fatal(err)
	}
	if err := creds.Delete(t.Context(), id, before.Version); !errors.Is(err, credentialstore.ErrConflict) {
		t.Fatal("stale child delete did not conflict")
	}
	after, err := creds.Load(t.Context(), id)
	if err != nil || after.Token.RefreshToken != "refresh-rotated" || after.Version.Equal(before.Version) {
		t.Fatal("child did not observe persisted rotation")
	}
}

func TestHeadlessCredentialStorage_Scenario2_BackendLifecycleMatrix(t *testing.T) {
	for _, backend := range []string{"file", "keyring"} {
		t.Run(backend, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "root")
			store := lifecycleStore(t, root, backend, false)
			defer func() { _ = store.Close() }()
			creds, err := NewCredentials(store)
			if err != nil {
				t.Fatal(err)
			}
			f := newRefreshFixture(t)
			id := identity("remote.example:443")
			id.Issuer = f.srv.URL
			original := Token{AccessToken: f.token(t, "original", time.Now().Add(time.Minute)), RefreshToken: "refresh-original", TokenType: "Bearer", Expiry: time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)}
			if _, err := creds.Save(t.Context(), id, original, nil); err != nil {
				t.Fatal(err)
			}
			alias := id
			alias.Target = "REMOTE.EXAMPLE:0443"
			if _, err := creds.Load(t.Context(), alias); err != nil {
				t.Fatal("canonical alias missed credential")
			}
			other := id
			other.Target = "other.example:443"
			if _, err := creds.Load(t.Context(), other); !errors.Is(err, credentialstore.ErrNotFound) {
				t.Fatal("identity crossed target")
			}
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			child := exec.CommandContext(ctx, executable, "-test.run=^TestBackendLifecycleProcess$")
			child.Env = append(os.Environ(), "MECATL_TEST_LIFECYCLE_ROOT="+root, "MECATL_TEST_LIFECYCLE_BACKEND="+backend, "MECATL_TEST_LIFECYCLE_ISSUER="+id.Issuer, "GORACE=atexit_sleep_ms=0")
			stdout, err := child.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			stdin, err := child.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := child.Start(); err != nil {
				t.Fatal(err)
			}
			joined := false
			defer func() {
				if !joined {
					cancel()
					_ = child.Wait()
				}
			}()
			ready, err := bufio.NewReader(stdout).ReadString('\n')
			if err != nil || ready != "ready\n" {
				t.Fatal("child failed before loading stale version")
			}
			source := f.source(t, creds, id, 0)
			token, err := source.Token(t.Context())
			if err != nil {
				_ = source.Close()
				t.Fatal("refresh failed")
			}
			if err := source.Close(); err != nil {
				t.Fatal(err)
			}
			loaded, err := creds.Load(t.Context(), id)
			if err != nil || loaded.Token.AccessToken != token || loaded.Token.RefreshToken != "refresh-rotated" {
				t.Fatal("rotation not persisted")
			}
			if _, err := stdin.Write([]byte{1}); err != nil {
				t.Fatal(err)
			}
			_ = stdin.Close()
			err = child.Wait()
			joined = true
			if err != nil {
				t.Fatal("stale subprocess mutation failed")
			}
			corruptEncryptedCredential(t, root)
			if _, err := creds.Load(t.Context(), id); !errors.Is(err, ErrCorrupt) {
				t.Fatal("corrupt record not classified as unusable")
			}
			repaired, err := creds.replaceUnusable(t.Context(), id, loaded.Token)
			if err != nil {
				t.Fatal("corrupt credential repair failed")
			}
			if err := creds.Delete(t.Context(), id, repaired.Version); err != nil {
				t.Fatal(err)
			}
			if _, err := creds.Load(t.Context(), id); !errors.Is(err, credentialstore.ErrNotFound) {
				t.Fatal("logout left credential")
			}
		})
	}
}
