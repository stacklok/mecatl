---
id: 05-resolver-exact-semantics-api-cleanup
title: Resolver ambiguity, exact-ID CLI escape hatch, and handle API repair
blocked_by: [01-handle-projection-presentation, 02-debug-handle-resolution, 03-command-help-and-docs, 04-ac-trace-compatibility]
status: in-progress
branch: ""
worktree: ".scratch/worktrees/issue-922-task05"
issue: "922"
retries: 0
last_error: ""
accumulator: acc/predictable-session-handles
---

# Task brief

Repair the client-owned projection and resolver without changing server/proto identity. Encode a
leading `-` as `%2D` while retaining literal non-leading hyphens and complete-atom width behavior.
For positional short handles, deduplicate inventory rows by exact ID, gather every distinct
projected match, and require exactly one; an exact-ID string match must not mask another projected
match. Add the conventional, mutually-exclusive `debug --exact SESSION_ID` bypass in embedded and
`connect ADDRESS debug` forms, implemented as a small `Client.CreateDebugSessionExact` sibling
sharing the private create tail rather than a mode type or test-only accessor. Existing
long/non-handle operands continue to bypass automatically. Keep failures before create and make
`/session` plus `--exact` the actionable fallback. Remove exported `SessionIDDisplayWidth`; `SessionHandleWidth` is the sole width API.
Preserve invalid-UTF-8 behavior and every debugger evidence/incarnation digest unchanged.

Use the existing client resolver and all-pages inventory helper; do not add another resolver,
decode handles, widen protobuf/server APIs, or implement collision expansion. Add focused projection,
resolver, command-grammar, and request-boundary tests. Do not relocate the UI integration proof or
update broad docs here; task 06 owns those dependent standards changes.

## Acceptance criteria

- AC1.1: A normal generated session ID has a handle containing its first twelve characters;
  header, `/sessions`, debugger target chrome, terminal title, and status input use that same
  literal rather than an ordinary display digest.
  - verify: `TestPredictableSessionHandles_Scenario1_SharedNormalHandle`
- AC1.2: Two IDs with the same fixed handle retain that same twelve-column projection; rendering
  neither expands either token nor loads the complete inventory, and pagination or ordering
  cannot change the displayed literal.
  - verify: `TestPredictableSessionHandles_Scenario1_FixedCollisionBehavior`
- AC1.3: An arbitrary non-empty valid-UTF-8 ID, including a long, shell-significant,
  leading-hyphen, or control-bearing legacy/custom value, produces the documented complete-atom,
  at-most-twelve ASCII-column literal. The literal contains only `[A-Za-z0-9._%-]`, never begins
  with `-`, and preserves non-leading hyphens literally; empty IDs remain corrupt/invalid for
  actionable handling and receive no fabricated handle.
  - verify: `TestPredictableSessionHandles_Scenario1_EscapedUTF8ControlsAndLeadingHyphen`
- AC1.4: Invalid UTF-8 is not repaired or percent-encoded into a new identity: ordinary
  projections emit no handle, and debug creation is not attempted. Existing corrupt-snapshot
  handling and protobuf-boundary UTF-8 behavior remain unchanged.
  - verify: `TestPredictableSessionHandles_Scenario1_InvalidUTF8HasNoHandleOrDebugCreate`
- AC1.5: Header and other normal chrome render the fixed handle without inventory. If positional
  debug cannot obtain a complete caller-visible inventory, it stops before create and directs the
  operator to `/session` plus `debug --exact`; that explicit form still sends each valid non-empty
  exact ID unchanged without inventory.
  - verify: `TestPredictableSessionHandles_Scenario1_InventoryFailureKeepsExplicitExactFallback`
- AC1.6: `SessionHandleWidth` is the sole exported ordinary-handle width constant; the obsolete
  `SessionIDDisplayWidth` compatibility alias is absent, with no change to debugger evidence or
  incarnation digest APIs.
  - verify: `TestPredictableSessionHandles_Scenario1_OnlyHandleWidthAPI`
- AC2.1: Only a syntactically valid positional short token invokes local resolution: it is
  non-empty ASCII, at most twelve columns, begins with `[A-Za-z0-9._]` or a complete uppercase
  `%[0-9A-F]{2}` atom, and thereafter consists of `[A-Za-z0-9._-]` literals or complete uppercase
  escapes. Lowercase, malformed, truncated, leading-hyphen, longer, and other operands remain exact
  IDs and are sent unchanged without inventory lookup. `--exact SESSION_ID` also bypasses inventory;
  it is mutually exclusive with the positional operand in embedded and connected forms.
  - verify: `TestPredictableSessionHandles_Scenario2_HandleGrammarAndExactEscapeHatch`
- AC2.2: For a syntactically valid short token, the complete caller-visible inventory is consulted
  before creation. Repeated rows for one exact ID count once; all distinct projected matches are
  gathered before selection, and multiple matches fail ambiguous even when one full ID exactly
  equals the token.
  - verify: `TestPredictableSessionHandles_Scenario2_AllProjectedMatchesPrecedeSelection`
- AC2.3: A literal short handle from the complete caller-visible inventory resolves uniquely before
  debug-session creation; the request carries the matched full ID, never the short handle. Explicit
  `--exact` sends its supplied valid ID unchanged and remains usable for short exact IDs and
  inventory outage.
  - verify: `TestPredictableSessionHandles_Scenario2_HandleAndExplicitExactPaths`
- AC2.4: Zero matches, distinct projected-ID ambiguity (including an exact-token collision), or an
  inventory error stops before `CreateSession`; each error gives concrete `/session` and `--exact`
  guidance without disclosing rows the caller cannot see.
  - verify: `TestPredictableSessionHandles_Scenario2_FailClosedWithExactGuidance`
