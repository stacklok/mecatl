package ui

import (
	"strings"
	"testing"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

func TestContextualGuardrailApprovalChoicesAndCoverage(t *testing.T) {
	th := theme.New("aztec", theme.AztecPalette())
	hk := helpKeys{allow: "a", allowAlways: "w", deny: "d"}
	result := pendingAsk{guardrail: &client.GuardrailApprovalScope{Kind: "result_release"}, focusedVerdict: client.VerdictAllowOnce}
	buttons := approvalButtons(th, hk, result, false)
	if len(buttons) != 2 || !strings.Contains(strings.ToLower(buttons[0].label), "release once") || !strings.Contains(strings.ToLower(buttons[1].label), "cancel") {
		t.Fatalf("result-release buttons = %#v", buttons)
	}
	action := pendingAsk{guardrail: &client.GuardrailApprovalScope{Kind: "action", RepeatAvailable: true}, offerAlways: true, focusedVerdict: client.VerdictAllowOnce}
	buttons = approvalButtons(th, hk, action, false)
	if len(buttons) != 3 || !strings.Contains(strings.ToLower(buttons[0].label), "run once") || !strings.Contains(strings.ToLower(buttons[1].label), "don't ask again") || !strings.Contains(strings.ToLower(buttons[2].label), "cancel") {
		t.Fatalf("action buttons = %#v", buttons)
	}
	notice := guardrailCoverageNotice(client.GuardrailCoverage{Enabled: true, CheckerProviderID: "p", CheckerModelID: "m", Entries: []client.GuardrailCoverageEntry{{Tool: "Read", Phase: "post", Job: "inbound", Mode: "block", Inspection: "operational_failure", RuleOrigin: "default", RuleID: "Read"}}})
	if !strings.Contains(notice, "checker p/m") || !strings.Contains(notice, "checker outage (not an unsafe finding)") {
		t.Fatalf("coverage notice = %q", notice)
	}
	outage := guardrailHookText(client.HookMsg{Tool: "Read", Guardrail: &client.GuardrailReview{Job: "inbound", Assessment: "unresolved", Inspection: "operational_failure", Disposition: "withhold_result"}})
	unresolved := guardrailHookText(client.HookMsg{Tool: "Read", Guardrail: &client.GuardrailReview{Job: "inbound", Assessment: "unresolved", Inspection: "complete", Disposition: "pass_advisory"}})
	finding := guardrailHookText(client.HookMsg{Tool: "Read", Guardrail: &client.GuardrailReview{Job: "inbound", Assessment: "prohibited", Inspection: "complete", Disposition: "withhold_result"}})
	if !strings.Contains(outage, "outage") || !strings.Contains(unresolved, "completed unresolved") || !strings.Contains(finding, "security finding") {
		t.Fatalf("review states outage=%q unresolved=%q finding=%q", outage, unresolved, finding)
	}
}
