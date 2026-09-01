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
managed service the pod talks to over the network (Redis + the k8s API server). Global
MCP profiles use the same operator settings loader as mecated/mecatequi. The intended
OAuth posture is a read-only environment credential injected from a Kubernetes Secret;
rotation requires an external provisioner and pod restart. `mecak8s` never launches a
browser and cannot run `mecated mcp login`. A mutable local credential root is accepted
only when explicitly mounted/configured, but contradicts the normal storage-free posture
and is not recommended. It drops
`mecated`'s `skills promote` / `config` / `perf-mcp` subcommands, ACP, and the
Prometheus/OTel admin surface. It inverts `mecated`'s interactive defaults: `--headless`
defaults **on** and `--posture` defaults to **`auto`** (an unattended daemon). Like the
other shipped executables, exact top-level `mecak8s --version` prints its build identity
and exits before loading normal configuration or starting listeners.

`mecak8s` is always a server-assigned workspace deployment, but its normal
storage-free pod has no mounted workspace. An omitted or empty wire `profile`
therefore creates a `no-fs` session; clients must not send a workspace path, and
any profile other than `no-fs` is rejected. The empty workspace/profile contract
is intentional: it does not request a cwd from the client or an arbitrary path
inside the pod. A mounted-workspace mode requires a future explicit operator
design.

```console
$ go run ./cmd/mecak8s --redis-url redis:6379 --redis-allow-plaintext --session-lease-k8s-namespace mecatl --openai
```

#### Flags

`mecak8s` reuses `mecated`'s provider/model/permission/skills flags via the shared CLI config wiring
(the four real-provider mains share that wiring). The k8s-native surface:

| Flag | Default | Meaning |
| --- | --- | --- |
| `--redis-url` | `""` | Redis address (`host:port`) for the session store + durable event log (ADRs 0048 and [0233](../adr/0233-secure-external-redis.md), storage-free). The `redisstore` `Store` doubles as its own `EventLog` (like `jsonlstore`). **Mutually exclusive with `--store-dir` / `--session-store-url`** (rejected at `Build`). Takes a bare `host:port`: a `redis://` or `rediss://` URL is rejected. An address alone is not a plaintext opt-in — see `--redis-allow-plaintext`. |
| `--redis-allow-plaintext` | `false` | Explicitly allow unauthenticated plaintext Redis; disposable local/Kind use only. Without it an address-only `--redis-url` is **rejected at `Build`**. Never set it for a production external Redis. |
| `--redis-username-file` / `--redis-password-file` | `""` | Paths to optional Redis ACL credentials in a mounted Kubernetes Secret. A password without a username authenticates as Redis's default ACL user; a username requires a password. Credential values are never accepted as command arguments. Any credential requires verified TLS — `--redis-tls` or `--redis-tls-ca`. When either file is configured, mecak8s watches its lexical parent and transactionally hot-reloads the complete configured Redis file set after a bounded successful probe. |
| `--redis-tls` | `false` | Verify Redis TLS against the host system trust store. Use for a managed Redis whose certificate chains to a public CA (Azure Cache for Redis, ElastiCache in-transit encryption). |
| `--redis-tls-ca` | `""` | Path to a PEM CA bundle from a mounted Secret used to verify Redis TLS, **replacing** the system trust store. Use for a private CA; takes precedence over `--redis-tls`. Either flag is mandatory whenever ACL credentials are configured. The file is included in the same automatic transactional reload as Redis credential files. Client-certificate (mTLS) authentication is not supported — see [ADR 0233](../adr/0233-secure-external-redis.md). |
| `--session-lease-k8s-namespace` | `mecatl` | Kubernetes namespace for `coordination.k8s.io` Lease-backed session leasing (the in-cluster multi-replica single-writer path). Uses in-cluster config (or the default kubeconfig out-of-cluster). The ServiceAccount needs `get,create,update,delete` on `leases` in `coordination.k8s.io` for this namespace. Empty = no leasing. |
| `--session-lease-ttl` | `30s` | session-lease lifetime; a crashed/killed holder's lease becomes claimable after this long. |
| `--session-lease-renew-interval` | `0` | how often the per-session renewer refreshes a held lease; `0` = `--session-lease-ttl` / 3. |
| `--headless` | `true` | run NON-interactive (inverted from `mecated`): a child ask is auto-denied / routed to the opt-in `--subagent-ask-reviewer` rather than parked. Pass `--headless=false` only if a client answers asks. |
| `--posture` | `auto` | operator posture ladder (strict < trusted < auto < yolo); `auto` is the recommended unattended single-tenant tier. On this **headless** root the posture ladder never raises `TrustProject`. Explicit `--trust-project`, `trustedWorkspaces:`, or undrifted remembered trust admits BOTH repo steering and the read-only child shell (the operator vouches for `.git`); without any trust source, the fail-safe default gives allow-all approvals with neither ([ADR 0095](../adr/0095-root-aware-project-trust.md)). |
| `--reasoning-effort` | `auto` | operator reasoning-effort tier ([ADR 0055](../adr/0055-reasoning-effort.md)): `auto` (unset, provider default) or `low`/`medium`/`high`/`xhigh`/`max` (OpenAI clamps `xhigh`/`max` → `high`). Empty honours the operator-global `reasoning-effort:` setting; a per-session `CreateSession.reasoning_effort` out-ranks it. Operator-tier only. CLI out-ranks the YAML key. |
| `--no-prompt-cache` | `false` | disable provider-side prompt caching ([ADR 0100](../adr/0100-provider-prompt-caching.md)), which is ON by default: every adapter's cache dialect degrades to `None`, reproducing the pre-caching wire exactly. |
| `--anthropic-cache-ttl` | `""` (API default, `5m`) | TTL stamped on every Anthropic ephemeral `cache_control` breakpoint: `5m` or `1h`. Empty omits the `ttl` field; any other value is ignored with a `WARN`. |
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

#### Production Helm chart (`deploy/helm/mecak8s/`)

A real-provider install (`mockProvider: false`) has three explicit postures.
**In-pod TLS** is `tls.enabled=true` plus OIDC.
**Edge-terminated TLS** is `security.tlsTerminatedUpstream=true` plus OIDC, with `tls.enabled=false` selecting a ClusterIP-only plaintext h2c backend.
The **unsafe bypass** is `security.allowUnsafeRealProvider=true`, for local or trusted-mesh deployments.
Setting both in-pod TLS and `tlsTerminatedUpstream=true` is valid and keeps the upstream attestation.
The unsafe bypass stamps the pod `mecatl.stacklok.com/unsafe-real-provider: "true"`; a secure upstream attestation stamps `mecatl.stacklok.com/tls-terminated-upstream: "true"`.
Neither stamp can be forged or cleared through `podAnnotations`.

Edge mode's cost is concrete: on an h2c backend the caller's `Authorization: Bearer` token crosses the pod network in cleartext.
Any workload that can reach the Service ClusterIP can read that token and replay it as the caller, and the chart ships no NetworkPolicy, so by default every pod in the cluster can reach it.
Admitting only the gateway's pods — by NetworkPolicy or an mTLS mesh — is therefore the load-bearing control in this posture, not optional hardening.
`tlsTerminatedUpstream` is an attestation the chart cannot verify: it checks neither gateway TLS, nor gateway-only reachability, nor token forwarding.
The gateway must forward the original bearer token rather than authenticate with a forwarded-identity header, and must expose a `GRPCRoute` only — never public-route the HTTP drain or health endpoints.
The chart deliberately creates no Gateway, Route, Certificate, or general NetworkPolicy.
Use an operator-owned `BackendTLSPolicy` or in-pod TLS where gateway-to-pod re-encryption is required.
Move an existing pod-TLS release to h2c with a blue-green or maintenance cutover, not an assumed-safe rolling update.
See [ADR 0278](../adr/0278-mecak8s-edge-terminated-tls.md).

Leave `redis.caKey` empty to select system-trust TLS.
This option mounts no Secret unless an ACL key is set.
ACL password and username Secret keys are optional.
A username key requires a password key.
The image defaults to `v<chart-version>`.
This default keeps ranged Helm upgrades aligned with released images.
Set a signed release tag or digest only to override the default.
The chart preserves the restricted, storage-free workload.
It also preserves bounded resources, rolling updates, probes, the PDB, and namespaced Lease RBAC.
The chart ships no general NetworkPolicy.
The cluster must provide network isolation because agent egress depends on operator-selected endpoints.
The `oidc.*` values add a narrow raw-driver NetworkPolicy when caller identity is enabled.

```sh
helm upgrade --install mecak8s deploy/helm/mecak8s --namespace mecatl --create-namespace \
  --set image.repository=registry.example/mecak8s \
  --set redis.endpoint=redis.example.internal:6379 \
  --set redis.credentialsSecret=mecak8s-redis \
  --set tls.enabled=true \
  --set tls.secretName=mecak8s-tls \
  --set oidc.enabled=true \
  --set oidc.issuer=https://idp.example.com \
  --set oidc.audience=mecatl
```

`defaultProvider` and `model` are empty by default and render only when set; provider IDs
must be built-in CLI IDs and model IDs are opaque but non-blank. `maxRunTokens` and
`maxTeamTokens` are nullable: `null` passes no flag (the runtime remains unlimited), while
an explicit value must be a positive integer. Empty `topologySpreadConstraints`,
`affinity`, `nodeSelector`, and `tolerations` render no scheduling fields. Supply normal
Kubernetes pod-spec shapes when setting them; a `kubernetes.io/hostname` spread constraint
keeps replicas apart where enough nodes are eligible.

For certificate/credential rotation, overlap old and new CAs in bundles until all leaves
and pods have moved, then remove the old CA. Projected server cert/key and file-backed
Redis CA/ACL changes are transactional and last-valid: malformed intermediate generations
stay rejected while the prior generation remains active. The server client-CA pool is
static and requires a rolling restart when it changes.

The Secret is mounted read-only at `/var/run/secrets/redis` with `defaultMode: 0440`; the chart projects exactly the configured CA and ACL keys, not the whole Secret. Their values are never chart values or command arguments. The external chart passes the CA path when `redis.caKey` is set and `--redis-tls` otherwise, and conditionally passes configured password and username paths. Both TLS modes verify the Redis certificate against the hostname from `redis.endpoint` (including IP SAN rules); hostname verification is never disabled. TLS with no ACL is valid, and a system-trust install with no ACL renders no Secret volume at all. `values-kind.yaml` is a separate disposable-only profile for the local `ko.local` image and plaintext Redis fixture, and its rendered command includes the explicit `--redis-allow-plaintext` opt-in. It must not be used for a production install.

#### Mounting XDG configuration

Use `extraEnv`, `extraVolumes`, and `extraVolumeMounts` to project trusted,
immutable agent configuration without adding pod-local state. Set `XDG_CONFIG_HOME`
to the mount root and place content below `<root>/mecatl/skills`,
`<root>/mecatl/agents`, and `<root>/mecatl/rules`. Set
`skills.autoDiscover: true` (default `false`) to discover skills from the
standard XDG locations (`$XDG_CONFIG_HOME/mecatl/skills` or
`~/.config/mecatl/skills`, plus `~/.claude/skills`) and, when the workspace is
trusted, `<workspace>/.mecatl/skills` and `<workspace>/.claude/skills`.
Create the ConfigMap in the release namespace before referencing it:

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: mecatl-config-v1
  namespace: mecatl
immutable: true
data:
  review-skill: |
    ---
    name: review
    ---
    Review changes for correctness and security.
  reviewer-agent: |
    ---
    name: reviewer
    ---
    Review the supplied change and report actionable findings.
  base-rules: |
    Keep responses concise and explain material risks.
```

Then supply the matching Helm values:

```yaml
extraEnv:
  - name: XDG_CONFIG_HOME
    value: /etc/mecatl-config
skills:
  autoDiscover: true
extraArgs:
  - --no-user-model
extraVolumeMounts:
  - name: mecatl-config
    mountPath: /etc/mecatl-config
    readOnly: true
extraVolumes:
  - name: mecatl-config
    configMap:
      name: mecatl-config-v1
      items:
        - {key: review-skill, path: mecatl/skills/review/SKILL.md}
        - {key: reviewer-agent, path: mecatl/agents/reviewer.md}
        - {key: base-rules, path: mecatl/rules/base.md}
```

Keep the ConfigMap and mount read-only. Immutable ConfigMaps cannot be changed:
create a new versioned ConfigMap, update both its content and the Helm
`configMap.name` reference, then run `helm upgrade`. Mutable ConfigMap updates
also require a Deployment rollout because discovery is snapshotted at startup.
A read-only XDG root cannot host the writable user-model store, so pass
`--no-user-model` through `extraArgs`. `XDG_CONFIG_HOME` changes more than these
three discovery paths: it also relocates operator settings, soul, and auth-file
lookup under `<root>/mecatl`; include those files deliberately or leave them
absent.

A ToolHive-packaged skill can instead be mounted directly from an OCI artifact
on Kubernetes 1.36 or newer. The artifact must contain `SKILL.md` at its root;
mount each artifact at
`<XDG_CONFIG_HOME>/mecatl/skills/<skill-name>` and enable
`skills.autoDiscover: true`:

```yaml
extraEnv:
  - name: XDG_CONFIG_HOME
    value: /etc/mecatl-config
skills:
  autoDiscover: true
extraArgs:
  - --no-user-model
extraVolumeMounts:
  - name: review-skill
    mountPath: /etc/mecatl-config/mecatl/skills/review
    readOnly: true
extraVolumes:
  - name: review-skill
    image:
      reference: registry.example/skills/review@sha256:<digest>
      pullPolicy: IfNotPresent
```

Kubernetes image volumes are inherently read-only, and the pod's
`imagePullSecrets` apply to these artifact pulls normally. Use a digest-pinned
reference in production; it remains immutable and `IfNotPresent` may safely use
the node cache. A mutable tag with `IfNotPresent` may also reuse cached content;
use `Always` if every pod start must resolve that tag from the registry. Mecatl
still snapshots discovered skill metadata and body at process startup, so any
artifact change requires pod recreation.

##### Prove the skill in a coding run

`GET /v1/skills` is an inventory check. It proves discovery, but it does not
prove that the coding agent loaded or followed the skill. A useful end-to-end
check must drive a real model through the skill and verify its tool calls.

For example, create `oci-skill-demo/SKILL.md` with a body that requires the
agent to create `oci-skill-proof.txt` containing `OCI_SKILL_MOUNT_OK`, then
verify it with `Read`:

```markdown
---
name: oci-skill-demo
description: Creates and verifies a proof file from an OCI-mounted skill.
---

When invoked, use `Write` to create `oci-skill-proof.txt` containing exactly
`OCI_SKILL_MOUNT_OK` and a trailing newline. Use `Read` to verify the file, then
report the verified path and marker.
```

Build and publish that directory with ToolHive, resolve the published digest,
and use the image-volume values above:

```sh
thv skill validate ./oci-skill-demo
thv skill build ./oci-skill-demo --tag registry.example/skills/oci-skill-demo:v1
thv skill push registry.example/skills/oci-skill-demo:v1
```

After starting mecak8s with a real provider, create a session and invoke the
mounted skill through the HTTP API:

```sh
session_id=$(curl -fsS -X POST http://127.0.0.1:8081/v1/sessions \
  -H 'Content-Type: application/json' -d '{}' | jq -r .session_id)

curl -fsS -N -X POST \
  "http://127.0.0.1:8081/v1/sessions/${session_id}/prompt" \
  -H 'Content-Type: application/json' \
  -d '{"text":"/oci-skill-demo Execute the mounted skill exactly and verify the resulting file."}'
```

The proof is the run, not the inventory response. Its SSE stream must show this
sequence:

1. `tool.call` for `Skill` with `name: "oci-skill-demo"`.
2. A `tool.result` containing the instructions read from the OCI artifact.
3. `tool.call` for `Write`, creating `oci-skill-proof.txt` with the unique marker.
4. `tool.call` for `Read`, followed by a result containing `OCI_SKILL_MOUNT_OK`.
5. A clean terminal result reporting the verified path and marker.

Use Helm 3.16 or newer when adding an image volume to an existing release.
Older Helm clients may render the manifest but fail to calculate the upgrade
patch because their embedded Kubernetes API does not know the `image` volume
field.

#### Server TLS (`tls.*` chart values)

Server TLS is separate from `redis.*` TLS and is disabled by default. Create the
certificate Secret in the release namespace, then enable the chart values:

```sh
kubectl create secret tls mecak8s-tls --namespace mecatl \
  --cert=server.crt --key=server.key

helm upgrade --install mecak8s deploy/helm/mecak8s --namespace mecatl \
  --set image.repository=registry.example/mecak8s \
  --set redis.endpoint=redis.example.internal:6379 \
  --set redis.credentialsSecret=mecak8s-redis \
  --set tls.enabled=true \
  --set tls.secretName=mecak8s-tls
```

The chart passes `/var/run/secrets/tls/tls.crt` and
`/var/run/secrets/tls/tls.key` to `--tls-cert` and `--tls-key`, enabling TLS on
both the gRPC and HTTP/SSE listeners. It projects only `tls.certKey` and
`tls.keyKey` (defaulting to `tls.crt` and `tls.key`) from the pre-created Secret
as a read-only `0440` volume; custom data-key names are supported. The chart
creates no Secret. When enabled, the health, readiness, and drain requests use
HTTPS. Projected certificate/key rotations are loaded transactionally and become visible to
new gRPC and HTTP handshakes without a rollout; malformed or expired candidates retain the
last valid certificate, and existing connections continue unchanged. A fixed internal
observer warns once for each certificate generation that becomes expiring or expired. The
client-CA bundle remains static and requires a rollout when it changes.

#### Caller identity (`oidc.*` chart values)

The chart's `oidc.*` values wire the same four flags the legacy kustomize overlay
used to append: `oidc.enabled` (default `false`), `oidc.issuer`, `oidc.audience`
(required together with `oidc.enabled`), the optional `oidc.jwksURI`, and
`oidc.maxJWKSStaleness` (default `1h`). See [`deploy/README.md`](https://github.com/stacklok/mecatl/blob/main/deploy/README.md#caller-identity-oidc--the-opt-in-chart-values)
for the full walkthrough, and [multi-user caller identity](https://github.com/stacklok/mecatl/blob/main/user-docs/building/deployment/mecak8s.md#multi-user-caller-identity-and-ownership-isolation-opt-in)
for the isolation semantics. When `oidc.enabled` is true the chart also renders
a `raw-driver` NetworkPolicy scoping ingress on a `app.kubernetes.io/component:
raw-driver`-labelled pod to the mecak8s agent pod only — trusted-infrastructure
raw gRPC drivers are not yet caller-enforced (ADR 0213).

#### Local optional Keycloak fixture

The disposable `deploy/mecak8s-kind/` fixture keeps its base chart deployment
unauthenticated and uses its fixture-only NodePort overlay. Its optional Keycloak
layer is an explicit local validation aid:
`task mecak8s:kind-keycloak-setup` installs it; the Kind cluster's static
`extraPortMappings` expose the issuer at loopback `127.0.0.1:8443` and mecak8s at
`127.0.0.1:18080`/`18081`. The mappings are installed only when the cluster is
created; the fixture NodePort overlay is not part of shared `values-kind.yaml` or
bare chart defaults. Only the host binding is loopback-only -- the NodePorts are
also reachable on the Kind node's own address from the Docker network, which is
accepted for a disposable fixture and is not a production isolation claim. Map `keycloak.mecatl.svc.cluster.local` to `127.0.0.1` locally
before the browser flow so the configured issuer hostname and certificate remain
intact.
The mecak8s certificate covers `localhost` and `127.0.0.1`; verify the fixture
CA and hostname when connecting to `https://localhost:18081`, rather than
weakening TLS verification.

The normal fixture login is Authorization Code + PKCE for the public
`mecatui-kind` client, requesting the `mecak8s:access` scope. A password grant
is only a non-browser test helper, never the normal client journey. The server
accepts only a Keycloak access token with `aud: mecak8s`; absent, forged,
wrong-issuer, or wrong-audience tokens fail before authenticated API handling.
The fixture's in-cluster issuer is deliberately private and CA-scoped. It does
not change the production chart's external-IdP security defaults.

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
4. **OIDC caller identity against a real in-cluster Dex** — the agent discovers Dex's
   signing keys, rejects no-token and forged-token requests on both HTTP and gRPC,
   records Alice as a newly created session owner, and records Bob as the durable
   actor when he prompts Alice's session. A separate journey proves bounded JWKS
   staleness returns 503 during an IdP outage and recovers when Dex is reachable.

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
