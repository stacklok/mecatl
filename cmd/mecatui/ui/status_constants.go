package ui

const (
	// stopError is the "error" terminal stop reason (proto Result.stop /
	// session.StopError).
	stopError = "error"

	// The closed team-member stop-reason vocabulary mirrors the proto
	// TeamMemberStopReason / client.reasonString output.
	teamStopReasonError     = "error"
	teamStopReasonCancelled = "cancelled"
	teamStopReasonBudget    = "budget"

	statusDone   = "done"
	statusFailed = "failed"
)
