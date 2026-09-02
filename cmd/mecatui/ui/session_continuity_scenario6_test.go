package ui

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

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
	return newTestModelFromDeps(Deps{
		Session: conv, Conv: conv, Theme: testTheme(), Ctx: t.Context(), Workspace: "/launch",
		Resume: startupSelection("existing", state), InitialPrompt: seed,
	})
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
	if m.sessionID != "existing" || m.sessionTitle != "Prior chat" || m.activePlacement.Label != "prior" || m.phase != phaseIdle || len(m.conv.blocks) == 0 || !m.prompt.Focused() {
		t.Fatalf("adopted model incomplete: id=%q title=%q workspace=%q phase=%v blocks=%d focused=%v", m.sessionID, m.sessionTitle, m.activePlacement.Label, m.phase, len(m.conv.blocks), m.prompt.Focused())
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
		if m.prompt.Value() != "retry this turn" || len(sender.frames()) != 1 || len(m.conv.blocks) == 0 {
			t.Fatalf("back lost prompt or transcript: prompt=%q frames=%d blocks=%d", m.prompt.Value(), len(sender.frames()), len(m.conv.blocks))
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
