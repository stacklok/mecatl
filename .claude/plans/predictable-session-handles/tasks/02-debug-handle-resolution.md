---
id: 02-debug-handle-resolution
title: Debug short-handle resolver and exact-ID boundary proof
blocked_by: [01-handle-projection-presentation]
status: done
branch: "plan-predictable-session-handles/02-debug-handle-resolution"
worktree: ".scratch/worktrees/issue-922-task02"
issue: "922"
retries: 0
last_error: ""
accumulator: acc/predictable-session-handles
---

# Task brief

Historical implementation: `Client.CreateDebugSession` gained the first client-side short-handle
resolver using the shared projection and all-pages `ListSessions` helper. Panel review superseded
its exact-equality-first rule and UI-package integration-test placement; current resolver/CLI
semantics belong to task 05 and the relocated composition proof belongs to task 06.

Historical seams were `cmd/mecatui/client/client.go`, `sessions_list.go`, and focused client
tests. The composition-level cross-boundary proof is deliberately deferred to task 06. Server,
proto, and debugger evidence/scope/incarnation contracts remain unchanged.

## Acceptance ownership

Historical task completed before panel review. Its former AC1.4–AC1.5 and AC2.1–AC2.5 text is
superseded by the revised plan; task 05 owns revised AC1.4–AC1.6 and AC2.1–AC2.4, while task 06
owns revised AC2.5. This done task owns no current numbered AC.
