package ui

import (
	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
)

// approvalFooterProjection is the narrow approval state footer needs. It is
// derived once per footer frame from the active surface.
type approvalFooterProjection struct {
	plan        bool
	queued      int
	offerAlways bool
}

func approvalFooterProjectionFor(s *approvalSurface) approvalFooterProjection {
	if s == nil {
		return approvalFooterProjection{}
	}
	return approvalFooterProjection{plan: isPlanAsk(s.ask.Tool), queued: len(s.queue), offerAlways: s.ask.offerAlways}
}

// openApprovalSurface is the sole approval-surface constructor. PermissionAskMsg
// reduction is the only normal caller; all other approval paths require this
// already-open surface and therefore cannot silently recreate one after close.
func openApprovalSurface(m *Model) *approvalSurface {
	if s := approvalSurfaceFor(m); s != nil {
		return s
	}
	s := &approvalSurface{
		deps:         m.surfaceDeps(),
		sessionID:    m.sessionID,
		modelID:      m.resolvedSessionModel.ModelID,
		debugSession: m.deps.DebugTarget != "",
		render:       newApprovalRender(m.rend),
		expandTools:  m.expandTools,
	}
	m.modal = s
	return s
}

func approvalSurfaceFor(m *Model) *approvalSurface {
	s, _ := m.modal.(*approvalSurface)
	return s
}

// approvalSendCmd is the one approval transport command. The Model captures the
// current stream before returning the command, preserving the existing resolved
// correlation and nil-stream behavior without exposing transport to the surface.
func (m *Model) approvalSendCmd(askID string, verdict client.Verdict) tea.Cmd {
	if authorizationStream := m.authorization.controlStream; authorizationStream != nil && m.authorization.runningControlGen == m.authorization.controlGen {
		authorizationStream.MarkApprovalResolved(askID)
		return func() tea.Msg {
			if err := authorizationStream.SendApproval(askID, verdict); err != nil {
				return client.StreamErrMsg{Err: err}
			}
			return nil
		}
	}
	stream := m.stream
	if stream == nil {
		return nil
	}
	stream.MarkApprovalResolved(askID)
	return func() tea.Msg {
		if err := stream.SendApproval(askID, verdict); err != nil {
			return client.StreamErrMsg{Err: err}
		}
		return nil
	}
}

// applyApprovalSurfaceIntent handles the approval intent family while keeping
// transcript, stream, and lifecycle effects Model-owned.
func (m Model) applyApprovalSurfaceIntent(intent surfaceIntent) (model tea.Model, cmd tea.Cmd, handled bool, stopSurfaceDispatch bool) {
	switch intent := intent.(type) {
	case approvalResolvedIntent:
		m.conv.addNotice(intent.notice)
		cmd := (&m).approvalSendCmd(intent.askID, intent.verdict)
		model, cmd, stopSurfaceDispatch = m.finishApprovalIntent(intent.advance, intent.resume, cmd)
		return model, cmd, true, stopSurfaceDispatch
	case approvalRetractedIntent:
		m.conv.addNotice(intent.notice)
		model, cmd, stopSurfaceDispatch = m.finishApprovalIntent(intent.advance, intent.resume, nil)
		return model, cmd, true, stopSurfaceDispatch
	case setExpandToolsIntent:
		m.expandTools = intent.expand
		return m, nil, true, false
	default:
		return m, nil, false, false
	}
}

func (m Model) finishApprovalIntent(advance approvalAdvance, resume phase, cmd tea.Cmd) (tea.Model, tea.Cmd, bool) {
	m.refreshView()
	switch advance.outcome {
	case approvalQueueUnchanged:
		return m, cmd, false
	case approvalQueueSuccessor:
		// The successor can change a fill plan/args surface into a centered card.
		// Its next frame must establish fresh hit bounds.
		m.hits.clear()
		m.metrics.clear()
		return m, cmd, false
	case approvalQueueDrained:
		m.phase = resume
		m.closeModal()
		m.prompt.Focus()
		if resume == phaseRunning {
			return m, tea.Batch(cmd, m.sp.Tick), true
		}
		return m, cmd, true
	default:
		return m, cmd, false
	}
}

// applyPermissionAsk reduces a PermissionAskMsg. The surface owns its FIFO and
// ask state; Model owns the interrupted phase and visible run chrome.
func (m Model) applyPermissionAsk(msg client.PermissionAskMsg) (tea.Model, tea.Cmd) {
	if m.authorization.controlStream != nil && m.authorization.controlStream.ApprovalResolved(msg.AskID) {
		return m.afterEvent()
	}
	if m.stream != nil && m.stream.ApprovalResolved(msg.AskID) {
		return m.afterEvent()
	}
	s := openApprovalSurface(&m)
	open := s.ask.AskID != ""
	opening := s.applyPermissionAsk(msg, open, m.phase)
	if opening {
		m.phase = phaseAwaitingApproval
		m.activeTool = ""
		m.toolProgress = ""
	}
	return m.afterEvent()
}
