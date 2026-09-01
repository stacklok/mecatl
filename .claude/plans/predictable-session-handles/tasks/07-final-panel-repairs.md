---
id: 07-final-panel-repairs
title: Final debugger grammar and ADR supersession repairs
blocked_by: [05-resolver-exact-semantics-api-cleanup, 06-standards-docs-integration-relocation]
status: in-progress
branch: ""
worktree: ".scratch/worktrees/issue-922-task07"
issue: "922"
retries: 0
last_error: ""
accumulator: acc/predictable-session-handles
---

# Task brief

Close the final panel findings without widening scope. Make the CLI error for a positional leading-hyphen exact ID explicitly direct the operator to `debug --exact SESSION_ID`; keep leading hyphen disallowed positionally so it cannot be confused with flags. Update the connected debugger grammar paragraph to include its `--exact` form. Record ADR-0280 as superseding ADR-0254's `DEBUG target #<digest>` presentation clause only and retain the scoped backlink already added to ADR-0254; do not alter debugger authority/evidence/incarnation decisions.

## Acceptance criteria

- AC2.1: Only a syntactically valid positional short token invokes local resolution: it is
  non-empty ASCII, at most twelve columns, begins with `[A-Za-z0-9._]` or a complete uppercase
  `%[0-9A-F]{2}` atom, and thereafter consists of `[A-Za-z0-9._-]` literals or complete uppercase
  escapes. Lowercase, malformed, truncated, longer, and other operands accepted positionally remain
  exact IDs and bypass inventory. A leading-hyphen exact ID uses `--exact SESSION_ID` so it cannot be
  mistaken for a flag; the exact form also bypasses inventory and is mutually exclusive with the
  positional operand in embedded and connected forms.
  - verify: `TestPredictableSessionHandles_Scenario2_HandleGrammarAndExactEscapeHatch`
- AC3.1: `mecatui debug` and `mecatui connect ADDRESS debug` help accurately distinguish a
  positional exact ID or displayed short handle from the explicit `--exact SESSION_ID` bypass,
  state their mutual exclusivity, direct ambiguous or inventory-failed input to `/session` plus
  `--exact`, explain that a leading-hyphen exact ID requires `--exact`, and show no leading `#`
  marker.
  - verify: `TestPredictableSessionHandles_Scenario3_CommandHelpAndExactBypass`
- AC3.5: ADR-0280 explicitly supersedes only ADR-0217's ordinary display-handle decision and
  ADR-0254's `DEBUG target #<digest>` presentation clause; both older ADRs carry scoped backlinks,
  while every debugger authority, evidence, and incarnation decision remains in force.
  - verify: inspection — `task docs` validates ADR metadata and links
