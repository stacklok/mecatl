package server_test

import (
	"context"
	"sync/atomic"
	"testing"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/mcp"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

// effortEchoFactory returns a per-session-engine factory that ECHOES the resolved
// reasoning effort the composition would (here: the per-provider clamp applied to
// the selector). It records the selector it was handed so a test can assert the
// effort crossed the seam, and stamps SessionEngineResult.ReasoningEffort = echo so
// the Service's ResolvedModel echo + the session label can be asserted end to end.
func effortEchoFactory(echo string, gotSel *server.ProviderSelector) server.SessionEngineFactory {
	return func(_ context.Context, sel server.ProviderSelector, _ []mcp.ServerConfig, _ server.SessionProfile, _ string, _ session.PermissionMode) (server.SessionEngineResult, error) {
		*gotSel = sel
		eng := agent.NewEngine(agent.Deps{
			LLM:     mockllm.New(mockllm.TextTurn("PER-SESSION-REPLY")),
			Catalog: tool.NewCatalog(),
			Policy:  permpolicy.NewPolicy(nil, nil),
			Model:   "test-model",
		})
		return server.SessionEngineResult{
			Engine:          eng,
			ProviderID:      sel.ProviderID,
			ModelID:         sel.ModelID,
			ReasoningEffort: echo,
			Close:           func() error { return nil },
		}, nil
	}
}

// TestCreateSessionCarriesReasoningEffort: a CreateSession with reasoning_effort set
// (a) crosses the factory seam in the selector, (b) is echoed on
// CreateSessionResponse.resolved_model.reasoning_effort with the resolved/clamped
// value the factory returned, and (c) is mirrored on GetSession's resolved_model
// (ADR 0055, runtime-discoverability). The factory echoes "high" to model the
// openai "max"→"high" clamp.
func TestCreateSessionCarriesReasoningEffort(t *testing.T) {
	var gotSel server.ProviderSelector
	factory := effortEchoFactory("high", &gotSel)
	svc := newMCPService(t, "SHARED-REPLY", factory)
	client, cleanup := dialGRPC(t, svc)
	defer cleanup()

	resp, err := client.CreateSession(context.Background(), &mecatlv1.CreateSessionRequest{
		ProviderId:      "openai",
		ModelId:         "gpt-5.2",
		ReasoningEffort: "max", // requested max; the factory echoes the clamped "high"
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	// (a) the effort crossed the factory seam in the selector.
	if gotSel.ReasoningEffort != "max" {
		t.Errorf("factory selector ReasoningEffort = %q, want the requested \"max\"", gotSel.ReasoningEffort)
	}
	// (b) the response echoes the RESOLVED (clamped) effort.
	if got := resp.GetResolvedModel().GetReasoningEffort(); got != "high" {
		t.Errorf("CreateSessionResponse resolved_model.reasoning_effort = %q, want the clamped \"high\"", got)
	}

	// (c) GetSession mirrors it.
	gs, err := client.GetSession(context.Background(), &mecatlv1.GetSessionRequest{SessionId: resp.GetSessionId()})
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if got := gs.GetSession().GetResolvedModel().GetReasoningEffort(); got != "high" {
		t.Errorf("GetSession resolved_model.reasoning_effort = %q, want \"high\"", got)
	}
}

// TestCreateSessionEffortPersistsOnSession: the per-session reasoning_effort is
// written onto the persisted Session aggregate as an inert creation label (ADR
// 0055), so a restart can re-mint the same-effort engine. The Service writes
// sess.ReasoningEffort via setSessionLabels.
func TestCreateSessionEffortPersistsOnSession(t *testing.T) {
	var gotSel server.ProviderSelector
	factory := effortEchoFactory("high", &gotSel)
	svc := newMCPService(t, "SHARED-REPLY", factory)

	sess, err := svc.CreateSessionWithProvider(context.Background(), session.ModeDefault, session.Limits{},
		server.ProviderSelector{ProviderID: "openai", ModelID: "gpt-5.2", ReasoningEffort: "high"})
	if err != nil {
		t.Fatalf("CreateSessionWithProvider: %v", err)
	}
	if sess.ReasoningEffort != "high" {
		t.Errorf("persisted Session.ReasoningEffort = %q, want \"high\" (the inert creation label)", sess.ReasoningEffort)
	}
}

// TestEffortSessionRehydratesWithPersistedEffort is the restart-rehydration guard
// for the reasoning-effort label (ADR 0055) — the WHOLE reason Session.ReasoningEffort
// is persisted. It mirrors TestSelectorSessionRehydratesWithPersistedSelector
// (rehydrate_selector_test.go) but with ReasoningEffort in wantSel: a persisted
// effort-bound session whose per-session engine died with the process must be
// REHYDRATED by a second Service over the same store, rebuilt through the factory
// with the SAME persisted effort (a) needsRehydration fires on the effort label and
// (b) the rehydration factory receives a ProviderSelector carrying that effort.
func TestEffortSessionRehydratesWithPersistedEffort(t *testing.T) {
	ctx := context.Background()
	store := memstore.New()

	wantSel := server.ProviderSelector{ProviderID: "openai", ModelID: "gpt-5.2", ReasoningEffort: "high"}

	// "Before the restart": create the effort-bound session.
	var (
		gotSel atomic.Value
		calls  atomic.Int32
	)
	svc1 := selectorServiceOverStore(t, store, selectorRecordingFactory("PRE-RESTART", &gotSel, &calls), nil)
	sess, err := svc1.CreateSessionWithProvider(ctx, session.ModeDefault, session.Limits{}, wantSel)
	if err != nil {
		t.Fatalf("CreateSessionWithProvider: %v", err)
	}
	// (a-pre) the effort persisted onto the aggregate as the opaque label.
	if sess.ReasoningEffort != "high" {
		t.Fatalf("persisted Session.ReasoningEffort = %q, want \"high\"", sess.ReasoningEffort)
	}

	// "After the restart": a NEW Service over the SAME store, empty registries.
	var factoryRoots []string
	var (
		gotSel2 atomic.Value
		calls2  atomic.Int32
	)
	svc2 := selectorServiceOverStore(t, store, selectorRecordingFactory("REHYDRATED", &gotSel2, &calls2), &factoryRoots)

	run2, err := svc2.StartRunContent(ctx, sess.ID, "post-restart turn", nil)
	if err != nil {
		t.Fatalf("StartRunContent (post-restart): %v", err)
	}
	if got := drainServerRun(run2); got != "REHYDRATED" {
		t.Fatalf("post-restart reply = %q, want REHYDRATED; "+
			"SHARED-ENGINE-REPLY means needsRehydration did NOT fire on the effort label", got)
	}
	// (a) needsRehydration fired → the factory was consulted exactly once.
	if calls2.Load() != 1 {
		t.Fatalf("factory called %d times after restart, want exactly 1 (rehydration on the effort label)", calls2.Load())
	}
	// (b) THE KEY ASSERTION: the rehydration factory saw the PERSISTED effort.
	if got := gotSel2.Load(); got != wantSel {
		t.Fatalf("rehydration factory saw selector %v, want the persisted %v (the effort must round-trip)", got, wantSel)
	}
}
