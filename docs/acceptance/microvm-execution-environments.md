# MicroVM execution environments — acceptance plan

**Phase:** Repository-scoped local microVM execution, including the experimental Darwin arm64 path
**Status:** in-progress
**Qualification:** Linux amd64 qualification landed. Darwin arm64 implementation is present in PR 580 and awaits native and release qualification.
**Evidence:** Tasks 59–65 and deterministic offline Linux coverage are complete. `task e2e:microvm` is the automated Linux amd64 KVM gate and uses the deterministic in-process mock provider; it does not contact OpenRouter and is not the evidence for the live-provider claim. A separate manual qualification trace was executed on 2026-09-10 with Linux amd64 KVM and OpenRouter `openai/gpt-5-mini` through the public HTTP create and prompt APIs. Normal Write, Read, and Bash ran in the Wolfi guest as UID 65532; the proof marker existed only in the MicroVM logical worktree. The same session reattached after mecated restarted while microvmd remained alive, and `microvm doctor` and `microvm status` both reported healthy. This does not claim recovery after a microvmd restart. No credential, exact private placement ref, socket, or host path from that trace is retained in this document. Darwin unit, ownership-xattr, and lifecycle tests have only been cross-compiled locally on Linux; the unpushed macOS CI change has no result, and no physical Apple Silicon VM journey has run.
**Issue:** [stacklok/mecatl#526](https://github.com/stacklok/mecatl/issues/526)
**Delivery:** PR 580 is the sole delivery. The operator waived the separate plan checkpoint for this same-PR completion; tests, security review, and human merge authority remain required.
**ADR:** [ADR 0350](../adr/0350-microvm-execution-environments.md), with the proposed Darwin decision in [ADR 0351](../adr/0351-microvm-darwin-xattr-ownership.md)
**Accumulator branch:** `acc/microvm-execution-environments`

The MVP uses one mutable microVM and one rootfs for one local operator and one canonical
Git repository. Sessions, schedules, and isolated children reuse that repository VM but receive
separate daemon-created worktrees and authenticated logical `EnvironmentRef`s. A schedule
created from a session borrows its exact worktree; an independent schedule owns one logical
worktree reused by every fire. This is a
small local, single-user, Git-only capability: it deliberately does not solve fleet
management, comprehensive lifecycle automation, or cross-process merge coordination.

Tasks 59–65 preserve their completed behavior and acceptance criteria and replace
all earlier superseded redesign tasks.

## In scope — 8 scenarios

### Scenario 1 — ordinary deployment-default selection converges on a ready local backend

This completed scenario follows
[ADR 0350 — readiness](../adr/0350-microvm-execution-environments.md#5-make-readiness-part-of-ordinary-use).

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

This follows [ADR 0350 — singleton lifecycle](../adr/0350-microvm-execution-environments.md#2-use-one-durable-vmrootfs-record-per-operator-and-canonical-git-repository).

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

This follows [ADR 0350 — direct Brood consumption](../adr/0350-microvm-execution-environments.md#4-consume-brood-directly-and-provide-an-explicit-linux-guest).

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
- AC5.2: Mecated owns one exact attached binding per live placement generation and shares it across runs, discovery, ACP, and team borrowers. Closing a session or child releases that ownership only after the final borrower exits and detaches its process-local handles exactly once, without destroying the repository VM, rootfs, shared cache, or another attached logical environment. Repeated runs do not mint attachment owners; service shutdown drains the same ownership registry.
  - verify: `TestMicroVMDefaultPlacementRetainsOneExactAttachmentUntilCloseSession`, `TestPlacementRepeatedRunsAndShutdownShareOneOwner`, `TestPlacementBorrowersDelayDetachUntilLastRelease`, `TestMicroVMMVP_Scenario5_CloseDetachesWithoutDestroyingRepositoryVM`
- AC5.2a: A logical placement provisioned for an unpublished session is exact-generation deleted when validation, factory construction, capacity admission, collision handling, or persistence fails. Successful persistence transfers ownership and disables rollback. Dirty or failed cleanup remains durably discoverable for retry; rollback never deletes the repository VM, rootfs, or sibling placements and does not change EndSession or DeleteSession policy.
  - verify: `TestFailedCreateRollsBackUnpublishedPlacement`, `TestFailedFactoryCreateRollsBackUnpublishedPlacement`, `TestCapacityFailurePrecedesPlacementProvisioning`, `TestCollisionAfterProvisioningRollsBackUnpublishedPlacement`, `TestRollbackFailureRetainsExplicitRecoveryDiagnostic`, `TestSuccessfulCreateNeverRollsBack`
- AC5.3: The existing isolated-child merge path applies a non-conflicting child change and preserves the child on conflict; the MVP makes no cross-process serialization or crash-recovery claim.
  - verify: `TestMicroVMMVP_Scenario5_BasicExistingMergeBehavior`
- AC5.4: Production status inventories repository logical attachments—not the superseded session-per-VM registry—in deterministic pages of at most 64, showing two distinct logical worktrees on one repository generation; exact logical deletion removes clean state, retains dirty state for recovery, and never implies repository-VM deletion.
  - verify: `TestRepositoryProductionInventoryPaginationAndLogicalDelete`
- AC5.5: Schedule creation durably pins one exact placement: an origin-backed schedule borrows its session's logical worktree, while an independent MicroVM schedule provisions and owns one logical worktree. Updates cannot change placement or ownership, and every fire exactly reauthorizes the persisted ref without following the current default, including after a harness restart while microvmd remains live. Before the first claim, deletion atomically disables the schedule and cleans only its owned placement, retaining dirty state; cleanup or completion failure leaves a restart-safe tombstone retried against the same ref. The first atomic claim hands placement lifetime to the persisted fire-session lineage, so later deletion removes only the schedule record and retains the worktree for historical or resumable fires. Active fires block deletion, and borrowed, no-FS, host-local, and legacy-ambiguous placements are never destructively cleaned up; no path deletes the repository VM, rootfs, origin session, or sibling worktrees.
  - verify: `TestADR_0291_ScheduleResolvesSelectorBeforePersistingExactEnvironmentRef`, `TestInvariant_scheduled_placement_is_reauthorized_at_fire`, `TestScheduleIndependentPlacementAllocatedOnceAndCleaned`, `TestScheduleClaimHandsPlacementToFireSession`, `TestScheduleCleanupFailureRetainsDisabledExactPlacement`, `TestScheduleLegacyAndNoFSPlacementsAreNeverDeleted`, `TestScheduleDeleteDisablesBeforeActiveFireCleanup`, `TestScheduleDeleteUsesAtomicBeginRecordForClaimHandoff`, `TestScheduleDeleteCompletionFailureLeavesRetryableTombstone`, `TestScheduleCreatePersistenceFailureRollsBackOwnedPlacement`

### Scenario 6 — platform ownership and useful networking are honest

This follows [ADR 0350's Linux guest decision](../adr/0350-microvm-execution-environments.md#4-consume-brood-directly-and-provide-an-explicit-linux-guest) and the proposed [Darwin xattr decision](../adr/0351-microvm-darwin-xattr-ownership.md#prepare-fixed-guest-ownership-with-virtiofs-xattrs).

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
- per-session fairness, quotas, dashboards, and exhaustive cache-poisoning controls.

Non-Git environments, remote/multi-user microvmd, cross-principal VM sharing,
unified host+guest egress containment, and moving provider/MCP credentials into the guest
also remain out of scope.

## Cross-cutting deliverables

- Historical tasks 1–58 and completed tasks 59–65 remain intact.
- The durable registry fails closed on inconsistent restart state; it does not silently
  recreate, delete, or reconcile an orphan.
- Default, no-fs, and engine-standalone gates preserve the opt-in module boundary.
- Every numbered AC is quoted with its exact `verify:` line in exactly one of tasks 62–65.

## Definition of done

1. Tasks 62–65 and every acceptance criterion above are complete.
2. `task lint`, `task test`, `task api:check`, and nested microVM module gates pass.
3. `task docs`, `task site:build`, and `task ac-trace-strict` pass.
4. `go run ./cmd/mecademo` still prints a complete offline session.
5. Linux amd64 KVM retains its qualified evidence. Darwin remains experimental until AC8.3 and AC8.4 have native and release evidence.
6. PR 580 remains the sole delivery. The separate plan checkpoint waiver does not waive tests, security review, or human merge authority.

## Exit criteria

The definition of done holds on the accumulator; humans retain merge authority.
