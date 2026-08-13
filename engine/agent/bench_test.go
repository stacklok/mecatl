package agent_test

import (
	"context"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// benchEpoch is the fixed session start time for benchmark sessions — a
// deterministic clock seed, no Now()/random in the measured path.
var benchEpoch = time.Unix(0, 0)

// benchCatalog builds a tool.Catalog from the given tools. It mirrors the
// catalogWith test helper but takes no *testing.T, so it is usable from a
// benchmark's setup region.
func benchCatalog(tools ...tool.Tool) *tool.Catalog {
	c := tool.NewCatalog()
	for _, tl := range tools {
		c.MustRegister(tl)
	}
	return c
}

// sinkEvents is a package-level sink so the drained events of a benchmarked run are
// not optimised away.
var sinkEvents []session.Event

// BenchmarkRunReadOnlyTurn measures a full engine run that issues a single
// read-only tool call (Read) then completes — exercising the read-parallel
// dispatch path (runReadBatch). A fresh session is built each iteration because the
// session is a one-shot state machine, and the mockllm provider is Reset() so the
// scripted turns replay. The b.Loop()-excluded setup keeps the measured region to
// the run itself.
func BenchmarkRunReadOnlyTurn(b *testing.B) {
	read := &fakeTool{name: "Read", readOnly: true,
		exec: func(_ context.Context, in session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
			return session.NewToolResult(in.ID, "file contents"), nil
		}}
	cat := benchCatalog(read)

	llm := mockllm.New(
		mockllm.ChunksTurn(
			mockllm.TextChunk("let me look"),
			mockllm.ToolCallChunk(toolCall("c1", "Read", `{"path":"a.go"}`)),
			mockllm.UsageChunk(session.Usage{InputTokens: 10, OutputTokens: 2}),
			mockllm.DoneChunk(session.StopEndTurn),
		),
		mockllm.ChunksTurn(
			mockllm.TextChunk("all done"),
			mockllm.UsageChunk(session.Usage{InputTokens: 5, OutputTokens: 3}),
			mockllm.DoneChunk(session.StopEndTurn),
		),
	)
	e := newEngine(agent.Deps{LLM: llm, Catalog: cat})

	b.ReportAllocs()
	for b.Loop() {
		// Per-iteration benchSession() + llm.Reset() is INTENTIONAL: a session is a
		// one-shot state machine (it drives to a terminal state in one Run and cannot
		// be re-run), so it can't be hoisted out of the loop; Reset rewinds the mock
		// script so each iteration replays the same scripted turns.
		llm.Reset()
		sess := benchSession()
		r := e.Run(context.Background(), sess, agent.MemEnv("/ws"), agent.RunRequest{Text: "look at a.go"})
		sinkEvents = drain(r)
	}
}

// BenchmarkRunReadParallelTurn measures a full engine run whose assistant turn
// issues FOUR read-only tool calls in one message, so runReadBatch's concurrent
// fan-out + result-collection path is genuinely exercised (the single-call
// BenchmarkRunReadOnlyTurn barely distinguishes runReadBatch from runOne). Same
// fresh-session-per-iteration + Reset discipline.
func BenchmarkRunReadParallelTurn(b *testing.B) {
	read := &fakeTool{name: "Read", readOnly: true,
		exec: func(_ context.Context, in session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
			return session.NewToolResult(in.ID, "file contents"), nil
		}}
	cat := benchCatalog(read)

	llm := mockllm.New(
		mockllm.ChunksTurn(
			mockllm.TextChunk("reading several files"),
			mockllm.ToolCallChunk(toolCall("c1", "Read", `{"path":"a.go"}`)),
			mockllm.ToolCallChunk(toolCall("c2", "Read", `{"path":"b.go"}`)),
			mockllm.ToolCallChunk(toolCall("c3", "Read", `{"path":"c.go"}`)),
			mockllm.ToolCallChunk(toolCall("c4", "Read", `{"path":"d.go"}`)),
			mockllm.UsageChunk(session.Usage{InputTokens: 10, OutputTokens: 4}),
			mockllm.DoneChunk(session.StopEndTurn),
		),
		mockllm.ChunksTurn(
			mockllm.TextChunk("all done"),
			mockllm.UsageChunk(session.Usage{InputTokens: 5, OutputTokens: 3}),
			mockllm.DoneChunk(session.StopEndTurn),
		),
	)
	e := newEngine(agent.Deps{LLM: llm, Catalog: cat})

	b.ReportAllocs()
	for b.Loop() {
		// Per-iteration benchSession() + llm.Reset() is INTENTIONAL — see
		// BenchmarkRunReadOnlyTurn: a session is a one-shot state machine.
		llm.Reset()
		sess := benchSession()
		r := e.Run(context.Background(), sess, agent.MemEnv("/ws"), agent.RunRequest{Text: "read four files"})
		sinkEvents = drain(r)
	}
}

// BenchmarkRunMutatingTurn measures a full engine run that issues a single
// mutating tool call (Write) then completes — exercising the serial-dispatch path
// (runOne). Setup mirrors the read-only benchmark.
func BenchmarkRunMutatingTurn(b *testing.B) {
	write := &fakeTool{name: "Write", readOnly: false,
		exec: func(_ context.Context, in session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
			return session.NewToolResult(in.ID, "wrote file"), nil
		}}
	cat := benchCatalog(write)

	llm := mockllm.New(
		mockllm.ChunksTurn(
			mockllm.TextChunk("writing it"),
			mockllm.ToolCallChunk(toolCall("c1", "Write", `{"file_path":"a.go","content":"x"}`)),
			mockllm.UsageChunk(session.Usage{InputTokens: 10, OutputTokens: 2}),
			mockllm.DoneChunk(session.StopEndTurn),
		),
		mockllm.ChunksTurn(
			mockllm.TextChunk("all done"),
			mockllm.UsageChunk(session.Usage{InputTokens: 5, OutputTokens: 3}),
			mockllm.DoneChunk(session.StopEndTurn),
		),
	)
	e := newEngine(agent.Deps{LLM: llm, Catalog: cat})

	b.ReportAllocs()
	for b.Loop() {
		// Per-iteration benchSession() + llm.Reset() is INTENTIONAL — see
		// BenchmarkRunReadOnlyTurn: a session is a one-shot state machine.
		llm.Reset()
		sess := benchSession()
		r := e.Run(context.Background(), sess, agent.MemEnv("/ws"), agent.RunRequest{Text: "write a.go"})
		sinkEvents = drain(r)
	}
}

// benchSession builds a fresh default-mode session. The shared newSession helper
// takes a *testing.T; benchmarks construct the session directly to avoid that
// dependency while keeping the same parameters.
func benchSession() *session.Session {
	return session.New("bench", session.ModeDefault, "/ws", session.Limits{}, benchEpoch)
}
