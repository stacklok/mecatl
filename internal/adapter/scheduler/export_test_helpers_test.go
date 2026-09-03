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

// SetLeaderStandbyBackoffForTest overrides the package-level
// leaderStandbyBackoff wait so an external-package test can drive the real
// leadership loop through repeated standby acquire attempts without sleeping
// through the production interval. Returns a restore func the caller defers.
func SetLeaderStandbyBackoffForTest(d time.Duration) func() {
	prev := leaderStandbyBackoff
	leaderStandbyBackoff = d
	return func() { leaderStandbyBackoff = prev }
}

// SetStandbyLogIntervalForTest overrides the package-level standbyLogInterval
// heartbeat bound so an external-package test can assert the standby log's
// rate limit without depending on the production default. Returns a restore
// func the caller defers.
func SetStandbyLogIntervalForTest(d time.Duration) func() {
	prev := standbyLogInterval
	standbyLogInterval = d
	return func() { standbyLogInterval = prev }
}
