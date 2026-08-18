package ui

// approval_invariants_test.go pins the three behaviors the frame goldens do NOT
// (issue #555 review — the QA/test-adequacy pass found these gaps): the
// fingerprint no-op-repopulation guard, the nil-stream /debug-ask resolve path,
// and the phase⟺ask biconditional across every transition. These are the axes a
// "pure migration" could silently break while the frame goldens stay green.

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

// TestPlanReviewNoOpRepopulationPreservesOffset pins the planVP fingerprint
// guard: a re-population with the SAME ask + SAME geometry short-circuits (no
// SetContent, no re-wrap), so the operator's reading position is untouched. A
// refactor that dropped the fingerprint from the guard would re-SetContent and
// re-clamp the offset — invisible to a single-frame golden.
func TestPlanReviewNoOpRepopulationPreservesOffset(t *testing.T) {
	m := planAskModel(t, true)
	// planAskModel populated planVP at (m.width, m.vp.Height()). Scroll down.
	m.approval.planVP.SetYOffset(5)
	before := m.approval.planVP.YOffset()
	// Re-populate with the SAME ask, SAME geometry → must be a no-op.
	m.openPlanReviewView(m.approval.ask, 0, m.effectiveModel.ModelID)
	if got := m.approval.planVP.YOffset(); got != before {
		t.Errorf("no-op repopulation moved YOffset: got %d, want %d (guard dropped?)", got, before)
	}
	// A fingerprint/geometry change MUST re-populate (the guard fires).
	m.approval.planVP.SetYOffset(0)
	m.openPlanReviewView(m.approval.ask, 3, m.effectiveModel.ModelID) // queued differs
	if m.approval.planVPFingerprint != planAskFingerprint(m.approval.ask, 3, m.effectiveModel.ModelID) {
		t.Error("changed queued count must re-stamp the fingerprint (re-population happened)")
	}
}

// TestArgsViewRawToggleRepopulates pins the rawBit half of argsAskFingerprint:
// with the args view open, toggling RawArgs (r) changes the fingerprint and
// RE-populates (the raw tier appears) — and the offset is preserved across the
// re-population, not yanked to the top.
func TestArgsViewRawToggleRepopulates(t *testing.T) {
	m := openArgsView(t, bashAskModel(t, longBashArgs))
	if m.approval.argsViewRaw {
		t.Fatal("precondition: args view opens pretty (argsViewRaw false)")
	}
	m.approval.argsVP.SetYOffset(3)
	before := m.approval.argsVP.YOffset()
	prettyFP := m.approval.argsVPFingerprint

	// Toggle to raw and re-populate (the reducer path: RawArgs key →
	// argsViewRaw flips → openAskArgsView re-populates because the fingerprint
	// changed).
	m.approval.argsViewRaw = true
	m.openAskArgsView(m.approval.ask, len(m.approval.queue))
	if m.approval.argsVPFingerprint == prettyFP {
		t.Error("raw toggle must change the fingerprint (re-populate), still matches pretty")
	}
	if m.approval.argsVPFingerprint != argsAskFingerprint(m.approval.ask, len(m.approval.queue), true) {
		t.Error("raw fingerprint must encode rawBit=1")
	}
	if got := m.approval.argsVP.YOffset(); got != before {
		t.Errorf("raw-toggle repopulation moved YOffset: got %d, want %d (reading position lost)", got, before)
	}
}

// TestDebugAskResolveNilStreamNoPanic pins the nil-stream guard: a /debug-ask
// opened from phaseIdle has no run stream, and resolving it must not panic —
// the sendApproval dep returns nil and the notice still lands. This is the one
// path the goldens cannot see (they never execute the returned cmd).
func TestDebugAskResolveNilStreamNoPanic(t *testing.T) {
	m := New(Deps{Theme: debugTheme()})
	m = applyAll(m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m.sessionID = "sess-debug-nil"
	m.stream = nil // a /debug-ask from phaseIdle has no live run stream
	m.phase = phaseAwaitingApproval
	m.approval.ask = pendingAsk{AskID: "sess-debug-nil:1:dbg", Tool: "Bash", Args: `{"command":"ls"}`, offerAlways: true}

	deps := (&m).approvalDeps()
	actions, adv := (&m.approval).resolveAsk(client.VerdictAllowOnce)
	cmd := (&m).dispatchApprovalActions(deps, actions)
	// Execute the returned send cmd: must not panic on the nil stream.
	if cmd != nil {
		_ = cmd() // a nil-stream send returns nil, not a panic
	}
	if adv.hasNext {
		t.Error("single ask must drain to empty")
	}
	// The notice action still landed (deps.notice fired synchronously).
	if got := lastNotice(m); got != "permission allowed" {
		t.Errorf("nil-stream resolve notice = %q, want %q", got, "permission allowed")
	}
}

// TestPhaseAskIDBiconditional is the executable oracle for the documented
// invariant `phase == phaseAwaitingApproval ⟺ m.approval.ask.AskID != ""`,
// swept across every transition the modal drives. The arch gate is
// placement-only; this is the behavioral pin.
func TestPhaseAskIDBiconditional(t *testing.T) {
	check := func(m Model, where string) {
		t.Helper()
		open := m.approval.ask.AskID != ""
		inAwaiting := m.phase == phaseAwaitingApproval
		if open != inAwaiting {
			t.Errorf("%s: invariant broken — phase=%v, ask.AskID=%q (open=%v, awaiting=%v)",
				where, m.phase, m.approval.ask.AskID, open, inAwaiting)
		}
	}

	// OPEN: an ask opens → phase awaiting, ask set.
	m := approvalModel(t, pendingAsk{AskID: askA, Tool: "Bash", Args: `{"command":"ls"}`})
	mm, _ := m.applyPermissionAsk(client.PermissionAskMsg{AskID: askB, Tool: "Bash", Args: `{"command":"pwd"}`})
	m = mm.(Model)
	check(m, "after applyPermissionAsk open")
	mm, _ = m.applyPermissionAsk(client.PermissionAskMsg{AskID: askC, Tool: "Bash", Args: `{"command":"date"}`})
	m = mm.(Model)
	check(m, "after enqueue (second ask)")

	// RESOLVE one → a successor pops (still awaiting, still an ask).
	m, _ = pressKey(m, tea.KeyPressMsg{Code: 'a', Text: "a"})
	check(m, "after resolve first (successor visible)")

	// RESOLVE the last → queue drains, phase resumes running, ask cleared.
	m, _ = pressKey(m, tea.KeyPressMsg{Code: 'a', Text: "a"})
	check(m, "after resolve last (drained)")

	// RETRACT a visible ask with a queued successor.
	m2, _ := queuedAskModel(t)
	check(m2, "queued model (two asks)")
	mm2, _ := m2.Update(client.PermissionRetractMsg{AskID: m2.approval.ask.AskID})
	m2 = mm2.(Model)
	check(m2, "after retract visible (successor visible)")
}

// debugTheme is the minimal theme the nil-stream test needs (no palette lookup).
func debugTheme() theme.Theme { return theme.New("aztec", theme.AztecPalette()) }
