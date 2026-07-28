package ui

import "testing"

// TestDeliveryBodyForDisplay pins the operator-facing display transform: the
// recorded delivery note is fenced-untrusted with a provenance header (both
// machine markers for the model), and the card strips them to show only the
// fire's outcome body. A non-fenced note is returned verbatim (fail-soft).
func TestDeliveryBodyForDisplay(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "strips fence + provenance header, keeps outcome body",
			raw: "<<<UNTRUSTED\n" +
				"[scheduled task recent-files (fire sched--recent-files-20260728-123104-8d4c829a) completed with stop reason: end_turn]\n" +
				"Working tree is clean. By most recent commit touching each file:\n\n1. foo.go\n2. bar.go\n" +
				"<<<UNTRUSTED",
			want: "Working tree is clean. By most recent commit touching each file:\n\n1. foo.go\n2. bar.go",
		},
		{
			name: "no-output body",
			raw: "<<<UNTRUSTED\n" +
				"[scheduled task ping (fire sched--ping-1) completed with stop reason: end_turn]\n" +
				"(no output)\n" +
				"<<<UNTRUSTED",
			want: "(no output)",
		},
		{
			name: "non-fenced note is returned verbatim (fail-soft)",
			raw:  "just some text\nwith lines",
			want: "just some text\nwith lines",
		},
		{
			name: "fenced note with no provenance header keeps its first line",
			raw: "<<<UNTRUSTED\n" +
				"an outcome with no header\nsecond line\n" +
				"<<<UNTRUSTED",
			want: "an outcome with no header\nsecond line",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := deliveryBodyForDisplay(tc.raw); got != tc.want {
				t.Errorf("deliveryBodyForDisplay() = %q, want %q", got, tc.want)
			}
		})
	}
}
