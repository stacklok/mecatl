package ui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
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

// quitHint is the footer hint shown while the quit guard is armed. The exact string
// is reused as the disarm/clear sentinel: onKey/quitDisarmMsg clear the status line
// ONLY when it still equals this hint, so a later status overwrite is never undone.
const quitHint = "press ctrl+c again to quit"

// quitDisarmMsg fires quitArmWindow after the guard is armed. Its gen is the arm
// generation it was scheduled with; the handler ignores it unless it still matches
// m.quitArmGen (a stale tick from a previous arm cannot disarm a fresh guard).
type quitDisarmMsg struct{ gen int }

// quitDisarmCmd schedules the one-shot disarm tick for arm generation gen.
func (Model) quitDisarmCmd(gen int) tea.Cmd {
	return tea.Tick(quitArmWindow, func(time.Time) tea.Msg { return quitDisarmMsg{gen} })
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

	model, cmd := m.update(msg)
	if mm, ok := model.(Model); ok {
		// SINGLE source of truth for "an overlay/modal/help/fatal took the body, so a
		// mid-drag selection is now stale" (Req 8). This wrapper sees the model BEFORE
		// and AFTER the message is reduced, so a selectable→non-selectable transition
		// is detectable in ONE place — covering every overlay opener, the permission
		// ask, and the fatal screen without a clearSelection() call sprinkled in each.
		// It runs synchronously while the overlay state is active (before View renders
		// the overlay body and well before the overlay closes), so the highlight is
		// cleared the moment the body changes hands. The openers do NOT refreshView, so
		// this cannot live in refreshView — it must be on the per-message seam.
		if mm.sel.active && !selectable(mm) {
			mm = mm.clearSelection()
			// The highlight is spliced into the content (styleSelection), not a native
			// viewport highlight, so dropping the selection needs a re-render to repaint
			// the now-UNSTYLED content. relayout below only refreshes on a height change,
			// so refresh here explicitly — this is the single seam that owns the
			// clear-then-render for the non-selectable transition.
			mm.refreshView()
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

// update is the body of the Elm reducer (see Update, which wraps it with the
// test-only onPhase observer).
func (m Model) update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case streamMsg:
		return m.onStreamMsg(msg)

	case tea.WindowSizeMsg:
		return m.onResize(msg)

	case tea.ColorProfileMsg:
		return m.onColorProfile(msg)

	case tea.MouseWheelMsg, tea.MouseClickMsg, tea.MouseMotionMsg, tea.MouseReleaseMsg:
		return m.onMouseMsg(msg)

	case autoScrollMsg:
		return m.onAutoScroll()

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

	case quitDisarmMsg:
		return m.onQuitDisarm(msg)

	case clickDisarmMsg:
		return m.onClickDisarm(msg)

	default:
		// Lifecycle / transport msgs (session-ready, connect/stream error, stream
		// close, clipboard results, slash-command discovery) are reduced in a
		// separate type-switch to keep this dispatcher's branch count in check.
		if mm, cmd, handled := m.updateLifecycle(msg); handled {
			return mm, cmd
		}
		// MCP overlay result/error msgs (Stage D) are reduced first; if it's not
		// one of those, fall through to the stream-event handler.
		if mm, cmd, handled := m.updateMCPMsg(msg); handled {
			return mm, cmd
		}
		// Skills overlay result/error msg is reduced next; if it's not a SkillsMsg,
		// fall through to the stream-event handler. It fires no follow-up command.
		if mm, handled := m.updateSkillsMsg(msg); handled {
			return mm, nil
		}
		// Agent-definition inventory result/error msg is reduced next; if it's not an
		// Inventory-overlay result/error msgs (agentsInv, soul, usermodel,
		// /worktrees) — none carries a follow-up command. Grouped into one helper to
		// keep update() under the cyclomatic cap as overlays accrue; each falls
		// through if its msg type does not match.
		if mm, handled := m.updateInventoryMsgs(msg); handled {
			return mm, nil
		}
		// /models picker result/error + selection-saved msg. During connect it also
		// returns the CreateSession command (the §4 reconcile-then-create sequence),
		// so this one DOES carry a follow-up command. Fall through if not a models msg.
		if mm, cmd, handled := m.updateModelsMsg(msg); handled {
			return mm, cmd
		}
		// Stream events (session.init / turn.start / deltas / tool.* /
		// permission.ask / hook / compaction / result) are handled separately to
		// keep this reducer's branch count in check.
		return m.updateStreamEvent(msg)
	}
}

// applySessionReady binds an established session into the model: the
// SessionReadyMsg arm's body, extracted so the connectFallbackMsg arm (the
// server-rejected-selection fallback, issue #41) can reuse it before layering its
// warning on top.
func (m Model) applySessionReady(msg client.SessionReadyMsg) (tea.Model, tea.Cmd, bool) {
	m.sessionID = msg.SessionID
	m.caps = msg.Capabilities // stored for Phase B; unrendered this phase
	// The EFFECTIVE provider+model the server resolved this session to (echoed
	// verbatim). The header shows it from turn zero. The model is FIXED per session,
	// so this is set once here. An older server yields the zero value → no segment.
	m.effectiveModel = msg.ResolvedModel
	if msg.Mode != "" {
		m.activeMode = client.ModeString(client.ModeFromString(msg.Mode))
	}
	m.restartFailed = false // a session is (re)established; any prior failure clears
	m.phase = phaseIdle
	m.statusMsg = "connected"
	// Now that we are idle + (still) empty, the welcome splash shows: transmit the
	// Kitty mascot if the terminal supports it (no-op otherwise). The WindowSizeMsg
	// path also fires this, but at connect the phase was still phaseConnecting when
	// that arrived, so fire it here on the idle transition too. Two-step form: the
	// pointer call mutates m (kittyActive/kittyTier), and mixing it with m as a
	// sibling return operand leaves the copy order UNSPECIFIED (see markDirty's
	// doc) — the cmd is taken first so the returned model carries the mutation.
	cmd := (&m).maybeKittyTransmit()
	return m, cmd, true
}

// updateLifecycle reduces the transport/lifecycle msgs (session-ready, connect &
// stream errors, stream close, clipboard results, slash-command discovery). It is
// split out of update so the top-level dispatcher stays under the cyclomatic cap;
// handled=false means the msg is none of these and the caller continues its
// fall-through chain (MCP/skills/agents overlays → stream events).
func (m Model) updateLifecycle(msg tea.Msg) (tea.Model, tea.Cmd, bool) {
	switch msg := msg.(type) {
	case client.SessionReadyMsg:
		return m.applySessionReady(msg)
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
		m.activeModel = client.ModelSelection{}
		m.models.active = client.ModelSelection{}
		notice := "saved model " + sanitizeTerminal(modelSelLabel(msg.rejected)) +
			" was rejected by the server (" + sanitizeTerminal(msg.err.Error()) +
			") — using the server default"
		// Name the model the session actually fell back to, when known — mirroring
		// the key-removed reconcile notice. The fallback create's response already
		// carries it (applySessionReady set m.effectiveModel from msg.ready).
		if id := m.effectiveModel.ModelID; id != "" {
			notice += " — now running " + sanitizeTerminal(id)
		}
		m.statusMsg = m.deps.Theme.Style("warning").Render(notice)
		return m, cmd, handled
	case client.ConnectErrMsg:
		m.phase = phaseFatal
		m.fatalErr = msg.Err.Error()
		return m, nil, true
	case restartFailedMsg:
		// A /models restart-now re-create failed. Unlike ConnectErrMsg this is NOT
		// terminal: we deliberately destroyed a working session, so leave the app
		// RECOVERABLE (idle, no session) with a loud status naming the failed model and
		// enter-to-retry armed (the selection still lives in m.activeModel). The
		// transcript is gone, but the app stays usable.
		m.phase = phaseIdle
		m.sessionID = ""
		m.restartFailed = true
		m.statusMsg = m.deps.Theme.Style("errorText").Render(
			"could not switch to " + sanitizeTerminal(msg.model) + ": " +
				sanitizeTerminal(msg.err.Error()) + " — press enter to retry")
		_ = m.ta.Focus()
		m.refreshView()
		return m, nil, true
	case client.StreamErrMsg:
		// An error PAUSES the queue (shouldDrain(stopError) is false): the staged
		// follow-ups are kept intact and marked paused (m.queuePaused) so the queue
		// card says why, not auto-sent into a broken run. drainQueue records the pause
		// here, but the policy lives in one place.
		m.conv.addError("stream error: " + msg.Err.Error())
		m = m.endRun(stopError)
		mm, drainCmd := m.drainQueue(stopError)
		return mm, tea.Batch(m.refreshCmd(), drainCmd), true
	case clipboardResultMsg:
		mm, cmd := m.onClipboardResult(msg)
		return mm, cmd, true
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
		// Clean close. If a run was still active (no terminal result seen),
		// finalise it; otherwise it's the expected post-result close (no-op).
		if m.phase == phaseRunning || m.phase == phaseAwaitingApproval {
			m = m.endRun("closed")
			modeCmd := m.retryPendingModeCmd()
			mm, drainCmd := m.drainQueue("closed")
			return mm, tea.Batch(m.refreshCmd(), modeCmd, drainCmd), true
		}
		return m, nil, true
	case client.CommandsMsg:
		// Slash-command discovery landed: store the set (a failure degrades quietly
		// to an empty palette) and re-sync so the palette reflects it immediately if
		// the input is still a command line.
		m.palette.commands = msg.Commands
		mm, cmd := m.syncPalette()
		return mm, cmd, true
	case client.ResolvedModelMsg:
		// Footer context-meter self-heal refetch result (issue #66). The ui refetched
		// the server's eventually-consistent live-first resolved model because the
		// meter's denominator was still unknown; this lands the result. The heal is:
		//   - SESSION-CORRELATED: a refetch in flight when a /models switch rebinds the
		//     session to a new id must not land its STALE window on the new session, so
		//     a msg whose SessionID no longer matches the current one is dropped.
		//   - benign on error: a failed refetch keeps the current denominator (the next
		//     turn boundary retries while the window is still unknown).
		//   - RAISE-ONLY: only ever raise the window (the healed live value > the 0 /
		//     floor echo); never lower it (a transient smaller value must not shrink a
		//     known window) and never touch ProviderID/ModelID (the model is FIXED per
		//     session — only the denominator self-corrects). Once raised, contextWindow()
		//     is non-zero so the turn-end gate stops firing the refetch — bounded.
		if msg.Err != nil || msg.SessionID != m.sessionID {
			return m, nil, true
		}
		if msg.Resolved.ContextWindow > m.effectiveModel.ContextWindow {
			m.effectiveModel.ContextWindow = msg.Resolved.ContextWindow
			m.refreshView()
		}
		return m, nil, true
	default:
		return m, nil, false
	}
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
	if m.viewDirty && m.phase == phaseRunning {
		m.tickArmed = true
		return m, m.renderTickCmd()
	}
	return m, nil
}

// updateStreamEvent reduces the per-event stream msgs into the conversation. It
// is the back half of Update, split out so the cyclomatic complexity of each
// stays manageable. Unknown msgs are a no-op.
func (m Model) updateStreamEvent(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case client.SessionInitMsg:
		return m, m.waitCmd()
	case client.TurnStartMsg:
		m.conv.startAssistant()
		m.activeTool = ""
		m.toolProgress = ""
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
		// still UNKNOWN (m.effectiveModel.ContextWindow == 0), refetch the session's
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
		// There is no client-side override (issue #66 deleted the --context-window flag);
		// the gate is purely "session live AND window still unknown".
		if m.sessionID != "" && m.effectiveModel.ContextWindow == 0 {
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
			m.recordFileChange(p)
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
		// Defensive same-stream dedupe: an askID already visible, queued, or
		// answered/retracted this run is dropped (the streamGen guard already kills
		// stale-reader duplicates; this kills same-stream ones).
		if m.askKnown(msg.AskID) {
			return m.afterEvent()
		}
		next := pendingAsk{
			AskID:       msg.AskID,
			Tool:        msg.Tool,
			Args:        msg.Args,
			Reason:      msg.Reason,
			focus:       0,
			offerAlways: !isChildAsk(msg.AskID, m.sessionID),
		}
		if m.phase == phaseAwaitingApproval {
			// A modal is already open: concurrent subagents (team members, parallel
			// Subagent calls) surface asks concurrently, and each parks its child
			// server-side until answered — so a second ask ENQUEUES FIFO behind the
			// visible head instead of clobbering it. The visible ask and the phase
			// are untouched; the (1 of N) badge in the modal title and footer
			// advertises the queue.
			m.askQueue = append(m.askQueue, next)
			return m.afterEvent()
		}
		m.phase = phaseAwaitingApproval
		m.activeTool = ""
		m.toolProgress = ""
		m.ask = next
		// Force-flush via afterEvent (refreshView + reader re-arm), like every other
		// non-delta boundary: any pending coalesced assistant tail must be rendered
		// into the viewport BEFORE the modal opens, so the transcript behind the modal
		// is current the moment it closes (the same "content flushes before a gate"
		// value as the toolcall-card-before-gate invariant). refreshView updating m.vp
		// underneath the centred modal overlay is harmless — View() shows the modal for
		// phaseAwaitingApproval regardless. Without this, the tail's flush would depend
		// on an already-armed one-shot tick happening to survive the phase change — the
		// one boundary that previously did NOT flush, breaking the "only deltas defer"
		// invariant.
		return m.afterEvent()
	case client.HookMsg:
		m.conv.addHook(msg.Text, msg.Phase, msg.Tool, string(msg.Decision))
		return m.afterEvent()
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

// updateStreamSecondary is the back half of updateStreamEvent: the delegation
// projections, the transient notices, and the permission retraction. Split out
// only so neither dispatcher grows past the cyclomatic-complexity bound.
func (m Model) updateStreamSecondary(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case client.PermissionRetractMsg:
		// The harness WITHDREW a surfaced ask (its owning subagent was cancelled while
		// parked). Three cases against the FIFO ask queue (m.ask is the head): a match
		// on the VISIBLE ask dismisses the modal and advances the queue; a match on a
		// QUEUED ask removes it in place; an unknown/stale id is idempotently dropped.
		if m.phase == phaseAwaitingApproval && m.ask.AskID == msg.AskID {
			m.markAskResolved(msg.AskID)
			m.conv.addNotice("permission request withdrawn (subagent cancelled)")
			mm, rearm := m.advanceAsk()
			if !rearm {
				// A queued successor took the head: the phase STAYS awaitingApproval,
				// so the spinner remains off-screen — no re-arm (re-arm fires only when
				// leaving awaitingApproval INTO running; see advanceAsk).
				return mm.afterEvent()
			}
			// Queue empty: modal closed, back to running. Re-arm the spinner for the
			// awaitingApproval→running transition (the phase-gated TickMsg handler
			// dropped the chain while the modal was open). Two-step form, never mixing
			// the old m with the helper's returned model in one expression (the
			// unspecified-evaluation-order trap — see markDirty).
			mm2, cmd := mm.afterEvent()
			return mm2, tea.Batch(cmd, mm2.sp.Tick)
		}
		if i := askQueueIndex(m.askQueue, msg.AskID); i >= 0 {
			// A QUEUED (not-yet-visible) ask was withdrawn: remove it in place. The
			// notice is a visible muted scrollback line because the (1 of N) count
			// badge advertised the queued ask — its silent disappearance would
			// otherwise need explaining. The visible modal is untouched.
			m.markAskResolved(msg.AskID)
			m.askQueue = append(m.askQueue[:i:i], m.askQueue[i+1:]...)
			m.conv.addNotice("queued permission request withdrawn (subagent cancelled)")
			return m.afterEvent()
		}
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
	default:
		return m, nil
	}
}

// applyResult handles a terminal ResultMsg: it folds the run's usage into the running
// totals, surfaces a terminal error, ends the run, and drains any queued prompts.
// Extracted from updateStreamEvent's switch to keep that dispatcher flat.
func (m Model) applyResult(msg client.ResultMsg) (tea.Model, tea.Cmd) {
	// ResultMsg.Usage is the run's CUMULATIVE total; fold it into the session
	// total exactly once here. The per-turn TurnEndMsg feeds only the
	// context-occupancy meter (m.contextTokens), never m.usage — adding both
	// would double-count.
	m.usage = sumUsage(m.usage, msg.Usage)
	if msg.Stop == stopError && msg.Error != "" {
		m.conv.addError(msg.Error)
	}
	m = m.endRun(msg.Stop)
	modeCmd := m.retryPendingModeCmd()
	mm, drainCmd := m.drainQueue(msg.Stop)
	return mm, tea.Batch(m.refreshCmd(), modeCmd, drainCmd)
}

// noticeLine renders the muted-notice text for a transient advisory message
// (compaction or no-progress). Both carry only harness-authored Text and render as a
// single muted line; this keeps updateStreamEvent's switch flat.
func noticeLine(msg tea.Msg) string {
	switch m := msg.(type) {
	case client.CompactionMsg:
		return "history compacted" + suffix(m.Text)
	case client.NoProgressMsg:
		return "no progress" + suffix(m.Text)
	default:
		return ""
	}
}

// applySubagent attributes a REDACTED subagent projection to its Subagent tool card by
// ParentCallID (id match, like resolveTool). A miss is silently dropped: the card
// carries no child content either way, so a lost subagent event only costs the
// trace, never correctness or isolation.
func (m *Model) applySubagent(msg client.SubagentMsg) {
	// Two destinations, fed from the SAME event: (1) the inline Subagent card, keyed by
	// ParentCallID (the calm in-context default); (2) the flat fleet collection, keyed
	// by ChildID, which backs the fleet footer segment and the ctrl+a Subagents tab.
	// Both are redacted, metadata-only — neither carries child content (gauntlet #7).
	switch msg.Kind {
	case client.SubagentStart:
		m.conv.setSubagentStart(msg.ParentCallID, msg.Goal, msg.RoutedCategory, msg.RoutedModel, msg.Model)
		m.conv.fleetStart(msg.ChildID, msg.Goal, msg.RoutedCategory, msg.RoutedModel, msg.Model, msg.Background)
	case client.SubagentTool:
		m.conv.addSubagentTool(msg.ParentCallID, msg.ToolName, msg.IsError, msg.ToolCount)
		m.conv.fleetTool(msg.ChildID, msg.ToolName, msg.IsError, msg.ToolCount)
	case client.SubagentEnd:
		m.conv.setSubagentEnd(msg.ParentCallID, msg.Usage, msg.ToolCount, msg.Stop, msg.DurationMs)
		m.conv.fleetEnd(msg.ChildID, msg.Usage, msg.ToolCount, msg.Stop, msg.DurationMs)
		// A BACKGROUND child finishing is otherwise invisible (its Subagent card
		// resolved long ago with the started-result), so surface a brief transient
		// footer notice — the same advisory channel as team-done / no-progress, never
		// a durable scrollback line. The result body goes to the AGENT (via
		// SubagentStatus), not to this client; the copy says exactly that.
		if ln := findFleetLane(m.conv.subagentFleet, msg.ChildID); ln != nil && ln.background {
			m.statusMsg = m.deps.Theme.Style("muted").Render(
				"background subagent #" + shortChildID(msg.ChildID) + " done — result ready for the agent")
		}
	}
}

// applyParallel routes a REDACTED Parallel fork-join projection into the GROUPED
// parallelGroups state (keyed by ParentCallID), which backs the fleet footer segment and
// the ctrl+a Parallel tab. Unlike Subagent it has no second inline-card destination: a
// Parallel run's deliverable (the winner + fork paths) rides the tool RESULT text the
// model reads; these events are the client observability channel only. All fields are
// redacted, metadata-only — none carries branch content (gauntlet #7).
func (m *Model) applyParallel(msg client.ParallelMsg) {
	switch msg.Kind {
	case client.ParallelStart:
		m.conv.parallelStart(msg.ParentCallID, msg.Join, msg.BranchCount)
	case client.ParallelBranchStart:
		m.conv.parallelBranchStart(msg.ParentCallID, msg.BranchIndex, msg.ChildID, msg.BranchLabel, msg.Goal, msg.RoutedCategory, msg.RoutedModel, msg.Model)
	case client.ParallelBranchTool:
		m.conv.parallelBranchTool(msg.ParentCallID, msg.BranchIndex, msg.ToolName, msg.IsError, msg.ToolCount)
	case client.ParallelBranchEnd:
		m.conv.parallelBranchEnd(msg.ParentCallID, msg.BranchIndex, msg.ChildID, msg.Usage, msg.ToolCount, msg.Stop, msg.Failed, msg.Workspace, msg.DurationMs)
	case client.ParallelEnd:
		m.conv.parallelEnd(msg.ParentCallID, msg.Join, msg.BranchCount, msg.Winner, msg.WinnerWorkspace, msg.Stop)
	}
}

// applyTeam attributes a BOUNDED team projection to its Team tool card by
// ParentCallID (id match, like applySubagent). A miss is silently dropped: the
// member transcripts never enter the parent conversation either way, so a lost
// team.* event only costs the lane trace, never correctness or isolation.
func (m *Model) applyTeam(msg client.TeamMsg) {
	switch msg.Kind {
	case client.TeamStart:
		m.conv.setTeamStart(msg.ParentCallID, msg.TeamID, msg.Roster)
	case client.TeamMember:
		m.conv.addTeamMember(msg)
	case client.TeamTasks:
		m.conv.setTeamTasks(msg.ParentCallID, msg.Tasks)
	case client.TeamFindings:
		m.conv.setTeamFindings(msg.ParentCallID, msg.Findings)
	case client.TeamEnd:
		m.conv.setTeamEnd(msg.ParentCallID, msg.TeamID, msg.Rounds, msg.Stop, msg.Usage, msg.Dispositions)
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
	m.ta.SetWidth(max(1, m.width-railFrame))
	m.rend.setWidth(m.width)
	// relayout sizes the viewport height from the measured layout (header + transients
	// + input + footer) and, when the height changed, re-renders + re-derives
	// auto-follow — the tail onResize used to do inline. The magic taH=4/footerH=2 and
	// the header arithmetic are GONE; the heights are measured via lipgloss.Height of
	// the rendered regions in chrome().
	m.relayout()
	return m, m.maybeKittyTransmit()
}

// onColorProfile records whether the terminal is truecolor (from the
// tea.ColorProfileMsg Bubble Tea sends once at startup). It gates the welcome
// wordmark gradient: only a truecolor profile gets the per-column jade→gold blend;
// anything poorer collapses to a single accent colour (set in the welcome package
// off m.fullColor). The ui keeps colorprofile contained to the reducer — the
// welcome package only ever sees the derived bool.
func (m Model) onColorProfile(msg tea.ColorProfileMsg) (tea.Model, tea.Cmd) {
	m.fullColor = msg.Profile == colorprofile.TrueColor
	return m, nil
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
	if !welcome.KittyCapable() {
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
// then syncStuck (SetHeight can clamp YOffset so AtBottom flips — re-derive
// auto-follow, otherwise the "↑ NN%" cue would lie). Width is NOT touched here (it is
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
	m.syncStuck()
}

// onKey routes key presses by phase. ctrl+c is handled first, with a graceful
// double-press guard (Claude Code's "press again to exit"): a first ctrl+c does
// NOT quit — it clears a non-empty prompt, or on an empty prompt arms the guard
// and shows a hint; a second ctrl+c while armed quits. The fatal screen is the one
// exception (single press quits — there is nothing to lose). The OS-signal path
// (SIGINT/SIGTERM via tea.WithContext in main) is unaffected; this is the in-TUI
// key path only.
func (m Model) onKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if key.Matches(msg, m.keys.Quit) {
		return m.onQuitKey()
	}

	// Any non-ctrl+c key disarms the quit guard (and clears the hint if it is still
	// the one we set) before routing on, so the "press again" window only spans
	// consecutive ctrl+c presses.
	if m.quitArmed {
		m.quitArmed = false
		if m.statusMsg == quitHint {
			m.statusMsg = ""
		}
	}

	// esc clears an ACTIVE text selection FIRST — before every other esc meaning
	// (cancel run / close overlay / clear input/queue). This consumes the key ONLY
	// when a selection is active; with no selection it falls through untouched, so
	// today's esc semantics are entirely preserved (Req 5). A selection only exists
	// on the alt screen with no overlay (selectable), so this never shadows an
	// overlay's own esc.
	if key.Matches(msg, m.keys.Cancel) && m.sel.active {
		m = m.clearSelection() // also zeroes any pending edge-autoscroll direction
		m.refreshView()
		return m, nil
	}

	// An open inventory/picker overlay (MCP, team, agents, skills, soul, usermodel,
	// models) owns the keyboard while open — each steps back / closes on esc
	// internally and returns handled=false when closed. Routed here before the phase
	// switch (and before the ctrl+t toggle, so esc/enter belong to the overlay).
	if mm, cmd, handled := m.onOverlayKey(msg); handled {
		return mm, cmd
	}

	// An open help overlay owns the keyboard: "?" or esc closes it, everything
	// else is swallowed. Routed AFTER the MCP/agents overlays (they never coexist;
	// those handlers return handled=false when closed) and BEFORE the ctrl+t
	// toggle and the phase switch — so a "?" pressed while help is up closes it
	// rather than reopening or leaking to the textarea.
	if m.showHelp {
		if key.Matches(msg, m.keys.Help) || key.Matches(msg, m.keys.Close) {
			m.showHelp = false
			_ = m.ta.Focus()
		}
		return m, nil
	}

	// ctrl+t is a global render toggle (full vs capped tool output); it works in
	// any phase and never feeds the textarea.
	if key.Matches(msg, m.keys.ExpandTools) {
		m.expandTools = !m.expandTools
		m.refreshView()
		return m, nil
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

	// alt+m cycles permission mode. It is handled here — before the phase switch —
	// for idle + running so the key never feeds the textarea. Ctrl+M collides with
	// Enter on real terminals, so the binding deliberately uses Alt+M.
	if key.Matches(msg, m.keys.ModeSwitch) && (m.phase == phaseIdle || m.phase == phaseRunning) {
		return m.switchMode(client.NextMode(m.desiredMode()))
	}

	switch m.phase {
	case phaseAwaitingApproval:
		return m.onApprovalKey(msg)
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
	overlays := []func(tea.KeyPressMsg) (tea.Model, tea.Cmd, bool){
		m.onMCPKey,
		m.onAgentsKey,
		m.onAgentsInvKey,
		m.onSkillsKey,
		m.onSoulKey,
		m.onUserModelKey,
		m.onModelsKey,
		m.onEffortKey,
		m.onWorktreesKey,
	}
	for _, route := range overlays {
		if mm, cmd, handled := route(msg); handled {
			return mm, cmd, true
		}
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
func (m Model) onQuitKey() (tea.Model, tea.Cmd) {
	// Already armed → a second ctrl+c within the window: quit now. The fatal
	// (dead-connection) screen also exits on a single press — there is no input to
	// clear and no run to protect, so the guard would only add friction.
	if m.quitArmed || m.phase == phaseFatal {
		if m.cancelRun != nil {
			m.cancelRun()
		}
		return m, tea.Quit
	}
	// First ctrl+c with staged input: clear the input (mirrors esc's clear-the-line)
	// and do NOT arm — a single press to wipe a draft is expected.
	if strings.TrimSpace(m.ta.Value()) != "" {
		m.ta.Reset()
		return m.afterInputEdit(nil)
	}
	// First ctrl+c on an empty prompt: arm the guard, show the hint, and schedule the
	// timed disarm for this arm generation.
	m.quitArmed = true
	m.quitArmGen++
	m.statusMsg = quitHint
	m.refreshView()
	return m, m.quitDisarmCmd(m.quitArmGen)
}

// onQuitDisarm handles the timed disarm tick: it disarms ONLY if the tick matches
// the current arm generation (a stale tick from a prior arm must not disarm a guard
// re-armed since) and clears the hint only if it is still the one we set.
func (m Model) onQuitDisarm(msg quitDisarmMsg) (tea.Model, tea.Cmd) {
	if m.quitArmed && msg.gen == m.quitArmGen {
		m.quitArmed = false
		if m.statusMsg == quitHint {
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
	// A bracketed paste whose payload is a single media FILE PATH (the common
	// drag-an-image-onto-the-terminal flow) is staged as an attachment instead of
	// inserted literally. On ANY miss (not a path, not media, cap-gated, oversize)
	// it returns ok=false and we fall through to the literal-text insert below
	// (iteration-1 behaviour) — so prose pastes are completely unaffected.
	if mm, cmd, ok := m.tryPasteMediaPath(msg.Content); ok {
		return mm, cmd
	}
	// A LARGE text paste (>= pasteCharThreshold runes or >= pasteLineThreshold
	// lines, alone or CUMULATIVELY with the current buffer — see pasteNeedsStaging)
	// is staged behind a "[Pasted text #N]" placeholder instead of entering the
	// textarea: the textarea re-wraps every logical line per rendered frame, so a
	// huge buffered paste makes every subsequent keypress O(paste) (issue #45).
	// The full text expands back in place at submit/enqueue. Below the thresholds
	// the literal insert below is byte-identical to the pre-staging behaviour.
	if pasteNeedsStaging(m.ta.Value(), msg.Content) {
		return m.stageLargePaste(msg.Content)
	}
	var cmd tea.Cmd
	m.ta, cmd = m.ta.Update(msg)
	return m.afterInputEdit(cmd)
}

// pasteGateOpen reports whether pasted text may reach the prompt input right now:
// no overlay/help/approval owns the screen, and the phase accepts input (idle or
// running — both keep the textarea focused for compose/enqueue). It is THE paste
// gate, shared by the bracketed-paste reducer (onPaste) and the middle-click
// primary-selection paste trigger (onMousePress), so the two paths can never
// drift apart.
func (m Model) pasteGateOpen() bool {
	if m.showHelp || m.phase == phaseAwaitingApproval ||
		m.mcp.view != mcpNone || m.team.view != teamNone || m.agentsInv.view != agentsInvNone {
		return false
	}
	return m.phase == phaseIdle || m.phase == phaseRunning
}

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

// onApprovalKey resolves the open permission modal. Left/right (or tab) cycle the
// focused button (over {allow, deny} or {allow, always, deny} per offerAlways);
// allow/always/deny keys send ResumeApproval with the exact ask_id. The always
// key (w) is ignored unless always-allow is offered for this ask.
func (m Model) onApprovalKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	// The focus ring is {0:allow, 2:deny} for a two-button modal and
	// {0:allow, 1:always, 2:deny} when always-allow is offered.
	ring := []int{0, 2}
	if m.ask.offerAlways {
		ring = []int{0, 1, 2}
	}
	idx := 0
	for i, v := range ring {
		if v == m.ask.focus {
			idx = i
			break
		}
	}
	switch msg.String() {
	case "right", "tab":
		m.ask.focus = ring[(idx+1)%len(ring)]
		return m, nil
	case "left":
		m.ask.focus = ring[(idx-1+len(ring))%len(ring)]
		return m, nil
	case "enter":
		return m.resolveAsk(focusVerdict(m.ask.focus))
	}
	if key.Matches(msg, m.keys.AllowAlways) {
		if m.ask.offerAlways {
			return m.resolveAsk(client.VerdictAllowAlways)
		}
		return m, nil
	}
	if key.Matches(msg, m.keys.Allow) {
		return m.resolveAsk(client.VerdictAllowOnce)
	}
	if key.Matches(msg, m.keys.Deny) {
		return m.resolveAsk(client.VerdictDeny)
	}
	return m, nil
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

// advanceAsk advances the FIFO ask queue's head: it pops the next queued ask into
// the visible m.ask slot (phase STAYS phaseAwaitingApproval — the successor modal
// opens immediately), or — when the queue is empty — clears the modal and returns
// to phaseRunning. It is the ONLY head-ADVANCING writer of m.ask/m.askQueue: the
// other writers are open/enqueue (the PermissionAskMsg reducer) or removal/clear
// only (the queued-retract in-place removal, endRun, resetSession) — so what "the
// next ask becomes visible" means lives in exactly one place. rearm reports
// whether the caller must re-arm the spinner tick (m.sp.Tick): true only when the
// modal actually closed (awaitingApproval → running, a spinner-visible
// transition). With a queued successor the phase never leaves awaitingApproval and
// spinnerVisible() is still false, so re-arming would start a dead chain — re-arm
// fires only when leaving awaitingApproval INTO running (keep in sync with
// TestSpinnerVisibleMatchesFooterRender).
func (m Model) advanceAsk() (Model, bool) {
	if len(m.askQueue) > 0 {
		// Plain re-slice pop (no copy): the head is copied BY VALUE into m.ask, and
		// the shared backing array is only ever touched through the one live Model
		// the single-threaded Elm reducer returns — a stale alias in a discarded
		// older Model copy is never observed. (The queued-retract removal uses the
		// capacity-clamped append form instead because it appends into the very
		// slice it splits.)
		m.ask = m.askQueue[0]
		m.askQueue = m.askQueue[1:]
		return m, false
	}
	m.ask = pendingAsk{}
	m.phase = phaseRunning
	return m, true
}

// markAskResolved records an answered/retracted askID into the resolvedAsks
// dedupe set, lazily initialising it (a reference type mutable through the
// value-receiver Model, same pattern as recordFileChange/filesSeen).
func (m *Model) markAskResolved(id string) {
	if m.resolvedAsks == nil {
		m.resolvedAsks = make(map[string]struct{})
	}
	m.resolvedAsks[id] = struct{}{}
}

// askKnown reports whether askID is already visible (the modal head), queued, or
// answered/retracted this run — the duplicate-ask drop predicate.
func (m Model) askKnown(id string) bool {
	if _, ok := m.resolvedAsks[id]; ok {
		return true
	}
	if m.ask.AskID == id {
		return true
	}
	return askQueueIndex(m.askQueue, id) >= 0
}

// askQueueIndex returns the index of askID in the queue, or -1.
func askQueueIndex(q []pendingAsk, id string) int {
	for i, a := range q {
		if a.AskID == id {
			return i
		}
	}
	return -1
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
func (m Model) resolveAsk(v client.Verdict) (tea.Model, tea.Cmd) {
	askID := m.ask.AskID
	stream := m.stream
	m.markAskResolved(askID)
	m, rearm := m.advanceAsk()

	var notice string
	switch v {
	case client.VerdictAllowAlways:
		notice = "permission allowed (always, this session)"
	case client.VerdictDeny:
		notice = "permission denied"
	default:
		notice = "permission allowed"
	}
	m.conv.addNotice(notice)
	m.refreshView()

	send := func() tea.Msg {
		if stream == nil {
			return nil
		}
		if err := stream.SendApproval(askID, v); err != nil {
			return client.StreamErrMsg{Err: err}
		}
		return nil
	}
	if !rearm {
		// A queued successor took the head: the phase STAYS awaitingApproval, so the
		// spinner is still off-screen — no m.sp.Tick. Re-arm fires only when leaving
		// awaitingApproval INTO running (see advanceAsk; keep in sync with
		// TestSpinnerVisibleMatchesFooterRender).
		return m, send
	}
	// Re-arm the spinner: the awaitingApproval→running transition re-enters a
	// spinner-visible phase, and the phase-gated TickMsg handler dropped the chain
	// when the modal opened. m.sp.Tick is NOT a stream reader — the no-extra-reader
	// invariant above is untouched.
	return m, tea.Batch(send, m.sp.Tick)
}

// onRunningKey handles keys while a run streams. Type-while-running: the textarea
// stays focused so the user can compose and ENQUEUE a follow-up. It mirrors
// onIdleKey's precedence so the input behaves the same mid-run as at idle, with
// two differences — enter ENQUEUES (instead of submitting), and esc has a layered
// meaning before it falls through to cancel:
//
//	(1) an open palette claims its NAVIGATION keys (↑/↓/tab/esc) so /-typing shows
//	    the dropdown while running — but NOT enter: mid-run enter must always ENQUEUE
//	    uniformly (the locked decision). So a "/clear" line composed mid-run is staged
//	    as the literal text "/clear" and dispatched as a built-in at DRAIN time (where
//	    the phase is idle and /clear's idle-guard is satisfied) via submitPrompt's
//	    existing intercept — never run immediately mid-run via the palette;
//	(2) esc/Cancel: non-empty input → clear the input (and resync the palette);
//	    else non-empty queue → clear the queue (status "queue cleared"); else →
//	    SendCancel (today's behaviour: the run ends with stop "cancelled");
//	(3) shift+enter (Newline) → insert a newline;
//	(4) enter (Submit) → enqueuePrompt (the locked decision: enter mid-run stages a
//	    follow-up, it does not submit a second concurrent run);
//	(5) pgup/pgdn → scroll the viewport;
//	(6) anything else → feed the textarea (+ palette resync via afterInputEdit).
//
// ctrl+t (expand) and ctrl+c (quit) are handled globally in onKey before this.
func (m Model) onRunningKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	// Let the palette claim its navigation keys while running, but NOT enter — enter
	// mid-run always enqueues (uniform), so it must fall through to the Submit case
	// below rather than completing/running a palette row. (esc is handled by the
	// Cancel branch below, which layers clear-input/clear-queue/cancel — the palette's
	// own esc-dismiss would shadow that, so it is excluded here too.)
	if m.palette.open && !key.Matches(msg, m.keys.Submit) && !key.Matches(msg, m.keys.Cancel) {
		if mm, cmd, handled := m.onPaletteKey(msg); handled {
			return mm, cmd
		}
	}
	// The @-mention menu, like the palette, claims its navigation/complete keys
	// while running EXCEPT enter (which must enqueue uniformly) and esc (the
	// Cancel branch layers clear-input/clear-queue/cancel below).
	if m.mention.open && !key.Matches(msg, m.keys.Submit) && !key.Matches(msg, m.keys.Cancel) {
		if mm, handled := m.onMentionKey(msg); handled {
			return mm, nil
		}
	}
	switch {
	case key.Matches(msg, m.keys.Agents):
		// ctrl+a opens the unified agents overlay MID-RUN (Gap B): the deep view is
		// most useful while agents stream. openAgents permits phaseRunning, reads the
		// live lanes/fleet, and picks the context-sensitive default tab; pre-empt the
		// textarea default so the keypress drives the overlay, never the input.
		return m.openAgents()
	case key.Matches(msg, m.keys.Cancel):
		if strings.TrimSpace(m.ta.Value()) != "" {
			// Staged-but-unsent input: esc clears it first (mirrors a text editor's
			// "esc clears the line"), leaving the queue and the run untouched.
			m.ta.Reset()
			return m.afterInputEdit(nil)
		}
		if len(m.queued) > 0 {
			// No live input but staged follow-ups: esc drops the queue before it would
			// cancel the run, so a user who changed their mind can clear the backlog
			// without killing the in-flight turn.
			m.queued = nil
			m.statusMsg = m.deps.Theme.Style("muted").Render("queue cleared")
			m.refreshView()
			return m, nil
		}
		// Nothing staged: esc cancels the run (today's behaviour — the run ends with
		// stop "cancelled"; the stream stays open until that terminal result).
		stream := m.stream
		m.statusMsg = "cancelling…"
		return m, func() tea.Msg {
			if stream != nil {
				_ = stream.SendCancel()
			}
			return nil
		}
	case key.Matches(msg, m.keys.Newline):
		m.ta.InsertRune('\n')
		return m.afterInputEdit(nil)
	case key.Matches(msg, m.keys.Submit):
		return m.enqueuePrompt()
	case key.Matches(msg, m.keys.ScrollU), key.Matches(msg, m.keys.ScrollD),
		key.Matches(msg, m.keys.ScrollTop), key.Matches(msg, m.keys.ScrollBottom):
		return m.onScrollKey(msg)
	default:
		if swallowWordLeftHang(m.ta, msg) {
			return m, nil // upstream bubbles#1652 workaround — see textarea_guard.go
		}
		var cmd tea.Cmd
		m.ta, cmd = m.ta.Update(msg)
		return m.afterInputEdit(cmd)
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
// Crucially it does NOT open a stream or send anything — the queued text becomes a
// real prompt only when drainQueue later hands it to submitPrompt (the existing
// send path, which maps to the server-side StartRunContent reopen). There is no
// second send path.
func (m Model) enqueuePrompt() (tea.Model, tea.Cmd) {
	text := strings.TrimSpace(m.ta.Value())
	if text == "" {
		return m, nil
	}
	if len(m.queued) >= maxQueued {
		m.statusMsg = m.deps.Theme.Style("muted").Render(fmt.Sprintf("queue full (%d)", maxQueued))
		return m, nil
	}
	// Expand staged large-paste placeholders AT ENQUEUE time (past the cap check,
	// which keeps the input — and so must keep the store). The queue always holds
	// FINAL text and the staged store stays textarea-scoped: the input is reset
	// below, so its markers are gone and the store must not outlive them. A
	// deleted marker's content is silently dropped (the image-marker UX).
	if len(m.stagedPastes) > 0 {
		text = strings.TrimSpace(expandPastePlaceholders(text, m.stagedPastes))
		m.stagedPastes = nil
		m.nextPasteN = 0
		if text == "" {
			// Every marker was deleted and nothing else was typed: nothing to queue.
			m.ta.Reset()
			return m.afterInputEdit(nil)
		}
	}
	m.queued = append(m.queued, text)
	m.ta.Reset()
	m.statusMsg = m.deps.Theme.Style("muted").Render(fmt.Sprintf("queued (%d)", len(m.queued)))
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
	case key.Matches(msg, m.keys.Help) && strings.TrimSpace(m.ta.Value()) == "":
		// "?" is printable: open help only on an empty prompt so "?" in prose still
		// inserts literally. The overlay claims the keyboard via the m.showHelp gate
		// in onKey; blur the input while it is up.
		m.showHelp = true
		m.ta.Blur()
		return m, nil
	case key.Matches(msg, m.keys.MCPPanel):
		return m.openMCP(mcpPanel)
	case key.Matches(msg, m.keys.Resources):
		return m.openMCP(mcpResources)
	case key.Matches(msg, m.keys.Prompts):
		return m.openMCP(mcpPrompts)
	case key.Matches(msg, m.keys.Agents):
		return m.openAgents()
	case key.Matches(msg, m.keys.Effort):
		// ctrl+e opens the /effort reasoning-effort picker — the same surface the
		// /effort command opens (runEffort → openEffort). openEffort self-gates on
		// idle + caps.ModelSelection, so when model selection is unavailable this
		// returns the model unchanged and the key never opens an empty picker.
		return m.openEffort()
	case key.Matches(msg, m.keys.Cancel) && m.queuePaused != "":
		// A run ended on a non-clean stop with staged follow-ups still queued (the
		// paused state). Mirror the running-phase esc layering: a non-empty input is
		// cleared first; otherwise esc drops the paused queue. (With no input and an
		// empty queue queuePaused is already "", so this branch never strands esc.)
		if strings.TrimSpace(m.ta.Value()) != "" {
			m.ta.Reset()
			return m.afterInputEdit(nil)
		}
		m.queued = nil
		m.queuePaused = ""
		m.statusMsg = m.deps.Theme.Style("muted").Render("queue cleared")
		m.refreshView()
		return m, nil
	case key.Matches(msg, m.keys.Newline):
		m.ta.InsertRune('\n')
		return m.afterInputEdit(nil)
	case key.Matches(msg, m.keys.Submit):
		return m.onIdleSubmit()
	case key.Matches(msg, m.keys.ScrollU), key.Matches(msg, m.keys.ScrollD),
		key.Matches(msg, m.keys.ScrollTop), key.Matches(msg, m.keys.ScrollBottom):
		return m.onScrollKey(msg)
	default:
		if swallowWordLeftHang(m.ta, msg) {
			return m, nil // upstream bubbles#1652 workaround — see textarea_guard.go
		}
		var cmd tea.Cmd
		m.ta, cmd = m.ta.Update(msg)
		return m.afterInputEdit(cmd)
	}
}

// onIdleSubmit handles enter at idle, in priority order (extracted from onIdleKey to
// keep its cyclomatic complexity under the cap):
//   - RETRY a failed restart-now re-create: in the recoverable state (restartFailed +
//     no session) enter on an EMPTY line re-fires the RECOVERABLE create path
//     (restartOnModelCmd with an empty oldID — no CloseSession) carrying the pending
//     selection (m.activeModel). Routing through restartOnModelCmd (NOT the generic
//     createSessionCmd) is what keeps a re-FAILED retry recoverable: a re-failure
//     emits restartFailedMsg again (looping back to this same recoverable state),
//     never client.ConnectErrMsg → phaseFatal. restartFailed is NOT cleared eagerly —
//     the resolving msg owns its lifecycle (SessionReadyMsg clears it on success;
//     restartFailedMsg re-sets it on a re-failure). The in-flight phaseConnecting
//     window swallows idle keys, so the un-cleared flag can't misfire meanwhile.
//   - RESUME a paused queue: enter on an EMPTY line fires the next staged prompt.
//   - otherwise a normal submitPrompt (a no-op on an empty sessionID).
func (m Model) onIdleSubmit() (tea.Model, tea.Cmd) {
	empty := strings.TrimSpace(m.ta.Value()) == ""
	if m.restartFailed && m.sessionID == "" && empty {
		m.phase = phaseConnecting
		m.statusMsg = "retrying — reconnecting…"
		m.refreshView()
		// m.sp.Tick re-arms the spinner for the idle→connecting transition (the
		// phase-gated TickMsg handler dropped the chain at idle).
		return m, tea.Batch(m.restartOnModelCmd("", m.activeModel), m.sp.Tick)
	}
	if m.queuePaused != "" && len(m.queued) > 0 && empty {
		return m.resumeQueue()
	}
	if m.pendingMode != "" && strings.TrimSpace(m.ta.Value()) != "" {
		m.statusMsg = m.deps.Theme.Style("warning").Render("mode " + m.pendingMode + " is still pending — press enter again after it applies")
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
		if mm, cmd, ran := m.runSelectedBuiltin(); ran {
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

// runSelectedBuiltin runs the currently-selected palette row IF it is a built-in
// command, closing the palette and resetting the input first, and reports ran=
// true. For a non-built-in (workspace) row it reports ran=false so the caller
// falls back to paletteComplete (text-completion). Running built-ins directly on
// palette-enter — rather than text-completing them — is deliberate:
// paletteComplete writes "/<name> " with a TRAILING SPACE, which makes
// commandPrefix false and would slip the line past the submitPrompt built-in
// intercept; for /clear the user expects enter to act, not to pre-fill the input.
func (m Model) runSelectedBuiltin() (tea.Model, tea.Cmd, bool) {
	if !m.palette.open || m.palette.cursor >= len(m.palette.filtered) {
		return m, nil, false
	}
	row := m.palette.filtered[m.palette.cursor]
	if !row.Builtin {
		return m, nil, false
	}
	b, found := builtinByName(m.caps, wiredCollaborators{
		MCP: m.deps.MCP != nil, Agents: m.deps.Agents != nil, Skills: m.deps.Skills != nil,
		Soul: m.deps.Soul != nil, UserModel: m.deps.UserModel != nil, Models: m.deps.Models != nil,
		Worktrees: m.deps.Worktrees != nil,
	}, row.Name)
	if !found {
		return m, nil, false
	}
	// Close the palette and clear the input before acting (mirrors the bare-line
	// submit intercept), then dispatch.
	m.palette.open = false
	m.palette.filtered = nil
	m.palette.cursor = 0
	m.ta.Reset()
	mm, cmd := b.run(m)
	return mm, cmd, true
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

// submitPrompt opens a fresh Converse run for the textarea text, sends the
// mandatory prompt frame, starts the reader goroutine, and arms WaitForMsg.
func (m Model) submitPrompt() (tea.Model, tea.Cmd) {
	text := strings.TrimSpace(m.ta.Value())
	if m.sessionID == "" {
		return m, nil
	}
	// A BARE built-in command line ("/clear", "/help", …) is intercepted here —
	// before addUser / stream-open — and dispatched to its Model action, so the
	// built-in text never reaches the model. A non-built-in "/…" falls through to
	// the normal send (preserving bare workspace-command invocation), and a
	// "/name arg" line has a space → commandPrefix is false → also falls through
	// (workspace commands expand server-side from the full line).
	if name, ok := commandPrefix(text); ok {
		if b, found := builtinByName(m.caps, wiredCollaborators{
			MCP: m.deps.MCP != nil, Agents: m.deps.Agents != nil, Skills: m.deps.Skills != nil,
			Soul: m.deps.Soul != nil, UserModel: m.deps.UserModel != nil, Models: m.deps.Models != nil,
			Worktrees: m.deps.Worktrees != nil,
		}, name); found {
			m.ta.Reset()
			return b.run(m)
		}
	}
	// Expand staged large-paste placeholders IN PLACE first, so the mention
	// expansion and the media reconcile below run on the FINAL text (a pasted
	// "@path" or "[Image #N]" inside the payload behaves exactly as if typed —
	// including an "[Image #N]" token embedded in a paste PAYLOAD: the media
	// reconcile pass below sees it like a typed marker, attaches the staged image
	// and strips the token from the payload's quoted text; typed-marker semantics,
	// accepted). The built-in intercept above ran on the RAW value — a placeholder
	// is never a bare "/command", so it is unaffected. The staged store is cleared
	// further down, AFTER the loud-reject early returns, so a failed submit keeps
	// it for retry.
	hadPastes := len(m.stagedPastes) > 0
	if hadPastes {
		text = strings.TrimSpace(expandPastePlaceholders(text, m.stagedPastes))
	}
	// Expand any @-mentions that resolve to an existing REGULAR FILE into media
	// parts (image/audio) and inlined text-file bodies. attachableMentions is the
	// stat-filter gate: a token that is not a real file (prose like "@oncall", a
	// directory, a dangling link) is left as literal text and never reaches
	// ExpandMentions. The remaining filesystem + proto work lives behind
	// client.ExpandMentions (the ui passes only resolved path strings + the
	// proto-free caps, and reads back proto-free Descriptors/InlineText — the proto
	// Parts stay opaque, kept in the MediaResult and handed straight to SendPrompt,
	// so the ui never names a proto type). ANY error LOUD-rejects: surface it in the
	// transcript, keep the input intact, send NOTHING.
	var media client.MediaResult
	if files := attachableMentions(m.deps.Workspace, text); len(files) > 0 {
		res, err := client.ExpandMentions(files, m.caps)
		if err != nil {
			m.conv.addError("attach: " + err.Error())
			m.refreshView()
			return m, nil
		}
		media = res
		for _, body := range res.InlineText {
			text = strings.TrimSpace(text + "\n\n" + body)
		}
	}
	// Reconcile staged clipboard / pasted-path image attachments. A "[Image #N]"
	// marker that still survives in the prompt text (the user did not delete it
	// while editing) becomes an inline media part; markers ascending by N → parts in
	// display order. Each is rebuilt at submit time via client.StageClipboardImage
	// (the same cap-gate + size-cap choke point), so a cap that flipped or an
	// oversize blob LOUD-rejects here too: surface it, keep the input, send nothing.
	// The markers are UI tokens, so they are STRIPPED from the sent text.
	hadStaged := len(m.stagedMedia) > 0
	for _, marker := range survivingMarkers(text, m.stagedMedia) {
		sa := m.stagedMedia[marker]
		part, desc, err := client.StageClipboardImage(sa.mime, sa.data, m.caps)
		if err != nil {
			m.conv.addError("attach: " + err.Error())
			m.refreshView()
			return m, nil
		}
		media.Parts = append(media.Parts, part)
		media.Descriptors = append(media.Descriptors, desc)
		text = stripMarker(text, marker)
	}
	// Re-check the COMBINED media aggregate (mention parts + clipboard parts):
	// ExpandMentions capped its own parts, but the clipboard reconciliation appended
	// more, so a mention-heavy + clipboard-heavy prompt could cross the per-prompt
	// caps. Loud-reject client-side (keep input, send nothing) exactly like the
	// mention over-cap path, rather than letting the server reject post-send. This
	// is the LAST loud-reject, and it runs BEFORE the staged stores are dropped
	// below, so an aggregate-cap refusal keeps both stores (media AND pastes) for
	// the retry — the input still holds every marker.
	if hadStaged {
		if err := media.CheckAggregateCaps(); err != nil {
			m.conv.addError("attach: " + err.Error())
			m.refreshView()
			return m, nil
		}
	}
	if hadStaged {
		// Drop the staged set (whether sent or — for deleted markers — discarded);
		// trim the whole text (stripMarker already removed the stray spaces around
		// each marker, so multi-line structure and inner newlines are preserved).
		text = strings.TrimSpace(text)
		m.stagedMedia = nil
		m.nextMediaN = 0
	}
	if hadPastes {
		// Drop the staged pastes (expanded above; a deleted marker's content is
		// silently discarded — the image-marker UX). Past ALL the loud-reject
		// returns (incl. the aggregate-cap one above), so an aborted submit kept
		// the store for retry.
		m.stagedPastes = nil
		m.nextPasteN = 0
	}
	// A media-only prompt (empty text but at least one part) still sends — the proto
	// allows text OR parts, and the server enforces "at least one non-empty". A
	// truly empty submit (no text AND no parts) is the no-op early-return.
	if text == "" && len(media.Parts) == 0 {
		return m, nil
	}
	if len(media.Descriptors) > 0 {
		m.conv.addUserWithMedia(text, media.Descriptors)
	} else {
		m.conv.addUser(text)
	}
	m.ta.Reset()
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

	// Open the run stream synchronously so the model can own the channel and
	// cancel func (commands run AFTER Update returns and cannot mutate the
	// model). The Converse open is a lazy gRPC stream create — cheap. The
	// blocking Recv loop runs on its own goroutine via ReadLoop.
	runCtx, cancel := context.WithCancel(m.deps.Ctx)
	stream, err := m.deps.Conv.OpenConverse(runCtx)
	if err != nil {
		cancel()
		m.conv.addError("open run: " + err.Error())
		return m.endRun(stopError), nil
	}
	ch := make(chan tea.Msg, 64)
	m.stream = stream
	m.streamCh = ch
	m.cancelRun = cancel
	m.streamGen++ // open a fresh reader generation; readers of any prior run go stale
	// ReadLoop selects on runCtx so it can never wedge if endRun stops draining
	// ch; endRun calls cancelRun, which unblocks and exits the reader.
	go stream.ReadLoop(runCtx, ch)

	send := func() tea.Msg {
		if err := stream.SendPrompt(m.sessionID, text, media.Parts); err != nil {
			return client.StreamErrMsg{Err: err}
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
	// A dead run's asks must not survive into idle: drop the visible modal, the FIFO
	// queue behind it, and the answered-set dedupe (covers every endRun caller —
	// ResultMsg, StreamErrMsg, StreamClosedMsg, cancel).
	m.ask = pendingAsk{}
	m.askQueue = nil
	m.resolvedAsks = nil
	m.phase = phaseIdle
	// Ensure the input is focused now the run is done. With type-while-running the
	// input is already focused during a run, so this is a no-op on the common path;
	// it still matters as the single re-focus point after a blur the run may have
	// crossed (e.g. an overlay that blurred the textarea). Focus() returns a
	// cursor-blink cmd we don't thread back here; the next keypress re-arms the blink.
	_ = m.ta.Focus()
	if stop != "" {
		text, slot := stopReasonLabel(stop)
		m.statusMsg = m.deps.Theme.Style(slot).Render(text)
	}
	m.refreshView()
	return m
}

// onMouseWheel routes a wheel event to the conversation viewport and re-derives
// auto-follow. Mouse capture is enabled only on the alt screen (View sets
// MouseModeCellMotion there); the viewport's own Update handles tea.MouseWheelMsg
// (gated on MouseWheelEnabled, default true), so a wheel-up unsticks and a wheel
// back to the bottom re-sticks — same as the nav keys.
func (m Model) onMouseWheel(msg tea.MouseWheelMsg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	m.vp, cmd = m.vp.Update(msg)
	// A wheel event changes the scroll offset: invalidate the vpView cache so the
	// next View() renders the new position rather than the stale pre-wheel output.
	m.rend.invalidateVPView()
	m.syncStuck()
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
	return fmt.Sprintf("MOUSE raw x=%d y=%d | top=%d yoff=%d vph=%d | map ok=%v L%d C%d",
		mo.X, mo.Y, convTopRow(m), m.vp.YOffset(), m.vp.Height(), ok, line, col)
}

// onMousePress handles a mouse button press. A LEFT press in the conversation
// region anchors a new selection (when selectable — no overlay owns the body, alt
// screen on); a RIGHT press copies the current selection if one exists (a
// convenience over the copy-on-release default). A press outside the conversation
// region (header/input/footer) or while an overlay owns the body starts nothing.
func (m Model) onMousePress(mo tea.Mouse) (tea.Model, tea.Cmd) {
	if m.deps.DebugMouse {
		m.mouseDebug = m.mouseDebugLine(mo)
	}
	switch mo.Button {
	case tea.MouseRight:
		if m.sel.active && !m.sel.empty() {
			return m.copySelection()
		}
		return m, nil
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
		// The selectable gate AND the count logic sit here, AFTER the gate: a press
		// while an overlay owns the body (or under --no-mouse/--inline) starts nothing
		// AND does not advance the multi-click count (Req 9).
		if !selectable(m) {
			return m, nil
		}
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
	payload := selectedText(m.vp.GetContent(), m.sel)
	if payload == "" {
		return m, disarm
	}
	m.statusMsg = m.deps.Theme.Style("muted").Render(fmt.Sprintf("copied %s", plural(len([]rune(payload)), "char")))
	m.refreshView()
	return m, tea.Batch(tea.SetClipboard(payload), m.shellWriteCmd(payload), disarm)
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
	if m.deps.DebugMouse {
		m.mouseDebug = m.mouseDebugLine(mo)
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
	m.syncStuck()
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
	m.syncStuck()
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
// click) selection is cleared with no copy; a real selection copies on release
// (the copy-on-select default). A release with no active selection is a no-op.
// Release always DISARMS edge-autoscroll (scrollNone) so any in-flight tick no-ops.
//
// Asymmetry note: a release uses screenToContent's clamp (a release outside the
// region still finalises the head at the nearest visible content), unlike motion,
// which routes an at-edge position into armAutoScroll. This is intentional —
// autoscroll only makes sense while the button is HELD; on release the drag is over,
// so we just snap the head to wherever the pointer last was and finish.
func (m Model) onMouseRelease(mo tea.Mouse) (tea.Model, tea.Cmd) {
	if !m.sel.active {
		return m, nil
	}
	m.sel.autoScroll = scrollNone // the drag is over: kill any pending autoscroll tick
	m.sel.autoScrollRamp = 0      // self-reset acceleration so release is a true reset seam
	if line, col, ok := screenToContent(m, mo.X, mo.Y); ok {
		m.sel.headL = line
		m.sel.headC = col
	}
	if m.sel.empty() {
		m.sel = selection{}
		m.refreshView() // repaint the now-UNSTYLED content (no native highlight to clear)
		return m, nil
	}
	// Release is the one head-changing path that doesn't already run through
	// snapshotSelection; capture the identity snapshot AND re-render the spliced
	// highlight for the final head before copy, so the immediately-following
	// refreshView (in copySelection) sees a matching snapshot and keeps the highlight
	// rather than treating the moved head as a reflow and clearing it.
	snapshotSelection(&m)
	return m.copySelection()
}

// copySelection copies the VISIBLE (ansi-stripped) selected text via OSC52
// (tea.SetClipboard, the primary path) AND the best-effort shell-clipboard write
// (shellWriteCmd, batched). It sets a muted "copied N chars" status. An empty
// payload is a no-op (defensive — release already clears empties).
func (m Model) copySelection() (tea.Model, tea.Cmd) {
	payload := selectedText(m.vp.GetContent(), m.sel)
	if payload == "" {
		return m, nil
	}
	m.statusMsg = m.deps.Theme.Style("muted").Render(fmt.Sprintf("copied %s", plural(len([]rune(payload)), "char")))
	m.refreshView()
	return m, tea.Batch(tea.SetClipboard(payload), m.shellWriteCmd(payload))
}

// snapshotSelection records the selection's identity anchor and RE-SPLICES the
// highlight over the unstyled base content in place — the per-gesture (press / drag /
// edge-autoscroll) update. It REPLACES the old applySelectionHighlight: the highlight
// is no longer the viewport's native SetHighlights (which mis-placed it on ANSI-styled
// content — see styleSelection) but our own per-line splice. So there is no
// SetHighlights/ClearHighlights and no YOffset save/restore to neutralise an
// EnsureVisible scroll-jump — re-splicing the SAME-length content never moves YOffset.
//
// It works off m.selBase (the unstyled conversation render, captured when the
// selection became active and refreshed by refreshView) rather than re-rendering the
// whole conversation, so a gesture does not rebuild the transcript or disturb the
// viewport's scroll/line geometry. The snapshot (selectedText against the unstyled
// base) is the identity anchor refreshView compares against to drop the selection on a
// reflow. A pointer receiver: it mutates m.vp in place.
func snapshotSelection(m *Model) {
	if !m.sel.active {
		return
	}
	// snapshotSelection calls vp.SetContent (re-splicing the highlight), so the
	// vpView cache must be invalidated so the next View() reflects the new content.
	m.rend.invalidateVPView()
	base := m.selBase
	if base == "" {
		// Defensive: no base captured (e.g. a test that set raw viewport content then
		// pointed a selection at it without a press). Adopt the current viewport content
		// as the base and remember it, so subsequent gestures re-splice cleanly.
		base = m.vp.GetContent()
		m.selBase = base
	}
	m.sel.snapshot = selectedText(base, m.sel)
	m.vp.SetContent(styleSelection(base, m.sel, m.deps.Theme.Style("selection")))
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

// syncStuck re-derives the auto-follow flag from the viewport's actual position:
// stuck is true exactly when the viewport is at the bottom. Called after every
// scroll/wheel/nav so a scroll-up unsticks (next streaming delta then re-renders
// in place without yanking to bottom — see refreshView) and scrolling/jumping
// back to the bottom re-sticks (auto-follow resumes).
func (m *Model) syncStuck() {
	m.stuck = m.vp.AtBottom()
}

// onScrollKey is the shared conversation-scroll handler used by both onIdleKey and
// onRunningKey: pgup/pgdn delegate to the viewport (which does its own scroll
// math), home/end jump to top/bottom, then syncStuck re-derives auto-follow. End
// naturally re-sticks; home (and a partial pgup) unsticks so streaming no longer
// yanks the view to the bottom. The caller has already matched one of these keys.
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
	m.syncStuck()
	return m, cmd
}

// refreshView re-renders the conversation into the viewport, keeping the view
// pinned to the bottom unless the user has scrolled up. It clears m.viewDirty, so
// every render path (afterEvent, endRun, onResize, the frame-cadence renderTickMsg)
// settles the flag — making "rendered ⟺ not dirty" an invariant and force-flushing
// the tail at every turn/tool/result/error boundary regardless of tick timing.
//
// It re-pins ONLY when m.stuck (auto-follow). A scroll-up sets stuck=false (via
// syncStuck), so a streaming delta re-renders the growing content in place without
// yanking the view back to the bottom — the user's scroll-up survives streaming.
// Scrolling/jumping back to the bottom re-sets stuck, and auto-follow resumes.
//
// Cost shape: renderConversation re-renders only blocks whose rev/width/expand
// changed since the last frame (the renderer's blockCache; in practice the live
// tail block) — settled blocks join from cache — and memoizes the WHOLE joined
// string (joinCache), so a frame that changed no block (a cursor move, scroll, or
// the twice-per-message renderInput) reuses the join verbatim instead of rebuilding
// it. The residual O(scrollback) per-frame cost is vp.SetContent's line
// split/measure of the full content. On TOP of refreshView, View() memoizes the
// vp.View() output (renderer.vpView) so a spinner-only frame that does NOT call
// refreshView skips the lipgloss grapheme-width pad entirely (issue #139).
func (m *Model) refreshView() {
	m.viewDirty = false
	// Any refreshView call changes the viewport content, so the vpView cache must be
	// invalidated — the caller may be a spinner-only frame that skips refreshView
	// entirely, in which case the vpView cache correctly serves the prior content.
	m.rend.invalidateVPView()
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
		m.vp.SetContentLines(m.rend.renderConversationLines(&m.conv, m.expandTools))
		if m.stuck {
			m.vp.GotoBottom()
		}
		return
	}
	content := m.rend.renderConversation(&m.conv, m.expandTools)
	// When the global details toggle is on, fold the session's changed-files list
	// in beneath the scrollback so the muted "Δ N files" header indicator has a
	// discoverable, scannable expansion — without a dedicated key or overlay.
	if m.expandTools {
		if list := m.rend.renderChangedFiles(m.filesChanged); list != "" {
			content += "\n" + list
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
	// Identity check FIRST, against the UNSTYLED content: the anchor/head are absolute
	// line indices, so a reflow that changed the line count above/within the selection
	// (ctrl+t expand/collapse, compaction) now re-points them at different text. If the
	// text under the selection no longer matches what was selected, DROP it rather than
	// highlight/copy the wrong runes. selectedText must read the UNSTYLED content, so it
	// runs before the splice. A pure append below leaves the selected lines untouched, so
	// this does NOT fire for streaming (Req 2 preserved).
	if m.sel.active && selectedText(content, m.sel) != m.sel.snapshot {
		*m = m.clearSelection()
	}
	if m.sel.active {
		// Record the UNSTYLED render as the splice base, so a subsequent gesture
		// (snapshotSelection) can re-splice the highlight in place without re-rendering
		// the whole conversation.
		m.selBase = content
		content = styleSelection(content, m.sel, m.deps.Theme.Style("selection"))
	}
	m.vp.SetContent(content)
	if m.stuck {
		m.vp.GotoBottom()
	}
}

// drainQueue pops the oldest staged follow-up and submits it, one at a time. It is
// called on every run-completion path (ResultMsg / StreamErrMsg / StreamClosedMsg)
// AFTER endRun has settled the model back to idle.
//
// It fires on a HEALTHY stop (shouldDrain): end_turn, the empty reason, or a size
// limit (max_turns / max_tool_calls / budget). On a non-healthy stop — error, a user-cancel
// ("cancelled"), max_consecutive_failures, or a stream close — it does NOT fire:
// instead it records the stop reason in m.queuePaused and KEEPS the queue, so the
// run that died never silently fires the next staged prompt. The user then resumes
// with enter on an empty line (resumeQueue) or clears with esc — renderQueue shows
// that affordance. The phase==phaseIdle guard is belt-and-braces (endRun always
// lands idle on these paths) so a future caller can't drain into a still-running
// model.
//
// The submit goes through the EXISTING submitPrompt path — the same one a typed
// prompt uses — so the queued prompt reopens the completed session server-side
// (StartRunContent) exactly like a manual follow-up; there is no separate send
// path. submitPrompt sets phaseRunning and opens a fresh stream whose own ResultMsg
// re-enters drainQueue, giving a one-at-a-time FIFO drain.
func (m Model) drainQueue(stop string) (tea.Model, tea.Cmd) {
	if len(m.queued) == 0 {
		m.queuePaused = ""
		return m, nil
	}
	if !shouldDrain(stop) {
		// A non-clean stop (error / user-cancel / repeated failures / stream close)
		// with staged follow-ups: PAUSE and KEEP the queue, but record the reason so
		// renderQueue can say so loudly (and the idle keys can resume/clear it) — a
		// silent "N queued" after the run died reads as a hang. The user resumes with
		// enter on an empty line (resumeQueue) or clears with esc.
		m.queuePaused = stop
		return m, nil
	}
	if m.phase != phaseIdle {
		return m, nil // belt-and-braces: never drain into a still-running model.
	}
	return m.popAndSubmit()
}

// popAndSubmit pops the oldest staged follow-up, clears any pause, and submits it
// through the EXISTING submitPrompt path (server-side StartRunContent reopen) — the
// single shared body behind both the auto-drain (drainQueue) and the manual resume
// (resumeQueue). submitPrompt sets phaseRunning and opens a fresh stream whose own
// ResultMsg re-enters drainQueue, giving a one-at-a-time FIFO drain.
func (m Model) popAndSubmit() (tea.Model, tea.Cmd) {
	m.queuePaused = ""
	next := m.queued[0]
	m.queued = m.queued[1:]
	m.ta.SetValue(next)
	if m.pendingMode != "" {
		m.statusMsg = m.deps.Theme.Style("warning").Render("mode " + m.pendingMode + " will apply before the queued prompt — press enter to continue")
		m.queuePaused = "mode"
		return m, nil
	}
	return m.submitPrompt()
}

// resumeQueue is the MANUAL counterpart to the auto-drain: it fires the next staged
// follow-up when the user presses enter on an empty line while the queue is paused
// (a run ended on a non-clean stop; see onIdleKey). It shares popAndSubmit with
// drainQueue so the two triggers — automatic on a healthy completion, manual on
// resume — go through one body and one send path. Callers gate on a non-empty,
// paused queue; this assumes m.queued is non-empty.
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
	sel := m.activeModel // the reconciled apply-on-next-create selection (zero ⇒ server default)
	return func() tea.Msg {
		id, caps, resolved, err := deps.Session.CreateSession(deps.Ctx, sel, m.desiredMode())
		if err == nil {
			return client.SessionReadyMsg{SessionID: id, Capabilities: caps, ResolvedModel: resolved, Mode: m.desiredMode()}
		}
		if sel.IsZero() || !client.IsInvalidArgument(err) {
			return client.ConnectErrMsg{Err: err}
		}
		id, caps, resolved, retryErr := deps.Session.CreateSession(deps.Ctx, client.ModelSelection{}, m.desiredMode())
		if retryErr != nil {
			// Both creates failed: the selection wasn't the problem. Surface the
			// ORIGINAL error on the unchanged fatal path.
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
