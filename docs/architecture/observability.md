# Observability, persistence & reliability

> Part of the [mecatl architecture guide](../architecture.md).

## Observability & persistence

- **EventSink** (`port.EventSink`) — an optional secondary relay.
  `Emit(ctx, ev)` carries the run's `context.Context` so telemetry can parent a
  run span to an inbound request span (the ctx is a **trace/baggage carrier
  only** — sinks must not derive cancellation/deadlines from it, since terminal
  emits deliberately pass an already-cancelled ctx). `Run.emit` always writes to
  the `Events()` channel (the primary surface) and then mirrors the sequenced
  event to `Deps.Sink` when configured (`dispatch.go`'s `Engine.emit`, which
  forwards the per-run `Run.ctx`). `Seq` is a monotonic per-run counter
  (`atomic.Int64`).
- **ToolCallRecorder** (`port.ToolCallRecorder`) — `ToolCall(id, call, result,
  queued, took)` records tool queue + execution timing, the per-tool AUDIT seam,
  distinct from the model-visible conversation. The loop times execution via the
  injected `Clock` (`timeExecute`).
- **Diagnostics** (`port.Diagnostics`) — the injected operational-logging seam
  (composition decisions, degraded-mode warnings, lifecycle notes), DISTINCT from
  the audit (`ToolCallRecorder`) and the event stream (`EventSink`). `engine/` + `internal/`
  take this port and NEVER touch global slog; the `slogdiag` adapter is the only
  slog bridge and composition picks the sink per binary. The ban is `forbidigo`-
  guarded. See `docs/adr/0020-diagnostics.md`.
- **Telemetry** (`internal/adapter/telemetry`) — one adapter that implements
  **both** `port.EventSink` (deriving counters/gauges from the event stream) and
  `port.ToolCallRecorder` (per-tool counters + a latency histogram). It is built on the
  **OTel metrics SDK**: `telemetry.NewMetrics(metric.MeterProvider)` creates the
  domain instruments from an injected MeterProvider. `telemetry.NewSink` fans one
  Engine `EventSink` out to several sinks; OTel spans model the run/turn/tool
  hierarchy. `telemetry.Setup` returns a `Providers{Tracer, Meter, Registry,
  Shutdown}` struct: **metrics are always on** — Setup always builds an SDK
  MeterProvider with a Prometheus-exporter reader on a fresh `Registry`, installs
  the tool-duration base-2 exponential-histogram view (`telemetry.ToolDurationView`,
  the single source of truth any MeterProvider feeding `NewMetrics` must install),
  and starts the contrib **runtime collector** (goroutines, GC, heap) against that
  provider. `/metrics` is served via the OTel Prometheus exporter using
  `telemetry.MetricsHandler(providers.Registry)`. **Tracing installs only when an
  OTLP endpoint is set**: with `cfg.Endpoint` non-empty Setup builds an OTLP span
  exporter + SDK TracerProvider and installs it globally (wired in `mecated` via
  `--otlp-endpoint` / `--otlp-protocol` / `--otlp-insecure`); with an empty
  endpoint `Providers.Tracer` is the current (no-op) global and only metrics run.
  Child engines (Subagent / team member / Parallel) get **role-tagged metric
  views** when `Config.MetricsRoleScoper` is wired (issue #47): their
  `Deps.Sink`/`ToolCallRecorder` are scoped per role FAMILY — the closed set
  `main|subagent|member|parallel|usermodel|child`, never the raw role (which can
  embed a def name or model id), so label cardinality stays bounded; without a
  scoper they stay nil, byte-identical to the metrics-off posture.
  The domain counters include the run-terminal `mecatl_runs_total{stop,role}`
  (one per run, by terminal stop reason) and — for **turn-semantics**
  observability (issue #81) — `mecatl_turns_total{role}` (one per COMPLETED
  turn — every model exchange that reached a turn boundary, the per-turn
  denominator; deliberately UNLABELLED by stop, since `EvTurnEnd` carries no stop
  reason) and `mecatl_turn_empty_total{role}` (the EMPTY SUBSET — a turn that
  produced NEITHER a tool call NOR text, derived from `EvNoProgress`). The two are
  **not** mutually exclusive: an empty turn emits `EvTurnEnd` first (bumping
  `turns_total`) and then `EvNoProgress` (bumping `turn_empty_total`), so
  `turn_empty_total / turns_total` is the empty-turn **share** (empty turns ⊂ all
  completed turns), not a disjoint numerator/denominator. `turn_empty_total` counts
  `EvNoProgress` *emissions* — the loop emits one per advisory nudge AND on the
  give-up, up to `MaxNoProgressNudges+1` per stuck sequence. Abnormal terminal
  stops stay on `runs_total{stop}`; the turn counters carry only the bounded
  `role` label. The perf MCP surfaces both with a per-role breakdown
  (`turns_total`, `turn_empty_total` in `perf://metrics/summary`).
  **Schedule metrics** (issue #233, Phase 2b) are a SEPARATE dimension — NOT a
  role-family: a scheduled-task fire mints a fresh session whose OWN run already
  carries `role="main"` via its EventSink, so the schedule series carry NO role
  label. The scheduler (`internal/adapter/scheduler`) is telemetry-import-free and
  reports fires through a callback seam (`Config.ScheduleMetrics`), which
  composition wires over `Metrics.EmitSchedule`. Two instruments:
  `mecatl_schedule_fires_total{outcome}` (fired/skipped/failed — bumped for
  every fire, including the misfire-skip and singleton-overlap-skip paths) and
  `mecatl_schedule_fire_duration_seconds` (the due→terminal wall-clock, recorded
  only for a fired/failed fire; a skipped fire has no run, duration 0). The
  duration histogram shares the `latencyInstruments` explicit-bucket ladder.
  > A broader performance-observability effort lands incrementally on a
  > **loopback-only, unauthenticated admin listener** (`--metrics-addr`, default
  > `127.0.0.1:9090`): `/metrics`, `/debug/pprof/*`, `/debug/vars` (a curated
  > `runtime/metrics` snapshot), `/debug/flightrecorder` (an execution-trace ring),
  > and — opt-in via `--perf-mcp` — `/mcp`, the read-only **perf MCP server**
  > (`internal/adapter/mcpperf`) that lets an agent introspect this process's
  > runtime/latency/profile state as reduced numeric summaries (slow-turns,
  > profile rankings, FlightRecorder summaries). All of it is **live**. The MCP
  > surface is fail-closed to loopback (it is unauthenticated and can embed
  > goroutine-derived names/timing). See `docs/adr/0018-perf-observability.md` (the
  > decided direction) and the [Go performance measurement & observability
  > survey](../perf-measurement-survey.md) (the technique reference behind that
  > decision).
- **SessionStore** — `memstore` (default, in-memory), `jsonlstore`
  (append-only JSONL replay log: `<dir>/<id>.session.jsonl` snapshots +
  `<dir>/<id>.tools.jsonl` tool records; `jsonlstore` also implements
  `ToolCallRecorder`),
  and `grpcdriver.SessionStore` (a **remote store driver** — see below).
  All serialize via **`sessnap`** (`engine/adapter/sessnap`): a `Snapshot` DTO
  that round-trips a `Session` by driving the public state machine on restore
  (so a session saved mid-`awaiting` reloads with its pending ask intact). It
  captures the terminal reason via `RecordedStopReason()` for exact round-trips,
  and (cloud-native Phase 1) the per-session profile, the opaque provider/model
  selector pair, and the cumulative token `usage` — additive fields so a
  restarted process rebuilds the SAME engine and the `MaxRunTokens` budget
  continues across restart (see `docs/adr/0027-cloud-native.md`).
  A store may additionally implement the optional **`port.PrunableStore`**
  (`List`/`Delete`; `ErrPruneUnsupported` otherwise) — the retention MECHANISM.
  The POLICY lives in composition (`internal/app/childgc.go`, issue #38):
  persisted CHILD session snapshots (the `subagent-*`/`parallel-*`/`team-*` ids
  behind `InspectSubagent`/`InspectMember`/`resume:`) are GC-swept by age
  (`--child-retention`, default 7d) and per-family count
  (`--child-retention-max-per-family`, default 500), skipping in-flight runs;
  main sessions are never touched.
- **EventLog** (`port.EventLog`, cloud-native Phase 3) — a DURABLE per-session
  event timeline, SEPARATE from `EventSink` (the sink mirrors live; the log is
  storage a later consumer reads back). **The loop never calls it** — persistence
  lives at the relay (`internal/adapter/server`), which appends every observed
  event (incl. the post-disconnect tail and `EvApproval`/`EvCompactionArchive`,
  which are skipped on the client wire) on a cancel-detached context so a dead
  client can't abort the durable write. `jsonlstore` triples as
  `SessionStore`+`ToolCallRecorder`+`EventLog` (a `.events.jsonl` sidecar);
  memstore has an in-memory sibling; `grpcdriver` carries the remote
  `EventLogService` (`--event-log-url`, independent of the session store). The
  log also records the new **log-only `EvUserPrompt`** — every user-role turn
  (the genuine prompt plus the harness's synthetic continuations), skipped on the
  client wire — so that a fold can reconstruct what the user asked. **Three**
  consumers, all in **composition** (never the loop): the non-destructive
  **compaction archive**; the **permstore verdict-replay**
  (`internal/app/approvalreplay.go`) that re-derives learned allow-always rules
  after a restart so a previously-approved tool does not re-ask; and (#115)
  **event-sourced `SessionStore.Load`** — a host whose system of record is the
  event log folds `EventLog` + `SessionMeta` → `*session.Session` via
  `engine/adapter/eventsource.Fold`. That third consumer carries a documented
  **replay-fidelity limitation**: the opaque assistant-replay fields
  (`Message.Reasoning`/`ProviderPhase`, `ToolCall.ItemID`) are not on the stream,
  so a pure fold is byte-identical-replay faithful only for plain-chat providers
  (ADR 0038). See `docs/adr/0027-cloud-native.md` Phase 3 and the
  `eventlogconformance` suite.

  > **Two `Load` implementations, one port.** mecatl's own adapters (memstore,
  > jsonlstore, the remote driver) implement `Load` by **snapshot-deserialize**
  > (`sessnap`) — byte-identical including the reasoning-replay fields. An
  > event-log-as-system-of-record host instead implements `Load` by
  > **event-fold** (`eventsource.Fold`), which carries the replay-fidelity caveat
  > above. Both satisfy the same `port.SessionStore.Load` contract.

- **SessionLease** (`port.SessionLease`, cloud-native Phase 4) — OPTIONAL
  cross-process single-writer enforcement for multi-replica deployments over a
  shared store. **The loop never imports it** — the lease is acquired at the
  run-entry funnel (`internal/adapter/server/service.go` (`acquireLease`), called
  by `StartRunContent`/`resumeFromAwaiting` AFTER the per-session `runEntryMu`),
  renewed by a Service-owned goroutine (`renewLoop`), and released on
  `CloseSession`/shutdown (cancel-detached short-timeout ctx). A competing live
  owner refuses the run with `ErrSessionLeasedElsewhere` (gRPC
  `FAILED_PRECONDITION` / HTTP 409 Conflict); a lost lease cancels the run
  (fail-safe, `StopCancelled` is recoverable). Backends (discovered by type
  assertion, the `PrunableStore` precedent): `memlease` (in-memory reference;
  `memstore` also implements it), `flocklease` (single-host flock), `grpcdriver`
  `SessionLeaseService` (`--session-lease-url`, multi-host), `k8slease`
  (`coordination.k8s.io` Lease, `--session-lease-k8s-namespace`, in-cluster). Wired
  ONLY when an operator selects a backend (`internal/app` (`buildSessionLease`));
  nil otherwise — byte-identical default (single-writer-by-affinity). The
  conformance contract is `leaseconformance`. See `docs/adr/0027-cloud-native.md`
  Phase 4.

### Remote store + source drivers (`internal/adapter/grpcdriver`)

> Design rationale — the port/driver pattern, the per-seam lifecycle and
> failure-posture decisions, the deferrals, and the workspace-driver sketch —
> lives in `docs/adr/0005-driver-seams.md`.

The session and memory stores have a **wire seam**: an operator can point
either at a remote, operator-run **driver process** speaking the
`mecatl.driver.v1` protocol (`contracts/proto/mecatl/driver/v1/` —
`SessionStoreService` for `port.SessionStore`, `MemoryStoreService` for
`tool.MemoryStore`). Selection is composition-only (`app.Build`):
`--session-store-url` replaces `--store-dir` (mutually exclusive, fatal at
build), `--memory-store-url` replaces `--memory-dir`; all-empty keeps the
local stores byte-identical. Equal URLs share ONE lazy `ClientConn` (the
build-scoped `driverConns` cache); the user-model store stays local (a Phase B
deferral).

Phase C1 adds the **content-source drivers** on the same protocol:
`SkillSourceService` behind the `tool.SkillSource` port (skills cross as
LOGICAL BUNDLES — metadata, body, payloads by logical name; NO path/dir/root
on the wire) and `SoulSourceService` behind `prompt.SoulSource`.
`--skill-source-url` replaces local skills discovery (mutually exclusive with
`--skills-dir`/`--skills-conventional`); a driver skill's payloads
materialize LAZILY into a build-scoped temp asset cache on first activation
(per-asset 16 MiB / per-bundle 64 MiB caps, logical-name validation +
containment, executable bit honored; the cache dir is the single skill read
root and is removed on shutdown). `--soul-source-url` occupies the USER slot
of the soul selection (mutually exclusive with `--soul-file`; `--no-soul`
wins); the body is RE-VALIDATED client-side (`soul.ValidateBody` — byte cap,
injection scan, fence integrity) because a driver is never trusted to
sanitize, the drift baseline is SKIPPED for driver provenance, and the driver
is probed at build (fatal if unreachable; runtime faults degrade fail-soft).

Phase C2 completes the content-source family: `AgentSourceService` behind the
`tool.AgentDefSource` port (the WHOLE `AgentDef` value object crosses — tools,
limits, model/provider hints, skills, hooks, MCP servers incl. their
SECRET-SHAPED inline headers, which are never logged or projected anywhere and
ride the wire only because driver dials refuse all non-local cleartext; NO
path/dir/root — the old `Path` field is gone, replaced by the adapters'
NON-PORT detail channel `agents.Discovered.Detail`/`Registry.Detail`) and
`CommandSourceService` behind the consumer-local `prompt.CommandSource`.
**The two seams deliberately differ in lifecycle**: agent defs are
SNAPSHOT-at-build (`--agent-source-url`; ONE `ListAgentDefs`, fatal if
unreachable — per-def child engines are baked once, the trust-gate-completeness
posture; mutually exclusive with `--agents-dir`, while the default-on
`--agents-conventional` is simply SUPERSEDED with an INFO narration), whereas
slash commands are LIVE-per-call (`--command-source-url`; consulted on every
expansion/listing, build-time `Probe` fatal, runtime faults fail SOFT — a
transient blip must never latch a command "missing"). The command driver also
COMPOSES instead of replacing: the expander order is file commands →
driver source → MCP prompts (first-that-expands-wins), so a local command
file shadows a same-named driver command. The harness clients re-normalize
defensively (blank/grammar-invalid names dropped, de-dup, sort, single-line +
re-capped descriptions/bodies, hooks/headers re-normalized) and stamp the
driver origin tier UNCONDITIONALLY; the registry is resolved EXACTLY ONCE per
build (`resolveAgentSeam`) and shared by the catalog, the per-session engine
factory, the `ListAgents` snapshot, and the team wiring.

**sessnap IS the wire format** for sessions: the snapshot crosses as an
opaque, format-tagged envelope (`format: "sessnap-json/1"`, payload =
`sessnap.Marshal` output). The driver stores/returns it VERBATIM and never
decodes; the harness rejects an unknown format on Load with an infrastructure
error (never not-found), and a decoded session whose id is not the requested
one (a mis-keyed driver) the same way. Snapshot schema evolution stays
additive in sessnap; the envelope's tag changes only if the encoding itself
is replaced (a future bump must be read-set-accept / write-newest, or stored
sessions brick). **Capacity:** a conforming driver MUST accept payloads up to
`grpcdriver.MaxSnapshotBytes` (64 MiB — media-carrying snapshots far exceed
gRPC's 4 MiB default); the harness client's send/receive limits are raised to
the same value by `Dial`. Error mapping mirrors the local stores: driver
`NOT_FOUND` → `errors.Is(err, port.ErrSessionNotFound)`; a Recall miss is
`found=false`, never an error; deadline passthrough only — no retries, no
default deadline (resilience, if ever needed, is a decorator, the
`llmresilience` precedent).

**Conformance is the contract.** Every store implementation passes a shared
behavioral suite, and the gRPC clients pass the SAME suite over a bufconn
wire (client → server wrapper → reference backend), so a remote driver cannot
drift from the in-process semantics:

| Suite | Backend | Run site |
|---|---|---|
| `storeconformance` (`Run` + `RunPrunable`) | `memstore` | `engine/adapter/memstore/conformance_test.go` |
| `storeconformance` (`Run` + `RunPrunable`) | `jsonlstore` | `internal/adapter/store/jsonlstore/conformance_test.go` |
| `storeconformance` (`Run` + `RunPrunable`) | grpcdriver → bufconn → `NewSessionStoreServer(memstore)` | `internal/adapter/grpcdriver/conformance_test.go` |
| `memconformance` | flock `memory.Store` | `internal/adapter/memory/conformance_test.go` |
| `memconformance` | grpcdriver → bufconn → `NewMemoryStoreServer(memory.Store)` | `internal/adapter/grpcdriver/conformance_test.go` |
| `sourceconformance.RunSkillSource` | in-memory `NewFixtureSource` (self-test) | `engine/adapter/sourceconformance/sourceconformance_selftest_test.go` |
| `sourceconformance.RunSkillSource` | `skills.FSSource` over a written-out fixture tree | `internal/adapter/skills/conformance_test.go` |
| `sourceconformance.RunSkillSource` | grpcdriver → bufconn → `NewSkillSourceServer(NewFixtureSource)` | `internal/adapter/grpcdriver/conformance_test.go` |
| `sourceconformance.RunSoulSource` | `soul.Store` (temp file) | `internal/adapter/soul/conformance_test.go` |
| `sourceconformance.RunSoulSource` | grpcdriver → bufconn → `NewSoulSourceServer(verbatim fake)` | `internal/adapter/grpcdriver/conformance_test.go` |
| `sourceconformance.RunAgentSource` | in-memory `NewAgentFixtureSource` (self-test) | `engine/adapter/sourceconformance/sourceconformance_selftest_test.go` |
| `sourceconformance.RunAgentSource` | `agents.FSSource` over a written-out fixture tree | `internal/adapter/agents/conformance_test.go` |
| `sourceconformance.RunAgentSource` | grpcdriver → bufconn → `NewAgentSourceServer(NewAgentFixtureSource)` | `internal/adapter/grpcdriver/conformance_test.go` |
| `sourceconformance.RunCommandSource` | in-memory `NewCommandFixtureSource` (self-test) | `engine/adapter/sourceconformance/sourceconformance_selftest_test.go` |
| `sourceconformance.RunCommandSource` | grpcdriver → bufconn → `NewCommandSourceServer(NewCommandFixtureSource)` | `internal/adapter/grpcdriver/conformance_test.go` |

(Deliberately NO filesystem row for commands: `prompt.DirCommandExpander` is
the workspace-tier surface — live, workspace-relative, read through the
`tool.Workspace` port — NOT a `CommandSource` implementation.)

## Reliability — provider resilience

The `port.LLMProvider` seam is wrapped by a **decorator**,
`llmresilience.Wrap(inner, Config) port.LLMProvider`, so the loop is unchanged.
It adds retry with exponential backoff and a circuit breaker, configured in
`mecated` via `--llm-max-attempts` / `--llm-per-attempt-timeout` /
`--llm-breaker-threshold` / `--llm-breaker-cooldown`.

Its load-bearing invariant is **no replay after the first chunk**: retries happen
only while *establishing* the stream (connect + first chunk). Once the first
`Chunk` has been yielded, the decorator never re-issues the call, so the model
never re-sees a half-streamed turn. The breaker opens after N consecutive
**transient** establishment failures (rate-limits, timeouts, 5xx, network);
permanent client errors (4xx other than 408/429) and caller cancellations don't
count. It short-circuits with a `BreakerError` until its
cooldown half-opens it; exhausted retries surface as an `ExhaustedError`. Both
flow back to the client as a terminal `result` event — `session.ResultPayload`
now carries an **`Error`** field, so a provider failure is reported to the caller
rather than swallowed.

Mid-stream stalls are bounded separately, by `Config.StreamIdleTimeout`
(`--llm-stream-idle-timeout`, default 120s, 0 disables), NOT by
`PerAttemptTimeout`: after the first chunk a per-chunk watchdog caps the idle
gap between consecutive chunks and synthesizes a terminal `*StreamIdleError`
(`errors.Is(_, context.DeadlineExceeded)`) when it fires — the wrapper must
synthesize it because the adapters deliberately swallow the ctx error on
cancel and would otherwise yield nothing. The stall is TERMINAL, never retried
(no-replay-after-first-chunk holds); pre-first-chunk stalls stay on
`PerAttemptTimeout` + retry, unchanged.

The `llmresilience` decorator emits stream-lifecycle diagnostics through an
**injected `port.Diagnostics`** (`Config.Diagnostics`, defaulted to
`port.NopDiagnostics`, wired at composition from the build-time sink): a DEBUG
line on each retry and per-attempt-timeout, and INFO on the idle-stall terminal
(the decisive #82 stall signal), the breaker open/half-open/close transitions,
and retry exhaustion. Every line is metadata-only (the wrapper sees only
`port.LLMRequest` + errors, never prompt text; error strings are clamped). This
is an **ADAPTER seam** and is deliberately OUTSIDE the agent loop's three-line
diagnostic budget (that budget governs the loop's run-scoped sink, not adapters
— see `docs/adr/0020-diagnostics.md`); the wrapper is per-provider and logs
provider-level lifecycle, not session-correlated lines.

Auto-resume complements this: `GetSession`/`Approve`/`Cancel` fall back to
`SessionStore.Load`, and the service persists at create, on entering `awaiting`,
and at run end. With `--store-dir` (jsonlstore) a session survives a restart and
is loadable; the in-flight *stream* itself is not resumed (the `*agent.Run` is
in-memory), and an approve/cancel against a stored-but-runless session returns
`ErrNoActiveRun` (HTTP 409 / gRPC `FailedPrecondition`).

## Related

- [The API surface it instruments](api-surface.md)
- [Providers — provider resilience](providers.md)
- [Deployment & server hardening](deployment-and-hardening.md)

---

[← Architecture guide](../architecture.md)
