package agent

import "github.com/stacklok/mecatl/engine/session"

// This file is the Go export_test.go seam: it hands unexported identifiers to
// the external agent_test package without widening the shipped API surface
// (engine/api/agent.txt is generated from non-test files only).

// WithSessionOrigin exposes withSessionOrigin so the external tests can build an
// origin-bearing context directly, instead of only through Engine.Run. Kept
// test-only on purpose — see the withSessionOrigin doc-comment (ADR 0209).
var WithSessionOrigin = withSessionOrigin

// EnqueueSteerForTest exposes the canonical Run steer entry point to external
// drain tests.
var EnqueueSteerForTest = (*Run).EnqueueSteer

// CancelSteerForTest exposes the Run's unexported steer-cancel seam so the
// external tests can retract a pending steer. It returns the authoritative
// SteerOutcome (task 02), not an error.
var CancelSteerForTest = (*Run).cancelSteer

// CloseSteerForTest exposes the Run's unexported steer-close seam so the
// internal concurrency test can simulate run-terminal without driving a full
// loop to completion.
var CloseSteerForTest = (*Run).closeSteer

// RefreshReviewTasksForTest simulates a committed root steer while a child waits.
func RefreshReviewTasksForTest(r *Run, messages []session.Message) {
	r.reviewRoot.refreshTasks(messages)
}

// WithParallelBranchRegisteredForTest explicitly observes branch registration
// before its worker-slot wait, so external cancellation tests do not rely on
// scheduler timing.
func WithParallelBranchRegisteredForTest(fn func(int)) ParallelOption {
	return func(t *ParallelTool) { t.onBranchRegistered = fn }
}
