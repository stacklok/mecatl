// Command perfconvert turns the offline scenario harness's per-scenario JSON
// KPIs (the []kpi.ScenarioResult that `task perf:scenarios` writes to
// $MECATL_PERF_JSON) into the THREE github-action-benchmark custom-format files
// the perf workflow feeds to the trend dashboard + alert gate. See
// docs/adr/0019-perf-tracking.md (Phase 3) and .github/workflows/perf.yml.
//
// THREE suites (a "customSmallerIsBetter" gate, a "customBiggerIsBetter" gate, and
// a "customSmallerIsBetter" ADVISORY suite):
//   - SMALLER (gated, fail-on-alert) — per scenario allocs_per_op (EXCEPT the
//     render benches; see RENDER below), plus tokens_total and goroutine_delta for
//     ALL scenarios (the tui render benches' tokens/goroutines are deterministic —
//     always 0 — so they gate safely).
//   - BIGGER (gated, fail-on-alert) — cache_hit_rate for the cacheHitWhitelist.
//   - RENDER (ADVISORY, fail-on-alert:false) — allocs_per_op for ONLY the
//     renderAllocAdvisory scenarios (the tui_scrollback_view* benches). These are
//     split out because their allocs/op is NOT deterministic the way the loop
//     scenarios' is: it carries a b.N residual (the streaming bench re-joins the
//     whole scrollback every op) and process-wide runtime.ReadMemStats background
//     noise (perf/kpi cannot goroutine-scope the MemStats read), so the gh-pages
//     history showed view ~4.6% / steady ~30% swings on commits that touched no TUI
//     code at all. Gating them at the shared 2% threshold false-positives; carrying
//     them as advisory keeps the trend visible without failing a PR. (This is the
//     escalation the SMALLER suite's comment anticipated — now shipped.)
//
// It imports ONLY perf/kpi + the standard library — same leaf posture as the kpi
// package itself; it never reaches into engine/... or internal/....
//
// Aggregation: under `go test -count=N` a scenario emits N rows that share
// (Name, GitSHA) and differ only in Sample. perfconvert groups by Name and takes
// the MEDIAN of each metric across the samples, so a single noisy sample cannot
// move the gated value. The deterministic metrics (allocs/op, tokens) are
// identical across samples anyway; the median is belt-and-braces and matters most
// for the advisory render-allocs metric routed through here.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"

	"github.com/stacklok/mecatl/perf/kpi"
)

// benchPoint is one github-action-benchmark "custom" data point. The custom-format
// tools (customSmallerIsBetter / customBiggerIsBetter) read a JSON array of these.
// See https://github.com/benchmark-action/github-action-benchmark#examples.
type benchPoint struct {
	Name  string  `json:"name"`
	Unit  string  `json:"unit"`
	Value float64 `json:"value"`
}

// cacheHitWhitelist is the EXPLICIT allowlist of scenarios whose cache_hit_rate is
// a gated bigger-is-better signal. It is an allowlist, NOT a `>0` heuristic, on
// purpose: a genuine cache regression that drops a whitelisted scenario's hit rate
// to 0 must still emit a point so the gate fails on it — a `>0` filter would
// silently drop exactly the regression we want to catch.
//
// The by-design-0 scenarios (compaction_cycle exercises compact-and-replace, not
// prefix caching; the tui_* render benches carry no model tokens at all) MUST NOT
// emit a cache-hit point — their honest 0 is not a regression. See
// docs/adr/0019-perf-tracking.md ("The non-obvious KPI: prompt-cache-hit-rate" and
// the JSON-KPI-shape whitelist note).
var cacheHitWhitelist = map[string]bool{
	"single_session_long": true,
	"team_fanout":         true,
}

// renderAllocAdvisory is the EXPLICIT allowlist of scenarios whose allocs_per_op is
// routed to the ADVISORY render suite (fail-on-alert:false) instead of the gated
// SMALLER suite. These are the mecatui scrollback render benches: their allocs/op
// is non-deterministic on a shared CI runner (a b.N residual from the streaming
// re-join + process-wide runtime.ReadMemStats background noise that perf/kpi can't
// goroutine-scope), so the gh-pages history showed view ~4.6% / steady ~30% swings
// on zero-TUI-code commits. Gating them at the shared 2% threshold false-positives;
// the advisory suite keeps the trend visible without failing a PR. Their other
// metrics (tokens_total, goroutine_delta) stay in SMALLER — those ARE deterministic
// (always 0 for a render bench). Mirror this set in the docs/adr/0019 RENDER prose
// + perf.yml when a new render bench is added.
var renderAllocAdvisory = map[string]bool{
	"tui_scrollback_view":        true,
	"tui_scrollback_view_steady": true,
}

func main() {
	in := flag.String("in", "", "path to the scenario KPI JSON ([]kpi.ScenarioResult)")
	smaller := flag.String("smaller", "", "output path for the customSmallerIsBetter (gated) suite")
	bigger := flag.String("bigger", "", "output path for the customBiggerIsBetter (gated) suite")
	render := flag.String("render", "", "output path for the customSmallerIsBetter ADVISORY render-allocs suite")
	flag.Parse()

	if *in == "" || *smaller == "" || *bigger == "" || *render == "" {
		fmt.Fprintln(os.Stderr, "usage: perfconvert -in <scenarios.json> -smaller <out> -bigger <out> -render <out>")
		os.Exit(2)
	}

	if err := run(*in, *smaller, *bigger, *render); err != nil {
		fmt.Fprintln(os.Stderr, "perfconvert:", err)
		os.Exit(1)
	}
}

func run(inPath, smallerPath, biggerPath, renderPath string) error {
	rows, err := readResults(inPath)
	if err != nil {
		return err
	}
	smaller, bigger, render := convert(rows)
	if err := writePoints(smallerPath, smaller); err != nil {
		return err
	}
	if err := writePoints(biggerPath, bigger); err != nil {
		return err
	}
	return writePoints(renderPath, render)
}

// readResults decodes the scenario JSON and HARD-fails on any row whose
// schema_version != kpi.SchemaVersion — a drift between the producer and this
// converter is a bug we want to fail loudly, never silently mis-aggregate.
func readResults(path string) ([]kpi.ScenarioResult, error) {
	data, err := os.ReadFile(path) //nolint:gosec // path is an operator/CI-supplied artifact path, not user input
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var rows []kpi.ScenarioResult
	if err := json.Unmarshal(data, &rows); err != nil {
		return nil, fmt.Errorf("decode %s: %w", path, err)
	}
	for i, r := range rows {
		if r.SchemaVersion != kpi.SchemaVersion {
			return nil, fmt.Errorf(
				"%s row %d (%q): schema_version %d != expected %d (regenerate the scenario JSON or update perfconvert)",
				path, i, r.Name, r.SchemaVersion, kpi.SchemaVersion)
		}
	}
	return rows, nil
}

// convert groups rows by Name and emits the three suites. Each metric is the
// MEDIAN across that scenario's samples. allocs_per_op routes to RENDER (advisory)
// for the renderAllocAdvisory scenarios and to SMALLER (gated) for the rest;
// tokens_total + goroutine_delta always go to SMALLER (deterministic for every
// scenario, including the always-0 render benches); cache_hit_rate goes to BIGGER
// for the cacheHitWhitelist.
func convert(rows []kpi.ScenarioResult) (smaller, bigger, render []benchPoint) {
	groups := groupByName(rows)
	// Deterministic output order so the emitted files are stable across runs
	// (github-action-benchmark and any human diff both benefit).
	names := make([]string, 0, len(groups))
	for name := range groups {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		g := groups[name]

		allocs := medianFloat(mapU64(g, func(r kpi.ScenarioResult) uint64 { return r.AllocsPerOp }))
		allocPoint := benchPoint{Name: name + "/allocs_per_op", Unit: "allocs/op", Value: allocs}
		if renderAllocAdvisory[name] {
			render = append(render, allocPoint)
		} else {
			smaller = append(smaller, allocPoint)
		}

		tokens := medianFloat(mapI64(g, func(r kpi.ScenarioResult) int64 { return r.TokensInput + r.TokensOutput }))
		smaller = append(smaller, benchPoint{Name: name + "/tokens_total", Unit: "tokens", Value: tokens})

		goro := medianFloat(mapI64(g, func(r kpi.ScenarioResult) int64 { return int64(r.GoroutinesEnd) }))
		smaller = append(smaller, benchPoint{Name: name + "/goroutine_delta", Unit: "goroutines", Value: goro})

		if cacheHitWhitelist[name] {
			hit := medianFloat(mapF64(g, func(r kpi.ScenarioResult) float64 { return r.CacheHitRate }))
			bigger = append(bigger, benchPoint{Name: name + "/cache_hit_rate", Unit: "ratio", Value: hit})
		}
	}
	return smaller, bigger, render
}

func groupByName(rows []kpi.ScenarioResult) map[string][]kpi.ScenarioResult {
	groups := make(map[string][]kpi.ScenarioResult)
	for _, r := range rows {
		groups[r.Name] = append(groups[r.Name], r)
	}
	return groups
}

func mapU64(rows []kpi.ScenarioResult, f func(kpi.ScenarioResult) uint64) []float64 {
	out := make([]float64, len(rows))
	for i, r := range rows {
		out[i] = float64(f(r))
	}
	return out
}

func mapI64(rows []kpi.ScenarioResult, f func(kpi.ScenarioResult) int64) []float64 {
	out := make([]float64, len(rows))
	for i, r := range rows {
		out[i] = float64(f(r))
	}
	return out
}

func mapF64(rows []kpi.ScenarioResult, f func(kpi.ScenarioResult) float64) []float64 {
	out := make([]float64, len(rows))
	for i, r := range rows {
		out[i] = f(r)
	}
	return out
}

// medianFloat returns the median of vs (the mean of the two middle elements for an
// even count). An empty slice returns 0 — a group always has at least one sample in
// practice, so this is just defensive.
func medianFloat(vs []float64) float64 {
	n := len(vs)
	if n == 0 {
		return 0
	}
	s := make([]float64, n)
	copy(s, vs)
	sort.Float64s(s)
	mid := n / 2
	if n%2 == 1 {
		return s[mid]
	}
	return (s[mid-1] + s[mid]) / 2
}

func writePoints(path string, points []benchPoint) error {
	if points == nil {
		points = []benchPoint{}
	}
	b, err := json.MarshalIndent(points, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal %s: %w", path, err)
	}
	b = append(b, '\n')
	if err := os.WriteFile(path, b, 0o644); err != nil { //nolint:gosec // CI artifact, not a secret
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}
