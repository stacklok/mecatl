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

## Caller identity (OIDC) — the opt-in overlay

`deploy/mecak8s-oidc/` is a kustomize overlay over `deploy/mecak8s/` that turns
on **caller identity and ownership isolation**: a real IdP authenticates each
caller, and every new session and schedule records the verified `(issuer,
subject)` that owns it. With the verifier enabled, callers can access only their
own records; historical ownerless records are deliberately unavailable rather
than adopted.

```sh
# Use a registry your target cluster can pull from; ko.local is not sufficient.
export KO_DOCKER_REPO=registry.example/mecatl
task deploy:apply ROOT=deploy/mecak8s-oidc     # edit the three flag values first
```

It appends four flags to the agent — `--oidc-issuer`, `--oidc-audience`
(required whenever the issuer is set), the optional `--oidc-jwks-uri` (pin the
signing-key endpoint and skip discovery, for an air-gapped or pinned-key
deployment), and `--oidc-max-jwks-staleness=1h`. The base deploys with identity
**off**, byte-identically to a mecatl without it, so nothing changes for existing
users of these manifests.

**This is an isolation cutover, not an ownerless-data migration.** Before
applying the overlay, inventory and back up ownerless sessions and schedules
from the configured stores: they remain available only to a deployment without
the verifier. The scheduler intentionally skips ownerless schedules after the
cutover, so it neither adopts nor retries historical work. Disabling the
verifier restores only the existing ownerless compatibility behavior; it does
not assign historical records to a caller.

**Raw drivers are trusted infrastructure.** The overlay includes
`raw-driver-networkpolicy.yaml`, which permits ingress to pods labelled
`app.kubernetes.io/component: raw-driver` only from the mecak8s agent pod.
Tenant workloads must not use that label and must reach the authenticated public
service instead. Until remote drivers receive caller claims (ADR 0103), deploy a
raw driver with that label and its listener on TCP 9090 in the same namespace;
do not expose it through a Service, Ingress, or tenant NetworkPolicy.

**Point it at a real external IdP over HTTPS.** That is the only shape that works
with the token validator's security defaults intact: it refuses an `http://`
issuer, and refuses a `jwks_uri` that resolves to a private, loopback or
link-local address — which is what stops a `jwks_uri` aimed at cloud instance
metadata (`169.254.169.254`). An **in-cluster** IdP needs a flag that relaxes
both checks; that flag exists for the end-to-end test fixtures only and is
deliberately absent from these manifests, because a published example must not
ship SSRF relaxation. No `NetworkPolicy` patch is needed either: the base egress
already allows DNS plus TCP 443 to any destination IP, exactly what an external
IdP requires.

### Validator and signing-key availability

The shipped validator is `toolhive-core/authn` **v0.0.39**. A bad OIDC
configuration — including an unreachable initial key fetch — is fatal at startup;
the deployment never silently becomes unauthenticated.

After a successful fetch, a short IdP outage can use the last good JWKS. The
explicit overlay value `--oidc-max-jwks-staleness=1h` bounds that availability
fallback: after one hour the validator tries to refresh and returns **503 Service
Unavailable** when it cannot obtain current keys. That is distinct from a bad,
expired, wrong-issuer, or wrong-audience token, which is rejected as **401**.
The hour is the maximum additional exposure for a signing key revoked at the IdP
during an outage; it is **not** per-token revocation. A token that remains valid
under a still-trusted signing key is accepted until its normal expiry. Set the
flag to `0` only to deliberately restore unbounded cached-key availability and
its corresponding signing-key revocation exposure.

The JWKS cache is process-local and is not persisted. Restarting fetches current
keys again; if the IdP is still unavailable, an identity-enabled process cannot
start.

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
