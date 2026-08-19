package agent

// This file is the Go export_test.go seam: it hands unexported identifiers to
// the external agent_test package without widening the shipped API surface
// (engine/api/agent.txt is generated from non-test files only).

// WithSessionOrigin exposes withSessionOrigin so the external tests can build an
// origin-bearing context directly, instead of only through Engine.Run. Kept
// test-only on purpose — see the withSessionOrigin doc-comment (ADR 0209).
var WithSessionOrigin = withSessionOrigin

// EnqueueSteerForTest exposes the Run's unexported steer-enqueue seam so the
// external drain tests can enqueue a mid-flight steer without the wire-facing
// entry point (a later task adds the exported API; this stays test-only). It
// returns the authoritative SteerOutcome (task 02), not an error.
var EnqueueSteerForTest = (*Run).enqueueSteer

// CancelSteerForTest exposes the Run's unexported steer-cancel seam so the
// external tests can retract a pending steer. It returns the authoritative
// SteerOutcome (task 02), not an error.
var CancelSteerForTest = (*Run).cancelSteer

// CloseSteerForTest exposes the Run's unexported steer-close seam so the
// internal concurrency test can simulate run-terminal without driving a full
// loop to completion.
var CloseSteerForTest = (*Run).closeSteer
