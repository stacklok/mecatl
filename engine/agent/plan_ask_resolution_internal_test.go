package agent

import (
	"testing"

	"github.com/stacklok/mecatl/engine/session"
)

func TestADR_0362_RootPlanAskResolution(t *testing.T) {
	run := &Run{asks: newAskRegistry(), childAsks: newChildAskRouter()}
	plan := run.asks.registerAsk(session.PendingAsk{AskID: "plan", Origin: session.ApprovalOriginPlan})
	ordinary := run.asks.registerAsk(session.PendingAsk{AskID: "ordinary", Origin: session.ApprovalOriginPermission})
	child := &Run{asks: newAskRegistry()}
	childPlan := child.asks.registerAsk(session.PendingAsk{AskID: "child-plan", Origin: session.ApprovalOriginPlan})
	run.childAsks.registerChild("child-plan", child)
	guardrail := run.asks.registerAsk(session.PendingAsk{AskID: "guardrail", Origin: session.ApprovalOriginPlan, Guardrail: &session.GuardrailPendingScope{ReviewID: "review", Kind: session.GuardrailApprovalAction}})

	if got := run.ResolvePlanAsk("ordinary", session.VerdictAllowOnce); got != AskResolutionNotPlan {
		t.Fatalf("ordinary = %v, want not-plan", got)
	}
	if got := run.ResolvePlanAsk("child-plan", session.VerdictAllowOnce); got != AskResolutionNotPending {
		t.Fatalf("child plan = %v, want not-pending", got)
	}
	if got := run.ResolvePlanAsk("guardrail", session.VerdictAllowOnce); got != AskResolutionNotPlan {
		t.Fatalf("guardrail-scoped plan = %v, want not-plan", got)
	}
	select {
	case got := <-guardrail:
		t.Fatalf("guardrail-scoped ask consumed: %+v", got)
	default:
	}
	if got := run.ResolvePlanAsk("plan", session.VerdictAllowAlways); got != AskResolutionResolved {
		t.Fatalf("plan = %v, want resolved", got)
	}
	if got := run.ResolvePlanAsk("plan", session.VerdictDeny); got != AskResolutionNotPending {
		t.Fatalf("replay = %v, want not-pending", got)
	}
	if got := requireApproval(t, plan); got.verdict != session.VerdictAllowAlways {
		t.Fatalf("delivered = %v, want allow-always", got.verdict)
	}
	if got := run.ResolveOrdinaryAsk("ordinary", session.VerdictDeny); got != AskResolutionResolved {
		t.Fatalf("ordinary was consumed: %v", got)
	}
	_ = requireApproval(t, ordinary)
	if got := child.ResolvePlanAsk("child-plan", session.VerdictDeny); got != AskResolutionResolved {
		t.Fatalf("child plan was consumed: %v", got)
	}
	_ = requireApproval(t, childPlan)
}
