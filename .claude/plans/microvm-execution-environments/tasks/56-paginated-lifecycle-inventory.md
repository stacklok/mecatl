---
id: 56-paginated-lifecycle-inventory
title: Paginate lifecycle inventory and recovery status
blocked_by: []
status: done
branch: "e45e93e0"
worktree: ".scratch/task-microvm-56"
issue: "526"
retries: 0
last_error: ""
accumulator: acc/microvm-execution-environments
---

# Usability repair

Replace the 256-lifetime-record failure with deterministic owner-scoped pagination and opaque continuation token across daemon protocol, root client, manager status/recover, and mecatui output. Bound page/message size; include retained destroyed dirty-worktree records; prevent cross-owner/token tampering; recover iterates safely without unbounded memory.

Verification: >256 records fully discoverable/deletable, deterministic pages, invalid/foreign tokens fail, bounded manager/TUI output and continuation, full gates.
