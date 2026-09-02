---
id: 04-mecatui-affinity
title: mecatui session-bound metadata propagation
blocked_by: [02-grpc-affinity-validation]
status: pending
branch: ""
worktree: ""
issue: ""
retries: 0
last_error: ""
accumulator: acc/session-affinity-and-handoff
---

# Task brief

Teach the mecatui client to attach the exact session ID as outgoing gRPC metadata on every operation for which it already holds the target session. Add a session-bound converse opener used by the TUI's prompt/retry flow while retaining the exported `OpenConverse(ctx)` compatibility path for raw/external callers.

**Likely scope:** `cmd/mecatui/client/client.go`, `events.go`, compact/reflection/adoption/session-management client methods, stream/reconnect helpers, and client tests; call sites in `cmd/mecatui/ui` only where they must select the bound opener.

**Invariants:** one shared metadata helper; exact bytes, no normalization; credentials and affinity metadata compose rather than overwrite one another; every unary/server stream and the one-run `Converse` stream is bound before work; later controls ride that stream binding. Keep `ui` proto-free and preserve the existing `OpenConverse(ctx)` public signature and behavior. Use fake clients/bufconn offline and strict TDD.

## Acceptance criteria

- AC4.1: mecatui sends the exact session ID as gRPC metadata for every session-bound
  unary and server-stream operation it performs.
  - verify: `TestSessionAffinityAndHandoff_Scenario4_MecatuiUnaryAndStreamPropagation`

- AC4.2: mecatui opens a session-bound `Converse` before prompt or retry and all later
  controls use that stream binding; the existing `OpenConverse(ctx)` API still compiles
  and behaves as before for external/raw callers.
  - verify: `TestADR_0290_MecatuiOpenConverseCompatibility`
