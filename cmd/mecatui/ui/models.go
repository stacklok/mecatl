package ui

import (
	"strconv"
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

// models.go is the /models picker — the FIRST *selecting* overlay (every other
// inventory overlay is read-only / esc-only). It mirrors the mcp.go resource
// picker's cursor+Choose model, NOT the soul/usermodel read-only model. Selecting a
// row SWITCHES IMMEDIATELY, always keeping the conversation: a live session is
// re-created via CreateSessionWithCarryover (server carries the history, any provider);
// with no live session the plain create path runs. The pick is persisted via the
// injected SelectionStore and a transient "switched to <model> — conversation kept"
// note surfaces on the rebind. Dropping context is /clear's job, not the switcher's.
// The picker renders purely from client.ModelInfo — no proto in ui.

// modelsView is the active /models overlay (none = closed). Like the other
// inventory overlays it is idle-only and dismissed with esc; UNLIKE them it has a
// real cursor and an enter-to-select action.
type modelsView int

const (
	modelsNone  modelsView = iota // overlay closed
	modelsPanel                   // the flat, type-to-filter picker
)

// modelsChrome is the number of non-row lines renderModelsPanel writes around the
// windowed row block (so the budget is one obvious expression, not magic numbers
// sprinkled in the loop). It accounts for the in-card lines — title (1), the
// provenance line (1, always reserved; the picker is idle-only so a current model is
// known), the filter input row (1), the blank after it (1), the footer's leading
// blank (1) + the two-line footer hint+legend (2) = 7 — plus the askCard border/
// padding the centred card adds (2). Mirrors team.go's roster chrome accounting; any
// small over-count (e.g. the rare connecting frame with no provenance) just shrinks
// the window by a row, never overflows.
const modelsChrome = 9

// modelsMinRows is the floor on visible rows so even a very short terminal still
// shows a usable window (mirrors team.go's teamMinRosterRows). The window still
// follows the cursor within those rows.
const modelsMinRows = 3

// modelsState holds the /models overlay state on the Model. Value-embedded so the
// Model stays a plain struct Update copies; the slices are replaced wholesale on
// each RPC result / filter recompute (never mutated in place).
type modelsState struct {
	view     modelsView
	loading  bool                  // the ListModels RPC is in flight
	err      error                 // last ListModels error, rendered distinctly
	models   []client.ModelInfo    // full list as relayed (provider_id,id-sorted)
	filtered []client.ModelInfo    // subset matching filter.Value(); recomputed on each key (mirror palette.filtered)
	filter   textinput.Model       // the type-to-filter input; focused while the picker is open
	cursor   int                   // index into FILTERED (clamped to its bounds)
	active   client.ModelSelection // the persisted/active selection (drives the ● marker)
	// globalDefault is the global `default:` block (drives the ★ marker + the
	// "global default" provenance label).
	globalDefault client.ModelSelection
	// statuses is the (possibly empty) operator-actionable per-provider
	// live-listing status list relayed alongside models. It currently carries
	// ToolHive gateway intent and openai-codex account-entitlement outcomes.
	statuses []client.ProviderStatus
	// intentProviders is the subset of status providers that are genuinely
	// config-detected intent providers (currently ToolHive). A model row whose
	// ProviderID is in this set carries an "org" segment. A Codex status row
	// never implies that label. nil when there are no intent providers.
	intentProviders map[string]bool
}

// openModels opens the picker and fires the ListModels RPC. Only callable while
// idle and when a model lister is wired; returns the model unchanged otherwise.
// The result arrives as a client.ModelsMsg handled in updateModelsMsg. The active
// selection is preserved across opens (it lives for the whole session), so the ●
// marker survives a close/reopen.
func (m Model) openModels() (tea.Model, tea.Cmd) {
	if m.phase != phaseIdle || m.deps.Models == nil {
		return m, nil
	}
	// Opening the picker is the operator acting on the gateway notice (Proposal 1):
	// dismiss it so the footer-left reverts to the status/ready line on close.
	m.gatewayNotice = ""
	m.ta.Blur() // overlay owns the keyboard while open
	m.models.view = modelsPanel
	m.models.loading = true
	m.models.err = nil
	m.models.cursor = 0
	// Open with the filter FOCUSED so the user can type to narrow immediately (the
	// headline use case at 300+ models). The filtered slice is (re)derived when the
	// list lands in updateModelsMsg; an empty query ⇒ filtered == models.
	ti := textinput.New()
	ti.Placeholder = "filter models…"
	// A fixed width so the placeholder + a typed query render in full (the bubbles
	// default width is 0, which truncates the view to a single glyph). Kept narrow
	// enough to sit inside the centred card at the test/default widths.
	ti.SetWidth(40)
	ti.Focus()
	m.models.filter = ti
	m.models.filtered = nil
	return m, tea.Batch(client.ListModelsCmd(m.deps.Ctx, m.deps.Models), textinput.Blink)
}

// closeModels dismisses the overlay and returns focus to the prompt input. The
// active selection + the loaded list survive (the picker is reopened cheaply); the
// filter is reset so a reopen starts clean (openModels re-News it regardless).
func (m Model) closeModels() (tea.Model, tea.Cmd) {
	m.models.view = modelsNone
	m.models.filter = textinput.Model{}
	m.models.filtered = nil
	cmd := m.ta.Focus()
	return m, cmd
}

// onModelsKey routes key presses while the picker is open. The filter input is
// FOCUSED, so the routing mirrors mcp.go's onPromptArgsKey: nav/action keys are
// intercepted first, everything else feeds the input.
//
// The single sharp edge (mirroring mcp.go:283-288): keys.Up/keys.Down also bind
// "k"/"j" (keys.go), so matching them with key.Matches would hijack a typed model
// name like "kimi"/"jamba". So list nav matches the ARROW keys by msg.String()
// ONLY; the other nav keys (pgup/pgdown/home/end) and the action keys
// (enter/esc) are all non-printable, so key.Matches against ScrollU/ScrollD/
// ScrollTop/ScrollBottom/Choose/Close is safe — none collide with typed text.
//
// esc is TWO-STAGE: a non-empty filter is cleared first (the picker stays open so
// a mistyped query can be undone without losing the open picker); an empty filter
// closes the picker. enter selects filtered[cursor]. Every key is handled=true
// (the open picker swallows keys), unchanged. After any input-feeding key the
// filter is re-synced (recompute + cursor clamp) and the input's cmd returned for
// the cursor blink.
func (m Model) onModelsKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd, bool) {
	if m.models.view == modelsNone {
		return m, nil, false
	}
	budget := m.modelsRowBudget()
	switch {
	case key.Matches(msg, m.keys.Close):
		if m.models.filter.Value() != "" {
			m.models.filter.SetValue("")
			m = m.syncModelsFilter()
			return m, nil, true
		}
		mm, cmd := m.closeModels()
		return mm, cmd, true
	case msg.String() == keyMenuUp:
		if m.models.cursor > 0 {
			m.models.cursor--
		}
		return m, nil, true
	case msg.String() == keyMenuDown:
		if m.models.cursor < len(m.models.filtered)-1 {
			m.models.cursor++
		}
		return m, nil, true
	case key.Matches(msg, m.keys.ScrollU):
		m.models.cursor = clampModelsCursor(m.models.cursor-budget, len(m.models.filtered))
		return m, nil, true
	case key.Matches(msg, m.keys.ScrollD):
		m.models.cursor = clampModelsCursor(m.models.cursor+budget, len(m.models.filtered))
		return m, nil, true
	case key.Matches(msg, m.keys.ScrollTop):
		m.models.cursor = 0
		return m, nil, true
	case key.Matches(msg, m.keys.ScrollBottom):
		m.models.cursor = clampModelsCursor(len(m.models.filtered)-1, len(m.models.filtered))
		return m, nil, true
	case key.Matches(msg, m.keys.SetGlobalDefault):
		mm, cmd := m.setGlobalDefault()
		return mm, cmd, true
	case key.Matches(msg, m.keys.Choose):
		return m.chooseModel()
	}
	// Everything else feeds the focused filter input (printable runes, backspace,
	// ←/→, …); recompute the filtered slice + clamp the cursor afterwards.
	var cmd tea.Cmd
	m.models.filter, cmd = m.models.filter.Update(msg)
	m = m.syncModelsFilter()
	return m, cmd, true
}

// clampModelsCursor clamps c to [0, n-1], or 0 when the list is empty.
func clampModelsCursor(c, n int) int {
	if n <= 0 {
		return 0
	}
	if c < 0 {
		return 0
	}
	if c >= n {
		return n - 1
	}
	return c
}

// filterModels returns the models whose provider_id, id, or display_name CONTAIN q
// (case-insensitive), preserving input order (already provider_id,id-sorted). An
// empty q returns the full list. Mirrors palette.filterCommands.
func filterModels(models []client.ModelInfo, q string) []client.ModelInfo {
	if q == "" {
		return models
	}
	lq := strings.ToLower(q)
	out := make([]client.ModelInfo, 0, len(models))
	for _, mi := range models {
		if strings.Contains(strings.ToLower(mi.ProviderID), lq) ||
			strings.Contains(strings.ToLower(mi.ID), lq) ||
			strings.Contains(strings.ToLower(mi.DisplayName), lq) {
			out = append(out, mi)
		}
	}
	return out
}

// syncModelsFilter recomputes the filtered slice from the current filter value and
// clamps the cursor into its bounds. Mirrors palette.syncPalette's recompute +
// clamp. Called on every input-feeding key and once when the list lands.
func (m Model) syncModelsFilter() Model {
	m.models.filtered = filterModels(m.models.models, m.models.filter.Value())
	if m.models.cursor >= len(m.models.filtered) {
		m.models.cursor = 0
	}
	return m
}

// chooseModel handles Enter on the cursor row: it switches IMMEDIATELY, always
// keeping the conversation — the seamless model-switch UX (no confirm overlay).
// Dropping context is the job of /clear, not the model switcher. The server accepts
// carryover for ANY provider (same-provider verbatim, cross-provider stripped), so
// there is no client-side same-provider gate.
//
//   - If a LIVE session exists (m.sessionID != "") → restartOnModelWithCarryover:
//     a new session seeded from the current one via CreateSessionWithCarryover.
//   - If NO live session yet (pre-first-connect, or a failure left no session) →
//     restartOnModel: the plain create path (there's no source to carry from).
//
// Either way a transient status note is armed (pendingModelSwitchNote) so the
// SessionReadyMsg rebind surfaces "switched to <model> — conversation kept" (or, for
// a cross-provider switch, the honest caveat that the prior model's cached reasoning
// was stripped). A cursor past the list end (or an already-empty list) is a no-op.
func (m Model) chooseModel() (tea.Model, tea.Cmd, bool) {
	if m.models.cursor < 0 || m.models.cursor >= len(m.models.filtered) {
		return m, nil, true
	}
	chosen := m.models.filtered[m.models.cursor]
	sel := client.ModelSelection{ProviderID: chosen.ProviderID, ModelID: chosen.ID}
	// Detect a cross-provider switch BEFORE the reset (resetSession zeroes
	// effectiveModel). A prior provider of "" means no live session yet, so the
	// plain-create path runs and the note is the simple "conversation kept" form
	// (there's nothing to strip — there was no prior model).
	priorProvider := m.effectiveModel.ProviderID
	crossProvider := priorProvider != "" && chosen.ProviderID != priorProvider
	m.pendingModelSwitchNote = modelSwitchNote(modelLabel(chosen), crossProvider)
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
	m.models.active = sel
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

	mm, cmd := m.closeModels() // dismiss the overlay, return focus to the prompt
	m = mm.(Model)
	m.refreshView()
	// m.sp.Tick re-arms the spinner for the transition into phaseConnecting (the
	// phase-gated spinner.TickMsg handler dropped the chain in the prior phase).
	return m, tea.Batch(cmd, m.restartOnModelCmd(oldID, sel), m.saveSelectionCmd(sel), m.sp.Tick), true
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
	m.models.active = sel
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

	mm, cmd := m.closeModels() // dismiss the overlay, return focus to the prompt
	m = mm.(Model)
	m.refreshView()
	return m, tea.Batch(cmd, m.carryoverCmd(oldID, sel), m.saveSelectionCmd(sel), m.sp.Tick), true
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
	m.models.active = sel
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

// liveModelLabel is the human label for the model the LIVE (current) session is
// running on — the effective model the server resolved, name-resolved from the
// inventory, falling back to the raw id, then to "the current model" when nothing is
// known yet. Used in the switch-next notice so it names both models honestly.
func (m Model) liveModelLabel() string {
	rm := m.effectiveModel
	if rm.ModelID == "" {
		return "the current model"
	}
	for _, mi := range m.models.models {
		if mi.ProviderID == rm.ProviderID && mi.ID == rm.ModelID && mi.DisplayName != "" {
			return mi.DisplayName
		}
	}
	return rm.ModelID
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
func (m Model) setGlobalDefault() (tea.Model, tea.Cmd) {
	if m.models.cursor < 0 || m.models.cursor >= len(m.models.filtered) {
		return m, nil
	}
	chosen := m.models.filtered[m.models.cursor]
	sel := client.ModelSelection{ProviderID: chosen.ProviderID, ModelID: chosen.ID}
	m.models.globalDefault = sel // the ★ marker tracks it immediately
	m.statusMsg = m.deps.Theme.Style("success").Render(
		"global default set: " + sanitizeTerminal(modelLabel(chosen)) + " (used by new workspaces)")
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

// updateModelsMsg reduces the client.ModelsMsg into the picker AND performs the
// key-removed reconcile (§4): when the loaded list does NOT contain the active
// selection's PROVIDER, the active selection is CLEARED to the server default (so
// subsequent creates don't send a now-unavailable provider — which the server
// would reject with InvalidArgument) and a loud notice fires. The reconcile is
// PROVIDER-level only (issue #41): a selection whose provider is present but whose
// exact model is absent from the snapshot is KEPT — the server validates the model
// string verbatim, and the boot snapshot may be the embedded catalog floor before
// the async live refresh lands. The state file is NOT rewritten (the key may
// return next launch).
//
// During CONNECT (phaseConnecting), this is the first leg of the §4 sequence:
// ListModels lands, the persisted selection is reconciled, THEN it fires
// CreateSession (carrying the now-validated selection) — so the startup create
// never sends a removed provider. The returned Cmd is the create in that case, nil
// otherwise. handled=false for any other msg.
func (m Model) updateModelsMsg(msg tea.Msg) (tea.Model, tea.Cmd, bool) {
	switch msg := msg.(type) {
	case client.ModelsMsg:
		if m.browsingStartupSessions {
			m.modelsReconciled = true
		}
		m.models.loading = false
		if msg.Err != nil {
			m.models.err = msg.Err
			// A failed ListModels carries no statuses — keeping stale ones would
			// render a PRIOR (possibly unrelated) provider remediation line
			// (e.g. "toolhive: proxy not reachable…") beneath THIS error, which
			// is dishonest: this error may be a transient RPC failure that has
			// nothing to do with that provider (review finding 5).
			m.models.statuses = nil
			m.models.intentProviders = nil
			// A connect-time ListModels error must NOT strand the UI at "connecting…":
			// proceed to create with the seeded (unreconciled) selection. The picker
			// still surfaces the error when opened. This is rare (the lister is the
			// dialled client), and degrading to "try the create" is safer than hanging.
			if m.phase == phaseConnecting {
				return m, m.createSessionCmd(), true
			}
			return m, nil, true
		}
		m.models.err = nil
		m.models.models = msg.Models
		m.models.statuses = msg.Statuses
		// Derive the genuine config-intent subset. provider_status also carries
		// Codex entitlement outcomes, which are actionable but are not an "org"
		// provider tier.
		m.models.intentProviders = intentProviderSet(msg.Statuses)
		// Derive the filtered slice (+ clamp the cursor) from the current filter
		// value; on a fresh open the filter is empty, so filtered == models.
		m = m.syncModelsFilter()
		m = m.reconcileSelection()
		// Idle footer notice (Proposal 1): when an intent-driven provider is
		// detected-and-reachable but NOT the active default, fire a dismissable
		// notice ONCE per process. At connect (phaseConnecting) the footer owns the
		// "connecting…" line, so the notice is only armed for the idle phase here;
		// a post-connect ModelsMsg (re-open / live refresh) arms it at idle.
		if !m.gatewayNoticeShown {
			if row, ok := availableNotDefaultStatus(msg.Statuses); ok {
				pid := sanitizeTerminal(row.ProviderID)
				m.gatewayNotice = pid + " gateway available (" +
					strconv.Itoa(int(row.ModelCount)) +
					" models, no API key needed) — /models to use it, or --default-provider " + pid
				m.gatewayNoticeShown = true
			}
		}
		if m.phase == phaseConnecting {
			// Reconcile done; now create the session with the validated selection.
			return m, m.createSessionCmd(), true
		}
		return m, nil, true
	case selectionSavedMsg:
		if msg.err != nil {
			m.statusMsg = m.deps.Theme.Style("warning").Render(
				"model set for this run, but could not persist: " + sanitizeTerminal(msg.err.Error()))
		}
		return m, nil, true
	default:
		return m, nil, false
	}
}

// reconcileSelection clears the active selection to the server default when its
// PROVIDER has no row in the loaded list (the key-removed fallback) and fires a
// loud notice. The check is PROVIDER-level, not exact-row (issue #41): the server
// validates the MODEL string verbatim on CreateSession (an unknown provider errors
// loudly; a non-empty model_id on a known provider is passthrough), and the boot
// snapshot may still be the EMBEDDED catalog floor before the async live refresh
// lands — so a saved model missing from the snapshot is KEPT and sent anyway
// (clearing it here silently downgraded the boot session to the server default).
// A server-rejected selection is handled by createSessionCmd's loud fallback leg,
// not here. A kept-but-absent selection simply renders no ● row in the picker (no
// phantom row). A zero active selection (server default already in use) is left
// untouched. It returns the model with the (possibly cleared) selection; the
// state file is never rewritten here.
func (m Model) reconcileSelection() Model {
	if m.models.active.IsZero() {
		return m
	}
	providerAvailable := false
	for _, mi := range m.models.models {
		if m.models.active.Matches(mi) {
			return m // exact row still available — keep it
		}
		if mi.ProviderID == m.models.active.ProviderID {
			providerAvailable = true
		}
	}
	if providerAvailable {
		// The provider is still available; the exact model is just absent from this
		// snapshot (likely the embedded floor pre-live-refresh, or a fresh release the
		// curated subset doesn't carry). Keep the selection — the server is the
		// authority on the model string (issue #41).
		return m
	}
	// The persisted provider is gone (key removed since last launch). Fall
	// back to the server default for THIS run, loudly, without rewriting the state
	// file (the preference may come back next launch).
	gone := m.models.active.ModelID
	if gone == "" {
		gone = m.models.active.ProviderID
	}
	m.models.active = client.ModelSelection{}
	m.activeModel = client.ModelSelection{}
	notice := "saved model " + sanitizeTerminal(gone) +
		" is no longer available (provider key removed?) — using the server default"
	// Name the model the session actually fell back to, when known. The effective
	// model lands on SessionReadyMsg; at connect-time reconcile it is usually not yet
	// populated (reconcile precedes the create), so this only appends post-connect
	// (e.g. a reconcile triggered by reopening the picker). Append only when populated
	// so the notice never reads "now running " with an empty model.
	if id := m.effectiveModel.ModelID; id != "" {
		notice += " — now running " + sanitizeTerminal(id)
	}
	m.statusMsg = m.deps.Theme.Style("warning").Render(notice)
	return m
}

// renderModelsOverlay draws the picker centred over the conversation region via
// centerCard. prov is the precomputed provenance line (modelProvenanceLine). All
// server-derived strings are terminal-sanitized.
func renderModelsOverlay(th theme.Theme, st modelsState, caps client.Capabilities, prov string, hk helpKeys, width, height int) string {
	switch st.view {
	case modelsPanel:
		return centerCard(th, renderModelsPanel(th, st, caps, prov, hk, modelsRowBudgetFor(height)), width, height)
	default:
		return ""
	}
}

// modelProvenanceLine is the "current: <model> (<provenance>)" line for the picker
// header — a best-effort DISPLAY hint derived ENTIRELY from client-held state (no
// server round-trip, no resolution logic; the server owns the truth). It returns ""
// when no current model is known yet (still connecting). Provenance is decided in
// precedence order against the effective model the server resolved THIS session to:
//
//   - picked this session — the user explicitly chose it via a restart-now confirm.
//   - --model flag — it equals the launch-time --model (and that flag was set).
//   - workspace default — it equals the per-workspace state-file entry loaded at launch.
//   - global default — it equals the global default block.
//   - server default — none of the above matched (the server's own default).
//
// Provenance hint (Proposal 3): when an intent-driven provider is
// available_not_default AND the current session's default provider is key-driven,
// a muted hint is appended naming the gateway (vendor-neutral, from the status row's
// ProviderID) outranked by the session's default provider. This fires ONLY when the
// outranking condition holds, so a toolhive-default session shows nothing.
func (m Model) modelProvenanceLine() string {
	eff := client.ModelSelection{ProviderID: m.effectiveModel.ProviderID, ModelID: m.effectiveModel.ModelID}
	if eff.ModelID == "" {
		return ""
	}
	label := m.liveModelLabel()
	line := "current: " + sanitizeTerminal(label) + " (" + m.modelProvenance(eff) + ")"
	// Provenance hint (Proposal 3): an available intent-driven alternative is
	// outranked by the current default provider (key-driven). The gateway name comes
	// from the status row's ProviderID (vendor-neutral); the default provider is the
	// live session's provider (m.effectiveModel.ProviderID). Suppressed when the
	// gateway IS the default (no outranking), when no available_not_default row exists,
	// or when the default IS itself intent-driven (intentProviders[eff.ProviderID]) —
	// an intent-driven default has no key, so "outranked by your … key" would mislead.
	if row, ok := availableNotDefaultStatus(m.models.statuses); ok {
		if row.ProviderID != eff.ProviderID && !m.models.intentProviders[eff.ProviderID] {
			line += " · " + sanitizeTerminal(row.ProviderID) +
				" gateway also available — outranked by your " + sanitizeTerminal(eff.ProviderID) + " key"
		}
	}
	return line
}

// modelProvenance returns the best-effort provenance word for the effective
// selection eff (see modelProvenanceLine for the precedence). Split out so it is
// unit-testable without rendering.
func (m Model) modelProvenance(eff client.ModelSelection) string {
	switch {
	case !m.pickedThisSession.IsZero() && m.pickedThisSession == eff:
		return "picked this session"
	case m.deps.Model != "" && m.deps.Model == eff.ModelID:
		return "--model flag"
	case m.deps.WorkspaceDefaultSet && m.deps.WorkspaceDefault == eff:
		return "workspace default"
	case !m.deps.GlobalDefault.IsZero() && m.deps.GlobalDefault == eff:
		return "global default"
	case statusAutoSelected(m.models.statuses, eff.ProviderID):
		// Issue #262 review finding 7: the vendor-name check ("toolhive") could
		// never see a server-side --default-model, so an operator-configured
		// toolhive default was ALSO mislabeled "auto-selected". The server-side
		// DefaultModelAutoSelected bit (wire: default_model_auto_selected) is the
		// honest, vendor-neutral signal — set ONLY when the server itself
		// resolved this provider's default model from a first-listed
		// heal/probe pick, never when an operator chose it.
		return "auto-selected"
	default:
		return "server default"
	}
}

// statusAutoSelected reports whether statuses carries a row for providerID
// with DefaultModelAutoSelected set — the ONE place modelProvenance's
// "auto-selected" branch reads the server-side provenance bit, so a future
// non-toolhive intent-driven provider is labeled correctly with no code
// change here.
func statusAutoSelected(statuses []client.ProviderStatus, providerID string) bool {
	for _, s := range statuses {
		if s.ProviderID == providerID {
			return s.DefaultModelAutoSelected
		}
	}
	return false
}

// intentProviderSet projects only genuine config-detected intent providers from
// the broader operator-status list. ToolHive is the sole such provider today;
// openai-codex has an actionable entitlement status but is manually configured
// and must never gain the "org" tier label. A future intent-driven provider must
// be added here alongside its server registration contract.
func intentProviderSet(statuses []client.ProviderStatus) map[string]bool {
	var out map[string]bool
	for _, s := range statuses {
		if s.ProviderID == "toolhive" {
			if out == nil {
				out = make(map[string]bool, 1)
			}
			out[s.ProviderID] = true
		}
	}
	return out
}

// availableNotDefaultStatus returns the FIRST status row with
// AvailableNotDefault==true (a reachable intent-driven provider that is NOT the
// active default) — the trigger for the idle footer notice + the provenance hint.
// ok is false when no such row exists (no gateway, gateway unreachable, or the
// gateway IS the default — all of which correctly suppress both surfaces).
func availableNotDefaultStatus(statuses []client.ProviderStatus) (client.ProviderStatus, bool) {
	for _, s := range statuses {
		if s.AvailableNotDefault {
			return s, true
		}
	}
	return client.ProviderStatus{}, false
}

// modelsRowBudgetFor converts an available card height into the number of model
// rows the windowed list may show, floored at modelsMinRows so a short terminal
// still shows a usable window. The chrome (title/filter/footer/card border) is
// subtracted via the single modelsChrome const. A height of 0 (unsized) yields the
// floor.
func modelsRowBudgetFor(height int) int {
	b := height - modelsChrome
	if b < modelsMinRows {
		return modelsMinRows
	}
	return b
}

// modelsRowBudget is the row budget derived from the live terminal height (the
// conversation viewport height, the same value view.go passes into the overlay).
// Used by the pgup/pgdown paging keys so a page equals one window.
func (m Model) modelsRowBudget() int {
	return modelsRowBudgetFor(m.vp.Height())
}

// modelsDisabledNote is the empty-state copy when model selection is NOT available
// on the connected server (caps.ModelSelection == false / zero providers).
const modelsDisabledNote = "Model selection is not available on this server.\n" +
	"Configure a provider on the server, then reconnect."

// modelsErrorHint is the next-action line rendered beneath a raw ListModels
// RPC error (review UX finding: a bare "✗ list models: <error>" was a dead
// end — it names neither a likely cause nor where to look). It is
// deliberately generic (the RPC can fail for many reasons — a crashed
// mecated, a network blip, a stale session) rather than guessing a specific
// provider's remediation, which belongs to the provider_status lines instead.
const modelsErrorHint = "the model service may be unavailable — check mecated is running " +
	"(log: $XDG_STATE_HOME/mecatl/mecatui.log)"

// modelsGatewayEmptyNote is the issue #262 R6.2 empty-state copy for a
// provider whose live-listing status is "empty" (a reachable, authorized
// credential that simply lists zero models) — distinct from the generic
// "No selectable models advertised." (which reads as "nothing is configured
// at all") and from modelsDisabledNote (which reads as "you haven't set a
// key"): here a gateway IS configured and reachable, so the remedy is
// organizational, not a local flag/key fix. This is DERIVED FROM, not kept
// word-for-word identical to, the server's toolhiveStatusHints[statusEmpty]
// (internal/app/registry.go): the server names ToolHive explicitly, while
// this client copy is DELIBERATELY vendor-neutral ("your gateway", not "your
// ToolHive gateway") — a future non-ToolHive intent-driven provider must read
// naturally here without a client change. Do not "fix" this by re-syncing the
// wording verbatim.
const modelsGatewayEmptyNote = "your gateway credential lists no models — ask your platform admin or re-run `thv llm setup`"

// modelsEmptyCopy returns the empty-state line. It is checked in THIS order
// (issue #262 review finding 6): (1) ANY promoted non-ok status — not just
// "empty" — is the empty-state CAUSE, checked FIRST, so a sole
// unreachable/unauthorized provider never lands on the generic "not
// available"/"nothing advertised" copy (which is contradictory alongside a
// remediation line naming the real cause); (2) the "not available" note (with
// remedy) when caps.ModelSelection is false; (3) else the generic "enabled
// but empty" note.
func modelsEmptyCopy(caps client.Capabilities, statuses []client.ProviderStatus) string {
	if s, ok := promotedStatus(statuses); ok {
		if s.State == "empty" {
			// The gateway-specific note (issue #262 R6.2): a reachable, authorized
			// credential that simply lists zero models — an organizational fix,
			// not a local flag/key fix.
			return modelsGatewayEmptyNote
		}
		// unreachable/unauthorized (and any future state): the SAME line
		// renderProviderStatusLines would build for this status — single-
		// sourced via providerStatusLine so the two surfaces cannot drift.
		return providerStatusLine(s)
	}
	if !caps.ModelSelection {
		return modelsDisabledNote
	}
	return "No selectable models advertised."
}

// promotedStatus returns the FIRST status entry whose State is neither ""
// (unset) nor "ok" — the ONE cause modelsEmptyCopy promotes to the top-level
// empty-state line (issue #262 review finding 6). ok is false when every
// entry is empty-State/"ok" (or statuses itself is empty).
func promotedStatus(statuses []client.ProviderStatus) (client.ProviderStatus, bool) {
	for _, s := range statuses {
		if s.State != "" && s.State != "ok" {
			return s, true
		}
	}
	return client.ProviderStatus{}, false
}

// toolhiveStatusCopy maps a non-ok provider_status state to the short
// human-readable clause rendered before the hint (issue #262 R6.2). "empty"
// IS included here (unlike the original cut): when the OVERALL model
// inventory is non-empty (other providers have models), a reachable-but-empty
// toolhive would otherwise be invisible in the picker — modelsEmptyCopy's
// replacement note only fires when the WHOLE list is empty. See
// renderProviderStatusLines for the suppression rule that keeps the two from
// double-stating the same remediation when the inventory IS empty.
var toolhiveStatusCopy = map[string]string{
	"unreachable":  "proxy not reachable",
	"unauthorized": "gateway rejected the credential",
	"empty":        "credential lists no models",
}

var openAICodexStatusCopy = map[string]string{
	"unreachable":  "ChatGPT Codex service not reachable",
	"unauthorized": "manual token rejected",
	"empty":        "account lists no selectable models",
}

// providerStatusLine builds the one-line remediation clause for a non-ok
// status: "<provider_id>: <short copy> — <hint>" (issue #262 R6.2). Extracted
// (review finding 6) so modelsEmptyCopy's promoted-cause line and
// renderProviderStatusLines' per-status lines are SINGLE-SOURCED and cannot
// drift on wording. A state absent from toolhiveStatusCopy (a future
// addition) still renders using the raw state string, so a new state is
// never silently dropped.
func providerStatusLine(s client.ProviderStatus) string {
	copyByState := toolhiveStatusCopy
	if s.ProviderID == "openai-codex" {
		copyByState = openAICodexStatusCopy
	}
	clause := copyByState[s.State]
	if clause == "" {
		clause = s.State
	}
	line := s.ProviderID + ": " + clause
	if s.Hint != "" {
		line += " — " + s.Hint
	}
	return line
}

// renderProviderStatusLines renders one muted line per non-ok status (issue
// #262 R6.2, generalized by review finding 6). inventoryEmpty reports
// whether the OVERALL model list (across every provider) is empty: when it
// IS, the ONE status modelsEmptyCopy PROMOTED to the top-level empty-state
// line is suppressed here (rendering both would double-state the same
// cause) — generalized from the original "empty-state suppressed" special
// case to ANY promoted state, since modelsEmptyCopy itself now promotes any
// non-ok status, not just "empty". When the inventory is NON-empty (a mixed
// deployment where OTHER providers have models), every non-ok status renders
// here — including "empty" — or it would otherwise be invisible. Returns nil
// when statuses is empty (or every entry is ok, or the sole non-ok entry was
// suppressed as the promoted cause) — the byte-identical no-op for every
// deployment without an intent-driven provider's trouble to report.
func renderProviderStatusLines(statuses []client.ProviderStatus, inventoryEmpty bool) []string {
	promoted, hasPromoted := promotedStatus(statuses)
	var lines []string
	for _, s := range statuses {
		if s.State == "" || s.State == "ok" {
			continue
		}
		if inventoryEmpty && hasPromoted && s == promoted {
			continue // modelsEmptyCopy's promoted-cause line already covers this
		}
		lines = append(lines, providerStatusLine(s))
	}
	return lines
}

// renderModelsPanel renders the flat type-to-filter picker: a title, the filter
// input row, then a WINDOWED slice of the filtered rows (the window follows the
// cursor via the shared scrollWindow helper so the selected row stays visible past
// the top/bottom edge), the cursor row highlighted, and a ● marker on the ACTIVE
// (persisted) row. Group headers are dropped; provider_id is folded into each row
// (so it is still visible AND a filter target). EVERY server-derived string is
// terminal-sanitized. rowBudget clips the list to the card height (the overflow
// fix — centerCard centres but does not clip).
func renderModelsPanel(th theme.Theme, st modelsState, caps client.Capabilities, prov string, hk helpKeys, rowBudget int) string {
	var b strings.Builder

	// The title carries a scroll-position indicator in the default (row-list) case so
	// the user knows the list is windowed (at 300+ models a 3-row window otherwise
	// hides that there are more rows off-screen). Computed once here so the window is
	// rendered with the SAME [start,end) the title reports.
	title := "Models"
	if !st.loading && st.err == nil && len(st.filtered) > 0 {
		start, end := scrollWindow(st.cursor, len(st.filtered), rowBudget)
		title += "  " + modelsPositionLabel(start, end, len(st.filtered))
	}
	b.WriteString(th.Style("askTitle").Render(title) + "\n")
	// Provenance line: the CURRENT (live) model + a best-effort label for where it
	// came from (server default / --model flag / picked this session / workspace
	// default / global default). Derived entirely from client-held state — a hint, not
	// authority. Omitted when nothing is known yet (still connecting). prov is passed
	// in by renderModelsPanel's caller (it needs Model-level state the modelsState
	// alone doesn't carry).
	if prov != "" {
		b.WriteString(th.Style("muted").Render(prov) + "\n")
	}
	b.WriteString(st.filter.View() + "\n\n")

	switch {
	case st.loading:
		b.WriteString(th.Style("muted").Render("loading…") + "\n")
	case st.err != nil:
		b.WriteString(th.Style("errorText").Render("✗ list models: "+sanitizeTerminal(st.err.Error())) + "\n")
		// UX: a bare error is a dead end — name the likely cause + where to look,
		// so the operator isn't left staring at an unexplained RPC failure.
		b.WriteString(th.Style("muted").Render(modelsErrorHint) + "\n")
	case len(st.models) == 0:
		// Server-side empty/disabled (no inventory at all) — distinct from a filter
		// that matched nothing.
		b.WriteString(th.Style("muted").Render(modelsEmptyCopy(caps, st.statuses)) + "\n")
	case len(st.filtered) == 0:
		// The filter matched nothing (the inventory is non-empty). A clear, distinct
		// note with a recovery hint; the cursor is safe (clamped to 0) and enter is a
		// no-op.
		b.WriteString(th.Style("muted").Render("no models match "+strconv.Quote(st.filter.Value())+" — "+hk.closeOnly+" to clear") + "\n")
	default:
		start, end := scrollWindow(st.cursor, len(st.filtered), rowBudget)
		for i := start; i < end; i++ {
			mi := st.filtered[i]
			b.WriteString(renderRow(th, modelRowText(st.active, st.globalDefault, st.intentProviders, mi), i == st.cursor) + "\n")
		}
	}

	// Provider-status remediation lines (issue #262 R6.2): one muted line per
	// non-ok status, under the list/empty state — an "empty" status is
	// suppressed ONLY when the OVERALL inventory is also empty (modelsEmptyCopy
	// already states that remediation); a mixed deployment (other providers
	// have models) still surfaces toolhive's "empty" here, or it would be
	// invisible. No statuses (or all ok) renders NOTHING — the byte-identical
	// no-op every existing golden pins. Gated on st.err == nil (review finding
	// 5, defense in depth alongside the updateModelsMsg-side clear): a
	// remediation line for a PRIOR provider outcome must never render beneath
	// an unrelated ListModels error, incl. the loading/stale-while-loading
	// frame before a fresh statuses list lands.
	if st.err == nil {
		for _, line := range renderProviderStatusLines(st.statuses, len(st.models) == 0) {
			b.WriteString(th.Style("muted").Render(sanitizeTerminal(line)) + "\n")
		}
	}

	// The "↑/↓" arrows stay literal: the models handler scrolls on the BARE
	// up/down keys (msg.String, deliberately NOT key.Matches on Up/Down so a
	// rebind to k/j can't hijack a typed model name). Paging IS keymap-bound, so
	// the compact one-sided cue reads the LIVE ScrollU marking; Choose,
	// SetGlobalDefault, and Close are live too (issue #457). With defaults the
	// hint is byte-identical to the historical literal.
	b.WriteString("\n" + th.Style("muted").Render(
		"type to filter · ↑/↓/"+hk.scrollUp+" move · "+hk.choose+" use · "+hk.setGlobalDefault+" set global default · "+hk.closeOnly+" clear filter / close"))
	b.WriteString("\n" + th.Style("muted").Render("● current  ★ global default"))
	// A "reason" segment marks a model that emits reasoning; the EFFORT tier for those
	// models is a separate per-session setting (ADR 0055) — point the user at /effort
	// rather than building a sub-picker inside this overlay.
	b.WriteString("\n" + th.Style("muted").Render("reason = emits reasoning · set its effort tier with /effort"))
	return b.String()
}

// modelsPositionLabel formats the scroll-position indicator for the title from a
// window's [start,end) bounds (0-based, end-exclusive) over total rows. It renders
// 1-based, end-INCLUSIVE: a partial window reads "(27–29 of 344)"; a window that
// holds the whole list reads "(4)" (no range when nothing is off-screen). total is
// the FILTERED count, so it tracks the filter. It is ASCII-safe + derived from
// internal ints (no server string), so it needs no sanitization.
func modelsPositionLabel(start, end, total int) string {
	if end-start >= total {
		return "(" + strconv.Itoa(total) + ")"
	}
	return "(" + strconv.Itoa(start+1) + "–" + strconv.Itoa(end) + " of " + strconv.Itoa(total) + ")"
}

// modelRowText builds one model row's content: a fixed-width 2-marker prefix, the
// provider_id segment, the label, and the capability + context-window segments. The
// marker column is two FIXED cells so a stripANSI'd row is layout-stable for goldens
// regardless of which markers a row carries: cell 1 is "●" on the ACTIVE/pending
// selection (else " "), cell 2 is "★" on the GLOBAL-DEFAULT row (else " "). A row
// that is both pending AND the global default shows "●★".
//
// intentProviders is the genuine config-intent subset of provider_status; a model
// row whose ProviderID is in it carries an "org" segment (Proposal 2 — ASCII,
// 3 chars, matching the img/reason token style). Actionable non-intent statuses
// such as openai-codex are deliberately absent. nil ⇒ no row carries the label.
func modelRowText(active, globalDefault client.ModelSelection, intentProviders map[string]bool, mi client.ModelInfo) string {
	activeMark := " "
	if active.Matches(mi) {
		activeMark = "●"
	}
	defMark := " "
	if !globalDefault.IsZero() && globalDefault.Matches(mi) {
		defMark = "★"
	}
	marker := activeMark + defMark + " "
	segs := modelCapSegments(mi)
	if intentProviders != nil && intentProviders[mi.ProviderID] {
		segs = append([]string{"org"}, segs...)
	}
	line := marker + sanitizeTerminal(mi.ProviderID) + " · " + sanitizeTerminal(modelLabel(mi))
	if len(segs) > 0 {
		line += "  " + strings.Join(segs, " ")
	}
	return line
}

// modelLabel is the human label for a model row: its display name, falling back to
// its id (the server already falls back, but be defensive against an empty one).
func modelLabel(mi client.ModelInfo) string {
	if mi.DisplayName != "" {
		return mi.DisplayName
	}
	return mi.ID
}

// modelCapSegments builds the fixed-token capability + context-window segments for
// a model row (ASCII-safe, layout-stable for goldens): "img" when Image, "reason"
// when Reasoning, and a humanized context window (reusing the footer's
// humanizeTokens — "200K"/"1.2M") OMITTED when unknown / 0 (never "0").
func modelCapSegments(mi client.ModelInfo) []string {
	var segs []string
	if mi.Image {
		segs = append(segs, "img")
	}
	if mi.Reasoning {
		segs = append(segs, "reason")
	}
	if mi.ContextLimit > 0 {
		segs = append(segs, humanizeTokens(mi.ContextLimit))
	}
	return segs
}
