package port

import (
	"context"

	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/session"
)

// HookResult is the complete result of one hook request. AuxiliaryUsage is
// returned explicitly so hook runners cannot retain a session mutation path.
type HookResult struct {
	Outcome        governance.HookOutcome
	AuxiliaryUsage session.AuxiliaryUsage
}

// HookRunner executes a lifecycle hook for a HookEvent and returns its outcome.
// The shell-exec adapter maps process exit code 0 to allow and exit code 2 to a
// blocking outcome.
//
// A PreToolUse outcome MAY set governance.HookOutcome.AskApproval together with
// Block to REFINE a block into an askable block: an interactive engine surfaces it
// as a permission ask rather than dead-ending the call (ADR 0062). A HookRunner is
// free to never set it (the byte-identical pre-feature terminal-block behaviour).
type HookRunner interface {
	// Run executes the hook(s) registered for ev.Phase and returns the complete result.
	Run(ctx context.Context, ev governance.HookEvent) (HookResult, error)
}

// HookApprovalLearner is an OPTIONAL capability a HookRunner may ALSO implement to
// be told when a HUMAN granted a durable "allow & don't ask again" verdict
// (session.VerdictAllowAlways) on a hook-originated approval ask (ADR 0062). The
// engine TYPE-ASSERTS this interface on Deps.Hooks and calls LearnHookApproval
// ONLY at that one verdict site — so a HookRunner that does not implement it is
// wholly unaffected (no method added to HookRunner: that would be a breaking
// change). It is called ONLY for session.VerdictAllowAlways — NEVER for allow-once
// (nothing is remembered) and NEVER for deny — and ONLY on the LIVE verdict path,
// not the awaiting-resume path (which would double-arm). The payload is the neutral
// governance.HookEvent the engine already holds (SessionID + Tool + Input args),
// carrying NO approval/guardrail vocabulary — the engine stays generic; the consumer
// (the guardrails adapter) interprets it to arm a session-scoped waiver so a later
// identical block does not re-ask.
type HookApprovalLearner interface {
	// LearnHookApproval records that the human authorized the hook-blocked call
	// described by ev (its SessionID/Tool/Input). The consumer decides the waiver
	// scope; the engine only reports the verdict. It is called synchronously at the
	// verdict site and MUST be cheap and non-blocking.
	LearnHookApproval(ctx context.Context, ev governance.HookEvent)
}
