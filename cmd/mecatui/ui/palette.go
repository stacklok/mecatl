package ui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

// maxPaletteRows caps how many command rows the palette shows at once so a large
// command set cannot push the input off-screen. The selection still moves
// through the full filtered set; the window scrolls to keep it visible.
const maxPaletteRows = 8

// Inline-menu navigation key strings, shared by the slash palette (onPaletteKey)
// and the @-mention menu (onMentionKey) since both react to msg.String() with the
// same vocabulary. Named so the two switches don't repeat the literals (and so a
// rebind is a single edit).
const (
	keyMenuUp      = "up"
	keyMenuDown    = "down"
	keyMenuTab     = "tab"
	keyMenuEnter   = "enter"
	keyMenuDismiss = "esc"
)

// paletteState holds the slash-command palette's state on the Model. It is value-
// embedded (like mcpState) so the Model stays a plain struct Update copies. The
// palette is a lightweight inline dropdown over the input — NOT a phase or an
// overlay that seizes the keyboard: normal typing continues to flow to the
// textarea, and the palette merely reacts to what the input now contains.
type paletteState struct {
	// open reports whether the palette is currently showing. It is true only while
	// the input is a command line (starts with "/" on a single line), the user has
	// not dismissed it with esc, and there is at least one matching command.
	open bool
	// dismissed latches an esc dismissal so the palette stays closed until the
	// input leaves command mode (or the user retypes "/"), matching the "esc
	// dismisses" contract without fighting the user's next keystroke.
	dismissed bool
	// fetched reports whether a ListCommands result has landed (success or empty),
	// so the fetch fires at most once per session.
	fetched bool

	// commands is the full discovered set (name-sorted, from the server).
	commands []client.Command
	// filtered is the subset matching the current "/<prefix>" token, recomputed on
	// each input change.
	filtered []client.Command
	// cursor is the selected row within filtered (clamped to its bounds).
	cursor int
}

// commandPrefix reports whether s is a command line and, if so, returns the
// command-name token typed so far (the run of name characters after a single
// leading "/"). A command line is one that, ignoring leading spaces, begins with
// "/" and is a SINGLE line (the palette is for the first command token only; once
// the user has typed args or a newline it no longer drives completion).
func commandPrefix(s string) (prefix string, ok bool) {
	if strings.ContainsRune(s, '\n') {
		return "", false
	}
	t := strings.TrimLeft(s, " \t")
	if !strings.HasPrefix(t, "/") {
		return "", false
	}
	t = t[1:]
	// The palette only completes the NAME token: as soon as a space follows the
	// name (the user is typing args) the palette stops driving completion.
	if strings.ContainsAny(t, " \t") {
		return "", false
	}
	return t, true
}

// builtinRows returns the caps-filtered built-in commands as client.Command rows
// (Builtin:true) for the palette. They always exist (at minimum /clear and
// /help), independent of any Commander or server slash-command support; the
// caps-gated ones (/mcp, /agents, /team, /skills) appear only when reachable.
func (m Model) builtinRows() []client.Command {
	bs := builtinCommands(m.caps, wiredCollaborators{
		MCP: m.deps.MCP != nil, Agents: m.deps.Agents != nil, Skills: m.deps.Skills != nil,
		Soul: m.deps.Soul != nil, UserModel: m.deps.UserModel != nil, Models: m.deps.Models != nil,
		Worktrees: m.deps.Worktrees != nil, Scheduling: m.deps.Sched != nil,
	})
	rows := make([]client.Command, 0, len(bs))
	for _, b := range bs {
		rows = append(rows, client.Command{Name: b.name, Description: b.desc, Builtin: true})
	}
	return rows
}

// mergeCommands merges the always-present built-in rows with the server-
// discovered workspace rows into the palette's command set. PRECEDENCE: a
// built-in WINS a name collision — the colliding discovered row is dropped, so a
// workspace "/clear" can never shadow the client-side /clear. Built-ins come
// first (in their fixed order), then the discovered rows (already name-sorted by
// the server), de-duplicated. The result feeds filterCommands unchanged.
func mergeCommands(builtins, discovered []client.Command) []client.Command {
	seen := make(map[string]struct{}, len(builtins)+len(discovered))
	out := make([]client.Command, 0, len(builtins)+len(discovered))
	for _, b := range builtins {
		if _, ok := seen[b.Name]; ok {
			continue
		}
		seen[b.Name] = struct{}{}
		out = append(out, b)
	}
	for _, d := range discovered {
		if _, ok := seen[d.Name]; ok {
			// Either a built-in already owns this name (built-in wins) or it is a
			// duplicate discovered row; drop it.
			continue
		}
		seen[d.Name] = struct{}{}
		out = append(out, d)
	}
	return out
}

// syncPalette recomputes the palette's open/filtered state from the current
// textarea content. It is called after every idle keystroke that may have changed
// the input. It returns the (possibly) updated model and a command — the latter
// is the ListCommands fetch, fired lazily the first time the input enters command
// mode (and only when a Commander is wired).
//
// The palette opens for ANY command line, even with no Commander wired, because
// the built-in commands always exist; the lazy server fetch fires only when a
// Commander IS wired.
func (m Model) syncPalette() (Model, tea.Cmd) {
	prefix, isCmd := commandPrefix(m.ta.Value())
	if !isCmd {
		// Left command mode: reset the palette (including the esc-dismiss latch) so a
		// later "/" opens it afresh.
		m.palette.open = false
		m.palette.dismissed = false
		m.palette.filtered = nil
		m.palette.cursor = 0
		return m, nil
	}

	var fetch tea.Cmd
	if !m.palette.fetched && m.deps.Cmds != nil {
		// Fetch once on first entry into command mode; the result arrives as a
		// CommandsMsg and re-syncs the palette. Only when a Commander is wired —
		// built-ins need no fetch.
		m.palette.fetched = true
		fetch = client.ListCommandsCmd(m.deps.Ctx, m.deps.Cmds, m.deps.Workspace)
	}

	if m.palette.dismissed {
		// The user pressed esc; stay closed until they leave command mode.
		m.palette.open = false
		return m, fetch
	}

	merged := mergeCommands(m.builtinRows(), m.palette.commands)
	m.palette.filtered = filterCommands(merged, prefix)
	m.palette.open = len(m.palette.filtered) > 0
	if m.palette.cursor >= len(m.palette.filtered) {
		m.palette.cursor = 0
	}
	return m, fetch
}

// filterCommands returns the commands whose name has prefix (case-insensitive),
// preserving the input order (already name-sorted from the server). An empty
// prefix matches everything.
func filterCommands(cmds []client.Command, prefix string) []client.Command {
	if prefix == "" {
		return cmds
	}
	lower := strings.ToLower(prefix)
	out := make([]client.Command, 0, len(cmds))
	for _, c := range cmds {
		if strings.HasPrefix(strings.ToLower(c.Name), lower) {
			out = append(out, c)
		}
	}
	return out
}

// paletteMoveUp moves the selection up one row (no wrap), clamped to the top.
func (m *Model) paletteMoveUp() {
	if m.palette.cursor > 0 {
		m.palette.cursor--
	}
}

// paletteMoveDown moves the selection down one row (no wrap), clamped to the last.
func (m *Model) paletteMoveDown() {
	if m.palette.cursor < len(m.palette.filtered)-1 {
		m.palette.cursor++
	}
}

// paletteComplete writes the selected command name into the input as "/<name> "
// (ready for args), closes the palette, and leaves the cursor after the trailing
// space. It is a no-op when the palette has no selectable rows.
func (m Model) paletteComplete() Model {
	if !m.palette.open || m.palette.cursor >= len(m.palette.filtered) {
		return m
	}
	name := m.palette.filtered[m.palette.cursor].Name
	m.ta.SetValue("/" + name + " ")
	// Completing leaves command mode (a trailing space follows the name), so the
	// palette closes; settle the derived state without re-fetching.
	m.palette.open = false
	m.palette.filtered = nil
	m.palette.cursor = 0
	return m
}

// paletteDismiss latches an esc dismissal: the palette closes but the input is
// left untouched, and it stays closed until the input leaves command mode.
func (m Model) paletteDismiss() Model {
	m.palette.open = false
	m.palette.dismissed = true
	return m
}

// renderPalette draws the command dropdown as a bordered card. The selected row
// is highlighted; descriptions are dim. All server-derived strings are
// terminal-sanitized. The list windows to maxPaletteRows around the selection so
// a large set never overruns the input.
//
// When the palette is NOT showing rows but the input IS a command line ("/…"),
// it renders a single neutral muted note instead of "". Built-ins always exist,
// so the only way to reach this note is a typed prefix matching neither a
// built-in nor a workspace command (e.g. "/zzz"); the note reads "no matching
// command". It returns "" only when the input is not a command line at all.
func renderPalette(th theme.Theme, st paletteState, caps client.Capabilities, input string, width int) string {
	if !st.open || len(st.filtered) == 0 {
		if note := paletteEmptyNote(th, st, caps, input); note != "" {
			card := th.Style("askCard").Render(note)
			if width > 0 {
				return lipgloss.NewStyle().MaxWidth(width).Render(card)
			}
			return card
		}
		return ""
	}
	start, end := scrollWindow(st.cursor, len(st.filtered), maxPaletteRows)

	var b strings.Builder
	b.WriteString(th.Style("muted").Render("commands") + "\n")
	for i := start; i < end; i++ {
		c := st.filtered[i]
		name := sanitizeTerminal("/" + c.Name)
		desc := sanitizeTerminal(c.Description)
		row := name
		if desc != "" {
			row += "  " + th.Style("muted").Render(desc)
		}
		if i == st.cursor {
			b.WriteString(th.Style("askButtonActive").Render("› "+row) + "\n")
		} else {
			b.WriteString(th.Style("toolArgs").Render("  "+row) + "\n")
		}
	}
	b.WriteString(th.Style("muted").Render("↑/↓ select · tab/enter complete · esc dismiss"))

	card := th.Style("askCard").Render(b.String())
	if width > 0 {
		return lipgloss.NewStyle().MaxWidth(width).Render(card)
	}
	return card
}

// paletteEmptyNote returns the one-line neutral note shown when the input is a
// command line ("/…") but the palette has no rows to offer. Because built-ins
// always exist, the caps-based "not enabled"/"enabled but empty" distinction is
// no longer meaningful here — the only way to land on it is a typed prefix that
// matches no command at all — so it is a single neutral "no matching command".
// It returns "" when the input is not a command line, or when the user dismissed
// the palette with esc (so esc still fully hides it without the note popping
// back). caps is retained in the signature for call-site symmetry but unused.
func paletteEmptyNote(th theme.Theme, st paletteState, _ client.Capabilities, input string) string {
	if _, isCmd := commandPrefix(input); !isCmd {
		return ""
	}
	if st.dismissed {
		return ""
	}
	return th.Style("muted").Render("no matching command")
}
