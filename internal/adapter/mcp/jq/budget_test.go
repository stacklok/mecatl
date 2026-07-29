package jq

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// withTinyBudget lowers the heap budget and sampling interval so the memory
// watchdog can be exercised in milliseconds against a few MiB rather than the
// real 256 MiB / multi-second default. It restores both on cleanup.
func withTinyBudget(t *testing.T, budget uint64, interval time.Duration) {
	t.Helper()
	origBudget, origInterval := maxHeapGrowth, heapSampleInterval
	maxHeapGrowth, heapSampleInterval = budget, interval
	t.Cleanup(func() { maxHeapGrowth, heapSampleInterval = origBudget, origInterval })
}

// TestRunMemoryBudgetSingleHugeValue is the CWE-770/400 regression guard: a filter
// that builds ONE giant value — which [MaxOutputBytes] can only catch AFTER the
// memory has already been allocated — is stopped mid-materialization by the heap
// watchdog and reported as a memory-budget error.
//
// # The invariant, and the three ways it can be missed
//
// Run has three bounds that can end this filter, and the guard is that the WATCHDOG
// is the one that does. Each alternative surfaces a distinct, nameable error, so the
// oracle is the error's IDENTITY rather than wall clock:
//
//   - the heap watchdog          -> "…exceeded the N-byte memory budget"   (wanted)
//   - the context deadline       -> "…timed out", wrapping context.DeadlineExceeded
//   - the post-materialization
//     output cap                 -> "…output exceeded the N-byte cap"
//
// The output-cap arm is the important negative: it is precisely what fires when the
// watchdog fails to trip, because the giant value then materializes in full and the
// cap sees it only afterwards. Note Run returns on the output cap BEFORE its
// post-loop overBudget check, so a watchdog that trips too late is reported as an
// output-cap overrun — which is exactly the distinction this test exists to pin.
//
// # Why the parameters are what they are (this test used to be flaky)
//
// It previously ran `[range(1e9)]` on a deadline-free ctx (so Run applied its 5s
// DefaultTimeout) with an 8 MiB budget, and asserted `elapsed <= 2*time.Second`.
// Two independent load-sensitivities, both observed to fail under a saturated
// parallel `-race` suite while passing in isolation and on a re-run:
//
//  1. The wall-clock assertion was a LATENCY BUDGET, not the invariant. Removed —
//     the error-identity assertions below prove which limiter fired, with no timing
//     in them at all. Do not reintroduce one.
//  2. More fundamentally, the watchdog's trip point sat at ~5% of the work
//     (8 MiB of a ~160 MiB materialization), so under starvation the remaining 95%
//     could complete before the sampler got a look in — and the run then reported a
//     deadline or output-cap overrun instead. Verified by repeated runs under 13x
//     CPU oversubscription: 8 MiB flaked, 1 MiB did not (30/30 clean).
//
// So the budget is 1 MiB: the trip point moves to under 1% of the materialization,
// leaving the sampler the other 99% to observe it. That STRENGTHENS the guard rather
// than weakening it — a smaller budget makes the watchdog's job strictly harder to
// pass by accident, and the budget was always a test-only knob (withTinyBudget).
//
// N is bounded at 1e6 (not 1e9) and the deadline is explicit and generous. Those two
// go together: a bounded N caps a REGRESSED watchdog's blast radius at ~160 MiB of
// transient allocation, which is what makes it safe to hand the run a deadline long
// enough that CPU starvation cannot win the race. With `[range(1e9)]` the deadline
// WAS the only backstop, so it could not be relaxed without risking an OOM. 1e6 is
// still ~160x the budget, so the filter is a faithful memory-amplifier.
func TestRunMemoryBudgetSingleHugeValue(t *testing.T) {
	withTinyBudget(t, 1<<20, 5*time.Millisecond)

	// Generous and EXPLICIT: the deadline must not be a participant in the race the
	// test is judging. Safe to set this high only because N is bounded (see above).
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	_, err := Run(ctx, "[range(1e6)]", []byte("null"))

	if err == nil {
		t.Fatal("expected a memory-budget error for [range(1e6)], got nil")
	}
	// The watchdog fired: only the overBudget branches compose this message.
	if !strings.Contains(err.Error(), "memory budget") {
		t.Fatalf("expected a memory-budget error, got: %v", err)
	}
	// The DEADLINE did not: only the timeout branch wraps the ctx error, so this is
	// the typed form of "the watchdog won", with no timing in it.
	if errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("the deadline stopped the run, not the heap watchdog: %v", err)
	}
	if strings.Contains(err.Error(), "timed out") {
		t.Fatalf("memory-budget trip must not be reported as a timeout: %v", err)
	}
	// Nor the post-materialization output cap — the arm that fires when the watchdog
	// trips too late (or not at all), i.e. when the giant value was allocated in full
	// before anything noticed. Catching that is the whole point of the watchdog.
	if strings.Contains(err.Error(), "output exceeded") {
		t.Fatalf("the value materialized in full and the OUTPUT CAP stopped it — the watchdog did not trip in time: %v", err)
	}
}

// TestRunMemoryBudgetLeavesLegitFilterUntouched confirms the watchdog does not
// false-trip a small, well-behaved filter even under the tiny test budget. It keeps
// the 8 MiB budget deliberately: the sibling above lowered its own to 1 MiB to make
// a TRIP easier to observe, whereas the false-trip guard is strictest at the budget
// closest to a real filter's footprint.
func TestRunMemoryBudgetLeavesLegitFilterUntouched(t *testing.T) {
	withTinyBudget(t, 8<<20, 5*time.Millisecond)

	out, err := Run(context.Background(), ".items | length", []byte(`{"items":[1,2,3,4,5]}`))
	if err != nil {
		t.Fatalf("legit filter tripped the watchdog: %v", err)
	}
	if out != "5" {
		t.Fatalf("output = %q, want %q", out, "5")
	}
}
