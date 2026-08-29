package clientauth

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zalando/go-keyring"

	"github.com/stacklok/mecatl/internal/adapter/credentialstore"
	"github.com/stacklok/mecatl/mcp/oauthlogin"
)

type fakeKeys struct {
	key []byte
	err error
}

func (f fakeKeys) StoreKey(context.Context) ([]byte, error)         { return f.key, f.err }
func (f fakeKeys) ExistingStoreKey(context.Context) ([]byte, error) { return f.key, f.err }

type memoryKeyring struct {
	mu       sync.Mutex
	values   map[string]string
	setCount int
}

func newMemoryKeyring() *memoryKeyring { return &memoryKeyring{values: make(map[string]string)} }
func (m *memoryKeyring) Get(_, account string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	value, ok := m.values[account]
	if !ok {
		return "", keyring.ErrNotFound
	}
	return value, nil
}
func (m *memoryKeyring) Set(_, account, value string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.values[account] = value
	m.setCount++
	return nil
}
func (m *memoryKeyring) put(account string, key []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.values[account] = base64.RawStdEncoding.EncodeToString(key)
}
func (m *memoryKeyring) value(account string) (string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	value, ok := m.values[account]
	return value, ok
}

func TestRootScopedKeyringConcurrentOpensConverge(t *testing.T) {
	// Distinct providers own distinct flock handles to the same root-local file.
	// gofrs/flock implements these handles with an OS advisory lock, so this is the
	// same contention path used by separate processes without a subprocess harness.
	root := t.TempDir()
	backend := newMemoryKeyring()
	first, err := newKeyringProvider(root, backend)
	if err != nil {
		t.Fatal(err)
	}
	second, err := newKeyringProvider(root, backend)
	if err != nil {
		t.Fatal(err)
	}
	providers := []*KeyringProvider{first, second}
	keys := make([][]byte, len(providers))
	errs := make([]error, len(providers))
	var wg sync.WaitGroup
	for i := range providers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			store, openErr := OpenStore(t.Context(), root, providers[i])
			if openErr == nil {
				_ = store.Close()
			}
			errs[i] = openErr
			keys[i], _ = providers[i].ExistingStoreKey(t.Context())
		}()
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if string(keys[0]) != string(keys[1]) || backend.setCount != 1 {
		t.Fatalf("keys converged = %v, keyring writes = %d", string(keys[0]) == string(keys[1]), backend.setCount)
	}
}

func TestRootScopedKeyringDomainsAreIndependent(t *testing.T) {
	backend := newMemoryKeyring()
	oneRoot, twoRoot := t.TempDir(), t.TempDir()
	one, err := newKeyringProvider(oneRoot, backend)
	if err != nil {
		t.Fatal(err)
	}
	two, err := newKeyringProvider(twoRoot, backend)
	if err != nil {
		t.Fatal(err)
	}
	oneKey, err := one.StoreKey(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	twoKey, err := two.StoreKey(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if one.account == two.account || string(oneKey) == string(twoKey) {
		t.Fatal("independent roots shared an account or key")
	}
	if strings.Contains(one.account, oneRoot) || strings.Contains(two.account, twoRoot) {
		t.Fatal("keyring account disclosed a raw store path")
	}
}

func TestRootScopedKeyringMigratesLegacyAndPreservesCredentials(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	backend := newMemoryKeyring()
	legacy := bytesOf(7)
	backend.put(legacyKeyringAccount, legacy)

	oldStore, err := credentialstore.NewEncryptedFile(root, credentialNamespace, legacy)
	if err != nil {
		t.Fatal(err)
	}
	oldCreds, _ := NewCredentials(oldStore)
	id := identity("legacy.example:443")
	if _, err := oldCreds.Upsert(t.Context(), id, Token{AccessToken: "still-readable", TokenType: "Bearer"}); err != nil {
		t.Fatal(err)
	}
	_ = oldStore.Close()

	provider, err := newKeyringProvider(root, backend)
	if err != nil {
		t.Fatal(err)
	}
	store, err := OpenExistingStore(t.Context(), root, provider)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	creds, _ := NewCredentials(store)
	record, err := creds.Load(t.Context(), id)
	if err != nil || record.Token.AccessToken != "still-readable" {
		t.Fatalf("migrated credential = %#v, %v", record, err)
	}
	if _, ok := backend.value(legacyKeyringAccount); !ok {
		t.Fatal("legacy keyring entry was removed")
	}
	if got, ok := backend.value(provider.account); !ok || got != base64.RawStdEncoding.EncodeToString(legacy) {
		t.Fatal("root keyring entry was not copied from legacy")
	}
}

func TestRootScopedKeyringRootKeyWinsAndCorruptionFailsClosed(t *testing.T) {
	root := t.TempDir()
	backend := newMemoryKeyring()
	provider, err := newKeyringProvider(root, backend)
	if err != nil {
		t.Fatal(err)
	}
	rootKey, legacy := bytesOf(1), bytesOf(2)
	backend.put(provider.account, rootKey)
	backend.put(legacyKeyringAccount, legacy)
	got, err := provider.StoreKey(t.Context())
	if err != nil || string(got) != string(rootKey) {
		t.Fatalf("root key = %x, %v", got, err)
	}
	backend.mu.Lock()
	backend.values[provider.account] = "corrupt"
	backend.mu.Unlock()
	if _, err := provider.StoreKey(t.Context()); !errors.Is(err, ErrKeyUnavailable) {
		t.Fatalf("corrupt root key error = %v", err)
	}
}

func TestRootScopedKeyringExistingOpenCreatesNoKey(t *testing.T) {
	root := t.TempDir()
	backend := newMemoryKeyring()
	provider, err := newKeyringProvider(root, backend)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := OpenExistingStore(t.Context(), root, provider); !errors.Is(err, ErrKeyUnavailable) {
		t.Fatalf("existing open error = %v", err)
	}
	if backend.setCount != 0 {
		t.Fatalf("existing open wrote %d keyring entries", backend.setCount)
	}
}

func TestEmptyRootLogoutWithLegacyGlobalKeyCreatesNoRootAccount(t *testing.T) {
	root := t.TempDir()
	backend := newMemoryKeyring()
	backend.put(legacyKeyringAccount, bytesOf(9))
	provider, err := newKeyringProvider(root, backend)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := OpenExistingStore(t.Context(), root, provider); !errors.Is(err, ErrKeyUnavailable) {
		t.Fatalf("empty-root existing open = %v", err)
	}
	registry, err := OpenRegistry(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Logout(t.Context(), "empty.example:443", LogoutConfig{Registry: registry}); err != nil {
		t.Fatal(err)
	}
	if _, ok := backend.value(provider.account); ok {
		t.Fatal("empty-root existing open created a root-scoped keyring account")
	}
}

func TestExistingEmptyLegacyNamespaceDoesNotMigrateGlobalKey(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	backend := newMemoryKeyring()
	legacy := bytesOf(9)
	backend.put(legacyKeyringAccount, legacy)
	store, err := credentialstore.NewEncryptedFile(root, credentialNamespace, legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	provider, err := newKeyringProvider(root, backend)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := OpenExistingStore(t.Context(), root, provider); !errors.Is(err, ErrKeyUnavailable) {
		t.Fatalf("empty namespace existing open = %v", err)
	}
	if _, ok := backend.value(provider.account); ok {
		t.Fatal("empty legacy namespace created a root-scoped keyring account")
	}
}

func TestReadOnlyLegacyNamespaceWithCredentialMigratesGlobalKey(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	backend := newMemoryKeyring()
	legacy := bytesOf(10)
	backend.put(legacyKeyringAccount, legacy)
	store, err := credentialstore.NewEncryptedFile(root, credentialNamespace, legacy)
	if err != nil {
		t.Fatal(err)
	}
	creds, err := NewCredentials(store)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := creds.Upsert(t.Context(), identity("read-only-legacy.example:443"), Token{AccessToken: "legacy", TokenType: "Bearer"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	provider, err := newKeyringProvider(root, backend)
	if err != nil {
		t.Fatal(err)
	}
	// Create the lock before making the root read-only; migration only needs to
	// inspect the namespace and copy keyring state.
	lockFile, err := os.OpenFile(filepath.Join(root, keyringLockName), os.O_CREATE|os.O_WRONLY, 0o600) // #nosec G304 -- test-owned path.
	if err != nil {
		t.Fatal(err)
	}
	if err := lockFile.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(root, 0o700) })
	got, err := provider.ExistingStoreKey(t.Context())
	if err != nil || !bytes.Equal(got, legacy) {
		t.Fatalf("read-only legacy migration = %x, %v", got, err)
	}
	if _, ok := backend.value(provider.account); !ok {
		t.Fatal("read-only legacy migration did not create the root-scoped keyring account")
	}
}

func TestRootScopedKeyringLockHonorsCancellation(t *testing.T) {
	root := t.TempDir()
	backend := newMemoryKeyring()
	holder, err := newKeyringProvider(root, backend)
	if err != nil {
		t.Fatal(err)
	}
	waiter, err := newKeyringProvider(root, backend)
	if err != nil {
		t.Fatal(err)
	}
	if err := holder.lock.Lock(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = holder.lock.Unlock() }()
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err = waiter.StoreKey(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("lock cancellation error = %v", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("lock cancellation took %s", elapsed)
	}
}

func bytesOf(value byte) []byte { return bytes.Repeat([]byte{value}, 32) }

func identity(target string) Identity {
	return Identity{Target: target, Issuer: "https://issuer.example", ClientID: "mecatui", Audience: "vmcp", RedirectURI: "https://app.example/callback", Scopes: []string{"openid", "profile"}}
}
func credentials(t *testing.T) *Credentials {
	t.Helper()
	b := credentialstore.NewMemoryBackend()
	s, err := b.Open("clientauth-test")
	if err != nil {
		t.Fatal(err)
	}
	c, err := NewCredentials(s)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

type corruptCredentialStore struct{ credentialstore.Store }

func (corruptCredentialStore) Get(context.Context, []byte) (credentialstore.Record, error) {
	return credentialstore.Record{}, credentialstore.ErrCorrupt
}

func TestBackendCorruptionIsClientauthCredentialUnusable(t *testing.T) {
	creds, err := NewCredentials(corruptCredentialStore{})
	if err != nil {
		t.Fatal(err)
	}
	id := identity("host.example:443")
	if _, err := creds.Load(t.Context(), id); !errors.Is(err, ErrCorrupt) || errors.Is(err, credentialstore.ErrCorrupt) {
		t.Fatalf("credential load corruption = %v", err)
	}

	f := newRefreshFixture(t)
	id.Issuer = f.srv.URL
	id, _ = id.Canonical()
	s := f.source(t, creds, id, time.Hour)
	defer s.Close()
	_, err = s.Token(t.Context())
	var required *LoginRequiredError
	if !errors.As(err, &required) || required.Cause != CredentialUnusable {
		t.Fatalf("refresh source error = %v, cause = %#v", err, required)
	}
}
func TestTargetIsCanonicalHostPort(t *testing.T) {
	id := identity("Mecak8s-Mecak8s.mecatl-vmcp.svc.cluster.local:18081")
	got, err := id.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	if got.Target != "mecak8s-mecak8s.mecatl-vmcp.svc.cluster.local:18081" {
		t.Fatalf("target = %q", got.Target)
	}
	if _, err := identity("https://mecak8s.example:18081").Canonical(); !errors.Is(err, ErrInvalidIdentity) {
		t.Fatalf("HTTPS target err = %v", err)
	}
}

func TestTargetCanonicalizesNumericPortAndRegistryLegacyIdentity(t *testing.T) {
	canonical, err := identity("host.example:0443").Canonical()
	if err != nil {
		t.Fatal(err)
	}
	if canonical.Target != "host.example:443" {
		t.Fatalf("canonical target = %q", canonical.Target)
	}

	r, err := OpenRegistry(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	legacy := Connection{Identity: identity("host.example:0443")}
	body, err := json.Marshal(registryFile{Version: 1, Connections: []Connection{legacy}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(r.path, body, 0600); err != nil {
		t.Fatal(err)
	}
	connections, err := r.List()
	if err != nil {
		t.Fatal(err)
	}
	if got := connections[0].Identity.Target; got != "host.example:443" {
		t.Fatalf("legacy registry target = %q", got)
	}
	if _, err := r.Upsert(legacy); err != nil {
		t.Fatal(err)
	}
	persisted, err := os.ReadFile(r.path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(persisted), ":0443") {
		t.Fatalf("next enrollment retained legacy target: %s", persisted)
	}
}

func TestLegacyPortCredentialRequiresReenrollment(t *testing.T) {
	c := credentials(t)
	legacy := identity("host.example:0443")
	body, err := json.Marshal(credentialEnvelope{Schema: envelopeSchema, Version: 1, Identity: legacy, Token: Token{AccessToken: "legacy", TokenType: "Bearer"}})
	if err != nil {
		t.Fatal(err)
	}
	identityBody, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(append([]byte("mecatl/clientauth/identity/v1\x00"), identityBody...))
	if _, err := c.store.Put(t.Context(), sum[:], body, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Load(t.Context(), identity("host.example:443")); !errors.Is(err, credentialstore.ErrNotFound) {
		t.Fatalf("legacy credential lookup = %v, want re-enrollment", err)
	}
	if _, err := c.Upsert(t.Context(), identity("host.example:0443"), Token{AccessToken: "new", TokenType: "Bearer"}); err != nil {
		t.Fatal(err)
	}
	if got, err := c.Load(t.Context(), identity("host.example:443")); err != nil || got.Token.AccessToken != "new" {
		t.Fatalf("canonical credential = %#v, %v", got, err)
	}
}

func TestCredentialsBindTargetAndCAS(t *testing.T) {
	c := credentials(t)
	a := identity("one.example:443")
	b := identity("two.example:443")
	first, err := c.Save(t.Context(), a, Token{AccessToken: "one", TokenType: "Bearer"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Load(t.Context(), b); !errors.Is(err, credentialstore.ErrNotFound) {
		t.Fatalf("cross target load = %v", err)
	}
	if _, err := c.Save(t.Context(), a, Token{AccessToken: "two", TokenType: "Bearer"}, nil); !errors.Is(err, credentialstore.ErrConflict) {
		t.Fatalf("unconditional replacement = %v", err)
	}
	second, err := c.Save(t.Context(), a, Token{AccessToken: "two", TokenType: "Bearer"}, &first.Version)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Delete(t.Context(), a, first.Version); !errors.Is(err, credentialstore.ErrConflict) {
		t.Fatalf("stale delete = %v", err)
	}
	if err := c.Delete(t.Context(), a, second.Version); err != nil {
		t.Fatal(err)
	}
}
func TestRegistryContainsNoTokens(t *testing.T) {
	r, err := OpenRegistry(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	id := identity("vmcp.example:443")
	if _, err := r.Upsert(Connection{Identity: id}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(r.path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "access_token") || strings.Contains(string(data), "secret-token") {
		t.Fatalf("registry contains credential: %s", data)
	}
	info, err := os.Stat(r.path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("registry mode = %o", info.Mode().Perm())
	}
}
func TestOpenStoreMissingKeyFailsClosed(t *testing.T) {
	_, err := OpenStore(t.Context(), t.TempDir(), fakeKeys{err: errors.New("no keyring")})
	if !errors.Is(err, ErrKeyUnavailable) {
		t.Fatalf("err = %v", err)
	}
}
func TestOpenExistingStoreDoesNotFallBackToKeyCreation(t *testing.T) {
	_, err := OpenExistingStore(t.Context(), t.TempDir(), fakeKeys{err: errors.New("absent")})
	if !errors.Is(err, ErrKeyUnavailable) {
		t.Fatalf("err = %v", err)
	}
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	store, err := OpenExistingStore(t.Context(), root, fakeKeys{key: make([]byte, 32)})
	if err != nil {
		t.Fatalf("open existing store: %v", err)
	}
	_ = store.Close()
}

func TestLoginDiscoveryEndpointErrorDoesNotReflectProviderURL(t *testing.T) {
	const providerMarker = "provider-controlled-endpoint-marker"
	var server *httptest.Server
	server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/.well-known/openid-configuration" {
			http.NotFound(w, r)
			return
		}
		_ = jsonNew(w, map[string]any{
			"issuer":                           server.URL,
			"authorization_endpoint":           "https://" + providerMarker + ".invalid/authorize",
			"token_endpoint":                   server.URL + "/token",
			"jwks_uri":                         server.URL + "/keys",
			"code_challenge_methods_supported": []string{"S256"},
		})
	}))
	defer server.Close()

	id := identity(strings.TrimPrefix(server.URL, "https://"))
	id.Issuer = server.URL
	_, err := Login(t.Context(), LoginConfig{Identity: id, HTTPClient: server.Client(), Presenter: PresenterFunc(func(context.Context, string) (oauthlogin.Result, error) {
		t.Fatal("presenter must not run after rejected discovery")
		return oauthlogin.Result{}, nil
	})})
	if !errors.Is(err, ErrDiscovery) {
		t.Fatalf("discovery error = %v", err)
	}
	if strings.Contains(err.Error(), providerMarker) {
		t.Fatalf("discovery error reflected provider endpoint: %q", err)
	}
	if !strings.Contains(err.Error(), "authorization_endpoint must be an HTTPS URL on the issuer's host") {
		t.Fatalf("discovery error = %q", err)
	}
}

func TestLoginUsesS256AndExchanges(t *testing.T) {
	var server *httptest.Server
	var gotAuth url.Values
	exchanged := false
	server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			_ = jsonNew(w, map[string]any{"issuer": server.URL, "authorization_endpoint": server.URL + "/authorize", "token_endpoint": server.URL + "/token", "jwks_uri": server.URL + "/keys", "code_challenge_methods_supported": []string{"S256"}})
		case "/token":
			if err := r.ParseForm(); err != nil {
				t.Fatal(err)
			}
			exchanged = r.Form.Get("code_verifier") != ""
			_ = jsonNew(w, map[string]any{"access_token": "not-a-jwt", "token_type": "Bearer"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	id := identity(strings.TrimPrefix(server.URL, "https://"))
	id.Issuer = server.URL
	_, err := Login(t.Context(), LoginConfig{Identity: id, HTTPClient: server.Client(), Presenter: PresenterFunc(func(_ context.Context, raw string) (oauthlogin.Result, error) {
		u, e := url.Parse(raw)
		if e != nil {
			return oauthlogin.Result{}, e
		}
		gotAuth = u.Query()
		return oauthlogin.Result{Code: "code", State: gotAuth.Get("state"), Iss: server.URL}, nil
	})})
	if err == nil {
		t.Fatal("Login unexpectedly succeeded with an invalid access token")
	}
	if gotAuth.Get("code_challenge_method") != "S256" || gotAuth.Get("code_challenge") == "" {
		t.Fatalf("PKCE params: %v", gotAuth)
	}
	if !exchanged {
		t.Fatal("token exchange did not include verifier")
	}
}
func jsonNew(w http.ResponseWriter, v any) error {
	w.Header().Set("Content-Type", "application/json")
	return json.NewEncoder(w).Encode(v)
}

// TestIssParameterIsRequiredOnlyWhenAdvertised pins RFC 9207 section 2.4. The
// callback listener rejects a mismatched iss; this layer holds the discovery
// document and so owns the required-if-advertised half. A server that never
// promised an iss (Dex, for one) must still be able to complete a login.
func TestIssParameterIsRequiredOnlyWhenAdvertised(t *testing.T) {
	tests := map[string]struct {
		advertised     bool
		callbackIss    func(issuer string) string
		wantAuthFailed bool
		wantMessage    string
	}{
		"not advertised, no iss": {
			advertised:  false,
			callbackIss: func(string) string { return "" },
		},
		"advertised, iss present": {
			advertised:  true,
			callbackIss: func(issuer string) string { return issuer },
		},
		"advertised, no iss": {
			advertised:     true,
			callbackIss:    func(string) string { return "" },
			wantAuthFailed: true,
			wantMessage:    "RFC 9207",
		},
		"not advertised, wrong iss": {
			advertised:     false,
			callbackIss:    func(string) string { return "https://other.example" },
			wantAuthFailed: true,
			wantMessage:    "iss did not match",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			var server *httptest.Server
			server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/.well-known/openid-configuration":
					doc := map[string]any{
						"issuer": server.URL, "authorization_endpoint": server.URL + "/authorize",
						"token_endpoint": server.URL + "/token", "jwks_uri": server.URL + "/keys",
						"code_challenge_methods_supported": []string{"S256"},
					}
					if tc.advertised {
						doc["authorization_response_iss_parameter_supported"] = true
					}
					_ = jsonNew(w, doc)
				case "/token":
					_ = jsonNew(w, map[string]any{"access_token": "not-a-jwt", "token_type": "Bearer"})
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			id := identity(strings.TrimPrefix(server.URL, "https://"))
			id.Issuer = server.URL
			_, err := Login(t.Context(), LoginConfig{Identity: id, HTTPClient: server.Client(),
				Presenter: PresenterFunc(func(_ context.Context, raw string) (oauthlogin.Result, error) {
					u, e := url.Parse(raw)
					if e != nil {
						return oauthlogin.Result{}, e
					}
					return oauthlogin.Result{Code: "code", State: u.Query().Get("state"), Iss: tc.callbackIss(server.URL)}, nil
				})})
			// Every case fails eventually: the fake access token is not a JWT. Only
			// the stage matters — ErrAuthorization means the iss gate rejected it.
			if got := errors.Is(err, ErrAuthorization); got != tc.wantAuthFailed {
				t.Fatalf("ErrAuthorization = %v, want %v (err = %v)", got, tc.wantAuthFailed, err)
			}
			if tc.wantMessage != "" && !strings.Contains(err.Error(), tc.wantMessage) {
				t.Fatalf("error %v does not mention %q", err, tc.wantMessage)
			}
		})
	}
}

// TestReEnrolmentReplacesAnExistingCredential pins that logging in again works.
// Save with a nil expected version is create-only, so enrolment used to succeed
// exactly once per target and every later login failed a CAS precondition —
// leaving no way to replace an expired or revoked credential.
func TestReEnrolmentReplacesAnExistingCredential(t *testing.T) {
	creds := credentials(t)
	id := identity("host.example:18081")

	first := Token{AccessToken: "first", TokenType: "Bearer", Expiry: "2026-01-01T00:00:00Z"}
	if _, err := creds.Upsert(t.Context(), id, first); err != nil {
		t.Fatalf("first enrolment: %v", err)
	}

	// The create-only path is exactly what broke: prove it still rejects, so the
	// test fails if someone "simplifies" Upsert back into a plain Save.
	if _, err := creds.Save(t.Context(), id, first, nil); err == nil {
		t.Fatal("Save with a nil version overwrote an existing record; it must stay create-only")
	}

	second := Token{AccessToken: "second", TokenType: "Bearer", Expiry: "2027-01-01T00:00:00Z"}
	if _, err := creds.Upsert(t.Context(), id, second); err != nil {
		t.Fatalf("re-enrolment: %v", err)
	}
	third := Token{AccessToken: "third", TokenType: "Bearer", Expiry: "2028-01-01T00:00:00Z"}
	if _, err := creds.Upsert(t.Context(), id, third); err != nil {
		t.Fatalf("third enrolment: %v", err)
	}

	got, err := creds.Load(t.Context(), id)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got.Token.AccessToken != "third" {
		t.Fatalf("stored token = %q, want the most recent", got.Token.AccessToken)
	}
}

// TestTokenSizeBoundIsTheTokenLimitNotTheIdentityLimit pins the bound a token
// value gets. safe() caps identity fields at 1024 and validToken used to apply it
// to the access token, which silently overrode the 16384 limit sitting beside it:
// a JWT with many group claims (Entra is the common case) could not be stored,
// and the failure surfaced as "credential storage unavailable".
func TestTokenSizeBoundIsTheTokenLimitNotTheIdentityLimit(t *testing.T) {
	creds := credentials(t)
	id := identity("host.example:18081")
	store := func(access, refresh string) error {
		_, err := creds.Upsert(t.Context(), id, Token{
			AccessToken: access, RefreshToken: refresh,
			TokenType: "Bearer", Expiry: "2026-01-01T00:00:00Z",
		})
		return err
	}

	// Over the old 1024 identity cap, well under the token limit: must store.
	if err := store(strings.Repeat("a", 4096), strings.Repeat("r", 4096)); err != nil {
		t.Fatalf("a 4096-byte token was rejected: %v", err)
	}
	if err := store(strings.Repeat("a", tokenValueLimit), ""); err != nil {
		t.Fatalf("a token at the limit was rejected: %v", err)
	}

	// The limit is still a limit, and the other invariants still hold.
	if err := store(strings.Repeat("a", tokenValueLimit+1), ""); err == nil {
		t.Fatal("a token over the limit was accepted")
	}
	if err := store("", ""); err == nil {
		t.Fatal("an empty access token was accepted")
	}
	if err := store("tok\r\nInjected: x", ""); err == nil {
		t.Fatal("a control character in the access token was accepted")
	}
	// The refresh token had only a length check before, so this is newly enforced.
	if err := store("tok", "ref\r\nInjected: x"); err == nil {
		t.Fatal("a control character in the refresh token was accepted")
	}
	// An absent refresh token is normal: no offline_access, or the provider
	// declined it.
	if err := store("tok", ""); err != nil {
		t.Fatalf("an empty refresh token was rejected: %v", err)
	}
}

// TestLogoutReconcilesLocalStateAndRevokesWithoutExposingTokens covers the
// complete local and provider-side path without allowing secret material into results.
func TestLogoutReconcilesLocalStateAndRevokesWithoutExposingTokens(t *testing.T) {
	root := t.TempDir()
	reg, err := OpenRegistry(root)
	if err != nil {
		t.Fatal(err)
	}
	creds := credentials(t)
	const target = "host.example:18081"
	id := identity(target)
	secret := "refresh-super-secret"
	access := "access-super-secret"
	var server *httptest.Server
	var revoked []string
	server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			_ = jsonNew(w, map[string]any{"issuer": server.URL, "revocation_endpoint": server.URL + "/revoke"})
		case "/revoke":
			if err := r.ParseForm(); err != nil {
				t.Fatal(err)
			}
			if r.Form.Get("client_id") != id.ClientID {
				t.Errorf("client_id = %q", r.Form.Get("client_id"))
			}
			revoked = append(revoked, r.Form.Get("token"))
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	id.Issuer = server.URL
	if _, err := reg.Upsert(Connection{Identity: id}); err != nil {
		t.Fatal(err)
	}
	if _, err := creds.Upsert(t.Context(), id, Token{AccessToken: access, RefreshToken: secret, TokenType: "Bearer"}); err != nil {
		t.Fatal(err)
	}

	result, err := Logout(t.Context(), target, LogoutConfig{Registry: reg, Credentials: creds, HTTPClient: func(context.Context, Connection) (*http.Client, error) { return server.Client(), nil }})
	if err != nil {
		t.Fatal(err)
	}
	if !result.RegistryDeleted || result.CredentialsDeleted != 1 || result.RevocationsAttempted != 2 || result.RevocationsFailed != 0 {
		t.Fatalf("result = %#v", result)
	}
	if len(revoked) != 2 || revoked[0] != secret || revoked[1] != access {
		t.Fatalf("revoked token count/order = %d", len(revoked))
	}
	if _, err := reg.FindTarget(target); !IsNotEnrolled(err) {
		t.Fatalf("registry lookup after logout = %v", err)
	}
	if _, err := creds.Load(t.Context(), id); !IsNotEnrolled(err) {
		t.Fatalf("credential load after logout = %v", err)
	}
	output, _ := json.Marshal(result)
	if strings.Contains(string(output), secret) || strings.Contains(string(output), access) {
		t.Fatal("logout result exposed a token")
	}
	again, err := Logout(t.Context(), target, LogoutConfig{Registry: reg, Credentials: creds})
	if err != nil || again.Entries != 0 {
		t.Fatalf("idempotent logout = %#v, %v", again, err)
	}
}

func TestLogoutPartialStates(t *testing.T) {
	t.Run("entry without credential is removed", func(t *testing.T) {
		reg, err := OpenRegistry(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		id := identity("missing.example:443")
		if _, err := reg.Upsert(Connection{Identity: id}); err != nil {
			t.Fatal(err)
		}
		result, err := Logout(t.Context(), id.Target, LogoutConfig{Registry: reg, Credentials: credentials(t)})
		if err != nil || !result.RegistryDeleted || result.CredentialsMissing != 1 {
			t.Fatalf("result = %#v, err = %v", result, err)
		}
	})

	t.Run("credential without entry is explicitly unreachable", func(t *testing.T) {
		reg, err := OpenRegistry(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		creds := credentials(t)
		id := identity("orphan.example:443")
		if _, err := creds.Upsert(t.Context(), id, Token{AccessToken: "opaque", TokenType: "Bearer"}); err != nil {
			t.Fatal(err)
		}
		result, err := Logout(t.Context(), id.Target, LogoutConfig{Registry: reg, Credentials: creds})
		if err != nil || result.Entries != 0 {
			t.Fatalf("result = %#v, err = %v", result, err)
		}
		if _, err := creds.Load(t.Context(), id); err != nil {
			t.Fatalf("pre-existing orphan was unexpectedly removed: %v", err)
		}
	})
}

type rotatingDeleteStore struct {
	credentialstore.Store
	identity Identity
	token    Token
	rotated  bool
}

func (s *rotatingDeleteStore) Delete(ctx context.Context, key []byte, expected credentialstore.Version) error {
	if !s.rotated {
		s.rotated = true
		canonical, _ := s.identity.Canonical()
		body, _ := json.Marshal(credentialEnvelope{envelopeSchema, 1, canonical, s.token})
		if _, err := s.Put(ctx, key, body, &expected); err != nil {
			return err
		}
		return credentialstore.ErrConflict
	}
	return s.Store.Delete(ctx, key, expected)
}

type conflictDeleteStore struct{ credentialstore.Store }

func (conflictDeleteStore) Delete(context.Context, []byte, credentialstore.Version) error {
	return credentialstore.ErrConflict
}

func TestLogoutReloadsOnceAfterCASConflict(t *testing.T) {
	backend := credentialstore.NewMemoryBackend()
	store, err := backend.Open("logout-retry")
	if err != nil {
		t.Fatal(err)
	}
	var server *httptest.Server
	var revoked []string
	server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/.well-known/openid-configuration" {
			_ = jsonNew(w, map[string]any{"issuer": server.URL, "revocation_endpoint": server.URL + "/revoke"})
			return
		}
		_ = r.ParseForm()
		revoked = append(revoked, r.Form.Get("token"))
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	id := identity("retry.example:443")
	id.Issuer = server.URL
	oldToken := Token{AccessToken: "old-access", RefreshToken: "old-refresh", TokenType: "Bearer"}
	newToken := Token{AccessToken: "new-access", RefreshToken: "new-refresh", TokenType: "Bearer"}
	initial, err := NewCredentials(store)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := initial.Upsert(t.Context(), id, oldToken); err != nil {
		t.Fatal(err)
	}
	rotating, err := NewCredentials(&rotatingDeleteStore{Store: store, identity: id, token: newToken})
	if err != nil {
		t.Fatal(err)
	}
	reg, err := OpenRegistry(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reg.Upsert(Connection{Identity: id}); err != nil {
		t.Fatal(err)
	}
	result, err := Logout(t.Context(), id.Target, LogoutConfig{Registry: reg, Credentials: rotating, HTTPClient: func(context.Context, Connection) (*http.Client, error) { return server.Client(), nil }})
	if err != nil || !result.RegistryDeleted || len(revoked) != 2 {
		t.Fatalf("result = %#v, err = %v, revoked = %#v", result, err, revoked)
	}
	for _, token := range revoked {
		if token != "new-access" && token != "new-refresh" {
			t.Fatalf("revocation used stale token %q", token)
		}
	}
}

func TestLogoutCASConflictRetainsRegistry(t *testing.T) {
	backend := credentialstore.NewMemoryBackend()
	store, err := backend.Open("logout-conflict")
	if err != nil {
		t.Fatal(err)
	}
	creds, err := NewCredentials(store)
	if err != nil {
		t.Fatal(err)
	}
	conflicting, err := NewCredentials(conflictDeleteStore{store})
	if err != nil {
		t.Fatal(err)
	}
	reg, err := OpenRegistry(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	id := identity("race.example:443")
	if _, err := reg.Upsert(Connection{Identity: id}); err != nil {
		t.Fatal(err)
	}
	if _, err := creds.Upsert(t.Context(), id, Token{AccessToken: "opaque", TokenType: "Bearer"}); err != nil {
		t.Fatal(err)
	}
	result, err := Logout(t.Context(), id.Target, LogoutConfig{Registry: reg, Credentials: conflicting})
	if !errors.Is(err, ErrIncompleteLogout) || result.RegistryDeleted || len(result.Issues) != 1 || result.Issues[0].Stage != "credential_changed" {
		t.Fatalf("result = %#v, err = %v", result, err)
	}
	if _, err := reg.FindTarget(id.Target); err != nil {
		t.Fatalf("CAS conflict made credential unreachable: %v", err)
	}
}

func TestLogoutRevocationFailureDoesNotBlockLocalDelete(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/.well-known/openid-configuration" {
			_ = jsonNew(w, map[string]any{"issuer": server.URL, "revocation_endpoint": server.URL + "/revoke"})
			return
		}
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	reg, err := OpenRegistry(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	creds := credentials(t)
	id := identity("failure.example:443")
	id.Issuer = server.URL
	if _, err := reg.Upsert(Connection{Identity: id}); err != nil {
		t.Fatal(err)
	}
	if _, err := creds.Upsert(t.Context(), id, Token{AccessToken: "opaque-access", RefreshToken: "opaque-refresh", TokenType: "Bearer"}); err != nil {
		t.Fatal(err)
	}
	cleaned := false
	result, err := Logout(t.Context(), id.Target, LogoutConfig{Registry: reg, Credentials: creds, HTTPClient: func(ctx context.Context, conn Connection) (*http.Client, error) {
		_, loadErr := creds.Load(ctx, conn.Identity)
		cleaned = IsNotEnrolled(loadErr)
		return server.Client(), nil
	}})
	if err != nil || !result.RegistryDeleted || result.CredentialsDeleted != 1 || result.RevocationsFailed != 2 || !cleaned {
		t.Fatalf("result = %#v, err = %v, local credential cleaned = %v", result, err, cleaned)
	}
}

// TestRegistryHoldsOneEnrolmentPerTarget pins target-keyed collapse and duplicate repair.
func TestRegistryHoldsOneEnrolmentPerTarget(t *testing.T) {
	dir := t.TempDir()
	reg, err := OpenRegistry(dir)
	if err != nil {
		t.Fatal(err)
	}
	const target = "host.example:18081"

	first := identity(target)
	first.Issuer = "https://dex.example"
	if _, err := reg.Upsert(Connection{Identity: first, IssuerCAFile: "/ca/dex.pem"}); err != nil {
		t.Fatal(err)
	}
	// A different issuer for the same target is a re-enrolment, not a second one.
	second := identity(target)
	second.Issuer = "https://keycloak.example/realms/x"
	displaced, err := reg.Upsert(Connection{Identity: second, IssuerCAFile: "/ca/kc.pem"})
	if err != nil {
		t.Fatal(err)
	}

	if len(displaced) != 1 || displaced[0].Issuer != "https://dex.example" {
		t.Fatalf("displaced = %#v, want the superseded Dex identity", displaced)
	}
	all, err := reg.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 {
		t.Fatalf("registry holds %d entries for one target, want 1", len(all))
	}
	got, err := reg.FindTarget(target)
	if err != nil {
		t.Fatalf("FindTarget after re-enrolment: %v", err)
	}
	if got.Identity.Issuer != "https://keycloak.example/realms/x" || got.IssuerCAFile != "/ca/kc.pem" {
		t.Fatalf("stale enrolment survived: %#v", got.Identity)
	}
	// The repair case my first attempt missed: a registry that ALREADY holds
	// duplicates for a target. Replacing matches in place kept them as identical
	// copies, so FindTarget stayed broken and a re-login could not recover. Write
	// such a registry directly, since Upsert can no longer create one.
	dupDir := t.TempDir()
	dupA, dupB := identity(target), identity(target)
	dupA.Issuer, dupB.Issuer = "https://old-one.example", "https://old-two.example"
	raw, err := json.Marshal(map[string]any{"version": 1, "connections": []Connection{
		{Identity: dupA, IssuerCAFile: "/ca/a.pem"}, {Identity: dupB, IssuerCAFile: "/ca/b.pem"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dupDir, "clientauth-connections.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	dupReg, err := OpenRegistry(dupDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, findErr := dupReg.FindTarget(target); findErr == nil {
		t.Fatal("a duplicated registry should not resolve before repair")
	}
	repaired, err := dupReg.Upsert(Connection{Identity: second, IssuerCAFile: "/ca/kc.pem"})
	if err != nil {
		t.Fatal(err)
	}
	if len(repaired) != 2 {
		t.Fatalf("displaced = %#v, want both stale identities", repaired)
	}
	if _, findErr := dupReg.FindTarget(target); findErr != nil {
		t.Fatalf("re-enrolment did not repair the registry: %v", findErr)
	}

	// A different target is still a separate enrolment.
	other := identity("other.example:18081")
	if _, err := reg.Upsert(Connection{Identity: other, IssuerCAFile: "/ca/kc.pem"}); err != nil {
		t.Fatal(err)
	}
}

func TestRegistryReadsLegacyIssuerCAAndMigratesOnMutation(t *testing.T) {
	dir := t.TempDir()
	id := identity("legacy.example:443")
	legacy, err := json.Marshal(map[string]any{
		"version": 1,
		"connections": []any{map[string]any{
			"identity":    id,
			"tls_ca_file": "/ca/legacy-issuer.pem",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "clientauth-connections.json")
	if err := os.WriteFile(path, legacy, 0o600); err != nil {
		t.Fatal(err)
	}
	reg, err := OpenRegistry(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, err := reg.FindTarget(id.Target)
	if err != nil {
		t.Fatal(err)
	}
	if got.IssuerCAFile != "/ca/legacy-issuer.pem" {
		t.Fatalf("legacy issuer CA = %q", got.IssuerCAFile)
	}
	if _, err := reg.Upsert(got); err != nil {
		t.Fatal(err)
	}
	migrated, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(migrated, []byte(`"tls_ca_file"`)) || !bytes.Contains(migrated, []byte(`"issuer_ca_file":"/ca/legacy-issuer.pem"`)) {
		t.Fatalf("mutated registry did not migrate issuer CA field: %s", migrated)
	}
}

// TestRegistryOmitsInvalidEntriesWithoutBlockingOthers pins the fix for a real
// regression: list() used to fail the WHOLE registry read if ANY single entry
// had a relative issuer_ca_file (a shape valid before validIssuerCAFile required
// an absolute path), regardless of which target the caller actually asked
// about. One legacy or damaged row then permanently broke List/FindTarget/Enroll
// for every OTHER target too, with no repair path -- Upsert's own per-target
// replace can only run after list() has already succeeded once. An invalid
// entry must be excluded, not fatal.
func TestRegistryOmitsInvalidEntriesWithoutBlockingOthers(t *testing.T) {
	dir := t.TempDir()
	bad := Connection{Identity: identity("relative-ca.example:443"), IssuerCAFile: "issuer-ca.pem"}
	good := Connection{Identity: identity("unrelated-target.example:443"), IssuerCAFile: "/ca.pem"}
	body, err := json.Marshal(registryFile{Version: 1, Connections: []Connection{bad, good}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "clientauth-connections.json"), body, 0600); err != nil {
		t.Fatal(err)
	}
	reg, err := OpenRegistry(dir)
	if err != nil {
		t.Fatal(err)
	}
	all, err := reg.List()
	if err != nil {
		t.Fatalf("List error = %v, want nil (the relative-CA entry should be omitted, not fatal)", err)
	}
	if len(all) != 1 || all[0].Identity.Target != good.Identity.Target {
		t.Fatalf("List = %#v, want only the unrelated valid target", all)
	}
	if _, err := reg.FindTarget(good.Identity.Target); err != nil {
		t.Fatalf("FindTarget(unrelated target) = %v, want nil: the bad entry must not block it", err)
	}
	if _, err := reg.FindTarget(bad.Identity.Target); !errors.Is(err, credentialstore.ErrNotFound) {
		t.Fatalf("FindTarget(invalid entry's own target) = %v, want ErrNotFound", err)
	}
	// New writes still enforce the absolute-path requirement; only PERSISTED
	// legacy rows are tolerated on read.
	if _, err := reg.Upsert(bad); err == nil || !strings.Contains(err.Error(), "absolute and clean") {
		t.Fatalf("Upsert relative CA error = %v", err)
	}
}

func TestRegistryPrecommitFailureCleansUniqueTemporary(t *testing.T) {
	dir := t.TempDir()
	reg, err := OpenRegistry(dir)
	if err != nil {
		t.Fatal(err)
	}
	reg.writeFault = func(stage string) error {
		if stage == "precommit" {
			return errors.New("injected precommit failure")
		}
		return nil
	}
	_, err = reg.Upsert(Connection{Identity: identity("atomic-registry.example:443"), IssuerCAFile: "/ca.pem"})
	if err == nil {
		t.Fatal("Upsert unexpectedly succeeded")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".clientauth-connections-") && strings.HasSuffix(entry.Name(), ".tmp") {
			t.Fatalf("temporary registry file survived failure: %s", entry.Name())
		}
	}
}
