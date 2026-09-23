package blocks

import (
	"strings"
	"testing"
)

func TestPermanentErrorSummaryTruncatesLongLine(t *testing.T) {
	longLine := strings.Repeat("x", 200)
	summary := PermanentErrorSummary(longLine)
	if runeLen := len([]rune(summary)); runeLen > 200 {
		t.Errorf("summary = %d runes, want <= ~140 (120 + advisory)", runeLen)
	}
	if !strings.HasPrefix(summary, strings.Repeat("x", 120)) {
		t.Errorf("summary must start with truncated first 120 runes, got %q", summary)
	}
}

func TestPermanentErrorSummaryDefaultFallback(t *testing.T) {
	for _, raw := range []string{"", "  ", "\n"} {
		summary := PermanentErrorSummary(raw)
		if !strings.HasPrefix(summary, "permanent provider error") {
			t.Errorf("empty input %q must fall back to generic summary, got %q", raw, summary)
		}
	}
}

func TestPermanentErrorSummaryCollapsesOpenAIPOSTJSON(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
		bad  string
	}{
		{
			name: "openai responses transport error",
			raw:  `POST "https://api.openai.com/v1/responses": 400 Bad Request {"error":{"code":"invalid_request_error","message":"Invalid 'model' field"}}`,
			want: "Invalid 'model' field",
			bad:  `POST "https://api.openai.com/v1/responses"`,
		},
		{
			name: "openaichat transport error with code+message",
			raw:  `POST "https://api.openai.com/v1/chat/completions": 401 Unauthorized {"error":{"message":"Incorrect API key provided","code":"invalid_api_key"}}`,
			want: "Incorrect API key provided",
			bad:  `{"error":`,
		},
		{
			name: "anthropic-style clean code: message (unchanged)",
			raw:  "invalid_request_error: the blob is malformed",
			want: "invalid_request_error: the blob is malformed",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			summary := PermanentErrorSummary(tc.raw)
			if !strings.Contains(summary, tc.want) {
				t.Errorf("summary %q must contain %q", summary, tc.want)
			}
			if tc.bad != "" && strings.Contains(summary, tc.bad) {
				t.Errorf("summary %q must not contain raw envelope noise %q", summary, tc.bad)
			}
			if !strings.Contains(summary, "retrying won't help") {
				t.Errorf("summary must contain the retry advisory, got %q", summary)
			}
		})
	}
}

func TestCollapseErrorSummaryEdgeCases(t *testing.T) {
	cases := []struct{ in, want string }{
		{"rate_limit_exceeded: slow down", "rate_limit_exceeded: slow down"},
		{`POST "https://x": 429 Too Many Requests`, "429 Too Many Requests"},
		{`400 Bad Request {"error":{"code":"x"}}`, "400 Bad Request"},
		{`400 Bad Request {"error":{"message":42}}`, "400 Bad Request"},
		{`failed: {"message":"oops"}`, "failed: oops"},
		{"", ""},
	}
	for _, tc := range cases {
		if got := CollapseErrorSummary(tc.in); got != tc.want {
			t.Errorf("CollapseErrorSummary(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
