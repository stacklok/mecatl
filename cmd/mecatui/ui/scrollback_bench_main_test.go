package ui

// TestMain + result accumulator for the scrollback render benchmark. It is the
// ui package's home for the Phase 2 JSON flush (perf-tracking.md): the TUI render
// bench is the SECOND metric family (alongside perf/scenarios), and the
// "Open decisions" lean is "same store, two metric families". So this TestMain
// MERGES its rows into an existing $MECATL_PERF_JSON (written first by the
// perf/scenarios run in the same `task perf:scenarios` invocation) rather than
// clobbering it — both families end up in one file.
//
// It is the only Test in this file and does only a cheap flush, so `task test`
// running the ui package stays cheap and the BenchmarkScrollbackView never runs
// under it (it is a Benchmark; the default -run skips it).

import (
	"encoding/json"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/stacklok/mecatl/perf/kpi"
)

var (
	scrollbackResultsMu sync.Mutex
	scrollbackResults   []kpi.ScenarioResult
	scrollbackSampleSeq = map[string]int{}
)

// addScrollbackResult stamps the schema version + timestamp + git SHA + the
// per-name Sample ordinal and appends a result (so -count=N yields N groupable
// rows, Sample 0,1,2…).
func addScrollbackResult(r kpi.ScenarioResult) {
	r.SchemaVersion = kpi.SchemaVersion
	r.Timestamp = time.Now().UTC().Format(time.RFC3339)
	r.GitSHA = os.Getenv("MECATL_PERF_SHA")
	scrollbackResultsMu.Lock()
	r.Sample = scrollbackSampleSeq[r.Name]
	scrollbackSampleSeq[r.Name]++
	scrollbackResults = append(scrollbackResults, r)
	scrollbackResultsMu.Unlock()
}

// TestMain runs the suite, then merges the accumulated scrollback rows into
// $MECATL_PERF_JSON (preserving any rows already written by perf/scenarios). With
// no env var set it writes nothing.
//
// It also pins MECATUI_NO_EMOJI for the WHOLE ui package so the emoji-presentation
// capability (emoji.go, COLORTERM=truecolor proxy and friends) defaults OFF in the
// test process regardless of the CI host's terminal env — the view_* goldens then
// never capture a host-varying VS16 in the yolo posture badge. Tests that exercise
// the emoji variant override this per-test with t.Setenv (which wins and restores).
func TestMain(m *testing.M) {
	if _, ok := os.LookupEnv("MECATUI_NO_EMOJI"); !ok {
		os.Setenv("MECATUI_NO_EMOJI", "1")
	}
	code := m.Run()
	if path := os.Getenv("MECATL_PERF_JSON"); path != "" {
		scrollbackResultsMu.Lock()
		fresh := append([]kpi.ScenarioResult(nil), scrollbackResults...)
		scrollbackResultsMu.Unlock()
		if len(fresh) > 0 {
			merged := mergeExisting(path, fresh)
			if err := kpi.WriteJSON(path, merged); err != nil {
				os.Stderr.WriteString("scrollback bench: write JSON: " + err.Error() + "\n")
				if code == 0 {
					code = 1
				}
			}
		}
	}
	os.Exit(code)
}

// mergeExisting reads any rows already in path (best-effort; a missing or
// unparsable file is treated as empty) and returns them with the fresh rows
// appended.
//
// ORDERING ASSUMPTION: this read-modify-write merge assumes perf/scenarios runs
// FIRST within one `task perf:scenarios` invocation (it truncates the file; this
// ui bench then appends). The Taskfile owns that ordering by running the two
// `go test` processes sequentially — this test cannot enforce it. A future CI
// author who parallelizes the two `go test` invocations would race here and
// silently drop one family's rows; keep them sequential (or switch to two files).
func mergeExisting(path string, fresh []kpi.ScenarioResult) []kpi.ScenarioResult {
	b, err := os.ReadFile(path)
	if err != nil {
		return fresh
	}
	var prior []kpi.ScenarioResult
	if err := json.Unmarshal(b, &prior); err != nil {
		return fresh
	}
	return append(prior, fresh...)
}
