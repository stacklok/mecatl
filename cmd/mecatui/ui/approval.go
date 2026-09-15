package ui

import (
	"errors"

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
func (m *Model) approvalSendCmd(askID string, verdict client.Verdict, guardrail *client.GuardrailApprovalScope, expectedRunID string) tea.Cmd {
	if authorizationStream := m.authorization.controlStream; authorizationStream != nil && m.authorization.runningControlGen == m.authorization.controlGen {
		authorizationStream.MarkApprovalResolved(askID)
		return func() tea.Msg {
			if err := authorizationStream.SendApprovalForScope(askID, verdict, guardrail, expectedRunID); err != nil {
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
		if err := stream.SendApprovalForScope(askID, verdict, guardrail, expectedRunID); err != nil {
			return client.StreamErrMsg{Err: err}
		}
		return nil
	}
}

func (m *Model) settlePendingApproval() {
	m.pendingApproval = nil
}

func (m *Model) settleApprovalOnEvent(msg tea.Msg) {
	if m.pendingApproval == nil {
		return
	}
	// A submitted approval remains correlatable until this run reaches a terminal
	// boundary. Unrelated worker asks and progress do not acknowledge it.
	if _, terminal := msg.(client.ResultMsg); terminal {
		m.settlePendingApproval()
	}
}

func (m *Model) restoreControlRefused(msg client.ControlRefusedMsg) bool {
	intent := m.pendingApproval
	if intent == nil || msg.AskID == "" || msg.AskID != intent.askID {
		return false
	}
	return m.restoreApprovalIntent(intent, errors.New(msg.Text))
}

func (m *Model) restoreRefusedApproval(err error) bool {
	intent := m.pendingApproval
	if intent == nil {
		return false
	}
	return m.restoreApprovalIntent(intent, err)
}

func (m *Model) restoreApprovalIntent(intent *approvalResolvedIntent, err error) bool {
	m.pendingApproval = nil
	m.conv.retractLatestNotice(intent.notice)
	if m.stream != nil {
		m.stream.ForgetApprovalResolved(intent.askID)
	}
	if m.authorization.controlStream != nil {
		m.authorization.controlStream.ForgetApprovalResolved(intent.askID)
	}
	s := approvalSurfaceFor(m)
	if s == nil {
		s = openApprovalSurface(m)
	} else if s.ask.AskID != "" && s.ask.AskID != intent.askID {
		s.queue = append([]pendingAsk{s.ask}, s.queue...)
	}
	delete(s.resolvedAsks, intent.askID)
	s.ask = intent.ask
	s.resumePhase = intent.resume
	m.phase = phaseAwaitingApproval
	errText := "approval reply was rejected"
	if err != nil {
		errText = oneLine(sanitizeTerminal(err.Error()))
	}
	m.statusMsg = m.deps.Theme.Style("errorText").Render("approval was refused: " + errText)
	m.refreshView()
	return true
}

// applyApprovalSurfaceIntent handles the approval intent family while keeping
// transcript, stream, and lifecycle effects Model-owned.
func (m Model) applyApprovalSurfaceIntent(intent surfaceIntent) (model tea.Model, cmd tea.Cmd, handled bool, stopSurfaceDispatch bool) {
	switch intent := intent.(type) {
	case approvalResolvedIntent:
		m.conv.addNotice(intent.notice)
		m.pendingApproval = &intent
		cmd := (&m).approvalSendCmd(intent.askID, intent.verdict, intent.guardrail, intent.expectedRunID)
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
	model, cmd := m.afterEvent()
	if msg.Guardrail != nil && m.deps.Guardrails != nil {
		detailSessionID := guardrailReviewSessionID(msg.AskID, m.sessionID)
		detailCmd := client.GetGuardrailReviewDetailCmd(m.deps.Ctx, m.deps.Guardrails, detailSessionID, msg.Guardrail.ReviewID)
		return model, tea.Batch(cmd, detailCmd)
	}
	return model, cmd
}
