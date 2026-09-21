# ADR 0350 — Optional native Kubernetes execution environments

- Status: Draft
- Date: 2026-09-15; draft amended 2026-09-21
- Scope: independently deployed execution environment provider service; mecak8s is an optional client; controller/executor, logical environment, PVC, Pod, authorization, fencing, and kind qualification boundaries
- Supersedes: none

## Context

Mecak8s externalizes session state to Redis and uses Kubernetes Leases for session-level
single-writer ownership, but it does not provide a production remote execution environment.
The current remote environment adapter is a fake (`internal/adapter/remoteenv/remoteenv.go:1`).
`tool.Environment` already binds immutable identity, Workspace, an independent read ledger, and an
optional CommandRunner (`engine/tool/environment.go:9`), so a Kubernetes implementation should
meet that runtime contract without widening the engine API.

A session lease does not fence an already-running remote command: its token is intentionally not
consulted on session writes (`engine/port/lease.go:37-48`). Kubernetes Pod replacement, network
partition, and controller restart therefore introduce a separate execution-ownership problem.
Likewise, startup placement validation currently reaches `Bind`
(`docs/design/IMPLEMENTATION-NOTES.md:6549-6560`), but using an allocating operation as preflight
would leak PVCs and Pods.

The human authorized the direction and defaults: a purpose-built execution environment provider in this
monorepo, separate from mecak8s, with its own binaries, images, chart lifecycle, optional client integration,
and deterministic kind qualification followed by native-provider OpenRouter coding evidence. The target
is an operator-controlled first production slice with explicit limits; release maturity remains undecided.

The existing plan PR #1579 and implementation PR #1614 form a human-authorized **draft** stack. The
[acceptance plan](../acceptance/native-kubernetes-execution.md) now authors the exact proposed contract,
reconciled against implementation `deaf1c3d1dfe7ea92afc8fe826f1bc613080f219`. API, schema, and security
review approvals remain unchecked; neither authored specs nor draft code is a merged approved baseline.
This amendment also authorizes development of scoped administration, continuing provisioning retries,
and retained-resource Helm lifecycle safeguards on those existing PRs. Human alone merges.

## Proposed decision within the authorized draft stack

This ADR records the approved direction: a separate execution environment provider in this monorepo;
it does **not supersede ADR 0048**. ADR 0048 governs storage-free mecak8s. Mecak8s remains a client of
the provider, keeps session state in its existing stores, and neither mounts provider workspace PVCs nor
owns executor/controller lifecycle. The provider owns its CRDs, Pods, PVCs, authorization,
reconciliation, binaries, images, and chart lifecycle. Its API is not tied to a mecak8s process or
release; other authorized Mecatl compositions can use an adapter without embedding the controller.
Provider allocation handles remain separate from harness session IDs. The
[exact interface contract](../acceptance/native-kubernetes-execution.md#interface-contract) is authored
for review in the acceptance plan; this ADR records the durable boundaries rather than duplicating the spec.

### 1. Own a narrow controller/executor boundary

Ship a dedicated controller and executor integration rather than Agent Sandbox. The controller
reconciles a namespaced `execution.mecatl.dev/v1alpha1` `ExecutionEnvironment`, a persistent workspace
PVC, and a replaceable executor Pod. Spec and status use explicit schema version 2. The Kubernetes
API name does not settle the release maturity label. Durable identity is the logical environment plus
immutable `EnvironmentRef.Revision`; run epoch, grant generation, Pod UID, and PVC UID have distinct roles.
`observedGeneration` records processed spec generation, not executor identity.

Only operator-configured, digest-pinned profiles select image, storage, resource/operation bounds, and
RuntimeClass. Public clients/models cannot submit arbitrary Pod specs, images, paths, URLs, credentials,
or Kubernetes names. Creator `clientHash`, owner identity, allocation fingerprint, and revision remain
immutable. The [schema and transaction contract](../acceptance/native-kubernetes-execution.md#reference-transaction-schema-v2-and-fencing)
records exact fields, receipts, conditions, and migration behavior.

Transient PVC/Pod create errors and quota saturation must recover through continuing rate-limited
reconciliation while the CR exists. Quota restoration alone must be sufficient; no unrelated reference
change, restart, or manual reconcile is required. A finite retry count or successful condition write
must not strand provisioning. Wrong ownership or missing/mismatched authoritative UIDs still fail
closed without replacement, regardless of retry scheduling.

### 2. Preserve the core environment and tool contracts

Keep `engine/tool` and public Harness APIs unchanged. A host-side adapter maps an exact persisted
`session.EnvironmentRef` to a complete remote `tool.Environment`. Existing filesystem tool schemas,
read-before-edit ledger, CreateFile/ReplaceFile version protocol, and bound Shell semantics remain.
Shell writes retain their documented ability to bypass Workspace CAS
(`engine/tool/tool.go:353-424`); the product must disclose that honestly rather than promising a
stronger remote filesystem transaction.

The private typed `mecatl.execution.v1.ExecutionProviderService` uses mTLS, with no REST fallback.
`ValidateProfile` is side-effect-free; Ensure reserves a reference, exact Attach reattaches, and an
independent Acquire/Renew/ReleaseRun claim gates operations. Reference and administrative lifecycle
RPCs carry exact identities and replay preconditions. `Files` transports bounded bytes and opaque
versions. `StartCommand` is foreground unary; authorized detached CommandStatus/CancelCommand calls
return Unimplemented. Do not advertise a command stream or background service absent from this slice.
The [RPC and host contract](../acceptance/native-kubernetes-execution.md#private-rpc-and-host-contract)
records exact messages, error classifications, and host-only placement/reference/run interfaces.

### 3. Make installation and connection explicitly optional

The provider has its own binaries, images, and Helm chart lifecycle in this monorepo. The mecak8s chart
has no default dependency on it. Kubernetes execution is disabled by default; an explicit endpoint and
operator-selected profile connect the optional client. Disabled means no execution Kubernetes client,
informer, goroutine, API call, CRD/RBAC install, or execution resource.

An explicitly selected remote environment fails clearly at validation if its configured endpoint is
unavailable, and fails closed at runtime if the provider is lost or its exact immutable revision cannot
reattach. It never falls back to the local project root. Existing local and `no-fs` behavior is unchanged.

The separate chart is `deploy/helm/mecatl-execution`. Mecak8s enables the client through
`--execution-enabled`, endpoint/profile flags, and three mounted TLS-file flags; OIDC ownership is
required. Profiles and the versioned security manifest are strict operator configuration, not model
inputs. The [configuration contract](../acceptance/native-kubernetes-execution.md#provider-configuration-rotation-and-scoped-administration)
records exact names and bounds. Old binaries reject the scoped-admin field: quiesce and upgrade provider
replicas before introducing it. CRD upgrade remains manual; there is no arbitrary downgrade or
mixed-version compatibility claim for this amendment.

### 4. Separate execution fencing from session leasing

The chosen approach is mTLS service links plus a least-privilege execution grant scoped to the exact
environment, operation, and ownership epoch. Initial qualification is authenticated and authorized use in
an operator-controlled cluster, not hardened hostile multitenancy. The arbitrary-shell workload receives
no controller credential, signing key, provider key, or default service-account token. Same-Pod or
localhost placement is not treated as a trust boundary.

Grant revocation prevents new commands. Cancellation request, acknowledged complete process termination,
and externally proven compute/storage fencing are distinct states. Namespace process teardown must include
detached descendants or fail closed. Pod deletion, Lease timeout, a timeout, controller restart, or
partition is not evidence that an old command stopped and cannot alone authorize takeover. If the old
executor cannot be positively terminated or externally fenced, replacement fails closed.

Ed25519 grants bind issuer/audience, creator, owner, environment revision, binding/run/claim, operation,
epoch, grant generation, and validity window. ResourceVersion CAS gates run claims and the separate
replica-owned operation lease. Expiry retains uncertain operation identity; it does not authorize a
second writer. Exact terminal Pod/PVC proof is persisted before finalizer removal and replacement;
RecoverEnvironment has no caller-supplied force-fencing assertion. The external-fencing runbook remains
necessary for unobservable nodes.

Rotation publishes a validated security snapshot backed by a durable generation/digest/key-fingerprint
high-water ledger. Every RPC rechecks current authority, including requests over established connections.
CA/key/client removal, scope removal, rollback, or equal-generation policy drift fail closed. Neither
security generation nor per-environment grant revocation is proof of termination. No existing SPIFFE
issuer is assumed, and no credential crosses the provider-to-workload stdin boundary.

Scoped administration uses one narrow extension to the existing client policy:
`administrator:true` plus optional `administratorFor:[canonicalCreatorURI]`. Absent/empty scope preserves
self-admin compatibility, not namespace-wide privilege. A distinct operations URI may administer only
the listed creators' allocations. The shared decision derives creator identity from the loaded
immutable `clientHash` and keeps exact owner, revision, epoch, UID, schema/generation, and operation replay
checks. It applies to all six administrative RPCs, including migration, revocation, and retained deletion.
A scope never grants attach, Files, Shell, run/reference access, or MayAttestOwner. No public Harness,
engine, or private protobuf widening is needed. The acceptance contract specifies validation and digest
inclusion; authoring this proposal is not the outstanding human security approval.

### 5. Start with a persistent blank workspace and an attenuated catalogue

The first slice provisions a blank/PVC-backed workspace that existing filesystem tools seed. It supports
foreground Shell and operator-global instructions only. It has no warm pools or automatic idle suspension.
Git clone, private source credentials, remote project ingestion, background commands, and isolated delegation
are deferred. Project AGENTS/rules/skills remain disabled until a separate root/source trust design exists. Current
project admission is local-root based (`internal/app/project_ingestion.go:20`).

Schedules remain excluded. SkillDraft and unsupported remote Subagent/Parallel/Team paths return explicit
unsupported/precondition results and never execute locally. Clear/Fork reserve and publish references
under environment exclusion while preserving the same remote `EnvironmentRef`; neither clones the
workspace. Clear creates empty history and Fork copies valid history. Publication failure preserves the
source; ambiguous reference outcomes stay durable for reconciliation. No public placement selector or
model-chosen profile is added.

### 6. Retain committed workspaces by default

One persistent PVC per logical environment is retained across executor and mecak8s/controller restarts.
Session deletion does not delete a workspace. The lifecycle default is: removal of one reference retains
the environment; removal of the last reference retains it; committed data retires only through explicit
authorized retirement; and a retiring environment rejects new bindings and successors. An unreadable
store/reference is not an orphan and is retained. A pending allocation whose session association might
have committed cannot be collected merely after TTL: publication absence must first be conclusively
reconciled. Explicit retirement of a live/shared environment is refused while any live reference exists
unless a later reviewed retire/quiesce policy specifies otherwise.

Default chart uninstall retains runtime CRs/PVCs. While executors survive, workload isolation,
security high-water/key history, and the profile-capacity ledger must survive too. Their lifetime follows
retained allocations, not the provider Deployment. A keep annotation or no-deletion-hook assertion is not
behavioral proof. Uninstall must not silently remove NetworkPolicies or reset authority on reinstall.

The supported reinstall is the same release and namespace, with explicit validation/reuse of that
release's retained resources and unchanged allocation/profile identity. Preserve ledger contents and
resource ownership; reject foreign/ambiguous resources and missing authority history while allocations
survive. Compatible upgrades also preserve state. Fresh install may initialize empty ledgers only in
the absence of retained allocations. No destructive hook, forced adoption/cleanup, authority-generation
reset, or arbitrary rollback is supported. Manual CRD upgrade precedes explicit compatible schema-v2
migration; CRD/namespace deletion with live resources is destructive and unsupported.

The [Helm lifecycle contract](../acceptance/native-kubernetes-execution.md#helm-lifecycle-amendment)
requires actual install/upgrade/uninstall/reinstall qualification: persistent data, exact reattachment,
negative enforcing-CNI probes during provider absence, rejection of stale authority after restart, and
capacity accounting. Add retained ledgers/isolation/configuration to the resource and rehydration
inventories in `docs/adr/0027-cloud-native.md` during implementation. PVC persistence does not claim
node-disaster recovery; kind host-local storage proves restart survival only.

### 7. Qualify deterministically, then prove native-provider live coding

The focused mock-only kind tasks run on the supported generic toolchain, including CI, with a unique
owned cluster, scratch kubeconfig, explicit context on every command, bounded sanitized artifacts, and
ownership-scoped cleanup. Default kind does not prove NetworkPolicy enforcement; the production profile
uses Calico. The acceptance plan records a successful production job at `deaf1c3d1`, distinct from the
pending final candidate and new scope/retry/Helm regression qualification. Historical local keyring
failures no longer block that baseline. Skipped draft race lanes remain unproven.

The checked human live decision requires the separate `task e2e:k8s:execution:live` OpenRouter coding
qualification **for this stack**, after deterministic qualification. It is not an always-on live CI
dependency. Prove model-driven tools, independently verified persistent artifacts, and a successful
bound test command; generic mecak8s live CI is not native-provider evidence. No final native live run is
claimed. The generic compaction live-lane correction in merged #1728 is separate from this capability.

Use verified nonsecret endpoint/protocol/model configuration in an authenticated operator-controlled
cluster. A trusted runtime-only loader consumes the approved protected file channel into a narrowly
scoped HARNESS-only Secret before tools/child processes can inherit it. Planning and mock tests never
access it. Neither agent nor tool reads the credential file or Secret; failures are redacted, and no
credential enters model/tool output, argv, Helm values, disk manifests, git, logs, artifacts, or the
execution workload. Restore mock configuration and delete only the run-owned Secret by recorded UID.
Token/run limits do not establish a hard dollar cap. Live coding evidence supplements, never replaces,
the deterministic correctness gate.

## Consequences

**Positive.** Mecak8s can optionally bind tools to a persistent Kubernetes namespace without changing
tool schemas, engine APIs, or public placement authority. Disabled deployments remain free of CRDs,
RBAC, informers, clients, and resources. Logical identity survives Pod replacement, and kind can prove
the complete coding path offline.

**Costs and risks.** Mecatl would own a controller, CRD, private protocol, PVC lifecycle, command
execution/cancellation, and a security-critical fencing system. A persistent volume does not stop a
partitioned writer. Arbitrary Shell makes the executor a hostile-workload boundary. Network policy,
resource quotas, output caps, non-root execution, service-account-token suppression, and credential
separation are required but not sufficient.

**Draft boundary.** The exact proposed interfaces are authored in the acceptance plan; human API/schema/
security approval and release maturity remain unresolved. Authorized pre-merge development may proceed
on the existing draft stack, but no review, approval, landed status, or merge is inferred. Completion
requires strict AC trace, full offline/race/build/API/docs/site/demo gates, independent panel review,
final deterministic qualification, and stack-specific native live evidence. New scope, retry, and Helm
behavioral proofs remain pending. Human alone merges.

## See also

- [Native Kubernetes execution acceptance plan](../acceptance/native-kubernetes-execution.md)
- [ADR 0048 — mecak8s](0048-mecak8s.md)
- [ADR 0291 — server-owned session placement](0291-server-owned-session-placement.md)
