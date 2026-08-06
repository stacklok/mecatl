package ui

import (
	"strings"
	"testing"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

// TestPermanentErrorBlockRendersSummary asserts that a permanent-error block
// renders the one-line human summary (not the raw JSON wall) in collapsed mode,
// and that the raw payload is NOT in the collapsed view.
func TestPermanentErrorBlockRendersSummary(t *testing.T) {
	r := newTestRenderer()
	c := &conversation{}
	c.addPermanentError("invalid_encrypted_content: the provider rejected the encrypted payload")

	out := stripANSIstr(r.renderBlock(0, &c.blocks[0], false))

	if !strings.Contains(out, "invalid_encrypted_content: the provider rejected the encrypted payload") {
		t.Errorf("collapsed permanent error must contain the first-line summary, got %q", out)
	}
	if !strings.Contains(out, "retrying won't help") {
		t.Errorf("collapsed permanent error must contain retry advisory, got %q", out)
	}
}

// TestPermanentErrorBlockCollapsedHidesRaw asserts the collapsed permanent-error
// block does NOT include the raw error payload (which may be long/verbose).
func TestPermanentErrorBlockCollapsedHidesRaw(t *testing.T) {
	r := newTestRenderer()
	c := &conversation{}
	raw := "error_code_4xx: the request was rejected by the provider\nmore detail on second line\nand third line"
	c.addPermanentError(raw)

	out := stripANSIstr(r.renderBlock(0, &c.blocks[0], false))

	if strings.Contains(out, "more detail on second line") {
		t.Errorf("collapsed permanent error must NOT show raw second line, got %q", out)
	}
	if strings.Contains(out, "and third line") {
		t.Errorf("collapsed permanent error must NOT show raw third line, got %q", out)
	}
	if strings.Contains(out, "raw payload") {
		t.Errorf("collapsed permanent error must NOT show raw payload header, got %q", out)
	}
}

// TestPermanentErrorBlockExpandShowsRaw asserts the expanded permanent-error block
// includes the raw error payload under a "raw payload:" header.
func TestPermanentErrorBlockExpandShowsRaw(t *testing.T) {
	r := newTestRenderer()
	c := &conversation{}
	raw := "error_code_4xx: the request was rejected\nsecond line detail"
	c.addPermanentError(raw)

	out := stripANSIstr(r.renderBlock(0, &c.blocks[0], true))

	if !strings.Contains(out, "raw payload") {
		t.Errorf("expanded permanent error must show raw payload header, got %q", out)
	}
	if !strings.Contains(out, "second line detail") {
		t.Errorf("expanded permanent error must show full raw text, got %q", out)
	}
	if !strings.Contains(out, "retrying won't help") {
		t.Errorf("expanded permanent error must still contain retry advisory in summary line, got %q", out)
	}
}

// TestPermanentErrorSummaryTruncatesLongLine asserts that a very long first line
// is truncated to a sane length in the summary.
func TestPermanentErrorSummaryTruncatesLongLine(t *testing.T) {
	longLine := strings.Repeat("x", 200)
	summary := permanentErrorSummary(longLine)
	runeLen := len([]rune(summary))
	if runeLen > 200 {
		t.Errorf("summary = %d runes, want <= ~140 (120 + advisory)", runeLen)
	}
	if !strings.HasPrefix(summary, strings.Repeat("x", 120)) {
		t.Errorf("summary must start with truncated first 120 runes, got %q", summary)
	}
}

// TestPermanentErrorSummaryDefaultFallback asserts the fallback message appears
// when the raw error is empty or blank.
func TestPermanentErrorSummaryDefaultFallback(t *testing.T) {
	for _, tc := range []string{"", "  ", "\n"} {
		summary := permanentErrorSummary(tc)
		if !strings.HasPrefix(summary, "permanent provider error") {
			t.Errorf("empty input %q must fall back to generic summary, got %q", tc, summary)
		}
	}
}

// TestTransientErrorRendersAsToday asserts a non-permanent error block still
// renders as the raw error text (the existing behavior).
func TestTransientErrorRendersAsToday(t *testing.T) {
	r := newTestRenderer()
	c := &conversation{}
	c.addError("upstream 503: service temporarily unavailable")

	out := stripANSIstr(r.renderBlock(0, &c.blocks[0], false))

	if !strings.Contains(out, "upstream 503: service temporarily unavailable") {
		t.Errorf("transient error must render raw text verbatim, got %q", out)
	}
	if strings.Contains(out, "retrying won't help") {
		t.Errorf("transient error must NOT contain permanent retry advisory, got %q", out)
	}
}

// TestResultMsgPermanentUsesAddPermanentError asserts that applyResult routes a
// permanent error to addPermanentError (not addError).
func TestResultMsgPermanentUsesAddPermanentError(t *testing.T) {
	m, _, _ := newTestModel(t, theme.New("aztec", theme.AztecPalette()))
	m.phase = phaseRunning

	// Send a permanent error result.
	m = applyAll(m, client.ResultMsg{
		Stop:      stopError,
		Error:     "invalid_encrypted_content: the blob is malformed",
		Permanent: true,
		Usage:     client.Usage{InputTokens: 10, OutputTokens: 2},
	})

	// The block should be a permanent-error block.
	var lastBlock *block
	if len(m.conv.blocks) > 0 {
		lastBlock = &m.conv.blocks[len(m.conv.blocks)-1]
	}
	if lastBlock == nil || lastBlock.kind != blockError {
		t.Fatalf("expected blockError kind, got %v", lastBlock)
	}
	if !lastBlock.permanent {
		t.Errorf("permanent ResultMsg must set block.permanent=true, got %v", lastBlock.permanent)
	}
	if !strings.Contains(lastBlock.raw, "invalid_encrypted_content") {
		t.Errorf("raw error text must survive in block, got %q", lastBlock.raw)
	}
}

// TestTransientResultMsgUsesAddError asserts a non-permanent error routes to
// addError as before.
func TestTransientResultMsgUsesAddError(t *testing.T) {
	m, _, _ := newTestModel(t, theme.New("aztec", theme.AztecPalette()))
	m.phase = phaseRunning

	m = applyAll(m, client.ResultMsg{
		Stop:      stopError,
		Error:     "upstream 503",
		Permanent: false,
		Usage:     client.Usage{InputTokens: 10, OutputTokens: 2},
	})

	var lastBlock *block
	if len(m.conv.blocks) > 0 {
		lastBlock = &m.conv.blocks[len(m.conv.blocks)-1]
	}
	if lastBlock == nil || lastBlock.kind != blockError {
		t.Fatalf("expected blockError kind, got %v", lastBlock)
	}
	if lastBlock.permanent {
		t.Errorf("non-permanent ResultMsg must leave block.permanent=false, got %v", lastBlock.permanent)
	}
}

// TestRecoverNoticeRendersAsScrollbackBlock asserts a RecoverNoticeMsg routes to
// a DURABLE warning-styled scrollback block (not a transient statusMsg that the
// run's first event would overwrite before the user reads it).
func TestRecoverNoticeRendersAsScrollbackBlock(t *testing.T) {
	m, _, _ := newTestModel(t, theme.New("aztec", theme.AztecPalette()))
	m.phase = phaseRunning
	before := len(m.conv.blocks)

	m = applyAll(m, client.RecoverNoticeMsg{
		Text: "this session's last turn failed on a permanent provider error",
	})

	if len(m.conv.blocks) != before+1 {
		t.Fatalf("RecoverNoticeMsg added %d scrollback block(s), want 1 (durable warning block)",
			len(m.conv.blocks)-before)
	}
	blk := m.conv.blocks[len(m.conv.blocks)-1]
	if blk.kind != blockNotice || !blk.recover {
		t.Errorf("RecoverNoticeMsg block = %+v, want blockNotice with recover=true", blk)
	}
	out := stripANSIstr(m.rend.renderBlock(0, &blk, false))
	if !strings.Contains(out, "permanent provider error") {
		t.Errorf("recover-notice block must render the advisory text, got %q", out)
	}
	if !strings.Contains(out, "⚠") {
		t.Errorf("recover-notice block must render the warning glyph ⚠, got %q", out)
	}
}

// TestPermanentErrorSummarySanitizesControlSequences asserts the summary line
// sanitizes terminal control/ANSI sequences from the raw provider error — a
// hostile provider/gateway could inject escapes into the scrollback otherwise
// (the blockError path already sanitizes; the permanent path must too).
func TestPermanentErrorSummarySanitizesControlSequences(t *testing.T) {
	r := newTestRenderer()
	raw := "invalid_encrypted_content: bad blob\x1b[31mRED\x1b[0m\r\nsecond line"
	c := &conversation{}
	c.addPermanentError(raw)

	out := stripANSIstr(r.renderBlock(0, &c.blocks[0], false))

	// No raw ESC (0x1b) or CR may survive into the rendered summary.
	if strings.Contains(out, "\x1b") {
		t.Errorf("summary must not contain ESC sequences, got %q", out)
	}
	if strings.Contains(out, "\r") {
		t.Errorf("summary must not contain carriage returns, got %q", out)
	}
	if !strings.Contains(out, "invalid_encrypted_content") {
		t.Errorf("summary must contain the error code, got %q", out)
	}
	if !strings.Contains(out, "retrying won't help") {
		t.Errorf("summary must contain the retry advisory, got %q", out)
	}
	// The expanded raw view must ALSO be sanitized (it already was — this guards
	// against a regression that routes the raw view unsanitized).
	expanded := stripANSIstr(r.renderBlock(0, &c.blocks[0], true))
	if strings.Contains(expanded, "\x1b") {
		t.Errorf("expanded raw view must be sanitized, got %q", expanded)
	}
}

// TestPermanentErrorSummaryCollapsesOpenAIPOSTJSON asserts the summary collapses
// the openai-go SDK transport error shape — 'POST "<url>": 400 Bad Request
// {"error":{…json…}}' — into a clean human-readable token, not a truncated-JSON
// wall. The extraction lives in the TUI (not the adapter Error()) because the
// adapters' Error() text is a deliberate byte-identical invariant.
func TestPermanentErrorSummaryCollapsesOpenAIPOSTJSON(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string // a token the summary MUST contain (clean, not raw JSON)
		bad  string // a token the summary must NOT contain (raw envelope noise)
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
			bad:  "",
		},
	}
	for _, tc := range cases {
		summary := permanentErrorSummary(tc.raw)
		if !strings.Contains(summary, tc.want) {
			t.Errorf("%s: summary %q must contain %q", tc.name, summary, tc.want)
		}
		if tc.bad != "" && strings.Contains(summary, tc.bad) {
			t.Errorf("%s: summary %q must NOT contain raw envelope noise %q", tc.name, summary, tc.bad)
		}
		if !strings.Contains(summary, "retrying won't help") {
			t.Errorf("%s: summary must contain the retry advisory, got %q", tc.name, summary)
		}
	}
}

// TestCollapseErrorSummaryEdgeCases pins the extractor's fall-backs so an
// unparseable envelope degrades cleanly rather than dumping raw JSON.
func TestCollapseErrorSummaryEdgeCases(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		// No POST prefix, no JSON — returned verbatim.
		{"rate_limit_exceeded: slow down", "rate_limit_exceeded: slow down"},
		// POST prefix with no JSON — prefix stripped, remainder kept.
		{`POST "https://x": 429 Too Many Requests`, "429 Too Many Requests"},
		// Trailing JSON with no message field — JSON dropped, head kept.
		{`400 Bad Request {"error":{"code":"x"}}`, "400 Bad Request"},
		// Trailing JSON whose message is non-string — dropped, head kept.
		{`400 Bad Request {"error":{"message":42}}`, "400 Bad Request"},
		// Bare trailing JSON object (no envelope) with a message — message
		// extracted and appended to the head.
		{`failed: {"message":"oops"}`, "failed: oops"},
		// Empty -> empty.
		{"", ""},
	}
	for _, tc := range cases {
		if got := collapseErrorSummary(tc.in); got != tc.want {
			t.Errorf("collapseErrorSummary(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
