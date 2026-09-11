package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/adrg/xdg"
	"github.com/golang-jwt/jwt/v5"

	"github.com/stacklok/mecatl/internal/adapter/clientauth"
	"github.com/stacklok/mecatl/internal/adapter/credentialstore"
	"github.com/stacklok/mecatl/mcp/oauthlogin"
)

func TestFileCredentialProductionRoutes(t *testing.T) {
	oldHome, oldRuntime := xdg.ConfigHome, newRemoteLoginRuntime
	xdg.ConfigHome = t.TempDir()
	t.Cleanup(func() { xdg.ConfigHome, newRemoteLoginRuntime = oldHome, oldRuntime })
	var refreshed string
	var exchanges atomic.Int32
	issuer := newOIDCTestIssuer(t, "fixture", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil || r.Form.Get("grant_type") != "refresh_token" || r.Form.Get("refresh_token") != "original-refresh" {
			t.Error("unexpected refresh request")
			http.Error(w, "invalid fixture request", http.StatusBadRequest)
			return
		}
		exchanges.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": refreshed, "refresh_token": "rotated-refresh", "token_type": "Bearer", "expires_in": 3600})
	}))
	sign := func(expiry time.Time) string {
		tok := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{"iss": issuer.Server.URL, "sub": "fixture-user", "aud": "api", "exp": expiry.Unix(), "iat": time.Now().Add(-time.Hour).Unix()})
		tok.Header["kid"] = "fixture"
		signed, err := tok.SignedString(issuer.Key)
		if err != nil {
			t.Fatal(err)
		}
		return signed
	}
	refreshed = sign(time.Now().Add(time.Hour))
	ca := filepath.Join(xdg.ConfigHome, "issuer-ca.pem")
	issuer.writeCA(t, ca)
	root := filepath.Join(xdg.ConfigHome, "mecatl")
	if _, err := clientauth.ResolveCredentialStore(t.Context(), root, clientauth.CredentialStoreFile); err != nil {
		t.Fatal(err)
	}
	store, err := clientauth.OpenCredentialStore(t.Context(), root, clientauth.CredentialBackendFile)
	if err != nil {
		t.Fatal(err)
	}
	creds, err := clientauth.NewCredentials(store)
	if err != nil {
		t.Fatal(err)
	}
	conn := clientauth.Connection{Identity: clientauth.Identity{Target: "example.test:443", Issuer: issuer.Server.URL, ClientID: "client", Audience: "api", RedirectURI: oauthlogin.ExactRedirectURL}, IssuerCAFile: ca, IssuerAddressPolicy: clientauth.IssuerAddressPolicyPrivate}
	initial, err := creds.Save(t.Context(), conn.Identity, clientauth.Token{AccessToken: sign(time.Now().Add(-time.Minute)), RefreshToken: "original-refresh", TokenType: "Bearer", Expiry: time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	registry, err := clientauth.OpenRegistry(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Upsert(conn); err != nil {
		t.Fatal(err)
	}
	marker, err := os.ReadFile(filepath.Join(root, "clientauth-credential-backend.json"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := config{transportMode: modeConnect, connectAddress: conn.Identity.Target, useTLS: true}
	target, dial, closeTransport, err := resolveTransport(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer closeTransport()
	if target != conn.Identity.Target || dial.TokenSource == nil || !dial.UseTLS {
		t.Fatal("connect did not resolve saved file credential")
	}
	token, err := dial.TokenSource.Token(t.Context())
	if err != nil || token != refreshed || exchanges.Load() != 1 {
		t.Fatal("connect token source did not refresh through fixture")
	}
	closeTransport()
	store, backend, err := clientauth.OpenExistingCredentialStore(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	creds, err = clientauth.NewCredentials(store)
	if err != nil {
		t.Fatal(err)
	}
	rotated, err := creds.Load(t.Context(), conn.Identity)
	if err != nil || backend != clientauth.CredentialBackendFile || rotated.Token.AccessToken != refreshed || rotated.Token.RefreshToken != "rotated-refresh" || rotated.Version.Equal(initial.Version) {
		t.Fatal("connect rotation was not persisted in file backend")
	}
	_ = store.Close()
	calls := 0
	sentinel := errors.New("stop at reauth OAuth boundary")
	newRemoteLoginRuntime = func(oauthlogin.Options) (*oauthlogin.Runtime, error) { calls++; return nil, sentinel }
	if err := runExistingSavedRemoteLogin(t.Context(), conn, true); !errors.Is(err, sentinel) || calls != 1 {
		t.Fatal("reauth did not open existing file storage before OAuth")
	}
	plain := filepath.Join(root, "clientauth-plaintext")
	saved := filepath.Join(root, "saved-plaintext")
	if err := os.Rename(plain, saved); err != nil {
		t.Fatal(err)
	}
	if err := runExistingSavedRemoteLogin(t.Context(), conn, true); err == nil || errors.Is(err, sentinel) || calls != 1 {
		t.Fatal("missing existing-only store reached OAuth")
	}
	if _, err := os.Stat(plain); !os.IsNotExist(err) {
		t.Fatal("reauth recreated missing store")
	}
	if err := os.Rename(saved, plain); err != nil {
		t.Fatal(err)
	}
	if err := runRemoteLogout(conn.Identity.Target, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Find(conn.Identity.Target); !errors.Is(err, credentialstore.ErrNotFound) {
		t.Fatal("logout retained registry row")
	}
	store, backend, err = clientauth.OpenExistingCredentialStore(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	creds, err = clientauth.NewCredentials(store)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := creds.Load(t.Context(), conn.Identity); !errors.Is(err, credentialstore.ErrNotFound) {
		t.Fatal("logout retained credential")
	}
	after, err := os.ReadFile(filepath.Join(root, "clientauth-credential-backend.json"))
	if err != nil || string(after) != string(marker) || backend != clientauth.CredentialBackendFile {
		t.Fatal("logout changed backend pin")
	}
}
