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
