---
id: 04-paged-inventory
title: Cursor-bounded session inventory and transport parity
blocked_by: [01-session-taxonomy]
status: done
branch: "plan-session-continuity-ux/04-paged-inventory"
worktree: ".scratch/worktrees/session-continuity-ux-task-04"
issue: "471"
retries: 0
last_error: ""
accumulator: acc/session-continuity-ux
---

# Task brief

Add the optional additive metadata-pager capability and keyset cursor contract from ADR-0217. Implement every in-tree durable store/remote driver, shared conformance, server capability/reason-code projection, and gRPC/HTTP parity. Bound response/page memory; do not claim constant-time backend traversal or mutate the required SessionStore interface.

## Acceptance criteria

- AC3.5: Public inventory responses use an opaque keyset cursor, enforce a maximum page size, order ties by `(modified_at DESC, session_id ASC)` after ownership filtering, and remain safe/bounded under concurrent saves without claiming snapshot-stable pages.
  - verify: `TestSessionContinuityUX_Scenario3_PaginationContract`
- AC3.6: Every in-tree durable store and remote-driver adapter passes shared optional metadata-pager conformance; a store without pager support reports listing unavailable instead of returning an unbounded response.
  - verify: `TestSessionContinuityUX_Scenario3_PagerConformance`
- AC3.7: gRPC and HTTP expose equivalent kind, relationship, capability, reason-code, pagination, and transcript behavior.
  - verify: `TestSessionContinuityUX_Scenario3_TransportParity`
