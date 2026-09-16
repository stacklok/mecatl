package ui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/lipgloss/v2"

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
// action column aligns. It accommodates the platform-adaptive scroll marking
// (up to ~21 chars on Mac, e.g. "fn+↑/fn+↓ (pgup/pgdn)") plus the standard
// chords (e.g. "shift+enter"); the help overlay card has ample width, so
// widening just shifts the action column right uniformly.
const helpKeyWidth = 22

// renderHelpOverlay draws the "?" keys-&-features overlay centred over the
// conversation region. When the body is taller than the offered height, it windows
// complete ANSI lines and reserves a row for its scroll indicator.
func renderHelpOverlay(th theme.Theme, caps client.Capabilities, width, height, scroll int, hk helpKeys) string {
	body := helpBody(th, caps, hk)
	if height <= 0 {
		return centerCard(th, body, width, height)
	}
	lines := helpRenderedLines(body)
	cardChrome := lipgloss.Height(th.Style("askCard").Render(""))
	if height <= cardChrome {
		// A card cannot fit in this exceptionally small viewport. Keep the overlay
		// usable rather than overflowing the conversation region.
		return lines[clampScroll(scroll, len(lines), 1)]
	}
	window := helpWindowHeight(th, height, len(lines))
	scroll = clampScroll(scroll, len(lines), window)
	body = strings.TrimSuffix(windowRenderedLinesWithIndicator(th, lines, scroll, window, func(start, end, total int) string {
		return helpScrollIndicator(hk, start, end, total)
	}), "\n")
	return centerCard(th, body, width, height)
}

// helpScrollIndicator keeps the navigation affordances in every clipped frame,
// including the initial top view where the help body's footer is not visible.
func helpScrollIndicator(hk helpKeys, start, end, total int) string {
	return fmt.Sprintf("lines %d–%d of %d · %s close · %s/%s scroll · %s page · %s jump", start+1, end, total, hk.close, hk.navUp, hk.navDown, hk.scroll, hk.jump)
}

// helpRenderedLines splits the help body into complete styled lines. helpBody
// renders every line independently, so windowing cannot leave a terminal style open.
func helpRenderedLines(body string) []string {
	return strings.Split(body, "\n")
}

// helpWindowHeight accounts for the card chrome and its scroll indicator. The
// indicator replaces one content row only when it is needed, keeping the card within
// the actual conversation viewport.
func helpWindowHeight(th theme.Theme, height, total int) int {
	window := max(1, height-lipgloss.Height(th.Style("askCard").Render("")))
	if total > window {
		window = max(1, window-1)
	}
	return window
}

// helpBody builds the overlay's text: a title, grouped chord sections (each row
// caps-annotated), the skills clarification, and the close hint.
// hk carries the LIVE key markings from the model's keyMap, so a rebinding
// propagates here.
func helpBody(th theme.Theme, caps client.Capabilities, hk helpKeys) string {
	muted := th.Style("muted")
	var b strings.Builder

	b.WriteString(th.Style("askTitle").Render("Help") + "\n\n")

	b.WriteString(muted.Render("Prompting") + "\n")
	writeHelpRows(&b, th, []helpRow{
		{key: hk.submit, action: "send prompt"},
		{key: hk.newlineFirst, action: "insert newline" + hk.newlineAlso},
		{key: "/", action: "open available commands, including workspace commands when enabled"},
		{key: hk.help + " or /help", action: "open this help; the shortcut works when the prompt is empty"},
		{key: "@", action: "attach a file; supported images and audio are sent as media, other files as text"},
		{key: hk.paste, action: "paste a clipboard image, or paste text when image attachments are unavailable"},
		{key: hk.clearPrompt, action: "clear the current draft"},
		{key: "esc, release, esc", action: "clear the current idle draft within 500ms; the first press makes no visible change"},
		{key: "", action: "also clears attachments, large pasted text, and pending media; requires a terminal with enhanced key-event support"},
		{key: "", action: "fixed shortcut; repeats do not count, and other views handle esc first"},
		{key: "", action: "use the remappable Clear prompt action (" + hk.clearPrompt + ") on any terminal"},
		{key: hk.cancel, action: "cancel the current run"},
	})

	b.WriteString("\n" + muted.Render("While a run is active") + "\n")
	streamingSubmit := "queue a follow-up to send after this run"
	if caps.Steer {
		streamingSubmit = "guide the running agent at its next step; built-in commands still run here"
	}
	writeHelpRows(&b, th, []helpRow{
		{key: hk.submit, action: streamingSubmit},
		{key: hk.clearPrompt, action: "clear the current draft"},
		{key: hk.cancel, action: "cancel the current run"},
	})

	b.WriteString("\n" + muted.Render("Permission request") + "\n")
	writeHelpRows(&b, th, []helpRow{
		{key: hk.allow, action: "allow once"},
		{key: hk.allowAlways, action: "always allow for this session (main agent only)"},
		{key: hk.deny, action: "deny"},
		{key: "←/→/tab", action: "choose an action; enter confirms it"},
		{key: hk.expandTools, action: "open full approval details when available"},
		{key: hk.rawArgs, action: "show raw arguments in the full view"},
	})

	b.WriteString("\n" + muted.Render("Inspect and manage") + "\n")
	inspectRows := []helpRow{
		{key: hk.mcpPanel, action: "open MCP servers and tools", available: caps.MCP, gated: true},
		{key: hk.resources, action: "browse MCP resources", available: caps.MCP, gated: true},
		{key: hk.prompts, action: "browse MCP prompts", available: caps.MCP, gated: true},
		{key: hk.agents, action: "inspect agents, parallel work, and teams; " + hk.nextTab + " switches views"},
		{key: hk.effort, action: "choose reasoning effort", available: caps.ModelSelection, gated: true},
		{key: "/schedule", action: "view and manage scheduled tasks", available: caps.Scheduling, gated: true},
	}
	if caps.ManualDream != nil {
		inspectRows = append(inspectRows, helpRow{key: "/dream", action: "review a memory-consolidation proposal (creating it uses tokens)"})
	}
	inspectRows = append(inspectRows,
		helpRow{key: "/session", action: "show the current session and copy its ID"},
		helpRow{key: "/sessions", action: "continue, inspect, or manage stored sessions"},
		helpRow{key: "/connect", action: "sign in or connect to a remote server"},
		helpRow{key: hk.modeSwitch, action: "change permission mode (unavailable while filling an MCP prompt)"},
		helpRow{key: hk.expandTools, action: "show or hide tool details"},
	)
	writeHelpRows(&b, th, inspectRows)

	b.WriteString("\n" + muted.Render("Conversation and navigation") + "\n")
	writeHelpRows(&b, th, []helpRow{
		{key: hk.selectAll, action: "select all prompt text"},
		{key: hk.copySelection, action: "copy selected prompt or conversation text"},
		{key: hk.scroll, action: "scroll the conversation; the header shows your position"},
		{key: hk.jump, action: "jump to top or bottom; " + hk.scrollBottom + " resumes automatic scrolling"},
		{key: "wheel", action: "scroll with the mouse (alternate screen only)"},
		{key: "drag", action: "select and copy conversation text; dragging past an edge scrolls"},
		{key: "double/triple-click", action: "select a word or line; right-click copies; " + hk.cancel + " clears"},
		{key: "middle-click", action: "paste the primary selection (X11/Wayland)"},
	})

	b.WriteString("\n" + muted.Render("Exit and suspend") + "\n")
	writeHelpRows(&b, th, []helpRow{
		{key: "/quit", action: "quit immediately (alias: /exit; cancels an active run)"},
		{key: hk.suspend, action: "suspend to the shell; the run continues, and fg resumes the TUI"},
		{key: hk.quit, action: "quit after two presses; the first clears the prompt or prepares to quit"},
		{key: "", action: "press again within 3 seconds to exit"},
		{key: hk.quitD, action: "quit after two presses on an empty prompt"},
	})

	// The skills clarification. Skills always ACTIVATE automatically (the model
	// decides when, not the user), but when the server advertises Skills the
	// inventory IS browsable via /skills — so the copy is caps-aware: it points at
	// /skills when enabled, and keeps the "run automatically, not browsable" framing
	// when skills are off (nothing to browse).
	b.WriteString("\n" + muted.Render("Features") + "\n")
	if caps.Skills {
		writeHelpMutedLines(&b, th,
			"The agent loads skills when needed. Type /skills to browse available skills.")
	} else {
		writeHelpMutedLines(&b, th,
			"This server does not provide a skills inventory.")
	}
	// Agent definitions, when served, are browsable via /agents (the inventory the
	// Subagent tool routes delegations to). Distinct from caps.Teams / f6, which is
	// the live overlay of a team that has actually run.
	if caps.Agents {
		b.WriteString(muted.Render("Type /agents to browse the agent-definition inventory.") + "\n")
	}
	if caps.SlashCommands {
		b.WriteString(muted.Render("Type / to browse slash commands.") + "\n")
	}
	if caps.Memory {
		b.WriteString(muted.Render("Cross-session memory is enabled.") + "\n")
	}
	switch {
	case caps.Image && caps.Audio:
		b.WriteString(muted.Render("Type @ to attach a file — images and audio go to the model as media.") + "\n")
	case caps.Image:
		b.WriteString(muted.Render("Type @ to attach a file — images go to the model as media.") + "\n")
	case caps.Audio:
		b.WriteString(muted.Render("Type @ to attach a file — audio goes to the model as media.") + "\n")
	default:
		b.WriteString(muted.Render("This model accepts text only; attached files are inserted as text.") + "\n")
	}

	// Usage legend: decode the footer/turn-stat token arrows AND the cache percentage,
	// so "↑1.2K ↓340 ⊕1.2K · cache 88%" is self-explanatory — the input/output/cache-write
	// glyphs, plus the share of input tokens served from cache (the number behind a
	// surprisingly large prompt).
	b.WriteString("\n" + muted.Render("Usage") + "\n")
	b.WriteString(muted.Render("↑ input · ↓ output · ⊕ cache write · cache N% input served from cache") + "\n")

	// The navigation and close affordances use the LIVE bindings, so a keymap
	// override never leaves an unusable scrollable overlay.
	b.WriteString("\n" + muted.Render(hk.close+" close · "+hk.navUp+"/"+hk.navDown+" scroll · "+hk.scroll+" page · "+hk.jump+" jump"))
	return b.String()
}

// writeHelpMutedLines renders each line independently so helpRenderedLines can
// safely window the ANSI output without severing a style sequence.
func writeHelpMutedLines(b *strings.Builder, th theme.Theme, lines ...string) {
	for _, line := range lines {
		b.WriteString(th.Style("muted").Render(line) + "\n")
	}
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
	submit        string // Submit — send the prompt / queue a follow-up
	newlineFirst  string // Newline — first chord of the binding
	newlineAlso   string // Newline — static " (also X)" suffix for remaining chords ("" when none)
	paste         string // Paste
	selectAll     string // SelectAll — select all prompt text
	copySelection string // CopySelection — copy the active prompt or conversation selection
	clearPrompt   string // ClearPrompt — clear the unsent prompt
	cancel        string // Cancel — cancel the running turn
	editBack      string // EditBack — pull the queued follow-up back into the textarea
	quit          string // Quit
	quitD         string // QuitD — the EOF-habit quit (double-press, empty prompt only)
	suspend       string // Suspend — suspend the TUI to the shell (fg resumes)
	help          string // Help — this overlay
	mcpPanel      string // MCPPanel
	resources     string // Resources
	prompts       string // Prompts
	agents        string // Agents
	effort        string // Effort
	modeSwitch    string // ModeSwitch
	expandTools   string // ExpandTools
	scroll        string // ScrollU/ScrollD — platform-adaptive on the default, "<up>/<down>" once remapped
	scrollUp      string // ScrollU — first chord, for compact one-sided paging hints
	scrollBottom  string // ScrollBottom — first chord, for auto-follow prose
	jump          string // ScrollTop/ScrollBottom joined as "home/end"
	close         string // Close/Help joined as "esc or ?" — the keys that dismiss this overlay
	allow         string // Allow — the permission-modal allow-once key (approval)
	allowAlways   string // AllowAlways — the permission-modal always-allow key (approval)
	deny          string // Deny — the permission-modal deny key (approval)
	rawArgs       string // RawArgs — pretty↔raw toggle inside the full-screen ask-args view (r)

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
func (m Model) helpKeyMarkings() helpKeys {
	marking := "pgup/pgdn"
	if m.deps.scrollKeysMarking != nil {
		marking = m.deps.scrollKeysMarking()
	}
	return keyMarkingsWithScroll(m.keys, marking)
}

// defaultHelpKeys builds the help markings from the DEFAULT bindings — the
// honest fixture for tests that don't wire custom keymaps. Standalone rendering
// is host-independent and uses the canonical PC marking.
func defaultHelpKeys() helpKeys { return keyMarkings(defaultKeys()) }

func keyMarkings(km keyMap) helpKeys { return keyMarkingsWithScroll(km, "pgup/pgdn") }

// keyMarkings derives the help-body key markings from km: each rebindable row
// shows the binding's first chord; the scroll pair joins ScrollTop/ScrollBottom
// as "home/end"; the newline row keeps its static " (also …)" suffix for any
// remaining chords. The approval keys (allow/allowAlways/deny) are populated
// too so the footer help line and the permission/plan-review action bars read
// the LIVE chords from the same struct (issue #457).
func keyMarkingsWithScroll(km keyMap, defaultScrollMarking string) helpKeys {
	hk := helpKeys{
		submit:        firstKey(km.Submit, "enter"),
		paste:         firstKey(km.Paste, "ctrl+v"),
		selectAll:     firstKey(km.SelectAll, "ctrl+g"),
		copySelection: firstKey(km.CopySelection, "ctrl+y"),
		clearPrompt:   firstKey(km.ClearPrompt, "ctrl+u"),
		cancel:        firstKey(km.Cancel, "esc"),
		editBack:      navGlyph(firstKey(km.EditBack, "up")),
		quit:          firstKey(km.Quit, "ctrl+c"),
		quitD:         firstKey(km.QuitD, "ctrl+d"),
		suspend:       firstKey(km.Suspend, "ctrl+z"),
		help:          firstKey(km.Help, "?"),
		mcpPanel:      firstKey(km.MCPPanel, "ctrl+o"),
		resources:     firstKey(km.Resources, "ctrl+r"),
		prompts:       firstKey(km.Prompts, "f8"),
		agents:        firstKey(km.Agents, "f6"),
		effort:        firstKey(km.Effort, "f7"),
		modeSwitch:    firstKey(km.ModeSwitch, "shift+tab"),
		expandTools:   firstKey(km.ExpandTools, "ctrl+t"),
		scroll:        scrollMarking(km, defaultScrollMarking),
		scrollUp:      firstKey(km.ScrollU, "pgup"),
		scrollBottom:  firstKey(km.ScrollBottom, "end"),
		jump:          firstKey(km.ScrollTop, "home") + "/" + firstKey(km.ScrollBottom, "end"),
		close:         firstKey(km.Close, "esc") + " or " + firstKey(km.Help, "?"),
		allow:         firstKey(km.Allow, "a"),
		allowAlways:   firstKey(km.AllowAlways, "w"),
		deny:          firstKey(km.Deny, "d"),
		rawArgs:       firstKey(km.RawArgs, "r"),

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
// DEFAULT pgup/pgdown chords it keeps the supplied platform-adaptive marking;
// once either half is remapped the platform gesture no longer applies, so the
// row shows the live "<scrollU>/<scrollD>" chords instead.
func scrollMarking(km keyMap, defaultMarking string) string {
	up, down := km.ScrollU.Keys(), km.ScrollD.Keys()
	if len(up) == 1 && up[0] == "pgup" && len(down) == 1 && down[0] == "pgdown" {
		return defaultMarking
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
		Provider:    m.createModelSelection.ProviderID, // "" when no selection yet → modelLine omits it
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
// ModelsMsg has landed and m.modelCatalog.statuses is populated — so this catches a
// new operator at the moment they're most attentive. Vendor-neutral: the
// provider id comes from the status row, so a future non-ToolHive
// intent-driven provider reads naturally without a code change here.
func (m Model) zeroStateGatewayNote() string {
	if isToolhiveProviderID(m.resolvedSessionModel.ProviderID) {
		return ""
	}
	row, ok := availableNotDefaultStatus(m.modelCatalog.statuses)
	if !ok {
		return ""
	}
	return m.deps.Theme.Style("muted").Render(
		"  " + sanitizeTerminal(row.ProviderID) + " gateway detected (no API key needed) — /models")
}

// zeroStateModelName picks the model id shown on the splash: the picker's active
// selection once set, else the launch-time --model display (mirrors the header).
func (m Model) zeroStateModelName() string {
	if name := m.createModelSelection.ModelID; name != "" {
		return sanitizeTerminal(name)
	}
	return m.deps.Model
}

// zeroStateRows is the affordance list on the welcome card. Every row is
// UNCONDITIONAL — "/" (built-in commands always exist) plus the help / agents /
// details rows, whose keys come from hk so a rebinding propagates here (with
// DEFAULT keys each resolves to exactly its default literal — "?", "f6",
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
