# SDK server enablers — acceptance plan

**Phase:** capability — the Go-side server contracts the TypeScript SDK is built on
**Status:** draft
**Issue:** [stacklok/mecatl#821](https://github.com/stacklok/mecatl/issues/821) (parent: [#761](https://github.com/stacklok/mecatl/issues/761)).
**ADRs:** [ADR-0248](../adr/0248-sdk-compatibility-and-error-contract.md) (compatibility discovery + typed errors), [ADR-0249](../adr/0249-durable-run-identity.md) (durable run identity), [ADR-0250](../adr/0250-durable-cursors-and-watch.md) (durable cursors + watch).
**Delivery:** a **linear stack of nine PRs**, `sdk/01-…` → `sdk/09-…`, each independently green on `task lint && task test`, each targeting its predecessor.

This plan covers **only the Go server surface**. No TypeScript is written here — `sdk/typescript/` does not exist at the end of this plan. That cut is deliberate: every invariant risk in #821 lives on this side, where the repo's gates (`task api:check`, the layering DAG, the conformance suites, `docs/lint`) actually have teeth, and every one of these contracts is independently valuable to the existing gRPC and HTTP clients. The SDK follows in a separate plan against a merged, proven server.

The doc is organized scenario-first because acceptance is about what the running harness can demonstrate, not which packages exist on disk.

## Why these scope cuts

- **Nine stacked PRs, not one accumulator.** Six of the nine units edit [`contracts/proto/mecatl/v1/harness.proto`](../../contracts/proto/mecatl/v1/harness.proto) and five edit [`internal/adapter/server/http.go`](../../internal/adapter/server/http.go); `contracts/gen/` is committed generated output, so parallel branches would churn the same generated files. A linear stack serializes those edits for free. Only two real dependencies exist (Scenario 4 → 5, and Scenarios 4+6 → 7); the rest of the ordering is contention management, not logic.
- **`capabilities` and `features` stay separate.** `ServerCapabilities` answers "what has this operator enabled?"; `features` answers "what does this build implement?". Conflating them makes a `--no-bash` deployment look like version skew. See [ADR-0248](../adr/0248-sdk-compatibility-and-error-contract.md).
- **Open strings, not proto enums**, for both `features` and error codes — the discipline [`AGENTS.md`](../../AGENTS.md) already settled for `EvNoProgress`/`StopBudget`. A new feature or error code is a minor SDK release, not a proto change.
- **The run id reuses `RunRequest.AskIDDiscriminator`**, whose [ADR-0044](../adr/0044-host-supplied-askid-discriminator.md) contract is already written in terms of a run id ("A durable host … passes its own RunID") and which nothing supplies today. One identifier, not two with overlapping uniqueness contracts.
- **Redis moves LIST → Stream.** A LIST cannot express a durable cross-process follow; emulating it with `LLEN` polling is strictly worse than the datatype Redis already ships, and positional cursors become a correctness bug the day anyone adds retention. See [ADR-0250](../adr/0250-durable-cursors-and-watch.md).
- **The append-gap guarantee is deliberately weaker than #821 asked for.** Cross-process gap detection is not achievable; the ADR states the residual rather than shipping an absolute that a Redis outage falsifies. #821 explicitly instructs this ("stop and tighten the ADR wording rather than shipping a false guarantee").
- **`mcp_servers` is listener-scoped and server-enforced**, not an SDK-side check. #821 places the "local daemon only" boundary in the client; a client-side check is not enforcement.

## Out of scope

The TypeScript package, `spawn()`/`query()`/`tool()` client implementations, the npm release pipeline, browser test matrices, and the ergonomic event union — all deferred to the SDK plan. Full SSRF hardening of the MCP client URL path (private-IP denial, DNS pinning) beyond the listener scope, deferred. Binary auto-download and Windows local spawn are #821 non-goals.

## In scope — 9 scenarios, in implementation order

Each is independently demoable; later scenarios assume earlier ones but do not change their acceptance criteria. Within each, ACs progress happy path → richer happy path → edges → cross-cutting.

---

### Scenario 1 — `GetCompatibilityInfo` and the compatibility floor

The SDK's first call. One authenticated RPC answers "what is this server?" without creating a probe session. `ServerCapabilities` already exists and is reused verbatim; `features` is new and open-stringed. See [ADR-0248](../adr/0248-sdk-compatibility-and-error-contract.md).

**Work:**
- `contracts/proto`: `GetCompatibilityInfo` on `HarnessService`; `GetCompatibilityInfoResponse{api_major, capabilities, features, deployment}`. Deliberately distinct from ADR 0245's already-shipped `GetServerInfo`, whose privacy boundary excludes capabilities and configuration; build identity stays there as `build_id`.
- `internal/adapter/server`: the gRPC handler, the authenticated `GET /v1/compatibility` peer, and the feature-registry projection.
- `cmd/mecated`: `--deployment-id` (opaque, bounded, empty by default).

**Acceptance:**
- AC1.1: `GetCompatibilityInfo` over gRPC returns `api_major == 1` and the same `ServerCapabilities` a `CreateSession` echo would carry for the same build, without creating a session.
  - verify: `TestSDKServerEnablers_Scenario1_CompatibilityInfoMatchesCapabilities`
- AC1.2: `GET /v1/compatibility` returns the identical projection as the gRPC handler for the same build.
  - verify: `TestSDKServerEnablers_Scenario1_CompatibilityInfoTransportParity`
- AC1.3: `GetCompatibilityInfo` requires authentication — an unauthenticated call is rejected with the same discipline as every other RPC, and is distinguishable from `UNIMPLEMENTED`.
  - verify: `TestSDKServerEnablers_Scenario1_CompatibilityInfoRequiresAuth`
- AC1.4: `features` contains an identifier for every landed enabler in this stack and none for an unlanded one; the registry is the single source of truth for both transports.
  - verify: `TestADR_0244_FeatureRegistryIsSingleSource`
- AC1.5: `capabilities` and `features` are independent: a `--no-bash` server reports `capabilities.bash == false` while its `features` set is unchanged.
  - verify: `TestADR_0244_CapabilitiesAreNotFeatures`
- AC1.6: `deployment` is empty unless `--deployment-id` is set, is length-bounded when set, and is never derived from hostname, pod name, or environment.
  - verify: `TestADR_0244_DeploymentIdentityIsOperatorSetOnly`
- AC1.7: Media capability remains session-authoritative — the `GetCompatibilityInfo` `image`/`audio` hint never overrides the per-session `CreateSessionResponse` echo.
  - verify: `TestInvariant_capability_truth_single_intersection`

---

### Scenario 2 — RFC 9457 problem details and the error-code registry

One stable machine-readable identity per failure, carried identically on both transports. [`internal/adapter/server/http.go`](../../internal/adapter/server/http.go) already funnels every failure through one `writeError`, so this is a chokepoint change plus a classifier — there are no in-repo non-test HTTP consumers to migrate.

**Work:**
- `internal/adapter/server`: the Go error-code registry; `writeError`/`writeServiceError` emitting `application/problem+json`; gRPC status details carrying the same code.

**Acceptance:**
- AC2.1: An HTTP failure returns `application/problem+json` with `type`, `title`, `status`, `detail`, and the stable mecatl code, plus the retained `error` compatibility key.
  - verify: `TestSDKServerEnablers_Scenario2_ProblemJSONShape`
- AC2.2: The same domain failure over gRPC carries the identical code string in a status detail alongside its `codes.Code`.
  - verify: `TestSDKServerEnablers_Scenario2_ErrorCodeTransportParity`
- AC2.3: Every registered code resolves to exactly one HTTP status and one gRPC code; the registry admits no duplicate or unmapped code.
  - verify: `TestADR_0244_ErrorRegistryIsTotalAndUnambiguous`
- AC2.4: No **harness-authored** half of a problem body carries a secret, a credential, a raw tool argument, or a deny-reason body — the structural no-secret guard walks `code`, `title`, and `type` for every registered code. `detail`/`error` carry the server's own `err.Error()` and are bounded and UTF-8-repaired (AC2.6) but deliberately **not** content-scrubbed: sanitising a free-text error would corrupt the diagnostic without being a real control, so the obligation not to put a secret in an error string stays with the backend that raises it, where the sensitive value is actually known.
  - verify: `TestADR_0244_ProblemDetailsCarryNoSecrets`
- AC2.5: A failure with no registered code degrades to a generic code rather than leaking an unmapped internal error string.
  - verify: `TestSDKServerEnablers_Scenario2_UnregisteredFailureDegrades`
- AC2.6: Problem bodies are valid UTF-8 for every producer-influenced string, per the existing mapper discipline.
  - verify: `TestInvariant_tool_result_valid_utf8`

---

### Scenario 3 — Exact-origin CORS

The local-development browser path. Production remains a same-origin BFF.

**Work:**
- `cmd/mecated`: `--cors-origins`. `internal/adapter/server`: exact-match middleware, preflight, `Vary: Origin`.

**Acceptance:**
- AC3.1: A request from an exactly-allowed origin receives that origin echoed in `Access-Control-Allow-Origin` and `Vary: Origin`.
  - verify: `TestSDKServerEnablers_Scenario3_ExactOriginAllowed`
- AC3.2: A non-allowed origin — including a suffix, prefix, or scheme/port variant of an allowed one — receives no CORS grant.
  - verify: `TestSDKServerEnablers_Scenario3_NearMissOriginsRejected`
- AC3.3: Credentials are permitted only for an exactly allowed origin; wildcard-with-credentials is never emitted under any configuration.
  - verify: `TestADR_0244_NoWildcardWithCredentials`
- AC3.4: A preflight `OPTIONS` returns the correct allowed methods and headers for the route and does not invoke the handler.
  - verify: `TestSDKServerEnablers_Scenario3_PreflightDoesNotInvokeHandler`
- AC3.5: With no `--cors-origins`, behaviour is byte-identical to today — no CORS headers on any response.
  - verify: `TestSDKServerEnablers_Scenario3_DefaultOffUnchanged`

---

### Scenario 4 — Durable run identity

A stable, opaque, host-minted handle for one run, persisted across restart. See [ADR-0249](../adr/0249-durable-run-identity.md).

**Work:**
- `engine/session`: `Event.RunID`; the aggregate accessor/mutator. `engine/agent`: `RunRequest.RunID` + the `Run.emit`/`emitOrAbort` stamp + ask-discriminator derivation. `engine/adapter/sessnap`: `Snapshot.RunID` (`omitempty`).
- `internal/adapter/server`: minting in `StartRunContent`, reuse in `resumeFromAwaiting` and `ApprovePlan`, the closed run-less set.
- `engine/api/*.txt` + `engine/CHANGELOG.md`: Added/minor entries.

**Acceptance:**
- AC4.1: Every event of a run reaching the relay carries the same non-empty `RunID`; two consecutive runs of one session carry different ones.
  - verify: `TestSDKServerEnablers_Scenario4_EveryRunEventCarriesOneID`
- AC4.2: The run id is supplied to the engine as `RunRequest.RunID`, and the engine derives the ask discriminator from it when `AskIDDiscriminator` is empty — so askIDs minted during the run embed it and are reconstructable across processes, from ONE host-set field.
  - verify: `TestADR_0245_RunIDIsTheAskDiscriminator`
- AC4.3: Every event a run emits carries the run id, stamped by the loop at `Run.emit`/`emitOrAbort` beside the existing `Seq` stamp — so no relay, transport, or persistence path can omit it. A run with no supplied id emits an empty one, byte-identical to prior behaviour.
  - verify: `TestADR_0245_LoopStampsEveryEmittedEvent`
- AC4.4: A session parked `awaiting` across a process restart resumes as **the same run** — the resume path reuses the persisted id and mints nothing.
  - verify: `TestADR_0245_AwaitingResumeKeepsRunID`
- AC4.5: `ApprovePlan` reuses the id for the resumed run and mints a distinct one for the continuation run.
  - verify: `TestSDKServerEnablers_Scenario4_PlanResolutionSpansTwoRunIDs`
- AC4.6: An empty `RunID` at the append chokepoint is legal for exactly the three `schedule.*` types and fails CI for any other type.
  - verify: `TestADR_0245_RunlessEventSetIsClosed`
- AC4.7: `eventsource.Fold` ignores `RunID`; a folded session is byte-identical to one folded before this scenario landed.
  - verify: `TestADR_0245_FoldIgnoresRunID`
- AC4.8: A legacy snapshot with no `run_id` key restores with an empty id and is stamped on the next run; no migration sweep runs.
  - verify: `TestSDKServerEnablers_Scenario4_LegacySnapshotRestoresEmpty`

---

### Scenario 5 — `expected_run_id` and strict steer

Stale controls fail instead of landing on a newer run. Closes a real current bug, not only an SDK gap.

**Work:**
- `contracts/proto`: optional `expected_run_id` on approve, cancel, and steer.
- `internal/adapter/server`: the guard on each control path; strict HTTP steer.

**Acceptance:**
- AC5.1: A control carrying the active run's id succeeds exactly as before.
  - verify: `TestSDKServerEnablers_Scenario5_MatchingExpectedRunIDSucceeds`
- AC5.2: A control carrying a stale run id fails with a typed error and leaves the newer run untouched — no verdict applied, no cancellation, no steer enqueued.
  - verify: `TestADR_0245_StaleControlCannotTouchNewerRun`
- AC5.3: A control omitting `expected_run_id` behaves exactly as today, so `mecatui` is unaffected.
  - verify: `TestSDKServerEnablers_Scenario5_OmittedExpectedRunIDUnchanged`
- AC5.4: A strict steer is never promoted into a new run; a late steer reports its outcome rather than starting one.
  - verify: `TestADR_0245_StrictSteerNeverPromotes`
- AC5.5: Existing steer promotion behaviour is preserved on the explicit non-strict session API.
  - verify: `TestSDKServerEnablers_Scenario5_PromotionRetainedOnRawAPI`

---

### Scenario 6 — `port.CursorEventLog` and the four backends

The storage seam. Additive to `port.EventLog`, which is untouched. See [ADR-0250](../adr/0250-durable-cursors-and-watch.md).

**Work:**
- `engine/port`: `CursorEventLog` (append-with-cursor, read-after, generation validation) + `engine/api/*.txt` + `engine/CHANGELOG.md`.
- `internal/adapter/redisstore`: LIST → Stream migration, including the `eventsKey` references in the delete/rebuild Lua scripts.
- `internal/adapter/store/jsonlstore`: byte-offset cursors + size-polled follow.
- `engine/adapter/memstore`, `internal/adapter/grpcdriver`: the remaining two implementations.
- A shared conformance suite covering all four.

**Acceptance:**
- AC6.1: All four backends satisfy one shared conformance suite for ordered, at-least-once, resumable delivery.
  - verify: `TestSDKServerEnablers_Scenario6_CursorConformanceAllBackends`
- AC6.2: Existing `port.EventLog` behaviour is unchanged for every backend — the additive port breaks no consumer.
  - verify: `TestADR_0246_EventLogContractUnbroken`
- AC6.3: A cursor from a prior log generation yields `CursorExpiredError`, never silent degradation or wrong data.
  - verify: `TestADR_0246_StaleGenerationCursorExpires`
- AC6.4: A tampered or malformed cursor is rejected, never coerced to a position.
  - verify: `TestADR_0246_TamperedCursorRejected`
- AC6.5: A watcher in a second process observes durable appends made by the first — the cross-process obligation, proved over Redis and JSONL.
  - verify: `TestADR_0246_CrossProcessWatchObservesAppends`
- AC6.6: Existing Redis LIST event logs are readable after the Stream migration; no session loses its history.
  - verify: `TestSDKServerEnablers_Scenario6_LegacyRedisListMigrates`
- AC6.7: A gap marker occupies an append position and advances cursors, is surfaced by `ReadAfter`, and is **skipped** by the legacy `EventLog.Read`.
  - verify: `TestADR_0246_GapMarkerIsEnvelopeNotEvent`
- AC6.8: `session.Event` and the proto `Event` message gain no gap-related field; the event kind-parity surface is unchanged.
  - verify: `TestADR_0246_GapAddsNoEventKind`

---

### Scenario 7 — `WatchSessionEvents`

The transport over the cursor seam. Depends on Scenarios 4 and 6.

**Work:**
- `contracts/proto`: `WatchSessionEvents` returning `{event, cursor, phase}`.
- `internal/adapter/server`: the gRPC handler, the SSE route, ownership checks, run filtering, replay→live phase transition, bounded delivery state.

**Acceptance:**
- AC7.1: A watch from the beginning replays the durable log in append order, transitions to `live`, and follows through the run's terminal result.
  - verify: `TestSDKServerEnablers_Scenario7_ReplayThenFollow`
- AC7.2: A watch resumed from a cursor delivers exactly the events after it, with no gap and no reordering.
  - verify: `TestSDKServerEnablers_Scenario7_ResumeFromCursorHasNoGap`
- AC7.3: gRPC and SSE deliver the same envelopes in the same order for the same session.
  - verify: `TestSDKServerEnablers_Scenario7_WatchTransportParity`
- AC7.4: A watch is authenticated and ownership-checked; a caller who may not read the session is refused.
  - verify: `TestSDKServerEnablers_Scenario7_WatchOwnershipEnforced`
- AC7.5: A slow watcher is terminated with a **resumable** error and never backpressures the run; the run completes normally.
  - verify: `TestADR_0246_SlowWatcherTerminatesWithoutBackpressure`
- AC7.6: A durable append failure terminates watchers **in that process** with `ActivityGapError` without advancing their cursor, and the owned run continues.
  - verify: `TestADR_0246_AppendFailureTerminatesLocalWatchers`
- AC7.7: The best-effort durable gap marker, when it lands, is observed by watchers in a **second** process.
  - verify: `TestADR_0246_GapMarkerObservedCrossProcess`
- AC7.8: Exactly one append occurs per event; cursor assignment happens at the persistence chokepoint, not at the emit site.
  - verify: `TestADR_0246_OneAppendPerEvent`
- AC7.9: The existing `StreamSessionEvents` and `StreamSessionLive` endpoints behave identically to today.
  - verify: `TestSDKServerEnablers_Scenario7_LegacyStreamEndpointsUnchanged`

---

### Scenario 8 — Daemon hosting: UDS, HTTP-disable, ready file, lifetime pipe

What a spawned local daemon needs. No proto change; `cmd/mecated` only.

**Work:**
- `cmd/mecated`: `--grpc-unix-socket` (mutually exclusive with TCP gRPC listen), empty HTTP address to disable HTTP, `--ready-file`, an inherited lifetime pipe, stale-socket cleanup, socket permissions, Darwin path-length handling.
- `docs/usage/` + `user-docs/`: every new flag.

**Acceptance:**
- AC8.1: `--grpc-unix-socket` serves gRPC on the socket and opens **no** TCP port; combining it with TCP gRPC configuration is rejected at startup.
  - verify: `TestSDKServerEnablers_Scenario8_UDSOnlyOpensNoPort`
- AC8.2: An empty HTTP address disables the HTTP and metrics listeners entirely.
  - verify: `TestSDKServerEnablers_Scenario8_HTTPDisabled`
- AC8.3: `--ready-file` is written **atomically** and only after composition and listeners are ready; it contains PID, transport, socket path, and non-secret server-info data.
  - verify: `TestSDKServerEnablers_Scenario8_ReadyFileAtomicAndLate`
- AC8.4: The ready file, startup logs, and startup errors contain no credential or secret-shaped value.
  - verify: `TestSDKServerEnablers_Scenario8_ReadinessCarriesNoSecrets`
- AC8.5: EOF on the inherited lifetime pipe gracefully stops the daemon — the parent-crash path.
  - verify: `TestSDKServerEnablers_Scenario8_LifetimePipeEOFStops`
- AC8.6: A stale socket from a dead process is cleaned up on start; a socket held by a live process is not.
  - verify: `TestSDKServerEnablers_Scenario8_StaleSocketCleanup`
- AC8.7: The socket is created with owner-only permissions.
  - verify: `TestSDKServerEnablers_Scenario8_SocketPermissionsOwnerOnly`

---

### Scenario 9 — Listener-scoped `mcp_servers` on session creation

The server-side half of callback tools. The boundary is enforced by the listener, not by a well-behaved client. Depends on Scenario 8.

**Work:**
- `contracts/proto` + HTTP: `mcp_servers` on session creation, routed through the existing `Service.CreateSessionWithMCP` and the ACP `partitionClientMCP` / [`mcp.ValidateClientURL`](../../internal/adapter/mcp/mcp.go) validation.
- `internal/adapter/server`: the listener scope, per [ADR-0237](../adr/0237-listener-scoped-workspace-authority.md).

**Acceptance:**
- AC9.1: A session created over a **UDS** listener with `mcp_servers` mounts them and reaches their tools.
  - verify: `TestSDKServerEnablers_Scenario9_UDSSessionMountsMCPServers`
- AC9.2: The same request over a **TCP** listener is refused with a typed unsupported-feature error — the boundary holds against a client that does not implement the SDK's check.
  - verify: `TestADR_0244_McpServersRejectedOnTCPListener`
- AC9.3: `mcp_servers_on_create` appears in `GetCompatibilityInfo.features` only on a listener that permits it.
  - verify: `TestADR_0244_ListenerScopedFeatureAdvertisement`
- AC9.4: A stdio entry and an sse entry are hard-rejected on every listener — the no-stdio invariant is unchanged.
  - verify: `TestInvariant_no_stdio_mcp_ever`
- AC9.5: Header values on a mounted MCP server are never logged, never projected into an event, and never appear in an error.
  - verify: `TestSDKServerEnablers_Scenario9_McpHeadersNeverLogged`
- AC9.6: The wire path and the ACP path share one validation helper — there is no second, divergent validator.
  - verify: `TestSDKServerEnablers_Scenario9_SingleMCPValidationPath`

---

## Cross-cutting gates

- `task lint && task test` green on every PR in the stack, and on the assembled stack head.
- `task api:check` — Scenarios 4 and 6 change the engine public API; each ships `task api:update` output plus an `engine/CHANGELOG.md` entry classified per [`engine/COMPATIBILITY.md`](../../engine/COMPATIBILITY.md) (both **Added = minor**).
- `task generate` after every proto change; `contracts/gen/` committed, never hand-edited.
- `task docs` after every Markdown change — `llms.txt` is generated and the strict link gate must pass.
- `go run ./cmd/mecademo` still prints a full offline session.
- Every long-lived resource added here — each watcher, each size-poll ticker, each spawned-daemon lifetime pipe — gets a row in [ADR-0027](../adr/0027-cloud-native.md)'s resource inventory (owner / scope / cleanup / re-attach), and a rehydrate-fidelity row where it holds state a restart would lose.
- `user-docs/` updated in the same PR for every operator-visible flag: `--deployment-id`, `--cors-origins`, `--grpc-unix-socket`, `--ready-file`.
