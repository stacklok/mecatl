package ui

import (
	"strings"
	"testing"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
	"github.com/stacklok/mecatl/cmd/mecatui/ui/internal/scrollback"
)

// TestPermanentErrorBlockRendersSummary asserts that a permanent-error block
// renders the one-line human summary (not the raw JSON wall) in collapsed mode,
// and that the raw payload is NOT in the collapsed view.
func TestPermanentErrorBlockRendersSummary(t *testing.T) {
	r := newTestRenderer()
	c := &conversation{}
	c.addPermanentError("invalid_encrypted_content: the provider rejected the encrypted payload")

	out := stripANSIstr(r.renderSnapshot(0, c.testBlocks()[0], false))

	if !strings.Contains(out, "invalid_encrypted_content: the provider rejected the encrypted payload") {
		t.Errorf("collapsed permanent error must contain the first-line summary, got %q", out)
	}
	if !strings.Contains(out, "ctrl+t shows details") {
		t.Errorf("collapsed permanent error must name the default details chord, got %q", out)
	}
}

func TestPermanentErrorBlockUsesLiveExpandToolsChord(t *testing.T) {
	m, _, _ := newTestModel(t, theme.New("aztec", theme.AztecPalette()), func(deps *Deps) {
		deps.KeyOverrides = map[string][]string{"ExpandTools": {"ctrl+f11"}}
	})
	c := &conversation{}
	c.addPermanentError("invalid request")

	out := stripANSIstr(m.rend.renderSnapshot(0, c.testBlocks()[0], false))
	if !strings.Contains(out, "ctrl+f11 shows details") || strings.Contains(out, "ctrl+t shows details") {
		t.Errorf("collapsed permanent error must use the live details chord, got %q", out)
	}
}

// TestPermanentErrorBlockCollapsedHidesRaw asserts the collapsed permanent-error
// block does NOT include the raw error payload (which may be long/verbose).
func TestPermanentErrorBlockCollapsedHidesRaw(t *testing.T) {
	r := newTestRenderer()
	c := &conversation{}
	raw := "error_code_4xx: the request was rejected by the provider\nmore detail on second line\nand third line"
	c.addPermanentError(raw)

	out := stripANSIstr(r.renderSnapshot(0, c.testBlocks()[0], false))

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

	out := stripANSIstr(r.renderSnapshot(0, c.testBlocks()[0], true))

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

// TestTransientErrorRendersAsToday asserts a non-permanent error block still
// renders as the raw error text (the existing behavior).
func TestTransientErrorRendersAsToday(t *testing.T) {
	r := newTestRenderer()
	c := &conversation{}
	c.addError("upstream 503: service temporarily unavailable")

	out := stripANSIstr(r.renderSnapshot(0, c.testBlocks()[0], false))

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
	lastCard := m.conv.testBlocks()[len(m.conv.testBlocks())-1]
	lastError, ok := lastCard.Payload.(scrollback.ErrorCardSnapshot)
	if !ok {
		t.Fatalf("expected Error card, got %T", lastCard.Payload)
	}
	if !lastError.Permanent {
		t.Error("permanent ResultMsg must set Permanent")
	}
	if !strings.Contains(lastError.Text, "invalid_encrypted_content") {
		t.Errorf("raw error text lost: %q", lastError.Text)
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

	lastCard := m.conv.testBlocks()[len(m.conv.testBlocks())-1]
	lastError, ok := lastCard.Payload.(scrollback.ErrorCardSnapshot)
	if !ok {
		t.Fatalf("expected Error card, got %T", lastCard.Payload)
	}
	if lastError.Permanent {
		t.Error("non-permanent ResultMsg set Permanent")
	}
}

// TestRecoverNoticeRendersAsScrollbackBlock asserts a RecoverNoticeMsg routes to
// a DURABLE warning-styled scrollback block (not a transient statusMsg that the
// run's first event would overwrite before the user reads it).
func TestRecoverNoticeRendersAsScrollbackBlock(t *testing.T) {
	m, _, _ := newTestModel(t, theme.New("aztec", theme.AztecPalette()))
	m.phase = phaseRunning
	before := len(m.conv.testBlocks())

	m = applyAll(m, client.RecoverNoticeMsg{
		Text: "this session's last turn failed on a permanent provider error",
	})

	if len(m.conv.testBlocks()) != before+1 {
		t.Fatalf("RecoverNoticeMsg added %d scrollback block(s), want 1 (durable warning block)",
			len(m.conv.testBlocks())-before)
	}
	blk := m.conv.testBlocks()[len(m.conv.testBlocks())-1]
	notice, ok := blk.Payload.(scrollback.NoticeCardSnapshot)
	if !ok || !notice.Recover {
		t.Errorf("RecoverNoticeMsg card = %+v, want recovery notice", blk)
	}
	out := stripANSIstr(m.rend.renderSnapshot(0, blk, false))
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

	out := stripANSIstr(r.renderSnapshot(0, c.testBlocks()[0], false))

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
	expanded := stripANSIstr(r.renderSnapshot(0, c.testBlocks()[0], true))
	if strings.Contains(expanded, "\x1b") {
		t.Errorf("expanded raw view must be sanitized, got %q", expanded)
	}
}
