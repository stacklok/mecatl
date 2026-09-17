package anthropicsub

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/mcp/oauthlogin"
)

func decodeJSONBody(t *testing.T, r *http.Request) map[string]string {
	t.Helper()
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	body := map[string]string{}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("token request is not a JSON object: %v (%s)", err, raw)
	}
	return body
}

type launcherStub func(context.Context, string) error

func (f launcherStub) Open(ctx context.Context, rawURL string) error { return f(ctx, rawURL) }

// The token endpoint takes JSON, not form encoding, and the grant must carry
// the PKCE verifier matching the presented challenge.
func TestLoginExchangesJSONWithMatchingVerifier(t *testing.T) {
	if held, err := net.Listen("tcp4", "127.0.0.1:54545"); err != nil {
		t.Skip("callback port 54545 is already in use")
	} else {
		_ = held.Close()
	}

	var submitted map[string]string
	var contentType string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		contentType = r.Header.Get("Content-Type")
		submitted = decodeJSONBody(t, r)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "access-canary",
			"refresh_token": "refresh-canary",
			"expires_in":    3600,
			"account":       map[string]string{"uuid": "acct-1", "email_address": "user@example.test"},
			"organization":  map[string]string{"uuid": "org-1", "name": "Example Org"},
		})
	}))
	defer server.Close()

	var presentedChallenge, presentedScope string
	launcher := launcherStub(func(_ context.Context, authorizationURL string) error {
		parsed, err := url.Parse(authorizationURL)
		if err != nil {
			return err
		}
		query := parsed.Query()
		presentedChallenge = query.Get("code_challenge")
		presentedScope = query.Get("scope")
		if query.Get("code") != "true" {
			t.Errorf("authorize URL missing the code marker")
		}
		if query.Get("code_challenge_method") != "S256" {
			t.Errorf("challenge method = %q", query.Get("code_challenge_method"))
		}
		callback := query.Get("redirect_uri") + "?" + url.Values{
			"code":  {"auth-code"},
			"state": {query.Get("state")},
		}.Encode()
		resp, err := (&http.Client{Timeout: 2 * time.Second}).Get(callback)
		if err != nil {
			return err
		}
		_ = resp.Body.Close()
		return nil
	})

	tokens, err := login(t.Context(), LoginOptions{
		Launcher:   launcher,
		HTTPClient: server.Client(),
	}, endpointSet{authorize: "https://claude.ai/oauth/authorize", token: server.URL})
	if err != nil {
		t.Fatal(err)
	}

	if contentType != "application/json" {
		t.Fatalf("token Content-Type = %q, want application/json", contentType)
	}
	if presentedScope != oauthScope {
		t.Fatalf("scope = %q, want %q", presentedScope, oauthScope)
	}
	if !strings.Contains(presentedScope, "user:inference") {
		t.Fatal("scope lacks user:inference; the grant could not serve inference")
	}
	sum := sha256.Sum256([]byte(submitted["code_verifier"]))
	if got := base64.RawURLEncoding.EncodeToString(sum[:]); got != presentedChallenge {
		t.Fatal("submitted verifier does not match the presented challenge")
	}
	if submitted["grant_type"] != "authorization_code" || submitted["client_id"] != oauthClientID {
		t.Fatalf("token request = %+v", submitted)
	}
	if tokens.AccountID != "acct-1" || tokens.Email != "user@example.test" {
		t.Fatalf("identity = %+v", tokens)
	}
	if tokens.OrgID != "org-1" || tokens.OrgName != "Example Org" {
		t.Fatalf("organization = %+v", tokens)
	}
	if tokens.AuthorizedAt.IsZero() {
		t.Fatal("AuthorizedAt was not anchored; the grant lifetime cannot be tracked")
	}

	listener, err := net.Listen("tcp4", "127.0.0.1:54545")
	if err != nil {
		t.Fatalf("callback port was not released: %v", err)
	}
	_ = listener.Close()
}

// Expiry is stored with a margin so a token is replaced before the provider
// considers it lapsed.
func TestExpiryAppliesSafetyMargin(t *testing.T) {
	before := time.Now().UTC()
	got := expiryFrom(3600)
	want := before.Add(time.Hour - expiryMargin)
	if got.Before(want.Add(-5*time.Second)) || got.After(want.Add(5*time.Second)) {
		t.Fatalf("expiry = %s, want about %s", got, want)
	}
}

// A refresh must not rewrite organization identity: the provider omits it on
// refresh, and re-keying would move the credential between workspaces.
func TestRefreshPreservesOrganizationAndAnchor(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("anthropic-beta"); got != oauthBeta {
			t.Errorf("refresh anthropic-beta = %q, want %q", got, oauthBeta)
		}
		if got := r.Header.Get("User-Agent"); got != refreshUserAgent {
			t.Errorf("refresh User-Agent = %q, want %q", got, refreshUserAgent)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "access-next",
			"refresh_token": "refresh-next",
			"expires_in":    3600,
		})
	}))
	defer server.Close()

	anchor := time.Now().Add(-10 * 24 * time.Hour).UTC()
	previous := OAuthTokens{
		RefreshToken: "refresh-original",
		AuthorizedAt: anchor,
		AccountID:    "acct-1",
		Email:        "user@example.test",
		OrgID:        "org-1",
		OrgName:      "Example Org",
	}
	next, err := refresh(t.Context(), server.Client(), previous, endpointSet{token: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	if next.OrgID != "org-1" || next.OrgName != "Example Org" {
		t.Fatalf("organization was rewritten: %+v", next)
	}
	if !next.AuthorizedAt.Equal(anchor) {
		t.Fatalf("AuthorizedAt = %s, want the original anchor %s", next.AuthorizedAt, anchor)
	}
	if next.Email != "user@example.test" || next.AccountID != "acct-1" {
		t.Fatalf("identity was lost: %+v", next)
	}
	if next.AccessToken != "access-next" || next.RefreshToken != "refresh-next" {
		t.Fatalf("tokens = %+v", next)
	}
}

// A provider that omits a rotated refresh token leaves the supplied one valid.
func TestRefreshCarriesForwardOmittedRefreshToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "access-next",
			"expires_in":   3600,
		})
	}))
	defer server.Close()

	next, err := refresh(t.Context(), server.Client(),
		OAuthTokens{RefreshToken: "refresh-original"}, endpointSet{token: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	if next.RefreshToken != "refresh-original" {
		t.Fatalf("refresh token = %q, want the supplied one carried forward", next.RefreshToken)
	}
}

// A refused refresh must not surface the provider body, which can quote the grant.
func TestRefreshRejectionIsSecretFree(t *testing.T) {
	const secret = "DO-NOT-SURFACE-THIS-GRANT"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = fmt.Fprintf(w, `{"error":"invalid_grant","error_description":%q}`, secret)
	}))
	defer server.Close()

	_, err := refresh(t.Context(), server.Client(),
		OAuthTokens{RefreshToken: secret}, endpointSet{token: server.URL})
	if err == nil {
		t.Fatal("refused refresh returned no error")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("error surfaced the grant: %v", err)
	}
}

// The grant family dies about a month after the interactive login regardless
// of rotation, so the deadline must be reported from the anchor.
func TestGrantLifetimeIsAnchoredAtLogin(t *testing.T) {
	anchor := time.Now().Add(-29 * 24 * time.Hour).UTC()
	tokens := OAuthTokens{AccessToken: "a", AuthorizedAt: anchor}
	if got, want := tokens.GrantExpiresAt(), anchor.Add(GrantLifetime); !got.Equal(want) {
		t.Fatalf("grant expiry = %s, want %s", got, want)
	}
	if (OAuthTokens{}).GrantExpiresAt() != (time.Time{}) {
		t.Fatal("an unanchored grant reported a deadline")
	}
}

// Tokens and the account email are secrets or identity; neither fmt nor slog
// may render them, including nested in another struct.
func TestOAuthTokensRedactSecrets(t *testing.T) {
	tokens := OAuthTokens{
		AccessToken:  "access-secret",
		RefreshToken: "refresh-secret",
		Email:        "user@secret.test",
		AccountID:    "acct-secret",
	}
	nested := struct{ Tokens OAuthTokens }{Tokens: tokens}
	for _, rendered := range []string{
		fmt.Sprintf("%v", tokens),
		fmt.Sprintf("%s", tokens),
		fmt.Sprintf("%+v", nested),
		tokens.LogValue().String(),
	} {
		for _, secret := range []string{"access-secret", "refresh-secret", "user@secret.test", "acct-secret"} {
			if strings.Contains(rendered, secret) {
				t.Fatalf("rendering %q leaked %q", rendered, secret)
			}
		}
	}
}

// The login flow must use the pinned loopback redirect the provider expects.
func TestLoginUsesPinnedRedirect(t *testing.T) {
	if _, err := oauthlogin.New(oauthlogin.Options{RedirectURL: oauthlogin.AnthropicRedirectURL}); err != nil {
		t.Fatalf("pinned Anthropic redirect was rejected: %v", err)
	}
	if !strings.HasSuffix(oauthlogin.AnthropicRedirectURL, ":54545/callback") {
		t.Fatalf("redirect = %q, want port 54545 and /callback", oauthlogin.AnthropicRedirectURL)
	}
}

// A pasted code may carry its state after a '#'; the loopback callback never
// produces that shape but a manual paste does.
func TestExchangeSplitsPastedCodeFragment(t *testing.T) {
	var submitted map[string]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		submitted = decodeJSONBody(t, r)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "access-canary",
			"refresh_token": "refresh-canary",
			"expires_in":    3600,
		})
	}))
	defer server.Close()

	_, err := exchangeCode(t.Context(), server.Client(), endpointSet{token: server.URL},
		oauthlogin.Result{Code: "the-code#the-state", State: "ignored"},
		"verifier", oauthlogin.AnthropicRedirectURL)
	if err != nil {
		t.Fatal(err)
	}
	if submitted["code"] != "the-code" {
		t.Fatalf("code = %q, want the fragment stripped", submitted["code"])
	}
	if submitted["state"] != "the-state" {
		t.Fatalf("state = %q, want the fragment value", submitted["state"])
	}
}
