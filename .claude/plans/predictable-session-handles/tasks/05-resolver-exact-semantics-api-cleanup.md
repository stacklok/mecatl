---
id: 05-resolver-exact-semantics-api-cleanup
title: Resolver semantics and handle API repair
blocked_by: [01-handle-projection-presentation, 02-debug-handle-resolution, 03-command-help-and-docs, 04-ac-trace-compatibility]
status: done
branch: "plan-predictable-session-handles/05-resolver-exact-semantics-api-cleanup"
worktree: ".scratch/worktrees/issue-922-task05"
issue: "922"
retries: 0
last_error: ""
accumulator: acc/predictable-session-handles
---

# Task brief

Repair the client-owned projection and resolver without changing server/proto identity. Encode a
leading `-` as `%2D` while retaining literal non-leading hyphens and complete-atom width behavior.
For short handles, deduplicate inventory rows by exact ID and gather projected matches. Keep
`SessionHandleWidth` as the sole width API and preserve invalid-UTF-8 behavior and every debugger
evidence/incarnation digest unchanged.

The final product contract is one `TARGET` grammar. Exact full-ID equality is authoritative;
otherwise one projected match resolves, ambiguity requires the copied full exact ID, and inventory
failure or zero matches passes `TARGET` unchanged to the server exact-ID path. Use the existing
client resolver and all-pages inventory helper; do not add another resolver, decode handles, widen
protobuf/server APIs, or implement collision expansion.

## Acceptance criteria

- AC1.1–AC1.4: fixed terminal-safe ordinary handles render consistently and invalid UTF-8 cannot
  produce a handle or debug create.
  - verify: `TestPredictableSessionHandles_Scenario1_SharedNormalHandle`
  - verify: `TestPredictableSessionHandles_Scenario1_FixedCollisionBehavior`
  - verify: `TestPredictableSessionHandles_Scenario1_EscapedUTF8ControlsAndLeadingHyphen`
  - verify: `TestPredictableSessionHandles_Scenario1_InvalidUTF8HasNoHandleOrDebugCreate`
- AC1.5–AC1.6: inventory outage preserves server exact-ID authority and the obsolete width alias is
  absent.
  - verify: `TestCreateDebugSessionResolution`
  - verify: `TestPredictableSessionHandles_Scenario1_OnlyHandleWidthAPI`
- AC2.1–AC2.4: the one `TARGET` path proves grammar, exact precedence, unique and ambiguous
  projection, zero-match fallthrough, and inventory-failure fallthrough.
  - verify: `TestPredictableSessionHandles_Scenario2_HandleGrammarAndUnifiedTarget`
  - verify: `TestCreateDebugSessionResolution`
