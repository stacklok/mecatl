# ADR 0279 — TypeScript SDK architecture: Connect-ES transport, protobuf-es codegen, in-repo pnpm project

- Status: Accepted
- Date: 2026-09-01
- Scope: `sdk/typescript/` (the `@stacklok/mecatl-sdk` package), `buf.gen.yaml`, the root Taskfile/CI wiring for the SDK tree.
- Supersedes: [ADR 0253](./0253-sdk-mocking-testkit.md) Decisions 1–2, in part — the vendored unary automocker and the SPDX-relicense surface it carried (see Decision 3). Also supersedes the npm package name `@stacklok/mecatl` as used in [#821](https://github.com/stacklok/mecatl/issues/821) and ADRs [0248](./0248-sdk-compatibility-and-error-contract.md)/[0252](./0252-http-steer-endpoint.md)/[0253](./0253-sdk-mocking-testkit.md). ADR 0253's Decisions 3–4 (name the residual; no premature shared package) stand.
- Superseded by: [ADR 0328](./0328-typescript-sdk-npmjs-stacklok-oss.md) (Decision 6 — published package name)
- Superseded by: [ADR 0339](./0339-typescript-sdk-deno.md), only for the supported-runtime set.

## Context

[#761](https://github.com/stacklok/mecatl/issues/761) and
[#821](https://github.com/stacklok/mecatl/issues/821) settled almost the whole
SDK design contract before any code: one Apache-2.0 in-repo package at
`sdk/typescript/` with an exactly-pinned
pnpm 11 and its own lockfile (the existing `website/` tree stays on npm);
the issues named it `@stacklok/mecatl` — this ADR renames it (Decision 6)
so the bare `mecatl` npm name stays free;
ESM-only unbundled transpiled modules with declarations and source maps;
subpath exports `.` (isomorphic core + the HTTP/SSE transport), `./node`
(gRPC transport, `spawn()`, `tool()`), `./gen` (generated protobuf types and
service descriptors); Node 22+, Bun 1.4+, the latest two stable
Chrome/Firefox/Safari; Windows is remote `connect()` only in v0.1; develop on
TypeScript 6 while emitting declarations compatible with TypeScript 5.7+.
This ADR records that contract rather than re-deciding it, except the npm
name (Decision 6) and the mocking cut (Decision 3).

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
  Transport. A later review constraint: downstream apps (Next.js and similar)
  need **transport-agnostic** end-to-end mocks, and a published mocker does
  not belong in the first prototype — only a minimal in-process cut, if any.

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

**3. M1 ships an injected-Transport seam, not a mocker product.** SDK
clients accept an injected Transport at construction. That is the only
mocking surface in this milestone: the SDK's own tests may route the
in-memory Connect fake (`createRouterTransport`) — and a sibling
fetch-level HTTP fake — at handler functions or fixture files. It is a
minimal cut so Scenario 3 can prove the raw seam offline. It is **not**
the downstream-app testkit (#872), and it is **not** transport-agnostic
end-to-end mocking (a Next.js app talking HTTP/SSE still cannot use a
gRPC router transport as its e2e double).

This still supersedes ADR 0253's Decision 1 (vendor the unary automocker)
and Decision 2 (its SPDX relicense): we do not vendor that prototype.
0253's Decision 3 (name the residual) and Decision 4 (no premature shared
package) stand. The residual is now stated honestly: **a published,
transport-agnostic mocker is deferred**; the HTTP/SSE path is un-mocked
as a product, and Connect's in-memory fake covers only the gRPC half of
the SDK's own unit tests. Issue #872 tracks the later product.

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

**6. The published npm name is `@stacklok/mecatl-sdk`.** [#821](https://github.com/stacklok/mecatl/issues/821)
used `@stacklok/mecatl`. The bare name stays available for other npm
artifacts (for example a future `mecatui` package). The in-repo path
remains `sdk/typescript/`.

## Consequences

- **One type system end to end.** protobuf-es types are what Connect-ES
  consumes and what `./gen` exports; there is no second codegen dialect to
  reconcile, and a new public RPC is reachable from generated descriptors
  without hand-written plumbing.
- **Streaming is first-class from day one** — `Converse`,
  `WatchSessionEvents`, and `RunTeam` map onto async iteration, which is the
  shape the SDK's `Run`/attachment contracts already promise.
- **No mocker product in M1.** No vendor drop, no SPDX relicense. Downstream
  apps do not get a documented e2e testkit yet; they inject a Transport or
  hit a real daemon. The cost: Next.js-style transport-agnostic e2e doubles
  wait on #872. Router-transport tests also do not prove byte-level framing
  — accepted, because that layer is Connect-ES's, and Scenario 9 hits a real
  `mecated --mock` over real wire.
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
  design contract this ADR records (npm name excepted; Decision 6);
  [#872](https://github.com/stacklok/mecatl/issues/872) — the deferred
  transport-agnostic mocker.
