# SDK session lifecycle surface — acceptance plan

**Contract:** human-reviewed/v2
**Work classification:** Architectural — this adds durable public TypeScript SDK resource, projection, option, and return-value contracts across the session lifecycle.
**Decision record:** [ADR 0304](../adr/0304-typescript-sdk-public-surface-and-release.md)
**Phase:** ergonomic TypeScript SDK session lifecycle
**Status:** proposed, 2026-09-14. The directing user resolved the public surface and delivery decisions through the `$grill-me` interview; this contract awaits Plan / Interface review.
**Delivery:** Split. The public SDK additions, projection semantics, lifecycle behavior, and transport-parity contract merit approval before implementation.
**Expected tasks:** deferred to orchestration
**Issue:** [#1468](https://github.com/stacklok/mecatl/issues/1468)
**Plan PR:** <added when opened>
**Approved baseline:** absent until the Plan / Interface PR merges

SDK consumers can inspect a session, read its authoritative transcript, mutate its
title or mode, compact it, create clear/fork successors, and retry a failed model
step without constructing raw descriptor calls. These operations remain thin over
the existing server contract while returning SDK-owned, readonly values and the
ordinary `Session` and `Run` lifecycle handles.

The generated protobuf entry point remains available at `./gen`; the ergonomic
surface owns the stable application vocabulary described below. This applies the
public-surface boundary in [ADR 0304](../adr/0304-typescript-sdk-public-surface-and-release.md)
and the existing [TypeScript SDK architecture](../architecture.md#typescript-sdk).

## Human decisions

- [x] Ownership of the public projections — Decision: the SDK owns readonly `SessionSnapshot` and `SessionTranscript` projections; generated protobuf types remain available only through the existing `./gen` escape hatch.
- [x] Snapshot completeness and compatibility — Decision: project every current canonical `Session` field, including per-session media capabilities, with descriptor-backed drift guards; omit deprecated `title` and `title_provenance` as properties and consult them only when canonical `title_metadata` is absent.
- [x] Permission-mode vocabulary — Decision: export an SDK-owned `SessionMode` constant/type pair for numeric values 0–3, use it in create/snapshot/set-mode APIs, and throw `ProtocolError` for an unknown returned value.
- [x] Session inspection and mutation results — Decision: `snapshot()` and `transcript()` return the SDK projections, `rename()` and `setMode()` return the resulting snapshot, snapshot-bearing operations refresh the handle's known per-session media gates for later runs, and `compact()` returns only the server's `compacted` Boolean.
- [x] Successor operations — Decision: `clear()` returns a distinct successor `Session` and never closes or invalidates its source; `fork()` remains only on `client.sessions`, with no `session.fork()` alias.
- [x] Successor option vocabularies — Decision: clear accepts only the opaque `worktreeSelector`; fork accepts `title`, `reasoningEffort`, opaque `worktreeSelector`, `providerId`, and `modelId`, without client-side selector validation.
- [x] Retry lifecycle — Decision: `retry()` carries only its bound session ID in the existing `RetryStart`; the server selects the eligible failed step, and the SDK returns the same single-consumption `Run` lifecycle, responders, cancellation, and terminal semantics as `run()`.
- [x] Request controls — Decision: domain options and final `RequestOptions` remain separate arguments; every existing unary lifecycle entry plus `run()` and `retry()` accepts request options while automatic session affinity is merged without losing caller headers or signals.
- [x] Projection value semantics — Decision: projections use exact readonly output types, `createdAtUnix` is `bigint`, nested message absence is `undefined`, arrays/records/bytes are detached from transport values, server-authored string vocabularies stay open, and values are not runtime deep-frozen.
- [x] Title normalization — Decision: expose `{ value, provenance, generationState?, latestAttempt?, revision? }`, preferring canonical metadata and using deprecated title fields only as a legacy fallback.
- [x] Capability sharing — Decision: introduce the SDK-owned `ServerCapabilities`, `ManualDreamCapabilities`, and `DreamTargetCapability` projections now and reuse those symbols in the compatibility/server-info work tracked by #1470.
- [x] Response integrity — Decision: missing required payloads or IDs and snapshot/transcript/get/rename/set-mode correlation mismatches throw `ProtocolError`; descriptor guards exempt only deprecated title duplicates and transcript provider-private replay fields.
- [x] Verification breadth — Decision: injected gRPC and HTTP transports cover every added operation's success, options, and affinity; each unary class has a typed-failure proof, and retry cancellation/terminal behavior is proved on both transports.
- [x] Documentation and delivery — Decision: ship TSDoc, API reports, and the sessions-and-runs guide in one implementation PR; #1471 owns runnable examples and cross-surface classification, no ADR is added, and no changelog file is edited because PR #1482 supplies the automated SDK changelog path.

## Interface contract

- **gRPC / protobuf:** None — the SDK consumes the existing `GetSession`, `GetSessionTranscript`, `RenameSession`, `SetMode`, `CompactSession`, `ClearSession`, `ForkSession`, `CloseSession`, `DeleteSession`, and `Converse` descriptors without changing messages, field numbers, routes, or generated output. `retry()` sends the existing `ConverseRequest.retry`/`RetryStart` first frame. The existing raw catalog and `./gen` exports remain intact.
- **Exported Go APIs / interfaces:** None — the plan changes only the TypeScript SDK's hand-written ergonomic layer and documentation; server and engine Go APIs already implement the required behavior.
- **Tool schemas:** None — no model-facing tool name, input schema, result schema, or dispatch behavior changes.
- **CLI / config:** None — no executable flag, environment variable, config key, default, or precedence changes.
- **Events / persistence:** None — no event vocabulary, conversation encoding, session snapshot schema, storage behavior, or migration changes. Snapshot/transcript methods only project existing authoritative server responses.
- **Security / authority:** The SDK adds no authorization or lifecycle policy. Every session-bound request carries the existing representable session-affinity hint while server authentication, ownership, eligibility, state validation, and mutation authority remain definitive. `worktreeSelector` stays opaque and source-scoped; projections expose only the protocol's bounded metadata and never provider-private transcript replay fields, filesystem authority, credentials, or TUI-local presentation state.
- **Compatibility / migration:** This is an additive hand-written SDK minor surface. Existing `Session.attach()`, `Session.activity()`, `Session.resolvePlan()`, `Sessions.list()`, and generated/raw APIs remain unchanged. Unknown returned permission modes and malformed correlations fail closed with `ProtocolError`; absent canonical title metadata may fall back to deprecated title fields, and other absent nested metadata remains `undefined`. Public API reports and user documentation change in the implementation PR; no changelog file changes under the automated release-note workflow in #1482.

The new SDK-owned types are exact and readonly:

```ts
export const SessionMode = {
  Unspecified: 0,
  Default: 1,
  Plan: 2,
  AcceptEdits: 3,
} as const;
export type SessionMode = (typeof SessionMode)[keyof typeof SessionMode];

interface SessionSnapshotLimits {
  readonly maxTurns: number;
  readonly maxToolCalls: number;
  readonly maxConsecutiveFailures: number;
}

interface SessionResolvedModel {
  readonly providerId: string;
  readonly modelId: string;
  readonly contextWindow: bigint;
  readonly reasoningEffort?: string;
}

interface SessionCapabilities {
  readonly image: boolean;
  readonly audio: boolean;
}

interface SessionPlacement {
  readonly kind: string;
  readonly label: string;
  readonly branch: string;
  readonly revision: string;
}

interface SessionRelationship {
  readonly parentSessionId?: string;
  readonly callId?: string;
  readonly branchIndex?: number;
  readonly scheduleName?: string;
  readonly originSessionId?: string;
  readonly teamId?: string;
  readonly memberName?: string;
  readonly debugTargetSessionId?: string;
}

interface SessionTitleAttempt {
  readonly id: string;
  readonly outcome: string;
}

interface SessionTitle {
  readonly value: string;
  readonly provenance: string;
  readonly generationState?: string;
  readonly latestAttempt?: SessionTitleAttempt;
  readonly revision?: bigint;
}

interface SessionTokenUsage {
  readonly total?: EventUsage;
  readonly models: Readonly<Record<string, EventUsage>>;
}

interface DreamTargetCapability {
  readonly generate: boolean;
  readonly decide: boolean;
  readonly unavailableReason?: string;
}

interface ManualDreamCapabilities {
  readonly projectMemory?: DreamTargetCapability;
  readonly userModel?: DreamTargetCapability;
}

interface ServerCapabilities {
  readonly mcp: boolean;
  readonly slashCommands: boolean;
  readonly memory: boolean;
  readonly skills: boolean;
  readonly teams: boolean;
  readonly bash: boolean;
  readonly image: boolean;
  readonly audio: boolean;
  readonly agents: boolean;
  readonly soul: boolean;
  readonly userModel: boolean;
  readonly modelSelection: boolean;
  readonly posture: string;
  readonly worktrees: boolean;
  readonly scheduling: boolean;
  readonly reflection: boolean;
  readonly learningProposals: boolean;
  readonly learnedSkills: boolean;
  readonly manualDream?: ManualDreamCapabilities;
  readonly storageMigration: boolean;
  readonly storageCleanup: boolean;
  readonly storageHealth: boolean;
  readonly steer: boolean;
  readonly manualCompaction: boolean;
  readonly sessionDebug: boolean;
  readonly debugMcp: boolean;
  readonly workspaceEnrollment: boolean;
  readonly mcpConnectorStatus: boolean;
}

interface SessionSnapshot {
  readonly sessionId: string;
  readonly state: string;
  readonly mode: SessionMode;
  readonly limits?: SessionSnapshotLimits;
  readonly turns: number;
  readonly toolCalls: number;
  readonly createdAtUnix: bigint;
  readonly resolvedModel?: SessionResolvedModel;
  readonly capabilities?: ServerCapabilities;
  readonly kind: string;
  readonly relationship?: SessionRelationship;
  readonly debugMcpServers: readonly string[];
  readonly debugMcpTools: readonly string[];
  readonly placement?: SessionPlacement;
  readonly title?: SessionTitle;
  readonly tokenUsage: Readonly<Record<string, SessionTokenUsage>>;
  readonly sessionCapabilities?: SessionCapabilities;
}

interface SessionTranscriptMessage {
  readonly role: string;
  readonly text: string;
  readonly toolCalls: readonly ToolCallEventPayload[];
  readonly toolResult?: ToolResultEventPayload;
  readonly parts: readonly EventContent[];
}

interface SessionActivityReplayStatus {
  readonly available: boolean;
  readonly complete: boolean;
  readonly authoritative: boolean;
}

interface SessionTranscript {
  readonly sessionId: string;
  readonly messages: readonly SessionTranscriptMessage[];
  readonly complete: boolean;
  readonly activity?: SessionActivityReplayStatus;
  readonly kind: string;
  readonly relationship?: SessionRelationship;
}
```

The lifecycle additions and request-option extensions are exact; existing methods
not repeated here retain their current signatures and behavior:

```ts
interface ClearSessionOptions {
  worktreeSelector?: string;
}

interface ForkSessionOptions {
  title?: string;
  reasoningEffort?: string;
  worktreeSelector?: string;
  providerId?: string;
  modelId?: string;
}

interface Session {
  snapshot(options?: RequestOptions): Promise<SessionSnapshot>;
  transcript(options?: RequestOptions): Promise<SessionTranscript>;
  rename(title: string, options?: RequestOptions): Promise<SessionSnapshot>;
  setMode(mode: SessionMode, options?: RequestOptions): Promise<SessionSnapshot>;
  compact(options?: RequestOptions): Promise<boolean>;
  clear(options?: ClearSessionOptions, requestOptions?: RequestOptions): Promise<Session>;
  retry(options?: RunOptions, requestOptions?: RequestOptions): Promise<Run>;
  run(
    prompt: PromptInput,
    options?: RunOptions,
    requestOptions?: RequestOptions,
  ): Promise<Run>;
  close(options?: RequestOptions): Promise<void>;
  delete(options?: RequestOptions): Promise<void>;
}

interface Sessions {
  create(options: CreateSessionOptions, requestOptions?: RequestOptions): Promise<Session>;
  get(sessionId: string, options?: RequestOptions): Promise<Session>;
  fork(
    sourceSessionId: string,
    options?: ForkSessionOptions,
    requestOptions?: RequestOptions,
  ): Promise<Session>;
}
```

`CreateSessionOptions.mode` changes from its anonymous numeric union to
`SessionMode`; this is source-compatible because `SessionMode` denotes the same
four numeric values.

## In scope — 5 scenarios, in implementation order

### Scenario 1 — authoritative inspection has a stable SDK shape

Snapshot and transcript are SDK-owned projections over the existing coherent server
responses, following [ADR 0304](../adr/0304-typescript-sdk-public-surface-and-release.md)
rather than leaking generated message types through the ergonomic entry point.

**Acceptance:**
- AC1.1: `snapshot()` maps every non-deprecated canonical `Session` field, uses canonical title metadata with the approved legacy fallback, preserves absent nested messages as `undefined`, accepts open server strings, converts 64-bit integers to `bigint`, validates `SessionMode`, and returns detached readonly arrays and records.
  - verify: vitest:sdk/typescript/test/session-projections.test.ts#c25hcHNob3QgcHJvamVjdHMgZXZlcnkgY2Fub25pY2FsIHNlc3Npb24gZmllbGQgd2l0aG91dCBhbGlhc2Vz — `sdk/typescript/test/session-projections.test.ts :: "snapshot projects every canonical session field without aliases"`
- AC1.2: `transcript()` returns the ordered authoritative message projection, completeness/activity/kind/relationship metadata, clones nested arrays and bytes, and never exposes `reasoning`, `provider_phase`, or `reasoning_item_id`.
  - verify: vitest:sdk/typescript/test/session-projections.test.ts#dHJhbnNjcmlwdCBvbWl0cyBwcm92aWRlci1wcml2YXRlIHJlcGxheSBzdGF0ZSBhbmQgZGV0YWNoZXMgcHVibGljIGRhdGE — `sdk/typescript/test/session-projections.test.ts :: "transcript omits provider-private replay state and detaches public data"`
- AC1.3: Snapshot/transcript/get/rename/set-mode responses with a missing required payload or ID, an unknown returned mode, or a session correlation different from the requested/bound session fail with local `ProtocolError`.
  - verify: vitest:sdk/typescript/test/session-projections.test.ts#c25hcHNob3QgYW5kIHRyYW5zY3JpcHQgcmVqZWN0IG1hbGZvcm1lZCBvciBtaXNtYXRjaGVkIHJlc3BvbnNlcw — `sdk/typescript/test/session-projections.test.ts :: "snapshot and transcript reject malformed or mismatched responses"`

### Scenario 2 — session mutations return useful lifecycle values

The session handle exposes server-owned operations directly and does not reproduce
server eligibility rules, consistent with the [SDK composition boundary](../design/IMPLEMENTATION-NOTES.md#typescript-sdk--sdktypescript-m1m4-public-v01-surface-adrs-0279-0288-0292-and-0304).

**Acceptance:**
- AC2.1: `rename(title)` and `setMode(mode)` issue their exact existing unary requests and return the validated resulting `SessionSnapshot`; every snapshot-bearing operation refreshes the handle's known per-session media gates when the response carries them, so a later run follows a mode-dependent model change; `compact()` returns the exact `compacted` Boolean.
  - verify: vitest:sdk/typescript/test/session-lifecycle.test.ts#cmVuYW1lIHNldE1vZGUgYW5kIGNvbXBhY3QgbWFwIGxpZmVjeWNsZSByZXN1bHRz — `sdk/typescript/test/session-lifecycle.test.ts :: "rename setMode and compact map lifecycle results"`
- AC2.2: Snapshot, transcript, rename, set-mode, compact, close, and delete preserve caller request controls, merge automatic affinity for a representable session ID, strip a conflicting affinity header for an unrepresentable ID, and surface normalized typed server/transport failures.
  - verify: vitest:sdk/typescript/test/session-lifecycle.test.ts#dW5hcnkgbGlmZWN5Y2xlIGNvbnRyb2xzIHByZXNlcnZlIHJlcXVlc3Qgb3B0aW9ucyBhZmZpbml0eSBhbmQgdHlwZWQgZXJyb3Jz — `sdk/typescript/test/session-lifecycle.test.ts :: "unary lifecycle controls preserve request options affinity and typed errors"`

### Scenario 3 — clear and fork make explicit successors

Clear and fork keep the server's distinct-resource semantics described in
[architecture](../architecture.md); neither operation turns
the source handle into an implicit alias for its successor.

**Acceptance:**
- AC3.1: `session.clear()` returns a new bound `Session` for the non-empty successor ID while the source remains open, usable, and independently closable/deletable; `client.sessions.fork()` behaves likewise and no `session.fork()` alias is exported.
  - verify: vitest:sdk/typescript/test/session-lifecycle.test.ts#Y2xlYXIgYW5kIGZvcmsgcmV0dXJuIHN1Y2Nlc3NvcnMgd2l0aG91dCBpbnZhbGlkYXRpbmcgdGhlIHNvdXJjZQ — `sdk/typescript/test/session-lifecycle.test.ts :: "clear and fork return successors without invalidating the source"`
- AC3.2: Clear maps only an optional opaque `worktreeSelector`; fork maps optional `title`, `reasoningEffort`, opaque `worktreeSelector`, `providerId`, and `modelId` byte-for-byte and performs no client-side worktree validation.
  - verify: vitest:sdk/typescript/test/session-lifecycle.test.ts#Zm9yayBhbmQgY2xlYXIgbWFwIGV4YWN0bHkgdGhlaXIgYXBwcm92ZWQgZG9tYWluIG9wdGlvbnM — `sdk/typescript/test/session-lifecycle.test.ts :: "fork and clear map exactly their approved domain options"`
- AC3.3: `sessions.create`, `sessions.get`, and `sessions.fork` preserve their separate `RequestOptions`, apply reference/source affinity where representable, validate required returned IDs/correlation, and bind available per-session prompt capabilities without treating bounded placement metadata as authority.
  - verify: vitest:sdk/typescript/test/session-lifecycle.test.ts#Y3JlYXRlIGdldCBhbmQgZm9yayBwcmVzZXJ2ZSByZXF1ZXN0IG9wdGlvbnMgYW5kIHZhbGlkYXRlIHJldHVybmVkIGlkcw — `sdk/typescript/test/session-lifecycle.test.ts :: "create get and fork preserve request options and validate returned ids"`

### Scenario 4 — failed-step retry is an ordinary Run

Retry uses the existing prompt-free start arm but joins the same run acceptance and
single-consumption lifecycle defined for ordinary sessions in
[ADR 0304](../adr/0304-typescript-sdk-public-surface-and-release.md).

**Acceptance:**
- AC4.1: `session.retry()` sends one `RetryStart` containing only the bound session ID, waits for the first run-ID-bearing event, validates subsequent session/run correlation, and returns the ordinary `Run` surface without a client-selected failed-step or run ID.
  - verify: vitest:sdk/typescript/test/session-retry.test.ts#cmV0cnkgcmV0dXJucyB0aGUgb3JkaW5hcnkgcnVuIGxpZmVjeWNsZQ — `sdk/typescript/test/session-retry.test.ts :: "retry returns the ordinary run lifecycle"`
- AC4.2: `run()` and `retry()` keep `RunOptions` separate from `RequestOptions`, preserve both responder callbacks, caller signal/headers/deadline, automatic affinity, local one-live-run exclusion, cancellation, single consumption, typed failures, and terminal-result behavior.
  - verify: vitest:sdk/typescript/test/session-retry.test.ts#cnVuIGFuZCByZXRyeSBwcmVzZXJ2ZSByZXNwb25kZXJzIHJlcXVlc3QgY29udHJvbHMgY2FuY2VsbGF0aW9uIGFuZCB0ZXJtaW5hbHM — `sdk/typescript/test/session-retry.test.ts :: "run and retry preserve responders request controls cancellation and terminals"`

### Scenario 5 — parity, drift detection, and public guidance stay together

The implementation remains part of the reviewed public package surface and both raw
transports already classified by [ADR 0304](../adr/0304-typescript-sdk-public-surface-and-release.md).

**Acceptance:**
- AC5.1: Injected gRPC and HTTP test transports each prove success, domain/request option mapping, and affinity for every in-scope operation; both prove retry cancellation/terminal behavior and at least one normalized typed failure for projection reads, snapshot-returning mutations, Boolean/empty acknowledgements, and session/successor acquisition.
  - verify: vitest:sdk/typescript/test/session-lifecycle-transports.test.ts#Ym90aCBpbmplY3RlZCB0cmFuc3BvcnRzIGNvdmVyIHRoZSBsaWZlY3ljbGUgc3VyZmFjZQ — `sdk/typescript/test/session-lifecycle-transports.test.ts :: "both injected transports cover the lifecycle surface"`
- AC5.2: Descriptor-backed guards fail when a canonical `Session`, `ServerCapabilities`, nested capability, or transcript message field is not deliberately mapped; the only explicit field exemptions are deprecated title duplicates and transcript provider-private replay fields.
  - verify: vitest:sdk/typescript/test/session-projections.test.ts#ZGVzY3JpcHRvciBndWFyZHMga2VlcCBsaWZlY3ljbGUgcHJvamVjdGlvbnMgY29tcGxldGU — `sdk/typescript/test/session-projections.test.ts :: "descriptor guards keep lifecycle projections complete"`
- AC5.3: The root entry point exports every approved type/value, both API reports record the surface, TSDoc explains lifecycle/error/option semantics, and `user-docs/building/typescript-sdk/sessions-and-runs.md` documents inspection, mutations, successors, retry, and source-versus-successor lifecycle without adding runnable examples or a changelog edit.
  - verify: vitest:sdk/typescript/test/package.test.ts#dGhlIGxpZmVjeWNsZSBzdXJmYWNlIGlzIGV4cG9ydGVkIGRvY3VtZW50ZWQgYW5kIEFQSSByZXZpZXdlZA — `sdk/typescript/test/package.test.ts :: "the lifecycle surface is exported documented and API reviewed"`

## Out of scope

| Item | Defer-to | Decision |
|---|---|---|
| New or changed server RPCs, protobuf fields, HTTP routes, or Go APIs | none required | Existing raw coverage already supplies the lifecycle operations. |
| Generic execution of mecatui command strings or TUI-local presentation state | never under this story | Reusable workflows are typed SDK operations; application rendering remains application-owned. |
| A `session.fork()` convenience alias | future evidence, if any | Fork remains a `Sessions` namespace operation because it is explicitly source-addressed. |
| Client-side validation of server lifecycle eligibility or worktree selectors | never under this plan | The server remains authoritative; selectors are opaque. |
| Runnable lifecycle examples and full RPC/TUI high-level-surface classification | #1471 | This plan updates only TSDoc, API reports, and the sessions-and-runs guide. |
| Pre-session compatibility/capability query API | #1470 | This plan introduces the shared capability projection only because session snapshots return it. |
| SDK changelog edits | #1482 automated release-note workflow | No changelog file is changed manually. |

## Definition of done

1. `task sdk:lint`, `task sdk:typecheck`, `task sdk:test`, `task sdk:build`, `task sdk:api:check`, and `task docs` pass.
2. `task ac-trace-strict` resolves every named proof when this plan becomes `landed`.
3. Existing raw descriptor/catalog parity and generated freshness gates remain green.
4. The implementation PR links this approved Plan / Interface PR and approved commit and reports projection, lifecycle, transport, affinity, error, API-report, and documentation conformance.
5. `/panel-review` reports no ship blockers or unwaived reviewer failures.

## Deferred decisions and known risks

- New server fields will intentionally fail the descriptor-backed projection guard until the SDK maps or visibly exempts them.
- Runtime deep freezing is deliberately absent; detached transport-owned collections prevent aliasing, while TypeScript readonly declarations express the public mutation contract.
- A future server may add a permission mode outside 0–3; this SDK version fails closed with `ProtocolError` until it deliberately adds the mode.
