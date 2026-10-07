---
sidebar_position: 140
title: Collect metrics, traces, and diagnostics
description:
  Collect operational metrics, traces, diagnostics, and tool audit records.
---

# Collect metrics, traces, and diagnostics

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

For retries, circuit breakers, stream timeouts, caching, and bounded network
inspection, see [Observability and resilience](/features/runtime/observability-and-resilience.md).
Configure their deployment controls through the [server CLI reference](/reference/server-cli.md).

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
[session store extension point](/building/go/extension-points/session-store.md) for
record ownership and naming.

## Anonymous product metrics

[Anonymous product metrics](/features/runtime/observability-and-resilience.md#anonymous-product-metrics)
are separate from your operational collection. The operator controls reporting
through flags, environment, or user-global settings.

The Helm chart stores `mecak8s`'s random installation ID in a ConfigMap and
passes it as `MECATL_PRODUCT_METRICS_INSTALL_ID`, so it survives pod restarts
without a persistent volume. Delete the ConfigMap to reset the ID, or disable
reporting with one of the controls in the capability guide.

## Next steps

- [Run `mecated`](/operating/mecated.md) to configure the admin listener, OTLP
  export, and durable storage.
- [Session store extension point](/building/go/extension-points/session-store.md)
  to provide custom persistence and audit recording.

## Related topics

<span id="retry-and-circuit-breaker" />
<span id="timeouts" />
<span id="prompt-caching" />
<span id="failure-diagnostics" />

[Observability and resilience](/features/runtime/observability-and-resilience.md)
