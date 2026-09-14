---
sidebar_position: 3
title: SessionStore & EventLog
description:
  Implement session snapshots and append-only event logs through independent
  storage ports.
---

# SessionStore & EventLog

Mecatl exposes two persistence ports: `port.SessionStore` and `port.EventLog`.
Both are Go interfaces in `engine/port/` and are supplied through composition.

`SessionStore` saves the current session snapshot and restores a live
`*session.Session`. `EventLog` appends loop events in chronological order and
streams them through `Read`. The ports are independent and can use different
backends.

---

## SessionStore

```go
// engine/port/store.go

type SessionStore interface {
    Save(ctx context.Context, s *session.Session) error
    Load(ctx context.Context, id session.SessionID) (*session.Session, error)
}
```

**`Save`** serializes the session's current state to stable storage and
overwrites any prior snapshot for the same id. The call is idempotent over the
session's state: calling `Save` twice on the same session object is safe — the
second call replaces the first. Implementations must deep-copy the session at
`Save` time so that mutations the caller makes afterward do not silently corrupt
the stored snapshot.

**`Load`** deserializes the snapshot stored under `id` and returns a live
`*session.Session`. The aggregate's invariants hold on the returned session —
the state machine is driven through the correct lifecycle transitions during
restore, not patched directly. If no session exists under `id`, `Load` returns
an error that wraps the port-level sentinel `port.ErrSessionNotFound`, so
callers that cannot import the adapter can distinguish "no such session" from an
infrastructure failure via `errors.Is`:

```go
sess, err := store.Load(ctx, id)
if errors.Is(err, port.ErrSessionNotFound) {
    // first run — no prior state
}
```

### SessionCreator — atomic first publication

Stores used with caller ownership enforcement must also implement the additive
`port.SessionCreator` capability:

```go
type SessionCreator interface {
    Create(ctx context.Context, s *session.Session) error
}
```

`Create` publishes the first authoritative snapshot only when the session ID is
absent. The check and publication must be backend-atomic across processes; a
collision wraps `port.ErrSessionAlreadyExists` and leaves the existing snapshot,
metadata, event log, and tool-call sidecar unchanged. `Save` remains the update
operation after a successful create. Mecatl refuses ownership-enforced
composition with a store that lacks this capability. The current gRPC
session-store driver does not advertise atomic create, so `--session-store-url`
cannot be combined with OIDC ownership enforcement; use an in-tree atomic
backend until the driver protocol adds that operation.

### PrunableStore — the optional retention seam

`SessionStore` is intentionally minimal. The retention seam is a **separate,
optional** interface discovered by type assertion:

```go
type PrunableStore interface {
    List(ctx context.Context) ([]StoredSession, error)
    Delete(ctx context.Context, id session.SessionID) error
}
```

**`List`** returns every stored session's id and last-save time in no guaranteed
order. It applies no filtering — retention policy (which ids are prunable, age
thresholds, per-family caps) is entirely the caller's concern.

**`Delete`** removes the snapshot for `id` plus any sidecars the adapter keeps
(such as a tool-call log). It is idempotent: deleting an unknown id is success.

A store that does not implement `PrunableStore` is silently skipped by the
composition-layer child-session GC — no error, no sweep. A store whose backend
cannot enumerate or delete sessions should return an error wrapping
`port.ErrPruneUnsupported`; the composition layer treats that sentinel as a
permanent signal and disables further sweeps rather than retrying.

### Bounded storage health

A backend may also implement `port.SessionStorageHealthProvider`. The management
endpoint advertises this capability only when an authorizer is wired, and
returns aggregate bytes plus format/kind/corruption counts from an existing
metadata index and cheap object/file metadata—never by loading transcripts.
Availability flags separate a real zero from unsupported or not-yet-measured
data. Jsonlstore supports this view; memstore and remote backends that do not
implement the seam report the feature as unsupported. The view is status-only:
it does not run cleanup or migration.

### Resumable v1-to-v2 migration

Jsonlstore and Redis implement the optional `port.SessionMigrationStore`
management capability. For jsonlstore, an authenticated plan reports
v1/v2/invalid/skipped families, current and estimated reclaimable bytes, and the
maximum temporary space for one family. Apply processes a bounded batch and
returns a durable job handle; use resume to process later batches or continue
after a server restart. Each job's complete load-to-checkpoint drive is
protected by a stable cross-process job exclusion, so overlapping resumes cannot
replay a batch or regress counters. A stale concurrent resume returns conflict.
Cancel waits for any committed in-flight batch, then becomes monotonic: later
resume attempts conflict and cannot restore the running state. Already-migrated
families remain committed.

Migration preserves the complete snapshot, owner, durable kind (including
`unknown`), logical modification time, and tool/event sidecars. For jsonlstore
it acquires the ordinary run-entry lease and the family's cross-process lock,
rereads and verifies v2 before removing v1, and reports corrupt/torn records
without discarding them. Redis uses the same authenticated, durable, bounded job
to adopt the derivative metadata index on upgrade: inventory and cleanup remain
unavailable while stale; stable inspection verifies that every valid snapshot
has its exact global/owner index row and makes missing or bad coverage a repair
candidate; each repair and final publication atomically verifies the job's exact
lock token before any write. Concurrent Save/Delete advances the source
generation, and `ready` is published only after clean per-snapshot coverage plus
the final constant-work cardinality check. Invalid snapshots complete the job
with failures while keeping paging unavailable; repair or remove them, then
create a fresh plan/job. Job errors expose stable reason codes and sanitized
text only. Memstore and remote stores advertise migration as unsupported rather
than returning fabricated zero counts.

### Authenticated cleanup planning

The server exposes one retention planner to both automatic sweeps and
authenticated manual cleanup. A manual dry-run is non-destructive and
owner-scoped. On a shared store, it samples cross-process lease status at the
planning instant with sequential bounded trial acquire/immediate-release calls;
this does not promise that a candidate remains idle for apply, which always
reacquires and revalidates. It reports only durable kind/state counts, age or
cap reasons, modification times, and byte estimates; transcript, tool arguments,
paths, credentials, and foreign-owner rows are never projected. Unknown,
invalid, corrupt, running, awaiting, live, and leased sessions are protected and
do not consume count-cap slots.

Apply requires the opaque confirmation token returned by the dry-run. The token
binds the caller, exact kind scope, inventory generation, and effective policy
version. A changed catalog or policy returns a stale-plan result without
deleting anything. Cleanup-capable backends implement
`port.ConditionalPrunableStore`: each candidate is revalidated under run-entry
serialization and the maintenance lease, then the backend holds its family
mutation exclusion across a final metadata comparison and
sidecar-first/snapshot-last deletion. Remotely reachable and multi-writer
composition must provide a working `port.SessionLease`; missing or
backend-unsupported leasing suppresses migration/cleanup capability
advertisement and fails apply closed. Only private embedded mecatui explicitly
proves the local single-process posture that may substitute process-local
`IsLive` plus family locking. Partial failures use stable, sanitized reason
codes and can be retried by planning again. Unsupported stores report
`backend_unsupported`; they never claim zero impact.

### Snapshot mechanics via sessnap

The snapshot format is defined in `engine/adapter/sessnap`. The
`sessnap.Snapshot` struct is a stable JSON DTO that the store adapters share:

- Conversation messages are serialized through `messageDTO` (explicit JSON tags,
  stable against field reordering in the domain type).
- The session lifecycle state is round-tripped through `sessnap.RestoreState`,
  which drives the session aggregate through the correct state-machine
  transitions on load — `Complete`, `Stop`, `Cancel`, `Fail`, or
  `PauseForApproval` — so the restored aggregate satisfies all invariants.
- Provider-private replay fields (`Message.Reasoning`, `Message.ProviderPhase`,
  `ToolCall.ItemID`) are captured in the snapshot and survive restart.
- Cumulative `Usage` (the budget brake's input), the `Profile`, `ProviderID`,
  `ModelID`, and `ReasoningEffort` labels are persisted so a restarted process
  can re-derive the exact same per-session engine.

---

## EventLog

```go
// engine/port/eventlog.go

type EventLog interface {
    Append(ctx context.Context, id session.SessionID, ev session.Event) error
    Read(ctx context.Context, id session.SessionID) iter.Seq2[session.Event, error]
}
```

**`Append`** durably records `ev` under the session id. The durability
obligation is strict: `Append` must return nil only after the record is on
stable storage or committed to the backing service. For local jsonlstore that
means both the file and its directory have synced; without either capability
append fails. An implementation that buffers without guaranteeing durability
violates the contract. Callers must attempt each event at most once because an
error may arrive after the write committed. The relay uses a cancel-detached
context so a dead client's cancellation cannot abort it. Clients still receive
every original chunk, while the run-scoped recorder coalesces message and
reasoning into UTF-8-safe chunks capped at 1 MiB—small enough for the JSONL
reader under worst-case escaping. Normal turns use one record per present kind;
oversized turns use the minimum bounded count. Every chunk is cleared after its
single attempt, append failure warns once per recorder, and later
boundary/result events continue. A crash or failed append can leave a log gap;
the completed session snapshot remains authoritative.

**`Read`** returns the session's events in append order as a lazy
`iter.Seq2[session.Event, error]`, following the same streaming idiom as
`port.LLMProvider.Stream`. Streaming matters: a long session log can exceed a
gRPC message limit if read as a single unary response; `Read` maps 1:1 to a
server-streaming RPC and avoids the size cap. Key contract points:

- A miss (no log recorded for `id`) yields an **empty sequence, not an error**.
  Absence is data.
- On an infrastructure fault, `Read` yields `(session.Event{}, err)` and then
  stops — no further events after an error.
- An implementation must release any resource it opened (file handle, stream
  connection) when the consumer breaks out of the `range` early, exactly like a
  well-behaved `iter.Seq2`.

**Concurrency contract.** Implementations must be safe for concurrent `Append`
and `Read` across session ids — the server shares one `EventLog` across all
relay goroutines. Per-id append order is only well-defined for a single
session's appends (the server serializes those through one relay loop per run).

### EventLog vs EventSink

These are separate seams and must not be confused:

||`port.EventSink`|`port.EventLog`|
|-|-|-|
|Purpose|Live telemetry mirror|Durable chronological record|
|Direction|One-way write|Write + read-back|
|Durability|None guaranteed (fire-and-forget)|Strict (nil means on stable storage)|
|Consumer|Metrics, ACP, TUI relay|Restart rehydration, approval replay, Phase 3 consumers|
|Where it lives|`engine/port/`|`engine/port/`|
|Loop awareness|Loop emits; relay mirrors to sinks|Loop is storage-agnostic; relay calls `Append`|

The loop never imports `port.EventLog` or calls `Append`. Persistence is a relay
concern — it lives in `internal/adapter/server`.

---

## The three reference backends

|Backend|Package|Notes|
|-|-|-|
|In-memory|`engine/adapter/memstore`|Default; test/single-process; implements `SessionStore` + `PrunableStore` + `EventLog` as siblings|
|JSONL on disk|`internal/adapter/store/jsonlstore`|Default for `mecated`; triples as `SessionStore` + `EventLog` + `ToolCallRecorder`. The configured path and every ancestor must be physical non-symlink directories (use macOS `/private/...`, not a `/var/...` symlink path). Existing canonical snapshot Save may retain weaker capability; first legacy-family Save, EventLog append, Delete, retention, and migration fail closed when their required sync is unavailable. ToolCall audit is best-effort and may be unsynced or partially synced. Capability probes establish syscall support, not media persistence.|
|Redis|`internal/adapter/redisstore`|Used by `mecak8s`; validated by conformance suites over miniredis|

### engine/adapter/memstore — in-memory

`memstore.Store` implements `port.SessionStore` and `port.PrunableStore`. It
stores sessions as `sessnap.Snapshot` values in a mutex-guarded map. Save
round-trips through `sessnap.Of` / `sessnap.Restore`, so the stored copy is
deep-isolated from both the caller's live session and from subsequently loaded
copies — mutations to either side do not alias the store.

`memstore.EventLog` is the companion in-memory event log. It stores events by
value (no serialization round-trip) under a mutex. Both are wired together in
composition when no `--store-dir` flag is given.

Use this backend for unit tests (it is the reference the conformance suites
validate first) and for single-process, non-persistent deployments.

```go
import "github.com/stacklok/mecatl/engine/adapter/memstore"

store := memstore.New()
log   := memstore.NewEventLog()
```

### internal/adapter/store/jsonlstore — JSONL on disk

The default backend for `mecated`. The same package triples as
`port.SessionStore`, `port.EventLog`, and `port.ToolCallRecorder`.

Each session owns an authoritative v2 snapshot and two append-only sidecars
under a `sid-v1` subdirectory, sharing one stem:

```text
<store-dir>/sid-v1/sid-v1-<token>.session.json      authoritative v2 current snapshot
<store-dir>/sid-v1/sid-v1-<token>.tools.jsonl       one record per tool call
<store-dir>/sid-v1/sid-v1-<token>.events.jsonl      one record per relayed event (eventlog-json/1)
```

Historical v1 `.session.jsonl` files remain readable and are lazily promoted on
a later write. Do not infer the session id from a filename or edit these files;
the store is plaintext and its files, including sidecars, are owner-only
(`0600`).

Snapshot replacement uses a same-directory temporary file, full write, file sync
where supported, atomic replacement, and directory sync where supported.
`SnapshotDurability` reports the available primitives: only all three, on an
underlying filesystem/storage stack that honors successful sync and atomic
rename, make a successful save host-crash safe. The probe verifies syscall
support; it does not make volatile storage such as tmpfs survive power loss.
Event records are newline-committed and append fails closed when required
directory sync is unavailable. Tool-call audit is best-effort and may drop. An
interrupted final fragment is ignored or replaced on the next append, while a
blank, whitespace-only, or otherwise malformed complete record fails loudly. A
stable family flock coordinates cooperating jsonlstore processes, not arbitrary
external writers. Quiesce every writer before copying the complete store
directory for backup or restore.

Select it with `--store-dir <path>`. The directory is created if it does not
exist, owner-only (`0700`). A store written by an older version keeps its files
directly under `<store-dir>`; those are read as-is and moved into `sid-v1/` the
next time that session is written.

### internal/adapter/redisstore — Redis-backed

The backend for `mecak8s` (the storage-free, Kubernetes-native composition
root). Implements `port.SessionStore`, `port.EventLog`, `port.PrunableStore`,
`port.SessionMigrationStore`, and `port.ToolCallRecorder` over a Redis
connection. Existing snapshot databases without the derivative metadata index
remain loadable but report paging and retention unsupported until an
authenticated storage-migration job adopts every row and atomically publishes
the index. Select it via `--redis-url` (mecak8s only). Validated by the same
conformance suites as jsonlstore, running over miniredis.

---

## Event-sourced Load

Mecatl's own adapters use the snapshot model: `Save` writes a full snapshot;
`Load` deserializes it. But a host whose system of record is an append-only
event log can implement `port.SessionStore.Load` by **folding** the event stream
into a `*session.Session` instead.

The reference implementation is `engine/adapter/eventsource.Fold`:

```go
// engine/adapter/eventsource

func Fold(meta SessionMeta, events iter.Seq2[session.Event, error]) (*session.Session, error)
```

`Fold` walks the event stream and reconstructs the session's conversation,
lifecycle state, counters, cumulative usage, and pending ask. `SessionMeta`
carries the creation facts that no event records — id, mode, limits, workspace,
profile, provider/model selector, reasoning-effort, and createdAt — which the
host supplies out-of-band.

### Field-by-field reconstruction contract

The table below is derived from `engine/COMPATIBILITY.md` ("Session
reconstruction contract"):

|Field|Round-trip obligation|Event source|
|-|-|-|
|`Conversation` (user prompts, assistant text, tool calls, tool results — pairing-valid)|**MUST**|`EvUserPrompt` (genuine prompt + harness continuations), `EvMessageDelta`, `EvToolCall`, `EvToolResult`; pre-compaction head from `EvCompactionArchive`|
|`State` (idle / running / awaiting / completed / failed / cancelled)|**MUST**|derived from terminal `EvResult.Stop`; trailing unanswered `EvPermissionAsk` → awaiting; no terminal → idle|
|Recorded stop reason (`RecordedStopReason`)|**MUST**|`EvResult.Stop`|
|Pending ask (`PendingAsk`, when awaiting)|**MUST**|trailing `EvPermissionAsk` with no following `EvApproval` or `EvResult`|
|Cumulative `Usage`|**MUST**|**SUM** of every per-run `EvResult.Usage` (each is per-run; the budget brake reads the cumulative aggregate)|
|Creation metadata (id, mode, limits, workspace, profile, provider/model selector, reasoning-effort, createdAt)|**MUST** — supplied out-of-band|**Not in any event** — provided via `eventsource.SessionMeta`|
|`Counters` (turns / tool calls / consecutive failures)|run-scoped — latest run segment only|`EvTurnStart` (turns), `EvToolResult` (tool calls / consecutive failures)|
|Run plumbing (diagnostics binding, askID serials)|safe to lose — rebuilt fresh|n/a|

### Replay-fidelity limitation

A fold reconstructs the structural conversation faithfully and is
byte-identical-replay-faithful **only for providers that do not use the
provider-private opaque replay fields**. Three fields reach the conversation
only via `Session.RecordAssistant` in the loop and are never emitted on the
event stream:

- `Message.Reasoning` — the provider reasoning replay blob (OpenAI encrypted
  reasoning content; Anthropic `(thinking, signature)` pair)
- `Message.ProviderPhase` — the OpenAI Responses phase marker
- `ToolCall.ItemID` — the provider-assigned item id

`EvReasoningDelta` carries a human-readable reasoning _summary_, which the loop
deliberately never places on `Message.Reasoning` — a fold must not do so either.

For plain-chat providers (including `mockllm`) those fields are empty and the
fold is byte-identical. For reasoning providers (OpenAI, Anthropic) the fold
produces a structurally correct but not byte-identical conversation. This is why
Mecatl's own resume uses the snapshot; the fold is for event-log-SoR hosts that
accept this boundary or carry those fields in their own richer event schema.

This is a documented contract limitation, not a bug. See
`engine/COMPATIBILITY.md` and ADR 0038.

---

## Conformance suites

Any `SessionStore` implementation — including your own — can be validated
against the shared conformance table in `engine/adapter/storeconformance`:

```go
import "github.com/stacklok/mecatl/engine/adapter/storeconformance"

func TestMyStore(t *testing.T) {
    storeconformance.Run(t, func(t *testing.T) port.SessionStore {
        return mystore.New(t.TempDir())
    })
}
```

If your store also implements `port.PrunableStore`, run the retention suite too:

```go
    storeconformance.RunPrunable(t, func(t *testing.T) port.SessionStore {
        return mystore.New(t.TempDir())
    })
```

The store suite pins the contract — snapshot encoding, isolation (Save and Load
must not alias the caller's pointer), lifecycle-state fidelity for all six
session states, multi-session keying, not-found sentinel wrapping, and overwrite
semantics. It does not assert wire format or file layout.

Similarly, any `EventLog` implementation can be validated against
`engine/adapter/eventlogconformance`:

```go
import "github.com/stacklok/mecatl/engine/adapter/eventlogconformance"

func TestMyEventLog(t *testing.T) {
    eventlogconformance.Run(t, func(t *testing.T) port.EventLog {
        return mylog.New(t.TempDir())
    })
}
```

The event log suite covers: append-then-read order, miss as empty sequence (not
error), distinct-session isolation, no Seq-based reordering (contract is raw
append order), large event round-trip, cumulative log crossing the framing
boundary (the streaming-vs-unary correctness test), early-break resource
release, and concurrent append/read safety under `-race`.

Both suites are the same ones `jsonlstore` and `redisstore` pass — passing them
is the bar for a production-grade implementation.

---

## Wiring

### Direct embed (engine consumer)

Pass your store and event log through `agent.Deps`:

```go
deps := agent.Deps{
    Store:    mystore.New(),
    // EventLog is wired at the relay layer (internal/adapter/server), not in Deps
    // ...other fields
}
eng := agent.NewEngine(deps)
```

The `EventLog` is not a `Deps` field — the loop is storage-agnostic by design
and never imports `port.EventLog`. Wire it in your relay layer, calling
`log.Append` for every event your relay observes before forwarding it to
clients.

### mecated (standalone server)

- **`--store-dir <path>`** — selects the jsonlstore backend (JSONL on disk).
  Both `SessionStore` and `EventLog` are wired from the same store directory.
- No flag — defaults to `memstore` (in-memory, no persistence across restarts).

### mecak8s

- **`--redis-url <url>`** — selects the Redis backend. Both `SessionStore` and
  `EventLog` are wired through the Redis adapter.

---

## What's next

- [LLM provider](llm-provider.md) — implement the `port.LLMProvider` interface
  to plug in a custom model backend.
- [Permission policy](permission-policy.md) — implement `port.PermissionPolicy`
  to replace the built-in rule engine.
- [Hook system](/building/what-you-get/hooks.md) — the `port.HookRunner`
  lifecycle hooks that fire around tool calls.
- [The agent loop](/building/what-you-get/agent-loop.md) — how the loop drives
  turns, emits events, and reaches terminal states.
