package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/session"
)

// TestPresentPlanReadOnly pins that PresentPlan is read-only (it dispatches on the
// read-parallel path — it is a signalling affordance, not a mutation).
func TestPresentPlanReadOnly(t *testing.T) {
	tl := NewPresentPlanTool()
	if !tl.ReadOnly() {
		t.Fatal("PresentPlan must report ReadOnly() == true (signalling-only, read-parallel)")
	}
}

// TestPresentPlanSpecName pins the catalog name the dispatcher intercepts by.
func TestPresentPlanSpecName(t *testing.T) {
	spec := NewPresentPlanTool().Spec()
	if spec.Name != presentPlanToolName {
		t.Fatalf("Spec().Name = %q, want %q", spec.Name, presentPlanToolName)
	}
	if spec.Name != "PresentPlan" { // belt-and-suspenders: the literal the dispatcher matches
		t.Fatalf("Spec().Name = %q, want literal %q", spec.Name, "PresentPlan")
	}
}

// TestPresentPlanExecuteVestigial pins the honest misroute path: Execute (only reached
// when the dispatcher did NOT intercept the call) returns a non-error ToolResult with
// the awaiting-approval content and the call's id, never a harness-level error.
func TestPresentPlanExecuteVestigial(t *testing.T) {
	tl := NewPresentPlanTool()
	call := session.NewToolCall("c1", presentPlanToolName, nil)
	res, err := tl.Execute(context.Background(), call, memfs.NewWorkspace("/ws"))
	if err != nil {
		t.Fatalf("vestigial Execute must never return a harness error: %v", err)
	}
	if res.IsError {
		t.Fatalf("vestigial Execute must not mark an error result: %+v", res)
	}
	if res.CallID != call.ID {
		t.Fatalf("result CallID = %q, want %q", res.CallID, call.ID)
	}
	if !strings.Contains(res.Content, "awaiting operator approval") {
		t.Fatalf("result Content = %q, want it to name the awaiting-approval state", res.Content)
	}
}
