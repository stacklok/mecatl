// Package as2signin signs users in at the MCP broker's embedded AS-2 for
// tests, as a browser would: through the company IdP, AS-2's first upstream,
// where AS-2 records the user that it later resolves ID-JAGs to, and then
// through each provider upstream. It is separate from mcpbrokertest so the
// mcpbroker package's own tests can use it; mcpbrokertest imports mcpbroker.
//
// It is the one ToolHive import leaf among the broker's test fixtures,
// alongside the adapter's own (TestToolHiveImportsStayBehindApprovedAdapterLeaves).
// The package is test code; production packages never import it.
package as2signin

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/toolhive/pkg/authserver/storage"
	"github.com/stacklok/toolhive/pkg/oauthproto"

	"github.com/stacklok/mecatl/internal/testutil/identityharness"
	"github.com/stacklok/mecatl/internal/testutil/oauthbrowser"
)

const (
	// ClientID is the public client SignIn signs users in through, which AS-2
	// adds to its store when its test seam names it, as
	// mcpbrokertest.SignInSeam's does.
	ClientID = "as2signin"
	// RedirectURI is ClientID's only redirect URI. SignIn never contacts it.
	RedirectURI = "https://sign-in.example/callback"
)

// Store is an in-memory store for AS-2, for ToolHiveConfig.AuthStorage,
// that tells how many users AS-2 recorded.
type Store struct{ *storage.MemoryStorage }

// NewStore returns an empty Store.
func NewStore() Store { return Store{storage.NewMemoryStorage()} }

// Users is how many users AS-2 recorded: one per user signed in at it.
func (s Store) Users() int { return s.Stats().Users }

// RegisterAtIdP registers AS-2 at issuer as a new client at h's IdP, with
// AS-2's upstream callback as its redirect URI, and returns what AS-2's
// company IdP configuration names: the client ID, its secret's file, and
// the CA certificate that verifies the IdP. Each call registers another
// client, so AS-2s sharing an issuer and an IdP keep their own secrets.
func RegisterAtIdP(t testing.TB, h *identityharness.Harness, issuer string) (clientID, secretFile, caFile string) {
	t.Helper()
	dir := t.TempDir()
	clientID = "as2-" + strings.ToLower(rand.Text())
	// The harness registers any OIDC client whose redirect URI is
	// <issuer>/oauth/callback this way, AS-2 included.
	secretFile = h.RegisterAS1Upstream(t, dir, clientID, issuer)
	return clientID, secretFile, h.WriteCACert(t, dir)
}

// Provider starts a fake OAuth provider on loopback for a protected
// upstream: it approves every authorization at once and issues an access
// token for any code. Its endpoints let SignIn complete the provider legs
// that follow the company IdP.
func Provider(t testing.TB) (authorizationEndpoint, tokenEndpoint string) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/authorize":
			q := r.URL.Query()
			back := url.Values{"code": {rand.Text()}, "state": {q.Get("state")}}
			//nolint:gosec // G710: a loopback test provider, redirecting back to the AS-2 under test as any OAuth provider does.
			http.Redirect(w, r, q.Get("redirect_uri")+"?"+back.Encode(), http.StatusFound)
		case "/token":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": rand.Text(), "token_type": "Bearer", "expires_in": 3600})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server.URL + "/authorize", server.URL + "/token"
}

// SignIn signs user in at AS-2, at issuer for resource, through ClientID,
// which AS-2's test seam registered: it plays the browser from AS-2's
// authorization endpoint through its consent page, the company IdP at h,
// and each provider upstream, until AS-2 redirects to RedirectURI with a
// code, and returns the user's access token from that code. as2 serves
// AS-2's routes at issuer's host, such as the process's mounted handlers;
// every other host, the IdP and the providers, is reached over the network,
// trusting h's certificate. It selects user at the IdP, so it must not run
// concurrently with another sign-in at h's IdP.
func SignIn(t testing.TB, h *identityharness.Harness, as2 http.Handler, issuer, resource, user string) string {
	t.Helper()
	browser, verifier, state, location := browse(t, h, as2, issuer, resource, user, func(u *url.URL) bool {
		return strings.HasPrefix(u.String(), RedirectURI+"?")
	})
	back := location.Query()
	if !strings.HasPrefix(location.String(), RedirectURI+"?") || back.Get("state") != state || back.Get("code") == "" {
		t.Fatalf("as2signin: sign %s in at AS-2: redirect to %s?error=%q&error_description=%q", user, location.Redacted(), back.Get("error"), back.Get("error_description"))
	}
	token, err := browser.PostForm(issuer+"/oauth/token", url.Values{
		"grant_type": {oauthproto.GrantTypeAuthorizationCode}, "client_id": {ClientID}, "code": {back.Get("code")},
		"redirect_uri": {RedirectURI}, "code_verifier": {verifier}, "resource": {resource},
	})
	if err != nil {
		t.Fatalf("as2signin: code exchange: %v", err)
	}
	defer func() { _ = token.Body.Close() }()
	var body struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
	}
	if err := json.NewDecoder(token.Body).Decode(&body); err != nil || token.StatusCode != http.StatusOK || body.AccessToken == "" {
		t.Fatalf("as2signin: code exchange: status %d, error %q, decode %v", token.StatusCode, body.Error, err)
	}
	return body.AccessToken
}

// SignInAtCompanyIdP is SignIn through the company IdP only: it stops where
// AS-2 sends the browser on to the first provider upstream, leaving the
// sign-in unfinished. AS-2 records the user at the company IdP's leg, so it
// resolves ID-JAGs naming them from then on, but holds no provider credential
// for them, and the providers are never contacted.
func SignInAtCompanyIdP(t testing.TB, h *identityharness.Harness, as2 http.Handler, issuer, resource, user string) {
	t.Helper()
	idp, err := url.Parse(h.IdP.Issuer())
	if err != nil {
		t.Fatalf("as2signin: IdP issuer: %v", err)
	}
	base, err := url.Parse(issuer)
	if err != nil {
		t.Fatalf("as2signin: issuer: %v", err)
	}
	_, _, _, location := browse(t, h, as2, issuer, resource, user, func(u *url.URL) bool {
		return u.Host != base.Host && u.Host != idp.Host
	})
	if location.Host == base.Host || location.Host == idp.Host || strings.HasPrefix(location.String(), RedirectURI+"?") {
		t.Fatalf("as2signin: sign %s in at AS-2's company IdP: redirect to %s, want one to a provider upstream", user, location.Redacted())
	}
}

// browse plays the browser for user from AS-2's authorization endpoint, at
// issuer for resource, through its consent page and on until stop accepts a
// redirect, which it returns with the browser, the PKCE verifier, and the
// state it sent; see SignIn.
func browse(t testing.TB, h *identityharness.Harness, as2 http.Handler, issuer, resource, user string, stop func(*url.URL) bool) (browser *http.Client, verifier, state string, location *url.URL) {
	t.Helper()
	base, err := url.Parse(issuer)
	if err != nil {
		t.Fatalf("as2signin: issuer: %v", err)
	}
	network := h.HTTPClient().Transport
	browser = &http.Client{
		Timeout: 10 * time.Second,
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if r.URL.Host == base.Host {
				return serve(as2, r), nil
			}
			return network.RoundTrip(r)
		}),
		CheckRedirect: func(r *http.Request, _ []*http.Request) error {
			if stop(r.URL) {
				return http.ErrUseLastResponse
			}
			return nil
		},
	}
	verifier = rand.Text() + rand.Text()
	sum := sha256.Sum256([]byte(verifier))
	state = rand.Text()
	authorize := issuer + "/oauth/authorize?" + url.Values{
		"response_type": {"code"}, "client_id": {ClientID}, "redirect_uri": {RedirectURI},
		"scope": {"openid"}, "state": {state}, "resource": {resource},
		"code_challenge": {base64.RawURLEncoding.EncodeToString(sum[:])}, "code_challenge_method": {"S256"},
	}.Encode()
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, authorize, nil)
	if err != nil {
		t.Fatal(err)
	}
	h.IdP.SignInNextAs(user)
	response, err := oauthbrowser.Approve(browser, request)
	if err != nil {
		h.IdP.ClearSignInNextAs()
		t.Fatalf("as2signin: sign %s in at AS-2: %v", user, err)
	}
	_ = response.Body.Close()
	if location, err = response.Location(); err != nil {
		h.IdP.ClearSignInNextAs()
		t.Fatalf("as2signin: sign %s in at AS-2: status %d without a redirect", user, response.StatusCode)
	}
	return browser, verifier, state, location
}

// serve answers r, a client request, with handler in process.
func serve(handler http.Handler, r *http.Request) *http.Response {
	in := r.Clone(r.Context())
	if in.Body == nil {
		in.Body = http.NoBody
	}
	in.Host, in.RequestURI, in.RemoteAddr = r.URL.Host, r.URL.RequestURI(), "192.0.2.1:1234"
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, in)
	response := recorder.Result()
	response.Request = r
	return response
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
