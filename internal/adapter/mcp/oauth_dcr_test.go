package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
	"golang.org/x/oauth2"

	"github.com/stacklok/mecatl/internal/adapter/credentialstore"
)

type dcrMetadataFixture struct {
	mu                 sync.Mutex
	server             *httptest.Server
	registration       string
	issuerOverride     string
	resourceValue      string
	authServers        []string
	codeMethods        []string
	grantTypes         []string
	authMethods        []string
	responseTypes      []string
	scopes             []string
	registerCount      int
	tokenCount         int
	tokenForms         []url.Values
	clientID           string
	cosmeticSuffix     string
	unsolicitedRefresh bool
}

func newDCRMetadataFixture(t *testing.T) *dcrMetadataFixture {
	t.Helper()
	f := &dcrMetadataFixture{
		codeMethods: []string{"S256"}, grantTypes: []string{"authorization_code", "refresh_token"},
		authMethods: []string{"none"}, responseTypes: []string{"code"}, scopes: []string{"openid", "offline_access"}, clientID: "public-client",
	}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		issuer := f.server.URL
		issuerValue := issuer
		if f.issuerOverride != "" {
			issuerValue = f.issuerOverride
		}
		switch {
		case strings.Contains(r.URL.Path, "oauth-protected-resource"):
			resourceValue := issuer + "/gw/mcp"
			if f.resourceValue != "" {
				resourceValue = f.resourceValue
			}
			authServers := f.authServers
			if authServers == nil {
				authServers = []string{issuer}
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(oauthex.ProtectedResourceMetadata{Resource: resourceValue, AuthorizationServers: authServers, ScopesSupported: []string{"openid"}})
		case strings.Contains(r.URL.Path, ".well-known/oauth-authorization-server") || strings.Contains(r.URL.Path, ".well-known/openid-configuration"):
			w.Header().Set("Content-Type", "application/json")
			registration := f.registration
			if registration == "" {
				registration = issuer + "/oauth/register"
			}
			_ = json.NewEncoder(w).Encode(oauthex.AuthServerMeta{
				Issuer: issuerValue, AuthorizationEndpoint: issuer + "/oauth/authorize" + f.cosmeticSuffix,
				TokenEndpoint: issuer + "/oauth/token", RegistrationEndpoint: registration,
				ScopesSupported: f.scopes, ResponseTypesSupported: f.responseTypes,
				GrantTypesSupported: f.grantTypes, TokenEndpointAuthMethodsSupported: f.authMethods,
				CodeChallengeMethodsSupported: f.codeMethods,
			})
		case r.URL.Path == "/oauth/register" || r.URL.Path == "/oauth/register-v2":
			w.Header().Set("Content-Type", "application/json")
			f.registerCount++
			var request oauthex.ClientRegistrationMetadata
			_ = json.NewDecoder(r.Body).Decode(&request)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"client_id": f.clientID, "token_endpoint_auth_method": "none", "redirect_uris": request.RedirectURIs,
				"grant_types": request.GrantTypes, "response_types": request.ResponseTypes, "scope": request.Scope,
			})
		case r.URL.Path == "/oauth/token":
			f.tokenCount++
			_ = r.ParseForm()
			f.tokenForms = append(f.tokenForms, r.PostForm)
			response := map[string]any{"access_token": "dcr-access", "token_type": "Bearer", "expires_in": 3600, "scope": "openid"}
			if f.unsolicitedRefresh {
				response["refresh_token"] = "unsolicited-refresh"
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(response)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.server.Close)
	return f
}

func (f *dcrMetadataFixture) options(t *testing.T, store credentialstore.Store) OAuthOptions {
	t.Helper()
	opts := OAuthOptions{
		Subject: OAuthSubject{Profile: "connector", Principal: "local-user"}, Issuer: f.server.URL,
		Client: OAuthClientConfig{DCR: &OAuthDCRConfig{}}, CredentialStore: store,
		AllowedScopes: []string{"openid"},
	}
	AllowOAuthLoopbackForTest(t, &opts)
	return opts
}

func newDCRMemoryStore(t *testing.T) credentialstore.Store {
	t.Helper()
	store, err := credentialstore.NewMemoryBackend().Open("mecatl-mcp-oauth")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

type conflictAfterCommitStore struct {
	credentialstore.Store
	mu            sync.Mutex
	conflictOnPut int
	puts          int
}

func (s *conflictAfterCommitStore) Put(ctx context.Context, key, value []byte, expected *credentialstore.Version) (credentialstore.Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.puts++
	record, err := s.Store.Put(ctx, key, value, expected)
	if err == nil && s.puts == s.conflictOnPut {
		return credentialstore.Record{}, credentialstore.ErrConflict
	}
	return record, err
}

func TestADR_0325_DirectDCRMetadataAndEgressPolicy(t *testing.T) {
	fixture := newDCRMetadataFixture(t)
	resource := fixture.server.URL + "/gw/mcp"
	store := newDCRMemoryStore(t)
	opts := fixture.options(t, store)

	prepared, path, err := PrepareOAuthDCRLogin(context.Background(), resource, opts, OAuthDCRLoginReuse)
	if err != nil {
		t.Fatalf("prepare valid metadata: %v", err)
	}
	prepared.RedirectURL = "http://127.0.0.1:49152" + path
	controller, err := NewOAuthController(context.Background(), resource, prepared)
	if err != nil {
		t.Fatalf("register public client: %v", err)
	}
	_ = controller.Close()
	if fixture.registerCount != 1 {
		t.Fatalf("registration POST count = %d, want 1", fixture.registerCount)
	}

	fixture.registration = fixture.server.URL + "/oauth/register-v2"
	fixture.cosmeticSuffix = "-v2"
	reused, reusedPath, err := PrepareOAuthDCRLogin(context.Background(), resource, opts, OAuthDCRLoginReuse)
	if err != nil {
		t.Fatalf("reuse after endpoint/name rotation: %v", err)
	}
	if reused.Client.DCR == nil || reusedPath != path || fixture.registerCount != 1 {
		t.Fatalf("reuse = %#v path %q registrations %d", reused.Client, reusedPath, fixture.registerCount)
	}

	for _, tc := range []struct {
		name   string
		mutate func(*dcrMetadataFixture)
	}{
		{name: "missing S256", mutate: func(f *dcrMetadataFixture) { f.codeMethods = []string{"plain"} }},
		{name: "missing authorization code", mutate: func(f *dcrMetadataFixture) { f.grantTypes = []string{"refresh_token"} }},
		{name: "missing public auth", mutate: func(f *dcrMetadataFixture) { f.authMethods = []string{"client_secret_basic"} }},
		{name: "protected resource mismatch", mutate: func(f *dcrMetadataFixture) { f.resourceValue = f.server.URL + "/other" }},
		{name: "multiple authorization servers", mutate: func(f *dcrMetadataFixture) { f.authServers = []string{f.server.URL, "https://other.example"} }},
		{name: "exact issuer mismatch", mutate: func(f *dcrMetadataFixture) { f.issuerOverride = f.server.URL + "/" }},
		{name: "queried authorization endpoint", mutate: func(f *dcrMetadataFixture) { f.cosmeticSuffix = "?audience=other" }},
		{name: "off-origin registration", mutate: func(f *dcrMetadataFixture) { f.registration = "https://evil.example/register" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bad := newDCRMetadataFixture(t)
			tc.mutate(bad)
			_, _, prepErr := PrepareOAuthDCRLogin(context.Background(), bad.server.URL+"/gw/mcp", bad.options(t, newDCRMemoryStore(t)), OAuthDCRLoginReuse)
			if prepErr == nil {
				t.Fatal("invalid DCR metadata was admitted")
			}
			if bad.registerCount != 0 {
				t.Fatalf("invalid metadata caused %d registration POSTs", bad.registerCount)
			}
		})
	}
}

func TestADR_0325_DirectDCRRegistrationPrecedesTokenIdentity(t *testing.T) {
	fixture := newDCRMetadataFixture(t)
	resource := fixture.server.URL + "/gw/mcp"
	opts := fixture.options(t, newDCRMemoryStore(t))
	if _, err := OAuthCredentialRecordKey(resource, opts); err == nil {
		t.Fatal("unresolved DCR produced a token key")
	}
	prepared, path, err := PrepareOAuthDCRLogin(context.Background(), resource, opts, OAuthDCRLoginReuse)
	if err != nil {
		t.Fatal(err)
	}
	prepared.RedirectURL = "http://127.0.0.1:49152" + path
	controller, err := NewOAuthController(context.Background(), resource, prepared)
	if err != nil {
		t.Fatal(err)
	}
	_ = controller.Close()
	restoredController, err := NewOAuthController(context.Background(), resource, opts)
	if err != nil {
		t.Fatalf("ordinary controller did not restore ready registration: %v", err)
	}
	_ = restoredController.Close()
	if fixture.registerCount != 1 {
		t.Fatalf("ordinary restore registered again: %d POSTs", fixture.registerCount)
	}
	resolved, _, err := PrepareOAuthDCRLogin(context.Background(), resource, opts, OAuthDCRLoginReuse)
	if err != nil {
		t.Fatal(err)
	}
	key, err := OAuthCredentialRecordKey(resource, resolved)
	if err != nil || len(key) != 32 {
		t.Fatalf("resolved DCR token key = %x, error %v", key, err)
	}
	if resolved.Client.DCR == nil || resolved.Client.Preregistered != nil {
		t.Fatalf("resolved public client kind was disguised: %#v", resolved.Client)
	}
}

func TestOAuthDCRExplicitResetAndRetryReplaceOnePredecessor(t *testing.T) {
	fixture := newDCRMetadataFixture(t)
	resource := fixture.server.URL + "/gw/mcp"
	store := newDCRMemoryStore(t)
	opts := fixture.options(t, store)

	prepared, path, err := PrepareOAuthDCRLogin(context.Background(), resource, opts, OAuthDCRLoginReuse)
	if err != nil {
		t.Fatal(err)
	}
	prepared.RedirectURL = "http://127.0.0.1:49152" + path
	controller, err := NewOAuthController(context.Background(), resource, prepared)
	if err != nil {
		t.Fatal(err)
	}
	_ = controller.Close()

	_, resetPath, err := PrepareOAuthDCRLogin(context.Background(), resource, opts, OAuthDCRLoginResetRegistration)
	if err != nil {
		t.Fatalf("reset ready registration: %v", err)
	}
	if resetPath == path {
		t.Fatal("registration reset reused callback path")
	}
	identity := oauthDCRIdentity{Profile: opts.Subject.Profile, Principal: opts.Subject.Principal, Resource: resource, Issuer: opts.Issuer}
	key, err := oauthDCRRegistrationKey(identity)
	if err != nil {
		t.Fatal(err)
	}
	record, err := store.Get(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	resetPending, err := decodeOAuthDCRRecord(record.Value, identity)
	if err != nil {
		t.Fatal(err)
	}
	if resetPending.State != "pending" || resetPending.PreviousAttempt == nil || resetPending.PreviousAttempt.Reason != "explicit_reset" {
		t.Fatalf("reset predecessor = %#v", resetPending.PreviousAttempt)
	}

	retry, retryPath, err := PrepareOAuthDCRLogin(context.Background(), resource, opts, OAuthDCRLoginRetryRegistration)
	if err != nil {
		t.Fatalf("retry pending registration: %v", err)
	}
	if retryPath == resetPath {
		t.Fatal("registration retry reused callback path")
	}
	record, err = store.Get(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	retryPending, err := decodeOAuthDCRRecord(record.Value, identity)
	if err != nil {
		t.Fatal(err)
	}
	if retryPending.PreviousAttempt == nil || retryPending.PreviousAttempt.Reason != "explicit_retry" || retryPending.PreviousAttempt.Generation != resetPending.Generation {
		t.Fatalf("retry predecessor = %#v, reset generation %q", retryPending.PreviousAttempt, resetPending.Generation)
	}
	retry.RedirectURL = "http://127.0.0.1:49153" + retryPath
	controller, err = NewOAuthController(context.Background(), resource, retry)
	if err != nil {
		t.Fatal(err)
	}
	_ = controller.Close()
	if fixture.registerCount != 2 {
		t.Fatalf("registration POST count = %d, want 2", fixture.registerCount)
	}
}

func TestADR_0325_DirectDCRRegistrationCASWinnerAdoption(t *testing.T) {
	fixture := newDCRMetadataFixture(t)
	resource := fixture.server.URL + "/gw/mcp"
	store := &conflictAfterCommitStore{Store: newDCRMemoryStore(t), conflictOnPut: 2}
	opts := fixture.options(t, store)
	prepared, path, err := PrepareOAuthDCRLogin(context.Background(), resource, opts, OAuthDCRLoginReuse)
	if err != nil {
		t.Fatal(err)
	}
	prepared.RedirectURL = "http://127.0.0.1:49152" + path
	controller, err := NewOAuthController(context.Background(), resource, prepared)
	if err != nil {
		t.Fatal(err)
	}
	_ = controller.Close()

	winner, winnerPath, err := PrepareOAuthDCRLogin(context.Background(), resource, opts, OAuthDCRLoginReuse)
	if err != nil {
		t.Fatal(err)
	}
	if winnerPath != path || winner.Client.DCR == nil {
		t.Fatalf("ready winner was not adopted: path=%q client=%#v", winnerPath, winner.Client)
	}
	if fixture.registerCount != 1 {
		t.Fatalf("winner adoption registered again: %d POSTs", fixture.registerCount)
	}

	_, _, err = PrepareOAuthDCRLogin(context.Background(), resource, fixture.options(t, store), OAuthDCRLoginRetryRegistration)
	if !errors.Is(err, ErrOAuthDCRRecoveryRequired) {
		t.Fatalf("retry against ready = %v, want recovery-required", err)
	}
}

func TestADR_0325_PublicNoneWireQualification(t *testing.T) {
	fixture := newDCRMetadataFixture(t)
	resource := fixture.server.URL + "/gw/mcp"
	store := newDCRMemoryStore(t)
	opts := fixture.options(t, store)
	opts.Presenter = OAuthPresenterFunc(func(_ context.Context, raw string) (*auth.AuthorizationResult, error) {
		u, err := url.Parse(raw)
		if err != nil {
			return nil, err
		}
		q := u.Query()
		if q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" || len(q["resource"]) != 1 || q.Get("resource") != resource || q.Get("scope") != "openid" {
			t.Fatalf("authorization query = %v", q)
		}
		return &auth.AuthorizationResult{Code: "fixture-code", State: q.Get("state"), Iss: fixture.server.URL}, nil
	})
	prepared, path, err := PrepareOAuthDCRLogin(context.Background(), resource, opts, OAuthDCRLoginReuse)
	if err != nil {
		t.Fatal(err)
	}
	prepared.RedirectURL = "http://127.0.0.1:49152" + path
	controller, err := NewOAuthController(context.Background(), resource, prepared)
	if err != nil {
		t.Fatal(err)
	}
	defer controller.Close()
	req, _ := http.NewRequest(http.MethodGet, resource, nil)
	resp := &http.Response{StatusCode: http.StatusUnauthorized, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(""))}
	resp.Header.Set("WWW-Authenticate", `Bearer scope="openid"`)
	if err := controller.Authorize(context.Background(), req, resp); err != nil {
		t.Fatalf("authorize public DCR: %v", err)
	}
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if fixture.tokenCount != 1 || len(fixture.tokenForms) != 1 {
		t.Fatalf("upstream token requests = %d, want one parameter fallback", fixture.tokenCount)
	}
	form := fixture.tokenForms[0]
	if form.Get("client_id") != fixture.clientID || len(form["resource"]) != 1 || form.Get("resource") != resource || form.Get("client_secret") != "" || form.Get("client_assertion") != "" || form.Get("code_verifier") == "" {
		t.Fatalf("public token form = %v", form)
	}
}

func TestDirectMCPDCR_Scenario2_RestartRestoresRegistrationGrantAndReadTool(t *testing.T) {
	fixture := newDCRMetadataFixture(t)
	resource := fixture.server.URL + "/gw/mcp"
	store := newDCRMemoryStore(t)
	opts := fixture.options(t, store)
	opts.Presenter = OAuthPresenterFunc(func(_ context.Context, raw string) (*auth.AuthorizationResult, error) {
		u, _ := url.Parse(raw)
		return &auth.AuthorizationResult{Code: "fixture-code", State: u.Query().Get("state"), Iss: fixture.server.URL}, nil
	})
	prepared, path, err := PrepareOAuthDCRLogin(context.Background(), resource, opts, OAuthDCRLoginReuse)
	if err != nil {
		t.Fatal(err)
	}
	prepared.RedirectURL = "http://127.0.0.1:49152" + path
	controller, err := NewOAuthController(context.Background(), resource, prepared)
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodGet, resource, nil)
	resp := &http.Response{StatusCode: http.StatusUnauthorized, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(""))}
	resp.Header.Set("WWW-Authenticate", `Bearer scope="openid"`)
	if err := controller.Authorize(context.Background(), req, resp); err != nil {
		t.Fatal(err)
	}
	_ = controller.Close()

	restored, err := NewOAuthController(context.Background(), resource, fixture.options(t, store))
	if err != nil {
		t.Fatalf("restore controller: %v", err)
	}
	defer restored.Close()
	source, err := restored.TokenSource(context.Background())
	if err != nil || source == nil {
		t.Fatalf("restored token source = %v, %v", source, err)
	}
	token, err := source.Token()
	if err != nil || token.AccessToken != "dcr-access" || token.RefreshToken != "" {
		t.Fatalf("restored token = %#v, %v", token, err)
	}
	fixture.mu.Lock()
	registrations, exchanges := fixture.registerCount, fixture.tokenCount
	fixture.mu.Unlock()
	if registrations != 1 || exchanges != 1 {
		t.Fatalf("restart network counts register=%d token=%d", registrations, exchanges)
	}
}

func TestADR_0325_DirectDCRRejectsUnsolicitedRefreshToken(t *testing.T) {
	fixture := newDCRMetadataFixture(t)
	fixture.unsolicitedRefresh = true
	resource := fixture.server.URL + "/gw/mcp"
	store := newDCRMemoryStore(t)
	opts := fixture.options(t, store)
	opts.Presenter = OAuthPresenterFunc(func(_ context.Context, raw string) (*auth.AuthorizationResult, error) {
		u, _ := url.Parse(raw)
		return &auth.AuthorizationResult{Code: "fixture-code", State: u.Query().Get("state"), Iss: fixture.server.URL}, nil
	})
	prepared, path, err := PrepareOAuthDCRLogin(context.Background(), resource, opts, OAuthDCRLoginReuse)
	if err != nil {
		t.Fatal(err)
	}
	prepared.RedirectURL = "http://127.0.0.1:49152" + path
	controller, err := NewOAuthController(context.Background(), resource, prepared)
	if err != nil {
		t.Fatal(err)
	}
	defer controller.Close()
	req, _ := http.NewRequest(http.MethodGet, resource, nil)
	resp := &http.Response{StatusCode: http.StatusUnauthorized, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(""))}
	resp.Header.Set("WWW-Authenticate", `Bearer scope="openid"`)
	if err := controller.Authorize(context.Background(), req, resp); err == nil {
		t.Fatal("unsolicited refresh token was accepted")
	}
	source, err := controller.TokenSource(context.Background())
	if err != nil || source != nil {
		t.Fatalf("unsolicited refresh persisted source = %v, %v", source, err)
	}
}

func TestADR_0325_DirectDCRExpiryRequiresLoginWithoutRefresh(t *testing.T) {
	fixture := newDCRMetadataFixture(t)
	resource := fixture.server.URL + "/gw/mcp"
	store := newDCRMemoryStore(t)
	opts := fixture.options(t, store)
	prepared, path, err := PrepareOAuthDCRLogin(context.Background(), resource, opts, OAuthDCRLoginReuse)
	if err != nil {
		t.Fatal(err)
	}
	prepared.RedirectURL = "http://127.0.0.1:49152" + path
	controller, err := NewOAuthController(context.Background(), resource, prepared)
	if err != nil {
		t.Fatal(err)
	}
	generation := controller.state.registration.generation
	clientID := controller.state.registration.clientID
	identity := oauthCredentialIdentity{Profile: opts.Subject.Profile, Principal: opts.Subject.Principal, Resource: resource, Issuer: opts.Issuer, ClientKind: "dcr", ClientID: clientID}
	key, _ := oauthDCRCredentialKey(identity, generation)
	record, _ := store.Get(context.Background(), key)
	cfg := &oauth2.Config{ClientID: clientID, Endpoint: oauth2.Endpoint{TokenURL: fixture.server.URL + "/oauth/token", AuthStyle: oauth2.AuthStyleAutoDetect}, RedirectURL: prepared.RedirectURL, Scopes: []string{"openid"}}
	expired := &oauth2.Token{AccessToken: "expired", TokenType: "Bearer", Expiry: time.Now().Add(-time.Hour)}
	grant := newOAuthDCRActiveGrant(identity, generation, cfg, expired)
	value, _ := encodeOAuthDCRGrant(grant, identity, generation, controller.state.origins)
	_, err = store.Put(context.Background(), key, value, &record.Version)
	if err != nil {
		t.Fatal(err)
	}
	_ = controller.Close()
	restored, err := NewOAuthController(context.Background(), resource, fixture.options(t, store))
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	source, _ := restored.TokenSource(context.Background())
	if source == nil {
		t.Fatal("expired durable grant was not restored for login-required classification")
	}
	if _, err := source.Token(); !errors.Is(err, ErrOAuthLoginRequired) {
		t.Fatalf("expired DCR token error = %v", err)
	}
	fixture.mu.Lock()
	exchanges := fixture.tokenCount
	fixture.mu.Unlock()
	if exchanges != 0 {
		t.Fatalf("expiry caused %d token requests", exchanges)
	}
}

func TestADR_0325_DirectDCRGrantResetFencesStaleWriters(t *testing.T) {
	fixture := newDCRMetadataFixture(t)
	resource := fixture.server.URL + "/gw/mcp"
	store := newDCRMemoryStore(t)
	opts := fixture.options(t, store)
	prepared, path, err := PrepareOAuthDCRLogin(context.Background(), resource, opts, OAuthDCRLoginReuse)
	if err != nil {
		t.Fatal(err)
	}
	prepared.RedirectURL = "http://127.0.0.1:49152" + path
	controller, err := NewOAuthController(context.Background(), resource, prepared)
	if err != nil {
		t.Fatal(err)
	}
	defer controller.Close()
	if err := controller.state.beginAuthorization(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := controller.state.reset(context.Background()); err != nil {
		t.Fatal(err)
	}
	cfg := &oauth2.Config{ClientID: controller.state.registration.clientID, Endpoint: oauth2.Endpoint{TokenURL: fixture.server.URL + "/oauth/token", AuthStyle: oauth2.AuthStyleAutoDetect}, RedirectURL: prepared.RedirectURL, Scopes: []string{"openid"}}
	_, err = controller.state.newTokenSource(context.Background(), cfg, &oauth2.Token{AccessToken: "stale", TokenType: "Bearer", Expiry: time.Now().Add(time.Hour)})
	if !errors.Is(err, ErrOAuthLoginRequired) {
		t.Fatalf("stale authorization write error = %v, want login required", err)
	}
	controller.state.mu.Lock()
	state := controller.state.dcrGrant.State
	controller.state.mu.Unlock()
	if state != "reset" {
		t.Fatalf("stale writer resurrected grant state %q", state)
	}
}
