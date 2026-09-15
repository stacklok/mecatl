package lint_test

import (
	"os"
	"strings"
	"testing"
)

func TestADR_0342_ContextualGuardrails_Scenario7_ResourceInventory(t *testing.T) {
	data, err := os.ReadFile("../adr/0027-cloud-native.md")
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(string(data), "## List 2: rehydrate-fidelity ledger")
	if len(parts) != 2 {
		t.Fatalf("cloud-native inventory has %d List 2 headings", len(parts)-1)
	}
	for _, resource := range []string{
		"Contextual guardrail transient",
		"held-result escrow",
		"review evidence",
		"current-root trajectory",
		"exact repeat grant",
	} {
		if !strings.Contains(parts[0], resource) {
			t.Errorf("List 1 missing %q", resource)
		}
		if !strings.Contains(parts[1], resource) {
			t.Errorf("List 2 missing %q", resource)
		}
	}
	for _, lifecycle := range []string{"reset-by-design", "restart", "cleanup"} {
		if !strings.Contains(parts[0], lifecycle) || !strings.Contains(parts[1], lifecycle) {
			t.Errorf("both inventories must state lifecycle/reset behavior; missing %q", lifecycle)
		}
	}
}
