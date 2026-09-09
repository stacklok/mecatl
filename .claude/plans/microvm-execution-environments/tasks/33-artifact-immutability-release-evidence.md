---
id: 33-artifact-immutability-release-evidence
title: Make verified artifacts immutable and publish complete admission evidence
blocked_by: []
status: done
branch: "plan-microvm-execution-environments/33-artifact-immutability-release-evidence"
worktree: ".scratch/task-microvm-33"
issue: "528"
retries: 0
last_error: ""
accumulator: acc/microvm-execution-environments
---

# Third panel repair

Final Spec blockers: an uncooperative same-user writer can mutate ordinary cached paths after validation while runtime consumes them; release evidence signs microvmd/guest binaries but does not publish independently admissible runtime, firmware, and execution-image bundles.

Make verified cache entries immutable to non-cooperating writers between admission and runtime consumption using ownership/mode/rename isolation, private immutable snapshot/FD, or equivalent platform-safe mechanism—not advisory flock alone. Publish or reference runtime, firmware, and execution-image payloads with independent digests and the exact Sigstore/provenance evidence microvmd admission consumes. Ensure release installation can configure strict admission without manufacturing evidence.

Protects AC2.1–AC2.3, AC8.1, release DoD.

Verification: concurrent same-account mutation after LockAndValidate cannot alter launched bytes; release fixture produces strict-admissible evidence for all three artifact kinds; production E2E consumes those packaged-format artifacts; lint/test/docs/action/ac-trace pass.
