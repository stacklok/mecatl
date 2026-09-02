package app

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/prompt"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/hookexec"
	"github.com/stacklok/mecatl/internal/adapter/server"
	"github.com/stacklok/mecatl/internal/adapter/store/jsonlstore"
)

// schedule_posture_test.go pins the ADR-0073 Schedule tool's posture surface:
// the model-visible prompt affordance (ADR 0070, AC4.1), the floor-scoped
// Allow across the posture tiers (AC4.2), the mutating-create plan-mode gate
// end-to-end at the loop level (AC4.3), and the full in-chat create→list→fire
// scenario (AC4.4). The TUI embedded-scheduler default (AC2.4) lives in
// cmd/mecatui (TestScheduleTool_TuiEmbeddedSchedulerOn).

// scheduleFloorCall builds the Schedule tool call the permission fold resolves.
func scheduleFloorCall() session.ToolCall {
	return session.NewToolCall("id", agent.ScheduleToolName, []byte(`{"verb":"list"}`))
}

// TestScheduleTool_FloorScopedAllowAllTiers pins AC4.2: the Schedule tool
// resolves as a ScopeBuiltinDefault Allow (NO ask) under the default `auto`
// posture AND under `strict`/`trusted` — the floor defaultRules carries — and
// stays config-overridable (an operator deny binds it). The tier ladder goes
// through applyPosture (strict/trusted leave AllowAllTools off; auto/yolo turn
// it on) over the PRODUCTION mainRules assembly, never a second list.
func TestScheduleTool_FloorScopedAllowAllTiers(t *testing.T) {
	t.Parallel()
	// The defaultRules floor row is ScopeBuiltinDefault-scoped: pre-approved but
	// config-overridable, the memory-tool posture (never a configured Allow).
	var floor *governance.Rule
	for i := range defaultRules() {
		if defaultRules()[i].Tool == agent.ScheduleToolName {
			floor = &defaultRules()[i]
			break
		}
	}
	if floor == nil {
		t.Fatal("defaultRules has no Schedule floor row (Task 01's floor Allow)")
	}
	if floor.Effect != governance.Allow || floor.Scope != governance.ScopeBuiltinDefault {
		t.Fatalf("Schedule floor row = {Effect:%v Scope:%v}, want {Allow, ScopeBuiltinDefault} (floor-scoped, config-overridable)", floor.Effect, floor.Scope)
	}

	// Every tier resolves the tool to Allow in default mode. applyPosture
	// derives the tier's knobs exactly as Build does, so a future tier-derived
	// knob that mainRules consumes is exercised too.
	for _, tc := range []struct {
		name string
		tier Posture
	}{
		{"auto (the default)", PostureAuto},
		{"strict", PostureStrict},
		{"trusted", PostureTrusted},
		{"yolo", PostureYolo},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := applyPosture(Config{Posture: tc.tier})
			policy := permpolicy.NewPolicy(mainRules(cfg), nil, mainEvaluatorOptions(cfg)...)
			got := policy.Evaluate(context.Background(), "s1", session.ModeDefault, scheduleFloorCall(), nil)
			if got.Effect != governance.Allow {
				t.Fatalf("Schedule under posture %s: effect = %v (%s), want Allow (floor-scoped, no ask)", tc.name, got.Effect, got.Reason)
			}
		})
	}

	// Config-overridable: an operator DENY binds the floor (deny-dominant — a
	// deny in ANY scope is absolute), and a configured Ask likewise beats the
	// floor Allow. Pinned at the auto tier (the default) and strict (the
	// fail-closed one); the fold is tier-independent.
	for _, tier := range []Posture{PostureAuto, PostureStrict} {
		cfg := applyPosture(Config{Posture: tier})
		for _, eff := range []governance.Effect{governance.Deny, governance.Ask} {
			rules := append(mainRules(cfg),
				governance.Rule{Scope: governance.ScopeUser, Tool: agent.ScheduleToolName, Effect: eff})
			policy := permpolicy.NewPolicy(rules, nil, mainEvaluatorOptions(cfg)...)
			got := policy.Evaluate(context.Background(), "s1", session.ModeDefault, scheduleFloorCall(), nil)
			if got.Effect != eff {
				t.Fatalf("a configured (ScopeUser) %v on Schedule under posture %s must beat the floor Allow; got %v (%s)", eff, tier, got.Effect, got.Reason)
			}
		}
	}
}

// TestApplySchedulePostureAppendsNote proves the helper appends the Schedule
// tool's model-visible instruction to the Role (DefaultRole fallback first —
// the applyNoFSPosture/applyPlanModePosture idiom) and is a no-op when the
// session's catalog has no Schedule tool (a store with no ScheduleStore).
func TestApplySchedulePostureAppendsNote(t *testing.T) {
	t.Parallel()
	pc := applySchedulePosture(prompt.Config{}, true)
	if pc.Role == "" {
		t.Fatal("applySchedulePosture on an empty Config must fall back to DefaultRole")
	}
	if !strings.Contains(pc.Role, schedulePostureNote) {
		t.Fatalf("Role missing the Schedule posture note\ngot=%q", pc.Role)
	}

	// An explicit Role is preserved; the note is appended.
	custom := applySchedulePosture(prompt.Config{Role: "custom-role"}, true)
	if !strings.HasPrefix(custom.Role, "custom-role") {
		t.Fatalf("explicit Role was not preserved; got %q", custom.Role)
	}
	if !strings.Contains(custom.Role, schedulePostureNote) {
		t.Fatal("the Schedule note was not appended to the explicit Role")
	}

	// A session with NO Schedule tool (the store backs no ScheduleStore) must
	// NOT carry the instruction — the model must not be told about a tool it
	// cannot call.
	off := applySchedulePosture(prompt.Config{Role: "just-role"}, false)
	if off.Role != "just-role" {
		t.Fatalf("applySchedulePosture without the tool must be a no-op; got Role=%q", off.Role)
	}
}

// TestScheduleTool_EngineSystemPromptContainsScheduleContract is the
// model-visible-discoverability gate for the Schedule tool (ADR 0070, AC4.1): a
// tool whose correct use depends on the model CALLING it (create a schedule
// instead of promising to "remember", list before duplicating, fire to verify)
// MUST ship a model-visible prompt instruction telling it so, AND a test
// proving that instruction lands in the built engine's system prompt via the
// REAL factory path — so deleting the applySchedulePosture wiring in
// sessionEngineFactory fails CI.
//
// It drives a one-turn run through a factory-built engine whose per-session
// catalog carries the Schedule tool and captures the LLM request. It asserts
// the note lands on the StablePrefix (the Role layer applySchedulePosture
// owns), NOT merely on the combined Render() — the tool's own Spec description
// also rides the rendered prompt, so a Render() oracle would stay green if the
// factory wiring were silently dropped.
func TestScheduleTool_EngineSystemPromptContainsScheduleContract(t *testing.T) {
	ctx := context.Background()
	const sessionModel = "gpt-5"

	// A ScheduleStore-backed manager so the per-session catalog REGISTERS the
	// Schedule tool (the registration gate the posture note mirrors).
	jstore, err := jsonlstore.New(t.TempDir())
	if err != nil {
		t.Fatalf("jsonlstore.New: %v", err)
	}
	svc := newScheduleTestService(t, jstore, mockllm.New(mockllm.TextTurn("x")), t.TempDir())

	var captured prompt.Layered
	var invoked bool
	provider := mockllm.NewWith([]mockllm.Option{
		mockllm.WithRequestObserver(func(req port.LLMRequest) {
			captured = req.System
			invoked = true
		}),
	}, mockllm.TextTurn("ok"))
	cfg := Config{Model: sessionModel}
	reg := regForTest(provider, providerOpenAI, sessionModel)
	assets := catalogAssets{scheduleManagerFactory: svc.ScheduleManager}
	factory := sessionEngineFactory(cfg, reg, provider, memstore.New(),
		permpolicy.NewPolicy(defaultRules(), nil), hookexec.New(nil), nil,
		prompt.RootAssembler{}, assets, nil)

	res, err := factory(ctx, server.ProviderSelector{}, nil, server.ProfileDefault, "", session.ModeDefault)
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	defer func() { _ = res.Close() }()

	// Drive a one-turn run to trigger buildRequest → prompt.Build → captured system.
	sess := session.New("s1", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{MaxTurns: 1}, time.Now())
	run := res.Engine.Run(ctx, sess, memEnvironment("/ws"), agent.RunRequest{Text: "hi", Parts: nil})
	for range run.Events() {
	}
	if !invoked {
		t.Fatal("the LLM was not invoked; the mock script may be insufficient")
	}
	// The note lives on the StablePrefix — the Role layer applySchedulePosture
	// appends to in sessionEngineFactory. Assert the note VERBATIM (its own
	// distinctive, Role-owned text), NOT generic "create"/"list"/"fire" clauses:
	// the StablePrefix ALSO carries a tool-inventory block whose Schedule Spec
	// description contains those words, so a generic-clause oracle stays green
	// when the factory wiring is deleted (proven by mutation: removing the
	// applySchedulePosture wiring leaves the inventory block intact). Only the
	// note's own Role-instruction text distinguishes the Role layer from the
	// inventory block — the note opens "You have a Schedule tool", the Spec
	// description opens "Manage scheduled tasks".
	if !strings.Contains(captured.StablePrefix, schedulePostureNote) {
		t.Errorf("StablePrefix missing the Schedule posture note (the Role-layer applySchedulePosture wiring)\ngot StablePrefix (first 600):\n%s",
			firstN(captured.StablePrefix, 600))
	}

	// The honest-absence half: a per-session engine whose catalog has NO
	// Schedule tool (the store backs no ScheduleStore — assets without a
	// factory) must NOT carry the instruction in its Role layer.
	var capturedNone prompt.Layered
	providerNone := mockllm.NewWith([]mockllm.Option{
		mockllm.WithRequestObserver(func(req port.LLMRequest) { capturedNone = req.System }),
	}, mockllm.TextTurn("ok"))
	regNone := regForTest(providerNone, providerOpenAI, sessionModel)
	factoryNone := sessionEngineFactory(cfg, regNone, providerNone, memstore.New(),
		permpolicy.NewPolicy(defaultRules(), nil), hookexec.New(nil), nil,
		prompt.RootAssembler{}, catalogAssets{}, nil)
	resNone, err := factoryNone(ctx, server.ProviderSelector{}, nil, server.ProfileDefault, "", session.ModeDefault)
	if err != nil {
		t.Fatalf("factory (no Schedule): %v", err)
	}
	defer func() { _ = resNone.Close() }()
	sessNone := session.New("s2", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{MaxTurns: 1}, time.Now())
	runNone := resNone.Engine.Run(ctx, sessNone, memEnvironment("/ws"), agent.RunRequest{Text: "hi", Parts: nil})
	for range runNone.Events() {
	}
	if strings.Contains(capturedNone.StablePrefix, schedulePostureNote) {
		t.Fatal("a session with NO Schedule tool carries the Schedule instruction in its StablePrefix — the model must not be told about a tool it cannot call")
	}
}

// TestFireDelivery_ScheduleToolNoteLands pins DoD #7 (ADR 0070, the
// model-visible-affordance gate) for the fire-result-delivery capability: the
// BUILT engine's system prompt (via the REAL sessionEngineFactory path, not the
// helper in isolation) tells the model that a schedule it creates reports its
// fire's result back into THIS conversation — the instruction that lets the
// model set the operator's expectation ("the outcome arrives in this chat, not
// a separate session"). Without it the model cannot promise the delivery, and
// decision #5's "visible in the connected client" is undermined at the prompt
// layer. Asserted against the StablePrefix (the Role layer applySchedulePosture
// appends to), not the combined Render() — the tool-inventory block also
// carries the Spec description (which now carries the same line), so a
// combined-layer oracle would stay green even if the Role wiring were deleted.
func TestFireDelivery_ScheduleToolNoteLands(t *testing.T) {
	ctx := context.Background()
	const sessionModel = "gpt-5"

	jstore, err := jsonlstore.New(t.TempDir())
	if err != nil {
		t.Fatalf("jsonlstore.New: %v", err)
	}
	svc := newScheduleTestService(t, jstore, mockllm.New(mockllm.TextTurn("x")), t.TempDir())

	var captured prompt.Layered
	var invoked bool
	provider := mockllm.NewWith([]mockllm.Option{
		mockllm.WithRequestObserver(func(req port.LLMRequest) {
			captured = req.System
			invoked = true
		}),
	}, mockllm.TextTurn("ok"))
	cfg := Config{Model: sessionModel}
	reg := regForTest(provider, providerOpenAI, sessionModel)
	assets := catalogAssets{scheduleManagerFactory: svc.ScheduleManager}
	factory := sessionEngineFactory(cfg, reg, provider, memstore.New(),
		permpolicy.NewPolicy(defaultRules(), nil), hookexec.New(nil), nil,
		prompt.RootAssembler{}, assets, nil)

	res, err := factory(ctx, server.ProviderSelector{}, nil, server.ProfileDefault, "", session.ModeDefault)
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	defer func() { _ = res.Close() }()

	sess := session.New("s1", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{MaxTurns: 1}, time.Now())
	run := res.Engine.Run(ctx, sess, memEnvironment("/ws"), agent.RunRequest{Text: "hi", Parts: nil})
	for range run.Events() {
	}
	if !invoked {
		t.Fatal("the LLM was not invoked; the mock script may be insufficient")
	}
	// The reports-back instruction (the Role-layer note) must land verbatim.
	const reportsBack = "reports its fire's result back into THIS conversation"
	if !strings.Contains(captured.StablePrefix, reportsBack) {
		t.Errorf("StablePrefix missing the fire-result-delivery reports-back instruction (ADR 0070)\ngot StablePrefix (first 800):\n%s",
			firstN(captured.StablePrefix, 800))
	}
}

// TestScheduleTool_MutatingCreateGatedByPlanMode pins AC4.3 end-to-end at the
// LOOP level: in a PLAN-MODE session a mutating: true create is DENIED (the
// plan-mode hard-deny on mutations), while a read-leaning (mutating: false)
// create is ALLOWED. The mechanism: a plan-mode session's per-session catalog
// carries the PLAN-AWARE Schedule variant (ReadOnly()==true so the catalog
// projection advertises it; the mutating create is denied per call before the
// base tool runs), so the model is TOLD the tool exists and only the mutating
// create is refused — the read-leaning create plan mode must keep drives
// through to the shared store.
//
// The dispatch path is driven through the REAL factory-built plan-mode engine:
// mockllm scripts one mutating-create turn (denied) then one read-leaning
// create turn (allowed); the assertions read the recorded conversation and the
// shared store.
func TestScheduleTool_MutatingCreateGatedByPlanMode(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	jstore, err := jsonlstore.New(t.TempDir())
	if err != nil {
		t.Fatalf("jsonlstore.New: %v", err)
	}
	svc := newScheduleTestService(t, jstore, mockllm.New(mockllm.TextTurn("x")), workspace)

	const sessionModel = "gpt-5"
	var capturedReq port.LLMRequest
	provider := mockllm.NewWith([]mockllm.Option{
		mockllm.WithRequestObserver(func(req port.LLMRequest) { capturedReq = req }),
	},
		// Turn 1: the model attempts a mutating create — plan mode must deny it.
		mockllm.ToolCallTurn(session.NewToolCall("c1", agent.ScheduleToolName,
			[]byte(`{"verb":"create","name":"mut","prompt":"p","cron":"@every 1h","mutating":true}`))),
		// Turn 2: the model falls back to a read-leaning create — plan mode allows it.
		mockllm.ToolCallTurn(session.NewToolCall("c2", agent.ScheduleToolName,
			[]byte(`{"verb":"create","name":"ro","prompt":"p","cron":"@every 1h"}`))),
		// Turn 3: done.
		mockllm.TextTurn("created the read-leaning schedule"),
	)
	cfg := Config{Model: sessionModel}
	reg := regForTest(provider, providerOpenAI, sessionModel)
	assets := catalogAssets{scheduleManagerFactory: svc.ScheduleManager}
	factory := sessionEngineFactory(cfg, reg, provider, memstore.New(),
		permpolicy.NewPolicy(defaultRules(), nil), hookexec.New(nil), nil,
		prompt.RootAssembler{}, assets, nil)

	// A PLAN-MODE session engine — its catalog carries the plan-aware variant.
	res, err := factory(ctx, server.ProviderSelector{}, nil, server.ProfileDefault, "", session.ModePlan)
	if err != nil {
		t.Fatalf("factory(plan): %v", err)
	}
	defer func() { _ = res.Close() }()

	sess := session.New("s1", session.ModePlan, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{MaxTurns: 5}, time.Now())
	// Save the session to the store the schedule manager validates against, so
	// OriginSessionID validation (which checks the session exists) passes.
	if err := jstore.Save(ctx, sess); err != nil {
		t.Fatalf("Save: %v", err)
	}
	run := res.Engine.Run(ctx, sess, memEnvironment("/ws"), agent.RunRequest{Text: "schedule the work", Parts: nil})
	var results []session.ToolResult
	for ev := range run.Events() {
		if ev.Type == session.EvToolResult && ev.ToolResult != nil {
			results = append(results, *ev.ToolResult)
		}
	}

	// The plan-mode catalog ADVERTISES the Schedule tool (the plan-aware
	// variant is ReadOnly()==true, so the mode projection keeps it) — the model
	// is told the tool exists; only the mutating create is refused. Pinned on
	// the advertised spec set the captured request carries (req.Tools is the
	// mode projection the loop reads to advertise tools to the model).
	advertised := false
	for _, spec := range capturedReq.Tools {
		if spec.Name == agent.ScheduleToolName {
			advertised = true
		}
	}
	if !advertised {
		t.Fatal("the plan-mode request does NOT advertise the Schedule tool — the plan-aware variant must keep it visible (ReadOnly()==true)")
	}

	if len(results) != 2 {
		t.Fatalf("got %d tool results, want 2 (the mutating-create deny + the read-leaning create)", len(results))
	}

	// The mutating create is DENIED with the plan-mode reason (never reached
	// the manager), the read-leaning create is ALLOWED and lands in the store.
	var deny, create *session.ToolResult
	for i := range results {
		switch results[i].CallID {
		case "c1":
			deny = &results[i]
		case "c2":
			create = &results[i]
		}
	}
	if deny == nil || !deny.IsError || !strings.Contains(deny.Content, "plan mode") {
		t.Fatalf("mutating-create result = %+v, want a plan-mode deny", deny)
	}
	if create == nil || create.IsError {
		t.Fatalf("read-leaning-create result = %+v, want allowed", create)
	}
	if _, err := svc.GetSchedule(ctx, "mut"); err == nil {
		t.Fatal("the mutating create reached the store, want denied before it")
	}
	if _, err := svc.GetSchedule(ctx, "ro"); err != nil {
		t.Fatalf("the read-leaning create did not land in the shared store: %v", err)
	}
}

// TestScheduleTool_Scenario4_FullInChatFlow pins AC4.4 (Scenario 4): an offline
// engine (mockllm) drives the model to create a schedule, list it, and fire
// it — and the fire mints a sched-- session that runs to a terminal stop. The
// whole flow rides the REAL seams: the factory-built engine's Schedule tool
// over the Service's schedule manager, and the synchronous-to-terminal FireNow
// minting a real sched-- session driven to StopEndTurn.
func TestScheduleTool_Scenario4_FullInChatFlow(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	storeDir := t.TempDir()
	jstore, err := jsonlstore.New(storeDir)
	if err != nil {
		t.Fatalf("jsonlstore.New: %v", err)
	}
	svc := newScheduleTestService(t, jstore, mockllm.New(mockllm.TextTurn("fire done")), workspace)
	startTestScheduler(t, svc, jstore.ScheduleStore(), fireFuncForScheduleTest(svc))

	const sessionModel = "gpt-5"
	provider := mockllm.New(
		// Turn 1: create the schedule.
		mockllm.ToolCallTurn(session.NewToolCall("c1", agent.ScheduleToolName,
			[]byte(`{"verb":"create","name":"nightly","prompt":"check ci","cron":"@every 1m"}`))),
		// Turn 2: list it (a READ-ONLY verb — on the ScheduleQuery tool after the
		// AC1.4 split).
		mockllm.ToolCallTurn(session.NewToolCall("c2", agent.ScheduleQueryToolName, []byte(`{"verb":"list"}`))),
		// Turn 3: fire it.
		mockllm.ToolCallTurn(session.NewToolCall("c3", agent.ScheduleToolName, []byte(`{"verb":"fire","name":"nightly"}`))),
		// Turn 4: done.
		mockllm.TextTurn("scheduled and fired"),
	)
	cfg := Config{Model: sessionModel}
	reg := regForTest(provider, providerOpenAI, sessionModel)
	assets := catalogAssets{scheduleManagerFactory: svc.ScheduleManager}
	factory := sessionEngineFactory(cfg, reg, provider, memstore.New(),
		permpolicy.NewPolicy(defaultRules(), nil), hookexec.New(nil), nil,
		prompt.RootAssembler{}, assets, nil)

	res, err := factory(ctx, server.ProviderSelector{}, nil, server.ProfileDefault, "", session.ModeDefault)
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	defer func() { _ = res.Close() }()

	sess := session.New("s1", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{MaxTurns: 6}, time.Now())
	// Save the session to the store the schedule manager validates against, so
	// OriginSessionID validation (which checks the session exists) passes.
	if err := jstore.Save(ctx, sess); err != nil {
		t.Fatalf("Save: %v", err)
	}
	run := res.Engine.Run(ctx, sess, memEnvironment("/ws"), agent.RunRequest{Text: "schedule a nightly ci check and fire it once", Parts: nil})
	var results []session.ToolResult
	var stop session.StopReason
	for ev := range run.Events() {
		switch ev.Type {
		case session.EvToolResult:
			if ev.ToolResult != nil {
				results = append(results, *ev.ToolResult)
			}
		case session.EvResult:
			if ev.Result != nil {
				stop = ev.Result.Stop
			}
		}
	}
	if stop != session.StopEndTurn {
		t.Fatalf("in-chat run stop = %q, want end_turn (the model drove create→list→fire then stopped)", stop)
	}
	if len(results) != 3 {
		t.Fatalf("got %d tool results, want 3 (create, list, fire)", len(results))
	}
	for i := range results {
		if results[i].IsError {
			t.Fatalf("tool result %d = error %q, want success (the full in-chat flow must drive clean)", i, results[i].Content)
		}
	}
	// The create + fire results carry the expected markers; the fire's result
	// names the minted sched-- session and its terminal stop.
	if !strings.Contains(results[0].Content, "nightly") {
		t.Fatalf("create result = %q, want the schedule named", results[0].Content)
	}
	if !strings.Contains(results[1].Content, "nightly") {
		t.Fatalf("list result = %q, want the schedule listed", results[1].Content)
	}
	fireRes := results[2].Content
	if !strings.Contains(fireRes, "sched--") || !strings.Contains(fireRes, "session id:") {
		t.Fatalf("fire result = %q, want the minted sched-- session id", fireRes)
	}

	// The fire minted a sched-- session that ran to a terminal stop — the fire
	// record's stop is recorded in the SAME store (the synchronous-to-terminal
	// FireNow contract).
	if !scheduleEventually(15*time.Second, func() bool {
		fires, _ := jstore.ScheduleStore().ListFires(ctx, "nightly")
		for _, f := range fires {
			if f.Stop != "" {
				return true
			}
		}
		return false
	}) {
		t.Fatal("the fired sched-- session did not reach a terminal stop within 15s")
	}
	fires, err := jstore.ScheduleStore().ListFires(ctx, "nightly")
	if err != nil || len(fires) == 0 {
		t.Fatalf("ListFires(nightly) = (%v, %d), want the recorded fire", err, len(fires))
	}
	if !strings.HasPrefix(fires[0].ID, "sched--") {
		t.Fatalf("fire id = %q, want the sched-- minted session", fires[0].ID)
	}
	if fires[0].Stop != session.StopEndTurn {
		t.Fatalf("fire stop = %q, want end_turn (the sched-- session ran to a terminal stop)", fires[0].Stop)
	}
}
