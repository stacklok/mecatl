---
sidebar_position: 3
title: SessionStore and EventLog
description:
  Implement session snapshots and durable event logs through independent ports.
---

# SessionStore and EventLog

`port.SessionStore` persists the current state of a session. `port.EventLog`
records the events emitted while runs execute. The ports are independent, so an
application can use different backends for snapshots and events.

## Implement SessionStore

```go
type SessionStore interface {
    Save(ctx context.Context, s *session.Session) error
    Load(ctx context.Context, id session.SessionID) (*session.Session, error)
}
```

`Save` replaces the snapshot for an ID. Copy or encode the session before
returning so later mutations to the caller's value cannot change stored state.

`Load` returns an independent `*session.Session` with valid lifecycle state.
Wrap `port.ErrSessionNotFound` when the ID does not exist:

```go
sess, err := store.Load(ctx, id)
if errors.Is(err, port.ErrSessionNotFound) {
    // Create a session.
}
```

Snapshot-backed stores can use `engine/adapter/sessnap` to encode and restore
the complete aggregate. It validates snapshots and reconstructs lifecycle state
through the session API.

### Publish a new session atomically

Stores used with caller ownership enforcement must also implement
`port.SessionCreator`:

```go
type SessionCreator interface {
    Create(ctx context.Context, s *session.Session) error
}
```

`Create` must atomically check for an existing ID and publish the first
snapshot. Wrap `port.ErrSessionAlreadyExists` on collision and leave all
existing snapshot, event, metadata, and audit records unchanged. Use `Save` only
after creation.

The current gRPC session-store driver does not provide atomic creation, so it
cannot be combined with OIDC caller ownership enforcement.

### Support retention

Implement `port.PrunableStore` when the backend supports session inventory and
deletion:

```go
type PrunableStore interface {
    List(ctx context.Context) ([]StoredSession, error)
    Delete(ctx context.Context, id session.SessionID) error
}
```

`List` returns IDs and modification times without loading transcripts. `Delete`
is idempotent for an unknown ID. Return `port.ErrPruneUnsupported` when the
backend cannot perform these operations.

Multi-writer backends should also implement `ConditionalPrunableStore`, which
deletes a session only when its durable metadata still matches the cleanup plan.
Cleanup revalidates candidates under a `SessionLease` and removes sidecars
before the authoritative snapshot.

JSONL and Redis stores implement `SessionMigrationStore` for bounded, resumable
storage migration. Stores without it report migration as unsupported. Migration
and cleanup remain unavailable until the backend's inventory is in a verified
ready state.

## Implement EventLog

```go
type EventLog interface {
    Append(
        ctx context.Context,
        id session.SessionID,
        ev session.Event,
    ) error

    Read(
        ctx context.Context,
        id session.SessionID,
    ) iter.Seq2[session.Event, error]
}
```

`Append` returns nil only after the event reaches stable storage or the backing
service commits it. The caller attempts each append once because an error can
arrive after the write committed. Implementations do not need to deduplicate
events.

`Read` returns events in append order. A missing log yields an empty sequence.
On an infrastructure or decoding error, yield the error and stop. Release open
resources when the context is canceled or the caller stops iterating.

Implementations must support concurrent operations across session IDs. Mecatl
serializes appends for one session through its event relay.

### Add resumable cursors

`port.CursorEventLog` extends `EventLog` for resumable and follow-mode readers:

```go
type CursorEventLog interface {
    EventLog

    AppendEvent(
        ctx context.Context,
        id session.SessionID,
        ev session.Event,
    ) (Cursor, error)

    AppendGap(
        ctx context.Context,
        id session.SessionID,
        reason string,
    ) (Cursor, error)

    ReadAfter(
        ctx context.Context,
        id session.SessionID,
        after Cursor,
        opts ReadOptions,
    ) iter.Seq2[LogRecord, error]
}
```

A cursor identifies one exact position and log generation. Return
`ErrCursorExpired` for a superseded generation and `ErrCursorMalformed` for an
invalid or unaligned cursor. Never approximate a position.

`AppendGap` records that an event could not be appended. It cannot report a
total backend outage, but it lets readers detect isolated missing records.

### Distinguish EventLog from EventSink

|Port|Purpose|Called by|
|-|-|-|
|`EventSink`|Relay live events to clients or telemetry|Agent loop|
|`EventLog`|Persist durable event history|Server relay|

The loop emits events without depending on `EventLog`. A failed event append can
leave a log gap, but it does not stop the live run. The completed session
snapshot remains authoritative.

## Choose a supplied backend

|Backend|Package|Use|
|-|-|-|
|In-memory|`engine/adapter/memstore`|Tests and non-persistent single-process applications|
|JSONL|`internal/adapter/store/jsonlstore`|Persistent `mecated` deployments|
|Redis|`internal/adapter/redisstore`|Persistent `mecak8s` and multi-replica deployments|

External applications can import `memstore`. The JSONL and Redis packages are
host adapters under `internal/`; use their behavior as a reference for your own
implementation.

The JSONL store persists snapshots, events, and tool-call audit records in
separate files. Use a physical, non-symlink store path whose filesystem supports
the synchronization operations required for durable writes.

The Redis store uses Redis streams for events and supports blocking follow reads
through `CursorEventLog`.

## Load from events

`engine/adapter/eventsource` folds an event log into a session when an
event-sourced backend has no snapshot. The caller must supply creation metadata,
including the session ID, permission mode, limits, environment reference,
provider and model selection, and creation time. Events do not contain all of
these values.

Event replay restores conversation content, usage, lifecycle state, and the
latest complete compaction boundary. It cannot restore provider-private replay
fields that were never present in the event log. Use snapshot persistence when
exact provider replay fidelity is required.

## Run the conformance suites

Validate a store with `engine/adapter/storeconformance`:

```go
func TestStore(t *testing.T) {
    storeconformance.Run(t, func(t *testing.T) port.SessionStore {
        return newStore(t)
    })
}
```

Run `eventlogconformance.Run` for `EventLog` and `eventlogconformance.RunCursor`
for `CursorEventLog`. The suites cover isolation, ordering, large records, early
iterator termination, cursor semantics, and concurrent access.

Run `storeconformance.RunPrunable` if your store implements `PrunableStore`.

## Wire the ports

Pass `SessionStore` through `agent.Deps.Store` in a direct engine embedding. The
server composition also receives the `EventLog`, because the relay owns event
persistence.

`mecated --store-dir <PATH>` selects JSONL storage. Without a store flag,
`mecated` uses in-memory storage. `mecak8s --redis-url <URL>` selects Redis for
both snapshots and events.

## Next steps

- [Implement a session lease](session-lease.md).
- [Record tool calls](tool-catalog.md#record-tool-calls).
- [Understand the agent loop](/building/what-you-get/agent-loop.md).
