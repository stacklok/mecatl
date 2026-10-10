# Deployment and hardening

> Part of the [mecatl architecture guide](../architecture.md).

This chapter covers the server process: edge authentication, how the verified caller
becomes the owner of what it creates, where credentials are kept, load and shutdown
behavior, and how the three server deployment shapes differ. Permissions, posture, shell
checks, and workspace trust are in [governance](governance.md); where a session's files
and commands live is in [microVM environments](microvm-environments.md). The server is
`internal/adapter/server`, wired by the roots in `cmd/`; the engine never sees a token,
a listener, or a certificate.

## Edge authentication

Three optional mechanisms are enforced by the gRPC interceptors and HTTP middleware in
`internal/adapter/server/authn.go`:

- **Static bearer token.** One shared secret, compared in constant time. It yields no
  caller identity: one credential, zero subjects.
- **TLS and mutual TLS.** A client CA bundle makes the server require and verify client
  certificates. mTLS authenticates the transport, not a caller.
- **OIDC.** A `server.PrincipalValidator` turns a JWT into a `session.Principal`
  identified by its `(Issuer, Subject)` pair. The shipped validator lives in the
  separate `authn/oidc` module, keeping JWT and IdP dependencies out of the engine.

Static bearer and OIDC are mutually exclusive (`cliconfig.ValidateOIDCAuthToken`), and
every OIDC misconfiguration is fatal at startup (`cliconfig.OIDCValidator`): degrading
to the unauthenticated path would silently open an authenticated deployment. Signing keys
are cached through a brief IdP outage up to a bounded staleness; past it the edge answers
`Unavailable` (503) rather than `Unauthenticated`, so clients retry instead of logging in
again. `mecated` binds loopback by default and warns loudly when it binds a non-loopback
address without authentication, because the API exposes command and file execution.

## Caller identity

Caller identity enters at the edge and nowhere else. The edge re-checks its validator's
verdict (`admissiblePrincipal`): it rejects a principal missing half of its identity
pair, carrying the system grant type, or claiming the internal issuer `mecatl:internal`,
which only `internal/syscaller` mints.

The principal rides the request context via `session.WithPrincipal` and
`session.PrincipalFromContext`. Absence is a nil principal; nothing mints an anonymous
placeholder (`TestInvariant_no_fabricated_principal`). A fabricated caller would look
real to every consumer and would merge all unauthenticated requests into one owner.
Internal work instead runs under an explicit system principal from the
`internal/syscaller.Roots` registry (child cleanup, memory consolidation, the scheduler,
JWKS refresh, and others). Embedders verify credentials themselves and attach the result
with `session.WithPrincipal` before `Engine.Run`.

## Caller ownership

`CreateSession` stamps the owner from the context principal, never from the request
body; children, forks, and resumed sessions inherit it, and a schedule captures it at
creation. With an OIDC validator wired, composition sets `OwnershipEnforced`, and a
caller reaches only its own sessions, schedules, teams, memory, event streams, and live
runs; without one, ownership is inert. Ownerless records become unreachable, not adopted.

**One decision function.** `Service.ownsResource` (`ownership.go`) is the only
comparison: the record's owner must be non-nil and match the context principal. Its
wrappers return the same not-found error as a missing ID, so a refusal never reveals that
another caller's record exists. Each verb re-runs the check instead of trusting an
earlier one. Caller-partitioned memory (`memory.CallerStore`) derives its namespace from
the context principal on every call.

### Kubernetes execution environments

The Kubernetes execution provider is a separate mTLS service used by `mecak8s`.
A session selects the deployment default, no filesystem, or an exact
operator-owned template ID and revision. The authenticated catalog returns only
bounded display metadata that the caller may select. Listing does not reserve an
environment; the provider and host policy reauthorize the exact template and
owner when the session binds.

The provider persists an exact environment reference, owner binding, execution
epoch, run claim, and revocation generation. A resumed session reattaches that
same reference rather than using the current default. Provider replicas use
Kubernetes compare-and-swap operations and claims to serialize environment work.
A lost claim or unprovable executor state fences the environment. Recovery and
retirement require the administrator to supply the recorded environment, epoch,
Pod UID, and PVC UID. Workspace PVCs remain retained until the separate delete
operation succeeds.

Provider TLS and client authorization are separate controls. Platform PKI issues
and projects the provider and client certificates and trust bundles. The provider
reloads valid projected TLS files independently of the client-policy manifest.
The manifest generation changes only for authorization policy updates. A valid
mTLS client still needs the manifest's exact client URI, template scope, owner
binding, run epoch, claim, and revocation checks for each operation. See
[Configure the Kubernetes execution provider](../../user-docs/operating/mecak8s/native-execution.md)
for the operator workflow.

**The classification guard.** Every boundary touching owned data is classified as
caller-owned, derived (follows from the current authorized run), shared-infrastructure,
or exempt, with a written rationale. `classification.go` covers the service and system
roots; `internal/app/catalog_classification.go` covers model-facing tools at their real
registration sites. `TestInvariant_owned_access_is_classified` and catalog assembly fail
on anything unclassified, so a new access path cannot skip ownership by being forgotten.

**System principals are scoped.** Each syscaller root is shared-infrastructure for one
narrow operation and fails every caller-owned check. The scheduler runs each fire under
the schedule's captured owner, so an ownerless schedule fails closed. **Raw drivers are
trusted infrastructure:** remote store and lease drivers receive no caller claims, so
only the server may reach them (`deploy/README.md` covers the isolation).

## The event log actor

`session.Event.Actor` records who acted, which can differ from who owns the session: an
approval must name the human who granted it. `Service.appendEvent` is the persistence
chokepoint, where the lease is checked and the durable cursor assigned, and it is the
only place the actor is stamped, from the verified request context. This keeps the loop
identity- and storage-agnostic and ties each event's actor to the context that
authorized the write; a second path could stamp the owner and misattribute an approval.

## Protected-resource discovery

With OIDC configured, `mecated` and `mecak8s` can publish RFC 9728 protected-resource
metadata (`protected_resource.go`) advertising the issuer, audience, public client ID,
and scopes, which `mecatui login` uses to enroll. The resource URL is explicit operator
configuration, never inferred from listeners or the untrusted `Host`. For the same
reason, protected API routes return a generic `Bearer` challenge: a subordinate path
cannot prove the exact resource identity. The anonymous metadata endpoint is HTTP-only.

## Internal credential store

`internal/adapter/credentialstore` stores opaque binary records with no OAuth or provider
semantics; it is `internal` because the engine never consumes it. `Reader` is read-only,
`ConditionalWriter` adds mutation, and `Store` combines both, so mutability is the
interface an adapter implements rather than a flag that could disagree. Every mutation
is compare-and-swap (a nil expected version creates only), so two processes refreshing
the same token cannot overwrite each other's rotation.

Adapters: in-memory, encrypted file (AES-256-GCM, owner-only, root and 32-byte key
injected by the caller), plain file, and a read-only environment reader. The file stores
coordinate cooperating processes on one host; they do not defend against the same OS
user or non-local filesystems.

Native LLM-endpoint OIDC credentials use the always-encrypted `mecatl/provider-oidc/v1`
namespace (`internal/adapter/llmendpoint`), keyed from the OS keyring or an
operator-named environment variable. A record's identity binds endpoint, issuer, client,
scopes, and trust policy, so changing any of them forces a new login instead of reusing
a token minted for something else. A crash between a provider-side refresh-token
rotation and the local save can still require a new login. MCP OAuth uses the same
store; see [extensibility](extensibility.md).

## Remote mecatui login

`mecatui login ADDRESS` enrolls a public OIDC client using Authorization Code with PKCE
(`internal/adapter/clientauth`), discovering issuer, client, audience, and scopes from
protected-resource metadata. First enrollment requires a confirmation that defaults to
no; later logins skip it only when discovery matches what was saved. Credentials are
bound to the canonical target, so one server's token is never sent to another.

`mecatui connect` never opens a browser. It prefers an explicit token, then explicit
anonymous, then a saved enrollment, which it refreshes on demand; only an OAuth
`invalid_grant` deletes it. Credentials use the OS keyring with an encrypted store, or a
plain owner-only file store, pinned per store root.

## Rate limiting, health, and shutdown

Rate limiting uses a per-client and a global token bucket with idle eviction. A
static-token client is keyed by its token and a verified caller by `(Issuer, Subject)`,
so rotating a credential is not a bypass. With OIDC on, a separate bucket limits
rejected tokens by direct peer IP before validation, protecting the IdP path; it ignores
forwarding headers, which callers control.

`/healthz`, `/readyz`, and the standard gRPC health service sit outside authentication
and rate limiting. Shutdown closes admission, drains the service, then stops gRPC and
HTTP within bounded timeouts.

## Multi-replica operation

**Affinity.** Clients send an optional `X-Mecatl-Session-ID` header so a gateway can pin
a session to one replica. It is compared byte-for-byte; a duplicate, illegal, or
mismatched value is rejected before work. It is a routing hint and grants no authority.
Outbound provider requests carry the active and root session IDs from the authoritative
run context, never forwarded from ingress.

**Single writer.** The run registry serializes one process; a session lease serializes
replicas. A replica that cannot acquire the lease refuses with HTTP 409 or gRPC
`FAILED_PRECONDITION`. A replica that loses the lease stops starting writes for that
session and cancels the run. That is local invalidation, not storage fencing: a storage
call already in flight may still complete. Handoff is interruptive: the client stream
drops, the client retries after routing and the lease TTL converge, and the successor
reloads the session. Lease mechanics are in [observability](observability.md); the Helm
chart creates no gateway or affinity policy.

## Deployment shapes

All three server roots share `app.Build` and `server.Service`; they differ around it.

**`mecated`** is the general-purpose daemon: listeners, TLS and authentication,
Prometheus and OpenTelemetry, and administrative subcommands. It defaults to interactive
approval and the strict posture. Session store and event log are each in memory, local,
or a remote driver. A local store directory always gets a file-lock session lease;
otherwise leasing is off unless the operator selects a lease backend.
`mecated config validate` checks operator settings offline with the runtime parser. See
[run mecated](../../user-docs/operating/mecated.md).

**`mecak8s`** is the Kubernetes peer. Pods hold no durable state by default: sessions and
the event log live in Redis, and a `coordination.k8s.io` Lease per session enforces the
single writer. It defaults to headless with the `auto` posture. Without a mounted
workspace, sessions are file-less or use an optional Redis virtual workspace. Readiness
is drain-gated, and a separate drain-only listener serves the `preStop` hook. On shutdown
in-flight runs are cancelled, not completed, since a long model turn cannot fit a
rolling-update grace period; the successor recovers the session from Redis. Server
certificates and Redis credentials reload in place, keeping the last valid set. See
[mecak8s](../../user-docs/operating/mecak8s.md).

**`mecatequi`** runs one prompt in CI against an in-process service with no listeners or
UI, emits a git diff, a JSON run summary, and optionally the event log, and maps the stop
reason to an exit code. It has no approver, so a main-engine permission request cancels
the run. See [mecatequi](../../user-docs/building/ci/mecatequi.md).

## Build and release

All modules and the workspace share the Go version that `go.work` declares. Releases
build images with ko, sign them with cosign, and attach an SBOM and SLSA provenance. `govulncheck`
runs per module through a fail-closed gate on reachable findings
(`.github/scripts/govulncheck-gate.go`), and Renovate keeps dependencies and SHA-pinned
actions current.

## Related

- [Governance](governance.md)
- [Observability](observability.md)
- [API surface](api-surface.md)
- [Caller identity](../../user-docs/features/security-and-execution/caller-identity.md)
- [Deployment guides](../../user-docs/operating/index.md)
