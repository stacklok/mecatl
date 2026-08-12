package scenarios_test

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/perf/kpi"
)

// singleSessionTurns is the length of the scripted long session: each scripted
// turn does a text emit + a read-only tool call + usage, and a final no-tool turn
// terminates the run. ~500 turns targets the long-session allocation budget + RSS
// growth (perf-tracking.md "single-session-long").
const singleSessionTurns = 500

// singleSessionCacheReadFrac is the fixed fraction of input tokens served from
// cache in the scripted usage, so cache_hit_rate is deterministic and a
// prefix-stability regression in the loop would move it (the non-obvious KPI).
const singleSessionCacheReadFrac = 0.9

// buildSingleSessionScript returns a mockllm provider scripting
// singleSessionTurns tool-using turns + a terminal no-tool turn. Token figures
// are fixed (deterministic), with a constant cache-read fraction.
func buildSingleSessionScript() *mockllm.Provider {
	const inputTokens = 1000
	const outputTokens = 40
	cacheRead := int(float64(inputTokens) * singleSessionCacheReadFrac)

	turns := make([]mockllm.Turn, 0, singleSessionTurns+1)
	for i := 0; i < singleSessionTurns; i++ {
		turns = append(turns, mockllm.ChunksTurn(
			mockllm.TextChunk("inspecting the next file"),
			mockllm.ToolCallChunk(session.NewToolCall(
				session.ToolCallID("read-"+strconv.Itoa(i)), "Read", []byte(`{"path":"a.go"}`))),
			mockllm.UsageChunk(session.Usage{
				InputTokens:     inputTokens,
				OutputTokens:    outputTokens,
				CacheReadTokens: cacheRead,
			}),
			mockllm.DoneChunk(session.StopEndTurn),
		))
	}
	// Terminal turn: no tool call, so the loop completes cleanly.
	turns = append(turns, mockllm.ChunksTurn(
		mockllm.TextChunk("all files inspected; done"),
		mockllm.UsageChunk(session.Usage{InputTokens: inputTokens, OutputTokens: outputTokens, CacheReadTokens: cacheRead}),
		mockllm.DoneChunk(session.StopEndTurn),
	))
	return mockllm.New(turns...)
}

// BenchmarkSingleSessionLong drives a ~500-turn read-only loop through the real
// engine, offline, capturing the whole-loop allocation budget, RSS over the run,
// and the accumulated token / cache-hit KPIs. The RSS sampler runs across the
// Run via kpi.Capture. A fresh session + llm.Reset per iteration (a session is a
// one-shot state machine; Reset rewinds the script).
func BenchmarkSingleSessionLong(b *testing.B) {
	llm := buildSingleSessionScript()
	e := buildEngine(agent.Deps{
		LLM:     llm,
		Catalog: scenarioCatalog(readTool{}),
		// A high turn/tool ceiling so the scripted length, not a limit, ends the run.
		// ContextWindowTokens left 0 → compaction never triggers (compaction has its
		// own scenario); this isolates the long-session allocation budget.
	})
	// MaxTurns/MaxToolCalls must exceed the script length so the script terminates
	// the run, not the limit.
	limits := session.Limits{MaxTurns: singleSessionTurns + 5, MaxToolCalls: singleSessionTurns + 5}

	var lastSess *session.Session
	// Baseline goroutine count BEFORE the measured region, so GoroutinesEnd is a
	// LEAK DELTA (end-minus-baseline, clamped at 0) rather than the raw count.
	baselineGoroutines := kpi.GoroutinesAfterSettle(20 * time.Millisecond)
	capt := kpi.NewCapture()
	b.ReportAllocs()
	capt.Begin()
	for b.Loop() {
		llm.Reset()
		sess := scenarioSession("perf-single", limits)
		r := e.Run(context.Background(), sess, scenarioWorkspace(), agent.RunRequest{Text: "inspect every file then summarise"})
		sinkInt = drain(r)
		lastSess = sess
	}
	m := capt.End()

	u := session.Usage{}
	if lastSess != nil {
		u = lastSess.Usage
	}
	addResult(kpi.ScenarioResult{
		Name:            "single_session_long",
		Iterations:      b.N,
		AllocsPerOp:     perOp(m.Allocs, b.N),
		BytesPerOp:      perOp(m.Bytes, b.N),
		GoroutinesEnd:   kpi.GoroutineDelta(baselineGoroutines, 20*time.Millisecond),
		TokensInput:     int64(u.InputTokens),
		TokensOutput:    int64(u.OutputTokens),
		TokensCacheRead: int64(u.CacheReadTokens),
		CacheHitRate:    u.CacheHitRate(),
		RSSPeakBytes:    m.RSSPeak,
		RSSFinalBytes:   m.RSSFinal,
		WallClockNs:     m.WallNs,
	})
}

// sinkInt defeats dead-code elimination of the drained event count.
var sinkInt int

// perOp divides a captured total by the iteration count, guarding n==0.
func perOp(total uint64, n int) uint64 {
	if n <= 0 {
		return 0
	}
	return total / uint64(n)
}

// ensure context import stays honest if the body is edited.
var _ = context.Background
