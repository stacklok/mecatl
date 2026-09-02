package agent

import (
	"context"
	"fmt"
	"time"

	"github.com/stacklok/mecatl/engine/session"
)

// guardrailCheckTimeout bounds one guardrail check so a tool call never hangs on a
// wedged checker model: RunGuardrailCheck derives this deadline from the caller's
// context, and a timed-out check returns an error.
const guardrailCheckTimeout = 30 * time.Second

// guardrailCheckLimits are the checker run's stop conditions: ONE turn, tool-less.
// The checker must answer in its first turn; an empty or verdict-less turn ends as a
// failure rather than a retry. The caller is expected to disable the no-progress
// nudge on the checker engine so an empty turn ends in exactly one provider call.
var guardrailCheckLimits = session.Limits{MaxTurns: 1, MaxToolCalls: 1, MaxConsecutiveFailures: 1}

// RunGuardrailCheck drives a dedicated, tool-less one-turn Engine over a fully
// assembled checker prompt and returns its raw final text. A run failure or
// cancellation is an error (never a fabricated reply), so a caller can treat a
// failed check distinctly from a verdict. The drive is bounded by a hard timeout
// derived from ctx.
//
// engine must be a tool-less checker Engine that fires no hooks and carries no
// nested reviewer, so a check can never recurse or call a tool. It returns an error
// on a nil engine rather than panicking, since it is a leaf helper.
func RunGuardrailCheck(ctx context.Context, engine *Engine, prompt string) (string, error) {
	if engine == nil {
		return "", fmt.Errorf("agent: RunGuardrailCheck requires a non-nil engine")
	}
	ctx, cancel := context.WithTimeout(ctx, guardrailCheckTimeout)
	defer cancel()

	sess := session.New(
		session.SessionID(fmt.Sprintf("guardrail-checker-%d", childSerial.Add(1))),
		session.ModeDefault,
		session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/", Revision: inTreeEnvironmentRevision},
		guardrailCheckLimits,
		engine.now(),
	)
	// A tool-less in-memory session under the zero (headless) child posture: the
	// checker scores text and calls no tools, so judgeWorkspace{} keeps it isolated
	// and its own (non-existent) asks auto-deny — no nesting, no surfacing.
	run := engine.Run(ctx, sess, judgeEnvironment, RunRequest{Text: prompt})
	final, stop := drainChild(run, childPosture{role: "guardrail-checker"})
	if stop == session.StopError || stop == session.StopCancelled {
		return "", fmt.Errorf("guardrail checker run did not complete (stop %q)", stop)
	}
	return final, nil
}
