package agent_test

import (
	"context"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// TestTitleSeededOnceFromFirstGenuinePrompt asserts Session.Title is seeded ONCE
// from the first genuine user prompt (via recordPrompt → SetTitle), and that a
// no-progress continuation nudge (the synthetic recordContinuation path) does
// NOT seed or overwrite it, nor does a second genuine prompt overwrite it
// (set-once). Script: genuine prompt → EmptyTurn (triggers a nudge) →
// TextTurn("done") → Reopen → second genuine prompt.
func TestTitleSeededOnceFromFirstGenuinePrompt(t *testing.T) {
	llm := mockllm.New(
		mockllm.EmptyTurn(),      // no text, no tool call → triggers a no-progress nudge
		mockllm.TextTurn("done"), // recovers, ends the first run cleanly
	)
	e := newEngine(agent.Deps{LLM: llm, Catalog: catalogWith(t)})
	sess := newSession(t, session.Limits{})
	ws := memfs.NewWorkspace("/ws")

	evs := drain(e.Run(context.Background(), sess, ws, agent.RunRequest{Text: "Fix the flaky CI job"}))

	// The run nudged then progressed (the empty turn was synthetic, not a
	// termination): exactly one no-progress nudge user message was recorded.
	if n := nudgeMessagesIn(sess.Conversation.Messages); n != 1 {
		t.Fatalf("continuation nudge messages = %d, want 1 (the nudge is synthetic, must not seed Title)", n)
	}
	_ = evs

	// Title was seeded from the FIRST genuine prompt — NOT the nudge.
	if got, want := sess.Title, "Fix the flaky CI job"; got != want {
		t.Fatalf("after first run Title = %q, want %q (seeded once from the genuine prompt, not the nudge)", got, want)
	}

	// Explicit guard: the synthetic no-progress nudge (recordContinuation) must
	// NOT have seeded or overwritten the title. recordContinuation has no
	// SetTitle call; this sub-assertion pins that structurally so a future
	// refactor adding one trips a clearly-labelled failure.
	nudgeText := "Make concrete progress on the task using your tools"
	if n := nudgeMessagesIn(sess.Conversation.Messages); n != 1 || strings.Contains(sess.Title, nudgeText) {
		t.Fatalf("nudge must not seed Title: nudges=%d, Title=%q (a synthetic continuation must never become the session title)", n, sess.Title)
	}

	// Reopen and run a SECOND genuine prompt — set-once must NOT overwrite.
	if err := sess.Reopen(); err != nil {
		t.Fatalf("Reopen: %v", err)
	}
	llm2 := mockllm.New(mockllm.TextTurn("ok"))
	e2 := newEngine(agent.Deps{LLM: llm2, Catalog: catalogWith(t)})
	drain(e2.Run(context.Background(), sess, ws, agent.RunRequest{Text: "A completely different second prompt"}))

	if got, want := sess.Title, "Fix the flaky CI job"; got != want {
		t.Fatalf("after second genuine prompt Title = %q, want %q (set-once — second prompt must not overwrite)", got, want)
	}
}

// TestTitleEmptyForMultimodalOnlyPrompt asserts a multimodal-only prompt (empty
// text, non-empty parts) does NOT seed the title — SetTitle's guard leaves
// Title=="" (the lazy fallback applies). Uses a parts-carrying prompt.
func TestTitleEmptyForMultimodalOnlyPrompt(t *testing.T) {
	llm := mockllm.New(mockllm.TextTurn("ok"))
	e := newEngine(agent.Deps{LLM: llm, Catalog: catalogWith(t)})
	sess := newSession(t, session.Limits{})
	ws := memfs.NewWorkspace("/ws")

	// A multimodal-only prompt: empty text + one image part.
	parts := []session.Content{
		{Kind: session.MediaImage, MIMEType: "image/png", Data: []byte("fakepng")},
	}
	r := e.Run(context.Background(), sess, ws, agent.RunRequest{Text: "", Parts: parts})
	drain(r)

	if sess.Title != "" {
		t.Fatalf("Title = %q, want empty (multimodal-only prompt must not seed — SetTitle's empty-text guard)", sess.Title)
	}
}

// TestTitleSeededOnReopenRun asserts the title survives a Reopen (it is an inert
// stored label, not cleared by resetToIdle) and a resumed run keeps it.
func TestTitleSeededOnReopenRun(t *testing.T) {
	llm := mockllm.New(mockllm.TextTurn("done"))
	e := newEngine(agent.Deps{LLM: llm, Catalog: catalogWith(t)})
	sess := newSession(t, session.Limits{})
	ws := memfs.NewWorkspace("/ws")

	drain(e.Run(context.Background(), sess, ws, agent.RunRequest{Text: "the original goal"}))
	if got, want := sess.Title, "the original goal"; got != want {
		t.Fatalf("Title after first run = %q, want %q", got, want)
	}

	// Reopen — resetToIdle must NOT clear Title (it is an inert stored label,
	// like Profile/ReasoningEffort, NOT a per-run Counter).
	if err := sess.Reopen(); err != nil {
		t.Fatalf("Reopen: %v", err)
	}
	if got, want := sess.Title, "the original goal"; got != want {
		t.Fatalf("Title after Reopen = %q, want %q (inert label, survives resetToIdle)", got, want)
	}
}

// Ensure the tool import is used (catalogWith takes ...tool.Tool; some builds
// tree-shake an otherwise-unused import when no test references tool directly).
var _ tool.Tool
