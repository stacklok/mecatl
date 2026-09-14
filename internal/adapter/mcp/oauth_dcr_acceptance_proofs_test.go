package mcp

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"golang.org/x/oauth2"

	"github.com/stacklok/mecatl/internal/adapter/credentialstore"
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
		key, _ := oauthDCRLifecycleKey(opts.Client.DCR.ServerName)
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
		regKey, _ := oauthDCRLifecycleKey(opts.Client.DCR.ServerName)
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
	registrationKey, _ := oauthDCRLifecycleKey(opts.Client.DCR.ServerName)
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
	key, _ := oauthDCRLifecycleKey(opts.Client.DCR.ServerName)
	record, _ := store.Get(context.Background(), key)
	drifted := strings.Replace(string(record.Value), `"scopes":["openid"]`, `"scopes":["other"]`, 1)
	if _, err := store.Put(context.Background(), key, []byte(drifted), &record.Version); err != nil {
		t.Fatal(err)
	}
	if _, _, err := PrepareOAuthDCRLogin(context.Background(), resource, opts, OAuthDCRLoginReuse); !errors.Is(err, ErrOAuthDCRRecoveryRequired) {
		t.Fatalf("scope/fingerprint drift = %v", err)
	}
}

type scriptedDCRStore struct {
	credentialstore.Store
	get func(context.Context, []byte) (credentialstore.Record, error)
	put func(context.Context, []byte, []byte, *credentialstore.Version) (credentialstore.Record, error)
}

func (s *scriptedDCRStore) Get(ctx context.Context, key []byte) (credentialstore.Record, error) {
	if s.get != nil {
		return s.get(ctx, key)
	}
	return s.Store.Get(ctx, key)
}

func (s *scriptedDCRStore) Put(ctx context.Context, key, value []byte, expected *credentialstore.Version) (credentialstore.Record, error) {
	if s.put != nil {
		return s.put(ctx, key, value, expected)
	}
	return s.Store.Put(ctx, key, value, expected)
}

func TestADR_0325_DirectDCRUnknownAttemptRecovery(t *testing.T) {
	t.Run("pending contender stops while winner still owns preparation", func(t *testing.T) {
		fixture := newDCRMetadataFixture(t)
		resource, base := fixture.server.URL+"/gw/mcp", newDCRMemoryStore(t)
		committed, release := make(chan struct{}), make(chan struct{})
		var once sync.Once
		store := &scriptedDCRStore{Store: base}
		store.put = func(ctx context.Context, key, value []byte, expected *credentialstore.Version) (credentialstore.Record, error) {
			record, err := base.Put(ctx, key, value, expected)
			if err == nil && expected == nil {
				once.Do(func() {
					close(committed)
					<-release
				})
			}
			return record, err
		}
		opts := fixture.options(t, store)
		type result struct {
			opts OAuthOptions
			path string
			err  error
		}
		winner := make(chan result, 1)
		go func() {
			prepared, path, err := PrepareOAuthDCRLogin(context.Background(), resource, opts, OAuthDCRLoginReuse)
			winner <- result{opts: prepared, path: path, err: err}
		}()
		<-committed
		if _, _, err := PrepareOAuthDCRLogin(context.Background(), resource, opts, OAuthDCRLoginReuse); !errors.Is(err, ErrOAuthDCRRecoveryRequired) {
			t.Fatalf("pending contender = %v, want recovery-required", err)
		}
		close(release)
		if got := <-winner; got.err != nil || got.opts.dcrTicket == nil || got.path == "" {
			t.Fatalf("pending winner = %#v", got)
		}
		if fixture.registerCount != 0 {
			t.Fatalf("contention issued %d registration POSTs", fixture.registerCount)
		}
	})

	t.Run("unknown POST outcome is not repeated automatically", func(t *testing.T) {
		fixture := newDCRMetadataFixture(t)
		resource, store := fixture.server.URL+"/gw/mcp", newDCRMemoryStore(t)
		opts := fixture.options(t, store)
		prepared, path, err := PrepareOAuthDCRLogin(context.Background(), resource, opts, OAuthDCRLoginReuse)
		if err != nil {
			t.Fatal(err)
		}
		prepared.RedirectURL = "http://127.0.0.1:49152" + path
		started, release := make(chan struct{}), make(chan struct{})
		fixture.registrationStarted, fixture.registrationRelease = started, release
		ctx, cancel := context.WithCancel(context.Background())
		result := make(chan error, 1)
		go func() {
			_, err := NewOAuthController(ctx, resource, prepared)
			result <- err
		}()
		<-started
		cancel()
		close(release)
		if err := <-result; !errors.Is(err, ErrOAuthDCRRecoveryRequired) {
			t.Fatalf("unknown POST outcome = %v, want recovery-required", err)
		}
		if _, _, err := PrepareOAuthDCRLogin(context.Background(), resource, opts, OAuthDCRLoginReuse); !errors.Is(err, ErrOAuthDCRRecoveryRequired) {
			t.Fatalf("ordinary retry after unknown POST = %v", err)
		}
		if fixture.registerCount != 1 {
			t.Fatalf("unknown POST was repeated %d times", fixture.registerCount)
		}
	})

	t.Run("explicit retry fences an old publication and retains one predecessor", func(t *testing.T) {
		fixture := newDCRMetadataFixture(t)
		resource, base := fixture.server.URL+"/gw/mcp", newDCRMemoryStore(t)
		store := &scriptedDCRStore{Store: base}
		opts := fixture.options(t, store)
		old, oldPath, err := PrepareOAuthDCRLogin(context.Background(), resource, opts, OAuthDCRLoginReuse)
		if err != nil {
			t.Fatal(err)
		}
		oldGeneration := old.dcrTicket.record.Generation
		atPublication, releasePublication := make(chan struct{}), make(chan struct{})
		var once sync.Once
		store.put = func(ctx context.Context, key, value []byte, expected *credentialstore.Version) (credentialstore.Record, error) {
			record, decodeErr := decodeOAuthDCRRecord(value, old.dcrTicket.record.Identity)
			if decodeErr == nil && record.State == oauthDCRStateReady && record.Generation == oldGeneration {
				once.Do(func() {
					close(atPublication)
					<-releasePublication
				})
			}
			return base.Put(ctx, key, value, expected)
		}
		old.RedirectURL = "http://127.0.0.1:49152" + oldPath
		oldResult := make(chan error, 1)
		go func() {
			_, err := NewOAuthController(context.Background(), resource, old)
			oldResult <- err
		}()
		<-atPublication
		retry, _, err := PrepareOAuthDCRLogin(context.Background(), resource, opts, OAuthDCRLoginRetryRegistration)
		if err != nil {
			t.Fatal(err)
		}
		close(releasePublication)
		if err := <-oldResult; !errors.Is(err, ErrOAuthDCRRecoveryRequired) {
			t.Fatalf("stale publication = %v, want recovery-required", err)
		}
		latest, latestPath, err := PrepareOAuthDCRLogin(context.Background(), resource, opts, OAuthDCRLoginRetryRegistration)
		if err != nil {
			t.Fatal(err)
		}
		latest.RedirectURL = "http://127.0.0.1:49153" + latestPath
		winner, err := NewOAuthController(context.Background(), resource, latest)
		if err != nil {
			t.Fatal(err)
		}
		_ = winner.Close()

		identity := latest.dcrTicket.record.Identity
		key, _ := oauthDCRLifecycleKey(opts.Client.DCR.ServerName)
		stored, err := base.Get(context.Background(), key)
		if err != nil {
			t.Fatal(err)
		}
		ready, err := decodeOAuthDCRRecord(stored.Value, identity)
		if err != nil {
			t.Fatal(err)
		}
		if ready.Generation != latest.dcrTicket.record.Generation || ready.PreviousAttempt == nil || ready.PreviousAttempt.Generation != retry.dcrTicket.record.Generation || ready.PreviousAttempt.Reason != "explicit_retry" {
			t.Fatalf("retry winner/evidence = %#v", ready)
		}
		if ready.PreviousAttempt.Generation == oldGeneration || strings.Count(string(stored.Value), `"previous_attempt"`) != 1 {
			t.Fatalf("previous-attempt evidence is not bounded: %s", stored.Value)
		}
	})

	t.Run("ready winner is adopted over a late registration error", func(t *testing.T) {
		fixture := newDCRMetadataFixture(t)
		resource, store := fixture.server.URL+"/gw/mcp", newDCRMemoryStore(t)
		opts := fixture.options(t, store)
		prepared, path, err := PrepareOAuthDCRLogin(context.Background(), resource, opts, OAuthDCRLoginReuse)
		if err != nil {
			t.Fatal(err)
		}
		prepared.RedirectURL = "http://127.0.0.1:49152" + path
		started, release := make(chan struct{}), make(chan struct{})
		fixture.registrationStarted, fixture.registrationRelease, fixture.registrationStatus = started, release, http.StatusGatewayTimeout
		result := make(chan struct {
			controller *OAuthController
			err        error
		}, 1)
		go func() {
			controller, err := NewOAuthController(context.Background(), resource, prepared)
			result <- struct {
				controller *OAuthController
				err        error
			}{controller: controller, err: err}
		}()
		<-started
		identity := prepared.dcrTicket.record.Identity
		key, _ := oauthDCRLifecycleKey(opts.Client.DCR.ServerName)
		pending, err := store.Get(context.Background(), key)
		if err != nil {
			t.Fatal(err)
		}
		ready := prepared.dcrTicket.record
		ready.State = oauthDCRStateReady
		ready.Registration = &oauthDCRRegistration{ClientID: fixture.clientID, RegisteredRedirectURI: prepared.RedirectURL}
		value, err := encodeOAuthDCRRecord(ready, identity)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.Put(context.Background(), key, value, &pending.Version); err != nil {
			t.Fatal(err)
		}
		close(release)
		got := <-result
		if got.err != nil || got.controller == nil {
			t.Fatalf("late error did not adopt ready winner: controller=%v err=%v", got.controller, got.err)
		}
		_ = got.controller.Close()
		if fixture.registerCount != 1 {
			t.Fatalf("late error caused %d registration POSTs", fixture.registerCount)
		}
	})

	for _, tc := range []struct {
		name      string
		commitPut bool
		wantOK    bool
	}{
		{name: "reported save failure with committed ready winner", commitPut: true, wantOK: true},
		{name: "uncommitted save ambiguity remains recovery required", commitPut: false, wantOK: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newDCRMetadataFixture(t)
			resource, base := fixture.server.URL+"/gw/mcp", newDCRMemoryStore(t)
			store := &scriptedDCRStore{Store: base}
			opts := fixture.options(t, store)
			prepared, path, err := PrepareOAuthDCRLogin(context.Background(), resource, opts, OAuthDCRLoginReuse)
			if err != nil {
				t.Fatal(err)
			}
			store.put = func(ctx context.Context, key, value []byte, expected *credentialstore.Version) (credentialstore.Record, error) {
				record, decodeErr := decodeOAuthDCRRecord(value, prepared.dcrTicket.record.Identity)
				if decodeErr == nil && record.State == oauthDCRStateReady {
					if tc.commitPut {
						if _, err := base.Put(ctx, key, value, expected); err != nil {
							return credentialstore.Record{}, err
						}
					}
					return credentialstore.Record{}, credentialstore.ErrUnavailable
				}
				return base.Put(ctx, key, value, expected)
			}
			prepared.RedirectURL = "http://127.0.0.1:49152" + path
			controller, err := NewOAuthController(context.Background(), resource, prepared)
			if tc.wantOK {
				if err != nil || controller == nil {
					t.Fatalf("committed winner was not adopted: controller=%v err=%v", controller, err)
				}
				_ = controller.Close()
				return
			}
			if !errors.Is(err, ErrOAuthDCRRecoveryRequired) || controller != nil {
				t.Fatalf("uncommitted ambiguity = controller=%v err=%v", controller, err)
			}
			identity := prepared.dcrTicket.record.Identity
			key, _ := oauthDCRLifecycleKey(opts.Client.DCR.ServerName)
			persisted, getErr := base.Get(context.Background(), key)
			if getErr != nil {
				t.Fatal(getErr)
			}
			pending, decodeErr := decodeOAuthDCRRecord(persisted.Value, identity)
			if decodeErr != nil || pending.State != oauthDCRStatePending {
				t.Fatalf("uncommitted ambiguity lost pending evidence: %#v %v", pending, decodeErr)
			}
		})
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
			var key []byte
			if tc.name == "registration identity" {
				key, _ = oauthDCRLifecycleKey(opts.Client.DCR.ServerName)
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
