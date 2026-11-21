package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stacklok/mecatl/internal/identitybroker"
)

// pagedGateway is a streamable-HTTP MCP gateway answering tools/list from
// page, by cursor. It answers in JSON, never SSE.
type pagedGateway struct {
	page func(r *http.Request, cursor string) (tools []map[string]any, next string)

	mu    sync.Mutex
	lists int
}

func (g *pagedGateway) listCalls() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.lists
}

func (g *pagedGateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodDelete {
		w.WriteHeader(http.StatusOK)
		return
	}
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Params struct {
			Cursor string `json:"cursor"`
		} `json:"params"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if len(req.ID) == 0 {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	var result any
	switch req.Method {
	case "initialize":
		w.Header().Set("Mcp-Session-Id", "session-1")
		result = map[string]any{
			"protocolVersion": gatewayProtocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "gateway", "version": "1"},
		}
	case "tools/list":
		g.mu.Lock()
		g.lists++
		g.mu.Unlock()
		tools, next := g.page(r, req.Params.Cursor)
		page := map[string]any{"tools": tools}
		if next != "" {
			page["nextCursor"] = next
		}
		result = page
	default:
		w.WriteHeader(http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
}

func pagedTool(name string) map[string]any {
	return map[string]any{"name": name, "inputSchema": map[string]any{"type": "object"}}
}

func listPagedGateway(ctx context.Context, t *testing.T, gateway *pagedGateway, limits GatewayListLimits) ([]string, error) {
	t.Helper()
	server := httptest.NewServer(gateway)
	t.Cleanup(server.Close)
	tools, err := ListGatewayTools(ctx, server.URL, server.Client(), identitybroker.NewToken("listing-token"), limits)
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		names = append(names, tool.Name)
	}
	return names, err
}

// TestListGatewayToolsFollowsEveryPage lists the tools of every page, in
// order, and stops at the page without a cursor.
func TestListGatewayToolsFollowsEveryPage(t *testing.T) {
	gateway := &pagedGateway{page: func(_ *http.Request, cursor string) ([]map[string]any, string) {
		switch cursor {
		case "":
			return []map[string]any{pagedTool("a"), pagedTool("b")}, "p2"
		case "p2":
			return []map[string]any{pagedTool("c")}, "p3"
		default:
			return []map[string]any{pagedTool("d")}, ""
		}
	}}
	names, err := listPagedGateway(context.Background(), t, gateway, GatewayListLimits{})
	if err != nil || strings.Join(names, ",") != "a,b,c,d" || gateway.listCalls() != 3 {
		t.Fatalf("ListGatewayTools = %v, %v after %d pages, want a,b,c,d after 3", names, err, gateway.listCalls())
	}
}

// TestListGatewayToolsRefusesAnUnboundedListing fails, without any tools,
// on a listing a gateway could otherwise keep going forever or make too big.
func TestListGatewayToolsRefusesAnUnboundedListing(t *testing.T) {
	endless := func(_ *http.Request, cursor string) ([]map[string]any, string) {
		n, _ := strconv.Atoi(strings.TrimPrefix(cursor, "p"))
		return []map[string]any{pagedTool("t" + strconv.Itoa(n))}, "p" + strconv.Itoa(n+1)
	}
	for name, tc := range map[string]struct {
		page   func(*http.Request, string) ([]map[string]any, string)
		limits GatewayListLimits
		// listed is the tools/list requests the gateway must receive before
		// the listing fails, so each case fails for its own reason.
		listed int
	}{
		"repeated cursor": {
			page: func(*http.Request, string) ([]map[string]any, string) {
				return []map[string]any{pagedTool("again")}, "same"
			},
			listed: 2,
		},
		"too many pages": {page: endless, limits: GatewayListLimits{MaxPages: 3}, listed: 4},
		"too many tools": {
			page: func(*http.Request, string) ([]map[string]any, string) {
				return []map[string]any{pagedTool("a"), pagedTool("b"), pagedTool("c")}, ""
			},
			limits: GatewayListLimits{MaxTools: 2},
			listed: 1,
		},
		"too many bytes": {
			page: func(*http.Request, string) ([]map[string]any, string) {
				tool := pagedTool("big")
				tool["description"] = strings.Repeat("x", 8<<10)
				return []map[string]any{tool}, ""
			},
			limits: GatewayListLimits{MaxBytes: 4 << 10},
			listed: 1,
		},
		// initialize answers at once and tools/list never does, so the
		// timeout runs out on the listing, not on the session's setup.
		"past its timeout": {
			page: func(r *http.Request, _ string) ([]map[string]any, string) {
				<-r.Context().Done()
				return nil, ""
			},
			limits: GatewayListLimits{Timeout: time.Second},
			listed: 1,
		},
	} {
		t.Run(name, func(t *testing.T) {
			gateway := &pagedGateway{page: tc.page}
			names, err := listPagedGateway(context.Background(), t, gateway, tc.limits)
			if !errors.Is(err, ErrGatewayList) || len(names) != 0 {
				t.Errorf("ListGatewayTools = %v, %v, want no tools and ErrGatewayList", names, err)
			}
			if got := gateway.listCalls(); got != tc.listed {
				t.Errorf("gateway received %d tools/list requests, want %d", got, tc.listed)
			}
		})
	}
}

// TestListGatewayToolsReturnsTheCallersContextError returns the caller's
// context error, not ErrGatewayList, when the caller gives up.
func TestListGatewayToolsReturnsTheCallersContextError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	gateway := &pagedGateway{page: func(r *http.Request, _ string) ([]map[string]any, string) {
		cancel()
		<-r.Context().Done()
		return nil, ""
	}}
	if _, err := listPagedGateway(ctx, t, gateway, GatewayListLimits{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("ListGatewayTools = %v, want context.Canceled", err)
	}
}

// TestListGatewayToolsSendsTheBearerOnEveryRequest sends the bearer in the
// Authorization header of every request of the listing: the session's
// setup, every page, and its close.
func TestListGatewayToolsSendsTheBearerOnEveryRequest(t *testing.T) {
	gateway := &pagedGateway{page: func(_ *http.Request, cursor string) ([]map[string]any, string) {
		if cursor == "" {
			return []map[string]any{pagedTool("a")}, "p2"
		}
		return []map[string]any{pagedTool("b")}, ""
	}}
	var mu sync.Mutex
	var seen []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(body))
		var msg struct {
			Method string `json:"method"`
		}
		_ = json.Unmarshal(body, &msg)
		mu.Lock()
		seen = append(seen, r.Method+" "+msg.Method+" "+r.Header.Get("Authorization"))
		mu.Unlock()
		gateway.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)
	if _, err := ListGatewayTools(t.Context(), server.URL, server.Client(), identitybroker.NewToken("listing-token"), GatewayListLimits{}); err != nil {
		t.Fatalf("ListGatewayTools: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	want := []string{
		"POST initialize Bearer listing-token",
		"POST notifications/initialized Bearer listing-token",
		"POST tools/list Bearer listing-token",
		"POST tools/list Bearer listing-token",
		"DELETE  Bearer listing-token",
	}
	if !slices.Equal(seen, want) {
		t.Fatalf("requests = %q, want %q", seen, want)
	}
}

// TestListGatewayToolsFollowsNoRedirect fails on a redirect rather than
// sending the listing, and its bearer, to the redirect's target.
func TestListGatewayToolsFollowsNoRedirect(t *testing.T) {
	var mu sync.Mutex
	var redirected []string
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		redirected = append(redirected, r.Header.Get("Authorization"))
		mu.Unlock()
		w.WriteHeader(http.StatusBadRequest)
	}))
	t.Cleanup(target.Close)
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/mcp", http.StatusTemporaryRedirect)
	}))
	t.Cleanup(gateway.Close)
	tools, err := ListGatewayTools(t.Context(), gateway.URL, gateway.Client(), identitybroker.NewToken("listing-token"), GatewayListLimits{})
	if !errors.Is(err, ErrGatewayList) || len(tools) != 0 {
		t.Fatalf("ListGatewayTools = %v, %v, want no tools and ErrGatewayList", tools, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(redirected) != 0 {
		t.Fatalf("the redirect's target received %d requests, want none", len(redirected))
	}
}

// TestListGatewayToolsRefusesMissingArguments refuses, before any request,
// a listing without an endpoint, a client, or a token.
func TestListGatewayToolsRefusesMissingArguments(t *testing.T) {
	for name, tc := range map[string]struct {
		endpoint string
		client   *http.Client
		token    identitybroker.Token
	}{
		"no endpoint": {"", http.DefaultClient, identitybroker.NewToken("listing-token")},
		"no client":   {"https://gateway.example/mcp", nil, identitybroker.NewToken("listing-token")},
		"no token":    {"https://gateway.example/mcp", http.DefaultClient, identitybroker.Token{}},
	} {
		if tools, err := ListGatewayTools(t.Context(), tc.endpoint, tc.client, tc.token, GatewayListLimits{}); !errors.Is(err, ErrGatewayList) || tools != nil {
			t.Errorf("%s: ListGatewayTools = %v, %v, want nil and ErrGatewayList", name, tools, err)
		}
	}
}

// TestListGatewayToolsNeverResumesADroppedAnswer fails a listing whose
// tools/list answer ends after an event ID, without the response, rather than
// resuming it with a GET whose body the byte budget would not count.
func TestListGatewayToolsNeverResumesADroppedAnswer(t *testing.T) {
	gateway := &pagedGateway{page: func(*http.Request, string) ([]map[string]any, string) { return nil, "" }}
	var mu sync.Mutex
	var listID json.RawMessage
	gets := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			// A resumed answer: the tool is bigger than the byte budget.
			mu.Lock()
			gets++
			id := listID
			mu.Unlock()
			tool := pagedTool("big")
			tool["description"] = strings.Repeat("x", 8<<10)
			reply, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{"tools": []any{tool}}})
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "id: 2\ndata: "+string(reply)+"\n\n")
			return
		}
		body, _ := io.ReadAll(r.Body)
		var msg struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		_ = json.Unmarshal(body, &msg)
		if msg.Method != "tools/list" {
			r.Body = io.NopCloser(bytes.NewReader(body))
			gateway.ServeHTTP(w, r)
			return
		}
		// The answer drops after an event that carries only an ID.
		mu.Lock()
		listID = msg.ID
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "id: 1\nretry: 1\ndata: \n\n")
	}))
	t.Cleanup(server.Close)
	tools, err := ListGatewayTools(t.Context(), server.URL, server.Client(), identitybroker.NewToken("listing-token"), GatewayListLimits{MaxBytes: 4 << 10})
	if !errors.Is(err, ErrGatewayList) || len(tools) != 0 {
		t.Fatalf("ListGatewayTools = %v, %v, want no tools and ErrGatewayList", tools, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if gets != 0 {
		t.Fatalf("the listing resumed its answer with %d GETs, want none", gets)
	}
}

// TestGatewayListLimitsDefaults takes each default for a zero field and keeps
// a set one.
func TestGatewayListLimitsDefaults(t *testing.T) {
	got := GatewayListLimits{MaxPages: 7}.withDefaults()
	want := GatewayListLimits{
		Timeout:  defaultGatewayListTimeout,
		MaxTools: defaultGatewayListMaxTools,
		MaxPages: 7,
		MaxBytes: defaultGatewayListMaxBytes,
	}
	if got != want {
		t.Fatalf("withDefaults = %+v, want %+v", got, want)
	}
}

// TestGatewayListLimitsDefaultsForNegative takes each default for a negative
// field, as for a zero one.
func TestGatewayListLimitsDefaultsForNegative(t *testing.T) {
	got := GatewayListLimits{Timeout: -1, MaxTools: -1, MaxPages: -1, MaxBytes: -1}.withDefaults()
	want := GatewayListLimits{
		Timeout:  defaultGatewayListTimeout,
		MaxTools: defaultGatewayListMaxTools,
		MaxPages: defaultGatewayListMaxPages,
		MaxBytes: defaultGatewayListMaxBytes,
	}
	if got != want {
		t.Fatalf("withDefaults = %+v, want %+v", got, want)
	}
}
