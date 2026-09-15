---
id: 63-logical-worktree-protocol
title: Route authenticated logical worktrees through the repository VM
blocked_by: [62-repository-vm-lifecycle]
status: done
branch: "plan-microvm-execution-environments/63-logical-worktree-protocol"
worktree: ""
issue: "526"
retries: 0
last_error: ""
accumulator: acc/microvm-execution-environments
---

# Logical worktree routing

Make the daemon start the repository VM from the singleton registry's one materialized
rootfs, using repository-scoped boot authority and authenticated endpoint health rather
than a session-bound preboot file. Create distinct Git worktrees and register each assigned
guest root against one opaque logical `EnvironmentRef` in that VM. Authenticate every
new channel under repository boot authority before host disclosure, then authenticate
registration, handshake, filesystem, and exec against owner, generation, ref, and root.
Preserve Workspace/runner affinity, versioned mutations, streaming, cancellation, and no
host fallback. Treat same-repository worktrees as logical Git/RPC isolation rather than
mutually hostile Bash sandboxes. Session/delegation composition follows in task 64.

## Acceptance criteria

- AC3.1: Two logical environments in one repository receive distinct daemon-created worktrees, branches, indexes, logical `EnvironmentRef`s, and guest roots while naming the same repository VM generation.
  - verify: `TestMicroVMMVP_Scenario3_LogicalEnvironmentsShareVMNotWorktree`
- AC3.2: A newly accepted vsock/Unix peer proves possession of the repository boot authority before the host sends any binding capability, registration, request, or secret; the challenge binds owner, repository/VM identity, VM generation, and control/data channel purpose, so connection order grants no identity. Guest registration, handshake, and every filesystem or exec request then authenticate the logical `EnvironmentRef` and assigned root before dispatch.
  - verify: `TestRepositoryGuestAuthenticationRejectsCompetingConnectorBeforeDisclosure`, `TestMicroVMMVP_Scenario3_EnvironmentRefAuthenticatesAssignedRoot`
- AC3.3: Stale, replayed, wrong-owner, wrong-generation, sibling-ref, path-escape, and cross-worktree protocol requests fail closed; Workspace and CommandRunner remain affined to the same assigned root/cwd with no host fallback. Git worktrees isolate working/index state and logical RPC routing, not mutually hostile same-repository Bash: arbitrary Bash may address sibling guest paths. Different repositories remain VM-isolated and host/other-repository paths remain unavailable.
  - verify: `TestInvariant_microvm_logical_environment_is_confined_and_affined`
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
