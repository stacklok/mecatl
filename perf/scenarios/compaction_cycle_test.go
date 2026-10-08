package scenarios_test

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/perf/kpi"
)

// compactionTurns is the number of bulky turns the scenario drives so the
// HeuristicCompactor fires repeatedly: each turn appends a large assistant text,
// growing the history past the small context window, then a no-tool turn ends the
// run (perf-tracking.md "compaction-cycle"). This tunes only INJECTABLE Deps
// (ContextWindowTokens) — it changes no production behaviour.
const compactionTurns = 40

// compactionContextWindow is a deliberately small context window (in tokens) so a
// few bulky turns cross the 0.8× compaction threshold over and over. With the
// default HeuristicTokenCounter (~chars/divisor), the bulky text below sizes each
// turn well above the per-turn budget, so compaction is exercised on most turns.
const compactionContextWindow = 4000

// bulkyText is a large assistant body (a few KB) so each recorded turn pushes the
// counted history toward the compaction threshold.
var bulkyText = strings.Repeat("the quick brown fox jumps over the lazy dog. ", 200)

// buildCompactionScript scripts compactionTurns bulky tool-using turns plus a
// final terminating turn. Each turn emits a large assistant body AND a read-only
// tool call so the loop CONTINUES to the next turn (a no-tool turn would end the
// run); the accumulating bulky history is what crosses the small context window
// and drives compaction at the turn boundary. The terminal turn emits no tool
// call so the run completes.
func buildCompactionScript() *mockllm.Provider {
	turns := make([]mockllm.Turn, 0, compactionTurns+1)
	for i := 0; i < compactionTurns; i++ {
		turns = append(turns, mockllm.ChunksTurn(
			mockllm.TextChunk(bulkyText),
			mockllm.ToolCallChunk(session.NewToolCall(
				session.ToolCallID("c-"+strconv.Itoa(i)), "Read", []byte(`{"path":"a.go"}`))),
			mockllm.UsageChunk(session.Usage{InputTokens: 500, OutputTokens: 500}),
			mockllm.DoneChunk(session.StopEndTurn),
		))
	}
	turns = append(turns, mockllm.ChunksTurn(
		mockllm.TextChunk("done"),
		mockllm.UsageChunk(session.Usage{InputTokens: 100, OutputTokens: 10}),
		mockllm.DoneChunk(session.StopEndTurn),
	))
	return mockllm.New(turns...)
}

// BenchmarkCompactionCycle drives history past the compaction threshold
// repeatedly with the default HeuristicCompactor + HeuristicTokenCounter, offline,
// capturing the allocation churn of the compact-and-replace cycle. Only injectable
// Deps are tuned (ContextWindowTokens); no production behaviour changes.
func BenchmarkCompactionCycle(b *testing.B) {
	llm := buildCompactionScript()
	e := buildEngine(agent.Deps{
		LLM:           llm,
		Catalog:       scenarioCatalog(readTool{}),
		ContextWindow: func() int { return compactionContextWindow },
		// Compactor + TokenCounter left nil → HeuristicCompactor + HeuristicTokenCounter
		// defaults (applied in NewEngine).
	})
	limits := session.Limits{MaxTurns: compactionTurns + 5, MaxToolCalls: compactionTurns + 5}

	var lastSess *session.Session
	// Baseline BEFORE the measured region: GoroutinesEnd is a leak DELTA (the same
	// treatment all scenarios use, for consistency — compaction is not a delegation
	// path, so this is expected to stay 0).
	baselineGoroutines := kpi.GoroutinesAfterSettle(20 * time.Millisecond)
	capt := kpi.NewCapture()
	b.ReportAllocs()
	capt.Begin()
	for b.Loop() {
		llm.Reset()
		sess := scenarioSession("perf-compaction", limits)
		r := e.Run(context.Background(), sess, scenarioWorkspace(), agent.RunRequest{Text: "keep going"})
		sinkInt = drain(r)
		lastSess = sess
	}
	m := capt.End()

	u := session.Usage{}
	if lastSess != nil {
		u = lastSess.UsageFor(session.UsageKindMain)
	}
	addResult(kpi.ScenarioResult{
		Name:            "compaction_cycle",
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
