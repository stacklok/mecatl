# ADR 0364 — Optional native Kubernetes execution environments

- Status: Draft
- Date: 2026-09-15; draft amended 2026-09-25
- Scope: independently deployed execution environment provider service; mecak8s is an optional client; controller/executor, logical environment, PVC, Pod, authorization, fencing, shared HarnessContext consumption, and kind qualification boundaries
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
Preflight placement validation must remain side-effect-free because using an allocating operation would leak PVCs and Pods.

The human authorized the direction and defaults: a minimal coding-agent MVP for multiple authenticated users
from one company, using the existing TUI OIDC login/connect flow and blank persistent tool-seeded workspaces.
A purpose-built execution environment provider lives in this monorepo, separate from mecak8s, with its own
binaries, images, chart lifecycle, optional client integration, and deterministic kind qualification followed
by native-provider OpenRouter coding evidence. The target is an operator-controlled first production slice
with explicit limits; it is not a hostile/public-tenancy claim, and release maturity remains undecided.

Plan / Interface PR #1579 is ready for review; implementation PR #1614 remains draft. The
[acceptance plan](../acceptance/native-kubernetes-execution.md) authors the exact proposed contract,
reconciled against tested runtime `2020e59934369e8f771059d0c173fa4cbf964c7e` and shared HarnessContext
source/test evidence at implementation candidate `f63055665e97372316847bb6f5a5aaa859cf3c66`. The latter is
not a new Kind, live-provider, or complete user-journey run. API, schema, and security review approvals
remain unchecked; neither authored specs nor draft code is a merged approved baseline. This amendment also
authorizes development of scoped administration, continuing provisioning retries, retained-resource Helm
lifecycle safeguards, and conformance with the shared [HarnessContext source-authority decision](0359-harness-context-source-authority.md).
Baseline coding requires no custom source; optional programmatic sources remain independent from execution,
and PVC-backed context remains unselected. Human alone merges.

## Proposed decision within the unmerged contract

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
RuntimeClass. The MVP qualifies one operator-configured coding image and one mecak8s replica; existing
provider replica-safety behavior remains supported rather than being removed. Public clients/models cannot
submit arbitrary Pod specs, images, paths, URLs, credentials, or Kubernetes names. Creator `clientHash`,
per-user OIDC owner identity, allocation fingerprint, and revision remain immutable. Users behind the same
mecak8s creator identity receive separate owner-bound environments; shared company membership grants no
cross-user list, read, run, control, or delete authority. Profile resource limits and durable capacity
accounting are required, not deferred. The [schema and transaction contract](../acceptance/native-kubernetes-execution.md#reference-transaction-schema-v2-and-fencing)
records exact fields, receipts, conditions, and migration behavior. Completed migration receipts retain
the source schema with explicit presence (including legacy schema zero); replay requires the exact
request identity, and receipts missing that information fail closed. Replacement atomically expires
completed migration receipts when it publishes the new Pod identity; replay with either the original or
replacement UIDs cannot revive an expired receipt.

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
environment, operation, and ownership epoch. Initial qualification serves authenticated users from one
company in an operator-controlled cluster, not random web users and not hardened hostile multitenancy.
Trust in the user population does not bypass OIDC/per-user ownership, configured Ask/Deny, file confinement,
operation fencing, credential isolation, resource limits, or capacity accounting. The arbitrary-shell
workload receives no controller credential, signing key, provider key, or default service-account token.
Same-Pod or localhost placement is not treated as a trust boundary.

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

Material publication uses the existing manifest filenames as immutable identities. Changed signing,
server-certificate, server-key, and client-CA bytes require new generation-specific names. Stage them
before publishing the higher-generation manifest and retain overlap files while any live/in-flight
manifest references them. Secret and ConfigMap updates are not atomic together. One loaded manifest
binds the confined immutable names even across projection changes; missing material fails closed before
ledger publication. No loader projection-pinning helper or new version/config API is required under
this operator-managed publication contract. Reusing names can publish a mixed-generation digest;
correcting that bundle at the same generation is then rejected permanently. Recover only by forward
publication, never by resetting authority history.

Peer-ledger advancement can transiently reject a pinned replica's read before dispatch even after
successful AcquireRun. Qualification may poll only read-only structured retryable `not_ready`, with a
bound and immediate failure on wrong content or other errors. It must not hide mixed-material drift
or count a released/expired claim as evidence of old-key/client rejection.

Scoped administration uses one narrow extension to the existing client policy:
`administrator:true` plus optional `administratorFor:[canonicalCreatorURI]`. Absent/empty scope preserves
self-admin compatibility, not namespace-wide privilege. A distinct operations URI may administer only
the listed creators' allocations. The shared decision derives creator identity from the loaded
immutable `clientHash` and keeps exact owner, revision, epoch, UID, schema/generation, and operation replay
checks. It applies to all six administrative RPCs, including migration, revocation, and retained deletion.
A scope never grants attach, Files, Shell, run/reference access, or MayAttestOwner. No public Harness,
engine, or private protobuf widening is needed. The acceptance contract specifies validation and digest
inclusion; authoring this proposal is not the outstanding human security approval.

### 5. Start with a persistent blank workspace and keep context independent

The MVP provisions a blank PVC-backed workspace that existing filesystem tools seed. A company user signs
in and connects through the existing TUI OIDC flow, creates a session, and directs coding through ordinary
prompts and built-in model instructions. Foreground Shell returns command results; there are no warm pools,
automatic idle suspension, background commands, or in-flight-command survival guarantee. Baseline coding
requires no custom HarnessContext source, source flag, API, configuration, or protocol.

Harness context remains architecturally independent from execution placement. A composition may register
optional programmatic instruction or command sources through the shared operator-tier HarnessContext policy;
remote execution does not suppress those sources merely because the backend is Kubernetes. Shared ordering,
provenance, collision, admission, source authority, cleanup, and Ask/Deny behavior remain canonical. This
compatibility boundary is not a requirement to provision a new source in standalone mecak8s for the MVP.

PVC-backed context is deliberately unselected. Conflicting AGENTS, instructions, or commands planted in the
PVC cannot enter context assembly or discovery. A future exact-backend PVC source requires separate
authorization and native adapter review. It must use the shared principal-scoped `UsesExecutionWorkspace`
registration with authoritative session ID, owner, and profile plus selector-free
`AcquireExecutionWorkspace`. Only a selected admitted source receives that lazy capability. It exposes the
exact authorized workspace read-only, with no runner or execution ReadLedger, and releases only its own
borrow. Independent sources receive no acquisition capability. Source reads never seed execution read
evidence or authorize Edit.

The native integration adds no source or inventory API, durable context identity, history transfer,
`SessionAccess`, five-kind parity requirement, or backend-specific lifecycle. Current shared invariants still
apply: required selected-source failure has no host, PVC, process-cwd, operator-global, or other unconfigured
fallback; each Build applies current policy and authorization; cancellation cannot publish a late binding;
and shutdown waits for in-flight creation and cleanup under the canonical shared
[HarnessContext lifecycle](../acceptance/harness-context.md#scenario-4---source-failure-does-not-change-authority).
Standalone source provisioning and an expanded native-specific source outage/no-FS/rebinding qualification
matrix are deferred product features, not waivers of those authorization, fail-closed, or lifetime rules.

Private Git credentials, remote project ingestion, automatic PRs, import/export, arbitrary user-supplied
tools, background commands, isolated delegation, schedules, and multi-replica MVP qualification remain
deferred. Existing supported provider replica-safety behavior remains in place. Harness content selection
does not change governance or permission roots: configured Ask/Deny, root-aware project trust, hooks,
credentials, tool grants, execution placement, and fencing remain effective. SkillDraft and unsupported
remote Subagent/Parallel/Team, background Shell, worktree-successor, delegation-expansion, and
schedule-expansion paths return explicit unsupported/precondition results and never execute locally.
Clear/Fork reserve and publish references under environment exclusion while preserving the same remote
`EnvironmentRef`; neither clones the workspace. Both share the exact workspace and serialize execution.
Clear creates empty history and Fork copies valid history. Publication failure preserves the source;
ambiguous reference outcomes stay durable for reconciliation. No public placement selector or model-chosen
profile is added.

### 6. Retain committed workspaces by default

One persistent PVC per logical environment is retained across executor and mecak8s/controller restarts.
Session deletion does not delete a workspace; the PVC remains for explicit operator retirement and manual
cleanup. The lifecycle default is: removal of one reference retains the environment; removal of the last
reference retains it; committed data retires only through explicit authorized retirement; and a retiring
environment rejects new bindings and successors. An unreadable store/reference is not an orphan and is
retained. A pending allocation whose session association might have committed cannot be collected merely
after TTL: publication absence must first be conclusively reconciled. Explicit retirement of a live/shared
environment is refused while any live reference exists unless a later reviewed retire/quiesce policy
specifies otherwise.

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
uses Calico. The acceptance plan retains historical production success at `deaf1c3d1` and the
later reported run `35650128625` at `f7919f6c` which passed 10/11 scenarios but failed rotation
at the final-authority read. Offline tests at repair commit `96e16e926` reproduce safe
pre-dispatch replica lag and persistent mixed-material ledger poisoning, and prove immutable-name
publication across both projection orders. They do not identify which mechanism caused that
historical failure. Historical local keyring failures and qualification failures are no longer
current blockers: the [candidate completion receipt](../acceptance/native-kubernetes-execution.md#candidate-completion-receipt)
records all 11 production Kind+Calico scenarios passing at runtime SHA
`2020e59934369e8f771059d0c173fa4cbf964c7e`, including scope/retry/Helm lifecycle qualification.
Draft full-race CI remained skipped; earlier local full-race and latest targeted-race results
are distinguished in that receipt, not represented as a draft full-race CI execution.

MVP product qualification uses one mecak8s replica with Redis, a supported model provider, and one
operator-configured coding image. Existing multi-replica provider safety remains supported but is not a
required MVP topology. Five observable current-candidate journeys remain required and PENDING: actual TUI
company OIDC login/connect through blank-session coding, file edit, protected Ask, and foreground test
results; concurrent Alice/Bob sessions with bidirectional cross-user denial behind the same creator identity;
disconnect/reconnect plus service restart preserving exact files; cancellation or authority loss with no
competing writer or local fallback; and profile-capacity exhaustion followed by authorized retirement/deletion
that safely restores allocation. The acceptance plan names exact proof targets. These journeys can use the
deterministic provider and do not each require a paid model call.

The checked human live decision requires the separate `task e2e:k8s:execution:live` OpenRouter coding
qualification **for this stack**, after deterministic qualification. It is not an always-on live CI
dependency. Prove model-driven tools, independently verified persistent artifacts, and a successful
bound test command; generic mecak8s live CI is not native-provider evidence. Manual exact-head run
[35700303737](https://github.com/stacklok/mecatl/actions/runs/35700303737) supplied that evidence
after production qualification: real OpenRouter coding, independent exact-ref typed-gRPC
reattachment/run acquisition and file/test verification, followed by verified cleanup. This is
qualification of the tested runtime SHA, not approval or a claim that documentation-only descendant
heads executed live. The generic compaction live-lane correction in merged #1728 remains separate.

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
tool schemas, engine APIs, public placement authority, or shared HarnessContext interfaces. Baseline coding
works with a blank workspace and no custom source. Optional programmatic instruction and command sources
continue to work independently from execution; the unselected PVC cannot silently become context authority.
Disabled deployments remain free of CRDs, RBAC, informers, clients, and resources. Logical identity survives
Pod replacement, and kind can prove the coding path offline.

**Costs and risks.** Mecatl would own a controller, CRD, private protocol, PVC lifecycle, command
execution/cancellation, and a security-critical fencing system. A persistent volume does not stop a
partitioned writer. Arbitrary Shell makes the executor a hostile-workload boundary. Network policy,
resource quotas, output caps, non-root execution, service-account-token suppression, and credential
separation are required but not sufficient.

**Draft boundary.** The exact proposed interfaces are authored in the acceptance plan; human API/schema/
security approval and release maturity remain unresolved. Plan / Interface PR #1579 is ready for review;
implementation PR #1614 remains draft, and neither is approved or landed. Shared HarnessContext source/test
evidence at implementation candidate `f63055665e97372316847bb6f5a5aaa859cf3c66` preserves independent
source authority without making standalone source provisioning an MVP requirement; it is not a new Kind,
live-provider, or complete user-journey run. Completion requires strict AC trace, `task test` plus
`task test:race`, build/API/docs/site/demo gates, independent panel review, the five pending current-candidate
journeys, final deterministic qualification, and stack-specific native live evidence. The latter two have
successful receipts only for tested runtime `2020e59934369e8f771059d0c173fa4cbf964c7e`; outstanding human/
panel review and new journey evidence remain explicit. Human alone merges.

## See also

- [Native Kubernetes execution acceptance plan](../acceptance/native-kubernetes-execution.md)
- [ADR 0048 — mecak8s](0048-mecak8s.md)
- [ADR 0291 — server-owned session placement](0291-server-owned-session-placement.md)
- [ADR 0359 — Harness context source authority](0359-harness-context-source-authority.md)
