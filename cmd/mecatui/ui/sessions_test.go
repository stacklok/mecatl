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
