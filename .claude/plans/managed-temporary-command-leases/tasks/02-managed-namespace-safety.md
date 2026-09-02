---
id: 02-managed-namespace-safety
title: Private managed namespace and workspace identity
blocked_by: [01-temporary-storage-config]
status: pending
branch: ""
worktree: ""
issue: ""
retries: 0
last_error: ""
accumulator: acc/managed-temporary-command-leases
---

# Private managed namespace and workspace identity

Build the Linux managed-root and workspace namespace substrate. Keep all mutation handle-rooted and no-link; establish private modes atomically. This task owns namespace/path safety, not lease lifecycle or command execution.

## Acceptance criteria

- AC1.5: Every managed root, workspace, lease, lock, manifest, completion record, and temporary-write file is created handle-relatively with private permissions before it is published. A permissive umask never creates a group- or world-readable observation window, and an existing foreign or permissive object is rejected rather than repaired.
  - verify: `TestADR_0281_ManagedObjectsCreatedAtomicallyPrivate`
