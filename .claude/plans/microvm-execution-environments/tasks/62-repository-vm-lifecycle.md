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
- AC2.1: Canonically equivalent checkouts for one local operator resolve to one repository identity and one VM generation, while a different Git common directory or operator resolves to a different durable registry entry.
  - verify: `TestMicroVMMVP_Scenario2_CanonicalRepositoryIdentitySelectsSingletonVM`
- AC2.3: Repository-controlled names, symlinks, linked-worktree metadata, and hostile path components cannot collide registry identities or escape the owner-scoped state root.
  - verify: `TestMicroVMMVP_Scenario2_RepositoryIdentityIsCanonicalAndConfined`
- Enabling primitive: concurrent registry use converges on one durable record and invokes one retained rootfs materializer for the validated canonical identity.
  - verify: `TestRepositoryVMRegistryConcurrentEnsureConvergesAndPersistsRecord`, `TestRepositoryVMRegistryMaterializesRootFSOnce`
