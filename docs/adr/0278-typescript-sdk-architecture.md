# ADR 0278 — TypeScript SDK architecture: Connect-ES transport, protobuf-es codegen, in-repo pnpm project

- Status: Proposed
- Date: 2026-09-01
- Scope: `sdk/typescript/` (the `@stacklok/mecatl` package), `buf.gen.yaml`, the root Taskfile/CI wiring for the SDK tree.
- Supersedes: [ADR 0253](./0253-sdk-mocking-testkit.md) Decisions 1–2, in part — the vendored unary automocker and the SPDX-relicense surface it carried (see "The mocking consequence" below). ADR 0253's Decisions 3–4 and its scoping of Studio's separate needs stand.

## Context

[#761](https://github.com/stacklok/mecatl/issues/761) and
[#821](https://github.com/stacklok/mecatl/issues/821) settled almost the whole
SDK design contract before any code: one Apache-2.0 package
`@stacklok/mecatl`, in-repo at `sdk/typescript/` with an exactly-pinned
pnpm 11 and its own lockfile (the existing `website/` tree stays on npm);
ESM-only unbundled transpiled modules with declarations and source maps;
subpath exports `.` (isomorphic core + the HTTP/SSE transport), `./node`
(gRPC transport, `spawn()`, `tool()`), `./gen` (generated protobuf types and
service descriptors); Node 22+, Bun 1.4+, the latest two stable
Chrome/Firefox/Safari; Windows is remote `connect()` only in v0.1; develop on
TypeScript 6 while emitting declarations compatible with TypeScript 5.7+.
This ADR records that contract rather than re-deciding it.

The one load-bearing item the issues point at but never settle is the
**transport client library** for the Node/Bun gRPC path. It is on the
critical path for two reasons:

- **Streaming carries the choreography.** Across `HarnessService` and
  `ScheduleService` the proto is heavily unary by count, but the handful of
  streaming RPCs (`Converse`, `StreamSessionEvents`, `StreamSessionLive`,
  `WatchSessionEvents`, `RunTeam`, `ApprovePlan`) carry permissions,
  cancellation, strict steer, asks, stale-control guards, and durable
  attachment — everything the ergonomic layer exists for. A client library
  that only handles unary well is the wrong choice regardless of how the RPC
  count reads.
- **The already-merged [ADR 0253](./0253-sdk-mocking-testkit.md)** decided to
  vendor a byte-level protobuf automocker out of a private prototype —
  explicitly unary-only, with streaming named as an unsolved residual, and
  with an SPDX relicensing decision attached to the vendor drop. That ADR
  never evaluated `createRouterTransport`, Connect-ES's in-memory fake
  Transport. Whether the mocker still earns its place is a direct consequence
  of the transport choice, so it must be resolved here, on the record.

The realistic candidates were **Connect-ES v2**
(`@connectrpc/connect` + `@connectrpc/connect-node`, over protobuf-es) and
**`@grpc/grpc-js`** (with protobuf-es or ts-proto for types). grpc-js brings
a callback-flavored API, an awkward streaming surface, a weaker Bun story,
and no in-memory transport primitive. Connect-ES speaks real gRPC over
HTTP/2 from Node/Bun, exposes async-iterator streaming that maps directly
onto the SDK's `Run` iteration contract, shares the protobuf-es type system
the `./gen` export needs anyway, and ships `createRouterTransport` — an
in-memory Transport that runs real protobuf (de)serialization against
hand-written handlers, covering unary and streaming, with no wire framing
to fake (unary and server-streaming are its documented purpose; the bidi
`Converse` case is expected to work in-memory but is proven at
implementation time, and the e2e suite exercises `Converse` over real wire
regardless). #761 and #821's own M3 wording ("UDS Connect-ES session
provider") already presumed it.

Repo facts at decision time: `buf.gen.yaml` is buf v2 managed mode with only
the two Go plugins writing to `contracts/gen/go`; there is no TypeScript
toolchain anywhere in CI; the root Taskfile composes sub-projects via
`includes:` (the `site:` include for `website/` is the precedent).

## Decision

**1. Connect-ES v2 is the gRPC client library.** The `./node` subpath uses
`@connectrpc/connect-node`'s gRPC transport (real gRPC over HTTP/2) for
Node and Bun, over TCP or a Unix domain socket — UDS is not a first-class
`baseUrl` scheme and is wired through the transport's node options (the
`http2.connect` socket path), a small amount of deliberate plumbing, not a
config one-liner. The browser does **not** use Connect-ES: mecated does
not speak the Connect protocol, so the `.` export's browser transport is a
hand-written fetch + SSE client against mecated's existing HTTP API, behind
the same transport-neutral raw-operation seam.

**2. protobuf-es v2 generates `./gen`, via a second buf template.** TS
generation is scoped to `mecatl.v1` only (the driver protocol in
`mecatl/driver/v1` is a Go-process contract, not SDK surface), writing
committed output under the SDK tree, exported as `./gen`. Buf's v2 config
has no per-plugin path scoping (`inputs`/`paths` filtering is
template-wide), so the pinned `protoc-gen-es` remote plugin lives in a
**second template** (a TS-only sibling of `buf.gen.yaml` with its own
`inputs` scoped to `mecatl/v1`) rather than in the Go template; `task
generate` runs both `buf generate` invocations, so the operator surface is
still one task. The TS template carries its own deliberate managed-mode
block — managed-mode option rewrites are stamped into the serialized
descriptors embedded in generated `*_pb` files, so the template that
produces the committed output must be the only one that ever produces it. A
freshness gate (regenerate, then fail on diff) keeps proto and generated TS
from drifting, the same generate→commit→diff discipline the repo already
applies to committed generated artifacts.

**3. The Transport is an injection seam, and `createRouterTransport` is the
mocking story.** SDK clients accept an injected Transport at construction.
That seam — not a byte-level wire fake — is how downstream apps unit-test
against `@stacklok/mecatl`: `createRouterTransport` runs their fixtures
through real protobuf (de)serialization for unary and streaming RPCs alike.

**The mocking consequence.** This supersedes ADR 0253's Decision 1 (vendor
the private prototype's unary automocker) and with it the Decision 2
SPDX-relicense surface, which existed only to carry that vendor drop: the
primitive Connect-ES ships in the dependency tree covers strictly more than
the vendored mocker would have (streaming included — 0253's own stated
residual), with zero vendored code to relicense or drift. 0253's honesty
about residuals survives, inverted: **streaming mocking is now covered; the
un-mocked residual is the browser HTTP/SSE path**, which is a different wire
shape (hand-written JSON + SSE, not Connect-RPC) and remains, per 0253's own
framing, a separately-buildable REST-shaped concern (MSW-style) for whoever
needs it — most plausibly Studio, whose client architecture is still
undecided. ADR 0253's Decision 3 (name the residual) and Decision 4 (no
premature shared package) carry over unchanged in spirit.

**4. Project tooling.** Exactly-pinned pnpm 11 via the `packageManager`
field (CI installs pnpm explicitly at the pinned version rather than
relying on corepack); TypeScript 6 for development with declarations
emitted compatible with TS 5.7+; **biome** for lint + format (one tool, no
eslint/prettier split; the generated `./gen` tree is excluded from
formatting so the freshness gate and the format gate never fight); **vitest**
as the test runner; plain `tsc` for the ESM-only unbundled build
(declarations + source maps); **API Extractor** for the public-surface
reports — one report per entry point, for `.` and `./node`; `./gen` is
deliberately excluded (its surface is machine-generated, churns with every
proto change, and is already governed by the codegen freshness gate plus
buf's breaking-change checks, matching #821's own M4 wording); Apache-2.0
package metadata. The tree wires into the root Taskfile as an `includes:`
sub-Taskfile (the `site:` precedent) and into CI as one hybrid Node+Go job
(it builds `mecated` for the e2e and runs buf for the freshness step):
install (frozen lockfile) → biome → typecheck → vitest → build + API
reports + pack → codegen freshness → offline e2e against a same-checkout
`mecated --mock`.

**5. No new Go-side resource-inventory rows for this milestone.** The M1 SDK
work introduces no new long-lived resource on the Go side —
[ADR 0027](./0027-cloud-native.md)'s List 1 rows 65–66 (added with the
daemon-hosting enablers) already cover the spawned-daemon lifetime pipe and
ready-file surface. The SDK-side long-lived objects (status-monitor
heartbeat, reconnect loops, the M3 tool host) live in the client process,
outside ADR 0027's scope; the milestone that lands each one owns its
client-side lifecycle contract (M2 attachment, M3 spawn/tools).

## Consequences

- **One type system end to end.** protobuf-es types are what Connect-ES
  consumes and what `./gen` exports; there is no second codegen dialect to
  reconcile, and a new public RPC is reachable from generated descriptors
  without hand-written plumbing.
- **Streaming is first-class from day one** — `Converse`,
  `WatchSessionEvents`, and `RunTeam` map onto async iteration, which is the
  shape the SDK's `Run`/attachment contracts already promise.
- **The testkit shrinks.** No vendor drop, no SPDX relicense, no drift
  against the private prototype. The cost: byte-level wire-framing fidelity
  is not exercised by router-transport tests — accepted, because the framing
  layer is Connect-ES's own tested code, and the SDK's e2e suite runs
  against a real `mecated --mock` daemon over real wire anyway.
- **A major-version commitment.** The SDK couples to Connect-ES v2 and
  protobuf-es v2 majors; their upgrade cadence becomes SDK maintenance load.
  Pinned plugin + runtime versions and the freshness gate keep the generated
  and runtime halves in lockstep.
- **`task generate` now emits TypeScript too.** Any proto-touching branch
  that merges after a codegen change (or vice versa) must re-run `task
  generate`; the freshness gate makes forgetting impossible rather than
  cheap.
- **The browser transport is hand-maintained.** The HTTP/SSE client tracks
  mecated's bespoke HTTP surface by hand; transport parity is enforced by
  tests, not by codegen. This is the standing cost of not introducing a new
  application protocol for browsers, which #761 already accepted.
- **Steer over HTTP is not yet reachable.** [ADR 0252](./0252-http-steer-endpoint.md)
  is Proposed and its route ([#873](https://github.com/stacklok/mecatl/issues/873))
  has not landed; until it does, the browser transport surfaces steer as a
  typed unsupported-feature error rather than silently degrading.

## See also

- [ADR 0253](./0253-sdk-mocking-testkit.md) — the superseded-in-part mocking
  decision; its residual framing survives here inverted.
- [ADR 0248](./0248-sdk-compatibility-and-error-contract.md),
  [ADR 0249](./0249-durable-run-identity.md),
  [ADR 0250](./0250-durable-cursors-and-watch.md),
  [ADR 0252](./0252-http-steer-endpoint.md) — the Go-side contracts the SDK
  consumes.
- [ADR 0093](./0093-provider-modules.md) — the release-tag discipline the
  eventual `sdk/typescript/vX.Y.Z` tags mirror (root `v*` image triggers
  never fire on them).
- `docs/acceptance/sdk-typescript-core.md` — the acceptance plan this ADR
  anchors (M1).
- [Issue #821](https://github.com/stacklok/mecatl/issues/821) — the settled
  design contract this ADR records; [#872](https://github.com/stacklok/mecatl/issues/872)
  — the testkit issue whose scope Decision 3 narrows.
