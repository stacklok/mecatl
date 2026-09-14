# Measuring & observing performance in Go — a reference for mecatl

> Reference companion to `docs/adr/0018-perf-observability.md` (the rationale +
> approaches + decision). This doc is the **survey**: the menu of techniques for
> measuring a Go program's performance, distilled and then filtered through one
> question that matters for this project — *which sources can an AI agent consume
> numerically, and which are human-only artifacts.* Where it names a package it
> uses the real import path; where it says "mecatl already has X" it has been
> checked against the tree (see `docs/architecture/observability.md`).
>
> **Status: the §14 recommendations have since SHIPPED** (perf-observability
> Phases 1+2 — pprof on the loopback admin mux, FlightRecorder, process RSS, the
> runtime-metrics snapshot, OTel metrics, goleak, the perf MCP server; see the
> [perf & observability decision doc](adr/0018-perf-observability.md)). The "gap"
> claims below (§2, §5, §6 —
> e.g. "the Go collector is not registered", "mecatl does NOT have OTel metrics",
> "`EventSink.Emit` carries no ctx") describe the pre-implementation tree and are
> retained as the survey of record.

## The shape of the problem

There is no single "performance number". Every technique sits somewhere on four
axes, and picking the wrong axis is how you measure the wrong thing:

| Axis | Ends | Why it matters here |
|---|---|---|
| **What clock** | on-CPU · wall-clock · off-CPU (blocked) | A streaming agent spends most of its wall time *blocked* on the model and on tool I/O. A CPU profile of mecatl will look idle and tell you nothing about TTFT. |
| **Granularity** | aggregate (a counter/histogram) · per-event (a trace span) | Aggregates answer "is p99 turn latency regressing"; per-event answers "*why was this one turn* slow". |
| **Fidelity** | sampling (pprof, perf) · exact (counters, the execution tracer) | Sampling is cheap and lossy; exact is precise and costly. |
| **Locus** | in-process (pprof, runtime/metrics) · out-of-process (perf, eBPF, /proc) | Only out-of-process sees memory the Go runtime cannot — the tree-sitter WASM leak (in the **since-removed** RepoMap tool) lived in wazero linear memory, invisible to the Go heap profiler (see §9, §10, and `docs/adr/0029-repomap-tree-sitter.md`); it remains the canonical off-heap-leak example. |

## §1 — pprof: `net/http/pprof` + `runtime/pprof`

The standard sampling profiler. Six built-in profiles: `profile` (CPU, timed),
`heap` (in-use + alloc space), `goroutine` (every stack — the leak detector),
`allocs`, `mutex`, `block`. The last two are **off by default** and need knobs:

```go
runtime.SetMutexProfileFraction(100) // sample 1/100 mutex contention events
runtime.SetBlockProfileRate(10000)   // sample a blocking event every ~10µs blocked
```

- Register the handlers on a **dedicated loopback admin mux**, never
  `http.DefaultServeMux` — importing `net/http/pprof` for its side-effect
  registration onto the default mux is the classic way to accidentally expose
  profiles on a public listener. mecatl already keeps a separate loopback
  `/metrics` listener (`cmd/mecated/main.go`, `--metrics-addr 127.0.0.1:9090`);
  pprof handlers belong on that same mux or a sibling.
- Workflow is **capture then diff**: `go tool pprof -base old.prof new.prof`.
- The raw `.prof` is a gzipped protobuf, **not** human- or agent-readable as
  bytes. Parse it with `github.com/google/pprof/profile` (BSD-3) — *already in
  the tree* as a transitive dep — to turn a blob into ranked numeric rows
  (function, flat%, cum%). This is the bridge that lets an agent reason about a
  profile without a flamegraph.

## §2 — `runtime/metrics`: the best programmatic numeric source

stdlib `runtime/metrics` is the canonical, low-overhead, **numeric** view of the
runtime. It supersedes `runtime.ReadMemStats` and `debug.GCStats` (those still
work but cause a STW; `runtime/metrics` does not). Discover the full set at
runtime via `metrics.All()`; read with `metrics.Read([]metrics.Sample)`.

The metrics that map onto mecatl's concerns:

| Metric family | Catches |
|---|---|
| `/sched/goroutines:goroutines` | goroutine **leaks** (a monotone climb across runs is the alarm) |
| `/sched/latencies:seconds` (histogram) | scheduler latency — runnable-but-not-running, the off-CPU tax |
| `/gc/pauses:seconds`, `/gc/heap/*` | GC pause distribution + heap growth |
| `/memory/classes/*:bytes` | **Go-heap** memory growth by class (does NOT see WASM/cgo — see §9) |
| `/cpu/classes/*:seconds` | where CPU went (gc vs user vs idle) |
| `/sync/mutex/wait/total:seconds` | aggregate lock-wait — the cheap signal for dispatch-lock contention |

The Prometheus bridge is `collectors.NewGoCollector(collectors.WithGoCollectorRuntimeMetrics(...))`
from `github.com/prometheus/client_golang/prometheus/collectors` — *already
vendored* (mecatl pins client_golang and ships the `collectors` package). It is
**not currently registered** on mecatl's registry; that is a one-line gap.

## §3 — `runtime/trace` + FlightRecorder

The execution tracer records every scheduler event, GC, syscall, and
user-annotated region — the only tool that explains *off-CPU* time precisely.
Annotate with `trace.NewTask` / `trace.WithRegion` / `trace.Log` to make the
trace domain-meaningful (a task per run, a region per turn, a region per tool).

**`runtime/trace.FlightRecorder` (stdlib since Go 1.25; Mecatl is on Go 1.27)**
is the production-safe form: a **continuously-running ring buffer** you arm at
startup and snapshot *only on an interesting event* (a tail-latency turn, an
error), so you pay tracing cost continuously but only write a bounded window when
something is worth keeping. This is the right tool for "capture what led up to
the slow turn" without recording gigabytes.

Parse a trace programmatically with `golang.org/x/exp/trace` (BSD-3). `go tool
trace`'s timeline UI is **human-only** — never hand it to an agent.

## §4 — Benchmarking, benchstat, PGO

- `testing.B` micro-benchmarks. Use `b.Loop()` (Go 1.24+) instead of the old
  `for i := 0; i < b.N; i++` — it handles setup/teardown correctly and resists
  dead-code elimination. `b.ReportAllocs()` for alloc/op; `b.ReportMetric(x,
  "tokens/s")` for domain throughput (directly relevant — a streaming harness
  cares about tokens/s, not just ns/op).
- Never eyeball one run. `golang.org/x/perf/cmd/benchstat` (BSD-3) runs the A/B
  comparison with a significance test; `benchstat -format=csv` is the
  **agent-consumable** output (the default table is for humans).
- **PGO**: drop a CPU profile captured from a representative workload at the
  module root as `default.pgo` and the Go toolchain auto-applies
  profile-guided optimisation on the next build. Zero code change; the profile
  comes free from §1.

## §5 — OpenTelemetry

CNCF, Apache-2.0 — the project's preferred third-party governance tier.

- **Traces**: mecatl **already has** an OTel trace model (`internal/adapter/telemetry/tracing.go`):
  a run span, a turn span, a tool span, exported over OTLP (`telemetry.Setup`,
  `--otlp-endpoint`). Note its documented limitation: `EventSink.Emit` carries no
  `context.Context` and `session.Event` has no session id, so it keeps **one
  "current run" span per sink instance** and cannot correlate truly concurrent
  runs. Fixing that needs a ctx-aware sink seam.
- **Metrics**: mecatl does **NOT** have OTel metrics — its metrics are
  Prometheus (`client_golang`) only. This is the real gap if a fleet wants
  push-based metrics with **exemplars** (a metric data-point that carries the
  trace ID of an exemplar request, jumping metric→trace) and **exponential /
  native histograms** (auto-bucketed, mergeable across instances — the correct
  shape for tail-latency at fleet scale).
- `go.opentelemetry.io/contrib/instrumentation/runtime` auto-collects the
  runtime metrics of §2 as OTel instruments; a host-metrics collector adds
  process RSS/CPU.

## §6 — Prometheus `client_golang`

CNCF, Apache-2.0. mecatl's `/metrics` is here today, but it exposes **only
domain-derived series** (events, runs, tool calls + a latency histogram, tokens,
cache-hit, active runs) — see `telemetry/metrics.go`. It does **not** register
the **Go collector** (goroutines, GC, heap) or the **Process collector** (RSS,
FDs, CPU). Those are one `reg.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(...))`
away and would immediately surface goroutine count and process RSS at `/metrics`.

Histograms vs summaries: prefer **histograms** (server-side quantiles,
aggregatable) over summaries (client-side quantiles, not aggregatable). Native
histograms are the modern, low-cardinality bucketing. `/metrics` text format is
parseable; a Prometheus server's HTTP API (`/api/v1/query`) returns **JSON** —
agent-consumable.

## §7 — `expvar`

stdlib. Publishes named variables as **JSON** at `/debug/vars`. The cheapest
possible *programmatic* surface — an agent can `GET` it and parse it directly with
no profile-decoding step. `expvar.Func` publishes a live gauge computed on each
scrape (e.g. current goroutine count, current heap-in-use). Caveat: the default
`memstats` var calls `ReadMemStats`, which is **STW** — publish curated
`runtime/metrics` values instead.

## §8 — Continuous profiling

Always-on, low-overhead profiling stored over time so you can retrospect "what
did the heap look like an hour ago".

- **Grafana Pyroscope** (Apache-2.0) — in-process Go SDK pushes profiles to a
  backend. CNCF-adjacent governance, clean.
- **Parca** + **parca-agent** (Apache-2.0) — eBPF, **zero-instrumentation**
  whole-host profiling; sees native/WASM frames a Go-only profiler misses.
- **Datadog** continuous profiler — **commercial backend; avoid** on the
  project's governance screen.

All three need **external infrastructure** — they are a fleet/production answer,
not a local-dev or single-binary answer.

## §9 — OS / kernel / eBPF (out-of-process)

The only locus that sees what the Go runtime cannot.

- `perf stat -j` / `-x,` — hardware counters as **JSON/CSV** (agent-consumable).
  `perf record` → flamegraph is **human-only**.
- `bpftrace` / eBPF — off-CPU analysis (`offcputime`), run-queue latency
  (`runqlat`). Numeric, scriptable, but Linux-only + privileged.
- **`/proc/<pid>/smaps_rollup` and `VmRSS`** — total resident memory **including
  the wazero WASM linear memory and any cgo allocation that the Go heap profiler
  structurally cannot see.** The load-bearing example for mecatl: the (since-removed)
  tree-sitter binding leaked ~23 MB of RSS per RepoMap session in WASM memory that
  `/memory/classes` and `pprof heap` both reported as *nothing*
  (`docs/adr/0029-repomap-tree-sitter.md`). To detect that class of leak you **must**
  read process RSS, not the Go heap.

## §10 — GC, GOMEMLIMIT, escape analysis

- `GODEBUG=gctrace=1` — one line per GC to stderr: heap sizes, pause, goal.
- **`GOMEMLIMIT`** — a soft memory ceiling; set it to ~90% of the container/cgroup
  limit and the GC works harder as you approach it. It made the old "ballast"
  trick obsolete. **Caveat for mecatl:** `GOMEMLIMIT` governs the **Go heap** — it
  does nothing about the off-heap WASM leak, which will OOM the container while the
  Go heap stays small.
- `go build -gcflags=-m` — escape analysis; "escapes to heap" explains alloc
  pressure. `pprof -alloc_space` ranks the allocation sites.

## §11 — goroutine-leak detection

`go.uber.org/goleak` (MIT) — *already in go.sum* (transitive). `goleak.VerifyTestMain(m)`
in a package's `TestMain` fails the test if goroutines outlive it. The high-value
targets in mecatl: the **stream consumer** (the OpenAI SSE loop and its
cancellation path), the **subagent/fork** drain loops, and the **server** Run
registry — every place that spins a background goroutine per run. In production,
a goroutine-count gauge (§2/§6) plus a periodic goroutine-profile diff is the
live equivalent.

## §12 — Tail latency, and why averages lie for a streaming agent

A streaming agent's latency is **not one number**. Measure three separately:

- **TTFT** — time to first token (enqueue → first `message.delta`).
- **inter-token latency** — the gaps between subsequent `message.delta`s (jitter
  shows here, and it is what a TUI render cadence has to keep up with).
- **total turn latency** — enqueue → terminal `result`.

**Coordinated omission** is the trap: if you start the clock when a request
*begins executing* rather than when it was *enqueued*, you systematically miss
the latency of everything that queued behind a slow predecessor. mecatl has a
real internal coordinated-omission source: **mutate-serial dispatch**
(`engine/agent/dispatch.go`) runs mutating tools strictly one at a time, so a
slow `Edit` delays every queued mutation. To measure it honestly, timestamp from
**intent/enqueue**, not from execution start.

For in-process exact quantiles use an **HDR histogram** (BSD-2) or **t-digest**
(Apache/MIT); for aggregatable fleet quantiles use **exponential/native
histograms** (§5/§6).

## §13 — THE decision table: what an agent can consume

This is the table the whole survey exists to produce. An AI agent reasoning over
perf data needs **numbers it can parse**, not pictures. The split:

| Source | Pull/push | Shape | Agent-consumable? | Access shape |
|---|---|---|---|---|
| `runtime/metrics` | pull | numeric | **yes, native** | in-process read → JSON |
| `expvar` `/debug/vars` | pull | numeric JSON | **yes, native** | HTTP GET |
| Prometheus `/metrics` | pull | numeric text | **yes** (parse exposition fmt) | HTTP GET |
| Prometheus HTTP API | pull | numeric JSON | **yes, native** | `/api/v1/query` |
| OTel metrics (backend) | push | numeric JSON | **yes** (backend query API) | backend query |
| pprof `.prof` | pull | **blob** | **only after parse** | `google/pprof/profile` → rows |
| `runtime/trace` / FlightRecorder | pull | **blob** | **only after parse** | `x/exp/trace` → events |
| `perf stat -j` | out-of-proc | numeric JSON | **yes** | exec + parse |
| `/proc/<pid>/smaps_rollup`, VmRSS | out-of-proc | numeric | **yes** | read file |
| eBPF (`offcputime`, `runqlat`) | out-of-proc | numeric | **yes** | exec + parse |
| `go tool pprof -http` flamegraph | — | **picture** | **NO — human only** | browser |
| `go tool trace` timeline | — | **picture** | **NO — human only** | browser |

**Rule of thumb:** runtime/metrics + expvar + Prometheus/PromQL-JSON +
OTel-backend-JSON are numeric-native. pprof + trace are blobs that become
numeric *after a parse step*. The two browser UIs are for humans — **never hand
an agent a flamegraph**; hand it the ranked rows the parser produced.

## §14 — Priority recommendations for mecatl (survey's own ranking)

1. **pprof handlers on the loopback admin mux** + the mutex/block knobs (§1).
2. **Go + Process collectors** on the existing Prometheus registry (§2/§6) —
   goroutines + RSS at `/metrics`, one line.
3. **Structured `runtime/metrics` JSON snapshot** + an `expvar` page (§2/§7) —
   the cheapest agent-consumable surface.
4. **FlightRecorder** armed at startup, snapshot on a tail-latency turn (§3).
5. **OTel metrics** with exponential histograms + exemplars + runtime/host
   collectors, measuring **from enqueue** (§5/§12).
6. **`goleak.VerifyTestMain`** in the stream / subagent / fork / server tests (§11).
7. **Read `/proc` `smaps_rollup`/VmRSS** alongside the Go heap for the WASM leak (§9).
8. **Benchmarks** with `ReportAllocs` + `ReportMetric` gated by `benchstat`, then
   PGO from a captured profile (§4).
9. **Optional** Pyroscope/Parca for fleet continuous profiling (§8).

### Governance screen (the project's dependency-preference order)

stdlib > CNCF/Apache-2.0 > permissive-MIT > commercial.

| Tier | Items | Verdict |
|---|---|---|
| **stdlib / Go-team BSD-3** | `net/http/pprof`, `runtime/pprof`, `runtime/metrics`, `runtime/trace`+FlightRecorder, `expvar`, `testing.B`, PGO, `google/pprof/profile`, `x/exp/trace`, `x/perf/benchstat` | **best** — most are already in the module graph |
| **CNCF / Apache-2.0** | OpenTelemetry (have traces, lack metrics), Prometheus `client_golang` (have it), Pyroscope, Parca | **preferred** third-party |
| **permissive MIT** | `go.uber.org/goleak` (have it), HDR-histogram / t-digest | acceptable |
| **commercial** | Datadog continuous profiler | **avoid** |

The headline: **most of items 1–8 are stdlib or already in mecatl's module
graph** (`google/pprof`, `goleak`, the Prometheus `collectors` package are all
present today; Go 1.27 ships FlightRecorder). The cost of the foundational layer
is wiring, not new dependencies.
</content>
</invoke>
