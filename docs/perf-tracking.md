# Performance regression tracking

This page explains how Mecatl answers one question: did this commit make the harness
slower, fatter, or more expensive than `main`, and will CI say so before it ships?
For diagnosing a running process instead, see
[measuring performance](perf-measurement-survey.md).

Mecatl's dominant costs are tokens and network wall-clock, not CPU, so the tracked
signals favor allocation discipline, goroutine hygiene, and token and cache
efficiency. Everything runs offline on free tooling: `testing.B` benchmarks, a
scenario harness over the reference adapters, and a `gh-pages` trend store.

## What we track

| KPI | Why it matters | Gate |
| --- | --- | --- |
| `allocs/op` on hot paths | Drives GC pressure; deterministic regardless of machine load | Hard |
| `allocs/op` per scenario | Whole-loop allocation budget | Hard (render benches advisory) |
| Goroutine delta per scenario | Catches the delegation and background-child leak class | Hard |
| Tokens per scenario | The dominant cost of an LLM harness | Hard |
| Prompt-cache-hit-rate | A prefix-stability regression silently raises cost | Hard, on whitelisted scenarios |
| `ns/op`, `B/op` on hot paths | Classic latency and memory shape | Advisory trend |
| RSS and wall-clock per scenario | OOM headroom and long-session growth | Recorded in the JSON only |

### Why allocs-first

Allocation counts don't move with CPU load, so they survive noisy shared CI runners
where `ns/op` is unreliable, and in a long-running harness they drive GC pressure
and memory growth. CI gates hard on allocations and treats wall-clock as advisory.

### The non-obvious KPI: prompt-cache-hit-rate

Mecatl relies on a byte-stable prompt prefix for provider-side caching. A change
that perturbs the prefix drops the cache-hit rate and raises token cost, and no CPU
or memory benchmark would notice. Each scenario reports the rate from its
accumulated `session.Usage`; only scripts with cache reads are gated on it.

## The benchmark harness

Neither suite is part of `task test`. Both are offline: `mockllm`, `memfs` or
`memstore`, and `permpolicy`, with no network, live model, or subprocess.

**Microbenchmarks** (`task bench`, default `BENCHCOUNT=10`) live beside the code
they measure in `engine/prompt`, `engine/governance`, and `engine/agent`: prompt
assembly, shell-command classification and permission evaluation, request building,
compaction, and full engine turns. `BenchmarkRun*Turn` includes first-session
startup work; `BenchmarkSteadyState*Turn` prewarms that and measures one recurring
turn. Both are gated. Compare runs locally with `benchstat`; the
[perf-optimization skill](../.claude/skills/perf-optimization/SKILL.md) has the
workflow.

**Scenarios** (`task perf:scenarios`, default `BENCHCOUNT=6`) drive whole loops
through the real engine to catch what microbenchmarks miss:

- **single-session-long**: about 500 tool-using turns in one session. Targets the
  long-session allocation budget and memory growth.
- **team-fanout**: a lead, several members, and a synthesis step. Targets
  goroutine hygiene and allocation under delegation.
- **background-subagents**: detached children, then a drain. Targets the registry,
  drain, and seal path, which has leaked before.
- **compaction-cycle**: history driven past a small context window repeatedly.
  Targets compaction churn and the kept tail.
- **tui-scrollback**: `mecatui` rendering a 400-block scrollback, in a streaming
  worst case, an unchanged steady frame, and a spinner-only tick.

The first four live in `perf/scenarios`; the TUI benches live in
`cmd/mecatui/ui/scrollback_bench_test.go` to reach the unexported render path.
Scenarios must stay deterministic: fixed mock scripts, a `time.Unix(0, 0)`
session epoch, a fresh session and script reset per iteration, and nothing that
reads the wall clock or randomizes in the measured path.

`perf/kpi` is the capture support. It imports only the standard library, so the
engine-shaped values (tokens, cache-hit rate) are passed in by the scenario. It
brackets allocations, samples RSS from `/proc/self/status` on Linux (reporting 0
elsewhere), and measures goroutines as a delta: the count after settling at the end
minus a baseline taken before the measured region, clamped at 0. Zero means no leak.

When `MECATL_PERF_JSON` is set, each scenario writes a `kpi.ScenarioResult` row
stamped with `MECATL_PERF_SHA` and a per-name sample number, and both packages merge
into one file. The field normalization (per-op, per-run, whole-bracket) is the
doc comment on `ScenarioResult` in [`perf/kpi/result.go`](../perf/kpi/result.go).

## Regression gating

[`.github/workflows/perf.yml`](../.github/workflows/perf.yml) is separate from
the main CI workflow because its pull request job is read-only while its `main` job
writes to `gh-pages`. The only credential is `GITHUB_TOKEN`.

**The allocs gate.** [`perf/cmd/allocsgate`](../perf/cmd/allocsgate/main.go)
compares the median `allocs/op` per microbenchmark between the stored baseline and
this run. It fails if any benchmark rose by at least one whole allocation and by
more than 2%. Because allocations are deterministic, no significance test is
needed. The gate is fail-closed: an empty or unparseable new run, or a baseline
that exists but parses to nothing, fails. A missing baseline skips with a notice,
and a run with no benchmark names in common with the baseline warns without failing.

**The scenario suites.** [`perf/cmd/perfconvert`](../perf/cmd/perfconvert/main.go)
groups scenario rows by name, takes the median of each metric, and writes three
`github-action-benchmark` suites:

- **Smaller is better, gated at 102%**: `allocs_per_op` for loop scenarios, plus
  `tokens_total` and `goroutine_delta` for every scenario.
- **Bigger is better, gated at 105%**: `cache_hit_rate` for an explicit whitelist,
  `single_session_long` and `team_fanout`. It is an allowlist, not a "greater than
  zero" filter, so a whitelisted scenario that regresses to 0 still fails.
  `compaction_cycle` and the TUI benches have an honest 0 and emit no point.
- **Render allocs, advisory**: `allocs_per_op` for the `tui_*` benches. Their
  allocation count carries process-wide `runtime.ReadMemStats` noise that
  `perf/kpi` cannot scope to one goroutine, enough to trip a 2% gate on commits that
  never touched the TUI. The trend is recorded but never fails a build.

The pull request job runs the gate and all three suites without writing the store.
The `main` job runs the same suites with auto-push, re-runs the allocs gate against
the previous baseline so a regression that merged still fails loudly, and publishes
an advisory microbenchmark trend (`ns/op`, `B/op`, `allocs/op`).
[`perf/cmd/benchtrend`](../perf/cmd/benchtrend/main.go) reduces the ten samples to
one median per metric per commit and compacts older history the same way.

## Baseline storage

The baseline-storage resolution is simple: the allocs gate's baseline is the raw
`task bench` output that the last `main` run stored on `gh-pages` at
`dev/bench/bench.txt`. Jobs fetch it through the authenticated contents API, which
also works for a private repository. A failed fetch leaves no file rather than an
empty one, so the gate takes its skip path instead of failing closed. The `main`
job writes the new file last and retries the push on a non-fast-forward so a
concurrent dashboard commit can't drop it. The trend dashboards live beside it
under `dev/bench/`.

## Baseline snapshot

A dated reference for reading numbers locally, not the gate's baseline. Allocation,
goroutine, token, and cache-hit values are deterministic and portable; `ns/op` and
bytes are machine-specific shape only. Re-capture with `task bench` and
`task perf:scenarios` after a deliberate change. Benchmarks added after this capture,
such as `BenchmarkSteadyState*Turn`, have no row yet.

Captured `2026-06-14` on a Linux workstation (12 logical CPUs).

Micro hot paths (`task bench`, count=10; allocs/op identical across all runs):

```
BenchmarkBuild                            21 allocs/op     6,224 B/op    ~1.7µs
BenchmarkBuildLargeCatalog                32 allocs/op    28,536 B/op   ~7µs
BenchmarkSplitCommands                    13 allocs/op       584 B/op   ~0.66µs
BenchmarkReadOnlyShell                    19 allocs/op       816 B/op   ~1.29µs
BenchmarkSubstitutionReadOnly             14 allocs/op       408 B/op   ~1.22µs
BenchmarkIsolationApprovable              32 allocs/op     1,272 B/op   ~2.0µs
BenchmarkEvaluatorEvaluate/simple-tool    13 allocs/op       856 B/op   ~0.94µs
BenchmarkEvaluatorEvaluate/plain-bash     18 allocs/op       920 B/op   ~1.39µs
BenchmarkEvaluatorEvaluate/compound-bash  29 allocs/op     1,544 B/op   ~2.8µs
BenchmarkBuildRequest                     77 allocs/op    12,648 B/op   ~4.86µs
BenchmarkHeuristicCompact                199 allocs/op    15,445 B/op   ~17.5µs
BenchmarkCascadeCompact                  249 allocs/op    29,551 B/op   ~24µs
BenchmarkRunReadOnlyTurn                 164 allocs/op    32,769 B/op   ~31µs
BenchmarkRunReadParallelTurn             228 allocs/op    38,671 B/op   ~45µs
BenchmarkRunMutatingTurn                 159 allocs/op    31,898 B/op   ~27.5µs
```

Scenarios (`task perf:scenarios`, count=6; `allocs_per_op`, goroutine delta, tokens):

```
single_session_long      ~36,656 allocs/op   ~4.83 MB/op   goroutines Δ=0   cache-hit 0.90
team_fanout               ~2,690 allocs/op    ~599 KB/op    goroutines Δ=0   cache-hit 0.75
background_subagents      ~1,307 allocs/op    ~258 KB/op    goroutines Δ=0
compaction_cycle          ~4,204 allocs/op    ~570 KB/op    goroutines Δ=0   (39 compactions/40 turns; cache-hit 0 by design)
tui_scrollback_view        ~3,560 allocs/op   ~33.4 MB/op   (400 blocks; streaming worst case: live block revised every op, join always rebuilds)
tui_scrollback_view_steady    ~51 allocs/op    ~137 KB/op   (400 blocks; unchanged frame served from the join cache)
tui_spinner_tick_vpview    ~1,088 allocs/op   ~133 KB/op   (400 blocks; spinner-only Update→View frame served from the viewport cache)
```

The streaming TUI bench revises the live block with a fixed-size, byte-different
body each op, so the cache always misses but `allocs/op` doesn't depend on `b.N`.
The spinner bench measures frames between content flushes, not streaming frames.

## Profile-guided optimization

Profile-guided optimization (PGO) lets the Go compiler optimize from a CPU profile.
The build applies a `default.pgo` found beside a `main` package under the default
`-pgo=auto`. The one reserved slot is `cmd/mecated/default.pgo`, since the server is
the only long-running binary worth profiling, and no file is committed there. Nothing
in the Taskfile or workflows passes `-pgo=off`, so a profile dropped there applies
to every later build, including release images.

No profile ships because the only one capturable offline is the wrong workload.
`task pgo:collect` profiles the scenarios and `engine/agent` benchmarks into
`.scratch/pgo/`, which trains the compiler on `mockllm` code that ships in no
production binary. It is a mechanism check; never commit it.

PGO degrades gracefully. The toolchain treats the profile as a hint: an absent
profile is a no-op, and a stale one matches fewer functions after refactors, so the
win decays toward the non-PGO build rather than regressing it.

To make a real profile, capture from a production-representative `mecated`:

1. Capture CPU profiles from its loopback admin listener under real traffic, for
   example `/debug/pprof/profile?seconds=30`, across a few hosts or time windows.
   Use the same `seconds` value for every capture so no single one dominates.
2. Merge them with `go tool pprof -proto a.pgo b.pgo > default.pgo`.
3. Commit it as `cmd/mecated/default.pgo` and refresh it after hot-path refactors.

## Add a scenario

1. Add a `Benchmark*` to `perf/scenarios` (or, for TUI rendering, beside the code
   in `cmd/mecatui/ui` and add it to the `-bench` pattern in `task perf:scenarios`).
   Use only `engine/` and the reference adapters, never `internal/`.
2. Keep it deterministic: the shared epoch, a fixed script reset each iteration,
   and setup outside `b.Loop()`. Take the goroutine baseline before the measured
   region and record a `kpi.ScenarioResult` with a snake_case name.
3. Decide how `perfconvert` treats it. Add the name to `cacheHitWhitelist` only if
   its script includes cache reads. Add it to `renderAllocAdvisory` only if its
   allocation count is provably noisy, and say why in the code.
4. Run `task perf:scenarios` twice to confirm stable numbers, then add a row to the
   [baseline snapshot](#baseline-snapshot).

## Related

- [Measuring performance](perf-measurement-survey.md)
- [Observability](architecture/observability.md)
- [Observability and resilience](../user-docs/building/what-you-get/observability.md)
