# ADR 0252 — HTTP steer endpoint: `POST /v1/sessions/{id}/steer`

- Status: Proposed
- Date: 2026-08-31
- Scope: `internal/adapter/server` (`http.go`), the RFC 9457 error registry
  ([ADR 0248](./0248-sdk-compatibility-and-error-contract.md)), mecatl's HTTP
  control surface.
- Supersedes: none — extends [ADR 0232](./0232-steer-while-running.md), which
  explicitly deferred this transport.

## Context

[ADR 0232](./0232-steer-while-running.md) shipped steer-while-running as a new
`steer`/`steer_cancel` oneof arm on the existing bidi gRPC `Converse` stream,
and named the gap on the way out:

> **gRPC-only v1.** ... **HTTP/SSE and ACP steer are deferred** — the HTTP run
> path (`POST /v1/sessions/{id}/prompt`) and ACP's blocking `session/prompt`
> have no client→server mid-run channel; a unary `POST .../steer` (mirroring
> `approve`/`cancel`) is the cheap follow-up shape.

`approve` and `cancel` already have unary HTTP endpoints
(`POST /v1/sessions/{id}/approve`, `POST /v1/sessions/{id}/cancel`). Steer does
not. [Issue #821](https://github.com/stacklok/mecatl/issues/821) (the
`@stacklok/mecatl` TypeScript SDK) settles the transport split for browser
consumers: Node/Bun get real gRPC (including full-duplex `Converse`), but
browsers deliberately get HTTP/SSE only, never gRPC-Web — "without
introducing another application protocol." That means any browser-only
consumer (Studio included) can never steer a running agent today, not because
of a hard architectural limit, but because the "cheap follow-up" ADR 0232
named was never filed as tracked work. It now is: issue
[#873](https://github.com/stacklok/mecatl/issues/873).

[PR #827](https://github.com/stacklok/mecatl/pull/827) and
[#828](https://github.com/stacklok/mecatl/pull/828) already landed durable
`run_id`/`expected_run_id` and hardened strict-steer semantics on the gRPC
path. This ADR does not revisit that state machine — it is purely about
giving the *existing* `Service.Steer`/`Service.CancelSteer` calls a second,
HTTP entry point, on the same terms the gRPC one already has.

## Decision

- **Two new unary HTTP endpoints, mirroring the existing control routes
  exactly:**
  - `POST /v1/sessions/{id}/steer` — enqueues (or appends to) the pending
    steer text for the session's active run.
  - `POST /v1/sessions/{id}/steer-cancel` — cancels the pending steer bundle,
    matching the existing `cancel-child` naming convention
    (`internal/adapter/server/http.go`).
- **Same request contract as the gRPC frame.** Body carries the steer text, a
  client-minted `message_id` (per ADR 0232's watermark correlation), and an
  optional `expected_run_id` — identical semantics to the gRPC oneof arm and
  to the existing `approve`/`cancel` HTTP bodies' optional `expected_run_id`
  (PR #828).
- **Same outcome vocabulary, over HTTP status + body.** The closed
  `SteerOutcome` enum (`accepted`/`appended`/`retracted`/`none_pending`/
  `too_late`) that already rides the gRPC ack is returned as the HTTP
  response body on `200`. A stale `expected_run_id` is refused as `409` +
  `application/problem+json`, code `stale_run_control` — reusing the
  registry from [ADR 0248](./0248-sdk-compatibility-and-error-contract.md),
  not a bespoke error path.
- **No change to the engine.** `internal/adapter/server` routes directly into
  the existing `Service.Steer`/`Service.CancelSteer` calls — the same calls
  the gRPC handler already invokes. The steer inbox, its mutex, its
  watermark correlation, and the promote-on-terminal-race behaviour in ADR
  0232 are untouched.
- **`ServerCapabilities.steer` is unaffected.** The capability bit already
  means "this server can be steered." It does not encode *which transport*
  can steer — a client discovers HTTP steer support the same way it
  discovers any other HTTP route: by it existing (or 404ing) on the version
  it is talking to.
- **ACP steer stays deferred.** ACP's blocking `session/prompt` still has no
  mid-run channel; this ADR does not touch it. Named again here only so a
  future reader does not read "HTTP steer shipped" as "all deferred
  transports shipped."

## Consequences

- **Browser-only consumers can steer a running agent for the first time.**
  Studio, or any `@stacklok/mecatl` consumer that never runs a Node/Bun
  backend holding a real gRPC connection, gains parity with `mecatui` on this
  one control operation.
- **The HTTP control surface becomes symmetric.** `approve`, `cancel`, and
  `steer` are now all reachable unary HTTP operations; a client no longer
  needs to special-case steer as "the one gRPC-only control."
- **Costs, stated honestly:**
  - A steer sent over HTTP still competes for the same single-slot, run-scoped
    steer inbox as a gRPC-originated one — nothing here changes the
    at-most-one-pending-bundle contract, so two clients steering the same run
    from different transports still append, exactly as two gRPC clients
    would.
  - HTTP has no persistent connection to hand back a promoted follow-up run
    on, unlike gRPC's Converse-stream handoff (ADR 0232's terminate-window
    promotion). A steer that loses the terminal race over HTTP gets the
    `SteerOutcome`/promotion result in its own response, not a second frame
    on a held stream; the client learns the new run id from that response
    and must poll/attach separately if it wants to follow it. This is a real
    ergonomic gap versus the gRPC path, accepted here rather than solved —
    solving it is the SDK's `session.attach()` job (ADR 0250), not this
    endpoint's.
  - ACP steer remains unsolved; this ADR does not reduce that scope.

## See also

- [ADR 0232](./0232-steer-while-running.md) — the steer-while-running design
  this ADR extends; names this exact follow-up.
- [ADR 0248](./0248-sdk-compatibility-and-error-contract.md) — the RFC 9457
  problem-details registry this endpoint's error path reuses.
- [ADR 0249](./0249-durable-run-identity.md) — durable `run_id` and
  `expected_run_id`, which this endpoint's request contract mirrors from the
  gRPC path.
- [PR #828](https://github.com/stacklok/mecatl/pull/828) — `expected_run_id`
  on controls, strict steer (gRPC).
- [Issue #873](https://github.com/stacklok/mecatl/issues/873) — the tracked
  work this ADR is the decision record for.
- [Issue #821](https://github.com/stacklok/mecatl/issues/821) — the SDK
  proposal whose browser-transport split (HTTP/SSE only, no gRPC-Web) is why
  this gap matters now.
