---
id: 62-repository-vm-lifecycle
title: Establish canonical repository identity and a durable singleton VM/rootfs registry
blocked_by: [61-repository-rootfs-guest-contract]
status: done
branch: "plan-microvm-execution-environments/62-repository-vm-lifecycle"
worktree: ""
issue: "526"
retries: 0
last_error: ""
accumulator: acc/microvm-execution-environments
---

# Repository identity and singleton lifecycle

Reset the earlier in-progress task. Canonicalize `(operator, Git common directory)` into an
owner-confined repository identity and provide a descriptor-confined durable singleton
registry under inter-process locking. The registry owns one rootfs materializer per key and
concurrent primitive use converges. Keep this task below production VM boot and the guest
data plane: task 63 owns repository-VM startup/routing, and task 64 owns live reattachment.

## Acceptance criteria

- AC2.1: Canonically equivalent checkouts for one local operator resolve to one repository identity and one VM generation, while a different Git common directory or operator resolves to a different durable registry entry.
  - verify: `TestMicroVMMVP_Scenario2_CanonicalRepositoryIdentitySelectsSingletonVM`
- AC2.3: Repository-controlled names, symlinks, linked-worktree metadata, and hostile path components cannot collide registry identities or escape the owner-scoped state root.
  - verify: `TestMicroVMMVP_Scenario2_RepositoryIdentityIsCanonicalAndConfined`
- Enabling primitive: concurrent registry use converges on one durable record and invokes one retained rootfs materializer for the validated canonical identity.
  - verify: `TestRepositoryVMRegistryConcurrentEnsureConvergesAndPersistsRecord`, `TestRepositoryVMRegistryMaterializesRootFSOnce`
