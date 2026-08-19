---
id: 05-wire-grpc-steer-frame
title: Wire — gRPC Converse steer frame + ServerCapabilities + EvSteer projection
blocked_by: [02-steer-supersede-contract]
status: done
branch: "plan-steer-while-running/05-wire-grpc-steer-frame"
worktree: ""
issue: "512"
retries: 0
last_error: ""
accumulator: feat/steer-while-running
---

# Task brief

The wire surface. `contracts/proto/mecatl/v1/harness.proto`: add `Steer` +
`SteerCancel` messages, a `steer` / `steer_cancel` oneof arm on
`ConverseRequest` (alongside `prompt`/`resume_approval`/`cancel`/
`cancel_child`), a `ServerCapabilities.steer` bit, and the `EvSteer` echo on
the event stream. `task generate` regenerates `contracts/gen` (commit it; do
NOT hand-edit). Engine app: an `Engine.Steer`/`Run.EnqueueSteer` entry point
the gRPC handler drives, and the `EvSteer` projection. Server: the `Converse`
handler routes steer frames to the live run's inbox and relays the outcome to
the client. The capability bit is computed once in `Service.capabilities()`
(the single-composition-intersection invariant) and consistent across every
sink. gRPC-only v1 — no HTTP/SSE, no ACP (both deferred; see plan Out of
scope). This task widens the engine exported surface → `task api:update` +
`engine/CHANGELOG.md` note (Added = minor) is REQUIRED.

## Acceptance criteria

- AC5.1: A client can send a `steer` frame mid-run on the `Converse` stream and observe the injected message + the `EvSteer` echo on the same stream.
  - verify: `TestSteer_ConverseFrameRoundTrip`
- AC5.2: `ServerCapabilities` advertises `steer` true when the feature is enabled and false/absent when not; an old server (no field) reads as false. The bit is computed **once** in composition (`Service.capabilities()`) and is consistent across every sink that surfaces it — never recomputed per sink.
  - verify: `TestSteer_CapabilityAdvertised`; `TestSteer_CapabilitySingleSource`
- AC5.3: `task generate` keeps `contracts/gen` in sync; the new oneof arm does not change the behaviour of existing arms.
  - verify: inspection — `buf generate` output committed; existing Converse control frames unchanged.
