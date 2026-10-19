package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// Re-composed fragments keep the merged message in the body preview, separate
// from the pending-state heading.
func TestSteer_RecomposedFragmentRendersPendingUserPrompt(t *testing.T) {
	m, _ := newSteerModel(t, true)
	m = startRunning(t, m, "original")

	m = enqueueSteer(t, m, "steer 7")
	m = enqueueSteer(t, m, "steer 8")
	mm, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	m = mm.(Model)
	runBatchLeaves(cmd)
	if got := m.prompt.Value(); got != "/steer steer 7"+queueMergeSep+"steer 8" {
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
	lines := strings.Split(card, "\n")
	if len(lines) != 4 || strings.Contains(lines[0], "steer 7") || lines[1] != " │ steer 7" || lines[2] != " │ " || lines[3] != " │ steer 8a" {
		t.Fatalf("queued steer must separate heading and logical message previews, got:\n%s", card)
	}
}
