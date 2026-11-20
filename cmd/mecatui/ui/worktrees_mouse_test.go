package ui

import (
	"errors"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
)

func worktreesMouseHit(t *testing.T, m Model, action worktreeAction, selector client.WorktreeSelector) renderedHitRegion {
	t.Helper()
	s := m.modal.(*worktreesState)
	for _, region := range m.hits.frame {
		target, ok := s.hitTargets[region.id]
		if ok && target.action == action && (action != worktreeRow || target.id == s.identities[selector]) {
			return region
		}
	}
	t.Fatalf("missing worktrees hit: action=%d selector=%v", action, selector)
	return renderedHitRegion{}
}

func TestWorktreesMouseConfirmationAndHandoff(t *testing.T) {
	for _, pointer := range []bool{false, true} {
		for _, remap := range []bool{false, true} {
			rows := worktreeInventory(40)
			m, s := openedWorktrees(t, rows)
			m.deps.NoAltScreen = false
			choose := tea.KeyPressMsg{Code: tea.KeyEnter}
			if remap {
				m.keys = applyKeyOverrides(m.keys, map[string][]string{"Choose": {"a"}, "Close": {"c"}})
				s.deps = (&m).surfaceDeps()
				choose = navigationTestKey("a")
			}
			m = effortOffer(m, 80, 10)
			m.conv.addUser("keep source until confirmed success")
			before := m.vp.YOffset()
			for range 12 {
				m = applyAll(m, tea.MouseWheelMsg{Button: tea.MouseWheelDown})
			}
			view := stripANSIstr(m.View().Content)
			if strings.Contains(view, "▶") || s.list.CursorID() != s.identities[rows[0].Selector] || m.vp.YOffset() != before {
				t.Fatal("wheel must browse independently of selection and conversation")
			}
			activate := func() tea.Cmd {
				_ = m.View()
				var cmd tea.Cmd
				if pointer {
					m, cmd = effortClick(m, worktreesMouseHit(t, m, worktreeStart, client.WorktreeSelector{}), remap)
				} else {
					updated, c := m.Update(choose)
					m, cmd = updated.(Model), c
				}
				return cmd
			}
			if cmd := activate(); cmd != nil || s.confirm == nil || s.confirm.Selector != rows[0].Selector || m.phase != phaseIdle {
				t.Fatal("explicit primary must confirm offscreen selection, not hand off")
			}
			offset := s.list.Offset()
			_ = m.View()
			oldConfirm := worktreesMouseHit(t, m, worktreeStart, client.WorktreeSelector{}).id
			m = applyAll(m, tea.MouseWheelMsg{Button: tea.MouseWheelDown})
			if s.list.Offset() != offset || m.vp.YOffset() != before {
				t.Fatal("confirmation wheel leaked")
			}
			_ = m.View()
			m, _ = effortClick(m, worktreesMouseHit(t, m, worktreeBack, client.WorktreeSelector{}), true)
			if s.confirm != nil || s.list.Offset() != offset {
				t.Fatal("Back lost picker state")
			}
			m = applyAll(m, surfaceHitMsg{ID: oldConfirm})
			if s.confirm != nil {
				t.Fatal("old confirmation activated picker")
			}
			activate()
			_ = m.View()
			oldConfirm = worktreesMouseHit(t, m, worktreeStart, client.WorktreeSelector{}).id
			cmd := activate()
			if cmd == nil || m.modal != nil || m.phase != phaseConnecting || m.sessionID != "source-session" || m.conv.isEmpty() {
				t.Fatal("confirmation must begin create-first handoff")
			}
			updated, duplicate := m.Update(surfaceHitMsg{ID: oldConfirm})
			m = updated.(Model)
			if duplicate != nil {
				t.Fatal("duplicate submission")
			}
			conv := m.deps.Session.(*fakeConv)
			m = feedCmd(t, m, cmd)
			calls := conv.clearCalls()
			if len(calls) != 1 || calls[0].sourceID != "source-session" || calls[0].selector == nil || *calls[0].selector != rows[0].Selector || m.sessionID == "source-session" || !m.conv.isEmpty() {
				t.Fatal("wrong successor authority or adoption")
			}
		}
	}
}

func TestWorktreesMouseFilterAndContinuation(t *testing.T) {
	rows := worktreeInventory(3)
	rows[1].Label = strings.Repeat("long label 界 ", 8)
	m, s := openedWorktrees(t, rows)
	m.deps.NoAltScreen = false
	m = effortOffer(m, 26, 26)
	_ = m.View()
	var continuations []renderedHitRegion
	for _, region := range m.hits.frame {
		target := s.hitTargets[region.id]
		if target.action == worktreeRow && target.id == s.identities[rows[1].Selector] {
			continuations = append(continuations, region)
		}
	}
	if len(continuations) < 2 {
		t.Fatal("fixture did not wrap row")
	}
	for _, region := range continuations {
		m, _ = effortClick(m, region, true)
		if s.confirm != nil || selectedWorktree(t, s).Selector != rows[1].Selector {
			t.Fatal("continuation must select only")
		}
		_ = m.View()
	}
	// Enter confirmation, then Back must retain the filter and selection.
	m = applyAll(m, navigationTestKey("l"))
	_ = m.View()
	if !strings.Contains(stripANSIstr(m.View().Content), "[esc Clear filter]") {
		t.Fatal("filtered close mislabeled")
	}
	m, _ = effortClick(m, worktreesMouseHit(t, m, worktreeStart, client.WorktreeSelector{}), false)
	_ = m.View()
	if !strings.Contains(stripANSIstr(m.View().Content), "[esc Back]") {
		t.Fatal("confirmation back mislabeled")
	}
	m, _ = effortClick(m, worktreesMouseHit(t, m, worktreeBack, client.WorktreeSelector{}), false)
	if s.filter.Value() != "l" || s.confirm != nil {
		t.Fatal("Back cleared filter")
	}
	_ = m.View()
	m, _ = effortClick(m, worktreesMouseHit(t, m, worktreeBack, client.WorktreeSelector{}), true)
	if m.modal != s || s.filter.Value() != "" {
		t.Fatal("Clear filter must not close")
	}
	_ = m.View()
	if !strings.Contains(stripANSIstr(m.View().Content), "[esc Close]") {
		t.Fatal("unfiltered Close missing")
	}
	m, _ = effortClick(m, worktreesMouseHit(t, m, worktreeBack, client.WorktreeSelector{}), true)
	if m.modal != nil || !m.prompt.Focused() {
		t.Fatal("Close must refocus")
	}
}

func TestWorktreesMouseStaleHits(t *testing.T) {
	for _, confirmation := range []bool{false, true} {
		for _, change := range []string{"row", "key", "cursor", "filter", "filter-direct", "resize", "prepare", "compact", "replace", "reopen", "refresh", "reorder", "remove", "source", "view", "selector"} {
			for _, action := range []string{"primary", "back"} {
				t.Run(change+"/"+action+map[bool]string{false: "/picker", true: "/confirm"}[confirmation], func(t *testing.T) {
					rows := worktreeInventory(3)
					m, s := openedWorktrees(t, rows)
					m.deps.NoAltScreen = false
					if confirmation {
						m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyEnter})
					}
					_ = m.View()
					primary := worktreesMouseHit(t, m, worktreeStart, client.WorktreeSelector{}).id
					back := worktreesMouseHit(t, m, worktreeBack, client.WorktreeSelector{}).id
					switch change {
					case "row":
						if confirmation {
							m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyEsc})
						} else {
							m, _ = effortClick(m, worktreesMouseHit(t, m, worktreeRow, rows[1].Selector), false)
						}
					case "key":
						m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyDown})
					case "cursor":
						if confirmation {
							wt := rows[1]
							s.confirm = &wt
						} else {
							s.list.SetCursor(1)
						}
					case "filter":
						s.filter.SetValue("worktree")
						s.syncFilter()
					case "filter-direct":
						s.filter.SetValue("worktree")
					case "resize":
						m = applyAll(m, tea.WindowSizeMsg{Width: 80, Height: 30})
					case "prepare":
						(&m).prepareSurfaceInput()
					case "compact":
						m = effortOffer(m, 80, 1)
						_ = m.View()
					case "replace":
						m.closeModal()
						updated, _ := m.runHelp()
						m = updated.(Model)
					case "reopen":
						m.closeModal()
						updated, _ := m.openWorktrees()
						m = updated.(Model)
					case "refresh":
						_ = s.listCmd()
					case "reorder":
						s.worktrees = slices.Clone(rows)
						slices.Reverse(s.worktrees)
						s.syncFilter()
					case "remove":
						s.worktrees = rows[1:]
						s.syncFilter()
					case "source":
						s.sourceID = "other"
					case "view":
						if confirmation {
							s.confirm = nil
						} else {
							wt := rows[0]
							s.confirm = &wt
						}
					case "selector":
						if confirmation {
							wt := *s.confirm
							wt.Selector = testWorktreeSelector("new")
							s.confirm = &wt
						} else {
							s.filtered = slices.Clone(rows)
							s.filtered[0].Selector = testWorktreeSelector("new")
						}
					}
					modal, confirm, phase := m.modal, s.confirm, m.phase
					if action == "back" {
						primary = back
					}
					updated, cmd := m.Update(surfaceHitMsg{ID: primary})
					m = updated.(Model)
					if cmd != nil || m.modal != modal || s.confirm != confirm || m.phase != phase || len(m.deps.Session.(*fakeConv).clearCalls()) != 0 {
						t.Fatal("stale control acted")
					}
				})
			}
		}
	}
}

func TestWorktreesMouseFooterGeometryAndIsolation(t *testing.T) {
	for _, width := range []int{26, 80} {
		for _, confirmation := range []bool{false, true} {
			for _, end := range []bool{false, true} {
				m, s := openedWorktrees(t, worktreeInventory(3))
				m.deps.NoAltScreen = false
				m = effortOffer(m, width, 20)
				if confirmation {
					m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyEnter})
				}
				view := m.View().Content
				primary := worktreesMouseHit(t, m, worktreeStart, client.WorktreeSelector{})
				back := worktreesMouseHit(t, m, worktreeBack, client.WorktreeSelector{})
				if width == 26 && primary.rect.y0 == back.rect.y0 {
					t.Fatal("controls must wrap")
				}
				backLabel := "[esc Close]"
				if confirmation {
					backLabel = "[esc Back]"
				}
				for _, control := range []struct {
					hit   renderedHitRegion
					label string
				}{{primary, "[enter Start session]"}, {back, backLabel}} {
					x, y := m.metrics.localToGlobal(control.hit.rect.x0, control.hit.rect.y0)
					line := strings.Split(stripANSIstr(view), "\n")[y]
					if ansi.Cut(line, x, x+control.hit.rect.x1-control.hit.rect.x0) != control.label {
						t.Fatal("hit must cover entire label including shortcut and brackets")
					}
					if !strings.Contains(view, m.deps.Theme.Style("viewport").Bold(true).Render(control.label)) {
						t.Fatal("enabled button must use bold normal theme text")
					}
				}
				if !confirmation && width == 80 && !strings.Contains(view, m.deps.Theme.Style("muted").Render("↑/↓ move · home first · end last")) {
					t.Fatal("navigation help must be muted and separate")
				}
				// Each control's trailing gap, title, border and padding are inert.
				points := []cellPoint{{x: primary.rect.x1, y: primary.rect.y0}, {x: back.rect.x1, y: back.rect.y0}, {x: 0, y: 0}, {x: -1, y: primary.rect.y0}, {x: 0, y: -1}}
				if !confirmation {
					row := worktreesMouseHit(t, m, worktreeRow, worktreeInventory(3)[1].Selector)
					points = append(points, cellPoint{x: row.rect.x1, y: row.rect.y0}, cellPoint{x: 0, y: row.rect.y0 - 2})
				}
				for _, point := range points {
					x, y := m.metrics.localToGlobal(point.x, point.y)
					confirm := s.confirm
					updated, cmd := m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: x, Y: y})
					m = updated.(Model)
					if cmd != nil || m.modal != s || s.confirm != confirm || s.list.Cursor() != 0 {
						t.Fatal("chrome/gap/padding acted")
					}
				}
				m, _ = effortClick(m, primary, end)
				if confirmation {
					if m.phase != phaseConnecting || m.modal != nil {
						t.Fatal("confirmation primary edge did not submit")
					}
				} else if s.confirm == nil || m.phase != phaseIdle {
					t.Fatal("picker primary edge did not enter confirmation")
				}
			}
		}
	}
}

func TestWorktreesMouseAvailability(t *testing.T) {
	for _, state := range []string{"loading", "error", "empty", "filtered-empty", "choose-disabled", "close-disabled", "both-disabled", "no-mouse"} {
		t.Run(state, func(t *testing.T) {
			rows := worktreeInventory(3)
			m, s := openedWorktrees(t, rows)
			m.deps.NoAltScreen = false
			switch state {
			case "loading":
				_ = s.listCmd()
			case "error":
				s.err = errors.New("offline")
			case "empty":
				s.worktrees = nil
				s.syncFilter()
			case "filtered-empty":
				s.filter.SetValue("missing")
				s.syncFilter()
			case "choose-disabled":
				s.deps.keys.Choose.SetEnabled(false)
			case "close-disabled":
				s.deps.keys.Close.SetEnabled(false)
			case "both-disabled":
				s.deps.keys.Choose.SetEnabled(false)
				s.deps.keys.Close.SetEnabled(false)
			case "no-mouse":
				m.deps.NoMouse = true
			}
			view := m.View()
			primary := state == "close-disabled" || state == "no-mouse"
			if strings.Contains(stripANSIstr(view.Content), "Start session]") != primary {
				t.Fatal("incorrect primary availability")
			}
			for _, target := range s.hitTargets {
				if !primary && target.action == worktreeStart {
					t.Fatal("unavailable primary hit")
				}
				if (state == "loading" || state == "error" || state == "empty" || state == "filtered-empty") && target.action == worktreeRow {
					t.Fatal("unavailable row hit")
				}
				if (state == "close-disabled" || state == "both-disabled") && target.action == worktreeBack {
					t.Fatal("disabled Close hit")
				}
			}
			if state == "no-mouse" {
				if view.MouseMode != tea.MouseModeNone {
					t.Fatal("no-mouse mode lost")
				}
				for _, action := range []worktreeAction{worktreeRow, worktreeStart, worktreeBack} {
					m, _ = effortClick(m, worktreesMouseHit(t, m, action, rows[1].Selector), false)
				}
				if m.modal != s || s.confirm != nil || s.list.Cursor() != 0 {
					t.Fatal("no-mouse clicked through")
				}
			} else if !primary {
				m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyEnter})
				if s.confirm != nil {
					t.Fatal("unavailable Choose activated")
				}
			}
			if state == "close-disabled" || state == "both-disabled" {
				if strings.Contains(stripANSIstr(view.Content), "Close]") {
					t.Fatal("disabled Close advertised")
				}
				m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyEsc})
				if m.modal != s {
					t.Fatal("disabled Close closed")
				}
				m = effortOffer(m, 80, 1)
				compact := stripANSIstr(m.View().Content)
				if !s.compact || len(m.hits.frame) != 0 || strings.Contains(compact, "esc close") || !strings.Contains(compact, "terminal too small") {
					t.Fatal("compact advertised disabled Close")
				}
			}
		})
	}
}

func TestWorktreesMouseCompactAndOptionalHelp(t *testing.T) {
	m, s := openedWorktrees(t, worktreeInventory(40))
	m.deps.NoAltScreen = false
	s.list.SetCursor(20)
	m = effortOffer(m, 80, 5)
	view := stripANSIstr(m.View().Content)
	if s.compact || !strings.Contains(view, "worktree-20") || strings.Contains(view, "first") {
		t.Fatal("optional help displaced useful body")
	}
	for _, offer := range [][2]int{{80, 3}, {12, 20}} {
		m = effortOffer(m, offer[0], offer[1])
		_ = m.View()
		if !s.compact || len(m.hits.frame) != 0 {
			t.Fatal("compact should have no hits")
		}
		m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyHome}, tea.KeyPressMsg{Code: tea.KeyEnter}, tea.MouseWheelMsg{Button: tea.MouseWheelDown})
		if s.list.Cursor() != 20 || s.confirm != nil {
			t.Fatal("compact input moved or activated")
		}
	}
	m = effortOffer(m, 80, 20)
	_ = m.View()
	if s.compact || selectedWorktree(t, s).Selector != worktreeInventory(40)[20].Selector {
		t.Fatal("restoration lost selection")
	}
	_ = worktreesMouseHit(t, m, worktreeRow, worktreeInventory(40)[20].Selector)
}

func TestWorktreesMouseRetainsRootValidation(t *testing.T) {
	for _, state := range []string{"source", "pending", "running", "selector", "server-error"} {
		t.Run(state, func(t *testing.T) {
			m, s := openedWorktrees(t, worktreeInventory(2))
			m.deps.NoAltScreen = false
			m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyEnter})
			conv := m.deps.Session.(*fakeConv)
			switch state {
			case "source":
				m.sessionID = "changed"
			case "pending":
				m.phase = phaseConnecting
			case "running":
				m.phase = phaseRunning
			case "selector":
				s.confirm.Selector = client.WorktreeSelector{}
			case "server-error":
				conv.createErr = errors.New("stale selector")
			}
			_ = m.View()
			m, cmd := effortClick(m, worktreesMouseHit(t, m, worktreeStart, client.WorktreeSelector{}), false)
			if state == "selector" {
				if m.modal != s || s.confirm != nil || s.err == nil || cmd != nil {
					t.Fatal("missing selector escaped local validation")
				}
			} else if state == "server-error" {
				m = feedCmd(t, m, cmd)
				if m.phase != phaseIdle || m.sessionID != "source-session" || !strings.Contains(m.statusMsg, "relist") || len(conv.closed()) != 0 {
					t.Fatal("server failure bypassed create-first authority")
				}
			} else if m.modal != nil || !strings.Contains(m.statusMsg, "stale") || len(conv.clearCalls()) != 0 {
				t.Fatal("root guard bypassed")
			}
		})
	}
}

func TestWorktreesMouseGoldens(t *testing.T) {
	for _, state := range []string{"picker", "confirmation", "wrapped", "confirmation_wrapped", "compact", "empty", "loading"} {
		t.Run(state, func(t *testing.T) {
			rows := worktreeInventory(4)
			if state == "empty" {
				rows = nil
			}
			m, s := openedWorktrees(t, rows)
			m = effortOffer(m, 80, 18)
			if strings.HasPrefix(state, "confirmation") {
				m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyEnter})
			}
			if strings.Contains(state, "wrapped") {
				m = effortOffer(m, 26, 18)
			}
			if state == "compact" {
				m = effortOffer(m, 80, 3)
			}
			if state == "loading" {
				_ = s.listCmd()
			}
			m = goldenStatusFrame(t, m)
			_ = m.View()
			if state == "compact" && (!s.compact || len(m.hits.frame) != 0) {
				t.Fatal("compact golden must expose no hits")
			}
			if strings.Contains(state, "wrapped") {
				start := worktreesMouseHit(t, m, worktreeStart, client.WorktreeSelector{})
				back := worktreesMouseHit(t, m, worktreeBack, client.WorktreeSelector{})
				if s.compact || start.rect.y0 == back.rect.y0 {
					t.Fatal("golden must wrap controls retaining body")
				}
			}
			compareGolden(t, "worktrees_"+state+".golden", stripANSI([]byte(m.View().Content)))
		})
	}
}

func TestWorktreesMouseRefreshAndPartialRows(t *testing.T) {
	rows := worktreeInventory(30)
	rows[1].Label = strings.Repeat("wrapped worktree 界 ", 20)
	m, s := openedWorktrees(t, rows)
	m.deps.NoAltScreen = false
	m = effortOffer(m, 26, 14)
	_ = m.View()
	oldRow := worktreesMouseHit(t, m, worktreeRow, rows[0].Selector).id
	// A foreign owner/request cannot change discovery or invalidate current controls.
	m = applyAll(m, worktreesResultMsg{owner: &worktreesState{}, request: s.request, result: client.WorktreesMsg{}})
	if _, ok := s.hitTargets[oldRow]; !ok {
		t.Fatal("foreign result invalidated current row")
	}
	refresh := s.listCmd()
	s.lister.(*fakeWorktreeLister).wts = append([]client.Worktree{rows[29]}, rows[:29]...)
	m = applyAll(m, refresh())
	s.list.SetCursor(0)
	m = applyAll(m, surfaceHitMsg{ID: oldRow})
	if selectedWorktree(t, s).Selector != rows[29].Selector || s.confirm != nil {
		t.Fatal("stale row retargeted after refresh")
	}
	_ = m.View()
	m, _ = effortClick(m, worktreesMouseHit(t, m, worktreeRow, rows[1].Selector), false)
	_ = m.View()
	// Scroll into the oversized item's physical continuations, without selecting.
	for range 3 {
		m = applyAll(m, tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	}
	_ = m.View()
	partial := s.list.View().Rows
	if len(partial) == 0 || partial[0].ID != s.identities[rows[1].Selector] || partial[0].ItemLine == 0 {
		t.Fatal("fixture must expose a scrolled physical continuation")
	}
	row := worktreesMouseHit(t, m, worktreeRow, rows[1].Selector)
	for range 2 {
		m, _ = effortClick(m, row, false)
		if s.confirm != nil || selectedWorktree(t, s).Selector != rows[1].Selector {
			t.Fatal("partial continuation activated or selected wrong selector")
		}
		_ = m.View()
		row = worktreesMouseHit(t, m, worktreeRow, rows[1].Selector)
	}
	// Neither overflow indicator is a row target.
	view := strings.Split(stripANSIstr(m.View().Content), "\n")
	for y, line := range view {
		if !strings.Contains(line, " more") {
			continue
		}
		x := strings.Index(line, "↑")
		if x < 0 {
			x = strings.Index(line, "↓")
		}
		updated, cmd := m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: x, Y: y})
		m = updated.(Model)
		if cmd != nil || s.confirm != nil || selectedWorktree(t, s).Selector != rows[1].Selector {
			t.Fatal("overflow indicator acted")
		}
	}
}

func TestWorktreesMouseRowsOnlySelect(t *testing.T) {
	m, s := openedWorktrees(t, worktreeInventory(3))
	m.deps.NoAltScreen = false
	for range 3 {
		view := stripANSIstr(m.View().Content)
		x, y := -1, -1
		for i, line := range strings.Split(view, "\n") {
			if at := strings.Index(line, "worktree-01"); at >= 0 {
				x, y = at, i
				break
			}
		}
		if x < 0 {
			t.Fatal("missing worktree row")
		}
		updated, cmd := m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: x, Y: y})
		m = updated.(Model)
		if selectedWorktree(t, s).Selector != worktreeInventory(3)[1].Selector || s.confirm != nil || cmd != nil || m.modal != s || m.phase != phaseIdle {
			t.Fatal("row must select without confirmation or handoff")
		}
	}
}
