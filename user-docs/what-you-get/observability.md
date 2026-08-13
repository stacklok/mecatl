---
sidebar_position: 7
title: Observability & resilience
---

# Observability & resilience

mecatl ships three distinct observability channels and a resilience decorator that wraps every LLM provider call. This doc describes what each one emits, what guarantees the LLM layer makes on failure, and which flags control the behavior.

---

## Overview

| Channel | What it carries |
|---|---|
| **Prometheus metrics** (`/metrics`) | Domain counters, gauges, and latency histograms — always on |
| **OTel traces** (OTLP) | Run/turn/tool spans — on when `--otlp-endpoint` is set |
| **Structured diagnostics** (stderr/log file) | Lifecycle and degraded-mode warnings from the harness |
| **Tool call audit** (`jsonlstore` sidecar) | One structured record per tool execution, with timing |

All four are injected at composition. Nothing reaches for a global logger or hardcoded sink.

---

## Prometheus metrics

The admin listener (`--metrics-addr`, default `127.0.0.1:9090`) serves `/metrics` via an OTel Prometheus exporter. It is **loopback-only and unauthenticated** — never bind it to a non-loopback address.

Metrics are always on. No flag is needed to enable them.

### Series emitted

All series carry a bounded `role` label (`main`, `subagent`, `member`, `parallel`, `usermodel`, `child`) so you can split per-engine-family without free-text cardinality leaking session ids or model names.

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

`turn_empty_total` counts `EvNoProgress` emissions — the loop emits one per advisory nudge and once on give-up (up to `MaxNoProgressNudges + 1` per stuck sequence), so `turn_empty_total / turns_total` gives the empty-turn share, not a disjoint count.

The admin endpoint also serves runtime introspection paths:

| Path | What it returns |
|---|---|
| `/metrics` | Prometheus scrape endpoint |
| `/debug/pprof/*` | Go pprof handlers (heap, goroutine, allocs, cpu, mutex, block) |
| `/debug/vars` | Curated `runtime/metrics` snapshot |
| `/debug/flightrecorder` | Snapshot of the in-memory execution-trace ring (8 MiB / 5s window) |

The flight recorder is armed at startup when `--flight-recorder=true` (default). The mutex and block pprof profiles are off by default; enable them with `--mutex-profile-fraction` and `--block-profile-rate` only while investigating contention, as they carry runtime overhead.

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

When tracing is active, mecatl models a run/turn/tool span hierarchy. The `EventSink.Emit` call carries the run's context so telemetry can parent a run span to an inbound request span.

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

The resilience decorator emits structured diagnostics through the injected diagnostics channel. These are provider-level lifecycle lines, not session-correlated:

- **DEBUG** on each retry and per-attempt-timeout event
- **INFO** on idle-stall terminal, breaker open/half-open/close transitions, and retry exhaustion

Every line is metadata-only. The decorator sees only the request metadata and errors, not prompt text. Error strings are clamped.

---

## Structured diagnostics

The harness routes operational logging through an injected `Diagnostics` port rather than a global logger. The `slogdiag` adapter is the only bridge to `log/slog`. The sink is chosen at composition:

- **mecated**: stderr (or journald, depending on environment)
- **mecatui**: `$XDG_STATE_HOME/mecatl/mecatui.log` when hosting an embedded server; `io.Discard` otherwise (the TUI owns the alt-screen)

### What the agent loop logs

The agent loop emits exactly three run-scoped diagnostic lines:

1. **Compaction failure** (Warn) — when conversation compaction fails and the run continues uncompacted.
2. **Policy deny** (Info, keys: `tool`, `reason`) — when the permission policy resolves a tool call to Deny.
3. **Background drain abandon** (Warn, keys: `ids`) — at run end, if a background subagent is still alive after the two-phase cancel+join and must be abandoned. Rare by construction.

Everything else is carried by the event stream (`session.Event` taxonomy): cancellation, tool errors, compaction success, permission asks and allows. Those are not duplicated in diagnostics.

One event worth calling out for OpenRouter users: `provider.route`. When the serving provider is `openrouter`, each turn reports which **downstream** inference provider OpenRouter actually routed to (mecatl's "provider" stays the wire adapter). It rides the event stream as a `provider.route` event — rendered in mecatui as a transient `via <slug>` footer status — and is absent on a cache hit (OpenRouter strips the routing metadata from cached responses). You can steer the choice per model with the operator-tier `openrouter:` settings block; see the [model-routing guide](https://github.com/stacklok/mecatl/blob/main/docs/usage/model-routing.md#5b-openrouter-downstream-provider-routing-openrouter-issue-480).

Build-time composition facts (store kind, compaction strategy, feature flags) are logged once in `app.Build` and never re-emitted per session.

---

## Tool call audit

`port.ToolCallRecorder` captures one structured record per tool execution with timing (queue wait + execution duration). It is a separate channel from diagnostics and the event stream.

The `jsonlstore` backend (selected with `--store-dir`) implements `ToolCallRecorder` alongside `SessionStore` and `EventLog`. It writes tool records to a `.tools.jsonl` sidecar in the session's family directory under `--store-dir` (the filename is derived from the session id but is not reversible — see [Session store](../extension-points/session-store.md)). The `telemetry` adapter additionally derives counters and a latency histogram from these records — those feed into `mecatl_tool_calls_total` and `mecatl_tool_duration_seconds` on the `/metrics` endpoint.

---

## What's next

To configure mecatl for production, see the deployment guide for how to wire an OTLP collector, configure the admin listener, and set up session persistence with `jsonlstore`.
