package agent_test

import (
	"context"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
)

// TestTurnEndZeroUsageFallsBackToCounter pins issue-#82 Fix A1, DISPLAY-ONLY: a turn
// that yields NO usage chunk (InputTokens == 0) on a non-empty conversation must not
// zero the context meter. The loop falls back to the TokenCounter's estimate over the
// recorded conversation for the EvTurnEnd payload ONLY, so EvTurnEnd.Usage.InputTokens
// (the meter) stays > 0 and equals that estimate AND TurnEnd.Estimated is true — while
// the cumulative budget figure (EvResult.Usage / sess.Usage) stays on PROVIDER TRUTH
// (0 here), so the estimate never moves a token budget. Output tokens are NOT
// fabricated.
func TestTurnEndZeroUsageFallsBackToCounter(t *testing.T) {
	llm := mockllm.New(
		// No UsageChunk: the turn reports zero usage (the laundered-stall shape).
		mockllm.ChunksTurn(
			mockllm.TextChunk("hello there"),
			mockllm.DoneChunk(session.StopEndTurn),
		),
	)
	e := newEngine(agent.Deps{LLM: llm, Catalog: catalogWith(t)})
	sess := newSession(t, session.Limits{})
	r := e.Run(context.Background(), sess, agent.MemEnv("/ws"), agent.RunRequest{Text: "investigate the large repo"})
	evs := drain(r)

	te := turnEndOf(t, evs)
	if te.Usage.InputTokens <= 0 {
		t.Fatalf("InputTokens = %d, want > 0 (zero-usage turn must fall back to the counter)", te.Usage.InputTokens)
	}
	if !te.Estimated {
		t.Errorf("TurnEnd.Estimated = false, want true (the meter figure is a fallback estimate)")
	}
	// DISPLAY-ONLY split: the cumulative/budget figure stays on provider truth.
	// EvResult.Usage is the per-run total the budget brake and the team supervisor
	// read; with the provider reporting no usage it must remain 0 (NOT the estimate).
	for _, ev := range evs {
		if ev.Type == session.EvResult && ev.Result != nil {
			if ev.Result.Usage.InputTokens != 0 {
				t.Errorf("EvResult.Usage.InputTokens = %d, want 0 (budget figure stays on provider truth, not the display estimate)", ev.Result.Usage.InputTokens)
			}
		}
	}
	// The session aggregate's cumulative main usage (what the budget brake + snapshot read)
	// likewise stays on provider truth.
	if got := sess.UsageFor(session.UsageKindMain).InputTokens; got != 0 {
		t.Errorf("main usage InputTokens = %d, want 0 (cumulative budget figure is provider truth, not the estimate)", got)
	}
	// The fallback is computed BEFORE the assistant message is recorded, so it
	// covers only the user prompt at that instant; the FINAL conversation
	// (user + assistant) is therefore an upper bound. Assert the meter is a
	// plausible counter estimate: positive and no greater than the full-history
	// estimate (the engine defaults TokenCounter to HeuristicTokenCounter).
	full := agent.HeuristicTokenCounter{}.CountMessages(sess.Conversation.Messages)
	if te.Usage.InputTokens > full {
		t.Errorf("InputTokens = %d, want <= %d (full-history counter estimate)", te.Usage.InputTokens, full)
	}
	// And it must equal the counter over the user-only prompt that existed at
	// fallback time — the assistant message is appended only after the turn.end emit.
	userOnly := agent.HeuristicTokenCounter{}.CountMessages(sess.Conversation.Messages[:1])
	if te.Usage.InputTokens != userOnly {
		t.Errorf("InputTokens = %d, want %d (counter over the user prompt present at fallback time)", te.Usage.InputTokens, userOnly)
	}
	// Output tokens are never fabricated by the fallback.
	if te.Usage.OutputTokens != 0 {
		t.Errorf("OutputTokens = %d, want 0 (fallback must not fabricate output tokens)", te.Usage.OutputTokens)
	}
}

// TestTurnEndNonZeroUsageNotOverwritten is the NEGATIVE guard for Fix A1: a turn
// that DOES report usage keeps its reported InputTokens verbatim — the fallback is
// gated strictly on InputTokens == 0 and never clobbers a real provider figure.
func TestTurnEndNonZeroUsageNotOverwritten(t *testing.T) {
	const reported = 4242
	llm := mockllm.New(
		mockllm.ChunksTurn(
			mockllm.TextChunk("hi"),
			mockllm.UsageChunk(session.Usage{InputTokens: reported, OutputTokens: 7}),
			mockllm.DoneChunk(session.StopEndTurn),
		),
	)
	e := newEngine(agent.Deps{LLM: llm, Catalog: catalogWith(t)})
	sess := newSession(t, session.Limits{})
	r := e.Run(context.Background(), sess, agent.MemEnv("/ws"), agent.RunRequest{Text: "do a thing"})
	evs := drain(r)

	te := turnEndOf(t, evs)
	if te.Usage.InputTokens != reported {
		t.Errorf("InputTokens = %d, want %d (reported usage must be preserved verbatim)", te.Usage.InputTokens, reported)
	}
	if te.Usage.OutputTokens != 7 {
		t.Errorf("OutputTokens = %d, want 7", te.Usage.OutputTokens)
	}
}
