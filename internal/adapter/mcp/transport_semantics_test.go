package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

type transportFixture struct {
	server *httptest.Server
	diag   *recordingDiag

	handshakes  atomic.Int32
	toolCalls   atomic.Int32
	sideEffects atomic.Int32

	mu         sync.Mutex
	sessionIDs []string
	intercept  func(http.ResponseWriter, *http.Request, rpcEnvelope) bool
}

type rpcEnvelope struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
}

func newTransportFixture(t *testing.T, intercept func(http.ResponseWriter, *http.Request, rpcEnvelope) bool) *transportFixture {
	t.Helper()
	f := &transportFixture{diag: &recordingDiag{}, intercept: intercept}
	sdkServer := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "transport-fixture", Version: "v1"}, nil)
	mcpsdk.AddTool(sdkServer, &mcpsdk.Tool{Name: "echo", Description: "mutating echo"},
		func(_ context.Context, _ *mcpsdk.CallToolRequest, in echoArgs) (*mcpsdk.CallToolResult, any, error) {
			f.sideEffects.Add(1)
			return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "echo:" + in.Text}}}, nil, nil
		})
	addTestResourcesAndPrompts(sdkServer)
	next := mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return sdkServer }, nil)
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var env rpcEnvelope
		if r.Method == http.MethodPost {
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Errorf("read MCP request: %v", err)
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
			_ = json.Unmarshal(body, &env)
			switch env.Method {
			case "initialize":
				f.handshakes.Add(1)
			case "tools/call":
				f.toolCalls.Add(1)
				f.mu.Lock()
				f.sessionIDs = append(f.sessionIDs, r.Header.Get("Mcp-Session-Id"))
				f.mu.Unlock()
			}
		}
		if f.intercept != nil && f.intercept(w, r, env) {
			return
		}
		next.ServeHTTP(w, r)
	}))
	t.Cleanup(f.server.Close)
	return f
}

func (f *transportFixture) connect(t *testing.T) *Server {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s, err := Connect(ctx, ServerConfig{Name: "rs", URL: f.server.URL, Timeout: 5 * time.Second}, f.diag)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestTransportPerCallRejectionDoesNotReconnectOrReplay(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		structured bool
		message    string
	}{
		{"transient-429", http.StatusTooManyRequests, false, ""},
		{"transient-502", http.StatusBadGateway, false, ""},
		{"transient-503", http.StatusServiceUnavailable, false, ""},
		{"transient-504", http.StatusGatewayTimeout, false, ""},
	}
	legacySignatures := []string{
		"session not found",
		"client is closing",
		"connection closed",
		"connection refused",
		"EOF",
		"rejected by transport",
	}
	for _, status := range []int{http.StatusBadRequest, http.StatusNotFound} {
		for _, message := range legacySignatures {
			tests = append(tests, struct {
				name       string
				status     int
				structured bool
				message    string
			}{
				name:       fmt.Sprintf("structured-%d-%s", status, message),
				status:     status,
				structured: true,
				message:    message,
			})
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var rejected atomic.Bool
			var f *transportFixture
			f = newTransportFixture(t, func(w http.ResponseWriter, _ *http.Request, env rpcEnvelope) bool {
				if env.Method != "tools/call" || rejected.Swap(true) {
					return false
				}
				// This represents a server-side mutation completed before its response
				// was rejected. Replaying would apply it twice.
				f.sideEffects.Add(1)
				if tc.structured {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(tc.status)
					_ = json.NewEncoder(w).Encode(map[string]any{
						"jsonrpc": "2.0", "id": env.ID,
						"error": map[string]any{"code": -32000, "message": tc.message},
					})
				} else {
					w.WriteHeader(tc.status)
				}
				return true
			})
			s := f.connect(t)

			first := callEcho(context.Background(), t, s, "first")
			if !first.IsError {
				t.Fatalf("first call = %+v, want a per-call error", first)
			}
			if got := f.toolCalls.Load(); got != 1 {
				t.Fatalf("first call HTTP requests = %d, want 1 (no replay)", got)
			}
			if got := f.sideEffects.Load(); got != 1 {
				t.Fatalf("first call side effects = %d, want 1 (no replay)", got)
			}
			if got := f.handshakes.Load(); got != 1 {
				t.Fatalf("handshakes = %d, want 1 (no reconnect)", got)
			}
			if got := f.diag.count("mcp server reconnecting"); got != 0 {
				t.Fatalf("reconnect diagnostics = %d, want 0", got)
			}

			second := callEcho(context.Background(), t, s, "second")
			if second.IsError || second.Content != "echo:second" {
				t.Fatalf("second call = %+v, want same-session success", second)
			}
			if got := f.toolCalls.Load(); got != 2 {
				t.Errorf("total call requests = %d, want 2", got)
			}
			if got := f.sideEffects.Load(); got != 2 {
				t.Errorf("total side effects = %d, want 2", got)
			}
			if got := f.handshakes.Load(); got != 1 {
				t.Errorf("handshakes after second call = %d, want 1", got)
			}
		})
	}
}

func TestPlainSessionMissingReconnectsExactlyOnce(t *testing.T) {
	var missing atomic.Bool
	f := newTransportFixture(t, func(w http.ResponseWriter, _ *http.Request, env rpcEnvelope) bool {
		if env.Method == "tools/call" && !missing.Swap(true) {
			http.Error(w, "session not found", http.StatusNotFound)
			return true
		}
		return false
	})
	s := f.connect(t)
	res := callEcho(context.Background(), t, s, "retry")
	if res.IsError || res.Content != "echo:retry" {
		t.Fatalf("call after missing session = %+v, want retried success", res)
	}
	if got := f.toolCalls.Load(); got != 2 {
		t.Errorf("tool requests = %d, want 2", got)
	}
	if got := f.sideEffects.Load(); got != 1 {
		t.Errorf("side effects = %d, want 1", got)
	}
	if got := f.handshakes.Load(); got != 2 {
		t.Errorf("handshakes = %d, want 2", got)
	}
	if got := f.diag.count("mcp server reconnecting"); got != 1 {
		t.Errorf("reconnect diagnostics = %d, want 1", got)
	}
	f.mu.Lock()
	ids := append([]string(nil), f.sessionIDs...)
	f.mu.Unlock()
	if len(ids) != 2 || ids[0] == "" || ids[1] == "" || ids[0] == ids[1] {
		t.Errorf("tool-call session IDs = %q, want two distinct non-empty IDs", ids)
	}
}

func TestCancellationReturnsBeforeAsyncNotificationAndKeepsSessionUsable(t *testing.T) {
	for _, kind := range []string{"tool", "prompt"} {
		t.Run(kind, func(t *testing.T) {
			started := make(chan struct{})
			handlerExited := make(chan struct{})
			notificationSeen := make(chan struct{})
			releaseNotification := make(chan struct{})
			var seenOnce sync.Once

			sdkServer := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "cancel-fixture", Version: "v1"}, nil)
			mcpsdk.AddTool(sdkServer, &mcpsdk.Tool{Name: "echo", Description: "echo"}, func(_ context.Context, _ *mcpsdk.CallToolRequest, in echoArgs) (*mcpsdk.CallToolResult, any, error) {
				return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "echo:" + in.Text}}}, nil, nil
			})
			mcpsdk.AddTool(sdkServer, &mcpsdk.Tool{Name: "block", Description: "blocks"}, func(ctx context.Context, _ *mcpsdk.CallToolRequest, _ noArgs) (*mcpsdk.CallToolResult, any, error) {
				close(started)
				<-ctx.Done()
				close(handlerExited)
				return nil, nil, ctx.Err()
			})
			sdkServer.AddPrompt(&mcpsdk.Prompt{Name: "block-prompt", Description: "blocks"}, func(ctx context.Context, _ *mcpsdk.GetPromptRequest) (*mcpsdk.GetPromptResult, error) {
				close(started)
				<-ctx.Done()
				close(handlerExited)
				return nil, ctx.Err()
			})
			next := mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return sdkServer }, nil)
			httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost {
					body, _ := io.ReadAll(r.Body)
					r.Body = io.NopCloser(bytes.NewReader(body))
					var env rpcEnvelope
					_ = json.Unmarshal(body, &env)
					if env.Method == "notifications/cancelled" {
						seenOnce.Do(func() { close(notificationSeen) })
						<-releaseNotification
					}
				}
				next.ServeHTTP(w, r)
			}))
			t.Cleanup(httpServer.Close)
			diag := &recordingDiag{}
			ctx, cancelConnect := context.WithTimeout(context.Background(), 10*time.Second)
			s, err := Connect(ctx, ServerConfig{Name: "rs", URL: httpServer.URL, Timeout: 5 * time.Second}, diag)
			cancelConnect()
			if err != nil {
				t.Fatalf("Connect: %v", err)
			}
			t.Cleanup(func() { _ = s.Close() })

			callCtx, cancelCall := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() {
				if kind == "tool" {
					_, err := s.callTool(callCtx, "block", json.RawMessage(`{}`))
					done <- err
					return
				}
				_, err := s.getPrompt(callCtx, "block-prompt", nil)
				done <- err
			}()
			awaitSignal(t, started, "blocked MCP handler start")
			cancelCall()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("cancelled %s error = %v, want context.Canceled", kind, err)
				}
			case <-time.After(time.Second):
				t.Fatalf("cancelled %s waited for blocked notification", kind)
			}
			awaitSignal(t, notificationSeen, "cancellation notification")
			if got := diag.count("mcp server reconnecting"); got != 0 {
				t.Fatalf("reconnect diagnostics = %d, want 0", got)
			}
			res := callEcho(context.Background(), t, s, "usable")
			if res.IsError || res.Content != "echo:usable" {
				t.Fatalf("post-cancel call = %+v, want usable session", res)
			}
			close(releaseNotification)
			awaitSignal(t, handlerExited, "cancelled MCP handler exit")
		})
	}
}

func awaitSignal(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

func TestUnsupportedProtocolConnectCleansUpSession(t *testing.T) {
	deleted := make(chan struct{})
	var deleteOnce sync.Once
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			deleteOnce.Do(func() { close(deleted) })
			w.WriteHeader(http.StatusNoContent)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var env rpcEnvelope
		_ = json.Unmarshal(body, &env)
		if env.Method != "initialize" {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Mcp-Session-Id", "unsupported-session")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0", "id": env.ID,
			"result": map[string]any{
				"protocolVersion": "1900-01-01",
				"capabilities":    map[string]any{},
				"serverInfo":      map[string]any{"name": "fixture", "version": "v1"},
			},
		})
	}))
	defer ts.Close()
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "fixture-client", Version: "v1"}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := client.Connect(ctx, &mcpsdk.StreamableClientTransport{Endpoint: ts.URL, DisableStandaloneSSE: true}, &mcpsdk.ClientSessionOptions{ProtocolVersion: "2024-11-05"})
	if err == nil {
		t.Fatal("Connect succeeded with unsupported negotiated protocol version")
	}
	awaitSignal(t, deleted, "failed Connect cleanup DELETE")
}
