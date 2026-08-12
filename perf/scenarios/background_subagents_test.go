package scenarios_test

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/perf/kpi"
)

// backgroundChildren is the number of detached background subagents the scenario
// starts before draining — it targets the child-run registry / drain / seal path
// that has leaked before (perf-tracking.md "background-subagents"). The end-of-run
// goroutine count is the headline KPI.
const backgroundChildren = 8

// runBackgroundSubagents drives a parent that starts M background children, waits
// for them all, then completes — exercising the background registry, notice/nudge,
// and run-end drain. Each child is a one-turn investigator over its own engine.
// Mirrors demo.go's RunBackgroundScenario, parametrised to M children.
func runBackgroundSubagents(ctx context.Context) int {
	// The background children: one-turn investigators sharing a child engine.
	childLLM := mockllm.New(scriptChildTurns(backgroundChildren)...)
	childEngine := buildEngine(agent.Deps{LLM: childLLM, Catalog: tool.NewCatalog()})

	// Parent script: start M background subagents in one turn, then a wait-all turn,
	// then a final no-tool turn. The harness completion-notice fires at the turn
	// boundary; the run-end drain cancels+joins anything still live.
	startTurn := make([]port.Chunk, 0, backgroundChildren+2)
	startTurn = append(startTurn, mockllm.TextChunk("Starting background subagents to verify slices."))
	for i := 0; i < backgroundChildren; i++ {
		startTurn = append(startTurn, mockllm.ToolCallChunk(
			session.NewToolCall(session.ToolCallID("bg-"+strconv.Itoa(i)), "Subagent",
				[]byte(`{"prompt":"verify slice `+strconv.Itoa(i)+` in the background","background":true}`))))
	}
	startTurn = append(startTurn, mockllm.DoneChunk(session.StopEndTurn))

	parentLLM := mockllm.New(
		mockllm.ChunksTurn(startTurn...),
		mockllm.ChunksTurn(
			mockllm.TextChunk("Waiting for the background subagents to finish."),
			mockllm.ToolCallChunk(session.NewToolCall("bg-wait", "SubagentStatus", []byte(`{"wait_ms":30000}`))),
			mockllm.DoneChunk(session.StopEndTurn),
		),
		mockllm.ChunksTurn(
			mockllm.TextChunk("Done: all background subagents verified their slices."),
			mockllm.DoneChunk(session.StopEndTurn),
		),
	)

	cat := tool.NewCatalog()
	cat.MustRegister(agent.NewSubagentTool(childEngine, agent.WithSubagentStore(memstore.New())))
	cat.MustRegister(agent.NewSubagentStatusTool())
	engine := buildEngine(agent.Deps{LLM: parentLLM, Catalog: cat, Store: memstore.New()})

	sess := scenarioSession("perf-background",
		session.Limits{MaxTurns: 8, MaxToolCalls: backgroundChildren + 8, MaxConsecutiveFailures: 3})
	r := engine.Run(ctx, sess, scenarioWorkspace(), agent.RunRequest{Text: "verify every slice in the background, then report"})
	return drain(r)
}

// scriptChildTurns returns n one-turn text scripts (one per background child; the
// shared child engine drives each fork sequentially against this rewound-per-fork
// script — mockllm replays from the start for each child Run).
func scriptChildTurns(n int) []mockllm.Turn {
	turns := make([]mockllm.Turn, 0, n)
	for i := 0; i < n; i++ {
		turns = append(turns, mockllm.TextTurn("Background check complete: slice "+strconv.Itoa(i)+" is intact."))
	}
	return turns
}

// BenchmarkBackgroundSubagents drives M detached children + a run-end drain
// through the real engine, offline. The headline KPI is the end-of-run goroutine
// count (settled), which catches the registry/drain/seal leak class.
func BenchmarkBackgroundSubagents(b *testing.B) {
	// Baseline BEFORE the measured region: GoroutinesEnd is a leak DELTA, so a
	// background child the run-end drain failed to join shows up as a positive
	// number — exactly the registry/drain/seal leak class this scenario guards.
	baselineGoroutines := kpi.GoroutinesAfterSettle(100 * time.Millisecond)
	capt := kpi.NewCapture()
	b.ReportAllocs()
	capt.Begin()
	for b.Loop() {
		sinkInt = runBackgroundSubagents(context.Background())
	}
	m := capt.End()

	addResult(kpi.ScenarioResult{
		Name:          "background_subagents",
		Iterations:    b.N,
		AllocsPerOp:   perOp(m.Allocs, b.N),
		BytesPerOp:    perOp(m.Bytes, b.N),
		GoroutinesEnd: kpi.GoroutineDelta(baselineGoroutines, 100*time.Millisecond),
		RSSPeakBytes:  m.RSSPeak,
		RSSFinalBytes: m.RSSFinal,
		WallClockNs:   m.WallNs,
	})
}
