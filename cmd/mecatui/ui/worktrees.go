package ui

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/internal/terminaltext"
	"github.com/stacklok/mecatl/cmd/mecatui/ui/internal/bounded"
)

// worktreesState owns one picker lifetime. Only Model can bind a successor session.
type worktreesState struct {
	deps       surfaceDeps
	lister     client.WorktreeLister
	sourceID   string
	cancel     context.CancelFunc
	request    uint64
	loading    bool
	err        error
	worktrees  []client.Worktree
	filtered   []client.Worktree
	filter     textinput.Model
	list       bounded.List
	identities map[client.WorktreeSelector]string
	confirm    *client.Worktree
	intent     surfaceIntent
	compact    bool   // view cache: refreshed from every Render offer
	offer      [2]int // view cache: last geometry, for reveal on resize
}

type worktreesResultMsg struct {
	owner   *worktreesState
	request uint64
	result  client.WorktreesMsg
}

type worktreeSelectIntent struct {
	sourceID string
	worktree client.Worktree
}

func (worktreeSelectIntent) isSurfaceIntent() {}

func (s *worktreesState) takeSurfaceIntent() surfaceIntent {
	intent := s.intent
	s.intent = nil
	return intent
}

func (m Model) openWorktrees() (tea.Model, tea.Cmd) {
	if m.phase != phaseIdle || m.deps.Worktrees == nil {
		return m, nil
	}
	ti := textinput.New()
	ti.Placeholder = "filter worktrees…"
	ti.Focus()
	s := &worktreesState{deps: (&m).surfaceDeps(), lister: m.deps.Worktrees, sourceID: m.sessionID, filter: ti, compact: true, identities: make(map[client.WorktreeSelector]string)}
	m.modal = s
	m.prompt.Blur()
	return m, tea.Batch(s.listCmd(), textinput.Blink)
}

func (s *worktreesState) listCmd() tea.Cmd {
	if s.cancel != nil {
		s.cancel()
	}
	ctx, cancel := context.WithCancel(s.deps.ctx)
	s.cancel = cancel
	s.request++
	s.loading, s.err = true, nil
	request, lister, sourceID := s.request, s.lister, s.sourceID
	return func() tea.Msg {
		worktrees, err := lister.List(ctx, sourceID)
		return worktreesResultMsg{owner: s, request: request, result: client.WorktreesMsg{Worktrees: worktrees, Err: err}}
	}
}

func (s *worktreesState) Close() {
	if s.cancel != nil {
		s.cancel()
	}
	s.request++
	s.intent = nil
	s.filter.Blur()
}

func (s *worktreesState) HandleMsg(msg tea.Msg) (tea.Cmd, bool, bool) {
	if result, ok := msg.(worktreesResultMsg); ok {
		if result.owner != s || result.request != s.request || !s.loading {
			return nil, true, false
		}
		s.loading, s.err = false, result.result.Err
		if s.err == nil {
			s.worktrees = result.result.Worktrees
			s.syncFilter()
		}
		return nil, true, false
	}
	var cmd tea.Cmd
	s.filter, cmd = s.filter.Update(msg)
	return cmd, cmd != nil, false
}

func (s *worktreesState) syncFilter() {
	s.filtered = filterWorktrees(s.worktrees, s.filter.Value())
	items := make([]bounded.ListItem, len(s.filtered))
	for i, wt := range s.filtered {
		id, ok := s.identities[wt.Selector]
		if !ok {
			id = strconv.Itoa(len(s.identities) + 1)
			s.identities[wt.Selector] = id
		}
		items[i] = bounded.ListItem{ID: id, Text: worktreeRowText(wt)}
	}
	s.list.SetItems(items)
}

func (s *worktreesState) HandleKey(msg tea.KeyPressMsg) (tea.Cmd, bool, bool) {
	if s.compact {
		return nil, true, key.Matches(msg, s.deps.keys.Close)
	}
	if s.confirm != nil {
		switch {
		case key.Matches(msg, s.deps.keys.Close):
			s.confirm = nil
		case key.Matches(msg, s.deps.keys.Choose):
			if s.sourceID == "" || s.confirm.Selector.IsZero() {
				s.confirm = nil
				s.err = errors.New("worktree choice is stale; relist and try again")
			} else {
				s.intent = worktreeSelectIntent{sourceID: s.sourceID, worktree: *s.confirm}
			}
		}
		return nil, true, false
	}
	move, navigation := boundedFilterNavigationMove(msg, s.deps.keys, bounded.LineUp, bounded.LineDown, bounded.Top, bounded.End)
	switch {
	case key.Matches(msg, s.deps.keys.Close):
		if s.filter.Value() == "" {
			return nil, true, true
		}
		s.filter.SetValue("")
		s.syncFilter()
	case navigation:
		s.list.Move(move)
	case key.Matches(msg, s.deps.keys.Choose):
		if !s.loading && s.err == nil && len(s.filtered) > 0 {
			wt := s.filtered[s.list.Cursor()]
			s.confirm = &wt
		}
	default:
		var cmd tea.Cmd
		s.filter, cmd = s.filter.Update(msg)
		s.syncFilter()
		return cmd, true, false
	}
	return nil, true, false
}

func (s *worktreesState) HandleWheel(msg tea.MouseWheelMsg) (tea.Cmd, bool) {
	if !s.compact && s.confirm == nil && !s.loading && s.err == nil {
		if move, ok := boundedWheelMove(msg); ok {
			s.list.Scroll(move)
		}
	}
	return nil, true
}

func (*worktreesState) modalMaxOuterWidth() int { return 128 }
func (s *worktreesState) modalFrame() bool      { return !s.compact }

func (s *worktreesState) Render(width, height int) (string, []ClickableRegion) {
	offer := [2]int{width, height}
	reveal := offer != s.offer || s.list.RevealPending()
	s.offer = offer
	s.compact = true
	if width <= 0 || height <= 0 {
		s.list.SetGeometry(0, 0, 1, bounded.Wrap)
		return "", nil
	}
	if s.confirm != nil {
		return s.renderConfirm(width, height), nil
	}
	th, hk := s.deps.theme, s.deps.marks
	title := skillsTextLines(th.Style("askTitle"), "worktrees", width)
	prefix := append(title, skillsTextLines(th.Style("muted"), "select a worktree to start a new session there", width)...)
	s.filter.SetWidth(max(1, width-2))
	prefix = append(prefix, ansi.Cut(s.filter.View(), 0, width))
	footerText := hk.closeOnly + ": close"
	if !s.loading && s.err == nil && len(s.filtered) > 0 && s.deps.keys.Choose.Enabled() {
		footerText = hk.choose + ": select  " + footerText
	}
	footer := skillsTextLines(th.Style("muted"), footerText, width)
	rows := height - len(prefix) - len(footer)
	s.list.SetGeometry(width, rows, 1, bounded.Wrap)
	if !s.list.Valid() {
		return renderSkillsCompact(th, hk, width), nil
	}
	state := s.stateLines(width)
	if len(state) > rows {
		return renderSkillsCompact(th, hk, width), nil
	}
	s.compact = false
	lines := prefix
	if state != nil {
		lines = append(lines, state...)
	} else {
		view := s.list.ViewWithIndicators(rows, reveal)
		if rows == 1 && (view.Above > 0 || view.Below > 0) {
			lines[0] = ansi.Cut(th.Style("askTitle").Render(fmt.Sprintf("↑ %d ↓ %d more · worktrees", view.Above, view.Below)), 0, width)
		} else if view.Above > 0 {
			lines = append(lines, ansi.Cut(th.Style("muted").Render(fmt.Sprintf("↑ %d more", view.Above)), 0, width))
		}
		for _, row := range view.Rows {
			p := presentListRow(row, th.Style("spinner"), th.Style("muted"))
			lines = append(lines, ansi.Cut(p.Style.Render(p.Text), 0, width))
		}
		if rows > 1 && view.Below > 0 {
			lines = append(lines, ansi.Cut(th.Style("muted").Render(fmt.Sprintf("↓ %d more", view.Below)), 0, width))
		}
	}
	lines = append(lines, footer...)
	return strings.Join(lines, "\n"), nil
}

func (s *worktreesState) renderConfirm(width, height int) string {
	th, hk := s.deps.theme, s.deps.marks
	lines := skillsTextLines(th.Style("askTitle"), "start session in worktree", width)
	lines = append(lines, skillsTextLines(th.Style("muted"), "a new session will start in:", width)...)
	lines = append(lines, skillsTextLines(th.Style("spinner"), terminaltext.SanitizeSingleLine(s.confirm.Label), width)...)
	if s.confirm.Branch != "" && s.confirm.Branch != s.confirm.Label {
		lines = append(lines, skillsTextLines(th.Style("muted"), "branch: "+terminaltext.SanitizeSingleLine(s.confirm.Branch), width)...)
	}
	footer := hk.closeOnly + ": back"
	if s.deps.keys.Choose.Enabled() {
		footer = hk.choose + ": start session  " + footer
	}
	lines = append(lines, skillsTextLines(th.Style("muted"), footer, width)...)
	if len(lines) > height {
		return renderSkillsCompact(th, hk, width)
	}
	s.compact = false
	return strings.Join(lines, "\n")
}

func (s *worktreesState) stateLines(width int) []string {
	th := s.deps.theme
	switch {
	case s.loading:
		return skillsTextLines(th.Style("muted"), "loading…", width)
	case s.err != nil:
		return skillsTextLines(th.Style("errorText"), "could not list worktrees: "+terminaltext.Sanitize(s.err.Error()), width)
	case len(s.filtered) == 0:
		text := "no worktrees found"
		if s.filter.Value() != "" {
			text = "no matches — clear filter to see all"
		}
		return skillsTextLines(th.Style("muted"), text, width)
	default:
		return nil
	}
}

func worktreeRowText(w client.Worktree) string {
	text := terminaltext.SanitizeSingleLine(w.Label)
	if w.Branch != "" && w.Branch != w.Label {
		text += "  (" + terminaltext.SanitizeSingleLine(w.Branch) + ")"
	} else if w.Revision != "" {
		text += "  (" + shortSHA(terminaltext.SanitizeSingleLine(w.Revision)) + ")"
	}
	return text
}

// switchToWorktree starts a create-first empty-history successor handoff using
// only the opaque selector issued for the current session. The old session and
// transcript stay selected until the server has created and returned the target.
func (m Model) switchToWorktree(wt client.Worktree) (tea.Model, tea.Cmd) {
	if m.sessionID == "" || wt.Selector.IsZero() {
		m.statusMsg = "worktree choice is stale; relist and try again"
		return m, nil
	}
	oldID := m.sessionID
	m.closeModal()
	m.phase = phaseConnecting
	m.statusMsg = "starting a new session in the selected worktree…"
	m.refreshView()
	return m, m.switchWorktreeCmd(oldID, wt)
}

func (m Model) switchWorktreeCmd(oldID string, wt client.Worktree) tea.Cmd {
	deps := m.deps
	return func() tea.Msg {
		selector := wt.Selector
		id, snapshot, err := deps.Session.ClearSession(deps.Ctx, oldID, &selector)
		if err != nil {
			return worktreeSwitchFailedMsg{sourceID: oldID, err: err}
		}
		return worktreeSwitchReadyMsg{
			ready:     client.SessionReadyMsg{SessionID: id, Capabilities: snapshot.Capabilities, ResolvedModel: snapshot.ResolvedModel, Mode: snapshot.Mode},
			placement: snapshot.Placement,
			oldID:     oldID,
		}
	}
}

type worktreeSwitchReadyMsg struct {
	ready     client.SessionReadyMsg
	placement client.Placement
	oldID     string
}

type worktreeSwitchFailedMsg struct {
	sourceID string
	err      error
}

func filterWorktrees(wts []client.Worktree, q string) []client.Worktree {
	if q == "" {
		return wts
	}
	needle := strings.ToLower(q)
	out := make([]client.Worktree, 0, len(wts))
	for _, w := range wts {
		if strings.Contains(strings.ToLower(terminaltext.SanitizeSingleLine(w.Label)), needle) ||
			strings.Contains(strings.ToLower(terminaltext.SanitizeSingleLine(w.Branch)), needle) ||
			strings.Contains(strings.ToLower(terminaltext.SanitizeSingleLine(w.Revision)), needle) {
			out = append(out, w)
		}
	}
	return out
}

func shortSHA(sha string) string {
	return string([]rune(sha)[:min(8, len([]rune(sha)))])
}
