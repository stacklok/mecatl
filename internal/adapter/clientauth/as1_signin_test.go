package clientauth

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stacklok/mecatl/internal/testutil/identityharness"
	"github.com/stacklok/mecatl/mcp/oauthlogin"
)

// tokenFormRecorder records the form of every token endpoint request, so a test
// can see what the client sent without the authorization server echoing it.
type tokenFormRecorder struct {
	next http.RoundTripper
	mu   sync.Mutex
	// forms are the token requests' forms, in order.
	forms []url.Values
}

func (r *tokenFormRecorder) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method == http.MethodPost && req.Body != nil {
		body, err := io.ReadAll(req.Body)
		_ = req.Body.Close()
		if err != nil {
			return nil, err
		}
		req.Body = io.NopCloser(bytes.NewReader(body))
		if form, err := url.ParseQuery(string(body)); err == nil {
			r.mu.Lock()
			r.forms = append(r.forms, form)
			r.mu.Unlock()
		}
	}
	return r.next.RoundTrip(req)
}

func (r *tokenFormRecorder) grants() []url.Values {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]url.Values(nil), r.forms...)
}

type as1Fixture struct {
	harness  *identityharness.Harness
	identity Identity
	client   *http.Client
	recorder *tokenFormRecorder
	// authorizeURLs are the authorization requests the presenter saw.
	authorizeURLs []*url.URL
}

func newAS1Fixture(t *testing.T) *as1Fixture {
	t.Helper()
	h := identityharness.New(t, identityharness.Options{Clients: []identityharness.Client{
		{ID: "mecatui", RedirectURIs: []string{oauthlogin.ExactRedirectURL}},
	}})
	client := h.HTTPClient()
	recorder := &tokenFormRecorder{next: client.Transport}
	client.Transport = recorder
	id, err := Identity{
		Target: "mecatl.test:443", Issuer: h.AS1.Issuer(), ClientID: "mecatui",
		Audience: h.AS1.Audience(), RedirectURI: oauthlogin.ExactRedirectURL, Scopes: []string{"openid"},
	}.Canonical()
	if err != nil {
		t.Fatalf("identity: %v", err)
	}
	return &as1Fixture{harness: h, identity: id, client: client, recorder: recorder}
}

// presenter completes the browser step at the harness for the real
// authorization URL Login built.
func (f *as1Fixture) presenter() Presenter {
	return PresenterFunc(func(ctx context.Context, raw string) (oauthlogin.Result, error) {
		u, err := url.Parse(raw)
		if err != nil {
			return oauthlogin.Result{}, err
		}
		f.authorizeURLs = append(f.authorizeURLs, u)
		back, err := f.harness.Authorize(ctx, raw)
		if err != nil {
			return oauthlogin.Result{}, err
		}
		if code := back.Get("error"); code != "" {
			return oauthlogin.Result{}, errors.New("authorization refused: " + code)
		}
		return oauthlogin.Result{Code: back.Get("code"), State: back.Get("state"), Iss: back.Get("iss")}, nil
	})
}

func (f *as1Fixture) login(t *testing.T, identityBrokerSignIn bool) Token {
	t.Helper()
	tok, err := Login(t.Context(), LoginConfig{Identity: f.identity, Presenter: f.presenter(), HTTPClient: f.client, IdentityBrokerSignIn: identityBrokerSignIn})
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	return tok
}

// TestLoginAtAS1SendsResourceAndRefreshRotates signs in at the AS-1 stand-in
// with the real authorization code + PKCE flow and the resource indicator on,
// then forces refreshes through a RefreshSource: every leg names the Mecatl API
// as the RFC 8707 resource, each forced refresh yields a new access token and
// persists the rotated refresh token, and replaying a retired refresh token
// ends the session.
func TestLoginAtAS1SendsResourceAndRefreshRotates(t *testing.T) {
	f := newAS1Fixture(t)
	initial := f.login(t, true)
	if initial.AccessToken == "" || initial.RefreshToken == "" {
		t.Fatal("Login returned no access or refresh token")
	}
	if len(f.authorizeURLs) != 1 || f.authorizeURLs[0].Query().Get("resource") != f.identity.Audience {
		t.Fatalf("authorization request resource = %v, want the audience", f.authorizeURLs)
	}

	c := credentials(t)
	if _, err := c.Save(t.Context(), f.identity, initial, nil); err != nil {
		t.Fatal(err)
	}
	registry, err := OpenRegistry(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s, err := NewRefreshSource(t.Context(), c, LoginConfig{Identity: f.identity, HTTPClient: f.client, Registry: registry, IdentityBrokerSignIn: true})
	if err != nil {
		t.Fatalf("NewRefreshSource: %v", err)
	}
	defer func() { _ = s.Close() }()

	if got, err := s.Token(t.Context()); err != nil || got != initial.AccessToken {
		t.Fatalf("Token: err=%v, same as login token=%v; want the still-valid login token", err, got == initial.AccessToken)
	}
	first, err := s.Refresh(t.Context())
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	second, err := s.Refresh(t.Context())
	if err != nil {
		t.Fatalf("second Refresh: %v", err)
	}
	if first == initial.AccessToken || second == first {
		t.Fatal("forced refresh returned an access token the client already held")
	}
	if got, err := s.Token(t.Context()); err != nil || got != second {
		t.Fatalf("Token after Refresh: err=%v, latest=%v; want the refreshed token", err, got == second)
	}
	rec, err := c.Load(t.Context(), f.identity)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Token.AccessToken != second || rec.Token.RefreshToken == "" || rec.Token.RefreshToken == initial.RefreshToken {
		t.Fatal("rotated refresh token was not persisted with the refreshed access token")
	}

	grants := map[string]int{}
	for _, form := range f.recorder.grants() {
		grant := form.Get("grant_type")
		grants[grant]++
		if got := form["resource"]; len(got) != 1 || got[0] != f.identity.Audience {
			t.Errorf("%s request resource = %q, want the audience", grant, got)
		}
	}
	if grants["authorization_code"] != 1 || grants["refresh_token"] != 2 {
		t.Fatalf("token requests by grant = %v, want 1 authorization_code and 2 refresh_token", grants)
	}

	// Put the retired refresh token back: AS-1 rotated it away, so the forced
	// refresh fails with invalid_grant and the session is over.
	stale := rec.Token
	stale.RefreshToken = initial.RefreshToken
	if _, err := c.Save(t.Context(), f.identity, stale, &rec.Version); err != nil {
		t.Fatal(err)
	}
	_, err = s.Refresh(t.Context())
	var required *LoginRequiredError
	if !errors.As(err, &required) || required.Cause != SessionExpired {
		t.Fatalf("Refresh with a retired refresh token: err=%v, want SessionExpired", err)
	}
}

// TestLoginAtAS1RefusesAForeignResource is the negative control for the
// resource assertion: a client configured with another audience is refused at
// the authorization request.
func TestLoginAtAS1RefusesAForeignResource(t *testing.T) {
	f := newAS1Fixture(t)
	f.identity.Audience = "https://other.test/api"
	if _, err := Login(t.Context(), LoginConfig{Identity: f.identity, Presenter: f.presenter(), HTTPClient: f.client, IdentityBrokerSignIn: true}); !errors.Is(err, ErrAuthorization) {
		t.Fatalf("Login with a foreign audience: err=%v, want ErrAuthorization", err)
	}
}

// assertNoResource fails if the authorization request or any token request
// carried the RFC 8707 resource parameter, which some providers reject.
func (f *as1Fixture) assertNoResource(t *testing.T, wantRefreshes int) {
	t.Helper()
	for _, u := range f.authorizeURLs {
		if _, ok := u.Query()["resource"]; ok {
			t.Errorf("authorization request sent resource %q", u.Query()["resource"])
		}
	}
	grants := map[string]int{}
	for _, form := range f.recorder.grants() {
		grants[form.Get("grant_type")]++
		if _, ok := form["resource"]; ok {
			t.Errorf("%s request sent resource %q", form.Get("grant_type"), form["resource"])
		}
	}
	if grants["refresh_token"] != wantRefreshes {
		t.Fatalf("token requests by grant = %v, want %d refresh_token", grants, wantRefreshes)
	}
}

// TestLoginWithoutIdentityBrokerSignInOmitsResource is the default: without the
// server's request, no leg (authorization, code exchange, refresh) sends
// resource, and AS-1 still issues tokens for the audience.
func TestLoginWithoutIdentityBrokerSignInOmitsResource(t *testing.T) {
	f := newAS1Fixture(t)
	initial := f.login(t, false)
	c := credentials(t)
	if _, err := c.Save(t.Context(), f.identity, initial, nil); err != nil {
		t.Fatal(err)
	}
	registry, err := OpenRegistry(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s, err := NewRefreshSource(t.Context(), c, LoginConfig{Identity: f.identity, HTTPClient: f.client, Registry: registry})
	if err != nil {
		t.Fatalf("NewRefreshSource: %v", err)
	}
	defer func() { _ = s.Close() }()
	if _, err := s.Refresh(t.Context()); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if len(f.authorizeURLs) != 1 {
		t.Fatalf("authorization requests = %d, want 1", len(f.authorizeURLs))
	}
	f.assertNoResource(t, 1)
}

// TestRefreshAfterReloadUsesTheSavedIdentityBrokerSignIn drives the restart path
// the TUI uses: the flag comes back from the saved registry row, not from
// fresh discovery. A row written without the field (as older registries are)
// refreshes without resource; a row saved with it refreshes with it.
func TestRefreshAfterReloadUsesTheSavedIdentityBrokerSignIn(t *testing.T) {
	for _, tc := range []struct {
		name      string
		indicator bool
	}{{"legacy row without the field", false}, {"row saved with the field", true}} {
		t.Run(tc.name, func(t *testing.T) {
			f := newAS1Fixture(t)
			c := credentials(t)
			if _, err := c.Save(t.Context(), f.identity, f.login(t, tc.indicator), nil); err != nil {
				t.Fatal(err)
			}
			root := t.TempDir()
			registry, err := OpenRegistry(root)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := registry.Upsert(Connection{Identity: f.identity, IdentityBrokerSignIn: tc.indicator}); err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(filepath.Join(registry.root, "clientauth-connections.json"))
			if err != nil {
				t.Fatal(err)
			}
			if got := bytes.Contains(raw, []byte(`"identity_broker_sign_in"`)); got != tc.indicator {
				t.Fatalf("registry row carries resource_indicator = %v, want %v (absent when false, as in older registries)", got, tc.indicator)
			}

			reloaded, err := OpenExistingRegistry(root)
			if err != nil {
				t.Fatal(err)
			}
			conn, err := reloaded.Find(f.identity.Target)
			if err != nil {
				t.Fatal(err)
			}
			if conn.IdentityBrokerSignIn != tc.indicator {
				t.Fatalf("reloaded IdentityBrokerSignIn = %v, want %v", conn.IdentityBrokerSignIn, tc.indicator)
			}
			s, err := NewRefreshSource(t.Context(), c, LoginConfig{Identity: conn.Identity, HTTPClient: f.client, Registry: reloaded, IdentityBrokerSignIn: conn.IdentityBrokerSignIn})
			if err != nil {
				t.Fatalf("NewRefreshSource: %v", err)
			}
			defer func() { _ = s.Close() }()
			if _, err := s.Refresh(t.Context()); err != nil {
				t.Fatalf("Refresh: %v", err)
			}
			if !tc.indicator {
				f.assertNoResource(t, 1)
				return
			}
			forms := f.recorder.grants()
			last := forms[len(forms)-1]
			if last.Get("grant_type") != "refresh_token" || last.Get("resource") != f.identity.Audience {
				t.Fatalf("refresh after reload sent grant %q resource %q, want refresh_token with the audience", last.Get("grant_type"), last["resource"])
			}
		})
	}
}
