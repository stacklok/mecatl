---
id: 05-system-and-deployment
title: Scope system workers and prove driver isolation
blocked_by: [01-ownership-core, 02-persisted-resources, 03-memory-isolation]
status: done
branch: "plan-caller-separation/05-system-and-deployment"
worktree: ""
issue: "368"
retries: 0
last_error: ""
accumulator: acc/caller-separation
---

# Scope system workers and prove driver isolation

Preserve the distinct actor and resource-owner identities for workers, limit system
principals to classified operations, prove the raw-driver deployment boundary, and make
the ownerless cutover observable and safe.

## Acceptance criteria

- AC4.1: A due schedule owned by Alice fires under the scheduler's explicit system principal while the created work retains Alice's durable schedule owner. Its durable lifecycle/run events name the scheduler as actor without substituting that identity for the resource owner or exposing either actor attribution on the client wire.
  - verify: `TestCallerSeparation_Scenario4_SchedulerActorAndOwnerRemainDistinct`
- AC4.2: Child GC and each memory/dream consolidator complete their explicitly classified shared-infrastructure operation under their registered system principal.
  - verify: `TestCallerSeparation_Scenario4_InternalWorkersUseOnlyClassifiedAccess`
- AC4.3: A system worker is denied when it attempts a caller-owned operation not explicitly classified as shared infrastructure.
  - verify: `TestCallerSeparation_Scenario4_SystemPrincipalIsNotUniversalBypass`
- AC4.4: Changing `strict`, `trusted`, `auto`, or `yolo` posture never disables caller ownership enforcement.
  - verify: `TestCallerSeparation_Scenario4_PostureCannotDisableOwnership`
- AC4.5: An OIDC deployment selects one concrete raw-driver boundary—NetworkPolicy, mTLS-pinned workload peer, or Unix socket—and proves the mecatl workload can use it while a tenant peer cannot connect or authenticate to a raw driver.
  - verify: `TestCallerSeparation_Scenario4_RawDriverIsTenantInaccessible`
- AC4.6: Before OIDC isolation is enabled, an operator can inventory the ownerless records that will become inaccessible. After enablement, background workers neither adopt nor repeatedly mutate/retry those stranded records; disabling the verifier restores only the pre-existing ownerless compatibility path.
  - verify: `TestCallerSeparation_Scenario4_OwnerlessCutoverIsObservableAndSafe`
