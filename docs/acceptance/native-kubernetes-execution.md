# Native Kubernetes execution — acceptance plan

**Contract:** human-reviewed/v2
**Work classification:** Architectural — introduces a controller/executor trust boundary, durable environment ownership, Kubernetes resources, and an optional mecak8s deployment integration.
**Decision record:** [ADR 0343](../adr/0343-native-kubernetes-execution.md)
**Phase:** native Kubernetes execution first slice
**Status:** draft, 2026-09-15. Planning artifact only; material protocol and lifecycle decisions remain open.
**Delivery:** Split. Human-authorized draft stack: the Plan / Interface PR targets `main`; the subsequent draft Implementation PR targets `plan/native-kubernetes-execution`. This explicitly permits draft implementation before plan merge, but neither PR is approved or merge-ready by that authorization alone.
**Expected tasks:** deferred to orchestration after the exact interfaces are specified.
**Issue:** None — planning request has no linked issue.
**Plan PR:** draft on `plan/native-kubernetes-execution`.
**Approved baseline:** absent; observed repository baseline `16a8e3b735bdc26f4c958cdf24fd1e7ac00a38f9` is evidence only.

All named verification tests below describe future implementation proof, not existing coverage.

The smallest demonstrable slice is an independently deployed execution environment provider service
with its own controller and executors. Mecak8s is an optional client: its adapter obtains a persistent
Kubernetes workspace and bound Shell through the provider API without owning Pods, PVCs, or the
controller lifecycle. The provider keeps the existing `tool.Environment` and file-tool schemas,
uses an operator-selected profile, proves behavior offline in kind, and makes no remote-project-ingestion claim.
ADR 0048 continues to govern storage-free mecak8s; this separate provider does not supersede it.

## Human decisions

- [x] Keep the execution environment provider a separate service in this monorepo. — Decision: the provider owns its controller, CRDs, Pods, PVCs, binaries, images, chart, and lifecycle; mecak8s consumes its API through an optional adapter and remains storage-free under ADR 0048, which is not superseded.
- [x] Choose a purpose-built controller rather than Agent Sandbox. — Decision: own the controller/executor boundary and its CRD.
- [x] Keep Kubernetes execution optional. — Decision: disabled/default deployments remain Kubernetes-execution-free.
- [x] Retain one PVC per logical environment by default. — Decision: session deletion does not delete a workspace; committed data is removed only through explicit retirement. Storage class, size, and quotas are operator-configured.
- [x] Set the first-slice source and execution boundary. — Decision: use a blank tool-seeded workspace, existing filesystem tools, foreground Shell, and operator-global instructions only, with no warm pools or automatic idle suspension. Defer Git clone, private credentials, remote project ingestion, background commands, and isolated delegation; schedules remain excluded.
- [x] Set the initial security and qualification posture. — Decision: initial use is authenticated and authorized in an operator-controlled cluster, not hardened hostile multitenancy; use mock/kind first, then an optional bounded real-model smoke.
- [x] Choose mTLS service links plus least-privilege grants scoped to environment, operation, and ownership epoch. — Decision: uncertain takeover fails closed; old-writer termination or external fencing is required before replacement.
- [x] Handle live-provider credentials only through a trusted runtime loader. — Decision: it may be consumed directly from its protected file channel to a narrowly scoped HARNESS-only Secret, with redacted failures, before child processes or tools can inherit it. The agent never accesses the file or Secret. No agent or tool reads, displays, or loads it into model/tool output, argv, Helm values, disk manifests, git, logs, artifacts, or the execution workload; mock tests do not access it. Endpoint, protocol, and model selection still require verified nonsecret configuration.
- [ ] Draft and review the exact private control/data-plane API: wire fields/errors, allocation transaction, ownership/reference lifecycle, idempotency, attach/reattach, command stream/cancel/status, and compatibility behavior.
- [ ] Draft and review the exact CRD and configuration schemas, names, versioning, validation, and compatibility/migration rules; approved defaults do not approve exact field names, sizes, or values.
- [ ] Draft and security-review the grant trust, issuance, verification, rotation/revocation, replay bounds, cancellation/termination acknowledgement, and external-fencing proof contract.

## Interface contract

This Plan / Interface PR contains documentation only. The user requested a later draft implementation
PR stacked on this plan branch. Record its exact plan commit as the working baseline, not as a merged
approval. Resolve the open technical contracts in this plan before dispatching affected implementation;
keep both PRs draft until their respective review gates are satisfied.

These are **candidate** surfaces for review, not implemented or approved contracts.

- **gRPC / protobuf:** Public Harness protobuf remains unchanged in the first slice. A private mTLS execution protocol is required for `ValidateProfile`, idempotent `EnsureEnvironment`, exact `AttachEnvironment`, bounded command start/stream/cancel/status, and retirement. Exact messages, field numbers, status/error mapping, fencing fields, stream resume semantics, and authorization metadata remain an unchecked Human decision; the plan cannot become `proposed` until they are written exactly.
- **Exported Go APIs / interfaces:** No `engine/` exported API change is proposed: `tool.Environment` remains immutable identity + Workspace + ReadLedger + optional bound CommandRunner (`engine/tool/environment.go:9`). Candidate host-only seams are a side-effect-free configuration validator/describer and an allocation/reattachment provider implemented under `internal/adapter`; their exact signatures and whether they extend or wrap the placement provider remain open. `Bind` cannot be used for startup validation because its request has no allocation identity and may allocate (`internal/adapter/server/placement.go:99-117`), while exact `Reattach` is distinct (`internal/adapter/server/placement.go:149`).
- **Tool schemas:** Read/ListDir/Edit/Write/Copy/Move/Remove/Grep/Glob/Shell schemas remain byte-compatible. The remote catalogue is explicit attenuation: the first slice advertises supported filesystem tools and Shell, excludes unsupported SkillDraft/Parallel/delegation/schedule paths as decided, and never silently routes an unsupported remote operation to local execution. Existing versioned CreateFile/ReplaceFile semantics remain (`engine/tool/tool.go:353-424`); Shell's documented POSIX bypass of file CAS remains honest (`engine/tool/tool.go:380-390`).
- **CLI / config:** Kubernetes execution remains disabled by default. The provider is packaged and released in this monorepo with its own binaries, images, and `deploy/helm/mecatl-execution` chart; the default mecak8s chart installs none of them and creates no Kubernetes client, informer, or execution goroutine. Candidate mecak8s configuration includes an enablement switch, endpoint, operator-selected profile, mTLS references, and operator-configured storage class/size/quotas. Exact names, precedence, validation behavior, Secret key layout, and schema compatibility remain to be drafted and reviewed.
- **Events / persistence:** The CRD and configuration schema remain future technical specifications. The approved storage behavior is one PVC per logical environment, retained by default across session deletion and executor replacement; session deletion is not workspace deletion, and committed data retires only through explicit authorized retirement. Storage class, size, and quotas are operator-configured, with exact field names and values still to be defined. `EnvironmentRef.Revision` remains immutable persisted identity, distinct from a transient execution-fence epoch; Pod UID is never durable session identity. The allocation transaction, ownership/reference lifecycle, status shape, exact mapping, and lifecycle protocol remain open for authoring and review.
- **Security / authority:** Initial qualification is for authenticated, authorized use in an operator-controlled cluster, not hardened hostile multitenancy. The chosen approach is mTLS service links plus least-privilege grants scoped to the exact environment, operation, and ownership epoch. The supervisor/controller and arbitrary-shell workload remain separate trust domains; no controller credential, signing key, provider key, or default service-account token enters the workload. The exact grant trust, issuance, verification, revocation, cancellation/termination acknowledgement, and fencing contract remains unresolved and blocks proposal. Any uncertain takeover fails closed: old-writer termination or external compute/storage fencing must be proven before replacement.
- **Compatibility / migration:** Additive and opt-in: existing local and `no-fs` sessions retain current behavior, and disabled mecak8s performs no Kubernetes-execution API calls or resource installation. Remote selection is explicit and fails closed when unavailable, with no local fallback. Existing Clear/Fork behavior is not environment-serialized today; the candidate contract would add environment exclusion while preserving same-`EnvironmentRef` successor semantics, subject to the unchecked lifecycle decision. PR #580 may inform behavioral conformance but is neither a dependency nor a protocol source.

## In scope — 7 scenarios, in implementation order

### Scenario 1 — Disabled means absent, including startup validation

The optional deployment boundary follows [draft ADR 0343](../adr/0343-native-kubernetes-execution.md).
Current startup provider validation reaches `Bind` (`docs/design/IMPLEMENTATION-NOTES.md:6549-6560`),
so execution configuration needs a side-effect-free validation path rather than a fake allocation.

**Acceptance:**
- AC1.1: With execution disabled, mecak8s creates no Kubernetes execution client, informer, goroutine, CRD, RBAC, controller, executor, PVC, or Pod, and performs no execution API call.
  - verify: `TestNativeKubernetesExecution_Scenario1_DisabledHasNoEffects`
- AC1.2: The provider service deploys, restarts, and reconciles independently of mecak8s. The default mecak8s chart has no dependency on the provider chart and installs no provider resource; enabling the client adapter requires explicit endpoint/profile configuration. The adapter needs no Pod/PVC/controller management RBAC.
  - verify: `TestNativeKubernetesExecution_Scenario1_DefaultChartIndependent`
- AC1.3: Preflight validates configured profiles and endpoint/provider compatibility without allocating an environment. Allocation occurs only at an authorized session-binding operation; enabled configuration that is incomplete fails startup before allocation, and a configured but unavailable endpoint fails validation clearly with no local fallback.
  - verify: `TestADR_0343_PreflightNeverAllocates`
- AC1.4: Disabling the mecak8s client integration neither adopts nor deletes existing `ExecutionEnvironment` CRs, PVCs, or Pods and does not stop the separate provider's reconciliation for its existing allocations or other authorized clients.
  - verify: `TestNativeKubernetesExecution_Scenario1_DisabledDoesNotAdoptOrDeleteExistingResources`

### Scenario 2 — One idempotent logical environment is allocated per binding

The controller owns a logical environment, PVC, and executor lifecycle rather than equating identity
with an ephemeral Pod, as decided in [draft ADR 0343](../adr/0343-native-kubernetes-execution.md).

**Acceptance:**
- AC2.1: Repeating the same authorized ensure request with the same final idempotency identity returns the same environment generation and does not create a second PVC or Pod.
  - verify: `TestNativeKubernetesExecution_Scenario2_IdempotentEnsure`
- AC2.2: A stale/different profile, digest, storage request, generation, or owner fails with the final reviewed conflict/precondition error and never adopts or mutates an unrelated allocation.
  - verify: `TestADR_0343_AllocationIdentityFailsClosed`
- AC2.3: Only operator-configured profile references select digest-pinned images and storage; public clients/models cannot submit arbitrary images, Pod specs, paths, URLs, source credentials, or Kubernetes object names.
  - verify: `TestNativeKubernetesExecution_Scenario2_ProfileIsOperatorSelected`

### Scenario 3 — Existing file tools and Shell are transparent but honest

The adapter preserves the immutable environment and independent read ledger
(`engine/tool/environment.go:9`; `engine/tool/ledger.go:1`) and the existing WorkspaceReader seam
(`engine/tool/tool.go:314`), under [draft ADR 0343](../adr/0343-native-kubernetes-execution.md).

**Acceptance:**
- AC3.1: A fresh environment can create, read, conditionally edit, replace, copy, move, remove, list, grep, and glob a small Go fixture with unchanged tool schemas and the existing read-before-edit/version behavior where that contract applies.
  - verify: `TestNativeKubernetesExecution_Scenario3_FileToolConformance`
- AC3.2: Read/Edit/Write preserve their applicable recorded-read and CreateFile/ReplaceFile CAS contracts; Copy/Move/Remove preserve their own positive conformance contracts and do not require an unrelated content read ledger/CAS precondition.
  - verify: `TestInvariant_remote_execution_preserves_file_version_protocol`
- AC3.3: File access is physically confined through symlink traversal rather than lexical checks alone, and every read, mutate, list, search, status, stream, and cancel request is authorized for the exact environment; stale, expired, revoked, or wrong-environment grants are denied.
  - verify: `TestADR_0343_RemoteFilesystemAndOperationAuthorizationFailClosed`
- AC3.4: Shell is bound to the same environment namespace as Workspace. Cancellation distinguishes requested cancellation, acknowledged complete process termination, and externally proven compute/storage fencing; namespace teardown includes detached descendants or fails closed. Output is capped and valid UTF-8 repaired, and Shell-created file changes are not falsely claimed to participate in Workspace CAS.
  - verify: `TestNativeKubernetesExecution_Scenario3_ShellNamespaceCancelAndCASDisclosure`

### Scenario 4 — Caller isolation and execution ownership survive failures

A session lease is session-only and its token is not consulted for writes
(`engine/port/lease.go:37-48`); [draft ADR 0343](../adr/0343-native-kubernetes-execution.md)
therefore requires independent environment execution fencing.

**Acceptance:**
- AC4.1: Caller A cannot discover, attach, execute in, stream from, cancel, or retire caller B's environment; hidden and absent allocations are indistinguishable at the public boundary.
  - verify: `TestNativeKubernetesExecution_Scenario4_CallerEnvironmentIsolation`
- AC4.2: Every command is authorized for one immutable environment revision and a distinct transient execution-fence epoch; lease loss or grant revocation prevents new commands and cancels/fences active work without exposing capability credentials to the workload or another caller's output. Same-Pod placement alone is not a security boundary.
  - verify: `TestADR_0343_ExecutionGrantIsEnvironmentAndFenceScoped`
- AC4.3: Timeout, Pod deletion, controller restart, Lease timeout, or network partition alone never authorizes a replacement executor while an old writer may run; unknown fencing state fails closed and requires the reviewed manual/external fencing path.
  - verify: `TestADR_0343_PartitionCannotAuthorizeTakeoverByTimeout`

### Scenario 5 — Restart preserves workspace while retirement is deliberate

Exact reattachment and safe lifecycle behavior follow [draft ADR 0343](../adr/0343-native-kubernetes-execution.md)
and remain separate from the session-only lease contract (`engine/port/lease.go:37-48`).

**Acceptance:**
- AC5.1: After mecak8s and controller restart, exact reattachment to the persisted environment revision restores the same PVC data and a correctly bound runner; an unavailable/mismatched generation fails with no default/local fallback.
  - verify: `TestNativeKubernetesExecution_Scenario5_RestartReattachesPVC`
- AC5.2: Executor Pod replacement preserves the logical environment and PVC while current Pod UID may change without a spec `metadata.generation` change; `observedGeneration` records processed spec generation, and status records current Pod/PVC UID references without using them as durable session identity.
  - verify: `TestADR_0343_LogicalIdentityOutlivesPodUID`
- AC5.3: The lifecycle matrix is explicit: removal of one reference retains the environment; removal of the last reference retains it by default; committed data retires only through explicit authorized retirement; a retiring environment rejects new bindings and successors. An unreadable store/reference is not an orphan and is retained.
  - verify: `TestNativeKubernetesExecution_Scenario5_RetentionAndSafeRetirement`
- AC5.4: A pending allocation whose session association might have committed is not garbage-collected merely after TTL; collection waits for conclusive reconciliation that publication is absent. Explicit retirement of a live/shared environment is refused while any live reference exists unless a separately reviewed retire/quiesce policy authorizes it.
  - verify: `TestADR_0343_PendingAllocationAndLiveReferenceRetirementFailClosed`
- AC5.5: Default chart uninstall never deletes runtime CRs/PVCs; deletion of CRDs while live resources exist is destructive and unsupported, and no automatic hook deletes CRDs. Owner references alone are not a lifecycle proof.
  - verify: `TestADR_0343_UninstallRetentionAndCRDDeletionSafety`
- AC5.6: PVC persistence is not documented as node-disaster recovery; kind host-local storage proves restart survival only, and a dead or unobservable node is outside that positive guarantee.
  - verify: inspection — human review of deployment documentation; `task docs` checks links and structure after authorized tracked changes

### Scenario 6 — Remote catalog and project-source boundaries are explicit

The first slice avoids claiming that local project trust applies remotely: current admission is rooted
in local composition (`internal/app/project_ingestion.go:20`) and AGENTS discovery reads Workspace
(`engine/prompt/builder.go:337`). The boundary is recorded in [draft ADR 0343](../adr/0343-native-kubernetes-execution.md).

**Acceptance:**
- AC6.1: A remote execution session receives operator-global instructions and only its explicitly supported catalog; project AGENTS/rules/skills and Git source ingestion are off unless a later reviewed source/trust contract enables them.
  - verify: `TestNativeKubernetesExecution_Scenario6_NoFalseRemoteProjectIngestion`
- AC6.2: Schedules remain excluded. Background commands, isolated Subagent/Parallel/Team delegation, SkillDraft, Git/source operations, and unsupported successor modes are rejected with named capability/precondition errors rather than running locally or silently omitting work; `no-fs` remains unchanged.
  - verify: `TestNativeKubernetesExecution_Scenario6_UnsupportedPathsNeverFallBackLocal`
- AC6.3: The candidate Clear/Fork contract adds environment exclusion while preserving same-remote-`EnvironmentRef` successor semantics; it makes no claim that current Clear/Fork already serializes shared environments, adds no public Harness field, and never lets a model choose an environment/profile.
  - verify: `TestNativeKubernetesExecution_Scenario6_SuccessorSemantics`

### Scenario 7 — kind proves an offline coding flow; live qualification is separate

The authoritative e2e is a focused mock-only task, following [draft ADR 0343](../adr/0343-native-kubernetes-execution.md).
The existing broad suite creates cluster/images before key capture (`e2e/k8s/suite_test.go:32-80`),
so the focused task must not inherit ambient provider credentials.

**Acceptance:**
- AC7.1: Candidate `task e2e:k8s:execution` runs on the supported generic toolchain, including CI, with a unique owned kind cluster, a scratch kubeconfig, and explicit `--kubeconfig`/context on every command. It deploys the separate chart plus explicitly connected mecak8s and uses only the deterministic mock provider.
  - verify: `TestNativeKubernetesExecution_Scenario7_FocusedKindMockFlow`
- AC7.2: The mock flow seeds a small Go fixture through file tools, asks the harness to implement a function/tests, runs `go test` through bound Shell, and verifies the persisted artifact and restart/reattach behavior.
  - verify: `TestNativeKubernetesExecution_Scenario7_MockCodingFlow`
- AC7.3: Failure artifacts are bounded and sanitized; cleanup targets only the recorded owned cluster. Destructive live cleanup requires confirmation and never mutates ambient kubeconfig/context.
  - verify: `TestNativeKubernetesExecution_Scenario7_OwnershipAndSanitizedArtifacts`
- AC7.4: Candidate `task e2e:k8s:execution:live` is a separate explicit, bounded real-model smoke after mock/kind qualification. It runs only against verified nonsecret endpoint, protocol, and model configuration in an authenticated, authorized operator-controlled cluster; it does not claim a hard dollar cap from token/run limits.
  - verify: `TestNativeKubernetesExecution_Scenario7_LiveQualificationIsExplicit`
- AC7.5: A trusted runtime-only loader may consume the approved credential directly from its protected file channel and create a narrowly scoped HARNESS-only Secret through a protected channel before child processes or tools can inherit it. The agent never accesses the file or Secret; failures are redacted, and the credential never appears in model/tool output, argv, Helm values, disk manifests, git, logs, artifacts, or the execution workload. Mock tests do not access it.
  - verify: `TestNativeKubernetesExecution_Scenario7_LiveSecretAndSpendBoundary`

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
| Provider credential discovery, arbitrary endpoint forwarding, or live model inference beyond the approved runtime-loader boundary | Separate explicit qualification | The approved credential is not read in planning/mock tests; verified nonsecret endpoint/protocol/model configuration governs any optional smoke. |
| Depending on or copying PR #580's transport | Independent work | Share behavioral conformance where useful; do not inherit unqualified cancellation/auth protocol. |

## Definition of done

1. The outstanding technical specifications — private API, allocation/ownership lifecycle, CRD/config schema and compatibility, and grant/revocation/termination/fencing proof — are authored and reviewed in this draft Plan / Interface PR before the affected implementation is dispatched.
2. The authorized implementation passes applicable `task lint`, `task test`, `task docs`, `task api:check`, `task site:build`, and `go run ./cmd/mecademo` gates.
3. `task ac-trace-strict` resolves every named proof when the plan becomes `landed`.
4. The focused mock-only kind task passes on the supported toolchain with a unique owned cluster; optional live qualification is reported separately and never substitutes for it.
5. The draft implementation PR targets `plan/native-kubernetes-execution`, links the draft Plan / Interface PR and exact working baseline, reports interface conformance, and completes panel review before becoming merge-ready. Neither PR is merged automatically.

## Deferred decisions and known risks

- Exact wire fields/errors, lifecycle signatures, CRD schema, and auth/fencing protocol are material open decisions in `## Human decisions`; implementation must not fill them in opportunistically.
- A PVC can preserve files while a stale command still mutates them. Session leasing does not fence command execution, and a timeout is not proof that a partitioned executor stopped.
- Default kind does not enforce NetworkPolicy. A negative isolation test needs a network-policy-capable CNI and must state what was actually enforced.
- Controller/executor credentials, signing material, and provider secrets must never enter arbitrary-shell workloads.
- The future controller, informer/cache, execution client, connection pool, grant verifier/rotation state, and stream/cancellation registry all outlive one tool call and must be inventoried in `docs/adr/0027-cloud-native.md` when tracked design work is authorized.
