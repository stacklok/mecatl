package agent

import (
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/stacklok/mecatl/engine/session"
)

func TestADR_0342_ContextualGuardrails_Scenario6_ImplementationCalibration(t *testing.T) {
	if defaultReviewEvidenceHandles != 16 || defaultReviewEvidenceBytes != 400_000 || defaultReviewTrajectoryFacts != 256 || defaultReviewTrajectoryBytes != 128_000 || maxHeldResults != 32 || maxHeldResultBytes != 2*1024*1024 {
		t.Fatalf("private capacities changed without calibration: evidence=%d/%d trajectory=%d/%d held=%d/%d", defaultReviewEvidenceHandles, defaultReviewEvidenceBytes, defaultReviewTrajectoryFacts, defaultReviewTrajectoryBytes, maxHeldResults, maxHeldResultBytes)
	}

	root := newReviewRoot(nil, nil)
	for i := 0; i < defaultReviewTrajectoryFacts; i++ {
		root.record(ReviewTrajectoryFact{Call: session.ToolCallID(fmt.Sprintf("call-%03d", i)), Ref: fmt.Sprintf("ref-%03d", i), Direction: "outbound", DataClass: "external_send", TargetID: strings.Repeat("t", 300), Decision: "acceptable"})
	}
	facts, complete := root.snapshot()
	if complete || len(facts) > defaultReviewTrajectoryFacts || root.bytes > defaultReviewTrajectoryBytes {
		t.Fatalf("trajectory exhaustion did not fail incomplete within capacity: complete=%v facts=%d bytes=%d", complete, len(facts), root.bytes)
	}

	result := session.NewToolResult("held", strings.Repeat("x", int(maxHeldResultBytes)))
	key := heldResultKey{reviewID: "r", session: "s", call: "held"}
	if !root.holdResult(key, result) {
		t.Fatal("exact held-result byte capacity was rejected")
	}
	before := root.heldBytes
	if root.holdResult(heldResultKey{reviewID: "overflow", session: "s", call: "overflow"}, session.NewToolResult("overflow", "x")) {
		t.Fatal("aggregate held-result exhaustion was admitted")
	}
	if root.heldBytes != before || len(root.held) != 1 {
		t.Fatalf("rejected hold allocated retained state: bytes=%d entries=%d", root.heldBytes, len(root.held))
	}

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			root.clearHeld()
		}()
	}
	wg.Wait()
	if root.heldBytes != 0 || len(root.held) != 0 {
		t.Fatalf("cancellation cleanup retained held results: bytes=%d entries=%d", root.heldBytes, len(root.held))
	}
}

func BenchmarkContextualReviewPrivateCapacity(b *testing.B) {
	b.Run("trajectory-exhaustion", func(b *testing.B) {
		b.ReportAllocs()
		for n := 0; n < b.N; n++ {
			root := newReviewRoot(nil, nil)
			for i := 0; i <= defaultReviewTrajectoryFacts; i++ {
				root.record(ReviewTrajectoryFact{Call: session.ToolCallID(fmt.Sprintf("call-%03d", i)), Ref: fmt.Sprintf("ref-%03d", i), Direction: "outbound", DataClass: "external_send", TargetID: strings.Repeat("t", 300), Decision: "acceptable"})
			}
			if _, complete := root.snapshot(); complete {
				b.Fatal("trajectory stayed complete over capacity")
			}
		}
	})
	b.Run("held-result-fill-clear", func(b *testing.B) {
		b.ReportAllocs()
		payload := strings.Repeat("x", int(maxHeldResultBytes/maxHeldResults))
		for n := 0; n < b.N; n++ {
			root := newReviewRoot(nil, nil)
			for i := 0; i < maxHeldResults; i++ {
				id := session.ToolCallID(fmt.Sprintf("call-%02d", i))
				if !root.holdResult(heldResultKey{reviewID: string(id), session: "s", call: id}, session.NewToolResult(id, payload)) {
					b.Fatal("held-result capacity rejected calibrated workload")
				}
			}
			root.clearHeld()
		}
	})
}
