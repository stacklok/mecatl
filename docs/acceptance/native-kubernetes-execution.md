# Native Kubernetes execution — acceptance plan

**Contract:** human-reviewed/v2
**Work classification:** Architectural — introduces a controller/executor trust boundary, durable environment ownership, Kubernetes resources, and an optional mecak8s deployment integration.
**Decision record:** [ADR 0350](../adr/0350-native-kubernetes-execution.md)
**Phase:** native Kubernetes execution first slice
**Status:** draft, amended 2026-09-21. Exact proposed contracts are authored below; API, schema, and security review approvals remain outstanding.
**Delivery:** Split. Human-authorized draft stack: the Plan / Interface PR targets `main`; the draft Implementation PR targets `plan/native-kubernetes-execution`. Authorization includes developing this amendment before plan merge, not approval or merge readiness. Human alone merges.
**Expected tasks:** draft implementation decomposition remains run-local; no approved orchestration baseline.
**Issue:** None — planning request has no linked issue.
**Plan PR:** [#1579](https://github.com/stacklok/mecatl/pull/1579), draft on `plan/native-kubernetes-execution`; implementation [#1614](https://github.com/stacklok/mecatl/pull/1614) remains draft.
**Approved baseline:** absent. Plan worktree baseline `a5030545e87e81aad12fba4c49f0c9933a634b5e` and implementation source reference `9bb4d89b8aa497c9d2cd6aa4a94ab9262a913364` are evidence, not approval.

The [implementation proof map](#implementation-proof-map) distinguishes existing draft coverage and
successful qualification from amendment proofs still pending. Implementation-only source paths below
refer to that pinned implementation reference; they are future code proofs on this documentation-only
branch, not claims that the files or tests already exist here.

The smallest demonstrable slice is an independently deployed execution environment provider service
with its own controller and executors. Mecak8s is an optional client: its adapter obtains a persistent
Kubernetes workspace and bound Shell through the provider API without owning Pods, PVCs, or the
controller lifecycle. The provider keeps the existing `tool.Environment` and file-tool schemas,
uses an operator-selected profile, proves behavior offline in kind, and makes no remote-project-ingestion claim.
ADR 0048 continues to govern storage-free mecak8s; this separate provider does not supersede it.

## Human decisions

- [x] Keep the execution environment provider a separate service in this monorepo. — Decision: the provider owns its controller, CRDs, Pods, PVCs, binaries, images, chart, and lifecycle; mecak8s consumes its API through an optional adapter and remains storage-free under ADR 0048, which is not superseded.
- [x] Choose a purpose-built controller rather than Agent Sandbox. — Decision: own the controller/executor boundary and its CRD.
- [x] Use typed gRPC for the private provider network protocol. — Decision: replace HTTP/JSON with protobuf RPCs over mandatory mTLS; keep the public Harness API unchanged and preserve the separate provider service boundary.
- [x] Qualify the gRPC coding flow with a real OpenRouter credential. — Decision: run the explicit live kind target after mock qualification using only the trusted runtime loader; prove model tool execution and independently verify workspace artifacts and a successful test command, then restore mock configuration and delete only the run-owned Secret by UID.
- [x] Keep Kubernetes execution optional. — Decision: disabled/default deployments remain Kubernetes-execution-free.
- [x] Retain one PVC per logical environment by default. — Decision: session deletion does not delete a workspace; committed data is removed only through explicit retirement. Storage class, size, and quotas are operator-configured.
- [x] Set the first-slice source and execution boundary. — Decision: use a blank tool-seeded workspace, existing filesystem tools, foreground Shell, and operator-global instructions only, with no warm pools or automatic idle suspension. Defer Git clone, private credentials, remote project ingestion, background commands, and isolated delegation; schedules remain excluded.
- [x] Set the initial security and qualification posture. — Decision: the first production slice is authenticated and authorized in an operator-controlled cluster, with the limits below, not hardened hostile multitenancy. Deterministic mock/kind qualification precedes the separately invoked native-provider OpenRouter coding qualification required by the checked live decision above; this is not an always-on live CI dependency.
- [x] Choose mTLS service links plus least-privilege grants scoped to environment, operation, and ownership epoch. — Decision: uncertain takeover fails closed; old-writer termination or external fencing is required before replacement.
- [x] Handle live-provider credentials only through a trusted runtime loader. — Decision: it may be consumed directly from its protected file channel to a narrowly scoped HARNESS-only Secret, with redacted failures, before child processes or tools can inherit it. The agent never accesses the file or Secret. No agent or tool reads, displays, or loads it into model/tool output, argv, Helm values, disk manifests, git, logs, artifacts, or the execution workload; mock tests do not access it. Endpoint, protocol, and model selection still require verified nonsecret configuration.
- [x] Develop scoped administrative authority in this existing draft stack. — Decision: a distinct administrator URI may administer only explicitly named creator URIs through `administratorFor`; this grants no data-plane or owner-attestation authority. Existing self-administration remains compatible, with no namespace-wide administrator grant.
- [ ] Review and approve the authored private control/data-plane API, reference transaction, run ownership, errors, and compatibility contract below.
- [ ] Review and approve the authored CRD/status schema v2 and configuration contract, including scoped administration, continuing provisioning retries, and retained-resource Helm lifecycle qualification.
- [ ] Security-review and approve the authored grant trust, rotation/revocation, replay bounds, termination acknowledgement, fencing, and retained authority-history contract.
- [ ] Decide the release maturity designation. The target remains an operator-controlled first production slice with explicit limits; the CRD name `v1alpha1` does not settle an “alpha” product label or waive any completion gate.

## Interface contract

This documentation-only amendment traces the draft implementation at `9bb4d89b8` against the
proposed contract. Authored specifications, advisory review, and passing CI do not constitute human
approval. Scoped administration, continuing provisioning retries, and Helm lifecycle proofs exist in
that source; final runtime qualification and the unchecked reviews above remain pending.

- **gRPC / protobuf:** Public Harness protobuf is unchanged. The private `mecatl.execution.v1.ExecutionProviderService` uses mandatory mTLS, a `host:port` endpoint, protocol identifier `execution-grpc/1`, and an 8 MiB message bound. The exact wire schema is the implementation reference's `contracts/proto/mecatl/execution/v1/execution.proto`, generated with `task generate`. The RPC/field summary below is the review contract; there is no HTTP/JSON compatibility endpoint.
- **Exported Go APIs / interfaces:** No `engine/` exported API change. `tool.Environment` remains identity + Workspace + independent ReadLedger + optional bound CommandRunner (`engine/tool/environment.go`). Host-only seams in the implementation's `internal/adapter/server/placement.go` are specified below; Kubernetes dependencies stay outside the engine.
- **Tool schemas:** Existing filesystem tools and foreground Shell remain byte-compatible, including recorded reads and conditional CreateFile/ReplaceFile. Copy/Move/Remove retain their own conformance contracts. Shell writes bypass file CAS (`engine/tool/tool.go`). Remote catalog attenuation and its model-visible posture must be tested through the real composition factory. Unsupported operations never execute locally.
- **CLI / config:** Disabled by default. Mecak8s accepts `--execution-enabled`, `--execution-endpoint`, `--execution-profile`, `--execution-tls-ca`, `--execution-tls-cert`, and `--execution-tls-key`. Enablement requires all five string settings and OIDC caller ownership enforcement; it conflicts with `--workspace` and `--redis-filesystem`. The separate `deploy/helm/mecatl-execution` chart owns the provider deployment. The default mecak8s chart has no provider dependency. Exact provider profiles and security configuration are below.
- **Events / persistence:** No public event widening. Provider CR status, reference intents, run/operation claims, lifecycle receipts, security high-water state, and capacity reservations are durable provider state. Exact session `EnvironmentRef.Revision` remains immutable and separate from transient execution epoch and grant generation. The namespaced CRD and schema-v2 fields are below.
- **Security / authority:** TLS 1.3 with one canonical allowlisted client URI SAN, short-lived signed grants, exact owner/reference checks, and independent environment fencing. Scoped administration extends only the provider's existing client policy. No controller credentials, signing keys, provider secrets, or service-account token enter arbitrary-shell workloads. This is an operator-controlled cluster boundary, not a hostile-multitenancy claim.
- **Compatibility / migration:** Additive and opt-in for existing local and `no-fs` sessions, with no local fallback for remote failures. Private schema migration is explicit and quiesced; CRD upgrade is manual. The `administratorFor` extension changes neither public Harness nor engine nor private protobuf. Old strict decoders reject it; use a quiesced provider upgrade, not a claimed mixed-version rolling upgrade. Neither downgrade nor reinstall may reset authority high-water state.

## Proposed technical specifications

These sections define the proposed interface details referenced by the categories above.

### Private RPC and host contract

Wire fields use protobuf snake_case. `EnvironmentRef = {id, revision}` and `Owner = {issuer, subject}`.
`RequestContext = {environment, owner, binding_id, epoch, grant, run_id, claim_id, grant_generation}`.
Environment and owner matches are exact; a client cannot select a creator through any new wire field.
The host maps this ref to `session.EnvironmentRef{Kind:"kubernetes", ID:id, Revision:revision}`;
reattachment never substitutes a current default or Pod UID.

| RPCs | Exact request/result and semantics |
|---|---|
| `ValidateProfile` | `{profile}` → `{profile, digest, capabilities, max_file_bytes, max_command_bytes, max_command_duration_millis}`. Side-effect-free preflight, never `Bind` or allocation. |
| `EnsureEnvironment` | `{binding_id, profile, owner, operation_id}` → `{environment, epoch, ready, grant, grant_expires_at, grant_generation}`. Server-minted final binding ID, authenticated creator, owner, and profile fingerprint identify one allocation; drift conflicts instead of adopting another environment. |
| `AttachEnvironment` | `{context, purpose:"session"}` → the same result fields as Ensure. Exact persisted ref and reference membership; attach does not confer an active execution claim. |
| `AcquireRun` | `{environment, owner, binding_id, run_id, operation_id, ttl_millis}` → `RunClaimResponse{environment, binding_id, run_id, claim_id, epoch, grant_generation, grant, expires_at}`. One published binding holds environment-wide execution ownership at a time. TTL is 30s–5m; adapter default 1m. |
| `RenewRun`, `ReleaseRun` | Both carry `{environment, owner, binding_id, run_id, claim_id, epoch, operation_id, grant_generation}`; Renew adds `ttl_millis` and returns RunClaimResponse; Release returns Empty. Exact resourceVersion CAS and operation replay checks prevent stale renewal/release. |
| `CommitReference`, `AbortReference`, `PrepareReferenceDelete`, `ConfirmReferenceDelete`, `CancelReferenceDelete` | `{environment, owner, binding_id, operation_id}` → Empty. Durable reference transaction described below. `ReleaseReference{context}` is a compatibility entry, not permission to drop ambiguous or active references. |
| `ReserveSuccessor` | `{environment, owner, source_binding_id, destination_binding_id, operation_id}` → `{environment}`. Reserve another reference to the same exact environment under exclusion; not a filesystem clone. |
| `ListReferenceIntents` | `{owner, limit, environment, binding_id}` → bounded `intents[{environment, binding_id, state, operation_id, source_binding_id, created_at, owner}]`. Client-scoped reconciliation; attested owner information returns only to the owning creator client. |
| `RetireEnvironment`, `ReplaceExecutor`, `RecoverEnvironment` | `{environment, owner, expected_execution_epoch, expected_pod_uid, expected_pvc_uid, operation_id}` → Empty. Administrative admission retains exact epoch/UID checks, reference/idle preconditions, and operation receipts. Recovery accepts independently observed exact terminal Pod proof, never a caller boolean asserting fencing. |
| `DeleteRetiredEnvironment` | `{environment, owner, expected_pvc_uid, operation_id}` → Empty. Explicit destructive retained-PVC deletion after retirement and reference exclusion, with UID-preconditioned deletion and capacity release; not default uninstall behavior. |
| `MigrateEnvironment` | `{environment, owner, optional expected_schema_version, expected_pod_uid, expected_pvc_uid, operation_id}` → Empty. Presence is mandatory; 0 or 1 selects the compatible prototype migration to 2. Unknown/current versions are not a general migration or rollback API. |
| `RevokeEnvironment` | `{environment, owner, expected_grant_generation, operation_id}` → `{grant_generation}`. CAS advances revocation generation without changing execution epoch; repeat exact operations return the receipt rather than revoking twice. |
| `Files` | `{context, operation, path, destination, pattern, data:bytes, version:bytes, limit}` → `{data:bytes, version:bytes, info, entries, paths, matches, authority_target, authority_workspace}`. Closed enum: READ, RESOLVE_AUTHORITY, STAT, CREATE, REPLACE, LIST, REMOVE, RENAME, COPY, GLOB, GREP; UNSPECIFIED is rejected. FileInfo carries `{name, size, mode, mod_time, is_dir}`; GrepMatch carries `{path, line, text}`. Paths remain confined inside the bound workspace. |
| `StartCommand` | `{context, command, timeout_millis}` → `{command_id, state, result}`. Foreground unary RPC, not a detached job or server stream. Result carries `{command_id, state, exit_code, stdout:bytes, stderr:bytes, next_offset, truncated, terminal_receipt}`. Closed states are RUNNING, SUCCEEDED, FAILED, CANCELLED, FENCE_UNKNOWN (UNSPECIFIED rejected). Context cancellation is not proof of process termination. |
| `CommandStatus`, `CancelCommand` | `{context, command_id, offset}`. Authenticate/authorize and validate first, then return gRPC `Unimplemented` for this foreground-only slice. No command-stream RPC is implemented. |

Sanitized errors carry `ErrorDetail{code, retryable}`. Stable mappings are `invalid_argument`→InvalidArgument,
`unauthenticated`→Unauthenticated, `permission_denied`→PermissionDenied, `not_found`→NotFound,
`already_exists`→AlreadyExists, `conflict`/`version_mismatch`/`directory_not_empty`→Aborted, `not_ready`→Unavailable,
`fence_unknown`→FailedPrecondition, `resource_exhausted`→ResourceExhausted, and `internal`→Internal.
Only `not_ready` and `unauthenticated` can carry `retryable:true`; this is not permission to retry a
mutation automatically. Grant-expiry refresh is read-only. Backend details never cross the boundary;
absent and out-of-scope environments have the same non-disclosing denial. Foreground detached-control
`Unimplemented` is the explicit exception to the structured error vocabulary.

Host-only interfaces keep allocation and execution separate:

- `PlacementValidator.ValidatePlacement(context.Context) error` validates without allocation.
- `PlacementBindRequest.BindingID session.SessionID` is the final server-minted binding;
  `PlacementBinding.Commit func(context.Context) error` publishes after store persistence, and
  `Close func() error` aborts only a conclusively uncommitted reservation.
- `ExecutionAccess` supplies `Applies(session.EnvironmentRef) bool` and
  `AcquireRun(context.Context, ExecutionRunRequest) (ExecutionRunHandle, error)`;
  the request contains `Ref, Principal, BindingID, RunID`, and the handle supplies
  `Environment() tool.Environment`, `Renew(context.Context) error`, and `Release(context.Context) error`.
- `PlacementSuccessorReservoir.ReserveSuccessor(context.Context, PlacementSuccessorRequest) (PlacementBinding, error)`
  and `ReferenceLifecycle.PrepareReferenceDelete(context.Context, PlacementSuccessorRequest) (ReferenceDeleteHandle, error)`
  use `Ref, Principal, SourceBindingID, DestinationBindingID`. ReferenceLifecycle also has `Applies`;
  the delete handle has `Confirm(context.Context) error` and `Cancel(context.Context) error`.
- `ReferenceIntentLifecycle.ListReferenceIntents(context.Context, int) ([]ReferenceIntent, error)` and
  `CommitReferenceIntent`, `ConfirmReferenceIntentDelete`, `CancelReferenceIntentDelete` (each
  `(context.Context, ReferenceIntent) error`) reconcile durable ambiguity. ReferenceIntent contains
  `Ref, Principal, BindingID, SourceBindingID, OperationID, PendingDelete`.
- `PlacementReattacher.Reattach(context.Context, PlacementReattachRequest) (PlacementBinding, error)`
  uses exact `Ref, Principal, Scope, BindingID`. Public callers receive display metadata only.

### Reference transaction, schema v2, and fencing

Ensure reserves `PendingCreate` before session-store publication; confirmed publication permits
Commit→`Published`. Abort is safe only when publication is conclusively absent. A lost commit response
remains recoverable, never TTL garbage. Delete uses `Published`→`PendingDelete` before store deletion,
then Confirm removes the reference or Cancel restores Published after confirmed non-deletion.
Store unavailability retains the intent. Clear/Fork reserve a destination reference under environment
exclusion, publish it, and preserve the source on failure. Clear creates empty history, Fork copies valid
history, both keep the same remote ref; neither is a workspace clone. A retiring environment admits no
new reference or successor. Retirement requires no references, including pending ones.

The implementation CRD is namespaced `execution.mecatl.dev/v1alpha1`, kind `ExecutionEnvironment`,
plural `executionenvironments`, with a status subresource. `v1alpha1` is the Kubernetes API name,
independent of the unresolved release maturity designation. The structural schema is in the implementation's
`deploy/helm/mecatl-execution/crds/executionenvironment.yaml`:

| Area | Exact fields and constraints |
|---|---|
| `spec` | `schemaVersion=2`; immutable `allocationID, revision, ownerHash, clientHash, bindingID, requestFingerprint, profile, profileDigest, image, storageClass, storageSize, resources`. Optional immutable `ownerIssuer, ownerSubject` retain attested ownership. `desired` is Active or Retiring. Hashes are 64 lowercase hex characters; profileDigest is `sha256:` plus 64 hex; image is digest-pinned. `allocationID`/profile ≤63 characters, revision ≤64, bindingID ≤253. |
| Core `status` | `schemaVersion=2, observedGeneration, epoch, grantGeneration, fenceState, references, pvc{name,uid}, pod{name,uid}, conditions`. Epoch and grantGeneration are positive signed-int64-bounded counters; fenceState is Healthy or FenceUnknown. Conditions carry `type,status,reason,message,observedGeneration,lastTransitionTime`; they describe observed spec generation, not durable identity. |
| References | At most 64 records, map-keyed by `bindingID`, with `state, operationID, sourceBindingID, createdAt`; state is PendingCreate, Published, or PendingDelete. |
| `activeRun` | `bindingID, runID, claimID, operationID, ownerHash, clientHash, epoch, grantGeneration, expiresAt`. |
| `activeOperation` | `id, operation, startedAt, claimID, runID, epoch, holderID, renewedAt, expiresAt`. Lease-holder identity is replica-specific; an expired holder remains recorded for recovery. |
| Receipts | `renewReceipts[{operationID,fingerprint,expiresAt}]` and `revocationReceipts[{operationID,fingerprint,expectedGrantGeneration,grantGeneration}]`, each capped at 32. Receipt eviction does not authorize replay past current claim/generation preconditions. |
| Lifecycle | `lifecycleOperation{id,type,phase,expectedEpoch,expectedPodUID,expectedPVCUID,createdAt}`; types ReplaceExecutor, RetireEnvironment, DeleteRetiredEnvironment; phases Quiescing, WaitingForTermination, RemovingPodFinalizer, WaitingForPodDeletion, CreatingReplacement, DeletingPVC, ReleasingSlot. |
| Recovery/migration | `terminationProof{operationID,podUID,pvcUID,epoch,podPhase,observedAt}` (Succeeded/Failed only); `lastReplacement{operationID,previousPodUID,replacementPodUID,pvcUID,previousEpoch,replacementEpoch}`; `migrationOperation{id,fromSchema,expectedPodUID,expectedPVCUID}` and `lastMigrationOperationID, lastMigrationFromSchema` (integer 0 or 1; presence required for completed replay). Completed receipts expire atomically when replacement publishes the new Pod identity; neither the old request nor the same operation with replacement UIDs may revive them. Completed and concurrent replay must match the operation, source schema, exact subject, and runtime UIDs; old receipts without source-schema presence fail closed. Schema 0/1 is accepted only for explicit compatible migration, never ordinary execution; unknown schemas fail closed. |

Run claims serialize environment use independently of session leases. A separate 30s operation-holder
lease renews every TTL/3. Every operation rechecks current claim, owner, creator, revision, epoch, and
grant generation against resourceVersion-CAS state. Holder loss, transport cancellation, revocation, or
uncertain helper completion cancels work and preserves fencing uncertainty; it does not clear operation
identity to let another writer start. Exact terminal proof is persisted before removing the executor
finalizer or replacing the Pod. Missing/unobservable Pods and missing or mismatched authoritative PVC/Pod
UIDs never justify automatic replacement. External compute/storage fencing requires an operator runbook;
the API has no force-takeover assertion. Helper process teardown must include detached descendants or
fail closed. Credential-free Kubernetes exec stdin framing is an internal helper transport, not another
provider network protocol or a trusted workload-supplied proof of node termination.

**Provisioning amendment:** transient PVC/Pod create failures, including quota saturation, retain
sanitized Ready=false conditions and schedule rate-limited retries for as long as the CR exists.
Restoring quota must converge without another reference/spec change, restart, or manual reconcile.
There is no finite retry count that silently forgets an existing unready allocation. Identity/ownership
failures remain fail-closed with no replacement, however often reconciliation runs. The inspected queue
already rate-limits returned errors; create-error branches currently return the condition-write result,
which can be nil. Merely retaining that queue is not proof of continuing provisioning retries.

### Provider configuration, rotation, and scoped administration

The strict profile YAML is `profiles: {<name>: ProfileSpec}`. ProfileSpec requires
`image, storageClass, storageSize, cpuRequest, memoryRequest, cpuLimit, memoryLimit,
ephemeralStorageRequest, ephemeralStorageLimit, tmpSizeLimit, runtimeClassName,
maxFileBytes, maxCommandBytes, maxCommandDuration, maxEnvironments`. Positive resource quantities,
request≤limit, available RuntimeClass, and digest-pinned images are required. Protocol ceilings are
5 MiB file payloads and 1 MiB command input/output bound; profiles tighten them. Command duration is
positive and ≤30m; maxEnvironments is 1–10,000. Helm values require
`provider.image, provider.securitySecretName, provider.securityManifest`, explicit ingress selectors,
API/DNS CIDRs, and enabled NetworkPolicy/resource governance. Replicas default to 2; stream/RPC/per-client
RPC limits default to 64/128/32. Arbitrary workload Pod specs and paths are not public inputs.

`provider.securityManifest` is a JSON **string**, decoded strictly by the provider; it is not a new Helm
RBAC object. Manifest version 1 has `version, generation, issuer, audience, activeKeyID, grantTTL,
clockSkew, keys, tls, clients`. `keys[]` has `id, version, file, publicKeySHA256, activateAt, verifyUntil,
state` (active, verify-only, revoked); TLS has `certificateFile, privateKeyFile, clientCAFile`, confined
relative to the mounted security directory. Secret keys are the operator-chosen filenames referenced
by that manifest, not credential literals in values. At most 64 keys and 256 clients; grantTTL is
positive and ≤5m, clockSkew is non-negative and smaller than TTL. No SPIFFE issuer deployment is assumed.

Ed25519 grants use header `{alg:"Ed25519", kid, typ:"MECATL-GRANT"}` and claims
`iss, aud, client, owner, binding_id, run_id, claim_id, environment, epoch, grant_generation,
operations, not_before, expires_at, nonce`. The provider verifies exact operation and current durable
claim, not just signature/expiry. Rotation reloads an immutable snapshot (default 2s polling) and checks
a durable ConfigMap high-water ledger `{generation,digest,fingerprints}` on each RPC, including existing
connections. Key windows, CA/client removal, equal-generation policy drift, key-ID/version reuse with
different material, and rollback fail closed. RevokeEnvironment advances the separate per-environment
grantGeneration. Neither revocation nor a changed security generation constitutes termination proof.

**Rotation publication amendment:** use the existing filename fields as immutable material identities.
Every changed signing key, server certificate, server private key, or client-CA bundle gets a new,
generation-specific filename; never overwrite a referenced name with different bytes. Stage the added
Secret entries before publishing the higher-generation manifest, retaining old entries until no live
or in-flight manifest references them. ConfigMap and Secret projections are not atomic together.
A manifest that arrives before its new files must fail closed without advancing the ledger; a material
projection that arrives first must leave the old manifest's authority unchanged. The loader reads one
manifest and confines each named file through `os.Root`; no projection-pinning helper, new config field,
proto, or engine API is introduced. Before a positive rotation barrier, qualification observes the
intended durable ledger generation; cached PodReady alone cannot establish it. Runtime byte immutability
is an operator publication obligation, not a newly enforced file-history registry. If reused filenames
have already published mixed material,
retries cannot repair equal-generation digest drift: publish a complete bundle at a higher generation,
never reset the ledger.

A peer may advance the shared ledger after a pinned replica successfully acquires a claim. Its next
read must return structured retryable `not_ready` before dispatch until that replica reloads. Positive
qualification uses a bounded read-only content poll for **only** `CodeNotReady && Retryable`; wrong
content and all other errors fail immediately. This amendment adds no mutating retry. Negative
old-client and revoked-key checks use a current, nonexpired claim, not a released phase claim.

**Scoped-admin amendment, exact proposed configuration:** add optional `administratorFor: []string`
to each `clients[]` entry, matching existing lowerCamelCase `mayAttestOwner` and `administrator`.
For example, the nonsecret manifest fragment is:

```json
{"clients":[
  {"uri":"spiffe://example.com/mecatl/harness","mayAttestOwner":true,"administrator":false},
  {"uri":"spiffe://example.com/mecatl/operations","mayAttestOwner":false,"administrator":true,
   "administratorFor":["spiffe://example.com/mecatl/harness"]}
]}
```

This is a policy fragment, not a complete install manifest. The proposed internal field is
`ClientPolicy.AdministratorFor []string`; no new public API, engine seam, proto field, or general RBAC
layer is needed. The strict manifest decoder owns validation because Helm's existing field is a string:

- Absent/empty list preserves self-admin only when `administrator:true`. A nonempty list requires
  `administrator:true`, contains at most 256 unique canonical URI strings, and rejects wildcards,
  duplicates, malformed entries, and noncanonical forms. Reuse the existing URI validator: nonempty
  scheme/host, lowercase canonical scheme/host, no userinfo, query, or fragment. No prefix matching.
- Entries name immutable **creator** URIs, not owners, namespaces, or admin-to-admin delegation.
  A creator need not remain in the current client allowlist: revoking its login must not prevent
  explicitly scoped retirement of its retained allocations. The list alone grants no owner attestation.
- One shared administrative subject decision loads the environment, preserves its immutable
  `spec.clientHash`, and matches it against the authenticated admin's own URI hash or an exact listed
  creator URI hash. Keep the actual caller separate from the matched creator; do not rewrite creation
  identity or trust a client-supplied creator. Require exact ownerHash and revision before disclosing
  lifecycle state, then retain each RPC's epoch/UID/schema/generation and operation-replay preconditions.
- Apply the same decision to RetireEnvironment, ReplaceExecutor, RecoverEnvironment,
  DeleteRetiredEnvironment, MigrateEnvironment, and RevokeEnvironment, including replay and CAS retry
  paths. A receipt is never an authorization bypass. Out-of-scope and missing targets are indistinguishable.
- Administration never grants AttachEnvironment, Files, Shell/StartCommand, run claims, reference
  discovery/mutation, or `MayAttestOwner`. Their existing creator and owner checks remain unchanged.
- Include the sorted scope list in the canonical authority digest. Scope removal at a higher generation
  must deny subsequent RPCs on already-open connections, including receipt replay; equal-generation scope
  edits fail closed. Already admitted lifecycle intents may finish safe reconciliation, but removal
  authorizes no new admin request. Old binaries reject the unknown field. Quiesce and upgrade all provider
  replicas before publishing it; do not claim mixed-version rolling compatibility.

### Helm lifecycle amendment

Default uninstall retains runtime CRs/PVCs and may leave executor Pods running. Consequently it must
also retain workload default-deny/profile NetworkPolicies, the security authority high-water ConfigMap,
and `mecatl-execution-profile-allocations` capacity ledger. Those resources live as long as retained
allocations/executors, not just the provider Deployment. Never render empty data over a retained ledger,
reset its generation, or recreate capacity as empty. Retain operator-owned security material and the
nonsecret profile/security configuration needed for exact reattachment; no chart hook deletes them.

Supported reinstall uses the **same release name and namespace** and unchanged resource names/profile
identity. It verifies retained resource identity and Helm ownership metadata, explicitly reuses/adopts
only that release's resources, preserves ledger contents, and fails on missing authority history or
foreign/ambiguous ownership while allocations survive. Fresh install initializes ledgers only when
there are no retained allocations to authorize. Changed release/namespace adoption, namespace deletion,
CRD deletion with live resources, force cleanup, arbitrary chart rollback, and authority-generation
reset are unsupported. CRDs require manual upgrade before a quiesced compatible schema migration;
unknown schema versions cannot be downgraded. Ordinary compatible upgrades also preserve live ledger data.

Qualification must execute real Helm install→upgrade→uninstall→same-release reinstall with retained
PVC data and executors. During provider absence, enforcing-CNI probes must still deny forbidden traffic.
After reinstall, current credentials reattach exactly, old authority/grants stay rejected, capacity
reservations still constrain allocation, and mismatched ownership/history fails safely. Annotation
string checks and the existing no-deletion-hook test are necessary but insufficient. Inventory both
ledgers and retained isolation/configuration in the resource and rehydration ledgers in
`docs/adr/0027-cloud-native.md` during implementation; no destructive hooks or automatic force deletion.

## In scope — 7 scenarios, in implementation order

### Scenario 1 — Disabled means absent, including startup validation

The optional deployment boundary follows [draft ADR 0350](../adr/0350-native-kubernetes-execution.md).
Startup preflight uses side-effect-free `ValidatePlacement`/`ValidateProfile`, not `Bind`
or a throwaway allocation. CLI and real composition tests below exercise that boundary.

**Acceptance:**
- AC1.1: With execution disabled, mecak8s creates no Kubernetes execution client, informer, goroutine, CRD, RBAC, controller, executor, PVC, or Pod, and performs no execution API call.
  - verify: `TestDisabledExecutionStartupDoesNotContactExecutionOrKubernetes`, `TestDisabledCompositionPreservesAllocationsAndIndependentReconciliation`; offline CLI `run()` reaches listener setup with poison execution TLS paths and a test-only kubeconfig/API endpoint untouched. That poisoned-endpoint test guards disabled startup contact; the lifecycle fixture's fake clients are not injected into app.Build, so its action log proves only independent reconciliation, not absence of an app-created client. Constructor/informer absence also follows from the disabled CLI branch and independent provider packaging (not goroutine counts).
- AC1.2: The provider service deploys, restarts, and reconciles independently of mecak8s. The default mecak8s chart has no dependency on the provider chart and installs no provider resource; enabling the client adapter requires explicit endpoint/profile configuration. The adapter needs no Pod/PVC/controller management RBAC.
  - verify: `TestChartRetainsCRDAndDoesNotGrantSecretAPI`, `TestKindExecutionProductionReplicaLifecycle`; inspection — default chart dependency/RBAC review and final Kind execution PENDING.
- AC1.3: Preflight validates configured profiles and endpoint/provider compatibility without allocating an environment. Allocation occurs only at an authorized session-binding operation; enabled configuration that is incomplete fails startup before allocation, and a configured but unavailable endpoint fails validation clearly with no local fallback.
  - verify: `TestExecutionPreflightRejectsIncompleteAndIncompatibleCLI`, `TestRemoteExecutionPreflightNeverAllocates`, `TestHandlerValidateIsReadOnlyAndEnsureIdempotent`, `TestGRPCReadinessAndMTLSAreMandatory`, `TestKindExecutionQualification`; offline CLI/composition and signed-handler validation cover rejection without allocation; final Kind run PENDING.
- AC1.4: Disabling the mecak8s client integration neither adopts nor deletes existing `ExecutionEnvironment` CRs, PVCs, or Pods and does not stop the separate provider's reconciliation for its existing allocations or other authorized clients.
  - verify: `TestDisabledCompositionPreservesAllocationsAndIndependentReconciliation`, `TestStartupDoesNotFenceLivePeerOperation`; the separate reconciler advances its retained allocation to Ready after an unrelated disabled Build/run/Close. Its fake clients are test-owned, not injected into app.Build; unchanged objects/actions are a limited independent-lifecycle oracle, not a client-construction guard or cluster restart run. `TestDisabledExecutionStartupDoesNotContactExecutionOrKubernetes` supplies the actual poisoned-endpoint CLI guard.

### Scenario 2 — One idempotent logical environment is allocated per binding

The controller owns a logical environment, PVC, and executor lifecycle rather than equating identity
with an ephemeral Pod, as decided in [draft ADR 0350](../adr/0350-native-kubernetes-execution.md).

**Acceptance:**
- AC2.1: Repeating the same authorized ensure request with the same final idempotency identity returns the same environment generation and does not create a second PVC or Pod.
  - verify: `TestHandlerValidateIsReadOnlyAndEnsureIdempotent`, `TestKindExecutionQualification` (final Kind execution PENDING).
- AC2.2: A stale/different profile, digest, storage request, generation, or owner fails with the final reviewed conflict/precondition error and never adopts or mutates an unrelated allocation.
  - verify: `TestStoreEnsureUsesStableLookupAndRejectsFingerprintDrift`, `TestReconcileRefusesForeignExistingPVCWithoutPersistingUID`.
- AC2.3: Only operator-configured profile references select digest-pinned images and storage; public clients/models cannot submit arbitrary images, Pod specs, paths, URLs, source credentials, or Kubernetes object names.
  - verify: `TestLoadProfilesStrictAndDigestPinned`, `TestReconcileCreatesTokenlessNonRootPodAndRetainedPVC`; inspection — public request/schema authority review PENDING.
- AC2.4: After transient PVC or Pod creation failure, including prolonged quota saturation, an existing allocation converges when the failure clears without unrelated CR/reference changes, restart, or manual reconcile. Retries remain rate-limited and continue while the object exists; wrong ownership and missing/mismatched authoritative UIDs never trigger adoption or replacement.
  - verify: `TestProvisioningWorkerConvergesWithoutAnotherEvent`, `TestProvisioningPreservesCreateAndConditionErrors`, `TestProvisioningRetriesNeverReplaceAuthoritativeResources`, `TestKindExecutionProductionQuotaSaturation` (Kind quota-restoration proof exists; final execution PENDING).

### Scenario 3 — Existing file tools and Shell are transparent but honest

The adapter preserves the immutable environment and independent read ledger
(`engine/tool/environment.go:9`; `engine/tool/ledger.go:1`) and the existing WorkspaceReader seam
(`engine/tool/tool.go:314`), under [draft ADR 0350](../adr/0350-native-kubernetes-execution.md).

**Acceptance:**
- AC3.1: A fresh environment can create, read, conditionally edit, replace, copy, move, remove, list, grep, and glob a small Go fixture with unchanged tool schemas and the existing read-before-edit/version behavior where that contract applies.
  - verify: `TestRemoteWorkspaceConformanceThroughSignedGRPC`, `TestRemoteFileToolsPreserveLedgerAndNamespaceContracts`, `TestKindExecutionQualification`; both shared Workspace conformance suites and all nine actual file tools run over loopback mTLS/signed gRPC with the real executor in a test-owned directory. Kind invokes the added remote-tool matrix, but final Kind execution remains PENDING.
- AC3.2: Read/Edit/Write preserve their applicable recorded-read and CreateFile/ReplaceFile CAS contracts; Copy/Move/Remove preserve their own positive conformance contracts and do not require an unrelated content read ledger/CAS precondition.
  - verify: `TestRemoteWorkspaceConformanceThroughSignedGRPC`, `TestRemoteFileToolsPreserveLedgerAndNamespaceContracts`; shared tests cover create/replace races, stale/zero versions, namespace positives and non-clobbering failures; actual tools cover unread/stale Edit/Write refusal and successful replacement. Namespace operations have no unrelated content-read precondition.
- AC3.3: File access is physically confined through symlink traversal rather than lexical checks alone, and every supported read/mutate/list/search/command request is authorized for the exact environment; stale, expired, revoked, or wrong-environment grants are denied. Detached status/cancel requests authorize before returning Unimplemented; this slice exposes no command-stream RPC.
  - verify: `TestEveryFileOperationRequiresExactCurrentGrant`, `TestFileOperationsNeverTraverseExternalSymlink`, `TestRemoteWorkspaceConformanceThroughSignedGRPC`, `TestSecurityManagerGuardsEveryRPCOnExistingConnection`, `TestRevokeEnvironmentFencesOldClaimWithoutChangingExecutionEpoch`, `TestCommandStatusIsUnimplementedOnlyAfterAuthorization`; every supported file operation has a valid positive control before wrong-client/owner/environment/revision/epoch/operation, expired, and revoked-nonce rejection at the shared handler gate. Executor tests cover symlink source/destination traversal and search leakage; durable generation revocation remains covered at the store boundary.
- AC3.4: Shell is bound to the same environment namespace as Workspace. Cancellation distinguishes requested cancellation, acknowledged complete process termination, and externally proven compute/storage fencing; namespace teardown includes detached descendants or fails closed. Output is capped and valid UTF-8 repaired, and Shell-created file changes are not falsely claimed to participate in Workspace CAS.
  - verify: `TestForegroundCommandReturnsReceiptAndTimeoutTerminatesDescendants`, `TestSuccessfulDetachedChildIsKilledBeforeTerminalReceipt`, `TestCancelledStoreOperationStaysActiveUntilBackendStopsThenFences`, `TestNativeShellCapsCombinedOutputAndRepairsUTF8`, `TestNativeShellWriteInvalidatesRecordedRead`, `TestProviderThroughRealGRPCSignedHandlerRefreshesAndReattachesExactly`, `TestServiceUsesRealMTLSProviderStoreAndReleasesOnlyAfterDrain`, `TestKindExecutionProductionHolderLossFencesActiveOperation`; the native helper executes oversized stdout/stderr and split malformed-byte output with combined-cap/truncation assertions, and writes after a recorded Read so actual Edit rejects the stale version without overwriting Shell's content. Private protobuf bytes may remain malformed; signed gRPC runner and model/client-event assertions cover text repair separately. Final Kind execution PENDING.

### Scenario 4 — Caller isolation and execution ownership survive failures

A session lease is session-only and its token is not consulted for writes
(`engine/port/lease.go:37-48`); [draft ADR 0350](../adr/0350-native-kubernetes-execution.md)
therefore requires independent environment execution fencing.

**Acceptance:**
- AC4.1: Ordinary creator client A cannot discover, attach, execute in, stream from, cancel, or retire creator client B's environment; hidden and absent allocations are indistinguishable at the public boundary. The explicit scoped administrative exception in AC4.4 permits only its named administrative RPCs.
  - verify: `TestClientScopedIntentListReturnsAttestedOwnerOnlyToOwningClient`, `TestScopedAdminAllRoutesOverMTLS`, `TestScopedAdminWithOwnerAttestationCannotUseAnotherCreatorsDataPlane`, `TestKindExecutionQualification` (final Kind execution PENDING).
- AC4.2: Every command is authorized for one immutable environment revision and a distinct transient execution-fence epoch; lease loss or grant revocation prevents new commands and cancels/fences active work without exposing capability credentials to the workload or another caller's output. Same-Pod placement alone is not a security boundary.
  - verify: `TestGrantRoundTripAndExactBindings`, `TestRevokeEnvironmentFencesOldClaimWithoutChangingExecutionEpoch`, `TestAcquireRunAndRevokeUseResourceVersionCAS`, `TestSecurityRotationPinnedReplicaReadConvergesBeforeDispatch`, `TestSecurityRotationMutableNamesPoisonSameGeneration`, `TestSecurityRotationImmutableNamesHandleProjectionSkew`, `TestReadCurrentAuthorityContentRetriesOnlyPreDispatchLag`, `TestReadCurrentAuthorityContentBoundAndCancellation`, `TestKindExecutionProductionSecurityRotation`, `TestKindExecutionProductionHolderLossFencesActiveOperation` (final Kind execution PENDING).
- AC4.3: Timeout, Pod deletion, controller restart, Lease timeout, or network partition alone never authorizes a replacement executor while an old writer may run; unknown fencing state fails closed and requires the reviewed manual/external fencing path.
  - verify: `TestExpiredOperationLeaseFencesWithoutClearingIdentity`, `TestRecoverMissingPodRemainsFenceUnknown`, `TestRecoveredTerminalProofCanStartExactReplacement`, `TestKindExecutionProductionHolderLossFencesActiveOperation` (final Kind execution PENDING); inspection — external fencing runbook review PENDING.
- AC4.4: A distinct `administrator:true` client with `administratorFor:[creatorURI]` can administer that creator's environment through all six administrative RPCs, including migration, revocation, and retained deletion, while preserving immutable creator identity and exact owner/revision/epoch/UID/operation checks. A second creator, wrong owner, stale identity, or replay with changed inputs is denied without existence disclosure. Self-admin compatibility remains explicit; no Files/Shell/attach/run/reference or MayAttestOwner authority is gained.
  - verify: `TestScopedAdminAllRoutesOverMTLS`, `TestScopedAdminCASRetryRechecksSubject`, `TestScopedAdminMigrationReceiptRetainsUIDPreconditions`, `TestMigrationCompletedCASReplayRequiresExactSourceSchema`, `TestMigrationReceiptExpiresAfterReconciledReplacement`, `TestScopedAdminDoesNotGrantAttestationOrDataPlane`, `TestScopedAdminWithOwnerAttestationCannotUseAnotherCreatorsDataPlane`, `TestKindExecutionProductionScopedAdministrator`; inspection — replacement-expiry regression is present in the implementation candidate; final Kind execution PENDING (Kind covers replacement/non-escalation, not all six routes).
- AC4.5: Strict `administratorFor` validation and canonical digest inclusion prevent ambiguous scope. Removal at a higher security generation takes effect on existing connections and receipt replay; an equal-generation scope edit fails closed. Old binaries reject the field and the documented quiesced upgrade preserves authority history.
  - verify: `TestAdministratorScopeManifestValidation`, `TestAdministratorScopeDigestIsNormalizedAndAuthorityBound`, `TestScopedAdminAllRoutesOverMTLS`, `TestScopedAdminEqualGenerationDriftFailsClosed`; inspection — old-binary rejection/quiesced-upgrade compatibility review PENDING.

### Scenario 5 — Restart preserves workspace while retirement is deliberate

Exact reattachment and safe lifecycle behavior follow [draft ADR 0350](../adr/0350-native-kubernetes-execution.md)
and remain separate from the session-only lease contract (`engine/port/lease.go:37-48`).

**Acceptance:**
- AC5.1: After mecak8s and controller restart, exact reattachment to the persisted environment revision restores the same PVC data and a correctly bound runner; an unavailable/mismatched generation fails with no default/local fallback.
  - verify: `TestProviderThroughRealGRPCSignedHandlerRefreshesAndReattachesExactly`, `TestKindExecutionQualification`, `TestKindExecutionProductionReplicaLifecycle` (final Kind restart/data-survival execution PENDING).
- AC5.2: Executor Pod replacement preserves the logical environment and PVC while current Pod UID may change without a spec `metadata.generation` change; `observedGeneration` records processed spec generation, and status records current Pod/PVC UID references without using them as durable session identity.
  - verify: `TestReplacementPersistsTerminalProofBeforeRemovingPodFinalizer`, `TestStaleReplacementObservationCannotOverwriteCompletion`, `TestKindExecutionProductionReplicaLifecycle`; inspection — observedGeneration/spec-generation distinction review and final Kind execution PENDING.
- AC5.3: The lifecycle matrix is explicit: removal of one reference retains the environment; removal of the last reference retains it by default; committed data retires only through explicit authorized retirement; a retiring environment rejects new bindings and successors. An unreadable store/reference is not an orphan and is retained.
  - verify: `TestReplacementQuiescesAndPendingReferenceBlocksRetirement`, `TestReferenceTransactionsRetainUnknownAndNeverChangeSource`, `TestKindExecutionProductionPendingDeleteOutageRecovery` (final Kind execution PENDING).
- AC5.4: A pending allocation whose session association might have committed is not garbage-collected merely after TTL; collection waits for conclusive reconciliation that publication is absent. Explicit retirement of a live/shared environment is refused while any live reference exists unless a separately reviewed retire/quiesce policy authorizes it.
  - verify: `TestAmbiguousCommitIsRetainedAndNeverAbortedByCleanup`, `TestReplacementQuiescesAndPendingReferenceBlocksRetirement`, `TestReferenceIntentReconciliationIsOwnerAndRefExact`.
- AC5.5: Default chart uninstall retains runtime CRs/PVCs and preserves workload network isolation and security authority history while executors survive. Retained security/capacity ledgers and required configuration outlive the provider Deployment. Deletion of CRDs with live resources is destructive and unsupported; no destructive hook or force cleanup is provided. Owner references or retention annotation strings alone are not lifecycle proof.
  - verify: `TestChartHasNoDeletionHook`, `TestChartRetainedLifetime`, `TestKindExecutionProductionHelmLifetime` (offline rendering is partial; actual Helm lifecycle execution PENDING).
- AC5.6: PVC persistence is not documented as node-disaster recovery; kind host-local storage proves restart survival only, and a dead or unobservable node is outside that positive guarantee.
  - verify: inspection — human review of deployment documentation; `task docs` checks links and structure after authorized tracked changes
- AC5.7: Real Helm install/compatible upgrade/uninstall/reinstall of the same release and namespace preserves exact runtime identity, PVC contents, NetworkPolicy enforcement during provider absence, security high-water/key history, and capacity reservations. Safe adoption validates ownership; missing ledgers with retained allocations, foreign resources, stale authority, arbitrary downgrade, or incompatible profile/schema fail closed without overwrite. CRD upgrades remain manual.
  - verify: `TestChartRetainedLifetime`, `TestSecurityLedgerRejectsKeyVersionRollbackAcrossRestart`, `TestKindExecutionProductionHelmLifetime` (real Helm lifecycle test exists; final execution PENDING).

### Scenario 6 — Remote catalog and project-source boundaries are explicit

The first slice avoids claiming that local project trust applies remotely: current admission is rooted
in local composition (`internal/app/project_ingestion.go:20`) and AGENTS discovery reads Workspace
(`engine/prompt/builder.go:337`). The boundary is recorded in [draft ADR 0350](../adr/0350-native-kubernetes-execution.md).

**Acceptance:**
- AC6.1: A remote execution session receives operator-global instructions and only its explicitly supported catalog; project AGENTS/rules/skills and Git source ingestion are off unless a later reviewed source/trust contract enables them. Existing operator-global and explicit-file permission policy remains effective, including configured Ask/Deny under auto; these authorization rules are distinct from prompt rules. Project permission sources are excluded, without a new trust decision.
  - verify: `TestRemoteExecutionRealFactoryCarriesPostureAndAttenuatedCatalog`, `TestRemoteExecutionPreservesOperatorPermissionsUnderAuto`; real factory invocation plants host and remote AGENTS, host rules/skills/commands, invokes the forbidden project skill, and proves operator-global prompt rules/skills survive. A test-owned Git repository yields a nonempty snapshot in the local factory request and none in the remote request. Under auto, both conventional global and explicit-file Deny/configured Ask survive remote selection, while project Deny/Ask are excluded and allowed Shell controls execute only on the in-memory runner.
- AC6.2: Schedules remain excluded. Background commands, isolated Subagent/Parallel/Team delegation, SkillDraft, Git/source operations, and unsupported successor modes are rejected with named capability/precondition errors rather than running locally or silently omitting work; `no-fs` remains unchanged.
  - verify: `TestRemoteExecutionRealFactoryCarriesPostureAndAttenuatedCatalog`, `TestRemoteDeploymentNoFSUsesLocalAttenuationWithoutProviderCall`, `TestExecutionPreflightRejectsIncompleteAndIncompatibleCLI`, `TestServiceUsesRealMTLSProviderStoreAndReleasesOnlyAfterDrain`, `TestCommandStatusIsUnimplementedOnlyAfterAuthorization`; real factory tests invoke Subagent/Parallel/Team/SkillDraft/Schedule and require model-visible errors, reject worktree discovery, and withhold command listing/expansion instead of opening host paths. The native mTLS/store integration rejects background Shell with its named runner-capability error and no executor dispatch, while foreground Shell succeeds. Actual Service Clear/Fork calls with a nonempty unsupported worktree selector return `ErrInvalidPlacementSelection`, publish no destination, and preserve source ref/history and provider references. A direct native Bind with a valid binding ID distinguishes the unsupported-selector gate from missing binding identity or malformed Service input.
- AC6.3: Clear/Fork use environment exclusion and reference reservation/publication while preserving same-remote-`EnvironmentRef` successor semantics and the source on publication failure. Clear/Fork are not workspace cloning; no public Harness field is added and no model chooses an environment/profile.
  - verify: `TestReferenceTransactionsRetainUnknownAndNeverChangeSource`, `TestKindExecutionProductionClearForkLifecycle` (final Kind execution PENDING); inspection — public API/profile authority review PENDING.

### Scenario 7 — kind proves an offline coding flow; live qualification is separate

The deterministic correctness gate is a focused mock-only task, following [draft ADR 0350](../adr/0350-native-kubernetes-execution.md).
The stack additionally requires the explicitly invoked native-provider OpenRouter coding qualification
recorded in Human decisions; it is completion evidence, not an always-on CI dependency. The broad mecak8s
live suite is not native-provider proof. The focused mock task must not inherit ambient provider credentials.

**Acceptance:**
- AC7.1: `task e2e:k8s:execution` and the enforcing-CNI `task e2e:k8s:execution:production` run on the supported generic toolchain, including CI, with a unique owned kind cluster, a scratch kubeconfig, and explicit `--kubeconfig`/context on every command. They deploy the separate chart plus explicitly connected mecak8s and use only the deterministic mock provider. The final production-profile run uses Calico and exercises negative network isolation, rotation/revocation, replica/lifecycle recovery, holder loss, quota recovery, pending delete, Clear/Fork, migration, sanitized artifacts, and the amendment regressions.
  - verify: `TestLegacyFixtureWaitsForQuotaAccountingBeforeCreate`, `TestKindExecutionQualification`, `TestKindExecutionProductionNetworkPolicyEnforced`, `TestKindExecutionProductionSecurityRotation`, `TestKindExecutionProductionReplicaLifecycle`, `TestKindExecutionProductionHolderLossFencesActiveOperation`, `TestKindExecutionProductionQuotaSaturation`, `TestKindExecutionProductionPendingDeleteOutageRecovery`, `TestKindExecutionProductionClearForkLifecycle`, `TestKindExecutionProductionCompatiblePrototypeMigration`, `TestKindExecutionProductionScopedAdministrator`, `TestKindExecutionProductionHelmLifetime` (names exist; final production Kind+Calico run PENDING).
- AC7.2: The mock flow seeds a small Go fixture through file tools, asks the harness to implement a function/tests, runs `go test` through bound Shell, and verifies the persisted artifact and restart/reattach behavior.
  - verify: `TestKindExecutionQualification` (coding/artifact/restart assertions exist; final Kind execution PENDING).
- AC7.3: Failure artifacts are bounded and sanitized; cleanup targets only the recorded owned cluster. Destructive live cleanup requires confirmation and never mutates ambient kubeconfig/context.
  - verify: `TestKindExecutionProductionFailureArtifactBoundary`, `TestLifetimeOutputRejectsOversizedArtifacts`; inspection — owned-cluster cleanup/context script review and final Kind execution PENDING.
- AC7.4: `task e2e:k8s:execution:live` is a separate explicit native-provider OpenRouter coding qualification, required to complete this stack after final deterministic qualification. It proves model-driven file/tool execution, independently verified persisted workspace artifacts, and a successful bound `go test` command. It uses verified nonsecret endpoint/protocol/model configuration in an authenticated operator-controlled cluster, then restores mock configuration and deletes only the run-owned Secret by recorded UID. It is not an always-on live CI dependency and claims no hard dollar cap from token/run limits.
  - verify: `TestKindExecutionLiveQualification`; inspection — record successful native-provider live evidence for the final implementation candidate (PENDING, not satisfied by generic mecak8s live CI)
- AC7.5: A trusted runtime-only loader may consume the approved credential directly from its protected file channel and create a narrowly scoped HARNESS-only Secret through a protected channel before child processes or tools can inherit it. The agent never accesses the file or Secret; failures are redacted, and the credential never appears in model/tool output, argv, Helm values, disk manifests, git, logs, artifacts, or the execution workload. Mock tests do not access it.
  - verify: `TestLoadCredentialAndRedaction`, `TestDeletePinsReceiptUIDAndLeavesReplacement`, `TestSecretReferencesFindEveryCredentialEscape`, `TestExpectedHarnessCredentialReferenceIsExact`, `TestKindExecutionLiveQualification`; inspection — offline synthetic-credential tests do not establish live handling; final authorized native live evidence PENDING.

## Implementation proof map

Source/test locations in this table and the `verify:` lines refer to implementation commit
`773a203df5de9b27612d190a774a857590702cbd`, not files supplied by this plan branch.
This is the intended candidate for the normal merge of this proof-map update into implementation.
The labels name actual tests; source resolution alone is not successful execution or complete
behavioral coverage. Offline boundaries and remaining partial proof are stated per AC;
PENDING runtime evidence still requires final-candidate execution.
The migration-receipt replacement-expiry regression `TestMigrationReceiptExpiresAfterReconciledReplacement`
is already present in that candidate's `internal/adapter/executioncontroller/production_lifecycle_test.go`.
Run `actrace --strict --plan <absolute-path-to-this-plan>` from the implementation worktree
for source resolution, then rerun after its normal plan merge. Draft status is intentional:
strict mode gates landed plans only, so inspect the native report for missing proofs as well.
A row naming a unit/fake-client test establishes only that boundary; it does not prove the entire AC.

**Prior offline verification:** implementation `0df361480b2b9c1b9a02ca5c7f55362d0e257966`
passed targeted race
packages (execution client/controller/executor/protocol, mecak8s, and remote composition), the
build-tagged quota test, compilation of the Kind qualification and fixture packages without running
Kind, full `task lint`, full `task test` (including race and standalone module checks), `task build`,
`task docs`, `task site:build`, and the offline demo. Local logs are under the implementation's ignored
`.scratch/orchestrate/native-kubernetes-execution/coverage-*.log`. Source conformance does not qualify
Kubernetes admission, CNI enforcement, or live-model behavior; those remain the runtime gates below.

**Bounded follow-up verification:** `773a203df` preserves operator permission sources through the
existing workspace-pinned resolver with no workspace. The real-factory auto-posture regression first
failed with both Deny/Ask commands executed and no configured ask, for global and explicit-file sources;
it passes after the fix. Local/no-FS policy selection and model configuration are unchanged. Default,
selector, and client-bound remote factory paths share `remoteSessionConfiguration` before catalog and
engine assembly. The added AC3.4/AC6 proofs pass targeted race tests; full lint, docs, site build,
Taskfile build, and offline demo pass. Logs are under the implementation's ignored
`.scratch/orchestrate/native-kubernetes-execution/final-bounded-*.log`. The first full-suite attempt
hit the unrelated status-line clock-refresh timeout; its isolated race rerun and one final full
`task test` rerun passed, including all module/standalone checks.
These are offline results, not new-head CI, live qualification, or approval.

**Observed qualification:** the [production Kind+Calico job](https://github.com/stacklok/mecatl/actions/runs/35589357385/job/106299991747)
completed **SUCCESS** at historical implementation `deaf1c3d1dfe7ea92afc8fe826f1bc613080f219`. This includes network isolation,
rotation/revocation, replicas/lifecycle, holder loss, quota saturation, pending-delete recovery,
Clear/Fork, compatible prototype migration, and artifact isolation. Historical local keyring/toolchain
failures are not active blockers for that run. This is baseline evidence, not final-candidate evidence
for the new scoped-admin, continuing-retry, or Helm uninstall/reinstall requirements.

Draft CI skipped race lanes; non-race success is not a race result. The newer
[production Kind+Calico job](https://github.com/stacklok/mecatl/actions/runs/35642635684/job/106475375010)
at `9bb4d89b8aa497c9d2cd6aa4a94ab9262a913364` failed **before qualification tests** while
seeding `legacy-migration`: ResourceQuota accounting for
`count/executionenvironments.execution.mecatl.dev` was still unknown. Provider Deployment
readiness was not quota readiness. This supersedes a test-race diagnosis for that job.
The implementation adds a two-minute, context-aware quota-accounting wait with delayed-status,
timeout, cancellation, and forbidden-response controls in
`TestLegacyFixtureWaitsForQuotaAccountingBeforeCreate`. It does not disable quota or retry
arbitrary forbidden creates. Those offline controls and tagged compilation are not a rerun
of the production job.

**Latest reported production context:** the operator supplied run
[35650128625, job 106500190798](https://github.com/stacklok/mecatl/actions/runs/35650128625/job/106500190798)
at `f7919f6c`: 10/11 production tests passed, including scoped administration, Helm lifecycle/quota,
and general file tools. Security rotation failed after about 250 seconds on the final-authority Files
READ immediately after successful AcquireRun (`production_qualification_test.go`, former line 279).
This report was not independently re-fetched during the offline repair. It supersedes the earlier
setup failure as the current reported runtime blocker, not as a final-candidate success.

**Deterministic repair evidence:** implementation `96e16e926` adds
`internal/adapter/executioncontroller/security_rotation_test.go`:
`TestSecurityRotationPinnedReplicaReadConvergesBeforeDispatch` proves peer advancement → retryable
`not_ready` with zero Files dispatch → reload → the same valid bridge-k2 claim succeeds.
`TestSecurityRotationMutableNamesPoisonSameGeneration` proves that a new manifest with old
`tls.crt`/`tls.key`/`clients.pem` can publish a mixed digest that the later complete bundle cannot replace
at that generation. This is a reproduced defect, **not proof that it caused the CI failure**.
`TestSecurityRotationImmutableNamesHandleProjectionSkew` proves both projection orders recover using
new immutable filenames while retaining old entries. All three pass offline with `-race`, without
sleeps or a cluster. The native exact-cap stdout/stderr/combined repair-expansion regression first
failed with `Truncated=false`, then passed with `-race` after tracking clipping after UTF-8 repair;
earlier bounded-writer truncation remains ORed into the result. Implementation `faacfcde7` stages and
retains immutable fixture names, observes the requested ledger generation, and uses a bounded read-only
barrier (30 seconds, at most 60 calls) that admits only structured retryable `not_ready`.
`TestReadCurrentAuthorityContentRetriesOnlyPreDispatchLag` and
`TestReadCurrentAuthorityContentBoundAndCancellation` pin success, immediate wrong-content/nonretryable/
wrong-code failure, persistent lag, and cancellation without sleeps. The rotation generator's
`TestForwardRestoreTrustBundleSupportsFixtureClient` pins the actual manifest filename references;
its new filename assertions failed before the fixture fix and pass afterward. Tagged race tests and
compilation pass. Old-client rejection uses the current successful final claim; the revoked-k1 probe
reuses Helm's re-sign/current-claim positive controls. No new mutating retry was added; the existing
bounded AcquireRun helper is unchanged. Full untagged `task lint` and one full `task test` invocation
(including module race and standalone checks), docs/checker, site build, strict
native-plan trace (32 ACs, zero missing; draft/report-only), targeted four-package race tests, and the
offline demo pass. Tagged whole-fixture lint exposes unrelated pre-existing findings; the changed-line
gate against baseline `2d32864cee8378939cb55137e1ab369e70a18349` passes with zero issues.
These results do not replace final runtime qualification; no live inference or cluster run is claimed.

[PR #1728](https://github.com/stacklok/mecatl/pull/1728), commit
`6501b5924`, is already integrated in the implementation ancestry. Its generic live-compaction
repair is not native-provider qualification. Final-candidate native-provider live and amended
production Kind qualification remain **PENDING**. Source resolution below does not assert
new-head CI, final live execution, or human contract/panel approval.

| AC | Existing implementation proof at the reference commit; remaining evidence |
|---|---|
| AC1.1 | `cmd/mecak8s/execution_startup_test.go`: `TestDisabledExecutionStartupDoesNotContactExecutionOrKubernetes` exercises CLI `run()` through composition to listener setup with poison TLS paths and no test-endpoint calls. `internal/adapter/executioncontroller/disabled_composition_test.go` exercises independent reconciliation beside disabled Build/run/Close; its fake clients are not injected into app.Build, so their empty action log does not detect app-created clients. Constructor/informer absence is also inspected at the CLI enablement branch; no flaky goroutine census. |
| AC1.2 | `deploy/helm/mecatl-execution/chart_test.go`: `TestChartRetainsCRDAndDoesNotGrantSecretAPI`; production job separately deploys provider and client. |
| AC1.3 | `cmd/mecak8s/execution_startup_test.go`: `TestExecutionPreflightRejectsIncompleteAndIncompatibleCLI`; `internal/app/remote_execution_test.go`: `TestRemoteExecutionPreflightNeverAllocates`; handler read-only validation and mTLS/readiness tests cover the transport. |
| AC1.4 | `internal/adapter/executioncontroller/disabled_composition_test.go`: `TestDisabledCompositionPreservesAllocationsAndIndependentReconciliation` observes independent reconciliation after disabled Build/run/Close. Retained objects and fake action counts are only a limited lifecycle oracle because the clients are not injected into app.Build; the poisoned-endpoint CLI test in AC1.1 guards startup contact. `controller_test.go`: peer-restart test remains complementary. |
| AC2.1 | `internal/adapter/executioncontroller/handler_test.go`: `TestHandlerValidateIsReadOnlyAndEnsureIdempotent`; `e2e/k8s_execution/qualification_test.go`: `TestKindExecutionQualification`. |
| AC2.2 | `internal/adapter/executioncontroller/store_test.go`: `TestStoreEnsureUsesStableLookupAndRejectsFingerprintDrift`; `controller_test.go`: `TestReconcileRefusesForeignExistingPVCWithoutPersistingUID`. |
| AC2.3 | `internal/adapter/executioncontroller/profiles_test.go`: `TestLoadProfilesStrictAndDigestPinned`; production `TestKindExecutionProductionQuotaSaturation`. |
| AC2.4 | `internal/adapter/executioncontroller/provisioning_retry_test.go`: `TestProvisioningWorkerConvergesWithoutAnotherEvent`, `TestProvisioningPreservesCreateAndConditionErrors`, `TestProvisioningRetriesNeverReplaceAuthoritativeResources` exercise PVC and Pod retry/confinement paths. Production `TestKindExecutionProductionQuotaSaturation` restores quota without a new reference event; final runtime evidence PENDING. |
| AC3.1 | `internal/adapter/executionclient/files_conformance_test.go`: `TestRemoteWorkspaceConformanceThroughSignedGRPC` runs both existing shared suites; `TestRemoteFileToolsPreserveLedgerAndNamespaceContracts` invokes all nine tool bodies against the signed handler and real executor. `e2e/k8s_execution/file_tools_test.go` adds the tool matrix called by Kind qualification; runtime execution PENDING. |
| AC3.2 | Same gRPC suites cover concurrent create/CAS, missing/stale/zero versions, non-clobbering namespace operations, and actual Read-ledger behavior. The suite exposed and now guards missing-file replacement precedence and typed non-empty-directory removal errors. |
| AC3.3 | `internal/adapter/executioncontroller/file_authorization_test.go`: every supported file operation has positive and negative signed-grant controls; `internal/executionexecutor/confinement_test.go`: physical symlink traversal/search checks. Existing security manager, protocol, durable revocation, and detached-control tests remain complementary. |
| AC3.4 | `internal/executionexecutor/executor_test.go`: `TestNativeShellCapsCombinedOutputAndRepairsUTF8`, `TestNativeShellWriteInvalidatesRecordedRead` execute the native helper for combined output bounds, malformed bytes, and actual Shell-write→Edit version refusal. `internal/adapter/executionclient/client_test.go`: `TestProviderThroughRealGRPCSignedHandlerRefreshesAndReattachesExactly` checks runner text repair after private protobuf bytes; `internal/adapter/executionclient/service_integration_test.go`: `TestServiceUsesRealMTLSProviderStoreAndReleasesOnlyAfterDrain` checks identical valid UTF-8 in client events and model history. `internal/adapter/executioncontroller/store_test.go`: `TestCancelledStoreOperationStaysActiveUntilBackendStopsThenFences`; production `TestKindExecutionProductionHolderLossFencesActiveOperation` remains runtime qualification. |
| AC4.1 | `internal/adapter/executioncontroller/store_lifecycle_test.go`: `TestClientScopedIntentListReturnsAttestedOwnerOnlyToOwningClient`; Kind isolation and pending-delete tests; scoped-admin non-escalation is exercised by the AC4.4 tests. |
| AC4.2 | `internal/adapter/executioncontroller/store_revoke_test.go`: `TestRevokeEnvironmentFencesOldClaimWithoutChangingExecutionEpoch`; `store_concurrency_test.go`: `TestAcquireRunAndRevokeUseResourceVersionCAS`; the three deterministic rotation tests listed above at repair commit `96e16e926`. Latest reported production rotation failed; final runtime success PENDING. |
| AC4.3 | `internal/adapter/executioncontroller/production_lifecycle_test.go`: `TestExpiredOperationLeaseFencesWithoutClearingIdentity`, `TestRecoverMissingPodRemainsFenceUnknown`, `TestRecoveredTerminalProofCanStartExactReplacement`; production holder loss. |
| AC4.4 | `internal/adapter/executioncontroller/admin_scope_test.go`: `TestScopedAdminAllRoutesOverMTLS`, `TestScopedAdminCASRetryRechecksSubject`, `TestScopedAdminMigrationReceiptRetainsUIDPreconditions`, `TestMigrationCompletedCASReplayRequiresExactSourceSchema`, `TestMigrationReceiptExpiresAfterReconciledReplacement`, `TestScopedAdminDoesNotGrantAttestationOrDataPlane`, `TestScopedAdminWithOwnerAttestationCannotUseAnotherCreatorsDataPlane`. Replacement-expiry test is in `internal/adapter/executioncontroller/production_lifecycle_test.go` in the pinned implementation candidate. `e2e/k8s_execution/ab_production_admin_test.go`: `TestKindExecutionProductionScopedAdministrator` exists (replacement/non-escalation, not all six routes); final Kind evidence PENDING. |
| AC4.5 | `internal/adapter/executioncontroller/admin_scope_security_test.go`: `TestAdministratorScopeManifestValidation`, `TestAdministratorScopeDigestIsNormalizedAndAuthorityBound`; `internal/adapter/executioncontroller/admin_scope_test.go`: all-routes scope removal plus `TestScopedAdminEqualGenerationDriftFailsClosed`. Compatibility/upgrade human review PENDING. |
| AC5.1 | `internal/adapter/executionclient/client_test.go`: exact reattachment test above; production `TestKindExecutionProductionReplicaLifecycle` and security rotation. |
| AC5.2 | `internal/adapter/executioncontroller/production_lifecycle_test.go`: `TestReplacementPersistsTerminalProofBeforeRemovingPodFinalizer`; production replica lifecycle. |
| AC5.3 | `internal/adapter/executioncontroller/production_lifecycle_test.go`: `TestReplacementQuiescesAndPendingReferenceBlocksRetirement`. |
| AC5.4 | `internal/adapter/executionclient/reference_ambiguity_test.go`: `TestAmbiguousCommitIsRetainedAndNeverAbortedByCleanup`; production `TestKindExecutionProductionPendingDeleteOutageRecovery`. |
| AC5.5, AC5.7 | `deploy/helm/mecatl-execution/chart_test.go`: `TestChartHasNoDeletionHook`; `deploy/helm/mecatl-execution/lifetime_test.go`: `TestChartRetainedLifetime` exercises lookup/adoption, missing history, foreign ownership, profile drift, and retained policies/ledgers offline. `e2e/k8s_execution/zz_production_helm_lifetime_test.go`: `TestKindExecutionProductionHelmLifetime` exercises actual upgrade/uninstall/reinstall and enforcement; final runtime evidence PENDING. |
| AC5.6 | Deployment-documentation inspection and docs gates required on the final implementation; Kind is restart proof only. |
| AC6.1–AC6.2 | `internal/app/remote_execution_test.go`: `TestRemoteExecutionRealFactoryCarriesPostureAndAttenuatedCatalog` includes a nonempty LOCAL Git snapshot control and REMOTE exclusion, poison-source and operator-global prompt controls, forbidden-tool errors and command/worktree discovery. `TestRemoteExecutionPreservesOperatorPermissionsUnderAuto` exercises global/explicit-file Deny and configured Ask, excluding project permissions, with allowed in-memory Shell controls. `TestRemoteDeploymentNoFSUsesLocalAttenuationWithoutProviderCall` retains the no-FS check. `internal/adapter/executionclient/service_integration_test.go`: `TestServiceUsesRealMTLSProviderStoreAndReleasesOnlyAfterDrain` rejects background Shell and actual Clear/Fork unsupported-selector calls with named errors, no destination publication, and unchanged source ref/history and provider references. |
| AC6.3 | `internal/adapter/executioncontroller/store_lifecycle_test.go`: `TestReferenceTransactionsRetainUnknownAndNeverChangeSource`; production `TestKindExecutionProductionClearForkLifecycle`. |
| AC7.1 | `.github/workflows/k8s-e2e.yml` runs `task e2e:k8s:execution:production`; the historical reference job succeeded. The latest operator-reported job at `f7919f6c` passed 10/11 production tests and failed rotation's final read; it progressed past the earlier quota setup failure. Offline rotation evidence and read-only barrier controls are listed above. `internal/adapter/executioncontroller/legacy_fixture_kind_test.go` covers the bounded quota-accounting wait. Final amended-candidate run **PENDING**. |
| AC7.2 | `e2e/k8s_execution/qualification_test.go`: `TestKindExecutionQualification`, run by the successful production profile. |
| AC7.3 | Production `TestKindExecutionProductionFailureArtifactBoundary`; workflow bounded uploads and ownership-scoped cleanup. |
| AC7.4 | `e2e/k8s_execution/live_qualification_test.go`: `TestKindExecutionLiveQualification` exists; successful final native-provider OpenRouter evidence **PENDING**. |
| AC7.5 | `e2e/k8s_execution/fixture/credentialloader/main_test.go`: `TestLoadCredentialAndRedaction`, `TestDeletePinsReceiptUIDAndLeavesReplacement`; these offline tests do not substitute for live execution. |

Production test names without another path above reside in the implementation's
`e2e/k8s_execution/production_qualification_test.go`. Do not import implementation files into this
plan worktree to make citations or trace appear complete.

## Out of scope

| Item | Defer-to | Decision |
|---|---|---|
| GitHub cloning, private-repository credentials, and authoritative remote project trust/AGENTS/rules/skills | Separate source-ingestion architecture plan | Blank/PVC workspace and tool-seeded fixture are sufficient for slice 1. |
| Agent Sandbox integration | Not planned for this capability | Human selected a purpose-built controller. |
| Public Harness placement/profile/image/path fields | Later architecture only if justified | First slice keeps the public API unchanged and server-owned. |
| Generic capabilities framework | Later, only with multiple proven consumers | Use narrow optional interfaces/catalog attenuation rather than speculative abstraction. |
| Schedules and remote delegation/fork support | Separate reviewed expansion | First slice rejects unsupported paths without local fallback. |
| Automated force takeover under uncertain fencing | Not permitted | Unknown old executor state is fail-closed/manual until a reviewed external fencing proof exists. |
| Node-disaster PVC recovery and production multi-zone storage | Production storage design | kind host-local PVC is only a restart-survival proof. |
| Provider credential discovery, arbitrary endpoint forwarding, or live inference beyond the approved native qualification | Separate explicit authorization | This stack requires native OpenRouter coding evidence within the protected runtime-loader boundary; planning/mock tests never access credentials. |
| Depending on or copying PR #580's transport | Independent work | Share behavioral conformance where useful; do not inherit unqualified cancellation/auth protocol. |

## Definition of done

1. Human API/schema/security review approves the authored interface and amendment requirements, and the release maturity designation is explicit. Both PRs remain draft with no approved baseline until human contract approval; authorized pre-merge development does not waive that gate.
2. The final implementation candidate passes `task lint`, full offline `task test` including race and engine-standalone coverage, `task build`, `task api:check`, `task docs`, `task site:build`, and `go run ./cmd/mecademo`. A skipped draft race lane is not a pass. Generated contracts/references must be fresh; no arbitrary prose-pinning tests.
3. `task ac-trace-strict` resolves every final `verify:` proof, including AC2.4, AC4.4–AC4.5, and AC5.7. Resolve the MISSING/partial behavioral coverage noted above; named-test resolution alone is not AC completion.
4. Fresh deterministic Kind+Calico production qualification passes at the final candidate, including the expanded scope/retry/Helm lifecycle matrix. Then record successful stack-specific native-provider OpenRouter coding qualification under AC7.4–AC7.5. Neither historical deterministic success nor generic mecak8s live CI substitutes for those final results.
5. Complete independent Spec/Standards/Test adequacy/Domain panel review and resolve merge-blocking findings. Update the owning `user-docs/building/deployment/mecak8s.md` and implementation's `user-docs/features/execution-environments.md`, plus resource/rehydration inventories, in the same implementation stack; keep this plan branch documentation-only.
6. The existing implementation PR targets `plan/native-kubernetes-execution`, links plan PR #1579 and its exact working commit, reports conformance/amendments and final evidence. Human alone approves and merges; no new PR, automatic merge, or unsupported landed claim.

## Deferred decisions and known risks

- Exact specifications are authored, but human API/schema/security approval and the release maturity designation remain open. Draft development is authorized; promotion/merge is not. Do not turn `v1alpha1` into an unreviewed product “alpha” decision.
- A PVC can preserve files while a stale command still mutates them. Session leasing does not fence command execution; missing old-executor evidence remains a fail-closed manual recovery boundary.
- Only an enforcing CNI supplies negative NetworkPolicy proof. Retaining storage while uninstall removes isolation or authority history is unsafe; the Helm lifecycle amendment is a completion requirement, not deferred polish.
- Every outlives-a-call resource, including retained security/capacity ledgers, reload state, informer/cache, connection pools, claims, and lifecycle/reference reconciliation, needs an accurate resource/rehydration inventory in `docs/adr/0027-cloud-native.md` on the implementation branch.
- Scope, retry, Helm lifecycle, disabled startup, remote filesystem conformance, and source-admission behavioral tests exist in the pinned implementation source. Final production Kind/native live evidence and human reviews remain pending. The requested offline output/CAS and Git/unsupported-successor proofs in AC3.4 and AC6.1–AC6.2 are present; they do not replace final runtime qualification. Source/proof locations absent on the plan branch stay explicitly implementation-scoped; do not add implementation code to satisfy plan-branch gates.
