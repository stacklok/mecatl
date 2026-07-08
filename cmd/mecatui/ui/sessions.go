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

// sessions.go is the /sessions overlay (issue #245) — the THIRD *selecting*
// overlay after /models and /worktrees. It lists the stored-session inventory
// (the durable SessionStore's picker metadata), lets the user filter by
// id/model, and on Enter confirms + opens the chosen session. Opening a TOP-LEVEL
// agent session CONTINUES BY DEFAULT: it binds the session id and drops straight
// to phaseIdle (live/interactive) — the user types immediately, and the server
// replays the full history to the model on the first prompt (loadAndReopen).
// The prior transcript is NOT shown in the TUI scrollback (a fresh conversation
// view binds the existing server session). This eliminates the read-only
// phaseReplay mode for top-level sessions. CHILD sessions (subagent/team/parallel,
// opened from the Children tab) stay READ-ONLY: they open the replay stream and
// enter phaseReplay (a read-only transcript inspection), since continuing a child
// as a top-level live session is incoherent (no parent context). Esc from a
// child's transcript view returns to idle with NO live session — read-only
// inspection ends honestly.

// Child-session id prefixes (the wire-side convention from
// engine/agent/childregistry.go: SubagentSessionPrefix/TeamSessionPrefix/
// ParallelSessionPrefix). Mirrored here as plain strings because the ui package
// must not import engine/agent — the id is an opaque string on the wire and the
// prefix is a stable documented contract. A top-level session id is a crypto-random
// hex string with NONE of these prefixes.
const (
	subagentIDPrefix = "subagent-"
	teamIDPrefix     = "team-"
	parallelIDPrefix = "parallel-"
)

// sessionsTab selects which slice the /sessions overlay's panel shows. Mirrors the
// agents_overlay.go tab pattern: ONE surface with two tabs — Sessions (top-level
// agent sessions only, where Continue is meaningful) and Children (subagent +
// team-member + parallel-branch sessions, read-only inspection). The default tab is
// always Sessions (the common case is finding a past agent session); the Children
// tab is opt-in via `tab`.
type sessionsTab int

const (
	tabSessions sessionsTab = iota // top-level agent sessions (no child prefix)
	tabChildren                    // child sessions (subagent-/team-/parallel-), read-only
)

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
	tab          sessionsTab // active panel tab (Sessions | Children); default tabSessions
	loading      bool        // the ListSessions RPC is in flight
	err          error       // last ListSessions error, rendered distinctly
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
	m.sessions.tab = tabSessions // default tab: top-level agent sessions (the common case)
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
	m.sessions.tab = tabSessions
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
	// `tab` switches the panel tab (Sessions↔Children), mirroring the agents
	// overlay's switchAgentsTab. Only from the panel (not the transcript view —
	// onReplayKey owns its keys; tab does nothing there).
	if key.Matches(msg, m.keys.NextTab) {
		m = m.switchSessionsTab()
		return m, nil, true
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

// syncSessionsFilter recomputes the filtered slice from the active tab + the filter
// input and clamps the cursor. The tab split is the FIRST filter: the Sessions tab
// shows only top-level sessions (no child prefix), the Children tab shows only
// child sessions (subagent-/team-/parallel-). The text filter then narrows within
// the tab's slice by id/model.
func (m Model) syncSessionsFilter() Model {
	tabbed := filterSessionsByTab(m.sessions.sessions, m.sessions.tab)
	m.sessions.filtered = filterSessions(tabbed, m.sessions.filter.Value())
	if m.sessions.cursor >= len(m.sessions.filtered) {
		m.sessions.cursor = 0
	}
	return m
}

// filterSessionsByTab returns the slice for the active tab: tabSessions keeps
// top-level ids (no child prefix), tabChildren keeps child ids (subagent-/team-/
// parallel-). Pure; an empty input passes through.
func filterSessionsByTab(sessions []client.SessionListItem, tab sessionsTab) []client.SessionListItem {
	if tab == tabChildren {
		out := make([]client.SessionListItem, 0, len(sessions))
		for _, s := range sessions {
			if isChildSessionID(s.ID) {
				out = append(out, s)
			}
		}
		return out
	}
	out := make([]client.SessionListItem, 0, len(sessions))
	for _, s := range sessions {
		if !isChildSessionID(s.ID) {
			out = append(out, s)
		}
	}
	return out
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
//   - enter — OPEN: for a top-level session, continue it interactively
//     (switchToSession → continueSessionDirect → phaseIdle); for a child
//     session, open the read-only replay transcript (switchToSession →
//     phaseReplay).
//   - esc — UNDO: return to the picker panel without opening.
//
// Any other key is swallowed.
func (m Model) onSessionsConfirmKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd, bool) {
	chosen := m.sessions.confirm
	switch {
	case key.Matches(msg, m.keys.Choose): // enter — open (continue top-level / read-only child)
		if !isSessionOpenable(chosen.State) {
			m.statusMsg = m.deps.Theme.Style("warning").Render(
				"session " + sanitizeTerminal(chosen.ID) + " is currently " + chosen.State +
					" — cannot open while active",
			)
			return m, nil, true // confirm overlay stays open so the user can esc back
		}
		return m.switchToSession(chosen)
	case key.Matches(msg, m.keys.Close): // esc — undo
		m.sessions.view = sessionsPanel
		m.sessions.confirm = client.SessionListItem{}
		return m, nil, true
	}
	return m, nil, true
}

// switchToSession performs the open handoff for a stored session (issue #245).
// The path branches on whether the chosen session is a TOP-LEVEL agent session
// or a CHILD (subagent/team/parallel):
//
//   - TOP-LEVEL (no child prefix): CONTINUE BY DEFAULT — adopt the session id
//     and go STRAIGHT to phaseIdle (live/interactive) WITHOUT opening a replay
//     stream. The user sees no prior transcript in the scrollback (a fresh
//     conversation view), but the first prompt they send goes to the server,
//     which loadAndReopens the session and replays the full history to the
//     model — so the model has the context. This eliminates the read-only
//     phaseReplay mode for top-level sessions entirely: the user types
//     immediately. (This is the old continueSession body, minus the
//     transcript-carry, since there is no transcript to carry — it's a fresh
//     start that binds the existing server session.)
//
//   - CHILD (subagent-/team-/parallel- prefix): READ-ONLY TRANSCRIPT — open the
//     replay stream via client.ReplayStreamCmd, project events into
//     m.sessions.transcript, and enter phaseReplay (read-only inspection). A
//     child session has no coherent parent context to continue as a live
//     top-level session, so it stays read-only. Esc from phaseReplay closes the
//     transcript view (closeSessionsTranscript).
func (m Model) switchToSession(s client.SessionListItem) (tea.Model, tea.Cmd, bool) {
	// Cancel any in-flight run FIRST (safe when idle). The live session is torn
	// down — the adopted session owns the screen next.
	m = m.endRun("")

	if !isChildSessionID(s.ID) {
		// Top-level: continue by default. Bind the server session id and drop to
		// phaseIdle so the user can type immediately. No replay stream — the
		// server replays history to the model on the first prompt
		// (loadAndReopen). The conversation view starts fresh (the prior
		// transcript is NOT shown in the TUI scrollback).
		return m.continueSessionDirect(s)
	}

	// Child: read-only transcript via the replay stream. Adopt the id, open the
	// replay, enter phaseReplay.
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

// continueSessionDirect is the top-level continue-by-default handoff: it adopts
// the given session id, drops to phaseIdle with the textarea focused, and sets a
// "continuing session <id>" status. It does NOT open a replay stream and does
// NOT carry a transcript (there is none — the conversation view starts fresh).
// The server replays the full history to the model on the first prompt the user
// sends (loadAndReopen reopens/recovers the session for the live turn). This is
// the direct-continue path that eliminates the read-only phaseReplay mode for
// top-level sessions.
func (m Model) continueSessionDirect(s client.SessionListItem) (tea.Model, tea.Cmd, bool) {
	m = m.resetSession() // fresh conversation view (no prior transcript carried)
	m.sessionID = s.ID
	m.effectiveModel = client.ResolvedModel{}
	m.caps = client.Capabilities{}
	m.restartedThisRun = true // suppress the welcome splash for the rest of the run
	m.stuck = true            // arm auto-follow so the live tail sticks once a new turn starts

	// Dismiss the picker/confirm overlay state (filter/filtered/confirm).
	m.sessions.view = sessionsNone
	m.sessions.filter = textinput.Model{}
	m.sessions.filtered = nil
	m.sessions.confirm = client.SessionListItem{}

	m.phase = phaseIdle
	if s.Title != "" {
		m.statusMsg = "continuing session " + sanitizeTerminal(s.ID) + " — " + sanitizeTerminal(s.Title) + " — type to add a turn"
	} else {
		m.statusMsg = "continuing session " + sanitizeTerminal(s.ID) + " — type to add a turn"
	}
	cmd := m.ta.Focus()
	m.refreshView()
	return m, cmd, true
}

// closeSessionsTranscript is the esc-teardown path from phaseReplay (now only
// reached for CHILD sessions opened from the Children tab): it stops the replay
// stream, clears the replay-derived state, resets the session-derived transcript
// state, and returns to idle with NO live session (read-only inspection ends
// honestly — the adopted session id is dropped). It does NOT clear the inventory
// picker state (sessions/filtered/filter) — those are picker/inventory state like
// models/worktrees, cleared only by closeSessions when the overlay dismisses.
// Only the replay-derived fields (replayCh/replayStop/transcript/receivedMsgs)
// are cleared here.
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
	m.sessionID = ""
	m.phase = phaseIdle
	m.statusMsg = ""
	cmd := m.ta.Focus()
	m.refreshView()
	return m, cmd
}

// updateSessionsMsg reduces a client.SessionsListedMsg (the ListSessions RPC
// result): it stores the list (minus the current adopted session, see
// excludeCurrentSession), derives the filtered slice, clears loading, and keeps
// the overlay open. On error it records the error and clears loading (the panel
// renders an error line). Returns handled=false for any non-SessionsListedMsg.
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
	m.sessions.sessions = excludeCurrentSession(sm.Sessions, m.sessionID)
	m = m.syncSessionsFilter()
	return m, true
}

// excludeCurrentSession drops the current live session (matched by id) from the
// picker's master list, so a freshly-created, still-empty session never appears
// in /sessions and never sorts to the top of the newest-first ordering — a user
// opening /sessions wants to find a PAST session, not the one they are in. The
// exclusion happens once here (the master-list write), so it holds for both the
// initial listing and every subsequent filterSessions recompute without needing
// to thread currentID through the filter path.
func excludeCurrentSession(sessions []client.SessionListItem, currentID string) []client.SessionListItem {
	if currentID == "" {
		return sessions
	}
	out := make([]client.SessionListItem, 0, len(sessions))
	for _, s := range sessions {
		if s.ID == currentID {
			continue
		}
		out = append(out, s)
	}
	return out
}

// isSessionOpenable reports whether a stored session in the given state may be
// opened read-only. The replay RPC (StreamSessionEvents) is a pure durable-log
// read with NO live-tail: it streams what has been appended so far and ends at
// the log's current tail. Opening a running/awaiting session would therefore
// show a PARTIAL transcript with no terminal EvResult — the honest posture is
// to block it at the UI level and tell the user why. This is a best-effort
// client-side gate: a session that transitions to running between the
// ListSessions call and the open will still replay successfully (a partial
// transcript ending at the log's current tail) — the gate closes the common
// case, not the race.
func isSessionOpenable(state string) bool {
	return state != "running" && state != "awaiting"
}

// isChildSessionID reports whether the session id carries a child-session prefix
// (subagent-/team-/parallel-). A top-level agent session id is a crypto-random
// hex string with NONE of these prefixes. Pure: consults only the id string, so it
// is safe to call on any session id (the wire-side prefix convention is documented
// in engine/agent/childregistry.go and mirrored here as plain string constants).
func isChildSessionID(id string) bool {
	return strings.HasPrefix(id, subagentIDPrefix) ||
		strings.HasPrefix(id, teamIDPrefix) ||
		strings.HasPrefix(id, parallelIDPrefix)
}

// childTypeLabel returns a short human-readable type label for a child session's
// id, shown in the Children tab so subagents, team members, and parallel branches
// are distinguishable: "subagent", "team", "parallel". A top-level id returns "".
func childTypeLabel(id string) string {
	switch {
	case strings.HasPrefix(id, subagentIDPrefix):
		return "subagent"
	case strings.HasPrefix(id, teamIDPrefix):
		return "team"
	case strings.HasPrefix(id, parallelIDPrefix):
		return "parallel"
	default:
		return ""
	}
}

// switchSessionsTab cycles the active panel tab (Sessions→Children→Sessions) and
// clamps the cursor to the now-active tab's filtered slice. Mirrors
// switchAgentsTab: a clean tab switch, no sub-view carryover. It does NOT close the
// overlay.
func (m Model) switchSessionsTab() Model {
	if m.sessions.tab == tabSessions {
		m.sessions.tab = tabChildren
	} else {
		m.sessions.tab = tabSessions
	}
	// Re-derive the filtered slice for the new tab and clamp the cursor (the
	// previous tab's cursor may be past the new slice's end).
	m = m.syncSessionsFilter()
	return m
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
// The transcript arm (3b) renders the drained transcript: the viewport content
// (populated by refreshView from m.sessions.transcript) framed by a header line
// and a "c: continue  esc: back" hint, falling back to the loading/error card
// when there is nothing to render yet. sessionID is the adopted session's id
// (m.sessionID), shown in the transcript header — distinct from st.confirm.ID
// (the picker candidate, cleared on the switchToSession handoff).
func renderSessionsOverlay(th theme.Theme, st sessionsState, caps client.Capabilities, sessionID, vpContent string, width, height int) string {
	switch st.view {
	case sessionsConfirm:
		return renderSessionsConfirm(th, st, width, height)
	case sessionsTranscript:
		return renderSessionsTranscript(th, st, sessionID, vpContent, width, height)
	default:
		return renderSessionsPanel(th, st, caps, width, height)
	}
}

// sessionsTabBar renders the "Sessions | Children" tab strip: the active tab in
// the title style with a leading "▸" marker, the inactive ones muted. Mirrors
// agentsTabBar's glyph+style so stripANSI goldens still show which is active.
func sessionsTabBar(th theme.Theme, tab sessionsTab) string {
	active := th.Style("askTitle")
	muted := th.Style("muted")
	seg := func(t sessionsTab, label string) string {
		if tab == t {
			return active.Render("▸ " + label)
		}
		return muted.Render("  " + label)
	}
	return seg(tabSessions, "Sessions") + muted.Render("  ") +
		seg(tabChildren, "Children")
}

// renderSessionsPanel renders the session list card with a tab bar (Sessions |
// Children) above the rows. The active tab selects which slice shows: Sessions
// (top-level agent sessions) or Children (subagent/team/parallel). In the Children
// tab each row carries a one-glyph type badge (S/T/P) + a short type label so the
// child kinds are distinguishable.
func renderSessionsPanel(th theme.Theme, st sessionsState, _ client.Capabilities, _, _ int) string {
	var b strings.Builder
	b.WriteString(sessionsTabBar(th, st.tab) + "\n\n")
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
		} else if st.tab == tabChildren {
			b.WriteString(th.Style("muted").Render("no child sessions (subagent/team/parallel) found"))
		} else {
			b.WriteString(th.Style("muted").Render("no sessions found"))
		}
		b.WriteString("\n" + th.Style("muted").Render("tab: switch  esc: close"))
		return b.String()
	}
	for i, s := range st.filtered {
		marker := "  "
		if i == st.cursor {
			marker = "▶ "
		}
		// Title leads the row (falls back to the id when absent); the id stays on
		// the confirm card for precise identification.
		label := s.Title
		if label == "" {
			label = s.ID
		}
		line := marker + stateBadge(s.State) + " " + relativeTime(s.ModifiedAt) +
			" " + strconv.Itoa(int(s.Turns)) + "t " + sanitizeTerminal(label)
		if s.ModelID != "" {
			line += "  (" + sanitizeTerminal(s.ModelID) + ")"
		}
		// In the Children tab, append a short type label so subagents/team-members/
		// parallel-branches are distinguishable (the one-glyph badge alone is cryptic).
		if st.tab == tabChildren {
			if tl := childTypeLabel(s.ID); tl != "" {
				line += "  [" + tl + "]"
			}
		}
		if i == st.cursor {
			line = th.Style("accent").Render(line)
		}
		b.WriteString(line + "\n")
	}
	hint := "enter: continue  tab: switch  esc: close"
	if st.tab == tabChildren {
		// Children are read-only inspection only.
		hint = "enter: open read-only  tab: switch  esc: close"
	}
	b.WriteString("\n" + th.Style("muted").Render(hint))
	return b.String()
}

// renderSessionsConfirm renders the post-Enter confirmation card. The wording
// branches on whether the chosen session is a top-level agent session (continue
// by default — the Enter handoff binds the id and drops to a live interactive
// session) or a child (read-only transcript inspection).
func renderSessionsConfirm(th theme.Theme, st sessionsState, _, _ int) string {
	s := st.confirm
	var b strings.Builder
	if isChildSessionID(s.ID) {
		b.WriteString(th.Style("title").Render("open session") + "\n\n")
		b.WriteString("open a read-only transcript of:\n")
	} else {
		b.WriteString(th.Style("title").Render("continue session") + "\n\n")
		b.WriteString("continue the session:\n")
	}
	b.WriteString(th.Style("accent").Render("  "+sanitizeTerminal(s.ID)) + "\n")
	if s.Title != "" {
		b.WriteString(th.Style("muted").Render("  title: "+sanitizeTerminal(s.Title)) + "\n")
	}
	b.WriteString(th.Style("muted").Render("  state: "+sanitizeTerminal(s.State)) + "\n")
	if s.ModelID != "" {
		b.WriteString(th.Style("muted").Render("  model: "+sanitizeTerminal(s.ModelID)) + "\n")
	}
	if isChildSessionID(s.ID) {
		b.WriteString("\n" + th.Style("muted").Render("enter: open read-only  esc: back"))
	} else {
		b.WriteString("\n" + th.Style("muted").Render("enter: continue  esc: back"))
	}
	return b.String()
}

// renderSessionsTranscript renders the read-only replay view (now only reached
// for CHILD sessions opened from the Children tab). Slice 3b renders the drained
// transcript: the viewport content (the block renderers' projection of
// m.sessions.transcript, populated by refreshView) framed by a "session <id> ·
// read-only transcript" header and an "esc: back" hint. While the replay is still
// loading (no content, not closed, no error) it renders the loading card; on a
// replay error it renders the error line; once the stream closes (or content has
// arrived) it renders the transcript. The transcript is a STATIC viewport
// (read-only, no auto-follow streaming dynamics — the replay is a bounded batch).
// The "c: continue" hint has been REMOVED: top-level sessions continue by
// default (never entering phaseReplay), and children cannot be continued.
func renderSessionsTranscript(th theme.Theme, st sessionsState, sessionID, vpContent string, _, _ int) string {
	hint := "esc: back"
	// Loading arm: nothing projected yet AND the stream has not closed → the
	// loading card. Once the first msg lands (transcript non-empty) OR the stream
	// closes (replayClosed), render the transcript view.
	if st.transcript.isEmpty() && !st.replayClosed {
		var b strings.Builder
		b.WriteString(th.Style("title").Render("session transcript") + "\n\n")
		b.WriteString(th.Style("muted").Render("loading transcript for " + sanitizeTerminal(sessionID) + "…"))
		b.WriteString("\n" + th.Style("muted").Render(hint))
		return b.String()
	}
	// Transcript arm: the header line + the projected transcript blocks (which
	// include any replay-error line projected by updateReplayMsg's StreamErrMsg
	// arm, so a partial projection before the error survives) + the esc hint.
	// The transcript conversation's isEmpty() is the honest emptiness signal: the
	// viewport pads empty content to its height with blank lines, so vpContent is
	// never truly "" for a sized viewport. An empty transcript (a clean EOF over
	// zero events, or only an error with no prior content) renders the error card
	// (when replayErr is set) or a "no events" note.
	if st.transcript.isEmpty() && st.replayErr != nil {
		var b strings.Builder
		b.WriteString(th.Style("title").Render("session transcript") + "\n\n")
		b.WriteString(th.Style("errorText").Render("replay error: " + sanitizeTerminal(st.replayErr.Error())))
		b.WriteString("\n" + th.Style("muted").Render(hint))
		return b.String()
	}
	var b strings.Builder
	b.WriteString(th.Style("muted").Render("session " + sanitizeTerminal(sessionID) + " · read-only transcript"))
	b.WriteString("\n")
	if st.transcript.isEmpty() {
		b.WriteString(th.Style("muted").Render("(no events in this session)"))
		b.WriteString("\n")
	} else {
		b.WriteString(vpContent)
	}
	b.WriteString(th.Style("muted").Render(hint))
	return b.String()
}
