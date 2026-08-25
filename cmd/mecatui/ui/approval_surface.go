package ui

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

// approvalRender exposes only the shared renderer operations approval needs. Its
// closures reuse renderer's diff implementation and width-keyed Glamour cache;
// the surface cannot otherwise reach or mutate the broad renderer.
type approvalRender struct {
	diff     func(name, rawArgs string, expand bool) (string, bool)
	markdown func(src string, width int) string
}

func newApprovalRender(r *renderer) approvalRender {
	if r == nil {
		return approvalRender{}
	}
	return approvalRender{diff: r.renderToolDiff, markdown: r.markdownWidth}
}

// approvalSurface is constructed only when the first ask arrives. Its state,
// queue, dedupe set, and view caches die together when the parent discards it.
type approvalSurface struct {
	// Visible approval and FIFO asks waiting behind it.
	ask         pendingAsk
	queue       []pendingAsk
	resumePhase phase

	// In-card args viewport position.
	askVPOffset int

	// Plan-review view cache.
	planVP            viewport.Model // view cache: plan content and scroll position
	planVPReady       bool           // view cache: content initialized for this frame identity
	planVPWidth       int            // view cache: render width
	planVPHeight      int            // view cache: render height
	planVPFingerprint string         // view cache: ask, queue, model, and geometry identity

	// Full-screen ask-args view cache.
	argsVP            viewport.Model // view cache: args content and scroll position
	argsVPReady       bool           // view cache: content initialized for this frame identity
	argsVPWidth       int            // view cache: render width
	argsVPHeight      int            // view cache: render height
	argsVPFingerprint string         // view cache: ask, queue, args tier, and geometry identity
	argsViewRaw       bool
	argsViewOpen      bool

	// Current render geometry.
	regionW, regionH int
	intent           surfaceIntent
	resolvedAsks     map[string]struct{}

	// Immutable inputs captured at Open.
	deps        surfaceDeps
	render      approvalRender
	sessionID   string
	modelID     string
	expandTools bool

	// hits is the current render frame's verdict hit map.
	hits map[HitID]client.Verdict
}

func (s *approvalSurface) Render(width, height int) (string, []ClickableRegion) {
	if s.render.diff == nil || s.render.markdown == nil {
		return "", nil
	}
	s.regionW, s.regionH = width, height
	var body string
	var rects []buttonRect
	var buttonsRow, buttonsHeight int
	switch {
	case s.argsViewOpen:
		s.openArgs(width, height)
		layout := s.argsLayout()
		body, buttonsRow, buttonsHeight = layout.content, layout.buttonsRow, layout.buttonsHeight
		rects = askButtonRects(s.deps.theme, s.deps.marks, s.ask, false)
	case isPlanAsk(s.ask.Tool):
		s.openPlan(width, height)
		layout := s.planLayout()
		body, buttonsRow, buttonsHeight = layout.content, layout.buttonsRow, layout.buttonsHeight
		rects = askButtonRects(s.deps.theme, s.deps.marks, s.ask, true)
	default:
		body, buttonsRow = s.permissionModalBodyParts(width, height)
		buttonsHeight = 3 // every Lipgloss approval button box is three rows.
		rects = askButtonRects(s.deps.theme, s.deps.marks, s.ask, false)
	}
	s.hits = make(map[HitID]client.Verdict, len(rects))
	regions := make([]ClickableRegion, 0, len(rects))
	for _, rect := range rects {
		id := s.deps.hits.allocate()
		s.hits[id] = rect.verdict
		regions = append(regions, ClickableRegion{rect: cellRect{x0: rect.x0, x1: rect.x1, y0: buttonsRow, y1: buttonsRow + buttonsHeight}, hit: id})
	}
	return body, regions
}

func (s *approvalSurface) HandleKey(msg tea.KeyPressMsg) (tea.Cmd, bool, bool) {
	if key.Matches(msg, s.deps.keys.ExpandTools) {
		if s.approvalExpandToggle() {
			s.intent = nil
			return nil, true, false
		}
		s.expandTools = !s.expandTools
		s.intent = setExpandToolsIntent{expand: s.expandTools}
		return nil, true, false
	}
	cmd, intent := s.onApprovalKey(msg)
	if intent != nil {
		s.intent = intent
	}
	return cmd, true, false
}

func (s *approvalSurface) HandleMsg(msg tea.Msg) (tea.Cmd, bool, bool) {
	if retract, ok := msg.(client.PermissionRetractMsg); ok {
		if intent := s.applyPermissionRetract(retract, true); intent != nil {
			s.intent = intent
		}
		return nil, true, false
	}
	hit, ok := msg.(surfaceHitMsg)
	if !ok {
		return nil, false, false
	}
	verdict, ok := s.hits[hit.ID]
	if !ok {
		return nil, true, false
	}
	s.intent = s.resolveAsk(verdict)
	return nil, true, false
}

func (s *approvalSurface) HandleWheel(msg tea.MouseWheelMsg) (tea.Cmd, bool) {
	if s.argsViewOpen {
		if s.argsVPReady {
			var cmd tea.Cmd
			s.argsVP, cmd = s.argsVP.Update(msg)
			return cmd, true
		}
		return nil, true
	}
	if isPlanAsk(s.ask.Tool) {
		if s.planVPReady {
			var cmd tea.Cmd
			s.planVP, cmd = s.planVP.Update(msg)
			return cmd, true
		}
		return nil, true
	}
	if !isDiffCapableAskTool(s.ask.Tool) {
		step := 3
		if msg.Mouse().Button == tea.MouseWheelUp {
			step = -3
		}
		s.miniScroll(step, s.miniScrollRange())
	}
	return nil, true
}

// Close is intentionally a no-op: closeModal discards this ephemeral surface and
// clears the parent-owned frame hit and geometry caches.
func (*approvalSurface) Close() {}
func (s *approvalSurface) modalPlacement() modalPlacement {
	if s.argsViewOpen || isPlanAsk(s.ask.Tool) {
		return modalPlacementFill
	}
	return modalPlacementCard
}

type approvalResolvedIntent struct {
	askID   string
	verdict client.Verdict
	notice  string
	advance approvalAdvance
	resume  phase
}

func (approvalResolvedIntent) isSurfaceIntent() {}

type approvalRetractedIntent struct {
	notice  string
	advance approvalAdvance
	resume  phase
}

func (approvalRetractedIntent) isSurfaceIntent() {}

type setExpandToolsIntent struct{ expand bool }

func (setExpandToolsIntent) isSurfaceIntent() {}

func (s *approvalSurface) takeSurfaceIntent() surfaceIntent {
	intent := s.intent
	s.intent = nil
	return intent
}

// ---------------------------------------------------------------------------
// Surface-local state transitions and semantic intent construction
// ---------------------------------------------------------------------------

// isChildAsk reports whether askID identifies a surfaced SUBAGENT (child)
// permission ask rather than one from the main session. The askID namespace
// contract (engine/agent/dispatch.go newAskID; CLAUDE.md: "the child session id
// IS the namespace") is "<sessionID>:<n>:<callID>:<discriminator>" — only the
// LEADING "<sessionID>:" prefix is consumed here (the trailing discriminator is the
// server's per-run uniqueness suffix — a host-supplied value or "r<runSerial>",
// ADR-0044 — and is opaque to the client). A MAIN-agent
// ask is prefixed with the live session id, a child ask is prefixed with the
// CHILD session id. So an askID that contains a colon but is NOT prefixed by
// "<sessionID>:" is a child ask. Fail-safe both directions: a colon-free fixture
// id classifies as the main agent (offers always-allow), and if sessionID were
// empty everything would classify as a child (the always button is merely
// withheld — never a wrong allow).
func isChildAsk(askID, sessionID string) bool {
	return strings.Contains(askID, ":") && !strings.HasPrefix(askID, sessionID+":")
}

// applyPermissionAsk dedupes a known askID, enqueues a second ask behind an
// already-open modal, or opens the modal head. The surface records the phase the
// first ask interrupted; Model applies phase chrome at the reducer boundary.
//
// opening reports whether this ask became the visible head (false = deduped or
// enqueued).
func (s *approvalSurface) applyPermissionAsk(msg client.PermissionAskMsg, open bool, interrupted phase) (opening bool) {
	if s.known(msg.AskID) {
		return false
	}
	next := pendingAsk{
		AskID:          msg.AskID,
		Tool:           msg.Tool,
		Args:           msg.Args,
		Reason:         msg.Reason,
		focusedVerdict: client.VerdictAllowOnce,
		offerAlways:    !isChildAsk(msg.AskID, s.sessionID),
	}
	if open {
		s.enqueue(next)
		return false
	}
	s.ask = next
	s.resumePhase = interrupted
	return true
}

// applyPermissionRetract is the pure surface half of the PermissionRetractMsg
// arm: the harness WITHDREW a surfaced ask (its owning subagent was cancelled
// while parked). Three cases against the FIFO ask queue (s.ask is the head): a
// match on the VISIBLE ask dismisses the modal and advances the queue; a match
// on a QUEUED ask removes it in place; an unknown/stale id is idempotently
// dropped. The visible-ask advance mirrors the resolve path's teardown (per-flavour
// clears, then the state advance); the Model shim owns the phase assign +
// spinner re-arm decision the returned advance implies.
func (s *approvalSurface) applyPermissionRetract(msg client.PermissionRetractMsg, open bool) surfaceIntent {
	if open && s.ask.AskID == msg.AskID {
		s.markAskResolved(msg.AskID)
		s.clearPlanReview()
		s.clearAskArgsView()
		return approvalRetractedIntent{
			notice:  "permission request withdrawn (subagent cancelled)",
			advance: s.advance(),
			resume:  s.restoredPhase(),
		}
	}
	if s.removeQueued(msg.AskID) {
		s.markAskResolved(msg.AskID)
		return approvalRetractedIntent{notice: "queued permission request withdrawn (subagent cancelled)", advance: approvalAdvance{outcome: approvalQueueUnchanged}}
	}
	return nil
}

// markAskResolved records an answered/retracted askID into the resolvedAsks
// dedupe set, lazily initialising it (a reference type mutable through the
// value-receiver Model, same pattern as recordFileChange/filesSeen).
func (s *approvalSurface) markAskResolved(id string) {
	if s.resolvedAsks == nil {
		s.resolvedAsks = make(map[string]struct{})
	}
	s.resolvedAsks[id] = struct{}{}
}

// resolveAsk records the answer, advances the FIFO, and emits one semantic
// intent. Model consumes that intent synchronously, preserving the existing
// notice-before-send order without exposing transport or transcript callbacks to
// the surface. It never re-arms the stream reader: the ask event already armed
// the run's single reader, which delivers the resumed events after the one send.
func (s *approvalSurface) resolveAsk(v client.Verdict) approvalResolvedIntent {
	askID := s.ask.AskID
	s.markAskResolved(askID)
	s.clearPlanReview()
	s.clearAskArgsView()

	notice := "permission allowed"
	switch v {
	case client.VerdictAllowAlways:
		notice = "permission allowed (always, this session)"
	case client.VerdictDeny:
		notice = "permission denied"
	}
	return approvalResolvedIntent{
		askID: askID, verdict: v, notice: notice,
		advance: s.advance(), resume: s.restoredPhase(),
	}
}

// onApprovalKey is the pure surface half of the approval key handler. It
// routes scroll keys to the open args/plan viewport, closes/toggles the
// ask-args view, moves the button focus, and resolves verdicts — mutating ONLY
// the approval state and returning (actions, advance) for the verdict paths.
// The Model's shim executes them (and owns the phase assign + spinner re-arm).
// The verdicts fall through even while a scrollable view owns the scroll keys,
// so the operator can act after reading.
func (s *approvalSurface) onApprovalKey(msg tea.KeyPressMsg) (cmd tea.Cmd, intent surfaceIntent) {
	if s.argsViewOpen {
		if cmd, handled := s.argsScroll(msg, s.deps.keys); handled {
			return cmd, nil
		}
		if key.Matches(msg, s.deps.keys.Cancel) {
			s.clearAskArgsView()
			return nil, nil
		}
		if key.Matches(msg, s.deps.keys.RawArgs) {
			s.argsViewRaw = !s.argsViewRaw
			return nil, nil
		}
	}
	if isPlanAsk(s.ask.Tool) {
		if cmd, handled := s.planScroll(msg, s.deps.keys); handled {
			return cmd, nil
		}
	}
	if !isPlanAsk(s.ask.Tool) && !isDiffCapableAskTool(s.ask.Tool) && s.miniScrollKey(msg) {
		return nil, nil
	}
	verdicts := visibleApprovalVerdicts(s.ask)
	idx := slices.Index(verdicts, s.ask.focusedVerdict)
	if idx < 0 {
		idx = 0
		s.ask.focusedVerdict = verdicts[idx]
	}
	switch msg.String() {
	case "right", "tab":
		s.ask.focusedVerdict = verdicts[(idx+1)%len(verdicts)]
		return nil, nil
	case "left":
		s.ask.focusedVerdict = verdicts[(idx-1+len(verdicts))%len(verdicts)]
		return nil, nil
	case "enter":
		return nil, s.resolveAsk(s.ask.focusedVerdict)
	}
	if key.Matches(msg, s.deps.keys.AllowAlways) {
		if s.ask.offerAlways {
			return nil, s.resolveAsk(client.VerdictAllowAlways)
		}
		return nil, nil
	}
	if key.Matches(msg, s.deps.keys.Allow) {
		return nil, s.resolveAsk(client.VerdictAllowOnce)
	}
	if key.Matches(msg, s.deps.keys.Deny) {
		return nil, s.resolveAsk(client.VerdictDeny)
	}
	return nil, nil
}

// visibleApprovalVerdicts is the one ordered button set for an ask. Render,
// hit geometry, and keyboard focus traversal all consume it, so child asks can
// withhold Allow Always without positional coupling.
func visibleApprovalVerdicts(ask pendingAsk) []client.Verdict {
	verdicts := []client.Verdict{client.VerdictAllowOnce, client.VerdictDeny}
	if ask.offerAlways {
		return []client.Verdict{client.VerdictAllowOnce, client.VerdictAllowAlways, client.VerdictDeny}
	}
	return verdicts
}

// miniScrollKey moves the in-card args mini-viewport (askVPOffset) on a scroll
// key while the args full-screen view is NOT open. Returns handled=true only
// when the key was a scroll key AND there are hidden rows to scroll to;
// otherwise the key falls through to the action handlers. It is the pure
// surface half of the old Model.onAskArgsMiniScrollKey — the scroll range comes
// from deps so the surface never touches the Model's geometry.
func (s *approvalSurface) miniScrollKey(msg tea.KeyPressMsg) bool {
	maxOff := s.miniScrollRange()
	if maxOff <= 0 {
		return false
	}
	step := 0
	switch {
	case key.Matches(msg, s.deps.keys.ScrollU):
		step = -3
	case key.Matches(msg, s.deps.keys.ScrollD):
		step = 3
	case msg.String() == keyMenuUp:
		step = -1
	case msg.String() == keyMenuDown:
		step = 1
	default:
		return false
	}
	s.miniScroll(step, maxOff)
	return true
}

// miniScrollRange computes the maximum clamped YOffset of the modal's in-card
// args mini-viewport for the current ask/geometry: the wrapped args line count
// minus the view rows the modal declares (the SAME arithmetic
// permissionModalBodyParts lays out, so the scroll bound matches the render).
// Returns 0 when nothing is hidden (the scroll keys then no-op). The layout
// inputs (theme, region width/height) ride the deps so the surface never
// touches the Model.
func (s approvalSurface) miniScrollRange() (maxOff int) {
	pretty, _, ok := askArgsContent(s.deps.theme, s.ask)
	if !ok || pretty == "" {
		return 0
	}
	// The SAME layout the render path lays out (askArgsMiniViewport) — never a
	// re-derived wrap/cap, so the scroll bound matches the frame by construction.
	return askArgsMiniViewport(s.deps.theme, pretty, s.regionW, s.regionH).maxOffset
}

// approvalExpandToggle is the approval arm of the ctrl+t handler (issue #488),
// the pure surface half of the old onExpandToolsKey approval branch: inside the
// permission modal ctrl+t ROUTES by ask type — a non-diff, non-plan ask
// opens/closes the full-screen ask-args view INSTEAD of toggling expandTools;
// a plan ask or an Edit/Write (diff-capable) ask reports approval=false so the
// Model keeps the in-modal expand behaviour byte-for-byte. The returned
// actions carry the view re-population (the Model executes it against its
// renderer).
func (s *approvalSurface) approvalExpandToggle() (approval bool) {
	if isPlanAsk(s.ask.Tool) || isDiffCapableAskTool(s.ask.Tool) {
		return false
	}
	if s.argsViewOpen {
		s.clearAskArgsView()
		return true
	}
	s.argsViewOpen = true
	return true
}

// ---------------------------------------------------------------------------
// Approval-local state, rendering, layout, and hit behavior
// ---------------------------------------------------------------------------

// pendingAsk holds the state of an open permission modal. AskID is the exact
// correlation key sent back in ResumeApproval — it is never derived from the
// tool name. focusedVerdict is keyboard-only view state. offerAlways gates the
// Allow Always verdict: it is offered only for the MAIN agent's asks, never for
// a surfaced subagent ask (a child engine's permission policy has a nil learn
// store, so always-allow would be a silent no-op there).
type pendingAsk struct {
	AskID          string
	Tool           string
	Args           string
	Reason         string
	focusedVerdict client.Verdict
	offerAlways    bool
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

// known reports whether askID is already visible (the modal head), queued, or
// answered/retracted this run — the duplicate-ask drop predicate.
func (s approvalSurface) known(id string) bool {
	if _, ok := s.resolvedAsks[id]; ok {
		return true
	}
	if s.ask.AskID == id {
		return true
	}
	return askQueueIndex(s.queue, id) >= 0
}

// enqueue appends a surfaced ask FIFO behind the visible head (concurrent
// subagents surface asks while one is already open; each parks its child
// server-side until answered, so a second ask must queue, never clobber the
// first).
func (s *approvalSurface) enqueue(ask pendingAsk) {
	s.queue = append(s.queue, ask)
}

// askQueueIndex returns the index of askID in the queue, or -1.
func askQueueIndex(q []pendingAsk, id string) int {
	return slices.IndexFunc(q, func(x pendingAsk) bool { return x.AskID == id })
}

// removeQueued removes the askID'd ask from the queue in place, reporting
// whether it was there (the queued-retract path). The visible head is
// untouched.
func (s *approvalSurface) removeQueued(id string) bool {
	i := askQueueIndex(s.queue, id)
	if i < 0 {
		return false
	}
	// Capacity-clamped append form: it appends into the very slice it splits, so
	// a stale alias can never observe a clobbered tail element.
	s.queue = append(s.queue[:i:i], s.queue[i+1:]...)
	return true
}

// approvalQueueOutcome states exactly what queue advancement did. Model uses it
// to decide whether to retain awaiting chrome or restore the interrupted phase.
type approvalQueueOutcome uint8

const (
	approvalQueueUnchanged approvalQueueOutcome = iota
	approvalQueueSuccessor
	approvalQueueDrained
)

// approvalAdvance reports the queue outcome after resolving or retracting an
// ask. Restoring the interrupted phase remains a Model-owned effect carried by
// the semantic intent.
type approvalAdvance struct {
	outcome approvalQueueOutcome
}

// advance pops the next queued ask into the visible slot, or drains the queue.
// It is the queue-manipulation half of the verdict/retract paths; Model owns the
// phase transition, spinner re-arm decision, and modal teardown.
func (s *approvalSurface) advance() approvalAdvance {
	if len(s.queue) > 0 {
		next := s.queue[0]
		s.queue = s.queue[1:]
		s.ask = next
		return approvalAdvance{outcome: approvalQueueSuccessor}
	}
	s.ask = pendingAsk{}
	return approvalAdvance{outcome: approvalQueueDrained}
}

func (s *approvalSurface) restoredPhase() phase {
	if s.resumePhase == phaseIdle {
		return phaseIdle
	}
	return phaseRunning
}

// planScroll routes a scroll key to the plan-review viewport while a plan ask
// is the front ask. It mirrors the Model's onScrollKey m.vp routing — pgup/
// pgdn delegate to the viewport, home/end jump to top/bottom, and the arrow
// keys (up/down) scroll a line at a time — EXCEPT up/down are NOT EditBack here
// (the plan-review view has no textarea queue to pull back). Returns
// handled=true when the key was a scroll key it consumed; false otherwise so
// the approval action-key fall-through (A/W/D/enter/left/right/tab) still
// resolves the ask. The plan viewport's scroll offset is the operator's reading
// position; clearing planVP on resolve preserves nothing (a new plan ask opens
// at the top).
func (s *approvalSurface) planScroll(msg tea.KeyPressMsg, keys keyMap) (cmd tea.Cmd, handled bool) {
	if !s.planVPReady {
		return nil, false
	}
	switch {
	case key.Matches(msg, keys.ScrollTop):
		s.planVP.GotoTop()
		return nil, true
	case key.Matches(msg, keys.ScrollBottom):
		s.planVP.GotoBottom()
		return nil, true
	case key.Matches(msg, keys.ScrollU), key.Matches(msg, keys.ScrollD):
		s.planVP, cmd = s.planVP.Update(msg)
		return cmd, true
	case msg.String() == keyMenuUp, msg.String() == keyMenuDown:
		// Arrow keys scroll the plan a line at a time (the plan-review view's
		// primary nav). They are NOT button-focus keys here (left/right/tab move
		// the button focus instead — documented in the plan-review footer hint),
		// so a long plan is navigable by the most natural keys without losing the
		// reading position.
		s.planVP, cmd = s.planVP.Update(msg)
		return cmd, true
	}
	return nil, false
}

// argsScroll routes a scroll key to the full-screen ask-args viewport (argsVP)
// while the ask-args view is open — the args-view analogue of planScroll:
// pgup/pgdn delegate to the viewport, home/end jump to top/bottom, and the
// arrow keys (up/down) scroll a line at a time. Returns handled=true when the
// key was a scroll key it consumed; false otherwise so the approval
// close/toggle/action fall-through still runs.
func (s *approvalSurface) argsScroll(msg tea.KeyPressMsg, keys keyMap) (cmd tea.Cmd, handled bool) {
	if !s.argsVPReady {
		return nil, false
	}
	switch {
	case key.Matches(msg, keys.ScrollTop):
		s.argsVP.GotoTop()
		return nil, true
	case key.Matches(msg, keys.ScrollBottom):
		s.argsVP.GotoBottom()
		return nil, true
	case key.Matches(msg, keys.ScrollU), key.Matches(msg, keys.ScrollD):
		s.argsVP, cmd = s.argsVP.Update(msg)
		return cmd, true
	case msg.String() == keyMenuUp, msg.String() == keyMenuDown:
		s.argsVP, cmd = s.argsVP.Update(msg)
		return cmd, true
	}
	return nil, false
}

// miniScroll moves the in-card args mini-viewport (askVPOffset) by step,
// clamped to [0, maxOff]. Shared by the scroll-key handler and the
// wheel-over-card handler so both clamp identically.
func (s *approvalSurface) miniScroll(step, maxOff int) {
	s.askVPOffset = max(0, min(maxOff, s.askVPOffset+step))
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

// wrapApprovalReason sanitizes untrusted reason text before wrapping it to the
// current rendered content width. Reasons are independent metadata: never parse
// or reconstruct the child ask's Args to display them.
func wrapApprovalReason(reason string, width int) string {
	return wrapAskArgs(sanitizeTerminal(reason), width)
}

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

// renderPermissionModal renders the centred generic approval card for fixtures.
// Live rendering receives card-content geometry directly through Render.
func (s *approvalSurface) renderPermissionModal(width, height int) string {
	style := s.deps.theme.Style("askCard")
	contentW := max(0, width-style.GetHorizontalFrameSize())
	contentH := max(0, height-style.GetVerticalFrameSize())
	return centerCard(s.deps.theme, s.renderPermissionModalBody(contentW, contentH), width, height)
}

func (s *approvalSurface) renderPermissionModalBody(width, height int) string {
	body, _ := s.permissionModalBodyParts(width, height)
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
// render path (renderPermissionModal) and the approval surface's mouse hit-test:
// the hit-test must not re-derive the buttons row from the trailing write order (a new trailing line would silently
// desync it), so the builder that LAYS OUT the body also owns where the buttons
// landed.
//
// width/height are the card content region's dimensions (0 = unknown): the args
// region of a non-diff ask wraps DETERMINISTICALLY at the supplied content width,
// capped at permissionModalMaxWidth converted once to content width. The render
// and the hit-test therefore measure the same wrap, and height bounds the region's
// row budget so centerCard never receives an over-region body. argsOffset is the
// args mini-viewport's YOffset.
func (s *approvalSurface) permissionModalBodyParts(width, height int) (body string, buttonsRow int) {
	th := s.deps.theme
	ask := s.ask
	expand := s.expandTools
	queued := len(s.queue)
	argsOffset := s.askVPOffset
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
	if diff, ok := s.render.diff(ask.Tool, ask.Args, expand); ok {
		if diff != "" {
			b.WriteString(th.Style("muted").Render("changes:") + "\n")
			b.WriteString(diff + "\n")
		}
		if ask.Reason != "" {
			b.WriteString("\n" + th.Style("muted").Render(wrapApprovalReason(ask.Reason, askArgsCardContentWidth(th, width))) + "\n")
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
		argsOffset = max(0, min(argsRegion.maxOffset, argsOffset))
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
		hk := s.deps.marks
		hint := hk.expandTools + " full args"
		if argsRegion.maxOffset > 0 {
			hint = "… " + hk.scroll + " scroll · " + hint
		}
		b.WriteString(th.Style("muted").Render(hint) + "\n")
		if ask.Reason != "" {
			b.WriteString(th.Style("muted").Render(wrapApprovalReason(ask.Reason, askArgsCardContentWidth(th, width))) + "\n")
		}
	} else if args := prettyJSON(ask.Args); args != "" {
		b.WriteString(th.Style("toolArgs").Render(args) + "\n")
		if ask.Reason != "" {
			b.WriteString("\n" + th.Style("muted").Render(wrapApprovalReason(ask.Reason, askArgsCardContentWidth(th, width))) + "\n")
		}
	} else if ask.Reason != "" {
		b.WriteString("\n" + th.Style("muted").Render(wrapApprovalReason(ask.Reason, askArgsCardContentWidth(th, width))) + "\n")
	}

	// Three visible verdicts when always-allow is offered (main-agent asks), two
	// otherwise (surfaced subagent asks). The shared visible verdict set drives
	// rendering, hit geometry, and keyboard traversal.
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
	hk := s.deps.marks
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
// non-diff ask's pretty tier at the given card-content geometry. The wrap budget
// is the offered card content width minus the askArgs accent-bar frame, so every
// styled line stays inside the card. blockWidth is the widest wrapped line's width
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
// region. Its width argument is ALREADY the askCard content width supplied by
// the parent, so this function must never subtract the askCard frame again.
// permissionModalMaxWidth remains an OUTER-card cap: convert it once to its
// content-width equivalent, then limit the supplied content width to that value.
// Both the render and hit-test derive the budget from the same inputs, so they
// cannot disagree about wrapping. width <= 1 means "unknown/tiny: no wrap" (0).
func askArgsCardContentWidth(th theme.Theme, width int) int {
	maxContentWidth := permissionModalMaxWidth - th.Style("askCard").GetHorizontalFrameSize()
	if width > maxContentWidth {
		width = maxContentWidth
	}
	if width <= 1 {
		return 0
	}
	return width
}

// approvalButtonSep is the exact separator lipgloss.JoinHorizontal places between
// the styled approval buttons in permissionButtonsLine / planButtonsLine. It is the
// SINGLE source for BOTH the render (the JoinHorizontal calls below) and the mouse
// hit-test column arithmetic (askButtonRects below), so changing the separator
// separator keeps the render and the hit-test in lockstep — a bare "  " literal on
// each side would silently desync them.
const approvalButtonSep = "  "

// approvalButton is a rendered approval verdict and label. Both the render and
// hit-test paths consume this descriptor so their labels, styles, and verdicts
// cannot drift.
type approvalButton struct {
	verdict client.Verdict
	label   string
}

func approvalButtons(th theme.Theme, hk helpKeys, ask pendingAsk, plan bool) []approvalButton {
	label := func(verdict client.Verdict) string {
		if plan {
			switch verdict {
			case client.VerdictAllowOnce:
				return planApprovalButtonLabel(hk.allow, "Allow", "approve & run")
			case client.VerdictAllowAlways:
				return planApprovalButtonLabel(hk.allowAlways, "Always", "auto-accept edits")
			default:
				return planApprovalButtonLabel(hk.deny, "Deny", "iterate")
			}
		}
		switch verdict {
		case client.VerdictAllowOnce:
			return approvalButtonLabel(hk.allow, "Allow", "allow")
		case client.VerdictAllowAlways:
			return approvalButtonLabel(hk.allowAlways, "Always", "always allow")
		default:
			return approvalButtonLabel(hk.deny, "Deny", "deny")
		}
	}
	verdicts := visibleApprovalVerdicts(ask)
	buttons := make([]approvalButton, 0, len(verdicts))
	for _, verdict := range verdicts {
		style := th.Style("askButton")
		if ask.focusedVerdict == verdict {
			style = th.Style("askButtonActive")
		}
		buttons = append(buttons, approvalButton{verdict: verdict, label: style.Render(label(verdict))})
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
// or the run/session ends — mirroring how the ask/queue are cleared — so a
// stale planVP never leaks across asks or sessions.
func (s *approvalSurface) clearPlanReview() {
	s.planVP.SetContent("")
	s.planVP.SetYOffset(0)
	s.planVPReady = false
	s.planVPWidth = 0
	s.planVPHeight = 0
	s.planVPFingerprint = ""
}

// planAskFingerprint is the identity of a plan ask the planVP content depends
// on (the tool name, the raw args JSON carrying the plan/note, the queued count
// for the title badge, and the effective model line). It lets openPlan
// short-circuit a no-op re-population when NOTHING about the rendered plan
// changed (only a geometry change or a new ask re-populates).
func planAskFingerprint(ask pendingAsk, queued int, effectiveModel string) string {
	return ask.Tool + "\x00" + ask.Args + "\x00" + ask.Reason + "\x00" +
		fmt.Sprintf("%d", queued) + "\x00" + effectiveModel
}

// planReviewLayout is the rendered plan-review body and the action button box's
// exact row span within it. Render and mouse hit-testing share it so viewport
// height, the joining newline, and Lipgloss's three-row button box stay in sync.
type planReviewLayout struct {
	content                   string
	buttonsRow, buttonsHeight int
}

// openPlan materializes the full shared-Markdown plan view. It preserves the
// reader's offset across ask, queue, model, or geometry cache invalidations.
func (s *approvalSurface) openPlan(width, height int) {
	vpHeight := max(1, height-planReviewFooterHeight)
	fingerprint := planAskFingerprint(s.ask, len(s.queue), s.modelID)
	if s.planVPReady && s.planVPWidth == width && s.planVPHeight == vpHeight && s.planVPFingerprint == fingerprint {
		return
	}
	previous, fresh := s.planVP.YOffset(), !s.planVPReady
	title := "Plan ready for review"
	if len(s.queue) > 0 {
		title = fmt.Sprintf("Plan ready for review (1 of %d)", len(s.queue)+1)
	}
	var head strings.Builder
	head.WriteString(s.deps.theme.Style("askTitle").Render(title) + "\n")
	head.WriteString(s.deps.theme.Style("toolName").Render(sanitizeTerminal(s.ask.Tool)) + "\n")
	modelLine := "execute model: session default model"
	if s.modelID != "" {
		modelLine = fmt.Sprintf("plan model: %s · execute model: session default model", sanitizeTerminal(s.modelID))
	}
	head.WriteString(s.deps.theme.Style("muted").Render(modelLine) + "\n")
	var body string
	if planText := planBodyFromArgs(s.ask.Args); planText != "" {
		body = s.deps.theme.Style("muted").Render("plan:") + "\n" + s.render.markdown(planText, planReviewContentWidth(width)) + "\n"
	} else if s.ask.Reason != "" {
		body = s.deps.theme.Style("muted").Render(wrapApprovalReason(s.ask.Reason, planReviewContentWidth(width))) + "\n"
	}
	s.planVP.SetWidth(width)
	s.planVP.SetHeight(vpHeight)
	s.planVP.SetContent(head.String() + "\n" + body)
	if fresh {
		s.planVP.SetYOffset(0)
	} else {
		s.planVP.SetYOffset(previous)
	}
	s.planVPReady, s.planVPWidth, s.planVPHeight, s.planVPFingerprint = true, width, vpHeight, fingerprint
}

func (s *approvalSurface) planLayout() planReviewLayout {
	var view string
	if s.planVPReady {
		view = s.planVP.View()
	}
	buttons := planButtonsLine(s.deps.theme, s.deps.marks, s.ask)
	bar := buttons
	if s.ask.offerAlways {
		bar += "\n" + s.deps.theme.Style("muted").Render("auto-accept allows every edit in the execution phase for the rest of this session")
	}
	bar += "\n" + s.deps.theme.Style("muted").Render(planScrollHint(s.deps.marks))
	layout := planReviewLayout{content: view, buttonsHeight: lipgloss.Height(buttons)}
	if view != "" {
		layout.buttonsRow = strings.Count(view, "\n") + 1
		layout.content += "\n"
	}
	layout.content += bar
	return layout
}

// ---------------------------------------------------------------------------
// Full-screen ask-args view (issue #488)
// ---------------------------------------------------------------------------

// argsReviewFooterHeight is the rows reserved at the bottom of the full-screen
// ask-args view for the pinned action bar (buttons + the always footnote + the
// scroll/raw hint). Same shape as planReviewFooterHeight.
const argsReviewFooterHeight = 3

// clearAskArgsView tears down the full-screen ask-args view AND resets the
// modal's args mini-viewport offset: it drops the viewport content, marks it
// not-ready/closed, and resets the raw toggle so the next ask opens pretty.
// Called whenever the ask resolves (any verdict), is retracted, advances to a
// queued successor, or the run/session ends — mirroring clearPlanReview — so a
// stale argsVP never leaks across asks or sessions.
func (s *approvalSurface) clearAskArgsView() {
	s.argsVP.SetContent("")
	s.argsVP.SetYOffset(0)
	s.argsVPReady = false
	s.argsVPWidth = 0
	s.argsVPHeight = 0
	s.argsVPFingerprint = ""
	s.argsViewRaw = false
	s.argsViewOpen = false
	s.askVPOffset = 0
}

// argsAskFingerprint is the identity of an ask the argsVP content depends on
// (tool, raw args JSON, reason, the queued count for the title badge, and the
// raw/pretty tier flag — a toggle re-populates). It lets openArgs
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
// shared by Render and the mouse hit-test so viewport height, the joining
// newline, and Lipgloss's three-row button box stay in sync.
type argsReviewLayout struct {
	content                   string
	buttonsRow, buttonsHeight int
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

// openArgs materializes the full pretty/raw args view and preserves the reader's
// offset across ask, queue, tier, or geometry cache invalidations.
func (s *approvalSurface) openArgs(width, height int) {
	vpHeight := max(1, height-argsReviewFooterHeight)
	fingerprint := argsAskFingerprint(s.ask, len(s.queue), s.argsViewRaw)
	if s.argsVPReady && s.argsVPWidth == width && s.argsVPHeight == vpHeight && s.argsVPFingerprint == fingerprint {
		return
	}
	previous, fresh := s.argsVP.YOffset(), !s.argsVPReady
	title := "Ask args: " + sanitizeTerminal(s.ask.Tool)
	if len(s.queue) > 0 {
		title = fmt.Sprintf("Ask args: %s (1 of %d)", sanitizeTerminal(s.ask.Tool), len(s.queue)+1)
	}
	pretty, raw, ok := askArgsContent(s.deps.theme, s.ask)
	tier := pretty
	if s.argsViewRaw {
		tier = raw
	}
	var body string
	if ok && tier != "" {
		body = s.deps.theme.Style("toolArgs").Render(wrapAskArgsContinuations(s.deps.theme, tier, planReviewContentWidth(width))) + "\n"
	}
	if s.ask.Reason != "" {
		body += "\n" + s.deps.theme.Style("muted").Render(wrapApprovalReason(s.ask.Reason, planReviewContentWidth(width))) + "\n"
	}
	s.argsVP.SetWidth(width)
	s.argsVP.SetHeight(vpHeight)
	s.argsVP.SetContent(s.deps.theme.Style("askTitle").Render(title) + "\n\n" + body)
	if fresh {
		s.argsVP.SetYOffset(0)
	} else {
		s.argsVP.SetYOffset(previous)
	}
	s.argsVPReady, s.argsVPWidth, s.argsVPHeight, s.argsVPFingerprint = true, width, vpHeight, fingerprint
}

func (s *approvalSurface) argsLayout() argsReviewLayout {
	var view string
	if s.argsVPReady {
		view = s.argsVP.View()
	}
	buttons := permissionButtonsLine(s.deps.theme, s.deps.marks, s.ask)
	bar := buttons
	if s.ask.offerAlways {
		bar += "\n" + s.deps.theme.Style("muted").Render(approvalAlwaysFootnote(s.deps.marks.allowAlways))
	}
	bar += "\n" + s.deps.theme.Style("muted").Render(argsScrollHint(s.deps.marks, askArgsTiersDiffer(s.deps.theme, s.ask)))
	layout := argsReviewLayout{content: view, buttonsHeight: lipgloss.Height(buttons)}
	if view != "" {
		layout.buttonsRow = strings.Count(view, "\n") + 1
		layout.content += "\n"
	}
	layout.content += bar
	return layout
}

// planBodyFromArgs extracts the PresentPlan `plan` (falling back to `note`) from
// the raw JSON args string and returns the raw, terminal-sanitized plan text.
// The caller (openPlan) wraps it with the shared Markdown renderer at the view
// content width. Returns "" when neither plan nor
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

// approvalNotice renders the muted one-line verdict notice for a replayed
// EvApproval (ApprovalMsg). The replay has no live ask modal, so this is the
// transcript's audit record of the verdict. Metadata-only (gauntlet #7): tool
// NAME + verdict, never the raw args.
func approvalNotice(msg client.ApprovalMsg) string {
	tool := msg.Tool
	if tool == "" {
		tool = "tool"
	}
	switch msg.Verdict {
	case "allow_once":
		return "✓ allowed once: " + tool
	case "allow_always":
		return "✓ allowed always: " + tool
	case "deny":
		return "✗ denied: " + tool
	default:
		if msg.Verdict == "" {
			return "· permission: " + tool
		}
		return "· " + msg.Verdict + ": " + tool
	}
}

// approvalButtonLabel renders a generic permission-modal button label that is
// honest about the LIVE approval chord. With the DEFAULT word-embedded chord
// ("a"/"w"/"d") the bracketed letter sits inside the word at its natural
// position, so the case follows the word's spelling and the historical form
// ("[A]llow" / "Al[w]ays" / "[D]eny") renders byte-for-byte. When the chord is
// rebound AWAY from its default word letter the wordplay no longer holds, so
// the button degrades to an honest standalone form: the bracketed live chord
// (upper-cased via approvalMnemonic for a bare rune, verbatim for a modified
// chord) followed by the action's standalone word ("[Y] allow" / "[Q] always
// allow" / "[N] deny", or "[ctrl+y] allow" for a modified chord). word is the
// default word-embedded form's word ("Allow"/"Always"/"Deny"); standalone is
// the overridden form's action phrase ("allow"/"always allow"/"deny"). The
// default chord for each action is its word's mnemonic letter (a/w/d), so an
// override to a different letter (y/q/n) OR a modified chord trips the
// standalone branch. Issue #457.
func approvalButtonLabel(chord, word, standalone string) string {
	if isDefaultApprovalChord(chord, word) {
		switch word {
		case "Allow":
			return "[A]llow"
		case "Always":
			return "Al[w]ays"
		case "Deny":
			return "[D]eny"
		}
	}
	return "[" + approvalMnemonic(chord) + "] " + standalone
}

// planApprovalButtonLabel renders a PLAN-review action-bar button label that is
// honest about the LIVE approval chord. With the DEFAULT a/w/d chords the
// historical word-embedded plan form ("[A]pprove & run" / "[W] auto-accept
// edits" / "[D] iterate") renders byte-for-byte. Under an override the plan
// wordplay ("[Y]pprove & run") would read as a typo — and for a MODIFIED chord
// ("ctrl+y") the stem-glued form ("[ctrl+y]pprove & run") is outright broken —
// so the button degrades to an honest standalone form: the bracketed live chord
// followed by the plan action phrase ("[Y] approve & run" / "[ctrl+y] approve &
// run"). This mirrors approvalButtonLabel's default-vs-override split for the
// generic modal; the plan path needs its own helper because its default labels
// differ from the generic modal's. Issue #457.
func planApprovalButtonLabel(chord, word, standalone string) string {
	if isDefaultApprovalChord(chord, word) {
		switch word {
		case "Allow":
			return "[A]pprove & run"
		case "Always":
			return "[W] auto-accept edits"
		case "Deny":
			return "[D] iterate"
		}
	}
	return "[" + approvalMnemonic(chord) + "] " + standalone
}

// approvalAlwaysFootnote renders the "always allows this exact command …"
// footnote under the always-allow button. With the default chord ("w") it is
// the historical byte-for-byte "al[w]ays allows …" word-embedded form; under
// an override it states the live chord honestly ("always (q) allows …"). Issue #457.
func approvalAlwaysFootnote(chord string) string {
	if isDefaultApprovalChord(chord, "Always") {
		return "al[w]ays allows this exact command for the rest of this session"
	}
	return "always (" + chord + ") allows this exact command for the rest of this session"
}

// isDefaultApprovalChord reports whether chord is the default first chord for
// the given approval word — "a"/"w"/"d" for Allow/Always/Deny — so the
// word-embedded form's wordplay holds. Any other chord (a different bare rune
// like "y", or a modified chord like "ctrl+y") trips the honest standalone
// form.
func isDefaultApprovalChord(chord, word string) bool {
	switch word {
	case "Allow":
		return chord == "a"
	case "Always":
		return chord == "w"
	case "Deny":
		return chord == "d"
	}
	return false
}

// approvalMnemonic renders the footer/plan-review approval affordance mnemonic for a
// rebindable approval chord: the chord with its first rune upper-cased, so the
// default Allow/AllowAlways/Deny chords ("a"/"w"/"d") render as the historical "A"/
// "W"/"D" mnemonics byte-for-byte, while a remapped bare rune ("y") renders as its
// upper-case ("Y"). A modified chord ("ctrl+y") is returned unchanged — upper-casing
// only the first LETTER of a modified chord would mangle it, and a modified approval
// chord is rare (the validator allows bare runes for approval keys), so the fallback
// keeps the chord legible. Issue #457.
func approvalMnemonic(chord string) string {
	if chord == "" {
		return chord
	}
	r := rune(chord[0])
	// Only upper-case a leading ASCII lowercase letter (a bare-rune approval key).
	// A modified chord ("ctrl+y", "f5") or a multi-rune chord stays verbatim.
	if r >= 'a' && r <= 'z' && len(chord) == 1 {
		return string(r - 'a' + 'A')
	}
	return chord
}

// askButtonRects maps the action bar rendered by approvalSurface.Render to
// body-relative button rectangles for the current frame.
func askButtonRects(th theme.Theme, hk helpKeys, ask pendingAsk, plan bool) []buttonRect {
	buttons := approvalButtons(th, hk, ask, plan)
	rects := make([]buttonRect, 0, len(buttons))
	x := 0
	for _, button := range buttons {
		w := lipgloss.Width(button.label)
		rects = append(rects, buttonRect{x0: x, x1: x + w, verdict: button.verdict})
		x += w + buttonGap
	}
	return rects
}

// buttonRect is one button's half-open horizontal cell span and verdict within the action bar.
type buttonRect struct {
	x0, x1  int
	verdict client.Verdict
}

var buttonGap = lipgloss.Width(approvalButtonSep)
