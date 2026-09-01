# Deploying mecated

Kubernetes manifests for the mecatl server, `mecated`. The container image is
built with [`ko`](https://ko.build) directly from `./cmd/mecated` — there is no
Dockerfile. Manifests reference the image via the `ko://…` placeholder, which
`ko resolve` / `ko apply` substitutes with the real ref at deploy time.

## ⚠️ Security: the mecated API is auth-optional

mecated exposes command and file execution over gRPC + HTTP. Authentication is
**opt-in**: it is **off by default**, so unless you enable it the API performs
**no caller identity check** (see `cmd/mecated/main.go`). The in-code default binds
loopback for exactly this reason. The Deployment overrides that to `0.0.0.0`
because a pod's network namespace is isolated — but that only shifts the trust
boundary to Kubernetes networking.

- **Recommended in-pod control: enable `--auth-token`.** mecated supports
  `--auth-token` (bearer-token auth) as well as TLS and mTLS. Turning on
  `--auth-token` gives you a caller-identity check that travels with the pod,
  independent of network topology. The HTTP `/healthz` and `/readyz` endpoints
  are mounted **outside** the auth boundary so probes keep working; the gRPC
  health service shares the API's auth interceptors and needs credentials when
  auth is enabled.
- A default-deny ingress **`NetworkPolicy`** ships in `networkpolicy.yaml`
  (wired into `kustomization.yaml`). It selects the mecated pod and allows no
  ingress until you add an explicit allow for your clients — defense-in-depth on
  top of (or in lieu of) `--auth-token`. See that file for a ready-to-adapt
  client-allow block. Egress is intentionally left open because mecated needs
  cluster DNS and outbound HTTPS to OpenAI.
- The `Service` is `ClusterIP` only. Do **not** expose it via
  LoadBalancer/NodePort/Ingress without an auth boundary in front.
- For exposure beyond a trusted network, combine `--auth-token`/mTLS with the
  NetworkPolicy, or front mecated with an external auth proxy / mTLS gateway.

## Prerequisites

- `ko` installed — https://ko.build/install/
- A container registry, set via `KO_DOCKER_REPO`
  (e.g. `export KO_DOCKER_REPO=ghcr.io/stacklok/mecatl`).
- `kubectl` with access to the target cluster.
- The `mecated-openai` Secret containing `OPENAI_API_KEY` (see below).

## The OpenAI API key Secret

The Deployment passes `--openai` and reads `OPENAI_API_KEY` from a Secret named
`mecated-openai`. Create it out-of-band — never commit a real key:

```sh
kubectl create secret generic mecated-openai \
  --from-literal=OPENAI_API_KEY="sk-...your-key..."
```

`secret.example.yaml` is an illustrative placeholder (plus a commented
`ExternalSecret` template for the External Secrets Operator). It is **not** in
`kustomization.yaml`, so a `kubectl apply -k deploy/` will not push a dummy key.

## Build a local image

```sh
task ko:build          # KO_DOCKER_REPO=ko.local, --local, no push
```

This loads the image into your local Docker/podman daemon and prints the
fully-qualified ref (content-hash tag).

## Build, push, and deploy to a cluster

```sh
export KO_DOCKER_REPO=ghcr.io/stacklok/mecatl   # your registry

task ko:publish                          # build + push the image
task ko:resolve | kubectl apply -f -      # render ko://… → real ref, then apply
```

`task ko:resolve` runs `ko resolve -f deploy/`, which builds+pushes the image and
emits the manifests with the `ko://…` placeholder replaced. Pipe straight into
`kubectl apply -f -`.

> Note: `ko resolve -f deploy/` processes the YAML files; `kustomization.yaml`
> is for `kubectl apply -k deploy/` (which does not do ko substitution). Use the
> `ko resolve | kubectl apply -f -` flow for the ko-built image.

## Probes: httpGet against /healthz and /readyz

mecated serves HTTP health endpoints on the `http` port (8081), mounted **outside**
the auth boundary so they work with or without `--auth-token`:

- **`/readyz`** — readiness probe; gates Service traffic until the app can serve.
- **`/healthz`** — liveness probe; detects a hung process so it gets restarted.

Both are configured as `httpGet` probes in `deployment.yaml`. (gRPC clients can
also use the standard `grpc_health_v1` health service on the `grpc` port.)

## Pod Security Standards: restricted

Both the pod- and container-level `securityContext` satisfy the PSS
**restricted** profile: `runAsNonRoot` (UID/GID 65532, matching the chainguard
static base), `allowPrivilegeEscalation: false`, `readOnlyRootFilesystem: true`,
`seccompProfile: RuntimeDefault`, and `capabilities.drop: [ALL]`.

## Caller identity (OIDC) — the opt-in chart values

`deploy/helm/mecak8s/` has three explicit real-provider postures: in-pod TLS plus
OIDC; edge-terminated TLS (`security.tlsTerminatedUpstream=true`, `tls.enabled=false`,
OIDC, and a `ClusterIP` Service); and the conspicuous unsafe bypass. The upstream value
is an attestation, not chart enforcement. Edge mode exposes an h2c backend only; the
operator must own gateway TLS, preserve the original `Authorization: Bearer` token (not
substitute forwarded-identity authentication), restrict plaintext backend access to the
gateway or mesh, and expose a `GRPCRoute` only—not `/drain`, `/healthz`, or `/readyz`.
The chart intentionally creates no Gateway, Route, Certificate, or general
NetworkPolicy; use an operator-owned `BackendTLSPolicy` or in-pod TLS when gateway-to-pod
re-encryption is required. Setting both in-pod TLS and the upstream attestation is valid.
Move an existing pod-TLS release to h2c with a blue-green or maintenance cutover, not an
assumed-safe rolling update. See [ADR 0278](../docs/adr/0278-mecak8s-edge-terminated-tls.md).

The `oidc.*` values turn on **caller identity and
ownership isolation** for the mecak8s agent: a real IdP authenticates each
caller, and every new session and schedule records the verified `(issuer,
subject)` that owns it. With the verifier enabled, callers can access only
their own records; historical ownerless records are deliberately unavailable
rather than adopted.

The chart uses `v<chart-version>` when both image selectors are empty. This keeps
ranged Helm upgrades aligned with released images. Set `image.tag` or
`image.digest` only to override that default.

```sh
helm upgrade --install mecak8s deploy/helm/mecak8s --namespace mecatl --create-namespace \
  --set image.repository=registry.example/mecak8s \
  --set redis.endpoint=redis.example.internal:6379 \
  --set redis.credentialsSecret=mecak8s-redis \
  --set tls.enabled=true \
  --set tls.secretName=mecak8s-tls \
  --set oidc.enabled=true \
  --set oidc.issuer=https://idp.example.com/realms/mecatl \
  --set oidc.audience=mecatl
```

`defaultProvider` and `model` are optional and become `--default-provider` and
`--model` only when non-empty. `maxRunTokens` and `maxTeamTokens` default to `null`
(unset/unlimited at the runtime); if supplied, each must be a positive integer. Optional
`topologySpreadConstraints`, `affinity`, `nodeSelector`, and `tolerations` values are
passed through under the pod spec and omitted when empty. For two replicas, a hostname
spread constraint is recommended where the cluster has multiple eligible nodes.

During Secret rotation, keep old and new issuing CAs together in the Redis/OIDC bundles
for an overlap window, rotate leaf credentials, then remove the old CA. mecak8s reloads
server certificate/key and file-backed Redis CA/ACL material transactionally and retains
the last valid generation when an intermediate projection or probe fails. The server
`client-ca` trust pool is static: changing it requires a rolling pod restart.

Enabling `oidc.enabled` appends four flags to the agent — `--oidc-issuer`,
`--oidc-audience` (required whenever the issuer is set), the optional
`--oidc-jwks-uri` (pin the signing-key endpoint and skip discovery, for an
air-gapped or pinned-key deployment; set via `oidc.jwksURI`), and
`--oidc-max-jwks-staleness` (`oidc.maxJWKSStaleness`, default `1h`). The
`oidc.enabled` itself defaults off for the mock fixture and explicit unsafe-bypass
profiles; a real-provider chart render cannot leave it off under the secure default.

**This is an isolation cutover, not an ownerless-data migration.** Before
enabling it, inventory and back up ownerless sessions and schedules from the
configured stores: they remain available only to a deployment without the
verifier. The scheduler intentionally skips ownerless schedules after the
cutover, so it neither adopts nor retries historical work. Disabling the
verifier restores only the existing ownerless compatibility behavior; it does
not assign historical records to a caller.

**Raw drivers are trusted infrastructure.** When `oidc.enabled` is true, the
chart also renders a `raw-driver` NetworkPolicy that permits ingress to pods
labelled `app.kubernetes.io/component: raw-driver` only from the mecak8s agent
pod. Tenant workloads must not use that label and must reach the
authenticated public service instead. Until remote drivers receive caller
claims (ADR 0213), deploy a raw driver with that label and its listener on TCP
9090 in the same namespace; do not expose it through a Service, Ingress, or
tenant NetworkPolicy.

**Point it at a real external IdP over HTTPS.** That is the only shape that
works with the token validator's security defaults intact: it refuses an
`http://` issuer, and refuses a `jwks_uri` that resolves to a private,
loopback or link-local address — which is what stops a `jwks_uri` aimed at
cloud instance metadata (`169.254.169.254`). An **in-cluster** IdP needs a flag
that relaxes both checks; that flag exists for the end-to-end test fixtures
only (applied there via a runtime `kubectl patch`, never through this chart)
and is deliberately absent from `deploy/helm/mecak8s/`, because a published
chart must not ship SSRF relaxation.

### Validator and signing-key availability

The shipped validator is `toolhive-core/authn` **v0.0.39**. A bad OIDC
configuration — including an unreachable initial key fetch — is fatal at startup;
the deployment never silently becomes unauthenticated.

After a successful fetch, a short IdP outage can use the last good JWKS. The
default `oidc.maxJWKSStaleness=1h` bounds that availability
fallback: after one hour the validator tries to refresh and returns **503 Service
Unavailable** when it cannot obtain current keys. That is distinct from a bad,
expired, wrong-issuer, or wrong-audience token, which is rejected as **401**.
The hour is the maximum additional exposure for a signing key revoked at the IdP
during an outage; it is **not** per-token revocation. A token that remains valid
under a still-trusted signing key is accepted until its normal expiry. Set the
value to `0` only to deliberately restore unbounded cached-key availability and
its corresponding signing-key revocation exposure.

The JWKS cache is process-local and is not persisted. Restarting fetches current
keys again; if the IdP is still unavailable, an identity-enabled process cannot
start.

### Disposable Keycloak validation fixture

`deploy/mecak8s-kind/` layers a private Keycloak issuer over its otherwise
unauthenticated Kind baseline for local validation only. Run
`task mecak8s:kind-keycloak-setup`, then use its explicit loopback-only
port-forward; mecak8s stays a `ClusterIP` Service with no Ingress, NodePort,
LoadBalancer, or wildcard host binding. Its fixture certificate covers
`localhost` and `127.0.0.1`, and clients must verify both the hostname and the
fixture CA.

The normal client login is Authorization Code + PKCE using Keycloak's public
client and an access token with the `mecak8s` audience. A password grant is a
narrow test helper, not a normal client flow. The fixture demonstrates the
same fail-closed boundary: initial JWKS unavailability prevents startup; once
keys have been fetched, an outage beyond `oidc.maxJWKSStaleness` returns 503,
not an unauthenticated fallback or a 401 token result. It is not a production
IdP configuration and does not relax the chart's external-IdP protections.

### Trying it locally by hand

The same flags work on a plain `mecated` and are the quickest way to see
attribution end to end:

```sh
bin/mecated --grpc-addr=127.0.0.1:8080 \
  --oidc-issuer=https://idp.example.com/realms/mecatl \
  --oidc-audience=mecatl \
  --oidc-jwks-uri=https://idp.example.com/realms/mecatl/protocol/openid-connect/certs \
  --oidc-max-jwks-staleness=1h
```

Then create a session with a bearer token from that issuer and list sessions: the
row names the caller.
