package ui

import (
	"context"
	"testing"

	tea "charm.land/bubbletea/v2"
)

type placementTestSurface struct {
	body         string
	renderWidth  int
	renderHeight int
}

func (s *placementTestSurface) Render(width, height int) (string, []ClickableRegion) {
	s.renderWidth, s.renderHeight = width, height
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

type fillPlacementTestSurface struct{ placementTestSurface }

func (*fillPlacementTestSurface) modalPlacement() modalPlacement { return modalPlacementFill }

func modalPlacementTestModel() Model {
	m := newTestModelFromDeps(Deps{Theme: testTheme(), Ctx: context.Background(), NoAltScreen: true})
	m.width = 80
	m.height = 30
	m.vp.SetHeight(24)
	return m
}

func TestModalDefaultsToCenteredCardPlacement(t *testing.T) {
	m := modalPlacementTestModel()
	surface := &placementTestSurface{body: "modal body"}
	m.modal = surface

	got := m.renderBody()
	want := centerCard(m.deps.Theme, "modal body", m.width, m.vp.Height())
	if got != want {
		t.Fatalf("default modal placement = %q, want centered card %q", got, want)
	}
	style := m.deps.Theme.Style("askCard")
	wantWidth := m.width - style.GetBorderLeftSize() - style.GetBorderRightSize() - style.GetPaddingLeft() - style.GetPaddingRight()
	wantHeight := m.vp.Height() - style.GetBorderTopSize() - style.GetBorderBottomSize() - style.GetPaddingTop() - style.GetPaddingBottom()
	if surface.renderWidth != wantWidth || surface.renderHeight != wantHeight {
		t.Errorf("card Render dimensions = (%d,%d), want content dimensions (%d,%d)", surface.renderWidth, surface.renderHeight, wantWidth, wantHeight)
	}
	if m.metrics.outerBounds == (cellRect{}) || m.metrics.contentBounds == (cellRect{}) {
		t.Fatal("rendered card must publish concrete outer and content bounds")
	}
	wantContentBounds := cellRect{
		x0: m.metrics.contentOrigin.x,
		x1: m.metrics.contentOrigin.x + wantWidth,
		y0: m.metrics.contentOrigin.y,
		y1: m.metrics.contentOrigin.y + wantHeight,
	}
	if m.metrics.contentBounds != wantContentBounds {
		t.Errorf("card content bounds = %+v, want full offered content area %+v", m.metrics.contentBounds, wantContentBounds)
	}
}

func TestFillModalReceivesConversationBodyDimensions(t *testing.T) {
	m := modalPlacementTestModel()
	surface := &fillPlacementTestSurface{placementTestSurface: placementTestSurface{body: "fill body"}}
	m.modal = surface

	if got := m.renderBody(); got != surface.body {
		t.Fatalf("fill modal body = %q, want %q", got, surface.body)
	}
	if surface.renderWidth != m.width || surface.renderHeight != m.vp.Height() {
		t.Errorf("fill Render dimensions = (%d,%d), want conversation body (%d,%d)", surface.renderWidth, surface.renderHeight, m.width, m.vp.Height())
	}
	if m.metrics.outerBounds != m.metrics.contentBounds {
		t.Errorf("fill metrics outer/content bounds differ: %+v / %+v", m.metrics.outerBounds, m.metrics.contentBounds)
	}
}

func TestRenderedSurfaceMetricsCoordinateHelpers(t *testing.T) {
	metrics := renderedSurfaceMetrics{contentOrigin: cellPoint{x: 11, y: 7}}
	for _, tc := range [][2]int{{14, 9}, {11, 7}, {8, 4}} {
		localX, localY := metrics.globalToLocal(tc[0], tc[1])
		if globalX, globalY := metrics.localToGlobal(localX, localY); globalX != tc[0] || globalY != tc[1] {
			t.Errorf("localToGlobal(globalToLocal(%d,%d)) = (%d,%d), want original coordinates", tc[0], tc[1], globalX, globalY)
		}
	}
	if x, y := metrics.globalToLocal(14, 9); x != 3 || y != 2 {
		t.Errorf("globalToLocal = (%d,%d), want (3,2)", x, y)
	}
	if x, y := metrics.localToGlobal(3, 2); x != 14 || y != 9 {
		t.Errorf("localToGlobal = (%d,%d), want (14,9)", x, y)
	}
}

func TestSessionsModalPlacement(t *testing.T) {
	for _, tc := range []struct {
		name        string
		view        sessionsView
		maintenance maintenanceView
	}{
		{name: "picker", view: sessionsPanel},
		{name: "maintenance", view: sessionsPanel, maintenance: maintenanceCleanupPlan},
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
