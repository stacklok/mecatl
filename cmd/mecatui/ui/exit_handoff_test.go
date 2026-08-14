package ui

import "testing"

func TestSessionContinuityUX_Scenario7_FinalRebindMatrix(t *testing.T) {
	for _, journey := range []string{"stored continuation", "model carryover", "effort fork", "worktree switch"} {
		t.Run(journey, func(t *testing.T) {
			m, wantID := driveSessionRebindJourney(t, journey, &fakeClipboard{})
			if got := m.ActiveSessionID(); got != wantID {
				t.Fatalf("exit handoff ID = %q, want final adopted ID %q", got, wantID)
			}
		})
	}
}
