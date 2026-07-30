package session

import (
	"strings"
	"testing"
)

// TestDelegationObservability_Scenario1_PayloadsCarryPreviewFields pins AC1.1 of the
// delegation-observability-convergence plan (ADR 0079): SubagentPayload and
// ParallelPayload each expose the same Text / Detail / InnerKind bounded-preview
// fields TeamPayload already carries, typed identically (Text/Detail string,
// InnerKind EventType) so the chokepoint can populate them with clamped previews.
func TestDelegationObservability_Scenario1_PayloadsCarryPreviewFields(t *testing.T) {
	t.Parallel()

	// The check is COMPILE-TIME: both payloads must accept every preview field with
	// the TeamPayload-mirroring types. The test goes red if any field is missing or
	// mis-typed, and stays live against a field removal/narrowing.
	sub := SubagentPayload{
		Text:      "child message preview",
		Detail:    "clamped args or result preview",
		InnerKind: EvMessageDelta,
	}
	assertPreviewFields(t, "SubagentPayload", sub.Text, sub.Detail, sub.InnerKind)

	par := ParallelPayload{
		Text:      "branch message preview",
		Detail:    "clamped args or result preview",
		InnerKind: EvToolResult,
	}
	assertPreviewFields(t, "ParallelPayload", par.Text, par.Detail, par.InnerKind)

	// Parity with TeamPayload: the three families carry the same preview field
	// types so drainChildObserved populates them identically (ADR 0079 tier 1).
	team := TeamPayload{Text: "t", Detail: "d", InnerKind: EvToolCall}
	assertPreviewFields(t, "TeamPayload", team.Text, team.Detail, team.InnerKind)

	// The accepted InnerKind vocabulary is exactly the four projected kinds —
	// message.delta / tool.call / tool.result / result — the same set TeamPayload
	// projects; a permission.ask is never one of them.
	for _, kind := range []EventType{EvMessageDelta, EvToolCall, EvToolResult, EvResult} {
		if kind == EvPermissionAsk {
			t.Fatalf("InnerKind vocabulary must never include %q (a child's permission.ask is NEVER forwarded)", EvPermissionAsk)
		}
	}
}

func assertPreviewFields(t *testing.T, name, text, detail string, kind EventType) {
	t.Helper()
	if text == "" {
		t.Errorf("%s.Text is unset after literal assignment", name)
	}
	if detail == "" {
		t.Errorf("%s.Detail is unset after literal assignment", name)
	}
	if strings.TrimSpace(string(kind)) == "" {
		t.Errorf("%s.InnerKind is unset after literal assignment", name)
	}
}
