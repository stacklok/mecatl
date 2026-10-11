package server

import (
	"testing"

	"github.com/stacklok/mecatl/engine/session"
)

func TestPublicToolIdentityProjection(t *testing.T) {
	private := session.ToolCallID("provider-pair")
	first := session.ToolCall{ID: private, InvocationID: "tc_first", Name: "Read"}
	second := session.ToolCall{ID: private, InvocationID: "tc_second", Name: "Read"}
	if got := toProtoToolCall(first).GetId(); got != "tc_first" {
		t.Fatalf("call id = %q", got)
	}
	if got := toProtoToolCall(second).GetId(); got != "tc_second" {
		t.Fatalf("repeated provider id = %q", got)
	}
	result := session.ToolResult{CallID: private, InvocationID: first.InvocationID}
	if got := toProtoToolResult(result).GetCallId(); got != "tc_first" {
		t.Fatalf("result id = %q", got)
	}
	hook := session.HookPayload{HookID: "review_tc_first_action", InvocationID: first.InvocationID, CallID: private, SessionID: "child", Revision: 3, Guardrail: &session.GuardrailReviewPayload{ReviewID: "review_tc_first_action"}}
	wire := toProtoHook(hook)
	if wire.GetCallId() != "tc_first" || wire.GetOriginSessionId() != "child" || wire.GetHookId() != wire.GetGuardrail().GetReviewId() || wire.GetRevision() != 3 {
		t.Fatalf("hook: %+v", wire)
	}
	ask := toProtoAsk(session.PendingAsk{AskID: "exact-ask", Call: private, InvocationID: first.InvocationID, SessionID: "child", Guardrail: &session.GuardrailPendingScope{ReviewID: hook.HookID}})
	if ask.GetCallId() != "tc_first" || ask.GetOriginSessionId() != "child" || ask.GetGuardrail().GetReviewId() != hook.HookID {
		t.Fatalf("ask: %+v", ask)
	}
	receipt := toProtoApproval(session.ApprovalPayload{AskID: "exact-ask", Call: private, InvocationID: first.InvocationID, SessionID: "child", ReviewID: hook.HookID})
	if receipt.GetCallId() != "tc_first" || receipt.GetOriginSessionId() != "child" || receipt.GetReviewId() != hook.HookID {
		t.Fatalf("receipt: %+v", receipt)
	}
	rel := toProtoSessionRelationship(session.SessionRelationship{ParentSessionID: "root", CallID: private, ParentInvocationID: "tc_parent"})
	if rel.GetCallId() != "tc_parent" {
		t.Fatalf("parent: %+v", rel)
	}
	projected := toProto(session.Event{Type: session.EvSubagentTool, Subagent: &session.SubagentPayload{ParentCallID: string(private), ParentInvocationID: "tc_parent", ChildToolCallID: private, ChildInvocationID: first.InvocationID}}).GetSubagent()
	if projected.GetParentCallId() != "tc_parent" || projected.GetChildToolCallId() != "tc_first" {
		t.Fatalf("child: %+v", projected)
	}
	legacy := toProtoToolCall(session.ToolCall{ID: private})
	if legacy.GetId() != "" {
		t.Fatalf("legacy private id leaked: %q", legacy.GetId())
	}
}
