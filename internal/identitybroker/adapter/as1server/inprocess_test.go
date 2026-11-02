package as1server

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// stubServer is a Server whose handler is h, for exercising the in-process
// transport without ToolHive.
func stubServer(t *testing.T, h http.Handler) *Server {
	t.Helper()
	issuer, err := url.Parse("https://as1.mecatl.test" + testIssuerPath)
	if err != nil {
		t.Fatal(err)
	}
	return &Server{issuerID: issuer.String(), issuer: issuer, handler: h}
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

func TestInProcessClientServesTheHandler(t *testing.T) {
	server := stubServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("X-Path", r.URL.Path)
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write(append([]byte("got "), body...))
	}))
	body := newCloseRecorder("ping")
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, server.Issuer()+"/oauth/token", body)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := server.InProcessClient().Transport.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	got, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusTeapot || string(got) != "got ping" || resp.Header.Get("X-Path") != testIssuerPath+"/oauth/token" {
		t.Errorf("response: status %d body %q path %q", resp.StatusCode, got, resp.Header.Get("X-Path"))
	}
	select {
	case <-body.closed:
	default:
		t.Error("request body not closed")
	}
}

func TestInProcessClientStopsWaitingAtTheDeadline(t *testing.T) {
	release := make(chan struct{})
	handlerDone := make(chan struct{})
	server := stubServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		defer close(handlerDone)
		<-release
		_, _ = w.Write([]byte("late"))
	}))
	t.Cleanup(func() {
		close(release)
		<-handlerDone
	})
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	body := newCloseRecorder("ping")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, server.Issuer()+"/oauth/token", body)
	if err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	resp, err := server.InProcessClient().Do(req)
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

func TestInProcessClientClosesTheBodyWhenAbandoned(t *testing.T) {
	release := make(chan struct{})
	server := stubServer(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
	ctx, cancel := context.WithCancel(t.Context())
	body := newCloseRecorder("ping")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, server.Issuer()+"/oauth/token", body)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	if _, err := server.InProcessClient().Transport.RoundTrip(req); !errors.Is(err, context.Canceled) {
		t.Fatalf("RoundTrip: %v, want context.Canceled", err)
	}
	close(release)
	select {
	case <-body.closed:
	case <-time.After(5 * time.Second):
		t.Fatal("abandoned handler did not close the request body")
	}
}

func TestInProcessClientClosesARefusedBody(t *testing.T) {
	server := stubServer(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("handler served a refused request")
	}))
	body := newCloseRecorder("ping")
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "https://other.mecatl.test"+testIssuerPath+"/oauth/token", body)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.InProcessClient().Transport.RoundTrip(req); err == nil || !strings.Contains(err.Error(), "outside the issuer") {
		t.Fatalf("RoundTrip: %v, want refused", err)
	}
	select {
	case <-body.closed:
	default:
		t.Error("refused request body not closed")
	}
}
