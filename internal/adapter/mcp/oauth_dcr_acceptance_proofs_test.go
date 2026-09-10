package mcp

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"golang.org/x/oauth2"
)

func dcrChallenge(t *testing.T, resource string) (*http.Request, *http.Response) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, resource, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp := &http.Response{StatusCode: http.StatusUnauthorized, Header: http.Header{"WWW-Authenticate": {`Bearer scope="openid"`}}, Body: io.NopCloser(strings.NewReader(""))}
	return req, resp
}

func TestADR_0325_DirectDCRStaleRegistrationLifecycle(t *testing.T) {
	t.Run("clean and registration only", func(t *testing.T) {
		fixture := newDCRMetadataFixture(t)
		resource, store := fixture.server.URL+"/gw/mcp", newDCRMemoryStore(t)
		opts := fixture.options(t, store)
		if _, err := NewOAuthController(context.Background(), resource, opts); !errors.Is(err, ErrOAuthLoginRequired) {
			t.Fatalf("clean bootstrap = %v", err)
		}
		if _, _, err := PrepareOAuthDCRLogin(context.Background(), resource, opts, OAuthDCRLoginReuse); err != nil {
			t.Fatal(err)
		}
		if _, err := NewOAuthController(context.Background(), resource, opts); !errors.Is(err, ErrOAuthDCRRecoveryRequired) {
			t.Fatalf("registration-only restore = %v", err)
		}
		if fixture.registerCount != 0 || fixture.tokenCount != 0 {
			t.Fatalf("registration-only state used network: %d/%d", fixture.registerCount, fixture.tokenCount)
		}
	})

	t.Run("stale registration generation is fenced", func(t *testing.T) {
		fixture := newDCRMetadataFixture(t)
		resource, store := fixture.server.URL+"/gw/mcp", newDCRMemoryStore(t)
		opts := fixture.options(t, store)
		old, oldPath, err := PrepareOAuthDCRLogin(context.Background(), resource, opts, OAuthDCRLoginReuse)
		if err != nil {
			t.Fatal(err)
		}
		newer, newerPath, err := PrepareOAuthDCRLogin(context.Background(), resource, opts, OAuthDCRLoginRetryRegistration)
		if err != nil {
			t.Fatal(err)
		}
		old.RedirectURL, newer.RedirectURL = "http://127.0.0.1:49152"+oldPath, "http://127.0.0.1:49153"+newerPath
		if _, err := NewOAuthController(context.Background(), resource, old); !errors.Is(err, ErrOAuthDCRRecoveryRequired) {
			t.Fatalf("stale publication = %v", err)
		}
		winner, err := NewOAuthController(context.Background(), resource, newer)
		if err != nil {
			t.Fatal(err)
		}
		defer winner.Close()
		reused, path, err := PrepareOAuthDCRLogin(context.Background(), resource, opts, OAuthDCRLoginReuse)
		if err != nil {
			t.Fatal(err)
		}
		if path != newerPath || reused.dcr == nil || reused.dcr.generation != newer.dcrTicket.record.Generation {
			t.Fatalf("ready winner not retained: %q %#v", path, reused.dcr)
		}
	})

	t.Run("grant only and corrupt registration fail closed", func(t *testing.T) {
		fixture := newDCRMetadataFixture(t)
		resource, store := fixture.server.URL+"/gw/mcp", newDCRMemoryStore(t)
		opts := fixture.options(t, store)
		controller := authorizeDCRForProof(t, fixture, resource, opts)
		_ = controller.Close()
		identity := oauthDCRIdentity{Profile: opts.Subject.Profile, Principal: opts.Subject.Principal, Resource: resource, Issuer: opts.Issuer}
		key, _ := oauthDCRRegistrationKey(identity)
		record, _ := store.Get(context.Background(), key)
		if err := store.Delete(context.Background(), key, record.Version); err != nil {
			t.Fatal(err)
		}
		if _, err := NewOAuthController(context.Background(), resource, opts); !errors.Is(err, ErrOAuthLoginRequired) {
			t.Fatalf("grant-only restore = %v", err)
		}
		if _, err := store.Put(context.Background(), key, []byte(`{"schema":"corrupt"}`), nil); err != nil {
			t.Fatal(err)
		}
		if _, err := NewOAuthController(context.Background(), resource, opts); !errors.Is(err, ErrOAuthDCRRecoveryRequired) {
			t.Fatalf("corrupt registration = %v", err)
		}
	})

	t.Run("reset tombstone fences in-flight authorization", func(t *testing.T) {
		fixture := newDCRMetadataFixture(t)
		resource, store := fixture.server.URL+"/gw/mcp", newDCRMemoryStore(t)
		opts := fixture.options(t, store)
		opts.Presenter = proofPresenter(fixture)
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
		if _, err := controller.state.newTokenSource(context.Background(), cfg, &oauth2.Token{AccessToken: "stale-canary", TokenType: "Bearer", Expiry: time.Now().Add(time.Hour)}); !errors.Is(err, ErrOAuthLoginRequired) {
			t.Fatalf("stale authorization = %v", err)
		}
		if controller.state.dcrGrant.State != "reset" {
			t.Fatalf("stale authorization resurrected %q", controller.state.dcrGrant.State)
		}
	})

	t.Run("unsolicited refresh and expiry", func(t *testing.T) {
		fixture := newDCRMetadataFixture(t)
		fixture.unsolicitedRefresh = true
		resource, store := fixture.server.URL+"/gw/mcp", newDCRMemoryStore(t)
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
		req, resp := dcrChallenge(t, resource)
		if err := controller.Authorize(context.Background(), req, resp); err == nil {
			t.Fatal("unsolicited refresh token was accepted")
		}
		if source, _ := controller.TokenSource(context.Background()); source != nil {
			t.Fatal("unsolicited refresh token was persisted")
		}
		_ = controller.Close()

		fixture = newDCRMetadataFixture(t)
		resource, store = fixture.server.URL+"/gw/mcp", newDCRMemoryStore(t)
		opts = fixture.options(t, store)
		controller = authorizeDCRForProof(t, fixture, resource, opts)
		identity := oauthCredentialIdentity{Profile: opts.Subject.Profile, Principal: opts.Subject.Principal, Resource: resource, Issuer: opts.Issuer, ClientKind: oauthDCRClientKind, ClientID: fixture.clientID}
		key, _ := oauthDCRCredentialKey(identity, controller.state.registration.generation)
		record, _ := store.Get(context.Background(), key)
		regKey, _ := oauthDCRRegistrationKey(oauthDCRIdentity{Profile: opts.Subject.Profile, Principal: opts.Subject.Principal, Resource: resource, Issuer: opts.Issuer})
		regStored, _ := store.Get(context.Background(), regKey)
		regRecord, _ := decodeOAuthDCRRecord(regStored.Value, oauthDCRIdentity{Profile: opts.Subject.Profile, Principal: opts.Subject.Principal, Resource: resource, Issuer: opts.Issuer})
		cfg := &oauth2.Config{ClientID: fixture.clientID, Endpoint: oauth2.Endpoint{TokenURL: fixture.server.URL + "/oauth/token", AuthStyle: oauth2.AuthStyleAutoDetect}, RedirectURL: regRecord.Registration.RegisteredRedirectURI, Scopes: []string{"openid"}}
		grant := newOAuthDCRActiveGrant(identity, controller.state.registration.generation, cfg, &oauth2.Token{AccessToken: "expired", TokenType: "Bearer", Expiry: time.Now().Add(-time.Hour)})
		value, _ := encodeOAuthDCRGrant(grant, identity, controller.state.registration.generation, controller.state.origins)
		if _, err := store.Put(context.Background(), key, value, &record.Version); err != nil {
			t.Fatal(err)
		}
		_ = controller.Close()
		restored, err := NewOAuthController(context.Background(), resource, opts)
		if err != nil {
			t.Fatal(err)
		}
		defer restored.Close()
		source, _ := restored.TokenSource(context.Background())
		if _, err := source.Token(); !errors.Is(err, ErrOAuthLoginRequired) {
			t.Fatalf("expired token = %v", err)
		}
		if fixture.tokenCount != 1 {
			t.Fatalf("expiry triggered hidden exchange/refresh: %d", fixture.tokenCount)
		}
	})

	t.Run("mismatched grant is not adopted", func(t *testing.T) {
		fixture := newDCRMetadataFixture(t)
		resource, store := fixture.server.URL+"/gw/mcp", newDCRMemoryStore(t)
		opts := fixture.options(t, store)
		controller := authorizeDCRForProof(t, fixture, resource, opts)
		generation, clientID := controller.state.registration.generation, controller.state.registration.clientID
		_ = controller.Close()
		identity := oauthCredentialIdentity{Profile: opts.Subject.Profile, Principal: opts.Subject.Principal, Resource: resource, Issuer: opts.Issuer, ClientKind: oauthDCRClientKind, ClientID: clientID}
		key, _ := oauthDCRCredentialKey(identity, generation)
		record, _ := store.Get(context.Background(), key)
		mismatch := strings.Replace(string(record.Value), `"principal":"local-user"`, `"principal":"other-user"`, 1)
		if _, err := store.Put(context.Background(), key, []byte(mismatch), &record.Version); err != nil {
			t.Fatal(err)
		}
		if _, err := NewOAuthController(context.Background(), resource, opts); err == nil {
			t.Fatal("cross-identity grant was adopted")
		}
		if fixture.registerCount != 1 || fixture.tokenCount != 1 {
			t.Fatalf("mismatch triggered network: %d/%d", fixture.registerCount, fixture.tokenCount)
		}
	})
}

func proofPresenter(fixture *dcrMetadataFixture) OAuthPresenter {
	return OAuthPresenterFunc(func(_ context.Context, raw string) (*auth.AuthorizationResult, error) {
		u, _ := url.Parse(raw)
		return &auth.AuthorizationResult{Code: "fixture-code", State: u.Query().Get("state"), Iss: fixture.server.URL}, nil
	})
}

func authorizeDCRForProof(t *testing.T, fixture *dcrMetadataFixture, resource string, opts OAuthOptions) *OAuthController {
	t.Helper()
	opts.Presenter = proofPresenter(fixture)
	prepared, path, err := PrepareOAuthDCRLogin(context.Background(), resource, opts, OAuthDCRLoginReuse)
	if err != nil {
		t.Fatal(err)
	}
	prepared.RedirectURL = "http://127.0.0.1:49152" + path
	controller, err := NewOAuthController(context.Background(), resource, prepared)
	if err != nil {
		t.Fatal(err)
	}
	req, resp := dcrChallenge(t, resource)
	if err := controller.Authorize(context.Background(), req, resp); err != nil {
		t.Fatal(err)
	}
	return controller
}

func TestInvariant_direct_mcp_dcr_secret_redaction(t *testing.T) {
	const (
		clientID           = "client-id-redaction-canary"
		accessToken        = "access-token-redaction-canary"
		code               = "authorization-code-redaction-canary"
		registrationAccess = "registration-access-redaction-canary"
		unsolicitedRefresh = "refresh-token-redaction-canary"
	)
	fixture := newDCRMetadataFixture(t)
	fixture.clientID, fixture.accessToken, fixture.registrationAccessToken = clientID, accessToken, registrationAccess
	resource, store := fixture.server.URL+"/gw/mcp", newDCRMemoryStore(t)
	var presented string
	opts := fixture.options(t, store)
	opts.Presenter = OAuthPresenterFunc(func(_ context.Context, raw string) (*auth.AuthorizationResult, error) {
		presented = raw
		u, _ := url.Parse(raw)
		return &auth.AuthorizationResult{Code: code, State: u.Query().Get("state"), Iss: fixture.server.URL}, nil
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
	req, resp := dcrChallenge(t, resource)
	if err := controller.Authorize(context.Background(), req, resp); err != nil {
		t.Fatal(err)
	}
	identity := oauthDCRIdentity{Profile: opts.Subject.Profile, Principal: opts.Subject.Principal, Resource: resource, Issuer: opts.Issuer}
	registrationKey, _ := oauthDCRRegistrationKey(identity)
	registration, _ := store.Get(context.Background(), registrationKey)
	grantIdentity := oauthCredentialIdentity{Profile: opts.Subject.Profile, Principal: opts.Subject.Principal, Resource: resource, Issuer: opts.Issuer, ClientKind: oauthDCRClientKind, ClientID: clientID}
	grantKey, _ := oauthDCRCredentialKey(grantIdentity, controller.state.registration.generation)
	grant, _ := store.Get(context.Background(), grantKey)
	registrationJSON, grantJSON := string(registration.Value), string(grant.Value)
	if !strings.Contains(registrationJSON, clientID) || strings.Contains(registrationJSON, accessToken) {
		t.Fatal("registration projection did not contain only its intentional client identity")
	}
	if !strings.Contains(grantJSON, clientID) || !strings.Contains(grantJSON, accessToken) {
		t.Fatal("encrypted credential projection omitted its intentional client/access credential")
	}
	u, _ := url.Parse(presented)
	state := u.Query().Get("state")
	fixture.mu.Lock()
	verifier := fixture.tokenForms[0].Get("code_verifier")
	fixture.mu.Unlock()
	if verifier == "" {
		t.Fatal("fixture did not observe the PKCE verifier")
	}
	for _, forbidden := range []string{code, state, presented, verifier, "code_verifier", registrationAccess, unsolicitedRefresh} {
		if forbidden != "" && (strings.Contains(registrationJSON, forbidden) || strings.Contains(grantJSON, forbidden)) {
			t.Fatalf("credential projection leaked transient %q", forbidden)
		}
	}

	bad := newDCRMetadataFixture(t)
	bad.clientID, bad.unsolicitedRefresh, bad.refreshToken, bad.registrationAccessToken = clientID, true, unsolicitedRefresh, registrationAccess
	badResource, badStore := bad.server.URL+"/gw/mcp", newDCRMemoryStore(t)
	badOpts := bad.options(t, badStore)
	badOpts.Presenter = proofPresenter(bad)
	badPrepared, badPath, err := PrepareOAuthDCRLogin(context.Background(), badResource, badOpts, OAuthDCRLoginReuse)
	if err != nil {
		t.Fatal(err)
	}
	badPrepared.RedirectURL = "http://127.0.0.1:49153" + badPath
	badController, err := NewOAuthController(context.Background(), badResource, badPrepared)
	if err != nil {
		t.Fatal(err)
	}
	defer badController.Close()
	req, resp = dcrChallenge(t, badResource)
	authErr := badController.Authorize(context.Background(), req, resp)
	if authErr == nil {
		t.Fatal("unsolicited refresh token was accepted")
	}
	for _, secret := range []string{clientID, unsolicitedRefresh, registrationAccess, badResource, "fixture-code"} {
		if strings.Contains(authErr.Error(), secret) {
			t.Fatalf("returned error leaked %q: %v", secret, authErr)
		}
	}
}

func TestDirectMCPDCR_Scenario2_ReauthorizationRedirectAndScopeBinding(t *testing.T) {
	fixture := newDCRMetadataFixture(t)
	resource, store := fixture.server.URL+"/gw/mcp", newDCRMemoryStore(t)
	var redirects, states, challenges []string
	presenter := OAuthPresenterFunc(func(_ context.Context, raw string) (*auth.AuthorizationResult, error) {
		u, _ := url.Parse(raw)
		q := u.Query()
		redirects, states, challenges = append(redirects, q.Get("redirect_uri")), append(states, q.Get("state")), append(challenges, q.Get("code_challenge"))
		if q.Get("scope") != "openid" || len(q["resource"]) != 1 || q.Get("resource") != resource {
			t.Fatalf("scope/resource drift: %v", q)
		}
		return &auth.AuthorizationResult{Code: "fixture-code", State: q.Get("state"), Iss: fixture.server.URL}, nil
	})
	opts := fixture.options(t, store)
	opts.Presenter = presenter
	prepared, path, err := PrepareOAuthDCRLogin(context.Background(), resource, opts, OAuthDCRLoginReuse)
	if err != nil {
		t.Fatal(err)
	}
	prepared.RedirectURL = "http://127.0.0.1:49152" + path
	controller, err := NewOAuthController(context.Background(), resource, prepared)
	if err != nil {
		t.Fatal(err)
	}
	req, resp := dcrChallenge(t, resource)
	if err := controller.Authorize(context.Background(), req, resp); err != nil {
		t.Fatal(err)
	}
	if err := controller.ResetCredential(context.Background()); err != nil {
		t.Fatal(err)
	}
	reused, reusedPath, err := PrepareOAuthDCRLogin(context.Background(), resource, opts, OAuthDCRLoginReuse)
	if err != nil {
		t.Fatal(err)
	}
	if reusedPath != path {
		t.Fatalf("registration path changed: %q != %q", reusedPath, path)
	}
	reused.Presenter, reused.RedirectURL = presenter, "http://127.0.0.1:49153"+reusedPath
	second, err := NewOAuthController(context.Background(), resource, reused)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	req, resp = dcrChallenge(t, resource)
	if err := second.Authorize(context.Background(), req, resp); err != nil {
		t.Fatal(err)
	}
	if len(redirects) != 2 || redirects[0] == redirects[1] || states[0] == states[1] || challenges[0] == challenges[1] {
		t.Fatalf("reauthorization did not bind fresh port/state/PKCE: redirects=%v states=%v challenges=%v", redirects, states, challenges)
	}
	fixture.mu.Lock()
	registrations := fixture.registerCount
	fixture.mu.Unlock()
	if registrations != 1 {
		t.Fatalf("reauthorization registered %d clients", registrations)
	}

	if err := second.ResetCredential(context.Background()); err != nil {
		t.Fatal(err)
	}
	fixture.mu.Lock()
	fixture.rejectPortVariation = true
	fixture.mu.Unlock()
	rejected, rejectedPath, err := PrepareOAuthDCRLogin(context.Background(), resource, opts, OAuthDCRLoginReuse)
	if err != nil {
		t.Fatal(err)
	}
	rejected.Presenter, rejected.RedirectURL = presenter, "http://127.0.0.1:49154"+rejectedPath
	third, err := NewOAuthController(context.Background(), resource, rejected)
	if err != nil {
		t.Fatal(err)
	}
	defer third.Close()
	req, resp = dcrChallenge(t, resource)
	if err := third.Authorize(context.Background(), req, resp); err == nil {
		t.Fatal("server rejection of redirect-port variation was hidden")
	}
	fixture.mu.Lock()
	registrations = fixture.registerCount
	fixture.mu.Unlock()
	if registrations != 1 {
		t.Fatalf("port rejection silently re-registered: %d", registrations)
	}

	identity := oauthDCRIdentity{Profile: opts.Subject.Profile, Principal: opts.Subject.Principal, Resource: resource, Issuer: opts.Issuer}
	key, _ := oauthDCRRegistrationKey(identity)
	record, _ := store.Get(context.Background(), key)
	drifted := strings.Replace(string(record.Value), `"scopes":["openid"]`, `"scopes":["other"]`, 1)
	if _, err := store.Put(context.Background(), key, []byte(drifted), &record.Version); err != nil {
		t.Fatal(err)
	}
	if _, _, err := PrepareOAuthDCRLogin(context.Background(), resource, opts, OAuthDCRLoginReuse); !errors.Is(err, ErrOAuthDCRRecoveryRequired) {
		t.Fatalf("scope/fingerprint drift = %v", err)
	}
}

func TestADR_0325_DirectDCRUnknownAttemptRecovery(t *testing.T) {
	fixture := newDCRMetadataFixture(t)
	resource, store := fixture.server.URL+"/gw/mcp", newDCRMemoryStore(t)
	opts := fixture.options(t, store)
	old, oldPath, err := PrepareOAuthDCRLogin(context.Background(), resource, opts, OAuthDCRLoginReuse)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := PrepareOAuthDCRLogin(context.Background(), resource, opts, OAuthDCRLoginReuse); !errors.Is(err, ErrOAuthDCRRecoveryRequired) {
		t.Fatalf("pending attempt was automatically retried: %v", err)
	}
	if fixture.registerCount != 0 {
		t.Fatalf("pending recovery posted %d registrations", fixture.registerCount)
	}
	retry, retryPath, err := PrepareOAuthDCRLogin(context.Background(), resource, opts, OAuthDCRLoginRetryRegistration)
	if err != nil {
		t.Fatal(err)
	}
	retry.RedirectURL = "http://127.0.0.1:49153" + retryPath
	winner, err := NewOAuthController(context.Background(), resource, retry)
	if err != nil {
		t.Fatal(err)
	}
	defer winner.Close()
	if fixture.registerCount != 1 {
		t.Fatalf("explicit retry registrations = %d", fixture.registerCount)
	}

	old.RedirectURL = "http://127.0.0.1:49152" + oldPath
	if _, err := NewOAuthController(context.Background(), resource, old); !errors.Is(err, ErrOAuthDCRRecoveryRequired) {
		t.Fatalf("late uncertain writer = %v", err)
	}
	if fixture.registerCount != 2 {
		t.Fatalf("late already-started POST count=%d, want 2 without an automatic retry", fixture.registerCount)
	}
	restored, path, err := PrepareOAuthDCRLogin(context.Background(), resource, opts, OAuthDCRLoginReuse)
	if err != nil {
		t.Fatal(err)
	}
	if path != retryPath || restored.dcr == nil || restored.dcr.generation != retry.dcrTicket.record.Generation {
		t.Fatalf("late error displaced ready winner: path=%q resolved=%#v", path, restored.dcr)
	}
}

func TestDirectMCPDCR_Scenario3_RestartIdentityMismatchFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(string) string
	}{
		{name: "registration identity", mutate: func(value string) string {
			return strings.Replace(value, `"principal":"local-user"`, `"principal":"other-user"`, 1)
		}},
		{name: "grant cross identity", mutate: func(value string) string {
			return strings.Replace(value, `"principal":"local-user"`, `"principal":"other-user"`, 1)
		}},
		{name: "grant generation", mutate: func(value string) string {
			replacement, _ := randomDCRValue()
			start := strings.Index(value, `"registration_generation":"`)
			if start < 0 {
				return value
			}
			start += len(`"registration_generation":"`)
			end := start + strings.Index(value[start:], `"`)
			return value[:start] + replacement + value[end:]
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newDCRMetadataFixture(t)
			resource, store := fixture.server.URL+"/gw/mcp", newDCRMemoryStore(t)
			opts := fixture.options(t, store)
			controller := authorizeDCRForProof(t, fixture, resource, opts)
			generation, clientID := controller.state.registration.generation, controller.state.registration.clientID
			_ = controller.Close()
			identity := oauthDCRIdentity{Profile: opts.Subject.Profile, Principal: opts.Subject.Principal, Resource: resource, Issuer: opts.Issuer}
			var key []byte
			if tc.name == "registration identity" {
				key, _ = oauthDCRRegistrationKey(identity)
			} else {
				grantIdentity := oauthCredentialIdentity{Profile: opts.Subject.Profile, Principal: opts.Subject.Principal, Resource: resource, Issuer: opts.Issuer, ClientKind: oauthDCRClientKind, ClientID: clientID}
				key, _ = oauthDCRCredentialKey(grantIdentity, generation)
			}
			record, err := store.Get(context.Background(), key)
			if err != nil {
				t.Fatal(err)
			}
			mutated := tc.mutate(string(record.Value))
			if mutated == string(record.Value) {
				t.Fatal("fixture did not mutate persisted identity")
			}
			if _, err := store.Put(context.Background(), key, []byte(mutated), &record.Version); err != nil {
				t.Fatal(err)
			}
			if _, err := NewOAuthController(context.Background(), resource, opts); err == nil {
				t.Fatal("mismatched persisted identity was restored")
			}
			fixture.mu.Lock()
			registrations, tokens := fixture.registerCount, fixture.tokenCount
			fixture.mu.Unlock()
			if registrations != 1 || tokens != 1 {
				t.Fatalf("mismatched restart used registration/token endpoint: %d/%d", registrations, tokens)
			}
		})
	}
}
