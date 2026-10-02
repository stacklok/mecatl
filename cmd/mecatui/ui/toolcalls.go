package ui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

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
	name     string
	intent   string
	resolved bool
	failed   bool
}

type toolcallsState struct {
	open     bool
	deps     surfaceDeps
	entries  []toolcallEntry
	selected int
	detail   bool
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
				name: card.Call.Name, intent: card.Call.Arguments,
				resolved: card.Resolved || card.Finished, failed: card.Result.IsError || card.Failed,
			}
		case scrollback.SubagentCardSnapshot:
			entry = toolcallEntry{
				name: card.Call.Name, intent: card.Call.Arguments,
				resolved: card.Resolved, failed: card.Result.IsError,
			}
		case scrollback.TeamCardSnapshot:
			entry = toolcallEntry{
				name: card.Call.Name, intent: card.Call.Arguments,
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
	following := opening || (len(s.entries) > 0 && s.selected == len(s.entries)-1)
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
		entry := s.entries[s.selected]
		return strings.Join([]string{title, "", line(th.Style("muted"), "detail placeholder for "+entry.name), "", line(th.Style("muted"), s.deps.marks.closeOnly+" back")}, "\n"), nil
	}
	footer := line(th.Style("muted"), s.deps.marks.scroll+" navigate · enter detail · "+s.deps.marks.closeOnly+" close")
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
		s.detail = true
		return nil, true, false
	default:
		return nil, true, false
	}
	if s.list != nil {
		s.list.Move(move)
		s.selected = s.list.Cursor()
	}
	return nil, true, false
}

func (s *toolcallsState) HandleWheel(msg tea.MouseWheelMsg) (tea.Cmd, bool) {
	if s.list == nil {
		return nil, true
	}
	s.list.Move(map[bool]bounded.Move{true: bounded.LineUp, false: bounded.LineDown}[msg.Mouse().Button == tea.MouseWheelUp])
	s.selected = s.list.Cursor()
	return nil, true
}
