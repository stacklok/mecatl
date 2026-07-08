package eventsource_test

import (
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/eventsource"
	"github.com/stacklok/mecatl/engine/session"
)

// TestFoldDerivesTitleFromFirstGenuineEvUserPrompt asserts Fold seeds the session
// Title from the FIRST genuine user prompt seen in the stream (an EvUserPrompt
// whose text passes session.IsGenuineUserPrompt), clamped via SetTitle.
func TestFoldDerivesTitleFromFirstGenuineEvUserPrompt(t *testing.T) {
	evs := []session.Event{
		{Type: session.EvUserPrompt, Turn: 0, UserPrompt: &session.UserPromptPayload{Text: "Fix the flaky CI job"}},
		{Type: session.EvTurnStart, Turn: 0},
		{Type: session.EvMessageDelta, Turn: 0, Text: "on it"},
		{Type: session.EvResult, Turn: 0, Result: &session.ResultPayload{Stop: session.StopEndTurn}},
	}
	s, err := eventsource.Fold(meta(), seq(evs))
	if err != nil {
		t.Fatalf("Fold: %v", err)
	}
	if got, want := s.Title, "Fix the flaky CI job"; got != want {
		t.Fatalf("Title = %q, want %q (seeded from first genuine EvUserPrompt)", got, want)
	}
}

// TestFoldNoTitleWhenNoGenuinePrompt asserts a stream with NO genuine user prompt
// (only synthesised compaction summaries + assistant turns) leaves Title empty.
func TestFoldNoTitleWhenNoGenuinePrompt(t *testing.T) {
	evs := []session.Event{
		// A synthesised compaction summary is RoleUser but NOT genuine — must NOT seed.
		{Type: session.EvUserPrompt, Turn: 0, UserPrompt: &session.UserPromptPayload{Text: session.CompactionSummaryMarker + " earlier turns…"}},
		{Type: session.EvTurnStart, Turn: 0},
		{Type: session.EvMessageDelta, Turn: 0, Text: "ok"},
		{Type: session.EvResult, Turn: 0, Result: &session.ResultPayload{Stop: session.StopEndTurn}},
	}
	s, err := eventsource.Fold(meta(), seq(evs))
	if err != nil {
		t.Fatalf("Fold: %v", err)
	}
	if s.Title != "" {
		t.Fatalf("Title = %q, want empty (no genuine prompt in stream)", s.Title)
	}
}

// TestFoldTitleSurvivesCompaction asserts the title survives compaction: the
// opener's EvUserPrompt was emitted BEFORE the EvCompactionArchive in the stream,
// so Fold captured it. The post-compaction history (which the snapshot lazy
// fallback would walk) does NOT contain the opener, but the Fold still has the
// title — the documented advantage over the snapshot lazy fallback.
func TestFoldTitleSurvivesCompaction(t *testing.T) {
	head := []session.Message{
		session.NewUserMessage("the original goal that was compacted away"),
		session.NewAssistantMessage("old turn", "", nil),
	}
	evs := []session.Event{
		// The genuine opener is evented BEFORE the compaction.
		{Type: session.EvUserPrompt, Turn: 0, UserPrompt: &session.UserPromptPayload{Text: "the original goal that was compacted away"}},
		{Type: session.EvTurnStart, Turn: 0},
		{Type: session.EvCompaction, Turn: 0, Text: "summary"},
		{Type: session.EvCompactionArchive, Turn: 0, CompactionArchive: &session.CompactionArchivePayload{Replaced: head}},
		{Type: session.EvMessageDelta, Turn: 0, Text: "post-compaction answer"},
		{Type: session.EvResult, Turn: 0, Result: &session.ResultPayload{Stop: session.StopEndTurn}},
	}
	s, err := eventsource.Fold(meta(), seq(evs))
	if err != nil {
		t.Fatalf("Fold: %v", err)
	}
	if got, want := s.Title, "the original goal that was compacted away"; got != want {
		t.Fatalf("Title = %q, want %q (survived compaction via the pre-compaction EvUserPrompt)", got, want)
	}
	// The reconstructed conversation's first message is the genuine goal (recovered
	// from the archive head), but even if it weren't, the title is captured from the
	// EVENT stream, not the post-compaction history.
}

// TestFoldTitleIsSetOnce asserts only the FIRST genuine EvUserPrompt seeds the
// title — a later genuine prompt does NOT overwrite (the set-once seam).
func TestFoldTitleIsSetOnce(t *testing.T) {
	evs := []session.Event{
		{Type: session.EvUserPrompt, Turn: 0, UserPrompt: &session.UserPromptPayload{Text: "first prompt"}},
		{Type: session.EvTurnStart, Turn: 0},
		{Type: session.EvResult, Turn: 0, Result: &session.ResultPayload{Stop: session.StopEndTurn}},
		// A second run (reopened) with a second genuine prompt.
		{Type: session.EvUserPrompt, Turn: 0, UserPrompt: &session.UserPromptPayload{Text: "second prompt"}},
		{Type: session.EvTurnStart, Turn: 0},
		{Type: session.EvResult, Turn: 0, Result: &session.ResultPayload{Stop: session.StopEndTurn}},
	}
	s, err := eventsource.Fold(meta(), seq(evs))
	if err != nil {
		t.Fatalf("Fold: %v", err)
	}
	if got, want := s.Title, "first prompt"; got != want {
		t.Fatalf("Title = %q, want %q (set-once — second prompt must not overwrite)", got, want)
	}
}

// TestFoldTitleClampsLongPrompt asserts the captured title is clamped to
// maxTitleRunes (120) + "…".
func TestFoldTitleClampsLongPrompt(t *testing.T) {
	long := strings.Repeat("x", 200)
	evs := []session.Event{
		{Type: session.EvUserPrompt, Turn: 0, UserPrompt: &session.UserPromptPayload{Text: long}},
		{Type: session.EvTurnStart, Turn: 0},
		{Type: session.EvResult, Turn: 0, Result: &session.ResultPayload{Stop: session.StopEndTurn}},
	}
	s, err := eventsource.Fold(meta(), seq(evs))
	if err != nil {
		t.Fatalf("Fold: %v", err)
	}
	want := strings.Repeat("x", 120) + "…"
	if s.Title != want {
		t.Fatalf("Title len = %d, want %d (clamped)", len([]rune(s.Title)), len([]rune(want)))
	}
}

// TestFoldTitleAwaiting asserts the title is seeded even for an awaiting
// reconstruction (the reconstructAwaiting path calls SetTitle too).
func TestFoldTitleAwaiting(t *testing.T) {
	call := toolCall("c1", "Bash", `{"command":"ls"}`)
	ask := session.PendingAsk{AskID: "s1:0:c1:r0", Tool: "Bash"}
	evs := []session.Event{
		{Type: session.EvUserPrompt, Turn: 0, UserPrompt: &session.UserPromptPayload{Text: "run ls please"}},
		{Type: session.EvTurnStart, Turn: 0},
		{Type: session.EvMessageDelta, Turn: 0, Text: "ok"},
		{Type: session.EvToolCall, Turn: 0, ToolCall: &call},
		{Type: session.EvPermissionAsk, Turn: 0, Ask: &ask},
	}
	s, err := eventsource.Fold(meta(), seq(evs))
	if err != nil {
		t.Fatalf("Fold: %v", err)
	}
	if s.State != session.StateAwaiting {
		t.Fatalf("state = %q, want awaiting", s.State)
	}
	if got, want := s.Title, "run ls please"; got != want {
		t.Fatalf("Title = %q, want %q (seeded on the awaiting path too)", got, want)
	}
}
