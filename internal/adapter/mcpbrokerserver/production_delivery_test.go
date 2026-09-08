package mcpbrokerserver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/stacklok/mecatl/internal/adapter/mcpbroker"
	contract "github.com/stacklok/mecatl/internal/mcpbroker"
)

func TestSingletonBrokerRemediation_Scenario3_ProductionReadinessUsesRealDependencies(t *testing.T) {
	issuer := newIdentityFixture(t)
	t.Setenv("MECATL_READINESS_SECRET", "offline-secret")
	var process *mcpbroker.Process
	srv, err := New(t.Context(), Config{
		OIDC: productionOIDC(issuer, time.Minute),
		Factory: func(ctx context.Context) (contract.Service, mcpbroker.HandlerBundle, string, func() error, error) {
			var buildErr error
			process, buildErr = mcpbroker.NewToolHiveProcess(ctx, mcpbroker.ToolHiveConfig{
				CallbackURL: "https://broker.example/callback",
				Profiles: []mcpbroker.ToolHiveProfile{{
					Name: "github", URL: "https://mcp.example/mcp", Auth: "oauth",
					OAuth: &mcpbroker.ToolHiveOAuth{
						AuthorizationEndpoint: "https://identity.example/authorize", TokenEndpoint: "https://identity.example/token",
						ClientID: "readiness-client", ClientSecretEnv: "MECATL_READINESS_SECRET",
					},
					Static: []mcpbroker.StaticTool{{Name: "mcp__github__read", Schema: json.RawMessage(`{"type":"object"}`), ReadOnly: true}},
				}},
			})
			if buildErr != nil {
				return nil, mcpbroker.HandlerBundle{}, "", nil, buildErr
			}
			return process.Runtime, process.Handlers, "/callback", process.Close, nil
		},
		ReadinessTimeout: time.Second,
	})
	if err != nil {
		t.Fatalf("production New: %v", err)
	}
	defer func() { _ = srv.Close(context.Background()) }()
	if process == nil || !srv.Ready(t.Context()) {
		t.Fatal("production OIDC and ToolHive dependencies did not open readiness")
	}
	if err := process.Close(); err != nil {
		t.Fatalf("close ToolHive dependency: %v", err)
	}
	if srv.Ready(t.Context()) {
		t.Fatal("readiness stayed open after the production ToolHive process failed")
	}
	srv.BeginDrain()
	if srv.Ready(t.Context()) {
		t.Fatal("readiness stayed open after admission closed")
	}
}

func TestSingletonBrokerRemediation_Scenario3_ProductionDrainAndCleanup(t *testing.T) {
	issuer := newIdentityFixture(t)
	t.Setenv("MECATL_DRAIN_SECRET", "offline-secret")
	entered := make(chan struct{})
	operationDone := make(chan struct{})
	var processClosed atomic.Bool
	srv, err := New(t.Context(), Config{
		OIDC: productionOIDC(issuer, time.Minute),
		Factory: func(ctx context.Context) (contract.Service, mcpbroker.HandlerBundle, string, func() error, error) {
			process, buildErr := mcpbroker.NewToolHiveProcess(ctx, mcpbroker.ToolHiveConfig{
				CallbackURL: "https://broker.example/callback",
				Profiles: []mcpbroker.ToolHiveProfile{{
					Name: "github", URL: "https://mcp.example/mcp", Auth: "oauth",
					OAuth:  &mcpbroker.ToolHiveOAuth{AuthorizationEndpoint: "https://identity.example/authorize", TokenEndpoint: "https://identity.example/token", ClientID: "drain-client", ClientSecretEnv: "MECATL_DRAIN_SECRET"},
					Static: []mcpbroker.StaticTool{{Name: "mcp__github__read", Schema: json.RawMessage(`{"type":"object"}`), ReadOnly: true}},
				}},
			})
			if buildErr != nil {
				return nil, mcpbroker.HandlerBundle{}, "", nil, buildErr
			}
			handlers := process.Handlers
			realCallback := handlers.Callback
			handlers.Callback = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				close(entered)
				<-r.Context().Done()
				close(operationDone)
				realCallback.ServeHTTP(w, r)
			})
			closeProcess := func() error {
				select {
				case <-operationDone:
				default:
					t.Error("ToolHive process closed before admitted callback joined")
				}
				processClosed.Store(true)
				return process.Close()
			}
			return process.Runtime, handlers, "/callback", closeProcess, nil
		},
		ReadinessTimeout: time.Second,
	})
	if err != nil {
		t.Fatalf("production New: %v", err)
	}
	callbackDone := make(chan struct{})
	go func() {
		defer close(callbackDone)
		srv.HTTPHandler().ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/callback", nil))
	}()
	<-entered

	srv.BeginDrain()
	late := httptest.NewRecorder()
	srv.HTTPHandler().ServeHTTP(late, httptest.NewRequest(http.MethodGet, "/callback", nil))
	if late.Code != http.StatusServiceUnavailable {
		t.Fatalf("new callback after drain = %d, want 503", late.Code)
	}
	_, grpcErr := srv.coordinator.UnaryInterceptor(t.Context(), nil, &grpc.UnaryServerInfo{}, func(context.Context, any) (any, error) {
		t.Fatal("new gRPC work reached handler after drain")
		return nil, nil
	})
	if status.Code(grpcErr) != codes.Unavailable {
		t.Fatalf("new gRPC work after drain = %v, want unavailable", grpcErr)
	}

	drainCtx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	if err := srv.Drain(drainCtx, 0); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Drain with active work = %v, want deadline exceeded", err)
	}
	cancel()
	select {
	case <-callbackDone:
	case <-time.After(time.Second):
		t.Fatal("drain deadline did not cancel and join admitted callback")
	}
	if processClosed.Load() {
		t.Fatal("drain closed ToolHive before lifecycle shutdown")
	}
	if err := srv.Close(t.Context()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !processClosed.Load() {
		t.Fatal("production shutdown did not close ToolHive process")
	}
}
