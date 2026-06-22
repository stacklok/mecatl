package agent

import (
	"encoding/json"
	"testing"

	"github.com/stacklok/mecatl/engine/session"
)

// TestWritableChildPostureNotIsolated pins the ADR 0041 security-relevant change: a
// direct-write (mode:"read-write") child is NOT isolated (it shares the REAL parent
// tree), so the A2 isolation auto-approve (governance.IsolationApprovable) must NOT
// fire for its Bash. The test drives resolveChildAsk with an isolation-APPROVABLE Bash
// substitution under both postures:
//   - isolated:true  → A2 auto-approves (the read-only worktree path).
//   - isolated:false → A2 is skipped; a headless child falls through to auto-deny.
//
// It is the behavioral guard for `posture := childPosture{isolated: !writable && t.childForker != nil ...}`
// in run() — dropping the `!writable &&` guard (so a writable child reads isolated:true
// off a wired forker) makes the second case auto-APPROVE, failing this test.
func TestWritableChildPostureNotIsolated(t *testing.T) {
	const cmd = `{"command":"go test ./..."}` // plain isolation-approvable; A2-eligible only when isolated

	mkAsk := func(askID string) session.PendingAsk {
		return session.PendingAsk{
			AskID:  askID,
			Tool:   "Bash",
			Args:   json.RawMessage(cmd),
			Reason: "approval required for Bash",
		}
	}

	t.Run("isolated read-only child A2 auto-approves", func(t *testing.T) {
		child := &Run{asks: newAskRegistry()}
		ch := child.asks.register("a1")
		resolveChildAsk(child, mkAsk("a1"), childPosture{isolated: true})
		select {
		case a := <-ch:
			if a.verdict != session.VerdictAllowOnce {
				t.Fatalf("isolated child's isolation-approvable Bash must A2 auto-approve, got %v", a.verdict)
			}
		default:
			t.Fatal("isolated child ask did not resolve (expected A2 auto-approve)")
		}
	})

	t.Run("non-isolated writable child does not A2 auto-approve (headless auto-deny)", func(t *testing.T) {
		child := &Run{asks: newAskRegistry()}
		ch := child.asks.register("a2")
		// isolated:false + headless (no surfaceAsk, no adjudicate) → must NOT
		// auto-approve; falls through to the plain headless auto-deny.
		resolveChildAsk(child, mkAsk("a2"), childPosture{isolated: false})
		select {
		case a := <-ch:
			if a.verdict != session.VerdictDeny {
				t.Fatalf("a NON-isolated (direct-write) child's Bash must NOT A2 auto-approve; "+
					"a headless child must auto-deny, got %v", a.verdict)
			}
		default:
			t.Fatal("non-isolated headless child ask did not resolve")
		}
	})
}
