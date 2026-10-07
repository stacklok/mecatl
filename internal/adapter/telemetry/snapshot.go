package telemetry

import (
	"encoding/json"
	"expvar"
	"net/http"
	"runtime"
	"runtime/metrics"
	"sync"
	"time"
)

// processStart is captured at package init so Snapshot can report process
// uptime without a dependency on the OS clock or the boot time. It is the
// process start as observed by this binary, good enough for an uptime gauge.
var processStart = time.Now()

// runtime/metrics sample names read by Snapshot. They are read defensively: a
// name missing on the running Go toolchain leaves its DTO field at the zero
// value rather than panicking, so the snapshot tolerates version drift (the
// metric set is documented as additive, but names have been renamed across
// major Go versions). Keep this list curated and small — it is the read budget
// Phase-2's MCP server inherits.
const (
	metricGoroutines      = "/sched/goroutines:goroutines"
	metricHeapAllocsBytes = "/gc/heap/allocs:bytes"
	metricHeapObjects     = "/gc/heap/objects:objects"
	metricTotalBytes      = "/memory/classes/total:bytes"
	metricHeapObjectBytes = "/memory/classes/heap/objects:bytes"
	metricGCPauses        = "/gc/pauses:seconds"
)

// RuntimeSnapshot is a plain, JSON-serialisable view of the process runtime
// state at one instant. It carries NO OTel/SDK types deliberately: it is the
// reusable read contract that Phase-2's perf-over-MCP server projects directly
// into tool output. Treat the
// field set + JSON tags as a stable wire shape — additive changes only. One
// deliberate exception on record: heap_alloc_bytes was RENAMED to
// heap_allocs_total_bytes — the old key read as a live-heap gauge (the
// Go MemStats.HeapAlloc meaning) when the value is actually the cumulative
// allocation counter, a misread worth a one-time break.
//
// Byte counts are bytes; durations are nanoseconds (GCPauseP99UpperBoundNs) or
// seconds (UptimeSeconds) as named.
//
// PRESENCE vs ZERO: a zero on a runtime/metrics-derived field carries NO
// semantic load — several fields are legitimately zero (GCPauseCount and
// GCPauseP99UpperBoundNs before the first GC; any counter with GOGC=off). Do NOT
// read "zero" as "absent". To learn which metrics were actually present this
// snapshot, consult Available: it lists the curated runtime/metrics names that
// were read (i.e. exist on this toolchain) this snapshot, so absence is explicit
// and zero is just a value.
type RuntimeSnapshot struct {
	// Goroutines is the live goroutine count from /sched/goroutines, falling back
	// to runtime.NumGoroutine() when the sample is unavailable.
	Goroutines int `json:"goroutines"`
	// NumCPU is the number of logical CPUs usable by the process.
	NumCPU int `json:"num_cpu"`
	// GOMAXPROCS is the current GOMAXPROCS setting.
	GOMAXPROCS int `json:"gomaxprocs"`

	// HeapAllocsTotalBytes is the CUMULATIVE number of bytes ever allocated to
	// the heap since process start (/gc/heap/allocs) — a monotonic counter that
	// reads 100+ GB on a long-lived process. It is NOT the live heap; live
	// heap-object memory is HeapObjectBytes (heap_object_bytes). Renamed from
	// heap_alloc_bytes, which collided with the LIVE-gauge meaning of Go
	// MemStats.HeapAlloc.
	HeapAllocsTotalBytes uint64 `json:"heap_allocs_total_bytes"`
	// HeapObjects is the count of live-or-unswept heap objects
	// (/gc/heap/objects), not a cumulative allocation count.
	HeapObjects uint64 `json:"heap_objects"`
	// TotalMemoryBytes is all memory mapped by the runtime (/memory/classes/total).
	TotalMemoryBytes uint64 `json:"total_memory_bytes"`
	// HeapObjectBytes is live heap-object memory (/memory/classes/heap/objects).
	HeapObjectBytes uint64 `json:"heap_object_bytes"`

	// GCPauseCount is the number of GC pauses observed (sum of the pause
	// histogram counts). Legitimately 0 before the first GC — see Available.
	GCPauseCount uint64 `json:"gc_pause_count"`
	// GCPauseP99UpperBoundNs is the histogram BUCKET UPPER BOUND containing the
	// ~p99 rank of the GC pause distribution, in nanoseconds, derived from the
	// /gc/pauses histogram. It is a representative bucket bound, NOT an exact
	// quantile — the field name says so deliberately so a Phase-2 consumer does
	// not over-trust the precision. Legitimately 0 before the first GC.
	GCPauseP99UpperBoundNs uint64 `json:"gc_pause_p99_upper_bound_ns"`

	// RSSBytes is the process resident set size in bytes, or 0 when it could not
	// be read (non-Linux, or /proc unavailable). See process_rss_linux.go.
	RSSBytes uint64 `json:"rss_bytes"`

	// UptimeSeconds is the wall-clock age of the process in seconds.
	UptimeSeconds float64 `json:"uptime_seconds"`

	// Available is the set of curated runtime/metrics sample names that were
	// actually present (and thus read) for this snapshot. It makes metric
	// presence EXPLICIT: a name absent here was not published by this toolchain,
	// whereas a name present here was read even if its value happens to be zero.
	// Phase-2 consumers should test membership here rather than inferring absence
	// from a zero field. The runtime counters (Goroutines/NumCPU/GOMAXPROCS/
	// UptimeSeconds) and RSSBytes are always populated and are NOT listed here —
	// Available tracks only the runtime/metrics-derived fields.
	Available []string `json:"available"`
}

// Snapshot reads a curated set of runtime/metrics samples plus runtime
// counters into a plain RuntimeSnapshot DTO. It is allocation-light and does
// NOT trigger a stop-the-world ReadMemStats (runtime/metrics is sampled
// lock-free), so it is safe to call from a request handler or an MCP tool on a
// hot path.
//
// Missing sample names are tolerated: the corresponding field is left zero. The
// goroutine count falls back to runtime.NumGoroutine() if the sample is absent.
func Snapshot() RuntimeSnapshot {
	snap := RuntimeSnapshot{
		Goroutines:    runtime.NumGoroutine(),
		NumCPU:        runtime.NumCPU(),
		GOMAXPROCS:    runtime.GOMAXPROCS(0),
		RSSBytes:      readRSS(),
		UptimeSeconds: time.Since(processStart).Seconds(),
	}

	// Read only the curated names that actually exist on this toolchain, so a
	// renamed/removed metric never panics the read.
	want := []string{
		metricGoroutines,
		metricHeapAllocsBytes,
		metricHeapObjects,
		metricTotalBytes,
		metricHeapObjectBytes,
		metricGCPauses,
	}
	samples := readMetrics(want)

	if len(samples) > 0 {
		snap.Available = make([]string, 0, len(samples))
	}
	for _, s := range samples {
		// Every sample in `samples` is a name that exists on this toolchain
		// (readMetrics filters against metrics.All), so its presence is what
		// Available records — independent of whether its value is zero.
		snap.Available = append(snap.Available, s.Name)
		switch s.Name {
		case metricGoroutines:
			if s.Value.Kind() == metrics.KindUint64 {
				snap.Goroutines = int(s.Value.Uint64()) //nolint:gosec // goroutine count fits an int
			}
		case metricHeapAllocsBytes:
			if s.Value.Kind() == metrics.KindUint64 {
				snap.HeapAllocsTotalBytes = s.Value.Uint64()
			}
		case metricHeapObjects:
			if s.Value.Kind() == metrics.KindUint64 {
				snap.HeapObjects = s.Value.Uint64()
			}
		case metricTotalBytes:
			if s.Value.Kind() == metrics.KindUint64 {
				snap.TotalMemoryBytes = s.Value.Uint64()
			}
		case metricHeapObjectBytes:
			if s.Value.Kind() == metrics.KindUint64 {
				snap.HeapObjectBytes = s.Value.Uint64()
			}
		case metricGCPauses:
			if s.Value.Kind() == metrics.KindFloat64Histogram {
				count, p99 := summarizeHistogram(s.Value.Float64Histogram(), 0.99)
				snap.GCPauseCount = count
				snap.GCPauseP99UpperBoundNs = uint64(p99 * float64(time.Second)) //nolint:gosec // pause seconds → ns, non-negative
			}
		}
	}

	return snap
}

// readMetrics builds a []metrics.Sample for only the names in want that the
// running toolchain actually publishes (discovered via metrics.All), then reads
// them. Filtering against metrics.All is the version-drift guard: metrics.Read
// panics on an unknown name, so a name absent from All is silently dropped.
func readMetrics(want []string) []metrics.Sample {
	all := metrics.All()
	known := make(map[string]struct{}, len(all))
	for _, d := range all {
		known[d.Name] = struct{}{}
	}
	samples := make([]metrics.Sample, 0, len(want))
	for _, name := range want {
		if _, ok := known[name]; ok {
			samples = append(samples, metrics.Sample{Name: name})
		}
	}
	if len(samples) == 0 {
		return nil
	}
	metrics.Read(samples)
	return samples
}

// summarizeHistogram returns the total count and an approximate value at the
// given quantile from a runtime/metrics Float64Histogram. The quantile is
// computed from the cumulative bucket counts, returning the (exclusive) upper
// bound of the bucket the target rank falls in — a representative, not exact,
// quantile suitable for a snapshot. A nil or empty histogram returns (0, 0).
func summarizeHistogram(h *metrics.Float64Histogram, q float64) (uint64, float64) {
	if h == nil || len(h.Counts) == 0 {
		return 0, 0
	}
	var total uint64
	for _, c := range h.Counts {
		total += c
	}
	if total == 0 {
		return 0, 0
	}
	target := uint64(q * float64(total))
	var cum uint64
	for i, c := range h.Counts {
		cum += c
		if cum >= target {
			// Buckets[i+1] is the exclusive upper bound of bucket i. Guard the
			// index and skip a +Inf upper bound in favour of the lower bound.
			if i+1 < len(h.Buckets) {
				ub := h.Buckets[i+1]
				if ub < 0 || ub > 1e18 { // -Inf / +Inf sentinel — fall back to the lower bound
					return total, h.Buckets[i]
				}
				return total, ub
			}
			return total, h.Buckets[i]
		}
	}
	return total, h.Buckets[len(h.Buckets)-1]
}

// expvarPublishOnce guards the one-time expvar.Publish of the mecatl_runtime
// var. expvar.Publish panics on a duplicate name, so publishing is made
// idempotent across repeated ExpvarHandler calls (e.g. tests that mount more
// than one mux, or the daemon + the embedded server in one process) by a
// sync.Once — the same process-singleton discipline as the FlightRecorder
// accessor. A plain bool flag would race under concurrent ExpvarHandler calls.
var expvarPublishOnce sync.Once

// ExpvarHandler returns the expvar HTTP handler backing /debug/vars, after
// publishing a curated "mecatl_runtime" expvar.Func that returns the current
// Snapshot(). The curated var is preferred over relying on expvar's default
// "memstats" entry, which calls runtime.ReadMemStats and triggers a
// stop-the-world pause on every scrape; Snapshot() reads lock-free
// runtime/metrics instead.
//
// expvar's default handler still also exposes "memstats" and "cmdline" (the
// package registers them in init). That is acceptable on the loopback admin
// surface; the curated mecatl_runtime var is the one Phase-2 reads.
func ExpvarHandler() http.Handler {
	expvarPublishOnce.Do(func() {
		expvar.Publish("mecatl_runtime", expvar.Func(func() any {
			return Snapshot()
		}))
	})
	return expvar.Handler()
}

// MarshalSnapshotJSON is a small convenience used by tests and any caller that
// wants the snapshot bytes directly (Phase-2 reduces server-side instead of
// shipping the raw blob, but the JSON form is the canonical interchange).
func MarshalSnapshotJSON(s RuntimeSnapshot) ([]byte, error) {
	return json.Marshal(s)
}
