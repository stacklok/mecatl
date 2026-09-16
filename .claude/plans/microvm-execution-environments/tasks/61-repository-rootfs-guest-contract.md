---
id: 61-repository-rootfs-guest-contract
title: Provide the static repository rootfs primitive and guest contract
blocked_by: [60-brood-admission-inprocess-verifier]
status: done
branch: "plan-microvm-execution-environments/61-repository-rootfs-guest-contract"
worktree: ""
issue: "526"
retries: 0
last_error: ""
accumulator: acc/microvm-execution-environments
---

# Repository rootfs and guest setup

Provide the primitive that clones admitted Brood bytes, injects the independently
verified guest agent, and establishes static HOME, PATH, workdir, writable home,
package/tool cache, and UID/GID behavior independent of the base image entrypoint.
The primitive contains no environment binding or capability key. Task 62 owns its
repository-generation lifecycle and the proof that sessions and children do not clone it.

## Acceptance criteria

- Enabling primitive: One retained materializer clones admitted Brood bytes once,
  injects the independently verified guest agent, establishes only static guest setup,
  and rejects a second materialization.
  - verify: `TestRepositoryRootFSMaterializer_ClonesStaticRootFSAndInjectsGuestAgentOnce`
- AC4.4: Guest setup explicitly establishes workload UID/GID 65532, HOME, PATH, default workdir, writable home, and declared package/tool caches independent of Brood's image command or entrypoint.
  - verify: `TestMicroVMRedesign_Scenario4_GuestRuntimeContractIsExplicit`
