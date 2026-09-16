# TypeScript SDK public surface and v0.0.1 GitHub Packages release (M4) — acceptance plan

**Phase:** capability — `@stacklok/mecatl-sdk` M4: complete public surface, platform contract, documentation, and first GitHub Packages release
**Contract:** human-reviewed/v1
**Status:** proposed
**Delivery:** Split
**Issue:** [stacklok/mecatl#821](https://github.com/stacklok/mecatl/issues/821) (parent: [#761](https://github.com/stacklok/mecatl/issues/761)).
**ADR:** [ADR-0304](../adr/0304-typescript-sdk-public-surface-and-release.md) and [ADR-0313](../adr/0313-interim-github-packages-typescript-sdk.md) — descriptor-to-transport parity, the one thin-namespace rule, ergonomic teams, streaming `PlanResolution`, the compatibility and browser matrices, executable examples, path-qualified GitHub Packages publication, and `v0.0.1`'s human gate. This plan exits on the verified `v0.0.1` GitHub Packages release; npmjs `v0.1.0` remains a follow-up checklist under #821.
**Delivery shape:** a **linear stack**, one PR per scenario — `sdk/41-rpc-catalog` is the stack root off `main`; subsequent layers are `sdk/42-http-routes`, `sdk/43-namespaces-core`, `sdk/44-namespaces-ops`, `sdk/45-teams`, `sdk/46-plan-resolution`, `sdk/47-compat-matrix`, `sdk/48-browser-chromium`, `sdk/49-browser-platforms`, `sdk/50-docs-examples`, `sdk/51-release-workflow`, and `sdk/52-publish-v0.0.1`. This is **not** an accumulator: each PR targets its predecessor and is reviewed and merged on its own, exactly as M3's `sdk/31`…`sdk/40` stack was.

The smallest complete stack that makes every public HarnessService and
ScheduleService operation reachable, adds ergonomics only where a lifecycle
requires them, proves the supported runtime and browser claims, documents the
package, and publishes the reviewed artifact as `v0.0.1` to GitHub Packages.
It is the final implementation milestone of [#821](https://github.com/stacklok/mecatl/issues/821), not a new
application protocol or an opportunity to redesign the Go server.

The doc is organized scenario-first because acceptance is about what the SDK
against the running harness can demonstrate, not which TypeScript files exist.

## Human decisions

- [x] Choose the interim package identity and registry. — Decision: Keep `@stacklok/mecatl-sdk` and publish the `0.0.x` preview line to GitHub Packages.
- [x] Choose interim publish authority and provenance. — Decision: Use only the publish job's ephemeral `GITHUB_TOKEN` and GitHub artifact attestations over the exact tarball.
- [x] Choose the first interim and npmjs versions. — Decision: Release GitHub Packages `0.0.1` first and reserve a fresh `0.1.0` for npmjs cutover.
- [x] Choose whether npmjs cutover dual-publishes. — Decision: Flip the one canonical registry at `0.1.0`; never promote or republish a GitHub Packages `0.0.x` version.

## Interface contract

- **gRPC / protobuf:** None — this plan classifies and wraps existing descriptors without changing the wire contract.
- **Exported Go APIs / interfaces:** None — only root-module parity guards inspect Go sources; no exported Go API changes.
- **Tool schemas:** None — the SDK release and distribution path changes no model-visible tool.
- **CLI / config:** The interim consumer configures `@stacklok:registry=https://npm.pkg.github.com` and authenticates with a `read:packages` PAT; the package declares that registry in `publishConfig`.
- **Events / persistence:** None — release metadata and attestations do not alter harness events or persisted session state.
- **Security / authority:** `verify` remains read-only; only the tag-gated, environment-protected `publish` job receives ephemeral package, OIDC, and attestation authority.
- **Compatibility / migration:** GitHub Packages owns only deletable `0.0.x` previews; npmjs begins from a fresh, permanent `0.1.0` line after an explicit one-registry cutover.

## Why these scope cuts

- **The plan is client-side by default.** Nothing edits `contracts/proto/`,
  production `internal/adapter/server/`, or `cmd/mecated/`. Root-module Go
  `_test.go` parity guards may read those sources and the TypeScript catalog.
  The audit found no server pre-PR needed: `ApprovePlan` already streams the
  two runs `PlanResolution` was designed to represent.
- **“Public” is a projection, not a guess.** The generated descriptors enumerate
  the 67 HarnessService and 10 ScheduleService RPCs; the explicit RPC-to-Service
  mapping projects those through `serviceAccessTable`, the existing reviewed
  public-boundary classification. The table contains non-RPC boundaries too,
  so comparing all its keys directly to 77 descriptors would be a false gate.
- **A transport exception is not a spelling for “skip”.** Each RPC has both a
  gRPC and HTTP classification. The exact gRPC-only set is separately asserted
  and starts with `StreamSessionLive` only. HTTP-only control routes live in a
  different inventory and cannot make an absent RPC row green.
- **Sixty-plus raw methods do not become sixty API designs.** Scenarios 3 and 4
  apply one thin-wrapper rule. Teams and plan resolution are the only new
  ergonomic objects because they own streaming lifecycle and terminal
  invariants.
- **Plan resolution follows the wire.** `session.resolvePlan()` operates on a
  durably parked session and consumes the existing resumed-run-then-continuation
  stream. Live `query()` plan handling uses its owned Converse stream and an
  SDK continuation whose fixed prompt is parity-tested against the Go constant;
  it does not call `ApprovePlan` while a live run is registered.
- **The browser matrix is intentionally asymmetric.** Chromium proves the full
  flow once; Firefox and WebKit prove import/connect/run/attach. One
  worker-scoped same-checkout daemon, strict timeouts, failure-only traces, and
  no blanket retry bound time and make deterministic failures visible.
- **The publication path is exercised before it receives authority.** A manual dry run
  reaches pack and exact inventory inspection but has no publish authority.
  Only the path-qualified tag reaches `npm publish`; GitHub Packages settings
  are a named human prerequisite, and no long-lived npm credential is added.
- **Verify names follow M1–M3's convention.** Go proofs are
  `TestSDKTypescriptRelease_ScenarioN_*`; Node-side TypeScript proofs use the
  strict `vitest:<path>#<base64url-title>` resolver form, while real-browser
  proofs use the sibling `playwright:<path>#<base64url-title>` form, so a
  renamed title cannot degrade to file-level coverage.

## Out of scope

| Item | Defer-to | ADR / decision |
|---|---|---|
| Binary auto-download | never | [ADR-0292](../adr/0292-typescript-sdk-local-daemon-and-tools.md) Decision 1 |
| Windows local spawn / named pipes | post-v0.1 | [#821](https://github.com/stacklok/mecatl/issues/821) explicit non-goals |
| Remote callback-tool hosting / reverse callback channel | post-v0.1 | [ADR-0292](../adr/0292-typescript-sdk-local-daemon-and-tools.md) Decision 14 |
| Dynamic callback-tool add/remove | post-v0.1 | [ADR-0292](../adr/0292-typescript-sdk-local-daemon-and-tools.md) Decision 9 |
| React bindings | post-v0.1 | [#821](https://github.com/stacklok/mecatl/issues/821) explicit non-goals |
| Python SDK | separate project | [#821](https://github.com/stacklok/mecatl/issues/821) explicit non-goals |
| Deno / edge runtimes | post-v0.1 | [ADR-0279](../adr/0279-typescript-sdk-architecture.md) runtime contract |
| Studio migration | separate project | [#821](https://github.com/stacklok/mecatl/issues/821) explicit non-goals |
| Any stdio MCP transport | never | [`AGENTS.md`](../../AGENTS.md) — “No stdio MCP, ever” |
| Downstream fake-transport testkit | [#872](https://github.com/stacklok/mecatl/issues/872) | [ADR-0279](../adr/0279-typescript-sdk-architecture.md) Decision 3 |
| HTTP steer completion | [#873](https://github.com/stacklok/mecatl/issues/873) | steering stays feature-gated |
| Redis follow-pool capacity/lifecycle | [#876](https://github.com/stacklok/mecatl/issues/876) | [ADR-0288](../adr/0288-typescript-sdk-durable-attachment.md) reconnect residual |
| Slack bot reference | [#881](https://github.com/stacklok/mecatl/issues/881) | not the concise schedules example in this plan |
| Attached `approve()` / `resolveAsk()` ergonomics | later server work | [ADR-0288](../adr/0288-typescript-sdk-durable-attachment.md) Decision 6; raw `ApprovePlan` remains reachable |
| A browser BFF implementation | application-owned | [ADR-0304](../adr/0304-typescript-sdk-public-surface-and-release.md) Decision 6 — docs and example shape only |

## In scope — 12 scenarios, in implementation order

Scenarios are listed in implementation order. Each is independently demoable;
later scenarios assume earlier ones but do not change their acceptance
criteria. Within each, ACs progress happy path → richer happy path → edges →
cross-cutting. TypeScript proofs use the strict `vitest:` resolver token and
show the human-readable title after it.

---

### Scenario 1 — The public RPC catalog and descriptor-to-transport parity gate

One reviewed SDK catalog classifies all 77 generated public RPC descriptors and
maps them to the public application boundaries in `serviceAccessTable`
([ADR-0304](../adr/0304-typescript-sdk-public-surface-and-release.md) Decision 1;
[#821](https://github.com/stacklok/mecatl/issues/821) M4 item 1 and AC2/AC10).

**Work:** the typed catalog; unary/server-stream/bidi metadata; explicit gRPC,
HTTP, and backing-Service classifications; the root Go structural guard; a
separate inventory for HTTP-only controls.

**Acceptance:**
- AC1.1: The SDK catalog key set is exactly the generated HarnessService and
  ScheduleService descriptor key set — 67 plus 10 today — with no missing,
  duplicate, or stale row, and every row classifies both raw transports. Other
  generated `mecatl.v1` services are excluded only through a pinned exact
  literal set (initially `{LocalSessionContextService}`, which is
  operator-enabled and local-trust-scoped), so adding a third public service is
  a visible diff rather than a silent gate shrink.
  - verify: TestSDKTypescriptRelease_Scenario1_RPCTransportCatalogParity
- AC1.2: The reviewed gRPC-only set is exactly `{StreamSessionLive}`, and the
  classification type structurally admits no value outside the enumerated
  gRPC / HTTP / route-family forms — there is no `unsupported` variant to
  select. The route-family form is itself pinned to an exact literal set
  (initially `{Converse}`).
  - verify: TestADR_0304_ExactGRPCOnlySet
- AC1.3: HTTP-only controls are callable from their separate inventory but do
  not satisfy, shadow, or replace any RPC catalog entry.
  - verify: vitest:sdk/typescript/test/rpc-catalog.test.ts#SFRUUC1vbmx5IGNvbnRyb2xzIGRvIG5vdCBzYXRpc2Z5IFJQQyBjb3ZlcmFnZQ — `sdk/typescript/test/rpc-catalog.test.ts :: "HTTP-only controls do not satisfy RPC coverage"`
- AC1.4: Every descriptor row names a real `*server.Service` method with a valid
  `serviceAccessTable` entry, and every classified RPC-reachable Service method
  is represented by exactly one descriptor row. The complement — classified
  Service methods deemed not RPC-reachable — is pinned as an exact literal set,
  so moving a method out of the gate is a visible diff.
  - verify: TestSDKTypescriptRelease_Scenario1_PublicServiceProjectionParity
- AC1.5: The catalog's unary, server-streaming, and bidirectional classifications
  match the generated descriptor shapes, including `RunTeam`, `ApprovePlan`,
  `StreamSessionEvents`, `StreamSessionLive`, `WatchSessionEvents`, and
  `Converse`.
  - verify: TestSDKTypescriptRelease_Scenario1_StreamingShapeParity

---

### Scenario 2 — Complete HTTP routing and raw reachability

Every non-gRPC-only descriptor receives an explicit HTTP route mapping, and the
HTTP/SSE raw transport consumes streams incrementally rather than buffering
them ([ADR-0304](../adr/0304-typescript-sdk-public-surface-and-release.md)
Decision 1; [Architecture](../architecture.md)'s SDK transport boundary).

**Work:** finish the HTTP route table, path/query/body codecs, JSON/SSE response
classification, and raw operations on `.` and `./node`; add Go route parity
against the handler inventory.

**Acceptance:**
- AC2.1: Every RPC not in the exact gRPC-only set has one reviewed HTTP method,
  path template, input mapping, and JSON-or-SSE response classification, with no
  stale route entry.
  - verify: TestSDKTypescriptRelease_Scenario2_HTTPRouteParity
- AC2.2: `RunTeam`, `ApprovePlan`, `StreamSessionEvents`, and
  `WatchSessionEvents` yield decoded items as SSE frames arrive and do not wait
  for response EOF or retain the whole stream.
  - verify: vitest:sdk/typescript/test/http-routes.test.ts#c2VydmVyIHN0cmVhbXMgZGVjb2RlIHdpdGhvdXQgYnVmZmVyaW5nIG9uIHRoZSBIVFRQIHRyYW5zcG9ydA — `sdk/typescript/test/http-routes.test.ts :: "server streams decode without buffering on the HTTP transport"`
- AC2.3: URL identifiers, query parameters, optional empty bodies, and response
  bodies match the Go handlers exactly, including schedules, teams, MCP reads,
  and storage plan/apply/job routes.
  - verify: TestSDKTypescriptRelease_Scenario2_HTTPCodecParity
- AC2.4: An injected fake transport can invoke every catalogued RPC through the
  public raw seam on gRPC and, except the exact gRPC-only set and the pinned
  route-family set, HTTP, preserving common request options and normalized
  errors.
  - verify: vitest:sdk/typescript/test/http-routes.test.ts#cmF3IG9wZXJhdGlvbnMgcmVhY2ggZXZlcnkgY2xhc3NpZmllZCBSUEMgb24gYm90aCB0cmFuc3BvcnRz — `sdk/typescript/test/http-routes.test.ts :: "raw operations reach every classified RPC on both transports"`

- AC2.5: Each row's HTTP (method, path template) equals the registration in
  `internal/adapter/server/http.go` whose handler invokes that row's
  AC1.4-declared backing `*server.Service` method, and no two rows share a
  (method, path) pair outside the pinned route-family set. A syntactically valid
  route aimed at the wrong handler therefore fails, closing the
  "says something rather than something true" hole a fake transport cannot see.
  - verify: TestSDKTypescriptRelease_Scenario2_RouteToServiceInjectivity
- AC2.6: The RPC catalog's HTTP classifications and the HTTP-only control
  inventory together account for every route registered in
  `internal/adapter/server/http.go`, with no route in both and none in neither,
  so a new server control route cannot be invisible to both inventories.
  - verify: TestSDKTypescriptRelease_Scenario2_HTTPRoutePartition

---

### Scenario 3 — Thin namespaces batch A: MCP inventory, agents, commands/worktrees, models

The first discoverability batch applies the one thin-wrapper rule without
inventing namespace-specific domain objects
([ADR-0304](../adr/0304-typescript-sdk-public-surface-and-release.md) Decision 2;
[#821](https://github.com/stacklok/mecatl/issues/821) M4 item 1).

**Work:** named namespaces for MCP resources/prompts/sources/ToolHive groups,
agents, commands, worktrees, and models; public exports and API reports.

**Acceptance:**
- AC3.1: Each batch-A namespace method takes a generated request or a lossless
  identifier convenience and returns the generated response without a second
  wire model.
  - verify: vitest:sdk/typescript/test/namespaces-core.test.ts#Y29yZSBpbnZlbnRvcnkgbmFtZXNwYWNlcyBhcmUgdGhpbiBnZW5lcmF0ZWQtdHlwZSB3cmFwcGVycw — `sdk/typescript/test/namespaces-core.test.ts :: "core inventory namespaces are thin generated-type wrappers"`
- AC3.2: Namespace calls preserve abort, headers/credentials, deadlines, typed
  server errors, and transport selection exactly as the raw call does.
  - verify: vitest:sdk/typescript/test/namespaces-core.test.ts#dGhpbiBuYW1lc3BhY2UgbWV0aG9kcyBwcmVzZXJ2ZSByZXF1ZXN0IG9wdGlvbnMgYW5kIHR5cGVkIGVycm9ycw — `sdk/typescript/test/namespaces-core.test.ts :: "thin namespace methods preserve request options and typed errors"`
- AC3.3: Isomorphic namespaces are exported from `.`, remain usable from
  `./node`, and introduce no Node builtin into the browser entrypoint graph.
  - verify: vitest:sdk/typescript/test/package.test.ts#dGhlIG5hbWVzcGFjZSBiYXRjaGVzIGFyZSBleHBvcnRlZCBmcm9tIGV2ZXJ5IHJ1bnRpbWUgZW50cnlwb2ludA — `sdk/typescript/test/package.test.ts :: "the namespace batches are exported from every runtime entrypoint"`

---

### Scenario 4 — Thin namespaces batch B: learning, schedules, dream plans, adoption, and storage

The second batch completes the non-team raw surface under the same shape rule,
including privileged and mutation-shaped operations without client-side policy
invention ([ADR-0304](../adr/0304-typescript-sdk-public-surface-and-release.md)
Decision 2; [ADR-0226](../adr/0226-session-storage-maintenance.md)).

**Work:** skills/learned skills, learning attempts/proposals, reflection,
soul/user model, schedules/fires, dream plans, session inventory/adoption, and
storage health/migration/cleanup namespaces.

**Acceptance:**
- AC4.1: Every batch-B method follows the generated-type thin-wrapper rule and
  delegates authorization, validation, and lifecycle state to the server.
  - verify: vitest:sdk/typescript/test/namespaces-ops.test.ts#b3BlcmF0aW9uYWwgbmFtZXNwYWNlcyBhcmUgdGhpbiBnZW5lcmF0ZWQtdHlwZSB3cmFwcGVycw — `sdk/typescript/test/namespaces-ops.test.ts :: "operational namespaces are thin generated-type wrappers"`
- AC4.2: Schedule fire results and storage plan/apply/resume/cancel/job responses
  remain the exact generated response types, including unknown future enum
  values and optional fields.
  - verify: vitest:sdk/typescript/test/namespaces-ops.test.ts#c2NoZWR1bGUgZmlyZSBhbmQgc3RvcmFnZSBtdXRhdGlvbnMgcHJlc2VydmUgZXhhY3QgZ2VuZXJhdGVkIHJlc3BvbnNlcw — `sdk/typescript/test/namespaces-ops.test.ts :: "schedule fire and storage mutations preserve exact generated responses"`
- AC4.3: The batch adds naming and discoverability only — no bespoke retry,
  pagination, cached state, or client-side maintenance job state machine.
  - verify: vitest:sdk/typescript/test/namespaces-ops.test.ts#dGhlIG9wZXJhdGlvbmFsIG5hbWVzcGFjZSBiYXRjaCBhZGRzIG5vIGJlc3Bva2Ugd2lyZSB0eXBlcw — `sdk/typescript/test/namespaces-ops.test.ts :: "the operational namespace batch adds no bespoke wire types"`
- AC4.4: This batch made all 84 descriptor operations then present callable
  through the public raw seam. [ADR-0346](../adr/0346-run-id-addressed-prompt-free-controls.md)
  later added four run-control RPCs, so the current exact catalog proof covers
  all 88; every non-team unary operation has a discoverable named
  client/session namespace where #821 calls for one.
  - verify: vitest:sdk/typescript/test/rpc-catalog.test.ts#YWxsIDg4IHJhdyBSUENzIGFyZSBjYWxsYWJsZSB0aHJvdWdoIHRoZSBwdWJsaWMgcmF3IHNlYW0 — `sdk/typescript/test/rpc-catalog.test.ts :: "all 88 raw RPCs are callable through the public raw seam"`

---

### Scenario 5 — Ergonomic teams and terminal outcome discipline

`client.teams.create()` returns a handle for the seven team RPCs while reusing
M1's event unions ([ADR-0304](../adr/0304-typescript-sdk-public-surface-and-release.md)
Decision 3; [ADR-0014](../adr/0014-agent-teams.md)).

**Work:** `Team`, its lifecycle methods, single-consumption team run, outcome
validation, tighten-only token option, and child-ID refusal coverage.

**Acceptance:**
- AC5.1: `client.teams.create()` returns a `Team` carrying the created team ID
  and typed initial roster response rather than only the raw response.
  - verify: vitest:sdk/typescript/test/team.test.ts#dGVhbSBjcmVhdGlvbiByZXR1cm5zIGFuIGVyZ29ub21pYyBUZWFt — `sdk/typescript/test/team.test.ts :: "team creation returns an ergonomic Team"`
- AC5.2: `team.spawn()`, `team.message()`, and `team.cancel()` bind the team ID,
  preserve generated member/message fields, and surface normalized server
  failures.
  - verify: vitest:sdk/typescript/test/team.test.ts#dGVhbSBtZW1iZXJzIHNwYXduIG1lc3NhZ2UgYW5kIGNhbmNlbCB0aHJvdWdoIHRoZSB0ZWFtIGhhbmRsZQ — `sdk/typescript/test/team.test.ts :: "team members spawn message and cancel through the team handle"`
- AC5.3: `team.run()` yields the existing discriminated team-event union, has
  the same iterate-or-`result()` single-consumption rule as `Run`, closes
  normally on exactly one terminal `outcome`, and throws a protocol error on
  missing or duplicate outcomes.
  - verify: vitest:sdk/typescript/test/team.test.ts#dGVhbSBydW4gc3RyZWFtcyBldmVudHMgYW5kIHJlcXVpcmVzIG9uZSB0ZXJtaW5hbCBvdXRjb21l — `sdk/typescript/test/team.test.ts :: "team run streams events and requires one terminal outcome"`
- AC5.4: `maxTeamTokens` is optional and tighten-only in the SDK surface; it
  maps to `max_team_tokens` and is never described or accepted as increasing a
  daemon-owned cap.
  - verify: vitest:sdk/typescript/test/team.test.ts#bWF4IHRlYW0gdG9rZW5zIGNhbiBvbmx5IHRpZ2h0ZW4gdGhlIGRhZW1vbiBidWRnZXQ — `sdk/typescript/test/team.test.ts :: "max team tokens can only tighten the daemon budget"`
- AC5.5: A server member session ID shaped `team-<id>-<member>` is preserved in
  team events but remains refused by M2 `session.attach()` as a child ID.
  - verify: vitest:sdk/typescript/test/team.test.ts#dGVhbSBtZW1iZXIgc2Vzc2lvbiBpZHMgcmVtYWluIGNoaWxkIGlkcyB0aGF0IGF0dGFjaCByZWZ1c2Vz — `sdk/typescript/test/team.test.ts :: "team member session ids remain child ids that attach refuses"`
- AC5.6: `team.list()` returns the typed roster/task/quiescence snapshot and
  `team.cleanup()` returns the typed cleanup response; cleanup is never implied
  by consuming a run.
  - verify: vitest:sdk/typescript/test/team.test.ts#dGVhbSBsaXN0IGFuZCBjbGVhbnVwIHByZXNlcnZlIHR5cGVkIHNlcnZlciByZXNwb25zZXM — `sdk/typescript/test/team.test.ts :: "team list and cleanup preserve typed server responses"`

---

### Scenario 6 — Streaming plan resolution and `query()` plan mode

The SDK models the actual `ApprovePlan` stream: the parked run resumes under its
existing ID and an allow starts one continuation with a new ID
([ADR-0304](../adr/0304-typescript-sdk-public-surface-and-release.md) Decision 4;
[ADR-0069](../adr/0069-plan-approval-gate.md), which owns `target_mode` ->
verdict, `StopPlanApproved`/`StopPlanIterate`, and the proceed message;
[ADR-0292](../adr/0292-typescript-sdk-local-daemon-and-tools.md) Decision 8,
whose plan-mode refusal AC6.5 lifts;
[#821](https://github.com/stacklok/mecatl/issues/821) “Events, runs, and controls”).

**Work:** `PlanResolution`, plan-specific callback/verdict types, transport
mapping, single-consumption and run partitioning, attachment composition,
`query()` live-plan composition, and proceed-message parity.

**Acceptance:**
- AC6.1: `session.resolvePlan()` on a durably parked plan yields the resumed
  run's events through its terminal before yielding a continuation with a
  different run ID, and `result()` returns both typed `RunResult`s in order.
  - verify: vitest:sdk/typescript/test/plan-resolution.test.ts#cmVzb2x2ZVBsYW4gc3RyZWFtcyB0aGUgcmVzdW1lZCBydW4gdGhlbiBpdHMgY29udGludWF0aW9u — `sdk/typescript/test/plan-resolution.test.ts :: "resolvePlan streams the resumed run then its continuation"`
- AC6.2: Any resolution whose resumed run does not reach `plan_approved` —
  deny, iterate, or an approving resolution whose resumed run ends on
  cancel/error — returns a resumed `RunResult` with no continuation; a third
  run ID, reversed ordering, or missing terminal is a protocol error rather
  than silently flattened.
  - verify: vitest:sdk/typescript/test/plan-resolution.test.ts#YSBkZW5pZWQgcGxhbiByZXNvbHV0aW9uIGhhcyBubyBjb250aW51YXRpb24gcnVu — `sdk/typescript/test/plan-resolution.test.ts :: "a denied plan resolution has no continuation run"`
- AC6.3: A `PlanResolution` may be iterated or drained with `result()` exactly
  once; concurrent or second consumption fails typed before issuing another
  control.
  - verify: vitest:sdk/typescript/test/plan-resolution.test.ts#UGxhblJlc29sdXRpb24gaGFzIG9uZSBjb25zdW1wdGlvbiBtb2Rl — `sdk/typescript/test/plan-resolution.test.ts :: "PlanResolution has one consumption mode"`
- AC6.4: An attachment bound to the resumed run ends at that run's terminal and
  never crosses into the continuation; `session.activity()` observes both IDs.
  - verify: vitest:sdk/typescript/e2e/plan-resolution.e2e.test.ts#YXR0YWNobWVudHMgcmVtYWluIGJvdW5kIHRvIG9uZSBydW4gZHVyaW5nIHBsYW4gcmVzb2x1dGlvbg — `sdk/typescript/e2e/plan-resolution.e2e.test.ts :: "attachments remain bound to one run during plan resolution"`
- AC6.5: `query({mode: "plan"})` refuses before resource creation without
  `onPlanApproval`; with it, the owned live run resolves the plan-specific ask,
  drains its terminal, and only on allow opens a **fresh** Converse stream (or
  the HTTP prompt route) for the new-ID continuation — never a second start
  frame on the live stream, which the server refuses with `InvalidArgument` and
  which cancels the active run. The one-shot result flattens both runs.
  - verify: vitest:sdk/typescript/e2e/plan-resolution.e2e.test.ts#cXVlcnkgcGxhbiBtb2RlIHJlcXVpcmVzIG9uUGxhbkFwcHJvdmFsIGFuZCBmbGF0dGVucyBib3RoIHJ1bnM — `sdk/typescript/e2e/plan-resolution.e2e.test.ts :: "query plan mode requires onPlanApproval and flattens both runs"`
- AC6.6: A plan-originated ask invokes only `onPlanApproval`, an ordinary tool
  ask invokes only `onPermissionAsk`, and the SDK's continuation prompt is
  byte-equal to `agent.PlanApprovedProceedText`. An operator note is out of
  scope for v0.1; if one is later accepted, the pin must cover the server's
  `PlanApprovedProceedText + "\n\nOperator note: " + note` composition, not
  the bare constant.
  - verify: TestADR_0304_PlanApprovalContractParity
- AC6.7: A terminal `EvResult` carrying `StopError` and an **empty** run ID
  after the resumed run's terminal — the server's honest
  `continuation run failed to start: …` shape — surfaces as a distinct typed
  continuation-start failure preserving that error text, never as a generic
  protocol error and never as a fabricated `RunResult` with an invented ID.
  - verify: vitest:sdk/typescript/test/plan-resolution.test.ts#Y29udGludWF0aW9uLXN0YXJ0IGZhaWx1cmUgcHJlc2VydmVzIHRoZSBzZXJ2ZXIgZXJyb3I — `sdk/typescript/test/plan-resolution.test.ts :: "continuation-start failure preserves the server error"`

---

### Scenario 7 — TypeScript declarations, API reports, and Node compatibility

The package develops on TS 6 but consumes on TS 5.7+, and its Node floor is 22
rather than whichever line CI happens to use today
([ADR-0279](../adr/0279-typescript-sdk-architecture.md) Decisions 2 and 4;
[ADR-0304](../adr/0304-typescript-sdk-public-surface-and-release.md) Decision 5).

**Work:** pinned TS 5.7 alias and declaration consumer, Node 22/current matrix,
`engines` correction, retained Bun leg, and explicit separation of API
Extractor from generated-protobuf gates.

**Acceptance:**
- AC7.1: A consumer project using the pinned latest TS 5.7 patch compiles the
  built `.` and `./node` declarations with no skip-lib-check escape hatch.
  - verify: vitest:sdk/typescript/test/declarations.test.ts#cHVibGljIGRlY2xhcmF0aW9ucyBjb21waWxlIHdpdGggVHlwZVNjcmlwdCA1Ljc — `sdk/typescript/test/declarations.test.ts :: "public declarations compile with TypeScript 5.7"`
- AC7.2: The same consumer compiles under the repository's pinned TS 6 and the
  ordinary SDK source typecheck remains TS 6.
  - verify: vitest:sdk/typescript/test/declarations.test.ts#cHVibGljIGRlY2xhcmF0aW9ucyBjb21waWxlIHdpdGggVHlwZVNjcmlwdCA2 — `sdk/typescript/test/declarations.test.ts :: "public declarations compile with TypeScript 6"`
- AC7.3: A `strategy.matrix` with `fail-fast: false` runs `task sdk:typecheck`,
  `task sdk:test`, `task sdk:build`, and `task sdk:api:check` on literal pinned
  Node versions `['22.x','24.x']` plus Bun 1.4.1; each leg declares its own
  `timeout-minutes`. The Go/buf codegen-freshness gate and the Go-binary e2e
  run **once**, not per Node leg, since neither is Node-dependent.
  `engines.node` is `>=22`, and matrix values are literal strings — never
  `latest` or `current`.
  - verify: TestSDKTypescriptRelease_Scenario7_NodeMatrixShape
- AC7.4: API Extractor reports for `.` and `./node` remain reviewed artifacts,
  while `./gen` stays governed by reproducible generation rather than being
  copied into those reports. `buf.yaml` declares a `breaking` stanza but **no
  workflow invokes `buf breaking --against`** today; the AC asserts only the
  gate that exists, and proto breaking-change detection is a named deferred
  decision rather than a claimed gate.
  - verify: TestSDKTypescriptRelease_Scenario7_CompatibilityGateSeparation

---

### Scenario 8 — Chromium full browser flow against exact-origin CORS

A real browser imports the packed surface and drives the existing HTTP/SSE API
through a same-checkout loopback daemon
([ADR-0304](../adr/0304-typescript-sdk-public-surface-and-release.md) Decision 6;
[#821](https://github.com/stacklok/mecatl/issues/821) AC1/AC3/AC12).

**Work:** Playwright harness under `sdk/typescript/e2e/browser/`, pinned browser
tooling, exact-origin fixture server, mock script, and Chromium full project.

**Acceptance:**
- AC8.1: Chromium imports the package entrypoint, connects, creates a session,
  runs to a typed terminal, and closes without cross-runtime leakage.
  - verify: playwright:sdk/typescript/e2e/browser/chromium.e2e.test.ts#Q2hyb21pdW0gY29tcGxldGVzIHRoZSBicm93c2VyIFNESyBjb250cm9sIGZsb3c — `sdk/typescript/e2e/browser/chromium.e2e.test.ts :: "Chromium completes the browser SDK control flow"`
- AC8.2: The browser's credentialed preflight succeeds only for the one exact
  configured origin, carries the expected allow headers and `Vary: Origin`, and
  a sibling origin is refused.
  - verify: playwright:sdk/typescript/e2e/browser/chromium.e2e.test.ts#dGhlIGJyb3dzZXIgdHJhbnNwb3J0IHBhc3NlcyBleGFjdC1vcmlnaW4gQ09SUyBwcmVmbGlnaHQ — `sdk/typescript/e2e/browser/chromium.e2e.test.ts :: "the browser transport passes exact-origin CORS preflight"`
- AC8.3: Chromium sends an in-memory multimodal prompt, observes an ordinary
  permission ask, resolves it through the callback, and reaches the scripted
  terminal without external network access.
  - verify: playwright:sdk/typescript/e2e/browser/chromium.e2e.test.ts#Q2hyb21pdW0gcnVucyBhIG11bHRpbW9kYWwgcHJvbXB0IGFuZCBwZXJtaXNzaW9uIGNhbGxiYWNr — `sdk/typescript/e2e/browser/chromium.e2e.test.ts :: "Chromium runs a multimodal prompt and permission callback"`
- AC8.4: Chromium cancels an owned run with the expected run ID, exercises
  strict steer when the feature is advertised (and asserts typed unsupported
  otherwise), then attaches and drains a persisted run.
  - verify: playwright:sdk/typescript/e2e/browser/chromium.e2e.test.ts#Q2hyb21pdW0gY2FuY2VscyBzdGVlcnMgd2hlbiBhZHZlcnRpc2VkIGFuZCByZWF0dGFjaGVz — `sdk/typescript/e2e/browser/chromium.e2e.test.ts :: "Chromium cancels steers when advertised and reattaches"`

---

### Scenario 9 — Firefox/WebKit smoke and macOS Node spawn

The remaining runtime claims get focused smoke coverage instead of three copies
of the full browser suite
([ADR-0304](../adr/0304-typescript-sdk-public-surface-and-release.md) Decision 6;
[ADR-0279](../adr/0279-typescript-sdk-architecture.md) Decision 4).

**Work:** Firefox and WebKit Playwright projects, shared bounded fixture, CI
cache/timeouts/traces, and a separate `macos-14` Node 22 spawn job.

**Acceptance:**
- AC9.1: Firefox imports `.`, connects over HTTP/SSE, runs to a terminal, and
  attaches to drain that run.
  - verify: playwright:sdk/typescript/e2e/browser/smoke.e2e.test.ts#RmlyZWZveCBpbXBvcnRzIGNvbm5lY3RzIHJ1bnMgYW5kIGF0dGFjaGVz — `sdk/typescript/e2e/browser/smoke.e2e.test.ts :: "Firefox imports connects runs and attaches"`
- AC9.2: WebKit imports `.`, connects over HTTP/SSE, runs to a terminal, and
  attaches to drain that run.
  - verify: playwright:sdk/typescript/e2e/browser/smoke.e2e.test.ts#V2ViS2l0IGltcG9ydHMgY29ubmVjdHMgcnVucyBhbmQgYXR0YWNoZXM — `sdk/typescript/e2e/browser/smoke.e2e.test.ts :: "WebKit imports connects runs and attaches"`
- AC9.3: On `macos-14` with Node 22, `spawn()` starts the same-checkout binary
  over UDS, reaches readiness, performs one run, and exits cleanly with its
  runtime directory removed. Because the runner's real `$TMPDIR`
  (`/var/folders/…/T/`) is the long-path input Linux never supplies, the job
  also asserts the resolved socket path stays within
  `spawn.ts`'s `DARWIN_SUN_PATH_BYTES` (104) bound and that the shorter-base
  fallback still binds when it triggers — the one thing Linux CI cannot prove.
  - verify: vitest:sdk/typescript/e2e/macos-spawn.e2e.test.ts#bWFjT1Mgc3Bhd25zIG92ZXIgVURTIGFuZCBleGl0cyBjbGVhbmx5 — `sdk/typescript/e2e/macos-spawn.e2e.test.ts :: "macOS spawns over UDS and exits cleanly"`
- AC9.4: Browser projects reuse one worker-scoped daemon, declare explicit
  per-test and per-job timeouts, collect traces only on failure, and use no
  blanket retry. Both the fixture-origin server and the daemon bind **ephemeral
  ports allocated per worker**, with `--cors-origins` derived from the fixture
  server's bound address, so concurrent workers cannot collide on a fixed port.
  The three browser projects and the `macos-14` job are **required** status
  checks with no `continue-on-error`. The browser cache key includes the runner
  label and the resolved Playwright version (not merely the lockfile hash), and
  a miss re-installs via a pinned `playwright install --with-deps`. Once setup
  completes, no test reaches any host other than the loopback daemon — browser
  binaries may be fetched during setup, which is not a test egress.
  - verify: inspection — review the runner config, fixture lifetime and port allocation, workflow timeouts, retry/trace policy, cache keys, and branch-protection required-check list

---

### Scenario 10 — Type-checked examples, public SDK pages, and living docs

The M1–M3 documentation deferral ends before publication, and every example is
compiled against only package exports
([ADR-0304](../adr/0304-typescript-sdk-public-surface-and-release.md) Decision 7;
[`AGENTS.md`](../../AGENTS.md)'s user-docs rule).

**Work:** concise examples; an examples typecheck project; `user-docs/` SDK
pages; architecture, implementation notes, cloud inventories, and corrected
package/PlanResolution wording.

**Acceptance:**
- AC10.1: Every committed SDK example imports only `.`, `./node`, or `./gen` and
  compiles in CI against the built package with no source-tree backdoor.
  - verify: vitest:sdk/typescript/test/examples.test.ts#ZXZlcnkgcHVibGlzaGVkIGV4YW1wbGUgdHlwZWNoZWNrcyBhZ2FpbnN0IHBhY2thZ2UgZXhwb3J0cw — `sdk/typescript/test/examples.test.ts :: "every published example typechecks against package exports"`
- AC10.2: Concise Node/Bun examples cover remote `connect()`, local
  `spawn()`/`query()`, and a callback tool without combining them into an
  application framework.
  - verify: vitest:sdk/typescript/test/examples.test.ts#dGhlIE5vZGUgYW5kIEJ1biBleGFtcGxlcyBjb3ZlciBjb25uZWN0IHNwYXduIHF1ZXJ5IGFuZCBjYWxsYmFjayB0b29scw — `sdk/typescript/test/examples.test.ts :: "the Node and Bun examples cover connect spawn query and callback tools"`
- AC10.3: Browser+BFF, permissions, durable attachment, teams, and schedules
  each have a focused example or public documentation section, and the BFF is
  explicitly guidance rather than shipped server code.
  - verify: inspection — review the `sdk/typescript/examples/` inventory and linked `user-docs/` sections for all five topics
- AC10.4: `user-docs/`, `docs/architecture.md`,
  `docs/design/IMPLEMENTATION-NOTES.md`, and relevant cloud inventories use
  `@stacklok/mecatl-sdk`, describe the two-run `PlanResolution`, and no longer
  present ADR 0292's M3 plan refusal as current behavior.
  - verify: inspection — review the living/public docs diff and run `task docs` plus `task site:build`

---

### Scenario 11 — Dry-runnable GitHub Packages workflow and tag isolation

A dedicated SHA-pinned workflow builds and inspects the exact npm artifact;
only a matching path-qualified tag can publish it
([ADR-0304](../adr/0304-typescript-sdk-public-surface-and-release.md) Decision 8,
as superseded in part by [ADR-0313](../adr/0313-interim-github-packages-typescript-sdk.md);
[ADR-0093](../adr/0093-provider-modules.md)'s tag discipline).

**Work:** SDK release workflow, dry-run dispatch, version/tag validation,
frozen install, generation cleanliness, all SDK gates, exact pack inspection,
ephemeral-token GitHub Packages publication, GitHub artifact attestation,
`publishConfig`, and two-workflow trigger parity.

**Acceptance:**
- AC11.1: The workflow is two jobs. `verify` holds `permissions: {contents:
  read}` with no `id-token`, runs checkout through exact tarball inspection, and
  uploads that tarball as the sole build artifact. `publish` declares `needs:
  verify`, `environment: github-packages-publish`, `permissions: {contents:
  read, packages: write, id-token: write, attestations: write}`, and `if:
  github.event_name == 'push' && startsWith(github.ref,
  'refs/tags/sdk/typescript/v')`; it installs nothing and runs no third-party
  lifecycle script. A `workflow_dispatch` run therefore never instantiates the
  only job able to mint an OIDC token or write a package or attestation.
  - verify: TestADR_0304_ManualDispatchIsDryRunOnly
- AC11.2: The tag must match
  `^sdk/typescript/v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$` — no
  prerelease or build metadata, so the implicit `latest` dist-tag is always
  correct — and must equal `sdk/typescript/v<package-version>`; the release
  checkout is the exact tag commit, and the workflow fails before install unless
  `$GITHUB_SHA` is an ancestor of `origin/main`, so a tag on an unmerged commit
  cannot publish.
  - verify: TestSDKTypescriptRelease_Scenario11_TagVersionParity
- AC11.3: The `verify` job performs a frozen pnpm install under an
  `onlyBuiltDependencies` allowlist, runs `task generate`, and rejects a dirty
  generated tree or SDK lockfile before build/pack. It also rejects the release
  unless the **packed** `package.json` declares `repository: {type: "git", url:
  "https://github.com/stacklok/mecatl.git", directory: "sdk/typescript"}`,
  `license: "Apache-2.0"`, `publishConfig.registry:
  "https://npm.pkg.github.com"`, no `publishConfig.access`, and a `version` equal
  to the tag's version — asserted on the packed manifest so `prepack` cannot
  alter it unobserved.
  - verify: TestSDKTypescriptRelease_Scenario11_GenerationCleanlinessGate
- AC11.4: Release runs SDK lint, both declaration compilers, unit/e2e/build/API
  gates, packs once, and applies the existing exact-inventory oracle — an exact
  allowlist of `dist/`, `package.json`, `README.md`, and `LICENSE`, no source,
  config, or test fixtures — to the same tarball it would publish. The `verify`
  job records that tarball's npm-format `sha512` integrity in
  `$GITHUB_STEP_SUMMARY`.
  - verify: vitest:sdk/typescript/test/package.test.ts#cGFja2VkIHRhcmJhbGwgY2FycmllcyBkaXN0IGFuZCBsaWNlbnNlIG9ubHk — `sdk/typescript/test/package.test.ts :: "packed tarball carries dist and license only"`
- AC11.5: A tag push publishes the **downloaded artifact by path** (`npm publish
  ./<name>-<version>.tgz`) to `https://npm.pkg.github.com`, never the package directory, so no
  `prepack`/`prepublishOnly`/`prepare` script runs at publish time; it
  re-verifies the tarball `sha512` against AC11.4's recorded value first. The
  workflow declares `permissions: {contents: read}` at top level, and only the
  publish job declares exactly `contents: read`, `packages: write`, `id-token:
  write`, and `attestations: write`; no job declares `contents: write`,
  `pull-requests:`, or `issues:`. The publish job configures
  `actions/setup-node` for `https://npm.pkg.github.com` and scope `@stacklok`,
  and its only `secrets.*` reference is `NODE_AUTH_TOKEN: ${{
  secrets.GITHUB_TOKEN }}`, the ephemeral job token rather than a stored
  secret. The workflow contains no `NPM_TOKEN`, `_authToken`, or `npm_config_*`
  auth variable and commits no `.npmrc`. A SHA-pinned
  `actions/attest-build-provenance` step attests the exact tarball before
  publication, `gh attestation verify <tgz> --repo stacklok/mecatl` verifies it,
  and the publish summary records the tarball `sha512` plus attestation URL;
  npm-native `dist.attestations` does not exist on GitHub Packages. The publish
  job runs Node >= 22.14.0 with a pinned npm >= 11.5.1 and asserts that floor
  before publishing. No `--access` flag is used.
  - verify: TestADR_0313_EphemeralJobTokenOnly
- AC11.6: The SDK workflow's `push.tags` is exactly `['sdk/typescript/v*']` and
  the root workflow's is exactly `['v*']`; neither uses `**`. A documented
  matcher implementing GitHub's rule that `*` does not match `/` yields this
  exact selection matrix: `sdk/typescript/v0.0.1` -> SDK only; `v0.0.1` -> root
  only; `sdk/typescript/v9.9.9` -> SDK only; `v1.2.3` -> root only. Any change
  to either pattern list fails the test rather than being silently
  re-evaluated. Push isolation is therefore mechanical; **dispatch** isolation
  is not a glob property, so the AC additionally records that the root
  workflow's `workflow_dispatch` accepts an unvalidated free-text tag input.
  - verify: TestADR_0304_TagTriggerIsolation

- AC11.8: No file in the packed tarball derives from a source carrying a
  non-Apache-2.0 SPDX identifier. `contracts/proto/mecatl/v1/*.proto` and the
  generated `sdk/typescript/src/gen/**` headers currently declare
  `LicenseRef-Stacklok-Proprietary` while `package.json` declares
  `Apache-2.0`, and `dist/gen/**` is packed — so the guard **fails closed** and
  blocks publication until an authorized relicense corrects those headers.
  - verify: TestSDKTypescriptRelease_Scenario11_PackedLicenseProvenance
- AC11.7: The workflow declares `concurrency: {group:
  release-sdk-typescript-${{ github.ref }}, cancel-in-progress: false}` — a
  half-published release is worse than a slow one — every job declares
  `timeout-minutes`, and every job pins `runs-on: ubuntu-24.04`, never
  `ubuntu-latest`.
  - verify: TestSDKTypescriptRelease_Scenario11_WorkflowBounding

---

### Scenario 12 — Human-gated `v0.0.1` GitHub Packages publication

The final scenario is an inspection checklist executed only after Scenarios
1–11 land and GitHub Packages authority exists
([ADR-0313](../adr/0313-interim-github-packages-typescript-sdk.md) Decisions
1–5; [#821](https://github.com/stacklok/mecatl/issues/821) M4 item 4 and AC13).

**Work:** release PR version bump, human prerequisite confirmation, reviewed tag
creation, workflow observation, GitHub attestation and package metadata
inspection, and issue status reconciliation without closing #821 from an
implementation PR.

**Acceptance:**
- AC12.1: The release PR changes package version `0.0.0` to `0.0.1`, keeps
  `publishConfig.registry: "https://npm.pkg.github.com"`, keeps
  `publishConfig.access` absent, updates the lockfile/API reports where
  required, uses no `--access` flag, and contains no release tag.
  - verify: inspection — review the release PR diff and its green required checks before merge
- AC12.2: Before tagging, the release checklist records four prerequisites: a
  named maintainer accepting responsibility for the tag; confirmation that no
  `NPM_TOKEN`-shaped secret exists at repository, environment, or organization
  scope; authorized relicensing of the proprietary-headed generated sources
  required by AC11.8; and confirmation that organization/repository package
  settings permit the repository-scoped ephemeral `GITHUB_TOKEN` to publish.
  - verify: inspection — release checklist records the responsible maintainer, absence of long-lived npm credentials, licence clearance, and package-settings clearance
- AC12.3: The annotated or lightweight `sdk/typescript/v0.0.1` tag points
  exactly at the reviewed release commit on `main`, and no root `v0.0.1` tag is
  created as part of the SDK release.
  - verify: inspection — compare `git rev-parse sdk/typescript/v0.0.1`, the merged release commit, and the Actions trigger run
- AC12.4: `gh attestation verify <downloaded-tarball> --repo stacklok/mecatl`
  succeeds for the exact release artifact, and `npm view
  @stacklok/mecatl-sdk@0.0.1 --registry=https://npm.pkg.github.com --json
  dist.integrity` returns its registry integrity.
  - verify: inspection — run the named GitHub and npm commands and record their output in the release checklist
- AC12.5: The GitHub Packages `dist.integrity` equals the `sha512` recorded in
  the release run's job summary (AC11.4), the extracted published tarball's file
  list equals the AC11.4 inventory, and no root image release job ran for the
  SDK tag.
  - verify: inspection — compare the recorded `sha512` and inventory against the GitHub Packages artifact, and inspect the Actions runs for the tag

**Follow-up checklist — npmjs cutover (`v0.1.0`, tracked under #821):**

- [ ] Make `stacklok/mecatl` public; npmjs does not emit provenance for a
      private source repository.
- [ ] Confirm `@stacklok` npm organization ownership and package publish rights.
- [ ] Configure npm trusted publishing for `stacklok/mecatl`, the exact
      `.github/workflows/release-sdk-typescript.yml` filename, and its release
      environment.
- [ ] Flip the one canonical publish target from GitHub Packages to npmjs for a
      fresh `0.1.0` release; do not dual-publish or promote a `0.0.x` tarball.
- [ ] Cut a fresh `sdk/typescript/v0.1.0` tag only after the release PR is on
      `main`, then verify npm-native provenance and integrity.

This checklist implements [ADR-0313](../adr/0313-interim-github-packages-typescript-sdk.md)
Decision 6. It is a follow-up under #821, not an exit condition for this plan.

---

## Cross-cutting deliverables

- [ADR-0304](../adr/0304-typescript-sdk-public-surface-and-release.md), which
  defines the M4 surface, and [ADR-0313](../adr/0313-interim-github-packages-typescript-sdk.md),
  Proposed for workflow implementation and changed to Accepted only by the
  verified `v0.0.1` release close-out.
- API Extractor reports for `.` and `./node` regenerated intentionally as
  public namespaces, `Team`, and `PlanResolution` land. `./gen` remains
  codegen-governed per [ADR-0279](../adr/0279-typescript-sdk-architecture.md).
- Root-module Go parity guards for descriptor/catalog/public-boundary mapping,
  HTTP routes, plan continuation text, compatibility-gate separation, and
  release-workflow trigger/credential structure.
- `sdk/typescript/e2e/browser/` Playwright harness and a separate macOS spawn
  smoke, all offline against same-checkout `mecated --mock-script`.
- `sdk/typescript/examples/` plus a no-emit compiler gate that resolves only
  published entrypoints.
- `user-docs/`, `docs/architecture.md`,
  `docs/design/IMPLEMENTATION-NOTES.md`, and relevant cloud inventories updated
  for the complete M4 preview surface; `task docs` and `task site:build` green.
- `sdk/typescript/package.json` declares Node `>=22`, version `0.0.1` in the
  release PR, and `publishConfig.registry: https://npm.pkg.github.com` without
  `publishConfig.access`; `website/` remains npm-based.
- A `package-ecosystem: npm` Dependabot entry for `/sdk/typescript` (grouped
  minor+patch, matching the seven existing `gomod` entries) and a vulnerability
  gate over the SDK's production closure (`pnpm audit --prod` or
  `osv-scanner`) in the `sdk` CI job. `.github/dependabot.yml` has **no** npm
  ecosystem today and `task vuln` is govulncheck (Go-only), so the risk
  section's "dependency update PRs carry the ordinary compatibility refresh"
  currently assumes a mechanism that does not exist. Note that
  `package.test.ts`'s exact-pin assertions must be updated by the same PR.
- The browser suite's `playwright:` resolver in `.actrace.yml`, kept beside the
  Node-side `vitest:` resolver so each runner's titled proofs resolve strictly.
- No `engine/` API or proto change is expected. If one appears, stop and revise
  the scope rather than silently widening this client-side plan.

## Sequencing recommendation

Scenario 1 is first because every later wrapper and route is checked against
its catalog. Scenario 2 completes raw HTTP reachability before Scenarios 3 and
4 add discoverability. Those namespace batches are separated by review size,
not different architecture, and can be prepared in parallel but land in order.
Scenario 5 consumes the team rows; Scenario 6 consumes the plan stream and is
the last public ergonomic change. Scenario 7 then freezes the declaration and
runtime compatibility line. Scenario 8 introduces the browser harness once the
surface is stable; Scenario 9 adds focused engine/platform legs without
duplicating Chromium's full flow. Scenario 10 documents exactly that frozen
surface. Scenario 11 can dry-run the release artifact only after examples and
docs are present. Scenario 12 is deliberately last and human-gated: it creates
the only external state in the stack.

## Named tests landing in this plan

- `TestSDKTypescriptRelease_Scenario1_RPCTransportCatalogParity`
- `TestADR_0304_ExactGRPCOnlySet`
- `TestSDKTypescriptRelease_Scenario1_PublicServiceProjectionParity`
- `TestSDKTypescriptRelease_Scenario1_StreamingShapeParity`
- `TestSDKTypescriptRelease_Scenario2_HTTPRouteParity`
- `TestSDKTypescriptRelease_Scenario2_HTTPCodecParity`
- `TestSDKTypescriptRelease_Scenario2_RouteToServiceInjectivity`
- `TestSDKTypescriptRelease_Scenario2_HTTPRoutePartition`
- `TestADR_0304_PlanApprovalContractParity`
- `TestSDKTypescriptRelease_Scenario7_CompatibilityGateSeparation`
- `TestSDKTypescriptRelease_Scenario7_NodeMatrixShape`
- `TestADR_0304_ManualDispatchIsDryRunOnly`
- `TestSDKTypescriptRelease_Scenario11_TagVersionParity`
- `TestSDKTypescriptRelease_Scenario11_GenerationCleanlinessGate`
- `TestADR_0313_EphemeralJobTokenOnly`
- `TestADR_0304_TagTriggerIsolation`
- `TestSDKTypescriptRelease_Scenario11_WorkflowBounding`
- `TestSDKTypescriptRelease_Scenario11_PackedLicenseProvenance`

Two naming families are deliberate: `TestADR_0304_*` and `TestADR_0313_*` pin
costly-to-reverse ADR decisions as durable invariants (the exact gRPC-only set,
the plan-approval contract, tag/dispatch isolation, and ephemeral GitHub
Packages authority), following the repository's existing `TestADR_NNNN_*`
convention; the
`TestSDKTypescriptRelease_Scenario*` names are scenario parity guards that may
be renamed with their scenario.

The Go tests live in the root module beside
`internal/adapter/server/sdk_typescript_*_test.go`; they read server, workflow,
and SDK sources, so they never belong under `engine/`
([`AGENTS.md`](../../AGENTS.md)'s module-boundary rule). All other executable
proofs are Vitest or Playwright/Vitest suites under `sdk/typescript/`, cited by
exact title.

## Definition of done

1. `task lint` and `task test` pass, plus `task sdk:lint`,
   `task sdk:typecheck`, the pinned TS 5.7 declaration consumer,
   `task sdk:test`, `task sdk:e2e`, `task sdk:build`, `task sdk:pack`, and
   `task sdk:api:check`.
2. Chromium full flow, Firefox/WebKit smoke, Node 22/current, Bun 1.4.1, and the
   `macos-14` Node spawn smoke are green and offline.
3. `task generate` reproduces generated Go and TypeScript trees
   byte-identically; protobuf breaking/freshness and API Extractor gates are
   green under their separate ownership.
4. `task docs` and `task site:build` pass for living docs and the new public SDK
   pages; the npm-managed `website/` tree is unchanged as a package manager.
5. `task ac-trace-strict` resolves every exact Go/Vitest proof after this plan
   becomes `landed`.
6. The Taskfile-built `./bin/mecademo` still prints a complete offline session.
7. `contracts/proto/`, production `internal/adapter/server/`, and
   `cmd/mecated/` are byte-unchanged across Scenarios 1–11; only root-module Go
   tests inspect those contracts.
8. The release workflow's manual dry run succeeds before the release PR is
   tagged, and no path from manual dispatch can publish.
9. All four human prerequisites are checked before `sdk/typescript/v0.0.1` is
   created; the GitHub Packages artifact, attestation, integrity, and inventory
   pass Scenario 12.
10. No test reaches a live model, npm publish endpoint, or external service
    before the human-gated tag scenario.

## Deferred decisions and known risks

- **The npmjs `v0.1.0` cutover is deferred under #821.** `0.1.0` is reserved as
  the first npmjs version and no GitHub Packages `0.0.x` version is promoted or
  reused there. Cutover requires a public `stacklok/mecatl` repository,
  confirmed `@stacklok` npm organization publish rights, and an npm
  trusted-publisher record for the exact SDK workflow filename and release
  environment. Per ADR 0313 Decision 6, the workflow flips its one canonical
  target rather than dual-publishing.
- **The gRPC-only set can grow only through a visible decision.** The parity
  gate deliberately rejects a convenient `unsupported` bucket and asserts the
  exact `{StreamSessionLive}` set. A future genuinely gRPC-only RPC needs an ADR
  or explicit amendment plus a changed test; that friction is intentional.
- **`Converse` has no one-route HTTP twin.** Its catalog row maps the gRPC bidi
  descriptor to the HTTP prompt/control route family. The raw HTTP abstraction
  remains normalized operations, not a fabricated bidi stream, and the route
  parity test must not mistake that honest classification for omission.
- **The live and parked plan paths use different server entry points.**
  `session.resolvePlan()` uses the atomic two-run `ApprovePlan` stream only
  after the session is durably parked and no live run remains. `query()` owns a
  live Converse stream, resolves its plan-specific ask there, then starts the
  continuation client-side. The proceed-message parity guard is the cost of
  that no-server-change choice. If the server makes the message private or adds
  more continuation state, M4 must choose a small server pre-PR instead of
  weakening the contract.
- **A continuation-start failure may lack a second run ID.** The current server
  can synthesize a terminal `StopError` if continuation admission fails before
  a run exists. `PlanResolution` surfaces that as a protocol/continuation-start
  failure, not as a fabricated `RunResult` with a fake ID.
- **Single consumption spans both plan runs.** `PlanResolution` owns the merged
  wire iterator; it cannot hand out two independently consumable live `Run`
  objects without violating M1's rule. It exposes results partitioned by ID
  only after their terminals, while durable attachments remain the independent
  observation path.
- **Browser CI represents, but does not perfectly reproduce, Safari releases.**
  Playwright WebKit is the automatable compatibility signal, not an assertion
  that Linux WebKit equals shipping macOS Safari byte-for-byte. The latest-two
  support promise may require release-candidate manual checks when a browser
  engine makes a breaking change.
- **Browser binaries and macOS minutes add supply and time cost.** Actions and
  Playwright versions are pinned, browser cache keys include the lockfile, jobs
  are time-bounded, and only Chromium runs the full flow. A flaky network install
  is infrastructure failure; retries are not added around product tests.
- **Package authority cannot be proven end-to-end before the real tag.** Manual
  dispatch proves everything through the exact tarball and is structurally
  publish-disabled. GitHub organization/repository package settings and the
  release environment remain human inspections; a staging token would defeat
  the chosen security model and is not introduced.
- **Tag isolation is a push-trigger property, not a manual-dispatch one.** The
  root release workflow's `workflow_dispatch` takes a *required free-text* `tag`
  input (`Existing v* tag to (re)publish`) that it never validates against
  `v*`. An `sdk/typescript/v0.0.1` push therefore cannot fire the root image
  release — which is exactly what AC11.6 proves — but a maintainer could still
  hand that tag to a manual root dispatch. M4 does not edit the root workflow
  (out of the client-side scope), so this stays a release-checklist note and the
  AC12.5 inspection rather than an unstated assumption.

- **The interim and permanent registries have different deletion semantics.**
  GitHub Packages versions are deletable, so `0.0.x` experimentation stays
  there. npmjs's 72-hour unpublish rule does not permit version reuse, so the
  deferred `0.1.0` cutover is treated as permanent. Scenario 12 still checks
  commit, version, inventory, attestation, and root-workflow isolation before
  and after the interim tag.
- **The “latest two stable browsers” policy moves over time.** The pinned
  Playwright revision makes CI reproducible but inevitably lags some release
  windows. Dependency update PRs carry the ordinary compatibility refresh; the
  `v0.0.1` release records the exact tested revisions.
- **The plan-approval public-API decision is closed.** The three audited
  options are resolved in ADR 0304: stream the existing shape; do not add a
  server pre-PR; do not defer the ergonomic method.
- **The licence release blocker is external and cannot be closed by this plan.**
  The protos and their generated TypeScript carry
  `LicenseRef-Stacklok-Proprietary` while the package declares `Apache-2.0`;
  AC11.8 fails closed until an authorized relicense lands. The repository may
  remain internal for the GitHub Packages release; its package settings are a
  separate human precondition in AC12.2.
- **The browser suite runs under `@playwright/test`.** Its worker-scoped
  fixtures, per-project browsers, failure-only traces, and `retries: 0` map
  directly onto AC9.4. Scenario 8/9 proofs therefore use the strict sibling
  `playwright:` resolver rather than being mislabeled as Vitest proofs.
- **Proto breaking-change detection does not exist yet.** `buf.yaml` declares a
  `breaking` stanza but no workflow invokes `buf breaking --against`. AC7.4 now
  claims only generation freshness. Deciding whether npmjs `v0.1.0`'s "real
  compatibility line" requires the gate — and adding it — belongs to the
  cutover checklist.
- **`LocalSessionContextService` is published in `./gen` today.** It is
  generated, re-exported from `src/gen/index.ts`, and packed. AC1.1 pins it as
  an excluded literal so the gate cannot silently shrink, but whether an
  operator-enabled, local-trust-scoped service belongs in a public `./gen` at
  all should be decided before npmjs `v0.1.0` freezes that surface.
- **The npmjs trusted-publisher workflow filename will be load-bearing.** At
  cutover, npm binds the record to the exact filename, so the #821 follow-up
  checklist must configure and preserve `release-sdk-typescript.yml`.
- **Prerelease dist-tag policy is deferred.** AC11.2's strict `vX.Y.Z` grammar
  keeps the implicit `latest` correct; the first `-rc` tag needs a decision
  rather than an accident.
- **Exact runtime dependency pins are deliberate.** `@bufbuild/protobuf 2.14.0`
  and siblings are pinned exactly in a *library*'s `dependencies`, which forces
  consumer-tree duplication and blocks upstream patch uptake without an SDK
  release. `package.test.ts` asserts them exactly. Recorded as a decision, not
  changed here.
- **Shipped sourcemaps reference unpublished sources.** `tsconfig.build.json`
  sets `sourceMap`/`declarationMap` but `files` is `["dist","LICENSE"]`, so
  every map resolves to an absent `../src/*.ts`. Either set `inlineSources` or
  drop maps from the package before publishing.
- **Root-workflow dispatch isolation is a pre-existing gap.** Root
  `release.yml` takes a free-text `tag` input and checks out `ref: ${{ env.VERSION }}`,
  so a maintainer could dispatch the **root** image release with an SDK tag.
  Scenario 11 does not introduce it but makes it reachable; a `^v` guard step in
  the root workflow is the smallest fix and is outside this client-side scope.
- **Operational handoff questions:** the GitHub Packages settings
  administrator, the named tag cutter, whether a
  `sdk/typescript/**` tag ruleset should restrict tag creators, and whether
  release engineering wants a manual Safari spot-check alongside WebKit. npm
  organization ownership and trusted-publisher administration are explicitly
  deferred to the `v0.1.0` cutover checklist.

## Exit criteria

When every point under *Definition of done* holds across the merged stack and
the human-gated Scenario 12 inspection records the authenticated GitHub
Packages artifact, this plan is satisfied. [#821](https://github.com/stacklok/mecatl/issues/821)
remains open to track the npmjs `v0.1.0` cutover checklist.
