---
id: 04-session-scoped-discovery-preparation
title: Session-scoped discovery preparation
blocked_by: [03-server-binding-and-exact-reattachment]
status: pending
branch: ""
worktree: ""
issue: ""
retries: 0
last_error: ""
accumulator: acc/server-owned-session-placement
---

# Task brief

Prepare additive service APIs that discover commands and worktrees from an owned session after authorization and exact reattachment. No-FS must short-circuit to its documented empty or unsupported result before any filesystem/default resolver, command source, or worktree lister runs. Worktree results use opaque provider-owned IDs and bounded safe metadata, and use still requires authorization and atomic binding. Retain existing wire adapters and public message shapes for the coordinated cutover.

Expected focus: service discovery seams, no-FS short circuits, opaque worktree service values, and focused tests. Do not modify protobufs, generated code, or mecatui yet.

## Acceptance criteria

- AC3.2: For a no-FS session, both discovery calls return the documented empty/
unsupported result without invoking a filesystem/default resolver, command source, or
worktree lister.
  - verify: `TestADR_0280_NoFSDiscoveryDoesNotInvokeFilesystemProviders`
- AC3.3: Worktree entries advertise only provider-owned opaque IDs and bounded safe
metadata. Creating or forking from one reauthorizes and atomically binds it; listing is
not a grant.
  - verify: `TestADR_0280_WorktreePlacementIsReauthorizedOnUse`
