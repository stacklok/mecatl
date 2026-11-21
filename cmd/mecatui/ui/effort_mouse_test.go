package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
)

func effortMouseHit(t *testing.T, m Model, apply, closeAction bool, tier string) renderedHitRegion {
	t.Helper()
	s := m.modal.(*effortState)
	hit, ok := currentFrameHit(m, func(region renderedHitRegion) bool {
		target, ok := s.hitTargets[region.id]
		return ok && target.apply == apply && target.close == closeAction && (tier == "" || target.tier == tier)
	})
	if !ok {
		t.Fatalf("missing effort hit in %d current-frame regions: apply=%v close=%v tier=%s", len(m.hits.frame), apply, closeAction, tier)
	}
	return hit
}

func TestEffortMouseApplyMatchesKeyboardAndKeepsOffscreenSelection(t *testing.T) {
	for _, tier := range []string{"auto", "high"} {
		for _, remap := range []bool{false, true} {
			for _, pointer := range []bool{false, true} {
				m := newModelsModel(t, sampleModels(), &fakeStore{}, modelsCaps(), client.ModelSelection{})
				m.deps.NoAltScreen = false
				m.resolvedSessionModel.ReasoningEffort = effortValue(tier)
				choose := tea.KeyPressMsg{Code: tea.KeyEnter}
				if remap {
					m.keys = applyKeyOverrides(m.keys, map[string][]string{"Choose": {"a"}, "Close": {"c"}})
					choose = navigationTestKey("a")
				}
				updated, _ := m.runEffort()
				m = offerCardBody(updated.(Model), 100, 5)
				s := m.modal.(*effortState)
				view := stripANSIstr(m.View().Content)
				if remap && (!strings.Contains(view, "[a Apply]") || !strings.Contains(view, "[c Close]")) {
					t.Fatal("footer lost configured shortcuts")
				}
				before := m.vp.YOffset()
				for range 8 {
					m = applyAll(m, tea.MouseWheelMsg{Button: tea.MouseWheelDown})
				}
				_ = m.View()
				if s.list.CursorID() != tier || m.vp.YOffset() != before {
					t.Fatal("wheel changed selection or conversation")
				}
				for _, row := range s.list.View().Rows {
					if row.ID == tier {
						t.Fatal("selected tier was not scrolled offscreen")
					}
				}
				old := effortMouseHit(t, m, true, false, "").id
				var cmd tea.Cmd
				if pointer {
					m, cmd = clickFrameHit(m, effortMouseHit(t, m, true, false, ""), remap)
				} else {
					updated, cmd = m.Update(choose)
					m = updated.(Model)
				}
				if m.modal != nil || m.phase != phaseConnecting || cmd == nil || m.createModelSelection.ReasoningEffort != effortValue(tier) {
					t.Fatal("Apply and keyboard must use selected offscreen tier, including auto mapping")
				}
				token := m.modelSwitchRequestToken
				updated, duplicate := m.Update(surfaceHitMsg{ID: old})
				m = updated.(Model)
				if duplicate != nil || m.modelSwitchRequestToken != token {
					t.Fatal("duplicate Apply submitted again")
				}
				// Exercise the same fork command selected by both input paths.
				batch := runCmd(cmd).(tea.BatchMsg)
				_ = runCmd(batch[0])
				conv := m.deps.Session.(*fakeConv)
				if conv.forkCount != 1 || conv.forkedEffort != effortValue(tier) {
					t.Fatal("wrong fork count or effort")
				}
			}
		}
	}
}

func TestEffortMouseStaleFramesAndGuards(t *testing.T) {
	for _, state := range []string{"row", "keyboard", "cursor-only", "resize", "compact", "prepare", "reopen", "replace", "pending", "running", "caps"} {
		t.Run(state, func(t *testing.T) {
			m, s := openedEffort(t, "medium", false)
			m.deps.NoAltScreen = false
			_ = m.View()
			old := effortMouseHit(t, m, true, false, "").id
			closeID := effortMouseHit(t, m, false, true, "").id
			switch state {
			case "row":
				m, _ = clickFrameHit(m, effortMouseHit(t, m, false, false, "high"), false)
			case "keyboard":
				m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyDown})
			case "cursor-only":
				s.list.SetCursor(3)
			case "resize":
				m = applyAll(m, tea.WindowSizeMsg{Width: 80, Height: 30})
			case "compact":
				m = offerCardBody(m, 100, 2)
				_ = m.View()
			case "prepare":
				(&m).prepareSurfaceInput()
			case "reopen":
				m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyEsc})
				updated, _ := m.runEffort()
				m = updated.(Model)
			case "replace":
				updated, _ := m.runModels()
				m = updated.(Model)
			case "pending":
				m.phase = phaseConnecting
			case "running":
				m.phase = phaseRunning
			case "caps":
				m.caps.ModelSelection = false
			}
			modal, token := m.modal, m.modelSwitchRequestToken
			updated, cmd := m.Update(surfaceHitMsg{ID: old})
			m = updated.(Model)
			if cmd != nil || m.modal != modal || m.modelSwitchRequestToken != token || m.deps.Session.(*fakeConv).forkCount != 0 {
				t.Fatal("stale/unavailable Apply acted")
			}
			if state == "replace" || state == "reopen" {
				m = applyAll(m, surfaceHitMsg{ID: closeID})
				if m.modal != modal {
					t.Fatal("old Close closed replacement")
				}
			}
		})
	}
}

func TestEffortMouseFooterGeometryAndIsolation(t *testing.T) {
	for _, width := range []int{18, 100} {
		for _, end := range []bool{false, true} {
			m, s := openedEffort(t, "medium", false)
			m.deps.NoAltScreen = false
			m = offerCardBody(m, width, 30)
			view := m.View().Content
			if s.compact {
				t.Fatal("useful narrow geometry became compact")
			}
			apply := effortMouseHit(t, m, true, false, "")
			closeHit := effortMouseHit(t, m, false, true, "")
			if width == 18 && apply.rect.y0 == closeHit.rect.y0 {
				t.Fatal("controls did not wrap")
			}
			for _, item := range []struct {
				hit   renderedHitRegion
				label string
			}{{apply, "[enter Apply]"}, {closeHit, "[esc Close]"}} {
				assertFrameHitLabel(t, view, *m.metrics, item.hit, item.label)
				x, y := m.metrics.localToGlobal(item.hit.rect.x1, item.hit.rect.y0)
				updated, cmd := m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: x, Y: y})
				m = updated.(Model)
				if cmd != nil || m.modal != s {
					t.Fatal("footer gap activated")
				}
			}
			if width == 100 {
				if !strings.Contains(view, m.deps.Theme.Style("viewport").Bold(true).Render("[enter Apply]")) || !strings.Contains(view, m.deps.Theme.Style("muted").Render("↑/↓ move · pgup/pgdn page · home/end first/last")) {
					t.Fatal("control/help styles lost")
				}
			}
			m, _ = clickFrameHit(m, closeHit, end)
			if m.modal != nil || !m.prompt.Focused() {
				t.Fatal("whole Close label must close/refocus")
			}
		}
	}
	for _, mode := range []string{"no-mouse", "disabled"} {
		m := newModelsModel(t, sampleModels(), &fakeStore{}, modelsCaps(), client.ModelSelection{})
		m.deps.NoAltScreen = false
		if mode == "disabled" {
			m.keys.Choose.SetEnabled(false)
		} else {
			m.deps.NoMouse = true
		}
		updated, _ := m.runEffort()
		m = updated.(Model)
		s := m.modal.(*effortState)
		view := m.View()
		if mode == "disabled" {
			if strings.Contains(stripANSIstr(view.Content), "Apply") {
				t.Fatal("disabled Choose advertised")
			}
			for _, target := range s.hitTargets {
				if target.apply {
					t.Fatal("disabled Apply hit")
				}
			}
		} else {
			hit := effortMouseHit(t, m, true, false, "")
			m, _ = clickFrameHit(m, hit, false)
			row := effortMouseHit(t, m, false, false, "high")
			m, _ = clickFrameHit(m, row, false)
			if view.MouseMode != tea.MouseModeNone || s.list.CursorID() != "auto" || m.modal != s {
				t.Fatal("no-mouse input acted")
			}
		}
	}
}

func TestEffortMouseDisabledClose(t *testing.T) {
	m := newModelsModel(t, sampleModels(), &fakeStore{}, modelsCaps(), client.ModelSelection{})
	m.deps.NoAltScreen = false
	m.keys.Close.SetEnabled(false)
	updated, _ := m.runEffort()
	m = updated.(Model)
	s := m.modal.(*effortState)
	view := m.View().Content
	if strings.Contains(stripANSIstr(view), "Close]") {
		t.Fatal("disabled Close advertised")
	}
	for _, target := range s.hitTargets {
		if target.close {
			t.Fatal("disabled Close published a hit")
		}
	}
	apply := effortMouseHit(t, m, true, false, "")
	// The absent Close would follow Apply on this wide footer.
	x, y := m.metrics.localToGlobal(apply.rect.x1+1, apply.rect.y0)
	updated, cmd := m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: x, Y: y})
	m = updated.(Model)
	if cmd != nil || m.modal != s {
		t.Fatal("absent Close responded to a click")
	}
	updated, cmd = m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	m = updated.(Model)
	if cmd != nil || m.modal != s {
		t.Fatal("disabled Close responded to its shortcut")
	}
	_ = m.View()
	m, cmd = clickFrameHit(m, effortMouseHit(t, m, true, false, ""), false)
	if m.modal != nil || m.phase != phaseConnecting || cmd == nil {
		t.Fatal("disabling Close also disabled Apply")
	}
}

func TestEffortMouseCompactRestorationAndOptionalHelp(t *testing.T) {
	m, s := openedEffort(t, "max", false)
	m.deps.NoAltScreen = false
	m = offerCardBody(m, 100, 3)
	view := stripANSIstr(m.View().Content)
	if s.compact || !strings.Contains(view, "max") || strings.Contains(view, "first/last") {
		t.Fatal("optional help displaced useful body")
	}
	m = offerCardBody(m, 100, 2)
	_ = m.View()
	if !s.compact || len(m.hits.frame) != 0 {
		t.Fatal("genuine compact must have no hits")
	}
	m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyHome}, tea.MouseWheelMsg{Button: tea.MouseWheelUp})
	if s.list.CursorID() != "max" {
		t.Fatal("compact input moved cursor")
	}
	m = offerCardBody(m, 100, 20)
	_ = m.View()
	if s.compact || s.list.CursorID() != "max" {
		t.Fatal("restoration lost selected tier")
	}
	_ = effortMouseHit(t, m, false, false, "max")
}

func TestEffortMouseInertChromeAndClippedRows(t *testing.T) {
	for _, width := range []int{18, 100} {
		m, s := openedEffort(t, "medium", false)
		m.deps.NoAltScreen = false
		m = offerCardBody(m, width, 20)
		_ = m.View()
		row := effortMouseHit(t, m, false, false, "low")
		// The row's right padding and the card's border/padding are not row targets.
		for _, point := range []cellPoint{
			{x: row.rect.x1, y: row.rect.y0}, {x: -1, y: row.rect.y0},
			{x: 0, y: 0}, {x: 0, y: -1},
		} {
			x, y := m.metrics.localToGlobal(point.x, point.y)
			updated, cmd := m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: x, Y: y})
			m = updated.(Model)
			if cmd != nil || m.modal != s || s.list.CursorID() != "medium" {
				t.Fatal("inert card chrome selected or activated")
			}
		}
		m, _ = clickFrameHit(m, effortMouseHit(t, m, false, false, "auto"), true)
		_ = m.View()
		if s.list.CursorID() != "auto" {
			t.Fatal("clipped row did not select by tier")
		}
		count := 0
		for _, target := range s.hitTargets {
			if !target.apply && !target.close && target.tier == "auto" {
				count++
			}
		}
		if count != 1 {
			t.Fatal("clipped auto row fabricated continuations")
		}
		m = offerCardBody(m, 100, 5)
		_ = m.View()
		// Capacity-one overflow belongs to the title, never the tier beneath it.
		x, y := m.metrics.localToGlobal(0, 0)
		updated, cmd := m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: x, Y: y})
		m = updated.(Model)
		if cmd != nil || s.list.CursorID() != "auto" || m.modal != s {
			t.Fatal("overflow indicator acted")
		}
	}
}

func TestEffortKeyboardSharesLiveRootGuards(t *testing.T) {
	for _, state := range []string{"pending", "running", "caps"} {
		m, s := openedEffort(t, "medium", false)
		_ = m.View()
		switch state {
		case "pending":
			m.phase = phaseConnecting
		case "running":
			m.phase = phaseRunning
		case "caps":
			m.caps.ModelSelection = false
		}
		updated, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		m = updated.(Model)
		if cmd != nil || m.modal != s || m.modelSwitchRequestToken != 0 {
			t.Fatal("keyboard bypassed root guard")
		}
	}
}

func TestEffortPickerWrappedGolden(t *testing.T) {
	m, _ := openedEffort(t, "medium", false)
	m = offerCardBody(m, 18, 18)
	m = goldenStatusFrame(t, m)
	_ = m.View()
	apply, closeHit := effortMouseHit(t, m, true, false, ""), effortMouseHit(t, m, false, true, "")
	if apply.rect.y0 == closeHit.rect.y0 || m.modal.(*effortState).compact {
		t.Fatal("golden must retain body and genuinely wrap controls")
	}
	compareGolden(t, "effort_picker_wrapped.golden", stripANSI([]byte(m.View().Content)))
}

func TestEffortMouseRowSelectsWithoutApplying(t *testing.T) {
	m, s := openedEffort(t, "medium", false)
	m.deps.NoAltScreen = false
	view := stripANSIstr(m.View().Content)
	lines := strings.Split(view, "\n")
	x, y := -1, -1
	for i, line := range lines {
		if j := strings.Index(line, "high"); j >= 0 && !strings.Contains(line, "xhigh") {
			x, y = j, i
			break
		}
	}
	if x < 0 {
		t.Fatal("missing high tier")
	}
	for range 3 {
		updated, cmd := m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: x, Y: y})
		m = updated.(Model)
		if m.modal != s || s.list.CursorID() != "high" || cmd != nil {
			t.Fatal("row click must select high without applying")
		}
		_ = m.View()
	}
}
