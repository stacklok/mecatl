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
	m := newUserModelModel(t, &fakeUserModel{model: client.UserModel{Entries: entries}}, client.Capabilities{UserModel: true})
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
			if s := m.modal.(*userModelState); len(s.list.View().Rows) <= 14 {
				t.Fatalf("normal body still capped at fourteen rows: %d", len(s.list.View().Rows))
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
	}
}
