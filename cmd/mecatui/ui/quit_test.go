package ui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/exp/teatest/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

// ctrlC is the ctrl+c key press the quit guard reacts to.
func ctrlC() tea.KeyPressMsg { return tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl} }

// quitModel builds an idle, connected Model for the direct (clock-free) quit-guard
// unit tests: it is sized, marked idle, and given a non-nil cancelRun stub so the
// "second ctrl+c quits" path exercises the cancel without a live stream.
func quitModel(t *testing.T) Model {
	t.Helper()
	m := newTestModelFromDeps(Deps{Theme: theme.New("aztec", theme.AztecPalette()), Ctx: context.Background()})
	m = applyAll(m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m.phase = phaseIdle
	return m
}

// pressKey feeds one key through Update and returns the new Model plus the command.
func pressKey(m Model, msg tea.Msg) (Model, tea.Cmd) {
	mm, cmd := m.Update(msg)
	return mm.(Model), cmd
}

// TestQuitGuardFirstCtrlCEmptyArms asserts a first ctrl+c on an empty prompt does
// NOT quit: it arms the guard, shows the hint in the footer, and schedules a disarm
// tick (a non-nil command). isQuitCmd would be true only on a real quit, so we
// assert the model is still alive and armed.
func TestQuitGuardFirstCtrlCEmptyArms(t *testing.T) {
	m := quitModel(t)
	if m.quitArmed {
		t.Fatal("guard should start disarmed")
	}
	m, cmd := pressKey(m, ctrlC())
	if !m.quitArmed {
		t.Fatal("first ctrl+c on an empty prompt should arm the guard, not quit")
	}
	// The scheduled command must be the DISARM tick specifically, not the render
	// tick. Both are tea.Tick commands (opaque, and tea.Tick's message only resolves
	// after its full wall-clock window — 3s for the disarm, so we do NOT execute it
	// here). Discriminate STRUCTURALLY instead: renderTickCmd always sets tickArmed,
	// the disarm tick never does — so a non-nil cmd with tickArmed still false proves
	// the render tick did not leak into this path. The disarm tick's own gen-matched
	// semantics are proven directly (no wall clock) in TestQuitDisarmMsgGenStaleVsMatching.
	if cmd == nil {
		t.Fatal("arming should schedule a disarm tick (non-nil cmd)")
	}
	if m.tickArmed {
		t.Error("arming the quit guard must not arm the render tick (wrong command scheduled)")
	}
	if m.statusMsg != quitHintFor(defaultKeys().Quit) {
		t.Errorf("status should carry the quit hint, got %q", m.statusMsg)
	}
	footer := stripANSIstr(m.renderFooter())
	if !strings.Contains(footer, "ctrl+c again to quit") {
		t.Errorf("armed footer should show the again-to-quit hint:\n%s", footer)
	}
}

// TestQuitGuardSecondCtrlCQuits asserts a second ctrl+c while armed returns
// tea.Quit (and fires the cancelRun stub).
func TestQuitGuardSecondCtrlCQuits(t *testing.T) {
	m := quitModel(t)
	cancelled := false
	m.cancelRun = func() { cancelled = true }

	m, _ = pressKey(m, ctrlC())
	if !m.quitArmed {
		t.Fatal("first ctrl+c should arm")
	}
	_, cmd := pressKey(m, ctrlC())
	if !isQuitCmd(cmd) {
		t.Error("second ctrl+c while armed should quit")
	}
	if !cancelled {
		t.Error("quit should fire the run cancel")
	}
}

// TestQuitGuardInterveningKeyDisarms asserts a non-ctrl+c key while armed disarms
// the guard and clears the hint, and that a subsequent single ctrl+c RE-ARMS (does
// not quit) — proving the window only spans consecutive ctrl+c presses.
func TestQuitGuardInterveningKeyDisarms(t *testing.T) {
	m := quitModel(t)

	m, _ = pressKey(m, ctrlC())
	if !m.quitArmed {
		t.Fatal("first ctrl+c should arm")
	}
	// An intervening key disarms and clears the hint. pgup scrolls the viewport
	// without inserting into the prompt, so the input stays empty and the re-arm
	// below exercises the empty-prompt arm path (not the clear-input path).
	m, _ = pressKey(m, tea.KeyPressMsg{Code: tea.KeyPgUp})
	if m.quitArmed {
		t.Error("an intervening key should disarm the guard")
	}
	if m.statusMsg == quitHintFor(defaultKeys().Quit) {
		t.Error("an intervening key should clear the quit hint")
	}
	// Render-level proof: the footer no longer carries the armed hint.
	if footer := stripANSIstr(m.renderFooter()); strings.Contains(footer, "ctrl+c again to quit") {
		t.Errorf("disarmed footer should not show the again-to-quit hint:\n%s", footer)
	}
	// A subsequent single ctrl+c must RE-ARM, not quit.
	m, cmd := pressKey(m, ctrlC())
	if !m.quitArmed {
		t.Error("ctrl+c after a disarm should re-arm")
	}
	if isQuitCmd(cmd) {
		t.Error("ctrl+c after a disarm must not quit (window was broken)")
	}
}

// TestQuitGuardFirstCtrlCNonEmptyClearsInput asserts a first ctrl+c with staged
// input clears the input and does NOT arm (mirrors esc's clear-the-line).
func TestQuitGuardFirstCtrlCNonEmptyClearsInput(t *testing.T) {
	m := quitModel(t)
	m.prompt.Rewrite("a draft prompt")

	m, cmd := pressKey(m, ctrlC())
	if m.quitArmed {
		t.Error("ctrl+c with non-empty input should NOT arm the guard")
	}
	if strings.TrimSpace(m.prompt.Value()) != "" {
		t.Errorf("ctrl+c with non-empty input should clear the input, got %q", m.prompt.Value())
	}
	if isQuitCmd(cmd) {
		t.Error("ctrl+c with non-empty input must not quit")
	}
}

// TestQuitDisarmMsgGenStaleVsMatching asserts the disarm tick disarms on a matching
// generation, and that a STALE tick (carrying a previous arm gen) does NOT disarm a
// guard that was disarmed-and-re-armed in between.
func TestQuitDisarmMsgGenStaleVsMatching(t *testing.T) {
	m := quitModel(t)

	// Arm once → gen G1.
	m, _ = pressKey(m, ctrlC())
	g1 := m.quitArmGen
	if !m.quitArmed {
		t.Fatal("first ctrl+c should arm")
	}

	// A STALE tick: disarm then re-arm so the current gen advances past G1, then
	// deliver the G1 tick. It must be ignored (guard stays armed). pgup disarms
	// without inserting into the prompt, so the re-arm hits the empty-prompt path.
	m, _ = pressKey(m, tea.KeyPressMsg{Code: tea.KeyPgUp}) // disarm
	m, _ = pressKey(m, ctrlC())                            // re-arm → gen G2
	g2 := m.quitArmGen
	if g2 == g1 {
		t.Fatal("re-arm should advance the arm generation")
	}
	m, _ = pressKey(m, quitDisarmMsg{gen: g1})
	if !m.quitArmed {
		t.Error("a stale-gen disarm tick must NOT disarm a re-armed guard")
	}

	// The MATCHING tick disarms and clears the hint.
	m, _ = pressKey(m, quitDisarmMsg{gen: g2})
	if m.quitArmed {
		t.Error("a matching-gen disarm tick should disarm the guard")
	}
	if m.statusMsg == quitHintFor(defaultKeys().Quit) {
		t.Error("disarm should clear the quit hint")
	}
	// Render-level proof (not just statusMsg): the footer must no longer carry the
	// armed hint, mirroring the arm-side render assertion in
	// TestQuitGuardFirstCtrlCEmptyArms.
	if footer := stripANSIstr(m.renderFooter()); strings.Contains(footer, "ctrl+c again to quit") {
		t.Errorf("disarmed footer should not show the again-to-quit hint:\n%s", footer)
	}
}

// TestQuitGuardFatalExitsOnFirstPress asserts the fatal (dead-connection) screen
// exits on a SINGLE ctrl+c — the guard is bypassed there.
func TestQuitGuardFatalExitsOnFirstPress(t *testing.T) {
	m := quitModel(t)
	m.phase = phaseFatal
	m.fatalErr = "dial tcp: connection refused"

	_, cmd := pressKey(m, ctrlC())
	if !isQuitCmd(cmd) {
		t.Error("fatal screen should quit on the first ctrl+c (single press)")
	}
}

// TestQuitGuardArmedThenFatalQuitsOnSinglePress covers the `quitArmed || phaseFatal`
// branch combination: arm on an idle empty prompt, then transition to phaseFatal
// (e.g. a connection drops mid-session), and assert a SINGLE further ctrl+c quits —
// the fatal screen exits regardless of the armed state.
func TestQuitGuardArmedThenFatalQuitsOnSinglePress(t *testing.T) {
	m := quitModel(t)

	m, _ = pressKey(m, ctrlC())
	if !m.quitArmed {
		t.Fatal("first ctrl+c on an empty prompt should arm")
	}
	// The connection dies after the guard armed.
	m.phase = phaseFatal
	m.fatalErr = "stream closed"

	_, cmd := pressKey(m, ctrlC())
	if !isQuitCmd(cmd) {
		t.Error("a single ctrl+c on the fatal screen should quit even when armed")
	}
}

// TestQuitDoublePressProgram is the whole-program proof that a SINGLE ctrl+c does
// not exit but a SECOND one does. It connects, presses ctrl+c once (the program
// keeps running — the guard arms), then ctrl+c again to exit, and asserts the
// program finished with the guard observable on the final model.
func TestQuitDoublePressProgram(t *testing.T) {
	pd := newProgramModel(t, theme.New("aztec", theme.AztecPalette()), preApprovalScript(), "permission.ask")
	tm := teatest.NewTestModel(t, pd.model, teatest.WithInitialTermSize(100, 30))

	pd.prog.wait(t, phaseIdle, 5*time.Second)

	// First ctrl+c: the program must NOT exit (the guard arms). We can't prove a
	// non-event directly, so we assert the program is still alive by driving a second
	// ctrl+c and seeing it finish — if the first had quit, the second would be
	// delivered after exit and WaitFinished would still pass, so the discriminating
	// check is the FinalModel below (armed + idle, proving the first press armed
	// rather than quit).
	tm.Send(ctrlC())
	tm.Send(ctrlC())
	tm.WaitFinished(t, teatest.WithFinalTimeout(scaleWait(3*time.Second)))

	fm := tm.FinalModel(t).(Model)
	if !fm.quitArmed {
		t.Error("after the first ctrl+c the guard should have armed (final model not armed ⇒ first press did not arm)")
	}
	if fm.phase != phaseIdle {
		t.Errorf("final phase = %s, want idle (no run was active)", phaseName(fm.phase))
	}
}

// TestQuitDoublePressCancelsRunningProgram is the whole-program proof of the
// "second ctrl+c fires cancelRun mid-run" wiring through the real teatest harness
// (the gap TestQuitDoublePressProgram leaves: it runs on an idle session where
// cancelRun is nil). It drives a GATED run to phaseRunning, presses ctrl+c once (the
// guard arms — the program keeps running), presses it again (quits), and asserts the
// run's context was cancelled — observed via the fake closing runCancelled when the
// per-run ctx submitPrompt handed OpenConverse is cancelled (cancelRun's effect),
// not a Cancel frame (the quit path calls cancelRun directly, sending no frame).
func TestQuitDoublePressCancelsRunningProgram(t *testing.T) {
	th := theme.New("aztec", theme.AztecPalette())
	// Gate the run at its delta so it is provably still streaming (phaseRunning) when
	// the ctrl+c presses land: the result is physically held in the fake until the
	// test would release it — and it never does, because the quit cancels the run.
	recv := &fakeRecver{script: simpleRunScript("only"), gateType: "message.delta", gate: make(chan struct{}), reachedGate: make(chan struct{})}
	send := &fakeSender{}
	conv := &fakeConv{
		recv:         recv,
		send:         send,
		recvers:      []*fakeRecver{recv},
		sessionReady: make(chan struct{}),
		runCancelled: make(chan struct{}),
	}
	prog := newProgress()
	model := newTestModelFromDeps(Deps{
		Session:     conv,
		Conv:        conv,
		Theme:       th,
		Ctx:         context.Background(),
		NoAltScreen: true,
		onPhase:     prog.record,
	})
	tm := teatest.NewTestModel(t, model, teatest.WithInitialTermSize(100, 30))

	prog.wait(t, phaseIdle, 5*time.Second)

	// Start the run; it streams its delta then blocks before the result (gated), so
	// the reducer is provably in phaseRunning when the ctrl+c presses are reduced.
	tm.Type("run something")
	tm.Send(tea.KeyPressMsg{Code: tea.KeyEnter})
	prog.wait(t, phaseRunning, 5*time.Second)
	waitClosed(t, "run streamed its delta (result gated)", recv.reachedGate, 5*time.Second)

	// First ctrl+c arms (does NOT quit); second ctrl+c quits and fires cancelRun.
	tm.Send(ctrlC())
	tm.Send(ctrlC())

	// The decisive proof: the per-run context was cancelled (cancelRun fired). This
	// closes independently of Bubble Tea's output flush, so it is starvation-robust.
	waitClosed(t, "run context cancelled by the quit (cancelRun fired)", conv.runCancelled, 5*time.Second)

	tm.WaitFinished(t, teatest.WithFinalTimeout(scaleWait(3*time.Second)))

	// The run was active when the first ctrl+c armed, so the guard must have been
	// armed (proving the first press armed rather than quit mid-run).
	if fm := tm.FinalModel(t).(Model); !fm.quitArmed {
		t.Error("the first ctrl+c during a run should have armed the guard")
	}
}

// TestQuitFatalSinglePressProgram is the whole-program proof that the fatal screen
// exits on the FIRST ctrl+c. CreateSession fails (a fake that errors), driving the
// reducer to phaseFatal; a single ctrl+c then finishes the program.
func TestQuitFatalSinglePressProgram(t *testing.T) {
	prog := newProgress()
	model := newTestModelFromDeps(Deps{
		Session:     &errSession{},
		Theme:       theme.New("aztec", theme.AztecPalette()),
		Ctx:         context.Background(),
		NoAltScreen: true,
		onPhase:     prog.record,
	})
	tm := teatest.NewTestModel(t, model, teatest.WithInitialTermSize(100, 30))

	// The fake's CreateSession errors → ConnectErrMsg → phaseFatal. Gate on the
	// reducer actually reaching fatal (output-flush-independent, like the rest of the
	// suite) BEFORE the ctrl+c, so the press lands on the fatal screen and not on
	// phaseConnecting (where it would arm the guard instead of quitting).
	prog.wait(t, phaseFatal, 5*time.Second)

	// A SINGLE ctrl+c on the fatal screen exits.
	tm.Send(ctrlC())
	tm.WaitFinished(t, teatest.WithFinalTimeout(scaleWait(3*time.Second)))

	fm := tm.FinalModel(t).(Model)
	if fm.phase != phaseFatal {
		t.Errorf("final phase = %s, want fatal", phaseName(fm.phase))
	}
}

// errSession is a SessionCreator whose CreateSession always fails, driving the
// reducer to phaseFatal for the single-press fatal-exit proof.
type errSession struct{}

func (errSession) CreateSession(_ context.Context, _ client.ModelSelection, _ string) (string, client.Capabilities, client.ResolvedModel, error) {
	return "", client.Capabilities{}, client.ResolvedModel{}, context.DeadlineExceeded
}

// CreateSessionWithCarryover satisfies the SessionCreator carryover seam (issue #20).
// errSession models a always-failing connect for the fatal-exit proof, so the
// carryover variant fails identically — the source id is irrelevant to that path.
func (errSession) CreateSessionWithCarryover(ctx context.Context, _ string, sel client.ModelSelection, mode string) (string, client.Capabilities, client.ResolvedModel, error) {
	return errSession{}.CreateSession(ctx, sel, mode)
}

func (errSession) ClearSession(_ context.Context, _ string, _ *string) (string, client.SessionSnapshot, error) {
	return "", client.SessionSnapshot{}, context.DeadlineExceeded
}

func (errSession) CloseSession(_ context.Context, _ string) error { return nil }

func (errSession) GetSession(_ context.Context, _ string) (client.SessionSnapshot, error) {
	return client.SessionSnapshot{}, nil
}

func (errSession) SetMode(_ context.Context, _, mode string) (string, error) { return mode, nil }

func (errSession) ForkSession(_ context.Context, _, _ string) (string, error) {
	return "", context.DeadlineExceeded
}
