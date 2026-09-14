# ADR 0330 — Isolated bounded Redis follow capacity

- Status: Accepted
- Date: 2026-09-11
- Scope: repository Go compatibility, redisstore client-generation ownership, follower admission and shutdown, and mecak8s operator sizing
- Supersedes: ADR 0036's Go 1.26 engine-module floor, ADR 0093's Go 1.26 provider-module floor, ADR 0233's `github.com/stacklok/toolhive-core/redis` construction-package locator, ADR 0250's shared-pool sizing deferral, and ADR 0240's no-force-close rule for isolated follow clients only; all five ADRs remain authoritative elsewhere
- Superseded by: None

## Context

ADR 0250 chose Redis Streams so `XREAD BLOCK` could provide durable cross-process follow. Its
implementation bounds every block to a one-second slice and releases a credential-generation
lease between slices. Those measures make cancellation and credential rotation prompt, but do
not isolate capacity: every blocked `XREAD` still occupies a connection from the same go-redis
pool used by `Save`, `AppendEvent`, and metadata operations.

The original ADR deferred a separate pool on the grounds that an operator could size the shared
pool. That premise is false in the shipped adapter, which exposes no pool control, and even an
exposed shared-pool size would only move the contention threshold. Enough idle watches can still
starve the writes that keep a live run durable. This matters most in the multi-replica mecak8s
deployment for which cross-process follow exists.

Follower lifetime is also split across owners. The watch service cancels its pump, while
redisstore owns the blocking storage call but has no registry of followers to cancel at
`Store.Close`. Force-closing the shared client is unsafe because it would interrupt unrelated
durability work. A useful shutdown bound therefore depends on first separating the clients.
ADR 0240 consequently forbids force-closing any actively leased client; this decision narrows
that rule only after the follow client is isolated from all durability work.

The independently versioned `github.com/stacklok/toolhive-core/redisconn` v0.0.2 module
exposes `PoolSize` and `MaxActiveConns` through its supported `redisconn.Config`. ToolHive
Core v0.0.46 provides the matching umbrella release. Both modules declare `go 1.27`;
mecatl's original plan targeted Go 1.26.6, so the first implementation attempt could not
pass the required tidy gate without a compatibility-floor decision. The user approved
raising mecatl to Go 1.27 rather than waiting for a different ToolHive release. Issue
[#876](https://github.com/stacklok/mecatl/issues/876) records the implementation scope.

## Decision

1. Each Redis credential generation owns an atomic pair of clients: one durability client using
   the existing connection defaults, and one follow client. Construct and probe the complete pair
   before publication. If either half fails, close the partial candidate and retain the previous
   generation. Upgrade the root ToolHive Core module to v0.0.46 and migrate redisstore from the
   deprecated `github.com/stacklok/toolhive-core/redis` facade to the independently versioned
   `github.com/stacklok/toolhive-core/redisconn` v0.0.2 module.

2. Raise every committed mecatl `go.mod` and `go.work` from Go 1.26.6 to `go 1.27`. Update the
   Taskfile requirement, source-build container, repository guidance, architecture, and
   canonical source-build documentation to the same floor. Commit the direct and transitive
   dependency changes required by `task tidy`; do not combine this compatibility migration
   with unrelated Go 1.27 language or library refactors. Independently released engine,
   provider, and authn
   modules move in lockstep with the repository even when they do not directly import ToolHive.

3. Configure the follow client with both `PoolSize` and `MaxActiveConns` equal to the effective
   follow-pool size. Route every Redis operation made by a `ReadAfter` with `Follow:true` through
   a follow client; route `Follow:false` and every other store operation through the durability
   client. Preserve the one-second `XREAD` slices and ADR 0240's per-cycle generation leasing: a
   follower holds process-wide admission for its full iterator, but acquires and releases the
   current follow-client generation around each bounded read cycle and before yielding. It never
   pins one credential generation for the iterator lifetime.

4. Give each Store one fail-fast follower admission limit, held once for the full lifetime of a
   `Follow:true` iterator. Admission happens before its first Redis operation and is released on
   every exit, including early consumer break. Saturation immediately yields the exported
   `port.ErrEventFollowCapacity`; it never waits for a pool timeout. The public error code is
   `watch_capacity`, mapped to gRPC `RESOURCE_EXHAUSTED`. The HTTP watch has already committed
   `200 text/event-stream`, so it carries the same code in its terminal SSE error frame.
   `watch_lagging` remains the distinct error for a transport consumer that exhausts its delivery
   buffer. Add `watch_capacity` to the TypeScript SDK's stable `MECATL_ERROR_CODES` and resumable
   watch-failure set so an attachment retries under its existing bounded backoff from the last
   processed cursor and unchanged filter.

5. Make the Store own follower cancellation and joining. Linearize close with admission so a
   racing read either receives `errStoreClosed` or is registered and cancelled; no follower can
   pass the closed check and then miss the cancellation snapshot. Close cancels all admitted
   followers and gives cooperative reads a bounded first wait. It then asynchronously force-closes
   only follow clients with stragglers and waits only through the fixed total close grace. Every
   iterator that exits because of store cancellation or follow-client closure ends cleanly. If an
   injected pathological read and client close ignore both signals, Close still returns at the
   total bound and the closed lifecycle retains ownership of the late cleanup without admitting
   new work. Never force-close a durability client on a follower's behalf. This narrowly
   supersedes ADR 0240's no-force-close rule for isolated follow clients; its rule remains exact
   for durability clients and every other Redis lease. Credential retirement closes both clients
   once each bounded operation releases its generation lease.

6. Expose `--redis-follow-pool-size` and `--redis-max-followers` on mecak8s and
   `redis.follow.poolSize` and `redis.follow.maxFollowers` in its Helm chart. Both defaults are 32.
   Validate the effective invariant `1 ≤ maxFollowers ≤ poolSize`; do not clamp invalid values.
   Helm JSON schema validates each positive integer, while a template helper validates the
   cross-field inequality. These are process-local resource controls, not caller authorization or
   persisted state.

7. Inventory two distinct process-lifetime resources in ADR 0027 List 1: the isolated follow
   clients attached to credential generations, and the Store's follower admission/cancellation/
   join registry. Neither receives a List 2 row because a restart reconstructs the clients from
   operator configuration and clients reattach watches with their durable cursors. Add no new
   metrics or telemetry for this change.

Consumer groups are rejected because every watcher needs the full stream. Pub/Sub may only ever
be a lossy wake-up hint followed by a read from the authoritative Stream. Per-session hubs,
multi-stream `XREAD` sharding, per-principal admission, and durability-pool tuning require
separate decisions if future measurements justify them.

## Consequences

Admitted followers can no longer consume the connections used by session saves or event appends,
and overload becomes an immediate, typed, resumable response instead of a pool-timeout stall.
Redisstore can now make follower teardown bounded without risking write-path clients. Credential
reload becomes more expensive because every generation constructs and probes two clients, and a
process reserves a separate bounded Redis connection budget for follow traffic.

Go 1.26 ceases to be a supported source-build or consumer toolchain for every mecatl Go module.
The Go 1.27 floor permits the released ToolHive connection modules and their required dependency
graph, but also makes the compatibility cost repository-wide rather than local to redisstore.
Keeping this as a floor-only migration avoids mixing opportunistic refactors into the resource
isolation change.

The two knobs deliberately expose both a socket ceiling and a logical admission ceiling. Keeping
`maxFollowers` at or below the pool size means an admitted follower never queues merely because
another admitted follower owns every allowed follow connection. The defaults impose a finite
cost but cannot choose the right production value for every deployment; operators remain
responsible for fitting the bound to their Redis service limit and watch concurrency.

The admission limit is adapter-local and has no principal. It protects one process's Redis
capacity but does not provide per-caller fairness or a cluster-wide watch quota. Those policies
belong at the server authority boundary, not in redisstore.

## See also

- [Redis follow capacity acceptance plan](../acceptance/redis-follow-capacity.md)
- [ADR 0250 — Durable cursors and the session watch transport](./0250-durable-cursors-and-watch.md)
- [ADR 0240 — mecak8s credential reload and chart security](./0240-mecak8s-credential-reload-and-chart-security.md)
- [ADR 0027 — Cloud-native resource inventory](./0027-cloud-native.md)
- [ADR 0048 — mecak8s](./0048-mecak8s.md)
- [ADR 0233 — Secure external Redis](./0233-secure-external-redis.md)
