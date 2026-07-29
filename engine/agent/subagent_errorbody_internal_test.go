package agent

import (
	"strings"
	"testing"
)

// errorBodyFloor is the caller-NEUTRAL floor subagentErrorBody falls to when neither
// half carries anything. It is deliberately noun-free: the Subagent path prefixes it
// with "Subagent: " and the Parallel path renders it under a "=== branch-N [FAILED] ==="
// header, so a noun from either caller would be the other's vocabulary.
const errorBodyFloor = "failed without producing a summary"

// TestSubagentErrorBodyPrefersCause is the ORACLE for subagentErrorBody — the single
// chokepoint that composes a StopError delegation's model-facing body (issue #319).
// The whole point of the fix is precedence: the harness/provider CAUSE leads because it
// is the actionable half; the child's last assistant text follows as clamped CONTEXT,
// never AS the failure reason. The empty-cause rows pin that nothing regresses for the
// terminals that carry no loop cause.
//
// The argument order is (cause, final) — the order they RENDER — so a call site reads as
// its own output. Both are plain strings, so a positional swap compiles and silently
// reproduces the very bug #319 fixed; the first row below is what catches it.
func TestSubagentErrorBodyPrefersCause(t *testing.T) {
	t.Parallel()
	const cause = "provider stream failed: 503 upstream unavailable"
	const final = "Now let me check the tests."

	tests := []struct {
		name  string
		cause string
		final string
		want  string
	}{
		{
			name:  "cause and final: cause leads, final follows as labelled context",
			cause: cause,
			final: final,
			want:  cause + "\n\nLast activity before the failure: " + final,
		},
		{
			name:  "cause only: the cause is the whole body",
			cause: cause,
			want:  cause,
		},
		{
			name:  "final only: today's shape, preserved for non-loop terminals",
			final: final,
			want:  final,
		},
		{
			name: "neither: the honest, caller-neutral floor",
			want: errorBodyFloor,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := subagentErrorBody(tc.cause, tc.final); got != tc.want {
				t.Fatalf("subagentErrorBody(%q, %q) =\n%q\nwant\n%q", tc.cause, tc.final, got, tc.want)
			}
		})
	}
}

// TestSubagentErrorBodyClampsBothHalves proves the composed body is BOUNDED on BOTH
// sides. This body is recorded into the PARENT's conversation and persisted, so the
// parent re-pays for every rune of it on every subsequent turn: an unbounded provider
// error body (an HTML error page, a giant JSON envelope) must not become permanent
// context, and neither must a runaway assistant message. The cause gets the larger
// maxSubagentCausePreview budget for the same reason the event payload does — a truncated
// provider error is unactionable — and the child's text the smaller
// maxSubagentFinalPreview one (a subagent-NAMED bound: the helper is the ONE place the
// StopError body is composed, so it must not read as borrowing the team tool's constant).
func TestSubagentErrorBodyClampsBothHalves(t *testing.T) {
	t.Parallel()
	hugeCause := "BOOM-" + strings.Repeat("x", maxSubagentCausePreview*4)
	hugeFinal := strings.Repeat("y", maxSubagentFinalPreview*4)

	got := subagentErrorBody(hugeCause, hugeFinal)
	const label = "\n\nLast activity before the failure: "
	if !strings.HasPrefix(got, "BOOM-") {
		t.Fatalf("cause must still lead, got %q", got)
	}
	idx := strings.Index(got, label)
	if idx < 0 {
		t.Fatalf("the labelled context half is missing, got %q", got)
	}
	// +1 on each bound for clampRunes' appended ellipsis.
	if n := len([]rune(got[:idx])); n > maxSubagentCausePreview+1 {
		t.Fatalf("cause was not clamped: %d runes, want <= %d", n, maxSubagentCausePreview+1)
	}
	tail := got[idx+len(label):]
	if n := len([]rune(tail)); n > maxSubagentFinalPreview+1 {
		t.Fatalf("final was not clamped: %d runes, want <= %d", n, maxSubagentFinalPreview+1)
	}
}

// TestSubagentErrorBodyTrimsWhitespaceOnlyInputs guards the degenerate case a real loop
// can produce: a StopError whose cause is empty and whose last assistant text is
// whitespace only. Without the trim the body would be a blank line — an opaque failure,
// the very thing renderSubagentResult's floor exists to prevent.
func TestSubagentErrorBodyTrimsWhitespaceOnlyInputs(t *testing.T) {
	t.Parallel()
	if got := subagentErrorBody("  ", "   \n\t "); got != errorBodyFloor {
		t.Fatalf("whitespace-only inputs must fall to the floor, got %q", got)
	}
}
