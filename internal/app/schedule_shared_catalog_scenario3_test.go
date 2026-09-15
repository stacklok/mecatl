package app

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/prompt"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/scheduler"
	"github.com/stacklok/mecatl/internal/adapter/store/jsonlstore"
)

// scheduleScenario3Build is the shared fixture for the Scenario-3 pins: a full
// store-backed app.Build over a scripted provider. With NO model selector, NO
// MCP servers, and a workspace equal to the launch root, every session the
// fixture creates rides the SHARED engine fast path (sessionNeedsPerFactory is
// false) — the plain mecatui launch.
func scheduleScenario3Build(t *testing.T, llm port.LLMProvider) (*Built, string) {
	t.Helper()
	workspace := t.TempDir()
	built, err := buildIsolated(t, context.Background(), Config{
		Workspace:    workspace,
		Model:        "mock",
		StoreDir:     t.TempDir(), // jsonlstore — backs a ScheduleStore
		MockProvider: llm,
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	t.Cleanup(built.Close)
	return built, workspace
}

// TestScheduleSharedCatalog_Scenario3_DefaultSessionHasTool pins AC3.1: a
// default-profile, zero-selector session on a store-backed Build resolves the
// Schedule tool from its engine's catalog — the shared-engine fast path, no
// per-session factory involved. The session is created on the launch-root
// workspace (sessionNeedsPerFactory false → the SHARED engine), and the oracle
// is the run's OWN EvToolResult: a tool the catalog resolved EXECUTES, while an
// unregistered tool comes back `unknown tool`. (The store-side
// ServerCapabilities.Scheduling bit is NOT the oracle: scheduleStore() != nil
// is green even when the shared catalog lacks the tools.)
func TestScheduleSharedCatalog_Scenario3_DefaultSessionHasTool(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// The shared engine's catalog resolves BOTH schedule tools: a scripted
	// dispatch of each through this session's run-entry funnel EXECUTES the tool
	// (an EvToolResult with NO `unknown tool` error) — the loop resolved it from
	// the engine's catalog. A default-profile, zero-selector session rides the
	// SHARED engine (sessionNeedsPerFactory is false for it — no per-session
	// factory is involved), so the catalog the dispatch consulted IS the shared
	// engine's. (The store-side ServerCapabilities.Scheduling bit is NOT the
	// oracle: scheduleStore() != nil is green even when the shared catalog lacks
	// the tools.)
	llm2 := mockllm.New(
		mockllm.ToolCallTurn(session.NewToolCall("q1", agent.ScheduleQueryToolName, []byte(`{"verb":"list"}`))),
		mockllm.ToolCallTurn(session.NewToolCall("q2", agent.ScheduleToolName,
			[]byte(`{"verb":"create","name":"ac31","prompt":"p","cron":"@every 1h"}`))),
		mockllm.TextTurn("done"),
	)
	built2, _ := scheduleScenario3Build(t, llm2)
	// Launch-root workspace → sessionNeedsPerFactory false → the SHARED engine.
	sess2, err := built2.Service.CreateSession(ctx, session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession (dispatch): %v", err)
	}
	run, err := built2.Service.StartRunContent(ctx, sess2.ID, "list then create", nil)
	if err != nil {
		t.Fatalf("StartRunContent: %v", err)
	}
	executed := map[string]bool{}
	callNames := map[session.ToolCallID]string{}
	for ev := range run.Events() {
		switch ev.Type {
		case session.EvToolCall:
			if ev.ToolCall != nil {
				callNames[ev.ToolCall.ID] = ev.ToolCall.Name
			}
		case session.EvToolResult:
			if ev.ToolResult == nil {
				continue
			}
			name := callNames[ev.ToolResult.CallID]
			if strings.Contains(ev.ToolResult.Content, "unknown tool") {
				t.Errorf("the SHARED engine's catalog rejected %q as unknown — the build-time eager scheduleManagerFactory bind (ADR 0076) must carry it; result: %q", name, ev.ToolResult.Content)
				continue
			}
			executed[name] = true
		}
	}
	for _, name := range []string{agent.ScheduleToolName, agent.ScheduleQueryToolName} {
		if !executed[name] {
			t.Errorf("no executed EvToolResult for %q from a shared-engine session — the SHARED engine's catalog must resolve it", name)
		}
	}
}

// TestScheduleSharedCatalog_Scenario3_SystemPromptCarriesScheduleNote pins
// AC3.2 (ADR 0070, the model-visible-affordance gate): the SHARED engine's
// built system prompt carries the schedulePostureNote on the Role /
// StablePrefix layer — asserted against req.System.StablePrefix, NOT the
// combined Render() (the tool-inventory block also rides Render, so a
// combined oracle would stay green if the Role wiring were silently dropped).
// A default-profile, zero-selector session on a store-backed Build rides the
// shared engine, so the request its run builds is the shared engine's prompt.
func TestScheduleSharedCatalog_Scenario3_SystemPromptCarriesScheduleNote(t *testing.T) {
	t.Parallel()
	var captured prompt.Layered
	var invoked bool
	llm := mockllm.NewWith([]mockllm.Option{
		mockllm.WithRequestObserver(func(req port.LLMRequest) {
			captured = req.System
			invoked = true
		}),
	}, mockllm.TextTurn("ok"))
	built, _ := scheduleScenario3Build(t, llm)
	ctx := context.Background()

	// Create the session on the LAUNCH-ROOT workspace (ws == cfg.Workspace) so
	// sessionNeedsPerFactory is false and it rides the SHARED engine — the
	// shared-engine fast path a plain mecatui launch takes. (A DIFFERENT
	// workspace would route to the per-session factory, which already carries
	// the note — asserting there would not exercise the shared engine.)
	sess, err := built.Service.CreateSession(ctx, session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	run, err := built.Service.StartRunContent(ctx, sess.ID, "hello", nil)
	if err != nil {
		t.Fatalf("StartRunContent: %v", err)
	}
	for range run.Events() {
	}
	if !invoked {
		t.Fatal("the LLM was not invoked; the mock script may be insufficient")
	}
	// The note's own Role-instruction text (it opens "You have a Schedule
	// tool") distinguishes the Role layer from the inventory block (whose Spec
	// description opens "Manage scheduled tasks") — assert it VERBATIM.
	if !strings.Contains(captured.StablePrefix, schedulePostureNote) {
		t.Errorf("the SHARED engine's StablePrefix is missing the Schedule posture note — applySchedulePosture must run on the shared engine's deps in buildEngine (ADR 0070)\ngot StablePrefix (first 600):\n%s",
			firstN(captured.StablePrefix, 600))
	}

	// Honest-absence half: a Build whose store backs NO ScheduleStore (the
	// in-memory default) carries NO Schedule tool, so the shared engine must
	// NOT be told about one.
	var capturedNone prompt.Layered
	llmNone := mockllm.NewWith([]mockllm.Option{
		mockllm.WithRequestObserver(func(req port.LLMRequest) { capturedNone = req.System }),
	}, mockllm.TextTurn("ok"))
	wsNone := t.TempDir()
	builtNone, err := buildIsolated(t, ctx, Config{
		Workspace:    wsNone,
		Model:        "mock",
		MockProvider: llmNone, // no StoreDir → memstore → no ScheduleStore
	})
	if err != nil {
		t.Fatalf("Build (no store): %v", err)
	}
	defer builtNone.Close()
	sessNone, err := builtNone.Service.CreateSession(ctx, session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession (no store): %v", err)
	}
	runNone, err := builtNone.Service.StartRunContent(ctx, sessNone.ID, "hello", nil)
	if err != nil {
		t.Fatalf("StartRunContent (no store): %v", err)
	}
	for range runNone.Events() {
	}
	if strings.Contains(capturedNone.StablePrefix, schedulePostureNote) {
		t.Error("a store-less shared engine carries the Schedule instruction — the model must not be told about a tool it cannot call")
	}
}

// TestScheduleSharedCatalog_Scenario3_OriginAndDeliveryWired pins AC3.3 (ADR
// 0075, superseded for origin attribution by ADR 0209): the SHARED engine's
// run-context attribution + DeliveryQueue are live once the manager is bound, so a schedule created from a shared-engine session stamps
// its OriginSessionID, and the fire's result is delivered back into that chat.
// The whole arc rides the PRODUCTION seams — no test-fire shortcut: the
// origin run's create goes through the context-attributing manager wrapper the shared
// catalog registered; the fire goes through scheduler.FireNow → makeFireFunc
// (the same closure startScheduler installs) → fireClaimed → the
// DeliverFireResult callback deliverFireResult(svc, queue), which enqueues to
// the SAME FileDeliveryQueue the shared engine's Step 2a drain reads.
func TestScheduleSharedCatalog_Scenario3_OriginAndDeliveryWired(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	workspace := t.TempDir()
	storeDir := t.TempDir()

	// Turn 1: the model creates the schedule (the read-leaning default keeps
	// the plan-mode variant happy). Turn 2: the origin acknowledges the note
	// (unused in this pin — see the queue-degrade comment below). The fire's
	// own sched-- run consumes turn 3.
	llm := mockllm.New(
		mockllm.ToolCallTurn(session.NewToolCall("c1", agent.ScheduleToolName,
			[]byte(`{"verb":"create","name":"nightly","prompt":"check ci","cron":"@every 1h"}`))),
		mockllm.TextTurn("scheduled"),
		mockllm.TextTurn("fire output"),
	)
	built, err := buildIsolated(t, ctx, Config{
		Workspace:    workspace,
		Model:        "mock",
		StoreDir:     storeDir,
		MockProvider: llm,
		// No SchedulerEnabled: the scheduler's Start is deliberately skipped so
		// the test can late-bind the REAL fire path (SetFire + SetDeliverFireResult,
		// mirroring startScheduler verbatim) over the same svc + durable queue.
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer built.Close()

	sess, err := built.Service.CreateSession(ctx, session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	if _, err := built.Service.ReattachPlacement(ctx, sess.EnvironmentRef); err != nil {
		t.Fatalf("created session exact placement cannot reattach before schedule creation: ref=%+v err=%v", sess.EnvironmentRef, err)
	}

	// The origin run creates the schedule. startRun places THIS session id on
	// the run context, so the create stamps OriginSessionID.
	run, err := built.Service.StartRunContent(ctx, sess.ID, "schedule a nightly ci check", nil)
	if err != nil {
		t.Fatalf("StartRunContent (create): %v", err)
	}
	for ev := range run.Events() {
		if ev.Type == session.EvToolResult && ev.ToolResult != nil && ev.ToolResult.IsError {
			t.Fatalf("the create failed (the shared engine's Schedule tool must ride the manager): %q", ev.ToolResult.Content)
		}
	}

	// ORIGIN half: the created schedule carries the shared-engine session's id
	// that the Schedule tool read off the run context.
	created, err := built.Service.GetSchedule(ctx, "nightly")
	if err != nil {
		t.Fatalf("GetSchedule: %v", err)
	}
	if created.Spec.OriginSessionID != sess.ID {
		t.Fatalf("OriginSessionID = %q, want the shared-engine session id %q — run-context attribution did not stamp the create", created.Spec.OriginSessionID, sess.ID)
	}

	// Late-bind the REAL production fire path, mirroring startScheduler
	// (SetFire + SetDeliverFireResult) over the SAME svc and the SAME durable
	// FileDeliveryQueue the shared engine's drain reads (built from the store
	// dir by buildDeliveryQueue — reconstructed here over the SAME dir, the
	// restart-equivalent handle the durable sidecars make interchangeable).
	schedStore, err := jsonlstore.New(storeDir)
	if err != nil {
		t.Fatalf("jsonlstore.New (schedule store): %v", err)
	}
	queue, err := NewFileDeliveryQueue(storeDir, WithDeliveryDiagnostics(port.NopDiagnostics{}))
	if err != nil {
		t.Fatalf("NewFileDeliveryQueue: %v", err)
	}
	sched := scheduler.New(scheduler.Config{
		Store:        schedStore.ScheduleStore(),
		Clock:        testWallClock{},
		Diagnostics:  port.NopDiagnostics{},
		TickInterval: time.Hour, // the manual FireNow path drives the test
	})
	sched.SetFire(makeFireFunc(built.Service, schedStore.ScheduleStore(), defaultFireTimeout, nil))
	sched.SetDeliverFireResult(deliverFireResult(built.Service, queue))
	defer func() { _ = sched.Stop() }()

	// Fire the schedule. fireClaimed drives the fire to terminal, RecordFires
	// it, then invokes DeliverFireResult. The origin is IDLE here (its run
	// ended), so deliverFireResult would normally drive a delivery run — but
	// the Build carried no Sink/Diagnostics relay, and the drive path requires
	// the origin's run-entry funnel; the DURABLE half of delivery (the
	// enqueue into the FileDeliveryQueue the shared engine's Step 2a drain
	// reads) is the shared-engine wiring this AC pins. Assert the fire ran
	// clean and the note landed in the queue.
	fire, err := sched.FireNow(ctx, "nightly", time.Now())
	if err != nil {
		t.Fatalf("FireNow: %v", err)
	}
	if fire.Stop == session.StopError {
		t.Fatalf("the fire errored: stop=%q err=%q", fire.Stop, fire.Err)
	}

	// DELIVERY half: the fire's result was enqueued for the origin — the same
	// durable queue the shared engine's Deps.DeliveryQueue Step 2a drain reads.
	pending, err := queue.Pending(ctx, sess.ID)
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	found := false
	for _, n := range pending {
		if strings.Contains(n.Text, "nightly") && strings.Contains(n.Text, "<<<UNTRUSTED") {
			found = true
		}
	}
	if !found {
		t.Fatalf("no fenced fire-result note pending for the origin %q — deliverFireResult did not enqueue into the durable queue the shared engine drains (ADR 0075)", sess.ID)
	}

	// The drain is the shared engine's OWN seam: a second prompt on the origin
	// records the pending note into the chat (Step 2a, BEFORE BeginTurn) via the
	// shared engine's Deps.DeliveryQueue — the delivery lands in the chat.
	run2, err := built.Service.StartRunContent(ctx, sess.ID, "continue", nil)
	if err != nil {
		t.Fatalf("StartRunContent (drain): %v", err)
	}
	for range run2.Events() {
	}
	origin, err := built.Service.GetSession(ctx, sess.ID)
	if err != nil {
		t.Fatalf("GetSession (origin): %v", err)
	}
	drained := false
	for _, m := range origin.Conversation.Messages {
		if m.Role == session.RoleUser && strings.Contains(m.Text, "nightly") && strings.Contains(m.Text, "<<<UNTRUSTED") {
			drained = true
		}
	}
	if !drained {
		t.Fatalf("the fire-result note was not delivered into the origin chat by the shared engine's Step 2a drain — its Deps.DeliveryQueue is not live (ADR 0075)")
	}
	// Exactly-once: the note was marked delivered, so the queue is empty now.
	pendingAfter, err := queue.Pending(ctx, sess.ID)
	if err != nil {
		t.Fatalf("Pending (after drain): %v", err)
	}
	if len(pendingAfter) != 0 {
		t.Errorf("pending = %d after the drain, want 0 (the exactly-once ledger marked the note delivered)", len(pendingAfter))
	}
}

// TestScheduleSharedCatalog_Scenario3_RehydratedSessionKeepsTool pins AC3.4: a
// default-profile session persisted, then reloaded via needsRehydration (the
// restart path — a second Build over the SAME store), still resolves the
// Schedule tool. The rehydration no longer lands the session on a schedule-less
// shared engine: the second Build's eager scheduleManagerFactory bind carries
// the tool, so the restored session (restored onto the shared engine) dispatches
// it — proven by the tool EXECUTING (no `unknown tool` error) through the
// post-restart run-entry funnel.
func TestScheduleSharedCatalog_Scenario3_RehydratedSessionKeepsTool(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	workspace := t.TempDir()
	storeDir := t.TempDir()

	mkConfig := func() Config {
		return Config{
			Workspace: workspace,
			Model:     "mock",
			StoreDir:  storeDir,
			MockProvider: mockllm.New(
				mockllm.ToolCallTurn(session.NewToolCall("c1", agent.ScheduleQueryToolName, []byte(`{"verb":"list"}`))),
				mockllm.TextTurn("done"),
			),
		}
	}

	// First process: create + persist a default-profile session.
	built1, err := buildIsolated(t, ctx, mkConfig())
	if err != nil {
		t.Fatalf("Build 1: %v", err)
	}
	sess, err := built1.Service.CreateSession(ctx, session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	built1.Close()

	// Second process (restart): the session reloads from the SAME store. A
	// default-profile session does NOT rehydrate to a per-session engine
	// (needsRehydration is false for it) — it is restored onto the SHARED
	// engine. That engine must carry the Schedule tool.
	built2, err := buildIsolated(t, ctx, mkConfig())
	if err != nil {
		t.Fatalf("Build 2: %v", err)
	}
	defer built2.Close()
	run, err := built2.Service.StartRunContent(ctx, sess.ID, "list schedules", nil)
	if err != nil {
		t.Fatalf("StartRunContent after restart: %v", err)
	}
	var sawResult bool
	for ev := range run.Events() {
		if ev.Type != session.EvToolResult || ev.ToolResult == nil {
			continue
		}
		sawResult = true
		if strings.Contains(ev.ToolResult.Content, "unknown tool") {
			t.Fatalf("the post-restart SHARED engine rejected the ScheduleQuery tool as unknown — the restart restore landed the session on a schedule-less shared engine (eager scheduleManagerFactory bind, ADR 0076); result: %q", ev.ToolResult.Content)
		}
		if ev.ToolResult.IsError {
			t.Fatalf("the post-restart ScheduleQuery errored: %q", ev.ToolResult.Content)
		}
	}
	if !sawResult {
		t.Fatal("no EvToolResult from the post-restart run — the scripted ScheduleQuery call did not dispatch")
	}
}
