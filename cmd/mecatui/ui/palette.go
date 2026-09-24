package ui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/internal/terminaltext"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
	"github.com/stacklok/mecatl/cmd/mecatui/ui/internal/bounded"
)

// maxPaletteRows caps the palette body at twelve physical rows, including up to
// two overflow indicators, so large command sets cannot push the input off-screen.
// The selection still moves through the full filtered set; the window scrolls to
// keep it visible.
const maxPaletteRows = 12

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
	// list is pointer-owned so Bubble Tea Model copies retain one selection and
	// viewport anchor. The palette owns command activation; bounded.List owns only
	// physical layout and selection.
	list *bounded.List
}

func (st *paletteState) syncList() {
	st.syncListWidth(0)
}

func (st *paletteState) syncListWidth(contentWidth int) {
	if st.list == nil {
		st.list = new(bounded.List)
	}
	items := make([]bounded.ListItem, 0, len(st.filtered))
	for _, command := range st.filtered {
		kind := "workspace:"
		if command.Builtin {
			kind = "builtin:"
		}
		items = append(items, bounded.ListItem{
			ID:   kind + strings.ToLower(command.Name),
			Text: paletteEntry(command, contentWidth),
		})
	}
	st.list.SetItems(items)
}

// paletteEntry creates the explicitly bounded physical lines for one command.
// contentWidth is the space after the bounded list's selection gutter.
func paletteEntry(command client.Command, contentWidth int) string {
	name := "/" + terminaltext.SanitizeSingleLine(command.Name)
	if contentWidth <= 0 {
		return ""
	}
	if ansi.StringWidth(name) >= contentWidth {
		return paletteTruncate(name, contentWidth)
	}

	description := terminaltext.SanitizeSingleLine(command.Description)
	if description == "" || contentWidth < 3 {
		return name
	}

	prefix := name + "  "
	firstWidth := contentWidth - ansi.StringWidth(prefix)
	if firstWidth <= 0 {
		return name
	}
	parts, complete := paletteWrap(description, []int{firstWidth, contentWidth - 2, contentWidth - 2})
	if len(parts) == 0 {
		return name
	}
	lines := []string{prefix + parts[0]}
	for _, part := range parts[1:] {
		lines = append(lines, "│ "+part)
	}
	if !complete {
		lines[len(lines)-1] = "│ " + paletteEllipsis(parts[len(parts)-1], contentWidth-2)
	}
	return strings.Join(lines, "\n")
}

func paletteTruncate(text string, width int) string {
	if width <= 0 {
		return ""
	}
	if ansi.StringWidth(text) <= width {
		return text
	}
	if width == 1 {
		return "…"
	}
	return paletteCut(text, width-1) + "…"
}

func paletteCut(text string, width int) string {
	return ansi.Strip(ansi.Cut(text, 0, width))
}

func paletteEllipsis(text string, width int) string {
	if width <= 0 {
		return ""
	}
	if width == 1 {
		return "…"
	}
	if ansi.StringWidth(text) < width {
		return text + "…"
	}
	return paletteTruncate(text, width)
}

// paletteWrap uses word boundaries when possible and cuts an overlong word by
// display cells. It returns at most one part for each supplied width.
func paletteWrap(text string, widths []int) ([]string, bool) {
	remaining := strings.TrimSpace(text)
	parts := make([]string, 0, len(widths))
	for _, width := range widths {
		if width <= 0 || remaining == "" {
			break
		}
		if ansi.StringWidth(remaining) <= width {
			parts = append(parts, remaining)
			remaining = ""
			break
		}
		cut := paletteCut(remaining, width)
		if space := strings.LastIndexByte(cut, ' '); space > 0 {
			cut = strings.TrimRight(cut[:space], " ")
		}
		parts = append(parts, cut)
		remaining = strings.TrimSpace(strings.TrimPrefix(remaining, cut))
	}
	return parts, remaining == ""
}

func (st paletteState) selected() client.Command {
	if st.list == nil || len(st.filtered) == 0 {
		return client.Command{}
	}
	index := st.list.Cursor()
	if index < 0 || index >= len(st.filtered) {
		return client.Command{}
	}
	return st.filtered[index]
}

func (m Model) paletteVisible() bool {
	return m.palette.open && (m.palette.list == nil || m.palette.list.Valid())
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
	bs := builtinCommands(m.caps, m.wiredCollaborators())
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
		key := canonicalBuiltinName(strings.ToLower(b.Name))
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, b)
	}
	for _, d := range discovered {
		key := canonicalBuiltinName(strings.ToLower(d.Name))
		if key != strings.ToLower(d.Name) {
			// Dispatch-only aliases are deliberately not palette rows: selecting one
			// would promise a workspace command that local dispatch will intercept.
			continue
		}
		if _, ok := seen[key]; ok {
			// Either a built-in already owns this name (built-in wins) or it is a
			// duplicate discovered row; drop it.
			continue
		}
		seen[key] = struct{}{}
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
	prefix, isCmd := commandPrefix(m.prompt.Value())
	if !isCmd {
		// Left command mode: reset the palette (including the esc-dismiss latch) so a
		// later "/" opens it afresh.
		m.palette.open = false
		m.palette.dismissed = false
		m.palette.filtered = nil
		m.palette.syncList()
		return m, nil
	}

	var fetch tea.Cmd
	if !m.palette.fetched && m.deps.Cmds != nil {
		// Fetch once on first entry into command mode; the result arrives as a
		// CommandsMsg and re-syncs the palette. Only when a Commander is wired —
		// built-ins need no fetch.
		m.palette.fetched = true
		fetch = client.ListCommandsCmd(m.deps.Ctx, m.deps.Cmds, m.sessionID)
	}

	if m.palette.dismissed {
		// The user pressed esc; stay closed until they leave command mode.
		m.palette.open = false
		return m, fetch
	}

	merged := mergeCommands(m.builtinRows(), m.palette.commands)
	m.palette.filtered = filterCommands(merged, prefix)
	m.palette.syncList()
	m.palette.open = len(m.palette.filtered) > 0
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

// paletteMoveUp moves the selection up one logical command (no wrap).
func (m *Model) paletteMoveUp() {
	if m.palette.list != nil {
		m.palette.list.Move(bounded.LineUp)
	}
}

// paletteMoveDown moves the selection down one logical command (no wrap).
func (m *Model) paletteMoveDown() {
	if m.palette.list != nil {
		m.palette.list.Move(bounded.LineDown)
	}
}

// paletteComplete writes the selected command name into the input as "/<name> "
// (ready for args), closes the palette, and leaves the cursor after the trailing
// space. It is a no-op when the palette has no selectable rows.
func (m Model) paletteComplete() Model {
	selected := m.palette.selected()
	if !m.palette.open || selected.Name == "" {
		return m
	}
	m.prompt.Rewrite("/" + selected.Name + " ")
	// Completing leaves command mode (a trailing space follows the name), so the
	// palette closes; settle the derived state without re-fetching.
	m.palette.open = false
	m.palette.filtered = nil
	m.palette.syncList()
	return m
}

// paletteDismiss latches an esc dismissal: the palette closes but the input is
// left untouched, and it stays closed until the input leaves command mode.
func (m Model) paletteDismiss() Model {
	m.palette.open = false
	m.palette.dismissed = true
	return m
}

// renderPalette draws the command dropdown with the normal maximum body.
func renderPalette(th theme.Theme, st paletteState, caps client.Capabilities, input string, width int) string {
	return renderPaletteSized(th, st, caps, input, width, maxPaletteRows)
}

// renderPaletteSized draws the command dropdown as a bordered card. bodyRows is
// the complete physical-row budget for command rows and overflow indicators.
func renderPaletteSized(th theme.Theme, st paletteState, caps client.Capabilities, input string, width, bodyRows int) string {
	cardWidth := min(128, width)
	cardStyle := th.Style("askCard")
	contentWidth := cardWidth - cardStyle.GetHorizontalFrameSize()
	if st.list == nil {
		st.syncList()
	}
	if bodyRows <= 0 || contentWidth < 3 {
		st.list.SetGeometry(contentWidth, 0, 1, bounded.Wrap)
		return ""
	}
	if !st.open || len(st.filtered) == 0 {
		st.list.SetGeometry(contentWidth, 0, 1, bounded.Wrap)
		if note := paletteEmptyNote(th, st, caps, input); note != "" {
			return cardStyle.Width(cardWidth).Render(ansi.Cut(note, 0, contentWidth))
		}
		return ""
	}

	st.list.SetGeometry(contentWidth, bodyRows, 1, bounded.Wrap)
	st.syncListWidth(contentWidth - 2) // selection cell and its trailing padding
	if !st.list.Valid() {
		return ""
	}
	// The palette has no independent physical-scroll action. Keep the selected
	// command visible on every rendered frame; wrapped commands still page through
	// their physical segments when they exceed the body height.
	view := st.list.ViewWithIndicators(bodyRows, true)
	header := "commands"
	if bodyRows == 1 {
		// One-row cards present the shared logical overflow metadata in their header
		// rather than displacing their only selectable row with chrome.
		var overflow []string
		if view.Above > 0 {
			overflow = append(overflow, fmt.Sprintf("↑%d above", view.Above))
		}
		if view.Below > 0 {
			overflow = append(overflow, fmt.Sprintf("↓%d below", view.Below))
		}
		if len(overflow) > 0 {
			header = strings.Join(overflow, "·")
		}
	}
	if len(view.Rows) == 0 {
		return ""
	}

	lines := []string{th.Style("muted").Render(ansi.Cut(header, 0, contentWidth))}
	if bodyRows > 1 && view.Above > 0 {
		lines = append(lines, th.Style("muted").Render(ansi.Cut(fmt.Sprintf("  ↑ +%d above", view.Above), 0, contentWidth)))
	}
	for _, row := range view.Rows {
		lines = append(lines, renderPaletteRow(th, row))
	}
	if bodyRows > 1 && view.Below > 0 {
		lines = append(lines, th.Style("muted").Render(ansi.Cut(fmt.Sprintf("  ↓ +%d below", view.Below), 0, contentWidth)))
	}
	lines = append(lines, th.Style("muted").Render(ansi.Cut("↑/↓ select · pgup/pgdn page · tab/enter complete · esc dismiss", 0, contentWidth)))
	return cardStyle.Width(cardWidth).Render(strings.Join(lines, "\n"))
}

func renderPaletteRow(th theme.Theme, row bounded.ListRow) string {
	// Keep the shared gutter and selection treatment while the entry text carries
	// its own command/description hierarchy.
	gutter := presentListRow(bounded.ListRow{
		Selected: row.Selected, CursorMarker: row.CursorMarker, GutterCells: row.GutterCells,
	}, th.Style("spinner"), th.Style("toolArgs"))
	text := row.Text
	if row.ItemLine == 0 {
		if name, description, found := strings.Cut(text, "  "); found {
			commandStyle := lipgloss.NewStyle().Bold(true)
			if row.Selected {
				commandStyle = th.Style("spinner").Bold(true)
			}
			text = commandStyle.Render(name) + th.Style("muted").Render("  "+description)
		} else {
			commandStyle := lipgloss.NewStyle().Bold(true)
			if row.Selected {
				commandStyle = th.Style("spinner").Bold(true)
			}
			text = commandStyle.Render(text)
		}
	} else {
		text = th.Style("muted").Render(text)
	}
	return gutter.Style.Render(gutter.Text) + text
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
