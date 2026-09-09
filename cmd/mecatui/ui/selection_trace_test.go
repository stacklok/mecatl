package ui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestSelectionTraceCapturesContentFreeViewportAndGestureState(t *testing.T) {
	m, _ := selModel(t)
	var records []SelectionTraceRecord
	m.deps.SelectionTrace = func(r SelectionTraceRecord) { records = append(records, r) }

	m.refreshView()
	_, _ = pressMouse(m, tea.MouseLeft, 4, convTopRow(m)+1)

	if len(records) < 2 {
		t.Fatalf("trace records = %d, want content replacement and mouse down", len(records))
	}
	content := records[0]
	if content.Event != "viewport.content_replace" || content.ViewportBytes == 0 || content.FrameLines == 0 {
		t.Fatalf("content trace = %#v, want populated content-free structural replacement state", content)
	}
	var gesture SelectionTraceRecord
	for _, record := range records {
		if record.Event == "selection.mouse_down" {
			gesture = record
			break
		}
	}
	if gesture.Event == "" || gesture.ViewDirty || gesture.Follow != (m.conversationView.mode == followTail) || gesture.YOffset != m.vp.YOffset() || gesture.AtBottom != m.vp.AtBottom() {
		t.Fatalf("gesture trace = %#v, want current viewport state", gesture)
	}
}

func TestSelectionTraceCapturesScrollAndReleaseTransitions(t *testing.T) {
	m, _ := selModel(t)
	var events []string
	m.deps.SelectionTrace = func(r SelectionTraceRecord) { events = append(events, r.Event) }

	updated, _ := m.onMouseWheel(tea.MouseWheelMsg{})
	m = updated.(Model)
	updated, _ = m.onScrollKey(tea.KeyPressMsg{})
	m = updated.(Model)
	updated, _ = m.onMouseRelease(tea.Mouse{Button: tea.MouseLeft})
	m = updated.(Model)

	for _, want := range []string{"viewport.mouse_wheel", "viewport.keyboard_scroll", "selection.mouse_release"} {
		found := false
		for _, event := range events {
			found = found || event == want
		}
		if !found {
			t.Errorf("trace events %v missing %q", events, want)
		}
	}
}

func TestSelectionTraceDisabledIsNilAndDoesNoCallbackWork(t *testing.T) {
	m, _ := selModel(t)
	m.deps.SelectionTrace = nil
	m.refreshView()
	m.traceSelection("selection.snapshot")
}
