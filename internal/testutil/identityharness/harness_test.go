package identityharness_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/stacklok/mecatl/internal/testutil/identityharness"
)

// TestSignInIssuesDesignAccessToken checks the AS-1 access token against
// design step (1), verifying it with the keys AS-1 publishes through both
// discovery locations.
func TestSignInIssuesDesignAccessToken(t *testing.T) {
	h := identityharness.New(t, identityharness.Options{})
	tokens, err := h.SignIn(t.Context(), identityharness.SignInRequest{User: identityharness.Alice})
	if err != nil {
		t.Fatalf("SignIn: %v", err)
	}
	if tokens.TokenType != "Bearer" || tokens.RefreshToken == "" || tokens.ExpiresIn != int(identityharness.DefaultAccessTokenLifetime.Seconds()) {
		t.Fatalf("tokens = %+v, want Bearer with a refresh token and the default lifetime", tokens)
	}

	header, claims := verify(t, h, tokens.AccessToken)
	if header["typ"] != "at+jwt" || header["alg"] != "ES256" || header["kid"] == "" {
		t.Errorf("header = %v, want typ at+jwt, alg ES256, and a kid", header)
	}
	want := map[string]any{
		"iss":       h.AS1.Issuer(),
		"sub":       h.AS1.Subject(identityharness.Alice),
		"aud":       identityharness.DefaultAudience,
		"client_id": identityharness.DefaultClientID,
		"sub_id":    map[string]any{"format": "iss_sub", "iss": h.IdP.Issuer(), "sub": identityharness.Alice},
	}
	for name, value := range want {
		if got, _ := json.Marshal(claims[name]); string(got) != mustJSON(t, value) {
			t.Errorf("claim %s = %s, want %s", name, got, mustJSON(t, value))
		}
	}
	if claims["jti"] == "" || claims["jti"] == nil {
		t.Error("access token has no jti")
	}
	if life := claims["exp"].(float64) - claims["iat"].(float64); life != identityharness.DefaultAccessTokenLifetime.Seconds() {
		t.Errorf("exp - iat = %v, want %v", life, identityharness.DefaultAccessTokenLifetime.Seconds())
	}
	if !strings.HasSuffix(h.AS1.Issuer(), "/as") {
		t.Errorf("issuer %q has no path; AS-1's final issuer URL has one", h.AS1.Issuer())
	}
}

func TestSubjectsAreStableAndDistinct(t *testing.T) {
	h := identityharness.New(t, identityharness.Options{})
	subjects := map[string]string{}
	for _, user := range []string{identityharness.Alice, identityharness.Bob, identityharness.Alice} {
		tokens, err := h.SignIn(t.Context(), identityharness.SignInRequest{User: user})
		if err != nil {
			t.Fatalf("SignIn(%s): %v", user, err)
		}
		_, claims := verify(t, h, tokens.AccessToken)
		sub := claims["sub"].(string)
		if prev, ok := subjects[user]; ok && prev != sub {
			t.Errorf("%s signed in as %q, then %q", user, prev, sub)
		}
		subjects[user] = sub
	}
	if subjects[identityharness.Alice] == subjects[identityharness.Bob] {
		t.Errorf("Alice and Bob share AS-1 subject %q", subjects[identityharness.Alice])
	}
}

// TestSignInAsksUpstreamForOfflineAccess: AS-1 needs an upstream refresh
// token for its later re-check, and the IdP issues one only for
// offline_access with prompt=consent.
func TestSignInAsksUpstreamForOfflineAccess(t *testing.T) {
	h := identityharness.New(t, identityharness.Options{})
	if _, err := h.SignIn(t.Context(), identityharness.SignInRequest{}); err != nil {
		t.Fatalf("SignIn: %v", err)
	}
	if got := h.IdP.RefreshTokensIssued(identityharness.Alice); got != 1 {
		t.Errorf("IdP refresh tokens for Alice = %d, want 1", got)
	}
	if !h.AS1.HasUpstreamRefreshToken(identityharness.Alice) {
		t.Error("AS-1 holds no upstream refresh token for Alice")
	}
}

func TestRefreshRenewsForTheSameUserAndClient(t *testing.T) {
	h := identityharness.New(t, identityharness.Options{Clients: []identityharness.Client{
		{ID: "mecatui", RedirectURIs: []string{identityharness.DefaultRedirectURI}},
		{ID: "studio", RedirectURIs: []string{"https://studio.test/callback"}},
	}})
	tokens, err := h.SignIn(t.Context(), identityharness.SignInRequest{User: identityharness.Bob})
	if err != nil {
		t.Fatalf("SignIn: %v", err)
	}
	renewed, err := h.Refresh(t.Context(), "mecatui", tokens.RefreshToken)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	_, claims := verify(t, h, renewed.AccessToken)
	if claims["sub"] != h.AS1.Subject(identityharness.Bob) || claims["client_id"] != "mecatui" {
		t.Errorf("renewed claims = %v, want Bob for mecatui", claims)
	}
	_, err = h.Refresh(t.Context(), "studio", tokens.RefreshToken)
	assertOAuthError(t, err, "invalid_grant")
	_, err = h.Refresh(t.Context(), "mecatui", "not-a-refresh-token")
	assertOAuthError(t, err, "invalid_grant")
}

func TestSignInFailures(t *testing.T) {
	for _, c := range []struct {
		name  string
		force func(*identityharness.Harness)
		user  string
		want  string
	}{
		{"IdP denies", func(h *identityharness.Harness) {
			h.IdP.FailNextAuthorization(identityharness.Fault{Error: "access_denied"})
		}, "", "access_denied"},
		{"IdP fails otherwise", func(h *identityharness.Harness) {
			h.IdP.FailNextAuthorization(identityharness.Fault{Error: "temporarily_unavailable"})
		}, "", "server_error"},
		{"IdP token endpoint fails", func(h *identityharness.Harness) {
			h.IdP.FailNextToken(identityharness.Fault{Status: http.StatusServiceUnavailable, Error: "temporarily_unavailable"})
		}, "", "server_error"},
		{"unknown company user", func(*identityharness.Harness) {}, "mallory", "access_denied"},
		{"AS-1 refuses authorization", func(h *identityharness.Harness) {
			h.AS1.FailNextAuthorization(identityharness.Fault{Error: "access_denied"})
		}, "", "access_denied"},
		{"AS-1 token endpoint refuses", func(h *identityharness.Harness) {
			h.AS1.FailNextToken(identityharness.Fault{Error: "invalid_grant", Reason: "token_used"})
		}, "", "invalid_grant"},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := identityharness.New(t, identityharness.Options{})
			c.force(h)
			_, err := h.SignIn(t.Context(), identityharness.SignInRequest{User: c.user})
			assertOAuthError(t, err, c.want)
			// Each fault fires once.
			if _, err := h.SignIn(t.Context(), identityharness.SignInRequest{}); err != nil {
				t.Fatalf("SignIn after the fault: %v", err)
			}
		})
	}
}

func TestTokenEndpointReasonStartsDescription(t *testing.T) {
	h := identityharness.New(t, identityharness.Options{})
	h.AS1.FailNextToken(identityharness.Fault{Error: "invalid_request", Reason: "token_used"})
	_, err := h.SignIn(t.Context(), identityharness.SignInRequest{})
	var oauthErr *identityharness.OAuthError
	if !errors.As(err, &oauthErr) || oauthErr.Code != "invalid_request" || oauthErr.Description != "token_used" {
		t.Fatalf("SignIn = %v, want invalid_request with description token_used", err)
	}
}

func TestAuthorizationRefusesUnknownClientWithoutRedirect(t *testing.T) {
	h := identityharness.New(t, identityharness.Options{})
	for _, req := range []identityharness.SignInRequest{
		{ClientID: "unknown"},
		{RedirectURI: "http://127.0.0.1:9999/elsewhere"},
	} {
		_, err := h.SignIn(t.Context(), req)
		var oauthErr *identityharness.OAuthError
		if err == nil || errors.As(err, &oauthErr) {
			t.Errorf("SignIn(%+v) = %v, want a refusal that does not redirect", req, err)
		}
	}
}

// TestCodeGrantChecks drives the authorization request by hand to present
// defective code grants.
func TestCodeGrantChecks(t *testing.T) {
	h := identityharness.New(t, identityharness.Options{})
	const verifier = "verifier-0123456789-0123456789-0123456789-0123456789"

	back := authorize(t, h, url.Values{"code_challenge": {challenge(verifier)}, "code_challenge_method": {"S256"}})
	code := back.Get("code")
	if code == "" || back.Get("state") != "s1" {
		t.Fatalf("authorization redirect = %v, want a code and state s1", back)
	}
	exchange := func(code, verifier, redirect string) error {
		_, err := postToken(t, h, url.Values{
			"grant_type": {"authorization_code"}, "client_id": {identityharness.DefaultClientID},
			"code": {code}, "code_verifier": {verifier}, "redirect_uri": {redirect},
		})
		return err
	}
	assertOAuthError(t, exchange(code, "wrong-verifier-0123456789-0123456789-0123456789", identityharness.DefaultRedirectURI), "invalid_grant")
	// A failed redemption consumes the code.
	assertOAuthError(t, exchange(code, verifier, identityharness.DefaultRedirectURI), "invalid_grant")

	code = authorize(t, h, url.Values{"code_challenge": {challenge(verifier)}, "code_challenge_method": {"S256"}}).Get("code")
	assertOAuthError(t, exchange(code, verifier, "http://127.0.0.1:8765/other"), "invalid_grant")

	code = authorize(t, h, url.Values{"code_challenge": {challenge(verifier)}, "code_challenge_method": {"S256"}}).Get("code")
	if err := exchange(code, verifier, identityharness.DefaultRedirectURI); err != nil {
		t.Fatalf("valid exchange: %v", err)
	}
	assertOAuthError(t, exchange(code, verifier, identityharness.DefaultRedirectURI), "invalid_grant")

	for _, pkce := range []url.Values{{}, {"code_challenge": {challenge(verifier)}, "code_challenge_method": {"plain"}}} {
		if got := authorize(t, h, pkce).Get("error"); got != "invalid_request" {
			t.Errorf("authorization with PKCE %v: error %q, want invalid_request", pkce, got)
		}
	}

	_, err := postToken(t, h, url.Values{"grant_type": {"refresh_token"}, "client_id": {"unknown"}})
	assertOAuthError(t, err, "invalid_client")
	_, err = postToken(t, h, url.Values{"grant_type": {"password"}, "client_id": {identityharness.DefaultClientID}})
	assertOAuthError(t, err, "unsupported_grant_type")
}

func TestMintAccessTokenDefects(t *testing.T) {
	h := identityharness.New(t, identityharness.Options{})
	_, claims := verify(t, h, h.AS1.MintAccessToken(identityharness.AccessTokenSpec{OmitSubID: true}))
	if _, ok := claims["sub_id"]; ok {
		t.Error("OmitSubID token carries sub_id")
	}
	header, claims := verify(t, h, h.AS1.MintAccessToken(identityharness.AccessTokenSpec{
		User: identityharness.Bob, Audience: "https://other.test", Type: "application/at+jwt",
	}))
	if claims["aud"] != "https://other.test" || header["typ"] != "application/at+jwt" || claims["sub"] != h.AS1.Subject(identityharness.Bob) {
		t.Errorf("minted token = %v %v, want Bob, other audience, application/at+jwt", header, claims)
	}
	header, _ = verify(t, h, h.AS1.MintAccessToken(identityharness.AccessTokenSpec{OmitType: true}))
	if _, ok := header["typ"]; ok {
		t.Error("OmitType token carries typ")
	}
	expired := h.AS1.MintAccessToken(identityharness.AccessTokenSpec{Lifetime: -time.Minute})
	if _, err := parse(t, h, expired); !errors.Is(err, jwt.ErrTokenExpired) {
		t.Errorf("expired token parse = %v, want ErrTokenExpired", err)
	}
	foreign := h.AS1.MintAccessToken(identityharness.AccessTokenSpec{ForeignKey: true})
	if _, err := parse(t, h, foreign); !errors.Is(err, jwt.ErrTokenSignatureInvalid) {
		t.Errorf("foreign-key token parse = %v, want ErrTokenSignatureInvalid", err)
	}
}

// authorize sends an authorization request to AS-1 with extra parameters and
// follows redirects back to the default redirect URI.
func authorize(t *testing.T, h *identityharness.Harness, extra url.Values) url.Values {
	t.Helper()
	q := url.Values{
		"response_type": {"code"}, "client_id": {identityharness.DefaultClientID},
		"redirect_uri": {identityharness.DefaultRedirectURI}, "state": {"s1"},
	}
	for k, v := range extra {
		q[k] = v
	}
	next := h.AS1.AuthorizationEndpoint() + "?" + q.Encode()
	for range 10 {
		resp, err := h.HTTPClient().Get(next)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		loc, err := resp.Location()
		if err != nil {
			t.Fatalf("GET %s: status %d, no redirect", next, resp.StatusCode)
		}
		if next = loc.String(); strings.HasPrefix(next, identityharness.DefaultRedirectURI+"?") {
			return loc.Query()
		}
	}
	t.Fatal("too many redirects")
	return nil
}

func postToken(t *testing.T, h *identityharness.Harness, form url.Values) (map[string]any, error) {
	t.Helper()
	resp, err := h.HTTPClient().PostForm(h.AS1.TokenEndpoint(), form)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.Header.Get("Cache-Control") != "no-store" {
		t.Errorf("token response Cache-Control = %q, want no-store", resp.Header.Get("Cache-Control"))
	}
	if resp.StatusCode != http.StatusOK {
		oauthErr := &identityharness.OAuthError{}
		if err := json.NewDecoder(resp.Body).Decode(oauthErr); err != nil {
			t.Fatal(err)
		}
		return nil, oauthErr
	}
	var body map[string]any
	return body, json.NewDecoder(resp.Body).Decode(&body)
}

// challenge is the PKCE S256 challenge, computed independently of the
// harness's own PKCE code.
func challenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func verify(t *testing.T, h *identityharness.Harness, token string) (map[string]any, jwt.MapClaims) {
	t.Helper()
	parsed, err := parse(t, h, token)
	if err != nil {
		t.Fatalf("verify access token: %v", err)
	}
	return parsed.Header, parsed.Claims.(jwt.MapClaims)
}

// parse verifies token against the JWKS AS-1 publishes, found through both
// discovery locations.
func parse(t *testing.T, h *identityharness.Harness, token string) (*jwt.Token, error) {
	t.Helper()
	issuer, _ := url.Parse(h.AS1.Issuer())
	var jwksURI string
	for _, path := range []string{
		h.AS1.Issuer() + "/.well-known/openid-configuration",
		issuer.Scheme + "://" + issuer.Host + "/.well-known/oauth-authorization-server" + issuer.Path,
	} {
		var doc struct {
			Issuer  string `json:"issuer"`
			JWKSURI string `json:"jwks_uri"`
		}
		getJSON(t, h, path, &doc)
		if doc.Issuer != h.AS1.Issuer() || doc.JWKSURI == "" {
			t.Fatalf("discovery %s = %+v, want issuer %s and a jwks_uri", path, doc, h.AS1.Issuer())
		}
		jwksURI = doc.JWKSURI
	}
	var set struct {
		Keys []struct{ Kty, Crv, Kid, X, Y string } `json:"keys"`
	}
	getJSON(t, h, jwksURI, &set)
	return jwt.Parse(token, func(tok *jwt.Token) (any, error) {
		for _, k := range set.Keys {
			if k.Kid != tok.Header["kid"] || k.Kty != "EC" || k.Crv != "P-256" {
				continue
			}
			x, _ := base64.RawURLEncoding.DecodeString(k.X)
			y, _ := base64.RawURLEncoding.DecodeString(k.Y)
			return ecdsa.ParseUncompressedPublicKey(elliptic.P256(), append(append([]byte{4}, x...), y...))
		}
		return nil, errors.New("no matching key")
	}, jwt.WithValidMethods([]string{"ES256"}), jwt.WithIssuer(h.AS1.Issuer()), jwt.WithExpirationRequired())
}

func getJSON(t *testing.T, h *identityharness.Harness, target string, out any) {
	t.Helper()
	resp, err := h.HTTPClient().Get(target)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: status %d", target, resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		t.Fatal(err)
	}
}

func assertOAuthError(t *testing.T, err error, code string) {
	t.Helper()
	var oauthErr *identityharness.OAuthError
	if !errors.As(err, &oauthErr) || oauthErr.Code != code {
		t.Fatalf("error = %v, want OAuth error %s", err, code)
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
