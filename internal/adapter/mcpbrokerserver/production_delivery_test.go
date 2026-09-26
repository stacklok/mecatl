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
)

func TestSingletonBrokerRemediation_Scenario3_ProductionReadinessUsesRealDependencies(t *testing.T) {
	issuer := newIdentityFixture(t)
	t.Setenv("../mcpbroker/testdata/client-secret", "offline-secret")
	var process *mcpbroker.Process
	srv, err := newBrokerHost(t.Context(), hostConfig{
		WorkloadJWT: productionOIDC(issuer, time.Minute),
		Runtime: func(ctx context.Context) (brokerRuntime, error) {
			var buildErr error
			process, buildErr = mcpbroker.NewToolHiveProcess(ctx, mcpbroker.ToolHiveConfig{
				CallbackURL: "https://broker.example/callback",
				Profiles: []mcpbroker.ToolHiveProfile{{
					Name: "github", URL: "https://mcp.example/mcp", Auth: "oauth",
					OAuth: &mcpbroker.ToolHiveOAuth{
						AuthorizationEndpoint: "https://identity.example/authorize", TokenEndpoint: "https://identity.example/token",
						ClientID: "readiness-client", ClientSecretFile: "../mcpbroker/testdata/client-secret",
					},
					Static: []mcpbroker.StaticTool{{Name: "mcp__github__read", Schema: json.RawMessage(`{"type":"object"}`), ReadOnly: true}},
				}},
			})
			if buildErr != nil {
				return brokerRuntime{}, buildErr
			}
			return brokerRuntime{Service: process.Runtime, Handlers: process.Handlers, CallbackPath: "/callback", Close: process.Close}, nil
		},
		ReadinessTimeout: time.Second,
	})
	if err != nil {
		t.Fatalf("production New: %v", err)
	}
	defer func() { _ = srv.close(context.Background()) }()
	if process == nil || !srv.ready(t.Context()) {
		t.Fatal("production OIDC and ToolHive dependencies did not open readiness")
	}
	if err := process.Close(); err != nil {
		t.Fatalf("close ToolHive dependency: %v", err)
	}
	if srv.ready(t.Context()) {
		t.Fatal("readiness stayed open after the production ToolHive process failed")
	}
	srv.beginDrain()
	if srv.ready(t.Context()) {
		t.Fatal("readiness stayed open after admission closed")
	}
}

func TestSingletonBrokerRemediation_Scenario3_ProductionDrainAndCleanup(t *testing.T) {
	issuer := newIdentityFixture(t)
	t.Setenv("../mcpbroker/testdata/client-secret", "offline-secret")
	entered := make(chan struct{})
	operationDone := make(chan struct{})
	var processClosed atomic.Bool
	srv, err := newBrokerHost(t.Context(), hostConfig{
		WorkloadJWT: productionOIDC(issuer, time.Minute),
		Runtime: func(ctx context.Context) (brokerRuntime, error) {
			process, buildErr := mcpbroker.NewToolHiveProcess(ctx, mcpbroker.ToolHiveConfig{
				CallbackURL: "https://broker.example/callback",
				Profiles: []mcpbroker.ToolHiveProfile{{
					Name: "github", URL: "https://mcp.example/mcp", Auth: "oauth",
					OAuth:  &mcpbroker.ToolHiveOAuth{AuthorizationEndpoint: "https://identity.example/authorize", TokenEndpoint: "https://identity.example/token", ClientID: "drain-client", ClientSecretFile: "../mcpbroker/testdata/client-secret"},
					Static: []mcpbroker.StaticTool{{Name: "mcp__github__read", Schema: json.RawMessage(`{"type":"object"}`), ReadOnly: true}},
				}},
			})
			if buildErr != nil {
				return brokerRuntime{}, buildErr
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
			return brokerRuntime{Service: process.Runtime, Handlers: handlers, CallbackPath: "/callback", Close: closeProcess}, nil
		},
		ReadinessTimeout: time.Second,
	})
	if err != nil {
		t.Fatalf("production New: %v", err)
	}
	callbackDone := make(chan struct{})
	go func() {
		defer close(callbackDone)
		srv.httpHandler().ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/callback", nil))
	}()
	<-entered

	srv.beginDrain()
	late := httptest.NewRecorder()
	srv.httpHandler().ServeHTTP(late, httptest.NewRequest(http.MethodGet, "/callback", nil))
	if late.Code != http.StatusServiceUnavailable {
		t.Fatalf("new callback after drain = %d, want 503", late.Code)
	}
	_, grpcErr := srv.admission.UnaryInterceptor(t.Context(), nil, &grpc.UnaryServerInfo{}, func(context.Context, any) (any, error) {
		t.Fatal("new gRPC work reached handler after drain")
		return nil, nil
	})
	if status.Code(grpcErr) != codes.Unavailable {
		t.Fatalf("new gRPC work after drain = %v, want unavailable", grpcErr)
	}

	drainCtx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	if err := srv.drain(drainCtx, 0); !errors.Is(err, context.DeadlineExceeded) {
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
	if err := srv.close(t.Context()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !processClosed.Load() {
		t.Fatal("production shutdown did not close ToolHive process")
	}
}
