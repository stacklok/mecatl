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

// BenchmarkRunReadOnlyTurn measures a full **startup-inclusive** engine run that
// issues a single read-only tool call (Read) then completes — exercising the
// read-parallel dispatch path (runReadBatch). A fresh session is built each
// iteration because the session is a one-shot state machine, and the mockllm
// provider is Reset() so the scripted turns replay. The b.Loop()-excluded setup
// keeps the measured region to first-session/first-prompt run work, including
// bounded session lifecycle initialization.
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

// BenchmarkRunReadParallelTurn measures a **startup-inclusive** full engine run
// whose assistant turn issues FOUR read-only tool calls in one message, so
// runReadBatch's concurrent fan-out + result-collection path is genuinely
// exercised (the single-call BenchmarkRunReadOnlyTurn barely distinguishes
// runReadBatch from runOne). It uses the same fresh-session-per-iteration + Reset
// discipline, so bounded first-session lifecycle initialization is in the
// measurement.
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

// BenchmarkRunMutatingTurn measures a **startup-inclusive** full engine run that
// issues a single mutating tool call (Write) then completes — exercising the
// serial-dispatch path (runOne). It includes bounded first-session lifecycle work;
// BenchmarkSteadyStateMutatingTurn measures the recurring counterpart.
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

// BenchmarkSteadyStateReadOnlyTurn measures a recurring continuation turn after
// a completed first run. Session creation and first-session initialization are
// prewarmed outside b.Loop(); Reopen and the follow-up run remain measured.
func BenchmarkSteadyStateReadOnlyTurn(b *testing.B) {
	read := &fakeTool{name: "Read", readOnly: true,
		exec: func(_ context.Context, in session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
			return session.NewToolResult(in.ID, "file contents"), nil
		}}
	llm := mockllm.New(
		mockllm.ChunksTurn(mockllm.TextChunk("let me look"), mockllm.ToolCallChunk(toolCall("c1", "Read", `{"path":"a.go"}`)), mockllm.UsageChunk(session.Usage{InputTokens: 10, OutputTokens: 2}), mockllm.DoneChunk(session.StopEndTurn)),
		mockllm.ChunksTurn(mockllm.TextChunk("all done"), mockllm.UsageChunk(session.Usage{InputTokens: 5, OutputTokens: 3}), mockllm.DoneChunk(session.StopEndTurn)),
	)
	benchmarkSteadyStateTurn(b, newEngine(agent.Deps{LLM: llm, Catalog: benchCatalog(read)}), llm, "look at a.go")
}

// BenchmarkSteadyStateReadParallelTurn measures a recurring continuation turn
// that fans out four read-only calls after first-session initialization.
func BenchmarkSteadyStateReadParallelTurn(b *testing.B) {
	read := &fakeTool{name: "Read", readOnly: true,
		exec: func(_ context.Context, in session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
			return session.NewToolResult(in.ID, "file contents"), nil
		}}
	llm := mockllm.New(
		mockllm.ChunksTurn(mockllm.TextChunk("reading several files"), mockllm.ToolCallChunk(toolCall("c1", "Read", `{"path":"a.go"}`)), mockllm.ToolCallChunk(toolCall("c2", "Read", `{"path":"b.go"}`)), mockllm.ToolCallChunk(toolCall("c3", "Read", `{"path":"c.go"}`)), mockllm.ToolCallChunk(toolCall("c4", "Read", `{"path":"d.go"}`)), mockllm.UsageChunk(session.Usage{InputTokens: 10, OutputTokens: 4}), mockllm.DoneChunk(session.StopEndTurn)),
		mockllm.ChunksTurn(mockllm.TextChunk("all done"), mockllm.UsageChunk(session.Usage{InputTokens: 5, OutputTokens: 3}), mockllm.DoneChunk(session.StopEndTurn)),
	)
	benchmarkSteadyStateTurn(b, newEngine(agent.Deps{LLM: llm, Catalog: benchCatalog(read)}), llm, "read four files")
}

// BenchmarkSteadyStateMutatingTurn measures a recurring continuation turn that
// issues one mutating call after first-session initialization.
func BenchmarkSteadyStateMutatingTurn(b *testing.B) {
	write := &fakeTool{name: "Write", readOnly: false,
		exec: func(_ context.Context, in session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
			return session.NewToolResult(in.ID, "wrote file"), nil
		}}
	llm := mockllm.New(
		mockllm.ChunksTurn(mockllm.TextChunk("writing it"), mockllm.ToolCallChunk(toolCall("c1", "Write", `{"file_path":"a.go","content":"x"}`)), mockllm.UsageChunk(session.Usage{InputTokens: 10, OutputTokens: 2}), mockllm.DoneChunk(session.StopEndTurn)),
		mockllm.ChunksTurn(mockllm.TextChunk("all done"), mockllm.UsageChunk(session.Usage{InputTokens: 5, OutputTokens: 3}), mockllm.DoneChunk(session.StopEndTurn)),
	)
	benchmarkSteadyStateTurn(b, newEngine(agent.Deps{LLM: llm, Catalog: benchCatalog(write)}), llm, "write a.go")
}

// benchmarkSteadyStateTurn excludes first-session work from each sample by
// prewarming a fresh completed session while its benchmark timer is stopped, then
// measuring exactly one normal continuation run. A fresh session per sample keeps
// growing conversation history out of the measurement.
func benchmarkSteadyStateTurn(b *testing.B, e *agent.Engine, llm *mockllm.Provider, prompt string) {
	b.ReportAllocs()
	for b.Loop() {
		b.StopTimer()
		llm.Reset()
		sess := benchSession()
		sinkEvents = drain(e.Run(context.Background(), sess, agent.MemEnv("/ws"), agent.RunRequest{Text: prompt}))
		b.StartTimer()

		if err := sess.Reopen(); err != nil {
			b.Fatal(err)
		}
		llm.Reset()
		r := e.Run(context.Background(), sess, agent.MemEnv("/ws"), agent.RunRequest{Text: prompt})
		sinkEvents = drain(r)
	}
}

// benchSession builds a fresh default-mode session. The shared newSession helper
// takes a *testing.T; benchmarks construct the session directly to avoid that
// dependency while keeping the same parameters.
func benchSession() *session.Session {
	return session.New("bench", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{}, benchEpoch)
}
