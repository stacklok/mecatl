---
id: 03-artifact-verification
title: Runtime, firmware, and execution-image verification
blocked_by: [01-module-contract]
status: done
branch: "plan-microvm-execution-environments/03-artifact-verification"
worktree: ".scratch/task-microvm-03"
issue: "528"
retries: 0
last_error: ""
accumulator: acc/microvm-execution-environments
---

# Task brief

Consume go-microvm's existing runtime/firmware artifacts, add immutable digest resolution, operator trust policy, signature/attestation verification, and atomic cache admission in the nested module. Keep verification offline-testable with fakes; do not require live registries in ordinary tests.

## Acceptance criteria

- AC2.1: A correctly signed and attested runtime, firmware, and execution image resolve
  to immutable digests, enter the verified cache atomically, and boot a VM whose durable
  metadata records those digests and the verification-policy revision.
  - verify: `TestMicroVMEnvironments_Scenario2_VerifiedArtifactsBoot`
- AC2.2: A mutable/tag-only input, wrong digest, unsigned artifact, wrong signer,
  missing/wrong attestation, revoked identity, corrupted cache entry, or stale policy
  fails before VM execution.
  - verify: `TestMicroVMEnvironments_Scenario2_UnverifiedArtifactsFailClosed`
- AC2.3: Concurrent sessions requesting the same cold artifact observe one complete
  verified cache result; none can execute a partial or pre-verification file.
  - verify: `TestMicroVMEnvironments_Scenario2_ConcurrentCacheAdmissionIsAtomic`
