# ADR 0346 — Optional native Kubernetes execution environments

- Status: Draft
- Date: 2026-09-15
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

The human has approved the direction and defaults: a purpose-built execution environment provider service in this monorepo, separate from mecak8s, with its own binaries, images, chart lifecycle, optional client integration, and mock/kind-first qualification. The companion
[acceptance plan](../acceptance/native-kubernetes-execution.md) and this ADR remain draft because the
exact private protocol, lifecycle, CRD/configuration schema, and grant/fencing specifications still
need authoring and review.

## Approved direction and defaults; technical specifications remain draft

This ADR records the approved direction: a separate execution environment provider in this monorepo;
it does **not supersede ADR 0048**. ADR 0048 governs storage-free mecak8s. Mecak8s remains a client of
the provider, keeps session state in its existing stores, and neither mounts provider workspace PVCs nor
owns executor/controller lifecycle. The provider owns its CRDs, Pods, PVCs, authorization,
reconciliation, binaries, images, and chart lifecycle. Its API is not tied to a mecak8s process or
release; other authorized Mecatl compositions can use an adapter without embedding the controller.
Provider allocation handles remain separate from harness session IDs. Exact API/version compatibility
still requires specification and review.

### 1. Own a narrow controller/executor boundary

Ship a dedicated controller and executor integration rather than Agent Sandbox. The controller
reconciles a logical execution environment consisting of a future `ExecutionEnvironment` custom
resource, persistent workspace PVC, and replaceable executor Pod. Durable identity is the logical
environment plus immutable `EnvironmentRef.Revision`, which persists unchanged across run ownership
turnover; it is not a fence epoch or restart counter. Pod and PVC names and UIDs are observed references
used to detect replacement/adoption errors.

The future CRD must express operator-selected, digest-pinned profile/image/storage policy and distinguish
processed spec generation from the current executor Pod UID. Only operator-configured profiles may choose
image, storage, or source policy. Public clients and models cannot submit arbitrary Pod specs, images,
paths, URLs, credentials, or Kubernetes names. Its exact group/version/kind, schema, conditions,
mutability rules, and source metadata remain to be authored and reviewed.

### 2. Preserve the core environment and tool contracts

Keep `engine/tool` and public Harness APIs unchanged. A host-side adapter maps an exact persisted
`session.EnvironmentRef` to a complete remote `tool.Environment`. Existing filesystem tool schemas,
read-before-edit ledger, CreateFile/ReplaceFile version protocol, and bound Shell semantics remain.
Shell writes retain their documented ability to bypass Workspace CAS
(`engine/tool/tool.go:353-424`); the product must disclose that honestly rather than promising a
stronger remote filesystem transaction.

Introduce a side-effect-free configuration/profile validation operation distinct from idempotent
allocation. Candidate private operations include profile validation, ensure, exact attach, command
start/stream/status/cancel, and retirement. Their precise Go signatures and private wire protocol
are deliberately undecided and block promotion of this ADR.

### 3. Make installation and connection explicitly optional

The provider has its own binaries, images, and Helm chart lifecycle in this monorepo. The mecak8s chart
has no default dependency on it. Kubernetes execution is disabled by default; an explicit endpoint and
operator-selected profile connect the optional client. Disabled means no execution Kubernetes client,
informer, goroutine, API call, CRD/RBAC install, or execution resource.

An explicitly selected remote environment fails clearly at validation if its configured endpoint is
unavailable, and fails closed at runtime if the provider is lost or its exact immutable revision cannot
reattach. It never falls back to the local project root. Existing local and `no-fs` behavior is unchanged.

Exact packaging names, values/flag precedence, mTLS Secret layout, storage class/size/quota configuration,
and compatibility rules remain to be authored and reviewed.

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

The exact grant trust, issuer/verifier, claims, audience, rotation/revocation, replay protection, command
cancellation acknowledgement, process-group termination, and takeover/fencing proof remain to be authored
and security-reviewed. No existing SPIFFE issuer is assumed.

### 5. Start with a persistent blank workspace and an attenuated catalogue

The first slice provisions a blank/PVC-backed workspace that existing filesystem tools seed. It supports
foreground Shell and operator-global instructions only. It has no warm pools or automatic idle suspension.
Git clone, private source credentials, remote project ingestion, background commands, and isolated delegation
are deferred. Project AGENTS/rules/skills remain disabled until a separate root/source trust design exists. Current
project admission is local-root based (`internal/app/project_ingestion.go:20`).

Schedules remain excluded. SkillDraft and unsupported remote Subagent/Parallel/Team paths return explicit
unsupported/precondition results and never execute locally. Existing Clear/Fork is not environment-
serialized today; the future lifecycle contract must preserve same-`EnvironmentRef` successor semantics
while adding appropriate environment exclusion. Exact capability/error and lifecycle mechanics remain
technical specifications, not new Human direction decisions.

### 6. Retain committed workspaces by default

One persistent PVC per logical environment is retained across executor and mecak8s/controller restarts.
Session deletion does not delete a workspace. The lifecycle default is: removal of one reference retains
the environment; removal of the last reference retains it; committed data retires only through explicit
authorized retirement; and a retiring environment rejects new bindings and successors. An unreadable
store/reference is not an orphan and is retained. A pending allocation whose session association might
have committed cannot be collected merely after TTL: publication absence must first be conclusively
reconciled. Explicit retirement of a live/shared environment is refused while any live reference exists
unless a later reviewed retire/quiesce policy specifies otherwise.

Default chart uninstall never deletes runtime CRs/PVCs. Deleting CRDs while live resources exist is
destructive and unsupported; no automatic hook deletes CRDs. Owner references alone do not prove safe
lifecycle ownership. PVC persistence does not claim node-disaster recovery; kind host-local storage
proves only process/Pod restart survival, not recovery from a dead or unobservable node.

Storage class, size, and quotas are operator-configured. Their exact configuration schema, sharing,
committed-state marker, retention mechanics, and retirement authorization remain to be authored and
reviewed.

### 7. Qualify offline before optional live inference

Add a focused mock-only kind task separate from the broad e2e suite. The task must run on the
supported generic toolchain, including CI.
It uses a unique owned cluster, a scratch kubeconfig, explicit kubeconfig/context on every command,
sanitized bounded artifacts, and confirmation before destructive live cleanup. Default kind is not
accepted as negative NetworkPolicy evidence; isolation testing needs a network-policy-capable CNI.

A later real-model smoke is separate and bounded, after mock/kind qualification, for authenticated and
authorized use in an operator-controlled cluster. Endpoint, protocol, and model selection require verified
nonsecret configuration; credential usability does not authorize arbitrary endpoint forwarding. The approved
credential is not accessed by planning or mock tests. A trusted runtime-only loader may consume its protected
file directly through a protected channel to create a narrowly scoped HARNESS-only Secret before child
processes or tools can inherit it; the agent never accesses the file or Secret. Failures are redacted, and
the credential never enters model/tool output,
argv, Helm values, disk manifests, git, logs, artifacts, or the execution workload. Token/run limits do not
claim a hard dollar cap. One bounded coding prompt is qualitative; deterministic mock behavior remains the
correctness gate.

## Consequences

**Positive.** Mecak8s can optionally bind tools to a persistent Kubernetes namespace without changing
tool schemas, engine APIs, or public placement authority. Disabled deployments remain free of CRDs,
RBAC, informers, clients, and resources. Logical identity survives Pod replacement, and kind can prove
the complete coding path offline.

**Costs and risks.** Mecatl would own a controller, CRD, private protocol, PVC lifecycle, command
streaming/cancellation, and a security-critical fencing system. A persistent volume does not stop a
partitioned writer. Arbitrary Shell makes the executor a hostile-workload boundary. Network policy,
resource quotas, output caps, non-root execution, service-account-token suppression, and credential
separation are required but not sufficient.

**Draft boundary.** The direction and defaults above are approved, but this ADR is not implementation-ready.
The next planning work authors and reviews the exact private wire fields/errors, allocation transaction and
ownership/reference lifecycle, CRD/configuration schema and compatibility, and grant trust/revocation/
termination/fencing proof. No implementation may invent those contracts.

## See also

- [Native Kubernetes execution acceptance plan](../acceptance/native-kubernetes-execution.md)
- [ADR 0048 — mecak8s](0048-mecak8s.md)
- [ADR 0291 — server-owned session placement](0291-server-owned-session-placement.md)
