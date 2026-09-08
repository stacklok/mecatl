package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPublicHandlerBoundsRejectBeforeCallbackSideEffects(t *testing.T) {
	var calls int
	handler := publicHandler(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}), http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusAccepted)
	}))

	tooLarge := httptest.NewRequest(http.MethodPost, "https://broker.test/oauth/callback", strings.NewReader(strings.Repeat("x", maxCallbackBodyBytes+1)))
	tooLarge.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, tooLarge)
	if response.Code != http.StatusRequestEntityTooLarge || calls != 0 {
		t.Fatalf("oversized callback = %d, calls=%d; want 413 and no dispatch", response.Code, calls)
	}

	unsupported := httptest.NewRequest(http.MethodPost, "https://broker.test/oauth/callback", strings.NewReader("x"))
	unsupported.Header.Set("Content-Type", "application/json")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, unsupported)
	if response.Code != http.StatusUnsupportedMediaType || calls != 0 {
		t.Fatalf("unsupported callback = %d, calls=%d; want 415 and no dispatch", response.Code, calls)
	}
}
