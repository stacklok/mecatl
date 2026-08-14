package ui

import (
	"context"
	"errors"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

var errGolden = errors.New("could not reach session store")

func newSessionsGoldenModel(t *testing.T, sessions []client.SessionListItem) Model {
	t.Helper()
	conv := newSessionsConv()
	loader := &fakeSessionTranscriptLoader{}
	m := New(Deps{
		Session: conv, Conv: conv, Sessions: &fakeSessionLister{sessions: sessions}, Transcript: loader,
		Theme: theme.New("aztec", theme.AztecPalette()), Workspace: "/workspace",
		Mode: "default", Model: "mock-model", Ctx: context.Background(), NoAltScreen: true,
	})
	return applyAll(m, tea.WindowSizeMsg{Width: 100, Height: 40}, client.SessionReadyMsg{SessionID: "sess-test-0001"})
}

func openAndLoad(t *testing.T, m Model, sessions []client.SessionListItem) Model {
	t.Helper()
	mm, _ := m.runSessions()
	return applyAll(mm.(Model), client.SessionsListedMsg{Sessions: sessions})
}

func TestSessionsPickerGolden(t *testing.T) {
	rows := sampleSessions()
	m := openAndLoad(t, newSessionsGoldenModel(t, rows), rows)
	compareGolden(t, "sessions_picker.golden", stripANSI([]byte(m.View().Content)))
}

func TestSessionsPickerEmptyGolden(t *testing.T) {
	m := openAndLoad(t, newSessionsGoldenModel(t, nil), nil)
	compareGolden(t, "sessions_picker_empty.golden", stripANSI([]byte(m.View().Content)))
}

func TestSessionsPickerErrorGolden(t *testing.T) {
	m := newSessionsGoldenModel(t, nil)
	mm, _ := m.runSessions()
	m = applyAll(mm.(Model), client.SessionsListedMsg{Err: errGolden})
	compareGolden(t, "sessions_picker_error.golden", stripANSI([]byte(m.View().Content)))
}

func TestSessionsTranscriptLoadingGolden(t *testing.T) {
	m := newSessionsGoldenModel(t, nil)
	m.sessions = sessionsState{view: sessionsTranscript, loading: true, selected: client.SessionListItem{ID: "sched-1", Title: "Nightly checks"}, inspect: true}
	m.phase = phaseReplay
	compareGolden(t, "sessions_transcript_loading.golden", stripANSI([]byte(m.View().Content)))
}

func TestSessionsTranscriptErrorGolden(t *testing.T) {
	m := newSessionsGoldenModel(t, nil)
	m.sessions = sessionsState{view: sessionsTranscript, selected: client.SessionListItem{ID: "sched-1", Title: "Nightly checks"}, inspect: true, loadErr: errGolden}
	m.phase = phaseReplay
	compareGolden(t, "sessions_transcript_error.golden", stripANSI([]byte(m.View().Content)))
}

func TestSessionsTranscriptRenderedGolden(t *testing.T) {
	m := newSessionsGoldenModel(t, nil)
	m.sessions = sessionsState{
		view: sessionsTranscript, selected: client.SessionListItem{ID: "sched-1", Title: "Nightly checks"}, inspect: true,
		transcript: conversationFromTranscript([]client.ConversationMessage{{Role: "user", Text: "run checks"}, {Role: "assistant", Text: "All checks passed."}}),
	}
	m.phase = phaseReplay
	m.refreshView()
	compareGolden(t, "sessions_transcript_rendered.golden", stripANSI([]byte(m.View().Content)))
}
