package ui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
)

func (m Model) runGuardrails() (tea.Model, tea.Cmd) {
	if m.deps.Guardrails == nil || m.sessionID == "" {
		return m, nil
	}
	return m, client.ListGuardrailCoverageCmd(m.deps.Ctx, m.deps.Guardrails, m.sessionID)
}

func guardrailDetailNotice(detail client.GuardrailReviewDetail) string {
	parts := []string{"Guardrail detail"}
	if detail.Concern != "" {
		parts = append(parts, "Concern: "+sanitizeTerminal(detail.Concern))
	}
	if detail.SourceDisplay != "" {
		parts = append(parts, "Source: "+sanitizeTerminal(detail.SourceDisplay))
	}
	if detail.NextAction != "" {
		parts = append(parts, "Next: "+sanitizeTerminal(detail.NextAction))
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

func guardrailCoverageNotice(coverage client.GuardrailCoverage) string {
	if !coverage.Enabled {
		return "Guardrails: off for this session. No contextual action or inbound inspection is configured."
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Guardrails: on · checker %s/%s\n", sanitizeTerminal(coverage.CheckerProviderID), sanitizeTerminal(coverage.CheckerModelID))
	if len(coverage.Entries) == 0 {
		b.WriteString("No effective rules apply to this session's assembled tool catalog.")
		return b.String()
	}
	for _, entry := range coverage.Entries {
		status := entry.Inspection
		if status == "operational_failure" {
			status = "checker outage (not an unsafe finding)"
		}
		if status == "" || status == "unknown" {
			status = "health unavailable"
		}
		fmt.Fprintf(&b, "• %s %s (%s): %s · %s · rule %s/%s", sanitizeTerminal(entry.Tool), sanitizeTerminal(entry.Phase), sanitizeTerminal(entry.Job), sanitizeTerminal(entry.Mode), sanitizeTerminal(status), sanitizeTerminal(entry.RuleOrigin), sanitizeTerminal(entry.RuleID))
		if entry.Reason != "" {
			fmt.Fprintf(&b, " — %s", sanitizeTerminal(entry.Reason))
		}
		b.WriteByte('\n')
	}
	return strings.TrimSpace(b.String())
}
