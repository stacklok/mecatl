package mcpbrokerserver

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestPublicHandlerAllowsFixedToolHiveProtocolRoutes(t *testing.T) {
	called := false
	handler := PublicHandler(http.NotFoundHandler(), http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	}), DefaultPublicListenerConfig())
	request := httptest.NewRequest(http.MethodPost, "https://broker.example/v1/mcp/broker/mcp", strings.NewReader(`{"jsonrpc":"2.0"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if !called || response.Code != http.StatusNoContent {
		t.Fatalf("fixed ToolHive route = called:%v status:%d, want delegated 204", called, response.Code)
	}
}

func TestPublicHandlerBoundsEveryToolHiveRouteBeforeDelegation(t *testing.T) {
	var calls int
	handler := PublicHandler(http.NotFoundHandler(), http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusNoContent)
	}), DefaultPublicListenerConfig())
	for _, tc := range []struct {
		name, method, path, contentType string
		body                            string
		want                            int
	}{
		{"MCP too large", http.MethodPost, "/v1/mcp/broker/mcp", "application/json", strings.Repeat("x", int(defaultMaxCallbackBodyBytes)+1), http.StatusRequestEntityTooLarge},
		{"MCP unsupported type", http.MethodPost, "/v1/mcp/broker/mcp", "text/plain", "x", http.StatusUnsupportedMediaType},
		{"MCP unsupported method", http.MethodPut, "/v1/mcp/broker/mcp", "", "", http.StatusMethodNotAllowed},
		{"ToolHive token form allowed", http.MethodPost, "/v1/mcp/broker/oauth/token", "application/x-www-form-urlencoded", "code=x", http.StatusNoContent},
		{"ToolHive token JSON rejected", http.MethodPost, "/v1/mcp/broker/oauth/token", "application/json", `{}`, http.StatusUnsupportedMediaType},
		{"ToolHive callback GET allowed", http.MethodGet, "/v1/mcp/broker/oauth/callback?code=x&state=y", "", "", http.StatusNoContent},
		{"ToolHive callback POST rejected", http.MethodPost, "/v1/mcp/broker/oauth/callback", "application/x-www-form-urlencoded", "code=x", http.StatusMethodNotAllowed},
		{"token form allowed", http.MethodPost, "/oauth/token", "application/x-www-form-urlencoded", "code=x", http.StatusNoContent},
		{"authorize GET allowed", http.MethodGet, "/oauth/authorize", "", "", http.StatusNoContent},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			request := httptest.NewRequest(tc.method, "https://broker.example"+tc.path, strings.NewReader(tc.body))
			if tc.contentType != "" {
				request.Header.Set("Content-Type", tc.contentType)
			}
			handler.ServeHTTP(response, request)
			if response.Code != tc.want {
				t.Fatalf("status = %d, want %d", response.Code, tc.want)
			}
		})
	}
	if calls != 4 {
		t.Fatalf("delegated calls = %d, want 4", calls)
	}
}

func TestPublicListenerConfigAllowsExecuteMargin(t *testing.T) {
	cfg := DefaultPublicListenerConfig()
	cfg.ExecuteDeadline = time.Second
	cfg.ReadTimeout = cfg.ExecuteDeadline + publicListenerExecuteMargin
	cfg.WriteTimeout = cfg.ExecuteDeadline + publicListenerExecuteMargin
	if !cfg.validForExecute() {
		t.Fatal("listener timeouts at the deadline margin must be accepted")
	}
	cfg.ReadTimeout--
	cfg.WriteTimeout--
	if cfg.validForExecute() {
		t.Fatal("listener timeouts below the deadline margin must be rejected")
	}
}
