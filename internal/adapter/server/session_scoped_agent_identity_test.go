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

// TestSessionScopedAgentIdentity_Scenario2_ResponseEchoesBoundName pins AC2.2
// (docs/acceptance/session-scoped-agent-identity.md): CreateSessionResponse's
// resolved_agent_definition_name echoes the request's agent_definition_name
// VERBATIM, mirroring the existing resolved_model echo discipline. This task
// is plumbing only — the value is stamped and echoed, not yet used to build
// the session's engine or restrict its catalog.
func TestSessionScopedAgentIdentity_Scenario2_ResponseEchoesBoundName(t *testing.T) {
	shared := agent.NewEngine(agent.Deps{
		LLM:     mockllm.New(mockllm.TextTurn("SHARED")),
		Catalog: tool.NewCatalog(),
		Policy:  permpolicy.NewPolicy(nil, nil),
		Model:   "test-model",
	})
	svc, err := newPlacementTestService(server.Config{
		Engine:        shared,
		Store:         memstore.New(),
		DefaultLimits: session.Limits{MaxTurns: 10, MaxToolCalls: 20},
		Now:           func() time.Time { return time.Unix(0, 0) },
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	h := server.NewHarnessServer(svc)

	t.Run("named agent_definition_name echoes verbatim", func(t *testing.T) {
		resp, err := h.CreateSession(context.Background(), &mecatlv1.CreateSessionRequest{
			AgentDefinitionName: "release-reviewer",
		})
		if err != nil {
			t.Fatalf("CreateSession: %v", err)
		}
		if got := resp.GetResolvedAgentDefinitionName(); got != "release-reviewer" {
			t.Fatalf("resolved_agent_definition_name = %q, want %q", got, "release-reviewer")
		}
	})

	t.Run("omitted agent_definition_name echoes empty (byte-identical default)", func(t *testing.T) {
		resp, err := h.CreateSession(context.Background(), &mecatlv1.CreateSessionRequest{})
		if err != nil {
			t.Fatalf("CreateSession: %v", err)
		}
		if got := resp.GetResolvedAgentDefinitionName(); got != "" {
			t.Fatalf("resolved_agent_definition_name = %q, want empty for a default session", got)
		}
	})
}
