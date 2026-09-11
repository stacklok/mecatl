package agent

import (
	"encoding/json"
	"testing"

	"github.com/stacklok/mecatl/engine/session"
)

// TestResolveChildAskBothBitsTrueFailsSafe pins the DevEx-A fail-safe ordering:
// the two config decision bits are mutually exclusive BY CONSTRUCTION (the
// evaluator never sets both), but they ride a PendingAsk that crosses run
// boundaries and is externally reachable. resolveChildAsk gates ConfiguredAsk
// FIRST, so the illegal both-true state fails SAFE — a headless child
// AUTO-DENIES it (never auto-approves it via the FlooredConfiguredAllow path).
func TestResolveChildAskBothBitsTrueFailsSafe(t *testing.T) {
	child := &Run{asks: newAskRegistry()}
	const askID = "ask-1"
	ch := child.asks.register(askID)

	// Both bits true (the illegal state) + a HEADLESS posture (no surfaceAsk):
	// step 0 (ConfiguredAsk) must win → fall through to headless auto-deny.
	ask := session.PendingAsk{
		AskID:                  askID,
		Tool:                   "Shell",
		Args:                   json.RawMessage(`{"command":"go test $(git rev-parse HEAD)"}`),
		Reason:                 "approval required by rule for Shell (go test*)",
		ConfiguredAsk:          true,
		FlooredConfiguredAllow: true,
	}
	resolveChildAsk(child, ask, childPosture{isolated: true})

	select {
	case a := <-ch:
		if a.verdict != session.VerdictDeny {
			t.Fatalf("both-bits-true ask must fail SAFE (auto-deny), got verdict %v", a.verdict)
		}
	default:
		t.Fatalf("both-bits-true headless ask must resolve (auto-deny), but nothing was delivered — it likely auto-approved via the floored-allow path")
	}
}
