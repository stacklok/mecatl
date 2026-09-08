# TypeScript SDK core (M1) — acceptance plan

**Phase:** capability — `@stacklok/mecatl-sdk` M1: foundation, codegen, and core runs
**Status:** landed, 2026-09-02. Synthesised from the settled #821 design contract plus the transport decision recorded in ADR 0279.
**Issue:** [stacklok/mecatl#821](https://github.com/stacklok/mecatl/issues/821) (parent: [#761](https://github.com/stacklok/mecatl/issues/761)).
**ADR:** [ADR-0279](../adr/0279-typescript-sdk-architecture.md) — Connect-ES v2 + protobuf-es v2, the injected-Transport seam, the in-repo pnpm/biome/vitest toolchain, and the in-part supersession of [ADR-0253](../adr/0253-sdk-mocking-testkit.md).
**Accumulator / stack:** `sdk/10-architecture-adr` was the stack trunk (PR #908). Subsequent layers were `sdk/11`…`sdk/19` via `gh stack` (linear, one PR per scenario — the plan's 4–8 parallelism was serialised because a stack cannot fork).
**Landed as:** ten squash-merges to `main` — [#908](https://github.com/stacklok/mecatl/pull/908) (ADR 0279, 2026-09-01), then [#924](https://github.com/stacklok/mecatl/pull/924) (Scenario 1), [#925](https://github.com/stacklok/mecatl/pull/925) (2), [#927](https://github.com/stacklok/mecatl/pull/927) (3), [#929](https://github.com/stacklok/mecatl/pull/929) (4), [#930](https://github.com/stacklok/mecatl/pull/930) (5), [#931](https://github.com/stacklok/mecatl/pull/931) (6), [#932](https://github.com/stacklok/mecatl/pull/932) (7), [#933](https://github.com/stacklok/mecatl/pull/933) (8), and [#934](https://github.com/stacklok/mecatl/pull/934) (9) on 2026-09-02. [#821](https://github.com/stacklok/mecatl/issues/821) stays open for M2–M4.

The smallest set of work that makes `@stacklok/mecatl-sdk` real: a scaffolded,
CI-gated `sdk/typescript/` tree; committed protobuf-es generation for
`mecatl.v1`; both raw transports (Node/Bun gRPC via Connect-ES, browser
HTTP/SSE) behind one transport-neutral seam; typed errors and the
compatibility floor; `Client`/`Session`/`Run` with run choreography,
permissions, cancellation, and strict steer; multimodal prompt helpers; and
an offline e2e proving the whole path against a same-checkout
`mecated --mock`. This is #821's M1 — the server enablers it consumes
([sdk-server-enablers](sdk-server-enablers.md)) are already on `main`.

The doc is organized scenario-first because acceptance is about what the
running harness (here: the SDK against the running harness) can demonstrate,
not which packages exist on disk.

## Why these scope cuts

- **All of M1, nothing past it.** M1 item 3 (the Go server contracts) landed
  via [sdk-server-enablers](sdk-server-enablers.md); the only unlanded piece,
  listener-scoped `mcp_servers` (its Scenario 9, PR
  [#903](https://github.com/stacklok/mecatl/pull/903)), is an M3 dependency
  (`tool()`), not an M1 one. Nothing in this plan touches
  `contracts/proto/mecatl/v1/harness.proto` or `internal/adapter/server/`,
  so that branch merges independently.
- **Connect-ES v2, decided — not re-litigated per task.**
  [ADR-0279](../adr/0279-typescript-sdk-architecture.md) settles the
  transport library, the codegen stack, the npm name
  (`@stacklok/mecatl-sdk`), and the mocking cut: no published mocker in M1;
  an injected-Transport seam plus a minimal in-process fake for the SDK's
  own tests. The vendored automocker of
  [ADR-0253](../adr/0253-sdk-mocking-testkit.md) is not shipped.
- **The browser transport is exercised from Node in M1.** The HTTP/SSE
  client is isomorphic code; M1 proves wire correctness against a real
  `mecated` HTTP listener from vitest in Node, and browser-only behaviours
  (page-hidden, credentials mode) via stubbed browser APIs. Real-browser
  runtimes (Playwright, real CORS/visibility behaviour) are #821 M4's
  matrix — pulling that CI plumbing into M1 buys little signal for its cost.
- **Steer over HTTP degrades to a typed error.** The HTTP steer route
  ([ADR-0252](../adr/0252-http-steer-endpoint.md), issue
  [#873](https://github.com/stacklok/mecatl/issues/873)) has not landed and
  this plan does not touch the server; the browser transport surfaces
  steer as a typed unsupported-feature error rather than silently dropping
  it. gRPC steer is fully supported.
- **Codegen lands now; the #903 overlap is one command.** Adding the
  protobuf-es plugin means the next `task generate` also emits TS. Output
  directories are disjoint from the Go output, so there is no textual
  conflict with PR #903 — whichever merges second re-runs `task generate`,
  and the freshness gate makes forgetting impossible. The PR body states
  which order actually happened.
- **No parallel-stack inheritance.** The sibling enabler plan shipped as a
  linear ten-PR stack because six PRs churned the same committed generated
  Go. `sdk/typescript/` is a new tree with no such contention; scenarios
  parallelize per the sequencing note instead.
- **Verify names are scenario-numbered, not ADR-numbered** — the enabler
  plan's pins were authored as `TestADR_0244_*`/`TestADR_0245_*` and a
  later ADR renumber left them pointing at a missing ADR (0244) and an
  unrelated one (0245 — safe build diagnostics). The M1 close-out
  repointed them at their real ADRs, `TestADR_0248_*` and
  `TestADR_0249_*`; this plan avoids the failure mode entirely by using
  `TestSDKTypescriptCore_ScenarioN_*` for Go proofs and
  `path :: "title"` vitest references for TypeScript proofs throughout.

## Out of scope

| Item | Defer-to | ADR / decision |
|---|---|---|
| `session.attach()` / `session.activity()` / cursor checkpointing / `WatchSessionEvents` ergonomics + envelope union | M2 plan | [ADR-0250](../adr/0250-durable-cursors-and-watch.md) shipped the server half; the client half is M2 |
| `spawn()`, `query()`, `tool()`, the local MCP host | M3 plan | needs PR [#903](https://github.com/stacklok/mecatl/pull/903) (`mcp_servers` on create) |
| Full HarnessService/ScheduleService RPC coverage, descriptor-to-transport parity gate | M4 plan | [#821](https://github.com/stacklok/mecatl/issues/821) M4 item 1 |
| Browser/Bun CI matrices, TS 5.7 declaration matrix, Playwright | M4 plan | #821 M4 item 2 |
| npm publish workflow, trusted publishing, `sdk/typescript/vX.Y.Z` tags | M4 plan | #821 M4 item 4; tag discipline per [ADR-0093](../adr/0093-provider-modules.md) |
| `user-docs/` pages for the SDK | M4 plan | nothing is published before `v0.1.0`; M1 updates `docs/architecture.md` + IMPLEMENTATION-NOTES only |
| Steer over the HTTP transport | when [#873](https://github.com/stacklok/mecatl/issues/873) lands | [ADR-0252](../adr/0252-http-steer-endpoint.md) |
| Downstream-app / transport-agnostic e2e mocker (Next.js and similar) | #872 | [ADR-0279](../adr/0279-typescript-sdk-architecture.md) Decision 3: M1 is the injection seam only |

## In scope — 9 scenarios, in implementation order

Each is independently demoable; later scenarios assume earlier ones but do
not change their acceptance criteria. Within each, ACs progress happy path →
richer happy path → edges → cross-cutting. TypeScript proofs are cited as
`<vitest file> :: "<test title>"`; titles are the contract, exact wording may
be refined at implementation time as long as the AC linkage stays greppable
from the file.

---

### Scenario 1 — The `sdk/typescript/` tree: toolchain, package shape, CI

The project exists and is gated. Exactly-pinned pnpm 11 (the `packageManager`
field), TypeScript 6, biome (lint + format), vitest, plain `tsc` ESM build
with declarations and source maps, API Extractor, Apache-2.0 metadata, and
subpath exports `.` / `./node` / `./gen` — per
[ADR-0279](../adr/0279-typescript-sdk-architecture.md) Decision 4. The tree
wires into the root Taskfile via the existing `includes:` pattern (the
`site:` include for `website/` is the precedent; `website/` itself stays on
npm untouched) and into `ci.yml` as one job.

**Work:**
- `sdk/typescript/`: package.json (exports map, `packageManager`, Apache-2.0),
  tsconfig (TS 6, ESM, declarations + source maps), biome config (with the
  generated `src/gen/` tree excluded from formatting, so the codegen
  freshness gate and the format gate never fight), vitest config, API
  Extractor configs + committed reports — one per entry point, for `.` and
  `./node`; `./gen` deliberately excluded per
  [ADR-0279](../adr/0279-typescript-sdk-architecture.md) Decision 4 — a
  local `.gitignore` (node_modules, dist, API Extractor temp), LICENSE,
  README.
- Root: the `sdk:` Taskfile include (lint / typecheck / test / build / pack);
  the `sdk` CI job in `.github/workflows/ci.yml` — a hybrid Node+Go job: it
  installs pnpm explicitly at the pinned version (never assumes corepack),
  caches on the SDK lockfile, and also sets up Go + buf because the
  freshness step and the Scenario 9 e2e build `mecated` from the same
  checkout; `.actrace.yml` with a resolver for this plan's vitest verify
  lines.

**Acceptance:**
- AC1.1: A clean checkout with only pnpm 11 and Node 24 runs install
  (frozen lockfile), lint, typecheck, test, build, and pack through the root
  `task sdk:*` targets; no root-level package.json appears and `website/`
  is untouched.
  - verify: inspection — the CI job executes exactly these targets from a
    clean runner; `website/` diff is empty in the scaffold PR.
- AC1.2: The built package is ESM-only with declarations and source maps,
  and its exports map exposes exactly `.`, `./node`, and `./gen` — a CJS
  `require()` of the package fails, and no other subpath resolves.
  - verify: vitest:sdk/typescript/test/package.test.ts#ZXhwb3J0cyBtYXAgZXhwb3NlcyBleGFjdGx5IC4sIC4vbm9kZSwgLi9nZW4 — `sdk/typescript/test/package.test.ts :: "exports map exposes exactly ., ./node, ./gen"`
- AC1.3: `pnpm pack` produces a tarball containing the built output, license,
  and package metadata — and no test files, config, or generated-source
  duplicates outside the intended layout; the license field and file are
  Apache-2.0.
  - verify: vitest:sdk/typescript/test/package.test.ts#cGFja2VkIHRhcmJhbGwgY2FycmllcyBkaXN0IGFuZCBsaWNlbnNlIG9ubHk — `sdk/typescript/test/package.test.ts :: "packed tarball carries dist and license only"`
- AC1.4: The committed API Extractor reports — one for `.`, one for `./node`
  (API Extractor is single-entry-point, so one report per subpath; `./gen`
  is deliberately not report-governed, its surface being machine-generated
  and already gated by codegen freshness) — match the built public surface;
  an unreviewed public-API change fails the CI job with a report diff.
  - verify: demonstration — the CI job runs API Extractor in verify mode
    against the committed reports.
- AC1.5: A `.actrace.yml` resolver makes this plan's vitest `verify:` lines
  actually resolve (a deleted test title fails `task ac-trace-strict` once
  the plan is landed); if the resolver mechanism cannot express it, the
  limitation is recorded in this plan's Deferred decisions instead — never
  silently vacuous.
  - verify: demonstration — `task ac-trace` output shows vitest proofs
    resolving (or the recorded limitation).
- AC1.6: `task lint && task test` at the repo root remain green and
  byte-identical in behaviour for the Go modules — the SDK tree adds gates,
  it does not perturb existing ones.
  - verify: inspection — the scaffold PR touches no Go source; the full
    suite passes.

---

### Scenario 2 — protobuf-es generation for `mecatl.v1` and the freshness gate

`task generate` emits committed TypeScript alongside the existing Go, from
the same proto source of truth, per
[ADR-0279](../adr/0279-typescript-sdk-architecture.md) Decision 2. The
generated tree is the `./gen` export. `contracts/gen/` stays
generated-never-hand-edited, per the standing rule in
[`AGENTS.md`](../../AGENTS.md); the same discipline extends to the TS output.

**Work:**
- A **second buf template** (a TS-only sibling of `buf.gen.yaml`, e.g.
  `buf.gen.ts.yaml`) carrying the pinned `protoc-gen-es` remote plugin with
  its own `inputs`/`paths` scoped to `mecatl/v1`, writing under
  `sdk/typescript/src/gen/` — buf's v2 config has no per-plugin path
  scoping, and the Go template must keep generating `mecatl/driver/v1`, so
  one shared template cannot serve both ([ADR-0279](../adr/0279-typescript-sdk-architecture.md)
  Decision 2). The TS template carries its own deliberate managed-mode
  block (managed-mode rewrites are stamped into the serialized descriptors
  embedded in generated output, so only this template may ever produce the
  committed tree).
- Taskfile `generate`: runs both `buf generate` invocations — the operator
  surface stays one task.
- CI: the freshness step — regenerate, then `git diff --exit-code` over both
  the Go and TS generated trees.

**Acceptance:**
- AC2.1: `task generate` on a clean checkout reproduces the committed TS
  generated output byte-identically; a hand-edit or a stale commit fails the
  CI freshness step.
  - verify: demonstration — the CI freshness step (regenerate → `git diff
    --exit-code`).
- AC2.2: Generation is scoped to `mecatl.v1` — no TypeScript is emitted for
  `mecatl/driver/v1` or any other proto package.
  - verify: `sdk/typescript/test/gen.test.ts :: "no driver protocol output is generated"`
- AC2.3: `./gen` exports message types and service descriptors for
  `HarnessService` and `ScheduleService` that a Connect-ES client consumes
  directly — a generated descriptor constructs a typed client without
  hand-written glue.
  - verify: `sdk/typescript/test/gen.test.ts :: "generated descriptors construct a typed Connect client"`
- AC2.4: The committed Go generated output (`contracts/gen/go`) is
  byte-unchanged by the plugin addition — adding TS generation does not
  churn the Go tree.
  - verify: inspection — the codegen PR's diff under `contracts/gen/` is
    empty.

---

### Scenario 3 — Raw transports, typed errors, and the compatibility floor

One transport-neutral raw-operation seam with two implementations: the
Connect-ES gRPC transport (Node/Bun, TCP and UDS) and the hand-written
fetch + SSE transport over mecated's existing HTTP API. Clients accept an
injected Transport — the M1 test seam ([ADR-0279](../adr/0279-typescript-sdk-architecture.md)
Decision 3): a minimal in-process fake (handlers or fixture files), not a
shipped downstream mocker. Errors normalize to one typed hierarchy carrying the stable
mecatl code from RFC 9457 problem bodies and gRPC status details alike, and
the first call enforces the compatibility floor — both per
[ADR-0248](../adr/0248-sdk-compatibility-and-error-contract.md).

**Work:**
- `sdk/typescript/src/`: the raw operation interfaces; the Connect-ES
  transport (`./node`); the HTTP/SSE transport (`.`); the typed error
  hierarchy (transport, authentication, protocol, unsupported-feature,
  invalid-state, server-domain) preserving cause/status/code/request-id;
  credentials (static headers + async per-request provider, browser
  credentials mode, pluggable fetch); the `GetCompatibilityInfo` floor check.

**Acceptance:**
- AC3.1: The same raw operation invoked over the gRPC transport and the HTTP
  transport returns the same normalized result for the same server state —
  transport parity at the raw seam. Proven with twin in-process fakes (a
  `createRouterTransport` gRPC fake and a fetch-level HTTP fake) driven from
  one shared scripted-state fixture; the live-wire proof is Scenario 9's
  AC9.1/AC9.2, not this test.
  - verify: `sdk/typescript/test/transport-parity.test.ts :: "raw operations agree across gRPC and HTTP"`
- AC3.2: A client constructed over an injected `createRouterTransport`
  exercises unary and server-streaming operations with no network and no
  daemon — the ADR-0279 M1 test seam works as documented.
  - verify: `sdk/typescript/test/router-transport.test.ts :: "router transport drives unary and streaming operations offline"`
- AC3.3: A server whose `GetCompatibilityInfo` is absent or reports an
  unsupported API major yields `IncompatibleServerError`; no probe session
  is created and no legacy mode is inferred.
  - verify: `sdk/typescript/test/compatibility.test.ts :: "missing or incompatible server info fails the floor"`
- AC3.4: An HTTP `application/problem+json` failure and the same
  domain failure over gRPC status details normalize to the same typed SDK
  error with the same stable mecatl code; cause and transport metadata are
  preserved on the error object.
  - verify: `sdk/typescript/test/errors.test.ts :: "problem+json and gRPC status details normalize to one typed error"`
- AC3.5: Credentials flow — static headers and an async per-request
  provider are attached on both transports, and the browser credentials
  mode option is passed through to every HTTP request (observable via the
  pluggable fetch); a provider rejection surfaces as a typed authentication
  error; no credential value appears in SDK diagnostics, error messages, or
  serialized state.
  - verify: `sdk/typescript/test/credentials.test.ts :: "credential providers attach headers and never leak"`
- AC3.6: A caller-supplied `fetch` implementation is used by the HTTP
  transport for every request — the SDK never reaches for a global it was
  told not to use.
  - verify: `sdk/typescript/test/credentials.test.ts :: "pluggable fetch owns every HTTP request"`
- AC3.7: Steer over the HTTP transport is gated on the `http_steer` feature
  string from `GetCompatibilityInfo` — the mechanism
  [ADR-0252](../adr/0252-http-steer-endpoint.md) mandates (features, never
  "404 until you try it") — and fails with a typed unsupported-feature
  error when the feature is absent; steer over gRPC succeeds. Because the
  gate reads the feature set, the error clears without SDK changes once
  [#873](https://github.com/stacklok/mecatl/issues/873) lands server-side.
  - verify: vitest:sdk/typescript/test/steer.test.ts#SFRUUCBzdGVlciBpcyBhIHR5cGVkIHVuc3VwcG9ydGVkLWZlYXR1cmUgZXJyb3I — `sdk/typescript/test/steer.test.ts :: "HTTP steer is a typed unsupported-feature error"`
- AC3.8: Unknown fields and unknown enum-like string values arriving from a
  newer server pass through the raw seam undamaged — the SDK never strips
  what it does not understand.
  - verify: `sdk/typescript/test/transport-parity.test.ts :: "unknown fields survive the raw seam"`
- AC3.9: A Go↔TS error-code parity gate walks the Go error-code registry
  against the TS typed-error vocabulary, so a new server code fails CI
  until it is typed — the gate
  [ADR-0248](../adr/0248-sdk-compatibility-and-error-contract.md)
  Decision 8 mandates, mirroring the event kind-parity gate of AC6.3 (same
  manifest mechanism, same root-module placement).
  - verify: `TestSDKTypescriptCore_Scenario3_ErrorCodeParity`

---

### Scenario 4 — `Client`, `Session` lifecycle, and connection status

`connect()` returns a `Client`; `client.sessions.create/get/fork` return
`Session`s; `session.close()` releases runtime resources while
`session.delete()` removes durable state — the #821 contract recorded in
[ADR-0279](../adr/0279-typescript-sdk-architecture.md). Connection status is
a multicast with `getSnapshot()`/`subscribe()` over the closed vocabulary
(connecting, online, reconnecting, offline, unauthorized, incompatible),
heartbeating `GetCompatibilityInfo` only while subscribed, per
[ADR-0248](../adr/0248-sdk-compatibility-and-error-contract.md)'s discovery
contract.

**Acceptance:**
- AC4.1: `connect()` yields a `Client` whose `sessions.create()`, `get()`,
  and `fork()` return `Session` handles mapped to `CreateSession`,
  `GetSession`, and `ForkSession`; `close()` releases only local resources
  (the session remains loadable) while `delete()` removes durable state (a
  subsequent `get()` fails with a typed not-found).
  - verify: `sdk/typescript/test/session-lifecycle.test.ts :: "create, get, fork, close, and delete map to their RPCs"`
- AC4.2: Status transitions are observable: a fresh client reports
  `connecting` then `online`; a dropped connection reports `reconnecting`
  then `offline`; an auth failure reports `unauthorized`; a floor failure
  reports `incompatible`. `getSnapshot()` agrees with the latest
  `subscribe()` emission.
  - verify: `sdk/typescript/test/status.test.ts :: "status walks the closed vocabulary"`
- AC4.3: The heartbeat runs only while at least one status subscriber
  exists, stops when the last unsubscribes, and ordinary request outcomes
  update status immediately without waiting for a heartbeat tick.
  - verify: `sdk/typescript/test/status.test.ts :: "heartbeat is subscriber-gated"`
- AC4.4: In a browser-like environment a hidden page pauses the heartbeat
  and visibility restore resumes it; in Node the same code path is a no-op.
  M1 proves this with a stubbed visibility API in vitest (the requirement is
  [#821](https://github.com/stacklok/mecatl/issues/821)'s settled contract);
  the real-browser proof is M4's matrix.
  - verify: `sdk/typescript/test/status.test.ts :: "hidden pages pause the heartbeat"`
- AC4.5: Closing the `Client` (including via `Symbol.asyncDispose`) detaches
  subscribers, stops the heartbeat, and closes owned transports; a
  subsequent operation fails with a typed invalid-state error.
  - verify: `sdk/typescript/test/session-lifecycle.test.ts :: "client disposal releases resources and fails fast after"`

---

### Scenario 5 — Run choreography: `run()`, terminal outcomes, stale controls, strict steer

The heart of M1. `await session.run(prompt)` resolves only after server
acceptance and the first run-ID-bearing event; a `Run` has exactly one
consumption mode; server terminal outcomes close normally with a typed
outcome while transport failures throw. Every control the SDK sends carries
`expected_run_id`, so a stale approve/cancel/steer fails without touching a
newer run — the [ADR-0249](../adr/0249-durable-run-identity.md) contract,
consumed from the client side. `run.steer()` is strict and never promotes
([ADR-0252](../adr/0252-http-steer-endpoint.md) records the same strictness
for the HTTP route to come).

**Acceptance:**
- AC5.1: `session.run(prompt)` resolves only after the server accepted the
  run and the first run-ID-bearing event arrived; the returned `Run` exposes
  that non-empty run ID.
  - verify: vitest:sdk/typescript/test/run.test.ts#cnVuIHJlc29sdmVzIG9uIGFjY2VwdGFuY2Ugd2l0aCB0aGUgZmlyc3QgcnVuIElE — `sdk/typescript/test/run.test.ts :: "run resolves on acceptance with the first run ID"`
- AC5.2: A `Run` is consumed exactly once: iterating it yields the event
  stream; `run.result()` drains and returns `RunResult`; doing both, or
  either twice, fails with a typed invalid-state error.
  - verify: vitest:sdk/typescript/test/run.test.ts#YSBydW4gaGFzIGV4YWN0bHkgb25lIGNvbnN1bXB0aW9uIG1vZGU — `sdk/typescript/test/run.test.ts :: "a run has exactly one consumption mode"`
- AC5.3: `RunResult` carries stop reason, final text/content, usage, session
  ID, run ID, and the raw terminal event; server terminals — `StopError`,
  cancellation, limits, budget — resolve normally with the typed outcome,
  while a transport/protocol failure rejects.
  - verify: vitest:sdk/typescript/test/run.test.ts#c2VydmVyIHRlcm1pbmFscyByZXNvbHZlIHR5cGVkLCB0cmFuc3BvcnQgZmFpbHVyZXMgdGhyb3c — `sdk/typescript/test/run.test.ts :: "server terminals resolve typed, transport failures throw"`
- AC5.4: A second local `run()` on a busy `Session` rejects with
  `SessionBusyError` without sending a prompt; the daemon stays
  authoritative for cross-client contention.
  - verify: vitest:sdk/typescript/test/run.test.ts#YSBzZWNvbmQgbG9jYWwgcnVuIGlzIFNlc3Npb25CdXN5RXJyb3I — `sdk/typescript/test/run.test.ts :: "a second local run is SessionBusyError"`
- AC5.5: Every approve/cancel/steer sent through the ergonomic
  `Run`/`Session` surface carries the active run's `expected_run_id`; a
  control racing a terminal (the run it named is gone) surfaces the
  server's typed stale-control failure and the newer run is observably
  untouched. The raw operation seam leaves `expected_run_id`
  caller-controlled — omitting it is how a raw caller opts into the
  server's legacy behaviour, including steer promotion.
  - verify: vitest:sdk/typescript/test/controls.test.ts#ZXJnb25vbWljIGNvbnRyb2xzIGFsd2F5cyBjYXJyeSBleHBlY3RlZF9ydW5faWQgYW5kIHN0YWxlIGNvbnRyb2xzIGZhaWwgdHlwZWQ — `sdk/typescript/test/controls.test.ts :: "ergonomic controls always carry expected_run_id and stale controls fail typed"`
- AC5.6: `run.steer()` is strict — because it always names its run
  (AC5.5), a steer that loses the terminal race is refused, never promoted
  into a new run; the raw seam retains the documented promotion behaviour
  for callers that deliberately omit `expected_run_id`.
  - verify: vitest:sdk/typescript/test/steer.test.ts#cnVuLnN0ZWVyIG5ldmVyIHByb21vdGVzOyB0aGUgcmF3IHNlYW0gbWF5 — `sdk/typescript/test/steer.test.ts :: "run.steer never promotes; the raw seam may"`
- AC5.7: `run.cancel()` resolves the run with the cancelled terminal outcome
  through the normal consumption path — cancellation is an outcome, not an
  exception.
  - verify: vitest:sdk/typescript/test/run.test.ts#Y2FuY2VsIGlzIGEgdHlwZWQgb3V0Y29tZQ — `sdk/typescript/test/run.test.ts :: "cancel is a typed outcome"`

---

### Scenario 6 — The event union and Go↔TS kind parity

Hand-crafted discriminated unions cover agent and team events; known kinds
narrow by literal `kind`; unknown kinds become
`{ kind: "unknown", wireKind, ... }` preserving decoded data plus
transport-native raw data (raw JSON for HTTP, unknown protobuf bytes for
gRPC). Event kinds and stop reasons are open string vocabularies on the wire
— the [`AGENTS.md`](../../AGENTS.md) discipline (`EvNoProgress`/`StopBudget`
are string passthroughs, no proto enums) — so the union must fail loudly at
build time when Go grows a kind TS has not typed, not at runtime in a
downstream app.

**Mechanism (decided here so parallel workers don't invent two):** the TS
package derives its union from one exported, trivially-parseable const
manifest of known kinds (and, for AC3.9, of typed error codes); the Go
parity tests live in the **root module** (never under `engine/` — the
engine tree is self-contained and its module boundary rejects a cross-tree
read), read that manifest file from `sdk/typescript/`, and compare it
against the server-side vocabulary. Reading
`internal/adapter/server/` sources for the relay-skipped audit list is
fine — the no-touch constraint is about edits, not reads.

**Acceptance:**
- AC6.1: Every known agent and team event kind narrows by literal `kind` to
  a payload type whose fields match the proto payload for that kind.
  - verify: vitest:sdk/typescript/test/events.test.ts#a25vd24ga2luZHMgbmFycm93IGJ5IGxpdGVyYWwga2luZA — `sdk/typescript/test/events.test.ts :: "known kinds narrow by literal kind"`
- AC6.2: An event of an unknown kind decodes to
  `{ kind: "unknown", wireKind, ... }` carrying the decoded common fields
  plus the transport-native raw data — raw JSON over HTTP, unknown protobuf
  bytes over gRPC — and iteration continues.
  - verify: vitest:sdk/typescript/test/events.test.ts#dW5rbm93biBraW5kcyBwcmVzZXJ2ZSB0cmFuc3BvcnQtbmF0aXZlIHJhdyBkYXRh — `sdk/typescript/test/events.test.ts :: "unknown kinds preserve transport-native raw data"`
- AC6.3: A Go-side parity guard enumerates the wire event kinds the server
  can emit and fails when the TS union misses one or types one the server
  no longer emits — a new `session.Event` kind fails CI until it is typed.
  - verify: `TestSDKTypescriptCore_Scenario6_EventKindParity`
- AC6.4: Log-only event kinds (the relay-skipped set) are typed or
  explicitly excluded by the parity guard's audit list — never silently
  absent.
  - verify: `TestSDKTypescriptCore_Scenario6_LogOnlyKindsAudited`

---

### Scenario 7 — Permission asks and the responder contract

Raw permission-ask events are always emitted. `onPermissionAsk` is an
automatic responder: the first accepted verdict wins; callbacks run
concurrently keyed by ask ID with an `AbortSignal` and no invented timeout;
a thrown or abstaining callback leaves the ask pending for manual
resolution; duplicate or late verdicts return a typed already-resolved
error. The ask/verdict semantics mirror the server's permission discipline
(deny-dominant resolution, the ask taxonomy) recorded in
[`AGENTS.md`](../../AGENTS.md) and the askID grammar of
[ADR-0044](../adr/0044-host-supplied-askid-discriminator.md); the SDK adds
client choreography, never a second policy. **The manual-resolution surface
is `run.resolveAsk(askId, verdict)`** — asks are run-scoped, so the method
lives on `Run` (the attached-run analogue arrives with M2); every "manual
resolution" below means this method.

**Acceptance:**
- AC7.1: With an `onPermissionAsk` responder installed, the raw
  permission-ask event still reaches the event stream — the responder is an
  addition, never a filter.
  - verify: `sdk/typescript/test/permissions.test.ts :: "raw asks are always emitted"`
- AC7.2: Concurrent asks run their callbacks concurrently keyed by ask ID;
  for one ask, the first accepted verdict wins and later verdicts for the
  same ask return the typed already-resolved error.
  - verify: `sdk/typescript/test/permissions.test.ts :: "first accepted verdict wins per ask"`
- AC7.3: A callback that throws or abstains leaves the ask pending; a
  subsequent manual resolution succeeds.
  - verify: `sdk/typescript/test/permissions.test.ts :: "thrown or abstaining callbacks leave the ask pending"`
- AC7.4: The callback's `AbortSignal` fires on manual resolution and on run
  termination; the SDK imposes no timeout of its own.
  - verify: `sdk/typescript/test/permissions.test.ts :: "abort fires on manual resolution and run end"`
- AC7.5: A verdict for an ask that no longer exists (resolved, retracted, or
  from a prior run) fails with the typed already-resolved error and mutates
  nothing.
  - verify: `sdk/typescript/test/permissions.test.ts :: "late verdicts fail typed"`

---

### Scenario 8 — Multimodal prompt helpers

Isomorphic prompt parts accept text and image/audio from `Uint8Array` or
HTTPS URL; browser helpers accept `Blob`/`File`; Node helpers read paths.
The SDK validates source XOR, MIME type, size, and session capability before
sending — and the server remains authoritative, because capability truth is
the single composition-computed intersection echoed on the session
([`AGENTS.md`](../../AGENTS.md), "Capability truth is a SINGLE
composition-computed intersection"); the client check is a UX courtesy,
never the enforcement point.

**Acceptance:**
- AC8.1: Text, image, and audio parts construct from `Uint8Array` or HTTPS
  URL; supplying both or neither source fails locally with a typed
  validation error before any request is sent.
  - verify: `sdk/typescript/test/media.test.ts :: "part sources are XOR-validated locally"`
- AC8.2: The browser helpers accept `Blob`/`File` and the Node helpers read
  paths — and the path helpers live under `./node` only, so the isomorphic
  `.` export never imports Node filesystem modules.
  - verify: `sdk/typescript/test/media.test.ts :: "runtime helpers stay in their subpath"`
- AC8.3: A part whose MIME type or size violates the local bounds, or whose
  media kind the session's echoed capability rejects, fails locally with a
  typed error naming the reason; a server-side rejection of a
  client-accepted part still surfaces as the server's typed error — the
  server stays authoritative.
  - verify: `sdk/typescript/test/media.test.ts :: "local validation is a courtesy, the server is authoritative"`

---

### Scenario 9 — Offline e2e against `mecated --mock` and the CI gate

The whole M1 surface proven end to end, offline, against a same-checkout
daemon — the repo's standing test discipline
([`AGENTS.md`](../../AGENTS.md): tests are offline, mockllm-backed, never a
live model). One CI job runs the unit suites and this e2e for every PR.

**Work:**
- `cmd/mecated` + `internal/app`: extend the mock surface so a spawned
  binary can drive these ACs. Today `--mock` is a single canned **text**
  turn that can never emit a tool call (the scripted
  `Config.MockProvider` seam exists but has no CLI flag), so AC9.3/AC9.4
  are unreachable without this. Add an operator-selectable scripted mock
  (e.g. `--mock-script`) whose script includes an ask-worthy tool call and
  a multi-turn run long enough to cancel. This touches neither
  `contracts/proto/mecatl/v1/harness.proto` nor `internal/adapter/server/`
  — the no-touch constraint holds.
- `sdk/typescript/e2e/`: the suites below. Expect the UDS leg of AC9.1 to
  need deliberate connect-node plumbing (socket path via node options, not
  a `baseUrl` scheme), per [ADR-0279](../adr/0279-typescript-sdk-architecture.md)
  Decision 1.

**Acceptance:**
- AC9.1: A Node gRPC e2e connects to a same-checkout `mecated --mock` (TCP
  and UDS), passes the compatibility floor, creates a session, runs a
  prompt, iterates events to the typed terminal result, and deletes the
  session — no network beyond loopback.
  - verify: vitest:sdk/typescript/e2e/grpc.e2e.test.ts#ZnVsbCBydW4gbGlmZWN5Y2xlIG92ZXIgZ1JQQw — `sdk/typescript/e2e/grpc.e2e.test.ts :: "full run lifecycle over gRPC"`
- AC9.2: The same flow over the HTTP/SSE transport (steer excepted per
  AC3.7) yields the same normalized events and terminal outcome.
  - verify: vitest:sdk/typescript/e2e/http.e2e.test.ts#ZnVsbCBydW4gbGlmZWN5Y2xlIG92ZXIgSFRUUC9TU0U — `sdk/typescript/e2e/http.e2e.test.ts :: "full run lifecycle over HTTP/SSE"`
- AC9.3: A permission ask surfaces end to end: the mock run hits an
  ask-worthy tool, `onPermissionAsk` approves, and the run completes; a
  second e2e denies and the run observably continues past the denial.
  - verify: vitest:sdk/typescript/e2e/permissions.e2e.test.ts#YXNrcyByZXNvbHZlIHRocm91Z2ggdGhlIHJlc3BvbmRlciBlbmQgdG8gZW5k — `sdk/typescript/e2e/permissions.e2e.test.ts :: "asks resolve through the responder end to end"`
- AC9.4: Cancellation and stale controls hold on the real wire: cancelling
  an in-flight run resolves the cancelled outcome, and a control carrying a
  finished run's ID fails typed while the session's next run is untouched.
  - verify: vitest:sdk/typescript/e2e/controls.e2e.test.ts#Y2FuY2VsIGFuZCBzdGFsZSBjb250cm9scyBvbiB0aGUgcmVhbCB3aXJl — `sdk/typescript/e2e/controls.e2e.test.ts :: "cancel and stale controls on the real wire"`
- AC9.5: The CI job runs biome, typecheck, the vitest unit suites, the
  build + API report + pack, the codegen freshness step, and this e2e on
  every PR — a failure in any of them fails the PR.
  - verify: inspection — the `sdk` job in `.github/workflows/ci.yml`.

---

## Cross-cutting deliverables

- [ADR-0279](../adr/0279-typescript-sdk-architecture.md) and the in-part
  supersession pointer on [ADR-0253](../adr/0253-sdk-mocking-testkit.md) —
  authored with this plan (already on the accumulator, not a worker task).
- `docs/architecture.md`: a short SDK section (what the SDK is, the
  transport split, where the trees live); `docs/design/IMPLEMENTATION-NOTES.md`:
  the dense per-subsystem notes for `sdk/typescript/`.
- `task docs` configuration-reference regeneration with every Markdown change.
- No `engine/` API change is expected; if one sneaks in, `task api:check` /
  `task api:update` + `engine/CHANGELOG.md` per the standing rule.

## Sequencing recommendation

Scenario 1 → 2 are strictly ordered (the tree, then generation into it).
Scenario 3 needs 2 (generated descriptors). Scenarios 4–8 all build on 3 and
parallelize across workers, with one caution: they share
`sdk/typescript/src/` entry points (the exports barrel, the error
hierarchy), so orchestrate should land 3 first and treat the barrel as a
merge-conflict hotspot rather than serializing everything. Scenario 9 lands
last, consuming everything. The #903 regen coordination (Why these scope
cuts) binds whichever of the two branches merges second.

## Named tests landing in this plan

- `TestSDKTypescriptCore_Scenario3_ErrorCodeParity`
- `TestSDKTypescriptCore_Scenario6_EventKindParity`
- `TestSDKTypescriptCore_Scenario6_LogOnlyKindsAudited`

All three live in the root module (they read TS sources from
`sdk/typescript/`, so they can never live under `engine/`). All other
proofs are vitest suites under `sdk/typescript/`, cited per AC.

## Definition of done

1. `task lint` and `task test` pass (both Go modules, `-race`), plus the new
   `task sdk:*` gates.
2. `task docs` — configuration reference regenerated and the matlatl strict link gate green.
3. `task generate` reproduces both generated trees byte-identically (the CI
   freshness step is green).
4. `task ac-trace-strict` — every AC's `verify:` proof resolves (this plan is
   `landed`), including the vitest resolver of AC1.5 or its recorded
   limitation.
5. The named Go tests are green and grep-locatable by their identifiers.
6. `go run ./cmd/mecademo` still prints a full offline session.
7. The PR body states the #903 regen order that actually happened.

## Deferred decisions and known risks

- **The `.actrace.yml` Vitest resolver is expressible in ac-trace v0.0.3.**
  Scenario 1 wires a `vitest:` custom resolver whose token carries the test
  file plus the exact title encoded as base64url. The repository-owned resolver
  parses the TypeScript AST and requires exactly one matching Vitest case, so a
  deleted or renamed title fails instead of degrading to a file-level proof.
  That parse uses the TypeScript compiler out of `sdk/typescript/node_modules`,
  so the three `task ac-trace*` targets take `sdk:install` as a real dependency:
  without it every vitest proof is rejected as unresolvable — merely noisy while
  a plan is `draft`, and **fatal** under `--strict` once it lands. The install
  task is fingerprinted on `package.json` + `pnpm-lock.yaml`, so it is a no-op
  on an already-installed tree.
- **HTTP steer lands out from under AC3.7** — if
  [#873](https://github.com/stacklok/mecatl/issues/873) merges mid-plan, the
  worker may wire HTTP steer behind the same `http_steer` feature gate and
  test both sides of it; the AC's contract (feature-gated, no silent
  degradation) is unchanged either way.
- **API Extractor on TS 6 is unverified** — API Extractor lags compiler
  releases; if it rejects TS 6-emitted declarations at implementation time,
  Scenario 1 falls back to pinning the analyzed declaration output to a
  5.x-compatible target (or an exports-map checker for the affected entry
  point) and records the substitution here.
- **`createRouterTransport` bidi coverage is expected, not yet proven** —
  AC3.2 gates unary + server-streaming; the bidi `Converse` case is proven
  at implementation time, and the real-wire e2e (AC9.1) covers `Converse`
  regardless.
- **Connect-ES/protobuf-es major cadence** — pinned versions; upgrades are
  ordinary maintenance, out of this plan.
- **Bun runtime claims are untested in M1** — the code targets Bun 1.4+ per
  the #821 contract, but the CI proof is M4's matrix; until then Bun support
  is by construction, not by gate.

## Exit criteria

When every point under *Definition of done* holds on the accumulator, this
plan is satisfied.
