# ADR 0304 — TypeScript SDK public surface completeness and v0.1.0 release

- Status: Accepted
- Date: 2026-09-07
- Scope: `sdk/typescript/`, its browser and platform verification, public documentation, and the npm release path for `@stacklok/mecatl-sdk`. Client-side by default: no contract change and no production server change.
- Supersedes: none. Completes the M4 surface deferred by [ADR 0279](./0279-typescript-sdk-architecture.md), [ADR 0288](./0288-typescript-sdk-durable-attachment.md), and [ADR 0292](./0292-typescript-sdk-local-daemon-and-tools.md).

## Context

M1–M3 of [#821](https://github.com/stacklok/mecatl/issues/821) are on
`main`. They established the in-repository `@stacklok/mecatl-sdk` package,
generated protobuf surface, HTTP/SSE and gRPC transports, ergonomic sessions and
runs, durable attachments, local daemon lifecycle, `query()`, and callback tools.
M4 is the last milestone before the first npm publication, `v0.1.0`.

The shipped ergonomic layer covers only the session/run/watch/tool subset. The
wire surface is larger: `HarnessService` currently declares 67 RPCs and
`ScheduleService` declares 10. Every operation is public server surface because
its application boundary is classified in
[`internal/adapter/server/classification.go`](../../internal/adapter/server/classification.go),
but the two SDK raw transports do not yet make all 77 operations reachable.
[`internal/adapter/server/http.go`](../../internal/adapter/server/http.go) has an
HTTP route for each current RPC except `StreamSessionLive`, whose live-only
semantics remain gRPC-only. HTTP also has control routes that do not correspond
one-for-one to descriptors. Counting hand-written SDK methods, or treating one
of those HTTP-only controls as coverage for an unrelated descriptor, is
therefore not a usable completeness gate.

Generated descriptors and server classification answer different questions.
The descriptors are the mechanically enumerable wire-operation inventory.
`serviceAccessTable` is the existing source of truth for whether the backing
exported `*server.Service` boundary is a reviewed public application boundary.
It is intentionally broader than these services, so parity needs an explicit
descriptor-to-Service-boundary mapping rather than a false equality between all
table rows and all RPC names.

Plan approval is the other load-bearing audit result. `ApprovePlan` is
server-streaming; there is no acknowledgement-only form. It resumes the parked
run and, on approval, streams a continuation run with a different run ID. The
only advertised features are `server_info`, `watch_session_events`, and
`mcp_servers_on_create`; the `approve_ack_only` and `prompt_free_controls`
features anticipated by [ADR 0288](./0288-typescript-sdk-durable-attachment.md)
do not exist. That is not a server gap for `session.resolvePlan()`: the
`PlanResolution` type in #821 exists precisely to model this two-run stream. It
does mean `query()` cannot invoke `ApprovePlan` while its original `Run` remains
registered, because the server correctly rejects a second live driver for the
session.

Teams have a similar raw-versus-ergonomic gap. `CreateTeam`, `SpawnTeammate`,
`SendTeammateMessage`, `CancelTeammate`, `RunTeam`, `ListTeam`, and
`CleanupTeam` exist over both transports, and M1 already decodes team events,
but callers have no `Team` handle that composes those operations.

The package still reports version `0.0.0`, declares Node `>=24` despite #821's
Node 22+ contract, has no `publishConfig`, no browser harness, no macOS SDK CI,
and no SDK release workflow. The root release workflow listens for root `v*`
tags. Like the provider-module discipline in
[ADR 0093](./0093-provider-modules.md), the SDK needs a path-qualified tag that
cannot release a root image. This must be mechanically proved rather than
trusted as a glob-reading convention.

## Decision

**1. One reviewed RPC catalog is the completeness authority for both raw
transports.** The SDK carries one typed catalog whose key set is exactly the 67
`HarnessService` plus 10 `ScheduleService` generated descriptors. Each row
names the generated service and method, its request/response types, unary,
server-streaming, or bidirectional-streaming shape, its backing
`*server.Service` boundary, and an explicit classification for both raw
transports.

The gRPC classification always dispatches through the generated descriptor.
The HTTP classification supplies method, path template, path/query/body
mapping, response decoder, and whether the response is JSON or SSE. An RPC may
instead carry a reviewed `grpc-only` classification with a rationale. The exact
allowed set is pinned and initially contains only `StreamSessionLive`. It is
not legal to label an operation generically `unsupported`: adding a gRPC-only
exception changes this decision and its exact-set test. `Converse` is recorded
as bidirectional gRPC plus the explicit HTTP prompt/control route family; it is
not falsely described as one HTTP bidi stream.

HTTP-only control operations are kept in a separate catalog. They remain
callable where already supported but cannot satisfy descriptor parity. This
keeps attached HTTP cancel and strict HTTP steer honest without making the RPC
gate gameable.

A root-module Go parity test enumerates the generated Go service descriptors,
projects their explicitly mapped backing methods through `serviceAccessTable`,
and compares that exact reviewed public RPC projection with the TypeScript
catalog. It fails for a missing or stale SDK row, an unclassified backing
boundary, a missing transport classification, an altered streaming shape, or
growth of the gRPC-only exception set. A new public server RPC therefore fails
CI until the SDK deliberately says how each transport reaches it.

**2. Non-session RPCs receive thin typed namespaces, not 60 separate API
designs.** The client exposes named namespaces for MCP inventory, agents,
commands/worktrees, models, skills and learned-skill operations, learning and
reflection, schedules and fires, dream plans, session adoption/inventory, and
storage migration/cleanup operations. Their one shape rule is: a method has a
discoverable camel-case name, takes the generated request type (or a narrow
identifier convenience that constructs it without changing meaning), returns
the generated response type or generated stream item, accepts the common
request options, and delegates to the raw catalog. It does not invent a second
domain model, retry policy, pagination abstraction, or lifecycle object.

The generated `./gen` entrypoint remains the escape hatch and is governed by
protobuf breaking/freshness checks. The named namespaces improve discovery;
they do not hide any raw method.

**3. Teams alone receive an ergonomic resource handle.**
`client.teams.create()` returns a `Team`. The handle owns only a client
reference and team ID and provides `spawn()`, `message()`, `cancel()`, `run()`,
`list()`, and `cleanup()`. `run()` is a single-consumption async stream of M1's
existing discriminated `TeamEvent` union. It must observe exactly one terminal
`outcome` frame; EOF before an outcome or a second outcome is a protocol error,
while a valid terminal outcome closes normally.

`maxTeamTokens` maps to `max_team_tokens` and is tighten-only: omission leaves
the daemon's budget unchanged, and a client value cannot be presented as an
increase or override of the server cap. Member session IDs keep the server
format `team-<id>-<member>`. They remain child IDs under [ADR 0288](./0288-typescript-sdk-durable-attachment.md):
M2 `session.attach()` refuses them rather than pretending a child stream is an
ordinary root run. Team observation happens through `Team.run()` and
`Team.list()`.

**4. `session.resolvePlan()` consumes the existing two-run stream as a
`PlanResolution`.** It is available only for a durably parked plan with no
locally live `Run`. Its result follows the same one-consumption rule as `Run`:
iterate events or call `result()`, never both. Events are partitioned by their
server run ID into the resumed run and, only after an approving resolution, a
continuation run with a new ID. `result()` returns the resumed `RunResult` and
an optional continuation `RunResult`; a denial has no continuation. A missing
run ID, a third run ID, a continuation before the resumed terminal, or EOF
without the required terminal is a protocol error.

Attachments remain run-bound. An attachment to the resumed run terminates with
that run and never silently crosses to the continuation; `session.activity()`
is the cross-run view. Applications that need durable observation of both run
IDs attach to each explicitly after the resolution exposes them.

`query({ mode: "plan" })` now requires `onPlanApproval` before it creates any
resource, lifting [ADR 0292](./0292-typescript-sdk-local-daemon-and-tools.md)'s
temporary refusal. `query()` already owns the original live run, so it does not
call `ApprovePlan` against that live registration. It feeds the callback's
verdict through the live Converse approval frame, drains that same-ID plan run
to its `plan_approved` or denied terminal, then, on allow, starts a new run with
the same fixed proceed message the server's parked-session path uses. A
root-module parity test prevents the SDK copy of that message from drifting
from `agent.PlanApprovedProceedText`. The query flattens both runs into its
one-shot result. This is the one deliberate internal reuse of the permission
frame: the public callback, ask classification, target-mode verdict, and error
surface remain plan-specific and are never exposed as `onPermissionAsk` or
`resolveAsk`.

We considered the three honest alternatives required by the audit: expose the
server stream as-is, add an acknowledgement-only server pre-PR, or defer the
ergonomic method and leave only the raw RPC. We choose the first for
`resolvePlan()` because it is the shape #821 designed `PlanResolution` to
represent. No server pre-PR is justified. Deferral would leave the documented
v0.1 plan API knowingly incomplete, while an ack-only operation would discard
the two run streams callers need.

**5. Three separate compatibility gates own hand-written, generated, and
declaration surface.** API Extractor reports for `.` and `./node` continue to
gate intentional ergonomic API changes. Protobuf generation freshness and
breaking checks govern `./gen`; generated declarations are not copied into the
API Extractor reports.

TypeScript 6 remains the development compiler. A devDependency alias pins the
latest accepted TypeScript 5.7 patch, and CI invokes that binary in a second
declaration-consumer project against the built package. The ordinary TypeScript
6 typecheck runs separately. The Node CI matrix is Node 22 and the repository's
current Node line (24 today), with Bun 1.4.1 retained. `engines.node` becomes
`>=22` in the release work.

**6. Browser and macOS claims get real-platform tests with bounded jobs.** The
browser harness lives at `sdk/typescript/e2e/browser/`. Its Node controller
starts a same-checkout `mecated --mock-script` on loopback HTTP with one exact
test origin in `--cors-origins`, waits for readiness, and gives Playwright only
that origin and base URL. No browser test reaches an external service.

Chromium runs the full public flow: package import, exact-origin CORS preflight,
connect, session create, multimodal run, permission callback, cancellation,
strict steer when the daemon advertises it, and durable attach. Firefox and
WebKit each run an import/connect/run/attach smoke. The three projects reuse a
worker-scoped daemon fixture, have per-test and per-job timeouts, retain traces
only on failure, and get no blanket retry that could hide a deterministic SDK
fault. Chromium is the only full-flow project to bound time and flake; the
other engines verify the transport and runtime assumptions.

A separate `macos-14` CI job builds the same-checkout `mecated` and runs one
Node 22 `spawn()`/UDS/readiness/clean-exit smoke. It neither repeats the full
Linux suite nor depends on npm publication. Browser support remains the latest
two stable releases promised by ADR 0279; the pinned Playwright revision is the
CI representative, with engine-version lag recorded as a release risk rather
than hidden behind an untestable claim.

Production browser authentication guidance remains a same-origin BFF that
injects credentials and enforces Origin/CSRF policy. M4 adds documentation and
an example shape, not a BFF library or service.

**7. Examples and user documentation are executable release surface.** Concise
examples cover Node/Bun connect, local spawn/query, callback tools, browser+BFF,
permissions, durable attachment, teams, and schedules. They live under
`sdk/typescript/examples/`, import only published entrypoints, and are compiled
by a dedicated no-emit examples project in CI so package exports and examples
cannot drift.

M4 adds the deferred SDK pages under `user-docs/`, and updates
`docs/architecture.md`, `docs/design/IMPLEMENTATION-NOTES.md`, and the relevant
cloud/resource inventories. `task docs` and `task site:build` are release gates.
The `website/` application remains npm-managed and is not added to the pnpm
workspace.

**8. The SDK publishes from a dedicated path-qualified, SHA-pinned workflow.**
`.github/workflows/release-sdk-typescript.yml` listens only for
`sdk/typescript/vX.Y.Z` tags. It follows [ADR 0093](./0093-provider-modules.md)'s
path-qualified tag discipline and mirrors the root workflow's pinned-action,
least-privilege, immutable-checkout, bounded-job, and provenance conventions.

On a tag push it checks out that exact tag, verifies the tag version equals
`sdk/typescript/package.json`, performs a frozen pnpm install, runs
`task generate` and rejects any generated-tree or lockfile diff, then runs SDK
lint, typecheck matrices, tests, build, API reports, pack, and the exact packed
tarball inventory oracle. It publishes that inspected tarball with public
access and npm provenance. The job has `id-token: write` and no `NPM_TOKEN`,
registry password, or long-lived publish credential. `publishConfig.access` is
`public`.

`workflow_dispatch` executes the same checkout-through-pack verification as a
dry run and is structurally unable to reach the publish step. This exercises
the workflow before the first real tag without weakening tag authority. A
root-module test inspects both release workflows and proves that
`sdk/typescript/v0.1.0` selects the SDK workflow but cannot select the root
image workflow's `v*` trigger.

The release PR changes `package.json` from `0.0.0` to `0.1.0`; the tag never
mutates source. A human cuts `sdk/typescript/v0.1.0` only after the release PR
is reviewed and merged.

**9. Publication has three explicit human prerequisites.** The acceptance plan
cannot create or infer them:

1. Confirm ownership of the npm `@stacklok` organization and package publish
   rights for the releasing maintainers.
2. Configure npm trusted publishing for repository `stacklok/mecatl` and the
   exact `.github/workflows/release-sdk-typescript.yml` workflow filename.
3. Name the release maintainer who will create and push
   `sdk/typescript/v0.1.0` after the release commit is on `main`.

The tag scenario is human-gated until all three are checked.

**10. M4 closes documentation state without rewriting frozen decisions.** ADR
0292 moves from Proposed to Accepted because its M3 implementation is on
`main`; this ADR is Accepted when this design and plan land. Living docs use
`@stacklok/mecatl-sdk`, and describe `PlanResolution` as the two-run streaming
shape above. They remove ADR 0292's temporary statement that v0.1 has no plan
mode without editing the historical decision text itself.

**11. The v0.1 non-goals remain explicit.** M4 does not add binary download,
Windows local spawn, remote callback-tool hosting, dynamic tool registration,
React bindings, a Python SDK, Deno/edge support, Studio migration, or stdio MCP.
The downstream fake-transport testkit [#872](https://github.com/stacklok/mecatl/issues/872),
HTTP steer completion [#873](https://github.com/stacklok/mecatl/issues/873),
Redis follow-pool work [#876](https://github.com/stacklok/mecatl/issues/876),
and Slack bot reference [#881](https://github.com/stacklok/mecatl/issues/881)
remain separate and do not block v0.1.

## Consequences

- A new HarnessService or ScheduleService RPC cannot land unnoticed by the
  SDK. The cost is an intentionally hand-maintained transport catalog and a
  mapping to the server's classified application boundary.
- The raw surface becomes complete without turning every server noun into a
  bespoke object model. Teams and plan resolution receive ergonomics because
  their streaming lifecycle needs it; the rest stay thin.
- `PlanResolution` preserves the real server choreography. Consumers must
  handle two run IDs, and an attachment remains intentionally narrower than a
  whole plan resolution.
- Browser verification adds the largest CI-time and flake surface in the SDK.
  A shared daemon, one full browser project, two smoke projects, strict
  timeouts, failure-only traces, and no generic retry keep that cost bounded.
- Trusted publishing eliminates a repository npm secret, but the first release
  depends on npm-side organization and publisher configuration that CI cannot
  bootstrap or fully validate before a real publication.
- Path-qualified tags keep SDK and image release authority separate. The
  separation is protected by a test over both workflow triggers, not folklore.
- `v0.1.0` is a real compatibility line: API Extractor, generated-protobuf, TS
  5.7/6, Node 22/current, browser, platform, documentation, and tarball gates
  all become release obligations.

## See also

- [TypeScript SDK release acceptance plan](../acceptance/sdk-typescript-release.md)
- [ADR 0279 — TypeScript SDK architecture](./0279-typescript-sdk-architecture.md)
- [ADR 0288 — TypeScript SDK durable attachment](./0288-typescript-sdk-durable-attachment.md)
- [ADR 0292 — TypeScript SDK local daemon and callback tools](./0292-typescript-sdk-local-daemon-and-tools.md)
- [ADR 0093 — Provider modules and path-qualified tags](./0093-provider-modules.md)
- [Architecture guide](../architecture.md)
- [Implementation notes](../design/IMPLEMENTATION-NOTES.md)
