package ui

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"charm.land/bubbles/v2/viewport"
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
			m.conv.appendAssistant(strings.Repeat("underlying conversation\n\n", 100))
			m.refreshView()
			m.vp.SetYOffset(9)
			m.conversationView.observe(m.vp)
			conversationOffset := m.vp.YOffset()
			if conversationOffset == 0 {
				t.Fatal("conversation must start scrolled")
			}
			openCtx := s.deps.ctx
			openHits := s.deps.hits
			parentCtx, cancel := context.WithCancel(context.Background())
			defer cancel()
			m.deps.Ctx = parentCtx
			m.hits = &hitRegions{}
			firstWheel.vp.SetContent(strings.Repeat("underlying conversation\n\n", 100))
			firstWheel.vp.SetYOffset(9)
			wheelModel, _ := firstWheel.onMouseWheel(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
			firstWheel = wheelModel.(Model)
			wheelW, wheelH := firstWheel.width, firstWheel.vp.Height()
			if !fill {
				style := firstWheel.deps.Theme.Style("askCard")
				wheelW -= style.GetHorizontalFrameSize()
				wheelH -= style.GetVerticalFrameSize()
			}
			if len(wheelSpy.renders) != 1 || wheelSpy.renders[0] != ([2]int{wheelW, wheelH}) || wheelSpy.wheels != 1 || len(firstWheel.hits.frame) != 0 || *firstWheel.metrics != (renderedSurfaceMetrics{}) || firstWheel.vp.YOffset() != 9 {
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
			if s.deps.theme.Name != m.deps.Theme.Name || s.deps.marks != m.helpKeyMarkings() || !reflect.DeepEqual(s.deps.keys, m.keys) || !s.deps.caps.UserModel || m.caps.UserModel || s.deps.ctx != openCtx || s.deps.ctx == m.deps.Ctx || s.deps.hits != openHits || s.deps.hits == m.hits || len(m.hits.frame) != 0 || *m.metrics != (renderedSurfaceMetrics{}) || m.vp.YOffset() != conversationOffset {
				t.Fatal("presentation or undisplayed frame state after key")
			}
			checkConversation := func(where string) {
				t.Helper()
				if got := m.vp.YOffset(); got != conversationOffset {
					t.Fatalf("%s changed underlying conversation offset: %d → %d", where, conversationOffset, got)
				}
			}
			checkConversation("first key")
			prepared := firstHitID(s)
			mm, _ = m.onMouseWheel(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
			m = mm.(Model)
			if len(s.renders) != 2 || s.wheels != 1 || len(m.hits.frame) != 0 || *m.metrics != (renderedSurfaceMetrics{}) {
				t.Fatal("wheel must prepare once and discard unpublished frame")
			}
			wheelID := firstHitID(s)
			checkConversation("first wheel")
			for _, id := range []HitID{prepared, wheelID} {
				mm, _, _ = m.dispatchSurfaceMsg(surfaceHitMsg{ID: id})
				m = mm.(Model)
			}
			if s.hitMsgs != 0 {
				t.Fatal("prepared IDs reached modal before View")
			}

			checkConversation("stale prepared ID")
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
			checkConversation("published View and stale ID")
			x, y := m.metrics.localToGlobal(3, 1)
			count := len(s.renders)
			mm, _, _ = m.dispatchSurfaceHit(x, y)
			m = mm.(Model)
			if len(s.renders) != count || len(s.received) != 1 || s.received[0].ID != id {
				t.Fatal("displayed click must dispatch without preparation")
			}
			checkConversation("point dispatch")
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
			checkConversation("resize and stale ID")
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

func TestModalPresentationRebuildsApprovalWithoutLosingReadingPosition(t *testing.T) {
	cases := []struct {
		name, title string
		model       func(*testing.T) Model
		viewport    func(*approvalSurface) *viewport.Model
	}{
		{
			name: "plan", title: "Plan ready for review",
			model: func(t *testing.T) Model {
				m := planAskModel(t, true)
				plan, err := json.Marshal(map[string]string{"plan": strings.Repeat("plan row\n\n", 70)})
				if err != nil {
					t.Fatal(err)
				}
				approvalSurfaceOf(t, m).ask.Args = string(plan)
				return m
			},
			viewport: func(s *approvalSurface) *viewport.Model { return &s.planVP },
		},
		{
			name: "args", title: "Ask args: Shell",
			model:    func(t *testing.T) Model { return openArgsView(t, shellAskModel(t, tallShellArgs)) },
			viewport: func(s *approvalSurface) *viewport.Model { return &s.argsVP },
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := tc.model(t)
			s := approvalSurfaceOf(t, m)
			oldTheme := m.deps.Theme
			_ = m.View()
			vp := tc.viewport(s)
			if vp.TotalLineCount() <= vp.Height()+8 {
				t.Fatalf("precondition: viewport must scroll: lines=%d height=%d content=%q", vp.TotalLineCount(), vp.Height(), stripANSIstr(vp.GetContent()))
			}
			oldStyledTitle := oldTheme.Style("askTitle").Render(tc.title)
			if !strings.Contains(m.View().Content, oldStyledTitle) {
				t.Fatal("old styled title missing from parent output")
			}
			vp.SetYOffset(5)
			before := vp.YOffset()
			if before != 5 || strings.Contains(stripANSIstr(vp.View()), tc.title) {
				t.Fatal("precondition: title must be offscreen")
			}
			oldVisibleLine := strings.Split(vp.View(), "\n")[0]
			if !strings.Contains(m.View().Content, oldVisibleLine) {
				t.Fatal("old off-top viewport line missing from parent output")
			}
			oldHint := s.deps.marks.scroll
			m.deps.Theme = theme.Solar()
			m.deps.scrollKeysMarking = func() string { return "new-scroll-keys" }
			// Preparation uses the same parent render path as View, without publishing hits.
			mm, _, handled := m.dispatchSurfaceKey(tea.KeyPressMsg{Code: 'z', Text: "z"})
			m = mm.(Model)
			if !handled || vp.YOffset() != before || !s.planVPReady && tc.name == "plan" || !s.argsVPReady && tc.name == "args" {
				t.Fatalf("pre-input refresh lost %s reading position or readiness: %d → %d", tc.name, before, vp.YOffset())
			}
			out := m.View().Content
			if !strings.Contains(out, "new-scroll-keys") || strings.Contains(out, oldHint) || vp.YOffset() != before {
				t.Fatalf("refreshed %s hints or offset incorrect: %d → %d", tc.name, before, vp.YOffset())
			}
			newVisibleLine := strings.Split(vp.View(), "\n")[0]
			if oldVisibleLine == newVisibleLine || !strings.Contains(out, newVisibleLine) || strings.Contains(out, oldVisibleLine) {
				t.Fatalf("%s off-top content retained old theme", tc.name)
			}
			m.deps.scrollKeysMarking = func() string { return "second-scroll-keys" }
			mm, _, handled = m.dispatchSurfaceKey(tea.KeyPressMsg{Code: 'z', Text: "z"})
			m = mm.(Model)
			out = m.View().Content
			if !handled || vp.YOffset() != before || !strings.Contains(out, "second-scroll-keys") || strings.Contains(out, "new-scroll-keys") || !strings.Contains(out, newVisibleLine) {
				t.Fatalf("%s marks-only refresh lost viewport position, content, or updated hint", tc.name)
			}
			vp.SetYOffset(0)
			out = m.View().Content
			newStyledTitle := m.deps.Theme.Style("askTitle").Render(tc.title)
			if oldStyledTitle == newStyledTitle || !strings.Contains(out, newStyledTitle) || strings.Contains(out, oldStyledTitle) {
				t.Fatalf("%s parent output retained old styled title", tc.name)
			}
			vp.GotoBottom()
			atEnd := vp.YOffset()
			if atEnd <= before {
				t.Fatal("precondition: must scroll past new geometry")
			}
			m = resize(m, 100, 50)
			_ = m.View()
			want := min(atEnd, max(0, vp.TotalLineCount()-vp.Height()))
			if want >= atEnd {
				t.Fatal("precondition: resize must clamp the previous offset")
			}
			if got := vp.YOffset(); got != want {
				t.Fatalf("%s geometry clamp: got %d, want %d", tc.name, got, want)
			}
		})
	}
}

func TestModalPresentationRebuildsTranscriptWithoutFollowingTail(t *testing.T) {
	m, _ := hitDispatchModel(t)
	s := &sessionsState{deps: (&m).surfaceDeps(), view: sessionsTranscript}
	s.transcript.appendAssistant(strings.Repeat("transcript row\n\n", 80))
	m.modal = s
	oldTheme := m.deps.Theme
	_ = m.View()
	s.transcriptVP.SetYOffset(5)
	s.transcriptStuck = false
	before := s.transcriptVP.YOffset()
	if before != 5 || s.transcriptVP.AtBottom() {
		t.Fatalf("precondition: transcript must be off tail: lines=%d height=%d offset=%d", s.transcriptVP.TotalLineCount(), s.transcriptVP.Height(), before)
	}
	oldVisibleLine := strings.Split(s.transcriptVP.View(), "\n")[0]
	if !strings.Contains(m.View().Content, oldVisibleLine) {
		t.Fatal("old transcript line missing from parent output")
	}
	m.deps.Theme = theme.Solar()
	m.keys = applyKeyOverrides(defaultKeys(), map[string][]string{"Close": {"ctrl+f16"}})
	mm, _, handled := m.dispatchSurfaceKey(tea.KeyPressMsg{Code: 'z', Text: "z"})
	m = mm.(Model)
	if !handled || s.transcriptVP.YOffset() != before || s.transcriptStuck {
		t.Fatal("pre-input refresh followed transcript tail")
	}
	out := m.View().Content
	newVisibleLine := strings.Split(s.transcriptVP.View(), "\n")[0]
	if oldVisibleLine == newVisibleLine || !strings.Contains(out, newVisibleLine) || strings.Contains(out, oldVisibleLine) {
		t.Fatal("transcript content retained old style")
	}
	oldStyle := oldTheme.Style("muted").Render("Inspecting session · read-only")
	newStyle := m.deps.Theme.Style("muted").Render("Inspecting session · read-only")
	if oldStyle == newStyle || !strings.Contains(out, newStyle) || strings.Contains(out, oldStyle) ||
		!strings.Contains(out, "ctrl+f16: Back") || strings.Contains(out, "esc: Back") || s.transcriptVP.YOffset() != before || s.transcriptStuck {
		t.Fatal("transcript parent output retained old presentation or changed reading position")
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
