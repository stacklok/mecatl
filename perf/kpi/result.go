// Package kpi is the stdlib-only KPI-capture support for the offline performance
// scenario harness (perf-tracking.md "The benchmark harness"). It owns the per-scenario metric
// shape (ScenarioResult), the allocation/RSS/wall-clock capture bracket
// (Capture), the linux /proc-based RSS sampler, and a settle-then-count
// goroutine probe.
//
// LAYERING: this package imports ONLY the standard library. It must NOT import
// engine/... or internal/... — the engine-shaped KPIs (tokens, cache-hit-rate)
// are passed IN by the scenario caller (perf/scenarios), which is the only side
// that knows about session.Usage. That keeps kpi a leaf the engine never depends
// on and a scenario can freely consume.
package kpi

import (
	"encoding/json"
	"fmt"
	"os"
)

// SchemaVersion is the on-disk JSON schema version for a ScenarioResult. Bump it
// when the field set changes shape so a trend-store ingester can branch.
const SchemaVersion = 1

// ScenarioResult is one offline scenario's captured KPIs, serialised to the
// trend-store JSON. The deterministic, low-noise fields (allocs/op, bytes/op,
// goroutines, tokens, cache-hit-rate) are the gate-hard signals; the RSS and
// wall-clock fields are advisory (machine-dependent). Token fields are zero for
// scenarios with no model usage (e.g. the TUI scrollback render bench).
//
// Field normalization — the contract a Phase 3 ingester reads (group rows by
// (Name, GitSHA), then aggregate the same-named samples):
//
//   - PER-OP (divided by the benchmark's b.N): AllocsPerOp, BytesPerOp. These are
//     the amortised cost of ONE scenario iteration and are the deterministic,
//     gate-hard signals.
//   - PER-RUN (the LAST iteration's cumulative figure, NOT divided): TokensInput,
//     TokensOutput, TokensCacheRead, CacheHitRate. They are read off the final
//     session's (or team outcome's) accumulated session.Usage — a single
//     iteration's whole-run total, which is constant across iterations because
//     the script is fixed. CacheHitRate == 0 is honest for scenarios with no
//     cache-read in their script (e.g. compaction_cycle) — see perf-tracking.md.
//   - WHOLE-BRACKET (captured ONCE across all b.N iterations, NOT divided):
//     RSSPeakBytes, RSSFinalBytes, WallClockNs. They describe the whole measured
//     region, so they scale with b.N and are advisory (machine-dependent); a
//     trend gate should compare them ratio-wise on the same runner, not absolutely.
//   - DELTA: GoroutinesEnd is end-minus-baseline (see its field comment) — 0 means
//     no goroutine leak, clamped at 0.
//   - DISCRIMINATOR: Sample is the per-(Name) ordinal within ONE process run
//     (0,1,2…), so -count=N emits N groupable rows per scenario rather than N
//     indistinguishable ones.
type ScenarioResult struct {
	SchemaVersion int    `json:"schema_version"`
	Name          string `json:"name"`
	// Sample is the per-scenario-name ordinal (0,1,2…) of this row within one
	// process run. Under `go test -count=N` a scenario emits N rows with identical
	// Name+GitSHA; Sample is the only thing that distinguishes them, letting Phase 3
	// group by (Name, GitSHA) and aggregate the N samples.
	Sample      int    `json:"sample"`
	GitSHA      string `json:"git_sha"`
	Timestamp   string `json:"timestamp"`
	Iterations  int    `json:"iterations"`
	AllocsPerOp uint64 `json:"allocs_per_op"`
	BytesPerOp  uint64 `json:"bytes_per_op"`
	// GoroutinesEnd is a DELTA: live goroutines at end-of-run MINUS a baseline
	// captured before the measured region, clamped at 0. So 0 = the scenario leaked
	// no goroutines; a positive number is the leak count (the team-fanout /
	// background-subagents leak class). It is NOT the raw process-wide
	// runtime.NumGoroutine (which would carry the test runner's own goroutines as
	// noise). See kpi.GoroutineDelta.
	GoroutinesEnd   int     `json:"goroutines_end"`
	TokensInput     int64   `json:"tokens_input"`
	TokensOutput    int64   `json:"tokens_output"`
	TokensCacheRead int64   `json:"tokens_cache_read"`
	CacheHitRate    float64 `json:"cache_hit_rate"`
	RSSPeakBytes    uint64  `json:"rss_peak_bytes"`
	RSSFinalBytes   uint64  `json:"rss_final_bytes"`
	WallClockNs     int64   `json:"wall_clock_ns"`
}

// WriteJSON marshals the accumulated scenario results to path as a pretty JSON
// array. The caller decides the path (typically $MECATL_PERF_JSON); an empty
// slice still writes a valid (empty) array so a downstream ingester never
// chokes on a missing file vs an empty one.
func WriteJSON(path string, results []ScenarioResult) error {
	if results == nil {
		results = []ScenarioResult{}
	}
	b, err := json.MarshalIndent(results, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal scenario results: %w", err)
	}
	b = append(b, '\n')
	if err := os.WriteFile(path, b, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}
