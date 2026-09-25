package ui

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
)

func startupSelection(id, state string) *client.ResumeSelection {
	return &client.ResumeSelection{
		Row: client.SessionListItem{
			ID: id, Title: "Prior chat", State: state, Placement: client.Placement{Kind: "local", Label: "prior"}, CreatedAt: 10, ModifiedAt: 20,
			Kind: client.SessionKindMain, Capabilities: client.SessionInventoryCapabilities{PublicChat: true, Inspect: true},
		},
		Transcript: client.SessionTranscript{
			SessionID: id, Complete: true,
			Messages: []client.ConversationMessage{{Role: "user", Text: "original question"}, {Role: "assistant", Text: "original answer"}},
		},
		Snapshot: client.SessionSnapshot{
			Title: "Prior chat", State: state, Placement: client.Placement{Kind: "local", Label: "prior"}, CreatedAt: 10,
		},
	}
}

func startupResumeUI(t *testing.T, conv *fakeConv, seed string, state string) Model {
	t.Helper()
	m := newTestModelFromDeps(Deps{
		Session: conv, Conv: conv, Theme: testTheme(), Ctx: t.Context(), Workspace: "/launch",
		Resume: startupSelection("existing", state), InitialPrompt: seed,
	})
	m.width, m.height = 80, 24
	return m
}

func runStartupCommands(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	if batch, ok := cmd().(tea.BatchMsg); ok {
		for _, child := range batch {
			runStartupCommands(child)
		}
	}
}

func TestSessionContinuityUX_Scenario6_NoThrowawaySession(t *testing.T) {
	conv := &fakeConv{}
	m := startupResumeUI(t, conv, "", "completed")
	m = applyAll(m, startupResumeReadyMsg{})
	if conv.createCount != 0 {
		t.Fatalf("startup adoption called CreateSession %d times", conv.createCount)
	}
	if m.sessionID != "existing" || m.sessionTitle != "Prior chat" || m.activePlacement.Label != "prior" || m.phase != phaseIdle || len(m.conv.testBlocks()) == 0 || !m.prompt.Focused() {
		t.Fatalf("adopted model incomplete: id=%q title=%q workspace=%q phase=%v blocks=%d focused=%v", m.sessionID, m.sessionTitle, m.activePlacement.Label, m.phase, len(m.conv.testBlocks()), m.prompt.Focused())
	}
}

func TestSessionContinuityUX_Scenario6_StaleRunningDefersToRunEntry(t *testing.T) {
	conv := &fakeConv{}
	m := startupResumeUI(t, conv, "", "running")
	if m.sessionState != "running" || m.phase != phaseIdle {
		t.Fatalf("stale-running transcript was not displayed exactly: state=%q phase=%v", m.sessionState, m.phase)
	}
	if conv.getSessionCount != 0 || conv.createCount != 0 {
		t.Fatalf("static adoption performed run-entry work: get=%d create=%d", conv.getSessionCount, conv.createCount)
	}
}

func TestStartupRunEntryFailureRebuildsDocumentProjection(t *testing.T) {
	resume := startupSelection("existing", "running")
	resume.Transcript.Messages = []client.ConversationMessage{
		{Role: "user", Text: "old request"},
		{Role: "assistant", Text: "old tool", ToolCalls: []client.ConvToolCall{{ID: "call-1", Name: "Read", Args: `{"path":"old.go"}`}}},
		{Role: "tool", ToolResult: &client.ConvToolResult{CallID: "call-1", Content: "old result"}},
	}
	m := newTestModelFromDeps(Deps{
		Session: &fakeConv{}, Theme: testTheme(), Ctx: t.Context(), Workspace: "/launch", Resume: resume,
	})
	m.width, m.height = 100, 30
	m.vp.SetWidth(100)
	m.vp.SetHeight(20)
	m.rend.setWidth(100)
	m.refreshView() // populate the old document's tool and frame caches.
	m.sel = selection{active: true}
	m.selBase = "old selection projection"
	m.conversationView.mode = anchored
	m.conversationView.anchor = readingAnchor{blockID: 2, region: conversationRegionArguments, bias: towardStart}
	m.clickCount, m.clickL, m.clickC, m.clickGen = 2, 7, 3, 41

	resume.Transcript.Messages = []client.ConversationMessage{
		{Role: "user", Text: "new request"},
		{Role: "assistant", Text: "new tool", ToolCalls: []client.ConvToolCall{{ID: "call-1", Name: "Bash", Args: `{"command":"printf new"}`}}},
		{Role: "tool", ToolResult: &client.ConvToolResult{CallID: "call-1", Content: "new result", IsError: true}},
	}
	m = m.failStartupRunEntry(nil)

	content := stripANSIstr(m.vp.GetContent())
	if !strings.Contains(content, "new request") || !strings.Contains(content, "new result") || strings.Contains(content, "old result") {
		t.Fatalf("replacement rendered stale document content:\n%s", content)
	}
	fresh := newRenderer(m.deps.Theme, m.rend.marks)
	fresh.setWidth(m.rend.width)
	wantFrame := fresh.renderConversationFrame(&m.conv.scrollback, m.expandTools)
	if !reflect.DeepEqual(m.conversationView.frame.provenance, wantFrame.provenance) {
		t.Fatal("replacement retained stale frame provenance")
	}
	if m.sel.active || m.selBase != "" {
		t.Fatalf("replacement retained selection: active=%v base=%q", m.sel.active, m.selBase)
	}
	if m.clickCount != 0 || m.clickL != 0 || m.clickC != 0 || m.clickGen != 42 {
		t.Fatalf("replacement retained click state: count=%d point=(%d,%d) generation=%d", m.clickCount, m.clickL, m.clickC, m.clickGen)
	}
	if m.conversationView.mode != followTail || !m.vp.AtBottom() {
		t.Fatalf("replacement did not follow tail: mode=%v atBottom=%v", m.conversationView.mode, m.vp.AtBottom())
	}
}

func TestStartupRunEntryFailureClassifiesSafeStatus(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		code       codes.Code
		masked     bool
	}{
		{name: "temporary service failure", code: codes.Unavailable, want: "The service is temporarily unavailable. Retry this turn."},
		{name: "run-entry conflict", code: codes.FailedPrecondition, want: "This chat is not ready for a new turn. Retry after its current operation finishes."},
		{name: "masked absence", code: codes.NotFound, want: "This conversation could not be loaded. You cannot continue this session.", masked: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			conv := &fakeConv{recv: &fakeRecver{}, send: &fakeSender{}}
			m := startupResumeUI(t, conv, "continue", "completed")
			m = applyAll(m, startupResumeReadyMsg{})
			m = applyAll(m, client.StreamErrMsg{Err: status.Error(tc.code, "private server detail / secret-path")})

			view := stripANSIstr(m.View().Content)
			if !strings.Contains(view, tc.want) {
				t.Fatalf("startup failure view missing safe classification %q:\n%s", tc.want, view)
			}
			if strings.Contains(view, "private server detail") || strings.Contains(view, "secret-path") {
				t.Fatalf("startup failure exposed server detail:\n%s", view)
			}
			if gotMasked := strings.Contains(view, "conversation unavailable"); gotMasked != tc.masked {
				t.Fatalf("generic unavailable classification = %t, want %t:\n%s", gotMasked, tc.masked, view)
			}
		})
	}
}

func TestADR_0108_FirstPromptRevalidatesAtomically(t *testing.T) {
	newFailedModel := func() (Model, *fakeConv, *fakeSender) {
		sender := &fakeSender{}
		conv := &fakeConv{recv: &fakeRecver{}, send: sender}
		m := startupResumeUI(t, conv, "", "running")
		m.prompt.Rewrite("retry this turn")
		mm, cmd := m.submitPrompt()
		m = mm.(Model)
		runStartupCommands(cmd)
		if !m.startupFirstPromptPending {
			t.Fatal("first adopted prompt did not arm run-entry failure protection")
		}
		m = applyAll(m, client.StreamErrMsg{Err: errors.New("lease unavailable for tenant path")})
		return m, conv, sender
	}

	t.Run("failure stays closed", func(t *testing.T) {
		m, conv, _ := newFailedModel()
		if m.phase != phaseReplay || m.sessionID != "existing" || m.prompt.Focused() || conv.createCount != 0 {
			t.Fatalf("run-entry failure did not leave adopted transcript read-only: phase=%v id=%q focused=%v creates=%d", m.phase, m.sessionID, m.prompt.Focused(), conv.createCount)
		}
		view := stripANSIstr(m.View().Content)
		if !strings.Contains(view, "Retry") || !strings.Contains(view, "Back") || strings.Contains(view, "tenant path") {
			t.Fatalf("run-entry failure guidance was not closed and retryable:\n%s", view)
		}
	})

	t.Run("retry resubmits the preserved prompt", func(t *testing.T) {
		m, conv, sender := newFailedModel()
		mm, cmd := m.Update(tea.KeyPressMsg{Code: 'r', Text: "r"})
		m = mm.(Model)
		runStartupCommands(cmd)
		frames := sender.frames()
		if len(frames) != 2 || frames[1].GetPrompt().GetSessionId() != "existing" || frames[1].GetPrompt().GetText() != "retry this turn" {
			t.Fatalf("retry frames = %#v", frames)
		}
		if m.phase != phaseRunning || !m.startupFirstPromptPending || m.startupRunEntryFailed || conv.createCount != 0 {
			t.Fatalf("retry state: phase=%v pending=%v failed=%v creates=%d", m.phase, m.startupFirstPromptPending, m.startupRunEntryFailed, conv.createCount)
		}
	})

	t.Run("back restores the adopted chat", func(t *testing.T) {
		m, conv, sender := newFailedModel()
		mm, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
		m = mm.(Model)
		if m.phase != phaseIdle || m.sessionID != "existing" || !m.prompt.Focused() || m.startupRunEntryFailed || conv.createCount != 0 {
			t.Fatalf("back state: phase=%v id=%q focused=%v failed=%v creates=%d", m.phase, m.sessionID, m.prompt.Focused(), m.startupRunEntryFailed, conv.createCount)
		}
		if m.prompt.Value() != "retry this turn" || len(sender.frames()) != 1 || len(m.conv.testBlocks()) == 0 {
			t.Fatalf("back lost prompt or transcript: prompt=%q frames=%d blocks=%d", m.prompt.Value(), len(sender.frames()), len(m.conv.testBlocks()))
		}
	})
}

func TestSessionContinuityUX_Scenario6_SeedAfterAdoption(t *testing.T) {
	sender := &fakeSender{}
	conv := &fakeConv{recv: &fakeRecver{}, send: sender}
	m := startupResumeUI(t, conv, "continue once", "completed")
	mm, cmd := m.Update(startupResumeReadyMsg{})
	m = mm.(Model)
	runStartupCommands(cmd)
	frames := sender.frames()
	if len(frames) != 1 || frames[0].GetPrompt().GetSessionId() != "existing" || frames[0].GetPrompt().GetText() != "continue once" {
		t.Fatalf("seed frames = %#v", frames)
	}
	m = applyAll(m, client.SessionReadyMsg{SessionID: "existing"})
	if len(sender.frames()) != 1 || m.pendingInitialPrompt != "" {
		t.Fatalf("seed was submitted more than once: frames=%d pending=%q", len(sender.frames()), m.pendingInitialPrompt)
	}
}

func TestSessionContinuityUX_Scenario6_DefaultRemainsNew(t *testing.T) {
	conv := &fakeConv{}
	m := newTestModelFromDeps(Deps{Session: conv, Conv: conv, Theme: testTheme(), Ctx: t.Context(), Workspace: "/launch"})
	runStartupCommands(m.Init())
	if conv.createCount != 1 {
		t.Fatalf("bare default CreateSession calls = %d, want 1", conv.createCount)
	}
}
