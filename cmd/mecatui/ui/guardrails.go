package ui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/internal/terminaltext"
)

func (m Model) runGuardrails() (tea.Model, tea.Cmd) {
	if m.deps.Guardrails == nil || m.sessionID == "" {
		return m, nil
	}
	m.guardrailStatusRequest++
	return m, client.ListGuardrailCoverageCmd(m.deps.Ctx, m.deps.Guardrails, m.sessionID, m.guardrailStatusRequest, false)
}

func benignGuardrailReview(review *client.GuardrailReview) bool {
	if review == nil || review.Inspection != "complete" || review.Assessment != "acceptable" {
		return false
	}
	return review.Disposition == "execute" || review.Disposition == "release_result"
}

func guardrailDetailNotice(detail client.GuardrailReviewDetail) string {
	parts := []string{"Guardrail detail"}
	if detail.Concern != "" {
		parts = append(parts, "Concern: "+terminaltext.Sanitize(detail.Concern))
	}
	if detail.SourceDisplay != "" {
		parts = append(parts, "Source: "+terminaltext.Sanitize(detail.SourceDisplay))
	}
	if detail.NextAction != "" {
		parts = append(parts, "Next: "+terminaltext.Sanitize(detail.NextAction))
	}
	return strings.Join(parts, " · ")
}

func guardrailHookText(msg client.HookMsg) string {
	review := msg.Guardrail
	if review == nil {
		return msg.Text
	}
	label := "inspection completed"
	switch {
	case review.Inspection == "operational_failure":
		label = "inspection outage (no unsafe finding inferred)"
	case review.Assessment == "prohibited":
		label = "security finding"
	case review.Assessment == "unresolved":
		label = "inspection completed unresolved (not an unsafe finding)"
	case review.Assessment == "acceptable":
		label = "inspection completed acceptable"
	}
	route := ""
	if review.CheckerProviderID != "" || review.CheckerModelID != "" {
		route = " · checker " + review.CheckerProviderID + "/" + review.CheckerModelID
	}
	return fmt.Sprintf("Guardrail %s: %s %s · %s%s", label, review.Job, msg.Tool, review.Disposition, route)
}

func guardrailCoverageCurrent(msg client.GuardrailCoverageMsg, sessionID string, requestID uint64) bool {
	return msg.SessionID == sessionID && msg.RequestID == requestID
}

func guardrailPostureSummary(coverage client.GuardrailCoverage) string {
	if !coverage.Enabled {
		return "checker off (permission posture is independent); configure models.slots.guardrail or --guardrails-model to enable contextual inspection"
	}
	if len(coverage.Entries) == 0 {
		return fmt.Sprintf("checker configured but no effective rule coverage; use /guardrails and verify rule matches against the session tool catalog · %s/%s", terminaltext.Sanitize(coverage.CheckerProviderID), terminaltext.Sanitize(coverage.CheckerModelID))
	}
	blocking, advisory := 0, 0
	for _, entry := range coverage.Entries {
		if entry.Mode == "block" {
			blocking++
		} else {
			advisory++
		}
	}
	var mode string
	switch {
	case blocking > 0 && advisory > 0:
		mode = fmt.Sprintf("mixed: %d enforcing, %d advisory", blocking, advisory)
	case blocking > 0:
		mode = fmt.Sprintf("enforcing: %d", blocking)
	default:
		mode = fmt.Sprintf("advisory: %d", advisory)
	}
	return fmt.Sprintf("checker on (%s) · %s/%s", mode, terminaltext.Sanitize(coverage.CheckerProviderID), terminaltext.Sanitize(coverage.CheckerModelID))
}

func guardrailCoverageNotice(coverage client.GuardrailCoverage) string {
	if !coverage.Enabled {
		return "Guardrails: off for this session. No contextual action or inbound inspection is configured."
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Guardrails: on · checker %s/%s\n", terminaltext.Sanitize(coverage.CheckerProviderID), terminaltext.Sanitize(coverage.CheckerModelID))
	if len(coverage.Entries) == 0 {
		b.WriteString("No effective rules apply to this session's assembled tool catalog. Check /guardrails and verify configured rule matches against the tool names and phases shown for this session.")
		return b.String()
	}
	for _, entry := range coverage.Entries {
		status := entry.Inspection
		if status == "operational_failure" {
			status = "checker outage (not an unsafe finding)"
		}
		if status == "" || status == "unknown" {
			status = "not yet checked"
		}
		fmt.Fprintf(&b, "• %s %s (%s): %s · %s · rule %s/%s", terminaltext.Sanitize(entry.Tool), terminaltext.Sanitize(entry.Phase), terminaltext.Sanitize(entry.Job), terminaltext.Sanitize(entry.Mode), terminaltext.Sanitize(status), terminaltext.Sanitize(entry.RuleOrigin), terminaltext.Sanitize(entry.RuleID))
		if entry.Reason != "" {
			fmt.Fprintf(&b, " — %s", terminaltext.Sanitize(entry.Reason))
		}
		b.WriteByte('\n')
	}
	return strings.TrimSpace(b.String())
}
