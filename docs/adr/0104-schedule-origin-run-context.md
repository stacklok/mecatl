# ADR 0104 — Attribute schedule origins through the run context

- Status: Accepted
- Date: 2026-08-12
- Scope: Agent run context and in-chat schedule origin attribution
- Supersedes: [ADR 0075](./0075-fire-result-delivery.md) — **the wrapper-wide origin-binding mechanism only.** The `OriginSessionID` contract, the durable delivery queue, the turn-boundary drain, and the fenced-untrusted note rendering all stand.
- Superseded by: none

## Context

Fire-result delivery needs every in-chat schedule to retain the session that created it.
ADR 0075 implemented this with mutable state on a shared schedule-manager wrapper: each
run start atomically replaced one process-wide current session id, and a later schedule
create read that value. A shared `Engine` is safe for concurrent use, however, so two
runs can interleave after binding and before tool execution. Atomic access prevents a
data race but does not preserve attribution: one session can stamp another session's id.
Mutate-serial dispatch applies within a run, not across concurrent runs.

The tool execution path already carries the run's context. Attribution belongs to that
request-scoped path rather than to shared wrapper state, and both normal runs and
awaiting-approval resumes pass through the same `startRun` seam.

## Decision

Bind the executing session id onto the cancellation-derived context in `startRun` with
the unexported `withSessionOrigin`. `ScheduleTool.create` reads it there and sets
`ScheduleSpec.OriginSessionID` in the spec literal it already builds. That literal is
the only place the field is ever assigned, and the tool's argument schema has no origin
field, so neither model-derived input nor a stale binding can supply one. An unbound
context yields the empty id, which means no delivery rather than delivery to an
arbitrary session.

Stamp at the construction site, not in a decorator. Remove `OriginBinder`,
`Deps.OriginBinder`, and the whole `SessionOriginScheduleManager` wrapper, and add no
exported replacement. Once the binder state is gone the wrapper holds nothing: it is a
single field assignment behind ten delegating methods, and — this is the reason to
delete rather than keep it — omitting it fails **silently**, because an empty origin is
also the legitimate originless posture, so miswiring is indistinguishable from correct
wiring. Moving the assignment into the tool makes it unforgettable, and composition
passes the `port.ScheduleManager` straight through.

Running under `Engine.Run` is the only way to acquire an origin; an out-of-band create
is originless by design (ADR 0075 decision #1) and goes to the manager directly, which
is what the in-repo REST/gRPC surface already does. An exported context constructor
would therefore serve no composition this harness performs, while letting an embedder
name any session as the origin — `validateScheduleOrigin` checks only that the session
exists, not that the caller owns it.

## Consequences

Concurrent sessions sharing one engine cannot cross-stamp schedule origins, and normal
runs and resumed approvals inherit identical attribution without composition-time
binder wiring. Context propagation is now part of the schedule-origin contract, and it
is engine-internal: an embedder acquires an origin by running under `Engine.Run` and
has no other way to name one. Out-of-band schedule creation remains originless through
an unbound context.

The exported engine API changes before v1: the binder surfaces and the wrapper type are
removed and nothing is added, so the surface net shrinks by fourteen symbols. Should a
real embedder need to attribute a schedule created outside `Engine.Run`, exporting
`withSessionOrigin` at that point is an Added (minor) change; removing an exported one
later would be breaking, so the narrower surface is the reversible direction. If that
export ever happens, `validateScheduleOrigin` must first gain an ownership check — that
the caller's principal matches the origin session's owner — because today the only thing
preventing an embedder from naming an arbitrary session (and inheriting its owner via
`captureScheduleOwner`, ADR 0100 decision 6) is that no exported writer exists.

The origin's context key stays in `engine/agent` rather than beside `session.Principal`
in the domain leaf. The principal has writers outside the engine core, which forces an
exported writer; this value has one writer and one reader, both in `engine/agent`, and
keeping the writer unexported is the argument above. The two are deliberately not
unified.

## See also

- [Architecture guide](../architecture.md)
- [ADR 0075 — Fire-result delivery](./0075-fire-result-delivery.md)
- [ADR 0100 — Caller-identity threading](./0100-caller-identity-threading.md)
- [ADR 0002 — Documentation lifecycle](./0002-documentation-lifecycle.md)
