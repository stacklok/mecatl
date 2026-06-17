package ui

// Offline performance benchmark for the mecatui scrollback render path
// (perf-tracking.md Phase 2, "tui-scrollback"): it targets the per-frame string
// JOIN of all blocks in renderConversation — the profile-confirmed O(scrollback)
// hotspot (~91% of per-frame allocations was strings.Builder.WriteString copying
// every cached block string into a fresh Builder each frame; vp.SetContent's line
// split is the smaller residual). It lives in the ui package (an internal _test
// file) so it can reach the unexported render path; perf/kpi is imported ONLY here,
// never by the production ui package (the production package must stay free of the
// perf dependency).
//
// TWO benchmarks: BenchmarkScrollbackView REVISES the live block every op (the
// streaming frame — the join must rebuild, the join cache cannot help it, so it is
// the worst-case floor), and BenchmarkScrollbackViewSteady re-renders without
// mutating (the unchanged frame — cursor move, scroll, the twice-per-message
// renderInput) which the join cache serves from memo, so its B/op collapses.
//
// The streaming bench REVISES (replaces) the live block with a FIXED-SIZE but
// byte-DIFFERENT string each op via reviseAssistant, rather than APPENDING a byte
// (which grew the block — and the markdown it renders — without bound, making
// per-op work creep up with the iteration count and the captured allocs/op a
// function of b.N: a determinism hazard on the gated suite). A fixed-size revision
// still misses markdownAt's src-keyed cache every op (so the live block re-renders,
// blockRenders bumps, and the join still ALL-MISSES — the same worst-case streaming
// floor), but the live block no longer grows, so allocs/op is b.N-independent. The
// determinism guard for this is TestScrollbackReviseAllocsIndependentOfN.
//
// It is a Benchmark, so `task test` (default -run) never runs it; it runs under
// `task perf:scenarios`. It records a kpi.ScenarioResult with NO token KPIs (a
// render bench has no model usage) into a package-local accumulator flushed by
// the TestMain in scrollback_bench_main_test.go.

import (
	"context"
	"strconv"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
	"github.com/stacklok/mecatl/perf/kpi"
)

// scrollbackBlocks is the scrollback depth the bench builds: a large, settled
// conversation so the per-frame join + SetContent dominates and a regression in
// the O(scrollback) cost shows up in allocs/op and ns/op.
const scrollbackBlocks = 400

// buildScrollbackModel constructs a connected, sized Model and pushes
// scrollbackBlocks settled blocks through the conversation reducer path (a mix of
// user prompts, resolved tool cards, and assistant turns — the common settled
// shapes). It returns the model ready to refreshView.
func buildScrollbackModel(tb testing.TB) Model {
	tb.Helper()
	m := New(Deps{
		Theme:       theme.New("aztec", theme.AztecPalette()),
		Ctx:         context.Background(),
		NoAltScreen: true,
	})
	m = applyAll(m,
		tea.WindowSizeMsg{Width: 100, Height: 40},
		client.SessionReadyMsg{SessionID: "perf-scrollback-0001"},
		client.TurnStartMsg{Turn: 1},
	)
	m.phase = phaseRunning

	for i := 0; i < scrollbackBlocks; i++ {
		id := "call-" + strconv.Itoa(i)
		m.conv.addUser("Question number " + strconv.Itoa(i) + ": please inspect the file and summarise the result.")
		m.conv.startAssistant()
		m.conv.appendAssistant("Here is **the** answer for step " + strconv.Itoa(i) +
			".\n\n- read the file\n- made the edit\n- ran the tests\n\nThe change is small and self-contained.\n")
		m.conv.addTool(id, "Read", `{"path":"pkg/file`+strconv.Itoa(i)+`.go"}`)
		m.conv.resolveTool(id, "package main\n\nfunc main() {}\n", false)
	}
	return m
}

// reviseBodyLen is the fixed length (in bytes) of the live-block body the
// streaming bench revises each op. It is large enough that the body renders as a
// non-trivial markdown block (so the per-op render is representative work) and
// fixed so the per-op cost — and therefore the captured allocs/op — does not drift
// with the iteration count.
const reviseBodyLen = 64

// reviseBody returns a reviseBodyLen-byte body whose bytes differ from the
// previous op's (it rotates a trailing character through a small alphabet by op
// index) so markdownAt's src-keyed cache MISSES every op, while the LENGTH stays
// constant so the live block never grows. The body is plain printable ASCII (no
// markdown control characters / emoji) so the render work is stable and the
// width-normalization fast path is hit identically each op.
func reviseBody(op int) string {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	buf := make([]byte, reviseBodyLen)
	for i := range buf {
		buf[i] = 'x'
	}
	// Rotate the trailing byte so consecutive ops differ; the rest stays constant.
	buf[reviseBodyLen-1] = alphabet[op%len(alphabet)]
	return string(buf)
}

// BenchmarkScrollbackView measures the conversation render path over a large
// settled scrollback: the measured region is m.refreshView() (the string join +
// vp.SetContent line split/measure). Allocations are the gated KPI.
func BenchmarkScrollbackView(b *testing.B) {
	m := buildScrollbackModel(b)
	// Prime once so the per-block render cache is warm; the measured region then
	// reflects the steady-state per-frame join + SetContent cost, not first-render.
	m.refreshView()

	// Baseline BEFORE the measured region: GoroutinesEnd is a leak DELTA, the same
	// treatment all scenarios use (the render path spawns no goroutines, so this is
	// expected to stay 0 — kept consistent with the delegation scenarios).
	baselineGoroutines := kpi.GoroutinesAfterSettle(20 * time.Millisecond)
	capt := kpi.NewCapture()
	b.ReportAllocs()
	capt.Begin()
	op := 0
	for b.Loop() {
		// REVISE the live block with a fixed-size, byte-different body each op so
		// refreshView does real work (markdownAt misses, the live block re-renders,
		// the whole scrollback re-joins) without the block GROWING — keeping per-op
		// work, and therefore allocs/op, independent of b.N. This mirrors the
		// steady-state streaming frame: one live block changes, the whole scrollback
		// is re-joined into the viewport.
		m.conv.reviseAssistant(reviseBody(op))
		op++
		m.refreshView()
	}
	mtr := capt.End()

	addScrollbackResult(kpi.ScenarioResult{
		Name:          "tui_scrollback_view",
		Iterations:    b.N,
		AllocsPerOp:   scrollbackPerOp(mtr.Allocs, b.N),
		BytesPerOp:    scrollbackPerOp(mtr.Bytes, b.N),
		GoroutinesEnd: kpi.GoroutineDelta(baselineGoroutines, 20*time.Millisecond),
		RSSPeakBytes:  mtr.RSSPeak,
		RSSFinalBytes: mtr.RSSFinal,
		WallClockNs:   mtr.WallNs,
	})
}

// BenchmarkScrollbackViewSteady measures the OTHER half of the per-frame cost: a
// re-render where the conversation did NOT change (a cursor move, input keystroke,
// scroll, overlay toggle, or the at-least-twice-per-message renderInput chokepoint
// — all of which call refreshView). Before the join cache this still re-joined the
// whole scrollback into a fresh Builder every time (the profile-confirmed
// O(scrollback) hotspot); the join cache reuses the memoized string, so the steady
// frame allocates ~nothing. This is the benchmark that exercises the join-cache fast
// path — the streaming bench above is structurally all-miss (it mutates every op).
func BenchmarkScrollbackViewSteady(b *testing.B) {
	m := buildScrollbackModel(b)
	m.refreshView() // warm the per-block caches AND the join cache

	baselineGoroutines := kpi.GoroutinesAfterSettle(20 * time.Millisecond)
	capt := kpi.NewCapture()
	b.ReportAllocs()
	capt.Begin()
	for b.Loop() {
		// No conversation mutation: the steady, unchanged-frame re-render that the
		// join cache targets. Without the cache this re-joined the full scrollback;
		// with it, the memoized join is returned verbatim.
		m.refreshView()
	}
	mtr := capt.End()

	addScrollbackResult(kpi.ScenarioResult{
		Name:          "tui_scrollback_view_steady",
		Iterations:    b.N,
		AllocsPerOp:   scrollbackPerOp(mtr.Allocs, b.N),
		BytesPerOp:    scrollbackPerOp(mtr.Bytes, b.N),
		GoroutinesEnd: kpi.GoroutineDelta(baselineGoroutines, 20*time.Millisecond),
		RSSPeakBytes:  mtr.RSSPeak,
		RSSFinalBytes: mtr.RSSFinal,
		WallClockNs:   mtr.WallNs,
	})
}

// scrollbackPerOp divides a captured total by the iteration count, guarding n==0.
func scrollbackPerOp(total uint64, n int) uint64 {
	if n <= 0 {
		return 0
	}
	return total / uint64(n)
}
