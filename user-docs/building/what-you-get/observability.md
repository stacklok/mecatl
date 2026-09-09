---
sidebar_position: 7
title: Observability & resilience
description: Monitor Mecatl runs with metrics, traces, diagnostics, and resilient model calls.
---

# Observability & resilience

Mecatl ships three distinct observability channels and a resilience decorator that wraps every LLM provider call. This doc describes what each one emits, what guarantees the LLM layer makes on failure, and which flags control the behavior.

---

## Overview

| Channel | What it carries |
|---|---|
| **Prometheus metrics** (`/metrics`) | Domain counters, gauges, and latency histograms — composition-dependent |
| **OTel traces** (OTLP) | Run/turn/tool spans — on when `--otlp-endpoint` is set |
| **Structured diagnostics** (stderr/log file) | Lifecycle and degraded-mode warnings from the harness |
| **Tool call audit** (`jsonlstore` sidecar) | One structured record per tool execution, with timing |

All four are injected at composition. Nothing reaches for a global logger or hardcoded sink.

---

## Prometheus metrics

The admin listener (`--metrics-addr`, default `127.0.0.1:9090` in `mecated`) serves `/metrics` via an OTel Prometheus exporter. It is **loopback-only and unauthenticated** — never bind it to a non-loopback address. `mecak8s` disables this endpoint by default; enable it explicitly with its metrics configuration when a local scrape endpoint is required.

Metrics are enabled when the deployment exposes the admin listener. They are not
part of the importable engine by themselves; embedders must wire their own
recorder and exporter.

### Series emitted

Engine- and run-derived series carry a bounded `role` label (`main`, `subagent`, `member`, `parallel`, `usermodel`, `child`) so you can split per-engine-family without free-text cardinality leaking session ids or model names. The target-free `mecatl_session_load_failures_total` counter is the deliberate exception: it is emitted by the service load boundary rather than an engine run and carries only `class`.

| Series | Type | Labels | What it measures |
|---|---|---|---|
| `mecatl_runs_total` | counter | `stop`, `role` | One per completed run, tagged by terminal stop reason |
| `mecatl_turns_total` | counter | `role` | One per completed LLM turn (every model exchange that reached a turn boundary) |
| `mecatl_turn_empty_total` | counter | `role` | Turns that produced neither a tool call nor text (the no-progress subset) |
| `mecatl_events_total` | counter | `type`, `role` | One per emitted session event, by event type |
| `mecatl_tool_calls_total` | counter | `tool`, `error`, `role` | One per tool execution |
| `mecatl_tool_duration_seconds` | histogram | `tool`, `role` | Tool execution latency — explicit-bucket histogram (millisecond resolution up to 300s), so `promtool`/a plain scrape gets usable p50/p90/p99 with no extra config |
| `mecatl_tokens_total` | counter | `kind`, `role` | Token consumption by kind (input, output, cache read, cache write) |
| `mecatl_cache_hit_ratio` | gauge | `role` | Ratio of cache-read tokens to total input tokens |
| `mecatl_active_runs` | gauge | `role` | Currently running Engine.Run goroutines |
| `mecatl_permission_asks_total` | counter | `role` | Permission pause events |
| `mecatl_session_load_failures_total` | counter | `class` | Ownership-concealed non-not-found session loads, classified as `store`, `snapshot`, or `unknown` |

`turn_empty_total` counts `EvNoProgress` emissions — the loop emits one per advisory nudge and once on give-up (up to `MaxNoProgressNudges + 1` per stuck sequence), so `turn_empty_total / turns_total` gives the empty-turn share, not a disjoint count.

The admin endpoint also serves runtime introspection paths:

| Path | What it returns |
|---|---|
| `/metrics` | Prometheus scrape endpoint |
| `/debug/pprof/*` | Go pprof handlers (heap, goroutine, allocs, cpu, mutex, block) |
| `/debug/vars` | Curated `runtime/metrics` snapshot |
| `/debug/flightrecorder` | Snapshot of the in-memory execution-trace ring (8 MiB / 5s window) |

The flight recorder is armed at startup when `--flight-recorder=true` (default). The mutex and block pprof profiles are off by default; enable them with `--mutex-profile-fraction` and `--block-profile-rate` only while investigating contention, as they carry runtime overhead.

### Embedded mecatui admin transport

`mecatui --perf` serves the same sensitive endpoints, but its default is an owner-private
per-instance UNIX `admin.sock` beside the embedded gRPC socket. Concurrent mecatui
instances therefore do not collide. `--perf-addr` explicitly selects TCP and accepts only
loopback addresses.

`--perf-mcp` with no explicit address uses ephemeral loopback TCP and logs the resolved
URL because the supported MCP transport is streaming HTTP. There is no stdio fallback.
Raw admin data remains an operator surface and is not injected into mecatui's session
debugger or any model context.

### Perf MCP server (opt-in)

`--perf-mcp` mounts a read-only MCP server at `/mcp` on the admin listener. It exposes the same runtime data as reduced numeric summaries (goroutine counts, latency percentiles, allocation rankings, slow-turn lists) so an agent can query performance state directly. Raw pprof blobs are offered as user-audience resource links, not injected into model context.

`--perf-mcp` is refused at startup if `--metrics-addr` is not loopback.

---

## OpenTelemetry traces

Tracing is enabled when `--otlp-endpoint` is non-empty. With an empty endpoint, the tracer is a no-op and only metrics run.

| Flag | Default | Description |
|---|---|---|
| `--otlp-endpoint` | `""` | OTLP collector endpoint. Empty disables tracing. |
| `--otlp-protocol` | `grpc` | Transport: `grpc` or `http`. |
| `--otlp-insecure` | `false` | Skip TLS — useful for a local collector. |

When tracing is active, Mecatl models a run/turn/tool span hierarchy. The `EventSink.Emit` call carries the run's context so telemetry can parent a run span to an inbound request span.

:::note[Concurrent-run span correlation]
The current span model has a single root per sink. Concurrent runs in the same process do not produce independently correlated spans — all runs on the same sink share the same root. This is a documented limitation; per-run correlation is the planned direction.
:::

---

## LLM resilience

Every LLM provider is wrapped by a resilience decorator (`llmresilience`) that sits between the agent loop and the provider. The loop is unchanged; the decorator adds retry, a circuit breaker, and a stream-idle watchdog.

### Retry with exponential backoff

Retries apply **only before the first committing chunk** (the first text, tool call, usage count, or done signal from the stream). Once streaming has begun, the decorator never re-issues the call.

| Flag | Default | Description |
|---|---|---|
| `--llm-max-attempts` | `3` | Total attempts (initial call plus retries). |

What triggers a retry: transient establishment failures — rate limits (429), server errors (5xx), timeouts, network errors, and a truncated or malformed first SSE frame (a decode error before any chunk has committed). Permanent client errors (4xx other than 408/429) and caller cancellations do not trigger retries and do not count toward the breaker.

After retries are exhausted the call surfaces as an `ExhaustedError`, which reaches the caller as a terminal `result` event.

### Circuit breaker

The breaker tracks consecutive transient establishment failures across attempts. On reaching the threshold it opens and short-circuits subsequent calls with a `BreakerError` until the cooldown period ends, at which point it half-opens to admit a trial.

| Flag | Default | Description |
|---|---|---|
| `--llm-breaker-threshold` | `5` | Consecutive transient failures that open the breaker (0 disables). |
| `--llm-breaker-cooldown` | `30s` | Duration the breaker stays open before admitting a half-open trial. |

A successful call resets the consecutive-failure counter. Permanent errors and cancellations are not counted.

### Per-attempt timeout

`--llm-per-attempt-timeout` bounds **only establishment** — from the call start to the first committing chunk. It is enforced by a separate timer that is stopped as soon as the first committing chunk arrives, so it never cuts an actively-streaming turn.

| Flag | Default | Description |
|---|---|---|
| `--llm-per-attempt-timeout` | `300s` | Establishment bound: connect + first committing chunk. 0 disables. |

A timeout here is retryable (it counts as a transient failure toward the breaker). The default is deliberately generous to accommodate reasoning models that have long thinking phases before their first output token. If your model is reliably fast to first token you can lower this value.

### Stream-idle watchdog

The idle watchdog applies **after the first chunk** and governs the rest of the stream. It caps the gap between consecutive chunks. A stall beyond this limit is **terminal and not retried** — replaying a half-streamed turn to the model is unsafe.

| Flag | Default | Description |
|---|---|---|
| `--llm-stream-idle-timeout` | `180s` | Max idle gap between stream chunks after the first chunk. 0 disables. |

When the watchdog fires it synthesizes a terminal `StreamIdleError`. The LLM provider adapters deliberately suppress the context cancellation error on cancel and would otherwise yield nothing; the wrapper synthesizes the error explicitly so the caller always sees a clean terminal signal.

Pre-first-chunk stalls (before any chunk is received) are governed by `--llm-per-attempt-timeout` and are retryable.

### Provider-side prompt caching

Caching is ON by default across all three provider adapters (Anthropic, OpenAI/OpenRouter, and the dormant openaichat path) — see [ADR 0100](https://github.com/stacklok/mecatl/blob/main/docs/adr/0100-provider-prompt-caching.md). It caches the growing conversation, not just the system prompt.

| Flag | Default | Description |
|---|---|---|
| `--no-prompt-cache` | `false` | Disable caching entirely: every adapter's cache dialect degrades to `None`, reproducing the pre-caching wire exactly. |
| `--anthropic-cache-ttl` | `""` (API default, `5m`) | TTL stamped on every Anthropic ephemeral `cache_control` breakpoint. Accepts `5m` or `1h`; any other value is ignored with a WARN. |

The token-accounting facets already surface cache activity per-turn: `mecatl_tokens_total{kind="cache read"}` / `{kind="cache write"}` and `mecatl_cache_hit_ratio` (both above) climb once caching is actually hitting. On OpenAI/OpenRouter, cache-write tokens are probed from the raw usage JSON (there is no typed SDK field for them yet) and clamped so they never exceed the turn's input tokens.

### Resilience diagnostics

The resilience decorator emits structured diagnostics through the injected diagnostics channel. Failed-attempt decision lines carry session/run/turn correlation when a model call belongs to a run:

- **DEBUG** on each retry and per-attempt-timeout event
- **INFO** on idle-stall terminal, breaker open/half-open/close transitions, and retry exhaustion

Decision metadata never includes a raw error. The same sanitized classification feeds a
log-only `network.attempt` event emitted by the agent loop and persisted by the ordinary
EventLog relay. A dedicated session debugger can read this target-correlated evidence through
`InspectSession {"view":"network"}`; it includes bounded retry/terminal decisions and safe
transport/provider classifications, not request/response content or credentials. The same
single tool exposes `related`, `delegation`, `history`, and `manifest`: related retained
sessions are addressed only with target-bound opaque handles, compaction archives remain
pageable, team/task/finding and parent-result facts come only from typed events, and request
manifests contain tool decisions and digests but no prompt bodies. Every view reports incomplete
scan/projection/retention honestly; snapshot latest-run counters are distinct from EventLog
lifetime totals. No session
ID is added to metric labels.

---

## Structured diagnostics

The harness routes operational logging through an injected `Diagnostics` port rather than a global logger. The `slogdiag` adapter is the only bridge to `log/slog`. The sink is chosen at composition:

- **mecated**: stderr (or journald, depending on environment)
- **mecatui**: `$XDG_STATE_HOME/mecatl/mecatui.log` when hosting an embedded server; `io.Discard` otherwise (the TUI owns the alt-screen)

### What the agent loop logs

The loop sends a small set of operator-only facts through diagnostics, including
compaction failures, policy or authority outcomes, persistence failures, delivery
queue problems, instruction-fragment assembly failures, observer/profile refresh
failures, and ask-ID fallback warnings. The exact set can grow when a fact has no
corresponding `session.Event`; the invariant is that events own session facts and
are not duplicated as log lines. Build-time composition facts are logged once by
`app.Build`, while per-run diagnostics are session-correlated.

Under caller-ownership enforcement, a storage or snapshot failure while loading a
session is intentionally reported to the caller as the same NotFound outcome as a
missing or foreign session. Operators receive one `session load failed` WARN carrying
only `class=store|snapshot|unknown` and `ownership=enforced`; the companion counter is
`mecatl_session_load_failures_total`. Neither signal includes the requested session,
principal, storage key or path, raw error, snapshot content, or snapshot size. Genuine
missing sessions and foreign-owner concealment remain silent.

Use the bounded class to choose a target-free response:

| Class | Meaning | Safe operator action |
|---|---|---|
| `store` | The store could not retrieve the snapshot, including transport failures. | Check backend health, connectivity, credentials, TLS, and timeouts; use backend-wide health signals rather than asking for or logging the requested session ID. |
| `snapshot` | Bytes were retrieved but the snapshot format, decoding, persisted identity, or validation failed. | Check storage-integrity and mis-keying alerts, then follow the backend's documented backup or repair procedure without copying snapshot contents into logs. |
| `unknown` | A custom store returned an untyped failure that Mecatl cannot classify safely. | Check the custom adapter's bounded health diagnostics and update it to wrap failures with the public `engine/port` classification contract; do not infer a class from error text. |

The counter identifies a failure family, not a target. It deliberately cannot answer
which session was requested; do not weaken ownership concealment to obtain that detail.

`provider.route` is an event-stream fact rather than a diagnostic. When the
serving provider is OpenRouter, it reports the downstream inference provider
selected for a turn; it may be absent on a cache hit. Configure downstream routing
in the operator-tier OpenRouter settings. See [Route OpenRouter models through
preferred downstreams](/features/choose-models.md#route-openrouter-models-through-preferred-downstreams).

## Tool call audit

`port.ToolCallRecorder` captures one structured record per tool execution with timing (queue wait + execution duration). It is a separate channel from diagnostics and the event stream.

The `jsonlstore` backend (selected with `--store-dir`) implements `ToolCallRecorder` alongside `SessionStore` and `EventLog`. It writes tool records to a `.tools.jsonl` sidecar in the session's family directory under `--store-dir` (the filename is derived from the session id but is not reversible — see [Session store](/building/extension-points/session-store.md)). The `telemetry` adapter additionally derives counters and a latency histogram from these records — those feed into `mecatl_tool_calls_total` and `mecatl_tool_duration_seconds` on the `/metrics` endpoint.

---

## What's next

To configure Mecatl for production, see the deployment guide for how to wire an OTLP collector, configure the admin listener, and set up session persistence with `jsonlstore`.
