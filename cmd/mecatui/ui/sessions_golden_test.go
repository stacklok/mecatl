package ui

import (
	"context"
	"errors"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

// errGolden is the canned ListSessions error the error-golden test drives.
var errGolden = errors.New("could not reach session store")

// sessions_golden_test.go locks the /sessions picker overlay's rendered frames
// (issue #245 Phase 3a). Mirrors models_test.go's golden pattern: build an idle
// model, open the picker, feed the ListSessions result, capture View().Content
// (ANSI-stripped) into a testdata golden. Run `task test:golden -update` to
// refresh.

// newSessionsGoldenModel builds a sized, idle, no-alt-screen model wired with a
// fake session lister + replayer, ready to open the picker.
func newSessionsGoldenModel(t *testing.T, sessions []client.SessionListItem) Model {
	t.Helper()
	fl := &fakeSessionLister{sessions: sessions}
	conv := &fakeConv{recv: &fakeRecver{gate: make(chan struct{})}, send: &fakeSender{}, caps: client.Capabilities{}}
	m := New(Deps{
		Session:     conv,
		Conv:        conv,
		Sessions:    fl,
		Replayer:    &fakeSessionReplayer{},
		Theme:       theme.New("aztec", theme.AztecPalette()),
		Workspace:   "/workspace",
		Mode:        "default",
		Model:       "mock-model",
		Ctx:         context.Background(),
		NoAltScreen: true,
	})
	m = applyAll(m,
		tea.WindowSizeMsg{Width: 100, Height: 40},
		client.SessionReadyMsg{SessionID: "sess-test-0001", Capabilities: client.Capabilities{}},
	)
	return m
}

// openAndLoad opens the picker and feeds the ListSessions result, returning the
// model with the panel populated.
func openAndLoad(t *testing.T, m Model, sessions []client.SessionListItem) Model {
	t.Helper()
	mm, cmd := m.runSessions()
	m = feedCmd(t, mm.(Model), cmd)
	m = applyAll(m, client.SessionsListedMsg{Sessions: sessions})
	return m
}

// typeSessionsFilter feeds a string into the sessions picker's focused filter
// rune-by-rune (the sessions overlay's own onSessionsKey, NOT the models one).
func typeSessionsFilter(t *testing.T, m Model, s string) Model {
	t.Helper()
	for _, r := range s {
		mm, _, handled := m.onSessionsKey(tea.KeyPressMsg{Code: r, Text: string(r)})
		if !handled {
			t.Fatalf("sessions picker key %v should be handled", r)
		}
		m = mm.(Model)
	}
	return m
}

// TestSessionsPickerGolden locks the populated picker with the state badge +
// relative-time + turns + model id.
func TestSessionsPickerGolden(t *testing.T) {
	m := newSessionsGoldenModel(t, sampleSessions())
	m = openAndLoad(t, m, sampleSessions())
	got := stripANSI([]byte(m.View().Content))
	compareGolden(t, "sessions_picker.golden", got)
}

// TestSessionsPickerEmptyGolden locks the "enabled but empty" state.
func TestSessionsPickerEmptyGolden(t *testing.T) {
	m := newSessionsGoldenModel(t, nil)
	m = openAndLoad(t, m, nil)
	got := stripANSI([]byte(m.View().Content))
	compareGolden(t, "sessions_picker_empty.golden", got)
}

// TestSessionsPickerErrorGolden locks the ListSessions-error state.
func TestSessionsPickerErrorGolden(t *testing.T) {
	fl := &fakeSessionLister{err: errGolden}
	conv := &fakeConv{recv: &fakeRecver{gate: make(chan struct{})}, send: &fakeSender{}, caps: client.Capabilities{}}
	m := New(Deps{
		Session: conv, Conv: conv, Sessions: fl, Replayer: &fakeSessionReplayer{},
		Theme: theme.New("aztec", theme.AztecPalette()), Workspace: "/workspace",
		Mode: "default", Model: "mock-model", Ctx: context.Background(), NoAltScreen: true,
	})
	m = applyAll(m,
		tea.WindowSizeMsg{Width: 100, Height: 40},
		client.SessionReadyMsg{SessionID: "sess-test-0001", Capabilities: client.Capabilities{}},
	)
	mm, cmd := m.runSessions()
	m = feedCmd(t, mm.(Model), cmd)
	m = applyAll(m, client.SessionsListedMsg{Err: errGolden})
	got := stripANSI([]byte(m.View().Content))
	compareGolden(t, "sessions_picker_error.golden", got)
}

// TestSessionsPickerFilteredGolden locks the narrowed list (filter applied).
func TestSessionsPickerFilteredGolden(t *testing.T) {
	m := newSessionsGoldenModel(t, sampleSessions())
	m = openAndLoad(t, m, sampleSessions())
	m = typeSessionsFilter(t, m, "gpt")
	got := stripANSI([]byte(m.View().Content))
	compareGolden(t, "sessions_picker_filtered.golden", got)
}

// TestSessionsPickerNoMatchGolden locks the filter-matched-nothing note.
func TestSessionsPickerNoMatchGolden(t *testing.T) {
	m := newSessionsGoldenModel(t, sampleSessions())
	m = openAndLoad(t, m, sampleSessions())
	m = typeSessionsFilter(t, m, "zzzzz")
	got := stripANSI([]byte(m.View().Content))
	compareGolden(t, "sessions_picker_nomatch.golden", got)
}

// TestSessionsTranscriptLoadingGolden locks the read-only transcript view's loading
// arm (phaseReplay, replay open, no StreamClosed yet): "loading transcript for <id>…".
// Driven through the real switchToSession handoff so the state is production-honest.
// Uses a CHILD session (only children open the replay stream; a top-level session
// continues by default and never enters phaseReplay).
func TestSessionsTranscriptLoadingGolden(t *testing.T) {
	fr := &fakeSessionReplayer{stream: client.NewFakeEventStream()}
	m := newSessionsGoldenModel(t, []client.SessionListItem{
		{ID: "subagent-call1", ModifiedAt: nowMinusMinutes(4), State: "completed", Turns: 2, ModelID: "gpt-5"},
	})
	m.deps.Replayer = fr
	mm, _, _ := m.switchToSession(client.SessionListItem{ID: "subagent-call1"})
	m = mm.(Model)
	if m.phase != phaseReplay {
		t.Fatalf("phase = %v, want phaseReplay (child)", m.phase)
	}
	got := stripANSI([]byte(m.View().Content))
	compareGolden(t, "sessions_transcript_loading.golden", got)
}

// TestSessionsTranscriptLoadedGolden locks the transcript view's loaded arm: a
// StreamClosedMsg flips the loading card to "transcript loaded", still phaseReplay.
func TestSessionsTranscriptLoadedGolden(t *testing.T) {
	m := newSessionsGoldenModel(t, sampleSessions())
	m.sessions.replayGen = 1
	m.sessions.view = sessionsTranscript
	m.phase = phaseReplay
	mm, _ := m.updateReplayMsg(replayMsg{gen: 1, msg: client.StreamClosedMsg{}})
	m = mm.(Model)
	if !m.sessions.replayClosed {
		t.Fatal("StreamClosedMsg should set replayClosed")
	}
	got := stripANSI([]byte(m.View().Content))
	compareGolden(t, "sessions_transcript_loaded.golden", got)
}

// TestSessionsTranscriptErrorGolden locks the transcript view's error arm: a
// StreamErrMsg renders the replay error line, still phaseReplay.
func TestSessionsTranscriptErrorGolden(t *testing.T) {
	m := newSessionsGoldenModel(t, sampleSessions())
	m.sessions.replayGen = 1
	m.sessions.view = sessionsTranscript
	m.phase = phaseReplay
	mm, _ := m.updateReplayMsg(replayMsg{gen: 1, msg: client.StreamErrMsg{Err: errGolden}})
	m = mm.(Model)
	if m.sessions.replayErr == nil {
		t.Fatal("StreamErrMsg should set replayErr")
	}
	got := stripANSI([]byte(m.View().Content))
	compareGolden(t, "sessions_transcript_error.golden", got)
}

// TestSessionsTranscriptRenderedGolden locks the rendered transcript view (Slice
// 3b): a scripted 3-event replay (user prompt + assistant text + tool call +
// result) is projected into m.sessions.transcript via updateReplayMsg, then
// View() renders the transcript blocks through the SAME block renderers the live
// scrollback uses — the projection-equivalence golden. The replay state is hand-
// set (no real stream goroutine) so the scripted msgs drive updateReplayMsg
// deterministically without racing a live reader.
func TestSessionsTranscriptRenderedGolden(t *testing.T) {
	m := newSessionsGoldenModel(t, sampleSessions())
	m = setupReplayTranscript(m, sampleSessions()[0])
	msgs := []tea.Msg{
		client.UserPromptMsg{Text: "read the greeting file"},
		client.TurnStartMsg{Turn: 1},
		client.AssistantDeltaMsg{Turn: 1, Text: "Reading the greeting file."},
		client.ToolCallMsg{ID: "call-read-1", Name: "Read", Args: `{"path":"greeting.txt"}`},
		client.ToolResultMsg{CallID: "call-read-1", Content: "hello from the mecatl demo workspace"},
		client.ResultMsg{Stop: "end_turn"},
	}
	for _, msg := range msgs {
		mm, _ := m.updateReplayMsg(replayMsg{gen: m.sessions.replayGen, msg: msg})
		m = mm.(Model)
	}
	// The per-event arm coalesces via markDirtyReplay (NOT a per-event refreshView),
	// so flush the frame-cadence renderTickMsg to settle the final frame before
	// capturing View().Content — mirroring how the live coalesce tests flush.
	m = applyAll(m, renderTickMsg{})
	got := stripANSI([]byte(m.View().Content))
	compareGolden(t, "sessions_transcript_rendered.golden", got)
}
