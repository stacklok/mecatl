# ADR 0250 — Durable cursors and the session watch transport

- Status: Proposed
- Date: 2026-08-28
- Scope: `port.CursorEventLog`, the Redis event-log datatype, cursor encoding, gap
  markers, and the `WatchSessionEvents` delivery contract.

## Context

[Issue #821](https://github.com/stacklok/mecatl/issues/821) needs an SDK client to replay
a session's history and then follow it live, as one operation, resumably. mecatl has two
read paths and neither can do it:

- [`port.EventLog.Read`](../../engine/port/eventlog.go) is a complete, ordered, durable
  replay with **no position and no follow**. It reads the whole log and stops. To catch up
  and then watch, a client must read everything and *then* subscribe — and any event
  appended between those two steps is silently lost. There is no token to hand back
  meaning "resume from here".
- `Service.Subscribe`, behind `StreamSessionLive` ([`internal/adapter/server/grpc.go`](../../internal/adapter/server/grpc.go)),
  is live but **process-local, in-memory, and non-durable**: a Go channel registry that is
  explicitly non-blocking and drops for slow subscribers. A second replica sees nothing. A
  reconnecting browser tab sees nothing that happened while it was away.

The concrete failures: a tab reloading mid-run cannot rejoin without either losing events
or re-downloading the whole transcript; and on `mecak8s` ([ADR 0048](./0048-mecak8s.md)),
where state deliberately lives in Redis rather than on a pod, a client reconnecting to a
*different replica* gets nothing at all, because the live registry died with the old pod.

A cursor is the one primitive that fixes both: an opaque token meaning "you have durably
received everything up to append position P".

**The Redis datatype is the crux.** The event log is currently a **LIST** —
[`redisstore.Append`](../../internal/adapter/redisstore/redisstore.go) is `RPUSH` onto
`mecatl:events:<id>`, `Read` is `LRANGE 0 -1`. A LIST gives ordering but no blocking read
and no stable per-entry identity: follow degenerates to `LLEN` polling per watcher;
position is an index, stable only because nothing is ever trimmed from the head (true
today — there is no `LTRIM` and no TTL on the events key), which also means the log grows
unbounded forever and the day anyone adds retention every outstanding cursor silently
points at the wrong event. Not an error — wrong data.

The blocker that would have made a Redis **Stream** expensive turns out not to exist:
`miniredis v2.38.0`, which backs the offline conformance tests, implements `XADD`,
`XLEN`, `XRANGE`, and `XREAD` **including its blocking path**.

Finally, #821 asks for a guarantee this ADR must not repeat uncritically: "a durable
append failure terminates connected watchers with `ActivityGapError` and never advances
their cursor". The issue itself anticipates that this may not survive contact with
storage, and instructs: *"stop and tighten the ADR wording rather than shipping a false
guarantee."*

## Decision

**1. Add `port.CursorEventLog` as an additive engine port.** `port.EventLog` is
unchanged and unbroken; a backend opts in by also implementing the cursor port. A
backend that does not is not silently degraded — the watch operation reports the feature
as unsupported.

**2. Migrate the Redis event log from a LIST to a STREAM.** `XADD` IDs *are* opaque,
monotonic, durable cursors — no invention required. `XREAD BLOCK` is a durable,
cross-process, blocking follow, which is the capability multi-replica deployment
requires and which a LIST structurally cannot provide. `XRANGE … COUNT` gives bounded
paging, which the current unbounded `LRANGE 0 -1` cannot. `XTRIM MINID` makes future
retention safe precisely *because* IDs are not positional.

**3. Cursors are opaque, stateless, and scoped to BOTH the session and the log
generation.** The wire form is an opaque `base64({session, generation, position})`; a
generation mismatch is `CursorExpiredError`, which requires an explicit
restart-from-beginning or transcript reload and never degrades silently, and a cursor
presented against a different session is `CursorMalformedError`.

Session scoping was added during implementation, after review of
[#868](https://github.com/stacklok/mecatl/pull/868) observed that the generation check
alone does not carry it. A position is only meaningful inside ONE log, and generation
cannot separate two logs whenever they share a basis value — which legacy logs
systematically do, because a log predating generations reports the EMPTY generation. A
cursor issued for session A then decoded cleanly against session B and resolved to a
real but WRONG record; that was reproduced against both shipped backends before the fix.
The check therefore lives in `DecodeCursor` rather than in each backend, so it is
structural: a backend cannot forget it and a new backend inherits it. Per backend: memstore uses a slice index; JSONL uses the **byte offset** of the
next record's first byte (`Seek` is O(1); a line index would need a rescan); Redis uses
the `XADD` ID; the gRPC driver passes the token through opaquely. A server-side cursor
registry was rejected — it would need eviction and a cloud-inventory row to buy nothing.

**4. JSONL follow is size-polling, not `fsnotify`.** A `Stat` on an interval. Attachment
is not a keystroke-latency path; `fsnotify` adds dependency surface to a store adapter
for a latency win that does not matter here, and silently does not work on network
filesystems — where size-polling degrades identically to the local case.

**5. A gap marker is a log-record envelope variant, never a `session.Event`.** Both
backends already wrap events in a versioned envelope — JSONL writes
`{"v":"eventlog-json/1","ev":…}`, Redis writes `{"v":"redisstore-eventlog/1","ev":…}`. A
gap is a sibling tag. It occupies a real append position, so cursors advance past it
correctly, but it decodes to a **delivery-envelope signal**, not an event: `WatchSessionEvents`
returns `{event, cursor, phase}` and a gap is a `phase`, alongside `replay` and `live`.

Consequently `session.Event` gains nothing, the proto `Event` message gains nothing, the
TypeScript event union gains nothing, and the kind-parity gate is untouched. The legacy
`port.EventLog.Read` **skips** gap records, preserving its existing contract of returning
only events.

**6. The append-gap guarantee is three-tier and honestly bounded.** #821's absolute is
not achievable and this ADR does not claim it. If replica A's `Append` fails, a watcher on
replica B has no way to learn it: a failed append consumed no position, so it leaves
nothing to observe. If A crashes at the same moment, even the local notification is lost.
What we guarantee:

- **Best-effort, cross-process:** on append failure, attempt one durable gap-marker
  record. If it lands, every watcher everywhere learns of the gap deterministically. This
  covers the *likely* failure — one rejected or unencodable record — but not a total
  backend outage.
- **Guaranteed, process-local:** watchers in the failing process terminate with
  `ActivityGapError` and their cursor never advances.
- **Stated residual:** total backend outage plus process loss leaves an **undetectable**
  gap. The SDK claims ordered, at-least-once delivery **for durably-appended events** and
  claims nothing beyond that.

The owned run continues in every case — a broken log must not break a live run, which is
the existing relay discipline.

**7. Slow watchers never backpressure a run.** Delivery state is bounded; a watcher that
cannot keep up is terminated with a **resumable** error rather than having its events
dropped. Dropping is what `Service.Subscribe` does today and is exactly what a durable
cursor exists to stop.

## Consequences

**Easier.** Replay-then-follow becomes one operation with no window to lose events in.
Reconnect becomes "hand back the last cursor". Multi-replica attachment works at all.
Bounded paging over a long log becomes possible for the first time.

**Harder — the honest costs.**

- **A Redis storage-format migration.** Existing `mecatl:events:*` LIST keys must be read
  by a legacy path or migrated, and `eventsKey(id)` is embedded in the delete/rebuild Lua
  scripts in [`internal/adapter/redisstore/metadata_index.go`](../../internal/adapter/redisstore/metadata_index.go),
  so a new key shape threads through those too. [`internal/adapter/redisstore/migration.go`](../../internal/adapter/redisstore/migration.go)
  and [`internal/adapter/redisstore/generation.go`](../../internal/adapter/redisstore/generation.go)
  are precedent, but this is real work with a real crash-safety surface.
- **We are shipping a guarantee weaker than the one #821 asked for**, deliberately, and
  the SDK's public documentation must say so. A user who loses a durable backend *and* the
  process holding the watchers has no mechanism to learn that they missed events. We
  believe stating this is strictly better than an absolute that Redis being down
  falsifies, but it is a real limitation and not a wording flourish.
- **Four cursor implementations plus a conformance suite.** JSONL, Redis, memstore, and
  the gRPC driver each need generation handling, expiry, and tamper rejection, validated
  by one shared suite.
- **Every watcher is a long-lived resource.** Each needs a row in
  [ADR 0027](./0027-cloud-native.md)'s resource inventory: owner, scope, cleanup,
  re-attach. That inventory has drifted before; this decision adds to what it must track.
- **Cursor opacity is a promise we must keep.** The encoding is stateless and therefore
  inspectable by a determined client. It is documented as opaque, and a client that
  decodes and hand-builds one gets `CursorExpiredError` at the next generation change.

## See also

- [Issue #821](https://github.com/stacklok/mecatl/issues/821); [`docs/acceptance/sdk-server-enablers.md`](../acceptance/sdk-server-enablers.md).
- [ADR 0027](./0027-cloud-native.md) — the cloud-native arc, the event log's origin, and
  the resource inventory this decision must feed.
- [ADR 0048](./0048-mecak8s.md) — the storage-free multi-replica deployment that makes
  cross-process follow a requirement rather than a nicety.
- [ADR 0038](./0038-event-sourced-rehydration.md) — the fold over the durable log.
- [ADR 0248](./0248-sdk-compatibility-and-error-contract.md) — how an unsupported cursor
  backend is advertised; [ADR 0249](./0249-durable-run-identity.md) — the `run_id` the
  watch filters on.
- [`engine/port/eventlog.go`](../../engine/port/eventlog.go) — the existing port this one
  extends without breaking.
