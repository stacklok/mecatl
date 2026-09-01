---
id: 06-events
title: Event unions and Go↔TS kind parity
blocked_by: [05-run]
status: pending
branch: ""
worktree: ""
issue: "914"
retries: 0
last_error: ""
accumulator: sdk/10-architecture-adr
---

# Task brief

Hand-crafted discriminated unions for agent and team events, plus the
Go↔TS kind-parity gate. Scenario 6.

Known kinds narrow by literal `kind` to a payload whose fields match the
proto. Unknown kinds become `{ kind: "unknown", wireKind, ... }` with
decoded common fields plus transport-native raw data (raw JSON for HTTP,
unknown protobuf bytes for gRPC); iteration continues.

**Mechanism (do not invent a second one):** the TS package exports a
trivially-parseable const manifest of known kinds (same pattern as AC3.9
error codes). Go parity tests live in the **root module**, never `engine/`.
They read the manifest from `sdk/typescript/` and compare against the
server vocabulary. Reading `internal/adapter/server/` for the relay-skipped
audit list is fine — do not edit that tree.

Named tests (exact identifiers):
- `TestSDKTypescriptCore_Scenario6_EventKindParity`
- `TestSDKTypescriptCore_Scenario6_LogOnlyKindsAudited`

Log-only kinds are typed **or** explicitly excluded by the audit list —
never silently absent.

Branch `sdk/16-events` off the stack tip. Do not push.

## Acceptance criteria

- AC6.1: Every known agent and team event kind narrows by literal `kind` to
  a payload type whose fields match the proto payload for that kind.
  - verify: `sdk/typescript/test/events.test.ts :: "known kinds narrow by literal kind"`
- AC6.2: An event of an unknown kind decodes to
  `{ kind: "unknown", wireKind, ... }` carrying the decoded common fields
  plus the transport-native raw data — raw JSON over HTTP, unknown protobuf
  bytes over gRPC — and iteration continues.
  - verify: `sdk/typescript/test/events.test.ts :: "unknown kinds preserve transport-native raw data"`
- AC6.3: A Go-side parity guard enumerates the wire event kinds the server
  can emit and fails when the TS union misses one or types one the server
  no longer emits — a new `session.Event` kind fails CI until it is typed.
  - verify: `TestSDKTypescriptCore_Scenario6_EventKindParity`
- AC6.4: Log-only event kinds (the relay-skipped set) are typed or
  explicitly excluded by the parity guard's audit list — never silently
  absent.
  - verify: `TestSDKTypescriptCore_Scenario6_LogOnlyKindsAudited`
