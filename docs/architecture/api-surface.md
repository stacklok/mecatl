# API surface

> Part of the [Mecatl architecture guide](../architecture.md).

Clients reach the agent loop through wire surfaces that all drive one surface-agnostic
`server.Service` (`internal/adapter/server`). This chapter covers how they share it, how
contracts are generated and versioned, what the event streams promise, and the session
commands, scheduler, and clients built on top. For RPCs and routes, see the
[gRPC](../../user-docs/reference/grpc-api.md) and
[HTTP/SSE](../../user-docs/reference/http-sse-api.md) references.

## One service, several wires

| Surface | Transport | Adapter |
| --- | --- | --- |
| gRPC `HarnessService`, `ScheduleService` | HTTP/2, TCP or Unix socket | `grpc.go`, `grpc_schedule.go` |
| HTTP/SSE | JSON request/response plus `text/event-stream` | `http.go` |
| ACP (Agent Client Protocol) | JSON-RPC 2.0 over stdio, started by `mecated acp` | `internal/adapter/acp` |

`Service` owns everything that must not drift between wires: ownership checks, placement
binding, the registry of in-flight `*agent.Run` values, durable event recording, and the
mapping from domain errors to wire codes (`errorcodes.go`). Adapters only decode
requests, call `Service`, and encode results. A rule added in one adapter but not the
others is a bug; the transport-parity tests in the package exist to catch it.

The HTTP surface is a hand-written mirror, not a generated gateway, because the
bidirectional `Converse` stream has no direct REST shape. A prompt opens one SSE response
for the run, and controls (resolve an ask, cancel, steer) are separate unary requests
that name the exact run id they target. A control aimed at a run that has already ended
fails as stale instead of landing on its successor.

ACP lets an editor such as Zed spawn Mecatl as a subprocess. The adapter speaks its own
JSON, never `contracts/gen`, and is both a JSON-RPC server (`session/new`,
`session/prompt`, and so on) and a client: it asks the editor to resolve permission asks
with `session/request_permission`. When the editor advertises file capabilities, reads
and writes go through the editor's buffers. The editor spawning Mecatl over stdio does
not conflict with the no-stdio MCP rule, which is about Mecatl spawning processes.

## Contracts and generation

The public wire contracts live in `contracts/proto/mecatl/v1/` (`harness.proto`,
`schedule.proto`, and `local_session_context.proto`, a privileged service registered only
on local-client-trusted listeners). Two sibling trees serve operators rather than
clients: `driver/v1` is the protocol remote stores implement, and `execution/v1` is the
execution-provider protocol.

`task generate` runs `buf generate` twice: once into `contracts/gen/go` for the server
and drivers, once into `sdk/typescript/src/gen` for the TypeScript SDK. CI regenerates
and fails on any diff, so a proto change and its generated code always land together.
Required-field annotations (`buf.validate`) document intent; the Go server enforces them.

Compatibility is negotiated, not inferred from versions, because Mecatl ships from
`main` as often as from tags. A client's first call, `GetCompatibilityInfo`, returns an
API major (`features.go`) that changes only on a genuine break, plus capabilities and
feature identifiers that announce every additive change. Feature identifiers, event
types, stop reasons, and watch phases are open strings, so an older client passes an
unknown value through instead of failing.

## Runs and the live stream

A run's domain `session.Event` values become one proto `Event` through the pure `toProto`
mapper (`mapper.go`), on gRPC and HTTP alike. The live relays (`Converse` and the prompt
SSE stream) skip three log-only kinds, `approval`, `compaction_archive`, and
`user_prompt`, because the client already holds its own verdicts and prompts.

The relay, not the engine, appends every event to the durable event log, whether or not
the client is still connected, so the log records a run's tail after its client
disappears ([observability](observability.md) covers the log).

When a client disconnects, the relay cancels the run but keeps draining and discarding
its events, and `Run.Cancel` arms a short grace after which every guarded send in the
loop gives up. A busy run therefore never blocks behind a dead consumer, while a slow one
still receives the terminal result. Cancel leaves a run durably parked on an
authorization handoff untouched, so its resume point survives.

## Durable watch

- `StreamSessionEvents` replays the whole durable log once and stops.
- `StreamSessionLive` is a live, process-local subscription. Slow subscribers lose
  events, and another replica sees nothing.
- `WatchSessionEvents` is the durable replay-then-follow stream that reconnecting and
  multi-replica clients use (`watch.go`).

A watch replays the log from an opaque cursor (empty means the start), sends one
phase-only `live` frame at the replay-to-live boundary, then follows new appends. The
boundary frame lets a client switch to a live view on an idle session without waiting
for an event that may never come. A cursor from a recreated log or one that does not
decode is rejected, never coerced to a nearby position, because resuming from almost the
right place silently loses data.

A `gap` frame marks a position where a durable append is known to have failed. It is a
delivery fact, not a run event, so it never enters `session.Event`, and it is delivered
even when the watch is filtered to one run.

A watch never applies backpressure to a run. A consumer that overflows its bounded buffer
past a short grace is terminated with `watch_lagging` rather than losing events, and
resumes losslessly from its last cursor. A server whose log cannot position by cursor
refuses the watch instead of degrading to a full replay.

## Session commands and successors

Discovery is scoped to a source session. `ListCommands` and `ListWorktrees` first
authorize the owner and exactly reattach that session's placement
(`placement_discovery.go`), so results reflect where the session actually runs. A no-FS
session has no worktrees.

A worktree entry carries display metadata and an opaque selector: an HMAC over caller,
source session, and choice under one random per-process key (`worktree_selector.go`).
Nothing is stored; matching recomputes selectors over the current inventory in constant
time. A selector is useless to another caller or session, and a restart invalidates it.

`ClearSession` and `ForkSession` (`placement_successor.go`) create a new session and keep
the source as a stored conversation. Clear starts with empty history; Fork copies it,
stripping provider-specific replay state when the fork changes provider. Both inherit the
source's exact placement unless given a fresh selector, and each successor gets its own
MCP broker attachment. Creation holds the source's mutation lease, and a failure leaves
the source untouched, with one exception: clearing a running source cancels it first, so
a later failure publishes no successor but the source stays cancelled.

`SetMode` is rejected mid-turn, because changing posture would race permission checks
already in flight; clients defer it to the next prompt. `CompactSession` likewise runs
only at an idle or terminal boundary.

## Scheduled tasks

A schedule is a saved prompt that runs on a cron expression or once, unattended and at
most once per due slot across replicas. It lives in the composition layer and reuses normal run
entry; `engine/agent` knows nothing about it.

- `port.ScheduleStore` (`engine/port/schedule.go`) is the ground truth. Its `Claim`
  atomically advances the next fire time, which is the at-most-once fence. Store
  adapters (`memschedulestore`, `adapters/jsonlstore`, `adapters/redisstore`, and a
  remote gRPC driver) share the `scheduleconformance` suite.
- The tick loop (`internal/adapter/scheduler`) runs only on the replica holding the
  `__scheduler__` leader lease; standby replicas take over when it lapses. Each tick
  finds due schedules, applies the misfire policy, then claims, fires, and records each.
  Leadership is hygiene; `Claim` is the correctness guarantee.
- Each fire creates a fresh `sched--` session owned by the schedule's captured owner,
  with bounded limits. A schedule not marked mutating runs in plan mode. The fire
  reattaches the exact placement persisted at creation, and fails before creating a
  session if that placement has drifted (`internal/app/scheduler_fire.go`).

The main failure mode is a crash between `Claim` and `RecordFire`. A recurring schedule
heals on its next due slot; a one-shot is lost unless it opts into bounded retry, which
starts each retry fresh. A fire whose predecessor is still running is skipped. Context
carried from a previous fire enters the prompt as a fenced, untrusted preamble, never as
history, because that fire may have been prompt-injected.

The in-chat `Schedule` tool, `ScheduleService`, and the REST routes all go through
`port.ScheduleManager`, so they share one registry and one validating create path.
`schedule.*` events go to the fire session's durable log. A schedule created in chat
records its origin session from the run context, never from model input; after each
fire, a fenced result note is queued for that origin (`port.DeliveryQueue`) and drained
as a new run or at its next turn boundary. Without a store directory the queue is
in-memory, so notes can be lost on restart. Details are in
[scheduled tasks](../../user-docs/building/what-you-get/scheduled-tasks.md).

## Clients

### TypeScript SDK

`@stacklok-oss/mecatl-sdk` (`sdk/typescript/`) is an ESM package with its own pnpm
lockfile, CI, and `sdk/typescript/v*` release tags; a maintainer approves each staged npm
artifact before it becomes public. Its entry points split by runtime: `.` holds the
transport-neutral core and browser HTTP/SSE client, `./node` and `./deno` add gRPC and
local daemon ownership, and `./gen` exposes the generated types. Every transport feeds
one `Client`/`Session`/`Run` layer: compatibility is checked before ordinary calls, a
`Run` is consumed once, and controls carry the exact run id.

Durable views (`Session.attach`, `Session.activity`) sit on `WatchSessionEvents`. Their
checkpoints wrap the server cursor with the view's run binding and filter, so a run-bound
checkpoint cannot widen to the whole session. On transport failure or lag they reconnect
from the checkpoint with backoff; a gap or an expired cursor surfaces as a typed error,
and the SDK never silently restarts from the beginning. Control acknowledgements are
delivery facts, not idempotency proofs, so an application reconciles ambiguous outcomes
from session state.

`spawn()` starts a private `mecated` (Unix socket on Node and Bun, loopback TCP on Deno)
and returns a client only after the daemon's ready document and a first compatibility
call succeed; a lifetime pipe makes the daemon exit if its parent dies. Spawned Node and
Bun clients can register callback tools, served from a loopback streaming-HTTP MCP host
the session mounts as an ordinary MCP server. Usage is in the
[TypeScript SDK guide](../../user-docs/building/typescript-sdk/index.md).

### Mecatl Studio

Mecatl Studio is the browser UI, a self-contained pnpm workspace in `apps/` with three
packages: `web` (a Vite and React single-page app), `server` (a Hono backend for
frontend, or BFF), and `contracts` (Zod schemas, OpenAPI, and the generated client,
committed and drift-gated).

The browser calls only the BFF's `/api/v1` product API and imports neither the SDK nor
daemon types. The BFF holds the user's credential, serves app and API from one origin,
and is the only part that talks to Mecatl. It depends on a released SDK version from
npm, never the in-repo source, so a UI change that needs an unreleased SDK change waits.

The BFF resolves exactly one runtime mode at startup: `external` (an existing gRPC
listener), `spawn` (a local `mecated`), or `mock`. Login uses the issuer named in the
target's protected-resource document, and session state lives in sealed cookies, so
replicas scale horizontally on one shared secret. The container image refuses `spawn`
and `mock` and requires an opt-in to run unauthenticated. Feature routes are gated on
the daemon's advertised capabilities. Studio is early access; see
[`apps/README.md`](../../apps/README.md) and the
[Studio deployment guide](../../user-docs/building/deployment/studio.md).

## Engine as a library

A Go program can embed the `engine/` module instead of calling a server. Its stable
surface is the exported identifiers of the core packages listed in `arch.CorePackages`
(`engine/arch/surface.go`), snapshotted in `engine/api/*.txt` and checked by
`task api:check`. The rules are in [`engine/COMPATIBILITY.md`](../../engine/COMPATIBILITY.md)
and [API stability](../../user-docs/building/api-stability.md).

## Related

- [The agent loop](agent-loop.md)
- [Observability](observability.md)
- [Deployment and hardening](deployment-and-hardening.md)
- [Governance](governance.md)
- [Drive Mecatl over gRPC or HTTP](../../user-docs/building/deployment/grpc-http.md)
