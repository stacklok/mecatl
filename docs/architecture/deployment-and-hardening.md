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
operation succeeds. The immutable recipe digest excludes the adjustable
`templates.<ID>.maxEnvironments` quota. Capacity is pooled across revisions of
one ID and persisted by CAS in its own retained ConfigMap; pending reservations
count before CR publication, and missing or malformed authority fails closed.
Ledgers contain JSON arrays of unique allocation names; `[]` is valid, while
`null`, invalid names, and duplicate names block reservation, release, and chart
rendering without rewriting retained state. Namespace
ResourceQuota budgets are explicit operator configuration. Both the per-ID ledger
and combined template/history
ConfigMap have a Kubernetes 1 MiB data limit. The selectable catalog remains
bounded to 64 items while retained-only history is bounded by storage bytes.
Startup validates every retained allocation's exact template and digest, then
checks each distinct RuntimeClass and StorageClass required by selectable recipes
or existing allocations once, including allocations of ineligible revisions.
Unused ineligible history is skipped. A missing required class blocks global
provider startup; there is no
per-allocation startup isolation or runtime fallback. Pruning checks the removed
revision against retained CRs and
resolved PVC/Pod identities under observed provider quiescence. Execution labels
or an `ExecutionEnvironment` owner reference without an environment label block
pruning; ordinary resources without execution evidence and verified unrelated
workspaces remain intact. The offline `deploy/execution-templates` generator and
provider loader share `CanonicalTemplateRevision` as the v1 recipe hash source.
It computes `v1-` followed by SHA-256 of `mecatl/execution-template/v1`, a NUL
byte, and Go's `encoding/json` representation of the typed `ProfileSpec`, not
the YAML text. Generation validates before creating a new values file;
validation of merged history uses the same loader without contacting Kubernetes.
The provider's ID-level default does not rewrite `mecak8s`'s explicitly pinned
`execution.templateRevision` or an existing session's binding.

Provider TLS and client authorization are separate controls. Platform PKI issues
and projects the provider and client certificates and trust bundles. The provider
reloads valid projected TLS files independently of the client-policy manifest.
The manifest generation changes only for authorization policy updates. A valid
mTLS client still needs the manifest's exact client URI, template scope, owner
binding, run epoch, claim, and revocation checks for each operation. Run acquire,
renew, and replay recheck template revocation and current client policy;
reference release and authorized lifecycle cleanup remain possible. The helper
has no service credentials or Kubernetes API token. mTLS authenticates services;
the verified OIDC issuer/subject still supplies the session owner. See
[Configure the Kubernetes execution provider](../../user-docs/operating/mecak8s/native-execution.md)
for the operator workflow.

#### Execution-provider qualification

From a checkout with the Taskfile's Kind prerequisites, run
`task e2e:k8s:execution:production` for the complete serial suite (45-minute test
ceiling), followed by mandatory `TestKindExecutionInFlightAutomaticRenewal`
(8-minute test ceiling, 7-minute context, 10-minute outer process deadline).
This test requests controlled renewal with the supported `cmctl renew` command
only after observing both the active operation and its started marker. The
production fixture installs cmctl v2.6.1 into its private, owned tools directory;
HTTPS downloads are checked against repository-pinned SHA-256 hashes before
making the binary executable. Each certificate has a separate renewal request.
Only an explicit update conflict permits a bounded, per-certificate retry, and
an already-issuing certificate is left to finish.

The command waits at a marker barrier while the test observes increased client
and server certificate revisions, changed client and server serials, and a
verified handshake against the provider's renewed projected identity. The test
atomically publishes the issued host client pair and calls `ReloadTLS` on the
same client during the active RPC. Fixture-admin exec releases the barrier on
the captured executor Pod, with its name, UID, environment, and active operation
verified before release. The original RPC must return a successful terminal
receipt, output, and workspace mutation with unchanged Pod/PVC, epoch, and grant.

The active-renewal budget is 180 seconds; the command's self-barrier expires
after 210 seconds, its timeout is 240 seconds, and the run claim lasts five
minutes. These bounds are intended to remove the roughly ten-minute scheduled
renewal wait; elapsed savings have not been measured. Normal PR checks and
native OpenRouter qualification use this controlled path. Certificate issuance
and Kubernetes Secret projection remain cert-manager/platform responsibilities;
the fixture does not edit cert-manager-owned Secrets. Runtime authorization,
status, and expiry semantics are unchanged.

The distinct long proof is `TestKindExecutionAutomaticCertificateRenewal`.
It exercises the **deployed `mecak8s` client's automatic reload loop** through
at least two observed client/server renewals and one minute beyond the original
server certificate's expiry. Remote tools must still work on the same session
and PVC with unchanged authorization generation. The mandatory in-flight test
uses a host-side client that mirrors the issued Secret and calls `ReloadTLS`
explicitly, so it cannot replace this deployed-client proof.

To include the long proof, explicitly select it:

```sh
MECATL_EXECUTION_QUAL_NATURAL_EXPIRY=1 task e2e:k8s:execution:production
```

The selector defaults to `0`, accepts only `0` or `1`, survives the runner's
clean-environment re-exec, and rejects `1` outside the production profile before
provisioning. The long test runs once with an 80-minute ceiling (78-minute
context), before the mandatory in-flight test. The runner logs each renewal
phase's start and elapsed completion, or an explicit natural-expiry skip.
In the secret-free `k8s-e2e.yml` workflow, dispatch with `natural_expiry: true`
for this proof; the boolean defaults to false. That workflow splits the suite
across independent Kind clusters: `deploy/mecatl-execution-kind/shards.txt`
assigns each cluster test, including both renewal proofs, to exactly one shard,
and `MECATL_EXECUTION_QUAL_SHARD` selects a shard locally. Per-shard job budgets
are 75 minutes normally and 210 minutes for explicit long mode, including setup
and cleanup. Including the in-flight outer process deadline, the normal 75-minute
budget leaves 20 minutes beyond the test/process ceilings; long mode leaves
75 minutes beyond all three.

The manual native job always sets the selector to `0`: its production step has
a 100-minute ceiling around a 98-minute command timeout. All native step ceilings
sum to 150 minutes within a 160-minute job, reserving 10 minutes of overhead.
Reviewed-SHA verification, disabled caching, successful same-run production
qualification before credential staging, owned-cluster handoff, and cleanup
remain mandatory. Run long renewal qualification once for the reviewed change
rather than repeating it before model coding.

The native workflow explicitly selects `MECATL_EXECUTION_QUAL_HANDOFF=1`.
This CI-only mode requires fresh, retained production state, numeric GitHub run
ID/attempt, an exact clean checkout matching the supplied reviewed SHA, and
`GITHUB_OUTPUT`. Its serial invocation excludes only the exact
`TestKindExecutionQualification` test. After the serial Helm-lifetime mutations,
optional natural-expiry proof, and mandatory controlled renewal finish, the
same compiled binary runs that full mock proof once. Its successful exit
includes deferred cleanup; the runner then checks readiness and publishes
`handoff_complete=1` with a nonsecret digest and SHA/run/attempt outputs. Early
state/cluster outputs remain cleanup ownership hints, not qualification evidence.

The workflow requires production-step success and the completion output, then
runs `live.sh --verify-handoff` before creating the private credential file.
This secret-free check validates the same run, checkout, owned state and cluster,
readiness, and current digest. Only after it succeeds does the workflow stage
the private credential file. The subsequent credentialed `live.sh` invocation
revalidates the handoff before calling credentialloader to create the Kubernetes
Secret. The digest binds the final provider, agent, and OIDC issuer Deployment
UIDs and specs, pinned containerd image alias descriptors, current template
catalog/revision, mock configuration, public security manifest/authority, public
trust, and owned cluster/kubeconfig identity. Tracked chart/config/build inputs
are bound by the exact clean checkout. Secret UID/source metadata is included;
private Secret data and auto-renewing leaf bytes/status clocks are excluded.
Successful reuse keeps the qualified reattach deployment and pinned images
without rebuilding, loading, reinstalling mock configuration, restarting, or
rerunning the mock proof before credential staging.
Malformed or drifted reuse fails instead of falling back. This is a trusted
same-job handoff, not a historical receipt or an attestation against a compromised
runner. `MECATL_EXECUTION_LIVE_SESSION` remains a separate standalone resume
selector and is rejected in qualified mode.

Both handoff selectors default to `0` and accept only literal `0` or `1`.
Ordinary production qualification keeps its complete serial suite and renewal
ordering. Standalone live qualification keeps its existing build/load/mock
setup and full mock prerequisite before Kubernetes credential staging, including
its existing continued-session behavior.

Coverage is split by the regression each layer can establish:

| Evidence | Regressions covered |
| --- | --- |
| `executioncontroller/security_rotation_test.go` | Coherent `..data` reads despite projection switches; incomplete or mismatched TLS fails closed; ordinary TLS reload preserves policy. Injected `SecurityManager.now` checks snapshot readiness, TLS admission, and authorization immediately before, at, and after expiry, then recovery without a policy change. |
| `executionclient/client_test.go` | Invalid or mismatched client reload closes the stale transport and valid material restores a fresh transport. |
| `executionclient/renewal_test.go` | Verified same-identity RPCs drain across replacement; synctest advances retired-channel expiry and checks reference cleanup; withdrawn trust terminates active RPCs. |
| `executioncontroller/operation_peer_test.go` | Original-peer expiry and CA/policy withdrawal fail closed at admission, during a command, and at completion; ordinary renewal preserves authority. |
| `e2e/k8s_execution/production_qualification_test.go` | Real projected Secret/policy updates, invalid-generation denial and recovery, trust withdrawal, durable authorization, and operation fencing across deployed replicas. Included in the unchanged serial suite. |
| Mandatory in-flight controlled renewal | Real cmctl-triggered issuer/client and projected provider renewal behind an active command barrier; explicit mirrored-client reload before release; terminal receipt, exit status and output, workspace mutation, preserved Pod/PVC, epoch and grant, and a later mutation without fencing. Session continuity is also covered by the serial mock journey. |
| Explicit natural-expiry renewal | Deployed `mecak8s` automatic client reload across repeated issuer renewals and original expiry; remote-tool session/PVC continuity and unchanged policy. |
| Full mock qualification | Ordinary runs execute it in the serial suite. Native handoff executes it exactly once after all serial/renewal mutations, proving the final installed authority/catalog and leaving the qualified reattach deployment. |
| `deploy/mecatl-execution-kind/{fixture_tls,repair,ci}_test.go` | Behavioral shell fixtures exercise proof ordering and failure publication, clean-environment selectors, valid no-rebuild reuse, standalone mock-before-stage, and rejection of SHA/run/attempt/state/cluster/config/spec/image/catalog/authority drift before credentialloader. |

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
otherwise leasing is off unless the operator selects a lease backend, and startup warns
when that leaves a store other processes may share without one.
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
