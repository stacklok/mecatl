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

// fakeWorktreeLister is a spy client.WorktreeLister for the /worktrees overlay
// tests (issue #102): it returns a fixed slice (recording the call count) or a
// fixed error.
type fakeWorktreeLister struct {
	wts   []client.Worktree
	err   error
	calls int
}

func (f *fakeWorktreeLister) List(_ context.Context, _ string) ([]client.Worktree, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return f.wts, nil
}

// newWorktreesModel builds a Model wired with a fakeConv + a worktree lister,
// driven through the connect (SessionReadyMsg) so it is idle and ready.
func newWorktreesModel(t *testing.T, conv *fakeConv, fw *fakeWorktreeLister, caps client.Capabilities) Model {
	t.Helper()
	m := newTestModelFromDeps(Deps{
		Session:     conv,
		Conv:        conv,
		Worktrees:   fw,
		Theme:       theme.New("aztec", theme.AztecPalette()),
		Workspace:   "/workspace",
		Mode:        "default",
		Model:       "mock-model",
		Ctx:         context.Background(),
		NoAltScreen: true,
	})
	m = applyAll(m,
		tea.WindowSizeMsg{Width: 100, Height: 40},
		client.SessionReadyMsg{
			SessionID:    "sess-test-0001",
			Capabilities: caps,
		},
	)
	return m
}

// newWorktreesConv builds a minimal fakeConv (a real client.Stream over a
// scripted fakeRecver is not needed — these tests never drive a run to
// completion; they assert the overlay + the restart-now handoff call sequence).
func newWorktreesConv(caps client.Capabilities) *fakeConv {
	return &fakeConv{recv: &fakeRecver{gate: make(chan struct{})}, send: &fakeSender{}, caps: caps}
}

func worktreesCaps() client.Capabilities { return client.Capabilities{Worktrees: true} }

// flattenBatch runs cmd; if it yields a tea.BatchMsg it returns each leaf
// command's msg (recursively flattening nested batches), otherwise a one-element
// slice with the single msg. A nil cmd yields an empty slice. Used by the
// /worktrees tests to find the SessionReadyMsg/restartFailedMsg leaf among the
// batched handoff commands.
func flattenBatch(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		var out []tea.Msg
		for _, c := range batch {
			out = append(out, flattenBatch(c)...)
		}
		return out
	}
	return []tea.Msg{msg}
}

// TestRunWorktreesOpensOverlay asserts runWorktrees opens the picker, blurs the
// input, fires ListWorktrees, and renders the rows once the result lands.
func TestRunWorktreesOpensOverlay(t *testing.T) {
	fw := &fakeWorktreeLister{wts: []client.Worktree{
		{Path: "/repo", Branch: "refs/heads/main", Head: "abcdef1"},
		{Path: "/repo-wt", Branch: "refs/heads/feature", Head: "1234567"},
	}}
	conv := newWorktreesConv(worktreesCaps())
	m := newWorktreesModel(t, conv, fw, worktreesCaps())

	mm, cmd := m.runWorktrees()
	m = mm.(Model)
	if m.worktrees.view != worktreesPanel {
		t.Fatalf("view = %v, want worktreesPanel", m.worktrees.view)
	}
	if !m.worktrees.loading {
		t.Error("overlay should be loading until ListWorktrees lands")
	}
	if m.ta.Focused() {
		t.Error("opening the overlay should blur the textarea")
	}
	if cmd == nil {
		t.Fatal("runWorktrees should fire the ListWorktrees RPC command")
	}
	m = feedCmd(t, m, cmd)
	if fw.calls != 1 {
		t.Errorf("ListWorktrees calls = %d, want 1", fw.calls)
	}
	if !strings.Contains(m.View().Content, "/repo") {
		t.Errorf("overlay missing a worktree path:\n%s", m.View().Content)
	}
}

// TestRunWorktreesNilGuard: with no lister wired (a no-FS/cloud server),
// openWorktrees is a no-op.
func TestRunWorktreesNilGuard(t *testing.T) {
	conv := newWorktreesConv(client.Capabilities{})
	m := newTestModelFromDeps(Deps{
		Session: conv, Conv: conv, Theme: theme.New("aztec", theme.AztecPalette()),
		Workspace: "/ws", Ctx: context.Background(), NoAltScreen: true,
	})
	m = applyAll(m, tea.WindowSizeMsg{Width: 100, Height: 40}, client.SessionReadyMsg{SessionID: "s1"})
	mm, cmd := m.openWorktrees()
	m = mm.(Model)
	if m.worktrees.view != worktreesNone {
		t.Fatalf("openWorktrees with nil lister should be a no-op, view = %v", m.worktrees.view)
	}
	if cmd != nil {
		t.Errorf("openWorktrees with nil lister should fire no command, got %v", cmd)
	}
}

// TestRunWorktreesNotIdle: opening mid-run is a no-op.
func TestRunWorktreesNotIdle(t *testing.T) {
	fw := &fakeWorktreeLister{wts: []client.Worktree{{Path: "/repo"}}}
	conv := newWorktreesConv(worktreesCaps())
	m := newWorktreesModel(t, conv, fw, worktreesCaps())
	m.phase = phaseRunning // mid-run
	mm, _ := m.openWorktrees()
	m = mm.(Model)
	if m.worktrees.view != worktreesNone {
		t.Fatalf("openWorktrees mid-run should be a no-op, view = %v", m.worktrees.view)
	}
}

// TestSelectWorktreeRestartsSession asserts the restart-now handoff: it closes
// the OLD session and calls CreateSessionInWorkspace with the chosen worktree
// PATH, emitting a SessionReadyMsg that rebinds the model.
func TestSelectWorktreeRestartsSession(t *testing.T) {
	fw := &fakeWorktreeLister{wts: []client.Worktree{
		{Path: "/repo", Branch: "refs/heads/main"},
		{Path: "/repo-wt", Branch: "refs/heads/feature"},
	}}
	conv := newWorktreesConv(worktreesCaps())
	m := newWorktreesModel(t, conv, fw, worktreesCaps())

	// Open + load the list.
	mm, _ := m.openWorktrees()
	m = mm.(Model)
	m = applyAll(m, client.WorktreesMsg{Worktrees: fw.wts})
	if m.worktrees.view != worktreesPanel {
		t.Fatalf("view = %v, want worktreesPanel", m.worktrees.view)
	}

	// Cursor on the second row (the sibling worktree); Enter → confirm; Enter → switch.
	m.worktrees.cursor = 1
	m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyEnter}) // chooseWorktree → worktreesConfirm
	if m.worktrees.view != worktreesConfirm {
		t.Fatalf("after enter: view = %v, want worktreesConfirm", m.worktrees.view)
	}
	if m.worktrees.confirm.Path != "/repo-wt" {
		t.Fatalf("confirm candidate = %+v, want /repo-wt", m.worktrees.confirm)
	}

	mm, cmd, _ := m.onWorktreesConfirmKey(tea.KeyPressMsg{Code: tea.KeyEnter}) // switch
	m = mm.(Model)
	if cmd == nil {
		t.Fatal("switchToWorktree should fire restartOnWorkspaceCmd")
	}
	// The handoff drove phase to connecting and cleared the session id.
	if m.phase != phaseConnecting {
		t.Fatalf("phase = %v, want phaseConnecting", m.phase)
	}
	if m.sessionID != "" {
		t.Fatalf("sessionID = %q, want empty (cleared for rebind)", m.sessionID)
	}
	// Run the restart cmd: it is a tea.Batch (closeWorktrees cmd + restartOnWorkspaceCmd
	// + sp.Tick); flatten it to find the SessionReadyMsg leaf.
	msgs := flattenBatch(cmd)
	var srm client.SessionReadyMsg
	found := false
	for _, msg := range msgs {
		if s, ok := msg.(client.SessionReadyMsg); ok {
			srm = s
			found = true
		}
	}
	if !found {
		t.Fatalf("restart cmd msgs = %+v, want a client.SessionReadyMsg among them", msgs)
	}
	conv.mu.Lock()
	closed := len(conv.closedIDs)
	wksp := conv.createdWksp
	conv.mu.Unlock()
	if closed != 1 {
		t.Errorf("CloseSession calls = %d, want 1 (close the old session)", closed)
	}
	if wksp != "/repo-wt" {
		t.Errorf("CreateSessionInWorkspace workspace = %q, want /repo-wt", wksp)
	}
	if srm.SessionID == "" {
		t.Error("SessionReadyMsg carried an empty session id")
	}
}

// TestWorktreesRestartFailureRecoverable asserts a failed CreateSessionInWorkspace
// yields restartFailedMsg (reused from /models), leaving the app RECOVERABLE
// (not fatal) rather than terminal.
func TestWorktreesRestartFailureRecoverable(t *testing.T) {
	fw := &fakeWorktreeLister{wts: []client.Worktree{{Path: "/repo-wt"}}}
	conv := newWorktreesConv(worktreesCaps())
	conv.secondCreateErr = nil
	conv.createErr = errors.New("server gone") // the restart-now re-create fails (the connect was applied directly, so only the restart hits the fake)
	m := newWorktreesModel(t, conv, fw, worktreesCaps())

	mm, _ := m.openWorktrees()
	m = mm.(Model)
	m = applyAll(m, client.WorktreesMsg{Worktrees: fw.wts})
	m.worktrees.cursor = 0
	m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyEnter})                       // confirm
	mm, cmd, _ := m.onWorktreesConfirmKey(tea.KeyPressMsg{Code: tea.KeyEnter}) // switch
	m = mm.(Model)
	msgs := flattenBatch(cmd)
	got := false
	for _, msg := range msgs {
		if _, ok := msg.(restartFailedMsg); ok {
			got = true
		}
	}
	if !got {
		t.Fatalf("restart cmd msgs = %+v, want a restartFailedMsg among them", msgs)
	}
	// The app stays recoverable: phaseConnecting (not fatal), no session.
	if m.phase == phaseFatal {
		t.Error("a restart failure must NOT be terminal (phaseFatal) — it should stay recoverable")
	}
}

// TestWorktreesFilterNarrows: the filter input narrows the list by path/branch.
func TestWorktreesFilterNarrows(t *testing.T) {
	fw := &fakeWorktreeLister{wts: []client.Worktree{
		{Path: "/repo", Branch: "refs/heads/main"},
		{Path: "/repo-feature", Branch: "refs/heads/feature"},
	}}
	conv := newWorktreesConv(worktreesCaps())
	m := newWorktreesModel(t, conv, fw, worktreesCaps())
	mm, _ := m.openWorktrees()
	m = mm.(Model)
	m = applyAll(m, client.WorktreesMsg{Worktrees: fw.wts})
	if len(m.worktrees.filtered) != 2 {
		t.Fatalf("filtered = %d, want 2 before filter", len(m.worktrees.filtered))
	}
	m.worktrees.filter.SetValue("feature")
	m = m.syncWorktreesFilter()
	if len(m.worktrees.filtered) != 1 {
		t.Fatalf("filtered = %d, want 1 after 'feature'", len(m.worktrees.filtered))
	}
	if m.worktrees.filtered[0].Path != "/repo-feature" {
		t.Errorf("filtered[0] = %+v, want /repo-feature", m.worktrees.filtered[0])
	}
}

// TestSwitchToWorktreeSetsActiveWorkspace asserts that after switchToWorktree is
// called, m.activeWorkspace == wt.Path (the AC#3 header indicator source).
func TestSwitchToWorktreeSetsActiveWorkspace(t *testing.T) {
	wt := client.Worktree{Path: "/repo-wt", Branch: "refs/heads/feature"}
	fw := &fakeWorktreeLister{wts: []client.Worktree{wt}}
	conv := newWorktreesConv(worktreesCaps())
	m := newWorktreesModel(t, conv, fw, worktreesCaps())

	mm, _, _ := m.switchToWorktree(wt)
	m = mm.(Model)
	if m.activeWorkspace != wt.Path {
		t.Errorf("activeWorkspace = %q, want %q", m.activeWorkspace, wt.Path)
	}
}

// TestWorktreesEscCloses: esc closes the overlay and refocuses the prompt.
func TestWorktreesEscCloses(t *testing.T) {
	fw := &fakeWorktreeLister{wts: []client.Worktree{{Path: "/repo"}}}
	conv := newWorktreesConv(worktreesCaps())
	m := newWorktreesModel(t, conv, fw, worktreesCaps())
	mm, _ := m.openWorktrees()
	m = mm.(Model)
	m = applyAll(m, client.WorktreesMsg{Worktrees: fw.wts})
	mm, _, _ = m.onWorktreesKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	m = mm.(Model)
	if m.worktrees.view != worktreesNone {
		t.Fatalf("esc should close the overlay, view = %v", m.worktrees.view)
	}
}
