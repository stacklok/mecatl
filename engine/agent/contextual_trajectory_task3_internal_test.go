package agent

import (
	"fmt"
	"testing"

	"github.com/stacklok/mecatl/engine/session"
)

func TestADR_0342_ContextualGuardrails_Scenario4_Retention(t *testing.T) {
	root := newReviewRoot(nil, nil, nil)
	root.maxFacts = 2
	root.maxBytes = 256
	one := ReviewTrajectoryFact{Call: "one", Ref: "one", Direction: "local", DataClass: "sensitive_read", TargetID: "a", Decision: "acceptable"}
	root.record(one)
	root.record(one) // exact duplicate is authorization-equivalent and compactable.
	root.record(ReviewTrajectoryFact{Call: "two", Ref: "two", Direction: "outbound", DataClass: "external_send", TargetID: "b", Decision: "denied"})
	root.record(ReviewTrajectoryFact{Call: "three", Ref: "three", Direction: "worker", DataClass: "delegation", TargetID: "c", Decision: "acceptable"})
	facts, complete := root.snapshot()
	if complete || len(facts) != 2 {
		t.Fatalf("complete=%v facts=%+v", complete, facts)
	}
	if got := fmt.Sprint(facts); got == "" || facts[1].Decision != "denied" {
		t.Fatalf("bounded metadata lost retained authorization decision: %s", got)
	}
	// The incomplete marker is sticky and no further body is allocated.
	root.record(ReviewTrajectoryFact{Call: session.ToolCallID("four"), Ref: string(make([]byte, 1024))})
	again, stillComplete := root.snapshot()
	if stillComplete || len(again) != len(facts) {
		t.Fatalf("post-exhaustion retention changed: complete=%v facts=%d", stillComplete, len(again))
	}
}
