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
	approval      client.PendingApproval
	watch         PendingApprovalWatch
	ctx           context.Context
	cancel        context.CancelFunc
	generation    uint64
	snapshotCalls map[string]struct{}
	resolved      bool
	resolving     bool
	cancelled     bool
}

type pendingApprovalWatchOpenedMsg struct {
	generation uint64
	watch      PendingApprovalWatch
	event      client.PendingApprovalEvent
	err        error
}

type pendingApprovalWatchMsg struct {
	generation uint64
	event      client.PendingApprovalEvent
	err        error
}

type pendingApprovalResolvedMsg struct {
	generation uint64
	approval   client.PendingApproval
	err        error
}
type pendingApprovalCancelledMsg struct {
	generation uint64
	approval   client.PendingApproval
	err        error
}

func samePendingApproval(a, b client.PendingApproval) bool {
	return a.SessionID == b.SessionID && a.RunID == b.RunID && a.AskID == b.AskID
}

func (m Model) openPendingApprovalWatchCmd() tea.Cmd {
	recovery := m.pendingRecovery
	controller := m.deps.PendingApprovals
	return func() tea.Msg {
		if recovery == nil || controller == nil {
			return pendingApprovalWatchOpenedMsg{err: context.Canceled}
		}
		watch, err := controller.WatchPendingApprovalRun(recovery.ctx, recovery.approval)
		if err != nil {
			return pendingApprovalWatchOpenedMsg{generation: recovery.generation, err: err}
		}
		if recovery.ctx.Err() != nil {
			watch.Close()
			return pendingApprovalWatchOpenedMsg{generation: recovery.generation, err: context.Canceled}
		}
		event, err := watch.Recv()
		if err != nil || recovery.ctx.Err() != nil {
			watch.Close()
			if err == nil {
				err = context.Canceled
			}
		}
		return pendingApprovalWatchOpenedMsg{generation: recovery.generation, watch: watch, event: event, err: err}
	}
}

func pendingApprovalRecvCmd(generation uint64, watch PendingApprovalWatch) tea.Cmd {
	return func() tea.Msg {
		if watch == nil {
			return pendingApprovalWatchMsg{generation: generation, err: context.Canceled}
		}
		event, err := watch.Recv()
		return pendingApprovalWatchMsg{generation: generation, event: event, err: err}
	}
}

func (m *Model) resolvePendingApprovalCmd(verdict client.Verdict) tea.Cmd {
	if m.pendingRecovery == nil || m.pendingRecovery.resolving || m.pendingRecovery.resolved {
		return nil
	}
	m.pendingRecovery.resolving = true
	controller := m.deps.PendingApprovals
	recovery := m.pendingRecovery
	approval := recovery.approval
	return func() tea.Msg {
		if controller == nil {
			return pendingApprovalResolvedMsg{generation: recovery.generation, approval: approval, err: context.Canceled}
		}
		return pendingApprovalResolvedMsg{generation: recovery.generation, approval: approval, err: controller.ResolvePendingApproval(recovery.ctx, approval, verdict)}
	}
}

func (m *Model) cancelPendingApprovalCmd() tea.Cmd {
	if m.pendingRecovery == nil || !m.pendingRecovery.resolved || m.pendingRecovery.cancelled {
		return nil
	}
	m.pendingRecovery.cancelled = true
	controller := m.deps.PendingApprovals
	recovery := m.pendingRecovery
	approval := recovery.approval
	return func() tea.Msg {
		if controller == nil {
			return pendingApprovalCancelledMsg{generation: recovery.generation, approval: approval, err: context.Canceled}
		}
		return pendingApprovalCancelledMsg{generation: recovery.generation, approval: approval, err: controller.CancelPendingRun(recovery.ctx, approval)}
	}
}

func (m *Model) retirePendingApprovalRecovery() {
	if m.pendingRecovery == nil {
		return
	}
	m.pendingRecovery.cancel()
	if m.pendingRecovery.watch != nil {
		m.pendingRecovery.watch.Close()
	}
	m.pendingRecovery = nil
}

func (m Model) refusePendingApprovalRecovery(text string) Model {
	(&m).retirePendingApprovalRecovery()
	m.closeModal()
	m.phase = phaseFatal
	m.statusMsg = m.deps.Theme.Style("errorText").Render(text)
	m.conv.addError(text)
	m.prompt.Blur()
	m.refreshView()
	return m
}

func (m Model) updatePendingApproval(msg tea.Msg) (tea.Model, tea.Cmd, bool) {
	if m.pendingRecovery == nil {
		return m.consumeRetiredPendingApprovalMessage(msg)
	}

	switch msg := msg.(type) {
	case pendingApprovalWatchOpenedMsg:
		return m.updatePendingApprovalWatchOpened(msg)
	case pendingApprovalResolvedMsg:
		return m.updatePendingApprovalResolved(msg)
	case pendingApprovalCancelledMsg:
		return m.updatePendingApprovalCancelled(msg)
	case pendingApprovalWatchMsg:
		return m.updatePendingApprovalWatch(msg)
	default:
		return m, nil, false
	}
}

// Recovery messages are always consumed. A late open owns a watch that the
// retired generation can no longer publish, so close it here.
func (m Model) consumeRetiredPendingApprovalMessage(msg tea.Msg) (tea.Model, tea.Cmd, bool) {
	switch msg := msg.(type) {
	case pendingApprovalWatchOpenedMsg:
		if msg.watch != nil {
			msg.watch.Close()
		}
		return m, nil, true
	case pendingApprovalWatchMsg, pendingApprovalResolvedMsg, pendingApprovalCancelledMsg:
		return m, nil, true
	default:
		return m, nil, false
	}
}

func (m Model) updatePendingApprovalWatchOpened(msg pendingApprovalWatchOpenedMsg) (tea.Model, tea.Cmd, bool) {
	recovery := m.pendingRecovery
	if msg.generation != recovery.generation {
		if msg.watch != nil {
			msg.watch.Close()
		}
		return m, nil, true
	}
	if msg.err != nil || msg.watch == nil || msg.event.Kind != client.PendingApprovalEventBoundary {
		if msg.watch != nil {
			msg.watch.Close()
		}
		return m.refusePendingApprovalRecovery("pending approval recovery could not establish a gap-free watch; leave the approval pending and retry"), nil, true
	}
	recovery.watch = msg.watch
	m.phase = phaseRunning
	ask := recovery.approval
	mm, cmd := m.applyPermissionAsk(client.PermissionAskMsg{RunID: ask.RunID, AskID: ask.AskID, Tool: ask.Tool, Args: ask.Args, Reason: ask.Reason, ExpectedRunID: ask.RunID, Recovery: true})
	m = mm.(Model)
	return m, tea.Batch(cmd, pendingApprovalRecvCmd(recovery.generation, msg.watch)), true
}

func (m Model) updatePendingApprovalResolved(msg pendingApprovalResolvedMsg) (tea.Model, tea.Cmd, bool) {
	recovery := m.pendingRecovery
	if msg.generation != recovery.generation || !samePendingApproval(msg.approval, recovery.approval) {
		return m, nil, true
	}
	recovery.resolving = false
	if msg.err != nil {
		return m.refusePendingApprovalRecovery("the pending approval changed or could not be acknowledged; no verdict will be resent"), nil, true
	}
	recovery.resolved = true
	return m, nil, true
}

func (m Model) updatePendingApprovalCancelled(msg pendingApprovalCancelledMsg) (tea.Model, tea.Cmd, bool) {
	recovery := m.pendingRecovery
	if msg.generation != recovery.generation || !samePendingApproval(msg.approval, recovery.approval) {
		return m, nil, true
	}
	if msg.err != nil {
		return m.refusePendingApprovalRecovery("the recovered run could not be cancelled; check the session before retrying"), nil, true
	}
	(&m).retirePendingApprovalRecovery()
	return m, tea.Quit, true
}

func (m Model) updatePendingApprovalWatch(msg pendingApprovalWatchMsg) (tea.Model, tea.Cmd, bool) {
	recovery := m.pendingRecovery
	if msg.generation != recovery.generation {
		return m, nil, true
	}
	if msg.err != nil {
		text := "the pending approval watch ended before a terminal result; check the session before retrying"
		if !recovery.resolving && !recovery.resolved {
			text = "the pending approval watch ended before your choice; leave the approval pending and retry"
		}
		return m.refusePendingApprovalRecovery(text), nil, true
	}
	if msg.event.Kind == client.PendingApprovalEventResolved && !recovery.resolved && !recovery.resolving {
		return m.refusePendingApprovalRecovery("the pending approval changed before your choice; no verdict was sent"), nil, true
	}
	if msg.event.Kind == client.PendingApprovalEventRetracted {
		text := "the pending approval was withdrawn; no verdict was sent"
		if recovery.resolving || recovery.resolved {
			text = "the recovered approval was withdrawn after your choice; check the session before retrying"
		}
		return m.refusePendingApprovalRecovery(text), nil, true
	}
	if msg.event.Kind == client.PendingApprovalEventAsk && msg.event.Approval != nil {
		ask := *msg.event.Approval
		recovery.approval = ask
		recovery.resolved = false
		recovery.resolving = false
		recovery.cancelled = false
		mm, cmd := m.applyPermissionAsk(client.PermissionAskMsg{RunID: ask.RunID, AskID: ask.AskID, Tool: ask.Tool, Args: ask.Args, Reason: ask.Reason, ExpectedRunID: ask.RunID, Recovery: true})
		m = mm.(Model)
		return m, tea.Batch(cmd, pendingApprovalRecvCmd(recovery.generation, recovery.watch)), true
	}
	// After an explicit choice, follow EventToMsg's nil/skip convention for
	// incidental telemetry (for example request.manifest). Before a choice,
	// an unknown event still makes the pending permission state uncertain.
	if msg.event.Kind == client.PendingApprovalEventOther && msg.event.Message == nil && !recovery.resolving && !recovery.resolved {
		return m.refusePendingApprovalRecovery("the pending approval watch returned an unknown event; no control was sent"), nil, true
	}
	eventCmd := m.applyPendingApprovalEventMessage(msg.event.Message, recovery)
	if msg.event.Kind == client.PendingApprovalEventTerminal {
		(&m).retirePendingApprovalRecovery()
		return m, eventCmd, true
	}
	return m, tea.Batch(eventCmd, pendingApprovalRecvCmd(recovery.generation, recovery.watch)), true
}

func (m *Model) applyPendingApprovalEventMessage(message tea.Msg, recovery *pendingApprovalRecovery) tea.Cmd {
	if message == nil {
		return nil
	}
	if call, ok := message.(client.ToolCallMsg); ok {
		if _, replay := recovery.snapshotCalls[call.ID]; replay {
			delete(recovery.snapshotCalls, call.ID)
			if m.conv.reconcileUnresolvedTool(call.ID, call.Name, call.Args) {
				return nil
			}
		}
	}
	mm, cmd := m.updateStreamEvent(message)
	*m = mm.(Model)
	return cmd
}
