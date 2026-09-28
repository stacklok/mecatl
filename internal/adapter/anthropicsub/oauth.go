// Package anthropicsub acquires and renews Claude subscription (Pro/Max)
// credentials and shapes the requests that carry them.
//
// A subscription grant is not an API key. It is minted through Anthropic's
// first-party client registration and is only honoured on requests that
// reproduce that client's fingerprint, which this package emits verbatim. That
// is a deliberate departure from the honest-originator rule ADR 0215 applies
// to the OpenAI subscription path: there is no fingerprint-free variant of
// this flow, so the choice is to reproduce it or not to support Claude
// subscriptions at all.
package anthropicsub

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/stacklok/mecatl/mcp/oauthlogin"
)

const (
	// oauthClientID is Anthropic's first-party client. The console
	// authorization endpoint issues tokens without user:inference, so it
	// cannot serve inference; only this client against claude.ai yields a
	// usable subscription grant.
	oauthClientID = "9d1c250a-e61b-44d9-88ed-5944d1962f5e"
	oauthIssuer   = "https://claude.ai"
	oauthScope    = "org:create_api_key user:profile user:inference user:sessions:claude_code user:mcp_servers user:file_upload"
)

// endpointSet names every provider URL the flows contact. Production uses
// defaultEndpoints; it is a struct so tests exercise real request and response
// handling against a local server rather than mutating globals.
type endpointSet struct {
	authorize string
	token     string
	bootstrap string
}

var defaultEndpoints = endpointSet{ //nolint:gosec // G101 false-positive on the word "token" in an endpoint URL; these are public provider addresses, not credentials
	authorize: "https://claude.ai/oauth/authorize",
	token:     "https://api.anthropic.com/v1/oauth/token",
	bootstrap: "https://api.anthropic.com/api/claude_cli/bootstrap",
}

const (
	oauthRequestTimeout   = 30 * time.Second
	oauthMaxResponseBytes = 1 << 20

	pkceVerifierBytes = 96
	stateBytes        = 16

	// expiryMargin is subtracted from the provider's expires_in so a token is
	// replaced before the provider considers it lapsed.
	expiryMargin = 5 * time.Minute

	// GrantLifetime is the absolute life of a grant family, anchored at the
	// interactive login. Refresh-token rotation does not extend it: roughly a
	// month after authorization the token endpoint refuses the newest rotated
	// token and only a fresh interactive login recovers the account. Callers
	// use this to warn before the deadline; it is a display heuristic, not a
	// wire contract.
	GrantLifetime = 30 * 24 * time.Hour
)

var (
	// ErrOAuthToken reports a token endpoint that refused or malformed a response.
	ErrOAuthToken = errors.New("anthropic: subscription token endpoint rejected the request")
)

// OAuthTokens is a renewable Claude subscription grant.
//
// AuthorizedAt anchors GrantLifetime and is preserved across refreshes.
// Organization identity is captured once at login and deliberately never
// rewritten by a refresh: re-keying a stored credential during background
// renewal would silently move it between subscription workspaces.
type OAuthTokens struct {
	AccessToken  string
	RefreshToken string
	ExpiresAt    time.Time
	AuthorizedAt time.Time
	AccountID    string
	Email        string
	OrgID        string
	OrgName      string
}

var (
	_ fmt.Formatter  = OAuthTokens{}
	_ slog.LogValuer = OAuthTokens{}
)

// Format keeps every fmt rendering secret-safe, including when nested inside
// another formatted struct. The account email is identity-shaped, so it is
// withheld alongside the tokens.
func (t OAuthTokens) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, t.redacted())
}

// LogValue gives structured handlers the same redacted representation as fmt.
func (t OAuthTokens) LogValue() slog.Value { return slog.StringValue(t.redacted()) }

func (t OAuthTokens) redacted() string {
	switch {
	case t.AccessToken == "":
		return "anthropic subscription grant (absent)"
	case t.RefreshToken == "":
		return "anthropic subscription grant (access only)"
	default:
		return "anthropic subscription grant (renewable)"
	}
}

// Expired reports whether the access token is unusable at the given instant.
func (t OAuthTokens) Expired(now time.Time) bool {
	return t.AccessToken == "" || (!t.ExpiresAt.IsZero() && !now.Before(t.ExpiresAt))
}

// GrantExpiresAt reports when the whole grant family dies, or the zero time
// when the login instant is unknown.
func (t OAuthTokens) GrantExpiresAt() time.Time {
	if t.AuthorizedAt.IsZero() {
		return time.Time{}
	}
	return t.AuthorizedAt.Add(GrantLifetime)
}

// LoginOptions configures an interactive subscription login.
type LoginOptions struct {
	NoBrowser  bool
	URLWriter  io.Writer
	Launcher   oauthlogin.BrowserLauncher
	HTTPClient *http.Client
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
		RedirectURL: oauthlogin.AnthropicRedirectURL,
	})
	if err != nil {
		return OAuthTokens{}, err
	}
	client := opts.HTTPClient
	if client == nil {
		client = newHTTPClient()
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
		result, err := present(ctx, authorizeURL(endpoints, state, redirectURL, challenge))
		if err != nil {
			return err
		}
		tokens, err = exchangeCode(ctx, client, endpoints, result, verifier, redirectURL)
		return err
	})
	if authorizeErr != nil {
		return OAuthTokens{}, authorizeErr
	}
	return tokens, nil
}

// Refresh exchanges a refresh token for a new grant. Organization identity and
// the authorization instant are carried from the previous grant: the provider
// omits them on refresh, and rewriting them would re-key the credential.
func Refresh(ctx context.Context, client *http.Client, previous OAuthTokens) (OAuthTokens, error) {
	return refresh(ctx, client, previous, defaultEndpoints)
}

func refresh(ctx context.Context, client *http.Client, previous OAuthTokens, endpoints endpointSet) (OAuthTokens, error) {
	if previous.RefreshToken == "" {
		return OAuthTokens{}, ErrOAuthToken
	}
	if client == nil {
		client = newHTTPClient()
	}
	// The provider expects these on refresh but not on the initial exchange.
	headers := map[string]string{
		"anthropic-beta": oauthBeta,
		"User-Agent":     refreshUserAgent,
	}
	payload, err := postTokenJSON(ctx, client, endpoints, map[string]string{
		"grant_type":    "refresh_token",
		"client_id":     oauthClientID,
		"refresh_token": previous.RefreshToken,
	}, headers)
	if err != nil {
		return OAuthTokens{}, err
	}

	next := OAuthTokens{
		AccessToken:  payload.AccessToken,
		RefreshToken: payload.RefreshToken,
		ExpiresAt:    expiryFrom(payload.ExpiresIn),
		AuthorizedAt: previous.AuthorizedAt,
		AccountID:    firstNonEmpty(payload.accountID(), previous.AccountID),
		Email:        firstNonEmpty(payload.email(), previous.Email),
		OrgID:        previous.OrgID,
		OrgName:      previous.OrgName,
	}
	if next.RefreshToken == "" {
		next.RefreshToken = previous.RefreshToken
	}
	return next, nil
}

// authorizeURL builds the authorization request.
//
// Parameter ORDER is reproduced from the first-party client, not left to Go's
// map iteration: url.Values.Encode sorts keys alphabetically, and claude.ai
// answered the sorted form with "Authorization failed / Invalid request
// format". The endpoint is bespoke rather than a generic OAuth server, so the
// query is assembled in the client's exact sequence.
func authorizeURL(endpoints endpointSet, state, redirectURL, challenge string) string {
	ordered := [][2]string{
		// The provider requires this marker on the authorization request.
		{"code", "true"},
		{"client_id", oauthClientID},
		{"response_type", "code"},
		{"redirect_uri", redirectURL},
		{"scope", oauthScope},
		{"code_challenge", challenge},
		{"code_challenge_method", "S256"},
		{"state", state},
	}
	var query strings.Builder
	for i, pair := range ordered {
		if i > 0 {
			query.WriteByte('&')
		}
		query.WriteString(url.QueryEscape(pair[0]))
		query.WriteByte('=')
		query.WriteString(url.QueryEscape(pair[1]))
	}
	return endpoints.authorize + "?" + query.String()
}

// exchangeCode swaps the authorization code for a grant. A pasted code may
// carry its state after a '#', which the loopback callback never produces but
// a manual paste does.
func exchangeCode(ctx context.Context, client *http.Client, endpoints endpointSet, result oauthlogin.Result, verifier, redirectURL string) (OAuthTokens, error) {
	code, state := result.Code, result.State
	if trimmed, fragment, found := strings.Cut(code, "#"); found {
		code = trimmed
		if fragment != "" {
			state = fragment
		}
	}
	if code == "" || verifier == "" {
		return OAuthTokens{}, ErrOAuthToken
	}

	payload, err := postTokenJSON(ctx, client, endpoints, map[string]string{
		"grant_type":    "authorization_code",
		"client_id":     oauthClientID,
		"code":          code,
		"state":         state,
		"redirect_uri":  redirectURL,
		"code_verifier": verifier,
	}, nil)
	if err != nil {
		return OAuthTokens{}, err
	}

	now := time.Now().UTC()
	tokens := OAuthTokens{
		AccessToken:  payload.AccessToken,
		RefreshToken: payload.RefreshToken,
		ExpiresAt:    expiryFrom(payload.ExpiresIn),
		AuthorizedAt: now,
		AccountID:    payload.accountID(),
		Email:        payload.email(),
		OrgID:        payload.orgID(),
		OrgName:      payload.orgName(),
	}
	// Older responses omit the inline account block; the first-party client's
	// bootstrap endpoint recovers the same identity. Identity is metadata, so
	// a bootstrap failure must not fail an otherwise good login.
	if tokens.AccountID == "" || tokens.Email == "" || tokens.OrgID == "" {
		if identity, err := fetchBootstrapIdentity(ctx, client, endpoints, tokens.AccessToken); err == nil {
			tokens.AccountID = firstNonEmpty(tokens.AccountID, identity.AccountID)
			tokens.Email = firstNonEmpty(tokens.Email, identity.Email)
			tokens.OrgID = firstNonEmpty(tokens.OrgID, identity.OrgID)
			tokens.OrgName = firstNonEmpty(tokens.OrgName, identity.OrgName)
		}
	}
	return tokens, nil
}

type tokenPayload struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
	Account      struct {
		UUID  string `json:"uuid"`
		Email string `json:"email_address"`
	} `json:"account"`
	Organization struct {
		UUID string `json:"uuid"`
		Name string `json:"name"`
	} `json:"organization"`
}

func (p tokenPayload) accountID() string { return strings.TrimSpace(p.Account.UUID) }
func (p tokenPayload) email() string     { return strings.TrimSpace(p.Account.Email) }
func (p tokenPayload) orgID() string     { return strings.TrimSpace(p.Organization.UUID) }
func (p tokenPayload) orgName() string   { return strings.TrimSpace(p.Organization.Name) }

// postTokenJSON posts a JSON token request. The provider's own client sends no
// Accept header on these, so none is set.
func postTokenJSON(ctx context.Context, client *http.Client, endpoints endpointSet, body map[string]string, headers map[string]string) (tokenPayload, error) {
	encoded, err := json.Marshal(body)
	if err != nil {
		return tokenPayload{}, ErrOAuthToken
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoints.token, strings.NewReader(string(encoded)))
	if err != nil {
		return tokenPayload{}, ErrOAuthToken
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return tokenPayload{}, ctxErr
		}
		return tokenPayload{}, ErrOAuthToken
	}
	defer drainAndClose(resp)

	// The response body may quote the submitted grant, so only the status
	// class distinguishes the failure.
	if resp.StatusCode != http.StatusOK {
		return tokenPayload{}, ErrOAuthToken
	}
	var payload tokenPayload
	if err := json.NewDecoder(io.LimitReader(resp.Body, oauthMaxResponseBytes)).Decode(&payload); err != nil {
		return tokenPayload{}, ErrOAuthToken
	}
	if payload.AccessToken == "" || payload.ExpiresIn <= 0 {
		return tokenPayload{}, ErrOAuthToken
	}
	return payload, nil
}

// Identity is the account and organization slice carried by a grant.
type Identity struct {
	AccountID string
	Email     string
	OrgID     string
	OrgName   string
}

func fetchBootstrapIdentity(ctx context.Context, client *http.Client, endpoints endpointSet, accessToken string) (Identity, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoints.bootstrap+"?entrypoint=cli&model="+url.QueryEscape(bootstrapModel), nil)
	if err != nil {
		return Identity{}, ErrOAuthToken
	}
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", bootstrapUserAgent)
	req.Header.Set("anthropic-beta", oauthBeta)

	resp, err := client.Do(req)
	if err != nil {
		return Identity{}, ErrOAuthToken
	}
	defer drainAndClose(resp)
	if resp.StatusCode != http.StatusOK {
		return Identity{}, ErrOAuthToken
	}
	var body struct {
		OAuthAccount struct {
			AccountUUID string `json:"account_uuid"`
			AccountMail string `json:"account_email"`
			OrgUUID     string `json:"organization_uuid"`
			OrgName     string `json:"organization_name"`
		} `json:"oauth_account"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, oauthMaxResponseBytes)).Decode(&body); err != nil {
		return Identity{}, ErrOAuthToken
	}
	return Identity{
		AccountID: strings.TrimSpace(body.OAuthAccount.AccountUUID),
		Email:     strings.TrimSpace(body.OAuthAccount.AccountMail),
		OrgID:     strings.TrimSpace(body.OAuthAccount.OrgUUID),
		OrgName:   strings.TrimSpace(body.OAuthAccount.OrgName),
	}, nil
}

func drainAndClose(resp *http.Response) {
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, oauthMaxResponseBytes))
	_ = resp.Body.Close()
}

func expiryFrom(expiresIn int64) time.Time {
	return time.Now().UTC().Add(time.Duration(expiresIn)*time.Second - expiryMargin)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

// newHTTPClient refuses redirects: a redirect would re-send the grant to an
// unvetted host.
func newHTTPClient() *http.Client {
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
		return "", "", errors.New("anthropic: generate PKCE verifier: failed")
	}
	verifier = base64.RawURLEncoding.EncodeToString(raw)
	sum := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(sum[:]), nil
}

// newState mints the CSRF nonce as 32 lowercase hex characters. The reference
// client renders 16 random bytes as hex; base64url would introduce "-" and "_"
// into a value this endpoint has not been observed to accept.
func newState() (string, error) {
	raw := make([]byte, stateBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", errors.New("anthropic: generate authorization state: failed")
	}
	return hex.EncodeToString(raw), nil
}
