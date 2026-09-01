# ADR 0253 — SDK mocking testkit: vendor the proven unary pattern, defer streaming

- Status: Proposed
- Date: 2026-08-31
- Scope: `sdk/typescript/` testing tooling for `@stacklok/mecatl` (issue #872).
- Supersedes: none.
- Superseded by: [ADR 0278](./0278-typescript-sdk-architecture.md), in part — Decisions 1–2 (the vendored unary automocker and its SPDX-relicense surface) are dropped, not replaced by a published mocker; M1 keeps only an injected-Transport seam plus a minimal in-process fake for the SDK's own tests. The npm name `@stacklok/mecatl` is also superseded (`@stacklok/mecatl-sdk`). Decisions 3–4 stand.

## Context

Neither [#761](https://github.com/stacklok/mecatl/issues/761) nor
[#821](https://github.com/stacklok/mecatl/issues/821) describes any story for
**client-side SDK mocking** — the only testing strategy either names is a real
`mecated --mock` daemon, useful for the SDK's own test suite but not for a
downstream app's unit/e2e tests. Issue [#872](https://github.com/stacklok/mecatl/issues/872)
tracks closing that gap. This ADR is the decision record for how.

**Two separate concerns, two separate deadlines, don't conflate them.** (1) The
SDK's own usability — can a downstream app author write tests against
`@stacklok/mecatl` without a real `mecated` running — has a firm near-term
deadline (the conference-demo release). (2) Studio's own testing needs are a
later, separate concern, and Studio's client architecture (a browser SPA
calling `mecated`'s HTTP endpoints directly, versus a BFF that could hold a
real server-side gRPC connection) is not yet decided by anyone. This ADR is
scoped to (1). It does not, and should not, try to pre-decide (2) — doing so
would be opining on an architecture nobody has committed to yet. Concretely:
shipping (1) needs exactly **one** easy way to test SDK-based apps working
before the deadline — the vendored gRPC mock below, or a sufficiently trivial
local `mecated --mock` setup, not necessarily both. Having a REST/JSON mock
for Studio's eventual HTTP surface too would be ideal, but is not a release
gate for (1).

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

This has an explicit ordering dependency: `sdk/typescript/` does not exist on
`main` yet (nor does any TypeScript tooling — no root `package.json`, no
`tsconfig.json`, no TypeScript CI job). This ADR cannot land before
[#821](https://github.com/stacklok/mecatl/issues/821) creates the SDK tree,
and the vendor-drop must land under a CI gate from day one (typecheck, lint,
and the license-header guard in Decision 2) — not silently, since
`task lint && task test` today has nothing to say about a
`sdk/typescript/` tree.

**2. Correct SPDX headers on port, backed by an actual relicense, checked by
CI.** The source carries `SPDX-License-Identifier: Proprietary` (the private
repo's default for that product's app). Stacklok is the copyright holder of
both the source repo and mecatl, so rewriting the header to
`Apache-2.0` on copy is an authorized relicense, not merely a string edit —
recorded here as the decision it actually is, not left implicit. This
follows the approach [#618](https://github.com/stacklok/mecatl/pull/618)
takes for the same class of fix (that PR is still open, not yet an
established precedent, but its shape is right): it ports 9 files with the
same header correction and adds a CI guard —
`git grep -l "SPDX-License-Identifier: Proprietary" -- studio/` failing the
build if a stray header survives. #618's guard is scoped to `studio/` and
would not catch a stray header under `sdk/`; this ADR commits to the same
guard, scoped to `sdk/`, landing with the vendor-drop rather than after it.

**3. Streaming/bidi mocking is explicitly out of scope for this pass, and the
vendored pattern's coverage is gRPC, not HTTP.** `Converse` (bidi) and any
future `WatchSessionEvents` mocking, plus modeling `run_id`/`expected_run_id`
race semantics (the state a mock needs to reproduce the stale-control bug
[#827](https://github.com/stacklok/mecatl/pull/827)/[#828](https://github.com/stacklok/mecatl/pull/828)
fixed) are a **stated residual** — unsolved anywhere surveyed, not just
unsolved here. Named honestly rather than silently dropped, matching
[ADR 0250](./0250-durable-cursors-and-watch.md)'s own residual-disclosure
discipline.

Stated precisely (correcting an earlier draft of this ADR, which conflated
transports): the vendored automocker validates and encodes real **protobuf**
bytes. It covers unary **gRPC** RPCs — the transport Node/Bun `@stacklok/mecatl`
consumers actually speak, including `GetCompatibilityInfo`, `ApproveRun`,
`CancelRun`, and `Steer` over gRPC. It does **not** cover mecated's
hand-written JSON HTTP endpoints (`GET /v1/compatibility`, `POST
.../approve`, `POST .../cancel`, and, once
[#873](https://github.com/stacklok/mecatl/issues/873) ships, `POST
.../steer`) — those are a different wire shape entirely and this pattern
cannot mock them as written. That is fine: per the framing above, this ADR's
release-blocking scope is the SDK's own testability, and the Node/Bun/gRPC
path is the one the near-term demo actually exercises. A REST/JSON mock for
the HTTP endpoints — reusing `AutoAPIMock`'s schema-agnostic core, per the
survey above — is a legitimate, separately-buildable solution for whoever
needs it (Studio's eventual browser-side testing, most plausibly), and can
land independently and later, without this ADR pre-deciding whether or when
it does.

**4. Lives inside mecatl's own SDK tree for now, not a shared package.** No
separate npm package, no dependency on `@stacklok/mocks` or a new sibling
package in `frontend-platform`. A follow-up issue will track the proper
"slot-in" operationalizing — letting other apps in the ecosystem (e.g. a
future cloud-ui integration) reuse this without re-copying it — once a
second real Connect-RPC consumer actually needs it. Building that
generality now, before it is needed, is exactly the premature-abstraction
cost this decision is deliberately deferring.

## Consequences

- **Unary gRPC mocking works from day one**, proven pattern, not a
  from-scratch design — the biggest risk (whether this is even tractable
  before the SDK's own deadline) is retired for the transport the near-term
  demo actually uses.
- **The HTTP/JSON surface (Studio's likely transport) is a separate,
  coexisting concern, deliberately not solved here.** It needs a different
  tool (a REST-shaped mock, not this gRPC one) and a different owner, once
  Studio's client architecture is actually decided. This ADR names both as
  legitimate paths rather than picking one for a problem that isn't scoped
  yet.
- **Streaming/bidi remains a real, known gap.** Any test needing to exercise
  `Converse` or the stale-run-control race still has no mock path and must
  fall back to a real `mecated --mock` daemon until someone builds it. This
  is the honest cost of shipping the unary case now rather than waiting for
  a complete solution.
- **Vendoring, not depending, means this code will drift** from whatever
  `frontend-platform` eventually settles on for its own gRPC-mocking story.
  Accepted for now; the follow-up "slot-in" issue is where that reconciles,
  not this ADR.
- **No cross-repo confidentiality risk, and the relicense is authorized, not
  assumed**: nothing copied is product business logic, Stacklok holds
  copyright on both sides (Decision 2), and headers are corrected on the way
  in — but the follow-up issue should still note the source's provenance so
  nobody mistakes the vendored code for something mecatl invented
  independently.

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
