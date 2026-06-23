package ui

import (
	"strings"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
	"github.com/stacklok/mecatl/cmd/mecatui/ui/welcome"
)

// notEnabledTag is the muted annotation appended to a chord row whose feature is
// not enabled on the connected server (per the relayed client.Capabilities). It
// replaces the old undecodable "ctrl+o/r/p MCP" footer mnemonic with an honest,
// per-feature availability cue.
const notEnabledTag = "[not enabled]"

// helpRow is one line of the decompressed chord table: a key, the action it
// performs, and whether the feature it reaches is currently available. An
// unavailable row is greyed (muted style) and tagged notEnabledTag so the user
// learns the chord exists AND that it is off on this server — the whole point of
// the Phase-A capabilities channel.
type helpRow struct {
	key       string
	action    string
	available bool // false → greyed + tagged; true → normal style
	gated     bool // false → always-available row (no caps gate, never tagged)
}

// helpKeyWidth is the fixed column width the chord keys are padded to so the
// action column aligns. It comfortably fits the widest key ("shift+enter").
const helpKeyWidth = 14

// renderHelpOverlay draws the "?" keys-&-features overlay centred over the
// conversation region, reusing the askCard + lipgloss.Place treatment the MCP
// and agents overlays use. Every availability decision reads the relayed caps
// (not a ui-local guess), so the same overlay honestly reflects an embedded
// default (mcp/commands/skills off) and an external mecated with everything on.
func renderHelpOverlay(th theme.Theme, caps client.Capabilities, width, height int) string {
	return centerCard(th, helpBody(th, caps), width, height)
}

// helpBody builds the overlay's text: a title, grouped chord sections (each row
// caps-annotated), the skills clarification, and the close hint.
func helpBody(th theme.Theme, caps client.Capabilities) string {
	muted := th.Style("muted")
	var b strings.Builder

	b.WriteString(th.Style("askTitle").Render("mecatui — keys & features") + "\n\n")

	b.WriteString(muted.Render("Prompting") + "\n")
	writeHelpRows(&b, th, []helpRow{
		{key: "enter", action: "send the prompt"},
		{key: "shift+enter", action: "newline (also ctrl+j)"},
		{key: "/", action: "slash-command palette (built-ins always; workspace commands when enabled)"},
		{key: "@", action: "attach a file: image/audio inlines as media (when supported), else inlines text"},
		{key: "ctrl+v", action: "paste a clipboard image as an attachment (when supported), else paste text"},
		{key: "esc", action: "cancel the running turn"},
	})

	b.WriteString("\n" + muted.Render("While a run is streaming") + "\n")
	writeHelpRows(&b, th, []helpRow{
		{key: "enter", action: "queue a follow-up (sends when the turn ends)"},
		{key: "esc", action: "clear staged input / queue, else cancel run"},
	})

	b.WriteString("\n" + muted.Render("Inspect & control") + "\n")
	writeHelpRows(&b, th, []helpRow{
		{key: "ctrl+o", action: "MCP inventory", available: caps.MCP, gated: true},
		{key: "ctrl+r", action: "MCP resources", available: caps.MCP, gated: true},
		{key: "ctrl+p", action: "MCP prompts", available: caps.MCP, gated: true},
		{key: "ctrl+a", action: "agents overlay (subagents / parallel / teams · tab to switch)"},
		{key: "alt+m", action: "cycle permission mode (default / plan / accept-edits)"},
		{key: "ctrl+t", action: "expand/collapse details"},
	})

	b.WriteString("\n" + muted.Render("General") + "\n")
	writeHelpRows(&b, th, []helpRow{
		{key: "pgup/pgdn", action: "scroll the conversation (a ↑NN% header cue shows while scrolled up)"},
		{key: "home/end", action: "jump to top / bottom (end resumes auto-follow)"},
		{key: "wheel", action: "mouse-wheel scroll (alt screen only)"},
		{key: "drag", action: "select text · drag to an edge auto-scrolls · copies on release · double-click word · triple-click line · right-click copies · esc clears"},
		{key: "middle-click", action: "paste the primary selection into the prompt (X11/Wayland; shift+middle-click pastes via the terminal instead)"},
		{key: "?", action: "this help (on an empty prompt)"},
		{key: "ctrl+c", action: "quit (press twice; first press clears the prompt or arms, again within 3s exits)"},
	})

	// The skills clarification. Skills always ACTIVATE automatically (the model
	// decides when, not the user), but when the server advertises Skills the
	// inventory IS browsable via /skills — so the copy is caps-aware: it points at
	// /skills when enabled, and keeps the "run automatically, not browsable" framing
	// when skills are off (nothing to browse).
	b.WriteString("\n")
	if caps.Skills {
		b.WriteString(muted.Render(
			"Skills activate automatically when the model needs them; type /skills\n"+
				"to browse the skills inventory.") + "\n")
	} else {
		b.WriteString(muted.Render(
			"Skills run automatically when the model needs them — not a browsable\n"+
				"list; watch the transcript for Skill tool calls.") + "\n")
	}
	// Agent definitions, when served, are browsable via /agents (the inventory the
	// Subagent tool routes delegations to). Distinct from caps.Teams / ctrl+a, which is
	// the live overlay of a team that has actually run.
	if caps.Agents {
		b.WriteString(muted.Render("Type /agents to browse the agent-definition inventory.") + "\n")
	}
	if caps.SlashCommands {
		b.WriteString(muted.Render("Type / to browse slash commands.") + "\n")
	}
	if caps.Memory {
		b.WriteString(muted.Render("Cross-session memory is on — context carries across runs.") + "\n")
	}
	switch {
	case caps.Image && caps.Audio:
		b.WriteString(muted.Render("Type @ to attach a file — images and audio go to the model as media.") + "\n")
	case caps.Image:
		b.WriteString(muted.Render("Type @ to attach a file — images go to the model as media.") + "\n")
	case caps.Audio:
		b.WriteString(muted.Render("Type @ to attach a file — audio goes to the model as media.") + "\n")
	default:
		b.WriteString(muted.Render("Type @ to attach a file — this model takes text only, so files inline as text.") + "\n")
	}

	// Usage legend: decode the footer/turn-stat token arrows AND the cache percentage,
	// so "↑1.2K ↓340 ⊕1.2K · cache 88%" is self-explanatory — the input/output/cache-write
	// glyphs, plus the share of input tokens served from cache (the number behind a
	// surprisingly large prompt).
	b.WriteString("\n" + muted.Render("↑ input · ↓ output · ⊕ cache write") + "\n")
	b.WriteString(muted.Render("cache N% — share of input tokens served from cache") + "\n")

	b.WriteString("\n" + muted.Render("esc or ? to close"))
	return b.String()
}

// writeHelpRows renders a group of chord rows. An available (or ungated) row uses
// the normal toolArgs style; an unavailable gated row is greyed and tagged
// notEnabledTag so its disabledness reads at a glance.
func writeHelpRows(b *strings.Builder, th theme.Theme, rows []helpRow) {
	for _, r := range rows {
		key := r.key + strings.Repeat(" ", max(0, helpKeyWidth-len(r.key)))
		line := "  " + key + r.action
		if r.gated && !r.available {
			b.WriteString(th.Style("muted").Render(line+"  "+notEnabledTag) + "\n")
		} else {
			b.WriteString(th.Style("toolArgs").Render(line) + "\n")
		}
	}
}

// renderZeroState draws the first-run welcome SPLASH in the EMPTY viewport (when
// the conversation has no blocks yet). It is NOT an overlay — it claims no
// keyboard, so typing / "/" / "?" all flow over it, and it vanishes the instant
// the first block is appended. The body (mascot + gradient wordmark + info block)
// is built by the welcome subpackage; this method assembles the caps-tailored
// welcome.Info from the model and frames the result with centerCard.
//
// Under Deps.NoBanner it short-circuits to the LEGACY plain card (title + prompt
// hint + affordance rows + memory note) with NO mascot or wordmark — the
// quiet/non-interactive/--no-banner path.
func (m Model) renderZeroState() string {
	th := m.deps.Theme
	width, height := m.width, m.vp.Height()

	if m.deps.NoBanner {
		return centerCard(th, m.legacyZeroStateBody(), width, height)
	}

	in := welcome.Info{
		Cwd:         m.deps.Workspace,
		Model:       m.zeroStateModelName(),
		Provider:    m.activeModel.ProviderID, // "" when no selection yet → modelLine omits it
		Version:     m.deps.Version,
		Tagline:     "your local agentic coding harness",
		Affordances: m.zeroStateAffordanceRows(),
		MemoryNote:  m.zeroStateMemoryNote(),
		FullColor:   m.fullColor,
		Kitty:       m.kittyActive,
	}
	return centerCard(th, welcome.Splash(th, in, width, height), width, height)
}

// legacyZeroStateBody is the plain (mascot-less, wordmark-less) welcome body used
// under --no-banner / quiet / non-interactive. It is the historical zero-state
// card content, kept so the suppressed path still advertises the affordances and
// carries the greppable title.
func (m Model) legacyZeroStateBody() string {
	th := m.deps.Theme
	var b strings.Builder
	b.WriteString(th.Style("askTitle").Render("Welcome to mecatui") + "\n\n")
	b.WriteString(th.Style("toolArgs").Render("  Type a request below and press enter.") + "\n\n")
	writeHelpRows(&b, th, zeroStateRows())
	if note := m.zeroStateMemoryNote(); note != "" {
		b.WriteString("\n" + note + "\n")
	}
	return b.String()
}

// zeroStateAffordanceRows renders the caps-tailored affordance chord rows the
// welcome splash lists, reusing the SAME zeroStateRows() + writeHelpRows path as
// the legacy card so the rows stay byte-equivalent in semantics.
func (m Model) zeroStateAffordanceRows() []string {
	var b strings.Builder
	writeHelpRows(&b, m.deps.Theme, zeroStateRows())
	return strings.Split(strings.TrimRight(b.String(), "\n"), "\n")
}

// zeroStateMemoryNote is the caps.Memory-gated "memory is on" line (themed muted),
// or "" when memory is off — the single caps-conditional welcome content,
// preserved byte-equivalently from the legacy card.
func (m Model) zeroStateMemoryNote() string {
	if !m.caps.Memory {
		return ""
	}
	return m.deps.Theme.Style("muted").Render(
		"  Cross-session memory is on — I'll remember context across runs.")
}

// zeroStateModelName picks the model id shown on the splash: the picker's active
// selection once set, else the launch-time --model display (mirrors the header).
func (m Model) zeroStateModelName() string {
	if name := m.activeModel.ModelID; name != "" {
		return sanitizeTerminal(name)
	}
	return m.deps.Model
}

// zeroStateRows is the affordance list on the welcome card. Every row is now
// UNCONDITIONAL — "?" / "/" (built-in commands always exist) / "ctrl+t" were always
// always-on, and "ctrl+a" (the unified agents overlay) is no longer caps-gated because
// subagents are always available via Subagent (teams are the only optional half). So it
// takes no caps argument; the caps-conditional welcome content (the memory note) lives
// in renderZeroState. Rows are rendered ungated (no [not enabled] tags on the welcome
// card — it advertises only what's on).
func zeroStateRows() []helpRow {
	return []helpRow{
		{key: "?", action: "keys & features"},
		{key: "/", action: "slash commands"},
		{key: "ctrl+a", action: "agents (when running)"},
		{key: "ctrl+t", action: "details"},
	}
}
