package identityharness_test

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/golang-jwt/jwt/v5"

	"github.com/stacklok/mecatl/internal/testutil/identityharness"
)

// The IdP's test client, registered as a real AS-1 registers itself.
const (
	idpClientID     = "as1-test"
	idpClientSecret = "as1-test-secret" // #nosec G101 -- test value, not a credential.
	idpRedirectURI  = "https://as1.test/oauth/callback"
)

// newIdPHarness starts a harness whose IdP knows the test client.
func newIdPHarness(t *testing.T) *identityharness.Harness {
	t.Helper()
	h := identityharness.New(t, identityharness.Options{})
	h.IdP.RegisterClient(idpClientID, idpClientSecret, idpRedirectURI)
	return h
}

// idpAuthorize sends an authorization request for the openid scope, with
// extra parameters, and returns the query the IdP redirected back with.
func idpAuthorize(t *testing.T, h *identityharness.Harness, extra url.Values) url.Values {
	t.Helper()
	q := url.Values{"response_type": {"code"}, "client_id": {idpClientID}, "redirect_uri": {idpRedirectURI}, "scope": {"openid"}, "state": {"st"}}
	for k, v := range extra {
		q[k] = v
	}
	resp, err := h.HTTPClient().Get(h.IdP.Issuer() + "/authorize?" + q.Encode())
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	location, err := resp.Location()
	if resp.StatusCode != http.StatusFound || err != nil || !strings.HasPrefix(location.String(), idpRedirectURI+"?") {
		t.Fatalf("authorize: status %d, location %v; want a redirect to the client", resp.StatusCode, location)
	}
	return location.Query()
}

// idpToken redeems code and returns the status and decoded body.
func idpToken(t *testing.T, h *identityharness.Harness, code string) (int, map[string]any) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, h.IdP.Issuer()+"/token", strings.NewReader(url.Values{
		"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {idpRedirectURI},
	}.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth(idpClientID, idpClientSecret)
	resp, err := h.HTTPClient().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var body map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&body)
	return resp.StatusCode, body
}

// idpSignIn signs a user in with extra authorization parameters and returns
// the token response, failing t unless the IdP issues tokens.
func idpSignIn(t *testing.T, h *identityharness.Harness, extra url.Values) map[string]any {
	t.Helper()
	back := idpAuthorize(t, h, extra)
	if back.Get("code") == "" {
		t.Fatalf("authorize redirected with error %q, want a code", back.Get("error"))
	}
	status, body := idpToken(t, h, back.Get("code"))
	if status != http.StatusOK {
		t.Fatalf("token: status %d, body %v", status, body)
	}
	return body
}

// signedInUser is the sub of a token response's ID token, read without
// verifying it.
func signedInUser(t *testing.T, body map[string]any) string {
	t.Helper()
	idToken, _ := body["id_token"].(string)
	claims := jwt.MapClaims{}
	if _, _, err := jwt.NewParser().ParseUnverified(idToken, claims); err != nil {
		t.Fatalf("parse ID token: %v", err)
	}
	sub, _ := claims.GetSubject()
	return sub
}

// TestSignInNextAsSelectsTheNextSignInOnly: the IdP signs in the selected
// user once, over login_hint, and then falls back to login_hint or the
// default user; an unknown selection or login_hint is refused.
func TestSignInNextAsSelectsTheNextSignInOnly(t *testing.T) {
	h := newIdPHarness(t)
	hint := func(user string) url.Values { return url.Values{"login_hint": {user}} }

	h.IdP.SignInNextAs(identityharness.Bob)
	if got := signedInUser(t, idpSignIn(t, h, nil)); got != identityharness.Bob {
		t.Fatalf("selected sign-in signed in %q, want %q", got, identityharness.Bob)
	}
	if got := signedInUser(t, idpSignIn(t, h, nil)); got != identityharness.Alice {
		t.Fatalf("the next sign-in signed in %q, want the default user %q", got, identityharness.Alice)
	}
	h.IdP.SignInNextAs(identityharness.Bob)
	if got := signedInUser(t, idpSignIn(t, h, hint(identityharness.Alice))); got != identityharness.Bob {
		t.Fatalf("selected sign-in with login_hint signed in %q, want %q", got, identityharness.Bob)
	}
	if got := signedInUser(t, idpSignIn(t, h, hint(identityharness.Bob))); got != identityharness.Bob {
		t.Fatalf("login_hint sign-in signed in %q, want %q", got, identityharness.Bob)
	}

	h.IdP.SignInNextAs("mallory-404")
	if back := idpAuthorize(t, h, nil); back.Get("error") != "access_denied" {
		t.Fatalf("sign-in as an unknown selected user = %v, want access_denied", back)
	}
	if back := idpAuthorize(t, h, hint("mallory-404")); back.Get("error") != "access_denied" {
		t.Fatalf("sign-in as an unknown hinted user = %v, want access_denied", back)
	}
}

// TestIdPIssuesARefreshTokenForOfflineConsentOnly: OIDC Core §11 offline
// access needs the offline_access scope and prompt=consent together.
func TestIdPIssuesARefreshTokenForOfflineConsentOnly(t *testing.T) {
	h := newIdPHarness(t)
	for name, extra := range map[string]url.Values{
		"openid only":               nil,
		"offline_access, no prompt": {"scope": {"openid offline_access"}},
		"prompt=consent, no scope":  {"prompt": {"consent"}},
	} {
		if _, issued := idpSignIn(t, h, extra)["refresh_token"]; issued {
			t.Errorf("%s: the IdP issued a refresh token", name)
		}
	}
	if got := h.IdP.RefreshTokensIssued(identityharness.Alice); got != 0 {
		t.Fatalf("refresh tokens issued = %d before offline consent, want 0", got)
	}
	body := idpSignIn(t, h, url.Values{"scope": {"openid offline_access"}, "prompt": {"consent"}})
	if body["refresh_token"] == nil || h.IdP.RefreshTokensIssued(identityharness.Alice) != 1 {
		t.Fatalf("offline consent: refresh token %v, %d counted; want one", body["refresh_token"] != nil, h.IdP.RefreshTokensIssued(identityharness.Alice))
	}
}

// TestIdPRefusesAnUnknownClientWithoutRedirect never redirects to a client
// or redirect URI it does not know (RFC 6749 §4.1.2.1).
func TestIdPRefusesAnUnknownClientWithoutRedirect(t *testing.T) {
	h := newIdPHarness(t)
	for name, q := range map[string]url.Values{
		"unknown client":       {"client_id": {"unknown"}, "redirect_uri": {idpRedirectURI}},
		"unknown redirect URI": {"client_id": {idpClientID}, "redirect_uri": {"https://as1.test/elsewhere"}},
	} {
		q.Set("response_type", "code")
		q.Set("scope", "openid")
		resp, err := h.HTTPClient().Get(h.IdP.Issuer() + "/authorize?" + q.Encode())
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest || resp.Header.Get("Location") != "" {
			t.Errorf("%s: status %d, location %q; want 400 without a redirect", name, resp.StatusCode, resp.Header.Get("Location"))
		}
	}
}
