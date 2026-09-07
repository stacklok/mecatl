package mcpbrokerserver

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestSingletonBrokerRemediation_Scenario3_ProductionReadinessUsesRealDependencies(t *testing.T) {
	var failed atomic.Int32
	calls := make([]atomic.Int32, 6)
	checks := make([]ReadinessCheck, len(calls))
	for i := range checks {
		index := int32(i)
		checks[i] = func(context.Context) error {
			calls[index].Add(1)
			if failed.Load() == index+1 {
				return errors.New("prerequisite unavailable")
			}
			return nil
		}
	}
	coordinator, err := NewCoordinator(50*time.Millisecond, checks...)
	if err != nil {
		t.Fatalf("NewCoordinator: %v", err)
	}
	if coordinator.Ready(t.Context()) {
		t.Fatal("readiness opened before construction completed")
	}
	coordinator.Open()
	if !coordinator.Ready(t.Context()) {
		t.Fatal("all healthy prerequisites did not open readiness")
	}
	for i := int32(1); i <= int32(len(checks)); i++ {
		failed.Store(i)
		if coordinator.Ready(t.Context()) {
			t.Fatalf("readiness stayed true with prerequisite %d failed", i)
		}
	}
	for i := range calls {
		if calls[i].Load() == 0 {
			t.Fatalf("readiness prerequisite %d was never checked", i+1)
		}
	}
	coordinator.BeginDrain()
	failed.Store(0)
	if coordinator.Ready(t.Context()) {
		t.Fatal("readiness stayed true after admission closed")
	}
}

func TestSingletonBrokerRemediation_Scenario3_ProductionDrainAndCleanup(t *testing.T) {
	coordinator, err := NewCoordinator(time.Second)
	if err != nil {
		t.Fatal(err)
	}
	coordinator.Open()
	entered := make(chan struct{})
	operationDone := make(chan struct{})
	handler := coordinator.HTTP(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		close(entered)
		<-r.Context().Done()
		close(operationDone)
	}))
	go handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/v1/mcp/broker/oauth/callback", nil))
	<-entered

	coordinator.BeginDrain()
	late := httptest.NewRecorder()
	handler.ServeHTTP(late, httptest.NewRequest(http.MethodPost, "/callback", nil))
	if late.Code != http.StatusServiceUnavailable {
		t.Fatalf("new callback after drain = %d, want 503", late.Code)
	}
	_, grpcErr := coordinator.UnaryInterceptor(t.Context(), nil, &grpc.UnaryServerInfo{}, func(context.Context, any) (any, error) {
		t.Fatal("new gRPC work reached handler after drain")
		return nil, nil
	})
	if status.Code(grpcErr) != codes.Unavailable {
		t.Fatalf("new gRPC work after drain = %v, want unavailable", grpcErr)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	if err := coordinator.Drain(ctx, 0); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Drain with active work = %v, want deadline exceeded", err)
	}
	select {
	case <-operationDone:
	case <-time.After(time.Second):
		t.Fatal("drain deadline did not cancel active operation")
	}
	if err := coordinator.Drain(t.Context(), -time.Second); err == nil {
		t.Fatal("negative endpoint propagation interval was accepted")
	}
}
