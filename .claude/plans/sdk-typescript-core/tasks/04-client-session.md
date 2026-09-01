---
id: 04-client-session
title: Client, Session lifecycle, and connection status
blocked_by: [03-transports]
status: pending
branch: ""
worktree: ""
issue: "912"
retries: 0
last_error: ""
accumulator: sdk/10-architecture-adr
---

# Task brief

Ergonomic `connect()` / `Client` / `Session` / connection status on top of
the raw seam. Scenario 4. Do not implement `session.run()` (Scenario 5).

`connect()` returns a `Client`. `client.sessions.create/get/fork` map to
`CreateSession` / `GetSession` / `ForkSession`. `session.close()` releases
runtime resources only (session remains loadable). `session.delete()`
removes durable state (subsequent `get()` is typed not-found).

Status is a multicast with `getSnapshot()` / `subscribe()` over the closed
vocabulary: connecting, online, reconnecting, offline, unauthorized,
incompatible. Heartbeat `GetCompatibilityInfo` **only while subscribed**.
Ordinary request outcomes update status immediately. Hidden-page pause uses
a stubbed visibility API in vitest (M4 owns real browsers). Node path is a
no-op for visibility. Client close including `Symbol.asyncDispose` detaches
subscribers, stops heartbeat, closes owned transports; later ops are typed
invalid-state.

Prove via injected Transport (`createRouterTransport`), not a live daemon.

Branch `sdk/14-client-session` off the stack tip. Do not push.

## Acceptance criteria

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
