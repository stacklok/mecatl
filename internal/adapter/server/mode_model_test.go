package server_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/mcp"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

// modeRecordingFactory returns a SessionEngineFactory that records every mode it was
// called with and resolves a per-mode MODEL (planModel for ModePlan, sessionModel
// otherwise), echoing it back as ModelID + BuiltForMode — the composition factory's
// Phase 3 contract, faked. It builds a fresh engine each call so a rebuild is observable.
func modeRecordingFactory(sessionModel, planModel string, modes *[]session.PermissionMode, calls *atomic.Int32) server.SessionEngineFactory {
	return func(_ context.Context, _ server.ProviderSelector, _ []mcp.ServerConfig, _ server.SessionProfile, _ string, mode session.PermissionMode) (server.SessionEngineResult, error) {
		calls.Add(1)
		model := sessionModel
		if mode == session.ModePlan && planModel != "" {
			model = planModel
		}
		*modes = append(*modes, mode)
		eng := agent.NewEngine(agent.Deps{
			LLM:     mockllm.New(mockllm.TextTurn("reply-" + model)),
			Catalog: tool.NewCatalog(),
			Policy:  permpolicy.NewPolicy(nil, nil),
			Model:   model,
		})
		return server.SessionEngineResult{
			Engine:       eng,
			ModelID:      model,
			ProviderID:   "openai",
			BuiltForMode: mode,
			Close:        func() error { return nil },
		}, nil
	}
}

// modeServiceOverStore builds a Service over store with the mode-aware factory and a
// ModeNeedsEngine predicate (so a DEFAULT-FS session is PROMOTED on a plan switch).
// needsEngine nil ⇒ the byte-identical (no-promotion) deployment.
func modeServiceOverStore(t *testing.T, store *memstore.Store, factory server.SessionEngineFactory, needsEngine func(session.PermissionMode) bool) *server.Service {
	t.Helper()
	svc, err := newPlacementTestService(server.Config{
		Engine: agent.NewEngine(agent.Deps{
			// Script several identical turns so a multi-turn shared-engine session does
			// not exhaust the mock (the byte-identical test runs two turns on it).
			LLM:     mockllm.New(mockllm.TextTurn("SHARED-ENGINE-REPLY"), mockllm.TextTurn("SHARED-ENGINE-REPLY"), mockllm.TextTurn("SHARED-ENGINE-REPLY")),
			Catalog: tool.NewCatalog(),
			Policy:  permpolicy.NewPolicy(nil, nil),
			Model:   "shared-model",
		}),
		Store: store,

		DefaultLimits:   session.Limits{MaxTurns: 5},
		Now:             func() time.Time { return time.Unix(0, 0) },
		SessionEngine:   factory,
		ModeNeedsEngine: needsEngine,
		DefaultResolvedModel: server.ResolvedModel{
			ProviderID: "openai", ModelID: "shared-model",
		},
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return svc
}

// TestModeFlipRebuildsOnPlanSlot is the core Phase 3 guard (ADR 0030 Layer 3): a
// DEFAULT-FS session run in default mode rides the shared engine; after SetMode(plan)
// the next StartRun PROMOTES it to a per-session factory engine resolved on the PLAN
// model. ResolvedModel reflects the plan model, and the factory was invoked with
// mode=plan.
func TestModeFlipRebuildsOnPlanSlot(t *testing.T) {
	ctx := context.Background()
	store := memstore.New()
	const sessionModel, planModel = "gpt-5", "opus-plan"
	var (
		modes []session.PermissionMode
		calls atomic.Int32
	)
	needsEngine := func(m session.PermissionMode) bool { return m == session.ModePlan }
	svc := modeServiceOverStore(t, store, modeRecordingFactory(sessionModel, planModel, &modes, &calls), needsEngine)

	sess, err := svc.CreateSession(ctx, session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	// Turn 1 (default mode): shared engine, factory NOT consulted.
	if got := drainAndFinish(t, svc, sess.ID, mustStart(t, svc, sess.ID, "turn one")); got != "SHARED-ENGINE-REPLY" {
		t.Fatalf("turn-1 reply = %q, want SHARED-ENGINE-REPLY (default mode rides the shared engine)", got)
	}
	if calls.Load() != 0 {
		t.Fatalf("factory called %d times in default mode, want 0 (no promotion)", calls.Load())
	}

	// Switch to plan mode (deferred to next prompt; the run already completed).
	if _, err := svc.SetMode(ctx, sess.ID, session.ModePlan); err != nil {
		t.Fatalf("SetMode(plan): %v", err)
	}

	// Turn 2 (plan mode): the session is PROMOTED to a factory engine on the plan model.
	if got := drainAndFinish(t, svc, sess.ID, mustStart(t, svc, sess.ID, "turn two")); got != "reply-"+planModel {
		t.Fatalf("turn-2 reply = %q, want reply-%s (plan mode runs the plan model)", got, planModel)
	}
	if calls.Load() != 1 {
		t.Fatalf("factory called %d times after the plan switch, want exactly 1 (the promotion)", calls.Load())
	}
	if len(modes) != 1 || modes[0] != session.ModePlan {
		t.Fatalf("factory saw modes %v, want exactly [plan]", modes)
	}
	// ResolvedModel re-emits the plan model after the rebuild (no recompute — it reads
	// the freshly-registered per-session engine's ids).
	if rm := svc.ResolvedModel(sess.ID); rm.ModelID != planModel {
		t.Fatalf("ResolvedModel.ModelID = %q after the plan rebuild, want %q", rm.ModelID, planModel)
	}
}

// TestModeRebuildReEmitsCapabilities pins that SessionCapabilities re-emits the
// rebuilt engine's per-session caps after a mode→model rebuild (ADR 0030 Layer 3) — the
// capability echo reads the freshly-registered se.caps, so a plan model with different
// modalities re-advertises correctly.
func TestModeRebuildReEmitsCapabilities(t *testing.T) {
	ctx := context.Background()
	store := memstore.New()
	const sessionModel, planModel = "gpt-5", "opus-plan"
	// A factory returning DISTINCT caps per mode (the plan model is image-capable here).
	factory := func(_ context.Context, _ server.ProviderSelector, _ []mcp.ServerConfig, _ server.SessionProfile, _ string, mode session.PermissionMode) (server.SessionEngineResult, error) {
		model, caps := sessionModel, port.ProviderCapabilities{}
		if mode == session.ModePlan {
			model, caps = planModel, port.ProviderCapabilities{Image: true}
		}
		eng := agent.NewEngine(agent.Deps{
			LLM: mockllm.New(mockllm.TextTurn("ok")), Catalog: tool.NewCatalog(),
			Policy: permpolicy.NewPolicy(nil, nil), Model: model,
		})
		return server.SessionEngineResult{Engine: eng, ModelID: model, ProviderID: "openai", Capabilities: caps, BuiltForMode: mode, Close: func() error { return nil }}, nil
	}
	svc := modeServiceOverStore(t, store, factory, func(m session.PermissionMode) bool { return m == session.ModePlan })
	sess, err := svc.CreateSessionWithProvider(ctx, session.ModeDefault, session.Limits{},
		server.ProviderSelector{ProviderID: "openai", ModelID: "gpt-5"})
	if err != nil {
		t.Fatalf("CreateSessionWithProvider: %v", err)
	}
	if caps := svc.SessionCapabilities(sess.ID); caps.Image {
		t.Fatalf("default-mode SessionCapabilities.Image = true, want false (the session model is text-only)")
	}
	_ = drainAndFinish(t, svc, sess.ID, mustStart(t, svc, sess.ID, "t1"))
	if _, err := svc.SetMode(ctx, sess.ID, session.ModePlan); err != nil {
		t.Fatalf("SetMode(plan): %v", err)
	}
	_ = drainAndFinish(t, svc, sess.ID, mustStart(t, svc, sess.ID, "t2"))
	if caps := svc.SessionCapabilities(sess.ID); !caps.Image {
		t.Fatalf("plan-mode SessionCapabilities.Image = false, want true (the rebuild re-emits the plan model's caps)")
	}
}

// TestModeFlipRebuildsBackToExecute pins the round trip: plan→default rebuilds back to
// the SESSION model (the engine is stale again once builtForMode != the new mode). Here
// the session is a SELECTOR session so it always has a per-session engine, exercising
// CASE 1 (rebuild a registered engine) rather than CASE 2 (promote a default session).
func TestModeFlipRebuildsBackToExecute(t *testing.T) {
	ctx := context.Background()
	store := memstore.New()
	const sessionModel, planModel = "gpt-5", "opus-plan"
	var (
		modes []session.PermissionMode
		calls atomic.Int32
	)
	svc := modeServiceOverStore(t, store, modeRecordingFactory(sessionModel, planModel, &modes, &calls),
		func(m session.PermissionMode) bool { return m == session.ModePlan })

	// A selector session: always per-session, created in plan mode.
	sess, err := svc.CreateSessionWithProvider(ctx, session.ModePlan, session.Limits{},
		server.ProviderSelector{ProviderID: "openai", ModelID: "gpt-5"})
	if err != nil {
		t.Fatalf("CreateSessionWithProvider: %v", err)
	}
	if rm := svc.ResolvedModel(sess.ID); rm.ModelID != planModel {
		t.Fatalf("created plan-mode selector ResolvedModel = %q, want %q", rm.ModelID, planModel)
	}
	// Create built the engine once (mode=plan).
	if calls.Load() != 1 {
		t.Fatalf("after create, factory calls = %d, want 1", calls.Load())
	}
	// Run once in plan, then switch to default and run: CASE 1 rebuild back to the model.
	if got := drainAndFinish(t, svc, sess.ID, mustStart(t, svc, sess.ID, "plan turn")); got != "reply-"+planModel {
		t.Fatalf("plan turn reply = %q, want reply-%s", got, planModel)
	}
	// MATCHING-MODE NO-OP (the builtForMode == sess.Mode short-circuit): a plan turn on a
	// plan-built engine must NOT rebuild — the factory call count is unchanged.
	if calls.Load() != 1 {
		t.Fatalf("after the matching-mode plan turn, factory calls = %d, want 1 (no rebuild when mode matches)", calls.Load())
	}
	if _, err := svc.SetMode(ctx, sess.ID, session.ModeDefault); err != nil {
		t.Fatalf("SetMode(default): %v", err)
	}
	if got := drainAndFinish(t, svc, sess.ID, mustStart(t, svc, sess.ID, "exec turn")); got != "reply-"+sessionModel {
		t.Fatalf("exec turn reply = %q, want reply-%s (rebuilt back to the session model)", got, sessionModel)
	}
	if rm := svc.ResolvedModel(sess.ID); rm.ModelID != sessionModel {
		t.Fatalf("ResolvedModel after switch-back = %q, want %q", rm.ModelID, sessionModel)
	}
}

// TestModeFlipByteIdenticalWithoutPlanSlot is the regression guard: with ModeNeedsEngine
// NIL (no plan slot configured), a default-FS session NEVER promotes on a mode switch —
// it keeps the shared engine, byte-identical to pre-Phase-3. The factory is never
// consulted and the engine pointer (reply) is unchanged across the flip.
func TestModeFlipByteIdenticalWithoutPlanSlot(t *testing.T) {
	ctx := context.Background()
	store := memstore.New()
	var (
		modes []session.PermissionMode
		calls atomic.Int32
	)
	// needsEngine NIL ⇒ no promotion.
	svc := modeServiceOverStore(t, store, modeRecordingFactory("gpt-5", "opus-plan", &modes, &calls), nil)

	sess, err := svc.CreateSession(ctx, session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if got := drainAndFinish(t, svc, sess.ID, mustStart(t, svc, sess.ID, "t1")); got != "SHARED-ENGINE-REPLY" {
		t.Fatalf("t1 reply = %q, want SHARED-ENGINE-REPLY", got)
	}
	if _, err := svc.SetMode(ctx, sess.ID, session.ModePlan); err != nil {
		t.Fatalf("SetMode(plan): %v", err)
	}
	if got := drainAndFinish(t, svc, sess.ID, mustStart(t, svc, sess.ID, "t2")); got != "SHARED-ENGINE-REPLY" {
		t.Fatalf("t2 reply = %q, want SHARED-ENGINE-REPLY (no plan slot ⇒ no rebuild, shared engine unchanged)", got)
	}
	if calls.Load() != 0 {
		t.Fatalf("factory called %d times with ModeNeedsEngine nil, want 0 (byte-identical default)", calls.Load())
	}
}

// TestSetModeRejectedMidTurn pins that a mode switch is rejected while a run is in
// flight (the aggregate's StateRunning/StateAwaiting guard), so the model stays fixed
// per turn — the mode→model rebuild only ever fires at a turn boundary.
func TestSetModeRejectedMidTurn(t *testing.T) {
	ctx := context.Background()
	store := memstore.New()
	// A factory whose engine blocks INSIDE the provider call until released, so the run
	// is observably mid-turn (StateRunning) when SetMode is attempted. The observer
	// signals `entered` so the test does not race the goroutine into the provider call.
	release := make(chan struct{})
	entered := make(chan struct{})
	blocking := func(_ context.Context, _ server.ProviderSelector, _ []mcp.ServerConfig, _ server.SessionProfile, _ string, mode session.PermissionMode) (server.SessionEngineResult, error) {
		eng := agent.NewEngine(agent.Deps{
			LLM: mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(port.LLMRequest) {
				close(entered)
				<-release
			})}, mockllm.TextTurn("late")),
			Catalog: tool.NewCatalog(),
			Policy:  permpolicy.NewPolicy(nil, nil),
			Model:   "gpt-5",
		})
		return server.SessionEngineResult{Engine: eng, ModelID: "gpt-5", ProviderID: "openai", BuiltForMode: mode, Close: func() error { return nil }}, nil
	}
	svc := modeServiceOverStore(t, store, blocking, func(m session.PermissionMode) bool { return m == session.ModePlan })

	sess, err := svc.CreateSessionWithProvider(ctx, session.ModeDefault, session.Limits{},
		server.ProviderSelector{ProviderID: "openai", ModelID: "gpt-5"})
	if err != nil {
		t.Fatalf("CreateSessionWithProvider: %v", err)
	}
	run, err := svc.StartRun(ctx, sess.ID, "go")
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	<-entered // the run is now blocked inside the provider call: StateRunning.
	// SetMode must be rejected as a mid-turn transition.
	_, modeErr := svc.SetMode(ctx, sess.ID, session.ModePlan)
	close(release)
	_ = drainServerRun(run)
	svc.FinishRun(sess.ID, run)
	if modeErr == nil {
		t.Fatal("SetMode mid-turn must be rejected (the model is fixed per turn), got nil")
	}
}

// drainAndFinish drains a run to its terminal text and removes it from the in-flight
// registry (the wire-adapter defer FinishRun contract), so a follow-up StartRun on the
// same session sees no live run — the precondition the mode→model rebuild asserts.
func drainAndFinish(t *testing.T, svc *server.Service, id session.SessionID, run *agent.Run) string {
	t.Helper()
	got := drainServerRun(run)
	svc.FinishRun(id, run)
	return got
}

// TestPlanModeSessionRehydratesOnPlanModel pins the restart path (ADR 0030 Layer 3 +
// cloud-native Phase 1): a session persisted with Mode=plan, whose per-session engine
// died with the process, is REHYDRATED at the run-entry seam on the PLAN model — the
// factory is invoked with mode=plan read off the persisted aggregate, never the default.
func TestPlanModeSessionRehydratesOnPlanModel(t *testing.T) {
	ctx := context.Background()
	store := memstore.New()
	const sessionModel, planModel = "gpt-5", "opus-plan"

	// "Before the restart": a selector session created in plan mode (so it persists a
	// selector AND Mode=plan).
	var modes1 []session.PermissionMode
	var calls1 atomic.Int32
	svc1 := modeServiceOverStore(t, store, modeRecordingFactory(sessionModel, planModel, &modes1, &calls1),
		func(m session.PermissionMode) bool { return m == session.ModePlan })
	sess, err := svc1.CreateSessionWithProvider(ctx, session.ModePlan, session.Limits{},
		server.ProviderSelector{ProviderID: "openai", ModelID: "gpt-5"})
	if err != nil {
		t.Fatalf("CreateSessionWithProvider: %v", err)
	}
	if sess.Mode != session.ModePlan {
		t.Fatalf("persisted Mode = %q, want plan", sess.Mode)
	}

	// "After the restart": a NEW Service over the SAME store, empty in-memory registries.
	var modes2 []session.PermissionMode
	var calls2 atomic.Int32
	svc2 := modeServiceOverStore(t, store, modeRecordingFactory(sessionModel, planModel, &modes2, &calls2),
		func(m session.PermissionMode) bool { return m == session.ModePlan })

	if got := drainAndFinish(t, svc2, sess.ID, mustStart(t, svc2, sess.ID, "post-restart")); got != "reply-"+planModel {
		t.Fatalf("post-restart reply = %q, want reply-%s (rehydrated on the plan model)", got, planModel)
	}
	if calls2.Load() != 1 {
		t.Fatalf("post-restart factory calls = %d, want 1 (rehydration)", calls2.Load())
	}
	if len(modes2) != 1 || modes2[0] != session.ModePlan {
		t.Fatalf("rehydration factory saw modes %v, want [plan] (the persisted mode, not the default)", modes2)
	}
	if rm := svc2.ResolvedModel(sess.ID); rm.ModelID != planModel {
		t.Fatalf("post-restart ResolvedModel = %q, want the plan model %q", rm.ModelID, planModel)
	}
}

// TestModeFlipEndToEndModelObserved is the authoritative offline end-to-end (ADR 0030
// Layer 3): CreateSession → Run(default) → SetMode(plan) → Run(plan), asserting via the
// mockllm request observer that the LLM saw the SESSION model on turn 1 and the PLAN
// model on turn 2 — the model the provider actually received, not just the echoed id.
func TestModeFlipEndToEndModelObserved(t *testing.T) {
	ctx := context.Background()
	store := memstore.New()
	const sessionModel, planModel = "gpt-5", "opus-plan"

	var observedModels []string
	// A mode-aware factory whose engine records the model the provider received.
	factory := func(_ context.Context, _ server.ProviderSelector, _ []mcp.ServerConfig, _ server.SessionProfile, _ string, mode session.PermissionMode) (server.SessionEngineResult, error) {
		model := sessionModel
		if mode == session.ModePlan {
			model = planModel
		}
		eng := agent.NewEngine(agent.Deps{
			LLM: mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(req port.LLMRequest) {
				observedModels = append(observedModels, req.Model)
			})}, mockllm.TextTurn("ok")),
			Catalog: tool.NewCatalog(),
			Policy:  permpolicy.NewPolicy(nil, nil),
			Model:   model,
		})
		return server.SessionEngineResult{Engine: eng, ModelID: model, ProviderID: "openai", BuiltForMode: mode, Close: func() error { return nil }}, nil
	}
	// Selector session so a per-session engine exists from turn 1 (CASE 1 rebuild on the
	// flip); ModeNeedsEngine active.
	svc := modeServiceOverStore(t, store, factory, func(m session.PermissionMode) bool { return m == session.ModePlan })

	sess, err := svc.CreateSessionWithProvider(ctx, session.ModeDefault, session.Limits{},
		server.ProviderSelector{ProviderID: "openai", ModelID: "gpt-5"})
	if err != nil {
		t.Fatalf("CreateSessionWithProvider: %v", err)
	}
	_ = drainAndFinish(t, svc, sess.ID, mustStart(t, svc, sess.ID, "turn 1"))
	if _, err := svc.SetMode(ctx, sess.ID, session.ModePlan); err != nil {
		t.Fatalf("SetMode(plan): %v", err)
	}
	_ = drainAndFinish(t, svc, sess.ID, mustStart(t, svc, sess.ID, "turn 2"))

	if len(observedModels) != 2 {
		t.Fatalf("provider saw %d requests, want 2 (one per turn): %v", len(observedModels), observedModels)
	}
	if observedModels[0] != sessionModel {
		t.Fatalf("turn-1 provider model = %q, want the session model %q", observedModels[0], sessionModel)
	}
	if observedModels[1] != planModel {
		t.Fatalf("turn-2 provider model = %q, want the plan model %q (re-resolved between turns)", observedModels[1], planModel)
	}
}

func mustStart(t *testing.T, svc *server.Service, id session.SessionID, text string) *agent.Run {
	t.Helper()
	run, err := svc.StartRun(context.Background(), id, text)
	if err != nil {
		t.Fatalf("StartRun(%q): %v", text, err)
	}
	return run
}

// TestPreP3FactoryBuiltForModeEmptyNoRebuild pins the `se.builtForMode != ""` skip
// (MUST-FIX #2): a per-session engine registered by a factory that returns an EMPTY
// BuiltForMode (a pre-Phase-3 factory, or an old in-flight shape) is treated as
// "no mode pin" — a later mode change must NOT trigger a rebuild. Dropping the `!= ""`
// guard would make every pre-Phase-3 selector session rebuild on its first mode touch.
func TestPreP3FactoryBuiltForModeEmptyNoRebuild(t *testing.T) {
	ctx := context.Background()
	store := memstore.New()
	var calls atomic.Int32
	// A pre-Phase-3 factory: builds a per-session engine but leaves BuiltForMode "".
	preP3 := func(_ context.Context, _ server.ProviderSelector, _ []mcp.ServerConfig, _ server.SessionProfile, _ string, _ session.PermissionMode) (server.SessionEngineResult, error) {
		calls.Add(1)
		eng := agent.NewEngine(agent.Deps{
			LLM:     mockllm.New(mockllm.TextTurn("pre-p3"), mockllm.TextTurn("pre-p3")),
			Catalog: tool.NewCatalog(), Policy: permpolicy.NewPolicy(nil, nil), Model: "gpt-5",
		})
		// BuiltForMode deliberately left "" (the pre-Phase-3 shape).
		return server.SessionEngineResult{Engine: eng, ModelID: "gpt-5", ProviderID: "openai", Close: func() error { return nil }}, nil
	}
	// ModeNeedsEngine active (a plan slot exists), so only the `!= ""` skip prevents a rebuild.
	svc := modeServiceOverStore(t, store, preP3, func(m session.PermissionMode) bool { return m == session.ModePlan })

	sess, err := svc.CreateSessionWithProvider(ctx, session.ModeDefault, session.Limits{},
		server.ProviderSelector{ProviderID: "openai", ModelID: "gpt-5"})
	if err != nil {
		t.Fatalf("CreateSessionWithProvider: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("after create, factory calls = %d, want 1", calls.Load())
	}
	_ = drainAndFinish(t, svc, sess.ID, mustStart(t, svc, sess.ID, "t1"))
	if _, err := svc.SetMode(ctx, sess.ID, session.ModePlan); err != nil {
		t.Fatalf("SetMode(plan): %v", err)
	}
	_ = drainAndFinish(t, svc, sess.ID, mustStart(t, svc, sess.ID, "t2"))
	// The empty builtForMode is treated as "no pin": no rebuild despite the mode change.
	if calls.Load() != 1 {
		t.Fatalf("factory calls = %d after a mode change on a builtForMode=\"\" engine, want 1 "+
			"(the != \"\" skip must treat an empty mode as no-pin — dropping it would rebuild)", calls.Load())
	}
}

// TestModePromotionUnderFullCap pins the at-cap failure mode for CASE-2 promotion
// (SHOULD-FIX #3): when MaxSessionEngines is saturated, a default-FS session that would
// be PROMOTED on a plan switch fails gracefully with ErrTooManySessionEngines — never a
// panic, never a silent shared-engine fallback (which would run plan mode on the wrong
// model).
func TestModePromotionUnderFullCap(t *testing.T) {
	ctx := context.Background()
	store := memstore.New()
	const sessionModel, planModel = "gpt-5", "opus-plan"
	var modes []session.PermissionMode
	var calls atomic.Int32
	// Build a Service with MaxSessionEngines = 1 (so one selector session saturates it).
	svc, err := newPlacementTestService(server.Config{
		Engine: agent.NewEngine(agent.Deps{
			LLM:     mockllm.New(mockllm.TextTurn("SHARED"), mockllm.TextTurn("SHARED")),
			Catalog: tool.NewCatalog(), Policy: permpolicy.NewPolicy(nil, nil), Model: "shared-model",
		}),
		Store: store,

		DefaultLimits:     session.Limits{MaxTurns: 5},
		Now:               func() time.Time { return time.Unix(0, 0) },
		SessionEngine:     modeRecordingFactory(sessionModel, planModel, &modes, &calls),
		ModeNeedsEngine:   func(m session.PermissionMode) bool { return m == session.ModePlan },
		MaxSessionEngines: 1,
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	// Saturate the cap with a selector session (one per-session engine registered).
	if _, err := svc.CreateSessionWithProvider(ctx, session.ModeDefault, session.Limits{},
		server.ProviderSelector{ProviderID: "openai", ModelID: "gpt-5"}); err != nil {
		t.Fatalf("CreateSessionWithProvider (cap hog): %v", err)
	}

	// A default-FS session (shared engine, no per-session slot used at create).
	planSess, err := svc.CreateSession(ctx, session.ModePlan, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession (default-FS plan): %v", err)
	}
	// Its first StartRun would PROMOTE it — but the cap is full, so it must fail with
	// ErrTooManySessionEngines (graceful), not panic and not fall back to the shared engine.
	_, runErr := svc.StartRun(ctx, planSess.ID, "promote me")
	if !errors.Is(runErr, server.ErrTooManySessionEngines) {
		t.Fatalf("StartRun on an at-cap promotion = %v, want ErrTooManySessionEngines (graceful, no silent shared-engine fallback)", runErr)
	}
}

// TestNoFSModeRebuildKeepsProfile pins that a CASE-1 rebuild of a NO-FS session keeps
// ProfileNoFS (via profileForSession) and does not escalate onto the FS catalog
// (SHOULD-FIX #5, security-adjacent). The factory records the profile it was rebuilt
// with; a rebuild that passed ProfileDefault would be a real no-fs escalation.
func TestNoFSModeRebuildKeepsProfile(t *testing.T) {
	ctx := context.Background()
	store := memstore.New()
	const sessionModel, planModel = "gpt-5", "opus-plan"
	var (
		mu             sync.Mutex
		profilesSeen   []server.SessionProfile
		modesSeen      []session.PermissionMode
		factoryCallCnt atomic.Int32
	)
	factory := func(_ context.Context, _ server.ProviderSelector, _ []mcp.ServerConfig, profile server.SessionProfile, _ string, mode session.PermissionMode) (server.SessionEngineResult, error) {
		factoryCallCnt.Add(1)
		mu.Lock()
		profilesSeen = append(profilesSeen, profile)
		modesSeen = append(modesSeen, mode)
		mu.Unlock()
		model := sessionModel
		if mode == session.ModePlan {
			model = planModel
		}
		eng := agent.NewEngine(agent.Deps{
			LLM: mockllm.New(mockllm.TextTurn("ok")), Catalog: tool.NewCatalog(),
			Policy: permpolicy.NewPolicy(nil, nil), Model: model,
		})
		return server.SessionEngineResult{Engine: eng, ModelID: model, ProviderID: "openai", BuiltForMode: mode, Close: func() error { return nil }}, nil
	}
	svc := modeServiceOverStore(t, store, factory, func(m session.PermissionMode) bool { return m == session.ModePlan })

	// A no-fs session is created with an EMPTY workspace + the no-fs profile.
	sess, err := svc.CreateSessionWithProfile(ctx, session.ModeDefault, session.Limits{},
		server.ProviderSelector{}, server.ProfileNoFS)
	if err != nil {
		t.Fatalf("CreateSessionWithProfile(no-fs): %v", err)
	}
	_ = drainAndFinish(t, svc, sess.ID, mustStart(t, svc, sess.ID, "t1"))
	if _, err := svc.SetMode(ctx, sess.ID, session.ModePlan); err != nil {
		t.Fatalf("SetMode(plan): %v", err)
	}
	_ = drainAndFinish(t, svc, sess.ID, mustStart(t, svc, sess.ID, "t2"))

	mu.Lock()
	defer mu.Unlock()
	if len(profilesSeen) == 0 {
		t.Fatal("factory was never called for the no-fs session")
	}
	if factoryCallCnt.Load() < 2 {
		t.Fatalf("factory called %d times, want >= 2 (create + the mode rebuild)", factoryCallCnt.Load())
	}
	for i, p := range profilesSeen {
		if p != server.ProfileNoFS {
			t.Fatalf("factory call %d (mode=%q) saw profile %q, want ProfileNoFS — a mode rebuild must NOT escalate a no-fs session onto the FS catalog", i, modesSeen[i], p)
		}
	}
}

// TestModeRebuildSerializedByRunEntryMu is the use-after-close guard (MUST-FIX #1),
// deterministic under -race: after a SetMode (while idle) makes the engine stale, TWO
// StartRuns race for the SAME id. runEntryMu serializes the run-entry critical section
// (engine-resolve → launch → register), so the FIRST rebuilds the engine and registers
// its run BEFORE the second resolves — the second therefore never reads the stale prior
// engine concurrently with the rebuild's Close, and the prior engine is closed exactly
// once and only after no run can still hold it. Both runs complete cleanly (each fully
// drained before reuse, so the aggregate is not driven concurrently); the assertion is:
// no panic, no spurious transport error, and the prior engine closed exactly once.
func TestModeRebuildSerializedByRunEntryMu(t *testing.T) {
	ctx := context.Background()
	store := memstore.New()
	const sessionModel, planModel = "gpt-5", "opus-plan"

	var closes atomic.Int32
	turns := make([]mockllm.Turn, 8)
	for i := range turns {
		turns[i] = mockllm.TextTurn("ok")
	}
	factory := func(_ context.Context, _ server.ProviderSelector, _ []mcp.ServerConfig, _ server.SessionProfile, _ string, mode session.PermissionMode) (server.SessionEngineResult, error) {
		model := sessionModel
		if mode == session.ModePlan {
			model = planModel
		}
		eng := agent.NewEngine(agent.Deps{
			LLM: mockllm.New(turns...), Catalog: tool.NewCatalog(),
			Policy: permpolicy.NewPolicy(nil, nil), Model: model,
		})
		return server.SessionEngineResult{
			Engine: eng, ModelID: model, ProviderID: "openai", BuiltForMode: mode,
			Close: func() error { closes.Add(1); return nil },
		}, nil
	}
	svc := modeServiceOverStore(t, store, factory, func(m session.PermissionMode) bool { return m == session.ModePlan })

	sess, err := svc.CreateSessionWithProvider(ctx, session.ModeDefault, session.Limits{},
		server.ProviderSelector{ProviderID: "openai", ModelID: "gpt-5"})
	if err != nil {
		t.Fatalf("CreateSessionWithProvider: %v", err)
	}
	// Make the engine stale while IDLE (no run): the next run-entry will rebuild.
	if _, err := svc.SetMode(ctx, sess.ID, session.ModePlan); err != nil {
		t.Fatalf("SetMode(plan): %v", err)
	}
	closesBefore := closes.Load()

	// Two run-entries race; runEntryMu serializes them. The first rebuilds (closing the
	// prior engine), the second runs on the registered (rebuilt) engine. Each is fully
	// drained before FinishRun so the second never overlaps the first on the aggregate;
	// the point is that the rebuild's Close never races a concurrent stale-engine read.
	var wg sync.WaitGroup
	wg.Add(2)
	runOnce := func(label string) {
		defer wg.Done()
		run, rerr := svc.StartRun(ctx, sess.ID, label)
		if rerr != nil {
			// A clean rejection (the other entry holds the run) is acceptable; a transport
			// error would point at a close-during-use.
			if !errors.Is(rerr, server.ErrFailedPrecondition) {
				t.Errorf("%s: unexpected StartRun error %v", label, rerr)
			}
			return
		}
		drainServerRun(run)
		svc.FinishRun(sess.ID, run)
	}
	go runOnce("entry-A")
	go runOnce("entry-B")
	wg.Wait()

	// The stale prior engine was displaced and closed (exactly once — the rebuild path,
	// never double-closed, never closed while a run held it).
	if got := closes.Load() - closesBefore; got != 1 {
		t.Fatalf("prior engine Close count = %d across the rebuild, want exactly 1 (closed once on the clean swap, never while in use)", got)
	}
	if rm := svc.ResolvedModel(sess.ID); rm.ModelID != planModel {
		t.Fatalf("ResolvedModel after the rebuild = %q, want the plan model %q", rm.ModelID, planModel)
	}
}

// TestModeRebuildCrossSessionConcurrentNoRace is the -race guard over the rebuild path's
// shared state (MUST-FIX #1): many sessions concurrently drive full SetMode→StartRun→
// drain→FinishRun rebuild cycles over the SAME Service, stressing s.mu / sessionEngines /
// buildAndRegisterSessionEngine under the detector. Each goroutine owns its OWN session
// (so no two goroutines drive the same *session.Session aggregate — that orthogonal race
// is not what this phase touched), and serializes its own cycle (drain+FinishRun before
// the next SetMode), so every rebuild runs only when its session has no live run.
func TestModeRebuildCrossSessionConcurrentNoRace(t *testing.T) {
	ctx := context.Background()
	store := memstore.New()
	const sessionModel, planModel = "gpt-5", "opus-plan"
	turns := make([]mockllm.Turn, 8)
	for i := range turns {
		turns[i] = mockllm.TextTurn("ok")
	}
	factory := func(_ context.Context, _ server.ProviderSelector, _ []mcp.ServerConfig, _ server.SessionProfile, _ string, mode session.PermissionMode) (server.SessionEngineResult, error) {
		model := sessionModel
		if mode == session.ModePlan {
			model = planModel
		}
		eng := agent.NewEngine(agent.Deps{
			LLM: mockllm.New(turns...), Catalog: tool.NewCatalog(),
			Policy: permpolicy.NewPolicy(nil, nil), Model: model,
		})
		return server.SessionEngineResult{Engine: eng, ModelID: model, ProviderID: "openai", BuiltForMode: mode, Close: func() error { return nil }}, nil
	}
	svc := modeServiceOverStore(t, store, factory, func(m session.PermissionMode) bool { return m == session.ModePlan })

	var wg sync.WaitGroup
	const workers, cycles = 6, 6
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sess, err := svc.CreateSessionWithProvider(ctx, session.ModeDefault, session.Limits{},
				server.ProviderSelector{ProviderID: "openai", ModelID: "gpt-5"})
			if err != nil {
				t.Errorf("CreateSessionWithProvider: %v", err)
				return
			}
			for i := 0; i < cycles; i++ {
				mode := session.ModeDefault
				if i%2 == 0 {
					mode = session.ModePlan
				}
				if _, err := svc.SetMode(ctx, sess.ID, mode); err != nil {
					t.Errorf("SetMode: %v", err)
					return
				}
				run, rerr := svc.StartRun(ctx, sess.ID, "turn")
				if rerr != nil {
					t.Errorf("StartRun: %v", rerr)
					return
				}
				drainServerRun(run)
				svc.FinishRun(sess.ID, run)
			}
		}()
	}
	wg.Wait()
}

// TestEngineAndWorkspaceForResolutionMatrix is the explicit decision-table test for
// Service.engineAndEnvironmentFor (internal/adapter/server/service.go). It names and
// pins every resolution branch so a regression changes an assertion rather than
// silently disappearing into incidental coverage.
//
// Actual resolution outcomes (5) + error exits (2) — note: the issue asked for a
// "four-way" matrix, but the current code has 5 named outcomes plus 2 error paths:
//
//	REUSE         — hasEngine && (builtForMode=="" || ==sess.Mode): returns the
//	               existing per-session engine unchanged; factory call count does NOT
//	               increase.
//	REBUILD       — hasEngine && builtForMode != "" && != sess.Mode (CASE 1): the
//	               per-session engine is stale; rebuilt through the factory (calls++).
//	PROMOTE       — !hasEngine && !needsRehydration && ModeNeedsEngine(mode) (CASE 2):
//	               a default-FS session promoted to a per-session engine (calls==1).
//	REHYDRATE     — !hasEngine && needsRehydration: rebuilt via rehydrateSession after
//	               a process restart (factory called once on the new service).
//	SHARED-DEFAULT — none of the above: the session rides the shared engine (calls==0).
//	ERROR mid-run  — CASE 1 with a live run → ErrInvalidArgument.
//	ERROR at-cap   — CASE 2/REHYDRATE when MaxSessionEngines is full →
//	               ErrTooManySessionEngines.
//
// The defensive ws=nofs.New() arm inside the DEFAULT block (~service.go:1438-1446) is
// normally unreachable (create and rehydration both register the workspace override
// first); it is left to implicit coverage rather than forced here.
func TestEngineAndWorkspaceForResolutionMatrix(t *testing.T) {
	// matrixRow describes one row of the decision table.
	type matrixRow struct {
		name string
		// run executes the row; it returns (assertFn, wantErr).
		// wantErr is the expected error (nil = success expected). For error rows the
		// returned error IS the assertion (it carries the errors.Is mismatch description).
		// assertFn (called only when wantErr==nil) reads each row's own captured closures
		// — it takes no params; the per-row factory counters/replies live in the closure.
		run func(t *testing.T) (assertFn func(), wantErr error)
	}

	rows := []matrixRow{
		// ── REUSE (matching builtForMode) ─────────────────────────────────────────
		{
			name: "reuse/matching-mode-per-session-engine",
			// A selector session created in plan mode has a per-session engine with
			// builtForMode=plan. Running again in plan mode must REUSE the engine —
			// the factory is NOT called a second time (calls stays at 1, set at create).
			run: func(t *testing.T) (func(), error) {
				t.Helper()
				ctx := context.Background()
				store := memstore.New()
				var modes []session.PermissionMode
				var calls atomic.Int32
				svc := modeServiceOverStore(t, store,
					modeRecordingFactory("gpt-5", "opus-plan", &modes, &calls),
					func(m session.PermissionMode) bool { return m == session.ModePlan })
				sess, err := svc.CreateSessionWithProvider(ctx, session.ModePlan, session.Limits{},
					server.ProviderSelector{ProviderID: "openai", ModelID: "gpt-5"})
				if err != nil {
					t.Fatalf("CreateSession: %v", err)
				}
				callsAtCreate := calls.Load() // should be 1 (the create-time factory call)
				reply := drainAndFinish(t, svc, sess.ID, mustStart(t, svc, sess.ID, "plan turn"))
				return func() {
					// REUSE: no additional factory call since create.
					if got := calls.Load() - callsAtCreate; got != 0 {
						t.Errorf("reuse/matching-mode: factory called %d extra times (want 0 — REUSE must not rebuild when mode matches)", got)
					}
					// REUSE also routes to the PER-SESSION engine (the plan-model reply), not
					// the shared engine: a regression that left the per-session engine
					// registered (calls delta still 0) but silently ran the shared engine would
					// flip this reply to "SHARED-ENGINE-REPLY".
					if reply != "reply-opus-plan" {
						t.Errorf("reuse/matching-mode: reply = %q, want reply-opus-plan (REUSE must route to the per-session engine, not the shared one)", reply)
					}
				}, nil
			},
		},
		// ── REUSE (empty builtForMode — pre-Phase-3 factory compat) ──────────────
		{
			name: "reuse/empty-builtForMode-no-rebuild",
			// A pre-Phase-3 factory returns BuiltForMode="" for a selector session.
			// Even after a mode change the engine must NOT rebuild (the != "" guard).
			run: func(t *testing.T) (func(), error) {
				t.Helper()
				ctx := context.Background()
				store := memstore.New()
				var calls atomic.Int32
				preP3 := func(_ context.Context, _ server.ProviderSelector, _ []mcp.ServerConfig, _ server.SessionProfile, _ string, _ session.PermissionMode) (server.SessionEngineResult, error) {
					calls.Add(1)
					eng := agent.NewEngine(agent.Deps{
						LLM:     mockllm.New(mockllm.TextTurn("pre-p3"), mockllm.TextTurn("pre-p3")),
						Catalog: tool.NewCatalog(), Policy: permpolicy.NewPolicy(nil, nil), Model: "gpt-5",
					})
					// BuiltForMode deliberately left "" (pre-Phase-3 shape).
					return server.SessionEngineResult{Engine: eng, ModelID: "gpt-5", ProviderID: "openai", Close: func() error { return nil }}, nil
				}
				svc := modeServiceOverStore(t, store, preP3,
					func(m session.PermissionMode) bool { return m == session.ModePlan })
				sess, err := svc.CreateSessionWithProvider(ctx, session.ModeDefault, session.Limits{},
					server.ProviderSelector{ProviderID: "openai", ModelID: "gpt-5"})
				if err != nil {
					t.Fatalf("CreateSession: %v", err)
				}
				_ = drainAndFinish(t, svc, sess.ID, mustStart(t, svc, sess.ID, "t1"))
				if _, err := svc.SetMode(ctx, sess.ID, session.ModePlan); err != nil {
					t.Fatalf("SetMode: %v", err)
				}
				_ = drainAndFinish(t, svc, sess.ID, mustStart(t, svc, sess.ID, "t2"))
				return func() {
					// REUSE via empty-builtForMode: create + no rebuilds = 1 call total.
					if calls.Load() != 1 {
						t.Errorf("reuse/empty-builtForMode: factory calls = %d, want 1 (the != \"\" guard must prevent rebuild)", calls.Load())
					}
				}, nil
			},
		},
		// ── REBUILD (CASE 1 — stale per-session engine) ───────────────────────────
		{
			name: "rebuild/stale-mode-case1",
			// A selector session built for ModeDefault; switch to plan → next StartRun
			// REBUILDS the engine (calls goes from 1 to 2; last recorded mode is plan).
			run: func(t *testing.T) (func(), error) {
				t.Helper()
				ctx := context.Background()
				store := memstore.New()
				var modes []session.PermissionMode
				var calls atomic.Int32
				svc := modeServiceOverStore(t, store,
					modeRecordingFactory("gpt-5", "opus-plan", &modes, &calls),
					func(m session.PermissionMode) bool { return m == session.ModePlan })
				sess, err := svc.CreateSessionWithProvider(ctx, session.ModeDefault, session.Limits{},
					server.ProviderSelector{ProviderID: "openai", ModelID: "gpt-5"})
				if err != nil {
					t.Fatalf("CreateSession: %v", err)
				}
				_ = drainAndFinish(t, svc, sess.ID, mustStart(t, svc, sess.ID, "exec turn"))
				if _, err := svc.SetMode(ctx, sess.ID, session.ModePlan); err != nil {
					t.Fatalf("SetMode: %v", err)
				}
				reply := drainAndFinish(t, svc, sess.ID, mustStart(t, svc, sess.ID, "plan turn"))
				return func() {
					// REBUILD: factory called twice (create + rebuild) and last mode is plan.
					if calls.Load() != 2 {
						t.Errorf("rebuild/stale-mode: factory calls = %d, want 2 (create + CASE-1 rebuild)", calls.Load())
					}
					if len(modes) < 2 || modes[len(modes)-1] != session.ModePlan {
						t.Errorf("rebuild/stale-mode: last recorded factory mode = %v, want plan; all: %v", modes, modes)
					}
					// The plan-turn reply comes from the rebuilt plan-model engine.
					if reply != "reply-opus-plan" {
						t.Errorf("rebuild/stale-mode: plan-turn reply = %q, want reply-opus-plan", reply)
					}
				}, nil
			},
		},
		// ── REBUILD (CASE 1 — terminal registered run handoff) ───────────────
		// A run remains registered until its relay calls FinishRun. Once its
		// authoritative snapshot is terminal, a follow-up prompt may replace that
		// finished registration under runEntryMu. Pointer-checked deregistration keeps
		// the old relay's later FinishRun from removing the continuation.
		{
			name: "rebuild/terminal-registered-handoff",
			run: func(t *testing.T) (func(), error) {
				t.Helper()
				ctx := context.Background()
				store := memstore.New()
				var modes []session.PermissionMode
				var calls atomic.Int32
				svc := modeServiceOverStore(t, store,
					modeRecordingFactory("gpt-5", "opus-plan", &modes, &calls),
					func(m session.PermissionMode) bool { return m == session.ModePlan })

				// Selector session: has a per-session engine with builtForMode=ModeDefault.
				sess, err := svc.CreateSessionWithProvider(ctx, session.ModeDefault, session.Limits{},
					server.ProviderSelector{ProviderID: "openai", ModelID: "gpt-5"})
				if err != nil {
					t.Fatalf("CreateSessionWithProvider: %v", err)
				}

				// Step 2: run1 completes cleanly. Do NOT call FinishRun — the run stays in
				// s.runs[id] (simulating a slow wire-drain that has not yet deregistered).
				run1, err := svc.StartRun(ctx, sess.ID, "run1")
				if err != nil {
					t.Fatalf("StartRun run1: %v", err)
				}
				drainServerRun(run1) // drain events but skip FinishRun

				// Step 3: the aggregate is now StateCompleted; SetMode is legal.
				if _, err := svc.SetMode(ctx, sess.ID, session.ModePlan); err != nil {
					t.Fatalf("SetMode(plan) after run1 completed: %v", err)
				}
				// builtForMode=ModeDefault, sess.Mode=plan → CASE 1 will fire on next StartRun.

				// The terminal aggregate authorizes a handoff even though run1 is still
				// registered while its old relay finishes cleanup.
				run2, err := svc.StartRun(ctx, sess.ID, "run2")
				if err != nil {
					return nil, fmt.Errorf("terminal registered handoff: %w", err)
				}
				if got, ok := svc.LookupRun(sess.ID); !ok || got != run2 {
					return nil, fmt.Errorf("registered run after handoff = %p, %v; want run2 %p", got, ok, run2)
				}

				// The old relay's deferred cleanup must not remove its replacement.
				svc.FinishRun(sess.ID, run1)
				if got, ok := svc.LookupRun(sess.ID); !ok || got != run2 {
					return nil, fmt.Errorf("old FinishRun removed replacement: got %p, %v; want run2 %p", got, ok, run2)
				}
				reply := drainServerRun(run2)
				svc.FinishRun(sess.ID, run2)

				return func() {
					if calls.Load() != 2 {
						t.Errorf("terminal handoff: factory calls = %d, want 2 (create + mode rebuild)", calls.Load())
					}
					if reply != "reply-opus-plan" {
						t.Errorf("terminal handoff reply = %q, want reply-opus-plan", reply)
					}
				}, nil
			},
		},
		// ── PROMOTE (CASE 2 — default-FS session gets a plan slot) ───────────────
		{
			name: "promote/default-fs-plan-slot-case2",
			// A default-FS session created in plan mode with ModeNeedsEngine active:
			// the first StartRun promotes it to a per-session engine. calls==1, mode==plan.
			run: func(t *testing.T) (func(), error) {
				t.Helper()
				ctx := context.Background()
				store := memstore.New()
				var modes []session.PermissionMode
				var calls atomic.Int32
				svc := modeServiceOverStore(t, store,
					modeRecordingFactory("gpt-5", "opus-plan", &modes, &calls),
					func(m session.PermissionMode) bool { return m == session.ModePlan })
				// Default-FS (no selector) but plan mode.
				sess, err := svc.CreateSession(ctx, session.ModePlan, session.Limits{})
				if err != nil {
					t.Fatalf("CreateSession: %v", err)
				}
				if calls.Load() != 0 {
					t.Fatalf("factory should not be called at create for default-FS; got %d calls", calls.Load())
				}
				reply := drainAndFinish(t, svc, sess.ID, mustStart(t, svc, sess.ID, "plan prompt"))
				return func() {
					// PROMOTE: factory called exactly once (on the first StartRun).
					if calls.Load() != 1 {
						t.Errorf("promote/case2: factory calls = %d, want 1 (the CASE-2 promotion)", calls.Load())
					}
					if len(modes) != 1 || modes[0] != session.ModePlan {
						t.Errorf("promote/case2: factory modes = %v, want [plan]", modes)
					}
					if reply != "reply-opus-plan" {
						t.Errorf("promote/case2: reply = %q, want reply-opus-plan", reply)
					}
				}, nil
			},
		},
		// ── PROMOTE (CASE 2 — at-cap → ErrTooManySessionEngines) ─────────────────
		// The at-cap rejection is also a BACKSTOP PAIR: a cheap pre-check
		// (~service.go:1578) and an authoritative re-check under the lock (~1604) both
		// return ErrTooManySessionEngines. Either alone backstops the cap invariant, so
		// this row pins the OUTCOME (graceful ErrTooManySessionEngines, never a panic or a
		// silent shared-engine fallback that would run plan mode on the wrong model)
		// rather than trying to distinguish which member fired.
		{
			name: "promote/at-cap-error",
			run: func(t *testing.T) (func(), error) {
				t.Helper()
				ctx := context.Background()
				store := memstore.New()
				var modes []session.PermissionMode
				var calls atomic.Int32
				// MaxSessionEngines = 1: one selector session saturates the cap.
				svc, err := newPlacementTestService(server.Config{
					Engine: agent.NewEngine(agent.Deps{
						LLM:     mockllm.New(mockllm.TextTurn("SHARED"), mockllm.TextTurn("SHARED")),
						Catalog: tool.NewCatalog(), Policy: permpolicy.NewPolicy(nil, nil), Model: "shared-model",
					}),
					Store: store,

					DefaultLimits:     session.Limits{MaxTurns: 5},
					Now:               func() time.Time { return time.Unix(0, 0) },
					SessionEngine:     modeRecordingFactory("gpt-5", "opus-plan", &modes, &calls),
					ModeNeedsEngine:   func(m session.PermissionMode) bool { return m == session.ModePlan },
					MaxSessionEngines: 1,
					DefaultResolvedModel: server.ResolvedModel{
						ProviderID: "openai", ModelID: "shared-model",
					},
				})
				if err != nil {
					t.Fatalf("NewService: %v", err)
				}
				// Saturate the cap.
				if _, err := svc.CreateSessionWithProvider(ctx, session.ModeDefault, session.Limits{},
					server.ProviderSelector{ProviderID: "openai", ModelID: "gpt-5"}); err != nil {
					t.Fatalf("CreateSessionWithProvider (cap hog): %v", err)
				}
				// Default-FS plan session would PROMOTE but cap is full.
				planSess, err := svc.CreateSession(ctx, session.ModePlan, session.Limits{})
				if err != nil {
					t.Fatalf("CreateSession (default-FS plan): %v", err)
				}
				_, runErr := svc.StartRun(ctx, planSess.ID, "promote me")
				if !errors.Is(runErr, server.ErrTooManySessionEngines) {
					return nil, fmt.Errorf("at-cap promote: want ErrTooManySessionEngines, got %v", runErr)
				}
				return func() {}, nil
			},
		},
		// ── REHYDRATE (selector session after restart) ────────────────────────────
		{
			name: "rehydrate/selector-after-restart",
			// A selector session created on svc1; svc2 simulates a restart (same
			// store, empty in-memory registries). The first StartRun on svc2 must
			// call the factory once (rehydration).
			run: func(t *testing.T) (func(), error) {
				t.Helper()
				ctx := context.Background()
				store := memstore.New()
				const sessionModel, planModel = "gpt-5", "opus-plan"

				var modes1 []session.PermissionMode
				var calls1 atomic.Int32
				svc1 := modeServiceOverStore(t, store,
					modeRecordingFactory(sessionModel, planModel, &modes1, &calls1),
					func(m session.PermissionMode) bool { return m == session.ModePlan })
				sess, err := svc1.CreateSessionWithProvider(ctx, session.ModeDefault, session.Limits{},
					server.ProviderSelector{ProviderID: "openai", ModelID: sessionModel})
				if err != nil {
					t.Fatalf("CreateSessionWithProvider: %v", err)
				}
				_ = drainAndFinish(t, svc1, sess.ID, mustStart(t, svc1, sess.ID, "pre-restart"))

				// Simulate restart: new service over same store.
				var modes2 []session.PermissionMode
				var calls2 atomic.Int32
				svc2 := modeServiceOverStore(t, store,
					modeRecordingFactory(sessionModel, planModel, &modes2, &calls2),
					func(m session.PermissionMode) bool { return m == session.ModePlan })
				reply := drainAndFinish(t, svc2, sess.ID, mustStart(t, svc2, sess.ID, "post-restart"))
				return func() {
					// REHYDRATE: factory called once on svc2 (rehydration), reply from session model.
					if calls2.Load() != 1 {
						t.Errorf("rehydrate/selector: svc2 factory calls = %d, want 1", calls2.Load())
					}
					if reply != "reply-"+sessionModel {
						t.Errorf("rehydrate/selector: reply = %q, want reply-%s", reply, sessionModel)
					}
					if !svc2.HasSessionEngineForTest(sess.ID) {
						t.Errorf("rehydrate/selector: svc2 HasSessionEngine = false, want true (rehydrated)")
					}
				}, nil
			},
		},
		// ── REHYDRATE (no-fs session after restart → nofs workspace preserved) ────
		{
			name: "rehydrate/no-fs-restart-sets-nofs-workspace",
			// A no-fs session created on svc1; svc2 starts fresh. After the first
			// post-restart StartRun the session must ride a per-session engine (the
			// factory was invoked and the profile is preserved as ProfileNoFS).
			run: func(t *testing.T) (func(), error) {
				t.Helper()
				ctx := context.Background()
				store := memstore.New()

				var calls1 atomic.Int32
				// Profile-recording factory for svc1.
				var gotProfile1 atomic.Value
				pfactory1 := func(_ context.Context, _ server.ProviderSelector, _ []mcp.ServerConfig, profile server.SessionProfile, _ string, mode session.PermissionMode) (server.SessionEngineResult, error) {
					calls1.Add(1)
					gotProfile1.Store(profile)
					eng := agent.NewEngine(agent.Deps{
						LLM:     mockllm.New(mockllm.TextTurn("nofs-pre")),
						Catalog: tool.NewCatalog(), Policy: permpolicy.NewPolicy(nil, nil), Model: "nofs-model",
					})
					return server.SessionEngineResult{Engine: eng, ModelID: "nofs-model", ProviderID: "openai", BuiltForMode: mode, Close: func() error { return nil }}, nil
				}
				svc1 := modeServiceOverStore(t, store, pfactory1, nil /* no plan slot */)
				sess, err := svc1.CreateSessionWithProfile(ctx, session.ModeDefault, session.Limits{},
					server.ProviderSelector{}, server.ProfileNoFS)
				if err != nil {
					t.Fatalf("CreateSessionWithProfile: %v", err)
				}
				_ = drainAndFinish(t, svc1, sess.ID, mustStart(t, svc1, sess.ID, "pre-restart"))

				// Simulate restart: new service over same store.
				var calls2 atomic.Int32
				var gotProfile2 atomic.Value
				pfactory2 := func(_ context.Context, _ server.ProviderSelector, _ []mcp.ServerConfig, profile server.SessionProfile, _ string, mode session.PermissionMode) (server.SessionEngineResult, error) {
					calls2.Add(1)
					gotProfile2.Store(profile)
					eng := agent.NewEngine(agent.Deps{
						LLM:     mockllm.New(mockllm.TextTurn("nofs-post")),
						Catalog: tool.NewCatalog(), Policy: permpolicy.NewPolicy(nil, nil), Model: "nofs-model",
					})
					return server.SessionEngineResult{Engine: eng, ModelID: "nofs-model", ProviderID: "openai", BuiltForMode: mode, Close: func() error { return nil }}, nil
				}
				svc2 := modeServiceOverStore(t, store, pfactory2, nil)
				reply := drainAndFinish(t, svc2, sess.ID, mustStart(t, svc2, sess.ID, "post-restart"))
				return func() {
					// REHYDRATE: factory called once on svc2 with ProfileNoFS.
					if calls2.Load() != 1 {
						t.Errorf("rehydrate/no-fs: svc2 factory calls = %d, want 1", calls2.Load())
					}
					if p, ok := gotProfile2.Load().(server.SessionProfile); !ok || p != server.ProfileNoFS {
						t.Errorf("rehydrate/no-fs: factory profile = %v, want ProfileNoFS", gotProfile2.Load())
					}
					// NeedsRehydrationForTest still returns true for a no-fs session (the
					// profile label is permanent on the persisted aggregate); HasSessionEngine
					// is the meaningful post-rehydration check.
					if !svc2.HasSessionEngineForTest(sess.ID) {
						t.Errorf("rehydrate/no-fs: svc2 HasSessionEngine = false, want true (rehydrated)")
					}
					if reply != "nofs-post" {
						t.Errorf("rehydrate/no-fs: reply = %q, want nofs-post", reply)
					}
				}, nil
			},
		},
		// ── SHARED-DEFAULT (no per-session engine, no promotion, no rehydration) ──
		{
			name: "default/shared-engine-no-plan-slot",
			// A default-FS session with ModeNeedsEngine=nil: the session always rides
			// the shared engine. Factory calls must be 0.
			run: func(t *testing.T) (func(), error) {
				t.Helper()
				ctx := context.Background()
				store := memstore.New()
				var modes []session.PermissionMode
				var calls atomic.Int32
				// needsEngine=nil ⇒ CASE 2 never fires.
				svc := modeServiceOverStore(t, store, modeRecordingFactory("gpt-5", "opus-plan", &modes, &calls), nil)
				sess, err := svc.CreateSession(ctx, session.ModeDefault, session.Limits{})
				if err != nil {
					t.Fatalf("CreateSession: %v", err)
				}
				reply := drainAndFinish(t, svc, sess.ID, mustStart(t, svc, sess.ID, "t1"))
				return func() {
					// SHARED-DEFAULT: factory never invoked; reply comes from the shared engine.
					if calls.Load() != 0 {
						t.Errorf("default/shared-no-plan: factory calls = %d, want 0", calls.Load())
					}
					if reply != "SHARED-ENGINE-REPLY" {
						t.Errorf("default/shared-no-plan: reply = %q, want SHARED-ENGINE-REPLY", reply)
					}
					if svc.HasSessionEngineForTest(sess.ID) {
						t.Errorf("default/shared-no-plan: HasSessionEngine = true, want false (shared engine)")
					}
				}, nil
			},
		},
		// ── SHARED-DEFAULT (mode flip without a plan slot — byte-identical guard) ─
		{
			name: "default/shared-engine-mode-flip-no-promotion",
			// Same as above but a SetMode(plan) fires between turns. With ModeNeedsEngine
			// nil CASE 2 never fires: the session stays on the shared engine (byte-identical
			// regression guard from TestModeFlipByteIdenticalWithoutPlanSlot).
			run: func(t *testing.T) (func(), error) {
				t.Helper()
				ctx := context.Background()
				store := memstore.New()
				var modes []session.PermissionMode
				var calls atomic.Int32
				svc := modeServiceOverStore(t, store, modeRecordingFactory("gpt-5", "opus-plan", &modes, &calls), nil)
				sess, err := svc.CreateSession(ctx, session.ModeDefault, session.Limits{})
				if err != nil {
					t.Fatalf("CreateSession: %v", err)
				}
				_ = drainAndFinish(t, svc, sess.ID, mustStart(t, svc, sess.ID, "t1"))
				if _, err := svc.SetMode(ctx, sess.ID, session.ModePlan); err != nil {
					t.Fatalf("SetMode: %v", err)
				}
				reply := drainAndFinish(t, svc, sess.ID, mustStart(t, svc, sess.ID, "t2"))
				return func() {
					// SHARED-DEFAULT: no promotion despite mode change (no plan slot).
					if calls.Load() != 0 {
						t.Errorf("default/shared-mode-flip: factory calls = %d, want 0 (no plan slot ⇒ no promotion)", calls.Load())
					}
					if reply != "SHARED-ENGINE-REPLY" {
						t.Errorf("default/shared-mode-flip: reply = %q, want SHARED-ENGINE-REPLY", reply)
					}
					if svc.HasSessionEngineForTest(sess.ID) {
						t.Errorf("default/shared-mode-flip: HasSessionEngine = true, want false (no promotion without plan slot)")
					}
				}, nil
			},
		},
		// ── REHYDRATE via the AWAITING-RESUME caller (cross-caller drift guard) ────
		// engineAndEnvironmentFor is the SHARED resolution point for BOTH StartRunContent
		// AND resumeFromAwaiting (its doc comment, ~service.go:1338-1342). Every row
		// above drives the StartRun caller; this row drives the OTHER caller so the two
		// cannot drift unnoticed — the exact risk the shared function exists to prevent.
		//
		// Which branch does the resume caller reach? The CASE 1 mode-rebuild branch is a
		// documented NO-OP on the resume path (SetMode is rejected from StateAwaiting, so
		// se.builtForMode==sess.Mode always holds; ~service.go:1875-1883). But the
		// REHYDRATE branch IS live and DISTINCT: a SELECTOR session that parked awaiting
		// and whose process died has no per-session engine after a restart, so the resume
		// caller must rehydrate it through engineAndEnvironmentFor — the same branch the
		// StartRun rehydrate row exercises, but reached via Approve→resumeFromAwaiting.
		//
		// Sequence (offline, two-Build restart — the resume_awaiting_test.go convention):
		//  1. svc1: a SELECTOR session whose per-session engine emits a Write ask; drive
		//     it to StateAwaiting, Persist the awaiting snapshot, then Cancel (the parked
		//     run "dies"). The factory engine has NO Store so the cancel terminal does not
		//     overwrite the awaiting snapshot (the parking-service discipline).
		//  2. svc2 over the SAME store (restart: empty in-memory registries). Its factory
		//     returns a CONTINUATION engine. Approve → no live run → resumeFromAwaiting →
		//     engineAndEnvironmentFor → REHYDRATE (selector, no engine) → factory called ONCE.
		//  3. Assert: svc2 factory called exactly once (REHYDRATE via the resume caller),
		//     HasSessionEngine true, the pending Write executed exactly once, run completes.
		{
			name: "rehydrate/awaiting-resume-caller",
			run: func(t *testing.T) (func(), error) {
				ctx := context.Background()
				store := memstore.New()
				sel := server.ProviderSelector{ProviderID: "openai", ModelID: "gpt-5"}

				// askingSelectorFactory builds a per-session engine over a writeAskTool
				// catalog. `ran` counts Write executions; `llm` is the engine's provider
				// turn; `store` (nil for the parking svc) controls engine auto-save. calls
				// counts factory invocations for the rehydration assertion.
				askingSelectorFactory := func(ran *atomic.Int64, llm port.LLMProvider, engineStore port.SessionStore, calls *atomic.Int32) server.SessionEngineFactory {
					return func(_ context.Context, _ server.ProviderSelector, _ []mcp.ServerConfig, _ server.SessionProfile, _ string, mode session.PermissionMode) (server.SessionEngineResult, error) {
						if calls != nil {
							calls.Add(1)
						}
						cat := tool.NewCatalog()
						cat.MustRegister(&writeAskTool{ran: ran})
						eng := agent.NewEngine(agent.Deps{
							LLM:     llm,
							Catalog: cat,
							Policy:  permpolicy.NewPolicy(nil, nil),
							Model:   "gpt-5",
							Store:   engineStore,
						})
						return server.SessionEngineResult{
							Engine: eng, ModelID: "gpt-5", ProviderID: "openai",
							BuiltForMode: mode, Close: func() error { return nil },
						}, nil
					}
				}

				// Step 1: svc1 — selector session parks awaiting. engineStore nil so the
				// cancel terminal does not clobber the awaiting snapshot.
				var ran1 atomic.Int64
				var calls1 atomic.Int32
				svc1 := modeServiceOverStore(t, store,
					askingSelectorFactory(&ran1,
						mockllm.New(mockllm.ToolCallTurn(session.NewToolCall("w1", "Write", json.RawMessage(`{"path":"a.go"}`)))),
						nil, &calls1),
					nil /* no plan slot — REHYDRATE, not PROMOTE */)
				sess, err := svc1.CreateSessionWithProvider(ctx, session.ModeDefault, session.Limits{}, sel)
				if err != nil {
					t.Fatalf("CreateSessionWithProvider: %v", err)
				}
				askID := driveServiceToAwaiting(t, svc1, sess.ID)
				if ran1.Load() != 0 {
					t.Fatalf("Write executed %d time(s) pre-approval, want 0 (parked at the ask)", ran1.Load())
				}

				// Step 2: svc2 over the SAME store — the restart. Continuation engine.
				var ran2 atomic.Int64
				var calls2 atomic.Int32
				svc2 := modeServiceOverStore(t, store,
					askingSelectorFactory(&ran2, mockllm.New(mockllm.TextTurn("done after approval")), store, &calls2),
					nil)

				run, err := svc2.ApproveRun(ctx, sess.ID, askID, session.VerdictAllowOnce, "")
				if err != nil {
					return nil, fmt.Errorf("awaiting-resume-caller: ApproveRun after restart: %w", err)
				}
				if run == nil {
					return nil, fmt.Errorf("awaiting-resume-caller: ApproveRun returned nil run — the resumeFromAwaiting rehydrate path did not fire")
				}
				var stop session.StopReason
				for ev := range run.Events() {
					if ev.Type == session.EvResult && ev.Result != nil {
						stop = ev.Result.Stop
					}
				}
				svc2.FinishRun(sess.ID, run)

				return func() {
					// REHYDRATE reached via the resume caller: svc2 factory called exactly once.
					if calls2.Load() != 1 {
						t.Errorf("awaiting-resume-caller: svc2 factory calls = %d, want 1 (engineAndEnvironmentFor REHYDRATE via resumeFromAwaiting)", calls2.Load())
					}
					if !svc2.HasSessionEngineForTest(sess.ID) {
						t.Errorf("awaiting-resume-caller: svc2 HasSessionEngine = false, want true (rehydrated on resume)")
					}
					// The pending Write executed exactly once on the rehydrated engine, and the
					// resumed run reached a clean terminal — proof the resolved engine is the
					// real per-session (asking) engine, not the shared one.
					if ran2.Load() != 1 {
						t.Errorf("awaiting-resume-caller: pending Write executed %d time(s) on resume, want exactly 1", ran2.Load())
					}
					if stop != session.StopEndTurn {
						t.Errorf("awaiting-resume-caller: resumed run stop = %q, want %q", stop, session.StopEndTurn)
					}
				}, nil
			},
		},
	}

	for _, row := range rows {
		row := row
		t.Run(row.name, func(t *testing.T) {
			assertFn, wantErr := row.run(t)
			if wantErr != nil {
				// For error rows the wantErr IS the assertion: it contains the mismatch
				// description from errors.Is checks within the row closure.
				t.Fatal(wantErr)
			}
			if assertFn != nil {
				// Each row reads its own captured factory counters/replies from closures.
				assertFn()
			}
		})
	}
}
