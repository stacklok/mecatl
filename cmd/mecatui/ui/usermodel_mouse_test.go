package ui

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/ui/internal/bounded"
)

func userModelMouseHit(t *testing.T, m Model, match func(userModelHit) bool) renderedHitRegion {
	t.Helper()
	s := m.modal.(*userModelState)
	hit, ok := currentFrameHit(m, func(region renderedHitRegion) bool {
		target, ok := s.hitTargets[region.id]
		return ok && match(target)
	})
	if !ok {
		t.Fatalf("missing saved-memory hit in %d current-frame regions", len(m.hits.frame))
	}
	return hit
}

func TestUserModelMouseRowsSelectOnlyIncludingContinuations(t *testing.T) {
	entries := []client.UserModelEntry{{Key: strings.Repeat("key ", 12) + "\x1b[2J", Description: strings.Repeat("description ", 12)}, {Key: "other", Description: "other description"}}
	memory := &fakeUserModel{model: client.UserModel{Entries: entries}}
	m, s := openedUserModel(t, newUserModelModel(t, memory, client.Capabilities{UserModel: true}))
	m.deps.NoAltScreen = false
	m = offerCardBody(m, 32, 28)
	_ = m.View()
	physicalRows := len(s.list.View().Rows) - 2 // other key and its description
	if physicalRows < 3 {
		t.Fatal("fixture lacks wrapped key/description rows")
	}
	for rowIndex := range physicalRows {
		// Start with a different selection so an inert hit cannot pass.
		s.list.SetCursor(1)
		s.list.Scroll(bounded.Top)
		_ = m.View()
		var rows []renderedHitRegion
		for _, hit := range m.hits.frame {
			if target, ok := s.hitTargets[hit.id]; ok && target.key == entries[0].Key {
				rows = append(rows, hit)
			}
		}
		if rowIndex >= len(rows) {
			t.Fatalf("missing physical row %d in %d hits", rowIndex, len(rows))
		}
		var cmd tea.Cmd
		m, cmd = clickFrameHit(m, rows[rowIndex], rowIndex%2 == 0)
		if cmd != nil || s.list.CursorID() != entries[0].Key || s.loading || memory.calls != 1 {
			t.Fatalf("row %d failed exact raw-key selection or fetched detail", rowIndex)
		}
		// Repeated selection uses a newly published frame, not an expired hit.
		_ = m.View()
		m, cmd = clickFrameHit(m, userModelMouseHit(t, m, func(hit userModelHit) bool { return hit.key == entries[0].Key }), false)
		if cmd != nil || s.loading || memory.calls != 1 {
			t.Fatal("repeat click fetched detail")
		}
	}
}

func TestUserModelMouseInspectBackAndCloseMatchKeys(t *testing.T) {
	memory := sampleUserModel()
	memory.model.Detail = &client.UserModelDetail{Current: client.UserModelRevision{Key: "name", Value: "Alice"}}
	m, s := openedUserModel(t, newUserModelModel(t, memory, client.Capabilities{UserModel: true}))
	m.deps.NoAltScreen = false
	m = offerCardBody(m, 80, 18)
	frame := m.View().Content
	inspect := userModelMouseHit(t, m, func(target userModelHit) bool { return target.inspect })
	closeHit := userModelMouseHit(t, m, func(target userModelHit) bool { return target.close })
	assertFrameHitLabel(t, frame, *m.metrics, inspect, "[enter Inspect]")
	assertFrameHitLabel(t, frame, *m.metrics, closeHit, "[esc Close]")
	m, cmd := clickFrameHit(m, inspect, true)
	if cmd == nil || !s.loading || s.requestKey != "name" || memory.calls != 1 {
		t.Fatal("Inspect did not share keyboard detail activation")
	}
	updated, duplicate := m.Update(surfaceHitMsg{ID: inspect.id})
	m = updated.(Model)
	if duplicate != nil || memory.calls != 1 {
		t.Fatal("stale Inspect submitted a duplicate request")
	}
	m = feedCmd(t, m, cmd)
	if s.view != userModelDetail {
		t.Fatal("Inspect response did not open detail")
	}
	frame = m.View().Content
	back := userModelMouseHit(t, m, func(target userModelHit) bool { return target.close })
	assertFrameHitLabel(t, frame, *m.metrics, back, "[esc Back]")
	m, cmd = clickFrameHit(m, back, false)
	if cmd != nil || s.view != userModelPanel || s.list.CursorID() != "name" {
		t.Fatal("Back did not preserve list selection")
	}
	_ = m.View()
	m, _ = clickFrameHit(m, userModelMouseHit(t, m, func(target userModelHit) bool { return target.close }), false)
	if m.modal != nil || !m.prompt.Focused() {
		t.Fatal("Close did not share modal teardown")
	}
}

func TestUserModelMouseLoadingOnlyPublishesClose(t *testing.T) {
	m, s := openedUserModel(t, newUserModelModel(t, sampleUserModel(), client.Capabilities{UserModel: true}))
	m.deps.NoAltScreen = false
	s.loading = true
	_ = m.View()
	for _, hit := range m.hits.frame {
		target := s.hitTargets[hit.id]
		if target.key != "" || target.inspect {
			t.Fatal("loading view published a row or Inspect target")
		}
	}
	m, _ = clickFrameHit(m, userModelMouseHit(t, m, func(target userModelHit) bool { return target.close }), false)
	if m.modal != nil {
		t.Fatal("Close was unavailable while loading")
	}
}

func TestUserModelMouseInspectUsesOffscreenSelectionAndRemappedBindings(t *testing.T) {
	m := newUserModelModel(t, &fakeUserModel{model: client.UserModel{Entries: savedMemoryEntries(30)}}, client.Capabilities{UserModel: true})
	m.keys = applyKeyOverrides(m.keys, map[string][]string{"Choose": {"i"}, "Close": {"x"}})
	m, s := openedUserModel(t, m)
	m.deps.NoAltScreen = false
	m = offerCardBody(m, 48, 12)
	frame := m.View().Content
	inspect := userModelMouseHit(t, m, func(target userModelHit) bool { return target.inspect })
	closeHit := userModelMouseHit(t, m, func(target userModelHit) bool { return target.close })
	assertFrameHitLabel(t, frame, *m.metrics, inspect, "[i Inspect]")
	assertFrameHitLabel(t, frame, *m.metrics, closeHit, "[x Close]")
	selected := s.list.CursorID()
	for range 20 {
		m = applyAll(m, tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	}
	_ = m.View()
	for _, row := range s.list.View().Rows {
		if row.ID == selected {
			t.Fatal("wheel did not move the selected row offscreen")
		}
	}
	m, cmd := clickFrameHit(m, userModelMouseHit(t, m, func(target userModelHit) bool { return target.inspect }), false)
	if cmd == nil || s.requestKey != selected || !s.loading {
		t.Fatal("Inspect did not use the retained offscreen selection")
	}
}

func TestUserModelMouseUnavailableActionsAndNoMouse(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(*userModelState, *Model)
	}{
		{"no detailer", func(s *userModelState, _ *Model) { s.detailer = nil }},
		{"empty", func(s *userModelState, _ *Model) { s.setModel(client.UserModel{}) }},
		{"initial error", func(s *userModelState, _ *Model) { s.setModel(client.UserModel{}); s.err = errors.New("initial") }},
		{"disabled choose", func(_ *userModelState, m *Model) {
			m.keys.Choose.SetEnabled(false)
			m.modal.(*userModelState).deps.keys.Choose.SetEnabled(false)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, s := openedUserModel(t, newUserModelModel(t, sampleUserModel(), client.Capabilities{UserModel: true}))
			m.deps.NoAltScreen = false
			tc.setup(s, &m)
			_ = m.View()
			for _, hit := range m.hits.frame {
				if s.hitTargets[hit.id].inspect {
					t.Fatal("unavailable Inspect was published")
				}
			}
			m, _ = clickFrameHit(m, userModelMouseHit(t, m, func(hit userModelHit) bool { return hit.close }), true)
			if m.modal != nil || !m.prompt.Focused() {
				t.Fatal("enabled Close did not tear down unavailable-action screen")
			}
		})
	}
	m, s := openedUserModel(t, newUserModelModel(t, sampleUserModel(), client.Capabilities{UserModel: true}))
	m.deps.NoAltScreen = false
	m.deps.NoMouse = true
	_ = m.View()
	row := userModelMouseHit(t, m, func(target userModelHit) bool { return target.key == "stack" })
	selected := s.list.CursorID()
	m, cmd := clickFrameHit(m, row, false)
	if cmd != nil || s.list.CursorID() != selected {
		t.Fatal("no-mouse pointer input changed saved-memory state")
	}
	updated, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(Model)
	if cmd == nil || !s.loading {
		t.Fatal("no-mouse mode disabled keyboard Inspect")
	}
	m = feedCmd(t, m, cmd)
	m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyEsc}, tea.KeyPressMsg{Code: tea.KeyDown})
	if s.view != userModelPanel || s.list.CursorID() != "stack" {
		t.Fatal("no-mouse mode disabled keyboard Back/navigation")
	}
	m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyEsc})
	if m.modal != nil || !m.prompt.Focused() {
		t.Fatal("no-mouse mode disabled keyboard Close")
	}
}

func TestUserModelMouseDetailErrorRetriesAndStaleFramesAreInert(t *testing.T) {
	m, s := openedUserModel(t, newUserModelModel(t, sampleUserModel(), client.Capabilities{UserModel: true}))
	m.deps.NoAltScreen = false
	m = offerCardBody(m, 80, 18)
	_ = m.View()
	oldInspect := userModelMouseHit(t, m, func(target userModelHit) bool { return target.inspect }).id
	m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyDown})
	updated, cmd := m.Update(surfaceHitMsg{ID: oldInspect})
	m = updated.(Model)
	if cmd != nil || s.loading || s.requestKey != "" {
		t.Fatal("Inspect hit survived keyboard selection")
	}
	_ = m.View()
	m, cmd = clickFrameHit(m, userModelMouseHit(t, m, func(target userModelHit) bool { return target.inspect }), false)
	if cmd == nil {
		t.Fatal("valid Inspect missing")
	}
	request := cmd().(client.UserModelDetailMsg)
	request.Err = errors.New("detail failed")
	m = applyAll(m, request)
	if s.err == nil || s.loading || s.requestKey != "" {
		t.Fatal("detail error did not become retryable")
	}
	_ = m.View()
	m, retry := clickFrameHit(m, userModelMouseHit(t, m, func(target userModelHit) bool { return target.inspect }), false)
	if retry == nil || !s.loading || m.modal != s {
		t.Fatal("detail error Inspect did not retry")
	}
}

func TestUserModelMouseCompactCloseTearsDownDetail(t *testing.T) {
	m, s := openedUserModel(t, newUserModelModel(t, sampleUserModel(), client.Capabilities{UserModel: true}))
	updated, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = feedCmd(t, updated.(Model), cmd)
	if s.view != userModelDetail {
		t.Fatal("fixture did not enter detail")
	}
	m = applyAll(m, tea.WindowSizeMsg{Width: 2, Height: 18})
	_ = m.View()
	if !s.compact {
		t.Fatal("fixture did not enter compact detail")
	}
	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	m = updated.(Model)
	if m.modal != nil {
		t.Fatal("compact detail Close went Back instead of tearing down")
	}
}

func TestUserModelMousePartialRowAndWrappedFooter(t *testing.T) {
	memory := &fakeUserModel{model: client.UserModel{Entries: []client.UserModelEntry{
		{Key: "first"}, {Key: "long-key", Description: strings.Repeat("description ", 30)},
	}}}
	m, s := openedUserModel(t, newUserModelModel(t, memory, client.Capabilities{UserModel: true}))
	m.deps.NoAltScreen = false
	m = offerCardBody(m, 18, 18)
	for range 5 {
		m = applyAll(m, tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	}
	frame := m.View().Content
	view := s.list.View()
	if len(view.Rows) == 0 || view.Rows[0].ID != "long-key" || view.Rows[0].ItemLine == 0 || s.list.CursorID() != "first" {
		t.Fatalf("fixture needs an unselected partial continuation: %+v", view)
	}
	inspect := userModelMouseHit(t, m, func(hit userModelHit) bool { return hit.inspect })
	closeHit := userModelMouseHit(t, m, func(hit userModelHit) bool { return hit.close })
	assertFrameHitLabel(t, frame, *m.metrics, inspect, "[enter Inspect]")
	assertFrameHitLabel(t, frame, *m.metrics, closeHit, "[esc Close]")
	if inspect.rect.y0 >= closeHit.rect.y0 {
		t.Fatal("fixture did not wrap footer controls")
	}
	m, cmd := clickFrameHit(m, userModelMouseHit(t, m, func(hit userModelHit) bool { return hit.key == "long-key" }), true)
	if cmd != nil || s.list.CursorID() != "long-key" || memory.calls != 1 {
		t.Fatal("partial continuation did not select its key without fetching")
	}
}

func TestUserModelMouseFooterButtonEdges(t *testing.T) {
	for _, width := range []int{18, 80} {
		for _, end := range []bool{false, true} {
			t.Run(fmt.Sprintf("width=%d/end=%v", width, end), func(t *testing.T) {
				memory := sampleUserModel()
				m, s := openedUserModel(t, newUserModelModel(t, memory, client.Capabilities{UserModel: true}))
				m.deps.NoAltScreen = false
				m = offerCardBody(m, width, 24)
				frame := m.View().Content
				inspect := userModelMouseHit(t, m, func(hit userModelHit) bool { return hit.inspect })
				closeHit := userModelMouseHit(t, m, func(hit userModelHit) bool { return hit.close })
				assertFrameHitLabel(t, frame, *m.metrics, inspect, "[enter Inspect]")
				assertFrameHitLabel(t, frame, *m.metrics, closeHit, "[esc Close]")
				if width == 18 && inspect.rect.y0 == closeHit.rect.y0 {
					t.Fatal("fixture did not wrap controls")
				}
				m, cmd := clickFrameHit(m, inspect, end)
				if cmd == nil {
					t.Fatal("Inspect edge did not fetch detail")
				}
				m = feedCmd(t, m, cmd)
				if memory.calls != 2 || s.view != userModelDetail || s.detail.Current.Key != "name" {
					t.Fatal("Inspect edge did not fetch exactly the selected detail once")
				}
				frame = m.View().Content
				back := userModelMouseHit(t, m, func(hit userModelHit) bool { return hit.close })
				assertFrameHitLabel(t, frame, *m.metrics, back, "[esc Back]")
				m, _ = clickFrameHit(m, back, end)
				if s.view != userModelPanel {
					t.Fatal("Back edge did not restore list")
				}
				_ = m.View()
				m, _ = clickFrameHit(m, userModelMouseHit(t, m, func(hit userModelHit) bool { return hit.close }), end)
				if m.modal != nil || !m.prompt.Focused() {
					t.Fatal("Close edge did not tear down/refocus")
				}
			})
		}
	}
}

func TestUserModelMouseStaleTargets(t *testing.T) {
	for _, transition := range []string{"selection", "resize", "preparation", "pending", "detail", "replacement", "removed"} {
		t.Run(transition, func(t *testing.T) {
			m, s := openedUserModel(t, newUserModelModel(t, sampleUserModel(), client.Capabilities{UserModel: true}))
			m.deps.NoAltScreen = false
			_ = m.View()
			old := []renderedHitRegion{
				userModelMouseHit(t, m, func(hit userModelHit) bool { return hit.key == "stack" }),
				userModelMouseHit(t, m, func(hit userModelHit) bool { return hit.inspect }),
				userModelMouseHit(t, m, func(hit userModelHit) bool { return hit.close }),
			}
			// Capture coordinates from the actual displayed frame before invalidation.
			var clicks []tea.MouseClickMsg
			for _, hit := range old {
				x, y := m.metrics.localToGlobal(hit.rect.x0, hit.rect.y0)
				clicks = append(clicks, tea.MouseClickMsg{Button: tea.MouseLeft, X: x, Y: y})
			}
			switch transition {
			case "selection":
				m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyDown})
			case "resize":
				m = applyAll(m, tea.WindowSizeMsg{Width: 80, Height: 28})
			case "preparation":
				(&m).prepareSurfaceInput()
			case "pending", "detail":
				updated, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
				m = updated.(Model)
				if transition == "detail" {
					m = feedCmd(t, m, cmd)
				}
			case "replacement":
				(&m).closeModal()
				m, s = openedUserModel(t, m)
				// Publish replacement IDs, but do not replay old coordinates onto
				// a newly displayed action in the same position.
				clicks = nil
			case "removed":
				m = applyAll(m, client.UserModelMsg{Generation: s.generation, UserModel: client.UserModel{Entries: []client.UserModelEntry{{Key: "different"}}}})
			}
			selected, loading, view, generation := s.list.CursorID(), s.loading, s.view, s.generation
			for i, hit := range old {
				updated, cmd := m.Update(surfaceHitMsg{ID: hit.id})
				m = updated.(Model)
				if cmd != nil {
					t.Fatal("stale semantic target submitted a request")
				}
				if len(clicks) > 0 {
					updated, cmd = m.Update(clicks[i])
					m = updated.(Model)
					if cmd != nil {
						t.Fatal("undisplayed stale coordinates submitted a request")
					}
				}
			}
			if m.modal != s || s.list.CursorID() != selected || s.loading != loading || s.view != view || s.generation != generation {
				t.Fatal("stale target changed current surface state")
			}
			_ = m.View()
			if transition != "pending" && transition != "detail" {
				m, cmd := clickFrameHit(m, userModelMouseHit(t, m, func(hit userModelHit) bool { return hit.inspect }), false)
				if cmd == nil || m.modal != s || !s.loading {
					t.Fatal("fresh positive-control Inspect did not work")
				}
			}
		})
	}
}

func TestUserModelMouseDisabledCloseAndHiddenInspect(t *testing.T) {
	m := newUserModelModel(t, sampleUserModel(), client.Capabilities{UserModel: true})
	m.keys.Close.SetEnabled(false)
	m, s := openedUserModel(t, m)
	m.deps.NoAltScreen = false
	_ = m.View()
	for _, target := range s.hitTargets {
		if target.close {
			t.Fatal("disabled Close was published")
		}
	}
	m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyEsc})
	if m.modal != s {
		t.Fatal("disabled Close key closed modal")
	}
	_ = m.View()
	_ = userModelMouseHit(t, m, func(hit userModelHit) bool { return hit.inspect })
	m = offerCardBody(m, 12, 30) // Inspect is wider than the offer; Close is disabled.
	frame := stripANSIstr(m.View().Content)
	if !s.compact || len(s.hitTargets) != 0 || strings.Contains(frame, "esc") {
		t.Fatal("unusable footer exposed hits, hidden activation, or disabled Close hint")
	}
	updated, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(Model)
	if cmd != nil || s.loading {
		t.Fatal("hidden Inspect remained keyboard-activatable")
	}
	m = offerCardBody(m, 60, 18)
	_ = m.View()
	m, cmd = clickFrameHit(m, userModelMouseHit(t, m, func(hit userModelHit) bool { return hit.inspect }), false)
	if cmd == nil || !s.loading {
		t.Fatal("usable Inspect did not restore")
	}
}

func TestUserModelMouseCompactRestoresIndependentBrowsers(t *testing.T) {
	memory := &fakeUserModel{model: client.UserModel{Entries: savedMemoryEntries(40)}}
	m, s := openedUserModel(t, newUserModelModel(t, memory, client.Capabilities{UserModel: true}))
	m.deps.NoAltScreen = false
	m = offerCardBody(m, 60, 16)
	_ = m.View()
	for range 15 {
		m = applyAll(m, tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	}
	_ = m.View()
	selected, listOffset := s.list.CursorID(), s.list.Offset()
	m, cmd := clickFrameHit(m, userModelMouseHit(t, m, func(hit userModelHit) bool { return hit.inspect }), false)
	detail := cmd().(client.UserModelDetailMsg)
	detail.UserModel.Detail = &client.UserModelDetail{
		Current:          client.UserModelRevision{Key: selected, Value: strings.Repeat("exact current value ", 80)},
		HistoryAvailable: true, History: []client.UserModelRevision{{Version: "old-revision", Status: "active"}},
	}
	m = applyAll(m, detail)
	_ = m.View()
	for range 4 {
		m = applyAll(m, tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	}
	_ = m.View()
	detailOffset := s.viewport.Offset()
	if detailOffset == 0 || s.list.Offset() != listOffset {
		t.Fatal("detail wheel failed to scroll independently")
	}
	m = offerCardBody(m, 60, 1)
	_ = m.View()
	if !s.compact || len(m.hits.frame) != 0 {
		t.Fatal("compact view exposed pointer controls")
	}
	m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyEnter}, tea.KeyPressMsg{Code: tea.KeyDown}, tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	if s.list.CursorID() != selected || s.list.Offset() != listOffset || s.viewport.Offset() != detailOffset || s.view != userModelDetail {
		t.Fatal("compact input changed hidden browsing state")
	}
	m = offerCardBody(m, 60, 16)
	_ = m.View()
	if s.compact || s.viewport.Offset() != detailOffset {
		t.Fatal("enlarging did not restore reader anchor")
	}
	m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyEnd})
	if frame := stripANSIstr(m.View().Content); !strings.Contains(frame, "old-revision") {
		t.Fatal("exact selected history was not reachable")
	}
	m, _ = clickFrameHit(m, userModelMouseHit(t, m, func(hit userModelHit) bool { return hit.close }), true)
	_ = m.View()
	if s.view != userModelPanel || s.list.CursorID() != selected || s.list.Offset() != listOffset || s.detail != nil || s.requestKey != "" || s.err != nil {
		t.Fatal("pointer Back lost parent window or retained detail/request state")
	}
}

func TestUserModelMouseNonTargetsAreInert(t *testing.T) {
	m, s := openedUserModel(t, newUserModelModel(t, &fakeUserModel{model: client.UserModel{Entries: savedMemoryEntries(40)}}, client.Capabilities{UserModel: true}))
	m.deps.NoAltScreen = false
	m = offerCardBody(m, 70, 16)
	m = applyAll(m, tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	_ = m.View()
	selected, offset := s.list.CursorID(), s.list.Offset()
	misses := 0
	for y := m.metrics.outerBounds.y0; y < m.metrics.outerBounds.y1; y++ {
		for x := m.metrics.outerBounds.x0; x < m.metrics.outerBounds.x1; x++ {
			lx, ly := m.metrics.globalToLocal(x, y)
			if _, hit := m.hits.at(lx, ly); hit {
				continue
			}
			m = applyAll(m, tea.MouseClickMsg{Button: tea.MouseLeft, X: x, Y: y})
			misses++
			if m.modal != s || s.list.CursorID() != selected || s.list.Offset() != offset || s.loading || m.sel.active {
				t.Fatalf("non-target cell %d,%d activated or leaked through modal", x, y)
			}
		}
	}
	if misses == 0 {
		t.Fatal("fixture lacked border/padding/overflow/whitespace misses")
	}
	_ = m.View()
	m, cmd := clickFrameHit(m, userModelMouseHit(t, m, func(hit userModelHit) bool { return hit.key != "" && hit.key != selected }), false)
	if cmd != nil || s.list.CursorID() == selected || m.modal != s {
		t.Fatal("enabled-row positive control did not select")
	}
}

func TestUserModelMouseLateRepliesAcrossPointerLifecycle(t *testing.T) {
	for _, transition := range []string{"navigation", "back", "close-reopen", "replacement"} {
		for _, fail := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/error=%v", transition, fail), func(t *testing.T) {
				m, s := openedUserModel(t, newUserModelModel(t, sampleUserModel(), client.Capabilities{UserModel: true}))
				m.deps.NoAltScreen = false
				_ = m.View()
				m, cmd := clickFrameHit(m, userModelMouseHit(t, m, func(hit userModelHit) bool { return hit.inspect }), false)
				late := cmd().(client.UserModelDetailMsg)
				switch transition {
				case "navigation":
					m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyDown})
				case "back":
					m = applyAll(m, late)
					_ = m.View()
					m, _ = clickFrameHit(m, userModelMouseHit(t, m, func(hit userModelHit) bool { return hit.close }), false)
				case "close-reopen":
					_ = m.View()
					m, _ = clickFrameHit(m, userModelMouseHit(t, m, func(hit userModelHit) bool { return hit.close }), false)
					m, s = openedUserModel(t, m)
				case "replacement":
					(&m).closeModal()
					m, s = openedUserModel(t, m)
				}
				if fail {
					late.Err = errors.New("late failure")
				}
				selected, generation := s.list.CursorID(), s.generation
				m = applyAll(m, late)
				if m.modal != s || s.view != userModelPanel || s.loading || s.err != nil || s.detail != nil || s.list.CursorID() != selected || s.generation != generation {
					t.Fatal("late detail reply changed current owner")
				}
				_ = m.View()
				m, cmd = clickFrameHit(m, userModelMouseHit(t, m, func(hit userModelHit) bool { return hit.inspect }), false)
				if cmd == nil || m.modal != s || !s.loading {
					t.Fatal("late reply blocked a current positive-control Inspect")
				}
			})
		}
	}
}
