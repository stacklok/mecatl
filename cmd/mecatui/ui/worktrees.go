package ui

import (
	"errors"
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

// worktrees.go is the /worktrees overlay (issue #102) — the SECOND *selecting*
// overlay after /models. Unlike /models it has NO apply-on-next option: a
// workspace switch is ALWAYS a restart-now handoff (there is no "pendingNext
// workspace" concept — switching a live session's workspace is forbidden by
// design, so the only path is close-old → create-new-rooted-at-worktree). It
// mirrors the /models cursor+filter+Choose model, but the confirm step offers
// only restart-now / undo.

// worktreesView is the active /worktrees overlay (none = closed). Like /models it
// has a real cursor and an enter-to-confirm step; UNLIKE it there is no
// "switch-next" arm.
type worktreesView int

const (
	worktreesNone    worktreesView = iota // overlay closed
	worktreesPanel                        // the flat, type-to-filter picker
	worktreesConfirm                      // the post-Enter confirmation overlay (restart-now / undo)
)

// worktreesState holds the /worktrees overlay state on the Model. Value-embedded
// so the Model stays a plain struct Update copies; the slices are replaced
// wholesale on each RPC result / filter recompute (never mutated in place).
type worktreesState struct {
	view      worktreesView
	loading   bool              // the ListWorktrees RPC is in flight
	err       error             // last ListWorktrees error, rendered distinctly
	worktrees []client.Worktree // full list as relayed (path-sorted by git)
	filtered  []client.Worktree // subset matching filter.Value(); recomputed on each key
	filter    textinput.Model   // the type-to-filter input; focused while the picker is open
	cursor    int               // index into FILTERED (clamped to its bounds)
	confirm   client.Worktree   // the candidate worktree when view==worktreesConfirm
}

// openWorktrees opens the picker and fires the ListWorktrees RPC. Only callable
// while idle and when a worktree lister is wired; returns the model unchanged
// otherwise. The result arrives as a client.WorktreesMsg handled in
// updateWorktreesMsg.
func (m Model) openWorktrees() (tea.Model, tea.Cmd) {
	if m.phase != phaseIdle || m.deps.Worktrees == nil {
		return m, nil
	}
	m.prompt.Blur() // overlay owns the keyboard while open
	m.worktrees.view = worktreesPanel
	m.worktrees.loading = true
	m.worktrees.err = nil
	m.worktrees.cursor = 0
	ti := textinput.New()
	ti.Placeholder = "filter worktrees…"
	ti.SetWidth(40)
	ti.Focus()
	m.worktrees.filter = ti
	m.worktrees.filtered = nil
	return m, tea.Batch(client.ListWorktreesCmd(m.deps.Ctx, m.deps.Worktrees, m.sessionID), textinput.Blink)
}

// closeWorktrees dismisses the overlay and returns focus to the prompt input.
func (m Model) closeWorktrees() (tea.Model, tea.Cmd) {
	m.worktrees.view = worktreesNone
	m.worktrees.filter = textinput.Model{}
	m.worktrees.filtered = nil
	cmd := m.prompt.Focus()
	return m, cmd
}

// onWorktreesKey routes key presses while the picker is open. Mirrors onModelsKey:
// the filter input is FOCUSED, so nav/action keys are intercepted first and
// everything else feeds the input. esc is two-stage (clear filter, then close).
func (m Model) onWorktreesKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd, bool) {
	if m.worktrees.view == worktreesNone {
		return m, nil, false
	}
	if m.worktrees.view == worktreesConfirm {
		return m.onWorktreesConfirmKey(msg)
	}
	switch {
	case key.Matches(msg, m.keys.Close):
		if m.worktrees.filter.Value() != "" {
			m.worktrees.filter.SetValue("")
			m = m.syncWorktreesFilter()
			return m, nil, true
		}
		mm, cmd := m.closeWorktrees()
		return mm, cmd, true
	case msg.String() == "up":
		if m.worktrees.cursor > 0 {
			m.worktrees.cursor--
		}
		return m, nil, true
	case msg.String() == "down":
		if m.worktrees.cursor < len(m.worktrees.filtered)-1 {
			m.worktrees.cursor++
		}
		return m, nil, true
	case key.Matches(msg, m.keys.ScrollTop):
		m.worktrees.cursor = 0
		return m, nil, true
	case key.Matches(msg, m.keys.ScrollBottom):
		m.worktrees.cursor = clampModelsCursor(len(m.worktrees.filtered)-1, len(m.worktrees.filtered))
		return m, nil, true
	case key.Matches(msg, m.keys.Choose):
		return m.chooseWorktree(), nil, true
	}
	var cmd tea.Cmd
	m.worktrees.filter, cmd = m.worktrees.filter.Update(msg)
	m = m.syncWorktreesFilter()
	return m, cmd, true
}

// syncWorktreesFilter recomputes the filtered slice from the filter input and
// clamps the cursor.
func (m Model) syncWorktreesFilter() Model {
	m.worktrees.filtered = filterWorktrees(m.worktrees.worktrees, m.worktrees.filter.Value())
	if m.worktrees.cursor >= len(m.worktrees.filtered) {
		m.worktrees.cursor = 0
	}
	return m
}

// chooseWorktree handles Enter on the cursor row: opens the confirmation overlay
// (worktreesConfirm) offering restart-now / undo. A cursor past the list end (or
// an empty list) is a no-op.
func (m Model) chooseWorktree() Model {
	if m.worktrees.cursor < 0 || m.worktrees.cursor >= len(m.worktrees.filtered) {
		return m
	}
	m.worktrees.confirm = m.worktrees.filtered[m.worktrees.cursor]
	m.worktrees.view = worktreesConfirm
	return m
}

// onWorktreesConfirmKey routes keys while the confirmation overlay is open. Two
// choices:
//   - enter — RESTART NOW: close the old session and create a fresh one rooted at
//     the chosen worktree (the only path — there is no apply-on-next for workspace).
//   - esc — UNDO: return to the picker panel without switching.
//
// Any other key is swallowed.
func (m Model) onWorktreesConfirmKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd, bool) {
	chosen := m.worktrees.confirm
	switch {
	case key.Matches(msg, m.keys.Choose): // enter — restart now
		return m.switchToWorktree(chosen)
	case key.Matches(msg, m.keys.Close): // esc — undo
		m.worktrees.view = worktreesPanel
		m.worktrees.confirm = client.Worktree{}
		return m, nil, true
	}
	return m, nil, true
}

// switchToWorktree starts a create-first empty-history successor handoff using
// only the opaque selector issued for the current session. The old session and
// transcript stay selected until the server has created and returned the target.
func (m Model) switchToWorktree(wt client.Worktree) (tea.Model, tea.Cmd, bool) {
	if m.sessionID == "" || wt.Selector.IsZero() {
		m.worktrees.view = worktreesPanel
		m.worktrees.err = errors.New("worktree choice is stale; relist and try again")
		return m, nil, true
	}
	oldID := m.sessionID
	m.phase = phaseConnecting
	m.statusMsg = "switching worktree — creating successor…"
	m.worktrees.view = worktreesNone
	m.refreshView()
	return m, m.switchWorktreeCmd(oldID, wt), true
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

// updateWorktreesMsg reduces a client.WorktreesMsg (the ListWorktrees RPC result):
// it stores the list, derives the filtered slice, clears loading, and keeps the
// overlay open. On error it records the error and clears loading (the panel
// renders an error line). Returns handled=false for any non-WorktreesMsg.
func (m Model) updateWorktreesMsg(msg tea.Msg) (tea.Model, bool) {
	wm, ok := msg.(client.WorktreesMsg)
	if !ok {
		return m, false
	}
	m.worktrees.loading = false
	if wm.Err != nil {
		m.worktrees.err = wm.Err
		m.worktrees.worktrees = nil
		m.worktrees.filtered = nil
		return m, true
	}
	m.worktrees.err = nil
	m.worktrees.worktrees = wm.Worktrees
	m = m.syncWorktreesFilter()
	return m, true
}

// filterWorktrees returns the worktrees whose Path/Branch/Head contains q
// (case-insensitive). An empty query returns the full list.
func filterWorktrees(wts []client.Worktree, q string) []client.Worktree {
	if q == "" {
		return wts
	}
	needle := strings.ToLower(q)
	out := make([]client.Worktree, 0, len(wts))
	for _, w := range wts {
		if strings.Contains(strings.ToLower(w.Label), needle) ||
			strings.Contains(strings.ToLower(w.Branch), needle) ||
			strings.Contains(strings.ToLower(w.Revision), needle) {
			out = append(out, w)
		}
	}
	return out
}

// renderWorktreesOverlay draws the picker (or its post-Enter confirmation
// overlay). Mirrors renderModelsOverlay's centred-card shape.
func renderWorktreesOverlay(th theme.Theme, st worktreesState, caps client.Capabilities, hk helpKeys, width, height int) string {
	switch st.view {
	case worktreesConfirm:
		return renderWorktreesConfirm(th, st, hk, width, height)
	default:
		return renderWorktreesPanel(th, st, caps, hk, width, height)
	}
}

// renderWorktreesPanel renders the worktree list card.
func renderWorktreesPanel(th theme.Theme, st worktreesState, _ client.Capabilities, hk helpKeys, width, _ int) string {
	var b strings.Builder
	b.WriteString(th.Style("title").Render("worktrees") + "\n")
	b.WriteString(th.Style("muted").Render("select a worktree to start a new session rooted there") + "\n\n")
	if st.loading {
		b.WriteString(th.Style("muted").Render("loading…"))
		return b.String()
	}
	if st.err != nil {
		b.WriteString(th.Style("errorText").Render("could not list worktrees: " + sanitizeTerminal(st.err.Error())))
		b.WriteString("\n" + th.Style("muted").Render(hk.closeOnly+": close"))
		return b.String()
	}
	if len(st.filtered) == 0 {
		if st.filter.Value() != "" {
			b.WriteString(th.Style("muted").Render("no matches — clear filter to see all"))
		} else {
			b.WriteString(th.Style("muted").Render("no worktrees found"))
		}
		b.WriteString("\n" + th.Style("muted").Render(hk.closeOnly+": close"))
		return b.String()
	}
	for i, w := range st.filtered {
		marker := "  "
		if i == st.cursor {
			marker = "▶ "
		}
		line := marker + sanitizeTerminal(w.Label)
		if w.Branch != "" {
			line += "  (" + sanitizeTerminal(w.Branch) + ")"
		} else if w.Revision != "" {
			line += "  (" + sanitizeTerminal(shortSHA(w.Revision)) + ")"
		}
		if i == st.cursor {
			line = renderToolCardText(th.Style("accent"), line, width)
		} else {
			line = renderToolCardText(th.Style("toolArgs"), line, width)
		}
		b.WriteString(line + "\n")
	}
	// The Choose/Close chords read the LIVE keyMap markings (issue #457); with
	// defaults the hint is byte-identical to the historical literal.
	b.WriteString("\n" + th.Style("muted").Render(hk.choose+": select  "+hk.closeOnly+": close"))
	return b.String()
}

// renderWorktreesConfirm renders the post-Enter confirmation card.
func renderWorktreesConfirm(th theme.Theme, st worktreesState, hk helpKeys, _, _ int) string {
	w := st.confirm
	var b strings.Builder
	b.WriteString(th.Style("title").Render("switch workspace") + "\n\n")
	b.WriteString("start a new session rooted at:\n")
	b.WriteString(th.Style("accent").Render("  "+sanitizeTerminal(w.Label)) + "\n")
	if w.Branch != "" {
		b.WriteString(th.Style("muted").Render("  branch: "+sanitizeTerminal(w.Branch)) + "\n")
	}
	b.WriteString("\n" + th.Style("muted").Render(hk.choose+": switch  "+hk.closeOnly+": back"))
	return b.String()
}

// shortSHA returns a shortened commit SHA (first 8 chars) for display.
func shortSHA(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}
