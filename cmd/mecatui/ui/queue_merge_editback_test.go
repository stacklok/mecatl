package ui

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
)

// pressUp sends the EditBack key (↑) and returns the updated model (running the
// returned command's leaves so any submit frame fires).
func pressUp(t *testing.T, m Model) Model {
	t.Helper()
	mm, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	runBatchLeaves(cmd)
	return mm.(Model)
}

// TestEditBackRunningPullsMergedQueue: ↑ on an EMPTY input while running with a
// non-empty queue pulls the whole queue (merged by queueMergeSep) into the textarea,
// clears the queue, and sends NOTHING. Non-destructive: the merged text becomes an
// editable draft.
func TestEditBackRunningPullsMergedQueue(t *testing.T) {
	m, conv := newQueueModel(t)
	m = startRunning(t, m, "first")
	m = enqueue(t, m, "second")
	m = enqueue(t, m, "third")

	m = pressUp(t, m)

	if got := m.prompt.Value(); got != "second"+queueMergeSep+"third" {
		t.Fatalf("edit-back must load the merged queue into the input, got %q", got)
	}
	if len(m.queued) != 0 {
		t.Fatalf("edit-back must clear the queue, got %v", m.queued)
	}
	if m.queuePaused != "" {
		t.Fatalf("edit-back must clear any pause, got %q", m.queuePaused)
	}
	if m.phase != phaseRunning {
		t.Errorf("edit-back must not change the phase, got %d", m.phase)
	}
	// Only the initial "first" frame — edit-back sends nothing.
	if got := promptTexts(conv.send); len(got) != 1 || got[0] != "first" {
		t.Fatalf("edit-back must not send a prompt, frames = %v", got)
	}
}

// TestEditBackNonEmptyInputIsNoOp: ↑ with a NON-EMPTY draft is not an edit-back — it
// falls through to the textarea/scroll default, leaving the queue intact and the
// input unchanged in content (a plain cursor/scroll key).
func TestEditBackNonEmptyInputIsNoOp(t *testing.T) {
	m, _ := newQueueModel(t)
	m = startRunning(t, m, "first")
	m = enqueue(t, m, "second")
	m = typeText(t, m, "draft")

	m = pressUp(t, m)

	if len(m.queued) != 1 || m.queued[0] != "second" {
		t.Fatalf("↑ over a draft must leave the queue intact, got %v", m.queued)
	}
	if !strings.Contains(m.prompt.Value(), "draft") {
		t.Errorf("↑ over a draft must not wipe the input, got %q", m.prompt.Value())
	}
}

// TestEditBackEmptyQueueIsNoOp: ↑ on an empty input with an EMPTY queue is not an
// edit-back (it falls through to the textarea default) — nothing to pull back.
func TestEditBackEmptyQueueIsNoOp(t *testing.T) {
	m, _ := newQueueModel(t)
	m = startRunning(t, m, "first")

	m = pressUp(t, m)

	if len(m.queued) != 0 {
		t.Fatalf("no queue: ↑ must not fabricate one, got %v", m.queued)
	}
	if strings.TrimSpace(m.prompt.Value()) != "" {
		t.Errorf("no queue: ↑ must leave the empty input empty, got %q", m.prompt.Value())
	}
}

// TestEditBackIdlePausedPullsMergedQueue: ↑ on an EMPTY input while idle with a
// PAUSED queue (a run ended on a non-clean stop) pulls the merged queue back for
// editing and clears both the queue and the pause. Distinct from esc (clear-all): the
// text survives in the textarea.
func TestEditBackIdlePausedPullsMergedQueue(t *testing.T) {
	m, conv := newQueueModel(t)
	m = startRunning(t, m, "first")
	m = enqueue(t, m, "second")
	m = enqueue(t, m, "third")
	// A user-cancel PAUSES the queue and lands idle.
	mm, cmd := m.Update(client.ResultMsg{Stop: "cancelled"})
	m = mm.(Model)
	runBatchLeaves(cmd)
	if m.queuePaused == "" || m.phase != phaseIdle {
		t.Fatalf("precondition: expected paused+idle, got paused=%q phase=%d", m.queuePaused, m.phase)
	}

	m = pressUp(t, m)

	if got := m.prompt.Value(); got != "second"+queueMergeSep+"third" {
		t.Fatalf("idle edit-back must load the merged queue, got %q", got)
	}
	if len(m.queued) != 0 || m.queuePaused != "" {
		t.Fatalf("idle edit-back must clear queue+pause, got queued=%v paused=%q", m.queued, m.queuePaused)
	}
	// No second frame — edit-back sends nothing.
	if got := promptTexts(conv.send); len(got) != 1 {
		t.Fatalf("idle edit-back must not submit, frames = %v", got)
	}
}

// TestEscStillClearsAllNotEditBack: esc (the clear-all key) still DROPS the queue
// outright — edit-back did not steal esc's meaning. Pins the non-regression that ↑
// and esc are distinct: ↑ preserves, esc destroys.
func TestEscStillClearsAllNotEditBack(t *testing.T) {
	m, _ := newQueueModel(t)
	m = startRunning(t, m, "first")
	m = enqueue(t, m, "second")

	mm, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	m = mm.(Model)

	if len(m.queued) != 0 {
		t.Fatalf("esc must clear the queue outright, got %v", m.queued)
	}
	if strings.TrimSpace(m.prompt.Value()) != "" {
		t.Errorf("esc-clear must not load the queue into the input, got %q", m.prompt.Value())
	}
}

// TestHardErrorStillPauses: a HARD error (Transient false) still PAUSES — the queue
// is kept and marked with the reason, no auto-submit. Both a result error and a
// stream error.
func TestHardErrorStillPauses(t *testing.T) {
	t.Run("result error", func(t *testing.T) {
		m, conv := newQueueModel(t)
		m = startRunning(t, m, "first")
		m = enqueue(t, m, "second")

		mm, cmd := m.Update(client.ResultMsg{Stop: stopError, Error: "invalid request", Transient: false})
		m = mm.(Model)
		runBatchLeaves(cmd)

		if len(m.queued) != 1 || m.queuePaused != stopError {
			t.Fatalf("hard error must pause the queue, got queued=%v paused=%q", m.queued, m.queuePaused)
		}
		if got := promptTexts(conv.send); len(got) != 1 {
			t.Fatalf("hard error must not auto-submit, frames = %v", got)
		}
	})
	t.Run("stream error", func(t *testing.T) {
		m, conv := newQueueModel(t)
		m = startRunning(t, m, "first")
		m = enqueue(t, m, "second")

		mm, cmd := m.Update(client.StreamErrMsg{Err: errors.New("permission denied"), Transient: false})
		m = mm.(Model)
		runBatchLeaves(cmd)

		if len(m.queued) != 1 || m.queuePaused != stopError {
			t.Fatalf("hard stream error must pause, got queued=%v paused=%q", m.queued, m.queuePaused)
		}
		if got := promptTexts(conv.send); len(got) != 1 {
			t.Fatalf("hard stream error must not auto-submit, frames = %v", got)
		}
	})
}

// TestCancelStillPausesEvenIfTransientFlag: a user-cancel PAUSES regardless of
// legacy display classification. Pins that the USER's intent to stop is never fought.
func TestCancelStillPausesEvenIfTransientFlag(t *testing.T) {
	m, conv := newQueueModel(t)
	m = startRunning(t, m, "first")
	m = enqueue(t, m, "second")

	mm, cmd := m.Update(client.ResultMsg{Stop: "cancelled", Transient: true})
	m = mm.(Model)
	runBatchLeaves(cmd)

	if len(m.queued) != 1 || m.queuePaused != "cancelled" {
		t.Fatalf("cancel must pause even with a transient flag, got queued=%v paused=%q", m.queued, m.queuePaused)
	}
	if got := promptTexts(conv.send); len(got) != 1 {
		t.Fatalf("cancel must not auto-submit, frames = %v", got)
	}
}

// TestMaxConsecutiveFailuresStillPausesEvenIfTransientFlag: a
// `max_consecutive_failures` stop PAUSES regardless of legacy display classification.
func TestMaxConsecutiveFailuresStillPausesEvenIfTransientFlag(t *testing.T) {
	m, conv := newQueueModel(t)
	m = startRunning(t, m, "first")
	m = enqueue(t, m, "second")

	mm, cmd := m.Update(client.ResultMsg{Stop: "max_consecutive_failures", Error: "engine overloaded", Transient: true})
	m = mm.(Model)
	runBatchLeaves(cmd)

	if len(m.queued) != 1 || m.queuePaused != "max_consecutive_failures" {
		t.Fatalf("max_consecutive_failures must pause even with a transient flag, got queued=%v paused=%q", m.queued, m.queuePaused)
	}
	if got := promptTexts(conv.send); len(got) != 1 {
		t.Fatalf("max_consecutive_failures must not auto-submit, frames = %v", got)
	}
}

// TestStreamClosedPausesQueue: a clean stream close (io.EOF → StreamClosedMsg) while
// a run is streaming with a non-empty queue PAUSES and KEEPS the queue. A close has
// no typed semantic commit facts, so it can never authorize replay or queue drain.
func TestStreamClosedPausesQueue(t *testing.T) {
	m, conv := newQueueModel(t)
	m = startRunning(t, m, "first")
	m = enqueue(t, m, "second")
	if m.phase != phaseRunning {
		t.Fatalf("precondition: expected phaseRunning, got %d", m.phase)
	}

	mm, cmd := m.Update(client.StreamClosedMsg{})
	m = mm.(Model)
	runBatchLeaves(cmd)

	if len(m.queued) != 1 || m.queuePaused != "closed" {
		t.Fatalf("clean close must pause+keep the queue, got queued=%v paused=%q", m.queued, m.queuePaused)
	}
	if got := promptTexts(conv.send); len(got) != 1 {
		t.Fatalf("clean close must not auto-submit, frames = %v", got)
	}
}

// TestEditBackSetsEditingStatusAndFocus: edit-back is more than a value swap — it must
// leave the pulled-back text as an EDITABLE, focused draft (it returns afterInputEdit,
// which re-focuses/re-syncs the input) and set a muted "editing" status so the user
// knows the queue moved into the input. A regression dropping afterInputEdit (leaving
// the text unfocused) or the status would pass the value-only assertions elsewhere.
func TestEditBackSetsEditingStatusAndFocus(t *testing.T) {
	m, _ := newQueueModel(t)
	m = startRunning(t, m, "first")
	m = enqueue(t, m, "second")

	m = pressUp(t, m)

	if got := stripANSIstr(m.statusMsg); !strings.Contains(got, "editing") {
		t.Errorf("edit-back must set an 'editing' status, got %q", got)
	}
	if !m.prompt.Focused() {
		t.Error("edit-back must leave the input focused for editing")
	}
}
