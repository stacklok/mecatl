package agent_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
)

// TestResumableSessionStatusMetrics_Scenario1_ContextOccupancySnapshots pins
// AC1.1: only a completed agent-loop turn's non-zero display meter establishes
// the session-owned context occupancy. The fallback estimate marker is display
// state and a zero numerator cannot erase the prior value.
func TestResumableSessionStatusMetrics_Scenario1_ContextOccupancySnapshots(t *testing.T) {
	e := newEngine(agent.Deps{
		LLM:     mockllm.New(mockllm.TextTurn("done")),
		Catalog: catalogWith(t),
	})
	sess := newSession(t, session.Limits{})

	_ = drain(e.Run(context.Background(), sess, agent.MemEnv("/ws"), agent.RunRequest{Text: "go"}))
	occupancy, ok := sess.LatestContextOccupancy()
	if !ok {
		t.Fatal("completed turn did not establish context occupancy")
	}
	if occupancy.InputTokens <= 0 || !occupancy.Estimated {
		t.Fatalf("context occupancy = %+v, want a non-zero fallback estimate", occupancy)
	}

	sess.RecordLatestContextOccupancy(session.ContextOccupancy{})
	if got, ok := sess.LatestContextOccupancy(); !ok || got != occupancy {
		t.Fatalf("zero context occupancy replaced prior value: got %+v, %v; want %+v, true", got, ok, occupancy)
	}
}

// TestResumableSessionStatusMetrics_Scenario1_NonTurnWorkCannotReplaceOccupancy
// pins AC1.4: a failed stream and title-generation accounting may update the
// durable ledger, but neither may replace the latest successful turn's display
// occupancy. The main ledger remains element-wise lifetime accounting.
func TestResumableSessionStatusMetrics_Scenario1_NonTurnWorkCannotReplaceOccupancy(t *testing.T) {
	const input = 41
	sess := newSession(t, session.Limits{})
	success := newEngine(agent.Deps{
		LLM:     mockllm.New(mockllm.ChunksTurn(mockllm.TextChunk("done"), mockllm.UsageChunk(session.Usage{InputTokens: input, OutputTokens: 3}), mockllm.DoneChunk(session.StopEndTurn))),
		Catalog: catalogWith(t),
	})
	_ = drain(success.Run(context.Background(), sess, agent.MemEnv("/ws"), agent.RunRequest{Text: "go"}))
	occupancy, ok := sess.LatestContextOccupancy()
	if !ok || occupancy != (session.ContextOccupancy{InputTokens: input}) {
		t.Fatalf("successful turn occupancy = %+v, %v; want {%d false}, true", occupancy, ok, input)
	}

	sess.RecordTokenUsage(session.UsageKindSessionTitle, "title", "model", session.Usage{InputTokens: 7, OutputTokens: 2})
	if got, ok := sess.LatestContextOccupancy(); !ok || got != occupancy {
		t.Fatalf("title usage replaced context occupancy: got %+v, %v; want %+v, true", got, ok, occupancy)
	}

	if err := sess.Reopen(); err != nil {
		t.Fatalf("Reopen: %v", err)
	}
	failed := newEngine(agent.Deps{
		LLM: mockllm.New(mockllm.ErrorTurn(
			errors.New("upstream failed"),
			mockllm.TextChunk("partial"),
			mockllm.UsageChunk(session.Usage{InputTokens: 19, OutputTokens: 5}),
		)),
		Catalog: catalogWith(t),
	})
	_ = drain(failed.Run(context.Background(), sess, agent.MemEnv("/ws"), agent.RunRequest{Text: "retry"}))
	if got, ok := sess.LatestContextOccupancy(); !ok || got != occupancy {
		t.Fatalf("failed stream replaced context occupancy: got %+v, %v; want %+v, true", got, ok, occupancy)
	}

	wantMain := session.Usage{InputTokens: input + 19, OutputTokens: 8}
	if got := sess.TokenUsageSnapshot()[session.UsageKindMain].Total; got != wantMain {
		t.Fatalf("token_usage[main].total = %+v, want %+v", got, wantMain)
	}

	cancelled := newSession(t, session.Limits{})
	cancelledSuccess := newEngine(agent.Deps{
		LLM:     mockllm.New(mockllm.ChunksTurn(mockllm.TextChunk("done"), mockllm.UsageChunk(session.Usage{InputTokens: input, OutputTokens: 3}), mockllm.DoneChunk(session.StopEndTurn))),
		Catalog: catalogWith(t),
	})
	_ = drain(cancelledSuccess.Run(context.Background(), cancelled, agent.MemEnv("/ws"), agent.RunRequest{Text: "go"}))
	cancelledOccupancy, ok := cancelled.LatestContextOccupancy()
	if !ok || cancelledOccupancy != occupancy {
		t.Fatalf("cancel test setup occupancy = %+v, %v; want %+v, true", cancelledOccupancy, ok, occupancy)
	}
	if err := cancelled.Reopen(); err != nil {
		t.Fatalf("cancel test Reopen: %v", err)
	}
	cancelledStream := newEngine(agent.Deps{
		LLM: mockllm.New(mockllm.ChunksTurn(
			mockllm.TextChunk("partial"),
			mockllm.UsageChunk(session.Usage{InputTokens: 23, OutputTokens: 4}),
			mockllm.DoneChunk(session.StopCancelled),
		)),
		Catalog: catalogWith(t),
	})
	_ = drain(cancelledStream.Run(context.Background(), cancelled, agent.MemEnv("/ws"), agent.RunRequest{Text: "cancelled"}))
	if got, ok := cancelled.LatestContextOccupancy(); !ok || got != cancelledOccupancy {
		t.Fatalf("cancelled stream replaced context occupancy: got %+v, %v; want %+v, true", got, ok, cancelledOccupancy)
	}
}
