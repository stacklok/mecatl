package client

import (
	"testing"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
)

func TestPendingApprovalExplicitOrigin(t *testing.T) {
	call := "tc_child"
	ev := &mecatlv1.Event{Type: "permission.ask", RunId: "root-run", Ask: &mecatlv1.PermissionAsk{AskId: "unchanged-key", Tool: "Shell", Args: `{}`, CallId: &call, OriginSessionId: "child", Guardrail: &mecatlv1.GuardrailApprovalScope{ReviewId: "review_tc_child_action"}}}
	candidate, ordinary, err := approvalFromEvent("root", "cursor", ev)
	if err != nil || ordinary || candidate.SessionID != "root" || candidate.OriginSessionID != "child" || candidate.ToolCallID != call || candidate.ReviewID != "review_tc_child_action" || candidate.AskID != "unchanged-key" {
		t.Fatalf("recovered ask = %+v, ordinary=%v, error=%v", candidate, ordinary, err)
	}
}
