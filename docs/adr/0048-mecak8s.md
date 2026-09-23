# ADR 0048 — mecak8s: Kubernetes-native agent harness (storage-free, managed-service state)

- Status: Accepted
- Date: 2026-06-24
- Scope: a new `cmd/mecak8s` binary, a new `internal/adapter/redisstore` adapter, a drain gate in `internal/adapter/server.Service`, k8s manifests, and a kind-based e2e suite
- Supersedes: none
- Superseded by: none

## Context

The cloud-native arc ([ADR 0027](./0027-cloud-native.md)) shipped the four
ports that make a Kubernetes-native posture *expressible* —
`port.SessionStore`, `port.EventLog`, `port.SessionLease`, `port.PrunableStore`
— plus the in-cluster lease adapter (`internal/adapter/k8slease`, a
`coordination.k8s.io/v1` Lease per session, built but unwired by default).
The cloud-native harness kit definition (PR #71) generalises the arc into a
set of properties: disposable process, externalized state, single-writer
multi-replica, durable record, operability over a network, everything behind a
port. The remaining gap was **demonstration**: no binary, no manifests, no e2e
proof showed these properties working together on a real cluster.

The design went through three iterations before landing:

1. **v1** proposed a RWO PVC shared by `replicas:2` agent pods. A review panel
   (devops-expert, kubernetes-deployment-expert, software-architect) caught a
   **critical** flaw: RWO pins to one node, so two pods on different nodes
   can't both mount it; kind's StorageClass is RWO-only. A PVC on the agent
   conflates application-level single-writer (the lease, per session id) with
   volume-attachment single-writer (a node-level constraint the lease doesn't
   solve).

2. **v2** moved the PVC to a custom `cmd/mecak8s-store/` StatefulSet serving
   the gRPC driver protocol over jsonlstore-on-PVC. This fixed the
   multi-attach problem but still had mecatl owning a PVC — a custom store
   binary wrapping a filesystem, which is reinventing storage rather than
   using a managed service (Google Cloud principle #3).

3. **v3** kept the custom store binary but made the agent storage-free. Still
   a PVC on something we own.

4. **v4 (this ADR)** eliminates the custom store binary entirely. The agent
   talks to **Redis** — a standard managed service — directly as a client. A
   new `internal/adapter/redisstore` adapter meets the existing ports. No PVC
   on anything mecatl owns. Redis IS the stateful backing service.

## Decision

Ship mecak8s as a **thin composition-root binary** (`cmd/mecak8s`) that reuses
the existing `app.Build` assembly with k8s-native defaults. The agent pods are
**storage-free**: every piece of state is a managed service the agent talks to
over the network.

### State topology

| State | Service | Adapter | Port |
|---|---|---|---|
| Session snapshots | **Redis** | `internal/adapter/redisstore` (new) | `port.SessionStore` |
| Durable event log | **Redis** | `internal/adapter/redisstore` (same adapter) | `port.EventLog` |
| Session retention (GC) | **Redis** | `internal/adapter/redisstore` | `port.PrunableStore` |
| Single-writer lease | **k8s API server** | `internal/adapter/k8slease` (shipped) | `port.SessionLease` |

No custom store binary. No PVC on the agent. No PVC on anything mecatl owns.
The agent is a client of Redis and the k8s API server — that's it.

### The new Redis adapter

A single `Store` type in `internal/adapter/redisstore/` meeting
`port.SessionStore` + `port.EventLog` + `port.PrunableStore` +
`port.ToolCallRecorder` over `go-redis/v9` (already an indirect dep in `go.mod`).
It reuses `sessnap.Marshal`/`Unmarshal` (the same snapshot format jsonlstore and
the gRPC driver use — `sessnap-json/1`); it is a transport, not a format. Event
log records use `RPUSH`/`LRANGE` (append order is the contract). Validated by
the SAME `storeconformance.Run` + `eventlogconformance.Run` +
`storeconformance.RunPrunable` suites jsonlstore passes, tested offline against
`miniredis` (already in `go.sum`).

### The drain gate

A small, additive change to `internal/adapter/server.Service`: an
`atomic.Bool draining` + `Service.Drain()` + `Service.ActiveRuns() int`,
checked in `acquireLease` (covering `StartRunContent` + `resumeFromAwaiting` +
`Approve`), returning `ErrUnavailable` (HTTP 503) before leasing. The loop
stays storage-agnostic. mecated is byte-identical (draining starts false). The
agent binary uses a bounded `GracefulStop` (goroutine + 30s timer →
`grpcSrv.Stop()` fallback); in-flight runs are cancelled, not drained, and
`Recover`-able on the successor (issue #51).

### The e2e proof

A kind-based e2e suite (`e2e/k8s/`, gated behind a `kind_e2e` build tag, run
via `task e2e:k8s` — NOT part of `task test`) spins a real cluster with a
Redis StatefulSet + two storage-free agent replicas and asserts: (1) lease
exclusion across replicas (HTTP 409 on conflict), (2) graceful failover
releases the lease before TTL, (3) session persistence across pod restart
(Redis-backed). Fully offline via `--mock` (mockllm).

## Consequences

**Positive.** The cloud-native properties the architecture was built for are
finally *demonstrated* on a real cluster, not just modeled. The agent is
genuinely disposable (kill any pod, a survivor resumes from Redis), genuinely
multi-replica (the lease prevents double-writes), and genuinely state-free
(swap the Redis StatefulSet for a managed ElastiCache/MemoryStore and zero
agent code changes). The Redis adapter is a new adapter behind existing ports
— same shape as jsonlstore/memstore, validated by the same conformance suites,
no `engine/` or `port` or proto changes. The drain gate is a small additive
improvement to `Service` that benefits any deployment wanting graceful
shutdown, not just mecak8s.

**Costs.** A new external dependency (`go-redis/v9`, already indirect, promoted
to direct). A new adapter to maintain (but it's small and conformance-tested).
The drain gate is new server code (~15 lines, additive — the one piece that
touches `internal/adapter/server`). The kind e2e is slow (~3-5 min) and needs
Docker, gated behind a build tag. Redis is a single point of failure in the MVP
(1 replica; for HA use Sentinel/Cluster or a managed Redis — the adapter
doesn't change). In-flight runs are cancelled, not drained — a multi-minute
LLM turn cannot survive a rolling update within
`terminationGracePeriodSeconds: 60`; this is honest: the pod is disposable, the
session is not (it's `Recover`-able via issue #51).

**Not done here (deliberately).** No CRD/Operator (a Deployment matches the
server shape; an Operator is for "agent run as a declarative k8s object"). No
HPA (CPU scaling is wrong for an LLM-I/O-bound loop; custom-metrics HPA on
active-runs is a future step). No managed Redis (ElastiCache/MemoryStore) —
the adapter talks to Redis generically; swapping is a manifest change. No fix
for mecated's unbounded `GracefulStop` (pre-existing; mecated is
single-replica; filed as a follow-up).

## See also

- [ADR 0027 — Cloud-native arc](./0027-cloud-native.md) — the four ports + the
  k8s lease adapter this builds on.
- [ADR 0005 — Driver seams](./0005-driver-seams.md) — the port/driver protocol
  that makes remote services first-class.
- [ADR 0028 — mecatequi](./0028-mecatequi.md) — the precedent for a new binary
  that composes `app.Build` with a different posture.
- [Historical mecak8s execution plan](https://github.com/stacklok/mecatl/blob/33a3747d9008691d4d51a872c9e82c050c43fafa/docs/design/MECAK8S-PLAN.md) — the living
  execution plan (step sequence, manifests, e2e architecture, risks).
- [Historical readiness tracker](https://github.com/stacklok/mecatl/blob/33a3747d9008691d4d51a872c9e82c050c43fafa/docs/design/PRODUCTION-READINESS.md) —
  the status tracker (mecak8s row added when implementation ships).
- [`docs/usage.md`](../usage.md) — the operator guide (mecak8s section added
  when implementation ships).
- [`docs/architecture.md`](../architecture.md) — the living architecture
  reference (mecak8s binary paragraph added when implementation ships).
