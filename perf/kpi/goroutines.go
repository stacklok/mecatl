package kpi

import (
	"runtime"
	"time"
)

// GoroutinesAfterSettle returns the live goroutine count after a settle delay,
// so transient run-teardown goroutines (a draining emitter, a parked sampler,
// the GC) are not miscounted as a leak. It is the tracked-number promotion of
// goleak's boolean pass/fail (perf-tracking.md "The benchmark harness"): the end-of-run
// goroutine count is a gate-hard KPI for the delegation/team/background leak
// class.
//
// It sleeps d (clamped to a small floor), runs a GC to release goroutines parked
// on finalizers/timers, then reads runtime.NumGoroutine. The reading includes
// the caller's own goroutine and the test runner's, so the harness compares it
// against a same-process baseline rather than treating it as an absolute — see
// GoroutineDelta, which is what scenarios actually store.
func GoroutinesAfterSettle(d time.Duration) int {
	if d < time.Millisecond {
		d = time.Millisecond
	}
	time.Sleep(d)
	runtime.GC()
	return runtime.NumGoroutine()
}

// GoroutineDelta returns the leaked-goroutine count: the settled end count minus
// a baseline the caller captured (also via GoroutinesAfterSettle) BEFORE the
// measured region, clamped at 0. So 0 = no leak; a positive value is the number
// of goroutines the scenario started and did not join. Clamping at 0 keeps a
// transient that the baseline happened to catch (a sampler/GC goroutine that has
// since exited) from reporting a spurious negative. This is the meaningful KPI
// for the delegation/team/background leak class — the raw process-wide count
// carries the test runner's own goroutines as noise.
func GoroutineDelta(baseline int, settle time.Duration) int {
	end := GoroutinesAfterSettle(settle)
	if d := end - baseline; d > 0 {
		return d
	}
	return 0
}
