// Command allocsgate is the allocs/op regression gate for the perf CI workflow
// (docs/perf-tracking.md "Regression gating"; .github/workflows/perf.yml). It compares
// two `go test -bench -benchmem` outputs and FAILS (exit 1) iff some benchmark's
// allocs/op rose beyond an epsilon (delta >= 1 whole alloc AND > 2 %).
//
// WHY A GO PROGRAM, NOT A SHELL/PYTHON HEREDOC: the gate decision is load-bearing,
// so it must be TESTED (main_test.go) and live in ONE place — the two workflow jobs
// (perf-pr, perf-main) both call it, rather than duplicating a parser. It is also
// FAIL-CLOSED: an empty/corrupt bench file does NOT silently pass the hard gate.
//
// allocs/op is the gated signal because it is deterministic — it does not move with
// CPU load, so no statistical significance test is needed (that is benchstat's job,
// and benchstat is the LOCAL human A/B tool, not the CI gate decision).
//
// It imports ONLY the standard library — no engine/..., no internal/..., same leaf
// posture as perf/kpi.
//
// Usage:
//
//	allocsgate <old.txt> <new.txt>
//
// <old.txt> is the previous-main baseline; it may be ABSENT (first run, before any
// baseline has been published) — in that case the gate skips green. <new.txt> is
// this run's bench output and must be non-empty.
package main

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// allocsLine matches one `go test -bench -benchmem` result line and captures the
// decorated benchmark name and its allocs/op figure. The full decorated name
// (including any `/sub` segments and the `-N` GOMAXPROCS suffix) is the key — two
// rows with the same base name but different sub/parallelism are distinct
// benchmarks. We anchor on the literal ` allocs/op` token so a format change
// elsewhere on the line cannot silently mis-capture.
//
// Example line:
//
//	BenchmarkEvaluatorEvaluate/simple-tool-12   	 1000000	  942 ns/op	 856 B/op	 13 allocs/op
var allocsLine = regexp.MustCompile(`^(Benchmark\S+)\s+\d+\s+.*?([0-9]+(?:\.[0-9]+)?)\s+allocs/op`)

// result is the outcome of the gate, separated from os.Exit so it is testable.
type result struct {
	exitCode int
	summary  string // markdown, printed to stdout (the job tees it to the step summary)
}

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: allocsgate <old.txt> <new.txt>")
		os.Exit(2)
	}
	r := gate(os.Args[1], os.Args[2])
	if r.summary != "" {
		fmt.Println(r.summary)
	}
	os.Exit(r.exitCode)
}

// gate runs the comparison. oldPath may be missing (first run); newPath must exist
// and parse to at least one benchmark.
func gate(oldPath, newPath string) result {
	newData, err := os.ReadFile(newPath) //nolint:gosec // CI artifact path, not user input
	if err != nil {
		return result{exitCode: 1, summary: errLine("could not read new bench output %s: %v", newPath, err)}
	}
	newAllocs := parseAllocs(string(newData))
	if len(newAllocs) == 0 {
		// FAIL-CLOSED: an empty/zero-benchmark new file means the bench step
		// produced nothing — never pass a hard gate on no data.
		return result{exitCode: 1, summary: errLine("new bench output %s parsed to ZERO benchmarks (bench step produced no results)", newPath)}
	}

	oldData, err := os.ReadFile(oldPath) //nolint:gosec // CI artifact path, not user input
	if errors.Is(err, os.ErrNotExist) {
		// First run, before any baseline exists: skip green with a notice.
		return result{exitCode: 0, summary: noticeLine("no baseline at %s (first run); allocs gate skipped — it engages once a baseline is published.", oldPath)}
	}
	if err != nil {
		return result{exitCode: 1, summary: errLine("could not read baseline %s: %v", oldPath, err)}
	}
	oldAllocs := parseAllocs(string(oldData))
	if len(oldAllocs) == 0 {
		// FAIL-CLOSED: the baseline file is PRESENT but parses to nothing —
		// a corrupt/truncated gh-pages file. Don't silently pass the gate.
		return result{exitCode: 1, summary: warnLine("baseline %s is present but parsed to ZERO benchmarks (corrupt/truncated); failing the gate rather than passing on no comparison.", oldPath)}
	}

	rows, overlap := compare(oldAllocs, newAllocs)
	table := renderTable(rows)

	if overlap == 0 {
		// No benchmark name appears in both files — everything was renamed. We
		// cannot compare, but a rename is not a regression, so WARN, don't fail.
		return result{exitCode: 0, summary: warnLine("no benchmark name overlaps between baseline and new output (all renamed?); skipping the comparison.") + "\n\n" + table}
	}

	var regressions []row
	for _, rw := range rows {
		if rw.regressed {
			regressions = append(regressions, rw)
		}
	}
	if len(regressions) > 0 {
		var b strings.Builder
		fmt.Fprintf(&b, "::error title=allocs/op regression::%d benchmark(s) regressed beyond epsilon (>=1 alloc AND >2%%)\n", len(regressions))
		for _, rw := range regressions {
			fmt.Fprintf(&b, "  %s: %s -> %s allocs/op (+%.1f%%)\n", rw.name, fmtAlloc(rw.old), fmtAlloc(rw.new), rw.pct)
		}
		b.WriteString("\n")
		b.WriteString(table)
		return result{exitCode: 1, summary: b.String()}
	}

	return result{exitCode: 0, summary: "allocs/op gate: no regression beyond epsilon (>=1 alloc AND >2%).\n\n" + table}
}

// parseAllocs extracts the median allocs/op per decorated benchmark name. Under
// -count=N a benchmark appears N times; the median collapses the samples (allocs
// are deterministic, so the samples are normally identical, but the median is
// robust to a stray noisy line).
func parseAllocs(out string) map[string]float64 {
	samples := make(map[string][]float64)
	for _, line := range strings.Split(out, "\n") {
		m := allocsLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		v, err := strconv.ParseFloat(m[2], 64)
		if err != nil {
			continue
		}
		samples[m[1]] = append(samples[m[1]], v)
	}
	medians := make(map[string]float64, len(samples))
	for name, vs := range samples {
		medians[name] = median(vs)
	}
	return medians
}

func median(vs []float64) float64 {
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

// row is one benchmark's old/new comparison.
type row struct {
	name      string
	old       float64
	new       float64
	pct       float64
	inBoth    bool
	regressed bool
}

// compare builds the per-benchmark rows and returns the count of benchmarks present
// in BOTH inputs (the overlap that the gate decision is made over).
func compare(oldAllocs, newAllocs map[string]float64) (rows []row, overlap int) {
	names := make([]string, 0, len(newAllocs))
	for name := range newAllocs {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		nv := newAllocs[name]
		ov, inBoth := oldAllocs[name]
		rw := row{name: name, old: ov, new: nv, inBoth: inBoth}
		if inBoth {
			overlap++
			delta := nv - ov
			if ov != 0 {
				rw.pct = delta / ov * 100.0
			} else if delta != 0 {
				rw.pct = 100.0 // 0 -> nonzero is an infinite ratio; report 100% and let epsilon decide
			}
			// Epsilon: regress only when BOTH the absolute (>=1 alloc) AND the
			// relative (>2%) thresholds are crossed.
			if delta >= 1 && rw.pct > 2.0 {
				rw.regressed = true
			}
		}
		rows = append(rows, rw)
	}
	return rows, overlap
}

func renderTable(rows []row) string {
	var b strings.Builder
	b.WriteString("| benchmark | old allocs/op | new allocs/op | Δ% | status |\n")
	b.WriteString("|---|---:|---:|---:|---|\n")
	for _, rw := range rows {
		status := "ok"
		oldCol := "—"
		pctCol := "—"
		if rw.inBoth {
			oldCol = fmtAlloc(rw.old)
			pctCol = fmt.Sprintf("%+.1f%%", rw.pct)
			if rw.regressed {
				status = "REGRESSED"
			}
		} else {
			status = "new"
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %s |\n", rw.name, oldCol, fmtAlloc(rw.new), pctCol, status)
	}
	return b.String()
}

func fmtAlloc(v float64) string {
	if v == float64(int64(v)) {
		return strconv.FormatInt(int64(v), 10)
	}
	return strconv.FormatFloat(v, 'f', 1, 64)
}

func errLine(format string, args ...any) string {
	return "::error title=allocs gate::" + fmt.Sprintf(format, args...)
}

func warnLine(format string, args ...any) string {
	return "::warning title=allocs gate::" + fmt.Sprintf(format, args...)
}

func noticeLine(format string, args ...any) string {
	return "::notice title=allocs gate::" + fmt.Sprintf(format, args...)
}
