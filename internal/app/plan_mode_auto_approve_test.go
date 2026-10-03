package app

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

// planAutoApproveConfig returns a Config with the given plan-mode-auto-approve
// settings, suitable for driving a plan-mode run through Build. The mock LLM is
// scripted via the providerConstructor seam so the model emits a PresentPlan call.
func planAutoApproveConfig(t *testing.T, llm *mockllm.Provider, interactive, autoApprove bool) Config {
	t.Helper()
	return Config{
		Workspace:           t.TempDir(),
		NoSoul:              true,
		Interactive:         interactive,
		PlanModeAutoApprove: autoApprove,
		envDetector:         fakeEnv(map[string]string{"OPENAI_API_KEY": "sk-openai"}),
		liveModelHTTPClient: offlineHTTPClient(),
		providerConstructor: func(_ Config, _, _, _ string) port.LLMProvider {
			return llm
		},
	}
}

// drainAll drains a run's event channel until it closes, returning all events.
// It simulates the relay by calling MaybeAutoApprovePlan on each EvPermissionAsk
// (the relay's hook for the auto-approve observer).
func drainAll(t *testing.T, svc interface {
	MaybeAutoApprovePlan(context.Context, session.SessionID, session.Event)
}, id session.SessionID, r interface{ Events() <-chan session.Event }) []session.Event {
	t.Helper()
	var evs []session.Event
	for ev := range r.Events() {
		evs = append(evs, ev)
		if ev.Type == session.EvPermissionAsk {
			svc.MaybeAutoApprovePlan(context.Background(), id, ev)
		}
	}
	return evs
}

// pcall builds a session.ToolCall for the mock.
func pcall(id, tool, args string) session.ToolCall {
	return session.NewToolCall(session.ToolCallID(id), tool, json.RawMessage(args))
}

// TestPlanModeAutoApproveOffByDefault proves that when PlanModeAutoApprove is NOT
// set (the default), a headless plan-mode run does NOT auto-approve — the plan ask
// is auto-denied by the engine (the existing headless degrade), and the run does
// NOT terminate with StopPlanApproved.
func TestPlanModeAutoApproveOffByDefault(t *testing.T) {
	llm := mockllm.New(
		mockllm.ToolCallTurn(pcall("c1", "PresentPlan", `{"note":"step 1"}`)),
		mockllm.TextTurn("done"),
	)
	cfg := planAutoApproveConfig(t, llm, false, false) // headless, flag OFF
	built, err := buildIsolated(t, context.Background(), cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer built.Close()

	sess, err := built.Service.CreateSession(context.Background(), session.ModePlan, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	r, err := built.Service.StartRunContent(context.Background(), sess.ID, "plan a task", nil)
	if err != nil {
		t.Fatalf("StartRunContent: %v", err)
	}
	evs := drainAll(t, built.Service, sess.ID, r)
	// The run must NOT end with StopPlanApproved — the plan was auto-denied.
	for _, ev := range evs {
		if ev.Type == session.EvResult && ev.Result != nil && ev.Result.Stop == session.StopPlanApproved {
			t.Fatal("PlanModeAutoApprove OFF must NOT terminate with StopPlanApproved (the plan ask is auto-denied)")
		}
	}
	// The session mode must NOT have flipped.
	reloaded, err := built.Service.GetSession(context.Background(), sess.ID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if reloaded.Mode != session.ModePlan {
		t.Fatalf("mode after run = %q, want %q (no auto-approve flip)", reloaded.Mode, session.ModePlan)
	}
}

// TestPlanModeAutoApproveFiresOnParkedPlanAsk proves that when enabled + headless,
// a parked plan-approval ask IS auto-approved: the run terminates StopPlanApproved,
// the mode flips to ModeDefault, and the LOUD diagnostic is emitted.
func TestPlanModeAutoApproveFiresOnParkedPlanAsk(t *testing.T) {
	llm := mockllm.New(
		mockllm.ToolCallTurn(pcall("c1", "PresentPlan", `{"note":"step 1"}`)),
		mockllm.TextTurn("plan executed"),
	)
	diag := slogdiagBuffer(t)
	cfg := planAutoApproveConfig(t, llm, false, true) // headless, flag ON
	cfg.Diagnostics = diag.diag
	built, err := buildIsolated(t, context.Background(), cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer built.Close()

	sess, err := built.Service.CreateSession(context.Background(), session.ModePlan, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	r, err := built.Service.StartRunContent(context.Background(), sess.ID, "plan a task", nil)
	if err != nil {
		t.Fatalf("StartRunContent: %v", err)
	}
	evs := drainAll(t, built.Service, sess.ID, r)

	// The run must terminate with StopPlanApproved (the auto-approve fired).
	var sawPlanApproved bool
	for _, ev := range evs {
		if ev.Type == session.EvResult && ev.Result != nil && ev.Result.Stop == session.StopPlanApproved {
			sawPlanApproved = true
		}
	}
	if !sawPlanApproved {
		t.Fatal("PlanModeAutoApprove ON must terminate with StopPlanApproved (the auto-approve fired)")
	}

	// The LOUD diagnostic must be emitted.
	if line := diag.lineContaining("plan_mode_auto_approve: auto-approving plan (NO HUMAN REVIEW)"); line == "" {
		t.Fatalf("expected the LOUD auto-approve diagnostic; log:\n%s", diag.String())
	}

	// The session mode must have flipped to ModeDefault.
	reloaded, err := built.Service.GetSession(context.Background(), sess.ID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if reloaded.Mode != session.ModeDefault {
		t.Fatalf("mode after auto-approve = %q, want %q (flipped to default)", reloaded.Mode, session.ModeDefault)
	}

	// FIX 1 (architecture judgement-call): the LIVE auto-approve path must ALSO
	// drive a continuation execution run (headless has no operator to re-prompt).
	// The original run's stream carries StopPlanApproved; the continuation run's
	// StopEndTurn is consumed by the background drain goroutine, so we poll the
	// session until the continuation reaches its terminal (StopEndTurn, the
	// execution turn "plan executed") — not just StopPlanApproved + mode flip.
	deadline := time.Now().Add(10 * time.Second)
	for {
		reloaded, err = built.Service.GetSession(context.Background(), sess.ID)
		if err != nil {
			t.Fatalf("GetSession: %v", err)
		}
		if reason, ok := reloaded.RecordedStopReason(); ok && reason == session.StopEndTurn {
			break // the continuation execution run completed.
		}
		if time.Now().After(deadline) {
			reason, _ := reloaded.RecordedStopReason()
			t.Fatalf("auto-approve continuation did not reach StopEndTurn; final stop=%q state=%q", reason, reloaded.State)
		}
		time.Sleep(10 * time.Millisecond)
	}

	// The continuation run's first user message must be PlanApprovedProceedText,
	// and the execution turn ("plan executed") must be in the conversation —
	// proof the continuation actually ran the model, not just mode-flipped.
	foundProceed := false
	foundExecution := false
	for _, msg := range reloaded.Conversation.Messages {
		if msg.Role == session.RoleUser && strings.Contains(msg.Text, agent.PlanApprovedProceedText) {
			foundProceed = true
		}
		if msg.Role == session.RoleAssistant && strings.Contains(msg.Text, "plan executed") {
			foundExecution = true
		}
	}
	if !foundProceed {
		t.Fatal("auto-approve continuation run's user message must contain PlanApprovedProceedText")
	}
	if !foundExecution {
		t.Fatal("auto-approve continuation run must drive the execution turn (the model's 'plan executed' text is missing)")
	}
}

// TestPlanModeAutoApproveDoesNotFireInteractive proves that when Interactive=true,
// the auto-approve does NOT fire even when enabled — the plan ask surfaces to the
// human instead.
func TestPlanModeAutoApproveDoesNotFireInteractive(t *testing.T) {
	llm := mockllm.New(
		mockllm.ToolCallTurn(pcall("c1", "PresentPlan", `{"note":"step 1"}`)),
		mockllm.TextTurn("done"),
	)
	diag := slogdiagBuffer(t)
	cfg := planAutoApproveConfig(t, llm, true, true) // interactive, flag ON
	cfg.Diagnostics = diag.diag
	built, err := buildIsolated(t, context.Background(), cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer built.Close()

	sess, err := built.Service.CreateSession(context.Background(), session.ModePlan, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	r, err := built.Service.StartRunContent(context.Background(), sess.ID, "plan a task", nil)
	if err != nil {
		t.Fatalf("StartRunContent: %v", err)
	}
	// Drain until we see the plan ask, then cancel (the human would approve, but
	// we're testing that the auto-approve does NOT pre-empt).
	var sawPlanAsk bool
	for ev := range r.Events() {
		if ev.Type == session.EvPermissionAsk && ev.Ask != nil && ev.Ask.Origin == session.ApprovalOriginPlan {
			sawPlanAsk = true
			built.Service.MaybeAutoApprovePlan(context.Background(), sess.ID, ev)
			r.Cancel()
		}
		if ev.Type == session.EvResult {
			break
		}
	}
	if !sawPlanAsk {
		t.Fatal("an interactive run must surface the plan ask (EvPermissionAsk with AskOriginPlan)")
	}
	// The auto-approve diagnostic must NOT appear.
	if line := diag.lineContaining("plan_mode_auto_approve: auto-approving plan"); line != "" {
		t.Fatal("PlanModeAutoApprove must NOT fire when interactive (a human can approve)")
	}
}

// TestPlanModeAutoApproveDoesNotFireInDefaultMode proves that a non-plan session
// does NOT trigger the auto-approve even when enabled.
func TestPlanModeAutoApproveDoesNotFireInDefaultMode(t *testing.T) {
	llm := mockllm.New(
		mockllm.TextTurn("no plan here"),
	)
	diag := slogdiagBuffer(t)
	cfg := planAutoApproveConfig(t, llm, false, true) // headless, flag ON
	cfg.Diagnostics = diag.diag
	built, err := buildIsolated(t, context.Background(), cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer built.Close()

	sess, err := built.Service.CreateSession(context.Background(), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	r, err := built.Service.StartRunContent(context.Background(), sess.ID, "do something", nil)
	if err != nil {
		t.Fatalf("StartRunContent: %v", err)
	}
	_ = drainAll(t, built.Service, sess.ID, r)
	// The auto-approve diagnostic must NOT appear (no plan ask was surfaced).
	if line := diag.lineContaining("plan_mode_auto_approve: auto-approving plan"); line != "" {
		t.Fatal("PlanModeAutoApprove must NOT fire in a non-plan session (no plan ask surfaced)")
	}
}

// TestPlanModeAutoApproveDoesNotFireForNonPlanAsk proves that a policy/hook
// permission ask (NOT a plan-approval ask) does NOT trigger the auto-approve.
func TestPlanModeAutoApproveDoesNotFireForNonPlanAsk(t *testing.T) {
	// The model calls Write in default mode — a policy ask, not a plan ask.
	llm := mockllm.New(
		mockllm.ToolCallTurn(pcall("c1", "Write", `{"path":"note.txt","content":"hi"}`)),
		mockllm.TextTurn("done"),
	)
	diag := slogdiagBuffer(t)
	cfg := planAutoApproveConfig(t, llm, false, true) // headless, flag ON
	cfg.Diagnostics = diag.diag
	built, err := buildIsolated(t, context.Background(), cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer built.Close()

	sess, err := built.Service.CreateSession(context.Background(), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	r, err := built.Service.StartRunContent(context.Background(), sess.ID, "write a note", nil)
	if err != nil {
		t.Fatalf("StartRunContent: %v", err)
	}
	// Drain until the policy ask surfaces, then cancel (the auto-approve must NOT
	// fire for a non-plan ask).
	for ev := range r.Events() {
		if ev.Type == session.EvPermissionAsk && ev.Ask != nil && ev.Ask.Origin != session.ApprovalOriginPlan {
			built.Service.MaybeAutoApprovePlan(context.Background(), sess.ID, ev)
			r.Cancel()
		}
		if ev.Type == session.EvResult {
			break
		}
	}
	// The auto-approve diagnostic must NOT appear (the ask was not plan-originated).
	if line := diag.lineContaining("plan_mode_auto_approve: auto-approving plan"); line != "" {
		t.Fatal("PlanModeAutoApprove must NOT fire for a non-plan (policy) ask")
	}
}

// TestPlanModeAutoApproveBuildNarration proves that the build-once narration
// "plan_mode_auto_approve: ON (NO HUMAN REVIEW)" is emitted when the flag is ON.
func TestPlanModeAutoApproveBuildNarration(t *testing.T) {
	diag := slogdiagBuffer(t)
	cfg := planAutoApproveConfig(t, mockllm.New(mockllm.TextTurn("ok")), false, true)
	cfg.Diagnostics = diag.diag
	built, err := buildIsolated(t, context.Background(), cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer built.Close()

	if line := diag.lineContaining("plan_mode_auto_approve: ON (NO HUMAN REVIEW)"); line == "" {
		t.Fatalf("expected the build-once plan_mode_auto_approve narration; log:\n%s", diag.String())
	}
}
