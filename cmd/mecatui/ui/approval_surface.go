package ui

import (
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

// approval_surface.go owns the approval modal's BEHAVIOUR (issue #555 Phase
// 1): the reducers/handlers that mutate the approvalState (approval_state.go)
// and PROPOSE actions for the Model to execute. Decision 4 ("the surface
// proposes; the Model disposes"): the pure surface half never touches the wire
// or the conversation — resolveAsk/applyPermissionAsk return approvalActions
// the Model's shims (dispatchApprovalActions) execute against m.stream /
// m.conv / m.rend, byte-for-byte the same effects in the same order as the old
// direct calls. The phase itself stays Model-owned: the surface reads it and the
// Model's resolveAsk/retract shims assign it (advancing the queue via
// approvalState.advance).

// approvalDeps is the explicit list of what the approval surface may touch
// (issue #555 decision D3), built fresh per call by Model.approvalDeps and
// NEVER stored: the surface takes it as a parameter, so there is no *Model
// back-reference anywhere in the approval files. The value fields are pure
// inputs (theme, key bindings, the session/model ids, the body-region
// geometry); the func fields are the Model's collaborators (the wire send, the
// transcript notice, the card rect the wheel gate shares with the click
// hit-test).
type approvalDeps struct {
	theme theme.Theme
	// keys is the flat keyMap (decision D6 — bindings stay Model-global): the
	// surface MATCHES against it (Allow/AllowAlways/Deny/RawArgs/Cancel +
	// ScrollU/ScrollD/ScrollTop/ScrollBottom).
	keys keyMap
	// marks is the help-key markings the RENDER hints consume (planScrollHint /
	// argsScrollHint / buttons). Distinct from keys: one matches, one renders.
	marks        helpKeys
	sendApproval func(askID string, v client.Verdict) error
	notice       func(text string)
	sessionID    string
	// modelID is the effective model the plan-review header shows.
	modelID string
	// regionW/regionH are the body-region geometry the args mini-viewport's
	// scroll bound wraps at (m.width, m.vp.Height()).
	regionW, regionH int
	// cardRect is the generic modal card's screen rect — the SAME source the
	// click hit-test (approvalCardRect) consumes, so the wheel-over-card gate
	// and the click region can never drift. ok=false on a zero-size layout.
	cardRect func() (cellRect, bool)
}

// approvalDeps builds the surface's collaborators from the live Model. The
// sendApproval closure captures the CURRENT run stream (nil-safe, mirroring
// resolveAsk's old inline guard) so a verdict send is a plain error return —
// dispatchApprovalActions wraps it in the SAME tea.Cmd shape the old inline
// `send` closure produced, so a send error surfaces as a StreamErrMsg through
// the ordinary stream-error path.
func (m *Model) approvalDeps() approvalDeps {
	stream := m.stream
	return approvalDeps{
		theme: m.deps.Theme,
		keys:  m.keys,
		marks: m.helpKeyMarkings(),
		sendApproval: func(askID string, v client.Verdict) error {
			if stream == nil {
				return nil
			}
			return stream.SendApproval(askID, v)
		},
		notice:    m.conv.addNotice,
		sessionID: m.sessionID,
		modelID:   m.effectiveModel.ModelID,
		regionW:   m.width,
		regionH:   m.vp.Height(),
		cardRect: func() (cellRect, bool) {
			rect, _, ok := m.approvalCardRect()
			return rect, ok
		},
	}
}

// approvalAction is the SEMANTIC side effect a pure surface half proposes —
// a sum type, never a bare func: funcs do not survive the Elm value-Model
// copies and cannot be asserted on in tests. One dispatcher
// (Model.dispatchApprovalActions) executes the batch against the Model's
// collaborators in order.
type approvalAction struct {
	kind approvalActionKind
	// sendApproval fields.
	askID   string
	verdict client.Verdict
	// addNotice field.
	text string
	// openPlanReview / openArgsView fields.
	ask    pendingAsk
	queued int
	model  string
}

type approvalActionKind int

const (
	// approvalSendApproval sends the verdict for askID on the run stream
	// (ask_id correlation) — the ResumeApproval frame.
	approvalSendApproval approvalActionKind = iota + 1
	// approvalAddNotice appends a transcript notice (the verdict/retract line).
	approvalAddNotice
	// approvalOpenPlanReview populates the plan-review viewport for a plan ask
	// (deferred to the Model because it needs the renderer's markdown wrapper).
	approvalOpenPlanReview
	// approvalOpenArgsView populates the full-screen ask-args viewport.
	approvalOpenArgsView
)

// dispatchApprovalActions executes the actions a pure surface half returned,
// in order, against the Model's collaborators. It returns the tea.Cmd the
// caller batches into its result (the deferred stream send, mirroring the old
// inline `send` command: a send error surfaces as a StreamErrMsg). The
// open-* actions run through the SAME (m *Model) helpers the old code called
// inline — only the CALL ORDER moved (the surface decides WHAT opens, the
// Model still owns HOW).
func (m *Model) dispatchApprovalActions(deps approvalDeps, actions []approvalAction) tea.Cmd {
	var send *approvalAction
	for _, act := range actions {
		switch act.kind {
		case approvalSendApproval:
			a := act
			send = &a
		case approvalAddNotice:
			deps.notice(act.text)
		case approvalOpenPlanReview:
			m.openPlanReviewView(act.ask, act.queued, act.model)
		case approvalOpenArgsView:
			m.openAskArgsView(act.ask, act.queued)
		}
	}
	if send == nil {
		return nil
	}
	a := *send
	return func() tea.Msg {
		if err := deps.sendApproval(a.askID, a.verdict); err != nil {
			return client.StreamErrMsg{Err: err}
		}
		return nil
	}
}

// onApprovalKey is the Model-side shim the dispatchPhaseKey approval arm calls:
// it delegates to the pure approvalState.onApprovalKey, executes the returned
// actions via dispatchApprovalActions, and — when a verdict advanced the queue
// to empty — owns the phase transition (clear + resume). It batches the surface
// cmd (a viewport scroll) with the action-dispatch cmd (the deferred send).
func (m Model) onApprovalKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	deps := (&m).approvalDeps()
	cmd, actions, adv, resolved := (&m.approval).onApprovalKey(msg, deps)
	actCmd := (&m).dispatchApprovalActions(deps, actions)
	if !resolved {
		return m, tea.Batch(cmd, actCmd)
	}
	if adv.hasNext {
		// A queued successor took the head: the phase STAYS awaitingApproval, so
		// the spinner is off-screen — no m.sp.Tick (re-arm fires only when leaving
		// awaitingApproval INTO running; see resolveAsk's rearm contract).
		return m, tea.Batch(cmd, actCmd)
	}
	// Queue drained → resume the interrupted phase. Re-arm the spinner when the
	// resumed phase is running (the phase-gated TickMsg handler dropped the chain
	// when the modal opened). m.sp.Tick is NOT a stream reader — the
	// no-extra-reader invariant is untouched (kept in sync with
	// TestResolveLastAskRearmsSpinner / TestSpinnerVisibleMatchesFooterRender).
	// adv.resume is pre-clamped to idle|running by approvalState.advance() — the
	// single source of truth for the resume rule; do NOT re-clamp here.
	m.phase = adv.resume
	if adv.resume == phaseRunning {
		return m, tea.Batch(cmd, actCmd, m.sp.Tick)
	}
	return m, tea.Batch(cmd, actCmd)
}

// applyPermissionAsk reduces a PermissionAskMsg, extracted from updateStreamEvent
// to keep that dispatcher under the cyclomatic-complexity bound. The pure surface
// half (approvalState.applyPermissionAsk) mutates the approval state and returns
// the actions; the Model disposes — the phase transition, the viewport population
// (via dispatchApprovalActions), and the transcript effects.
func (m Model) applyPermissionAsk(msg client.PermissionAskMsg) (tea.Model, tea.Cmd) {
	open := m.phase == phaseAwaitingApproval
	deps := (&m).approvalDeps()
	interrupted := m.phase
	opening, resumePhase, actions := (&m.approval).applyPermissionAsk(msg, open, interrupted, deps)
	(&m).dispatchApprovalActions(deps, actions)
	if opening {
		m.approval.resumePhase = resumePhase
		m.phase = phaseAwaitingApproval
		m.activeTool = ""
		m.toolProgress = ""
	}
	// Force-flush via afterEvent (refreshView + reader re-arm), like every other
	// non-delta boundary: any pending coalesced assistant tail must be rendered
	// into the viewport BEFORE the modal opens, so the transcript behind the modal
	// is current the moment it closes. refreshView updating m.vp underneath the
	// centred modal overlay is harmless — View() shows the modal for
	// phaseAwaitingApproval regardless.
	return m.afterEvent()
}

// resolveAsk sends the approval/denial on the SAME stream (ask_id correlation),
// advances the ask queue — popping the next surfaced ask into the modal, or
// closing it and resuming the run (spinner restarts) when the queue is empty. The
// send is wrapped in a command so a send error surfaces as a StreamErrMsg.
//
// It MUST NOT re-arm the stream reader (no m.waitCmd()): unlike a stream-event
// handler, an approval keypress consumes no message, and the PermissionAskMsg that
// opened the modal already armed the run's single reader (afterEvent) — still in
// flight, since the paused run has put nothing on the channel. Arming a second here
// would leak an extra reader that outlives the run (see streamMsg / the streamGen
// guard for why a leaked reader is dangerous across a queue-drain). One send, no
// reader: the existing one delivers the resume events — true on the queued-successor
// path too (still one send, no reader).
//
// This is the Model shim over the pure approvalState.resolveAsk: the surface
// mutates state + proposes the actions (notice, plan-review repop, the send); the
// shim executes them via dispatchApprovalActions, owns the phase assign, refreshes
// the view, and re-arms the spinner only on the awaitingApproval→running transition
// (kept in sync with TestResolveLastAskRearmsSpinner / TestSpinnerVisibleMatchesFooterRender).
// m.sp.Tick is NOT a stream reader — the no-extra-reader invariant is untouched.
func (m Model) resolveAsk(v client.Verdict) (tea.Model, tea.Cmd) {
	deps := (&m).approvalDeps()
	actions, adv := (&m.approval).resolveAsk(v)
	sendCmd := (&m).dispatchApprovalActions(deps, actions)
	m.refreshView()
	if adv.hasNext {
		// A queued successor took the head: the phase STAYS awaitingApproval, so the
		// spinner is still off-screen — no m.sp.Tick. Re-arm fires only when leaving
		// awaitingApproval INTO running (keep in sync with
		// TestSpinnerVisibleMatchesFooterRender).
		return m, sendCmd
	}
	// adv.resume is pre-clamped to idle|running by approvalState.advance() — the
	// single source of truth for the resume rule; do NOT re-clamp here.
	m.phase = adv.resume
	if adv.resume == phaseRunning {
		return m, tea.Batch(sendCmd, m.sp.Tick)
	}
	return m, sendCmd
}

// dispatchClick executes a ClickAction from the hit-test registry — the SINGLE
// executor for clickable regions (issue #555). Every action drives the SAME
// path its key chord would (a verdict click is identical to pressing the
// button's chord: set focus, then resolveAsk). New ClickAction kinds add ONE
// case here; they never grow a parallel click path.
func (m Model) dispatchClick(act ClickAction) (tea.Model, tea.Cmd) {
	switch act.kind {
	case clickAskVerdict:
		m.approval.ask.focus = act.focus
		return m.resolveAsk(focusVerdict(act.focus))
	default:
		return m, nil
	}
}

// askArgsMiniScrollRange is the Model shim over
// approvalState.miniScrollRange: it computes the maximum clamped YOffset of the
// modal's in-card args mini-viewport for the current ask/geometry (the wrapped
// args line count minus the view rows the modal declares — the SAME arithmetic
// permissionModalBodyParts lays out, so the scroll bound matches the render).
// Returns 0 when nothing is hidden (the scroll keys then no-op). The layout
// inputs ride approvalDeps so the surface never touches the Model's geometry.
func (m Model) askArgsMiniScrollRange() (maxOff int) {
	return m.approval.miniScrollRange((&m).approvalDeps())
}

// ---------------------------------------------------------------------------
// Pure surface halves (deps-taking; mutate approvalState, return actions)
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

// applyPermissionAsk is the pure surface half of the PermissionAskMsg reducer:
// it dedupes a known askID, enqueues a second ask behind an already-open modal
// (concurrent subagents park their child server-side), or opens the modal as
// the head — mutating ONLY the approval state (plus reporting the phase the
// ask interrupted so the Model shim can own the phase transition) and
// returning the actions the Model executes (populate the plan-review viewport
// for a plan ask). The surface proposes; the Model disposes.
//
// opening reports whether THIS ask became the visible head (false = deduped or
// enqueued); resumePhase is meaningful only when opening (the phase the ask
// interrupted — the shim records it as approval.resumePhase before entering
// phaseAwaitingApproval).
func (a *approvalState) applyPermissionAsk(msg client.PermissionAskMsg, open bool, interrupted phase, deps approvalDeps) (opening bool, resumePhase phase, actions []approvalAction) {
	// Defensive same-stream dedupe: an askID already visible, queued, or
	// answered/retracted this run is dropped (the streamGen guard already kills
	// stale-reader duplicates; this kills same-stream ones).
	if a.known(msg.AskID) {
		return false, 0, nil
	}
	next := pendingAsk{
		AskID:       msg.AskID,
		Tool:        msg.Tool,
		Args:        msg.Args,
		Reason:      msg.Reason,
		focus:       0,
		offerAlways: !isChildAsk(msg.AskID, deps.sessionID),
	}
	if open {
		// A modal is already open: a second ask ENQUEUES FIFO behind the visible
		// head instead of clobbering it. The visible ask and the phase are
		// untouched; the (1 of N) badge in the modal title and footer advertises
		// the queue.
		a.enqueue(next)
		return false, 0, nil
	}
	a.ask = next
	if isPlanAsk(next.Tool) {
		actions = append(actions, approvalAction{kind: approvalOpenPlanReview, ask: next, queued: len(a.queue), model: deps.modelID})
	}
	return true, interrupted, actions
}

// applyPermissionRetract is the pure surface half of the PermissionRetractMsg
// arm: the harness WITHDREW a surfaced ask (its owning subagent was cancelled
// while parked). Three cases against the FIFO ask queue (a.ask is the head): a
// match on the VISIBLE ask dismisses the modal and advances the queue; a match
// on a QUEUED ask removes it in place; an unknown/stale id is idempotently
// dropped. The visible-ask advance mirrors the resolve path's teardown (per-flavour
// clears, then the state advance); the Model shim owns the phase assign +
// spinner re-arm decision the returned advance implies.
func (a *approvalState) applyPermissionRetract(msg client.PermissionRetractMsg, open bool) (actions []approvalAction, adv approvalAdvance) {
	if open && a.ask.AskID == msg.AskID {
		a.markAskResolved(msg.AskID)
		actions = append(actions, approvalAction{kind: approvalAddNotice, text: "permission request withdrawn (subagent cancelled)"})
		a.clearPlanReview()
		a.clearAskArgsView()
		adv = a.advance()
		if adv.hasNext && isPlanAsk(adv.next.Tool) {
			actions = append(actions, approvalAction{kind: approvalOpenPlanReview, ask: adv.next, queued: adv.queued})
		}
		return actions, adv
	}
	if a.removeQueued(msg.AskID) {
		// A QUEUED (not-yet-visible) ask was withdrawn: remove it in place. The
		// notice is a visible muted scrollback line because the (1 of N) count
		// badge advertised the queued ask — its silent disappearance would
		// otherwise need explaining. The visible modal is untouched.
		a.markAskResolved(msg.AskID)
		actions = append(actions, approvalAction{kind: approvalAddNotice, text: "queued permission request withdrawn (subagent cancelled)"})
	}
	return actions, approvalAdvance{}
}

// markAskResolved records an answered/retracted askID into the resolvedAsks
// dedupe set, lazily initialising it (a reference type mutable through the
// value-receiver Model, same pattern as recordFileChange/filesSeen).
func (a *approvalState) markAskResolved(id string) {
	if a.resolvedAsks == nil {
		a.resolvedAsks = make(map[string]struct{})
	}
	a.resolvedAsks[id] = struct{}{}
}

// resolveAsk is the pure surface half of the verdict path: it records the
// answer, advances the ask queue (popping the next surfaced ask into the
// visible slot, or clearing it for the Model shim to resume the run), and
// returns the actions the Model executes — the stream send (the
// ResumeApproval frame, ask_id correlation) FIRST in intent, the transcript
// notice, and a plan-review repopulation when the successor is a plan ask. The
// Model's shim orders the OBSERVABLE effects byte-for-byte as before: the
// notice lands via dispatchApprovalActions, then the send is wrapped in a
// command so a send error surfaces as a StreamErrMsg.
//
// It MUST NOT re-arm the stream reader (no waitCmd): unlike a stream-event
// handler, an approval keypress consumes no message, and the PermissionAskMsg
// that opened the modal already armed the run's single reader (afterEvent) —
// still in flight, since the paused run has put nothing on the channel. One
// send, no reader: the existing one delivers the resume events — true on the
// queued-successor path too.
func (a *approvalState) resolveAsk(v client.Verdict) (actions []approvalAction, adv approvalAdvance) {
	askID := a.ask.AskID
	a.markAskResolved(askID)
	a.clearPlanReview()
	a.clearAskArgsView()
	adv = a.advance()

	var notice string
	switch v {
	case client.VerdictAllowAlways:
		notice = "permission allowed (always, this session)"
	case client.VerdictDeny:
		notice = "permission denied"
	default:
		notice = "permission allowed"
	}
	actions = append(actions, approvalAction{kind: approvalAddNotice, text: notice})
	if adv.hasNext && isPlanAsk(adv.next.Tool) {
		actions = append(actions, approvalAction{kind: approvalOpenPlanReview, ask: adv.next, queued: adv.queued})
	}
	actions = append(actions, approvalAction{kind: approvalSendApproval, askID: askID, verdict: v})
	return actions, adv
}

// onApprovalKey is the pure surface half of the approval key handler. It
// routes scroll keys to the open args/plan viewport, closes/toggles the
// ask-args view, moves the button focus, and resolves verdicts — mutating ONLY
// the approval state and returning (actions, advance) for the verdict paths.
// The Model's shim executes them (and owns the phase assign + spinner re-arm).
// The verdicts fall through even while a scrollable view owns the scroll keys,
// so the operator can act after reading.
func (a *approvalState) onApprovalKey(msg tea.KeyPressMsg, deps approvalDeps) (cmd tea.Cmd, actions []approvalAction, adv approvalAdvance, resolved bool) {
	// The full-screen ask-args view owns the keyboard while open (issue #488):
	// scroll keys route to argsVP, Cancel (esc) closes the view back to the
	// modal, RawArgs (r) toggles the pretty/raw tier and re-populates. The
	// verdict keys (A/W/D/enter/left/right/tab) fall through to the ordinary
	// action handlers so the operator can resolve the ask from inside the view.
	if a.argsViewOpen {
		if cmd, handled := a.argsScroll(msg, deps.keys); handled {
			return cmd, nil, approvalAdvance{}, false
		}
		if key.Matches(msg, deps.keys.Cancel) {
			a.clearAskArgsView()
			return nil, nil, approvalAdvance{}, false
		}
		if key.Matches(msg, deps.keys.RawArgs) {
			a.argsViewRaw = !a.argsViewRaw
			actions = append(actions, approvalAction{kind: approvalOpenArgsView, ask: a.ask, queued: len(a.queue)})
			return nil, actions, approvalAdvance{}, false
		}
	}
	// A plan ask owns the keyboard for SCROLL keys: route them to planVP. This
	// mirrors the conversation viewport's routing (pgup/pgdn delegate to the
	// viewport, home/end jump to top/bottom) so the scroll affordance is
	// identical to the running phase. up/down also scroll (NOT button focus) so
	// a long plan is navigable by the most natural keys; left/right/tab keep
	// button focus.
	if isPlanAsk(a.ask.Tool) {
		if cmd, handled := a.planScroll(msg, deps.keys); handled {
			return cmd, nil, approvalAdvance{}, false
		}
	}
	// A non-diff ask with hidden args rows owns the SCROLL keys for its in-card
	// args mini-viewport (issue #488): pgup/pgdn and up/down move askVPOffset
	// within the clamped range (no-op at the edges). left/right/tab keep button
	// focus; the verdict keys fall through.
	if !isPlanAsk(a.ask.Tool) && !isDiffCapableAskTool(a.ask.Tool) {
		if a.miniScrollKey(msg, deps) {
			return nil, nil, approvalAdvance{}, false
		}
	}
	// The focus ring is {0:allow, 2:deny} for a two-button modal and
	// {0:allow, 1:always, 2:deny} when always-allow is offered.
	ring := []int{0, 2}
	if a.ask.offerAlways {
		ring = []int{0, 1, 2}
	}
	idx := 0
	for i, v := range ring {
		if v == a.ask.focus {
			idx = i
			break
		}
	}
	switch msg.String() {
	case "right", "tab":
		a.ask.focus = ring[(idx+1)%len(ring)]
		return nil, nil, approvalAdvance{}, false
	case "left":
		a.ask.focus = ring[(idx-1+len(ring))%len(ring)]
		return nil, nil, approvalAdvance{}, false
	case "enter":
		actions, adv = a.resolveAsk(focusVerdict(a.ask.focus))
		return nil, actions, adv, true
	}
	if key.Matches(msg, deps.keys.AllowAlways) {
		if a.ask.offerAlways {
			actions, adv = a.resolveAsk(client.VerdictAllowAlways)
			return nil, actions, adv, true
		}
		return nil, nil, approvalAdvance{}, false
	}
	if key.Matches(msg, deps.keys.Allow) {
		actions, adv = a.resolveAsk(client.VerdictAllowOnce)
		return nil, actions, adv, true
	}
	if key.Matches(msg, deps.keys.Deny) {
		actions, adv = a.resolveAsk(client.VerdictDeny)
		return nil, actions, adv, true
	}
	return nil, nil, approvalAdvance{}, false
}

// focusVerdict maps a focus index (0=allow-once, 1=always, 2=deny) to its verdict.
func focusVerdict(focus int) client.Verdict {
	switch focus {
	case 1:
		return client.VerdictAllowAlways
	case 2:
		return client.VerdictDeny
	default:
		return client.VerdictAllowOnce
	}
}

// miniScrollKey moves the in-card args mini-viewport (askVPOffset) on a scroll
// key while the args full-screen view is NOT open. Returns handled=true only
// when the key was a scroll key AND there are hidden rows to scroll to;
// otherwise the key falls through to the action handlers. It is the pure
// surface half of the old Model.onAskArgsMiniScrollKey — the scroll range comes
// from deps so the surface never touches the Model's geometry.
func (a *approvalState) miniScrollKey(msg tea.KeyPressMsg, deps approvalDeps) bool {
	maxOff := a.miniScrollRange(deps)
	if maxOff <= 0 {
		return false
	}
	step := 0
	switch {
	case key.Matches(msg, deps.keys.ScrollU):
		step = -3
	case key.Matches(msg, deps.keys.ScrollD):
		step = 3
	case msg.String() == keyMenuUp:
		step = -1
	case msg.String() == keyMenuDown:
		step = 1
	default:
		return false
	}
	a.miniScroll(step, maxOff)
	return true
}

// miniScrollRange computes the maximum clamped YOffset of the modal's in-card
// args mini-viewport for the current ask/geometry: the wrapped args line count
// minus the view rows the modal declares (the SAME arithmetic
// permissionModalBodyParts lays out, so the scroll bound matches the render).
// Returns 0 when nothing is hidden (the scroll keys then no-op). The layout
// inputs (theme, region width/height) ride the deps so the surface never
// touches the Model.
func (a approvalState) miniScrollRange(deps approvalDeps) (maxOff int) {
	pretty, _, ok := askArgsContent(deps.theme, a.ask)
	if !ok || pretty == "" {
		return 0
	}
	// The SAME layout the render path lays out (askArgsMiniViewport) — never a
	// re-derived wrap/cap, so the scroll bound matches the frame by construction.
	return askArgsMiniViewport(deps.theme, pretty, deps.regionW, deps.regionH).maxOffset
}

// approvalExpandToggle is the approval arm of the ctrl+t handler (issue #488),
// the pure surface half of the old onExpandToolsKey approval branch: inside the
// permission modal ctrl+t ROUTES by ask type — a non-diff, non-plan ask
// opens/closes the full-screen ask-args view INSTEAD of toggling expandTools;
// a plan ask or an Edit/Write (diff-capable) ask reports approval=false so the
// Model keeps the in-modal expand behaviour byte-for-byte. The returned
// actions carry the view re-population (the Model executes it against its
// renderer).
func (a *approvalState) approvalExpandToggle() (actions []approvalAction, approval bool) {
	if isPlanAsk(a.ask.Tool) || isDiffCapableAskTool(a.ask.Tool) {
		return nil, false
	}
	if a.argsViewOpen {
		a.clearAskArgsView()
		return nil, true
	}
	a.argsViewOpen = true
	return []approvalAction{{kind: approvalOpenArgsView, ask: a.ask, queued: len(a.queue)}}, true
}

// approvalWheel is the approval arm of the mouse-wheel handler, the pure
// surface half of the old onMouseWheel approval arms: the full-screen ask-args
// view owns the wheel while open; a plan ask's wheel routes to the plan-review
// viewport; a non-diff ask's wheel over the centered card scrolls the in-card
// args mini-viewport (a wheel elsewhere keeps scrolling the conversation
// behind the modal, reported as approval=false so the Model's conversation
// arm runs). The card hit-test rides deps.cardRect so the wheel region and the
// click region consume the SAME rect source.
func (a *approvalState) approvalWheel(msg tea.MouseWheelMsg, deps approvalDeps) (cmd tea.Cmd, approval bool) {
	if a.argsViewOpen && a.argsVPReady {
		a.argsVP, cmd = a.argsVP.Update(msg)
		return cmd, true
	}
	if isPlanAsk(a.ask.Tool) && a.planVPReady {
		a.planVP, cmd = a.planVP.Update(msg)
		return cmd, true
	}
	if !isPlanAsk(a.ask.Tool) && !isDiffCapableAskTool(a.ask.Tool) {
		if rect, ok := deps.cardRect(); ok {
			mo := msg.Mouse()
			if rect.contains(mo.X, mo.Y) {
				// The modal's in-card args mini-viewport scrolls ONLY when the cursor
				// is over the card rect (a wheel elsewhere keeps scrolling the
				// conversation behind the modal).
				step := 3
				if mo.Button == tea.MouseWheelUp {
					step = -3
				}
				a.miniScroll(step, a.miniScrollRange(deps))
				return nil, true
			}
		}
	}
	return nil, false
}
