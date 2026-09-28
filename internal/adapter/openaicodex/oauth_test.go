package openaicodex

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/internal/testutil/codextest"
)

func formBody(t *testing.T, r *http.Request) url.Values {
	t.Helper()
	if err := r.ParseForm(); err != nil {
		t.Fatalf("parse form: %v", err)
	}
	return r.PostForm
}

// tokenStub serves the token endpoint and records the last submitted form.
func tokenStub(t *testing.T, token string) (*http.Client, string, *url.Values) {
	t.Helper()
	var submitted url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		submitted = formBody(t, r)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  token,
			"refresh_token": "refresh-next",
			"expires_in":    3600,
		})
	}))
	t.Cleanup(server.Close)
	return server.Client(), server.URL, &submitted
}

const (
	deviceVerifyURLCanary = "https://verify.example.test/device"
	deviceRedirectCanary  = "https://redirect.example.test/deviceauth/callback"
)

// The account id must come from the token's own claims. A refreshed token that
// belongs to another workspace must re-route with it, and a caller must not be
// able to pin routing to a workspace the token does not name.
func TestRefreshTakesAccountIDFromTokenClaims(t *testing.T) {
	expires := time.Now().Add(time.Hour)
	client, tokenURL, submitted := tokenStub(t, codextest.Token(expires, "acct-from-claims"))

	tokens, err := refresh(t.Context(), client, "refresh-original", endpointSet{token: tokenURL})
	if err != nil {
		t.Fatal(err)
	}
	if tokens.AccountID != "acct-from-claims" {
		t.Fatalf("account id = %q, want the claim value", tokens.AccountID)
	}
	if got := submitted.Get("grant_type"); got != "refresh_token" {
		t.Fatalf("grant_type = %q", got)
	}
	if got := submitted.Get("client_id"); got != oauthClientID {
		t.Fatalf("client_id = %q", got)
	}
	if got := submitted.Get("refresh_token"); got != "refresh-original" {
		t.Fatalf("refresh_token = %q", got)
	}
	if tokens.ExpiresAt.IsZero() {
		t.Fatal("expiry was not derived from expires_in")
	}
}

// A provider that omits a rotated refresh token leaves the supplied one valid;
// dropping it would strand the grant and force a fresh interactive login.
func TestRefreshCarriesForwardOmittedRefreshToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": codextest.Token(time.Now().Add(time.Hour), "acct-keep"),
			"expires_in":   3600,
		})
	}))
	defer server.Close()

	tokens, err := refresh(t.Context(), server.Client(), "refresh-original", endpointSet{token: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	if tokens.RefreshToken != "refresh-original" {
		t.Fatalf("refresh token = %q, want the supplied one carried forward", tokens.RefreshToken)
	}
}

// A refused refresh must not surface the provider body, which can quote the
// submitted grant.
func TestRefreshRejectionIsBoundedAndSecretFree(t *testing.T) {
	const secret = "DO-NOT-SURFACE-THIS-GRANT"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = fmt.Fprintf(w, `{"error":"invalid_grant","error_description":%q}`, secret)
	}))
	defer server.Close()

	_, err := refresh(t.Context(), server.Client(), secret, endpointSet{token: server.URL})
	if err == nil {
		t.Fatal("refused refresh returned no error")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("error surfaced the grant: %v", err)
	}
}

// An access token whose claims carry no account id cannot be routed, so it must
// be refused rather than stored and used without the ChatGPT-Account-ID header.
func TestRefreshRejectsTokenWithoutAccountID(t *testing.T) {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"exp":9999999999}`))
	signature := base64.RawURLEncoding.EncodeToString([]byte("signature"))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  header + "." + payload + "." + signature,
			"refresh_token": "refresh-next",
			"expires_in":    3600,
		})
	}))
	defer server.Close()

	if _, err := refresh(t.Context(), server.Client(), "refresh-original", endpointSet{token: server.URL}); err == nil {
		t.Fatal("token without an account id was accepted")
	}
}

// PKCE must be S256 over the exact verifier sent to the token endpoint; a
// mismatch downgrades the flow to a bearer authorization code.
func TestPKCEChallengeMatchesVerifier(t *testing.T) {
	verifier, challenge, err := newPKCE()
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(verifier))
	if want := base64.RawURLEncoding.EncodeToString(sum[:]); challenge != want {
		t.Fatalf("challenge = %q, want S256 of the verifier", challenge)
	}
	raw, err := base64.RawURLEncoding.DecodeString(verifier)
	if err != nil || len(raw) != pkceVerifierBytes {
		t.Fatalf("verifier decoded to %d bytes (err %v), want %d", len(raw), err, pkceVerifierBytes)
	}
	if strings.ContainsAny(verifier, "+/=") {
		t.Fatalf("verifier is not base64url: %q", verifier)
	}
}

// The authorization request must carry PKCE and must not identify this client
// as Codex CLI. ADR 0215 forbids sending another vendor's client version.
func TestAuthorizeURLIsPKCEBoundAndHonest(t *testing.T) {
	raw := authorizeURL(defaultEndpoints, "state-canary", "http://localhost:1455/auth/callback", "challenge-canary", "mecatl")
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	for key, want := range map[string]string{
		"response_type":              "code",
		"client_id":                  oauthClientID,
		"redirect_uri":               "http://localhost:1455/auth/callback",
		"scope":                      oauthScope,
		"code_challenge":             "challenge-canary",
		"code_challenge_method":      "S256",
		"state":                      "state-canary",
		"id_token_add_organizations": "true",
		"originator":                 "mecatl",
	} {
		if got := query.Get(key); got != want {
			t.Fatalf("%s = %q, want %q", key, got, want)
		}
	}
	if query.Has("codex_cli_simplified_flow") {
		t.Fatal("authorize URL claims the Codex CLI flow")
	}
	if strings.Contains(raw, "0.144.1") {
		t.Fatal("authorize URL carries the Codex CLI release version")
	}
}

// Device polling must treat 403/404 as "not yet authorized" and keep waiting,
// then exchange the returned code and verifier.
func TestDeviceLoginPollsThroughPendingStatuses(t *testing.T) {
	polls := 0
	token := codextest.Token(time.Now().Add(time.Hour), "acct-device")
	mux := http.NewServeMux()
	mux.HandleFunc("/usercode", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"device_auth_id": "device-canary",
			"user_code":      "CODE-123",
			"interval":       "1",
		})
	})
	mux.HandleFunc("/devicetoken", func(w http.ResponseWriter, _ *http.Request) {
		polls++
		switch polls {
		case 1:
			w.WriteHeader(http.StatusForbidden)
		case 2:
			w.WriteHeader(http.StatusNotFound)
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"authorization_code": "device-code",
				"code_verifier":      "device-verifier",
			})
		}
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		form := formBody(t, r)
		if got := form.Get("code_verifier"); got != "device-verifier" {
			t.Errorf("code_verifier = %q", got)
		}
		if got := form.Get("redirect_uri"); got != deviceRedirectCanary {
			t.Errorf("redirect_uri = %q, want the device callback", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  token,
			"refresh_token": "refresh-device",
			"expires_in":    3600,
		})
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	endpoints := endpointSet{
		token:          server.URL + "/token",
		deviceUserCode: server.URL + "/usercode",
		deviceToken:    server.URL + "/devicetoken",
		deviceVerify:   deviceVerifyURLCanary,
		deviceRedirect: deviceRedirectCanary,
	}

	var shownURL, shownCode string
	tokens, err := loginDevice(t.Context(), DeviceOptions{
		Prompt: func(verificationURL, userCode string) error {
			shownURL, shownCode = verificationURL, userCode
			return nil
		},
		HTTPClient: server.Client(),
		Sleep:      func(context.Context, time.Duration) error { return nil },
	}, endpoints)
	if err != nil {
		t.Fatal(err)
	}
	if polls != 3 {
		t.Fatalf("polls = %d, want the two pending statuses then success", polls)
	}
	if shownCode != "CODE-123" || shownURL != deviceVerifyURLCanary {
		t.Fatalf("prompt got %q / %q", shownURL, shownCode)
	}
	if tokens.AccountID != "acct-device" || tokens.RefreshToken != "refresh-device" {
		t.Fatalf("tokens = %+v", tokens)
	}
}

// Device login without a prompt can never be completed by the operator.
func TestDeviceLoginRequiresPrompt(t *testing.T) {
	if _, err := loginDevice(t.Context(), DeviceOptions{}, defaultEndpoints); err == nil {
		t.Fatal("device login without a prompt was accepted")
	}
}

// The advertised interval is honored but floored, so a provider reporting 0
// cannot produce a tight poll loop.
func TestDevicePollIntervalIsFloored(t *testing.T) {
	for raw, want := range map[string]time.Duration{
		`0`:    devicePollFloor + devicePollSafetyMargin,
		`"1"`:  devicePollFloor + devicePollSafetyMargin,
		`10`:   10*time.Second + devicePollSafetyMargin,
		`"12"`: 12*time.Second + devicePollSafetyMargin,
		`null`: devicePollFloor + devicePollSafetyMargin,
	} {
		if got := devicePollInterval(json.RawMessage(raw)); got != want {
			t.Fatalf("devicePollInterval(%s) = %s, want %s", raw, got, want)
		}
	}
}

// Tokens are bearer secrets; neither fmt nor slog may render them, including
// when nested in another struct.
func TestOAuthTokensRedactEverySecret(t *testing.T) {
	tokens := OAuthTokens{
		AccessToken:  "access-secret",
		RefreshToken: "refresh-secret",
		AccountID:    "acct-secret",
		ExpiresAt:    time.Now(),
	}
	nested := struct{ Tokens OAuthTokens }{Tokens: tokens}
	for _, rendered := range []string{
		fmt.Sprintf("%v", tokens),
		fmt.Sprintf("%s", tokens),
		fmt.Sprintf("%+v", nested),
		tokens.LogValue().String(),
	} {
		for _, secret := range []string{"access-secret", "refresh-secret", "acct-secret"} {
			if strings.Contains(rendered, secret) {
				t.Fatalf("rendering %q leaked %q", rendered, secret)
			}
		}
	}
}

// A grant projects into the same validated snapshot a pasted token produces,
// so refreshed credentials cannot bypass the credential rules.
func TestOAuthTokensProjectIntoValidatedCredential(t *testing.T) {
	now := time.Now()
	tokens := OAuthTokens{
		AccessToken: codextest.Token(now.Add(time.Hour), "acct-project"),
		AccountID:   "acct-project",
		ExpiresAt:   now.Add(time.Hour),
	}
	credential, err := tokens.Credential(now)
	if err != nil {
		t.Fatal(err)
	}
	if !credential.Configured() {
		t.Fatal("projected credential is not configured")
	}
	if err := credential.Validate(now); err != nil {
		t.Fatalf("projected credential invalid: %v", err)
	}

	expired := OAuthTokens{
		AccessToken: codextest.Token(now.Add(-time.Hour), "acct-project"),
		AccountID:   "acct-project",
		ExpiresAt:   now.Add(-time.Hour),
	}
	if _, err := expired.Credential(now); err == nil {
		t.Fatal("expired grant projected into a usable credential")
	}
}

// End-to-end browser flow: the pinned loopback callback receives the redirect,
// the verifier sent to the token endpoint must be the one whose S256 challenge
// was presented, and the resulting grant must carry the claim-derived account.
func TestLoginCompletesOverPinnedLoopbackCallback(t *testing.T) {
	if held, err := net.Listen("tcp4", "127.0.0.1:1455"); err != nil {
		t.Skip("callback port 1455 is already in use")
	} else {
		_ = held.Close()
	}

	var presentedChallenge, submittedVerifier string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		submittedVerifier = formBody(t, r).Get("code_verifier")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  codextest.Token(time.Now().Add(time.Hour), "acct-e2e"),
			"refresh_token": "refresh-e2e",
			"expires_in":    3600,
		})
	}))
	defer server.Close()

	launcher := launcherStub(func(_ context.Context, authorizationURL string) error {
		parsed, err := url.Parse(authorizationURL)
		if err != nil {
			return err
		}
		query := parsed.Query()
		presentedChallenge = query.Get("code_challenge")
		callback := query.Get("redirect_uri") + "?" + url.Values{
			"code":  {"code-e2e"},
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
	}, endpointSet{authorize: "https://auth.example.test/oauth/authorize", token: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	if tokens.AccountID != "acct-e2e" || tokens.RefreshToken != "refresh-e2e" {
		t.Fatalf("tokens = %+v", tokens)
	}
	if submittedVerifier == "" || presentedChallenge == "" {
		t.Fatal("PKCE values were not exchanged")
	}
	sum := sha256.Sum256([]byte(submittedVerifier))
	if got := base64.RawURLEncoding.EncodeToString(sum[:]); got != presentedChallenge {
		t.Fatal("submitted verifier does not match the presented challenge")
	}

	listener, err := net.Listen("tcp4", "127.0.0.1:1455")
	if err != nil {
		t.Fatalf("callback port was not released: %v", err)
	}
	_ = listener.Close()
}

type launcherStub func(context.Context, string) error

func (f launcherStub) Open(ctx context.Context, rawURL string) error { return f(ctx, rawURL) }
