package openaicodex

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/stacklok/mecatl/mcp/oauthlogin"
)

// OAuth client identity for ChatGPT subscription login.
//
// The client id is OpenAI's public Codex OAuth application: a subscription
// grant can only be minted through it, and it is a public client, so PKCE
// carries the proof of possession and no client secret exists.
//
// The redirect is registered with the provider as an exact string, so it is
// owned by oauthlogin.CodexRedirectURL rather than derived here.
const (
	oauthClientID = "app_EMoamEEZ73f0CkXaXp7hrann"
	oauthIssuer   = "https://auth.openai.com"
	oauthScope    = "openid profile email offline_access api.connectors.read api.connectors.invoke"
)

// endpointSet names every provider URL the flows contact. Production always
// uses defaultEndpoints; it is a struct so tests can exercise the real request
// and response handling against a local server instead of mutating globals.
type endpointSet struct {
	authorize      string
	token          string
	deviceUserCode string
	deviceToken    string
	deviceVerify   string
	deviceRedirect string
}

var defaultEndpoints = endpointSet{
	authorize:      "https://auth.openai.com/oauth/authorize",
	token:          "https://auth.openai.com/oauth/token",
	deviceUserCode: "https://auth.openai.com/api/accounts/deviceauth/usercode",
	deviceToken:    "https://auth.openai.com/api/accounts/deviceauth/token",
	deviceVerify:   "https://auth.openai.com/codex/device",
	deviceRedirect: "https://auth.openai.com/deviceauth/callback",
}

const (
	// oauthRequestTimeout bounds one token-endpoint round trip. Login as a
	// whole is bounded by the caller's context, not by this.
	oauthRequestTimeout = 15 * time.Second
	// oauthMaxResponseBytes bounds a token or device-authorization response.
	oauthMaxResponseBytes = 1 << 20

	pkceVerifierBytes = 96
	stateBytes        = 16

	// devicePollSafetyMargin is added to the interval the provider advertises
	// so a clock difference cannot turn into a tight poll loop.
	devicePollSafetyMargin = 3 * time.Second
	devicePollFloor        = 5 * time.Second
	// deviceMaxPolls bounds device polling so a provider that never reaches a
	// terminal state cannot loop forever.
	deviceMaxPolls = 120
)

var (
	// ErrOAuthAuthorization reports a failed or abandoned authorization interaction.
	ErrOAuthAuthorization = errors.New("openai-codex: subscription authorization failed")
	// ErrOAuthToken reports a token endpoint that refused or malformed a response.
	ErrOAuthToken = errors.New("openai-codex: subscription token endpoint rejected the request")
	// ErrOAuthDeviceTimeout reports device authorization that was never completed.
	ErrOAuthDeviceTimeout = errors.New("openai-codex: device authorization was not completed in time")
)

// OAuthTokens is a renewable ChatGPT subscription grant. Unlike Credential it
// retains the refresh token, so it is the persisted form; Credential remains
// the immutable per-request snapshot handed to RequestPolicy.
//
// Both fmt and slog renderings are redacted: the tokens are bearer secrets and
// the account id, while only routing metadata, is still identity-shaped.
type OAuthTokens struct {
	AccessToken  string
	RefreshToken string
	AccountID    string
	ExpiresAt    time.Time
}

var (
	_ fmt.Formatter  = OAuthTokens{}
	_ slog.LogValuer = OAuthTokens{}
)

// Format keeps every fmt rendering secret-safe, including when OAuthTokens is
// nested inside another formatted struct.
func (t OAuthTokens) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, t.redacted())
}

// LogValue gives structured handlers the same redacted representation as fmt.
func (t OAuthTokens) LogValue() slog.Value {
	return slog.StringValue(t.redacted())
}

func (t OAuthTokens) redacted() string {
	if t.AccessToken == "" {
		return "openai-codex oauth tokens (absent)"
	}
	if t.RefreshToken == "" {
		return "openai-codex oauth tokens (access only)"
	}
	return "openai-codex oauth tokens (renewable)"
}

// Credential projects the grant into the immutable snapshot the request policy
// consumes. It re-validates through NewCredential so a refreshed token is held
// to exactly the same rules as one pasted into auth.yaml.
func (t OAuthTokens) Credential(now time.Time) (Credential, error) {
	expiry := ""
	if !t.ExpiresAt.IsZero() {
		expiry = t.ExpiresAt.UTC().Format(time.RFC3339)
	}
	return NewCredential(t.AccessToken, t.AccountID, expiry, now)
}

// LoginOptions configures an interactive subscription login.
type LoginOptions struct {
	// NoBrowser prints the authorization URL instead of launching a browser.
	// URLWriter is then required.
	NoBrowser bool
	URLWriter io.Writer
	// Launcher overrides browser launching; nil uses the host policy default.
	Launcher oauthlogin.BrowserLauncher
	// HTTPClient overrides the token-endpoint client. nil uses a bounded
	// client that refuses redirects.
	HTTPClient *http.Client
	// Originator identifies this client to the provider. Empty sends the
	// honest mecatl value. It deliberately never defaults to a Codex CLI
	// identity: ADR 0215 forbids claiming to be another vendor's client, and
	// the Codex CLI release version is never sent at all.
	Originator string
}

// Login performs the authorization-code flow with PKCE against a loopback
// callback and returns a renewable grant.
func Login(ctx context.Context, opts LoginOptions) (OAuthTokens, error) {
	return login(ctx, opts, defaultEndpoints)
}

func login(ctx context.Context, opts LoginOptions, endpoints endpointSet) (OAuthTokens, error) {
	runtime, err := oauthlogin.New(oauthlogin.Options{
		NoBrowser:   opts.NoBrowser,
		URLWriter:   opts.URLWriter,
		Launcher:    opts.Launcher,
		RedirectURL: oauthlogin.CodexRedirectURL,
	})
	if err != nil {
		return OAuthTokens{}, err
	}

	client := opts.HTTPClient
	if client == nil {
		client = newOAuthHTTPClient()
	}
	originator := strings.TrimSpace(opts.Originator)
	if originator == "" {
		originator = UserAgent
	}

	var tokens OAuthTokens
	authorizeErr := runtime.Authorize(ctx, oauthIssuer, func(ctx context.Context, redirectURL string, present func(context.Context, string) (oauthlogin.Result, error)) error {
		verifier, challenge, err := newPKCE()
		if err != nil {
			return err
		}
		state, err := newState()
		if err != nil {
			return err
		}
		result, err := present(ctx, authorizeURL(endpoints, state, redirectURL, challenge, originator))
		if err != nil {
			return err
		}
		tokens, err = exchangeAuthorizationCode(ctx, client, endpoints, result.Code, verifier, redirectURL)
		return err
	})
	if authorizeErr != nil {
		return OAuthTokens{}, authorizeErr
	}
	return tokens, nil
}

// DeviceOptions configures a device-code login.
type DeviceOptions struct {
	// Prompt receives the verification URL and user code. It is required:
	// a device login the operator is never shown cannot be completed.
	Prompt func(verificationURL, userCode string) error
	// HTTPClient overrides the endpoint client.
	HTTPClient *http.Client
	// Sleep overrides the poll delay; nil sleeps against the context.
	Sleep func(context.Context, time.Duration) error
}

// LoginDevice performs the device-code flow. It binds no local listener and
// needs no browser on this host, which is what makes it the usable path for a
// daemon or a remote shell.
func LoginDevice(ctx context.Context, opts DeviceOptions) (OAuthTokens, error) {
	return loginDevice(ctx, opts, defaultEndpoints)
}

func loginDevice(ctx context.Context, opts DeviceOptions, endpoints endpointSet) (OAuthTokens, error) {
	if opts.Prompt == nil {
		return OAuthTokens{}, errors.New("openai-codex: device login requires a prompt")
	}
	client := opts.HTTPClient
	if client == nil {
		client = newOAuthHTTPClient()
	}
	sleep := opts.Sleep
	if sleep == nil {
		sleep = sleepContext
	}

	var init struct {
		DeviceAuthID string          `json:"device_auth_id"`
		UserCode     string          `json:"user_code"`
		Interval     json.RawMessage `json:"interval"`
	}
	if err := postJSON(ctx, client, endpoints.deviceUserCode, map[string]string{"client_id": oauthClientID}, &init); err != nil {
		return OAuthTokens{}, err
	}
	if init.DeviceAuthID == "" || init.UserCode == "" {
		return OAuthTokens{}, ErrOAuthToken
	}
	if err := opts.Prompt(endpoints.deviceVerify, init.UserCode); err != nil {
		return OAuthTokens{}, err
	}

	interval := devicePollInterval(init.Interval)
	for range deviceMaxPolls {
		if err := sleep(ctx, interval); err != nil {
			return OAuthTokens{}, err
		}
		var authorized struct {
			AuthorizationCode string `json:"authorization_code"`
			CodeVerifier      string `json:"code_verifier"`
		}
		status, err := postJSONStatus(ctx, client, endpoints.deviceToken, map[string]string{
			"device_auth_id": init.DeviceAuthID,
			"user_code":      init.UserCode,
		}, &authorized)
		if err != nil {
			return OAuthTokens{}, err
		}
		// The device endpoint reports "not yet authorized" as 403/404 rather
		// than an OAuth authorization_pending body, so these are the wait
		// states, not failures.
		if status == http.StatusForbidden || status == http.StatusNotFound {
			continue
		}
		if status != http.StatusOK {
			return OAuthTokens{}, ErrOAuthToken
		}
		if authorized.AuthorizationCode == "" || authorized.CodeVerifier == "" {
			return OAuthTokens{}, ErrOAuthToken
		}
		return exchangeAuthorizationCode(ctx, client, endpoints, authorized.AuthorizationCode, authorized.CodeVerifier, endpoints.deviceRedirect)
	}
	return OAuthTokens{}, ErrOAuthDeviceTimeout
}

// Refresh exchanges a refresh token for a new grant. The provider may omit a
// rotated refresh token, in which case the supplied one remains valid and is
// carried forward.
func Refresh(ctx context.Context, client *http.Client, refreshToken string) (OAuthTokens, error) {
	return refresh(ctx, client, refreshToken, defaultEndpoints)
}

func refresh(ctx context.Context, client *http.Client, refreshToken string, endpoints endpointSet) (OAuthTokens, error) {
	if refreshToken == "" {
		return OAuthTokens{}, ErrOAuthToken
	}
	if client == nil {
		client = newOAuthHTTPClient()
	}
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
		"client_id":     {oauthClientID},
	}
	tokens, err := postTokenForm(ctx, client, endpoints, form)
	if err != nil {
		return OAuthTokens{}, err
	}
	if tokens.RefreshToken == "" {
		tokens.RefreshToken = refreshToken
	}
	return tokens, nil
}

func authorizeURL(endpoints endpointSet, state, redirectURL, challenge, originator string) string {
	query := url.Values{
		"response_type":              {"code"},
		"client_id":                  {oauthClientID},
		"redirect_uri":               {redirectURL},
		"scope":                      {oauthScope},
		"code_challenge":             {challenge},
		"code_challenge_method":      {"S256"},
		"state":                      {state},
		"id_token_add_organizations": {"true"},
		"originator":                 {originator},
	}
	return endpoints.authorize + "?" + query.Encode()
}

func exchangeAuthorizationCode(ctx context.Context, client *http.Client, endpoints endpointSet, code, verifier, redirectURL string) (OAuthTokens, error) {
	if code == "" || verifier == "" {
		return OAuthTokens{}, ErrOAuthToken
	}
	return postTokenForm(ctx, client, endpoints, url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {oauthClientID},
		"code":          {code},
		"code_verifier": {verifier},
		"redirect_uri":  {redirectURL},
	})
}

func postTokenForm(ctx context.Context, client *http.Client, endpoints endpointSet, form url.Values) (OAuthTokens, error) {
	var payload struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoints.token, strings.NewReader(form.Encode()))
	if err != nil {
		return OAuthTokens{}, ErrOAuthToken
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	if err := doJSON(client, req, &payload); err != nil {
		return OAuthTokens{}, err
	}
	if payload.AccessToken == "" || payload.ExpiresIn <= 0 {
		return OAuthTokens{}, ErrOAuthToken
	}

	// The account id is authoritative from the token's own claims, never from
	// a caller-supplied value, so a refreshed token cannot silently re-route
	// to another workspace.
	claims, err := decodeJWTPayload(payload.AccessToken)
	if err != nil {
		return OAuthTokens{}, ErrOAuthToken
	}
	accountID, _, _, err := routingClaims(claims)
	if err != nil || accountID == "" {
		return OAuthTokens{}, ErrOAuthToken
	}
	return OAuthTokens{
		AccessToken:  payload.AccessToken,
		RefreshToken: payload.RefreshToken,
		AccountID:    accountID,
		ExpiresAt:    time.Now().UTC().Add(time.Duration(payload.ExpiresIn) * time.Second),
	}, nil
}

func postJSON(ctx context.Context, client *http.Client, endpoint string, body map[string]string, out any) error {
	status, err := postJSONStatus(ctx, client, endpoint, body, out)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return ErrOAuthToken
	}
	return nil
}

func postJSONStatus(ctx context.Context, client *http.Client, endpoint string, body map[string]string, out any) (int, error) {
	encoded, err := json.Marshal(body)
	if err != nil {
		return 0, ErrOAuthToken
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(encoded)))
	if err != nil {
		return 0, ErrOAuthToken
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return 0, ctxErr
		}
		return 0, ErrOAuthToken
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, oauthMaxResponseBytes))
		_ = resp.Body.Close()
	}()
	if resp.StatusCode != http.StatusOK {
		return resp.StatusCode, nil
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, oauthMaxResponseBytes)).Decode(out); err != nil {
		return resp.StatusCode, ErrOAuthToken
	}
	return resp.StatusCode, nil
}

func doJSON(client *http.Client, req *http.Request, out any) error {
	resp, err := client.Do(req)
	if err != nil {
		if ctxErr := req.Context().Err(); ctxErr != nil {
			return ctxErr
		}
		return ErrOAuthToken
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, oauthMaxResponseBytes))
		_ = resp.Body.Close()
	}()
	// The response body may quote the submitted grant, so it is never
	// surfaced or logged; only the status class distinguishes the failure.
	if resp.StatusCode != http.StatusOK {
		return ErrOAuthToken
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, oauthMaxResponseBytes)).Decode(out); err != nil {
		return ErrOAuthToken
	}
	return nil
}

// newOAuthHTTPClient refuses redirects for the same reason the request policy
// does: a redirect would re-send the grant to an unvetted host.
func newOAuthHTTPClient() *http.Client {
	return &http.Client{
		Timeout: oauthRequestTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func newPKCE() (verifier, challenge string, err error) {
	raw := make([]byte, pkceVerifierBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", "", errors.New("openai-codex: generate PKCE verifier: failed")
	}
	verifier = base64.RawURLEncoding.EncodeToString(raw)
	sum := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(sum[:]), nil
}

func newState() (string, error) {
	raw := make([]byte, stateBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", errors.New("openai-codex: generate authorization state: failed")
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func devicePollInterval(raw json.RawMessage) time.Duration {
	seconds := int64(0)
	if len(raw) > 0 {
		var asNumber int64
		var asString string
		switch {
		case json.Unmarshal(raw, &asNumber) == nil:
			seconds = asNumber
		case json.Unmarshal(raw, &asString) == nil:
			parsed, err := strconv.ParseInt(strings.TrimSpace(asString), 10, 64)
			if err == nil {
				seconds = parsed
			}
		}
	}
	interval := time.Duration(seconds) * time.Second
	if interval < devicePollFloor {
		interval = devicePollFloor
	}
	return interval + devicePollSafetyMargin
}

func sleepContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
