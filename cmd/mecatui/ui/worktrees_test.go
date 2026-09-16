package ui

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

func testWorktreeSelector(token string) client.WorktreeSelector {
	selector, err := client.NewWorktreeSelector(token)
	if err != nil {
		panic(err)
	}
	return selector
}

type fakeWorktreeLister struct {
	wts       []client.Worktree
	err       error
	calls     int
	sessionID string
}

func (f *fakeWorktreeLister) List(_ context.Context, sessionID string) ([]client.Worktree, error) {
	f.calls++
	f.sessionID = sessionID
	if f.err != nil {
		return nil, f.err
	}
	return f.wts, nil
}

func newWorktreesModel(t *testing.T, conv *fakeConv, fw *fakeWorktreeLister) Model {
	t.Helper()
	m := newTestModelFromDeps(Deps{
		Session: conv, Conv: conv, Worktrees: fw,
		Theme: theme.New("aztec", theme.AztecPalette()), Workspace: "/local-composition-only",
		Mode: "default", Ctx: t.Context(), NoAltScreen: true,
	})
	return applyAll(m, tea.WindowSizeMsg{Width: 100, Height: 40}, client.SessionReadyMsg{
		SessionID: "source-session", Capabilities: client.Capabilities{Worktrees: true},
	})
}

func TestWorktreesDiscoveryIsSessionScoped(t *testing.T) {
	fw := &fakeWorktreeLister{wts: []client.Worktree{{Selector: testWorktreeSelector("opaque-1"), Kind: "git", Label: "feature", Branch: "refs/heads/feature", Revision: "abcdef1"}}}
	m := newWorktreesModel(t, &fakeConv{}, fw)
	mm, cmd := m.openWorktrees()
	m = mm.(Model)
	m = feedCmd(t, m, cmd)
	if fw.sessionID != "source-session" {
		t.Fatalf("ListWorktrees session = %q, want source-session", fw.sessionID)
	}
	if !strings.Contains(m.View().Content, "feature") || strings.Contains(m.View().Content, "/local-composition-only") {
		t.Fatalf("worktree view did not use safe metadata only:\n%s", m.View().Content)
	}
}

func TestWorktreeSwitchUsesOpaqueSelectorAndClosesAfterBinding(t *testing.T) {
	wt := client.Worktree{Selector: testWorktreeSelector("opaque-selector"), Kind: "git", Label: "feature", Branch: "refs/heads/feature"}
	conv := &fakeConv{}
	m := newWorktreesModel(t, conv, &fakeWorktreeLister{wts: []client.Worktree{wt}})
	m.conv.addUser("source transcript")
	mm, cmd, handled := m.switchToWorktree(wt)
	m = mm.(Model)
	if !handled || m.sessionID != "source-session" || m.conv.isEmpty() {
		t.Fatal("switch destroyed source binding before successor creation")
	}
	if !strings.Contains(m.statusMsg, "starting a new session") || strings.Contains(m.statusMsg, "switching worktree") {
		t.Fatalf("successor status = %q, want a new-session action without switching copy", m.statusMsg)
	}
	msg := cmd()
	ready, ok := msg.(worktreeSwitchReadyMsg)
	if !ok {
		t.Fatalf("switch result = %T, want worktreeSwitchReadyMsg", msg)
	}
	if got := conv.closed(); len(got) != 0 {
		t.Fatalf("source closed before successor binding: %v", got)
	}
	mm, closeCmd := m.Update(ready)
	m = mm.(Model)
	if m.sessionID == "source-session" || m.activePlacement.Label != "selected worktree" {
		t.Fatalf("successor not bound from server metadata: id=%q placement=%+v", m.sessionID, m.activePlacement)
	}
	runBatchLeaves(closeCmd)
	if got := conv.closed(); len(got) != 1 || got[0] != "source-session" {
		t.Fatalf("source close after bind = %v", got)
	}
}

func TestServerOwnedSessionPlacement_Scenario3_OwnershipRelistAndSafeSwitch(t *testing.T) {
	wt := client.Worktree{Selector: testWorktreeSelector("expired-after-restart"), Label: "feature"}
	conv := &fakeConv{createErr: errors.New("selector is stale")}
	m := newWorktreesModel(t, conv, &fakeWorktreeLister{wts: []client.Worktree{wt}})
	m.conv.addUser("keep me")
	mm, cmd, _ := m.switchToWorktree(wt)
	m = mm.(Model)
	failed, ok := cmd().(worktreeSwitchFailedMsg)
	if !ok {
		t.Fatalf("stale selector result = %T", cmd())
	}
	mm, _ = m.Update(failed)
	m = mm.(Model)
	if m.sessionID != "source-session" || m.conv.isEmpty() || m.phase != phaseIdle {
		t.Fatalf("failed switch changed selected session: id=%q empty=%v phase=%v", m.sessionID, m.conv.isEmpty(), m.phase)
	}
	if !strings.Contains(m.statusMsg, "relist") || len(conv.closed()) != 0 {
		t.Fatalf("failure did not require relist or closed source: status=%q closed=%v", m.statusMsg, conv.closed())
	}

	lister := &fakeWorktreeLister{err: errors.New("hidden or unknown session")}
	m.deps.Worktrees = lister
	mm, listCmd := m.openWorktrees()
	m = mm.(Model)
	m = feedCmd(t, m, listCmd)
	if m.sessionID != "source-session" || m.worktrees.err == nil {
		t.Fatal("relist failure changed selected session or hid the error")
	}
}

func TestWorktreeFilterUsesDisplayMetadata(t *testing.T) {
	wts := []client.Worktree{{Selector: testWorktreeSelector("a"), Label: "main"}, {Selector: testWorktreeSelector("b"), Label: "feature", Branch: "refs/heads/topic"}}
	got := filterWorktrees(wts, "topic")
	if len(got) != 1 || got[0].Selector.IsZero() {
		t.Fatalf("filtered worktrees = %+v", got)
	}
}

func TestWorktreeCopyDescribesStartingANewSession(t *testing.T) {
	wt := client.Worktree{Selector: testWorktreeSelector("opaque-copy"), Label: "feature", Branch: "refs/heads/feature"}
	m := newWorktreesModel(t, &fakeConv{}, &fakeWorktreeLister{wts: []client.Worktree{wt}})
	mm, cmd := m.openWorktrees()
	m = feedCmd(t, mm.(Model), cmd)
	panel := stripANSIstr(m.View().Content)
	for _, want := range []string{"select a worktree to start a new session there", "enter: select", "esc: close"} {
		if !strings.Contains(panel, want) {
			t.Fatalf("worktree picker missing %q:\n%s", want, panel)
		}
	}

	mm, _, handled := m.onWorktreesKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !handled {
		t.Fatal("enter was not handled")
	}
	m = mm.(Model)
	confirm := stripANSIstr(m.View().Content)
	for _, want := range []string{"start session in worktree", "a new session will start in:", "enter: start session", "esc: back"} {
		if !strings.Contains(confirm, want) {
			t.Fatalf("worktree confirmation missing %q:\n%s", want, confirm)
		}
	}
	if strings.Contains(strings.ToLower(confirm), "switch workspace") || strings.Contains(confirm, "enter: switch") {
		t.Fatalf("worktree confirmation still implies a workspace switch:\n%s", confirm)
	}
}
