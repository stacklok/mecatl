---
sidebar_position: 4
title: Cloud-native k8s with mecak8s
---

# Cloud-native k8s with mecak8s

`mecak8s` (`cmd/mecak8s`) is a thin composition-root binary that reuses the same `app.Build` assembly as `mecated`, but with Kubernetes-native defaults baked in. The agent pods hold no durable state: session snapshots and the event log live in Redis, and single-writer enforcement per session uses `coordination.k8s.io` Leases backed by the Kubernetes API server.

```mermaid
flowchart TD
    subgraph Pods["mecak8s pods (2 replicas)"]
        Pod1["Pod 1"]
        Pod2["Pod 2"]
    end
    Redis["Redis<br/>(session store + event log)"]
    K8sAPI["k8s API server<br/>(coordination.k8s.io Leases)"]

    Pods --> Redis
    Pods --> K8sAPI
```

Kill any pod. The survivor acquires the lease and resumes interrupted sessions from the Redis snapshot. The pod is disposable; the session is not.

---

## How mecak8s differs from mecated

`mecak8s` has a deliberately narrower surface than `mecated`. The differences are not runtime configuration — they are compile-time defaults and removed capabilities.

| Dimension | `mecated` | `mecak8s` |
|---|---|---|
| `--headless` default | `false` (interactive) | `true` (headless daemon) |
| `--posture` default | `strict` | `auto` |
| Project-tier ingestion + read-only child shell | granted at `auto`/`yolo` by the interactive ladder | one root-aware trust decision — explicit `--trust-project`, `trustedWorkspaces:`, or remembered trust admits BOTH; without trust, headless auto gives allow-all with neither |
| Bind address default | `127.0.0.1` (loopback) | `0.0.0.0` (pod netns) |
| Session store | In-memory or JSONL on disk (`--store-dir`); optional `--session-store-url` | **Redis only** (`--redis-url`; no `--store-dir`) |
| Session lease | Optional (`--session-lease-k8s-namespace`) | **On by default** (`--session-lease-k8s-namespace=mecatl`) |
| Prometheus `/metrics` listener | Yes | Opt-in (`--metrics-addr`, loopback only) |
| OTel / admin mux | Yes | Opt-in (`--otlp-*` push; `/metrics` loopback scrape) |
| `perf-mcp` subcommand | Yes | No |
| `skills promote` / `config` subcommands | Yes | No |
| ACP surface | Yes | No |

The `--redis-url` flag exists **only on `cmd/mecak8s`**. `mecated` does not expose it. If you want Redis-backed state with `mecated`, you need `mecak8s`.

---

## State topology

`mecak8s` maps each `port` interface to a managed service:

| State | Service | Adapter | Port |
|---|---|---|---|
| Session snapshots | Redis | `internal/adapter/redisstore` | `port.SessionStore` |
| Durable event log | Redis | `internal/adapter/redisstore` | `port.EventLog` |
| Session retention / GC | Redis | `internal/adapter/redisstore` | `port.PrunableStore` |
| Single-writer lease | k8s API server | `internal/adapter/k8slease` | `port.SessionLease` |

The `redisstore` adapter reuses `sessnap.Marshal`/`Unmarshal` — the same snapshot format `jsonlstore` and the gRPC driver use (`sessnap-json/1`). It is a transport alternative, not a new format. Event log records use `RPUSH`/`LRANGE` so append order is preserved. The adapter is validated by the same `storeconformance.Run`, `eventlogconformance.Run`, and `storeconformance.RunPrunable` suites that `jsonlstore` passes, tested offline against `miniredis`.

---

## Prerequisites

Before applying the kustomize base you need:

1. **Redis.** A Redis instance reachable from the agent pods — either the in-cluster `redis-statefulset.yaml` from the kustomize base, or a managed service (ElastiCache, MemoryStore, etc.). The `--redis-url` flag takes `host:port`, e.g. `redis:6379`.

2. **Kubernetes RBAC.** The `mecak8s-agent` ServiceAccount needs `get`, `create`, `update`, `delete` on `leases` in `coordination.k8s.io` in the `mecatl` namespace. The `rbac.yaml` in the base grants exactly those verbs — never `list` or `watch`.

3. **The `deploy/mecak8s/` kustomize base.** Contains the full topology (see below). Build the agent image with `ko` and apply:

   ```sh
   ko resolve -f deploy/mecak8s/ | kubectl apply -f -
   ```

---

## The kustomize base

`deploy/mecak8s/kustomization.yaml` includes ten resources that together form the full cloud-native topology:

| Resource file | What it creates |
|---|---|
| `namespace.yaml` | `mecatl` namespace with Pod Security Standards `restricted` profile enforced |
| `rbac.yaml` | `mecak8s-agent` ServiceAccount + Role (lease verbs only) + RoleBinding |
| `redis-statefulset.yaml` | Redis StatefulSet (single replica for in-cluster dev/test) |
| `redis-service.yaml` | ClusterIP Service for Redis at `redis:6379` |
| `namespace-default-deny.yaml` | Default-deny NetworkPolicy for the namespace (applied first) |
| `redis-networkpolicy.yaml` | Explicit ingress allow from agent pods to Redis |
| `agent-deployment.yaml` | Agent Deployment — `replicas: 2`, no PVC, storage-free |
| `agent-service.yaml` | ClusterIP Service exposing gRPC (8080) and HTTP/SSE (8081) |
| `pdb.yaml` | PodDisruptionBudget (`minAvailable: 1`) |
| `networkpolicy.yaml` | Default-deny ingress for agent pods + explicit egress allows |

Key details from `agent-deployment.yaml`:

- `replicas: 2` with `RollingUpdate`, `maxSurge: 1`, `maxUnavailable: 0` — there is always a ready survivor during a rolling update.
- `terminationGracePeriodSeconds: 60` — the bounded `GracefulStop` window.
- No PVC, no `--store-dir`. The only `volumeMount` is `/tmp` for the Go runtime and SSE buffering under `readOnlyRootFilesystem: true`.
- A `preStop` lifecycle hook calls `GET /drain` on the HTTP port. This arms the drain gate and blocks ~3 seconds for endpoint propagation before returning, so the kubelet's SIGTERM arrives after the pod has left the Service endpoints.
- PSS `restricted` in full: `runAsNonRoot`, `allowPrivilegeEscalation: false`, `capabilities: drop: ALL`, `seccompProfile: RuntimeDefault`.

The PDB ensures that voluntary disruptions (node drains, cluster autoscaler) never take both replicas offline simultaneously, keeping at least one pod available to hold leases and serve traffic.

---

## Quick start

```sh
# 1. Build and push the mecak8s image with ko, and apply the full topology.
#    ko resolves the ko:// placeholder in agent-deployment.yaml with the built ref.
ko resolve -f deploy/mecak8s/ | kubectl apply -f -

# 2. Create the API key secret. The kustomize base uses --openai; swap the env
#    var name and Secret key if you are using a different provider.
kubectl create secret generic mecak8s-openai \
  --from-literal=OPENAI_API_KEY=<your-key> \
  -n mecatl

# 3. Patch the Deployment to mount the secret (or add it to a kustomize overlay).
#    The agent-deployment.yaml in the base ships --mock for the e2e suite; replace
#    it with --openai and the secretKeyRef for production.
```

For a kustomize overlay that overrides the mock provider with a real one:

```yaml
# overlays/production/kustomization.yaml
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
bases:
  - ../../deploy/mecak8s

patches:
  - target:
      kind: Deployment
      name: mecak8s-agent
    patch: |-
      - op: replace
        path: /spec/template/spec/containers/0/args
        value:
          - --grpc-addr=0.0.0.0:8080
          - --http-addr=0.0.0.0:8081
          - --redis-url=redis:6379
          - --session-lease-k8s-namespace=mecatl
          - --headless=true
          - --posture=auto
          - --openai
      - op: add
        path: /spec/template/spec/containers/0/env
        value:
          - name: OPENAI_API_KEY
            valueFrom:
              secretKeyRef:
                name: mecak8s-openai
                key: OPENAI_API_KEY
```

---

## Readiness and health

mecak8s exposes three endpoints on the HTTP port (default `0.0.0.0:8081`), all mounted **outside** the auth boundary:

| Endpoint | Purpose |
|---|---|
| `GET /healthz` | Liveness — returns 200 unless the process is hung |
| `GET /readyz` | Readiness — returns 200 only when `!draining && redisOK`; flips to 503 on drain or Redis failure |
| `GET /drain` | preStop hook target — arms the drain gate, blocks ~3s for endpoint propagation, returns 200 |

The `readyz` probe is dynamic: it calls `svc.StorageReady`, which pings the Redis store with a 2-second timeout. A Redis failure shows up as not-ready and removes the pod from Service endpoints without a restart.

---

## Graceful shutdown

The shutdown sequence on SIGTERM (or when the kubelet calls `GET /drain` via the `preStop` hook) is:

```mermaid
sequenceDiagram
  participant K as kubelet
  participant D as /drain handler
  participant G as drain gate
  participant GS as grpcSrv.GracefulStop
  participant L as k8slease.Release

  K->>D: GET /drain (preStop)
  D->>G: svc.Drain() — new runs → 503, /readyz → not-ready
  D->>D: sleep 3s (endpoint propagation)
  D-->>K: 200 draining
  K->>K: SIGTERM
  Note over G,GS: svc.Drain() is idempotent — no double-drain
  GS->>GS: grpcSrv.GracefulStop() (30s timeout)
  note over GS: in-flight runs cancelled, Recover-able on survivor
  GS->>L: built.Close() → release all held coordination.k8s.io Leases
  note over L: cancel-detached short ctx, survivor acquires immediately
```

The `GracefulStop` timeout is 30 seconds, well within the 60-second `terminationGracePeriodSeconds`. If it elapses, the server hard-stops: in-flight runs are cancelled but immediately `Recover`-able on the successor pod from the Redis snapshot (ADR 0027 issue #51 — `Session.Recover` repairs orphaned tool calls and moves the session to idle).

Releasing leases uses a cancel-detached context with a short timeout so the release succeeds even though the signal context is already cancelled. A survivor can acquire the released lease immediately — it does not have to wait for the 30-second TTL (`--session-lease-ttl`, default 30s) to expire.

:::note[In-flight runs are cancelled, not drained]

A multi-minute LLM turn cannot complete within the 60-second termination grace period. The honest contract is: the pod is disposable, the session is not. Any interrupted session is `Recover`-able on the successor from the Redis snapshot and durable event log. All previously approved `allow-always` decisions replay automatically from the event log at the next run entry (`replayApprovals`, ADR 0027 Phase 3b), so the successor does not re-ask for already-approved tool patterns.

:::

---

## Try it: watch a session survive a pod dying

The sequence above is easiest to believe by actually doing it. This assumes the cluster from [Quick start](#quick-start) is already up with two Ready replicas.

Grab both replica names, and port-forward one of them:

```sh
POD_A=$(kubectl get pods -n mecatl -l app.kubernetes.io/component=agent -o jsonpath='{.items[0].metadata.name}')
POD_B=$(kubectl get pods -n mecatl -l app.kubernetes.io/component=agent -o jsonpath='{.items[1].metadata.name}')

kubectl port-forward -n mecatl "pod/$POD_A" 8081:8081 &
```

Create a session and run a prompt through pod A:

```sh
SESSION_ID=$(curl -s -X POST http://127.0.0.1:8081/v1/sessions \
  -d '{"workspace":"/tmp","mode":"default"}' | jq -r .session_id)

curl -s -X POST "http://127.0.0.1:8081/v1/sessions/$SESSION_ID/prompt" \
  -H 'Accept: text/event-stream' -d '{"text":"say hello"}' > /dev/null
```

Check who holds the session's lease — every session gets its own, so you may see more than one entry; yours is the one that just appeared:

```sh
kubectl get lease -n mecatl -o wide
```

Now kill pod A gracefully — the way a node drain or a rolling update would, not a hard crash:

```sh
kubectl delete pod -n mecatl "$POD_A"
```

`kubectl get pods -n mecatl --watch` to see its replacement come up. Then port-forward **pod B** — a replica that was already running the whole time, not the replacement — and send the *same* session id through it:

```sh
kubectl port-forward -n mecatl "pod/$POD_B" 8082:8081 &

curl -s -X POST "http://127.0.0.1:8082/v1/sessions/$SESSION_ID/prompt" \
  -H 'Accept: text/event-stream' -d '{"text":"are you still there?"}'
# -> 200, same conversation continues
```

That 200 is the whole point: pod B never touched this session before, yet it picked up the conversation with full context, because the conversation was never pod A's to keep — it was always in Redis. `kubectl get lease -n mecatl -o wide` again to see the `HOLDER` column has moved to pod B.

Try the same thing again, but with a hard kill this time. Pod B is now the session's holder, so force-kill *it* — not pod A, which is already gone — and retry the session via a third replica (any agent pod that isn't pod B; `kubectl get pods -n mecatl -l app.kubernetes.io/component=agent` to find one, port-forward it the same way as above):

```sh
kubectl delete pod -n mecatl "$POD_B" --force --grace-period=0
```

That retry gets a `409` with `"leased by another process"` — not the `200` a graceful kill gave you — and stays that way until the lease's TTL (`--session-lease-ttl`, default 30s) naturally expires, since there was no graceful shutdown this time to release it early. That 409 is the lease actually gating something, not just an artifact of the pod being gone.

For the scripted version of exactly this (plus the case above), see `task e2e:k8s` — it's the same failover behavior, asserted rather than eyeballed.

---

## Scaling

Add replicas freely. The `coordination.k8s.io` Lease backend enforces single-writer per session: when two pods both try to start a run on the same session, the second gets `ErrSessionLeasedElsewhere` (HTTP 409 / gRPC `FAILED_PRECONDITION`). The acquiring pod renews its lease on a background goroutine; the interval defaults to `--session-lease-ttl / 3`.

No session affinity is required on the Service. The lease is the exclusion mechanism — not routing. A client can connect to any replica; if that replica does not hold the lease, the call fails with 409 and the client retries against another replica (or waits for the in-flight run to finish).

The PodDisruptionBudget (`minAvailable: 1`) prevents voluntary disruptions from taking all replicas offline simultaneously.

For production load, note that Redis is a single point of failure in the default in-cluster setup (1 replica, no persistence). For high availability, use Redis Sentinel, Redis Cluster, or a managed service (ElastiCache, MemoryStore). The adapter talks to Redis generically — swapping the backing service is a manifest change; no adapter code changes.

---

## What you give up vs mecated

`mecak8s` trades operator surface for operational simplicity:

| Capability | mecated | mecak8s |
|---|---|---|
| Interactive TUI clients | Yes (`mecatui` connects; `--headless=false` default) | No (`--headless=true` default; headless-only) |
| Prometheus `/metrics` listener | Yes (`--metrics-addr`) | Opt-in (`--metrics-addr`, loopback only — [ADR 0098](https://github.com/stacklok/mecatl/blob/main/docs/adr/0098-headless-telemetry.md)) |
| OTel traces and runtime admin mux | Yes | Opt-in (`--otlp-*` push + the `/metrics` loopback admin mux — [ADR 0098](https://github.com/stacklok/mecatl/blob/main/docs/adr/0098-headless-telemetry.md)) |
| `perf-mcp` diagnostics subcommand | Yes | No |
| `skills promote` / `config` subcommands | Yes | No |
| JSONL on-disk session store | Yes (`--store-dir`) | No — Redis only |
| Single-replica without external state | Yes (in-memory or JSONL) | No — Redis is required |

If you need the `perf-mcp` diagnostics subcommand, the `skills promote` / `config` subcommands, or an interactive TUI client, run `mecated` instead. `mecak8s` now offers OPT-IN telemetry (`--metrics-addr` loopback scrape + `--otlp-*` push, see [ADR 0098](https://github.com/stacklok/mecatl/blob/main/docs/adr/0098-headless-telemetry.md) and the [`mecak8s` flag reference](https://github.com/stacklok/mecatl/blob/main/docs/usage/mecak8s.md)); for multi-replica deployments with `mecated` and Redis-backed state you would need to wire `--redis-url` — but that flag does not exist on `mecated`. `mecak8s` is the only binary that exposes it.

---

## What's next

- [Pick your deployment shape](/getting-started/deployment-decision.md) — decision tree comparing all four shapes.
- [Run mecated standalone](/deployment/mecated.md) — the interactive, single-server alternative with a full operator surface.
- [Embed the engine directly](/deployment/embed-engine.md) — bring your own composition if you need to run the loop inside an existing service.
- [Single-shot CI with mecatequi](/deployment/mecatequi.md) — the stateless, one-prompt-per-run shape for GitHub Actions.
