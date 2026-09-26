---
id: 28-artifact-release-alignment-repair
title: Close artifact launch TOCTOU and align release evidence with admission
blocked_by: []
status: done
branch: "plan-microvm-execution-environments/28-artifact-release-alignment-repair"
worktree: ".scratch/task-microvm-28"
issue: "528"
retries: 0
last_error: ""
accumulator: acc/microvm-execution-environments
---

# Second panel repair

Remaining Spec/DevOps blockers: verified cache paths can mutate after Verify before launch; release upload assumes a pre-existing GitHub Release; matrix cells clobber one SHA256SUMS; published Cosign/GitHub evidence is incompatible with daemon Ed25519 admission; the hand-written SBOM has no dependency inventory.

Make verified artifacts immutable between admission and launch using a content-addressed immutable handle or launch-time revalidation under lock. Add serialized release creation, per-platform or aggregated checksums, real Syft-equivalent SPDX/CycloneDX component SBOMs, and one evidence format/identity policy consumed by microvmd. Production E2E must admit the exact packaged evidence, not manufacture another scheme.

Protects AC2.1–AC2.3, AC8.1 and release DoD.

Verification: mutate-after-verify fails; tag workflow creates release; matrix checksum assets cannot race; generated SBOM includes modules/dependencies; strict daemon accepts published bundle and rejects altered/wrong identity; action lint/lint/test/docs pass.
