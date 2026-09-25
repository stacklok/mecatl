package server_test

import (
	"context"
	"testing"
	"time"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

// TestGRPCCreateSessionUnspecifiedModeUsesServerDefault pins that a gRPC
// CreateSession leaving mode UNSPECIFIED starts in the server's configured
// DefaultMode (the session half of the operator's permission mode, ADR 0365),
// while an explicit mode still wins.
func TestGRPCCreateSessionUnspecifiedModeUsesServerDefault(t *testing.T) {
	llm := mockllm.New()
	svc, err := newPlacementTeamTestService(server.Config{
		Engine: agent.NewEngine(agent.Deps{
			LLM:     llm,
			Catalog: tool.NewCatalog(),
			Policy:  permpolicy.NewPolicy(allowRules(), nil),
			Model:   "test-model",
		}),
		Store:               memstore.New(),
		PlacementProvider:   testPlacementProvider{},
		PlacementScope:      "test",
		Now:                 func() time.Time { return time.Unix(0, 0) },
		DefaultCapabilities: llm.Capabilities(),
		DefaultMode:         session.ModePlan,
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	client, cleanup := dialGRPC(t, svc)
	defer cleanup()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	for _, tc := range []struct {
		req  mecatlv1.PermissionMode
		want session.PermissionMode
	}{
		{mecatlv1.PermissionMode_PERMISSION_MODE_UNSPECIFIED, session.ModePlan},
		{mecatlv1.PermissionMode_PERMISSION_MODE_DEFAULT, session.ModeDefault},
		{mecatlv1.PermissionMode_PERMISSION_MODE_ACCEPT_EDITS, session.ModeAccept},
	} {
		cs, err := client.CreateSession(ctx, &mecatlv1.CreateSessionRequest{Mode: tc.req})
		if err != nil {
			t.Fatalf("CreateSession(%s): %v", tc.req, err)
		}
		sess, err := svc.GetSession(ctx, session.SessionID(cs.GetSessionId()))
		if err != nil {
			t.Fatalf("GetSession: %v", err)
		}
		if sess.Mode != tc.want {
			t.Errorf("CreateSession(%s) started in %q, want %q", tc.req, sess.Mode, tc.want)
		}
	}
}
