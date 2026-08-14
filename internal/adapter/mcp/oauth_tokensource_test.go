package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/oauth2"

	"github.com/stacklok/mecatl/internal/adapter/credentialstore"
)

func TestOAuthNewTokenSourceCreatesReplacesAndRestores(t *testing.T) {
	store := newOAuthMemoryStore(t)
	state := newTestOAuthState(t, store, nil, "https://issuer.example/token", true)
	cfg := testOAuthConfig("https://issuer.example/token")
	first := validOAuthToken("first", "refresh-1")
	source, err := state.newTokenSource(context.Background(), cfg, first)
	if err != nil {
		t.Fatal(err)
	}
	assertTokenAccess(t, source, "first")
	firstRecord, err := store.Get(context.Background(), state.key)
	if err != nil {
		t.Fatal(err)
	}

	second := validOAuthToken("second", "refresh-2")
	if _, err := state.newTokenSource(context.Background(), cfg, second); err != nil {
		t.Fatal(err)
	}
	secondRecord, err := store.Get(context.Background(), state.key)
	if err != nil {
		t.Fatal(err)
	}
	if secondRecord.Version.Equal(firstRecord.Version) {
		t.Fatal("replace did not advance the record version")
	}

	restored := newTestOAuthState(t, store, nil, "https://issuer.example/token", true)
	assertTokenAccess(t, restored.initialTokenSource(), "second")
}

func TestOAuthNewTokenSourceConflictAdoptsWinner(t *testing.T) {
	store := newOAuthMemoryStore(t)
	winner := newTestOAuthState(t, store, nil, "https://issuer.example/token", true)
	loser := newTestOAuthState(t, store, nil, "https://issuer.example/token", true)
	cfg := testOAuthConfig("https://issuer.example/token")
	if _, err := winner.newTokenSource(context.Background(), cfg, validOAuthToken("winner", "winner-refresh")); err != nil {
		t.Fatal(err)
	}
	source, err := loser.newTokenSource(context.Background(), cfg, validOAuthToken("loser-canary", "loser-refresh-canary"))
	if err != nil {
		t.Fatal(err)
	}
	assertTokenAccess(t, source, "winner")
	record, err := store.Get(context.Background(), winner.key)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(record.Value), "loser-canary") || strings.Contains(string(record.Value), "loser-refresh-canary") {
		t.Fatal("CAS loser overwrote the winner")
	}
}

func TestOAuthLazyRefreshPersistsRotationAndPreservesOmittedRefresh(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if user, secret, ok := r.BasicAuth(); !ok || user != "client-id" || secret != testClientSecretCanary {
			http.Error(w, `{"error":"invalid_client"}`, http.StatusUnauthorized)
			return
		}
		if err := r.ParseForm(); err != nil || r.Form.Get("refresh_token") != "old-refresh" {
			http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"rotated-access","token_type":"Bearer","expires_in":3600}`))
	}))
	defer server.Close()

	store := newOAuthMemoryStore(t)
	state := newTestOAuthState(t, store, server.Client(), server.URL, true)
	cfg := testOAuthConfig(server.URL)
	source, err := state.newTokenSource(context.Background(), cfg, expiredOAuthToken("old-access", "old-refresh"))
	if err != nil {
		t.Fatal(err)
	}
	token, err := source.(*persistentTokenSource).tokenWithContext(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if token.AccessToken != "rotated-access" {
		t.Fatalf("access token = %q, want rotated-access", token.AccessToken)
	}
	if requests.Load() != 1 {
		t.Fatalf("refresh requests = %d, want 1", requests.Load())
	}
	record, err := store.Get(context.Background(), state.key)
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := decodeOAuthCredential(record.Value, state.identity, true, state.origins)
	if err != nil {
		t.Fatal(err)
	}
	if envelope.Token.RefreshToken != "old-refresh" {
		t.Fatalf("persisted refresh token = %q, want preserved old token", envelope.Token.RefreshToken)
	}
}

func TestOAuthRefreshConflictReloadsAndAdoptsWinner(t *testing.T) {
	var sequence atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		id := sequence.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(fmt.Appendf(nil, `{"access_token":"refreshed-%d","token_type":"Bearer","refresh_token":"next","expires_in":3600}`, id))
	}))
	defer server.Close()

	store := newOAuthMemoryStore(t)
	seed := newTestOAuthState(t, store, server.Client(), server.URL, true)
	if _, err := seed.newTokenSource(context.Background(), testOAuthConfig(server.URL), expiredOAuthToken("old", "refresh")); err != nil {
		t.Fatal(err)
	}
	first := newTestOAuthState(t, store, server.Client(), server.URL, true)
	second := newTestOAuthState(t, store, server.Client(), server.URL, true)
	firstSource := first.initialTokenSource()
	secondSource := second.initialTokenSource()
	assertTokenAccess(t, firstSource, "refreshed-1")
	assertTokenAccess(t, secondSource, "refreshed-1")
	if sequence.Load() != 2 {
		t.Fatalf("refresh attempts = %d, want one stale attempt plus winner adoption", sequence.Load())
	}
}

func TestOAuthInvalidGrantDeletesUnchangedCredential(t *testing.T) {
	server := invalidGrantServer(t)
	defer server.Close()
	store := newOAuthMemoryStore(t)
	state := newTestOAuthState(t, store, server.Client(), server.URL, true)
	source, err := state.newTokenSource(context.Background(), testOAuthConfig(server.URL), expiredOAuthToken("old", "bad-refresh"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = source.Token()
	if !errors.Is(err, ErrOAuthLoginRequired) {
		t.Fatalf("Token error = %v, want login required", err)
	}
	if _, getErr := store.Get(context.Background(), state.key); !errors.Is(getErr, credentialstore.ErrNotFound) {
		t.Fatalf("record after invalid_grant: %v", getErr)
	}
	if state.initialTokenSource() != nil {
		t.Fatal("invalid_grant did not clear current source")
	}
}

func TestOAuthInvalidGrantReloadsChangedVersionOnce(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if r.Form.Get("refresh_token") == "stale-refresh" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
			return
		}
		_, _ = w.Write([]byte(`{"access_token":"winner-refreshed","token_type":"Bearer","refresh_token":"winner-next","expires_in":3600}`))
	}))
	defer server.Close()

	store := newOAuthMemoryStore(t)
	seed := newTestOAuthState(t, store, server.Client(), server.URL, true)
	if _, err := seed.newTokenSource(context.Background(), testOAuthConfig(server.URL), expiredOAuthToken("stale", "stale-refresh")); err != nil {
		t.Fatal(err)
	}
	stale := newTestOAuthState(t, store, server.Client(), server.URL, true)
	winner := newTestOAuthState(t, store, server.Client(), server.URL, true)
	if _, err := winner.newTokenSource(context.Background(), testOAuthConfig(server.URL), expiredOAuthToken("winner", "winner-refresh")); err != nil {
		t.Fatal(err)
	}
	assertTokenAccess(t, stale.initialTokenSource(), "winner-refreshed")
	if requests.Load() != 2 {
		t.Fatalf("refresh requests = %d, want stale attempt plus one winner retry", requests.Load())
	}
}

func TestOAuthCancellationDoesNotMutateCredential(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
	defer server.Close()
	store := newOAuthMemoryStore(t)
	state := newTestOAuthState(t, store, server.Client(), server.URL, true)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := state.newTokenSource(ctx, testOAuthConfig(server.URL), validOAuthToken("never", "never")); !errors.Is(err, context.Canceled) {
		t.Fatalf("NewTokenSource error = %v, want canceled", err)
	}
	if _, err := store.Get(context.Background(), state.key); !errors.Is(err, credentialstore.ErrNotFound) {
		t.Fatalf("canceled create mutated store: %v", err)
	}

	source, err := state.newTokenSource(context.Background(), testOAuthConfig(server.URL), expiredOAuthToken("old", "refresh"))
	if err != nil {
		t.Fatal(err)
	}
	persistent := source.(*persistentTokenSource)
	if _, err := persistent.tokenWithContext(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("refresh error = %v, want canceled", err)
	}
	if requests.Load() != 0 {
		t.Fatalf("canceled refresh made %d HTTP requests", requests.Load())
	}
}

func TestOAuthErrorsNeverProjectRawCanaries(t *testing.T) {
	canaries := []string{"access-canary", "refresh-canary", testClientSecretCanary, "client-id-canary", "principal-canary", "issuer-canary", "response-body-canary"}
	raw := errors.New(strings.Join(canaries, " "))
	for _, projected := range []error{projectOAuthError(raw), projectOAuthError(ErrOAuthLoginRequired)} {
		for _, canary := range canaries {
			if strings.Contains(projected.Error(), canary) {
				t.Fatalf("projected error leaked %q", canary)
			}
		}
	}
	if !errors.Is(projectOAuthError(raw), ErrOAuthUnavailable) {
		t.Fatal("raw error was not projected to unavailable")
	}
}

func TestOAuthRestoreRejectsCorruptionAndUnavailableStoreSafely(t *testing.T) {
	store := newOAuthMemoryStore(t)
	identity := testOAuthIdentity()
	key, err := oauthCredentialKey(identity)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Put(context.Background(), key, []byte(`{"canary":"corrupt-record-canary"}`), nil); err != nil {
		t.Fatal(err)
	}
	registration := oauthRegistration{kind: "preregistered", clientID: "client-id", clientSecret: testClientSecretCanary}
	_, err = restoreOAuthCredential(context.Background(), store, store, identity, registration, testOAuthOrigins(), http.DefaultClient, true, false)
	if !errors.Is(err, ErrOAuthUnavailable) || strings.Contains(err.Error(), "corrupt-record-canary") {
		t.Fatalf("corrupt restore error = %v", err)
	}

	unavailable := &oauthStoreWrapper{Store: store, get: func(context.Context, []byte) (credentialstore.Record, error) {
		return credentialstore.Record{}, errors.New("store-unavailable-canary")
	}}
	_, err = restoreOAuthCredential(context.Background(), unavailable, unavailable, identity, registration, testOAuthOrigins(), http.DefaultClient, true, false)
	if !errors.Is(err, ErrOAuthUnavailable) || strings.Contains(err.Error(), "store-unavailable-canary") {
		t.Fatalf("unavailable restore error = %v", err)
	}
}

func TestOAuthRefreshSecondConflictIsBounded(t *testing.T) {
	var refreshes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		id := refreshes.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(fmt.Appendf(nil, `{"access_token":"attempt-%d","token_type":"Bearer","refresh_token":"next-%d","expires_in":3600}`, id, id))
	}))
	defer server.Close()

	base := newOAuthMemoryStore(t)
	seed := newTestOAuthState(t, base, server.Client(), server.URL, true)
	cfg := testOAuthConfig(server.URL)
	if _, err := seed.newTokenSource(context.Background(), cfg, expiredOAuthToken("seed", "seed-refresh")); err != nil {
		t.Fatal(err)
	}

	var puts atomic.Int32
	wrapper := &oauthStoreWrapper{Store: base}
	wrapper.put = func(ctx context.Context, key, value []byte, expected *credentialstore.Version) (credentialstore.Record, error) {
		call := puts.Add(1)
		current, getErr := base.Get(ctx, key)
		if getErr != nil {
			return credentialstore.Record{}, getErr
		}
		winner := newOAuthCredentialEnvelope(testOAuthIdentity(), cfg, expiredOAuthToken(fmt.Sprintf("winner-%d", call), fmt.Sprintf("winner-refresh-%d", call)))
		origins := map[string]struct{}{"https://issuer.example": {}, server.URL: {}}
		winnerValue, encodeErr := encodeOAuthCredential(winner, testOAuthIdentity(), true, origins)
		if encodeErr != nil {
			return credentialstore.Record{}, encodeErr
		}
		if _, putErr := base.Put(ctx, key, winnerValue, &current.Version); putErr != nil {
			return credentialstore.Record{}, putErr
		}
		return base.Put(ctx, key, value, expected)
	}
	state := newTestOAuthState(t, wrapper, server.Client(), server.URL, true)
	_, err := state.initialTokenSource().Token()
	if !errors.Is(err, ErrOAuthUnavailable) {
		t.Fatalf("Token error = %v, want unavailable", err)
	}
	if puts.Load() != 2 || refreshes.Load() != 2 {
		t.Fatalf("puts=%d refreshes=%d, want bounded 2/2", puts.Load(), refreshes.Load())
	}
}

func TestOAuthInvalidGrantDeleteConflictValidatesExpiredWinner(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_ = r.ParseForm()
		w.Header().Set("Content-Type", "application/json")
		if r.Form.Get("refresh_token") == "stale-refresh" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
			return
		}
		_, _ = w.Write([]byte(`{"access_token":"delete-winner-refreshed","token_type":"Bearer","refresh_token":"winner-next","expires_in":3600}`))
	}))
	defer server.Close()
	base := newOAuthMemoryStore(t)
	seed := newTestOAuthState(t, base, server.Client(), server.URL, true)
	cfg := testOAuthConfig(server.URL)
	if _, err := seed.newTokenSource(context.Background(), cfg, expiredOAuthToken("stale", "stale-refresh")); err != nil {
		t.Fatal(err)
	}

	wrapper := &oauthStoreWrapper{Store: base}
	var deletes atomic.Int32
	wrapper.delete = func(ctx context.Context, key []byte, expected credentialstore.Version) error {
		deletes.Add(1)
		current, err := base.Get(ctx, key)
		if err != nil {
			return err
		}
		winner := newOAuthCredentialEnvelope(testOAuthIdentity(), cfg, expiredOAuthToken("delete-winner-expired", "winner-refresh"))
		origins := map[string]struct{}{"https://issuer.example": {}, server.URL: {}}
		value, err := encodeOAuthCredential(winner, testOAuthIdentity(), true, origins)
		if err != nil {
			return err
		}
		if _, err := base.Put(ctx, key, value, &current.Version); err != nil {
			return err
		}
		return base.Delete(ctx, key, expected)
	}
	state := newTestOAuthState(t, wrapper, server.Client(), server.URL, true)
	assertTokenAccess(t, state.initialTokenSource(), "delete-winner-refreshed")
	if deletes.Load() != 1 || requests.Load() != 2 {
		t.Fatalf("delete calls=%d refresh requests=%d, want 1/2", deletes.Load(), requests.Load())
	}
}

func TestOAuthRefreshCancellationStopsHTTPAndPreservesRecord(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		close(started)
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer server.Close()
	defer close(release)
	store := newOAuthMemoryStore(t)
	state := newTestOAuthState(t, store, server.Client(), server.URL, true)
	if _, err := state.newTokenSource(context.Background(), testOAuthConfig(server.URL), expiredOAuthToken("old", "refresh")); err != nil {
		t.Fatal(err)
	}
	before, err := store.Get(context.Background(), state.key)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, tokenErr := state.initialTokenSource().(*persistentTokenSource).tokenWithContext(ctx)
		done <- tokenErr
	}()
	<-started
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("refresh error = %v, want canceled", err)
	}
	after, err := store.Get(context.Background(), state.key)
	if err != nil {
		t.Fatal(err)
	}
	if !after.Version.Equal(before.Version) {
		t.Fatal("canceled refresh mutated the record")
	}
}

func newOAuthMemoryStore(t *testing.T) credentialstore.Store {
	t.Helper()
	store, err := credentialstore.NewMemoryBackend().Open("mcp-oauth-test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func newTestOAuthState(t *testing.T, store credentialstore.Store, client *http.Client, tokenURL string, requestRefresh bool) *oauthCredentialState {
	t.Helper()
	if client == nil {
		client = http.DefaultClient
	}
	identity := testOAuthIdentity()
	origins := testOAuthOrigins()
	if req, err := http.NewRequest(http.MethodGet, tokenURL, nil); err == nil && req.URL.Host != "" {
		origins[urlOrigin(req.URL)] = struct{}{}
	}
	state, err := restoreOAuthCredential(context.Background(), store, store, identity, oauthRegistration{kind: "preregistered", clientID: "client-id", clientSecret: testClientSecretCanary}, origins, client, requestRefresh, false)
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func validOAuthToken(access, refresh string) *oauth2.Token {
	return &oauth2.Token{AccessToken: access, TokenType: "Bearer", RefreshToken: refresh, Expiry: time.Now().Add(time.Hour)}
}

func expiredOAuthToken(access, refresh string) *oauth2.Token {
	return &oauth2.Token{AccessToken: access, TokenType: "Bearer", RefreshToken: refresh, Expiry: time.Now().Add(-time.Hour)}
}

func assertTokenAccess(t *testing.T, source oauth2.TokenSource, want string) {
	t.Helper()
	if source == nil {
		t.Fatal("token source is nil")
	}
	token, err := source.Token()
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	if token.AccessToken != want {
		t.Fatalf("access token = %q, want %q", token.AccessToken, want)
	}
}

type oauthStoreWrapper struct {
	credentialstore.Store
	get    func(context.Context, []byte) (credentialstore.Record, error)
	put    func(context.Context, []byte, []byte, *credentialstore.Version) (credentialstore.Record, error)
	delete func(context.Context, []byte, credentialstore.Version) error
}

func (s *oauthStoreWrapper) Get(ctx context.Context, key []byte) (credentialstore.Record, error) {
	if s.get != nil {
		return s.get(ctx, key)
	}
	return s.Store.Get(ctx, key)
}

func (s *oauthStoreWrapper) Put(ctx context.Context, key, value []byte, expected *credentialstore.Version) (credentialstore.Record, error) {
	if s.put != nil {
		return s.put(ctx, key, value, expected)
	}
	return s.Store.Put(ctx, key, value, expected)
}

func (s *oauthStoreWrapper) Delete(ctx context.Context, key []byte, expected credentialstore.Version) error {
	if s.delete != nil {
		return s.delete(ctx, key, expected)
	}
	return s.Store.Delete(ctx, key, expected)
}

func invalidGrantServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant", "error_description": "response-body-canary"})
	}))
}
