package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// A re-composed (↑-edit) fragment carries the WHOLE merged bundle in a single
// send's Text — the card must show each part on its own line (the "steer 7 /
// steer 8a as one line" UI glitch in session 8d9c51278).
func TestSteer_RecomposedFragmentRendersPerPart(t *testing.T) {
	m, _ := newSteerModel(t, true)
	m = startRunning(t, m, "original")

	m = enqueueSteer(t, m, "steer 7")
	m = enqueueSteer(t, m, "steer 8")
	mm, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	m = mm.(Model)
	runBatchLeaves(cmd)
	if got := m.ta.Value(); got != "steer 7"+queueMergeSep+"steer 8" {
		t.Fatalf("↑ must pull the merged bundle into the textarea, got %q", got)
	}
	m = typeText(t, m, "a")
	mm2, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = mm2.(Model)
	runBatchLeaves(cmd)

	card := stripANSIstr(m.renderSteer())
	if !strings.Contains(card, "steer 7") || !strings.Contains(card, "steer 8a") {
		t.Fatalf("the card must show both parts, got:\n%s", card)
	}
	i7 := strings.Index(card, "steer 7")
	i8 := strings.Index(card, "steer 8a")
	if i7 < 0 || i8 < 0 {
		t.Fatalf("the card must show both parts, got:\n%s", card)
	}
	// The parts live on DIFFERENT rendered lines — a single-line merge would
	// put them on the same \n-free span.
	if !strings.Contains(card[i7:i8], "\n") {
		t.Fatalf("the parts must render on separate lines, got:\n%s", card)
	}
}
