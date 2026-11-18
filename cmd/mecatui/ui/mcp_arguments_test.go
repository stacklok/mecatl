package ui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
)

type argumentMCP struct {
	*fakeMCP
	lastPromptArgs map[string]string
}

func (f *argumentMCP) GetMCPPrompt(ctx context.Context, server, name string, args map[string]string) (string, []client.MCPPromptMessage, error) {
	f.lastPromptArgs = args
	return f.fakeMCP.GetMCPPrompt(ctx, server, name, args)
}

func mcpArgumentModel(t *testing.T, count int) (Model, *argumentMCP) {
	t.Helper()
	f := &argumentMCP{fakeMCP: samplePromptMCP()}
	f.prompts = f.prompts[:1]
	f.prompts[0].Arguments = nil
	for i := range count {
		f.prompts[0].Arguments = append(f.prompts[0].Arguments, client.MCPPromptArgument{Name: fmt.Sprintf("field-%02d\tlabel\x1b[31m", i), Required: true})
	}
	f.prompts[0].Arguments = append(f.prompts[0].Arguments, client.MCPPromptArgument{Name: "optional"})
	m := openOverlay(t, newMCPModel(t, aztec(), f), tea.KeyPressMsg{Code: tea.KeyF8})
	m, _ = pickerKey(m, tea.KeyEnter)
	return m, f
}

func TestMCPArgumentExplicitActionsAndTraversal(t *testing.T) {
	m, f := mcpArgumentModel(t, 2)
	s := mcpActive(m)
	m.prompt.Rewrite("draft")
	for i := range 2 {
		m = applyAll(m, tea.PasteMsg{Content: fmt.Sprintf(" value-%d ", i)})
		m, _ = pickerKey(m, tea.KeyEnter)
	}
	if s.argCursor != 2 || s.loading || f.getPromptCalls != 0 || len(s.argFields) != 2 {
		t.Fatal("last field Choose must advance to Cancel without fetching")
	}
	m, _ = pickerKey(m, tea.KeyDown)
	if s.argCursor != 3 {
		t.Fatal("Down must reach Submit")
	}
	m, _ = pickerKey(m, tea.KeyUp)
	if s.argCursor != 2 {
		t.Fatal("Up must reach Cancel")
	}
	m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
	if s.argCursor != 1 {
		t.Fatal("backtab must return to last field")
	}
	m, _ = pickerKey(m, tea.KeyTab)
	m, _ = pickerKey(m, tea.KeyTab)
	m, cmd := pickerKey(m, tea.KeyEnter)
	if !s.loading || cmd == nil {
		t.Fatal("explicit Submit must fetch")
	}
	m = applyAll(m, tea.PasteMsg{Content: "hidden"}, tea.KeyPressMsg{Code: 'x', Text: "x"})
	if s.argFields[1].input.Value() != " value-1 " {
		t.Fatal("pending form was edited")
	}
	m = feedMCPInsertion(t, m, cmd)
	if m.modal != nil || !strings.Contains(m.prompt.Value(), "Please review") {
		t.Fatal("accepted fetch did not insert")
	}
	if f.lastPromptArgs[s.argFields[0].name] != "value-0" {
		t.Fatal("required values were not trimmed")
	}
}

func TestMCPArgumentBoundedFocusAndCompact(t *testing.T) {
	m, f := mcpArgumentModel(t, 30)
	s := mcpActive(m)
	m = applyAll(m, tea.WindowSizeMsg{Width: 78, Height: 26})
	_ = m.View()
	dimensions := *m.metrics
	for i := range 30 {
		m = applyAll(m, tea.PasteMsg{Content: fmt.Sprintf("v%d", i)})
		body := stripANSIstr(m.renderBody())
		if !strings.Contains(body, fmt.Sprintf("field-%02d", i)) || !strings.Contains(body, "Cancel") || !strings.Contains(body, "Submit") || strings.ContainsAny(body, "\t\x1b") {
			t.Fatalf("focused field/actions not safely visible at %d: %q", i, body)
		}
		_ = m.View()
		if *m.metrics != dimensions {
			t.Fatal("focus resized form")
		}
		m, _ = pickerKey(m, tea.KeyEnter)
	}
	m, _ = pickerKey(m, tea.KeyTab)
	if s.argCursor != len(s.argFields)+1 {
		t.Fatal("precondition: Submit must be focused before compact input")
	}
	focus, value, calls := s.argCursor, s.argFields[29].input.Value(), f.getPromptCalls
	m = applyAll(m, tea.WindowSizeMsg{Width: 8, Height: 3})
	for _, msg := range []tea.Msg{tea.PasteMsg{Content: "hidden"}, tea.KeyPressMsg{Code: tea.KeyEnter}, tea.KeyPressMsg{Code: tea.KeyTab}, tea.MouseWheelMsg{Button: tea.MouseWheelDown}} {
		mm, cmd := m.Update(msg)
		m = mm.(Model)
		if cmd != nil {
			t.Fatalf("compact input %T produced a command", msg)
		}
	}
	if !s.compact || s.argCursor != focus || s.argFields[29].input.Value() != value || s.loading || f.getPromptCalls != calls || m.modal != s {
		t.Fatal("compact Submit changed form or started an RPC")
	}
	m = applyAll(m, client.SessionTitleMsg{Title: "root compact", Revision: 1})
	if m.sessionTitle != "root compact" {
		t.Fatal("compact form swallowed unrelated event")
	}
	for _, size := range [][2]int{{128, 24}, {30, 18}, {80, 8}, {1, 1}, {0, 0}, {-1, -1}} {
		m = applyAll(m, tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		_ = m.View()
		if m.width <= 0 || m.vp.Height() <= 0 {
			if !s.compact {
				t.Fatalf("%v: unusable root offer did not compact", size)
			}
			continue
		}
		b := m.metrics.outerBounds
		if b.x0 < 0 || b.x1 > m.width || b.y0 < convTopRow(m) || b.y1 > convTopRow(m)+m.vp.Height() {
			t.Fatalf("%v: root-rendered form bounds %+v outside conversation region", size, b)
		}
		if !s.compact && b.x1-b.x0 > 128 {
			t.Fatalf("%v: framed form exceeds width cap: %+v", size, b)
		}
		if strings.Contains(m.renderBody(), "\x1b[31m") {
			t.Fatal("untrusted form escape survived styling")
		}
	}
	m = applyAll(m, tea.WindowSizeMsg{Width: 78, Height: 26})
	_ = m.View()
	body := stripANSIstr(m.renderBody())
	if s.argCursor != focus || !strings.Contains(body, "Submit") || s.argFields[29].input.Value() != value {
		t.Fatal("resize lost form state")
	}
	m, cmd := pickerKey(m, tea.KeyEnter)
	if !s.loading || cmd == nil {
		t.Fatal("restored Submit did not fetch")
	}
	m = feedMCPInsertion(t, m, cmd)
	if m.modal != nil || f.getPromptCalls != calls+1 {
		t.Fatal("restored Submit did not complete")
	}
}

func TestMCPArgumentClipboardOwnership(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX clipboard fixture")
	}
	if os.Getenv("MECATL_TEST_MCP_CLIPBOARD") == "" {
		dir := t.TempDir()
		for _, name := range []string{"xclip", "pbpaste"} {
			if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\nprintf 'queued clipboard'\n"), 0o700); err != nil {
				t.Fatal(err)
			}
		}
		t.Setenv("PATH", dir)
		t.Setenv("WAYLAND_DISPLAY", "")
		t.Setenv("MECATL_TEST_MCP_CLIPBOARD", "1")
		exe, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command(exe, "-test.run=^TestMCPArgumentClipboardOwnership$", "-test.count=1").CombinedOutput(); err != nil {
			t.Fatalf("clipboard subprocess: %v\n%s", err, out)
		}
		return
	}
	for _, transition := range []string{"current", "focus", "form", "picker", "compact", "batch current", "batch focus", "batch form", "batch picker", "batch compact"} {
		t.Run(transition, func(t *testing.T) {
			m, _ := mcpArgumentModel(t, 2)
			m.prompt.Rewrite("root draft")
			s := mcpActive(m)
			mm, cmd := m.Update(ctrlKey('v'))
			m = mm.(Model)
			receipts := scheduleMessages(cmd)
			if len(receipts) != 1 || fmt.Sprintf("%T", receipts[0].(mcpArgumentEditorMsg).msg) != "textinput.pasteMsg" {
				t.Fatalf("not private clipboard receipt: %#v", receipts)
			}
			var delayed tea.Cmd
			if strings.HasPrefix(transition, "batch ") {
				private := receipts[0].(mcpArgumentEditorMsg).msg
				delayed = s.argumentEditorCmd(func() tea.Msg {
					return tea.BatchMsg{func() tea.Msg {
						return tea.BatchMsg{func() tea.Msg { return private }}
					}}
				})
			}
			transition := strings.TrimPrefix(transition, "batch ")
			switch transition {
			case "focus":
				m, _ = pickerKey(m, tea.KeyTab)
				m = applyAll(m, receipts...)
				m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
			case "form":
				m, _ = pickerKey(m, tea.KeyEsc)
				m, _ = pickerKey(m, tea.KeyEnter)
			case "picker":
				m, _ = pickerKey(m, tea.KeyEsc)
				m, _ = pickerKey(m, tea.KeyEsc)
				m = openOverlay(t, m, tea.KeyPressMsg{Code: tea.KeyF8})
				m, _ = pickerKey(m, tea.KeyEnter)
				s = mcpActive(m)
			case "compact":
				m = applyAll(m, tea.WindowSizeMsg{Width: 8, Height: 3})
				m = applyAll(m, receipts...)
				m = applyAll(m, tea.WindowSizeMsg{Width: 100, Height: 30})
			}
			if delayed != nil {
				receipts = scheduleMessages(delayed)
			}
			m = applyAll(m, receipts...)
			want := ""
			if transition == "current" {
				want = "queued clipboard"
			}
			if s.argFields[0].input.Value() != want || s.argFields[1].input.Value() != "" || m.prompt.Value() != "root draft" {
				t.Fatal("clipboard provenance failed")
			}
			if transition != "current" {
				mm, cmd = m.Update(ctrlKey('v'))
				m = applyAll(mm.(Model), scheduleMessages(cmd)...)
				if s.argFields[0].input.Value() != "queued clipboard" {
					t.Fatal("current clipboard did not work after transition")
				}
			}
		})
	}
}

func TestMCPArgumentEditorGeometryAndTheme(t *testing.T) {
	m, _ := mcpArgumentModel(t, 1)
	s := mcpActive(m)
	m = applyAll(m, tea.WindowSizeMsg{Width: 56, Height: 30}, tea.PasteMsg{Content: strings.Repeat("wide value ", 30)})
	_ = m.View()
	body, _ := s.Render(46, 20)
	if s.compact || lipgloss.Width(body) > 46 {
		t.Fatal("editor does not fit bounded form")
	}
	input := s.argFields[0].input
	if lipgloss.Width(input.View()) > 44 {
		t.Fatalf("editor cursor is cropped: input width %d, budget 44", lipgloss.Width(input.View()))
	}
	styles := input.Styles()
	want := lipgloss.Color(s.deps.theme.Palette.Text)
	if styles.Focused.Text.GetForeground() != want || styles.Blurred.Text.GetForeground() != want {
		t.Fatal("editable values must use normal theme text")
	}
}

func TestMCPArgumentWhitespaceOnlyValidation(t *testing.T) {
	m, f := mcpArgumentModel(t, 2)
	s := mcpActive(m)
	s.argFields[0].input.SetValue(" \t ")
	for range 3 {
		m, _ = pickerKey(m, tea.KeyTab)
	}
	if s.argCursor != len(s.argFields)+1 {
		t.Fatal("precondition: Submit must be focused")
	}
	m, _ = pickerKey(m, tea.KeyEnter)
	if s.loading || s.errCls != client.MCPErrInput || s.argCursor != 0 || f.getPromptCalls != 0 {
		t.Fatal("whitespace-only required value must focus the field without fetching")
	}
	if !strings.Contains(stripANSIstr(m.renderBody()), "field-00") {
		t.Fatal("whitespace validation feedback did not retain the invalid field")
	}
}

func TestMCPArgumentValidationFeedbackAndFocus(t *testing.T) {
	m, f := mcpArgumentModel(t, 15)
	s := mcpActive(m)
	m = applyAll(m, tea.WindowSizeMsg{Width: 76, Height: 26})
	m = applyAll(m, tea.PasteMsg{Content: "first"})
	for range 17 {
		m, _ = pickerKey(m, tea.KeyTab)
	}
	if s.argCursor != 0 {
		t.Fatal("Tab traversal must wrap after Submit")
	}
	m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
	if s.argCursor != 16 {
		t.Fatal("Backtab traversal must wrap to Submit")
	}
	_ = m.View()
	dimensions := *m.metrics
	m, _ = pickerKey(m, tea.KeyEnter)
	_ = m.View()
	if s.argCursor != 1 || s.errCls != client.MCPErrInput || f.getPromptCalls != 0 {
		t.Fatal("validation must focus first blank required field without fetching")
	}
	body := stripANSIstr(m.renderBody())
	if !strings.Contains(body, "field-01") || strings.Index(body, "✗") < strings.Index(body, "Submit") || strings.ContainsAny(body, "\t\x1b") {
		t.Fatalf("unsafe or misplaced validation feedback: %q", body)
	}
	if *m.metrics != dimensions {
		t.Fatal("validation resized form")
	}
	m = applyAll(m, tea.WindowSizeMsg{Width: 8, Height: 3})
	m, _ = pickerKey(m, tea.KeyEsc)
	if s.view != mcpPrompts || len(s.argFields) != 0 || s.errMsg != "" {
		t.Fatal("compact Back must discard form and validation")
	}
}

func TestMCPArgumentShadowedActionHints(t *testing.T) {
	m := newMCPModel(t, aztec(), samplePromptMCP())
	m.keys.Choose = key.NewBinding(key.WithKeys("ctrl+b", "ctrl+s"))
	m.keys.Close = key.NewBinding(key.WithKeys("ctrl+b"))
	m = openOverlay(t, m, tea.KeyPressMsg{Code: tea.KeyF8})
	m = applyAll(m, ctrlKey('s'))
	body := stripANSIstr(m.renderBody())
	if strings.Contains(body, "ctrl+b next") || !strings.Contains(body, "ctrl+s next") {
		t.Fatalf("shadowed Choose is advertised: %q", body)
	}
	m = applyAll(m, ctrlKey('s'))
	if mcpActive(m).argCursor != 1 {
		t.Fatal("unshadowed Choose did not advance")
	}
	m = applyAll(m, ctrlKey('b'))
	if mcpActive(m).view != mcpPrompts {
		t.Fatal("Close must win over shadowed Choose")
	}
}

func TestMCPArgumentRemappedActions(t *testing.T) {
	m := newMCPModel(t, aztec(), samplePromptMCP())
	m.keys.Choose = key.NewBinding(key.WithKeys("ctrl+s"))
	m.keys.Close = key.NewBinding(key.WithKeys("ctrl+b"))
	m = openOverlay(t, m, tea.KeyPressMsg{Code: tea.KeyF8})
	m = applyAll(m, ctrlKey('s'))
	s := mcpActive(m)
	m = applyAll(m, tea.PasteMsg{Content: "x"})
	body := stripANSIstr(m.renderBody())
	if !strings.Contains(body, "ctrl+s next") || !strings.Contains(body, "ctrl+b back") || strings.Contains(body, "enter next") {
		t.Fatalf("untruthful remapped field hints: %q", body)
	}
	m = applyAll(m, ctrlKey('s'))
	if s.argCursor != 1 || s.loading {
		t.Fatal("remapped Choose did not advance")
	}
	if !strings.Contains(stripANSIstr(m.renderBody()), "ctrl+s cancel") {
		t.Fatal("Cancel hint is not focus-specific")
	}
	m = applyAll(m, ctrlKey('s'))
	if s.view != mcpPrompts || len(s.argFields) != 0 {
		t.Fatal("Cancel did not discard form")
	}
	m = applyAll(m, ctrlKey('s'), tea.PasteMsg{Content: "value"}, tea.KeyPressMsg{Code: tea.KeyTab}, tea.KeyPressMsg{Code: tea.KeyTab})
	if !strings.Contains(stripANSIstr(m.renderBody()), "ctrl+s submit") {
		t.Fatal("Submit hint is not focus-specific")
	}
	mm, cmd := m.Update(ctrlKey('s'))
	m = mm.(Model)
	if !s.loading || cmd == nil {
		t.Fatal("remapped Submit did not fetch")
	}
	body = stripANSIstr(m.renderBody())
	if strings.Contains(body, "ctrl+s submit") || strings.Contains(body, "tab next") || !strings.Contains(body, "ctrl+b back") {
		t.Fatal("pending hints expose blocked actions")
	}
	m = applyAll(m, ctrlKey('b'))
	if s.view != mcpPrompts || s.loading || len(s.argFields) != 0 {
		t.Fatal("remapped Back did not abandon pending form")
	}
	m = applyAll(m, runCmd(cmd))
	if m.modal != s || s.view != mcpPrompts {
		t.Fatal("abandoned remapped Submit changed list")
	}
}
