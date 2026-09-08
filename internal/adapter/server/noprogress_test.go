package server_test

import (
	"context"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/session"
)

// TestServiceNoProgressSurfacesAndReopens is the SERVICE-LEVEL e2e for the
// no-progress handler — the layer (server.Service / StartRunContent →
// loadAndReopen) where the prior silent-stall bugs actually lived, beyond the bare
// Engine. It drives a no-progress run through the real service and asserts:
//
//   - EvNoProgress events cross the service event stream (the harness made the stall
//     visible, not silent);
//   - the terminal result carries StopNoProgress on the wire;
//   - the completed session stays loadAndReopen-recoverable — a SUBSEQUENT prompt on
//     the SAME session succeeds and runs to a real answer (StopNoProgress is a clean
//     completed terminal, not a brick).
func TestServiceNoProgressSurfacesAndReopens(t *testing.T) {
	// The default engine nudge budget is 2 (applied in NewEngine), so 3 empty turns
	// exhaust it → StopNoProgress; then a TextTurn for the reopened follow-up run.
	llm := mockllm.New(
		mockllm.EmptyTurn(),
		mockllm.EmptyTurn(),
		mockllm.EmptyTurn(),
		mockllm.TextTurn("answer after reopen"),
	)
	svc := newService(t, llm, allowRules())

	// MaxTurns disabled so the nudge BUDGET (not the turn cap) is what bounds the run.
	sess, err := svc.CreateSession(context.Background(), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// First run: no-progress to exhaustion.
	run, err := svc.StartRunContent(context.Background(), sess.ID, "do the task", nil)
	if err != nil {
		t.Fatalf("StartRunContent #1: %v", err)
	}
	var sawNoProgress bool
	var stop session.StopReason
	for ev := range run.Events() {
		if ev.Type == session.EvNoProgress {
			sawNoProgress = true
		}
		if ev.Type == session.EvResult && ev.Result != nil {
			stop = ev.Result.Stop
		}
	}
	svc.Persist(context.Background(), sess.ID)
	svc.FinishRun(sess.ID, run)

	if !sawNoProgress {
		t.Fatal("no EvNoProgress crossed the service event stream (the stall was silent)")
	}
	if stop != session.StopNoProgress {
		t.Fatalf("terminal stop = %q, want %q on the wire", stop, session.StopNoProgress)
	}
	if got, _ := svc.GetSession(context.Background(), sess.ID); got.State != session.StateCompleted {
		t.Fatalf("state after no-progress run = %q, want completed (reopen-recoverable)", got.State)
	}

	// Second run on the SAME session must succeed (loadAndReopen recovers the
	// StopNoProgress-completed session) and produce a real answer.
	run2, err := svc.StartRunContent(context.Background(), sess.ID, "try again", nil)
	if err != nil {
		t.Fatalf("StartRunContent #2 on a StopNoProgress-completed session: %v (must be reopen-recoverable)", err)
	}
	var finalText string
	for ev := range run2.Events() {
		if ev.Type == session.EvResult && ev.Result != nil {
			finalText = ev.Result.Text
		}
	}
	svc.Persist(context.Background(), sess.ID)
	svc.FinishRun(sess.ID, run2)

	if !strings.Contains(finalText, "answer after reopen") {
		t.Fatalf("reopened run final text = %q, want the real answer", finalText)
	}
}
