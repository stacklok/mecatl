# Observability, persistence, and reliability

> Part of the [Mecatl architecture guide](../architecture.md).

This chapter covers what Mecatl records about itself, where session state lives,
and how sessions survive provider failures and restarts. The loop only emits events
and calls ports; composition decides what is logged, measured, stored, and leased.

## Three contracts, kept apart

| Contract | Port | Answers | Data rules |
| --- | --- | --- | --- |
| Diagnostics | `port.Diagnostics` | What is the harness doing? | Low-volume operational lines. May contain user data. |
| Audit | `port.ToolCallRecorder` | Which tools ran, with what result and timing? | Structured per-tool records, separate from the conversation. |
| Events | `port.EventSink` (live), `port.EventLog` (durable) | What happened in this session, in order? | Holds conversation content; the durable log is append-only. |

Keeping them apart lets each change alone: raising the log level leaves the audit
trail untouched, and metrics read events instead of parsing log lines.

### Diagnostics

`engine/` and `internal/` take an injected `port.Diagnostics` and never use
package-level `slog`; the lint configuration forbids `slog.Default`,
`slog.SetDefault`, and package-level `slog.Info`-style calls there. Only `cmd/`
mains install a global logger, so third-party libraries that log through `slog`
land in the same place as Mecatl's own lines. `internal/adapter/slogdiag` is the
production adapter, and each binary picks the destination: stderr for `mecated`,
a private state log for embedded `mecatui`. An engine built without a sink gets
`port.NopDiagnostics`. Diagnostics are not a privacy boundary: session ids, tool
names, and error text appear in them. The loop writes only a few degraded-mode
warnings; adapters log their own lifecycle.

### Tool-call audit

`port.ToolCallRecorder` receives every executed tool call with its result, its
wait in dispatch (mutating calls queue behind reads and permission asks), and its
run time. Recorders include the telemetry adapter, the anonymous product-metrics
adapter (`internal/adapter/productmetrics`), and the JSONL and Redis stores, which
keep a per-session tool sidecar.

### Live events and the durable log

`port.EventSink` is a synchronous mirror: the run writes each sequenced event to
its own `Events()` channel, then to the sink. The `ctx` passed to `Emit` carries
trace context only. Terminal events are emitted with an already-cancelled context,
so a sink that checked `ctx.Err()` would drop them.

`port.EventLog` is durable per-session storage, read back in append order. The
loop never calls it. The server relay in `internal/adapter/server` appends every
event it observes, including ones skipped on the client wire such as approvals,
user prompts, and compaction archives. It appends on a cancel-detached context, so
a disconnecting client cannot abort the durable write, and stamps each event with
the verified caller who acted, which may differ from the session owner.

The relay coalesces text and reasoning deltas into bounded chunks
(`RunEventRecorder`), while clients still receive every delta. Each record is
attempted exactly once: `Append` can fail after the write committed, so a retry
could duplicate it. A crash can lose a chunk; the snapshot remains authoritative.
A backend implementing `port.CursorEventLog` gets durable positions minted at
append time, so other processes can follow the log.

Consumers of the durable log all live in composition. Approval replay
(`internal/app/approvalreplay.go`) restores allow-always rules after a restart.
Compaction archives keep pre-compaction history recoverable, and the debug
`InspectSession` tool reads archives and delegation lineage. For hosts whose system
of record is an event log, `engine/adapter/eventsource.Fold` rebuilds a session
from events. Provider-private replay fields such as reasoning blobs never cross
the event stream, so a folded session replays byte-identically only for providers
that do not use them; Mecatl's own stores load from snapshots, which carry them.

## Telemetry

`internal/adapter/telemetry` implements both `port.EventSink` and
`port.ToolCallRecorder`. `telemetry.Setup` always builds an OpenTelemetry
MeterProvider with a Prometheus reader, so `/metrics` always has data. It adds
OTLP trace export or OTLP metrics push only when the matching endpoint is set.
All latency instruments share one explicit-bucket ladder (`LatencyViews`).

Composition happens in `cmd/` mains: `cmd/mecated` wires it inline, and
`internal/cliconfig.HeadlessTelemetry` gives the headless mains the same shape.
`internal/app` never imports the telemetry adapter. It receives closures instead,
such as `Config.MetricsRoleScoper`, which tags each child engine's sink with a
bounded role family (`subagent`, `member`, `parallel`, and so on). Labels come
from closed sets, never session or model ids, so series cardinality stays bounded.

The unauthenticated admin listener (pprof, flight recorder, optional read-only perf
MCP server in `internal/adapter/mcpperf`) binds only to loopback or an owner-private
Unix socket. Offline benchmarks are in [performance tracking](../perf-tracking.md).

## Persistence

`port.SessionStore` saves and loads whole sessions. Implementations: in-memory
`engine/adapter/memstore` (the default), `adapters/jsonlstore` (files),
`adapters/redisstore`, and `adapters/grpcdriver` (remote). All serialize through
`engine/adapter/sessnap`, which restores by driving the session's public state
machine rather than setting fields. A session saved while awaiting approval
reloads with its pending ask, a terminal session keeps its exact stop reason, and
the token ledger carries over so a run budget continues across restart.

The server saves at session creation, when a run pauses for approval, and at run
end. A lookup that misses the in-memory registry falls back to the store. An
approval against a persisted awaiting session rebuilds the engine and resumes the
loop at the ask; any other stored session has no run, so approve or cancel returns
`ErrNoActiveRun`. An in-flight stream is never resumed. The JSONL store replaces
snapshots atomically under a per-session file lock shared with its sidecars.
Optional capabilities are found by type assertion. `port.PrunableStore` is the
retention mechanism; policy lives in `internal/app/childgc.go`, and all cleanup
goes through `internal/sessionretention/planner.go`, which protects running,
awaiting, live, and leased sessions. See
[session storage operations](../../user-docs/operating/session-storage-operations.md).

### Session lease and single-writer

When processes share a store, two must never drive the same session at once.
`port.SessionLease` enforces that; it is optional, and the loop never imports it.
The server acquires the lease at run entry, after the same-process per-session
lock, for new prompts and for approvals that resume a parked run. A Service-owned
goroutine renews it. It is held for the session's life in this process, not per
run, so no competitor takes the session between turns.

- A live competing owner refuses the run with `ErrSessionLeasedElsewhere` (gRPC
  `FAILED_PRECONDITION`, HTTP 409).
- A renewal that finds another owner cancels the run, and the process will not
  re-acquire until the loss is reconciled. Transient renewal errors get grace
  until the lease nears expiry.
- `ErrLeaseUnsupported` disables leasing for the process with one log line.

Grants carry a fencing token that stores do not check; the grant itself is the
enforcement. `buildSessionLease` in `internal/app` picks the backend: a gRPC
driver, Kubernetes `Lease` objects (`internal/adapter/k8slease`), or a flock
directory (`internal/adapter/flocklease`). A local JSONL store always gets a flock
lease beneath its root; otherwise the store is asked whether it provides one.
Replica operations are in [deployment and hardening](deployment-and-hardening.md);
the contract is in
[the session lease extension point](../../user-docs/building/go/extension-points/session-lease.md).

### Remote store + source drivers (`adapters/grpcdriver`)

Each storage or content seam can move out of process, to a driver speaking
`mecatl.driver.v1` (`contracts/proto/mecatl/driver/v1/`). `adapters/grpcdriver`
holds client adapters for the engine ports and server wrappers for Go backends.
`app.Build` selects each seam independently: session store, memory store, event
log, lease, schedule store, and the skill, soul, agent, and command sources. Equal
URLs share one connection. See the
[store integration guide](../../user-docs/building/go/extension-points/session-store.md)
and [`adapters/COMPATIBILITY.md`](../../adapters/COMPATIBILITY.md). A driver is
trusted like a store directory, but never trusted to sanitize:

- Snapshots cross as an opaque `sessnap-json/1` envelope the driver stores
  verbatim. An unknown tag or a mis-keyed session is an infrastructure error,
  never "not found". Drivers must accept up to `grpcdriver.MaxSnapshotBytes`.
- Skills cross as path-free logical bundles, so a driver cannot widen the
  workspace. A driver soul body is re-validated client-side.
- Agent definitions are read once at build, because each bakes a child engine.
  Slash commands are read live, and local command files shadow driver commands.
- Session and memory clients negotiate capabilities at build. Clients add no
  retries or default deadline, and refuse non-local cleartext.

Shared conformance suites (`storeconformance`, `eventlogconformance`,
`leaseconformance`, `sourceconformance`, and others) run against every backend and
against the gRPC clients over an in-memory connection, so drivers cannot drift.

## Reliability & provider resilience

`internal/app` wraps each `port.LLMProvider` in `llmresilience.Wrap`, so recovery
belongs to the decorator and the loop never replays a model step. It adds bounded
retries with backoff and provider retry hints, per-attempt and stream-idle
timeouts, and a circuit breaker. Settings are in
[observability and resilience](../../user-docs/operating/observability.md).

### Semantic stream retry

The rule is no replay after the user could see output. Each failure carries two
typed facts: retry disposition (`unknown`, `retryable`, `permanent`) and stream
progress (`unknown`, `precommit`, `visible`, `complete`). The decorator buffers
tentative output: leading whitespace, reasoning, replay state, tool calls. The
first non-whitespace text commits the attempt and flushes the buffer in order; a
clean completion also commits, so tool-only turns never run tools twice. A
retryable precommit failure discards the buffer and retries; after commit, failure
is terminal. Reasoning displays slightly later, but a retry never shows two
attempts. Usage from discarded attempts is still counted.

The breaker counts only transient establishment failures; permanent errors,
caller cancellation, and credential failures are breaker-neutral.

The terminal `result` event, snapshots, and event folding preserve both facts. A
retryable failure at `precommit` or `visible` can be retried explicitly (gRPC
`RetryStart`, HTTP `POST /v1/sessions/{id}/retry`). The session records a durable
retry intent and blocks normal prompts until it resolves. The retry reuses the
stored conversation but re-resolves live instructions, so it is not a byte-exact
replay. Failed partial deltas stay in the event log but never enter the rebuilt
conversation. Retry diagnostics never record prompts, headers, or raw error bodies.

## Related

- [Ports](ports.md)
- [Providers](providers.md)
- [Deployment and hardening](deployment-and-hardening.md)
- [Observability and resilience (user docs)](../../user-docs/operating/observability.md)
