package ui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
)

func toolcallsActionHit(t *testing.T, m Model, action toolcallsHitAction) renderedHitRegion {
	t.Helper()
	s := toolcallsForTest(t, m)
	for _, region := range m.hits.frame {
		if target, ok := s.hitTargets[region.id]; ok && target.action == action {
			return region
		}
	}
	t.Fatalf("no hit for action %d", action)
	return renderedHitRegion{}
}

func TestMecatuiToolcalls_FooterOpenBackAndClose(t *testing.T) {
	m := newToolcallsInspectorModel(t)
	m.deps.NoAltScreen = false
	m = addToolcallsForTest(t, m, 125)
	m = openToolcallsForTest(t, m)
	_ = m.View()
	for range 8 {
		updated, _ := m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelUp})
		m = updated.(Model)
		_ = m.View()
	}
	s := toolcallsForTest(t, m)
	selected := s.entries[s.selected].blockID
	offset := s.list.Offset()
	for _, id := range s.hitItems {
		if id == selected {
			t.Fatal("fixture did not scroll selection offscreen")
		}
	}
	open := toolcallsActionHit(t, m, toolcallsHitOpen)
	x, y := m.metrics.localToGlobal(open.rect.x0, open.rect.y0)
	updated, _ := m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: x, Y: y})
	m = updated.(Model)
	s = toolcallsForTest(t, m)
	if !s.detail || s.entries[s.selected].blockID != selected || s.detailEntry == nil {
		t.Fatalf("Open did not activate offscreen selection: detail=%v selected=%d want=%d", s.detail, s.entries[s.selected].blockID, selected)
	}
	openedCall := s.detailEntry.callID
	_ = m.View()
	back := toolcallsActionHit(t, m, toolcallsHitBack)
	x, y = m.metrics.localToGlobal(back.rect.x0, back.rect.y0)
	updated, _ = m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: x, Y: y})
	m = updated.(Model)
	s = toolcallsForTest(t, m)
	if s.detail || s.entries[s.selected].blockID != selected {
		t.Fatalf("Back did not restore list selection: detail=%v selected=%d want=%d", s.detail, s.entries[s.selected].blockID, selected)
	}
	_ = m.View()
	if s.list.Offset() != offset {
		t.Fatalf("Back moved list window: got %d, want %d", s.list.Offset(), offset)
	}
	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(Model)
	if got := toolcallsForTest(t, m).detailEntry; got == nil || got.callID != openedCall {
		t.Fatalf("Enter and Open chose different offscreen calls: got %#v, want %q", got, openedCall)
	}
	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	m = updated.(Model)
	_ = m.View()
	closeHit := toolcallsActionHit(t, m, toolcallsHitClose)
	x, y = m.metrics.localToGlobal(closeHit.rect.x0, closeHit.rect.y0)
	updated, _ = m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: x, Y: y})
	m = updated.(Model)
	if m.modal != nil {
		t.Fatal("Close did not close the inspector")
	}
}

func TestMecatuiToolcalls_FooterEmptyAndStaleOpenAreInert(t *testing.T) {
	empty := newToolcallsInspectorModel(t)
	empty.deps.NoAltScreen = false
	empty = openToolcallsForTest(t, empty)
	view := empty.View()
	t.Logf("empty list:\n%s", stripANSIstr(view.Content))
	if !strings.Contains(stripANSIstr(view.Content), "[enter Open details] (unavailable)") {
		t.Fatalf("empty footer lacks disabled Open: %q", stripANSIstr(view.Content))
	}
	for _, region := range empty.hits.frame {
		if toolcallsForTest(t, empty).hitTargets[region.id].action == toolcallsHitOpen {
			t.Fatal("empty footer published an Open hit")
		}
	}
	closeHit := toolcallsActionHit(t, empty, toolcallsHitClose)
	x, y := empty.metrics.localToGlobal(closeHit.rect.x0, closeHit.rect.y0)
	updated, _ := empty.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: x, Y: y})
	if updated.(Model).modal != nil {
		t.Fatal("empty list Close did not close inspector")
	}
	unavailable := newToolcallsInspectorModel(t)
	unavailable.deps.NoAltScreen = false
	unavailable = addToolcallsForTest(t, unavailable, 1)
	unavailable = openToolcallsForTest(t, unavailable)
	s := toolcallsForTest(t, unavailable)
	s.detail, s.detailEntry = true, nil
	view = unavailable.View()
	if !strings.Contains(stripANSIstr(view.Content), "call unavailable") || !strings.Contains(stripANSIstr(view.Content), "[esc Back]") {
		t.Fatalf("unavailable detail lacks Back: %q", stripANSIstr(view.Content))
	}
	back := toolcallsActionHit(t, unavailable, toolcallsHitBack)
	x, y = unavailable.metrics.localToGlobal(back.rect.x0, back.rect.y0)
	updated, _ = unavailable.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: x, Y: y})
	unavailable = updated.(Model)
	if toolcallsForTest(t, unavailable).detail {
		t.Fatal("Back did not leave unavailable detail")
	}

	m := newToolcallsInspectorModel(t)
	m.deps.NoAltScreen = false
	m = addToolcallsForTest(t, m, 1)
	m = openToolcallsForTest(t, m)
	_ = m.View()
	stale := toolcallsActionHit(t, m, toolcallsHitOpen).id
	m = addToolcallsForTest(t, m, 1)
	updated, _ = m.Update(surfaceHitMsg{ID: stale})
	m = updated.(Model)
	if toolcallsForTest(t, m).detail {
		t.Fatal("stale Open activated a changed list")
	}
}

func TestMecatuiToolcalls_RowClickSelectsWithoutOpening(t *testing.T) {
	m := newToolcallsInspectorModel(t)
	m.deps.NoAltScreen = false
	m = addToolcallsForTest(t, m, 2)
	m = openToolcallsForTest(t, m)
	_ = m.View()

	s := toolcallsForTest(t, m)
	var target renderedHitRegion
	for _, region := range m.hits.frame {
		if s.hitItems[region.id] != s.entries[s.selected].blockID {
			target = region
			break
		}
	}
	if target.id == 0 {
		t.Fatal("no alternate tool call row hit")
	}
	want := s.hitItems[target.id]
	x, y := m.metrics.localToGlobal(target.rect.x0, target.rect.y0)
	updated, _ := m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: x, Y: y})
	m = updated.(Model)
	s = toolcallsForTest(t, m)
	if got := s.entries[s.selected].blockID; got != want {
		t.Fatalf("selected block = %d, want %d", got, want)
	}
	if s.detail {
		t.Fatal("row click opened detail")
	}
	_ = m.View()
	s = toolcallsForTest(t, m)
	var repeated renderedHitRegion
	for _, region := range m.hits.frame {
		if s.hitItems[region.id] == s.entries[s.selected].blockID {
			repeated = region
			break
		}
	}
	if repeated.id == 0 {
		t.Fatal("selected row has no repeat hit")
	}
	x, y = m.metrics.localToGlobal(repeated.rect.x0, repeated.rect.y0)
	updated, _ = m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: x, Y: y})
	m = updated.(Model)
	if toolcallsForTest(t, m).detail {
		t.Fatal("repeated row click opened detail")
	}
	for range 2 {
		updated, _ = m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: x, Y: y})
		m = updated.(Model)
		if toolcallsForTest(t, m).detail {
			t.Fatal("rapid repeated row click opened detail")
		}
	}
}

func TestMecatuiToolcalls_FooterGeometry(t *testing.T) {
	for _, width := range []int{20, 30, 80} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			m := newToolcallsInspectorModel(t)
			m.deps.NoAltScreen = false
			m = addToolcallsForTest(t, m, 3)
			m = resize(m, width, 30)
			m = openToolcallsForTest(t, m)
			view := m.View().Content
			t.Logf("list at width %d:\n%s", width, stripANSIstr(view))
			if toolcallsForTest(t, m).compact {
				t.Fatal("normal controls unexpectedly compact")
			}
			if width == 80 {
				footer := strings.Split(stripANSIstr(view), "\n")[m.metrics.contentOrigin.y+m.vp.Height()-1]
				if footer != "[enter Open details] [esc Close]  ·  ↑/↓ select · pgup/pgdn scroll" {
					t.Fatalf("footer does not separate clickable shortcuts from navigation help: %q", footer)
				}
				label := m.deps.Theme.Style("viewport").Bold(true).Render("[enter Open details]")
				help := m.deps.Theme.Style("muted").Render("  ·  ↑/↓ select · pgup/pgdn scroll")
				if !strings.Contains(view, label) || !strings.Contains(view, help) {
					t.Fatal("enabled targets and navigation help lost their distinct semantic styles")
				}
			}
			open := toolcallsActionHit(t, m, toolcallsHitOpen)
			closeHit := toolcallsActionHit(t, m, toolcallsHitClose)
			if width == 20 && open.rect.y0 == closeHit.rect.y0 {
				t.Fatal("narrow fixture did not wrap controls")
			}
			for action, label := range map[toolcallsHitAction]string{toolcallsHitOpen: "[enter Open details]", toolcallsHitClose: "[esc Close]"} {
				hit := toolcallsActionHit(t, m, action)
				x, y := m.metrics.localToGlobal(hit.rect.x0, hit.rect.y0)
				row := strings.Split(stripANSIstr(view), "\n")[y]
				if got := ansi.Cut(row, x, x+hit.rect.x1-hit.rect.x0); got != label {
					t.Fatalf("hit label = %q, want %q", got, label)
				}
				x, y = m.metrics.localToGlobal(hit.rect.x1, hit.rect.y0)
				updated, _ := m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: x, Y: y})
				m = updated.(Model)
				if m.modal == nil || toolcallsForTest(t, m).detail {
					t.Fatal("footer gap activated a control")
				}
			}
			// Both ends of a displayed label are clickable, not just its marker.
			x, y := m.metrics.localToGlobal(open.rect.x1-1, open.rect.y0)
			updated, _ := m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: x, Y: y})
			m = updated.(Model)
			if !toolcallsForTest(t, m).detail {
				t.Fatal("displayed Open label did not open detail")
			}
			view = m.View().Content
			t.Logf("detail at width %d:\n%s", width, stripANSIstr(view))
			back := toolcallsActionHit(t, m, toolcallsHitBack)
			x, y = m.metrics.localToGlobal(back.rect.x0, back.rect.y0)
			if got := ansi.Cut(strings.Split(stripANSIstr(view), "\n")[y], x, x+back.rect.x1-back.rect.x0); got != "[esc Back]" {
				t.Fatalf("Back region does not match displayed label: %q", got)
			}
		})
	}
}

func TestMecatuiToolcalls_FooterIsolation(t *testing.T) {
	for _, state := range []string{"no-mouse", "resize", "compact", "reopen", "selection", "append", "result"} {
		t.Run(state, func(t *testing.T) {
			m := newToolcallsInspectorModel(t)
			m.deps.NoAltScreen = false
			m = addToolcallsForTest(t, m, 2)
			m = openToolcallsForTest(t, m)
			_ = m.View()
			open := toolcallsActionHit(t, m, toolcallsHitOpen)
			x, y := m.metrics.localToGlobal(open.rect.x0, open.rect.y0)
			switch state {
			case "no-mouse":
				m.deps.NoMouse = true
			case "resize", "compact":
				width := 80
				if state == "compact" {
					width = 8
				}
				updated, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: 30})
				m = updated.(Model)
			case "reopen":
				updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
				m = openToolcallsForTest(t, updated.(Model))
			case "selection":
				row := toolcallsActionHit(t, m, toolcallsHitSelect)
				rx, ry := m.metrics.localToGlobal(row.rect.x0, row.rect.y0)
				updated, _ := m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: rx, Y: ry})
				m = updated.(Model)
			case "append":
				m = addToolcallsForTest(t, m, 1)
			case "result":
				m = applyAll(m, client.ToolResultMsg{CallID: "call-0", Content: "arrived between render and click"})
			}
			s := toolcallsForTest(t, m)
			selected := s.entries[s.selected].blockID
			updated, _ := m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: x, Y: y})
			m = updated.(Model)
			if m.modal == nil || s.detail || s.intent != nil || s.entries[s.selected].blockID != selected {
				t.Fatal("obsolete or uncaptured Open activated or retargeted selection")
			}
			// A fresh displayed frame is the positive control after recovery.
			m.deps.NoMouse = false
			if state == "compact" {
				m = resize(m, 80, 30)
			}
			_ = m.View()
			open = toolcallsActionHit(t, m, toolcallsHitOpen)
			x, y = m.metrics.localToGlobal(open.rect.x0, open.rect.y0)
			updated, _ = m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: x, Y: y})
			m = updated.(Model)
			if !toolcallsForTest(t, m).detail {
				t.Fatal("fresh Open did not recover")
			}
			updated, _ = m.Update(surfaceHitMsg{ID: open.id})
			m = updated.(Model)
			if !toolcallsForTest(t, m).detail {
				t.Fatal("replayed Open acted as Back")
			}
			_ = m.View()
			back := toolcallsActionHit(t, m, toolcallsHitBack)
			x, y = m.metrics.localToGlobal(back.rect.x0, back.rect.y0)
			updated, _ = m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: x, Y: y})
			m = updated.(Model)
			updated, _ = m.Update(surfaceHitMsg{ID: back.id})
			m = updated.(Model)
			if m.modal == nil || toolcallsForTest(t, m).detail {
				t.Fatal("replayed Back closed the inspector")
			}
		})
	}
}

func TestMecatuiToolcalls_FirstFrameAndCloseReplay(t *testing.T) {
	probe := newToolcallsInspectorModel(t)
	probe.deps.NoAltScreen = false
	probe = addToolcallsForTest(t, probe, 1)
	probe = openToolcallsForTest(t, probe)
	_ = probe.View()
	open := toolcallsActionHit(t, probe, toolcallsHitOpen)
	x, y := probe.metrics.localToGlobal(open.rect.x0, open.rect.y0)

	m := newToolcallsInspectorModel(t)
	m.deps.NoAltScreen = false
	m = addToolcallsForTest(t, m, 1)
	m = openToolcallsForTest(t, m)
	updated, _ := m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: x, Y: y})
	m = updated.(Model)
	if s := toolcallsForTest(t, m); s.detail || s.intent != nil || len(m.hits.frame) != 0 {
		t.Fatal("click before first View activated unpublished controls")
	}
	_ = m.View()
	closeHit := toolcallsActionHit(t, m, toolcallsHitClose)
	x, y = m.metrics.localToGlobal(closeHit.rect.x0, closeHit.rect.y0)
	updated, _ = m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: x, Y: y})
	m = updated.(Model)
	if m.modal != nil {
		t.Fatal("fresh Close did not close inspector")
	}
	m.prompt.Rewrite("untouched draft")
	updated, cmd := m.Update(surfaceHitMsg{ID: closeHit.id})
	m = updated.(Model)
	if cmd != nil || m.modal != nil || m.prompt.Value() != "untouched draft" {
		t.Fatal("replayed Close affected conversation")
	}
	m = openToolcallsForTest(t, m)
	_ = m.View()
	updated, _ = m.Update(surfaceHitMsg{ID: closeHit.id})
	m = updated.(Model)
	if m.modal == nil {
		t.Fatal("stale Close closed replacement inspector")
	}
	fresh := toolcallsActionHit(t, m, toolcallsHitClose)
	x, y = m.metrics.localToGlobal(fresh.rect.x0, fresh.rect.y0)
	updated, _ = m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: x, Y: y})
	if updated.(Model).modal != nil {
		t.Fatal("replacement Close was disabled by stale input")
	}
}

func TestMecatuiToolcalls_RemappedFooterKeys(t *testing.T) {
	m := newToolcallsInspectorModel(t)
	m.keys = applyKeyOverrides(m.keys, map[string][]string{"Choose": {"ctrl+f17"}, "Close": {"ctrl+f16"}})
	m = addToolcallsForTest(t, m, 1)
	m = openToolcallsForTest(t, m)
	view := stripANSIstr(m.View().Content)
	if !strings.Contains(view, "[ctrl+f17 Open details]") || !strings.Contains(view, "[ctrl+f16 Close]") {
		t.Fatalf("footer lost remapped shortcuts: %q", view)
	}
	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(Model)
	if toolcallsForTest(t, m).detail {
		t.Fatal("default Enter bypassed remapped Choose")
	}
	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyF17, Mod: tea.ModCtrl})
	m = updated.(Model)
	if !toolcallsForTest(t, m).detail {
		t.Fatal("remapped Choose did not open detail")
	}
	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyF16, Mod: tea.ModCtrl})
	m = updated.(Model)
	if m.modal == nil || toolcallsForTest(t, m).detail {
		t.Fatal("remapped Close did not go Back")
	}
	for _, target := range []struct {
		action toolcallsHitAction
		label  string
	}{
		{toolcallsHitOpen, "[ctrl+f17 Open details]"},
		{toolcallsHitBack, "[ctrl+f16 Back]"},
	} {
		view = stripANSIstr(m.View().Content)
		hit := toolcallsActionHit(t, m, target.action)
		x, y := m.metrics.localToGlobal(hit.rect.x0, hit.rect.y0)
		if got := ansi.Cut(strings.Split(view, "\n")[y], x, x+hit.rect.x1-hit.rect.x0); got != target.label {
			t.Fatalf("remapped control label = %q, want %q", got, target.label)
		}
		x, y = m.metrics.localToGlobal(hit.rect.x1-1, hit.rect.y0)
		updated, _ = m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: x, Y: y})
		m = updated.(Model)
		if toolcallsForTest(t, m).detail != (target.action == toolcallsHitOpen) {
			t.Fatalf("last character of remapped label %q did not activate", target.label)
		}
	}
	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyF16, Mod: tea.ModCtrl})
	if updated.(Model).modal != nil {
		t.Fatal("remapped Close did not close list")
	}
}
