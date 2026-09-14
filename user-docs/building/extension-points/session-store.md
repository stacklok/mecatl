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

`Save` replaces the current snapshot for an ID and is idempotent for the same
session state. The stored value must not alias the caller's live session.

`Load` returns an independent, live `*session.Session` with valid lifecycle
state. A missing session must wrap `port.ErrSessionNotFound`:

```go
sess, err := store.Load(ctx, id)
if errors.Is(err, port.ErrSessionNotFound) {
    // No prior state.
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

`Create` publishes only when the ID is absent. The check and write must be
atomic across processes. A collision wraps `port.ErrSessionAlreadyExists` and
must not change any existing snapshot, metadata, event log, or audit sidecar.
Use `Save` only after creation.

Caller ownership enforcement requires `SessionCreator`. The current gRPC store
driver does not provide it, so `--session-store-url` cannot be combined with
OIDC ownership enforcement.

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

JSONL and Redis stores implement the optional `port.SessionMigrationStore`. Its
contract is:

- Planning reports current, target, invalid, and skipped records plus bounded
  storage estimates.
- Apply processes bounded batches and returns a durable job handle that can
  resume after restart.
- Cross-process exclusion prevents concurrent resumes from replaying a batch.
- Cancel waits for a committed batch and is final. Completed batches remain
  committed.
- Migration preserves the snapshot, owner, durable kind, modification time,
  event log, and tool audit.
- Corrupt records are reported and retained. Paging and cleanup remain
  unavailable until the backend reaches a verified ready state.
- Concurrent saves or deletes invalidate stale inventory. Errors expose stable,
  sanitized reason codes.

Stores without this interface advertise migration as unsupported.

### Authenticated cleanup planning

Manual cleanup uses a non-destructive, owner-scoped plan. It reports counts,
reasons, modification times, and byte estimates without exposing transcripts,
tool arguments, paths, credentials, or other owners' records. Running, awaiting,
live, leased, corrupt, invalid, and unknown sessions are protected.

Apply requires the plan's opaque confirmation token, which binds the caller,
scope, inventory generation, and policy version. Each candidate is revalidated
under the maintenance lease before sidecars and then the snapshot are deleted.
Remote and multi-writer deployments must provide `port.SessionLease`; otherwise
cleanup and migration fail closed. Retry partial failures by creating a new
plan.

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

`Append` returns nil only after the event reaches stable storage or the backing
service commits it. Callers attempt each event once because an error can arrive
after a successful write. A failed append can leave a log gap; the completed
session snapshot remains authoritative.

The supplied relay records UTF-8-safe message and reasoning chunks of at most 1
MiB. It attempts each chunk once, warns on failure, and continues with later
boundary and result events.

`Read` returns events in append order as a lazy
`iter.Seq2[session.Event, error]`. Streaming avoids loading a long log into one
response. Implementations must follow these rules:

- A miss (no log recorded for `id`) yields an **empty sequence, not an error**.
  Absence is data.
- On an infrastructure fault, `Read` yields `(session.Event{}, err)` and then
  stops — no further events after an error.
- An implementation must release any resource it opened (file handle, stream
  connection) when the consumer breaks out of the `range` early, exactly like a
  well-behaved `iter.Seq2`.

Implementations must support concurrent `Append` and `Read` operations across
session IDs. The server serializes one session's appends through its relay.

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

The loop emits events; the relay appends them to `EventLog`.

---

## The three reference backends

|Backend|Package|Use|
|-|-|-|
|In-memory|`engine/adapter/memstore`|Tests and non-persistent single-process deployments|
|JSONL|`internal/adapter/store/jsonlstore`|Persistent `mecated` deployments|
|Redis|`internal/adapter/redisstore`|Persistent `mecak8s` deployments|

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

The suite covers snapshot isolation, lifecycle states, multi-session keying,
not-found wrapping, and replacement. It does not require a wire format or file
layout.

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

The event-log suite covers append order, empty misses, session isolation, large
events, streaming boundaries, early-break cleanup, and concurrent access under
`-race`. The supplied JSONL and Redis stores pass these same suites.

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
