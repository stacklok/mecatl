package agent

import (
	"sync"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/session"
)

// TestSDKRunControls_Scenario3_AtomicOrdinaryAskResolution pins the engine-side
// linearization point used by prompt-free run controls. Ordinary root and
// surfaced-child asks resolve exactly once, while plan asks stay pending for the
// dedicated plan-resolution choreography. Approve remains the compatibility
// path that resolves asks from every origin.
func TestSDKRunControls_Scenario3_AtomicOrdinaryAskResolution(t *testing.T) {
	t.Run("root ordinary resolution is atomic", func(t *testing.T) {
		run := &Run{asks: newAskRegistry()}
		verdicts := run.asks.registerAsk(session.PendingAsk{AskID: "root-ordinary", Origin: session.ApprovalOriginPermission})

		const callers = 32
		assertOneResolution(t, resolveConcurrently(run, "root-ordinary", session.VerdictAllowAlways, callers), callers)
		if got := requireApproval(t, verdicts); got.verdict != session.VerdictAllowAlways {
			t.Fatalf("delivered verdict = %v, want %v", got.verdict, session.VerdictAllowAlways)
		}
	})

	t.Run("root plan ask stays pending for Approve", func(t *testing.T) {
		run := &Run{asks: newAskRegistry()}
		verdicts := run.asks.registerAsk(session.PendingAsk{AskID: "root-plan", Origin: session.ApprovalOriginPlan})

		for i := 0; i < 2; i++ {
			if got := run.ResolveOrdinaryAsk("root-plan", session.VerdictAllowOnce); got != AskResolutionPlanOriginated {
				t.Fatalf("attempt %d outcome = %v, want %v", i+1, got, AskResolutionPlanOriginated)
			}
			select {
			case got := <-verdicts:
				t.Fatalf("ordinary resolver consumed plan ask with verdict %+v", got)
			default:
			}
		}

		run.Approve("root-plan", session.VerdictDeny)
		if got := requireApproval(t, verdicts); got.verdict != session.VerdictDeny {
			t.Fatalf("Approve delivered verdict = %v, want %v", got.verdict, session.VerdictDeny)
		}
	})

	t.Run("surfaced child ordinary ask resolves exactly once", func(t *testing.T) {
		parent := &Run{asks: newAskRegistry(), childAsks: newChildAskRouter()}
		child := &Run{asks: newAskRegistry()}
		verdicts := child.asks.registerAsk(session.PendingAsk{AskID: "child-ordinary", Origin: session.ApprovalOriginPermission})
		parent.childAsks.registerChild("child-ordinary", child)

		const callers = 32
		assertOneResolution(t, resolveConcurrently(parent, "child-ordinary", session.VerdictAllowOnce, callers), callers)
		if got := requireApproval(t, verdicts); got.verdict != session.VerdictAllowOnce {
			t.Fatalf("child verdict = %v, want %v", got.verdict, session.VerdictAllowOnce)
		}
	})

	t.Run("surfaced child plan ask and route stay pending for Approve", func(t *testing.T) {
		parent := &Run{asks: newAskRegistry(), childAsks: newChildAskRouter()}
		child := &Run{asks: newAskRegistry()}
		verdicts := child.asks.registerAsk(session.PendingAsk{AskID: "child-plan", Origin: session.ApprovalOriginPlan})
		parent.childAsks.registerChild("child-plan", child)

		for i := 0; i < 2; i++ {
			if got := parent.ResolveOrdinaryAsk("child-plan", session.VerdictAllowOnce); got != AskResolutionPlanOriginated {
				t.Fatalf("attempt %d outcome = %v, want %v", i+1, got, AskResolutionPlanOriginated)
			}
			select {
			case got := <-verdicts:
				t.Fatalf("ordinary resolver consumed surfaced plan ask with verdict %+v", got)
			default:
			}
		}

		parent.Approve("child-plan", session.VerdictAllowAlways)
		if got := requireApproval(t, verdicts); got.verdict != session.VerdictAllowAlways {
			t.Fatalf("Approve delivered child verdict = %v, want %v", got.verdict, session.VerdictAllowAlways)
		}
	})

	t.Run("not-pending does not reserve a future ask", func(t *testing.T) {
		run := &Run{asks: newAskRegistry()}
		if got := run.ResolveOrdinaryAsk("late", session.VerdictDeny); got != AskResolutionNotPending {
			t.Fatalf("unknown outcome = %v, want %v", got, AskResolutionNotPending)
		}

		verdicts := run.asks.registerAsk(session.PendingAsk{AskID: "late", Origin: session.ApprovalOriginPermission})
		if got := run.ResolveOrdinaryAsk("late", session.VerdictAllowOnce); got != AskResolutionResolved {
			t.Fatalf("later registered ask outcome = %v, want %v", got, AskResolutionResolved)
		}
		if got := requireApproval(t, verdicts); got.verdict != session.VerdictAllowOnce {
			t.Fatalf("later registered ask verdict = %v, want %v", got.verdict, session.VerdictAllowOnce)
		}
	})
}

// TestSurfacedChildAcceptedVerdictSurvivesCancelledAwait deterministically
// models cancellation after the verdict entered the child's buffered channel,
// before the child could emit EvApproval. The terminal drain must still publish
// exactly one parent approval and must not retract an answered ask.
func TestSurfacedChildAcceptedVerdictSurvivesCancelledAwait(t *testing.T) {
	for _, tc := range []struct {
		name      string
		verdict   session.ApprovalVerdict
		guardrail bool
	}{
		{name: "ordinary allow", verdict: session.VerdictAllowOnce},
		{name: "ordinary deny", verdict: session.VerdictDeny},
		{name: "scoped guardrail", verdict: session.VerdictAllowOnce, guardrail: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parent := &Run{childAsks: newChildAskRouter(), children: newChildRunRegistry()}
			child := &Run{asks: newAskRegistry()}
			ask := session.PendingAsk{AskID: "child-ask", Tool: "Shell", Call: "collision", Origin: session.ApprovalOriginPermission}
			if tc.guardrail {
				ask.Origin = session.ApprovalOriginHookGuardrail
				ask.Guardrail = &session.GuardrailPendingScope{ReviewID: "review", Kind: session.GuardrailApprovalAction}
			}
			verdicts := child.asks.registerAsk(ask)
			parent.childAsks.registerSurfaced(ask, child, 7)
			var emitted []session.Event
			parent.children.emit = func(ev session.Event) { emitted = append(emitted, ev) }
			posture := childPosture{caps: parentCaps{emitChildApprovals: func(child *Run) {
				parent.children.emitAccepted(func() []session.Event { return parent.childAsks.takeAccepted(child) }, parent.children.emit)
			}}}
			if tc.guardrail {
				wrong := ApprovalResolution{AskID: ask.AskID, ReviewID: "wrong", Kind: session.GuardrailApprovalAction, Verdict: tc.verdict}
				if err := parent.ResolveApproval(wrong); err == nil {
					t.Fatal("wrong guardrail review was accepted")
				}
				if got := parent.childAsks.takeAccepted(child); len(got) != 0 {
					t.Fatalf("rejected verdict recorded approval: %+v", got)
				}
				valid := ApprovalResolution{AskID: ask.AskID, ReviewID: "review", Kind: session.GuardrailApprovalAction, Verdict: tc.verdict}
				if err := parent.ResolveApproval(valid); err != nil {
					t.Fatal(err)
				}
			} else if got := parent.ResolveOrdinaryAsk(ask.AskID, tc.verdict); got != AskResolutionResolved {
				t.Fatalf("ordinary result = %v, want resolved", got)
			}
			// The buffered verdict was accepted, but the cancelled child never
			// consumes it or emits its own approval before its terminal result.
			if got := len(verdicts); got != 1 {
				t.Fatalf("buffered verdicts = %d, want one", got)
			}
			handleChildEvent(child, session.Event{Type: session.EvResult, Result: &session.ResultPayload{Stop: session.StopCancelled}}, posture)
			parent.children.retractAsksVia(parent.unregisterChildAsk, []string{ask.AskID})
			if len(emitted) != 1 || emitted[0].Type != session.EvApproval || emitted[0].Turn != 7 ||
				emitted[0].Approval == nil || emitted[0].Approval.AskID != ask.AskID ||
				emitted[0].Approval.Verdict != session.VerdictString(tc.verdict) || emitted[0].Approval.Call != "" {
				t.Fatalf("terminal child events = %+v, want one safe parent approval", emitted)
			}
		})
	}
}

func resolveConcurrently(run *Run, askID string, verdict session.ApprovalVerdict, callers int) <-chan AskResolution {
	start := make(chan struct{})
	outcomes := make(chan AskResolution, callers)
	var wg sync.WaitGroup
	wg.Add(callers)
	for range callers {
		go func() {
			defer wg.Done()
			<-start
			outcomes <- run.ResolveOrdinaryAsk(askID, verdict)
		}()
	}
	close(start)
	wg.Wait()
	close(outcomes)
	return outcomes
}

func assertOneResolution(t *testing.T, outcomes <-chan AskResolution, callers int) {
	t.Helper()
	resolved, notPending := 0, 0
	for outcome := range outcomes {
		switch outcome {
		case AskResolutionResolved:
			resolved++
		case AskResolutionNotPending:
			notPending++
		default:
			t.Fatalf("ordinary resolution returned unexpected outcome %v", outcome)
		}
	}
	if resolved != 1 || notPending != callers-1 {
		t.Fatalf("outcomes resolved=%d not-pending=%d, want 1 and %d", resolved, notPending, callers-1)
	}
}

func requireApproval(t *testing.T, verdicts <-chan approval) approval {
	t.Helper()
	select {
	case got := <-verdicts:
		return got
	case <-time.After(time.Second):
		t.Fatal("resolved outcome did not deliver a verdict")
		return approval{}
	}
}
