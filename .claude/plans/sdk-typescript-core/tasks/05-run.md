---
id: 05-run
title: Run choreography, stale controls, strict steer
blocked_by: [04-client-session]
status: pending
branch: ""
worktree: ""
issue: "913"
retries: 0
last_error: ""
accumulator: sdk/10-architecture-adr
---

# Task brief

`await session.run(prompt)` and the `Run` object. Scenario 5. ADR 0249
(durable run identity) and ADR 0252 (strict steer). Permission responders
are Scenario 7 — you may emit raw ask events but do not land
`onPermissionAsk` / `resolveAsk` here.

`run()` resolves only after server acceptance **and** the first
run-ID-bearing event. Exactly one consumption mode: iterate events **or**
`run.result()`, not both, not twice. Server terminals (`StopError`,
cancel, limits, budget) resolve normally with typed `RunResult`;
transport/protocol failures throw. Second local `run()` on a busy Session
is `SessionBusyError` with **no** prompt sent.

Every approve/cancel/steer on the **ergonomic** `Run`/`Session` surface
carries `expected_run_id`. Stale controls fail typed; the newer run is
untouched. The **raw** seam leaves `expected_run_id` caller-controlled
(omitting it is how a raw caller opts into legacy promotion). `run.steer()`
is strict and never promotes. `run.cancel()` is a typed cancelled outcome,
not an exception.

Prove with injected Transport, not a live daemon.

Branch `sdk/15-run` off the stack tip. Do not push.

## Acceptance criteria

- AC5.1: `session.run(prompt)` resolves only after the server accepted the
  run and the first run-ID-bearing event arrived; the returned `Run` exposes
  that non-empty run ID.
  - verify: `sdk/typescript/test/run.test.ts :: "run resolves on acceptance with the first run ID"`
- AC5.2: A `Run` is consumed exactly once: iterating it yields the event
  stream; `run.result()` drains and returns `RunResult`; doing both, or
  either twice, fails with a typed invalid-state error.
  - verify: `sdk/typescript/test/run.test.ts :: "a run has exactly one consumption mode"`
- AC5.3: `RunResult` carries stop reason, final text/content, usage, session
  ID, run ID, and the raw terminal event; server terminals — `StopError`,
  cancellation, limits, budget — resolve normally with the typed outcome,
  while a transport/protocol failure rejects.
  - verify: `sdk/typescript/test/run.test.ts :: "server terminals resolve typed, transport failures throw"`
- AC5.4: A second local `run()` on a busy `Session` rejects with
  `SessionBusyError` without sending a prompt; the daemon stays
  authoritative for cross-client contention.
  - verify: `sdk/typescript/test/run.test.ts :: "a second local run is SessionBusyError"`
- AC5.5: Every approve/cancel/steer sent through the ergonomic
  `Run`/`Session` surface carries the active run's `expected_run_id`; a
  control racing a terminal (the run it named is gone) surfaces the
  server's typed stale-control failure and the newer run is observably
  untouched. The raw operation seam leaves `expected_run_id`
  caller-controlled — omitting it is how a raw caller opts into the
  server's legacy behaviour, including steer promotion.
  - verify: `sdk/typescript/test/controls.test.ts :: "ergonomic controls always carry expected_run_id and stale controls fail typed"`
- AC5.6: `run.steer()` is strict — because it always names its run
  (AC5.5), a steer that loses the terminal race is refused, never promoted
  into a new run; the raw seam retains the documented promotion behaviour
  for callers that deliberately omit `expected_run_id`.
  - verify: `sdk/typescript/test/steer.test.ts :: "run.steer never promotes; the raw seam may"`
- AC5.7: `run.cancel()` resolves the run with the cancelled terminal outcome
  through the normal consumption path — cancellation is an outcome, not an
  exception.
  - verify: `sdk/typescript/test/run.test.ts :: "cancel is a typed outcome"`
