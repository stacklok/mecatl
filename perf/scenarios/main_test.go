// Package scenarios_test holds the offline, deterministic performance SCENARIO
// benchmarks (perf-tracking.md "The benchmark harness"): whole-loop runs through the real engine
// over mockllm + memfs/memstore that catch the failure modes the hot-path
// microbenchmarks miss — allocation budget over a long session,
// goroutine hygiene under delegation, token/cache regressions, compaction churn.
//
// It is an EXTERNAL test package (scenarios_test): it imports engine/... + the
// engine/adapter/* reference adapters + perf/kpi, and NEVER internal/... . The
// benchmarks are the home; there is no cmd/ binary. Each benchmark records a
// kpi.ScenarioResult into the package-level accumulator (addResult), and TestMain
// flushes the accumulator to $MECATL_PERF_JSON after the suite runs.
//
// Run them with `task perf:scenarios` (NOT part of `task test`): they are
// Benchmarks, so a plain `go test` with the default -run skips them, and the one
// TestMain here does only a cheap flush.
package scenarios_test

import (
	"os"
	"sync"
	"testing"
	"time"

	"github.com/stacklok/mecatl/perf/kpi"
)

// results accumulates one ScenarioResult per benchmark. Benchmarks run
// sequentially within a `go test` process, but addResult is mutexed for
// defensiveness. sampleSeq tracks the per-(scenario-name) ordinal so -count=N
// emits N groupable rows per scenario (Sample 0,1,2…) rather than N
// indistinguishable ones.
var (
	resultsMu sync.Mutex
	results   []kpi.ScenarioResult
	sampleSeq = map[string]int{}
)

// addResult stamps the schema version + timestamp + git SHA (from the
// environment; empty in-process) + the per-name Sample ordinal onto a result and
// appends it. The caller fills the measured fields.
func addResult(r kpi.ScenarioResult) {
	r.SchemaVersion = kpi.SchemaVersion
	r.Timestamp = time.Now().UTC().Format(time.RFC3339)
	r.GitSHA = os.Getenv("MECATL_PERF_SHA")
	resultsMu.Lock()
	r.Sample = sampleSeq[r.Name]
	sampleSeq[r.Name]++
	results = append(results, r)
	resultsMu.Unlock()
}

// TestMain runs the suite, then — if MECATL_PERF_JSON is set — flushes the
// accumulated scenario results to that path. With no env var set it writes
// nothing (the default: benchstat lines on stdout only). It is the only Test in
// the package and does no heavy work, so `task test` running this package is
// cheap and the scenario Benchmarks never run under it.
func TestMain(m *testing.M) {
	code := m.Run()
	if path := os.Getenv("MECATL_PERF_JSON"); path != "" {
		resultsMu.Lock()
		snapshot := append([]kpi.ScenarioResult(nil), results...)
		resultsMu.Unlock()
		if err := kpi.WriteJSON(path, snapshot); err != nil {
			// A flush failure is a harness error, not a benchmark failure; surface
			// it on stderr and fail the process so CI notices.
			os.Stderr.WriteString("perf scenarios: write JSON: " + err.Error() + "\n")
			if code == 0 {
				code = 1
			}
		}
	}
	os.Exit(code)
}
