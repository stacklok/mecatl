package scenarios_test

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memledger"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/team"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/perf/kpi"
)

// teamFanoutMembers is the number of worker members (plus a lead) the team
// scenario fans out — it targets goroutine hygiene + per-round allocation under
// delegation (perf-tracking.md "team-fanout").
const teamFanoutMembers = 6

// runTeamFanout drives one fully-offline team round: a lead delegates, each
// worker records a finding, and the lead synthesises. It returns the outcome so
// the benchmark can read TeamOutcome.Usage. It mirrors demo.go's RunTeamScenario
// but with K workers and is offline (mockllm + memfs + memstore).
func runTeamFanout(ctx context.Context) agent.TeamOutcome {
	tm := team.New("perf-team")
	base := memfs.NewWorkspace(scenarioWorkspaceRoot)

	// One script per member name. The lead delegates then synthesises; each worker
	// records a finding then reports.
	// Each turn carries a usage chunk so the team-accumulated token KPI is
	// meaningful (TextTurn/ToolCallTurn emit zero usage).
	usage := session.Usage{InputTokens: 800, OutputTokens: 120, CacheReadTokens: 600}
	scripts := map[string]*mockllm.Provider{
		"lead": mockllm.New(
			mockllm.ChunksTurn(
				mockllm.TextChunk("Delegating the inspection to the workers."),
				mockllm.UsageChunk(usage),
				mockllm.DoneChunk(session.StopEndTurn),
			),
			mockllm.ChunksTurn(
				mockllm.TextChunk("Consolidated report: all workers confirmed their slices read cleanly; nothing to fix."),
				mockllm.UsageChunk(usage),
				mockllm.DoneChunk(session.StopEndTurn),
			),
		),
	}
	for i := 0; i < teamFanoutMembers; i++ {
		name := "worker-" + strconv.Itoa(i)
		finding := session.NewToolCall(
			session.ToolCallID("f-"+strconv.Itoa(i)), "RecordFinding",
			[]byte(`{"finding":"slice `+strconv.Itoa(i)+` reads cleanly; no issues"}`))
		scripts[name] = mockllm.New(
			mockllm.ChunksTurn(
				mockllm.ToolCallChunk(finding),
				mockllm.UsageChunk(usage),
				mockllm.DoneChunk(session.StopEndTurn),
			),
			mockllm.ChunksTurn(
				mockllm.TextChunk("Inspection complete; finding recorded."),
				mockllm.UsageChunk(usage),
				mockllm.DoneChunk(session.StopEndTurn),
			),
		)
	}

	factory := func(spec agent.MemberSpec, _ string) agent.MemberBuild {
		prov, ok := scripts[spec.Name]
		if !ok {
			return agent.MemberBuild{}
		}
		cat := tool.NewCatalog()
		for _, tl := range agent.MemberTools(tm, spec.Name, nil) {
			cat.MustRegister(tl)
		}
		return agent.MemberBuild{Engine: buildEngine(agent.Deps{LLM: prov, Catalog: cat})}
	}

	baseEnv := tool.MustEnvironment(session.EnvironmentRef{Kind: session.EnvKindMem, ID: scenarioWorkspaceRoot, Revision: scenarioWorkspaceRevision}, base, memledger.New(), nil)
	sup := agent.NewSupervisor(tm, baseEnv, factory,
		agent.WithTeamReadLedgerFactory(func() tool.ReadLedger { return memledger.New() }),
		agent.WithTeamGoal("verify every slice of the codebase reads cleanly"),
		agent.WithMemberStore(memstore.New()),
		agent.WithMemberSessionPrefix("perf-team"),
		agent.WithMaxRounds(6))

	// The lead must be enrolled first.
	_ = sup.AddMember(ctx, agent.MemberSpec{Name: "lead", Lead: true, InitialPrompt: "coordinate the verification"})
	for i := 0; i < teamFanoutMembers; i++ {
		name := "worker-" + strconv.Itoa(i)
		_ = sup.AddMember(ctx, agent.MemberSpec{Name: name, InitialPrompt: "inspect your slice and report"})
	}
	return sup.Run(ctx, nil)
}

// BenchmarkTeamFanout drives a lead + K workers through the real Supervisor,
// offline, capturing per-round allocation, the team-accumulated token usage, and
// the end-of-run goroutine count (the delegation leak class).
func BenchmarkTeamFanout(b *testing.B) {
	var lastOutcome agent.TeamOutcome
	// Baseline BEFORE the measured region: GoroutinesEnd is a leak DELTA, so a
	// supervisor/member goroutine left running shows up as a small positive number.
	baselineGoroutines := kpi.GoroutinesAfterSettle(50 * time.Millisecond)
	capt := kpi.NewCapture()
	b.ReportAllocs()
	capt.Begin()
	for b.Loop() {
		lastOutcome = runTeamFanout(context.Background())
	}
	m := capt.End()

	u := lastOutcome.Usage
	addResult(kpi.ScenarioResult{
		Name:            "team_fanout",
		Iterations:      b.N,
		AllocsPerOp:     perOp(m.Allocs, b.N),
		BytesPerOp:      perOp(m.Bytes, b.N),
		GoroutinesEnd:   kpi.GoroutineDelta(baselineGoroutines, 50*time.Millisecond),
		TokensInput:     int64(u.InputTokens),
		TokensOutput:    int64(u.OutputTokens),
		TokensCacheRead: int64(u.CacheReadTokens),
		CacheHitRate:    u.CacheHitRate(),
		RSSPeakBytes:    m.RSSPeak,
		RSSFinalBytes:   m.RSSFinal,
		WallClockNs:     m.WallNs,
	})
}
