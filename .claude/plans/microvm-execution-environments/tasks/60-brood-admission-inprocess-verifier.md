---
id: 60-brood-admission-inprocess-verifier
title: Admit immutable Brood platform digests with in-process Sigstore verification
blocked_by: [59-manager-ensure-ready-surface]
status: done
branch: "plan-microvm-execution-environments/60-brood-admission-inprocess-verifier"
worktree: ""
issue: "526"
retries: 0
last_error: ""
accumulator: acc/microvm-execution-environments
---

# Artifact trust redesign

Use Brood base latest only as controlled discovery, resolve and endorse the immutable
platform digest, remove the Brood rebuild and derived-image lineage, and replace runtime
cosign execution/config/temp files with `toolhive-core/container/verifier` in-process.
Keep downstream mecatl endorsement authoritative while upstream signing is deferred.

## Acceptance criteria

- AC4.1: Controlled release/admission resolves Brood base `latest` to an immutable platform manifest digest, records the mutable discovery reference and resolution evidence, and admits runtime use only under a valid mecatl downstream endorsement.
  - verify: `TestMicroVMRedesign_Scenario4_BroodLatestIsDiscoveryOnly`
- AC4.2: Runtime boots the admitted Brood platform bytes directly without a Brood rebuild or derived guest-tools image, and a changed resolution, wrong platform, missing endorsement, stale policy, or corrupted subject fails before VM execution.
  - verify: `TestMicroVMRedesign_Scenario4_RuntimeConsumesOnlyEndorsedBroodDigest`
- AC4.5: Runtime Sigstore verification uses `toolhive-core/container/verifier` in-process and production configuration exposes no cosign executable/path, verification subprocess, or verification temporary-file protocol.
  - verify: `TestMicroVMRedesign_Scenario4_SigstoreVerificationIsInProcess`
