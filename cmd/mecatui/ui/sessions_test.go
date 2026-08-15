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
		{ID: "sess-aaa", Title: "Fix the flaky CI", Kind: client.SessionKindMain, ModifiedAt: nowMinusMinutes(5), State: "completed", Turns: 12, ModelID: "gpt-5", Capabilities: client.SessionInventoryCapabilities{PublicChat: true, Inspect: true, CopyID: true, ViewTranscript: true, Fork: true, Rename: true, Delete: true}},
		{ID: "sched-1", Title: "Nightly checks", Kind: client.SessionKindScheduled, ModifiedAt: nowMinusMinutes(60), State: "completed", Turns: 3, ModelID: "claude-opus", Capabilities: client.SessionInventoryCapabilities{Inspect: true, CopyID: true, ViewTranscript: true}},
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

type fakeSessionManager struct {
	renameSnapshot client.SessionSnapshot
	renameErr      error
	deleteErr      error
	renameID       string
	renameTitle    string
	deletedID      string
}

func (f *fakeSessionManager) RenameSession(_ context.Context, id, title string) (client.SessionSnapshot, error) {
	f.renameID, f.renameTitle = id, title
	return f.renameSnapshot, f.renameErr
}

func (f *fakeSessionManager) DeleteSession(_ context.Context, id string) error {
	f.deletedID = id
	return f.deleteErr
}

func TestSessionsCopyViewAndCapabilityHints(t *testing.T) {
	clip := &fakeClipboard{}
	loader := &fakeSessionTranscriptLoader{transcript: client.SessionTranscript{SessionID: "opaque\nID", Complete: true}}
	m := newSessionsModel(t, newSessionsConv(), &fakeSessionLister{}, loader)
	m.deps.Clipboard = clip
	row := client.SessionListItem{ID: "opaque\nID", Kind: client.SessionKindMain, Capabilities: client.SessionInventoryCapabilities{CopyID: true, ViewTranscript: true, Fork: true, Rename: true, Delete: true}}
	m.sessions = newSessionsPanelState()
	m.sessions.loading = false
	m.sessions.sessions = []client.SessionListItem{row}
	m = m.syncSessionsFilter()

	mm, cmd, handled := m.onSessionsKey(tea.KeyPressMsg{Code: 'y', Text: "y"})
	m = mm.(Model)
	if !handled || cmd == nil {
		t.Fatal("copy action was not handled")
	}
	m = applyAll(m, cmd())
	if len(clip.wrote) != 1 || string(clip.wrote[0]) != row.ID || !strings.Contains(stripANSIstr(m.statusMsg), `"opaque\nID"`) {
		t.Fatalf("copy payload/status = %q / %q", clip.wrote, stripANSIstr(m.statusMsg))
	}

	clip.writeErr = errors.New("clipboard unavailable")
	mm, cmd, handled = m.onSessionsKey(tea.KeyPressMsg{Code: 'y', Text: "y"})
	m = applyAll(mm.(Model), cmd())
	if !handled || !strings.Contains(stripANSIstr(m.statusMsg), "clipboard unavailable") {
		t.Fatalf("copy failure was reported as success: %q", stripANSIstr(m.statusMsg))
	}
	clip.writeErr = nil

	mm, cmd, handled = m.onSessionsKey(tea.KeyPressMsg{Code: 'v', Text: "v"})
	m = mm.(Model)
	if !handled || cmd == nil || !m.sessions.inspect || m.sessionID != "sess-test-0001" {
		t.Fatalf("view did not preserve active chat: handled=%v inspect=%v active=%q", handled, m.sessions.inspect, m.sessionID)
	}
}

func TestSessionsUnavailableActionsUsePerActionReasons(t *testing.T) {
	tests := []struct {
		name   string
		key    tea.KeyPressMsg
		reason client.CapabilityReason
		want   string
	}{
		{"continue", tea.KeyPressMsg{Code: tea.KeyEnter}, client.CapabilityReasonInspectOnlyKind, "inspection only"},
		{"copy", tea.KeyPressMsg{Code: 'y', Text: "y"}, client.CapabilityReasonActiveElsewhere, "active elsewhere"},
		{"view", tea.KeyPressMsg{Code: 'v', Text: "v"}, client.CapabilityReasonTranscriptUnavailable, "transcript is unavailable"},
		{"fork", tea.KeyPressMsg{Code: 'f', Text: "f"}, client.CapabilityReasonAwaitingApproval, "awaiting approval"},
		{"rename", tea.KeyPressMsg{Code: 'r', Text: "r"}, client.CapabilityReasonActiveElsewhere, "active elsewhere"},
		{"delete storage", tea.KeyPressMsg{Code: 'd', Text: "d"}, client.CapabilityReasonStorageUnsupported, "unsupported by session storage"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := newSessionsModel(t, newSessionsConv(), &fakeSessionLister{}, &fakeSessionTranscriptLoader{})
			m.deps.SessionManagement = &fakeSessionManager{}
			row := client.SessionListItem{ID: "target", Kind: client.SessionKindMain}
			switch tc.name {
			case "continue":
				row.Reasons.PublicChat = tc.reason
			case "copy":
				row.Reasons.CopyID = tc.reason
			case "view":
				row.Reasons.ViewTranscript = tc.reason
			case "fork":
				row.Reasons.Fork = tc.reason
			case "rename":
				row.Reasons.Rename = tc.reason
			case "delete storage":
				row.Reasons.Delete = tc.reason
			}
			m.sessions = newSessionsPanelState()
			m.sessions.loading = false
			m.sessions.sessions = []client.SessionListItem{row}
			m = m.syncSessionsFilter()
			mm, _, handled := m.onSessionsKey(tc.key)
			m = mm.(Model)
			if !handled || !strings.Contains(stripANSIstr(m.statusMsg), tc.want) {
				t.Fatalf("handled=%v status=%q, want %q", handled, stripANSIstr(m.statusMsg), tc.want)
			}
		})
	}
}

func TestSessionsRenameCancelSuccessAndServerError(t *testing.T) {
	mgr := &fakeSessionManager{renameSnapshot: client.SessionSnapshot{Title: "server title", TitleProvenance: "operator"}}
	m := newSessionsModel(t, newSessionsConv(), &fakeSessionLister{}, &fakeSessionTranscriptLoader{})
	m.deps.SessionManagement = mgr
	row := client.SessionListItem{ID: m.sessionID, Title: "old", Kind: client.SessionKindMain, Capabilities: client.SessionInventoryCapabilities{Rename: true}}
	m.sessions = newSessionsPanelState()
	m.sessions.loading = false
	m.sessions.sessions = []client.SessionListItem{row}
	m = m.syncSessionsFilter()

	mm, _, _ := m.onSessionsKey(tea.KeyPressMsg{Code: 'r', Text: "r"})
	m = mm.(Model)
	if !m.sessions.renaming || m.sessions.renameInput.Value() != "old" {
		t.Fatal("rename form did not open prefilled")
	}
	mm, _, _ = m.onSessionsKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	m = mm.(Model)
	if m.sessions.renaming || mgr.renameID != "" {
		t.Fatal("escape did not cancel rename")
	}

	mm, _, _ = m.onSessionsKey(tea.KeyPressMsg{Code: 'r', Text: "r"})
	m = mm.(Model)
	m.sessions.renameInput.SetValue("requested")
	mm, cmd, _ := m.onSessionsKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = mm.(Model)
	m = applyAll(m, cmd())
	if got := m.sessions.sessions[0]; got.Title != "server title" || got.TitleProvenance != "operator" || m.sessionTitle != "server title" {
		t.Fatalf("rename result not adopted: row=%+v active=%q", got, m.sessionTitle)
	}

	mgr.renameErr = errors.New("ownership changed")
	mm, _, _ = m.onSessionsKey(tea.KeyPressMsg{Code: 'r', Text: "r"})
	m = mm.(Model)
	mm, cmd, _ = m.onSessionsKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = applyAll(mm.(Model), cmd())
	if !strings.Contains(stripANSIstr(m.statusMsg), "ownership changed") {
		t.Fatalf("rename error not shown: %q", stripANSIstr(m.statusMsg))
	}
}

func TestSessionsRenameRefreshAdoptsAuthoritativeOrderAndModifiedAt(t *testing.T) {
	lister := &fakeSessionLister{}
	mgr := &fakeSessionManager{renameSnapshot: client.SessionSnapshot{Title: "match renamed", TitleProvenance: "operator"}}
	m := newSessionsModel(t, newSessionsConv(), lister, &fakeSessionTranscriptLoader{})
	m.deps.SessionManagement = mgr
	m.sessions = newSessionsPanelState()
	m.sessions.loading = false
	m.sessions.sessions = []client.SessionListItem{
		{ID: "first", Title: "match first", ModifiedAt: 200, Kind: client.SessionKindMain, Capabilities: client.SessionInventoryCapabilities{Rename: true}},
		{ID: "renamed", Title: "match old", ModifiedAt: 100, Kind: client.SessionKindMain, Capabilities: client.SessionInventoryCapabilities{Rename: true}},
	}
	m.sessions.filter.SetValue("match")
	m = m.syncSessionsFilter()
	m.sessions.cursor = 1

	mm, _, _ := m.onSessionsKey(tea.KeyPressMsg{Code: 'r', Text: "r"})
	m = mm.(Model)
	m.sessions.renameInput.SetValue("requested")
	mm, renameCmd, _ := m.onSessionsKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = mm.(Model)
	renameMsg := renameCmd()
	mm, refreshCmd := m.Update(renameMsg)
	m = mm.(Model)
	if refreshCmd == nil {
		t.Fatal("successful rename did not request an authoritative inventory refresh")
	}

	lister.sessions = []client.SessionListItem{
		{ID: "renamed", Title: "match authoritative", ModifiedAt: 300, Kind: client.SessionKindMain, Capabilities: client.SessionInventoryCapabilities{Rename: true}},
		{ID: "first", Title: "match first", ModifiedAt: 200, Kind: client.SessionKindMain, Capabilities: client.SessionInventoryCapabilities{Rename: true}},
	}
	mm, _ = m.Update(refreshCmd())
	m = mm.(Model)
	if m.sessions.filter.Value() != "match" {
		t.Fatalf("filter = %q, want retained", m.sessions.filter.Value())
	}
	if len(m.sessions.sessions) != 2 || m.sessions.sessions[0].ID != "renamed" || m.sessions.sessions[0].ModifiedAt != 300 {
		t.Fatalf("authoritative rows not adopted: %+v", m.sessions.sessions)
	}
	if m.sessions.cursor != 0 || m.sessions.filtered[m.sessions.cursor].ID != "renamed" {
		t.Fatalf("selection not retained by ID: cursor=%d filtered=%+v", m.sessions.cursor, m.sessions.filtered)
	}
}

func TestSessionsDeleteConfirmationCurrentRefusalAndSelection(t *testing.T) {
	mgr := &fakeSessionManager{}
	m := newSessionsModel(t, newSessionsConv(), &fakeSessionLister{}, &fakeSessionTranscriptLoader{})
	m.deps.SessionManagement = mgr
	rows := []client.SessionListItem{
		{ID: m.sessionID, Title: "current", Kind: client.SessionKindMain, Capabilities: client.SessionInventoryCapabilities{Delete: true}},
		{ID: "other", Title: "other", Kind: client.SessionKindMain, Capabilities: client.SessionInventoryCapabilities{Delete: true}},
		{ID: "last", Title: "last", Kind: client.SessionKindMain, Capabilities: client.SessionInventoryCapabilities{Delete: true}},
	}
	m.sessions = newSessionsPanelState()
	m.sessions.loading = false
	m.sessions.sessions = rows
	m = m.syncSessionsFilter()
	mm, cmd, _ := m.onSessionsKey(tea.KeyPressMsg{Code: 'd', Text: "d"})
	m = mm.(Model)
	if cmd != nil || m.sessions.confirmDelete || !strings.Contains(stripANSIstr(m.statusMsg), "switch") {
		t.Fatalf("current delete not refused actionably: %q", stripANSIstr(m.statusMsg))
	}

	m.sessions.cursor = 1
	mm, _, _ = m.onSessionsKey(tea.KeyPressMsg{Code: 'd', Text: "d"})
	m = mm.(Model)
	if !m.sessions.confirmDelete {
		t.Fatal("delete confirmation did not open")
	}
	mm, _, _ = m.onSessionsKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	m = mm.(Model)
	if m.sessions.confirmDelete || mgr.deletedID != "" {
		t.Fatal("escape did not cancel delete")
	}
	mm, _, _ = m.onSessionsKey(tea.KeyPressMsg{Code: 'd', Text: "d"})
	m = mm.(Model)
	mm, cmd, _ = m.onSessionsKey(tea.KeyPressMsg{Code: 'y', Text: "y"})
	m = applyAll(mm.(Model), cmd())
	if mgr.deletedID != "other" || len(m.sessions.filtered) != 2 || m.sessions.filtered[m.sessions.cursor].ID != "last" {
		t.Fatalf("delete result incoherent: deleted=%q cursor=%d rows=%+v", mgr.deletedID, m.sessions.cursor, m.sessions.filtered)
	}
}

func TestSessionsForkAdoptsOnlyAfterCompleteSuccess(t *testing.T) {
	conv := newSessionsConv()
	conv.forkedID = "forked"
	conv.getSessionTitle = "fork title"
	loader := &fakeSessionTranscriptLoader{transcript: client.SessionTranscript{SessionID: "forked", Complete: true, Messages: []client.ConversationMessage{{Role: "assistant", Text: "fork transcript"}}}}
	m := newSessionsModel(t, conv, &fakeSessionLister{}, loader)
	m.sessions = newSessionsPanelState()
	m.sessions.loading = false
	m.sessions.sessions = []client.SessionListItem{{ID: "source", Kind: client.SessionKindMain, Capabilities: client.SessionInventoryCapabilities{Fork: true}}}
	m = m.syncSessionsFilter()
	mm, cmd, _ := m.onSessionsKey(tea.KeyPressMsg{Code: 'f', Text: "f"})
	m = applyAll(mm.(Model), cmd())
	if m.sessionID != "forked" || m.sessionTitle != "fork title" || m.conv.isEmpty() {
		t.Fatalf("fork not adopted: id=%q title=%q empty=%v", m.sessionID, m.sessionTitle, m.conv.isEmpty())
	}

	conv.forkErr = errors.New("race")
	m.sessions = newSessionsPanelState()
	m.sessions.loading = false
	m.sessions.sessions = []client.SessionListItem{{ID: "source2", Kind: client.SessionKindMain, Capabilities: client.SessionInventoryCapabilities{Fork: true}}}
	m = m.syncSessionsFilter()
	oldID := m.sessionID
	mm, cmd, _ = m.onSessionsKey(tea.KeyPressMsg{Code: 'f', Text: "f"})
	m = applyAll(mm.(Model), cmd())
	if m.sessionID != oldID || !strings.Contains(stripANSIstr(m.statusMsg), "race") {
		t.Fatalf("failed fork partially rebound: id=%q status=%q", m.sessionID, stripANSIstr(m.statusMsg))
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
