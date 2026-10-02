package ui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/internal/terminaltext"
	"github.com/stacklok/mecatl/cmd/mecatui/ui/internal/bounded"
	"github.com/stacklok/mecatl/cmd/mecatui/ui/internal/scrollback"
)

// runToolcalls opens the current session's local tool-call projection. It has no
// server dependency, so it remains available while a run streams.
func (m Model) runToolcalls() (tea.Model, tea.Cmd) {
	if m.phase != phaseIdle && m.phase != phaseRunning {
		return m, nil
	}
	m.prompt.Blur()
	s := &toolcallsState{open: true, deps: (&m).surfaceDeps()}
	s.setEntries(m.toolcallEntries(), true)
	m.modal = s
	return m, nil
}

type toolcallEntry struct {
	blockID  scrollback.BlockID
	callID   string
	name     string
	intent   string
	result   scrollback.ToolResult
	resolved bool
	failed   bool
}

type toolcallsState struct {
	open     bool
	deps     surfaceDeps
	entries  []toolcallEntry
	selected int
	detail   bool
	window   *bounded.Viewport
	width    int
	lines    int
	follow   bool
	list     *bounded.List
	compact  bool
}

func (*toolcallsState) modalPlacement() modalPlacement          { return modalPlacementFill }
func (*toolcallsState) Close()                                  {}
func (*toolcallsState) HandleMsg(tea.Msg) (tea.Cmd, bool, bool) { return nil, false, false }

func (m Model) toolcallEntries() []toolcallEntry {
	entries := make([]toolcallEntry, 0)
	for i := 0; i < m.conv.scrollback.Len(); i++ {
		snapshot := m.conv.scrollback.SnapshotAt(i)
		var entry toolcallEntry
		switch card := snapshot.Payload.(type) {
		case scrollback.ToolCardSnapshot:
			entry = toolcallEntry{
				callID: card.Call.ID, name: card.Call.Name, intent: card.Call.Arguments, result: card.Result,
				resolved: card.Resolved || card.Finished, failed: card.Result.IsError || card.Failed,
			}
		case scrollback.SubagentCardSnapshot:
			entry = toolcallEntry{
				callID: card.Call.ID, name: card.Call.Name, intent: card.Call.Arguments, result: card.Result,
				resolved: card.Resolved, failed: card.Result.IsError,
			}
		case scrollback.TeamCardSnapshot:
			entry = toolcallEntry{
				callID: card.Call.ID, name: card.Call.Name, intent: card.Call.Arguments, result: card.Result,
				resolved: card.Resolved, failed: card.Result.IsError,
			}
		default:
			continue
		}
		entry.blockID = snapshot.ID
		entries = append(entries, entry)
	}
	return entries
}

// setEntries preserves an earlier reader's block identity while new calls arrive.
// A reader already following the newest row advances to the new newest row.
func (s *toolcallsState) setEntries(entries []toolcallEntry, opening bool) {
	var selected scrollback.BlockID
	following := !s.detail && (opening || (len(s.entries) > 0 && s.selected == len(s.entries)-1))
	if !following && s.selected >= 0 && s.selected < len(s.entries) {
		selected = s.entries[s.selected].blockID
	}
	s.entries = entries
	if len(entries) == 0 {
		s.selected = 0
		return
	}
	if following {
		s.selected = len(entries) - 1
		return
	}
	for i := range entries {
		if entries[i].blockID == selected {
			s.selected = i
			return
		}
	}
	s.selected = min(s.selected, len(entries)-1)
}

func (m *Model) syncToolcalls() {
	if s, ok := m.modal.(*toolcallsState); ok {
		s.setEntries(m.toolcallEntries(), false)
	}
}

func (s *toolcallsState) Render(width, height int) (string, []ClickableRegion) {
	s.compact = false
	if !s.open || width <= 0 || height <= 0 {
		s.list = nil
		return "", nil
	}
	th := s.deps.theme
	line := func(style lipgloss.Style, text string) string {
		return ansi.Cut(style.Render(terminaltext.Sanitize(text)), 0, width) + "\x1b[0m"
	}
	if height < 5 || width < 12 {
		s.compact, s.list = true, nil
		return line(th.Style("muted"), "too small · "+s.deps.marks.closeOnly+" close"), nil
	}
	title := line(th.Style("askTitle"), "Tool calls")
	if s.detail {
		return s.renderDetail(width, height, title, line), nil
	}
	footer := line(th.Style("muted"), s.deps.marks.navUp+"/"+s.deps.marks.navDown+" · "+s.deps.marks.scroll+" · "+s.deps.marks.choose+" detail · "+s.deps.marks.closeOnly+" close")
	if len(s.entries) == 0 {
		return strings.Join([]string{title, "", line(th.Style("muted"), "no tool calls in this session."), "", footer}, "\n"), nil
	}
	bodyHeight := height - 4
	if bodyHeight < 1 {
		s.compact, s.list = true, nil
		return line(th.Style("muted"), "too small · "+s.deps.marks.closeOnly+" close"), nil
	}
	if s.list == nil {
		s.list = new(bounded.List)
	}
	items := make([]bounded.ListItem, len(s.entries))
	for i, entry := range s.entries {
		status := "running"
		if entry.resolved {
			status = "done"
		}
		if entry.failed {
			status = "failed"
		}
		intent := strings.ReplaceAll(terminaltext.Sanitize(entry.intent), "\n", " ")
		items[i] = bounded.ListItem{ID: fmt.Sprintf("%d", entry.blockID), Text: status + " · " + terminaltext.Sanitize(entry.name) + " · " + intent}
	}
	s.list.SetGeometry(width, bodyHeight, 0, bounded.Clip)
	s.list.SetItems(items)
	s.list.SetCursor(s.selected)
	view := s.list.View()
	body := []string{title, ""}
	for _, row := range view.Rows {
		prefix := "  "
		if row.Selected {
			prefix = "> "
		}
		body = append(body, ansi.Cut(prefix+row.Text, 0, width)+"\x1b[0m")
	}
	body = append(body, "", footer)
	return strings.Join(body, "\n"), nil
}

func (s *toolcallsState) renderDetail(width, height int, title string, line func(lipgloss.Style, string) string) string {
	entry := s.entries[s.selected]
	if s.window == nil {
		s.window = new(bounded.Viewport)
		s.follow = true
	}
	header := []string{title, ""}
	footer := line(s.deps.theme.Style("muted"), s.deps.marks.navUp+"/"+s.deps.marks.navDown+" · "+s.deps.marks.scroll+" · "+s.deps.marks.jumpTopFull+"/"+s.deps.marks.jumpEndFull+" · "+s.deps.marks.closeOnly+" back")
	content := toolcallDetailLines(entry)
	if !s.follow && s.width > 0 && s.width != width {
		oldRows := toolcallRowCounts(content, s.width)
		newRows := toolcallRowCounts(content, width)
		oldStart, newStart := 0, 0
		for i, count := range oldRows {
			if s.window.Offset() < oldStart+count {
				s.window.SetGeometry(width, height-len(header)-1, 0, bounded.Wrap)
				s.window.Move(bounded.End, newStart+min(s.window.Offset()-oldStart, newRows[i]-1)+s.window.Height())
				break
			}
			oldStart += count
			newStart += newRows[i]
		}
	}
	s.width = width
	s.window.SetGeometry(width, height-len(header)-1, 0, bounded.Wrap)
	view := s.window.View(content)
	s.lines = view.Above + len(view.Rows) + view.Below
	if s.lines <= s.window.Height() {
		s.follow = true
	}
	if s.follow {
		s.window.Move(bounded.End, s.lines)
		view = s.window.View(content)
	}
	for _, row := range view.Rows {
		header = append(header, row)
	}
	for len(header) < height-1 {
		header = append(header, "")
	}
	return strings.Join(append(header, footer), "\n")
}

// toolcallRowCounts uses the same bounded wrapping policy as the detail window
// to remap its top logical line when a terminal resize reflows earlier text.
func toolcallRowCounts(lines []string, width int) []int {
	probe := new(bounded.Viewport)
	probe.SetGeometry(width, 1, 0, bounded.Wrap)
	var counts []int
	for _, line := range lines {
		for _, source := range strings.Split(line, "\n") {
			view := probe.View([]string{source})
			counts = append(counts, view.Above+len(view.Rows)+view.Below)
		}
	}
	return counts
}

func toolcallDetailLines(entry toolcallEntry) []string {
	status := "running"
	if entry.resolved {
		status = "done"
	}
	if entry.failed {
		status = "failed"
	}
	lines := []string{terminaltext.Sanitize(entry.name) + " · " + status, "Call: " + terminaltext.Sanitize(entry.callID), "Arguments:", terminaltext.Sanitize(entry.intent)}
	if !entry.resolved {
		return append(lines, "", "Result: pending")
	}
	result := entry.result
	lines = append(lines, "", "Result:")
	if result.Body != "" {
		lines = append(lines, terminaltext.Sanitize(result.Body))
	}
	structured := result.StructuredContent
	for _, a := range result.Artifacts {
		switch client.ContentBlockKind(a.Kind) {
		case client.ContentBlockText:
			if a.Text != "" && a.Text != result.Body {
				lines = append(lines, "Text:", terminaltext.Sanitize(a.Text))
			}
		case client.ContentBlockStructuredContent:
			// The typed block is canonical when a field mirror is also present.
			structured = a.Text
		case client.ContentBlockResourceLink:
			lines = append(lines, "Resource: "+terminaltext.Sanitize(a.Name)+" · "+terminaltext.Sanitize(a.URL))
		case client.ContentBlockEmbeddedResource:
			if len(a.Data) > 0 {
				lines = append(lines, "Embedded resource ("+terminaltext.Sanitize(a.MIMEType)+", binary content)")
			} else {
				lines = append(lines, "Embedded resource:", terminaltext.Sanitize(a.Text))
			}
		case client.ContentBlockImage, client.ContentBlockAudio:
			lines = append(lines, string(a.Kind)+" ("+terminaltext.Sanitize(a.MIMEType)+", media content)")
		}
	}
	if structured != "" {
		lines = append(lines, "Structured JSON:", terminaltext.Sanitize(structured))
	}
	return lines
}

func (s *toolcallsState) HandleKey(msg tea.KeyPressMsg) (tea.Cmd, bool, bool) {
	if key.Matches(msg, s.deps.keys.Close) {
		if s.detail {
			s.detail = false
			return nil, true, false
		}
		return nil, true, true
	}
	if s.compact || len(s.entries) == 0 {
		return nil, true, false
	}
	move := bounded.LineDown
	switch {
	case key.Matches(msg, s.deps.keys.Down):
	case key.Matches(msg, s.deps.keys.Up):
		move = bounded.LineUp
	case key.Matches(msg, s.deps.keys.ScrollD):
		move = bounded.PageDown
	case key.Matches(msg, s.deps.keys.ScrollU):
		move = bounded.PageUp
	case key.Matches(msg, s.deps.keys.ScrollBottom):
		move = bounded.End
	case key.Matches(msg, s.deps.keys.ScrollTop):
		move = bounded.Top
	case key.Matches(msg, s.deps.keys.Choose):
		if !s.detail {
			s.detail = true
			s.window = new(bounded.Viewport)
			s.follow = false
		}
		return nil, true, false
	default:
		return nil, true, false
	}
	if s.detail {
		if s.window != nil {
			s.window.Move(move, s.lines)
			s.follow = s.window.Offset() >= max(0, s.lines-s.window.Height())
		}
		return nil, true, false
	}
	if s.list != nil {
		s.list.Move(move)
		s.selected = s.list.Cursor()
	}
	return nil, true, false
}

func (s *toolcallsState) HandleWheel(msg tea.MouseWheelMsg) (tea.Cmd, bool) {
	if s.detail && s.window != nil {
		move := bounded.LineDown
		if msg.Mouse().Button == tea.MouseWheelUp {
			move = bounded.LineUp
		}
		s.window.Move(move, s.lines)
		s.follow = s.window.Offset() >= max(0, s.lines-s.window.Height())
		return nil, true
	}
	if s.list == nil {
		return nil, true
	}
	s.list.Move(map[bool]bounded.Move{true: bounded.LineUp, false: bounded.LineDown}[msg.Mouse().Button == tea.MouseWheelUp])
	s.selected = s.list.Cursor()
	return nil, true
}
