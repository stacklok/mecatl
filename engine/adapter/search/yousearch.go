package search

// This file (yousearch.go) is an OPT-IN tool.SearchProvider over You.com's search
// API (issue #2441). It is never the default: composition selects it only when the
// operator asks for it, and anonymous Exa stays the zero-config default.
//
// It has two modes over two FIXED endpoints and contacts no other URL:
//
//   - keyless:  GET  https://api.you.com/v1/agents/search?query=...&count=...
//   - keyed:    POST https://ydc-index.io/v1/search with the key in X-API-Key
//
// Both return plain JSON (results.web[] with url/title/description/snippets/
// page_age), so this is a small HTTP adapter rather than an MCP client. Like
// httpsearch.go and exasearch.go it carries its OWN per-call timeout and
// concurrency limiter, caps the response body, never follows redirects, and
// passes the query VERBATIM (a Site refinement is appended as " site:<domain>",
// the same way the HTTP adapter does it).
//
// Status mapping:
//
//   - 401/403 (and 402, "payment required" on an exhausted plan) are CREDENTIAL
//     errors: a *YouCredentialError, which is NOT tool.ErrSearchBackendDown,
//     so the tool reports a real failure the operator has to fix instead of a
//     transient outage.
//   - 429 is a *YouRateLimitError that wraps tool.ErrSearchBackendDown and
//     carries the parsed Retry-After. The provider then refuses further egress
//     until Retry-After has elapsed, so a fan-out of concurrent WebSearch calls
//     does not keep hammering a rate-limited endpoint.
//   - any other non-2xx, transport fault, or malformed/empty body maps to
//     tool.ErrSearchBackendDown.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/semaphore"

	"github.com/stacklok/mecatl/engine/tool"
)

const (
	// defaultYouKeylessEndpoint is the keyless (no account) You.com search endpoint.
	defaultYouKeylessEndpoint = "https://api.you.com/v1/agents/search"
	// defaultYouKeyedEndpoint is the keyed You.com search endpoint (X-API-Key).
	defaultYouKeyedEndpoint = "https://ydc-index.io/v1/search"
	// youAPIKeyHeader carries the YDC_API_KEY on the keyed endpoint.
	youAPIKeyHeader = "X-API-Key" //nolint:gosec // G101: a header name, not a credential.
	// youUserAgent identifies this integration on every outbound call.
	youUserAgent = "youdotcom-integration/stacklok-mecatl"
	// youDefaultRetryAfter is the cooldown used when a 429 carries no usable
	// Retry-After header.
	youDefaultRetryAfter = 30 * time.Second
	// youMaxRetryAfter bounds a hostile or broken Retry-After so one response
	// cannot disable search for the life of the process.
	youMaxRetryAfter = 1 * time.Hour
)

// YouConfig configures the You.com search provider. The zero value is the keyless
// mode against the fixed keyless endpoint.
type YouConfig struct {
	// APIKey, when set (from YDC_API_KEY), switches to the keyed endpoint and is
	// sent in the X-API-Key header. It is NEVER placed in a URL or logged.
	APIKey string
	// KeylessEndpoint overrides the keyless endpoint (test seam).
	KeylessEndpoint string
	// KeyedEndpoint overrides the keyed endpoint (test seam).
	KeyedEndpoint string
	// Timeout bounds one outbound call (default 10s). Always also honored via ctx.
	Timeout time.Duration
	// MaxConcurrent bounds simultaneous in-flight egress (default 4).
	MaxConcurrent int
	// HTTPClient is an injectable transport (the test seam). nil uses a default
	// client with Timeout.
	HTTPClient *http.Client
	// Now is the clock used for the Retry-After cooldown (test seam). nil uses
	// time.Now.
	Now func() time.Time
}

// YouProvider is a tool.SearchProvider over the You.com search API. It is safe
// for concurrent use.
type YouProvider struct {
	endpoint string
	apiKey   string // SECRET — never logged, never in a URL
	timeout  time.Duration
	client   *http.Client
	sem      *semaphore.Weighted
	now      func() time.Time

	mu           sync.Mutex
	blockedUntil time.Time
}

// YouCredentialError reports that You.com rejected the request's credentials or
// plan (HTTP 401, 402, or 403). It deliberately does NOT wrap
// tool.ErrSearchBackendDown: retrying will not help until the operator fixes
// YDC_API_KEY or the plan.
type YouCredentialError struct {
	StatusCode int
	Keyed      bool
}

func (e *YouCredentialError) Error() string {
	switch {
	case e.StatusCode == http.StatusPaymentRequired:
		return fmt.Sprintf("search: you.com returned HTTP %d: the plan or credits for YDC_API_KEY are exhausted", e.StatusCode)
	case e.Keyed:
		return fmt.Sprintf("search: you.com returned HTTP %d: YDC_API_KEY was rejected", e.StatusCode)
	default:
		return fmt.Sprintf("search: you.com returned HTTP %d: the keyless endpoint refused the request; set YDC_API_KEY to use the keyed endpoint", e.StatusCode)
	}
}

// YouRateLimitError reports HTTP 429 (or a call refused locally because an
// earlier 429's Retry-After has not elapsed). It wraps
// tool.ErrSearchBackendDown, so the WebSearch tool renders it as the transient
// "temporarily unavailable, do not retry immediately" message, and implements
// tool.SearchBackendHint so that message carries the 429 and Retry-After.
type YouRateLimitError struct {
	RetryAfter time.Duration
	Keyed      bool
}

func (e *YouRateLimitError) Error() string {
	msg := fmt.Sprintf("search: you.com rate limited; retry after %s", e.RetryAfter.Round(time.Second))
	if !e.Keyed {
		msg += " (keyless tier; set YDC_API_KEY for higher limits)"
	}
	return msg
}

// Unwrap makes errors.Is(err, tool.ErrSearchBackendDown) true.
func (*YouRateLimitError) Unwrap() error { return tool.ErrSearchBackendDown }

// SearchBackendHint implements tool.SearchBackendHint so the 429 and its
// Retry-After reach the model through the shared backend-down message.
func (e *YouRateLimitError) SearchBackendHint() string {
	msg := fmt.Sprintf("You.com returned HTTP 429 (rate limited); retry after %s.", e.RetryAfter.Round(time.Second))
	if !e.Keyed {
		msg += " The keyless tier allows 100 searches a day per IP; set YDC_API_KEY for higher limits."
	}
	return msg
}

// NewYouProvider constructs a YouProvider, applying defaults. It never returns an
// error: keyless mode needs no configuration.
func NewYouProvider(cfg YouConfig) *YouProvider {
	key := strings.TrimSpace(cfg.APIKey)
	endpoint := strings.TrimSpace(cfg.KeylessEndpoint)
	if endpoint == "" {
		endpoint = defaultYouKeylessEndpoint
	}
	if key != "" {
		endpoint = strings.TrimSpace(cfg.KeyedEndpoint)
		if endpoint == "" {
			endpoint = defaultYouKeyedEndpoint
		}
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = defaultTimeout
	}
	if cfg.MaxConcurrent <= 0 {
		cfg.MaxConcurrent = defaultMaxConcurrent
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: cfg.Timeout}
	}
	// Never follow redirects (CWE-918, SSRF), same posture as the other adapters.
	client.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	return &YouProvider{
		endpoint: endpoint,
		apiKey:   key,
		timeout:  cfg.Timeout,
		client:   client,
		sem:      semaphore.NewWeighted(int64(cfg.MaxConcurrent)),
		now:      now,
	}
}

// Endpoint returns the LOG-SAFE endpoint in use (the key is never part of it).
func (p *YouProvider) Endpoint() string { return p.endpoint }

// Keyed reports whether a YDC_API_KEY is configured.
func (p *YouProvider) Keyed() bool { return p.apiKey != "" }

// Search runs one You.com search.
func (p *YouProvider) Search(ctx context.Context, q tool.SearchQuery) ([]tool.SearchResult, error) {
	if wait := p.cooldown(); wait > 0 {
		return nil, &YouRateLimitError{RetryAfter: wait, Keyed: p.Keyed()}
	}
	if err := p.sem.Acquire(ctx, 1); err != nil {
		return nil, fmt.Errorf("search: acquire egress slot: %w", err)
	}
	defer p.sem.Release(1)
	// Check again now that we hold a slot: a caller queued on the semaphore
	// while another caller received a 429 must not send its request anyway.
	if wait := p.cooldown(); wait > 0 {
		return nil, &YouRateLimitError{RetryAfter: wait, Keyed: p.Keyed()}
	}

	reqCtx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	req, err := p.newRequest(reqCtx, q)
	if err != nil {
		return nil, fmt.Errorf("search: build you.com request: %w", tool.ErrSearchBackendDown)
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("search: you.com request failed: %w", tool.ErrSearchBackendDown)
	}
	body, rerr := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	_ = resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusUnauthorized,
		resp.StatusCode == http.StatusPaymentRequired,
		resp.StatusCode == http.StatusForbidden:
		return nil, &YouCredentialError{StatusCode: resp.StatusCode, Keyed: p.Keyed()}
	case resp.StatusCode == http.StatusTooManyRequests:
		wait := parseRetryAfter(resp.Header.Get("Retry-After"), p.now())
		p.block(wait)
		return nil, &YouRateLimitError{RetryAfter: wait, Keyed: p.Keyed()}
	case resp.StatusCode < 200 || resp.StatusCode >= 300:
		return nil, fmt.Errorf("search: you.com returned HTTP %d: %w", resp.StatusCode, tool.ErrSearchBackendDown)
	}
	if rerr != nil {
		return nil, fmt.Errorf("search: read you.com response: %w: %v", tool.ErrSearchBackendDown, rerr)
	}

	out, err := parseYouResults(body, q.Limit)
	if err != nil {
		return nil, err
	}
	if len(out) == 0 {
		// A reachable endpoint with no results is backend-down, never a silent
		// empty result (the mandatory-degradation contract).
		return nil, fmt.Errorf("search: you.com returned no results: %w", tool.ErrSearchBackendDown)
	}
	return out, nil
}

// newRequest builds the keyless GET or the keyed POST.
func (p *YouProvider) newRequest(ctx context.Context, q tool.SearchQuery) (*http.Request, error) {
	query := q.Query
	if q.Site != "" {
		query = query + " site:" + q.Site
	}
	var req *http.Request
	if p.Keyed() {
		payload := map[string]any{"query": query}
		if q.Limit > 0 {
			payload["count"] = q.Limit
		}
		if q.Freshness != "" {
			payload["freshness"] = q.Freshness
		}
		buf, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		req, err = http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint, bytes.NewReader(buf))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set(youAPIKeyHeader, p.apiKey)
	} else {
		u, err := url.Parse(p.endpoint)
		if err != nil {
			return nil, err
		}
		vals := u.Query()
		vals.Set("query", query)
		if q.Limit > 0 {
			vals.Set("count", strconv.Itoa(q.Limit))
		}
		if q.Freshness != "" {
			vals.Set("freshness", q.Freshness)
		}
		u.RawQuery = vals.Encode()
		req, err = http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		if err != nil {
			return nil, err
		}
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", youUserAgent)
	return req, nil
}

// cooldown returns how long remains on an active Retry-After window.
func (p *YouProvider) cooldown() time.Duration {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.blockedUntil.IsZero() {
		return 0
	}
	if wait := p.blockedUntil.Sub(p.now()); wait > 0 {
		return wait
	}
	p.blockedUntil = time.Time{}
	return 0
}

// block extends the Retry-After window; it never shortens an existing one.
func (p *YouProvider) block(wait time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	until := p.now().Add(wait)
	if until.After(p.blockedUntil) {
		p.blockedUntil = until
	}
}

// parseRetryAfter parses a Retry-After header (delta-seconds or HTTP-date),
// falling back to youDefaultRetryAfter and clamping to youMaxRetryAfter.
func parseRetryAfter(v string, now time.Time) time.Duration {
	v = strings.TrimSpace(v)
	wait := time.Duration(0)
	if v != "" {
		if secs, err := strconv.Atoi(v); err == nil {
			wait = time.Duration(secs) * time.Second
		} else if t, err := http.ParseTime(v); err == nil {
			wait = t.Sub(now)
		}
	}
	if wait <= 0 {
		wait = youDefaultRetryAfter
	}
	if wait > youMaxRetryAfter {
		wait = youMaxRetryAfter
	}
	return wait
}

// youResponse is the subset of the You.com response this adapter reads. Both
// endpoints share it.
type youResponse struct {
	Results struct {
		Web []struct {
			URL         string   `json:"url"`
			Title       string   `json:"title"`
			Description string   `json:"description"`
			Snippets    []string `json:"snippets"`
			PageAge     string   `json:"page_age"`
		} `json:"web"`
	} `json:"results"`
}

// parseYouResults maps results.web[] onto tool.SearchResult, never exceeding
// limit. The first snippet is preferred over the description because it is the
// passage that matched the query.
func parseYouResults(body []byte, limit int) ([]tool.SearchResult, error) {
	var parsed youResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("search: parse you.com JSON: %w: %v", tool.ErrSearchBackendDown, err)
	}
	out := make([]tool.SearchResult, 0, len(parsed.Results.Web))
	for _, r := range parsed.Results.Web {
		if strings.TrimSpace(r.URL) == "" && strings.TrimSpace(r.Title) == "" {
			continue
		}
		snippet := r.Description
		for _, s := range r.Snippets {
			if strings.TrimSpace(s) != "" {
				snippet = s
				break
			}
		}
		res := tool.SearchResult{Title: r.Title, URL: r.URL, Snippet: snippet, Date: r.PageAge}
		if u, err := url.Parse(r.URL); err == nil {
			res.Source = u.Hostname()
		}
		out = append(out, res)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}

// Compile-time assertion that *YouProvider implements tool.SearchProvider.
var _ tool.SearchProvider = (*YouProvider)(nil)
