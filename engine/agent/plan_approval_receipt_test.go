package agent_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/session"
)

type receiptSpy struct {
	mu       sync.Mutex
	receipt  agent.PlanApprovalReceipt
	recorded int
	consumed int
}

func (s *receiptSpy) RecordPlanApproval(r agent.PlanApprovalReceipt) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.receipt = r
	s.recorded++
}
func (s *receiptSpy) ConsumePlanApproval(id session.SessionID) (agent.PlanApprovalReceipt, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.receipt.SessionID != id {
		return agent.PlanApprovalReceipt{}, false
	}
	r := s.receipt
	s.receipt = agent.PlanApprovalReceipt{}
	s.consumed++
	return r, true
}
func TestPlanApprovalReceiptCrossesTwoRunsOnceLive(t *testing.T) {
	store := &receiptSpy{}
	e := newEngine(agent.Deps{LLM: mockllm.New(mockllm.ToolCallTurn(toolCall("c1", "PresentPlan", `{"note":"x"}`))), Catalog: planCatalog(t), Interactive: true, PlanApprovals: store})
	sess := newPlanSession(t)
	run := e.Run(context.Background(), sess, agent.MemEnv("/ws"), agent.RunRequest{Text: "plan"})
	for ev := range run.Events() {
		if ev.Ask != nil {
			_ = run.Approve(ev.Ask.AskID, session.VerdictAllowOnce)
		}
	}
	if store.recorded != 1 || store.consumed != 0 {
		t.Fatalf("after approval recorded=%d consumed=%d", store.recorded, store.consumed)
	}
	if err := sess.Reopen(); err != nil {
		t.Fatal(err)
	}
	next := newEngine(agent.Deps{LLM: mockllm.New(mockllm.TextTurn("done")), Catalog: catalogWith(t), PlanApprovals: store})
	_ = drain(next.Run(context.Background(), sess, agent.MemEnv("/ws"), agent.RunRequest{Text: "execute"}))
	if store.recorded != 1 || store.consumed != 1 {
		t.Fatalf("after execution recorded=%d consumed=%d", store.recorded, store.consumed)
	}
}

func TestPlanApprovalReceiptRejectedPromptDoesNotConsume(t *testing.T) {
	store := &receiptSpy{receipt: agent.PlanApprovalReceipt{Ref: "r", SessionID: "s1", Call: "c", TargetMode: session.ModeDefault}}
	sess := session.New("s1", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(0, 0))
	hooks := newRecordingHooks(map[governance.HookPhase]string{governance.PhaseUserPromptSubmit: "rejected"})
	eng := newEngine(agent.Deps{LLM: mockllm.New(), Catalog: catalogWith(t), Hooks: hooks, PlanApprovals: store})
	_ = drain(eng.Run(context.Background(), sess, agent.MemEnv("/ws"), agent.RunRequest{Text: "rejected"}))
	if store.consumed != 0 {
		t.Fatalf("rejected prompt consumed receipt %d times", store.consumed)
	}
}

func TestPlanApprovalReceiptCrossesTwoRunsOnceAwaiting(t *testing.T) {
	store := &receiptSpy{}
	sess := newPlanSession(t)
	e := newEngine(agent.Deps{LLM: mockllm.New(mockllm.ToolCallTurn(toolCall("c1", "PresentPlan", `{"note":"x"}`))), Catalog: planCatalog(t), Interactive: true, PlanApprovals: store})
	run := e.Run(context.Background(), sess, agent.MemEnv("/ws"), agent.RunRequest{Text: "plan"})
	var askID string
	for ev := range run.Events() {
		if ev.Ask != nil {
			askID = ev.Ask.AskID
			run.Cancel()
		}
	}
	if askID == "" {
		t.Fatal("no plan ask")
	}
	// Restore the parked state to model process-loss awaiting re-entry.
	if sess.State != session.StateCancelled {
		t.Fatalf("cancelled state = %s", sess.State)
	}
	// The existing restart test proves snapshot restoration; use its restored shape
	// by creating another parked session and resolving through ResumeApproval.
	parked := newPlanSession(t)
	if err := parked.RecordUserPrompt("plan", nil); err != nil {
		t.Fatal(err)
	}
	if err := parked.BeginTurn(); err != nil {
		t.Fatal(err)
	}
	call := toolCall("c2", "PresentPlan", `{"note":"x"}`)
	if err := parked.RecordAssistant(session.Message{Role: session.RoleAssistant, ToolCalls: []session.ToolCall{call}}); err != nil {
		t.Fatal(err)
	}
	ask := session.PendingAsk{AskID: "s1:1:c2:resume", Tool: "PresentPlan", Call: call.ID, Origin: session.ApprovalOriginPlan}
	if err := parked.PauseForApproval(ask); err != nil {
		t.Fatal(err)
	}
	resume := newEngine(agent.Deps{LLM: mockllm.New(), Catalog: planCatalog(t), Interactive: true, PlanApprovals: store})
	_ = drain(resume.ResumeApproval(context.Background(), parked, agent.MemEnv("/ws"), ask.AskID, session.VerdictAllowOnce))
	if store.recorded != 1 {
		t.Fatalf("awaiting approval recorded=%d", store.recorded)
	}
	if err := parked.Reopen(); err != nil {
		t.Fatal(err)
	}
	next := newEngine(agent.Deps{LLM: mockllm.New(mockllm.TextTurn("done")), Catalog: catalogWith(t), PlanApprovals: store})
	_ = drain(next.Run(context.Background(), parked, agent.MemEnv("/ws"), agent.RunRequest{Text: "execute"}))
	if store.consumed != 1 {
		t.Fatalf("awaiting receipt consumed=%d", store.consumed)
	}
}
