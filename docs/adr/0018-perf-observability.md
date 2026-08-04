# ADR 0018 — Performance observability

- Status: Accepted
- Date: 2026-06-03
- Scope: how mecatl exposes its own runtime performance for measurement — by humans, by tooling, and by an AI agent over MCP
- Superseded by: [ADR 0045](./0045-explicit-bucket-latency-histograms.md) — the histogram-aggregation choice (§5 decision 2) ONLY; the phased hybrid, the perf-MCP server, and the loopback posture are unchanged

## Context

mecatl had no runtime visibility: no goroutine count, no GC metrics, no process RSS, and no pprof handlers. The dominant cost is off-CPU (model + tool I/O), so CPU profiling alone was insufficient. A proposal surfaced to expose perf data to an AI agent via an MCP server so the agent could query p99 latency, goroutine counts, and top allocation sites as ranked numeric summaries rather than raw blobs.

## Decision

Adopt a phased hybrid (Approach D). Phase 1 lands stdlib foundations — pprof on the loopback admin mux, Go and Process collectors on the Prometheus registry, FlightRecorder, goleak in concurrency-heavy tests, and a process-RSS gauge. Phase 2 layers an opt-in perf-over-MCP server reading those same Phase 1 sources. Phase 3 (fleet OTel + continuous profiling) is deferred until mecated runs as a real long-lived service. No external profiling infrastructure is stood up; profiling is delivered in-process via the MCP server.

## Consequences

Phase 1 closes the embarrassing gaps (no goroutine count, no RSS instrument) and every later phase reuses those same sources. The MCP Go SDK was already a direct dependency, so Phase 2 adds no new SDK. The loopback-only enforcement is fail-closed: enabling the perf MCP on a non-loopback address refuses to start. Phase 3 remains intentionally deferred; current behaviour and shipped/deferred state are tracked in docs/architecture.md and the production readiness tracker respectively.

---
- Companion: the [Go-perf measurement survey](../perf-measurement-survey.md) (the
  Go-perf technique survey and the agent-consumable decision table). This doc
  grounds that survey in mecatl and lays out concrete approaches with trade-offs.
  See also the sibling [long-term performance regression tracking](0019-perf-tracking.md),
  which covers the *regression-gating* half (baselines, the CI trend store, PGO).

> This follows the `docs/design/` convention: rationale + options + a
> recommendation framed as a proposal, citing real package paths. It writes **no
> production Go**. The recommendation is a phased hybrid, but every approach is
> presented on its own merits so the choice stays open.

---

## 1. Problem statement — what actually goes slow in mecatl

mecatl is a **streaming agentic loop** (`engine/agent`, see
`docs/architecture/agent-loop.md`). Its performance profile is dominated by *off-CPU*
time — waiting on the model and on tool I/O — which means the naive "run a CPU
profile" instinct measures the wrong thing. The concerns that are real here, tied
to the code paths that cause them:

| Concern | Where it lives | Why it bites |
|---|---|---|
| **TTFT vs inter-token latency** | `runTurn` consuming `LLM.Stream` chunks (`engine/agent/loop.go`); OpenAI SSE→Chunk (`provider/openai/stream.go`) | Users feel time-to-first-token and *jitter* between tokens, not mean latency. One number hides both. |
| **Dispatch lock contention / mutate-serial queueing** | `Engine.dispatch` read-parallel/mutate-serial (`engine/agent/dispatch.go`); results merged under a mutex | A slow `Edit` serially blocks every queued mutation — an internal **coordinated-omission** source (survey §12). Mean tool latency won't show the queueing. |
| **Goroutine leaks** | per-run background goroutine (`Engine.Run` → `drive`), the SSE consumer + its cancel path, subagent/parallel drain loops (`subagent.go`, the Parallel tool in `parallel.go`), the server Run registry (`internal/adapter/server/service.go`) | Each run spins goroutines; a cancellation path that doesn't unwind leaks them across a long-lived `mecated`. |
| **GC pressure / allocation churn** | chunk decoding, event fan-out (`Run.emit`), prompt assembly, the compaction cascade (`engine/agent/cascade.go`) | High alloc/op on the hot streaming path drives GC pauses that show up as inter-token jitter. |
| **Long-session memory growth** | conversation history before compaction; the jsonl store; historically the tree-sitter WASM leak (`adapter/repomap`, now **removed** — see `docs/adr/0029-repomap-tree-sitter.md`) | Memory climbs over a long session. The since-removed WASM leak (~23 MB RSS per call) was **off the Go heap** — invisible to `pprof heap` and `runtime/metrics`; only process RSS saw it (survey §9). The off-heap-growth signature still applies to any future off-heap consumer. |
| **TUI render cadence** | `cmd/mecatui/ui` coalescing streamed deltas to frame cadence | Render must keep up with inter-token rate without scrambling markdown; a starved render goroutine is the symptom the (now-removed) WASM hang already produced once. The measured idle-churn source was the footer spinner's self-perpetuating tick chain (a 10fps full-screen Update→View re-render forever: ~12.6% CPU, ~3.7MB/s alloc, GC every ~3.5s idle) — now phase-gated (`spinnerVisible`, dropped outside running/connecting, re-armed on every transition in). |
| **Tail latency** | run/turn timing across all the above | p99 turn latency is the SLO that matters; averages lie. |

What mecatl **already has** (verified against `internal/adapter/telemetry/` and
`docs/architecture/observability.md`):

- **Prometheus `/metrics`** on a separate loopback listener (`--metrics-addr
  127.0.0.1:9090`), exposing **domain-derived** series only: `mecatl_events_total`,
  `mecatl_runs_total`, `mecatl_tool_calls_total`, `mecatl_tool_duration_seconds`
  (histogram), `mecatl_tokens_total`, `mecatl_cache_hit_ratio`,
  `mecatl_active_runs`, `mecatl_permission_asks_total` (`internal/adapter/telemetry/metrics.go`).
  Attribute set (all bounded — never a session id, def name, or free text):
  `type` (event type), `stop` (stop reason), `tool` (tool name), `error`
  ("true"/"false"), `kind` (token kind), and — since issue #47 — `role`, the
  CLOSED engine role family `main|subagent|member|parallel|usermodel|child`
  carried by EVERY series (main engine = `role="main"`; child engines are tagged
  via `telemetry.Metrics.WithRole` behind `app.Config.MetricsRoleScoper`, with
  internal/app's `roleFamily` as the cardinality choke point).
- **OTel traces** — run/turn/tool spans over OTLP (`internal/adapter/telemetry/tracing.go`,
  `telemetry.Setup`), with the documented single-root-per-sink limitation
  (no per-concurrent-run correlation because `EventSink.Emit` has no `ctx`).
- Per-tool **`Logger`** timing and a jsonl replay log (`jsonlstore`).

What it **does not** have (the gaps every approach below must reckon with):

- **No `runtime` visibility at all** — no Go collector, no Process collector on
  the Prometheus registry. So **goroutine count, GC, heap, and process RSS are
  invisible today**. (`prometheus/.../collectors` is vendored; this is a wiring
  gap, not a dependency gap.)
- **No pprof** handlers anywhere.
- **No `expvar`, no `runtime/metrics` snapshot, no `runtime/trace`/FlightRecorder.**
- **No OTel metrics** (traces only).
- **No goroutine-leak gate** in tests (`go.uber.org/goleak` is in go.sum,
  transitively, but unused).
- **No process-RSS reading** — so the one leak we knew about at the time
  (the since-removed tree-sitter WASM tool) had no instrument that could see it.

The corrected record matters: a prior research note asserted mecatl "already has
OTel metrics." It does **not** — it has OTel *traces*. Metrics are Prometheus-only
and domain-only.

---

## 2. The MCP-exposure idea (the user's proposal)

The proposal: expose perf data to an **AI agent** via an MCP server, so the agent
driving (or debugging) mecatl can ask "what's the p99 turn latency / are
goroutines leaking / what are the top allocation sites" and get **numbers it can
reason over** — not a flamegraph it can't read. This is approach **B** below; the
design detail lives here because it is the same regardless of which approach wins.

### 2.1 Resources vs Tools — the control split

MCP has three primitives by *who controls invocation*: **Tools** (model-controlled),
**Resources** (app-controlled, addressable read-only state), **Prompts**
(user-controlled). MCP is a UI for an LLM, not a REST wrapper. The split for perf
data:

| Primitive | Use for | Examples |
|---|---|---|
| **Resources** | cheap, addressable, point-in-time state | `perf://runtime/summary` (goroutines, heap, GC pause, RSS), `perf://runtime/memstats`, `perf://metrics/prometheus` (curated subset) |
| **Resource templates** (RFC 6570) | parameterised reads | `perf://pprof/{profile}` where `{profile}` ∈ `heap\|goroutine\|allocs\|mutex\|block` |
| **Tools** | expensive / parameterised / perturbing / computed | `query_metric`, `capture_cpu_profile` (MUST be a tool — it perturbs), `top_allocations`, `top_cpu_functions`, `list_slow_turns` (mecatl-specific) |
| **Prompts** (optional) | a user-invoked workflow | `diagnose_perf_regression` |

### 2.2 The dominant constraint: output token budget

The single rule that shapes every tool: **never return a raw artifact.** A pprof
blob, a 5 MB `/metrics` dump, or a full goroutine dump will blow the model's
context and leak secrets. The server **reduces server-side** and returns ranked
numeric summaries:

- `capture_cpu_profile` → `{duration, sample_count, top:[{function, flat_pct, cum_pct}]}`
  (parsed with `google/pprof/profile`, already vendored — survey §1).
- `top_allocations` → `{top:[{function, alloc_bytes, alloc_objects, in_use_bytes}], total_heap_bytes}`.
- `query_metric` → a scalar, or a series capped at ≤20 points, or a requested
  quantile; **omitting the name lists available metric names** (discovery).
- `list_slow_turns` → `{turns:[...], nextCursor}` with opaque-cursor pagination >20.
- Human-only blobs (a raw profile worth downloading) go out as a **resource_link
  annotated `audience:["user"]`** so they never enter model context.

### 2.3 Transport, SDK, and where it mounts

- **Streamable HTTP only.** Single `/mcp` endpoint (POST+GET), `Mcp-Session-Id`
  for session management, SSE for progress on the one slow tool. This respects the
  project's **hard no-stdio / no-`os/exec`-MCP rule** by construction — a
  `StreamableHTTPHandler` is a pure in-process `http.Handler`.
- **SDK:** `github.com/modelcontextprotocol/go-sdk` — **already a direct
  dependency** (`v1.6.1`, Apache-2.0; the MCP *client* adapter uses its
  `StreamableClientTransport`, `internal/adapter/mcp/mcp.go`). So approach B adds
  **no new SDK dependency** — a material correction to the "build cost" framing:
  the server handler (`mcp.NewStreamableHTTPHandler`) lives in the same SDK
  already in the tree.
- **Mounts** as one more `http.Handler` on the loopback admin mux — the same
  `--metrics-addr` listener that serves `/metrics` today (`cmd/mecated/main.go`,
  `serve`). gRPC stays on its own HTTP/2 listener, untouched.

### 2.4 Minimal loopback security (OAuth is overkill)

The admin mux is single-user loopback. The proportionate posture:

1. **Bind `127.0.0.1` only** (already the listener's default).
2. **Origin / DNS-rebinding protection** — keep the SDK's localhost protection
   default **on**.
3. **Static bearer token** — random at startup or from config/env; `401` +
   `WWW-Authenticate` via middleware. **OAuth 2.1 is overkill for single-user
   loopback** and is reserved for a future off-loopback "scary flag."
4. **`readOnlyHint: true` on every tool** — there are no mutating perf tools.
5. **Rate-limit `capture_cpu_profile`** — bounded duration (1–30 s), one-at-a-time
   + cooldown, surface "retry in Xs" as an `isError` data result.
6. **Output redaction** — goroutine dumps, stacks, and pprof labels can contain
   args/paths/tokens/prompt text. Strip locals, redact args, never echo env vars,
   curate metric cardinality. Never log the bearer token.

### 2.5 Layering fit

A new **edge adapter** `internal/adapter/mcpperf` imports the SDK + the perf
sources; **domain / port / agent never import it** (the inward-only rule,
`docs/architecture.md`). Wiring is confined to `internal/app` (behind a flag,
`app.Build`) and `cmd/mecated` (owns the flag + token, mounts on the loopback
mux). This mirrors exactly how the MCP *client* and skills adapters sit.
A `mecated perf-mcp print-config` helper (mirroring the `skills promote`
subcommand) would print a paste-ready client snippet:

```jsonc
// .mcp.json — Streamable HTTP needs only a URL + header
{ "mcpServers": { "mecatl-perf": {
    "type": "http",
    "url": "http://127.0.0.1:9090/mcp",
    "headers": { "Authorization": "Bearer ${MECATL_PERF_TOKEN}" } } } }
```

---

## 3. The approaches (the heart of this doc)

Four distinct, comparable approaches. The project's dependency preference is
**stdlib > CNCF/Apache-2.0 > permissive-MIT > commercial**.

### Approach A — Stdlib endpoints only (pprof + Go collector + expvar)

**What it delivers.** pprof handlers on the loopback admin mux (+ the mutex/block
knobs); register `collectors.NewGoCollector(WithGoCollectorRuntimeMetrics)` and
`collectors.NewProcessCollector` on the existing Prometheus registry (goroutines,
GC, heap, **RSS** at `/metrics`); an `expvar` `/debug/vars` page with curated
`runtime/metrics` values. Standard, human- and tooling-consumable.

- **Effort:** low (days). Mostly wiring onto surfaces that already exist.
- **Dependencies:** **none new** — all stdlib + already-vendored `collectors` +
  `google/pprof/profile`. **Best** governance tier.
- **Security:** the loopback `/metrics` listener has **no auth today**; adding
  pprof there widens a secret-bearing surface (profiles can carry prompt text),
  so this approach should *also* gate the admin mux behind the bearer token.
- **Layering:** none — extends `cmd/mecated` + the telemetry registry only.
- **Concerns addressed:** goroutine leaks (count + profile), GC pressure, alloc
  churn (pprof), **process RSS incl. the WASM leak** (Process collector). **Not**
  addressed: TTFT/inter-token split, dispatch queueing tail latency, AI-native
  consumption (a human reads pprof; an agent gets the parsed rows only if someone
  builds B).

### Approach B — Perf-over-MCP (the user's idea)

**What it delivers.** Everything in §2 — an **AI-native, pluggable, opt-in**
surface (`--perf-mcp`, off by default) that returns ranked numbers an agent can
reason over. Sits on the same loopback mux.

- **Effort:** medium (1–2 weeks) — tool/resource design, the reducers
  (profile→rows, metric→series), security middleware, tests.
- **Dependencies:** **no new SDK** (`modelcontextprotocol/go-sdk` is already
  direct). Apache-2.0. Pulls in `google/pprof/profile` for the reducers (already
  vendored).
- **Security:** the §2.4 posture — purpose-built, the strongest of the four
  because it is read-only-by-construction with explicit redaction.
- **Layering:** new edge adapter `internal/adapter/mcpperf`, wired only in
  `app`/`cmd` — clean fit.
- **Concerns addressed:** *all* of them, including the mecatl-specific
  `list_slow_turns` / TTFT split — **but only as well as the underlying sources
  expose them.** B reading thin sources gives thin answers; this is why B is most
  valuable *on top of* A's foundations, not instead of them.

### Approach C — OTel metrics + continuous profiling backend

**What it delivers.** Add **OTel metrics** (exponential histograms + exemplars
metric→trace; `contrib/instrumentation/runtime` + host collectors) alongside the
existing OTel traces, and stand up **Pyroscope or Parca** for always-on
fleet profiling and retrospective analysis.

- **Effort:** medium-high, and **needs external infra** (a collector + a
  profiling backend + dashboards).
- **Dependencies:** OTel metrics SDK + contrib (Apache-2.0); Pyroscope/Parca
  (Apache-2.0). Preferred third-party tier; **Datadog explicitly avoided.**
- **Security:** push-based to a collector you operate; no new local surface.
- **Layering:** OTel metrics extend the `telemetry` adapter (and want the
  ctx-aware sink seam to correlate concurrent runs — currently a documented gap).
- **Concerns addressed:** fleet-grade tail latency (native histograms), GC, leak
  trends over time, continuous CPU/heap retrospect. **Best for production
  retrospect across many mecated instances.** Overkill for local single-binary
  dev; doesn't itself give an AI agent an in-process query surface.

### Approach D — Phased hybrid

**Phase 1 — stdlib foundations (= Approach A).** pprof on the loopback admin mux +
the two knobs; Go + Process collectors on the registry; `runtime/metrics`/`expvar`
snapshot; **FlightRecorder** armed at startup; **`goleak.VerifyTestMain`** in the
stream / subagent / fork / server tests; **read `/proc smaps_rollup`/VmRSS** for
the WASM leak. Every later phase needs these sources anyway. Lowest cost, no new
deps, immediate value.

**Phase 2 — Perf-over-MCP (= Approach B) reading the Phase-1 sources.** The MCP
server's reducers read the *same* runtime/metrics, pprof, and FlightRecorder
sources Phase 1 exposed — so B becomes "an AI-native projection of foundations
that already exist," which is far cheaper and richer than B standing alone.

**Phase 3 (optional) — fleet (= Approach C).** OTel metrics + continuous
profiling when/if there's a fleet to observe.

- **Effort:** incremental — ship value at each phase, stop whenever.
- **Dependencies:** Phase 1 none-new; Phase 2 none-new; Phase 3 the OTel/profiling
  tier.
- **Concerns addressed:** all of them, in priority order, without committing to
  infra before it's warranted.

### Side-by-side

| | A. stdlib | B. MCP | C. OTel+CP | D. phased hybrid |
|---|---|---|---|---|
| AI-native (agent-consumable directly) | no | **yes** | partial (backend API) | **yes (phase 2)** |
| Human-consumable | **yes** | via summaries | **yes (dashboards)** | **yes** |
| New dependencies | **none** | none (SDK already in tree) | OTel metrics + Pyroscope/Parca | phased |
| External infra needed | no | no | **yes** | only at phase 3 |
| Sees the WASM RSS leak | **yes** (Process collector + /proc) | yes (if it reads RSS) | yes (host metrics / parca-agent) | **yes** |
| TTFT/inter-token + dispatch tail | partial | **yes** (domain tools) | yes (native histograms) | **yes** |
| Effort | low | medium | medium-high | incremental |
| Governance tier | **best (stdlib)** | best+preferred | preferred | best→preferred |
| Layering impact | minimal | clean edge adapter | extends telemetry | clean, staged |

---

## 4. Decision

**Approach D (phased hybrid).** Phase 1 lands the stdlib foundations (pprof, the
runtime collectors, FlightRecorder, goleak, RSS); Phase 2 layers the opt-in
perf-over-MCP server on top, reading those same sources; the companion skill
makes the output actionable. **There is no external-infra phase** — see decision
§5.4: profiling is delivered *in-process via the MCP server* (on-demand pprof +
FlightRecorder snapshots), so no Pyroscope/Parca backend is stood up.

Rationale: Phase 1 closes the embarrassing gaps first (no goroutine count, no RSS,
no leak instrument) and every later piece reads those same sources. The MCP server
is an AI-native projection of numbers that already exist, not a parallel stack.
The MCP Go SDK is **already a direct dependency** (`v1.6.1`), so Phase 2 adds no
new SDK. The user's stance: **functionality over dependency-weight** — the
governance-tier caution that argued for cautious staging is relaxed, so Phases 1
and 2 are both committed (not "maybe later").

---

## 5. Decisions (settled in discussion, 2026-06-03)

1. **Metrics pipeline — migrate fully to OTel metrics.** Move all domain metrics
   (`mecatl_events_total`, `…_runs_total`,
   `…_tool_calls_total`, `…_tool_duration_seconds`, `…_tokens_total`,
   `…_cache_hit_ratio`, `…_active_runs`, `…_permission_asks_total`) to the OTel
   metrics SDK, and expose them for scrape via the OTel **prometheus exporter** so
   the existing `/metrics` contract survives (`client_golang` is demoted to the
   exporter host — the registry + `promhttp` serving — and defines no domain
   instrument, but is not dropped). Unifies with the existing OTel
   traces and unlocks **exemplars (metric→trace)**. Add the
   `contrib/instrumentation/runtime` + host collectors for the runtime picture.
2. **Histograms — exponential / native.** Use exponential (OTel) / native
   (Prometheus-exposition) histograms for turn time, TTFT, inter-token, and tool
   duration. Accurate tails across a wide dynamic range, fleet-aggregatable.
   *Wiring note (issue #53):* these instruments read off the loop's injected
   `port.Clock`; composition injects the production clock
   (`engine/adapter/wallclock`) in `engineDepsForProvider`. Before issue #53 the
   field was never injected (nil Clock), so every observation — turn duration,
   TTFT, inter-token, tool queued/took — was silently zero.
3. **EventSink seam — fix now.** Add a `ctx`-aware emit (`Emit(ctx, ev)` variant /
   per-run sink) so concurrent runs get correctly correlated spans and accurate
   per-run tail-latency attribution. This is load-bearing for the whole effort and
   is done as part of Phase 1, not deferred. Touches `engine/port` + implementers.
4. **No external profiling infra — profile in-process via MCP.** Pyroscope/Parca
   are **out of scope**. pprof is in-process and needs no backend; the MCP server
   (Phase 2) is the delivery mechanism — on-demand `capture_cpu_profile`, heap,
   and **FlightRecorder** snapshots, reduced server-side. (This was the user's
   originating insight: scrape live profiling data through the MCP server.)
5. **MCP output — summaries + opt-in raw blob.** Tools return ranked numeric
   summaries by default; a full pprof/trace blob is *also* offered as a
   **user-audience `resource_link`** (downloadable out-of-band, never auto-fed to
   the model). The companion skill (§8) parses the raw blob when deep analysis is
   wanted.
6. **Auth — loopback, no auth (for now).** Rely on **`127.0.0.1` binding +
   Origin/DNS-rebinding protection** only; no bearer token in v1. *Caveat on
   record:* pprof profiles and goroutine dumps can carry prompt text / paths, so
   any future off-loopback exposure MUST add auth (and the §2.4 redaction still
   applies to summaries regardless). The embedded mecatui server uses its UNIX
   socket's filesystem perms.
   - **TRIPWIRE (enforced as of the Phase-2 wiring, addressing the security
     review's CWE-306 Low):** loopback-only is no longer documentation-only — it
     is **enforced at the composition root, fail-closed**. `--perf-mcp` on a
     **non-loopback** `--metrics-addr` (mecated) or a non-loopback `--perf-addr`
     (mecatui embed) **refuses to start** (`bind loopback or add auth (future
     work)`). The check runs before any listener/recorder side effect. If a
     future change deliberately serves `/mcp` off loopback, it **MUST** add auth
     first (a bearer token plumbed through the admin mux, the §2.4 "scary flag")
     — relaxing the tripwire without adding auth re-opens the unauthenticated
     runtime-data exposure.
7. **Scope — both `mecated` and the `mecatui` embedded server.** Perf
   observability covers the embedded in-process server too (the WASM hang that
   motivated this was a mecatui freeze), exposed over its loopback/socket surface.
   - **Admin-port default — fixed `127.0.0.1:9099`, fail-on-clash (revised).** The
     embedded `mecatui` perf admin listener originally defaulted to an **ephemeral**
     `127.0.0.1:0` so it could never collide with a co-running `mecated` (`:9090`).
     That made the `/mcp` URL unknowable without scraping logs and changed every
     restart, so an MCP-client config could never hardcode it. **Revised:** the
     default is now a **fixed, predictable** `127.0.0.1:9099` (distinct from
     `mecated`'s `9090`, so the two still don't collide). On a port clash at
     startup, `embed.Start` **fails with guidance** pointing at `--perf-addr` —
     **no silent ephemeral fallback**. The ephemeral behaviour remains an explicit
     opt-in via `--perf-addr 127.0.0.1:0` (and any other `host:port` is accepted).
8. **Companion skill — build it.** Author a Claude skill that teaches an agent to
   read mecatl's perf MCP output (pprof rankings, `runtime/metrics`,
   FlightRecorder summaries; leak/contention/GC-pressure signatures). Built with
   `/skill-write`; the MCP server itself is built with `/mcp-server-authoring`.
9. **WASM leak — resolved by removal.** Tree-sitter / RepoMap has been **removed**
   entirely (see `docs/adr/0029-repomap-tree-sitter.md`), so the specific ~23 MB-RSS
   leak no longer exists in the tree. A **process-RSS gauge** still ships (Phase 1)
   for general long-session memory visibility (history, jsonl store), but no
   leak-specific alarm and no Option-E rework in this effort.
10. **goleak — broad + live alarm.** `goleak.VerifyTestMain` across the
    concurrency-heavy packages (`agent`, `server`, subagent/fork, openai stream)
    **and** a live goroutine-count gauge/alarm in `mecated` — not just a test gate.
</content>


---

*Part of the [design docs](../design/README.md). Related: [Long-term performance & resource regression tracking](0019-perf-tracking.md), [Diagnostics, audit, and the global-slog ban](0020-diagnostics.md).*
