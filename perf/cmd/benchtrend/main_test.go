package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestConvertBench_MediansPreserveCommitGranularity(t *testing.T) {
	in := strings.NewReader(strings.Join([]string{
		"pkg: github.com/stacklok/mecatl/engine/agent",
		"BenchmarkRunReadOnlyTurn-4 100 30 ns/op 300 B/op 3 allocs/op",
		"BenchmarkRunReadOnlyTurn-4 100 10 ns/op 100 B/op 1 allocs/op",
		"BenchmarkRunReadOnlyTurn-4 100 20 ns/op 200 B/op 2 allocs/op",
		"pkg: github.com/stacklok/mecatl/engine/prompt",
		"BenchmarkBuild-4 100 8 ns/op 80 B/op 5 allocs/op",
		"BenchmarkBuild-4 100 12 ns/op 120 B/op 7 allocs/op",
	}, "\n"))

	points, err := convertBench(in)
	if err != nil {
		t.Fatal(err)
	}
	if len(points) != 6 {
		t.Fatalf("points = %d, want 6: %#v", len(points), points)
	}
	want := []benchPoint{
		{Name: "BenchmarkRunReadOnlyTurn (github.com/stacklok/mecatl/engine/agent) - ns/op", Unit: "ns/op", Value: 20},
		{Name: "BenchmarkRunReadOnlyTurn (github.com/stacklok/mecatl/engine/agent) - B/op", Unit: "B/op", Value: 200},
		{Name: "BenchmarkRunReadOnlyTurn (github.com/stacklok/mecatl/engine/agent) - allocs/op", Unit: "allocs/op", Value: 2},
		{Name: "BenchmarkBuild (github.com/stacklok/mecatl/engine/prompt) - ns/op", Unit: "ns/op", Value: 10},
		{Name: "BenchmarkBuild (github.com/stacklok/mecatl/engine/prompt) - B/op", Unit: "B/op", Value: 100},
		{Name: "BenchmarkBuild (github.com/stacklok/mecatl/engine/prompt) - allocs/op", Unit: "allocs/op", Value: 6},
	}
	if !reflect.DeepEqual(points, want) {
		t.Errorf("points = %#v, want %#v", points, want)
	}
}

func TestConvertBench_FailsClosed(t *testing.T) {
	cases := map[string]string{
		"no benchmarks":    "pkg: example.test\nPASS\n",
		"no package":       "BenchmarkBuild-4 100 10 ns/op 20 B/op 1 allocs/op\n",
		"missing benchmem": "pkg: example.test\nBenchmarkBuild-4 100 10 ns/op\n",
		"malformed metric": "pkg: example.test\nBenchmarkBuild-4 100 nope ns/op 20 B/op 1 allocs/op\n",
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := convertBench(strings.NewReader(input)); err == nil {
				t.Fatal("convertBench accepted invalid input")
			}
		})
	}
}

func TestCompactHistory_AggregatesEveryCommitAndPreservesOtherSuites(t *testing.T) {
	input := dataJSPrefix + `{
  "lastUpdate": 123,
  "repoUrl": "https://example.test/repo",
  "entries": {
    "mecatl go microbenchmarks": [
      {
        "commit": {"id": "abc"},
        "date": 123,
        "tool": "go",
        "unknown": "preserved",
        "benches": [
          {"name":"BenchmarkBuild (example/pkg)","value":30,"unit":"ns/op 300 B/op 3 allocs/op","extra":"100 times"},
          {"name":"BenchmarkBuild (example/pkg) - ns/op","value":30,"unit":"ns/op","extra":"100 times"},
          {"name":"BenchmarkBuild (example/pkg) - B/op","value":300,"unit":"B/op","extra":"100 times"},
          {"name":"BenchmarkBuild (example/pkg) - allocs/op","value":3,"unit":"allocs/op","extra":"100 times"},
          {"name":"BenchmarkBuild (example/pkg)","value":10,"unit":"ns/op 100 B/op 1 allocs/op","extra":"100 times"},
          {"name":"BenchmarkBuild (example/pkg) - ns/op","value":10,"unit":"ns/op","extra":"100 times"},
          {"name":"BenchmarkBuild (example/pkg) - B/op","value":100,"unit":"B/op","extra":"100 times"},
          {"name":"BenchmarkBuild (example/pkg) - allocs/op","value":1,"unit":"allocs/op","extra":"100 times"},
          {"name":"BenchmarkBuild (example/pkg)","value":20,"unit":"ns/op 200 B/op 2 allocs/op","extra":"100 times"},
          {"name":"BenchmarkBuild (example/pkg) - ns/op","value":20,"unit":"ns/op","extra":"100 times"},
          {"name":"BenchmarkBuild (example/pkg) - B/op","value":200,"unit":"B/op","extra":"100 times"},
          {"name":"BenchmarkBuild (example/pkg) - allocs/op","value":2,"unit":"allocs/op","extra":"100 times"}
        ]
      }
	  ,{
		"commit": {"id": "def"},
		"date": 456,
		"tool": "customSmallerIsBetter",
		"benches": [
		  {"name":"BenchmarkBuild (example/pkg) - ns/op","value":40,"unit":"ns/op","extra":"kept"},
		  {"name":"BenchmarkBuild (example/pkg) - ns/op","value":60,"unit":"ns/op","extra":"duplicate"},
		  {"name":"BenchmarkBuild (example/pkg) - B/op","value":400,"unit":"B/op"},
		  {"name":"BenchmarkBuild (example/pkg) - B/op","value":600,"unit":"B/op"},
		  {"name":"BenchmarkBuild (example/pkg) - allocs/op","value":4,"unit":"allocs/op"},
		  {"name":"BenchmarkBuild (example/pkg) - allocs/op","value":6,"unit":"allocs/op"}
		]
	  },{
		"commit": {"id": "abc"},
		"date": 789,
		"tool": "customSmallerIsBetter",
		"duplicate-row-field": "discarded with the duplicate row",
		"benches": [
		  {"name":"BenchmarkBuild (example/pkg) - ns/op","value":50,"unit":"ns/op"},
		  {"name":"BenchmarkBuild (example/pkg) - ns/op","value":70,"unit":"ns/op"},
		  {"name":"BenchmarkBuild (example/pkg) - B/op","value":500,"unit":"B/op"},
		  {"name":"BenchmarkBuild (example/pkg) - B/op","value":700,"unit":"B/op"},
		  {"name":"BenchmarkBuild (example/pkg) - allocs/op","value":5,"unit":"allocs/op"},
		  {"name":"BenchmarkBuild (example/pkg) - allocs/op","value":7,"unit":"allocs/op"}
		]
	  }
    ],
	"scenario suite": [{"commit":{"id":"scenario"},"metadata":"untouched","benches":[{"name":"scenario","value":9,"unit":"allocs/op","extra":"preserved"}]}]
  }
}`

	out, stats, err := compactHistory(strings.NewReader(input), "mecatl go microbenchmarks", "")
	if err != nil {
		t.Fatal(err)
	}
	if stats != (compactStats{rows: 3, before: 24, after: 6}) {
		t.Fatalf("stats = %#v, want rows=3 before=24 after=6", stats)
	}
	root := decodeHistoryForTest(t, out)
	entries := root["entries"].(map[string]any)
	row := entries["mecatl go microbenchmarks"].([]any)[0].(map[string]any)
	if row["unknown"] != "preserved" {
		t.Errorf("unknown row field lost: %#v", row)
	}
	benches := row["benches"].([]any)
	if len(benches) != 3 {
		t.Fatalf("compacted benches = %d, want 3", len(benches))
	}
	values := make(map[string]float64)
	for _, raw := range benches {
		point := raw.(map[string]any)
		values[point["unit"].(string)] = point["value"].(float64)
		if point["extra"] != "100 times" {
			t.Errorf("unknown point field lost: %#v", point)
		}
	}
	for unit, want := range map[string]float64{"ns/op": 30, "B/op": 300, "allocs/op": 3} {
		if values[unit] != want {
			t.Errorf("%s median = %v, want %v", unit, values[unit], want)
		}
	}
	secondRow := entries["mecatl go microbenchmarks"].([]any)[1].(map[string]any)
	secondValues := make(map[string]float64)
	for _, raw := range secondRow["benches"].([]any) {
		point := raw.(map[string]any)
		secondValues[point["unit"].(string)] = point["value"].(float64)
	}
	if !reflect.DeepEqual(secondValues, map[string]float64{"ns/op": 50, "B/op": 500, "allocs/op": 5}) {
		t.Errorf("second commit medians = %#v", secondValues)
	}
	if secondRow["benches"].([]any)[0].(map[string]any)["extra"] != "kept" {
		t.Errorf("second commit point metadata lost: %#v", secondRow)
	}
	wantOther := []any{map[string]any{
		"commit":   map[string]any{"id": "scenario"},
		"metadata": "untouched",
		"benches":  []any{map[string]any{"name": "scenario", "value": float64(9), "unit": "allocs/op", "extra": "preserved"}},
	}}
	if !reflect.DeepEqual(entries["scenario suite"], wantOther) {
		t.Errorf("unrelated suite was changed: %#v", entries["scenario suite"])
	}

	second, secondStats, err := compactHistory(bytes.NewReader(out), "mecatl go microbenchmarks", "")
	if err != nil {
		t.Fatal(err)
	}
	if secondStats != (compactStats{rows: 2, before: 6, after: 6}) {
		t.Fatalf("second stats = %#v, want rows=2 before=6 after=6", secondStats)
	}
	if !bytes.Equal(out, second) {
		t.Error("compaction is not byte-idempotent")
	}

	replaced, replaceStats, err := compactHistory(bytes.NewReader(out), "mecatl go microbenchmarks", "abc")
	if err != nil {
		t.Fatal(err)
	}
	if replaceStats != (compactStats{rows: 2, before: 6, after: 3}) {
		t.Fatalf("replace stats = %#v, want rows=2 before=6 after=3", replaceStats)
	}
	replacedRoot := decodeHistoryForTest(t, replaced)
	replacedRows := replacedRoot["entries"].(map[string]any)["mecatl go microbenchmarks"].([]any)
	if len(replacedRows) != 1 || replacedRows[0].(map[string]any)["commit"].(map[string]any)["id"] != "def" {
		t.Fatalf("replace rows = %#v, want only commit def", replacedRows)
	}
}

func TestCompactHistory_FailsClosed(t *testing.T) {
	cases := map[string]string{
		"missing prefix": `{}`,
		"invalid json":   dataJSPrefix + `{`,
		"bad entries":    dataJSPrefix + `{"entries":[]}`,
		"missing suite":  dataJSPrefix + `{"entries":{"other":[]}}`,
		"bad point":      dataJSPrefix + `{"entries":{"suite":[{"commit":{"id":"abc"},"benches":[{"name":"x","unit":"u","value":"bad"}]}]}}`,
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			if _, _, err := compactHistory(strings.NewReader(input), "suite", ""); err == nil {
				t.Fatal("compactHistory accepted invalid input")
			}
		})
	}
}

func TestCompactHistory_AllowsEmptyBootstrapHistory(t *testing.T) {
	input := dataJSPrefix + `{"entries":{}}`
	out, stats, err := compactHistory(strings.NewReader(input), "suite", "")
	if err != nil {
		t.Fatal(err)
	}
	if stats != (compactStats{}) {
		t.Fatalf("stats = %#v, want zero", stats)
	}
	root := decodeHistoryForTest(t, out)
	if entries := root["entries"].(map[string]any); len(entries) != 0 {
		t.Fatalf("bootstrap entries = %#v, want empty", entries)
	}
}

func TestRunCompact_RewritesInputInPlace(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "data.js")
	input := dataJSPrefix + `{"entries":{"suite":[{"commit":{"id":"abc"},"benches":[
		{"name":"BenchmarkX - ns/op","unit":"ns/op","value":10},
		{"name":"BenchmarkX - ns/op","unit":"ns/op","value":20}
	]}]}}`
	if err := os.WriteFile(path, []byte(input), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runCompact([]string{"-in", path, "-out", path, "-suite", "suite"}); err != nil {
		t.Fatal(err)
	}
	out, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	root := decodeHistoryForTest(t, out)
	benches := root["entries"].(map[string]any)["suite"].([]any)[0].(map[string]any)["benches"].([]any)
	if len(benches) != 1 || benches[0].(map[string]any)["value"] != float64(15) {
		t.Fatalf("in-place compact benches = %#v, want median 15", benches)
	}
}

func decodeHistoryForTest(t *testing.T, b []byte) map[string]any {
	t.Helper()
	text := strings.TrimSpace(strings.TrimPrefix(string(b), dataJSPrefix))
	var root map[string]any
	if err := json.Unmarshal([]byte(text), &root); err != nil {
		t.Fatal(err)
	}
	return root
}
