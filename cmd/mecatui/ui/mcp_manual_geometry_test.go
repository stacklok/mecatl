package ui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
)

func TestMCPManualInventoryScrolledPartialAndHitIsolation(t *testing.T) {
	for _, resources := range []bool{true, false} {
		t.Run(fmt.Sprint(resources), func(t *testing.T) {
			f := browserInventory()
			f.resources[1].Name = "item-01\ncontinuation-01\nlast-line-01"
			f.prompts[1].Name = f.resources[1].Name
			f.resources[0].Name = strings.Repeat("selected continuation\n", 10)
			f.prompts[0].Name = f.resources[0].Name
			m := newMCPModel(t, aztec(), f)
			m.deps.NoAltScreen = false
			open := tea.KeyPressMsg{Code: tea.KeyF8}
			if resources {
				open = ctrlKey('r')
			}
			m = openOverlay(t, m, open)
			m = applyAll(m, tea.WindowSizeMsg{Width: 32, Height: 24})
			s := mcpActive(m)
			list := &s.prList
			wantID := mcpItemID(f.prompts[1].Server, f.prompts[1].Name)
			if resources {
				list = &s.resList
				wantID = mcpItemID(f.resources[1].Server, f.resources[1].URI)
			}
			var target renderedHitRegion
			found, selectedPartial := false, false
			for range 30 {
				m = applyAll(m, tea.MouseWheelMsg{Button: tea.MouseWheelDown})
				plain := strings.Split(stripANSIstr(m.View().Content), "\n")
				if list.Cursor() != 0 {
					t.Fatal("wheel selected")
				}
				if len(m.hits.frame) == 0 {
					t.Fatal("missing inventory hits")
				}
				first := m.hits.frame[0]
				x, y := m.metrics.localToGlobal(first.rect.x0, first.rect.y0)
				markers := 0
				for i, hit := range m.hits.frame {
					hx, hy := m.metrics.localToGlobal(hit.rect.x0, hit.rect.y0)
					if strings.Contains(ansi.Cut(plain[hy], hx+2, hx+4), "↳") {
						markers++
						if i != 0 {
							t.Fatal("continuation marker repeated below the first visible item row")
						}
					}
				}
				if strings.Contains(ansi.Cut(plain[y], x, x+4), "↳") && markers != 1 {
					t.Fatalf("partial viewport has %d continuation markers, want one", markers)
				}
				if ansi.Cut(plain[y], x, x+4) == "▶ ↳ " {
					selectedPartial = true
				}
				if strings.Contains(plain[y], "continuation-01") && ansi.Cut(plain[y], x, x+4) == "  ↳ " {
					target, found = first, true
					break
				}
			}
			if !found || !selectedPartial {
				t.Fatal("fixture did not expose selected and unselected partial rows")
			}
			old := list.CursorID()
			for _, point := range []cellPoint{
				{x: m.metrics.outerBounds.x0, y: m.metrics.outerBounds.y0},
				{x: m.metrics.contentOrigin.x - 1, y: m.metrics.contentOrigin.y + target.rect.y0},
				{x: m.metrics.contentOrigin.x, y: m.metrics.contentOrigin.y},
				{x: m.metrics.contentOrigin.x, y: m.metrics.contentOrigin.y + target.rect.y0 - 1},
			} {
				mm, cmd := m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: point.x, Y: point.y})
				m = mm.(Model)
				if cmd != nil || list.CursorID() != old {
					t.Fatal("border/padding/chrome click changed selection")
				}
			}
			x, y := m.metrics.localToGlobal(target.rect.x0, target.rect.y0)
			click := tea.MouseClickMsg{Button: tea.MouseLeft, X: x, Y: y}
			m.deps.NoMouse = true
			m = applyAll(m, click)
			if list.CursorID() != old {
				t.Fatal("no-mouse selected")
			}
			m.deps.NoMouse = false
			m = applyAll(m, click)
			if list.CursorID() != wantID || s.loading {
				t.Fatal("partial-row click failed to select only")
			}
			plain := strings.Split(stripANSIstr(m.View().Content), "\n")
			cursorRows := 0
			for _, hit := range m.hits.frame {
				x, y := m.metrics.localToGlobal(hit.rect.x0, hit.rect.y0)
				gutter := ansi.Cut(plain[y], x, x+2)
				if gutter != "  " && gutter != "▶ " {
					t.Fatalf("hit on non-item row: %q", plain[y])
				}
				if strings.Contains(gutter, "▶") {
					cursorRows++
				}
				if _, ok := m.hits.at(hit.rect.x1, hit.rect.y0); ok {
					t.Fatal("row hit includes trailing padding")
				}
			}
			if cursorRows != 1 {
				t.Fatalf("selected first-visible cursor count: %d", cursorRows)
			}
			m = applyAll(m, tea.MouseWheelMsg{Button: tea.MouseWheelDown})
			_ = m.View()
			stale := m.hits.frame[0].id
			before := list.CursorID()
			m = applyAll(m, tea.MouseWheelMsg{Button: tea.MouseWheelDown})
			if len(m.hits.frame) != 0 {
				t.Fatal("preparation retained hits")
			}
			m = applyAll(m, surfaceHitMsg{ID: stale}, click)
			if list.CursorID() != before {
				t.Fatal("stale preparation hit selected")
			}
			_ = m.View()
			stale = m.hits.frame[0].id
			m = applyAll(m, tea.WindowSizeMsg{Width: 8, Height: 3})
			m = applyAll(m, surfaceHitMsg{ID: stale}, click)
			_ = m.View()
			if !s.compact || len(m.hits.frame) != 0 || list.CursorID() != before {
				t.Fatal("compact/stale hit changed inventory")
			}
			m = applyAll(m, tea.WindowSizeMsg{Width: 32, Height: 24})
			_ = m.View()
			if s.compact || len(m.hits.frame) == 0 || list.CursorID() != before {
				t.Fatal("inventory not restored")
			}
			for _, loading := range []bool{true, false} {
				s.loading = loading
				if !loading {
					s.setResources(nil)
					s.setPrompts(nil)
				}
				_ = m.View()
				if len(m.hits.frame) != 0 {
					t.Fatal("loading/empty inventory has hits")
				}
			}
		})
	}
}

func TestMCPPickerHangingIndent(t *testing.T) {
	for _, resources := range []bool{true, false} {
		t.Run(fmt.Sprint(resources), func(t *testing.T) {
			f := sampleResourceMCP()
			f.resources = []client.MCPResource{{Server: "srv", URI: "first", Name: "First"}, {Server: "srv", URI: "next", Name: "Next"}}
			f.prompts = []client.MCPPrompt{{Server: "srv", Name: "First"}, {Server: "srv", Name: "Next"}}
			m := newMCPModel(t, aztec(), f)
			open := tea.KeyPressMsg{Code: tea.KeyF8}
			if resources {
				open = ctrlKey('r')
			}
			m = openOverlay(t, m, open)
			s := mcpActive(m)
			body, _ := s.Render(80, 100)
			if !strings.Contains(stripANSIstr(body), "▶ First  srv\n  Next  srv") {
				t.Fatalf("single-line entries must have no grouping glyphs:\n%s", stripANSIstr(body))
			}
			f.resources[0].Name = "First\nsecond line\nthird line"
			f.prompts[0].Name = f.resources[0].Name
			s.setResources(f.resources)
			s.setPrompts(f.prompts)
			body, _ = s.Render(80, 100)
			if !strings.Contains(stripANSIstr(body), "▶ First\n    second line\n    third line  srv\n  Next  srv") {
				t.Fatalf("continuations must hang under the item:\n%s", stripANSIstr(body))
			}
			for _, name := range []string{strings.Repeat("abcdefgh", 4), strings.Repeat("界e\u0301", 12)} {
				f.resources = []client.MCPResource{{Server: "srv", URI: "raw", Name: name}}
				f.prompts = []client.MCPPrompt{{Server: "srv", Name: name}}
				s.setResources(f.resources)
				s.setPrompts(f.prompts)
				for _, width := range []int{8, 12, 20} {
					body, hits := s.Render(width, 100)
					lines := strings.Split(stripANSIstr(body), "\n")
					var text strings.Builder
					for i, hit := range hits {
						line := lines[hit.rect.y0]
						indent := 4
						if i == 0 {
							indent = 2
						} else if !strings.HasPrefix(line, "    ") {
							t.Fatalf("wrapped continuation lacks indent at width %d: %q", width, line)
						}
						if ansi.StringWidth(line) > width {
							t.Fatalf("indent overflows width %d: %q", width, line)
						}
						text.WriteString(ansi.Cut(line, indent, ansi.StringWidth(line)))
					}
					if text.String() != name+"  srv" {
						t.Fatalf("wrapping lost label content at width %d: %q", width, text.String())
					}
				}
			}
		})
	}
}

func TestMCPManualArgumentWrappedButtonGeometry(t *testing.T) {
	for _, width := range []int{12, 15, 19, 23, 32, 100} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			m, f := mcpArgumentModel(t, 1)
			m.deps.NoAltScreen = false
			m = applyAll(m, tea.WindowSizeMsg{Width: width, Height: 90})
			s := mcpActive(m)
			plain := strings.Split(stripANSIstr(m.View().Content), "\n")
			if s.compact {
				t.Fatal("fixture unexpectedly compact")
			}
			pieces := map[int]string{}
			var submit tea.MouseClickMsg
			for _, hit := range m.hits.frame {
				control := s.hitControls[hit.id]
				x, y := m.metrics.localToGlobal(hit.rect.x0, hit.rect.y0)
				piece := ansi.Cut(plain[y], x, x+hit.rect.x1-hit.rect.x0)
				pieces[control] += piece
				if control == 2 {
					submit = tea.MouseClickMsg{Button: tea.MouseLeft, X: x, Y: y}
				}
			}
			if pieces[1] != "[Cancel]" || pieces[2] != "[Submit]" {
				t.Fatalf("button hits do not match wrapped labels: %#v", pieces)
			}
			staleRender := m.hits.frame[len(m.hits.frame)-1].id
			_ = m.View()
			m = applyAll(m, surfaceHitMsg{ID: staleRender})
			if s.errMsg != "" || s.loading {
				t.Fatal("prior render's Submit remained live")
			}
			m.deps.NoMouse = true
			m = applyAll(m, submit)
			if s.errMsg != "" {
				t.Fatal("no-mouse submitted")
			}
			m.deps.NoMouse = false
			m = applyAll(m, submit)
			if s.errCls != client.MCPErrInput || s.errMsg == "" || f.getPromptCalls != 0 {
				t.Fatal("wrapped submit did not validate")
			}
			_ = m.View()
			stale := m.hits.frame[len(m.hits.frame)-1].id
			m = applyAll(m, tea.PasteMsg{Content: "valid"})
			m = applyAll(m, surfaceHitMsg{ID: stale}, submit)
			if s.loading {
				t.Fatal("prepared, undisplayed form accepted stale Submit")
			}
			_ = m.View()
			stale = m.hits.frame[len(m.hits.frame)-1].id
			m = applyAll(m, tea.WindowSizeMsg{Width: 8, Height: 3})
			m = applyAll(m, surfaceHitMsg{ID: stale}, submit)
			_ = m.View()
			if !s.compact || s.loading || len(m.hits.frame) != 0 {
				t.Fatal("compact form accepted Submit")
			}
			m = applyAll(m, tea.WindowSizeMsg{Width: width, Height: 90})
			_ = m.View()
			if s.compact || s.argFields[0].input.Value() != "valid" {
				t.Fatal("form did not restore")
			}
			hit := m.hits.frame[len(m.hits.frame)-1]
			x, y := m.metrics.localToGlobal(hit.rect.x1-1, hit.rect.y0)
			mm, cmd := m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: x, Y: y})
			m = mm.(Model)
			if cmd == nil || !s.loading {
				t.Fatal("restored wrapped Submit did not submit")
			}
			_ = m.View()
			if len(m.hits.frame) != 0 {
				t.Fatal("pending form has hits")
			}
			m = applyAll(m, submit)
			m = feedMCPInsertion(t, m, cmd)
			if f.getPromptCalls != 1 {
				t.Fatal("pending click duplicated submit")
			}
		})
	}
}

func TestMCPManualMultilinePreviewRender(t *testing.T) {
	f := sampleResourceMCP()
	f.resources[0] = client.MCPResource{Name: "Project notes\nsecond name line", Server: "docs\nsecondary server line", URI: "mcp://project/notes\nversion-two"}
	f.contents = []client.MCPResourceContents{{Text: "First body line\nName: this is body text\nFinal body line"}}
	m := openOverlay(t, newMCPModel(t, aztec(), f), ctrlKey('r'))
	m, cmd := pickerKey(m, tea.KeyEnter)
	m = applyAll(m, runCmd(cmd))
	plain := stripANSIstr(m.View().Content)
	t.Log("Multiline preview frame:\n" + plain)
	for _, text := range []string{"Name:   Project notes", "        second name line", "Server: docs", "        secondary server line", "URI:    mcp://project/notes", "        version-two", "── Content ──"} {
		if !strings.Contains(plain, text) {
			t.Fatalf("missing structured metadata/boundary %q", text)
		}
	}
	if strings.Index(plain, "── Content ──") > strings.Index(plain, "First body line") {
		t.Fatal("content boundary follows body")
	}
	s := mcpActive(m)
	if s.preview != f.contents[0].Text {
		t.Fatal("metadata altered insertion payload")
	}
}
