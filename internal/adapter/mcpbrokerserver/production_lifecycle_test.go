package mcpbrokerserver

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	brokerv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/broker/v1"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/mcpbroker"
)

func TestSingletonBrokerRemediation_Scenario5_ProductionLifecycleUsesSharedFactory(t *testing.T) {
	issuer := newIdentityFixture(t)
	t.Setenv("MECATL_LIFECYCLE_SECRET", "offline-secret")
	lifecycle, err := NewProduction(t.Context(), ProductionConfig{
		PublicAddress: "127.0.0.1:0", AdminAddress: "127.0.0.1:0",
		TLSConfig: &tls.Config{Certificates: issuer.server.TLS.Certificates, MinVersion: tls.VersionTLS12},
		OIDC:      productionOIDC(issuer, time.Minute),
		ToolHive: mcpbroker.ToolHiveConfig{CallbackURL: "https://broker.example/callback", Profiles: []mcpbroker.ToolHiveProfile{{
			Name: "github", URL: "https://mcp.example/mcp", Auth: "oauth",
			OAuth:  &mcpbroker.ToolHiveOAuth{AuthorizationEndpoint: "https://identity.example/authorize", TokenEndpoint: "https://identity.example/token", ClientID: "lifecycle-client", ClientSecretEnv: "MECATL_LIFECYCLE_SECRET"},
			Static: []mcpbroker.StaticTool{{Name: "read", Schema: json.RawMessage(`{"type":"object"}`), ReadOnly: true}},
		}}},
		PropagationWait: time.Millisecond, DrainTimeout: time.Second,
		RuntimeLimits: mcpbroker.Limits{MaxLogicalSessions: 1, LogicalRetention: time.Hour, SweepInterval: time.Minute, MaxPendingStates: 1},
	})
	if err != nil {
		t.Fatalf("NewProduction: %v", err)
	}
	lifecycle.Start()
	if !lifecycle.Ready(t.Context()) {
		t.Fatal("shared production lifecycle never became ready")
	}
	principalCtx := session.WithPrincipal(t.Context(), &session.Principal{Subject: "runtime-limit-test"})
	if _, err := lifecycle.broker.rpc.Attach(principalCtx, &brokerv1.AttachRequest{SessionId: "runtime-one"}); err != nil {
		t.Fatalf("first runtime attach: %v", err)
	}
	if _, err := lifecycle.broker.rpc.Attach(principalCtx, &brokerv1.AttachRequest{SessionId: "runtime-two"}); status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("second runtime attach = %v, want underlying runtime capacity rejection", err)
	}
	closeCtx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := lifecycle.Close(closeCtx); err != nil {
		t.Fatalf("production lifecycle Close: %v", err)
	}
}
