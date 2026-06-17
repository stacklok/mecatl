# ADR 0019 — Long-term performance and resource regression tracking

- Status: Accepted
- Date: 2026-06-14
- Scope: CI regression gating and trend tracking for allocation counts, goroutine hygiene, token spend, and cache-hit rate across mecatl's hot paths and whole-loop scenarios

## Context

mecatl's dominant resource costs are token spend and network wall-clock, not CPU. Point-in-time profiling (covered by ADR 0018) answers "why is it slow now" but not "did this commit make it slower than last week." The project needed a free, offline regression-tracking story that catches allocation regressions, goroutine leaks, and prompt-cache-prefix instability — all of which are invisible to wall-clock benchmarks on noisy shared CI runners.

## Decision

Adopt a phased approach using only OSS tooling and no paid infrastructure. Phase 0 defines KPIs (allocs/op, goroutine delta, tokens, cache-hit rate) and gating strategy (hard gate on deterministic signals; advisory on wall-clock). Phase 1 ships hot-path testing.B microbenchmarks. Phase 2 ships an offline whole-loop scenario harness with a JSON KPI emitter. Phase 3 ships a CI trend store and a deterministic allocs gate via a split mechanism: a tested allocsgate binary over task bench plus github-action-benchmark for scenario KPIs and the gh-pages dashboard. Phase 4 wires the PGO mechanism without committing a profile. Phases 5 (continuous profiling) and 6 (bare-metal wall-clock gating) are intentionally deferred.

## Consequences

Phases 0–4 deliver a complete, free OSS story for both regression tracking and build-time optimization with no paid infrastructure. allocs/op is hard-gated (fail-closed on empty/corrupt input); ns/op is advisory. The cache-hit whitelist must be maintained when new scenarios are added. PGO degrades gracefully on a stale or absent profile. Current behaviour lives in docs/architecture.md; shipped/deferred phase status is in the production readiness tracker.

---

Sibling to the [performance observability design](0018-perf-observability.md), which
covers the *introspection* half (pprof, flight recorder, OTel metrics, the perf
MCP, `--perf`, goleak).

## The gap this closes

`perf-observability.md` answers **"why is it slow/fat right now?"** — point-in-time
debugging. It does **not** answer **"did this commit make mecatl slower or fatter
than last week, and will CI tell me before it ships?"** That is a *regression
tracking* problem: baseline + time-series + a gate. This doc proposes how we close
it without standing up paid infrastructure.

Guiding calibration: mecatl is a library / CLI / embedded harness, not a fleet of
long-lived services. Its dominant resource cost is **tokens / $ and network
wall-clock**, not CPU. So the KPIs skew toward allocation discipline, memory growth
in long-running processes, goroutine hygiene, and token/cache efficiency — not CPU
flamegraphs (which the existing introspection tooling already covers on demand).

## Phase 0 — What we track (KPIs + baselines)

Gate **hard** on the deterministic/low-noise signals; treat wall-clock as advisory
until/unless we have a stable bench environment (see Phase 6 in the roadmap).

| KPI | Why it matters here | Noise | Gate | Current baseline |
|---|---|---|---|---|
| `allocs/op` on hot paths | Leading indicator of GC pressure; deterministic regardless of machine load | very low | **hard** | per-benchmark — see [Baseline snapshot](#baseline-snapshot) (micro) |
| Total allocations per offline scenario | Whole-loop allocation budget | very low | **hard** | per-scenario `allocs_per_op` — see [Baseline snapshot](#baseline-snapshot) (scenarios) |
| RSS-over-time on a fixed scenario | Long-session / scrollback growth (e.g. the `SetContent` O(scrollback) follow-up) | low | soft → hard | `rss_peak_bytes` / `rss_final_bytes` in the JSON (machine-specific; advisory) |
| End-of-run goroutine count | Delegation / team leak class we have hit before | deterministic | **hard** | **0** (delta) on every scenario — see [Baseline snapshot](#baseline-snapshot) |
| Tokens per scenario (input/output) | The dominant cost of an LLM harness | deterministic (offline) | **hard** | per-scenario — see [Baseline snapshot](#baseline-snapshot) |
| Prompt-cache-hit-rate per scenario | A prefix-stability regression silently doubles cost and no CPU/mem bench catches it | deterministic (offline) | **hard** | single_session **0.90**, team_fanout **0.75**; 0 by design on compaction/tui |
| `ns/op` on hot paths | Classic latency | high in shared CI | advisory | machine-specific; not pinned (advisory only) |
| Peak RSS per scenario | OOM headroom / `GOMEMLIMIT` budgeting | low | soft | `rss_peak_bytes` in the JSON (machine-specific) |

### Baseline snapshot

Captured `2026-06-14` on a Linux dev workstation (12 logical CPUs) at the Phase-1/2
landing. **`allocs/op` / `*_per_op` / cache-hit / goroutine-delta are deterministic and
portable — these are the gated baselines.** `ns/op` and `rss_*` are machine-specific and
recorded for shape only (advisory). The authoritative, commit-keyed baseline is owned by
the Phase 3 trend store once it lands; this block is the bootstrap reference. Re-capture
with `task bench` (micro) and `task perf:scenarios` (scenarios).

Micro hot paths (`task bench`, count=10 — allocs/op were identical across all 10 runs):

```
BenchmarkBuild                            21 allocs/op     6,224 B/op    ~1.7µs
BenchmarkBuildLargeCatalog                32 allocs/op    28,536 B/op   ~7µs
BenchmarkSplitCommands                    13 allocs/op       584 B/op   ~0.66µs
BenchmarkReadOnlyBash                     19 allocs/op       816 B/op   ~1.29µs
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

Scenarios (`task perf:scenarios`, BENCHCOUNT=6 — `allocs_per_op`, goroutine delta, tokens):

```
single_session_long      ~36,656 allocs/op   ~4.83 MB/op   goroutines Δ=0   cache-hit 0.90
team_fanout               ~2,690 allocs/op    ~599 KB/op    goroutines Δ=0   cache-hit 0.75
background_subagents      ~1,307 allocs/op    ~258 KB/op    goroutines Δ=0
compaction_cycle          ~4,204 allocs/op    ~570 KB/op    goroutines Δ=0   (39 compactions/40 turns; cache-hit 0 by design)
tui_scrollback_view        ~3,560 allocs/op   ~33.4 MB/op   (400 blocks; STREAMING worst case — live block REVISED every op with a fixed-size body, join always rebuilds; allocs/op b.N-independent)
tui_scrollback_view_steady    ~51 allocs/op    ~137 KB/op   (400 blocks; UNCHANGED frame — join cache serves the memoized string; was ~94 allocs / ~32.5 MB/op pre-cache)
```

> **tui-scrollback hotspot (2026-06-15).** A heap profile of the render path pinned
> the per-frame cost to the JOIN in `renderConversation`, NOT `vp.SetContent`:
> `strings.Builder.WriteString` was ~91% of allocations — every flushed frame
> re-copied all 400 cached block strings into a fresh `Builder`, even when the
> per-block cache hit on every block. The fix caches the whole joined string
> (`renderer.joinCache`, keyed on a `blockRenders`/block-count/width/expand
> signature) and reuses it verbatim on any frame that re-rendered no block. The
> streaming bench (`tui_scrollback_view`) REVISES the live block every op (with a
> fixed-SIZE, byte-DIFFERENT body — see the bench-determinism note below) so the
> join always rebuilds — it is the worst-case streaming floor and is flat across the
> change. The realistic win is the UNCHANGED frame (cursor move, scroll, the
> twice-per-message `renderInput`): `tui_scrollback_view_steady` falls from
> ~32.5 MB/op to ~137 KB/op (−99.6% B/op; the residual is `vp.SetContent`'s line
> split, the named follow-up).

> **tui-scrollback bench determinism + advisory render suite (2026-06-17).** The
> streaming bench originally APPENDED a byte to the live block every op, so the block
> — and the markdown it rendered — GREW with the iteration count, making the captured
> `allocs/op` a function of `b.N`. Combined with the process-wide
> `runtime.ReadMemStats` background noise (`perf/kpi` cannot goroutine-scope the read,
> and quiescing is invasive), the gh-pages trend showed the tui render allocs swinging
> ~4.6 % (`tui_scrollback_view`) / ~30 % (`tui_scrollback_view_steady`) on commits
> that touched no TUI code — enough to false-positive the gated 2 % smaller-suite
> threshold. Two coordinated fixes: (1) the bench now REVISES the live block with a
> fixed-SIZE, byte-DIFFERENT body per op
> ([`cmd/mecatui/ui/conversation.go`](../../cmd/mecatui/ui/conversation.go)
> (`reviseAssistant`)) so `markdownAt`'s `src`-keyed cache still misses every op (the
> join still ALL-misses — the worst-case streaming floor) but the block no longer
> grows, making `allocs/op` `b.N`-independent (guarded by
> `TestScrollbackReviseAllocsIndependentOfN`); (2)
> [`perf/cmd/perfconvert/main.go`](../../perf/cmd/perfconvert/main.go) routes the two
> `tui_scrollback_view*`/`allocs_per_op` points to a dedicated ADVISORY
> `customSmallerIsBetter` suite (`fail-on-alert:false`) — the deterministic
> escalation the smaller-suite comment anticipated, now SHIPPED — while their
> deterministic siblings (`tokens_total`/`goroutine_delta`, always 0) stay on the
> gated smaller suite.

> **prompt inventory-render swap (2026-06-15).** A stateless, byte-identical swap in
> [`engine/prompt/builder.go`](../../engine/prompt/builder.go): `toolInventory` now
> writes each `- name: purpose` entry via direct `strings.Builder` writes instead of
> `fmt.Fprintf` (dropping the per-tool reflection allocation), and `firstLine` extracts
> the first non-empty line via `strings.Cut` instead of `strings.Split` (dropping the
> per-call slice allocation). Output is unchanged — the gauntlet #6 stable-prefix tests
> are the guard. `BenchmarkBuildLargeCatalog` falls 224 → 32 allocs/op (−85.7%) and
> `BenchmarkBuild` 31 → 21 (−32.3%); no new state, no memoization.

`goroutines Δ=0` on every scenario means no leak across the run — the gated invariant
for the team / background-subagent leak class.

### Why allocs-first

Allocation counts are deterministic: they do not move with CPU load, so they survive
noisy shared CI runners where `ns/op` is useless. For a long-running harness they are
*also* the thing that actually drives GC pressure and memory growth. So allocs are
both the lowest-noise signal and a high-value one — gate hard on them, gate soft (or
not at all in shared CI) on wall-clock.

### The non-obvious KPI: prompt-cache-hit-rate

mecatl relies on a **byte-stable prompt prefix** for provider-side caching. A change
that perturbs the prefix tanks the cache-hit rate and silently doubles token cost —
and *no CPU or memory benchmark would ever catch it*. `session.Usage` already carries
`CacheReadTokens ⊂ InputTokens`, so the raw signal exists; this KPI just tracks it
per scenario over commits. Arguably higher value than any `ns/op` number.

## Regression-detection methodology

- **Sampling:** `-benchmem -count=10 -run='^$'` (skip unit tests to cut noise),
  consistent `GOMAXPROCS`.
- **Comparison:** `benchstat` (`golang.org/x/perf/cmd/benchstat`) — reports deltas
  with a confidence interval + p-value, distinguishing real change from noise. This
  is the single most important tool in the Go ecosystem for this.
- **Noise handling** (in order of cost): allocs-first gating (free); relative
  comparison (baseline + candidate on the *same* CI job, compare the ratio);
  change-point detection over a window of history (Phase 3, Bencher) for fewer false
  positives than threshold-on-last-value; dedicated bare-metal runner + `perflock`
  (Phase 6) for trustworthy wall-clock.

## Phase 1 — Status

**Shipped.** Hot-path `testing.B` microbenchmarks now live next to the code they
measure, each using the Go 1.25 `for b.Loop()` form with results assigned to
package-level sinks so dead-code elimination cannot delete the work:

- [`engine/prompt/bench_test.go`](../../engine/prompt/bench_test.go) — `BenchmarkBuild`,
  `BenchmarkBuildLargeCatalog` (per-turn prompt assembly, the hot path inside
  `Engine.buildRequest`).
- [`engine/governance/bench_test.go`](../../engine/governance/bench_test.go) —
  `BenchmarkSplitCommands`, `BenchmarkReadOnlyBash`, `BenchmarkSubstitutionReadOnly`,
  `BenchmarkIsolationApprovable`, and `BenchmarkEvaluatorEvaluate` (sub-benchmarks for
  simple-tool / plain-Bash / compound-Bash over a realistic deny+ask+allow rule set).
  These are the security-critical Bash gate + permission-fold paths.
- [`engine/agent/bench_test.go`](../../engine/agent/bench_test.go) —
  `BenchmarkRunReadOnlyTurn` (read-parallel dispatch), `BenchmarkRunMutatingTurn`
  (serial dispatch); full engine runs over `mockllm` + `memfs`, offline.
- [`engine/agent/bench_internal_test.go`](../../engine/agent/bench_internal_test.go) —
  `BenchmarkBuildRequest`, a white-box benchmark of the unexported per-turn request
  assembler.

All four are offline (mockllm + memfs, no network/live model) and import no
`internal/...` package.

Run them with `task bench` (see [`Taskfile.yml`](../../Taskfile.yml)) — overridable
sample count via `BENCHCOUNT` (default 10), `-run='^$'` to skip unit tests,
`-benchmem` for the allocs/op signal. This is the command that fills the `allocs/op`
baseline cells above: run it on `main`, paste the `allocs/op` numbers (or let the
Phase 3 trend store own them), and use `benchstat` to compare a candidate against the
baseline. `task bench` is deliberately NOT part of `task test` — same posture as
`task fuzz`.

## Phase 2 — Status

**Shipped.** The offline scenario harness now lives in two homes, both
`testing.B`-driven (no `cmd/` binary), behind
[`task perf:scenarios`](../../Taskfile.yml) (manual / CI target — NOT part of
`task test`, same posture as `task bench`):

- [`perf/kpi/`](../../perf/kpi) — the stdlib-only KPI-capture support: the
  per-scenario metric shape (`ScenarioResult` + `WriteJSON`), the
  allocation/RSS/wall-clock capture bracket (`Capture`), the Linux
  `/proc/self/status` RSS sampler (`rss_linux.go`; a `rss_other.go` no-op
  off-Linux), and the settle-then-count goroutine probe
  (`GoroutinesAfterSettle`). It imports ONLY the standard library — never
  `engine/...` or `internal/...` — so the engine-shaped KPIs (tokens,
  cache-hit-rate) are passed IN by the scenario caller.
- [`perf/scenarios/`](../../perf/scenarios) — the four whole-loop scenario
  benchmarks (external-test package, imports `engine/...` + the
  `engine/adapter/{mockllm,memfs,memstore,permpolicy}` reference adapters +
  `perf/kpi`, never `internal/...`): `BenchmarkSingleSessionLong` (~500-turn
  tool-using loop), `BenchmarkTeamFanout` (lead + K workers + synthesis),
  `BenchmarkBackgroundSubagents` (M detached children + drain), and
  `BenchmarkCompactionCycle` (history driven past a small `ContextWindowTokens`
  repeatedly). Each records a `kpi.ScenarioResult`; a `TestMain` flushes them to
  `$MECATL_PERF_JSON` when set.
- [`cmd/mecatui/ui/scrollback_bench_test.go`](../../cmd/mecatui/ui/scrollback_bench_test.go)
  — `BenchmarkScrollbackView` (streaming worst case) and
  `BenchmarkScrollbackViewSteady` (unchanged frame), the TUI scrollback render path
  (`refreshView` → `renderConversation`'s join, the profile-confirmed O(scrollback)
  hotspot — now memoized; see the tui-scrollback note above). It is an internal
  `_test` file so it can reach the unexported render path; `perf/kpi` is imported
  ONLY in the test file — the production `ui` package stays free of the perf
  dependency. Its rows MERGE into the same `$MECATL_PERF_JSON` (two metric families,
  one file).

All scenarios are deterministic and offline (`mockllm` + `memfs`/`memstore` +
`permpolicy`, fixed scripts, `time.Unix(0,0)` session epoch, `llm.Reset()` between
iterations) — no network, no live model, no `os/exec`. The ONE measurement-noise
exception is the `tui_scrollback_view*` render benches' `allocs/op` (process-wide
`runtime.ReadMemStats` background noise + a `b.N` residual), which is why that single
metric rides the advisory render suite, not the gated one. They are Benchmarks, so the
default `-run` skips them under `task test`; the only `Test*` in each home is a
cheap `TestMain` JSON flush.

### JSON KPI shape (what Phase 3 ingests)

Each row is a `kpi.ScenarioResult` (`schema_version` 1). The full per-field
normalization contract is the doc-comment on `ScenarioResult` in
[`perf/kpi/result.go`](../../perf/kpi/result.go); the points a trend gate must
know:

- **Grouping key:** `(name, git_sha)`. `task perf:scenarios` stamps `git_sha` from
  HEAD (overridable via `MECATL_PERF_SHA`), and `sample` is the per-`name` ordinal
  (0,1,2…) within one run, so `-count=N` emits N groupable rows per scenario.
- **`goroutines_end` is a DELTA**, not a raw count: live goroutines at end-of-run
  minus a baseline taken before the measured region, clamped at 0. **0 = no leak**;
  a positive value is the leak count. Gate it hard on the delegation scenarios
  (`team_fanout`, `background_subagents`) — that is the leak class it guards.
- **`compaction_cycle.cache_hit_rate == 0` is EXPECTED / by-design**: that
  scenario's script carries no cache-read tokens (it exercises the
  compact-and-replace cycle, not prefix caching). A Phase-3 cache-hit-rate gate
  must WHITELIST `compaction_cycle` (and any other no-cache scenario, e.g. the
  token-less `tui_scrollback_view`) rather than false-positive on the honest 0.
  The cache-hit signal is meaningful on `single_session_long` (~0.9) and
  `team_fanout` (~0.75), where the script scripts a cache-read fraction.

The original design sketch (kept for context):

The highest-leverage phase, because it catches the failure modes microbenchmarks
miss (leaks, memory growth, token/cache regressions) and it is **free and offline**:
it reuses the deterministic `mockllm` + `mecademo` path — no network, no cost.

### Scenarios (fixed, scripted, deterministic)

Each scenario is a scripted `mockllm` conversation driven through the real engine:

- **single-session-long** — N turns (e.g. 500) of tool-using loop; targets RSS
  growth + allocation budget over a long session.
- **team-fanout** — a supervisor with K members + synthesis; targets goroutine
  hygiene and per-round allocation under delegation.
- **background-subagents** — M detached children + drain; targets the registry /
  drain / seal path that has leaked before.
- **tui-scrollback** — render a large scrollback through `mecatui`'s `refreshView`
  path; targets the `renderConversation` join (the profile-confirmed O(scrollback)
  hotspot, now memoized — see the tui-scrollback note above). Two variants: a
  streaming worst case (live block mutates every frame) and a steady unchanged frame
  (the join-cache fast path).
- **compaction-cycle** — drive history past the compaction threshold repeatedly;
  targets allocation churn + correctness of the kept tail.

Scenarios live next to the offline driver (candidate home: `internal/app` test
support or a dedicated `perf/scenarios` package that imports only `mockllm` +
composition). They must stay deterministic — fixed mock scripts, injected `Clock`
(already supported), no `Date.now`/random in the path.

### Metrics captured per scenario

- `allocs` and `bytes` total (`runtime.MemStats` / `runtime/metrics` deltas around
  the run, with `runtime.GC()` bracketing).
- RSS sampled over time (peak + final; gopsutil already a transitive dep, or read
  `/proc/self/status` directly to avoid a new dep — decide in spec).
- goroutine count at end-of-run (promote goleak from boolean pass/fail to a *tracked
  number*).
- tokens (input/output) and cache-read tokens from the accumulated `session.Usage`.
- wall-clock (advisory).

### Output format

Emit two shapes from one run:

1. **benchstat-compatible** lines (so the same `benchstat` gate covers micro + macro).
2. **JSON** (so Phase 3's trend store can ingest scenario KPIs that aren't
   `testing.B`-shaped — tokens, cache-hit-rate, RSS-over-time).

### CI wiring (Phase 3 hooks)

Run on PR; compare against the `main` baseline; fail on a significant **allocs /
goroutine / token / cache-hit** regression; report **RSS / ns/op** as advisory
comment. Trend store: `benchmark-action/github-action-benchmark` (free, `gh-pages`)
to start; self-hosted Bencher if/when change-point detection earns its keep.

## Phase 3 — Status

**Shipped.** The trend store + regression gate now live in a dedicated workflow
[`.github/workflows/perf.yml`](../../.github/workflows/perf.yml) (separate from
`ci.yml` because the two jobs need different permission postures — the PR job is
read-only, the main job needs `contents: write` for `gh-pages`). The mechanism is a
DELIBERATE SPLIT of two complementary OSS tools, not one:

1. **A tested allocs gate** over the `task bench` microbenchmarks. The PR job
   fetches the previous-main `bench.txt` baseline from `gh-pages` via AUTHENTICATED
   `gh api` (the `GITHUB_TOKEN`; `raw.githubusercontent.com` is unauthenticated and
   404s on a PRIVATE repo — which this is — so the raw URL would silently sink the
   gate into its skip path; the contents API is private-repo-safe). A genuinely
   absent baseline (the very first run) leaves no file → the gate skips green with a
   notice; a failed fetch must leave NO file, not an empty one (else allocsgate's
   present-but-empty→fail-loud path would misfire). Then
   [`perf/cmd/allocsgate/main.go`](../../perf/cmd/allocsgate/main.go)
   compares the median `allocs/op` per benchmark and FAILS iff some benchmark rose
   beyond an epsilon (**≥ 1 whole alloc AND > 2 %**). The gate decision is this
   epsilon comparison over the raw bench numbers — allocs are deterministic, so no
   statistical significance test is needed (that is benchstat's job; benchstat
   stays the LOCAL human A/B tool, documented in `task bench` help, and is
   deliberately NOT in the CI gate path). allocsgate is FAIL-CLOSED: an
   empty/corrupt bench file or baseline fails rather than passing on no comparison;
   a missing baseline (first run) skips green; an all-renamed (no-overlap) run warns
   without failing. allocs/op is hard-gated; `ns/op` is advisory.
2. **`benchmark-action/github-action-benchmark`** for the scenario KPIs and the
   trend dashboard. The scenario JSON (`$MECATL_PERF_JSON`) is reshaped by a small
   converter, [`perf/cmd/perfconvert/main.go`](../../perf/cmd/perfconvert/main.go),
   into three github-action-benchmark custom-format suites:
   - **smaller-is-better** (gated) — per scenario `allocs_per_op` (EXCEPT the render
     benches; see render below), plus `tokens_total` (input+output) and
     `goroutine_delta` for ALL scenarios. One `alert-threshold: 102%` covers all
     three metrics; the LOOP scenarios' allocs are consequently gated at 2 %, which
     is safe because they are deterministic.
   - **bigger-is-better** (gated) — `cache_hit_rate`, emitted ONLY for the explicit
     whitelist `{single_session_long, team_fanout}`. `alert-threshold: 105%`. The
     by-design-0 scenarios (`compaction_cycle`, the `tui_*` benches) must NOT emit a
     cache-hit point — the converter hardcodes the allowlist rather than a `>0`
     heuristic, so a genuine cache regression to 0 on a whitelisted scenario still
     produces a point and fails the gate.
   - **render allocs (advisory)** — the `tui_scrollback_view*`/`allocs_per_op` points
     ONLY, on a `customSmallerIsBetter` suite with `fail-on-alert:false`. SHIPPED as
     the escalation the smaller suite anticipated: render allocs are the ONE advisory
     exception to the otherwise hard-gated allocs/op, because they are NOT
     deterministic on a shared runner (a `b.N` residual + process-wide
     `runtime.ReadMemStats` background noise that `perf/kpi` cannot goroutine-scope —
     the gh-pages history showed ~4.6 % / ~30 % swings on zero-TUI-code commits). The
     advisory suite records their trend without ever failing a PR; the converter
     hardcodes the `renderAllocAdvisory` allowlist (mirror it when a new render bench
     is added).

The **PR-vs-main split:** the PR job runs the two GATED github-action-benchmark
suites with `fail-on-alert: true` (plus the advisory render suite with
`fail-on-alert: false`) but `auto-push: false` — it fails a regressing PR without ever
writing the store. The main job (`push`) runs the same suites with `auto-push: true`
to update the `gh-pages` dashboard, runs the advisory `go`-tool trend (ns/op +
allocs, `fail-on-alert: false`), re-runs the allocs gate against the just-superseded
baseline to catch a regression that merged, and finally writes the raw `bench.txt`
back to the `gh-pages` data dir (with a rebase-retry push so a concurrent gh-pages
advance never silently drops the baseline update) so the next PR's gate has a
baseline to fetch.

**Security posture:** `pull_request` (never `pull_request_target`); the PR job is
`contents: read` (no push), the main job is `contents: write` (gh-pages only); the
only credential is the automatic `GITHUB_TOKEN` — no PAT, no repo secret. All
actions SHA-pinned with a `# vX.Y.Z` comment, matching the house pins in `ci.yml`.

### Trend dashboard (live)

The interactive trend chart auto-publishes to GitHub Pages on every push to `main`:

> **https://potential-barnacle-mvm429e.pages.github.io/dev/bench/**

Access-controlled — sign in to GitHub with access to the repo. The hostname is a
GitHub-assigned random slug *because the repo is private/internal* (private Pages are
served from an unguessable `*.pages.github.io` subdomain rather than the predictable
`<owner>.github.io/<repo>`); it is **stable** across builds (bookmark it), but a new
slug is minted if Pages is ever disabled and re-enabled. The chart lives under the
`/dev/bench/` path — the bare-domain root 404s (github-action-benchmark only writes
into `dev/bench/`). For a memorable URL, point a custom domain at it
(Settings → Pages → Custom domain + a DNS CNAME).

**Private-repo posture:** the gate and the trend store work fully on this private repo
— github-action-benchmark reads/writes `gh-pages` over authenticated git
(`GITHUB_TOKEN`), and the allocs baseline is fetched over the authenticated contents
API (NOT `raw.githubusercontent.com`, which 404s on private). `gh-pages` is
bootstrapped once as an empty orphan branch (github-action-benchmark's `auto-push`
fetches the branch before it can create it, so it must pre-exist on the first run);
the raw data is also browsable on the `gh-pages` branch directly.

`perfconvert` imports ONLY `perf/kpi` + the standard library (same leaf posture as
`perf/kpi` itself), hard-fails on a `schema_version` mismatch, and aggregates the
`-count=N` samples by median per metric. Its `main_test.go` runs offline under
`task test`.

## Phase 4 — Status

**Shipped — the mechanism, deliberately NOT a committed profile.** Profile-Guided
Optimization is wired so a real profile auto-applies the moment it is dropped in,
the collection path is a first-class task, and the refresh process is written down.
No profile is committed.

### Honesty assessment (why no profile ships today)

Three facts shape the decision, and they all point the same way:

1. **The only profile we can capture offline is partial-coverage.** Every benchmark
   in `task bench` / `task perf:scenarios` runs over the `mockllm` + `memfs`/
   `memstore` path — exactly the path that ships in **no** production binary. A
   `default.pgo` trained on it would teach the compiler to optimize the mock
   adapters and the offline harness, not the real OpenAI/Anthropic SSE decode, the
   resilience wrapper, or the gRPC/HTTP server surface. Committing it would be
   training on the wrong distribution, so we don't.
2. **PGO's ceiling here is sub-1% of wall-clock.** mecatl's dominant cost is
   network/token wall-clock (the calibration this whole doc opens with), not CPU.
   Go's published PGO win is ~2–14% **of CPU time**; for a harness whose CPU is a
   thin slice of a network-bound run, that is a fraction of a percent of the wall
   clock a user feels. PGO is worth wiring (it is free once a real profile exists),
   not worth contorting the build to force.
3. **The floor is no-PGO — PGO cannot pessimize on an unrepresentative profile.**
   The Go toolchain treats the profile as a *hint*: a stale or unrepresentative
   `default.pgo` degrades gracefully toward the non-PGO build, it does not make a
   correct program slower than no-PGO. So the cost of shipping the mechanism with
   no profile (or, later, a slightly stale one) is bounded at "no win", never a
   regression. That asymmetry is why wiring-the-slot-now / fill-it-later is safe.

### The `task pgo:collect` mechanism

[`task pgo:collect`](../../Taskfile.yml) (manual target — NOT part of `task build`
or `task test`, same posture as `task bench`/`task perf:scenarios`) captures CPU
profiles from the offline benchmarks under a **fixed** iteration count
(`-benchtime=Nx`, default `200x`, override via `PGOITERS`). It captures two
families (the `perf/scenarios` whole-loop benchmarks and the `engine/agent`
dispatch/build-request microbenchmarks), then merges them with
`go tool pprof -proto a.cpu b.cpu > default.pgo`. The merge is **sample-weighted**:
`-benchtime=Nx` fixes the iteration count, not wall-time, and the whole-loop
scenarios run ~100x longer per op than the agent micro-benches — so the scenario
samples dominate the merged profile (~19:1 in practice). That bias is desirable
here: the scenarios are the more production-representative workload, and the
agent micro-benches are a subset of the dispatch path they already exercise. This
is a throwaway provisional artifact, so capture balance is a non-goal — see the
production refresh path below, where equal-`?seconds=N` captures DO equalize
wall-time. Everything lands under the
gitignored `.scratch/pgo/`, and the recipe prints a loud banner that the result is
a PROVISIONAL OFFLINE profile (mockllm only) that must NOT be committed as
`cmd/mecated/default.pgo`. It is a mechanism check and a local experiment, nothing
more.

### The per-binary decision

PGO is per-`main`. The Go build looks for a `default.pgo` **next to the `main`
package** and, under the default `-pgo=auto`, applies it automatically. The single
slot we reserve is `cmd/mecated/default.pgo` (the server is the only long-running,
CPU-doing binary worth profiling), and it is **left absent**: with no file there,
`-pgo=auto` is a no-op and the build is byte-for-byte the non-PGO build. Drop a real
profile at that path and `go build`/`ko build` pick it up with zero flag or workflow
changes (confirmed: no `-pgo=off` anywhere in [`Taskfile.yml`](../../Taskfile.yml),
[`.github/workflows/ci.yml`](../../.github/workflows/ci.yml), or
[`.github/workflows/release.yml`](../../.github/workflows/release.yml)). `mecatui`
and `mecademo` get no profile — the TUI client and the offline demo are not
CPU-bound server workloads. A `*.pgo binary linguist-generated` rule is pre-staged
in [`.gitattributes`](../../.gitattributes) so a future committed profile is treated
as a binary blob.

### The production refresh process (how a REAL profile gets made)

When a representative `cmd/mecated/default.pgo` is wanted, capture from a **real**
mecated under real load, not from the offline harness:

1. Run a production-representative `mecated`. The loopback admin mux serves
   `/debug/pprof` alongside `/metrics` (`--metrics-addr`, default `127.0.0.1:9090`;
   set empty to disable; see the
   [performance observability design](0018-perf-observability.md)). The
   surface is loopback-only and never exposed.
2. Capture a CPU profile under representative traffic:
   `curl -o prod-a.pgo 'http://127.0.0.1:9090/debug/pprof/profile?seconds=30'`
   (repeat across a few hosts / time windows for coverage).
3. Merge the captures: `go tool pprof -proto prod-a.pgo prod-b.pgo > default.pgo`.
   **Caveat:** `-proto` merging assumes the inputs cover comparable durations — use
   the **same** `?seconds=` on every capture so one host doesn't dominate the merge.
4. Drop the result at `cmd/mecated/default.pgo`, commit it (the `.gitattributes`
   rule makes it a binary blob), and it is auto-applied by every subsequent
   `go build`/`ko build` — including the release image — via `-pgo=auto`.

### Staleness caveat

A `default.pgo` matches functions by name/structure. A large refactor (renames,
inlining-boundary changes, restructured hot paths) degrades the match rate, so the
PGO win decays toward no-PGO over time — it does not regress, but it stops helping.
Treat a committed profile as something to **refresh periodically** (or after a
notable hot-path refactor), the same way the baseline snapshot above is re-captured;
a stale profile is a missed win, never a correctness or performance hazard.

## Roadmap & the OSS boundary

| Phase | Goal | OSS tools | OSS gap |
|---|---|---|---|
| 0 — KPIs + baselines | Decide what we measure | (a doc) | none |
| 1 — Micro-gating ✅ | Fail a PR that regresses a hot path | `testing.B`, `benchstat` (`task bench`) | none for allocs; trustworthy `ns/op` needs Phase 6 |
| 2 — Scenario harness ✅ | Catch leaks / mem growth / token regressions offline | stdlib `runtime`, `mockllm` driver (`task perf:scenarios`) | none |
| 3 — Trend + history ✅ | See regressions over time, not just per-PR | github-action-benchmark (free, `gh-pages`) + a tested allocs gate (`perf/cmd/allocsgate`, `.github/workflows/perf.yml`) | Bencher's *hosted* analytics + same-bare-metal-local-and-CI is the paid delta |
| 4 — PGO ✅ | Wire the PGO mechanism; auto-apply a profile when one is dropped in | the Go toolchain (`task pgo:collect`, `cmd/mecated/default.pgo`) | none — PGO is entirely OSS |
| 5 — Continuous profiling | Always-on queryable fleet profiles | Pyroscope / Parca + Grafana | *operational only* — see below; defer until `mecated`-as-a-service |
| 6 — Stable wall-clock gating | Make `ns/op` gating reliable in CI | self-hosted runner + `perflock` | the **hardware**, not the software |

### What OSS genuinely can't give us

Almost nothing here is a software-capability gap; everything that *catches a
regression* has a complete OSS path. The honest paid-only advantages are
operational:

1. **A stable bare-metal bench environment without owning/operating hardware.**
   (Bencher Cloud's signature feature: identical bare metal local + CI.) OSS gives
   `perflock` + a self-hosted runner, but we buy and babysit the box. The most
   material gap — and only relevant if `ns/op` gating becomes load-bearing, which our
   allocs/RSS/token KPIs are designed to avoid.
2. **Zero-ops hosting / retention / scaling for continuous profiling.**
   Pyroscope/Parca are fully capable self-hosted; Grafana Cloud Profiles / Polar
   Signals Cloud / Datadog just run the storage for you. Pure ops-offload, irrelevant
   at our scale.
3. **Polished managed regression analytics + PR UX** (Bencher Cloud / Datadog wrap
   change-point detection, alerting, dashboards as a product). OSS engines exist; we
   assemble them.
4. **Support / SLAs / SSO / multi-tenant team features.**

What money buys is **removing ops and hardware burden, not new abilities.**

### Suggested ordering (solo dev)

- **Do now** (free, offline, catches our real bugs): 0 → 2 → 1 → 3 (gh-pages route).
- **Done** (free win, mechanism wired): 4 (PGO — see [Phase 4 — Status](#phase-4--status)).
- **Defer until justified:** 5 (only when `mecated` is a real long-lived service),
  6 (only if `ns/op` gating becomes load-bearing).

### Roadmap close-out

Phases 0–4 deliver a **complete, free, OSS** story for both halves of the problem:
regression *tracking* (Phase 0 KPIs → Phase 1 micro-gating → Phase 2 offline
scenario harness → Phase 3 CI trend store + allocs-first gate) and one-time
build-time *optimization* (Phase 4 PGO mechanism). Every link that actually
*catches a regression* or *applies a free win* is shipped with no paid
infrastructure.

Phases 5 (continuous profiling) and 6 (bare-metal `ns/op` gating) remain
**intentionally deferred-until-justified** — and the reason is the same for both:
the gap is operational/hardware, not software. Phase 5 only earns its keep once
`mecated` is a real long-lived service worth always-on fleet profiles (Pyroscope/
Parca are fully capable self-hosted whenever that day comes). Phase 6 only earns its
keep if `ns/op` gating becomes load-bearing — which the allocs/RSS/token KPIs are
deliberately designed to avoid — and even then it is a runner-and-`perflock`
purchase, not a missing capability. Neither is a software hole in this roadmap; both
are a decision to spend ops/hardware budget that today's calibration does not
justify.

## Open decisions

Resolved in the Phase 2 build:

- **RSS sampling — RESOLVED: direct `/proc/self/status` read.** No new dependency;
  Linux-only (CI + dev are Linux), with a no-op off-Linux fallback so the harness
  still builds and runs everywhere (RSS just reports 0). Lives in
  [`perf/kpi/rss_linux.go`](../../perf/kpi/rss_linux.go) /
  [`rss_other.go`](../../perf/kpi/rss_other.go).
- **Scenario-harness home — RESOLVED: `perf/scenarios` + a `cmd/mecatui/ui` bench,
  `testing.B`-driven, no `cmd/` binary.** The scenarios are external-test
  Benchmarks under [`perf/scenarios/`](../../perf/scenarios) over a stdlib-only
  [`perf/kpi/`](../../perf/kpi) support package; the TUI render bench lives next to
  the code it measures as an internal `_test` file. No test-support under
  `internal/app` (it would entangle the scenarios with composition and break the
  "no `internal/...`" rule the offline harness keeps).
- **Trend store — RESOLVED: same `gh-pages` store, two metric families.** The
  scenario JSON ($MECATL_PERF_JSON) and the TUI render bench MERGE into one file,
  fed to the single `gh-pages` trend store as two families by the Phase 3 wiring.

Resolved in the Phase 3 build (see [Phase 3 — Status](#phase-3--status)):

- **Gate mechanism — RESOLVED: a SPLIT, not one tool.** A tested allocs gate
  ([`perf/cmd/allocsgate`](../../perf/cmd/allocsgate/main.go); deterministic
  epsilon ≥ 1 alloc AND > 2 %, FAIL-CLOSED on empty/corrupt input) over `task bench`
  PLUS `benchmark-action/github-action-benchmark` for the scenario KPIs + the
  `gh-pages` dashboard. allocs/op hard-gated; ns/op advisory. benchstat stays the
  LOCAL human A/B tool, not the CI gate decision.
- **Converter home — RESOLVED:
  [`perf/cmd/perfconvert`](../../perf/cmd/perfconvert/main.go).** A stdlib +
  `perf/kpi`-only `package main` that reshapes the scenario JSON into the two
  github-action-benchmark custom suites, hard-failing on a `schema_version`
  mismatch and aggregating samples by median.
- **Cache-hit whitelist — RESOLVED: an explicit allowlist
  `{single_session_long, team_fanout}`, not a `>0` heuristic** — so a regression to
  0 on a whitelisted scenario still fails the gate, while the by-design-0 scenarios
  (`compaction_cycle`, `tui_*`) emit no cache-hit point.
- **PR vs main — RESOLVED: PR fails-but-never-pushes; main pushes the store** (and
  re-stores `bench.txt` as the next baseline). `pull_request`, never
  `pull_request_target`; PR `contents: read`, main `contents: write`.
- **Per-metric thresholds — RESOLVED: ONE 102 % threshold on the unified
  smaller-suite** (so scenario allocs are gated at 2 %, safe because deterministic);
  splitting into a third suite is the escalation if a 2 % false positive ever fires.

Still open:

- Trend tooling: github-action-benchmark is in; revisit Bencher only when false
  positives from threshold gating become a real cost.


---

*Part of the [design docs](../design/README.md). Related: [Performance observability — problem, approaches, and the decided direction](0018-perf-observability.md), [Diagnostics, audit, and the global-slog ban](0020-diagnostics.md).*
