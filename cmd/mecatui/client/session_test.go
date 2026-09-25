package client

import (
	"testing"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
)

func TestSnapshotFromUsesSessionMediaCapabilities(t *testing.T) {
	textOnly := snapshotFrom(&mecatlv1.Session{
		SessionCapabilities: &mecatlv1.SessionCapabilities{},
	})
	if textOnly.Capabilities.Image || !textOnly.Capabilities.SessionMediaPresent {
		t.Fatalf("text-only snapshot capabilities = %+v", textOnly.Capabilities)
	}
	withoutMedia := snapshotFrom(&mecatlv1.Session{})
	if withoutMedia.Capabilities.SessionMediaPresent {
		t.Fatalf("absent session media unexpectedly marked present: %+v", withoutMedia.Capabilities)
	}
}

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

// TestSnapshotFromReadsTitle asserts snapshotFrom projects the proto Session's
// Title field — the footer/heal surface may surface it. Covers the populated,
// empty, and nil cases (nil-safe via the getter).
func TestSnapshotFromReadsTitle(t *testing.T) {
	snap := snapshotFrom(&mecatlv1.Session{TitleMetadata: &mecatlv1.SessionTitle{Title: "Fix the CI", Provenance: "generated", Revision: 7}})
	if snap.Title != "Fix the CI" || snap.TitleProvenance != "generated" || snap.TitleRevision != 7 {
		t.Fatalf("title/provenance/revision = %q/%q/%d, want Fix the CI/generated/7", snap.Title, snap.TitleProvenance, snap.TitleRevision)
	}
	empty := snapshotFrom(&mecatlv1.Session{})
	if empty.Title != "" {
		t.Fatalf("empty Title = %q, want empty", empty.Title)
	}
	nilSnap := snapshotFrom(nil)
	if nilSnap.Title != "" {
		t.Fatalf("nil Title = %q, want empty", nilSnap.Title)
	}
}

func TestSnapshotFromProjectsMainUsageAndOptionalContextOccupancy(t *testing.T) {
	snap := snapshotFrom(&mecatlv1.Session{
		TokenUsage: map[string]*mecatlv1.TokenUsage{
			"main": {Total: &mecatlv1.Usage{InputTokens: 120_000, OutputTokens: 4_000, CacheReadTokens: 90_000}},
		},
		LatestContextOccupancy: &mecatlv1.ContextOccupancy{InputTokens: 40_000, Estimated: true},
	})
	if snap.Usage != (Usage{InputTokens: 120_000, OutputTokens: 4_000, CacheReadTokens: 90_000}) {
		t.Fatalf("main usage = %+v", snap.Usage)
	}
	if snap.ContextOccupancy == nil || *snap.ContextOccupancy != (ContextOccupancy{InputTokens: 40_000, Estimated: true}) {
		t.Fatalf("context occupancy = %+v", snap.ContextOccupancy)
	}
	legacy := snapshotFrom(&mecatlv1.Session{TokenUsage: map[string]*mecatlv1.TokenUsage{"main": {Total: &mecatlv1.Usage{InputTokens: 120_000}}}})
	if legacy.ContextOccupancy != nil || legacy.Usage.InputTokens != 120_000 {
		t.Fatalf("legacy snapshot = %+v", legacy)
	}
}
