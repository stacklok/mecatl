package ui

import "testing"

func TestSessionContinuityUX_Scenario7_FinalRebindMatrix(t *testing.T) {
	m := Model{}.bindSessionID("startup-id")
	for _, rebind := range []struct {
		name string
		id   string
	}{
		{name: "stored-session continuation", id: "continued-id"},
		{name: "model carryover", id: "model-carryover-id"},
		{name: "effort fork", id: "effort-fork-id"},
		{name: "worktree switch", id: "worktree-session-id"},
	} {
		t.Run(rebind.name, func(t *testing.T) {
			m = m.bindSessionID(rebind.id)
			if got := m.ActiveSessionID(); got != rebind.id {
				t.Fatalf("ActiveSessionID() = %q, want final adopted ID %q", got, rebind.id)
			}
		})
	}
}
