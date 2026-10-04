package renderfmt_test

import (
	"strings"
	"testing"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/ui/internal/renderfmt"
)

func TestHumanizeTokens(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{0, "0"},
		{1, "1"},
		{999, "999"},
		{1000, "1K"},
		{2000, "2K"},
		{7903, "7.9K"},
		{1500, "1.5K"},
		{999999, "1000K"},
		{1_000_000, "1M"},
		{1_200_000, "1.2M"},
		{-5, "0"},
	}
	for _, c := range cases {
		if got := renderfmt.HumanizeTokens(c.in); got != c.want {
			t.Errorf("renderfmt.HumanizeTokens(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestCacheHitRate(t *testing.T) {
	cases := []struct {
		name string
		u    client.Usage
		want float64
	}{
		{"zero input", client.Usage{}, 0},
		{"half", client.Usage{InputTokens: 100, CacheReadTokens: 50}, 0.5},
		{"full", client.Usage{InputTokens: 100, CacheReadTokens: 100}, 1.0},
		{"88pct", client.Usage{InputTokens: 1500, CacheReadTokens: 1320}, 0.88},
	}
	for _, c := range cases {
		got := renderfmt.CacheHitRate(c.u)
		if diff := got - c.want; diff > 1e-9 || diff < -1e-9 {
			t.Errorf("%s: renderfmt.CacheHitRate = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestPctString(t *testing.T) {
	cases := []struct {
		in   float64
		want string
	}{
		{0, "0%"},
		{0.5, "50%"},
		{0.881, "88%"},
		{1.0, "100%"},
		{1.5, "100%"},
		{-0.2, "0%"},
	}
	for _, c := range cases {
		if got := renderfmt.PctString(c.in); got != c.want {
			t.Errorf("renderfmt.PctString(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestHumanizeDuration(t *testing.T) {
	for _, tc := range []struct {
		ms   int64
		want string
	}{
		{-1, "0ms"}, {840, "840ms"}, {2000, "2s"}, {4100, "4.1s"}, {60000, "1m 0s"},
	} {
		if got := renderfmt.HumanizeDuration(tc.ms); got != tc.want {
			t.Errorf("HumanizeDuration(%d) = %q, want %q", tc.ms, got, tc.want)
		}
	}
}

func TestRenderContextMeterPlain(t *testing.T) {
	for _, tc := range []struct {
		used, window int64
		want         string
	}{
		{-1, 0, "ctx 0"},
		{40_000, 200_000, "ctx ▒▒░░░░░░ 20% · 40K/200K"},
		{190_000, 200_000, "ctx ████████ 95% ⚠ · 190K/200K"},
	} {
		if got := renderfmt.RenderContextMeterPlain(tc.used, tc.window); got != tc.want {
			t.Errorf("RenderContextMeterPlain(%d, %d) = %q, want %q", tc.used, tc.window, got, tc.want)
		}
	}
}

// TestTurnStatLine pins the per-turn stat line: it always leads with the
// input/output token arrows, appends the duration when the server reported one,
// and appends "N% cached" ONLY when the turn's cache-hit rate is at or above
// turnStatCacheFloor (decision 4). A negligible cache rate is omitted to keep the
// line scannable.
func TestTurnStatLine(t *testing.T) {
	cases := []struct {
		name       string
		usage      client.Usage
		durationMs int64
		wantSubs   []string
		absentSubs []string
	}{
		{
			name:       "no cache facet below floor",
			usage:      client.Usage{InputTokens: 1200, OutputTokens: 340}, // 0% cache
			durationMs: 4100,
			wantSubs:   []string{"↑1.2K", "↓340", "4.1s"},
			absentSubs: []string{"cached"},
		},
		{
			name:       "cache facet when material",
			usage:      client.Usage{InputTokens: 1500, OutputTokens: 30, CacheReadTokens: 1320}, // 88%
			durationMs: 0,
			wantSubs:   []string{"↑1.5K", "↓30", "88% cached"},
			absentSubs: []string{" · 4"}, // no duration segment when 0ms
		},
		{
			name:       "just under the floor is omitted",
			usage:      client.Usage{InputTokens: 1000, OutputTokens: 10, CacheReadTokens: 90}, // 9% < 10%
			durationMs: 0,
			wantSubs:   []string{"↑1K", "↓10"},
			absentSubs: []string{"cached"},
		},
		{
			name:       "exactly at the floor is shown",
			usage:      client.Usage{InputTokens: 1000, OutputTokens: 10, CacheReadTokens: 100}, // 10%
			durationMs: 0,
			wantSubs:   []string{"10% cached"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := renderfmt.TurnStatLine(client.TurnEndMsg{Usage: c.usage, DurationMs: c.durationMs})
			for _, sub := range c.wantSubs {
				if !strings.Contains(got, sub) {
					t.Errorf("renderfmt.TurnStatLine = %q, want it to contain %q", got, sub)
				}
			}
			for _, sub := range c.absentSubs {
				if strings.Contains(got, sub) {
					t.Errorf("renderfmt.TurnStatLine = %q, must NOT contain %q", got, sub)
				}
			}
		})
	}
}

// TestStopReasonLabel locks the human phrasing + style slot for every stop
// reason in the session.StopReason / proto Result.stop vocabulary, plus the
// empty and unknown fallbacks. The limit stops carry the warning slot; a clean
// end_turn / cancelled is muted; error is the error slot.
func TestStopReasonLabel(t *testing.T) {
	cases := []struct {
		stop string
		text string
		slot string
	}{
		{"end_turn", "done", "muted"},
		{"", "done", "muted"},
		{"max_turns", "stopped · turn limit", "ctxWarn"},
		{"max_tool_calls", "stopped · tool-call limit", "ctxWarn"},
		{"max_consecutive_failures", "stopped · repeated failures", "ctxWarn"},
		{"budget", "stopped · token budget", "ctxWarn"},
		{"cancelled", "cancelled", "muted"},
		{"no_progress", "stopped · no progress", "ctxWarn"},
		{"structured_output", "stopped · schema unmet", "ctxWarn"},
		{"plan_approved", "plan approved · executing", "muted"},
		{"plan_iterate", "plan iterate · awaiting your feedback", "muted"},
		{"error", "error", "errorText"},
		{"some_future_reason", "some_future_reason", "muted"},
	}
	for _, c := range cases {
		text, slot := renderfmt.StopReasonLabel(c.stop)
		if text != c.text {
			t.Errorf("renderfmt.StopReasonLabel(%q) text = %q, want %q", c.stop, text, c.text)
		}
		if slot != c.slot {
			t.Errorf("renderfmt.StopReasonLabel(%q) slot = %q, want %q", c.stop, slot, c.slot)
		}
	}
}

// TestStopReasonLabelSanitizesUnknown asserts an unknown reason carrying an ESC
// byte is stripped before it reaches the footer (it is rendered via lipgloss,
// which would otherwise pass the escape through).
func TestStopReasonLabelSanitizesUnknown(t *testing.T) {
	text, _ := renderfmt.StopReasonLabel("evil\x1b[2Jreason")
	if strings.ContainsRune(text, 0x1b) {
		t.Errorf("unknown stop reason should be sanitized, got %q", text)
	}
}

func TestHumanizeBytesSI(t *testing.T) {
	// SI/decimal math (1 KB = 1000 B, 1 MB = 1e6 B) — the number must reconcile
	// with bytes/1000, so a labelled "KB" is honest (not mislabelled KiB).
	cases := []struct {
		n    int64
		want string
	}{
		{0, "0 B"},
		{-5, "0 B"},
		{999, "999 B"},
		{1000, "1 KB"},   // exactly 1000 → 1 KB (would be "0.98 KB" under 1024 math)
		{1500, "1.5 KB"}, // 1500/1000
		{5000, "5 KB"},   // trailing .0 trimmed
		{999999, "1000 KB"},
		{1000000, "1 MB"}, // exactly 1e6 → 1 MB
		{2500000, "2.5 MB"},
		{1_000_000_000, "1 GB"},
		{2_500_000_000, "2.5 GB"},
		{1_000_000_000_000, "1 TB"},
	}
	for _, c := range cases {
		if got := renderfmt.HumanizeBytes(c.n); got != c.want {
			t.Errorf("renderfmt.HumanizeBytes(%d) = %q, want %q", c.n, got, c.want)
		}
	}
}
