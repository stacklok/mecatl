# ADR 0252 — HTTP steer endpoint: `POST /v1/sessions/{id}/steer`

- Status: Accepted
- Date: 2026-08-31
- Scope: `internal/adapter/server` (`http.go`, `grpc.go`, `service.go`), the RFC 9457 error registry
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
  - `POST /v1/sessions/{id}/cancel-steer` — cancels the pending steer bundle,
    matching the existing `cancel-child` naming convention
    (`internal/adapter/server/http.go`): the verb leads, the noun follows.
- **Same request contract as the gRPC frame, parts included.** Body carries
  the steer text, the same `parts` array the HTTP prompt body already
  accepts (`promptContentBody`, `internal/adapter/server/http.go`), a
  client-minted `message_id` (per ADR 0232's watermark correlation), and an
  optional `expected_run_id` — identical semantics to the gRPC oneof arm
  (which carries both text and parts per [ADR 0251](./0251-multimodal-steer.md))
  and to the existing `approve`/`cancel` HTTP bodies' optional
  `expected_run_id` (PR #828). Parts decode and validate through the SAME
  path the prompt body already uses (`toContentParts` →
  `session.NewContent` + `session.ValidateMediaParts`) and are gated by the
  same `ProviderCapabilities()` check the ACP prompt path uses — no second
  validation path. Dropping parts would regress exactly the browser clients
  this endpoint exists for to text-only steer, one release behind the gRPC
  path.
- **Strict, bounded control bodies.** Both endpoints reject unknown fields,
  trailing JSON, and bodies larger than 32 MiB. A steer body is required and
  must contain text or at least one part. A cancel-steer body is optional; an
  empty body is equivalent to `{}`. `message_id` remains optional on both
  endpoints, but a supplied value longer than 64 Unicode code points is
  rejected rather than truncated so acknowledgements and watermark echoes stay
  identical.
- **Same outcome vocabulary, over HTTP status + body.** The closed
  `SteerOutcome` enum (`accepted`/`appended`/`retracted`/`none_pending`/
  `too_late`) that already rides the gRPC ack is returned as the HTTP
  response body on `200`. The single `409` + `application/problem+json`,
  code `stale_run_control` — reusing the registry from
  [ADR 0248](./0248-sdk-compatibility-and-error-contract.md), not a bespoke
  error path — covers **two distinct refusals** that both funnel through
  the same sentinel: (1) an `expected_run_id` naming a run that is not the
  session's current one, and (2) strict steer (ADR 0249): `expected_run_id`
  is set *and* the named run has already gone terminal, which deliberately
  refuses rather than promotes — the caller asked to say something to run
  X, not to start a new run. Case 2 is the one an SDK author most needs
  stated, since it is the opposite of the unqualified-steer promotion
  default described below.
- **Cancel uses the same run guard.** `cancel-steer` accepts
  `expected_run_id`, and the Service checks it atomically with finding and
  retracting the pending bundle. A qualified request returns
  `409 stale_run_control` when that exact run is absent or has been replaced.
  An unqualified request remains idempotent and returns `200` with
  `{"outcome":"none_pending"}` when there is nothing to retract.
- **A promoted follow-up run is drained by the handler, never handed back
  bare.** `Service.Steer` can return a *registered* `promotedRun` whose
  caller "must drain + FinishRun" (`service.go`'s own doc comment) — the
  same contract `StartRunContent` hands every other caller. The HTTP endpoint
  remains unconditionally unary: it returns
  `{"outcome":"too_late","promoted":true,"run_id":"..."}` and drains the
  promoted run in a request-detached background relay. That relay records its
  events and deregisters the run. The response never changes to SSE based on
  `http.Flusher`, so clients have one deterministic response contract and the
  promoted run is never left registered without a consumer.
- **One Service owns admission and cancellation.** HTTP and gRPC both call
  `Service.Steer` and `Service.CancelSteer`; the live-run lookup,
  `expected_run_id` check, inbox transition, and watermark update happen under
  the Service lock. A stale or malformed gRPC control is refused without
  terminating the `Converse` stream: steer acknowledges `too_late`,
  cancel-steer acknowledges `none_pending`, and the bounded diagnostic records
  the refusal. The engine inbox and promote-on-terminal-race behavior from ADR
  0232 otherwise remain unchanged.
- **`ServerCapabilities.steer` is unaffected; the feature registry gets a
  new row.** The capability bit already means "this server can be
  steered" and does not encode *which transport* can steer. Discoverability
  is NOT "404 until you try it" — PR #823's feature registry
  (`internal/adapter/server/features.go`) exists precisely so #821-family
  additions self-describe, and its own comment says each PR in this stack
  "appends its own identifier as it lands." This ADR adds
  `FeatureHTTPSteer = "http_steer"` to `allFeatures`.
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
  - HTTP has no persistent client-to-server control stream on which to hand
    back a promoted follow-up run. A steer that loses the terminal race gets a
    unary acknowledgement containing the new `run_id`; the server drains and
    records that run in the background. A client that wants to watch it uses
    the durable watch API with the returned run ID. This is the SDK's
    `session.attach()` concern (ADR 0250), not a second response mode on this
    endpoint.
  - ACP steer remains unsolved; this ADR does not reduce that scope.

## See also

- [ADR 0232](./0232-steer-while-running.md) — the steer-while-running design
  this ADR extends; names this exact follow-up.
- [ADR 0248](./0248-sdk-compatibility-and-error-contract.md) — the RFC 9457
  problem-details registry this endpoint's error path reuses.
- [ADR 0249](./0249-durable-run-identity.md) — durable `run_id` and
  `expected_run_id`, which this endpoint's request contract mirrors from the
  gRPC path.
- [ADR 0251](./0251-multimodal-steer.md) — multimodal steer parts, which this
  endpoint's request contract must carry alongside text.
- [PR #823](https://github.com/stacklok/mecatl/pull/823) — `GetServerInfo`
  and the feature registry this endpoint's discoverability row lands in.
- [PR #828](https://github.com/stacklok/mecatl/pull/828) — `expected_run_id`
  on controls, strict steer (gRPC).
- [Issue #873](https://github.com/stacklok/mecatl/issues/873) — the tracked
  work this ADR is the decision record for.
- [Issue #821](https://github.com/stacklok/mecatl/issues/821) — the SDK
  proposal whose browser-transport split (HTTP/SSE only, no gRPC-Web) is why
  this gap matters now.
