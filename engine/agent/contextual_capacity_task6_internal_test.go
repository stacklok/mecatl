package agent

import (
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/stacklok/mecatl/engine/session"
)

func TestPermissionAuthorizationBindingIsExact(t *testing.T) {
	call := session.NewToolCall("call", "Read", []byte(`{"path":"a"}`))
	env := session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "workspace", Revision: "r1"}
	auth := permissionAuthorization{call: call, env: env}
	if !auth.matches(call, env) {
		t.Fatal("exact permission binding did not match")
	}
	changedCall := session.NewToolCall("call", "Read", []byte(`{"path":"b"}`))
	if auth.matches(changedCall, env) {
		t.Fatal("changed call retained permission authorization")
	}
	changedEnv := env
	changedEnv.Revision = "r2"
	if auth.matches(call, changedEnv) {
		t.Fatal("changed environment retained permission authorization")
	}
}

func TestReviewTrajectoryFactBytesCountsEveryRetainedString(t *testing.T) {
	fields := []struct {
		name string
		fact ReviewTrajectoryFact
	}{
		{"call", ReviewTrajectoryFact{Call: "x"}},
		{"ref", ReviewTrajectoryFact{Ref: "x"}},
		{"direction", ReviewTrajectoryFact{Direction: "x"}},
		{"data class", ReviewTrajectoryFact{DataClass: "x"}},
		{"target", ReviewTrajectoryFact{TargetID: "x"}},
		{"decision", ReviewTrajectoryFact{Decision: "x"}},
		{"session", ReviewTrajectoryFact{SessionID: "x"}},
		{"tool", ReviewTrajectoryFact{Tool: "x"}},
		{"approval origin", ReviewTrajectoryFact{ApprovalOrigin: "x"}},
		{"approval kind", ReviewTrajectoryFact{ApprovalKind: "x"}},
		{"review", ReviewTrajectoryFact{ReviewID: "x"}},
	}
	for _, tc := range fields {
		t.Run(tc.name, func(t *testing.T) {
			if got := reviewTrajectoryFactBytes(tc.fact); got != 1 {
				t.Fatalf("bytes = %d, want 1", got)
			}
		})
	}
}

func TestReviewTrajectoryAggregateCapacityCountsMetadata(t *testing.T) {
	root := newReviewRoot(nil, nil, nil)
	root.maxBytes = 8
	root.record(ReviewTrajectoryFact{SessionID: "12345678"})
	root.record(ReviewTrajectoryFact{ReviewID: "x"})
	if root.bytes > root.maxBytes {
		t.Fatalf("retained bytes = %d, max = %d", root.bytes, root.maxBytes)
	}
	if _, complete := root.snapshot(); complete {
		t.Fatal("metadata-only overflow left trajectory complete")
	}
}

func TestADR_0363_ContextualGuardrails_Scenario6_ImplementationCalibration(t *testing.T) {
	if defaultReviewEvidenceHandles != 16 || defaultReviewEvidenceBytes != 400_000 || defaultReviewTrajectoryFacts != 256 || defaultReviewTrajectoryBytes != 128_000 || maxHeldResults != 32 || maxHeldResultBytes != 2*1024*1024 {
		t.Fatalf("private capacities changed without calibration: evidence=%d/%d trajectory=%d/%d held=%d/%d", defaultReviewEvidenceHandles, defaultReviewEvidenceBytes, defaultReviewTrajectoryFacts, defaultReviewTrajectoryBytes, maxHeldResults, maxHeldResultBytes)
	}

	root := newReviewRoot(nil, nil, nil)
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
			root := newReviewRoot(nil, nil, nil)
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
			root := newReviewRoot(nil, nil, nil)
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
