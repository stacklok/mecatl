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
		verdicts := run.asks.registerPending(session.PendingAsk{AskID: "root-ordinary"})

		const callers = 32
		assertOneResolution(t, resolveConcurrently(run, "root-ordinary", session.VerdictAllowAlways, callers), callers)
		if got := requireApproval(t, verdicts); got.verdict != session.VerdictAllowAlways {
			t.Fatalf("delivered verdict = %v, want %v", got.verdict, session.VerdictAllowAlways)
		}
	})

	t.Run("root plan ask stays pending for Approve", func(t *testing.T) {
		run := &Run{asks: newAskRegistry()}
		verdicts := run.asks.registerPending(session.PendingAsk{AskID: "root-plan", PlanOriginated: true})

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
		verdicts := child.asks.registerPending(session.PendingAsk{AskID: "child-ordinary"})
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
		verdicts := child.asks.registerPending(session.PendingAsk{AskID: "child-plan", PlanOriginated: true})
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

		verdicts := run.asks.registerPending(session.PendingAsk{AskID: "late"})
		if got := run.ResolveOrdinaryAsk("late", session.VerdictAllowOnce); got != AskResolutionResolved {
			t.Fatalf("later registered ask outcome = %v, want %v", got, AskResolutionResolved)
		}
		if got := requireApproval(t, verdicts); got.verdict != session.VerdictAllowOnce {
			t.Fatalf("later registered ask verdict = %v, want %v", got.verdict, session.VerdictAllowOnce)
		}
	})
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
