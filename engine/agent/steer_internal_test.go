package agent

import (
	"reflect"
	"sync"
	"testing"

	"github.com/stacklok/mecatl/engine/session"
)

func TestSteerAggregateRejectionIsAtomic(t *testing.T) {
	r := &Run{steer: newSteerInbox()}
	parts := make([]session.Content, session.MaxPromptMediaParts)
	for i := range parts {
		part, err := session.NewImageContent("image/png", []byte{byte(i)})
		if err != nil {
			t.Fatal(err)
		}
		parts[i] = part
	}
	if got, err := r.enqueueSteerContent("kept", parts); err != nil || got != SteerAccepted {
		t.Fatalf("initial enqueue = %q, %v", got, err)
	}
	before := steerContent{text: r.steer.pending.text, parts: append([]session.Content(nil), r.steer.pending.parts...)}
	extra, err := session.NewAudioContent("audio/wav", []byte("overflow"))
	if err != nil {
		t.Fatal(err)
	}
	if got, err := r.enqueueSteerContent("rejected", []session.Content{extra}); err == nil || got != SteerTooLate {
		t.Fatalf("over-cap append = %q, %v", got, err)
	}
	if r.steer.pending.text != before.text || !reflect.DeepEqual(r.steer.pending.parts, before.parts) {
		t.Fatalf("rejection mutated pending content: before=%#v after=%#v", before, r.steer.pending)
	}

	// Content.Data is immutable by domain convention, but the caller-owned slice
	// backing array is not: replacing an element after enqueue must not rewrite the inbox.
	parts[0] = extra
	if !reflect.DeepEqual(r.steer.pending.parts, before.parts) {
		t.Fatal("inbox retained caller slice backing array")
	}
}

func TestSteerMultimodalAppendAndCancel(t *testing.T) {
	r := &Run{steer: newSteerInbox()}
	image, err := session.NewImageContent("image/png", []byte("one"))
	if err != nil {
		t.Fatal(err)
	}
	audio, err := session.NewAudioContent("audio/wav", []byte("two"))
	if err != nil {
		t.Fatal(err)
	}
	if got, err := r.enqueueSteerContent("first", []session.Content{image}); err != nil || got != SteerAccepted {
		t.Fatalf("first = %q, %v", got, err)
	}
	if got, err := r.enqueueSteerContent("", []session.Content{audio}); err != nil || got != SteerAppended {
		t.Fatalf("media-only append = %q, %v", got, err)
	}
	if got, err := r.enqueueSteerContent("last", nil); err != nil || got != SteerAppended {
		t.Fatalf("text append = %q, %v", got, err)
	}
	content, ok := r.drainSteerContent()
	if !ok || content.text != "first\n\nlast" || len(content.parts) != 2 || content.parts[0].Kind != session.MediaImage || content.parts[1].Kind != session.MediaAudio {
		t.Fatalf("drain = %#v, %v", content, ok)
	}
	if _, err := r.enqueueSteerContent("", []session.Content{image}); err != nil {
		t.Fatal(err)
	}
	if got, _ := r.cancelSteer(); got != SteerRetracted {
		t.Fatalf("cancel = %q", got)
	}
	if _, ok := r.drainSteerContent(); ok {
		t.Fatal("cancel left multimodal steer pending")
	}
}

// TestSteer_InboxLinearizable (R2-3): the mutex inbox makes the finding-#1
// deadlock shape impossible BY CONSTRUCTION — every transition (enqueue /
// cancel / drain / close) is ONE critical section over the whole
// {closed,pending,has} triple, so there is no receive/send between two selects
// and no done-check torn from the send. A forced drain between a would-be
// observe and a would-be mutate cannot deadlock, cannot double-commit, and
// cannot resurrect a drained steer. Run under -race.
func TestSteer_InboxLinearizable(t *testing.T) {
	// Deterministic sequencing: the drain (the run goroutine's role) is forced
	// BETWEEN the two enqueues — the exact interleave that on the old cap-1
	// channel was a non-atomic "observe full → receive → send" replace window.
	r := &Run{steer: newSteerInbox()}

	if outcome, _ := r.enqueueSteer("steer: v1"); outcome != SteerAccepted {
		t.Fatalf("first enqueue = %q, want %q", outcome, SteerAccepted)
	}
	// Occupied slot: the second enqueue APPENDS in the same critical section —
	// the pending bundle's text grows by "\n\n"+v2 (append is the default; the
	// old reject-on-full/supersede was dropped).
	if outcome, _ := r.enqueueSteer("steer: v2"); outcome != SteerAppended {
		t.Fatalf("enqueue on an occupied slot = %q, want %q", outcome, SteerAppended)
	}
	// The merged bundle drains ONCE as the appended text (v1 + "\n\n" + v2).
	text, ok := r.drainSteer()
	if !ok || text != "steer: v1\n\nsteer: v2" {
		t.Fatalf("drain = (%q, %v), want (%q, true) — the appended bundle commits exactly once", text, ok, "steer: v1\n\nsteer: v2")
	}
	// The drained slot is EMPTY: a second drain commits nothing (no
	// double-commit) and a cancel finds nothing (no resurrection).
	if text, ok := r.drainSteer(); ok {
		t.Fatalf("second drain = (%q, %v), want empty — the drain must take+clear atomically", text, ok)
	}
	if outcome, _ := r.cancelSteer(); outcome != SteerNonePending {
		t.Fatalf("cancel after the drain = %q, want %q (the drained steer is gone)", outcome, SteerNonePending)
	}
	// A post-drain enqueue lands in the re-emptied slot (the inbox stays open
	// until close); close then flips too_late / none_pending deterministically.
	if outcome, _ := r.enqueueSteer("steer: v3"); outcome != SteerAccepted {
		t.Fatalf("post-drain enqueue = %q, want %q", outcome, SteerAccepted)
	}
	r.closeSteer()
	if outcome, _ := r.enqueueSteer("steer: too late"); outcome != SteerTooLate {
		t.Fatalf("enqueue after close = %q, want %q", outcome, SteerTooLate)
	}
	// close does NOT drain: a steer parked at close is retracted by the cancel
	// (the terminal close-drain is a separate explicit hook — closeSteer itself
	// only flips closed).
	if outcome, _ := r.cancelSteer(); outcome != SteerRetracted {
		t.Fatalf("cancel of the close-parked steer = %q, want %q (close must not swallow the parked steer)", outcome, SteerRetracted)
	}
	// close is idempotent.
	r.closeSteer()
}

// TestSteerInboxConcurrentTransitions exercises the inbox's four transitions
// (enqueue / cancel-retract / drain-commit / close-terminal) under direct
// concurrent access, including the closeSteer seam the external test only
// reaches by driving a full run to terminal. Run under -race: the run goroutine
// drains while wire-handler goroutines enqueue and cancel, and any missing
// mutex is a data race. It asserts the outcome ENUM is always a defined value
// and that a closed inbox reports too_late / none_pending — never panics.
func TestSteerInboxConcurrentTransitions(t *testing.T) {
	r := &Run{steer: newSteerInbox()}

	var wg sync.WaitGroup
	// One goroutine drains (the run loop's role) — the ONLY slot consumer.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 400; i++ {
			r.drainSteer()
		}
	}()
	// Several goroutines enqueue (into the single slot) and cancel (retract).
	for g := 0; g < 6; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				switch outcome, _ := r.enqueueSteer("steer: internal racer"); outcome {
				case SteerAccepted, SteerAppended, SteerTooLate:
				default:
					t.Errorf("enqueue outcome %q is not a defined SteerOutcome", outcome)
					return
				}
				switch outcome, _ := r.cancelSteer(); outcome {
				case SteerRetracted, SteerNonePending:
				default:
					t.Errorf("cancel outcome %q is not a defined SteerOutcome", outcome)
					return
				}
			}
		}()
	}
	wg.Wait()

	// Close the inbox (run terminal) and confirm the too-late contract holds
	// against a final racing enqueue/cancel.
	r.closeSteer()
	if outcome, _ := r.enqueueSteer("steer: after close"); outcome != SteerTooLate {
		t.Fatalf("enqueue after close = %q, want %q", outcome, SteerTooLate)
	}
	if outcome, _ := r.cancelSteer(); outcome != SteerNonePending {
		t.Fatalf("cancel after close = %q, want %q", outcome, SteerNonePending)
	}
}
