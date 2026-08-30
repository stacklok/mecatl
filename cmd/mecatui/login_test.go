package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/adrg/xdg"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/internal/adapter/clientauth"
	"github.com/stacklok/mecatl/mcp/oauthlogin"
)

func TestRemoteLoginStoresAbsoluteIssuerCAReferenceAcrossCWDChanges(t *testing.T) {
	oldConfigHome := xdg.ConfigHome
	xdg.ConfigHome = t.TempDir()
	t.Cleanup(func() { xdg.ConfigHome = oldConfigHome })
	first := t.TempDir()
	second := t.TempDir()
	ca := filepath.Join(first, "issuer-ca.pem")
	if err := os.WriteFile(ca, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWD) })
	if err := os.Chdir(first); err != nil {
		t.Fatal(err)
	}
	original := executeRemoteLogin
	t.Cleanup(func() { executeRemoteLogin = original })
	executeRemoteLogin = func(_ context.Context, conn clientauth.Connection, _ bool) error {
		registry, openErr := clientauth.OpenRegistry(filepath.Join(xdg.ConfigHome, "mecatl"))
		if openErr != nil {
			return openErr
		}
		_, upsertErr := registry.Upsert(conn)
		return upsertErr
	}
	if err := runRemoteLogin("remote.example:443", []string{
		"--issuer", "https://issuer.example", "--client-id", "client", "--audience", "audience", "--tls-ca", "issuer-ca.pem",
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(second); err != nil {
		t.Fatal(err)
	}
	conn, err := savedConnection("remote.example:443")
	if err != nil {
		t.Fatal(err)
	}
	physicalCA, err := filepath.EvalSymlinks(ca)
	if err != nil {
		t.Fatal(err)
	}
	if conn.IssuerCAFile != physicalCA || !filepath.IsAbs(conn.IssuerCAFile) {
		t.Fatalf("saved issuer CA = %q, want %q", conn.IssuerCAFile, physicalCA)
	}
}

func TestRemoteLoginWiresExactRedirectURL(t *testing.T) {
	t.Run("command identity", func(t *testing.T) {
		original := executeRemoteLogin
		t.Cleanup(func() { executeRemoteLogin = original })
		marker := errors.New("stop after option capture")
		executeRemoteLogin = func(_ context.Context, conn clientauth.Connection, _ bool) error {
			if conn.Identity.RedirectURI != oauthlogin.ExactRedirectURL {
				t.Fatalf("redirect URI = %q, want exact callback", conn.Identity.RedirectURI)
			}
			return marker
		}
		err := runRemoteLogin("https://mecated.example", []string{
			"--issuer", "https://issuer.example",
			"--client-id", "client",
			"--audience", "audience",
			"--tls-ca", "ca.pem",
		})
		if !errors.Is(err, marker) {
			t.Fatalf("runRemoteLogin error = %v", err)
		}
	})

	t.Run("callback runtime", func(t *testing.T) {
		original := newRemoteLoginRuntime
		originalPrepare := prepareSavedLogin
		t.Cleanup(func() { newRemoteLoginRuntime = original; prepareSavedLogin = originalPrepare })
		prepareSavedLogin = func(context.Context, clientauth.Connection) (preparedSavedLogin, error) {
			return preparedSavedLogin{close: func() {}}, nil
		}
		marker := errors.New("stop after runtime option capture")
		newRemoteLoginRuntime = func(opts oauthlogin.Options) (*oauthlogin.Runtime, error) {
			if opts.RedirectURL != oauthlogin.ExactRedirectURL {
				t.Fatalf("runtime redirect URL = %q, want exact callback", opts.RedirectURL)
			}
			return nil, marker
		}
		caFile := filepath.Join(t.TempDir(), "ca.pem")
		if err := os.WriteFile(caFile, []byte("not parsed before runtime construction"), 0o600); err != nil {
			t.Fatal(err)
		}
		err := runSavedRemoteLogin(context.Background(), clientauth.Connection{
			Identity:     clientauth.Identity{Issuer: "https://issuer.example"},
			IssuerCAFile: caFile,
		}, false)
		if !errors.Is(err, marker) {
			t.Fatalf("runSavedRemoteLogin error = %v", err)
		}
	})
}

func TestSavedRemoteLoginPreflightsLocalStorageBeforeRuntime(t *testing.T) {
	originalRuntime := newRemoteLoginRuntime
	t.Cleanup(func() { newRemoteLoginRuntime = originalRuntime })
	opened := false
	newRemoteLoginRuntime = func(oauthlogin.Options) (*oauthlogin.Runtime, error) {
		opened = true
		return nil, errors.New("runtime must not open")
	}
	originalPrepare := prepareSavedLogin
	t.Cleanup(func() { prepareSavedLogin = originalPrepare })
	prepareSavedLogin = func(context.Context, clientauth.Connection) (preparedSavedLogin, error) {
		return preparedSavedLogin{}, &client.AuthError{Reason: client.AuthStorageUnavailable}
	}

	caFile := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(caFile, []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := runSavedRemoteLogin(t.Context(), clientauth.Connection{IssuerCAFile: caFile}, false)
	var authErr *client.AuthError
	if !errors.As(err, &authErr) || authErr.Reason != client.AuthStorageUnavailable {
		t.Fatalf("preflight error = %v, want storage unavailable", err)
	}
	if opened {
		t.Fatal("OIDC runtime opened before local credential preflight")
	}
}
