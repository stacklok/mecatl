---
id: 07-durable-attempt-store
title: Implement a crash-safe durable AttemptRepository adapter
blocked_by: [05-attempt-conformance]
status: pending
branch: ""
worktree: ""
issue: ""
retries: 0
last_error: ""
accumulator: acc/cloud-native-learning
---

# Task brief

Add the host-side durable attempt store beside existing reflection/skill persistence, following bounded-document, stable-lock, temp-fsync/rename/directory-sync, no-follow, and multi-instance CAS conventions. Keep private owner bindings and raw partition material out of public records. Prove reopen, concurrent instances, expiry, and cleanup with shared conformance.

## Acceptance criteria

No numbered acceptance criterion is owned by this adapter task. It provides the durable boundary required by AC2.1 and the explicit cross-Build slice.
