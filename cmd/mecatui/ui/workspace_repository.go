package ui

import (
	"context"

	tea "charm.land/bubbletea/v2"
)

type workspaceRepositoryRequest struct {
	ctx    context.Context //nolint:containedctx // Binding lifetime, independent of runs and surfaces.
	cancel context.CancelFunc
}

type workspaceRepositoryMsg struct {
	owner     *workspaceRepositoryRequest
	sessionID string
	inGit     bool
}

func (m *Model) resetWorkspaceRepository() {
	if m.workspaceRepository != nil {
		m.workspaceRepository.cancel()
	}
	m.workspaceRepository = nil
	m.workspaceInGit = false
}

// Session establishment owns discovery; rendering and inspector opens never do.
func (m *Model) refreshSessionContextCmd() tea.Cmd {
	status := m.refreshStatusContextCmd()
	if m.workspaceRepository != nil || m.sessionID == "" || m.deps.WorkspaceRepository == nil {
		return status
	}
	ctx, cancel := context.WithCancel(m.deps.Ctx)
	request := &workspaceRepositoryRequest{ctx: ctx, cancel: cancel}
	m.workspaceRepository = request
	probe, id := m.deps.WorkspaceRepository, m.sessionID
	return tea.Batch(status, func() tea.Msg {
		inGit, err := probe(ctx, id)
		return workspaceRepositoryMsg{owner: request, sessionID: id, inGit: err == nil && inGit}
	})
}

func (m Model) workspaceHintAvailable() bool {
	return m.workspaceInGit || m.activePlacement.Branch != "" || m.changedFilesIndicator() != ""
}
