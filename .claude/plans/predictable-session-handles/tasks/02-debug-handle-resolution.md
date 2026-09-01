---
id: 02-debug-handle-resolution
title: Debug short-handle resolver and exact-ID boundary proof
blocked_by: [01-handle-projection-presentation]
status: in-progress
branch: ""
worktree: ".scratch/worktrees/issue-922-task02"
issue: "922"
retries: 0
last_error: ""
accumulator: acc/predictable-session-handles
---

# Task brief

Make `Client.CreateDebugSession` own the sole client-side short-handle resolver before it builds the existing create request. Use the shared projection from task 01 and only the existing all-pages `ListSessions` helper. A syntactically valid short candidate must resolve against the caller-visible inventory with exact-ID equality first and deduplicated exact IDs; malformed, lowercase, truncated, long, and other operands remain opaque exact-ID inputs without a lookup. Zero matches, distinct projected-ID ambiguity, and inventory failure must fail before `CreateSession` with concrete `/session` exact-copy guidance. Only the resolved full exact ID may cross in `CreateSessionRequest`.

Work in `cmd/mecatui/client/client.go`, `sessions_list.go`, and focused client tests. Add the real header-to-real-create cross-boundary proof, including a valid escaped control-bearing ID. Do not change server/proto APIs or debugger evidence/scope/incarnation contracts.

## Acceptance criteria

- AC1.4: Invalid UTF-8 is not repaired or percent-encoded into a new identity: ordinary
  projections emit no handle, and debug creation is not attempted. Existing corrupt-snapshot
  handling and protobuf-boundary UTF-8 behavior remain unchanged.
  - verify: `TestPredictableSessionHandles_Scenario1_InvalidUTF8HasNoHandleOrDebugCreate`
- AC1.5: Header and other normal chrome render the fixed handle without inventory. If debug
  cannot obtain a complete caller-visible inventory, it stops before create and `/session` still
  safely renders and copies each valid non-empty exact ID unchanged.
  - verify: `TestPredictableSessionHandles_Scenario1_InventoryFailureKeepsExactCopyFallback`

- AC2.1: Only a syntactically valid short token invokes local resolution: it is non-empty ASCII,
  at most twelve columns, and consists of `[A-Za-z0-9._-]` literals and complete uppercase
  `%[0-9A-F]{2}` atoms. Lowercase, malformed, or truncated escape candidates, plus longer/other operands, remain
  exact-ID inputs and are sent unchanged without inventory lookup.
  - verify: `TestPredictableSessionHandles_Scenario2_HandleGrammar`
- AC2.2: For a syntactically valid short token, the complete caller-visible inventory is consulted
  before creation. Exact-ID equality wins before projection matching, and repeated rows for one
  exact ID count as one candidate.
  - verify: `TestPredictableSessionHandles_Scenario2_ExactIDPrecedesDistinctProjectionMatches`
- AC2.3: A literal short handle from the complete caller-visible inventory resolves uniquely before
  debug-session creation; the request carries the matched full ID, never the short handle.
  - verify: `TestPredictableSessionHandles_Scenario2_HandleResolvesToExactID`
- AC2.4: Zero matches, distinct projected-ID ambiguity, or an inventory error stops before
  `CreateSession`; each error gives concrete `/session` exact-copy guidance without disclosing
  rows the caller cannot see.
  - verify: `TestPredictableSessionHandles_Scenario2_FailClosedBeforeCreate`
- AC2.5: The literal emitted by the real rendered normal-session header passes unchanged through
  the real `CreateDebugSession` path and binds the resulting debugger request to that header's
  exact target; the same proof covers an escaped control-bearing valid-UTF-8 ID.
  - verify: `TestPredictableSessionHandles_Scenario2_RealHeaderHandleCreatesBoundDebugger`
