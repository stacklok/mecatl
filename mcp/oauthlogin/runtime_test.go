package oauthlogin

import (
	"bytes"
	"context"
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
		"wrong issuer": func(redirect string) *http.Request {
			req, _ := http.NewRequest(http.MethodGet, callbackURL(redirect, "c", "s", "https://other.example.test"), nil)
			return req
		},
		"missing issuer": func(redirect string) *http.Request {
			req, _ := http.NewRequest(http.MethodGet, redirect+"?code=c&state=s", nil)
			return req
		},
		"missing code": func(redirect string) *http.Request {
			req, _ := http.NewRequest(http.MethodGet, redirect+"?state=s&iss="+url.QueryEscape(testIssuer), nil)
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
		"code and OAuth error": func(redirect string) *http.Request {
			req, _ := http.NewRequest(http.MethodGet, callbackURL(redirect, "c", "s", testIssuer)+"&error=denied", nil)
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

func TestOAuthErrorIsTerminalAndRedacted(t *testing.T) {
	const description = "description-secret-canary"
	var redirect string
	launcher := launcherFunc(func(_ context.Context, _ string) error {
		values := url.Values{"error": {"access_denied"}, "error_description": {description}, "state": {"s"}, "iss": {testIssuer}}
		req, _ := http.NewRequest(http.MethodGet, redirect+"?"+values.Encode(), nil)
		response := request(t, req)
		if response.body != failureHTML {
			t.Fatalf("body = %q", response.body)
		}
		return nil
	})
	err := runWithLauncher(t, launcher, func(ctx context.Context, got string, present func(context.Context, string) (Result, error)) error {
		redirect = got
		_, err := present(ctx, "https://as.example.test/authorize?state=s")
		return err
	})
	if !errors.Is(err, ErrAuthorizationFailed) {
		t.Fatalf("error = %v", err)
	}
	if strings.Contains(err.Error(), description) || strings.Contains(err.Error(), "access_denied") {
		t.Fatalf("error leaked provider response: %v", err)
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
		// Raw TCP accepts and requests without the random callback capability are
		// untrusted ambient loopback traffic, not authorization attempts.
		for range 2 * maxRequestAttempts {
			conn, dialErr := net.Dial("tcp4", parsed.Host)
			if dialErr != nil {
				return dialErr
			}
			_ = conn.Close()

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
