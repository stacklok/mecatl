package ui

import (
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
)

// models.go owns /models installation plus model-selection restart and persistence
// effects. The dynamic picker state and rendering live in models_surface.go; durable
// catalog state lives in models_catalog.go.

// openModels opens the dynamic picker, seeds its request ownership from the last
// root-synchronized catalog token, and starts a fresh listing request. The root
// token advances only when this surface closes, preserving root-owned startup and
// closed-picker result handling while the picker is open.
func (m Model) openModels() (tea.Model, tea.Cmd) {
	if m.phase != phaseIdle || m.deps.Models == nil {
		return m, nil
	}
	requestToken := m.modelCatalogRequestToken + 1
	m.gatewayNotice = ""
	m.ta.Blur()
	ti := textinput.New()
	ti.Placeholder = "filter models…"
	ti.SetWidth(40)
	ti.Focus()
	m.modal = &modelsState{
		view:         modelsPanel,
		requestToken: requestToken,
		catalog:      m.modelCatalog,
		provenance:   m.modelProvenanceLine(),
		loading:      true,
		filter:       ti,
		deps:         (&m).surfaceDeps(),
	}
	return m, tea.Batch(client.ListModelsCmd(m.deps.Ctx, m.deps.Models, requestToken), textinput.Blink)
}

// chooseModel applies a surface selection through the existing restart path.
func (m Model) chooseModel(sel client.ModelSelection, label string) (tea.Model, tea.Cmd, bool) {
	crossProvider := m.effectiveModel.ProviderID != "" && sel.ProviderID != m.effectiveModel.ProviderID
	if label == "" {
		label = modelSelLabel(sel)
	}
	m.pendingModelSwitchNote = modelSwitchNote(label, crossProvider)
	if m.sessionID != "" {
		return m.restartOnModelWithCarryover(sel)
	}
	return m.restartOnModel(sel)
}

// modelSwitchNote is the transient status note armed at chooseModel time and surfaced
// on the SessionReadyMsg rebind. Same-provider: "switched to <model> — conversation
// kept". Cross-provider: the honest caveat that the conversation carried but the
// prior model's provider-private reasoning cache was stripped (the server-side
// StripProviderState path). Kept as a helper so the wording is testable in isolation.
func modelSwitchNote(label string, crossProvider bool) string {
	if crossProvider {
		return "switched to " + label + " — conversation kept (prior reasoning cache dropped)"
	}
	return "switched to " + label + " — conversation kept"
}

// restartOnModel performs the restart-now handoff: it persists the pick
// per-workspace, records it as the explicit this-session pick (provenance), tears
// down ALL per-session client state bound to the OLD session, resets the
// conversation transcript, drives the phase back to phaseConnecting, and fires the
// restartOnModelCmd (CloseSession(old) → CreateSession(new, selector)). The new
// header/caps/effectiveModel all arrive on the resulting SessionReadyMsg, so the UI
// rebinds entirely from the NEW session. See restartOnModelCmd for the teardown
// rationale (esp. the stream-subscription invalidation).
func (m Model) restartOnModel(sel client.ModelSelection) (tea.Model, tea.Cmd, bool) {
	// Cancel any in-flight run FIRST: endRun bumps streamGen (invalidating the old
	// reader) and tears down the stream/cancelRun, so no goroutine stays subscribed to
	// the soon-to-be-closed session. Pass "" so endRun sets no stop-status (we set the
	// "switching model" status below). Safe even when idle (endRun is a no-op then).
	m = m.endRun("")

	oldID := m.sessionID
	m.modelCatalog.active = sel
	m.activeModel = sel
	m.pickedThisSession = sel
	// Suppress the first-run welcome splash for the rest of the run (resetSession
	// empties the conversation; without this the restart's empty-idle frame re-fires
	// the first-run splash on every model switch).
	m.restartedThisRun = true

	// Reset the conversation/transcript + all stream-accumulated session state via the
	// single resetSession seam (changed-files, usage, context size, queue, selection…).
	m = m.resetSession()

	// Rebind the rest of the per-session client state to "no session yet": the new
	// values arrive on the NEW session's SessionReadyMsg.
	m = m.bindSessionID("")
	m.effectiveModel = client.ResolvedModel{}
	m.caps = client.Capabilities{}
	m.restartFailed = false // a fresh attempt; clear any prior failure flag
	m.restartFailedForkID = ""
	m.phase = phaseConnecting
	m.statusMsg = "switching model — reconnecting…"

	m.refreshView()
	// m.sp.Tick re-arms the spinner for the transition into phaseConnecting (the
	// phase-gated spinner.TickMsg handler dropped the chain in the prior phase).
	return m, tea.Batch(m.restartOnModelCmd(oldID, sel), m.saveSelectionCmd(sel), m.sp.Tick), true
}

// restartOnModelCmd closes the OLD session (best-effort) then creates a NEW session
// carrying the picked selector, off the update goroutine. On SUCCESS it returns the
// same SessionReadyMsg the connect path uses, so the reducer rebinds the session id,
// caps, and effectiveModel uniformly (no second code path). On FAILURE it returns a
// DISTINCT restartFailedMsg (NOT client.ConnectErrMsg): a ConnectErrMsg would drive
// the TERMINAL fatal screen, which is right for "never connected" but WRONG here — we
// just destroyed a working session at the user's request, so a transient blip (server
// hiccup, revoked key, rate limit) must stay RECOVERABLE. restartFailedMsg's reducer
// keeps the app usable (idle, retryable). A CloseSession failure is swallowed —
// orphaning a server-side session is preferable to blocking the re-create.
func (m Model) restartOnModelCmd(oldID string, sel client.ModelSelection) tea.Cmd {
	deps := m.deps
	return func() tea.Msg {
		if oldID != "" {
			_ = deps.Session.CloseSession(deps.Ctx, oldID)
		}
		id, caps, resolved, err := deps.Session.CreateSession(deps.Ctx, sel, m.desiredMode())
		if err != nil {
			return restartFailedMsg{err: err, model: modelSelLabel(sel)}
		}
		return client.SessionReadyMsg{SessionID: id, Capabilities: caps, ResolvedModel: resolved, Mode: m.desiredMode()}
	}
}

// restartOnModelWithCarryover is the seamless-switch handoff (issue #20): it
// mirrors restartOnModel (end the run, persist the pick, reset session state,
// phaseConnecting) but seeds the NEW session with the current session's
// conversation via carryoverCmd → CreateSessionWithCarryover. Reached for ANY
// pick with a live session (same- AND cross-provider) — the confirm overlay and
// its [c] key are gone, so chooseModel calls this unconditionally when a session
// exists. The server is the authority on same-vs-cross (validateCarryover): a
// same-provider carryover replays the history VERBATIM, a cross-provider
// carryover strips the provider-private replay blobs via StripProviderState
// (both adapters omit empty blobs, so a stripped history replays safely to any
// provider). Like restartOnModel it resetSession()s the LOCAL transcript — the
// server-side seed is what carries the history, NOT the client's m.conv (which
// is wiped so stale per-block renderer caches never key into a dead
// conversation); the new SessionReadyMsg + the next turn rebuild it from the
// seeded server history. The old session id is captured BEFORE the reset as the
// carryover source.
func (m Model) restartOnModelWithCarryover(sel client.ModelSelection) (tea.Model, tea.Cmd, bool) {
	// Cancel any in-flight run FIRST (same endRun rationale as restartOnModel: bump
	// streamGen + tear down the stream so nothing stays subscribed to the
	// soon-to-be-closed source session). Pass "" so endRun sets no stop-status (we set
	// the "carrying over" status below). Safe even when idle (endRun is a no-op then).
	m = m.endRun("")

	oldID := m.sessionID
	m.modelCatalog.active = sel
	m.activeModel = sel
	m.pickedThisSession = sel
	m.restartedThisRun = true

	// Reset the LOCAL conversation/transcript + stream-accumulated state (same
	// rationale as restartOnModel): the server carries the history, the client
	// rebuilds it from the seeded session. Stale per-block caches would otherwise key
	// into the dead conversation.
	m = m.resetSession()

	// Rebind the rest of the per-session client state to "no session yet": the new
	// values arrive on the new session's SessionReadyMsg.
	m = m.bindSessionID("")
	m.effectiveModel = client.ResolvedModel{}
	m.caps = client.Capabilities{}
	m.restartFailed = false
	m.restartFailedForkID = ""
	m.phase = phaseConnecting
	m.statusMsg = "switching model — carrying over conversation…"

	m.refreshView()
	return m, tea.Batch(m.carryoverCmd(oldID, sel), m.saveSelectionCmd(sel), m.sp.Tick), true
}

// carryoverCmd mirrors restartOnModelCmd but calls CreateSessionWithCarryover so the
// server seeds the new session's history from oldID's conversation. On SUCCESS it
// closes the OLD session best-effort AFTER the new one is ready (the server already
// snapshotted it at create time, so a late close is safe) and returns the SAME
// SessionReadyMsg the connect path uses (the reducer rebinds uniformly — no second
// code path). On FAILURE it returns restartFailedMsg (reuse the recoverable reducer
// path — NOT a new failure type): like a plain restart failure the local transcript
// is already gone, so the app stays idle + retryable. The retry re-creates FRESH
// (CreateSession, not carryover): the source session was closed below only on the
// SUCCESS path, so on failure oldID is still live — but a retry via enter-to-r
// re-fires restartOnModelCmd (create-fresh), which is the honest recoverable
// behaviour (the carryover affordance is re-offered on the next confirm, not
// auto-retried as carryover).
func (m Model) carryoverCmd(oldID string, sel client.ModelSelection) tea.Cmd {
	deps := m.deps
	return func() tea.Msg {
		id, caps, resolved, err := deps.Session.CreateSessionWithCarryover(deps.Ctx, oldID, sel, m.desiredMode())
		if err != nil {
			return restartFailedMsg{err: err, model: modelSelLabel(sel)}
		}
		// Close the source AFTER the new session is ready — the server snapshotted it
		// at create time, so a late close can't orphan the seed. Best-effort (swallowed).
		if oldID != "" {
			_ = deps.Session.CloseSession(deps.Ctx, oldID)
		}
		return client.SessionReadyMsg{SessionID: id, Capabilities: caps, ResolvedModel: resolved, Mode: m.desiredMode()}
	}
}

// switchEffort performs the /effort fork-resume handoff (ADR 0068): like
// restartOnModel it persists the pick, records provenance, ends any in-flight run,
// and drives phaseConnecting — but it deliberately does NOT resetSession(): the
// server forks the session's conversation onto the peer session, so the transcript
// SURVIVES. The forked session's usage/contextTokens are zero (a fresh aggregate),
// so the footer self-corrects from the new session's SessionReadyMsg + the next
// turn-end ResultMsg/footer-heal refetch — clearing them here would be a needless
// flicker on state that re-derives anyway. The fork carries provider/model (only
// the effort delta rides the call), so capabilities are unchanged across the fork;
// m.caps is left in place and the new session's SessionReadyMsg re-binds it.
func (m Model) switchEffort(sel client.ModelSelection) (tea.Model, tea.Cmd, bool) {
	// Cancel any in-flight run FIRST (same endRun rationale as restartOnModel: bump
	// streamGen + tear down the stream so nothing stays subscribed to the
	// soon-to-be-closed source session). Safe when idle (endRun is a no-op then).
	m = m.endRun("")

	oldID := m.sessionID
	m.modelCatalog.active = sel
	m.activeModel = sel
	m.pickedThisSession = sel
	// Suppress the first-run welcome splash for the rest of the run — the fork keeps
	// the transcript, but a fork at turn 0 (empty conversation) would otherwise
	// re-fire the splash on the switch's empty-idle frame (same rationale as
	// restartOnModel).
	m.restartedThisRun = true

	// NO resetSession() — the deliberate divergence from restartOnModel: the fork
	// carries the conversation, so m.conv (and the renderer's per-block caches, keyed
	// on the conversation index) stays intact. Usage/contextTokens/queue/asks stay
	// too: the forked session self-heals them (its usage is zero; the next ResultMsg
	// + footer heal re-derive the meter), and a queued/ask state belongs to the
	// user's uninterrupted flow, not to the old session id.

	// Rebind the rest of the per-session client state to "no session yet": the new
	// values arrive on the fork's SessionReadyMsg.
	m = m.bindSessionID("")
	m.effectiveModel = client.ResolvedModel{}
	m.caps = client.Capabilities{}
	m.restartFailed = false // a fresh attempt; clear any prior failure flag
	m.restartFailedForkID = ""
	m.phase = phaseConnecting
	m.statusMsg = "switching effort — forking conversation…"

	m.refreshView()
	// m.sp.Tick re-arms the spinner for the transition into phaseConnecting (the
	// phase-gated spinner.TickMsg handler dropped the chain in the prior phase).
	return m, tea.Batch(m.switchEffortCmd(oldID, sel), m.saveSelectionCmd(sel), m.sp.Tick), true
}

// switchEffortCmd forks the OLD session at the new effort off the update goroutine.
// On SUCCESS it closes the source session (best-effort, swallowed — orphaning is
// preferable to blocking the handoff) and refetches the fork's resolved model via
// GetSession (the fork RPC carries no capabilities/resolved-model echo), returning
// the SAME SessionReadyMsg the connect path uses so the reducer rebinds session id,
// caps, and effectiveModel uniformly. The resolved-model refetch is what drives the
// /effort cursor ● and the header effort suffix (the fork's effort echo).
// Capabilities come from the GetSession snapshot (snap.Capabilities), the
// same authoritative source as the footer-heal RefetchSessionCmd path.
// On FAILURE it returns the recoverable restartFailedMsg (NOT client.ConnectErrMsg)
// and the source session is deliberately NOT closed — the old session is still the
// user's live one, so a failed fork leaves it untouched (the recoverable reducer's
// enter-to-retry re-fires the fork).
func (m Model) switchEffortCmd(oldID string, sel client.ModelSelection) tea.Cmd {
	deps := m.deps
	return func() tea.Msg {
		newID, err := deps.Session.ForkSession(deps.Ctx, oldID, sel.ReasoningEffort)
		if err != nil {
			return restartFailedMsg{err: err, model: "effort " + effortLabel(sel.ReasoningEffort), viaFork: true, sourceID: oldID}
		}
		if oldID != "" {
			_ = deps.Session.CloseSession(deps.Ctx, oldID)
		}
		snap, err := deps.Session.GetSession(deps.Ctx, newID)
		if err != nil {
			return restartFailedMsg{err: err, model: "effort " + effortLabel(sel.ReasoningEffort), viaFork: true, sourceID: oldID}
		}
		return client.SessionReadyMsg{SessionID: newID, ResolvedModel: snap.ResolvedModel, Capabilities: snap.Capabilities, Mode: m.desiredMode()}
	}
}

// restartFailedMsg reports that a /models restart-now re-create (or a /worktrees
// re-create, or an /effort fork) FAILED. Distinct from client.ConnectErrMsg (which
// is terminal): its reducer (updateLifecycle) leaves the app RECOVERABLE — idle with
// no session, a loud error status naming the failed model, and enter-to-retry armed.
// model is the human label of the model that failed (for the status); err is the
// create error. viaFork + sourceID record the /effort fork origin (the fork's SOURCE
// session — still open) so the enter-retry re-fires the FORK from that source,
// preserving the transcript, instead of the create-fresh + resetSession restart path
// (which would wipe it — the exact thing the fork-resume switch exists to prevent).
// Zero for the /models + /worktrees failures, whose retry re-creates fresh (their
// old session is already gone).
type restartFailedMsg struct {
	err      error
	model    string
	viaFork  bool   // the failure came from the /effort fork (retry must re-fork, not re-create)
	sourceID string // viaFork only: the fork's SOURCE session id (still open; the retry re-forks from it)
}

// modelSelLabel is the human label for a selection used in the restart-failure
// status — the model id (the part the user picked), falling back to the provider id.
func modelSelLabel(sel client.ModelSelection) string {
	if sel.ModelID != "" {
		return sel.ModelID
	}
	return sel.ProviderID
}

// saveSelectionCmd persists the selection via the injected SelectionStore off the
// update goroutine. A nil store (persistence disabled) or a write failure is
// fail-soft: the in-memory active selection still holds for this run, and the
// failure surfaces as a muted notice — never a crash, never an aborted pick. The
// result arrives as a selectionSavedMsg.
func (m Model) saveSelectionCmd(sel client.ModelSelection) tea.Cmd {
	store := m.deps.SelectionStore
	ws := m.deps.Workspace
	return func() tea.Msg {
		if store == nil {
			return selectionSavedMsg{}
		}
		return selectionSavedMsg{err: store.Save(ws, sel)}
	}
}

// selectionSavedMsg is the result of a SelectionStore.Save (nil err on success).
type selectionSavedMsg struct{ err error }

// setGlobalDefault sets the picker CURSOR row as the client global default: it
// updates the in-memory ★ marker, shows a confirm toast, and fires the persist Cmd
// (SaveGlobalDefault, off the update goroutine). It does NOT change the active
// selection (the ● marker) or the live/next session — the global default only steers
// NEW/unseen workspaces. A cursor past the list end is a no-op (defensive).
func (m Model) setGlobalDefault(sel client.ModelSelection, label string) (tea.Model, tea.Cmd) {
	m.modelCatalog.globalDefault = sel
	m.statusMsg = m.deps.Theme.Style("success").Render(
		"global default set: " + sanitizeTerminal(label) + " (used by new workspaces)")
	return m, m.saveGlobalDefaultCmd(sel)
}

// saveGlobalDefaultCmd persists the global default via the injected SelectionStore
// off the update goroutine. Fail-soft like saveSelectionCmd: a nil store or a write
// failure surfaces as a muted notice (selectionSavedMsg), never a crash.
func (m Model) saveGlobalDefaultCmd(sel client.ModelSelection) tea.Cmd {
	store := m.deps.SelectionStore
	return func() tea.Msg {
		if store == nil {
			return selectionSavedMsg{}
		}
		return selectionSavedMsg{err: store.SaveGlobalDefault(sel)}
	}
}
