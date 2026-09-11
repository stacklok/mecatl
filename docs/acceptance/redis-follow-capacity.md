# Redis follow capacity — acceptance plan

**Contract:** human-reviewed/v2
**Work classification:** Architectural — this changes exported engine and TypeScript SDK error contracts, operator CLI and Helm configuration, and ownership of process-lifetime Redis clients and follower goroutines.
**Decision record:** [ADR 0328](../adr/0328-isolated-redis-follow-capacity.md)
**Phase:** Cloud-native durable event watch
**Status:** proposed, 2026-09-11. Decisions approved by the user while grilling the design for [issue #876](https://github.com/stacklok/mecatl/issues/876).
**Delivery:** Split. The exported Go API, public error code, operator configuration, and shutdown resource boundary warrant independent Plan / Interface review before implementation.
**Expected tasks:** deferred to orchestration
**Issue:** [stacklok/mecatl#876](https://github.com/stacklok/mecatl/issues/876).
**Plan PR:** [stacklok/mecatl#1422](https://github.com/stacklok/mecatl/pull/1422)
**Approved baseline:** absent until approved

Redis durable followers get their own bounded connection pool and fail-fast admission limit,
so any admitted follower can block without consuming the durability capacity used by session
saves, event appends, and metadata operations. The Redis store owns every admitted follower
until its iterator ends and can cancel and join them during shutdown without force-closing a
client used by writes.

## Human decisions

- [x] Use released `github.com/stacklok/toolhive-core` v0.0.46 and migrate redisstore from the deprecated `redis` facade to `redisconn`. — Decision: one supported connection API supplies the separate bounded clients.
- [x] Bind one durability client and one follow client atomically to each credential generation. — Decision: a candidate generation is published only when the complete pair is usable; partial candidates are closed.
- [x] Route `Follow:true` through the follow client under full-iterator admission, and `Follow:false` through the durability client. — Decision: every Redis operation made by one read follows the selected resource boundary.
- [x] Default both follow-pool size and maximum followers to 32 and require `1 ≤ maxFollowers ≤ poolSize`. — Decision: expose the exact CLI and Helm names in the interface contract below.
- [x] Distinguish storage admission exhaustion from slow transport consumption. — Decision: add `port.ErrEventFollowCapacity`, public code `watch_capacity`, gRPC `RESOURCE_EXHAUSTED`, and an HTTP watch terminal SSE error after its existing `200` response; the TypeScript durable-watch client resumes it from the last processed cursor.
- [x] Make follower shutdown store-owned. — Decision: closing the store rejects new admissions, cancels active followers, joins them within the existing bounded close grace, and may force-close only follow clients; active iterators end cleanly. ADR 0328 narrowly supersedes ADR 0240's no-force-close rule for isolated follow clients only.
- [x] Preserve bounded one-second `XREAD` slices and add no telemetry. — Decision: the new hard bounds and typed failure are sufficient operator feedback for this change.
- [x] Keep consumer groups, authoritative Pub/Sub, local watch hubs, and multi-stream sharding out of scope. — Decision: Redis Streams remain the authoritative full-feed source for every watcher.

## Interface contract

- **gRPC / protobuf:** No protobuf messages, fields, methods, or numbers change. Existing `WatchSessionEvents` reports `watch_capacity` with gRPC `RESOURCE_EXHAUSTED`. Existing HTTP `GET /v1/sessions/{id}/watch` still commits `200 text/event-stream`; capacity exhaustion is its terminal `event: error` frame with `{"code":"watch_capacity",...}` rather than a later HTTP status. The TypeScript `WatchConnection` classifies `watch_capacity` as resumable and reconnects with its last processed cursor and unchanged filter under the existing bounded backoff.
- **Exported Go APIs / interfaces:** Add `port.ErrEventFollowCapacity` in `engine/port` as the sentinel yielded by a `CursorEventLog.ReadAfter` with `ReadOptions.Follow` when the store cannot immediately admit another follower. `CursorEventLog`, `ReadOptions`, and all method signatures remain unchanged.
- **Tool schemas:** None — durable event watch is a host transport and storage concern; no model-visible tool or tool schema changes.
- **CLI / config:** `mecak8s` adds `--redis-follow-pool-size` and `--redis-max-followers`, both defaulting to `32`. Helm adds `redis.follow.poolSize` and `redis.follow.maxFollowers`, also defaulting to `32`, and renders both flags. Effective startup configuration must satisfy `1 ≤ redis-max-followers ≤ redis-follow-pool-size`; CLI validation and Helm's schema-plus-template validation reject invalid values rather than clamp. The follow client's `redisconn.Config` sets both `PoolSize` and `MaxActiveConns` to the effective pool size; the durability client retains its existing defaults.
- **Events / persistence:** None — no session event, watch envelope field, cursor encoding, Redis key, snapshot, migration, or other persisted state changes. Admission and follower lifecycle state reset on restart.
- **Security / authority:** The store-owned admission/lifecycle registry is process-scoped and has no caller principal or authorization role. It bounds backend resource use only. Shutdown may force-close an isolated follow client after the first join wait, but never force-closes a durability client merely because a follower failed to exit. Existing credential-file, verified-TLS, secret-redaction, and caller ownership rules remain unchanged.
- **Compatibility / migration:** Additive Go sentinel, stable server/TypeScript error code, CLI flags, and Helm values; existing deployments receive effective `32`/`32` defaults. `MECATL_ERROR_CODES` and therefore `ServerErrorCode` add `watch_capacity`; no new TypeScript error class is introduced. The internal adapter migrates from deprecated `toolhive-core/redis` to released `toolhive-core/redisconn` v0.0.46 without changing Redis wire data or credential semantics. ADR 0328 supersedes ADR 0250's shared-pool sizing deferral and ADR 0240's no-force-close rule only for the newly isolated follow clients; all durability and other leased Redis clients retain ADR 0240 behavior. `watch_lagging` remains distinct and continues to mean a client failed to consume its bounded delivery buffer.

## In scope — 4 scenarios, in implementation order

### Scenario 1 — Credential generations isolate follow traffic from durability traffic

The durable ownership decision is recorded in [ADR 0328](../adr/0328-isolated-redis-follow-capacity.md), while credential reload retains the atomic-generation behavior described by the [architecture](../architecture.md).

**Acceptance:**
- AC1.1: Each published Redis credential generation owns a usable durability client and a distinct follow client whose `PoolSize` and `MaxActiveConns` equal the configured follow-pool size.
  - verify: `TestRedisFollowCapacity_Scenario1_GenerationPublishesIsolatedClientPair`
- AC1.2: A candidate pair is published atomically; failure constructing or probing either half closes the partial candidate and leaves the current generation serving both paths.
  - verify: `TestRedisFollowCapacity_Scenario1_CredentialReloadSwapsPairAtomically`
- AC1.3: Every Redis command in `Follow:true` uses a follow client, while every command in `Follow:false` and all non-follow storage operations use the durability client. The follower holds admission for its full iterator but acquires and releases the current credential generation around each bounded read cycle and before yielding, so an idle iterator never pins a retired pair.
  - verify: `TestRedisFollowCapacity_Scenario1_ReadRoutingIsComplete`
- AC1.4: Followers parked at the admission maximum do not delay or consume connections from a concurrent `AppendEvent` or `Save`.
  - verify: `TestADR_0328_FollowersDoNotStarveDurability`

### Scenario 2 — Follower admission is bounded, fail-fast, and resumable

The admission error extends the typed watch contract from [ADR 0250](../adr/0250-durable-cursors-and-watch.md) without conflating backend capacity with the server delivery buffer.

**Acceptance:**
- AC2.1: `Follow:true` acquires one admission before its first Redis operation, holds it for the iterator's complete lifetime, and releases it on normal completion, error, context cancellation, or an early consumer break.
  - verify: `TestRedisFollowCapacity_Scenario2_AdmissionCoversIteratorLifetime`
- AC2.2: When all follower admissions are held, the next `Follow:true` read immediately yields an error wrapping `port.ErrEventFollowCapacity`, performs no Redis operation, and does not wait for a pool timeout.
  - verify: `TestADR_0328_CapacityFailsFast`
- AC2.3: `Follow:false` bypasses follower admission even when the follow limit is saturated.
  - verify: `TestRedisFollowCapacity_Scenario2_ReplayBypassesAdmission`
- AC2.4: Both watch transports classify the sentinel as public `watch_capacity`; gRPC returns `RESOURCE_EXHAUSTED`, while HTTP retains `200` and emits a terminal SSE error frame. `watch_lagging` behavior remains unchanged.
  - verify: `TestRedisFollowCapacity_Scenario2_TransportClassification`
- AC2.5: TypeScript `MECATL_ERROR_CODES` includes `watch_capacity`, and `WatchConnection` treats it like `watch_lagging`: it reconnects under bounded backoff from the last processed cursor and unchanged run filter rather than terminating or replaying acknowledged envelopes.
  - verify: vitest:sdk/typescript/test/attach-reconnect.test.ts — `watch_capacity` resumes from the attachment checkpoint under the same filter

### Scenario 3 — The Redis store owns and bounds follower shutdown

The lifecycle follows [ADR 0027](../adr/0027-cloud-native.md): the store rejects new work at close, cancels its owned operations, and does not let one external client close stall process shutdown indefinitely.

**Acceptance:**
- AC3.1: A `Store.Close` racing a `Follow:true` admission is linearized: the read either fails with the existing internal `errStoreClosed`, or is registered as store-owned and cancelled before Close returns; no admitted iterator can miss the close snapshot.
  - verify: `TestRedisFollowCapacity_Scenario3_CloseLinearizesWithAdmission`
- AC3.2: Close cancels every cooperative active follower, joins it within the existing bounded close grace, and its iterator ends without yielding shutdown as an event-log fault or producing an outstanding-operation warning.
  - verify: `TestRedisFollowCapacity_Scenario3_CloseCancelsAndJoinsFollowers`
- AC3.3: If cancellation alone does not release a follower before the first bounded wait, close asynchronously force-closes only its follow client; a read unblocked by that close is joined within the total close grace and its iterator still ends cleanly, while the paired durability client remains usable by an existing lease.
  - verify: `TestADR_0328_ForceCloseIsFollowOnly`
- AC3.4: Store close returns by its fixed total grace even if an injected pathological follow read and client close ignore cancellation; the closed lifecycle retains ownership of late cleanup, admits no new work, and does not force-close a durability client.
  - verify: `TestRedisFollowCapacity_Scenario3_PathologicalCloseRemainsBounded`
- AC3.5: A follower that remains attached across credential rotation releases the retired client pair after its current one-second read cycle and continues on the new follow client without releasing its process-wide admission; retired pairs close without waiting for iterator termination.
  - verify: `TestRedisFollowCapacity_Scenario3_RotationDoesNotPinGeneration`

### Scenario 4 — Operators can configure and audit the bounds

The exact flags and Helm values are part of the storage-free deployment surface described in the [mecak8s architecture](../architecture.md), and process-lifetime ownership remains explicit in [ADR 0027](../adr/0027-cloud-native.md).

**Acceptance:**
- AC4.1: Omitted configuration resolves to follow-pool size 32 and maximum followers 32; the two CLI flags reach redisstore unchanged.
  - verify: `TestADR_0328_CLIConfigAndDefaults`
- AC4.2: CLI startup rejects zero, negative, or `maxFollowers > poolSize` effective values. Helm JSON schema rejects non-positive/non-integer fields, and a chart template helper rejects the cross-field `maxFollowers > poolSize` relationship; neither surface clamps.
  - verify: `TestRedisFollowCapacity_Scenario4_InvalidBoundsFailClosed`
- AC4.3: Helm defaults `redis.follow.poolSize` and `redis.follow.maxFollowers` to 32 and renders the corresponding flags from valid overrides.
  - verify: `TestRedisFollowCapacity_Scenario4_HelmValuesRenderFlags`
- AC4.4: ADR 0027 List 1 gains separate rows for per-generation isolated follow clients and for the process-scoped admission/lifecycle registry, including owner, scope, cleanup, re-attach, and evidence; the change explicitly adds no List 2 row because neither resource contains durable state.
  - verify: inspection — [ADR 0027](../adr/0027-cloud-native.md) contains both cited List 1 rows and an explicit no-List-2 statement; `task docs` validates citations.
- AC4.5: Living architecture and implementation notes, canonical `user-docs/` deployment and gRPC/HTTP/TypeScript references, compatibility pointers under `docs/usage/`, Helm documentation/schema, generated TypeScript API docs and API snapshots, and the engine changelog/API baseline describe the exact limits and typed error. No new metric, trace, or diagnostic counter is introduced.
  - verify: `task docs`, `task site:build`, and `task api:check`

## Out of scope

| Item | Defer-to | Decision |
|---|---|---|
| Per-principal, per-session, or cross-replica watch admission | Later server admission design | redisstore has no caller authority; this issue provides a per-process backend bound only. |
| Consumer groups | Not planned for session watch | Every watcher requires the full stream, while a consumer group distributes records among consumers. |
| Redis Pub/Sub as authoritative delivery | Not planned | The durable Stream remains authoritative; Pub/Sub could only be a future lossy wake-up hint followed by Stream drain. |
| Per-session local hubs and multi-stream `XREAD` sharding | Later scale phase | Add only if measured watcher scale exceeds the isolated per-process pool. |
| Main durability-pool tuning | Separate operator-capacity work | This plan isolates followers and preserves durability-client defaults. |
| New metrics or telemetry | Separate observability proposal | Hard limits and the existing typed transport error are the approved surface. |

## Definition of done

1. Applicable `task lint`, `task test`, `task docs`, `task site:build`, and `task api:check` gates pass.
2. `task ac-trace-strict` resolves every named proof when the plan becomes `landed`.
3. `go run ./cmd/mecademo` remains green for runtime changes.
4. The implementation PR links the Plan / Interface PR and approved commit and reports interface conformance.
5. `/panel-review` reports no ship blockers or unwaived reviewer failures.

## Deferred decisions and known risks

- Admission fairness is the Go runtime's semaphore wake-up behavior; no ordering or starvation guarantee is added.
- The default of 32 is a bounded deployment starting point, not an autosizing promise. Operators must size it against Redis connection budgets and expected concurrent watches.
- A pathological injected Redis implementation may ignore both context cancellation and client close. `Store.Close` stays bounded and retains ownership of that late cleanup rather than violating the durability-client boundary; the supported go-redis client observes one of those stop signals.
