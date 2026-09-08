package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/internal/adapter/mcpbrokerserver"
)

func TestPublicHandlerBoundsRejectBeforeCallbackSideEffects(t *testing.T) {
	var calls int
	handler := mcpbrokerserver.PublicHandler(http.NotFoundHandler(), http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusAccepted)
	}), mcpbrokerserver.DefaultPublicListenerConfig())

	tooLarge := httptest.NewRequest(http.MethodPost, "https://broker.test/v1/mcp/broker/mcp", strings.NewReader(strings.Repeat("x", 64<<10+1)))
	tooLarge.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, tooLarge)
	if response.Code != http.StatusRequestEntityTooLarge || calls != 0 {
		t.Fatalf("oversized MCP route = %d, calls=%d; want 413 and no dispatch", response.Code, calls)
	}
}
