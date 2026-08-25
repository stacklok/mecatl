package ui

import (
	"context"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
)

type surfaceIntentTestSurface struct {
	intent         surfaceIntent
	keyHandled     bool
	closeRequested bool
	didClose       bool
}

func (*surfaceIntentTestSurface) Render(int, int) (string, []ClickableRegion) { return "", nil }

func (s *surfaceIntentTestSurface) HandleKey(tea.KeyPressMsg) (tea.Cmd, bool, bool) {
	return nil, s.keyHandled, s.closeRequested
}

func (*surfaceIntentTestSurface) HandleMsg(tea.Msg) (tea.Cmd, bool, bool) { return nil, true, false }
func (*surfaceIntentTestSurface) HandleWheel(tea.MouseWheelMsg) (tea.Cmd, bool) {
	return nil, true
}
func (s *surfaceIntentTestSurface) Close() {
	s.didClose = true
	s.intent = nil
}

func (s *surfaceIntentTestSurface) takeSurfaceIntent() surfaceIntent {
	intent := s.intent
	s.intent = nil
	return intent
}

func TestDispatchSurfaceKeyAppliesHandledIntentOnce(t *testing.T) {
	surface := &surfaceIntentTestSurface{
		intent:     sessionsPhaseIntent{phase: sessionsIntentPhaseIdle},
		keyHandled: true,
	}
	m := New(Deps{Ctx: context.Background(), Theme: testTheme(), NoAltScreen: true})
	m.phase = phaseReplay
	m.modal = surface

	got, _, handled := m.dispatchSurfaceKey(tea.KeyPressMsg{})
	if !handled {
		t.Fatal("dispatchSurfaceKey() handled = false, want true")
	}
	gotModel := got.(Model)
	if gotModel.phase != phaseIdle {
		t.Fatalf("dispatchSurfaceKey() phase = %v, want %v", gotModel.phase, phaseIdle)
	}
	if surface.intent != nil {
		t.Fatal("intent was not cleared while taking it")
	}

	gotModel.phase = phaseReplay
	got, _, _ = gotModel.dispatchSurfaceKey(tea.KeyPressMsg{})
	gotModel = got.(Model)
	if gotModel.phase != phaseReplay {
		t.Fatalf("second dispatchSurfaceKey() phase = %v, want unchanged %v", gotModel.phase, phaseReplay)
	}
}

func TestDispatchSurfaceKeyAppliesIntentBeforeClose(t *testing.T) {
	surface := &surfaceIntentTestSurface{
		intent:         sessionsPhaseIntent{phase: sessionsIntentPhaseIdle},
		keyHandled:     true,
		closeRequested: true,
	}
	m := New(Deps{Ctx: context.Background(), Theme: testTheme(), NoAltScreen: true})
	m.phase = phaseReplay
	m.modal = surface

	got, _, handled := m.dispatchSurfaceKey(tea.KeyPressMsg{})
	if !handled {
		t.Fatal("dispatchSurfaceKey() handled = false, want true")
	}
	gotModel := got.(Model)
	if gotModel.phase != phaseIdle || gotModel.modal != nil || !surface.didClose {
		t.Fatalf("intent/close ordering: phase=%v modal=%T closed=%v", gotModel.phase, gotModel.modal, surface.didClose)
	}
}

func TestDispatchSurfaceMsgAdoptsTranscriptIntent(t *testing.T) {
	m := New(Deps{Ctx: context.Background(), Theme: testTheme(), NoAltScreen: true})
	loaded := conversation{}
	loaded.addUser("continued")
	m.modal = &sessionsState{intent: sessionsTranscriptAdoptionIntent{
		row:        client.SessionListItem{ID: "continued-session", Title: "continued"},
		transcript: loaded,
	}}

	got, _, handled := m.dispatchSurfaceMsg(storageHealthLoadedMsg{})
	if !handled {
		t.Fatal("dispatchSurfaceMsg() handled = false, want true")
	}
	gotModel := got.(Model)
	if gotModel.sessionID != "continued-session" || gotModel.modal != nil || gotModel.conv.isEmpty() {
		t.Fatalf("adopted model id=%q modal=%T empty=%v", gotModel.sessionID, gotModel.modal, gotModel.conv.isEmpty())
	}
}

func TestDispatchSurfaceKeyIgnoresUnhandledIntent(t *testing.T) {
	m := Model{
		phase: phaseReplay,
		modal: &surfaceIntentTestSurface{
			intent:     sessionsPhaseIntent{phase: sessionsIntentPhaseIdle},
			keyHandled: false,
		},
	}

	got, _, handled := m.dispatchSurfaceKey(tea.KeyPressMsg{})
	if handled {
		t.Fatal("dispatchSurfaceKey() handled = true, want false")
	}
	gotModel := got.(Model)
	if gotModel.phase != phaseReplay {
		t.Fatalf("dispatchSurfaceKey() phase = %v, want %v", gotModel.phase, phaseReplay)
	}
}
