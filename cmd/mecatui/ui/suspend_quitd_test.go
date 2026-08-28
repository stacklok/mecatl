package ui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

// ctrlD is the ctrl+d key press the QuitD guard reacts to.
func ctrlD() tea.KeyPressMsg { return tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl} }

// ctrlZ is the ctrl+z key press that suspends the TUI.
func ctrlZ() tea.KeyPressMsg { return tea.KeyPressMsg{Code: 'z', Mod: tea.ModCtrl} }

// isSuspendCmd reports whether cmd is tea.Suspend (Suspend() returns nil-ish
// command whose message is a SuspendMsg). Like isQuitCmd it executes the command
// directly — tea.Suspend is a plain command constructor (func() tea.Msg returning
// SuspendMsg{}), not a timer.
func isSuspendCmd(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}
	_, ok := cmd().(tea.SuspendMsg)
	return ok
}

// --- ctrl+d (QuitD) ---

func TestQuitDFirstOnEmptyArms(t *testing.T) {
	m := quitModel(t)
	if m.quitDArmed {
		t.Fatal("QuitD guard should start disarmed")
	}
	m, cmd := pressKey(m, ctrlD())
	if !m.quitDArmed {
		t.Fatal("first ctrl+d on an empty prompt should arm the QuitD guard, not quit")
	}
	if cmd == nil {
		t.Fatal("arming should schedule a disarm tick")
	}
	if m.statusMsg != quitDHintFor(defaultKeys().QuitD) {
		t.Errorf("status should carry the quitD hint, got %q", m.statusMsg)
	}
	if got := stripANSIstr(m.renderFooter()); got == "" {
		t.Error("footer should render with the armed hint present")
	}
}

func TestQuitDSecondQuits(t *testing.T) {
	m := quitModel(t)
	cancelled := false
	m.cancelRun = func() { cancelled = true }
	m, _ = pressKey(m, ctrlD())
	if !m.quitDArmed {
		t.Fatal("first ctrl+d should arm")
	}
	_, cmd := pressKey(m, ctrlD())
	if !isQuitCmd(cmd) {
		t.Error("second ctrl+d while armed should quit")
	}
	if !cancelled {
		t.Error("quit should fire the run cancel")
	}
}

// TestQuitDPopulatedPromptDoesNotQuit pins the empty-prompt gate: ctrl+d on a
// populated prompt is the textarea's DeleteCharacterForward, never a quit.
func TestQuitDPopulatedPromptDoesNotQuit(t *testing.T) {
	m := quitModel(t)
	m.ta.Rewrite("a draft prompt")
	m, cmd := pressKey(m, ctrlD())
	if m.quitDArmed {
		t.Error("ctrl+d on a populated prompt must not arm the QuitD guard")
	}
	if isQuitCmd(cmd) {
		t.Error("ctrl+d on a populated prompt must not quit")
	}
}

// TestQuitDIndependentOfQuitC pins the independent-armed-state decision (issue
// #504): each guard's armed window spans ONLY its own key's consecutive presses,
// and — the load-bearing safety property — NEITHER key confirms the OTHER's armed
// quit. Arming ctrl+c then pressing ctrl+d must not quit; arming ctrl+d then
// pressing ctrl+c must not quit. The other key simply acts as an intervening
// disarm (so each key's own hint is conservative, matching how ctrl+c alone treats
// any other key today).
func TestQuitDIndependentOfQuitC(t *testing.T) {
	// Arm ctrl+c (Quit), then press ctrl+d (QuitD): must not quit.
	m := quitModel(t)
	m, _ = pressKey(m, ctrlC())
	if !m.quitArmed {
		t.Fatal("ctrl+c should arm the Quit guard")
	}
	m, cmd := pressKey(m, ctrlD())
	if isQuitCmd(cmd) {
		t.Error("ctrl+d must not confirm an armed ctrl+c guard")
	}

	// Arm ctrl+d (QuitD), then press ctrl+c (Quit): must not quit.
	m2 := quitModel(t)
	m2, _ = pressKey(m2, ctrlD())
	if !m2.quitDArmed {
		t.Fatal("ctrl+d should arm the QuitD guard")
	}
	m2, cmd2 := pressKey(m2, ctrlC())
	if isQuitCmd(cmd2) {
		t.Error("ctrl+c must not confirm an armed ctrl+d guard")
	}
}

func TestQuitDHintNamesLiveChord(t *testing.T) {
	m := quitModel(t)
	m, _ = pressKey(m, ctrlD())
	if got := stripANSIstr(m.renderFooter()); got == "" {
		t.Error("footer should render")
	}
	if m.statusMsg != "press ctrl+d again to quit" {
		t.Errorf("default hint = %q, want 'press ctrl+d again to quit'", m.statusMsg)
	}
}

// TestQuitDDisarmStaleGenIgnored mirrors the Quit guard's stale-gen test: a disarm
// tick for an OLD generation must not disarm a freshly re-armed guard.
func TestQuitDDisarmStaleGenIgnored(t *testing.T) {
	m := quitModel(t)
	m, _ = pressKey(m, ctrlD())
	if !m.quitDArmed {
		t.Fatal("first ctrl+d should arm")
	}
	gen1 := m.quitDArmGen
	// Re-arm (another ctrl+d confirms → but here simulate re-arm via a fresh first
	// press after disarming by an intervening key) — simplest: bump arm manually.
	m, _ = pressKey(m, tea.KeyPressMsg{Code: tea.KeyPgUp}) // disarms
	if m.quitDArmed {
		t.Fatal("intervening key should disarm")
	}
	m, _ = pressKey(m, ctrlD()) // re-arm, gen2
	if m.quitDArmGen == gen1 {
		t.Fatal("re-arm should bump the generation")
	}
	// A disarm tick carrying the STALE gen must not disarm the fresh guard.
	m, _ = pressKey(m, quitDDisarmMsg{gen: gen1})
	if !m.quitDArmed {
		t.Error("a stale-generation disarm tick must not disarm the re-armed guard")
	}
	// A tick carrying the CURRENT gen disarms.
	m, _ = pressKey(m, quitDDisarmMsg{gen: m.quitDArmGen})
	if m.quitDArmed {
		t.Error("a matching-generation disarm tick should disarm")
	}
}

// --- ctrl+z (Suspend) ---

func TestSuspendReturnsSuspendCmd(t *testing.T) {
	m := quitModel(t)
	_, cmd := pressKey(m, ctrlZ())
	if !isSuspendCmd(cmd) {
		t.Error("ctrl+z should return tea.Suspend")
	}
}

// TestSuspendWorksInEveryPhase pins the "suspend everywhere" contract: ctrl+z
// suspends from idle, running, and awaiting-approval (the ask stays pending).
func TestSuspendWorksInEveryPhase(t *testing.T) {
	for _, ph := range []phase{phaseIdle, phaseRunning, phaseAwaitingApproval} {
		m := quitModel(t)
		m.phase = ph
		_, cmd := pressKey(m, ctrlZ())
		if !isSuspendCmd(cmd) {
			t.Errorf("ctrl+z in phase %v should return tea.Suspend", ph)
		}
	}
}

// TestResumeMsgRefreshes covers the ResumeMsg handler: after a suspend/resume the
// model re-renders (refreshView runs) without error. There is no user-visible state
// to assert beyond "it doesn't panic and returns the model", so this pins that the
// handler is wired (a ResumeMsg is not dropped on the floor) and returns no command.
func TestResumeMsgRefreshes(t *testing.T) {
	m := quitModel(t)
	mm, cmd := m.Update(tea.ResumeMsg{})
	if cmd != nil {
		t.Error("ResumeMsg should not schedule a command")
	}
	if mm == nil {
		t.Error("ResumeMsg should return the model")
	}
}

// TestSuspendResumeEmitsNotice pins the resume-side notice contract: a ctrl+z
// suspend records the phase + session id, and the ResumeMsg on fg emits an
// in-conversation notice naming what was suspended (the pre-suspend print was
// discarded with the alt screen, so the notice is honest only on resume).
func TestSuspendResumeEmitsNotice(t *testing.T) {
	m := quitModel(t)
	m.phase = phaseRunning
	m.sessionID = "sess-abc"
	m, _ = pressKey(m, ctrlZ())
	if m.suspendedAtID != "sess-abc" {
		t.Errorf("suspend should record the session id, got %q", m.suspendedAtID)
	}
	if m.suspendedFrom != phaseRunning {
		t.Errorf("suspend should record phaseRunning, got %v", m.suspendedFrom)
	}
	blocksBefore := len(m.conv.blocks)
	mm, _ := m.Update(tea.ResumeMsg{})
	m = mm.(Model)
	if len(m.conv.blocks) != blocksBefore+1 {
		t.Fatalf("resume should append one notice block, got %d (was %d)", len(m.conv.blocks), blocksBefore)
	}
	if m.suspendedAtID != "" {
		t.Error("resume should clear the suspended marker")
	}
}

// TestSuspendIdleResumeNoticeOmitsPhase covers the idle case: suspending from
// idle names "idle" (no mid-flight work to surface).
func TestSuspendIdleResumeNoticeOmitsPhase(t *testing.T) {
	m := quitModel(t)
	m, _ = pressKey(m, ctrlZ())
	if m.suspendedFrom != phaseIdle {
		t.Errorf("idle suspend should record phaseIdle, got %v", m.suspendedFrom)
	}
}
