---
sidebar_position: 3
title: SessionStore and EventLog
description:
  Use built-in Go stores for session snapshots and durable event logs, or implement the persistence ports.
---

# SessionStore and EventLog

`port.SessionStore` persists the current state of a session. `port.EventLog`
records the events emitted while runs execute. The ports are independent, so an
application can use different backends for snapshots and events.

## Use a supplied backend

External Go applications can import the concrete stores from
`github.com/stacklok/mecatl/adapters`. The packages retain their full APIs,
including writes, scheduling, and the gRPC content-source and learning drivers.
Use the engine ports to expose only the operations your application needs.

|Backend|Import path|Use|
|-|-|-|
|In-memory|`github.com/stacklok/mecatl/engine/adapter/memstore`|Tests and non-persistent single-process applications|
|JSONL|`github.com/stacklok/mecatl/adapters/jsonlstore`|Local durable snapshots, event logs, and tool-call audit files|
|Redis|`github.com/stacklok/mecatl/adapters/redisstore`|Shared persistence across replicas|
|gRPC driver|`github.com/stacklok/mecatl/adapters/grpcdriver`|Clients and server wrappers over an independently operated backend|

Install `adapters/v0.1.1` with Go 1.27 or later. It resolves through the
public Go proxy with its support and driver dependencies. The
[adapter compatibility and release policy](https://github.com/stacklok/mecatl/blob/main/adapters/COMPATIBILITY.md)
records the module boundaries and release verification. Local workspace
replacements are for repository development only.

```sh
go get github.com/stacklok/mecatl/adapters@v0.1.1
```

### Open JSONL storage

Use a physical, non-symlink directory on a filesystem that supports the required
synchronization operations. For example, this function loads the latest saved
snapshot by its exact session ID:

```go
import (
    "context"

    "github.com/stacklok/mecatl/adapters/jsonlstore"
    "github.com/stacklok/mecatl/engine/session"
)

func loadSnapshot(ctx context.Context, dir string, id session.SessionID) (*session.Session, error) {
    store, err := jsonlstore.New(dir)
    if err != nil {
        return nil, err
    }
    return store.Load(ctx, id)
}
```

`New` creates directories, probes atomic replacement and file/directory sync,
and reaps abandoned temporary generations. `Load`, `Read`, and `ReadAfter` need
family lock files; metadata paging can rebuild or delete derivative catalog
artifacts. This is **not a general read-only filesystem mount API**, even if your
application only reads. Plain `List` or lineage reads do not remove the
constructor's write requirements. Use a writable copy when inspecting an
immutable backup.

JSONL has no `Store.Close`. Operations own their file handles; event iterators
retain handles until iteration ends. Finish iteration, break out of the range,
or cancel its context, and let the iterator return. Cancellation cannot interrupt
your code while it is handling a yielded record.

### Open Redis storage

Use `NewWithConfig` with verified TLS and mounted credential files. This example
loads a snapshot and closes the store before returning:

```go
import (
    "context"
    "errors"

    "github.com/stacklok/mecatl/adapters/redisstore"
    "github.com/stacklok/mecatl/engine/session"
)

func loadRedis(ctx context.Context, id session.SessionID) (snapshot *session.Session, err error) {
    store, err := redisstore.NewWithConfig(redisstore.Config{
        Addr:         "redis.example.com:6379",
        TLS:          true,
        UsernameFile: "<USERNAME_FILE>",
        PasswordFile: "<PASSWORD_FILE>",
    })
    if err != nil {
        return nil, err
    }
    defer func() { err = errors.Join(err, store.Close()) }()
    return store.Load(ctx, id)
}
```

Replace the file placeholders with paths supplied by your deployment. `TLS: true`
uses system trust; set `CAFile` for a private PEM trust bundle. Credentials require
verified TLS. `redisstore.New(addr)` opts into unauthenticated plaintext and is
intended for local fixtures, not production connections.

Call `store.Close()` during shutdown and handle its error. The store owns its
connection pools, credential reloader, and followers. Stop consuming follow
iterators before shutdown; cancellation does not interrupt a consumer callback.

Construction verifies or initializes metadata and lineage markers, using `SETNX`
when markers are missing. On a preinitialized backend, read operations do not
write data, but metadata pagination uses ordinary `EVALSHA`/`EVAL` with a
read-only script body. Your Redis ACL still needs the relevant scripting and read
permissions; this is not a guarantee of compatibility with a read-only replica
or its ACL. `List` skips per-key `HGET` errors, so an incomplete ACL can produce an
incomplete inventory. Verify the exact commands and key scopes on your Redis
server. The offline miniredis write-denial fixture is not a real Redis ACL proof.

### Read inventory, snapshots, and events

Use the smallest port that answers your question:

|Question|Port or operation|Interpretation|
|-|-|-|
|Which sessions exist?|`PrunableStore.List`|IDs and modification times, without transcripts|
|Which sessions match an inventory page?|`SessionMetadataPager`|Bounded discovery metadata and an opaque continuation|
|What was last saved?|`SessionStore.Load`|Latest saved aggregate, including usage and relationships|
|What was recorded during runs?|`EventLog.Read`|Events in append order; gaps are omitted|
|Where can processing resume?|`CursorEventLog.ReadAfter`|Events and gap markers with durable cursors|
|Was related content pruned?|`SessionLineageReader`|Content-free identities and tombstones for exact incarnations|

For optional operations, use `port.SupportsSessionMetadataPaging`,
`port.SupportsSessionLineage`, `port.SupportsSessionCreate`, and
`port.SupportsActivityProjection` before asserting the corresponding interface.
In particular, a gRPC client implements optional Go interfaces even when the
remote backend does not advertise them. Handle operation errors as well as the
capability checks. See [remote driver integration](#connect-a-remote-driver).

A snapshot represents the latest successful save, not necessarily the live state.
Use its saved usage values for usage accounting; counting log records measures
recorded events, not tokens or turns. The event log is
not a complete transactional reconstruction of every snapshot change. Use
`ReadAfter` to see `LogRecordGap` markers, and persist each record's opaque
`Cursor` only after successfully processing the record. Treat replay as
at-least-once delivery.

An empty read from the zero cursor cannot distinguish a never-recorded log from
a pruned one. A saved cursor expires when deletion/recreation changes the log's
generation; handle `port.ErrCursorExpired` separately from
`port.ErrCursorMalformed`. Lineage tombstones can explain missing snapshots
without retaining their content. Arbitrary external Redis stream trimming that
does not change the generation is not detected as cursor expiration.

## Connect a remote driver

The public `grpcdriver` package borrows a connection from your application. Use
`Dial` with its TLS options, construct the client with a context, and close the
connection after all clients and streams using it finish:

```go
import (
    "context"

    "github.com/stacklok/mecatl/adapters/grpcdriver"
    "github.com/stacklok/mecatl/engine/session"
)

func loadRemote(ctx context.Context, id session.SessionID) (*session.Session, error) {
    conn, err := grpcdriver.Dial("driver.example.com:443",
        grpcdriver.WithTLS(grpcdriver.TLSOptions{}))
    if err != nil {
        return nil, err
    }
    defer conn.Close()

    store, err := grpcdriver.NewSessionStore(ctx, conn)
    if err != nil {
        return nil, err
    }
    return store.Load(ctx, id)
}
```

`Dial` is lazy. `NewSessionStore` performs bounded capability negotiation and
requires the current base-contract marker. TLS options also accept `CAFile`,
`ClientCertFile`, and `ClientKeyFile`; `WithBearerToken` supplies a per-RPC bearer
credential. Non-local connections require TLS. The helper accepts its own
`grpcdriver.Option` values, not arbitrary `grpc.DialOption` values. If you need
client interceptors, construct your own `grpc.ClientConnInterface` with verified
transport credentials and the snapshot message limits.

`grpcdriver.NewEventLog(conn)` borrows the same connection. Its cursor interface
does not negotiate support at construction: unsupported cursor RPCs return
`grpcdriver.ErrDriverCursorUnsupported` on invocation. Handle it separately from
malformed or expired cursors. A backend follow-capacity error currently crosses
the server wrapper as gRPC `Internal`; its local sentinel classification does not
survive the transport.

### Serve a backend

Register the wrappers with the generated driver package. The import path remains
`github.com/stacklok/mecatl/contracts/gen/go/mecatl/driver/v1`, owned by the
`github.com/stacklok/mecatl/contracts/gen/go/mecatl/driver` module:

```go
import (
    "github.com/stacklok/mecatl/adapters/grpcdriver"
    driverv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/driver/v1"
    "github.com/stacklok/mecatl/engine/port"
    "google.golang.org/grpc"
    "google.golang.org/grpc/credentials"
)

func newDriverServer(store port.SessionStore, log port.EventLog,
    transport credentials.TransportCredentials,
    unaryAuth grpc.UnaryServerInterceptor,
    streamAuth grpc.StreamServerInterceptor,
) *grpc.Server {
    server := grpc.NewServer(
        grpc.Creds(transport),
        grpc.UnaryInterceptor(unaryAuth),
        grpc.StreamInterceptor(streamAuth),
        grpc.MaxRecvMsgSize(grpcdriver.MaxSnapshotBytes),
        grpc.MaxSendMsgSize(grpcdriver.MaxSnapshotBytes),
    )
    driverv1.RegisterSessionStoreServiceServer(server, grpcdriver.NewSessionStoreServer(store))
    driverv1.RegisterEventLogServiceServer(server, grpcdriver.NewEventLogServer(log))
    return server
}
```

Supply server TLS credentials and interceptors that authenticate callers and
authorize each operation, including streaming reads. The wrappers provide **no
authentication or authorization**. Your application owns the listener, server
shutdown, and backend cleanup. A client-side read-only interface is not an access
control boundary, and backend reads can still perform the storage maintenance
described above. The wrappers advertise the backend's capabilities; atomic create
is available only when that backend supports it.

For protocol rationale and the complete conformance matrix, see the
[driver architecture](https://github.com/stacklok/mecatl/blob/main/docs/architecture/observability.md#remote-store--source-drivers-adaptersgrpcdriver).

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

For a remote store, check `port.SupportsSessionCreate(store)` after capability
negotiation before enabling caller ownership enforcement.

### Support retention

Implement `port.PrunableStore` when the backend supports session inventory and
deletion:

```go
type PrunableStore interface {
    List(ctx context.Context) ([]StoredSession, error)
    Delete(ctx context.Context, id session.SessionID) error
}
```

`List` returns every stored session's ID and modification time without loading
transcripts. It does not apply retention filters. `Delete` is idempotent for an
unknown ID. Return `port.ErrPruneUnsupported` when the backend cannot perform
these operations.

Multi-writer backends should also implement `ConditionalPrunableStore`, which
deletes a session only when its durable metadata still matches the cleanup plan.
The implementation must hold its mutation exclusion while it revalidates the
metadata and removes sidecars before the authoritative snapshot.

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

`Read` returns events in append order without reordering them by `Event.Seq`. A
missing log yields an empty sequence. On an infrastructure or decoding error,
yield the error and stop. Release open resources when the context is canceled or
the caller stops iterating.

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

Run `eventlogconformance.Run` for `EventLog`. For `CursorEventLog`, pass
`eventlogconformance.RunCursor` a `CursorSuite` with `New`, `Reset`, and
`NewPair` functions. `New` creates an isolated log, `Reset` changes a log's
positional basis, and `NewPair` returns independently constructed handles over
the same durable state. Only an in-memory backend should use
`SkipCrossProcess` instead of `NewPair`. The suites cover isolation, ordering,
large records, early iterator termination, cursor semantics, and concurrent
access.

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
