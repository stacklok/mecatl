package agent

import (
	"context"
	"encoding/json"
	"time"

	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

// This file wires the run-lifecycle hook phases SessionStart, UserPromptSubmit,
// and Stop into the loop via the existing port.HookRunner seam (Deps.Hooks). The
// per-tool phases (PreToolUse/PostToolUse) live in dispatch.go; SubagentStop
// lives in subagent.go.
//
// A nil Deps.Hooks (the default when no hooks are configured) makes every fire
// site a clean no-op. The hookexec adapter likewise treats an empty phase map as
// "allow", so firing these phases with no configured command never blocks.

// runOwnedHook records returned hook usage while this Engine owns the session.
func (e *Engine) runOwnedHook(ctx context.Context, r *Run, sess *session.Session, ev governance.HookEvent) (governance.HookOutcome, error) {
	result, err := e.deps.Hooks.Run(ctx, ev)
	r.recordAuxiliaryUsage(sess, remapAuxiliaryUsage(ctx, r.diag, session.UsageKindGuardrail, result.AuxiliaryUsage))
	return result.Outcome, err
}

// fireSessionStart fires the SessionStart phase once at the very start of a run,
// before the prompt is recorded. This is a BLOCKING run-level gate, symmetric
// with UserPromptSubmit: a Block outcome (hookexec exit 2) aborts the run before
// any prompt processing or model call, and the caller terminates the run. It
// returns blocked=true with the rejection reason in that case.
//
// Error-handling note: a hook execution fault is treated the SAME as a block
// (fail-safe) — the run cannot proceed past a vetoing run-level phase whose
// verdict is unknown. This mirrors UserPromptSubmit deliberately; see the report.
func (e *Engine) fireSessionStart(ctx context.Context, r *Run, sess *session.Session) (blocked bool, reason string) {
	if e.deps.Hooks == nil {
		return false, ""
	}
	if ctx.Err() != nil {
		return false, ""
	}
	ev := governance.HookEvent{
		Phase:     governance.PhaseSessionStart,
		SessionID: string(sess.ID),
	}
	outcome, err := e.runOwnedHook(ctx, r, sess, ev)
	if err != nil {
		// A hook execution fault aborts the run: a vetoing phase whose verdict is
		// unknown cannot be assumed to allow.
		return true, "SessionStart hook error: " + err.Error()
	}
	if outcome.Block {
		msg := outcome.Message
		if msg == "" {
			msg = "session blocked by SessionStart hook"
		}
		e.emit(r, session.Event{Type: session.EvHook, Text: msg,
			Hook: &session.HookPayload{Phase: string(governance.PhaseSessionStart), Decision: session.HookBlocked}})
		return true, msg
	}
	return false, ""
}

// fireUserPromptSubmit fires the UserPromptSubmit phase on the (command-expanded)
// prompt text, BEFORE the prompt is recorded and before the first model call.
// This is a BLOCKING phase: a Block outcome (hookexec exit 2) rejects the prompt,
// and the caller terminates the run without ever calling the model — it returns
// blocked=true with the rejection reason.
//
// Mutation: a hook MAY return a mutated payload (HookOutcome.Mutated) to rewrite
// the prompt. The payload is interpreted SYMMETRICALLY with HookEvent.Input — a
// JSON object {"prompt": "..."} — so the mutation replaces that same field. The
// returned finalText is the effective prompt the caller records and the model
// sees: the mutated text when a well-formed mutation is present, otherwise the
// input userText unchanged. A malformed mutation payload is ignored (the original
// text stands) and surfaced as a hook Event for observability.
func (e *Engine) fireUserPromptSubmit(ctx context.Context, r *Run, sess *session.Session, userText string) (finalText string, blocked bool, reason string) {
	if e.deps.Hooks == nil {
		return userText, false, ""
	}
	if ctx.Err() != nil {
		return userText, false, ""
	}
	input, _ := json.Marshal(promptPayload{Prompt: userText})
	ev := governance.HookEvent{
		Phase:     governance.PhaseUserPromptSubmit,
		Input:     input,
		SessionID: string(sess.ID),
	}
	outcome, err := e.runOwnedHook(ctx, r, sess, ev)
	if err != nil {
		// A hook execution fault rejects the prompt: the run cannot proceed past a
		// vetoing phase whose verdict is unknown.
		return userText, true, "UserPromptSubmit hook error: " + err.Error()
	}
	if outcome.Block {
		msg := outcome.Message
		if msg == "" {
			msg = "prompt blocked by UserPromptSubmit hook"
		}
		e.emit(r, session.Event{Type: session.EvHook, Text: msg,
			Hook: &session.HookPayload{Phase: string(governance.PhaseUserPromptSubmit), Decision: session.HookBlocked}})
		return userText, true, msg
	}
	if len(outcome.Mutated) > 0 {
		// Apply the mutation symmetrically: decode the same {"prompt": ...} shape and
		// use the rewritten text as the effective prompt.
		var p promptPayload
		if jerr := json.Unmarshal(outcome.Mutated, &p); jerr == nil {
			e.emit(r, session.Event{Type: session.EvHook, Text: "UserPromptSubmit hook rewrote the prompt",
				Hook: &session.HookPayload{Phase: string(governance.PhaseUserPromptSubmit), Decision: session.HookModified}})
			return p.Prompt, false, ""
		}
		e.emit(r, session.Event{Type: session.EvHook, Text: "UserPromptSubmit hook returned a malformed prompt mutation (ignored)",
			Hook: &session.HookPayload{Phase: string(governance.PhaseUserPromptSubmit), Decision: session.HookInfo}})
	}
	return userText, false, ""
}

// promptPayload is the JSON shape of the UserPromptSubmit hook's Input and the
// symmetric shape its Mutated payload is interpreted as.
type promptPayload struct {
	Prompt string `json:"prompt"`
}

// fireStop fires the Stop phase at the terminal end of a run (any terminal path:
// complete / stop-condition / error / cancel), after the result is determined. It
// is informational and best-effort: a Block outcome cannot veto an already-ended
// run (mirroring SubagentStop). It is invoked exactly once per run from the
// terminate/terminateComplete paths.
func (e *Engine) fireStop(ctx context.Context, r *Run, sess *session.Session, reason session.StopReason) {
	if e.deps.Hooks == nil {
		return
	}
	// Stop is a terminal notification: run it even if ctx is already cancelled,
	// using a detached, short-lived context so a cancelled run still notifies.
	hookCtx := ctx
	if ctx.Err() != nil {
		var cancel context.CancelFunc
		hookCtx, cancel = context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
	}
	input, _ := json.Marshal(struct {
		Stop string `json:"stop_reason"`
	}{Stop: string(reason)})
	ev := governance.HookEvent{
		Phase:     governance.PhaseStop,
		Input:     input,
		SessionID: string(sess.ID),
	}
	outcome, err := e.runOwnedHook(hookCtx, r, sess, ev)
	if err == nil && outcome.Block && outcome.Message != "" {
		// Stop is terminal — a Block can't veto an already-ended run, so this is an
		// informational notice, not a blocking one.
		e.emit(r, session.Event{Type: session.EvHook, Text: outcome.Message,
			Hook: &session.HookPayload{Phase: string(governance.PhaseStop), Decision: session.HookInfo}})
	}
}

// fireNotify runs a terminal, best-effort lifecycle NOTIFICATION hook — one that
// cannot be vetoed (SubagentStop, TeammateIdle). A Block or error outcome is
// ignored: the event being reported has already happened. Like fireStop, it
// DETACHES from an already-cancelled ctx (with a short timeout) so a terminal
// notification still reaches the runner even when the run was cancelled — this is
// the single definition of that detach rule, which previously diverged across the
// Subagent/Fork/team fire sites. A nil runner is a no-op.
func fireNotify(ctx context.Context, hooks port.HookRunner, ev governance.HookEvent) {
	if hooks == nil {
		return
	}
	if ctx.Err() != nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
	}
	_, _ = hooks.Run(ctx, ev)
}
