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
	userModelNone  userModelView = iota // overlay closed
	userModelPanel                      // read-only inventory (key + description)
)

// userModelState holds the user-model overlay state on the Model. Value-embedded so
// the Model stays a plain struct Update copies; the UserModel value is replaced
// wholesale on each RPC result.
type userModelState struct {
	view    userModelView
	loading bool // the GetUserModel RPC is in flight
	err     error
	model   client.UserModel
}

// openUserModel opens the inventory panel and fires the GetUserModel RPC. Only
// callable while idle and when a user-model lister is wired; returns the model
// unchanged otherwise. The result arrives as a client.UserModelMsg handled in
// updateUserModelMsg.
func (m Model) openUserModel() (tea.Model, tea.Cmd) {
	if m.phase != phaseIdle || m.deps.UserModel == nil {
		return m, nil
	}
	m.ta.Blur() // overlay owns the keyboard while open
	m.userModel = userModelState{view: userModelPanel, loading: true}
	return m, client.GetUserModelCmd(m.deps.Ctx, m.deps.UserModel)
}

// closeUserModel dismisses the overlay and returns focus to the prompt input.
func (m Model) closeUserModel() (tea.Model, tea.Cmd) {
	m.userModel = userModelState{}
	cmd := m.ta.Focus()
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
		mm, cmd := m.closeUserModel()
		return mm, cmd, true
	}
	return m, nil, true
}

// updateUserModelMsg reduces a client.UserModelMsg into the overlay state. It fires
// no follow-up command (single-shot read); handled=false for any other message so
// Update can fall through.
func (m Model) updateUserModelMsg(msg tea.Msg) (tea.Model, bool) {
	um, ok := msg.(client.UserModelMsg)
	if !ok {
		return m, false
	}
	m.userModel.loading = false
	if um.Err != nil {
		m.userModel.err = um.Err
		return m, true
	}
	m.userModel.err = nil
	m.userModel.model = um.UserModel
	return m, true
}

// renderUserModelOverlay draws the panel centred over the conversation region via
// centerCard. All server-derived strings are terminal-sanitized.
func renderUserModelOverlay(th theme.Theme, st userModelState, caps client.Capabilities, hk helpKeys, width, height int) string {
	if st.view != userModelPanel {
		return ""
	}
	return centerCard(th, renderUserModelPanel(th, st, caps, hk, width), width, height)
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
func renderUserModelPanel(th theme.Theme, st userModelState, caps client.Capabilities, hk helpKeys, width int) string {
	var b strings.Builder
	b.WriteString(th.Style("askTitle").Render("User model (operator facts)") + "\n\n")

	budget := cardTextWidth(width)
	switch {
	case st.loading:
		b.WriteString(th.Style("muted").Render("loading…") + "\n")
	case st.err != nil:
		line := "get user model: " + sanitizeTerminal(st.err.Error())
		if budget > 0 {
			line = ansi.Wrap(line, budget, "")
		}
		b.WriteString(th.Style("errorText").Render(line) + "\n")
	case len(st.model.Entries) == 0:
		b.WriteString(th.Style("muted").Render(userModelEmptyCopy(caps)) + "\n")
	default:
		b.WriteString(th.Style("muted").Render(renderUserModelMeta(st.model)) + "\n\n")
		for _, e := range st.model.Entries {
			b.WriteString(th.Style("toolName").Render(sanitizeTerminal(e.Key)) + "\n")
			if e.Description != "" {
				b.WriteString(th.Style("toolArgs").Render(indentWrap(sanitizeTerminal(e.Description), budget)) + "\n")
			}
		}
	}

	// The close chord (Close) reads the LIVE keyMap marking (issue #457); with the
	// default it is byte-identical to the historical "esc close".
	b.WriteString("\n" + th.Style("muted").Render("the agent curates this · Recall loads a fact's value · "+hk.closeOnly+" close"))
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
