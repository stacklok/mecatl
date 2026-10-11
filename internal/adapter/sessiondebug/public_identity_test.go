package sessiondebug

import (
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/session"
)

func TestInspectionDoesNotProjectProviderPairingID(t *testing.T) {
	call := session.NewToolCall("provider-secret-pair", "Read", nil)
	call.InvocationID = "tc_public"
	result := session.NewToolResult(call.ID, "ok")
	result.InvocationID = call.InvocationID
	projectedCall, _ := projectMessage(0, session.NewAssistantMessage("", "", []session.ToolCall{call}))
	projectedResult, _ := projectMessage(1, session.NewToolMessage(result))
	if projectedCall.ToolCalls[0].ID != "tc_public" || projectedResult.ToolResult.CallID != "tc_public" {
		t.Fatalf("inspector projected provider ID: %+v %+v", projectedCall, projectedResult)
	}
	rel := session.SessionRelationship{ParentInvocationID: "tc_public", CallID: call.ID}
	if !parentOccurrenceProven("tc_public", rel.ParentInvocationID) || parentOccurrenceProven("tc_other", rel.ParentInvocationID) {
		t.Fatal("unproven parent correlation accepted")
	}
	if strings.Contains(projectedCall.ToolCalls[0].ID, string(call.ID)) {
		t.Fatal("private pairing ID exposed")
	}
}
