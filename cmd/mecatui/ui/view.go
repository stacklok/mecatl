package ui

import (
	"fmt"
	"path/filepath"
	"strings"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

// centerCard frames body in the askCard style and centers it over the
// conversation region; an unknown size (width/height <= 0) returns the bare card.
// It is the shared framing tail for every askCard-style overlay (permission
// modal, MCP, agents, help, zero-state) — the body builders stay separate, only
// this Place-based framing is shared. The palette deliberately does NOT use it
// (it is an inline MaxWidth dropdown, not a centered overlay).
func centerCard(th theme.Theme, body string, width, height int) string {
	card := th.Style("askCard").Render(body)
	if width <= 0 || height <= 0 {
		return card
	}
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, card)
}

// mouseCaptureEnabled is the exact terminal posture where mecatui owns mouse
// clicks. It is shared by View and mouse actions so inline/no-mouse modes leave
// approval controls to the terminal just like selection gestures.
func mouseCaptureEnabled(m Model) bool {
	return !m.deps.NoAltScreen && !m.deps.NoMouse
}

// View assembles the three-region layout (header / viewport / input / footer)
// into a tea.View. While a permission modal is open it overlays the modal,
// centred, over the conversation region. Bubble Tea v2 returns a tea.View struct
// (not a string); we set Content and request the alt screen.
func (m Model) View() tea.View {
	var v tea.View
	v.AltScreen = !m.deps.NoAltScreen
	v.WindowTitle = m.windowTitle()
	// Capture the mouse — but ONLY on the alt screen, and ONLY when mouse capture
	// is not disabled. Capturing the mouse buys wheel-scroll and the in-app
	// drag-select/copy layer (see selection.go) at the cost of the terminal's OWN
	// native click-drag selection (the terminal forwards drags to us instead).
	// Two opt-outs leave the mouse uncaptured so native selection works:
	//   - --inline / --no-alt-screen: native scrollback + selection, inline buffer.
	//   - --no-mouse (NoMouse): alt screen kept, but native selection over in-app
	//     wheel/drag — the escape hatch for terminals that strip OSC52. Keyboard
	//     scroll (pgup/pgdn/home/end) is unaffected either way.
	// (Bubble Tea v2 has no wheel-only mouse mode, so wheel-scroll and native
	// selection genuinely cannot coexist; this is the deliberate tradeoff.)
	// Capturing the mouse ALSO suppresses the terminal's native middle-click
	// primary-selection paste, so onMousePress handles tea.MouseMiddle in-app
	// (shell backend → OSC52 fallback; issue #43); shift+middle-click bypasses
	// the capture in most terminals and still performs the native paste.
	if mouseCaptureEnabled(m) {
		v.MouseMode = tea.MouseModeCellMotion
	}

	if m.phase == phaseFatal {
		v.Content = m.renderFatal()
		return v
	}

	// The full vertical region stack — header, body, the conditional inline
	// palette/mention/queue regions, then input + footer — is owned by the layout
	// model (layout.go). assembleLayout mirrors the old hand-joined order and the
	// SAME non-empty conditions, so a no-transient frame is byte-identical. The same
	// chrome() the layout uses also drives onResize/relayout (viewport sizing) and
	// convTopRow (click→content mapping), so the three can never disagree about where
	// the body sits or how tall it must be.
	v.Content = m.assembleLayout(m.renderBody()).join()
	return v
}

// renderBody picks the viewport body: a help/picker/panel overlay, an open modal
// surface centered by the PARENT via centerCard, or the conversation. "Parents
// place, surfaces size": a modal returns its UNSCENTERED body sized from the
// offered geometry; view.go then centers it here with the conversation geometry.
func (m Model) renderBody() string {
	m.hits.clear()
	m.metrics.clear()
	switch {
	case m.sessionDetailsOpen:
		return renderSessionDetails(m.deps.Theme, m.sessionDetails(), m.helpKeyMarkings(), m.width, m.vp.Height())
	case m.showHelp:
		return renderHelpOverlay(m.deps.Theme, m.caps, m.width, m.vp.Height(), m.helpKeyMarkings())
	case m.team.view != teamNone:
		return renderAgentsOverlay(m.deps.Theme, m.agentsTab, m.subagents, m.parallel, m.team, m.conv.latestTeamBlock(), m.conv.subagentFleet, m.conv.parallelGroups, m.helpKeyMarkings(), m.width, m.vp.Height())
	case m.agentsInv.view != agentsInvNone:
		return renderAgentsInvOverlay(m.deps.Theme, m.agentsInv, m.caps, m.helpKeyMarkings(), m.width, m.vp.Height())
	case m.modal != nil:
		return (&m).renderModalSurface()
	case m.userModel.view != userModelNone:
		return renderUserModelOverlay(m.deps.Theme, m.userModel, m.caps, m.helpKeyMarkings(), m.width, m.vp.Height())
	case m.reflections.view != reflectionsNone:
		return renderReflectionsOverlay(m.deps.Theme, m.reflections, m.caps, m.helpKeyMarkings(), m.width, m.vp.Height())
	case m.dream.view != dreamClosed:
		return renderDreamOverlay(m.deps.Theme, m.dream, m.caps, m.helpKeyMarkings(), m.width, m.vp.Height())
	case m.effort.view != effortNone:
		return renderEffortOverlay(m.deps.Theme, m.effort, m.effectiveModel.ReasoningEffort, m.currentModelNoReasoning(), m.helpKeyMarkings(), m.width, m.vp.Height())
	case m.worktrees.view != worktreesNone:
		return renderWorktreesOverlay(m.deps.Theme, m.worktrees, m.caps, m.helpKeyMarkings(), m.width, m.vp.Height())
	case m.schedule.view != scheduleNone:
		return renderScheduleOverlay(m.deps.Theme, m.schedule, m.caps, m.deps.Transcript != nil, m.helpKeyMarkings(), m.width, m.vp.Height())
	case m.phase == phaseIdle && m.conv.isEmpty() && !m.restartedThisRun:
		return m.renderZeroState()
	default:
		return m.rend.vpView(m.vp)
	}
}

// renderHeader is the top bar: session id · model · mode · server.
func (m Model) renderHeader() string {
	sid := m.sessionID
	if sid == "" {
		sid = "connecting…"
	} else {
		sid = "#" + sessionDigest(sid)[:8]
	}
	// next: badge — the pendingNext (apply-on-next-create) selection, shown ONLY when
	// it is set AND differs from the effective model this session runs on (same model
	// ⇒ nothing to preview). It is the FIRST segment shed under width pressure: the
	// line is assembled WITH it, and dropped to the without-it form when the with-it
	// line would overflow the header width.
	nextBadge := m.headerNextBadge()
	withBadge := m.headerIdentityParts(sid, nextBadge)
	line := strings.Join(withBadge, "  ·  ")
	if nextBadge != "" && lipgloss.Width(line) > m.widthOr()-headerIdentityPad {
		line = strings.Join(m.headerIdentityParts(sid, ""), "  ·  ")
	}
	// Right-align ONE muted indicator on the header line when it fits beside the
	// identity segment; otherwise drop it (so the indicator never forces a wrap — the
	// identity line itself still wraps when it alone exceeds the width). The header is
	// the least-crowded bar — the footer is already busy with the context meter
	// and usage facets. The scroll-position indicator takes precedence over the
	// changed-files indicator while the user is scrolled up, so it is visible
	// exactly when it matters; at the bottom it is "" and the changed-files cue
	// shows (so the steady-state at-bottom frame is byte-identical to before).
	tail := m.scrollIndicator()
	if tail == "" {
		tail = m.changedFilesIndicator()
	}
	// Operator-posture badge: right-aligned CHROME (NOT the per-session `mode`
	// segment, which is PermissionMode). It surfaces the SERVER-WIDE automation
	// posture for auto/yolo ONLY — strict/trusted render NO badge, so the steady-state
	// frame (and the goldens) are byte-identical to before this feature. postureBadgeRender
	// returns the fully-styled badge (auto → inline warning text; yolo → plain emoji bolt +
	// clean danger pill) plus its visible width, so fitHeader only does layout. When a
	// scroll/changed-files tail is also present the badge sits to its LEFT so the warning is
	// never hidden by scrolling.
	badge, badgeW, hasBadge := m.postureBadgeRender()
	if hasBadge || tail != "" {
		line = m.fitHeader(line, badge, badgeW, tail, m.widthOr())
	}
	return m.deps.Theme.Style("header").Width(m.widthOr()).Render(line)
}

// Operator-posture tier names (the m.caps.Posture vocabulary, server-wide). Named
// once so the badge, the fitHeader tier styling, and the /posture summary share one
// spelling rather than scattering the literals.
const (
	postureStrict  = "strict"
	postureTrusted = "trusted"
	postureAuto    = "auto"
	postureYolo    = "yolo"
)

// autoBadgeText is the auto-posture badge: amber inline WARNING text (no pill).
const autoBadgeText = "⚠ auto"

// yoloPillText is the YOLO pill's CONTENT — just the word, padded by a space each side
// so the filled dangerPill chip has visual breathing room around "YOLO". The pill never
// contains the lightning bolt: the emoji (when shown) rides OUTSIDE the pill as plain
// decoration (see postureBadgeRender) — VTE renders ⚡️ in its own multicolour glyph and
// IGNORES a foreground, which clashed inside the dark-on-red pill.
const yoloPillText = " YOLO "

// yoloBoltPrefix is the PLAIN (unstyled) emoji bolt shown immediately BEFORE the YOLO
// pill on an emoji-capable terminal — ⚡ + U+FE0F (VS16) for the width-2 emoji
// presentation, plus a trailing space. It is decoration OUTSIDE the pill, so the
// terminal shows the bolt's natural colour and the pill stays a clean dark-on-red chip.
// The VS16 MUST survive to the screen — the header path renders via lipgloss styles only
// and never routes the badge through normalizeEmojiWidth/wrapStyled (which strip VS16).
const yoloBoltPrefix = "⚡️ "

// postureBadgeRender builds the right-aligned operator-posture chrome badge — the STYLED
// string ready to drop into the header AND its visible (plain) cell width for the
// fit/shed math. It surfaces the SERVER-WIDE automation posture (m.caps.Posture) for the
// allow-all tiers ONLY — strict/trusted/unknown render NO badge (present=false), so the
// steady-state frame and the goldens stay byte-identical. It is DISTINCT from the
// per-session `mode` segment.
//
// Tiers:
//   - auto → amber inline WARNING text "⚠ auto".
//   - yolo → a clean filled DANGER PILL (dangerPill style) of " YOLO ", optionally
//     PRECEDED by a PLAIN ⚡️ emoji bolt when m.emojiOK (seeded once at New). The bolt is
//     pure decoration OUTSIDE the pill; the pill is always a clean dark-on-red chip.
//
// The plain width is computed to match the ACTUAL rendered glyphs: the dangerPill's
// Padding(0,1) adds 2 cells lipgloss.Width(yoloPillText) does count (the text already
// carries its spaces), so the pill's visible width is lipgloss.Width(yoloPillText)+2; the
// optional bolt prefix adds lipgloss.Width(yoloBoltPrefix) (⚡️ width-2 + space).
func (m Model) postureBadgeRender() (styled string, plainWidth int, present bool) {
	switch m.caps.Posture {
	case postureAuto:
		return m.deps.Theme.Style("warning").Render(autoBadgeText), lipgloss.Width(autoBadgeText), true
	case postureYolo:
		pill := m.deps.Theme.Style("dangerPill").Render(yoloPillText)
		// The pill's visible width = its text cells + the 2 padding cells the style adds.
		w := lipgloss.Width(yoloPillText) + 2
		if m.emojiOK {
			// PLAIN bolt prefix (no style) so VTE shows its natural emoji colour.
			return yoloBoltPrefix + pill, lipgloss.Width(yoloBoltPrefix) + w, true
		}
		return pill, w, true
	case postureStrict, postureTrusted:
		// The non-allow-all tiers carry no badge — the steady-state frame stays
		// byte-identical (the goldens are captured at strict).
		return "", 0, false
	default:
		// Unknown / older-server posture: no badge.
		return "", 0, false
	}
}

// headerModelLabel returns the model label for the header, or "" when none should
// show. The header only CHOOSES which KNOWN string to display — it NEVER computes or
// resolves a default itself. The order of preference:
//
//  1. While CONNECTING (no create response yet) ⇒ "" (no segment): the server owns
//     the resolved value and we must not guess it.
//  2. The EFFECTIVE model the server resolved THIS session to (m.effectiveModel,
//     echoed verbatim on SessionReadyMsg) — shown from turn zero. Its human display
//     name is resolved from the already-held ListModels inventory by (provider_id,
//     model_id); when the inventory has no match (not yet loaded, or a passthrough
//     id) it falls back to the raw model id.
//  3. The picker's active selection (what the NEXT session will request), then the
//     launch-time --model — the PRE-EXISTING fallbacks, kept as-is so an older server
//     that omits resolved_model still shows the configured model after connect.
func (m Model) headerModelLabel() string {
	if m.phase == phaseConnecting {
		return ""
	}
	if rm := m.effectiveModel; rm.ModelID != "" {
		for _, mi := range m.modelCatalog.models {
			if mi.ProviderID == rm.ProviderID && mi.ID == rm.ModelID && mi.DisplayName != "" {
				return mi.DisplayName
			}
		}
		return rm.ModelID
	}
	if name := m.activeModel.ModelID; name != "" {
		return name
	}
	return m.deps.Model
}

// headerIdentityPad is the slack subtracted from the header width when deciding
// whether the next: badge fits: the header's 1-cell padding each side (2) plus the
// fitHeader gap+indicator headroom, so the badge is dropped a touch EARLY rather than
// fighting the right-aligned indicator for the last cells.
const headerIdentityPad = 4

// headerIdentityParts builds the header identity segments. withNext is the next:
// badge ("" to omit it). Order: mecatui · session · model · [next: …] · mode ·
// ws: <worktree> · socket. The next: badge sits AFTER the current model so the eye
// reads "running X, next Y", and is the FIRST segment renderHeader sheds under width
// pressure. The ws: segment is shown only when the active workspace differs from the
// launch workspace (Deps.Workspace) — no noise when not switched (issue #102).
func (m Model) headerIdentityParts(sid, withNext string) []string {
	parts := []string{"mecatui", "session " + short(sid)}
	// Model segment: the EFFECTIVE model the server resolved THIS session to (set once
	// on SessionReadyMsg). The header only CHOOSES which known string to display; it
	// never resolves a default itself. While connecting there is NO model segment.
	if name := m.headerModelLabel(); name != "" {
		seg := truncate(sanitizeTerminal(name), maxModelLen)
		// Downstream-provider suffix (issue #480): when the serving provider routed
		// this session's latest turn to a DOWNSTREAM provider (openrouter today), append
		// "/ <name>" so the operator sees e.g. "Kimi K3/Google". Shown ONLY when a route
		// has actually been reported this session (m.providerRoute non-empty) — empty on
		// a cache hit, before the first turn, and for any non-routed provider, so the
		// bare model segment shows with no stale/fabricated suffix.
		if m.providerRoute != "" {
			seg += "/" + sanitizeTerminal(m.providerRoute)
		}
		// Reasoning-effort suffix (ADR 0055): the EFFECTIVE effort the server resolved
		// THIS session to, appended as a subtle ` · <effort>` so it rides WITH the model
		// segment (and sheds with it under width pressure). Shown ONLY when non-empty
		// (auto/unset echoes "" and so never renders).
		if eff := effortHeaderSuffix(m.effectiveModel.ReasoningEffort); eff != "" {
			seg += " · " + eff
		}
		parts = append(parts, seg)
	}
	// ToolHive gateway disclosure (issue #262, R6.3): a persistent, muted "via
	// ToolHive gateway" segment whenever the ACTIVE session's provider is
	// toolhive — disclosure-only (no acknowledgment required), riding the same
	// segment slice so the EXISTING width-shedding/fitHeader math applies
	// unchanged (it sheds like any other low-priority segment under pressure).
	if m.effectiveModel.ProviderID == "toolhive" {
		parts = append(parts, m.deps.Theme.Style("muted").Render("via ToolHive gateway"))
	} else if row, ok := availableNotDefaultStatus(m.modelCatalog.statuses); ok {
		// Sibling (N1): when an intent-driven provider is detected-and-reachable
		// but NOT the active default, show a muted "<provider-id> gateway
		// available" segment. Mutually exclusive with the active-case branch
		// above by construction: availableNotDefaultStatus is false when the
		// gateway IS the default, so the two never both render. Vendor-neutral —
		// the provider id comes from the status row, not a hardcoded "toolhive".
		parts = append(parts, m.deps.Theme.Style("muted").
			Render(sanitizeTerminal(row.ProviderID)+" gateway available"))
	}
	if withNext != "" {
		parts = append(parts, withNext)
	}
	mode := m.activeMode
	if mode == "" {
		mode = m.deps.Mode
	}
	if m.pendingMode != "" {
		mode = m.pendingMode + " pending"
	}
	// Mode segment: the active/pending permission mode is colour-coded as the
	// persistent visual cue for the same state the input box advertises.
	if mode != "" {
		parts = append(parts, m.renderHeaderMode(mode))
	}
	// Workspace segment (issue #102): shown only when the session is rooted at a
	// DIFFERENT workspace than the launch directory (no noise in the common case).
	// Display just the last path component to keep the header compact.
	if ws := m.activeWorkspace; ws != "" && ws != m.deps.Workspace {
		wsPart := m.deps.Theme.Style("muted").Render("ws:" + filepath.Base(ws))
		parts = append(parts, wsPart)
	}
	if m.deps.Server != "" {
		parts = append(parts, m.deps.Server)
	}
	return parts
}

func (m Model) inputMode() string {
	return client.ModeString(client.ModeFromString(m.desiredMode()))
}

func modeAccentStyle(th theme.Theme, mode string) lipgloss.Style {
	s := lipgloss.NewStyle().Bold(true)
	mode = client.ModeString(client.ModeFromString(strings.TrimSuffix(mode, " pending")))
	switch mode {
	case "plan":
		return s.Foreground(th.Color("info"))
	case "accept-edits":
		return s.Foreground(th.Color("success"))
	default:
		return s.Foreground(th.Color("accent"))
	}
}

func (m *Model) applyModeInputStyle() {
	mode := m.inputMode()
	styles := m.ta.Styles()
	base := textarea.DefaultDarkStyles()
	accent := modeAccentStyle(m.deps.Theme, mode)
	// The textarea's inner prompt bar and line-number gutter are suppressed (New() sets
	// Prompt="" and ShowLineNumbers=false, issue #161), so the rail border is the single
	// mode cue — no Prompt/LineNumber/CursorLineNumber styling needed here any more.
	styles.Focused.Placeholder = base.Focused.Placeholder.Foreground(accent.GetForeground())
	styles.Cursor.Color = accent.GetForeground()
	// Tint the WHOLE textarea body on the faint panel background so the input block
	// reads as ONE even surface — the same bgPanel the rail pads its margins with — and
	// so an empty input and a typed one look identical (the inconsistent-tint bug). The
	// DefaultDarkStyles ship their own per-state backgrounds (a black cursor-line, a
	// transparent body), which made the rows tint differently by content; overriding the
	// body/cursor-line/placeholder/end-of-buffer backgrounds to bgPanel makes the fill
	// uniform across every row. Applied to BOTH focus states so blur doesn't change the
	// surface (the rail border is the single mode cue and stays at full accent strength
	// regardless of focus — see the rail-blur invariant).
	bg := m.deps.Theme.Color("bgPanel")
	// Typed text gets the theme's full-strength Text colour for contrast: the bubbles
	// DefaultDarkStyles leave Text with no foreground (terminal default) and tint the
	// CursorLine grey (color 245), so what you type rendered washed-out on the panel.
	// Setting both to the bright Text slot makes the input legible without touching the
	// dim Placeholder (which stays muted as a prompt cue).
	txt := m.deps.Theme.Color("text")
	for _, st := range []*textarea.StyleState{&styles.Focused, &styles.Blurred} {
		st.Base = st.Base.Background(bg)
		st.Text = st.Text.Background(bg).Foreground(txt)
		st.CursorLine = st.CursorLine.Background(bg).Foreground(txt)
		st.EndOfBuffer = st.EndOfBuffer.Background(bg)
		st.Placeholder = st.Placeholder.Background(bg)
	}
	m.ta.SetStyles(styles)
}

func (m Model) renderHeaderMode(mode string) string {
	clean := sanitizeTerminal(mode)
	return "mode " + modeAccentStyle(m.deps.Theme, clean).Render(clean)
}

// headerNextBadge is the muted "next: <model>" header badge previewing the
// pendingNext (apply-on-next-create) selection. It shows ONLY when there is a KNOWN
// effective model to contrast against (m.effectiveModel set) AND the pendingNext
// resolves to a DIFFERENT (provider, model). Both guards matter: when no effective
// model is known yet (connecting / older server) the model SEGMENT already shows the
// pendingNext, so a "next:" badge would just duplicate it; and a same-model next is
// nothing to preview. The display name is resolved from the ListModels inventory by
// (provider_id, model_id), falling back to the raw id — the same lookup the
// effective-model label uses. Returns "" when there is no distinct next model.
func (m Model) headerNextBadge() string {
	next := m.activeModel
	eff := m.effectiveModel
	if next.ModelID == "" || eff.ModelID == "" {
		return ""
	}
	if next.ModelID == eff.ModelID && next.ProviderID == eff.ProviderID {
		return ""
	}
	label := next.ModelID
	for _, mi := range m.modelCatalog.models {
		if mi.ProviderID == next.ProviderID && mi.ID == next.ModelID && mi.DisplayName != "" {
			label = mi.DisplayName
			break
		}
	}
	return "next: " + truncate(sanitizeTerminal(label), maxModelLen)
}

// scrollIndicator returns the muted "↑ NN%" header cue shown ONLY when the user
// has scrolled up off the bottom (!m.stuck) — the discoverable signal that the
// view is no longer tailing live output and how far up it sits. It is "" while
// stuck (auto-following the bottom), so the at-bottom steady-state header — and
// thus the View goldens captured there — is unchanged.
func (m Model) scrollIndicator() string {
	if m.stuck {
		return ""
	}
	return fmt.Sprintf("↑ %d%%", int(m.vp.ScrollPercent()*100))
}

// changedFilesIndicator returns the muted "✎ N files" header indicator
// summarising how many distinct workspace files file-mutating tools have touched
// this session, or "" when none have. The "✎" (pencil = "edited") is coherent
// with the hook-modified glyph and avoids "Δ" colliding with the Edit/Write
// "+N/-N" diff size signals. It is a compact count; the full path list is
// revealed under ctrl+t (see renderChangedFiles).
func (m Model) changedFilesIndicator() string {
	n := len(m.filesChanged)
	if n == 0 {
		return ""
	}
	return "✎ " + plural(n, "file")
}

// headerGapPad is the minimum blank gap kept between the header identity segment
// and the right-aligned changed-files indicator so they never touch. Mirrors the
// footer's footerGapPad but is owned by the header path (naming honesty).
const headerGapPad = 2

// fitHeader right-aligns the indicator (an optional ALREADY-STYLED posture badge plus an
// optional MUTED scroll/changed-files tail) beside the identity line when there is room
// (accounting for the header's 1-cell horizontal padding on each side), and otherwise
// returns the identity line unchanged — so the indicator never forces a wrap; a
// too-narrow terminal simply sheds it. The badge arrives pre-styled with its visible
// width (badgeW) from postureBadgeRender — fitHeader does NOT re-derive the per-tier
// styling or the pill padding, it only lays out — so the gap/shed math uses badgeW
// directly and can never disagree with what was rendered (auto inline text, yolo plain
// bolt + clean pill all measured at the source). The tail is muted here. (The identity
// line itself still wraps when it alone exceeds the width; the header's rendered row count
// is measured via region.height()/chrome() in layout.go.)
func (m Model) fitHeader(line, badge string, badgeW int, tail string, width int) string {
	const headerPad = 2 // the "header" style pads 1 cell each side
	// Plain (visible-cell) width math; the rendered segments carry style. badgeW is the
	// badge's true visible width (incl. the pill padding / emoji bolt), measured at render.
	plainW := badgeW
	switch {
	case badge != "" && tail != "":
		plainW += 2 + lipgloss.Width(tail) // the "  " gap + the tail
	case tail != "":
		plainW += lipgloss.Width(tail)
	}
	gap := width - headerPad - lipgloss.Width(line) - plainW - headerGapPad
	if gap < 0 {
		return line
	}
	var styled string
	switch {
	case badge != "" && tail != "":
		styled = badge + "  " + m.deps.Theme.Style("muted").Render(tail)
	case badge != "":
		styled = badge
	default:
		styled = m.deps.Theme.Style("muted").Render(tail)
	}
	return line + strings.Repeat(" ", gap) + styled
}

// renderFooter is the status bar: spinner + active tool + status + usage.
func (m Model) renderFooter() string {
	approval := approvalFooterProjection{}
	if m.phase == phaseAwaitingApproval {
		approval = approvalFooterProjectionFor(approvalSurfaceFor(&m))
	}
	var left string
	switch m.phase {
	case phaseRunning:
		spin := m.sp.View()
		switch {
		case m.activeTool != "" && m.toolProgress != "":
			// A long-running tool forwarded a transient progress line: show it in
			// place of the bare "Running X…" so the footer reflects live activity
			// instead of looking frozen. Cleared on the next tool.result/turn boundary.
			left = fmt.Sprintf("%s %s…", spin, m.toolProgress)
		case m.activeTool != "":
			left = fmt.Sprintf("%s Running %s…", spin, m.activeTool)
		default:
			left = spin + " thinking…"
		}
	case phaseAwaitingApproval:
		label := "⚠ awaiting approval"
		if approval.plan {
			label = "⚙ plan review"
		}
		if approval.queued > 0 {
			label = fmt.Sprintf("%s (1 of %d)", label, 1+approval.queued)
		}
		left = m.deps.Theme.Style("askTitle").Render(label)
	case phaseConnecting:
		left = m.sp.View() + " connecting…"
	default:
		// The idle/default-phase footer-left (selection count / reconnecting cue /
		// gateway notice / statusMsg) is extracted to keep renderFooter under the
		// cyclomatic bound; see idleFooterLeft.
		left = m.idleFooterLeft()
	}

	// The mouse-debug overlay (MECATUI_DEBUG_MOUSE=1) takes the footer-left at the
	// HIGHEST priority — over every phase arm above — so the live raw-coords/mapping
	// line stays visible even during a drag (the gesture that the diagnostic targets).
	if m.deps.DebugMouse && m.mouseDebug != "" {
		left = m.deps.Theme.Style("muted").Render(m.mouseDebug)
	}

	// The full decompressed chord list now lives in the "?" help overlay, so the
	// footer carries only the two entry points and quit. "/ commands" is ALWAYS
	// shown: the TUI ships built-in client-side commands (/clear, /help, and the
	// caps-gated /mcp,/agents), so "/" is a live entry point even when the server
	// has slash-command expansion disabled. While a run streams the line is extended
	// with the type-while-running affordance (enter queues a follow-up; esc clears
	// the staged input/queue or cancels the run). Every chord is sourced from the
	// LIVE keyMap markings (hk) so a rebinding propagates to the footer affordances
	// (issue #457, the #455 liveness pattern extended to the footer).
	hk := m.helpKeyMarkings()
	help := hk.help + " help · / commands · " + hk.quit + " quit"
	if m.phase == phaseRunning {
		help = hk.submit + " queue · " + hk.cancel + " cancel/clear · " + help
	}
	if m.phase == phaseAwaitingApproval && approval.plan {
		allow, always, deny := approvalMnemonic(hk.allow), approvalMnemonic(hk.allowAlways), approvalMnemonic(hk.deny)
		if approval.offerAlways {
			help = allow + " approve & run · " + always + " auto-accept · " + deny + " iterate · " + help
		} else {
			help = allow + " approve & run · " + deny + " iterate · " + help
		}
	}
	// While the double-quit guard is armed, prepend a loud "again to quit" cue to
	// the help line. The footer is the one chrome line present in every phase (the
	// left status differs by phase), so it is the robust place for the hint.
	if m.quitArmed {
		help = m.deps.Theme.Style("ctxWarn").Render(hk.quit+" again to quit") + " · " + help
	}

	width := m.widthOr()
	line := m.fitFooter(left, width)
	footer := m.deps.Theme.Style("footer").Width(width).Render(line)
	return footer + "\n" + m.deps.Theme.Style("muted").Render(help)
}

// idleFooterLeft renders the footer-left for the idle/default phase, extracted
// from renderFooter to keep that dispatcher under the cyclomatic-complexity
// bound. Precedence: the live-feed reconnecting cue (issue #387) → the
// selection count → the gateway notice → the bare statusMsg / "ready". The
// selection count appears ONLY in this phase (the running/approval/connecting
// arms own the footer-left there), so it is never shown mid-run by construction
// (Req 5); the reconnecting cue and gateway notice likewise only surface here.
func (m Model) idleFooterLeft() string {
	switch {
	case m.liveReconnecting:
		// The live session event feed dropped and the client is reconnecting with
		// bounded backoff (issue #387). A concise degraded cue so the operator
		// knows deliveries may be momentarily delayed (they recover via the
		// durable catch-up on reconnect). Styled as a warning so it reads as
		// chrome, not an alert.
		return m.deps.Theme.Style("ctxWarn").Render(
			fmt.Sprintf("live feed reconnecting (attempt %d)…", m.liveReconnectAttempt),
		)
	case m.sel.active && !m.sel.empty():
		return m.selectionStatus()
	case m.gatewayNotice != "":
		return m.deps.Theme.Style("muted").Render(m.gatewayNotice)
	default:
		if m.statusMsg == "" {
			return "ready"
		}
		return m.statusMsg
	}
}

// selectionStatus is the footer-left segment shown while a non-empty selection
// is active (idle/default phase only — see renderFooter). It reports the live
// VISIBLE size of the selection as "N chars · M lines" (correct singular: "1
// char", "1 line"), counting the copy-ready runes (selectedText) and the spanned
// logical lines. After a copy the statusMsg carries "copied …", so the count is
// prefixed "copied · " — the selection persists past a copy (Req 7), so the
// confirmation rides alongside the still-live count rather than replacing it. The
// whole segment is muted so it reads as chrome, not an alert.
func (m Model) selectionStatus() string {
	muted := m.deps.Theme.Style("muted")
	txt := selectedText(m.vp.GetContent(), m.sel)
	chars := len([]rune(txt))
	startL, _, endL, _ := m.sel.normalize()
	lines := endL - startL + 1
	count := fmt.Sprintf("%s · %s", plural(chars, "char"), plural(lines, "line"))
	// statusMsg is lipgloss-rendered (ANSI-wrapped), so strip before matching the
	// "copied" sentinel copySelection sets.
	if strings.Contains(ansi.Strip(m.statusMsg), "copied") {
		return muted.Render("copied · " + count)
	}
	return muted.Render(count)
}

// footerGapPad is the minimum blank gap kept between the left status and the
// right-aligned usage segment so they never touch.
const footerGapPad = 2

// contextWindow returns the denominator for the footer context meter:
//
//  1. m.effectiveModel.ContextWindow > 0 — the window the SERVER resolved for THIS
//     session's model (echoed on SessionReadyMsg, refreshed on every model switch /
//     GetSession). It is now LIVE-FIRST server-side, so a live-only model heals to
//     its real window. Exact operator configuration precedes live metadata; the global
//     escape hatch is mecatui embedded mode's (or an external mecated's)
//     --context-window-override, which moves this echoed denominator and the engine
//     trigger together. The client never recomputes the window.
//  2. 0 — unknown; the meter renderers degrade to the bare "ctx <N>" current size.
func (m Model) contextWindow() int64 {
	if m.effectiveModel.ContextWindow > 0 {
		return m.effectiveModel.ContextWindow
	}
	return 0
}

// fitFooter right-aligns the richest usage segment that fits beside the left
// status, shedding facets before the context signal — context % is the single
// most valuable signal, so it survives longest. When a team is LIVE a team-summary
// segment is PREPENDED to the right side; it is LOWER priority than the context
// meter (it's an advertisement, context % is the headline safety signal), so it is
// the FIRST thing dropped as width tightens. The <agents> chord below is the LIVE
// Agents binding (issue #457) — "ctrl+a" by default, rebound via keymap. Tiers,
// richest to poorest (defaults shown):
//
//	"⟳ team-x · 2/3 working · ctrl+a agents  ctx ▒▒▒▒▒·· 70% · 140K/200K · ↑7.9K ↓345 cache 88%"
//	"⟳ team-x · 2/3 working · ctrl+a agents  ctx ▒▒▒▒▒·· 70% · 140K/200K"
//	"⟳ 2/3 working · ctrl+a  ctx ▒▒▒▒▒·· 70%"   (team→medium, ctx→compact)
//	"⟳ 2/3  ctx 70%"                            (team→compact, ctx→minimal)
//	"ctx 70%"                                    (team DROPPED, ctx wins)
//	then the existing ctx-only fallbacks, then left status alone.
//
// When no team is live the team segment is empty and the candidate list collapses
// to EXACTLY the historical ctx-only list — keeping the no-team footer
// byte-identical (existing goldens unaffected).
//
// When the window is unknown every meter tier collapses to "ctx 7.9K", so the
// tiers naturally narrow to just that, then to nothing.
func (m Model) fitFooter(left string, width int) string {
	th := m.deps.Theme
	window := m.contextWindow()
	meter := renderContextMeter(th, m.contextTokens, window)
	meterCompact := renderContextMeterCompact(th, m.contextTokens, window)
	meterMinimal := renderContextMeterMinimal(th, m.contextTokens, window)

	// The agents prefix is the combined team + subagent-fleet advertisement, prepended
	// to the right side at three tiers (full/medium/compact). Each is built from up to
	// two sub-segments joined by sep:
	//   - the team segment, non-empty ONLY for a LIVE team (liveTeamBlock — not teamDone).
	//     This DELIBERATELY differs from the ctrl+a overlay's gate: the footer is a
	//     live-activity advertisement and hides once the team is done, whereas the
	//     overlay opens on the last-seen team done-or-not (so the user can still review a
	//     finished roster). The two are meant to disagree in the done state — don't unify.
	//   - the fleet segment, non-empty whenever ≥1 subagent has STARTED this session
	//     (hasSubagents). Unlike the team segment this stays visible after the children
	//     finish (the "3◐ 1✓" counts still inform), matching the F2 "3/4 done" cue.
	const sep = "  " // gap between the agents prefix and the ctx segment, and between sub-segments
	// agentsMark is the LIVE Agents chord (issue #457) so the footer advertisement
	// reflects a rebound open-overlay key.
	agentsMark := m.helpKeyMarkings().agents
	var teamFull, teamMedium, teamCompact string
	if b := m.conv.liveTeamBlock(); b != nil {
		working, total := teamWorkingCounts(b.teamLanes)
		teamFull = teamFooterFull(th, b.teamID, working, total, agentsMark)
		teamMedium = th.Style("spinner").Render(teamFooterMedium(b.teamID, working, total, agentsMark))
		teamCompact = th.Style("spinner").Render(teamFooterCompact(working, total))
	}
	var subFull, subMedium, subCompact string
	if m.conv.hasSubagents() {
		running, done := m.conv.subagentFleetCounts()
		subFull = subagentFooterFull(th, running, done, agentsMark)
		subMedium = th.Style("spinner").Render(subagentFooterMedium(running, done, agentsMark))
		subCompact = th.Style("spinner").Render(subagentFooterCompact(running, done))
	}
	// The Parallel segment, non-empty whenever ≥1 Parallel run has STARTED this session
	// (hasParallel) — like the fleet segment it stays visible after the run finishes (the
	// "1◐ 2✓" counts still inform), matching the subagent-fleet footer behaviour.
	var parFull, parMedium, parCompact string
	if m.conv.hasParallel() {
		running, done := m.conv.parallelGroupCounts()
		parFull = parallelFooterFull(th, running, done, agentsMark)
		parMedium = th.Style("spinner").Render(parallelFooterMedium(running, done, agentsMark))
		parCompact = th.Style("spinner").Render(parallelFooterCompact(running, done))
	}
	agentsFull := joinSeg(sep, teamFull, parFull, subFull)
	agentsMedium := joinSeg(sep, teamMedium, parMedium, subMedium)
	agentsCompact := joinSeg(sep, teamCompact, parCompact, subCompact)

	var candidates []string
	if agentsFull != "" {
		// Richest-to-poorest cross-product. The agents prefix sheds before the ctx
		// meter: the last agents-bearing tier (compact + ctx-minimal) is followed by the
		// agents-LESS ctx-minimal so context wins when width is tight.
		candidates = append(candidates,
			agentsFull+sep+meter+" · "+renderUsageFacets(m.usage),
			agentsFull+sep+meter,
			agentsMedium+sep+meterCompact,
			agentsCompact+sep+meterMinimal,
		)
	}
	candidates = append(candidates,
		meter+" · "+renderUsageFacets(m.usage),
		meter,
		meterCompact,
		meterMinimal,
	)
	leftW := lipgloss.Width(left)
	for _, seg := range candidates {
		gap := width - leftW - lipgloss.Width(seg) - footerGapPad
		if gap >= 0 {
			return left + strings.Repeat(" ", gap) + seg
		}
	}
	return left
}

// joinSeg joins the non-empty segments with sep, so a footer prefix built from up to
// two optional sub-segments (team + subagent fleet) collapses cleanly: with neither
// it is "", with one it is that segment alone (no leading/trailing sep), with both it
// is "<a><sep><b>". This keeps the no-agents footer byte-identical to the historical
// ctx-only footer (agentsFull == "" drops the whole prefix branch).
func joinSeg(sep string, segs ...string) string {
	parts := make([]string, 0, len(segs))
	for _, s := range segs {
		if s != "" {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, sep)
}

// queuePreviewLimit is the number of staged follow-ups previewed in the queue
// card; the rest are summarised as a "+K more" line so a deep queue stays compact.
const queuePreviewLimit = 3

// queuePreviewWidth caps each preview line's display width so a long staged prompt
// can't blow out the card; truncate appends an ellipsis past the cap.
const queuePreviewWidth = 60

// queuePreviewBound caps how many leading RUNES of a queued prompt feed the
// per-frame preview flatten (oneLine) + truncate. renderQueue runs on every
// rendered frame while the queue is non-empty, and an enqueue-expanded staged
// paste can be a multi-KB payload — flattening it in full made every frame pay
// O(payload) until the queue drained (the very lag issue #45's staging removes
// from the input). 4× the preview's display width is always enough runes to fill
// the truncate(…, queuePreviewWidth) output, except for a prefix that is almost
// entirely collapsed whitespace — then the preview just shows less of a huge
// payload, which is fine for a one-line hint. Output is byte-identical to the
// unbounded algorithm for any queued string under the bound.
const queuePreviewBound = 4 * queuePreviewWidth

// runePrefix returns the first n runes of s without scanning past them (no
// full-string RuneCountInString) — the O(1)-in-payload slice renderQueue's
// preview is built from.
func runePrefix(s string, n int) string {
	for i := range s {
		if n == 0 {
			return s[:i]
		}
		n--
	}
	return s
}

// renderQueue draws the "staged follow-ups" card shown just above the input
// whenever the queue is non-empty. It reuses existing theme slots only (muted /
// toolArgs for the normal card, ctxWarn for the paused header) — no new slots — so
// it inherits the palette/card visual language. Returns "" for an empty queue (View
// omits it then).
//
// Two states:
//   - DRAINING (m.queuePaused == ""): a run is streaming (or about to) and the queue
//     will fire FIFO at its clean completion — a muted "⏳ N queued".
//   - PAUSED (m.queuePaused != ""): the last run ended on a non-clean stop (error /
//     cancel / repeated failures / stream close) so the queue is HELD, not fired. A
//     ctxWarn "⏸ N queued · paused: <reason>" header plus the resume/clear keys, so
//     a held queue never reads as a silent hang (the whole point of this card).
func (m Model) renderQueue() string {
	n := len(m.queued)
	if n == 0 {
		return ""
	}
	th := m.deps.Theme
	hk := m.helpKeyMarkings()
	muted := th.Style("muted")
	var b strings.Builder
	if m.queuePaused != "" {
		reason, _ := stopReasonLabel(m.queuePaused)
		b.WriteString(th.Style("ctxWarn").Render(fmt.Sprintf("⏸ %d queued · paused: %s", n, reason)))
	} else {
		// The edit hint only applies when EditBack is actionable, which
		// requires an EMPTY input line; a draft present would make the
		// hint misleading.
		if strings.TrimSpace(m.ta.Value()) == "" {
			b.WriteString(muted.Render(fmt.Sprintf("⏳ %d queued · %s edit", n, hk.editBack)))
		} else {
			b.WriteString(muted.Render(fmt.Sprintf("⏳ %d queued", n)))
		}
	}
	shown := min(n, queuePreviewLimit)
	for i := 0; i < shown; i++ {
		// Bound the flatten to a rune prefix (queuePreviewBound) so a multi-KB
		// queued payload is never whitespace-scanned in full on every frame.
		b.WriteString("\n" + th.Style("toolArgs").Render("  "+truncate(oneLine(runePrefix(m.queued[i], queuePreviewBound)), queuePreviewWidth)))
	}
	if rest := n - shown; rest > 0 {
		b.WriteString("\n" + muted.Render(fmt.Sprintf("  +%d more", rest)))
	}
	if m.queuePaused != "" {
		b.WriteString("\n" + muted.Render("  "+hk.submit+" sends · "+hk.editBack+" edit · "+hk.cancel+" clears"))
	}
	return th.Style("askCard").Render(b.String())
}

// renderSteer draws the steer-mode (Capabilities.Steer) "in-flight steer" card
// shown just above the input whenever a steer is live. It mirrors renderQueue's
// visual language (muted / toolArgs / ctxWarn on the askCard style) but reflects
// the AUTHORITATIVE server-reported state — the engine is the sole authority on
// what happened to a steer (the client cannot observe the drain moment across
// stream latency), so the card shows what the server acked/echoed, never a
// client-side guess. Returns "" when no steer is in flight (layout omits it then).
//
// Four honest states:
//   - PENDING (steerPending): sent, ack not yet back — a muted "⏳ steer: sending…".
//   - SENT (steerSent): acked accepted/appended, parked for the next turn
//     boundary — a muted "⏳ steer queued · ↑ edit · esc retract".
//   - PROMOTED (steerPromoted): acked too_late — the run had ended, so the text
//     auto-started a follow-up — a ctxWarn "↪ steer sent as a follow-up (run had
//     already ended)".
//   - RETRACTED (steerRetracted): the steer_cancel won — a muted "✕ steer
//     retracted".
func (m Model) renderSteer() string {
	if m.steer == nil {
		return ""
	}
	th := m.deps.Theme
	hk := m.helpKeyMarkings()
	muted := th.Style("muted")
	var b strings.Builder
	switch m.steer.Phase {
	case steerPending:
		b.WriteString(muted.Render("⏳ steer: sending…"))
	case steerSent:
		b.WriteString(muted.Render("⏳ steer queued · " + hk.editBack + " edit · " + hk.cancel + " retract"))
	case steerPromoted:
		b.WriteString(th.Style("ctxWarn").Render("↪ steer sent as a follow-up (run had already ended)"))
	case steerFailed:
		b.WriteString(th.Style("warning").Render("✕ steer not sent (promotion failed) · ↑ to edit · esc to drop"))
	case steerRetracted:
		b.WriteString(muted.Render("✕ steer retracted"))
	}
	// Preview each logical message on its own line. A re-composed (↑-edit)
	// fragment carries the WHOLE merged bundle in its Text (with blank-line
	// separators INSIDE), so split each send's text on the merge separator:
	// the wire stays one frame while the card shows the parts as separate
	// queued lines.
	for _, s := range m.steer.Sends {
		for _, part := range strings.Split(s.Text, queueMergeSep) {
			b.WriteString("\n" + th.Style("toolArgs").Render("  "+truncate(oneLine(runePrefix(part, queuePreviewBound)), queuePreviewWidth)))
		}
	}
	return th.Style("askCard").Render(b.String())
}

// renderInput renders the textarea (now always focused — it stays editable while a
// run streams so a follow-up can be composed and enqueued), memoized on the
// renderer's single-entry input cache (renderer.inputKey — see its doc): when no
// textarea fact in the key changed since the previous render, the cached string is
// returned instead of re-running textarea.View()'s full per-line re-wrap.
//
// Correctness of the single entry rests on two facts. (1) The textarea's only
// state NOT in the key — its internal viewport scroll offset, and the virtual
// cursor's blink phase — can only change as a side effect of a mutation that also
// changes a keyed fact in the SAME reducer step (the scroll offset moves only when
// the cursor crosses the visible window, i.e. row/rowOffset/width/height changed;
// the blink phase flips only on Focus/Blur, since the reducer never routes
// cursor.BlinkMsg to the textarea — the cursor is static, not blinking, today).
// The active permission mode is deliberately keyed because it changes the input
// colour cue without necessarily changing any textarea-owned state.
// (2) renderInput runs on EVERY reduced message (the relayout chokepoint's
// chrome()), so the cache is re-keyed in the same step the mutation lands — there
// is no window in which a hidden-state change can hide behind an unchanged key.
func (m Model) renderInput() string {
	m.applyModeInputStyle()
	li := m.ta.LineInfo()
	key := inputRenderKey{
		value:     m.ta.Value(),
		row:       m.ta.Line(),
		rowOffset: li.RowOffset,
		colOffset: li.ColumnOffset,
		focused:   m.ta.Focused(),
		width:     m.ta.Width(),
		height:    m.ta.Height(),
		mode:      m.inputMode(),
	}
	if m.rend.inputValid && m.rend.inputKey == key {
		return m.rend.inputView
	}
	out := m.renderInputRail(m.ta.View())
	m.rend.inputKey, m.rend.inputView, m.rend.inputValid = key, out, true
	return out
}

// inputRailStyle builds the mode-coloured left rail wrapping the input textarea: a
// left border tinted by the active permission mode's accent (default accent / plan
// info / accept-edits success, via modeAccentStyle) over a faint panel background. It
// is the SINGLE source of the rail's geometry, so onResize can subtract its
// GetHorizontalFrameSize() from the textarea width and the two never drift. The colour
// is a pure function of (theme, mode) — both fixed-or-keyed — keeping the input cache
// sound (see renderInput).
func inputRailStyle(th theme.Theme, mode string) lipgloss.Style {
	return lipgloss.NewStyle().
		BorderStyle(lipgloss.NormalBorder()).
		BorderLeft(true).
		BorderForeground(modeAccentStyle(th, mode).GetForeground()).
		Background(th.Color("bgPanel")).
		Padding(inputRailPadTop, inputRailPadX, 0, inputRailPadX)
}

// inputRailPadX is the horizontal padding inside the input panel (each side). With the
// textarea's own inner prompt bar gone (issue #161), a single column now sits directly
// between the rail border and the text — enough breathing room without the text floating.
// renderInputRail derives the content width from the rail's GetHorizontalFrameSize(),
// so this value flows through automatically.
const inputRailPadX = 1

// inputRailPadTop is the TOP inner padding of the input panel: one tinted blank row
// above the input content so the placeholder/typed text isn't pressed against the top
// border. It INTENTIONALLY makes the input region one row taller — the layout measures
// region heights via lipgloss.Height, so the body shrinks by it automatically (the
// input height-invariance test expects exactly this +1). lipgloss renders the pad row
// with the style's Background, so it is bgPanel-tinted full-width like the content rows.
const inputRailPadTop = 1

// renderInputRail wraps the textarea view in the mode-coloured rail AND fills the faint
// panel tint UNIFORMLY across the whole input block — full terminal width and every
// textarea row — so an empty input and a typed one look identical.
//
// Why this is not just a Background on the wrapper: the bubbles textarea renders its
// content through an INTERNAL viewport (textarea.View → viewport.View) that pads every
// line out to its width with PLAIN, unstyled spaces — a trailing region the textarea's
// own Style fields (Base/Text/CursorLine/EndOfBuffer, tinted to bgPanel in
// applyModeInputStyle) cannot reach. That unstyled run is what made the tint ragged on
// the right and different empty-vs-typed. The textarea ALWAYS emits its own styled
// padding (and the reverse-video cursor) BEFORE that viewport padding, so the trailing
// blank run is genuinely unstyled: stripTrailingBlank removes it and we re-pad each line
// to the full content width with bgPanel-backed spaces, flush to the edge on every row.
//
// Width: contentW = m.width − the rail's own frame (BorderLeft + PaddingLeft), so the
// re-padded content plus the rail's border+padding is exactly m.width. (onResize sizes
// the textarea from the same GetHorizontalFrameSize(), so the two never drift.)
func (m Model) renderInputRail(taView string) string {
	style := inputRailStyle(m.deps.Theme, m.inputMode())
	contentW := max(1, m.width-style.GetHorizontalFrameSize())
	fill := lipgloss.NewStyle().Background(m.deps.Theme.Color("bgPanel"))
	lines := strings.Split(taView, "\n")
	for i, ln := range lines {
		ln = stripTrailingBlank(ln)
		if pad := contentW - ansi.StringWidth(ln); pad > 0 {
			ln += fill.Render(strings.Repeat(" ", pad))
		}
		lines[i] = ln
	}
	return style.Render(strings.Join(lines, "\n"))
}

// stripTrailingBlank removes a line's trailing run of plain spaces and bare SGR resets
// (\x1b[m / \x1b[0m) — the unstyled padding the textarea's internal viewport appends past
// the styled content. It stops at the first styled (non-reset) sequence, so the
// textarea's own bgPanel-backed padding and the reverse-video cursor — which always sit
// BEFORE the viewport padding — are preserved. Used by renderInputRail to re-pad the
// input tint flush to the right edge.
func stripTrailingBlank(s string) string {
	for {
		switch {
		case strings.HasSuffix(s, "\x1b[m"):
			s = s[:len(s)-3]
		case strings.HasSuffix(s, "\x1b[0m"):
			s = s[:len(s)-4]
		default:
			if t := strings.TrimRight(s, " "); t != s {
				s = t
				continue
			}
			return s
		}
	}
}

// renderFatal renders a centred fatal-error panel.
func (m Model) renderFatal() string {
	msg := m.deps.Theme.Style("errorText").Render("connection failed") + "\n\n" +
		m.deps.Theme.Style("muted").Render(m.fatalErr) + "\n\n" +
		m.deps.Theme.Style("muted").Render("press "+m.helpKeyMarkings().quit+" to quit")
	card := m.deps.Theme.Style("askCard").Render(msg)
	if m.width > 0 && m.height > 0 {
		return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, card)
	}
	return card
}

// short truncates a long id for the header.
func short(s string) string {
	if len(s) <= 12 {
		return s
	}
	return s[:12]
}

// maxModelLen caps the model name shown in the header so a long provider-scoped
// id (e.g. "anthropic/claude-opus-4-...") can't blow out the header width.
const maxModelLen = 24

// effortHeaderSuffix returns the reasoning-effort token to show beside the model in
// the header, or "" when nothing should render (ADR 0055). It hides the unset state
// honestly: the server echoes "" for an unset/auto effort, and an explicit "auto"
// (defensive — the picker maps auto→"" before sending, but an older path could echo
// it) is treated the same. The value is server-owned, so it is terminal-sanitized.
func effortHeaderSuffix(effort string) string {
	if effort == "" || effort == effortAuto {
		return ""
	}
	return sanitizeTerminal(effort)
}

// truncate clamps s to at most limit display runes, appending an ellipsis when
// it overflows (the "…" counts toward limit). Rune-safe so multibyte model ids
// aren't split mid-character. limit <= 1 yields the raw ellipsis.
func truncate(s string, limit int) string {
	r := []rune(s)
	if len(r) <= limit {
		return s
	}
	if limit <= 1 {
		return "…"
	}
	return string(r[:limit-1]) + "…"
}

// defaultHeaderWidth is the assumed terminal width before the first resize (every
// widthOr caller wants this same fallback, so it is a const, not a parameter).
const defaultHeaderWidth = 80

// widthOr returns the terminal width, or defaultHeaderWidth when unset
// (pre-first-resize).
func (m Model) widthOr() int {
	if m.width > 0 {
		return m.width
	}
	return defaultHeaderWidth
}
