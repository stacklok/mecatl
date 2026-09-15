# SDK server discovery — acceptance plan

**Contract:** human-reviewed/v2
**Work classification:** Architectural — this adds durable public TypeScript SDK resource, projection, constant, validation, and compatibility-cache contracts before session creation.
**Decision record:** [ADR 0304](../adr/0304-typescript-sdk-public-surface-and-release.md)
**Related decisions:** [ADR 0248](../adr/0248-sdk-compatibility-and-error-contract.md) and [ADR 0245](../adr/0245-safe-build-diagnostics.md) remain authoritative for compatibility and safe server identity.
**Phase:** ergonomic TypeScript SDK server discovery
**Status:** proposed, 2026-09-15. The directing user resolved the public surface, validation, refresh, concurrency, and delivery decisions through the `$grill-me` interview; this contract awaits Plan / Interface review.
**Delivery:** Split. The public SDK additions and compatibility-cache concurrency contract merit approval before implementation.
**Expected tasks:** deferred to orchestration
**Issue:** [stacklok/mecatl#1470](https://github.com/stacklok/mecatl/issues/1470)
**Plan PR:** added when opened
**Approved baseline:** absent until the Plan / Interface PR merges

SDK consumers can inspect the server's compatibility descriptor and safe build identity
before creating a session. The high-level `client.server` namespace returns detached,
readonly SDK projections, preserves open server vocabularies, and keeps request controls
and typed failures consistent across gRPC and HTTP.

The implementation reuses the existing server RPCs and the `ServerCapabilities`,
`ManualDreamCapabilities`, and `DreamTargetCapability` projections introduced by
[#1542](https://github.com/stacklok/mecatl/pull/1542). It does not combine compatibility
negotiation with server identity: [ADR 0248](../adr/0248-sdk-compatibility-and-error-contract.md)
and [ADR 0245](../adr/0245-safe-build-diagnostics.md) intentionally give those resources
different privacy and evolution contracts.

## Human decisions

- [x] Public entry point — Decision: add one readonly `client.server` namespace with `compatibility()` and `info()`; do not add top-level functions or session-bound aliases.
- [x] Compatibility projection — Decision: return SDK-owned `ServerCompatibility` with the supported API-major literal type, the shared `ServerCapabilities`, an open readonly string-valued feature set, and an optional deployment label.
- [x] Stable known vocabularies — Decision: export `ServerFeature` and `ServerPosture` const/type pairs with the exact values below, retain `WATCH_SESSION_EVENTS_FEATURE` as a deprecated alias, and keep `ServerCapabilities.posture` typed as `string` for forward compatibility.
- [x] Refresh and sharing — Decision: every explicit `server.compatibility()` starts a fresh RPC and becomes the cache generation later ordinary calls share; explicit refreshes never coalesce with each other.
- [x] Cache races — Decision: a later-started generation is newer; only the current generation may publish a cached success or clear the cache on failure, so a stale completion cannot overwrite or evict newer state.
- [x] Compatibility validation — Decision: require capabilities and a nonempty, duplicate-free list of nonempty feature identifiers; normalize an empty deployment to absence and otherwise validate the existing bounded printable single-line deployment contract without changing valid text.
- [x] Server-info preflight — Decision: `server.info()` uses the cached or first ordinary compatibility negotiation, never forces refresh, and refuses locally with `UnsupportedFeatureError` unless `server_info` is advertised.
- [x] Server-info projection — Decision: trim and require the build ID; trim the implementation and map empty to `"unknown"`, otherwise require a bounded printable single-line safe identity; expose an absent endpoint as `undefined` and accept a nonempty endpoint only when it is already the canonical safe ADR 0245 projection.
- [x] Provider selection — Decision: `ServerInfoOptions.providerId`, when present, is sent byte-for-byte; the SDK never infers a provider from defaults, sessions, models, or compatibility data.
- [x] Failure taxonomy — Decision: a missing compatibility RPC or API-major mismatch is `IncompatibleServerError`, an absent advertised server-info feature is `UnsupportedFeatureError`, malformed successful responses are `ProtocolError`, and ordinary authenticated/server/transport failures retain their existing typed errors.
- [x] Unsafe-value handling — Decision: rejected server identity values never appear in an error message, diagnostic record, or attached cause; successful projections are detached from transport/cache objects but are not runtime deep-frozen.
- [x] Verification and docs — Decision: cover injected gRPC and HTTP parity, generation races, registry/export/API-report guards, and a focused server-discovery guide; #1471 owns runnable examples and #1482 owns generated SDK changelog entries, so neither is changed here.
- [x] Decision-record outcome — Decision: add no ADR; this work applies ADRs 0304, 0248, and 0245 without making a new durable architecture decision.

## Interface contract

- **gRPC / protobuf:** None — the SDK consumes the existing `GetCompatibilityInfo` and `GetServerInfo` descriptors plus their existing HTTP peers, `GET /v1/compatibility` and `GET /v1/info`, without changing messages, field numbers, routes, generated output, authentication, or listener scoping. An omitted `providerId` omits the selector; a provided value, including the empty string, is sent unchanged.
- **Exported Go APIs / interfaces:** None — no engine, server, adapter, or other exported Go symbol changes. Existing server feature and posture registries are verification sources for the TypeScript constants, not new Go APIs.
- **Tool schemas:** None — no model-facing tool name, input schema, result schema, or dispatch behavior changes.
- **CLI / config:** None — no executable flag, environment variable, config key, default, or precedence changes. The existing `mecated --deployment-id` value is only projected and validated by the SDK.
- **Events / persistence:** None — no event vocabulary, session snapshot, transport envelope, cursor, durable record, cache persistence, or migration changes. The compatibility cache is process-local to one `Client` and ends with that client.
- **Security / authority:** Both methods use the caller's existing request credentials and the server's existing authentication policy; neither creates a session or adds authority. Compatibility calls carry no session-affinity hint. Server info selects only the caller-supplied provider ID and remains diagnostic display data, never provider discovery or connection configuration. Malformed identity data fails closed without relaying the rejected value through messages, diagnostics, or causes.
- **Compatibility / migration:** This is an additive hand-written SDK minor surface. Existing raw/generated APIs, automatic compatibility floor, session capability authority, and client methods remain unchanged. Unknown feature identifiers and posture strings are preserved; known const/type pairs are conveniences, not closed decoders. Missing compatibility support or an API-major mismatch remains incompatible, while a server lacking only `server_info` remains otherwise usable and fails that method with `UnsupportedFeatureError`. `WATCH_SESSION_EVENTS_FEATURE` remains as a deprecated value alias. Public API reports and generated reference documentation change in the implementation PR; no manual SDK changelog edit is made because #1482 owns that generated path.

The exact public TypeScript additions are:

```ts
export const ServerFeature = {
  HttpSteer: "http_steer",
  McpServersOnCreate: "mcp_servers_on_create",
  ServerInfo: "server_info",
  SessionActivityInventory: "session_activity_inventory",
  WatchSessionEvents: "watch_session_events",
} as const;
export type ServerFeature = (typeof ServerFeature)[keyof typeof ServerFeature];

export const ServerPosture = {
  Strict: "strict",
  Trusted: "trusted",
  Auto: "auto",
  Yolo: "yolo",
} as const;
export type ServerPosture = (typeof ServerPosture)[keyof typeof ServerPosture];

/** @deprecated Use ServerFeature.WatchSessionEvents. */
export const WATCH_SESSION_EVENTS_FEATURE = ServerFeature.WatchSessionEvents;

export interface ServerCompatibility {
  readonly apiMajor: typeof SUPPORTED_API_MAJOR;
  readonly capabilities: ServerCapabilities;
  readonly features: ReadonlySet<string>;
  readonly deployment?: string;
}

export interface ServerInfo {
  readonly buildId: string;
  readonly serverImplementation: string;
  readonly llmProviderDisplayEndpoint?: string;
}

export interface ServerInfoOptions {
  readonly providerId?: string;
}

export interface Server {
  compatibility(options?: RequestOptions): Promise<ServerCompatibility>;
  info(
    options?: ServerInfoOptions,
    requestOptions?: RequestOptions,
  ): Promise<ServerInfo>;
}

export interface Client {
  readonly server: Server;
}
```

`ServerCapabilities.posture` remains `string`; the `ServerPosture` value type does not
replace it. The `ServerFeature` values likewise do not narrow
`ServerCompatibility.features`, whose unknown members remain observable.

## In scope — 4 scenarios, in implementation order

### Scenario 1 — Applications read a typed compatibility projection before creating a session

The SDK exposes the existing stateless negotiation resource through the thin public
surface established by [ADR 0304](../adr/0304-typescript-sdk-public-surface-and-release.md),
while preserving the capability/feature distinction in
[ADR 0248](../adr/0248-sdk-compatibility-and-error-contract.md).

**Acceptance:**
- AC1.1: `client.server.compatibility(requestOptions)` performs a compatibility RPC without creating, loading, or binding a session; it preserves request headers, cancellation, and deadline, strips any session-affinity hint, and returns the exact public shape above with `apiMajor === SUPPORTED_API_MAJOR`.
  - verify: vitest:sdk/typescript/test/server-discovery.test.ts#Y29tcGF0aWJpbGl0eSByZXR1cm5zIGEgZGV0YWNoZWQgdHlwZWQgcHJlLXNlc3Npb24gcHJvamVjdGlvbg — `sdk/typescript/test/server-discovery.test.ts :: "compatibility returns a detached typed pre-session projection"`
- AC1.2: Capabilities use the shared complete SDK projection, including optional manual-dream targets; features are returned as a detached `ReadonlySet<string>` that preserves every unknown identifier exactly; deployment `""` becomes `undefined` and every valid nonempty deployment is preserved verbatim.
  - verify: vitest:sdk/typescript/test/server-discovery.test.ts#Y29tcGF0aWJpbGl0eSBwcmVzZXJ2ZXMgdW5rbm93biBmZWF0dXJlIGlkZW50aWZpZXJzIGFuZCBrbm93biBjb25zdGFudCB2YWx1ZXM — `sdk/typescript/test/server-discovery.test.ts :: "compatibility preserves unknown feature identifiers and known constant values"`
- AC1.3: A successful response with absent capabilities, no features, an empty feature identifier, a duplicate feature identifier, or a nonempty deployment that is invalid UTF-8, over 128 UTF-8 bytes, whitespace-only, non-printable, or multiline fails with `ProtocolError`; the error does not expose rejected input.
  - verify: vitest:sdk/typescript/test/server-discovery.test.ts#Y29tcGF0aWJpbGl0eSByZWplY3RzIG1hbGZvcm1lZCBjYXBhYmlsaXR5IGZlYXR1cmUgYW5kIGRlcGxveW1lbnQgcHJvamVjdGlvbnM — `sdk/typescript/test/server-discovery.test.ts :: "compatibility rejects malformed capability feature and deployment projections"`

### Scenario 2 — Explicit refresh and ordinary negotiation share one race-safe cache

Compatibility validation lives on the one negotiation path used by both explicit discovery
and ordinary SDK operations. The cache remains a performance mechanism, never a second
source of compatibility truth. Its public ownership stays inside the thin SDK layer defined
by [ADR 0304](../adr/0304-typescript-sdk-public-surface-and-release.md).

**Acceptance:**
- AC2.1: Every call to `server.compatibility()` starts its own new RPC even when another compatibility request is pending or cached. At start it installs a new monotonically ordered generation as the shared cache; ordinary raw/high-level operations with no explicit refresh coalesce on the current generation, including the newest pending explicit refresh.
  - verify: vitest:sdk/typescript/test/server-discovery.test.ts#ZXhwbGljaXQgY29tcGF0aWJpbGl0eSBjYWxscyBzdGFydCBmcmVzaCBnZW5lcmF0aW9ucyBhbmQgb3JkaW5hcnkgY2FsbHMgc2hhcmUgdGhlIG5ld2VzdA — `sdk/typescript/test/server-discovery.test.ts :: "explicit compatibility calls start fresh generations and ordinary calls share the newest"`
- AC2.2: Each caller resolves or rejects from its own selected generation, but only the later-started current generation may retain a successful cache entry or clear it on failure. Success or failure from an older in-flight generation cannot overwrite or evict a newer pending or successful generation; failure of the current generation leaves the shared cache empty so the next ordinary operation retries.
  - verify: vitest:sdk/typescript/test/server-discovery.test.ts#c3RhbGUgY29tcGF0aWJpbGl0eSBjb21wbGV0aW9ucyBjYW5ub3Qgb3ZlcndyaXRlIG9yIGNsZWFyIGEgbmV3ZXIgZ2VuZXJhdGlvbg — `sdk/typescript/test/server-discovery.test.ts :: "stale compatibility completions cannot overwrite or clear a newer generation"`
- AC2.3: Missing `GetCompatibilityInfo` support over gRPC or HTTP and an API-major mismatch throw `IncompatibleServerError`; malformed successful projections throw `ProtocolError`; authenticated server and transport failures retain their established typed errors. All current-generation failures remain retryable through the empty-cache rule.
  - verify: vitest:sdk/typescript/test/server-discovery.test.ts#Y29tcGF0aWJpbGl0eSBmbG9vciBhbmQgdHJhbnNwb3J0IGZhaWx1cmVzIHN0YXkgdHlwZWQgYW5kIHJldHJ5YWJsZQ — `sdk/typescript/test/server-discovery.test.ts :: "compatibility floor and transport failures stay typed and retryable"`

### Scenario 3 — Applications read safe provider-selectable server identity

`server.info()` projects the separate authenticated identity resource from
[ADR 0245](../adr/0245-safe-build-diagnostics.md). Compatibility advertises whether the
build implements that resource; it does not supply or imply identity.

**Acceptance:**
- AC3.1: `server.info(options, requestOptions)` first uses the cached or first ordinary compatibility negotiation without forcing a refresh, requires `ServerFeature.ServerInfo`, and sends `options.providerId` exactly when supplied. Missing feature advertisement throws `UnsupportedFeatureError` before any info RPC, and no default, session, model, or previous selector is inferred.
  - verify: vitest:sdk/typescript/test/server-discovery.test.ts#c2VydmVyIGluZm8gcmVxdWlyZXMgYWR2ZXJ0aXNlZCBzdXBwb3J0IGFuZCBzZW5kcyBvbmx5IHRoZSBleHBsaWNpdCBwcm92aWRlciBpZA — `sdk/typescript/test/server-discovery.test.ts :: "server info requires advertised support and sends only the explicit provider id"`
- AC3.2: The method preserves info-call request options and returns a detached `ServerInfo`: `buildId` is trimmed and then required nonempty; `serverImplementation` is trimmed, with empty normalized to `"unknown"` and any nonempty value required to be a bounded printable single-line safe identity; an empty provider endpoint becomes `undefined`.
  - verify: vitest:sdk/typescript/test/server-discovery.test.ts#c2VydmVyIGluZm8gdmFsaWRhdGVzIGFuZCBkZXRhY2hlcyBzYWZlIGlkZW50aXR5IHByb2plY3Rpb25z — `sdk/typescript/test/server-discovery.test.ts :: "server info validates and detaches safe identity projections"`
- AC3.3: A nonempty provider endpoint is accepted only when it is already ADR 0245's canonical, valid-Unicode, control-free, absolute scheme/host/escaped-clean-path projection of at most 2048 UTF-8 bytes, with no userinfo, query, fragment, or unsafe host syntax. The SDK rejects rather than repairs a noncanonical value, and no rejected build, implementation, or endpoint text appears in the `ProtocolError`, diagnostics, or cause.
  - verify: vitest:sdk/typescript/test/server-discovery.test.ts#c2VydmVyIGluZm8gcmVqZWN0cyB1bnNhZmUgaWRlbnRpdHkgZGF0YSB3aXRob3V0IGVjaG9pbmcgaXQ — `sdk/typescript/test/server-discovery.test.ts :: "server info rejects unsafe identity data without echoing it"`

### Scenario 4 — Both transports and every public report carry the same contract

The SDK's descriptor-driven transport boundary and public compatibility gates remain those
defined by [ADR 0304](../adr/0304-typescript-sdk-public-surface-and-release.md). This scenario
adds targeted parity and drift guards rather than a parallel transport implementation.

**Acceptance:**
- AC4.1: Injected gRPC and HTTP clients return equivalent compatibility and info projections, preserve the same request controls and feature preflight, and produce the same typed failures for unsupported or malformed responses.
  - verify: vitest:sdk/typescript/test/server-discovery.test.ts#c2VydmVyIGRpc2NvdmVyeSBoYXMgZXF1aXZhbGVudCBncnBjIGFuZCBodHRwIGJlaGF2aW9y — `sdk/typescript/test/server-discovery.test.ts :: "server discovery has equivalent grpc and http behavior"`
- AC4.2: Root, Node, and Deno entry points export `Server`, `ServerCompatibility`, `ServerInfo`, `ServerInfoOptions`, `ServerFeature`, `ServerPosture`, and the deprecated watch alias; API Extractor reports and generated SDK references contain the exact signatures and values above without exposing generated protobuf response types.
  - verify: vitest:sdk/typescript/test/server-discovery.test.ts#c2VydmVyIGRpc2NvdmVyeSBleHBvcnRzIGFuZCBhcGkgcmVwb3J0cyBzdGF5IGNvbXBsZXRl — `sdk/typescript/test/server-discovery.test.ts :: "server discovery exports and api reports stay complete"`
- AC4.3: A cross-language registry guard proves every current Go server feature has the exact corresponding `ServerFeature` value, the four posture values match the canonical application posture vocabulary, and `WATCH_SESSION_EVENTS_FEATURE === ServerFeature.WatchSessionEvents`; future additions fail the guard until the public known-value manifest is considered explicitly.
  - verify: `TestSDKServerDiscovery_Scenario4_ConstantRegistryParity`; `TestADR_0248_FeatureRegistryIsSingleSource`
- AC4.4: TSDoc and `user-docs/building/typescript-sdk/server-discovery.md`, linked from the TypeScript SDK index, explain negotiation versus identity, explicit refresh versus ordinary caching, feature/capability/posture interpretation, provider selection, typed failures, and why the display endpoint is not connection configuration. No runnable example or manual changelog entry is added.
  - verify: inspection — the focused guide and SDK index contain the stated contract; `task docs` validates generated references and links.

## Out of scope

| Item | Defer-to | Decision |
|---|---|---|
| New server RPCs, HTTP routes, protobuf fields, feature identifiers, capabilities, or posture values | Separate server work | The required server contract already exists; this issue exposes it ergonomically. |
| Inferring workflow support from SDK, build, implementation, deployment, or package versions | Not planned | Only API major, advertised feature identifiers, and server capabilities are authoritative. |
| Treating the provider display endpoint as connection configuration | Not planned | ADR 0245 defines diagnostic display data only; connection ownership remains with the caller. |
| Provider/model discovery or a remembered/default provider selector | Existing typed namespaces or later design | `providerId` is an exact, per-call selector for an already-known provider. |
| Runtime deep-freezing of projections | Not planned | Readonly public types plus detached values provide the contract without imposing freeze semantics. |
| Formatted diagnostics, terminal tables, or operator-facing reports | Application-owned | The SDK exposes typed data and does not own presentation. |
| Runnable server-discovery example | [#1471](https://github.com/stacklok/mecatl/issues/1471) | Cross-surface examples are handled together. |
| Manual SDK changelog edit | [#1482](https://github.com/stacklok/mecatl/issues/1482) | The automated release-note path owns generated changelog entries. |

## Definition of done

1. Applicable `task sdk:lint`, `task sdk:typecheck`, `task sdk:test`, `task sdk:build`, `task sdk:api:check`, `task lint`, `task test`, and `task docs` gates pass.
2. `task ac-trace-strict` resolves every named proof when the plan becomes `landed`.
3. `go run ./cmd/mecademo` remains green for runtime changes.
4. The implementation PR links this Plan / Interface PR and its full approved commit and reports interface conformance.
5. `/panel-review` reports no ship blockers or unwaived reviewer failures.

## Deferred decisions and known risks

- A shared compatibility request necessarily has the request controls of the generation's initiating caller. Later ordinary callers coalesce on that promise; an explicit caller that needs independent controls always starts its own refresh generation.
- `ReadonlySet<string>` is a type-level immutability contract over an SDK-owned detached `Set`; no runtime read-only wrapper or deep freeze is promised.
- Future server feature and posture values remain observable as strings. Adding them to the exported known-value constants is an intentional SDK minor-surface decision guarded by the cross-language parity test.
