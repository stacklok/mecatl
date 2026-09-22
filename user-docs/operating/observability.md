---
sidebar_position: 140
title: Observability and resilience
description:
  Monitor Mecatl runs with metrics, traces, diagnostics, audit records, and
  resilient model calls.
---

# Observability and resilience

Mecatl exposes metrics, traces, structured logs, and tool audit records. Its
model-provider wrapper also handles transient failures and stalled streams.

|Channel|Purpose|
|-|-|
|Prometheus metrics|Measure runs, turns, tools, tokens, and failures.|
|OpenTelemetry traces|Follow run, turn, and tool spans.|
|Structured diagnostics|Report lifecycle and degraded-mode conditions.|
|Tool audit records|Record each tool call and its timing.|

Embedders choose and inject the sinks. The engine does not use a global logger.

## Prometheus metrics

In `mecated`, the admin listener serves `/metrics` at `127.0.0.1:9090` by
default. The endpoint is unauthenticated, so keep it on a loopback address.
`mecak8s` disables it by default. Embedders must provide their own recorder and
exporter.

Run metrics use a bounded `role` label: `main`, `subagent`, `member`,
`parallel`, `usermodel`, or `child`. Session IDs and model names do not appear
in labels.

|Series|Type|Labels|Measures|
|-|-|-|-|
|`mecatl_runs_total`|Counter|`stop`, `role`|Completed runs by stop reason|
|`mecatl_turns_total`|Counter|`role`|Completed model turns|
|`mecatl_turn_empty_total`|Counter|`role`|No-progress events|
|`mecatl_events_total`|Counter|`type`, `role`|Emitted session events|
|`mecatl_tool_calls_total`|Counter|`tool`, `error`, `role`|Tool calls|
|`mecatl_tool_duration_seconds`|Histogram|`tool`, `role`|Tool latency|
|`mecatl_tokens_total`|Counter|`kind`, `role`|Input, output, and cache tokens|
|`mecatl_cache_hit_ratio`|Gauge|`role`|Cache-read tokens divided by input tokens|
|`mecatl_active_runs`|Gauge|`role`|Active runs|
|`mecatl_permission_asks_total`|Counter|`role`|Permission requests|
|`mecatl_session_load_failures_total`|Counter|`class`|Concealed store or snapshot load failures|

The admin listener also serves:

|Path|Content|
|-|-|
|`/debug/pprof/*`|Go CPU, heap, goroutine, allocation, mutex, and block profiles|
|`/debug/vars`|Selected `runtime/metrics` values|
|`/debug/flightrecorder`|The in-memory execution trace ring|

The flight recorder is enabled by default with an 8 MiB, five-second window.
Mutex and block profiling require explicit flags because they add overhead.

### `mecatui` performance endpoints

`mecatui --perf` exposes the same endpoints through an owner-private UNIX socket
by default. `--perf-addr` selects a loopback TCP address. Raw admin data remains
outside model context and the session debugger.

`--perf-mcp` adds a read-only MCP server at `/mcp`. It summarizes runtime data
for agent analysis and exposes raw profiles only as user-audience resource
links. Mecatl refuses this option when the admin listener is not loopback.

## OpenTelemetry traces

Set `--otlp-endpoint` to enable run, turn, and tool spans. An empty endpoint
disables tracing without affecting Prometheus metrics.

|Flag|Default|Purpose|
|-|-|-|
|`--otlp-endpoint`|Empty|Set the OTLP collector and enable tracing.|
|`--otlp-protocol`|`grpc`|Use `grpc` or `http`.|
|`--otlp-insecure`|`false`|Disable TLS for a local collector.|

Concurrent runs that share one sink also share its root span. Mecatl does not
yet provide independent root correlation for those runs.

`EventSink.Emit` receives the run context so an embedder can parent telemetry to
the originating request. Read trace data during the call, but do not retain the
context or use its cancellation state to drop events.

## Model-call resilience

Mecatl wraps every model provider with retry, circuit-breaker, establishment
timeout, and stream-idle controls.

### Retry and circuit breaker

Mecatl retries transient failures only before the first response content is
committed. Retryable failures include rate limits, server errors, network
errors, establishment timeouts, and malformed initial SSE frames. It does not
retry other client errors, caller cancellations, or failures after streaming
begins.

|Flag|Default|Purpose|
|-|-|-|
|`--llm-max-attempts`|`3`|Limit the initial call plus retries.|
|`--llm-breaker-threshold`|`5`|Open the breaker after consecutive transient failures. `0` disables it.|
|`--llm-breaker-cooldown`|`30s`|Wait before a half-open trial.|

A successful call resets the breaker. Permanent client errors other than 408 or
429 and caller cancellations do not count toward the threshold. Exhausted
retries produce an `ExhaustedError`; an open breaker produces a `BreakerError`.

### Timeouts

|Flag|Default|Purpose|
|-|-|-|
|`--llm-per-attempt-timeout`|`300s`|Limit connection and time to first committed content. `0` disables it.|
|`--llm-stream-idle-timeout`|`180s`|Limit the gap between later stream chunks. `0` disables it.|

An establishment timeout is retryable. A stream-idle timeout is terminal because
replaying a partially visible response could duplicate work.

### Prompt caching

Provider-side prompt caching is enabled for Anthropic, OpenAI Responses, and
OpenRouter. The current OpenAI Chat Completions route uses no cache dialect.

|Flag|Default|Purpose|
|-|-|-|
|`--no-prompt-cache`|`false`|Disable provider caching.|
|`--anthropic-cache-ttl`|Provider default, usually `5m`|Set Anthropic cache breakpoints to `5m` or `1h`.|

Use `mecatl_tokens_total` and `mecatl_cache_hit_ratio` to confirm cache use.

### Failure diagnostics

Retry decisions and breaker transitions go to structured diagnostics without raw
errors or request content. The event log also receives sanitized
`network.attempt` records. `InspectSession {"view":"network"}` returns bounded
retry and terminal decisions without prompts, credentials, or response bodies.

## Structured diagnostics

`mecated` writes diagnostics to stderr or journald. An embedded `mecatui` server
writes to `$XDG_STATE_HOME/mecatl/mecatui.log`; a remote client discards local
server diagnostics.

Session facts belong in the event stream. Diagnostics cover operational
conditions that have no matching event, such as persistence failures, policy
denials, instruction-fragment assembly failures, and degraded integrations.

When caller ownership is enforced, session-load failures look like `NotFound` to
the caller. Operators receive only `class=store|snapshot|unknown`; logs and
metrics omit the requested session, owner, storage key, path, raw error, and
snapshot contents.

|Class|Operator response|
|-|-|
|`store`|Check backend health, credentials, TLS, connectivity, and timeouts.|
|`snapshot`|Check storage-integrity alerts and follow the backend's repair process.|
|`unknown`|Check the custom adapter's bounded diagnostics and add public failure classification.|

OpenRouter's downstream `provider.route` is an event-stream fact, not a log
entry. It can be absent on cache hits.

## Tool call audit

`port.ToolCallRecorder` receives one record per tool execution, including queue
and execution time. With `--store-dir`, `jsonlstore` writes `.tools.jsonl`
sidecars and feeds the tool count and duration metrics. See the
[session store extension point](/building/extension-points/session-store.md) for
record ownership and naming.

## Anonymous product metrics

Mecatl separately reports aggregate adoption metrics to Stacklok by default.
These metrics include version, OS and architecture, a random installation ID,
enabled feature families, provider family, binary name, and coarse counts and
durations. Tool names are limited to built-ins or the category `mcp`.

The report excludes prompts, file paths, MCP server and tool names, session and
run IDs, model IDs, and other free text. A one-time stderr notice appears before
the first report.

Disable product metrics with any of these controls:

- `--product-metrics=false`
- `MECATL_PRODUCT_METRICS=false`
- A truthy `DO_NOT_TRACK` value
- `telemetry.productMetrics.enabled: false` in user-global settings

`MECATL_PRODUCT_METRICS=true` overrides `DO_NOT_TRACK`. Project settings cannot
change the operator's choice. Use `--product-metrics-dry-run` to print the
observations instead of sending them.

The Helm chart stores `mecak8s`'s random installation ID in a ConfigMap and
passes it as `MECATL_PRODUCT_METRICS_INSTALL_ID`, so it survives pod restarts
without a persistent volume. Delete the ConfigMap to reset the ID, or disable
reporting with one of the controls above.

## Next steps

- [Run `mecated`](/operating/mecated.md) to configure the admin listener, OTLP
  export, and durable storage.
- [Session store extension point](/building/extension-points/session-store.md)
  to provide custom persistence and audit recording.
