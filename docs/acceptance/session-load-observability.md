# Session-load observability — acceptance plan

**Phase:** capability — actionable operator visibility without caller disclosure
**Status:** landed
**Issue:** [stacklok/mecatl#1125](https://github.com/stacklok/mecatl/issues/1125)
**Branch:** `acc/session-load-observability`

## Goal and boundary

When an owned session cannot be loaded, the operator should be able to distinguish a
backend/retrieval failure from a corrupt or invalid snapshot without learning which
session was requested. The caller-facing result remains the same absence-style
`NotFound` for a missing, foreign, or load-failed session.

The implementation adds one closed load-failure classification in `engine/port`,
propagated through wrapped errors rather than server string matching:
`store`, `snapshot`, and `unknown`. Built-in snapshot-backed stores classify
retrieval/transport failures as `store` and decode/validation failures as `snapshot`;
a genuine `ErrSessionNotFound` is not a failure. Under `OwnershipEnforced`,
`Service.GetSession` emits one bounded WARN and increments
`mecatl_session_load_failures_total{class=...}` once per non-not-found load failure.
The only metric label is the closed `class`. For its bounded WARN, `Service.GetSession`
uses a detached clean context and supplies only the direct fields `class` and constant
`ownership=enforced`; it never supplies a session ID, principal, Redis key/path, raw
error, blob content, or blob size. Attributes deliberately pre-bound by the trusted
operator-supplied `port.Diagnostics` sink are outside this producer's control. Nil
telemetry is a no-op.

This follows [ADR 0212 caller ownership](../adr/0212-caller-ownership-enforcement.md)
(the absence contract), [ADR 0020 diagnostics](../adr/0020-diagnostics.md) (injected,
low-volume operational diagnostics), [ADR 0027 cloud-native state](../adr/0027-cloud-native.md),
the [architecture guide](../architecture.md), and the [AGENTS.md invariants](../../AGENTS.md).

### Scenario 1 — an owned load failure is actionable only to the operator

With ownership enforcement enabled, exercise the same service load boundary with a
missing session, a foreign session, a store/retrieval failure, and a snapshot
decode/validation failure. Missing and foreign requests remain indistinguishable from
each other and from load failures to the caller. The two real failures expose only
their closed class to the operator and metrics, exactly once per failed load. Wrapped
errors preserve classification across the store-to-service boundary; unknown wrapped
failures fail closed into `unknown`. No event, proto, caller-facing error, admin
introspection endpoint, repetition cache, or legacy-snapshot read path is introduced.

**Acceptance:**

- AC1.1: `ErrSessionNotFound`, a foreign owner, and every classified load failure all return the same caller-visible NotFound outcome, with no target, principal, storage locator, or raw cause disclosed.
  - verify: `TestADR_0212_SessionLoadObservability_Scenario1_NotFoundConcealsMissingForeignAndLoadFailure`
- AC1.2: Built-in snapshot-backed stores classify retrieval/transport failures as `store`, decode/validation failures as `snapshot`, preserve classification through wrapping with typed `errors.Is`/`errors.As` contracts rather than error text, and leave genuine `ErrSessionNotFound` silent; unrecognized failures and adversarial lookalike error strings classify as `unknown`.
  - verify: `TestSessionLoadFailureClassificationFromWrappedErrors`
- AC1.3: Under `OwnershipEnforced`, one public `GetSession` invocation that encounters a non-not-found load failure emits at most one WARN and increments `mecatl_session_load_failures_total` at most once, regardless of store wrapping. `Service.GetSession` uses a detached clean diagnostics context and adds only the closed `class` plus constant `ownership=enforced` direct fields; it never adds request target, principal, path, cause, blob content, or blob size data. Attributes deliberately pre-bound by the trusted operator-supplied `port.Diagnostics` sink are outside this producer's control. The metric carries only `class=store|snapshot|unknown`; nil telemetry suppresses only the metric while the diagnostic remains.
  - verify: `TestADR_0212_SessionLoadObservability_Scenario1_BoundedWarningAndMetric`
- AC1.4: The load-failure class is a closed `engine/port` contract with no server-side string matching, and adding it does not change events, protobufs, caller-facing APIs, or the ownership decision.
  - verify: `TestSessionLoadFailureClassificationIsClosedAndPortOwned`

## One eventual implementation task

1. Add the closed classification and wrapping helpers to `engine/port`; update each
   built-in snapshot-backed store's error boundary; instrument the ownership-enforced
   `Service.GetSession` path with the existing injected diagnostics and telemetry
   seams; add the scenario tests and redaction assertions; then update the living
   [observability architecture](../architecture/observability.md), relevant
   design notes, and user-facing [observability documentation](../../user-docs/building/what-you-get/observability.md).
   If the exported port changes, refresh the engine API baseline and add the required
   `engine/CHANGELOG.md` entry. Do not add retained repetition state, an admin
   introspection endpoint, or legacy-session reading in this task.

## Out of scope

- Legacy-session reading or a separate non-runnable snapshot projection; this needs a
  separate medium-sized security/API design.
- Repetition detection or retained keyed state, including its lifecycle, cardinality,
  and privacy policy; the counter supplies rate observability.
- An admin introspection endpoint; loopback is not an authorization boundary.
- Any event, protobuf, public caller error, ownership-policy, or target-bearing log
  change.

## Definition of done

- The scenario tests above pass, including no-leak assertions for WARN fields and
  metric labels, and the implementation has no target-bearing load-failure output.
- If the port surface is exported, `task api:update` has been run intentionally and
  the API baseline plus `engine/CHANGELOG.md` are updated.
- `task lint`, `task test`, `task docs`, and `task ac-trace-strict` pass in the
  implementation worktree; `task site:build` passes for the user-doc update.
- `go run ./cmd/mecademo` still prints the full offline turn → tool.call →
  permission.ask + approval → result flow.
