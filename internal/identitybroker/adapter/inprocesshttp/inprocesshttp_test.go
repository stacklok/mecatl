package inprocesshttp

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	testServed  = "https://as.mecatl.test/as/oauth/token"
	testRefused = "https://other.mecatl.test/as/oauth/token"
)

var errRefused = errors.New("refused")

// newTestClient serves h for testServed's host and refuses every other.
func newTestClient(h http.Handler) *http.Client {
	return NewClient(h, func(u *url.URL) error {
		if u.Host != "as.mecatl.test" {
			return errRefused
		}
		return nil
	})
}

// closeRecorder is a request body that reports its Close.
type closeRecorder struct {
	io.Reader
	closed chan struct{}
}

func newCloseRecorder(s string) *closeRecorder {
	return &closeRecorder{Reader: strings.NewReader(s), closed: make(chan struct{})}
}

func (c *closeRecorder) Close() error {
	select {
	case <-c.closed:
	default:
		close(c.closed)
	}
	return nil
}

func TestClientServesTheHandler(t *testing.T) {
	client := newTestClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("X-Path", r.URL.Path)
		w.Header().Set("X-Host", r.Host)
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write(append([]byte("got "), body...))
	}))
	body := newCloseRecorder("ping")
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, testServed, body)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = "spoofed.mecatl.test"
	resp, err := client.Transport.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	got, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusTeapot || string(got) != "got ping" ||
		resp.Header.Get("X-Path") != "/as/oauth/token" || resp.Header.Get("X-Host") != "as.mecatl.test" {
		t.Errorf("response: status %d body %q path %q host %q", resp.StatusCode, got, resp.Header.Get("X-Path"), resp.Header.Get("X-Host"))
	}
	select {
	case <-body.closed:
	default:
		t.Error("request body not closed")
	}
}

func TestClientDoesNotFollowRedirects(t *testing.T) {
	client := newTestClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, testServed+"/next", http.StatusFound)
	}))
	resp, err := client.Get(testServed)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Errorf("status %d, want the unfollowed redirect", resp.StatusCode)
	}
}

func TestClientStopsWaitingAtTheDeadline(t *testing.T) {
	release := make(chan struct{})
	handlerDone := make(chan struct{})
	client := newTestClient(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		defer close(handlerDone)
		<-release
		_, _ = w.Write([]byte("late"))
	}))
	t.Cleanup(func() {
		close(release)
		<-handlerDone
	})
	client.Timeout = 50 * time.Millisecond
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, testServed, newCloseRecorder("ping"))
	if err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	resp, err := client.Do(req)
	if resp != nil {
		_ = resp.Body.Close()
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Do: %v, want context.DeadlineExceeded", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("Do returned after %v", elapsed)
	}
}

func TestClientClosesTheBodyWhenAbandoned(t *testing.T) {
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	client := newTestClient(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
	ctx, cancel := context.WithCancel(t.Context())
	body := newCloseRecorder("ping")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, testServed, body)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	if _, err := client.Transport.RoundTrip(req); !errors.Is(err, context.Canceled) {
		t.Fatalf("RoundTrip: %v, want context.Canceled", err)
	}
	unblock()
	select {
	case <-body.closed:
	case <-time.After(5 * time.Second):
		t.Fatal("abandoned handler did not close the request body")
	}
}

func TestClientClosesARefusedBody(t *testing.T) {
	client := newTestClient(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("handler served a refused request")
	}))
	body := newCloseRecorder("ping")
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, testRefused, body)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Transport.RoundTrip(req); !errors.Is(err, errRefused) {
		t.Fatalf("RoundTrip: %v, want refused", err)
	}
	select {
	case <-body.closed:
	default:
		t.Error("refused request body not closed")
	}
}

func TestClientReraisesAHandlerPanic(t *testing.T) {
	client := newTestClient(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("handler failed") }))
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, testServed, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if got := recover(); got != "handler failed" {
			t.Errorf("recovered %v, want the handler's panic", got)
		}
	}()
	resp, err := client.Transport.RoundTrip(req)
	if err == nil {
		_ = resp.Body.Close()
	}
	t.Fatalf("RoundTrip returned %v, want a panic", err)
}
