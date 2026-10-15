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

func modeTestModel(t *testing.T, conv *fakeConv) Model {
	t.Helper()
	m := newTestModelFromDeps(Deps{
		Session: conv,
		Conv:    conv,
		Theme:   theme.New("aztec", theme.AztecPalette()),
		Mode:    "default",
		Ctx:     context.Background(),
	})
	return applyAll(m,
		tea.WindowSizeMsg{Width: 120, Height: 30},
		client.SessionReadyMsg{SessionID: "sess-test-0001", Mode: "default"},
	)
}

func modeKey() tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}
}

func altModeKey() tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: 'm', Mod: tea.ModAlt}
}

func TestModeSwitchUpdatesServerAndHeader(t *testing.T) {
	conv := &fakeConv{recv: &fakeRecver{}, send: &fakeSender{}, mode: "default"}
	m := modeTestModel(t, conv)

	m.prompt.Rewrite("keep this draft")
	mm, cmd := m.Update(modeKey())
	m = mm.(Model)
	if got := m.prompt.Value(); got != "keep this draft" {
		t.Fatalf("shift+tab changed prompt text to %q", got)
	}
	if cmd == nil {
		t.Fatal("shift+tab should issue SetMode command")
	}
	m = applyAll(m, cmd())

	if got := conv.setModes(); len(got) != 1 || got[0] != "plan" {
		t.Fatalf("SetMode calls = %v, want [plan]", got)
	}
	if m.activeMode != "plan" || m.pendingMode != "" {
		t.Fatalf("active/pending mode = %q/%q, want plan/empty", m.activeMode, m.pendingMode)
	}
	if mode := m.statusLineSnapshot().Session.Mode; mode != "plan" {
		t.Fatalf("status snapshot mode = %q, want plan", mode)
	}
}

func TestModeSwitchOverrideRemainsLive(t *testing.T) {
	conv := &fakeConv{recv: &fakeRecver{}, send: &fakeSender{}, mode: "default"}
	m := modeTestModel(t, conv)
	m.keys = applyKeyOverrides(m.keys, map[string][]string{"ModeSwitch": {"alt+m"}})

	m.prompt.Rewrite("keep this draft")
	mm, cmd := m.Update(modeKey())
	m = mm.(Model)
	if cmd != nil {
		t.Fatal("shift+tab must not switch mode after ModeSwitch is overridden")
	}
	if got := conv.setModes(); len(got) != 0 {
		t.Fatalf("shift+tab SetMode calls = %v, want none", got)
	}
	if got := m.prompt.Value(); got != "keep this draft" {
		t.Fatalf("shift+tab changed prompt text to %q", got)
	}

	mm, cmd = m.Update(altModeKey())
	m = mm.(Model)
	if cmd == nil {
		t.Fatal("overridden alt+m should issue SetMode command")
	}
	m = applyAll(m, cmd())
	if got := conv.setModes(); len(got) != 1 || got[0] != "plan" {
		t.Fatalf("SetMode calls = %v, want [plan]", got)
	}
}

func TestModeSwitchFailureDefersToNextPrompt(t *testing.T) {
	conv := &fakeConv{recv: &fakeRecver{}, send: &fakeSender{}, mode: "default", setModeErr: errors.New("illegal transition")}
	m := modeTestModel(t, conv)
	m.phase = phaseRunning

	mm, cmd := m.Update(modeKey())
	m = mm.(Model)
	if cmd == nil {
		t.Fatal("shift+tab while running should attempt SetMode")
	}
	m = applyAll(m, cmd())

	if m.activeMode != "default" || m.pendingMode != "plan" {
		t.Fatalf("active/pending mode = %q/%q, want default/plan", m.activeMode, m.pendingMode)
	}
	if !strings.Contains(stripANSIstr(m.statusMsg), "will apply on the next prompt") {
		t.Fatalf("status = %q, want deferred notice", stripANSIstr(m.statusMsg))
	}
	if mode := m.statusLineSnapshot().Session.Mode; mode != "plan pending" {
		t.Fatalf("status snapshot mode = %q, want plan pending", mode)
	}

	conv.setModeErr = nil
	m.phase = phaseIdle
	cmd = m.retryPendingModeCmd()
	if cmd == nil {
		t.Fatal("pending mode should retry at turn boundary")
	}
	m = applyAll(m, cmd())
	if m.activeMode != "plan" || m.pendingMode != "" {
		t.Fatalf("active/pending mode after retry = %q/%q, want plan/empty", m.activeMode, m.pendingMode)
	}
}

// TestModeRefusedAtIdleClearsPending pins that a SetMode error with no turn in
// flight is the server's final refusal: the header must not stay "pending"
// and the status names the server's reason.
func TestModeRefusedAtIdleClearsPending(t *testing.T) {
	conv := &fakeConv{recv: &fakeRecver{}, send: &fakeSender{}, mode: "default", setModeErr: errors.New("set mode: mode \"plan\" exceeds the server ceiling")}
	m := modeTestModel(t, conv)

	mm, cmd := m.Update(modeKey())
	m = mm.(Model)
	if cmd == nil {
		t.Fatal("shift+tab should issue SetMode command")
	}
	m = applyAll(m, cmd())

	if m.activeMode != "default" || m.pendingMode != "" {
		t.Fatalf("active/pending mode = %q/%q, want default/empty", m.activeMode, m.pendingMode)
	}
	status := stripANSIstr(m.statusMsg)
	if !strings.Contains(status, "mode plan refused") || !strings.Contains(status, "exceeds the server ceiling") {
		t.Fatalf("status = %q, want refusal naming the server reason", status)
	}
	if mode := m.statusLineSnapshot().Session.Mode; mode != "default" {
		t.Fatalf("status snapshot mode = %q, want default", mode)
	}
	if m.retryPendingModeCmd() != nil {
		t.Fatal("a refused mode must not be retried at the next turn boundary")
	}
}

// TestRefusedModeDoesNotTrapCycle pins that after the server refuses a mode
// the next ModeSwitch continues past it instead of re-requesting it forever:
// on a yolo server an auto refusal still lets the cycle reach yolo.
func TestRefusedModeDoesNotTrapCycle(t *testing.T) {
	conv := &fakeConv{recv: &fakeRecver{}, send: &fakeSender{}, mode: "accept-edits"}
	m := newTestModelFromDeps(Deps{Session: conv, Conv: conv, Theme: theme.New("aztec", theme.AztecPalette()), Mode: "accept-edits", Ctx: context.Background(), Embedded: true})
	m = applyAll(m, tea.WindowSizeMsg{Width: 120, Height: 30}, client.SessionReadyMsg{SessionID: "sess-test-0001", Mode: "accept-edits"})
	m.caps.Posture = postureYolo

	press := func() {
		t.Helper()
		mm, cmd := m.Update(modeKey())
		m = mm.(Model)
		if cmd != nil {
			m = applyAll(m, cmd())
		}
	}
	conv.setModeErr = errors.New(`mode "auto" requires an enforcing guardrails checker`)
	press()
	if m.activeMode != "accept-edits" || !strings.Contains(stripANSIstr(m.statusMsg), "mode auto refused") {
		t.Fatalf("after auto refusal: active=%q status=%q", m.activeMode, stripANSIstr(m.statusMsg))
	}
	conv.setModeErr = nil
	press()
	if got := conv.setModes(); len(got) != 2 || got[1] != "yolo" {
		t.Fatalf("SetMode calls = %v, want [auto yolo]", got)
	}
	if m.activeMode != "yolo" || m.headerMode() != "yolo" {
		t.Fatalf("active/header = %q/%q, want yolo", m.activeMode, m.headerMode())
	}
}

// TestModeMismatchAdoptsServerMode pins that a SetMode reply naming a
// different mode than requested is adopted, not swallowed into a permanent
// "pending" header.
func TestModeMismatchAdoptsServerMode(t *testing.T) {
	conv := &fakeConv{recv: &fakeRecver{}, send: &fakeSender{}, mode: "default"}
	m := modeTestModel(t, conv)
	m.pendingMode = "auto"

	m = applyAll(m, client.ModeChangedMsg{SessionID: "sess-test-0001", Requested: "auto", Mode: "default"})
	if m.activeMode != "default" || m.pendingMode != "" {
		t.Fatalf("active/pending mode = %q/%q, want default/empty", m.activeMode, m.pendingMode)
	}
	if status := stripANSIstr(m.statusMsg); !strings.Contains(status, "mode auto not applied") || !strings.Contains(status, "kept default") {
		t.Fatalf("status = %q, want mismatch notice", status)
	}
	if strings.Contains(m.headerMode(), "pending") {
		t.Fatalf("header mode = %q, must not stay pending", m.headerMode())
	}
}

func TestStaleModeResponseDoesNotClobberNewerRequest(t *testing.T) {
	conv := &fakeConv{recv: &fakeRecver{}, send: &fakeSender{}, mode: "default"}
	m := modeTestModel(t, conv)
	m.pendingMode = "accept-edits"

	m = applyAll(m, client.ModeChangedMsg{SessionID: "sess-test-0001", Requested: "plan", Mode: "plan"})
	if m.activeMode != "default" || m.pendingMode != "accept-edits" {
		t.Fatalf("stale response changed active/pending to %q/%q", m.activeMode, m.pendingMode)
	}
}

func TestNewSessionUsesCurrentMode(t *testing.T) {
	conv := &fakeConv{recv: &fakeRecver{}, send: &fakeSender{}, mode: "default"}
	m := modeTestModel(t, conv)
	m.activeMode = "plan"

	msg := m.createSessionCmd()()
	ready, ok := msg.(client.SessionReadyMsg)
	if !ok {
		t.Fatalf("createSessionCmd msg = %T, want SessionReadyMsg", msg)
	}
	if ready.Mode != "plan" {
		t.Fatalf("ready mode = %q, want plan", ready.Mode)
	}
	if conv.mode != "plan" {
		t.Fatalf("created server mode = %q, want plan", conv.mode)
	}
}

func TestPendingModeBlocksQueuedPromptUntilApplied(t *testing.T) {
	conv := &fakeConv{recv: &fakeRecver{}, send: &fakeSender{}, mode: "default"}
	m := modeTestModel(t, conv)
	m.pendingMode = "plan"
	m.queued = []string{"queued follow-up"}

	mm, cmd := m.drainQueue("end_turn")
	m = mm.(Model)
	if cmd != nil {
		t.Fatal("drainQueue should not submit while a mode switch is pending")
	}
	if strings.TrimSpace(m.prompt.Value()) != "queued follow-up" {
		t.Fatalf("textarea = %q, want queued follow-up kept for manual send", m.prompt.Value())
	}
	if !strings.Contains(stripANSIstr(m.statusMsg), "will apply before the queued prompt") {
		t.Fatalf("status = %q, want pending-mode queue notice", stripANSIstr(m.statusMsg))
	}
}
