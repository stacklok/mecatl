package server

import (
	"context"
	"errors"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/session"
)

// agentDefinitionEngineResult supplies the durable authority required by an
// agent-bound snapshot; brokerEngineResult alone is an ordinary-session fixture.
func agentDefinitionEngineResult(name string) SessionEngineResult {
	res := brokerEngineResult()
	res.AgentDefinitionName = name
	res.BuiltForMode = session.ModeDefault
	res.Authority = session.Authority{
		Provenance:    "agent-def:test",
		CapabilitySet: governance.CapabilitySet{Tools: []string{"Read"}},
		Ceiling:       &governance.CapabilitySet{Tools: []string{"Read"}},
	}
	return res
}

// agentDefLimitsEngine builds an AgentDefSessionEngineFactory whose
// SessionEngineResult.Limits simulates a def that caps MaxTurns/MaxToolCalls
// to maxTurns regardless of what raw request limits the caller sent —
// mirroring the real agentDefSessionEngineFactory's tighten-only clamp,
// without needing a real AgentDef/registry for this package-level test.
func agentDefLimitsEngine(maxTurns int) AgentDefSessionEngineFactory {
	return func(_ context.Context, _ ProviderSelector, _ SessionProfile, mode session.PermissionMode, _ session.Limits, name string) (SessionEngineResult, error) {
		res := agentDefinitionEngineResult(name)
		limits := session.Limits{MaxTurns: maxTurns, MaxToolCalls: maxTurns}
		res.Limits = &limits
		res.BuiltForMode = mode // pass through: this fake def imposes no permissionMode clamp of its own.
		return res, nil
	}
}

// newAgentDefRetryService builds a Service sharing store against a def that
// always clamps to a 10-turn cap, for the explicit-ID-retry tests below. The
// retry-classification path (createRequest.matches) only fires when the
// session exists in the STORE but has no LIVE in-memory engine in the calling
// Service instance (reserveCreateID's own "already in use" short-circuit fires
// first otherwise) — exactly like the existing
// TestCreateSessionWithSessionIDCrossServiceRetryIsIdempotent, so each retry
// below uses a FRESH Service instance sharing the same store, never the
// original instance.
func newAgentDefRetryService(t *testing.T, store *memstore.Store) *Service {
	t.Helper()
	svc, err := NewService(Config{
		Engine:                brokerEngineResult().Engine,
		Store:                 store,
		PlacementProvider:     brokerPlacementProvider{},
		PlacementScope:        "test",
		OwnershipEnforced:     true,
		AgentDefSessionEngine: agentDefLimitsEngine(10),
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	t.Cleanup(func() { svc.Close() })
	return svc
}

// TestCreateSessionExplicitIDRetryRejectsDifferentAgentBinding pins a gap
// found by external review: createRequest.matches never compared
// AgentDefinitionName, so a same-owner explicit-ID retry could silently swap
// between an agent-bound and an ordinary (or differently-bound) session —
// defeating ADR 0353's durable fixed-identity and restricted-catalog
// guarantees for a caller trusting the explicit ID to mean "the same session."
// Mutation-verified: both rejection subtests below failed (retry succeeded,
// silently returning the first session) before AgentDefinitionName was added
// to createRequest/matches.
func TestCreateSessionExplicitIDRetryRejectsDifferentAgentBinding(t *testing.T) {
	store := memstore.New()
	alice := session.Principal{Issuer: "https://issuer.example", Subject: "alice"}
	ctx := session.WithPrincipal(context.Background(), &alice)
	const id session.SessionID = "retry-agent-binding"

	first, err := newAgentDefRetryService(t, store).CreateSessionWithProfile(ctx, session.ModeDefault, session.Limits{},
		ProviderSelector{}, ProfileDefault, WithSessionID(id), WithAgentDefinitionName("reviewer"))
	if err != nil {
		t.Fatalf("first CreateSession: %v", err)
	}
	if first.AgentDefinitionName != "reviewer" {
		t.Fatalf("first.AgentDefinitionName = %q, want reviewer", first.AgentDefinitionName)
	}

	t.Run("retry naming a DIFFERENT def is rejected, not silently granted the existing session", func(t *testing.T) {
		_, err := newAgentDefRetryService(t, store).CreateSessionWithProfile(ctx, session.ModeDefault, session.Limits{},
			ProviderSelector{}, ProfileDefault, WithSessionID(id), WithAgentDefinitionName("escalator"))
		if err == nil {
			t.Fatal("retry with a different agent_definition_name succeeded — it must be rejected (session id retried with a different request)")
		}
	})

	t.Run("retry naming NO def at all is rejected, not silently granted the agent-bound session", func(t *testing.T) {
		_, err := newAgentDefRetryService(t, store).CreateSessionWithProfile(ctx, session.ModeDefault, session.Limits{},
			ProviderSelector{}, ProfileDefault, WithSessionID(id))
		if err == nil {
			t.Fatal("retry omitting agent_definition_name succeeded — it must be rejected, not silently return the agent-bound session")
		}
	})

	t.Run("retry naming the SAME def is idempotent", func(t *testing.T) {
		again, err := newAgentDefRetryService(t, store).CreateSessionWithProfile(ctx, session.ModeDefault, session.Limits{},
			ProviderSelector{}, ProfileDefault, WithSessionID(id), WithAgentDefinitionName("reviewer"))
		if err != nil {
			t.Fatalf("retry with the same binding: %v", err)
		}
		if again.ID != id || again.AgentDefinitionName != "reviewer" {
			t.Fatalf("retry = %+v, want the same agent-bound session back", again)
		}
	})
}

// Explicit-ID retries compare original intent, not effective clamps or a newly
// resolved definition. memstore serializes snapshots on every save/load.
func TestCreateSessionAgentIntentSurvivesSnapshotAndFreshService(t *testing.T) {
	store := memstore.New()
	alice := session.Principal{Issuer: "https://issuer.example", Subject: "alice"}
	ctx := session.WithPrincipal(context.Background(), &alice)
	const id session.SessionID = "retry-agent-intent"
	selector := ProviderSelector{ProviderID: "requested", ModelID: "raw/vendor:model@revision", ReasoningEffort: "high"}
	limits := session.Limits{MaxTurns: 1000, MaxToolCalls: 200, MaxConsecutiveFailures: 3}
	svc := newAgentDefRetryService(t, store)
	svc.cfg.AgentDefSessionEngine = func(context.Context, ProviderSelector, SessionProfile, session.PermissionMode, session.Limits, string) (SessionEngineResult, error) {
		res := agentDefinitionEngineResult("reviewer")
		res.BuiltForMode = session.ModePlan
		res.Limits = &session.Limits{MaxTurns: 1, MaxToolCalls: 2, MaxConsecutiveFailures: 1}
		res.ProviderID, res.ModelID, res.ReasoningEffort = "resolved", selector.ModelID, "low"
		return res, nil
	}
	first, err := svc.CreateSessionWithProfile(ctx, session.ModeDefault, limits, selector, ProfileDefault, WithSessionID(id), WithAgentDefinitionName("reviewer"))
	if err != nil {
		t.Fatal(err)
	}
	if first.Mode != session.ModePlan || first.Limits.MaxTurns != 1 || first.AgentCreateIntent == "" || first.ProviderID != "resolved" || first.ModelID != selector.ModelID || first.ReasoningEffort != "low" {
		t.Fatalf("effective creation metadata = %+v", first)
	}
	for _, tc := range []struct {
		name     string
		mode     session.PermissionMode
		limits   session.Limits
		selector ProviderSelector
		binding  string
		ok       bool
	}{
		{"same", session.ModeDefault, limits, selector, "reviewer", true},
		{"mode", session.ModePlan, limits, selector, "reviewer", false},
		{"turns", session.ModeDefault, session.Limits{MaxTurns: 50, MaxToolCalls: 200, MaxConsecutiveFailures: 3}, selector, "reviewer", false},
		{"tools", session.ModeDefault, session.Limits{MaxTurns: 1000, MaxToolCalls: 201, MaxConsecutiveFailures: 3}, selector, "reviewer", false},
		{"failures", session.ModeDefault, session.Limits{MaxTurns: 1000, MaxToolCalls: 200, MaxConsecutiveFailures: 4}, selector, "reviewer", false},
		{"provider", session.ModeDefault, limits, ProviderSelector{ProviderID: "different", ModelID: selector.ModelID, ReasoningEffort: "high"}, "reviewer", false},
		{"model", session.ModeDefault, limits, ProviderSelector{ProviderID: "requested", ModelID: "different", ReasoningEffort: "high"}, "reviewer", false},
		{"effort", session.ModeDefault, limits, ProviderSelector{ProviderID: "requested", ModelID: selector.ModelID, ReasoningEffort: "low"}, "reviewer", false},
		{"binding", session.ModeDefault, limits, selector, "different", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			retry := newAgentDefRetryService(t, store)
			retry.cfg.AgentDefSessionEngine = func(context.Context, ProviderSelector, SessionProfile, session.PermissionMode, session.Limits, string) (SessionEngineResult, error) {
				t.Fatal("retry re-resolved definition or rebuilt resources")
				return SessionEngineResult{}, nil
			}
			retry.cfg.ResolveSessionModel = func(ProviderSelector, session.PermissionMode) ResolvedModel {
				t.Fatal("persisted agent identity was re-resolved")
				return ResolvedModel{}
			}
			again, err := retry.CreateSessionWithProfile(ctx, tc.mode, tc.limits, tc.selector, ProfileDefault, WithSessionID(id), WithAgentDefinitionName(tc.binding))
			if !tc.ok {
				if !errors.Is(err, ErrInvalidArgument) {
					t.Fatalf("changed intent = %v, want InvalidArgument", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if again.Mode != first.Mode || again.Limits != first.Limits || again.AgentCreateIntent != first.AgentCreateIntent || again.ProviderID != first.ProviderID || again.ModelID != first.ModelID || again.ReasoningEffort != first.ReasoningEffort {
				t.Fatalf("retry changed effective metadata: %+v", again)
			}
			rm := retry.resolvedModelFor(again)
			if rm.ProviderID != first.ProviderID || rm.ModelID != first.ModelID || rm.ReasoningEffort != first.ReasoningEffort {
				t.Fatalf("reloaded identity = %+v", rm)
			}
		})
	}
	first.AgentCreateIntent = ""
	if err := store.Save(ctx, first); err != nil {
		t.Fatal(err)
	}
	_, err = newAgentDefRetryService(t, store).CreateSessionWithProfile(ctx, session.ModeDefault, limits, selector, ProfileDefault, WithSessionID(id), WithAgentDefinitionName("reviewer"))
	if !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("legacy missing intent = %v, want fail-closed conflict", err)
	}
}
