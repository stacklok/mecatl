---
id: 07-permissions
title: Permission asks and run.resolveAsk
blocked_by: [06-events]
status: pending
branch: ""
worktree: ""
issue: "915"
retries: 0
last_error: ""
accumulator: sdk/10-architecture-adr
---

# Task brief

Permission-ask choreography on `Run`. Scenario 7. The SDK adds client
choreography, never a second permission policy. Ask/verdict semantics
mirror the server (deny-dominant, ask taxonomy, ADR-0044 askID grammar).

Raw permission-ask events are **always** emitted — `onPermissionAsk` is an
addition, never a filter. First accepted verdict wins. Callbacks run
concurrently keyed by ask ID, with an `AbortSignal` and **no invented
timeout**. Thrown or abstaining callback leaves the ask pending for manual
resolution. Duplicate/late verdicts return typed already-resolved.

**Manual surface is `run.resolveAsk(askId, verdict)`** — asks are
run-scoped. AbortSignal fires on manual resolution and on run termination.

Prove with injected Transport. Do not add `--mock-script` (Scenario 9).

Branch `sdk/17-permissions` off the stack tip. Do not push.

## Acceptance criteria

- AC7.1: With an `onPermissionAsk` responder installed, the raw
  permission-ask event still reaches the event stream — the responder is an
  addition, never a filter.
  - verify: `sdk/typescript/test/permissions.test.ts :: "raw asks are always emitted"`
- AC7.2: Concurrent asks run their callbacks concurrently keyed by ask ID;
  for one ask, the first accepted verdict wins and later verdicts for the
  same ask return the typed already-resolved error.
  - verify: `sdk/typescript/test/permissions.test.ts :: "first accepted verdict wins per ask"`
- AC7.3: A callback that throws or abstains leaves the ask pending; a
  subsequent manual resolution succeeds.
  - verify: `sdk/typescript/test/permissions.test.ts :: "thrown or abstaining callbacks leave the ask pending"`
- AC7.4: The callback's `AbortSignal` fires on manual resolution and on run
  termination; the SDK imposes no timeout of its own.
  - verify: `sdk/typescript/test/permissions.test.ts :: "abort fires on manual resolution and run end"`
- AC7.5: A verdict for an ask that no longer exists (resolved, retracted, or
  from a prior run) fails with the typed already-resolved error and mutates
  nothing.
  - verify: `sdk/typescript/test/permissions.test.ts :: "late verdicts fail typed"`
