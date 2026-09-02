---
id: 03-http-affinity-validation
title: HTTP session-route affinity validation
blocked_by: [01-session-header-contract]
status: pending
branch: ""
worktree: ""
issue: ""
retries: 0
last_error: ""
accumulator: acc/session-affinity-and-handoff
---

# Task brief

Introduce a single HTTP session-route affinity gate and apply it structurally to every route whose decoded path names a session: reads, management mutations, prompt/retry/approval/control, fork/adoption/reflection, replay, live stream, and watch. Build a route-inventory test that fails when a new session-bound route is registered without an explicit classification.

**Likely scope:** `internal/adapter/server/http.go`, focused HTTP affinity/inventory tests, and ordinary typed-error helpers only if needed. Keep route-specific handlers free of duplicate parsing policy.

**Invariants:** compare the legal header byte-for-byte with `Request.PathValue("id")` after normal `net/http` path decoding; never decode, trim, normalize, log, or reflect the header; reject duplicates/illegal/mismatch before handler dispatch; preserve headerless compatibility and ADR-0248 typed invalid-argument projection; affinity grants no authentication, ownership, or management authority. Tests use `httptest` only.

## Acceptance criteria

- AC3.1: Every session-bound HTTP read, mutation, prompt, retry, approval, control,
  replay, and watch route accepts a missing field or one legal field exactly equal to
  its decoded path session ID.
  - verify: `TestSessionAffinityAndHandoff_Scenario3_HTTPRouteInventory`

- AC3.2: Duplicate values, illegal bytes, and byte-mismatched values are rejected before
  handler dispatch with the ordinary typed invalid-argument response, and no response
  body or diagnostic reflects either value.
  - verify: `TestADR_0290_HTTPHeaderFailureIsNonDisclosing`

- AC3.3: Escaped path IDs are compared after the server's normal path decoding; the
  field itself remains byte-exact and is never URL-decoded, trimmed, or normalized.
  - verify: `TestADR_0290_HTTPDecodedPathEquality`

- AC3.4: Header validation grants no access: authentication, caller ownership, and
  management-root checks still run independently and return their existing outcomes.
  - verify: `TestADR_0290_AffinityHeaderGrantsNoAuthority`
