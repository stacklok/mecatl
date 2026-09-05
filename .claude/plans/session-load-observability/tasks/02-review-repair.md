---
id: 02-review-repair
title: Close observability review gaps
blocked_by: [01-observability]
status: in-progress
attempt: 1
branch: "plan-session-load-observability/02-review-repair-attempt-1"
worktree: ".scratch/worker-session-load-observability-02-review-repair-attempt-1"
issue: "1125"
retries: 0
last_error: ""
accumulator: acc/session-load-observability
---

# Task brief

Repair the panel-review findings without widening scope:

1. Production `app.Build` must actually wire the diagnostics sink used by `Service.GetSession`; add a real composition-level regression test proving the bounded warning survives the factory path together with the metric callback.
2. Prevent request-context baggage from entering the ownership-concealed warning; use a detached clean context for this target-free operator fact and test the final diagnostic record where practical. Do not expose target or raw cause.
3. Classify `memstore` snapshot restore failures as `snapshot`, with regression coverage.
4. Make Redis reject a decoded snapshot whose persisted ID does not equal the requested key ID, classifying it as `snapshot`, with coverage.
5. Add gRPC-driver tests for malformed snapshot payload and mismatched returned ID classification.
6. Fix public docs: mark the acceptance-plan index entry landed; qualify public port names in compatibility docs; clarify that the new target-free counter is an exception to the role-label statement; define each class and give safe class-specific operator next actions.
7. Keep caller-facing NotFound concealment, the closed label set, events/protos, and all legacy-read/admin/repetition deferrals unchanged.

## Protected acceptance criteria

- AC1.1: `ErrSessionNotFound`, a foreign owner, and every classified load failure all return the same caller-visible NotFound outcome, with no target, principal, storage locator, or raw cause disclosed.
  - verify: `TestADR_0212_SessionLoadObservability_Scenario1_NotFoundConcealsMissingForeignAndLoadFailure`
- AC1.2: Built-in snapshot-backed stores classify retrieval/transport failures as `store`, decode/validation failures as `snapshot`, preserve classification through wrapping with typed `errors.Is`/`errors.As` contracts rather than error text, and leave genuine `ErrSessionNotFound` silent; unrecognized failures and adversarial lookalike error strings classify as `unknown`.
  - verify: `TestSessionLoadFailureClassificationFromWrappedErrors`
- AC1.3: Under `OwnershipEnforced`, one public `GetSession` invocation that encounters a non-not-found load failure emits at most one WARN and increments `mecatl_session_load_failures_total` at most once, regardless of store wrapping. The final rendered diagnostic record—message, direct fields, and inherited attributes—is limited to the bounded class and optional constant ownership marker and contains no session ID, principal, Redis key/path, raw or wrapped error, blob content, or blob size. The metric carries only `class=store|snapshot|unknown`; nil telemetry suppresses only the metric while the diagnostic remains.
  - verify: `TestADR_0212_SessionLoadObservability_Scenario1_BoundedWarningAndMetric`
- AC1.4: The load-failure class is a closed `engine/port` contract with no server-side string matching, and adding it does not change events, protobufs, caller-facing APIs, or the ownership decision.
  - verify: `TestSessionLoadFailureClassificationIsClosedAndPortOwned`
