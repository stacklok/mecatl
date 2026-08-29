package ui

import (
	"context"
	"strconv"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

// TestScrollbackReviseAllocsIndependentOfN is the direct allocation-scaling guard
// for the streaming benchmark. It measures the real reviseAssistant+refreshView
// loop at two cumulative window sizes; fixed-size replacement must keep allocations
// per operation independent of the window size.
//
// The 50/200 windows are the smallest tested pair that was stable without weakening
// the former threshold: over 100 race-enabled runs, high-low ranged from -2 to +3
// allocs/op (distribution: -2:7, -1:25, 0:29, +1:29, +2:8, +3:2). Smaller 10/40
// and 20/80 windows reached +6 and +5 respectively. Three allocations is therefore
// measured race-instrumentation noise, while preserving a slightly tighter ceiling
// than the former ~3.5-allocation tolerance. An output-preserving mutation that grew
// off-screen history exceeded this ceiling by more than 35,000 allocations/op.
func TestScrollbackReviseAllocsIndependentOfN(t *testing.T) {
	measure := func(runs int) float64 {
		m := newReviseDeterminismModel(t)
		m.refreshView()
		op := 0
		return testing.AllocsPerRun(runs, func() {
			m.conv.reviseAssistant(reviseBody(op))
			op++
			m.refreshView()
		})
	}

	const (
		lowRuns   = 50
		highRuns  = 200
		tolerance = 3.0
	)
	low := measure(lowRuns)
	high := measure(highRuns)
	t.Logf("allocs/op: low(%d)=%.1f high(%d)=%.1f delta=%.1f", lowRuns, low, highRuns, high, high-low)
	if high > low+tolerance {
		t.Fatalf("per-op allocs scale with iteration count: low(%d)=%.1f high(%d)=%.1f (delta=%.1f, want <=%.1f); reviseAssistant must keep cumulative work bounded",
			lowRuns, low, highRuns, high, high-low, tolerance)
	}
}

// TestScrollbackReviseOutputIndependentOfN pins the streaming benchmark's
// iteration-independent behavior: every operation replaces the live assistant
// body with one fixed-size revision. A cumulatively revised model must therefore
// render exactly like an independently warmed model that received only the
// current revision.
func TestScrollbackReviseOutputIndependentOfN(t *testing.T) {
	cumulative := newReviseDeterminismModel(t)
	cumulative.refreshView()

	const ops = 4
	previous := ""
	for op := 0; op < ops; op++ {
		body := reviseBody(op)
		if len(body) != reviseBodyLen {
			t.Fatalf("op %d: reviseBody length = %d, want %d", op, len(body), reviseBodyLen)
		}
		if op > 0 && body == previous {
			t.Fatalf("op %d: consecutive reviseBody values are equal", op)
		}
		previous = body

		cumulative.conv.reviseAssistant(body)
		cumulative.refreshView()

		reference := newReviseDeterminismModel(t)
		reference.refreshView()
		reference.conv.reviseAssistant(body)
		reference.refreshView()

		got := cumulative.vp.View()
		want := reference.vp.View()
		if got != want {
			t.Fatalf("op %d: cumulative revisions changed rendered viewport; reviseAssistant must replace the current body\n got %q\nwant %q", op, got, want)
		}
		if rendered := stripANSIstr(got); !strings.Contains(rendered, body) {
			t.Fatalf("op %d: rendered viewport does not contain latest revision %q\nviewport: %q", op, body, rendered)
		}
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
// behavioral oracle stays cheap.
func newReviseDeterminismModel(tb testing.TB) Model {
	tb.Helper()
	const depth = 4
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
