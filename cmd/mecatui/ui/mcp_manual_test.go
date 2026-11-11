package ui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
)

func mcpTextClick(t *testing.T, m Model, text string) tea.MouseClickMsg {
	t.Helper()
	for y, line := range strings.Split(stripANSIstr(m.View().Content), "\n") {
		if x := strings.Index(line, text); x >= 0 {
			return tea.MouseClickMsg{Button: tea.MouseLeft, X: ansi.StringWidth(line[:x]), Y: y}
		}
	}
	t.Fatalf("no rendered %q", text)
	return tea.MouseClickMsg{}
}

func TestMCPManualInventoryClickSelectsOnly(t *testing.T) {
	for _, resources := range []bool{true, false} {
		t.Run(fmt.Sprint(resources), func(t *testing.T) {
			f := browserInventory()
			f.resources[1].Name = "item-01\ncontinuation-01\nlast-line-01"
			f.prompts[1].Name = f.resources[1].Name
			m := newMCPModel(t, aztec(), f)
			m.deps.NoAltScreen = false
			open := tea.KeyPressMsg{Code: tea.KeyF8}
			if resources {
				open = ctrlKey('r')
			}
			m = openOverlay(t, m, open)
			s := mcpActive(m)
			list := &s.prList
			wantID := mcpItemID(f.prompts[1].Server, f.prompts[1].Name)
			if resources {
				list = &s.resList
				wantID = mcpItemID(f.resources[1].Server, f.resources[1].URI)
			}
			for _, text := range []string{"item-01", "continuation-01", "last-line-01"} {
				click := mcpTextClick(t, m, text)
				for range 2 {
					mm, cmd := m.Update(click)
					m = mm.(Model)
					if cmd != nil || list.CursorID() != wantID || s.loading || f.getPromptCalls != 0 {
						t.Fatalf("click on %q must select only raw identity %q: got %q loading=%t cmd=%v", text, wantID, list.CursorID(), s.loading, cmd != nil)
					}
					_ = m.View()
				}
			}
		})
	}
}

func TestMCPManualArgumentButtons(t *testing.T) {
	m, f := mcpArgumentModel(t, 1)
	m.deps.NoAltScreen = false
	s := mcpActive(m)
	click := mcpTextClick(t, m, "[Submit]")
	mm, _ := m.Update(click)
	m = mm.(Model)
	if s.errCls != client.MCPErrInput || s.errMsg == "" || s.argCursor != 0 || f.getPromptCalls != 0 {
		t.Fatal("clicked blank Submit did not validate and focus required field")
	}
	m = applyAll(m, tea.PasteMsg{Content: "value"})
	click = mcpTextClick(t, m, "[Submit]")
	mm, cmd := m.Update(click)
	m = mm.(Model)
	if cmd == nil || !s.loading {
		t.Fatal("clicked valid Submit did not fetch")
	}
	mm, duplicate := m.Update(click)
	m = mm.(Model)
	if duplicate != nil {
		t.Fatal("duplicate click submitted twice")
	}
	m = feedMCPInsertion(t, m, cmd)
	if f.getPromptCalls != 1 || m.modal != nil {
		t.Fatal("submit not accepted exactly once")
	}
	m, _ = mcpArgumentModel(t, 1)
	m.deps.NoAltScreen = false
	click = mcpTextClick(t, m, "[Cancel]")
	mm, _ = m.Update(click)
	m = mm.(Model)
	if mcpActive(m).view != mcpPrompts {
		t.Fatal("clicked Cancel did not return to inventory")
	}
}
