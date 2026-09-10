package mcp

import (
	"bytes"
	"context"
	"encoding/base64"
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
	mu                      sync.Mutex
	server                  *httptest.Server
	registration            string
	issuerOverride          string
	resourceValue           string
	authServers             []string
	codeMethods             []string
	grantTypes              []string
	authMethods             []string
	responseTypes           []string
	scopes                  []string
	registerCount           int
	tokenCount              int
	tokenForms              []url.Values
	clientID                string
	clientIDIssuedAt        *int64
	cosmeticSuffix          string
	unsolicitedRefresh      bool
	accessToken             string
	refreshToken            string
	registrationAccessToken string
	rejectPortVariation     bool
	registeredRedirect      string
	registrationStarted     chan struct{}
	registrationRelease     <-chan struct{}
	registrationStatus      int
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
			if f.registrationStarted != nil {
				close(f.registrationStarted)
				f.registrationStarted = nil
			}
			if f.registrationRelease != nil {
				<-f.registrationRelease
			}
			if f.registrationStatus != 0 {
				http.Error(w, `{"error":"registration_outcome_unknown"}`, f.registrationStatus)
				return
			}
			var request oauthex.ClientRegistrationMetadata
			_ = json.NewDecoder(r.Body).Decode(&request)
			if len(request.RedirectURIs) == 1 {
				f.registeredRedirect = request.RedirectURIs[0]
			}
			response := map[string]any{
				"client_id": f.clientID, "token_endpoint_auth_method": "none", "redirect_uris": request.RedirectURIs,
				"grant_types": request.GrantTypes, "response_types": request.ResponseTypes, "scope": request.Scope,
			}
			if f.clientIDIssuedAt != nil {
				response["client_id_issued_at"] = *f.clientIDIssuedAt
			}
			if f.registrationAccessToken != "" {
				response["registration_access_token"] = f.registrationAccessToken
			}
			_ = json.NewEncoder(w).Encode(response)
		case r.URL.Path == "/oauth/token":
			f.tokenCount++
			_ = r.ParseForm()
			f.tokenForms = append(f.tokenForms, r.PostForm)
			if f.rejectPortVariation && r.PostForm.Get("redirect_uri") != f.registeredRedirect {
				http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
				return
			}
			access := f.accessToken
			if access == "" {
				access = "dcr-access"
			}
			response := map[string]any{"access_token": access, "token_type": "Bearer", "expires_in": 3600, "scope": "openid"}
			if f.unsolicitedRefresh {
				refresh := f.refreshToken
				if refresh == "" {
					refresh = "unsolicited-refresh"
				}
				response["refresh_token"] = refresh
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

func TestValidateDCRAuthorizationURL(t *testing.T) {
	const resource = "https://connector.example/gw/mcp"
	valid := "https://issuer.example/authorize?scope=openid&resource=https%3A%2F%2Fconnector.example%2Fgw%2Fmcp"
	for _, tc := range []struct {
		name string
		raw  string
		ok   bool
	}{
		{name: "exact", raw: valid, ok: true},
		{name: "missing scope", raw: strings.Replace(valid, "scope=openid&", "", 1)},
		{name: "challenge scope union", raw: strings.Replace(valid, "scope=openid", "scope=openid+admin", 1)},
		{name: "duplicate scope", raw: valid + "&scope=openid"},
		{name: "missing resource", raw: strings.Split(valid, "&resource=")[0]},
		{name: "wrong resource", raw: strings.Replace(valid, url.QueryEscape(resource), url.QueryEscape("https://connector.example/mcp"), 1)},
		{name: "duplicate resource", raw: valid + "&resource=" + url.QueryEscape(resource)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateDCRAuthorizationURL(tc.raw, resource)
			if (err == nil) != tc.ok {
				t.Fatalf("ValidateDCRAuthorizationURL() error = %v, want success %t", err, tc.ok)
			}
		})
	}
}

func TestADR_0325_DCRPersistedFormatsRejectMalformedRecords(t *testing.T) {
	generation := base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	path := oauthDCRCallbackPrefix + base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32))
	identity := oauthDCRIdentity{Profile: "connector", Principal: "local-user", Resource: "https://connector.example/gw/mcp", Issuer: "https://issuer.example"}
	metadata := oauthDCRMetadata{
		Issuer: identity.Issuer, Resource: identity.Resource, RedirectPolicy: oauthDCRRedirectPolicy,
		RedirectPath: path, TokenEndpointAuthMethod: "none", GrantTypes: []string{"authorization_code"},
		ResponseTypes: []string{"code"}, Scopes: []string{"openid"},
	}
	registration := oauthDCRRecord{
		Schema: oauthDCRRegistrationSchema, Version: oauthDCRRegistrationVersion, Identity: identity,
		Generation: generation, State: oauthDCRStateReady, AttemptStartedAt: "2026-09-10T10:00:00Z",
		Metadata: metadata, MetadataFingerprint: fingerprintDCRMetadata(metadata),
		Registration: &oauthDCRRegistration{ClientID: "public-client", RegisteredRedirectURI: "http://127.0.0.1:49152" + path},
	}
	validRegistration, err := json.Marshal(registration)
	if err != nil {
		t.Fatal(err)
	}
	registrationJSON := func(mutate func(*oauthDCRRecord)) []byte {
		candidate := registration
		candidate.Metadata = registration.Metadata
		candidate.Registration = &oauthDCRRegistration{ClientID: registration.Registration.ClientID, RegisteredRedirectURI: registration.Registration.RegisteredRedirectURI}
		mutate(&candidate)
		value, marshalErr := json.Marshal(candidate)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		return value
	}
	registrationCases := map[string][]byte{
		"duplicate key":        append([]byte(`{"version":1,`), validRegistration[1:]...),
		"unknown key":          append(validRegistration[:len(validRegistration)-1], []byte(`,"unknown":true}`)...),
		"trailing JSON":        append(append([]byte(nil), validRegistration...), []byte(` {}`)...),
		"oversized value":      bytes.Repeat([]byte{'x'}, credentialstore.MaxValueBytes+1),
		"malformed generation": registrationJSON(func(record *oauthDCRRecord) { record.Generation = "not-base64url" }),
		"malformed callback path": registrationJSON(func(record *oauthDCRRecord) {
			record.Metadata.RedirectPath = "/oauth/callback/not-base64url"
			record.MetadataFingerprint = fingerprintDCRMetadata(record.Metadata)
			record.Registration.RegisteredRedirectURI = "http://127.0.0.1:49152" + record.Metadata.RedirectPath
		}),
		"non-UTC timestamp":             registrationJSON(func(record *oauthDCRRecord) { record.AttemptStartedAt = "2026-09-10T12:00:00+02:00" }),
		"unsupported state":             registrationJSON(func(record *oauthDCRRecord) { record.State = "retired" }),
		"unsupported version":           registrationJSON(func(record *oauthDCRRecord) { record.Version++ }),
		"metadata fingerprint mismatch": registrationJSON(func(record *oauthDCRRecord) { record.MetadataFingerprint = strings.Repeat("0", 64) }),
		"metadata identity mismatch": registrationJSON(func(record *oauthDCRRecord) {
			record.Metadata.Issuer = "https://other.example"
			record.MetadataFingerprint = fingerprintDCRMetadata(record.Metadata)
		}),
		"record identity mismatch": registrationJSON(func(record *oauthDCRRecord) { record.Identity.Principal = "other-user" }),
	}
	for name, value := range registrationCases {
		t.Run("registration/"+name, func(t *testing.T) {
			if _, decodeErr := decodeOAuthDCRRecord(value, identity); decodeErr == nil {
				t.Fatal("invalid registration record was accepted")
			}
		})
	}

	grantIdentity := oauthCredentialIdentity{
		Profile: identity.Profile, Principal: identity.Principal, Resource: identity.Resource,
		Issuer: identity.Issuer, ClientKind: oauthDCRClientKind, ClientID: "public-client",
	}
	config := &oauth2.Config{Endpoint: oauth2.Endpoint{TokenURL: identity.Issuer + "/oauth/token"}, RedirectURL: registration.Registration.RegisteredRedirectURI}
	grant := newOAuthDCRActiveGrant(grantIdentity, generation, config, &oauth2.Token{AccessToken: "access", TokenType: "Bearer", Expiry: time.Date(2026, 9, 10, 11, 0, 0, 0, time.UTC)})
	validGrant, err := json.Marshal(grant)
	if err != nil {
		t.Fatal(err)
	}
	grantJSON := func(mutate func(*oauthDCRGrantEnvelope)) []byte {
		candidate := grant
		token, authorization := *grant.Token, *grant.Authorization
		candidate.Token, candidate.Authorization = &token, &authorization
		mutate(&candidate)
		value, marshalErr := json.Marshal(candidate)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		return value
	}
	origins := map[string]struct{}{identity.Issuer: {}}
	grantCases := map[string][]byte{
		"duplicate key":        append([]byte(`{"version":2,`), validGrant[1:]...),
		"unknown key":          append(validGrant[:len(validGrant)-1], []byte(`,"unknown":true}`)...),
		"trailing JSON":        append(append([]byte(nil), validGrant...), []byte(` []`)...),
		"oversized value":      bytes.Repeat([]byte{'x'}, credentialstore.MaxValueBytes+1),
		"malformed generation": grantJSON(func(record *oauthDCRGrantEnvelope) { record.RegistrationGeneration = "not-base64url" }),
		"malformed timestamp":  grantJSON(func(record *oauthDCRGrantEnvelope) { record.Token.Expiry = "tomorrow" }),
		"unsupported state":    grantJSON(func(record *oauthDCRGrantEnvelope) { record.State = "expired" }),
		"unsupported version":  grantJSON(func(record *oauthDCRGrantEnvelope) { record.Version++ }),
		"identity mismatch":    grantJSON(func(record *oauthDCRGrantEnvelope) { record.Identity.ClientID = "other-client" }),
		"generation mismatch": grantJSON(func(record *oauthDCRGrantEnvelope) {
			record.RegistrationGeneration = base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{2}, 32))
		}),
		"off-origin token endpoint": grantJSON(func(record *oauthDCRGrantEnvelope) {
			record.Authorization.TokenURL = "https://evil.example/oauth/token"
		}),
		"invalid token endpoint": grantJSON(func(record *oauthDCRGrantEnvelope) {
			record.Authorization.TokenURL = "https://issuer.example/oauth/token?secret=value"
		}),
	}
	for name, value := range grantCases {
		t.Run("grant/"+name, func(t *testing.T) {
			if _, decodeErr := decodeOAuthDCRGrant(value, grantIdentity, generation, origins); decodeErr == nil {
				t.Fatal("invalid grant record was accepted")
			}
		})
	}
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

func TestADR_0325_DirectDCRRegistrationIssuedAtIsNonnegativeAndPresencePreserved(t *testing.T) {
	for _, tc := range []struct {
		name     string
		issuedAt int64
		wantOK   bool
	}{
		{name: "negative rejected", issuedAt: -1},
		{name: "Unix epoch preserved", issuedAt: 0, wantOK: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newDCRMetadataFixture(t)
			fixture.clientIDIssuedAt = &tc.issuedAt
			resource := fixture.server.URL + "/gw/mcp"
			store := newDCRMemoryStore(t)
			opts := fixture.options(t, store)
			prepared, path, err := PrepareOAuthDCRLogin(context.Background(), resource, opts, OAuthDCRLoginReuse)
			if err != nil {
				t.Fatal(err)
			}
			prepared.RedirectURL = "http://127.0.0.1:49152" + path
			controller, err := NewOAuthController(context.Background(), resource, prepared)
			if (err == nil) != tc.wantOK {
				t.Fatalf("registration error = %v, want success %t", err, tc.wantOK)
			}
			if controller != nil {
				_ = controller.Close()
			}
			identity := oauthDCRIdentity{Profile: opts.Subject.Profile, Principal: opts.Subject.Principal, Resource: resource, Issuer: opts.Issuer}
			key, keyErr := oauthDCRRegistrationKey(identity)
			if keyErr != nil {
				t.Fatal(keyErr)
			}
			record, getErr := store.Get(context.Background(), key)
			if getErr != nil {
				t.Fatal(getErr)
			}
			stored, decodeErr := decodeOAuthDCRRecord(record.Value, identity)
			if tc.wantOK {
				if decodeErr != nil || stored.Registration == nil || stored.Registration.ClientIDIssuedAt == nil || *stored.Registration.ClientIDIssuedAt != 0 {
					t.Fatalf("stored epoch registration = %#v, error %v", stored.Registration, decodeErr)
				}
			} else if decodeErr == nil && stored.State == oauthDCRStateReady {
				t.Fatalf("negative issuance time was persisted ready: %#v", stored.Registration)
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

func TestOAuthDCRRegistrationResetRejectsCorruptGrantWithoutMutation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func([]byte) []byte
		fail   bool
	}{
		{name: "corrupt", mutate: func([]byte) []byte { return []byte(`{"schema":"corrupt"}`) }},
		{name: "unsupported", mutate: func(value []byte) []byte {
			return []byte(strings.Replace(string(value), `"version":2`, `"version":3`, 1))
		}},
		{name: "identity mismatch", mutate: func(value []byte) []byte {
			return []byte(strings.Replace(string(value), `"principal":"local-user"`, `"principal":"other-user"`, 1))
		}},
		{name: "backend failure", fail: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newDCRMetadataFixture(t)
			resource, store := fixture.server.URL+"/gw/mcp", newDCRMemoryStore(t)
			opts := fixture.options(t, store)
			controller := authorizeDCRForProof(t, fixture, resource, opts)
			generation, clientID := controller.state.registration.generation, controller.state.registration.clientID
			_ = controller.Close()

			grantIdentity := oauthCredentialIdentity{
				Profile: opts.Subject.Profile, Principal: opts.Subject.Principal, Resource: resource,
				Issuer: opts.Issuer, ClientKind: oauthDCRClientKind, ClientID: clientID,
			}
			grantKey, err := oauthDCRCredentialKey(grantIdentity, generation)
			if err != nil {
				t.Fatal(err)
			}
			grant, err := store.Get(context.Background(), grantKey)
			if err != nil {
				t.Fatal(err)
			}
			var resetStore credentialstore.Store
			resetStore = store
			if tc.fail {
				wrapped := &scriptedDCRStore{Store: store}
				wrapped.get = func(ctx context.Context, key []byte) (credentialstore.Record, error) {
					if string(key) == string(grantKey) {
						return credentialstore.Record{}, credentialstore.ErrUnavailable
					}
					return store.Get(ctx, key)
				}
				resetStore = wrapped
			} else {
				mutated := tc.mutate(grant.Value)
				if string(mutated) == string(grant.Value) {
					t.Fatal("fixture did not corrupt the grant")
				}
				if _, err := store.Put(context.Background(), grantKey, mutated, &grant.Version); err != nil {
					t.Fatal(err)
				}
			}

			registrationIdentity := oauthDCRIdentity{Profile: opts.Subject.Profile, Principal: opts.Subject.Principal, Resource: resource, Issuer: opts.Issuer}
			registrationKey, err := oauthDCRRegistrationKey(registrationIdentity)
			if err != nil {
				t.Fatal(err)
			}
			before, err := store.Get(context.Background(), registrationKey)
			if err != nil {
				t.Fatal(err)
			}
			resetOpts := fixture.options(t, resetStore)
			if _, _, err := PrepareOAuthDCRLogin(context.Background(), resource, resetOpts, OAuthDCRLoginResetRegistration); !errors.Is(err, ErrOAuthDCRRecoveryRequired) {
				t.Fatalf("reset with invalid current grant = %v, want recovery-required", err)
			}
			after, err := store.Get(context.Background(), registrationKey)
			if err != nil {
				t.Fatal(err)
			}
			if string(after.Value) != string(before.Value) || !after.Version.Equal(before.Version) {
				t.Fatal("reset mutated the registration after grant validation failed")
			}
		})
	}
}

func TestOAuthDCRRegistrationResetAllowsValidOrMissingGrant(t *testing.T) {
	for _, state := range []string{"active", "reset", "missing"} {
		t.Run(state, func(t *testing.T) {
			fixture := newDCRMetadataFixture(t)
			resource, store := fixture.server.URL+"/gw/mcp", newDCRMemoryStore(t)
			opts := fixture.options(t, store)
			var controller *OAuthController
			if state == "active" {
				controller = authorizeDCRForProof(t, fixture, resource, opts)
			} else {
				prepared, path, err := PrepareOAuthDCRLogin(context.Background(), resource, opts, OAuthDCRLoginReuse)
				if err != nil {
					t.Fatal(err)
				}
				prepared.RedirectURL = "http://127.0.0.1:49152" + path
				controller, err = NewOAuthController(context.Background(), resource, prepared)
				if err != nil {
					t.Fatal(err)
				}
			}
			generation, clientID := controller.state.registration.generation, controller.state.registration.clientID
			_ = controller.Close()
			if state == "missing" {
				identity := oauthCredentialIdentity{Profile: opts.Subject.Profile, Principal: opts.Subject.Principal, Resource: resource, Issuer: opts.Issuer, ClientKind: oauthDCRClientKind, ClientID: clientID}
				key, _ := oauthDCRCredentialKey(identity, generation)
				record, err := store.Get(context.Background(), key)
				if err != nil {
					t.Fatal(err)
				}
				if err := store.Delete(context.Background(), key, record.Version); err != nil {
					t.Fatal(err)
				}
			}
			if _, path, err := PrepareOAuthDCRLogin(context.Background(), resource, opts, OAuthDCRLoginResetRegistration); err != nil || path == "" {
				t.Fatalf("reset with %s grant = path %q, error %v", state, path, err)
			}
		})
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
