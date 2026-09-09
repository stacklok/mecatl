package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/adrg/xdg"

	"github.com/stacklok/mecatl/internal/adapter/clientauth"
	"github.com/stacklok/mecatl/mcp/oauthlogin"
)

func TestHeadlessCredentialStorage_Scenario3_TruthfulNotice(t *testing.T) {
	oldHome, oldStderr, oldRuntime := xdg.ConfigHome, os.Stderr, newRemoteLoginRuntime
	xdg.ConfigHome = t.TempDir()
	out, err := os.CreateTemp(t.TempDir(), "notice")
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = out
	t.Cleanup(func() {
		xdg.ConfigHome, os.Stderr, newRemoteLoginRuntime = oldHome, oldStderr, oldRuntime
		_ = out.Close()
	})
	stop := errors.New("stop before OAuth")
	calls := 0
	newRemoteLoginRuntime = func(oauthlogin.Options) (*oauthlogin.Runtime, error) {
		calls++
		root := filepath.Join(xdg.ConfigHome, "mecatl")
		data, err := os.ReadFile(filepath.Join(root, "clientauth-credential-backend.json"))
		if err != nil || string(data) != `{"version":1,"backend":"file"}` {
			t.Fatal("file pin not durable at OAuth boundary")
		}
		store, backend, err := clientauth.OpenExistingCredentialStore(t.Context(), root)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = store.Close() }()
		if backend != clientauth.CredentialBackendFile {
			t.Fatal("OAuth boundary reopened another backend")
		}
		return nil, stop
	}
	conn := clientauth.Connection{Identity: clientauth.Identity{Target: "example.test:443", Issuer: "https://issuer.test", ClientID: "client", Audience: "api", RedirectURI: oauthlogin.ExactRedirectURL}}
	for _, mode := range []clientauth.CredentialStoreMode{clientauth.CredentialStoreFile, clientauth.CredentialStoreAuto} {
		if err := runSelectedRemoteLogin(t.Context(), conn, true, mode); !errors.Is(err, stop) {
			t.Fatal(err)
		}
	}
	data, err := os.ReadFile(out.Name())
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "Using file-backed credential storage (owner-only permissions).\n" {
		t.Fatalf("unexpected notice %q", data)
	}
	if err := runSelectedRemoteLogin(t.Context(), conn, true, clientauth.CredentialStoreKeyring); err == nil || errors.Is(err, stop) {
		t.Fatal("conflict reached OAuth")
	}
	if calls != 2 {
		t.Fatalf("OAuth boundary calls = %d, want 2", calls)
	}
}

func TestHeadlessCredentialStorage_Scenario5_NegativePaths(t *testing.T) {
	oldHome, oldRuntime := xdg.ConfigHome, newRemoteLoginRuntime
	xdg.ConfigHome = t.TempDir()
	calls := 0
	newRemoteLoginRuntime = func(oauthlogin.Options) (*oauthlogin.Runtime, error) {
		calls++
		return nil, errors.New("unexpected OAuth boundary")
	}
	t.Cleanup(func() { xdg.ConfigHome, newRemoteLoginRuntime = oldHome, oldRuntime })
	root := filepath.Join(xdg.ConfigHome, "mecatl")
	if err := os.Mkdir(root, 0755); err != nil {
		t.Fatal(err)
	}
	err := runSelectedRemoteLogin(t.Context(), clientauth.Connection{}, true, clientauth.CredentialStoreFile)
	if err == nil || !strings.Contains(err.Error(), "unsafe") {
		t.Fatalf("unsafe root: %v", err)
	}
	if calls != 0 {
		t.Fatal("unsafe root reached OAuth")
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatal("unsafe login created state")
	}
}

func TestHeadlessCredentialStorage_Scenario2_PinnedLifecycle(t *testing.T) {
	old := executeRemoteLogin
	t.Cleanup(func() { executeRemoteLogin = old })
	sentinel := errors.New("stop before OAuth")
	executeRemoteLogin = func(_ context.Context, _ clientauth.Connection, _ bool, mode clientauth.CredentialStoreMode) error {
		if mode != clientauth.CredentialStoreFile {
			t.Fatal("selector not forwarded")
		}
		return sentinel
	}
	err := runRemoteLogin("example.test:443", []string{"--credential-store=file", "--issuer=https://issuer.test", "--client-id=client", "--audience=api"})
	if !errors.Is(err, sentinel) {
		t.Fatalf("login selector: %v", err)
	}
	if err := runRemoteLogout("example.test:443", []string{"--credential-store=file"}); err == nil {
		t.Fatal("logout exposes selector")
	}
}
