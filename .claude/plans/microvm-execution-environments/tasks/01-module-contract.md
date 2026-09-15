---
id: 01-module-contract
title: Nested module boundary and architecture contract
blocked_by: []
status: done
branch: "plan-microvm-execution-environments/01-module-contract"
worktree: ".scratch/task-microvm-01"
issue: "527"
retries: 0
last_error: ""
accumulator: acc/microvm-execution-environments
---

# Task brief

Establish the nested `environment/microvm` module boundary, keep go-microvm/libkrun out of engine/root default builds, and pin the byte-compatible disabled path. Reconcile the proposed ADR and living architecture/resource inventories where this task owns their structural contract. Do not implement runtime behavior owned by later tasks.

## Acceptance criteria

- AC1.5: With microVM support disabled or no microVM profile selected, existing
  default/no-fs session snapshots, catalogs, prompts, and behavior remain byte-compatible.
  - verify: `TestInvariant_microvm_disabled_is_byte_compatible`
- AC1.6: The normal engine module and root default build have no go-microvm/libkrun
  import or link dependency; only the nested `environment/microvm` module owns the
  heavy runtime.
  - verify: `TestADR_0108_MicroVMDependenciesStayOutOfEngineAndRoot`
