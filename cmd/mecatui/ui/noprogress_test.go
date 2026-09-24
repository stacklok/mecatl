package ui

import (
	"strings"
	"testing"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

// countNoticeBlocks returns how many scrollback notice blocks the conversation holds.
func countNoticeBlocks(m Model) int {
	n := 0
	for i := range m.conv.testBlocks() {
		if m.conv.testBlocks()[i].kind == blockNotice {
			n++
		}
	}
	return n
}

// TestNoProgressAdvisoryIsTransientNotScrollback asserts AC10: a NoProgressMsg routes to
// the transient footer status, NOT scrollback — a successful nudge-recover leaves no
// permanent residue. The text reaches m.statusMsg; no blockNotice is appended.
func TestNoProgressAdvisoryIsTransientNotScrollback(t *testing.T) {
	m, _, _ := newTestModel(t, theme.New("aztec", theme.AztecPalette()))
	m.phase = phaseRunning
	before := countNoticeBlocks(m)

	m = applyAll(m, client.NoProgressMsg{Text: "model produced no tool call or text; nudging to continue"})

	if got := countNoticeBlocks(m); got != before {
		t.Errorf("NoProgressMsg added %d scrollback notice block(s); it must be transient", got-before)
	}
	if !strings.Contains(stripANSIstr(m.statusMsg), "no progress") {
		t.Errorf("NoProgressMsg must populate the transient footer status, got %q", stripANSIstr(m.statusMsg))
	}
}

// TestCompactionStillScrollbackNotice asserts the split did not regress compaction: a
// CompactionMsg STILL adds a durable scrollback notice (the compaction boundary is a
// fact worth keeping in the transcript).
func TestCompactionStillScrollbackNotice(t *testing.T) {
	m, _, _ := newTestModel(t, theme.New("aztec", theme.AztecPalette()))
	m.phase = phaseRunning
	before := countNoticeBlocks(m)

	m = applyAll(m, client.CompactionMsg{Text: "summarised 12 messages"})

	if got := countNoticeBlocks(m); got != before+1 {
		t.Errorf("CompactionMsg must still add exactly one scrollback notice; added %d", got-before)
	}
}

// TestNoProgressTerminalStopStillShown confirms the durable terminal signal survives the
// scrollback removal: the terminal no-progress stop is rendered by the footer's
// stopReasonLabel as "stopped · no progress", independent of any scrollback notice.
func TestNoProgressTerminalStopStillShown(t *testing.T) {
	text, slot := stopReasonLabel("no_progress")
	if text != "stopped · no progress" {
		t.Errorf("terminal no-progress footer label = %q, want %q", text, "stopped · no progress")
	}
	if slot != slotCtxWarn {
		t.Errorf("terminal no-progress slot = %q, want %q", slot, slotCtxWarn)
	}
}
