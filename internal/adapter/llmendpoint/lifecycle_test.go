package llmendpoint_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/oauth2"

	"github.com/stacklok/mecatl/internal/adapter/credentialstore"
	"github.com/stacklok/mecatl/internal/adapter/llmendpoint"
	"github.com/stacklok/mecatl/internal/adapter/oidcclient"
	"github.com/stacklok/mecatl/mcp/oauthlogin"
)

func TestNativeLLMGatewayLogin_Scenario4_PKCEAndBoundedCallback(t *testing.T) {
	var tokenRequests atomic.Int32
	var issuer *httptest.Server
	issuer = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			_ = json.NewEncoder(w).Encode(map[string]any{"issuer": issuer.URL, "authorization_endpoint": issuer.URL + "/authorize", "token_endpoint": issuer.URL + "/token", "jwks_uri": issuer.URL + "/keys", "code_challenge_methods_supported": []string{"S256"}, "authorization_response_iss_parameter_supported": true})
		case "/token":
			tokenRequests.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_ = r.ParseForm()
			if r.Form.Get("code_verifier") == "" || r.Form.Get("grant_type") != "authorization_code" {
				t.Errorf("bad exchange: %v", r.Form)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "jwt-access", "refresh_token": "refresh", "token_type": "Bearer", "expires_in": 60})
		default:
			http.NotFound(w, r)
		}
	}))
	defer issuer.Close()

	base := oidcclient.Config{Issuer: issuer.URL, ClientID: "client", Audience: "gateway", Scopes: []string{"offline_access"}, RedirectURI: oauthlogin.ExactRedirectURL, HTTPClient: issuer.Client(), ValidateAccessToken: func(context.Context, string) error { return nil }}

	base.Present = func(_ context.Context, authURL string) (oauthlogin.Result, error) {
		u, err := url.Parse(authURL)
		if err != nil {
			return oauthlogin.Result{}, err
		}
		q := u.Query()
		if q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" || q.Get("redirect_uri") != oauthlogin.ExactRedirectURL {
			t.Fatalf("PKCE/redirect missing: %v", q)
		}
		return oauthlogin.Result{Code: "code", State: q.Get("state"), Iss: issuer.URL}, nil
	}
	if _, err := oidcclient.AuthorizationCode(t.Context(), base); err != nil {
		t.Fatalf("AuthorizationCode: %v", err)
	}

	backend := credentialstore.NewMemoryBackend()
	store, err := backend.Open(llmendpoint.CredentialNamespace)
	if err != nil {
		t.Fatal(err)
	}
	repo := llmendpoint.NewCredentialRepository(store)
	identity := testIdentity()
	lifecycle := llmendpoint.Lifecycle{Repository: repo, Locker: llmendpoint.MemoryLocker()}
	lifecycle.Authorize = func(context.Context) (llmendpoint.Token, error) {
		return llmendpoint.Token{AccessToken: "must-not-persist", RefreshToken: "must-not-persist", TokenType: "Bearer", Expiry: time.Now().Add(time.Hour)}, oidcclient.ErrAuthorization
	}
	if err := lifecycle.Enroll(t.Context(), identity); err == nil {
		t.Fatal("rejected callback unexpectedly enrolled")
	}
	if _, err := repo.Load(t.Context(), identity); !errors.Is(err, llmendpoint.ErrNotEnrolled) {
		t.Fatalf("rejected callback wrote a record: %v", err)
	}
	lifecycle.Authorize = func(ctx context.Context) (llmendpoint.Token, error) {
		tok, err := oidcclient.AuthorizationCode(ctx, base)
		return llmendpoint.Token{AccessToken: tok.AccessToken, RefreshToken: tok.RefreshToken, TokenType: tok.TokenType, Expiry: tok.Expiry}, err
	}
	if err := lifecycle.Enroll(t.Context(), identity); err != nil {
		t.Fatalf("legitimate callback after rejection: %v", err)
	}

	failures := []struct {
		name   string
		mutate func(*oidcclient.Config)
	}{
		{"wrong state", func(c *oidcclient.Config) {
			c.Present = func(context.Context, string) (oauthlogin.Result, error) {
				return oauthlogin.Result{Code: "code", State: "wrong", Iss: issuer.URL}, nil
			}
		}},
		{"wrong issuer callback", func(c *oidcclient.Config) {
			c.Present = func(_ context.Context, u string) (oauthlogin.Result, error) {
				p, _ := url.Parse(u)
				return oauthlogin.Result{Code: "code", State: p.Query().Get("state"), Iss: "https://wrong.example"}, nil
			}
		}},
		{"wrong route", func(c *oidcclient.Config) { c.RedirectURI = "http://127.0.0.1:18473/wrong" }},
		{"excess traffic", func(c *oidcclient.Config) {
			c.Present = func(context.Context, string) (oauthlogin.Result, error) {
				return oauthlogin.Result{}, oauthlogin.ErrCallbackAttempts
			}
		}},
		{"timeout", func(c *oidcclient.Config) {
			c.Present = func(context.Context, string) (oauthlogin.Result, error) {
				return oauthlogin.Result{}, context.DeadlineExceeded
			}
		}},
		{"cancellation", func(c *oidcclient.Config) {
			c.Present = func(context.Context, string) (oauthlogin.Result, error) { return oauthlogin.Result{}, context.Canceled }
		}},
	}
	for _, tc := range failures {
		t.Run(tc.name, func(t *testing.T) {
			c := base
			tc.mutate(&c)
			before := tokenRequests.Load()
			if _, err := oidcclient.AuthorizationCode(t.Context(), c); err == nil {
				t.Fatal("expected rejection")
			}
			if tokenRequests.Load() != before {
				t.Fatal("rejected callback reached token exchange")
			}
		})
	}

	noS256 := base
	noS256.HTTPClient = discoveryClient(t, issuer.URL, []string{"plain"})
	if _, err := oidcclient.AuthorizationCode(t.Context(), noS256); err == nil {
		t.Fatal("discovery without S256 accepted")
	}
	wrongIssuer := base
	wrongIssuer.HTTPClient = discoveryClient(t, "https://wrong.example", []string{"S256"})
	if _, err := oidcclient.AuthorizationCode(t.Context(), wrongIssuer); err == nil {
		t.Fatal("wrong discovery issuer accepted")
	}
	insecure := base
	insecure.Issuer = "http://issuer.example"
	insecure.HTTPClient = discoveryClient(t, insecure.Issuer, []string{"S256"})
	presented := false
	insecure.Present = func(context.Context, string) (oauthlogin.Result, error) {
		presented = true
		return oauthlogin.Result{}, nil
	}
	if _, err := oidcclient.AuthorizationCode(t.Context(), insecure); err == nil || presented {
		t.Fatalf("insecure issuer reached authorization: err=%v presented=%v", err, presented)
	}
}

func TestNativeLLMGatewayLogin_Scenario4_AccessTokenOnlyProfile(t *testing.T) {
	cases := []struct {
		name      string
		response  map[string]any
		validator func(context.Context, string) error
		ok        bool
	}{
		{"valid access token", map[string]any{"access_token": "jwt-access", "id_token": "jwt-id", "refresh_token": "refresh", "token_type": "Bearer", "expires_in": 60}, func(_ context.Context, got string) error {
			if got != "jwt-access" {
				return errors.New("ID token substituted")
			}
			return nil
		}, true},
		{"id token only", map[string]any{"id_token": "jwt-id", "refresh_token": "refresh", "token_type": "Bearer"}, func(context.Context, string) error { return nil }, false},
		{"wrong token type", map[string]any{"access_token": "jwt-access", "refresh_token": "refresh", "token_type": "MAC"}, func(context.Context, string) error { return nil }, false},
		{"missing refresh", map[string]any{"access_token": "jwt-access", "token_type": "Bearer"}, func(context.Context, string) error { return nil }, false},
		{"opaque rejected by profile", map[string]any{"access_token": "opaque", "refresh_token": "refresh", "token_type": "Bearer"}, func(context.Context, string) error { return errors.New("not gateway JWT profile") }, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, closeFn := loginFixture(t, tc.response)
			defer closeFn()
			cfg.ValidateAccessToken = tc.validator
			_, err := oidcclient.AuthorizationCode(t.Context(), cfg)
			if (err == nil) != tc.ok {
				t.Fatalf("err=%v want ok=%v", err, tc.ok)
			}
		})
	}
}

func TestNativeLLMGatewayLogin_Scenario4_CredentialIdentityAndStorage(t *testing.T) {
	if llmendpoint.CredentialNamespace != "mecatl/provider-oidc/v1" {
		t.Fatalf("namespace=%q", llmendpoint.CredentialNamespace)
	}
	backend := credentialstore.NewMemoryBackend()
	store, err := backend.Open(llmendpoint.CredentialNamespace)
	if err != nil {
		t.Fatal(err)
	}
	repo := llmendpoint.NewCredentialRepository(store)
	id := testIdentity()
	token := llmendpoint.Token{AccessToken: "access", RefreshToken: "refresh", TokenType: "Bearer", Expiry: time.Now().Add(time.Hour)}
	if _, err = repo.Save(t.Context(), id, token, nil); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*llmendpoint.CredentialIdentity){"endpoint": func(i *llmendpoint.CredentialIdentity) { i.EndpointID = "other" }, "gateway": func(i *llmendpoint.CredentialIdentity) { i.Gateway = "https://gateway.example/other" }, "issuer": func(i *llmendpoint.CredentialIdentity) { i.Issuer = "https://other.example" }, "client": func(i *llmendpoint.CredentialIdentity) { i.ClientID = "other" }, "audience": func(i *llmendpoint.CredentialIdentity) { i.ResourceAudience = "other" }, "scopes": func(i *llmendpoint.CredentialIdentity) { i.Scopes = []string{"other"} }, "redirect": func(i *llmendpoint.CredentialIdentity) { i.RedirectURI = "other" }, "issuer trust": func(i *llmendpoint.CredentialIdentity) { i.IssuerTrust.CADigest = "changed" }, "gateway trust": func(i *llmendpoint.CredentialIdentity) { i.GatewayTrust.CADigest = "changed" }} {
		t.Run(name, func(t *testing.T) {
			drift := id
			drift.Scopes = append([]string(nil), id.Scopes...)
			mutate(&drift)
			if _, err := repo.Load(t.Context(), drift); !errors.Is(err, llmendpoint.ErrNotEnrolled) {
				t.Fatalf("identity drift=%v", err)
			}
		})
	}
	if _, err := llmendpoint.NewProtectedStore(t.Context(), llmendpoint.ProtectedStoreConfig{}); !errors.Is(err, llmendpoint.ErrNotEnrolled) {
		t.Fatalf("unavailable keyring/store=%v", err)
	}
	protectedRoot := t.TempDir()
	if err := os.Chmod(protectedRoot, 0700); err != nil {
		t.Fatal(err)
	}
	protected, err := llmendpoint.NewProtectedStore(t.Context(), llmendpoint.ProtectedStoreConfig{Root: protectedRoot, Keyring: staticKeyring{key: make([]byte, 32)}})
	if err != nil {
		t.Fatalf("open protected keyring-backed store: %v", err)
	}
	defer func() { _ = protected.Close() }()
	if caps := protected.Capabilities(); !caps.Persistent || !caps.CrossProcessCAS {
		t.Fatalf("protected store capabilities = %#v", caps)
	}
	if _, err := repo.Load(t.Context(), id); err != nil {
		t.Fatalf("valid record: %v", err)
	}
	key, err := llmendpoint.CredentialRecordKey(id)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := store.Get(t.Context(), key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Put(t.Context(), key, []byte(`{"schema":"future","version":2}`), &raw.Version); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Load(t.Context(), id); !errors.Is(err, llmendpoint.ErrNotEnrolled) {
		t.Fatalf("unknown schema did not fail closed: %v", err)
	}
}

func TestNativeLLMGatewayLogin_Scenario5_TransactionLockOrderingAndConcurrency(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	first, err := llmendpoint.NewTransactionLocker(root)
	if err != nil {
		t.Fatal(err)
	}
	second, err := llmendpoint.NewTransactionLocker(root)
	if err != nil {
		t.Fatal(err)
	}
	id := testIdentity()
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- first.With(t.Context(), id, func(context.Context) error { close(entered); <-release; return nil })
	}()
	<-entered
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	if err := second.With(ctx, id, func(context.Context) error { return nil }); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("contender=%v", err)
	}
	other := id
	other.EndpointID = "other"
	if err := second.With(t.Context(), other, func(context.Context) error { return nil }); err != nil {
		t.Fatalf("unrelated lock blocked: %v", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	matches, _ := filepath.Glob(filepath.Join(root, ".provider-oidc-*.lock"))
	if len(matches) < 2 {
		t.Fatalf("lock files=%v", matches)
	}
	for _, p := range matches {
		info, _ := os.Stat(p)
		if info.Mode().Perm() != 0600 {
			t.Fatalf("%s mode=%o", p, info.Mode().Perm())
		}
	}

	backend := credentialstore.NewMemoryBackend()
	store, err := backend.Open(llmendpoint.CredentialNamespace)
	if err != nil {
		t.Fatal(err)
	}
	repo := llmendpoint.NewCredentialRepository(store)
	if _, err := repo.Save(t.Context(), id, llmendpoint.Token{AccessToken: "old", RefreshToken: "refresh", TokenType: "Bearer", Expiry: time.Now().Add(-time.Minute)}, nil); err != nil {
		t.Fatal(err)
	}
	tracked := &trackingLocker{next: llmendpoint.MemoryLocker()}
	lifecycle := llmendpoint.Lifecycle{Repository: repo, Locker: tracked, Exchange: func(context.Context, string) (llmendpoint.Token, error) {
		if !tracked.held.Load() {
			t.Fatal("token exchange ran outside transaction lock")
		}
		return llmendpoint.Token{AccessToken: "new", RefreshToken: "rotated", TokenType: "Bearer", Expiry: time.Now().Add(time.Hour)}, nil
	}, AfterCommit: func() {
		if !tracked.held.Load() {
			t.Fatal("commit completed outside transaction lock")
		}
	}}
	if _, err := lifecycle.Refresh(t.Context(), id); err != nil {
		t.Fatal(err)
	}
}

func TestInvariant_rotated_refresh_is_persisted_before_access_validation(t *testing.T) {
	backend := credentialstore.NewMemoryBackend()
	store, _ := backend.Open(llmendpoint.CredentialNamespace)
	repo := llmendpoint.NewCredentialRepository(store)
	id := testIdentity()
	old := llmendpoint.Token{AccessToken: "old", RefreshToken: "prior", TokenType: "Bearer", Expiry: time.Now().Add(-time.Minute)}
	if _, err := repo.Save(t.Context(), id, old, nil); err != nil {
		t.Fatal(err)
	}
	validationErr := errors.New("access profile rejected")
	lifecycle := llmendpoint.Lifecycle{
		Repository: repo,
		Locker:     llmendpoint.MemoryLocker(),
		Exchange: func(context.Context, string) (llmendpoint.Token, error) {
			return llmendpoint.Token{AccessToken: "untrusted-access", RefreshToken: "rotated", TokenType: "Bearer", Expiry: time.Now().Add(time.Hour)}, nil
		},
		ValidateAccessToken: func(context.Context, string) error {
			loaded, err := repo.Load(t.Context(), id)
			if err != nil || loaded.Token.RefreshToken != "rotated" {
				t.Fatalf("validation ran before rotated refresh commit: record=%+v err=%v", loaded.Token, err)
			}
			return validationErr
		},
	}
	if _, err := lifecycle.Refresh(t.Context(), id); !errors.Is(err, validationErr) {
		t.Fatalf("Refresh error = %v", err)
	}
	loaded, err := repo.Load(t.Context(), id)
	if err != nil || loaded.Token.RefreshToken != "rotated" {
		t.Fatalf("rotated refresh was not retained after access rejection: %+v %v", loaded.Token, err)
	}
}

func TestNativeLLMGatewayLogin_Scenario5_RotationAndAmbiguousCommit(t *testing.T) {
	backend := credentialstore.NewMemoryBackend()
	store, _ := backend.Open(llmendpoint.CredentialNamespace)
	repo := llmendpoint.NewCredentialRepository(store)
	id := testIdentity()
	old := llmendpoint.Token{AccessToken: "old", RefreshToken: "prior", TokenType: "Bearer", Expiry: time.Now().Add(-time.Minute)}
	rec, err := repo.Save(t.Context(), id, old, nil)
	if err != nil {
		t.Fatal(err)
	}
	var returnedBeforeCommit atomic.Bool
	lifecycle := llmendpoint.Lifecycle{Repository: repo, Locker: llmendpoint.MemoryLocker(), Exchange: func(context.Context, string) (llmendpoint.Token, error) {
		return llmendpoint.Token{AccessToken: "new", RefreshToken: "rotated", TokenType: "Bearer", Expiry: time.Now().Add(time.Hour)}, nil
	}, AfterCommit: func() { returnedBeforeCommit.Store(true) }}
	got, err := lifecycle.Refresh(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	if !returnedBeforeCommit.Load() || got.AccessToken != "new" {
		t.Fatal("bearer returned before durable commit")
	}
	loaded, _ := repo.Load(t.Context(), id)
	if loaded.Token.RefreshToken != "rotated" {
		t.Fatal("rotation not persisted")
	}
	lifecycle.Exchange = func(context.Context, string) (llmendpoint.Token, error) {
		return llmendpoint.Token{AccessToken: "newer", TokenType: "Bearer", Expiry: time.Now().Add(time.Hour)}, nil
	}
	got, err = lifecycle.Refresh(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	loaded, _ = repo.Load(t.Context(), id)
	if loaded.Token.RefreshToken != "rotated" {
		t.Fatal("omitted refresh did not retain prior")
	}

	ambiguousBackend := credentialstore.NewMemoryBackend()
	ambiguousInner, err := ambiguousBackend.Open(llmendpoint.CredentialNamespace)
	if err != nil {
		t.Fatal(err)
	}
	ambiguous := &ambiguousCommitStore{Store: ambiguousInner}
	ambiguousRepo := llmendpoint.NewCredentialRepository(ambiguous)
	ambiguousRec, err := ambiguousRepo.Save(t.Context(), id, old, nil)
	if err != nil {
		t.Fatal(err)
	}
	ambiguous.failNext.Store(true)
	committed := llmendpoint.Token{AccessToken: "committed", RefreshToken: "committed-refresh", TokenType: "Bearer", Expiry: time.Now().Add(time.Hour)}
	if _, err := ambiguousRepo.Save(t.Context(), id, committed, &ambiguousRec.Version); err != nil {
		t.Fatalf("ambiguous post-commit result was not reconciled: %v", err)
	}
	observed, err := ambiguousRepo.Load(t.Context(), id)
	if err != nil || observed.Token.AccessToken != "committed" {
		t.Fatalf("ambiguous committed record = %#v, %v", observed.Token, err)
	}

	stale := loaded.Version
	winner := llmendpoint.Token{AccessToken: "winner", RefreshToken: "winner-refresh", TokenType: "Bearer", Expiry: time.Now().Add(time.Hour)}
	if _, err = repo.Save(t.Context(), id, winner, &stale); err != nil {
		t.Fatal(err)
	}
	if err = repo.Delete(t.Context(), id, stale); !errors.Is(err, credentialstore.ErrConflict) {
		t.Fatalf("stale delete=%v", err)
	}
	invalid := &oauth2.RetrieveError{ErrorCode: "invalid_grant"}
	lifecycle.Exchange = func(context.Context, string) (llmendpoint.Token, error) { return llmendpoint.Token{}, invalid }
	if _, err = lifecycle.Refresh(t.Context(), id); !errors.Is(err, llmendpoint.ErrNotEnrolled) {
		t.Fatalf("invalid grant=%v", err)
	}
	if _, err = repo.Load(t.Context(), id); !errors.Is(err, llmendpoint.ErrNotEnrolled) {
		t.Fatalf("credential survived exact invalid_grant: %v", err)
	}
	_ = rec
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func discoveryClient(t *testing.T, discoveredIssuer string, methods []string) *http.Client {
	t.Helper()
	return &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body := `{"issuer":"` + discoveredIssuer + `","authorization_endpoint":"https://issuer.example/auth","token_endpoint":"https://issuer.example/token","jwks_uri":"https://issuer.example/keys","code_challenge_methods_supported":["` + strings.Join(methods, `","`) + `"]}`
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: ioNopCloser{strings.NewReader(body)}, Request: r}, nil
	})}
}

type ioNopCloser struct{ *strings.Reader }

func (ioNopCloser) Close() error { return nil }
func loginFixture(t *testing.T, response map[string]any) (oidcclient.Config, func()) {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/.well-known/openid-configuration" {
			_ = json.NewEncoder(w).Encode(map[string]any{"issuer": srv.URL, "authorization_endpoint": srv.URL + "/auth", "token_endpoint": srv.URL + "/token", "jwks_uri": srv.URL + "/keys", "code_challenge_methods_supported": []string{"S256"}})
			return
		}
		if r.URL.Path == "/token" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(response)
			return
		}
		http.NotFound(w, r)
	}))
	cfg := oidcclient.Config{Issuer: srv.URL, ClientID: "client", Audience: "gateway", Scopes: []string{"offline_access"}, RedirectURI: oauthlogin.ExactRedirectURL, HTTPClient: srv.Client()}
	cfg.Present = func(_ context.Context, u string) (oauthlogin.Result, error) {
		p, _ := url.Parse(u)
		return oauthlogin.Result{Code: "code", State: p.Query().Get("state")}, nil
	}
	return cfg, srv.Close
}

type staticKeyring struct{ key []byte }

func (k staticKeyring) Key(context.Context, bool) ([]byte, error) {
	return append([]byte(nil), k.key...), nil
}

type trackingLocker struct {
	next llmendpoint.Locker
	held atomic.Bool
}

func (l *trackingLocker) With(ctx context.Context, id llmendpoint.CredentialIdentity, fn func(context.Context) error) error {
	return l.next.With(ctx, id, func(ctx context.Context) error {
		l.held.Store(true)
		defer l.held.Store(false)
		return fn(ctx)
	})
}

type ambiguousCommitStore struct {
	credentialstore.Store
	failNext atomic.Bool
}

func (s *ambiguousCommitStore) Put(ctx context.Context, key, value []byte, expected *credentialstore.Version) (credentialstore.Record, error) {
	rec, err := s.Store.Put(ctx, key, value, expected)
	if err == nil && s.failNext.Swap(false) {
		return credentialstore.Record{}, errors.New("ambiguous post-commit failure")
	}
	return rec, err
}

func testIdentity() llmendpoint.CredentialIdentity {
	return llmendpoint.CredentialIdentity{SchemaVersion: 1, EndpointID: "corp", Gateway: "https://gateway.example/v1", Issuer: "https://issuer.example", ClientID: "client", ResourceAudience: "gateway", Scopes: []string{"models.read", "offline_access"}, RedirectURI: oauthlogin.ExactRedirectURL, IssuerTrust: llmendpoint.TrustIdentity{Policy: "private-ca", CADigest: "issuer-ca"}, GatewayTrust: llmendpoint.TrustIdentity{Policy: "private-ca", CADigest: "gateway-ca"}}
}
