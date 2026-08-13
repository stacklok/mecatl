package agent

import (
	"context"
	"encoding/json"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// presentplan.go implements the PresentPlan tool — the plan-approval gate's
// signalling affordance (issue #206). In plan mode, once the model has presented a
// complete plan in its preceding assistant text, it calls PresentPlan to hand control
// to the operator: the dispatcher (W2) intercepts this tool by name and surfaces the
// ask; the operator approves, requests edits, or iterates.
//
// The tool itself is VESTIGIAL: it performs no workspace mutation and records no
// state. Execute exists only for honesty on a misroute — the surfaced path never
// reaches it (the dispatcher intercepts PresentPlan in plan mode before execution).
// It is read-only / signaling-only, so it dispatches on the read-parallel path.
//
// The plan CONTENT rides the tool's `plan` string argument. surfacePlanAsk copies
// c.Args into PendingAsk.Args (the existing channel), so the plan reaches the
// mecatui plan-approval modal via proto `PermissionAsk.args` — the SAME posture as
// every other permission ask (Write/Bash asks carry their args for operator review).
// The operator is the intended audience. Gauntlet #7 holds: the EvApproval payload
// carries ONLY tool NAME + verdict + askID + call id — NO args (ApprovalPayload has
// no args field); the plan in EvPermissionAsk.Args is the live operator-review
// channel, the durable log stores ALREADY-REDACTED events, and a child's prompt
// stays on the child run (a PresentPlan call is plan-mode-only and never surfaces
// from a read-only subagent).

// presentPlanToolName is the catalog name of the plan-presentation signalling tool.
// The dispatcher intercepts a tool call whose name equals this constant.
const presentPlanToolName = "PresentPlan"

// presentPlanSchema is the JSON schema the model sees for PresentPlan's arguments.
// The plan CONTENT rides the `plan` argument so it reaches the plan-approval modal
// via PendingAsk.Args → proto `PermissionAsk.args` (the operator-review channel);
// the model should ALSO present the plan in its assistant message text for the
// transcript. The optional `note` is a one-line aside.
var presentPlanSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "plan": {
      "type": "string",
      "description": "The full plan text you are presenting for approval (the plan you just wrote in your message). The operator reads this in the approval modal, so pass the complete plan here."
    },
    "note": {
      "type": "string",
      "description": "Optional one-line note on the presented plan."
    }
  }
}`)

// presentPlanTool is the plan-approval gate's signalling tool. Stateless; the gate
// state lives in the dispatcher / awaiting-ask machinery, not here.
type presentPlanTool struct{}

// NewPresentPlanTool constructs the PresentPlan tool — the signalling affordance the
// model calls once it has presented a complete plan for operator approval. The tool
// is read-only and signaling-only; in plan mode the dispatcher intercepts it by name
// (presentPlanToolName) and surfaces the ask, so Execute is only reached on a misroute.
func NewPresentPlanTool() tool.Tool { return &presentPlanTool{} }

// Spec returns the model-facing specification for the PresentPlan tool.
func (*presentPlanTool) Spec() tool.ToolSpec {
	return tool.ToolSpec{
		Name: presentPlanToolName,
		Description: "Call this EXACTLY ONCE when your plan is complete and presented in your message text, then " +
			"STOP and wait for the operator. Pass the FULL plan text in the `plan` argument — the operator " +
			"reads it in the approval modal (also present it in your message text for the transcript). " +
			"The operator approves, requests edits, or iterates THROUGH this gate. " +
			"An inline 'acceptable'/'looks good'/'approved' in chat is NOT approval — only an approval via this tool " +
			"starts execution. Do NOT call any other tool or continue working after calling this.",
		Schema: presentPlanSchema,
	}
}

// ReadOnly reports true: PresentPlan performs no workspace mutation (it is a signalling
// affordance), so the dispatcher may run it on the read-parallel path.
func (*presentPlanTool) ReadOnly() bool { return true }

// PlanOnlyTool marks PresentPlan as a plan-mode-only tool (issue #206). It is
// registered into every catalog (so the shared and per-session catalog name-sets
// stay equal — guarded by TestPerSessionCatalogMatchesSharedCatalog) but the
// catalog's mode projection (tool.Available / Specs / AdvertisedSpecs) EXCLUDES it
// from every non-plan mode, so it is never advertised to the model in
// default/acceptEdits. The dispatcher's name+mode check (sess.Mode == ModePlan &&
// c.Name == "PresentPlan") is defense-in-depth on top of the projection gate.
func (*presentPlanTool) PlanOnlyTool() {}

// Execute is the vestigial misroute path. The plan-mode dispatcher intercepts a
// PresentPlan call by name and surfaces it as an operator ask before reaching here;
// Execute exists only so a misrouted call (e.g. PresentPlan invoked outside plan mode)
// produces an honest model-addressable result rather than a silent no-op.
func (*presentPlanTool) Execute(_ context.Context, call session.ToolCall, _ tool.Environment) (session.ToolResult, error) {
	return session.NewToolResult(call.ID, "PresentPlan: awaiting operator approval."), nil
}

// Compile-time assertions: presentPlanTool is a Tool AND a tool.PlanOnly (the
// marker that makes the catalog's non-plan projection exclude it — issue #206).
var (
	_ tool.Tool     = (*presentPlanTool)(nil)
	_ tool.PlanOnly = (*presentPlanTool)(nil)
)

// PlanApprovedProceedText is the harness-framed proceed message injected (by Wave 4's
// ApprovePlan service seam) as ordinary recorded history when an operator approves a
// presented plan, signalling the model to begin execution. It is event-silent (it is a
// recorded user message, NOT a diagnostics line — the loop's "exactly THREE lines"
// invariant holds) and mirrors the harness-note framing of the background-completion
// notices. Exported so the composition/service layer (which owns the proceed injection)
// can reference the exact text without re-stringing it; the text itself is a stable
// contract the model reads as the proceed signal.
const PlanApprovedProceedText = "Plan approved by operator. Proceed with execution."
