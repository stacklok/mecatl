package openaicodex

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stacklok/mecatl/internal/testutil/codextest"
)

type memoryStore struct {
	mu      sync.Mutex
	tokens  OAuthTokens
	saves   int
	saveErr error
}

func (s *memoryStore) Load(context.Context) (OAuthTokens, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tokens, nil
}

func (s *memoryStore) Save(_ context.Context, tokens OAuthTokens) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.saveErr != nil {
		return s.saveErr
	}
	s.tokens = tokens
	s.saves++
	return nil
}

func (s *memoryStore) snapshot() (OAuthTokens, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tokens, s.saves
}

// refreshCounter serves the token endpoint and counts exchanges.
func refreshCounter(t *testing.T, accountID string, lifetime time.Duration) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := calls.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  codextest.Token(time.Now().Add(lifetime), accountID),
			"refresh_token": "refresh-rotated",
			"expires_in":    int64(lifetime / time.Second),
		})
		_ = n
	}))
	t.Cleanup(server.Close)
	return server, &calls
}

func renewer(t *testing.T, store TokenStore, server *httptest.Server, now func() time.Time) *RenewableCredential {
	t.Helper()
	renewable, err := NewRenewableCredential(store, server.Client(), now)
	if err != nil {
		t.Fatal(err)
	}
	renewable.endpoints = endpointSet{token: server.URL}
	return renewable
}

// A grant comfortably inside its lifetime must be used as-is; refreshing early
// would burn the provider's rotation for nothing.
func TestRenewableUsesFreshGrantWithoutRefreshing(t *testing.T) {
	now := time.Now()
	store := &memoryStore{tokens: OAuthTokens{
		AccessToken:  codextest.Token(now.Add(time.Hour), "acct-fresh"),
		RefreshToken: "refresh-original",
		AccountID:    "acct-fresh",
		ExpiresAt:    now.Add(time.Hour),
	}}
	server, calls := refreshCounter(t, "acct-rotated", time.Hour)

	credential, err := renewer(t, store, server, func() time.Time { return now }).Credential(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := credential.Validate(now); err != nil {
		t.Fatal(err)
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("refreshes = %d, want none", got)
	}
}

// A token inside the skew window must be renewed before use, and the rotated
// grant must be persisted so a restart does not replay a retired token.
func TestRenewableRefreshesWithinSkewAndPersists(t *testing.T) {
	now := time.Now()
	store := &memoryStore{tokens: OAuthTokens{
		AccessToken:  codextest.Token(now.Add(30*time.Second), "acct-stale"),
		RefreshToken: "refresh-original",
		AccountID:    "acct-stale",
		ExpiresAt:    now.Add(30 * time.Second),
	}}
	server, calls := refreshCounter(t, "acct-rotated", time.Hour)

	credential, err := renewer(t, store, server, func() time.Time { return now }).Credential(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := credential.Validate(now); err != nil {
		t.Fatal(err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("refreshes = %d, want exactly one", got)
	}
	saved, saves := store.snapshot()
	if saves != 1 {
		t.Fatalf("saves = %d, want one", saves)
	}
	if saved.RefreshToken != "refresh-rotated" {
		t.Fatalf("stored refresh token = %q, want the rotated one", saved.RefreshToken)
	}
	if saved.AccountID != "acct-rotated" {
		t.Fatalf("stored account = %q, want the refreshed claim", saved.AccountID)
	}
}

// Each refresh rotates the refresh token, so concurrent expiring requests must
// collapse into one exchange; a second would present a retired token.
func TestRenewableSingleFlightsConcurrentRefresh(t *testing.T) {
	now := time.Now()
	store := &memoryStore{tokens: OAuthTokens{
		AccessToken:  codextest.Token(now.Add(10*time.Second), "acct-race"),
		RefreshToken: "refresh-original",
		AccountID:    "acct-race",
		ExpiresAt:    now.Add(10 * time.Second),
	}}
	server, calls := refreshCounter(t, "acct-rotated", time.Hour)
	renewable := renewer(t, store, server, func() time.Time { return now })

	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := renewable.Credential(t.Context()); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent renewal failed: %v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("refreshes = %d, want exactly one under concurrency", got)
	}
}

// A grant that cannot be persisted must fail the request: proceeding would use
// a rotated token whose refresh half was lost.
func TestRenewableFailsWhenRotatedGrantCannotPersist(t *testing.T) {
	now := time.Now()
	store := &memoryStore{
		tokens: OAuthTokens{
			AccessToken:  codextest.Token(now.Add(10*time.Second), "acct-persist"),
			RefreshToken: "refresh-original",
			AccountID:    "acct-persist",
			ExpiresAt:    now.Add(10 * time.Second),
		},
		saveErr: errors.New("store unavailable"),
	}
	server, _ := refreshCounter(t, "acct-rotated", time.Hour)

	if _, err := renewer(t, store, server, func() time.Time { return now }).Credential(t.Context()); err == nil {
		t.Fatal("renewal succeeded despite a failed persist")
	}
}

// An empty store must name the missing login rather than surfacing a decode error.
func TestRenewableReportsMissingGrant(t *testing.T) {
	server, _ := refreshCounter(t, "acct-none", time.Hour)
	_, err := renewer(t, &memoryStore{}, server, time.Now).Credential(t.Context())
	if !errors.Is(err, ErrNoGrant) {
		t.Fatalf("error = %v, want ErrNoGrant", err)
	}
}

// An access-only grant (a pasted token with no refresh half) stays usable
// until it lapses, then reports the credential's own expiry verdict.
func TestRenewableHandlesAccessOnlyGrant(t *testing.T) {
	now := time.Now()
	server, calls := refreshCounter(t, "acct-none", time.Hour)

	usable := &memoryStore{tokens: OAuthTokens{
		AccessToken: codextest.Token(now.Add(time.Hour), "acct-manual"),
		AccountID:   "acct-manual",
		ExpiresAt:   now.Add(time.Hour),
	}}
	if _, err := renewer(t, usable, server, func() time.Time { return now }).Credential(t.Context()); err != nil {
		t.Fatalf("access-only grant inside its lifetime was rejected: %v", err)
	}

	lapsed := &memoryStore{tokens: OAuthTokens{
		AccessToken: codextest.Token(now.Add(-time.Hour), "acct-manual"),
		AccountID:   "acct-manual",
		ExpiresAt:   now.Add(-time.Hour),
	}}
	if _, err := renewer(t, lapsed, server, func() time.Time { return now }).Credential(t.Context()); err == nil {
		t.Fatal("lapsed access-only grant was accepted")
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("refreshes = %d, want none without a refresh token", got)
	}
}

// A request cancelled mid-exchange must not strand the grant. The provider
// retires the submitted refresh token as soon as it rotates, so abandoning the
// exchange would discard a replacement for a token that is already dead,
// leaving the account permanently unable to refresh.
func TestRenewableCompletesRefreshDespiteCallerCancellation(t *testing.T) {
	now := time.Now()
	store := &memoryStore{tokens: OAuthTokens{
		AccessToken:  codextest.Token(now.Add(10*time.Second), "acct-cancel"),
		RefreshToken: "refresh-original",
		AccountID:    "acct-cancel",
		ExpiresAt:    now.Add(10 * time.Second),
	}}

	ctx, cancel := context.WithCancel(t.Context())
	released := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Cancel the caller while the provider is mid-rotation.
		cancel()
		<-released
		if err := r.Context().Err(); err != nil {
			t.Errorf("refresh inherited the caller's cancellation: %v", err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  codextest.Token(now.Add(time.Hour), "acct-rotated"),
			"refresh_token": "refresh-rotated",
			"expires_in":    3600,
		})
	}))
	defer server.Close()

	renewable := renewer(t, store, server, func() time.Time { return now })
	go func() {
		time.Sleep(10 * time.Millisecond)
		close(released)
	}()
	if _, err := renewable.Credential(ctx); err != nil {
		t.Fatalf("renewal did not survive caller cancellation: %v", err)
	}

	saved, saves := store.snapshot()
	if saves != 1 || saved.RefreshToken != "refresh-rotated" {
		t.Fatalf("rotated grant was not persisted: saves=%d token=%q", saves, saved.RefreshToken)
	}
}

// A renewing policy must resolve its credential at the network boundary, so a
// rotation reaches both inference and listing without a restart. A policy that
// captured the credential at construction would keep sending a retired token.
func TestRenewingPolicyUsesCurrentCredential(t *testing.T) {
	now := time.Now()
	store := &memoryStore{tokens: OAuthTokens{
		AccessToken:  codextest.Token(now.Add(10*time.Second), "acct-before"),
		RefreshToken: "refresh-original",
		AccountID:    "acct-before",
		ExpiresAt:    now.Add(10 * time.Second),
	}}
	tokenServer, _ := refreshCounter(t, "acct-after", time.Hour)
	renewable := renewer(t, store, tokenServer, func() time.Time { return now })

	captured := &headerCapturingTransport{}
	policy, err := NewRenewingRequestPolicy(renewable, func() time.Time { return now }, captured)
	if err != nil {
		t.Fatal(err)
	}

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, BaseURL+"/models?client_version=1.0.0", nil)
	if err != nil {
		t.Fatal(err)
	}
	// The policy refuses a Host override, and NewRequest populates Host from
	// the URL, so it is cleared exactly as the production lister does.
	req.Host = ""
	resp, err := policy.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()

	// The credential was renewed during the request, so the rotated account is
	// what reached the wire.
	if got := captured.header.Get("ChatGPT-Account-ID"); got != "acct-after" {
		t.Fatalf("ChatGPT-Account-ID = %q, want the renewed account", got)
	}
	if got := captured.header.Get("Authorization"); got == "" || got == "Bearer " {
		t.Fatalf("Authorization = %q", got)
	}
	if got := captured.header.Get("originator"); got != "mecatl" {
		t.Fatalf("originator = %q, want the honest value", got)
	}
	if captured.header.Get("version") != "" {
		t.Fatal("the Codex CLI version header was sent")
	}
}

// headerCapturingTransport records the headers the policy stamped.
type headerCapturingTransport struct{ header http.Header }

func (t *headerCapturingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.header = req.Header.Clone()
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader("{}")),
		Header:     make(http.Header),
		Request:    req,
	}, nil
}

// A renewing policy with no source is a programming error and must be refused
// rather than sending unauthenticated requests.
func TestRenewingPolicyRequiresSource(t *testing.T) {
	if _, err := NewRenewingRequestPolicy(nil, time.Now, nil); err == nil {
		t.Fatal("a renewing policy was built with no credential source")
	}
}
