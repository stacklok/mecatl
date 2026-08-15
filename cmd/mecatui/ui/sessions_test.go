package ui

import (
	"context"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

type fakeSessionLister struct {
	sessions []client.SessionListItem
	err      error
	calls    int
}

func (f *fakeSessionLister) ListSessions(context.Context) ([]client.SessionListItem, error) {
	f.calls++
	return f.sessions, f.err
}

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

func newSessionsConv() *fakeConv {
	return &fakeConv{recv: &fakeRecver{gate: make(chan struct{})}, send: &fakeSender{}, caps: client.Capabilities{}}
}

func newSessionsModel(t *testing.T, conv *fakeConv, fl *fakeSessionLister, loader client.SessionTranscripter) Model {
	t.Helper()
	m := New(Deps{
		Session: conv, Conv: conv, Sessions: fl, Transcript: loader,
		Theme: theme.New("aztec", theme.AztecPalette()), Workspace: "/workspace",
		Mode: "default", Model: "mock-model", Ctx: context.Background(), NoAltScreen: true,
	})
	return applyAll(m,
		tea.WindowSizeMsg{Width: 100, Height: 40},
		client.SessionReadyMsg{SessionID: "sess-test-0001", Capabilities: client.Capabilities{}},
	)
}

func nowMinusMinutes(n int) int64 { return time.Now().Add(-time.Duration(n) * time.Minute).Unix() }

func sampleSessions() []client.SessionListItem {
	return []client.SessionListItem{
		{ID: "sess-aaa", Title: "Fix the flaky CI", Kind: client.SessionKindMain, ModifiedAt: nowMinusMinutes(5), State: "completed", Turns: 12, ModelID: "gpt-5", Capabilities: client.SessionInventoryCapabilities{PublicChat: true, Inspect: true}},
		{ID: "sched-1", Title: "Nightly checks", Kind: client.SessionKindScheduled, ModifiedAt: nowMinusMinutes(60), State: "completed", Turns: 3, ModelID: "claude-opus", Capabilities: client.SessionInventoryCapabilities{Inspect: true}},
	}
}

func newScenario4Model(t *testing.T, loader client.SessionTranscripter) Model {
	t.Helper()
	return newSessionsModel(t, newSessionsConv(), &fakeSessionLister{}, loader)
}

func testTheme() theme.Theme { return theme.New("aztec", theme.AztecPalette()) }

func TestStartupSessionsWaitsForModelsThenCreatesOnlyOnNew(t *testing.T) {
	conv := newSessionsConv()
	lister := &fakeSessionLister{}
	m := New(Deps{
		Session: conv, Conv: conv, Sessions: lister, Transcript: &fakeSessionTranscriptLoader{},
		Models: &fakeModels{}, BrowseSessions: true,
		Theme: testTheme(), Workspace: "/workspace", Mode: "plan", Ctx: context.Background(), NoAltScreen: true,
	})
	if m.sessions.view != sessionsPanel || m.sessionID != "" {
		t.Fatalf("startup = view %v session %q, want sessions panel with no session", m.sessions.view, m.sessionID)
	}
	if conv.createCount != 0 {
		t.Fatalf("startup created %d sessions, want zero", conv.createCount)
	}

	// n is inert until the saved-model reconcile has completed.
	mm, cmd, handled := m.onSessionsKey(tea.KeyPressMsg{Code: 'n', Text: "n"})
	m = mm.(Model)
	if !handled || cmd != nil || conv.createCount != 0 {
		t.Fatalf("pre-reconcile n: handled=%v cmd=%v creates=%d", handled, cmd != nil, conv.createCount)
	}

	mm, cmd, handled = m.updateModelsMsg(client.ModelsMsg{})
	m = mm.(Model)
	if !handled || cmd != nil {
		t.Fatalf("models reconcile should not create or replace the startup picker: handled=%v cmd=%v", handled, cmd != nil)
	}
	mm, cmd, handled = m.onSessionsKey(tea.KeyPressMsg{Code: 'n', Text: "n"})
	m = mm.(Model)
	if !handled || cmd == nil {
		t.Fatal("post-reconcile n did not start the existing create flow")
	}
	ready := cmd()
	if conv.createCount != 1 {
		t.Fatalf("new chat created %d sessions, want one", conv.createCount)
	}
	m = applyAll(m, ready)
	if m.sessionID == "" || m.browsingStartupSessions || m.sessions.view != sessionsNone || m.phase != phaseIdle {
		t.Fatalf("ready state: id=%q startup=%v view=%v phase=%v", m.sessionID, m.browsingStartupSessions, m.sessions.view, m.phase)
	}
}

func TestStartupSessionsContinueInspectBackAndCancel(t *testing.T) {
	conv := newSessionsConv()
	loader := &fakeSessionTranscriptLoader{transcript: client.SessionTranscript{Complete: true}}
	m := New(Deps{
		Session: conv, Conv: conv, Sessions: &fakeSessionLister{}, Transcript: loader,
		BrowseSessions: true, Theme: testTheme(), Workspace: "/workspace", Mode: "default", Ctx: context.Background(), NoAltScreen: true,
	})
	m.modelsReconciled = true // no model lister: startup is immediately safe to create

	inspect := client.SessionListItem{ID: "sched-1", Title: "scheduled", Kind: client.SessionKindScheduled, Capabilities: client.SessionInventoryCapabilities{Inspect: true}}
	chat := client.SessionListItem{ID: "chat-1", Title: "chat", Kind: client.SessionKindMain, Capabilities: client.SessionInventoryCapabilities{PublicChat: true, Inspect: true}}
	m = applyAll(m, client.SessionsListedMsg{Sessions: []client.SessionListItem{chat, inspect}})
	m.sessions.tab = tabScheduledRuns
	m = m.syncSessionsFilter()
	mm, cmd, _ := m.chooseSession()
	m = mm.(Model)
	loader.transcript.SessionID = inspect.ID
	m = applyAll(m, cmd())
	if m.phase != phaseReplay || !m.sessions.inspect {
		t.Fatalf("inspect state: phase=%v inspect=%v", m.phase, m.sessions.inspect)
	}
	mm, _ = m.onReplayKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	m = mm.(Model)
	if m.sessions.view != sessionsPanel || !m.browsingStartupSessions || m.sessionID != "" {
		t.Fatalf("inspect Back did not return to startup picker: view=%v startup=%v id=%q", m.sessions.view, m.browsingStartupSessions, m.sessionID)
	}

	m.sessions.tab = tabChats
	m = m.syncSessionsFilter()
	mm, cmd, _ = m.chooseSession()
	m = mm.(Model)
	loader.transcript.SessionID = chat.ID
	m = applyAll(m, cmd())
	if m.sessionID != chat.ID || m.browsingStartupSessions || conv.createCount != 0 {
		t.Fatalf("continue: id=%q startup=%v creates=%d", m.sessionID, m.browsingStartupSessions, conv.createCount)
	}

	cancel := New(Deps{BrowseSessions: true, Sessions: &fakeSessionLister{}, Theme: testTheme(), Ctx: context.Background(), NoAltScreen: true})
	mm, quit, handled := cancel.onSessionsKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	cancel = mm.(Model)
	if !handled || quit == nil {
		t.Fatalf("startup esc: handled=%v quit=%v", handled, quit != nil)
	}
	if _, ok := quit().(tea.QuitMsg); !ok {
		t.Fatalf("startup esc command = %T, want tea.QuitMsg", quit())
	}
	if cancel.ActiveSessionID() != "" {
		t.Fatalf("cancel active session = %q, want empty", cancel.ActiveSessionID())
	}
}

func TestSessionsTranscriptReducer(t *testing.T) {
	got := conversationFromTranscript([]client.ConversationMessage{
		{Role: "user", Text: "read it"},
		{Role: "assistant", Text: "reading", ToolCalls: []client.ConvToolCall{{ID: "c1", Name: "Read", Args: `{"path":"a"}`}}},
		{Role: "tool", ToolResult: &client.ConvToolResult{CallID: "c1", Content: "hello"}},
		{Role: "assistant", Text: "done"},
	})
	if got.isEmpty() || len(got.blocks) != 4 {
		t.Fatalf("transcript blocks = %d, want 4", len(got.blocks))
	}
}

func TestSessionsRetryAndBack(t *testing.T) {
	loader := &fakeSessionTranscriptLoader{err: context.DeadlineExceeded}
	m := newScenario4Model(t, loader)
	row := client.SessionListItem{ID: "target", Kind: client.SessionKindMain, Capabilities: client.SessionInventoryCapabilities{PublicChat: true}}
	m.sessions.filtered = []client.SessionListItem{row}
	mm, cmd, _ := m.chooseSession()
	m = applyAll(mm.(Model), cmd())
	loader.err = nil
	loader.transcript = client.SessionTranscript{SessionID: row.ID, Complete: true}
	mm, cmd = m.onReplayKey(tea.KeyPressMsg{Code: 'r', Text: "r"})
	if cmd == nil {
		t.Fatal("retry did not issue a transcript request")
	}
	m = applyAll(mm.(Model), cmd())
	if m.sessionID != row.ID || m.phase != phaseIdle {
		t.Fatalf("retry did not continue chat: id=%q phase=%v", m.sessionID, m.phase)
	}
}
