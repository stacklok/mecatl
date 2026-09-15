package oauthlogin

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const testIssuer = "https://issuer.example.test/oauth"

type launcherFunc func(context.Context, string) error

func (f launcherFunc) Open(ctx context.Context, rawURL string) error { return f(ctx, rawURL) }

func loopbackClient() *http.Client {
	return &http.Client{Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true}, Timeout: 2 * time.Second}
}

type responseSnapshot struct {
	status int
	header http.Header
	body   string
}

func request(t *testing.T, req *http.Request) responseSnapshot {
	t.Helper()
	resp, err := loopbackClient().Do(req)
	if err != nil {
		t.Fatalf("callback request: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read callback response: %v", err)
	}
	return responseSnapshot{status: resp.StatusCode, header: resp.Header, body: string(body)}
}

func callbackURL(redirect, code, state, issuer string) string {
	values := url.Values{"code": {code}, "state": {state}, "iss": {issuer}}
	return redirect + "?" + values.Encode()
}

func runWithLauncher(t *testing.T, launcher BrowserLauncher, authorize AuthorizeFunc) error {
	t.Helper()
	runtime, err := New(Options{Launcher: launcher})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return runtime.Authorize(ctx, testIssuer, authorize)
}

func TestADR_0325_RegistrationBoundCallbackPath(t *testing.T) {
	path := callbackPrefix + base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{7}, callbackBytes))
	runtime, err := New(Options{Launcher: launcherFunc(func(context.Context, string) error { return nil })})
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.AuthorizeWithCallbackPath(context.Background(), testIssuer, "/wrong", func(context.Context, string, func(context.Context, string) (Result, error)) error { return nil }); err == nil {
		t.Fatal("invalid registration-bound path was accepted")
	}

	var redirects []string
	for i := 0; i < 2; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		err = runtime.AuthorizeWithCallbackPath(ctx, testIssuer, path, func(ctx context.Context, redirect string, present func(context.Context, string) (Result, error)) error {
			redirects = append(redirects, redirect)
			parsed, parseErr := url.Parse(redirect)
			if parseErr != nil || parsed.Path != path || parsed.Hostname() != "127.0.0.1" {
				return fmt.Errorf("bound redirect = %q: %v", redirect, parseErr)
			}
			go func() {
				req, _ := http.NewRequest(http.MethodGet, callbackURL(redirect, "code", fmt.Sprintf("state-%d", i), testIssuer), nil)
				_ = request(t, req)
			}()
			result, presentErr := present(ctx, "https://as.example.test/authorize?state="+fmt.Sprintf("state-%d", i))
			if presentErr == nil && result.State != fmt.Sprintf("state-%d", i) {
				t.Fatalf("callback result = %#v", result)
			}
			return presentErr
		})
		cancel()
		if err != nil {
			t.Fatal(err)
		}
	}
	if redirects[0] == redirects[1] {
		t.Fatalf("ephemeral callback port was reused: %q", redirects[0])
	}

	fixed, err := New(Options{RedirectURL: ExactRedirectURL})
	if err != nil {
		t.Fatal(err)
	}
	if err := fixed.AuthorizeWithCallbackPath(context.Background(), testIssuer, path, func(context.Context, string, func(context.Context, string) (Result, error)) error { return nil }); err == nil {
		t.Fatal("registration-bound path conflicted with fixed redirect but was accepted")
	}
}

func TestAuthorizeRealLoopbackHappyPath(t *testing.T) {
	var redirect string
	var response responseSnapshot
	launcher := launcherFunc(func(_ context.Context, authorizationURL string) error {
		parsed, err := url.Parse(authorizationURL)
		if err != nil {
			t.Fatal(err)
		}
		if got := parsed.Query()["state"]; len(got) != 1 || got[0] != "state-canary" {
			t.Fatalf("authorization state = %q", got)
		}
		req, _ := http.NewRequest(http.MethodGet, callbackURL(redirect, "code-canary", "state-canary", testIssuer), nil)
		response = request(t, req)
		return nil
	})

	err := runWithLauncher(t, launcher, func(ctx context.Context, gotRedirect string, present func(context.Context, string) (Result, error)) error {
		redirect = gotRedirect
		parsed, err := url.Parse(gotRedirect)
		if err != nil {
			return err
		}
		if parsed.Scheme != "http" || parsed.Hostname() != "127.0.0.1" || parsed.Port() == "" {
			t.Fatalf("redirect is not ephemeral IPv4 loopback: %q", gotRedirect)
		}
		if !regexp.MustCompile(`^/oauth/callback/[A-Za-z0-9_-]{43}$`).MatchString(parsed.Path) {
			t.Fatalf("callback path = %q", parsed.Path)
		}
		result, err := present(ctx, "https://as.example.test/authorize?state=state-canary")
		if err != nil {
			return err
		}
		if result != (Result{Code: "code-canary", State: "state-canary", Iss: testIssuer}) {
			t.Fatalf("result = %#v", result)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.status != http.StatusOK || response.body != successHTML {
		t.Fatalf("response = %d %q", response.status, response.body)
	}
	assertSecurityHeaders(t, response.header)

	address := strings.TrimPrefix(strings.Split(redirect, callbackPrefix)[0], "http://")
	listener, err := net.Listen("tcp4", address)
	if err != nil {
		t.Fatalf("callback port was not released: %v", err)
	}
	_ = listener.Close()
}

func TestCallbackAcceptsAbsentIssuerAndPreservesEmptyResult(t *testing.T) {
	var redirect string
	launcher := launcherFunc(func(_ context.Context, _ string) error {
		req, _ := http.NewRequest(http.MethodGet, redirect+"?code=code-canary&state=state-canary", nil)
		if got := request(t, req).status; got != http.StatusOK {
			t.Fatalf("callback status = %d", got)
		}
		return nil
	})

	err := runWithLauncher(t, launcher, func(ctx context.Context, gotRedirect string, present func(context.Context, string) (Result, error)) error {
		redirect = gotRedirect
		result, err := present(ctx, "https://as.example.test/authorize?state=state-canary")
		if err != nil {
			return err
		}
		if result != (Result{Code: "code-canary", State: "state-canary", Iss: ""}) {
			t.Fatalf("result = %#v", result)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestCallbackRejectsInvalidRequestsThenAcceptsValid covers rejections that
// happen at or before the state compare. The sender has not proved knowledge of
// the state secret, so it may be any local process: such a rejection records a
// reason but must never stop the real browser callback from completing.
func TestCallbackRejectsInvalidRequestsThenAcceptsValid(t *testing.T) {
	tests := map[string]func(string) *http.Request{
		"wrong path": func(redirect string) *http.Request {
			req, _ := http.NewRequest(http.MethodGet, strings.Replace(redirect, callbackPrefix, "/wrong/", 1)+"?code=c&state=s&iss="+url.QueryEscape(testIssuer), nil)
			return req
		},
		"wrong method": func(redirect string) *http.Request {
			req, _ := http.NewRequest(http.MethodPost, callbackURL(redirect, "c", "s", testIssuer), nil)
			return req
		},
		"content length body": func(redirect string) *http.Request {
			req, _ := http.NewRequest(http.MethodGet, callbackURL(redirect, "c", "s", testIssuer), strings.NewReader("x"))
			return req
		},
		"transfer encoded body": func(redirect string) *http.Request {
			req, _ := http.NewRequest(http.MethodGet, callbackURL(redirect, "c", "s", testIssuer), io.NopCloser(strings.NewReader("x")))
			req.ContentLength = -1
			return req
		},
		"wrong host": func(redirect string) *http.Request {
			req, _ := http.NewRequest(http.MethodGet, callbackURL(redirect, "c", "s", testIssuer), nil)
			req.Host = "localhost"
			return req
		},
		"wrong state": func(redirect string) *http.Request {
			req, _ := http.NewRequest(http.MethodGet, callbackURL(redirect, "c", "wrong-state", testIssuer), nil)
			return req
		},
		"empty code": func(redirect string) *http.Request {
			req, _ := http.NewRequest(http.MethodGet, redirect+"?code=&state=s&iss="+url.QueryEscape(testIssuer), nil)
			return req
		},
		"empty state": func(redirect string) *http.Request {
			req, _ := http.NewRequest(http.MethodGet, redirect+"?code=c&state=&iss="+url.QueryEscape(testIssuer), nil)
			return req
		},
		"empty issuer": func(redirect string) *http.Request {
			req, _ := http.NewRequest(http.MethodGet, redirect+"?code=c&state=s&iss=", nil)
			return req
		},
		"duplicate code": func(redirect string) *http.Request {
			req, _ := http.NewRequest(http.MethodGet, callbackURL(redirect, "c", "s", testIssuer)+"&code=c", nil)
			return req
		},
		"duplicate issuer": func(redirect string) *http.Request {
			req, _ := http.NewRequest(http.MethodGet, callbackURL(redirect, "c", "s", testIssuer)+"&iss="+url.QueryEscape(testIssuer), nil)
			return req
		},
		"duplicate state": func(redirect string) *http.Request {
			req, _ := http.NewRequest(http.MethodGet, callbackURL(redirect, "c", "s", testIssuer)+"&state=s", nil)
			return req
		},
		"duplicate unknown": func(redirect string) *http.Request {
			req, _ := http.NewRequest(http.MethodGet, callbackURL(redirect, "c", "s", testIssuer)+"&x=1&x=2", nil)
			return req
		},
		"duplicate OAuth error": func(redirect string) *http.Request {
			req, _ := http.NewRequest(http.MethodGet, redirect+"?error=denied&error=denied&state=s&iss="+url.QueryEscape(testIssuer), nil)
			return req
		},
		"empty query value": func(redirect string) *http.Request {
			req, _ := http.NewRequest(http.MethodGet, callbackURL(redirect, "c", "s", testIssuer)+"&x=", nil)
			return req
		},
		"oversized code": func(redirect string) *http.Request {
			req, _ := http.NewRequest(http.MethodGet, callbackURL(redirect, strings.Repeat("x", maxQueryValueBytes+1), "s", testIssuer), nil)
			return req
		},
		"oversized state": func(redirect string) *http.Request {
			req, _ := http.NewRequest(http.MethodGet, callbackURL(redirect, "c", strings.Repeat("x", maxQueryValueBytes+1), testIssuer), nil)
			return req
		},
		"oversized issuer": func(redirect string) *http.Request {
			req, _ := http.NewRequest(http.MethodGet, callbackURL(redirect, "c", "s", strings.Repeat("x", maxQueryValueBytes+1)), nil)
			return req
		},
		"oversized raw query": func(redirect string) *http.Request {
			req, _ := http.NewRequest(http.MethodGet, callbackURL(redirect, "c", "s", testIssuer)+"&padding="+strings.Repeat("x", maxRawQueryBytes), nil)
			return req
		},
		"oversized unknown value": func(redirect string) *http.Request {
			req, _ := http.NewRequest(http.MethodGet, callbackURL(redirect, "c", "s", testIssuer)+"&x="+strings.Repeat("x", maxQueryValueBytes+1), nil)
			return req
		},
	}

	for name, invalidRequest := range tests {
		t.Run(name, func(t *testing.T) {
			var redirect string
			launcher := launcherFunc(func(_ context.Context, _ string) error {
				invalid := request(t, invalidRequest(redirect))
				if invalid.status < 400 || invalid.body != failureHTML {
					t.Fatalf("invalid response = %d %q", invalid.status, invalid.body)
				}
				assertSecurityHeaders(t, invalid.header)
				valid, _ := http.NewRequest(http.MethodGet, callbackURL(redirect, "good", "s", testIssuer), nil)
				if got := request(t, valid).status; got != http.StatusOK {
					t.Fatalf("valid status = %d", got)
				}
				return nil
			})
			err := runWithLauncher(t, launcher, func(ctx context.Context, gotRedirect string, present func(context.Context, string) (Result, error)) error {
				redirect = gotRedirect
				result, err := present(ctx, "https://as.example.test/authorize?state=s")
				if err == nil && result.Code != "good" {
					t.Fatalf("result = %#v", result)
				}
				return err
			})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

// TestAuthenticatedRejectionIsTerminalAndNamesTheRule covers rejections past the
// state compare. Only the browser that received the authorization URL knows the
// state, so the reason is trustworthy: the flow ends immediately with the rule
// named, rather than waiting out the callback timeout with no diagnosis.
func TestAuthenticatedRejectionIsTerminalAndNamesTheRule(t *testing.T) {
	tests := map[string]struct {
		query  func(string) string
		reason string
	}{
		"wrong issuer": {
			query:  func(redirect string) string { return callbackURL(redirect, "c", "s", "https://other.example.test") },
			reason: reasonIssuerMismatch,
		},
		"missing code": {
			query: func(redirect string) string {
				return redirect + "?state=s&iss=" + url.QueryEscape(testIssuer)
			},
			reason: reasonNoCode,
		},
		"code and OAuth error": {
			query:  func(redirect string) string { return callbackURL(redirect, "c", "s", testIssuer) + "&error=denied" },
			reason: reasonErrorAndCode,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			var redirect string
			launcher := launcherFunc(func(_ context.Context, _ string) error {
				req, _ := http.NewRequest(http.MethodGet, tc.query(redirect), nil)
				if got := request(t, req); got.status < 400 || got.body != failureHTML {
					t.Fatalf("rejection response = %d %q", got.status, got.body)
				}
				// The flow is already over: a later valid callback finds it gone.
				valid, _ := http.NewRequest(http.MethodGet, callbackURL(redirect, "good", "s", testIssuer), nil)
				if got := request(t, valid).status; got != http.StatusGone {
					t.Fatalf("late valid status = %d, want 410", got)
				}
				return nil
			})
			err := runWithLauncher(t, launcher, func(ctx context.Context, got string, present func(context.Context, string) (Result, error)) error {
				redirect = got
				_, err := present(ctx, "https://as.example.test/authorize?state=s")
				return err
			})
			var rejected *CallbackRejectedError
			if !errors.As(err, &rejected) {
				t.Fatalf("error = %v, want *CallbackRejectedError", err)
			}
			if rejected.Reason != tc.reason {
				t.Fatalf("reason = %q, want %q", rejected.Reason, tc.reason)
			}
			// Existing callers test for the sentinel; that must keep working.
			if !errors.Is(err, ErrAuthorizationFailed) {
				t.Fatalf("error is not ErrAuthorizationFailed: %v", err)
			}
		})
	}
}

// TestUnauthenticatedRejectionReasonSurvivesToTimeout proves the recorded
// pre-state reason reaches the caller when no valid callback ever arrives.
func TestUnauthenticatedRejectionReasonSurvivesToTimeout(t *testing.T) {
	var redirect string
	launcher := launcherFunc(func(_ context.Context, _ string) error {
		req, _ := http.NewRequest(http.MethodGet, callbackURL(redirect, "c", "wrong-state", testIssuer), nil)
		request(t, req)
		return nil
	})
	runtime, err := New(Options{Launcher: launcher})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	err = runtime.Authorize(ctx, testIssuer, func(ctx context.Context, got string, present func(context.Context, string) (Result, error)) error {
		redirect = got
		_, err := present(ctx, "https://as.example.test/authorize?state=s")
		return err
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want DeadlineExceeded", err)
	}
	if !strings.Contains(err.Error(), reasonState) {
		t.Fatalf("timeout error omits the last rejection reason: %v", err)
	}
}

// TestOAuthErrorIsSurfacedAndSanitized pins that a provider's OAuth error reaches
// the caller. Withholding it made an authorization that fails at the provider
// undiagnosable -- Keycloak refusing a scope read as a bare "OAuth authorization
// failed" and had to be reproduced by hand to learn it was invalid_scope. Both
// fields are sanitized to the printable subset RFC 6749 allows and clamped, so a
// hostile value cannot inject log lines or flood the output.
func TestOAuthErrorIsSurfacedAndSanitized(t *testing.T) {
	const description = "Invalid scopes: openid profile offline_access"
	var redirect string
	launcher := launcherFunc(func(_ context.Context, _ string) error {
		values := url.Values{"error": {"invalid_scope"}, "error_description": {description},
			"state": {"s"}, "iss": {testIssuer}}
		req, _ := http.NewRequest(http.MethodGet, redirect+"?"+values.Encode(), nil)
		if got := request(t, req).body; got != failureHTML {
			t.Fatalf("body = %q", got)
		}
		return nil
	})
	err := runWithLauncher(t, launcher, func(ctx context.Context, got string, present func(context.Context, string) (Result, error)) error {
		redirect = got
		_, err := present(ctx, "https://as.example.test/authorize?state=s")
		return err
	})
	var provider *AuthorizationErrorResponse
	if !errors.As(err, &provider) {
		t.Fatalf("error = %v, want *AuthorizationErrorResponse", err)
	}
	if provider.Code != "invalid_scope" || provider.Description != description {
		t.Fatalf("provider error = %#v", provider)
	}
	if !errors.Is(err, ErrAuthorizationFailed) {
		t.Fatalf("error is not ErrAuthorizationFailed: %v", err)
	}
}

// TestOAuthErrorFieldsAreBoundedAndScrubbed covers the hostile-value side: control
// characters and non-printables are dropped rather than echoed, oversized fields
// are clamped, and a wholly-disallowed code degrades to a placeholder instead of
// being partially reconstructed.
func TestOAuthErrorFieldsAreBoundedAndScrubbed(t *testing.T) {
	tests := map[string]struct {
		code, desc string
		wantCode   string
		wantDesc   string
	}{
		"newline injection": {
			code: "bad\r\nSet-Cookie: x", desc: "line\r\nInjected: y",
			wantCode: "badSet-Cookie: x", wantDesc: "lineInjected: y",
		},
		"non-printable code": {
			code: "\x01\x02", desc: "fine",
			wantCode: "unspecified", wantDesc: "fine",
		},
		"oversized description": {
			code: "invalid_request", desc: strings.Repeat("d", maxOAuthErrorField*2),
			wantCode: "invalid_request", wantDesc: strings.Repeat("d", maxOAuthErrorField),
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			var redirect string
			launcher := launcherFunc(func(_ context.Context, _ string) error {
				values := url.Values{"error": {tc.code}, "error_description": {tc.desc},
					"state": {"s"}, "iss": {testIssuer}}
				req, _ := http.NewRequest(http.MethodGet, redirect+"?"+values.Encode(), nil)
				request(t, req)
				return nil
			})
			err := runWithLauncher(t, launcher, func(ctx context.Context, got string, present func(context.Context, string) (Result, error)) error {
				redirect = got
				_, err := present(ctx, "https://as.example.test/authorize?state=s")
				return err
			})
			var provider *AuthorizationErrorResponse
			if !errors.As(err, &provider) {
				t.Fatalf("error = %v", err)
			}
			if provider.Code != tc.wantCode {
				t.Fatalf("code = %q, want %q", provider.Code, tc.wantCode)
			}
			if provider.Description != tc.wantDesc {
				t.Fatalf("description = %q, want %q", provider.Description, tc.wantDesc)
			}
			if strings.ContainsAny(provider.Code+provider.Description, "\r\n\x00") {
				t.Fatalf("control characters survived: %#v", provider)
			}
		})
	}
}

func TestRequestAttemptBudget(t *testing.T) {
	var redirect string
	launcher := launcherFunc(func(_ context.Context, _ string) error {
		for range maxRequestAttempts {
			req, _ := http.NewRequest(http.MethodGet, callbackURL(redirect, "c", "wrong-state", testIssuer), nil)
			request(t, req)
		}
		return nil
	})
	err := runWithLauncher(t, launcher, func(ctx context.Context, got string, present func(context.Context, string) (Result, error)) error {
		redirect = got
		_, err := present(ctx, "https://as.example.test/authorize?state=s")
		return err
	})
	if !errors.Is(err, ErrCallbackAttempts) {
		t.Fatalf("error = %v", err)
	}
}

func TestUnauthenticatedProbeFloodDoesNotAbortValidCallback(t *testing.T) {
	var redirect string
	launcher := launcherFunc(func(_ context.Context, _ string) error {
		parsed, err := url.Parse(redirect)
		if err != nil {
			return err
		}
		// Requests without the random callback capability are untrusted ambient
		// loopback traffic, not authorization attempts.
		for range 2 * maxRequestAttempts {
			wrongPath, _ := http.NewRequest(http.MethodGet, "http://"+parsed.Host+"/probe", nil)
			if got := request(t, wrongPath).status; got != http.StatusNotFound {
				t.Fatalf("wrong-path status = %d", got)
			}

			wrongHost, _ := http.NewRequest(http.MethodGet, callbackURL(redirect, "probe", "s", testIssuer), nil)
			wrongHost.Host = "localhost"
			if got := request(t, wrongHost).status; got != http.StatusNotFound {
				t.Fatalf("wrong-host status = %d", got)
			}
		}
		valid, _ := http.NewRequest(http.MethodGet, callbackURL(redirect, "good", "s", testIssuer), nil)
		if got := request(t, valid).status; got != http.StatusOK {
			t.Fatalf("valid status after probe flood = %d", got)
		}
		return nil
	})
	err := runWithLauncher(t, launcher, func(ctx context.Context, got string, present func(context.Context, string) (Result, error)) error {
		redirect = got
		result, err := present(ctx, "https://as.example.test/authorize?state=s")
		if err == nil && result.Code != "good" {
			t.Fatalf("result = %#v", result)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestDuplicateCallbackOnlyOneWins(t *testing.T) {
	var redirect string
	launcher := launcherFunc(func(_ context.Context, _ string) error {
		start := make(chan struct{})
		statuses := make(chan int, 2)
		var wg sync.WaitGroup
		for range 2 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				req, _ := http.NewRequest(http.MethodGet, callbackURL(redirect, "c", "s", testIssuer), nil)
				statuses <- request(t, req).status
			}()
		}
		close(start)
		wg.Wait()
		close(statuses)
		counts := map[int]int{}
		for status := range statuses {
			counts[status]++
		}
		if counts[http.StatusOK] != 1 || counts[http.StatusGone] != 1 {
			t.Fatalf("statuses = %#v", counts)
		}
		return nil
	})
	err := runWithLauncher(t, launcher, func(ctx context.Context, got string, present func(context.Context, string) (Result, error)) error {
		redirect = got
		_, err := present(ctx, "https://as.example.test/authorize?state=s")
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestExactRedirectUsesFixedIPv4Callback(t *testing.T) {
	var redirect string
	runtime, err := New(Options{
		RedirectURL: ExactRedirectURL,
		Launcher: launcherFunc(func(_ context.Context, _ string) error {
			wrongHost, _ := http.NewRequest(http.MethodGet, callbackURL(redirect, "bad", "s", testIssuer), nil)
			wrongHost.Host = "127.0.0.1:18472"
			if got := request(t, wrongHost).status; got != http.StatusNotFound {
				t.Fatalf("wrong-host status = %d", got)
			}
			wrongPath, _ := http.NewRequest(http.MethodGet, strings.Replace(callbackURL(redirect, "bad", "s", testIssuer), "/oauth/callback?", "/oauth/wrong?", 1), nil)
			if got := request(t, wrongPath).status; got != http.StatusNotFound {
				t.Fatalf("wrong-path status = %d", got)
			}
			valid, _ := http.NewRequest(http.MethodGet, callbackURL(redirect, "good", "s", testIssuer), nil)
			if got := request(t, valid).status; got != http.StatusOK {
				t.Fatalf("valid status = %d", got)
			}
			return nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err = runtime.Authorize(ctx, testIssuer, func(ctx context.Context, got string, present func(context.Context, string) (Result, error)) error {
		redirect = got
		if got != ExactRedirectURL {
			t.Fatalf("redirect = %q", got)
		}
		result, err := present(ctx, "https://as.example.test/authorize?state=s")
		if err != nil {
			return err
		}
		if result.Code != "good" {
			t.Fatalf("result = %#v", result)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestExactRedirectUnauthenticatedFloodDoesNotSpendAttempts(t *testing.T) {
	var redirect string
	runtime, err := New(Options{
		RedirectURL: ExactRedirectURL,
		Launcher: launcherFunc(func(_ context.Context, _ string) error {
			for i := range 2 * maxRequestAttempts {
				var req *http.Request
				switch i % 3 {
				case 0:
					req, _ = http.NewRequest(http.MethodGet, callbackURL(redirect, "probe", "wrong-state", testIssuer), nil)
				case 1:
					req, _ = http.NewRequest(http.MethodPost, redirect+"?state=wrong-state", nil)
				default:
					req, _ = http.NewRequest(http.MethodGet, redirect+"?state=%zz", nil)
				}
				if got := request(t, req).status; got < 400 {
					t.Fatalf("probe %d status = %d", i, got)
				}
			}
			valid, _ := http.NewRequest(http.MethodGet, callbackURL(redirect, "good", "s", testIssuer), nil)
			if got := request(t, valid).status; got != http.StatusOK {
				t.Fatalf("valid status after fixed-path flood = %d", got)
			}
			return nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err = runtime.Authorize(ctx, testIssuer, func(ctx context.Context, got string, present func(context.Context, string) (Result, error)) error {
		redirect = got
		result, err := present(ctx, "https://as.example.test/authorize?state=s")
		if err == nil && result.Code != "good" {
			t.Fatalf("result = %#v", result)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestExactRedirectAuthenticatedRejectionIsTerminal(t *testing.T) {
	var redirect string
	runtime, err := New(Options{
		RedirectURL: ExactRedirectURL,
		Launcher: launcherFunc(func(_ context.Context, _ string) error {
			rejected, _ := http.NewRequest(http.MethodGet, redirect+"?state=s", nil)
			if got := request(t, rejected).status; got != http.StatusBadRequest {
				t.Fatalf("rejection status = %d", got)
			}
			valid, _ := http.NewRequest(http.MethodGet, callbackURL(redirect, "late", "s", testIssuer), nil)
			if got := request(t, valid).status; got != http.StatusGone {
				t.Fatalf("late valid status = %d", got)
			}
			return nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	err = runtime.Authorize(context.Background(), testIssuer, func(ctx context.Context, got string, present func(context.Context, string) (Result, error)) error {
		redirect = got
		_, err := present(ctx, "https://as.example.test/authorize?state=s")
		return err
	})
	var rejected *CallbackRejectedError
	if !errors.As(err, &rejected) || rejected.Reason != reasonNoCode || !errors.Is(err, ErrAuthorizationFailed) {
		t.Fatalf("error = %v", err)
	}
}

func TestFixedRedirectRejectsOccupiedPort(t *testing.T) {
	for _, tc := range []struct {
		name, redirect, address string
	}{
		{name: "remote", redirect: ExactRedirectURL, address: "127.0.0.1:18473"},
		{name: "ToolHive-compatible", redirect: ToolHiveCompatibleRedirectURL, address: "localhost:8666"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			listener, err := net.Listen("tcp4", tc.address)
			if err != nil {
				t.Skipf("fixed callback port unavailable for test: %v", err)
			}
			defer listener.Close()
			runtime, err := New(Options{RedirectURL: tc.redirect})
			if err != nil {
				t.Fatal(err)
			}
			called := false
			err = runtime.Authorize(context.Background(), testIssuer, func(context.Context, string, func(context.Context, string) (Result, error)) error {
				called = true
				return nil
			})
			// The closed bind reason distinguishes an occupied fixed port without retaining
			// the nested network error or the callback route.
			if err == nil || called {
				t.Fatalf("occupied-port result = %v, authorize called=%v", err, called)
			}
			var bind *CallbackBindError
			if !errors.As(err, &bind) || bind.Reason != CallbackBindAddressInUse || !errors.Is(err, ErrAuthorizationFailed) {
				t.Fatalf("error = %v, want address-in-use CallbackBindError", err)
			}
			if strings.Contains(err.Error(), callbackPrefix) || strings.Contains(err.Error(), "/callback") {
				t.Fatalf("error leaked the callback path: %v", err)
			}
		})
	}
}

func TestExactRedirectCancellationReleasesListener(t *testing.T) {
	runtime, err := New(Options{RedirectURL: ExactRedirectURL, Launcher: launcherFunc(func(context.Context, string) error { return nil })})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err = runtime.Authorize(ctx, testIssuer, func(ctx context.Context, _ string, present func(context.Context, string) (Result, error)) error {
		_, err := present(ctx, "https://as.example.test/authorize?state=s")
		return err
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout error = %v", err)
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:18473")
	if err != nil {
		t.Fatalf("exact callback listener was not released: %v", err)
	}
	_ = listener.Close()
}

func TestFixedRedirectValidationIsStrict(t *testing.T) {
	for _, redirect := range []string{
		"http://localhost:18473/oauth/callback",
		"http://127.0.0.1:18473/wrong",
		"http://127.0.0.1:18473/oauth/callback?x=1",
		"http://user@127.0.0.1:18473/oauth/callback",
		"https://127.0.0.1:18473/oauth/callback",
		"http://127.0.0.1:8666/callback",
		"http://localhost:8666/oauth/callback",
		"http://localhost:8667/callback",
		"http://localhost:8666/callback?x=1",
		"https://localhost:8666/callback",
	} {
		if _, err := New(Options{RedirectURL: redirect}); err == nil {
			t.Errorf("accepted redirect %q", redirect)
		}
	}
}

func TestCancellationWhileWaitingForCallback(t *testing.T) {
	started := make(chan struct{})
	var redirect string
	runtime, err := New(Options{Launcher: launcherFunc(func(context.Context, string) error {
		close(started)
		return nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- runtime.Authorize(ctx, testIssuer, func(ctx context.Context, got string, present func(context.Context, string) (Result, error)) error {
			redirect = got
			_, err := present(ctx, "https://as.example.test/authorize?state=s")
			return err
		})
	}()
	<-started
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
	parsed, err := url.Parse(redirect)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp4", parsed.Host)
	if err != nil {
		t.Fatalf("canceled callback listener was not released: %v", err)
	}
	_ = listener.Close()
}

func TestSerializationAndCanceledWaiter(t *testing.T) {
	firstStarted := make(chan struct{})
	release := make(chan struct{})
	var launches atomic.Int32
	runtime, err := New(Options{Launcher: launcherFunc(func(context.Context, string) error {
		if launches.Add(1) == 1 {
			close(firstStarted)
			<-release
		}
		return nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	ctx1, cancel1 := context.WithCancel(context.Background())
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- runtime.Authorize(ctx1, testIssuer, func(ctx context.Context, _ string, present func(context.Context, string) (Result, error)) error {
			_, err := present(ctx, "https://as.example.test/authorize?state=first")
			return err
		})
	}()
	<-firstStarted

	ctx2, cancel2 := context.WithCancel(context.Background())
	secondDone := make(chan error, 1)
	go func() {
		secondDone <- runtime.Authorize(ctx2, testIssuer, func(context.Context, string, func(context.Context, string) (Result, error)) error {
			t.Error("serialized waiter entered authorization function")
			return nil
		})
	}()
	cancel2()
	if err := <-secondDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("waiter error = %v", err)
	}
	if launches.Load() != 1 {
		t.Fatalf("launches = %d", launches.Load())
	}
	close(release)
	cancel1()
	if err := <-firstDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("leader error = %v", err)
	}
}

func TestSerializedCallersRunInOrder(t *testing.T) {
	runtime, _ := New(Options{Launcher: launcherFunc(func(context.Context, string) error { return nil })})
	entered := make(chan int, 2)
	releaseFirst := make(chan struct{})
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- runtime.Authorize(context.Background(), testIssuer, func(context.Context, string, func(context.Context, string) (Result, error)) error {
			entered <- 1
			<-releaseFirst
			return nil
		})
	}()
	if got := <-entered; got != 1 {
		t.Fatalf("first entry = %d", got)
	}
	secondDone := make(chan error, 1)
	go func() {
		secondDone <- runtime.Authorize(context.Background(), testIssuer, func(context.Context, string, func(context.Context, string) (Result, error)) error {
			entered <- 2
			return nil
		})
	}()
	select {
	case got := <-entered:
		t.Fatalf("second caller overlapped first: %d", got)
	case <-time.After(30 * time.Millisecond):
	}
	close(releaseFirst)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	if got := <-entered; got != 2 {
		t.Fatalf("second entry = %d", got)
	}
	if err := <-secondDone; err != nil {
		t.Fatal(err)
	}
}

func TestCanceledLeaderReleasesRuntime(t *testing.T) {
	runtime, err := New(Options{Launcher: launcherFunc(func(context.Context, string) error { return nil })})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := runtime.Authorize(ctx, testIssuer, func(context.Context, string, func(context.Context, string) (Result, error)) error { return nil }); !errors.Is(err, context.Canceled) {
		t.Fatalf("first error = %v", err)
	}
	if err := runtime.Authorize(context.Background(), testIssuer, func(context.Context, string, func(context.Context, string) (Result, error)) error { return nil }); err != nil {
		t.Fatalf("second error = %v", err)
	}
}

func TestBindCancellationAndRandomFailureAreSafe(t *testing.T) {
	runtime, _ := New(Options{Launcher: launcherFunc(func(context.Context, string) error {
		t.Fatal("browser must not launch")
		return nil
	})})
	const canary = "bind-secret-canary"
	entered := make(chan struct{})
	runtime.listen = func(ctx context.Context, _, _ string) (net.Listener, error) {
		close(entered)
		<-ctx.Done()
		return nil, fmt.Errorf("%s: %w", canary, ctx.Err())
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- runtime.Authorize(ctx, testIssuer, func(context.Context, string, func(context.Context, string) (Result, error)) error { return nil })
	}()
	<-entered
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) || strings.Contains(err.Error(), canary) {
		t.Fatalf("error = %v", err)
	}

	runtime.random = bytes.NewReader(nil)
	err := runtime.Authorize(context.Background(), testIssuer, func(context.Context, string, func(context.Context, string) (Result, error)) error { return nil })
	if err == nil || strings.Contains(err.Error(), canary) {
		t.Fatalf("unsafe random error = %v", err)
	}
}

func TestAuthorizationURLAndIssuerValidation(t *testing.T) {
	runtime, _ := New(Options{Launcher: launcherFunc(func(context.Context, string) error {
		t.Fatal("browser must not launch for invalid URL")
		return nil
	})})
	issuerTests := []string{"", "issuer.example", "ftp://issuer.example", "https://user@issuer.example", "https://issuer.example?q=x", "https://issuer.example/#fragment"}
	for _, issuer := range issuerTests {
		if err := runtime.Authorize(context.Background(), issuer, func(context.Context, string, func(context.Context, string) (Result, error)) error { return nil }); err == nil {
			t.Errorf("issuer %q accepted", issuer)
		}
	}

	urls := []string{"not a URL", "ftp://as.example/?state=s", "https://as.example/", "https://as.example/?state=", "https://as.example/?state=a&state=b", "https://user@as.example/?state=s", "https://as.example/?state=s#fragment", "https://as.example/?state=" + strings.Repeat("s", maxQueryValueBytes+1)}
	for _, authorizationURL := range urls {
		err := runtime.Authorize(context.Background(), testIssuer, func(ctx context.Context, _ string, present func(context.Context, string) (Result, error)) error {
			_, err := present(ctx, authorizationURL)
			return err
		})
		if err == nil {
			t.Errorf("authorization URL %q accepted", authorizationURL)
		}
	}
}

func TestPresentCanOnlyBeCalledOnce(t *testing.T) {
	runtime, _ := New(Options{Launcher: launcherFunc(func(context.Context, string) error { return errors.New("stop") })})
	err := runtime.Authorize(context.Background(), testIssuer, func(ctx context.Context, _ string, present func(context.Context, string) (Result, error)) error {
		_, _ = present(ctx, "https://as.example/?state=s")
		_, err := present(ctx, "https://as.example/?state=s")
		return err
	})
	if err == nil {
		t.Fatal("second presentation unexpectedly succeeded")
	}
}

func TestAuthorizeReturnUnblocksDetachedPresenter(t *testing.T) {
	launched := make(chan struct{})
	presentDone := make(chan error, 1)
	runtime, _ := New(Options{Launcher: launcherFunc(func(context.Context, string) error {
		close(launched)
		return nil
	})})
	err := runtime.Authorize(context.Background(), testIssuer, func(_ context.Context, _ string, present func(context.Context, string) (Result, error)) error {
		go func() {
			_, presentErr := present(context.Background(), "https://as.example/?state=s")
			presentDone <- presentErr
		}()
		<-launched
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-presentDone:
		if !errors.Is(err, ErrAuthorizationFailed) {
			t.Fatalf("present error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("detached presenter did not stop")
	}
}

func TestCallbackBeforePresentationDoesNotWin(t *testing.T) {
	var redirect string
	launcher := launcherFunc(func(_ context.Context, _ string) error {
		valid, _ := http.NewRequest(http.MethodGet, callbackURL(redirect, "good", "s", testIssuer), nil)
		if got := request(t, valid).status; got != http.StatusOK {
			t.Fatalf("valid status = %d", got)
		}
		return nil
	})
	err := runWithLauncher(t, launcher, func(ctx context.Context, got string, present func(context.Context, string) (Result, error)) error {
		redirect = got
		early, _ := http.NewRequest(http.MethodGet, callbackURL(redirect, "early", "s", testIssuer), nil)
		if status := request(t, early).status; status != http.StatusBadRequest {
			t.Fatalf("early callback status = %d", status)
		}
		result, err := present(ctx, "https://as.example.test/authorize?state=s")
		if err == nil && result.Code != "good" {
			t.Fatalf("result = %#v", result)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestAuthorizeFuncErrorsAreRedacted(t *testing.T) {
	const canary = "https://as.example.test/authorize?state=secret-code-state-issuer"
	runtime, _ := New(Options{Launcher: launcherFunc(func(context.Context, string) error { return nil })})
	err := runtime.Authorize(context.Background(), testIssuer, func(context.Context, string, func(context.Context, string) (Result, error)) error {
		return errors.New(canary)
	})
	if !errors.Is(err, ErrAuthorizationFailed) || strings.Contains(err.Error(), canary) || strings.Contains(err.Error(), "secret-code-state-issuer") {
		t.Fatalf("unsafe authorization error = %v", err)
	}
}

func TestShutdownClosesPartialConnectionsAndJoins(t *testing.T) {
	runtime, _ := New(Options{Launcher: launcherFunc(func(context.Context, string) error { return nil })})
	var conn net.Conn
	started := time.Now()
	err := runtime.Authorize(context.Background(), testIssuer, func(_ context.Context, redirect string, _ func(context.Context, string) (Result, error)) error {
		parsed, parseErr := url.Parse(redirect)
		if parseErr != nil {
			return parseErr
		}
		conn, parseErr = net.Dial("tcp4", parsed.Host)
		return parseErr
	})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if elapsed := time.Since(started); elapsed > 2*shutdownTimeout {
		t.Fatalf("shutdown took %v", elapsed)
	}
	_ = conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	if _, err := conn.Read(make([]byte, 1)); err == nil {
		t.Fatal("partial connection remained open after shutdown")
	}
}

func TestLimitedListenerBoundsConcurrentConnectionsWithoutFlowBudget(t *testing.T) {
	base, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	limited := newLimitedListener(base, 1)

	client1, err := net.Dial("tcp4", base.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	server1, err := limited.Accept()
	if err != nil {
		t.Fatal(err)
	}

	acceptDone := make(chan error, 1)
	go func() {
		conn, acceptErr := limited.Accept()
		if conn != nil {
			_ = conn.Close()
		}
		acceptDone <- acceptErr
	}()
	client2, err := net.Dial("tcp4", base.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer client2.Close()
	_ = client2.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := client2.Read(make([]byte, 1)); err == nil {
		t.Fatal("connection above concurrency cap remained open")
	}
	_ = server1.Close()
	_ = client1.Close()

	client3, err := net.Dial("tcp4", base.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer client3.Close()
	if err := <-acceptDone; err != nil {
		t.Fatalf("accept after shedding = %v", err)
	}

	// Sequential accepted connections remain available indefinitely; the limit
	// bounds concurrent resource use rather than acting as a flow-wide budget.
	for range 8 {
		client, dialErr := net.Dial("tcp4", base.Addr().String())
		if dialErr != nil {
			t.Fatal(dialErr)
		}
		server, acceptErr := limited.Accept()
		if acceptErr != nil {
			_ = client.Close()
			t.Fatalf("later accept error = %v", acceptErr)
		}
		_ = server.Close()
		_ = client.Close()
	}
}

func assertSecurityHeaders(t *testing.T, header http.Header) {
	t.Helper()
	want := map[string]string{
		"Content-Type":            "text/html; charset=utf-8",
		"Cache-Control":           "no-store",
		"Content-Security-Policy": contentSecurityPolicyValue,
		"Referrer-Policy":         "no-referrer",
		"X-Content-Type-Options":  "nosniff",
		"Permissions-Policy":      permissionsPolicyValue,
	}
	for key, value := range want {
		if got := header.Get(key); got != value {
			t.Errorf("%s = %q, want %q", key, got, value)
		}
	}
}
