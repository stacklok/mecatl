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

func TestPublicListenerConfigRequiresExecuteMarginWhenExplicit(t *testing.T) {
	cfg := DefaultPublicListenerConfig()
	cfg.ExecuteDeadline = time.Second
	cfg.ReadTimeout = cfg.ExecuteDeadline + publicListenerExecuteMargin
	cfg.WriteTimeout = cfg.ExecuteDeadline + publicListenerExecuteMargin
	if cfg.validForExecute() {
		t.Fatal("listener timeouts at the deadline margin must be rejected")
	}
	cfg.ReadTimeout++
	cfg.WriteTimeout++
	if !cfg.validForExecute() {
		t.Fatal("listener timeouts above the deadline margin must be accepted")
	}
}
