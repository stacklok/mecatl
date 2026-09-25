# MicroVM execution environments — acceptance plan

**Contract:** human-reviewed/v2
**Work classification:** Architectural — this amendment adds connection-owned, daemon-local MicroVM acquisitions across local harness processes while preserving the independently approved Environment and HarnessContext contracts.
**Decision record:** [ADR 0364](../adr/0364-microvm-execution-environments.md)

**Phase:** Repository-scoped local microVM execution, including the experimental Darwin arm64 path
**Status:** in-progress
**Qualification:** The amended cross-process acquisition contract in AC5.2 and AC5.7–AC5.10 is implemented with offline production-composed ownership proofs. Prior Linux amd64 qualification does not qualify this changed protocol on a live guest. Darwin arm64 remains experimental and awaits physical HVF and release qualification.
**Evidence:** Tasks 59–65 and deterministic offline Linux coverage are complete. `task e2e:microvm` is the automated Linux amd64 KVM gate and uses the deterministic in-process mock provider; it does not contact OpenRouter and is not the evidence for the live-provider claim. A separate manual qualification trace was executed on 2026-09-10 with Linux amd64 KVM and OpenRouter `openai/gpt-5-mini` through the public HTTP create and prompt APIs. Normal Write, Read, and Bash ran in the Wolfi guest as UID 65532; the proof marker existed only in the MicroVM logical worktree. The same session reattached after mecated restarted while microvmd remained alive, and `microvm doctor` and `microvm status` both reported healthy. This does not claim recovery after a microvmd restart. No credential, exact private placement ref, socket, or host path from that trace is retained in this document. Darwin unit, ownership-xattr, and lifecycle tests have only been cross-compiled locally on Linux; the unpushed macOS CI change has no result, and no physical Apple Silicon VM journey has run.
**Issue:** [stacklok/mecatl#526](https://github.com/stacklok/mecatl/issues/526)
**Delivery:** Split. PR 580 is the sole delivery. The operator explicitly authorized direct same-PR amendment of this plan and unmerged ADR 0364 for connection-owned acquisitions, overriding the separate amendment-PR/superseding-ADR route. The amendment is committed separately before implementation; tests, security review, and human merge authority remain required.
**ADR:** [ADR 0364](../adr/0364-microvm-execution-environments.md), with the proposed Darwin decision in [ADR 0365](../adr/0365-microvm-darwin-xattr-ownership.md)
**Accumulator branch:** `acc/microvm-execution-environments`

The MVP uses one mutable microVM and one rootfs for one local operator and one canonical
Git repository. Sessions, schedules, and isolated children reuse that repository VM but receive
separate daemon-created worktrees and authenticated logical `EnvironmentRef`s. A schedule
created from a session borrows its exact worktree; an independent schedule owns one logical
worktree reused by every fire. This is a
small local, single-user, Git-only capability: it deliberately does not solve fleet
management, comprehensive lifecycle automation, or cross-process merge coordination.
Harness context follows the independently approved
[harness-context source contract](harness-context.md): MicroVM placement registers an opt-in
`repository` instruction and command source, while operator policy alone enables it. The
backend evidence for exact guest-source acquisition belongs to that contract's Scenario 6;
the placement, schedule, delegation, and platform criteria remain unchanged except for
AC5.2 and AC5.7–AC5.10 below. Connection-owned acquisitions support multiple local coding
terminals without extra configuration or a one-harness-per-daemon restriction.

Tasks 59–65 preserve their completed behavior and acceptance criteria and replace
all earlier superseded redesign tasks.

## Human decisions

- [x] HarnessContext alignment. — Decision: The directing human authorized AC5.6 for PR 580 in [the HarnessContext direction](https://github.com/stacklok/mecatl/pull/580#issuecomment-5814249136), limiting it to alignment with merged 1875/1878; it preserves the approved MicroVM criteria and adds no architectural decision.

- [x] Local multi-instance baseline. — Decision: Multiple mecatui/embedded-mecated instances on one repository are normal coding use; no one-harness-per-daemon restriction is acceptable.
- [x] Connection-owned acquisitions. — Decision: The directing human approved the reviewed connection-owned, daemon-local acquisition proposal on 2026-09-25. One retained authenticated Unix socket per independently acquired binding provides process-death cleanup without heartbeat, TTL, database, or new configuration. The exact private v4 exchange below governs release, operation pins, rollback and restart.
- [x] Cleanup and compatibility. — Decision: Ambiguous outcomes retain durable worktrees, live ownership/pins block destructive deletion, and private v4 rejects v3 rather than allowing ref-global detach. A compatibility mismatch does not automatically replace a daemon serving other local harnesses.
- [x] Direct same-PR amendment. — Decision: The directing human authorized this plan's AC5.2 replacement and AC5.7–AC5.10 additions, the narrow in-place attachment-lifetime amendment to unmerged ADR 0364, and ADR 0027's maintained inventories. This explicitly overrides the separate amendment-PR/superseding-ADR route; all other criteria and the shared HarnessContext contract remain intact. Verbatim approval and source are recorded in the existing run's `run.md`.

## Interface contract

- **gRPC / protobuf:** None — no public RPC, session field, inventory selector, client SDK, or HarnessContext API change. The private Unix JSON lifecycle delta is specified below.
- **Exported Go APIs / interfaces:** No `engine` API change. In the opt-in `environment/microvm` module, `LifecycleProtocolVersion` becomes `4`; `LifecycleRequest` and `LifecycleResponse` gain `AcquisitionID string` with `json:"acquisition_id,omitempty"`; `ChildMergePayload` gains `ChildAcquisitionID string` with `json:"child_acquisition_id"`. Mirror these fields in the root adapter's private wire structs and set its `DaemonProtocolVersion` to `4`. Existing `PlacementBinding`, `ExecutionWorkspaceAcquirer`, and `tool.Environment` signatures remain unchanged. `Daemon.ServeConn` acquires the connection-lifetime semantics below; its stateless `Handle` entry must reject ownership-creating v4 requests rather than fabricate a connection owner. Tests exercising acquisition go through ServeConn.
- **Tool schemas:** None — no new tool, argument, model-visible handle, or source selector. Existing instruction/source tools discover exactly the same content under the same policy.
- **CLI / config:** None — no opt-in flag, lease timeout, owner ID, maintenance command, or one-instance limit. Existing readiness compatibility checks reject mixed protocol versions with the existing actionable mismatch path; a new client does not kill a daemon serving older clients. Existing `harness_context` operator policy may select the registered `repository` instruction and command sources; MicroVM placement alone does not enable them.
- **Events / persistence:** No session/schedule/placement schema migration. Acquisition IDs, sockets, operation pins and pending teardown are daemon memory, never persisted or emitted in model/client/durable event projections. Existing exact refs, dirty retention and attachment inventory remain the durable truth.
- **Security / authority:** Kernel Unix-peer authentication and complete authorized Binding/ref/generation checks remain mandatory on every request. An acquisition ID scopes lifetime; knowing it alone does not authorize a different principal/ref or bypass project ingestion, permissions, configured Ask/Deny, credentials, or no-FS restrictions. No virtual root is reopened on the host. Do not log acquisition IDs or add them to status output. Source views remain read-only, runner-less and execution-ledger-free. A selected repository source requires independent project trust and ingestion admission and reads only its exact guest workspace.
- **Compatibility / migration:** Exact private protocol break v3→v4 for this unreleased feature; no unsafe legacy detach shim. No durable data migration. Upgrade/restart the daemon only through existing safe manager behavior; old handles are unusable after daemon restart, while durable worktrees and refs remain reattachable. This is not automatic replay or transparent recovery of in-flight commands. Unselected and independently registered sources retain their existing behavior.

### Private lifecycle v4: exact exchange rules

The added `acquisition_id` is 32 lowercase hexadecimal characters from 16 cryptographically random bytes, minted by microvmd and checked against active IDs before publication. It identifies one live acquisition, not the placement. The daemon stores its complete Binding and owning connection; clients cannot choose it. Cryptographic freshness plus active-collision rejection suffices: do not promise mathematically impossible collision-free randomness or retain released-ID tombstones. Existing durable inventory `attachment_id` and the operator's `--attachment-id` remain unchanged.

| Operation | Request | Response / connection lifetime |
|---|---|---|
| `create` | Existing Provision/Binding; `acquisition_id` absent | Existing Created and allocated Binding plus new ID. The same socket stays open and owns this new binding. |
| `resolve` | Existing exact Binding and Provision; ID absent | Exact Binding plus a **new** ID even if another client already holds the ref. Same socket stays open. No replacement placement or SessionStore lookup is implied. |
| `fork` | Parent Binding and its live `acquisition_id`; existing label payload | Existing newly allocated child Binding plus a new child ID. The fork request's socket becomes the child owner; the parent owner/socket remains unchanged. Parent is pinned for the operation. |
| `workspace`, `exec` | Complete Binding and live matching ID | Existing unary/streamed response. The one-shot request connection remains separate from the retained owner socket; operation pins protect registration until its call finishes. |
| `merge` | Parent Binding/ID; payload contains existing Child binding and `child_acquisition_id` | Validate and pin both owners before using either environment; preserve existing merge/conflict behavior. No cross-process Git transaction claim. |
| `detach` | Sent **on the acquisition's retained socket**, with that exact Binding and ID, empty Provision/Payload | Retire only that acquisition, return exact Binding/ID and cleanup result, then close socket. It is never a fresh one-shot global detach by ref. |
| `delete`, `child-delete` | Existing exact deletion authorization. Owned rollback/child cleanup uses the retained socket and matching AcquisitionID; ordinary management/schedule deletion carries no ID | Owned admission requires the requesting acquisition to be the sole owner and the ref to have **zero operation pins**; no-ID deletion requires zero owners and pins. Admission, a per-ref deleting gate, and consumption of the requesting acquisition are atomic. The gate excludes new acquisitions until deletion completes or uncertain teardown is reconciled. Every admitted terminal outcome—clean deletion, dirty retention, cleanup error, or lost reply—retires the requesting ID and closes its socket. `in_use` performs no destructive mutation but also retires the requesting owner by ordinary release; the client always closes its socket, since its rollback callback is discarded. Other owners/pins survive. No `force` override or later implicit delete on EOF/error. |
| `info`, `metrics`, `inventory`, `inspect`, manager `shutdown` | Existing checks; ID absent | Remain bounded one-shot operations. Inspect does not acquire or release a registration. Explicit manager shutdown retains its existing authenticated daemon-wide meaning, not a release shortcut. |

Successful `create`/`resolve`/`fork` publish their acquisition in the daemon before replying. The client returns a binding only after validating the response and atomically transferring the socket from attempt-context cancellation to the returned owner's lifetime. Cancellation winning that transfer runs the safely attributable cleanup below; later completion/cancellation of the creation request cannot close a transferred binding. A resolve retry opens a fresh socket and gets a fresh owner; no request-ID deduplication store is required. Create/fork are not blindly retried after an unknown outcome.

**Response validation is separate from rollback authority:**
- A fully validated new create/fork result failing before session publication retains exact rollback authority: close attempt-owned source borrows, then perform bounded ownership-consuming deletion. Do not reduce ADR 0364's known unpublished-failure cleanup to detach-only.
- Malformed non-authority metadata (for example Created profile/root/egress fields) does not erase safe attribution. For create, preserve the existing check at `internal/adapter/microvm/client.go:244-255`: expected owner, the unpredictable fresh request placement/session ID, and a complete canonical logical ref/generation. A matching well-formed AcquisitionID additionally permits terminal deletion on its originating socket; the daemon verifies that ID and tuple are precisely the new result created by that connection. If create's existing exact attribution is safe but its ID is absent/invalid, close the socket and attempt the existing bounded exact-ref rollback under the new zero-owner/zero-pin deletion gate. Cleanup refusal/error is reported as retained or uncertain, never as successful deletion.
- Fork labels are reusable: parent/owner/label similarity alone never authorizes deleting a returned sibling. Fork rollback requires a complete validated child tuple, distinct from the parent, and a well-formed AcquisitionID whose originating connection the daemon verifies created that exact child. A malformed result lacking that proof is not destructively cleaned up.
- Unknown, untrustworthy, truncated, or ambiguous transport outcomes without the above safe attribution close the connection and retain durable state. Resolve failure only releases its borrow; it never deletes the pre-existing placement. A syntactically valid ID alone grants no deletion authority. Preserve existing actionable retained/unknown-cleanup diagnostics and clean-delete/dirty-retention results; no extra cleanup service is introduced.

Only the acquisition connection accepts its `detach` or ownership-consuming deletion. Wrong connection, mismatched Binding, unknown/released ID, and old-daemon ID fail closed before touching another ref. Repeated local Close is `sync.Once`-idempotent with the first result cached. Release validates the live ID **and its originating connection**, never decrements ownership by ref alone, and does not replay a stale release against a replacement. The daemon removes released IDs; cryptographic freshness replaces any growing release-history cache. Wrong/stale ownership maps to existing `binding_mismatch`; an in-use delete is the sole new private error code. No new public error enum is necessary.

### Retained-socket framing

`ServeConn` currently starts a one-byte EOF probe (`environment/microvm/daemon.go:281-285`). **That probe must not run on retained acquisition sockets:** it would consume release framing. After authentication, one reader parses the initial acquisition frame and then reads only a terminal frame or EOF through the same bounded codec. Acquisition work may run while that reader waits, but no second goroutine reads this socket. This is one acquisition plus one terminal exchange, not multiplexing; ordinary one-shot workspace/exec/management sockets keep their existing framing and cancellation behavior.

- **Acquiring:** before the daemon has a validated result ready to reply, EOF cancels the attempt. Any second frame is a protocol violation, not an early delete: cancel the attempt, release any acquisition it created, and perform only safely attributable unpublished cleanup. Never dispatch that premature frame against a ref.
- **Replying:** publish the connection-owned acquisition and enter this state immediately before writing its success response. The sole reader may queue at most one terminal frame while that write completes, avoiding a race with a fast client that has already read the reply. Only after a successful response write may a fully matching terminal request run. Failed reply write/EOF retires the acquisition non-destructively unless independent safe unpublished attribution warrants rollback; the queued frame itself grants no authority after write failure.
- **Owned:** accept only matching `detach`, `delete`, or `child-delete`, then enter terminal processing and close after its result. Malformed/mismatched frames close and retire only this connection's acquisition without executing their requested mutation. EOF means ordinary release, never delete. A second acquisition/request stream is not accepted on this socket. The reader and worker terminate on socket close or daemon shutdown; no unbounded queue or extra cancellation graph is introduced.

### Ordering, cancellation and cleanup

1. Acquisition, owner retirement, destructive-delete admission, and final detach are serialized for the **exact ref**. Concurrent first resolves converge on one runtime registration but distinct acquisition IDs. Do not hold a daemon-wide lock across guest execution, source capture, or waiting for callers; unrelated PRs must progress independently.
2. A request with a live ID obtains an operation pin before exposing the Workspace/Runner. Retirement rejects new operations for that ID and waits for its existing pins before final unregister; it does **not** cancel them. Each one-shot operation connection already owns its cancellation. Owner-socket loss alone need not stop a still-live bounded operation; process death closes both sockets. Do not add acquisition-owned cancel functions, an operation-cancellation registry, or a second cancellation graph.
3. Ordinary Close sends detach with the existing bounded cleanup timeout, reads its result, and always closes the owner socket. Lost release replies still lead to EOF cleanup. Errors are returned, not logged as success; duplicated local Close cannot repeat a ref-global teardown.
4. Ctrl-C, SIGKILL, or an abandoned failed acquisition closes its sockets automatically. EOF retires the owner; operation pins drain through their existing operation-socket cancellation and completion. Final registration cleanup retains durable worktrees. The single framed reader above detects EOF during a slow acquisition and after success; no concurrent byte probe is permitted.
5. Known unpublished failure follows the rollback-authority boundary above. Owned deletion is terminal even on `in_use`: no destructive mutation occurs, but the caller always releases/closes its acquisition and reports the refusal; it cannot rely on a later Close callback after rollback is discarded. Admitted deletion keeps new acquisitions excluded through its outcome, retires the requesting acquisition on every result, and preserves existing dirty-retention/error state. Socket loss never retries deletion implicitly. Ambiguous outcomes retain durable state; no orphan-worktree collector is added.
6. Failed final guest unregister uses the runtime's existing registration-incarnation/pending-teardown mechanism (`repository_runtime.go:353-386,389-424`). The ref cannot publish a new live acquisition until teardown is reconciled; old cleanup cannot unregister a replacement incarnation. Report the failure. No new background reaper/lease timer is introduced. Normal EOF cleanup is automatic; a genuinely broken guest may require existing recovery, not silent success.
7. Daemon-wide shutdown keeps its existing cancellation and bounded runtime cleanup; it closes and joins retained connection handlers, without adding per-acquisition operation cancellation. **Harness restart:** a fresh Build has no cached binding; existing authorized load/use reattaches the exact durable ref and obtains a new acquisition. **Daemon restart while a harness remains alive:** owner sockets and IDs become invalid, cached bindings/current calls fail visibly, and no automatic reconnect, binding replacement or command replay occurs. Recovery uses existing explicit teardown and load/reattach paths, not a new reload seam. `CloseSession` drops that session's environment/source ownership (`service.go:2998-3063`); `LoadSession` authorizes and activates sources (`service.go:4112,4276-4313`), and subsequent placement use reattaches when no retained attachment remains (`placement.go:519-567`). Load alone does not evict an active cached binding shared with other sessions. If retained owners keep a dead binding cached, restart the affected harness to clear them; do not promise an unimplemented in-place refresh. Reattachment still requires the exact daemon to be ready and current authorization; failure never selects a substitute ref.

The daemon's connection handler, not a caller-supplied session ID or host PID, is the acquisition owner. `net.Conn` and exact-ref state suffice; no generic lease port is added.

The client acquired-binding value owns the socket, ID and idempotent Close. Replace
`Client.ownershipMu`/ownership counters and per-client final-detach arbitration; remove the
unused ref-only `Client.Detach(ctx, sess)` rather than retaining a global-detach API. Keep
Service and source-generation reference counts, which own separate resources. Explicit
administrative Delete retains its distinct semantics with in-use refusal. Reuse the existing
manager maps and runtime registration-incarnation cleanup, without a parallel repository
registry or new manager orchestration.

## In scope — 8 scenarios

### Scenario 1 — ordinary deployment-default selection converges on a ready local backend

This completed scenario follows
[ADR 0364 — readiness](../adr/0364-microvm-execution-environments.md#5-make-readiness-part-of-ordinary-use).

**Acceptance:**

- AC1.1: Selecting `microvm-local` as the trusted deployment default runs idempotent `EnsureReady` before MicroVM placement, while the host-local default does not start or probe microvmd. Service construction allocates no attachment, no-fs bypasses MicroVM readiness, and failed readiness persists nothing or falls back nowhere.
  - verify: `TestBareEmbeddedConfigResolvesOperatorExecutionSettings`, `TestBareEmbeddedHostLocalOmissionDoesNoMicroVMWork`, `TestMicroVMOperatorJourneyIsLazyIsolatedAndRestartExact`, `TestServiceConstructionDoesNotAllocatePlacement`, `TestPlacementReadinessFailurePersistsNothingAndDoesNotFallBack`
- AC1.2: Concurrent startup or first-use calls are serialized by the inter-process manager lock and converge on one compatible daemon and configuration.
  - verify: `TestMicroVMRedesign_Scenario1_EnsureReadyConvergesUnderManagerLock`
- AC1.3: A failed readiness attempt returns an actionable error without changing the configured deployment default; repeating ordinary use retries readiness.
  - verify: `TestMicroVMRedesign_Scenario1_ReadinessNeverRewritesDesiredConfig`
- AC1.4: The dedicated `--microvm` flag and obsolete `microvm init` and `microvm recover` commands are absent; `status`, `doctor`, and `delete` remain available and nonduplicative.
  - verify: `TestMecatedMicroVMHelpAllMatchesAdvertisedTopLevelGuidance`
- AC1.5: With no MicroVM profile selected, host-local and no-FS placement behavior and catalog profiles remain unchanged, and MicroVM dependencies stay out of the root module, engine module, and default binaries.
  - verify: `TestBareEmbeddedHostLocalOmissionDoesNoMicroVMWork`, `TestHostLocalOmissionDoesNoMicroVMWork`, `TestNoFSCatalogProfile`, `TestADR_0224_MicroVMDependenciesStayOutOfEngineAndRoot`

### Scenario 2 — canonical repository identity owns one durable VM generation

This follows [ADR 0364 — singleton lifecycle](../adr/0364-microvm-execution-environments.md#2-use-one-durable-vmrootfs-record-per-operator-and-canonical-git-repository).

The daemon uses `(operator, canonical Git common directory)` as the only repository VM
key. This scenario establishes lifecycle identity and the rootfs singleton; it does not add
logical guest multiplexing, worktree routing, deletion UX, or retention policy.

**Acceptance:**

- AC2.1: Canonically equivalent checkouts for one local operator resolve to one repository identity and one VM generation, while a different Git common directory or operator resolves to a different durable registry entry.
  - verify: `TestMicroVMMVP_Scenario2_CanonicalRepositoryIdentitySelectsSingletonVM`
- AC2.2: Concurrent first use and concurrent restart recovery converge on one stable placement generation and one rootfs. A healthy current-daemon boot is reused. Daemon restart or cold-host-reboot equivalent reconciles the prior exact launch owner, rotates boot authority, and starts one replacement boot around retained artifacts and mutable state without changing logical refs. Missing, corrupt, policy-incompatible, pending, or uncertain state fails without empty replacement, deletion, or host fallback.
  - verify: `TestMicroVMMVP_Scenario2_DurableSingletonRegistryReattachesOrFailsLoudly`, `TestRepositoryRecoveryPreservesExactPlacementAndMutableState`, `TestRepositoryRecoveryConcurrentEnsureStartsOneBoot`, `TestRepositoryRecoveryRejectsPolicyDriftWithoutMutatingState`, `TestRepositoryRecoveryRejectsCorruptRetainedArtifact`, `TestRepositoryRecoveryRetriesFailedReplacementBoot`, `TestLaunchOwnershipReconcileTerminatesOnlyExactOwnedRunner`, `TestLaunchOwnershipFreeLockRecoversAcrossBootIDChange`
- AC2.3: Repository-controlled names, symlinks, linked-worktree metadata, and hostile path components cannot collide registry identities or escape the owner-scoped state root.
  - verify: `TestMicroVMMVP_Scenario2_RepositoryIdentityIsCanonicalAndConfined`

### Scenario 3 — logical worktrees are authenticated and confined

This follows [the logical-routing architecture](../architecture/microvm-environments.md#logical-worktree-routing).

The daemon creates worktrees and registers their logical roots with the already-running
repository guest. Filesystem and exec remain bound to one `EnvironmentRef` and one root.

**Acceptance:**

- AC3.1: Two logical environments in one repository receive distinct daemon-created worktrees, branches, indexes, logical `EnvironmentRef`s, and guest roots while naming the same repository VM generation.
  - verify: `TestMicroVMMVP_Scenario3_LogicalEnvironmentsShareVMNotWorktree`
- AC3.2: A newly accepted vsock/Unix peer proves possession of the repository boot authority before the host sends any binding capability, registration, request, or secret; the challenge binds owner, repository/VM identity, VM generation, and control/data channel purpose, so connection order grants no identity. Guest registration, handshake, and every filesystem or exec request then authenticate the logical `EnvironmentRef` and assigned root before dispatch.
  - verify: `TestRepositoryGuestAuthenticationRejectsCompetingConnectorBeforeDisclosure`, `TestMicroVMMVP_Scenario3_EnvironmentRefAuthenticatesAssignedRoot`
- AC3.3: Stale, replayed, wrong-owner, wrong-generation, sibling-ref, path-escape, and cross-worktree protocol requests fail closed; Workspace and CommandRunner remain affined to the same assigned root/cwd with no host fallback. Git worktrees isolate working/index state and logical RPC routing, not mutually hostile same-repository Bash: arbitrary Bash may address sibling guest paths. Different repositories remain VM-isolated and host/other-repository paths remain unavailable.
  - verify: `TestInvariant_microvm_logical_environment_is_confined_and_affined`
- AC3.4: Host repository capture is fail-bounded before logical-placement registration: no more than two captures run per daemon process; one initial capture cumulatively admits 256 MiB across Git listings, tracked/untracked content, staged/unstaged binary patches, and committed archive plus 100,000 tracked/untracked/archive entry records; each verification scan has the same ceilings. Potentially large data streams through owner-private temporary files, tar extraction copies only validated regular-entry sizes, and limit, cancellation, or validation failure removes temporary files and provisional worktree state while returning the stable actionable `source capture limit exceeded` error.
  - verify: `TestSourceCaptureLimitsAndCleanup`, `TestSourceCaptureVerificationLimitCleansProvisionalWorktree`, `TestSourceCaptureCancellationCleansTemporaryState`, `TestSourceCaptureConcurrencyIsProcessBounded`, `TestMicroVMEnvironments_Scenario3_SourceStateCaptureIsExactOrFails`

### Scenario 4 — immutable Brood bytes and one explicit repository rootfs

This follows [ADR 0364 — direct Brood consumption](../adr/0364-microvm-execution-environments.md#4-consume-brood-directly-and-provide-an-explicit-linux-guest).

The artifact and static guest-contract work in tasks 60–61 remains complete. The remaining
MVP step attaches that primitive to the repository generation.

**Acceptance:**

- AC4.1: Controlled release/admission resolves Brood base `latest` to an immutable platform manifest digest, records the mutable discovery reference and resolution evidence, and admits runtime use only under a valid mecatl downstream endorsement.
  - verify: `TestMicroVMRedesign_Scenario4_BroodLatestIsDiscoveryOnly`
- AC4.2: Runtime boots the admitted Brood platform bytes directly without a Brood rebuild or derived guest-tools image, and a changed resolution, wrong platform, missing endorsement, stale policy, or corrupted subject fails before VM execution.
  - verify: `TestMicroVMRedesign_Scenario4_RuntimeConsumesOnlyEndorsedBroodDigest`
- AC4.3: One materializer is owned by each repository VM generation; the independently verified guest agent is injected into that private rootfs, and creating sessions or children performs no additional rootfs clone or copy.
  - verify: `TestMicroVMMVP_Scenario4_RepositoryGenerationOwnsSingleRootFS`
- AC4.4: Guest setup explicitly establishes workload UID/GID 65532, HOME, PATH, default workdir, writable home, and declared package/tool caches independent of Brood's image command or entrypoint.
  - verify: `TestMicroVMRedesign_Scenario4_GuestRuntimeContractIsExplicit`
- AC4.5: Runtime Sigstore verification uses `toolhive-core/container/verifier` in-process and production configuration exposes no cosign executable/path, verification subprocess, or verification temporary-file protocol.
  - verify: `TestMicroVMRedesign_Scenario4_SigstoreVerificationIsInProcess`

### Scenario 5 — sessions, schedules, and delegation attach to repository-scoped execution

This follows [the attachment architecture](../architecture/microvm-environments.md#session-and-delegation-attachment).

The existing Environment forker/merger behavior is sufficient for the MVP. No new
cross-process merge coordinator or crash-durable merge journal is required.

**Acceptance:**

- AC5.1: Sessions in one repository attach to the same repository VM with distinct logical refs and worktrees; a direct-write child reuses its parent's logical Environment, while a read-only Subagent, Parallel branch, or Team member receives a distinct logical ref and worktree in that VM.
  - verify: `TestMicroVMOperatorJourneyIsLazyIsolatedAndRestartExact`, `TestMicroVMMVP_Scenario5_SessionsAndChildrenReuseRepositoryVM`
- AC5.2: A Service retains and shares its exact binding across its consumers; microvmd separately retains the logical guest registration across all live acquisition connections from all local harness processes. Closing a session, source, fire, child, reader, or whole harness releases only its owned acquisitions. Final cleanup detaches guest/process handles exactly once without destroying durable worktrees, the repository VM/rootfs, or sibling registrations. Same-session writer lease semantics are unchanged.
  - verify: `TestMicroVMDefaultPlacementRetainsOneExactAttachmentUntilCloseSession`, `TestPlacementRepeatedRunsAndShutdownShareOneOwner`, `TestPlacementBorrowersDelayDetachUntilLastRelease`, `TestMicroVMMVP_Scenario5_CloseDetachesWithoutDestroyingRepositoryVM`, `TestMicroVMTwoBuildsSeparatePlacementsSurvivePeerClose`, `TestMicroVMCrossBuildScheduleClosePreservesOrigin`
- AC5.2a: A logical placement provisioned for an unpublished session is exact-generation deleted when validation, factory construction, capacity admission, collision handling, or persistence fails. Successful persistence transfers ownership and disables rollback. Dirty or failed cleanup remains durably discoverable for retry; rollback never deletes the repository VM, rootfs, or sibling placements and does not change EndSession or DeleteSession policy.
  - verify: `TestFailedCreateRollsBackUnpublishedPlacement`, `TestFailedFactoryCreateRollsBackUnpublishedPlacement`, `TestCapacityFailurePrecedesPlacementProvisioning`, `TestCollisionAfterProvisioningRollsBackUnpublishedPlacement`, `TestRollbackFailureRetainsExplicitRecoveryDiagnostic`, `TestSuccessfulCreateNeverRollsBack`
- AC5.3: The existing isolated-child merge path applies a non-conflicting child change and preserves the child on conflict; the MVP makes no cross-process serialization or crash-recovery claim.
  - verify: `TestMicroVMMVP_Scenario5_BasicExistingMergeBehavior`
- AC5.4: Production status inventories repository logical attachments—not the superseded session-per-VM registry—in deterministic pages of at most 64, showing two distinct logical worktrees on one repository generation; exact logical deletion removes clean state, retains dirty state for recovery, and never implies repository-VM deletion.
  - verify: `TestRepositoryProductionInventoryPaginationAndLogicalDelete`
- AC5.5: Schedule creation durably pins one exact placement: an origin-backed schedule borrows its session's logical worktree, while an independent MicroVM schedule provisions and owns one logical worktree. Updates cannot change placement or ownership, and every fire exactly reauthorizes the persisted ref without following the current default, including after a harness restart while microvmd remains live. Before the first claim, deletion atomically disables the schedule and cleans only its owned placement, retaining dirty state; cleanup or completion failure leaves a restart-safe tombstone retried against the same ref. The first atomic claim hands placement lifetime to the persisted fire-session lineage, so later deletion removes only the schedule record and retains the worktree for historical or resumable fires. Active fires block deletion, and borrowed, no-FS, host-local, and legacy-ambiguous placements are never destructively cleaned up; no path deletes the repository VM, rootfs, origin session, or sibling worktrees.
  - verify: `TestADR_0291_ScheduleResolvesSelectorBeforePersistingExactEnvironmentRef`, `TestInvariant_scheduled_placement_is_reauthorized_at_fire`, `TestScheduleIndependentPlacementAllocatedOnceAndCleaned`, `TestScheduleClaimHandsPlacementToFireSession`, `TestScheduleCleanupFailureRetainsDisabledExactPlacement`, `TestScheduleLegacyAndNoFSPlacementsAreNeverDeleted`, `TestScheduleDeleteDisablesBeforeActiveFireCleanup`, `TestScheduleDeleteUsesAtomicBeginRecordForClaimHandoff`, `TestScheduleDeleteCompletionFailureLeavesRetryableTombstone`, `TestScheduleCreatePersistenceFailureRollsBackOwnedPlacement`
- AC5.6: MicroVM composition registers, but does not select, a `repository` instruction and command source. A selected, independently trusted and admitted source borrows only its session's exact guest workspace as a read-only, runner-less, mutation-less, and execution-read-ledger-free view. It retains that source binding across delegated children and successors, releases only its own borrow on cancellation or retirement, and reauthorizes the persisted exact ref after schedule or harness restart. Independent sources acquire no guest workspace, and a required repository source fails rather than using a host checkout, another session, or no-FS placement.
  - verify: `TestMicroVMIndependentContextIgnoresGuestAndSurvivesUnavailableExecution`, `TestMicroVMCancelledUnpublishedSourceReleasesOnlyAttempt`, `TestMicroVMChildRetainsParentSourceAfterRetirement`, `TestMicroVMSuccessorRetainsPlacementAndContext`, `TestMicroVMSelectedScheduleAndRestartReauthorizeContext`, `TestMicroVMRepositorySelectionDoesNotGrantProjectAdmission`, `TestMicroVMRequiredRepositorySourceFailsForNoFS`, `TestMicroVMSelectedContextFreshnessAndReadEvidence`

- AC5.7: Independent Builds on the same checkout and on different linked host Git worktrees create distinct logical refs sharing one repository VM; their conflicting dirty files/instruction markers remain distinct. Closing either Build leaves the other's model-visible selected context, workspace and command runner usable. Cross-instance shared-ref schedule/command-discovery cleanup cannot revoke the originating execution owner.
  - verify: `TestMicroVMTwoBuildsSeparatePlacementsSurvivePeerClose` (same-checkout/linked-worktree table, including the cross-Build command-reader release subcase); `TestMicroVMCrossBuildScheduleClosePreservesOrigin`
- AC5.8: Duplicate local release, stale/wrong-owner/wrong-ref IDs, simultaneous acquire/final-release, and failed/cancelled acquisition do not release a different holder or replacement. Safe attribution preserves known unpublished rollback despite malformed non-authority metadata; ambiguous outcomes retain durable state. Destructive deletion requires atomic sole-owner/zero-pin admission, excludes new acquisitions until completion, and retires the caller on every terminal result, including `in_use`, dirty retention and errors, without deleting another live holder's work.
  - verify: `TestMicroVMAcquisitionOwnershipIsExactAndIdempotent` (table and deterministic interleavings); `TestMicroVMAcquisitionCancellationAndLostRepliesReleaseOnlyOwner`; `TestMicroVMDeleteRejectsLiveOtherAcquisitions`
- AC5.9: Normal close/owner-socket loss retires acquisition ownership; existing operation pins drain under operation-socket cancellation, then final registration/handler resources are reclaimed. Harness crash retains durable worktrees; a fresh harness's authorized load/use reattaches the exact ref. Daemon restart invalidates cached bindings and current calls fail; recovery requires existing explicit teardown/load/reattach or harness restart, never transparent replacement or replay. Pending final teardown cannot unregister a newly acquired replacement.
  - verify: `TestMicroVMOwnerDisconnectReclaimsRegistration`; `TestMicroVMOwnershipRestartPreservesExactWorktree`; `TestMicroVMFinalDetachFailureCannotCloseReplacement`
- AC5.10: v3 and malformed ownership requests are rejected without performing the requested mutation or releasing another acquisition. A malformed retained connection retires only its own acquisition through ordinary release. Multiple local harnesses require no new flags/configuration, while a daemon compatibility mismatch does not automatically stop a serving peer's runtime.
  - verify: `TestMicroVMLifecycleV4RejectsUnsafeLegacyOwnership`, `TestEnsureReadyReusesOnlyCompatibleDaemonWithoutRestart`, `TestEnsureReadyRecoversCompatibleStoppedDaemonAndRefusesUnsafeRuntimeBranches`, `TestEnsureReadyPreservesExistingConfigurationOnMismatch`, `TestMicroVMRedesign_Scenario1_EnsureReadyConvergesUnderManagerLock`, `TestMicroVMRedesign_Scenario1_ReadinessNeverRewritesDesiredConfig`

**Proof boundary and implementation order:**

1. Add the **two-Build distinct-placement** and **cross-instance origin-backed schedule** failing regressions first. Both Builds use separate production MicroVM Clients, a shared real JSONL store/lease directory where appropriate, and the same real `RuntimeDaemon`/`RepositoryAttachmentManager`/guest registration path. Substitute only VM/rootfs/network launch with test-owned offline fixtures. The fake must not implement Resolve/Detach/owner counts itself. Reuse exported production composition and existing `repository_composition_test.go` fixture patterns; keep host runtime dependencies out of root production imports/default binaries.
2. In the schedule proof, A is the elected scheduler leader; B creates the origin-backed schedule through the real service. The read-only fire completes and its session is closed before B performs another workspace operation and model request containing its selected instruction/command marker. Controlled clocks/channels, not sleeps, establish the ordering. Closing B must eventually release the final registration too; retaining everything is not a passing implementation.
3. Add command-reader safety as a subcase of the two-Build fixture: drive actual listing/expansion, retire B's source, then verify A's execution remains usable. Reuse existing shared child/direct-write/Parallel/Team, successor, no-FS, admission, ledger and adversarial-model proofs unchanged. No optional successor matrix or duplicated delegation suite.
4. Use table/subcases of the named daemon tests for single-reader framing (premature frame and fast release during reply), EOF/lost acquire/release reply, safe-vs-unsafe malformed-result rollback, atomic deletion/admission, `in_use` retirement, dirty/error outcomes and overlapping teardown. An operation-socket subcase proves owner retirement drains rather than cancels a still-live call. A small subprocess socket owner proves actual process-exit cleanup. Reuse test-owned offline composition; no new production fixture package, operator state, live VM/provider or broader matrix.
5. Then replace the private client/daemon exchange and wire all existing acquisitions through it. Public tools, schedule claims and SessionLease remain unchanged. Run focused module/client/app proofs and targeted race tests while iterating.

The new test names are required proofs, not claims that they exist or pass. Retained AC5.2
and AC5.6 proofs do not qualify cross-client ownership. Offline production composition is
not Linux KVM/Darwin HVF qualification; the separate platform requirements remain.
The [Environment and HarnessContext domain contracts](../architecture/mecatl.modelith.yaml)
and [shared source-lifetime criteria](harness-context.md) remain unchanged.

### Scenario 6 — platform ownership and useful networking are honest

This follows [ADR 0364's Linux guest decision](../adr/0364-microvm-execution-environments.md#4-consume-brood-directly-and-provide-an-explicit-linux-guest) and the proposed [Darwin xattr decision](../adr/0365-microvm-darwin-xattr-ownership.md#prepare-fixed-guest-ownership-with-virtiofs-xattrs).

Linux amd64 keeps its qualified namespace mechanism. Darwin arm64 keeps the same fixed guest identity through strict VirtioFS ownership preparation, but remains experimental until Scenario 8. The default network is useful rather than presented as containment; tightening remains optional and fail-closed when selected.

**Acceptance:**

- AC6.1: On Linux amd64, guest workload UID/GID 65532 maps to the daemon user through an unprivileged user namespace, model commands run unprivileged, and session or child worktrees are never widened to world-readable, world-writable, or world-traversable modes.
  - verify: `TestMicroVMMVP_Scenario6_LinuxUserNamespaceAvoidsWorldModeWidening`, `TestRepositoryOwnershipLinuxIsNoOp`
- AC6.1a: On Darwin arm64, both read-write logical roots and read-only object snapshots use strict go-microvm ownership preparation for fixed guest UID/GID 65532. Rootfs home/workspace and logical subtrees are prepared before publication or guest registration. Object files are copied privately, prepared, then sealed to mode `0400`.
  - verify: `TestRepositoryMountsDarwinRequireStrictFixedGuestOwnership`, `TestRepositoryLogicalManagerPreparesEachLogicalRootBeforeRegister`, `TestRepositoryLogicalManagerOwnershipFailureRollsBackBeforeRegister`, `TestRepositoryObjectSnapshotRetainsPackedGitObjects`, `TestRepositoryRootFSMaterializer_ClonesStaticRootFSAndInjectsGuestAgentOnce`
- AC6.1b: Host-side merge-back applies the Git patch and then refreshes upstream ownership preparation. New inodes retain host-derived modes, guest chmod changes survive when represented by the patch, and a preparation failure reports that the patch was already applied. This is not transactional with concurrent Bash and promises no cache invalidation.
  - verify: `TestMicroVMMVP_Scenario5_BasicExistingMergeBehavior`, `TestRepositoryMergeReportsOwnershipRefreshFailureAfterPatchApplied`
- AC6.2: The built-in hosted profile provides unrestricted IPv4 guest egress by default and reports that the IPv6 stack remains enabled while external IPv6 is unrouted and unsupported; optional deny-all or allowlist tightening filters IPv4, disables IPv6, and aborts readiness rather than falling back when either enforcement step fails.
  - verify: `TestMicroVMMVP_Scenario6_PermissiveIPv4WithOptionalFailClosedTightening`

### Scenario 7 — one Linux amd64 journey proves the MVP

This follows [the required live journey](../architecture/microvm-environments.md#required-live-journey-and-limits).

The automated `task e2e:microvm` proof is intentionally narrow and deterministic: it uses the
mock LLM provider while crossing production Linux amd64 KVM placement and guest boundaries. It
never requires or reads `OPENROUTER_API_KEY` and is suitable for the opt-in KVM CI job.

A separate manual qualification was executed on 2026-09-10 with OpenRouter
`openai/gpt-5-mini`. The reproducible procedure was:

1. Start from a verified release-stamped `mecated` in a disposable Git checkout on a Linux amd64
   host where the invoking user can open `/dev/kvm`; use private, newly created XDG state, data,
   config, and runtime directories.
2. Supply `OPENROUTER_API_KEY` only in the server process environment (never a command argument,
   file, trace, or captured output), select `microvm-local`, and run `mecated microvm doctor` before
   starting the headless HTTP server.
3. Through the documented public HTTP API, create one session and prompt the model to use Write,
   Read, and Bash to create and read a unique marker and report `id -u`. Confirm the terminal result
   reports UID 65532, then confirm the source checkout does not contain the marker.
4. Stop only `mecated`, leave microvmd running, restart `mecated` with the same private XDG roots,
   and prompt the same public session ID to read the marker. Confirm exact session continuation.
5. Run `mecated microvm doctor` and `mecated microvm status`; confirm healthy output. Copy no status
   row into evidence because it contains private attachment/ref/path data. End the server, unset the
   credential, and remove the disposable state through the operator-controlled cleanup procedure.

The observed outcomes were successful Write/Read/Bash in the Wolfi guest, UID 65532, source-checkout
isolation, harness-restart reattachment while microvmd remained alive, and healthy doctor/status.
No credential, private placement ref, socket, or host path was retained. This manual trace is not an
automated gate and is not evidence for microvmd restart recovery.

**Acceptance:**

- AC7.1: Linux amd64 KVM enters through production profile/session composition and proves ordinary first use, direct admitted Brood consumption with in-process verification, one guest-backed session, confined filesystem/Bash execution, and source-checkout isolation. Deterministic production-composition tests separately prove two-session logical-worktree isolation, exact harness-restart reattachment, microvmd restart recovery around retained state, and direct-write versus isolated-child routing. Repository-VM/rootfs singleton, networking, close-detach, and merge/conflict behavior remain proven by their focused AC2–AC6 tests; the live journey does not overclaim those observations.
  - verify: `task e2e:microvm` runs `TestMicroVMDefaultPlacementDailyHarnessJourney` plus `TestMicroVMOperatorJourneyIsLazyIsolatedAndRestartExact`; focused evidence is listed by AC2–AC6
- AC7.2: Concise architecture, operator, and public documentation describes profile selection, the repository sharing boundary, distinct worktrees, direct Brood admission, Linux ownership, permissive networking, optional tightening, automatic restart recovery, fail-closed uncertainty, and the experimental Darwin code path without claiming native or released qualification.
  - verify: inspection — `task docs` and `task site:build` prove the linked documentation surfaces build

### Scenario 8 — Darwin arm64 is implemented but not yet qualified

This follows the proposed [Darwin ownership decision](../adr/0365-microvm-darwin-xattr-ownership.md).

The Darwin code path uses go-microvm v0.0.41 runtime and firmware artifacts pinned by SHA-256, repository registry version 1, fixed guest UID/GID 65532 ownership xattrs, and a direct-child launch supervisor. Normal admission requires Apple Silicon, macOS 15 or newer, and Hypervisor.framework. Linux arm64 remains rejected.

**Acceptance:**

- AC8.1: The Darwin launch-owner helper is the runner's direct parent and is the sole owner of nonblocking `Wait4`, TERM, and KILL. Daemon-pipe EOF stops and reaps the runner. The runner inherits the attempt flock, so supervisor death cannot permit replacement; reconciliation never signals a stored PID and returns an operator-recovery-required error while a historical lock remains busy. Linux keeps its pidfd implementation.
  - verify: `TestDarwinLaunchOwnerSupervisorStopsDirectRunner`, `TestDarwinDirectChildStopEscalatesAfterIgnoredTERM`, `TestDarwinDirectChildStopsOnControlEOF`, `TestDarwinKilledSupervisorLeavesRunnerLockHeld`, `TestDarwinHistoricalBusyAttemptNeverSignalsStoredPID`, plus the existing Linux launch-ownership suite
- AC8.2: Manager and daemon service ownership use the shared Unix flock lifecycle on Linux and Darwin. Darwin manager stop authenticates the owner-only control peer, matches the exact serving daemon identity, receives a shutdown acknowledgement before graceful cancellation, and never signals a PID. Linux retains pidfd signaling. Platform admission accepts only Linux amd64 or Darwin arm64; Darwin preflight requires macOS 15+ and Hypervisor.framework. Development descriptors and preparation scripts select the current supported host, and Linux arm64 remains outside normal admission.
  - verify: `TestDarwinDaemonOwnershipHeldTracksServiceLock`, `TestDaemonOwnershipPrecedesSocketMutationAndIsLifetimeExclusive`, `TestDaemonShutdownAcknowledgesBeforeGracefulCancellation`, `TestRuntimeDaemonShutdownAcknowledgementCancelsServer`, `TestDaemonShutdownRejectsUnknownAndWrongIdentity`, `TestDaemonShutdownRejectsUnauthorizedPeer`, `TestShutdownDaemonSendsExactIdentityAndRequiresAcknowledgement`, `TestShutdownDaemonPropagatesServerFailureAndRejectsMalformedAck`, `TestDarwinStopUsesAuthenticatedControlWithoutPIDSignal`, `TestMicroVMLivePlatformBoundary`, `TestDarwinPreflightRequiresMacOS15AndHVF`, `TestDevelopmentReleaseDescriptorIsStrictAndLocal`
- AC8.3: A native non-root Apple Silicon host runs the real VM through the production-composed harness with the offline scripted provider. The trace proves guest UID 65532, Read/Write/Shell in the logical worktree, private home/cache writes, read-only Git objects, source and host-path isolation, prepared post-boot children, non-conflicting merge, conflict preservation, guest mode retention, graceful microvmd stop/restart, and exact reattachment. It records the source revision and development bundle digest but no credential, private path, socket, placement ref, or capability.
  - verify: `TestMicroVMDefaultPlacementDailyHarnessJourney`
  - `task e2e:microvm` executes the test on Darwin arm64. Physical Apple Silicon execution remains pending, and compile-only evidence does not satisfy this criterion.
- AC8.4: Release qualification exercises packaging, provenance, signing, embedded defaults, download, verification, installation, and the complete physical Apple Silicon journey before Darwin is described as released support.
  - verify: pending non-publishing signed-candidate execution and human review. The macOS CI job is configured to run the complete nested module with `-race`, but this unpushed change has no CI result.

Use this reproducible native development check from a clean checkout on a non-root Apple Silicon macOS 15+ host. It requires network access only to prepare the pinned VM artifacts; the agent run uses the offline scripted provider and no paid model:

```sh
task microvm:dev:prepare
task microvm:dev:build
QUAL_ROOT="$(pwd)/.scratch/microvm-darwin-qualification"
mkdir -p "$QUAL_ROOT/state" "$QUAL_ROOT/config" "$QUAL_ROOT/runtime"
cat >"$QUAL_ROOT/mock.json" <<'JSON'
{"turns":[
  {"tool_calls":[{"id":"shell-uid","name":"Shell","args":{"command":"id -u && pwd"}}]},
  {"tool_calls":[{"id":"write-proof","name":"Write","args":{"path":"darwin-vm-proof.txt","content":"darwin microvm proof\n"}}]},
  {"tool_calls":[{"id":"read-proof","name":"Read","args":{"path":"darwin-vm-proof.txt"}}]},
  {"text":"offline Darwin microVM check complete"}
]}
JSON
export XDG_STATE_HOME="$QUAL_ROOT/state"
export XDG_CONFIG_HOME="$QUAL_ROOT/config"
export XDG_RUNTIME_DIR="$QUAL_ROOT/runtime"
.scratch/microvm-dev/bin/mecated serve --headless --posture auto \
  --store-dir="$QUAL_ROOT/sessions" \
  --default-placement microvm-local \
  --mock-script="$QUAL_ROOT/mock.json" \
  --microvm-dev-release="$(pwd)/.scratch/microvm-dev/darwin-arm64/release.json" \
  --microvm-dev-acknowledge-untrusted-local-artifacts
```

In a second terminal, create and drive the session through the public API. Set the same
private XDG roots for local administration:

```sh
QUAL_ROOT="$(pwd)/.scratch/microvm-darwin-qualification"
export XDG_STATE_HOME="$QUAL_ROOT/state"
export XDG_CONFIG_HOME="$QUAL_ROOT/config"
export XDG_RUNTIME_DIR="$QUAL_ROOT/runtime"
curl -sS -X POST http://127.0.0.1:8081/v1/sessions \
  -H 'Content-Type: application/json' -d '{}'
SESSION_ID=copy-from-create-response
curl -sS -N -X POST \
  "http://127.0.0.1:8081/v1/sessions/${SESSION_ID}/prompt" \
  -H 'Content-Type: application/json' \
  -d '{"text":"Run the scripted offline Darwin microVM qualification."}'
test ! -e darwin-vm-proof.txt
.scratch/microvm-dev/bin/mecated microvm doctor
.scratch/microvm-dev/bin/mecated microvm status
```

Stop the server, replace the mock script at `$QUAL_ROOT/mock.json` with a Read-only restart
script, and restart the same command:

```sh
cat >"$QUAL_ROOT/mock.json" <<'JSON'
{"turns":[
  {"tool_calls":[{"id":"read-after-restart","name":"Read","args":{"path":"darwin-vm-proof.txt"}}]},
  {"text":"offline Darwin microVM restart check complete"}
]}
JSON
```

Restart only `mecated serve` with the same XDG roots and store directory, then prompt the same
`SESSION_ID`. Retain a redacted result only after confirming UID 65532, the guest worktree
marker, source isolation, exact session continuation, and healthy doctor/status. A
supervisor-death orphan is a separate fail-closed case requiring operator recovery, not an
automatic-restart claim.

## Out of scope — explicitly deferred

- repository-VM deletion UX and sophisticated retention policy;
- crash-durable merge recovery and cross-process/multi-client merge proofs;
- released macOS support and native Apple Silicon plus signed-candidate qualification;
- Linux arm64 live support;
- upstream Brood signing and independent artifact/config refresh channels;
- per-session fairness, quotas, dashboards, and exhaustive cache-poisoning controls;
- durable acquisition leases, heartbeats, TTLs, PID watchers, or new storage fencing;
- a one-harness-per-daemon limit, per-terminal VMs, or registrations retained until daemon exit;
- crash-orphan worktree collection, new cleanup commands/configuration, or transparent command replay;
- new source-selection policy, trust tiers, public placement selectors, or engine abstractions.

Non-Git environments, remote/multi-user microvmd, cross-principal VM sharing,
unified host+guest egress containment, and moving provider/MCP credentials into the guest
also remain out of scope.

## Cross-cutting deliverables

- Historical tasks 1–58 and completed tasks 59–65 remain intact.
- The durable registry fails closed on inconsistent restart state; it does not silently
  recreate, delete, or reconcile an orphan.
- Default, no-fs, and engine-standalone gates preserve the opt-in module boundary.
- Every amended AC and its exact `verify:` line must be covered by the regenerated run-local
  brief before fresh implementation dispatch; historical task completion does not qualify it.
- After implementation, update only the owning operator guide
  (`user-docs/building/deployment/microvm-environments.md`) and architecture page for the
  supported multi-terminal workflow, non-transactional shared-worktree writes and unchanged
  same-conversation SessionLease contention. Until then, those pages describe current
  behavior and preserve the source-selection documentation; approval is not implementation.

## Definition of done

1. Tasks 62–65 and every acceptance criterion above are complete.
2. `task lint`, `task test`, `task api:check`, and nested microVM module gates pass.
3. `task docs`, `task site:build`, and `task ac-trace-strict` pass.
4. `go run ./cmd/mecademo` still prints a complete offline session.
5. Linux amd64 KVM retains its qualified evidence. Darwin remains experimental until AC8.3 and AC8.4 have native and release evidence.
6. PR 580 remains the sole delivery. The separate plan checkpoint waiver does not waive tests, security review, or human merge authority.

## Exit criteria

The definition of done holds on the accumulator; humans retain merge authority.
