package ui

import (
	"strings"

	"charm.land/bubbles/v2/key"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
	"github.com/stacklok/mecatl/cmd/mecatui/ui/platform"
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
// action column aligns. It accommodates the platform-adaptive scroll marking
// (up to ~21 chars on Mac, e.g. "fn+↑/fn+↓ (pgup/pgdn)") plus the standard
// chords (e.g. "shift+enter"); the help overlay card has ample width, so
// widening just shifts the action column right uniformly.
const helpKeyWidth = 22

// renderHelpOverlay draws the "?" keys-&-features overlay centred over the
// conversation region, reusing the askCard + lipgloss.Place treatment the MCP
// and agents overlays use. Every availability decision reads the relayed caps
// (not a ui-local guess), so the same overlay honestly reflects an embedded
// default (mcp/commands/skills off) and an external mecated with everything on.
// hk carries the LIVE key markings so a rebinding propagates here.
func renderHelpOverlay(th theme.Theme, caps client.Capabilities, width, height int, hk helpKeys) string {
	return centerCard(th, helpBody(th, caps, hk), width, height)
}

// helpBody builds the overlay's text: a title, grouped chord sections (each row
// caps-annotated), the skills clarification, and the close hint.
// hk carries the LIVE key markings from the model's keyMap, so a rebinding
// propagates here.
func helpBody(th theme.Theme, caps client.Capabilities, hk helpKeys) string {
	muted := th.Style("muted")
	var b strings.Builder

	b.WriteString(th.Style("askTitle").Render("mecatui — keys & features") + "\n\n")

	b.WriteString(muted.Render("Prompting") + "\n")
	writeHelpRows(&b, th, []helpRow{
		{key: hk.submit, action: "send the prompt"},
		{key: hk.newlineFirst, action: "newline" + hk.newlineAlso},
		{key: "/", action: "slash-command palette (built-ins always; workspace commands when enabled)"},
		{key: "@", action: "attach a file: image/audio inlines as media (when supported), else inlines text"},
		{key: hk.paste, action: "paste a clipboard image as an attachment (when supported), else paste text"},
		{key: hk.cancel, action: "cancel the running turn"},
	})

	b.WriteString("\n" + muted.Render("While a run is streaming") + "\n")
	writeHelpRows(&b, th, []helpRow{
		{key: hk.submit, action: "queue a follow-up (sends when the turn ends)"},
		{key: hk.cancel, action: "clear staged input / queue, else cancel run"},
	})

	b.WriteString("\n" + muted.Render("Inspect & control") + "\n")
	writeHelpRows(&b, th, []helpRow{
		{key: hk.mcpPanel, action: "MCP inventory", available: caps.MCP, gated: true},
		{key: hk.resources, action: "MCP resources", available: caps.MCP, gated: true},
		{key: hk.prompts, action: "MCP prompts", available: caps.MCP, gated: true},
		{key: hk.agents, action: "agents overlay (subagents / parallel / teams · " + hk.nextTab + " to switch)"},
		{key: hk.effort, action: "reasoning-effort picker", available: caps.ModelSelection, gated: true},
		{key: "/schedule", action: "browse & manage scheduled tasks", available: caps.Scheduling, gated: true},
		{key: "/sessions", action: "open a stored session (read-only transcript)"},
		{key: hk.modeSwitch, action: "cycle permission mode (default / plan / accept-edits)"},
		{key: hk.expandTools, action: "expand/collapse details"},
	})

	b.WriteString("\n" + muted.Render("General") + "\n")
	writeHelpRows(&b, th, []helpRow{
		{key: hk.scroll, action: "scroll the conversation (a ↑NN% header cue shows while scrolled up)"},
		{key: hk.jump, action: "jump to top / bottom (" + hk.scrollBottom + " resumes auto-follow)"},
		{key: "wheel", action: "mouse-wheel scroll (alt screen only)"},
		{key: "drag", action: "select text · drag to an edge auto-scrolls · copies on release · double-click word · triple-click line · right-click copies · " + hk.cancel + " clears"},
		{key: "middle-click", action: "paste the primary selection into the prompt (X11/Wayland; shift+middle-click pastes via the terminal instead)"},
		{key: hk.help, action: "this help (on an empty prompt)"},
		{key: hk.suspend, action: "suspend to the shell — the engine keeps running; fg resumes"},
		{key: hk.quit, action: "quit (press twice; first press clears the prompt or arms, again within 3s exits)"},
		{key: hk.quitD, action: "quit (EOF habit; press twice on an empty prompt)"},
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

	// Close hint. Sourced LIVE from the Close/Help bindings (the two keys that
	// actually dismiss this overlay — see the m.showHelp gate in update.go), NOT a
	// hardcoded "esc or ?": an operator can remap either, and a stale hint would
	// lie about how to close. With default keys it renders exactly "esc or ?".
	b.WriteString("\n" + muted.Render(hk.close+" to close"))
	return b.String()
}

// helpKeys is the set of pre-computed chord markings helpBody renders for the
// REBINDABLE rows — one field per rebindable action that appears in the help
// body. Keeping the renderer string-driven (not keyMap-driven) means the body
// builder stays pure, and the welcome card reuses the same struct for its
// live affordance rows. With DEFAULT keys every field resolves to exactly the
// literal it replaced, so the goldens stay byte-identical.
//
// The approval markings (allow/allowAlways/deny) are carried here too even
// though the help body does not render them as rows: the footer help line and
// the permission-modal/plan-review action bars read them off the SAME struct
// (via the renderer's copy) so a rebinding propagates to those affordances
// without a second markings path (issue #457 — the #455 liveness pattern
// extended to the footer + inline cards).
type helpKeys struct {
	submit       string // Submit — send the prompt / queue a follow-up
	newlineFirst string // Newline — first chord of the binding
	newlineAlso  string // Newline — static " (also X)" suffix for remaining chords ("" when none)
	paste        string // Paste
	cancel       string // Cancel — cancel the running turn / clear staged input & queue
	editBack     string // EditBack — pull the queued follow-up back into the textarea
	quit         string // Quit
	quitD        string // QuitD — the EOF-habit quit (double-press, empty prompt only)
	suspend      string // Suspend — suspend the TUI to the shell (fg resumes)
	help         string // Help — this overlay
	mcpPanel     string // MCPPanel
	resources    string // Resources
	prompts      string // Prompts
	agents       string // Agents
	effort       string // Effort
	modeSwitch   string // ModeSwitch
	expandTools  string // ExpandTools
	scroll       string // ScrollU/ScrollD — platform-adaptive on the default, "<up>/<down>" once remapped
	scrollUp     string // ScrollU — first chord, for compact one-sided paging hints
	scrollBottom string // ScrollBottom — first chord, for auto-follow prose
	jump         string // ScrollTop/ScrollBottom joined as "home/end"
	close        string // Close/Help joined as "esc or ?" — the keys that dismiss this overlay
	allow        string // Allow — the permission-modal allow-once key (approval)
	allowAlways  string // AllowAlways — the permission-modal always-allow key (approval)
	deny         string // Deny — the permission-modal deny key (approval)

	// Overlay-navigation markings (issue #457). The agents/team/mcp/effort/models/
	// sessions/worktrees/schedule/skills/soul/usermodel overlays render inline hints
	// whose chords are backed by rebindable keyMap actions; these carry the LIVE
	// first chord (or, for JumpTop/JumpEnd, the full joined help key) so an override
	// propagates to the displayed hint. With DEFAULT keys each resolves to exactly
	// the literal it replaced, so the goldens stay byte-identical.
	choose           string // Choose — the list "select"/"focus"/"apply" key (enter)
	nextTab          string // NextTab — the in-overlay tab switch (tab)
	jumpTop          string // JumpTop — first chord (home)
	jumpEnd          string // JumpEnd — first chord (end)
	jumpTopFull      string // JumpTop — full joined key (home/g)
	jumpEndFull      string // JumpEnd — full joined key (end/G)
	cancelChild      string // CancelChild — cancel the selected subagent/branch/member (x)
	tasks            string // Tasks — team overlay tasks sub-view (t)
	findings         string // Findings — team overlay findings sub-view (f)
	refresh          string // Refresh — MCP inventory re-probe (r)
	setGlobalDefault string // SetGlobalDefault — models picker set-global-default (ctrl+g)
	closeOnly        string // Close — the bare close chord (esc), distinct from `close` ("esc or ?")
	navUp            string // Up — humanized first chord (↑ for the default "up", else the chord)
	navDown          string // Down — humanized first chord (↓ for the default "down", else the chord)
}

// firstKey returns the first chord of b, or def when the binding is empty
// (by construction a default binding always has at least one key).
func firstKey(b key.Binding, def string) string {
	if keys := b.Keys(); len(keys) > 0 {
		return keys[0]
	}
	return def
}

// fullKey returns the binding's full joined help key (all chords joined by "/"),
// or def when the binding is empty. Used for the JumpTop/JumpEnd overlay hints
// that render the FULL key set ("home/g · end/G") rather than just the first
// chord (issue #457).
func fullKey(b key.Binding, def string) string {
	if h := b.Help(); h.Key != "" {
		return h.Key
	}
	if keys := b.Keys(); len(keys) > 0 {
		return strings.Join(keys, "/")
	}
	return def
}

// navGlyph humanizes an Up/Down list-nav chord for the overlay hints: the
// DEFAULT "up"/"down" chords render as the arrow glyphs "↑"/"↓" (the compact
// form the overlays have always used); any other chord (a rebind to "k", a
// modified chord) renders verbatim so an override is advertised honestly. It is
// ONLY applied to the Up/Down list-nav bindings (NOT the token-direction arrows
// in the footer/subagent lines, which are literal glyphs, not key bindings)
// (issue #457).
func navGlyph(chord string) string {
	switch chord {
	case keyMenuUp:
		return "↑"
	case keyMenuDown:
		return "↓"
	default:
		return chord
	}
}

// helpKeyMarkings builds the help-body key markings from the model's LIVE
// keyMap, so a rebinding propagates into the "?" overlay.
func (m Model) helpKeyMarkings() helpKeys { return keyMarkings(m.keys) }

// defaultHelpKeys builds the help markings from the DEFAULT bindings — the
// honest fixture for tests that don't wire custom keymaps.
func defaultHelpKeys() helpKeys { return keyMarkings(defaultKeys()) }

// keyMarkings derives the help-body key markings from km: each rebindable row
// shows the binding's first chord; the scroll pair joins ScrollTop/ScrollBottom
// as "home/end"; the newline row keeps its static " (also …)" suffix for any
// remaining chords. The approval keys (allow/allowAlways/deny) are populated
// too so the footer help line and the permission/plan-review action bars read
// the LIVE chords from the same struct (issue #457).
func keyMarkings(km keyMap) helpKeys {
	hk := helpKeys{
		submit:       firstKey(km.Submit, "enter"),
		paste:        firstKey(km.Paste, "ctrl+v"),
		cancel:       firstKey(km.Cancel, "esc"),
		editBack:     navGlyph(firstKey(km.EditBack, "up")),
		quit:         firstKey(km.Quit, "ctrl+c"),
		quitD:        firstKey(km.QuitD, "ctrl+d"),
		suspend:      firstKey(km.Suspend, "ctrl+z"),
		help:         firstKey(km.Help, "?"),
		mcpPanel:     firstKey(km.MCPPanel, "ctrl+o"),
		resources:    firstKey(km.Resources, "ctrl+r"),
		prompts:      firstKey(km.Prompts, "ctrl+p"),
		agents:       firstKey(km.Agents, "ctrl+a"),
		effort:       firstKey(km.Effort, "ctrl+e"),
		modeSwitch:   firstKey(km.ModeSwitch, "alt+m"),
		expandTools:  firstKey(km.ExpandTools, "ctrl+t"),
		scroll:       scrollMarking(km),
		scrollUp:     firstKey(km.ScrollU, "pgup"),
		scrollBottom: firstKey(km.ScrollBottom, "end"),
		jump:         firstKey(km.ScrollTop, "home") + "/" + firstKey(km.ScrollBottom, "end"),
		close:        firstKey(km.Close, "esc") + " or " + firstKey(km.Help, "?"),
		allow:        firstKey(km.Allow, "a"),
		allowAlways:  firstKey(km.AllowAlways, "w"),
		deny:         firstKey(km.Deny, "d"),

		choose:           firstKey(km.Choose, "enter"),
		nextTab:          firstKey(km.NextTab, "tab"),
		jumpTop:          firstKey(km.JumpTop, "home"),
		jumpEnd:          firstKey(km.JumpEnd, "end"),
		jumpTopFull:      fullKey(km.JumpTop, "home/g"),
		jumpEndFull:      fullKey(km.JumpEnd, "end/G"),
		cancelChild:      firstKey(km.CancelChild, "x"),
		tasks:            firstKey(km.Tasks, "t"),
		findings:         firstKey(km.Findings, "f"),
		refresh:          firstKey(km.Refresh, "r"),
		setGlobalDefault: firstKey(km.SetGlobalDefault, "ctrl+g"),
		closeOnly:        firstKey(km.Close, "esc"),
		navUp:            navGlyph(firstKey(km.Up, keyMenuUp)),
		navDown:          navGlyph(firstKey(km.Down, keyMenuDown)),
	}
	nl := km.Newline.Keys()
	if len(nl) > 0 {
		hk.newlineFirst = nl[0]
		if len(nl) > 1 {
			hk.newlineAlso = " (also " + strings.Join(nl[1:], ", ") + ")"
		}
	} else {
		hk.newlineFirst = "shift+enter"
		hk.newlineAlso = " (also ctrl+j)"
	}
	return hk
}

// scrollMarking renders the ScrollU/ScrollD row. While the pair still holds the
// DEFAULT pgup/pgdown chords it keeps the platform-adaptive marking
// (platform.ScrollKeysMarking — "fn+↑/fn+↓ (pgup/pgdn)" on macOS, "pgup/pgdn"
// elsewhere), so the default help body stays byte-identical; once either half is
// remapped the platform gesture no longer applies, so the row shows the live
// "<scrollU>/<scrollD>" chords instead.
func scrollMarking(km keyMap) string {
	up, down := km.ScrollU.Keys(), km.ScrollD.Keys()
	if len(up) == 1 && up[0] == "pgup" && len(down) == 1 && down[0] == "pgdown" {
		return platform.ScrollKeysMarking()
	}
	return firstKey(km.ScrollU, "pgup") + "/" + firstKey(km.ScrollD, "pgdown")
}

// writeHelpRows renders a group of chord rows. An available (or ungated) row uses
// the normal toolArgs style; an unavailable gated row is greyed and tagged
// notEnabledTag so its disabledness reads at a glance.
func writeHelpRows(b *strings.Builder, th theme.Theme, rows []helpRow) {
	for _, r := range rows {
		keyCol := r.key + strings.Repeat(" ", max(0, helpKeyWidth-len(r.key)))
		line := "  " + keyCol + r.action
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

	hk := m.helpKeyMarkings()
	in := welcome.Info{
		Cwd:         m.deps.Workspace,
		Model:       m.zeroStateModelName(),
		Provider:    m.activeModel.ProviderID, // "" when no selection yet → modelLine omits it
		Version:     m.deps.Version,
		Tagline:     "your local agentic coding harness",
		Submit:      hk.submit,
		Affordances: m.zeroStateAffordanceRows(),
		MemoryNote:  m.zeroStateMemoryNote(),
		GatewayNote: m.zeroStateGatewayNote(),
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
	hk := m.helpKeyMarkings()
	b.WriteString(th.Style("toolArgs").Render("  Type a request below and press "+hk.submit+".") + "\n\n")
	writeHelpRows(&b, th, zeroStateRows(hk))
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
	writeHelpRows(&b, m.deps.Theme, zeroStateRows(m.helpKeyMarkings()))
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

// zeroStateGatewayNote is the gateway-detected welcome line (N2): a muted
// "ToolHive gateway detected (no API key needed) — /models" when an
// intent-driven provider is available-but-not-default, or "" otherwise. The
// splash only renders at phaseIdle (post-connect), by which point the first
// ModelsMsg has landed and m.models.statuses is populated — so this catches a
// new operator at the moment they're most attentive. Vendor-neutral: the
// provider id comes from the status row, so a future non-ToolHive
// intent-driven provider reads naturally without a code change here.
func (m Model) zeroStateGatewayNote() string {
	row, ok := availableNotDefaultStatus(m.models.statuses)
	if !ok {
		return ""
	}
	return m.deps.Theme.Style("muted").Render(
		"  " + sanitizeTerminal(row.ProviderID) + " gateway detected (no API key needed) — /models")
}

// zeroStateModelName picks the model id shown on the splash: the picker's active
// selection once set, else the launch-time --model display (mirrors the header).
func (m Model) zeroStateModelName() string {
	if name := m.activeModel.ModelID; name != "" {
		return sanitizeTerminal(name)
	}
	return m.deps.Model
}

// zeroStateRows is the affordance list on the welcome card. Every row is
// UNCONDITIONAL — "/" (built-in commands always exist) plus the help / agents /
// details rows, whose keys come from hk so a rebinding propagates here (with
// DEFAULT keys each resolves to exactly the historical literal — "?", "ctrl+a",
// "ctrl+t" — so the zerostate goldens stay byte-identical). It takes no caps
// argument; the caps-conditional welcome content (the memory note) lives in
// renderZeroState. Rows are rendered ungated (no [not enabled] tags on the
// welcome card — it advertises only what's on).
func zeroStateRows(hk helpKeys) []helpRow {
	return []helpRow{
		{key: hk.help, action: "keys & features"},
		{key: "/", action: "slash commands"},
		{key: hk.agents, action: "agents (when running)"},
		{key: hk.expandTools, action: "details"},
	}
}
