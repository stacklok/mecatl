package client

import (
	"testing"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
)

// TestSnapshotFromReadsState asserts snapshotFrom projects the proto Session's
// State field (issue #245 Phase 1) — the picker row needs it to render an
// "open existing session" affordance. Covers the populated and nil cases.
func TestSnapshotFromReadsState(t *testing.T) {
	// Populated: a completed session carries its state through.
	snap := snapshotFrom(&mecatlv1.Session{State: "completed"})
	if snap.State != "completed" {
		t.Fatalf("State = %q, want %q", snap.State, "completed")
	}

	// nil session degrades to the default-mode snapshot with an empty State.
	nilSnap := snapshotFrom(nil)
	if nilSnap.State != "" {
		t.Fatalf("nil State = %q, want empty", nilSnap.State)
	}
	if nilSnap.Mode != ModeDefaultString {
		t.Fatalf("nil Mode = %q, want %q", nilSnap.Mode, ModeDefaultString)
	}
}
