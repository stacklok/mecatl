package agent_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/sessnap"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// presentplan_ask_test.go mirrors guardrail_ask_test.go's structure for the
// plan-approval gate's ask seam (issue #206, Wave 2). The PresentPlan tool is
// intercepted by name in plan mode and surfaced as a PlanOriginated ask; the
// verdict flips the mode (AllowOnce→ModeDefault, AllowAlways→ModeAccept) and
// terminates the run with StopPlanApproved, or on Deny terminates CLEANLY with
// StopPlanIterate so the operator's next prompt drives the revision (the model
// does NOT continue in-turn — issue #206 iterate UX fix).

// newPlanSession returns a session in ModePlan (the gate is inert outside plan
// mode — Catalog.Available hides PresentPlan under ModeDefault/ModeAccept).
func newPlanSession(t *testing.T) *session.Session {
	t.Helper()
	return session.New("s1", session.ModePlan, "/ws", session.Limits{}, time.Unix(0, 0))
}

// planCatalog returns a catalog with the PresentPlan tool registered (the
// dispatcher intercepts it by name; it must be in the catalog so lookupTool
// resolves and the tool card opens).
func planCatalog(t *testing.T) *tool.Catalog {
	t.Helper()
	return catalogWith(t, agent.NewPresentPlanTool())
}

// drivePlanAsk runs ONE prompt issuing a single PresentPlan call under plan mode,
// resolving the FIRST plan-originated permission ask with verdict. It returns the
// captured ask (if any) and the drained events. The model emits one PresentPlan
// call then a follow-up text turn; the follow-up is only reached on neither
// verdict (on Allow the run terminates StopPlanApproved before it; on Deny the run
// terminates StopPlanIterate before it — issue #206 iterate UX fix).
func drivePlanAsk(t *testing.T, interactive bool, verdict session.ApprovalVerdict) (ask *session.PendingAsk, evs []session.Event) {
	t.Helper()
	cat := planCatalog(t)
	llm := mockllm.New(
		mockllm.ToolCallTurn(toolCall("c1", "PresentPlan", `{"note":"step 1"}`)),
		mockllm.TextTurn("revised plan"), // reached only on Deny (loop continues)
	)
	e := newEngine(agent.Deps{LLM: llm, Catalog: cat, Interactive: interactive})
	sess := newPlanSession(t)
	r := e.Run(context.Background(), sess, memfs.NewWorkspace("/ws"), agent.RunRequest{Text: "plan a thing"})
	for ev := range r.Events() {
		evs = append(evs, ev)
		if ev.Type == session.EvPermissionAsk && ev.Ask != nil && ask == nil {
			a := *ev.Ask
			ask = &a
			r.Approve(ev.Ask.AskID, verdict)
		}
	}
	return ask, evs
}

// TestPresentPlanSurfacesPlanOriginatedAsk pins the interactive surface: the ask
// carries PlanOriginated=true (the serialized marker that makes cross-process
// resume work), Tool=="PresentPlan", and a human-readable reason.
func TestPresentPlanSurfacesPlanOriginatedAsk(t *testing.T) {
	ask, _ := drivePlanAsk(t, true, session.VerdictAllowOnce)
	if ask == nil {
		t.Fatal("an interactive plan-mode PresentPlan call must surface a permission ask")
	}
	if !ask.PlanOriginated {
		t.Fatal("the surfaced ask must carry PlanOriginated=true (cross-process load-bearing)")
	}
	if ask.Tool != "PresentPlan" {
		t.Fatalf("ask.Tool = %q, want %q", ask.Tool, "PresentPlan")
	}
	if ask.Reason == "" {
		t.Fatal("the ask must carry a human-readable reason")
	}
	if ask.Origin() != session.AskOriginPlan {
		t.Fatalf("ask.Origin() = %v, want AskOriginPlan", ask.Origin())
	}
}

// TestPlanApprovalAllowOnceFlipsModeAndTerminates pins the AllowOnce tail: the run
// ends with StopPlanApproved (a CLEAN terminal), the session mode flips from
// ModePlan to ModeDefault, the session is StateCompleted, and the recorded stop
// reason is StopPlanApproved. The model is NOT re-called after approval (the LLM
// script has only the PresentPlan turn — a second turn would prove the loop
// continued; it never fires).
func TestPlanApprovalAllowOnceFlipsModeAndTerminates(t *testing.T) {
	cat := planCatalog(t)
	llm := mockllm.New(mockllm.ToolCallTurn(toolCall("c1", "PresentPlan", `{"note":"x"}`)))
	e := newEngine(agent.Deps{LLM: llm, Catalog: cat, Interactive: true})
	sess := newPlanSession(t)
	r := e.Run(context.Background(), sess, memfs.NewWorkspace("/ws"), agent.RunRequest{Text: "plan"})
	var sawAsk bool
	var evs []session.Event
	for ev := range r.Events() {
		evs = append(evs, ev)
		if ev.Type == session.EvPermissionAsk && ev.Ask != nil {
			sawAsk = true
			r.Approve(ev.Ask.AskID, session.VerdictAllowOnce)
		}
	}
	if !sawAsk {
		t.Fatal("no plan ask surfaced")
	}
	if sess.Mode != session.ModeDefault {
		t.Fatalf("session mode after AllowOnce = %q, want %q", sess.Mode, session.ModeDefault)
	}
	if sess.State != session.StateCompleted {
		t.Fatalf("session state = %q, want %q", sess.State, session.StateCompleted)
	}
	if reason, ok := sess.RecordedStopReason(); !ok || reason != session.StopPlanApproved {
		t.Fatalf("recorded stop = %q (ok=%v), want %q", reason, ok, session.StopPlanApproved)
	}
	res := lastResult(t, evs)
	if res.Stop != session.StopPlanApproved {
		t.Fatalf("EvResult.Stop = %q, want %q", res.Stop, session.StopPlanApproved)
	}
}

// TestPlanApprovalAllowAlwaysFlipsToAcceptEdits pins the AllowAlways tail: the
// mode flips to ModeAccept (acceptEdits), the run ends with StopPlanApproved.
func TestPlanApprovalAllowAlwaysFlipsToAcceptEdits(t *testing.T) {
	cat := planCatalog(t)
	llm := mockllm.New(mockllm.ToolCallTurn(toolCall("c1", "PresentPlan", `{"note":"x"}`)))
	e := newEngine(agent.Deps{LLM: llm, Catalog: cat, Interactive: true})
	sess := newPlanSession(t)
	r := e.Run(context.Background(), sess, memfs.NewWorkspace("/ws"), agent.RunRequest{Text: "plan"})
	var sawAsk bool
	for ev := range r.Events() {
		if ev.Type == session.EvPermissionAsk && ev.Ask != nil {
			sawAsk = true
			r.Approve(ev.Ask.AskID, session.VerdictAllowAlways)
		}
	}
	if !sawAsk {
		t.Fatal("no plan ask surfaced")
	}
	if sess.Mode != session.ModeAccept {
		t.Fatalf("session mode after AllowAlways = %q, want %q", sess.Mode, session.ModeAccept)
	}
	if reason, ok := sess.RecordedStopReason(); !ok || reason != session.StopPlanApproved {
		t.Fatalf("recorded stop = %q (ok=%v), want %q", reason, ok, session.StopPlanApproved)
	}
}

// TestPlanApprovalDenyPausesForIteration pins the Deny tail (issue #206 iterate
// UX fix): a deny of a plan ask TERMINATES the run CLEANLY with StopPlanIterate
// (the run ENDS so the operator's next typed prompt drives the revision — the
// model does NOT continue iterating with no operator input). The session stays
// ModePlan (no mode flip — terminateComplete only flips when planApprovedTarget
// != ""), the session is StateCompleted, and the model is NOT re-called after
// the deny (the LLM script has only the PresentPlan turn — a second turn would
// prove the loop continued; it never fires). The deny result teaches the model
// the turn is pausing for operator feedback. Replaces the old
// TestPlanApprovalDenyContinuesInPlanMode (the behaviour changed from
// continue-in-turn → pause).
func TestPlanApprovalDenyPausesForIteration(t *testing.T) {
	ask, evs := drivePlanAsk(t, true, session.VerdictDeny)
	if ask == nil {
		t.Fatal("the plan ask must surface before the deny")
	}
	// A deny result must have been recorded for the PresentPlan call.
	var sawDenyResult bool
	for _, ev := range evs {
		if ev.Type == session.EvToolResult && ev.ToolResult != nil && ev.ToolResult.CallID == "c1" &&
			ev.ToolResult.IsError && strings.Contains(ev.ToolResult.Content, "plan not approved by operator") {
			sawDenyResult = true
		}
	}
	if !sawDenyResult {
		t.Fatal("a deny must surface a 'plan not approved by operator' error result for the PresentPlan call")
	}
	// The run PAUSED: it terminated with StopPlanIterate, NOT StopPlanApproved, and
	// NOT StopEndTurn (the model's follow-up text turn must NOT have run).
	res := lastResult(t, evs)
	if res.Stop != session.StopPlanIterate {
		t.Fatalf("a Deny must terminate with StopPlanIterate (pause for operator feedback); got stop = %q", res.Stop)
	}
	// An EvApproval must have fired for the deny.
	var sawApproval bool
	for _, ev := range evs {
		if ev.Type == session.EvApproval && ev.Approval != nil && ev.Approval.AskID == ask.AskID {
			sawApproval = true
		}
	}
	if !sawApproval {
		t.Fatal("an EvApproval must fire for the resolved plan ask (even on deny)")
	}
}

// TestPresentPlanAskCarriesPlanArgs pins issue #206 UX fix: surfacePlanAsk copies
// c.Args into PendingAsk.Args, so a PresentPlan call whose `plan` arg carries the
// full plan text surfaces that args blob on the wire (EvPermissionAsk.Ask.Args) for
// the mecatui approval modal to parse + render. The args JSON (incl. a multi-line
// plan) pass through verbatim — no proto change needed.
func TestPresentPlanAskCarriesPlanArgs(t *testing.T) {
	const planText = "1. read foo\n2. edit bar\n3. run tests"
	args := `{"plan":"` + planText + `","note":"three steps"}`
	cat := planCatalog(t)
	llm := mockllm.New(mockllm.ToolCallTurn(toolCall("c1", "PresentPlan", args)))
	e := newEngine(agent.Deps{LLM: llm, Catalog: cat, Interactive: true})
	sess := newPlanSession(t)
	r := e.Run(context.Background(), sess, memfs.NewWorkspace("/ws"), agent.RunRequest{Text: "plan"})
	var got []byte
	for ev := range r.Events() {
		if ev.Type == session.EvPermissionAsk && ev.Ask != nil {
			got = ev.Ask.Args
			r.Approve(ev.Ask.AskID, session.VerdictAllowOnce)
		}
	}
	if len(got) == 0 {
		t.Fatal("the surfaced plan ask must carry the PresentPlan args (PendingAsk.Args)")
	}
	if !strings.Contains(string(got), planText) {
		t.Fatalf("PendingAsk.Args must contain the full plan text\ngot=%q", string(got))
	}
	if !strings.Contains(string(got), `"note":"three steps"`) {
		t.Fatalf("PendingAsk.Args must retain the note field too\ngot=%q", string(got))
	}
}

// TestResumePlanApprovalAfterRestartExecutesAndFlips pins the cross-process resume:
// drive to StateAwaiting on a plan ask, snapshot-restore (process death), then
// ResumeApproval(AllowOnce) sets planApprovedTarget, driveFromAwaiting's runLoop sees
// it at the early check, terminates with StopPlanApproved, and flips the mode. This
// pins the serialized PlanOriginated marker as cross-process load-bearing (it must
// survive the snapshot round-trip, mirroring the guardrail restart test).
func TestResumePlanApprovalAfterRestartExecutesAndFlips(t *testing.T) {
	cat := planCatalog(t)
	llm := mockllm.New(mockllm.ToolCallTurn(toolCall("c1", "PresentPlan", `{"note":"x"}`)))
	sess := newPlanSession(t)
	e := newEngine(agent.Deps{LLM: llm, Catalog: cat, Interactive: true})

	// Drive to the awaiting ask, snapshot, then cancel (process death).
	r := e.Run(context.Background(), sess, memfs.NewWorkspace("/ws"), agent.RunRequest{Text: "plan"})
	var askID string
	var snap sessnap.Snapshot
	var snapErr error
	for ev := range r.Events() {
		if ev.Type == session.EvPermissionAsk && ev.Ask != nil && askID == "" {
			askID = ev.Ask.AskID
			if !ev.Ask.PlanOriginated {
				t.Fatal("the awaiting plan ask must be PlanOriginated")
			}
			snap, snapErr = sessnap.Of(sess)
			r.Cancel()
		}
	}
	if askID == "" {
		t.Fatal("no plan ask surfaced")
	}
	if snapErr != nil {
		t.Fatalf("snapshot: %v", snapErr)
	}
	if snap.State != session.StateAwaiting {
		t.Fatalf("snapshot state = %q, want awaiting", snap.State)
	}
	restored, err := snap.Restore()
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	// The PlanOriginated marker must survive the snapshot round-trip.
	ra, ok := restored.PendingAsk()
	if !ok || !ra.PlanOriginated {
		t.Fatalf("PlanOriginated must round-trip the snapshot; ok=%v ask=%+v", ok, ra)
	}

	// Fresh engine (a new process): resume the ask with AllowOnce. The run must
	// terminate with StopPlanApproved and flip the mode to ModeDefault, WITHOUT
	// re-calling the model on the plan (the pending call is NOT re-presented).
	cat2 := planCatalog(t)
	e2 := newEngine(agent.Deps{LLM: mockllm.New(mockllm.TextTurn("should not be reached")), Catalog: cat2, Interactive: true})
	rr := e2.ResumeApproval(context.Background(), restored, memfs.NewWorkspace("/ws"), askID, session.VerdictAllowOnce)
	evs := drain(rr)
	res := lastResult(t, evs)
	if res.Stop != session.StopPlanApproved {
		t.Fatalf("resume stop = %q, want %q", res.Stop, session.StopPlanApproved)
	}
	if restored.Mode != session.ModeDefault {
		t.Fatalf("mode after resume AllowOnce = %q, want %q", restored.Mode, session.ModeDefault)
	}
}

// TestResumePlanApprovalDenyIteratesTerminates pins the cross-process resume
// iterate path (issue #206): drive to StateAwaiting on a plan ask, snapshot-restore
// (process death), then ResumeApproval(Deny) sets r.planIterateRequested in the
// resolvePendingCall PlanOriginated deny arm, driveFromAwaiting's runLoop sees it at
// the early check, and the resumed run terminates CLEANLY with StopPlanIterate (the
// operator pauses to type feedback — the model is NOT re-called). The session stays
// ModePlan (no mode flip). This mirrors the live-path deny pause but on a FRESH
// process, pinning the serialized PlanOriginated marker as cross-process
// load-bearing for the iterate verdict too.
func TestResumePlanApprovalDenyIteratesTerminates(t *testing.T) {
	cat := planCatalog(t)
	llm := mockllm.New(mockllm.ToolCallTurn(toolCall("c1", "PresentPlan", `{"note":"x"}`)))
	sess := newPlanSession(t)
	e := newEngine(agent.Deps{LLM: llm, Catalog: cat, Interactive: true})

	// Drive to the awaiting ask, snapshot, then cancel (process death).
	r := e.Run(context.Background(), sess, memfs.NewWorkspace("/ws"), agent.RunRequest{Text: "plan"})
	var askID string
	var snap sessnap.Snapshot
	var snapErr error
	for ev := range r.Events() {
		if ev.Type == session.EvPermissionAsk && ev.Ask != nil && askID == "" {
			askID = ev.Ask.AskID
			if !ev.Ask.PlanOriginated {
				t.Fatal("the awaiting plan ask must be PlanOriginated")
			}
			snap, snapErr = sessnap.Of(sess)
			r.Cancel()
		}
	}
	if askID == "" {
		t.Fatal("no plan ask surfaced")
	}
	if snapErr != nil {
		t.Fatalf("snapshot: %v", snapErr)
	}
	restored, err := snap.Restore()
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	ra, ok := restored.PendingAsk()
	if !ok || !ra.PlanOriginated {
		t.Fatalf("PlanOriginated must round-trip the snapshot; ok=%v ask=%+v", ok, ra)
	}

	// Fresh engine (a new process): resume the ask with Deny. The run must
	// terminate with StopPlanIterate and stay in ModePlan, WITHOUT re-calling the
	// model (the pending call is NOT re-presented; the operator's next prompt drives
	// the revision).
	cat2 := planCatalog(t)
	e2 := newEngine(agent.Deps{LLM: mockllm.New(mockllm.TextTurn("should not be reached")), Catalog: cat2, Interactive: true})
	rr := e2.ResumeApproval(context.Background(), restored, memfs.NewWorkspace("/ws"), askID, session.VerdictDeny)
	evs := drain(rr)
	res := lastResult(t, evs)
	if res.Stop != session.StopPlanIterate {
		t.Fatalf("resume stop = %q, want %q (pause for operator feedback)", res.Stop, session.StopPlanIterate)
	}
	if restored.Mode != session.ModePlan {
		t.Fatalf("mode after resume Deny = %q, want %q (no flip — operator iterates)", restored.Mode, session.ModePlan)
	}
	if restored.State != session.StateCompleted {
		t.Fatalf("state after resume Deny = %q, want %q (clean terminal)", restored.State, session.StateCompleted)
	}
}

// TestPlanApprovalDoesNotFlipMidTurn pins session.go:765: SetMode is rejected from
// Running/Awaiting, so a plan-approval flip can ONLY happen at the terminal
// boundary (StateCompleted) via terminateComplete. A mid-turn SetMode must error.
func TestPlanApprovalDoesNotFlipMidTurn(t *testing.T) {
	sess := newPlanSession(t)
	// StateIdle (freshly created) — SetMode is legal from idle (not Running/Awaiting).
	if err := sess.SetMode(session.ModeDefault); err != nil {
		t.Fatalf("SetMode from idle must succeed: %v", err)
	}
	sess.Mode = session.ModePlan // reset for the next checks

	// BeginTurn drives the session to StateRunning; SetMode must be rejected there.
	if err := sess.BeginTurn(); err != nil {
		t.Fatalf("BeginTurn: %v", err)
	}
	if sess.State != session.StateRunning {
		t.Fatalf("state after BeginTurn = %q, want running", sess.State)
	}
	if err := sess.SetMode(session.ModeDefault); err == nil {
		t.Fatal("SetMode from StateRunning must be rejected (session.go:765 invariant)")
	}

	// PauseForApproval drives to StateAwaiting; SetMode must be rejected there too.
	if err := sess.PauseForApproval(session.PendingAsk{AskID: "a1", Tool: "PresentPlan", PlanOriginated: true}); err != nil {
		t.Fatalf("PauseForApproval: %v", err)
	}
	if sess.State != session.StateAwaiting {
		t.Fatalf("state after PauseForApproval = %q, want awaiting", sess.State)
	}
	if err := sess.SetMode(session.ModeDefault); err == nil {
		t.Fatal("SetMode from StateAwaiting must be rejected (session.go:765 invariant)")
	}
}

// TestPresentPlanAdvertisedOnlyInPlanMode pins the catalog projection gate (Wave 3,
// issue #206): PresentPlan is registered in the catalog (so the dispatcher's
// lookupTool resolves it and the tool card opens in plan mode) but the mode
// projection EXCLUDES it from every non-plan mode — it implements tool.PlanOnly,
// so Catalog.Available / Specs / AdvertisedSpecs hide it under ModeDefault and
// ModeAccept (it is never advertised to the model outside plan mode) while it
// REMAINS visible under ModePlan. The dispatcher's name+mode check is
// defense-in-depth ON TOP of this gate; a directly-dispatched PresentPlan call
// outside plan mode still falls through to the vestigial Execute (no ask
// surfaces), which this test also pins.
func TestPresentPlanAdvertisedOnlyInPlanMode(t *testing.T) {
	cat := planCatalog(t)

	// PresentPlan is EXCLUDED from the non-plan projection (Available/Specs): it is
	// never advertised to the model in default/acceptEdits. The catalog projection
	// is the gate; the dispatcher name+mode check is defense-in-depth.
	for _, mode := range []session.PermissionMode{session.ModeDefault, session.ModeAccept} {
		for _, tl := range cat.Available(mode) {
			if tl.Spec().Name == "PresentPlan" {
				t.Fatalf("PresentPlan must NOT be in Catalog.Available(%q) — the projection gate excludes PlanOnly tools from non-plan modes", mode)
			}
		}
		for _, spec := range cat.Specs(mode) {
			if spec.Name == "PresentPlan" {
				t.Fatalf("PresentPlan must NOT be in Catalog.Specs(%q) — the projection gate excludes PlanOnly tools from non-plan modes", mode)
			}
		}
	}

	// PresentPlan IS visible under ModePlan (it is read-only, so it passes the
	// plan-mode read-only filter AND it is the plan-mode signalling tool).
	var planVisible bool
	for _, tl := range cat.Available(session.ModePlan) {
		if tl.Spec().Name == "PresentPlan" {
			planVisible = true
		}
	}
	if !planVisible {
		t.Fatal("PresentPlan must be in Catalog.Available(ModePlan) — the projection gate advertises it in plan mode")
	}
	var planSpecVisible bool
	for _, spec := range cat.Specs(session.ModePlan) {
		if spec.Name == "PresentPlan" {
			planSpecVisible = true
		}
	}
	if !planSpecVisible {
		t.Fatal("PresentPlan must be in Catalog.Specs(ModePlan) — the projection gate advertises it in plan mode")
	}

	// Defense-in-depth: outside plan mode the dispatcher's name+mode check fails, so
	// a directly-dispatched PresentPlan call falls through to the vestigial Execute
	// (the misroute path) and NO ask surfaces.
	llm := mockllm.New(mockllm.ToolCallTurn(toolCall("c1", "PresentPlan", `{"note":"x"}`)), mockllm.TextTurn("done"))
	e := newEngine(agent.Deps{LLM: llm, Catalog: cat, Interactive: true})
	sess := session.New("s1", session.ModeDefault, "/ws", session.Limits{}, time.Unix(0, 0))
	r := e.Run(context.Background(), sess, memfs.NewWorkspace("/ws"), agent.RunRequest{Text: "go"})
	evs := drain(r)
	// No EvPermissionAsk may fire outside plan mode (the dispatcher does not intercept).
	for _, ev := range evs {
		if ev.Type == session.EvPermissionAsk {
			t.Fatal("PresentPlan must NOT surface an ask outside plan mode (dispatcher name+mode guard)")
		}
	}
	// The vestigial Execute ran: a non-error tool result with the awaiting-approval text.
	var sawVestige bool
	for _, ev := range evs {
		if ev.Type == session.EvToolResult && ev.ToolResult != nil && ev.ToolResult.CallID == "c1" &&
			!ev.ToolResult.IsError && strings.Contains(ev.ToolResult.Content, "awaiting operator approval") {
			sawVestige = true
		}
	}
	if !sawVestige {
		t.Fatal("outside plan mode PresentPlan must fall through to the vestigial Execute (misroute path)")
	}
}

// TestPlanApprovedTargetIsRunScoped pins that planApprovedTarget is a per-RUN
// transient, NOT serialized onto the session: a second run on the same session
// starts with a fresh (empty) planApprovedTarget. Run 1 approves a plan (flips to
// ModeDefault); run 2 (after Reopen, now in ModeDefault) does NOT carry over any
// plan-approved target — it runs an ordinary turn to completion.
func TestPlanApprovedTargetIsRunScoped(t *testing.T) {
	cat := planCatalog(t)
	llm1 := mockllm.New(mockllm.ToolCallTurn(toolCall("c1", "PresentPlan", `{"note":"x"}`)))
	e := newEngine(agent.Deps{LLM: llm1, Catalog: cat, Interactive: true})
	sess := newPlanSession(t)
	r1 := e.Run(context.Background(), sess, memfs.NewWorkspace("/ws"), agent.RunRequest{Text: "plan"})
	for ev := range r1.Events() {
		if ev.Type == session.EvPermissionAsk && ev.Ask != nil {
			r1.Approve(ev.Ask.AskID, session.VerdictAllowOnce)
		}
	}
	if sess.Mode != session.ModeDefault {
		t.Fatalf("after run 1 mode = %q, want %q", sess.Mode, session.ModeDefault)
	}
	// Reopen for run 2 — the session is now ModeDefault (flipped), so PresentPlan is
	// NOT intercepted; an ordinary text turn completes with StopEndTurn.
	if err := sess.Reopen(); err != nil {
		t.Fatalf("reopen: %v", err)
	}
	llm2 := mockllm.New(mockllm.TextTurn("done"))
	e2 := newEngine(agent.Deps{LLM: llm2, Catalog: cat, Interactive: true})
	r2 := e2.Run(context.Background(), sess, memfs.NewWorkspace("/ws"), agent.RunRequest{Text: "go"})
	evs := drain(r2)
	res := lastResult(t, evs)
	if res.Stop == session.StopPlanApproved {
		t.Fatal("run 2 must NOT carry over planApprovedTarget (it is run-scoped, not serialized)")
	}
	if res.Stop != session.StopEndTurn {
		t.Fatalf("run 2 stop = %q, want %q (ordinary completion)", res.Stop, session.StopEndTurn)
	}
}

// TestPresentPlanHeadlessNoAsk pins the headless posture: a non-interactive engine
// does NOT surface a plan-approval ask (there is no human to ask). Mirroring the
// guardrail headless degrade (preHook's askable-block → terminal block), surfacePlanAsk
// fails safe: it synthesizes a deny result teaching the model the plan was not
// approved, does NOT flip the mode or set planApprovedTarget, and the loop continues
// so the model can iterate or end the turn. No EvPermissionAsk may be emitted.
func TestPresentPlanHeadlessNoAsk(t *testing.T) {
	cat := planCatalog(t)
	llm := mockllm.New(
		mockllm.ToolCallTurn(toolCall("c1", "PresentPlan", `{"note":"x"}`)),
		mockllm.TextTurn("done"), // the loop continues after the degrade; the model ends the turn
	)
	e := newEngine(agent.Deps{LLM: llm, Catalog: cat, Interactive: false})
	sess := newPlanSession(t)
	r := e.Run(context.Background(), sess, memfs.NewWorkspace("/ws"), agent.RunRequest{Text: "plan"})
	evs := drain(r)
	for _, ev := range evs {
		if ev.Type == session.EvPermissionAsk {
			t.Fatal("a headless engine must NOT surface a plan-approval ask")
		}
	}
	// No mode flip occurred (no approval, planApprovedTarget never set).
	if sess.Mode != session.ModePlan {
		t.Fatalf("headless mode = %q, want %q (no approval, no flip)", sess.Mode, session.ModePlan)
	}
	// The degrade surfaced a deny result for the PresentPlan call.
	var sawDegrade bool
	for _, ev := range evs {
		if ev.Type == session.EvToolResult && ev.ToolResult != nil && ev.ToolResult.CallID == "c1" &&
			ev.ToolResult.IsError && strings.Contains(ev.ToolResult.Content, "plan not approved") {
			sawDegrade = true
		}
	}
	if !sawDegrade {
		t.Fatal("the headless degrade must surface a 'plan not approved' error result for the PresentPlan call")
	}
	// The run did NOT terminate with StopPlanApproved (no approval happened).
	res := lastResult(t, evs)
	if res.Stop == session.StopPlanApproved {
		t.Fatal("a headless plan run must NOT terminate with StopPlanApproved (no approval, no flip)")
	}
}
