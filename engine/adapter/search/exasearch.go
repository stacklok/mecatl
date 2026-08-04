package search

// This file (exasearch.go) is the TIER-1 default tool.SearchProvider (issue #26): a
// MINIMAL, dedicated streamable-HTTP JSON-RPC MCP client for Exa's anonymous
// web-search endpoint (https://mcp.exa.ai/mcp → tools/call web_search_exa). It works
// zero-config with no credentials — the last keyless general-web option standing —
// so the harness has web search ON by default; an EXA_API_KEY upgrades it to the
// paid tier.
//
// Why a dedicated client and NOT the general MCP manager: a WebSearch backend is an
// EPHEMERAL, per-call search — not a long-lived MCP server registered into the tool
// catalog. Routing it through the manager would (a) register web_search_exa /
// web_fetch_exa as CATALOG tools (the wrong surface — WebSearch is a single core
// tool over a SearchProvider, not a fleet of MCP tools) and (b) tie the search to
// the manager's connection lifecycle. A dedicated minimal client keeps it a plain
// tool.SearchProvider and keeps the Exa-specific result-shape parsing (the
// content[].text blobs) local to this file.
//
// This client deliberately does NO OAuth discovery. Exa's endpoint advertises OAuth
// metadata at /.well-known/oauth-protected-resource (→ auth.exa.ai) but does not
// ENFORCE it, so anonymous calls work. A proactive-OAuth client — a ToolHive-style
// workload that adopts this endpoint, or any MCP client that follows the advertised
// metadata — WOULD discover that document and risk parking on a browser auth flow.
// We never follow it: we stay anonymous and never park. Concretely, this client
// makes EXACTLY three POSTs to the fixed endpoint per call — initialize,
// notifications/initialized, tools/call — and NOTHING ELSE: it NEVER issues a GET,
// and NEVER touches a /.well-known/ path (a load-bearing invariant the WE-don't-
// discover stance enforces, asserted by exasearch_test.go).
//
// Like httpsearch.go it carries its OWN per-call timeout + concurrency limiter (the
// read-parallel dispatcher fans out N concurrent WebSearch calls per turn), caps the
// response body, never follows redirects, and passes the query VERBATIM.

import (
	"bytes"
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

// defaultExaEndpoint is the fixed Exa streamable-HTTP MCP endpoint. The client
// never contacts any other URL (modulo the secret-bearing query param the paid
// tier appends to THIS endpoint).
const defaultExaEndpoint = "https://mcp.exa.ai/mcp"

// exaToolName is the Exa MCP tool the client invokes.
const exaToolName = "web_search_exa"

// mcpSessionHeader is the response header the Exa initialize call returns and the
// subsequent calls must echo back.
const mcpSessionHeader = "Mcp-Session-Id"

// ExaConfig configures the Exa anonymous-default search provider. Only the
// zero-value is needed for the anonymous default; the fields tune the endpoint,
// the optional paid-tier key, and the safety envelope.
type ExaConfig struct {
	// Endpoint overrides the Exa MCP endpoint (default https://mcp.exa.ai/mcp).
	Endpoint string
	// APIKey, when set (from EXA_API_KEY), upgrades to the paid tier: it is
	// appended to the endpoint as the ?exaApiKey= query parameter (Exa's documented
	// scheme). The assembled URL is SECRET-BEARING and is NEVER logged or passed to
	// diagnostics — only the fixed base endpoint + a boolean "paid tier" flag are.
	APIKey string
	// Timeout bounds one outbound call across all three POSTs (default 10s). Always
	// also honored via ctx.
	Timeout time.Duration
	// MaxConcurrent bounds simultaneous in-flight egress across the process
	// (default 4). <=0 uses the default.
	MaxConcurrent int
	// HTTPClient is an injectable transport (the test seam: an httptest.Server
	// client). nil uses a default client with Timeout.
	HTTPClient *http.Client
}

// ExaProvider is a tool.SearchProvider over Exa's anonymous streamable-HTTP MCP
// endpoint. It is safe for concurrent use; egress is bounded by an internal
// semaphore and a per-call timeout.
type ExaProvider struct {
	endpoint string // the (possibly secret-bearing) request URL — NEVER logged
	base     string // the fixed base endpoint, log-safe
	paidTier bool
	timeout  time.Duration
	client   *http.Client
	sem      *semaphore.Weighted
}

// NewExaProvider constructs an ExaProvider from cfg, applying defaults. It never
// returns an error: the anonymous default needs no configuration, and a missing
// key simply leaves the client on the free tier.
func NewExaProvider(cfg ExaConfig) *ExaProvider {
	base := strings.TrimSpace(cfg.Endpoint)
	if base == "" {
		base = defaultExaEndpoint
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
	// Never follow redirects (CWE-918, SSRF) — same posture as the HTTP adapter.
	client.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}

	endpoint := base
	paid := false
	if key := strings.TrimSpace(cfg.APIKey); key != "" {
		// SECRET-BEARING URL: the paid-tier key rides the ?exaApiKey= query param
		// (Exa's documented scheme), built via net/url so it is escaped. This URL
		// must NEVER be logged or handed to diagnostics — only `base` + `paidTier`.
		if u, err := url.Parse(base); err == nil {
			vals := u.Query()
			vals.Set("exaApiKey", key)
			u.RawQuery = vals.Encode()
			endpoint = u.String()
			paid = true
		}
	}

	return &ExaProvider{
		endpoint: endpoint,
		base:     base,
		paidTier: paid,
		timeout:  cfg.Timeout,
		client:   client,
		sem:      semaphore.NewWeighted(int64(cfg.MaxConcurrent)),
	}
}

// BaseEndpoint returns the fixed, LOG-SAFE base endpoint (never the secret-bearing
// request URL). PaidTier reports whether an API key is in use. Composition uses
// these to narrate without ever logging the key.
func (p *ExaProvider) BaseEndpoint() string { return p.base }

// PaidTier reports whether an EXA_API_KEY is configured (paid tier).
func (p *ExaProvider) PaidTier() bool { return p.paidTier }

// Search performs one Exa web search via the three-POST MCP handshake. Any closed/
// auth/rate-limit/5xx status, transport error, JSON-RPC error object, or
// malformed/empty result is mapped to tool.ErrSearchBackendDown (wrapped with
// context) — the mandatory-degradation signal.
func (p *ExaProvider) Search(ctx context.Context, q tool.SearchQuery) ([]tool.SearchResult, error) {
	if err := p.sem.Acquire(ctx, 1); err != nil {
		return nil, fmt.Errorf("search: acquire egress slot: %w", err)
	}
	defer p.sem.Release(1)

	reqCtx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	// (1) initialize — capture the Mcp-Session-Id response header.
	sessionID, err := p.initialize(reqCtx)
	if err != nil {
		return nil, err
	}

	// (2) notifications/initialized — spec-correct, sent with the session header.
	if err := p.notifyInitialized(reqCtx, sessionID); err != nil {
		return nil, err
	}

	// (3) tools/call web_search_exa — the actual search.
	text, err := p.callSearch(reqCtx, sessionID, q)
	if err != nil {
		return nil, err
	}

	out := make([]tool.SearchResult, 0, len(text))
	for _, t := range text {
		out = append(out, parseExaResultText(t))
		if q.Limit > 0 && len(out) >= q.Limit {
			break
		}
	}
	if len(out) == 0 {
		// Empty result from a reachable endpoint is treated as a backend-down
		// degradation (never a silent empty result), per the mandatory-degradation
		// contract.
		return nil, fmt.Errorf("search: exa returned no results: %w", tool.ErrSearchBackendDown)
	}
	return out, nil
}

// jsonRPCRequest is a minimal JSON-RPC 2.0 request/notification envelope. ID is a
// pointer so a notification (no id) omits it.
type jsonRPCRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      *int   `json:"id,omitempty"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

// jsonRPCResponse is the JSON-RPC response shape we parse: an error object (any
// non-nil error → backend-down) and a permissive result carrying Exa's
// content[].text blobs.
type jsonRPCResponse struct {
	Error  *jsonRPCError `json:"error"`
	Result struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"result"`
}

type jsonRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// initialize POSTs the JSON-RPC initialize request and returns the session id from
// the Mcp-Session-Id response header.
func (p *ExaProvider) initialize(ctx context.Context) (string, error) {
	id := 1
	body := jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      &id,
		Method:  "initialize",
		Params: map[string]any{
			"protocolVersion": "2025-03-26",
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "mecatl", "version": "1"},
		},
	}
	resp, raw, err := p.post(ctx, body, "")
	if err != nil {
		return "", err
	}
	// Parse for a JSON-RPC error even on initialize (a refused handshake).
	if _, perr := parseJSONRPC(raw, resp.Header.Get("Content-Type")); perr != nil {
		return "", perr
	}
	sessionID := resp.Header.Get(mcpSessionHeader)
	return sessionID, nil
}

// notifyInitialized POSTs the notifications/initialized notification (no id).
func (p *ExaProvider) notifyInitialized(ctx context.Context, sessionID string) error {
	body := jsonRPCRequest{
		JSONRPC: "2.0",
		Method:  "notifications/initialized",
	}
	// A notification has no response body to parse; we only care that the POST
	// succeeded (post maps a non-2xx/transport fault to backend-down).
	if _, _, err := p.post(ctx, body, sessionID); err != nil {
		return err
	}
	return nil
}

// callSearch POSTs tools/call web_search_exa and returns the content[].text blobs.
func (p *ExaProvider) callSearch(ctx context.Context, sessionID string, q tool.SearchQuery) ([]string, error) {
	id := 2
	body := jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      &id,
		Method:  "tools/call",
		Params: map[string]any{
			"name": exaToolName,
			"arguments": map[string]any{
				"query":      q.Query,
				"numResults": q.Limit,
			},
		},
	}
	resp, raw, err := p.post(ctx, body, sessionID)
	if err != nil {
		return nil, err
	}
	parsed, perr := parseJSONRPC(raw, resp.Header.Get("Content-Type"))
	if perr != nil {
		return nil, perr
	}
	texts := make([]string, 0, len(parsed.Result.Content))
	for _, c := range parsed.Result.Content {
		if strings.TrimSpace(c.Text) != "" {
			texts = append(texts, c.Text)
		}
	}
	return texts, nil
}

// post issues ONE POST to the fixed endpoint with the JSON-RPC envelope and the
// optional session header. It maps a non-2xx status / transport error to
// tool.ErrSearchBackendDown and returns the (capped) raw body for parsing.
func (p *ExaProvider) post(ctx context.Context, payload jsonRPCRequest, sessionID string) (*http.Response, []byte, error) {
	buf, err := json.Marshal(payload)
	if err != nil {
		return nil, nil, fmt.Errorf("search: marshal %s: %w", payload.Method, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint, bytes.NewReader(buf))
	if err != nil {
		return nil, nil, fmt.Errorf("search: build %s request: %w", payload.Method, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if sessionID != "" {
		req.Header.Set(mcpSessionHeader, sessionID)
	}

	resp, err := p.client.Do(req)
	if err != nil {
		// Transport error (unreachable, DNS, TLS, ctx) → backend-down degradation.
		return nil, nil, fmt.Errorf("search: exa %s request failed: %w: %v", payload.Method, tool.ErrSearchBackendDown, err)
	}
	body, rerr := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	_ = resp.Body.Close()
	if rerr != nil {
		return resp, nil, fmt.Errorf("search: read exa %s response: %w: %v", payload.Method, tool.ErrSearchBackendDown, rerr)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// 401/403/429/5xx (and any other non-2xx) → backend-down degradation.
		return resp, body, fmt.Errorf("search: exa %s returned HTTP %d: %w", payload.Method, resp.StatusCode, tool.ErrSearchBackendDown)
	}
	return resp, body, nil
}

// parseJSONRPC parses a JSON-RPC response, tolerating SSE framing. When the
// Content-Type is text/event-stream the body is run through parseSSEData first;
// otherwise it is parsed as raw JSON (the plain-JSON fallback). A JSON-RPC error
// object or unparseable body maps to tool.ErrSearchBackendDown.
func parseJSONRPC(body []byte, contentType string) (jsonRPCResponse, error) {
	data := body
	if strings.Contains(strings.ToLower(contentType), "text/event-stream") {
		d, err := parseSSEData(body)
		if err != nil {
			return jsonRPCResponse{}, fmt.Errorf("search: parse exa SSE: %w: %v", tool.ErrSearchBackendDown, err)
		}
		data = d
	}
	var resp jsonRPCResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return jsonRPCResponse{}, fmt.Errorf("search: parse exa JSON-RPC: %w: %v", tool.ErrSearchBackendDown, err)
	}
	if resp.Error != nil {
		return jsonRPCResponse{}, fmt.Errorf("search: exa JSON-RPC error %d %q: %w", resp.Error.Code, resp.Error.Message, tool.ErrSearchBackendDown)
	}
	return resp, nil
}

// parseSSEData extracts and concatenates the data: payload(s) from an SSE-framed
// body per the SSE spec: data: lines are joined with newlines, event:/id:/retry:/
// comment (lines starting with ":") lines are ignored. It returns the joined data
// bytes for the (single) event we care about; multiple events' data are joined.
//
// This is a SECOND, intentionally minimal SSE parser — it deliberately does NOT
// reuse the openai adapter's decodeSSE (internal/adapter/openai/stream.go): that one
// is unexported, bound to the OpenAI Responses event union, and lives in the root
// module (unimportable from engine/). A tiny field-level parser here is the right
// scope for a single JSON-RPC-over-SSE response, not an oversight.
func parseSSEData(body []byte) ([]byte, error) {
	var data []string
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}
		field, value, found := strings.Cut(line, ":")
		if !found {
			// A bare field name with no colon — no value; ignore.
			continue
		}
		// Per spec, a single leading space after the colon is stripped.
		value = strings.TrimPrefix(value, " ")
		if field == "data" {
			data = append(data, value)
		}
		// event:/id:/retry: and any other field are ignored.
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("no data lines in SSE body")
	}
	return []byte(strings.Join(data, "\n")), nil
}

// parseExaResultText parses one Exa content[].text blob into a SearchResult. The
// blob is line-prefixed metadata ("Title: ...", "URL: ...", "Published: ...",
// "Author: ...", "Highlights: ..."). Parsing is TOLERANT of missing fields and
// multi-line Highlights (joined with "; "); a blob with no recognised structure
// becomes the Snippet whole, so a result is NEVER dropped.
//
// This is deliberately tolerant prose-prefix parsing, NOT a strict schema: Exa
// returns human-shaped text, not a stable JSON object, so the field prefixes could
// change. The whole-text-to-Snippet fallback makes a format change a KNOWN, handled
// graceful degradation (the model still gets the text, just less structured) rather
// than a silent drop or a parse panic.
func parseExaResultText(text string) tool.SearchResult {
	var res tool.SearchResult
	var highlights []string
	var unstructured []string
	inHighlights := false

	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		switch {
		case strings.HasPrefix(trimmed, "Title:"):
			res.Title = strings.TrimSpace(strings.TrimPrefix(trimmed, "Title:"))
			inHighlights = false
		case strings.HasPrefix(trimmed, "URL:"):
			res.URL = strings.TrimSpace(strings.TrimPrefix(trimmed, "URL:"))
			inHighlights = false
		case strings.HasPrefix(trimmed, "Published:"):
			res.Date = strings.TrimSpace(strings.TrimPrefix(trimmed, "Published:"))
			inHighlights = false
		case strings.HasPrefix(trimmed, "Author:"):
			res.Source = strings.TrimSpace(strings.TrimPrefix(trimmed, "Author:"))
			inHighlights = false
		case strings.HasPrefix(trimmed, "Highlights:"):
			h := strings.TrimSpace(strings.TrimPrefix(trimmed, "Highlights:"))
			if h != "" {
				highlights = append(highlights, h)
			}
			inHighlights = true
		default:
			if inHighlights {
				highlights = append(highlights, trimmed)
			} else {
				unstructured = append(unstructured, trimmed)
			}
		}
	}

	if len(highlights) > 0 {
		res.Snippet = strings.Join(highlights, "; ")
	}
	// If nothing structured was recognised, the whole blob is the snippet (never
	// drop a result).
	if res.Title == "" && res.URL == "" && res.Snippet == "" && res.Date == "" && res.Source == "" {
		res.Snippet = strings.TrimSpace(text)
	} else if res.Snippet == "" && len(unstructured) > 0 {
		res.Snippet = strings.Join(unstructured, "; ")
	}
	return res
}

// Compile-time assertion that *ExaProvider implements tool.SearchProvider.
var _ tool.SearchProvider = (*ExaProvider)(nil)
