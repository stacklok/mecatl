## 8. mecak8s (Kubernetes-native agent)

### Graceful shutdown

`mecated` traps `SIGINT` / `SIGTERM`, stops accepting new work, drains the HTTP
server (bounded by a 10 s timeout) and `GracefulStop`s the gRPC server:

```
level=INFO msg="shutdown signal received; stopping servers"
```

### mecak8s — the storage-free Kubernetes-native agent (`cmd/mecak8s`, ADR 0048)

`mecak8s` is a **thin peer of `mecated`**: the same `app.Build` assembly composed with
**k8s-native defaults** — a **Redis** session store + durable event log,
a `coordination.k8s.io` Lease per session
(the in-cluster multi-replica single-writer path), a dynamic
`/readyz` (drain-gated + Redis-pinged), and a bounded `GracefulStop`. The agent pods are
**storage-free**: no PVC, no `--store-dir`, no local state — every piece of state is a
managed service the pod talks to over the network (Redis + the k8s API server). It drops
`mecated`'s `skills promote` / `config` / `perf-mcp` subcommands, ACP, and the
Prometheus/OTel admin surface. It inverts `mecated`'s interactive defaults: `--headless`
defaults **on** and `--posture` defaults to **`auto`** (an unattended daemon).

```console
$ go run ./cmd/mecak8s --redis-url redis:6379 --session-lease-k8s-namespace mecatl --openai
```

#### Flags

`mecak8s` reuses `mecated`'s provider/model/permission/skills flags via the shared CLI config wiring
(the four real-provider mains share that wiring). The k8s-native surface:

| Flag | Default | Meaning |
| --- | --- | --- |
| `--redis-url` | `""` | Redis address (`host:port`) for the session store + durable event log (ADR 0048, storage-free). The `redisstore` `Store` doubles as its own `EventLog` (like `jsonlstore`). **Mutually exclusive with `--store-dir` / `--session-store-url`** (rejected at `Build`). |
| `--session-lease-k8s-namespace` | `mecatl` | Kubernetes namespace for `coordination.k8s.io` Lease-backed session leasing (the in-cluster multi-replica single-writer path). Uses in-cluster config (or the default kubeconfig out-of-cluster). The ServiceAccount needs `get,create,update,delete` on `leases` in `coordination.k8s.io` for this namespace. Empty = no leasing. |
| `--session-lease-ttl` | `30s` | session-lease lifetime; a crashed/killed holder's lease becomes claimable after this long. |
| `--session-lease-renew-interval` | `0` | how often the per-session renewer refreshes a held lease; `0` = `--session-lease-ttl` / 3. |
| `--headless` | `true` | run NON-interactive (inverted from `mecated`): a child ask is auto-denied / routed to the opt-in `--subagent-ask-reviewer` rather than parked. Pass `--headless=false` only if a client answers asks. |
| `--posture` | `auto` | operator posture ladder (strict < trusted < auto < yolo); `auto` is the recommended unattended single-tenant tier. On this **headless** root the posture ladder never raises `TrustProject`. Explicit `--trust-project`, `trustedWorkspaces:`, or undrifted remembered trust admits BOTH repo steering and the read-only child shell (the operator vouches for `.git`); without any trust source, the fail-safe default gives allow-all approvals with neither ([ADR 0095](../adr/0095-root-aware-project-trust.md)). |
| `--reasoning-effort` | `auto` | operator reasoning-effort tier ([ADR 0055](../adr/0055-reasoning-effort.md)): `auto` (unset, provider default) or `low`/`medium`/`high`/`xhigh`/`max` (OpenAI clamps `xhigh`/`max` → `high`). Empty honours the operator-global `reasoning-effort:` setting; a per-session `CreateSession.reasoning_effort` out-ranks it. Operator-tier only. CLI out-ranks the YAML key. |
| `--mock` | `false` | canned offline mock provider (no network, no API key) — used by the kind e2e. |
| `--grpc-addr` | `0.0.0.0:8080` | gRPC listen address (a pod binds `0.0.0.0`, unlike `mecated`'s loopback). |
| `--http-addr` | `0.0.0.0:8081` | HTTP/SSE listen address (carries `/healthz`, `/readyz`, `/drain` outside auth; the API mux inside auth). |
| `--auth-token` / `--tls-cert` / `--tls-key` / `--client-ca` | `""` | bearer / TLS / mTLS — enable before binding a non-mesh address (a pod is otherwise fronted by the Service/mesh). |
| `--max-run-tokens` / `--max-team-tokens` | `0` | loop-level / team-wide cumulative token ceilings (`0` = unlimited). |
| `--mcp-server` | — | remote MCP server as `name=URL` (repeatable); a per-server bearer token is read from `MCP_<NAME>_TOKEN` (name upper-cased, token optional). Names must match `[A-Za-z0-9_]+` and be case-insensitively unique; a token-bearing URL must be `https` (or `http` to loopback). The same flag + env convention as `mecated`/`mecatequi` ([ADR 0082](../adr/0082-factory-mcp-wiring.md)). NOTE: the token is read **once at startup** and shared across all sessions for the pod's lifetime — per-run identity is a `mecatequi` property; a per-session credential source is future work (mecatl#342). |
| `--mcp-server-insecure-http` | — | EXPLICIT per-server opt-in (repeatable): name of a `--mcp-server` entry whose bearer may ride plain `http` to a non-loopback host — e.g. an in-cluster NetworkPolicy-scoped Service. Acknowledges the token travels **cleartext on the network path**; the mitigations are network-layer controls plus the short-lived token. Relaxes ONLY the http scheme gate, ONLY for that name, order-independently of the `--mcp-server` position; an unregistered name or an https/loopback/non-http URL is a startup error ([ADR 0090](../adr/0090-mcp-insecure-http-optin.md)). |
| `--llm-per-attempt-timeout` / `--llm-stream-idle-timeout` | `300s` / `180s` | LLM resilience knobs (mirror `mecated`). |
| `--metrics-addr` | `""` (off) | OPT-IN Prometheus `/metrics` listen address for a SEPARATE loopback admin listener (the admin mux — `/metrics` + pprof/expvar, ADR 0018 decision 6). MUST be loopback — a non-loopback bind is REJECTED at parse time (fail-closed; the admin mux output is secret-shaped). e.g. `127.0.0.1:9090`. |
| `--otlp-endpoint` | `""` (off) | OPT-IN OTLP trace collector endpoint (push). Empty disables tracing. |
| `--otlp-protocol` | `grpc` | OTLP transport for traces (`grpc` or `http`). |
| `--otlp-insecure` | `false` | skip TLS when dialing the OTLP collector (dev only). |
| `--otlp-metrics-endpoint` | `""` (off) | OPT-IN OTLP METRICS collector endpoint (push) — the opt-in twin to `--metrics-addr` for non-scrape deployments. Empty disables metrics push. |
| `--otlp-metrics-protocol` | `grpc` | OTLP transport for metrics (`grpc` or `http`). |
| `--otlp-shutdown-timeout` | `5s` | bound on the telemetry flush at SIGTERM (so a dead collector cannot hang shutdown). |

Plus the shared provider flags (`--openai`, `--openai-base-url`, `--openrouter-base-url`,
`--anthropic-base-url`, `--model`, `--default-provider`, `--default-model`,
`--model-alias`, `--model-slot`) and the permission/skills/agents/soul/retention knobs —
all identical to `mecated`'s (see §3).

> **`--redis-url` is mecak8s-only.** `mecated` does **not** expose it (its flag set has no
> `--redis-url`); the field exists on `app.Config` but is wired only by `cmd/mecak8s`.

#### Manifests (`deploy/mecak8s/`)

A kustomize base deploys the full topology: a `mecatl` namespace (PSS restricted), RBAC
(ServiceAccount + `leases` Role + RoleBinding), a Redis StatefulSet + Service (the managed
stateful backing service — `redis:6379`), two storage-free agent replicas (no PVC, no
volumes), an agent Service (gRPC + HTTP), a PodDisruptionBudget (`minAvailable: 1`), and a
default-deny NetworkPolicy with explicit egress (DNS 53, k8s API 443, Redis 6379, LLM 443).

```sh
# Resolve the manifests with ko (image refs baked in) and apply:
task ko:build:k8s                       # build the mecak8s image locally with ko
task ko:resolve | kubectl apply -f -    # or: ko resolve -f deploy/mecak8s/ | kubectl apply -f -
kubectl wait --for=condition=Ready pod -n mecatl -l app.kubernetes.io/part-of=mecak8s
```

The agent Deployment sets `terminationGracePeriodSeconds: 60`, `preStop: httpGet /drain`,
and `automountServiceAccountToken: true` (for leases); `strategy: RollingUpdate,
maxSurge:1, maxUnavailable:0`.

#### Kind e2e (`task e2e:k8s`)

A kind-based e2e suite lives under `e2e/k8s/` (build tag `kind_e2e` — `task build`/`task
test`/`task lint` never compile it). It spins a real kind cluster with a Redis StatefulSet +
two storage-free agent replicas and asserts three cloud-native properties (the proof of
ADR 0048):

1. **Lease exclusion across replicas** — a run started on pod-A holds the session's lease;
   the same session POSTed to pod-B returns **HTTP 409**; after pod-A releases (session
   delete / shutdown), pod-B succeeds.
2. **Graceful failover releases the lease before TTL** — deleting pod-A (graceful SIGTERM →
   drain → lease release) lets a survivor take over *immediately*, not after the 30s TTL.
3. **Session persistence across pod restart** — a session + run reaching terminal on pod-A
   survives pod-A's deletion; a follow-up on pod-B succeeds (Redis snapshot, `Recover`/reopen).

```sh
task e2e:k8s   # needs kind + ko + kubectl + Docker; NOT part of task test (~3-5 min)
```

The suite uses `--mock` (`mockllm`) — fully offline, no API key, no real LLM spend. Export
`OPENROUTER_API_KEY` to ALSO run the **live variant**: BeforeSuite patches the Deployment from
`--mock` to the real OpenRouter provider (`--default-provider=openrouter --default-model=
anthropic/claude-haiku-4.5`, the key staged via a k8s `Secret` — never a pod arg or log) and
three additional specs run real multi-second model turns through the pods, proving the lease,
the Redis snapshot, and the drain gate hold under a live LLM stream (not just the mock's instant
completion). The live specs `Skip` without the key; the mock suite is unaffected either way.

#### Honest shutdown contract

On SIGTERM (or the `preStop` `httpGet /drain`) the drain gate arms (`/readyz` → false, the
endpoint controller removes the pod) and new runs are rejected with **HTTP 503**. In-flight
runs are **cancelled, not drained to completion** — a multi-minute LLM turn cannot survive a
rolling update within `terminationGracePeriodSeconds: 60`. The pod is disposable; the
session is not — it is **`Recover`-able on the successor** from the Redis
snapshot + durable event log. The bounded `GracefulStop` (30s) hard-stops (`grpcSrv.Stop()`)
on timeout, and `Service.Close` releases every held `coordination.k8s.io` Lease
(cancel-detached) so a survivor can take over immediately, without the 30s TTL. See
`docs/adr/0048-mecak8s.md` for the design rationale and the deliberately-deferred items
(CRD/operator, HPA, managed Redis, Redis auth, fixing `mecated`'s unbounded `GracefulStop`).

---

See also: [running the server (`mecated`)](mecated.md), or the
[operator guide index](../usage.md).

---
