package ui

import (
	"encoding/json"
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

// pendingAsk holds the state of an open permission modal. AskID is the exact
// correlation key sent back in ResumeApproval — it is never derived from the
// tool name. focus tracks which button is highlighted (0=allow-once, 1=always,
// 2=deny). offerAlways gates the middle "always" button: it is offered only for
// the MAIN agent's asks, never for a surfaced subagent ask (a child engine's
// permission policy has a nil learn store, so always-allow would be a silent
// no-op there).
type pendingAsk struct {
	AskID       string
	Tool        string
	Args        string
	Reason      string
	focus       int
	offerAlways bool
}

// isPlanAsk reports whether a permission ask is a plan-approval gate (the model
// called PresentPlan in plan mode). The tool name is the sole discriminator —
// no new proto field is needed.
func isPlanAsk(tool string) bool {
	return tool == "PresentPlan"
}

// isDiffCapableAskTool reports whether a permission ask's tool renders as a
// colourised diff in the modal (matching renderToolDiff's switch). It is the
// ctrl+t routing discriminator (issue #488): a diff-capable ask keeps the
// in-modal ctrl+t diff expand, while every other non-plan ask's ctrl+t opens
// the full-screen args view. A malformed-Edit-args ask still classifies as
// diff-capable (it falls back to JSON args, but the ask's FLAVOUR is the diff
// surface).
func isDiffCapableAskTool(tool string) bool {
	return tool == "Edit" || tool == "Write"
}

// bashAskArgs mirrors the Bash tool's args JSON shape for the ask-args pretty
// tier (mirroring mutatedPath's editDiffArgs/writeDiffArgs precedent). The
// envelope can also carry timeout_ms (engine's bashArgs shape); the ui layer
// knows it as a JSON field NAME, not an import.
type bashAskArgs struct {
	Command   string `json:"command"`
	TimeoutMS int    `json:"timeout_ms"`
}

// askArgsContent derives the pretty and raw display tiers of a non-diff,
// non-plan ask's args (issue #488). ok is false for plan asks and diff-capable
// tools (their surfaces are the plan-review view and the in-modal diff).
//
// The RAW tier is the VERBATIM wire args text — sanitizeTerminal(ask.Args),
// nothing else (no prettyJSON, no re-indent): raw is the escape hatch that can
// never lie, "exactly what am I approving". The PRETTY tier is the readable
// decode: a Bash ask's {"command": …} decodes into the command TEXT (real
// newlines, terminal-sanitized) so a long pipeline reads as shell, not escaped
// JSON, and a non-zero timeout_ms appends a muted "timeout_ms: N" annotation
// line (th styles it) so the pretty tier loses nothing the envelope carries;
// every other tool/shape falls back to prettyJSON. The tiers differ for Bash
// and for any multi-line-tool args, so the raw/pretty toggle is honest on
// those asks; a single short line identical in both tiers (empty, or non-JSON
// passthrough) hides the hint.
func askArgsContent(th theme.Theme, ask pendingAsk) (pretty string, raw string, ok bool) {
	if isPlanAsk(ask.Tool) || isDiffCapableAskTool(ask.Tool) {
		return "", "", false
	}
	raw = sanitizeTerminal(strings.TrimSpace(ask.Args))
	pretty = prettyJSON(ask.Args)
	if ask.Tool == "Bash" {
		var args bashAskArgs
		if err := json.Unmarshal([]byte(strings.TrimSpace(ask.Args)), &args); err == nil && args.Command != "" {
			pretty = sanitizeTerminal(args.Command)
			if args.TimeoutMS != 0 {
				pretty += "\n" + th.Style("muted").Render(fmt.Sprintf("timeout_ms: %d", args.TimeoutMS))
			}
		}
	}
	return pretty, raw, true
}

// askArgsTiersDiffer reports whether the raw tier adds anything over the pretty
// tier — true whenever the two tiers genuinely differ (a decoded Bash command,
// a timeout_ms annotation, or any args whose pretty tier re-indents). The
// toggle hint renders only then; for an ask whose tiers are byte-identical
// (empty args, or a non-JSON single-line passthrough) a "raw" toggle would be
// a lie, so the hint hides.
func askArgsTiersDiffer(th theme.Theme, ask pendingAsk) bool {
	pretty, raw, ok := askArgsContent(th, ask)
	return ok && pretty != raw
}

// wrapAskArgs soft-wraps args display text to width w: normalizeEmojiWidth
// first (ansi.Wrap measures cells on GraphemeWidth while the viewport paints on
// WcWidth — render.go:1230-1235's documented invariant), then ansi.Wrap with
// the established "- " breakpoint set, then an ansi.Hardwrap backstop so an
// unbreakable long token (a space-free URL, a base64 blob) can never run off
// the card. w <= 0 means "unknown width: no wrap".
func wrapAskArgs(s string, w int) string {
	if w <= 0 {
		return s
	}
	return ansi.Hardwrap(ansi.Wrap(normalizeEmojiWidth(s), w, "- "), w, false)
}

// wrapContinuationMarker is the glyph appended to the END of every wrapped
// segment that is NOT the last of its source line — "this logical line
// continues below". It renders in the warning colour so a soft wrap is visibly
// not a natural break (a plain-text reader just sees the glyph; the colour is
// the attention cue on a capable terminal).
const wrapContinuationMarker = " ↩"

// wrapAskArgsContinuations wraps like wrapAskArgs, then appends a styled
// trailing wrapContinuationMarker to every wrapped segment that a SOURCE line
// spills onto the next visual row, so a soft-wrapped long command reads as one
// logical line whose break point is marked at the END of the continuing row
// (never mistaken for a hard newline in the actual args). The marker is styled
// AFTER wrapping so its width is not counted against the wrap budget (it sits
// in the trailing margin of a line that wrapped because it was full — the line
// already ends at the budget).
func wrapAskArgsContinuations(th theme.Theme, s string, w int) string {
	if w <= 0 {
		return s
	}
	styledMarker := th.Style("warning").Render(wrapContinuationMarker)
	markerW := lipgloss.Width(wrapContinuationMarker)
	// Reserve the marker's width from the wrap budget: a segment about to be
	// continued must already end markerW columns early so the appended marker
	// stays inside the budget and never overflows the card/region.
	inner := w - markerW
	var out []string
	for _, srcLine := range strings.Split(s, "\n") {
		segs := strings.Split(wrapAskArgs(srcLine, inner), "\n")
		for i, seg := range segs {
			if i < len(segs)-1 {
				seg += styledMarker
			}
			out = append(out, seg)
		}
	}
	return strings.Join(out, "\n")
}

// planApprovedProceedText is the harness-framed proceed message the TUI sends as
// the follow-up prompt when an operator APPROVES a plan ask interactively, so the
// server starts the execution run (mirroring the ApprovePlan RPC's atomic
// continuation, service.go:2593, and the headless auto-approve continuation,
// service.go:2945).
//
// The ui/client layer CANNOT import engine/agent (the layering rule), so the
// literal is DUPLICATED here. It is a STABLE WIRE CONTRACT: it MUST stay
// byte-identical to engine/agent.PlanApprovedProceedText
// ("Plan approved by operator. Proceed with execution.") — the server RECORDS it
// as the user turn driving execution (ordinary recorded history the model reads),
// and both server-side continuation paths send the SAME constant. A change here
// is a coordinated change in both places (and an engine/CHANGELOG.md note per
// the engine contract).
const planApprovedProceedText = "Plan approved by operator. Proceed with execution."

// renderPermissionModal renders the centred approval card. It is drawn with
// lipgloss.Place over the available area so it reads as a modal overlay. The
// warning border + accent on the focused button make it unmissable. It is a
// method on renderer so it can reuse renderToolDiff: for an Edit/Write ask the
// concrete colourised diff of the change is shown in place of the raw JSON args,
// so the operator approves a real edit rather than an opaque blob. expand is the
// global details toggle (ctrl+t): when on, the diff renders in full instead of
// line-capped, so the collapse marker's "ctrl+t expand" hint is truthful — the
// operator can genuinely reveal every line being authorized before deciding.
// queued is the number of asks waiting FIFO behind this one (len(m.askQueue)):
// when non-zero the title carries a "(1 of N)" badge so the operator knows more
// approvals follow; at zero the modal is byte-identical to the single-ask frame.
//
// This is the GENERIC tool-permission path only. A plan-approval ask
// (isPlanAsk) gets the full-screen SCROLLABLE plan-review view instead (see
// renderPlanReviewView / openPlanReviewView) — the plan is too long to read in a
// small centered card, so it fills the conversation region and scrolls.
func (r *renderer) renderPermissionModal(ask pendingAsk, expand bool, queued, width, height, argsOffset int) string {
	return centerCard(r.th, r.renderPermissionModalBody(ask, expand, queued, width, height, argsOffset), width, height)
}

// renderPermissionModalBody builds the generic permission modal's body CONTENT —
// the exact string centerCard frames. It is the SINGLE source for BOTH the render
// path (renderPermissionModal) and the mouse hit-test (clickgeom.go askButtonAt via
// Model.permissionModalBody), so the body the hit-test measures is byte-identical
// to the body the frame renders.
func (r *renderer) renderPermissionModalBody(ask pendingAsk, expand bool, queued, width, height, argsOffset int) string {
	body, _ := r.permissionModalBodyParts(ask, expand, queued, width, height, argsOffset)
	return body
}

// permissionModalArgsMaxLines caps the in-card args mini-viewport (issue #488):
// a non-diff ask's wrapped args render at most this many rows in the modal, with
// a scroll/full-args hint line appended when rows are hidden. Only long-args
// asks grow the modal — up to the cap — so short asks keep today's compact card
// byte-for-byte.
const permissionModalArgsMaxLines = 10

// permissionModalBodyReserve is the fixed-line budget the modal body keeps for
// everything that is NOT the args region (title + tool line + buttons + the
// always footnote + the args hint line + the askCard border/padding frame). The
// args region declares min(natural, cap, region-reserve) rows so centerCard
// never receives an over-region body even on a tiny terminal.
const permissionModalBodyReserve = 16

// permissionModalBodyParts builds the generic permission modal's body CONTENT AND
// reports the button box's top row within it. It is the SINGLE source for BOTH the
// render path (renderPermissionModal) and the mouse hit-test (clickgeom.go
// askButtonAt via Model.permissionModalBody): the hit-test must not re-derive the
// buttons row from the trailing write order (a new trailing line would silently
// desync it), so the builder that LAYS OUT the body also owns where the buttons
// landed.
//
// width/height are the render region's dimensions (0 = unknown): the args
// region of a non-diff ask wraps DETERMINISTICALLY at the card content width
// derived from width (min(width,100) minus the askCard frame), so the render
// and the hit-test measure the same wrap, and height bounds the region's row
// budget so centerCard never receives an over-region body. argsOffset is the
// args mini-viewport's YOffset.
func (r *renderer) permissionModalBodyParts(ask pendingAsk, expand bool, queued, width, height, argsOffset int) (body string, buttonsRow int) {
	th := r.th
	titleText := "Permission required"
	if queued > 0 {
		titleText = fmt.Sprintf("Permission required (1 of %d)", queued+1)
	}
	title := th.Style("askTitle").Render(titleText)

	// All ask.* fields are server-derived and rendered via lipgloss, so they MUST
	// be terminal-sanitized: an attacker who controls a tool result could
	// otherwise embed escapes to redraw/spoof this very approval modal. (Args is
	// sanitized inside prettyJSON; the diff path sanitizes internally.)
	var b strings.Builder
	b.WriteString(title + "\n\n")
	b.WriteString(th.Style("toolName").Render(sanitizeTerminal(ask.Tool)) + "\n")
	// Prefer a concrete diff for Edit/Write. Collapsed by default (line-capped, so
	// a huge Write can't grow the modal off-screen); ctrl+t (expand) reveals the
	// full diff right here at the gate. Fall back to pretty JSON for any other
	// tool, or when the Edit/Write args don't parse into the expected shape.
	if diff, ok := r.renderToolDiff(ask.Tool, ask.Args, expand); ok {
		if diff != "" {
			b.WriteString(th.Style("muted").Render("changes:") + "\n")
			b.WriteString(diff + "\n")
		}
		if ask.Reason != "" {
			b.WriteString("\n" + th.Style("muted").Render(sanitizeTerminal(ask.Reason)) + "\n")
		}
	} else if pretty, _, ok := askArgsContent(th, ask); ok && pretty != "" {
		// Non-diff ask: the args region is a height-capped mini-viewport of the
		// WRAPPED pretty tier (a Bash ask's decoded command text, else pretty
		// JSON). The region declares min(natural, cap, region-budget) rows and
		// scrolls via argsOffset; hidden rows append a hint line advertising the
		// scroll keys and the ctrl+t full-args view. A pathological no-args ask
		// keeps today's shape exactly (no region, no hint).
		// The args region is rendered through the askArgs style (bright Text + a
		// Tool-coloured left accent bar) so the command being approved reads
		// distinct from the muted reason/hint around it. The block is pinned to
		// blockWidth (the widest WRAPPED line over the whole content) so a scroll
		// onto shorter lines never narrows the block and re-centres the card.
		argsStyle := th.Style("askArgs")
		argsRegion := askArgsMiniViewport(th, pretty, width, height)
		if argsOffset > argsRegion.maxOffset {
			argsOffset = argsRegion.maxOffset
		}
		if argsOffset < 0 {
			argsOffset = 0
		}
		lines := argsRegion.lines
		// lipgloss Width() sets the CONTENT-region width, which the accent-bar
		// border+padding are drawn INSIDE — so pin to blockWidth + the style's
		// frame, or a blockWidth-long line would wrap onto a second row.
		blockTotal := argsRegion.blockWidth + argsStyle.GetHorizontalFrameSize()
		b.WriteString(argsStyle.Width(blockTotal).Render(strings.Join(lines[argsOffset:argsOffset+argsRegion.viewRows], "\n")) + "\n")
		// A blank spacer separates the command block from the metadata (hint +
		// reason) below it — the break belongs BETWEEN the thing being approved
		// and the info about it, not wedged between the hint and the reason.
		b.WriteString("\n")
		// The hint line is UNCONDITIONAL for a non-diff ask: even a short one opens
		// the full-screen args view on ctrl+t (a conditional hint hid the affordance
		// on exactly the short asks that still benefit from the full view).
		hk := r.marks
		hint := hk.expandTools + " full args"
		if argsRegion.maxOffset > 0 {
			hint = "… " + hk.scroll + " scroll · " + hint
		}
		b.WriteString(th.Style("muted").Render(hint) + "\n")
		if ask.Reason != "" {
			b.WriteString(th.Style("muted").Render(sanitizeTerminal(ask.Reason)) + "\n")
		}
	} else if args := prettyJSON(ask.Args); args != "" {
		b.WriteString(th.Style("toolArgs").Render(args) + "\n")
		if ask.Reason != "" {
			b.WriteString("\n" + th.Style("muted").Render(sanitizeTerminal(ask.Reason)) + "\n")
		}
	} else if ask.Reason != "" {
		b.WriteString("\n" + th.Style("muted").Render(sanitizeTerminal(ask.Reason)) + "\n")
	}

	// Three buttons when always-allow is offered (main-agent asks), two otherwise
	// (surfaced subagent asks). focus indexes {allow-once, always, deny}; for a
	// two-button modal focus only ever takes 0 (allow) or 2 (deny).
	//
	// The mnemonics are honest about the LIVE approval chords (issue #457 SPEC/UX
	// gap: they were hard-coded to the word-embedded form). With the DEFAULT
	// approval chords (a/w/d) the bracketed letter sits inside "Allow"/"Always"/
	// "Deny" at its natural position, so the case follows the WORD's spelling
	// (the "w" in "Al[w]ays" is lowercase because it is a middle letter, not
	// because the chord is) — the historical word-embedded form renders
	// byte-for-byte. When an approval chord is rebound AWAY from its default
	// word letter, the wordplay no longer holds, so the button degrades to an
	// honest STANDALONE form ("[Y] allow" / "[Q] always allow" / "[N] deny", or
	// "[ctrl+y] allow" for a modified chord) so every displayed chord is the
	// one that actually fires.
	hk := r.marks
	buttons := permissionButtonsLine(th, hk, ask)
	// The buttons row is the NEXT content line after what is written so far (the
	// leading "\n" joins it below the reason/args). Capture it BEFORE writing so
	// the hit-test reads the SAME row the render lays out — never a re-derived
	// offset that a later trailing write would silently move.
	buttonsRow = lipgloss.Height(b.String())
	b.WriteString("\n" + buttons)
	if ask.offerAlways {
		b.WriteString("\n" + th.Style("muted").Render(approvalAlwaysFootnote(hk.allowAlways)))
	}

	return b.String(), buttonsRow
}

// askArgsRegion is the wrapped + capped + budget-bounded layout of the modal's
// args mini-viewport, computed ONCE (by askArgsMiniViewport) so the
// render path, the wheel/key scroll bound, and the hint's "rows hidden" clause
// can never disagree (the scroll-range helper used to duplicate this arithmetic
// and drifted).
type askArgsRegion struct {
	lines      []string // the wrapped args lines (all of them)
	viewRows   int      // how many lines the modal shows at once
	maxOffset  int      // the largest legal YOffset (0 when everything fits)
	blockWidth int      // the content width of the widest wrapped line — the block is pinned to this so scrolling never resizes the card
}

// askArgsMiniViewport computes the modal's args mini-viewport layout for a
// non-diff ask's pretty tier at the given region geometry. The wrap budget is
// the card content width minus the askArgs accent-bar frame, so every styled
// line stays inside the card. blockWidth is the widest wrapped line's width
// (measured over ALL lines, not the visible window): the render pins the args
// block to it via the style's Width, so a scroll onto shorter lines does not
// narrow the block and re-centre the card (the width is a property of the
// content, not the viewport position).
func askArgsMiniViewport(th theme.Theme, pretty string, width, height int) askArgsRegion {
	cw := askArgsCardContentWidth(th, width) - th.Style("askArgs").GetHorizontalFrameSize()
	// Continuation-marked wrap (a trailing ↩ on a continued row) so a soft-wrapped
	// long command reads as one logical line with visible joins — the SAME marking
	// the full-screen args view uses, so both surfaces agree on what a wrap looks
	// like.
	lines := strings.Split(wrapAskArgsContinuations(th, pretty, cw), "\n")
	blockWidth := 0
	for _, ln := range lines {
		if w := lipgloss.Width(ln); w > blockWidth {
			blockWidth = w
		}
	}
	viewRows := len(lines)
	if viewRows > permissionModalArgsMaxLines {
		viewRows = permissionModalArgsMaxLines
	}
	if budget := height - permissionModalBodyReserve; height > 0 && viewRows > budget {
		viewRows = budget
	}
	if viewRows < 1 {
		viewRows = 1
	}
	maxOffset := len(lines) - viewRows
	if maxOffset < 0 {
		maxOffset = 0
	}
	return askArgsRegion{lines: lines, viewRows: viewRows, maxOffset: maxOffset, blockWidth: blockWidth}
}

// permissionModalMaxWidth caps the permission card's width on a wide terminal:
// the card grows with the terminal up to this many columns, then holds (a modal
// should read as a card, not a full-width band — but it should USE a generous
// share of a large window rather than a cramped 100-col strip).
const permissionModalMaxWidth = 132

// askArgsCardContentWidth is the deterministic wrap budget for the modal's args
// region: the card's content width at the region width the render lays out.
// centerCard never grows a card past its content, and the card caps at
// permissionModalMaxWidth columns on a wide terminal, so the budget is
// min(width, permissionModalMaxWidth) minus the askCard style's own horizontal
// frame (border+padding via GetHorizontalFrameSize — the single source of
// truth, so it tracks theme.go edits). Both the render and the hit-test derive
// it from the same inputs, so they can never disagree about the wrap.
// width <= frame means "unknown/tiny: no wrap" (0).
func askArgsCardContentWidth(th theme.Theme, width int) int {
	frame := th.Style("askCard").GetHorizontalFrameSize()
	w := width
	if w > permissionModalMaxWidth {
		w = permissionModalMaxWidth
	}
	if w <= frame+1 {
		return 0
	}
	return w - frame
}

// permissionModalBody returns the generic permission modal's body CONTENT and the
// button box's top row within it for the CURRENT front ask, so the mouse hit-test
// (clickgeom.go askButtonAt) measures the same body the render path lays out. It
// delegates to the renderer with the model's live expand/queue/offset state and
// the SAME region geometry renderBody passes; the hit-test path re-applies the
// askCard style + centering arithmetic itself.
func (m Model) permissionModalBody() (body string, buttonsRow int) {
	return m.rend.permissionModalBodyParts(m.ask, m.expandTools, len(m.askQueue), m.width, m.vp.Height(), m.askVPOffset)
}

// approvalButtonSep is the exact separator lipgloss.JoinHorizontal places between
// the styled approval buttons in permissionButtonsLine / planButtonsLine. It is the
// SINGLE source for BOTH the render (the JoinHorizontal calls below) and the mouse
// hit-test column arithmetic (clickgeom.go's askButtonRects), so changing the
// separator keeps the render and the hit-test in lockstep — a bare "  " literal on
// each side would silently desync them.
const approvalButtonSep = "  "

// approvalButton is a rendered approval action and the verdict-focus it selects.
// Both the render and hit-test paths consume this value so their labels, styles, and
// widths cannot drift.
type approvalButton struct {
	focus int
	label string
}

func approvalButtons(th theme.Theme, hk helpKeys, ask pendingAsk, plan bool) []approvalButton {
	label := func(focus int) string {
		if plan {
			switch focus {
			case 0:
				return planApprovalButtonLabel(hk.allow, "Allow", "approve & run")
			case 1:
				return planApprovalButtonLabel(hk.allowAlways, "Always", "auto-accept edits")
			default:
				return planApprovalButtonLabel(hk.deny, "Deny", "iterate")
			}
		}
		switch focus {
		case 0:
			return approvalButtonLabel(hk.allow, "Allow", "allow")
		case 1:
			return approvalButtonLabel(hk.allowAlways, "Always", "always allow")
		default:
			return approvalButtonLabel(hk.deny, "Deny", "deny")
		}
	}
	foci := []int{0, 2}
	if ask.offerAlways {
		foci = []int{0, 1, 2}
	}
	buttons := make([]approvalButton, 0, len(foci))
	for _, focus := range foci {
		style := th.Style("askButton")
		if ask.focus == focus {
			style = th.Style("askButtonActive")
		}
		buttons = append(buttons, approvalButton{focus: focus, label: style.Render(label(focus))})
	}
	return buttons
}

func renderApprovalButtons(buttons []approvalButton) string {
	parts := make([]string, 0, len(buttons)*2-1)
	for i, button := range buttons {
		if i > 0 {
			parts = append(parts, approvalButtonSep)
		}
		parts = append(parts, button.label)
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, parts...)
}

// permissionButtonsLine renders the generic permission modal's joined button row
// (Allow [· Always] · Deny) with the LIVE focus style.
func permissionButtonsLine(th theme.Theme, hk helpKeys, ask pendingAsk) string {
	return renderApprovalButtons(approvalButtons(th, hk, ask, false))
}

// planButtonsLine renders the plan-review action bar's joined button row
// (approve & run [· auto-accept edits] · iterate) with the LIVE focus style. Plan
// wording intentionally differs from the generic permission modal.
func planButtonsLine(th theme.Theme, hk helpKeys, ask pendingAsk) string {
	return renderApprovalButtons(approvalButtons(th, hk, ask, true))
}

// planReviewFooterHeight is the rows reserved at the bottom of the plan-review
// view for the pinned action bar (buttons + the auto-accept footnote). The bar
// is drawn as the last lines of the view so it never scrolls away — the operator
// reads (scrolls the planVP), then acts.
const planReviewFooterHeight = 3

// planScrollHint renders the muted scroll hint shown as the LAST line of the
// pinned action bar. Arrow scrolling and the mouse wheel are genuinely fixed;
// ScrollU/ScrollD and ScrollTop/ScrollBottom read the LIVE keyMap markings so a
// rebind is advertised honestly (issue #457). Defaults remain byte-identical.
func planScrollHint(hk helpKeys) string {
	return "scroll: ↑/↓ · " + hk.scroll + " · " + hk.jump + " · mouse wheel"
}

// openPlanReviewView populates the dedicated plan-review viewport (planVP) with
// the FULL plan and sizes it to the conversation region. It is the REPLACEMENT
// for the collapsed centered-card approach: the plan fills the conversation
// region (minus the pinned action-bar rows at the bottom) and scrolls, so a long
// plan is readable in full without a ctrl+t expand gate. It is called lazily
// when a plan ask becomes the front ask (PermissionAskMsg reducer / advanceAsk
// successor) and re-called when the region geometry changes (relayout/onResize)
// so a resize re-wraps the plan at the new width. Idempotent: re-populating the
// same plan at the same geometry is a no-op cost.
//
// The plan content is the FULL glamour-wrapped plan (markdownWidth at the view's
// content width) — NO line cap, NO collapse marker, NO ctrl+t gate. The
// model-authored plan text is terminal-sanitized (it crosses the wire from the
// model, same as tool args). Fallbacks (issue #206 backwards-compat): no `plan`
// arg → the optional `note`; no note → the "plan ready for operator approval"
// reason line — all rendered scrollable, no break.
//
// Scroll-offset preservation: a re-population triggered by a geometry change
// (relayout/onResize) PRESERVES the operator's current YOffset (clamped to the
// new valid range) so a resize mid-read does not yank the view back to the top.
// Only the initial open (planVPReady was false) starts at the top.
func (m *Model) openPlanReviewView(ask pendingAsk, queued int, effectiveModel string) {
	th := m.deps.Theme
	r := m.rend
	width := m.width
	// The plan-review view occupies the SAME region the conversation viewport
	// does (the body region), so it shares m.vp's height/width budget.
	height := m.vp.Height()

	// The pinned action-bar rows reserved at the bottom of the body region.
	vpHeight := height - planReviewFooterHeight
	if vpHeight < 1 {
		vpHeight = 1
	}

	// Short-circuit a no-op re-population: if the geometry is unchanged AND the
	// plan ask is the same (Args + Tool + queued + model), the planVP is already
	// correct — skip the expensive glamour re-wrap + SetContent. relayout fires
	// on every message while a plan ask is open, so without this guard a plan
	// review would re-render the plan on every keypress. The geometry tracking
	// (planVPWidth/planVPHeight) plus the ask fingerprint catches the no-op.
	if m.planVPReady &&
		m.planVPWidth == width && m.planVPHeight == vpHeight &&
		m.planVPFingerprint == planAskFingerprint(ask, queued, effectiveModel) {
		return
	}

	// Preserve the operator's reading position across a re-population (a resize
	// re-wraps the plan; the offset is re-clamped to the new valid range after
	// SetContent). Only a FRESH open (not yet ready) starts at the top.
	prevYOffset := m.planVP.YOffset()
	freshOpen := !m.planVPReady

	// Build the header (title + model line) that sits at the TOP of the
	// scrollable content. It scrolls with the plan (the pinned action bar at the
	// bottom is what stays reachable; the header is the first thing read).
	titleText := "Plan ready for review"
	if queued > 0 {
		titleText = fmt.Sprintf("Plan ready for review (1 of %d)", queued+1)
	}
	var head strings.Builder
	head.WriteString(th.Style("askTitle").Render(titleText) + "\n")
	head.WriteString(th.Style("toolName").Render(sanitizeTerminal(ask.Tool)) + "\n")
	// Show the plan model. The execute model is not separately echoed to the
	// TUI — show a graceful line naming the default model when known, or a
	// generic "session default model" when unknown.
	modelLine := "execute model: session default model"
	if effectiveModel != "" {
		modelLine = fmt.Sprintf("plan model: %s · execute model: session default model", sanitizeTerminal(effectiveModel))
	}
	head.WriteString(th.Style("muted").Render(modelLine) + "\n")

	// Render the FULL plan content the operator is approving. The plan rides the
	// PresentPlan `plan` argument; it reaches here as ask.Args (the raw JSON
	// string). Parse it, render through markdownWidth at the view's content
	// width (so long markdown lines wrap, not run off-screen). NO line cap, NO
	// collapse — the whole plan is scrollable. Fall back to `note` then the
	// reason line for older models (issue #206 backwards-compat).
	var body string
	if planText := planBodyFromArgs(ask.Args); planText != "" {
		cw := planReviewContentWidth(width)
		wrapped := r.markdownWidth(planText, cw)
		body = th.Style("muted").Render("plan:") + "\n" + wrapped + "\n"
	} else if ask.Reason != "" {
		body = th.Style("muted").Render(sanitizeTerminal(ask.Reason)) + "\n"
	}

	content := head.String() + "\n" + body

	m.planVP.SetWidth(width)
	m.planVP.SetHeight(vpHeight)
	m.planVP.SetContent(content)
	if freshOpen {
		// A fresh plan view opens at the TOP (the operator reads from the title
		// down). SetYOffset(0) clamps to the valid range; GotoTop is equivalent
		// but explicit about intent.
		m.planVP.SetYOffset(0)
	} else {
		// Re-population (a resize): preserve the operator's reading position,
		// re-clamped to the new valid range (SetContent may have clamped it
		// already, but be explicit).
		m.planVP.SetYOffset(prevYOffset)
	}
	m.planVPReady = true
	m.planVPWidth = width
	m.planVPHeight = vpHeight
	m.planVPFingerprint = planAskFingerprint(ask, queued, effectiveModel)
}

// planReviewContentWidth is the wrap budget for the plan body inside the
// plan-review view: the view width minus a small indent so the plan text aligns
// with the 1-col-padded header/footer chrome (mirroring the conversation
// block indent) and a safety margin so a glamour-rendered line never runs flush
// to the right edge. It floors at a readable minimum and degrades to 0 (no
// wrap) on a tiny/unknown width so an unknown size still renders the bare plan.
func planReviewContentWidth(width int) int {
	const (
		indent = 1 // align with the 1-col-padded header/footer chrome
		margin = 1 // safety so a wrapped line never touches the right edge
		minW   = 20
	)
	w := width - indent - margin
	if w < minW {
		return 0
	}
	return w
}

// clearPlanReview tears down the plan-review viewport: it drops the content and
// marks it not-ready so neither the render path nor the scroll-key routing
// touch it. Called whenever the plan ask resolves (any verdict), is retracted,
// or the run/session ends — mirroring how m.ask/m.askQueue are cleared — so a
// stale planVP never leaks across asks or sessions.
func (m *Model) clearPlanReview() {
	m.planVP.SetContent("")
	m.planVP.SetYOffset(0)
	m.planVPReady = false
	m.planVPWidth = 0
	m.planVPHeight = 0
	m.planVPFingerprint = ""
}

// planAskFingerprint is the identity of a plan ask the planVP content depends
// on (the tool name, the raw args JSON carrying the plan/note, the queued count
// for the title badge, and the effective model line). It lets openPlanReviewView
// short-circuit a no-op re-population when NOTHING about the rendered plan
// changed (only a geometry change or a new ask re-populates).
func planAskFingerprint(ask pendingAsk, queued int, effectiveModel string) string {
	return ask.Tool + "\x00" + ask.Args + "\x00" + ask.Reason + "\x00" +
		fmt.Sprintf("%d", queued) + "\x00" + effectiveModel
}

// planReviewLayout is the rendered plan-review body and the action button box's
// exact row span within it. The renderer and mouse hit-test share it so viewport
// height, the joining newline, and Lipgloss's three-row button box stay in sync.
type planReviewLayout struct {
	content                   string
	buttonsRow, buttonsHeight int
}

func (m Model) planReviewLayout(ask pendingAsk) planReviewLayout {
	th := m.deps.Theme
	var vpView string
	if m.planVPReady {
		vpView = m.planVP.View()
	}

	hk := m.helpKeyMarkings()
	buttons := planButtonsLine(th, hk, ask)
	var bar strings.Builder
	bar.WriteString(buttons)
	if ask.offerAlways {
		bar.WriteString("\n" + th.Style("muted").Render(
			"auto-accept allows every edit in the execution phase for the rest of this session"))
	}
	bar.WriteString("\n" + th.Style("muted").Render(planScrollHint(hk)))

	layout := planReviewLayout{buttonsHeight: lipgloss.Height(buttons)}
	layout.content = vpView
	if layout.content != "" {
		// Count rendered lines, rather than lipgloss.Height: a viewport can retain a
		// trailing newline, which occupies a screen row before the joining newline.
		layout.buttonsRow = strings.Count(layout.content, "\n") + 1
		layout.content += "\n"
	}
	layout.content += bar.String()
	return layout
}

// renderPlanReviewView renders the full-screen scrollable plan-review view: the
// plan-review viewport (planVP.View(), already populated by openPlanReviewView)
// stacked above the PINNED action bar (buttons + auto-accept footnote + scroll
// hint) that stays reachable regardless of how far the operator has scrolled.
// It replaces the centered card (renderPlanApprovalModal) for the plan path.
func (m Model) renderPlanReviewView(ask pendingAsk) string {
	return m.planReviewLayout(ask).content
}

// ---------------------------------------------------------------------------
// Full-screen ask-args view (issue #488)
// ---------------------------------------------------------------------------

// argsReviewFooterHeight is the rows reserved at the bottom of the full-screen
// ask-args view for the pinned action bar (buttons + the always footnote + the
// scroll/raw hint). Same shape as planReviewFooterHeight.
const argsReviewFooterHeight = 3

// openAskArgsView populates the dedicated ask-args viewport (argsVP) with the
// FULL pretty/raw args of the front ask and sizes it to the conversation
// region — the non-diff, non-plan analogue of openPlanReviewView. It is called
// when the operator opens the view (ctrl+t on a non-diff ask) and re-called on
// a geometry change (relayout/onResize) so a resize re-wraps at the new width.
// Idempotent: re-populating the same ask at the same geometry and raw flag is a
// no-op (the fingerprint catches it). A re-population PRESERVES the operator's
// YOffset (clamped); only a fresh open starts at the top.
func (m *Model) openAskArgsView(ask pendingAsk, queued int) {
	th := m.deps.Theme
	width := m.width
	// The ask-args view occupies the SAME region the conversation viewport does
	// (the body region), so it shares m.vp's height/width budget.
	height := m.vp.Height()

	vpHeight := height - argsReviewFooterHeight
	if vpHeight < 1 {
		vpHeight = 1
	}

	if m.argsVPReady &&
		m.argsVPWidth == width && m.argsVPHeight == vpHeight &&
		m.argsVPFingerprint == argsAskFingerprint(ask, queued, m.argsViewRaw) {
		return
	}

	// Preserve the operator's reading position across a re-population (a resize
	// re-wraps the args; the offset is re-clamped to the new valid range by the
	// SetYOffset below). Only a FRESH open (not yet ready) starts at the top.
	prevYOffset := m.argsVP.YOffset()
	freshOpen := !m.argsVPReady

	// Header: title (+ queue badge) carrying the tool name ON ONE LINE ("Ask args:
	// Bash"). NO model line (the plan-review view's model line is plan-specific)
	// and NO separate tool line — the title carries the tool.
	titleText := "Ask args: " + sanitizeTerminal(ask.Tool)
	if queued > 0 {
		titleText = fmt.Sprintf("Ask args: %s (1 of %d)", sanitizeTerminal(ask.Tool), queued+1)
	}
	var head strings.Builder
	head.WriteString(th.Style("askTitle").Render(titleText) + "\n")

	pretty, raw, ok := askArgsContent(th, ask)
	tier := pretty
	if m.argsViewRaw {
		tier = raw
	}
	// NOTE: wrap BEFORE styling — styling a wrapped string would let lipgloss
	// re-pad its lines (whitespace-fill beyond the wrap budget).
	var body string
	if ok && tier != "" {
		body = th.Style("toolArgs").Render(wrapAskArgsContinuations(th, tier, planReviewContentWidth(width))) + "\n"
	}
	if ask.Reason != "" {
		body += "\n" + th.Style("muted").Render(sanitizeTerminal(ask.Reason)) + "\n"
	}

	content := head.String() + "\n" + body

	m.argsVP.SetWidth(width)
	m.argsVP.SetHeight(vpHeight)
	m.argsVP.SetContent(content)
	if freshOpen {
		m.argsVP.SetYOffset(0)
	} else {
		m.argsVP.SetYOffset(prevYOffset)
	}
	m.argsVPReady = true
	m.argsVPWidth = width
	m.argsVPHeight = vpHeight
	m.argsVPFingerprint = argsAskFingerprint(ask, queued, m.argsViewRaw)
}

// clearAskArgsView tears down the full-screen ask-args view AND resets the
// modal's args mini-viewport offset: it drops the viewport content, marks it
// not-ready/closed, and resets the raw toggle so the next ask opens pretty.
// Called whenever the ask resolves (any verdict), is retracted, advances to a
// queued successor, or the run/session ends — mirroring clearPlanReview — so a
// stale argsVP never leaks across asks or sessions.
func (m *Model) clearAskArgsView() {
	m.argsVP.SetContent("")
	m.argsVP.SetYOffset(0)
	m.argsVPReady = false
	m.argsVPWidth = 0
	m.argsVPHeight = 0
	m.argsVPFingerprint = ""
	m.argsViewRaw = false
	m.argsViewOpen = false
	m.askVPOffset = 0
}

// argsAskFingerprint is the identity of an ask the argsVP content depends on
// (tool, raw args JSON, reason, the queued count for the title badge, and the
// raw/pretty tier flag — a toggle re-populates). It lets openAskArgsView
// short-circuit a no-op re-population.
func argsAskFingerprint(ask pendingAsk, queued int, raw bool) string {
	rawBit := "0"
	if raw {
		rawBit = "1"
	}
	return ask.Tool + "\x00" + ask.Args + "\x00" + ask.Reason + "\x00" +
		fmt.Sprintf("%d", queued) + "\x00" + rawBit
}

// argsReviewLayout is the rendered ask-args view body and the action button
// box's exact row span within it — the ask-args analogue of planReviewLayout,
// shared by the renderer and the mouse hit-test so viewport height, the joining
// newline, and Lipgloss's three-row button box stay in sync.
type argsReviewLayout struct {
	content                   string
	buttonsRow, buttonsHeight int
}

func (m Model) argsReviewLayout(ask pendingAsk) argsReviewLayout {
	th := m.deps.Theme
	var vpView string
	if m.argsVPReady {
		vpView = m.argsVP.View()
	}

	hk := m.helpKeyMarkings()
	buttons := permissionButtonsLine(th, hk, ask)
	var bar strings.Builder
	bar.WriteString(buttons)
	if ask.offerAlways {
		bar.WriteString("\n" + th.Style("muted").Render(approvalAlwaysFootnote(hk.allowAlways)))
	}
	bar.WriteString("\n" + th.Style("muted").Render(argsScrollHint(hk, askArgsTiersDiffer(th, ask))))

	layout := argsReviewLayout{buttonsHeight: lipgloss.Height(buttons)}
	layout.content = vpView
	if layout.content != "" {
		// Count rendered lines, rather than lipgloss.Height: a viewport can retain a
		// trailing newline, which occupies a screen row before the joining newline.
		layout.buttonsRow = strings.Count(layout.content, "\n") + 1
		layout.content += "\n"
	}
	layout.content += bar.String()
	return layout
}

// argsScrollHint renders the muted scroll/toggle hint shown as the LAST line of
// the ask-args view's pinned action bar. The raw/pretty clause is advertised
// only when the tiers genuinely differ (askArgsTiersDiffer) — for an ask whose
// tiers are byte-identical the toggle would be a no-op hint, so it hides.
func argsScrollHint(hk helpKeys, tiersDiffer bool) string {
	hint := "scroll: ↑/↓ · " + hk.scroll + " · " + hk.jump + " · mouse wheel"
	if tiersDiffer {
		hint += " · " + hk.rawArgs + " raw|pretty"
	}
	return hint + " · " + hk.cancel + " back"
}

// renderAskArgsView renders the full-screen scrollable ask-args view: the
// args viewport (argsVP.View(), already populated by openAskArgsView) stacked
// above the PINNED action bar (the GENERIC permission buttons + the always
// footnote + the scroll/raw hint) that stays reachable regardless of how far
// the operator has scrolled.
func (m Model) renderAskArgsView(ask pendingAsk) string {
	return m.argsReviewLayout(ask).content
}

// planBodyFromArgs extracts the PresentPlan `plan` (falling back to `note`) from
// the raw JSON args string and returns the raw, terminal-sanitized plan text.
// The caller (openPlanReviewView) is responsible for wrapping (via
// markdownWidth at the view's content width). Returns "" when neither plan nor
// note is present (the caller falls back to the reason line). Malformed JSON
// yields "" so an older model that omitted the `plan` arg degrades gracefully.
func planBodyFromArgs(rawArgs string) string {
	rawArgs = strings.TrimSpace(rawArgs)
	if rawArgs == "" {
		return ""
	}
	var args struct {
		Plan string `json:"plan"`
		Note string `json:"note"`
	}
	if err := json.Unmarshal([]byte(rawArgs), &args); err != nil {
		return ""
	}
	body := args.Plan
	if body == "" {
		body = args.Note
	}
	if body == "" {
		return ""
	}
	return sanitizeTerminal(strings.TrimRight(body, "\n"))
}
