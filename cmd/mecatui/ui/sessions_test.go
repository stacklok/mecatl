package ui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

// sessions_test.go mirrors worktrees_test.go for the /sessions overlay (issue #245
// Phase 3a). The fakes are proto-free at the SEAM the ui holds: a fake
// SessionLister (client.SessionLister) and a fake SessionReplayer
// (client.SessionReplayer) returning a *client.EventStream over a scripted
// client.EventRecver (the exported client.FakeEventStream, so the ui tests stay
// self-contained AND proto-free — the ui package must never import the proto
// package, even in tests). The ui never sees a proto type.

// fakeSessionLister is a spy client.SessionLister: returns a fixed slice or a
// fixed error, recording the call count.
type fakeSessionLister struct {
	sessions []client.SessionListItem
	err      error
	calls    int
}

func (f *fakeSessionLister) ListSessions(_ context.Context) ([]client.SessionListItem, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return f.sessions, nil
}

// fakeSessionReplayer is a spy client.SessionReplayer: StreamSessionEvents returns
// a *client.EventStream over a scripted client.FakeEventStream (an EventRecver) or
// a configured open error. It records the id it was called with so the handoff
// test can assert it. The scripted EventRecver comes from the client package (an
// exported test fake) so the ui test never names a proto type.
type fakeSessionReplayer struct {
	stream *client.FakeEventStream
	err    error
	calls  int
	lastID string
}

func (f *fakeSessionReplayer) StreamSessionEvents(_ context.Context, id string) (*client.EventStream, error) {
	f.calls++
	f.lastID = id
	if f.err != nil {
		return nil, f.err
	}
	return client.NewEventStream(f.stream), nil
}

// newSessionsModel builds a Model wired with a fakeConv + a session lister + a
// replayer, driven through the connect (SessionReadyMsg) so it is idle and ready.
func newSessionsModel(t *testing.T, conv *fakeConv, fl *fakeSessionLister, fr *fakeSessionReplayer) Model {
	t.Helper()
	deps := Deps{
		Session:     conv,
		Conv:        conv,
		Sessions:    fl,
		Replayer:    fr,
		Theme:       theme.New("aztec", theme.AztecPalette()),
		Workspace:   "/workspace",
		Mode:        "default",
		Model:       "mock-model",
		Ctx:         context.Background(),
		NoAltScreen: true,
	}
	if fl == nil {
		deps.Sessions = nil
	}
	if fr == nil {
		deps.Replayer = nil
	}
	m := New(deps)
	m = applyAll(
		m,
		tea.WindowSizeMsg{Width: 100, Height: 40},
		client.SessionReadyMsg{SessionID: "sess-test-0001", Capabilities: client.Capabilities{}},
	)
	return m
}

func newSessionsConv() *fakeConv {
	return &fakeConv{recv: &fakeRecver{gate: make(chan struct{})}, send: &fakeSender{}, caps: client.Capabilities{}}
}

// sampleSessions is a small fixed inventory for the picker tests.
func sampleSessions() []client.SessionListItem {
	return []client.SessionListItem{
		{ID: "sess-aaa", ModifiedAt: nowMinusMinutes(5), State: "completed", Turns: 12, ModelID: "gpt-5"},
		{ID: "sess-bbb", ModifiedAt: nowMinusMinutes(60), State: "running", Turns: 3, ModelID: "claude-opus"},
	}
}

// nowMinusMinutes returns the Unix seconds for N minutes ago (for relativeTime).
func nowMinusMinutes(n int) int64 {
	return time.Now().Add(-time.Duration(n) * time.Minute).Unix()
}

// TestRunSessionsOpensOverlay asserts runSessions opens the picker, blurs the
// input, fires ListSessions, and renders the rows once the result lands.
func TestRunSessionsOpensOverlay(t *testing.T) {
	fl := &fakeSessionLister{sessions: sampleSessions()}
	conv := newSessionsConv()
	m := newSessionsModel(t, conv, fl, &fakeSessionReplayer{})

	mm, cmd := m.runSessions()
	m = mm.(Model)
	if m.sessions.view != sessionsPanel {
		t.Fatalf("view = %v, want sessionsPanel", m.sessions.view)
	}
	if !m.sessions.loading {
		t.Error("overlay should be loading until ListSessions lands")
	}
	if m.ta.Focused() {
		t.Error("opening the overlay should blur the textarea")
	}
	if cmd == nil {
		t.Fatal("runSessions should fire the ListSessions RPC command")
	}
	m = feedCmd(t, m, cmd)
	if fl.calls != 1 {
		t.Errorf("ListSessions calls = %d, want 1", fl.calls)
	}
	if !strings.Contains(m.View().Content, "sess-aaa") {
		t.Errorf("overlay missing a session id:\n%s", m.View().Content)
	}
}

// TestSessionsPanelRendersTitleLeadingRow asserts the picker row renders the
// session Title (when set) as the leading label, falling back to the ID when
// the Title is empty. The ID stays on the confirm card for precise ID.
func TestSessionsPanelRendersTitleLeadingRow(t *testing.T) {
	t.Run("title leads when set", func(t *testing.T) {
		sessions := []client.SessionListItem{
			{ID: "sess-aaa", Title: "Fix the flaky CI", ModifiedAt: nowMinusMinutes(5), State: "completed", Turns: 12, ModelID: "gpt-5"},
		}
		fl := &fakeSessionLister{sessions: sessions}
		conv := newSessionsConv()
		m := newSessionsModel(t, conv, fl, &fakeSessionReplayer{})
		mm, cmd := m.runSessions()
		m = mm.(Model)
		m = feedCmd(t, m, cmd)

		content := m.View().Content
		// The Title leads the row.
		if !strings.Contains(content, "Fix the flaky CI") {
			t.Errorf("row missing the Title label:\n%s", content)
		}
		// The ID is NOT the row label when a Title is set (it still appears on the
		// confirm card after Enter, but the panel row shows the title).
		// Sanity: the row still carries the model id parenthetical.
		if !strings.Contains(content, "(gpt-5)") {
			t.Errorf("row missing the model id parenthetical:\n%s", content)
		}
	})
	t.Run("id leads when title empty", func(t *testing.T) {
		sessions := []client.SessionListItem{
			{ID: "sess-bbb", Title: "", ModifiedAt: nowMinusMinutes(5), State: "completed", Turns: 3},
		}
		fl := &fakeSessionLister{sessions: sessions}
		conv := newSessionsConv()
		m := newSessionsModel(t, conv, fl, &fakeSessionReplayer{})
		mm, cmd := m.runSessions()
		m = mm.(Model)
		m = feedCmd(t, m, cmd)
		if !strings.Contains(m.View().Content, "sess-bbb") {
			t.Errorf("row missing the ID fallback label:\n%s", m.View().Content)
		}
	})
}

// TestRunSessionsNilGuard: with no lister wired, openSessions is a no-op.
func TestRunSessionsNilGuard(t *testing.T) {
	conv := newSessionsConv()
	m := New(Deps{
		Session: conv, Conv: conv, Theme: theme.New("aztec", theme.AztecPalette()),
		Workspace: "/ws", Ctx: context.Background(), NoAltScreen: true,
	})
	m = applyAll(m, tea.WindowSizeMsg{Width: 100, Height: 40}, client.SessionReadyMsg{SessionID: "s1"})
	mm, cmd := m.openSessions()
	m = mm.(Model)
	if m.sessions.view != sessionsNone {
		t.Fatalf("openSessions with nil lister should be a no-op, view = %v", m.sessions.view)
	}
	if cmd != nil {
		t.Errorf("openSessions with nil lister should fire no command, got %v", cmd)
	}
}

// TestRunSessionsNotIdle: opening mid-run is a no-op.
func TestRunSessionsNotIdle(t *testing.T) {
	fl := &fakeSessionLister{sessions: sampleSessions()}
	conv := newSessionsConv()
	m := newSessionsModel(t, conv, fl, &fakeSessionReplayer{})
	m.phase = phaseRunning // mid-run
	mm, _ := m.openSessions()
	m = mm.(Model)
	if m.sessions.view != sessionsNone {
		t.Fatalf("openSessions mid-run should be a no-op, view = %v", m.sessions.view)
	}
}

// TestSessionsFilterNarrows: the filter input narrows the list by id/model.
func TestSessionsFilterNarrows(t *testing.T) {
	fl := &fakeSessionLister{sessions: sampleSessions()}
	conv := newSessionsConv()
	m := newSessionsModel(t, conv, fl, &fakeSessionReplayer{})
	mm, _ := m.openSessions()
	m = mm.(Model)
	m = applyAll(m, client.SessionsListedMsg{Sessions: fl.sessions})
	if len(m.sessions.filtered) != 2 {
		t.Fatalf("filtered = %d, want 2 before filter", len(m.sessions.filtered))
	}
	m.sessions.filter.SetValue("gpt-5")
	m = m.syncSessionsFilter()
	if len(m.sessions.filtered) != 1 {
		t.Fatalf("filtered = %d, want 1 after 'gpt-5'", len(m.sessions.filtered))
	}
	if m.sessions.filtered[0].ID != "sess-aaa" {
		t.Errorf("filtered[0] = %+v, want sess-aaa", m.sessions.filtered[0])
	}
}

// TestSessionsListExcludesCurrentSession asserts the current live session
// (m.sessionID, newly created and empty) is dropped from the picker's list — a
// user opening /sessions is looking for a PAST session, and the current one
// would otherwise sort to the top of the newest-first ordering and get clicked
// by mistake.
func TestSessionsListExcludesCurrentSession(t *testing.T) {
	fl := &fakeSessionLister{sessions: sampleSessions()}
	conv := newSessionsConv()
	m := newSessionsModel(t, conv, fl, &fakeSessionReplayer{})
	// newSessionsModel adopts "sess-test-0001" via SessionReadyMsg; inject it into
	// the RPC result as the newest, emptiest entry.
	current := client.SessionListItem{ID: m.sessionID, ModifiedAt: nowMinusMinutes(0), State: "idle", Turns: 0}
	withCurrent := append([]client.SessionListItem{current}, sampleSessions()...)
	mm, _ := m.openSessions()
	m = mm.(Model)
	m = applyAll(m, client.SessionsListedMsg{Sessions: withCurrent})
	if len(m.sessions.sessions) != len(sampleSessions()) {
		t.Fatalf("sessions = %d, want %d (current session excluded)", len(m.sessions.sessions), len(sampleSessions()))
	}
	for _, s := range m.sessions.sessions {
		if s.ID == m.sessionID {
			t.Fatalf("current session %q should be excluded from the picker list", m.sessionID)
		}
	}
	for _, s := range m.sessions.filtered {
		if s.ID == m.sessionID {
			t.Fatalf("current session %q should be excluded from the filtered list", m.sessionID)
		}
	}
}

// TestSessionsEscCloses: esc closes the overlay.
func TestSessionsEscCloses(t *testing.T) {
	fl := &fakeSessionLister{sessions: sampleSessions()}
	conv := newSessionsConv()
	m := newSessionsModel(t, conv, fl, &fakeSessionReplayer{})
	mm, _ := m.openSessions()
	m = mm.(Model)
	m = applyAll(m, client.SessionsListedMsg{Sessions: fl.sessions})
	mm, _, _ = m.onSessionsKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	m = mm.(Model)
	if m.sessions.view != sessionsNone {
		t.Fatalf("esc should close the overlay, view = %v", m.sessions.view)
	}
}

// TestSelectSessionDirectOpenTopLevel asserts the direct-open (no confirm) +
// load-history-on-continue handoff for a TOP-LEVEL session: Enter on the picker
// row opens the session directly — it opens the replay stream (loading the prior
// conversation into m.sessions.transcript while showing a loading view), and on
// stream close (StreamClosedMsg) carries the transcript into m.conv and
// transitions to phaseIdle (live/interactive), focusing the textarea and setting
// a "continuing" status. The prior conversation is loaded into m.conv.
func TestSelectSessionDirectOpenTopLevel(t *testing.T) {
	fl := &fakeSessionLister{sessions: sampleSessions()}
	fr := &fakeSessionReplayer{stream: client.NewFakeEventStream()}
	conv := newSessionsConv()
	m := newSessionsModel(t, conv, fl, fr)

	// Open + load the list.
	mm, _ := m.openSessions()
	m = mm.(Model)
	m = applyAll(m, client.SessionsListedMsg{Sessions: fl.sessions})
	if m.sessions.view != sessionsPanel {
		t.Fatalf("view = %v, want sessionsPanel", m.sessions.view)
	}

	// Cursor on the first row; Enter → direct open (no confirm step).
	m.sessions.cursor = 0
	mm, _, _ = m.onSessionsKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = mm.(Model)
	// The replay stream is opened and the model enters phaseReplay (loading)
	// with continueOnLoad set (top-level → continue on stream close).
	if m.phase != phaseReplay {
		t.Fatalf("after enter: phase = %v, want phaseReplay (loading)", m.phase)
	}
	if !m.sessions.continueOnLoad {
		t.Error("continueOnLoad should be true for a top-level session")
	}
	if m.sessionID != "sess-aaa" {
		t.Fatalf("sessionID = %q, want sess-aaa", m.sessionID)
	}
	if m.sessions.replayCh == nil {
		t.Error("replayCh should be set (replay stream opened to load history)")
	}
	if fr.calls != 1 || fr.lastID != "sess-aaa" {
		t.Fatalf("replayer calls=%d lastID=%q, want 1/sess-aaa", fr.calls, fr.lastID)
	}

	// Stream closes (empty event log) → carry transcript (empty) → phaseIdle.
	mm, _ = m.updateReplayMsg(replayMsg{gen: m.sessions.replayGen, msg: client.StreamClosedMsg{}})
	m = mm.(Model)
	if m.phase != phaseIdle {
		t.Fatalf("after StreamClosed: phase = %v, want phaseIdle (continue-by-default)", m.phase)
	}
	if m.sessionID != "sess-aaa" {
		t.Fatalf("sessionID = %q, want sess-aaa (kept)", m.sessionID)
	}
	if m.sessions.replayCh != nil {
		t.Error("replayCh should be nil after the stream closed")
	}
	if m.sessions.continueOnLoad {
		t.Error("continueOnLoad should be cleared after the handoff")
	}
	if m.sessions.view != sessionsNone {
		t.Fatalf("view = %v, want sessionsNone (overlay dismissed on continue)", m.sessions.view)
	}
	if !m.ta.Focused() {
		t.Error("textarea should be focused after continue")
	}
	if !m.stuck {
		t.Error("stuck should be true (auto-follow armed)")
	}
	if !m.restartedThisRun {
		t.Error("restartedThisRun should be true (suppresses the welcome splash)")
	}
	got := stripANSIstr(m.statusMsg)
	if !strings.Contains(got, "continuing") || !strings.Contains(got, "sess-aaa") {
		t.Errorf("statusMsg = %q, want 'continuing' and 'sess-aaa'", got)
	}
}

// TestSelectSessionDirectOpenTopLevelLoadsHistory asserts that when a top-level
// session WITH replay events is continued, the prior conversation is projected
// into m.conv on stream close (history loaded) — the user sees the prior
// transcript and can type immediately.
func TestSelectSessionDirectOpenTopLevelLoadsHistory(t *testing.T) {
	fl := &fakeSessionLister{sessions: sampleSessions()}
	fr := &fakeSessionReplayer{stream: client.NewFakeEventStream()}
	conv := newSessionsConv()
	m := newSessionsModel(t, conv, fl, fr)

	mm, _ := m.openSessions()
	m = mm.(Model)
	m = applyAll(m, client.SessionsListedMsg{Sessions: fl.sessions})
	m.sessions.cursor = 0
	mm, _, _ = m.onSessionsKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = mm.(Model)
	if m.phase != phaseReplay {
		t.Fatalf("after enter: phase = %v, want phaseReplay (loading)", m.phase)
	}

	// Drive a scripted replay (user prompt + assistant text + tool call/result).
	for _, msg := range replayScriptMsgs() {
		mm, _ := m.updateReplayMsg(replayMsg{gen: m.sessions.replayGen, msg: msg})
		m = mm.(Model)
	}
	// The transcript should now hold the projected blocks.
	if m.sessions.transcript.isEmpty() {
		t.Fatal("transcript should be non-empty after projecting replay events")
	}

	// Stream closes → carry transcript into m.conv → phaseIdle.
	mm, _ = m.updateReplayMsg(replayMsg{gen: m.sessions.replayGen, msg: client.StreamClosedMsg{}})
	m = mm.(Model)
	if m.phase != phaseIdle {
		t.Fatalf("after StreamClosed: phase = %v, want phaseIdle", m.phase)
	}
	if m.conv.isEmpty() {
		t.Fatal("m.conv should be non-empty (history carried from the transcript)")
	}
	// The carried blocks should match the transcript's projection.
	blocks := m.conv.blocks
	if len(blocks) < 5 {
		t.Fatalf("m.conv blocks = %d, want ≥5 (history loaded)", len(blocks))
	}
	if blocks[0].kind != blockUser || blocks[0].raw != "read the greeting file" {
		t.Errorf("block 0 = %+v, want the user prompt", blocks[0])
	}
	// The replay transcript should be cleared (it now lives in m.conv).
	if !m.sessions.transcript.isEmpty() {
		t.Errorf("sessions.transcript should be cleared after the carry, got %d blocks", len(m.sessions.transcript.blocks))
	}
	if !m.ta.Focused() {
		t.Error("textarea should be focused after continue")
	}
}

// TestSelectSessionDirectOpenTopLevelEmptyLogStillContinues asserts that a
// top-level session with an EMPTY event log still continues to phaseIdle (empty
// conv, the user can type) — the server holds the history regardless.
func TestSelectSessionDirectOpenTopLevelEmptyLogStillContinues(t *testing.T) {
	fl := &fakeSessionLister{sessions: sampleSessions()}
	fr := &fakeSessionReplayer{stream: client.NewFakeEventStream()}
	conv := newSessionsConv()
	m := newSessionsModel(t, conv, fl, fr)

	mm, _ := m.openSessions()
	m = mm.(Model)
	m = applyAll(m, client.SessionsListedMsg{Sessions: fl.sessions})
	m.sessions.cursor = 0
	mm, _, _ = m.onSessionsKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = mm.(Model)
	if m.phase != phaseReplay {
		t.Fatalf("after enter: phase = %v, want phaseReplay (loading)", m.phase)
	}

	// Empty event log → clean EOF → phaseIdle with empty conv.
	mm, _ = m.updateReplayMsg(replayMsg{gen: m.sessions.replayGen, msg: client.StreamClosedMsg{}})
	m = mm.(Model)
	if m.phase != phaseIdle {
		t.Fatalf("after StreamClosed (empty log): phase = %v, want phaseIdle", m.phase)
	}
	if !m.conv.isEmpty() {
		t.Errorf("m.conv should be empty (no prior history), got %d blocks", len(m.conv.blocks))
	}
	if !m.ta.Focused() {
		t.Error("textarea should be focused (user can type)")
	}
	got := stripANSIstr(m.statusMsg)
	if !strings.Contains(got, "no prior history") {
		t.Errorf("statusMsg = %q, want 'no prior history'", got)
	}
}

// TestSelectSessionDirectOpenTopLevelReplayErrStillContinues asserts that on a
// replay error, a top-level continue still goes to phaseIdle with whatever
// partial transcript loaded (or empty), and the error surfaces in the status —
// the user can still type (the server holds the history regardless).
func TestSelectSessionDirectOpenTopLevelReplayErrStillContinues(t *testing.T) {
	fl := &fakeSessionLister{sessions: sampleSessions()}
	fr := &fakeSessionReplayer{stream: client.NewFakeEventStream()}
	conv := newSessionsConv()
	m := newSessionsModel(t, conv, fl, fr)

	mm, _ := m.openSessions()
	m = mm.(Model)
	m = applyAll(m, client.SessionsListedMsg{Sessions: fl.sessions})
	m.sessions.cursor = 0
	mm, _, _ = m.onSessionsKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = mm.(Model)
	if m.phase != phaseReplay {
		t.Fatalf("after enter: phase = %v, want phaseReplay (loading)", m.phase)
	}

	boom := errors.New("rpc gone")
	mm, _ = m.updateReplayMsg(replayMsg{gen: m.sessions.replayGen, msg: client.StreamErrMsg{Err: boom}})
	m = mm.(Model)
	if m.phase != phaseIdle {
		t.Fatalf("after StreamErrMsg: phase = %v, want phaseIdle (continue anyway)", m.phase)
	}
	if !m.ta.Focused() {
		t.Error("textarea should be focused (user can type despite the replay error)")
	}
	got := stripANSIstr(m.statusMsg)
	if !strings.Contains(got, "replay error") || !strings.Contains(got, "sess-aaa") {
		t.Errorf("statusMsg = %q, want 'replay error' and 'sess-aaa'", got)
	}
}

// TestStateBadge pins the one-glyph state badge for each stored-session state.
func TestStateBadge(t *testing.T) {
	cases := map[string]string{
		"running":   "▶",
		"completed": "✓",
		"cancelled": "✗",
		"failed":    "✗",
		"awaiting":  "⏸",
		"idle":      "·",
		"":          "·",
	}
	for state, want := range cases {
		if got := stateBadge(state); got != want {
			t.Errorf("stateBadge(%q) = %q, want %q", state, got, want)
		}
	}
}

// TestRelativeTime pins the humanised "time since" for representative durations.
func TestRelativeTime(t *testing.T) {
	if got := relativeTime(0); got != "—" {
		t.Errorf("relativeTime(0) = %q, want —", got)
	}
	if got := relativeTime(-1); got != "—" {
		t.Errorf("relativeTime(-1) = %q, want —", got)
	}
	// 5 minutes ago → "5m ago"
	min5 := time.Now().Add(-5 * time.Minute).Unix()
	if got := relativeTime(min5); got != "5m ago" {
		t.Errorf("relativeTime(5m) = %q, want 5m ago", got)
	}
	// 2 hours ago → "2h ago"
	hr2 := time.Now().Add(-2 * time.Hour).Unix()
	if got := relativeTime(hr2); got != "2h ago" {
		t.Errorf("relativeTime(2h) = %q, want 2h ago", got)
	}
	// 3 days ago → "3d ago"
	day3 := time.Now().Add(-72 * time.Hour).Unix()
	if got := relativeTime(day3); got != "3d ago" {
		t.Errorf("relativeTime(3d) = %q, want 3d ago", got)
	}
}

// TestReplayGenGuardsStaleReader asserts a replayMsg whose gen no longer matches
// m.sessions.replayGen is dropped (a stale reader from a torn-down replay cannot
// route into the current view), and NOT re-armed. Uses a CHILD session (which
// opens the replay stream); a top-level session no longer uses replays.
func TestReplayGenGuardsStaleReader(t *testing.T) {
	fl := &fakeSessionLister{sessions: []client.SessionListItem{
		{ID: "subagent-call1", ModifiedAt: nowMinusMinutes(4), State: "completed", Turns: 2, ModelID: "gpt-5"},
	}}
	fr := &fakeSessionReplayer{stream: client.NewFakeEventStream()}
	conv := newSessionsConv()
	m := newSessionsModel(t, conv, fl, fr)
	mm, _, _ := m.switchToSession(fl.sessions[0])
	m = mm.(Model)
	if m.phase != phaseReplay {
		t.Fatalf("setup: phase = %v, want phaseReplay (child)", m.phase)
	}
	before := m.sessions.receivedMsgs
	// A stale-gen replayMsg must be dropped.
	stale := replayMsg{gen: m.sessions.replayGen + 1, msg: client.SessionInitMsg{}}
	mm, cmd := m.updateReplayMsg(stale)
	m = mm.(Model)
	if m.sessions.receivedMsgs != before {
		t.Errorf("stale replayMsg should be dropped, receivedMsgs = %d, want %d", m.sessions.receivedMsgs, before)
	}
	if cmd != nil {
		t.Errorf("stale replayMsg should NOT re-arm, got cmd = %v", cmd)
	}
}

// TestReplayStreamErrShowsError asserts a StreamErrMsg from the replay flips the
// loading card to the error line, stays in phaseReplay, and does not re-arm.
func TestReplayStreamErrShowsError(t *testing.T) {
	fl := &fakeSessionLister{sessions: sampleSessions()}
	conv := newSessionsConv()
	m := newSessionsModel(t, conv, fl, &fakeSessionReplayer{})
	// Manually set up the replay state (switchToSession opens the stream; here we
	// drive updateReplayMsg directly with a StreamErrMsg to test the reducer arm).
	m.sessions.replayGen = 1
	m.sessions.view = sessionsTranscript
	m.phase = phaseReplay
	boom := errors.New("rpc gone")
	mm, cmd := m.updateReplayMsg(replayMsg{gen: 1, msg: client.StreamErrMsg{Err: boom}})
	m = mm.(Model)
	if m.sessions.replayErr == nil {
		t.Error("StreamErrMsg should set replayErr")
	}
	if !m.sessions.replayClosed {
		t.Error("StreamErrMsg should set replayClosed")
	}
	if m.phase != phaseReplay {
		t.Fatalf("phase = %v, want phaseReplay", m.phase)
	}
	if cmd != nil {
		t.Errorf("StreamErrMsg should NOT re-arm, got cmd = %v", cmd)
	}
	if !strings.Contains(m.View().Content, "replay error") {
		t.Errorf("transcript view should render the error line:\n%s", m.View().Content)
	}
}

// TestReplayStreamClosedKeepsTranscript asserts a StreamClosedMsg from the replay
// keeps the transcript rendered (Slice 3b: the full transcript renders, not just a
// "loaded" card), stays in phaseReplay, and does not re-arm. With no prior events
// the transcript is empty, so the view renders the "no events" note.
func TestReplayStreamClosedKeepsTranscript(t *testing.T) {
	fl := &fakeSessionLister{sessions: sampleSessions()}
	conv := newSessionsConv()
	m := newSessionsModel(t, conv, fl, &fakeSessionReplayer{})
	m.sessions.replayGen = 1
	m.sessions.view = sessionsTranscript
	m.phase = phaseReplay
	mm, cmd := m.updateReplayMsg(replayMsg{gen: 1, msg: client.StreamClosedMsg{}})
	m = mm.(Model)
	if !m.sessions.replayClosed {
		t.Error("StreamClosedMsg should set replayClosed")
	}
	if m.phase != phaseReplay {
		t.Fatalf("phase = %v, want phaseReplay", m.phase)
	}
	if cmd != nil {
		t.Errorf("StreamClosedMsg should NOT re-arm, got cmd = %v", cmd)
	}
	if !strings.Contains(m.View().Content, "read-only transcript") {
		t.Errorf("transcript view should render the read-only transcript header:\n%s", m.View().Content)
	}
	if !strings.Contains(m.View().Content, "no events") {
		t.Errorf("an empty closed transcript should render the 'no events' note:\n%s", m.View().Content)
	}
}

// TestSessionsEscOnTranscriptTearsDown asserts the esc-teardown path from
// phaseReplay (now only reached for CHILD sessions opened from the Children tab):
// esc stops the replay (spy replayStop called), clears replayCh/replayStop,
// bumps replayGen (invalidating any stale reader), returns to phaseIdle, dismisses
// the overlay (sessions.view == sessionsNone), and runs resetSession so the
// adopted session's transcript (m.conv) is cleared. Read-only inspection ends
// honestly — no live session survives the teardown. Uses a CHILD session (which
// still opens the replay stream); a top-level session goes straight to phaseIdle.
func TestSessionsEscOnTranscriptTearsDown(t *testing.T) {
	fl := &fakeSessionLister{sessions: []client.SessionListItem{
		{ID: "subagent-call1", ModifiedAt: nowMinusMinutes(4), State: "completed", Turns: 2, ModelID: "gpt-5"},
	}}
	fr := &fakeSessionReplayer{stream: client.NewFakeEventStream()}
	conv := newSessionsConv()
	m := newSessionsModel(t, conv, fl, fr)

	// Drive the real handoff so the replay state is set up by production code
	// (switchToSession), not hand-rolled — then esc from the transcript view.
	// A child session opens the replay stream and enters phaseReplay.
	child := fl.sessions[0]
	mm, _, _ := m.switchToSession(child)
	m = mm.(Model)
	if m.phase != phaseReplay {
		t.Fatalf("setup: phase = %v, want phaseReplay (child session)", m.phase)
	}
	if m.sessions.replayCh == nil || m.sessions.replayStop == nil {
		t.Fatal("setup: replay stream should be open after switchToSession (child)")
	}
	genBefore := m.sessions.replayGen
	// Plant a non-empty conversation so resetSession's clearing is observable, and a
	// spy replayStop so the teardown's stop call is observable.
	stopped := false
	m.sessions.replayStop = func() { stopped = true }
	m.conv.addUser("staged transcript content that teardown must drop")

	// Esc drives the phaseReplay arm via the key dispatcher (the production path).
	mm, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	m = mm.(Model)

	if !stopped {
		t.Error("esc should call replayStop")
	}
	if m.sessions.replayCh != nil {
		t.Errorf("replayCh should be nil after teardown, got %v", m.sessions.replayCh)
	}
	if m.sessions.replayStop != nil {
		t.Error("replayStop should be nil after teardown")
	}
	if m.sessions.replayGen <= genBefore {
		t.Errorf("replayGen should bump after teardown: before=%d after=%d", genBefore, m.sessions.replayGen)
	}
	if m.phase != phaseIdle {
		t.Fatalf("phase = %v, want phaseIdle", m.phase)
	}
	if m.sessions.view != sessionsNone {
		t.Fatalf("sessions.view = %v, want sessionsNone", m.sessions.view)
	}
	if !m.conv.isEmpty() {
		t.Errorf("conv should be cleared by resetSession, got %d blocks", len(m.conv.blocks))
	}
	if m.sessionID != "" {
		t.Errorf("sessionID should be cleared after teardown, got %q", m.sessionID)
	}
}

// replayScriptMsgs is a scripted replay sequence (user_prompt + turn.start +
// assistant delta + tool.call + tool.result + approval + result) the projection-
// equivalence test drives through updateReplayMsg. It is the SAME shape of
// sequence askFrameMsgs drives through the LIVE updateStreamEvent path, so the
// two paths' projections can be compared.
func replayScriptMsgs() []tea.Msg {
	return []tea.Msg{
		client.UserPromptMsg{Text: "read the greeting file"},
		client.TurnStartMsg{Turn: 1},
		client.AssistantDeltaMsg{Turn: 1, Text: "Reading the greeting file."},
		client.ToolCallMsg{ID: "call-read-1", Name: "Read", Args: `{"path":"greeting.txt"}`},
		client.ToolResultMsg{CallID: "call-read-1", Content: "hello from the mecatl demo workspace"},
		client.TurnEndMsg{Turn: 1, Usage: client.Usage{InputTokens: 1200, OutputTokens: 340}, DurationMs: 4100},
		client.ApprovalMsg{AskID: "ask-1", Verdict: "allow_once", Tool: "Read", CallID: "call-read-1"},
		client.ResultMsg{Stop: "end_turn"},
	}
}

// driveReplay feeds a scripted replay sequence through updateReplayMsg, gen-
// matching each msg, returning the resulting model. Mirrors how the live path's
// applyAll feeds askFrameMsgs through Update.
func driveReplay(t *testing.T, m Model, msgs []tea.Msg) Model {
	t.Helper()
	for _, msg := range msgs {
		mm, _ := m.updateReplayMsg(replayMsg{gen: m.sessions.replayGen, msg: msg})
		m = mm.(Model)
	}
	return m
}

// setupReplayTranscript sets up the phaseReplay transcript state WITHOUT opening
// a real replay stream (so a test can drive updateReplayMsg deterministically
// with scripted msgs, without a live reader goroutine racing the gen guard). It
// mirrors switchToSession's state setup minus the ReplayStreamCmd open: adopt the
// session id, enter phaseReplay, and zero the transcript. The block caches are
// reset (as switchToSession does via resetSession) so the transcript's blocks
// never alias a prior conversation's cache entries.
func setupReplayTranscript(m Model, s client.SessionListItem) Model {
	m = m.resetSession()
	m.sessionID = s.ID
	m.sessions.replayGen = 1
	m.sessions.transcript = conversation{}
	m.sessions.view = sessionsTranscript
	m.sessions.confirm = s
	m.phase = phaseReplay
	m.restartedThisRun = true
	return m
}

// TestReplayProjectionEquivalence drives a scripted replay (user_prompt + turn +
// assistant text + tool call/result + turn-end + approval verdict + result)
// through updateReplayMsg and asserts m.sessions.transcript has the right blocks
// (a user block, an assistant block carrying the text, a resolved tool block, a
// turn-stat, an approval verdict notice, and NO error block). This is the
// projection-equivalence proof: the replay path produces the SAME block shape the
// live updateStreamEvent path would for the same event sequence.
func TestReplayProjectionEquivalence(t *testing.T) {
	fl := &fakeSessionLister{sessions: sampleSessions()}
	conv := newSessionsConv()
	m := newSessionsModel(t, conv, fl, &fakeSessionReplayer{})
	m = setupReplayTranscript(m, sampleSessions()[0])
	m = driveReplay(t, m, replayScriptMsgs())

	blocks := m.sessions.transcript.blocks
	// Expected block kinds in order: user, assistant, tool, turnStat, notice(approval).
	// (ResultMsg{Stop:"end_turn"} is a non-error terminal → no block.)
	if len(blocks) < 5 {
		t.Fatalf("transcript blocks = %d, want ≥5:\n%+v", len(blocks), blocks)
	}
	if blocks[0].kind != blockUser {
		t.Errorf("block 0 kind = %v, want blockUser", blocks[0].kind)
	}
	if blocks[0].raw != "read the greeting file" {
		t.Errorf("block 0 raw = %q, want the user prompt text", blocks[0].raw)
	}
	if blocks[1].kind != blockAssistant {
		t.Errorf("block 1 kind = %v, want blockAssistant", blocks[1].kind)
	}
	if !strings.Contains(blocks[1].raw, "Reading the greeting file.") {
		t.Errorf("block 1 raw = %q, want the assistant text", blocks[1].raw)
	}
	if blocks[2].kind != blockTool {
		t.Errorf("block 2 kind = %v, want blockTool", blocks[2].kind)
	}
	if blocks[2].toolName != "Read" || !blocks[2].resolved {
		t.Errorf("block 2 = %+v, want a resolved Read tool block", blocks[2])
	}
	if !strings.Contains(blocks[2].resultBody, "hello from the mecatl demo workspace") {
		t.Errorf("block 2 resultBody = %q, want the tool result content", blocks[2].resultBody)
	}
	if blocks[3].kind != blockTurnStat {
		t.Errorf("block 3 kind = %v, want blockTurnStat", blocks[3].kind)
	}
	// The approval verdict is a notice. Find the notice block carrying the verdict.
	found := false
	for _, b := range blocks {
		if b.kind == blockNotice && strings.Contains(b.raw, "allowed once") && strings.Contains(b.raw, "Read") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("transcript missing the approval verdict notice, blocks:\n%+v", blocks)
	}
	// No error block: ResultMsg{Stop:"end_turn"} is a clean terminal.
	for i, b := range blocks {
		if b.kind == blockError {
			t.Errorf("block %d is blockError, want none for a clean terminal result: %+v", i, b)
		}
	}
	// The live m.conv must be UNTOUCHED (the replay projects into sessions.transcript,
	// never the live conversation).
	if !m.conv.isEmpty() {
		t.Errorf("live conv should be empty during replay, got %d blocks", len(m.conv.blocks))
	}
}

// TestReplayCompactionArchiveNotice asserts a CompactionArchiveMsg renders as a
// bounded "history compacted — N turns archived" notice rather than the verbatim
// (huge) archived message slice.
func TestReplayCompactionArchiveNotice(t *testing.T) {
	fl := &fakeSessionLister{sessions: sampleSessions()}
	conv := newSessionsConv()
	m := newSessionsModel(t, conv, fl, &fakeSessionReplayer{})
	m = setupReplayTranscript(m, sampleSessions()[0])
	archive := client.CompactionArchiveMsg{Replaced: []client.ConversationMessage{
		{Role: "user", Text: "old prompt 1"},
		{Role: "assistant", Text: "old answer 1"},
		{Role: "user", Text: "old prompt 2"},
	}}
	m = driveReplay(t, m, []tea.Msg{archive})
	blocks := m.sessions.transcript.blocks
	if len(blocks) != 1 || blocks[0].kind != blockNotice {
		t.Fatalf("transcript blocks = %+v, want one blockNotice", blocks)
	}
	if !strings.Contains(blocks[0].raw, "history compacted") {
		t.Errorf("notice = %q, want 'history compacted'", blocks[0].raw)
	}
	if !strings.Contains(blocks[0].raw, "3 turns archived") {
		t.Errorf("notice = %q, want '3 turns archived'", blocks[0].raw)
	}
}

// TestReplayApprovalVerdictNotices asserts the three verdict kinds render their
// one-line notice correctly.
func TestReplayApprovalVerdictNotices(t *testing.T) {
	cases := []struct {
		verdict string
		want    string
	}{
		{"allow_once", "✓ allowed once: Bash"},
		{"allow_always", "✓ allowed always: Bash"},
		{"deny", "✗ denied: Bash"},
	}
	for _, c := range cases {
		fl := &fakeSessionLister{sessions: sampleSessions()}
		conv := newSessionsConv()
		m := newSessionsModel(t, conv, fl, &fakeSessionReplayer{})
		m = setupReplayTranscript(m, sampleSessions()[0])
		m = driveReplay(t, m, []tea.Msg{client.ApprovalMsg{Verdict: c.verdict, Tool: "Bash"}})
		blocks := m.sessions.transcript.blocks
		if len(blocks) != 1 || blocks[0].kind != blockNotice {
			t.Fatalf("verdict %q: blocks = %+v, want one blockNotice", c.verdict, blocks)
		}
		if blocks[0].raw != c.want {
			t.Errorf("verdict %q: notice = %q, want %q", c.verdict, blocks[0].raw, c.want)
		}
	}
}

// TestSwitchToSessionBlocksRunning asserts the state gate blocks opening a
// session whose State is "running": Enter on the picker row does NOT open the
// replay stream, sets a statusMsg naming the state and "cannot open", and
// leaves the picker open so the user can pick another.
func TestSwitchToSessionBlocksRunning(t *testing.T) {
	fl := &fakeSessionLister{sessions: []client.SessionListItem{
		{ID: "sess-run", ModifiedAt: nowMinusMinutes(1), State: "running", Turns: 2, ModelID: "gpt-5"},
	}}
	fr := &fakeSessionReplayer{stream: client.NewFakeEventStream()}
	conv := newSessionsConv()
	m := newSessionsModel(t, conv, fl, fr)

	mm, _ := m.openSessions()
	m = mm.(Model)
	m = applyAll(m, client.SessionsListedMsg{Sessions: fl.sessions})
	m.sessions.cursor = 0
	// Enter → blocked by the state gate (picker stays open).
	mm, _, _ = m.onSessionsKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = mm.(Model)
	if m.phase != phaseIdle {
		t.Fatalf("phase = %v, want phaseIdle (did not switch)", m.phase)
	}
	if m.sessions.view != sessionsPanel {
		t.Fatalf("view = %v, want sessionsPanel (picker still up)", m.sessions.view)
	}
	if fr.calls != 0 {
		t.Errorf("replayer should NOT be called, got %d calls", fr.calls)
	}
	got := stripANSIstr(m.statusMsg)
	if !strings.Contains(got, "running") || !strings.Contains(got, "cannot open") {
		t.Errorf("statusMsg = %q, want it to mention 'running' and 'cannot open'", got)
	}
}

// TestSwitchToSessionBlocksAwaiting asserts the state gate also blocks an
// "awaiting" session (parked on a permission ask — a partial transcript).
func TestSwitchToSessionBlocksAwaiting(t *testing.T) {
	fl := &fakeSessionLister{sessions: []client.SessionListItem{
		{ID: "sess-wait", ModifiedAt: nowMinusMinutes(1), State: "awaiting", Turns: 1, ModelID: "gpt-5"},
	}}
	fr := &fakeSessionReplayer{stream: client.NewFakeEventStream()}
	conv := newSessionsConv()
	m := newSessionsModel(t, conv, fl, fr)

	mm, _ := m.openSessions()
	m = mm.(Model)
	m = applyAll(m, client.SessionsListedMsg{Sessions: fl.sessions})
	m.sessions.cursor = 0
	mm, _, _ = m.onSessionsKey(tea.KeyPressMsg{Code: tea.KeyEnter}) // → open (blocked)
	m = mm.(Model)
	if m.phase != phaseIdle {
		t.Fatalf("phase = %v, want phaseIdle (did not switch)", m.phase)
	}
	if m.sessions.view != sessionsPanel {
		t.Fatalf("view = %v, want sessionsPanel (picker still up)", m.sessions.view)
	}
	if fr.calls != 0 {
		t.Errorf("replayer should NOT be called, got %d calls", fr.calls)
	}
	got := stripANSIstr(m.statusMsg)
	if !strings.Contains(got, "awaiting") || !strings.Contains(got, "cannot open") {
		t.Errorf("statusMsg = %q, want it to mention 'awaiting' and 'cannot open'", got)
	}
}

// TestContinueByDefaultTopLevel asserts the continue-by-default handoff for a
// top-level session: switchToSession opens the replay stream (loading the prior
// conversation), binds the session id, sets continueOnLoad, arms auto-follow,
// and enters phaseReplay (loading) with a "continuing" status. The terminal
// handoff (on stream close) to phaseIdle is covered by
// TestSelectSessionDirectOpenTopLevel*; this test pins the LOADING state.
func TestContinueByDefaultTopLevel(t *testing.T) {
	fl := &fakeSessionLister{sessions: sampleSessions()}
	fr := &fakeSessionReplayer{stream: client.NewFakeEventStream()}
	conv := newSessionsConv()
	m := newSessionsModel(t, conv, fl, fr)
	top := sampleSessions()[0] // sess-aaa, no child prefix

	mm, _, _ := m.switchToSession(top)
	m = mm.(Model)

	// Loading state: phaseReplay with continueOnLoad set.
	if m.phase != phaseReplay {
		t.Fatalf("phase = %v, want phaseReplay (loading)", m.phase)
	}
	if m.sessionID != top.ID {
		t.Errorf("sessionID = %q, want %q (kept)", m.sessionID, top.ID)
	}
	if !m.sessions.continueOnLoad {
		t.Error("continueOnLoad should be true (top-level continue on stream close)")
	}
	if m.sessions.replayCh == nil {
		t.Error("replayCh should be set (replay stream opened to load history)")
	}
	if m.sessions.replayStop == nil {
		t.Error("replayStop should be set (replay stream opened)")
	}
	if m.sessions.view != sessionsTranscript {
		t.Fatalf("sessions.view = %v, want sessionsTranscript (loading view)", m.sessions.view)
	}
	if !m.stuck {
		t.Error("stuck should be true (auto-follow armed for the live tail)")
	}
	if m.restartedThisRun != true {
		t.Error("restartedThisRun should be true (suppresses the welcome splash)")
	}
	if fr.calls != 1 || fr.lastID != top.ID {
		t.Fatalf("replayer calls=%d lastID=%q, want 1/%q", fr.calls, fr.lastID, top.ID)
	}
	got := stripANSIstr(m.statusMsg)
	if !strings.Contains(got, "continuing") || !strings.Contains(got, top.ID) {
		t.Errorf("statusMsg = %q, want 'continuing' and %q", got, top.ID)
	}
}

// TestContinueByDefaultChildStaysReadOnly asserts a CHILD session (opened from
// the Children tab) does NOT continue by default: it opens the replay stream and
// enters phaseReplay (read-only transcript inspection), since continuing a child
// as a top-level live session is incoherent (no parent context).
func TestContinueByDefaultChildStaysReadOnly(t *testing.T) {
	child := client.SessionListItem{ID: "subagent-call1", ModifiedAt: nowMinusMinutes(4), State: "completed", Turns: 2, ModelID: "gpt-5"}
	fl := &fakeSessionLister{sessions: []client.SessionListItem{child}}
	fr := &fakeSessionReplayer{stream: client.NewFakeEventStream()}
	conv := newSessionsConv()
	m := newSessionsModel(t, conv, fl, fr)

	mm, _, _ := m.switchToSession(child)
	m = mm.(Model)

	if m.phase != phaseReplay {
		t.Fatalf("phase = %v, want phaseReplay (child stays read-only)", m.phase)
	}
	if m.sessionID != child.ID {
		t.Errorf("sessionID = %q, want %q", m.sessionID, child.ID)
	}
	if m.sessions.replayCh == nil {
		t.Error("replayCh should be set (child opens the replay stream)")
	}
	if fr.calls != 1 || fr.lastID != child.ID {
		t.Fatalf("replayer calls=%d lastID=%q, want 1/%q", fr.calls, fr.lastID, child.ID)
	}
	// The transcript hint must NOT advertise "c: continue" (no Continue action).
	content := m.View().Content
	if strings.Contains(stripANSIstr(content), "c: continue") {
		t.Errorf("child transcript hint should omit 'c: continue':\n%s", content)
	}
	if !strings.Contains(stripANSIstr(content), "esc: back") {
		t.Errorf("child transcript hint should show 'esc: back':\n%s", content)
	}
}

// mixedSessions is a fixed inventory mixing top-level and child-prefixed ids for
// the tab-split tests.
func mixedSessions() []client.SessionListItem {
	return []client.SessionListItem{
		{ID: "sess-aaa", ModifiedAt: nowMinusMinutes(5), State: "completed", Turns: 12, ModelID: "gpt-5"},
		{ID: "subagent-call1", ModifiedAt: nowMinusMinutes(4), State: "completed", Turns: 2, ModelID: "gpt-5"},
		{ID: "team-t1-alice", ModifiedAt: nowMinusMinutes(3), State: "completed", Turns: 4, ModelID: "claude-opus"},
		{ID: "sess-bbb", ModifiedAt: nowMinusMinutes(60), State: "running", Turns: 3, ModelID: "claude-opus"},
		{ID: "parallel-call2-0", ModifiedAt: nowMinusMinutes(2), State: "completed", Turns: 1, ModelID: "gpt-5"},
	}
}

// TestSessionsTabsSplitTopLevelAndChildren asserts the Sessions tab shows only
// top-level sessions (no child prefix) and the Children tab shows only child
// sessions (subagent-/team-/parallel-), and `tab` switches between them.
func TestSessionsTabsSplitTopLevelAndChildren(t *testing.T) {
	fl := &fakeSessionLister{sessions: mixedSessions()}
	conv := newSessionsConv()
	m := newSessionsModel(t, conv, fl, &fakeSessionReplayer{})
	mm, _ := m.openSessions()
	m = mm.(Model)
	m = applyAll(m, client.SessionsListedMsg{Sessions: fl.sessions})

	// Default tab is Sessions: only top-level (sess-aaa, sess-bbb).
	if m.sessions.tab != tabSessions {
		t.Fatalf("default tab = %v, want tabSessions", m.sessions.tab)
	}
	if len(m.sessions.filtered) != 2 {
		t.Fatalf("Sessions tab filtered = %d, want 2 (top-level only):\n%+v", len(m.sessions.filtered), m.sessions.filtered)
	}
	for _, s := range m.sessions.filtered {
		if isChildSessionID(s.ID) {
			t.Errorf("Sessions tab should not contain child session %q", s.ID)
		}
	}

	// Press `tab` → Children tab.
	mm, _, _ = m.onSessionsKey(tea.KeyPressMsg{Code: tea.KeyTab})
	m = mm.(Model)
	if m.sessions.tab != tabChildren {
		t.Fatalf("after tab: tab = %v, want tabChildren", m.sessions.tab)
	}
	if len(m.sessions.filtered) != 3 {
		t.Fatalf("Children tab filtered = %d, want 3 (subagent+team+parallel):\n%+v", len(m.sessions.filtered), m.sessions.filtered)
	}
	for _, s := range m.sessions.filtered {
		if !isChildSessionID(s.ID) {
			t.Errorf("Children tab should not contain top-level session %q", s.ID)
		}
	}

	// Press `tab` again → back to Sessions.
	mm, _, _ = m.onSessionsKey(tea.KeyPressMsg{Code: tea.KeyTab})
	m = mm.(Model)
	if m.sessions.tab != tabSessions {
		t.Fatalf("after second tab: tab = %v, want tabSessions", m.sessions.tab)
	}
}

// (TestContinueGatedOnChildren and TestContinueAllowedOnTopLevel were removed:
// the bare `c` key / continueSession have been removed — top-level sessions now
// continue by default, and children stay read-only. The behavior is covered by
// TestContinueByDefaultTopLevel and TestContinueByDefaultChildStaysReadOnly above.)

// TestReplayCoalescesNoGlamourPerEvent proves the replay path coalesces like the
// live delta path: N replay events project into the transcript with ZERO glamour
// renders (they only append + mark dirty via markDirtyReplay), and a single
// frame-cadence renderTickMsg flushes exactly one. This is the O(N) not O(N²)
// fix for the child-inspection replay (Change 2). Uses a CHILD session so the
// replay stream path is exercised.
func TestReplayCoalescesNoGlamourPerEvent(t *testing.T) {
	child := client.SessionListItem{ID: "subagent-call1", ModifiedAt: nowMinusMinutes(4), State: "completed", Turns: 2, ModelID: "gpt-5"}
	fl := &fakeSessionLister{sessions: []client.SessionListItem{child}}
	fr := &fakeSessionReplayer{stream: client.NewFakeEventStream()}
	conv := newSessionsConv()
	m := newSessionsModel(t, conv, fl, fr)
	mm, _, _ := m.switchToSession(child)
	m = mm.(Model)
	if m.phase != phaseReplay {
		t.Fatalf("setup: phase = %v, want phaseReplay (child)", m.phase)
	}
	before := m.rend.mdRenders

	// Drive a scripted replay (several events) WITHOUT flushing a tick between them.
	msgs := replayScriptMsgs()
	for _, msg := range msgs {
		mm, _ := m.updateReplayMsg(replayMsg{gen: m.sessions.replayGen, msg: msg})
		m = mm.(Model)
	}
	if m.rend.mdRenders != before {
		t.Fatalf("expected %d glamour renders across %d coalesced replay events, got %d", before, len(msgs), m.rend.mdRenders-before)
	}
	if !m.viewDirty {
		t.Fatal("expected viewDirty=true after replay events with no flush")
	}

	// A single renderTickMsg flushes exactly one glamour render.
	flushedBefore := m.rend.mdRenders
	m = applyAll(m, renderTickMsg{})
	if got := m.rend.mdRenders - flushedBefore; got != 1 {
		t.Fatalf("expected exactly 1 glamour render on the flush, got %d", got)
	}
}
