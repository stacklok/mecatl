package ui

import (
	"slices"
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// Private clipboard messages and nested widget batches carry immutable provenance.
type mcpArgumentEditorMsg struct {
	owner       *mcpState
	form, focus uint64
	field       int
	msg         tea.Msg
}

func (mcpArgumentEditorMsg) isSurfaceInputMsg() {}

func (input mcpArgumentEditorMsg) wrap(cmd tea.Cmd) tea.Cmd {
	if cmd == nil {
		return nil
	}
	return func() tea.Msg {
		msg := cmd()
		if batch, ok := msg.(tea.BatchMsg); ok {
			for i, child := range batch {
				batch[i] = input.wrap(child)
			}
			return batch
		}
		input.msg = msg
		return input
	}
}

func (s *mcpState) argumentEditorCmd(cmd tea.Cmd) tea.Cmd {
	return (mcpArgumentEditorMsg{owner: s, form: s.argFormGeneration, focus: s.argFocusGeneration, field: s.argCursor}).wrap(cmd)
}

func (s *mcpState) updateArgument(msg tea.Msg) tea.Cmd {
	if s.view != mcpPromptArgs || s.compact || s.zeroOffer || s.loading || s.argCursor >= len(s.argFields) {
		return nil
	}
	var cmd tea.Cmd
	field := &s.argFields[s.argCursor]
	field.input, cmd = field.input.Update(msg)
	return s.argumentEditorCmd(cmd)
}

func (s *mcpState) leavePromptArgs() {
	s.cancelPickerRequest()
	s.argFormGeneration++
	for i := range s.argFields {
		s.argFields[i].input.Blur()
	}
	s.argFields = nil
	s.errMsg = ""
	s.view = mcpPrompts
}

func (s *mcpState) focusArg(i int) tea.Cmd {
	s.argFocusGeneration++
	s.argCursor = (i + len(s.argFields) + 2) % (len(s.argFields) + 2)
	var cmd tea.Cmd
	for j := range s.argFields {
		s.argFields[j].input.Blur()
		if j == s.argCursor {
			cmd = s.argFields[j].input.Focus()
		}
	}
	return s.argumentEditorCmd(cmd)
}

func (s *mcpState) handlePromptArgsKey(msg tea.KeyPressMsg) (tea.Cmd, bool, bool) {
	switch {
	case key.Matches(msg, s.deps.keys.Close):
		s.leavePromptArgs()
	case key.Matches(msg, s.deps.keys.Choose):
		switch s.argCursor {
		case len(s.argFields):
			s.leavePromptArgs()
		case len(s.argFields) + 1:
			return s.submitPromptArgs(), true, false
		default:
			return s.focusArg(s.argCursor + 1), true, false
		}
	case msg.String() == "shift+tab", msg.String() == "up":
		return s.focusArg(s.argCursor - 1), true, false
	case msg.String() == "tab", msg.String() == "down":
		return s.focusArg(s.argCursor + 1), true, false
	default:
		return s.updateArgument(msg), true, false
	}
	return nil, true, false
}

func (s *mcpState) argumentHints(focus int, pending bool) string {
	back := ""
	if s.deps.keys.Close.Enabled() {
		back = s.deps.marks.closeOnly + " back"
	}
	if pending {
		return back
	}
	hints := []string{}
	for _, control := range []struct {
		msg   tea.KeyPressMsg
		label string
	}{
		{tea.KeyPressMsg{Code: tea.KeyTab}, "tab next"},
		{tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}, "shift+tab previous"},
		{tea.KeyPressMsg{Code: tea.KeyUp}, "↑ previous"},
		{tea.KeyPressMsg{Code: tea.KeyDown}, "↓ next"},
	} {
		if !key.Matches(control.msg, s.deps.keys.Choose, s.deps.keys.Close) {
			hints = append(hints, control.label)
		}
	}
	if s.deps.keys.Choose.Enabled() {
		choose := ""
		for i, chord := range s.deps.keys.Choose.Keys() {
			if s.deps.keys.Close.Enabled() && slices.Contains(s.deps.keys.Close.Keys(), chord) {
				continue
			}
			choose = chord
			if i == 0 {
				choose = s.deps.marks.choose
			}
			break
		}
		action := mcpPickerActionNext
		if focus == len(s.argFields) {
			action = mcpPickerActionCancel
		}
		if focus == len(s.argFields)+1 {
			action = "submit"
		}
		if choose != "" {
			hints = append(hints, choose+" "+action)
		}
	}
	if back != "" {
		hints = append(hints, back)
	}
	return strings.Join(hints, " · ")
}

func (s *mcpState) renderPromptArgs(width, height int) string {
	th := s.deps.theme
	text := lipgloss.NewStyle().Foreground(lipgloss.Color(th.Palette.Text))
	prefix := skillsTextLines(th.Style("askTitle"), connectDisplay("arguments for "+s.argPrompt.Name), width)
	prefix = append(prefix, skillsTextLines(th.Style("muted"), connectDisplay(s.argPrompt.Server), width)...)
	prefix = append(prefix, "")
	actions := []string{}
	for i, name := range []string{"Cancel", "Submit"} {
		label := "  [" + name + "]"
		if s.argCursor == len(s.argFields)+i {
			label = "▶ [" + name + "]"
		}
		actions = append(actions, label)
	}
	footer := skillsTextLines(text, strings.Join(actions, "  "), width)
	footer = append(footer, "")
	statusStyle, status := th.Style("muted"), "Required arguments only"
	if s.loading {
		status = "loading…"
	}
	if s.errMsg != "" {
		statusStyle, status = th.Style("errorText"), "✗ "+s.errCls.String()+": "+s.errMsg
	}
	// Reserve two feedback rows, including before validation or an RPC fails.
	feedback := skillsTextLines(statusStyle, connectDisplay(status), width)
	for len(feedback) < 2 {
		feedback = append(feedback, "")
	}
	if len(feedback) > 2 {
		feedback = feedback[:2]
		feedback[1] = ansi.Truncate(feedback[1]+"…", width, "…")
	}
	footer = append(footer, feedback...)
	hints := skillsTextLines(th.Style("muted"), connectDisplay(s.argumentHints(s.argCursor, s.loading)), width)
	hintRows := len(hints)
	for _, focus := range []int{0, len(s.argFields), len(s.argFields) + 1} {
		hintRows = max(hintRows, len(skillsTextLines(th.Style("muted"), connectDisplay(s.argumentHints(focus, false)), width)))
	}
	hints = append(hints, make([]string, hintRows-len(hints))...)
	footer = append(footer, hints...)
	bodyHeight := height - len(prefix) - len(footer)
	if width < 4 || bodyHeight < 2 {
		return s.pickerCompact(width)
	}

	styles := textinput.DefaultStyles(true)
	styles.Focused.Text, styles.Blurred.Text = text, text
	styles.Focused.Placeholder, styles.Blurred.Placeholder = th.Style("muted"), th.Style("muted")
	styles.Focused.Prompt, styles.Blurred.Prompt = text, text
	styles.Cursor.Color = lipgloss.Color(th.Palette.Text)
	var rows []string
	focusStart, focusEnd := 0, 0
	for i := range s.argFields {
		f := &s.argFields[i]
		// Bubbles excludes the cursor cell and only recomputes overflow on cursor updates.
		f.input.SetWidth(width - 3)
		f.input.SetCursor(f.input.Position())
		f.input.SetStyles(styles)
		label := "  " + connectDisplay(f.name)
		if i == s.argCursor {
			label = "▶ " + connectDisplay(f.name)
			focusStart = len(rows)
		}
		rows = append(rows, skillsTextLines(text.Bold(true), label, width)...)
		rows = append(rows, ansi.Truncate("  "+f.input.View(), width, "…"))
		if i == s.argCursor {
			focusEnd = len(rows)
		}
	}
	if focusEnd-focusStart > bodyHeight {
		return s.pickerCompact(width)
	}
	offset := min(s.argOffset, max(0, len(rows)-bodyHeight))
	if s.argCursor < len(s.argFields) {
		if focusStart < offset {
			offset = focusStart
		}
		if focusEnd > offset+bodyHeight {
			offset = focusEnd - bodyHeight
		}
	}
	s.argOffset = offset
	bodyHeight = min(bodyHeight, max(2, len(rows)))
	end := min(len(rows), offset+bodyHeight)
	body := append([]string(nil), rows[offset:end]...)
	body = append(body, make([]string, bodyHeight-len(body))...)
	s.compact = false
	return strings.Join(append(append(prefix, body...), footer...), "\n")
}
