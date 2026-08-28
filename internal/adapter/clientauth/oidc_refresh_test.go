package clientauth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/oauth2"

	authoidc "github.com/stacklok/mecatl/authn/oidc"
	"github.com/stacklok/mecatl/internal/adapter/credentialstore"
)

type refreshFixture struct {
	srv             *httptest.Server
	key             *rsa.PrivateKey
	calls           atomic.Int32
	bad             atomic.Bool
	descriptionOnly atomic.Bool
	tokenEntered    chan struct{}
	tokenRelease    <-chan struct{}
}

func newRefreshFixture(t *testing.T) *refreshFixture {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &refreshFixture{key: key}
	f.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"issuer": f.srv.URL, "authorization_endpoint": f.srv.URL + "/authorize",
				"token_endpoint": f.srv.URL + "/token", "jwks_uri": f.srv.URL + "/keys",
				"code_challenge_methods_supported": []string{"S256"},
			})
		case "/keys":
			_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
				"kty": "RSA", "use": "sig", "alg": "RS256", "kid": "refresh-test",
				"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
				"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
			}}})
		case "/token":
			f.calls.Add(1)
			if f.tokenEntered != nil {
				close(f.tokenEntered)
				f.tokenEntered = nil
			}
			if f.tokenRelease != nil {
				select {
				case <-f.tokenRelease:
				case <-r.Context().Done():
					return
				}
			}
			w.Header().Set("Content-Type", "application/json")
			if f.bad.Load() {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				if f.descriptionOnly.Load() {
					_, _ = w.Write([]byte(`{"error":"invalid_client","error_description":"invalid_grant"}`))
				} else {
					_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
				}
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token":  f.token(t, "rotated", time.Now().Add(time.Minute)),
				"refresh_token": "refresh-rotated", "token_type": "Bearer", "expires_in": 60,
			})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *refreshFixture) token(t *testing.T, subject string, expiry time.Time) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"iss": f.srv.URL, "sub": subject, "aud": "vmcp", "exp": expiry.Unix(), "iat": time.Now().Unix(),
	})
	tok.Header["kid"] = "refresh-test"
	v, err := tok.SignedString(f.key)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func (f *refreshFixture) source(t *testing.T, c *Credentials, id Identity, poll time.Duration) *RefreshSource {
	t.Helper()
	validator, err := authoidc.NewValidator(context.Background(), authoidc.Config{
		Issuer: f.srv.URL, JWKSURI: f.srv.URL + "/keys", Audience: "vmcp", HTTPClient: f.srv.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	registry, err := OpenRegistry(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return newRefreshSource(id, c, registry, f.srv.Client(), f.srv.URL+"/token", validator, poll)
}

func TestLoginRequiredCauses(t *testing.T) {
	if err := loginRequired(NotEnrolled); !errors.Is(err, ErrLoginRequired) {
		t.Fatal(err)
	}
	var typed *LoginRequiredError
	if !errors.As(loginRequired(CredentialUnusable), &typed) || typed.Cause != CredentialUnusable {
		t.Fatalf("cause = %#v", typed)
	}
	if strings.Contains(typed.Error(), "token") {
		t.Fatalf("error exposes credential-shaped text: %q", typed.Error())
	}
	if _, err := NewRefreshSource(context.Background(), nil, LoginConfig{}); !errors.Is(err, ErrLoginRequired) {
		t.Fatalf("nil creds: %v", err)
	}
}

func TestRefreshInvalidGrantRequiresStructuredErrorCode(t *testing.T) {
	exact := &oauth2.RetrieveError{ErrorCode: "invalid_grant"}
	for name, err := range map[string]error{
		"exact":            exact,
		"wrapped":          fmt.Errorf("token refresh: %w", exact),
		"description only": &oauth2.RetrieveError{ErrorCode: "invalid_client", ErrorDescription: "contains invalid_grant prose"},
		"plain prose":      errors.New("invalid_grant"),
	} {
		t.Run(name, func(t *testing.T) {
			got := isInvalidGrant(err)
			want := name == "exact" || name == "wrapped"
			if got != want {
				t.Fatalf("isInvalidGrant(%v) = %v, want %v", err, got, want)
			}
		})
	}
}

func TestRefreshDescriptionMentioningInvalidGrantKeepsCredential(t *testing.T) {
	f := newRefreshFixture(t)
	c := credentials(t)
	id := identity("remote.example:443")
	id.Issuer = f.srv.URL
	id, _ = id.Canonical()
	stored := Token{AccessToken: f.token(t, "original", time.Now().Add(time.Minute)), RefreshToken: "refresh-original", TokenType: "Bearer", Expiry: time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)}
	if _, err := c.Save(t.Context(), id, stored, nil); err != nil {
		t.Fatal(err)
	}
	f.bad.Store(true)
	f.descriptionOnly.Store(true)
	s := f.source(t, c, id, time.Hour)
	defer s.Close()
	if _, err := s.Token(t.Context()); !errors.Is(err, ErrTokenExchange) {
		t.Fatalf("refresh error = %v", err)
	}
	if got, err := c.Load(t.Context(), id); err != nil || got.Token.RefreshToken != stored.RefreshToken {
		t.Fatalf("credential after description-only response = %#v, %v", got, err)
	}
}

func TestRefreshSourceLoginRequiredStateCauses(t *testing.T) {
	f := newRefreshFixture(t)
	id := identity("remote.example:443")
	id.Issuer = f.srv.URL
	id, _ = id.Canonical()
	cases := []struct {
		name  string
		token Token
		want  LoginRequiredCause
	}{
		{"absent", Token{}, NotEnrolled},
		{"malformed expiry", Token{AccessToken: "opaque", TokenType: "Bearer", Expiry: "not-a-time"}, CredentialUnusable},
		{"without refresh", Token{AccessToken: "opaque", TokenType: "Bearer", Expiry: time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)}, SessionExpired},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := credentials(t)
			if tc.name != "absent" {
				if _, err := c.Save(t.Context(), id, tc.token, nil); err != nil {
					t.Fatal(err)
				}
			}
			s := f.source(t, c, id, time.Hour)
			defer s.Close()
			_, err := s.Token(t.Context())
			var got *LoginRequiredError
			if !errors.As(err, &got) || got.Cause != tc.want || !errors.Is(err, ErrLoginRequired) {
				t.Fatalf("err = %v, cause = %#v, want %s", err, got, tc.want)
			}
		})
	}
}

func TestRefreshSourceActivityGatesProactiveRefresh(t *testing.T) {
	f := newRefreshFixture(t)
	c := credentials(t)
	id := identity("remote.example:443")
	id.Issuer = f.srv.URL
	id, _ = id.Canonical()
	original := Token{AccessToken: f.token(t, "original", time.Now().Add(time.Minute)), RefreshToken: "refresh-original", TokenType: "Bearer", Expiry: time.Now().Add(time.Minute).UTC().Format(time.RFC3339)}
	if _, err := c.Save(t.Context(), id, original, nil); err != nil {
		t.Fatal(err)
	}
	s := f.source(t, c, id, 10*time.Millisecond)
	defer s.Close()
	if _, err := s.Token(t.Context()); err != nil {
		t.Fatal(err)
	}
	// Serialize the test's setup mutation with the source, just as another
	// credential operation in the application would be serialized.
	s.mu.Lock()
	rec, err := c.Load(t.Context(), id)
	if err != nil {
		s.mu.Unlock()
		t.Fatal(err)
	}
	near := original
	near.Expiry = time.Now().Add(100 * time.Millisecond).UTC().Format(time.RFC3339)
	if _, err := c.Save(t.Context(), id, near, &rec.Version); err != nil {
		s.mu.Unlock()
		t.Fatal(err)
	}
	s.mu.Unlock()
	deadline := time.Now().Add(time.Second)
	for f.calls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := f.calls.Load(); got != 1 {
		t.Fatalf("refresh calls = %d, want 1", got)
	}
	// The proactive refresh itself cannot make the next refresh eligible.
	time.Sleep(80 * time.Millisecond)
	if got := f.calls.Load(); got != 1 {
		t.Fatalf("self-sustaining refresh calls = %d, want 1", got)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	stored, err := c.Load(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Token.RefreshToken != "refresh-rotated" {
		t.Fatalf("refresh token was not rotated: %q", stored.Token.RefreshToken)
	}
}

func TestPublicRefreshDoesNotRepeatInPoller(t *testing.T) {
	f := newRefreshFixture(t)
	c := credentials(t)
	id := identity("remote.example:443")
	id.Issuer = f.srv.URL
	id, _ = id.Canonical()
	near := Token{
		AccessToken:  f.token(t, "public-refresh", time.Now().Add(time.Minute)),
		RefreshToken: "refresh-original",
		TokenType:    "Bearer",
		Expiry:       time.Now().Add(100 * time.Millisecond).UTC().Format(time.RFC3339),
	}
	if _, err := c.Save(t.Context(), id, near, nil); err != nil {
		t.Fatal(err)
	}
	s := f.source(t, c, id, 10*time.Millisecond)
	defer s.Close()
	if _, err := s.Token(t.Context()); err != nil {
		t.Fatal(err)
	}
	time.Sleep(80 * time.Millisecond)
	if got := f.calls.Load(); got != 1 {
		t.Fatalf("public refresh was repeated by poller: %d calls", got)
	}
}

func TestBackgroundRejectedRefreshIsReportedOnNextToken(t *testing.T) {
	f := newRefreshFixture(t)
	c := credentials(t)
	id := identity("remote.example:443")
	id.Issuer = f.srv.URL
	id, _ = id.Canonical()
	original := Token{AccessToken: f.token(t, "background", time.Now().Add(time.Minute)), RefreshToken: "refresh-original", TokenType: "Bearer", Expiry: time.Now().Add(time.Minute).UTC().Format(time.RFC3339)}
	if _, err := c.Save(t.Context(), id, original, nil); err != nil {
		t.Fatal(err)
	}
	s := f.source(t, c, id, 10*time.Millisecond)
	defer s.Close()
	if _, err := s.Token(t.Context()); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	rec, err := c.Load(t.Context(), id)
	if err != nil {
		s.mu.Unlock()
		t.Fatal(err)
	}
	near := original
	near.Expiry = time.Now().Add(100 * time.Millisecond).UTC().Format(time.RFC3339)
	if _, err := c.Save(t.Context(), id, near, &rec.Version); err != nil {
		s.mu.Unlock()
		t.Fatal(err)
	}
	f.bad.Store(true)
	s.mu.Unlock()
	deadline := time.Now().Add(time.Second)
	for f.calls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if f.calls.Load() == 0 {
		t.Fatalf("background refresh did not run")
	}
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = s.Token(cancelled)
	if err == nil || errors.Is(err, ErrLoginRequired) {
		t.Fatalf("cancelled Token masked an operational error: %v", err)
	}
	_, err = s.Token(t.Context())
	var required *LoginRequiredError
	if !errors.As(err, &required) || required.Cause != SessionExpired || !errors.Is(err, ErrLoginRequired) {
		t.Fatalf("next Token error = %v, cause = %#v", err, required)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Load(t.Context(), id); !errors.Is(err, credentialstore.ErrNotFound) {
		t.Fatalf("credential after rejection = %v", err)
	}
}

func TestNewRefreshSourceUsesDiscovery(t *testing.T) {
	f := newRefreshFixture(t)
	c := credentials(t)
	id := identity("remote.example:443")
	id.Issuer = f.srv.URL
	id, _ = id.Canonical()
	tok := Token{AccessToken: f.token(t, "constructor", time.Now().Add(time.Minute)), RefreshToken: "refresh-original", TokenType: "Bearer", Expiry: time.Now().Add(time.Minute).UTC().Format(time.RFC3339)}
	if _, err := c.Save(t.Context(), id, tok, nil); err != nil {
		t.Fatal(err)
	}
	registry, err := OpenRegistry(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s, err := NewRefreshSource(t.Context(), c, LoginConfig{Identity: id, HTTPClient: f.srv.Client(), Registry: registry})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if got, err := s.Token(t.Context()); err != nil || got != tok.AccessToken {
		t.Fatalf("Token = %q, %v", got, err)
	}
}

func TestRefreshTransactionFinishesBeforeWaitingEnrollment(t *testing.T) {
	f := newRefreshFixture(t)
	f.bad.Store(true)
	entered := make(chan struct{})
	f.tokenEntered = entered
	release := make(chan struct{})
	f.tokenRelease = release

	root := t.TempDir()
	registry, err := OpenRegistry(root)
	if err != nil {
		t.Fatal(err)
	}
	store, err := credentialstore.NewEncryptedFile(root, "refresh-enrollment-race", bytesOf(1))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	creds, err := NewCredentials(store)
	if err != nil {
		t.Fatal(err)
	}
	id := identity("refresh-enroll.example:443")
	id.Issuer = f.srv.URL
	id, _ = id.Canonical()
	old := Token{AccessToken: f.token(t, "old", time.Now().Add(-time.Minute)), RefreshToken: "old-refresh", TokenType: "Bearer", Expiry: time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)}
	if _, err := creds.Save(t.Context(), id, old, nil); err != nil {
		t.Fatal(err)
	}
	validator, err := authoidc.NewValidator(t.Context(), authoidc.Config{Issuer: f.srv.URL, JWKSURI: f.srv.URL + "/keys", Audience: "vmcp", HTTPClient: f.srv.Client()})
	if err != nil {
		t.Fatal(err)
	}
	source := newRefreshSource(id, creds, registry, f.srv.Client(), f.srv.URL+"/token", validator, 0)
	defer source.Close()
	refreshDone := make(chan error, 1)
	go func() { _, tokenErr := source.Token(context.Background()); refreshDone <- tokenErr }()
	<-entered

	probeCtx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	if _, err := registry.lockTarget(probeCtx, id.Target); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("refresh did not hold target transaction: %v", err)
	}
	enrollDone := make(chan error, 1)
	enrollLockAttempt := make(chan struct{})
	registry.targetLockAttempt = func() { close(enrollLockAttempt) }
	newToken := Token{AccessToken: "new-login", RefreshToken: "new-refresh", TokenType: "Bearer"}
	go func() {
		enrollDone <- Enroll(context.Background(), Connection{Identity: id}, newToken, EnrollmentConfig{Registry: registry, Credentials: creds})
	}()
	<-enrollLockAttempt
	select {
	case err := <-enrollDone:
		t.Fatalf("enrollment bypassed refresh target lock: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	if err := <-refreshDone; !errors.Is(err, ErrLoginRequired) {
		t.Fatalf("old refresh result = %v", err)
	}
	if err := <-enrollDone; err != nil {
		t.Fatal(err)
	}
	rec, err := creds.Load(t.Context(), id)
	if err != nil || rec.Token.AccessToken != newToken.AccessToken {
		t.Fatalf("credential after serialized refresh/enroll = %#v, %v", rec.Token, err)
	}
}

func TestRefreshSourceIdleDoesNotRefreshAndRejectDeletes(t *testing.T) {
	f := newRefreshFixture(t)
	c := credentials(t)
	id := identity("remote.example:443")
	id.Issuer = f.srv.URL
	id, _ = id.Canonical()
	old := Token{AccessToken: f.token(t, "old", time.Now().Add(time.Minute)), RefreshToken: "refresh-old", TokenType: "Bearer", Expiry: time.Now().Add(50 * time.Millisecond).UTC().Format(time.RFC3339)}
	if _, err := c.Save(t.Context(), id, old, nil); err != nil {
		t.Fatal(err)
	}
	s := f.source(t, c, id, 10*time.Millisecond)
	defer s.Close()
	time.Sleep(100 * time.Millisecond)
	if f.calls.Load() != 0 {
		t.Fatalf("idle source refreshed: %d", f.calls.Load())
	}
	f.bad.Store(true)
	_, err := s.Token(t.Context())
	var typed *LoginRequiredError
	if !errors.Is(err, ErrLoginRequired) || !errors.As(err, &typed) || typed.Cause != SessionExpired {
		t.Fatalf("rejection = %v, cause = %#v", err, typed)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Load(t.Context(), id); !errors.Is(err, credentialstore.ErrNotFound) {
		t.Fatalf("credential after rejection = %v", err)
	}
}
