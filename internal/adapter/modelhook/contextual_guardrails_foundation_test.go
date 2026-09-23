package modelhook

import "testing"

func TestADR_0363_ContextualGuardrails_Scenario1_ReplacementRemoval(t *testing.T) {
	if _, ok := CompileRule(RuleSpec{Match: "Shell", Phases: []string{"pre"}, Mode: "sanitize"}); ok {
		t.Fatal("sanitize mode must be rejected rather than rewritten or mapped")
	}
}
