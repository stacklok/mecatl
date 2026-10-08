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
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
	"github.com/stacklok/mecatl/cmd/mecatui/ui/internal/bounded"
)

// agentColorPalette maps the Claude Code agent-def `color` hint (one of a fixed
// allowlist — see internal/adapter/agents/agentdef.go) to a standard ANSI
// 16-colour index (as the string lipgloss.Color expects). It is a HINT-ONLY tint
// applied to the def name; an empty or unrecognised value yields no entry, so the
// name keeps its default style. Colour NEVER affects layout — it only sets the
// foreground of the already-rendered name string, so a stripANSI'd row is
// byte-identical regardless of the colour.
var agentColorPalette = map[string]string{
	"red":    "9",  // bright red
	"green":  "10", // bright green
	"yellow": "11", // bright yellow
	"blue":   "12", // bright blue
	"purple": "13", // bright magenta
	"pink":   "13", // bright magenta (closest ANSI)
	"cyan":   "14", // bright cyan
	"orange": "3",  // yellow (closest ANSI for orange)
}

// agentNameStyle returns the lipgloss style for a def name: the base toolName
// style, tinted with the def's colour hint when it maps to a supported ANSI
// colour. An empty / unknown colour falls back to the plain base style. The match
// is case-insensitive and whitespace-trimmed. Tinting only sets a foreground, so
// it never alters the name's text width or the stripANSI'd content.
func agentNameStyle(th theme.Theme, color string) lipgloss.Style {
	base := th.Style("toolName")
	if idx, ok := agentColorPalette[strings.ToLower(strings.TrimSpace(color))]; ok {
		return base.Foreground(lipgloss.Color(idx))
	}
	return base
}

// agentsInvView is the active agent-definition inventory overlay (none =
// closed). Like the /skills panel it is a read-only inventory layered over the
// conversation: it does not change the run phase, opens only while idle, and is
// dismissed with esc. It is the DEFINITION inventory (the resolved registry the
// Subagent tool routes delegations to) — distinct from the live-team overlay (/team,
// f6), which shows a team that has actually run. Activation stays the
// model's run-path concern; the panel is discovery only.
type agentsInvView int

const (
	agentsInvNone  agentsInvView = iota // overlay closed
	agentsInvPanel                      // read-only inventory (name + description + metadata)
)

// agentsInvState holds the root-owned agent-definition inventory overlay state.
// The pointer-owned viewport is the sole physical browsing authority. Agent
// snapshots are still replaced wholesale so Model's value-copy semantics remain
// unchanged.
type agentsInvState struct {
	view       agentsInvView
	generation uint64
	loading    bool  // the ListAgents RPC is in flight
	err        error // the ListAgents error, rendered distinctly (nil on success)
	agents     []client.Agent
	viewport   *bounded.Viewport
}

// agentsInvResultMsg binds a ListAgents result to the open that issued it.
// It remains UI-private because the protocol's result carries no request identity.
type agentsInvResultMsg struct {
	generation uint64
	result     client.AgentsMsg
}

func newAgentsInvViewport() *bounded.Viewport { return new(bounded.Viewport) }

// openAgentsInv opens the inventory panel and fires the ListAgents RPC. Only
// callable while idle and when an agents lister is wired; returns the model
// unchanged otherwise. The UI-private result wrapper is handled in
// updateAgentsInvMsg.
func (m Model) openAgentsInv() (tea.Model, tea.Cmd) {
	if m.phase != phaseIdle || m.deps.Agents == nil {
		return m, nil
	}
	m.prompt.Blur() // overlay owns the keyboard while open
	m.agentsInvRequestToken++
	generation := m.agentsInvRequestToken
	m.agentsInv = agentsInvState{view: agentsInvPanel, generation: generation, loading: true}
	list := client.ListAgentsCmd(m.deps.Ctx, m.deps.Agents)
	return m, func() tea.Msg {
		return agentsInvResultMsg{generation: generation, result: list().(client.AgentsMsg)}
	}
}

// closeAgentsInv dismisses the overlay and returns focus to the prompt input.
func (m Model) closeAgentsInv() (tea.Model, tea.Cmd) {
	m.agentsInv = agentsInvState{}
	cmd := m.prompt.Focus()
	return m, cmd
}

// onAgentsInvKey routes key presses while the inventory overlay is open. esc
// closes it; the scroll keys (pgup/pgdown, up/down, home/end) move the row
// window over a long inventory (the /soul onSoulKey pattern). Every other key
// is swallowed (handled=true) so it never leaks into idle input. Returns
// handled=false only when the overlay is closed so the caller falls through to
// normal idle key handling.
func (m Model) onAgentsInvKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd, bool) {
	if m.agentsInv.view == agentsInvNone {
		return m, nil, false
	}
	if key.Matches(msg, m.keys.Close) {
		mm, cmd := m.closeAgentsInv()
		return mm, cmd, true
	}
	rows := m.configureAgentsInvViewport()
	if m.agentsInv.viewport == nil {
		return m, nil, true
	}
	switch {
	case key.Matches(msg, m.keys.Down):
		m.agentsInv.viewport.Move(bounded.LineDown, len(rows))
	case key.Matches(msg, m.keys.Up):
		m.agentsInv.viewport.Move(bounded.LineUp, len(rows))
	case key.Matches(msg, m.keys.ScrollD):
		m.agentsInv.viewport.Move(bounded.PageDown, len(rows))
	case key.Matches(msg, m.keys.ScrollU):
		m.agentsInv.viewport.Move(bounded.PageUp, len(rows))
	case key.Matches(msg, m.keys.ScrollBottom):
		m.agentsInv.viewport.Move(bounded.End, len(rows))
	case key.Matches(msg, m.keys.ScrollTop):
		m.agentsInv.viewport.Move(bounded.Top, len(rows))
	}
	return m, nil, true
}

// onAgentsInvWheel keeps physical scrolling owned by the visible definition
// inventory. Compact and non-inventory states have no viewport and still consume
// the event through the root-owned overlay route.
func (m Model) onAgentsInvWheel(msg tea.MouseWheelMsg) (tea.Model, tea.Cmd) {
	if m.agentsInv.view == agentsInvNone {
		return m, nil
	}
	rows := m.configureAgentsInvViewport()
	if m.agentsInv.viewport == nil {
		return m, nil
	}
	switch msg.Button {
	case tea.MouseWheelUp:
		m.agentsInv.viewport.Move(bounded.LineUp, len(rows))
	case tea.MouseWheelDown:
		m.agentsInv.viewport.Move(bounded.LineDown, len(rows))
	}
	return m, nil
}

// configureAgentsInvViewport applies the same measured geometry used by the
// renderer and returns the ANSI-safe physical rows navigation moves over.
func (m *Model) configureAgentsInvViewport() []string {
	if len(m.agentsInv.agents) == 0 || m.agentsInv.loading || m.agentsInv.err != nil {
		m.agentsInv.viewport = nil
		return nil
	}
	layout := newAgentsInvLayout(m.deps.Theme, m.helpKeyMarkings(), m.width, m.vp.Height())
	rows := agentsInvRowLines(m.deps.Theme, m.agentsInv.agents, layout.bodyWidth)
	height, ok := layout.viewportHeight(len(rows))
	if !ok {
		m.agentsInv.viewport = nil
		return nil
	}
	if m.agentsInv.viewport == nil {
		m.agentsInv.viewport = newAgentsInvViewport()
	}
	m.agentsInv.viewport.SetGeometry(layout.bodyWidth, height, 0, bounded.Clip)
	m.agentsInv.viewport.Clamp(len(rows))
	return rows
}

// updateAgentsInvMsg reduces a client.AgentsMsg into the overlay state. It fires
// no follow-up command (the panel is a single-shot read), so it returns only the
// model + handled flag; handled=false for any other message so Update can fall
// through.
func (m Model) updateAgentsInvMsg(msg tea.Msg) (tea.Model, bool) {
	result, ok := msg.(agentsInvResultMsg)
	if !ok {
		return m, false
	}
	if m.agentsInv.view != agentsInvPanel || result.generation != m.agentsInv.generation {
		return m, true
	}
	am := result.result
	m.agentsInv.loading = false
	if am.Err != nil {
		m.agentsInv.err = am.Err
		m.agentsInv.agents = nil
		m.agentsInv.viewport = nil
		return m, true
	}
	m.agentsInv.err = nil
	m.agentsInv.agents = am.Agents
	m.agentsInv.viewport = nil
	m.configureAgentsInvViewport()
	return m, true
}

// renderAgentsInvOverlay draws the root-owned inventory overlay within the
// offered conversation geometry. Positive geometry either gets one measured
// askCard layout or a close-only compact fallback; nonpositive geometry renders
// nothing.
func renderAgentsInvOverlay(th theme.Theme, st agentsInvState, caps client.Capabilities, hk helpKeys, width, height int) string {
	if st.view != agentsInvPanel || width <= 0 || height <= 0 {
		return ""
	}
	layout := newAgentsInvLayout(th, hk, width, height)
	body, ok := renderAgentsInvNormalBody(th, st, caps, layout)
	if !ok {
		return renderAgentsInvCompact(th, hk, width)
	}
	card := th.Style("askCard").Width(layout.outerWidth).Render(body)
	rows := strings.Split(card, "\n")
	for i, row := range rows {
		rows[i] = strings.Repeat(" ", max(0, (width-lipgloss.Width(row))/2)) + row
	}
	card = strings.Join(rows, "\n")
	remaining := max(0, height-lipgloss.Height(card))
	return strings.Repeat("\n", remaining/2) + card + strings.Repeat("\n", remaining-remaining/2)
}

const agentsInvMaxOuterWidth = 128

const agentsInvFooter = "agent definitions route Subagent delegations · %s scroll · %s close"

type agentsInvLayout struct {
	outerWidth, bodyWidth int
	frameHeight           int
	title, footer         []string
	bodyCapacity          int
	normal                bool
}

func newAgentsInvLayout(th theme.Theme, hk helpKeys, width, height int) agentsInvLayout {
	layout := agentsInvLayout{}
	if width <= 0 || height <= 0 {
		return layout
	}
	card := th.Style("askCard")
	layout.outerWidth = min(agentsInvMaxOuterWidth, width)
	layout.bodyWidth = layout.outerWidth - card.GetHorizontalFrameSize()
	layout.frameHeight = card.GetVerticalFrameSize()
	if layout.bodyWidth <= 0 {
		return layout
	}
	layout.title = renderAgentsInvTextLines(th.Style("askTitle"), "Agent definitions", layout.bodyWidth)
	layout.footer = renderAgentsInvTextLines(th.Style("muted"), fmt.Sprintf(agentsInvFooter, hk.scroll, hk.closeOnly), layout.bodyWidth)
	layout.bodyCapacity = height - layout.frameHeight - len(layout.title) - len(layout.footer) - 2
	layout.normal = layout.bodyCapacity >= 1
	return layout
}

func (l agentsInvLayout) viewportHeight(total int) (int, bool) {
	if !l.normal {
		return 0, false
	}
	if total <= l.bodyCapacity {
		return l.bodyCapacity, true
	}
	if l.bodyCapacity < 2 {
		return 0, false
	}
	return l.bodyCapacity - 1, true
}

func renderAgentsInvTextLines(style lipgloss.Style, text string, width int) []string {
	if width <= 0 {
		return nil
	}
	plain := strings.Split(ansi.Hardwrap(text, width, true), "\n")
	lines := make([]string, 0, len(plain))
	for _, line := range plain {
		lines = append(lines, style.Render(line))
	}
	return lines
}

func renderAgentsInvCompact(th theme.Theme, hk helpKeys, width int) string {
	if width <= 0 {
		return ""
	}
	line := th.Style("muted").Render(hk.closeOnly + " close")
	return ansi.Cut(line, 0, width) + "\x1b[0m"
}

func renderAgentsInvNormalBody(th theme.Theme, st agentsInvState, caps client.Capabilities, layout agentsInvLayout) (string, bool) {
	if !layout.normal {
		return "", false
	}
	var body []string
	switch {
	case st.loading:
		body = renderAgentsInvTextLines(th.Style("muted"), "loading…", layout.bodyWidth)
	case st.err != nil:
		body = renderAgentsInvTextLines(th.Style("errorText"), "list agents: "+terminaltext.Sanitize(st.err.Error()), layout.bodyWidth)
	case len(st.agents) == 0:
		body = renderAgentsInvTextLines(th.Style("muted"), agentsInvEmptyCopy(caps), layout.bodyWidth)
	default:
		rows := agentsInvRowLines(th, st.agents, layout.bodyWidth)
		height, ok := layout.viewportHeight(len(rows))
		if !ok {
			return "", false
		}
		viewport := st.viewport
		if viewport == nil {
			viewport = newAgentsInvViewport()
		}
		viewport.SetGeometry(layout.bodyWidth, height, 0, bounded.Clip)
		view := viewport.View(rows)
		body = append(body, view.Rows...)
		if view.Above > 0 || view.Below > 0 {
			indicator := fmt.Sprintf("lines %d–%d of %d", view.Above+1, len(rows)-view.Below, len(rows))
			line := th.Style("muted").Render(indicator)
			body = append(body, ansi.Cut(line, 0, layout.bodyWidth)+"\x1b[0m")
		}
	}
	if len(body) > layout.bodyCapacity {
		body = body[:layout.bodyCapacity]
	}
	lines := append([]string(nil), layout.title...)
	lines = append(lines, "")
	lines = append(lines, body...)
	lines = append(lines, "")
	lines = append(lines, layout.footer...)
	return strings.Join(lines, "\n"), true
}

// agentsInvDisabledNote is the empty-inventory copy when agent definitions are
// NOT enabled on the connected server (caps.Agents == false), with the remedy.
// Like the skills panel, the relayed caps let the ui distinguish "not enabled"
// from "enabled but empty", which a ui-local guess never could for an external
// server.
const agentsInvDisabledNote = "agent definitions are not enabled on this server.\n" +
	"Add .claude/agents/*.md (or pass --agents-dir) and reconnect."

// agentsInvEmptyCopy returns the empty-state line: the "not enabled" note (with
// remedy) when caps.Agents is false, else the "enabled but empty" note.
func agentsInvEmptyCopy(caps client.Capabilities) string {
	if !caps.Agents {
		return agentsInvDisabledNote
	}
	return "no agent definitions resolved on this server."
}

// agentMetaLine renders one definition's dim metadata line: "model · perm:<mode>
// · tools:a,b,c", omitting empty fields gracefully (an empty model / mode / tool
// scope simply drops its segment). The colour field is intentionally NOT shown
// here — colour is a UX hint only and never affects layout. All segments are
// terminal-sanitized. Returns "" when nothing is known (so the caller can skip
// the line entirely).
func agentMetaLine(a client.Agent) string {
	var segs []string
	if a.Model != "" {
		segs = append(segs, "model:"+terminaltext.Sanitize(a.Model))
	}
	if a.PermissionMode != "" {
		segs = append(segs, "perm:"+terminaltext.Sanitize(a.PermissionMode))
	}
	if len(a.Tools) > 0 {
		sane := make([]string, 0, len(a.Tools))
		for _, t := range a.Tools {
			sane = append(sane, terminaltext.Sanitize(t))
		}
		segs = append(segs, "tools:"+strings.Join(sane, ","))
	}
	return strings.Join(segs, " · ")
}

// agentsInvRowLines builds the rendered (ANSI-carrying) inventory body rows: per
// definition, the routing name (toolName style, tinted with the def's colour
// hint when it maps to a supported ANSI colour), the indented word-wrapped
// description lines, then the dim metadata lines (model · perm · tools). EVERY
// server-derived string is terminal-sanitized BEFORE styling, so the rows are
// safe inputs for the bounded viewport (which must not re-sanitize — that
// would strip the styling). Multi-line renders are split into complete styled
// lines (lipgloss emits per-line SGR sequences), so the scroll window can slice
// anywhere without severing an escape.
func agentsInvRowLines(th theme.Theme, agents []client.Agent, budget int) []string {
	var lines []string
	for _, a := range agents {
		name := renderToolCardText(agentNameStyle(th, a.Color), terminaltext.Sanitize(a.Name), budget)
		lines = append(lines, strings.Split(name, "\n")...)
		if a.Description != "" {
			desc := th.Style("toolArgs").Render(indentWrap(terminaltext.Sanitize(a.Description), budget))
			lines = append(lines, strings.Split(desc, "\n")...)
		}
		if meta := agentMetaLine(a); meta != "" {
			ml := th.Style("muted").Render(indentWrap(meta, budget))
			lines = append(lines, strings.Split(ml, "\n")...)
		}
	}
	return lines
}

// renderAgentsInvPanel is the unframed compatibility seam used by focused row
// tests. Production rendering goes through renderAgentsInvOverlay so width and
// height share one measured geometry path.
func renderAgentsInvPanel(th theme.Theme, st agentsInvState, caps client.Capabilities, hk helpKeys, width int) string {
	layout := newAgentsInvLayout(th, hk, width, 1<<20)
	body, ok := renderAgentsInvNormalBody(th, st, caps, layout)
	if !ok {
		return renderAgentsInvCompact(th, hk, width)
	}
	return body
}
