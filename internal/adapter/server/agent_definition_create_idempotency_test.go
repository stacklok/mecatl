package server

import (
	"context"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/session"
)

// agentDefLimitsEngine builds an AgentDefSessionEngineFactory whose
// SessionEngineResult.Limits simulates a def that caps MaxTurns/MaxToolCalls
// to maxTurns regardless of what raw request limits the caller sent —
// mirroring the real agentDefSessionEngineFactory's tighten-only clamp,
// without needing a real AgentDef/registry for this package-level test.
func agentDefLimitsEngine(maxTurns int) AgentDefSessionEngineFactory {
	return func(_ context.Context, _ ProviderSelector, _ SessionProfile, mode session.PermissionMode, _ session.Limits, _ string) (SessionEngineResult, error) {
		res := brokerEngineResult()
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

// TestCreateSessionExplicitIDRetryIgnoresRawLimitsForAgentBoundSession pins
// the companion gap: an agent-bound session's PERSISTED Limits are the def's
// own tighten-only CLAMPED effective value, never the caller's raw request
// value (createSession deliberately skips its WithDefaults fold for an
// agent-bound create so the factory alone sees "zero means not supplied").
// Comparing sess.Limits against a retry's raw r.limits directly would reject
// nearly every legitimate retry whenever the def's cap differs at all from
// whatever the caller happened to send — confirmed here with a retry sending
// a deliberately DIFFERENT raw limit that clamps to the SAME effective value.
// Mutation-verified: this failed with "session id retried with a different
// request" before matches() stopped comparing raw Limits directly for an
// agent-bound request.
func TestCreateSessionExplicitIDRetryIgnoresRawLimitsForAgentBoundSession(t *testing.T) {
	store := memstore.New()
	alice := session.Principal{Issuer: "https://issuer.example", Subject: "alice"}
	ctx := session.WithPrincipal(context.Background(), &alice)
	const id session.SessionID = "retry-agent-limits"

	first, err := newAgentDefRetryService(t, store).CreateSessionWithProfile(ctx, session.ModeDefault, session.Limits{MaxTurns: 1000},
		ProviderSelector{}, ProfileDefault, WithSessionID(id), WithAgentDefinitionName("reviewer"))
	if err != nil {
		t.Fatalf("first CreateSession: %v", err)
	}
	if first.Limits.MaxTurns != 10 {
		t.Fatalf("first.Limits.MaxTurns = %d, want 10 (the def's clamp, not the caller's raw 1000)", first.Limits.MaxTurns)
	}

	// A retry (fresh Service instance, same store) sending a DIFFERENT raw
	// value (50, not the original 1000) that the SAME def clamps to the SAME
	// effective 10 must still be accepted as the same logical create.
	again, err := newAgentDefRetryService(t, store).CreateSessionWithProfile(ctx, session.ModeDefault, session.Limits{MaxTurns: 50},
		ProviderSelector{}, ProfileDefault, WithSessionID(id), WithAgentDefinitionName("reviewer"))
	if err != nil {
		t.Fatalf("retry with different raw limits (same def, same effective clamp): %v", err)
	}
	if again.ID != id || again.Limits.MaxTurns != 10 {
		t.Fatalf("retry = %+v, want the same agent-bound session with its original 10-turn clamp intact", again)
	}
}
