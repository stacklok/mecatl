package agent

import (
	"strings"
	"testing"
)

// TestSubagentErrorBodyPrefersCause is the ORACLE for subagentErrorBody — the single
// chokepoint that composes a StopError delegation's model-facing body (issue #319).
// The whole point of the fix is precedence: the harness/provider CAUSE leads because it
// is the actionable half; the child's last assistant text follows as clamped CONTEXT,
// never AS the failure reason. The empty-cause rows pin that nothing regresses for the
// terminals that carry no loop cause.
func TestSubagentErrorBodyPrefersCause(t *testing.T) {
	t.Parallel()
	const cause = "provider stream failed: 503 upstream unavailable"
	const final = "Now let me check the tests."

	tests := []struct {
		name  string
		final string
		cause string
		want  string
	}{
		{
			name:  "cause and final: cause leads, final follows as labelled context",
			final: final,
			cause: cause,
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
			name: "neither: today's honest floor",
			want: "subagent failed without producing a summary",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := subagentErrorBody(tc.final, tc.cause); got != tc.want {
				t.Fatalf("subagentErrorBody(%q, %q) =\n%q\nwant\n%q", tc.final, tc.cause, got, tc.want)
			}
		})
	}
}

// TestSubagentErrorBodyClampsFinal proves the child-authored half is BOUNDED: the cause
// crosses whole (it is harness metadata and the emit site clamps it), but the child's
// last text is clamped to maxTeamPreview so a runaway assistant message cannot dump
// unbounded content into the parent's tool result.
func TestSubagentErrorBodyClampsFinal(t *testing.T) {
	t.Parallel()
	huge := strings.Repeat("y", maxTeamPreview*4)
	got := subagentErrorBody(huge, "boom")
	if !strings.HasPrefix(got, "boom\n\nLast activity before the failure: ") {
		t.Fatalf("cause must still lead, got %q", got)
	}
	tail := strings.TrimPrefix(got, "boom\n\nLast activity before the failure: ")
	if len([]rune(tail)) > maxTeamPreview+1 { // +1 for the appended ellipsis
		t.Fatalf("final was not clamped: %d runes, want <= %d", len([]rune(tail)), maxTeamPreview+1)
	}
}

// TestSubagentErrorBodyTrimsWhitespaceOnlyInputs guards the degenerate case a real loop
// can produce: a StopError whose cause is empty and whose last assistant text is
// whitespace only. Without the trim the body would be a blank line — an opaque failure,
// the very thing renderSubagentResult's floor exists to prevent.
func TestSubagentErrorBodyTrimsWhitespaceOnlyInputs(t *testing.T) {
	t.Parallel()
	if got := subagentErrorBody("   \n\t ", "  "); got != "subagent failed without producing a summary" {
		t.Fatalf("whitespace-only inputs must fall to the floor, got %q", got)
	}
}
