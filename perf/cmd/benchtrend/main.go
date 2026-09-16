// Command benchtrend keeps the github-action-benchmark microbenchmark history
// compact without sacrificing commit granularity.
//
// The perf gate deliberately runs `go test -count=10`. Feeding that raw output
// to github-action-benchmark stores all ten samples for every metric, even though
// the dashboard needs one representative point per commit. The convert command
// emits the median ns/op, B/op, and allocs/op values in the action's custom JSON
// format. The compact command applies the same median rule to the existing
// benchmark-action data.js history and removes the action's redundant composite
// Go points.
//
// Both modes are standard-library-only and fail closed on malformed input.
package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const dataJSPrefix = "window.BENCHMARK_DATA = "

var benchmarkName = regexp.MustCompile(`^(Benchmark\S+?)(?:-(\d+))?$`)

var trackedUnits = []string{"ns/op", "B/op", "allocs/op"}

type benchPoint struct {
	Name  string  `json:"name"`
	Unit  string  `json:"unit"`
	Value float64 `json:"value"`
}

type sample struct {
	pkg     string
	name    string
	metrics map[string]float64
}

type sampleGroup struct {
	pkg     string
	name    string
	metrics map[string][]float64
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	var err error
	switch os.Args[1] {
	case "convert":
		err = runConvert(os.Args[2:])
	case "compact":
		err = runCompact(os.Args[2:])
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "benchtrend:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: benchtrend convert -in <bench.txt> -out <trend.json>")
	fmt.Fprintln(os.Stderr, "       benchtrend compact -in <data.js> -out <data.js> -suite <name> [-replace-commit <sha>]")
}

func runConvert(args []string) error {
	fs := flag.NewFlagSet("convert", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	inPath := fs.String("in", "", "go benchmark input path, or - for stdin")
	outPath := fs.String("out", "", "custom benchmark JSON output path, or - for stdout")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *inPath == "" || *outPath == "" || fs.NArg() != 0 {
		return errors.New("convert requires -in and -out")
	}

	in, closeIn, err := openInput(*inPath)
	if err != nil {
		return err
	}
	defer closeIn()
	points, err := convertBench(in)
	if err != nil {
		return err
	}
	b, err := json.MarshalIndent(points, "", "  ")
	if err != nil {
		return fmt.Errorf("encode trend points: %w", err)
	}
	b = append(b, '\n')
	return writeOutput(*outPath, b)
}

func runCompact(args []string) error {
	fs := flag.NewFlagSet("compact", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	inPath := fs.String("in", "", "benchmark-action data.js input path, or - for stdin")
	outPath := fs.String("out", "", "compacted data.js output path, or - for stdout")
	suite := fs.String("suite", "", "benchmark suite to compact")
	replaceCommit := fs.String("replace-commit", "", "remove this commit so the publisher can replace it")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *inPath == "" || *outPath == "" || *suite == "" || fs.NArg() != 0 {
		return errors.New("compact requires -in, -out, and -suite")
	}

	in, closeIn, err := openInput(*inPath)
	if err != nil {
		return err
	}
	defer closeIn()
	b, stats, err := compactHistory(in, *suite, *replaceCommit)
	if err != nil {
		return err
	}
	if err := writeOutput(*outPath, b); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "compacted %q: %d rows, %d -> %d benchmark points\n", *suite, stats.rows, stats.before, stats.after)
	return nil
}

func openInput(path string) (io.Reader, func(), error) {
	if path == "-" {
		return os.Stdin, func() {}, nil
	}
	f, err := os.Open(path) //nolint:gosec // operator/CI-supplied artifact path
	if err != nil {
		return nil, func() {}, fmt.Errorf("open %s: %w", path, err)
	}
	return f, func() { _ = f.Close() }, nil
}

func writeOutput(path string, b []byte) error {
	if path == "-" {
		if _, err := os.Stdout.Write(b); err != nil {
			return fmt.Errorf("write stdout: %w", err)
		}
		return nil
	}
	if err := os.WriteFile(path, b, 0o644); err != nil { //nolint:gosec // CI artifact, not a secret
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

func convertBench(r io.Reader) ([]benchPoint, error) {
	samples, err := parseSamples(r)
	if err != nil {
		return nil, err
	}
	if len(samples) == 0 {
		return nil, errors.New("input parsed to zero benchmarks")
	}

	packages := make(map[string]bool)
	groups := make(map[string]*sampleGroup)
	for _, s := range samples {
		packages[s.pkg] = true
		key := s.pkg + "\x00" + s.name
		g := groups[key]
		if g == nil {
			g = &sampleGroup{pkg: s.pkg, name: s.name, metrics: make(map[string][]float64)}
			groups[key] = g
		}
		for _, unit := range trackedUnits {
			v, ok := s.metrics[unit]
			if !ok {
				return nil, fmt.Errorf("%s in %s is missing required %s metric", s.name, s.pkg, unit)
			}
			g.metrics[unit] = append(g.metrics[unit], v)
		}
	}

	keys := make([]string, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	points := make([]benchPoint, 0, len(groups)*len(trackedUnits))
	for _, key := range keys {
		g := groups[key]
		name := g.name
		if len(packages) > 1 && !containsPackageRef(name, g.pkg) {
			name += " (" + g.pkg + ")"
		}
		for _, unit := range trackedUnits {
			points = append(points, benchPoint{
				Name:  name + " - " + unit,
				Unit:  unit,
				Value: median(g.metrics[unit]),
			})
		}
	}
	return points, nil
}

func parseSamples(r io.Reader) ([]sample, error) {
	scanner := bufio.NewScanner(r)
	// A benchmark line is small, but permit long package/test diagnostics without
	// silently failing at bufio.Scanner's default 64 KiB token limit.
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	var pkg string
	var samples []sample
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "pkg:") {
			pkg = strings.TrimSpace(strings.TrimPrefix(line, "pkg:"))
			continue
		}
		if !strings.HasPrefix(line, "Benchmark") {
			continue
		}
		if pkg == "" {
			return nil, fmt.Errorf("benchmark line has no preceding pkg section: %q", line)
		}
		fields := strings.Fields(line)
		if len(fields) < 4 || len(fields)%2 != 0 {
			return nil, fmt.Errorf("malformed benchmark line: %q", line)
		}
		m := benchmarkName.FindStringSubmatch(fields[0])
		if m == nil {
			return nil, fmt.Errorf("malformed benchmark name %q", fields[0])
		}
		if _, err := strconv.ParseUint(fields[1], 10, 64); err != nil {
			return nil, fmt.Errorf("malformed iteration count in %q: %w", line, err)
		}
		metrics := make(map[string]float64)
		for i := 2; i < len(fields); i += 2 {
			v, err := strconv.ParseFloat(fields[i], 64)
			if err != nil {
				return nil, fmt.Errorf("malformed metric value %q in %q: %w", fields[i], line, err)
			}
			unit := fields[i+1]
			if _, exists := metrics[unit]; exists {
				return nil, fmt.Errorf("duplicate %s metric in %q", unit, line)
			}
			metrics[unit] = v
		}
		samples = append(samples, sample{pkg: pkg, name: m[1], metrics: metrics})
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read benchmark input: %w", err)
	}
	return samples, nil
}

func containsPackageRef(name, pkg string) bool {
	segments := strings.Split(pkg, "/")
	for i := 0; i+1 < len(segments); i++ {
		suffix := strings.Join(segments[i:], "/")
		if strings.Contains(name, suffix) || strings.Contains(name, strings.ReplaceAll(suffix, "/", "_")) {
			return true
		}
	}
	return false
}

type compactStats struct {
	rows   int
	before int
	after  int
}

func compactHistory(r io.Reader, suite, replaceCommit string) ([]byte, compactStats, error) {
	b, err := io.ReadAll(r)
	if err != nil {
		return nil, compactStats{}, fmt.Errorf("read data.js: %w", err)
	}
	text := strings.TrimSpace(string(b))
	if !strings.HasPrefix(text, dataJSPrefix) {
		return nil, compactStats{}, errors.New("data.js is missing benchmark-action prefix")
	}
	jsonText := strings.TrimSpace(strings.TrimPrefix(text, dataJSPrefix))
	jsonText = strings.TrimSuffix(jsonText, ";")

	dec := json.NewDecoder(strings.NewReader(jsonText))
	dec.UseNumber()
	var root map[string]any
	if err := dec.Decode(&root); err != nil {
		return nil, compactStats{}, fmt.Errorf("decode data.js: %w", err)
	}
	if dec.Decode(&struct{}{}) != io.EOF {
		return nil, compactStats{}, errors.New("data.js contains trailing JSON values")
	}
	entries, ok := root["entries"].(map[string]any)
	if !ok {
		return nil, compactStats{}, errors.New("data.js entries is not an object")
	}
	rawRows, exists := entries[suite]
	if !exists {
		if len(entries) == 0 {
			out, err := marshalDataJS(root)
			return out, compactStats{}, err
		}
		return nil, compactStats{}, fmt.Errorf("suite %q is missing", suite)
	}
	rows, ok := rawRows.([]any)
	if !ok {
		return nil, compactStats{}, fmt.Errorf("suite %q is not an array", suite)
	}

	stats := compactStats{rows: len(rows)}
	grouped := make([]map[string]any, 0, len(rows))
	commitIndexes := make(map[string]int, len(rows))
	for i, rawRow := range rows {
		row, ok := rawRow.(map[string]any)
		if !ok {
			return nil, compactStats{}, fmt.Errorf("suite %q row %d is not an object", suite, i)
		}
		rawBenches, ok := row["benches"].([]any)
		if !ok {
			return nil, compactStats{}, fmt.Errorf("suite %q row %d benches is not an array", suite, i)
		}
		stats.before += len(rawBenches)
		commit, ok := row["commit"].(map[string]any)
		if !ok {
			return nil, compactStats{}, fmt.Errorf("suite %q row %d commit is not an object", suite, i)
		}
		commitID, ok := commit["id"].(string)
		if !ok || commitID == "" {
			return nil, compactStats{}, fmt.Errorf("suite %q row %d commit id is invalid", suite, i)
		}
		if commitID == replaceCommit {
			continue
		}
		if existing, found := commitIndexes[commitID]; found {
			prior := grouped[existing]["benches"].([]any)
			grouped[existing]["benches"] = append(prior, rawBenches...)
			continue
		}
		commitIndexes[commitID] = len(grouped)
		grouped = append(grouped, row)
	}

	compactedRows := make([]any, 0, len(grouped))
	for i, row := range grouped {
		compacted, err := compactBenchPoints(row["benches"].([]any))
		if err != nil {
			return nil, compactStats{}, fmt.Errorf("suite %q row %d: %w", suite, i, err)
		}
		stats.after += len(compacted)
		row["benches"] = compacted
		compactedRows = append(compactedRows, row)
	}
	entries[suite] = compactedRows

	out, err := marshalDataJS(root)
	return out, stats, err
}

func marshalDataJS(root map[string]any) ([]byte, error) {
	b, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode data.js: %w", err)
	}
	out := make([]byte, 0, len(dataJSPrefix)+len(b)+1)
	out = append(out, dataJSPrefix...)
	out = append(out, b...)
	out = append(out, '\n')
	return out, nil
}

type pointGroup struct {
	point  map[string]any
	values []float64
}

func compactBenchPoints(raw []any) ([]any, error) {
	points := make([]map[string]any, 0, len(raw))
	derivedBases := make(map[string]bool)
	for i, item := range raw {
		point, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("benchmark point %d is not an object", i)
		}
		name, ok := point["name"].(string)
		if !ok || name == "" {
			return nil, fmt.Errorf("benchmark point %d has invalid name", i)
		}
		unit, ok := point["unit"].(string)
		if !ok || unit == "" {
			return nil, fmt.Errorf("benchmark point %d has invalid unit", i)
		}
		if base, found := strings.CutSuffix(name, " - "+unit); found {
			derivedBases[base] = true
		}
		points = append(points, point)
	}

	groups := make(map[string]*pointGroup)
	order := make([]string, 0, len(points))
	for i, point := range points {
		name := point["name"].(string)
		if derivedBases[name] {
			// The Go parser stores a composite first point whose unit embeds the
			// remaining metrics. The explicit per-unit siblings carry the same
			// information and are stable aggregation keys, so retain only them.
			continue
		}
		unit := point["unit"].(string)
		value, err := numberValue(point["value"])
		if err != nil {
			return nil, fmt.Errorf("benchmark point %d (%q): %w", i, name, err)
		}
		key := name + "\x00" + unit
		g := groups[key]
		if g == nil {
			g = &pointGroup{point: point}
			groups[key] = g
			order = append(order, key)
		}
		g.values = append(g.values, value)
	}

	out := make([]any, 0, len(order))
	for _, key := range order {
		g := groups[key]
		g.point["value"] = median(g.values)
		out = append(out, g.point)
	}
	return out, nil
}

func numberValue(v any) (float64, error) {
	switch n := v.(type) {
	case json.Number:
		f, err := n.Float64()
		if err != nil {
			return 0, fmt.Errorf("invalid value %q: %w", n, err)
		}
		return f, nil
	case float64:
		return n, nil
	default:
		return 0, fmt.Errorf("value is not numeric")
	}
}

func median(vs []float64) float64 {
	if len(vs) == 0 {
		return 0
	}
	sorted := append([]float64(nil), vs...)
	sort.Float64s(sorted)
	mid := len(sorted) / 2
	if len(sorted)%2 == 1 {
		return sorted[mid]
	}
	return (sorted[mid-1] + sorted[mid]) / 2
}
