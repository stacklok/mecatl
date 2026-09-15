package ui

import (
	"errors"
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
	notYet := guardrailCoverageNotice(client.GuardrailCoverage{Enabled: true, CheckerProviderID: "p", CheckerModelID: "m", Entries: []client.GuardrailCoverageEntry{{Tool: "Read", Phase: "post", Job: "inbound", Mode: "block"}}})
	if !strings.Contains(notYet, "not yet checked") || strings.Contains(notYet, "unavailable") {
		t.Fatalf("not-yet-checked coverage = %q", notYet)
	}
	outage := guardrailHookText(client.HookMsg{Tool: "Read", Guardrail: &client.GuardrailReview{Job: "inbound", Assessment: "unresolved", Inspection: "operational_failure", Disposition: "withhold_result"}})
	unresolved := guardrailHookText(client.HookMsg{Tool: "Read", Guardrail: &client.GuardrailReview{Job: "inbound", Assessment: "unresolved", Inspection: "complete", Disposition: "pass_advisory"}})
	finding := guardrailHookText(client.HookMsg{Tool: "Read", Guardrail: &client.GuardrailReview{Job: "inbound", Assessment: "prohibited", Inspection: "complete", Disposition: "withhold_result"}})
	if !strings.Contains(outage, "outage") || !strings.Contains(unresolved, "completed unresolved") || !strings.Contains(finding, "security finding") {
		t.Fatalf("review states outage=%q unresolved=%q finding=%q", outage, unresolved, finding)
	}
}

func TestGuardrailDetailFailureIsVisibleWithoutInventingFinding(t *testing.T) {
	s := &approvalSurface{ask: pendingAsk{guardrail: &client.GuardrailApprovalScope{ReviewID: "r", Kind: "result_release"}}}
	_, handled, _ := s.HandleMsg(client.GuardrailReviewDetailMsg{Err: errors.New("expired private detail")})
	if !handled || !s.ask.reviewDetailUnavailable || s.ask.reviewDetail.Concern != "" {
		t.Fatalf("detail failure state = %+v handled=%v", s.ask, handled)
	}
	var b strings.Builder
	writeGuardrailApprovalDetail(&b, theme.New("aztec", theme.AztecPalette()), s.ask, 80)
	got := b.String()
	if !strings.Contains(got, "unavailable or expired") || strings.Contains(got, "expired private detail") || strings.Contains(got, "Concern:") {
		t.Fatalf("detail failure rendering = %q", got)
	}
}
