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
	return tea.KeyPressMsg{Code: 'm', Mod: tea.ModAlt}
}

func TestModeSwitchUpdatesServerAndHeader(t *testing.T) {
	conv := &fakeConv{recv: &fakeRecver{}, send: &fakeSender{}, mode: "default"}
	m := modeTestModel(t, conv)

	mm, cmd := m.Update(modeKey())
	m = mm.(Model)
	if cmd == nil {
		t.Fatal("alt+m should issue SetMode command")
	}
	m = applyAll(m, cmd())

	if got := conv.setModes(); len(got) != 1 || got[0] != "plan" {
		t.Fatalf("SetMode calls = %v, want [plan]", got)
	}
	if m.activeMode != "plan" || m.pendingMode != "" {
		t.Fatalf("active/pending mode = %q/%q, want plan/empty", m.activeMode, m.pendingMode)
	}
	if header := stripANSIstr(m.renderHeader()); !strings.Contains(header, "mode plan") {
		t.Fatalf("header missing mode plan: %q", header)
	}
}

func TestModeSwitchFailureDefersToNextPrompt(t *testing.T) {
	conv := &fakeConv{recv: &fakeRecver{}, send: &fakeSender{}, mode: "default", setModeErr: errors.New("illegal transition")}
	m := modeTestModel(t, conv)
	m.phase = phaseRunning

	mm, cmd := m.Update(modeKey())
	m = mm.(Model)
	if cmd == nil {
		t.Fatal("alt+m while running should attempt SetMode")
	}
	m = applyAll(m, cmd())

	if m.activeMode != "default" || m.pendingMode != "plan" {
		t.Fatalf("active/pending mode = %q/%q, want default/plan", m.activeMode, m.pendingMode)
	}
	if !strings.Contains(stripANSIstr(m.statusMsg), "will apply on the next prompt") {
		t.Fatalf("status = %q, want deferred notice", stripANSIstr(m.statusMsg))
	}
	if header := stripANSIstr(m.renderHeader()); !strings.Contains(header, "mode plan pending") {
		t.Fatalf("header missing pending mode: %q", header)
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
