---
id: 06-classification-guard
title: Guard application boundaries and reconcile documentation
blocked_by: [02-persisted-resources, 03-memory-isolation, 04-live-model-paths, 05-system-and-deployment]
status: done
branch: "plan-caller-separation/06-classification-guard"
worktree: ""
issue: "368"
retries: 0
last_error: ""
accumulator: acc/caller-separation
---

# Guard application boundaries and reconcile documentation

Implement the source-level classification guard over the agreed application boundaries,
make exemptions narrow and reviewable, and update the living architecture/operator docs
for the shipped caller-isolation behavior.

## Acceptance criteria

- AC5.1: The classification guard resolves every current designated object-touching application boundary to exactly one valid table entry and reports an unclassified call site or stale table entry.
  - verify: `TestInvariant_owned_access_is_classified`
- AC5.2: A fixture that adds an unclassified owned access path causes the classification guard to fail.
  - verify: `TestCallerSeparation_Scenario5_UnclassifiedAccessFailsGuard`
- AC5.3: Explicit shared-infrastructure/exempt entries state their rationale and do not become caller-owned bypasses.
  - verify: `TestCallerSeparation_Scenario5_ExemptionsAreExplicitAndNarrow`
