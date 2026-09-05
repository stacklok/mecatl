---
id: 01-observability
title: Classify and instrument session-load failures
blocked_by: []
status: pending
attempt: 0
branch: ""
worktree: ""
issue: "1125"
retries: 0
last_error: ""
accumulator: acc/session-load-observability
---

# Task brief

Add target-free, ownership-safe operator observability for non-not-found session-load failures. Introduce a closed typed load-failure classification at the engine port boundary; classify retrieval versus snapshot failures in built-in snapshot-backed stores; preserve caller-facing NotFound concealment; emit one bounded diagnostic and one bounded-cardinality metric; wire telemetry through composition; update API baselines, compatibility notes, architecture/design notes, and public observability documentation. Do not add legacy-session reading, repetition state, an admin endpoint, events, or protocol changes.

## Acceptance criteria

- AC1.1: `ErrSessionNotFound`, a foreign owner, and every classified load failure all return the same caller-visible NotFound outcome, with no target, principal, storage locator, or raw cause disclosed.
  - verify: `TestADR_0212_SessionLoadObservability_Scenario1_NotFoundConcealsMissingForeignAndLoadFailure`
- AC1.2: Built-in snapshot-backed stores classify retrieval/transport failures as `store`, decode/validation failures as `snapshot`, preserve classification through wrapping with typed `errors.Is`/`errors.As` contracts rather than error text, and leave genuine `ErrSessionNotFound` silent; unrecognized failures and adversarial lookalike error strings classify as `unknown`.
  - verify: `TestSessionLoadFailureClassificationFromWrappedErrors`
- AC1.3: Under `OwnershipEnforced`, one public `GetSession` invocation that encounters a non-not-found load failure emits at most one WARN and increments `mecatl_session_load_failures_total` at most once, regardless of store wrapping. The final rendered diagnostic record—message, direct fields, and inherited attributes—is limited to the bounded class and optional constant ownership marker and contains no session ID, principal, Redis key/path, raw or wrapped error, blob content, or blob size. The metric carries only `class=store|snapshot|unknown`; nil telemetry suppresses only the metric while the diagnostic remains.
  - verify: `TestADR_0212_SessionLoadObservability_Scenario1_BoundedWarningAndMetric`
- AC1.4: The load-failure class is a closed `engine/port` contract with no server-side string matching, and adding it does not change events, protobufs, caller-facing APIs, or the ownership decision.
  - verify: `TestSessionLoadFailureClassificationIsClosedAndPortOwned`
