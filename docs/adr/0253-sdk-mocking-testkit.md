# ADR 0253 — SDK mocking testkit: vendor the proven unary pattern, defer streaming

- Status: Proposed
- Date: 2026-08-31
- Scope: `sdk/typescript/` testing tooling for `@stacklok/mecatl` (issue #872).
- Supersedes: none.

## Context

Neither [#761](https://github.com/stacklok/mecatl/issues/761) nor
[#821](https://github.com/stacklok/mecatl/issues/821) describes any story for
**client-side SDK mocking** — the only testing strategy either names is a real
`mecated --mock` daemon, useful for the SDK's own test suite but not for a
downstream app's unit/e2e tests. Issue [#872](https://github.com/stacklok/mecatl/issues/872)
tracks closing that gap. This ADR is the decision record for how.

**Survey of what already exists, checked directly rather than assumed:**

- `stacklok/toolhive-studio` and `stacklok/toolhive-cloud-ui` (both public,
  Apache-2.0) have a mature MSW-based REST/OpenAPI auto-mocker, but **zero**
  Connect-RPC/gRPC support — confirmed by code search, not inference.
- `stacklok/frontend-platform`'s `@stacklok/mocks` package (public,
  Apache-2.0, real npm-publishable) extracts that same REST auto-mocker
  (`AutoAPIMock`, `buildServerHandlers()`, `mocker.ts`'s codegen) per its own
  [ADR 0014](https://github.com/stacklok/frontend-platform/blob/main/docs/adr/0014-shared-sdk-and-mock-tooling.md).
  `AutoAPIMock<T>`'s core wrapper (fixtures, `.override()`, scenarios) is
  genuinely schema-agnostic — it would work over protobuf-generated types as
  well as OpenAPI-derived ones. `buildServerHandlers()`/the codegen half are
  OpenAPI-route-shaped and do not fit Connect-RPC's wire shape at all.
- A private, unmerged prototype (a sibling internal repository exploring
  Connect-RPC mocking for a different product's frontend) has already solved
  the **unary** case: a `buildGrpcServerHandlers()` that walks any
  `GrpcServiceDescriptor`, decodes and validates the real request bytes
  against the protobuf schema, resolves a hand-authored fixture per RPC
  method, and a `grpcResponse()` helper that encodes a real protobuf message
  to binary bytes for the fixture response. This is not business logic and
  not confidential — it just hasn't finished migrating to the shared
  `frontend-platform` home yet, and its config doesn't match mecatl's
  services out of the box.
- That same prototype's code is explicit about what it does **not** do — the
  thrown error reads verbatim: *"streaming gRPC methods aren't supported by
  the automocker yet."* mecatl's `Converse` RPC is bidi. Nothing, anywhere
  surveyed, mocks a streaming or bidi Connect-RPC call today.

## Decision

**1. Vendor, don't depend.** Copy and adapt the AutoAPIMock core and the
unary `buildGrpcServerHandlers()`/`grpcResponse()` pattern directly into
`sdk/typescript/`, taking the most current version (currently still in the
private sibling repo, not yet in `frontend-platform`), reconfigured for
mecatl's actual service definitions. This iteration does **not** take
`@stacklok/mocks` as an npm dependency — the code becomes mecatl's own,
matching the same "copy what's needed, simplify pragmatically" discipline
used elsewhere in this stack rather than forcing a shared-package dependency
before one is actually warranted.

**2. Correct SPDX headers on port.** The source carries
`SPDX-License-Identifier: Proprietary` (the private repo's default for that
product's app). mecatl is Apache-2.0; headers are corrected to match on
copy — same discipline [#618](https://github.com/stacklok/mecatl/pull/618)'s
vendor-drop already established for exactly this class of fix.

**3. Streaming/bidi mocking is explicitly out of scope for this pass.**
`Converse` (bidi) and any future `WatchSessionEvents` mocking, plus modeling
`run_id`/`expected_run_id` race semantics (the state a mock needs to
reproduce the stale-control bug [#827](https://github.com/stacklok/mecatl/pull/827)/[#828](https://github.com/stacklok/mecatl/pull/828)
fixed) are a **stated residual** — unsolved anywhere surveyed, not just
unsolved here. Named honestly rather than silently dropped, matching
[ADR 0250](./0250-durable-cursors-and-watch.md)'s own residual-disclosure
discipline. The unary vendored pattern covers `GetCompatibilityInfo`,
`approve`, `cancel`, and (once #873 ships) `steer` over HTTP; it does not
cover anything requiring a live, stateful bidi stream.

**4. Lives inside mecatl's own SDK tree for now, not a shared package.** No
separate npm package, no dependency on `@stacklok/mocks` or a new sibling
package in `frontend-platform`. A follow-up issue will track the proper
"slot-in" operationalizing — letting other apps in the ecosystem (e.g. a
future cloud-ui integration) reuse this without re-copying it — once a
second real Connect-RPC consumer actually needs it. Building that
generality now, before it is needed, is exactly the premature-abstraction
cost this decision is deliberately deferring.

## Consequences

- **Unary RPC mocking works from day one**, proven pattern, not a from-scratch
  design — the biggest risk (whether this is even tractable before the SDK's
  own deadline) is retired.
- **Streaming/bidi remains a real, known gap.** Any test needing to exercise
  `Converse` or the stale-run-control race still has no mock path and must
  fall back to a real `mecated --mock` daemon until someone builds it. This
  is the honest cost of shipping the unary case now rather than waiting for
  a complete solution.
- **Vendoring, not depending, means this code will drift** from whatever
  `frontend-platform` eventually settles on for its own gRPC-mocking story.
  Accepted for now; the follow-up "slot-in" issue is where that reconciles,
  not this ADR.
- **No cross-repo confidentiality risk**: nothing copied is product business
  logic, and headers are corrected on the way in — but the follow-up issue
  should still note the source's provenance so nobody mistakes the vendored
  code for something mecatl invented independently.

## See also

- [Issue #872](https://github.com/stacklok/mecatl/issues/872) — the tracked
  work this ADR is the decision record for.
- [ADR 0248](./0248-sdk-compatibility-and-error-contract.md),
  [ADR 0249](./0249-durable-run-identity.md) — the RPCs this testkit's unary
  pattern needs to mock first.
- [ADR 0250](./0250-durable-cursors-and-watch.md) — the "stated residual"
  disclosure discipline this ADR mirrors for the streaming gap.
- [PR #618](https://github.com/stacklok/mecatl/pull/618) — precedent for
  correcting stray `Proprietary` SPDX headers on a vendor drop.
