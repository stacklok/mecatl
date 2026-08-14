package oauthlogin

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestFixedBrowserArgv(t *testing.T) {
	tests := []struct {
		goos string
		name string
		args []string
	}{
		{goos: "linux", name: "xdg-open", args: []string{"https://example.test/a?state=x;y"}},
		{goos: "freebsd", name: "xdg-open", args: []string{"https://example.test/a?state=x;y"}},
		{goos: "darwin", name: "open", args: []string{"https://example.test/a?state=x;y"}},
		{goos: "windows", name: "rundll32.exe", args: []string{"url.dll,FileProtocolHandler", "https://example.test/a?state=x;y"}},
	}
	for _, test := range tests {
		t.Run(test.goos, func(t *testing.T) {
			calls := 0
			err := launchBrowser(context.Background(), test.goos, test.args[len(test.args)-1], func(_ context.Context, name string, args ...string) error {
				calls++
				if name != test.name || fmt.Sprint(args) != fmt.Sprint(test.args) {
					t.Fatalf("command = %q %q, want %q %q", name, args, test.name, test.args)
				}
				return nil
			})
			if err != nil || calls != 1 {
				t.Fatalf("error = %v, calls = %d", err, calls)
			}
		})
	}
}

func TestBrowserFailureIsTypedRedactedAndDoesNotFallback(t *testing.T) {
	const (
		authorizationURL = "https://as.example.test/authorize?state=browser-secret"
		processCanary    = "process-output-secret"
	)
	calls := 0
	err := launchBrowser(context.Background(), "linux", authorizationURL, func(context.Context, string, ...string) error {
		calls++
		return errors.New(processCanary)
	})
	if !errors.Is(err, ErrBrowserLaunch) || calls != 1 {
		t.Fatalf("error = %v, calls = %d", err, calls)
	}
	if strings.Contains(err.Error(), authorizationURL) || strings.Contains(err.Error(), "browser-secret") || strings.Contains(err.Error(), processCanary) {
		t.Fatalf("browser error leaked sensitive data: %v", err)
	}
	if !strings.Contains(err.Error(), "no-browser") {
		t.Fatalf("browser error lacks remediation: %v", err)
	}
}

func TestBrowserLaunchCancellationRemainsDiscoverable(t *testing.T) {
	started := make(chan struct{})
	runtime, err := New(Options{Launcher: launcherFunc(func(ctx context.Context, _ string) error {
		close(started)
		<-ctx.Done()
		return errors.New("browser cancellation output")
	})})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- runtime.Authorize(ctx, testIssuer, func(ctx context.Context, _ string, present func(context.Context, string) (Result, error)) error {
			_, err := present(ctx, "https://as.example.test/authorize?state=secret-state")
			return err
		})
	}()
	<-started
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
}

func TestRuntimeBrowserFailureNeverWritesURL(t *testing.T) {
	var output bytes.Buffer
	runtime, err := New(Options{
		URLWriter: &output,
		Launcher:  launcherFunc(func(context.Context, string) error { return errors.New("browser output canary") }),
	})
	if err != nil {
		t.Fatal(err)
	}
	err = runtime.Authorize(context.Background(), testIssuer, func(ctx context.Context, _ string, present func(context.Context, string) (Result, error)) error {
		_, err := present(ctx, "https://as.example.test/authorize?state=secret-state")
		return err
	})
	if !errors.Is(err, ErrBrowserLaunch) {
		t.Fatalf("error = %v", err)
	}
	if output.Len() != 0 {
		t.Fatalf("unexpected fallback output: %q", output.String())
	}
}

func TestNoBrowserRequiresWriterAndPrintsURLExactlyOnce(t *testing.T) {
	if _, err := New(Options{NoBrowser: true}); err == nil {
		t.Fatal("New accepted no-browser mode without writer")
	}

	const authorizationURL = "https://as.example.test/authorize?state=terminal-secret"
	var output bytes.Buffer
	var redirect string
	runtime, err := New(Options{
		NoBrowser: true,
		URLWriter: &output,
		Launcher: launcherFunc(func(context.Context, string) error {
			t.Fatal("launcher called in no-browser mode")
			return nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	err = runtime.Authorize(context.Background(), testIssuer, func(ctx context.Context, got string, present func(context.Context, string) (Result, error)) error {
		redirect = got
		go func() {
			req, _ := http.NewRequest(http.MethodGet, callbackURL(redirect, "c", "terminal-secret", testIssuer), nil)
			resp, requestErr := loopbackClient().Do(req)
			if requestErr == nil {
				_ = resp.Body.Close()
			}
		}()
		_, err := present(ctx, authorizationURL)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(output.String(), authorizationURL) != 1 {
		t.Fatalf("URL output = %q", output.String())
	}
	if !strings.Contains(output.String(), "callback must reach this machine") {
		t.Fatalf("warning missing: %q", output.String())
	}
}

func TestNoBrowserWriterFailureIsRedacted(t *testing.T) {
	const canary = "writer-secret-canary"
	runtime, err := New(Options{NoBrowser: true, URLWriter: failingWriter{err: errors.New(canary)}})
	if err != nil {
		t.Fatal(err)
	}
	err = runtime.Authorize(context.Background(), testIssuer, func(ctx context.Context, _ string, present func(context.Context, string) (Result, error)) error {
		_, err := present(ctx, "https://as.example.test/authorize?state=url-secret")
		return err
	})
	if err == nil || strings.Contains(err.Error(), canary) || strings.Contains(err.Error(), "url-secret") {
		t.Fatalf("unsafe writer error = %v", err)
	}
}

type failingWriter struct{ err error }

func (w failingWriter) Write([]byte) (int, error) { return 0, w.err }
