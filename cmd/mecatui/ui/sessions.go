package ui

import (
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

// sessions.go is the /sessions overlay (issue #245 Phase 3a) — the THIRD
// *selecting* overlay after /models and /worktrees. It lists the stored-session
// inventory (the durable SessionStore's picker metadata), lets the user filter by
// id/model, and on Enter confirms + opens a READ-ONLY replay of the chosen
// session's durable event log. Unlike /worktrees there is no restart handoff: the
// replay is a read-only inspection (phaseReplay); continue-interactive is out of
// scope (Slice 3a). Esc from the transcript view returns to idle with NO live
// session — read-only inspection ends honestly.

// sessionsView is the active /sessions overlay (none = closed). Like /worktrees it
// has a real cursor and an enter-to-confirm step; the transcript arm
// (sessionsTranscript) is the read-only replay view. Slice 3a renders a loading
// card there; Slice 3b renders the drained transcript.
type sessionsView int

const (
	sessionsNone       sessionsView = iota // overlay closed
	sessionsPanel                          // the flat, type-to-filter picker
	sessionsConfirm                        // the post-Enter confirmation overlay (open read-only / undo)
	sessionsTranscript                     // the read-only replay view (3a: loading card; 3b: transcript)
)

// sessionsState holds the /sessions overlay state on the Model. Value-embedded so
// the Model stays a plain struct Update copies; the slices are replaced wholesale
// on each RPC result / filter recompute (never mutated in place).
//
// replayCh/replayStop/replayGen are the replay-stream fan-in seam (parallel to the
// live run's streamCh/streamGen): a goroutine pumps translated tea.Msgs onto
// replayCh via client.ReplayStreamCmd; waitReplayCmd re-arms the fan-in tagged
// with replayGen so a stale reader (left bound to a torn-down replay) is dropped.
// transcript is the conversation the replay projects into (Slice 3b wires the
// projection; 3a drains the msgs honestly without projecting). receivedMsgs counts
// drained replay msgs so the loading card can distinguish "still loading" from
// "loaded but empty" — Slice 3b will replace this with a real transcript render.
type sessionsState struct {
	view         sessionsView
	loading      bool  // the ListSessions RPC is in flight
	err          error // last ListSessions error, rendered distinctly
	sessions     []client.SessionListItem
	filtered     []client.SessionListItem
	filter       textinput.Model        // the type-to-filter input; focused while the picker is open
	cursor       int                    // index into FILTERED (clamped to its bounds)
	confirm      client.SessionListItem // the candidate session when view==sessionsConfirm
	replayCh     chan tea.Msg
	replayStop   func()
	replayGen    uint64
	transcript   conversation
	receivedMsgs int  // count of drained replay msgs (3a: loading-card state)
	replayClosed bool // the replay stream closed (clean EOF or error)
	replayErr    error
}

// openSessions opens the picker and fires the ListSessions RPC. Only callable
// while idle and when BOTH a session lister AND a replayer are wired (the
// transcript handoff needs the replayer; gating on both keeps the overlay honest —
// a lister without a replayer could list sessions it cannot open). Returns the
// model unchanged otherwise. The result arrives as a client.SessionsListedMsg
// handled in updateSessionsMsg.
func (m Model) openSessions() (tea.Model, tea.Cmd) {
	if m.phase != phaseIdle || m.deps.Sessions == nil || m.deps.Replayer == nil {
		return m, nil
	}
	m.ta.Blur() // overlay owns the keyboard while open
	m.sessions.view = sessionsPanel
	m.sessions.loading = true
	m.sessions.err = nil
	m.sessions.cursor = 0
	ti := textinput.New()
	ti.Placeholder = "filter sessions…"
	ti.SetWidth(40)
	ti.Focus()
	m.sessions.filter = ti
	m.sessions.filtered = nil
	return m, tea.Batch(client.ListSessionsCmd(m.deps.Ctx, m.deps.Sessions), textinput.Blink)
}

// closeSessions dismisses the picker/confirm overlay and returns focus to the
// prompt input. It does NOT touch the replay-stream state (replayCh/replayStop/
// transcript) — those are cleared by closeSessionsTranscript on the esc-teardown
// path from phaseReplay, and are otherwise inert when the overlay is at panel/
// confirm (no replay is open until the Enter→switchToSession handoff).
func (m Model) closeSessions() (tea.Model, tea.Cmd) {
	m.sessions.view = sessionsNone
	m.sessions.filter = textinput.Model{}
	m.sessions.filtered = nil
	cmd := m.ta.Focus()
	return m, cmd
}

// onSessionsKey routes key presses while the picker is open. Mirrors onWorktreesKey:
// the filter input is FOCUSED, so nav/action keys are intercepted first and
// everything else feeds the input. esc is two-stage (clear filter, then close).
// The transcript arm (sessionsTranscript) is intentionally NOT handled here: it is a
// read-only replay view owned by the phaseReplay dispatcher (onReplayKey →
// closeSessionsTranscript), so returning handled=false lets the key fall through to
// dispatchPhaseKey's phaseReplay arm. Handling esc here would call closeSessions
// (picker-only teardown), leaking the replay stream + the adopted session id.
func (m Model) onSessionsKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd, bool) {
	if m.sessions.view == sessionsNone {
		return m, nil, false
	}
	if m.sessions.view == sessionsConfirm {
		return m.onSessionsConfirmKey(msg)
	}
	if m.sessions.view == sessionsTranscript {
		// The phaseReplay arm (onReplayKey) owns the transcript view's keys.
		return m, nil, false
	}
	switch {
	case key.Matches(msg, m.keys.Close):
		if m.sessions.filter.Value() != "" {
			m.sessions.filter.SetValue("")
			m = m.syncSessionsFilter()
			return m, nil, true
		}
		mm, cmd := m.closeSessions()
		return mm, cmd, true
	case msg.String() == keyMenuUp:
		if m.sessions.cursor > 0 {
			m.sessions.cursor--
		}
		return m, nil, true
	case msg.String() == keyMenuDown:
		if m.sessions.cursor < len(m.sessions.filtered)-1 {
			m.sessions.cursor++
		}
		return m, nil, true
	case key.Matches(msg, m.keys.ScrollTop):
		m.sessions.cursor = 0
		return m, nil, true
	case key.Matches(msg, m.keys.ScrollBottom):
		m.sessions.cursor = clampModelsCursor(len(m.sessions.filtered)-1, len(m.sessions.filtered))
		return m, nil, true
	case key.Matches(msg, m.keys.Choose):
		return m.chooseSession(), nil, true
	}
	var cmd tea.Cmd
	m.sessions.filter, cmd = m.sessions.filter.Update(msg)
	m = m.syncSessionsFilter()
	return m, cmd, true
}

// syncSessionsFilter recomputes the filtered slice from the filter input and
// clamps the cursor.
func (m Model) syncSessionsFilter() Model {
	m.sessions.filtered = filterSessions(m.sessions.sessions, m.sessions.filter.Value())
	if m.sessions.cursor >= len(m.sessions.filtered) {
		m.sessions.cursor = 0
	}
	return m
}

// filterSessions returns the sessions whose ID or ModelID contains q
// (case-insensitive). An empty query returns the full list.
func filterSessions(sessions []client.SessionListItem, q string) []client.SessionListItem {
	if q == "" {
		return sessions
	}
	needle := strings.ToLower(q)
	out := make([]client.SessionListItem, 0, len(sessions))
	for _, s := range sessions {
		if strings.Contains(strings.ToLower(s.ID), needle) ||
			strings.Contains(strings.ToLower(s.ModelID), needle) {
			out = append(out, s)
		}
	}
	return out
}

// chooseSession handles Enter on the cursor row: opens the confirmation overlay
// (sessionsConfirm) offering open-read-only / undo. A cursor past the list end (or
// an empty list) is a no-op.
func (m Model) chooseSession() Model {
	if m.sessions.cursor < 0 || m.sessions.cursor >= len(m.sessions.filtered) {
		return m
	}
	m.sessions.confirm = m.sessions.filtered[m.sessions.cursor]
	m.sessions.view = sessionsConfirm
	return m
}

// onSessionsConfirmKey routes keys while the confirmation overlay is open. Two
// choices:
//   - enter — OPEN READ-ONLY: adopt the chosen session id and open the replay
//     stream (switchToSession). No live session is created; the transcript is
//     read-only.
//   - esc — UNDO: return to the picker panel without opening.
//
// Any other key is swallowed.
func (m Model) onSessionsConfirmKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd, bool) {
	chosen := m.sessions.confirm
	switch {
	case key.Matches(msg, m.keys.Choose): // enter — open read-only
		return m.switchToSession(chosen)
	case key.Matches(msg, m.keys.Close): // esc — undo
		m.sessions.view = sessionsPanel
		m.sessions.confirm = client.SessionListItem{}
		return m, nil, true
	}
	return m, nil, true
}

// switchToSession performs the read-only handoff for a stored session (issue #245
// Phase 3a): it tears down ALL per-session client state bound to the OLD (live)
// session, resets the conversation transcript, adopts the chosen session id, and
// opens the replay stream via client.ReplayStreamCmd. The adopted session's model
// arrives via the replay (or stays zero — read-only inspection does not rebind a
// live session). The phase moves to phaseReplay (a read-only inspection phase);
// the spinner shows while the first replay msg is pending. Esc from phaseReplay
// closes the transcript view (closeSessionsTranscript): stop the replay, clear
// replay state, resetSession, return to idle with NO live session.
func (m Model) switchToSession(s client.SessionListItem) (tea.Model, tea.Cmd, bool) {
	// Cancel any in-flight run FIRST (safe when idle). The live session is torn down
	// — read-only inspection does not keep it alive.
	m = m.endRun("")

	m = m.resetSession()
	m.sessionID = s.ID
	m.effectiveModel = client.ResolvedModel{}
	m.caps = client.Capabilities{}
	m.restartedThisRun = true // suppress the welcome splash for the rest of the run

	// Open the replay stream over the SessionReplayer interface (NOT the concrete
	// *Client — the ui holds the interface). Bump replayGen so any stale reader from
	// a prior replay is dropped.
	m.sessions.replayGen++
	ch, stop := client.ReplayStreamCmd(m.deps.Ctx, m.deps.Replayer, s.ID)
	m.sessions.replayCh = ch
	m.sessions.replayStop = stop
	m.sessions.receivedMsgs = 0
	m.sessions.replayClosed = false
	m.sessions.replayErr = nil
	m.sessions.transcript = conversation{}
	m.sessions.view = sessionsTranscript
	m.phase = phaseReplay
	m.statusMsg = ""

	// Dismiss the picker/confirm overlay state (filter/filtered/confirm) WITHOUT
	// refocusing the textarea — closeSessions refocuses, but phaseReplay is a
	// read-only view that does not take input. The view is already
	// sessionsTranscript (set above), so the panel/confirm arms no longer render.
	m.sessions.filter = textinput.Model{}
	m.sessions.filtered = nil
	m.sessions.confirm = client.SessionListItem{}
	m.refreshView()
	return m, tea.Batch(m.waitReplayCmd(), m.sp.Tick), true
}

// closeSessionsTranscript is the esc-teardown path from phaseReplay: it stops the
// replay stream, clears the replay-derived state, resets the session-derived
// transcript state, and returns to idle with NO live session (read-only
// inspection ends honestly — the adopted session id is dropped). It does NOT clear
// the inventory picker state (sessions/filtered/filter) — those are
// picker/inventory state like models/worktrees, cleared only by closeSessions when
// the overlay dismisses. Only the replay-derived fields (replayCh/replayStop/
// transcript/receivedMsgs) are cleared here.
func (m Model) closeSessionsTranscript() (tea.Model, tea.Cmd) {
	if m.sessions.replayStop != nil {
		m.sessions.replayStop()
	}
	m.sessions.replayCh = nil
	m.sessions.replayStop = nil
	m.sessions.replayGen++ // invalidate any stale reader
	m.sessions.transcript = conversation{}
	m.sessions.receivedMsgs = 0
	m.sessions.replayClosed = false
	m.sessions.replayErr = nil
	m.sessions.view = sessionsNone
	m = m.resetSession() // drop the adopted session id + transcript
	m.phase = phaseIdle
	m.statusMsg = ""
	cmd := m.ta.Focus()
	m.refreshView()
	return m, cmd
}

// updateSessionsMsg reduces a client.SessionsListedMsg (the ListSessions RPC
// result): it stores the list, derives the filtered slice, clears loading, and
// keeps the overlay open. On error it records the error and clears loading (the
// panel renders an error line). Returns handled=false for any non-SessionsListedMsg.
func (m Model) updateSessionsMsg(msg tea.Msg) (tea.Model, bool) {
	sm, ok := msg.(client.SessionsListedMsg)
	if !ok {
		return m, false
	}
	m.sessions.loading = false
	if sm.Err != nil {
		m.sessions.err = sm.Err
		m.sessions.sessions = nil
		m.sessions.filtered = nil
		return m, true
	}
	m.sessions.err = nil
	m.sessions.sessions = sm.Sessions
	m = m.syncSessionsFilter()
	return m, true
}

// stateBadge returns the one-glyph state badge for a stored session's state string
// (the proto SessionSummary.State). Pure: running→"▶", completed→"✓",
// cancelled/failed→"✗", awaiting→"⏸", idle/""→"·".
func stateBadge(state string) string {
	switch state {
	case "running":
		return "▶"
	case "completed":
		return "✓"
	case teamStopReasonCancelled, "failed":
		return "✗"
	case "awaiting":
		return "⏸"
	default: // idle, ""
		return "·"
	}
}

// relativeTime returns a humanised "time since" for a Unix-seconds timestamp,
// pure: ≥24h→"Nd ago", ≥1h→"Nh ago", else→"Nm ago", zero→"—".
func relativeTime(unixSec int64) string {
	if unixSec <= 0 {
		return "—"
	}
	d := time.Since(time.Unix(unixSec, 0))
	switch {
	case d >= 24*time.Hour:
		return strconv.Itoa(int(d/(24*time.Hour))) + "d ago"
	case d >= time.Hour:
		return strconv.Itoa(int(d/time.Hour)) + "h ago"
	default:
		return strconv.Itoa(int(d/time.Minute)) + "m ago"
	}
}

// renderSessionsOverlay draws the picker, its post-Enter confirmation, or the
// read-only transcript view. Mirrors renderWorktreesOverlay's centred-card shape.
// The transcript arm (3a) renders a loading card; 3b will render the drained
// transcript.
func renderSessionsOverlay(th theme.Theme, st sessionsState, caps client.Capabilities, width, height int) string {
	switch st.view {
	case sessionsConfirm:
		return renderSessionsConfirm(th, st, width, height)
	case sessionsTranscript:
		return renderSessionsTranscript(th, st, width, height)
	default:
		return renderSessionsPanel(th, st, caps, width, height)
	}
}

// renderSessionsPanel renders the session list card.
func renderSessionsPanel(th theme.Theme, st sessionsState, _ client.Capabilities, _, _ int) string {
	var b strings.Builder
	b.WriteString(th.Style("title").Render("sessions") + "\n")
	b.WriteString(th.Style("muted").Render("select a stored session to open read-only") + "\n\n")
	if st.loading {
		b.WriteString(th.Style("muted").Render("loading…"))
		return b.String()
	}
	if st.err != nil {
		b.WriteString(th.Style("errorText").Render("could not list sessions: " + sanitizeTerminal(st.err.Error())))
		b.WriteString("\n" + th.Style("muted").Render("esc: close"))
		return b.String()
	}
	if len(st.filtered) == 0 {
		if st.filter.Value() != "" {
			b.WriteString(th.Style("muted").Render("no matches — clear filter to see all"))
		} else {
			b.WriteString(th.Style("muted").Render("no sessions found"))
		}
		b.WriteString("\n" + th.Style("muted").Render("esc: close"))
		return b.String()
	}
	for i, s := range st.filtered {
		marker := "  "
		if i == st.cursor {
			marker = "▶ "
		}
		line := marker + stateBadge(s.State) + " " + relativeTime(s.ModifiedAt) +
			" " + strconv.Itoa(int(s.Turns)) + "t " + sanitizeTerminal(s.ID)
		if s.ModelID != "" {
			line += "  (" + sanitizeTerminal(s.ModelID) + ")"
		}
		if i == st.cursor {
			line = th.Style("accent").Render(line)
		}
		b.WriteString(line + "\n")
	}
	b.WriteString("\n" + th.Style("muted").Render("enter: open read-only  esc: close"))
	return b.String()
}

// renderSessionsConfirm renders the post-Enter confirmation card.
func renderSessionsConfirm(th theme.Theme, st sessionsState, _, _ int) string {
	s := st.confirm
	var b strings.Builder
	b.WriteString(th.Style("title").Render("open session") + "\n\n")
	b.WriteString("open a read-only transcript of:\n")
	b.WriteString(th.Style("accent").Render("  "+sanitizeTerminal(s.ID)) + "\n")
	b.WriteString(th.Style("muted").Render("  state: "+s.State) + "\n")
	if s.ModelID != "" {
		b.WriteString(th.Style("muted").Render("  model: "+sanitizeTerminal(s.ModelID)) + "\n")
	}
	b.WriteString("\n" + th.Style("muted").Render("enter: open read-only  esc: back"))
	return b.String()
}

// renderSessionsTranscript renders the read-only replay view. Slice 3a renders a
// loading card ("loading transcript for <id>…" / "transcript loaded" / the replay
// error line); Slice 3b will render the drained transcript via the block
// renderers over sessions.transcript.
func renderSessionsTranscript(th theme.Theme, st sessionsState, _, _ int) string {
	var b strings.Builder
	b.WriteString(th.Style("title").Render("session transcript") + "\n\n")
	if st.replayErr != nil {
		b.WriteString(th.Style("errorText").Render("replay error: " + sanitizeTerminal(st.replayErr.Error())))
		b.WriteString("\n" + th.Style("muted").Render("esc: close"))
		return b.String()
	}
	if st.replayClosed {
		b.WriteString(th.Style("muted").Render("transcript loaded"))
	} else {
		b.WriteString(th.Style("muted").Render("loading transcript for " + sanitizeTerminal(st.confirm.ID) + "…"))
	}
	b.WriteString("\n" + th.Style("muted").Render("esc: close"))
	return b.String()
}
