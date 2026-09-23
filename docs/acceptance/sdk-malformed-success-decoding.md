# TypeScript SDK malformed-success decoding - acceptance plan

**Contract:** human-reviewed/v2
**Work classification:** Architectural - this changes the durable public diagnostic and security policy for `ProtocolError` at the TypeScript SDK's HTTP successful-response boundary.
**Decision record:** [ADR 0349](../adr/0349-typescript-sdk-malformed-success-decoding.md)
**Phase:** TypeScript SDK HTTP transport hardening
**Status:** in-progress, 2026-09-18. The stacked implementation candidate satisfies AC1.1-AC2.4 and all applicable local gates; the host Xcode/macOS 27 linker blocks the full CGO race suite, so CI must supply that final proof. The contract becomes authoritative only after the Plan / Interface and Implementation PRs merge in order.
**Delivery:** Split, with an explicit checkpoint waiver. The Plan / Interface PR records the security and compatibility boundary; in this session the directing human explicitly instructed the implementation to proceed as the next `gh stack` layer without waiting for the plan to merge. `/plan-orchestrate` is not used because its merged-baseline precondition is intentionally waived.
**Expected tasks:** 1
**Issue:** [stacklok/mecatl#1694](https://github.com/stacklok/mecatl/issues/1694).
**Plan PR:** [#1698](https://github.com/stacklok/mecatl/pull/1698)
**Approved baseline:** `b6e05820685a85bf3cf027ed42da4e269060e6ed`, the exact Plan / Interface commit used under the explicit human stacking exception; it is not merged authority. The directing human requested two sequential `gh stack` PRs and explicitly said there is no need to wait for the plan to merge.

The TypeScript SDK rejects malformed successful unary HTTP responses and
ordinary SSE data frames without retaining runtime-dependent decoder exceptions.
The cause-free error keeps safe transport metadata while established server,
network, authentication, cancellation, and streaming behavior remains intact.

This policy applies at the shared HTTP boundary described by
[ADR 0349](../adr/0349-typescript-sdk-malformed-success-decoding.md). It does not
change public method signatures or the server protocol.

## Human decisions

None — issue #1694 defines the exact cause, metadata, exclusion, documentation, and compatibility policy, and the directing human authorized the two-PR stack.

## Interface contract

- **gRPC / protobuf:** None - no service, method, message, field, descriptor, or
  generated protobuf output changes. The policy applies only after successful
  HTTP or SSE responses reach the hand-written TypeScript transport.
- **Exported Go APIs / interfaces:** None - no Go package, symbol, signature, or
  API snapshot changes.
- **Tool schemas:** None - response decoding is an SDK transport concern and does
  not change model-facing tools or dispatch.
- **CLI / config:** None - no flag, environment variable, settings key, default,
  or precedence changes.
- **Events / persistence:** None - no event, snapshot, cursor, cache, log, or
  migration changes. Ordinary SSE iteration and cancellation keep their current
  lifecycle.
- **Security / authority:** After a unary response body has been acquired,
  successful unary HTTP and ordinary SSE JSON,
  well-known-type, and protobuf decode failures produce the existing generic
  `ProtocolError` with `code: "protocol"`, `transport: "http"`, HTTP status,
  and `X-Request-ID` when available, but no `cause`. The SDK copies no decoder
  message or rejected response value into the error and performs no arbitrary
  string redaction. Failure to read a unary response body after headers remains
  a caused `ProtocolError`; a valid SSE `event: error` frame and a non-2xx HTTP
  response continue through the existing typed server-error boundary.
- **Compatibility / migration:** Public TypeScript types and method signatures
  are unchanged. This intentionally removes runtime-specific diagnostic detail
  from `ProtocolError.cause` for malformed successful HTTP and SSE payloads.
  The four existing generic messages remain exact and `toJSON()` remains
  response-content-free. Non-2xx typed server errors retain code, status,
  request ID, and cause; credential-provider and HTTP authentication failures,
  fetch/network failures, post-header body-read failures, abort, cancellation,
  and stream/control semantics remain unchanged. The `ProtocolError` TSDoc and
  generated SDK reference own the public error contract. The TypeScript SDK
  connection guide links that reference and briefly calls out safe logging;
  architecture and implementation notes record the living design.

## In scope - 2 scenarios, in implementation order

### Scenario 1 - Malformed successful payloads expose only safe error metadata

The malformed-success decode paths share the cause-free boundary from
[ADR 0349](../adr/0349-typescript-sdk-malformed-success-decoding.md), covering
both unary HTTP responses and ordinary SSE data frames after body acquisition.

**Acceptance:**

- AC1.1: A successful unary response with malformed JSON rejects with
  `ProtocolError`, `code: "protocol"`, `transport: "http"`, the HTTP status,
  the response request ID when present, and no own `cause` property; its message
  is exactly `The mecatl server returned invalid JSON`, and neither the message
  nor `toJSON()` contains a planted response canary. A second case without an
  `X-Request-ID` proves that no request ID is synthesized.
  - verify: vitest:sdk/typescript/test/http-decoding-errors.test.ts#bWFsZm9ybWVkIHN1Y2Nlc3NmdWwgSFRUUCBhbmQgU1NFIHBheWxvYWRzIG9taXQgZGVjb2RlciBjYXVzZXM - `sdk/typescript/test/http-decoding-errors.test.ts :: "malformed successful HTTP and SSE payloads omit decoder causes"`
- AC1.2: A successful unary response that parses as JSON but fails
  well-known-type normalization or protobuf-es decoding has the same cause-free,
  canary-free metadata contract and the exact message
  `The mecatl server returned an invalid response`.
  - verify: vitest:sdk/typescript/test/http-decoding-errors.test.ts#bWFsZm9ybWVkIHN1Y2Nlc3NmdWwgSFRUUCBhbmQgU1NFIHBheWxvYWRzIG9taXQgZGVjb2RlciBjYXVzZXM - `sdk/typescript/test/http-decoding-errors.test.ts :: "malformed successful HTTP and SSE payloads omit decoder causes"`
- AC1.3: After one valid ordinary SSE data frame is yielded, malformed JSON and
  descriptor/protobuf failures in the next frame follow the same cause-free,
  canary-free policy. The exact messages remain `The mecatl SSE stream contained
  invalid JSON` and `The mecatl SSE stream contained an invalid event`. Cases
  with and without `X-Request-ID` prove its faithful propagation, the failing
  pull rejects, and no later frame is yielded.
  - verify: vitest:sdk/typescript/test/http-decoding-errors.test.ts#bWFsZm9ybWVkIHN1Y2Nlc3NmdWwgSFRUUCBhbmQgU1NFIHBheWxvYWRzIG9taXQgZGVjb2RlciBjYXVzZXM - `sdk/typescript/test/http-decoding-errors.test.ts :: "malformed successful HTTP and SSE payloads omit decoder causes"`

### Scenario 2 - Neighboring error and lifecycle semantics stay unchanged

The cause policy leaves the typed server-error contract from
[ADR 0248](../adr/0248-sdk-compatibility-and-error-contract.md) and the shared
[TypeScript SDK architecture](../architecture.md#typescript-sdk) intact.

**Acceptance:**

- AC2.1: Independently exercised non-2xx RFC 9457 responses and valid SSE
  `event: error` frames retain their typed code, HTTP status, request ID,
  message, and established parsed-problem cause.
  - verify: vitest:sdk/typescript/test/http-decoding-errors.test.ts#c2VydmVyIGFuZCBhdXRoZW50aWNhdGlvbiBlcnJvcnMgcmV0YWluIHRoZWlyIHR5cGVzIGFuZCBjYXVzZXM - `sdk/typescript/test/http-decoding-errors.test.ts :: "server and authentication errors retain their types and causes"`
- AC2.2: Credential-provider rejection and an HTTP 401 response remain
  `AuthenticationError` values with their established causes and available
  request ID/status metadata.
  - verify: vitest:sdk/typescript/test/http-decoding-errors.test.ts#c2VydmVyIGFuZCBhdXRoZW50aWNhdGlvbiBlcnJvcnMgcmV0YWluIHRoZWlyIHR5cGVzIGFuZCBjYXVzZXM - `sdk/typescript/test/http-decoding-errors.test.ts :: "server and authentication errors retain their types and causes"`
- AC2.3: Fetch/network failures remain `TransportError` values with their native
  cause. A unary post-header response-body read failure remains a
  `ProtocolError` with its native cause. Abort/cancellation preserves the
  current signal reason and transport classification, while SSE reader failures
  continue to escape the decoder boundary unchanged.
  - verify: vitest:sdk/typescript/test/http-decoding-errors.test.ts#SFRUUCB0cmFuc3BvcnQgYm9keS1yZWFkIGFuZCBjYW5jZWxsYXRpb24gZmFpbHVyZXMgcmV0YWluIHRoZWlyIGNhdXNlcw - `sdk/typescript/test/http-decoding-errors.test.ts :: "HTTP transport body-read and cancellation failures retain their causes"`
- AC2.4: The TypeScript SDK error documentation, connection guide, architecture,
  and implementation notes state the malformed-success cause policy and its
  retained metadata without changing generated public signatures.
  - verify: inspection - `task sdk:api:check`, `task sdk:docs:check`, `task docs`,
    and `task site:build` prove generated-reference freshness and documentation
    integrity; review proves the behavioral wording.

## Out of scope

| Item | Defer-to | Decision |
|---|---|---|
| Non-2xx server-error normalization | Existing SDK error contract | Preserve `errorFromProblem` behavior and causes. |
| Fetch, network, authentication, and abort classification | Existing transport contract | Preserve existing error types, causes, and signal behavior. |
| Best-effort decoder-message redaction | Not planned | Omit the arbitrary decoder cause instead of maintaining an unsafe string filter. |
| Server, wire, stream, and control lifecycle changes | Not part of #1694 | Keep current protocols and iterator/control behavior. |

## Definition of done

1. Focused SDK lint, typecheck, unit, build, API, and documentation checks pass.
2. `task lint`, `task test`, `task docs`, `task site:build`, and the offline demo
   pass.
3. `task ac-trace-strict` resolves every named proof when this plan becomes
   `landed`.
4. Tests are observed failing against the planted pre-fix decoder-cause behavior
   before the shared helper is implemented.
5. `/panel-review` reports no ship blockers or unwaived reviewer failures.
6. The stacked Implementation PR reports interface conformance and links this
   Plan / Interface PR; humans retain merge authority for both PRs.

## Deferred decisions and known risks

- JavaScript runtimes can change native decoder messages. The tests therefore
  plant response canaries and assert the SDK-owned error surface rather than a
  runtime-specific exception string.
