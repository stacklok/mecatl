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
	hitTargets map[HitID]worktreeHit
}

type worktreeAction uint8

const (
	worktreeRow worktreeAction = iota
	worktreeStart
	worktreeBack
)

type worktreeHit struct {
	action             worktreeAction
	id, source, filter string
	selected           client.WorktreeSelector
	confirmation       bool
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
	s.hitTargets = nil
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
	s.intent, s.hitTargets = nil, nil
	s.filter.Blur()
}

func (s *worktreesState) HandleMsg(msg tea.Msg) (tea.Cmd, bool, bool) {
	if hit, ok := msg.(surfaceHitMsg); ok {
		target, current := s.hitTargets[hit.ID]
		s.hitTargets = nil
		if !current || s.compact || target.source != s.sourceID || target.filter != s.filter.Value() || target.confirmation != (s.confirm != nil) || target.selected != s.selectedSelector() {
			return nil, true, false
		}
		if target.action == worktreeRow {
			if !s.loading && s.err == nil && s.confirm == nil {
				for i, wt := range s.filtered {
					if s.identities[wt.Selector] == target.id {
						s.list.SetCursor(i)
						break
					}
				}
			}
			return nil, true, false
		}
		return nil, true, s.act(target.action)
	}
	if result, ok := msg.(worktreesResultMsg); ok {
		if result.owner != s || result.request != s.request || !s.loading {
			return nil, true, false
		}
		s.hitTargets = nil
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
	s.hitTargets = nil
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

func (s *worktreesState) selectedSelector() client.WorktreeSelector {
	if s.confirm != nil {
		return s.confirm.Selector
	}
	if i := s.list.Cursor(); i >= 0 && i < len(s.filtered) {
		return s.filtered[i].Selector
	}
	return client.WorktreeSelector{}
}

func (s *worktreesState) canStart() bool {
	return s.deps.keys.Choose.Enabled() && (s.confirm != nil || (!s.loading && s.err == nil && len(s.filtered) > 0))
}

// act is shared by keyboard and pointer; Model still owns the actual handoff.
func (s *worktreesState) act(action worktreeAction) bool {
	if action == worktreeBack && s.deps.keys.Close.Enabled() {
		if s.compact {
			return true
		}
		if s.confirm != nil {
			s.confirm = nil
		} else if s.filter.Value() != "" {
			s.filter.SetValue("")
			s.syncFilter()
		} else {
			return true
		}
	} else if action == worktreeStart && !s.compact && s.canStart() && s.intent == nil {
		if s.confirm == nil {
			wt := s.filtered[s.list.Cursor()]
			s.confirm = &wt
		} else if s.sourceID == "" || s.confirm.Selector.IsZero() {
			s.confirm = nil
			s.err = errors.New("worktree choice is stale; relist and try again")
		} else {
			s.intent = worktreeSelectIntent{sourceID: s.sourceID, worktree: *s.confirm}
		}
	}
	return false
}

func (s *worktreesState) HandleKey(msg tea.KeyPressMsg) (tea.Cmd, bool, bool) {
	s.hitTargets = nil
	if key.Matches(msg, s.deps.keys.Close) {
		return nil, true, s.act(worktreeBack)
	}
	if s.compact {
		return nil, true, false
	}
	if s.confirm != nil {
		if key.Matches(msg, s.deps.keys.Choose) {
			s.act(worktreeStart)
		}
		return nil, true, false
	}
	move, navigation := boundedFilterNavigationMove(msg, s.deps.keys, bounded.LineUp, bounded.LineDown, bounded.Top, bounded.End)
	switch {
	case navigation:
		s.list.Move(move)
	case key.Matches(msg, s.deps.keys.Choose):
		s.act(worktreeStart)
	default:
		var cmd tea.Cmd
		s.filter, cmd = s.filter.Update(msg)
		s.syncFilter()
		return cmd, true, false
	}
	return nil, true, false
}

func (s *worktreesState) HandleWheel(msg tea.MouseWheelMsg) (tea.Cmd, bool) {
	s.hitTargets = nil
	if !s.compact && s.confirm == nil && !s.loading && s.err == nil {
		if move, ok := boundedWheelMove(msg); ok {
			s.list.Scroll(move)
		}
	}
	return nil, true
}

func (*worktreesState) modalMaxOuterWidth() int { return 128 }
func (s *worktreesState) modalFrame() bool      { return !s.compact }

type worktreeControl struct {
	rect   cellRect
	action worktreeAction
}

func (s *worktreesState) footerControls(width int) ([]string, []worktreeControl) {
	back := footerCloseLabel
	if s.confirm != nil {
		back = "Back"
	} else if s.filter.Value() != "" {
		back = "Clear filter"
	}
	lines, rendered := renderFooterControls(s.deps.theme, width, []footerControlSpec{
		{hint: s.deps.marks.choose, label: "Start session", enabled: s.canStart()},
		{hint: s.deps.marks.closeOnly, label: back, enabled: s.deps.keys.Close.Enabled()},
	})
	controls := make([]worktreeControl, len(rendered))
	for i, control := range rendered {
		controls[i] = worktreeControl{rect: control.rect, action: [...]worktreeAction{worktreeStart, worktreeBack}[control.index]}
	}
	return lines, controls
}

func (s *worktreesState) addHit(regions *[]ClickableRegion, rect cellRect, action worktreeAction, id string) {
	if s.deps.hits == nil {
		return
	}
	hit := s.deps.hits.allocate()
	s.hitTargets[hit] = worktreeHit{action: action, id: id, selected: s.selectedSelector(), source: s.sourceID, filter: s.filter.Value(), confirmation: s.confirm != nil}
	*regions = append(*regions, ClickableRegion{rect: rect, hit: hit})
}

func (s *worktreesState) addControls(regions *[]ClickableRegion, controls []worktreeControl, y int) {
	for _, control := range controls {
		rect := control.rect
		rect.y0 += y
		rect.y1 += y
		s.addHit(regions, rect, control.action, "")
	}
}

func (s *worktreesState) renderCompact(width int) string {
	if !s.deps.keys.Close.Enabled() {
		return ansi.Cut(s.deps.theme.Style("muted").Render("terminal too small"), 0, width)
	}
	return renderSkillsCompact(s.deps.theme, s.deps.marks, width)
}

func (s *worktreesState) Render(width, height int) (string, []ClickableRegion) {
	offer := [2]int{width, height}
	reveal := offer != s.offer || s.list.RevealPending()
	s.offer = offer
	s.compact = true
	s.hitTargets = make(map[HitID]worktreeHit)
	if width <= 0 || height <= 0 {
		s.list.SetGeometry(0, 0, 1, bounded.Wrap)
		return "", nil
	}
	if s.confirm != nil {
		return s.renderConfirm(width, height)
	}
	th := s.deps.theme
	title := skillsTextLines(th.Style("askTitle"), "worktrees", width)
	prefix := append(title, skillsTextLines(th.Style("muted"), "select a worktree to start a new session there", width)...)
	s.filter.SetWidth(max(1, width-2))
	prefix = append(prefix, ansi.Cut(s.filter.View(), 0, width))
	footer, controls := s.footerControls(width)
	if (s.canStart() || s.deps.keys.Close.Enabled()) && len(controls) == 0 {
		return s.renderCompact(width), nil
	}
	// Filter navigation always accepts arrows, but not page keys or printable aliases.
	navigation := "↑/↓ move"
	if s.deps.keys.ScrollTop.Enabled() {
		navigation += " · " + terminaltext.SanitizeSingleLine(firstKey(s.deps.keys.ScrollTop, "")) + " first"
	}
	if s.deps.keys.ScrollBottom.Enabled() {
		navigation += " · " + terminaltext.SanitizeSingleLine(firstKey(s.deps.keys.ScrollBottom, "")) + " last"
	}
	help := skillsTextLines(th.Style("muted"), navigation, width)
	if height-len(prefix)-len(footer)-len(help) >= 3 {
		footer = append(footer, help...)
	}
	rows := height - len(prefix) - len(footer)
	spacing := rows >= 3
	if spacing {
		rows -= 2
	}
	s.list.SetGeometry(width, rows, 1, bounded.Wrap)
	if !s.list.Valid() {
		return s.renderCompact(width), nil
	}
	state := s.stateLines(width)
	if len(state) > rows {
		return s.renderCompact(width), nil
	}
	s.compact = false
	return s.renderRows(prefix, state, footer, controls, width, rows, spacing, reveal)
}

func (s *worktreesState) renderRows(prefix, state, footer []string, controls []worktreeControl, width, rows int, spacing bool, reveal bool) (string, []ClickableRegion) {
	th := s.deps.theme
	var regions []ClickableRegion
	lines := prefix
	if spacing {
		lines = append(lines, "")
	}
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
			text := ansi.Cut(p.Style.Render(p.Text), 0, width)
			s.addHit(&regions, cellRect{x0: 0, x1: ansi.StringWidth(text), y0: len(lines), y1: len(lines) + 1}, worktreeRow, row.ID)
			lines = append(lines, text)
		}
		if rows > 1 && view.Below > 0 {
			lines = append(lines, ansi.Cut(th.Style("muted").Render(fmt.Sprintf("↓ %d more", view.Below)), 0, width))
		}
	}
	if spacing {
		lines = append(lines, "")
	}
	s.addControls(&regions, controls, len(lines))
	lines = append(lines, footer...)
	return strings.Join(lines, "\n"), regions
}

func (s *worktreesState) renderConfirm(width, height int) (string, []ClickableRegion) {
	th := s.deps.theme
	lines := skillsTextLines(th.Style("askTitle"), "start session in worktree", width)
	lines = append(lines, skillsTextLines(th.Style("muted"), "a new session will start in:", width)...)
	lines = append(lines, skillsTextLines(th.Style("spinner"), terminaltext.SanitizeSingleLine(s.confirm.Label), width)...)
	if s.confirm.Branch != "" && s.confirm.Branch != s.confirm.Label {
		lines = append(lines, skillsTextLines(th.Style("muted"), "branch: "+terminaltext.SanitizeSingleLine(s.confirm.Branch), width)...)
	}
	footer, controls := s.footerControls(width)
	if len(lines)+len(footer) > height || ((s.canStart() || s.deps.keys.Close.Enabled()) && len(controls) == 0) {
		return s.renderCompact(width), nil
	}
	var regions []ClickableRegion
	s.addControls(&regions, controls, len(lines))
	lines = append(lines, footer...)
	s.compact = false
	return strings.Join(lines, "\n"), regions
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
