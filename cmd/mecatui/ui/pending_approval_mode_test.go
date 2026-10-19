package ui

import (
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
	"github.com/stacklok/mecatl/cmd/mecatui/ui/internal/scrollback"
)

var shiftTab = tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}

// refusedModeCaps is a server whose posture no longer covers a stored yolo
// session and that advertises the refused-mode recovery when recovery is set.
func refusedModeCaps(recovery bool) client.Capabilities {
	return client.Capabilities{Posture: postureTrusted, SessionPermissionModes: true, RefusedModeRecovery: recovery}
}

// pendingModeRecoveryModel is pendingRecoveryModel for a stored session in
// mode whose transcript ends on the unanswered gated call w1, with conv as the
// SetMode transport.
func pendingModeRecoveryModel(t *testing.T, controller *pendingApprovalFixtureController, conv *fakeConv, caps client.Capabilities, mode, prompt string) Model {
	t.Helper()
	approval := client.PendingApproval{SessionID: "session", RunID: "run", AskID: "ask", Tool: "Write", Args: `{"path":"x"}`, Reason: "write?", Cursor: "cursor"}
	transcript := client.SessionTranscript{SessionID: "session", Complete: true, Kind: client.SessionKindMain, Messages: []client.ConversationMessage{
		{Role: "user", Text: "go"},
		{Role: "assistant", ToolCalls: []client.ConvToolCall{{ID: "w1", Name: "Write", Args: `{"path":"x"}`}}},
	}}
	m := newTestModelFromDeps(Deps{
		Theme: theme.New("aztec", theme.AztecPalette()), Ctx: t.Context(), Session: conv,
		Resume: &client.ResumeSelection{
			Row:        client.SessionListItem{ID: "session", Kind: client.SessionKindMain},
			Transcript: transcript,
			Snapshot:   client.SessionSnapshot{State: "awaiting", Mode: mode, Capabilities: caps}, Pending: &approval,
		},
		PendingApprovals: controller, InitialPrompt: prompt, NoAltScreen: true,
	})
	m = applyAll(m, tea.WindowSizeMsg{Width: 160, Height: 30})
	next, _ := m.Update(m.openPendingApprovalWatchCmd()())
	m = next.(Model)
	approvalSurfaceOf(t, m)
	return m
}

// modeChangedFrom executes cmd's leaves and returns the ModeChangedMsg it
// produced, failing unless there is exactly one.
func modeChangedFrom(t *testing.T, cmd tea.Cmd) client.ModeChangedMsg {
	t.Helper()
	var found []client.ModeChangedMsg
	var walk func(tea.Cmd)
	walk = func(c tea.Cmd) {
		if c == nil {
			return
		}
		switch msg := c().(type) {
		case tea.BatchMsg:
			for _, child := range msg {
				walk(child)
			}
		case client.ModeChangedMsg:
			found = append(found, msg)
		}
	}
	walk(cmd)
	if len(found) != 1 {
		t.Fatalf("command produced %d ModeChangedMsg, want 1", len(found))
	}
	return found[0]
}

// flatView is the rendered view with ANSI stripped and every whitespace run
// collapsed, so assertions survive the footer wrapping a long notice.
func flatView(m Model) string {
	return strings.Join(strings.Fields(stripANSIstr(m.View().Content)), " ")
}

func toolCard(t *testing.T, m Model, id string) scrollback.ToolCardSnapshot {
	t.Helper()
	for i := 0; i < m.conv.scrollback.Len(); i++ {
		if card, ok := m.conv.scrollback.SnapshotAt(i).Payload.(scrollback.ToolCardSnapshot); ok && card.Call.ID == id {
			return card
		}
	}
	t.Fatalf("no tool card %q", id)
	return scrollback.ToolCardSnapshot{}
}

func newRecoveryFixtures(state string) (*pendingApprovalFixtureController, *fakeConv) {
	return &pendingApprovalFixtureController{watch: &pendingApprovalFixtureWatch{}},
		&fakeConv{recv: &fakeRecver{}, send: &fakeSender{}, mode: "yolo", setModeState: state}
}

// TestPendingApprovalRecoveryModeSwitchCancelsWithoutVerdict pins the TUI path
// to the refused-mode recovery: ModeSwitch in the recovered approval sends
// SetMode to the next lower mode and, on the server's cancelled answer, closes
// the modal, marks the gated call interrupted, and returns to idle with the
// draft kept and no verdict or cancel sent.
func TestPendingApprovalRecoveryModeSwitchCancelsWithoutVerdict(t *testing.T) {
	controller, conv := newRecoveryFixtures("cancelled")
	m := pendingModeRecoveryModel(t, controller, conv, refusedModeCaps(true), "yolo", "keep this draft")
	if !strings.Contains(flatView(m), "approving will be refused") {
		t.Fatalf("recovery entry footer lacks the refused-mode hint:\n%s", flatView(m))
	}

	next, cmd := m.Update(shiftTab)
	m = next.(Model)
	changed := modeChangedFrom(t, cmd)
	if got := conv.setModes(); !reflect.DeepEqual(got, []string{"default"}) {
		t.Fatalf("SetMode calls = %v, want [default]", got)
	}
	if m.pendingMode != "" {
		t.Fatalf("recovery mode change armed the deferred pendingMode %q", m.pendingMode)
	}
	next, cmd = m.Update(changed)
	m = next.(Model)
	runBatchLeaves(cmd)

	if m.modal != nil || m.phase != phaseIdle || m.pendingRecovery != nil || !controller.watch.isClosed() {
		t.Fatalf("after recovery: modal=%T phase=%v recovery=%v watch closed=%t", m.modal, m.phase, m.pendingRecovery != nil, controller.watch.isClosed())
	}
	if resolves, cancels, _ := controller.counts(); resolves != 0 || cancels != 0 {
		t.Fatalf("recovery sent controls: resolve %d cancel %d", resolves, cancels)
	}
	if m.prompt.Value() != "keep this draft" || m.activeMode != "default" || m.sessionState != "cancelled" {
		t.Fatalf("draft %q mode %q state %q, want draft kept, default, cancelled", m.prompt.Value(), m.activeMode, m.sessionState)
	}
	if card := toolCard(t, m, "w1"); !card.Resolved || !card.Result.IsError || card.Result.Body != interruptedToolText {
		t.Fatalf("gated call card = %+v, want the interrupted close-out", card)
	}
	if !strings.Contains(stripANSIstr(m.statusMsg), "cancelled without running") {
		t.Fatalf("status = %q, want the cancellation outcome", stripANSIstr(m.statusMsg))
	}
}

// TestPendingApprovalRecoveryModeSwitchNeedsFeatureAndOpenAsk pins that
// ModeSwitch does nothing in the approval without the server feature or once
// a verdict is in flight.
func TestPendingApprovalRecoveryModeSwitchNeedsFeatureAndOpenAsk(t *testing.T) {
	t.Run("no feature", func(t *testing.T) {
		controller, conv := newRecoveryFixtures("cancelled")
		m := pendingModeRecoveryModel(t, controller, conv, refusedModeCaps(false), "yolo", "")
		t.Cleanup(m.pendingRecovery.cancel)
		if strings.Contains(flatView(m), "approving will be refused") {
			t.Fatal("hint offered a recovery the server does not advertise")
		}
		next, cmd := m.Update(shiftTab)
		m = next.(Model)
		runBatchLeaves(cmd)
		if got := conv.setModes(); len(got) != 0 || m.recoveryModeSwitch != "" {
			t.Fatalf("SetMode calls = %v, in flight %q; want none", got, m.recoveryModeSwitch)
		}
		approvalSurfaceOf(t, m)
	})
	t.Run("verdict in flight", func(t *testing.T) {
		controller, conv := newRecoveryFixtures("cancelled")
		m := pendingModeRecoveryModel(t, controller, conv, refusedModeCaps(true), "yolo", "")
		t.Cleanup(m.pendingRecovery.cancel)
		next, _ := m.Update(tea.KeyPressMsg{Code: 'a', Text: "a"})
		m = next.(Model)
		// After a verdict the modal is gone and ModeSwitch is the ordinary
		// running-phase cycle, never the recovery.
		next, _ = m.Update(shiftTab)
		m = next.(Model)
		if m.recoveryModeSwitch != "" {
			t.Fatalf("recovery mode change %q started after a verdict", m.recoveryModeSwitch)
		}
	})
}

// TestPendingApprovalRecoveryModeRefusalKeepsApproval pins that a refused
// recovery mode change is final: the modal and recovery stay, the server's
// reason shows, nothing is deferred, and the next press tries a lower mode.
func TestPendingApprovalRecoveryModeRefusalKeepsApproval(t *testing.T) {
	controller, conv := newRecoveryFixtures("cancelled")
	conv.setModeErr = status.Error(codes.InvalidArgument, `mode "default" exceeds the server ceiling "plan"`)
	m := pendingModeRecoveryModel(t, controller, conv, refusedModeCaps(true), "yolo", "draft")
	t.Cleanup(m.pendingRecovery.cancel)

	next, cmd := m.Update(shiftTab)
	m = next.(Model)
	next, _ = m.Update(modeChangedFrom(t, cmd))
	m = next.(Model)
	approvalSurfaceOf(t, m)
	if m.phase != phaseAwaitingApproval || m.pendingRecovery == nil || m.pendingMode != "" || m.recoveryModeSwitch != "" || m.activeMode != "yolo" {
		t.Fatalf("refusal changed state: phase %v recovery %t pendingMode %q inflight %q mode %q", m.phase, m.pendingRecovery != nil, m.pendingMode, m.recoveryModeSwitch, m.activeMode)
	}
	if footer := flatView(m); !strings.Contains(footer, `exceeds the server ceiling "plan"`) {
		t.Fatalf("footer lacks the server's reason:\n%s", footer)
	}

	next, cmd = m.Update(shiftTab)
	m = next.(Model)
	modeChangedFrom(t, cmd)
	if got := conv.setModes(); !reflect.DeepEqual(got, []string{"default", "plan"}) {
		t.Fatalf("SetMode calls = %v, want [default plan]", got)
	}
	if resolves, cancels, _ := controller.counts(); resolves != 0 || cancels != 0 {
		t.Fatalf("refusal sent controls: resolve %d cancel %d", resolves, cancels)
	}
}

// TestPendingApprovalRecoveryModeSwitchBlocksVerdicts pins that a verdict
// cannot race the in-flight recovery mode change.
func TestPendingApprovalRecoveryModeSwitchBlocksVerdicts(t *testing.T) {
	controller, conv := newRecoveryFixtures("cancelled")
	m := pendingModeRecoveryModel(t, controller, conv, refusedModeCaps(true), "yolo", "")
	t.Cleanup(m.pendingRecovery.cancel)
	next, _ := m.Update(shiftTab)
	m = next.(Model)
	for _, k := range []tea.KeyPressMsg{{Code: 'a', Text: "a"}, {Code: 'd', Text: "d"}, {Code: tea.KeyEnter}} {
		next, cmd := m.Update(k)
		m = applyPendingCommand(t, next.(Model), cmd)
	}
	if resolves, _, _ := controller.counts(); resolves != 0 {
		t.Fatalf("verdict sent during the recovery mode change: %d", resolves)
	}
	approvalSurfaceOf(t, m)
}

// TestPendingApprovalRecoveryModeSwitchToleratesEitherOrder pins that the
// recovery's own close-out on the watch (retract, tool result, cancelled
// terminal) is the expected outcome whether it lands before or after the
// SetMode response, never a fatal recovery failure.
func TestPendingApprovalRecoveryModeSwitchToleratesEitherOrder(t *testing.T) {
	closeOut := []client.PendingApprovalEvent{
		{Kind: client.PendingApprovalEventRetracted, Message: client.PermissionRetractMsg{AskID: "ask"}},
		{Kind: client.PendingApprovalEventOther, Message: client.ToolResultMsg{CallID: "w1", Content: interruptedToolText, IsError: true}},
		{Kind: client.PendingApprovalEventTerminal, Message: client.ResultMsg{Stop: "cancelled"}},
	}
	for _, order := range []string{"watch first", "response first"} {
		t.Run(order, func(t *testing.T) {
			controller, conv := newRecoveryFixtures("cancelled")
			m := pendingModeRecoveryModel(t, controller, conv, refusedModeCaps(true), "yolo", "draft")
			generation := m.pendingRecovery.generation
			t.Cleanup(controller.watch.Close)
			next, cmd := m.Update(shiftTab)
			m = next.(Model)
			changed := modeChangedFrom(t, cmd)
			deliverWatch := func() {
				for _, ev := range closeOut {
					next, _ := m.Update(pendingApprovalWatchMsg{generation: generation, event: ev})
					m = next.(Model)
					if m.phase == phaseFatal {
						t.Fatalf("%s: event %s made recovery fatal: %s", order, ev.Kind, stripANSIstr(m.statusMsg))
					}
				}
			}
			if order == "watch first" {
				deliverWatch()
			}
			next, _ = m.Update(changed)
			m = next.(Model)
			if order == "response first" {
				deliverWatch()
			}
			if m.phase != phaseIdle || m.modal != nil || m.pendingRecovery != nil || m.recoveryModeSwitch != "" || m.activeMode != "default" {
				t.Fatalf("%s: phase %v modal %T recovery %t inflight %q mode %q", order, m.phase, m.modal, m.pendingRecovery != nil, m.recoveryModeSwitch, m.activeMode)
			}
			if card := toolCard(t, m, "w1"); !card.Resolved || card.Result.Body != interruptedToolText {
				t.Fatalf("%s: gated call card = %+v", order, card)
			}
			if m.prompt.Value() != "draft" {
				t.Fatalf("%s: draft = %q", order, m.prompt.Value())
			}
		})
	}
}

// TestPendingApprovalRecoveryRetractWithoutModeSwitchStaysFatal pins that the
// tolerance is scoped to the user's own recovery: a retract the user did not
// cause still ends recovery.
func TestPendingApprovalRecoveryRetractWithoutModeSwitchStaysFatal(t *testing.T) {
	controller, conv := newRecoveryFixtures("cancelled")
	m := pendingModeRecoveryModel(t, controller, conv, refusedModeCaps(true), "yolo", "")
	next, _ := m.Update(pendingApprovalWatchMsg{generation: m.pendingRecovery.generation, event: client.PendingApprovalEvent{
		Kind: client.PendingApprovalEventRetracted, Message: client.PermissionRetractMsg{AskID: "ask"},
	}})
	if m = next.(Model); m.phase != phaseFatal {
		t.Fatalf("unrequested retract phase = %v, want fatal", m.phase)
	}
}

// TestPendingApprovalRecoveryRefusedVerdictNamesRecovery pins the
// FailedPrecondition verdict refusal: with the feature the modal reopens with
// the server's reason and verdicts blocked, so ModeSwitch can recover;
// without it recovery ends naming the server's reason, not the generic text.
func TestPendingApprovalRecoveryRefusedVerdictNamesRecovery(t *testing.T) {
	reason := `session "s" is in permission mode "yolo", which this server does not admit`
	t.Run("with feature", func(t *testing.T) {
		controller, conv := newRecoveryFixtures("cancelled")
		controller.resolveErr = client.PendingApprovalNotRecoverableError(reason)
		m := pendingModeRecoveryModel(t, controller, conv, refusedModeCaps(true), "yolo", "")
		t.Cleanup(m.pendingRecovery.cancel)
		next, cmd := m.Update(tea.KeyPressMsg{Code: 'a', Text: "a"})
		m = applyPendingCommand(t, next.(Model), cmd)
		approvalSurfaceOf(t, m)
		if m.phase != phaseAwaitingApproval || m.pendingRecovery == nil || !m.pendingRecovery.verdictRefused {
			t.Fatalf("refused verdict: phase %v recovery %t", m.phase, m.pendingRecovery != nil)
		}
		footer := flatView(m)
		if !strings.Contains(footer, "does not admit") || !strings.Contains(footer, "lower mode") {
			t.Fatalf("footer does not name the server reason and the recovery:\n%s", footer)
		}
		next, cmd = m.Update(tea.KeyPressMsg{Code: 'a', Text: "a"})
		m = applyPendingCommand(t, next.(Model), cmd)
		if resolves, _, _ := controller.counts(); resolves != 1 {
			t.Fatalf("verdict resent after refusal: %d resolves", resolves)
		}
		next, cmd = m.Update(shiftTab)
		m = next.(Model)
		modeChangedFrom(t, cmd)
		if got := conv.setModes(); !reflect.DeepEqual(got, []string{"default"}) {
			t.Fatalf("SetMode calls = %v, want [default]", got)
		}
	})
	t.Run("without feature", func(t *testing.T) {
		controller, conv := newRecoveryFixtures("cancelled")
		controller.resolveErr = client.PendingApprovalNotRecoverableError(reason)
		m := pendingModeRecoveryModel(t, controller, conv, refusedModeCaps(false), "yolo", "")
		next, cmd := m.Update(tea.KeyPressMsg{Code: 'a', Text: "a"})
		m = applyPendingCommand(t, next.(Model), cmd)
		status := stripANSIstr(m.statusMsg)
		if m.phase != phaseFatal || !strings.Contains(status, "does not admit") || strings.Contains(status, "could not be acknowledged") {
			t.Fatalf("refused verdict without feature: phase %v status %q", m.phase, status)
		}
	})
}
