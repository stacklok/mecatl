package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// parentMutatingCaller is an OPTIONAL interface a ReadOnly() tool may implement to
// declare that a SPECIFIC call will mutate the PARENT workspace at run end (FIX C).
// A tool stays ReadOnly()==true so its read-only fan-out keeps batching in parallel,
// but a call for which MutatesParent reports true is excluded from the concurrent
// read batch (dispatch-serial, flushed alone via runOne) so its post-run merge into
// the parent can never overlap a sibling parent Read/Grep/Glob — a torn read. A tool
// that does NOT implement this interface is wholly unaffected.
//
// Both implementers (SubagentTool, ParallelTool) return true ONLY for a call that
// will ACTUALLY merge (mode:"read-write" with the writable engine + merger wired;
// single-branch first/judge Parallel with the merger wired). This is the PER-RUN
// dispatch-serial half; the SerializingMerger mutex is the COMPLEMENTARY cross-run
// half (it serializes merge-vs-merge across concurrent sessions/runs targeting the
// same workspace — dispatch-serial only orders calls within one run).
type parentMutatingCaller interface {
	MutatesParent(call session.ToolCall) bool
}

// readBatchable reports whether a call may join the concurrent read batch: it must
// be a known ReadOnly tool that does not request serial dispatch and whose this-call
// posture is NOT parent-mutating. A DispatchSerial tool or parent-mutating call
// (parentMutatingCaller.MutatesParent true) is excluded so it flushes alone via
// runOne, exactly like a mutating tool — see DispatchSerial and
// parentMutatingCaller.
func readBatchable(t tool.Tool, known bool, c session.ToolCall) bool {
	if !known || !t.ReadOnly() {
		return false
	}
	if _, serial := t.(tool.DispatchSerial); serial {
		return false
	}
	if pm, ok := t.(parentMutatingCaller); ok && pm.MutatesParent(c) {
		return false
	}
	return true
}

// dispatch executes a turn's tool calls and returns their results in the
// original call order, plus a cancelled flag set when ctx was cancelled (mid
// permission-await or mid-execution) so the loop can terminate as cancelled.
//
// Ordering contract (gauntlet #4, read-parallel / mutate-serial):
//   - This ordering is run-local: it applies among sibling calls in this dispatch;
//     shared state reached by concurrent runs still requires its own synchronization.
//   - Calls are processed in their original order, batched into maximal runs of
//     consecutive read-batchable tools (readBatchable: known ReadOnly, not
//     DispatchSerial, not parent-mutating-this-call).
//   - A read-only batch runs CONCURRENTLY (one goroutine per call).
//   - A mutating tool, a serial-dispatch read-only tool, or a read-only tool
//     whose THIS call mutates the parent (parentMutatingCaller) runs ALONE,
//     strictly serially, never overlapping.
//   - Permission "asks" are sequenced one at a time (we never ask for two at
//     once): a batch that contains an Ask is resolved call-by-call before the
//     read-only calls that follow it execute.
//
// Results are keyed by CallID and re-assembled in input order.
func (e *Engine) dispatch(ctx context.Context, r *Run, sess *session.Session, env tool.Environment, turnIdx int, calls []session.ToolCall) ([]session.ToolResult, bool) {
	results := make(map[session.ToolCallID]session.ToolResult, len(calls))

	// Enqueue timestamp: every call in this turn enters dispatch NOW, before any
	// batching or serialization. Queue time is (execution-start − this enqueue),
	// so a mutating call held behind an earlier serial tool (or a permission ask)
	// records the real wait it sat through — the coordinated-omission fix
	// (perf-observability §5 decision 2). A nil Clock leaves enqueue as the zero
	// time, and timeExecute then reports a 0 queue duration (guarded like took).
	var enqueue time.Time
	if e.deps.Clock != nil {
		enqueue = e.deps.Clock.Now()
	}

	i := 0
	for i < len(calls) {
		c := calls[i]
		t, known := e.lookupTool(r, c.Name)

		// Mutating (or unknown) tools flush alone, serially. The same run-local
		// barrier applies to a read-only DispatchSerial tool and to a read-only tool
		// whose THIS call will mutate the parent workspace (parentMutatingCaller, FIX
		// C): a writable Subagent or single-branch auto-merging Parallel call merges
		// its fork diff into the parent at run end, so it must NOT overlap a sibling
		// parent Read/Grep/Glob in the same concurrent batch (torn read). Such a call
		// flushes ALONE via runOne, restoring read-parallel/mutate-serial.
		if !readBatchable(t, known, c) {
			res, cancelled := e.runOne(ctx, r, sess, env, turnIdx, c, t, known, enqueue)
			if cancelled {
				return nil, true
			}
			results[c.ID] = res
			i++
			continue
		}

		// Gather the maximal run of consecutive read-batchable calls.
		j := i
		var batch []session.ToolCall
		for j < len(calls) {
			nc := calls[j]
			nt, ok := e.lookupTool(r, nc.Name)
			if !readBatchable(nt, ok, nc) {
				break
			}
			batch = append(batch, nc)
			j++
		}

		batchRes, cancelled := e.runReadBatch(ctx, r, sess, env, turnIdx, batch, enqueue)
		if cancelled {
			return nil, true
		}
		for id, res := range batchRes {
			results[id] = res
		}
		i = j
	}

	// Re-assemble in original call order.
	ordered := make([]session.ToolResult, len(calls))
	for k, c := range calls {
		ordered[k] = results[c.ID]
	}
	return ordered, false
}

// runReadBatch runs a batch of read-only tool calls concurrently. Each call still
// passes through permission evaluation and the hook lifecycle. Permission "asks"
// are sequenced first (resolved one at a time, before any execution) so we never
// surface two asks simultaneously; the calls cleared to execute then run in
// parallel. It returns the results keyed by CallID and a cancelled flag.
func (e *Engine) runReadBatch(ctx context.Context, r *Run, sess *session.Session, env tool.Environment, turnIdx int, batch []session.ToolCall, enqueue time.Time) (map[session.ToolCallID]session.ToolResult, bool) {
	out := make(map[session.ToolCallID]session.ToolResult, len(batch))

	// Phase 1: resolve permission (asks sequenced) and run hooks. Calls that are
	// denied or hook-blocked get a synthesized result now and are excluded from
	// the parallel execution phase.
	type pending struct {
		call session.ToolCall
		t    tool.Tool
	}
	var toRun []pending
	for _, c := range batch {
		c := c // local copy: openCard takes &c, and this loop variable is reused.
		t, _ := e.lookupTool(r, c.Name)
		// Open the tool card BEFORE the permission/hook gate so any synthesized
		// failure (a deny result or a PreToolUse veto) lands on a card the client has
		// already seen — see openCard.
		e.openCard(r, turnIdx, c)
		// Plan-approval gate (issue #206, Wave 2): in plan mode a PresentPlan call is
		// intercepted by name BEFORE the permission/hook gate and surfaced as a
		// plan-approval ask (PlanOriginated). It must be sequenced one-at-a-time here
		// in Phase 1 — NEVER raised from the parallel fan-out (Phase 2) — exactly like
		// a permission/hook ask. Outside plan mode the tool is invisible via
		// Catalog.Available, so this branch is inert; the name+mode check is the
		// honest guard. The result is recorded now and the call is excluded from the
		// concurrent execution batch.
		if sess.Mode == session.ModePlan && c.Name == presentPlanToolName {
			res, cancelled := e.surfacePlanAsk(ctx, r, sess, turnIdx, c)
			if cancelled {
				return nil, true
			}
			out[c.ID] = res
			continue
		}
		decision, cancelled := e.authorize(ctx, r, sess, env, turnIdx, c)
		if cancelled {
			return nil, true
		}
		if decision.Effect == governance.Deny {
			out[c.ID] = denyResult(c, decision.Reason)
			e.emit(r, session.Event{Type: session.EvToolResult, Turn: turnIdx, ToolResult: ptr(out[c.ID])})
			continue
		}
		pre, herr := e.preHook(ctx, r, sess, turnIdx, c)
		if herr != nil {
			return nil, true
		}
		if pre.blocked {
			out[c.ID] = session.NewToolError(c.ID, pre.msg)
			e.emit(r, session.Event{Type: session.EvToolResult, Turn: turnIdx, ToolResult: ptr(out[c.ID])})
			continue
		}
		if pre.askApproval {
			// A hook BLOCK refined into an approval (ADR 0062). Sequence it HERE in
			// Phase 1 — one ask at a time — exactly like a permission ask; it must
			// NEVER be raised from the parallel fan-out (Phase 2). The call runs (on
			// allow) or is denied synchronously; either way its result is recorded now
			// and it is excluded from the concurrent execution batch.
			res, cancelled := e.askHookApproval(ctx, r, sess, env, turnIdx, c, t, pre.msg)
			if cancelled {
				return nil, true
			}
			out[c.ID] = res
			continue
		}
		// Execute the EFFECTIVE call (args possibly rewritten by the hook).
		toRun = append(toRun, pending{call: pre.effective, t: t})
	}

	// Phase 2: execute the cleared read-only calls concurrently.
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, p := range toRun {
		wg.Add(1)
		go func(p pending) {
			defer wg.Done()
			res := e.execute(ctx, r, sess, env, turnIdx, p.call, p.t, enqueue)
			mu.Lock()
			out[p.call.ID] = res
			mu.Unlock()
		}(p)
	}
	wg.Wait()

	if ctx.Err() != nil {
		return nil, true
	}
	return out, false
}

// resumeAbortedSiblingMessage is the synthetic error result text recorded for a
// tool call on the trailing assistant message that the awaiting re-entry will NOT
// dispatch (a sibling of the pending call, or — Q4 — the pending call itself when
// it was a SURFACED-child ask whose child run did not survive the restart). It is
// the closeOutInterruptedTurn analogue for the awaiting seam: model-facing replayed
// history, so it must accurately state why the call never got a real result and
// never claim a user action that did not happen.
const resumeAbortedSiblingMessage = "tool call aborted: the run was resumed at a different pending approval after restart; this sibling call's verdict was lost"

// Q4 (surfaced child asks) — VERIFIED no PendingAsk marker field needed. A SURFACED
// child ask never sets the PARENT session's pending: only the CHILD session's own
// loop calls PauseForApproval, while the parent stays StateRunning inside the
// delegation tool call. The server persists (and resumes via Approve) only top-level
// registered runs, so a restored StateAwaiting session ALWAYS holds a parent-OWN ask.
// The honest close-out the plan describes for a surfaced-child resume is therefore
// structurally unreachable through this seam; if a future change ever persisted a
// surfaced-child ask onto a parent, it would close out here as an ordinary unanswered
// sibling (resumeAbortedSiblingMessage). No surfaced-marker field was added (see the
// Phase 2 report + CLOUD-NATIVE.md ledger row 6).

// driveFromAwaiting is the body of the awaiting-only run-entry seam (ResumeApproval).
// It re-enters the loop AT the parked ask: it applies verdict to the pending tool
// call EXACTLY ONCE, closes out every OTHER unanswered tool call on the trailing
// assistant message as a synthetic aborted error result (so the replayed history has
// no dangling tool_use — provider-valid, ValidateToolPairing passes), records ONE
// ordered RecordToolResults slice (pending result + synthetic siblings) in ToolCalls
// order, saves, then continues the SHARED runLoop to completion.
//
// Exactly-once discipline: the pending call is resolved through the SAME
// post-authorize tail runOne uses after a verdict (deny → denyResult; allow →
// preHook + execute, so PostToolUse hooks + the audit recorder + EvToolResult fire
// identically; allow-always → also Policy.Learn). The sibling calls are NEVER
// dispatched (their verdicts were lost with the dead process) — they are closed out,
// not re-run. No call on the trailing assistant message is dispatched twice.
func (e *Engine) driveFromAwaiting(ctx context.Context, r *Run, sess *session.Session, env tool.Environment, askID string, verdict session.ApprovalVerdict) {
	// Step 0: the run-open signal, exactly like drive's Step 0a, so telemetry
	// adapters open a span/counter for the resumed run.
	e.emit(r, session.Event{Type: session.EvSessionInit})

	// Step 1: guard. The session MUST be awaiting and the pending ask MUST match the
	// askID we were asked to resume; a mismatch ends the run as StopError (never a
	// silent clean complete) so a stale/duplicate Approve cannot drive an unexpected
	// session to a clean terminal or execute the wrong call.
	ask, ok := sess.PendingAsk()
	if !ok {
		e.terminate(ctx, r, sess, session.StopError, "", session.Usage{},
			fmt.Errorf("%w: not in StateAwaiting", ErrNotAwaiting), false)
		return
	}
	if ask.AskID != askID {
		e.terminate(ctx, r, sess, session.StopError, "", session.Usage{},
			fmt.Errorf("%w: pending ask %q does not match requested %q", ErrNotAwaiting, ask.AskID, askID), false)
		return
	}

	// Locate the trailing assistant message (it carries the pending tool call plus
	// any unanswered siblings) and the pending call within it. The pending call's
	// args come from the trailing assistant message (the verbatim call the model
	// emitted), NOT from the ask (which clamps/redacts for surfacing).
	lastAssistant, pendingIdx, ok := locatePendingCall(sess.Conversation.Messages, ask)
	if !ok {
		e.terminate(ctx, r, sess, session.StopError, "", session.Usage{},
			fmt.Errorf("%w: pending tool call not found on the trailing assistant message", ErrNotAwaiting), false)
		return
	}
	msgs := sess.Conversation.Messages
	calls := msgs[lastAssistant].ToolCalls
	turnIdx := sess.Counters.Turns - 1

	// Step 2: leave StateAwaiting via the awaiting-only ResumeWith seam (clears
	// pending, preserves Counters/Usage). This is the SAME seam the live loop calls
	// in authorize; it is NOT resetToIdle (which would zero Counters + clear pending).
	if _, err := sess.ResumeWith(); err != nil {
		e.terminate(ctx, r, sess, session.StopError, "", session.Usage{},
			fmt.Errorf("agent: resume awaiting: %w", err), false)
		return
	}

	// Step 3: resolve the pending call EXACTLY ONCE through the verdict. A cancelled
	// ctx mid-resolution (cancelled=true) ends the run as cancelled, executing nothing
	// further.
	pendingResult, cancelled := e.resolvePendingCall(ctx, r, sess, env, turnIdx, calls[pendingIdx], ask, verdict)
	if cancelled {
		e.terminate(ctx, r, sess, session.StopCancelled, "", session.Usage{}, nil, false)
		return
	}

	// Step 4: assemble the ONE ordered RecordToolResults slice. For each tool call on
	// the trailing assistant message not already answered: the pending call gets its
	// real result; every other gets a synthetic aborted error (verdict lost with the
	// dead process — close out, do NOT re-dispatch). Already-answered calls (a
	// partially-dispatched turn) keep their recorded result and are skipped. Order
	// follows ToolCalls, so the recorded tool messages pair 1:1 with the assistant's
	// calls and ValidateToolPairing passes.
	answered := make(map[session.ToolCallID]struct{})
	for _, m := range msgs[lastAssistant+1:] {
		if m.Role == session.RoleTool && m.ToolResult != nil {
			answered[m.ToolResult.CallID] = struct{}{}
		}
	}
	var toRecord []session.ToolResult
	for i, c := range calls {
		if _, done := answered[c.ID]; done {
			continue
		}
		if i == pendingIdx {
			toRecord = append(toRecord, pendingResult)
			continue
		}
		sibling := session.NewToolError(c.ID, resumeAbortedSiblingMessage)
		e.openCard(r, turnIdx, c)
		e.emit(r, session.Event{Type: session.EvToolResult, Turn: turnIdx, ToolResult: ptr(sibling)})
		toRecord = append(toRecord, sibling)
	}
	if err := sess.RecordToolResults(toRecord); err != nil {
		e.terminate(ctx, r, sess, session.StopError, "", session.Usage{}, err, false)
		return
	}
	e.save(ctx, r, sess)

	// Step 5: continue the SHARED loop. total seeds zero (the verdict resolution
	// recorded no model usage; the budget brake reads the persisted cumulative
	// sess.Usage directly). lastText seeds empty (the prior assistant text, if any,
	// is in history and replays).
	e.runLoop(ctx, r, sess, env, session.Usage{}, "", false)
}

// pendingCallID picks the tool call id the pending ask refers to. The ask does not
// carry the call id directly, but on a well-formed awaiting turn the trailing
// assistant message's calls and the ask agree on Tool; when exactly one call
// matches the ask's Tool that call's id is authoritative. With multiple same-named
// calls the caller falls back to the Tool match (first wins) — a benign ambiguity:
// the multi-tool gate test parks on a UNIQUELY-named middle call, the real shape.
func pendingCallID(ask session.PendingAsk, calls []session.ToolCall) string {
	var match string
	n := 0
	for _, c := range calls {
		if c.Name == ask.Tool {
			match = string(c.ID)
			n++
		}
	}
	if n == 1 {
		return match
	}
	return ""
}

// locatePendingCall finds the trailing assistant message and the index of the
// pending tool call within it for the awaiting re-entry. It returns the trailing
// assistant message index, the pending call's index in that message's ToolCalls, and
// ok=false when there is no trailing assistant message or no call matching the ask.
// It prefers the call-id heuristic (a single same-named call is authoritative) and
// falls back to the first Tool match (a benign ambiguity for multiple same-named
// calls; the gate test parks on a uniquely-named call, the real shape).
func locatePendingCall(msgs []session.Message, ask session.PendingAsk) (lastAssistant, pendingIdx int, ok bool) {
	lastAssistant = -1
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == session.RoleAssistant {
			lastAssistant = i
			break
		}
	}
	if lastAssistant < 0 {
		return -1, -1, false
	}
	calls := msgs[lastAssistant].ToolCalls
	pendingIdx = -1
	if id := pendingCallID(ask, calls); id != "" {
		for i, c := range calls {
			if string(c.ID) == id {
				pendingIdx = i
				break
			}
		}
	}
	if pendingIdx < 0 {
		for i, c := range calls {
			if c.Name == ask.Tool {
				pendingIdx = i
				break
			}
		}
	}
	if pendingIdx < 0 {
		return -1, -1, false
	}
	return lastAssistant, pendingIdx, true
}

// resolvePendingCall applies verdict to the pending tool call EXACTLY ONCE and
// returns its result, plus cancelled=true if ctx was cancelled mid-resolution. Deny
// synthesizes a deny result (the tool is NOT run); AllowOnce/AllowAlways run the call
// through the SAME post-authorize tail runOne uses (preHook + execute, so PostToolUse
// hooks + the audit recorder + EvToolResult fire identically); AllowAlways also
// Learns a per-session rule for FUTURE calls (the rehydrated permstore is in-memory —
// the accepted Phase 2 wart; the rule covers later calls in THIS resumed run, Phase
// 3b makes it durable). The card is opened before the gate/result (the "ToolCall card
// before the gate" invariant) on every branch.
func (e *Engine) resolvePendingCall(ctx context.Context, r *Run, sess *session.Session, env tool.Environment, turnIdx int, pendingCall session.ToolCall, ask session.PendingAsk, verdict session.ApprovalVerdict) (session.ToolResult, bool) {
	// Record the resolved verdict on the event stream (the resume-from-awaiting
	// twin of the authorize emit) so the durable EventLog captures the approval
	// record on the resume path too. Tool NAME + verdict string + askID + the
	// opaque gated call id only — no raw args, no deny-reason body (gauntlet #7).
	// AllowAlways mirrors the verdict, not the policy outcome.
	e.emit(r, session.Event{Type: session.EvApproval, Turn: turnIdx, Approval: &session.ApprovalPayload{
		AskID:       ask.AskID,
		Verdict:     session.VerdictString(verdict),
		Tool:        pendingCall.Name,
		Call:        pendingCall.ID,
		AllowAlways: verdict == session.VerdictAllowAlways,
	}})
	if verdict == session.VerdictDeny {
		// PLAN-ORIGINATED resume deny (issue #206): a cross-process resumed plan ask
		// denied via ResumeApproval must ALSO set r.planIterateRequested so the
		// resumed run terminates StopPlanIterate at runLoop's EARLY check (the live
		// path sets it in surfacePlanAsk's deny branch). The serialized
		// PlanOriginated marker is the durable signal that this was a plan ask.
		if ask.PlanOriginated {
			r.planIterateRequested = true
		}
		e.openCard(r, turnIdx, pendingCall)
		res := denyResult(pendingCall, fmt.Sprintf("denied by user: %s", ask.Reason))
		e.emit(r, session.Event{Type: session.EvToolResult, Turn: turnIdx, ToolResult: ptr(res)})
		return res, false
	}
	if verdict == session.VerdictAllowAlways {
		e.deps.Policy.Learn(sess.ID, pendingCall)
	}
	t, known := e.lookupTool(r, pendingCall.Name)
	e.openCard(r, turnIdx, pendingCall)
	if !known {
		res := session.NewToolError(pendingCall.ID, fmt.Sprintf("unknown tool %q", pendingCall.Name))
		e.emit(r, session.Event{Type: session.EvToolResult, Turn: turnIdx, ToolResult: ptr(res)})
		return res, false
	}
	// HOOK-ORIGINATED resume (ADR 0062): a hook-blocked call that the human authorized
	// while parked AWAITING must EXECUTE WITHOUT re-running the PreToolUse hook —
	// re-running it would re-block (re-ask), and a fresh process has no in-memory
	// waiver to short-circuit it. The serialized HookOriginated marker is the only
	// durable signal that this exact blocked call was already approved, so on Allow we
	// skip preHook and execute directly (a deny was handled above). This mirrors
	// askHookApproval's live-path allow tail.
	if ask.HookOriginated {
		var enqueue time.Time
		if e.deps.Clock != nil {
			enqueue = e.deps.Clock.Now()
		}
		return e.execute(ctx, r, sess, env, turnIdx, pendingCall, t, enqueue), false
	}

	// PLAN-ORIGINATED resume (issue #206, Wave 2): a plan-approval ask that the
	// human authorized while parked AWAITING must NOT re-present the plan or run any
	// tool — the serialized PlanOriginated marker is the durable signal that this
	// PresentPlan call was already approved. On Allow we synthesize the allow result
	// (mirroring surfacePlanAsk's live-path allow tail) and set r.planApprovedTarget
	// (AllowOnce→ModeDefault, AllowAlways→ModeAccept); driveFromAwaiting's subsequent
	// runLoop sees planApprovedTarget != "" at its early check and terminates with
	// StopPlanApproved, and terminateComplete flips the session mode at the boundary.
	// A deny was handled by the deny arm above (it sets r.planIterateRequested so the
	// resumed run terminates StopPlanIterate). We do NOT re-run preHook (the human
	// already authorized this plan; PresentPlan is signalling-only, so there is
	// nothing to execute). This mirrors surfacePlanAsk's live-path allow tail.
	if ask.PlanOriginated {
		r.planApprovedTarget = planApprovedTargetForVerdict(verdict)
		res := session.NewToolResult(pendingCall.ID, "plan approved by operator: proceeding to execution")
		e.emit(r, session.Event{Type: session.EvToolResult, Turn: turnIdx, ToolResult: ptr(res)})
		return res, false
	}

	pre, herr := e.preHook(ctx, r, sess, turnIdx, pendingCall)
	if herr != nil {
		return session.ToolResult{}, true // ctx cancelled while running the hook
	}
	if pre.blocked {
		res := session.NewToolError(pendingCall.ID, pre.msg)
		e.emit(r, session.Event{Type: session.EvToolResult, Turn: turnIdx, ToolResult: ptr(res)})
		return res, false
	}
	// A non-hook-originated (policy) ask cannot itself yield an askable hook block on
	// resume: a re-run preHook here that wanted to ask would have nowhere to surface
	// (the run is already mid-resume). Treat an askApproval refinement as a terminal
	// block on this path (fail-safe) so the resumed call never silently runs unasked.
	if pre.askApproval {
		res := session.NewToolError(pendingCall.ID, pre.msg)
		e.emit(r, session.Event{Type: session.EvToolResult, Turn: turnIdx, ToolResult: ptr(res)})
		return res, false
	}
	if decision, cancelled := e.authorizeMutatedSystemScope(ctx, r, sess, env, turnIdx, pendingCall, pre.effective, true); cancelled {
		return session.ToolResult{}, true
	} else if decision.Effect == governance.Deny {
		res := denyResult(pre.effective, decision.Reason)
		e.emit(r, session.Event{Type: session.EvToolResult, Turn: turnIdx, ToolResult: ptr(res)})
		return res, false
	}
	var enqueue time.Time
	if e.deps.Clock != nil {
		enqueue = e.deps.Clock.Now()
	}
	return e.execute(ctx, r, sess, env, turnIdx, pre.effective, t, enqueue), false
}

// runOne handles a single (mutating or unknown) tool call serially: authorize,
// pre-hook, execute, post-hook. It returns the result and a cancelled flag.
func (e *Engine) runOne(ctx context.Context, r *Run, sess *session.Session, env tool.Environment, turnIdx int, c session.ToolCall, t tool.Tool, known bool, enqueue time.Time) (session.ToolResult, bool) {
	if !known {
		// Open a card for the unknown tool BEFORE its error result, exactly like the
		// known-tool path opens one before the gate. A client (ACP/mecatui) keys a
		// tool.result update to a prior tool.call card; without an open card the failure
		// for a tool_call the client never saw is droppable (the "ToolCall card before
		// the gate" invariant). The card carries the unknown name + args so the client
		// can render it and then mark it failed when the error result arrives.
		e.openCard(r, turnIdx, c)
		res := session.NewToolError(c.ID, fmt.Sprintf("unknown tool %q", c.Name))
		e.emit(r, session.Event{Type: session.EvToolResult, Turn: turnIdx, ToolResult: ptr(res)})
		return res, false
	}

	// Open the tool card BEFORE the permission/hook gate so any synthesized failure
	// (a deny result or a PreToolUse veto) lands on a card the client has already
	// seen.
	e.openCard(r, turnIdx, c)

	// Plan-approval gate (issue #206, Wave 2): in plan mode a PresentPlan call is
	// intercepted by name BEFORE the permission/hook gate and surfaced as a
	// plan-approval ask. PresentPlan is read-only so it normally batches in
	// runReadBatch Phase 1; this runOne branch is the defensive mirror so a
	// PresentPlan call flushed alone (or alongside a mutating sibling) is sequenced
	// correctly and never reaches authorize/execute. Outside plan mode the branch
	// is inert (the tool is invisible via Catalog.Available).
	if sess.Mode == session.ModePlan && c.Name == presentPlanToolName {
		return e.surfacePlanAsk(ctx, r, sess, turnIdx, c)
	}

	decision, cancelled := e.authorize(ctx, r, sess, env, turnIdx, c)
	if cancelled {
		return session.ToolResult{}, true
	}
	if decision.Effect == governance.Deny {
		res := denyResult(c, decision.Reason)
		e.emit(r, session.Event{Type: session.EvToolResult, Turn: turnIdx, ToolResult: ptr(res)})
		return res, false
	}

	pre, herr := e.preHook(ctx, r, sess, turnIdx, c)
	if herr != nil {
		return session.ToolResult{}, true
	}
	if pre.blocked {
		res := session.NewToolError(c.ID, pre.msg)
		e.emit(r, session.Event{Type: session.EvToolResult, Turn: turnIdx, ToolResult: ptr(res)})
		return res, false
	}
	if pre.askApproval {
		// A hook BLOCK refined into an approval (ADR 0062): surface it to the human.
		// It runs the call on allow, denies it otherwise — all serial, like a policy
		// ask (runOne is the mutate-serial path; surfacing here never overlaps a batch).
		return e.askHookApproval(ctx, r, sess, env, turnIdx, c, t, pre.msg)
	}

	// A PreToolUse hook is trusted to rewrite ordinary arguments, but it cannot
	// silently turn a managed Bash call into the separately-authorized system scope.
	if decision, cancelled := e.authorizeMutatedSystemScope(ctx, r, sess, env, turnIdx, c, pre.effective, false); cancelled {
		return session.ToolResult{}, true
	} else if decision.Effect == governance.Deny {
		res := denyResult(pre.effective, decision.Reason)
		e.emit(r, session.Event{Type: session.EvToolResult, Turn: turnIdx, ToolResult: ptr(res)})
		return res, false
	}

	// Execute the EFFECTIVE call (args possibly rewritten by the PreToolUse hook).
	return e.execute(ctx, r, sess, env, turnIdx, pre.effective, t, enqueue), false
}

// surfaceAsk is the shared SPINE both ask sites (authorize's policy ask and
// askHookApproval's guardrail ask) run: register the resolution channel BEFORE
// pausing/emitting (so a racing Approve can't be lost) → PauseForApproval (with
// discard-on-err) → emit EvPermissionAsk → await the verdict → ResumeWith → emit the
// resolved EvApproval. The caller supplies the fully-built PendingAsk (each site owns
// its own provenance bits — ConfiguredAsk/FlooredConfiguredAllow vs HookOriginated)
// and keeps only its verdict→action tail.
//
// Returns: the awaited verdictResult; ok=false when ctx was cancelled while awaiting
// (the caller terminates the call as cancelled); paused=false when PauseForApproval
// failed (the caller synthesizes its own "cannot pause" result — the channel was
// already discarded here). When paused is false, verdictResult/ok are zero and MUST
// be ignored. The EvApproval is emitted here ONLY on a real verdict (ok), exactly as
// before — a cancelled await emits none.
func (e *Engine) surfaceAsk(ctx context.Context, r *Run, sess *session.Session, turnIdx int, ask session.PendingAsk) (res approval, ok, paused bool) {
	ch := r.asks.register(ask.AskID)
	if err := sess.PauseForApproval(ask); err != nil {
		r.asks.discard(ask.AskID)
		return approval{}, false, false
	}
	a := ask
	e.emit(r, session.Event{Type: session.EvPermissionAsk, Turn: turnIdx, Ask: &a})

	verdictResult, ok := r.asks.await(ctx, ask.AskID, ch)

	// Resume the session regardless of verdict; the caller owns acting on the
	// decision, so the aggregate only reconciles its own lifecycle.
	if _, err := sess.ResumeWith(); err != nil {
		_ = err // already resumed/terminal (e.g. cancel path); fall through.
	}

	if !ok {
		// ctx cancelled while awaiting — no verdict, no EvApproval.
		return approval{}, false, true
	}

	// Record the resolved verdict on the event stream (the durable EventLog consumes
	// it at the server relay; the loop only emits — it never touches a store). Tool
	// NAME + verdict string + askID + the opaque gated call id only — no raw args, no
	// deny-reason body (gauntlet #7; see ApprovalPayload). AllowAlways mirrors the
	// verdict, not the policy outcome.
	e.emit(r, session.Event{Type: session.EvApproval, Turn: turnIdx, Approval: &session.ApprovalPayload{
		AskID:       ask.AskID,
		Verdict:     session.VerdictString(verdictResult.verdict),
		Tool:        ask.Tool,
		Call:        ask.Call,
		AllowAlways: verdictResult.verdict == session.VerdictAllowAlways,
	}})
	return verdictResult, true, true
}

// permissionDecision evaluates normal Bash authority and, only for the declared
// system temporary scope, the independent tool-wide escape capability. The
// synthetic capability is never dispatched or registered as a tool.
func (e *Engine) permissionDecision(ctx context.Context, sess *session.Session, env tool.Environment, c session.ToolCall) governance.PermissionDecision {
	ordinary := e.deps.Policy.Evaluate(ctx, sess.ID, sess.Mode, c, env.Workspace())
	if !bashSystemScope(c) || ordinary.Effect == governance.Deny {
		return ordinary
	}
	system := e.deps.Policy.Evaluate(ctx, sess.ID, sess.Mode, session.ToolCall{Name: bashSystemTempToolName}, env.Workspace())
	if system.Effect == governance.Deny {
		return system
	}
	if ordinary.Effect == governance.Ask {
		return ordinary
	}
	return system
}

func (e *Engine) authorizeMutatedSystemScope(ctx context.Context, r *Run, sess *session.Session, env tool.Environment, turnIdx int, original, effective session.ToolCall, resuming bool) (governance.PermissionDecision, bool) {
	if bashSystemScope(original) || !bashSystemScope(effective) {
		return governance.PermissionDecision{Effect: governance.Allow}, false
	}
	decision := e.deps.Policy.Evaluate(ctx, sess.ID, sess.Mode, session.ToolCall{Name: bashSystemTempToolName}, env.Workspace())
	if decision.Effect == governance.Deny {
		return decision, false
	}
	if decision.Effect != governance.Ask {
		return decision, false
	}
	if resuming {
		return governance.PermissionDecision{Effect: governance.Deny, Reason: "system temporary scope requires separate approval after PreToolUse mutation"}, false
	}
	ask := session.PendingAsk{AskID: newAskID(sess.ID, sess.Counters.ToolCalls, effective.ID, r.askDiscriminator), Tool: effective.Name, Args: systemScopeApprovalArgs(effective), Reason: decision.Reason, Call: effective.ID, ConfiguredAsk: decision.ConfiguredAsk, FlooredConfiguredAllow: decision.FlooredConfiguredAllow}
	verdict, ok, paused := e.surfaceAsk(ctx, r, sess, turnIdx, ask)
	if !paused {
		return governance.PermissionDecision{Effect: governance.Deny, Reason: "internal: cannot pause"}, false
	}
	if !ok {
		return governance.PermissionDecision{Effect: governance.Deny, Reason: "cancelled"}, true
	}
	if verdict.verdict == session.VerdictAllowOnce || verdict.verdict == session.VerdictAllowAlways {
		return governance.PermissionDecision{Effect: governance.Allow}, false
	}
	return governance.PermissionDecision{Effect: governance.Deny, Reason: "denied by user: " + decision.Reason}, false
}

func bashSystemScope(c session.ToolCall) bool {
	if c.Name != tool.BashToolName {
		return false
	}
	args, _, ok := parseBashArgs(c)
	return ok && bashScope(args) == tool.TemporaryScopeSystem
}

// systemScopeApprovalArgs projects only the requested scope and a command verb.
// It must never include a runner-owned temporary path or environment value.
func systemScopeApprovalArgs(c session.ToolCall) []byte {
	args, _, ok := parseBashArgs(c)
	if !ok {
		return []byte(`{"temp_scope":"system","command_summary":"Bash command"}`)
	}
	summary := "Bash command"
	if fields := strings.Fields(args.Command); len(fields) > 0 {
		summary = fields[0]
		if len(fields) > 1 {
			summary += " " + fields[1]
		}
	}
	out, err := json.Marshal(struct {
		TempScope string `json:"temp_scope"`
		Summary   string `json:"command_summary"`
	}{TempScope: "system", Summary: summary})
	if err != nil {
		return []byte(`{"temp_scope":"system","command_summary":"Bash command"}`)
	}
	return out
}

// authorize evaluates the permission policy for a call and, on Ask, pauses the
// loop until the client approves or denies (or ctx cancels). It returns the
// effective decision (Allow or Deny — an approved Ask becomes Allow, a denied or
// cancelled Ask becomes Deny) and a cancelled flag set only when ctx was
// cancelled while awaiting.
func (e *Engine) authorize(ctx context.Context, r *Run, sess *session.Session, env tool.Environment, turnIdx int, c session.ToolCall) (governance.PermissionDecision, bool) {
	decision := e.permissionDecision(ctx, sess, env, c)
	if decision.Effect != governance.Ask {
		// Operator visibility for a policy DENY: the deny reason otherwise reaches
		// only the client event (via denyResult), never the operator channel. Emit
		// it here, at the one site where the POLICY (not the user) resolves the call,
		// so an operator can see WHY a call was refused. Allow/Ask are NOT emitted —
		// they are the event taxonomy's job (EvToolCall / EvPermissionAsk); emitting
		// them here would double-log.
		if decision.Effect == governance.Deny {
			r.diag.Log(ctx, port.LevelInfo, "tool call denied by policy", "tool", c.Name, "reason", decision.Reason, "turn", turnIdx)
		}
		return decision, false
	}

	askID := newAskID(sess.ID, sess.Counters.ToolCalls, c.ID, r.askDiscriminator)
	args := c.Args
	if bashSystemScope(c) {
		args = systemScopeApprovalArgs(c)
	}
	ask := session.PendingAsk{
		AskID:  askID,
		Tool:   c.Name,
		Args:   args,
		Reason: decision.Reason,
		Call:   c.ID,
		// The two child-ask decision bits ride the PendingAsk verbatim (issue #32)
		// so resolveChildAsk can honour a configured Ask (never auto-approved) and
		// resolve a substitution-floored configured Allow without surfacing.
		ConfiguredAsk:          decision.ConfiguredAsk,
		FlooredConfiguredAllow: decision.FlooredConfiguredAllow,
	}

	verdictResult, ok, paused := e.surfaceAsk(ctx, r, sess, turnIdx, ask)
	if !paused {
		return governance.PermissionDecision{Effect: governance.Deny, Reason: "internal: cannot pause"}, false
	}
	if !ok {
		// ctx cancelled while awaiting.
		return governance.PermissionDecision{Effect: governance.Deny, Reason: "cancelled"}, true
	}
	verdict := verdictResult.verdict

	switch verdict {
	case session.VerdictAllowAlways:
		// Learn a per-session allow rule for this exact tool+pattern as a SIDE
		// EFFECT — it governs FUTURE calls only and never blocks or re-evaluates the
		// current one (which proceeds one-shot via the Allow below). Learn is itself
		// a no-op when the call is not safely learnable (compound/substituted Bash,
		// no targetable pattern). It can NEVER override a deny or bypass plan mode:
		// the rule is consulted by Evaluate at the lowest scope, behind the
		// deny-dominant fold and the plan-mode gate.
		e.deps.Policy.Learn(sess.ID, c)
		return governance.PermissionDecision{Effect: governance.Allow}, false
	case session.VerdictAllowOnce:
		// Permit THIS call only; nothing learned.
		return governance.PermissionDecision{Effect: governance.Allow}, false
	default:
		// VerdictDeny (incl. the zero value / fail-safe). A HEADLESS subagent auto-deny
		// carries an accurate denyReason ("not permitted in a non-interactive subagent
		// shell: …"); use it verbatim so the model sees the real cause and what to do.
		// Without one (a real user / surfaced-human deny) keep the "denied by user"
		// wording, which is accurate there.
		if verdictResult.denyReason != "" {
			return governance.PermissionDecision{Effect: governance.Deny, Reason: verdictResult.denyReason}, false
		}
		return governance.PermissionDecision{
			Effect: governance.Deny,
			Reason: fmt.Sprintf("denied by user: %s", decision.Reason),
		}, false
	}
}

// preHookResult is the outcome of running the PreToolUse hook, normalized for every
// caller (runOne, runReadBatch Phase 1, resolvePendingCall). Exactly one of the
// three terminal shapes holds:
//   - PLAIN ALLOW: blocked=false, askApproval=false — execute effective.
//   - TERMINAL BLOCK: blocked=true, msg set — synthesize a NewToolError(msg).
//   - ASKABLE BLOCK: askApproval=true, msg set, effective=c — surface to the human
//     via askHookApproval (INTERACTIVE engines only; the headless degrade to a
//     terminal block already happened inside preHook, so askApproval is never true
//     when e.deps.Interactive is false).
//
// The tri-state invariant (at most one of blocked/askApproval set) is guaranteed by
// preHook being the SOLE constructor — the struct does not enforce it. Do not build a
// preHookResult outside preHook; a caller reading {blocked, askApproval} both true
// would be a preHook bug, not a possible input.
//
// err is non-nil ONLY when ctx was cancelled (the hook ran under a cancelled
// context); the caller then terminates as cancelled.
type preHookResult struct {
	effective   session.ToolCall
	blocked     bool
	askApproval bool
	msg         string
}

// preHook runs the PreToolUse hook and returns a normalized preHookResult (plus a
// cancellation error). It returns a TERMINAL block when the hook vetoes the call
// (exit 2 / hook error), an ASKABLE block when the hook refined a Block into an
// approval (HookOutcome.AskApproval) AND a human approver is attached
// (e.deps.Interactive) — a HEADLESS engine DEGRADES an askable block to a terminal
// block here, INSIDE preHook, so every caller inherits the fail-safe automatically
// (ADR 0062) — and otherwise a PLAIN allow carrying the EFFECTIVE call to execute:
// identical to the input call unless the hook returned a non-empty
// HookOutcome.Mutated payload, in which case the call's Args are rewritten (see the
// mutation note below).
//
// The PreToolUse HookEvent.Input is the tool's raw arguments JSON (c.Args, no
// wrapper). HookOutcome.Mutated is interpreted SYMMETRICALLY: it is the rewritten
// arguments JSON, and it replaces c.Args while preserving the CallID and tool
// Name. A malformed (non-JSON) mutation is ignored — the original args stand —
// and a notice event is emitted.
//
// TRUST / ORDERING (security-relevant): the permission policy (authorize →
// Policy.Evaluate) has ALREADY run on the ORIGINAL, pre-mutation args by the time
// preHook is called. The mutated args are NOT re-permission-checked. This is
// deliberate and matches the trust model: a PreToolUse hook is operator-deployed
// and strictly more trusted than the model, so a hook is allowed to rewrite a
// call past the policy that gated the model's original request (mirroring Claude
// Code semantics). Callers MUST execute the returned call, not the input call.
func (e *Engine) preHook(ctx context.Context, r *Run, sess *session.Session, turnIdx int, c session.ToolCall) (preHookResult, error) {
	if e.deps.Hooks == nil {
		return preHookResult{effective: c}, nil
	}
	if ctx.Err() != nil {
		return preHookResult{effective: c}, ctx.Err()
	}
	ev := governance.HookEvent{
		Phase:     governance.PhasePreToolUse,
		Tool:      c.Name,
		Input:     c.Args,
		SessionID: string(sess.ID),
		CallID:    string(c.ID),
	}
	outcome, herr := e.deps.Hooks.Run(ctx, ev)

	// Normalize the hook's producer-influenced output to valid UTF-8 HERE — the
	// one point both values arrive (issue #402). A hook is a subprocess and its
	// Message is its raw stdout (hookexec blockMessage), so it is the same
	// arbitrary-bytes producer as a tool's stdout; Mutated is its rewritten args
	// JSON, and json.Valid ACCEPTS invalid UTF-8 inside a string literal, so a
	// malformed payload would be adopted into c.Args verbatim. Neither value
	// passes through execute, so RepairToolResult never sees them: without this,
	// a blocked call's ToolError and a mutated call's Args reach the recorded
	// conversation RAW while the client stream, the model view and the snapshot
	// each get U+FFFD from a DIFFERENT mechanism (the mapper backstop, the
	// provider's JSON marshal, encoding/json on Save) — the invariant holding by
	// coincidence rather than construction. Repairing inside the JSON leaves it
	// valid JSON: it only rewrites bytes within string literals, exactly what
	// json.Unmarshal would have substituted on decode anyway.
	outcome.Message = session.ToValidUTF8(outcome.Message)
	if len(outcome.Mutated) > 0 {
		outcome.Mutated = json.RawMessage(session.ToValidUTF8(string(outcome.Mutated)))
	}

	if herr != nil {
		if ctx.Err() != nil {
			return preHookResult{effective: c}, ctx.Err()
		}
		// A hook execution error is surfaced to the model as a block annotation
		// rather than aborting the whole run. The error can wrap the hook's own
		// stderr, so it gets the same repair as Message above.
		return preHookResult{blocked: true, msg: session.ToValidUTF8(fmt.Sprintf("PreToolUse hook error: %v", herr))}, nil
	}
	if outcome.Block {
		m := outcome.Message
		if m == "" {
			m = "blocked by PreToolUse hook"
		}
		// AskApproval refines the block into an approval ask — but ONLY when a human
		// approver is attached. A headless engine DEGRADES to a terminal block here
		// (fail-safe), so callers never see askApproval true without an interactive
		// engine and the block annotation still fires for the operator/client.
		if outcome.AskApproval && e.deps.Interactive {
			return preHookResult{askApproval: true, msg: m, effective: c}, nil
		}
		e.emit(r, session.Event{Type: session.EvHook, Turn: turnIdx, Text: m,
			Hook: &session.HookPayload{Phase: string(governance.PhasePreToolUse), Tool: c.Name, Decision: session.HookBlocked, CallID: c.ID}})
		return preHookResult{blocked: true, msg: m}, nil
	}
	if len(outcome.Mutated) > 0 {
		// Apply the mutation: the payload is the rewritten args JSON. Validate it as
		// JSON before adopting it; a malformed payload is ignored. The permission
		// decision is NOT re-evaluated on these args — see the trust note above.
		if json.Valid(outcome.Mutated) {
			e.emit(r, session.Event{Type: session.EvHook, Turn: turnIdx, Text: "PreToolUse hook rewrote tool arguments for " + c.Name,
				Hook: &session.HookPayload{Phase: string(governance.PhasePreToolUse), Tool: c.Name, Decision: session.HookModified, CallID: c.ID}})
			return preHookResult{effective: session.NewToolCall(c.ID, c.Name, outcome.Mutated)}, nil
		}
		e.emit(r, session.Event{Type: session.EvHook, Turn: turnIdx, Text: "PreToolUse hook returned a malformed argument mutation (ignored)",
			Hook: &session.HookPayload{Phase: string(governance.PhasePreToolUse), Tool: c.Name, Decision: session.HookInfo, CallID: c.ID}})
	}
	if outcome.Message != "" && !outcome.Block && len(outcome.Mutated) == 0 {
		// An advisory (message-only) outcome: the call proceeds unchanged, but the
		// hook flagged content — surface a client-visible EvHook (model-invisible).
		e.emit(r, session.Event{Type: session.EvHook, Turn: turnIdx, Text: outcome.Message,
			Hook: &session.HookPayload{Phase: string(governance.PhasePreToolUse), Tool: c.Name, Decision: session.HookAdvisory, CallID: c.ID}})
	}
	return preHookResult{effective: c}, nil
}

// askHookApproval surfaces a hook-originated PreToolUse block to the human as a
// permission ask, mirroring authorize's ask block but keyed on the HOOK's reason
// rather than a policy decision. It mints an askID, builds a HookOriginated
// PendingAsk, registers the resolution channel, PauseForApproval → StateAwaiting,
// emits EvPermissionAsk, and blocks on the verdict. On allow it executes the call
// DIRECTLY (it does NOT re-run preHook — the human already authorized THIS blocked
// call; re-running would re-block/re-ask); on AllowAlways it ALSO arms the optional
// HookApprovalLearner so a later identical block does not re-ask. On deny/cancel it
// synthesizes a deny/aborted result. It returns the result and a cancelled flag set
// only when ctx was cancelled while awaiting.
//
// It is sequenced one-at-a-time exactly like a policy ask (runReadBatch resolves it
// in Phase 1, never from the parallel fan-out), so two asks never surface at once.
func (e *Engine) askHookApproval(ctx context.Context, r *Run, sess *session.Session, env tool.Environment, turnIdx int, c session.ToolCall, t tool.Tool, reason string) (session.ToolResult, bool) {
	ask := session.PendingAsk{
		AskID:          newAskID(sess.ID, sess.Counters.ToolCalls, c.ID, r.askDiscriminator),
		Tool:           c.Name,
		Args:           c.Args,
		Reason:         reason,
		Call:           c.ID,
		HookOriginated: true,
	}

	// Run the SHARED ask spine (register → pause → emit → await → resume → EvApproval);
	// only the verdict→action tail below is hook-specific.
	verdictResult, ok, paused := e.surfaceAsk(ctx, r, sess, turnIdx, ask)
	if !paused {
		return session.NewToolError(c.ID, "internal: cannot pause for guardrail approval"), false
	}
	if !ok {
		// ctx cancelled while awaiting.
		return session.ToolResult{}, true
	}
	verdict := verdictResult.verdict

	switch verdict {
	case session.VerdictAllowAlways:
		// Arm the optional waiver (HookApprovalLearner) so a later identical block in
		// this session does not re-ask. The engine stays generic: it hands the neutral
		// HookEvent (SessionID/Tool/Input) to the consumer, which decides the scope.
		if learner, lok := e.deps.Hooks.(port.HookApprovalLearner); lok {
			learner.LearnHookApproval(ctx, governance.HookEvent{
				Phase:     governance.PhasePreToolUse,
				Tool:      c.Name,
				Input:     c.Args,
				SessionID: string(sess.ID),
				CallID:    string(c.ID),
			})
		}
		// Execute DIRECTLY — do NOT re-run preHook (the human authorized this call).
		var enqueue time.Time
		if e.deps.Clock != nil {
			enqueue = e.deps.Clock.Now()
		}
		return e.execute(ctx, r, sess, env, turnIdx, c, t, enqueue), false
	case session.VerdictAllowOnce:
		var enqueue time.Time
		if e.deps.Clock != nil {
			enqueue = e.deps.Clock.Now()
		}
		return e.execute(ctx, r, sess, env, turnIdx, c, t, enqueue), false
	default:
		// VerdictDeny (incl. the zero value / fail-safe). Operator visibility: a HUMAN
		// denied a guardrail block — a distinct, grep-able record from a checker
		// auto-block (which the modelhook adapter logs) and from the AllowAlways
		// `guardrail-waived` line. It is a dispatch-time INFO, the sibling of authorize's
		// policy-deny INFO (both record a call resolved against execution at the
		// resolution site), NOT a new per-loop line. The model also sees the guardrail
		// reason verbatim so it can re-route.
		r.diag.Log(ctx, port.LevelInfo, "guardrail block denied by human",
			"tool", c.Name, "turn", turnIdx, "decision", "deny")
		res := session.NewToolError(c.ID, reason)
		e.emit(r, session.Event{Type: session.EvToolResult, Turn: turnIdx, ToolResult: ptr(res)})
		return res, false
	}
}

// surfacePlanAsk surfaces a plan-approval ask (the operator is asked to approve a
// presented plan) to the human, mirroring askHookApproval but keyed on the
// PresentPlan signalling tool rather than a PreToolUse hook block. It mints an
// askID, builds a PlanOriginated PendingAsk, registers the resolution channel,
// PauseForApproval → StateAwaiting, emits EvPermissionAsk, and blocks on the
// verdict. On Allow it does NOT execute anything (PresentPlan is a signalling
// affordance, not a mutating tool): it sets r.planApprovedTarget (AllowOnce →
// ModeDefault, AllowAlways → ModeAccept) and synthesizes an allow result; the
// runLoop then terminates the run with StopPlanApproved and terminateComplete
// flips the session mode at the terminal boundary. On Deny it sets
// r.planIterateRequested and synthesizes a deny result; the runLoop then
// terminates the run CLEANLY with StopPlanIterate so the operator's next typed
// prompt drives the revision (the model does NOT continue in-turn). On cancel it
// returns a zero result + cancelled=true.
//
// It takes no Workspace/Tool param (unlike askHookApproval): PresentPlan is
// signalling-only, so nothing is executed and there is no tool handle to run. It
// is sequenced one-at-a-time exactly like a policy/hook ask (runReadBatch resolves
// it in Phase 1, runOne on the mutate-serial path), never from the parallel
// fan-out, so two asks never surface at once. The tool card is opened by the
// CALLER before routing here (the card-before-the-gate invariant), exactly as
// askHookApproval relies on its caller's openCard. It emits NO diagnostics line
// (the loop's "exactly THREE lines" invariant holds).
func (e *Engine) surfacePlanAsk(ctx context.Context, r *Run, sess *session.Session, turnIdx int, c session.ToolCall) (session.ToolResult, bool) {
	// Headless degrade (mirrors preHook's askable-block degrade, ADR 0062): a
	// non-interactive engine has NO human to approve a plan, so it must NOT surface
	// an ask (a headless run never emits EvPermissionAsk). Fail safe: synthesize a
	// deny result teaching the model the plan was not approved, and do NOT flip the
	// mode or set planApprovedTarget. The loop continues in plan mode so the model
	// can iterate or end the turn; it must NOT auto-approve (no silent mode flip).
	//
	// EXCEPTION (issue #206 Wave 6a): PlanModeAutoApprove SURFACES the plan ask
	// even when headless so the composition Service layer can auto-approve it
	// (an operator deployment decision). A non-plan ask is still headless-auto-
	// denied — this gate ONLY opens for PresentPlan. DEFAULT false keeps the
	// existing headless deny behaviour byte-identical.
	if !e.deps.Interactive && !e.deps.PlanModeAutoApprove {
		res := session.NewToolError(c.ID, "plan not approved: plan approval requires an interactive operator")
		e.emit(r, session.Event{Type: session.EvToolResult, Turn: turnIdx, ToolResult: ptr(res)})
		return res, false
	}
	ask := session.PendingAsk{
		AskID:          newAskID(sess.ID, sess.Counters.ToolCalls, c.ID, r.askDiscriminator),
		Tool:           presentPlanToolName,
		Args:           c.Args,
		Reason:         "plan ready for operator approval",
		Call:           c.ID,
		PlanOriginated: true,
	}

	// Run the SHARED ask spine (register → pause → emit → await → resume → EvApproval);
	// only the verdict→action tail below is plan-specific.
	verdictResult, ok, paused := e.surfaceAsk(ctx, r, sess, turnIdx, ask)
	if !paused {
		return session.NewToolError(c.ID, "internal: cannot pause for plan approval"), false
	}
	if !ok {
		// ctx cancelled while awaiting.
		return session.ToolResult{}, true
	}
	verdict := verdictResult.verdict

	switch verdict {
	case session.VerdictAllowAlways, session.VerdictAllowOnce:
		// Flip to the verdict's target mode (AllowAlways→ModeAccept,
		// AllowOnce→ModeDefault) at the terminal boundary; the run ends with
		// StopPlanApproved and the loop does NOT re-call the model.
		r.planApprovedTarget = planApprovedTargetForVerdict(verdict)
		res := session.NewToolResult(c.ID, "plan approved by operator: proceeding to execution")
		e.emit(r, session.Event{Type: session.EvToolResult, Turn: turnIdx, ToolResult: ptr(res)})
		return res, false
	default:
		// VerdictDeny (incl. the zero value / fail-safe): the operator chose to
		// iterate. Set r.planIterateRequested so the runLoop Step 6 check (or the
		// EARLY check on the resume path) terminates the run CLEANLY with
		// StopPlanIterate — the run ENDS so the operator's next typed prompt drives
		// the revision (the model does NOT keep iterating with no operator input).
		// The session stays ModePlan (terminateComplete only flips when
		// planApprovedTarget != ""). The deny result teaches the model the turn is
		// pausing for operator feedback.
		r.planIterateRequested = true
		res := session.NewToolError(c.ID, "plan not approved by operator: the operator will provide feedback; end this turn and wait for it")
		e.emit(r, session.Event{Type: session.EvToolResult, Turn: turnIdx, ToolResult: ptr(res)})
		return res, false
	}
}

// planApprovedTargetForVerdict maps a plan-approval verdict to the permission
// mode the session flips into at the terminal boundary:
// VerdictAllowAlways → ModeAccept, VerdictAllowOnce → ModeDefault. It is the
// shared verdict→mode mapping used by BOTH the live plan-ask surface
// (surfacePlanAsk) and the cross-process plan-ask resume
// (resolvePendingCall's PlanOriginated branch), so the target_mode an operator
// picks (or the auto-approve default) drives the EXACT mode the session lands
// in on both paths. (The inverse target_mode→verdict mapping lives in
// composition: service.go's planVerdictForMode — a different direction.)
func planApprovedTargetForVerdict(v session.ApprovalVerdict) session.PermissionMode {
	if v == session.VerdictAllowAlways {
		return session.ModeAccept
	}
	return session.ModeDefault
}

// execute runs the tool against the workspace, times it, runs the PostToolUse
// hook, then logs and emits the effective result.
//
// The EvToolCall "open card" event is NOT emitted here — it is emitted by openCard
// BEFORE the permission/hook gate (in runReadBatch Phase 1 and runOne), so a
// synthesized failure on the gated paths (a deny result or a PreToolUse veto) lands
// on a card the client has already opened. execute is only ever reached AFTER the
// gate, so the card always exists by the time the result is emitted.
//
// Ordering note: PostToolUse runs BEFORE the result is logged, emitted, or
// returned, so a PostToolUse result mutation is reflected uniformly — the
// EFFECTIVE (possibly rewritten) result is what the audit Logger records, what the
// client sees on the event stream, AND what the loop records for the model
// (RecordToolResults records exactly what this returns). There is deliberately no
// divergence between the three views; in particular a redacting hook's redaction
// reaches the audit log too rather than leaking the raw output. PostToolUse remains
// otherwise best-effort: a hook execution error does not abort, and a block only
// annotates (the tool already ran; a block neither undoes nor suppresses the
// result).
func (e *Engine) execute(ctx context.Context, r *Run, sess *session.Session, env tool.Environment, turnIdx int, c session.ToolCall, t tool.Tool, enqueue time.Time) session.ToolResult {
	// Execution begins now. queued is the wait from enqueue (when the call entered
	// dispatch) to this point — for a mutating call serialized behind an earlier
	// tool, or any call held behind a permission ask, this is the real queue time
	// (perf-observability §5 decision 2). The same execStart anchors the execution
	// duration, so queued + took partition the wall time from enqueue to result.
	var execStart time.Time
	var queued time.Duration
	if e.deps.Clock != nil {
		execStart = e.deps.Clock.Now()
		if !enqueue.IsZero() {
			queued = execStart.Sub(enqueue)
		}
	}

	var authorityResult *session.ToolResult
	if result, checked := e.authorizeExecution(ctx, r, sess, env, turnIdx, c); checked {
		authorityResult = &result
	}

	var res session.ToolResult
	var dur time.Duration
	if authorityResult != nil {
		res = *authorityResult
	} else if _, ok := t.(*tool.Search); ok {
		if authority, bound := sess.BoundAuthority(); bound {
			res = authorityToolSearch(c, e.deps.Catalog, authority.CapabilitySet)
		} else {
			res, dur = e.timeExecute(ctx, r, sess, env, turnIdx, c, t, execStart)
		}
	} else {
		res, dur = e.timeExecute(ctx, r, sess, env, turnIdx, c, t, execStart)
	}

	// PostToolUse may rewrite the result. The effective (possibly rewritten) result
	// is what we log, emit, and return, so the audit log, the client event stream,
	// and the model's recorded history all agree — in particular, a redacting hook's
	// redaction reaches the audit log too rather than leaking the raw tool output.
	res = e.postHook(ctx, r, sess, turnIdx, c, res)

	// Normalize the effective result to valid UTF-8 BEFORE it is logged,
	// emitted, or recorded (issue #402): a tool can hand back arbitrary bytes
	// (a command's stdout, a file's contents, an MCP server's text), and a
	// protobuf string field rejects invalid UTF-8 at marshal time, killing the
	// Converse stream. Repairing here — after PostToolUse, before the three
	// consumers — keeps the recorded == streamed == model-view invariant: all
	// three carry the SAME repaired text. The protobuf mapper keeps its own
	// backstop for producers that never pass through execute; this is the
	// semantic repair, not the last-resort one.
	res = session.RepairToolResult(res)

	if e.deps.ToolCallRecorder != nil {
		e.deps.ToolCallRecorder.ToolCall(sess.ID, c, res, queued, dur)
	}

	e.emit(r, session.Event{Type: session.EvToolResult, Turn: turnIdx, ToolResult: ptr(res)})
	return res
}

const callMcpWithQueryToolName = "CallMcpWithQuery"

// authorizeExecution is the single authority-enforcement boundary. Permission
// and hook gates decide whether a call may reach execution; a bound session's
// carried authority independently decides which exact tool it may execute.
func (e *Engine) authorizeExecution(ctx context.Context, r *Run, sess *session.Session, env tool.Environment, turnIdx int, call session.ToolCall) (session.ToolResult, bool) {
	authority, bound := sess.BoundAuthority()
	if !bound {
		return session.ToolResult{}, false
	}
	if e.deps.AuthorityEvaluator == nil {
		return session.NewToolError(call.ID, fmt.Sprintf("tool %q was not executed: authority evaluator is not configured", call.Name)), true
	}

	target, err := authorityTarget(call, authority.CapabilitySet)
	if err != nil {
		return session.NewToolError(call.ID, fmt.Sprintf("tool %q denied by authority: %v", call.Name, err)), true
	}
	resource, err := authorityResource(call, env)
	if err != nil {
		return session.NewToolError(call.ID, fmt.Sprintf("tool %q denied by authority: %v", target, err)), true
	}
	ownerIssuer := ""
	ownerSubject := ""
	if sess.Owner != nil {
		ownerIssuer = sess.Owner.Issuer
		ownerSubject = sess.Owner.Subject
	}
	if requirement, required := e.deps.AuthorityEvaluator.(port.AuthorityOwnerRequirement); required && requirement.RequiresOwnerIdentity() && (ownerIssuer == "" || ownerSubject == "") {
		return session.NewToolError(call.ID, fmt.Sprintf("tool %q denied by authority: owner identity is unavailable", target)), true
	}
	request := port.AuthorityRequest{
		CapabilitySet:   authority.CapabilitySet,
		ToolName:        target,
		Action:          call.Name,
		DelegationDepth: authority.CapabilitySet.RemainingDelegationDepth,
		Principal: port.AuthorityPrincipal{
			Definition:   authorityDefinition(authority),
			Instance:     string(sess.ID),
			OwnerIssuer:  ownerIssuer,
			OwnerSubject: ownerSubject,
		},
		Resource: resource,
	}
	decision, err := e.deps.AuthorityEvaluator.AuthorizeTool(ctx, request)
	if err != nil {
		r.diag.Log(ctx, port.LevelWarn, "authority evaluator unavailable", "tool", target, "turn", turnIdx, "err", err)
		return session.NewToolError(call.ID, fmt.Sprintf("tool %q was not executed: authority evaluator unavailable", target)), true
	}
	if !decision.Allowed {
		reason := decision.Reason
		if reason == "" {
			reason = "authorization denied"
		}
		return session.NewToolError(call.ID, fmt.Sprintf("tool %q denied by authority: %s", target, reason)), true
	}
	return session.ToolResult{}, false
}

func authorityDefinition(authority session.Authority) string {
	if authority.DefinitionIdentity != "" {
		return authority.DefinitionIdentity
	}
	return "root"
}

// authorityTarget translates meta-tools whose actual reach is named in their
// arguments. Resource operations authorize the server's opaque derived capability,
// while retaining their actual meta-tool name as the evaluator action.
func authorityTarget(call session.ToolCall, _ governance.CapabilitySet) (string, error) {
	switch call.Name {
	case callMcpWithQueryToolName:
		var args struct {
			Server string `json:"server"`
			Tool   string `json:"tool"`
		}
		if err := json.Unmarshal(call.Args, &args); err != nil || strings.TrimSpace(args.Server) == "" || strings.TrimSpace(args.Tool) == "" {
			return "", errors.New("CallMcpWithQuery target is invalid")
		}
		return "mcp__" + strings.TrimSpace(args.Server) + "__" + strings.TrimSpace(args.Tool), nil
	case "ListMcpResources", "ReadMcpResource":
		var args struct {
			Server string `json:"server"`
		}
		if err := json.Unmarshal(call.Args, &args); err != nil || strings.TrimSpace(args.Server) == "" {
			return "", errors.New("MCP resource target is invalid")
		}
		return governance.MCPResourceCapability(strings.TrimSpace(args.Server)), nil
	default:
		return call.Name, nil
	}
}

func authorityResource(call session.ToolCall, env tool.Environment) (*port.AuthorityResource, error) {
	switch call.Name {
	case "Read", "Edit", "Write":
		path, err := authorityPath(call.Args)
		if err != nil {
			return nil, err
		}
		return authorityWorkspaceResource(path, env)
	default:
		return nil, nil
	}
}

// authorityPath reads only the path field from the known local-file tool shapes.
// It refuses duplicate, missing, non-string, or trailing values so an evaluator
// never receives a target selected from ambiguous JSON.
func authorityPath(args json.RawMessage) (string, error) {
	decoder := json.NewDecoder(strings.NewReader(string(args)))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return "", errors.New("local resource arguments are invalid")
	}

	var path string
	pathCount := 0
	for decoder.More() {
		key, err := decoder.Token()
		if err != nil {
			return "", errors.New("local resource arguments are invalid")
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return "", errors.New("local resource arguments are invalid")
		}
		if key != "path" {
			continue
		}
		pathCount++
		if pathCount != 1 || json.Unmarshal(value, &path) != nil {
			return "", errors.New("local resource path is ambiguous")
		}
	}
	if token, err := decoder.Token(); err != nil || token != json.Delim('}') {
		return "", errors.New("local resource arguments are invalid")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return "", errors.New("local resource arguments are invalid")
	}
	if pathCount != 1 || path == "" || len(path) > 4096 || strings.IndexByte(path, 0) >= 0 {
		return "", errors.New("local resource path is invalid")
	}
	return path, nil
}

// authorityWorkspaceResource delegates physical identity derivation to the live
// workspace so the policy descriptor names the same confined target filesystem
// access will use. A bound authority must not fall back to lexical normalization:
// an unavailable or ambiguous resolver fails the call closed.
func authorityWorkspaceResource(path string, env tool.Environment) (*port.AuthorityResource, error) {
	resolver, ok := env.Workspace().(tool.AuthorityResourceResolver)
	if !ok {
		return nil, errors.New("session workspace cannot derive an authority resource identity")
	}
	target, workspace, err := resolver.AuthorityResourcePath(path)
	if err != nil {
		return nil, fmt.Errorf("derive authority resource identity: %w", err)
	}
	if target == "" || workspace == "" || !filepath.IsAbs(target) || !filepath.IsAbs(workspace) {
		return nil, errors.New("session workspace returned an invalid authority resource identity")
	}
	return &port.AuthorityResource{
		Kind:      port.AuthorityResourceWorkspaceFile,
		Path:      filepath.ToSlash(target),
		Workspace: filepath.ToSlash(workspace),
	}, nil
}

// timeExecute runs the tool and reports its elapsed wall time as (Clock.Now −
// start), where start is the execution-start anchor the caller already read from
// the injected Clock (so queued and took share one clock read and never
// double-advance a fake clock). The duration is 0 when no Clock is configured. A
// harness-level execution error becomes an error ToolResult so the model can
// recover; the loop never aborts on a single tool failure.
//
// Observability seam: a tool that implements observableTool (the Subagent tool)
// is run via ExecuteObserved with an emit closure bound to THIS run, so it can
// forward a redacted, metadata-only projection of its internal activity (the
// subagent.* events) onto the same sequenced event stream the loop emits. The
// closure stamps the current Turn and routes through e.emit (Seq + sink mirror),
// matching every other dispatch emit. It is invoked from the (possibly
// concurrent, read-parallel) tool goroutine — consistent with the existing
// dispatch emits, which e.emit serialises. Tools that do not implement the seam
// take the ordinary Execute path unchanged.
func (e *Engine) timeExecute(ctx context.Context, r *Run, sess *session.Session, env tool.Environment, turnIdx int, c session.ToolCall, t tool.Tool, start time.Time) (session.ToolResult, time.Duration) {
	var (
		res session.ToolResult
		err error
	)
	// CHILD emits route through the registry's seal guard (A4c): a background
	// child's goroutine outlives its dispatch slot and may emit subagent.tool/end
	// while the loop is terminating — safeEmit makes a post-seal emit a silent
	// no-op (never a send-on-closed-channel panic) and an in-drain emit a
	// give-up-at-seal send (Run.emitOrAbort, bound in Engine.Run), which
	// still sequences + sink-mirrors a delivered event exactly like e.emit.
	emit := func(ev session.Event) {
		ev.Turn = turnIdx
		r.children.safeEmit(ev)
	}
	switch ct := t.(type) {
	case childCapableTool:
		// A subagent-spawning tool (Subagent/Team/Fork) also receives the parent's caps so a
		// child's permission ask can be SURFACED to the human (interactive) or auto-denied
		// with the accurate message + operator diagnostic (headless). The surface seam is
		// bound to THIS parent Run (register-then-emit), symmetric to the emit closure.
		res, err = ct.ExecuteWithParent(ctx, c, env, emit, e.parentCaps(r, sess, turnIdx))
	case observableTool:
		res, err = ct.ExecuteObserved(ctx, c, env, emit)
	default:
		res, err = t.Execute(ctx, c, env)
	}
	if err != nil {
		res = session.NewToolError(c.ID, fmt.Sprintf("tool %q failed: %v", c.Name, err))
	}
	var dur time.Duration
	if e.deps.Clock != nil {
		dur = e.deps.Clock.Now().Sub(start)
	}
	return res, dur
}

// parentCaps builds the parent-capability bundle threaded into a subagent-spawning
// tool (childCapableTool). It exposes the parent run's interactivity (a non-nil router
// ⇔ an interactive engine installed one in Engine.Run) plus the register-then-emit
// surface seam: surfaceAsk registers the child Run in this parent's router and then
// emits a REDACTED parent EvPermissionAsk for the child's ask, so the existing client
// approval UI + ResumeApproval→Run.Approve routing resolve it and the verdict routes
// back to the child. The surfaced ask carries a CLAMPED command (clampPreview) framed as
// a subagent request — peer-injected/untrusted args in a member command never ride raw
// (gauntlet #7: an ASK with a tool name + clamped command + static-framed reason, never
// transcript content). diag is the parent run's run-scoped diagnostics for the headless
// auto-deny operator line.
func (e *Engine) parentCaps(r *Run, sess *session.Session, turnIdx int) parentCaps {
	interactive := r.childAsks != nil
	caps := parentCaps{
		interactive: interactive,
		diag:        r.diag,
		// The run's explicit unwedge signal (fired hardAbortGrace after Run.Cancel)
		// rides down so a delegation tool's internal forwarding sends can give up
		// alongside the run's own guarded emits — see parentCaps.hardAbort.
		hardAbort: r.hardAbort,
		// The child-run registry is handed down DIRECTLY and UNCONDITIONALLY (it
		// exists on every run; cancel frames arrive only on interactive surfaces but
		// the bookkeeping — and the clientCancelled read — must work headless too).
		// It is an agent-package handle, so no layering rule is crossed; the spawning
		// tools go through parentCaps' nil-safe wrappers.
		children: r.children,
	}
	// fork:true Subagent context inheritance (issue #34): hand down a closure that
	// takes a DEEP COPY of the PARENT conversation, trailing-fork-call orphan
	// stripped, so a fork child can be seeded with the parent's full context. It is
	// captured over THIS run's session and read SYNCHRONOUSLY on the dispatch
	// goroutine while the conversation is stable (Subagent's run()/startBackground
	// snapshot it before any detach), never inside a detached background goroutine.
	// nil when no parent session is threaded (plain Execute) — fork then unsupported.
	if sess != nil {
		caps.forkHistory = func() []session.Message {
			return session.ForkSnapshot(sess.Conversation)
		}
		// The parent session's owner (ADR 0204 decision 4) rides down so every
		// child session it spawns is attributed to the SAME principal. Read off
		// the aggregate, never off the ambient context — see parentCaps.owner.
		caps.owner = sess.Owner
		caps.authority, caps.authorityBound = sess.BoundAuthority()
		// The parent session's OWN id rides down too (review finding 2, issue
		// #368) so every derived child/branch/member id is namespaced under a
		// value that is already collision-safe across owners — see
		// parentCaps.parentSessionID.
		caps.parentSessionID = sess.ID
		caps.parentIncarnation = sess.Incarnation()
	}
	// The OPT-IN headless ask reviewer: bind the engine's reviewer + THIS run's
	// breaker + the timeout into a closure resolveChildAsk consults on the headless
	// branch only. The breaker mutex is held across the whole Review, deliberately
	// SERIALIZING reviews within the run (deterministic consecutive-failure
	// semantics; a concurrent child fan-out cannot multiply reviewer spend). The
	// askReview-non-nil pairing is guaranteed by Engine.Run (created iff the
	// reviewer is wired); the double check is belt-and-braces for a Run built
	// outside it (unit tests).
	if e.deps.ChildAskReviewer != nil && r.askReview != nil {
		reviewer, breaker, hardAbort := e.deps.ChildAskReviewer, r.askReview, r.hardAbort
		caps.adjudicate = func(ask session.PendingAsk, isolated bool) askReviewOutcome {
			breaker.mu.Lock()
			defer breaker.mu.Unlock()
			if breaker.consecutiveDenies >= breaker.max {
				return askReviewOutcome{} // breaker open: not reviewed, plain auto-deny (no repeat INFO)
			}
			// A run already past Cancel+grace is tearing down: treat it like an open
			// breaker rather than spending a reviewer turn whose child is about to be
			// unwound anyway. A nil channel never selects — correct no-abort behaviour
			// for a zero-caps construction.
			select {
			case <-hardAbort:
				return askReviewOutcome{}
			default:
			}
			// A fresh background context + timeout: resolveChildAsk runs on the drain
			// path with no ctx of its own, and the parked child must not hang on a
			// wedged reviewer. A timed-out review is a failure → fall-through deny.
			ctx, cancel := context.WithTimeout(context.Background(), askReviewTimeout)
			defer cancel()
			review, err := reviewer.Review(ctx, ChildAskReviewRequest{Ask: ask, Isolated: isolated})
			switch {
			case errors.Is(err, ErrNotReviewable):
				// ABSTENTION: the reviewer cannot judge THIS ask. Fall through to the
				// plain auto-deny WITHOUT touching the breaker — a reviewer that
				// abstains on some tools must never silence review for the ones it can
				// judge. No reviewer-failure INFO (it is a deliberate decline, not a fault).
				return askReviewOutcome{}
			case err != nil:
				return askReviewOutcome{failed: true, failReason: err.Error(),
					breakerJustOpened: noteBreakerFailure(breaker)}
			case review.Allowed:
				breaker.consecutiveDenies = 0
				return askReviewOutcome{reviewed: true, allowed: true, reason: review.Reason}
			default:
				out := askReviewOutcome{reviewed: true, reason: review.Reason,
					breakerJustOpened: noteBreakerFailure(breaker)}
				return out
			}
		}
	}
	// The OPT-IN semantic model router (ADR 0031): bind the engine's router closure +
	// THIS run's router breaker into a routeTask the Subagent run() hook consults for a
	// plain default delegation. The breaker mutex is held across the whole
	// classification, SERIALIZING classifications within the run (deterministic
	// consecutive-miss semantics; a concurrent Subagent fan-out cannot multiply
	// classifier spend). The router-non-nil ⇔ breaker pairing is guaranteed by
	// Engine.Run (the breaker is created iff the router is wired); the double check
	// is belt-and-braces for a Run built outside it (unit tests). FAIL-SOFT throughout:
	// any miss (classifier failure, unknown category, breaker open) returns ok=false and
	// the caller inherits the default explorer model.
	//
	// CLASSIFIER SPEND ACCOUNTING (#92 fix): after every route() call — hit OR miss —
	// the classifier's usage is folded into sess.Usage UNCONDITIONALLY before the
	// miss/hit branches. This makes the single budget brake authority (budgetExhausted
	// reads sess.Usage.TotalTokens()) cover classifier spend, preventing CWE-770
	// unbounded accumulation. The fold is SYNCHRONOUS on this dispatch goroutine
	// (sess is StateRunning here; RecordUsage is legal). The error is swallowed (`_ =`)
	// as defense-in-depth: a guard error means a best-effort undercount (tolerable),
	// never a correctness fault — mirroring loop.go's own `_ = sess.RecordUsage(usage)`.
	// The two diagnostics below (breaker-OPEN INFO and "subagent routed" INFO) are
	// emitted from THIS dispatch-path closure, NOT the resolveChildAsk child chokepoint
	// (the loop still emits exactly THREE lines; the router INFOs are dispatch-time
	// lines, like the policy-deny INFO).
	if e.deps.SubagentModelRouter != nil && r.router != nil {
		// Pre-build a nil-safe usage-fold func to keep the closure branch-free (#92 fix,
		// avoids +1 cyclomatic complexity inside the already-branchy closure).
		foldUsage := foldClassifierUsage(sess)
		// The classification body lives in a package-level func (routeTaskBody) so this
		// already-branchy constructor stays under the gocyclo budget; the closure here is
		// a one-line adapter capturing the run-scoped breaker/hardAbort/diag/foldUsage.
		route, breaker, hardAbort, diag := e.deps.SubagentModelRouter, r.router, r.hardAbort, r.diag
		caps.routeTask = func(ctx context.Context, taskPrompt string) (string, string, string, bool) {
			return routeTaskBody(ctx, taskPrompt, route, breaker, hardAbort, diag, foldUsage)
		}
	}
	if interactive {
		caps.surfaceAsk = func(askID, childID string, child *Run, ask session.PendingAsk, requesterLabel string) {
			// Register BEFORE emitting so a fast ResumeApproval cannot race ahead of
			// registration (mirror askRegistry.register-before-emit).
			r.registerChildAsk(askID, child)
			// Record ask OWNERSHIP at this single surfacing seam (all three delegation
			// families thread their child session id through childPosture.childID), so
			// a later CancelChild can retract the parked ask. recordAsk no-ops for an
			// id the registry does not hold (team/parallel until they register).
			r.children.recordAsk(childID, askID)
			// requester ATTRIBUTES the ask to its delegation (subagent goal / team member
			// name / parallel branch label). It is clamped metadata (clampPreview neutralises
			// C0/C1 + rune-caps), already composed at the child posture; an empty label keeps
			// today's generic "subagent" framing. The raw args are still NOT forwarded
			// (gauntlet #7) — only this static-framed requester + clamped command preview.
			requester := "subagent"
			if requesterLabel != "" {
				requester = clampPreview(requesterLabel)
			}
			// The policy REASON is harness/policy-authored metadata (e.g. "approval
			// required by rule for Bash (go test*)" for a configured subagent: ask,
			// vs the substitution-floor message) — NOT peer/transcript content, so
			// it is safe to surface (clamped). Appending it lets the operator tell
			// WHY the ask surfaced: their own configured rule vs the substitution
			// floor, which the command preview alone cannot distinguish. Raw args
			// stay dropped (gauntlet #7).
			surfaced := session.PendingAsk{
				AskID: askID,
				Tool:  ask.Tool,
				// Propagate the child's gated tool-call id (issue #148): the child
				// engine's own authorize populated ask.Call, so the surfaced ask
				// carries the same opaque correlation id (no new leak — see
				// PendingAsk.Call).
				Call: ask.Call,
				// Clamp/redact the command: a subagent's Bash args can contain peer-injected
				// untrusted text. Frame it explicitly as a quoted subagent REQUEST so the
				// human reads it as "the subagent wants to run X", never as a trusted
				// instruction. The original args are NOT forwarded.
				Args: nil,
				Reason: fmt.Sprintf("%s requests approval to run %s: %s (%s)",
					requester, ask.Tool, clampPreview(surfacedCommandPreview(ask)), clampPreview(ask.Reason)),
			}
			// CHILD-originated emit: through the seal guard (A4c) — a BACKGROUND
			// child can surface an ask from its own goroutine at any point, including
			// while the run terminates; post-seal this is a safe no-op (the child's
			// parked await then unwinds via its cancelled ctx, not a verdict).
			r.children.safeEmit(session.Event{Type: session.EvPermissionAsk, Turn: turnIdx, Ask: &surfaced})
		}
	}
	return caps
}

// foldClassifierUsage returns a nil-safe fold function that accumulates a classifier's
// session.Usage into sess (#92 fix, CWE-770): the returned func calls sess.RecordUsage
// unconditionally, swallowing the error as defense-in-depth (a guard error is a
// best-effort undercount, never a correctness fault — mirroring loop.go's own
// `_ = sess.RecordUsage(usage)`). When sess is nil (plain Execute with no parent
// session threaded), the returned func is a no-op, keeping the parentCaps closure
// branch-free (no `if sess != nil` inside the hot routeTask loop).
func foldClassifierUsage(sess *session.Session) func(session.Usage) {
	if sess == nil {
		return func(session.Usage) {} // nil-safe nop: plain Execute, no parent session.
	}
	return func(u session.Usage) {
		_ = sess.RecordUsage(u)
	}
}

// routeTaskBody is the classification half of the parentCaps routeTask closure, factored
// out of parentCaps so that constructor stays under the gocyclo budget (the closure there
// is a one-line adapter capturing the run-scoped breaker/hardAbort/diag/foldUsage). It
// classifies a delegation prompt via the wired SubagentModelRouter, honouring the per-run
// circuit breaker + the run's hardAbort fast-path skip, folding classifier spend into the
// parent session's Usage, and returning the internal reason on a miss (issue #397): the
// classifier's missReason (or "empty-model" for a blank-model "hit"), or a synthesized gate
// constant (RoutingReasonBreakerOpen / RoutingReasonAborted) when the classifier was skipped.
// Empty reason on a routed hit. The full reason reaches operator diagnostics; every event
// emitter reduces it through routingReasonPayload to a static/generic code (gauntlet #7).
// FAIL-SOFT throughout: any miss returns ok=false and the caller inherits the default model.
func routeTaskBody(
	ctx context.Context,
	taskPrompt string,
	route func(context.Context, string) (string, string, session.Usage, string, bool),
	breaker *modelRouterBreaker,
	hardAbort chan struct{},
	diag port.Diagnostics,
	foldUsage func(session.Usage),
) (category, model, reason string, ok bool) {
	breaker.mu.Lock()
	defer breaker.mu.Unlock()
	if breaker.consecutiveMiss >= breaker.max {
		// Breaker open: skip the classifier, inherit the default model. The
		// synthesized reason rides the delegation-start event (issue #397); this
		// path stays diag-SILENT (no classification attempt was made).
		return "", "", session.RoutingReasonBreakerOpen, false
	}
	// A run already past Cancel+grace is tearing down: skip the classifier turn
	// rather than spend one whose child is about to be unwound. A nil channel
	// never selects — correct no-route behaviour for a zero-caps construction.
	select {
	case <-hardAbort:
		return "", "", session.RoutingReasonAborted, false
	default:
	}
	// Pass the run's ctx (not context.Background()) so a Run.Cancel between this
	// check and the classifier call propagates into RunModelRouter and the
	// classifier turn dies with the run instead of running out its 30s clock
	// (issue #94). The hardAbort check above is a fast-path skip; ctx is the
	// race-closing bound. Fail-soft holds: a cancelled ctx → StopCancelled →
	// ok=false → inherit the default model, the existing miss path.
	category, model, classifierUsage, missReason, routeOK := route(ctx, taskPrompt)
	// Fold classifier spend into the parent session's cumulative Usage
	// UNCONDITIONALLY (on both miss and hit paths) so --max-run-tokens bounds
	// the classifier cost (#92 fix). foldUsage is nil-safe (nop when sess==nil).
	foldUsage(classifierUsage)
	if !routeOK || strings.TrimSpace(model) == "" {
		// Per-miss observability (issue #287): log WHY this plain delegation fell
		// through to the inherited default model, at THIS dispatch chokepoint so all
		// three delegation families (subagent/team/parallel) share the line for free.
		// The nil-diag guard + the "empty-model" fallback live in logRouterMissReason
		// so this hot closure stays under the gocyclo budget. It is emitted BEFORE the
		// breaker-open check below; the breaker-open skip and the hardAbort skip above
		// return earlier and stay SILENT deliberately (no classifier call was made, so
		// there is no miss to attribute — only an actual classification attempt logs).
		logRouterMissReason(ctx, diag, missReason)
		if justOpened := noteRouterMiss(breaker); justOpened && diag != nil {
			diag.Log(ctx, port.LevelInfo,
				"subagent model router: breaker OPEN after consecutive misses; remaining subagents this run inherit the default model",
				"threshold", breaker.max)
		}
		// The missReason (or the "empty-model" fallback for a blank-model "hit")
		// passes through as the RoutingReason the delegation-start event carries
		// (issue #397) — bare metadata, same footing as the diag line.
		if missReason == "" {
			missReason = routingReasonEmptyModel
		}
		return "", "", missReason, false
	}
	breaker.consecutiveMiss = 0 // a successful classification resets the breaker.
	if diag != nil {
		diag.Log(ctx, port.LevelInfo,
			"subagent routed", "category", category, "model", model)
	}
	return category, model, "", true
}

// logRouterMissReason emits the per-miss model-router INFO (issue #287) naming WHY a plain
// delegation fell through to the inherited default model. It is factored out of the
// parentCaps routeTask closure so that closure stays under the gocyclo budget. The reason
// is METADATA ONLY — a harness/composition constant, never the task prompt or the
// classifier output (gauntlet #7). A nil diag is a no-op; a blank reason (a route that
// reported ok but a blank model — a defensive belt-and-braces path with no reason of its
// own) is logged as "empty-model".
func logRouterMissReason(ctx context.Context, diag port.Diagnostics, missReason string) {
	if diag == nil {
		return
	}
	if missReason == "" {
		missReason = "empty-model"
	}
	diag.Log(ctx, port.LevelInfo,
		"subagent model router: classification MISSED; child inherits the default model",
		"reason", missReason)
}

// surfacedCommandPreview returns the human-facing preview of a surfaced child ask: for
// a Bash ask, the command string; otherwise the ask reason (already policy-authored, not
// peer content). It is the single text that rides the surfaced EvPermissionAsk and is
// always clampPreview'd by the caller before emission.
func surfacedCommandPreview(ask session.PendingAsk) string {
	if ask.Tool == "Bash" {
		if cmd := bashCmdFromArgs(ask.Args); cmd != "" {
			return cmd
		}
	}
	return ask.Reason
}

// resultPayload is the JSON shape of the PostToolUse hook's view of a tool
// result and the symmetric shape its Mutated payload is interpreted as.
type resultPayload struct {
	Content string `json:"content"`
	IsError bool   `json:"is_error"`
}

// postHookContent returns the string fed to the PostToolUse hook's Content field.
// When res.Parts is non-empty, providers render the MODEL-FACING view from Parts
// (via session.ToolBlockText — the shared projection consumed by both the OpenAI
// and Anthropic adapters), not from the legacy res.Content string; a resource_link
// block's Title/Description, for instance, live ONLY in Parts. Feeding the hook
// only res.Content would leave it blind to that content (CWE-345 / OWASP LLM01: a
// hostile MCP server can smuggle prompt injection in a block Title that reaches the
// model but never reaches the guardrail). So when Parts is non-empty, the hook sees
// the concatenation of ToolBlockText over the blocks instead — the same text the
// model actually sees. Image blocks carry no text projection and are skipped, they
// pose no analogous hidden-text risk. When Parts is empty, res.Content is returned
// unchanged (no behavior change on the legacy string-only path).
//
// This ONLY widens what the hook is shown; it never mutates the recorded
// session.ToolResult, the client event stream, or the model-facing request.
func postHookContent(res session.ToolResult) string {
	if len(res.Parts) == 0 {
		return res.Content
	}
	texts := make([]string, 0, len(res.Parts))
	for _, b := range res.Parts {
		if b.BlockKind == session.BlockImage {
			continue
		}
		texts = append(texts, session.ToolBlockText(b))
	}
	return strings.Join(texts, "\n")
}

// postHook runs the PostToolUse hook best-effort and returns the EFFECTIVE
// result. It carries the result under review as the HookEvent.Input
// ({"content", "is_error"}, plus the call args for context) — Content is
// postHookContent(res), the model-facing Parts projection when Parts is
// non-empty, else the legacy Content string (see postHookContent). A block only
// annotates (the tool already executed; the result is neither undone nor
// suppressed); a hook execution error is ignored — neither aborts the run.
//
// Mutation: a non-empty HookOutcome.Mutated is interpreted SYMMETRICALLY with the
// result the hook saw — the same {"content", "is_error"} object — and, when valid
// JSON, REPLACES the result (a fresh session.ToolResult preserving the CallID,
// via NewToolError when is_error is true else NewToolResult). A malformed (non-
// JSON) payload is ignored (the original result stands) and a notice is emitted.
//
// TRUST: a PostToolUse hook is operator-deployed and trusted, so it may rewrite
// what the model sees the tool returned (e.g. redact secrets from output). Because
// execute emits the EFFECTIVE result, the client stream shows the rewritten result
// too — there is no hidden divergence between the client and model views.
func (e *Engine) postHook(ctx context.Context, r *Run, sess *session.Session, turnIdx int, c session.ToolCall, res session.ToolResult) session.ToolResult {
	if e.deps.Hooks == nil || ctx.Err() != nil {
		return res
	}
	input, _ := json.Marshal(struct {
		Args    json.RawMessage `json:"args"`
		Content string          `json:"content"`
		IsError bool            `json:"is_error"`
	}{Args: c.Args, Content: postHookContent(res), IsError: res.IsError})
	ev := governance.HookEvent{
		Phase:     governance.PhasePostToolUse,
		Tool:      c.Name,
		Input:     input,
		SessionID: string(sess.ID),
		CallID:    string(c.ID),
	}
	outcome, err := e.deps.Hooks.Run(ctx, ev)
	if err != nil {
		// Best-effort: a PostToolUse execution error never aborts and never alters
		// the result.
		return res
	}
	if outcome.Block && outcome.Message != "" {
		// PostToolUse can't veto an already-run tool, but a Block message is the
		// hook flagging the output — surface it as a blocked-severity notice so it
		// reads distinctly from a benign annotation.
		e.emit(r, session.Event{Type: session.EvHook, Turn: turnIdx, Text: outcome.Message,
			Hook: &session.HookPayload{Phase: string(governance.PhasePostToolUse), Tool: c.Name, Decision: session.HookBlocked, CallID: c.ID}})
	}
	if len(outcome.Mutated) > 0 {
		// Apply the mutation: decode the same {"content", "is_error"} shape and
		// rebuild the result via the value-object constructors, preserving CallID.
		if json.Valid(outcome.Mutated) {
			var p resultPayload
			if jerr := json.Unmarshal(outcome.Mutated, &p); jerr == nil {
				e.emit(r, session.Event{Type: session.EvHook, Turn: turnIdx, Text: "PostToolUse hook rewrote the tool result for " + c.Name,
					Hook: &session.HookPayload{Phase: string(governance.PhasePostToolUse), Tool: c.Name, Decision: session.HookModified, CallID: c.ID}})
				if p.IsError {
					return session.NewToolError(res.CallID, p.Content)
				}
				return session.NewToolResult(res.CallID, p.Content)
			}
		}
		e.emit(r, session.Event{Type: session.EvHook, Turn: turnIdx, Text: "PostToolUse hook returned a malformed result mutation (ignored)",
			Hook: &session.HookPayload{Phase: string(governance.PhasePostToolUse), Tool: c.Name, Decision: session.HookInfo, CallID: c.ID}})
	}
	if outcome.Message != "" && !outcome.Block && len(outcome.Mutated) == 0 {
		// An advisory (message-only) outcome: the result is unchanged, but the hook
		// flagged content — surface a client-visible EvHook (model-invisible).
		e.emit(r, session.Event{Type: session.EvHook, Turn: turnIdx, Text: outcome.Message,
			Hook: &session.HookPayload{Phase: string(governance.PhasePostToolUse), Tool: c.Name, Decision: session.HookAdvisory, CallID: c.ID}})
	}
	return res
}

// openCard emits the EvToolCall "open card" event for a call. It is called BEFORE
// the permission/hook gate (not inside execute) so the card exists before any
// synthesized failure — a permission-deny result or a PreToolUse veto — is emitted
// against its id; otherwise a client (e.g. the ACP adapter) would receive a
// failed/error update for a tool_call it never opened and could silently drop it.
//
// The card carries the ORIGINAL call args as received. A PreToolUse hook that
// rewrites the args emits its own HookModified notice; the card is not re-opened
// with the rewritten args (accepted tradeoff: the original args are shown, the
// modified-notice flags the rewrite).
func (e *Engine) openCard(r *Run, turnIdx int, c session.ToolCall) {
	call := c
	e.emit(r, session.Event{Type: session.EvToolCall, Turn: turnIdx, ToolCall: &call})
}

// emit assigns the next Seq via Run.emit, then mirrors the sequenced event to the
// injected EventSink when one is configured. The Run channel is the primary
// surface; the sink is an optional secondary relay.
func (e *Engine) emit(r *Run, ev session.Event) {
	sequenced := r.emit(ev)
	if e.deps.Sink != nil {
		ctx := r.ctx
		if ctx == nil {
			ctx = context.Background()
		}
		e.deps.Sink.Emit(ctx, sequenced)
	}
}

// denyResult synthesizes the error ToolResult that teaches the model why a call
// was refused.
func denyResult(c session.ToolCall, reason string) session.ToolResult {
	if reason == "" {
		reason = "denied by permission policy"
	}
	return session.NewToolError(c.ID, "permission denied: "+reason)
}

// ptr returns a pointer to a copy of v (Events carry pointers to value objects).
func ptr(v session.ToolResult) *session.ToolResult {
	c := v
	return &c
}

// newAskID derives a unique ask id for a permission pause. The leading
// "<sessionID>:" prefix is a CONSUMED CONTRACT, not an implementation detail:
// cmd/mecatui's isChildAsk classifies a surfaced ask as main-agent vs subagent by
// whether the askID is prefixed with the live session id (the child session id IS
// the namespace) — don't change the PREFIX without updating that consumer.
//
// The trailing discriminator component is a SUFFIX (so the consumed prefix
// contract is untouched): it is the host-supplied RunRequest.AskIDDiscriminator
// when set (a durable, cross-process-reconstructable value — ADR-0044), else the
// process-global "r<serial>" fallback resolved in startRun. Either way it makes
// two RUNS of the same session mint disjoint askIDs: without it, cancel a parked
// ask, `resume` the same child id in the same parent run (Counters reset on
// Interrupt), and the provider re-mints the same call id — letting a stale/queued
// ResumeApproval for the RETRACTED ask resolve the NEW one (CWE-863). The serial
// guarantees disjointness automatically; a host-supplied discriminator inherits
// the SAME guarantee via the host contract (unique-per-attempt AND
// stable-per-attempt-across-processes — see RunRequest.AskIDDiscriminator), so a
// replayed old verdict dies as an unknown-ask no-op.
func newAskID(id session.SessionID, n int, callID session.ToolCallID, discriminator string) string {
	return fmt.Sprintf("%s:%d:%s:%s", id, n, callID, discriminator)
}
