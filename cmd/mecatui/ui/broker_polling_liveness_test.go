package ui

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
)

func pendingAuthorizationControlModel(control client.MCPAuthorizationController) Model {
	m := New(Deps{Ctx: context.Background(), MCPAuthorization: control})
	m.sessionID = "session-current"
	m.phase = phaseAuthorizing
	m.authorization = mcpAuthorizationState{
		authorizationID: "authorization-current",
		controlGen:      4,
		polling:         true,
		pollBusy:        true,
	}
	m.authorizationEvents = make(chan tea.Msg)
	return m
}

func applyPendingAuthorizationControlEvent(m Model) (Model, tea.Cmd) {
	mm, cmd, _ := m.updateMCPAuthorizationMsg(mcpAuthorizationEventMsg{
		sessionID: "session-current", authorizationID: "authorization-current", gen: 4,
		msg: client.MCPAuthorizationMsg{AuthorizationID: "authorization-current", Status: mcpAuthorizationStatusPending},
	})
	return mm.(Model), cmd
}

func shortenMCPAuthorizationPoll(t *testing.T) {
	t.Helper()
	old := mcpAuthorizationPollInterval
	mcpAuthorizationPollInterval = time.Millisecond
	t.Cleanup(func() { mcpAuthorizationPollInterval = old })
}

func TestMecatuiBrokerPollingLiveness_Scenario1_PendingControlEventSchedulesCurrentGenerationPoll(t *testing.T) {
	shortenMCPAuthorizationPoll(t)
	m, cmd := applyPendingAuthorizationControlEvent(pendingAuthorizationControlModel(&mcpAuthorizationControllerFake{}))
	if cmd == nil {
		t.Fatal("correlated pending control event returned no poll handoff")
	}
	msg := cmd()
	tick, ok := msg.(mcpAuthorizationPollTickMsg)
	if !ok {
		t.Fatalf("handoff delivered %T, want authorization poll tick", msg)
	}
	if tick.sessionID != m.sessionID || tick.authorizationID != m.authorization.authorizationID || tick.gen != m.authorization.controlGen || tick.gen == 4 {
		t.Fatalf("poll tick correlation = %+v, current generation = %d", tick, m.authorization.controlGen)
	}
}

func TestMecatuiBrokerPollingLiveness_Scenario1_PendingControlEventContinuesAutomatically(t *testing.T) {
	shortenMCPAuthorizationPoll(t)
	control := &mcpAuthorizationControllerFake{}
	m, handoff := applyPendingAuthorizationControlEvent(pendingAuthorizationControlModel(control))
	if handoff == nil {
		t.Fatal("correlated pending control event returned no poll handoff")
	}
	mm, controlCmd := m.Update(handoff())
	m = mm.(Model)
	if controlCmd == nil || !m.authorization.pollBusy {
		t.Fatal("executing pending handoff did not start the next recheck")
	}
	batch, ok := controlCmd().(tea.BatchMsg)
	if !ok || len(batch) == 0 {
		t.Fatalf("recheck command = %T, want batch", controlCmd())
	}
	_ = batch[0]()
	if control.recheck != 1 {
		t.Fatalf("automatic rechecks = %d, want 1", control.recheck)
	}
	currentGen := m.authorization.controlGen
	if _, staleCmd := m.applyMCPAuthorizationPollTick(mcpAuthorizationPollTickMsg{sessionID: m.sessionID, authorizationID: m.authorization.authorizationID, gen: 4}); staleCmd != nil {
		t.Fatal("previously scheduled tick started a superseded control")
	}
	for _, stale := range []tea.Msg{
		mcpAuthorizationStreamClosedMsg{sessionID: m.sessionID, authorizationID: m.authorization.authorizationID, gen: 4},
		mcpAuthorizationErrorMsg{sessionID: m.sessionID, authorizationID: m.authorization.authorizationID, gen: 4, err: errors.New("superseded")},
	} {
		mm, cmd, handled := m.updateMCPAuthorizationMsg(stale)
		m = mm.(Model)
		if !handled || cmd != nil || m.authorization.controlGen != currentGen || !m.authorization.pollBusy || m.authorization.errorText != "" {
			t.Fatalf("superseded terminal changed replacement control: state=%+v cmd=%v", m.authorization, cmd != nil)
		}
	}
}

func TestMecatuiBrokerPollingLiveness_Scenario1_PendingHandoffDoesNotRearmConverseOrDuplicateControl(t *testing.T) {
	shortenMCPAuthorizationPoll(t)
	control := &mcpAuthorizationControllerFake{}
	m := pendingAuthorizationControlModel(control)
	m.streamCh = make(chan tea.Msg)
	m.streamGen = 8
	m, handoff := applyPendingAuthorizationControlEvent(m)
	if handoff == nil {
		t.Fatal("correlated pending control event returned no poll handoff")
	}
	msg := handoff()
	if _, rearmed := msg.(streamMsg); rearmed {
		t.Fatal("pending control handoff re-armed the parked Converse reader")
	}
	tick, ok := msg.(mcpAuthorizationPollTickMsg)
	if !ok {
		t.Fatalf("handoff delivered %T, want one poll tick", msg)
	}
	mm, first := m.applyMCPAuthorizationPollTick(tick)
	m = mm.(Model)
	if first == nil {
		t.Fatal("current handoff did not start a control")
	}
	if _, duplicate := m.applyMCPAuthorizationPollTick(mcpAuthorizationPollTickMsg{sessionID: m.sessionID, authorizationID: m.authorization.authorizationID, gen: m.authorization.controlGen}); duplicate != nil {
		t.Fatal("pending handoff created two simultaneous authorization controls")
	}
	if m.authorizationEvents != nil {
		t.Fatal("superseded control reader remained attached")
	}
	m.authorizationEvents = make(chan tea.Msg)
	m.authorization.controlStream = client.NewAuthorizationEventStream(client.NewFakeEventStream(), &authorizationControlRecorder{})
	m = applyAll(m, mcpAuthorizationEventMsg{
		sessionID: m.sessionID, authorizationID: m.authorization.authorizationID, gen: m.authorization.controlGen,
		msg: client.MCPAuthorizationMsg{AuthorizationID: m.authorization.authorizationID, Status: "granted"},
	})
	if m.phase != phaseRunning || m.authorization.runningControlGen != m.authorization.controlGen || m.authorization.controlStream == nil {
		t.Fatalf("granted continuation lost control-stream ownership: phase=%v state=%+v", m.phase, m.authorization)
	}
}

// deadlineEnrollmentController blocks cooperatively until the command context ends.
// transition records that the server may have committed before withholding its reply.
type deadlineEnrollmentController struct {
	mu               sync.Mutex
	calls            []string
	recordTransition bool
	transitioned     bool
}

func (f *deadlineEnrollmentController) wait(ctx context.Context, action string) (client.WorkspaceEnrollment, error) {
	f.mu.Lock()
	f.calls = append(f.calls, action)
	if f.recordTransition {
		f.transitioned = true
	}
	f.mu.Unlock()
	<-ctx.Done()
	if f.recordTransition {
		return client.WorkspaceEnrollment{ID: "enrollment-current", Status: client.WorkspaceEnrollmentConnected}, nil
	}
	return client.WorkspaceEnrollment{}, ctx.Err()
}

func (f *deadlineEnrollmentController) ConnectWorkspaceServices(ctx context.Context, _ string) (client.WorkspaceEnrollment, error) {
	return f.wait(ctx, "connect")
}

func (f *deadlineEnrollmentController) RetryWorkspaceEnrollment(ctx context.Context, _, _ string) (client.WorkspaceEnrollment, error) {
	return f.wait(ctx, "retry")
}

func (f *deadlineEnrollmentController) CancelWorkspaceEnrollment(ctx context.Context, _, _ string) (client.WorkspaceEnrollment, error) {
	return f.wait(ctx, "cancel")
}

func (f *deadlineEnrollmentController) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *deadlineEnrollmentController) didTransition() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.transitioned
}

func currentPendingEnrollmentModel(ctx context.Context, control client.WorkspaceEnrollmentController) Model {
	m := New(Deps{Ctx: ctx, WorkspaceEnrollment: control})
	m.sessionID = "session-current"
	m.enrollment = workspaceEnrollmentState{
		ID:                    "enrollment-current",
		Status:                client.WorkspaceEnrollmentPending,
		controlGen:            7,
		presentationDelivered: true,
	}
	return m
}

func TestMecatuiBrokerPollingLiveness_Scenario1_AllWorkspaceEnrollmentActionsAreDeadlineBounded(t *testing.T) {
	if workspaceEnrollmentTimeout != 30*time.Second {
		t.Fatalf("production workspace enrollment deadline = %v, want 30s", workspaceEnrollmentTimeout)
	}
	old := workspaceEnrollmentTimeout
	workspaceEnrollmentTimeout = 20 * time.Millisecond
	t.Cleanup(func() { workspaceEnrollmentTimeout = old })

	for _, action := range []string{connectAction, "check", "retry", "cancel"} {
		t.Run(action, func(t *testing.T) {
			control := &deadlineEnrollmentController{}
			m := currentPendingEnrollmentModel(t.Context(), control)
			if action == connectAction {
				m.enrollment = workspaceEnrollmentState{}
			}
			m.enrollment.busy = true
			mm, cmd := m.startWorkspaceEnrollmentControl(action)
			m = mm.(Model)
			started := time.Now()
			result := cmd()
			if elapsed := time.Since(started); elapsed > time.Second {
				t.Fatalf("%s command exceeded fixed test bound: %v", action, elapsed)
			}
			m = applyAll(m, result)
			if m.enrollment.busy {
				t.Fatalf("%s timeout left enrollment busy", action)
			}
			if control.callCount() != 1 {
				t.Fatalf("%s calls = %d, want 1", action, control.callCount())
			}
		})
	}
}

func TestMecatuiBrokerPollingLiveness_Scenario1_AmbiguousTimeoutFailsClosed(t *testing.T) {
	old := workspaceEnrollmentTimeout
	workspaceEnrollmentTimeout = 20 * time.Millisecond
	t.Cleanup(func() { workspaceEnrollmentTimeout = old })

	control := &deadlineEnrollmentController{recordTransition: true}
	m := currentPendingEnrollmentModel(t.Context(), control)
	m.enrollment.busy = true
	mm, cmd := m.startWorkspaceEnrollmentControl("check")
	m = mm.(Model)
	m = applyAll(m, cmd())
	if !control.didTransition() {
		t.Fatal("fake did not record the server-side transition before withholding its response")
	}
	if m.enrollment.busy || m.enrollment.presentationDelivered {
		t.Fatalf("ambiguous timeout remained active: %+v", m.enrollment)
	}
	if m.enrollment.ID != "enrollment-current" || m.enrollment.Status != client.WorkspaceEnrollmentPending || m.enrollment.controlGen != 8 {
		t.Fatalf("ambiguous timeout lost correlation or fabricated state: %+v", m.enrollment)
	}
	message := stripANSIstr(m.workspaceEnrollmentNotice)
	if !strings.Contains(message, "outcome may be uncertain") {
		t.Fatalf("timeout notice = %q, want uncertain outcome", message)
	}
	for _, fabricated := range []string{"connected", "failed", "declined", "expired"} {
		if strings.Contains(message, fabricated) {
			t.Fatalf("timeout notice fabricated %q outcome: %q", fabricated, message)
		}
	}
}

func TestMecatuiBrokerPollingLiveness_Scenario1_TimeoutRequiresFreshSessionRecovery(t *testing.T) {
	old := workspaceEnrollmentTimeout
	workspaceEnrollmentTimeout = 20 * time.Millisecond
	t.Cleanup(func() { workspaceEnrollmentTimeout = old })

	for _, action := range []string{connectAction, "check", "retry", "cancel"} {
		t.Run(action, func(t *testing.T) {
			control := &deadlineEnrollmentController{}
			m := currentPendingEnrollmentModel(t.Context(), control)
			if action == connectAction {
				m.enrollment = workspaceEnrollmentState{}
			}
			m.enrollment.busy = true
			mm, cmd := m.startWorkspaceEnrollmentControl(action)
			m = mm.(Model)
			m = applyAll(m, cmd())
			time.Sleep(2 * workspaceEnrollmentTimeout)
			if control.callCount() != 1 {
				t.Fatalf("%s timeout made %d calls, want no automatic follow-up", action, control.callCount())
			}
			message := stripANSIstr(m.workspaceEnrollmentNotice)
			if !strings.Contains(message, "/clear") || !strings.Contains(message, "/tools-connect") || !strings.Contains(message, "replacement session") {
				t.Fatalf("%s timeout notice is not actionable: %q", action, message)
			}
		})
	}
}

func TestMecatuiBrokerPollingLiveness_Scenario1_CancellationAndLateResultStayInert(t *testing.T) {
	old := workspaceEnrollmentTimeout
	workspaceEnrollmentTimeout = 20 * time.Millisecond
	t.Cleanup(func() { workspaceEnrollmentTimeout = old })

	parent, cancel := context.WithCancel(context.Background())
	cancel()
	m := currentPendingEnrollmentModel(parent, &deadlineEnrollmentController{})
	m.enrollment.busy = true
	mm, cmd := m.startWorkspaceEnrollmentControl("check")
	m = mm.(Model)
	m = applyAll(m, cmd())
	if strings.Contains(strings.ToLower(m.workspaceEnrollmentNotice), "timeout") || strings.Contains(m.workspaceEnrollmentNotice, "uncertain") {
		t.Fatalf("parent cancellation was rendered as timeout: %q", m.workspaceEnrollmentNotice)
	}

	opened, submitted := 0, 0
	m.deps.OpenURL = func(context.Context, string) error { opened++; return nil }
	m.enrollment = workspaceEnrollmentState{ID: "enrollment-replacement", Status: client.WorkspaceEnrollmentPending, controlGen: 20}
	m.pendingInitialPrompt = "retained prompt"
	lateMessages := []workspaceEnrollmentMsg{
		{
			action: "retry", sessionID: "session-current", targetEnrollmentID: "enrollment-current", gen: 8,
			result: client.WorkspaceEnrollment{ID: "enrollment-current", Status: client.WorkspaceEnrollmentPending, PresentationURL: "https://should-not-open.example"},
		},
		{
			action: "check", sessionID: "session-current", targetEnrollmentID: "enrollment-current", gen: 8,
			result: client.WorkspaceEnrollment{ID: "enrollment-current", Status: client.WorkspaceEnrollmentConnected},
		},
	}
	for _, late := range lateMessages {
		mm, lateCmd := m.applyWorkspaceEnrollment(late)
		m = mm.(Model)
		if lateCmd != nil {
			_ = lateCmd()
			submitted++
		}
	}
	m.sessionID = "session-replacement"
	mm, lateCmd := m.applyWorkspaceEnrollment(lateMessages[1])
	m = mm.(Model)
	if lateCmd != nil {
		_ = lateCmd()
		submitted++
	}
	if opened != 0 || submitted != 0 || m.enrollment.ID != "enrollment-replacement" || m.enrollment.controlGen != 20 || m.enrollment.presentationDelivered {
		t.Fatalf("late result mutated replacement: opened=%d submitted=%d state=%+v", opened, submitted, m.enrollment)
	}
	if _, poll := m.applyWorkspaceEnrollmentPollTick(workspaceEnrollmentPollTickMsg{sessionID: "session-current", enrollmentID: "enrollment-current", gen: 8}); poll != nil {
		t.Fatal("expired attempt armed replacement poller")
	}
}
