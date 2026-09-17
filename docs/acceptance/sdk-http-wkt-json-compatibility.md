# TypeScript SDK HTTP well-known-type JSON compatibility — acceptance plan

**Contract:** human-reviewed/v2
**Work classification:** Bounded — this repairs the existing hand-written TypeScript SDK HTTP/SSE decoder without changing protobuf, exported APIs, server behavior, or a durable architecture decision.
**Decision record:** None — ADR 0279 already assigns daemon HTTP/SSE decoding to the hand-written SDK transport, and the fix stays inside that boundary.
**Phase:** HTTP transport compatibility
**Status:** proposed, 2026-09-17. Drafted from issue #1631 and the current SDK transport contract.
**Delivery:** Split. The recursive descriptor-guided conversion, fail-closed numeric rules, raw-response preservation, and release documentation deserve interface review before implementation.
**Expected tasks:** deferred to orchestration
**Issue:** [stacklok/mecatl#1631](https://github.com/stacklok/mecatl/issues/1631).
**Plan PR:** [#1684](https://github.com/stacklok/mecatl/pull/1684)
**Approved baseline:** absent until the Plan / Interface PR merges

Let the TypeScript SDK consume the daemon's existing standard-library JSON encoding of
`google.protobuf.Timestamp` and `google.protobuf.Duration` over HTTP and SSE. Immediately before
protobuf-es decoding, the HTTP transport walks the output descriptor and converts only those two
well-known message types from `{seconds,nanos}` objects to their ProtoJSON strings.

The conversion is recursive across message, repeated, and map values, preserves an already-valid
ProtoJSON string or `null`, and operates on a detached value so `getRawJson()` continues to expose
the exact registered daemon response. Invalid objects remain typed HTTP `ProtocolError` failures
rather than being rounded, clamped, guessed from field names, or silently defaulted.

## Human decisions

None — the issue fixes a mismatch inside the existing HTTP transport, and the accepted SDK architecture, protobuf validity rules, typed-error contract, raw-JSON contract, and automated release-changelog workflow determine the behavior.

## Interface contract

- **gRPC / protobuf:** None — no service, message, field, number, generated source, or gRPC decoder changes. The HTTP normalizer consumes the existing output `DescMessage` and recognizes only descriptors whose type names are exactly `google.protobuf.Timestamp` or `google.protobuf.Duration`.
- **Exported Go APIs / interfaces:** None — no Go package or symbol changes. No TypeScript export or signature changes; `createHttpTransport`, `RawClient`, `ProtocolError`, and `getRawJson` retain their existing declarations.
- **Tool schemas:** None — no model-visible tool name, input, output, permission, or advertisement changes.
- **CLI / config:** None — no flag, environment variable, setting, transport option, default, or precedence changes.
- **Events / persistence:** None — the daemon's JSON, SSE frames, protobuf events, snapshots, stores, and replay formats do not change. Normalization applies only to a detached decode value at the TypeScript SDK HTTP boundary; every message and nested event registered through `getRawJson()` retains the structurally unchanged parsed JSON value, including `{seconds,nanos}` objects and unknown fields.
- **Security / authority:** None — the change adds no request, network destination, credential, authority, trust decision, or mutation. Descriptor identity, not JSON key names, selects conversion; unrelated `{seconds,nanos}` application messages remain untouched.
- **Compatibility / migration:** This is an additive bug fix for `@stacklok-oss/mecatl-sdk` HTTP clients under API major 1. At descriptor-declared Timestamp or Duration positions, an object may carry `seconds` as an integer JSON number or canonical signed base-10 integer string and `nanos` as an integer JSON number; either member may be absent and then has protobuf's zero default. Valid values convert without loss to the well-known type's canonical ProtoJSON string, including zero, three, six, or nine fractional digits as needed to preserve nanoseconds and a leading minus sign for negative Duration values. An input that is already a string or `null` passes unchanged to protobuf-es, retaining protobuf-es v2's existing singular-message null-as-unset behavior. Other non-record values, non-integral or imprecise numbers, non-canonical decimal strings, invalid nanos, mixed-sign Duration components, or values outside the protobuf well-known-type range fail as the existing typed HTTP `ProtocolError` for unary responses or SSE events. Descriptor traversal recognizes both a known field's protobuf name and JSON name; it does not infer types from either name. gRPC behavior is unchanged. The implementation updates the SDK connection guide plus the living architecture/implementation notes. Its merge title uses a descriptive `fix(sdk): ...` subject so ADR 0328's release automation adds the compatibility fix to the next generated `sdk/typescript/CHANGELOG.md` section; released changelog sections are not edited manually.

## In scope — 3 scenarios, in implementation order

### Scenario 1 — unary HTTP responses decode daemon timestamp and duration objects

The hand-written browser transport remains responsible for adapting the daemon HTTP protocol to
protobuf-es, as established by [ADR 0279](../adr/0279-typescript-sdk-architecture.md) and described
in the [TypeScript SDK architecture](../architecture.md#typescript-sdk).

**Acceptance:**
- AC1.1: A unary response converts descriptor-declared Timestamp objects with numeric or decimal-string seconds and absent, millisecond, microsecond, or nanosecond precision into the corresponding protobuf-es Timestamp without precision loss.
  - verify: vitest:sdk/typescript/test/http-wkt-json.test.ts#dW5hcnkgSFRUUCByZXNwb25zZXMgZGVjb2RlIGRhZW1vbiB0aW1lc3RhbXAgb2JqZWN0cw — `sdk/typescript/test/http-wkt-json.test.ts :: "unary HTTP responses decode daemon timestamp objects"`
- AC1.2: A unary response converts descriptor-declared Duration objects, including positive, zero, and negative seconds/nanos combinations, into the corresponding protobuf-es Duration without precision loss.
  - verify: vitest:sdk/typescript/test/http-wkt-json.test.ts#dW5hcnkgSFRUUCByZXNwb25zZXMgZGVjb2RlIGRhZW1vbiBkdXJhdGlvbiBvYmplY3Rz — `sdk/typescript/test/http-wkt-json.test.ts :: "unary HTTP responses decode daemon duration objects"`
- AC1.3: Conversion follows descriptors recursively through nested messages, repeated fields, and map values under either the descriptor's protobuf field name or JSON field name, while leaving map keys, scalar fields, unknown fields, and ordinary messages with `seconds` or `nanos` keys unchanged.
  - verify: vitest:sdk/typescript/test/http-wkt-json.test.ts#bm9ybWFsaXphdGlvbiBmb2xsb3dzIGRlc2NyaXB0b3JzIHRocm91Z2ggbmVzdGVkIGxpc3QgYW5kIG1hcCB2YWx1ZXM — `sdk/typescript/test/http-wkt-json.test.ts :: "normalization follows descriptors through nested list and map values"`

### Scenario 2 — streamed decoding and raw JSON preserve both wire dialects

Streaming responses use the same output-descriptor contract as unary responses, while the raw seam
continues to expose transport-native data under [ADR 0304's HTTP transport contract](../adr/0304-typescript-sdk-public-surface-and-release.md).

**Acceptance:**
- AC2.1: Every ordinary SSE data frame, including wrapped `Converse` events and nested `WatchSessionEvents` events, applies the same descriptor-guided Timestamp/Duration conversion before protobuf-es decoding without buffering the stream.
  - verify: vitest:sdk/typescript/test/http-wkt-json.test.ts#U1NFIGZyYW1lcyBkZWNvZGUgZGVzY3JpcHRvci1kZWNsYXJlZCB3ZWxsLWtub3duIHR5cGVz — `sdk/typescript/test/http-wkt-json.test.ts :: "SSE frames decode descriptor-declared well-known types"`
- AC2.2: Already-valid RFC 3339 Timestamp and protobuf Duration strings pass through unchanged and decode to the same protobuf values over unary and streamed HTTP paths; `null` passes unchanged and retains protobuf-es's existing singular-message null-as-unset behavior.
  - verify: vitest:sdk/typescript/test/http-wkt-json.test.ts#UHJvdG9KU09OIHN0cmluZ3MgYW5kIG51bGwgcGFzcyB0aHJvdWdoIHVuY2hhbmdlZA — `sdk/typescript/test/http-wkt-json.test.ts :: "ProtoJSON strings and null pass through unchanged"`
- AC2.3: `getRawJson()` on unary messages, streamed envelopes, and their registered nested events returns the original parsed JSON, with well-known values still represented exactly as received and unknown fields retained.
  - verify: vitest:sdk/typescript/test/http-wkt-json.test.ts#cmF3IEpTT04gcHJlc2VydmVzIHRoZSBvcmlnaW5hbCBkYWVtb24gd2lyZSB2YWx1ZQ — `sdk/typescript/test/http-wkt-json.test.ts :: "raw JSON preserves the original daemon wire value"`
- AC2.4: Against a same-checkout `mecated` with a durable schedule store and real HTTP listener, an SDK-created and retrieved schedule exposes its daemon-encoded one-shot Timestamp and fire-timeout Duration as the exact protobuf values while its registered raw JSON retains the object wire shapes.
  - verify: vitest:sdk/typescript/e2e/http.e2e.test.ts#c2NoZWR1bGUgdGltZXN0YW1wcyBhbmQgZHVyYXRpb25zIGRlY29kZSBmcm9tIHRoZSByZWFsIGRhZW1vbg — `sdk/typescript/e2e/http.e2e.test.ts :: "schedule timestamps and durations decode from the real daemon"`

### Scenario 3 — malformed values fail closed and compatibility is documented

Transport violations retain the typed failure promised by
[ADR 0248](../adr/0248-sdk-compatibility-and-error-contract.md), while the SDK guide explains the
compatibility behavior without asking applications to rewrite response data.

**Acceptance:**
- AC3.1: Malformed non-null shapes, fractions, unsafe numeric seconds, invalid decimal strings, invalid nanos/sign combinations, and out-of-range Timestamp or Duration values fail with `ProtocolError` carrying `transport: "http"`; no value is rounded, clamped, or silently coerced.
  - verify: vitest:sdk/typescript/test/http-wkt-json.test.ts#aW52YWxpZCB3ZWxsLWtub3duIHR5cGUgb2JqZWN0cyBmYWlsIHdpdGggdHlwZWQgcHJvdG9jb2wgZXJyb3Jz — `sdk/typescript/test/http-wkt-json.test.ts :: "invalid well-known type objects fail with typed protocol errors"`
- AC3.2: `user-docs/building/typescript-sdk/connect.md`, `docs/architecture.md`, and `docs/design/IMPLEMENTATION-NOTES.md` describe transparent HTTP/SSE compatibility, descriptor-guided recursion, ProtoJSON-string pass-through, raw-response preservation, and fail-closed malformed input.
  - verify: inspection — these are reader-facing and living-design statements rather than executable behavior
- AC3.3: The implementation merge title is a descriptive `fix(sdk): ...` entry eligible for ADR 0328's automated next-release changelog generation, and no released SDK changelog section is hand-edited.
  - verify: inspection — release-note classification and immutable released changelog history are repository workflow metadata

## Out of scope

| Item | Defer-to | Decision |
|---|---|---|
| Changing the daemon from `encoding/json` to ProtoJSON | separate server compatibility work | The SDK boundary accepts both existing wire dialects; server output and other HTTP consumers remain unchanged. |
| General JSON coercion or support for other protobuf well-known types | a separately reviewed issue | Only Timestamp and Duration are affected and selected by exact descriptor identity. |
| Normalizing requests or the gRPC transport | not planned | Requests already use protobuf-es ProtoJSON, and gRPC carries protobuf messages rather than daemon stdlib JSON. |
| Exporting the normalizer as public SDK API | not planned | This is private HTTP transport compatibility behavior. |
| Editing an already-published changelog section | next SDK release workflow | ADR 0328 generates the next version section from reviewed SDK commits. |

## Definition of done

1. `task sdk:lint`, `task sdk:typecheck`, `task sdk:test`, `task sdk:build`, `task sdk:api:check`, `task sdk:e2e`, `task docs`, `task site:build`, and `task api:check` pass.
2. `task ac-trace-strict` resolves every named proof when the plan becomes `landed`.
3. `go run ./cmd/mecademo` remains green for runtime changes.
4. The implementation PR links the Plan / Interface PR and approved commit and reports interface conformance.
5. `/panel-review` reports no ship blockers or unwaived reviewer failures.

## Deferred decisions and known risks

- A future daemon-wide ProtoJSON migration could remove this compatibility path only after all HTTP consumers and the raw-wire contract are reviewed; it is not implied by this SDK-local fix.
