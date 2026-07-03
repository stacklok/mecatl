package jq

import (
	"context"
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

// TestRunMemoryBudgetSingleHugeValue is the CWE-770/400 regression guard: a
// filter that builds ONE giant value (which the post-materialization output cap
// cannot catch) is stopped by the heap watchdog and reported as a memory-budget
// error — distinct from the timeout path — well before the deadline.
func TestRunMemoryBudgetSingleHugeValue(t *testing.T) {
	withTinyBudget(t, 8<<20, 5*time.Millisecond)

	start := time.Now()
	_, err := Run(context.Background(), "[range(1e9)]", []byte("null"))
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected a memory-budget error for [range(1e9)], got nil")
	}
	if !strings.Contains(err.Error(), "memory budget") {
		t.Fatalf("expected a memory-budget error, got: %v", err)
	}
	if strings.Contains(err.Error(), "timed out") {
		t.Fatalf("memory-budget trip must not be reported as a timeout: %v", err)
	}
	// The watchdog must win the race against DefaultTimeout (5s) by a wide margin.
	if elapsed > 2*time.Second {
		t.Fatalf("watchdog too slow (%v); expected sub-second cancellation", elapsed)
	}
}

// TestRunMemoryBudgetLeavesLegitFilterUntouched confirms the watchdog does not
// false-trip a small, well-behaved filter even under the tiny test budget.
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
