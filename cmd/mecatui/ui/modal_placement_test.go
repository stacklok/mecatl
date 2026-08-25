package ui

import (
	"context"
	"testing"

	tea "charm.land/bubbletea/v2"
)

type placementTestSurface struct {
	body string
}

func (s *placementTestSurface) Render(_, _ int) (string, []ClickableRegion) {
	return s.body, nil
}

func (*placementTestSurface) HandleKey(tea.KeyPressMsg) (tea.Cmd, bool, bool) {
	return nil, false, false
}

func (*placementTestSurface) HandleMsg(tea.Msg) (tea.Cmd, bool, bool) {
	return nil, false, false
}

func (*placementTestSurface) HandleWheel(tea.MouseWheelMsg) (tea.Cmd, bool) {
	return nil, false
}

func (*placementTestSurface) Close() {}

func modalPlacementTestModel() Model {
	m := New(Deps{Theme: testTheme(), Ctx: context.Background(), NoAltScreen: true})
	m.width = 80
	m.vp.SetHeight(24)
	return m
}

func TestModalDefaultsToCenteredCardPlacement(t *testing.T) {
	m := modalPlacementTestModel()
	m.modal = &placementTestSurface{body: "modal body"}

	got := m.renderBody()
	want := centerCard(m.deps.Theme, "modal body", m.width, m.vp.Height())
	if got != want {
		t.Fatalf("default modal placement = %q, want centered card %q", got, want)
	}
}

func TestSessionsModalPlacement(t *testing.T) {
	for _, tc := range []struct {
		name        string
		view        sessionsView
		maintenance maintenanceView
	}{
		{name: "picker", view: sessionsPanel},
		{name: "maintenance", view: sessionsPanel, maintenance: maintenanceOptimizePlan},
		{name: "transcript", view: sessionsTranscript},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := modalPlacementTestModel()
			st := m.newSessionsSurface(false)
			st.view = tc.view
			st.maintenance = tc.maintenance
			if tc.view == sessionsTranscript {
				st.transcript.addUser("history")
			}

			body, _ := st.Render(m.width, m.vp.Height())
			if got := m.renderBody(); got != body {
				t.Fatalf("sessions %s placement = %q, want fill body %q", tc.name, got, body)
			}
		})
	}
}
