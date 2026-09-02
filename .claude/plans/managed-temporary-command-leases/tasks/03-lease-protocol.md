---
id: 03-lease-protocol
title: Versioned leases, manifests, and safe ownership protocol
blocked_by: [02-managed-namespace-safety]
status: pending
branch: ""
worktree: ""
issue: ""
retries: 0
last_error: ""
accumulator: acc/managed-temporary-command-leases
---

# Versioned leases, manifests, and safe ownership protocol

Add random allocation identity, versioned lifecycle manifests, lease locks, and workspace index consistency over the private namespace. This task supplies the protocol consumed by command and reaper work; it does not schedule maintenance.

## Acceptance criteria

- AC1.6: A managed root/workspace allocation rejects symlink, replacement, ownership, mode, manifest-key, or index-collision failures without deleting anything, and maps the raw workspace identity only in owner-only index metadata—not into an agent-visible path. A same-UID component replacement after validation and before cleanup/reaping cannot redirect deletion outside the retained handle-rooted lease; the candidate is retained or fails closed.
  - verify: `TestADR_0281_ManagedRootAndWorkspaceFailClosed`
- AC1.7: A missing, malformed, or newer-than-supported version in any allocation manifest or sweep-completion record is reported and retained without rewrite or deletion.
  - verify: `TestADR_0281_UnknownManifestVersionRetained`
