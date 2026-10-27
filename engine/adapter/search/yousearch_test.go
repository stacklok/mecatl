package search

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/tool"
)

const youResultsJSON = `{"results":{"web":[
 {"url":"https://go.dev/doc/go1.26","title":"Go 1.26 Release Notes","description":"desc one","snippets":["matched passage"],"page_age":"2026-02-01T00:00:00"},
 {"url":"https://example.com/b","title":"Second","description":"desc two","snippets":[]},
 {"url":"https://example.com/c","title":"Third","description":"desc three"}
]}}`

type youRecorder struct {
	mu      sync.Mutex
	calls   int
	method  string
	path    string
	query   string
	count   string
	key     string
	ua      string
	body    map[string]any
	status  int
	headers map[string]string
	payload string
}

func newYouServer(t *testing.T) (*httptest.Server, *youRecorder) {
	t.Helper()
	rec := &youRecorder{payload: youResultsJSON}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.mu.Lock()
		defer rec.mu.Unlock()
		rec.calls++
		rec.method, rec.path = r.Method, r.URL.Path
		rec.query, rec.count = r.URL.Query().Get("query"), r.URL.Query().Get("count")
		rec.key, rec.ua = r.Header.Get("X-API-Key"), r.Header.Get("User-Agent")
		rec.body = nil
		if r.Method == http.MethodPost {
			raw, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(raw, &rec.body)
		}
		for k, v := range rec.headers {
			w.Header().Set(k, v)
		}
		if rec.status != 0 {
			w.WriteHeader(rec.status)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, rec.payload)
	}))
	t.Cleanup(srv.Close)
	return srv, rec
}

func TestYouProvider_DefaultEndpoints(t *testing.T) {
	if got := NewYouProvider(YouConfig{}).Endpoint(); got != "https://api.you.com/v1/agents/search" {
		t.Fatalf("keyless endpoint = %q", got)
	}
	p := NewYouProvider(YouConfig{APIKey: "k"})
	if got := p.Endpoint(); got != "https://ydc-index.io/v1/search" {
		t.Fatalf("keyed endpoint = %q", got)
	}
	if !p.Keyed() || NewYouProvider(YouConfig{APIKey: "  "}).Keyed() {
		t.Fatal("Keyed() must reflect a non-blank key")
	}
}

func TestYouProvider_KeylessGET(t *testing.T) {
	srv, rec := newYouServer(t)
	p := NewYouProvider(YouConfig{KeylessEndpoint: srv.URL + "/v1/agents/search", HTTPClient: srv.Client()})
	got, err := p.Search(t.Context(), tool.SearchQuery{Query: "go release notes", Limit: 2})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if rec.method != http.MethodGet || rec.path != "/v1/agents/search" {
		t.Fatalf("got %s %s, want GET /v1/agents/search", rec.method, rec.path)
	}
	if rec.query != "go release notes" || rec.count != "2" {
		t.Fatalf("query=%q count=%q", rec.query, rec.count)
	}
	if rec.key != "" {
		t.Fatal("keyless mode must not send X-API-Key")
	}
	if rec.ua != "youdotcom-integration/stacklok-mecatl" {
		t.Fatalf("User-Agent = %q", rec.ua)
	}
	if len(got) != 2 {
		t.Fatalf("limit not honored: %d results", len(got))
	}
	want := tool.SearchResult{Title: "Go 1.26 Release Notes", URL: "https://go.dev/doc/go1.26", Snippet: "matched passage", Date: "2026-02-01T00:00:00", Source: "go.dev"}
	if got[0] != want {
		t.Fatalf("result[0] = %+v, want %+v", got[0], want)
	}
	if got[1].Snippet != "desc two" {
		t.Fatalf("description fallback: %q", got[1].Snippet)
	}
}

func TestYouProvider_KeyedPOST(t *testing.T) {
	srv, rec := newYouServer(t)
	p := NewYouProvider(YouConfig{APIKey: "secret-key", KeyedEndpoint: srv.URL + "/v1/search", HTTPClient: srv.Client()})
	if _, err := p.Search(t.Context(), tool.SearchQuery{Query: "q", Limit: 5, Site: "go.dev", Freshness: "week"}); err != nil {
		t.Fatalf("Search: %v", err)
	}
	if rec.method != http.MethodPost || rec.path != "/v1/search" {
		t.Fatalf("got %s %s, want POST /v1/search", rec.method, rec.path)
	}
	if rec.key != "secret-key" {
		t.Fatal("keyed mode must send X-API-Key")
	}
	if rec.query != "" {
		t.Fatal("keyed mode must not put the query in the URL")
	}
	if rec.body["query"] != "q site:go.dev" || rec.body["count"] != float64(5) || rec.body["freshness"] != "week" {
		t.Fatalf("POST body = %v", rec.body)
	}
	if rec.ua != "youdotcom-integration/stacklok-mecatl" {
		t.Fatalf("User-Agent = %q", rec.ua)
	}
}

func TestYouProvider_CredentialErrors(t *testing.T) {
	for _, code := range []int{http.StatusUnauthorized, http.StatusPaymentRequired, http.StatusForbidden} {
		for _, keyed := range []bool{false, true} {
			srv, rec := newYouServer(t)
			rec.status = code
			cfg := YouConfig{KeylessEndpoint: srv.URL, KeyedEndpoint: srv.URL, HTTPClient: srv.Client()}
			if keyed {
				cfg.APIKey = "secret-key"
			}
			_, err := NewYouProvider(cfg).Search(t.Context(), tool.SearchQuery{Query: "q", Limit: 1})
			var ce *YouCredentialError
			if !errors.As(err, &ce) || ce.StatusCode != code {
				t.Fatalf("HTTP %d keyed=%v: err = %v, want *YouCredentialError", code, keyed, err)
			}
			if errors.Is(err, tool.ErrSearchBackendDown) {
				t.Fatalf("HTTP %d must not be reported as backend-down", code)
			}
			// The operator must be told which variable to fix, in both modes.
			if msg := err.Error(); !strings.Contains(msg, "YDC_API_KEY") {
				t.Fatalf("HTTP %d keyed=%v: message does not name YDC_API_KEY: %q", code, keyed, msg)
			}
		}
	}
}

func TestYouProvider_RateLimitHonorsRetryAfter(t *testing.T) {
	srv, rec := newYouServer(t)
	rec.status = http.StatusTooManyRequests
	rec.headers = map[string]string{"Retry-After": "120"}
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	p := NewYouProvider(YouConfig{KeylessEndpoint: srv.URL, HTTPClient: srv.Client(), Now: clock})

	_, err := p.Search(t.Context(), tool.SearchQuery{Query: "q", Limit: 1})
	var rl *YouRateLimitError
	if !errors.As(err, &rl) || rl.RetryAfter != 120*time.Second {
		t.Fatalf("err = %v, want *YouRateLimitError with 120s", err)
	}
	if !errors.Is(err, tool.ErrSearchBackendDown) {
		t.Fatal("429 must surface as backend-down")
	}

	// Inside the window: no egress at all.
	rec.status = 0
	now = now.Add(60 * time.Second)
	_, err = p.Search(t.Context(), tool.SearchQuery{Query: "q", Limit: 1})
	if !errors.As(err, &rl) || rl.RetryAfter != 60*time.Second {
		t.Fatalf("inside window: err = %v, want remaining 60s", err)
	}
	if rec.calls != 1 {
		t.Fatalf("provider contacted the endpoint during Retry-After: %d calls", rec.calls)
	}

	// After the window: egress resumes.
	now = now.Add(61 * time.Second)
	if _, err := p.Search(t.Context(), tool.SearchQuery{Query: "q", Limit: 1}); err != nil {
		t.Fatalf("after window: %v", err)
	}
	if rec.calls != 2 {
		t.Fatalf("calls = %d, want 2", rec.calls)
	}
}

func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	cases := map[string]time.Duration{
		"":        youDefaultRetryAfter,
		"garbage": youDefaultRetryAfter,
		"0":       youDefaultRetryAfter,
		"5":       5 * time.Second,
		"999999":  youMaxRetryAfter,
		now.Add(90 * time.Second).Format(http.TimeFormat): 90 * time.Second,
	}
	for in, want := range cases {
		if got := parseRetryAfter(in, now); got != want {
			t.Errorf("parseRetryAfter(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestYouProvider_BackendDown(t *testing.T) {
	cases := map[string]func(*youRecorder){
		"5xx":       func(r *youRecorder) { r.status = http.StatusBadGateway },
		"malformed": func(r *youRecorder) { r.payload = "not json" },
		"empty":     func(r *youRecorder) { r.payload = `{"results":{"web":[]}}` },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			srv, rec := newYouServer(t)
			mutate(rec)
			_, err := NewYouProvider(YouConfig{KeylessEndpoint: srv.URL, HTTPClient: srv.Client()}).
				Search(t.Context(), tool.SearchQuery{Query: "q", Limit: 1})
			if !errors.Is(err, tool.ErrSearchBackendDown) {
				t.Fatalf("err = %v, want ErrSearchBackendDown", err)
			}
		})
	}
}

// TestYouProvider_NoRedirects redirects to a second server and asserts it is
// never hit. It runs in keyed mode, so it also shows X-API-Key never reaches
// the redirect target.
func TestYouProvider_NoRedirects(t *testing.T) {
	var targetHit atomic.Bool
	var targetKey atomic.Value
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetHit.Store(true)
		targetKey.Store(r.Header.Get("X-API-Key"))
		_, _ = io.WriteString(w, youResultsJSON)
	}))
	t.Cleanup(target.Close)

	srv, rec := newYouServer(t)
	rec.status = http.StatusTemporaryRedirect // 307 would replay the POST body and headers
	rec.headers = map[string]string{"Location": target.URL + "/v1/search"}
	_, err := NewYouProvider(YouConfig{APIKey: "secret-key", KeyedEndpoint: srv.URL, HTTPClient: srv.Client()}).
		Search(t.Context(), tool.SearchQuery{Query: "q", Limit: 1})
	if !errors.Is(err, tool.ErrSearchBackendDown) {
		t.Fatalf("unfollowed redirect should be backend-down, got %v", err)
	}
	if targetHit.Load() {
		t.Fatalf("redirect was followed (target saw X-API-Key=%q)", targetKey.Load())
	}
	if rec.calls != 1 || rec.key != "secret-key" {
		t.Fatalf("origin calls=%d key sent=%v", rec.calls, rec.key == "secret-key")
	}
}

// TestYouProvider_QueuedCallersRespect429 queues callers on the semaphore
// behind a request that comes back 429. None of them may reach the endpoint
// once the cooldown starts.
func TestYouProvider_QueuedCallersRespect429(t *testing.T) {
	const callers = 12
	var calls atomic.Int32
	entered := make(chan struct{})
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			close(entered)
			<-release
		}
		w.Header().Set("Retry-After", "120")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	t.Cleanup(srv.Close)
	p := NewYouProvider(YouConfig{KeylessEndpoint: srv.URL, HTTPClient: srv.Client(), MaxConcurrent: 1, Timeout: 5 * time.Second})

	errs := make([]error, callers)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, errs[0] = p.Search(context.Background(), tool.SearchQuery{Query: "q", Limit: 1})
	}()
	<-entered
	for i := 1; i < callers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = p.Search(context.Background(), tool.SearchQuery{Query: "q", Limit: 1})
		}(i)
	}
	// Give the other callers time to park on the semaphore before the 429 lands.
	time.Sleep(100 * time.Millisecond)
	close(release)
	wg.Wait()

	if n := calls.Load(); n != 1 {
		t.Fatalf("%d requests reached the rate-limited endpoint, want 1", n)
	}
	for i, err := range errs {
		var rl *YouRateLimitError
		if !errors.As(err, &rl) {
			t.Fatalf("caller %d: err = %v, want *YouRateLimitError", i, err)
		}
	}
}

// TestYouProvider_BlockNeverShortensCooldown asserts a shorter Retry-After
// cannot cut a running cooldown short.
func TestYouProvider_BlockNeverShortensCooldown(t *testing.T) {
	now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	p := NewYouProvider(YouConfig{Now: func() time.Time { return now }})
	p.block(120 * time.Second)
	p.block(10 * time.Second)
	if got := p.cooldown(); got != 120*time.Second {
		t.Fatalf("cooldown = %s after a shorter block, want 120s", got)
	}
	p.block(300 * time.Second)
	if got := p.cooldown(); got != 300*time.Second {
		t.Fatalf("cooldown = %s after a longer block, want 300s", got)
	}
}

// TestYouProvider_RateLimitHint asserts the 429 detail the WebSearch tool
// shows the model carries the status and Retry-After, and the keyless quota.
func TestYouProvider_RateLimitHint(t *testing.T) {
	var hint tool.SearchBackendHint
	err := error(&YouRateLimitError{RetryAfter: 42 * time.Second})
	if !errors.As(err, &hint) {
		t.Fatal("YouRateLimitError must implement tool.SearchBackendHint")
	}
	h := hint.SearchBackendHint()
	for _, want := range []string{"429", "retry after 42s", "100 searches a day per IP", "YDC_API_KEY"} {
		if !strings.Contains(h, want) {
			t.Fatalf("hint missing %q: %q", want, h)
		}
	}
	if keyed := (&YouRateLimitError{RetryAfter: time.Second, Keyed: true}).SearchBackendHint(); strings.Contains(keyed, "keyless") {
		t.Fatalf("keyed hint should not mention the keyless tier: %q", keyed)
	}
}

// TestYouProvider_ConcurrencyBounded ports TestHTTPProviderConcurrencyBounded.
func TestYouProvider_ConcurrencyBounded(t *testing.T) {
	const maxConcurrent = 4
	const callers = 12
	var inFlight, peak atomic.Int32
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := inFlight.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		<-release
		inFlight.Add(-1)
		_, _ = io.WriteString(w, youResultsJSON)
	}))
	t.Cleanup(srv.Close)
	p := NewYouProvider(YouConfig{KeylessEndpoint: srv.URL, HTTPClient: srv.Client(), MaxConcurrent: maxConcurrent, Timeout: 5 * time.Second})

	var wg sync.WaitGroup
	errs := make([]error, callers)
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = p.Search(context.Background(), tool.SearchQuery{Query: "q", Limit: 1})
		}(i)
	}
	waitForInFlight(t, &inFlight, maxConcurrent)
	if got := inFlight.Load(); got > maxConcurrent {
		t.Fatalf("%d requests in flight, bound is %d", got, maxConcurrent)
	}
	close(release)
	wg.Wait()
	if pk := peak.Load(); pk != maxConcurrent {
		t.Fatalf("peak concurrency %d, want exactly %d", pk, maxConcurrent)
	}
	for i, err := range errs {
		if err != nil {
			t.Fatalf("caller %d: %v", i, err)
		}
	}
}

// TestYouProvider_PerCallTimeout ports TestHTTPProviderPerCallTimeout.
func TestYouProvider_PerCallTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	t.Cleanup(srv.Close)
	p := NewYouProvider(YouConfig{KeylessEndpoint: srv.URL, HTTPClient: srv.Client(), Timeout: 50 * time.Millisecond})
	start := time.Now()
	_, err := p.Search(context.Background(), tool.SearchQuery{Query: "q", Limit: 1})
	if !errors.Is(err, tool.ErrSearchBackendDown) {
		t.Fatalf("err = %v, want ErrSearchBackendDown", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("per-call timeout did not fire promptly")
	}
}

// TestYouProvider_ContextCancel ports TestHTTPProviderContextCancel.
func TestYouProvider_ContextCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	t.Cleanup(srv.Close)
	p := NewYouProvider(YouConfig{KeylessEndpoint: srv.URL, HTTPClient: srv.Client()})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := p.Search(ctx, tool.SearchQuery{Query: "q", Limit: 1}); err == nil {
		t.Fatal("expected an error after ctx cancellation")
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("ctx cancellation did not abort the request promptly")
	}
}

// TestYouProvider_OversizedResponse ports TestHTTPProviderOversizedResponse:
// the body read is capped, and the truncated body is backend-down.
func TestYouProvider_OversizedResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"results":{"web":[{"url":"https://x.example","title":"x","description":"`)
		_, _ = io.WriteString(w, strings.Repeat("A", 2<<20))
		_, _ = io.WriteString(w, `"}]}}`)
	}))
	t.Cleanup(srv.Close)
	_, err := NewYouProvider(YouConfig{KeylessEndpoint: srv.URL, HTTPClient: srv.Client()}).
		Search(context.Background(), tool.SearchQuery{Query: "q", Limit: 1})
	if !errors.Is(err, tool.ErrSearchBackendDown) {
		t.Fatalf("oversized body: err = %v, want ErrSearchBackendDown", err)
	}
}
