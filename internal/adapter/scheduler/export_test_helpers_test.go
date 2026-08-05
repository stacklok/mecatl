package scheduler

import "time"

// SetStopLeadershipJoinTimeoutForTest overrides the package-level
// stopLeadershipJoinTimeout bound so an external-package test can shrink it to
// assert Stop is bounded. Returns a restore func the caller defers. It lives in
// the internal package so it can touch the unexported var; it is reached from
// the external scheduler_test package via this `_test.go`-only exported func.
func SetStopLeadershipJoinTimeoutForTest(d time.Duration) func() {
	prev := stopLeadershipJoinTimeout
	stopLeadershipJoinTimeout = d
	return func() { stopLeadershipJoinTimeout = prev }
}

// SetStaleFireWindowForTest overrides the package-level staleFireWindow (the
// stale-fire reconciler's staleness threshold, issue #386 Phase 4b) so an
// external-package test can shrink it to exercise the reconcile path without
// waiting the full default window. Returns a restore func the caller defers.
func SetStaleFireWindowForTest(d time.Duration) func() {
	prev := staleFireWindow
	staleFireWindow = d
	return func() { staleFireWindow = prev }
}
