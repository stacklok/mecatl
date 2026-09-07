package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestPublicHandlerRoutesGRPCAndCallbacks(t *testing.T) {
	var grpcCalls, callbackCalls atomic.Int32
	handler := publicHandler(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			grpcCalls.Add(1)
			w.WriteHeader(http.StatusNoContent)
		}),
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			callbackCalls.Add(1)
			w.WriteHeader(http.StatusAccepted)
		}),
	)

	grpcRequest := httptest.NewRequest(http.MethodPost, "https://broker.test/mecatl.broker.v1.BrokerService/Attach", nil)
	grpcRequest.ProtoMajor = 2
	grpcRequest.Header.Set("Content-Type", "application/grpc+proto")
	grpcResponse := httptest.NewRecorder()
	handler.ServeHTTP(grpcResponse, grpcRequest)
	if grpcResponse.Code != http.StatusNoContent || grpcCalls.Load() != 1 || callbackCalls.Load() != 0 {
		t.Fatalf("gRPC request routed incorrectly: status=%d grpc=%d callback=%d", grpcResponse.Code, grpcCalls.Load(), callbackCalls.Load())
	}

	callbackResponse := httptest.NewRecorder()
	handler.ServeHTTP(callbackResponse, httptest.NewRequest(http.MethodGet, "https://broker.test/oauth/callback", nil))
	if callbackResponse.Code != http.StatusAccepted || grpcCalls.Load() != 1 || callbackCalls.Load() != 1 {
		t.Fatalf("callback request routed incorrectly: status=%d grpc=%d callback=%d", callbackResponse.Code, grpcCalls.Load(), callbackCalls.Load())
	}
}

func TestAdminHandlerIsLoopbackOnlyAndDrainIsGETWithReadinessTransition(t *testing.T) {
	for _, address := range []string{"0.0.0.0:9082", "[::]:9082", "broker.test:9082"} {
		if err := validateAdminAddress(address); err == nil {
			t.Errorf("validateAdminAddress(%q) accepted a non-loopback address", address)
		}
	}
	if err := validateAdminAddress("127.0.0.1:9082"); err != nil {
		t.Fatalf("validate loopback admin address: %v", err)
	}

	var ready atomic.Bool
	ready.Store(true)
	propagated := make(chan struct{})
	var starts atomic.Int32
	handler := adminHandler(func(context.Context) bool { return ready.Load() }, func() {
		if starts.Add(1) == 1 {
			ready.Store(false)
			go func() {
				time.Sleep(10 * time.Millisecond)
				close(propagated)
			}()
		}
	}, propagated)

	readyResponse := httptest.NewRecorder()
	handler.ServeHTTP(readyResponse, httptest.NewRequest(http.MethodGet, "http://127.0.0.1:9082/readyz", nil))
	if readyResponse.Code != http.StatusOK {
		t.Fatalf("ready before drain = %d, want 200", readyResponse.Code)
	}

	postResponse := httptest.NewRecorder()
	handler.ServeHTTP(postResponse, httptest.NewRequest(http.MethodPost, "http://127.0.0.1:9082/drain", nil))
	if postResponse.Code != http.StatusMethodNotAllowed || starts.Load() != 0 {
		t.Fatalf("POST /drain = %d, starts=%d; want 405 and no drain", postResponse.Code, starts.Load())
	}

	drainResponse := httptest.NewRecorder()
	handler.ServeHTTP(drainResponse, httptest.NewRequest(http.MethodGet, "http://127.0.0.1:9082/drain", nil))
	if drainResponse.Code != http.StatusOK || starts.Load() != 1 {
		t.Fatalf("GET /drain = %d, starts=%d; want 200 and one drain", drainResponse.Code, starts.Load())
	}
	readyResponse = httptest.NewRecorder()
	handler.ServeHTTP(readyResponse, httptest.NewRequest(http.MethodGet, "http://127.0.0.1:9082/readyz", nil))
	if readyResponse.Code != http.StatusServiceUnavailable {
		t.Fatalf("ready after drain = %d, want 503", readyResponse.Code)
	}
}
