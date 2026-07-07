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
	m = applyAll(m,
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

// TestSelectSessionOpensReplay asserts the handoff: Enter→confirm→Enter opens the
// replay stream, drives phase to phaseReplay, adopts the chosen session id, and
// calls the replayer with the chosen id.
func TestSelectSessionOpensReplay(t *testing.T) {
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

	// Cursor on the first row; Enter → confirm; Enter → switch.
	m.sessions.cursor = 0
	m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyEnter}) // chooseSession → sessionsConfirm
	if m.sessions.view != sessionsConfirm {
		t.Fatalf("after enter: view = %v, want sessionsConfirm", m.sessions.view)
	}
	if m.sessions.confirm.ID != "sess-aaa" {
		t.Fatalf("confirm candidate = %+v, want sess-aaa", m.sessions.confirm)
	}

	mm, cmd, _ := m.onSessionsConfirmKey(tea.KeyPressMsg{Code: tea.KeyEnter}) // switch
	m = mm.(Model)
	if m.phase != phaseReplay {
		t.Fatalf("phase = %v, want phaseReplay", m.phase)
	}
	if m.sessionID != "sess-aaa" {
		t.Fatalf("sessionID = %q, want sess-aaa", m.sessionID)
	}
	if m.sessions.replayCh == nil {
		t.Fatal("replayCh should be set after switchToSession")
	}
	if fr.calls != 1 {
		t.Fatalf("StreamSessionEvents calls = %d, want 1", fr.calls)
	}
	if fr.lastID != "sess-aaa" {
		t.Fatalf("replayer called with id %q, want sess-aaa", fr.lastID)
	}
	if m.sessions.view != sessionsTranscript {
		t.Fatalf("view = %v, want sessionsTranscript", m.sessions.view)
	}
	// The handoff returned a batch (closeSessions cmd + waitReplayCmd + sp.Tick);
	// drain it so the first replay msg lands and the reader re-arms deterministically.
	_ = cmd
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
// route into the current view), and NOT re-armed.
func TestReplayGenGuardsStaleReader(t *testing.T) {
	fl := &fakeSessionLister{sessions: sampleSessions()}
	fr := &fakeSessionReplayer{stream: client.NewFakeEventStream()}
	conv := newSessionsConv()
	m := newSessionsModel(t, conv, fl, fr)
	mm, _, _ := m.switchToSession(sampleSessions()[0])
	m = mm.(Model)
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
// flips the loading card to "transcript loaded", stays in phaseReplay, and does
// not re-arm.
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
	if !strings.Contains(m.View().Content, "transcript loaded") {
		t.Errorf("transcript view should render 'transcript loaded':\n%s", m.View().Content)
	}
}

// TestSessionsEscOnTranscriptTearsDown asserts the esc-teardown path from
// phaseReplay (update.go's phaseReplay arm → onReplayKey → closeSessionsTranscript):
// esc stops the replay (spy replayStop called), clears replayCh/replayStop,
// bumps replayGen (invalidating any stale reader), returns to phaseIdle, dismisses
// the overlay (sessions.view == sessionsNone), and runs resetSession so the
// adopted session's transcript (m.conv) is cleared. Read-only inspection ends
// honestly — no live session survives the teardown.
func TestSessionsEscOnTranscriptTearsDown(t *testing.T) {
	fl := &fakeSessionLister{sessions: sampleSessions()}
	fr := &fakeSessionReplayer{stream: client.NewFakeEventStream()}
	conv := newSessionsConv()
	m := newSessionsModel(t, conv, fl, fr)

	// Drive the real handoff so the replay state is set up by production code
	// (switchToSession), not hand-rolled — then esc from the transcript view.
	mm, _, _ := m.switchToSession(sampleSessions()[0])
	m = mm.(Model)
	if m.phase != phaseReplay {
		t.Fatalf("setup: phase = %v, want phaseReplay", m.phase)
	}
	if m.sessions.replayCh == nil || m.sessions.replayStop == nil {
		t.Fatal("setup: replay stream should be open after switchToSession")
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
}
