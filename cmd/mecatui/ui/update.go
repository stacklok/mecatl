package ui

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	statusline "github.com/stacklok/mecatl/cmd/mecatui/statusline"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
	"github.com/stacklok/mecatl/cmd/mecatui/ui/welcome"
)

// renderInterval is the coalescing window for streamed deltas: one frame at
// ~60fps. Streamed assistant/reasoning deltas arrive far faster than the eye can
// see, and each one would otherwise trigger a full conversation re-render (and a
// fresh glamour render of the live, growing block — O(n²) over a turn). Instead a
// delta only marks the view dirty; a renderTickMsg fired on this interval flushes
// the dirty view at most once per frame. The visible cadence is unchanged.
const renderInterval = 16 * time.Millisecond

// renderTickMsg is the one-shot frame-cadence flush signal. A streamed delta arms
// it (via markDirty) when none is pending; the renderTickMsg handler flushes the
// dirty view and disarms, re-arming only if more deltas arrived in the meantime.
type renderTickMsg struct{}

// renderTickCmd schedules a single frame-cadence flush.
func (Model) renderTickCmd() tea.Cmd {
	return tea.Tick(renderInterval, func(time.Time) tea.Msg { return renderTickMsg{} })
}

// quitArmWindow is how long the double-ctrl+c guard stays armed: a first ctrl+c on
// an empty prompt arms it and shows a hint; a second ctrl+c within this window
// quits, otherwise the guard disarms (quitDisarmMsg) and the hint clears. Matches
// Claude Code's "press ctrl+c again to exit" grace window.
const quitArmWindow = 3 * time.Second

// doubleEscapeWindow bounds the physical Escape gesture used to abandon an idle
// draft. The reducer never reads a clock: a generation-tagged Tick message owns
// expiry, which keeps tests and stale-tick handling deterministic.
const doubleEscapeWindow = 500 * time.Millisecond

type doubleEscapeExpiryMsg struct{ gen int }

type doubleEscapeTimerFunc func(time.Duration, int) tea.Cmd

func scheduleDoubleEscapeExpiry(after time.Duration, gen int) tea.Cmd {
	return tea.Tick(after, func(time.Time) tea.Msg { return doubleEscapeExpiryMsg{gen} })
}

func (m Model) doubleEscapeExpiryCmd(gen int) tea.Cmd {
	timer := m.doubleEscapeTimer
	if timer == nil {
		timer = scheduleDoubleEscapeExpiry
	}
	return timer(doubleEscapeWindow, gen)
}

// disarmQuitGuards clears any armed quit guards the current keypress did NOT itself
// invoke (pressedQuit / pressedQuitD): an armed guard disarms on any key other than
// its own, so its "press again" window spans only its own consecutive presses. It
// also clears the guard's footer hint, but only when the hint is still the one that
// guard set. Called from onKey AFTER both quit branches so a confirm press reaches
// its handler first.
func (m Model) disarmQuitGuards(pressedQuit, pressedQuitD bool) Model {
	if m.quitArmed && !pressedQuit {
		m.quitArmed = false
		if m.statusMsg == quitHintFor(m.keys.Quit) {
			m.statusMsg = ""
		}
	}
	if m.quitDArmed && !pressedQuitD {
		m.quitDArmed = false
		if m.statusMsg == quitDHintFor(m.keys.QuitD) {
			m.statusMsg = ""
		}
	}
	return m
}

// quitHintFor builds the footer quit hint LIVE from the model's current Quit
// binding, so a rebound quit chord is advertised honestly. With the default
// binding ("ctrl+c") it is byte-identical to the historical "press ctrl+c again
// to quit". It doubles as the disarm/clear sentinel — the arm and the disarm
// both re-derive it from the SAME binding, so a mid-arm remap (a live reload
// path) cannot strand a stale hint.
func quitHintFor(b key.Binding) string {
	return "press " + firstKey(b, "ctrl+c") + " again to quit"
}

// quitDisarmMsg fires quitArmWindow after the guard is armed. Its gen is the arm
// generation it was scheduled with; the handler ignores it unless it still matches
// m.quitArmGen (a stale tick from a previous arm cannot disarm a fresh guard).
type quitDisarmMsg struct{ gen int }

// quitDisarmCmd schedules the one-shot disarm tick for arm generation gen.
func (Model) quitDisarmCmd(gen int) tea.Cmd {
	return tea.Tick(quitArmWindow, func(time.Time) tea.Msg { return quitDisarmMsg{gen} })
}

// quitDHintFor builds the ctrl+d quit hint LIVE from the QuitD binding — the
// QuitD-guard analogue of quitHintFor ("press ctrl+d again to quit" by default).
func quitDHintFor(b key.Binding) string {
	return "press " + firstKey(b, "ctrl+d") + " again to quit"
}

// quitDDisarmMsg is the QuitD guard's timed-disarm tick; the handler ignores it
// unless it still matches m.quitDArmGen.
type quitDDisarmMsg struct{ gen int }

// quitDDisarmCmd schedules the one-shot QuitD disarm tick for arm generation gen.
func (Model) quitDDisarmCmd(gen int) tea.Cmd {
	return tea.Tick(quitArmWindow, func(time.Time) tea.Msg { return quitDDisarmMsg{gen} })
}

// clickWindow is how long a multi-click sequence (single → double → triple) stays
// armed between presses: a press within this window of the previous one at the SAME
// logical position advances the count; otherwise the sequence has lapsed and the
// next press starts a fresh single click. It is enforced via the armed-generation +
// tea.Tick disarm pattern (clickDisarmMsg), NOT a wall clock read in the reducer —
// exactly like the double-ctrl+c quit guard, so the reducer stays a pure function of
// its messages.
const clickWindow = 400 * time.Millisecond

// clickDisarmMsg fires clickWindow after a counted press. Its gen is the click
// generation it was scheduled with; onClickDisarm ignores it unless it still matches
// m.clickGen (a stale tick from a prior press cannot reset a freshly-advanced count).
type clickDisarmMsg struct{ gen int }

// clickDisarmCmd schedules the one-shot multi-click disarm tick for click generation gen.
func (Model) clickDisarmCmd(gen int) tea.Cmd {
	return tea.Tick(clickWindow, func(time.Time) tea.Msg { return clickDisarmMsg{gen} })
}

// markDirty records that a streamed delta mutated the conversation and returns the
// updated model plus the command to drive the coalesced flush. It arms a one-shot
// renderTickMsg ONLY when none is already pending (tickArmed) — so a burst of deltas
// schedules exactly one tick, not one per delta — and returns the reader re-arm
// otherwise. The flush itself happens in the renderTickMsg handler, never per delta.
//
// It returns (Model, tea.Cmd) — the well-defined form — rather than mutating via a
// pointer receiver and being called as `return m.markDirty()`. In that operand
// form the Go spec leaves the order of evaluating the returned `m` value and the
// `m.markDirty()` call UNSPECIFIED, so the dirty/armed flags the method sets could
// be snapshotted into the return value BEFORE they are set — silently losing the
// arm and stalling the coalesced flush until the next boundary. Returning the model
// makes the flag updates land on exactly the model the caller returns, deterministically.
func (m Model) markDirty() (Model, tea.Cmd) {
	m.viewDirty = true
	if m.tickArmed {
		return m, m.waitCmd()
	}
	m.tickArmed = true
	return m, tea.Batch(m.waitCmd(), m.renderTickCmd())
}

// Update is the Elm reducer. It is split by message type; all model mutation and
// all glamour rendering happen here on the single update goroutine (the stream
// reader never touches the model). After most state changes it calls refreshView
// to re-render the conversation into the viewport.
//
// Streamed deltas (AssistantDeltaMsg/ReasoningDeltaMsg) do NOT refreshView per
// token: they append to the conversation and mark the view dirty (markDirty),
// which arms a single one-shot renderTickMsg (~16ms ≈ one 60fps frame) that flushes
// the dirty view at most once per frame and disarms. The invariant is "ONLY the
// delta cases defer; every other transition force-flushes" — turn/tool/result/error
// AND the permission.ask gate all flush (via afterEvent/endRun), so the pending tail
// is always rendered before any boundary (the final frame and event ordering are
// unchanged; only the per-token re-render churn is coalesced). refreshView clears
// viewDirty, making "rendered ⟺ not dirty" an invariant.
//
// Within each 16ms flush, the per-BLOCK render cache (renderer.blockCache, keyed
// on block.rev/width/expand) means only blocks whose rev, the wrap width, or the
// expand toggle changed actually re-render — in practice just the live tail
// block; every settled block joins the conversation string from cache. The
// selection splice and vp.SetContent still see the full joined string, so
// selection/scroll behaviour is unchanged.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// Capture whether the welcome splash was showing (idle + empty) BEFORE this
	// message, so the wrapper below can detect the empty→non-empty transition that
	// retires the kitty mascot image (the splash's placeholder cells stop being
	// emitted then anyway; an explicit a=d delete frees the terminal-side image and
	// keeps the state machine symmetric).
	wasKittyShowing := m.kittyActive && m.conv.isEmpty()

	m.syncMCPSetup()
	model, cmd := m.update(msg)
	if mm, ok := model.(Model); ok {
		mm.syncMCPSetup()
		// A non-selectable body owner stops an in-progress prompt drag without
		// clearing a completed prompt selection. Conversation selection remains
		// blocked and is cleared when its body owner changes.
		if !selectable(mm) {
			mm.prompt.StopMouseSelection()
			if mm.sel.active {
				mm = mm.clearSelection()
				mm.refreshView()
			}
		}
		// SINGLE relayout chokepoint: re-size the viewport from the CURRENT region
		// stack after every message, so the body height always matches the layout
		// regardless of WHAT changed — a resize, a transient (palette/mention/queue)
		// toggling on or off, or a header re-wrap on a model-id change. Without it only
		// onResize re-sized, so a transient appearing between resizes would push the
		// footer off-screen. relayout acts ONLY when the height actually changed, so the
		// per-message cost is one chrome render + a compare (and nothing more) on the
		// vast majority of messages — including stream deltas. Placed AFTER the
		// selection-clear so a height change re-splices the selection (styleSelection)
		// against the already-settled selection state in the same frame; relayout's own
		// refreshView (height-changed only) re-renders, so a frame is never rendered twice
		// (the clear path above already refreshed when it fired).
		mm.relayout()
		if m.statusLineSnapshot() != mm.statusLineSnapshot() {
			mm.submitStatusLine()
		}
		// Retire the kitty mascot on the empty→non-empty transition (the splash just
		// left). Reset kittyActive so a later /clear back to the zero-state re-transmits,
		// and batch the a=d delete out-of-band (tea.Raw) so the terminal frees the image.
		// A no-op when the half-block path was used (kittyActive is false).
		if wasKittyShowing && !mm.conv.isEmpty() {
			mm.kittyActive = false
			mm.kittyTier = 0
			cmd = tea.Batch(cmd, tea.Raw(welcome.DeleteMascot()))
		}
		model = mm
		// Test-only deterministic progress observer (nil in production). Fired on the
		// update goroutine after the message is reduced so teatest can sequence on the
		// reducer's actual phase, not on the CPU-starved output flush. See Deps.onPhase.
		if m.deps.onPhase != nil {
			m.deps.onPhase(mm.phase)
		}
	}
	return model, cmd
}

// onStreamMsg applies the generation guard for the stream fan-in, then re-dispatches
// the unwrapped message. A message produced by a run's reader (waitCmd) carries the
// generation of the channel it was read from; if that no longer matches the current
// run, the reader is bound to an ABANDONED channel (a reader left over after a
// queue-drain re-pointed streamCh, or any future double-arm), so the message is
// dropped and NOT re-armed — the stale reader dies with it. This neutralises both
// stale-teardown (a leftover StreamClosed/StreamErr cancelling the new run) and stale
// re-arm (a leftover event re-arming a reader on the new channel) structurally,
// regardless of how many readers leaked. Messages NOT from the stream reader
// (SessionReadyMsg, CommandsMsg, key/tick/paste, a send-error StreamErrMsg) are not
// wrapped and never reach here — they go straight to the switch in update.
func (m Model) onStreamMsg(sm streamMsg) (tea.Model, tea.Cmd) {
	if sm.gen != m.streamGen {
		return m, nil
	}
	return m.update(sm.msg)
}

func (m Model) blockClearPendingInput(msg tea.Msg) (tea.Model, tea.Cmd, bool) {
	if m.clearPending == nil {
		return m, nil, false
	}
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		// Clear blocks source-affecting input, but process-wide lifecycle
		// controls must remain available during a slow handoff.
		if key.Matches(msg, m.keys.Quit) || key.Matches(msg, m.keys.QuitD) || key.Matches(msg, m.keys.Suspend) {
			mm, cmd := m.onKey(msg)
			return mm, cmd, true
		}
		return m, nil, true
	case tea.PasteMsg, primaryReadMsg, tea.ClipboardMsg, tea.MouseClickMsg, tea.MouseReleaseMsg:
		// Clear owns the source handoff until its correlated response. Keep
		// rendering/draining old-stream events, but admit no prompt, approval,
		// duplicate command, paste, or click against the old source.
		return m, nil, true
	default:
		return m, nil, false
	}
}

// update is the body of the Elm reducer (see Update, which wraps it with the
// test-only onPhase observer).
func (m Model) update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if mm, cmd, handled := m.blockClearPendingInput(msg); handled {
		return mm, cmd
	}
	switch msg := msg.(type) {
	case streamMsg:
		return m.onStreamMsg(msg)

	case liveMsg:
		return m.updateLiveMsg(msg)

	case mcpAuthorizationPollTickMsg:
		return m.applyMCPAuthorizationPollTick(msg)

	case tea.WindowSizeMsg:
		return m.onResize(msg)

	case tea.ResumeMsg:
		return m.onResume(), nil

	case tea.ColorProfileMsg:
		return m.onColorProfile(msg), nil

	case tea.BackgroundColorMsg:
		return m.onBackgroundColor(msg), nil

	case tea.MouseWheelMsg, tea.MouseClickMsg, tea.MouseMotionMsg, tea.MouseReleaseMsg:
		return m.onMouseMsg(msg)

	case autoScrollMsg:
		return m.onAutoScroll()

	case tea.KeyboardEnhancementsMsg, tea.KeyReleaseMsg:
		return m.onKeyboardProtocolMsg(msg)

	case tea.KeyPressMsg:
		return m.onKey(msg)

	case tea.PasteMsg, primaryReadMsg, tea.ClipboardMsg:
		return m.onPasteMsg(msg)

	case spinner.TickMsg:
		if !m.spinnerVisible() {
			// Spinner not rendered in this phase: drop the tick WITHOUT calling
			// m.sp.Update (which always returns the next tick cmd). Returning nil
			// terminates the self-perpetuating chain; each transition into a
			// visible phase re-arms m.sp.Tick. (An idle 10fps tick chain drove a
			// full Update→View re-render forever: ~12% CPU, ~3.7MB/s alloc idle.)
			return m, nil
		}
		var cmd tea.Cmd
		m.sp, cmd = m.sp.Update(msg)
		return m, cmd

	case renderTickMsg:
		return m.onRenderTick()

	case statusLineChangedMsg:
		m.generatedStatusLine = msg.line
		return m, m.statusLineWaitCmd()

	case quitDisarmMsg, quitDDisarmMsg, clickDisarmMsg, doubleEscapeExpiryMsg:
		return m.onDisarmMsg(msg)

	case diagnosticsMsg:
		return m.handleDiagnostics(msg)

	default:
		return m.dispatchNonInputMsg(msg)
	}
}

// dispatchNonInputMsg is the default (non-key/non-mouse/non-tick) arm of
// update(): the surface-migrated overlay route first, then the lifecycle /
// MCP / skills / inventory / models fall-through chain, terminating at
// updateStreamEvent. Extracted so update() stays under the cyclomatic cap as
// overlays accrue (each helper returns handled=false for a non-matching msg, so
// at most one consumes).
func (m Model) dispatchNonInputMsg(msg tea.Msg) (tea.Model, tea.Cmd) {
	// The open modal surface routes every non-key/wheel Msg through m.modal
	// BEFORE the Model's generic reducer; handled=false falls through to the
	// rest of the chain.
	if mm, cmd, handled := m.dispatchSurfaceMsg(msg); handled {
		return mm, cmd
	}
	// /models owns list responses only while its surface is open. Once a ModelsMsg
	// falls through, Model is the owner and rejects only results older than its
	// last synchronized token; a different modal must not hide a current result.
	if result, ok := msg.(client.ModelsMsg); ok && result.RequestToken < m.modelCatalogRequestToken {
		return m, nil
	}
	// Lifecycle / transport msgs (session-ready, connect/stream error, stream
	// close, clipboard results, slash-command discovery).
	if mm, cmd, handled := m.updateLifecycle(msg); handled {
		return mm, cmd
	}
	// MCP overlay result/error msgs (Stage D).
	if mm, cmd, handled := m.updateMCPMsg(msg); handled {
		return mm, cmd
	}
	// Skills lifecycle-change receipt; fires no follow-up command. (The four
	// RPC-backed skills msgs are consumed by the surface's HandleMsg upstream;
	// only this Model-owned status receipt remains.)
	if mm, handled := m.updateSkillChangesMsg(msg); handled {
		return mm, nil
	}
	// Inventory-overlay result/error msgs (agentsInv, usermodel, /worktrees,
	// /schedule, /sessions) — grouped to keep update() flat as overlays accrue.
	if mm, cmd, handled := m.updateInventoryMsgs(msg); handled {
		return mm, cmd
	}
	// /models catalog results are either consumed by the open surface and applied
	// through its intent, or fall through here after close; persistence receipts stay root-owned.
	if mm, cmd, handled := m.updateModelsMsg(msg); handled {
		return mm, cmd
	}
	// /connect saved-target picker response (tombstone overlay); fires no follow-up command.
	if mm, handled := m.updateConnectMsg(msg); handled {
		return mm, nil
	}
	// Stream events (session.init / turn.start / deltas / tool.* /
	// permission.ask / hook / compaction / result).
	return m.updateStreamEvent(msg)
}

// updateInventoryMsgs is the fall-through chain for the unmigrated inventory
// overlays (agentsInv, userModel, reflections, dream, worktrees, schedule):
// each per-overlay helper returns handled=false for a non-matching msg, so at
// most one consumes. Most carry no follow-up command; /schedule's
// ScheduleActionMsg re-lists on success so the cmd is propagated. Surfaces
// migrated onto the modal no longer ride this chain — HandleMsg owns their
// routing.
func (m Model) updateInventoryMsgs(msg tea.Msg) (tea.Model, tea.Cmd, bool) {
	if mm, handled := m.updateAgentsInvMsg(msg); handled {
		return mm, nil, true
	}
	if mm, handled := m.updateUserModelMsg(msg); handled {
		return mm, nil, true
	}
	if mm, handled := m.updateReflectionsMsg(msg); handled {
		return mm, nil, true
	}
	if mm, handled := m.updateDreamMsg(msg); handled {
		return mm, nil, true
	}
	if mm, handled := m.updateWorktreesMsg(msg); handled {
		return mm, nil, true
	}
	if mm, cmd, handled := m.updateScheduleMsg(msg); handled {
		return mm, cmd, true
	}
	return m, nil, false
}

func (m Model) finishStartupResume() (tea.Model, tea.Cmd) {
	cmd := (&m).maybeKittyTransmit()
	if contextCmd := m.refreshStatusContextCmd(); contextCmd != nil {
		cmd = tea.Batch(cmd, contextCmd)
	}
	if liveCmd := (&m).armLiveFeed(); liveCmd != nil {
		cmd = tea.Batch(cmd, liveCmd)
	}
	if m.caps.WorkspaceEnrollment {
		m.enrollment = workspaceEnrollmentState{}
		m.workspaceEnrollmentNotice = "workspace services not connected — /tools-connect to enable protected tools"
		return m, cmd
	}
	if mm, submitCmd, ok := m.startInitialPrompt(); ok {
		return mm, tea.Batch(cmd, submitCmd)
	}
	return m, cmd
}

// applySessionReady binds an established session into the model: the
// SessionReadyMsg arm's body, extracted so the connectFallbackMsg arm (the
// server-rejected-selection fallback, issue #41) can reuse it before layering its
// warning on top.
func (m Model) applySessionReady(msg client.SessionReadyMsg) (tea.Model, tea.Cmd, bool) {
	// Only the process's initial create is known to be a fresh pre-prompt
	// session. Rebinds are conservatively ineligible; /clear sets its known-new
	// successor after this shared path returns.
	freshBinding := m.sessionID == "" && !m.restartedThisRun && !m.startupAdopted
	// A ready message establishes (or re-establishes) the authoritative session
	// binding. A recovery from any prior binding must never cross it.
	m.promptRecovery = nil
	m = m.bindSessionID(msg.SessionID)
	m.freshSessionBinding = freshBinding
	if freshBinding {
		m.sessionState = sessionStateIdle
	}
	m = m.syncDebugTarget()
	m.failedStepRetryTried = false
	m.browsingStartupSessions = false
	m.closeModal()
	m.statusContextRoot = ""
	m.caps = msg.Capabilities // stored for Phase B; unrendered this phase
	// The EFFECTIVE provider+model the server resolved this session to (echoed
	// verbatim). The header shows it from turn zero. The model is FIXED per session,
	// so this is set once here. An older server yields the zero value → no segment.
	(&m).setResolvedSessionModel(msg.ResolvedModel)
	if msg.Mode != "" {
		m.activeMode = client.ModeString(client.ModeFromString(msg.Mode))
	}
	m.restartFailed = false // a session is (re)established; any prior failure clears
	m.restartFailedForkID = ""
	m.phase = phaseIdle
	// A model switch arms a transient "switched to <model> — conversation kept" note
	// (chooseModel); surface it on the rebind instead of the bare "connected", then
	// clear the one-shot so a later connect never echoes a stale note.
	if note := m.pendingModelSwitchNote; note != "" {
		m.pendingModelSwitchNote = ""
		m.statusMsg = m.deps.Theme.Style("success").Render(note)
	} else {
		m.statusMsg = "connected"
	}
	if msg.Capabilities.WorkspaceEnrollment {
		m.enrollment = workspaceEnrollmentState{}
		m.workspaceEnrollmentNotice = "workspace services not connected — /tools-connect to enable protected tools"
	}
	// Now that we are idle + (still) empty, the welcome splash shows: transmit the
	// Kitty mascot if the terminal supports it (no-op otherwise). The WindowSizeMsg
	// path also fires this, but at connect the phase was still phaseConnecting when
	// that arrived, so fire it here on the idle transition too. Two-step form: the
	// pointer call mutates m (kittyActive/kittyTier), and mixing it with m as a
	// sibling return operand leaves the copy order UNSPECIFIED (see markDirty's
	// doc) — the cmd is taken first so the returned model carries the mutation.
	cmd := (&m).maybeKittyTransmit()
	if contextCmd := m.refreshStatusContextCmd(); contextCmd != nil {
		cmd = tea.Batch(cmd, contextCmd)
	}
	// Self-heal the terminal window title on the carryover/fork/adopt paths where
	// the server already set a title this client never saw: if we have NO local
	// title yet AND a session is bound, fire a GetSession refetch so
	// onResolvedModelMsg adopts the stored title. The footer-heal refetch already
	// carries the title on its msg; this reuses that channel rather than a second
	// RPC. Batched with the kitty transmit cmd so both run. (On the startup create
	// the title is always "" server-side too, so the refetch is a no-op for the
	// title — it still may raise the footer window denominator, which is the
	// existing footer-heal path's concern.)
	if m.sessionID != "" && m.deps.Session != nil {
		heal := client.RefreshResolvedModelCmd(m.deps.Ctx, m.deps.Session, m.sessionID)
		cmd = tea.Batch(cmd, heal)
	}
	// Arm the live subscription for the active session so fire-result
	// delivery notes render as delivery cards with no operator input.
	// Batched with the kitty/heal cmds so the live feed opens while the
	// session is loading.
	if liveCmd := (&m).armLiveFeed(); liveCmd != nil {
		cmd = tea.Batch(cmd, liveCmd)
	}
	// Seed the CLI prompt on the FIRST session bind only: set the textarea value,
	// clear the pending field, and submit via the identical typed-prompt path so
	// the behavior is byte-identical to the operator pressing enter. The pending
	// field is cleared BEFORE submitPrompt runs (defense-in-depth against re-fire
	// on a /models restart or the connect-fallback rebind, the two paths that
	// funnel back through here — /clear is NOT one: runClear resets the same
	// session via resetSession and never reaches this seam).
	// A "/"-prefixed seed (e.g. -p /clear) is dispatched by submitPrompt's
	// builtin dispatcher — documented behavior.
	// InitialPrompt remains queued until the complete frozen catalogue is admitted.
	if !msg.Capabilities.WorkspaceEnrollment {
		if mm, submitCmd, ok := m.startInitialPrompt(); ok {
			return mm, tea.Batch(cmd, submitCmd), true
		}
	}
	if m.deps.ConnectOpen {
		m.connect.err = m.deps.ConnectError
		m.connect.reason = m.deps.ConnectReason
		m.connect.failedTarget = m.deps.ConnectTarget
		m.connect.resumeSessionID = m.deps.ConnectResumeSessionID
		mm, connectCmd := m.openConnect()
		m = mm.(Model)
		cmd = tea.Batch(cmd, connectCmd)
	}
	return m, cmd, true
}

// updateSkillChangesMsg reduces a client.SkillChangesMsg — a bounded lifecycle
// change receipt — into the Model-owned status line (m.statusMsg from
// m.skillChangeLast, the newest receipt already announced). It is Model-side by
// design: it is a status receipt, NOT skills-surface state, and it must keep
// firing even when no /skills surface is open (the surface's HandleMsg passes it
// through handled=false). The four RPC-backed skills msgs (inventory, learned
// list, detail, diff) are consumed by skillsState.HandleMsg upstream and never
// reach this reducer.
func (m Model) updateSkillChangesMsg(msg tea.Msg) (tea.Model, bool) {
	changes, ok := msg.(client.SkillChangesMsg)
	if !ok {
		return m, false
	}
	if changes.Err != nil || len(changes.Changes) == 0 {
		return m, true
	}
	latest := changes.Changes[len(changes.Changes)-1]
	if latest.ID != m.skillChangeLast {
		m.skillChangeLast = latest.ID
		m.statusMsg = m.deps.Theme.Style("muted").Render(fmt.Sprintf("%d learned-skill change receipt(s) available — open /skills", len(changes.Changes)))
	}
	return m, true
}

// updateLifecycle reduces the transport/lifecycle msgs (session-ready, connect &
// stream errors, stream close, clipboard results, slash-command discovery). It is
// split out of update so the top-level dispatcher stays under the cyclomatic cap;
// handled=false means the msg is none of these and the caller continues its
// fall-through chain (MCP/skills/agents overlays → stream events).
//
//nolint:gocyclo // one flat lifecycle message classifier; splitting it would duplicate the handled contract.
func (m Model) updateLifecycle(msg tea.Msg) (tea.Model, tea.Cmd, bool) {
	switch msg := msg.(type) {
	case inventorySessionIDCopiedMsg:
		if msg.err != nil {
			m.statusMsg = m.deps.Theme.Style("warning").Render("could not copy session ID: " + sanitizeTerminal(msg.err.Error()))
			return m, nil, true
		}
		m.statusMsg = m.deps.Theme.Style("success").Render("copied exact session ID " + safeSessionID(msg.id))
		return m, tea.SetClipboard(msg.id), true
	case startupResumeReadyMsg:
		mm, cmd := m.finishStartupResume()
		return mm, cmd, true
	case reconnectMsg:
		// Live-feed reconnect loop msgs (issue #387): degraded-state markers and
		// the catch-up event msgs ride the reconnect channel. Handled here (a
		// lifecycle/transport msg), keeping the main dispatcher's branch count
		// under the cyclomatic cap.
		mm, cmd := m.updateReconnectMsg(msg)
		return mm, cmd, true
	case modelSwitchReadyMsg:
		// The target's authoritative transcript is already complete and correlated.
		// Only now may we discard the source projection or arm target interaction.
		if msg.token != m.modelSwitchRequestToken || m.phase != phaseConnecting || m.sessionID != msg.sourceID {
			return m, nil, true
		}
		m = m.resetSession()
		m.conv = conversationFromTranscript(msg.transcript.Messages)
		mm, cmd, handled := m.applySessionReady(msg.ready)
		m = mm.(Model)
		m.refreshView()
		// The source close is deliberately scheduled only after the target
		// projection and metadata are installed. Its best-effort result cannot
		// roll back the target.
		return m, tea.Batch(cmd, m.closeSessionCmd(msg.sourceID)), handled
	case modelSwitchFailedMsg:
		// The source was deliberately left bound and open. Restore only its live feed;
		// no reset or rebind is permitted on this path, so its transcript and metadata
		// remain exactly as they were before selection.
		if msg.token != m.modelSwitchRequestToken || m.phase != phaseConnecting || m.sessionID != msg.sourceID {
			return m, nil, true
		}
		m.phase = phaseIdle
		m.pendingModelSwitchNote = ""
		m.statusMsg = m.deps.Theme.Style("errorText").Render(
			"could not switch to " + sanitizeTerminal(msg.model) + ": " + sanitizeTerminal(msg.err.Error()))
		focusCmd := m.prompt.Focus()
		m.refreshView()
		return m, tea.Batch(focusCmd, (&m).armLiveFeed()), true
	case statusContextMsg:
		if msg.sessionID == m.sessionID {
			m.statusContextRoot = msg.root
			statusline.SetCommandCWD(m.deps.StatusSource, msg.root)
			m.submitStatusLine()
		}
		return m, nil, true
	case client.SessionReadyMsg:
		return m.applySessionReady(msg)
	case workspaceEnrollmentMsg:
		mm, cmd := m.applyWorkspaceEnrollment(msg)
		return mm, cmd, true
	case workspaceEnrollmentPresentationMsg:
		mm, cmd := m.applyWorkspaceEnrollmentPresentation(msg)
		return mm, cmd, true
	case workspaceEnrollmentPollTickMsg:
		mm, cmd := m.applyWorkspaceEnrollmentPollTick(msg)
		return mm, cmd, true
	case client.SessionCompactedMsg:
		if msg.RequestToken != m.compactRequestToken || msg.SessionID != m.sessionID || !m.compactPending {
			return m, nil, true
		}
		m.compactPending = false
		if msg.Err != nil {
			m.statusMsg = m.deps.Theme.Style("warning").Render("could not compact model history: " + sanitizeTerminal(msg.Err.Error()))
			return m, nil, true
		}
		if msg.Compacted {
			m.conv.addNotice("Model history compacted.")
		} else {
			m.conv.addNotice("Model history is already compact.")
		}
		m.statusMsg = m.deps.Theme.Style("muted").Render("ready")
		m.refreshView()
		return m, nil, true
	case worktreeSwitchReadyMsg:
		if m.sessionID != msg.oldID || m.phase != phaseConnecting {
			return m, nil, true
		}
		m = m.resetSession()
		m.activePlacement = msg.placement
		mm, bindCmd, handled := m.applySessionReady(msg.ready)
		m = mm.(Model)
		m.statusMsg = m.deps.Theme.Style("success").Render("worktree switched")
		m.refreshView()
		return m, tea.Batch(bindCmd, m.closeSessionCmd(msg.oldID)), handled
	case worktreeSwitchFailedMsg:
		if m.sessionID != msg.sourceID || m.phase != phaseConnecting {
			return m, nil, true
		}
		m.phase = phaseIdle
		m.statusMsg = m.deps.Theme.Style("errorText").Render("could not switch worktree: " + sanitizeTerminal(msg.err.Error()) + "; relist and try again")
		focusCmd := m.prompt.Focus()
		return m, focusCmd, true
	case clearSessionReadyMsg:
		if m.clearPending == nil || m.clearPending.sourceID != msg.oldID || m.clearPending.token != msg.token || m.sessionID != msg.oldID {
			return m, nil, true
		}
		// The server returned only after the exact source lifecycle settled. Invalidate
		// any locally queued old-stream messages before binding the successor.
		m.clearPending = nil
		if m.stream != nil || m.streamCh != nil || m.cancelRun != nil {
			m = m.endRun("")
		}
		m = m.resetSession()
		m.activePlacement = msg.placement
		mm, bindCmd, handled := m.applySessionReady(msg.ready)
		m = mm.(Model)
		m.freshSessionBinding = true
		m.sessionState = sessionStateIdle
		// The replacement was created with desiredMode, so its ready echo confirms
		// that any deferred old-session mode switch is now settled. Leaving it set
		// would make onIdleSubmit keep deferring every future prompt.
		m.pendingMode = ""
		m.statusMsg = m.deps.Theme.Style("success").Render("cleared")
		m.refreshView()
		return m, tea.Batch(bindCmd, m.closeSessionCmd(msg.oldID)), handled
	case clearSessionFailedMsg:
		if m.clearPending == nil || m.clearPending.sourceID != msg.sourceID || m.clearPending.token != msg.token || m.sessionID != msg.sourceID {
			return m, nil, true
		}
		failure := "could not clear: " + sanitizeTerminal(msg.err.Error())
		m.clearPending.failure = failure
		activeSource := m.clearPending.sourcePhase == phaseRunning || m.clearPending.sourcePhase == phaseAwaitingApproval
		if activeSource && !m.clearPending.sourceSettled {
			// Cancellation is irreversible once the server crosses the active-source
			// boundary. Keep this stream generation blocked until its delayed terminal
			// event settles it; restoring running/awaiting would admit prompts or verdicts
			// against a lifecycle the server is already cancelling.
			m.phase = phaseConnecting
			m.statusMsg = m.deps.Theme.Style("errorText").Render(failure + "; waiting for the source to settle before retry")
			return m, nil, true
		}
		m.clearPending = nil
		m.phase = phaseIdle
		m.statusMsg = m.deps.Theme.Style("errorText").Render(failure + "; retry /clear")
		return m, m.prompt.Focus(), true
	case connectFallbackMsg:
		// The connect-time create REJECTED the saved selection; the zero-selection
		// retry succeeded (createSessionCmd's fallback leg, issue #41). The session is
		// live on the SERVER DEFAULT: apply the ready payload as usual, then clear the
		// now-known-bad selection for THIS run (the next create must not re-send it)
		// and overwrite the "connected" status with a LOUD warning naming the rejected
		// model + the server's error — never a silent downgrade. The state file is NOT
		// rewritten (the pick may become valid again next launch).
		mm, cmd, handled := m.applySessionReady(msg.ready)
		m = mm.(Model)
		m.createModelSelection = client.ModelSelection{}
		m.modelCatalog.active = client.ModelSelection{}
		notice := "saved model " + sanitizeTerminal(modelSelLabel(msg.rejected)) +
			" was rejected by the server (" + sanitizeTerminal(msg.err.Error()) +
			") — using the server default"
		// Name the model the session actually fell back to, when known — mirroring
		// the key-removed reconcile notice. The fallback create's response already
		// carries it (applySessionReady set m.resolvedSessionModel from msg.ready).
		if id := m.resolvedSessionModel.ModelID; id != "" {
			notice += " — now running " + sanitizeTerminal(id)
		}
		m.statusMsg = m.deps.Theme.Style("warning").Render(notice)
		return m, cmd, handled
	case client.ConnectErrMsg:
		m = m.syncDebugTarget()
		if msg.AuthReason != "" {
			m.phase = phaseIdle
			m.connect.err = "Authentication needs attention."
			m.connect.reason = msg.AuthReason
			m.connect.failedTarget = m.deps.Server
			mm, cmd := m.openConnect()
			return mm, cmd, true
		}
		m.phase = phaseFatal
		m.fatalErr = msg.Err.Error()
		return m, nil, true
	case restartFailedMsg:
		// A /models restart-now re-create (or a /worktrees re-create, or an /effort
		// fork) failed. Unlike ConnectErrMsg this is NOT terminal: we deliberately
		// destroyed a working session (or attempted a fork), so leave the app
		// RECOVERABLE (idle, no session) with a loud status naming the failed model and
		// enter-to-retry armed (the selection still lives in m.createModelSelection). On the
		// /models + /worktrees paths the transcript is gone, but the app stays usable;
		// on the /effort path the source session (and transcript) SURVIVES — see
		// restartFailedForkID.
		m.phase = phaseIdle
		m = m.bindSessionID("")
		m.restartFailed = true
		// A carryover create's failure means enter-to-retry re-fires a FRESH
		// (non-carryover) create (see onIdleSubmit), so the note armed by
		// chooseModel for the ORIGINAL attempt would otherwise survive to falsely
		// claim "conversation kept" on the retry's SessionReadyMsg.
		m.pendingModelSwitchNote = ""
		// Record the retry origin: an /effort fork failure (msg.viaFork) re-forks from
		// the SURVIVING source session on retry — m.sessionID is "" by now, so the
		// source id must ride its own field. A non-fork failure clears it so a stale
		// origin from an earlier failed fork can't leak into a create retry.
		if msg.viaFork {
			m.restartFailedForkID = msg.sourceID
		} else {
			m.restartFailedForkID = ""
		}
		m.statusMsg = m.deps.Theme.Style("errorText").Render(
			"could not switch to " + sanitizeTerminal(msg.model) + ": " +
				sanitizeTerminal(msg.err.Error()) + " — press " + firstKey(m.keys.Submit, "enter") + " to retry")
		_ = m.prompt.Focus()
		m.refreshView()
		return m, nil, true
	case client.StreamErrMsg:
		if m.phase == phaseAuthorizing && m.authorization.authorizationID != "" && client.IsMCPAuthorizationPending(msg.Err) {
			// The parked authorization remains authoritative. The original Converse
			// stream is done, but its card and automatic polling remain active.
			m.stream = nil
			m.streamCh = nil
			m.streamGen++
			return m, nil, true
		}
		if m.clearPending != nil {
			// The old stream failed while Clear owns the handoff. Preserve the fact,
			// settle the reader, and suppress every recovery path that could reopen or
			// continue the source.
			if msg.Err != nil {
				m.conv.addError("stream error: " + msg.Err.Error())
			}
			m = m.endRun(stopError)
			m.failedStepRetryRun = false
			m.failedStepRetryAuthoritative = false
			m.clearPending.sourceSettled = true
			m.phase = phaseConnecting
			if settled, focusCmd, failed := m.settleFailedClearSource(); failed {
				settled.refreshView()
				return settled, tea.Batch(settled.refreshCmd(), focusCmd), true
			}
			return m, m.refreshCmd(), true
		}
		if msg.AuthReason != "" {
			mm, cmd := m.reduceLiveAuthRecovery(msg.AuthReason)
			return mm, cmd, true
		}
		if m.startupFirstPromptPending {
			m = m.failStartupRunEntry()
			return m, nil, true
		}
		if m.failedStepRetryRun && !m.failedStepRetryAuthoritative {
			// RetryStart transport/server rejection is non-destructive. The server is
			// authoritative, so preserve textarea, transcript, queue, and /retry access.
			m = m.endRun(stopError)
			m.failedStepRetryRun = false
			m.statusMsg = m.deps.Theme.Style("warning").Render("retry was not started: " + sanitizeTerminal(msg.Err.Error()) + " — resolve the condition and use /retry")
			if len(m.queued) > 0 {
				m.queuePaused = stopError
			}
			return m, tea.Batch(m.refreshCmd(), m.armLiveFeed()), true
		}
		// A transport error has no semantic commit fact. Restore only an
		// unmodified text-only draft; a server-confirmed enrollment rejection is
		// the one case eligible for a later exact-once replay.
		authoritative := isWorkspaceEnrollmentRejection(msg.Err.Error())
		m.recoverPrompt(authoritative)
		if authoritative && m.promptRecovery != nil {
			m.promptRecovery.autoReplay = true
		}
		m.conv.addError("stream error: " + friendlyWorkspaceEnrollmentRejection(msg.Err.Error()))
		m = m.endRun(stopError)
		liveCmd := m.armLiveFeed()
		mm, drainCmd := m.drainQueue(stopError)
		return mm, tea.Batch(m.refreshCmd(), drainCmd, liveCmd), true
	case clipboardResultMsg:
		mm, cmd := m.onClipboardResult(msg)
		return mm, cmd, true
	case sessionIDCopyResultMsg:
		return m.onSessionIDCopyResult(msg), nil, true
	case shellWriteResultMsg:
		// Best-effort shell-clipboard WRITE result: intentionally swallowed. OSC52
		// (tea.SetClipboard) is the primary copy path and the copy already reported
		// success via the status line, so a missing/failed shell backend must NOT
		// surface — handled here only so it doesn't fall through to a stream handler.
		return m, nil, true
	case clipboardErrMsg:
		return m.onClipboardErr(msg), nil, true
	case client.ModeChangedMsg:
		return m.onModeChanged(msg), nil, true
	case client.StreamClosedMsg:
		// Clear still owns the handoff, but the source stream can settle before the
		// RPC response arrives. Tear it down without draining queues, retrying steps,
		// reopening live delivery, or otherwise continuing the source.
		if m.clearPending != nil {
			m = m.endRun("closed")
			m.clearPending.sourceSettled = true
			m.phase = phaseConnecting
			if settled, focusCmd, failed := m.settleFailedClearSource(); failed {
				settled.refreshView()
				return settled, tea.Batch(settled.refreshCmd(), focusCmd), true
			}
			return m, m.refreshCmd(), true
		}
		// Clean close. If a run was still active (no terminal result seen),
		// finalise it; otherwise it's the expected post-result close (no-op).
		if m.phase == phaseRunning || m.phase == phaseAwaitingApproval {
			m.recoverPrompt(false)
			m = m.endRun("closed")
			modeCmd := m.retryPendingModeCmd()
			liveCmd := m.armLiveFeed()
			mm, drainCmd := m.drainQueue("closed")
			return mm, tea.Batch(m.refreshCmd(), modeCmd, drainCmd, liveCmd), true
		}
		if m.phase == phaseAuthorizing {
			// A parked run's Converse stream closes before its authorization
			// continuation is reattached. Invalidate that source so afterEvent cannot
			// re-arm its closed channel into the resumed run.
			m.stream = nil
			m.streamCh = nil
			m.streamGen++
		}
		return m, nil, true
	case client.SessionRenamedMsg:
		mm, cmd := m.onTitleRenamed(msg)
		return mm, cmd, true
	case client.SessionTitleMsg:
		return m.onSessionTitle(msg), nil, true
	case client.CommandsMsg:
		// Slash-command discovery landed: store the set (a failure degrades quietly
		// to an empty palette) and re-sync so the palette reflects it immediately if
		// the input is still a command line.
		m.palette.commands = msg.Commands
		mm, cmd := m.syncPalette()
		return mm, cmd, true
	case client.ResolvedModelMsg:
		return m.onResolvedModelMsg(msg)
	default:
		return m, nil, false
	}
}

// onResolvedModelMsg handles the ResolvedModelMsg from a GetSession refetch
// (footer context-meter heal, issue #66) and the plan-approval mode+model
// refresh (issue #206). Extracted from updateLifecycle to keep its cyclomatic
// complexity under the cap.
//
//nolint:unparam // tea.Cmd is always nil; the (Model, tea.Cmd, bool) shape matches the caller's switch.
func (m Model) onResolvedModelMsg(msg client.ResolvedModelMsg) (Model, tea.Cmd, bool) {
	// Heal invariants:
	//   - SESSION-CORRELATED: a refetch in flight when a /models switch rebinds
	//     the session to a new id must not land its STALE window on the new
	//     session, so a msg whose SessionID no longer matches the current one
	//     is dropped.
	//   - benign on error: a failed refetch keeps the current denominator (the
	//     next turn boundary retries while the window is still unknown).
	// The plan-approval path (issue #206): after a plan_approved terminal, the
	// server has flipped the mode and switched the session to the execute model.
	// The refetch carries the new Mode + the new ResolvedModel; the reducer
	// applies both so the header reflects the flipped state before the execution
	// run starts.
	if msg.Err != nil || msg.SessionID != m.sessionID {
		return m, nil, true
	}
	// A present SessionCapabilities message is authoritative even when both media
	// values are false. If it is the only populated wire capability, overlay just
	// media so an older server's absent global feature snapshot does not erase the
	// already-known global bits.
	incomingCaps := msg.Capabilities
	mediaOnly := incomingCaps.SessionMediaPresent
	incomingCaps.SessionMediaPresent = false
	incomingCaps.Image = false
	incomingCaps.Audio = false
	if mediaOnly && incomingCaps == (client.Capabilities{}) {
		m.caps.Image = msg.Capabilities.Image
		m.caps.Audio = msg.Capabilities.Audio
		m.caps.SessionMediaPresent = true
	} else if msg.Capabilities != (client.Capabilities{}) {
		m.caps = msg.Capabilities
	}
	// The snapshot is authoritative after reconnect/session adoption, so unlike the
	// old first-prompt seed it may replace a local title.
	m, _ = m.adoptTitle(msg.Title, msg.TitleProvenance, msg.TitleRevision)
	// Mode update: apply when the refetch carries a mode (the plan-approval
	// refresh path). On the footer-heal path Mode is the same as m.activeMode
	// (or empty from an older server), so this is a benign no-op.
	if msg.Mode != "" {
		m.activeMode = client.ModeString(client.ModeFromString(msg.Mode))
	}
	if msg.State != "" {
		m.sessionState = msg.State
	}
	m.sessionCreatedAt = msg.CreatedAt
	m.activePlacement = msg.Placement
	if (&m).setResolvedSessionModel(msg.Resolved) {
		m.refreshView()
	}
	return m, nil, true
}

// onRenderTick is the frame-cadence flush of coalesced deltas. The one-shot tick
// has fired: disarm, flush if a delta dirtied the view (refreshView clears
// viewDirty), and re-arm a fresh one-shot only if more deltas are still pending AND
// a run is streaming — so the ticker idles to zero when the stream goes quiet and
// self-terminates at run end. No flush depends on this tick (every boundary
// force-flushes), so a dropped/late tick can never lose the tail.
func (m Model) onRenderTick() (tea.Model, tea.Cmd) {
	m.tickArmed = false
	if m.viewDirty {
		m.refreshView()
	}
	// Re-arm the tick while the view is still dirty AND a streaming phase is
	// active: phaseRunning (the live delta path) OR phaseReplay (the child-
	// inspection replay path — its per-event arm coalesces via markDirtyReplay,
	// so it needs the same frame-cadence flush a live turn does).
	if m.viewDirty && (m.phase == phaseRunning || m.phase == phaseReplay) {
		m.tickArmed = true
		return m, m.renderTickCmd()
	}
	return m, nil
}

func mcpAuthorizationNotice(msg client.MCPAuthorizationMsg) string {
	displayName := oneLine(sanitizeTerminal(msg.DisplayName))
	target := ""
	if displayName != "" {
		target = " for " + displayName
	}
	if msg.Status == mcpAuthorizationStatusPending {
		return "MCP authorization required" + target + ". Open Browser or Copy Link to start automatic checking, or Cancel."
	}
	return fmt.Sprintf("MCP authorization%s: %s.", target, oneLine(sanitizeTerminal(msg.Status)))
}

// updateStreamEvent reduces the per-event stream msgs into the conversation. It
// is the back half of Update, split out so the cyclomatic complexity of each
// stays manageable. Unknown msgs are a no-op.
func (m Model) updateStreamEvent(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case client.SessionInitMsg:
		if m.startupFirstPromptPending {
			m.startupFirstPromptPending = false
			m.startupAdopted = false
			m.startupRetryPrompt = ""
		}
		return m, m.waitCmd()
	case client.TurnStartMsg:
		m.beginTurnEvent()
		return m.afterEvent()
	case client.AssistantDeltaMsg:
		// Append only; markDirty arms a one-shot frame-cadence flush. No per-token
		// refreshView (that re-runs glamour on the growing live block every token —
		// O(n²) over a turn). The next turn/tool/result boundary force-flushes too.
		m.conv.appendAssistant(msg.Text)
		return m.markDirty()
	case client.ReasoningDeltaMsg:
		// Same coalescing as the assistant delta: append, markDirty, no refreshView.
		m.conv.appendReasoning(msg.Text)
		return m.markDirty()
	case client.TurnEndMsg:
		// The turn's model exchange is done: freeze any live "reasoning…"
		// affordance, then append a muted stat line unless the turn was trivial.
		// The turn's prompt size is the CURRENT context occupancy (latest turn,
		// assigned not summed — mirroring the team lane meter's turn.end handling).
		// Keep it STICKY: a turn that reports zero input tokens (a stalled/usage-less
		// turn) must not erase a known occupancy (mirroring the sticky window at
		// conversation.go endReasoningStream/turn.end and ContextWindow above). The
		// engine's DISPLAY-ONLY zero-usage fallback (issue #82) fills turn_end with a
		// conversation-size estimate so this is normally non-zero already (the estimate
		// feeds only this meter, never the cumulative ↑/↓/⊕ totals or any token budget,
		// which stay on provider truth) — this guard is belt-and-braces for the edge
		// where even the estimate is zero.
		if msg.Usage.InputTokens > 0 {
			m.contextTokens = msg.Usage.InputTokens
		}
		m.conv.endReasoningStream()
		if !trivialTurn(msg) {
			m.conv.addTurnStat(turnStatLine(msg))
		}
		mm, cmd := m.afterEvent()
		// Footer context-meter self-heal (issue #66): if the meter's denominator is
		// still UNKNOWN (m.resolvedSessionModel.ContextWindow == 0), refetch the session's
		// resolved model. For a session on a LIVE-ONLY model the create-time echo carries
		// a DELIBERATE PROVISIONAL 0 — the server's echo resolver (echoWindowResolver)
		// reports 0 (not an accidental 128k floor) while the one-shot live model-list
		// refresh is still in flight, honestly signalling "live window not in yet". Once
		// the refresh settles the server's ResolvedModel resolves the real live window
		// (or floors an uncatalogued model to 128k), and the ResolvedModelMsg arm raises
		// the denominator. The gate is correct-by-construction: it keys ONLY off ==0, so
		// a genuinely-known window (catalogued or live) never triggers it, and it is
		// bounded by refresh completion — the server stops emitting 0 once settled, so
		// the RPC fires at most until the first heal lands, never on every turn forever.
		// The client never overrides this value. Embedded mode can configure the
		// server-side resolver with --context-window-override; the gate remains purely
		// "session live AND window still unknown".
		if m.sessionID != "" && m.resolvedSessionModel.ContextWindow == 0 {
			refresh := client.RefreshResolvedModelCmd(m.deps.Ctx, m.deps.Session, m.sessionID)
			return mm, tea.Batch(cmd, refresh)
		}
		return mm, cmd
	case client.ToolCallMsg:
		m.conv.addTool(msg.ID, msg.Name, msg.Args)
		m.activeTool = msg.Name
		m.toolProgress = ""
		// Accumulate the file path for the session changed-files summary. Tracked
		// at call time (not on the result) so the header reflects intent the moment
		// the mutation is announced; mutatedPath gates to Edit/Write + dedupes.
		if p, ok := mutatedPath(msg.Name, msg.Args); ok {
			m.conv.recordFileChange(p)
		}
		return m.afterEvent()
	case client.ToolResultMsg:
		if !m.conv.resolveTool(msg.CallID, msg.Content, msg.IsError, msg.Blocks...) {
			m.conv.addNotice("orphan tool result for " + msg.CallID)
		}
		m.activeTool = ""
		m.toolProgress = ""
		return m.afterEvent()
	case client.ToolProgressMsg:
		// Transient advisory line from a long-running tool: show it beside the
		// spinner while the tool runs. It is cleared on the next tool.result or
		// turn boundary; it never enters the transcript.
		m.toolProgress = msg.Text
		return m.afterEvent()
	case client.PermissionAskMsg:
		return m.applyPermissionAsk(msg)
	case client.HookMsg:
		m.conv.addHook(msg.Text, msg.Phase, msg.Tool, string(msg.Decision))
		return m.afterEvent()
	case client.DeliveryNoteMsg:
		return m.applyDeliveryNote(msg)
	case client.ResultMsg:
		return m.applyResult(msg)
	default:
		// The delegation projections (subagent/team/parallel), the transient notices,
		// and the permission retraction are reduced by updateStreamSecondary (a second
		// switch) to keep this dispatcher under the cyclomatic-complexity bound — the
		// same split EventToMsg/delegationEventToMsg carries. Together the two switches
		// remain total over the stream msg taxonomy; a new delegation/notice msg must
		// be added there, not here. Unknown msgs are a no-op.
		return m.updateStreamSecondary(msg)
	}
}

// applyDeliveryNote reduces a DeliveryNoteMsg, extracted from updateStreamEvent
// to keep that dispatcher under the cyclomatic-complexity bound. It renders a
// fire-result delivery note as a distinct delivery card, deduped by FireID so a
// note that arrives BOTH via the durable catch-up (reconnect) AND the reopened
// live feed renders exactly once (issue #387). A note with an empty FireID
// (malformed) is NOT deduped (renders once per arrival — the rare malformed
// case; the fenced discriminator keeps an ordinary user prompt out of this
// arm). This is the SINGLE dedup site: BOTH the live path (updateLiveMsg →
// updateStreamEvent) AND the catch-up path (updateReconnectMsg →
// updateStreamEvent) funnel through here.
func (m Model) applyDeliveryNote(msg client.DeliveryNoteMsg) (tea.Model, tea.Cmd) {
	if msg.FireID != "" {
		if m.seenFireIDs == nil {
			m.seenFireIDs = make(map[string]struct{}, 1)
		}
		if _, dup := m.seenFireIDs[msg.FireID]; dup {
			return m.afterEvent()
		}
		m.seenFireIDs[msg.FireID] = struct{}{}
	}
	m.conv.addDelivery(msg.ScheduleName, msg.FireID, msg.Text)
	return m.afterEvent()
}

func (m *Model) beginTurnEvent() {
	if m.failedStepRetryRun {
		m.failedStepRetryAuthoritative = true
	}
	m.conv.startAssistant()
	m.activeTool = ""
	m.toolProgress = ""
	// Routing metadata is per turn. OpenRouter omits it on cache hits, so the
	// previous turn's downstream must not survive into a turn that reports none.
	m.providerRoute = ""
}

// updateStreamSecondary is the back half of updateStreamEvent: the delegation
// projections, the transient notices, and the permission retraction. Split out
// only so neither dispatcher grows past the cyclomatic-complexity bound.
func (m Model) updateStreamSecondary(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case client.MCPAuthorizationMsg:
		return m.applyMCPAuthorization(msg)
	case client.PermissionRetractMsg:
		// An active approval surface consumes retractions through HandleMsg. A
		// stale retraction after close is transport-only and needs no approval state.
		return m.afterEvent()
	case client.SubagentMsg:
		m.applySubagent(msg)
		return m.afterEvent()
	case client.TeamMsg:
		m.applyTeam(msg)
		return m.afterEvent()
	case client.ParallelMsg:
		m.applyParallel(msg)
		return m.afterEvent()
	case client.ModelRetryMsg:
		m.failedStepRetryRun = true
		m.failedStepRetryAuthoritative = false
		m.conv.addNotice(noticeLine(msg))
		m.statusMsg = m.deps.Theme.Style("muted").Render("retrying failed model step…")
		return m.afterEvent()
	case client.CompactionMsg:
		// A compaction boundary is a DURABLE fact worth keeping in the transcript, so it
		// stays a scrollback notice.
		m.conv.addNotice(noticeLine(msg))
		return m.afterEvent()
	case client.NoProgressMsg:
		// No-progress (advisory nudge OR terminal give-up) is TRANSIENT: a successful
		// nudge-recover must leave NO permanent scrollback residue. The terminal stop is
		// conveyed durably and independently by the ResultMsg → footer "stopped · no
		// progress", so routing every no-progress notice to the transient footer status
		// loses nothing terminal while keeping the advisory ephemeral. (It surfaces only
		// at idle — during a run the spinner owns the footer-left and already shows
		// liveness; that is the intended, non-intrusive behaviour.)
		m.statusMsg = m.deps.Theme.Style("muted").Render(noticeLine(msg))
		return m.afterEvent()
	case client.ProviderRouteMsg:
		// The routed downstream provider (issue #480) is shown persistently beside
		// the model id for the current turn and transiently in the footer when the
		// metadata arrives. TurnStartMsg clears the header first, so a cache hit or
		// any turn with missing metadata cannot retain a stale route.
		m.providerRoute = msg.Text
		m.statusMsg = m.deps.Theme.Style("muted").Render("via " + msg.Text)
		return m.afterEvent()
	case client.RecoverNoticeMsg:
		// Recover-notice (permanent-failure advisory) is a DURABLE scrollback
		// block, not a transient statusMsg: the run's first event would overwrite
		// a transient footer before the user reads it, and the advisory is
		// actionable (start a new session / change the request) so it must
		// persist. It renders as a warning-styled ⚠ block (see renderBlock's
		// blockNotice recover branch). It carries only harness-authored advisory
		// Text (no model content).
		m.conv.addRecoverNotice(msg.Text)
		return m.afterEvent()
	case client.SteerOutcomeMsg:
		return m.applySteerOutcome(msg)
	case client.SteerEchoMsg:
		return m.applySteerEcho(msg)
	default:
		return m, nil
	}
}

// mintSteerID derives the next session-scoped steer message id (1-based
// "steer-%04d"). Session-scoped needs no crypto — within one session the
// client is the sole sender on its single stream, and the server only ever
// echoes them verbatim; a per-process shared counter would not survive the
// module boundary anyway.
func (m *Model) mintSteerID() string {
	m.steerSeq++
	return fmt.Sprintf("steer-%04d", m.steerSeq)
}

// applySteerOutcome reduces the server's AUTHORITATIVE ack for a steer /
// steer_cancel frame into the steer lifecycle, keyed BY message_id (never text —
// a stale ack for a drained id or one bundle's ack during a recomposed bundle's
// flight is ignored). The phases advance ONLY here (and in applySteerEcho),
// never on send — the client cannot observe the drain moment across stream
// latency, so it renders what the server reports:
//
//   - accepted → steerSent (parked in the run's inbox, awaiting the boundary
//     drain); Text adopts the acked text (the version the engine last confirmed).
//   - appended → merged into the pending bundle server-side (it drains as ONE
//     bundle). The queue display stays derived from Sends (the ack's lone
//     fragment must NOT overwrite the merged join).
//   - too_late + promoted → steerPromoted (the run had gone terminal; the text
//     auto-started a fresh follow-up run). The card states it honestly.
//   - retracted → steerRetracted (the cancel won; the run drains nothing).
//   - none_pending → the cancel found an empty slot (the drain already won, or
//     nothing was pending); the steer state clears (there is nothing to show).
//
// An ack for a frame the ui no longer tracks (steer == nil, a burned id, or an
// id that is not the live bundle's — e.g. a late ack after the echo committed,
// or one outstanding bundle's ack racing a recomposed replacement) is
// idempotently dropped.
// steerTrace emits a one-line correlation trace into the status bar when
// Deps.Debug or Deps.DebugSteer is set: the ack/echo kind, the
// incoming message_id, the live bundle's id, and the decision. It makes a stuck
// or mis-correlated steer lifecycle visible in the TUI rather than opaque. A nil
// live bundle renders "-".
func (m Model) steerTrace(kind, inID, decision string) string {
	if !m.deps.Debug && !m.deps.DebugSteer {
		return ""
	}
	live := "-"
	if m.steer != nil {
		live = orDash(m.steer.watermarkID())
	}
	return m.deps.Theme.Style("muted").Render("[steer] " + kind + " id=" + orDash(inID) + " live=" + live + " → " + decision)
}

// steerSendIndex returns the index of the send with id in the ordered queue, or
// -1 when absent (a watermark split drops sends[0:idx+1]).
func steerSendIndex(sends []steerQueuedSend, id string) int {
	return slices.IndexFunc(sends, func(s steerQueuedSend) bool { return s.ID == id })
}

// joinSteerSends re-joins the ordered queue's fragment texts with the blank-line
// separator (the merged display text shown on the card).
func joinSteerSends(sends []steerQueuedSend) string {
	parts := make([]string, 0, len(sends))
	for _, s := range sends {
		parts = append(parts, s.Text)
	}
	return strings.Join(parts, queueMergeSep)
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func (m *Model) restoreSteerSend(send steerQueuedSend) {
	draft := send.Draft
	if current := m.prompt.Value(); strings.TrimSpace(current) != "" {
		draft = current + queueMergeSep + draft
	}
	m.prompt.Rewrite(draft)
	if len(send.Staged) > 0 {
		if m.stagedMedia == nil {
			m.stagedMedia = make(map[string]stagedAttachment)
		}
		maps.Copy(m.stagedMedia, send.Staged)
	}
	if len(send.Pastes) > 0 {
		if m.stagedPastes == nil {
			m.stagedPastes = make(map[string]string)
		}
		maps.Copy(m.stagedPastes, send.Pastes)
	}
}

func (m Model) applySteerOutcome(msg client.SteerOutcomeMsg) (tea.Model, tea.Cmd) {
	if m.steer == nil {
		// A stale ack (drained id, or an id-less legacy ack with nothing live)
		// reconciles to nothing — there is no card to advance.
		if tr := m.steerTrace("ack:"+string(msg.Outcome), msg.MessageID, "dropped (no live bundle)"); tr != "" {
			m.statusMsg = tr
		}
		return m.afterEvent()
	}
	// Correlate BY QUEUED id: acks scope a queued send (an EMPTY id is legacy
	// and falls through). A drained id leaves the queue at the echo split, so
	// "not in queue" alone marks it stale — no burn map needed.
	if msg.MessageID != "" && steerSendIndex(m.steer.Sends, msg.MessageID) < 0 {
		if tr := m.steerTrace("ack:"+string(msg.Outcome), msg.MessageID, "ignored (id not in queue)"); tr != "" {
			m.statusMsg = tr
		}
		return m.afterEvent()
	}
	switch msg.Outcome {
	case client.SteerAccepted:
		m.steer.Phase = steerSent
		m.statusMsg = m.deps.Theme.Style("muted").Render("steer queued")
	case client.SteerAppended:
		// The engine merged the new line into the outstanding pending bundle (append
		// is the default): the bundle's Text grew, and it still drains as ONE. The
		// card honestly reports "merged onto the in-flight steer" — the merged queue
		// display stays derived from Sends (the ack's lone fragment must NOT
		// overwrite it).
		m.steer.Phase = steerSent
		m.statusMsg = m.deps.Theme.Style("muted").Render("line merged onto the in-flight steer (↑ to edit)")
	case client.SteerTooLate:
		if msg.Promoted {
			m.steer.Phase = steerPromoted
			m.statusMsg = m.deps.Theme.Style("muted").Render("steer arrived after the run ended — sent as a follow-up")
		} else {
			// This exact frame was not delivered. Restore only the correlated send;
			// earlier accepted sends remain live until their drain echo, and unrelated
			// later ids retain their own authoritative outcomes.
			idx := steerSendIndex(m.steer.Sends, msg.MessageID)
			if msg.MessageID == "" {
				idx = len(m.steer.Sends) - 1
			}
			if idx >= 0 {
				failed := m.steer.Sends[idx]
				m.steer.Sends = append(m.steer.Sends[:idx], m.steer.Sends[idx+1:]...)
				m.restoreSteerSend(failed)
			}
			if len(m.steer.Sends) == 0 {
				m.steer = nil
			} else {
				m.steer.Text = joinSteerSends(m.steer.Sends)
			}
			m.statusMsg = m.deps.Theme.Style("warning").Render("steer not sent — restored for editing (try again)")
		}
	case client.SteerRetracted:
		m.steer.Phase = steerRetracted
		for i := range m.steer.Sends {
			m.steer.Sends[i].Media = client.MediaResult{}
			m.steer.Sends[i].Staged = nil
			m.steer.Sends[i].Pastes = nil
		}
		m.statusMsg = m.deps.Theme.Style("muted").Render("steer retracted")
	case client.SteerNonePending:
		m.steer = nil
		m.statusMsg = m.deps.Theme.Style("muted").Render("no pending steer to retract")
	}
	return m.afterEvent()
}

// applySteerEcho reduces the run's steer-inbox DRAIN echo (the COMMITTED text
// recorded into history). It is the authoritative "the steer landed" signal,
// keyed BY message_id: the in-flight card clears (the committed text now lives
// in the transcript) and the landed line renders IN CONTEXT at the echo's stream
// position (after the settling tool activity, before the next turn-start). A
// duplicate echo for an already-burned id is ignored, so the landed line never
// double-renders.
func (m Model) applySteerEcho(msg client.SteerEchoMsg) (tea.Model, tea.Cmd) {
	// Watermark split: the echo's message_id is the TAIL of the drained bundle —
	// drop every send in this bundle's queue up to and including it (drained),
	// and keep the suffix (still pending). The queue is the ONLY correlation
	// source: a duplicate or stale echo finds its id already out of the queue
	// and is a no-op (no burn map). For an id-less legacy echo, clear the whole
	// bundle. The landed line renders ONLY when the echo matched live state —
	// a stale/duplicate echo never double-renders the transcript.
	// The echo is authoritative ONLY while a live bundle can correlate it — a
	// late/duplicate echo with no live bundle drops to the no-render arm (never
	// re-renders the transcript line; the one drain = one echo invariant makes
	// the duplicate a server non-occurrence).
	landed := false
	if m.steer != nil {
		if msg.MessageID == "" {
			// id-less legacy echo: clear the whole bundle.
			m.steer = nil
			landed = true
		} else {
			idx := steerSendIndex(m.steer.Sends, msg.MessageID)
			switch {
			case idx >= 0:
				rest := m.steer.Sends[idx+1:]
				if len(rest) == 0 {
					m.steer = nil // the whole bundle drained
				} else {
					m.steer.Sends = rest
					m.steer.Text = joinSteerSends(rest)
				}
				landed = true
				if tr := m.steerTrace("echo", msg.MessageID, "split queue at watermark; tail pending"); tr != "" {
					m.statusMsg = tr
				}
			case len(m.steer.Sends) == 0:
				// An ack already emptied the queue client-side (the phase
				// advanced); the echo is the drain confirming it — clear.
				m.steer = nil
				landed = true
			default:
				if tr := m.steerTrace("echo", msg.MessageID, "no matching live bundle (card left)"); tr != "" {
					m.statusMsg = tr
				}
			}
		}
	}
	if landed {
		// Render the landed line IN CONTEXT (its true stream position — the echo
		// arrives exactly where the drain committed the user continuation).
		if media := mediaDescriptors(msg.Parts); len(media) > 0 {
			m.conv.addUserWithMedia(msg.Text, media)
		} else {
			m.conv.addUser(msg.Text)
		}
		m.statusMsg = m.deps.Theme.Style("muted").Render("steer applied")
	}
	return m.afterEvent()
}

func (m Model) settleFailedClearSource() (Model, tea.Cmd, bool) {
	if m.clearPending == nil || m.clearPending.failure == "" {
		return m, nil, false
	}
	failure := m.clearPending.failure
	m.clearPending = nil
	m.phase = phaseIdle
	m.statusMsg = m.deps.Theme.Style("errorText").Render(failure + "; retry /clear")
	return m, m.prompt.Focus(), true
}

// applyResult handles a terminal ResultMsg: it folds the run's usage into the running
// totals, surfaces a terminal error, ends the run, and drains any queued prompts.
// Extracted from updateStreamEvent's switch to keep that dispatcher flat.
func (m Model) applyResult(msg client.ResultMsg) (tea.Model, tea.Cmd) {
	// A terminal result is a server-side run fact, so this prompt cannot be replayed
	// by a later unrelated control response.
	m.promptRecovery = nil
	// ResultMsg.Usage is the run's CUMULATIVE total; fold it into the session
	// total exactly once here. The per-turn TurnEndMsg feeds only the
	// context-occupancy meter (m.contextTokens), never m.usage — adding both
	// would double-count.
	m.usage = sumUsage(m.usage, msg.Usage)
	if msg.Stop == stopError && msg.Error != "" {
		rejection := friendlyWorkspaceEnrollmentRejection(msg.Error)
		if msg.Permanent {
			m.conv.addPermanentError(rejection)
		} else {
			m.conv.addError(rejection)
		}
	}
	if m.clearPending != nil {
		// The terminal facts still belong in the source projection, but Clear owns
		// what happens next. Settle only: no queued prompt, failed-step retry,
		// pending-mode retry, plan continuation, live-feed rearm, or other source run.
		m = m.endRun(msg.Stop)
		m.failedStepRetryRun = false
		m.failedStepRetryAuthoritative = false
		m.clearPending.sourceSettled = true
		m.phase = phaseConnecting
		if settled, focusCmd, failed := m.settleFailedClearSource(); failed {
			settled.refreshView()
			return settled, tea.Batch(settled.refreshCmd(), focusCmd)
		}
		return m, m.refreshCmd()
	}
	retryDeferred := m.failedStepRetryRun && !m.failedStepRetryAuthoritative
	m = m.endRun(msg.Stop)
	m.failedStepRetryRun = false
	m.failedStepRetryAuthoritative = false
	if retryDeferred {
		if len(m.queued) > 0 {
			m.queuePaused = "retry_pending"
		}
		m.statusMsg = m.deps.Theme.Style("warning").Render("retry stopped before the model was called — adjust configuration and use /retry")
		return m, tea.Batch(m.refreshCmd(), m.retryPendingModeCmd(), m.armLiveFeed())
	}
	if msg.FailedStepRetryEligible() && !m.failedStepRetryTried {
		m.failedStepRetryTried = true
		rm, retryCmd := m.startFailedStepRetry()
		return rm, tea.Batch(m.refreshCmd(), retryCmd, m.armLiveFeed())
	}
	if msg.Stop == stopError && msg.RetryDispositionPresent && msg.RetryDisposition == client.RetryDispositionRetryable {
		m.statusMsg = m.deps.Theme.Style("warning").Render("model step failed — use /retry to retry without duplicating the prompt")
	}
	modeCmd := m.retryPendingModeCmd()
	// Interactive plan-approval continuation (issue #206). A plan_approved
	// terminal means the operator APPROVED the plan over the Converse
	// ResumeApproval frame (resolveAsk → SendApproval). Unlike the ApprovePlan
	// RPC (service.go:2593) and the headless auto-approve continuation
	// (service.go:2945), the interactive ResumeApproval path does NOT start a
	// continuation run — so the TUI fires the proceed prompt HERE to start the
	// execution run (StartRunContent reopens the StopPlanApproved-completed
	// session; the CASE-1 mode→model rebuild picks up the flipped mode → execute
	// model). See submitProceedPrompt for why this fires post-terminal (on
	// ResultMsg), never immediately after SendApproval. A deny ends
	// StopPlanIterate (not plan_approved), so the proceed is gated off the
	// iterate path; a non-plan run never emits plan_approved. A staged queue, if
	// any, drains after the execution run (drainQueue below no-ops while the
	// proceed has set phase==running).
	if msg.Stop == "plan_approved" {
		pm, proceedCmd := m.submitProceedPrompt()
		// Refetch the session snapshot so the header reflects the server's
		// flipped mode (plan→default/acceptEdits) AND the execute model.
		// The result lands as a ResolvedModelMsg on the update goroutine
		// while the execution run is in progress; the ResolvedModelMsg arm
		// applies both mode + model from the session snapshot (issue #206).
		refresh := client.RefreshResolvedModelCmd(m.deps.Ctx, m.deps.Session, m.sessionID)
		dm, drainCmd := pm.drainQueue(msg.Stop)
		return dm, tea.Batch(pm.refreshCmd(), modeCmd, proceedCmd, drainCmd, refresh)
	}
	mm, drainCmd := m.drainQueue(msg.Stop)
	var skillChanges tea.Cmd
	if lifecycle, ok := m.deps.Skills.(client.LearnedSkillClient); ok {
		skillChanges = client.ListSkillChangesCmd(m.deps.Ctx, lifecycle, m.deps.Workspace)
	}
	return mm, tea.Batch(m.refreshCmd(), modeCmd, drainCmd, m.armLiveFeed(), skillChanges)
}

// noticeLine renders the muted-notice text for a transient advisory message
// (compaction or no-progress). Both carry only harness-authored Text and render as a
// single muted line; this keeps updateStreamEvent's switch flat.
func noticeLine(msg tea.Msg) string {
	switch m := msg.(type) {
	case client.ModelRetryMsg:
		return "model retry" + suffix(m.Text)
	case client.CompactionMsg:
		return "history compacted" + suffix(m.Text)
	case client.NoProgressMsg:
		return "no progress" + suffix(m.Text)
	default:
		return ""
	}
}

// applySubagent attributes a BOUNDED subagent projection to its Subagent tool card by
// ParentCallID (id match, like resolveTool). A miss is silently dropped: a lost
// subagent event only costs the trace, never correctness or isolation.
func (m *Model) applySubagent(msg client.SubagentMsg) {
	applySubagentTo(&m.conv, msg)
	// A BACKGROUND child finishing is otherwise invisible (its Subagent card
	// resolved long ago with the started-result), so surface a brief transient
	// footer notice — the same advisory channel as team-done / no-progress, never
	// a durable scrollback line. The result body goes to the AGENT (via
	// SubagentStatus), not to this client; the copy says exactly that.
	if msg.Kind == client.SubagentEnd {
		if ln := findFleetLane(m.conv.subagentFleet, msg.ChildID); ln != nil && ln.background {
			m.statusMsg = m.deps.Theme.Style("muted").Render(
				"background subagent #" + shortChildID(msg.ChildID) + " done — result ready for the agent")
		}
	}
}

// applySubagentTo is the pure conversation-projection half of applySubagent: it
// routes a BOUNDED subagent projection into the inline Subagent card (keyed by
// ParentCallID) AND the flat fleet collection (keyed by ChildID) on c. The previews
// are bounded/scrubbed/client-only — neither surface feeds the parent conversation
// (gauntlet #7). The live applySubagent delegates here and layers the transient
// footer status on top; the replay path (applyReplayEvent) calls this directly so
// the read-only transcript gets the SAME projection without any live-run footer
// side-effect.
func applySubagentTo(c *conversation, msg client.SubagentMsg) {
	switch msg.Kind {
	case client.SubagentStart:
		c.setSubagentStart(msg.ParentCallID, msg.Goal, msg.RoutedCategory, msg.RoutedModel, msg.RoutingReason, msg.Model)
		c.fleetStart(msg.ChildID, msg.Goal, msg.RoutedCategory, msg.RoutedModel, msg.RoutingReason, msg.Model, msg.Background)
	case client.SubagentTool:
		c.addSubagentTool(msg)
		c.fleetTool(msg)
	case client.SubagentEnd:
		c.setSubagentEnd(msg.ParentCallID, msg.Usage, msg.ToolCount, msg.Stop, msg.DurationMs)
		c.fleetEnd(msg.ChildID, msg.Usage, msg.ToolCount, msg.Stop, msg.Cause, msg.DurationMs)
	}
}

// applyParallel routes a BOUNDED Parallel fork-join projection into the GROUPED
// parallelGroups state (keyed by ParentCallID), which backs the fleet footer segment and
// the f6 Parallel tab. Unlike Subagent it has no second inline-card destination: a
// Parallel run's deliverable (the winner summary + opaque artifact handle) rides
// the tool RESULT text the model reads; these events are the client observability channel only. The previews
// are bounded/scrubbed/client-only (gauntlet #7).
func (m *Model) applyParallel(msg client.ParallelMsg) {
	applyParallelTo(&m.conv, msg)
}

// applyParallelTo is the pure conversation-projection half of applyParallel: it
// routes a BOUNDED Parallel fork-join projection into c's parallelGroups (keyed
// by ParentCallID). The live applyParallel delegates here; the replay path calls
// this directly. The previews are bounded/scrubbed/client-only (gauntlet #7).
func applyParallelTo(c *conversation, msg client.ParallelMsg) {
	switch msg.Kind {
	case client.ParallelStart:
		c.parallelStart(msg.ParentCallID, msg.Join, msg.BranchCount)
	case client.ParallelBranchStart:
		c.parallelBranchStart(msg.ParentCallID, msg.BranchIndex, msg.ChildID, msg.BranchLabel, msg.Goal, msg.RoutedCategory, msg.RoutedModel, msg.RoutingReason, msg.Model)
	case client.ParallelBranchTool:
		c.parallelBranchTool(msg)
	case client.ParallelBranchEnd:
		c.parallelBranchEnd(msg.ParentCallID, msg.BranchIndex, msg.ChildID, msg.Usage, msg.ToolCount, msg.Stop, msg.Failed, msg.DurationMs)
	case client.ParallelEnd:
		c.parallelEnd(msg.ParentCallID, msg.Join, msg.BranchCount, msg.Winner, msg.Stop)
	}
}

// applyTeam attributes a BOUNDED team projection to its Team tool card by
// ParentCallID (id match, like applySubagent). A miss is silently dropped: the
// member transcripts never enter the parent conversation either way, so a lost
// team.* event only costs the lane trace, never correctness or isolation.
func (m *Model) applyTeam(msg client.TeamMsg) {
	applyTeamTo(&m.conv, msg)
	if msg.Kind == client.TeamEnd {
		// team.end carries the terminal task + findings snapshots too, so the sub-views
		// land the final state even if no member event followed the last transition.
		m.conv.setTeamTasks(msg.ParentCallID, msg.Tasks)
		m.conv.setTeamFindings(msg.ParentCallID, msg.Findings)
		// A team boundary is transient (like a no-progress notice): a brief muted footer
		// status, never a durable scrollback notice. The durable team outcome already rides
		// the team card + the run's ResultMsg.
		m.statusMsg = m.deps.Theme.Style("muted").Render(fmt.Sprintf("team done · %s", plural(msg.Rounds, "round")))
	}
}

// applyTeamTo is the pure conversation-projection half of applyTeam: it routes a
// BOUNDED team projection into c's Team tool card (keyed by ParentCallID). The
// live applyTeam delegates here and layers the terminal task/findings snapshot +
// the transient footer status on top; the replay path calls this directly so the
// read-only transcript gets the SAME projection without any live-run footer
// side-effect.
func applyTeamTo(c *conversation, msg client.TeamMsg) {
	switch msg.Kind {
	case client.TeamStart:
		c.setTeamStart(msg.ParentCallID, msg.TeamID, msg.Roster)
	case client.TeamMember:
		c.addTeamMember(msg)
	case client.TeamTasks:
		c.setTeamTasks(msg.ParentCallID, msg.Tasks)
	case client.TeamFindings:
		c.setTeamFindings(msg.ParentCallID, msg.Findings)
	case client.TeamEnd:
		c.setTeamEnd(msg.ParentCallID, msg.TeamID, msg.Rounds, msg.Stop, msg.Usage, msg.Dispositions)
	}
}

// onResize updates the WIDTH-bearing widget dimensions and invalidates the glamour
// width cache via the renderer width, then delegates HEIGHT sizing to relayout. Width
// is set here (it is purely a resize concern); the viewport height is derived from the
// measured region stack by relayout, so a single source of truth — chrome() — drives
// both onResize and convTopRow and they can never disagree.
//
// Order matters: width/height and the per-widget widths are set FIRST so renderHeader/
// chrome (which relayout measures) reflect the NEW width before the body height is
// computed from them.
func (m Model) onResize(msg tea.WindowSizeMsg) (tea.Model, tea.Cmd) {
	widthChanged := m.width != msg.Width
	viewportHeight := m.vp.Height()
	m.width = msg.Width
	m.height = msg.Height
	m.vp.SetWidth(m.width)
	// Viewport geometry changed: the vpView cache is stale regardless of relayout's
	// height-equality guard (relayout calls refreshView, which also invalidates, but
	// onResize must invalidate here too in case relayout short-circuits on height match).
	m.rend.invalidateVPView()
	// The input textarea is wrapped in the mode-coloured left rail (renderInput),
	// which adds its horizontal frame (border + padding). Shrink the textarea width by
	// that frame so the railed input stays within the terminal width instead of
	// overflowing by the rail's columns. inputRailStyle is the single source of the
	// rail geometry, so this can never drift from the rendered rail.
	railFrame := inputRailStyle(m.deps.Theme, m.inputMode()).GetHorizontalFrameSize()
	m.prompt.SetWidth(max(1, m.width-railFrame))
	m.rend.setWidth(m.width)
	// relayout sizes the viewport height from the measured layout (header + transients
	// + input + footer) and, when the height changed, re-renders + re-derives
	// auto-follow — the tail onResize used to do inline. A width-only resize leaves
	// that layout unchanged, so re-render below to replace any stale wrapped lines.
	// The magic taH=4/footerH=2 and
	// the header arithmetic are GONE; the heights are measured via lipgloss.Height of
	// the rendered regions in chrome().
	m.relayout()
	m.clampHelpScroll()
	m.clampAgentsDetailScroll()
	if widthChanged && m.vp.Height() == viewportHeight {
		m.refreshView()
		m.conversationView.observe(m.vp)
	}
	// Open modal surfaces derive geometry at Render time; no resize fan-out is needed.
	return m, m.maybeKittyTransmit()
}

// onColorProfile records whether the terminal is truecolor (from the
// tea.ColorProfileMsg Bubble Tea sends once at startup). It gates the welcome
// wordmark gradient: only a truecolor profile gets the per-column jade→gold blend;
// anything poorer collapses to a single accent colour (set in the welcome package
// off m.fullColor). The ui keeps colorprofile contained to the reducer — the
// welcome package only ever sees the derived bool.
func (m Model) onColorProfile(msg tea.ColorProfileMsg) Model {
	m.fullColor = msg.Profile == colorprofile.TrueColor
	return m
}

// onBackgroundColor is the light/dark auto-detect reducer (ADR 0280): it
// consumes the tea.BackgroundColorMsg Init requested via
// tea.RequestBackgroundColor when Deps.ThemeAutoDetect was set. It disarms
// themeAutoDetectArmed FIRST, so a duplicate or late response — a misbehaving
// terminal, or a race with a fast quit — is a structural no-op rather than a
// second switch. A light response (msg.IsDark() == false) switches to the
// built-in "solar" theme; a dark response leaves the given theme untouched.
// Only fires once: a caller with the detect disabled (or already consumed)
// sees themeAutoDetectArmed false and returns m unchanged.
func (m Model) onBackgroundColor(msg tea.BackgroundColorMsg) Model {
	if !m.themeAutoDetectArmed {
		return m
	}
	m.themeAutoDetectArmed = false
	if !msg.IsDark() {
		m = m.switchTheme(theme.Solar())
	}
	return m
}

// switchTheme installs th as the active theme and resets every baked-in
// consumer so nothing keeps rendering the old palette (ADR 0280): the input
// textarea and every overlay read m.deps.Theme fresh on each render, but the
// renderer's glamour/block/join caches and the spinner's style are captured at
// construction time and must be rebuilt explicitly.
func (m Model) switchTheme(th theme.Theme) Model {
	m.deps.Theme = th
	m.rend = newRenderer(th, m.rend.marks)
	m.rend.setWidth(m.width)
	m.sp.Style = th.Style("spinner")
	m.refreshView()
	return m
}

// maybeKittyTransmit fires the out-of-band Kitty mascot transmit (via tea.Raw)
// when the welcome splash is about to show on a Kitty-capable terminal and the
// image has not yet been transmitted at the CURRENT size tier. It is a no-op
// (nil cmd) when the banner is suppressed, the terminal isn't Kitty-capable, the
// zero-state isn't showing, the size is unknown, or the mascot is already
// transmitted at this tier. A size-TIER change re-transmits at the new footprint.
//
// The transmit escape produces no visible output and no cursor move, so it is safe
// to interleave with frames; it MUST go via tea.Raw (not View content), where the
// ultraviolet renderer would otherwise parse it into cells and desync the cursor.
func (m *Model) maybeKittyTransmit() tea.Cmd {
	if m.deps.NoBanner || m.width <= 0 || m.height <= 0 {
		return nil
	}
	// Only when the zero-state splash is what's on screen (idle + empty conversation),
	// and never after a restart-now handoff (the splash is suppressed then — see the
	// view.go body switch — so transmitting the mascot would orphan a placeholder grid
	// that never renders).
	if m.phase != phaseIdle || !m.conv.isEmpty() || m.restartedThisRun {
		return nil
	}
	if !m.deps.kittyCapable() {
		return nil
	}
	// Size the kitty footprint with the SAME (width, height) tier Splash uses, so the
	// transmitted virtual placement (cols×rows) matches the placeholder grid Splash
	// emits. Tier returns (0,0) when no mascot fits the budget — then there is no kitty
	// image to transmit (Splash renders mascot-less too), so leave kittyActive false.
	cols, rows := welcome.Tier(m.width, m.vp.Height())
	if cols == 0 {
		return nil
	}
	if m.kittyActive && m.kittyTier == cols {
		return nil // already transmitted at this tier.
	}
	esc := welcome.TransmitMascot(cols, rows)
	if esc == "" {
		return nil // decode failed → stay on the half-block path.
	}
	m.kittyActive = true
	m.kittyTier = cols
	return tea.Raw(esc)
}

// relayout sizes the conversation viewport HEIGHT to whatever the current region
// stack leaves for the body, so the footer is never pushed off-screen by a transient
// region (palette / mention / queue) — the overflow fix. bodyHeight is the total
// height minus everything chrome() renders above and below the body at the CURRENT
// model state; when a transient is present the viewport shrinks to keep the footer
// on-screen, and when it clears the viewport grows back.
//
// It is the SINGLE height authority (onResize and the per-message Update chokepoint
// both call it). onResize keeps its OWN call rather than relying on the chokepoint:
// onResize is invoked directly (not only through Update) — by tests and by any future
// non-Update caller — so it must size the viewport itself; the chokepoint's later call
// then early-returns on the height-equality guard, so the double call is free. It acts
// ONLY when the computed height differs from the viewport's current height — so the
// common case (no layout change) is a cheap chrome render + compare with no re-render.
// When the height DID change it mirrors onResize's old
// tail: SetHeight, refreshView (re-render the conversation into the resized viewport),
// then observes the viewport (SetHeight can clamp YOffset so AtBottom flips —
// re-derive auto-follow, otherwise the "↑ NN%" cue would lie). Width is NOT touched here (it is
// an onResize concern). A pointer receiver: it mutates the viewport in place.
func (m *Model) relayout() {
	if m.width <= 0 || m.height <= 0 {
		return // pre-first-resize: nothing to size against yet.
	}
	above, below := m.chrome()
	bodyHeight := m.height - sumHeight(above) - sumHeight(below)
	if bodyHeight < 1 {
		bodyHeight = 1
	}
	if bodyHeight == m.vp.Height() {
		return
	}
	m.vp.SetHeight(bodyHeight)
	m.refreshView()
	m.conversationView.observe(m.vp)
}

// physicalEscape reports the non-remappable hardware gesture. Bubble Tea's
// repeat metadata is authoritative when available; repeats remain owner-visible
// but can never arm or complete the draft-clear gesture.
func physicalEscape(msg tea.KeyPressMsg) bool {
	return msg.Code == tea.KeyEscape && msg.Mod == 0
}

func (m Model) hasDoubleEscapeDraft() bool {
	return m.prompt.Value() != "" || len(m.stagedMedia) > 0 || len(m.stagedPastes) > 0 ||
		len(m.pendingPromptMedia.Parts) > 0 || len(m.pendingPromptMedia.Descriptors) > 0
}

// doubleEscapeEligible is intentionally narrower than ordinary prompt input:
// every selection, menu, overlay, modal, paused queue, and running-cancel owner
// gets Escape first. Only the focused, plain idle composer can use the gesture.
func (m Model) doubleEscapeEligible() bool {
	return m.keyboardEventTypes && m.phase == phaseIdle && m.prompt.Focused() && m.hasDoubleEscapeDraft() &&
		!m.sel.active && !m.prompt.HasSelection() && !m.palette.open && !m.mention.open &&
		m.queuePaused == "" && !bodyOwnerOpen(m)
}

func (m Model) onKeyboardProtocolMsg(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyboardEnhancementsMsg:
		m.keyboardEventTypes = msg.SupportsEventTypes()
		if !m.keyboardEventTypes {
			m.doubleEscapeArmed = false
			m.doubleEscapeReleased = false
		}
	case tea.KeyReleaseMsg:
		releasedKey := msg.Key()
		if m.doubleEscapeArmed && m.doubleEscapeEligible() && releasedKey.Code == tea.KeyEscape && releasedKey.Mod == 0 {
			m.doubleEscapeReleased = true
		}
	}
	return m, nil
}

// onKey routes key presses by phase. ctrl+c is handled first, with a graceful
// double-press guard (Claude Code's "press again to exit"): a first ctrl+c does
// NOT quit — it clears a non-empty prompt, or on an empty prompt arms the guard
// and shows a hint; a second ctrl+c while armed quits. The fatal screen is the one
// exception (single press quits — there is nothing to lose). The OS-signal path
// (SIGINT/SIGTERM via tea.WithContext in main) is unaffected; this is the in-TUI
// key path only.
func (m Model) onKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	// Any intervening key breaks consecutiveness. Escape owned by another surface
	// also disarms before that owner handles it; a repeat is never a gesture press.
	if !physicalEscape(msg) || !m.doubleEscapeEligible() {
		m.doubleEscapeArmed = false
		m.doubleEscapeReleased = false
	}

	// Global lifecycle controls retain precedence over every overlay.
	if key.Matches(msg, m.keys.Quit) {
		return m.onQuitKey()
	}

	// ctrl+d is the unix EOF-habit quit (issue #504), an INDEPENDENT second guard.
	// It sits right after Quit and BEFORE the textarea/phase routing so the chord is
	// intercepted everywhere — but only when the prompt textarea is EMPTY. On a
	// populated prompt we deliberately do NOT consume it, so the chord falls through
	// to the textarea's DeleteCharacterForward (its default bubble binding) — never a
	// surprise quit mid-draft. Handled even when an overlay/modal owns the keyboard
	// (the textarea is blurred and empty then, so the empty gate passes).
	if key.Matches(msg, m.keys.QuitD) && strings.TrimSpace(m.prompt.Value()) == "" {
		return m.onQuitDKey()
	}

	// ctrl+z suspends the whole TUI process to the shell (issue #504). Handled
	// BEFORE the phase/overlay routing so it works in EVERY state — idle, mid-run,
	// the permission modal (the ask stays pending and durable). onSuspend writes the
	// leave-behind notice to stderr, then returns tea.Suspend. Suspending does NOT
	// stop the embedded mecated or an in-flight run — they keep working and the UI
	// re-syncs on resume.
	if key.Matches(msg, m.keys.Suspend) {
		return m.onSuspend()
	}

	// Help owns the remaining keys while open: its documented navigation and close
	// controls act on the overlay and every ordinary key is swallowed.
	if m.showHelp {
		return m.onHelpKey(msg)
	}

	// Disarm whichever quit guards are armed: any non-ctrl+c key disarms the Quit
	// guard and any non-ctrl+d key disarms the QuitD guard (each guard's window spans
	// only its own consecutive presses). The two guards are INDEPENDENT — neither key
	// confirms the other (validator rule 6 also forbids them sharing a chord). Placed
	// AFTER both branches so a second press of either key reaches its confirm path
	// while armed rather than disarming here.
	m = m.disarmQuitGuards(key.Matches(msg, m.keys.Quit), key.Matches(msg, m.keys.QuitD))

	// Any keypress at idle dismisses the once-per-process gateway notice (Proposal 1)
	// — the operator has seen it and is now doing something. gatewayNoticeShown stays
	// latched so it never re-fires this process. Placed in the top-level key handler
	// (gated on idle) so every idle key clears it before per-phase routing; at other
	// phases the notice is not rendered (the running/approval/connecting arms own the
	// footer-left), so clearing it is unnecessary there.
	if m.phase == phaseIdle && m.gatewayNotice != "" {
		m.gatewayNotice = ""
	}

	// esc clears an ACTIVE text selection FIRST — before every other esc meaning
	// (cancel run / close overlay / clear input/queue). This consumes the key ONLY
	// when a selection is active; with no selection it falls through untouched, so
	// today's esc semantics are entirely preserved (Req 5). A selection only exists
	// on the alt screen with no overlay (selectable), so this never shadows an
	// overlay's own esc.
	if mm, cleared := m.clearAnySelection(msg); cleared {
		return mm, nil
	}

	// An open inventory/picker overlay (MCP, team, agents, skills, soul, usermodel,
	// models) owns the keyboard while open — each steps back / closes on esc
	// internally and returns handled=false when closed. Routed here before the phase
	// switch (and before the ctrl+t toggle, so esc/enter belong to the overlay).
	if mm, cmd, handled := m.onOverlayKey(msg); handled {
		return mm, cmd
	}

	// ctrl+t is a global render toggle (full vs capped tool output); it works in
	// any phase and never feeds the textarea.
	if key.Matches(msg, m.keys.ExpandTools) {
		return m.onExpandToolsKey()
	}

	// ctrl+v reads the OS clipboard into the prompt (image → staged attachment,
	// text → inserted). It is handled here — before the phase switch — for the two
	// input-accepting phases (idle + running, both keep the textarea focused for
	// compose/enqueue), so it behaves identically in either and is not duplicated in
	// both per-phase handlers. It is not a palette/mention nav key, so routing it
	// ahead of those menus shadows nothing.
	if key.Matches(msg, m.keys.Paste) && (m.phase == phaseIdle || m.phase == phaseRunning) {
		return m.onClipboardPaste()
	}

	// shift+tab cycles permission mode. It is handled here — before the phase switch —
	// for idle + running so the key never feeds the textarea. It also avoids relying
	// on macOS Option being configured as Meta.
	if m.deps.DebugTarget == "" && key.Matches(msg, m.keys.ModeSwitch) && (m.phase == phaseIdle || m.phase == phaseRunning) {
		return m.switchMode(client.NextMode(m.desiredMode()))
	}

	return m.dispatchPhaseKey(msg)
}

// onHelpKey handles the help overlay's complete keyboard contract. It runs before
// phase routing, so navigation never reaches the conversation and every other key
// remains swallowed.
func (m Model) onHelpKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	m.clampHelpScroll()
	total, window := m.helpScrollGeometry()
	switch {
	case key.Matches(msg, m.keys.Help), key.Matches(msg, m.keys.Close):
		m.showHelp = false
		m.helpScroll = 0
		_ = m.prompt.Focus()
	case key.Matches(msg, m.keys.ScrollD):
		m.helpScroll = clampScroll(m.helpScroll+window, total, window)
	case key.Matches(msg, m.keys.ScrollU):
		m.helpScroll = clampScroll(m.helpScroll-window, total, window)
	case key.Matches(msg, m.keys.Down):
		m.helpScroll = clampScroll(m.helpScroll+1, total, window)
	case key.Matches(msg, m.keys.Up):
		m.helpScroll = clampScroll(m.helpScroll-1, total, window)
	case key.Matches(msg, m.keys.ScrollBottom):
		m.helpScroll = maxScrollOffset(total, window)
	case key.Matches(msg, m.keys.ScrollTop):
		m.helpScroll = 0
	}
	return m, nil
}

// helpScrollGeometry derives the same complete rendered lines and window used by
// renderHelpOverlay, keeping key navigation and height-bounded rendering aligned.
func (m Model) helpScrollGeometry() (total, window int) {
	lines := helpRenderedLines(helpBody(m.deps.Theme, m.caps, m.helpKeyMarkings()))
	return len(lines), helpWindowHeight(m.deps.Theme, m.vp.Height(), len(lines))
}

// clampHelpScroll keeps a retained offset valid after a relayout changes the
// viewport geometry. It deliberately preserves a still-valid offset rather than
// pinning an earlier End selection to the new bottom.
func (m *Model) clampHelpScroll() {
	total, window := m.helpScrollGeometry()
	m.helpScroll = clampScroll(m.helpScroll, total, window)
}

// selection owner is active.
func (m Model) clearAnySelection(msg tea.KeyPressMsg) (Model, bool) {
	if !key.Matches(msg, m.keys.Cancel) || (!m.sel.active && !m.prompt.HasSelection()) ||
		m.showHelp || m.modal != nil || m.team.view != teamNone || m.agentsInv.view != agentsInvNone ||
		m.userModel.view != userModelNone || m.reflections.view != reflectionsNone ||
		m.dream.view != dreamClosed || m.effort.view != effortNone || m.worktrees.view != worktreesNone {
		return m, false
	}
	m = m.clearSelection()
	m.prompt.ClearSelection()
	m.refreshView()
	return m, true
}

// onExpandToolsKey is the ctrl+t handler, extracted from onKey so onKey stays
// under the cyclomatic cap. ctrl+t is a global render toggle (full vs capped
// tool output); inside the permission modal it ROUTES by ask type (issue #488):
// a non-diff, non-plan ask opens/closes the full-screen ask-args view INSTEAD
// of toggling expandTools; a plan ask or an Edit/Write (diff-capable) ask keeps
// the in-modal expand behavior byte-for-byte. Approval emits a semantic toggle
// intent for the latter; Model owns the global expandTools effect.
func (m Model) onExpandToolsKey() (tea.Model, tea.Cmd) {
	m.expandTools = !m.expandTools
	m.refreshView()
	return m, nil
}

// dispatchPhaseKey is the per-phase key router, extracted from onKey so onKey
// stays under the cyclomatic cap as phases accrue. phaseAwaitingApproval→modal,
// phaseRunning→running-key, phaseReplay→replay-key (esc closes the transcript),
// phaseIdle→idle-key. The default (connecting/fatal) is a no-op.
func (m Model) dispatchPhaseKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if mm, cmd, handled := m.promptKey(msg); handled {
		return mm, cmd
	}
	switch m.phase {
	case phaseAwaitingApproval:
		return m, nil
	case phaseAuthorizing:
		return m.onMCPAuthorizationKey(msg)
	case phaseRunning:
		return m.onRunningKey(msg)
	case phaseIdle:
		return m.onIdleKey(msg)
	default:
		return m, nil
	}
}

// onOverlayKey routes a key to whichever inventory/picker overlay is open, in a
// fixed order. Each per-overlay handler returns handled=false when its overlay is
// closed, so at most one consumes the key (they never coexist). Extracted from
// onKey so the dispatcher stays under the cyclomatic cap as overlays accrue. The
// /models picker is the only SELECTING one (cursor + enter); the rest are read-only
// / esc-only. Returns handled=false when no overlay is open so onKey falls through.
func (m Model) onOverlayKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd, bool) {
	if m.startupRunEntryFailed {
		mm, cmd := m.onStartupRunEntryKey(msg)
		return mm, cmd, true
	}
	if mm, cmd, handled := m.dispatchSurfaceKey(msg); handled {
		return mm, cmd, true
	}
	overlays := []func(tea.KeyPressMsg) (tea.Model, tea.Cmd, bool){
		m.onSessionDetailsKey,
		m.onAgentsKey,
		m.onAgentsInvKey,
		m.onUserModelKey,
		m.onReflectionsKey,
		m.onDreamKey,
		m.onConnectKey,
		m.onEffortKey,
		m.onWorktreesKey,
		m.onScheduleKey,
	}
	for _, route := range overlays {
		if mm, cmd, handled := route(msg); handled {
			return mm, cmd, true
		}
	}
	return m, nil, false
}

// dispatchSurfaceKey routes a KeyPressMsg through the open modal surface when
// one is open. On handled+closed it runs the surface's (no-return) Close, nils
// the field, and batches the textarea refocus — the parent refocuses because
// the returned cmd belongs to the surface, not the close. Extracted from
// onOverlayKey so update() stays under the cyclomatic cap; onOverlayKey reads it.
func (m Model) dispatchSurfaceKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd, bool) {
	if m.modal == nil {
		return m, nil, false
	}
	// A clear command composed while running stays actionable if a permission ask
	// opens before Enter is pressed. Route it ahead of the approval surface:
	// ClearSession cancels the ask and must never become Allow/Deny/Learn.
	if clearCommandSubmitted(msg, m.keys.Submit, m.prompt.Value()) {
		return m.dispatchBareBuiltin(m.prompt.Value())
	}
	cmd, handled, closed := m.modal.HandleKey(msg)
	if !handled {
		return m, nil, false
	}
	if source, ok := m.modal.(surfaceIntentSource); ok {
		mm, intentCmd, stopSurfaceDispatch := m.applySurfaceIntent(source.takeSurfaceIntent())
		if stopSurfaceDispatch {
			return mm, tea.Batch(cmd, intentCmd), true
		}
		m = mm.(Model)
		cmd = tea.Batch(cmd, intentCmd)
	}
	if closed {
		m.closeModal()
		return m, tea.Batch(cmd, m.prompt.Focus()), true
	}
	return m, cmd, true
}

func clearCommandSubmitted(msg tea.KeyPressMsg, submit key.Binding, prompt string) bool {
	return key.Matches(msg, submit) && strings.EqualFold(strings.TrimSpace(prompt), "/clear")
}

// dispatchSurfaceMsg routes a NON-input Msg through the open modal surface
// before the Model's generic reducer; handled=false falls through. On
// handled+closed it runs the surface's (no-return) Close, nils the field, and
// batches the textarea refocus (same parent-refocuses path as
// dispatchSurfaceKey). Extracted so update() stays under the cyclomatic cap.
func (m Model) dispatchSurfaceMsg(msg tea.Msg) (tea.Model, tea.Cmd, bool) {
	if hit, ok := msg.(surfaceHitMsg); ok && !m.hits.contains(hit.ID) {
		return m, nil, true
	}
	if m.modal == nil {
		return m, nil, false
	}
	cmd, handled, closed := m.modal.HandleMsg(msg)
	if !handled {
		return m, nil, false
	}
	if source, ok := m.modal.(surfaceIntentSource); ok {
		mm, intentCmd, stopSurfaceDispatch := m.applySurfaceIntent(source.takeSurfaceIntent())
		if stopSurfaceDispatch {
			return mm, tea.Batch(cmd, intentCmd), true
		}
		m = mm.(Model)
		cmd = tea.Batch(cmd, intentCmd)
	}
	if closed {
		m.closeModal()
		return m, tea.Batch(cmd, m.prompt.Focus()), true
	}
	return m, cmd, true
}

// applyModelsSurfaceIntent handles the small /models intent family while keeping
// Model-owned catalog, restart, and persistence effects out of the surface.
// stopSurfaceDispatch tells the caller to skip its common surface-dispatch
// post-processing after this root-owned effect takes over.
func (m Model) applyModelsSurfaceIntent(intent surfaceIntent) (model tea.Model, cmd tea.Cmd, handled bool, stopSurfaceDispatch bool) {
	switch intent := intent.(type) {
	case modelsCatalogIntent:
		mm, cmd, stopSurfaceDispatch := m.applyModelsCatalog(intent.msg)
		return mm, cmd, true, stopSurfaceDispatch
	case modelsSelectIntent:
		mm, cmd, _ := m.chooseModel(intent.selection, intent.label)
		return mm, cmd, true, false
	case modelsGlobalDefaultIntent:
		mm, cmd := m.setGlobalDefault(intent.selection, intent.label)
		m = mm.(Model)
		if surface, ok := m.modal.(*modelsState); ok {
			surface.catalog.globalDefault = m.modelCatalog.globalDefault
		}
		return m, cmd, true, false
	default:
		return m, nil, false, false
	}
}

// applySessionsSurfaceIntent handles the /sessions intent family while keeping
// active-session, startup, and maintenance effects owned by Model.
func (m Model) applySessionsSurfaceIntent(intent surfaceIntent) (model tea.Model, cmd tea.Cmd, handled bool, stopSurfaceDispatch bool) {
	switch intent := intent.(type) {
	case sessionsPhaseIntent:
		switch intent.phase {
		case sessionsIntentPhaseIdle:
			m.phase = phaseIdle
		case sessionsIntentPhaseReplay:
			m.phase = phaseReplay
			m.prompt.Blur()
		}
		return m, nil, true, false
	case sessionsMigrationJobIntent:
		m.maintenanceMigrationJobID = intent.jobID
		return m, nil, true, false
	case sessionsCleanupJobIntent:
		m.maintenanceCleanupJobID = intent.jobID
		return m, nil, true, false
	case sessionsTranscriptAdoptionIntent:
		m.caps = intent.capabilities
		(&m).setResolvedSessionModel(intent.model)
		m.activeMode = intent.mode
		mm, cmd, stopSurfaceDispatch := m.adoptAuthoritativeTranscript(intent.row, intent.transcript)
		return mm, cmd, true, stopSurfaceDispatch
	case sessionsStartupQuitIntent:
		if sessions, ok := m.modal.(*sessionsState); ok && sessions.pageCancel != nil {
			sessions.pageCancel()
		}
		m.sessionsPageRequestToken++
		return m, tea.Quit, true, true
	case sessionsStartupNewIntent:
		if !m.modelsReconciled {
			m.statusMsg = m.deps.Theme.Style("muted").Render("loading model defaults…")
			return m, nil, true, false
		}
		if sessions, ok := m.modal.(*sessionsState); ok && sessions.pageCancel != nil {
			sessions.pageCancel()
		}
		m.sessionsPageRequestToken++
		m.closeModal()
		m.phase = phaseConnecting
		return m, m.createSessionCmd(), true, true
	case sessionsActiveTitleIntent:
		if intent.id == m.sessionID {
			m, _ = m.adoptTitle(intent.title, intent.provenance, intent.revision)
		}
		m.statusMsg = m.deps.Theme.Style("success").Render(intent.successNotice)
		return m, nil, true, false
	case sessionsStatusNoticeIntent:
		style := "warning"
		if intent.style == sessionsStatusNoticeSuccess {
			style = "success"
		}
		m.statusMsg = m.deps.Theme.Style(style).Render(intent.text)
		return m, nil, true, false
	default:
		return m, nil, false, false
	}
}

// applySurfaceIntent applies a drained surface intent synchronously in the same
// Tea Update. Returned commands still run asynchronously. stopSurfaceDispatch
// tells the caller to skip common dispatch post-processing after a root-owned
// effect takes over; handled=false never carries meaningful work.
func (m Model) applySurfaceIntent(intent surfaceIntent) (model tea.Model, cmd tea.Cmd, stopSurfaceDispatch bool) {
	if model, cmd, handled, stopSurfaceDispatch := m.applyApprovalSurfaceIntent(intent); handled {
		return model, cmd, stopSurfaceDispatch
	}
	if model, cmd, handled, stopSurfaceDispatch := m.applyModelsSurfaceIntent(intent); handled {
		return model, cmd, stopSurfaceDispatch
	}
	if model, cmd, handled, stopSurfaceDispatch := m.applySessionsSurfaceIntent(intent); handled {
		return model, cmd, stopSurfaceDispatch
	}
	return m, nil, false
}

func (m Model) desiredMode() string {
	if m.pendingMode != "" {
		return m.pendingMode
	}
	if m.activeMode != "" {
		return m.activeMode
	}
	return client.ModeString(client.ModeFromString(m.deps.Mode))
}

func (m Model) switchMode(mode string) (tea.Model, tea.Cmd) {
	mode = client.ModeString(client.ModeFromString(mode))
	if m.sessionID == "" {
		m.statusMsg = m.deps.Theme.Style("warning").Render("no active session — mode switch unavailable")
		return m, nil
	}
	if mode == m.activeMode && m.pendingMode == "" {
		m.statusMsg = m.deps.Theme.Style("muted").Render("mode already " + mode)
		return m, nil
	}
	m.pendingMode = mode
	m.statusMsg = m.deps.Theme.Style("muted").Render("switching mode to " + mode + "…")
	return m, client.SetModeCmd(m.deps.Ctx, m.deps.Session, m.sessionID, mode)
}

func (m Model) onModeChanged(msg client.ModeChangedMsg) tea.Model {
	if msg.SessionID != m.sessionID {
		return m
	}
	requested := client.ModeString(client.ModeFromString(msg.Requested))
	if m.pendingMode != "" && requested != m.pendingMode {
		return m
	}
	if msg.Err != nil {
		m.pendingMode = requested
		m.statusMsg = m.deps.Theme.Style("warning").Render("mode " + m.pendingMode + " will apply on the next prompt")
		return m
	}
	mode := client.ModeString(client.ModeFromString(msg.Mode))
	if mode != requested {
		return m
	}
	m.activeMode = mode
	m.pendingMode = ""
	m.statusMsg = m.deps.Theme.Style("success").Render("mode " + mode)
	return m
}

func (m Model) retryPendingModeCmd() tea.Cmd {
	if m.pendingMode == "" || m.sessionID == "" {
		return nil
	}
	return client.SetModeCmd(m.deps.Ctx, m.deps.Session, m.sessionID, m.pendingMode)
}

// onQuitKey implements the graceful double-press ctrl+c (issue #17): a second press
// while armed quits; the fatal screen quits on the first press; a first press with
// staged input clears it (no arm); a first press on an empty prompt arms the guard,
// shows the hint, and schedules the timed disarm. See onKey's doc for the rationale.
func (m Model) quitNow() (tea.Model, tea.Cmd) {
	if m.cancelRun != nil {
		m.cancelRun()
	}
	return m, tea.Quit
}

// onQuitKey implements the guarded ctrl+c exit behavior. The immediate exit is
// shared with /quit so both paths cancel an active run before Bubble Tea exits.
func (m Model) onQuitKey() (tea.Model, tea.Cmd) {
	// Already armed → a second ctrl+c within the window: quit now. The fatal
	// (dead-connection) screen also exits on a single press — there is no input to
	// clear and no run to protect, so the guard would only add friction.
	if m.quitArmed || m.phase == phaseFatal {
		return m.quitNow()
	}
	// First ctrl+c with staged input: clear the input (mirrors esc's clear-the-line)
	// and do NOT arm — a single press to wipe a draft is expected.
	if strings.TrimSpace(m.prompt.Value()) != "" {
		m.prompt.Reset()
		m.pendingPromptMedia = client.MediaResult{}
		return m.afterInputEdit(nil)
	}
	// First ctrl+c on an empty prompt: arm the guard, show the hint, and schedule the
	// timed disarm for this arm generation.
	m.quitArmed = true
	m.quitArmGen++
	m.statusMsg = quitHintFor(m.keys.Quit)
	m.refreshView()
	return m, m.quitDisarmCmd(m.quitArmGen)
}

// onSuspend suspends the whole TUI process to the shell (ctrl+z, issue #504).
// A pre-suspend leave-behind notice is NOT printed: it landed either in the
// alt-screen buffer (direct write — discarded on the suspend's alt-screen exit)
// or was frozen before Bubble Tea could flush its deferred-print queue
// (tea.Println is only drained on a render-tick, which the SIGTSTP freeze
// precludes). Instead we RECORD the phase we suspend from, and the ResumeMsg
// reducer emits an honest "you suspended mid-X" notice into the conversation on
// return — the operator sees it at the only moment they can act on it (after
// fg). tea.Suspend drives Bubble Tea's suspend (release the terminal, exit the
// alt screen, SIGTSTP the process group); on fg/SIGCONT Bubble Tea restores the
// terminal (re-enter alt screen, re-capture the mouse, repaint, fresh
// WindowSizeMsg) and delivers a ResumeMsg.
func (m Model) onSuspend() (tea.Model, tea.Cmd) {
	m.suspendedFrom = m.phase
	m.suspendedAtID = m.sessionID
	return m, tea.Suspend
}

// onResume handles tea.ResumeMsg — the model resumed from a ctrl+z suspend (issue
// #504). Bubble Tea has already re-entered the alt screen, re-captured the mouse,
// and repainted (its RestoreTerminal + checkResize sends a fresh WindowSizeMsg
// too). Surface the leave-behind notice NOW — the only moment the operator can
// read it — as an in-conversation notice naming what was suspended. (We could not
// print it pre-suspend: a direct write landed in the discarded alt-screen buffer,
// and tea.Println's deferred queue never gets a flush tick before SIGTSTP freezes
// the process.)
func (m Model) onResume() Model {
	if m.suspendedAtID != "" || m.suspendedFrom != phaseConnecting {
		m.conv.addNotice(m.resumeNotice())
		m.suspendedFrom = phaseConnecting
		m.suspendedAtID = ""
	}
	m.refreshView()
	return m
}

// resumeNotice builds the "you suspended; the engine kept running" notice shown on
// resume from a ctrl+z suspend (issue #504). It names the session captured at
// suspend time and what the phase was, so a mid-run suspend's continuation is
// explicit.
func (m Model) resumeNotice() string {
	sid := m.suspendedAtID
	if sid == "" {
		sid = "connecting"
	}
	what := "idle"
	switch m.suspendedFrom {
	case phaseRunning:
		what = "a running turn"
	case phaseAwaitingApproval:
		what = "a pending permission ask"
	case phaseReplay:
		what = "a session replay"
	}
	return "Resumed from suspend — the mecatl engine kept running while the UI was suspended (session " + sid + ", was " + what + ")."
}

// onQuitDKey implements the ctrl+d double-press quit (issue #504) — the unix
// EOF-habit companion to onQuitKey, with INDEPENDENT armed state. A second ctrl+d
// while armed quits; the fatal screen quits on a single press (no input, no run to
// protect). UNLIKE ctrl+c there is no clear-the-input arm: ctrl+d is EOF-quit only,
// and the caller's empty-prompt gate already ensured the prompt is empty — a
// populated prompt never reaches here (the chord falls through to the textarea's
// DeleteCharacterForward instead). ctrl+c owns "clear the line"; the two guards are
// deliberately separate chords + separate state so neither confirms the other.
func (m Model) onQuitDKey() (tea.Model, tea.Cmd) {
	if m.quitDArmed || m.phase == phaseFatal {
		return m.quitNow()
	}
	// The caller's gate keeps the prompt empty here (a populated prompt keeps the
	// chord in the textarea).
	m.quitDArmed = true
	m.quitDArmGen++
	m.statusMsg = quitDHintFor(m.keys.QuitD)
	m.refreshView()
	return m, m.quitDDisarmCmd(m.quitDArmGen)
}

// onQuitDDisarm handles the QuitD timed disarm tick: it disarms ONLY if the tick
// matches the current arm generation and clears the hint only if still ours.
func (m Model) onQuitDDisarm(msg quitDDisarmMsg) (tea.Model, tea.Cmd) {
	if m.quitDArmed && msg.gen == m.quitDArmGen {
		m.quitDArmed = false
		if m.statusMsg == quitDHintFor(m.keys.QuitD) {
			m.statusMsg = ""
		}
		m.refreshView()
	}
	return m, nil
}

// onQuitDisarm handles the timed disarm tick: it disarms ONLY if the tick matches
// the current arm generation (a stale tick from a prior arm must not disarm a guard
// re-armed since) and clears the hint only if it is still the one we set.
func (m Model) onQuitDisarm(msg quitDisarmMsg) (tea.Model, tea.Cmd) {
	if m.quitArmed && msg.gen == m.quitArmGen {
		m.quitArmed = false
		if m.statusMsg == quitHintFor(m.keys.Quit) {
			m.statusMsg = ""
		}
		m.refreshView()
	}
	return m, nil
}

// onClickDisarm handles the timed multi-click disarm tick: it resets the click
// count to 0 ONLY if the tick matches the current click generation (a stale tick
// from a prior press must not reset a count advanced by a press since). It mutates
// no visible state, so no refreshView is needed.
func (m Model) onClickDisarm(msg clickDisarmMsg) (tea.Model, tea.Cmd) {
	if msg.gen == m.clickGen {
		m.clickCount = 0
	}
	return m, nil
}

// onDisarmMsg fans the timed-disarm messages out to their reducers: one switch
// case in update() that re-discriminates the concrete type here — the
// onPasteMsg/onMouseMsg pattern, keeping update()'s cyclomatic complexity bounded.
func (m Model) onDisarmMsg(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case quitDisarmMsg:
		return m.onQuitDisarm(msg)
	case quitDDisarmMsg:
		return m.onQuitDDisarm(msg)
	case clickDisarmMsg:
		return m.onClickDisarm(msg)
	case doubleEscapeExpiryMsg:
		if m.doubleEscapeArmed && msg.gen == m.doubleEscapeGen {
			m.doubleEscapeArmed = false
			m.doubleEscapeReleased = false
		}
		return m, nil
	default:
		return m, nil
	}
}

// onPasteMsg fans the three paste-delivery messages out to their reducers: a
// bracketed paste (tea.PasteMsg), the shell primary-selection read result
// (primaryReadMsg, the middle-click paste), and an OSC52 clipboard response
// (tea.ClipboardMsg, the middle-click paste's terminal fallback). It is one
// switch case in update() that re-discriminates the concrete type here — the
// onMouseMsg pattern, keeping update()'s cyclomatic complexity bounded.
func (m Model) onPasteMsg(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.PasteMsg:
		return m.onPaste(msg)
	case primaryReadMsg:
		return m.onPrimaryRead(msg)
	case tea.ClipboardMsg:
		return m.onClipboardMsg(msg)
	default:
		return m, nil
	}
}

// onPaste routes a bracketed-paste payload to the prompt input. Bubble Tea v2
// emits tea.PasteMsg (not KeyPressMsg) for a paste, so it does NOT pass through
// onKey — this mirrors onKey's overlay/help/phase gating itself. A paste is
// dropped while any overlay/help/approval owns the keyboard (it must not leak
// into the input behind the modal); otherwise it is accepted ONLY in the
// input-accepting phases (idle or running — both keep the textarea focused for
// compose/enqueue) and forwarded to the textarea, which inserts the runes
// internally, then run through afterInputEdit so the slash palette re-syncs
// (e.g. pasting "/cl" opens the palette) — the same funnel typed input uses.
func (m Model) onPaste(msg tea.PasteMsg) (tea.Model, tea.Cmd) {
	if !m.pasteGateOpen() {
		return m, nil
	}
	content := normalizePastedNewlines(msg.Content)
	// A bracketed paste whose payload is a single media FILE PATH (the common
	// drag-an-image-onto-the-terminal flow) is staged as an attachment instead of
	// inserted literally. On ANY miss (not a path, not media, cap-gated, oversize)
	// it returns ok=false and we fall through to the literal-text insert below
	// (iteration-1 behaviour) — so prose pastes are completely unaffected.
	if mm, cmd, ok := m.tryPasteMediaPath(content); ok {
		return mm, cmd
	}
	// A LARGE text paste (>= pasteCharThreshold runes or >= pasteLineThreshold
	// lines, alone or CUMULATIVELY with the current buffer — see pasteNeedsStaging)
	// is staged behind a "[Pasted text #N]" placeholder instead of entering the
	// textarea: the textarea re-wraps every logical line per rendered frame, so a
	// huge buffered paste makes every subsequent keypress O(paste) (issue #45).
	// The full text expands back in place at submit/enqueue. Below the thresholds
	// the literal insert below is byte-identical to the pre-staging behaviour.
	if pasteNeedsStaging(m.prompt.Value(), content) {
		return m.stageLargePaste(content)
	}
	msg.Content = content
	cmd := m.prompt.UpdatePaste(msg)
	return m.afterInputEdit(cmd)
}

const xtermModifyOtherKeysCtrlJ = "\x1b[27;5;106~"

// normalizePastedNewlines restores newlines that xterm modifyOtherKeys encodes
// as Ctrl-J while bracketed paste is active.
func normalizePastedNewlines(content string) string {
	return strings.ReplaceAll(content, xtermModifyOtherKeysCtrlJ, "\n")
}

// pasteGateOpen reports whether pasted text may reach the prompt input right now:
// no overlay/help/approval owns the screen, and the phase accepts input (idle or
// running — both keep the textarea focused for compose/enqueue). It is THE paste
// gate, shared by the bracketed-paste reducer (onPaste) and the middle-click
// primary-selection paste trigger (onMousePress), so the two paths can never
// drift apart.
func (m Model) pasteGateOpen() bool {
	if m.showHelp || m.phase == phaseAwaitingApproval ||
		m.modal != nil || m.team.view != teamNone || m.agentsInv.view != agentsInvNone || m.dream.view != dreamClosed {
		return false
	}
	return m.phase == phaseIdle || m.phase == phaseRunning
}

// onRunningKey handles keys while a run streams. Type-while-running: the textarea
// stays focused so the user can compose a steer or queued follow-up. It mirrors
// onIdleKey's precedence so the input behaves the same mid-run as at idle, with
// two differences — bare local built-ins still run locally, then enter steers or
// enqueues ordinary input; esc cancels the in-flight run directly:
//
//	(1) an open palette claims its navigation and completion keys (↑/↓/tab/enter)
//	    so builtins dispatch through the same path and workspace rows complete;
//	    esc is handled by the cancel path below;
//	(2) esc/Cancel sends Cancel and leaves the draft, staged queue, and steer state intact;
//	(3) shift+enter (Newline) → insert a newline;
//	(4) enter (Submit) → run a bare local built-in, or enqueuePrompt for model-facing
//	    input (which steers when supported);
//	(5) pgup/pgdn → scroll the viewport;
//	(6) anything else → feed the textarea (+ palette resync via afterInputEdit).
//
// ctrl+t (expand) and ctrl+c (quit) are handled globally in onKey before this.
func (m Model) onRunningKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	// Let the palette claim its navigation and completion keys while running. A
	// selected builtin dispatches locally; a workspace row completes into the
	// textarea. Esc stays with the layered cancel path below.
	if m.palette.open && !key.Matches(msg, m.keys.Cancel) {
		if mm, cmd, handled := m.onPaletteKey(msg); handled {
			return mm, cmd
		}
	}
	// The @-mention menu, like the palette, claims its navigation/complete keys
	// while running EXCEPT enter (which reaches the same builtin dispatcher, then
	// steers or queues) and esc (the Cancel branch sends Cancel directly).
	if m.mention.open && !key.Matches(msg, m.keys.Submit) && !key.Matches(msg, m.keys.Cancel) {
		if mm, handled := m.onMentionKey(msg); handled {
			return mm, nil
		}
	}
	switch {
	case m.wantsEditBack(msg):
		// ↑ on an EMPTY input line with staged follow-ups pulls the merged queue back
		// into the textarea for editing. It is distinct from Escape, which cancels
		// the running turn without changing the queue.
		return m.editBackQueue()
	case key.Matches(msg, m.keys.Agents):
		// f6 opens the unified agents overlay MID-RUN (Gap B): the deep view is
		// most useful while agents stream. openAgents permits phaseRunning, reads the
		// live lanes/fleet, and picks the context-sensitive default tab; pre-empt the
		// textarea default so the keypress drives the overlay, never the input.
		return m.openAgents()
	case key.Matches(msg, m.keys.Cancel):
		return m.onRunningCancel()
	case key.Matches(msg, m.keys.Newline):
		m.prompt.InsertNewline()
		return m.afterInputEdit(nil)
	case key.Matches(msg, m.keys.Submit):
		if mm, cmd, handled := m.dispatchBareBuiltin(m.prompt.Value()); handled {
			return mm, cmd
		}
		return m.enqueuePrompt()
	case key.Matches(msg, m.keys.ScrollU), key.Matches(msg, m.keys.ScrollD),
		key.Matches(msg, m.keys.ScrollTop), key.Matches(msg, m.keys.ScrollBottom):
		return m.onScrollKey(msg)
	default:
		cmd := (&m).updatePromptKey(msg)
		return m.afterInputEdit(cmd)
	}
}

// onRunningCancel sends Cancel while a run streams without changing any unsent
// composition state. ClearPrompt is the explicit action for dropping a draft.
func (m Model) onRunningCancel() (tea.Model, tea.Cmd) {
	stream := m.stream
	authorizationStream := m.authorization.controlStream
	if m.authorization.runningControlGen != m.authorization.controlGen {
		authorizationStream = nil
	}
	m.statusMsg = "cancelling…"
	return m, func() tea.Msg {
		if authorizationStream != nil {
			_ = authorizationStream.SendCancel()
		} else if stream != nil {
			_ = stream.SendCancel()
		}
		return nil
	}
}

// enqueuePrompt stages the current textarea text as a follow-up to drain when the
// running turn ends (see drainQueue). It trims the input; an empty line is a no-op
// (so enter on a blank prompt mid-run does nothing). At the cap it rejects with a
// muted "queue full" status and KEEPS the input (the user can edit it down or wait
// for a drain to free a slot). Otherwise it appends, resets the input, sets a
// "queued (N)" status, and re-syncs the palette (afterInputEdit) so the dropdown
// closes now that the "/" line is gone.
//
// Crucially it does NOT open a stream or send anything in local-queue mode — the
// queued text becomes a real prompt only when drainQueue later hands it to
// submitPrompt (the existing send path, which maps to the server-side
// StartRunContent reopen). In steer mode it sends only model-facing input; callers
// intercept bare local built-ins before reaching this path. There is no second send
// path.
func (m Model) enqueuePrompt() (tea.Model, tea.Cmd) {
	text := strings.TrimSpace(m.prompt.Value())
	if text == "" && len(m.stagedMedia) == 0 && len(m.stagedPastes) == 0 {
		return m, nil
	}
	// Native multimodal steer is enabled by the single steer capability. When it
	// is runtime-disabled, the local queue owns all mid-run text and media.
	if m.caps.Steer && m.stream != nil {
		return m.sendSteer(text)
	}
	if len(m.queued) >= maxQueued {
		m.statusMsg = m.deps.Theme.Style("muted").Render(fmt.Sprintf("queue full (%d)", maxQueued))
		return m, nil
	}
	prepared, media, hadPastes, hadStaged, err := m.preparePromptContent(text)
	if err != nil {
		m.conv.addError("attach: " + err.Error())
		m.refreshView()
		return m, nil
	}
	combined := appendMedia(m.queuedMedia, media)
	if err := combined.CheckAggregateCaps(); err != nil {
		m.conv.addError("attach: " + err.Error())
		m.refreshView()
		return m, nil
	}
	if prepared == "" && len(media.Parts) == 0 {
		return m, nil
	}
	m.queued = append(m.queued, prepared)
	m.queuedMedia = combined
	if hadStaged {
		m.stagedMedia = nil
		m.nextMediaN = 0
	}
	if hadPastes {
		m.stagedPastes = nil
		m.nextPasteN = 0
	}
	m.prompt.Reset()
	m.statusMsg = m.deps.Theme.Style("muted").Render(fmt.Sprintf("queued (%d)", len(m.queued)))
	m.refreshView()
	return m.afterInputEdit(nil)
}

func appendMedia(a, b client.MediaResult) client.MediaResult {
	out := a
	out.Parts = append(slices.Clone(a.Parts), b.Parts...)
	out.Descriptors = append(slices.Clone(a.Descriptors), b.Descriptors...)
	return out
}

func filterStaged(draft string, in map[string]stagedAttachment) map[string]stagedAttachment {
	var out map[string]stagedAttachment
	for marker, value := range in {
		if strings.Contains(draft, marker) {
			if out == nil {
				out = make(map[string]stagedAttachment)
			}
			out[marker] = value
		}
	}
	return out
}

func filterPastes(draft string, in map[string]string) map[string]string {
	var out map[string]string
	for marker, value := range in {
		if strings.Contains(draft, marker) {
			if out == nil {
				out = make(map[string]string)
			}
			out[marker] = value
		}
	}
	return out
}

// sendSteer collapses the textarea text into ONE steer bundle and sends it on
// the live Converse stream with a FRESH client-minted message_id. A bundle is
// always a complete replacement: an in-flight bundle is CANCELED first (the ↑
// edit-back path, cancel-then-recompose), so the old text is never merged onto
// the new frame — resend carries exactly what the user edited, with a new id.
// The lifecycle then advances ONLY on the server's authoritative steer.outcome
// ack / steer drain echo (updateStreamSecondary), correlated BY the minted id —
// never assumed client-side. The textarea is reset and the card moves to the
// pending state.
func (m Model) sendSteer(text string) (tea.Model, tea.Cmd) {
	draft := m.prompt.Value()
	prepared, media, hadPastes, hadStaged, err := m.preparePromptContent(text)
	if err != nil {
		m.conv.addError("attach: " + err.Error())
		m.refreshView()
		return m, nil
	}
	if prepared == "" && len(media.Parts) == 0 {
		return m, nil
	}
	combined := media
	if m.steer != nil {
		for _, send := range m.steer.Sends {
			combined = appendMedia(send.Media, combined)
		}
	}
	if err := combined.CheckAggregateCaps(); err != nil {
		m.conv.addError("attach: " + err.Error())
		m.refreshView()
		return m, nil
	}
	staged := filterStaged(draft, m.stagedMedia)
	pastes := filterPastes(draft, m.stagedPastes)
	if hadStaged {
		m.stagedMedia = nil
	}
	if hadPastes {
		m.stagedPastes = nil
	}
	text = prepared
	// A new send mints a fresh id and appends to the ordered queue; the WIRE
	// carries ONLY this line's fragment (the engine appends it to the pending
	// bundle — re-sending the full merged text would re-append drained text, the
	// duplication bug). The bundle's display Text re-joins from the whole queue.
	id := m.mintSteerID()
	if m.steer == nil {
		m.steer = &steerState{Phase: steerPending}
	}
	m.steer.Sends = append(m.steer.Sends, steerQueuedSend{ID: id, Text: text, Draft: draft, Media: media, Staged: staged, Pastes: pastes})
	m.steer.Text = joinSteerSends(m.steer.Sends)
	stream := m.stream
	sendMsg := func() tea.Msg {
		if err := stream.SendSteer(text, media, id); err != nil {
			return authStreamErr(m, err)
		}
		return nil
	}
	m.prompt.Reset()
	m.statusMsg = m.deps.Theme.Style("muted").Render("steering…")
	m.refreshView()
	return m, sendMsg
}

// wantsEditBack reports whether msg is the EditBack key (↑) pressed on an EMPTY
// input line with a non-empty queue — the precondition for pulling the merged queue
// back into the textarea (see editBackQueue). Gating on empty input keeps ↑ a plain
// textarea/scroll key over a draft. Shared by onRunningKey and onIdleKey so the
// guard reads as one predicate in each switch (and keeps their cyclomatic complexity
// under the cap).
func (m Model) wantsEditBack(msg tea.KeyPressMsg) bool {
	if !key.Matches(msg, m.keys.EditBack) || strings.TrimSpace(m.prompt.Value()) != "" {
		return false
	}
	// Steer mode: ↑ pulls the COMBINED in-flight steer back for editing (the
	// merged message, not a queue). Steer state takes precedence — with steer
	// armed mid-run the local queue is never populated (steer owns mid-run input).
	if m.steer != nil && (m.steer.Phase == steerPending || m.steer.Phase == steerSent) {
		return true
	}
	return len(m.queued) > 0
}

// editBackQueue is the inverse of enqueuePrompt: it pulls the whole staged queue
// back into the textarea (merged by queueMergeSep) so the user can revise it, and
// CLEARS the queue + any pause. It is non-destructive — bound to ↑ on an EMPTY input
// line with a non-empty queue (see onRunningKey / onIdleKey), distinct from Escape:
// during a run Escape cancels directly, while an idle paused queue may be dropped.
// It sends nothing on the queue path: the merged
// text is now an ordinary draft the user edits and (re)submits or (re)enqueues.
// Callers gate on the empty-input / non-empty-queue precondition, so this assumes
// m.queued is non-empty.
func (m Model) editBackQueue() (tea.Model, tea.Cmd) {
	// Steer mode (task-10 queue model): ↑ CANCELS the outstanding bundle WITHOUT
	// waiting for the ack and pulls the whole not-yet-drained text back into the
	// textarea as ONE editable blob. The old frame is NEVER left live to be merged
	// onto the resend (the edit-back duplication bug): the steer_cancel rides the
	// stream first, the draft is immediately editable, and the resend collapses
	// the edited blob into ONE new bundle with a NEW message_id. A drained id is
	// burned — after the echo there is no bundle to edit (wantsEditBack gates).
	if m.steer != nil && len(m.steer.Sends) > 0 {
		stream := m.stream
		id := m.steer.watermarkID()
		parts := make([]string, 0, len(m.steer.Sends))
		for _, send := range m.steer.Sends {
			parts = append(parts, send.Draft)
			if m.stagedMedia == nil && len(send.Staged) > 0 {
				m.stagedMedia = make(map[string]stagedAttachment)
			}
			maps.Copy(m.stagedMedia, send.Staged)
			if m.stagedPastes == nil && len(send.Pastes) > 0 {
				m.stagedPastes = make(map[string]string)
			}
			maps.Copy(m.stagedPastes, send.Pastes)
		}
		draft := strings.Join(parts, queueMergeSep)
		m.steer = nil
		m.prompt.Rewrite(draft)
		m.statusMsg = m.deps.Theme.Style("muted").Render("steer pulled back for editing — resend to replace")
		m.refreshView()
		cancel := func() tea.Msg {
			if stream != nil {
				_ = stream.SendSteerCancel(id)
			}
			return nil
		}
		return m.afterInputEdit(cancel)
	}
	m.prompt.Rewrite(strings.Join(m.queued, queueMergeSep))
	m.queued = nil
	m.pendingPromptMedia = m.queuedMedia
	m.queuedMedia = client.MediaResult{}
	m.queuePaused = ""
	m.statusMsg = m.deps.Theme.Style("muted").Render("queue pulled back for editing")
	m.refreshView()
	return m.afterInputEdit(nil)
}

// onIdleKey handles keys while idle: enter submits the prompt, shift+enter (and
// ctrl+j) inserts a newline, everything else feeds the textarea (or scrolls).
//
// The slash-command palette is woven in BEFORE the textarea path: while it is
// open it claims ↑/↓ (move selection), tab/enter (complete), and esc (dismiss)
// so those keys drive completion instead of the normal idle bindings. When it is
// closed every key falls through unchanged, and after any key that may have
// edited the input the palette is re-synced (open/filter/fetch) from the new
// content — so it appears the moment the input becomes "/…" and tracks the
// typed prefix.
func (m Model) onIdleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.palette.open {
		if mm, cmd, handled := m.onPaletteKey(msg); handled {
			return mm, cmd
		}
	}
	// The @-mention menu (mutually exclusive with the palette) claims the same
	// navigation/complete keys while it is open.
	if m.mention.open {
		if mm, handled := m.onMentionKey(msg); handled {
			return mm, nil
		}
	}
	switch {
	case m.wantsEditBack(msg):
		// ↑ on an EMPTY input line with staged follow-ups (a PAUSED queue held after a
		// non-clean stop, or a queue lingering at idle) pulls the merged queue back into
		// the textarea for editing (non-destructive; Escape clears a paused queue). Placed
		// before Submit/the textarea default so it wins the empty-input case in both paused
		// and idle states.
		return m.editBackQueue()
	case key.Matches(msg, m.keys.Help) && strings.TrimSpace(m.prompt.Value()) == "":
		// "?" is printable: open help only on an empty prompt so "?" in prose still
		// inserts literally. The overlay claims the keyboard via the m.showHelp gate
		// in onKey; blur the input while it is up.
		m.showHelp = true
		m.helpScroll = 0
		m.prompt.Blur()
		return m, nil
	case key.Matches(msg, m.keys.MCPPanel):
		return m.runMCP()
	case key.Matches(msg, m.keys.Resources):
		return m.runMCPResources()
	case key.Matches(msg, m.keys.Prompts):
		return m.runMCPPrompts()
	case key.Matches(msg, m.keys.Agents):
		return m.openAgents()
	case key.Matches(msg, m.keys.Effort) && m.deps.DebugTarget == "":
		// f7 opens the /effort reasoning-effort picker — the same surface the
		// /effort command opens (runEffort → openEffort). openEffort self-gates on
		// idle + caps.ModelSelection, so when model selection is unavailable this
		// returns the model unchanged and the key never opens an empty picker.
		return m.openEffort()
	case key.Matches(msg, m.keys.Cancel) && m.queuePaused != "":
		// A run ended on a non-clean stop with staged follow-ups still queued (the
		// paused state). Escape drops that paused queue; it never clears a draft.
		m.queued = nil
		m.queuedMedia = client.MediaResult{}
		m.queuePaused = ""
		m.statusMsg = m.deps.Theme.Style("muted").Render("queue cleared")
		m.refreshView()
		return m, nil
	case physicalEscape(msg):
		return m.onIdleDoubleEscape(msg)
	case key.Matches(msg, m.keys.Newline):
		m.prompt.InsertNewline()
		return m.afterInputEdit(nil)
	case key.Matches(msg, m.keys.Submit):
		return m.onIdleSubmit()
	case key.Matches(msg, m.keys.ScrollU), key.Matches(msg, m.keys.ScrollD),
		key.Matches(msg, m.keys.ScrollTop), key.Matches(msg, m.keys.ScrollBottom):
		return m.onScrollKey(msg)
	default:
		cmd := (&m).updatePromptKey(msg)
		return m.afterInputEdit(cmd)
	}
}

func (m Model) onIdleDoubleEscape(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if msg.IsRepeat || !m.doubleEscapeEligible() {
		return m, nil
	}
	if m.doubleEscapeArmed {
		if !m.doubleEscapeReleased {
			return m, nil
		}
		m.doubleEscapeArmed = false
		m.doubleEscapeReleased = false
		return m.clearPrompt()
	}
	m.doubleEscapeArmed = true
	m.doubleEscapeReleased = false
	m.doubleEscapeGen++
	return m, m.doubleEscapeExpiryCmd(m.doubleEscapeGen)
}

// onIdleSubmit handles enter at idle, in priority order (extracted from onIdleKey to
// keep its cyclomatic complexity under the cap):
//   - RETRY a failed restart-now re-create: in the recoverable state (restartFailed +
//     no session) enter on an EMPTY line re-fires the RECOVERABLE create path
//     (restartOnModelCmd with an empty oldID — no CloseSession) carrying the pending
//     selection (m.createModelSelection). Routing through restartOnModelCmd (NOT the generic
//     createSessionCmd) is what keeps a re-FAILED retry recoverable: a re-failure
//     emits restartFailedMsg again (looping back to this same recoverable state),
//     never client.ConnectErrMsg → phaseFatal. restartFailed is NOT cleared eagerly —
//     the resolving msg owns its lifecycle (SessionReadyMsg clears it on success;
//     restartFailedMsg re-sets it on a re-failure). The in-flight phaseConnecting
//     window swallows idle keys, so the un-cleared flag can't misfire meanwhile.
//   - RETRY a failed /effort FORK (restartFailedForkID != ""): re-fire the FORK from
//     the surviving source session (switchEffortCmd), NOT restartOnModelCmd — the
//     source session is still open, and re-forking PRESERVES the transcript where a
//     create-fresh retry would wipe it (the exact thing the fork-resume switch exists
//     to prevent). The effort rides m.createModelSelection (the switch applied it
//     synchronously before the fork failed).
//   - RESUME a paused queue: enter on an EMPTY line fires the next staged prompt.
//   - otherwise a normal submitPrompt (a no-op on an empty sessionID).
func (m Model) onIdleSubmit() (tea.Model, tea.Cmd) {
	empty := strings.TrimSpace(m.prompt.Value()) == ""
	if m.restartFailed && m.sessionID == "" && empty {
		m.phase = phaseConnecting
		m.statusMsg = "retrying — reconnecting…"
		m.refreshView()
		// m.sp.Tick re-arms the spinner for the idle→connecting transition (the
		// phase-gated TickMsg handler dropped the chain at idle).
		if m.restartFailedForkID != "" {
			return m, tea.Batch(m.switchEffortCmd(m.restartFailedForkID, m.createModelSelection), m.sp.Tick)
		}
		return m, tea.Batch(m.restartOnModelCmd("", m.createModelSelection), m.sp.Tick)
	}
	if m.queuePaused != "" && len(m.queued) > 0 && empty {
		return m.resumeQueue()
	}
	if m.pendingMode != "" && strings.TrimSpace(m.prompt.Value()) != "" {
		m.statusMsg = m.deps.Theme.Style("warning").Render("mode " + m.pendingMode + " is still pending — press " + firstKey(m.keys.Submit, "enter") + " again after it applies")
		return m, nil
	}
	return m.submitPrompt()
}

// onPaletteKey handles keys while the slash-command palette is open. It returns
// handled=false for keys the palette does not claim, so the caller falls through
// to the normal idle handling (and the key still reaches the textarea). Most of
// the palette's actions only mutate model state (no command); the exception is
// enter/tab over a BUILT-IN row, which runs the built-in directly (it may issue
// a command, e.g. opening an overlay).
func (m Model) onPaletteKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd, bool) {
	switch msg.String() {
	case keyMenuUp:
		m.paletteMoveUp()
		return m, nil, true
	case keyMenuDown:
		m.paletteMoveDown()
		return m, nil, true
	case keyMenuTab, keyMenuEnter:
		if mm, cmd, ran := m.dispatchSelectedBuiltin(); ran {
			return mm, cmd, true
		}
		return m.paletteComplete(), nil, true
	case keyMenuDismiss:
		return m.paletteDismiss(), nil, true
	}
	return m, nil, false
}

// onMentionKey handles keys while the @-mention file menu is open. Like
// onPaletteKey it reports handled=false for keys it does not claim (so the key
// still reaches the textarea). Unlike onPaletteKey it issues no command —
// completing a mention only rewrites the input, never runs a built-in — so it
// returns just (Model, handled). up/down move the selection; tab/enter complete
// the highlighted path; esc dismisses. The two menus never coexist (mutually
// exclusive tokens), so the caller routes to whichever is open.
func (m Model) onMentionKey(msg tea.KeyPressMsg) (Model, bool) {
	switch msg.String() {
	case keyMenuUp:
		m.mentionMoveUp()
		return m, true
	case keyMenuDown:
		m.mentionMoveDown()
		return m, true
	case keyMenuTab, keyMenuEnter:
		return m.mentionComplete(), true
	case keyMenuDismiss:
		return m.mentionDismiss(), true
	}
	return m, false
}

// dispatchSelectedBuiltin validates that the selected palette row is a builtin and
// dispatches the equivalent canonical bare command. Non-builtin workspace rows
// return ran=false so the caller can complete them into the model-facing input.
func (m Model) dispatchSelectedBuiltin() (tea.Model, tea.Cmd, bool) {
	if !m.palette.open || m.palette.cursor >= len(m.palette.filtered) {
		return m, nil, false
	}
	row := m.palette.filtered[m.palette.cursor]
	if !row.Builtin {
		return m, nil, false
	}
	// Argument-taking built-ins complete into the editor so the operator can
	// supply their required input; dispatching /title here would incorrectly run
	// its bare read action.
	if b, ok := builtinByName(m.caps, m.wiredCollaborators(), row.Name); ok && b.acceptsArgs {
		return m, nil, false
	}
	return m.dispatchBareBuiltin("/" + row.Name)
}

// afterInputEdit re-syncs the palette from the (possibly changed) textarea
// content and batches the palette's fetch command with cmd (the textarea's own
// command, e.g. a cursor blink). It is the single funnel every idle key path that
// edits the input runs through, so the palette can never get out of step with the
// input.
func (m Model) afterInputEdit(cmd tea.Cmd) (tea.Model, tea.Cmd) {
	mm, fetch := m.syncPalette()
	// The @-mention menu syncs from the SAME input edit. It is mutually exclusive
	// with the palette (mentionToken returns false for a "/" line and for a
	// multi-line input), so at most one opens; the sync is synchronous (no fetch).
	mm = mm.syncMention()
	if fetch == nil {
		return mm, cmd
	}
	if cmd == nil {
		return mm, fetch
	}
	return mm, tea.Batch(cmd, fetch)
}

var errStartupRunEntry = &sessionTranscriptError{"the chat could not be attached for a new turn"}

func (m Model) failStartupRunEntry() Model {
	m = m.endRun("")
	m = m.resetDocumentProjection()
	m.conv = conversationFromTranscript(m.deps.Resume.Transcript.Messages)
	m.modal = &sessionsState{
		selected:        m.deps.Resume.Row,
		inspect:         true,
		loadErr:         errStartupRunEntry,
		view:            sessionsTranscript,
		deps:            (&m).surfaceDeps(),
		activeSessionID: m.sessionID,
		transcript:      conversationFromTranscript(m.deps.Resume.Transcript.Messages),
		transcriptStuck: true,
		transcripter:    m.deps.Transcript,
	}
	m.phase = phaseReplay
	m.startupFirstPromptPending = false
	m.startupRunEntryFailed = true
	m.prompt.Rewrite(m.startupRetryPrompt)
	m.prompt.Blur()
	m.refreshView()
	return m
}

// preparePromptContent expands paste placeholders, file mentions, and staged
// media without mutating their stores. Callers clear the stores only after this
// returns successfully, so rejection keeps the draft and attachment bytes intact.
func (m Model) preparePromptContent(text string) (string, client.MediaResult, bool, bool, error) {
	hadPastes := len(m.stagedPastes) > 0
	if hadPastes {
		text = strings.TrimSpace(expandPastePlaceholders(text, m.stagedPastes))
	}
	var media client.MediaResult
	if files := attachableMentions(m.deps.Workspace, text); len(files) > 0 {
		res, err := client.ExpandMentions(files, m.caps)
		if err != nil {
			return "", client.MediaResult{}, hadPastes, false, err
		}
		media = res
		for _, body := range res.InlineText {
			text = strings.TrimSpace(text + "\n\n" + body)
		}
	}
	hadStaged := len(m.stagedMedia) > 0
	for _, marker := range survivingMarkers(text, m.stagedMedia) {
		sa := m.stagedMedia[marker]
		part, desc, err := client.StageClipboardImage(sa.mime, sa.data, m.caps)
		if err != nil {
			return "", client.MediaResult{}, hadPastes, hadStaged, err
		}
		media.Parts = append(media.Parts, part)
		media.Descriptors = append(media.Descriptors, desc)
		text = stripMarker(text, marker)
	}
	if err := media.CheckAggregateCaps(); err != nil {
		return "", client.MediaResult{}, hadPastes, hadStaged, err
	}
	return strings.TrimSpace(text), media, hadPastes, hadStaged, nil
}

// recoverPrompt restores a text-only prompt only while it still belongs to this
// exact run. An existing draft is user-authored replacement state and wins.
func (m *Model) recoverPrompt(authoritative bool) {
	r := m.promptRecovery
	if r == nil || r.sessionID != m.sessionID || r.streamGen != m.streamGen {
		m.promptRecovery = nil
		return
	}
	if m.prompt.Value() != "" {
		m.promptRecovery = nil
		return
	}
	m.prompt.Rewrite(r.text)
	if !authoritative {
		m.promptRecovery = nil
	}
}

// submitPrompt opens a fresh Converse run for the textarea text, sends the
// mandatory prompt frame, starts the reader goroutine, and arms WaitForMsg.
func (m Model) submitPrompt() (tea.Model, tea.Cmd) {
	text := strings.TrimSpace(m.prompt.Value())
	// A BARE built-in command line ("/clear", "/help", …) is dispatched here —
	// before addUser / stream-open — and dispatched to its Model action, so the
	// built-in text never reaches the model. A non-built-in "/…" falls through to
	// the normal send (preserving bare workspace-command invocation), and a
	// "/name arg" line has a space → commandPrefix is false → also falls through
	// (workspace commands expand server-side from the full line).
	if mm, cmd, handled := m.dispatchBareBuiltin(m.prompt.Value()); handled {
		return mm, cmd
	}
	if m.sessionID == "" {
		return m, nil
	}
	if m.compactPending {
		m.statusMsg = m.deps.Theme.Style("warning").Render("wait for session compaction to finish before sending a prompt")
		return m, nil
	}
	text, media, hadPastes, hadStaged, err := m.preparePromptContent(text)
	if err == nil {
		media = appendMedia(m.pendingPromptMedia, media)
		err = media.CheckAggregateCaps()
	}
	if err != nil {
		m.conv.addError("attach: " + err.Error())
		m.refreshView()
		return m, nil
	}
	if hadStaged {
		m.stagedMedia = nil
		m.nextMediaN = 0
	}
	if hadPastes {
		m.stagedPastes = nil
		m.nextPasteN = 0
	}
	m.pendingPromptMedia = client.MediaResult{}
	// A media-only prompt (empty text but at least one part) still sends — the proto
	// allows text OR parts, and the server enforces "at least one non-empty". A
	// truly empty submit (no text AND no parts) is the no-op early-return.
	if text == "" && len(media.Parts) == 0 {
		return m, nil
	}
	if m.startupAdopted {
		m.startupRetryPrompt = m.prompt.Value()
		m.startupFirstPromptPending = true
	}
	// This is a genuine new user turn, so it starts a fresh one-retry budget.
	// Automatic failed-step retry bypasses submitPrompt and therefore cannot re-arm itself.
	m.failedStepRetryTried = false
	m.promptRecovery = nil
	// Only a plain text prompt is recoverable. Attachments, media, and staged file
	// parts have one-shot lifecycle and must never be replayed implicitly.
	if text != "" && len(media.Parts) == 0 && !hadPastes && !hadStaged {
		m.promptRecovery = &promptRecovery{text: text, sessionID: m.sessionID, streamGen: m.streamGen + 1}
	}
	if len(media.Descriptors) > 0 {
		m.conv.addUserWithMedia(text, media.Descriptors)
	} else {
		m.conv.addUser(text)
	}
	// Seed the session title set-once from the first genuine prompt (mirrors the
	// server's session.SetTitle: the FIRST non-empty prompt sticks, later prompts
	// never overwrite it). An empty text (media-only / a bare built-in already
	// intercepted above) leaves it empty — the window title then self-heals from a
	// GetSession refetch on the carryover/fork/adopt paths (onResolvedModelMsg).
	if m.sessionTitle == "" {
		m.sessionTitle = text
		m.sessionTitleProvenance = "first-prompt"
	}
	m.prompt.Reset()
	// A fresh run clears any queue pause: whether this is the auto-drain (popAndSubmit
	// already cleared it) or a manual send while paused, the queue now gets a new
	// chance to drain at this run's clean completion, so it is no longer "paused".
	m.queuePaused = ""
	// The textarea stays FOCUSED while running so the user can type a follow-up and
	// enqueue it (see enqueuePrompt / onRunningKey). It used to Blur here to signal
	// "input disabled while running"; type-while-running supersedes that.
	m.phase = phaseRunning
	m.statusMsg = "running…"
	m.refreshView()

	return m.openRun(false, func(stream *client.Stream) error {
		return stream.SendPrompt(m.sessionID, text, media.Parts)
	})
}

// submitProceedPrompt opens a fresh Converse run carrying the plan-approved
// proceed message, mirroring submitPrompt's run-open tail (stream-open +
// SendPrompt + reader + spinner) WITHOUT the builtin/media/textarea logic. It is
// the INTERACTIVE counterpart to the server-side ApprovePlan RPC's atomic
// continuation (service.go:2593) and the headless auto-approve continuation
// (service.go:2945): the TUI sends the proceed text as an ordinary prompt so
// StartRunContent reopens the StopPlanApproved-completed session (loadAndReopen)
// and the CASE-1 mode→model rebuild picks up the FLIPPED mode → the agent begins
// executing on the execute model.
//
// ORDERING (issue #206 root cause): this MUST fire from the ResultMsg{Stop:
// "plan_approved"} handler (applyResult), NOT immediately after SendApproval in
// resolveAsk. The gRPC Converse handler IGNORES a second Prompt frame on the same
// stream (grpc.go readControl default arm), so the proceed cannot ride the
// approval's stream; it opens a FRESH stream. And StartRunContent's run-entry
// funnel requires the approval run to have fully TERMINATED (loadAndReopen drives
// the session idle; a still-registered live run blocks a new one) — ResultMsg is
// the client-side guarantee the approval run ended (endRun has cancelled/nilled
// the stream and settled phaseIdle). Firing earlier races the live approval run;
// firing here is provably post-terminal, exactly the gate the server's own
// autoApproveContinuation polls for (it waits for the terminal StopPlanApproved
// state before starting the continuation).
//
// The proceed text is recorded as an ordinary user turn (the execution driver),
// matching the ApprovePlan RPC path which records it server-side. It renders in
// the transcript so the operator sees what drove execution. The session's mode
// was flipped server-side at the approval terminal; the TUI header mode+model
// echo is refreshed by a concurrent RefreshResolvedModelCmd (also fired from
// applyResult on the same gate) whose ResolvedModelMsg result updates
// m.activeMode and m.resolvedSessionModel from the server's session snapshot.
func (m Model) submitProceedPrompt() (Model, tea.Cmd) {
	if m.sessionID == "" {
		return m, nil
	}
	// Record the proceed text as the user turn driving execution (ordinary
	// recorded history — same as the ApprovePlan RPC path records server-side).
	m.conv.addUser(planApprovedProceedText)
	m.queuePaused = ""
	m.phase = phaseRunning
	m.statusMsg = "running…"
	m.refreshView()

	return m.openRun(false, func(stream *client.Stream) error {
		return stream.SendPrompt(m.sessionID, planApprovedProceedText, nil)
	})
}

// startFailedStepRetry opens a new Converse stream whose first frame is RetryStart.
// It adds no user turn and consumes no queued text. Automatic retry leaves arbitrary
// compose text untouched; the ordinary built-in dispatcher consumes a typed /retry.
func (m Model) startFailedStepRetry() (Model, tea.Cmd) {
	m.phase = phaseRunning
	m.failedStepRetryRun = true
	m.failedStepRetryAuthoritative = false
	m.statusMsg = m.deps.Theme.Style("muted").Render("retrying failed model step…")
	m.refreshView()
	return m.openRun(true, func(stream *client.Stream) error {
		return stream.SendRetryStart(m.sessionID)
	})
}

// openRun owns the common one-Converse-run transport setup. firstFrame must send
// exactly one Prompt or RetryStart before any control frame.
func (m Model) openRun(retry bool, firstFrame func(*client.Stream) error) (Model, tea.Cmd) {
	m.streamGen++ // reserve this generation before OpenConverse so open failures are correlated too
	m.authorization.runningControlGen = 0
	runCtx, cancel := context.WithCancel(m.deps.Ctx)
	var stream *client.Stream
	var err error
	if bound, ok := m.deps.Conv.(SessionBoundConverser); ok {
		stream, err = bound.OpenConverseForSession(runCtx, m.sessionID)
	} else {
		stream, err = m.deps.Conv.OpenConverse(runCtx)
	}
	if err != nil {
		return m.handleOpenError(err, cancel, retry)
	}
	ch := make(chan tea.Msg, 64)
	m.stream = stream
	m.streamCh = ch
	m.cancelRun = cancel
	go stream.ReadLoop(runCtx, ch)

	gen := m.streamGen
	send := func() tea.Msg {
		if err := firstFrame(stream); err != nil {
			return streamMsg{gen: gen, msg: authStreamErr(m, err)}
		}
		return nil
	}
	return m, tea.Batch(send, m.waitCmd(), m.sp.Tick)
}

// afterEvent re-renders the conversation and re-arms the reader, returning the
// updated model and the re-arm command. Used by events that change the scrollback
// but don't need bespoke handling.
//
// It returns (Model, tea.Cmd) and is called as `return m.afterEvent()` — the
// well-defined form — rather than mutating via a pointer receiver and being called
// as `return m.afterEvent()`. In that operand form the Go spec leaves the order
// of evaluating the returned `m` value and the `m.afterEvent()` call UNSPECIFIED,
// so the refreshView (and its viewDirty clear) could be snapshotted into the return
// value BEFORE it runs — silently dropping the boundary force-flush the delta-
// coalescing relies on (deltas only mark dirty; turn/tool/result boundaries MUST
// flush). Returning the model makes every afterEvent boundary a genuine, deterministic
// flush on exactly the model the caller returns.
func (m Model) afterEvent() (Model, tea.Cmd) {
	m.refreshView()
	return m, m.waitCmd()
}

// streamMsg wraps one message pulled from a run's reader channel with the stream
// GENERATION that channel belonged to when the reader was armed. The reducer drops
// any streamMsg whose gen no longer matches m.streamGen (see update), so a reader
// left bound to an abandoned channel — leaked across a queue-drain or any future
// double-arm — cannot route its messages into the current run. It is the structural
// backstop behind the "exactly one reader per run" fan-in invariant.
type streamMsg struct {
	gen uint64
	msg tea.Msg
}

// waitCmd re-arms the fan-in command on the current run channel, tagging whatever it
// delivers with the current stream generation so a stale reader's output is dropped
// rather than misrouted (see streamMsg + update's gen check). Returns nil when no
// run is active (defensive).
func (m Model) waitCmd() tea.Cmd {
	if m.streamCh == nil {
		return nil
	}
	gen := m.streamGen
	read := client.WaitForMsg(m.streamCh)
	return func() tea.Msg { return streamMsg{gen: gen, msg: read()} }
}

// liveMsg wraps one message pulled from the LIVE session event feed
// (LiveStreamCmd) with the generation that channel belonged to when the
// reader was armed. It is the live-delivery analogue of streamMsg (live Converse
// run) and replayMsg (stored-session replay): the reducer drops any liveMsg whose
// gen no longer matches m.liveGen, so a stale reader left bound to an abandoned
// channel — torn down on a session switch / reset / a new arm for a different id —
// cannot route its messages into the current session. It is the structural backstop
// behind the "exactly one live subscription per active session" fan-in invariant.
type liveMsg struct {
	gen uint64
	msg tea.Msg
}

// waitLiveCmd re-arms the fan-in command on the current live channel, tagging
// whatever it delivers with the current live generation so a stale reader's output
// is dropped rather than misrouted (see liveMsg + updateLiveMsg's gen check).
// Returns nil when no live channel is armed (defensive). Parallel to waitCmd /
// waitReplayCmd.
func (m Model) waitLiveCmd() tea.Cmd {
	if m.liveCh == nil {
		return nil
	}
	gen := m.liveGen
	read := client.WaitForMsg(m.liveCh)
	return func() tea.Msg { return liveMsg{gen: gen, msg: read()} }
}

func (m Model) reduceLiveAuthRecovery(reason client.AuthReason) (tea.Model, tea.Cmd) {
	if reason == "" {
		return m, nil
	}
	// Tear down both readers before opening the existing connect surface. In
	// particular, an active Converse run is cancelled and its generation is
	// invalidated; auth recovery is not a completed run.
	if m.phase == phaseRunning || m.phase == phaseAwaitingApproval {
		m = m.endRun("")
	}
	m.disarmLiveFeed()
	m.liveReconnecting = false
	m.liveReconnectAttempt = 0
	m.liveReconnectErr = ""
	m.connect.err = "Authentication needs attention."
	m.connect.reason = reason
	m.connect.failedTarget = m.deps.Server
	m.connect.resumeSessionID = m.sessionID
	mm, cmd := m.openConnect()
	return mm, tea.Batch(m.refreshCmd(), cmd)
}

// updateLiveMsg applies the generation guard for the live feed fan-in, then
// reduces the inner msg into the live conversation. A message produced by the
// live feed's reader (waitLiveCmd) carries the generation of the channel it was
// read from; if that no longer matches the current live arm, the reader is bound
// to an ABANDONED channel, so the message is dropped and NOT re-armed — the stale
// reader dies with it. Parallel to onStreamMsg / updateReplayMsg.
//
// The inner msg is reduced into m.conv via m.update (which routes through
// updateStreamEvent/updateStreamSecondary — the SAME live conversation mutators
// a Converse stream event uses). A delivery event (DeliveryNoteMsg) renders as a
// delivery card (addDelivery); other event kinds (StreamClosedMsg, StreamErrMsg)
// are handled here. A StreamErrMsg on the live feed is transient — it does NOT end
// the run; the ui keeps the live convo and the next arm re-opens.
func (m Model) updateLiveMsg(sm liveMsg) (tea.Model, tea.Cmd) {
	if sm.gen != m.liveGen {
		return m, nil // stale reader — drop, do not re-arm
	}
	switch msg := sm.msg.(type) {
	case client.StreamErrMsg:
		if msg.AuthReason != "" {
			return m.reduceLiveAuthRecovery(msg.AuthReason)
		}
		m.liveContinuityAttempt++
		m.liveCh = nil
		m.liveStop = nil
		return m, (&m).startReconnect(msg.Err)
	case client.StreamClosedMsg:
		m.liveContinuityAttempt++
		m.liveCh = nil
		m.liveStop = nil
		return m, (&m).startReconnect(nil)
	case client.SessionTitleMsg:
		mm := m.onSessionTitle(msg)
		return mm, mm.waitLiveCmd()
	default:
		// A real event from the current reader proves the feed is healthy. Do not
		// let replay/catch-up events or probe success reset this sequence.
		m.liveContinuityAttempt = 0
		// A delivery event (or any other EventToMsg projection): reduce into the
		// live conversation via the SAME updateStreamEvent path a Converse stream
		// event would take (addDelivery + refreshView via afterEvent). Then
		// re-arm BOTH the Converse reader (from afterEvent's waitCmd, nil when
		// idle) AND the live feed reader (waitLiveCmd) so both streams keep
		// draining. DeliveryNoteMsg → addDelivery renders the delivery card.
		mm, cmd := m.updateStreamEvent(msg)
		if m2, ok := mm.(Model); ok {
			cmd = tea.Batch(cmd, m2.waitLiveCmd())
		}
		return mm, cmd
	}
}

// startReconnect kicks off the live-feed reconnect+catch-up loop (issue #387)
// for the current session, returning the waitReconnectCmd fan-in. It is a no-op
// (returns nil) when no live streamer is wired, the session is empty, or a
// reconnect is already in flight for this session (the no-duplicate-concurrent-
// subscriptions invariant — parallel to armLiveFeed's liveArmed guard). The
// caller (updateLiveMsg) has already cleared the live channel refs; this opens
// the reconnect loop's OWN channel + teardown, tagged with a fresh liveReconGen
// so a stale reconnect reader after a session switch is dropped. The catch-up
// uses m.deps.Replayer (the SAME *Client implements both LiveStreamer and
// SessionReplayer); when no replayer is wired the loop still retries the live
// reopen but skips the catch-up (the gap's deliveries recover on the next
// reconnect that DOES have a replayer, or via a manual /sessions reload).
// Pointer-receiver so the caller's model carries the armed reconnect channel.
func (m *Model) startReconnect(prevErr error) tea.Cmd {
	if m.deps.LiveStream == nil || m.sessionID == "" {
		return nil
	}
	// No duplicate concurrent reconnect: if one is already in flight for this
	// session, keep it. (The live reader is gone, so this is the sole recovery
	// path; a second start would open a second loop.)
	if m.liveReconCh != nil {
		return nil
	}
	m.liveReconGen++
	var replayer client.SessionReplayer
	if m.deps.Replayer != nil {
		replayer = m.deps.Replayer
	}
	ch, stop := client.ReconnectLiveCmdFromAttempt(m.deps.Ctx, m.deps.LiveStream, replayer, m.sessionID, m.liveContinuityAttempt-1)
	m.liveReconCh = ch
	m.liveReconStop = stop
	// Seed the degraded footer state immediately so the operator sees the feed
	// is down before the first LiveReconnectingMsg lands.
	m.liveReconnecting = true
	m.liveReconnectAttempt = 1
	if prevErr != nil {
		m.liveReconnectErr = prevErr.Error()
	} else {
		m.liveReconnectErr = ""
	}
	return m.waitReconnectCmd()
}

// reconnectMsg wraps one message pulled from the live-feed reconnect loop's
// channel with the live-reconnect generation that channel belonged to when the
// reader was armed. The reducer drops any reconnectMsg whose gen no longer
// matches m.liveReconGen, so a stale reader left bound to an abandoned reconnect
// loop (torn down on a session switch / reset / a re-arm after a successful
// reconnect) cannot route its messages into the current session. It is the
// reconnect analogue of liveMsg/replayMsg.
type reconnectMsg struct {
	gen uint64
	msg tea.Msg
}

// waitReconnectCmd re-arms the fan-in command on the current reconnect channel,
// tagging whatever it delivers with the current live-reconnect generation so a
// stale reader's output is dropped rather than misrouted (see reconnectMsg +
// updateReconnectMsg's gen check). Returns nil when no reconnect is active
// (defensive). Parallel to waitLiveCmd / waitReplayCmd.
func (m Model) waitReconnectCmd() tea.Cmd {
	if m.liveReconCh == nil {
		return nil
	}
	gen := m.liveReconGen
	read := client.WaitForMsg(m.liveReconCh)
	return func() tea.Msg { return reconnectMsg{gen: gen, msg: read()} }
}

// updateReconnectMsg applies the generation guard for the reconnect fan-in,
// then reduces the inner msg. A LiveReconnectingMsg updates the degraded footer
// state (attempt/error); a LiveReconnectedMsg clears it, tears down the
// reconnect loop (its job is done), and re-arms the live reader (armLiveFeed)
// off a FRESH live channel. A catch-up event msg (DeliveryNoteMsg/…) reduces
// through the SAME updateStreamEvent path a live event takes (so a delivery
// caught up here renders via addDelivery, deduped by FireID against the live
// set). The loop swallows its own terminal StreamClosed/StreamErr, so those
// reaching here mean the reconnect channel closed without a LiveReconnectedMsg
// (ctx cancelled / session switch): clear the degraded state and stop.
func (m Model) updateReconnectMsg(rm reconnectMsg) (tea.Model, tea.Cmd) {
	if rm.gen != m.liveReconGen {
		return m, nil // stale reader — drop, do not re-arm
	}
	if msg, ok := rm.msg.(client.StreamErrMsg); ok && msg.AuthReason != "" {
		return m.reduceLiveAuthRecovery(msg.AuthReason)
	}
	switch msg := rm.msg.(type) {
	case client.LiveReconnectingMsg:
		m.liveReconnecting = true
		m.liveReconnectAttempt = msg.Attempt
		if msg.Err != nil {
			m.liveReconnectErr = msg.Err.Error()
		} else {
			m.liveReconnectErr = ""
		}
		return m, m.waitReconnectCmd()
	case client.LiveReconnectedMsg:
		m.liveReconnecting = false
		m.liveReconnectAttempt = 0
		m.liveReconnectErr = ""
		// Tear down the reconnect loop (its ctx also cancels the probe stream
		// it opened) and clear its channel so the gen bump invalidates any
		// stale reader.
		(&m).disarmReconnect()
		// Re-arm the live reader off a FRESH live channel.
		liveCmd := (&m).armLiveFeed()
		if m.deps.Session != nil && m.sessionID != "" {
			return m, tea.Batch(liveCmd, client.RefreshResolvedModelCmd(m.deps.Ctx, m.deps.Session, m.sessionID))
		}
		return m, liveCmd
	case client.StreamClosedMsg, client.StreamErrMsg:
		// The reconnect channel closed without a LiveReconnectedMsg (ctx
		// cancelled — session switch / TUI exit). Clear the degraded state; a
		// subsequent arm (if any) re-opens the live feed fresh.
		m.liveReconnecting = false
		m.liveReconnectAttempt = 0
		m.liveReconnectErr = ""
		(&m).disarmReconnect()
		return m, nil
	default:
		// Catch-up is a FULL historical scan. Results never enter applyResult, so replay
		// cannot trigger automatic retry, mutate usage, or append another error card.
		if _, ok := msg.(client.ResultMsg); ok {
			return m, m.waitReconnectCmd()
		}
		// Delivery notes and pending authorization markers reduce through the normal
		// event path. The latter restores the actionable card after reconnect; its
		// durable payload contains correlation only, never a presentation URL.
		switch msg.(type) {
		case client.DeliveryNoteMsg, client.MCPAuthorizationMsg:
		default:
			return m, m.waitReconnectCmd()
		}
		mm, cmd := m.updateStreamEvent(msg)
		if m2, ok := mm.(Model); ok {
			cmd = tea.Batch(cmd, m2.waitReconnectCmd())
		}
		return mm, cmd
	}
}

// disarmReconnect tears down the live-feed reconnect loop and clears its state
// fields. Idempotent (safe to call when not armed). The gen bump invalidates
// any stale reader still draining into the now-defunct channel, so a late
// reconnect msg after a session switch is dropped WITHOUT triggering a
// reconnect for the old session.
func (m *Model) disarmReconnect() {
	if m.liveReconStop != nil {
		m.liveReconStop()
	}
	m.liveReconCh = nil
	m.liveReconStop = nil
	m.liveReconGen++
}

// armLiveFeed opens the LIVE session event stream for the current active session
// (m.sessionID) via m.deps.LiveStream if a live streamer is wired AND the session
// is non-empty AND the feed is not already armed for this id. It tears down any
// stale feed first (a session switch, or a re-arm after a transient close). The
// returned tea.Cmd carries the waitLiveCmd fan-in; the caller batches it with the
// caller's own cmds. Returns nil (no-op) when no streamer is wired, the session is
// empty, or the feed is already armed for this id.
func (m *Model) armLiveFeed() tea.Cmd {
	if m.deps.LiveStream == nil || m.sessionID == "" {
		return nil
	}
	if m.liveArmed == m.sessionID && m.liveCh != nil {
		return nil // already armed for this session
	}
	// Tear down any stale feed (session switch / re-arm).
	m.disarmLiveFeed()

	m.liveGen++
	ch, stop := client.LiveStreamCmd(m.deps.Ctx, m.deps.LiveStream, m.sessionID)
	m.liveCh = ch
	m.liveStop = stop
	m.liveArmed = m.sessionID
	return m.waitLiveCmd()
}

// disarmLiveFeed tears down the live feed reader goroutine and clears the live
// state fields. Idempotent (safe to call when not armed). The gen bump
// invalidates any stale reader still draining into the now-defunct channel. It
// ALSO disarms the reconnect loop (issue #387): a session switch abandons the
// old session's live recovery, and the liveReconGen bump invalidates any stale
// reconnect reader so it cannot route a late LiveReconnectingMsg (or a catch-up
// event) into the fresh session.
func (m *Model) disarmLiveFeed() {
	if m.liveStop != nil {
		m.liveStop()
	}
	m.liveCh = nil
	m.liveStop = nil
	m.liveGen++ // invalidate any stale reader
	m.liveArmed = ""
	m.disarmReconnect()
}

// mediaDescriptors builds one human-readable descriptor per non-text media part of
// a replayed user prompt (UserPromptMsg.Parts), mirroring the live submit path's
// media.Descriptors so addUserWithMedia renders the same "📎 …" placeholder lines.
// Text/embedded/structured parts are already represented in the flattened Text and
// are skipped here (as they are on the live path).
func mediaDescriptors(parts []client.ContentBlock) []string {
	var out []string
	for _, p := range parts {
		switch p.Kind {
		case client.ContentBlockImage, client.ContentBlockAudio:
			if p.URL != "" {
				out = append(out, string(p.Kind)+" ("+p.URL+")")
			} else if p.MimeType != "" {
				out = append(out, p.MimeType+" (inline)")
			} else {
				out = append(out, string(p.Kind)+" (inline)")
			}
		}
	}
	return out
}

// compactionArchiveNotice renders a bounded notice for a replayed
// EvCompactionArchive (CompactionArchiveMsg). The archive carries the FULL
// pre-compaction message slice (huge); the compacted tail the following events
// replay already represents the surviving conversation, so a one-line "history
// compacted — N turns archived" notice is the honest transcript record.
func compactionArchiveNotice(msg client.CompactionArchiveMsg) string {
	n := len(msg.Replaced)
	return "history compacted — " + plural(n, "turn") + " archived"
}

// onStartupRunEntryKey preserves the retry/back controls for a failed startup
// continuation. Ordinary transcript navigation is owned by sessionsState.
func (m Model) onStartupRunEntryKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if key.Matches(msg, m.keys.Close) {
		m.closeModal()
		m.startupRunEntryFailed = false
		m.phase = phaseIdle
		cmd := m.prompt.Focus()
		m.refreshView()
		return m, cmd
	}
	if msg.String() == "r" {
		m.closeModal()
		m.startupRunEntryFailed = false
		m.phase = phaseIdle
		m.prompt.Rewrite(m.startupRetryPrompt)
		return m.submitPrompt()
	}
	return m, nil
}

// refreshCmd is the command returned on the run-completion paths, after endRun +
// refreshView have already settled the final frame. It forces a full repaint
// (tea.ClearScreen erases and redraws from scratch) so the terminating frame the
// user is left reading is reconciled cleanly against any genuine diff dirt the
// differential renderer left behind while diffing a fast, reflowing stream. It does
// NOT heal the streaming scramble: that is a deterministic width-method layout
// error (glamour wraps on GraphemeWidth, the renderer paints on WcWidth — see
// render.go's normalizeEmojiWidth), so a re-paint just reproduces the same wrong
// layout. The scramble is fixed at the source by normalizeEmojiWidth; this repaint
// is retained only for ordinary stale cells. It fires once per run end, never per
// delta, so the cost is a single repaint when a turn settles.
func (Model) refreshCmd() tea.Cmd { return tea.ClearScreen }

// endRun tears down the current run: clears the stream/channel/cancel, returns to
// idle, and re-focuses input. The stop reason updates the status line.
func (m Model) endRun(stop string) Model {
	if m.cancelRun != nil {
		m.cancelRun()
		m.cancelRun = nil
	}
	m.stream = nil
	m.streamCh = nil
	m.streamGen++ // invalidate any reader still bound to the torn-down run's channel
	m.activeTool = ""
	m.closeModal()
	// The run is terminal: its steer inbox is closed, so any in-flight steer state is
	// over. A pending/sent (un-drained, un-acked) steer simply clears — it is lost
	// with the run (the honest best-effort contract); a promoted/retracted terminal
	// state already rendered its card. Clearing here keeps a dead run's steer card
	// from surviving into idle.
	m.steer = nil
	m.phase = phaseIdle
	// Ensure the input is focused now the run is done. With type-while-running the
	// input is already focused during a run, so this is a no-op on the common path;
	// it still matters as the single re-focus point after a blur the run may have
	// crossed (e.g. an overlay that blurred the textarea). Focus() returns a
	// cursor-blink cmd we don't thread back here; the next keypress re-arms the blink.
	_ = m.prompt.Focus()
	if stop != "" {
		text, slot := stopReasonLabel(stop)
		m.statusMsg = m.deps.Theme.Style(slot).Render(text)
	}
	m.refreshView()
	return m
}

// onMouseWheel lets an open modal handle the wheel, then always consumes it.
// The conversation viewport receives wheel events only while no modal is open.
func (m Model) onMouseWheel(msg tea.MouseWheelMsg) (tea.Model, tea.Cmd) {
	if m.modal != nil {
		cmd, _ := m.modal.HandleWheel(msg)
		return m, cmd
	}
	var cmd tea.Cmd
	m.vp, cmd = m.vp.Update(msg)
	// A wheel event changes the scroll offset: invalidate the vpView cache so the
	// next View() renders the new position rather than the stale pre-wheel output.
	m.rend.invalidateVPView()
	m.conversationView.observe(m.vp)
	return m, cmd
}

// onMouseMsg fans the four mouse message types out to their handlers. It is one
// switch case in update() (keeping update()'s cyclomatic complexity bounded) that
// re-discriminates the concrete mouse type here, where the dispatch logically
// belongs.
func (m Model) onMouseMsg(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.MouseWheelMsg:
		return m.onMouseWheel(msg)
	case tea.MouseClickMsg:
		return m.onMousePress(msg.Mouse())
	case tea.MouseMotionMsg:
		return m.onMouseMotion(msg.Mouse())
	case tea.MouseReleaseMsg:
		return m.onMouseRelease(msg.Mouse())
	default:
		return m, nil
	}
}

// mouseDebugLine formats a one-line mouse diagnostic for the footer overlay (gated
// on Deps.DebugMouse): the raw pointer cell, the layout offsets (convTopRow, the
// viewport YOffset and height), and the screenToContent mapping (ok + logical
// line/col). It is the durable instrument for diagnosing selection/coordinate issues
// — the wrong-line highlight that motivated this overlay shows up as a mismatch
// between the raw y and the mapped L. Pure read; no mutation.
func (m Model) mouseDebugLine(mo tea.Mouse) string {
	line, col, ok := screenToContent(m, mo.X, mo.Y)
	input, inputOK := inputRegionRect(m)
	return fmt.Sprintf("MOUSE raw x=%d y=%d | top=%d yoff=%d vph=%d | map ok=%v L%d C%d | input ok=%v [%d,%d)x[%d,%d)",
		mo.X, mo.Y, convTopRow(m), m.vp.YOffset(), m.vp.Height(), ok, line, col,
		inputOK && input.contains(mo.X, mo.Y), input.x0, input.x1, input.y0, input.y1)
}

// inputRegionRect returns the text-bearing cells of the prompt textarea. Its vertical
// placement comes from chrome(), the same layout source View and relayout use; its
// horizontal inset comes from the rail style that onResize uses to size the textarea.
func inputRegionRect(m Model) (cellRect, bool) {
	if m.width <= 0 || m.height <= 0 {
		return cellRect{}, false
	}
	_, below := m.chrome()
	y := m.height - sumHeight(below)
	for _, r := range below {
		if r.role == regionInput {
			rail := inputRailStyle(m.deps.Theme, m.inputMode())
			x := rail.GetBorderLeftSize() + rail.GetPaddingLeft()
			return cellRect{x0: x, x1: x + m.prompt.Width(), y0: y + inputRailPadTop, y1: y + inputRailPadTop + m.prompt.Height()}, true
		}
		y += r.height()
	}
	return cellRect{}, false
}

// onModalMousePress handles generic rendered-frame hits before the legacy
// approval branch. A modal owns misses as well, so clicks never reach selection.
func (m Model) onModalMousePress(mo tea.Mouse) (tea.Model, tea.Cmd, bool) {
	if m.modal == nil {
		return m, nil, false
	}
	if !mouseCaptureEnabled(m) {
		return m, nil, true
	}
	if mm, cmd, handled := m.dispatchSurfaceHit(mo.X, mo.Y); handled {
		return mm, cmd, true
	}
	return m, nil, true
}

// onMousePress handles a mouse button press. A LEFT press in the conversation
// region anchors a new selection (when selectable — no overlay owns the body, alt
// screen on); a RIGHT press copies the current selection if one exists (a
// convenience over the copy-on-release default). A press outside the conversation
// region (header/input/footer) or while an overlay owns the body starts nothing.
func (m Model) onMousePress(mo tea.Mouse) (tea.Model, tea.Cmd) {
	if m.deps.Debug || m.deps.DebugMouse {
		m.mouseDebug = m.mouseDebugLine(mo)
	}
	switch mo.Button {
	case tea.MouseRight:
		return m.copyActiveSelection()
	case tea.MouseMiddle:
		// Middle-click pastes the PRIMARY selection (issue #43). Mouse capture
		// (cell-motion tracking, see View) means the terminal never performs its
		// native middle-click paste — the app does it instead: an async primary-
		// selection read (shell backend preferred, OSC52 fallback — see
		// primaryPasteCmd) whose result routes through the bracketed-paste pipeline.
		// Gated exactly like a bracketed paste; position-independent (the paste goes
		// to the prompt input wherever the pointer is, matching terminal convention).
		// Deliberately does NOT touch clickCount/clickGen or the drag-selection
		// state — like right-click, it lives outside the multi-click sequence.
		if !m.pasteGateOpen() {
			return m, nil
		}
		return m, m.primaryPasteCmd()
	case tea.MouseLeft:
		if mm, cmd, handled := m.onModalMousePress(mo); handled {
			return mm, cmd
		}
		if cmd, handled := (&m).promptMousePress(mo); handled {
			return m, cmd
		}
		// The selectable gate AND the count logic sit here, BEFORE prompt selection is
		// cleared: a press while an overlay owns the body (or under --no-mouse/--inline)
		// starts nothing, preserves an existing prompt selection, and does not advance
		// the multi-click count (Req 9).
		if !selectable(m) {
			return m, nil
		}
		m.prompt.ClearSelection()
		line, col, ok := screenToContent(m, mo.X, mo.Y)
		if !ok {
			return m, nil
		}

		// Advance the multi-click count. Same LOGICAL position (line,col) within the
		// armed window → increment with wrap 1→2→3→1; a different position (or a lapsed
		// sequence, clickCount==0) → a fresh single click. Position equality is by
		// logical content position, NOT raw x/y (Req 5).
		if m.clickCount > 0 && line == m.clickL && col == m.clickC {
			m.clickCount++
			if m.clickCount > 3 {
				m.clickCount = 1
			}
		} else {
			m.clickCount = 1
			m.clickL, m.clickC = line, col
		}
		m.clickGen++
		disarm := m.clickDisarmCmd(m.clickGen)

		switch m.clickCount {
		case 2:
			m = m.wordSelect(line, col)
		case 3:
			m = m.lineSelect(line)
		default: // 1 (and a wrapped 4th press): today's zero-width anchor, no copy.
			m.sel = selection{active: true, anchorL: line, anchorC: col, headL: line, headC: col}
			snapshotSelection(&m)
			return m, disarm
		}

		// Word/line select copies immediately (copy-on-select), but only when the
		// gesture produced a non-empty span — a double-click past end-of-line, or a
		// triple-click on a blank line, yields an empty selection and copies nothing.
		if m.sel.empty() {
			return m, disarm
		}
		return m.clickCopy(disarm)
	default:
		return m, nil
	}
}

// clickCopy copies the current (word/line) selection immediately — the copy-on-select
// behaviour for double/triple-click — and BATCHES the multi-click disarm tick with
// the copy commands so the sequence still lapses on its own. It mirrors
// copySelection's status + OSC52 + shell-write fallback rather than reusing it,
// because copySelection's signature is shared with the right-click and
// release-on-drag paths (which carry no disarm) and must stay unchanged.
func (m Model) clickCopy(disarm tea.Cmd) (tea.Model, tea.Cmd) {
	mm, cmd := m.copySelection()
	return mm, tea.Batch(cmd, disarm)
}

// autoScrollInterval is the cadence of the edge-drag autoscroll tick (FIXED — the
// acceleration is purely lines-per-tick, see rampToLines, not a varying interval).
// ~50ms keeps a gentle first step controllable; a sustained hold ramps the
// lines-per-tick up (capped at maxAutoScrollLines) so a large region selects fast.
// The tick is a SELF-RE-ARMING one-shot driven only while a drag sits at an edge
// (m.sel.autoScroll != scrollNone), so it idles to zero the instant the drag leaves
// the edge, releases, or the content runs out.
const autoScrollInterval = 50 * time.Millisecond

// maxAutoScrollLines caps edge-autoscroll at 10 lines/tick — under half a typical
// viewport so one 50ms tick can never leap past a full screen (no overshoot). The
// interval stays fixed; acceleration is purely lines-per-tick.
const maxAutoScrollLines = 10

// rampToLines maps the consecutive-tick ramp counter to a lines-per-tick step. It is
// a pure, deterministic, monotonic-non-decreasing Fibonacci-ish curve that starts
// gentle (rampToLines(0)==1) and saturates at maxAutoScrollLines: 1,1,2,3,5,8,10,10…
// onAutoScroll increments the ramp THEN scrolls, so the OBSERVED per-tick deltas are
// 1,2,3,5,8,10,10,… — first contact is one line (via armAutoScroll's explicit n=1),
// then a sustained hold accelerates. Speed is a pure function of the integer ramp, so
// tests can drive N ticks and assert exact YOffset deltas with no wall clock.
func rampToLines(r int) int {
	a, b := 1, 1
	for i := 0; i < r; i++ {
		a, b = b, a+b
		if a >= maxAutoScrollLines {
			return maxAutoScrollLines
		}
	}
	if a > maxAutoScrollLines {
		return maxAutoScrollLines
	}
	return a
}

// autoScrollMsg is the edge-drag autoscroll tick. The handler scrolls a ramped number
// of lines (rampToLines, accelerating the longer the edge is held) in the stashed
// direction, extends the selection head to the newly-revealed edge line, and re-arms
// ITSELF only if still dragging at that edge with room to move.
type autoScrollMsg struct{}

// autoScrollCmd schedules one edge-drag autoscroll tick.
func (Model) autoScrollCmd() tea.Cmd {
	return tea.Tick(autoScrollInterval, func(time.Time) tea.Msg { return autoScrollMsg{} })
}

// onMouseMotion extends an active selection as the pointer is dragged with the
// button held. It is a no-op when no selection is active (a bare hover never starts
// one). When the pointer reaches the TOP edge (y <= top) or BOTTOM edge (y >=
// bottom) of the conversation region it ARMS edge-autoscroll: it scrolls one line
// that way now (first contact stays gentle), stashes the direction, and returns the
// self-re-arming tick so the view keeps scrolling (ACCELERATING the longer it is
// held — see onAutoScroll/rampToLines) and the selection keeps extending even while
// the pointer is held still at the edge. A motion back INSIDE the region disarms
// autoscroll and resets the acceleration ramp.
func (m Model) onMouseMotion(mo tea.Mouse) (tea.Model, tea.Cmd) {
	if m.deps.Debug || m.deps.DebugMouse {
		m.mouseDebug = m.mouseDebugLine(mo)
	}
	if (&m).promptMouseMotion(mo) {
		return m, nil
	}
	if !m.sel.active {
		return m, nil
	}
	// A real drag-extend invalidates any in-flight multi-click sequence (so the next
	// press after a drag is a fresh single click, not a phantom double). Placed AFTER
	// the active guard so a BARE hover (no held selection) leaves the count untouched.
	// Only clickCount is cleared — sel.autoScroll is the drag's own concern below.
	m.clickCount = 0
	top := convTopRow(m)
	if top < 0 {
		return m, nil
	}
	bottom := top + m.vp.Height() - 1
	switch {
	case mo.Y <= top:
		return m.armAutoScroll(scrollUp, mo.X)
	case mo.Y >= bottom:
		return m.armAutoScroll(scrollDown, mo.X)
	}
	// Inside the region: plain extend, and disarm any edge-autoscroll. Reset the
	// acceleration ramp too, so re-entering an edge restarts at the gentle 1-line
	// step (Req 4 — the inside-region path is the one disarm seam that does not go
	// through m.sel = selection{}, so the reset is explicit here).
	m.sel.autoScroll = scrollNone
	m.sel.autoScrollRamp = 0
	line, col, ok := screenToContent(m, mo.X, mo.Y)
	if !ok {
		return m, nil
	}
	m.sel.headL = line
	m.sel.headC = col
	snapshotSelection(&m)
	return m, nil
}

// armAutoScroll begins (or continues) edge-autoscroll in dir: it scrolls the
// viewport exactly ONE line (first contact stays gentle — acceleration is the held-
// tick concern of onAutoScroll, not the per-cell re-arm), extends the selection head
// to the now-edge content line at column-for-x, re-applies the highlight, re-derives
// auto-follow (an edge scroll is an explicit user scroll, exactly like a wheel/key),
// and returns the re-arming tick. If the viewport can't move further in dir (already
// at content top/bottom) it DISARMS — no scroll, no tick — so the ticker never spins
// at the content edge. It does NOT touch autoScrollRamp: onMouseMotion re-fires this
// per cell at the edge, so resetting the ramp here would defeat acceleration; the
// ramp is 0 on a fresh hold via the disarm-time reset instead.
//
// SINGLE-FLIGHT: it spawns a NEW tick loop ONLY when transitioning into an armed
// state FROM scrollNone. Terminal drag reporting (mode 1002/1003) emits a motion
// event on every cell change, so a drag jittering at the edge would otherwise spawn
// a fresh self-re-arming tick per event — multiple concurrent loops → runaway
// double/triple-speed autoscroll. The gate is `!= scrollNone` (NOT `== dir`): a
// loop is already running regardless of its direction, and the single loop reads
// m.sel.autoScroll fresh each tick, so a direction flip (up→down) is picked up by
// the existing loop without spawning a second. Gating on `== dir` would leave a
// stale opposite-direction loop alive on a flip, which then converts itself into a
// duplicate same-direction loop — the exact bug this guards against.
func (m Model) armAutoScroll(dir autoScrollDir, x int) (tea.Model, tea.Cmd) {
	m.sel.dragX = x // remember the column so the tick can keep the head aligned
	if !m.scrollLines(dir, 1) {
		m.sel.autoScroll = scrollNone // at the content edge: stop, don't re-arm
		m.extendHeadToEdge(dir, x)
		snapshotSelection(&m)
		return m, nil
	}
	alreadyRunning := m.sel.autoScroll != scrollNone
	m.sel.autoScroll = dir
	m.conversationView.observe(m.vp)
	m.extendHeadToEdge(dir, x)
	snapshotSelection(&m)
	if alreadyRunning {
		return m, nil // a tick loop is already live; it reads the new direction itself
	}
	return m, m.autoScrollCmd()
}

// onAutoScroll handles one edge-drag autoscroll tick. It no-ops unless a drag is
// STILL armed at an edge (release / motion-back-inside / esc-clear all set
// autoScroll back to scrollNone, so a pending tick from a prior arm dies here),
// then ADVANCES the ramp and scrolls rampToLines(ramp) lines (accelerating the
// longer the edge is held), extends the head, and re-arms — stopping when the
// content edge is reached (scrollLines returns false), where it also resets the ramp
// so the next hold restarts gentle. The ramp is touched ONLY here, never in
// armAutoScroll (which re-fires per cell and would reset acceleration each motion).
func (m Model) onAutoScroll() (tea.Model, tea.Cmd) {
	if !m.sel.active || m.sel.autoScroll == scrollNone {
		return m, nil
	}
	dir := m.sel.autoScroll
	m.sel.autoScrollRamp++
	n := rampToLines(m.sel.autoScrollRamp)
	if !m.scrollLines(dir, n) {
		m.sel.autoScroll = scrollNone // reached content top/bottom: stop re-arming
		m.sel.autoScrollRamp = 0      // and reset acceleration for the next hold
		m.extendHeadToEdge(dir, m.sel.dragX)
		snapshotSelection(&m)
		return m, nil
	}
	m.conversationView.observe(m.vp)
	m.extendHeadToEdge(dir, m.sel.dragX)
	snapshotSelection(&m)
	return m, m.autoScrollCmd()
}

// scrollLines moves the viewport n lines in dir and reports whether it actually
// moved (false when already at the content top/bottom). n is clamped to at least 1.
// A partially-clamped multi-line scroll (fewer lines remaining than n) still moves
// YOffset → returns true; the NEXT tick then moves 0 → false → disarm. That is how
// the loop lands exactly on the content edge and stops with no extra clamp math (Req
// 5). Uses the bubbles viewport ScrollUp/ScrollDown API, which already takes a count.
func (m *Model) scrollLines(dir autoScrollDir, n int) bool {
	if n < 1 {
		n = 1
	}
	before := m.vp.YOffset()
	switch dir {
	case scrollUp:
		m.vp.ScrollUp(n)
	case scrollDown:
		m.vp.ScrollDown(n)
	default:
		return false
	}
	changed := m.vp.YOffset() != before
	if changed {
		// Scroll offset changed: the vpView cache must be invalidated so the next
		// View() renders the new scroll position rather than the stale one.
		m.rend.invalidateVPView()
	}
	return changed
}

// extendHeadToEdge sets the selection head to the content line now at the scrolled
// edge (top line when scrolling up, bottom visible line when scrolling down) at the
// grapheme column under x — so the selection grows to cover the newly-revealed
// lines as the view scrolls. convTopRow/screenToContent are pure value-receiver
// reads, so passing *m to them is a read-only snapshot.
func (m *Model) extendHeadToEdge(dir autoScrollDir, x int) {
	top := convTopRow(*m)
	if top < 0 {
		return
	}
	y := top
	if dir == scrollDown {
		y = top + m.vp.Height() - 1
	}
	if line, col, ok := screenToContent(*m, x, y); ok {
		m.sel.headL = line
		m.sel.headC = col
	}
}

// onMouseRelease finalises a selection. An empty (anchor==head, e.g. a plain
// click) selection is cleared with no copy; a real conversation selection copies on
// release (the copy-on-select default). A release with no active selection is a no-op.
// Release always DISARMS edge-autoscroll (scrollNone) so any in-flight tick no-ops.
//
// Asymmetry note: a release uses screenToContent's clamp (a release outside the
// region still finalises the head at the nearest visible content), unlike motion,
// which routes an at-edge position into armAutoScroll. This is intentional —
// autoscroll only makes sense while the button is HELD; on release the drag is over,
// so we just snap the head to wherever the pointer last was and finish.
func (m Model) onMouseRelease(mo tea.Mouse) (tea.Model, tea.Cmd) {
	if (&m).promptMouseRelease(mo) {
		return m, nil
	}
	if !m.sel.active {
		return m, nil
	}
	wasAutoScroll := m.sel.autoScroll != scrollNone
	m.sel.autoScroll = scrollNone // the drag is over: kill any pending autoscroll tick
	m.sel.autoScrollRamp = 0      // self-reset acceleration so release is a true reset seam
	if !wasAutoScroll {
		if line, col, ok := screenToContent(m, mo.X, mo.Y); ok {
			m.sel.headL = line
			m.sel.headC = col
		}
	}
	if m.sel.empty() && m.sel.copied == "" && !wasAutoScroll {
		m.sel = selection{}
		// Do not let the next press re-install this completed selection's old projection.
		m.selBase = ""
		m.refreshView() // repaint the now-UNSTYLED content (no native highlight to clear)
		return m, nil
	}
	// Release is the one head-changing path that doesn't already run through
	// snapshotSelection; capture the identity snapshot and re-render the spliced
	// highlight for the final head before copying.
	snapshotSelection(&m)
	return m.copySelection()
}

// copySelection copies the VISIBLE (ansi-stripped) selected text via OSC52
// (tea.SetClipboard, the primary path) AND the best-effort shell-clipboard write
// (shellWriteCmd, batched). It sets a muted "copied N chars" status. An empty
// payload is a no-op (defensive — release already clears empties).
func (m Model) copySelection() (tea.Model, tea.Cmd) {
	if !m.sel.active {
		return m.copyPayload("")
	}
	return m.copyPayload(m.sel.copied)
}

// snapshotSelection records the selection's identity anchor and RE-SPLICES the
// highlight over the unstyled base content in place — the per-gesture (press / drag /
// edge-autoscroll) update. It REPLACES the old applySelectionHighlight: the highlight
// is no longer the viewport's native SetHighlights (which mis-placed it on ANSI-styled
// content — see styleSelection) but our own per-line splice. So there is no
// SetHighlights/ClearHighlights and no YOffset save/restore to neutralise an
// EnsureVisible scroll-jump — re-splicing the SAME-length content never moves YOffset.
//
// It works off m.selBase (the unstyled currently displayed conversation render,
// captured when the selection became active and refreshed by refreshView) rather
// than re-rendering the whole conversation, so a gesture does not rebuild the
// transcript or disturb the viewport's scroll/line geometry. A pending coalesced
// delta remains hidden until its ordinary render tick: snapshot and copy both use
// this displayed frame. The snapshot (selectedText against the unstyled base) is
// the identity anchor refreshView compares against to drop the selection on a
// reflow. A pointer receiver: it mutates m.vp in place.
func snapshotSelection(m *Model) {
	if !m.sel.active {
		return
	}
	// snapshotSelection calls vp.SetContent (re-splicing the highlight), so the
	// vpView cache must be invalidated so the next View() reflects the new content.
	m.rend.invalidateVPView()
	base := m.selBase
	frame := m.conversationView.frame
	if base == "" {
		// Defensive: no base captured (e.g. a test that set raw viewport content then
		// pointed a selection at it without a press). Adopt the current viewport content
		// as the base and remember it, so subsequent gestures re-splice cleanly.
		base = m.vp.GetContent()
		m.selBase = base
	}
	if !m.sel.snapshotLogical(frame, base) {
		// Some derived rows have no canonical text provenance. Keep their existing
		// gesture behaviour; a later replacement will still clear them rather than
		// claiming logical survival.
		m.sel.anchorPoint, m.sel.headPoint = selectionPoint{}, selectionPoint{}
		m.sel.snapshot = selectedText(base, m.sel)
		m.sel.copied = m.sel.snapshot
	}
	m.conversationView.replaceProjection(&m.vp, styleSelection(base, m.sel, m.deps.Theme.Style("selection")))
}

// clearSelection drops any active text selection (including a pending edge-
// autoscroll direction, so an in-flight tick no-ops). It is called from the SINGLE
// selectable→non-selectable chokepoint in Update (Req 8) and from the esc-clear
// path. A no-op when nothing is selected. Value-receiver-friendly: returns the
// mutated Model.
//
// IMPORTANT: it does NOT re-render. The highlight is now spliced into the content by
// styleSelection inside refreshView (not a native viewport highlight mutated in
// place), so dropping the selection requires a subsequent refreshView to re-render
// the UNSTYLED content — every clearSelection caller must follow with one (the esc
// path, the Update selectable→non-selectable chokepoint, overlay-open). The
// chokepoint's caller (Update) is the seam that guarantees a render: the per-message
// relayout/refresh runs after it. The esc path refreshViews explicitly.
//
// It also zeroes the multi-click sequence (clickCount): esc-clear and the
// non-selectable chokepoint both drop the selection but predate the multi-click
// machinery, so without this a sequence armed just before an esc / overlay-open
// could carry into the next press. The drag-invalidation, different-position reset,
// disarm tick, and resetSession already cover the cases that matter; this is the
// belt-and-suspenders symmetry that keeps "selection dropped ⟹ sequence dropped"
// true at every seam.
func (m Model) clearSelection() Model {
	if m.sel.active {
		m.sel = selection{} // zeroes autoScroll too
		m.selBase = ""      // drop the splice base so the next selection re-captures it
	}
	m.clickCount = 0
	return m
}

// shellWriteResultMsg is the (ignored) result of the best-effort shell-clipboard
// write. It carries the error so a future maintainer could surface it, but the
// handler is intentionally a silent no-op: OSC52 is the primary copy path and the
// copy is considered to have succeeded regardless, so a missing wl-copy/xclip/
// pbcopy/clip must never produce a scary status (per the copy UX: best-effort).
type shellWriteResultMsg struct{ err error }

// shellWriteCmd returns a command that mirrors payload into the OS clipboard via
// the platform binary (the OSC52 fallback). It is best-effort: a nil Clipboard
// collaborator or any backend error is swallowed into a no-op result. It runs off
// the update goroutine (it shells out), like the ctrl+v read.
func (m Model) shellWriteCmd(payload string) tea.Cmd {
	cb := m.deps.Clipboard
	if cb == nil {
		return nil
	}
	ctx := m.deps.Ctx
	return func() tea.Msg {
		err := cb.Write(ctx, "text/plain", []byte(payload))
		return shellWriteResultMsg{err: err}
	}
}

// primaryWriteCmd is the PRIMARY-selection twin of shellWriteCmd: it mirrors
// payload into the X11/Wayland primary selection (wl-copy --primary / xclip
// -selection primary) so a selection copied inside mecatui can be middle-click
// pasted elsewhere. Same best-effort contract — a nil Clipboard, a platform with
// no primary selection, or any backend error is swallowed into a no-op result.
func (m Model) primaryWriteCmd(payload string) tea.Cmd {
	cb := m.deps.Clipboard
	if cb == nil {
		return nil
	}
	ctx := m.deps.Ctx
	return func() tea.Msg {
		err := cb.WritePrimary(ctx, []byte(payload))
		return shellWriteResultMsg{err: err}
	}
}

// onScrollKey is the shared conversation-scroll handler used by both onIdleKey and
// onRunningKey: pgup/pgdn delegate to the viewport (which does its own scroll
// math), home/end jump to top/bottom, then conversationView observes the result.
func (m Model) onScrollKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	switch {
	case key.Matches(msg, m.keys.ScrollTop):
		m.vp.GotoTop()
	case key.Matches(msg, m.keys.ScrollBottom):
		m.vp.GotoBottom()
	default: // ScrollU / ScrollD
		m.vp, cmd = m.vp.Update(msg)
	}
	// Any scroll-key changes the scroll offset: invalidate the vpView cache so the
	// next View() renders the new position rather than the stale pre-scroll output.
	m.rend.invalidateVPView()
	m.conversationView.observe(m.vp)
	return m, cmd
}

// conversationContent produces the complete current projection, including the expanded
// changed-files appendix which is outside the conversation renderer's block rows.
func (m *Model) conversationContent() string {
	frame := m.rend.renderConversationFrame(&m.conv, m.expandTools)
	content := strings.Join(frame.lines, "\n")
	if m.expandTools {
		if list := m.rend.renderChangedFiles(m.conv.filesChanged); list != "" {
			content += "\n" + list
		}
	}
	return content
}

// refreshView re-renders the conversation into the viewport, keeping the view
// pinned to the bottom unless the user has scrolled up. It clears m.viewDirty, so
// every render path (afterEvent, endRun, onResize, the frame-cadence renderTickMsg)
// settles the flag — making "rendered ⟺ not dirty" an invariant and force-flushing
// the tail at every turn/tool/result/error boundary regardless of tick timing.
//
// It re-pins only in tail-follow mode. A scroll-up switches to anchored mode, so
// a streaming delta re-renders the growing content in place without yanking the
// view back to the bottom. Scrolling/jumping back to the bottom resumes tail follow.
//
// Cost shape: renderConversationFrame reuses cached block renders and the cached
// settled prefix; content post-processing still joins the current frame when needed.
// View() memoizes the viewport output for spinner-only frames.
func (m *Model) refreshView() {
	m.viewDirty = false
	// Any refreshView call changes the viewport content, so the vpView cache must be
	// invalidated — the caller may be a spinner-only frame that skips refreshView
	// entirely, in which case the vpView cache correctly serves the prior content.
	m.rend.invalidateVPView()
	frame := m.rend.renderConversationFrame(&m.conv, m.expandTools)
	// FAST PATH: the line-slice handoff. When no selection is active AND the
	// changed-files footer is not in play (it renders only under the global expand
	// toggle), feed vp.SetContentLines directly with the incrementally-joined line
	// slice — reusing the cached prefix of settled blocks and only building the
	// changed suffix. This skips the O(scrollback) Builder copy + strings.Split that
	// the string SetContent path forces on every streaming frame (the live tail
	// re-renders every token, so the whole-join memo never helps streaming). The
	// selection and footer paths both post-process the JOINED STRING, so they fall
	// back to the byte-identical string path below.
	if !m.sel.active && !m.expandTools {
		m.conversationView.replace(&m.vp, frame)
		return
	}
	content := strings.Join(frame.lines, "\n")
	if m.expandTools {
		if list := m.rend.renderChangedFiles(m.conv.filesChanged); list != "" {
			content += "\n" + list
			frame = frameWithAppendix(frame, content, m.conv.changedFilesAppendixID)
		}
	}
	// An active text selection is now rendered by US (styleSelection splices the
	// selection style into the content lines) rather than the viewport's native
	// SetHighlights — which mis-placed the highlight on ANSI-styled (glamour) content
	// because its parseMatches detects newlines at stripped offsets in the original
	// ANSI bytes (see styleSelection). This is the single content-render chokepoint,
	// so it covers every refreshView caller — the highlight survives a streaming
	// re-render (Req 2).
	//
	// A selection survives only when both logical endpoint contexts resolve and the
	// resulting ANSI-free copied text remains byte-identical. Reading anchors may
	// fall back; selections deliberately may not.
	if m.sel.active && ((m.sel.anchorPoint.blockID == 0 && selectedText(content, m.sel) != m.sel.snapshot) ||
		(m.sel.anchorPoint.blockID != 0 && !m.sel.resolveLogical(frame))) {
		*m = m.clearSelection()
	}
	if m.sel.active {
		// Record the UNSTYLED render as the splice base, so a subsequent gesture
		// (snapshotSelection) can re-splice the highlight in place without re-rendering
		// the whole conversation.
		m.selBase = content
		content = styleSelection(content, m.sel, m.deps.Theme.Style("selection"))
	}
	m.conversationView.replaceContent(&m.vp, content, frame)
}

// drainQueue MERGES staged follow-ups into ONE prompt only after a healthy
// terminal. It is called after endRun has settled the model back to idle.
//
// Every error terminal pauses and preserves the queue. Automatic recovery from an
// eligible failure is an exact RetryStart handled before this function; it never
// consumes future prompts. Once that retry ends healthily, this function resumes
// the existing FIFO drain behavior.
func (m Model) drainQueue(stop string) (tea.Model, tea.Cmd) {
	if len(m.queued) == 0 {
		m.queuePaused = ""
		return m, nil
	}
	if !shouldDrain(stop) {
		m.queuePaused = stop
		return m, nil
	}
	if m.phase != phaseIdle {
		return m, nil // belt-and-braces: never drain into a still-running model.
	}
	return m.popAndSubmit()
}

// popAndSubmit MERGES all staged follow-ups into ONE prompt (joined by
// queueMergeSep), clears the queue and any pause, and submits the merged text
// through the EXISTING submitPrompt path (server-side StartRunContent reopen) — the
// single shared body behind both the auto-drain (drainQueue) and the manual resume
// (resumeQueue). submitPrompt sets phaseRunning and opens a fresh stream. The whole
// queue drains in ONE step, so there is no re-entrant drain — the merged run's own
// ResultMsg finds an empty queue.
//
// The pendingMode guard is preserved exactly: with a mode switch still pending the
// merged text is placed in the textarea and NOT submitted (queuePaused="mode"), so
// the mode applies before the user presses enter to send it.
func (m Model) popAndSubmit() (tea.Model, tea.Cmd) {
	m.queuePaused = ""
	merged := strings.Join(m.queued, queueMergeSep)
	m.queued = nil
	m.pendingPromptMedia = m.queuedMedia
	m.queuedMedia = client.MediaResult{}
	m.prompt.Rewrite(merged)
	if m.pendingMode != "" {
		submit := firstKey(m.keys.Submit, "enter")
		m.statusMsg = m.deps.Theme.Style("warning").Render("mode " + m.pendingMode + " will apply before the queued prompt — press " + submit + " to continue")
		m.queuePaused = "mode"
		return m, nil
	}
	return m.submitPrompt()
}

// resumeQueue is the MANUAL counterpart to the auto-drain: it fires the merged
// staged follow-ups when the user presses enter on an empty line while the queue is
// paused (a run ended on a non-clean stop; see onIdleKey). It shares popAndSubmit
// with drainQueue so the two triggers — automatic on a healthy/transient completion,
// manual on resume — go through one body and one send path (the whole queue merges
// into one prompt). Callers gate on a non-empty, paused queue; this assumes
// m.queued is non-empty.
func (m Model) resumeQueue() (tea.Model, tea.Cmd) {
	return m.popAndSubmit()
}

// shouldDrain reports whether a terminal stop reason should auto-fire the next
// staged follow-up. It drains on a HEALTHY stop — one where the model was either
// done or merely hit a SIZE bound, so feeding the next staged prompt simply
// continues the work the user lined up:
//
//   - ""        — treated as a clean end_turn throughout (cf. stopReasonLabel).
//   - end_turn  — the model finished without requesting more tools.
//   - max_turns / max_tool_calls / budget — the run hit a per-run budget. The model was
//     healthy; it just ran out of room. A queued follow-up ("continue", or the next
//     step) is exactly what's wanted here, and firing it reopens the session with a
//     fresh budget — so these DRAIN (issue: a silent pause-on-limit read as a hang).
//
// Everything else PAUSES the drain and keeps the queue intact, because the run
// stopped for a bad reason or the user intervened — firing a follow-up into it would
// be surprising:
//
//   - max_consecutive_failures — the run was failing repeatedly; don't pile on.
//   - cancelled — the USER stopped the run; auto-resuming would fight that intent.
//   - error / "closed" — the run broke or the stream ended abnormally.
//
// The vocabulary is the session.StopReason set plus the "closed" StreamClosed
// sentinel; stopReasonLabel renders the same set for the footer.
func shouldDrain(stop string) bool {
	switch stop {
	case "", "end_turn", "max_turns", "max_tool_calls", "budget":
		return true
	default:
		return false
	}
}

// suffix appends ": <text>" when text is non-empty, for notice formatting.
func suffix(text string) string {
	if strings.TrimSpace(text) == "" {
		return ""
	}
	return ": " + text
}

// sumUsage accumulates token usage across a session for the footer.
func sumUsage(a, b client.Usage) client.Usage {
	return client.Usage{
		InputTokens:      a.InputTokens + b.InputTokens,
		OutputTokens:     a.OutputTokens + b.OutputTokens,
		CacheReadTokens:  a.CacheReadTokens + b.CacheReadTokens,
		CacheWriteTokens: a.CacheWriteTokens + b.CacheWriteTokens,
		ReasoningTokens:  a.ReasoningTokens + b.ReasoningTokens,
	}
}

func (m Model) handleOpenError(err error, cancel context.CancelFunc, retry bool) (Model, tea.Cmd) {
	cancel()
	streamErr := authStreamErr(m, err)
	if streamErr.AuthReason != "" {
		mm, connectCmd := m.reduceLiveAuthRecovery(streamErr.AuthReason)
		return mm.(Model), connectCmd
	}
	m.recoverPrompt(false)
	m = m.endRun(stopError)
	if retry {
		m.failedStepRetryRun = false
		m.statusMsg = m.deps.Theme.Style("warning").Render("retry transport failed: " + sanitizeTerminal(err.Error()) + " — use /retry to try again")
	} else {
		m.conv.addError("open run: " + friendlyWorkspaceEnrollmentRejection(err.Error()))
	}
	if len(m.queued) > 0 {
		m.queuePaused = stopError
	}
	return m, m.armLiveFeed()
}

func authStreamErr(m Model, err error) client.StreamErrMsg {
	reason, classified := client.AuthFailure(err, m.deps.BearerBacked)
	return client.StreamErrMsg{Err: err, AuthReason: reason, Transient: !classified && client.TransientStreamErr(err)}
}

// createSessionCmd runs CreateSession off the update goroutine; result arrives as
// SessionReadyMsg, connectFallbackMsg, or ConnectErrMsg.
//
// The fallback leg (issue #41): the connect-time reconcile is PROVIDER-level, so
// the carried selection's model string is the SERVER's to validate. When the
// create REJECTS a non-zero selection (gRPC InvalidArgument — the code the server
// maps a bad provider_id/model_id selector to; see client.IsInvalidArgument),
// retry ONCE with the zero selection (server default): success surfaces as
// connectFallbackMsg (a LOUD warning, never a silent downgrade); both failing
// keeps today's fatal path with the ORIGINAL error. Any OTHER failure (transient:
// unavailable, deadline, auth) goes straight to ConnectErrMsg — a valid saved
// selection must never be downgraded to the server default with a dishonest
// "rejected" warning over a server blip. A zero-selection create that fails also
// goes straight to ConnectErrMsg.
func (m Model) createSessionCmd() tea.Cmd {
	deps := m.deps
	sel := m.createModelSelection // the reconciled apply-on-next-create selection (zero ⇒ server default)
	return func() tea.Msg {
		id, caps, resolved, err := deps.Session.CreateSession(deps.Ctx, sel, m.desiredMode())
		if err == nil {
			return client.SessionReadyMsg{SessionID: id, Capabilities: caps, ResolvedModel: resolved, Mode: m.desiredMode()}
		}
		if sel.IsZero() || !client.IsInvalidArgument(err) {
			reason, _ := client.AuthFailure(err, deps.BearerBacked)
			return client.ConnectErrMsg{Err: err, AuthReason: reason}
		}
		id, caps, resolved, retryErr := deps.Session.CreateSession(deps.Ctx, client.ModelSelection{}, m.desiredMode())
		if retryErr != nil {
			// Authentication on the retry is authoritative: unlike an unclassified
			// infrastructure failure, it must enter auth recovery rather than being
			// hidden behind the original selector diagnostic.
			if reason, classified := client.AuthFailure(retryErr, deps.BearerBacked); classified {
				return client.ConnectErrMsg{Err: retryErr, AuthReason: reason}
			}
			return client.ConnectErrMsg{Err: err}
		}
		return connectFallbackMsg{
			ready:    client.SessionReadyMsg{SessionID: id, Capabilities: caps, ResolvedModel: resolved, Mode: m.desiredMode()},
			rejected: sel,
			err:      err,
		}
	}
}

// connectFallbackMsg reports a connect-time CreateSession whose carried model
// selection the server REJECTED, where the immediate zero-selection retry
// succeeded: the session is live on the SERVER DEFAULT, not the saved pick. The
// reducer applies the ready payload like SessionReadyMsg, clears the now-known-bad
// active selection for this run, and sets a WARNING status naming the rejected
// model + the server's error — NEVER silent (issue #41). The state file is NOT
// rewritten (the pick may become valid again, e.g. when the key returns).
type connectFallbackMsg struct {
	ready    client.SessionReadyMsg
	rejected client.ModelSelection
	err      error
}
