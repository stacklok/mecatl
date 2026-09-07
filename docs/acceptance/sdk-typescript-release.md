# TypeScript SDK public surface and v0.1.0 release (M4) — acceptance plan

**Phase:** capability — `@stacklok/mecatl-sdk` M4: complete public surface, platform contract, documentation, and first npm release
**Status:** draft
**Issue:** [stacklok/mecatl#821](https://github.com/stacklok/mecatl/issues/821) (parent: [#761](https://github.com/stacklok/mecatl/issues/761)).
**ADR:** [ADR-0304](../adr/0304-typescript-sdk-public-surface-and-release.md) — descriptor-to-transport parity, the one thin-namespace rule, ergonomic teams, streaming `PlanResolution`, the compatibility and browser matrices, executable examples, path-qualified trusted publishing, and v0.1.0's human gate.
**Delivery shape:** a **linear stack**, one PR per scenario — `sdk/41-rpc-catalog` is the stack root off `main`; subsequent layers are `sdk/42-http-routes`, `sdk/43-namespaces-core`, `sdk/44-namespaces-ops`, `sdk/45-teams`, `sdk/46-plan-resolution`, `sdk/47-compat-matrix`, `sdk/48-browser-chromium`, `sdk/49-browser-platforms`, `sdk/50-docs-examples`, `sdk/51-release-workflow`, and `sdk/52-publish-v0.1.0`. This is **not** an accumulator: each PR targets its predecessor and is reviewed and merged on its own, exactly as M3's `sdk/31`…`sdk/40` stack was.

The smallest complete stack that makes every public HarnessService and
ScheduleService operation reachable, adds ergonomics only where a lifecycle
requires them, proves the supported runtime and browser claims, documents the
package, and publishes the reviewed artifact as `v0.1.0`. It is the final
milestone of [#821](https://github.com/stacklok/mecatl/issues/821), not a new
application protocol or an opportunity to redesign the Go server.

The doc is organized scenario-first because acceptance is about what the SDK
against the running harness can demonstrate, not which TypeScript files exist.

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
- **Trusted publishing is exercised before it is trusted.** A manual dry run
  reaches pack and exact inventory inspection but has no publish authority.
  Only the path-qualified tag reaches `npm publish`; the npm-side trust setup is
  a named human prerequisite, not a secret added to GitHub.
- **Verify names follow M1–M3's convention.** Go proofs are
  `TestSDKTypescriptRelease_ScenarioN_*`; TypeScript proofs use the strict
  `vitest:<path>#<base64url-title>` resolver form so a renamed title cannot
  degrade to file-level coverage.

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
  duplicate, or stale row, and every row classifies both raw transports.
  - verify: TestSDKTypescriptRelease_Scenario1_RPCTransportCatalogParity
- AC1.2: The reviewed gRPC-only set is exactly `{StreamSessionLive}`; changing
  that set or adding a generic `unsupported` classification fails the guard.
  - verify: TestADR_0304_ExactGRPCOnlySet
- AC1.3: HTTP-only controls are callable from their separate inventory but do
  not satisfy, shadow, or replace any RPC catalog entry.
  - verify: vitest:sdk/typescript/test/rpc-catalog.test.ts#SFRUUC1vbmx5IGNvbnRyb2xzIGRvIG5vdCBzYXRpc2Z5IFJQQyBjb3ZlcmFnZQ — `sdk/typescript/test/rpc-catalog.test.ts :: "HTTP-only controls do not satisfy RPC coverage"`
- AC1.4: Every descriptor row names a real `*server.Service` method with a valid
  `serviceAccessTable` entry, and every classified RPC-reachable Service method
  is represented by exactly one descriptor row.
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
  public raw seam on gRPC and, except the exact gRPC-only set, HTTP, preserving
  common request options and normalized errors.
  - verify: vitest:sdk/typescript/test/http-routes.test.ts#cmF3IG9wZXJhdGlvbnMgcmVhY2ggZXZlcnkgY2xhc3NpZmllZCBSUEMgb24gYm90aCB0cmFuc3BvcnRz — `sdk/typescript/test/http-routes.test.ts :: "raw operations reach every classified RPC on both transports"`

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
  - verify: vitest:sdk/typescript/test/package.test.ts#dGhlIGNvcmUgbmFtZXNwYWNlIGJhdGNoIGlzIGV4cG9ydGVkIGZyb20gYm90aCBzdXBwb3J0ZWQgZW50cnlwb2ludHM — `sdk/typescript/test/package.test.ts :: "the core namespace batch is exported from both supported entrypoints"`

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
- AC4.4: After this batch, all 77 descriptor operations are callable through
  the public raw seam and every non-team unary operation has a discoverable
  named client/session namespace where #821 calls for one.
  - verify: vitest:sdk/typescript/test/rpc-catalog.test.ts#YWxsIDc3IHJhdyBSUENzIGFyZSBjYWxsYWJsZSB0aHJvdWdoIHRoZSBwdWJsaWMgcmF3IHNlYW0 — `sdk/typescript/test/rpc-catalog.test.ts :: "all 77 raw RPCs are callable through the public raw seam"`

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
[#821](https://github.com/stacklok/mecatl/issues/821) “Events, runs, and controls”).

**Work:** `PlanResolution`, plan-specific callback/verdict types, transport
mapping, single-consumption and run partitioning, attachment composition,
`query()` live-plan composition, and proceed-message parity.

**Acceptance:**
- AC6.1: `session.resolvePlan()` on a durably parked plan yields the resumed
  run's events through its terminal before yielding a continuation with a
  different run ID, and `result()` returns both typed `RunResult`s in order.
  - verify: vitest:sdk/typescript/test/plan-resolution.test.ts#cmVzb2x2ZVBsYW4gc3RyZWFtcyB0aGUgcmVzdW1lZCBydW4gdGhlbiBpdHMgY29udGludWF0aW9u — `sdk/typescript/test/plan-resolution.test.ts :: "resolvePlan streams the resumed run then its continuation"`
- AC6.2: A deny/iterate resolution returns the resumed run's terminal and no
  continuation result; a third run ID, reversed ordering, or missing terminal
  is a protocol error rather than silently flattened.
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
  drains its terminal, starts a new-ID continuation only on allow, and returns
  the continuation's one-shot result.
  - verify: vitest:sdk/typescript/e2e/plan-resolution.e2e.test.ts#cXVlcnkgcGxhbiBtb2RlIHJlcXVpcmVzIG9uUGxhbkFwcHJvdmFsIGFuZCBmbGF0dGVucyBib3RoIHJ1bnM — `sdk/typescript/e2e/plan-resolution.e2e.test.ts :: "query plan mode requires onPlanApproval and flattens both runs"`
- AC6.6: A plan-originated ask invokes only `onPlanApproval`, an ordinary tool
  ask invokes only `onPermissionAsk`, and the SDK's continuation prompt is
  byte-equal to `agent.PlanApprovedProceedText`.
  - verify: TestADR_0304_PlanApprovalContractParity

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
- AC7.3: CI runs the SDK unit/build/API suite on Node 22 and the current Node
  line (24 when this plan lands), retains Bun 1.4.1, and `engines.node` is
  `>=22`.
  - verify: inspection — review `.github/workflows/ci.yml`, `sdk/typescript/package.json`, and both matrix job logs
- AC7.4: API Extractor reports for `.` and `./node` remain reviewed artifacts,
  while `./gen` stays governed by reproducible generation and protobuf breaking
  checks rather than being copied into those reports.
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
  - verify: vitest:sdk/typescript/e2e/browser/chromium.e2e.test.ts#Q2hyb21pdW0gY29tcGxldGVzIHRoZSBicm93c2VyIFNESyBjb250cm9sIGZsb3c — `sdk/typescript/e2e/browser/chromium.e2e.test.ts :: "Chromium completes the browser SDK control flow"`
- AC8.2: The browser's credentialed preflight succeeds only for the one exact
  configured origin, carries the expected allow headers and `Vary: Origin`, and
  a sibling origin is refused.
  - verify: vitest:sdk/typescript/e2e/browser/chromium.e2e.test.ts#dGhlIGJyb3dzZXIgdHJhbnNwb3J0IHBhc3NlcyBleGFjdC1vcmlnaW4gQ09SUyBwcmVmbGlnaHQ — `sdk/typescript/e2e/browser/chromium.e2e.test.ts :: "the browser transport passes exact-origin CORS preflight"`
- AC8.3: Chromium sends an in-memory multimodal prompt, observes an ordinary
  permission ask, resolves it through the callback, and reaches the scripted
  terminal without external network access.
  - verify: vitest:sdk/typescript/e2e/browser/chromium.e2e.test.ts#Q2hyb21pdW0gcnVucyBhIG11bHRpbW9kYWwgcHJvbXB0IGFuZCBwZXJtaXNzaW9uIGNhbGxiYWNr — `sdk/typescript/e2e/browser/chromium.e2e.test.ts :: "Chromium runs a multimodal prompt and permission callback"`
- AC8.4: Chromium cancels an owned run with the expected run ID, exercises
  strict steer when the feature is advertised (and asserts typed unsupported
  otherwise), then attaches and drains a persisted run.
  - verify: vitest:sdk/typescript/e2e/browser/chromium.e2e.test.ts#Q2hyb21pdW0gY2FuY2VscyBzdGVlcnMgd2hlbiBhZHZlcnRpc2VkIGFuZCByZWF0dGFjaGVz — `sdk/typescript/e2e/browser/chromium.e2e.test.ts :: "Chromium cancels steers when advertised and reattaches"`

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
  - verify: vitest:sdk/typescript/e2e/browser/smoke.e2e.test.ts#RmlyZWZveCBpbXBvcnRzIGNvbm5lY3RzIHJ1bnMgYW5kIGF0dGFjaGVz — `sdk/typescript/e2e/browser/smoke.e2e.test.ts :: "Firefox imports connects runs and attaches"`
- AC9.2: WebKit imports `.`, connects over HTTP/SSE, runs to a terminal, and
  attaches to drain that run.
  - verify: vitest:sdk/typescript/e2e/browser/smoke.e2e.test.ts#V2ViS2l0IGltcG9ydHMgY29ubmVjdHMgcnVucyBhbmQgYXR0YWNoZXM — `sdk/typescript/e2e/browser/smoke.e2e.test.ts :: "WebKit imports connects runs and attaches"`
- AC9.3: On `macos-14` with Node 22, `spawn()` starts the same-checkout binary
  over UDS, reaches readiness, performs one run, and exits cleanly with its
  runtime directory removed.
  - verify: vitest:sdk/typescript/e2e/macos-spawn.e2e.test.ts#bWFjT1Mgc3Bhd25zIG92ZXIgVURTIGFuZCBleGl0cyBjbGVhbmxl — `sdk/typescript/e2e/macos-spawn.e2e.test.ts :: "macOS spawns over UDS and exits cleanly"`
- AC9.4: Browser projects reuse one worker-scoped daemon, have explicit test and
  job timeouts, collect traces only on failure, use no blanket retry, and never
  reach an external network.
  - verify: inspection — review Playwright config, fixture lifetime, workflow timeout, retry, trace, and network-denial settings

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

### Scenario 11 — Dry-runnable trusted-publishing workflow and tag isolation

A dedicated SHA-pinned workflow builds and inspects the exact npm artifact;
only a matching path-qualified tag can publish it
([ADR-0304](../adr/0304-typescript-sdk-public-surface-and-release.md) Decision 8;
[ADR-0093](../adr/0093-provider-modules.md)'s tag discipline).

**Work:** SDK release workflow, dry-run dispatch, version/tag validation,
frozen install, generation cleanliness, all SDK gates, exact pack inspection,
OIDC publication, `publishConfig`, and two-workflow trigger parity.

**Acceptance:**
- AC11.1: `workflow_dispatch` runs checkout through exact tarball inspection but
  has no condition/path that can invoke `npm publish`.
  - verify: TestADR_0304_ManualDispatchIsDryRunOnly
- AC11.2: A tag other than `sdk/typescript/v<package-version>` fails before
  install or publication; the release checkout is the exact tag commit.
  - verify: TestSDKTypescriptRelease_Scenario11_TagVersionParity
- AC11.3: The workflow performs a frozen pnpm install, runs `task generate`, and
  rejects a dirty generated tree, SDK lockfile, or package metadata before
  build/pack.
  - verify: TestSDKTypescriptRelease_Scenario11_GenerationCleanlinessGate
- AC11.4: Release runs SDK lint, both declaration compilers, unit/e2e/build/API
  gates, packs once, and applies the existing exact-inventory oracle — `dist`
  and `LICENSE` only — to the same tarball it would publish.
  - verify: vitest:sdk/typescript/test/package.test.ts#cGFja2VkIHRhcmJhbGwgY2FycmllcyBkaXN0IGFuZCBsaWNlbnNlIG9ubHk — `sdk/typescript/test/package.test.ts :: "packed tarball carries dist and license only"`
- AC11.5: A tag push publishes the inspected tarball with public access and npm
  provenance under `id-token: write`; neither workflow nor repository release
  configuration refers to `NPM_TOKEN` or another long-lived npm credential.
  - verify: TestADR_0304_TrustedPublishingOnly
- AC11.6: `sdk/typescript/v0.1.0` selects the SDK release workflow and provably
  does not match the root image workflow's `v*` **push-tag** trigger; the guard
  also asserts that the root workflow's `workflow_dispatch` tag input is
  unvalidated, so push isolation is mechanical while a manual root dispatch of
  an SDK tag is recorded as an operational hazard rather than a glob outcome.
  - verify: TestADR_0304_TagTriggerIsolation

---

### Scenario 12 — Human-gated `v0.1.0` publication

The final scenario is an inspection checklist executed only after Scenarios
1–11 land and npm-side authority exists
([ADR-0304](../adr/0304-typescript-sdk-public-surface-and-release.md) Decisions
8–9; [#821](https://github.com/stacklok/mecatl/issues/821) M4 item 4 and AC13).

**Work:** release PR version bump, human prerequisite confirmation, reviewed tag
creation, workflow observation, npm metadata/provenance inspection, and issue
status reconciliation without closing #821 from an implementation PR.

**Acceptance:**
- AC12.1: The release PR changes package version `0.0.0` to `0.1.0`, sets
  `publishConfig.access` to `public`, updates the lockfile/API reports where
  required, and contains no release tag.
  - verify: inspection — review the release PR diff and its green required checks before merge
- AC12.2: Before tagging, npm `@stacklok` organization ownership/package rights
  are confirmed, the trusted publisher names `stacklok/mecatl` plus the exact
  SDK workflow file, and a named maintainer accepts responsibility for the tag.
  - verify: inspection — release checklist records all three named human prerequisites and the responsible maintainer
- AC12.3: The annotated or lightweight `sdk/typescript/v0.1.0` tag points
  exactly at the reviewed release commit on `main`, and no root `v0.1.0` tag is
  created as part of the SDK release.
  - verify: inspection — compare `git rev-parse sdk/typescript/v0.1.0`, the merged release commit, and the Actions trigger run
- AC12.4: npm reports `@stacklok/mecatl-sdk@0.1.0` as public with GitHub OIDC
  provenance tied to `stacklok/mecatl` and the dedicated SDK workflow.
  - verify: inspection — inspect npm package visibility, version metadata, provenance statement, repository, commit, and workflow identity
- AC12.5: The downloaded published tarball's file inventory and integrity match
  the artifact inspected by the release workflow, and root image release jobs
  did not run for the SDK tag.
  - verify: inspection — compare npm integrity/inventory with the workflow pack log and inspect Actions runs for the tag

---

## Cross-cutting deliverables

- [ADR-0304](../adr/0304-typescript-sdk-public-surface-and-release.md), authored
  with this plan and Accepted when the documentation PR lands; ADR 0292 moves
  from Proposed to Accepted in the same PR.
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
  for complete v0.1 behavior; `task docs` and `task site:build` green.
- `sdk/typescript/package.json` declares Node `>=22`, version `0.1.0` in the
  release PR, and `publishConfig.access: public`; `website/` remains npm-based.
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
- `TestADR_0304_PlanApprovalContractParity`
- `TestSDKTypescriptRelease_Scenario7_CompatibilityGateSeparation`
- `TestADR_0304_ManualDispatchIsDryRunOnly`
- `TestSDKTypescriptRelease_Scenario11_TagVersionParity`
- `TestSDKTypescriptRelease_Scenario11_GenerationCleanlinessGate`
- `TestADR_0304_TrustedPublishingOnly`
- `TestADR_0304_TagTriggerIsolation`

Two naming families are deliberate: `TestADR_0304_*` pins a costly-to-reverse
ADR 0304 decision as a durable invariant (the exact gRPC-only set, the
plan-approval contract, and the three release-authority properties), following
the repository's existing `TestADR_NNNN_*` convention; the
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
6. `go run ./cmd/mecademo` still prints a complete offline session.
7. `contracts/proto/`, production `internal/adapter/server/`, and
   `cmd/mecated/` are byte-unchanged across Scenarios 1–11; only root-module Go
   tests inspect those contracts.
8. The release workflow's manual dry run succeeds before the release PR is
   tagged, and no path from manual dispatch can publish.
9. All three human prerequisites are checked before `sdk/typescript/v0.1.0` is
   created; the published package, provenance, and inventory pass Scenario 12.
10. No test reaches a live model, npm publish endpoint, or external service
    before the human-gated tag scenario.

## Deferred decisions and known risks

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
- **Trusted publishing cannot be proven end-to-end before the real tag.** Manual
  dispatch proves everything through the exact tarball and is structurally
  publish-disabled. npm organization ownership and the trusted-publisher record
  remain human inspections; a staging token would defeat the chosen security
  model and is not introduced.
- **Tag isolation is a push-trigger property, not a manual-dispatch one.** The
  root release workflow's `workflow_dispatch` takes a *required free-text* `tag`
  input (`Existing v* tag to (re)publish`) that it never validates against
  `v*`. An `sdk/typescript/v0.1.0` push therefore cannot fire the root image
  release — which is exactly what AC11.6 proves — but a maintainer could still
  hand that tag to a manual root dispatch. M4 does not edit the root workflow
  (out of the client-side scope), so this stays a release-checklist note and the
  AC12.5 inspection rather than an unstated assumption.

- **The first publish is externally irreversible.** A bad `0.1.0` cannot be
  overwritten on npm. Scenario 12 therefore checks commit, version, inventory,
  provenance, and root-workflow isolation before and after the tag rather than
  treating publication as another automated worker task.
- **The “latest two stable browsers” policy moves over time.** The pinned
  Playwright revision makes CI reproducible but inevitably lags some release
  windows. Dependency update PRs carry the ordinary compatibility refresh; the
  v0.1 release records the exact tested revisions.
- **No open public-API decision remains.** The three audited plan-approval
  options are resolved in ADR 0304: stream the existing shape; do not add a
  server pre-PR; do not defer the ergonomic method. Remaining questions are
  operational release choices batched for the handoff: the npm organization
  owner, exact trusted-publisher administrator, named tag cutter, and whether
  release engineering wants a manual Safari spot-check in addition to WebKit.

## Exit criteria

When every point under *Definition of done* holds across the merged stack and
the human-gated Scenario 12 inspection records the public package, this plan is
satisfied and [#821](https://github.com/stacklok/mecatl/issues/821) can be closed
by the maintainer responsible for the parent issue.
