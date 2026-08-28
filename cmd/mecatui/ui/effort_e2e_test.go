package ui

import (
	"context"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/exp/teatest/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

// The user-reachable E2E for the /effort fork-resume slice (ADR 0068), at the
// teatest level (the real program loop). It proves the headline behaviour
// end-to-end: driving /effort → enter forks the session at the new effort, the
// TRANSCRIPT SURVIVES (no resetSession wipe), the session id rebinds to the fork,
// and the source is closed once. The failure case proves a failed fork stays
// RECOVERABLE and does NOT close the source.
//
// teatest DISCIPLINE (the documented -race flush-starvation flake): these cases
// sequence on the fake's goroutine signals (conv.forked / conv.sessionReady) + the
// onPhase observer — NEVER WaitFor(tm.Output()). conv.forked closes when ForkSession
// returns, conv.forkedFrom/forkedEffort record what it carried.

// newEffortProgram wires a real program with a Models lister + the deterministic
// fork/session signals. The fake stream is idle-able (no prompt is sent); the
// fork-time behaviour is what we test.
func newEffortProgram(t *testing.T, models []client.ModelInfo) (Model, *fakeConv, *progress) {
	t.Helper()
	recv := &fakeRecver{gate: make(chan struct{})}
	conv := &fakeConv{
		recv:         recv,
		send:         &fakeSender{},
		caps:         client.Capabilities{ModelSelection: true},
		sessionReady: make(chan struct{}),
		created:      make(chan struct{}),
		forked:       make(chan struct{}),
	}
	prog := newProgress()
	m := newTestModelFromDeps(Deps{
		Session:      conv,
		Conv:         conv,
		Models:       &fakeModels{models: models},
		Theme:        theme.New("aztec", theme.AztecPalette()),
		Server:       "127.0.0.1:8080",
		Workspace:    "/workspace",
		Mode:         "default",
		Ctx:          context.Background(),
		NoAltScreen:  true,
		InitialModel: client.ModelSelection{ProviderID: "openai", ModelID: "gpt-5"},
		onPhase:      prog.record,
	})
	return m, conv, prog
}

// TestEffortE2EForkPreservesTranscript drives /effort → enter and asserts the fork
// fired at the new effort, the session rebound to the fork id, the source was
// closed once, and — the headline regression guard — the conversation transcript
// survived (the fork carries it server-side, so the client must NOT wipe it).
func TestEffortE2EForkPreservesTranscript(t *testing.T) {
	models := []client.ModelInfo{
		{ID: "gpt-5", ProviderID: "openai", DisplayName: "GPT-5", Reasoning: true, ContextLimit: 200000},
	}
	m, conv, prog := newEffortProgram(t, models)
	tm := teatest.NewTestModel(t, m, teatest.WithInitialTermSize(100, 30))

	// Connect + settle to idle.
	waitClosed(t, "startup CreateSession", conv.created, 5*time.Second)
	prog.wait(t, phaseIdle, 5*time.Second)

	// Stage a transcript by submitting one prompt and letting the run complete (the
	// fake stream EOFs immediately, recording a user block + an assistant block).
	for _, r := range "hello there" {
		tm.Send(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	tm.Send(tea.KeyPressMsg{Code: tea.KeyEnter})
	prog.waitRunComplete(t, 1, 5*time.Second)
	prog.wait(t, phaseIdle, 5*time.Second)

	// Drive /effort via the slash-command submit path, then enter on the current row.
	for _, r := range "/effort" {
		tm.Send(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	tm.Send(tea.KeyPressMsg{Code: tea.KeyEnter})
	// Move to "high" (the picker opens on the current row; move down to a real tier)
	// and press enter — the fork fires DIRECTLY (no confirm step).
	tm.Send(tea.KeyPressMsg{Code: tea.KeyDown})
	tm.Send(tea.KeyPressMsg{Code: tea.KeyDown})
	tm.Send(tea.KeyPressMsg{Code: tea.KeyDown})
	tm.Send(tea.KeyPressMsg{Code: tea.KeyEnter})

	// Sequence on the fork-happened signal (goroutine signal), not output.
	waitClosed(t, "ForkSession after /effort pick", conv.forked, 5*time.Second)
	if conv.forkedEffort == "" {
		t.Fatalf("ForkSession carried an empty effort, want the picked tier")
	}
	// The fork's SessionReadyMsg rebinds the session and returns to idle.
	prog.wait(t, phaseIdle, 5*time.Second)

	// Graceful double-ctrl+c quit.
	tm.Send(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	tm.Send(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	tm.WaitFinished(t, teatest.WithFinalTimeout(scaleWait(3*time.Second)))

	fm := tm.FinalModel(t).(Model)
	// The transcript survived the fork (the regression guard against resetSession).
	if fm.conv.isEmpty() {
		t.Errorf("transcript is EMPTY after the effort fork — resetSession was re-introduced; the fork must keep the conversation")
	}
	// The session id rebound to the fork id (distinct from the startup id).
	if fm.sessionID == "" || fm.sessionID == "sess-test-0001" {
		t.Errorf("sessionID = %q, want the fork id (rebound, not the startup sess-test-0001)", fm.sessionID)
	}
	// The source was closed exactly once (best-effort, after a successful fork).
	if got := conv.closed(); len(got) != 1 || got[0] != "sess-test-0001" {
		t.Errorf("closed = %v, want [sess-test-0001] (source closed once after the fork)", got)
	}
}

// TestEffortE2EForkFailureLeavesSourceOpen drives /effort → enter with a fork that
// FAILS, and asserts the recoverable path: restartFailed armed (NOT the terminal
// fatal screen) and the source session NOT closed (a failed fork leaves the live
// session alone).
func TestEffortE2EForkFailureLeavesSourceOpen(t *testing.T) {
	models := []client.ModelInfo{
		{ID: "gpt-5", ProviderID: "openai", DisplayName: "GPT-5", Reasoning: true, ContextLimit: 200000},
	}
	m, conv, prog := newEffortProgram(t, models)
	conv.forkErr = context.DeadlineExceeded
	tm := teatest.NewTestModel(t, m, teatest.WithInitialTermSize(100, 30))

	waitClosed(t, "startup CreateSession", conv.created, 5*time.Second)
	prog.wait(t, phaseIdle, 5*time.Second)

	// Drive /effort → enter on a tier → the fork fails.
	for _, r := range "/effort" {
		tm.Send(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	tm.Send(tea.KeyPressMsg{Code: tea.KeyEnter})
	tm.Send(tea.KeyPressMsg{Code: tea.KeyDown})
	tm.Send(tea.KeyPressMsg{Code: tea.KeyEnter})

	// The fork fired (and failed); the recoverable reducer settles back to idle.
	waitClosed(t, "ForkSession (failing) after /effort pick", conv.forked, 5*time.Second)
	prog.wait(t, phaseIdle, 5*time.Second)

	tm.Send(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	tm.Send(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	tm.WaitFinished(t, teatest.WithFinalTimeout(scaleWait(3*time.Second)))

	fm := tm.FinalModel(t).(Model)
	if !fm.restartFailed {
		t.Errorf("restartFailed = false, want true (recoverable fork failure armed, not the fatal screen)")
	}
	if fm.phase != phaseIdle {
		t.Errorf("phase = %s, want idle (recoverable, not fatal)", phaseName(fm.phase))
	}
	// The source session was NOT closed — a failed fork leaves the live session alone.
	if got := conv.closed(); len(got) != 0 {
		t.Errorf("closed = %v, want empty (the source is NOT closed on a failed fork)", got)
	}
}

// TestEffortE2ELocalMidRunThenForksAfterRunEnd pins the in-flight-run ordering
// ADR 0068 promises ("switchEffort ends any in-flight run before forking") at the
// teatest level. /effort is a bare local built-in, so typed mid-run it reaches its
// idle-only guard immediately: it does NOT open the picker, enqueue, or fork over
// a live run. After the run ends, a fresh /effort opens the picker and enter forks.
// A GATED run that stays streaming proves no fork fires while the run is live.
func TestEffortE2ELocalMidRunThenForksAfterRunEnd(t *testing.T) {
	models := []client.ModelInfo{
		{ID: "gpt-5", ProviderID: "openai", DisplayName: "GPT-5", Reasoning: true, ContextLimit: 200000},
	}
	m, conv, prog := newEffortProgram(t, models)
	tm := teatest.NewTestModel(t, m, teatest.WithInitialTermSize(100, 30))

	waitClosed(t, "startup CreateSession", conv.created, 5*time.Second)
	prog.wait(t, phaseIdle, 5*time.Second)

	// Gate the run at its delta so it is provably still streaming (phaseRunning) while
	// the /effort command is typed: the result is physically held in the fake until the
	// test releases it. While gated, the local /effort must NOT fork.
	conv.recv.mu.Lock()
	conv.recv.script = simpleRunScript("working")
	conv.recv.gateType = "message.delta"
	conv.recv.gate = make(chan struct{})
	conv.recv.reachedGate = make(chan struct{})
	conv.recv.mu.Unlock()

	// Start the run; it streams its delta then blocks before the result (gated).
	for _, r := range "run something" {
		tm.Send(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	tm.Send(tea.KeyPressMsg{Code: tea.KeyEnter})
	prog.wait(t, phaseRunning, 5*time.Second)
	waitClosed(t, "run streamed its delta (result gated)", conv.recv.reachedGate, 5*time.Second)

	// Type /effort mid-run: it runs locally but the idle-only picker guard keeps it
	// closed. No fork may fire yet.
	for _, r := range "/effort" {
		tm.Send(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	tm.Send(tea.KeyPressMsg{Code: tea.KeyEnter})
	select {
	case <-conv.forked:
		t.Fatal("a fork fired while the run was still live — /effort must not fork over an in-flight run")
	case <-time.After(scaleWait(500 * time.Millisecond)):
	}

	// Release the gate and wait for the in-flight run to finish. The local mid-run
	// /effort did not queue a follow-up, so submit it again now that the picker can
	// open on an idle session.
	conv.recv.release()
	prog.waitRunComplete(t, 1, 5*time.Second)
	prog.wait(t, phaseIdle, 5*time.Second)

	for _, r := range "/effort" {
		tm.Send(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	tm.Send(tea.KeyPressMsg{Code: tea.KeyEnter})

	// The picker is open. Move to a real tier and apply — the source run is already
	// ended.
	tm.Send(tea.KeyPressMsg{Code: tea.KeyDown}) // picker: move to a real tier
	tm.Send(tea.KeyPressMsg{Code: tea.KeyEnter})

	waitClosed(t, "ForkSession after the idle /effort pick", conv.forked, 5*time.Second)
	if conv.forkedEffort == "" {
		t.Fatalf("ForkSession carried an empty effort, want the picked tier")
	}
	prog.wait(t, phaseIdle, 5*time.Second)

	tm.Send(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	tm.Send(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	tm.WaitFinished(t, teatest.WithFinalTimeout(scaleWait(3*time.Second)))
}
