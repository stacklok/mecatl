---
sidebar_position: 3
title: SessionStore & EventLog
---

# SessionStore & EventLog

mecatl's persistence layer exposes two ports: `port.SessionStore` and `port.EventLog`. Both live in `engine/port/` and have no dependency on adapters or infrastructure — they are pure Go interfaces the engine consumes through injection.

The two are distinct by design. The store is snapshot-based: each `Save` captures the full current state; `Load` restores a live `*session.Session` from the most recent snapshot. The event log is append-only: the relay records every event the loop emits in chronological order, and `Read` streams them back. Neither depends on the other; they are wired independently in composition.

---

## SessionStore

```go
// engine/port/store.go

type SessionStore interface {
    Save(ctx context.Context, s *session.Session) error
    Load(ctx context.Context, id session.SessionID) (*session.Session, error)
}
```

**`Save`** serializes the session's current state to stable storage and overwrites any prior snapshot for the same id. The call is idempotent over the session's state: calling `Save` twice on the same session object is safe — the second call replaces the first. Implementations must deep-copy the session at `Save` time so that mutations the caller makes afterward do not silently corrupt the stored snapshot.

**`Load`** deserializes the snapshot stored under `id` and returns a live `*session.Session`. The aggregate's invariants hold on the returned session — the state machine is driven through the correct lifecycle transitions during restore, not patched directly. If no session exists under `id`, `Load` returns an error that wraps the port-level sentinel `port.ErrSessionNotFound`, so callers that cannot import the adapter can distinguish "no such session" from an infrastructure failure via `errors.Is`:

```go
sess, err := store.Load(ctx, id)
if errors.Is(err, port.ErrSessionNotFound) {
    // first run — no prior state
}
```

### PrunableStore — the optional retention seam

`SessionStore` is intentionally minimal. The retention seam is a **separate, optional** interface discovered by type assertion:

```go
type PrunableStore interface {
    List(ctx context.Context) ([]StoredSession, error)
    Delete(ctx context.Context, id session.SessionID) error
}
```

**`List`** returns every stored session's id and last-save time in no guaranteed order. It applies no filtering — retention policy (which ids are prunable, age thresholds, per-family caps) is entirely the caller's concern.

**`Delete`** removes the snapshot for `id` plus any sidecars the adapter keeps (such as a tool-call log). It is idempotent: deleting an unknown id is success.

A store that does not implement `PrunableStore` is silently skipped by the composition-layer child-session GC — no error, no sweep. A store whose backend cannot enumerate or delete sessions should return an error wrapping `port.ErrPruneUnsupported`; the composition layer treats that sentinel as a permanent signal and disables further sweeps rather than retrying.

### Snapshot mechanics via sessnap

The snapshot format is defined in `engine/adapter/sessnap`. The `sessnap.Snapshot` struct is a stable JSON DTO that the store adapters share:

- Conversation messages are serialized through `messageDTO` (explicit JSON tags, stable against field reordering in the domain type).
- The session lifecycle state is round-tripped through `sessnap.RestoreState`, which drives the session aggregate through the correct state-machine transitions on load — `Complete`, `Stop`, `Cancel`, `Fail`, or `PauseForApproval` — so the restored aggregate satisfies all invariants.
- Provider-private replay fields (`Message.Reasoning`, `Message.ProviderPhase`, `ToolCall.ItemID`) are captured in the snapshot and survive restart.
- Cumulative `Usage` (the budget brake's input), the `Profile`, `ProviderID`, `ModelID`, and `ReasoningEffort` labels are persisted so a restarted process can re-derive the exact same per-session engine.

---

## EventLog

```go
// engine/port/eventlog.go

type EventLog interface {
    Append(ctx context.Context, id session.SessionID, ev session.Event) error
    Read(ctx context.Context, id session.SessionID) iter.Seq2[session.Event, error]
}
```

**`Append`** durably records `ev` under the session id. The durability obligation is strict: `Append` must return nil only after the record is on stable storage or committed to the backing service. An implementation that buffers without guaranteeing durability violates the contract. The relay calls `Append` for every observed event, on a cancel-detached context, so a dead client's cancelled context cannot abort the durable write; a failure warns and never aborts the live run.

**`Read`** returns the session's events in append order as a lazy `iter.Seq2[session.Event, error]`, following the same streaming idiom as `port.LLMProvider.Stream`. Streaming matters: a long session log can exceed a gRPC message limit if read as a single unary response; `Read` maps 1:1 to a server-streaming RPC and avoids the size cap. Key contract points:

- A miss (no log recorded for `id`) yields an **empty sequence, not an error**. Absence is data.
- On an infrastructure fault, `Read` yields `(session.Event{}, err)` and then stops — no further events after an error.
- An implementation must release any resource it opened (file handle, stream connection) when the consumer breaks out of the `range` early, exactly like a well-behaved `iter.Seq2`.

**Concurrency contract.** Implementations must be safe for concurrent `Append` and `Read` across session ids — the server shares one `EventLog` across all relay goroutines. Per-id append order is only well-defined for a single session's appends (the server serializes those through one relay loop per run).

### EventLog vs EventSink

These are separate seams and must not be confused:

| | `port.EventSink` | `port.EventLog` |
|---|---|---|
| Purpose | Live telemetry mirror | Durable chronological record |
| Direction | One-way write | Write + read-back |
| Durability | None guaranteed (fire-and-forget) | Strict (nil means on stable storage) |
| Consumer | Metrics, ACP, TUI relay | Restart rehydration, approval replay, Phase 3 consumers |
| Where it lives | `engine/port/` | `engine/port/` |
| Loop awareness | Loop emits; relay mirrors to sinks | Loop is storage-agnostic; relay calls `Append` |

The loop never imports `port.EventLog` or calls `Append`. Persistence is a relay concern — it lives in `internal/adapter/server`.

---

## The three reference backends

| Backend | Package | Notes |
|---|---|---|
| In-memory | `engine/adapter/memstore` | Default; test/single-process; implements `SessionStore` + `PrunableStore` + `EventLog` as siblings |
| JSONL on disk | `internal/adapter/store/jsonlstore` | Default for `mecated`; triples as `SessionStore` + `EventLog` + `ToolCallRecorder` |
| Redis | `internal/adapter/redisstore` | Used by `mecak8s`; validated by conformance suites over miniredis |

### engine/adapter/memstore — in-memory

`memstore.Store` implements `port.SessionStore` and `port.PrunableStore`. It stores sessions as `sessnap.Snapshot` values in a mutex-guarded map. Save round-trips through `sessnap.Of` / `sessnap.Restore`, so the stored copy is deep-isolated from both the caller's live session and from subsequently loaded copies — mutations to either side do not alias the store.

`memstore.EventLog` is the companion in-memory event log. It stores events by value (no serialization round-trip) under a mutex. Both are wired together in composition when no `--store-dir` flag is given.

Use this backend for unit tests (it is the reference the conformance suites validate first) and for single-process, non-persistent deployments.

```go
import "github.com/stacklok/mecatl/engine/adapter/memstore"

store := memstore.New()
log   := memstore.NewEventLog()
```

### internal/adapter/store/jsonlstore — JSONL on disk

The default backend for `mecated`. A single store directory holds one `<id>.json` file per session (the snapshot) plus a `<id>.events.jsonl` sidecar (one event per line, tagged with `eventlog-json/1`). The same package triples as `port.SessionStore`, `port.EventLog`, and `port.ToolCallRecorder`. `Delete` removes both files atomically.

Select it with `--store-dir <path>`. The directory is created if it does not exist. The JSONL sidecar is read cumulatively by `Read` — a `Delete` removes the sidecar before the session file.

### internal/adapter/redisstore — Redis-backed

The backend for `mecak8s` (the storage-free, Kubernetes-native composition root). Implements `port.SessionStore`, `port.EventLog`, `port.PrunableStore`, and `port.ToolCallRecorder` over a Redis connection. Used for stateless pod deployments where no persistent volume is available. Select it via `--redis-url` (mecak8s only). Validated by the same conformance suites as jsonlstore, running over miniredis.

---

## Event-sourced Load

mecatl's own adapters use the snapshot model: `Save` writes a full snapshot; `Load` deserializes it. But a host whose system of record is an append-only event log can implement `port.SessionStore.Load` by **folding** the event stream into a `*session.Session` instead.

The reference implementation is `engine/adapter/eventsource.Fold`:

```go
// engine/adapter/eventsource

func Fold(meta SessionMeta, events iter.Seq2[session.Event, error]) (*session.Session, error)
```

`Fold` walks the event stream and reconstructs the session's conversation, lifecycle state, counters, cumulative usage, and pending ask. `SessionMeta` carries the creation facts that no event records — id, mode, limits, workspace, profile, provider/model selector, reasoning-effort, and createdAt — which the host supplies out-of-band.

### Field-by-field reconstruction contract

The table below is derived from `engine/COMPATIBILITY.md` ("Session reconstruction contract"):

| Field | Round-trip obligation | Event source |
|---|---|---|
| `Conversation` (user prompts, assistant text, tool calls, tool results — pairing-valid) | **MUST** | `EvUserPrompt` (genuine prompt + harness continuations), `EvMessageDelta`, `EvToolCall`, `EvToolResult`; pre-compaction head from `EvCompactionArchive` |
| `State` (idle / running / awaiting / completed / failed / cancelled) | **MUST** | derived from terminal `EvResult.Stop`; trailing unanswered `EvPermissionAsk` → awaiting; no terminal → idle |
| Recorded stop reason (`RecordedStopReason`) | **MUST** | `EvResult.Stop` |
| Pending ask (`PendingAsk`, when awaiting) | **MUST** | trailing `EvPermissionAsk` with no following `EvApproval` or `EvResult` |
| Cumulative `Usage` | **MUST** | **SUM** of every per-run `EvResult.Usage` (each is per-run; the budget brake reads the cumulative aggregate) |
| Creation metadata (id, mode, limits, workspace, profile, provider/model selector, reasoning-effort, createdAt) | **MUST** — supplied out-of-band | **Not in any event** — provided via `eventsource.SessionMeta` |
| `Counters` (turns / tool calls / consecutive failures) | run-scoped — latest run segment only | `EvTurnStart` (turns), `EvToolResult` (tool calls / consecutive failures) |
| Run plumbing (diagnostics binding, askID serials) | safe to lose — rebuilt fresh | n/a |

### Replay-fidelity limitation

A fold reconstructs the structural conversation faithfully and is byte-identical-replay-faithful **only for providers that do not use the provider-private opaque replay fields**. Three fields reach the conversation only via `Session.RecordAssistant` in the loop and are never emitted on the event stream:

- `Message.Reasoning` — the provider reasoning replay blob (OpenAI encrypted reasoning content; Anthropic `(thinking, signature)` pair)
- `Message.ProviderPhase` — the OpenAI Responses phase marker
- `ToolCall.ItemID` — the provider-assigned item id

`EvReasoningDelta` carries a human-readable reasoning *summary*, which the loop deliberately never places on `Message.Reasoning` — a fold must not do so either.

For plain-chat providers (including `mockllm`) those fields are empty and the fold is byte-identical. For reasoning providers (OpenAI, Anthropic) the fold produces a structurally correct but not byte-identical conversation. This is why mecatl's own resume uses the snapshot; the fold is for event-log-SoR hosts that accept this boundary or carry those fields in their own richer event schema.

This is a documented contract limitation, not a bug. See `engine/COMPATIBILITY.md` and ADR 0038.

---

## Conformance suites

Any `SessionStore` implementation — including your own — can be validated against the shared conformance table in `engine/adapter/storeconformance`:

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

The store suite pins the contract — snapshot encoding, isolation (Save and Load must not alias the caller's pointer), lifecycle-state fidelity for all six session states, multi-session keying, not-found sentinel wrapping, and overwrite semantics. It does not assert wire format or file layout.

Similarly, any `EventLog` implementation can be validated against `engine/adapter/eventlogconformance`:

```go
import "github.com/stacklok/mecatl/engine/adapter/eventlogconformance"

func TestMyEventLog(t *testing.T) {
    eventlogconformance.Run(t, func(t *testing.T) port.EventLog {
        return mylog.New(t.TempDir())
    })
}
```

The event log suite covers: append-then-read order, miss as empty sequence (not error), distinct-session isolation, no Seq-based reordering (contract is raw append order), large event round-trip, cumulative log crossing the framing boundary (the streaming-vs-unary correctness test), early-break resource release, and concurrent append/read safety under `-race`.

Both suites are the same ones `jsonlstore` and `redisstore` pass — passing them is the bar for a production-grade implementation.

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

The `EventLog` is not a `Deps` field — the loop is storage-agnostic by design and never imports `port.EventLog`. Wire it in your relay layer, calling `log.Append` for every event your relay observes before forwarding it to clients.

### mecated (standalone server)

- **`--store-dir <path>`** — selects the jsonlstore backend (JSONL on disk). Both `SessionStore` and `EventLog` are wired from the same store directory.
- No flag — defaults to `memstore` (in-memory, no persistence across restarts).

### mecak8s

- **`--redis-url <url>`** — selects the Redis backend. Both `SessionStore` and `EventLog` are wired through the Redis adapter.

---

## What's next

- [LLM provider](llm-provider.md) — implement the `port.LLMProvider` interface to plug in a custom model backend.
- [Permission policy](permission-policy.md) — implement `port.PermissionPolicy` to replace the built-in rule engine.
- [Hook system](/what-you-get/hooks.md) — the `port.HookRunner` lifecycle hooks that fire around tool calls.
- [The agent loop](/what-you-get/agent-loop.md) — how the loop drives turns, emits events, and reaches terminal states.
