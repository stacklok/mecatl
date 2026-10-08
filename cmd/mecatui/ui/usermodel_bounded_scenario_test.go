package ui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/ui/internal/bounded"
)

func TestMecatuiSavedMemoryBoundedBrowser_Scenario1_ResponsiveCardAndBodyBudget(t *testing.T) {
	entries := make([]client.UserModelEntry, 30)
	for i := range entries {
		entries[i] = client.UserModelEntry{Key: strings.Repeat("k", i+1), Description: "description"}
	}
	m := newUserModelModel(t, &fakeUserModel{model: client.UserModel{Entries: entries, Detail: &client.UserModelDetail{Current: client.UserModelRevision{Key: entries[0].Key, Value: strings.Repeat("saved memory detail ", 200)}}}}, client.Capabilities{UserModel: true})
	mm, cmd := m.runUserModel()
	m = feedCmd(t, mm.(Model), cmd)
	for _, width := range []int{60, 140} {
		m = applyAll(m, tea.WindowSizeMsg{Width: width, Height: 48})
		_ = m.View()
		want := min(width, 128)
		if m.metrics.outerBounds.x1-m.metrics.outerBounds.x0 != want {
			t.Fatalf("width=%d card width=%d, want %d", width, m.metrics.outerBounds.x1-m.metrics.outerBounds.x0, want)
		}
		if m.metrics.outerBounds.x0 != (width-want)/2 {
			t.Fatalf("card not centered at width %d: %+v", width, m.metrics.outerBounds)
		}
		if m.metrics.outerBounds.y1 > convTopRow(m)+m.vp.Height() {
			t.Fatalf("card exceeds height: %+v", m.metrics.outerBounds)
		}
		if lipgloss.Width(m.renderBody()) > width {
			t.Fatalf("body overflows %d", width)
		}
		if width == 140 {
			s := m.modal.(*userModelState)
			content := m.metrics.contentBounds
			budget := content.y1 - content.y0 - len(skillsTextLines(m.deps.Theme.Style("askTitle"), "Saved memory", content.x1-content.x0)) - len(skillsTextLines(m.deps.Theme.Style("muted"), renderUserModelMeta(s.model), content.x1-content.x0)) - len(skillsTextLines(m.deps.Theme.Style("muted"), "↑/↓ select · enter inspect · the agent saves and updates these facts · "+s.deps.marks.closeOnly+" close", content.x1-content.x0))
			view := s.list.View()
			indicators := 0
			if view.Above > 0 {
				indicators++
			}
			if view.Below > 0 {
				indicators++
			}
			if got := s.list.Height() + indicators; got != budget || len(view.Rows) != s.list.Height() || budget <= 15 {
				t.Fatalf("panel usable rows=%d + indicators=%d, offered content budget=%d, visible=%d", s.list.Height(), indicators, budget, len(view.Rows))
			}
			if got := len(strings.Split(stripANSIstr(m.renderBody()), "\n")); got != m.metrics.outerBounds.y1-m.metrics.outerBounds.y0 {
				t.Fatalf("panel frame/chrome body rows=%d, card budget=%d", got, m.metrics.outerBounds.y1-m.metrics.outerBounds.y0)
			}
		}
	}
	mm, cmd, _ = m.dispatchSurfaceKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = feedCmd(t, mm.(Model), cmd)
	for _, width := range []int{60, 140} {
		m = applyAll(m, tea.WindowSizeMsg{Width: width, Height: 48})
		_ = m.View()
		want := min(width, 128)
		if got := m.metrics.outerBounds.x1 - m.metrics.outerBounds.x0; got != want || m.metrics.outerBounds.x0 != (width-want)/2 {
			t.Fatalf("detail card width/placement at %d: %+v", width, m.metrics.outerBounds)
		}
		if got := lipgloss.Width(m.renderBody()); got > width {
			t.Fatalf("detail body overflows %d: %d", width, got)
		}
		if width == 140 {
			s := m.modal.(*userModelState)
			content := m.metrics.contentBounds
			budget := content.y1 - content.y0 - len(skillsTextLines(m.deps.Theme.Style("askTitle"), "User model detail", content.x1-content.x0)) - len(skillsTextLines(m.deps.Theme.Style("muted"), s.deps.marks.scroll+" scroll · read-only · ask the agent to remove or restore this saved fact · "+s.deps.marks.closeOnly+" back", content.x1-content.x0))
			if got := s.viewport.Height(); got != budget || len(s.viewport.View(s.detailLines).Rows) != budget || budget <= 15 {
				t.Fatalf("detail usable rows=%d, offered content budget=%d, visible=%d", got, budget, len(s.viewport.View(s.detailLines).Rows))
			}
			if got := len(strings.Split(stripANSIstr(m.renderBody()), "\n")); got != m.metrics.outerBounds.y1-m.metrics.outerBounds.y0 {
				t.Fatalf("detail frame/chrome body rows=%d, card budget=%d", got, m.metrics.outerBounds.y1-m.metrics.outerBounds.y0)
			}
		}
	}
}

func TestMecatuiSavedMemoryBoundedBrowser_Scenario1_RootResizeKeepsRailFitAndAnchor(t *testing.T) {
	entries := make([]client.UserModelEntry, 10)
	for i := range entries {
		entries[i] = client.UserModelEntry{
			Key:         fmt.Sprintf("fact/%02d/界-%s", i, strings.Repeat("long-key-", 8)),
			Description: strings.Repeat("wide ���🙂 saved-memory description ", 6),
		}
	}
	m, s := savedMemoryOpened(t, entries)

	m = applyAll(m, tea.WindowSizeMsg{Width: 44, Height: 30})
	_ = m.View()
	for range 6 {
		m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyDown})
	}
	s.list.Scroll(bounded.Top)
	_ = m.View()
	selected := s.list.CursorID()
	anchor := s.list.View().Rows[0]
	if anchor.ItemLine != 0 {
		t.Fatalf("test requires a feasible first physical anchor, got {%q,%d}", anchor.ID, anchor.ItemLine)
	}

	for _, width := range []int{44, 120, 44} {
		m = applyAll(m, tea.WindowSizeMsg{Width: width, Height: 30})
		rendered := m.View().Content
		if s.compact {
			t.Fatalf("width %d unexpectedly used compact saved-memory output", width)
		}
		if strings.Contains(rendered, userModelDescriptionMarker) {
			t.Fatalf("width %d leaked the internal description marker", width)
		}
		if !strings.Contains(rendered, m.deps.Theme.Style("muted").Render(userModelDescriptionRail)) {
			t.Fatalf("width %d did not render the description rail with the muted style", width)
		}
		if got := lipgloss.Width(rendered); got > width {
			t.Fatalf("width %d rendered %d cells", width, got)
		}
		rows := s.list.View().Rows
		if len(rows) == 0 || s.list.CursorID() != selected || rows[0].ID != anchor.ID || rows[0].ItemLine != anchor.ItemLine {
			t.Fatalf("width %d lost selected key or feasible top anchor: selected=%q top=%#v, want selected=%q anchor=%#v", width, s.list.CursorID(), rows, selected, anchor)
		}
	}
}
