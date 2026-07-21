package app

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

// plan_approval_restart_e2e_test.go is the two-Build cross-process restart e2e
// for the plan-approval gate (issue #206, Wave 4). It mirrors
// approve_after_restart_test.go's two-Build shape but exercises the
// PlanOriginated ask + the atomic ApprovePlan continuation (mode flip +
// execution run) across a process restart:
//
//  1. built1 (Interactive) over a shared StoreDir; the model returns a
//     PresentPlan tool call under ModePlan. Start a run, range to
//     EvPermissionAsk (the plan-originated ask), capture the AskID, and model
//     the relay's Persist-on-ask (Service.Persist) so a durable StateAwaiting
//     snapshot is saved. DO NOT approve.
//  2. built1.Close() = process death: the parked run + askRegistry die; the
//     durable awaiting snapshot is the last write to the store.
//  3. built2 over the SAME store; built2.Service.ApprovePlan(sess, ModeDefault)
//     → LookupRun miss → resumeFromAwaiting → ResumeApproval → the parked plan
//     ask resolves, the run terminates StopPlanApproved, the mode flips to
//     ModeDefault, and the atomic continuation run drives the execution turn.
//  4. Relay the returned merged event stream: assert StopPlanApproved (resumed
//     run) precedes StopEndTurn (continuation run), the mode flipped to
//     ModeDefault, the PlanOriginated marker was honored cross-process, and
//     the continuation run's user message is agent.PlanApprovedProceedText.
//
// This is the TRUE two-Build durable-store gate (vs the engine-level
// presentplan_ask_test.go which uses in-process newEngine + sessnap).
func TestPlanApprovalRestartE2E(t *testing.T) {
	ctx := context.Background()
	storeDir := t.TempDir()
	memoryDir := t.TempDir()
	workspace := t.TempDir()

	baseCfg := func() Config {
		return Config{
			Workspace:           workspace,
			NoSoul:              true,
			StoreDir:            storeDir,
			MemoryDir:           memoryDir,
			Interactive:         true, // so PresentPlan SURFACES an ask and parks (vs headless degrade)
			envDetector:         fakeEnv(map[string]string{"OPENAI_API_KEY": "sk-openai"}),
			liveModelHTTPClient: offlineHTTPClient(),
		}
	}

	// built1: the model issues a single PresentPlan call under ModePlan. The run
	// parks awaiting the plan-originated ask.
	cfg1 := baseCfg()
	cfg1.providerConstructor = func(_ Config, _, _, _ string) port.LLMProvider {
		return mockllm.New(mockllm.ToolCallTurn(
			session.NewToolCall("c1", "PresentPlan", json.RawMessage(`{"note":"step 1"}`)),
		))
	}
	built1, err := Build(ctx, cfg1)
	if err != nil {
		t.Fatalf("Build #1: %v", err)
	}
	sess, err := built1.Service.CreateSession(ctx, workspace, session.ModePlan, session.Limits{})
	if err != nil {
		built1.Close()
		t.Fatalf("CreateSession: %v", err)
	}
	run1, err := built1.Service.StartRun(ctx, sess.ID, "plan a task")
	if err != nil {
		built1.Close()
		t.Fatalf("StartRun (pre-restart): %v", err)
	}
	var askID string
	for ev := range run1.Events() {
		if ev.Type == session.EvPermissionAsk && ev.Ask != nil && ev.Ask.Origin() == session.AskOriginPlan && askID == "" {
			askID = ev.Ask.AskID
			// The PlanOriginated marker must be set (cross-process load-bearing).
			if !ev.Ask.PlanOriginated {
				built1.Close()
				t.Fatal("the awaiting plan ask must be PlanOriginated")
			}
			// Model the relay's Persist-on-ask: a durable StateAwaiting snapshot.
			built1.Service.Persist(ctx, sess.ID)
			break // stop ranging; DO NOT approve. The parked run dies with built1.
		}
	}
	if askID == "" {
		built1.Close()
		t.Fatal("the pre-restart run never raised a plan-originated permission ask")
	}
	built1.Close() // process death: parked run + askRegistry die; awaiting snapshot persists.

	// built2: a brand-new Build over the SAME store. The model has the
	// continuation turn (the execution turn, reached after the plan approval).
	cfg2 := baseCfg()
	cfg2.providerConstructor = func(_ Config, _, _, _ string) port.LLMProvider {
		return mockllm.New(mockllm.TextTurn("executed after restart"))
	}
	built2, err := Build(ctx, cfg2)
	if err != nil {
		t.Fatalf("Build #2: %v", err)
	}
	defer built2.Close()

	// ApprovePlan resolves the parked plan-ask cross-process: LookupRun miss →
	// resumeFromAwaiting → ResumeApproval(AllowOnce) → StopPlanApproved + mode
	// flip + atomic continuation run.
	events, err := built2.Service.ApprovePlan(ctx, sess.ID, session.ModeDefault, "approved post-restart")
	if err != nil {
		t.Fatalf("ApprovePlan after restart: %v (want a rehydrated resume, not ErrNotAwaitingPlan)", err)
	}

	// Relay the MERGED event stream: the resumed run's terminal (StopPlanApproved)
	// followed by the continuation run's terminal (StopEndTurn).
	var evs []session.Event
	for ev := range events {
		evs = append(evs, ev)
	}

	// FIX 5 (ordering): StopPlanApproved MUST precede StopEndTurn in the merged
	// stream — the resumed run terminates BEFORE the continuation run starts.
	idxPlanApproved := indexOfStop(evs, session.StopPlanApproved)
	idxEndTurn := indexOfStop(evs, session.StopEndTurn)
	if idxPlanApproved < 0 {
		t.Fatal("merged stream must contain a StopPlanApproved result from the resumed plan-approval run")
	}
	if idxEndTurn < 0 {
		t.Fatal("merged stream must contain a StopEndTurn result from the continuation execution run")
	}
	if idxPlanApproved >= idxEndTurn {
		t.Fatalf("StopPlanApproved (idx %d) must PRECEDE StopEndTurn (idx %d) in the merged stream", idxPlanApproved, idxEndTurn)
	}

	// The session mode must have flipped to ModeDefault (PlanOriginated honored
	// cross-process — the serialized marker survived the restart).
	final, err := built2.Service.GetSession(ctx, sess.ID)
	if err != nil {
		t.Fatalf("GetSession after resume: %v", err)
	}
	if final.Mode != session.ModeDefault {
		t.Fatalf("mode after ApprovePlan(AllowOnce) post-restart = %q, want %q (flipped to default)", final.Mode, session.ModeDefault)
	}
	if final.State != session.StateCompleted {
		t.Fatalf("final state = %q, want completed", final.State)
	}

	// The continuation run's first user message must be PlanApprovedProceedText
	// (the atomic continuation), and the execution turn ("executed after
	// restart") must be in the conversation — proof the continuation actually
	// ran the model post-restart.
	foundProceed := false
	foundExecution := false
	for _, msg := range final.Conversation.Messages {
		if msg.Role == session.RoleUser && strings.Contains(msg.Text, agent.PlanApprovedProceedText) {
			foundProceed = true
		}
		if msg.Role == session.RoleAssistant && strings.Contains(msg.Text, "executed after restart") {
			foundExecution = true
		}
	}
	if !foundProceed {
		t.Fatal("continuation run's user message must contain PlanApprovedProceedText (atomic continuation post-restart)")
	}
	if !foundExecution {
		t.Fatal("continuation run must drive the execution turn post-restart (the model's text is missing)")
	}
}

// indexOfStop returns the index of the FIRST EvResult carrying the given stop
// reason in evs, or -1 if none. Used by the plan-approval e2e to assert the
// resumed run's StopPlanApproved precedes the continuation run's StopEndTurn
// in the merged event stream.
func indexOfStop(evs []session.Event, stop session.StopReason) int {
	for i, ev := range evs {
		if ev.Type == session.EvResult && ev.Result != nil && ev.Result.Stop == stop {
			return i
		}
	}
	return -1
}
