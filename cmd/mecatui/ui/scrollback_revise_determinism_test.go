package ui

import (
	"context"
	"strconv"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

// TestScrollbackReviseAllocsIndependentOfN is the determinism guard for the
// reviseAssistant streaming-bench mechanism (scrollback_bench_test.go): it proves
// that the per-op allocation count of the revise+refreshView loop does NOT depend
// on the iteration count. The old mechanism appended a byte per op, so the live
// block (and the markdown it rendered) GREW with the loop, making allocs/op a
// function of b.N — a determinism hazard that made the gated scrollback suite
// flaky. The fixed-size revision keeps per-op work constant.
//
// It measures testing.AllocsPerRun at two very different iteration counts and
// asserts they agree within ±1 alloc. ±1 (not exact equality) absorbs the rare
// boundary alloc (e.g. a map grow on a cold run) without masking the regression
// this guards: re-introducing unbounded per-op growth would make the high-count
// measurement many allocs higher than the low-count one, far outside ±1.
//
// If this test is ever too noisy at ±1, widen the epsilon — but it MUST stay a
// meaningful guard: deleting reviseAssistant's fixed-size discipline (e.g.
// switching it back to b.raw += text) must make it FAIL.
func TestScrollbackReviseAllocsIndependentOfN(t *testing.T) {
	// A modest scrollback so the test stays cheap and offline; the absolute alloc
	// count is irrelevant — only its INVARIANCE across iteration counts matters.
	build := func() Model {
		m := newReviseDeterminismModel(t)
		m.refreshView() // warm the per-block + join caches before measuring
		return m
	}

	measure := func(runs int) float64 {
		m := build()
		op := 0
		return testing.AllocsPerRun(runs, func() {
			m.conv.reviseAssistant(reviseBody(op))
			op++
			m.refreshView()
		})
	}

	const lowRuns = 50
	const highRuns = 2000

	low := measure(lowRuns)
	high := measure(highRuns)

	delta := high - low
	if delta < 0 {
		delta = -delta
	}
	// ±1 alloc tolerance: equal per-op allocs prove the cost is N-independent; a
	// regression to unbounded-growth-per-op would blow well past 1.
	if delta > 1 {
		t.Fatalf("per-op allocs depend on iteration count: low(%d)=%.1f high(%d)=%.1f (Δ=%.1f, want ≤1)\n"+
			"this means the live block grows per op again — reviseAssistant must keep a FIXED-size body",
			lowRuns, low, highRuns, high, delta)
	}
}

// TestScrollbackReviseBustsCacheEveryOp pins the "join still all-misses" invariant
// the streaming bench relies on: each reviseAssistant + refreshView must re-render
// the live block (a renderer.blockCache MISS, which bumps blockRenders), so the join
// cache also misses and the whole scrollback re-joins — the worst-case streaming
// floor the bench is meant to measure. The miss is driven by reviseAssistant's
// currentAssistant() rev bump (blockCache keys on rev); the fixed-size byte-different
// body additionally keeps the markdownAt src-cache missing, so the per-op render is
// real, representative work. If a future cache change let a revise HIT (so the bench
// measured ~nothing), blockRenders would stop advancing on some op and this FAILS.
// Mutation-verified: deleting the currentAssistant() rev bump makes it fail at op 0.
// It is the behavioural complement to the structural rev-bump comment on
// reviseAssistant.
func TestScrollbackReviseBustsCacheEveryOp(t *testing.T) {
	m := newReviseDeterminismModel(t)
	m.refreshView() // warm caches; subsequent revises must each still miss

	const ops = 8
	for op := 0; op < ops; op++ {
		before := m.rend.blockRenders
		m.conv.reviseAssistant(reviseBody(op))
		m.refreshView()
		if m.rend.blockRenders <= before {
			t.Fatalf("op %d: blockRenders did not advance (before=%d after=%d): the revise HIT the render cache, "+
				"so the bench would measure ~nothing — reviseAssistant must bust the cache every op",
				op, before, m.rend.blockRenders)
		}
	}
}

// newReviseDeterminismModel builds a small connected, sized Model with a handful
// of settled blocks plus a live assistant block to revise. It mirrors
// buildScrollbackModel's reducer path but at a fraction of the depth so the
// determinism test stays cheap (the absolute alloc count is irrelevant; only its
// invariance across iteration counts is asserted).
func newReviseDeterminismModel(tb testing.TB) Model {
	tb.Helper()
	const depth = 16
	m := New(Deps{
		Theme:       theme.New("aztec", theme.AztecPalette()),
		Ctx:         context.Background(),
		NoAltScreen: true,
	})
	m = applyAll(m,
		tea.WindowSizeMsg{Width: 100, Height: 40},
		client.SessionReadyMsg{SessionID: "perf-revise-0001"},
		client.TurnStartMsg{Turn: 1},
	)
	m.phase = phaseRunning
	for i := 0; i < depth; i++ {
		id := "call-" + strconv.Itoa(i)
		m.conv.addUser("Question number " + strconv.Itoa(i) + ".")
		m.conv.startAssistant()
		m.conv.appendAssistant("Answer for step " + strconv.Itoa(i) + ".\n")
		m.conv.addTool(id, "Read", `{"path":"pkg/file`+strconv.Itoa(i)+`.go"}`)
		m.conv.resolveTool(id, "package main\n", false)
	}
	// Open a live assistant block for reviseAssistant to target each op.
	m.conv.startAssistant()
	m.conv.appendAssistant("priming the live block")
	return m
}
