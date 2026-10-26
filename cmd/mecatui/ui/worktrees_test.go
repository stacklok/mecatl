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
	ctx       context.Context
}

func (f *fakeWorktreeLister) List(ctx context.Context, sessionID string) ([]client.Worktree, error) {
	f.ctx = ctx
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

// feedWorktreesList reduces only discovery; widget blink timers remain owned by Tea.
func feedWorktreesList(t *testing.T, m Model, cmd tea.Cmd) Model {
	t.Helper()
	batch, ok := cmd().(tea.BatchMsg)
	if !ok || len(batch) != 2 {
		t.Fatal("open must batch discovery and filter blink")
	}
	msg := batch[0]()
	if _, ok := msg.(worktreesResultMsg); !ok {
		t.Fatalf("discovery returned %T", msg)
	}
	return applyAll(m, msg)
}

func TestWorktreesDiscoveryIsSessionScoped(t *testing.T) {
	fw := &fakeWorktreeLister{wts: []client.Worktree{{Selector: testWorktreeSelector("opaque-1"), Kind: "git", Label: "feature", Branch: "feature", Revision: "abcdef1"}}}
	m := newWorktreesModel(t, &fakeConv{}, fw)
	mm, cmd := m.openWorktrees()
	m = mm.(Model)
	m = feedWorktreesList(t, m, cmd)
	if fw.sessionID != "source-session" {
		t.Fatalf("ListWorktrees session = %q, want source-session", fw.sessionID)
	}
	if !strings.Contains(m.View().Content, "feature") || strings.Contains(m.View().Content, "/local-composition-only") {
		t.Fatalf("worktree view did not use safe metadata only:\n%s", m.View().Content)
	}
}

func TestWorktreeSwitchUsesOpaqueSelectorAndClosesAfterBinding(t *testing.T) {
	wt := client.Worktree{Selector: testWorktreeSelector("opaque-selector"), Kind: "git", Label: "feature", Branch: "feature"}
	conv := &fakeConv{}
	m := newWorktreesModel(t, conv, &fakeWorktreeLister{wts: []client.Worktree{wt}})
	m.conv.addUser("source transcript")
	mm, cmd := m.switchToWorktree(wt)
	m = mm.(Model)
	if m.sessionID != "source-session" || m.conv.isEmpty() {
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
	if calls := conv.clearCalls(); len(calls) != 1 || calls[0].sourceID != "source-session" || calls[0].selector == nil || *calls[0].selector != wt.Selector {
		t.Fatalf("successor authority = %+v, want source-session and %q", calls, wt.Selector)
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
	mm, cmd := m.switchToWorktree(wt)
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
	m = feedWorktreesList(t, m, listCmd)
	if m.sessionID != "source-session" || m.modal.(*worktreesState).err == nil {
		t.Fatal("relist failure changed selected session or hid the error")
	}
}

func TestWorktreeFilterUsesDisplayMetadata(t *testing.T) {
	wts := []client.Worktree{{Selector: testWorktreeSelector("a"), Label: "main"}, {Selector: testWorktreeSelector("b"), Label: "feature", Branch: "topic"}}
	got := filterWorktrees(wts, "topic")
	if len(got) != 1 || got[0].Selector.IsZero() {
		t.Fatalf("filtered worktrees = %+v", got)
	}
}

func TestWorktreeCopyDescribesStartingANewSession(t *testing.T) {
	wt := client.Worktree{Selector: testWorktreeSelector("opaque-copy"), Label: "feature", Branch: "feature"}
	m := newWorktreesModel(t, &fakeConv{}, &fakeWorktreeLister{wts: []client.Worktree{wt}})
	mm, cmd := m.openWorktrees()
	m = feedWorktreesList(t, mm.(Model), cmd)
	panel := stripANSIstr(m.View().Content)
	for _, want := range []string{"select a worktree to start a new session there", "enter: select", "esc: close"} {
		if !strings.Contains(panel, want) {
			t.Fatalf("worktree picker missing %q:\n%s", want, panel)
		}
	}

	mm, _, handled := m.dispatchSurfaceKey(tea.KeyPressMsg{Code: tea.KeyEnter})
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

func TestWorktreeConfirmShowsBranchOnlyWhenItDiffersFromLabel(t *testing.T) {
	for _, tc := range []struct {
		wt         client.Worktree
		wantBranch bool
	}{
		{client.Worktree{Selector: testWorktreeSelector("same"), Label: "feat/x", Branch: "feat/x"}, false},
		{client.Worktree{Selector: testWorktreeSelector("differs"), Label: "Primary repository", Branch: "main"}, true},
	} {
		m := newWorktreesModel(t, &fakeConv{}, &fakeWorktreeLister{wts: []client.Worktree{tc.wt}})
		mm, cmd := m.openWorktrees()
		m = feedWorktreesList(t, mm.(Model), cmd)
		mm, _, handled := m.dispatchSurfaceKey(tea.KeyPressMsg{Code: tea.KeyEnter})
		if !handled {
			t.Fatal("enter was not handled")
		}
		confirm := stripANSIstr(mm.(Model).View().Content)
		if got := strings.Contains(confirm, "branch: "+tc.wt.Branch); got != tc.wantBranch {
			t.Errorf("confirm for %+v shows branch line = %v, want %v:\n%s", tc.wt, got, tc.wantBranch, confirm)
		}
		if !tc.wantBranch && strings.Contains(confirm, "branch:") {
			t.Errorf("confirm repeats the branch:\n%s", confirm)
		}
	}
}

func TestWorktreeRowShowsBranchOnlyWhenItDiffersFromLabel(t *testing.T) {
	for _, tc := range []struct {
		wt   client.Worktree
		want string
	}{
		{client.Worktree{Label: "feat/x", Branch: "feat/x", Revision: "abcdef1234567"}, "feat/x  (abcdef12)"},
		{client.Worktree{Label: "Primary repository", Branch: "main"}, "Primary repository  (main)"},
		{client.Worktree{Label: "Detached worktree", Revision: "abcdef1234567"}, "Detached worktree  (abcdef12)"},
	} {
		if got := worktreeRowText(tc.wt); got != tc.want {
			t.Errorf("worktreeRowText(%+v) = %q, want %q", tc.wt, got, tc.want)
		}
	}
}
