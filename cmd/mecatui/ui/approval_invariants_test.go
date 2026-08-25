package ui

// approval_invariants_test.go pins the three behaviors the frame goldens do NOT
// (issue #555 review — the QA/test-adequacy pass found these gaps): the
// fingerprint no-op-repopulation guard, the nil-stream /debug-ask resolve path,
// and the phase⟺ask biconditional across every transition. These are the axes a
// "pure migration" could silently break while the frame goldens stay green.

import (
	"strings"
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
	_ = m.View()
	s := approvalSurfaceOf(t, m)
	s.planVP.SetYOffset(5)
	before := s.planVP.YOffset()
	_ = m.View()
	if got := s.planVP.YOffset(); got != before {
		t.Errorf("rendering the same plan moved YOffset: got %d, want %d", got, before)
	}
	m = applyAll(m, client.PermissionAskMsg{AskID: "sess-test-0001:2:queued", Tool: "Bash"})
	if got := stripANSIstr(m.View().Content); !strings.Contains(got, "Plan ready for review (1 of 2)") {
		t.Errorf("queue update did not refresh the plan frame: %q", got)
	}
}

// TestArgsViewRawToggleRepopulates pins the rawBit half of argsAskFingerprint:
// with the args view open, toggling RawArgs (r) changes the fingerprint and
// RE-populates (the raw tier appears) — and the offset is preserved across the
// re-population, not yanked to the top.
func TestArgsViewRawToggleRepopulates(t *testing.T) {
	m := openArgsView(t, bashAskModel(t, longBashArgs))
	before := approvalSurfaceOf(t, m).argsVP.YOffset()
	m, _ = pressKey(m, tea.KeyPressMsg{Code: 'r', Text: "r"})
	raw := stripANSIstr(m.View().Content)
	if !strings.Contains(raw, `{"command":"find . -name`) {
		t.Errorf("raw toggle did not refresh the rendered args frame: %q", raw)
	}
	if got := approvalSurfaceOf(t, m).argsVP.YOffset(); got != before {
		t.Errorf("raw-toggle render moved YOffset: got %d, want %d", got, before)
	}
}

// TestDebugAskResolveNilStreamNoPanic pins the nil-stream guard: a /debug-ask
// opened from phaseIdle has no run stream, and resolving it must not panic —
// Model emits no send command while the transcript notice still lands. This is the one
// path the goldens cannot see (they never execute the returned cmd).
func TestDebugAskResolveNilStreamNoPanic(t *testing.T) {
	m := New(Deps{Theme: debugTheme()})
	m = applyAll(m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m.sessionID = "sess-debug-nil"
	m.stream = nil // a /debug-ask from phaseIdle has no live run stream
	m.phase = phaseIdle
	m = applyAll(m, client.PermissionAskMsg{AskID: "sess-debug-nil:1:dbg", Tool: "Bash", Args: `{"command":"ls"}`})
	m, cmd := pressKey(m, tea.KeyPressMsg{Code: 'a', Text: "a"})
	// Execute the returned send cmd: must not panic on the nil stream.
	if cmd != nil {
		_ = cmd() // a nil-stream send returns nil, not a panic
	}
	// The notice action still landed (deps.notice fired synchronously).
	if got := lastNotice(m); got != "permission allowed" {
		t.Errorf("nil-stream resolve notice = %q, want %q", got, "permission allowed")
	}
}

// TestPhaseAskIDBiconditional is the executable oracle for the documented
// invariant `phase == phaseAwaitingApproval ⟺ approvalSurfaceOf(t, m).ask.AskID != ""`,
// swept across every transition the modal drives. The arch gate is
// placement-only; this is the behavioral pin.
func TestPhaseAskIDBiconditional(t *testing.T) {
	check := func(m Model, where string) {
		t.Helper()
		open := m.modal != nil
		inAwaiting := m.phase == phaseAwaitingApproval
		if open != inAwaiting {
			t.Errorf("%s: invariant broken — phase=%v, modal=%T (open=%v, awaiting=%v)",
				where, m.phase, m.modal, open, inAwaiting)
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
	mm2, _ := m2.Update(client.PermissionRetractMsg{AskID: approvalSurfaceOf(t, m2).ask.AskID})
	m2 = mm2.(Model)
	check(m2, "after retract visible (successor visible)")
}

// debugTheme is the minimal theme the nil-stream test needs (no palette lookup).
func debugTheme() theme.Theme { return theme.New("aztec", theme.AztecPalette()) }
