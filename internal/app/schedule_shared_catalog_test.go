package app

import (
	"context"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
)

// TestScheduleSharedCatalog_Scenario2_SharedCatalogHasScheduleTools pins AC2.1
// (schedule-shared-catalog plan, Scenario 2): the build-time SHARED catalog of a
// store-backed Build carries BOTH schedule tools — Schedule (mutating) and
// ScheduleQuery (read-only) — exactly like the six memory tools (ADR 0073
// decision 1: "registered in the catalog for every session that has a backing
// ScheduleStore"). ADR 0076 made the schedule manager store-shaped (resolvable
// BEFORE buildEngine), so assets.scheduleManagerFactory is bound EAGERLY and
// registerScheduleTool fires on the build-time pass — the late bind (a factory
// set after server.NewService) left the default-profile shared-engine fast path
// schedule-less. StoreDir (a jsonlstore) backs a ScheduleStore; the default
// mecatui session rides this shared engine.
//
// The oracle is the session run's OWN EvToolResult: a tool the catalog
// RESOLVED executes and returns its own result, while an unknown tool returns
// the loop's `unknown tool %q` error — so dispatching a scripted call to each
// schedule verb through a default-profile, zero-selector session (the
// shared-engine fast path — what a plain mecatui launch creates) and asserting
// NO unknown-tool error pins the build-time registration end-to-end.
// (EvToolCall is NOT the oracle: the unknown-tool path opens a card too, by
// design.) The store-side Scheduling capability bit (scheduleStore() != nil)
// is also green even when the catalog lacks the tools, so it is NOT the
// oracle either. The provider is the internal mock-script seam — offline, no
// UseMock canned turn.
func TestScheduleSharedCatalog_Scenario2_SharedCatalogHasScheduleTools(t *testing.T) {
	t.Parallel()
	llm := mockllm.New(
		// Turn 1: the mutating tool creates a schedule (jsonlstore-backed, so
		// the create-seam saves it); turn 2 lists it via the read-only tool.
		mockllm.ToolCallTurn(session.NewToolCall("c1", agent.ScheduleToolName,
			[]byte(`{"verb":"create","name":"shared-cat","prompt":"check ci","cron":"0 9 * * *"}`))),
		mockllm.ToolCallTurn(session.NewToolCall("c2", agent.ScheduleQueryToolName, []byte(`{"verb":"list"}`))),
		mockllm.TextTurn("done"),
	)
	workspace := t.TempDir()
	built, err := Build(context.Background(), Config{
		Workspace:    workspace,
		Model:        "mock",
		StoreDir:     t.TempDir(), // jsonlstore — backs a ScheduleStore
		MockProvider: llm,
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer built.Close()

	ctx := context.Background()
	sess, err := built.Service.CreateSession(ctx, "", session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// One prompt drives BOTH scripted turns through the SAME run-entry funnel
	// every mecatui prompt takes (the session's engine is the shared one).
	run, err := built.Service.StartRunContent(ctx, sess.ID, "schedule the check, then list schedules", nil)
	if err != nil {
		t.Fatalf("StartRunContent: %v", err)
	}
	callNames := map[session.ToolCallID]string{}
	executed := map[string]bool{}
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
				t.Errorf("the shared engine's catalog rejected a scripted %q call as unknown — the build-time assembly must register it (eager scheduleManagerFactory bind, ADR 0076); result: %q", name, ev.ToolResult.Content)
				continue
			}
			executed[name] = true
		}
	}
	for _, name := range []string{agent.ScheduleToolName, agent.ScheduleQueryToolName} {
		if !executed[name] {
			t.Errorf("no executed EvToolResult for %q on a shared-engine session — the build-time assembly must register it (eager scheduleManagerFactory bind, ADR 0076)", name)
		}
	}
}
