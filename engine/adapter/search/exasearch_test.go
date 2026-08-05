package search

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stacklok/mecatl/engine/tool"
)

// exaSearchText is one canned Exa content[].text blob in the line-prefixed shape
// the real endpoint returns.
const exaSearchText = "Title: Go 1.26 Release Notes\n" +
	"URL: https://go.dev/doc/go1.26\n" +
	"Published: 2026-02-01\n" +
	"Author: The Go Team\n" +
	"Highlights: Generics improvements;\n" +
	"new stdlib helpers"

// exaMockServer is a configurable httptest.Server that speaks the Exa MCP
// protocol. It records every request path (for the .well-known no-discovery
// assertion) and the session id echoed on the notifications/initialized + tools/
// call requests.
type exaMockServer struct {
	srv *httptest.Server

	mu              sync.Mutex
	paths           []string
	gotInitialized  bool
	initializedSess string
	callSess        string
	gotQuery        string
	gotNumResults   int

	// knobs
	statusCode   int    // override status for tools/call (0 = 200)
	contentType  string // "" → text/event-stream; "json" → application/json
	rpcError     bool   // emit a JSON-RPC error object on tools/call
	malformed    bool   // emit a non-JSON-RPC body on tools/call
	empty        bool   // emit an empty content array
	multiText    bool   // emit multiple content[].text blobs
	multiLineSSE bool   // frame the SSE payload across ≥2 data: lines
	initRPCError bool   // emit a JSON-RPC error object on the INITIALIZE response
}

func newExaMockServer(t *testing.T) *exaMockServer {
	t.Helper()
	m := &exaMockServer{}
	m.srv = httptest.NewServer(http.HandlerFunc(m.handle))
	t.Cleanup(m.srv.Close)
	return m
}

func (m *exaMockServer) handle(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var req struct {
		Method string `json:"method"`
		Params struct {
			Arguments struct {
				Query      string `json:"query"`
				NumResults int    `json:"numResults"`
			} `json:"arguments"`
		} `json:"params"`
	}
	_ = json.Unmarshal(body, &req)

	m.mu.Lock()
	m.paths = append(m.paths, r.URL.Path)
	m.mu.Unlock()

	switch req.Method {
	case "initialize":
		w.Header().Set(mcpSessionHeader, "sess-123")
		if m.initRPCError {
			// A JSON-RPC error object on the initialize response (a refused handshake)
			// — exasearch.go parses initialize for an error and must early-return.
			m.writeResult(w, `{"jsonrpc":"2.0","id":1,"error":{"code":-32000,"message":"handshake refused"}}`, false)
			return
		}
		m.writeResult(w, `{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-03-26"}}`, false)
	case "notifications/initialized":
		m.mu.Lock()
		m.gotInitialized = true
		m.initializedSess = r.Header.Get(mcpSessionHeader)
		m.mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
	case "tools/call":
		m.mu.Lock()
		m.callSess = r.Header.Get(mcpSessionHeader)
		m.gotQuery = req.Params.Arguments.Query
		m.gotNumResults = req.Params.Arguments.NumResults
		m.mu.Unlock()
		m.writeToolsCall(w)
	default:
		w.WriteHeader(http.StatusBadRequest)
	}
}

func (m *exaMockServer) writeToolsCall(w http.ResponseWriter) {
	if m.statusCode != 0 {
		w.WriteHeader(m.statusCode)
		return
	}
	jsonCT := m.contentType == "json"
	var payload string
	switch {
	case m.rpcError:
		payload = `{"jsonrpc":"2.0","id":2,"error":{"code":-32000,"message":"rate limited"}}`
	case m.malformed:
		payload = `this is not json {{{`
	case m.empty:
		payload = `{"jsonrpc":"2.0","id":2,"result":{"content":[]}}`
	case m.multiText:
		c1, _ := json.Marshal(exaSearchText)
		c2, _ := json.Marshal("Title: Second\nURL: https://example.com/2")
		payload = fmt.Sprintf(`{"jsonrpc":"2.0","id":2,"result":{"content":[{"type":"text","text":%s},{"type":"text","text":%s}]}}`, c1, c2)
	default:
		c1, _ := json.Marshal(exaSearchText)
		payload = fmt.Sprintf(`{"jsonrpc":"2.0","id":2,"result":{"content":[{"type":"text","text":%s}]}}`, c1)
	}
	m.writeResult(w, payload, jsonCT)
}

// writeResult writes payload either as plain JSON or SSE-framed (the default,
// matching the real Exa endpoint). For SSE it emits an event:/id:/comment line (to
// exercise the ignore paths) plus the payload on data: line(s).
func (m *exaMockServer) writeResult(w http.ResponseWriter, payload string, jsonCT bool) {
	if jsonCT {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(payload))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	_, _ = io.WriteString(w, ": a comment line\n")
	_, _ = io.WriteString(w, "event: message\n")
	_, _ = io.WriteString(w, "id: 1\n")
	if m.multiLineSSE {
		// Split the JSON payload across two data: lines at a JSON-INSIGNIFICANT
		// boundary (right after the top-level comma between "jsonrpc" and the next
		// member). The SSE parser joins data: lines with "\n", which is whitespace
		// between JSON tokens, so the reconstructed bytes are valid JSON again — this
		// proves the real client path (parseJSONRPC → parseSSEData → json.Unmarshal)
		// end-to-end across MULTIPLE data: lines, not just the single-line case.
		sep := `","`
		idx := strings.Index(payload, sep)
		if idx < 0 {
			idx = 0
		} else {
			idx += len(sep) - 1 // split just before the opening quote of the next key
		}
		_, _ = io.WriteString(w, "data: "+payload[:idx]+"\n")
		_, _ = io.WriteString(w, "data: "+payload[idx:]+"\n\n")
		return
	}
	_, _ = io.WriteString(w, "data: "+payload+"\n\n")
}

func (m *exaMockServer) snapshotPaths() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, len(m.paths))
	copy(out, m.paths)
	return out
}

func newExaProviderFor(m *exaMockServer, key string) *ExaProvider {
	return NewExaProvider(ExaConfig{
		Endpoint:   m.srv.URL,
		APIKey:     key,
		HTTPClient: m.srv.Client(),
	})
}

// TestExaProviderParsesResults drives the full three-POST handshake against a
// local MCP-speaking httptest.Server and asserts the field mapping, the verbatim
// query, numResults==Limit, and that notifications/initialized was sent with the
// echoed session id.
func TestExaProviderParsesResults(t *testing.T) {
	m := newExaMockServer(t)
	p := newExaProviderFor(m, "")

	results, err := p.Search(context.Background(), tool.SearchQuery{Query: "go 1.26", Limit: 5})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d: %+v", len(results), results)
	}
	r := results[0]
	if r.Title != "Go 1.26 Release Notes" {
		t.Errorf("Title = %q", r.Title)
	}
	if r.URL != "https://go.dev/doc/go1.26" {
		t.Errorf("URL = %q", r.URL)
	}
	if r.Date != "2026-02-01" {
		t.Errorf("Date = %q", r.Date)
	}
	if r.Source != "The Go Team" {
		t.Errorf("Source = %q", r.Source)
	}
	if r.Snippet != "Generics improvements;; new stdlib helpers" {
		t.Errorf("Snippet = %q", r.Snippet)
	}

	// notifications/initialized was sent with the echoed session id.
	if !m.gotInitialized {
		t.Fatal("client never sent notifications/initialized")
	}
	if m.initializedSess != "sess-123" {
		t.Errorf("notifications/initialized session id = %q, want sess-123", m.initializedSess)
	}
	if m.callSess != "sess-123" {
		t.Errorf("tools/call session id = %q, want sess-123", m.callSess)
	}
	// Query verbatim; numResults == Limit.
	if m.gotQuery != "go 1.26" {
		t.Errorf("query forwarded = %q, want verbatim", m.gotQuery)
	}
	if m.gotNumResults != 5 {
		t.Errorf("numResults = %d, want 5 (== Limit)", m.gotNumResults)
	}
}

// TestExaProviderLimitBounds asserts q.Limit caps the result count even when the
// endpoint returns more blobs — AND that the cap is not accidentally always-1 (a
// Limit>=2 sub-case keeps BOTH blobs).
func TestExaProviderLimitBounds(t *testing.T) {
	t.Run("Limit=1 caps 2 blobs to 1", func(t *testing.T) {
		m := newExaMockServer(t)
		m.multiText = true
		p := newExaProviderFor(m, "")
		results, err := p.Search(context.Background(), tool.SearchQuery{Query: "x", Limit: 1})
		if err != nil {
			t.Fatalf("Search: %v", err)
		}
		if len(results) != 1 {
			t.Fatalf("limit not honored: got %d results", len(results))
		}
	})
	t.Run("Limit=5 keeps both blobs (cap is not always-1)", func(t *testing.T) {
		m := newExaMockServer(t)
		m.multiText = true
		p := newExaProviderFor(m, "")
		results, err := p.Search(context.Background(), tool.SearchQuery{Query: "x", Limit: 5})
		if err != nil {
			t.Fatalf("Search: %v", err)
		}
		if len(results) != 2 {
			t.Fatalf("expected BOTH blobs (Limit=5 > 2 returned), got %d: %+v", len(results), results)
		}
	})
}

// TestExaProviderMultiLineSSE proves the real client path (parseJSONRPC →
// parseSSEData → json.Unmarshal) end-to-end against a tools/call result framed
// across ≥2 data: lines, not just the single-line case TestParseSSEData unit-covers.
func TestExaProviderMultiLineSSE(t *testing.T) {
	m := newExaMockServer(t)
	m.multiLineSSE = true
	p := newExaProviderFor(m, "")
	results, err := p.Search(context.Background(), tool.SearchQuery{Query: "x", Limit: 5})
	if err != nil {
		t.Fatalf("Search (multi-line SSE): %v", err)
	}
	if len(results) != 1 || results[0].Title != "Go 1.26 Release Notes" {
		t.Fatalf("multi-line SSE not reassembled into valid JSON: %+v", results)
	}
}

// TestExaProviderInitializeRPCError covers the initialize-response error early
// return: a JSON-RPC error object on the INITIALIZE handshake (not just tools/call)
// must degrade to ErrSearchBackendDown.
func TestExaProviderInitializeRPCError(t *testing.T) {
	m := newExaMockServer(t)
	m.initRPCError = true
	p := newExaProviderFor(m, "")
	_, err := p.Search(context.Background(), tool.SearchQuery{Query: "x", Limit: 5})
	if !errors.Is(err, tool.ErrSearchBackendDown) {
		t.Fatalf("expected ErrSearchBackendDown on an initialize JSON-RPC error, got %v", err)
	}
	// The handshake was refused, so tools/call must never have been reached.
	if m.gotInitialized {
		t.Fatal("notifications/initialized should NOT be sent after a failed initialize")
	}
}

// TestExaProviderNoWellKnownDiscovery is the LOAD-BEARING invariant: the client
// must make EXACTLY the three POSTs to the fixed endpoint and NEVER GET a
// /.well-known/ path (no proactive OAuth discovery).
func TestExaProviderNoWellKnownDiscovery(t *testing.T) {
	m := newExaMockServer(t)
	p := newExaProviderFor(m, "")
	if _, err := p.Search(context.Background(), tool.SearchQuery{Query: "x", Limit: 3}); err != nil {
		t.Fatalf("Search: %v", err)
	}
	paths := m.snapshotPaths()
	for _, pth := range paths {
		if strings.Contains(pth, ".well-known") {
			t.Fatalf("OAuth discovery leaked: request to %q", pth)
		}
	}
	if len(paths) != 3 {
		t.Fatalf("expected exactly 3 POSTs (initialize, notifications/initialized, tools/call), got %d: %v", len(paths), paths)
	}
}

// TestExaProviderDegradation maps closed/auth/rate-limit/5xx/JSON-RPC-error/
// malformed/empty conditions to ErrSearchBackendDown.
func TestExaProviderDegradation(t *testing.T) {
	cases := []struct {
		name  string
		setup func(m *exaMockServer)
	}{
		{"401", func(m *exaMockServer) { m.statusCode = http.StatusUnauthorized }},
		{"403", func(m *exaMockServer) { m.statusCode = http.StatusForbidden }},
		{"429", func(m *exaMockServer) { m.statusCode = http.StatusTooManyRequests }},
		{"500", func(m *exaMockServer) { m.statusCode = http.StatusInternalServerError }},
		{"rpc-error", func(m *exaMockServer) { m.rpcError = true }},
		{"malformed", func(m *exaMockServer) { m.malformed = true }},
		{"empty", func(m *exaMockServer) { m.empty = true }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newExaMockServer(t)
			tc.setup(m)
			p := newExaProviderFor(m, "")
			_, err := p.Search(context.Background(), tool.SearchQuery{Query: "x", Limit: 5})
			if !errors.Is(err, tool.ErrSearchBackendDown) {
				t.Fatalf("expected ErrSearchBackendDown, got %v", err)
			}
		})
	}
}

// TestExaProviderTransportErrorDegrades maps an unreachable endpoint (transport
// error) to ErrSearchBackendDown.
func TestExaProviderTransportErrorDegrades(t *testing.T) {
	// Point at a closed server (construct then close).
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	client := srv.Client()
	url := srv.URL
	srv.Close()
	p := NewExaProvider(ExaConfig{Endpoint: url, HTTPClient: client})
	_, err := p.Search(context.Background(), tool.SearchQuery{Query: "x", Limit: 5})
	if !errors.Is(err, tool.ErrSearchBackendDown) {
		t.Fatalf("expected ErrSearchBackendDown on a transport error, got %v", err)
	}
}

// TestExaProviderAPIKeyInQueryParam asserts the EXA_API_KEY is appended as the
// ?exaApiKey= query param when set, and ABSENT when unset.
func TestExaProviderAPIKeyInQueryParam(t *testing.T) {
	t.Run("with key", func(t *testing.T) {
		m := newExaMockServer(t)
		var gotRawQuery string
		var mu sync.Mutex
		base := m.srv.Config.Handler
		m.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			if r.URL.RawQuery != "" {
				gotRawQuery = r.URL.RawQuery
			}
			mu.Unlock()
			base.ServeHTTP(w, r)
		})
		p := newExaProviderFor(m, "paid-key-xyz")
		if !p.PaidTier() {
			t.Fatal("PaidTier() should be true when a key is set")
		}
		if _, err := p.Search(context.Background(), tool.SearchQuery{Query: "x", Limit: 3}); err != nil {
			t.Fatalf("Search: %v", err)
		}
		mu.Lock()
		defer mu.Unlock()
		if !strings.Contains(gotRawQuery, "exaApiKey=paid-key-xyz") {
			t.Fatalf("expected ?exaApiKey= in the request query, got %q", gotRawQuery)
		}
		// BaseEndpoint must NOT contain the key (log-safety).
		if strings.Contains(p.BaseEndpoint(), "paid-key-xyz") {
			t.Fatalf("BaseEndpoint leaked the key: %q", p.BaseEndpoint())
		}
	})

	t.Run("without key", func(t *testing.T) {
		m := newExaMockServer(t)
		var gotRawQuery string
		var mu sync.Mutex
		base := m.srv.Config.Handler
		m.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			if r.URL.RawQuery != "" {
				gotRawQuery = r.URL.RawQuery
			}
			mu.Unlock()
			base.ServeHTTP(w, r)
		})
		p := newExaProviderFor(m, "")
		if p.PaidTier() {
			t.Fatal("PaidTier() should be false when no key is set")
		}
		if _, err := p.Search(context.Background(), tool.SearchQuery{Query: "x", Limit: 3}); err != nil {
			t.Fatalf("Search: %v", err)
		}
		mu.Lock()
		defer mu.Unlock()
		if strings.Contains(gotRawQuery, "exaApiKey") {
			t.Fatalf("did not expect exaApiKey in the query when no key is set, got %q", gotRawQuery)
		}
	})
}

// TestExaProviderPlainJSONFallback asserts a tools/call response sent as
// application/json (not SSE) is parsed via the raw-JSON fallback.
func TestExaProviderPlainJSONFallback(t *testing.T) {
	m := newExaMockServer(t)
	m.contentType = "json"
	p := newExaProviderFor(m, "")
	results, err := p.Search(context.Background(), tool.SearchQuery{Query: "x", Limit: 5})
	if err != nil {
		t.Fatalf("Search (plain JSON): %v", err)
	}
	if len(results) != 1 || results[0].Title != "Go 1.26 Release Notes" {
		t.Fatalf("plain-JSON fallback failed to parse: %+v", results)
	}
}

// TestParseSSEData covers the SSE helper directly: multi-line data join, ignored
// event:/id:/comment lines, and the no-data error.
func TestParseSSEData(t *testing.T) {
	t.Run("multi-line join", func(t *testing.T) {
		body := ": comment\nevent: message\nid: 7\ndata: line1\ndata: line2\n\n"
		got, err := parseSSEData([]byte(body))
		if err != nil {
			t.Fatalf("parseSSEData: %v", err)
		}
		if string(got) != "line1\nline2" {
			t.Fatalf("join mismatch: %q", got)
		}
	})
	t.Run("no data lines", func(t *testing.T) {
		if _, err := parseSSEData([]byte("event: ping\nid: 1\n")); err == nil {
			t.Fatal("expected an error when no data: lines are present")
		}
	})
}

// TestParseExaResultText covers the result-text parser: full mapping, missing
// fields, and the unstructured fallback (never drop a result).
func TestParseExaResultText(t *testing.T) {
	t.Run("full", func(t *testing.T) {
		r := parseExaResultText(exaSearchText)
		if r.Title == "" || r.URL == "" || r.Date == "" || r.Source == "" || r.Snippet == "" {
			t.Fatalf("expected all fields populated: %+v", r)
		}
	})
	t.Run("missing fields tolerated", func(t *testing.T) {
		r := parseExaResultText("Title: Only A Title")
		if r.Title != "Only A Title" {
			t.Fatalf("Title = %q", r.Title)
		}
	})
	t.Run("unstructured falls to snippet", func(t *testing.T) {
		r := parseExaResultText("just some prose with no labels")
		if r.Snippet != "just some prose with no labels" {
			t.Fatalf("unstructured blob not preserved as snippet: %+v", r)
		}
	})
}
