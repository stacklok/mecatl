package inprocesshttp

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

// newTestStreamingClient serves h for testServed's host and refuses every other.
func newTestStreamingClient(h http.Handler) *http.Client {
	return NewStreamingClient(h, func(u *url.URL) error {
		if u.Host != "as.mecatl.test" {
			return errRefused
		}
		return nil
	})
}

// waitClosed fails t unless ch closes within a few seconds.
func waitClosed(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

func TestStreamingClientDeliversEventsBeforeTheHandlerReturns(t *testing.T) {
	release, returned := make(chan struct{}), make(chan struct{})
	client := newTestStreamingClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(returned)
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("X-Host", r.Host)
		_, _ = io.WriteString(w, "data: first\n\n")
		w.(http.Flusher).Flush()
		<-release
		w.Header().Set("X-Late", "ignored")
		_, _ = io.WriteString(w, "data: second\n\n")
	}))
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, testServed, strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "text/event-stream" || resp.Header.Get("X-Host") != "as.mecatl.test" {
		t.Fatalf("response %d %v; want 200 with the handler's headers", resp.StatusCode, resp.Header)
	}
	first := make([]byte, len("data: first\n\n"))
	if _, err := io.ReadFull(resp.Body, first); err != nil || string(first) != "data: first\n\n" {
		t.Fatalf("first event = %q, %v", first, err)
	}
	select {
	case <-returned:
		t.Fatal("the handler returned before the test released it")
	default:
	}
	close(release)
	rest, err := io.ReadAll(resp.Body)
	if err != nil || string(rest) != "data: second\n\n" {
		t.Fatalf("rest = %q, %v; want the second event then EOF", rest, err)
	}
	waitClosed(t, returned, "the handler to return")
	if resp.Header.Get("X-Late") != "" {
		t.Error("a header set after the first write reached the response")
	}
}

// TestStreamingClientSendsAReplyWhenTheHandlerReturns shows an unflushed
// reply is sent only once the handler returns, so work it does after writing
// finishes before the caller sees the reply.
func TestStreamingClientSendsAReplyWhenTheHandlerReturns(t *testing.T) {
	var stored bool
	client := newTestStreamingClient(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Mcp-Session-Id", "s1")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":1,"result":{}}`)
		time.Sleep(10 * time.Millisecond)
		stored = true
	}))
	resp, err := client.Get(testServed)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	// Checked before the body is read: reading to EOF waits for the handler
	// anyway.
	if !stored || resp.Header.Get("Mcp-Session-Id") != "s1" {
		t.Fatalf("stored=%v session %q; want the handler finished before the reply arrived", stored, resp.Header.Get("Mcp-Session-Id"))
	}
	if _, err := io.ReadAll(resp.Body); err != nil {
		t.Fatal(err)
	}
	big := strings.Repeat("x", streamingBufferSize)
	release := make(chan struct{})
	client = newTestStreamingClient(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, big)
		<-release
	}))
	resp, err = client.Get(testServed)
	if err != nil {
		t.Fatalf("Get a full buffer: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	got := make([]byte, len(big))
	if _, err := io.ReadFull(resp.Body, got); err != nil {
		t.Fatalf("read a full buffer before the handler returns: %v", err)
	}
	close(release)
}

func TestStreamingClientCancelStopsTheHandler(t *testing.T) {
	t.Run("after the response", func(t *testing.T) {
		stopped := make(chan struct{})
		client := newTestStreamingClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer close(stopped)
			w.WriteHeader(http.StatusAccepted)
			w.(http.Flusher).Flush()
			<-r.Context().Done()
		}))
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, testServed, nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("Do: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusAccepted {
			t.Fatalf("status %d, want 202", resp.StatusCode)
		}
		cancel()
		waitClosed(t, stopped, "the handler to see the cancellation")
		if _, err := io.ReadAll(resp.Body); !errors.Is(err, context.Canceled) {
			t.Fatalf("body read after cancel = %v, want context.Canceled", err)
		}
	})
	t.Run("before the response", func(t *testing.T) {
		stopped := make(chan struct{})
		client := newTestStreamingClient(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			defer close(stopped)
			<-r.Context().Done()
		}))
		ctx, cancel := context.WithCancel(t.Context())
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, testServed, nil)
		if err != nil {
			t.Fatal(err)
		}
		time.AfterFunc(10*time.Millisecond, cancel)
		if _, err := client.Do(req); !errors.Is(err, context.Canceled) {
			t.Fatalf("Do = %v, want context.Canceled", err)
		}
		waitClosed(t, stopped, "the handler to see the cancellation")
	})
	t.Run("closing the body", func(t *testing.T) {
		stopped := make(chan struct{})
		client := newTestStreamingClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer close(stopped)
			w.(http.Flusher).Flush()
			<-r.Context().Done()
		}))
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, testServed, nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("Do: %v", err)
		}
		_ = resp.Body.Close()
		waitClosed(t, stopped, "the handler to stop after the body closed")
	})
}

func TestStreamingClientDropsAnAbortedResponse(t *testing.T) {
	client := newTestStreamingClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "partial")
		if r.URL.Query().Get("flushed") != "" {
			w.(http.Flusher).Flush()
			_, _ = io.WriteString(w, " never sent")
		}
		panic(http.ErrAbortHandler)
	}))
	if _, err := client.Get(testServed); !errors.Is(err, errHandlerAborted) {
		t.Fatalf("abort before the response was sent: %v, want errHandlerAborted", err)
	}
	resp, err := client.Get(testServed + "?flushed=1")
	if err != nil {
		t.Fatalf("abort after the response was sent: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if body, err := io.ReadAll(resp.Body); !errors.Is(err, io.ErrUnexpectedEOF) || string(body) != "partial" {
		t.Fatalf("body = %q, %v; want the partial write then io.ErrUnexpectedEOF", body, err)
	}
}

func TestStreamingClientRefusesUnadmittedURLs(t *testing.T) {
	client := newTestStreamingClient(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("the handler served a refused URL")
	}))
	body := newCloseRecorder("x")
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, testRefused, body)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Do(req); !errors.Is(err, errRefused) {
		t.Fatalf("Do = %v, want the admit error", err)
	}
	waitClosed(t, body.closed, "the refused request's body to close")
}
