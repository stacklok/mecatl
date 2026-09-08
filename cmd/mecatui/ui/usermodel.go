package ui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

// userModelView is the active user-model inspection overlay (none = closed). Like
// /skills it is a read-only, idle-only inventory of short rows (key — description),
// dismissed with esc. UNLIKE soul it is a LIVE read (the server reads the store's
// current index), so it reflects facts the agent has saved since startup. The agent
// curates the user model; the panel only displays it, never edits it.
type userModelView int

const (
	userModelNone   userModelView = iota // overlay closed
	userModelPanel                       // read-only inventory (key + description)
	userModelDetail                      // selected exact fact + bounded history
)

// userModelState holds the user-model overlay state on the Model. Value-embedded so
// the Model stays a plain struct Update copies; the UserModel value is replaced
// wholesale on each RPC result.
type userModelState struct {
	view       userModelView
	loading    bool // the GetUserModel RPC is in flight
	err        error
	model      client.UserModel
	cursor     int
	detail     *client.UserModelDetail
	scroll     int
	requestKey string // exact key currently in flight; empty for inventory
}

// openUserModel opens the inventory panel and fires the GetUserModel RPC. Only
// callable while idle and when a user-model lister is wired; returns the model
// unchanged otherwise. The result arrives as a client.UserModelMsg handled in
// updateUserModelMsg.
func (m Model) openUserModel() (tea.Model, tea.Cmd) {
	if m.phase != phaseIdle || m.deps.UserModel == nil {
		return m, nil
	}
	m.prompt.Blur() // overlay owns the keyboard while open
	m.userModelGen++
	m.userModel = userModelState{view: userModelPanel, loading: true}
	return m, client.GetUserModelCmdTagged(m.deps.Ctx, m.deps.UserModel, m.userModelGen)
}

// closeUserModel dismisses the overlay and returns focus to the prompt input.
func (m Model) closeUserModel() (tea.Model, tea.Cmd) {
	m.userModelGen++
	m.userModel = userModelState{}
	cmd := m.prompt.Focus()
	return m, cmd
}

// onUserModelKey routes key presses while the overlay is open. esc closes it (the
// panel is read-only — nothing to navigate). Returns handled=false when closed so
// the caller falls through to normal idle key handling.
func (m Model) onUserModelKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd, bool) {
	if m.userModel.view == userModelNone {
		return m, nil, false
	}
	if key.Matches(msg, m.keys.Close) {
		if m.userModel.view == userModelDetail {
			m.userModelGen++
			m.userModel.view = userModelPanel
			m.userModel.detail = nil
			m.userModel.requestKey = ""
			return m, nil, true
		}
		mm, cmd := m.closeUserModel()
		return mm, cmd, true
	}
	if m.userModel.view == userModelDetail {
		switch {
		case key.Matches(msg, m.keys.Up):
			m.userModel.scroll = max(0, m.userModel.scroll-1)
		case key.Matches(msg, m.keys.Down):
			m.userModel.scroll++
		}
	}
	if m.userModel.view == userModelPanel {
		switch {
		case key.Matches(msg, m.keys.Up):
			if m.userModel.cursor > 0 {
				m.userModel.cursor--
			}
		case key.Matches(msg, m.keys.Down):
			if m.userModel.cursor < len(m.userModel.model.Entries)-1 {
				m.userModel.cursor++
			}
		case key.Matches(msg, m.keys.Choose):
			detailer, ok := m.deps.UserModel.(client.UserModelDetailer)
			if ok && m.userModel.cursor < len(m.userModel.model.Entries) {
				m.userModelGen++
				selectedKey := m.userModel.model.Entries[m.userModel.cursor].Key
				m.userModel.loading = true
				m.userModel.requestKey = selectedKey
				return m, client.GetUserModelEntryCmdTagged(m.deps.Ctx, detailer, selectedKey, m.userModelGen), true
			}
		}
	}
	return m, nil, true
}

// updateUserModelMsg reduces a client.UserModelMsg into the overlay state. It fires
// no follow-up command (single-shot read); handled=false for any other message so
// Update can fall through.
func (m Model) updateUserModelMsg(msg tea.Msg) (tea.Model, bool) {
	switch um := msg.(type) {
	case client.UserModelMsg:
		if um.Generation != m.userModelGen || m.userModel.view == userModelNone || m.userModel.requestKey != "" {
			return m, true
		}
		m.userModel.loading = false
		if um.Err != nil {
			m.userModel.err = um.Err
			return m, true
		}
		m.userModel.err = nil
		m.userModel.model = um.UserModel
		return m, true
	case client.UserModelDetailMsg:
		if um.Generation != m.userModelGen || m.userModel.view != userModelPanel || um.RequestKey == "" || um.RequestKey != m.userModel.requestKey {
			return m, true
		}
		m.userModel.loading = false
		if um.Err != nil {
			m.userModel.err = um.Err
			return m, true
		}
		m.userModel.err = nil
		m.userModel.model = um.UserModel
		m.userModel.detail = um.UserModel.Detail
		m.userModel.scroll = 0
		if m.userModel.detail == nil {
			m.userModel.detail = &client.UserModelDetail{Current: client.UserModelRevision{Key: um.RequestKey}}
		}
		m.userModel.view = userModelDetail
		return m, true
	default:
		return m, false
	}
}

// renderUserModelOverlay draws the panel centred over the conversation region via
// centerCard. All server-derived strings are terminal-sanitized.
func renderUserModelOverlay(th theme.Theme, st userModelState, caps client.Capabilities, hk helpKeys, width, height int) string {
	if st.view != userModelPanel && st.view != userModelDetail {
		return ""
	}
	if st.view == userModelDetail {
		return centerCard(th, renderUserModelDetail(th, st, hk, width, height), width, height)
	}
	return centerCard(th, renderUserModelPanel(th, st, caps, hk, width, height), width, height)
}

// userModelDisabledNote is the empty-state copy when the user model is NOT enabled
// on the connected server (caps.UserModel == false), with the remedy.
const userModelDisabledNote = "The user model is not enabled on this server.\n" +
	"Run a mecated without --no-user-model (and with an XDG/home to resolve the store)."

// userModelEmptyCopy returns the empty-state line: the "not enabled" note (with
// remedy) when caps.UserModel is false, else the "enabled but empty" note.
func userModelEmptyCopy(caps client.Capabilities) string {
	if !caps.UserModel {
		return userModelDisabledNote
	}
	return "The user model is empty — no operator facts saved yet."
}

// renderUserModelPanel renders the read-only inventory: a title, a dim aggregate
// metadata line (count · size · sha), then one row per entry (key + indented
// description), key-sorted by the server. EVERY server-derived string is
// terminal-sanitized.
func renderUserModelPanel(th theme.Theme, st userModelState, caps client.Capabilities, hk helpKeys, width, height int) string {
	var b strings.Builder
	b.WriteString(th.Style("askTitle").Render("User model (operator facts)") + "\n\n")

	budget := cardTextWidth(width)
	var lines []string
	selectedLine := 0
	switch {
	case st.loading:
		lines = []string{th.Style("muted").Render("loading…")}
	case st.err != nil:
		line := "get user model: " + sanitizeTerminal(st.err.Error())
		if budget > 0 {
			line = ansi.Wrap(line, budget, "")
		}
		lines = strings.Split(th.Style("errorText").Render(line), "\n")
	case len(st.model.Entries) == 0:
		lines = strings.Split(th.Style("muted").Render(userModelEmptyCopy(caps)), "\n")
	default:
		lines = append(lines, th.Style("muted").Render(renderUserModelMeta(st.model)), "")
		for i, e := range st.model.Entries {
			if i == st.cursor {
				selectedLine = len(lines)
			}
			marker := "  "
			if i == st.cursor {
				marker = "› "
			}
			lines = append(lines, renderToolCardText(th.Style("toolName"), marker+sanitizeTerminal(e.Key), budget))
			if e.Description != "" {
				lines = append(lines, strings.Split(th.Style("toolArgs").Render(indentWrap(sanitizeTerminal(e.Description), budget)), "\n")...)
			}
		}
	}
	window := userModelWindowLines(height, len(lines))
	scroll := clampScroll(selectedLine-window/2, len(lines), window)
	b.WriteString(windowRenderedLines(th, lines, scroll, window))
	b.WriteString("\n" + th.Style("muted").Render("↑/↓ select · enter inspect · the agent curates this · Recall loads a fact's value · "+hk.closeOnly+" close"))
	return b.String()
}

func userModelWindowLines(height, total int) int {
	rows := height - 10 // card border/padding, title, overflow marker, and footer
	if rows < 1 {
		rows = 1
	}
	if rows > 14 {
		rows = 14
	}
	if total > rows && rows > 1 {
		return rows - 1 // windowRenderedLines adds its overflow indicator
	}
	return rows
}

func renderUserModelDetail(th theme.Theme, st userModelState, hk helpKeys, width, height int) string {
	var b strings.Builder
	b.WriteString(th.Style("askTitle").Render("User model detail") + "\n\n")
	budget := cardTextWidth(width)
	wrap := func(text string) string { return wrapCardText(text, budget) }
	var lines []string
	if st.loading {
		lines = append(lines, th.Style("muted").Render("loading…"))
	} else if st.err != nil {
		lines = append(lines, strings.Split(th.Style("errorText").Render(wrap(st.err.Error())), "\n")...)
	} else if st.detail != nil {
		rev := st.detail.Current
		lines = append(lines, renderToolCardText(th.Style("toolName"), sanitizeTerminal(rev.Key), budget))
		for _, row := range []struct{ label, value string }{{"status", rev.Status}, {"version", rev.Version}, {"writer", rev.Writer}, {"origin", rev.Origin}, {"source session", rev.SourceSessionID}, {"proposal", rev.SourceProposalID}} {
			if row.value != "" {
				lines = append(lines, th.Style("muted").Render(row.label+": ")+wrap(row.value))
			}
		}
		if !rev.UpdatedAt.IsZero() {
			lines = append(lines, th.Style("muted").Render("updated: ")+sanitizeTerminal(rev.UpdatedAt.UTC().Format("2006-01-02 15:04:05Z")))
		}
		if rev.Description != "" {
			lines = append(lines, th.Style("muted").Render("description: ")+wrap(rev.Description))
		}
		lines = append(lines, "")
		lines = append(lines, strings.Split(th.Style("toolArgs").Render(indentWrap(sanitizeTerminal(rev.Value), cardTextWidth(width))), "\n")...)
		lines = append(lines, "")
		if !st.detail.HistoryAvailable {
			lines = append(lines, th.Style("muted").Render("History unavailable from this store/driver."))
		} else {
			lines = append(lines, th.Style("muted").Render(fmt.Sprintf("History: %d bounded revisions", len(st.detail.History))))
			for _, prior := range st.detail.History {
				line := fmt.Sprintf("%s · %s · %s", prior.Version, prior.Status, prior.UpdatedAt.UTC().Format("2006-01-02"))
				lines = append(lines, th.Style("toolArgs").Render(sanitizeTerminal(line)))
			}
		}
	}
	window := userModelWindowLines(height, len(lines))
	b.WriteString(windowRenderedLines(th, lines, clampScroll(st.scroll, len(lines), window), window))
	b.WriteString("\n" + th.Style("muted").Render(hk.scroll+" scroll · read-only · ask the model to use ForgetUserMemory or UndoUserMemory · "+hk.closeOnly+" back"))
	return b.String()
}

// renderUserModelMeta renders the dim aggregate line: N facts · M bytes · sha.
func renderUserModelMeta(um client.UserModel) string {
	segs := []string{
		plural(len(um.Entries), "fact"),
		fmt.Sprintf("%d bytes", um.SizeBytes),
	}
	if um.SHA256 != "" {
		short := um.SHA256
		if len(short) > 12 {
			short = short[:12]
		}
		segs = append(segs, "sha:"+sanitizeTerminal(short))
	}
	return strings.Join(segs, " · ")
}
