# mecak8s — MVP Implementation Plan

> **Lifecycle: Execution plan — living.** This doc tracks the implementation of
> [ADR 0048](../adr/0048-mecak8s.md) (the *why*, frozen). Current behaviour
> folds into [`docs/architecture.md`](../architecture.md) and
> [`docs/usage.md`](../usage.md) when the code ships; status lives in
> [`PRODUCTION-READINESS.md`](./PRODUCTION-READINESS.md). Per
> [ADR 0002](../adr/0002-documentation-lifecycle.md), this is a living execution
> doc, not a frozen decision record.

## TL;DR

mecak8s is a **thin composition-root binary** (`cmd/mecak8s`) that reuses the
existing `app.Build` assembly + `server.Service` + the already-shipped k8s
lease adapter (`internal/adapter/k8slease`), wired with k8s-native defaults.
The agent pods are **storage-free** — every piece of state (session snapshots,
the durable event log, the single-writer lease) is a **managed service** the
agent talks to over the network:

- **Session store + event log → Redis** (a new `internal/adapter/redisstore`
  adapter implementing `port.SessionStore` + `port.EventLog` + `PrunableStore`,
  validated by the existing conformance suites).
- **Single-writer lease → the k8s API server** (coordination.k8s.io Lease per
  session, via the already-shipped `internal/adapter/k8slease`).

No custom store binary. No PVC on anything mecatl owns. The agent talks to
Redis directly as a service — exactly the 12-factor Factor VI discipline
("stateless processes; any state in a stateful backing service") and the
managed-service posture (Google Cloud principle #3).

The proof is a **kind-based e2e test** that spins a real cluster with a Redis
service + two storage-free agent replicas, and asserts (1) lease exclusion
across replicas, (2) graceful failover releases the lease before TTL, (3)
session persistence across pod restart.

### The new work

1. **`internal/adapter/redisstore`** — a Redis-backed adapter implementing
   `port.SessionStore` + `port.EventLog` + `PrunableStore`, validated by the
   SAME `storeconformance`/`eventlogconformance` suites jsonlstore passes.
   This is the "implement the relevant ports" work — the ports already exist;
   this is a new adapter meeting them.
2. **`cmd/mecak8s/`** — the agent binary (thin peer of mecated, storage-free).
3. **A drain gate** in `internal/adapter/server.Service` (the one new server code).
4. **`deploy/mecak8s/`** manifests (agent Deployment + Redis StatefulSet + RBAC).
5. **`e2e/k8s/`** kind-based e2e proof.

No `engine/` changes. No `port` changes. No proto changes. The Redis adapter is
a new adapter behind existing ports — the same shape as jsonlstore, memstore,
and the gRPC driver.

---

## 1. State topology: storage-free agents, managed-service state

### The principle

An agent pod holds **zero durable state**. Every piece of state is a **managed
service** the agent talks to over the network:

| State | Service | Adapter | Port |
|---|---|---|---|
| Session snapshots | **Redis** | `internal/adapter/redisstore` (new) | `port.SessionStore` |
| Durable event log | **Redis** | `internal/adapter/redisstore` (same adapter) | `port.EventLog` |
| Session retention (GC) | **Redis** | `internal/adapter/redisstore` | `port.PrunableStore` |
| Single-writer lease | **k8s API server** | `internal/adapter/k8slease` (shipped) | `port.SessionLease` |

This is the cloud-native posture: the pod is stateless compute, managed
services hold the state. No PVC on the agent. No custom store binary wrapping
a PVC. The agent is a **client** of Redis and the k8s API server — that's it.

### The topology

```
┌──────────────────────────────────────────────────────────────────────┐
│  Namespace: mecatl                                                   │
│                                                                      │
│  ┌─────────────────────┐        ┌─────────────────────┐              │
│  │ mecak8s-agent (r1)  │        │ mecak8s-agent (r2)  │              │
│  │ STORAGE-FREE        │        │ STORAGE-FREE        │              │
│  │ no PVC, no local    │        │ no PVC, no local    │              │
│  │ --redis-url         │        │ --redis-url         │              │
│  │   redis:6379        │        │   redis:6379        │              │
│  │ --session-lease-    │        │ --session-lease-    │              │
│  │   k8s-namespace=   │        │   k8s-namespace=   │              │
│  │   mecatl            │        │   mecatl            │              │
│  └───┬──────────┬─────┘        └───┬──────────┬─────┘              │
│      │          │                  │          │                      │
│      │ Redis    │ k8s API          │ Redis    │ k8s API              │
│      │ (store + │ (lease)          │ (store + │ (lease)              │
│      │  eventlog)│                  │  eventlog)│                     │
│      │          │                  │          │                      │
│      ▼          │                  ▼          │                      │
│  ┌────────┐     │              ┌────────┐     │                      │
│  │ Redis  │     │              │ (same  │     │                      │
│  │Stateful│     │              │ Redis) │     │                      │
│  │Set r1  │     │              └────────┘     │                      │
│  └────────┘     │                  ▲          │                      │
│                 │                  │          │                      │
│           ┌─────┴──────────────────┴──────────┘                      │
│           ▼                                                            │
│  ┌──────────────────────┐                                            │
│  │ coordination.k8s.io  │                                            │
│  │ Lease per session    │                                            │
│  │ (k8s API server)     │                                            │
│  └──────────────────────┘                                            │
└──────────────────────────────────────────────────────────────────────┘
```

- **Redis** (StatefulSet, `replicas:1`): the managed stateful backing service.
  A standard Redis instance — we don't write a custom binary, we deploy Redis.
  The agent talks to it directly via `go-redis/v9` (already an indirect dep in
  `go.mod`). For production, swap for a managed Redis (ElastiCache, MemoryStore,
  Upstash) — the adapter doesn't change.

- **mecak8s-agent** (Deployment, `replicas:2`): storage-free pods. They connect
  to Redis (`--redis-url`) and the k8s API server (in-cluster, for leases). No
  PVC, no `--store-dir`, no local files. Pod failover = release-on-shutdown (or
  TTL lapse) → survivor acquires → resumes from Redis. **Pure compute.**

### Why this is the right cloud-native posture

1. **No PVC on anything mecatl owns.** The agent is storage-free. Redis is a
   standard managed service — deployed as a StatefulSet for the MVP, swappable
   for a cloud-managed Redis in production with zero agent changes.
2. **The ports already exist.** `port.SessionStore` + `port.EventLog` +
   `port.PrunableStore` + `port.SessionLease` are all shipped. The Redis adapter
   is a new adapter meeting existing ports — same shape as jsonlstore/memstore.
3. **The conformance suites already exist.** `storeconformance.Run` +
   `eventlogconformance.Run` + `storeconformance.RunPrunable` validate any
   adapter against the same contract. The Redis adapter passes them — tested
   against `miniredis` (already in `go.sum`) for offline CI.
4. **12-factor Factor VI.** "Processes are stateless and share nothing. Any
   data that needs to persist must be stored in a stateful backing service."
   Redis is the stateful backing service. The agent is the stateless process.
5. **Managed-service posture (Google principle #3).** Use managed services
   rather than reinventing storage. Redis is battle-tested; a PVC-wrapping
   custom binary is not.
6. **The lease is also a managed service.** `coordination.k8s.io` Lease is
   served by the k8s API server — the agent talks to it over the network via
   the in-cluster ServiceAccount token. No local state.

---

## 2. The Redis adapter (`internal/adapter/redisstore`)

### What it implements

A single `Store` type meeting four ports:

| Port | Redis operations |
|---|---|
| `port.SessionStore` (Save/Load) | `HSET mecatl:session:<id> blob <sessnap> mtime <now>` / `HGET mecatl:session:<id> blob` |
| `port.EventLog` (Append/Read) | `XADD mecatl:events:<id> * r <json-line>` / `XRANGE mecatl:events:<id> - +` |
| `port.CursorEventLog` (AppendEvent/ReadAfter) | `XADD` (the entry ID is the cursor) / `XREAD [BLOCK] COUNT n STREAMS mecatl:events:<id> <id>` |
| `port.PrunableStore` (List/Delete) | `SCAN mecatl:session:*` / `DEL mecatl:session:<id> mecatl:events:<id> mecatl:tools:<id> mecatl:events-gen:<id>` |
| `port.ToolCallRecorder` (Record/List) | `RPUSH mecatl:tools:<id> <json>` / `LRANGE mecatl:tools:<id> 0 -1` |

The event log is the one **Stream** in this layout (the rest are hashes and
lists): a LIST offers no blocking read and no stable per-entry identity, so
durable cross-process follow and non-positional cursors both need `XADD` IDs.
See [ADR 0250](../adr/0250-durable-cursors-and-watch.md). A LIST written by an
earlier release stays readable and is converted in place by the next append.

### Key design decisions

- **Snapshot format:** reuses `sessnap.Marshal(s)` / `sessnap.Unmarshal(b)` —
  the SAME format jsonlstore and the gRPC driver use (`sessnap-json/1`). The
  adapter is a transport, not a format; the snapshot crosses as an opaque blob
  (same as the gRPC driver contract). No new encoding.
- **Event log format:** reuses the jsonlstore event record shape
  (`{V: "redisstore-eventlog/1", Ev: <event-json>}`). Append order is preserved
  by `XADD`; `XRANGE` returns in append order (the conformance contract is raw
  append order, NOT Seq order). A log still stored as a legacy LIST is read with
  `LRANGE` until the next append migrates it in place (ADR 0250).
- **Not-found:** `GET`/`HGET` returns redis.Nil → wrap `port.ErrSessionNotFound`
  (the contract every store adapter must follow).
- **Durability:** `Append` uses `XADD` (synchronous, durable by Redis's
  persistence config — for the MVP, Redis's default RDB/AOF is sufficient; the
  contract is "durable before nil returned," same as jsonlstore's fsync-less
  append).
- **Concurrency:** Redis is single-threaded for commands; `HSET`/`HGET`/`XADD`/`RPUSH`
  are atomic. No client-side mutex needed (unlike jsonlstore's in-process
  `sync.Mutex`). The lease serializes writers per session; Redis serializes the
  command execution.
- **`PrunableStore.List`:** `SCAN` with the `mecatl:session:*` pattern returns
  all session ids. `ModifiedAt` comes from the `mtime` field in the session
  hash (`HGET mecatl:session:<id> mtime`), since Redis keys don't carry mtime.
  `Delete` removes the session, events, and tool-call keys for the id.

### Conformance testing

The adapter is validated by the SAME suites jsonlstore passes:

```go
// internal/adapter/redisstore/conformance_test.go
func TestRedisStoreConformance(t *testing.T) {
    storeconformance.Run(t, func(t *testing.T) port.SessionStore {
        return redisstore.New(miniredisAddr(t))
    })
}
func TestRedisStorePrunableConformance(t *testing.T) {
    storeconformance.RunPrunable(t, func(t *testing.T) port.SessionStore {
        return redisstore.New(miniredisAddr(t))
    })
}
func TestRedisStoreEventLogConformance(t *testing.T) {
    eventlogconformance.Run(t, func(t *testing.T) port.EventLog {
        return redisstore.New(miniredisAddr(t))
    })
}
```

Tests use `miniredis` (already in `go.sum` as `github.com/alicebob/miniredis/v2`)
— fully offline, no Redis server needed for CI. The kind e2e uses a real Redis
StatefulSet.

### Where it lives

`internal/adapter/redisstore/` — a heavy adapter (imports `go-redis/v9`), same
tier as jsonlstore and k8slease. No depguard rule targets `internal/adapter/`
(only the core `engine/` tiers are allowlisted), so the `go-redis` import is
clean.

### Wiring into `app.Build`

A new `Config.RedisURL` field + `--redis-url` flag. When set, `buildSessionStore`
returns a `redisstore.New(url)` instead of jsonlstore/memstore. This is a small,
additive change to `internal/app/build.go` — the same shape as the existing
`SessionStoreURL`/`StoreDir` switch:

```go
// In buildSessionStore, before the StoreDir/memstore fallbacks:
if cfg.RedisURL != "" {
    st, err := redisstore.New(cfg.RedisURL)
    if err != nil { return nil, nil, nil, fmt.Errorf("redis store: %w", err) }
    cfg.diag().Log(ctx, port.LevelInfo, "session store: redis", "url", cfg.RedisURL)
    return st, st, func() { _ = st.Close() }, nil
}
```

`RedisURL` is mutually exclusive with `StoreDir` and `SessionStoreURL`
(validated in `validateDriverConfig`, same as the existing mutual exclusivity).

---

## 3. Cloud-native properties demonstrated

| Property | How mecak8s demonstrates it | Source |
|---|---|---|
| **Disposable process** (kit #1) | Kill an agent pod mid-run → a survivor reloads the snapshot from Redis + resumes. The pod holds zero durable state. | [ADR 0027](../adr/0027-cloud-native.md) Phase 1-2; 12-factor IX |
| **Externalized state** (kit #1) | Session snapshot + event log in Redis (managed service), not agent memory/disk. | ADR 0027 Phase 1, 3; 12-factor VI |
| **Single-writer / multi-replica** (kit #1) | Per-session coordination.k8s.io Lease → two agent replicas never write one session. HTTP 409 on conflict. | ADR 0027 Phase 4; k8slease adapter |
| **Durable record** (kit #1) | Event log persists approvals, compaction archives, tool results in Redis — not emitted-and-discarded. | ADR 0027 Phase 3 |
| **Stateless provider replay** (kit #2) | LLM adapters are store:false, full replay each turn. | [ADR 0016](../adr/0016-multi-provider.md) |
| **Operable over a network** (kit #5) | gRPC + HTTP/SSE API, probes, structured diagnostics to stdout, graceful drain on SIGTERM. | CNCF; 12-factor XI |
| **Everything behind a port** (kit #6) | The hexagonal core; Redis + k8s lease meet ports in composition. | [ADR 0005](../adr/0005-driver-seams.md) |

### Google Cloud 5 principles

1. **Design for automation** — declarative manifests, probes drive restart.
2. **Be smart with state** — ALL state externalized to managed services (Redis +
   k8s API server). Agent pods are pure compute.
3. **Favor managed services** — Redis (managed service for state) + k8s API
   server (managed service for coordination). No reinvented storage.
4. **Practice defense in depth** — RBAC least-privilege, PSS restricted,
   NetworkPolicy default-deny + explicit egress, secret-scrubbed env.
5. **Always be architecting** — MVP is Phase 0; CRD/operator is a later
   evolution.

---

## 4. What's new (the scope)

### 4a. `internal/adapter/redisstore/` — the Redis adapter (NEW)

```
internal/adapter/redisstore/
  redisstore.go            # Store type: New(addr) → SessionStore + EventLog + PrunableStore + ToolCallRecorder
  redisstore_test.go       # unit tests
  conformance_test.go      # storeconformance + eventlogconformance (miniredis)
```

- `New(addr string) (*Store, error)` — constructs a `go-redis/v9` client.
- `Save` → `HSET mecatl:session:<id> blob <sessnap> mtime <now>`.
- `Load` → `HGET mecatl:session:<id> blob`; redis.Nil → `port.ErrSessionNotFound`.
- `Append` → `XADD mecatl:events:<id> * r <record-json>` (delegates to
  `AppendEvent` and drops the cursor — one write path, so the two ports cannot
  disagree on the datatype).
- `Read` → `XRANGE` (or `LRANGE` for a log not yet migrated from a LIST) + an
  iterator yielding decoded events, SKIPPING gap markers.
- `AppendEvent`/`AppendGap`/`ReadAfter` → the `port.CursorEventLog` half: `XADD`
  returns the entry ID that IS the cursor; `ReadAfter` is `XREAD` (exclusive of
  the given ID), with `BLOCK` in bounded slices when following.
- `List` → `SCAN MATCH mecatl:session:*` + `HGET mtime`.
- `Delete` → `DEL mecatl:session:<id> mecatl:events:<id> mecatl:tools:<id>
  mecatl:events-gen:<id>` (the generation key goes with the log, or a recreated
  log would inherit the old positional basis).
- `Close` → closes the Redis client.
- Conformance: passes `storeconformance.Run` + `RunPrunable` +
  `eventlogconformance.Run` + `eventlogconformance.RunCursor` over `miniredis`
  (offline).

### 4b. `internal/app/build.go` — Redis wiring (MODIFIED, additive)

- New `Config.RedisURL string` field + `--redis-url` flag.
- `buildSessionStore`: when `RedisURL` is set, return `redisstore.New(url)`.
- `validateDriverConfig`: `RedisURL` mutually exclusive with `StoreDir` +
  `SessionStoreURL`.
- ~10 lines of composition code, additive, byte-identical when empty.

### 4c. Drain gate (the one new server code)

**Finding (panel H1+L1):** `Service` has no "stop accepting new runs" gate.
`GracefulStop()` is unbounded.

**Fix:**
1. `atomic.Bool draining` + `Service.Drain()` + `Service.ActiveRuns() int` in
   `internal/adapter/server/service.go`. Checked in `acquireLease` (covers
   `StartRunContent` + `resumeFromAwaiting` + `Approve`), returns
   `ErrUnavailable` (HTTP 503) before leasing. ~15 lines, additive.
2. Bounded `GracefulStop` in `cmd/mecak8s/serve.go`: goroutine + 30s timer →
   `grpcSrv.Stop()` fallback. In-flight runs cancelled, not drained;
   Recover-able on successor (issue #51).

### 4d. `cmd/mecak8s/` — the storage-free agent binary

~250-300 lines, thin peer of mecated:

```
cmd/mecak8s/
  main.go       # flags → app.Build → serve → shutdown; os.Exit
  flags.go      # k8s-specific flags + appConfig() via cliconfig.ProviderFlags
  serve.go      # signal ctx, HTTP/gRPC/drain listeners, health probes, bounded GracefulStop
  main_test.go  # offline test over mockllm+memfs
```

- **Storage-free:** `--redis-url` points at Redis; NO `--store-dir`, NO PVC.
- Dynamic `ReadyFunc` (`!draining && redisOK`).
- Drain gate armed on SIGTERM + plaintext drain-only `GET /drain` (preStop).
- Bounded `GracefulStop`.
- `--session-lease-k8s-namespace` defaults to `mecatl`.
- `--headless=true` + `--posture=auto` defaults.
- Provider flags via `cliconfig`.
- Plaintext drain-only listener on `0.0.0.0:8082` serves `GET /drain` (blocks
  ~3s, outside auth); the normal HTTP listener has no drain route.

**Shutdown sequence:**
```
SIGTERM (or preStop httpGet /drain)
  1. arm drain gate (atomic.Bool = true)
     → acquireLease returns ErrUnavailable (503) before leasing
     → /readyz returns false → endpoint controller removes pod
     → /drain handler blocks ~3s (endpoint propagation window) then returns
  2. log "draining: N active runs" (Service.ActiveRuns())
  3. grpcSrv.GracefulStop() in goroutine + select 30s timer
  4. on timeout: grpcSrv.Stop() (hard) — in-flight runs cancelled,
     persist best-effort, Recover-able on successor (issue #51)
  5. httpSrv.Shutdown(10s ctx) + drainSrv.Shutdown(10s ctx)
  6. built.Close() → Service.Close():
     → stops every held-lease renewer
     → releases every held coordination.k8s.io Lease (cancel-detached short-ctx)
     → a survivor can take over IMMEDIATELY, without the 30s TTL
  7. exit 0
```

**Honest contract:** new runs are rejected (503). In-flight runs are
**cancelled** (not drained to completion) — a multi-minute LLM turn cannot
survive a rolling update within `terminationGracePeriodSeconds: 60`. Runs are
`Recover`-able on the successor (issue #51). This is the cloud-native
disposability property: the pod is disposable, the session is not.

### 4e. `deploy/mecak8s/` — the manifests

```
deploy/mecak8s/
  kustomization.yaml
  namespace.yaml            # mecatl namespace, PSS restricted
  rbac.yaml                 # ServiceAccount + Role (leases) + RoleBinding
  redis-statefulset.yaml    # Redis: StatefulSet r1, standard Redis image, RWO PVC
  redis-service.yaml        # Redis: ClusterIP, port 6379
  agent-deployment.yaml     # mecak8s-agent: Deployment r2, STORAGE-FREE, probes, lifecycle
  agent-service.yaml        # mecak8s-agent: ClusterIP, grpc+http
  pdb.yaml                 # minAvailable:1 (agent)
  networkpolicy.yaml        # default-deny + explicit egress (DNS, API 443, Redis 6379, LLM 443)
```

**Redis StatefulSet:**
- Standard `redis:7-alpine` image (not our binary — a managed service).
- `replicas: 1`, RWO PVC (Redis owns its persistence; that's its job).
- `automountServiceAccountToken: false`.
- PSS restricted (note: Redis needs to write to its data dir; the PVC mount
  handles that. The redis:7-alpine image runs as redis:999 — adjust
  securityContext or use a distroless Redis image).
- Probes: `redis-cli ping` on port 6379.

**Agent Deployment:**
- `replicas: 2`, **no PVC, no volumes** (storage-free).
- `strategy: RollingUpdate, maxSurge:1, maxUnavailable:0`.
- `terminationGracePeriodSeconds: 60`.
- `automountServiceAccountToken: true` (for leases).
- PSS restricted in full.
- `preStop: httpGet: /drain` on a named Pod-only plaintext drain port
  (`8082`, distroless-safe); Service exposes only gRPC + HTTP.
- Probes: startup (/readyz), readiness (/readyz, dynamic), liveness (/healthz).
- Args: `--redis-url=redis:6379`,
  `--session-lease-k8s-namespace=mecatl`, `--mock` (e2e) / `--openai` (prod).
- Env: `OPENAI_API_KEY` from Secret (prod) / `--mock` (e2e).

**RBAC:** ServiceAccount `mecak8s-agent`, Role with `get,create,update,delete`
on `leases` in `coordination.k8s.io` (no list/watch). RoleBinding namespace-scoped.

**NetworkPolicy:** default-deny both. Egress: DNS (53), k8s API (443), Redis
(6379), LLM provider (443). Missing any = a feature dies.

### 4f. `.ko.yaml` — one new build entry

```yaml
  - id: mecak8s
    main: ./cmd/mecak8s
    flags: [-trimpath]
    env: [CGO_ENABLED=0]
    ldflags: [-s -w]
```

(No store binary — Redis is a standard image.)

### 4g. Taskfile + e2e

```yaml
  ko:build:k8s:
    desc: Build the mecak8s image locally with ko
    cmds:
      - KO_DOCKER_REPO=ko.local ko build --local --bare ./cmd/mecak8s

  e2e:k8s:
    desc: Run the kind-based k8s e2e suite (needs kind + ko + kubectl)
    deps: [build]
    cmds:
      - go test -tags kind_e2e -count=1 -timeout 15m ./e2e/k8s/...
```

### 4h. `e2e/k8s/` — the kind-based e2e proof

```
e2e/k8s/
  suite_test.go       # TestMain: require kind/ko/kubectl; create/delete cluster
  lease_test.go       # two-pod lease exclusion (THE proof)
  failover_test.go    # graceful failover releases lease before TTL
  persistence_test.go # session survives across agent pod restart (Redis)
  helpers_test.go     # port-forward, apply, wait-ready, HTTP client
```

**Architecture:**
```
1. kind create cluster --name mecatl-e2e --image kindest/node:v1.35.8@sha256:07b2536e30b803ed61d1677a79df6115f798ce64c80f9e22f6ed45afd09323c0
2. ko build --local --bare ./cmd/mecak8s  →  ko.local/mecak8s:sha
3. kind load docker-image ko.local/mecak8s:<sha> --name mecatl-e2e
4. ko resolve -f deploy/mecak8s/  |  kubectl apply -f -
   (Redis image is pulled from Docker Hub — no ko build needed)
5. kubectl wait --for=condition=Ready pod -n mecatl -l app.kubernetes.io/part-of=mecak8s
6. Assertions via port-forward to the agent Service + individual agent pods
7. kind delete cluster --name mecatl-e2e
```

**The three tests:**

1. **Lease exclusion across replicas (THE proof):**
   - Port-forward to each agent pod directly.
   - Create a session via pod-A, start a run (mock multi-turn, lease held).
   - POST same session to pod-B → assert **HTTP 409** (lease held elsewhere).
   - After pod-A's run completes → POST to pod-B → still assert **409**
     (the lease is **session-scoped**: held past run completion, released only
     by CloseSession / shutdown, never per-run).
   - DELETE the session on pod-A (→ `releaseLease`) → POST to pod-B → assert
     **200** (release enables takeover).

2. **Graceful failover releases lease before TTL:**
   - Start a run on pod-A (holds the lease).
   - `kubectl delete pod` pod-A (graceful SIGTERM → drain → lease release).
   - Wait for replacement Ready.
   - Start a run for the same session on pod-B → assert **immediate success**
     (not after 30s TTL).
   - Control: `kubectl delete pod --force` → assert pod-B waits ~TTL.

3. **Session persistence across pod restart:**
   - Create a session + run on pod-A, reach terminal.
   - Delete pod-A.
   - Wait for replacement Ready.
   - `GET /v1/sessions/{id}` on pod-B → session survives (Redis).
   - POST follow-up → succeeds (Recover/reopen, issue #51).

**Mock provider:** `--mock` wires `mockllm`. Fully offline (no API key).

---

## 5. What is NOT in the MVP (and why)

| Deferred | Why |
|---|---|
| CRD / Operator pattern | A Deployment is simpler. Operator is for "agent run as declarative k8s object." |
| HPA | CPU scaling is wrong for LLM-I/O. Custom-metrics HPA (active-runs) is step 2. |
| Managed Redis (ElastiCache/MemoryStore) | The adapter talks to Redis generically. Swapping the StatefulSet for a managed Redis is a manifest change, zero agent code. |
| Custom store binary | Eliminated. Redis IS the store service — we deploy Redis, not a wrapper. |
| ACP, metrics subcommand, skills-promote | Strip to the daemon subset. |
| Fixing mecated's unbounded `GracefulStop` | Pre-existing; mecated is single-replica. Follow-up. |
| `Config.K8sClient` field | cmd main builds its own. Rule of Three. |

---

## 6. Risks and honest caveats

1. **In-flight runs are cancelled, not drained.** A multi-minute LLM turn can't
   survive a rolling update within `terminationGracePeriodSeconds: 60`.
   Recover-able on successor (issue #51). Honest: "new runs rejected, in-flight
   cancelled, recoverable."

2. **The drain gate is new server code** (~15 lines, additive). Unit-tested
   offline. mecated byte-identical (draining starts false).

3. **kind e2e is slow (~3-5 min) and needs Docker.** Gated behind a build tag.

4. **Redis is a single point of failure** (1 replica in the MVP). If Redis dies,
   sessions are on its PVC (survive if Redis persistence is on). For HA, use
   Redis Sentinel/Cluster or a managed Redis — the adapter doesn't change.

5. **Egress NetworkPolicy is load-bearing.** Missing the API server = lease
   fails. Missing Redis = store fails. Missing DNS = nothing resolves.

6. **Redis PSS-restricted compatibility.** The `redis:7-alpine` image runs as
   UID 999. PSS restricted requires `runAsNonRoot: true` + a non-zero UID. Set
   `runAsUser: 999` or use a distroless Redis image. Verify in the e2e.

7. **Redis persistence config.** For the MVP, Redis's default RDB snapshots are
   sufficient. For production, enable AOF (`appendonly yes`) for the EventLog
   durability contract. The adapter calls `XADD` (synchronous); Redis's
   persistence config determines durability-on-crash.

---

## 7. Implementation sequence

Each step is independently shippable, CI-green.

### Step 1: Redis adapter (`internal/adapter/redisstore/`) — ✅ DONE
- `redisstore.go` + `redisstore_test.go` + `conformance_test.go`.
- Implements `port.SessionStore` + `port.EventLog` + `PrunableStore` + `ToolCallRecorder`.
- Conformance over `miniredis` (offline).
- `task lint && task test` green.

### Step 2: Wire Redis into `app.Build` — ✅ DONE
- `Config.RedisURL` + `--redis-url` flag.
- `buildSessionStore` branch + `validateDriverConfig` mutual exclusivity.
- ~10 lines additive. Byte-identical when empty.
- `task lint && task test` green.

### Step 3: Drain gate — ✅ DONE
- `atomic.Bool draining` + `Service.Drain()` + `Service.ActiveRuns()`.
- Check in `acquireLease`. Unit-test offline.
- Fix `cmd/mecated/main.go:1129` flag help RBAC verbs (panel M2).
- `task lint && task test` green.

### Step 4: `cmd/mecak8s/` (storage-free agent binary) — ✅ DONE
- `main.go` + `flags.go` (cliconfig) + `serve.go` + `main_test.go`.
- `--redis-url`, dynamic `ReadyFunc`, drain gate, bounded `GracefulStop`, `/drain`.
- Offline test over mockllm + memfs.
- `task build` produces `bin/mecak8s`.

### Step 5: `.ko.yaml` + Taskfile — ✅ DONE
- New build entry for mecak8s. `ko:build:k8s`, `e2e:k8s` tasks.

### Step 6: `deploy/mecak8s/` manifests — ✅ DONE
- namespace, rbac, redis-statefulset, redis-service, agent-deployment,
  agent-service, pdb, networkpolicy, kustomization.

### Step 7: `e2e/k8s/` kind suite — ✅ DONE
- `suite_test.go`, `lease_test.go`, `failover_test.go`, `persistence_test.go`,
  `helpers_test.go`. Gated behind `kind_e2e` tag.

### Step 8: living docs — ✅ DONE
- Update `docs/architecture.md` (mecak8s binary paragraph + diagram + adapter table).
- Update `docs/usage.md` (mecak8s section: flags, manifests, kind e2e).
- Update `docs/design/PRODUCTION-READINESS.md` (mecak8s status row).
- `task generate` / `task docs` before commit.

### Step 9: CI workflow — ✅ DONE
- `.github/workflows/k8s-e2e.yml` on relevant-path PRs.

---

## 8. Files touched / created

```
NEW:
  internal/adapter/redisstore/redisstore.go        # the Redis adapter
  internal/adapter/redisstore/redisstore_test.go    # unit tests
  internal/adapter/redisstore/conformance_test.go  # conformance (miniredis)
  cmd/mecak8s/main.go
  cmd/mecak8s/flags.go
  cmd/mecak8s/serve.go
  cmd/mecak8s/main_test.go
  deploy/mecak8s/kustomization.yaml
  deploy/mecak8s/namespace.yaml
  deploy/mecak8s/rbac.yaml
  deploy/mecak8s/redis-statefulset.yaml
  deploy/mecak8s/redis-service.yaml
  deploy/mecak8s/redis-networkpolicy.yaml
  deploy/mecak8s/namespace-default-deny.yaml
  deploy/mecak8s/agent-deployment.yaml
  deploy/mecak8s/agent-service.yaml
  deploy/mecak8s/pdb.yaml
  deploy/mecak8s/networkpolicy.yaml
  e2e/k8s/suite_test.go
  e2e/k8s/lease_test.go
  e2e/k8s/failover_test.go
  e2e/k8s/persistence_test.go
  e2e/k8s/helpers_test.go
  .github/workflows/k8s-e2e.yml

MODIFIED:
  internal/app/build.go                              # Config.RedisURL + buildSessionStore branch (Step 2)
  internal/adapter/server/service.go                 # drain gate (Step 3)
  internal/adapter/server/drain_test.go (new)        # drain gate unit test
  cmd/mecated/main.go                               # fix flag help RBAC verbs (Step 3)
  .ko.yaml                                          # new build entry
  Taskfile.yml                                      # ko:build:k8s, e2e:k8s
  docs/usage.md                                     # mecak8s section (Step 8)
  docs/architecture.md                              # mecak8s in the binary list (Step 8)
  docs/design/PRODUCTION-READINESS.md               # mecak8s status row (Step 8)

NOT TOUCHED:
  engine/**              # no port/domain change
  contracts/**           # no proto change
  cmd/mecatequi/         # unaffected
  cmd/mecatui/           # unaffected
```

---

## 9. Verification checklist

- [x] `task lint && task test` green (Redis adapter conformance + drain gate).
- [x] `task build` produces `bin/mecak8s`.
- [x] `ko:build:k8s` produces a distroless image.
- [x] `ko:resolve:k8s` renders valid manifests.
- [x] `task e2e:k8s` passes: lease exclusion, graceful failover, persistence.
- [x] `task api:check` green (no engine API change → no `api:update`).
- [x] `task generate` / `task docs` green.
- [x] `go run ./cmd/mecademo` still prints the offline session.
- [x] mecated's existing e2e unaffected (drain gate starts false).
- [x] mecated's `--session-lease-k8s-namespace` flag help matches RBAC verbs.
- [x] Agent pod spec has NO PVC, NO volumeMounts (storage-free verified).
- [x] Redis adapter passes storeconformance + eventlogconformance over miniredis.

---

*Execution plan for [ADR 0048](../adr/0048-mecak8s.md). Related:
[ADR 0027](../adr/0027-cloud-native.md) (the cloud-native arc),
[ADR 0028](../adr/0028-mecatequi.md) (the binary-precedent).*
