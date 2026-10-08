package ui

import (
	"reflect"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

type fillHitDispatchSurface struct{ hitDispatchSurface }

func (*fillHitDispatchSurface) modalPlacement() modalPlacement { return modalPlacementFill }

func TestModalFrameLifecycleSharedAcrossPlacements(t *testing.T) {
	for _, fill := range []bool{false, true} {
		t.Run(map[bool]string{false: "card", true: "fill"}[fill], func(t *testing.T) {
			m, s := hitDispatchModel(t)
			firstWheel, wheelSpy := hitDispatchModel(t)
			if fill {
				m.modal = &fillHitDispatchSurface{hitDispatchSurface: *s}
				s = &m.modal.(*fillHitDispatchSurface).hitDispatchSurface
				firstWheel.modal = &fillHitDispatchSurface{hitDispatchSurface: *wheelSpy}
				wheelSpy = &firstWheel.modal.(*fillHitDispatchSurface).hitDispatchSurface
			}
			wheelModel, _ := firstWheel.onMouseWheel(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
			firstWheel = wheelModel.(Model)
			wheelW, wheelH := firstWheel.width, firstWheel.vp.Height()
			if !fill {
				style := firstWheel.deps.Theme.Style("askCard")
				wheelW -= style.GetHorizontalFrameSize()
				wheelH -= style.GetVerticalFrameSize()
			}
			if len(wheelSpy.renders) != 1 || wheelSpy.renders[0] != ([2]int{wheelW, wheelH}) || wheelSpy.wheels != 1 || len(firstWheel.hits.frame) != 0 || *firstWheel.metrics != (renderedSurfaceMetrics{}) {
				t.Fatal("first wheel must prepare exactly once without publishing hits")
			}
			m.deps.Theme = theme.Solar()
			s.deps.caps.UserModel = true
			m.keys = applyKeyOverrides(defaultKeys(), overrideAll())
			m.hits.clear()
			m.metrics.clear()
			mm, _, handled := m.dispatchSurfaceKey(tea.KeyPressMsg{Code: 'z', Text: "z"})
			m = mm.(Model)
			wantW, wantH := m.width, m.vp.Height()
			if !fill {
				style := m.deps.Theme.Style("askCard")
				wantW -= style.GetHorizontalFrameSize()
				wantH -= style.GetVerticalFrameSize()
			}
			if !handled || len(s.renders) != 1 || s.keys != 1 || s.renders[0] != ([2]int{wantW, wantH}) {
				t.Fatalf("first key preparation: renders=%v keys=%d handled=%v", s.renders, s.keys, handled)
			}
			if s.deps.theme.Name != m.deps.Theme.Name || s.deps.marks != m.helpKeyMarkings() || !reflect.DeepEqual(s.deps.keys, m.keys) || !s.deps.caps.UserModel || s.deps.hits != m.hits || len(m.hits.frame) != 0 || *m.metrics != (renderedSurfaceMetrics{}) {
				t.Fatal("presentation or undisplayed frame state after key")
			}
			prepared := firstHitID(s)
			mm, _ = m.onMouseWheel(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
			m = mm.(Model)
			if len(s.renders) != 2 || s.wheels != 1 || len(m.hits.frame) != 0 || *m.metrics != (renderedSurfaceMetrics{}) {
				t.Fatal("wheel must prepare once and discard unpublished frame")
			}
			wheelID := firstHitID(s)
			for _, id := range []HitID{prepared, wheelID} {
				mm, _, _ = m.dispatchSurfaceMsg(surfaceHitMsg{ID: id})
				m = mm.(Model)
			}
			if s.hitMsgs != 0 {
				t.Fatal("prepared IDs reached modal before View")
			}

			_ = m.View()
			if len(m.hits.frame) != 1 {
				t.Fatal("View did not publish hit")
			}
			id := m.hits.frame[0].id
			for _, stale := range []HitID{prepared, wheelID} {
				mm, _, _ = m.dispatchSurfaceMsg(surfaceHitMsg{ID: stale})
				m = mm.(Model)
			}
			if s.hitMsgs != 0 {
				t.Fatal("prepared hit became actionable after View")
			}
			x, y := m.metrics.localToGlobal(3, 1)
			count := len(s.renders)
			mm, _, _ = m.dispatchSurfaceHit(x, y)
			m = mm.(Model)
			if len(s.renders) != count || len(s.received) != 1 || s.received[0].ID != id {
				t.Fatal("displayed click must dispatch without preparation")
			}
			mm, _ = m.onResize(tea.WindowSizeMsg{Width: 70, Height: 22})
			m = mm.(Model)
			if len(s.renders) != count || len(m.hits.frame) != 0 || *m.metrics != (renderedSurfaceMetrics{}) {
				t.Fatal("resize must invalidate without rendering")
			}
			mm, _, _ = m.dispatchSurfaceHit(x, y)
			m = mm.(Model)
			mm, _, _ = m.dispatchSurfaceMsg(surfaceHitMsg{ID: id})
			m = mm.(Model)
			if len(s.received) != 1 || s.hitMsgs != 1 {
				t.Fatal("resized old hit reached surface")
			}
			m.vp.SetHeight(0)
			_ = (&m).renderModalSurface()
			if s.renders[len(s.renders)-1] != ([2]int{0, 0}) || s.hits != nil || len(m.hits.frame) != 0 || *m.metrics != (renderedSurfaceMetrics{}) {
				t.Fatal("zero offer must clear surface and parent frame")
			}
			count = len(s.renders)
			mm, _ = m.onMouseWheel(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
			m = mm.(Model)
			if len(s.renders) != count+1 || s.renders[count] != ([2]int{0, 0}) || s.wheels != 1 {
				t.Fatal("zero-size wheel must prepare and consume without dispatch")
			}
		})
	}
}

func TestModalPresentationInvalidatesOwnerRenderCaches(t *testing.T) {
	m, _ := hitDispatchModel(t)
	sessions := &sessionsState{deps: (&m).surfaceDeps(), transcriptRend: newRenderer(m.deps.Theme, m.helpKeyMarkings())}
	m.modal = sessions
	m.deps.Theme = theme.Solar()
	m.width = 0
	_ = (&m).renderModalSurface()
	if sessions.transcriptRend != nil || sessions.deps.theme.Name != "solar" {
		t.Fatal("sessions retained old transcript palette")
	}

	m, _ = hitDispatchModel(t)
	approval := &approvalSurface{deps: (&m).surfaceDeps(), render: newApprovalRender(m.rend), planVPReady: true, argsVPReady: true}
	m.modal = approval
	m.deps.Theme = theme.Solar()
	m.width = 0
	_ = (&m).renderModalSurface()
	if approval.planVPReady || approval.argsVPReady || approval.render.markdown == nil || approval.deps.theme.Name != "solar" {
		t.Fatal("approval retained old themed content or renderer")
	}
}

func TestSessionsZeroRenderKeepsHiddenActionsGated(t *testing.T) {
	m, _ := hitDispatchModel(t)
	s := sessionsScenarioState(sessionsScenarioRows(2))
	s.deps = (&m).surfaceDeps()
	m.modal = s
	if body, _ := s.Render(m.width, m.vp.Height()); body == "" || s.compact {
		t.Fatal("sized sessions inventory must start in normal mode")
	}
	m.width = 0
	if body, regions := s.Render(0, 0); body != "" || len(regions) != 0 || !s.compact || s.rowBudget != 0 {
		t.Fatal("zero render did not enter close-only mode")
	}
	for _, action := range []string{"f", "d", "y"} {
		mm, cmd, handled := m.dispatchSurfaceKey(sessionsScenarioKey(action))
		m = mm.(Model)
		if !handled || cmd != nil || s.intent != nil || s.actionLoading || s.confirmDelete || s.actionID != "" {
			t.Fatalf("hidden action %q escaped zero-size gate", action)
		}
	}
	mm, _, handled := m.dispatchSurfaceKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	m = mm.(Model)
	if !handled || m.modal != nil {
		t.Fatal("Close must remain available after zero render")
	}
}

func firstHitID(s *hitDispatchSurface) HitID {
	for id := range s.hits {
		return id
	}
	return 0
}
