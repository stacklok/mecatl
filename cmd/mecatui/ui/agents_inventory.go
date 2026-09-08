package ui

import (
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
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
// ctrl+a), which shows a team that has actually run. Activation stays the
// model's run-path concern; the panel is discovery only.
type agentsInvView int

const (
	agentsInvNone  agentsInvView = iota // overlay closed
	agentsInvPanel                      // read-only inventory (name + description + metadata)
)

// agentsInvBodyLines is the fixed number of inventory rows the panel shows at
// once (the scroll window). A fixed budget keeps the panel — and its goldens —
// deterministic regardless of terminal height (the /soul soulBodyLines
// convention). A long inventory scrolls; a short one shows in full with no
// scroll indicator.
const agentsInvBodyLines = 14

// agentsInvState holds the agent-definition inventory overlay state on the
// Model. It is value-embedded so the Model stays a plain struct that Update
// copies. The agents slice is replaced wholesale on each RPC result (never
// mutated in place) so the value-copy semantics hold. scroll is the 0-based
// index of the first visible rendered row (clamped in the key handlers, reset
// on each RPC result).
type agentsInvState struct {
	view    agentsInvView
	loading bool  // the ListAgents RPC is in flight
	err     error // the ListAgents error, rendered distinctly (nil on success)
	agents  []client.Agent
	scroll  int // first visible rendered body row (clamped in the key handlers)
}

// openAgentsInv opens the inventory panel and fires the ListAgents RPC. Only
// callable while idle and when an agents lister is wired; returns the model
// unchanged otherwise. The result arrives as a client.AgentsMsg handled in
// updateAgentsInvMsg.
func (m Model) openAgentsInv() (tea.Model, tea.Cmd) {
	if m.phase != phaseIdle || m.deps.Agents == nil {
		return m, nil
	}
	m.prompt.Blur() // overlay owns the keyboard while open
	m.agentsInv = agentsInvState{view: agentsInvPanel, loading: true}
	return m, client.ListAgentsCmd(m.deps.Ctx, m.deps.Agents)
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
	switch {
	case key.Matches(msg, m.keys.Close):
		mm, cmd := m.closeAgentsInv()
		return mm, cmd, true
	case key.Matches(msg, m.keys.ScrollD), key.Matches(msg, m.keys.Down):
		m.agentsInv.scroll = clampScroll(m.agentsInv.scroll+1, m.agentsInvRowTotal(), agentsInvBodyLines)
		return m, nil, true
	case key.Matches(msg, m.keys.ScrollU), key.Matches(msg, m.keys.Up):
		m.agentsInv.scroll = clampScroll(m.agentsInv.scroll-1, m.agentsInvRowTotal(), agentsInvBodyLines)
		return m, nil, true
	case key.Matches(msg, m.keys.ScrollBottom):
		m.agentsInv.scroll = maxScrollOffset(m.agentsInvRowTotal(), agentsInvBodyLines)
		return m, nil, true
	case key.Matches(msg, m.keys.ScrollTop):
		m.agentsInv.scroll = 0
		return m, nil, true
	}
	return m, nil, true
}

// agentsInvRowTotal is the rendered body-row count the key handlers clamp the
// scroll offset against — computed from the SAME row builder the render path
// windows (agentsInvRowLines at the model's current wrap budget), so the clamp
// and the window can never disagree about the line count.
func (m Model) agentsInvRowTotal() int {
	return len(agentsInvRowLines(m.deps.Theme, m.agentsInv.agents, cardTextWidth(m.width)))
}

// updateAgentsInvMsg reduces a client.AgentsMsg into the overlay state. It fires
// no follow-up command (the panel is a single-shot read), so it returns only the
// model + handled flag; handled=false for any other message so Update can fall
// through.
func (m Model) updateAgentsInvMsg(msg tea.Msg) (tea.Model, bool) {
	am, ok := msg.(client.AgentsMsg)
	if !ok {
		return m, false
	}
	m.agentsInv.loading = false
	if am.Err != nil {
		m.agentsInv.err = am.Err
		return m, true
	}
	m.agentsInv.err = nil
	m.agentsInv.agents = am.Agents
	m.agentsInv.scroll = 0
	return m, true
}

// renderAgentsInvOverlay draws the inventory panel centred over the conversation
// region via centerCard (the same bordered-card treatment as the skills/MCP
// overlays). All server-derived strings are terminal-sanitized.
func renderAgentsInvOverlay(th theme.Theme, st agentsInvState, caps client.Capabilities, hk helpKeys, width, height int) string {
	if st.view != agentsInvPanel {
		return ""
	}
	return centerCard(th, renderAgentsInvPanel(th, st, caps, hk, width), width, height)
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
		segs = append(segs, "model:"+sanitizeTerminal(a.Model))
	}
	if a.PermissionMode != "" {
		segs = append(segs, "perm:"+sanitizeTerminal(a.PermissionMode))
	}
	if len(a.Tools) > 0 {
		sane := make([]string, 0, len(a.Tools))
		for _, t := range a.Tools {
			sane = append(sane, sanitizeTerminal(t))
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
// safe inputs for windowRenderedLines (which must not re-sanitize — that would
// strip the styling). The multi-line renders are split per line (lipgloss emits
// complete per-line SGR sequences) so the scroll window can slice anywhere
// without severing an escape.
func agentsInvRowLines(th theme.Theme, agents []client.Agent, budget int) []string {
	var lines []string
	for _, a := range agents {
		lines = append(lines, renderToolCardText(agentNameStyle(th, a.Color), sanitizeTerminal(a.Name), budget))
		if a.Description != "" {
			desc := th.Style("toolArgs").Render(indentWrap(sanitizeTerminal(a.Description), budget))
			lines = append(lines, strings.Split(desc, "\n")...)
		}
		if meta := agentMetaLine(a); meta != "" {
			ml := th.Style("muted").Render(indentWrap(meta, budget))
			lines = append(lines, strings.Split(ml, "\n")...)
		}
	}
	return lines
}

// renderAgentsInvPanel renders the read-only inventory: one row per definition
// (name + description + metadata — see agentsInvRowLines for the row anatomy and
// the sanitize/colour invariants), scroll-windowed to agentsInvBodyLines with a
// "lines X–Y of N" indicator when the inventory overflows. Colour is a UX hint
// only and never affects layout — it only tints the already-rendered name
// foreground, so a stripANSI'd row is colour-invariant.
func renderAgentsInvPanel(th theme.Theme, st agentsInvState, caps client.Capabilities, hk helpKeys, width int) string {
	var b strings.Builder
	b.WriteString(th.Style("askTitle").Render("Agent definitions") + "\n\n")

	budget := cardTextWidth(width)
	switch {
	case st.loading:
		b.WriteString(th.Style("muted").Render("loading…") + "\n")
	case st.err != nil:
		line := "list agents: " + sanitizeTerminal(st.err.Error())
		if budget > 0 {
			line = ansi.Wrap(line, budget, "")
		}
		b.WriteString(th.Style("errorText").Render(line) + "\n")
	case len(st.agents) == 0:
		b.WriteString(th.Style("muted").Render(agentsInvEmptyCopy(caps)) + "\n")
	default:
		b.WriteString(windowRenderedLines(th, agentsInvRowLines(th, st.agents, budget), st.scroll, agentsInvBodyLines))
	}

	// The scroll pair (ScrollU/ScrollD) and the close chord (Close) read the LIVE
	// keyMap markings (issue #457); with defaults the hint is byte-identical to
	// the historical literal.
	b.WriteString("\n" + th.Style("muted").Render("agent definitions route Subagent delegations · "+hk.scroll+" scroll · "+hk.closeOnly+" close"))
	return b.String()
}
