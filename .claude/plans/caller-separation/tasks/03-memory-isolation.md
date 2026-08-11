---
id: 03-memory-isolation
title: Partition user-model and project memory
blocked_by: [01-ownership-core]
status: done
branch: "plan-caller-separation/03-memory-isolation"
worktree: ""
issue: "368"
retries: 0
last_error: ""
accumulator: acc/caller-separation
---

# Partition user-model and project memory

Keep the user-model and project-memory stores separate while applying caller and
workspace partitioning to their real tool and persistence paths. Include search, cache,
consolidation, update, and deletion behavior.

## Acceptance criteria

- AC2.1: Alice and Bob can each store and retrieve a user-model-memory entry using the same logical key without observing the other's value; the local user-model store is caller-partitioned and is not silently treated as a remote driver store.
  - verify: `TestCallerSeparation_Scenario2_UserModelMemoryIsCallerPartitioned`
- AC2.2: Project memory is partitioned by verified owner and workspace, so equal logical keys in the same project are caller-isolated while a caller retains its own project memory across sessions. Its backing store, search/index, cache, consolidation, and delete operations stay within that owner/workspace namespace.
  - verify: `TestCallerSeparation_Scenario2_ProjectMemoryIsCallerPartitioned`
- AC3.4: The real Remember, Recall, search, and forget memory-tool paths keep Alice's and Bob's same-key user/project memory isolated; a foreign read is absent and a foreign write or delete cannot alter the owner's value.
  - verify: `TestCallerSeparation_Scenario3_ModelFacingMemoryToolsAreOwnerChecked`
