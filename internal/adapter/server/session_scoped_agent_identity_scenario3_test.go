package server

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/mcp"
)

// newAgentDefBoundService builds a minimal Service wired with an
// AgentDefSessionEngine factory (the only per-session engine factory an
// agent-bound create needs), mirroring
// TestSessionScopedAgentIdentity_Scenario1_MCPBrokerAttachmentSkipped's fixture
// shape. It never configures Config.MCPBroker/LearnedSkills and never sets a
// provider/model selector — the "boring" configuration AC3.4 names, under
// which needsRehydration()'s other clauses are all false. SessionEngine is
// also configured (never used by an agent-bound session) so a disabled
// LoadSessionWithMCP guard is not masked by its own separate
// "no per-session engine configured" precondition.
func newAgentDefBoundService(t *testing.T) *Service {
	t.Helper()
	svc, err := NewService(Config{
		Engine:            brokerEngineResult().Engine,
		Store:             memstore.New(),
		PlacementProvider: brokerPlacementProvider{},
		PlacementScope:    "test",
		AgentDefSessionEngine: func(_ context.Context, _ ProviderSelector, _ SessionProfile, mode session.PermissionMode, _ session.Limits, _ string) (SessionEngineResult, error) {
			// A real factory (agentDefSessionEngineFactory) always stamps
			// BuiltForMode with the resolved/clamped mode; createPerSessionEngine
			// reassigns its local `mode` FROM this field (ADR 0353 tighten-only
			// clamp), so a stub leaving it "" would silently blank sess.Mode.
			res := brokerEngineResult()
			res.BuiltForMode = mode
			return res, nil
		},
		SessionEngine: func(context.Context, ProviderSelector, []mcp.ServerConfig, SessionProfile, string, session.PermissionMode) (SessionEngineResult, error) {
			return brokerEngineResult(), nil
		},
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	t.Cleanup(svc.Close)
	return svc
}

// newAgentDefBoundServiceOverStore is newAgentDefBoundService, but takes an
// explicit shared port.SessionStore instead of minting its own memstore — so a
// test can build TWO Service instances over the SAME durable store, the
// smallest reproduction of a process restart: a second Service's in-memory
// sessionEngines/runs maps start EMPTY even though the store already holds a
// session an earlier Service created. SharedEngineRoot is pinned to the exact
// governance root brokerPlacementProvider reports ("/workspace"), so
// placementNeedsEngine's OWN separate "different governance root" clause is
// false here — isolating needsRehydration's AgentDefinitionName clause as the
// ONLY thing standing between this agent-bound session and a silent fall-
// through onto the deployment's default shared engine (AC3.4's exact "boring
// config" framing).
func newAgentDefBoundServiceOverStore(t *testing.T, store *memstore.Store) *Service {
	t.Helper()
	svc, err := NewService(Config{
		Engine:            brokerEngineResult().Engine,
		Store:             store,
		PlacementProvider: brokerPlacementProvider{},
		PlacementScope:    "test",
		SharedEngineRoot:  "/workspace",
		AgentDefSessionEngine: func(_ context.Context, _ ProviderSelector, _ SessionProfile, mode session.PermissionMode, _ session.Limits, _ string) (SessionEngineResult, error) {
			res := brokerEngineResult()
			res.BuiltForMode = mode
			return res, nil
		},
		SessionEngine: func(context.Context, ProviderSelector, []mcp.ServerConfig, SessionProfile, string, session.PermissionMode) (SessionEngineResult, error) {
			return brokerEngineResult(), nil
		},
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	t.Cleanup(svc.Close)
	return svc
}

func createAgentDefBoundSession(t *testing.T, svc *Service, defName string) *session.Session {
	t.Helper()
	sess, err := svc.CreateSessionWithProfile(context.Background(), session.ModeDefault, session.Limits{},
		ProviderSelector{}, ProfileDefault, WithAgentDefinitionName(defName))
	if err != nil {
		t.Fatalf("CreateSessionWithProfile: %v", err)
	}
	if sess.AgentDefinitionName != defName {
		t.Fatalf("AgentDefinitionName = %q, want %q", sess.AgentDefinitionName, defName)
	}
	return sess
}

// TestSessionScopedAgentIdentity_Scenario3_SetModeRejected pins AC3.3: SetMode
// is rejected with InvalidArgument outright for an agent-bound session — the
// mode is fixed for the session's entire lifetime, never merely clamped.
func TestSessionScopedAgentIdentity_Scenario3_SetModeRejected(t *testing.T) {
	svc := newAgentDefBoundService(t)
	sess := createAgentDefBoundSession(t, svc, "release-reviewer")

	if _, err := svc.SetMode(context.Background(), sess.ID, session.ModePlan); err == nil || !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("SetMode on an agent-bound session = %v, want an ErrInvalidArgument rejection", err)
	}

	reloaded, err := svc.GetSession(context.Background(), sess.ID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if reloaded.Mode != session.ModeDefault {
		t.Fatalf("Mode = %q after a rejected SetMode, want unchanged %q", reloaded.Mode, session.ModeDefault)
	}
}

// TestSessionScopedAgentIdentity_Scenario3_LoadSessionWithMCPRejected pins
// AC3.5: LoadSessionWithMCP is rejected with InvalidArgument for an
// agent-bound session whenever the caller supplies client MCP servers on
// resume — mirroring the create-time AC1.2 guard.
func TestSessionScopedAgentIdentity_Scenario3_LoadSessionWithMCPRejected(t *testing.T) {
	svc := newAgentDefBoundService(t)
	sess := createAgentDefBoundSession(t, svc, "release-reviewer")

	specs := []mcp.ServerConfig{{Name: "srv", URL: "https://example.invalid/mcp"}}
	if _, err := svc.LoadSessionWithMCP(context.Background(), sess.ID, specs); err == nil || !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("LoadSessionWithMCP with client MCP servers on an agent-bound session = %v, want an ErrInvalidArgument rejection", err)
	}

	// An EMPTY spec list takes the ordinary LoadSession fast path and must still
	// succeed — the guard rejects only a caller actually trying to widen MCP
	// scope, not every resume of an agent-bound session.
	if _, err := svc.LoadSessionWithMCP(context.Background(), sess.ID, nil); err != nil {
		t.Fatalf("LoadSessionWithMCP with no client MCP servers must still succeed for an agent-bound session, got: %v", err)
	}
}

// TestSessionScopedAgentIdentity_Scenario3_BrokerAuthorizationAndWorkspaceEnrollmentSkipped
// pins AC3.6: the lazy per-tool broker-OAuth grant flow
// (rebuildGrantedAuthorizationEngine) and the workspace/bundle enrollment RPC
// (ConnectWorkspaceServices) are both skipped/rejected for an agent-bound
// session, so its catalog is never widened by either.
func TestSessionScopedAgentIdentity_Scenario3_BrokerAuthorizationAndWorkspaceEnrollmentSkipped(t *testing.T) {
	t.Run("rebuildGrantedAuthorizationEngine", func(t *testing.T) {
		svc := newAgentDefBoundService(t)
		now := time.Now()
		sess := session.New("agent-bound-authz", session.ModeDefault,
			session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/workspace", Revision: "in-tree-v1"}, session.Limits{}, now)
		sess.AgentDefinitionName = "release-reviewer"
		call := session.NewToolCall("protected-call", "protected", []byte(`{}`))
		pending := session.PendingAuthorization{
			Authorization: session.ExternalAuthorization{ID: "authorization:1", Binding: "opaque-binding", ExpiresAt: now.Add(time.Hour)},
			Call:          call,
		}
		if err := sess.BeginTurn(); err != nil {
			t.Fatalf("BeginTurn: %v", err)
		}
		if err := sess.RecordAssistant(session.NewAssistantMessage("", "", []session.ToolCall{call})); err != nil {
			t.Fatalf("RecordAssistant: %v", err)
		}
		if err := sess.PauseForAuthorization(pending); err != nil {
			t.Fatalf("PauseForAuthorization: %v", err)
		}
		claimed, err := sess.ClaimAuthorization()
		if err != nil {
			t.Fatalf("ClaimAuthorization: %v", err)
		}
		if err := svc.rebuildGrantedAuthorizationEngine(context.Background(), sess, claimed, nil); err == nil || !errors.Is(err, ErrInvalidArgument) {
			t.Fatalf("rebuildGrantedAuthorizationEngine for an agent-bound session = %v, want an ErrInvalidArgument rejection", err)
		}
		// The claim must be restored (never stranded) by the rejection.
		if restored, ok := sess.PendingAuthorization(); !ok || restored.Authorization.ID != pending.Authorization.ID {
			t.Fatalf("rejected rebuild did not restore the authorization claim: pending=%+v ok=%v", restored, ok)
		}
	})

	t.Run("ConnectWorkspaceServices", func(t *testing.T) {
		runtime := testBrokerRuntime(t)
		defer runtime.Close()
		svc, err := NewService(Config{
			Engine:            brokerEngineResult().Engine,
			Store:             memstore.New(),
			PlacementProvider: brokerPlacementProvider{},
			PlacementScope:    "test",
			MCPBroker:         runtime,
			AgentDefSessionEngine: func(context.Context, ProviderSelector, SessionProfile, session.PermissionMode, session.Limits, string) (SessionEngineResult, error) {
				return brokerEngineResult(), nil
			},
			SessionEngineWithTools: func(context.Context, ProviderSelector, []mcp.ServerConfig, SessionProfile, string, session.PermissionMode, []tool.Tool) (SessionEngineResult, error) {
				return brokerEngineResult(), nil
			},
		})
		if err != nil {
			t.Fatalf("NewService: %v", err)
		}
		defer svc.Close()
		sess := createAgentDefBoundSession(t, svc, "release-reviewer")

		if _, err := svc.ConnectWorkspaceServices(context.Background(), sess.ID); err == nil || !errors.Is(err, ErrInvalidArgument) {
			t.Fatalf("ConnectWorkspaceServices for an agent-bound session = %v, want an ErrInvalidArgument rejection", err)
		}
	})
}

// TestSessionScopedAgentIdentity_Scenario3_RestartFailsClosedOnBoringConfig
// pins AC3.4: an agent-bound session with an EMPTY provider/model selector,
// default (non-no-fs) profile, and a deployment with no
// Config.MCPBroker/LearnedSkills/SessionContextEngine configured — the
// "boring" configuration under which needsRehydration()'s every OTHER clause
// is false, so a fix touching only rehydrateSession would be a silent no-op —
// fails closed at engineAndEnvironmentFor (never the deployment's default
// engine) whenever its in-memory per-session engine registration is lost.
// newAgentDefBoundServiceOverStore's second Service, sharing the SAME
// memstore instance as the first but starting with EMPTY sessionEngines/runs
// maps, is the smallest reproduction of a process restart.
//
// Mutation-verified: reverting needsRehydration's AgentDefinitionName clause
// (Task F) makes every subtest below observe a silent success reusing the
// deployment's DEFAULT (non-agent-bound) shared engine, rather than an
// ErrInvalidArgument fail-closed rejection.
func TestSessionScopedAgentIdentity_Scenario3_RestartFailsClosedOnBoringConfig(t *testing.T) {
	t.Run("StartRunContent", func(t *testing.T) {
		store := memstore.New()
		svc1 := newAgentDefBoundServiceOverStore(t, store)
		sess := createAgentDefBoundSession(t, svc1, "reviewer")

		svc2 := newAgentDefBoundServiceOverStore(t, store) // "restart": a FRESH Service, empty in-memory maps, SAME store.
		if _, err := svc2.StartRunContent(context.Background(), sess.ID, "post-restart turn", nil); err == nil || !errors.Is(err, ErrInvalidArgument) {
			t.Fatalf("StartRunContent on an agent-bound session after restart = %v, want an ErrInvalidArgument fail-closed rejection", err)
		}
	})

	t.Run("RetryFailedRun", func(t *testing.T) {
		store := memstore.New()
		svc1 := newAgentDefBoundServiceOverStore(t, store)
		sess := createAgentDefBoundSession(t, svc1, "reviewer")

		// Land the session in a retry-eligible StateFailed directly on the
		// aggregate (mirroring TestMCPRuntimeConsistency_FailedStepRetryPinsOperationEntry's
		// fixture shape) — no live run is needed; RetryFailedRun's own
		// precondition checks run BEFORE engineAndEnvironmentFor, so the guard
		// under test is reached regardless of how StateFailed was produced.
		if err := sess.Fail(); err != nil {
			t.Fatalf("Fail: %v", err)
		}
		if err := sess.RecordFailureMetadata(session.RetryMetadata{Disposition: session.RetryDispositionRetryable, Progress: session.StreamProgressPrecommit}); err != nil {
			t.Fatalf("RecordFailureMetadata: %v", err)
		}
		if err := store.Save(context.Background(), sess); err != nil {
			t.Fatalf("Save failed session: %v", err)
		}

		svc2 := newAgentDefBoundServiceOverStore(t, store) // "restart"
		if _, err := svc2.RetryFailedRun(context.Background(), sess.ID); err == nil || !errors.Is(err, ErrInvalidArgument) {
			t.Fatalf("RetryFailedRun on an agent-bound session after restart = %v, want an ErrInvalidArgument fail-closed rejection", err)
		}
	})

	t.Run("CompactSession", func(t *testing.T) {
		store := memstore.New()
		svc1 := newAgentDefBoundServiceOverStore(t, store)
		sess := createAgentDefBoundSession(t, svc1, "reviewer")

		svc2 := newAgentDefBoundServiceOverStore(t, store) // "restart"
		if _, err := svc2.CompactSession(context.Background(), sess.ID, nil); err == nil || !errors.Is(err, ErrInvalidArgument) {
			t.Fatalf("CompactSession on an agent-bound session after restart = %v, want an ErrInvalidArgument fail-closed rejection", err)
		}
	})

	t.Run("resumeFromAwaiting", func(t *testing.T) {
		store := memstore.New()
		svc1 := newAgentDefBoundServiceOverStore(t, store)
		sess := createAgentDefBoundSession(t, svc1, "reviewer")

		// Park the session at StateAwaiting directly on the aggregate — no live
		// run is needed; ApproveRun's fast path only checks the (empty, for a
		// fresh Service) in-memory s.runs registry before falling through to
		// resumeFromAwaiting, which reloads this persisted snapshot.
		sess.BeginRun("run-1")
		if err := sess.BeginTurn(); err != nil {
			t.Fatalf("BeginTurn: %v", err)
		}
		if err := sess.PauseForApproval(session.PendingAsk{AskID: "ask-1", Tool: "Write", Origin: session.ApprovalOriginPermission}); err != nil {
			t.Fatalf("PauseForApproval: %v", err)
		}
		if err := store.Save(context.Background(), sess); err != nil {
			t.Fatalf("Save awaiting session: %v", err)
		}

		svc2 := newAgentDefBoundServiceOverStore(t, store) // "restart": the parked run + askRegistry die with svc1.
		if _, err := svc2.ApproveRun(context.Background(), sess.ID, "ask-1", session.VerdictAllowOnce, "run-1"); err == nil || !errors.Is(err, ErrInvalidArgument) {
			t.Fatalf("ApproveRun (resumeFromAwaiting) on an agent-bound session after restart = %v, want an ErrInvalidArgument fail-closed rejection", err)
		}
	})
}
