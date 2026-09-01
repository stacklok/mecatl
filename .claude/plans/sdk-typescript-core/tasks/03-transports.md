---
id: 03-transports
title: Raw transports, typed errors, compatibility floor
blocked_by: [02-codegen]
status: pending
branch: ""
worktree: ""
issue: "911"
retries: 0
last_error: ""
accumulator: sdk/10-architecture-adr
---

# Task brief

One transport-neutral raw-operation seam with two implementations, plus the
typed error hierarchy and the `GetCompatibilityInfo` floor. Scenario 3.
ADR 0278 Decisions 1 and 3; ADR 0248.

**Stack:** Connect-ES v2 (`@connectrpc/connect` + `@connectrpc/connect-node`)
for Node/Bun **real gRPC over HTTP/2** (TCP and UDS). UDS is not a first-class
`baseUrl` scheme — wire it through connect-node's node options (`http2.connect`
socket path). Browsers do **not** use Connect-ES (mecated does not speak
Connect); the `.` export's browser transport is hand-written fetch + SSE
against mecated's existing HTTP API, behind the same raw-operation seam.

**Mocking (M1 cut only):** clients accept an injected Transport. The SDK's
own tests may use `createRouterTransport` (and a fetch-level HTTP fake)
routed at handler functions or fixture files. Do **not** ship a
downstream-app mocker or a transport-agnostic e2e testkit — that is #872.

**Steer over HTTP:** gate on the `http_steer` **feature** string from
`GetCompatibilityInfo` (ADR 0252). When absent, typed unsupported-feature
error — never silent drop, never "try until 404". gRPC steer succeeds.

**Error-code parity (AC3.9):** export a trivially-parseable const manifest of
typed error codes from the TS package. The Go test
`TestSDKTypescriptCore_Scenario3_ErrorCodeParity` lives in the **root
module** (never `engine/`). Reading `internal/adapter/server/` is fine; do
not edit it.

Do not implement Client/Session/Run ergonomics (Scenarios 4–5). Do not
touch proto or the Go server.

Branch `sdk/13-transports` off the stack tip. Do not push.

## Acceptance criteria

- AC3.1: The same raw operation invoked over the gRPC transport and the HTTP
  transport returns the same normalized result for the same server state —
  transport parity at the raw seam. Proven with twin in-process fakes (a
  `createRouterTransport` gRPC fake and a fetch-level HTTP fake) driven from
  one shared scripted-state fixture; the live-wire proof is Scenario 9's
  AC9.1/AC9.2, not this test.
  - verify: `sdk/typescript/test/transport-parity.test.ts :: "raw operations agree across gRPC and HTTP"`
- AC3.2: A client constructed over an injected `createRouterTransport`
  exercises unary and server-streaming operations with no network and no
  daemon — the ADR-0278 M1 test seam works as documented.
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
  - verify: `sdk/typescript/test/steer.test.ts :: "HTTP steer is a typed unsupported-feature error"`
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
