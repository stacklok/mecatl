package main

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/stacklok/mecatl/perf/kpi"
)

// pointByName looks a point up in a suite, returning it and whether it was found.
func pointByName(points []benchPoint, name string) (benchPoint, bool) {
	for _, p := range points {
		if p.Name == name {
			return p, true
		}
	}
	return benchPoint{}, false
}

func TestConvert_CacheHitWhitelist(t *testing.T) {
	// ONLY the whitelisted scenarios (single_session_long, team_fanout) emit a
	// bigger-suite cache-hit point. compaction_cycle and the tui_* benches must be
	// ABSENT from bigger even though they carry a CacheHitRate field (their honest 0
	// is by design).
	rows := []kpi.ScenarioResult{
		{SchemaVersion: kpi.SchemaVersion, Name: "single_session_long", Sample: 0, AllocsPerOp: 36000, TokensInput: 100, TokensOutput: 50, CacheHitRate: 0.90},
		{SchemaVersion: kpi.SchemaVersion, Name: "team_fanout", Sample: 0, AllocsPerOp: 2600, TokensInput: 40, TokensOutput: 10, CacheHitRate: 0.75},
		{SchemaVersion: kpi.SchemaVersion, Name: "compaction_cycle", Sample: 0, AllocsPerOp: 4200, TokensInput: 30, TokensOutput: 5, CacheHitRate: 0},
		{SchemaVersion: kpi.SchemaVersion, Name: "tui_scrollback_view", Sample: 0, AllocsPerOp: 6200, CacheHitRate: 0},
		{SchemaVersion: kpi.SchemaVersion, Name: "tui_scrollback_view_steady", Sample: 0, AllocsPerOp: 51, CacheHitRate: 0},
	}

	smaller, bigger, _ := convert(rows)

	// bigger: exactly the two whitelisted scenarios, nothing else.
	if _, ok := pointByName(bigger, "single_session_long/cache_hit_rate"); !ok {
		t.Error("single_session_long/cache_hit_rate missing from bigger suite")
	}
	if _, ok := pointByName(bigger, "team_fanout/cache_hit_rate"); !ok {
		t.Error("team_fanout/cache_hit_rate missing from bigger suite")
	}
	if _, ok := pointByName(bigger, "compaction_cycle/cache_hit_rate"); ok {
		t.Error("compaction_cycle/cache_hit_rate must NOT be in the bigger suite (by-design 0)")
	}
	if _, ok := pointByName(bigger, "tui_scrollback_view/cache_hit_rate"); ok {
		t.Error("tui_scrollback_view/cache_hit_rate must NOT be in the bigger suite (token-less)")
	}
	if len(bigger) != 2 {
		t.Errorf("bigger suite has %d points, want 2 (the whitelist)", len(bigger))
	}

	// A whitelisted scenario whose cache hit rate regressed to 0 STILL emits a
	// point (the allowlist, not a >0 heuristic, guarantees the gate can fail).
	rowsRegressed := []kpi.ScenarioResult{
		{SchemaVersion: kpi.SchemaVersion, Name: "single_session_long", Sample: 0, CacheHitRate: 0},
	}
	_, biggerReg, _ := convert(rowsRegressed)
	p, ok := pointByName(biggerReg, "single_session_long/cache_hit_rate")
	if !ok {
		t.Fatal("a whitelisted scenario must emit a cache-hit point even at 0")
	}
	if p.Value != 0 {
		t.Errorf("regressed cache hit value = %v, want 0", p.Value)
	}

	// smaller: the NON-render scenarios emit allocs/op; EVERY scenario (incl. the
	// render benches) emits tokens_total + goroutine_delta there.
	for _, name := range []string{"single_session_long", "team_fanout", "compaction_cycle"} {
		for _, suffix := range []string{"/allocs_per_op", "/tokens_total", "/goroutine_delta"} {
			if _, ok := pointByName(smaller, name+suffix); !ok {
				t.Errorf("smaller suite missing %s%s", name, suffix)
			}
		}
	}
	for _, name := range []string{"tui_scrollback_view", "tui_scrollback_view_steady"} {
		for _, suffix := range []string{"/tokens_total", "/goroutine_delta"} {
			if _, ok := pointByName(smaller, name+suffix); !ok {
				t.Errorf("smaller suite missing %s%s (render benches' deterministic metrics stay gated)", name, suffix)
			}
		}
	}
	// tokens_total is the sum of input+output.
	if tp, _ := pointByName(smaller, "single_session_long/tokens_total"); tp.Value != 150 {
		t.Errorf("single_session_long/tokens_total = %v, want 150 (100+50)", tp.Value)
	}
}

func TestConvert_RenderAllocsAdvisorySplit(t *testing.T) {
	// The tui render benches' allocs_per_op routes to the ADVISORY render suite, NOT
	// the gated smaller suite; their deterministic tokens/goroutine metrics stay in
	// smaller. The non-render scenarios' allocs_per_op stays in smaller.
	rows := []kpi.ScenarioResult{
		{SchemaVersion: kpi.SchemaVersion, Name: "single_session_long", Sample: 0, AllocsPerOp: 36000, TokensInput: 100, TokensOutput: 50, CacheHitRate: 0.90},
		{SchemaVersion: kpi.SchemaVersion, Name: "team_fanout", Sample: 0, AllocsPerOp: 2600},
		{SchemaVersion: kpi.SchemaVersion, Name: "compaction_cycle", Sample: 0, AllocsPerOp: 4200},
		{SchemaVersion: kpi.SchemaVersion, Name: "tui_scrollback_view", Sample: 0, AllocsPerOp: 6200},
		{SchemaVersion: kpi.SchemaVersion, Name: "tui_scrollback_view_steady", Sample: 0, AllocsPerOp: 51},
	}

	smaller, _, render := convert(rows)

	// render: EXACTLY the two render-alloc points and nothing else.
	if _, ok := pointByName(render, "tui_scrollback_view/allocs_per_op"); !ok {
		t.Error("tui_scrollback_view/allocs_per_op missing from render suite")
	}
	if _, ok := pointByName(render, "tui_scrollback_view_steady/allocs_per_op"); !ok {
		t.Error("tui_scrollback_view_steady/allocs_per_op missing from render suite")
	}
	if len(render) != 2 {
		t.Errorf("render suite has %d points, want exactly 2 (the two render-alloc points)", len(render))
	}

	// The render benches' allocs_per_op must NOT be in the gated smaller suite.
	if _, ok := pointByName(smaller, "tui_scrollback_view/allocs_per_op"); ok {
		t.Error("tui_scrollback_view/allocs_per_op must NOT be in the gated smaller suite (it is advisory)")
	}
	if _, ok := pointByName(smaller, "tui_scrollback_view_steady/allocs_per_op"); ok {
		t.Error("tui_scrollback_view_steady/allocs_per_op must NOT be in the gated smaller suite (it is advisory)")
	}

	// The render benches' DETERMINISTIC metrics stay in the gated smaller suite.
	for _, name := range []string{"tui_scrollback_view", "tui_scrollback_view_steady"} {
		for _, suffix := range []string{"/tokens_total", "/goroutine_delta"} {
			if _, ok := pointByName(smaller, name+suffix); !ok {
				t.Errorf("smaller suite missing %s%s", name, suffix)
			}
		}
		// And their allocs must NOT leak into render's siblings.
		if _, ok := pointByName(render, name+"/tokens_total"); ok {
			t.Errorf("render suite must not carry %s/tokens_total", name)
		}
	}

	// The NON-render scenarios' allocs_per_op stays in the gated smaller suite and
	// out of render.
	for _, name := range []string{"single_session_long", "team_fanout", "compaction_cycle"} {
		if _, ok := pointByName(smaller, name+"/allocs_per_op"); !ok {
			t.Errorf("smaller suite missing %s/allocs_per_op", name)
		}
		if _, ok := pointByName(render, name+"/allocs_per_op"); ok {
			t.Errorf("render suite must NOT carry %s/allocs_per_op (it is deterministic, gated)", name)
		}
	}
}

func TestConvert_MedianAcrossSamples(t *testing.T) {
	// Three samples of one scenario with differing allocs; the gated value is the
	// MEDIAN, so an outlier sample cannot move it.
	rows := []kpi.ScenarioResult{
		{SchemaVersion: kpi.SchemaVersion, Name: "team_fanout", Sample: 0, AllocsPerOp: 2600},
		{SchemaVersion: kpi.SchemaVersion, Name: "team_fanout", Sample: 1, AllocsPerOp: 2700},
		{SchemaVersion: kpi.SchemaVersion, Name: "team_fanout", Sample: 2, AllocsPerOp: 9999}, // outlier
	}
	smaller, _, _ := convert(rows)
	p, ok := pointByName(smaller, "team_fanout/allocs_per_op")
	if !ok {
		t.Fatal("team_fanout/allocs_per_op missing")
	}
	if p.Value != 2700 {
		t.Errorf("median allocs = %v, want 2700 (median of 2600,2700,9999)", p.Value)
	}

	// Even count → mean of the two middle values.
	rowsEven := []kpi.ScenarioResult{
		{SchemaVersion: kpi.SchemaVersion, Name: "team_fanout", Sample: 0, AllocsPerOp: 100},
		{SchemaVersion: kpi.SchemaVersion, Name: "team_fanout", Sample: 1, AllocsPerOp: 200},
		{SchemaVersion: kpi.SchemaVersion, Name: "team_fanout", Sample: 2, AllocsPerOp: 300},
		{SchemaVersion: kpi.SchemaVersion, Name: "team_fanout", Sample: 3, AllocsPerOp: 400},
	}
	smallerEven, _, _ := convert(rowsEven)
	pe, _ := pointByName(smallerEven, "team_fanout/allocs_per_op")
	if pe.Value != 250 {
		t.Errorf("even median = %v, want 250 (mean of 200,300)", pe.Value)
	}
}

func TestMedianFloat(t *testing.T) {
	cases := []struct {
		in   []float64
		want float64
	}{
		{nil, 0},
		{[]float64{5}, 5},
		{[]float64{3, 1, 2}, 2},
		{[]float64{4, 2}, 3},
		{[]float64{9999, 2600, 2700}, 2700},
	}
	for _, c := range cases {
		if got := medianFloat(c.in); got != c.want {
			t.Errorf("medianFloat(%v) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestReadResults_SchemaMismatch(t *testing.T) {
	// A row with the wrong schema version is a HARD error (drift between producer
	// and converter); readResults must reject the whole file rather than
	// mis-aggregate.
	dir := t.TempDir()
	path := dir + "/bad.json"
	if err := kpi.WriteJSON(path, []kpi.ScenarioResult{
		{SchemaVersion: kpi.SchemaVersion + 1, Name: "single_session_long"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := readResults(path); err == nil {
		t.Fatal("readResults accepted a schema-version mismatch; want a hard error")
	}
}

// TestRun_EmptyRenderRoundTrip exercises the full run() plumbing (the 4-arg
// signature + writePoints(renderPath, …)) for the no-render-advisory-scenarios case:
// an input with ZERO render benches must still WRITE the -render file as a valid
// EMPTY JSON array `[]` — the contract github-action-benchmark consumes on a
// no-points / first run (writePoints normalises a nil slice to `[]`, never `null`).
// It locks the empty-suite contract that only convert() unit tests would miss.
func TestRun_EmptyRenderRoundTrip(t *testing.T) {
	dir := t.TempDir()
	inPath := dir + "/in.json"
	smallerPath := dir + "/smaller.json"
	biggerPath := dir + "/bigger.json"
	renderPath := dir + "/render.json"

	// No tui_scrollback_view* rows ⇒ the render suite is empty.
	in := []kpi.ScenarioResult{
		{SchemaVersion: kpi.SchemaVersion, Name: "single_session_long", AllocsPerOp: 36000, TokensInput: 100, TokensOutput: 50, CacheHitRate: 0.90},
		{SchemaVersion: kpi.SchemaVersion, Name: "team_fanout", AllocsPerOp: 2600},
	}
	if err := kpi.WriteJSON(inPath, in); err != nil {
		t.Fatal(err)
	}

	if err := run(inPath, smallerPath, biggerPath, renderPath); err != nil {
		t.Fatalf("run: %v", err)
	}

	// The render file MUST exist and decode to an EMPTY (non-nil) JSON array.
	data, err := os.ReadFile(renderPath)
	if err != nil {
		t.Fatalf("render output file not written: %v", err)
	}
	var points []benchPoint
	if err := json.Unmarshal(data, &points); err != nil {
		t.Fatalf("render output is not valid JSON: %v (content: %q)", err, string(data))
	}
	if len(points) != 0 {
		t.Errorf("render suite has %d points, want 0 (no render-advisory scenarios)", len(points))
	}
	// github-action-benchmark needs `[]`, never `null`: writePoints normalises a nil
	// slice, so the marshalled form must start with '['.
	if len(data) == 0 || data[0] != '[' {
		t.Errorf("render output must be a JSON array literal (`[]`), got %q", string(data))
	}

	// Sanity: the gated suites were still written and non-empty (the gated allocs
	// rode the smaller suite as usual).
	if sd, err := os.ReadFile(smallerPath); err != nil || len(sd) == 0 {
		t.Errorf("smaller output missing/empty: err=%v", err)
	}
	if _, err := os.ReadFile(biggerPath); err != nil {
		t.Errorf("bigger output missing: %v", err)
	}
}

func TestReadResults_OK(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/good.json"
	want := []kpi.ScenarioResult{
		{SchemaVersion: kpi.SchemaVersion, Name: "single_session_long", AllocsPerOp: 100},
	}
	if err := kpi.WriteJSON(path, want); err != nil {
		t.Fatal(err)
	}
	got, err := readResults(path)
	if err != nil {
		t.Fatalf("readResults: %v", err)
	}
	if len(got) != 1 || got[0].Name != "single_session_long" {
		t.Errorf("readResults round-trip mismatch: %+v", got)
	}
}
