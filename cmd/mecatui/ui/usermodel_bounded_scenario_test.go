package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
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
			if got := len(s.list.View().Rows); got <= 14 {
				t.Fatalf("normal body still capped at fourteen rows: %d", got)
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
			if got := len(s.viewport.View(s.detailLines).Rows); got <= 14 {
				t.Fatalf("detail body still capped at fourteen rows: %d", got)
			}
			if got := len(strings.Split(stripANSIstr(m.renderBody()), "\n")); got != m.metrics.outerBounds.y1-m.metrics.outerBounds.y0 {
				t.Fatalf("detail frame/chrome body rows=%d, card budget=%d", got, m.metrics.outerBounds.y1-m.metrics.outerBounds.y0)
			}
		}
	}
}
