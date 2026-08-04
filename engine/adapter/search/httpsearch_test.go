package search

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/tool"
)

// cannedJSON is a generic JSON search response (SearXNG-style) the adapter parses.
const cannedJSON = `{
  "results": [
    {"title": "Go 1.26", "url": "https://go.dev/1", "content": "release notes", "publishedDate": "2026-01-01", "engine": "ddg"},
    {"title": "Second",  "url": "https://go.dev/2", "snippet": "snip two", "source": "bing"}
  ]
}`

// TestHTTPProviderParsesResults drives the adapter against a LOCAL httptest.Server
// (NEVER a live endpoint — offline purity) and asserts the generic JSON is parsed
// into SearchResult, the query/site/freshness reach the backend, and the secret
// rides the header, never the query string.
func TestHTTPProviderParsesResults(t *testing.T) {
	var gotURL string
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotURL = r.URL.String()
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(cannedJSON))
	}))
	defer srv.Close()

	p, err := NewHTTPProvider(HTTPConfig{
		BaseURL:    srv.URL,
		APIKey:     "s3cr3t",
		HTTPClient: srv.Client(),
	})
	if err != nil {
		t.Fatalf("NewHTTPProvider: %v", err)
	}

	results, err := p.Search(context.Background(), tool.SearchQuery{
		Query: "go 1.26", Limit: 5, Site: "go.dev", Freshness: "week",
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d: %+v", len(results), results)
	}
	if results[0].Title != "Go 1.26" || results[0].URL != "https://go.dev/1" {
		t.Fatalf("result 0 mismatch: %+v", results[0])
	}
	// Field aliasing: content→Snippet, publishedDate→Date, engine→Source.
	if results[0].Snippet != "release notes" || results[0].Date != "2026-01-01" || results[0].Source != "ddg" {
		t.Fatalf("alias mapping failed: %+v", results[0])
	}
	// Secret in the header, NEVER the query string.
	if gotAuth != "Bearer s3cr3t" {
		t.Fatalf("expected Bearer auth header, got %q", gotAuth)
	}
	if strings.Contains(gotURL, "s3cr3t") {
		t.Fatalf("secret leaked into the query string: %q", gotURL)
	}
	// Query + site + freshness reached the backend.
	if !strings.Contains(gotURL, "go") || !strings.Contains(gotURL, "site") || !strings.Contains(gotURL, "time_range=week") {
		t.Fatalf("query params missing: %q", gotURL)
	}
}

// braveNestedJSON is a Brave Web Search API response: results are nested under
// {"web":{"results":[...]}}, NOT a top-level "results" array.
const braveNestedJSON = `{
  "web": {
    "results": [
      {"title": "Brave One", "url": "https://example.com/1", "description": "first hit"},
      {"title": "Brave Two", "url": "https://example.com/2", "description": "second hit"}
    ]
  }
}`

// TestHTTPProviderParsesBraveNestedResults asserts the adapter parses the Brave
// {"web":{"results":[...]}} shape (the prior round registered the Brave endpoint
// but never parsed its nested body, so it returned zero hits).
func TestHTTPProviderParsesBraveNestedResults(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(braveNestedJSON))
	}))
	defer srv.Close()
	p, _ := NewHTTPProvider(HTTPConfig{BaseURL: srv.URL, HTTPClient: srv.Client()})
	results, err := p.Search(context.Background(), tool.SearchQuery{Query: "x", Limit: 5})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 nested Brave results, got %d: %+v", len(results), results)
	}
	if results[0].Title != "Brave One" || results[0].URL != "https://example.com/1" || results[0].Snippet != "first hit" {
		t.Fatalf("brave nested result 0 mismatch: %+v", results[0])
	}
}

// TestHTTPProviderLimitClamp asserts the adapter honors q.Limit (never exceeds it),
// even when the backend returns more.
func TestHTTPProviderLimitClamp(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(cannedJSON))
	}))
	defer srv.Close()
	p, _ := NewHTTPProvider(HTTPConfig{BaseURL: srv.URL, HTTPClient: srv.Client()})
	results, err := p.Search(context.Background(), tool.SearchQuery{Query: "x", Limit: 1})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("limit not honored: got %d results", len(results))
	}
}

// TestHTTPProvider5xxError maps a backend 5xx to a non-nil error.
func TestHTTPProvider5xxError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()
	p, _ := NewHTTPProvider(HTTPConfig{BaseURL: srv.URL, HTTPClient: srv.Client()})
	if _, err := p.Search(context.Background(), tool.SearchQuery{Query: "x", Limit: 5}); err == nil {
		t.Fatal("expected an error on a 5xx backend response")
	}
}

// TestHTTPProviderContextCancel asserts ctx cancellation aborts the request (the
// adapter honors ctx in addition to its own timeout).
func TestHTTPProviderContextCancel(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done() // hold until the client cancels
		close(block)
	}))
	defer srv.Close()
	p, _ := NewHTTPProvider(HTTPConfig{BaseURL: srv.URL, HTTPClient: srv.Client()})

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := p.Search(ctx, tool.SearchQuery{Query: "x", Limit: 5})
	if err == nil {
		t.Fatal("expected a context-deadline error")
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("ctx cancellation did not abort the request promptly")
	}
}

// TestHTTPProviderRequiresBaseURL asserts a missing BaseURL is a loud construction
// error (a misconfigured adapter must not pass silently).
func TestHTTPProviderRequiresBaseURL(t *testing.T) {
	if _, err := NewHTTPProvider(HTTPConfig{}); err == nil {
		t.Fatal("expected an error for an empty BaseURL")
	}
}

// TestHTTPProviderRejectsMalformedURL asserts a configured-but-invalid BaseURL is a
// loud construction error (it has a host-bearing http(s) form requirement), so
// composition can route it to backend-down rather than silently building a provider
// every Search would fail on.
func TestHTTPProviderRejectsMalformedURL(t *testing.T) {
	for _, bad := range []string{
		"://missing-scheme",        // parse error
		"ftp://example.com/search", // wrong scheme
		"not-a-url",                // no scheme, no host
		"https://",                 // no host
	} {
		if _, err := NewHTTPProvider(HTTPConfig{BaseURL: bad}); err == nil {
			t.Fatalf("expected a construction error for malformed BaseURL %q", bad)
		}
	}
}

// TestBackendDownYieldsBackendDown pins the construction-failure sentinel: a
// misconfigured backend resolves to BackendDown, whose Search reports
// ErrSearchBackendDown (NOT ErrSearchUnavailable) — so the WebSearch tool shows the
// backend-down message naming the upgrade path, never the operator-disabled message.
func TestBackendDownYieldsBackendDown(t *testing.T) {
	_, err := BackendDown{}.Search(context.Background(), tool.SearchQuery{Query: "anything"})
	if !errors.Is(err, tool.ErrSearchBackendDown) {
		t.Fatalf("BackendDown.Search should return ErrSearchBackendDown, got %v", err)
	}
	if errors.Is(err, tool.ErrSearchUnavailable) {
		t.Fatalf("BackendDown.Search must NOT return ErrSearchUnavailable (that is the disabled cause), got %v", err)
	}
}

// TestHTTPProviderDoesNotFollowRedirects is the SSRF guard (CWE-918): a backend that
// 302-redirects the harness toward another address must NOT be followed. The adapter
// sees the redirect response itself (a non-2xx → error); the redirect target handler
// must NEVER be reached, so the Authorization header cannot ride a same-host or
// cross-host redirect to an internal endpoint (e.g. the cloud metadata IP).
func TestHTTPProviderDoesNotFollowRedirects(t *testing.T) {
	var targetHit atomic.Bool
	// The "internal" target the redirect points at — it must never be reached.
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		targetHit.Store(true)
		_, _ = w.Write([]byte(cannedJSON))
	}))
	defer target.Close()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Redirect(w, &http.Request{}, target.URL, http.StatusFound) // 302 → target
	}))
	defer srv.Close()

	p, _ := NewHTTPProvider(HTTPConfig{BaseURL: srv.URL, APIKey: "s3cr3t", HTTPClient: srv.Client()})
	_, err := p.Search(context.Background(), tool.SearchQuery{Query: "x", Limit: 5})
	// The adapter sees the 302 as a non-2xx response → error (it does not chase it).
	if err == nil {
		t.Fatal("expected an error from the unfollowed 302 redirect, got nil")
	}
	if targetHit.Load() {
		t.Fatal("SSRF: the adapter followed the redirect and hit the internal target")
	}
}

// TestHTTPProviderMalformedJSON asserts an invalid JSON body surfaces a non-nil error
// (the json.Unmarshal path), not a silent empty result set.
func TestHTTPProviderMalformedJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("this is not json {{{"))
	}))
	defer srv.Close()
	p, _ := NewHTTPProvider(HTTPConfig{BaseURL: srv.URL, HTTPClient: srv.Client()})
	if _, err := p.Search(context.Background(), tool.SearchQuery{Query: "x", Limit: 5}); err == nil {
		t.Fatal("expected a parse error for a malformed JSON body")
	}
}

// TestHTTPProviderOversizedResponse asserts a body that exceeds the 1 MiB LimitReader
// cap is handled WITHOUT crashing: the read is bounded, and because the truncated body
// is no longer valid JSON the adapter returns a parse error rather than reading
// unbounded data into the harness.
func TestHTTPProviderOversizedResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// A well-formed JSON prefix followed by ~2 MiB of padding inside a string —
		// past the 1 MiB LimitReader cap, so the body is truncated mid-stream.
		_, _ = w.Write([]byte(`{"results":[{"title":"x","content":"`))
		_, _ = w.Write([]byte(strings.Repeat("A", 2<<20)))
		_, _ = w.Write([]byte(`"}]}`))
	}))
	defer srv.Close()
	p, _ := NewHTTPProvider(HTTPConfig{BaseURL: srv.URL, HTTPClient: srv.Client()})
	// Must not panic / hang; the truncated body fails to parse → error (handled).
	if _, err := p.Search(context.Background(), tool.SearchQuery{Query: "x", Limit: 5}); err == nil {
		t.Fatal("expected a parse error for the truncated (capped) oversized body")
	}
}

// TestHTTPProviderConcurrencyBounded exercises the semaphore under contention: with
// MaxConcurrent=4 and a blocking backend, firing 12 concurrent Search calls must never
// have more than 4 in flight at once (and all must complete). Run with -race to catch
// a dropped Acquire/Release. Guards the egress-bounding invariant.
func TestHTTPProviderConcurrencyBounded(t *testing.T) {
	const maxConcurrent = 4
	const callers = 12

	var inFlight atomic.Int32
	var peak atomic.Int32
	release := make(chan struct{})
	var gate sync.WaitGroup
	gate.Add(callers)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := inFlight.Add(1)
		// Track the high-water mark of simultaneous in-flight requests.
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		gate.Done() // signal this request has entered the handler
		<-release   // hold until the test releases all of them
		inFlight.Add(-1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(cannedJSON))
	}))
	defer srv.Close()

	p, _ := NewHTTPProvider(HTTPConfig{
		BaseURL: srv.URL, MaxConcurrent: maxConcurrent, HTTPClient: srv.Client(),
		Timeout: 5 * time.Second,
	})

	var wg sync.WaitGroup
	errs := make([]error, callers)
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			_, errs[idx] = p.Search(context.Background(), tool.SearchQuery{Query: "x", Limit: 1})
		}(i)
	}

	// Wait until exactly maxConcurrent handlers are parked (the semaphore is holding
	// the rest), then verify the peak never exceeded the bound while still blocked.
	waitForInFlight(t, &inFlight, maxConcurrent)
	if got := inFlight.Load(); got > maxConcurrent {
		t.Fatalf("more than %d requests in flight under the semaphore: %d", maxConcurrent, got)
	}
	close(release)
	wg.Wait()

	if pk := peak.Load(); pk > maxConcurrent {
		t.Fatalf("peak concurrency %d exceeded the bound %d", pk, maxConcurrent)
	}
	if pk := peak.Load(); pk < maxConcurrent {
		t.Fatalf("expected the semaphore to be saturated at %d; peak was only %d", maxConcurrent, pk)
	}
	for i, err := range errs {
		if err != nil {
			t.Fatalf("caller %d failed: %v", i, err)
		}
	}
}

// waitForInFlight blocks until at least want requests are parked in the handler, or
// fails the test after a generous deadline (guards against a dropped Acquire that
// would never let the bound be reached).
func waitForInFlight(t *testing.T, inFlight *atomic.Int32, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if int(inFlight.Load()) >= want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d concurrent in-flight requests (saw %d)", want, inFlight.Load())
}

// TestHTTPProviderPerCallTimeout asserts the adapter's OWN timeout fires even when
// the caller's ctx has no deadline (egress bounding lives in the adapter).
func TestHTTPProviderPerCallTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer srv.Close()
	p, _ := NewHTTPProvider(HTTPConfig{BaseURL: srv.URL, HTTPClient: srv.Client(), Timeout: 50 * time.Millisecond})
	start := time.Now()
	if _, err := p.Search(context.Background(), tool.SearchQuery{Query: "x", Limit: 5}); err == nil {
		t.Fatal("expected a per-call-timeout error")
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("per-call timeout did not fire promptly")
	}
}
