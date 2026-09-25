# Observability, persistence & reliability

> Part of the [mecatl architecture guide](../architecture.md).

**What this covers:** the `EventSink`/`ToolCallRecorder`/`Diagnostics` instrumentation seams, the OTel telemetry adapter (metrics + optional tracing), the `SessionStore` families (memstore/jsonlstore/redisstore/remote driver) and `sessnap` snapshot format, the `EventLog` durable event timeline (cloud-native Phase 3), the `SessionLease` single-writer seam (Phase 4), the remote store + content-source driver protocol (`grpcdriver`), conformance suites, and the `llmresilience` retry/breaker decorator.

**Prerequisites:** [the ports](ports.md) — the ports being instrumented and persisted.

**Follow-on:** [deployment & hardening](deployment-and-hardening.md) — server hardening over these persistence seams.

## Observability & persistence

### Offline performance regression tracking

`task bench` measures deterministic `allocs/op` for two complementary agent-loop
contracts. `BenchmarkRun*Turn` constructs a fresh session for every iteration, so
it captures bounded session lifecycle and first-prompt initialization. The matching
`BenchmarkSteadyState*Turn` prewarms that first run and measures one normal
continuation turn after `Session.Reopen`, including recurring reopen and run work
without attributing startup initialization to every turn. Both families are
hard-gated independently by `perf/cmd/allocsgate`; the PR workflow compares them
against the main baseline but never publishes a new baseline from a PR.

`task perf:scenarios` remains the separate whole-loop signal. In particular,
`BenchmarkSingleSessionLong` preserves the approximately 500-turn single-session
coverage for allocations, RSS, tokens, cache-hit rate, and goroutine hygiene. See
`docs/adr/0019-perf-tracking.md` for the performance-tracking decision.

The `gh-pages` trend store keeps commit granularity without retaining duplicate
sampling rows. The hard microbenchmark gate and its latest-main `bench.txt`
baseline retain all `BENCHCOUNT=10` samples; `perf/cmd/benchtrend` median-aggregates
those samples to one `ns/op`, `B/op`, and `allocs/op` point per benchmark and main
commit for the advisory dashboard. Before publishing, the same command
idempotently compacts legacy Go-tool history to that representation. Scenario
samples follow the same median-per-commit rule through `perf/cmd/perfconvert`.

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
  guarded. See `docs/adr/0020-diagnostics.md`. The `mecated` and `mecak8s`
  command roots share an exact `--log-level` flag (`debug`, `info`, `warn`, or
  `error`; default `info`). Each root installs its configured stderr logger as
  the global `slog` default and wraps that same logger with `slogdiag`, so
  ambient library records and injected diagnostics obey one threshold. Embedded
  `mecatui` instead redirects its diagnostics to its private state log. A stable
  cross-process lock is held for the writer lifetime, so another instance fails
  closed rather than replacing an actively written log. At startup it opens the
  data path without following symlinks, atomically retains a recent 10 MiB tail
  of an oversized regular log, syncs the replacement and containing directory,
  then appends. Unsafe paths and failures before replacement preserve the prior
  log and fall back to `io.Discard`. Invalid
  values, including an explicitly empty value, fail soft to `info` and produce
  one warning after logger installation.
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
  Session lookup failures use a separate target-free callback: under ownership
  enforcement, one non-not-found `GetSession` failure emits one WARN and increments
  `mecatl_session_load_failures_total{class="store|snapshot|unknown"}` once. Unlike
  run-derived metrics, this service-boundary counter has no `role` label: `class` is
  its only label. `store` means retrieval or transport failed; `snapshot` means
  retrieved bytes failed format, decode, persisted-identity, or validation checks;
  `unknown` is the fail-safe result for an untyped custom-store error. Operators
  should respectively check backend reachability/configuration, storage integrity or
  mis-keying and backup recovery, or the custom adapter's bounded health diagnostics
  and typed wrapping. They must not infer a class from text or add target data while
  investigating. `Service.GetSession` invokes its injected diagnostics sink with a
  detached clean context and supplies only the port-owned `class` field and constant
  `ownership=enforced` marker. It adds no request target, principal, path, cause,
  blob content, or blob size data. Attributes deliberately pre-bound by the trusted
  operator-supplied `port.Diagnostics` sink are outside this producer's control.
  Genuine absence and foreign ownership remain silent and caller-visible as the same
  NotFound result.
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
  > separate unauthenticated admin listener: `mecated --metrics-addr` remains
  > loopback-only (default `127.0.0.1:9090`), while embedded `mecatui --perf`
  > defaults to an owner-private per-instance UNIX `admin.sock` beside its gRPC
  > socket. Multiple mecatui instances therefore do not contend for a fixed port.
  > An explicit mecatui `--perf-addr` selects TCP and is rejected unless loopback;
  > `--perf-mcp` with no explicit address uses ephemeral loopback TCP because the
  > supported MCP transport is streaming HTTP and requires a URL. No stdio MCP is
  > introduced. The surfaces serve `/metrics`, `/debug/pprof/*`, `/debug/vars`,
  > `/debug/flightrecorder` (an execution-trace ring),
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
  (one atomically replaced current snapshot at
  `<dir>/sid-v1/<versioned-token>.session.json`, with append-only `.tools.jsonl`
  and `.events.jsonl` sidecars in that same family), and
  `grpcdriver.SessionStore` (a **remote store driver** — see below). Distinct
  root-level artifacts are invisible and left untouched. The current
  envelope contains bounded inventory metadata ahead
  of the complete `sessnap` payload plus logical modification time. Jsonlstore also
  maintains an adapter-private, atomically replaced metadata catalog containing only
  the session-discovery projection—never messages, tool arguments, or event content.
  It pre-sorts deterministic `(modified_at DESC, session_id ASC)` global and
  owner-specific scopes. A ready metadata page privately decodes its opaque
  backend continuation, seeks directly to the corresponding catalog position, and
  decodes at most the requested rows plus one lookahead; it does not traverse prior
  pages or open snapshot/transcript payloads. The optional
  `port.SessionStorageHealthProvider` uses that already-ready catalog plus cheap
  file metadata to report aggregate bytes and kind/corruption counts. A
  stale or absent index is `unavailable`, never a measured zero, and unsupported
  backends do not advertise the management capability. Process-wide health and
  cleanup are disabled unless composition has an explicit management authority.
  Destructive cleanup capability additionally requires a working cross-process
  `port.SessionLease` for remotely reachable or multi-writer composition;
  missing or stickily unsupported leasing suppresses the bit and mutations fail closed.
  The private embedded mecatui Unix-socket server is the sole explicit single-process
  exception: composition marks that proven posture, allowing process-local liveness plus
  family locks. Lease absence by itself never selects the exception. That embedded
  server explicitly grants its
  local operator; an OIDC daemon grants only exact operator-tier
  `storage_management.principals` issuer/subject pairs. Ordinary authenticated tenants,
  unlisted system principals, anonymous remote callers, and project/request-supplied
  identity data never grant authority, and denial occurs before backend inspection.
  The authenticated cleanup API runs a non-destructive dry run over the store-wide generation after the explicit
  management-authority gate. On a shared store, lease status is sampled at the planning instant
  through sequential bounded trial acquire/immediate-release operations because the lease port has
  no inspect verb; apply makes no future-validity claim and reacquires/revalidates every candidate. It
  returns age/cap candidates oldest-first by `(modified_at ASC, session_id ASC)`,
  protected and eligible kind/state/reason counts, mtimes, and byte estimates—never transcript,
  tool arguments, paths, secrets, or foreign-owner rows. Automatic sweeps and manual
  plans call the same planner. Unknown/invalid/corrupt/running/awaiting/live/leased
  records are protected and excluded from cap slots. Apply accepts only a signed
  caller/scope/generation/policy-bound token, rejects stale generations explicitly,
  then revalidates owner/kind/state/liveness under run-entry serialization and a
  maintenance lease before `port.ConditionalPrunableStore` takes its family lock,
  compares the exact durable metadata again, and keeps all exclusions held while
  deleting sidecars before the snapshot. Partial failures use stable sanitized codes and remain
  retryable; unsupported backends report unsupported, not zero impact. Shared health
  tracks retention and cleanup independently, renders concurrent active kinds with
  counts instead of last-writer-wins, clears each terminal job independently, and
  retains only stable sanitized last-failure text. Cursors retain neutral ordering and
  bind the catalog fingerprint generation and ownership/filter scope; the backend
  token itself is issued and validated only by the pager. A stale, mismatched, or
  foreign cursor returns `port.ErrSessionMetadataCursorRestart`, requiring page-one restart
  rather than mixing generations or owner scopes. Ready-state inventory checks an
  O(1) source stamp from the authoritative `sid-v1` snapshot directory before reading the
  catalog; the catalog's private child directory keeps its own replacements out of
  that stamp. Missing, corrupt, or stale catalogs rebuild from current metadata
  headers; a fresh directory fingerprint
  before and after rebuild detects concurrent and other-`Store` family changes instead
  of trusting process-local state. Catalog rebuild/publication uses a dedicated
  process mutex plus a stable cross-process catalog flock; it never holds a
  store-wide session-operation lock, so a blocked rebuild cannot delay another
  family's Save, Load, event append, or tool audit. Obsolete generations and
  interrupted catalog temporaries are reconciled only under that catalog lock.
  The catalog is derivative: snapshots remain the
  sole transcript authority, and `port.SessionStore` is unchanged. Redisstore follows
  the same projection contract with atomically co-written snapshot and metadata rows
  in global and owner-specific lexicographic indexes. Its pager performs one direct
  exclusive-cursor range read of at most the limit plus one lookahead and never loads
  snapshot blobs. Save and Delete atomically update the snapshot, index membership,
  owner-scoped generations, and existing event/tool sidecar lifecycle. Redisstore
  uses the canonical `mecatl:session:`, `mecatl:events:`, `mecatl:events-gen:`,
  `mecatl:tools:`, metadata, lineage, and ledger keys. Incompatible values at
  those keys fail validation; there is no inventory scan, adoption job, or
  migration path.
  For jsonlstore, Save, Delete,
  EventLog.Append, and ToolCall all take the same stable per-family flock identity;
  sidecar-first/snapshot-last deletion therefore cannot race a same-family append,
  while unrelated families proceed independently. Snapshot replacement holds that
  owner-only flock from inactive-temp recovery through same-directory write, file
  sync, atomic rename, and directory sync. EventLog strict append
  requires both file and directory sync. ToolCall instead attempts every available sync
  and may leave an unsynced or partially synced best-effort audit record. Event and tool
  sidecars use the same
  flock across cooperating jsonlstore processes: newline is their commit marker,
  append opens the canonical sidecar through an `os.Root`, rejects non-regular
  entries without blocking on FIFOs, truncates only an unterminated EOF fragment,
  then writes and file-syncs one complete record and directory-syncs after every
  successful append. Event reads use confined canonical/store roots, capture a
  bounded newline-terminated prefix under that lock, then yield after releasing it
  while retaining the bounded descriptor/section view until iteration ends; only an
  unterminated final fragment is ignored. A blank, whitespace-only, malformed complete
  or middle record, unknown format, malformed payload, or I/O error fails loudly. The flock
  does not coordinate arbitrary external writers. Replacement temp names carry a random process-owner token and
  monotonic generation; only names that validate against that private protocol are
  cleanup candidates. Startup skips a family whose lock is live, while the next
  successful Save waits for the lock and removes all prior inactive generations
  before creating its own. Thus another process's active temp and the committed
  snapshot are never reaped, and repeated crashes do not accumulate an unbounded
  temp set. The configured `--store-dir` path and every ancestor must be physical,
  non-symlink directories; on macOS use the physical `/private/...` spelling rather
  than a `/var/...` path that traverses the `/var` symlink.
  `Store.SnapshotDurability` exposes those three verified primitives: an unsupported
  sync primitive is reported as weaker snapshot durability rather than overclaiming
  host-crash safety. That claim also depends on an underlying filesystem/storage stack
  that honors successful sync and atomic rename; the probe verifies syscall support,
  not media persistence or whether volatile storage such as tmpfs survives power loss.
  Snapshot Save retains the weaker-capability behavior for an existing canonical family;
  EventLog append is unavailable without both file and directory sync, and Delete,
  retention, and destructive/move operations fail closed without directory sync.
  ToolCall appends within the current namespace, attempts every available sync,
  and may leave an unsynced or partially synced best-effort record. Sidecars are
  opened through validated no-follow descriptors at `0600`. Failures before rename
  preserve the prior snapshot; a failure after rename is loud while the new
  snapshot remains authoritative. The logical session id is an opaque valid-UTF-8
  string stored inside each snapshot; the bounded hash-suffixed `sid-v1-` filename
  token is not an operator API. Reads and discovery inspect only the owner-only
  `sid-v1/` directory; malformed current artifacts fail closed. Distinct
  root-level artifacts remain untouched and cannot poison current inventory.
  All serialize via **`sessnap`** (`engine/adapter/sessnap`): a `Snapshot` DTO
  that round-trips a `Session` by driving the public state machine on restore
  (so a session saved mid-`awaiting` reloads with its pending ask intact). It
  captures the terminal reason via `RecordedStopReason()` for exact round-trips,
  and (cloud-native Phase 1) the per-session profile, the opaque provider/model
  selector pair, the title/provenance and title-generation metadata, and the
  canonical auxiliary `token_usage` ledger. A restarted process rebuilds the SAME
  engine and the `MaxRunTokens` budget continues across restart (see `docs/adr/0027-cloud-native.md`).
  A store may additionally implement the optional **`port.PrunableStore`**
  (`List`/`Delete`; `ErrPruneUnsupported` otherwise) — the retention MECHANISM.
  Automatic retention consumes `SessionMetadataPager`, and stale-session
  reconciliation consumes the catalog-backed `MetaList`; neither reloads full
  conversations for discovery. Candidate deletion/settlement still rechecks the
  existing durable state, process liveness, and lease protections after discovery.
  The POLICY lives in composition (`internal/app/childgc.go`) and all automatic
  and manual selection uses `internal/sessionretention/planner.go`. The strict
  operator-only `retention.version: 1` settings block exposes main/child/scheduled
  age and count limits plus sweep cadence; explicit legacy CLI flags win. Zero
  disables a limit, invalid/negative/unknown config fails, and project-tier
  retention is ignored. Main deletion defaults off and requires an explicit
  acknowledgement after the planner summary is logged. Durable unknown/invalid
  taxonomy, running/awaiting, live, and leased rows remain protected. Under caller
  ownership, ownerless rows are filtered before retention planning or mutation. The effective
  secret-free `retention/v1` policy and bounded ownerless session/schedule cutover inventory
  are projected by authenticated storage health.
- **EventLog** (`port.EventLog`, cloud-native Phase 3) — a DURABLE per-session
  event timeline, SEPARATE from `EventSink` (the sink mirrors live; the log is
  storage a later consumer reads back). **The loop never calls it** — persistence
  lives at the relay (`internal/adapter/server`), which appends every observed
  event (incl. the post-disconnect tail and `EvApproval`/`EvCompactionArchive`,
  which are skipped on the client wire) on a cancel-detached context so a dead
  client can't abort the durable write. Clients remain chunk-streamed. The run-scoped
  durable recorder coalesces message and reasoning separately into UTF-8-safe chunks capped
  at 1 MiB, conservatively below jsonlstore's 16 MiB record limit under worst-case JSON
  escaping. Typical turns still produce one record of each kind; oversized turns produce
  the minimum bounded number. A non-delta boundary flushes pending chunks before its own
  append. Every chunk and boundary is attempted exactly once and cleared regardless of
  error because EventLog errors can follow a durable write; retry would duplicate folded
  text. Memory remains bounded, one warning is emitted per recorder, and later boundary/result
  appends continue. A process crash or failed append can lose a chunk; the completed snapshot
  remains authoritative. See [ADR 0243](../adr/0243-jsonl-durability.md). `jsonlstore` triples as
  `SessionStore`+`ToolCallRecorder`+`EventLog` (a `.events.jsonl` sidecar);
  memstore has an in-memory sibling; `grpcdriver` carries the remote
  `EventLogService` (`--event-log-url`, independent of the session store). The
  log also records the log-only **`EvUserPrompt`** and out-of-band
  **`EvSessionTitle`** events. The append boundary stamps `Event.Actor` from the
  verified request-context principal, not the session owner. User prompts and
  compaction archives contain conversation content; delegation-preview redaction
  is not blanket secret or PII filtering. A title event follows a successful snapshot save and
  carries only title lifecycle metadata; it contains no title-source prompts or
  provider errors. It is also offered best-effort to gRPC live-session subscribers,
  while HTTP clients reconcile it through the authoritative snapshot or durable
  event stream. The `EvUserPrompt` records every user-role turn (the genuine prompt
  plus the harness's synthetic continuations), skipped on the client wire — so that a fold can reconstruct what the user asked. **Three**
  consumers, all in **composition** (never the loop): the non-destructive
  **compaction archive**; the **permstore verdict-replay**
  (`internal/app/approvalreplay.go`) that re-derives learned allow-always rules
  after a restart so a previously-approved tool does not re-ask; and (#115)
  **event-sourced `SessionStore.Load`** — a host whose system of record is the
  event log folds `EventLog` + `SessionMeta` → `*session.Session` via
  `engine/adapter/eventsource.Fold`. The out-of-band metadata includes the durable,
  validated session kind/relationship; absent legacy kind metadata restores as
  fail-closed `unknown`. That third consumer carries a documented
  **replay-fidelity limitation**: the opaque assistant-replay fields
  (`Message.Reasoning`/`ProviderPhase`, `ToolCall.ItemID`) are not on the stream,
  so a pure fold is byte-identical-replay faithful only for plain-chat providers
  (ADR 0038). See `docs/adr/0027-cloud-native.md` Phase 3 and the
  `eventlogconformance` suite.

  A dedicated debug session is a fourth bounded consumer of the same durable facts. Its
  target-bound `InspectSession` projections expose compaction archives, content-free request
  manifests, sanitized network attempts, typed delegation, and latest-run versus lifetime
  counters without widening ordinary client streams. Lineage comes from a bounded
  `SessionLineageReader` plus typed-event fallback; retained descendants receive opaque
  incarnation-bound, revalidated handles, while pruned snapshots remain content-free
  tombstones keyed separately from a later same-ID incarnation. Event-log
  retention and scan limits are reported rather than inferred. See
  [ADR 0256](../adr/0256-session-debugger-evidence-and-reporting.md).

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
  (`coordination.k8s.io` Lease, `--session-lease-k8s-namespace`, in-cluster).
  Explicit backends win; every local JSONL StoreDir otherwise auto-wires
  `flocklease` beneath the store root, then composition falls back to a
  store-provided lease (`internal/app` (`buildSessionLease`)). Other stores remain
  nil/single-writer-by-affinity. The
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
`tool.MemoryStore`). Both services require successful bounded capability
negotiation with the exact current base-contract marker before composition; an
old peer, an empty response, timeout, or transport failure fails construction.
Memory's wire surface directly mirrors the mandatory lifecycle/CAS port:
Remember carries the complete expected presence/version, Inspect returns bounded
history, and Forget/Undo require exact opaque versions. There is no unconditional
write/delete RPC and no fallback to an older memory contract. Session capability
bits remain honest for genuinely optional backend operations such as listing,
metadata paging, deletion, lineage, create, and activity projection. Metadata
continuation cursors are accepted only when key, generation, ownership scope, and
opaque continuation are all present. Remote driver backing stores own their
namespace; the harness neither scans old driver artifacts nor guesses formats from
opaque payload bytes.
The durable **schedule registry** has the same seam:
`ScheduleStoreService` + `ScheduleOneShotReArmerService` back
`port.ScheduleStore` + `port.ScheduleOneShotReArmer` (`--schedule-store-url`,
INDEPENDENT of the session store — replaces the `ScheduleStore()` discovery;
the driver's `Claim`/`ClaimNow`/`ReArmOneShot` run the at-most-once atomic
advance server-side). Selection is composition-only (`app.Build`):
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
`--skills-dir`/`--skills-conventional`). Driver skills remain path-free: activation
loads the body and logical inventory, and `Skill({name, asset})` fetches one bounded
textual payload on demand. No temp cache, materialization, executable-bit application,
or workspace read root is created. `--soul-source-url` occupies the USER slot
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
| `sourceconformance.RunSkillSource` | `skills.FSSource` over a written-out fixture tree | `engine/adapter/skillfs/conformance_test.go` |
| `sourceconformance.RunSkillSource` | grpcdriver → bufconn → `NewSkillSourceServer(NewFixtureSource)` | `internal/adapter/grpcdriver/conformance_test.go` |
| `sourceconformance.RunSoulSource` | `soul.Store` (temp file) | `internal/adapter/soul/conformance_test.go` |
| `sourceconformance.RunSoulSource` | grpcdriver → bufconn → `NewSoulSourceServer(verbatim fake)` | `internal/adapter/grpcdriver/conformance_test.go` |
| `sourceconformance.RunAgentSource` | in-memory `NewAgentFixtureSource` (self-test) | `engine/adapter/sourceconformance/sourceconformance_selftest_test.go` |
| `sourceconformance.RunAgentSource` | `agents.FSSource` over a written-out fixture tree | `engine/adapter/agentfs/conformance_test.go` |
| `sourceconformance.RunAgentSource` | grpcdriver → bufconn → `NewAgentSourceServer(NewAgentFixtureSource)` | `internal/adapter/grpcdriver/conformance_test.go` |
| `sourceconformance.RunCommandSource` | in-memory `NewCommandFixtureSource` (self-test) | `engine/adapter/sourceconformance/sourceconformance_selftest_test.go` |
| `sourceconformance.RunCommandSource` | grpcdriver → bufconn → `NewCommandSourceServer(NewCommandFixtureSource)` | `internal/adapter/grpcdriver/conformance_test.go` |

(Deliberately NO filesystem row for commands: `prompt.DirCommandExpander` is
the workspace-tier surface — live, workspace-relative, read through the
`tool.Workspace` port — NOT a `CommandSource` implementation.)

## Reliability — provider resilience

The `port.LLMProvider` seam is wrapped by `llmresilience.Wrap(inner, Config)`, so recovery is server-owned and the agent loop does not replay a model step itself. The decorator retries transient failures before the step reaches its semantic commit boundary, with exponential backoff, provider retry hints, and a circuit breaker. It is configured with `--llm-max-attempts`, `--llm-recovery-budget`, `--llm-per-attempt-timeout`, `--llm-stream-idle-timeout`, `--llm-breaker-threshold`, and `--llm-breaker-cooldown`.

The semantic commit boundary is the first text delta that makes accumulated text non-whitespace, or a clean `ChunkDone`. Before that boundary, reasoning, provider metadata, whitespace-only text, tool calls, and usage remain tentative. A retryable failure discards those tentative chunks and can retry the same model step; their usage remains accounted for. Once meaningful text is visible, a failure is terminal and the decorator never re-issues the call. A clean completion also commits pure-tool-call and whitespace-only turns, so completed tools are never rerun.

`--llm-recovery-budget` starts at the first retryable failure or breaker rejection for each precommit model step, rather than at task start. It bounds retry waits and breaker admission; a distinct step receives a new budget. A value of `0` disables additional waiting and never shortens a provider retry hint. The caller's shorter deadline still wins, and token budgets are checked between turns rather than between retries. The breaker opens after its configured number of consecutive transient establishment failures, admits one half-open probe after its cooldown, and resets only after clean completion.

Establishment ends on the first raw chunk, independently of semantic progress. `--llm-per-attempt-timeout` applies until then. Afterwards, `--llm-stream-idle-timeout` bounds gaps between raw chunks. An idle stall is retryable only while the step is still precommit; after semantic output it is terminal. Diagnostics record recovery activity without prompt text.

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

## Prerequisites

- [The ports](ports.md) — the ports being instrumented and persisted.

## Follow-on reading

- [Deployment & server hardening](deployment-and-hardening.md)

## Related

- [The API surface it instruments](api-surface.md)
- [Providers — provider resilience](providers.md)

---

[← Architecture guide](../architecture.md)
