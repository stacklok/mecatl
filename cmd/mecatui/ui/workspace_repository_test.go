package ui

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
)

func workspaceProbeResult(t *testing.T, cmd tea.Cmd) workspaceRepositoryMsg {
	t.Helper()
	if cmd == nil {
		t.Fatal("missing repository probe")
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, child := range batch {
			return workspaceProbeResult(t, child)
		}
	}
	result, ok := msg.(workspaceRepositoryMsg)
	if !ok {
		t.Fatalf("expected repository result, got %T", msg)
	}
	return result
}

func TestWorkspaceRepositoryBindingLifecycle(t *testing.T) {
	for _, entry := range []string{"create", "resume", "continue"} {
		t.Run(entry, func(t *testing.T) {
			var ids []string
			var probeCtx context.Context
			deps := Deps{Ctx: t.Context(), Theme: testTheme(), NoBanner: true,
				WorkspaceRepository: func(ctx context.Context, id string) (bool, error) {
					ids = append(ids, id)
					probeCtx = ctx
					return true, nil
				}}
			if entry == "resume" {
				deps.Resume = &client.ResumeSelection{Row: client.SessionListItem{ID: "exact-session"}}
			}
			m := New(deps)
			var updated tea.Model
			var cmd tea.Cmd
			switch entry {
			case "create":
				updated, cmd = m.Update(client.SessionReadyMsg{SessionID: "exact-session"})
			case "resume":
				updated, cmd = m.Update(startupResumeReadyMsg{})
			case "continue":
				updated, cmd, _ = m.adoptAuthoritativeTranscript(client.SessionListItem{ID: "exact-session"}, conversation{}, client.SessionSnapshot{})
			}
			m = updated.(Model)
			if len(ids) != 0 || m.workspaceInGit {
				t.Fatal("binding probed synchronously or claimed Git before confirmation")
			}
			// Continue batches focus before discovery; neither waits on a subscription.
			if entry == "continue" {
				batch := cmd().(tea.BatchMsg)
				cmd = batch[1]
			}
			result := workspaceProbeResult(t, cmd)
			m = applyAll(m, result)
			if len(ids) != 1 || ids[0] != "exact-session" || !m.workspaceInGit {
				t.Fatalf("discovery: ids=%v git=%v", ids, m.workspaceInGit)
			}
			if m.refreshSessionContextCmd() != nil {
				t.Fatal("same binding scheduled another probe")
			}
			m.phase = phaseRunning
			m = m.endRun("")
			if probeCtx.Err() != nil || !m.workspaceInGit {
				t.Fatal("ordinary run completion canceled binding discovery")
			}
			m = m.resetSession()
			if probeCtx.Err() == nil || m.workspaceInGit || m.workspaceRepository != nil {
				t.Fatal("session reset retained discovery")
			}
		})
	}
}

func TestWorkspaceRepositoryStaleAndCanceled(t *testing.T) {
	m := New(Deps{Ctx: t.Context(), Theme: testTheme(), NoBanner: true,
		WorkspaceRepository: func(context.Context, string) (bool, error) { return true, nil }})
	updated, cmd := m.Update(client.SessionReadyMsg{SessionID: "a"})
	m = updated.(Model)
	first := workspaceProbeResult(t, cmd)
	for _, id := range []string{"a", "b"} {
		updated, cmd = m.Update(client.SessionReadyMsg{SessionID: id})
		m = updated.(Model)
		if first.owner.ctx.Err() == nil {
			t.Fatal("rebind did not cancel old probe, including same-ID rebind")
		}
		m = applyAll(m, first)
		if m.workspaceInGit {
			t.Fatal("accepted stale binding result")
		}
		current := workspaceProbeResult(t, cmd)
		mismatch := current
		mismatch.sessionID = "other-session"
		m = applyAll(m, mismatch)
		if m.workspaceInGit {
			t.Fatal("accepted mismatched session")
		}
		// Modal open/close is not a session lifetime boundary. Results must pass
		// through the surface reducer, and must not masquerade as snapshot results.
		m, _, _ = openTestWorkspace(t, m)
		m = applyAll(m, current)
		if !m.workspaceInGit {
			t.Fatal("modal swallowed repository discovery")
		}
		m, _ = pressKey(m, tea.KeyPressMsg{Code: tea.KeyEscape})
		if current.owner.ctx.Err() != nil {
			t.Fatal("closing inspector canceled session probe")
		}
		first = current
	}
	updated, _ = m.quitNow()
	m = updated.(Model)
	m = applyAll(m, first)
	if first.owner.ctx.Err() == nil || m.workspaceInGit {
		t.Fatal("quit retained or accepted discovery")
	}

	ctx, cancel := context.WithCancel(t.Context())
	m = New(Deps{Ctx: ctx, Theme: testTheme(), WorkspaceRepository: func(context.Context, string) (bool, error) { return true, nil }})
	updated, cmd = m.Update(client.SessionReadyMsg{SessionID: "canceled"})
	m = updated.(Model)
	cancel()
	m = applyAll(m, workspaceProbeResult(t, cmd))
	if m.workspaceInGit {
		t.Fatal("accepted result after parent context cancellation")
	}
}

func TestWorkspaceRepositoryFooter(t *testing.T) {
	for _, test := range []struct {
		name             string
		repository       bool
		err              error
		branch, revision string
		observed         bool
		remote           bool
		want             bool
	}{
		{name: "clean-git", repository: true, want: true},
		{name: "non-git"},
		{name: "unknown", repository: true, err: errors.New("unknown")},
		{name: "remote-branch", remote: true, branch: "confirmed", want: true},
		{name: "remote-unknown", remote: true},
		{name: "remote-observed", remote: true, observed: true, want: true},
		{name: "generic-revision", revision: "not-git"},
		{name: "observed-non-git", observed: true, want: true},
		{name: "observed-unknown", observed: true, err: errors.New("unknown"), want: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			m := scenario2Model(t, 160, 40, false, phaseIdle, map[string][]string{"Workspace": {"ctrl+f12"}})
			if !test.remote {
				m.deps.WorkspaceRepository = func(context.Context, string) (bool, error) { return test.repository, test.err }
			}
			updated, cmd := m.Update(client.SessionReadyMsg{SessionID: "active"})
			m = updated.(Model)
			if !test.remote {
				m = applyAll(m, workspaceProbeResult(t, cmd))
			}
			m.activePlacement = client.Placement{Branch: test.branch, Revision: test.revision}
			if test.observed {
				m = applyAll(m, client.ToolCallMsg{ID: "failed", Name: "Edit", Args: `{"path":"announced"}`}, client.ToolResultMsg{CallID: "failed", IsError: true})
			}
			for _, phase := range []phase{phaseIdle, phaseRunning} {
				m.phase = phase
				footer := ansi.Strip(m.renderFooter())
				if strings.Contains(footer, "ctrl+f12 workspace") != test.want || strings.Contains(footer, "f10 workspace") || !strings.Contains(footer, "quit") {
					t.Fatalf("phase %v footer: %s", phase, footer)
				}
			}
			m.phase = phaseAwaitingApproval
			if strings.Contains(m.renderFooter(), "ctrl+f12 workspace") {
				t.Fatal("approval exposed underlying workspace action")
			}
			m.phase = phaseIdle
			m, _, _ = openTestWorkspace(t, m)
			if strings.Contains(m.renderFooter(), "ctrl+f12 workspace") {
				t.Fatal("modal exposed underlying workspace action")
			}
			m = m.resetSession()
			if m.workspaceHintAvailable() {
				t.Fatal("session reset retained branch or observed-path evidence")
			}
		})
	}
}

func TestWorkspaceRepositorySuccessorPlacement(t *testing.T) {
	for _, route := range []string{"clear", "worktree"} {
		t.Run(route, func(t *testing.T) {
			m, _ := newQueueModel(t)
			m.deps.ConnectionMode = "connect"
			m.activePlacement = client.Placement{Kind: "git", Label: "old", Branch: "stale"}
			oldID := m.sessionID
			m.phase = phaseConnecting
			placement := client.Placement{Kind: "git", Label: "successor", Branch: "confirmed", Revision: "abc123"}
			ready := client.SessionReadyMsg{SessionID: "successor"}
			var msg tea.Msg = worktreeSwitchReadyMsg{oldID: oldID, ready: ready, placement: placement}
			if route == "clear" {
				m.clearPending = &clearHandoff{sourceID: oldID, token: 1}
				msg = clearSessionReadyMsg{oldID: oldID, token: 1, ready: ready, placement: placement}
			}
			// Do not run the follow-up refresh: the handoff already confirms placement.
			m = applyAll(m, msg)
			if m.activePlacement != placement || m.sessionDetails().Placement != placement || !m.workspaceHintAvailable() {
				t.Fatalf("successor lost confirmed placement: %+v", m.activePlacement)
			}
			m, s, _ := openTestWorkspace(t, m)
			body, _ := s.Render(120, 60)
			if !strings.Contains(ansi.Strip(body), "Branch: confirmed") {
				t.Fatal("frozen inspector lost confirmed successor branch")
			}
			m, _ = pressKey(m, tea.KeyPressMsg{Code: tea.KeyEscape})
			m = m.bindSessionID("other")
			if m.activePlacement != (client.Placement{}) || m.workspaceHintAvailable() {
				t.Fatal("new binding retained stale placement evidence")
			}
		})
	}
}

func TestWorkspaceRepositoryNoImplicitResampling(t *testing.T) {
	source := &statusSourceFake{changed: make(chan struct{}, 1)}
	m := scenario2Model(t, 100, 40, false, phaseRunning, nil)
	m.deps.StatusSource = source
	probes, snapshots := 0, 0
	m.deps.WorkspaceRepository = func(context.Context, string) (bool, error) { probes++; return true, nil }
	m.deps.WorkspaceSnapshot = func(context.Context, string) (WorkspaceSnapshot, error) { snapshots++; return WorkspaceSnapshot{}, nil }
	m.conv.recordFileChange("observed")
	for range 3 {
		m.refreshView()
		_ = m.View()
		var cmd tea.Cmd
		m, cmd = pressKey(m, f9)
		if cmd != nil {
			t.Fatalf("F9 scheduled unexpected work: %T", cmd)
		}
		var updated tea.Model
		updated, cmd = m.Update(client.AssistantDeltaMsg{Turn: 1, Text: "stream"})
		m = updated.(Model)
		if cmd == nil || m.streamCh != nil {
			t.Fatal("expected only a bounded render timer without a stream subscription")
		}
		tick := waitStatusMessage(t, cmd)
		if _, ok := tick.(renderTickMsg); !ok {
			t.Fatalf("stream scheduled unexpected work: %T", tick)
		}
		updated, cmd = m.Update(tick)
		m = updated.(Model)
		if cmd != nil {
			t.Fatalf("render tick scheduled unexpected work: %T", cmd)
		}
		source.publish(source.Latest())
		updated, cmd = m.Update(waitStatusMessage(t, m.statusLineWaitCmd()))
		m = updated.(Model)
		if cmd == nil {
			t.Fatal("status refresh did not rearm its source poll")
		}
		source.publish(source.Latest())
		next := waitStatusMessage(t, cmd)
		if _, ok := next.(statusLineChangedMsg); !ok {
			t.Fatalf("status refresh scheduled unexpected work: %T", next)
		}
	}
	if probes != 0 || snapshots != 0 {
		t.Fatal("render/F9/stream invoked Git collaborators")
	}
	m, _, load := openTestWorkspace(t, m)
	m = applyAll(m, load())
	if probes != 0 || snapshots != 1 {
		t.Fatal("inspector open changed discovery or resampled")
	}
	m, _ = pressKey(m, tea.KeyPressMsg{Code: tea.KeyEscape})
	for _, width := range []int{35, 60, 100, 160} {
		m.width = width
		for _, phase := range []phase{phaseIdle, phaseRunning} {
			m.phase = phase
			line := strings.Split(ansi.Strip(m.renderFooter()), "\n")
			help := line[len(line)-1]
			if strings.Contains(help, "workspace") && (ansi.StringWidth(help) > width || !strings.HasSuffix(help, "quit")) {
				t.Fatalf("unbounded hint or lost quit at %d: %s", width, help)
			}
		}
	}
	m = m.resetSession()
	if m.workspaceHintAvailable() {
		t.Fatal("observed paths survived session reset")
	}
}
