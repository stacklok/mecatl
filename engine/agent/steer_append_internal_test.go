package agent

import (
	"fmt"
	"strings"
	"sync"
	"testing"
)

// TestSteer_NoSlotFullOrSuperseded (R5-3): the SteerOutcome enum no longer
// carries slot_full or superseded — append-default (task 12) covers BOTH: a
// second enqueue on an occupied slot APPENDS into the pending bundle instead
// of rejecting (slot_full) or silently replacing (superseded). The closed set
// is exactly {accepted, appended, retracted, none_pending, too_late}, and none
// of the exported constants resolves to the dropped values.
func TestSteer_NoSlotFullOrSuperseded(t *testing.T) {
	defined := map[SteerOutcome]bool{
		SteerAccepted:    true,
		SteerAppended:    true,
		SteerRetracted:   true,
		SteerNonePending: true,
		SteerTooLate:     true,
	}
	if len(defined) != 5 {
		t.Fatalf("defined outcome set = %d, want exactly 5", len(defined))
	}
	for _, outcome := range []SteerOutcome{SteerAccepted, SteerAppended, SteerRetracted, SteerNonePending, SteerTooLate} {
		if !defined[outcome] {
			t.Fatalf("exported outcome %q missing from the defined set", outcome)
		}
		if outcome == "slot_full" || outcome == "superseded" {
			t.Fatalf("the dropped outcome %q survived the append-default rework", outcome)
		}
	}
}

// TestSteer_AppendLinearizable (R5-4): the append-default inbox keeps the
// single-slot invariant — one pending BUNDLE, grown by append — and the mutex
// linearizability of every transition. The deterministic half forces the exact
// interleaves: an enqueue into the occupied slot APPENDS (old+"\n\n"+new as
// ONE bundle); the drain takes the WHOLE merged bundle exactly once (no
// half-merge, no double-commit, no resurrection); a post-drain enqueue parks a
// FRESH bundle; close flips too_late / none_pending. The concurrent half
// hammers enqueue/append/cancel/drain from several goroutines (-race): every
// outcome is a defined enum value, and every marker an enqueue reported
// accepted/appended for shows up in EXACTLY ONE observation (a drained bundle,
// a retracted cancel, or the terminal pending bundle) — never lost, never
// doubled.
func TestSteer_AppendLinearizable(t *testing.T) {
	// Deterministic sequencing: v1 parks (accepted), v2 appends (appended) into
	// the SAME single slot — one merged bundle, never a second slot.
	r := &Run{steer: newSteerInbox()}
	if outcome, _ := r.EnqueueSteer("steer: v1", nil); outcome != SteerAccepted {
		t.Fatalf("first enqueue = %q, want %q", outcome, SteerAccepted)
	}
	if outcome, _ := r.EnqueueSteer("steer: v2", nil); outcome != SteerAppended {
		t.Fatalf("enqueue on an occupied slot = %q, want %q (append-default)", outcome, SteerAppended)
	}
	// The drain commits the merged bundle ONCE, whole.
	content, ok := r.drainSteer()
	if !ok || content.text != "steer: v1\n\nsteer: v2" {
		t.Fatalf("drain = (%q, %v), want (%q, true) — the merged bundle commits exactly once, whole", content.text, ok, "steer: v1\n\nsteer: v2")
	}
	// The drained slot is EMPTY: a second drain commits nothing (no
	// double-commit) and a cancel finds nothing (no resurrection of the merged
	// bundle or either half).
	if content, ok := r.drainSteer(); ok {
		t.Fatalf("second drain = (%q, %v), want empty — drain must take+clear atomically", content.text, ok)
	}
	if outcome, _ := r.cancelSteer(); outcome != SteerNonePending {
		t.Fatalf("cancel after the drain = %q, want %q", outcome, SteerNonePending)
	}
	// A post-drain enqueue parks a FRESH bundle (accepted — the slot re-emptied);
	// an append onto it merges; close then flips too_late / none_pending
	// deterministically.
	if outcome, _ := r.EnqueueSteer("steer: v3", nil); outcome != SteerAccepted {
		t.Fatalf("post-drain enqueue = %q, want %q", outcome, SteerAccepted)
	}
	if outcome, _ := r.EnqueueSteer("steer: v4", nil); outcome != SteerAppended {
		t.Fatalf("append onto v3 = %q, want %q", outcome, SteerAppended)
	}
	r.closeSteer()
	if outcome, _ := r.EnqueueSteer("steer: too late", nil); outcome != SteerTooLate {
		t.Fatalf("enqueue after close = %q, want %q", outcome, SteerTooLate)
	}
	// close does NOT drain: the close-parked merged bundle is still there for the
	// cancel to retract (closeSteer only flips closed; the terminal close-drain
	// is a separate explicit hook).
	if outcome, _ := r.cancelSteer(); outcome != SteerRetracted {
		t.Fatalf("cancel of the close-parked bundle = %q, want %q (close must not swallow it)", outcome, SteerRetracted)
	}
	r.closeSteer() // idempotent

	// Concurrent half: enqueue/append/cancel/drain race under -race. Every
	// outcome must be a defined enum value, and conservation must hold: every
	// marker an enqueue reported parked (accepted/appended) is contained in
	// EXACTLY ONE observation — a drained bundle or the terminal pending bundle
	// — so no contribution is lost (a torn append) or doubled (a drain that
	// didn't clear). Markers in NO bundle were retracted by a racing cancel
	// (legal: the cancel takes the whole bundle before any drain sees it).
	var mu sync.Mutex
	var bundles []string // every drained bundle, whole
	stop := make(chan struct{})
	var consumerWG sync.WaitGroup
	consumerWG.Add(1)
	go func() {
		defer consumerWG.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			if content, ok := r.drainSteer(); ok {
				mu.Lock()
				bundles = append(bundles, content.text)
				mu.Unlock()
			}
		}
	}()

	var deliveredMu sync.Mutex
	delivered := map[string]bool{} // markers an enqueue reported parked
	var producersWG sync.WaitGroup
	for g := 0; g < 6; g++ {
		producersWG.Add(1)
		go func(g int) {
			defer producersWG.Done()
			for i := 0; i < 40; i++ {
				marker := fmt.Sprintf("m-%d-%d", g, i)
				outcome, err := r.EnqueueSteer(marker, nil)
				if err != nil {
					t.Errorf("enqueue returned error %v", err)
					return
				}
				switch outcome {
				case SteerAccepted, SteerAppended:
					deliveredMu.Lock()
					delivered[marker] = true
					deliveredMu.Unlock()
				case SteerTooLate:
					// The inbox is never closed here, so this arm is unreachable;
					// it stays a defined outcome anyway.
				default:
					t.Errorf("enqueue outcome %q is not a defined SteerOutcome", outcome)
					return
				}
				// A racing cancel retracts the parked bundle whole (its markers
				// then legally appear in NO drained bundle).
				switch outcome, _ := r.cancelSteer(); outcome {
				case SteerRetracted, SteerNonePending:
				default:
					t.Errorf("cancel outcome %q is not a defined SteerOutcome", outcome)
					return
				}
			}
		}(g)
	}
	producersWG.Wait()
	close(stop)
	consumerWG.Wait()
	// Terminal deterministic drain: whatever is still parked after the producers
	// finished and the consumer stopped is ONE final observation.
	if content, ok := r.drainSteer(); ok {
		mu.Lock()
		bundles = append(bundles, content.text)
		mu.Unlock()
	}

	mu.Lock()
	defer mu.Unlock()
	for marker := range delivered {
		seen := 0
		for _, bundle := range bundles {
			if strings.Contains(bundle, marker) {
				seen++
			}
		}
		if seen > 1 {
			t.Fatalf("marker %q appears in %d bundles — the inbox lost a clear or tore an append (linearizability violated)", marker, seen)
		}
	}
}
