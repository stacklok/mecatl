package ui

import (
	"slices"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
)

// approval_state.go owns the approval modal's STATE cluster (issue #555 Phase
// 1): the pendingAsk value, the approvalState struct the Model embeds as its
// single `approval` field, and the pure queue-manipulation methods on it. The
// behavioural reducers live in approval_surface.go, the renderers in
// approval_render.go, and the click hit-test regions in approval_regions.go.

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

// approvalState is the approval modal's whole state cluster, embedded in the
// Model as the single `approval` field (issue #555 Phase 1): the visible ask,
// the FIFO queue behind it, the answered-set dedupe, and the scrollable
// surfaces (plan-review viewport, full-screen ask-args view, in-card args
// mini-viewport offset). The modal's reducers mutate this state and return
// actions for the Model to execute (the surface proposes, the Model disposes);
// the phase itself stays Model-owned.
//
// Invariants (kept from the loose-field layout):
//
//   - The head is always `ask`: phase==phaseAwaitingApproval ⟺
//     approval.ask.AskID != "".
//   - Concurrent subagents (team members, parallel Subagent calls) can surface
//     asks while one is already open; each parks its child server-side until
//     answered, so a second ask must queue, never clobber the first. Bounded in
//     practice by the server's child-concurrency gate — no client-side cap
//     needed. Head-advancement funnels through advance (the pure queue pop the
//     Model's resolveAsk/retract shims drive).
//   - resumePhase is the phase the currently-visible ask interrupted, recorded
//     by the PermissionAskMsg reducer (the ONLY ask-opening path). The advance
//     path returns to it when the queue drains — a wire ask resumes phaseRunning, a
//     /debug-ask resumes phaseIdle (never a spinner-running phase no run owns).
//   - resolvedAsks is the set of askIDs answered/retracted THIS run — a
//     defensive same-stream dedupe for re-delivered PermissionAskMsgs (the
//     streamGen guard already kills stale-reader duplicates; this kills
//     same-stream ones). Lazily initialised (markAskResolved); a reference type
//     mutable through the value-receiver Model, same pattern as filesSeen.
//   - planVPReady is true once the plan-review viewport has been populated for
//     the current plan ask (openPlanReviewView). It gates both the render path
//     (so a half-initialized planVP never renders) and the scroll-key routing
//     (so a scroll key before population does not no-op into an empty
//     viewport). planVPWidth/planVPHeight record the geometry planVP was LAST
//     populated at, so relayout can skip a no-op re-population (and the
//     expensive glamour re-wrap + SetContent it triggers) when the body region
//     did not actually change; a width/height change re-populates so the plan
//     re-wraps at the new size, the scroll offset preserved (clamped).
//     planVPFingerprint is the ask fingerprint (Tool + Args + queued + model)
//     planVP was LAST populated for — the no-op-repopulation short-circuit.
//   - askVPOffset is the YOffset of the permission modal's in-card args
//     mini-viewport (issue #488): the args region of a non-diff ask is a
//     height-capped plain string slice scrolled by this offset (NOT a third
//     viewport.Model — the body is rebuilt per frame/call from the same source,
//     and the hit-test reuses the same builder, so render and hit-test can
//     never desync).
//   - The full-screen ask-args view (issue #488) mirrors the planVP cluster
//     one-for-one: a dedicated viewport the ctrl+t full-args view populates
//     (openAskArgsView) for a non-diff, non-plan ask, sized to the body region
//     minus the pinned action bar. argsViewOpen is approval state alongside
//     the phase (NOT a new phase): the phase stays phaseAwaitingApproval and
//     the render/hit-test/key arms discriminate on this flag BEFORE the
//     generic modal arms. argsViewRaw selects the raw-JSON tier (RawArgs
//     toggle). argsVPWidth/argsVPHeight/argsVPFingerprint drive the same no-op
//     re-population short-circuit + resize re-wrap (YOffset preserved) as the
//     planVP trio.
type approvalState struct {
	ask               pendingAsk
	queue             []pendingAsk
	resumePhase       phase
	askVPOffset       int
	planVP            viewport.Model
	planVPReady       bool
	planVPWidth       int
	planVPHeight      int
	planVPFingerprint string
	argsVP            viewport.Model
	argsVPReady       bool
	argsVPWidth       int
	argsVPHeight      int
	argsVPFingerprint string
	argsViewRaw       bool
	argsViewOpen      bool
	resolvedAsks      map[string]struct{}
}

// reset zeroes the WHOLE approval state: the visible ask, the FIFO queue, the
// answered-set dedupe, and every scrollable surface (plan-review viewport,
// full-screen ask-args view, in-card args offset) — the fingerprint+geometry
// pairs all clear together. It subsumes clearPlanReview + clearAskArgsView and
// is the ONE teardown the Model's session/run boundaries (resetSession /
// endRun) call; the advance path still uses the per-flavour clears because it
// must preserve the queue/ask/resumePhase half. The pointer receiver lets the
// value-receiver resetSession mutate the shared viewport headers in place.
func (a *approvalState) reset() {
	*a = approvalState{}
}

// known reports whether askID is already visible (the modal head), queued, or
// answered/retracted this run — the duplicate-ask drop predicate.
func (a approvalState) known(id string) bool {
	if _, ok := a.resolvedAsks[id]; ok {
		return true
	}
	if a.ask.AskID == id {
		return true
	}
	return askQueueIndex(a.queue, id) >= 0
}

// enqueue appends a surfaced ask FIFO behind the visible head (concurrent
// subagents surface asks while one is already open; each parks its child
// server-side until answered, so a second ask must queue, never clobber the
// first).
func (a *approvalState) enqueue(ask pendingAsk) {
	a.queue = append(a.queue, ask)
}

// askQueueIndex returns the index of askID in the queue, or -1.
func askQueueIndex(q []pendingAsk, id string) int {
	return slices.IndexFunc(q, func(x pendingAsk) bool { return x.AskID == id })
}

// removeQueued removes the askID'd ask from the queue in place, reporting
// whether it was there (the queued-retract path). The visible head is
// untouched.
func (a *approvalState) removeQueued(id string) bool {
	i := askQueueIndex(a.queue, id)
	if i < 0 {
		return false
	}
	// Capacity-clamped append form: it appends into the very slice it splits, so
	// a stale alias can never observe a clobbered tail element.
	a.queue = append(a.queue[:i:i], a.queue[i+1:]...)
	return true
}

// approvalAdvance is the outcome of approvalState.advance: the new head (and
// its queue depth) when a successor took over, or — when the queue drained —
// the phase the visible ask interrupted (resume), defaulted to phaseRunning
// when unset (a wire ask; a test that bypassed the reducer).
type approvalAdvance struct {
	next    pendingAsk
	queued  int
	resume  phase
	hasNext bool
}

// advance pops the next queued ask into the visible slot, or — when the queue
// is empty — reports the phase to resume. It is the queue-manipulation half of
// the verdict/retract paths (whose Model shims own the phase assign, the spinner
// re-arm decision, and the plan-review re-population): the surface owns WHAT
// becomes visible, the Model owns the phase transition. Plain re-slice pop (no
// copy): the head is copied BY VALUE into the result, and the shared backing
// array is only ever touched through the one live Model the single-threaded Elm
// reducer
// returns — a stale alias in a discarded older Model copy is never observed.
func (a *approvalState) advance() approvalAdvance {
	if len(a.queue) > 0 {
		next := a.queue[0]
		a.queue = a.queue[1:]
		a.ask = next
		return approvalAdvance{next: next, queued: len(a.queue), hasNext: true}
	}
	a.ask = pendingAsk{}
	resume := a.resumePhase
	if resume != phaseIdle {
		resume = phaseRunning
	}
	a.resumePhase = 0
	return approvalAdvance{resume: resume}
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
func (a *approvalState) planScroll(msg tea.KeyPressMsg, keys keyMap) (cmd tea.Cmd, handled bool) {
	if !a.planVPReady {
		return nil, false
	}
	switch {
	case key.Matches(msg, keys.ScrollTop):
		a.planVP.GotoTop()
		return nil, true
	case key.Matches(msg, keys.ScrollBottom):
		a.planVP.GotoBottom()
		return nil, true
	case key.Matches(msg, keys.ScrollU), key.Matches(msg, keys.ScrollD):
		a.planVP, cmd = a.planVP.Update(msg)
		return cmd, true
	case msg.String() == keyMenuUp, msg.String() == keyMenuDown:
		// Arrow keys scroll the plan a line at a time (the plan-review view's
		// primary nav). They are NOT button-focus keys here (left/right/tab move
		// the button focus instead — documented in the plan-review footer hint),
		// so a long plan is navigable by the most natural keys without losing the
		// reading position.
		a.planVP, cmd = a.planVP.Update(msg)
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
func (a *approvalState) argsScroll(msg tea.KeyPressMsg, keys keyMap) (cmd tea.Cmd, handled bool) {
	if !a.argsVPReady {
		return nil, false
	}
	switch {
	case key.Matches(msg, keys.ScrollTop):
		a.argsVP.GotoTop()
		return nil, true
	case key.Matches(msg, keys.ScrollBottom):
		a.argsVP.GotoBottom()
		return nil, true
	case key.Matches(msg, keys.ScrollU), key.Matches(msg, keys.ScrollD):
		a.argsVP, cmd = a.argsVP.Update(msg)
		return cmd, true
	case msg.String() == keyMenuUp, msg.String() == keyMenuDown:
		a.argsVP, cmd = a.argsVP.Update(msg)
		return cmd, true
	}
	return nil, false
}

// miniScroll moves the in-card args mini-viewport (askVPOffset) by step,
// clamped to [0, maxOff]. Shared by the scroll-key handler and the
// wheel-over-card handler so both clamp identically.
func (a *approvalState) miniScroll(step, maxOff int) {
	a.askVPOffset = max(0, min(maxOff, a.askVPOffset+step))
}
