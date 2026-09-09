# MicroVM execution environments — acceptance plan

**Phase:** MVP rescope — repository-scoped local microVM execution
**Status:** landed
**Evidence:** Tasks 59–65 are complete. The required Linux amd64 KVM journey passes; all other live-platform claims remain deferred.
**Issue:** [stacklok/mecatl#526](https://github.com/stacklok/mecatl/issues/526)
**ADR:** [ADR 0326](../adr/0326-microvm-execution-environments.md)
**Accumulator branch:** `acc/microvm-execution-environments`

The MVP uses one mutable microVM and one rootfs for one local operator and one canonical
Git repository. Sessions and isolated children reuse that repository VM but receive
separate daemon-created worktrees and authenticated logical `EnvironmentRef`s. This is a
small local, single-user, Git-only capability: it deliberately does not solve fleet
management, comprehensive lifecycle automation, or cross-process merge coordination.

Tasks 59–65 preserve their completed behavior and acceptance criteria and replace
all earlier superseded redesign tasks.

## In scope — 7 scenarios

### Scenario 1 — ordinary deployment-default selection converges on a ready local backend

This completed scenario follows
[ADR 0326 — readiness](../adr/0326-microvm-execution-environments.md#5-make-readiness-part-of-ordinary-use).

**Acceptance:**

- AC1.1: Selecting `microvm-local` as the trusted deployment default runs idempotent `EnsureReady` before MicroVM placement, while the host-local default does not start or probe microvmd.
  - verify: `TestMicroVMDefaultPlacementUsesNormalCreateSessionAndExactReattach`
- AC1.2: Concurrent startup or first-use calls are serialized by the inter-process manager lock and converge on one compatible daemon and configuration.
  - verify: `TestMicroVMRedesign_Scenario1_EnsureReadyConvergesUnderManagerLock`
- AC1.3: A failed readiness attempt returns an actionable error without changing the configured deployment default; repeating ordinary use retries readiness.
  - verify: `TestMicroVMRedesign_Scenario1_ReadinessNeverRewritesDesiredConfig`
- AC1.4: The dedicated `--microvm` flag and required `microvm init` and `microvm recover` commands are absent; `status`, `doctor`, and `delete` remain available and nonduplicative.
  - verify: `TestMicroVMRedesign_Scenario1_ObsoleteActivationAndRecoverySurfaceIsRemoved`
- AC1.5: With no microVM profile selected, existing default and no-fs snapshots, catalogs, prompts, startup, and tool behavior remain byte-compatible and no microVM dependency enters the engine module.
  - verify: `TestInvariant_microvm_disabled_is_byte_compatible`

### Scenario 2 — canonical repository identity owns one durable VM generation

This follows [ADR 0326 — singleton lifecycle](../adr/0326-microvm-execution-environments.md#2-use-one-durable-vmrootfs-record-per-operator-and-canonical-git-repository).

The daemon uses `(operator, canonical Git common directory)` as the only repository VM
key. This scenario establishes lifecycle identity and the rootfs singleton; it does not add
logical guest multiplexing, worktree routing, deletion UX, or retention policy.

**Acceptance:**

- AC2.1: Canonically equivalent checkouts for one local operator resolve to one repository identity and one VM generation, while a different Git common directory or operator resolves to a different durable registry entry.
  - verify: `TestMicroVMMVP_Scenario2_CanonicalRepositoryIdentitySelectsSingletonVM`
- AC2.2: Concurrent first use durably converges on one VM/rootfs record for the canonical key; an exact healthy generation reattaches only while every owned dependency remains live in-process, while daemon restart or other missing/inconsistent runtime state fails loudly without minting a replacement and preserves the durable record/rootfs/worktrees.
  - verify: `TestMicroVMMVP_Scenario2_DurableSingletonRegistryReattachesOrFailsLoudly`
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

### Scenario 4 — immutable Brood bytes and one explicit repository rootfs

This follows [ADR 0326 — direct Brood consumption](../adr/0326-microvm-execution-environments.md#4-consume-brood-directly-and-provide-an-explicit-linux-guest).

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

### Scenario 5 — sessions and delegation attach to repository-scoped execution

This follows [the attachment architecture](../architecture/microvm-environments.md#session-and-delegation-attachment).

The existing Environment forker/merger behavior is sufficient for the MVP. No new
cross-process merge coordinator or crash-durable merge journal is required.

**Acceptance:**

- AC5.1: Sessions in one repository attach to the same repository VM with distinct logical refs and worktrees; a direct-write child reuses its parent's logical Environment, while a read-only Subagent, Parallel branch, or Team member receives a distinct logical ref and worktree in that VM.
  - verify: `TestMicroVMMVP_Scenario5_SessionsAndChildrenReuseRepositoryVM`
- AC5.2: Closing a session or child detaches its process-local handles without destroying the repository VM, rootfs, shared cache, or another attached logical environment.
  - verify: `TestMicroVMMVP_Scenario5_CloseDetachesWithoutDestroyingRepositoryVM`
- AC5.3: The existing isolated-child merge path applies a non-conflicting child change and preserves the child on conflict; the MVP makes no cross-process serialization or crash-recovery claim.
  - verify: `TestMicroVMMVP_Scenario5_BasicExistingMergeBehavior`
- AC5.4: Production status inventories repository logical attachments—not the superseded session-per-VM registry—in deterministic pages of at most 64, showing two distinct logical worktrees on one repository generation; exact logical deletion removes clean state, retains dirty state for recovery, and never implies repository-VM deletion.
  - verify: `TestRepositoryProductionInventoryPaginationAndLogicalDelete`

### Scenario 6 — Linux ownership and useful networking are honest

This follows [ADR 0326's Linux guest decision](../adr/0326-microvm-execution-environments.md#4-consume-brood-directly-and-provide-an-explicit-linux-guest).

Linux amd64 is the only live target. The default network is useful rather than presented as
containment; tightening remains optional and fail-closed when selected.

**Acceptance:**

- AC6.1: On Linux amd64, guest workload UID/GID 65532 maps to the daemon user through an unprivileged user namespace, model commands run unprivileged, and session or child worktrees are never widened to world-readable, world-writable, or world-traversable modes.
  - verify: `TestMicroVMMVP_Scenario6_LinuxUserNamespaceAvoidsWorldModeWidening`
- AC6.2: The built-in hosted profile provides unrestricted IPv4 guest egress by default and reports that the IPv6 stack remains enabled while external IPv6 is unrouted and unsupported; optional deny-all or allowlist tightening filters IPv4, disables IPv6, and aborts readiness rather than falling back when either enforcement step fails.
  - verify: `TestMicroVMMVP_Scenario6_PermissiveIPv4WithOptionalFailClosedTightening`

### Scenario 7 — one Linux amd64 journey proves the MVP

This follows [the required live journey](../architecture/microvm-environments.md#required-live-journey-and-limits).

The live proof is intentionally narrow and operator-oriented.

**Acceptance:**

- AC7.1: Linux amd64 KVM enters through production profile/session composition and proves ordinary first use, direct admitted Brood consumption with in-process verification, one repository VM/rootfs, two sessions with a shared declared cache and distinct worktrees, confined filesystem and exec, unrestricted IPv4 networking with external IPv6 unsupported, close-detach, and prompt explicit daemon-restart failure that preserves the record/rootfs/worktrees and mints no replacement. Optional fail-closed tightening is proven separately by AC6.2's production app/profile and network enforcement tests, not by this live journey.
  - verify: demonstration — `task e2e:microvm` is the required Linux amd64 KVM live gate
- AC7.2: Concise architecture, operator, and public documentation describes profile selection, the repository sharing boundary, distinct worktrees, direct Brood admission, Linux ownership, permissive networking, optional tightening, restart failure behavior, and the deferred surfaces without claiming Linux arm64 or macOS live support.
  - verify: inspection — `task docs` and `task site:build` prove the linked documentation surfaces build

## Out of scope — explicitly deferred

- repository-VM deletion UX and sophisticated retention policy;
- crash-orphan reconciliation beyond an actionable, safe, loud failure;
- crash-durable merge recovery and cross-process/multi-client merge proofs;
- macOS live ownership parity and Linux arm64 live support;
- upstream Brood signing and independent artifact/config refresh channels;
- per-session fairness, quotas, dashboards, and exhaustive cache-poisoning controls.

Non-Git environments, schedules, remote/multi-user microvmd, cross-principal VM sharing,
unified host+guest egress containment, and moving provider/MCP credentials into the guest
also remain out of scope.

## Cross-cutting deliverables

- Historical tasks 1–58 and completed tasks 59–61 remain intact. Tasks 62–65 are the only
  remaining implementation tasks.
- The durable registry fails closed on inconsistent restart state; it does not silently
  recreate, delete, or reconcile an orphan.
- Default, no-fs, and engine-standalone gates preserve the opt-in module boundary.
- Every remaining numbered AC is quoted with its exact `verify:` line in exactly one of
  tasks 62–65.

## Definition of done

1. Tasks 62–65 and every acceptance criterion above are complete.
2. `task lint`, `task test`, `task api:check`, and nested microVM module gates pass.
3. `task docs`, `task site:build`, and `task ac-trace-strict` pass.
4. `go run ./cmd/mecademo` still prints a complete offline session.
5. Linux amd64 KVM passes the required MVP journey; no other platform is represented as live evidence.

## Exit criteria

When the definition of done holds on the accumulator and review finds no ship blocker, the
orchestrator may flip this plan from `in-progress` to `landed`.
