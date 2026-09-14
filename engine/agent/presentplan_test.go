package agent

import (
	"context"
	"strings"
	"testing"

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

// TestPresentPlanDescriptionCarriesApprovalContract pins the model-visible tool
// contract independently from the two system-prompt copies. Each current plan
// presentation gets exactly one call and then stops. After feedback on an iterate/deny
// or cancellation, only new user input may trigger a revised or unchanged presentation
// through a new gated call; later chat assent is never execution approval.
func TestPresentPlanDescriptionCarriesApprovalContract(t *testing.T) {
	desc := NewPresentPlanTool().Spec().Description
	for _, clause := range []string{
		"EXACTLY ONCE PER CURRENT PRESENTATION",
		"STOP",
		"wait for the operator",
		"denied for iteration",
		"pending run is cancelled",
		"wait for new user input",
		"revised or unchanged plan",
		"NEW PresentPlan call",
		"Later chat assent requests a fresh gated review and is never execution approval. Only the harness proceed message that follows approval through the current PresentPlan gate starts execution.",
	} {
		if !strings.Contains(desc, clause) {
			t.Errorf("PresentPlan Spec().Description missing clause %q\ngot=%q", clause, desc)
		}
	}
	// It must also tell the model NOT to continue working after calling.
	if !strings.Contains(desc, "Do NOT call any other tool or continue working after calling this") {
		t.Errorf("PresentPlan Spec().Description missing post-call stop directive\ngot=%q", desc)
	}
}

// TestPresentPlanSpecIncludesPlanArg pins issue #206 UX fix: the PresentPlan schema
// the model sees carries a `plan` string argument so the plan content can ride the
// tool args through PendingAsk.Args → proto PermissionAsk.args → the mecatui
// approval modal. The optional `note` field is retained.
func TestPresentPlanSpecIncludesPlanArg(t *testing.T) {
	spec := NewPresentPlanTool().Spec()
	body := string(spec.Schema)
	if !strings.Contains(body, `"plan"`) {
		t.Errorf("PresentPlan schema missing the `plan` property\ngot=%s", body)
	}
	if !strings.Contains(body, "presenting for approval") {
		t.Errorf("PresentPlan `plan` description must tell the model to pass the full plan\ngot=%s", body)
	}
	// The optional note field is retained.
	if !strings.Contains(body, `"note"`) {
		t.Errorf("PresentPlan schema missing the `note` property\ngot=%s", body)
	}
	// The Spec().Description must instruct the model to pass the full plan in `plan`.
	if !strings.Contains(spec.Description, "FULL plan text in the `plan` argument") {
		t.Errorf("PresentPlan Description must instruct passing the plan in the `plan` arg\ngot=%q", spec.Description)
	}
}

// TestPresentPlanExecuteVestigial pins the honest misroute path: Execute (only reached
// when the dispatcher did NOT intercept the call) returns a non-error ToolResult with
// the awaiting-approval content and the call's id, never a harness-level error.
func TestPresentPlanExecuteVestigial(t *testing.T) {
	tl := NewPresentPlanTool()
	call := session.NewToolCall("c1", presentPlanToolName, nil)
	res, err := tl.Execute(context.Background(), call, memEnv("/ws"))
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
