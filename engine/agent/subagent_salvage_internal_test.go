package agent

import (
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/session"
)

// TestIsEmptyTerminalStop pins the issue #152 allow-set: the four bounded terminals plus
// an EMPTY clean end (StopEndTurn) trigger salvage+digest recovery; every other stop
// (success-with-text is gated separately by the blank-finalText guard, and crash/cancel
// terminals) falls through.
func TestIsEmptyTerminalStop(t *testing.T) {
	in := []session.StopReason{
		session.StopMaxTurns,
		session.StopMaxToolCalls,
		session.StopBudget,
		session.StopNoProgress,
		session.StopEndTurn,
	}
	for _, stop := range in {
		if !isEmptyTerminalStop(stop) {
			t.Errorf("isEmptyTerminalStop(%q) = false, want true (in the empty-terminal allow-set)", stop)
		}
	}
	out := []session.StopReason{
		session.StopError,
		session.StopCancelled,
		session.StopStructuredOutput,
		session.StopPlanApproved,
		session.StopPlanIterate,
		session.StopNone,
	}
	for _, stop := range out {
		if isEmptyTerminalStop(stop) {
			t.Errorf("isEmptyTerminalStop(%q) = true, want false (outside the allow-set)", stop)
		}
	}
}

// TestDigestChildActivity proves the last-resort recovery walks the child's history
// backwards for the most recent non-empty assistant text (issue #152) — the defensive
// floor when both the original drive and the bounded salvage turn produced no final
// text. It is exercised directly here because the live loop's own lastText machinery
// already carries tool-call-turn text into the result, so the digest is genuine
// belt-and-suspenders that a model-facing test cannot reliably force.
func TestDigestChildActivity(t *testing.T) {
	// nil child → "".
	if got := digestChildActivity(nil); got != "" {
		t.Fatalf("digestChildActivity(nil) = %q, want empty", got)
	}

	clk := time.Unix(0, 0)
	// No assistant text anywhere → "".
	empty := mustChildSession(t, "c-empty", clk)
	if got := digestChildActivity(empty); got != "" {
		t.Fatalf("digestChildActivity(no assistant text) = %q, want empty", got)
	}

	// The most-recent NON-EMPTY assistant message wins (a later empty assistant message
	// is skipped; the walk continues backwards).
	s := mustChildSession(t, "c", clk)
	mustRecordAssistant(t, s, "FIRST PARTIAL FINDINGS")
	mustRecordAssistant(t, s, "SECOND PARTIAL FINDINGS")
	mustRecordAssistant(t, s, "   ") // empty-after-trim, skipped
	got := digestChildActivity(s)
	if !strings.Contains(got, "SECOND PARTIAL FINDINGS") {
		t.Fatalf("digestChildActivity = %q, want the last non-empty assistant text", got)
	}
	if strings.Contains(got, "FIRST PARTIAL FINDINGS") {
		t.Fatalf("digestChildActivity = %q, must return ONLY the last non-empty assistant text", got)
	}
}

// TestRecoveredDigestPrefixFraming pins the issue #152 UX contract for the last-resort
// digest prefix: it states provenance + partial-ness ONLY, so it reads coherently
// standalone on the note-less empty-StopEndTurn path while restating NEITHER of the two
// things renderSubagentResult's stop-reason note owns — the stop reason ("ended without a
// final summary") and the next action ("resume it with the agentId above"). The two
// strings are rendered one after the other on a StopNoProgress terminal, so a duplicated
// clause reads to the model as two separate instructions.
//
// The de-duplication is asserted end-to-end through the real render path in
// TestRecoveredDigestStatesTheNextActionOnce; this is the constant-level half.
func TestRecoveredDigestPrefixFraming(t *testing.T) {
	// Provenance + partial-ness are present (what the prefix owns).
	for _, want := range []string{"recovered", "partial"} {
		if !strings.Contains(recoveredDigestPrefix, want) {
			t.Errorf("recoveredDigestPrefix %q must mention %q", recoveredDigestPrefix, want)
		}
	}
	// It must NOT restate the stop reason the StopNoProgress note owns — otherwise the
	// note + prefix double-state "ended without a final summary" and subtly conflict.
	if strings.Contains(recoveredDigestPrefix, "ended without a final summary") {
		t.Errorf("recoveredDigestPrefix must not restate the StopNoProgress note's stop reason: %q", recoveredDigestPrefix)
	}
	// Nor the next action, for the same reason (the issue #319 review round found the
	// clause "treat as partial; resume it with the agentId above to continue" was
	// byte-identical in the prefix and the note).
	for _, forbidden := range []string{"resume", "agentId"} {
		if strings.Contains(recoveredDigestPrefix, forbidden) {
			t.Errorf("recoveredDigestPrefix must not restate the note's next action (%q): %q", forbidden, recoveredDigestPrefix)
		}
	}
}

// TestDigestChildActivityClamps proves the digest is bounded (clampRunes to
// maxTeamPreview), so a runaway last assistant message cannot dump unbounded content
// back into the parent's context.
func TestDigestChildActivityClamps(t *testing.T) {
	clk := time.Unix(0, 0)
	s := mustChildSession(t, "c", clk)
	huge := strings.Repeat("x", maxTeamPreview*4)
	mustRecordAssistant(t, s, huge)
	got := digestChildActivity(s)
	if len([]rune(got)) > maxTeamPreview+1 { // +1 for the appended ellipsis
		t.Fatalf("digestChildActivity returned %d runes, want clamped to <= maxTeamPreview+1 (%d)", len([]rune(got)), maxTeamPreview+1)
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("a clamped digest must end with an ellipsis, got: %q", got[max(0, len(got)-8):])
	}
}

// mustChildSession builds a fresh idle child session and records a user prompt so it is
// in StateRunning, ready for RecordAssistant (the loop's normal turn-0 sequence).
func mustChildSession(t *testing.T, id session.SessionID, clk time.Time) *session.Session {
	t.Helper()
	s := session.New(id, session.ModeDefault, "/ws", session.Limits{}, clk)
	if err := s.RecordUserPrompt("investigate", nil); err != nil {
		t.Fatalf("RecordUserPrompt: %v", err)
	}
	if err := s.BeginTurn(); err != nil {
		t.Fatalf("BeginTurn: %v", err)
	}
	return s
}

func mustRecordAssistant(t *testing.T, s *session.Session, text string) {
	t.Helper()
	if err := s.RecordAssistant(session.Message{Role: session.RoleAssistant, Text: text}); err != nil {
		t.Fatalf("RecordAssistant(%q): %v", text, err)
	}
}
