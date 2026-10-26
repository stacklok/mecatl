package identityharness

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// SignInRequest selects who signs in, with which client.
type SignInRequest struct {
	// User is the company identity to sign in. Empty selects the IdP's
	// default user.
	User string
	// ClientID is the AS-1 client. Empty selects DefaultClientID.
	ClientID string
	// RedirectURI is the client's redirect URI. Empty selects
	// DefaultRedirectURI.
	RedirectURI string
}

// Tokens are what a client holds after signing in at AS-1.
type Tokens struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
}

// OAuthError is an OAuth error a sign-in ended with, from the authorization
// redirect or the token endpoint.
type OAuthError struct {
	Code        string `json:"error"`
	Description string `json:"error_description"`
	// Status is the HTTP status of a token or revocation endpoint error.
	Status int `json:"-"`
}

func (e *OAuthError) Error() string {
	if e.Description == "" {
		return "oauth error " + e.Code
	}
	return "oauth error " + e.Code + ": " + e.Description
}

// SignIn drives the browser side of a sign-in, as the TUI or Studio would:
// authorization code with PKCE at AS-1, through the upstream IdP, then the
// code exchange at AS-1's token endpoint. A refused sign-in returns an
// *OAuthError.
func (h *Harness) SignIn(ctx context.Context, req SignInRequest) (Tokens, error) {
	clientID := firstNonEmpty(req.ClientID, DefaultClientID)
	redirectURI := firstNonEmpty(req.RedirectURI, DefaultRedirectURI)
	verifier := randomToken("verifier-") + randomToken("")
	state := randomToken("state-")
	q := url.Values{
		"response_type":         {responseTypeCode},
		paramClientID:           {clientID},
		"redirect_uri":          {redirectURI},
		"state":                 {state},
		"code_challenge":        {s256(verifier)},
		"code_challenge_method": {pkceS256},
	}
	if req.User != "" {
		q.Set("login_hint", req.User)
	}
	back, err := h.followToRedirect(ctx, withQuery(h.AS1.AuthorizationEndpoint(), q), redirectURI)
	if err != nil {
		return Tokens{}, err
	}
	if code := back.Get("error"); code != "" {
		return Tokens{}, &OAuthError{Code: code, Description: back.Get("error_description")}
	}
	if back.Get("state") != state {
		return Tokens{}, fmt.Errorf("identityharness: sign-in state mismatch")
	}
	return h.exchange(ctx, url.Values{
		"grant_type":    {grantAuthorizationCode},
		paramClientID:   {clientID},
		"code":          {back.Get("code")},
		"redirect_uri":  {redirectURI},
		"code_verifier": {verifier},
	})
}

// Refresh renews an access token with a client refresh token.
func (h *Harness) Refresh(ctx context.Context, clientID, refreshToken string) (Tokens, error) {
	return h.exchange(ctx, url.Values{
		"grant_type":    {grantRefreshToken},
		paramClientID:   {firstNonEmpty(clientID, DefaultClientID)},
		"refresh_token": {refreshToken},
	})
}

// followToRedirect follows redirects from start until one targets
// redirectURI, and returns that redirect's query.
func (h *Harness) followToRedirect(ctx context.Context, start, redirectURI string) (url.Values, error) {
	client := h.HTTPClient()
	next := start
	for range 10 {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, next, nil)
		if err != nil {
			return nil, err
		}
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusFound {
			return nil, fmt.Errorf("identityharness: GET %s: status %d", req.URL.Path, resp.StatusCode)
		}
		location, err := resp.Location()
		if err != nil {
			return nil, err
		}
		next = location.String()
		if strings.HasPrefix(next, redirectURI+"?") {
			return location.Query(), nil
		}
	}
	return nil, fmt.Errorf("identityharness: too many redirects")
}

func (h *Harness) exchange(ctx context.Context, form url.Values) (Tokens, error) {
	var tokens Tokens
	err := h.postForm(ctx, h.AS1.TokenEndpoint(), form, &tokens)
	return tokens, err
}

// postForm posts form to an AS-1 endpoint and decodes a 200 response into
// out, when out is non-nil. An OAuth error response returns an *OAuthError.
func (h *Harness) postForm(ctx context.Context, endpoint string, form url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := h.HTTPClient().Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		oauthErr := &OAuthError{Status: resp.StatusCode}
		if err := json.NewDecoder(resp.Body).Decode(oauthErr); err != nil || oauthErr.Code == "" {
			return fmt.Errorf("identityharness: %s: status %d", req.URL.Path, resp.StatusCode)
		}
		return oauthErr
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
