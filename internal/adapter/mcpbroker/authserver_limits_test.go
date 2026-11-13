package mcpbroker

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stacklok/toolhive/pkg/oauthproto"

	"github.com/stacklok/mecatl/internal/adapter/authserverlimit"
	"github.com/stacklok/mecatl/internal/identitybroker/adapter/inprocesshttp"
)

const (
	limitAuthorize         = toolHiveBasePath + "/oauth/authorize"
	limitConsent           = toolHiveBasePath + "/oauth/consent"
	limitToken             = toolHiveBasePath + "/oauth/token"
	limitUpstreamCallback  = toolHiveBasePath + "/oauth/callback"
	limitDiscovery         = toolHiveBasePath + "/.well-known/openid-configuration"
	limitJWKS              = toolHiveBasePath + "/.well-known/jwks.json"
	limitProtectedResource = toolHiveBasePath + "/.well-known/oauth-protected-resource"
)

// mountedProcess is process's handler bundle on a fresh mux.
func mountedProcess(t *testing.T, process *Process) *http.ServeMux {
	t.Helper()
	mux := http.NewServeMux()
	if err := process.Handlers.Mount(mux, process.CallbackPath); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	return mux
}

// limitStatus is the status handler answers for method and target.
func limitStatus(handler http.Handler, method, target string) int {
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(method, "https://broker.example"+target, nil))
	return recorder.Code
}

// exhaustRoute sends requests to target until a 429 and returns how many
// passed. The bucket refills while it runs, so at least its burst passes.
func exhaustRoute(t *testing.T, handler http.Handler, method, target string, burst int) int {
	t.Helper()
	for passed := range 10 * burst {
		if limitStatus(handler, method, target) == http.StatusTooManyRequests {
			return passed
		}
	}
	t.Fatalf("%s %s: no 429 after %d requests", method, target, 10*burst)
	return 0
}

func newLimitedProcess(t *testing.T) *Process {
	t.Helper()
	t.Setenv("MECATL_TEST_CLIENT_SECRET", "construction-only-secret")
	process, err := NewToolHiveProcess(t.Context(), ToolHiveConfig{
		CallbackURL: as2TestCallbackURL, Profiles: []ToolHiveProfile{staticProtectedProfile("private")}, AuthStorage: newRegisterClientSpy(),
	})
	if err != nil {
		t.Fatalf("NewToolHiveProcess: %v", err)
	}
	t.Cleanup(func() { _ = process.Close() })
	return process
}

// TestEmbeddedAuthServerRoutesAreRateLimited pins the global rate limits on
// the embedded authorization server's mounted routes: the token endpoint has
// its own bucket, and every other route shares the sign-in bucket, so
// exhausting either leaves the other serving. The vMCP endpoint is in
// neither bucket.
func TestEmbeddedAuthServerRoutesAreRateLimited(t *testing.T) {
	signInRoutes := []string{limitConsent, limitUpstreamCallback, limitDiscovery, limitJWKS, limitProtectedResource}
	t.Run("sign-in flood", func(t *testing.T) {
		process := newLimitedProcess(t)
		if process.Handlers.VMCP == nil {
			t.Fatal("no vMCP handler with a protected upstream")
		}
		mux := mountedProcess(t, process)
		if passed := exhaustRoute(t, mux, http.MethodGet, limitAuthorize, authserverlimit.SignInBurst); passed < authserverlimit.SignInBurst {
			t.Fatalf("%d authorize requests passed before a 429, want the sign-in burst of %d", passed, authserverlimit.SignInBurst)
		}
		for _, route := range signInRoutes {
			if passed := exhaustRoute(t, mux, http.MethodGet, route, authserverlimit.SignInBurst); passed > 5 {
				t.Errorf("%d requests to %s passed after authorize exhausted the sign-in bucket, want it shared", passed, route)
			}
		}
		if passed := exhaustRoute(t, mux, http.MethodPost, limitToken, authserverlimit.TokenBurst); passed < authserverlimit.TokenBurst {
			t.Errorf("%d token requests passed while sign-in was exhausted, want the token burst of %d", passed, authserverlimit.TokenBurst)
		}
		if code := limitStatus(mux, http.MethodPost, toolHiveMCPPath); code == http.StatusTooManyRequests {
			t.Error("the vMCP endpoint is rate limited with both authorization server buckets exhausted")
		}
	})
	t.Run("token flood", func(t *testing.T) {
		mux := mountedProcess(t, newLimitedProcess(t))
		if passed := exhaustRoute(t, mux, http.MethodPost, limitToken, authserverlimit.TokenBurst); passed < authserverlimit.TokenBurst {
			t.Fatalf("%d token requests passed before a 429, want the token burst of %d", passed, authserverlimit.TokenBurst)
		}
		if passed := exhaustRoute(t, mux, http.MethodGet, limitAuthorize, authserverlimit.SignInBurst); passed < authserverlimit.SignInBurst {
			t.Errorf("%d authorize requests passed while the token endpoint was exhausted, want the sign-in burst of %d", passed, authserverlimit.SignInBurst)
		}
	})
}

// TestEmbeddedAuthServerRoutesRefuseAnOversizedQuery pins the query cap on
// every mounted authorization server route.
func TestEmbeddedAuthServerRoutesRefuseAnOversizedQuery(t *testing.T) {
	mux := mountedProcess(t, newLimitedProcess(t))
	long := "?state=" + strings.Repeat("s", authserverlimit.MaxQueryBytes)
	for _, route := range []string{limitAuthorize, limitConsent, limitToken, limitUpstreamCallback, limitDiscovery, limitJWKS, limitProtectedResource} {
		if code := limitStatus(mux, http.MethodGet, route+long); code != http.StatusRequestURITooLong {
			t.Errorf("GET %s with an oversized query = %d, want 414", route, code)
		}
	}
}

// TestAS2InProcessHandlerIsCappedButNotPublicRateLimited pins that the
// identity broker's in-process path to AS-2 has the same query cap as the
// mounted token route, and still redeems an ID-JAG once anonymous traffic has
// exhausted the mounted token bucket. ToolHive caps every auth-server body
// itself, so the body cap is proven by the authserverlimit unit test.
func TestAS2InProcessHandlerIsCappedButNotPublicRateLimited(t *testing.T) {
	h, broker, process := newTrustingAS2(t)
	as2 := process.AS2
	mux := mountedProcess(t, process)
	redeem := func(handler http.Handler) int {
		t.Helper()
		form := url.Values{
			"grant_type":            {oauthproto.GrantTypeJWTBearer},
			"assertion":             {mintAS2IDJAG(h, as2)},
			"resource":              {as2.Resource},
			"client_assertion_type": {clientAssertionTyp},
			"client_assertion":      {broker.assertion(t, as2.Issuer)},
		}
		client := inprocesshttp.NewClient(handler, func(*url.URL) error { return nil })
		client.Timeout = 5 * time.Second
		resp, err := client.PostForm(as2.TokenEndpoint, form)
		if err != nil {
			t.Fatalf("token request: %v", err)
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}
	for name, handler := range map[string]http.Handler{"in-process AS-2": as2.Handler(), "mounted token route": mux} {
		t.Run(name, func(t *testing.T) {
			if code := redeem(handler); code != http.StatusOK {
				t.Fatalf("redemption = %d, want 200", code)
			}
			if code := limitStatus(handler, http.MethodPost, limitToken+"?state="+strings.Repeat("s", authserverlimit.MaxQueryBytes)); code != http.StatusRequestURITooLong {
				t.Errorf("token request with an oversized query = %d, want 414", code)
			}
		})
	}

	exhaustRoute(t, mux, http.MethodPost, limitToken, authserverlimit.TokenBurst)
	if code := redeem(mux); code != http.StatusTooManyRequests {
		t.Fatalf("mounted redemption after the flood = %d, want 429", code)
	}
	if code := redeem(as2.Handler()); code != http.StatusOK {
		t.Fatalf("in-process redemption after the public token bucket is exhausted = %d, want 200", code)
	}
}

// TestCallbackOnlyBundleIsNotRateLimited pins that a broker with no protected
// upstream, which mounts only its own callback route, is unchanged.
func TestCallbackOnlyBundleIsNotRateLimited(t *testing.T) {
	var requests atomic.Int32
	anonymous := toolHiveDiscoveryServer(t, "get_status", &requests)
	process, err := NewToolHiveProcess(t.Context(), ToolHiveConfig{
		CallbackURL: as2TestCallbackURL, Profiles: []ToolHiveProfile{{Name: "status", URL: anonymous.URL, Auth: authNone}},
	})
	if err != nil {
		t.Fatalf("NewToolHiveProcess: %v", err)
	}
	t.Cleanup(func() { _ = process.Close() })
	others := process.Handlers
	others.Callback = nil
	if !others.Empty() || process.Handlers.Callback == nil {
		t.Fatalf("handlers = %+v, want only the callback", process.Handlers)
	}
	mux := mountedProcess(t, process)
	for range 2 * authserverlimit.SignInBurst {
		if code := limitStatus(mux, http.MethodGet, process.CallbackPath); code == http.StatusTooManyRequests {
			t.Fatal("the broker callback was rate limited")
		}
	}
}
