// Package search holds the heavy-adapter implementations of the engine
// tool.SearchProvider seam. httpsearch.go is a VENDOR-NEUTRAL HTTP JSON search
// adapter behind operator config (a base URL + optional API key/header), OFF by
// default — the harness's first outbound-network capability, so it is conservative:
//
//   - It carries its OWN per-call timeout (honoring ctx) AND a concurrency limiter,
//     because the read-parallel dispatcher fans out N concurrent WebSearch calls per
//     turn → N egress requests. Egress is bounded HERE, never in the dispatcher or
//     the tool.
//   - It passes the query VERBATIM. Secret-scanning of the query is the guardrails
//     layer's responsibility, not this adapter's — it must never inspect/rewrite the
//     query, and it never sends the configured secret as part of the query.
//
// This file graduated from internal/adapter/search into the importable engine
// module (issue #363) — it sits alongside engine/adapter/search's fakesearch.go
// (the offline reference fake) so engine consumers get working web search by import.
package search

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/sync/semaphore"

	"github.com/stacklok/mecatl/engine/tool"
)

// Defaults for the HTTP adapter's safety envelope.
const (
	// defaultTimeout bounds a single outbound search request.
	defaultTimeout = 10 * time.Second
	// defaultMaxConcurrent bounds simultaneous in-flight egress requests across the
	// process (the read-parallel dispatcher fans out concurrent WebSearch calls).
	defaultMaxConcurrent = 4
	// maxResponseBytes caps how much of a backend response we read, so a hostile or
	// broken endpoint cannot stream unbounded data into the harness.
	maxResponseBytes = 1 << 20 // 1 MiB
)

// HTTPConfig is the operator configuration for the HTTP JSON search adapter. Only
// BaseURL is required; the rest tune auth, the request method, and the safety
// envelope.
type HTTPConfig struct {
	// BaseURL is the search endpoint (e.g. a SearXNG "/search" URL or any generic
	// JSON search API). Required.
	BaseURL string
	// APIKey, when set, is sent in the AuthHeader (default "Authorization" with a
	// "Bearer " prefix). It is NEVER placed in the query string or logged.
	APIKey string
	// AuthHeader overrides the header the API key is sent in (default
	// "Authorization"). When it is "Authorization" the value is "Bearer <key>";
	// for any other header the raw key is sent (e.g. "X-API-Key").
	AuthHeader string
	// QueryParam is the URL query parameter the search string is placed in (default
	// "q"). Generic over SearXNG ("q") and most JSON search APIs.
	QueryParam string
	// Method is the HTTP method ("GET" default, or "POST"). For POST the query
	// params are sent as a form-encoded body.
	Method string
	// Timeout bounds one request (default 10s). Always also honored via ctx.
	Timeout time.Duration
	// MaxConcurrent bounds simultaneous egress (default 4). <=0 uses the default.
	MaxConcurrent int
	// HTTPClient is an injectable transport (the test seam: an httptest.Server
	// client). nil uses a default client with Timeout.
	HTTPClient *http.Client
}

// HTTPProvider is a vendor-neutral tool.SearchProvider over a JSON HTTP endpoint.
// It is safe for concurrent use; egress is bounded by an internal semaphore and a
// per-call timeout.
type HTTPProvider struct {
	cfg    HTTPConfig
	client *http.Client
	sem    *semaphore.Weighted
}

// httpResponse is the GENERIC JSON response shape this adapter parses. It is a
// deliberately permissive superset of common search-API shapes: a top-level
// "results" array of objects with title/url/snippet-style fields, accepting a few
// common field aliases so a SearXNG instance or a generic JSON search endpoint both
// parse without per-vendor code. Documented so an operator knows what to expose.
type httpResponse struct {
	Results []httpResult `json:"results"`
	// Web nests the results array the Brave Web Search API returns
	// ({"web":{"results":[...]}}) — a top-level "results" array is the SearXNG /
	// generic shape, while Brave buries its web results one level down. We merge
	// web.results into the flat results so the Brave tier actually returns hits
	// (the prior round registered the Brave endpoint but never parsed its shape).
	Web struct {
		Results []httpResult `json:"results"`
	} `json:"web"`
}

// merged returns the flat top-level results followed by any nested Brave
// web.results, so a generic SearXNG body and a Brave body both yield results.
func (r httpResponse) merged() []httpResult {
	out := make([]httpResult, 0, len(r.Results)+len(r.Web.Results))
	out = append(out, r.Results...)
	out = append(out, r.Web.Results...)
	return out
}

// httpResult is one result object. The field aliases (Content/Snippet/Description,
// PublishedDate/Date, Engine/Source) cover the common JSON search shapes.
type httpResult struct {
	Title         string `json:"title"`
	URL           string `json:"url"`
	Content       string `json:"content"`
	Snippet       string `json:"snippet"`
	Description   string `json:"description"`
	PublishedDate string `json:"publishedDate"`
	Date          string `json:"date"`
	Engine        string `json:"engine"`
	Source        string `json:"source"`
}

// NewHTTPProvider constructs an HTTPProvider from cfg, applying defaults. It returns
// an error if BaseURL is empty (a misconfigured adapter must fail loudly, not pass
// silently — the composition root only builds this when the operator set the URL).
func NewHTTPProvider(cfg HTTPConfig) (*HTTPProvider, error) {
	trimmed := strings.TrimSpace(cfg.BaseURL)
	if trimmed == "" {
		return nil, fmt.Errorf("search: HTTP provider requires a non-empty BaseURL")
	}
	// A configured-but-malformed URL must fail construction loudly (the operator
	// set a broken endpoint), so composition can route it to backend-down rather
	// than silently building a provider every Search would error on. We require an
	// absolute http/https URL with a host.
	u, err := url.Parse(trimmed)
	if err != nil {
		return nil, fmt.Errorf("search: HTTP provider BaseURL %q is not a valid URL: %w", trimmed, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("search: HTTP provider BaseURL %q must be an http(s) URL (got scheme %q)", trimmed, u.Scheme)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("search: HTTP provider BaseURL %q must include a host", trimmed)
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = defaultTimeout
	}
	if cfg.MaxConcurrent <= 0 {
		cfg.MaxConcurrent = defaultMaxConcurrent
	}
	if cfg.QueryParam == "" {
		cfg.QueryParam = "q"
	}
	if cfg.AuthHeader == "" {
		cfg.AuthHeader = "Authorization"
	}
	if cfg.Method == "" {
		cfg.Method = http.MethodGet
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: cfg.Timeout}
	}
	// Never follow redirects (CWE-918, SSRF). A JSON search API answers directly; a
	// hostile or MITM'd configured backend that 302s us toward an internal address
	// (e.g. the cloud metadata IP 169.254.169.254) must be stopped at the first hop,
	// and the Authorization header must never ride a redirect to another host. We set
	// this on the client regardless of whether it was injected, so even a test- or
	// operator-supplied client inherits the no-follow policy.
	client.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return &HTTPProvider{
		cfg:    cfg,
		client: client,
		sem:    semaphore.NewWeighted(int64(cfg.MaxConcurrent)),
	}, nil
}

// Search performs one outbound JSON search. It bounds egress with the internal
// semaphore + a per-call timeout, sends the query VERBATIM (never the secret in the
// query), parses the generic JSON response, and returns up to q.Limit results.
func (p *HTTPProvider) Search(ctx context.Context, q tool.SearchQuery) ([]tool.SearchResult, error) {
	// Bound concurrency: acquire a slot (respecting ctx cancellation) so N
	// concurrent WebSearch calls cannot launch N unbounded egress requests.
	if err := p.sem.Acquire(ctx, 1); err != nil {
		return nil, fmt.Errorf("search: acquire egress slot: %w", err)
	}
	defer p.sem.Release(1)

	// Per-call timeout on top of ctx (the adapter owns its timeout; the dispatcher
	// does not bound it).
	reqCtx, cancel := context.WithTimeout(ctx, p.cfg.Timeout)
	defer cancel()

	req, err := p.buildRequest(reqCtx, q)
	if err != nil {
		return nil, err
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("search: request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("search: backend returned HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("search: read response: %w", err)
	}

	var parsed httpResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("search: parse response JSON: %w", err)
	}

	results := parsed.merged()
	out := make([]tool.SearchResult, 0, len(results))
	for _, r := range results {
		out = append(out, tool.SearchResult{
			Title:   r.Title,
			URL:     r.URL,
			Snippet: firstNonEmpty(r.Snippet, r.Content, r.Description),
			Date:    firstNonEmpty(r.PublishedDate, r.Date),
			Source:  firstNonEmpty(r.Source, r.Engine),
		})
		if q.Limit > 0 && len(out) >= q.Limit {
			break
		}
	}
	return out, nil
}

// buildRequest constructs the HTTP request: query params (q + optional site as a
// SearXNG-style site: refinement, + freshness as a generic "time_range" param) and
// the auth header. The secret rides the HEADER only — never the query string.
func (p *HTTPProvider) buildRequest(ctx context.Context, q tool.SearchQuery) (*http.Request, error) {
	params := url.Values{}
	query := q.Query
	if q.Site != "" {
		// SearXNG-style site refinement folded into the query verbatim; harmless to
		// a generic endpoint that ignores it (it is still part of the query text).
		query = query + " site:" + q.Site
	}
	params.Set(p.cfg.QueryParam, query)
	params.Set("format", "json") // SearXNG requires it; generic endpoints ignore it.
	if q.Freshness != "" {
		params.Set("time_range", q.Freshness)
	}

	var req *http.Request
	var err error
	if strings.EqualFold(p.cfg.Method, http.MethodPost) {
		req, err = http.NewRequestWithContext(ctx, http.MethodPost, p.cfg.BaseURL, strings.NewReader(params.Encode()))
		if err == nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
	} else {
		u := p.cfg.BaseURL
		if strings.Contains(u, "?") {
			u += "&" + params.Encode()
		} else {
			u += "?" + params.Encode()
		}
		req, err = http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	}
	if err != nil {
		return nil, fmt.Errorf("search: build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if p.cfg.APIKey != "" {
		if strings.EqualFold(p.cfg.AuthHeader, "Authorization") {
			req.Header.Set("Authorization", "Bearer "+p.cfg.APIKey)
		} else {
			req.Header.Set(p.cfg.AuthHeader, p.cfg.APIKey)
		}
	}
	return req, nil
}

// BackendDown is the SearchProvider the composition root installs when an operator
// CONFIGURED a backend but its construction FAILED (a bad --websearch-url /
// SEARXNG_URL, or invalid Brave config). It is distinct from the engine's
// fakesearch.Unavailable: a construction failure means "a backend was intended but
// is not usable" — backend-down semantics — NOT the operator kill switch. Every
// Search returns tool.ErrSearchBackendDown, so the WebSearch tool surfaces the
// backend-down message (naming the upgrade path) rather than the "disabled by
// operator" message, which would mis-attribute the cause.
type BackendDown struct{}

// Search always returns tool.ErrSearchBackendDown.
func (BackendDown) Search(_ context.Context, _ tool.SearchQuery) ([]tool.SearchResult, error) {
	return nil, fmt.Errorf("search: backend misconfigured: %w", tool.ErrSearchBackendDown)
}

// Compile-time assertion that BackendDown implements tool.SearchProvider.
var _ tool.SearchProvider = BackendDown{}

// firstNonEmpty returns the first non-empty (after trim) string, or "".
func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}

// Compile-time assertion that *HTTPProvider implements tool.SearchProvider.
var _ tool.SearchProvider = (*HTTPProvider)(nil)
