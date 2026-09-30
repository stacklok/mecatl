package server

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/mcp"
)

// TestSessionScopedAgentIdentity_Scenario2_ForkClearPreserveBindingAndCatalog
// pins AC2.3: ForkSession/ClearSession always inherit the source session's
// agent_definition_name unconditionally (Profile-style, no request-side
// override field) and rebuild the SAME restricted catalog for the successor —
// the successor's registered per-session engine must be the one
// AgentDefSessionEngine built for the def, never one built by the ordinary
// SessionEngine factory (which would mean the deployment's default catalog
// leaked in).
func TestSessionScopedAgentIdentity_Scenario2_ForkClearPreserveBindingAndCatalog(t *testing.T) {
	for _, tc := range []struct {
		name  string
		clear bool
	}{
		{name: "fork"},
		{name: "clear", clear: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := memstore.New()
			restrictedEngine := agent.NewEngine(agent.Deps{
				LLM:     mockllm.New(mockllm.TextTurn("restricted")),
				Catalog: tool.NewCatalog(),
				Policy:  permpolicy.NewPolicy(nil, nil),
			})
			var agentDefCalls int32
			svc, err := NewService(Config{
				Engine:            brokerEngineResult().Engine,
				Store:             store,
				PlacementProvider: brokerPlacementProvider{}, PlacementScope: "test",
				AgentDefSessionEngine: func(_ context.Context, _ ProviderSelector, _ SessionProfile, _ session.PermissionMode, _ session.Limits, defName string) (SessionEngineResult, error) {
					atomic.AddInt32(&agentDefCalls, 1)
					if defName != "release-reviewer" {
						t.Errorf("AgentDefSessionEngine called with defName=%q, want %q", defName, "release-reviewer")
					}
					return SessionEngineResult{Engine: restrictedEngine, Close: func() error { return nil }}, nil
				},
				// Wired so that IF the successor rebuild ever regressed into the
				// ordinary catalog path, the test fails on the precise engine-identity
				// assertion below rather than on an unrelated "no session-engine
				// factory configured" config error.
				SessionEngine: func(context.Context, ProviderSelector, []mcp.ServerConfig, SessionProfile, string, session.PermissionMode) (SessionEngineResult, error) {
					t.Fatal("agent-bound successor used the ordinary SessionEngine factory — the deployment default catalog leaked in")
					return SessionEngineResult{}, nil
				},
			})
			if err != nil {
				t.Fatalf("NewService: %v", err)
			}
			defer svc.Close()

			source, err := svc.CreateSessionWithProfile(context.Background(), session.ModeDefault, session.Limits{},
				ProviderSelector{}, ProfileDefault, WithAgentDefinitionName("release-reviewer"))
			if err != nil {
				t.Fatalf("CreateSession: %v", err)
			}

			var successorID session.SessionID
			if tc.clear {
				successorID, err = svc.ClearSessionSuccessor(context.Background(), source.ID, SuccessorPlacement{})
			} else {
				successorID, err = svc.ForkSessionSuccessor(context.Background(), ForkSuccessorRequest{Source: source.ID})
			}
			if err != nil {
				t.Fatalf("successor: %v", err)
			}

			successor, err := store.Load(context.Background(), successorID)
			if err != nil {
				t.Fatalf("load successor: %v", err)
			}
			if successor.AgentDefinitionName != "release-reviewer" {
				t.Fatalf("successor.AgentDefinitionName = %q, want %q (AC2.3: Fork/Clear must inherit it unconditionally)",
					successor.AgentDefinitionName, "release-reviewer")
			}

			svc.mu.Lock()
			registered, hasEngine := svc.sessionEngines[successorID]
			svc.mu.Unlock()
			if !hasEngine {
				t.Fatal("successor has no registered per-session engine — the restricted catalog was never rebuilt")
			}
			if registered.engine != restrictedEngine {
				t.Fatal("successor's registered engine is not the one AgentDefSessionEngine built — the def's restricted catalog was not rebuilt for the successor")
			}
			if atomic.LoadInt32(&agentDefCalls) == 0 {
				t.Fatal("AgentDefSessionEngine was never invoked for the successor")
			}
		})
	}
}

// TestSessionScopedAgentIdentity_Scenario2_ForkClearSkipMCPBrokerAttachment
// pins AC2.4: a forked/cleared agent-bound successor's engine build also skips
// Config.MCPBroker attachment entirely, mirroring AC1.10's create-time skip
// (TestSessionScopedAgentIdentity_Scenario1_MCPBrokerAttachmentSkipped) for
// the DISTINCT construction path placement_successor.go uses (a new engine
// built directly, never routed through the create-time path AC1.10 covers).
func TestSessionScopedAgentIdentity_Scenario2_ForkClearSkipMCPBrokerAttachment(t *testing.T) {
	for _, tc := range []struct {
		name  string
		clear bool
	}{
		{name: "fork"},
		{name: "clear", clear: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runtime := testBrokerRuntime(t)
			defer runtime.Close()
			var attachCalled int32

			svc, err := NewService(Config{
				Engine:            brokerEngineResult().Engine,
				Store:             memstore.New(),
				PlacementProvider: brokerPlacementProvider{}, PlacementScope: "test",
				MCPBroker: countingBrokerService{inner: runtime, attachCalled: &attachCalled},
				AgentDefSessionEngine: func(context.Context, ProviderSelector, SessionProfile, session.PermissionMode, session.Limits, string) (SessionEngineResult, error) {
					return brokerEngineResult(), nil
				},
			})
			if err != nil {
				t.Fatalf("NewService: %v", err)
			}
			defer svc.Close()

			source, err := svc.CreateSessionWithProfile(context.Background(), session.ModeDefault, session.Limits{},
				ProviderSelector{}, ProfileDefault, WithAgentDefinitionName("release-reviewer"))
			if err != nil {
				t.Fatalf("CreateSession: %v", err)
			}
			if got := atomic.LoadInt32(&attachCalled); got != 0 {
				t.Fatalf("MCPBroker.AttachSession was called %d time(s) at CREATE — precondition for this test is broken (AC1.10)", got)
			}

			if tc.clear {
				_, err = svc.ClearSessionSuccessor(context.Background(), source.ID, SuccessorPlacement{})
			} else {
				_, err = svc.ForkSessionSuccessor(context.Background(), ForkSuccessorRequest{Source: source.ID})
			}
			if err != nil {
				t.Fatalf("successor: %v", err)
			}

			if got := atomic.LoadInt32(&attachCalled); got != 0 {
				t.Fatalf("MCPBroker.AttachSession was called %d time(s) for an agent-bound fork/clear successor — it must be skipped entirely (AC2.4)", got)
			}
		})
	}
}
