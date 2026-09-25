package ui

import (
	"context"

	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
)

// PendingApprovalWatch is the caller-owned exact-run receive side.
type PendingApprovalWatch interface {
	Recv() (client.PendingApprovalEvent, error)
	Close()
}

// PendingApprovalController is the narrow exact-run recovery transport.
type PendingApprovalController interface {
	WatchPendingApprovalRun(context.Context, client.PendingApproval) (PendingApprovalWatch, error)
	ResolvePendingApproval(context.Context, client.PendingApproval, client.Verdict) error
	CancelPendingRun(context.Context, client.PendingApproval) error
}

type pendingApprovalRecovery struct {
	approval  client.PendingApproval
	watch     PendingApprovalWatch
	resolved  bool
	resolving bool
	cancelled bool
}

type pendingApprovalWatchOpenedMsg struct {
	watch PendingApprovalWatch
	event client.PendingApprovalEvent
	err   error
}

type pendingApprovalWatchMsg struct {
	event client.PendingApprovalEvent
	err   error
}

type pendingApprovalResolvedMsg struct {
	askID string
	err   error
}
type pendingApprovalCancelledMsg struct{ err error }

func (m Model) openPendingApprovalWatchCmd() tea.Cmd {
	recovery := m.pendingRecovery
	controller := m.deps.PendingApprovals
	return func() tea.Msg {
		if recovery == nil || controller == nil {
			return pendingApprovalWatchOpenedMsg{err: context.Canceled}
		}
		watch, err := controller.WatchPendingApprovalRun(m.deps.Ctx, recovery.approval)
		if err != nil {
			return pendingApprovalWatchOpenedMsg{err: err}
		}
		event, err := watch.Recv()
		if err != nil {
			watch.Close()
		}
		return pendingApprovalWatchOpenedMsg{watch: watch, event: event, err: err}
	}
}

func pendingApprovalRecvCmd(watch PendingApprovalWatch) tea.Cmd {
	return func() tea.Msg {
		event, err := watch.Recv()
		return pendingApprovalWatchMsg{event: event, err: err}
	}
}

func (m *Model) resolvePendingApprovalCmd(verdict client.Verdict) tea.Cmd {
	if m.pendingRecovery == nil || m.pendingRecovery.resolving || m.pendingRecovery.resolved {
		return nil
	}
	m.pendingRecovery.resolving = true
	controller := m.deps.PendingApprovals
	approval := m.pendingRecovery.approval
	return func() tea.Msg {
		if controller == nil {
			return pendingApprovalResolvedMsg{askID: approval.AskID, err: context.Canceled}
		}
		return pendingApprovalResolvedMsg{askID: approval.AskID, err: controller.ResolvePendingApproval(m.deps.Ctx, approval, verdict)}
	}
}

func (m *Model) cancelPendingApprovalCmd() tea.Cmd {
	if m.pendingRecovery == nil || !m.pendingRecovery.resolved || m.pendingRecovery.cancelled {
		return nil
	}
	m.pendingRecovery.cancelled = true
	controller := m.deps.PendingApprovals
	approval := m.pendingRecovery.approval
	return func() tea.Msg {
		if controller == nil {
			return pendingApprovalCancelledMsg{err: context.Canceled}
		}
		return pendingApprovalCancelledMsg{err: controller.CancelPendingRun(m.deps.Ctx, approval)}
	}
}

func (m *Model) closePendingApprovalWatch() {
	if m.pendingRecovery != nil && m.pendingRecovery.watch != nil {
		m.pendingRecovery.watch.Close()
		m.pendingRecovery.watch = nil
	}
}

func (m Model) refusePendingApprovalRecovery(text string) (tea.Model, tea.Cmd) {
	(&m).closePendingApprovalWatch()
	m.closeModal()
	m.phase = phaseFatal
	m.statusMsg = m.deps.Theme.Style("errorText").Render(text)
	m.conv.addError(text)
	m.prompt.Blur()
	m.refreshView()
	return m, nil
}

func (m Model) updatePendingApproval(msg tea.Msg) (tea.Model, tea.Cmd, bool) {
	if m.pendingRecovery == nil {
		return m, nil, false
	}
	switch msg := msg.(type) {
	case pendingApprovalWatchOpenedMsg:
		if msg.err != nil || msg.watch == nil || msg.event.Kind != client.PendingApprovalEventBoundary {
			if msg.watch != nil {
				msg.watch.Close()
			}
			mm, cmd := m.refusePendingApprovalRecovery("pending approval recovery could not establish a gap-free watch; leave the approval pending and retry")
			return mm, cmd, true
		}
		m.pendingRecovery.watch = msg.watch
		m.phase = phaseRunning
		ask := m.pendingRecovery.approval
		mm, cmd := m.applyPermissionAsk(client.PermissionAskMsg{RunID: ask.RunID, AskID: ask.AskID, Tool: ask.Tool, Args: ask.Args, Reason: ask.Reason, ExpectedRunID: ask.RunID, Recovery: true})
		m = mm.(Model)
		return m, tea.Batch(cmd, pendingApprovalRecvCmd(msg.watch)), true
	case pendingApprovalResolvedMsg:
		if msg.askID != m.pendingRecovery.approval.AskID {
			return m, nil, true
		}
		m.pendingRecovery.resolving = false
		if msg.err != nil {
			mm, cmd := m.refusePendingApprovalRecovery("the pending approval changed or could not be acknowledged; no verdict will be resent")
			return mm, cmd, true
		}
		m.pendingRecovery.resolved = true
		return m, nil, true
	case pendingApprovalCancelledMsg:
		if msg.err != nil {
			mm, cmd := m.refusePendingApprovalRecovery("the recovered run could not be cancelled; check the session before retrying")
			return mm, cmd, true
		}
		return m, nil, true
	case pendingApprovalWatchMsg:
		if msg.err != nil {
			mm, cmd := m.refusePendingApprovalRecovery("the pending approval watch ended before a terminal result; check the session before retrying")
			return mm, cmd, true
		}
		if msg.event.Kind == client.PendingApprovalEventResolved && !m.pendingRecovery.resolved && !m.pendingRecovery.resolving {
			mm, cmd := m.refusePendingApprovalRecovery("the pending approval changed before your choice; no verdict was sent")
			return mm, cmd, true
		}
		if msg.event.Kind == client.PendingApprovalEventRetracted {
			mm, cmd := m.refusePendingApprovalRecovery("the pending approval was withdrawn; no verdict was sent")
			return mm, cmd, true
		}
		if msg.event.Kind == client.PendingApprovalEventAsk && msg.event.Approval != nil {
			ask := *msg.event.Approval
			m.pendingRecovery.approval = ask
			m.pendingRecovery.resolved = false
			m.pendingRecovery.resolving = false
			m.pendingRecovery.cancelled = false
			mm, cmd := m.applyPermissionAsk(client.PermissionAskMsg{RunID: ask.RunID, AskID: ask.AskID, Tool: ask.Tool, Args: ask.Args, Reason: ask.Reason, ExpectedRunID: ask.RunID, Recovery: true})
			m = mm.(Model)
			return m, tea.Batch(cmd, pendingApprovalRecvCmd(m.pendingRecovery.watch)), true
		}
		if msg.event.Kind == client.PendingApprovalEventOther && msg.event.Message == nil {
			mm, cmd := m.refusePendingApprovalRecovery("the pending approval watch returned an unknown event; no control was sent")
			return mm, cmd, true
		}
		var eventCmd tea.Cmd
		if msg.event.Message != nil {
			mm, cmd := m.updateStreamEvent(msg.event.Message)
			m, eventCmd = mm.(Model), cmd
		}
		if msg.event.Kind == client.PendingApprovalEventTerminal {
			(&m).closePendingApprovalWatch()
			m.pendingRecovery = nil
			return m, eventCmd, true
		}
		return m, tea.Batch(eventCmd, pendingApprovalRecvCmd(m.pendingRecovery.watch)), true
	default:
		return m, nil, false
	}
}
