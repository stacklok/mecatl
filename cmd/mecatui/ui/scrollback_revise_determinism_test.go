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
// Precision note — why we assert a relative tolerance band rather than exact
// equality: testing.AllocsPerRun returns floor(process-wide malloc delta / runs).
// Under -race, background goroutines (race-detector bookkeeping, GC workers)
// contribute a small, N-INDEPENDENT residue of process-wide allocations. Because
// the high-count window runs ~4× longer wall-clock than the low-count window, more
// background mallocs accumulate in it; when floor-divided by the run count the
// quotient can tip +1 or +2 relative to the low window. This is purely an
// instrument artefact: the real regression this guard targets (re-introducing
// `b.raw += text` unbounded growth) makes high grow with N, producing a delta on
// the order of ~10^5 allocs/op — orders of magnitude larger than the ±2 noise
// band.
//
// The assertion therefore checks a relative tolerance BAND whose width scales with
// the baseline (not a pure ratio): high must not exceed low by more than
// max(2, low*0.001). With low≈3469 the tolerance is ≈3.5 allocs (~0.1%), safely
// above the observed ±2 residue and safely below any real regression (which
// overshoots it by ~100×+).
//
// Cross-reference: .github/workflows/perf.yml already classifies the
// tui_scrollback_view* render-alloc scenario suite as ADVISORY
// (fail-on-alert:false) for the same reason — "non-deterministic on shared
// runner". This test was the last hard pass/fail on the same noisy metric;
// the tolerance band brings it in line with that guidance while keeping the
// guard meaningful.
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

	// lowRuns raised 50→500 so the wall-clock ratio between the two measurement
	// windows drops from ~40× to ~4×, cutting the differential background-goroutine
	// residue that caused the -race flake.
	const lowRuns = 500
	const highRuns = 2000

	low := measure(lowRuns)
	high := measure(highRuns)

	// Relative tolerance band — see the header comment above.
	// tolerance = max(2, low*0.001) (≈3.5 allocs at low≈3469): absorbs the ±2
	// N-independent instrument residue under -race while a real b.raw+=text
	// regression (order ~10^5 allocs/op) exceeds it by ~100×+.
	tolerance := low * 0.001
	if tolerance < 2 {
		tolerance = 2
	}
	if high > low+tolerance {
		t.Fatalf("per-op allocs scale with iteration count: low(%d)=%.1f high(%d)=%.1f (Δ=%.1f, want ≤%.1f)\n"+
			"this means the live block grows per op again — reviseAssistant must keep a FIXED-size body",
			lowRuns, low, highRuns, high, high-low, tolerance)
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
	m := newTestModelFromDeps(Deps{
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
