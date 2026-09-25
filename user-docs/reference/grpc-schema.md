---
title: gRPC schema reference
description: Look up Mecatl gRPC services, RPCs, messages, enums, and fields.
sidebar_position: 3
hide_table_of_contents: true
---

import GrpcDescription from '@site/src/components/GrpcDescription';

# gRPC schema reference

{/* GENERATED FILE. DO NOT EDIT. Run `task docs:grpc-ref` after changing the source protobuf files. */}

This reference is generated from
[`contracts/proto/mecatl/v1/harness.proto`](https://github.com/stacklok/mecatl/blob/main/contracts/proto/mecatl/v1/harness.proto)
and
[`contracts/proto/mecatl/v1/schedule.proto`](https://github.com/stacklok/mecatl/blob/main/contracts/proto/mecatl/v1/schedule.proto).
For behavior, authentication, status codes, and operational guidance, see the
[gRPC API reference](/reference/grpc-api.md). For help choosing between gRPC and
HTTP/SSE, see [Connect with gRPC or HTTP](/building/deployment/grpc-http.md).

## Services


### `mecatl.v1.HarnessService`

HarnessService is the mecatl API: unary session setup/inspection plus
the bidi Converse stream that drives one agent run.

<div className="grpc-service-table">

| RPC details | Client streaming | Server streaming | Description |
|---|---|---|---|
| **RPC:** `GetCompatibilityInfo`<br />**Request:** `GetCompatibilityInfoRequest`<br />**Response:** `GetCompatibilityInfoResponse` | <span className="grpc-mobile-label">Client streaming</span>No | <span className="grpc-mobile-label">Server streaming</span>No | <span className="grpc-mobile-label">Description</span><GrpcDescription name="GetCompatibilityInfo">GetCompatibilityInfo returns the deployment&#39;s compatibility descriptor: the API major, the operator-enabled ServerCapabilities, the build&#39;s supported feature identifiers, and an optional operator-set deployment label. It is the FIRST call a client makes — it answers &#34;what may I do with this server?&#34; WITHOUT creating a probe session (the ServerCapabilities echo otherwise rides CreateSessionResponse only).  DISTINCT FROM GetServerInfo below, deliberately. That RPC answers &#34;which BUILD is this?&#34; and ADR 0245 draws an explicit privacy boundary around it: its response must never carry capabilities, configuration, or auth details. This one is exactly those things — negotiation input, not identity — so folding the two would either breach that boundary or overload one message with two audiences. Build identity therefore lives ONLY on GetServerInfo, and a client that wants both makes both calls.  A server that does not implement this RPC (UNIMPLEMENTED) is below the SDK compatibility floor; a client fails loudly rather than inferring a legacy mode. Authenticated like every other RPC, so UNAUTHENTICATED and UNIMPLEMENTED stay distinguishable. See ADR 0248.</GrpcDescription> |
| **RPC:** `CreateSession`<br />**Request:** `CreateSessionRequest`<br />**Response:** `CreateSessionResponse` | <span className="grpc-mobile-label">Client streaming</span>No | <span className="grpc-mobile-label">Server streaming</span>No | <span className="grpc-mobile-label">Description</span><GrpcDescription name="CreateSession">CreateSession allocates a new server-side session and returns its id.</GrpcDescription> |
| **RPC:** `GetServerInfo`<br />**Request:** `GetServerInfoRequest`<br />**Response:** `GetServerInfoResponse` | <span className="grpc-mobile-label">Client streaming</span>No | <span className="grpc-mobile-label">Server streaming</span>No | <span className="grpc-mobile-label">Description</span><GrpcDescription name="GetServerInfo">GetServerInfo returns only the composed server build identity. It is authenticated like every HarnessService operation and does not inspect configuration or state.</GrpcDescription> |
| **RPC:** `GetSession`<br />**Request:** `GetSessionRequest`<br />**Response:** `GetSessionResponse` | <span className="grpc-mobile-label">Client streaming</span>No | <span className="grpc-mobile-label">Server streaming</span>No | <span className="grpc-mobile-label">Description</span><GrpcDescription name="GetSession">GetSession returns a snapshot of an existing session.</GrpcDescription> |
| **RPC:** `ListGuardrailCoverage`<br />**Request:** `ListGuardrailCoverageRequest`<br />**Response:** `ListGuardrailCoverageResponse` | <span className="grpc-mobile-label">Client streaming</span>No | <span className="grpc-mobile-label">Server streaming</span>No |  |
| **RPC:** `GetGuardrailReviewDetail`<br />**Request:** `GetGuardrailReviewDetailRequest`<br />**Response:** `GetGuardrailReviewDetailResponse` | <span className="grpc-mobile-label">Client streaming</span>No | <span className="grpc-mobile-label">Server streaming</span>No |  |
| **RPC:** `UploadArtifact`<br />**Request:** `UploadArtifactRequest`<br />**Response:** `UploadArtifactResponse` | <span className="grpc-mobile-label">Client streaming</span>Yes | <span className="grpc-mobile-label">Server streaming</span>No | <span className="grpc-mobile-label">Description</span><GrpcDescription name="UploadArtifact">UploadArtifact stages a session-owned artifact from one metadata frame followed by bounded nonempty byte chunks. Only PDF is supported in v1.</GrpcDescription> |
| **RPC:** `DownloadArtifact`<br />**Request:** `DownloadArtifactRequest`<br />**Response:** `DownloadArtifactResponse` | <span className="grpc-mobile-label">Client streaming</span>No | <span className="grpc-mobile-label">Server streaming</span>Yes | <span className="grpc-mobile-label">Description</span><GrpcDescription name="DownloadArtifact">DownloadArtifact streams one previously published, owned artifact.</GrpcDescription> |
| **RPC:** `GetSessionTranscript`<br />**Request:** `GetSessionTranscriptRequest`<br />**Response:** `GetSessionTranscriptResponse` | <span className="grpc-mobile-label">Client streaming</span>No | <span className="grpc-mobile-label">Server streaming</span>No | <span className="grpc-mobile-label">Description</span><GrpcDescription name="GetSessionTranscript">GetSessionTranscript returns the authoritative, snapshot-derived human transcript for one owned session. It is read-only and does not use EventLog.</GrpcDescription> |
| **RPC:** `SetMode`<br />**Request:** `SetModeRequest`<br />**Response:** `SetModeResponse` | <span className="grpc-mobile-label">Client streaming</span>No | <span className="grpc-mobile-label">Server streaming</span>No | <span className="grpc-mobile-label">Description</span><GrpcDescription name="SetMode">SetMode changes an existing session&#39;s permission posture. The session aggregate remains authoritative: a mid-turn change is rejected with InvalidArgument, so clients that want &#34;next prompt&#34; semantics must defer and retry once idle.</GrpcDescription> |
| **RPC:** `CloseSession`<br />**Request:** `CloseSessionRequest`<br />**Response:** `CloseSessionResponse` | <span className="grpc-mobile-label">Client streaming</span>No | <span className="grpc-mobile-label">Server streaming</span>No | <span className="grpc-mobile-label">Description</span><GrpcDescription name="CloseSession">CloseSession ends a session and releases its server-side resources (learned permission rules, bound placement, and any per-session engine). Idempotent: closing an unknown or already-closed session via the wire returns NotFound only for a never-created id; an already-released session succeeds.</GrpcDescription> |
| **RPC:** `RenameSession`<br />**Request:** `RenameSessionRequest`<br />**Response:** `RenameSessionResponse` | <span className="grpc-mobile-label">Client streaming</span>No | <span className="grpc-mobile-label">Server streaming</span>No | <span className="grpc-mobile-label">Description</span><GrpcDescription name="RenameSession">RenameSession explicitly replaces an idle main session&#39;s title.</GrpcDescription> |
| **RPC:** `DeleteSession`<br />**Request:** `DeleteSessionRequest`<br />**Response:** `DeleteSessionResponse` | <span className="grpc-mobile-label">Client streaming</span>No | <span className="grpc-mobile-label">Server streaming</span>No | <span className="grpc-mobile-label">Description</span><GrpcDescription name="DeleteSession">DeleteSession physically removes an idle main session and its sidecars.</GrpcDescription> |
| **RPC:** `CompactSession`<br />**Request:** `CompactSessionRequest`<br />**Response:** `CompactSessionResponse` | <span className="grpc-mobile-label">Client streaming</span>No | <span className="grpc-mobile-label">Server streaming</span>No | <span className="grpc-mobile-label">Description</span><GrpcDescription name="CompactSession">CompactSession applies one manual compaction pass to an owned main-chat session at a turn boundary. It creates no model turn.</GrpcDescription> |
| **RPC:** `ClearSession`<br />**Request:** `ClearSessionRequest`<br />**Response:** `ClearSessionResponse` | <span className="grpc-mobile-label">Client streaming</span>No | <span className="grpc-mobile-label">Server streaming</span>No | <span className="grpc-mobile-label">Description</span><GrpcDescription name="ClearSession">ClearSession creates a distinct empty-history successor that inherits the source session&#39;s placement unless a fresh source-scoped selector is supplied.</GrpcDescription> |
| **RPC:** `ForkSession`<br />**Request:** `ForkSessionRequest`<br />**Response:** `ForkSessionResponse` | <span className="grpc-mobile-label">Client streaming</span>No | <span className="grpc-mobile-label">Server streaming</span>No | <span className="grpc-mobile-label">Description</span><GrpcDescription name="ForkSession">ForkSession creates a history-carrying successor with optional placement and model-routing overrides.</GrpcDescription> |
| **RPC:** `Converse`<br />**Request:** `ConverseRequest`<br />**Response:** `ConverseResponse` | <span className="grpc-mobile-label">Client streaming</span>Yes | <span className="grpc-mobile-label">Server streaming</span>Yes | <span className="grpc-mobile-label">Description</span><GrpcDescription name="Converse">Converse drives one run. The first frame MUST be `prompt` or `retry`; later frames may carry controls. A received second `prompt` or `retry` is rejected with INVALID_ARGUMENT. The server streams `Event` envelopes until either a terminal `result` or a pending `authorization.required` park, then closes the stream; controls still in transit may instead observe normal stream completion. A context cancel from the client aborts the run.</GrpcDescription> |
| **RPC:** `ResolveRunAsk`<br />**Request:** `ResolveRunAskRequest`<br />**Response:** `ResolveRunAskResponse` | <span className="grpc-mobile-label">Client streaming</span>No | <span className="grpc-mobile-label">Server streaming</span>No | <span className="grpc-mobile-label">Description</span><GrpcDescription name="ResolveRunAsk">ResolveRunAsk resolves one ordinary permission ask on the exact addressed run without opening or owning its event stream.</GrpcDescription> |
| **RPC:** `ResolvePlanAsk`<br />**Request:** `ResolvePlanAskRequest`<br />**Response:** `ResolvePlanAskResponse` | <span className="grpc-mobile-label">Client streaming</span>No | <span className="grpc-mobile-label">Server streaming</span>No | <span className="grpc-mobile-label">Description</span><GrpcDescription name="ResolvePlanAsk">ResolvePlanAsk acknowledges a verdict for one exact plan-originated ask.</GrpcDescription> |
| **RPC:** `CancelRun`<br />**Request:** `CancelRunRequest`<br />**Response:** `CancelRunResponse` | <span className="grpc-mobile-label">Client streaming</span>No | <span className="grpc-mobile-label">Server streaming</span>No | <span className="grpc-mobile-label">Description</span><GrpcDescription name="CancelRun">CancelRun cancels the exact addressed live run without opening Converse.</GrpcDescription> |
| **RPC:** `SteerRun`<br />**Request:** `SteerRunRequest`<br />**Response:** `SteerRunResponse` | <span className="grpc-mobile-label">Client streaming</span>No | <span className="grpc-mobile-label">Server streaming</span>No | <span className="grpc-mobile-label">Description</span><GrpcDescription name="SteerRun">SteerRun injects an instruction into the exact addressed live run. Unlike the legacy Converse control, it never promotes a late steer to a successor.</GrpcDescription> |
| **RPC:** `CancelRunSteer`<br />**Request:** `CancelRunSteerRequest`<br />**Response:** `CancelRunSteerResponse` | <span className="grpc-mobile-label">Client streaming</span>No | <span className="grpc-mobile-label">Server streaming</span>No | <span className="grpc-mobile-label">Description</span><GrpcDescription name="CancelRunSteer">CancelRunSteer retracts the pending steer on the exact addressed live run.</GrpcDescription> |
| **RPC:** `ListMcpResources`<br />**Request:** `ListMcpResourcesRequest`<br />**Response:** `ListMcpResourcesResponse` | <span className="grpc-mobile-label">Client streaming</span>No | <span className="grpc-mobile-label">Server streaming</span>No | <span className="grpc-mobile-label">Description</span><GrpcDescription name="ListMcpResources">ListMcpResources returns the static resource snapshots advertised by the connected MCP servers. An empty `server` returns the union across every server; a specific name returns just that server&#39;s. Catalog-level inspection: unrelated to any in-flight Converse run.</GrpcDescription> |
| **RPC:** `ReadMcpResource`<br />**Request:** `ReadMcpResourceRequest`<br />**Response:** `ReadMcpResourceResponse` | <span className="grpc-mobile-label">Client streaming</span>No | <span className="grpc-mobile-label">Server streaming</span>No | <span className="grpc-mobile-label">Description</span><GrpcDescription name="ReadMcpResource">ReadMcpResource reads a single resource by URI from the named server.</GrpcDescription> |
| **RPC:** `ListMcpPrompts`<br />**Request:** `ListMcpPromptsRequest`<br />**Response:** `ListMcpPromptsResponse` | <span className="grpc-mobile-label">Client streaming</span>No | <span className="grpc-mobile-label">Server streaming</span>No | <span className="grpc-mobile-label">Description</span><GrpcDescription name="ListMcpPrompts">ListMcpPrompts returns the static prompt snapshots advertised by the connected MCP servers. An empty `server` returns the union across every server.</GrpcDescription> |
| **RPC:** `GetMcpPrompt`<br />**Request:** `GetMcpPromptRequest`<br />**Response:** `GetMcpPromptResponse` | <span className="grpc-mobile-label">Client streaming</span>No | <span className="grpc-mobile-label">Server streaming</span>No | <span className="grpc-mobile-label">Description</span><GrpcDescription name="GetMcpPrompt">GetMcpPrompt expands a named prompt with the given arguments on the named server and returns the rendered messages.</GrpcDescription> |
| **RPC:** `ListMcpSources`<br />**Request:** `ListMcpSourcesRequest`<br />**Response:** `ListMcpSourcesResponse` | <span className="grpc-mobile-label">Client streaming</span>No | <span className="grpc-mobile-label">Server streaming</span>No | <span className="grpc-mobile-label">Description</span><GrpcDescription name="ListMcpSources">ListMcpSources returns the cached published/pre-shadow source inventory and reconciler status. It performs no independent upstream probe.</GrpcDescription> |
| **RPC:** `RefreshMcpSources`<br />**Request:** `RefreshMcpSourcesRequest`<br />**Response:** `RefreshMcpSourcesResponse` | <span className="grpc-mobile-label">Client streaming</span>No | <span className="grpc-mobile-label">Server streaming</span>No | <span className="grpc-mobile-label">Description</span><GrpcDescription name="RefreshMcpSources">RefreshMcpSources explicitly reconciles direct MCP sources for an owned eligible ordinary-root session and unions newly active direct names.</GrpcDescription> |
| **RPC:** `ListSessionMcpConnectors`<br />**Request:** `ListSessionMcpConnectorsRequest`<br />**Response:** `ListSessionMcpConnectorsResponse` | <span className="grpc-mobile-label">Client streaming</span>No | <span className="grpc-mobile-label">Server streaming</span>No | <span className="grpc-mobile-label">Description</span><GrpcDescription name="ListSessionMcpConnectors">ListSessionMcpConnectors inspects the owned session&#39;s broker-local catalogue. This read neither probes upstreams nor progresses enrollment.</GrpcDescription> |
| **RPC:** `ListToolHiveGroups`<br />**Request:** `ListToolHiveGroupsRequest`<br />**Response:** `ListToolHiveGroupsResponse` | <span className="grpc-mobile-label">Client streaming</span>No | <span className="grpc-mobile-label">Server streaming</span>No | <span className="grpc-mobile-label">Description</span><GrpcDescription name="ListToolHiveGroups">ListToolHiveGroups returns the distinct, non-empty ToolHive groups present in the resolved source inventory. Derived from the snapshot — it does NOT call ToolHive.</GrpcDescription> |
| **RPC:** `ListAgents`<br />**Request:** `ListAgentsRequest`<br />**Response:** `ListAgentsResponse` | <span className="grpc-mobile-label">Client streaming</span>No | <span className="grpc-mobile-label">Server streaming</span>No | <span className="grpc-mobile-label">Description</span><GrpcDescription name="ListAgents">ListAgents returns the resolved agent-definition registry snapshot: each discovered agent def&#39;s routing metadata (name/description), its resolved model, its effective read-only tool scope, permission mode, and UX color. Derived from the snapshot taken at startup; it performs no live discovery.</GrpcDescription> |
| **RPC:** `ListCommands`<br />**Request:** `ListCommandsRequest`<br />**Response:** `ListCommandsResponse` | <span className="grpc-mobile-label">Client streaming</span>No | <span className="grpc-mobile-label">Server streaming</span>No | <span className="grpc-mobile-label">Description</span><GrpcDescription name="ListCommands">ListCommands returns slash commands discovered for one owned session&#39;s exact server-bound placement. It authorizes and reattaches that session before discovery and accepts no workspace/root input. It powers the client&#39;s command palette; command expansion remains a run-path concern. A no-FS session or a server with no command expander returns an empty list.</GrpcDescription> |
| **RPC:** `ListWorktrees`<br />**Request:** `ListWorktreesRequest`<br />**Response:** `ListWorktreesResponse` | <span className="grpc-mobile-label">Client streaming</span>No | <span className="grpc-mobile-label">Server streaming</span>No | <span className="grpc-mobile-label">Description</span><GrpcDescription name="ListWorktrees">ListWorktrees discovers eligible alternatives for one owned source session&#39;s exactly reattached placement. Results carry bounded display metadata and an opaque source-scoped selector accepted only by ClearSession or ForkSession. The server re-enumerates and matches current choices on use; selectors are not paths, are not persisted, and expire on restart. A no-FS session or a server with no worktree lister returns an empty list.</GrpcDescription> |
| **RPC:** `StreamSessionEvents`<br />**Request:** `StreamSessionEventsRequest`<br />**Response:** `Event` | <span className="grpc-mobile-label">Client streaming</span>No | <span className="grpc-mobile-label">Server streaming</span>Yes | <span className="grpc-mobile-label">Description</span><GrpcDescription name="StreamSessionEvents">StreamSessionEvents replays a session&#39;s durable event log as a server stream of `Event` envelopes (cloud-native Phase 3a read-back). It is the client-tier surface over the SAME `port.EventLog.Read` the operator-tier 3c EventLogService.Read serves; the loop itself stays storage-agnostic (it only emits — persistence lives at the relay, never in `engine/agent`). The log stores ALREADY-REDACTED events and inherits the stream&#39;s redaction, so this path adds none (gauntlet #7: no raw args/deny-reason bodies/child content ever cross).  UNLIKE the live `Converse` relay — which SKIPS the three log-only event kinds (`approval`/`compaction_archive`/`user_prompt`) on the client wire because they are persistence-only — StreamSessionEvents relays ALL events, including those three: a client opening a PAST session wants the verdicts and user prompts, as they ARE the transcript. They are metadata-only/redacted by construction, so no extra filter applies here.  Read-only: no live model call, no mutation, no Converse run is started. An unknown session id yields an EMPTY stream (absence is data — a never-created or pruned id is indistinguishable, by design). A server with NO durable EventLog wired returns `UNIMPLEMENTED` (HTTP 501). There is intentionally NO ServerCapabilities bit for this feature: the capability is RPC-discoverable (UNIMPLEMENTED vs. an empty stream degrade honestly).</GrpcDescription> |
| **RPC:** `StreamSessionLive`<br />**Request:** `StreamSessionLiveRequest`<br />**Response:** `Event` | <span className="grpc-mobile-label">Client streaming</span>No | <span className="grpc-mobile-label">Server streaming</span>Yes | <span className="grpc-mobile-label">Description</span><GrpcDescription name="StreamSessionLive">StreamSessionLive is the LIVE per-session event stream (ADR 0075 fire-result-delivery Scenario 6 / Wave 3): a server-streaming RPC backed by the in-process per-session subscription registry (Service.Subscribe / PublishSessionEvent, List 1 row 35). It is the UNIFIED transport for BOTH embedded and remote clients — the embedded mecatui dials its in-process server over a real gRPC UNIX socket, so the in-process Subscribe registry is unreachable from the TUI without a wire transport; this RPC is that transport, and it serves a remote mecated identically (ONE proto, ONE TUI consumption path).  UNLIKE StreamSessionEvents (the durable-log READ-BACK, which relays ALL events including the three log-only kinds), StreamSessionLive is a LIVE stream and applies the SAME live-wire relay discipline as the live `Converse` relay, with ONE narrow exception: a fire-result delivery note (an EvUserPrompt whose text starts with the renderFireDelivery provenance header &#34;[scheduled task &#34;) is RELAYED so the connected client renders the delivery card as it happens (AC6.2). The other two log-only kinds (EvApproval, EvCompactionArchive) stay SKIPPED on the live wire (they are persistence-only; a client holds its own verdict/compaction view); a non-delivery EvUserPrompt stays SKIPPED too (the client already holds its own prompt). The delivery note is metadata-only/redacted by construction (gauntlet #7 — it is the fenced-untrusted harness note the engine recorded).  The stream stays open until the client cancels or disconnects. The handler is drain-to-discard: a dead client never wedges the delivery run (a full subscriber channel drops the event, AC6.3). The durable log records the tail regardless (it is appended by the relay/loop, independent of this stream). There is intentionally NO ServerCapabilities bit — the capability is RPC-discoverable (UNIMPLEMENTED on an older server, an empty stream on a server with no subscribers). An unknown session id yields an empty stream (absence is data — a never-created id is indistinguishable from a session with no live events yet).</GrpcDescription> |
| **RPC:** `WatchSessionEvents`<br />**Request:** `WatchSessionEventsRequest`<br />**Response:** `WatchSessionEventsResponse` | <span className="grpc-mobile-label">Client streaming</span>No | <span className="grpc-mobile-label">Server streaming</span>Yes | <span className="grpc-mobile-label">Description</span><GrpcDescription name="WatchSessionEvents">WatchSessionEvents is the DURABLE replay-then-follow stream: one operation that replays a session&#39;s durable event log from a position, transitions to live, and keeps following as the run appends (issue #821, ADR 0250).  It exists because neither existing read path can do this. StreamSessionEvents is a complete, ordered replay with NO position and NO follow — it reads the whole log and stops, so a client that wants to catch up and then watch must read everything and THEN subscribe, and any event appended between those two steps is silently lost. StreamSessionLive is live but process-local, in-memory, and non-durable: a second replica sees nothing, a reconnecting tab sees nothing that happened while it was away, and a slow subscriber has its events DROPPED. Both remain, unchanged, for the callers that want exactly their semantics.  CURSOR. `cursor` is an OPAQUE resume token meaning &#34;I have durably received everything up to here&#34;. Treat it as bytes to hand back — never parse, build, or edit one. An EMPTY cursor means THE BEGINNING OF THE LOG, which is a real value and the common case (a client attaching for the first time wants replay-then-follow). A cursor from a superseded log generation returns `cursor_expired` (the log was deleted and recreated); one that cannot be decoded returns `cursor_malformed`. Neither is ever coerced to a position: resuming from approximately the right place is indistinguishable from resuming from the right place until data is already lost.  PHASE is an OPEN STRING, not an enum — `replay`, `live`, `gap` today, and a client must tolerate a value it does not know (the same discipline the event `type`/`stop` fields already carry). `replay` records were already durable when the watch attached; `live` records arrived after it caught up. Exactly one PHASE-ONLY `live` frame (no `event`, cursor set) marks the replay to live boundary, so a client can render the transcript and then show a live view WITHOUT waiting for the next event to arrive — on an idle session that event may never come. `gap` marks a position where a durable append is KNOWN to have failed; it too carries no `event`, because a gap is a fact about DELIVERY rather than something that happened in the run (ADR 0250 decision 5 — this is why neither `session.Event` nor the `Event` message gains a gap field).  RELAY DISCIPLINE mirrors StreamSessionEvents, NOT the live wire: this is the READ-BACK of the durable log, so it relays ALL events including the three log-only kinds (`approval`/`compaction_archive`/`user_prompt`) — a client replaying a session wants the verdicts and prompts, as they ARE the transcript. They are metadata-only/redacted by construction.  `run_id` optionally narrows delivery to ONE run (ADR 0249). Gap frames are delivered regardless of the filter: a failed append leaves nothing to attribute to a run, so suppressing it would hide a real gap.  TERMINATION. A watch ends when the client cancels, or with an error that says what to do next. A client that falls too far behind is TERMINATED with `watch_lagging` rather than having its events dropped — the cursor exists precisely so that termination is recoverable: reconnect with the last cursor received and nothing is lost. A durable append failure terminates the watchers in that process with `activity_gap` and never advances their cursor. In BOTH cases the run continues normally: a broken or slow watch must never break a live run, and a watcher never backpressures one.  A server with no durable EventLog returns `no_event_log`; one whose log does not implement the cursor seam returns `watch_unsupported` — never a silent degrade to replaying the whole transcript, because a client asking to resume from a position and being handed everything is a correctness problem dressed as a performance one. Ownership is enforced exactly as on GetSession.</GrpcDescription> |
| **RPC:** `GetMcpAuthorizationPresentation`<br />**Request:** `GetMcpAuthorizationPresentationRequest`<br />**Response:** `GetMcpAuthorizationPresentationResponse` | <span className="grpc-mobile-label">Client streaming</span>No | <span className="grpc-mobile-label">Server streaming</span>No | <span className="grpc-mobile-label">Description</span><GrpcDescription name="GetMcpAuthorizationPresentation">GetMcpAuthorizationPresentation returns the live browser URL for one owned, still-pending authorization. The request carries correlation only; the URL is returned only by this live control and never enters an Event or snapshot.</GrpcDescription> |
| **RPC:** `RecheckMcpAuthorization`<br />**Request:** `RecheckMcpAuthorizationRequest`<br />**Response:** `RecheckMcpAuthorizationResponse` | <span className="grpc-mobile-label">Client streaming</span>Yes | <span className="grpc-mobile-label">Server streaming</span>Yes | <span className="grpc-mobile-label">Description</span><GrpcDescription name="RecheckMcpAuthorization">RecheckMcpAuthorization is a bidirectional authorization-control stream. Its first frame MUST carry owned session/authorization correlation; subsequent frames may resolve permission asks or cancel the continuation.</GrpcDescription> |
| **RPC:** `CancelMcpAuthorization`<br />**Request:** `CancelMcpAuthorizationRequest`<br />**Response:** `CancelMcpAuthorizationResponse` | <span className="grpc-mobile-label">Client streaming</span>Yes | <span className="grpc-mobile-label">Server streaming</span>Yes | <span className="grpc-mobile-label">Description</span><GrpcDescription name="CancelMcpAuthorization">CancelMcpAuthorization is the cancellation counterpart with the same first-frame and continuation-control grammar.</GrpcDescription> |
| **RPC:** `ListSessions`<br />**Request:** `ListSessionsRequest`<br />**Response:** `ListSessionsResponse` | <span className="grpc-mobile-label">Client streaming</span>No | <span className="grpc-mobile-label">Server streaming</span>No | <span className="grpc-mobile-label">Description</span><GrpcDescription name="ListSessions">ListSessions returns the stored-session inventory — the picker metadata a client renders to let an operator open an EXISTING session by id (issue #245 Phase 1). It is backed by `port.PrunableStore.List` (type-asserted on the configured store); a store that does not implement `PrunableStore`, or one that returns `ErrPruneUnsupported`, degrades to an EMPTY list — never an error — so a no-persistence/cloud server honestly reports &#34;no sessions&#34;. Each row carries only picker metadata (id, timestamps, state, turn count, model id); NO conversation content is loaded. Rows are sorted most-recently-active first (modified_at descending). Read-only. There is intentionally NO ServerCapabilities bit — see StreamSessionEvents for the rationale.</GrpcDescription> |
| **RPC:** `GetStorageHealth`<br />**Request:** `GetStorageHealthRequest`<br />**Response:** `GetStorageHealthResponse` | <span className="grpc-mobile-label">Client streaming</span>No | <span className="grpc-mobile-label">Server streaming</span>No | <span className="grpc-mobile-label">Description</span><GrpcDescription name="GetStorageHealth">GetStorageHealth returns authenticated, content-free aggregate storage status. It never returns session ids, owners, paths, or transcript content.</GrpcDescription> |
| **RPC:** `PlanSessionCleanup`<br />**Request:** `PlanSessionCleanupRequest`<br />**Response:** `PlanSessionCleanupResponse` | <span className="grpc-mobile-label">Client streaming</span>No | <span className="grpc-mobile-label">Server streaming</span>No | <span className="grpc-mobile-label">Description</span><GrpcDescription name="PlanSessionCleanup">Session cleanup is an authenticated plan/apply maintenance workflow. Plan is read-only; apply requires its caller-bound opaque confirmation token.</GrpcDescription> |
| **RPC:** `ApplySessionCleanup`<br />**Request:** `ApplySessionCleanupRequest`<br />**Response:** `CleanupJob` | <span className="grpc-mobile-label">Client streaming</span>No | <span className="grpc-mobile-label">Server streaming</span>No |  |
| **RPC:** `CancelSessionCleanup`<br />**Request:** `CancelSessionCleanupRequest`<br />**Response:** `CleanupJob` | <span className="grpc-mobile-label">Client streaming</span>No | <span className="grpc-mobile-label">Server streaming</span>No |  |
| **RPC:** `GetSessionCleanupJob`<br />**Request:** `GetSessionCleanupJobRequest`<br />**Response:** `CleanupJob` | <span className="grpc-mobile-label">Client streaming</span>No | <span className="grpc-mobile-label">Server streaming</span>No |  |
| **RPC:** `ListSkills`<br />**Request:** `ListSkillsRequest`<br />**Response:** `ListSkillsResponse` | <span className="grpc-mobile-label">Client streaming</span>No | <span className="grpc-mobile-label">Server streaming</span>No | <span className="grpc-mobile-label">Description</span><GrpcDescription name="ListSkills">ListSkills returns the resolved skills inventory snapshot: each discovered skill&#39;s name + one-line description. Derived from the snapshot taken at startup (skills are discovered once at build time and immutable for the process lifetime); it performs no live discovery. Metadata only — activating a skill remains a run-path concern (the Skill tool reads the body).</GrpcDescription> |
| **RPC:** `GetSoul`<br />**Request:** `GetSoulRequest`<br />**Response:** `GetSoulResponse` | <span className="grpc-mobile-label">Client streaming</span>No | <span className="grpc-mobile-label">Server streaming</span>No | <span className="grpc-mobile-label">Description</span><GrpcDescription name="GetSoul">GetSoul returns the resolved soul (user-scoped persona) BUILD-TIME SNAPSHOT: the selected soul&#39;s content + size/hash + provenance (user/project) + trust and drift state. The soul is selected once at startup (USER-wins precedence, project trust gate, drift check — issue #14 Phase 3 Items 1+2), so this is a pure read of that snapshot, never a live re-read. Read-only inspection: it never mutates the soul (the agent-read-only invariant).</GrpcDescription> |
| **RPC:** `GetUserModel`<br />**Request:** `GetUserModelRequest`<br />**Response:** `GetUserModelResponse` | <span className="grpc-mobile-label">Client streaming</span>No | <span className="grpc-mobile-label">Server streaming</span>No | <span className="grpc-mobile-label">Description</span><GrpcDescription name="GetUserModel">GetUserModel returns the CURRENT user-model entries (durable FACTS about the operator): each entry&#39;s key + one-line description, plus an aggregate size + hash. Unlike GetSoul (a startup snapshot) this is a LIVE read of the user-model store&#39;s index, so it reflects entries the agent has saved since startup. Metadata only — the per-entry values are omitted (Recall loads them).</GrpcDescription> |
| **RPC:** `ReflectSession`<br />**Request:** `ReflectSessionRequest`<br />**Response:** `ReflectSessionResponse` | <span className="grpc-mobile-label">Client streaming</span>No | <span className="grpc-mobile-label">Server streaming</span>No | <span className="grpc-mobile-label">Description</span><GrpcDescription name="ReflectSession">ReflectSession explicitly reflects one completed, caller-owned session. It remains available when automatic learning is off if reflection is configured.</GrpcDescription> |
| **RPC:** `GetLearningAttempt`<br />**Request:** `GetLearningAttemptRequest`<br />**Response:** `GetLearningAttemptResponse` | <span className="grpc-mobile-label">Client streaming</span>No | <span className="grpc-mobile-label">Server streaming</span>No | <span className="grpc-mobile-label">Description</span><GrpcDescription name="GetLearningAttempt">Learning attempts expose content-free lifecycle metadata from only the verified caller&#39;s private partition. Pagination cursors are opaque IDs.</GrpcDescription> |
| **RPC:** `ListLearningAttempts`<br />**Request:** `ListLearningAttemptsRequest`<br />**Response:** `ListLearningAttemptsResponse` | <span className="grpc-mobile-label">Client streaming</span>No | <span className="grpc-mobile-label">Server streaming</span>No |  |
| **RPC:** `RetryLearningAttempt`<br />**Request:** `MutateLearningAttemptRequest`<br />**Response:** `MutateLearningAttemptResponse` | <span className="grpc-mobile-label">Client streaming</span>No | <span className="grpc-mobile-label">Server streaming</span>No | <span className="grpc-mobile-label">Description</span><GrpcDescription name="RetryLearningAttempt">Retry and abandon are opaque-version CAS controls over only the attempt. Abandon is non-compensating and does not promise downstream rollback.</GrpcDescription> |
| **RPC:** `AbandonLearningAttempt`<br />**Request:** `MutateLearningAttemptRequest`<br />**Response:** `MutateLearningAttemptResponse` | <span className="grpc-mobile-label">Client streaming</span>No | <span className="grpc-mobile-label">Server streaming</span>No |  |
| **RPC:** `GenerateDreamPlan`<br />**Request:** `GenerateDreamPlanRequest`<br />**Response:** `GenerateDreamPlanResponse` | <span className="grpc-mobile-label">Client streaming</span>No | <span className="grpc-mobile-label">Server streaming</span>No | <span className="grpc-mobile-label">Description</span><GrpcDescription name="GenerateDreamPlan">GenerateDreamPlan creates a bounded-lifetime manual consolidation review for one closed, deployment-owned target. The request carries no mutation material. Unavailable deployments return Unimplemented (HTTP parity: 501).</GrpcDescription> |
| **RPC:** `DecideDreamPlan`<br />**Request:** `DecideDreamPlanRequest`<br />**Response:** `DecideDreamPlanResponse` | <span className="grpc-mobile-label">Client streaming</span>No | <span className="grpc-mobile-label">Server streaming</span>No | <span className="grpc-mobile-label">Description</span><GrpcDescription name="DecideDreamPlan">DecideDreamPlan applies or dismisses the exact retained plan identified by plan_id. The caller cannot replace any operation or memory content.</GrpcDescription> |
| **RPC:** `ListLearningProposals`<br />**Request:** `ListLearningProposalsRequest`<br />**Response:** `ListLearningProposalsResponse` | <span className="grpc-mobile-label">Client streaming</span>No | <span className="grpc-mobile-label">Server streaming</span>No | <span className="grpc-mobile-label">Description</span><GrpcDescription name="ListLearningProposals">ListLearningProposals returns a bounded, caller/project-partitioned page.</GrpcDescription> |
| **RPC:** `GetLearningProposal`<br />**Request:** `GetLearningProposalRequest`<br />**Response:** `GetLearningProposalResponse` | <span className="grpc-mobile-label">Client streaming</span>No | <span className="grpc-mobile-label">Server streaming</span>No | <span className="grpc-mobile-label">Description</span><GrpcDescription name="GetLearningProposal">GetLearningProposal returns one proposal with digest-verified evidence status.</GrpcDescription> |
| **RPC:** `DecideLearningProposal`<br />**Request:** `DecideLearningProposalRequest`<br />**Response:** `DecideLearningProposalResponse` | <span className="grpc-mobile-label">Client streaming</span>No | <span className="grpc-mobile-label">Server streaming</span>No | <span className="grpc-mobile-label">Description</span><GrpcDescription name="DecideLearningProposal">DecideLearningProposal approves or rejects a staged proposal using CAS.</GrpcDescription> |
| **RPC:** `UndoLearningPromotion`<br />**Request:** `UndoLearningPromotionRequest`<br />**Response:** `UndoLearningPromotionResponse` | <span className="grpc-mobile-label">Client streaming</span>No | <span className="grpc-mobile-label">Server streaming</span>No | <span className="grpc-mobile-label">Description</span><GrpcDescription name="UndoLearningPromotion">UndoLearningPromotion commits a compensating revision only while the promoted revision is still current, also guarded by proposal-version CAS.</GrpcDescription> |
| **RPC:** `ListLearnedSkills`<br />**Request:** `ListLearnedSkillsRequest`<br />**Response:** `ListLearnedSkillsResponse` | <span className="grpc-mobile-label">Client streaming</span>No | <span className="grpc-mobile-label">Server streaming</span>No | <span className="grpc-mobile-label">Description</span><GrpcDescription name="ListLearnedSkills">Learned-skill lifecycle inspection and CAS mutations. These methods are caller/project partitioned and are unimplemented when no lifecycle store exists.</GrpcDescription> |
| **RPC:** `GetLearnedSkill`<br />**Request:** `GetLearnedSkillRequest`<br />**Response:** `GetLearnedSkillResponse` | <span className="grpc-mobile-label">Client streaming</span>No | <span className="grpc-mobile-label">Server streaming</span>No |  |
| **RPC:** `DiffLearnedSkillVersions`<br />**Request:** `DiffLearnedSkillVersionsRequest`<br />**Response:** `DiffLearnedSkillVersionsResponse` | <span className="grpc-mobile-label">Client streaming</span>No | <span className="grpc-mobile-label">Server streaming</span>No |  |
| **RPC:** `ActivateLearnedSkill`<br />**Request:** `MutateLearnedSkillRequest`<br />**Response:** `MutateLearnedSkillResponse` | <span className="grpc-mobile-label">Client streaming</span>No | <span className="grpc-mobile-label">Server streaming</span>No |  |
| **RPC:** `RejectLearnedSkill`<br />**Request:** `MutateLearnedSkillRequest`<br />**Response:** `MutateLearnedSkillResponse` | <span className="grpc-mobile-label">Client streaming</span>No | <span className="grpc-mobile-label">Server streaming</span>No |  |
| **RPC:** `ArchiveLearnedSkill`<br />**Request:** `MutateLearnedSkillRequest`<br />**Response:** `MutateLearnedSkillResponse` | <span className="grpc-mobile-label">Client streaming</span>No | <span className="grpc-mobile-label">Server streaming</span>No |  |
| **RPC:** `RollbackLearnedSkill`<br />**Request:** `RollbackLearnedSkillRequest`<br />**Response:** `MutateLearnedSkillResponse` | <span className="grpc-mobile-label">Client streaming</span>No | <span className="grpc-mobile-label">Server streaming</span>No |  |
| **RPC:** `ListSkillChanges`<br />**Request:** `ListSkillChangesRequest`<br />**Response:** `ListSkillChangesResponse` | <span className="grpc-mobile-label">Client streaming</span>No | <span className="grpc-mobile-label">Server streaming</span>No |  |
| **RPC:** `ListModels`<br />**Request:** `ListModelsRequest`<br />**Response:** `ListModelsResponse` | <span className="grpc-mobile-label">Client streaming</span>No | <span className="grpc-mobile-label">Server streaming</span>No | <span className="grpc-mobile-label">Description</span><GrpcDescription name="ListModels">ListModels returns the selectable-model inventory: every AVAILABLE provider&#39;s catalog models, projected to public metadata only (no secrets). It powers the client /models picker (multi-provider Phase 0, S3). Derived from the build-time registry+catalog snapshot; it performs no live discovery and never reveals an unavailable provider.</GrpcDescription> |
| **RPC:** `CreateTeam`<br />**Request:** `CreateTeamRequest`<br />**Response:** `CreateTeamResponse` | <span className="grpc-mobile-label">Client streaming</span>No | <span className="grpc-mobile-label">Server streaming</span>No | <span className="grpc-mobile-label">Description</span><GrpcDescription name="CreateTeam">CreateTeam allocates a new agent team and returns its id, optionally enrolling an initial roster in the same atomic call. Members may also be added afterwards with SpawnTeammate; the team is then driven with RunTeam.</GrpcDescription> |
| **RPC:** `SpawnTeammate`<br />**Request:** `SpawnTeammateRequest`<br />**Response:** `SpawnTeammateResponse` | <span className="grpc-mobile-label">Client streaming</span>No | <span className="grpc-mobile-label">Server streaming</span>No | <span className="grpc-mobile-label">Description</span><GrpcDescription name="SpawnTeammate">SpawnTeammate enrols a member in an existing team (before RunTeam). A read-only member shares the base workspace; a mutating member runs in an isolated fork so parallel writes are safe.</GrpcDescription> |
| **RPC:** `SendTeammateMessage`<br />**Request:** `SendTeammateMessageRequest`<br />**Response:** `SendTeammateMessageResponse` | <span className="grpc-mobile-label">Client streaming</span>No | <span className="grpc-mobile-label">Server streaming</span>No | <span className="grpc-mobile-label">Description</span><GrpcDescription name="SendTeammateMessage">SendTeammateMessage posts a message into a member&#39;s inbox (e.g. from the operator), delivered at that member&#39;s next turn boundary.</GrpcDescription> |
| **RPC:** `CancelTeammate`<br />**Request:** `CancelTeammateRequest`<br />**Response:** `CancelTeammateResponse` | <span className="grpc-mobile-label">Client streaming</span>No | <span className="grpc-mobile-label">Server streaming</span>No | <span className="grpc-mobile-label">Description</span><GrpcDescription name="CancelTeammate">CancelTeammate cancels ONE member of a RUNNING team mid-round (the headless RunTeam-path analogue of the Converse-path CancelChild frame). The member de-schedules with the cancelled stop reason, its claimed tasks release back to pending, and the team run continues — the lead still synthesises and the RunTeam stream still delivers its terminal outcome frame. A team that is not running (created but never run, or already done) is rejected with FailedPrecondition; an unknown member name with NotFound.</GrpcDescription> |
| **RPC:** `RunTeam`<br />**Request:** `RunTeamRequest`<br />**Response:** `TeamEvent` | <span className="grpc-mobile-label">Client streaming</span>No | <span className="grpc-mobile-label">Server streaming</span>Yes | <span className="grpc-mobile-label">Description</span><GrpcDescription name="RunTeam">RunTeam drives the team to quiescence, streaming every member&#39;s events — each tagged with the member name — until the team finishes. The stream ends with a single terminal frame carrying TeamEvent.outcome (rounds, stop, budget_exhausted, usage, dispositions, findings); no member events follow it.</GrpcDescription> |
| **RPC:** `ListTeam`<br />**Request:** `ListTeamRequest`<br />**Response:** `ListTeamResponse` | <span className="grpc-mobile-label">Client streaming</span>No | <span className="grpc-mobile-label">Server streaming</span>No | <span className="grpc-mobile-label">Description</span><GrpcDescription name="ListTeam">ListTeam returns a snapshot of the team roster, the shared task list, and whether the team has reached quiescence.</GrpcDescription> |
| **RPC:** `CleanupTeam`<br />**Request:** `CleanupTeamRequest`<br />**Response:** `CleanupTeamResponse` | <span className="grpc-mobile-label">Client streaming</span>No | <span className="grpc-mobile-label">Server streaming</span>No | <span className="grpc-mobile-label">Description</span><GrpcDescription name="CleanupTeam">CleanupTeam tears down a finished team and releases its resources.</GrpcDescription> |
| **RPC:** `ApprovePlan`<br />**Request:** `ApprovePlanRequest`<br />**Response:** `Event` | <span className="grpc-mobile-label">Client streaming</span>No | <span className="grpc-mobile-label">Server streaming</span>Yes | <span className="grpc-mobile-label">Description</span><GrpcDescription name="ApprovePlan">ApprovePlan atomically resolves a parked plan-approval ask (a PresentPlan call surfaced in plan mode, issue #206) and — on an ALLOW verdict — starts a fresh continuation run carrying the harness proceed message, streaming BOTH the resumed run&#39;s and the continuation run&#39;s events on the one response stream. It composes EXISTING seams (Service.resumeFromAwaiting + StartRunContent over the engine&#39;s ResumeApproval): the resumed run applies the verdict, sets the plan-approved target mode, terminates with StopPlanApproved, and flips the session mode at the terminal boundary; the continuation run then drives the model on the FLIPPED mode (default / accept-edits) with the proceed message as ordinary recorded history.  target_mode → verdict mapping: - PERMISSION_MODE_DEFAULT → allow-once (the session flips to DEFAULT). - PERMISSION_MODE_ACCEPT_EDITS → allow-always (the session flips to ACCEPT_EDITS). - PERMISSION_MODE_PLAN / UNSPECIFIED → deny (the run stays in plan mode; no continuation run starts — the model iterates on the plan on the next prompt).  `note` is an OPTIONAL operator remark appended to the proceed message injected into the continuation run&#39;s first user message (allow paths only).  Preconditions (else FailedPrecondition): the session must NOT have a live in-flight run (an approve mid-run is rejected — use the Converse ResumeApproval frame for a live run), and it MUST be in the awaiting state parked on a PLAN-ORIGINATED ask (not a generic tool-permission ask). An unknown session yields NotFound. The stream ends with the terminal `result` event of whichever run ran last (the continuation run on an allow path; the resumed run on a deny path).</GrpcDescription> |
| **RPC:** `ConnectWorkspaceServices`<br />**Request:** `WorkspaceEnrollmentConnectRequest`<br />**Response:** `WorkspaceEnrollment` | <span className="grpc-mobile-label">Client streaming</span>No | <span className="grpc-mobile-label">Server streaming</span>No | <span className="grpc-mobile-label">Description</span><GrpcDescription name="ConnectWorkspaceServices">Workspace enrollment is a client-owned pre-prompt bundle. These unary controls carry only whole-bundle correlation; no backend selector exists.</GrpcDescription> |
| **RPC:** `RetryWorkspaceEnrollment`<br />**Request:** `WorkspaceEnrollmentControlRequest`<br />**Response:** `WorkspaceEnrollment` | <span className="grpc-mobile-label">Client streaming</span>No | <span className="grpc-mobile-label">Server streaming</span>No |  |
| **RPC:** `CancelWorkspaceEnrollment`<br />**Request:** `WorkspaceEnrollmentControlRequest`<br />**Response:** `WorkspaceEnrollment` | <span className="grpc-mobile-label">Client streaming</span>No | <span className="grpc-mobile-label">Server streaming</span>No |  |

</div>


### `mecatl.v1.ScheduleService`

ScheduleService is the scheduler API: a durable registry of named schedules
that fire agent runs on a configured cadence. A schedule&#39;s fires are
at-most-once across replicas (the claim-before-fire contract — see
ScheduleSpec / ScheduleState). The service is OPTIONAL: a backend that does
not implement port.ScheduleStore answers ErrScheduleUnsupported and
composition disables scheduling for the process (byte-identical no-schedule
path).

<div className="grpc-service-table">

| RPC details | Client streaming | Server streaming | Description |
|---|---|---|---|
| **RPC:** `CreateSchedule`<br />**Request:** `CreateScheduleRequest`<br />**Response:** `CreateScheduleResponse` | <span className="grpc-mobile-label">Client streaming</span>No | <span className="grpc-mobile-label">Server streaming</span>No | <span className="grpc-mobile-label">Description</span><GrpcDescription name="CreateSchedule">CreateSchedule saves a new schedule (an upsert by name) and returns the created aggregate. The create-seam (composition) validates the spec — the exactly-one-of(cron, one_shot) trigger XOR, the prompt-or-parts non-empty rule, the cron grammar — fail-closed; the proto carries per-field constraints only; cross-field rules and exact server-owned placement binding are server-enforced.</GrpcDescription> |
| **RPC:** `GetSchedule`<br />**Request:** `GetScheduleRequest`<br />**Response:** `GetScheduleResponse` | <span className="grpc-mobile-label">Client streaming</span>No | <span className="grpc-mobile-label">Server streaming</span>No | <span className="grpc-mobile-label">Description</span><GrpcDescription name="GetSchedule">GetSchedule returns the schedule stored under name (spec + state).</GrpcDescription> |
| **RPC:** `ListSchedules`<br />**Request:** `ListSchedulesRequest`<br />**Response:** `ListSchedulesResponse` | <span className="grpc-mobile-label">Client streaming</span>No | <span className="grpc-mobile-label">Server streaming</span>No | <span className="grpc-mobile-label">Description</span><GrpcDescription name="ListSchedules">ListSchedules returns all stored schedules, in no guaranteed order.</GrpcDescription> |
| **RPC:** `UpdateSchedule`<br />**Request:** `UpdateScheduleRequest`<br />**Response:** `UpdateScheduleResponse` | <span className="grpc-mobile-label">Client streaming</span>No | <span className="grpc-mobile-label">Server streaming</span>No | <span className="grpc-mobile-label">Description</span><GrpcDescription name="UpdateSchedule">UpdateSchedule updates an existing schedule&#39;s spec (the state half is preserved on overwrite — call Delete + Create to reset firing progress).</GrpcDescription> |
| **RPC:** `DeleteSchedule`<br />**Request:** `DeleteScheduleRequest`<br />**Response:** `DeleteScheduleResponse` | <span className="grpc-mobile-label">Client streaming</span>No | <span className="grpc-mobile-label">Server streaming</span>No | <span className="grpc-mobile-label">Description</span><GrpcDescription name="DeleteSchedule">DeleteSchedule removes the schedule stored under name. Idempotent: deleting an unknown name is success.</GrpcDescription> |
| **RPC:** `FireNow`<br />**Request:** `FireNowRequest`<br />**Response:** `FireNowResponse` | <span className="grpc-mobile-label">Client streaming</span>No | <span className="grpc-mobile-label">Server streaming</span>No | <span className="grpc-mobile-label">Description</span><GrpcDescription name="FireNow">FireNow forces an immediate fire of the schedule, returning the per-fire session id (fire_id) the caller may poll via GetFire / the session APIs. It bypasses the due check but STILL claims the slot (at-most-once): a concurrent FireNow on the same schedule is serialised by the caller.</GrpcDescription> |
| **RPC:** `PauseSchedule`<br />**Request:** `PauseScheduleRequest`<br />**Response:** `PauseScheduleResponse` | <span className="grpc-mobile-label">Client streaming</span>No | <span className="grpc-mobile-label">Server streaming</span>No | <span className="grpc-mobile-label">Description</span><GrpcDescription name="PauseSchedule">PauseSchedule disables a schedule without deleting it (Enabled=false). A disabled schedule is excluded from Due even if NextFireAt is in the past.</GrpcDescription> |
| **RPC:** `ResumeSchedule`<br />**Request:** `ResumeScheduleRequest`<br />**Response:** `ResumeScheduleResponse` | <span className="grpc-mobile-label">Client streaming</span>No | <span className="grpc-mobile-label">Server streaming</span>No | <span className="grpc-mobile-label">Description</span><GrpcDescription name="ResumeSchedule">ResumeSchedule re-enables a paused schedule (Enabled=true). It does NOT recompute NextFireAt — a schedule paused past its next fire resumes via the misfire policy on the next tick.</GrpcDescription> |
| **RPC:** `GetFire`<br />**Request:** `GetFireRequest`<br />**Response:** `GetFireResponse` | <span className="grpc-mobile-label">Client streaming</span>No | <span className="grpc-mobile-label">Server streaming</span>No | <span className="grpc-mobile-label">Description</span><GrpcDescription name="GetFire">GetFire returns the fire record stored under fire_id (the pull-only result-delivery read path for v1).</GrpcDescription> |
| **RPC:** `ListFires`<br />**Request:** `ListFiresRequest`<br />**Response:** `ListFiresResponse` | <span className="grpc-mobile-label">Client streaming</span>No | <span className="grpc-mobile-label">Server streaming</span>No | <span className="grpc-mobile-label">Description</span><GrpcDescription name="ListFires">ListFires returns the fire records for a schedule, in no guaranteed order.</GrpcDescription> |

</div>


## Messages and enums


### `mecatl/v1/harness.proto`




#### `mecatl.v1.ActivityReplayStatus`

ActivityReplayStatus describes the optional EventLog activity plane. It is
deliberately independent of transcript completeness: EventLog append can gap,
read can fail, and EOF carries no completeness attestation.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `available` | `bool` |  |  | available reports whether this deployment configured an EventLog reader. |
| `complete` | `bool` |  |  | complete is false in v1 because EventLog has no completeness attestation. |
| `authoritative` | `bool` |  |  | authoritative is always false: activity never attests model context. |




#### `mecatl.v1.AgentInfo`

AgentInfo is the inventory for one resolved agent definition. It mirrors the
resolved registry snapshot (see internal/adapter/agents.AgentDef and the
composition-layer resolution in internal/app): the model and tools fields are
the RESOLVED outcome (model alias resolved, tool scope filtered to the
read-only Task call site), not the raw frontmatter.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `name` | `string` |  |  | name is the def&#39;s stable routing identifier (the value passed to Task&#39;s `agent` arg). |
| `description` | `string` |  |  | description is the always-in-context one-line routing summary. |
| `model` | `string` |  |  | model is the resolved provider model id (empty means &#34;inherit the parent&#34;). |
| `tools` | `string` | repeated |  | tools is the def&#39;s effective read-only tool scope at the Task call site. |
| `permission_mode` | `string` |  |  | permission_mode is the raw frontmatter permission mode (empty / &#34;default&#34;). |
| `color` | `string` |  |  | color is the optional UX hint; it never affects execution. |




#### `mecatl.v1.ApplySessionCleanupRequest`



| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `confirmation_token` | `string` |  |  |  |




#### `mecatl.v1.Approval`

Approval is the proto projection of session.ApprovalPayload (EvApproval):
the verdict half of a permission ask. Metadata-only (gauntlet #7): tool NAME +
verdict enum + askID + the opaque gated-call id. NEVER raw args. LOG-ONLY on the live Converse wire; surfaced only by the
StreamSessionEvents replay.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `ask_id` | `string` |  |  | ask_id is the id of the resolved permission ask (mirrors PermissionAsk.ask_id). |
| `tool` | `string` |  |  | tool is the NAME of the tool the ask gated. It is the tool name ALONE — never the call&#39;s args. |
| `call_id` | `string` |  |  | call_id is the opaque id of the gated ToolCall (the durable, grammar-free correlation handle a 3b consumer uses to find the call in the conversation). |
| `verdict` | `ApprovalVerdict` |  |  | verdict is the typed resolution of the permission ask. |
| `origin` | `string` |  |  | origin is the explicit approval provenance. Empty/unknown origins fail closed. |




#### `mecatl.v1.ApprovePlanRequest`

ApprovePlanRequest resolves a parked plan-approval ask (issue #206). See the
ApprovePlan rpc doc above for the target_mode → verdict mapping and the
atomic resume + continuation contract.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `session_id` | `string` |  |  | session_id is the session parked awaiting a plan-approval ask. |
| `target_mode` | `PermissionMode` |  |  | target_mode selects the verdict / resulting posture: DEFAULT → allow-once (flip to default), ACCEPT_EDITS → allow-always (flip to accept-edits), PLAN/UNSPECIFIED → deny (iterate, no flip, no continuation run). |
| `note` | `string` |  |  | note is an OPTIONAL operator remark appended to the proceed message injected into the continuation run&#39;s first user message (allow paths only). |




#### `mecatl.v1.Authorization`

Authorization is the safe event projection of an external authorization
lifecycle. Status is a closed string passthrough: pending, granted, denied,
cancelled, expired, interrupted, failed, or closed. display_name is optional
bounded human-facing authority/service text. The payload deliberately has no
URL, OAuth code, credential, private binding, topology, or tool arguments.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `authorization_id` | `string` |  |  |  |
| `call_id` | `string` |  |  |  |
| `expires_at` | `google.protobuf.Timestamp` |  |  |  |
| `status` | `string` |  |  |  |
| `display_name` | `string` |  |  |  |




#### `mecatl.v1.Cancel`

Cancel aborts the in-flight run. The loop observes the cancellation and
terminates with a result carrying stop = &#34;cancelled&#34;.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `expected_run_id` | `string` |  |  | expected_run_id, when set, scopes the cancel to ONE run: the server refuses it if the session&#39;s current run is a different one. Cancelling the wrong run is the most costly stale-control outcome — it destroys work rather than merely permitting it — so a client that knows which run it is stopping should always send this. Empty keeps the legacy &#34;cancel whatever is running&#34; behaviour. |




#### `mecatl.v1.CancelChild`

CancelChild cancels ONE child of the in-flight run, addressed by its child
SESSION id — the same handle the model reads on the Subagent result&#39;s
&#34;agentId:&#34; line and clients read on subagent.start&#39;s child_id (no derivation;
one handle everywhere). The child unwinds (mid-drive, mid-gate-wait, or parked
on a surfaced permission.ask — the server then emits a permission.retract for
each withdrawn ask), ends StopCancelled, is persisted, and stays resumable;
the parent run keeps streaming. Unknown or already-finished ids are ignored on
the stream (the finished-as-you-pressed race is benign); the HTTP mirror
(POST /v1/sessions/&#123;id&#125;/cancel-child) reports them as 404.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `child_id` | `string` |  |  | child_id is the child session id, verbatim (e.g. &#34;subagent-&lt;callID&gt;&#34;). |




#### `mecatl.v1.CancelMcpAuthorizationRequest`

CancelMcpAuthorizationRequest uses the same grammar while keeping the two RPC
request envelopes method-specific.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `session_id` | `string` |  |  |  |
| `authorization_id` | `string` |  |  |  |
| `resume_approval` | `ResumeApproval` |  | `control` |  |
| `cancel` | `Cancel` |  | `control` |  |




#### `mecatl.v1.CancelMcpAuthorizationResponse`

CancelMcpAuthorizationResponse wraps one authoritative resolution or continuation event.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `event` | `Event` |  |  |  |




#### `mecatl.v1.CancelRunRequest`

CancelRunRequest addresses one exact live run.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `session_id` | `string` |  |  |  |
| `expected_run_id` | `string` |  |  |  |




#### `mecatl.v1.CancelRunResponse`

CancelRunResponse acknowledges only the run accepted for cancellation.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `run_id` | `string` |  |  |  |




#### `mecatl.v1.CancelRunSteerRequest`

CancelRunSteerRequest retracts pending steering from one exact live run.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `session_id` | `string` |  |  |  |
| `expected_run_id` | `string` |  |  |  |
| `message_id` | `string` |  |  |  |




#### `mecatl.v1.CancelRunSteerResponse`

CancelRunSteerResponse reports the authoritative exact-run retraction result.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `outcome` | `SteerOutcome` |  |  |  |
| `run_id` | `string` |  |  |  |
| `message_id` | `string` |  |  |  |




#### `mecatl.v1.CancelSessionCleanupRequest`



| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `job_id` | `string` |  |  |  |




#### `mecatl.v1.CancelTeammateRequest`

CancelTeammateRequest cancels one member of a running team.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `team_id` | `string` |  |  | team_id is the team the member belongs to. |
| `member` | `string` |  |  | member is the name of the member to cancel. |




#### `mecatl.v1.CancelTeammateResponse`

CancelTeammateResponse is the empty acknowledgement.

This message has no fields.



#### `mecatl.v1.CleanupCandidate`



| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `session_id` | `string` |  |  |  |
| `kind` | `string` |  |  |  |
| `state` | `string` |  |  |  |
| `reason` | `string` |  |  |  |
| `modified_at_unix` | `int64` |  |  |  |
| `estimated_bytes` | `int64` |  |  |  |




#### `mecatl.v1.CleanupCounts`



| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `total` | `int32` |  |  |  |
| `by_kind` | `CleanupCounts.ByKindEntry` | repeated |  |  |
| `by_state` | `CleanupCounts.ByStateEntry` | repeated |  |  |
| `by_reason` | `CleanupCounts.ByReasonEntry` | repeated |  |  |




#### `mecatl.v1.CleanupCounts.ByKindEntry`



| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `key` | `string` |  |  |  |
| `value` | `int32` |  |  |  |




#### `mecatl.v1.CleanupCounts.ByReasonEntry`



| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `key` | `string` |  |  |  |
| `value` | `int32` |  |  |  |




#### `mecatl.v1.CleanupCounts.ByStateEntry`



| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `key` | `string` |  |  |  |
| `value` | `int32` |  |  |  |




#### `mecatl.v1.CleanupItemError`



| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `item_handle` | `string` |  |  |  |
| `reason_code` | `string` |  |  |  |
| `message` | `string` |  |  |  |




#### `mecatl.v1.CleanupJob`



| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `job_id` | `string` |  |  |  |
| `state` | `string` |  |  |  |
| `processed` | `int32` |  |  |  |
| `deleted` | `int32` |  |  |  |
| `skipped` | `int32` |  |  |  |
| `stale` | `int32` |  |  |  |
| `failed` | `int32` |  |  |  |
| `errors` | `CleanupItemError` | repeated |  |  |




#### `mecatl.v1.CleanupTeamRequest`

CleanupTeamRequest names the finished team to tear down.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `team_id` | `string` |  |  | team_id is the team to clean up. |




#### `mecatl.v1.CleanupTeamResponse`

CleanupTeamResponse is the empty acknowledgement.

This message has no fields.



#### `mecatl.v1.ClearSessionRequest`

ClearSessionRequest creates an empty-history successor.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `source_session_id` | `string` |  |  |  |
| `worktree_selector` | `string` | optional |  |  |




#### `mecatl.v1.ClearSessionResponse`



| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `session_id` | `string` |  |  |  |
| `placement` | `PlacementMetadata` |  |  |  |




#### `mecatl.v1.CloseSessionRequest`

CloseSessionRequest names the session to end.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `session_id` | `string` |  |  | session_id is the session to close. |




#### `mecatl.v1.CloseSessionResponse`

CloseSessionResponse is the empty acknowledgement.

This message has no fields.



#### `mecatl.v1.Command`

Command is one discovered slash command&#39;s listing metadata. Mirrors
prompt.Command: the invocation name (without the leading &#34;/&#34;) and a short,
human-facing description for the palette. It carries NO body — discovery is
metadata-only; expansion (which reads the body) is a separate run-path concern.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `name` | `string` |  |  | name is the command&#39;s invocation name (the &#34;&lt;name&gt;&#34; of &#34;/&lt;name&gt;&#34;). |
| `description` | `string` |  |  | description is a short one-line summary derived from the command file (a frontmatter description: field, else the first non-blank body line), capped. |




#### `mecatl.v1.CompactSessionRequest`

CompactSessionRequest identifies the owned main-chat session to compact.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `session_id` | `string` |  |  |  |




#### `mecatl.v1.CompactSessionResponse`

CompactSessionResponse reports whether the compactor reduced model history.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `compacted` | `bool` |  |  |  |




#### `mecatl.v1.CompactionArchive`

CompactionArchive is the proto projection of session.CompactionArchivePayload
(EvCompactionArchive): the pre-compaction conversation that ReplaceHistory
replaced. It carries the full pre-compaction Messages slice so a replay recovers
the dropped turns. The parent&#39;s OWN conversation (gauntlet #7 — no child content).
LOG-ONLY on the live Converse wire; surfaced only by the StreamSessionEvents
replay.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `replaced` | `ConversationMessage` | repeated |  | replaced is the pre-compaction message slice (each message is a ConversationMessage). |




#### `mecatl.v1.Content`

Content is one non-text part of a multimodal user prompt. EXACTLY ONE of
data/url is set: data carries inline bytes, url a remote reference. mime_type
is the IANA media type (e.g. &#34;image/png&#34;, &#34;audio/wav&#34;). The &#34;data XOR url&#34; and
&#34;kind != UNSPECIFIED&#34; rules are enforced by the server (the mapper rejects a
malformed part), not at the wire layer.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `kind` | `Content.Kind` |  |  | kind is the media kind (image / audio); UNSPECIFIED is rejected. |
| `mime_type` | `string` |  |  | mime_type is the IANA media type of the part. |
| `data` | `bytes` |  |  | data is the inline content bytes; empty when url-sourced. |
| `url` | `string` |  |  | url is the remote reference; empty when inline. |
| `artifact_id` | `string` |  |  | artifact_id is required for PDF parts; data/url must be empty. |
| `name` | `string` |  |  | Server-filled metadata on transcript/event projections, never client input. |
| `size` | `int64` |  |  |  |
| `sha256` | `string` |  |  |  |




#### `mecatl.v1.ContentBlock`

ContentBlock is a typed content block on a tool result (mirrors
session.Content for tool-result Parts). A sum-type over Kind.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `kind` | `ContentBlock.Kind` |  |  | kind is the block kind. |
| `mime_type` | `string` |  |  | mime_type is the IANA media type of inline bytes (image/audio/blob). |
| `data` | `bytes` |  |  | data is the inline content bytes (image/audio/blob). |
| `url` | `string` |  |  | url is the remote reference (resource-link URI, or URL-sourced media). |
| `text` | `string` |  |  | text is the text payload (BlockText, BlockEmbeddedResource text, or BlockStructuredContent JSON). |
| `name` | `string` |  |  | name is the resource-link name. |
| `title` | `string` |  |  | title is the resource-link title. |
| `description` | `string` |  |  | description is the resource-link description. |
| `size` | `int64` |  |  | size is the resource-link size in bytes. |
| `audience` | `string` | repeated |  | audience is advisory display routing only (CWE-345: untrusted). |
| `priority` | `double` |  |  | priority is the advisory display priority. |
| `last_modified` | `string` |  |  | last_modified is the resource-link last-modified timestamp (RFC 3339). |
| `artifact_id` | `string` |  |  |  |
| `sha256` | `string` |  |  |  |




#### `mecatl.v1.ControlRefused`

ControlRefused is the metadata-only acknowledgement for a rejected in-stream
approval control. Message text remains a sanitized human diagnostic on Event.text.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `ask_id` | `string` |  |  |  |
| `category` | `string` |  |  |  |




#### `mecatl.v1.ConversationMessage`

ConversationMessage is the proto projection of one session.Message (an immutable
entry in the model-visible conversation history). It exists so the
CompactionArchive payload can carry a message slice — the live Converse wire has
no message-slice surface (the relay streams events, not history), so this type is
introduced here for the replay path. It mirrors the Message value object: Role +
Text + the assistant&#39;s ToolCalls + an optional tool-role ToolResult + the opaque
provider replay blobs (Reasoning / ProviderPhase) + the user-role media Parts.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `role` | `string` |  |  | role is the author (user / assistant / tool / system). |
| `text` | `string` |  |  | text is the message body (assistant text, user prompt, etc.). |
| `tool_calls` | `ToolCall` | repeated |  | tool_calls holds the tool invocations requested by an assistant message. |
| `tool_result` | `ToolResult` |  |  | tool_result holds the result carried by a tool-role message; nil otherwise. |
| `reasoning` | `string` |  |  | reasoning is the provider&#39;s opaque reasoning REPLAY blob, replayed back verbatim and never interpreted/displayed by the harness. |
| `provider_phase` | `string` |  |  | provider_phase is the OpenAI Responses opaque phase marker on an assistant message, replayed verbatim and never interpreted/displayed. |
| `parts` | `Content` | repeated |  | parts carries non-text media (image/audio) on a USER message; nil otherwise. |
| `reasoning_item_id` | `string` |  |  | reasoning_item_id is the OpenAI Responses reasoning-item id carrier (session.Message.ReasoningItemID), replayed verbatim and never interpreted/displayed by the harness. Opaque, provider-private. |




#### `mecatl.v1.ConverseRequest`

ConverseRequest is a client-to-server frame on the Converse stream. The
first frame MUST be `prompt` or `retry`; later frames are controls. A received
second `prompt` or `retry` is rejected with INVALID_ARGUMENT, but controls in
transit when the server completes the run may observe normal stream completion.
Field numbers 1-9 are reserved for the start family; 10+ for the
gate/cancellation family.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `prompt` | `Prompt` |  | `kind` | prompt starts a normal run with a new user message. |
| `retry` | `RetryStart` |  | `kind` | retry starts a prompt-free retry of an eligible failed model step. |
| `resume_approval` | `ResumeApproval` |  | `kind` | resume_approval resolves a paused permission.ask. |
| `cancel` | `Cancel` |  | `kind` | cancel aborts the in-flight run. |
| `cancel_child` | `CancelChild` |  | `kind` | cancel_child cancels ONE child run (a subagent) of the in-flight run, leaving the run itself (and any sibling children) untouched. |
| `steer` | `Steer` |  | `kind` | steer injects an operator instruction mid-run (steer-while-running, issue #512): the run&#39;s steer inbox parks it and the loop drains it at the next turn boundary, recording it as an ordinary user continuation. The server echoes the authoritative outcome back on the stream as a steer.outcome event. gRPC-only in v1 (the HTTP/SSE surface has no client→server mid-run channel). |
| `steer_cancel` | `SteerCancel` |  | `kind` | steer_cancel retracts the run&#39;s PENDING (un-drained) steer, if any. The authoritative outcome rides back as a steer.outcome event (retracted / none_pending). |




#### `mecatl.v1.ConverseResponse`

ConverseResponse is a server-to-client frame: one Event envelope per
domain event, emitted in sequence order until the terminal result.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `event` | `Event` |  |  | event is the domain event for this frame. |




#### `mecatl.v1.CreateSessionRequest`

CreateSessionRequest opens a new session.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `mode` | `PermissionMode` |  |  | mode is the permission posture; unspecified defaults to DEFAULT. |
| `limits` | `Limits` |  |  | limits are the optional stop conditions. |
| `provider_id` | `string` |  |  | provider_id selects which configured provider backs this session, by its stable registry id (&#34;openai&#34; / &#34;openrouter&#34;). Empty =&gt; the server default provider (today&#39;s behaviour). An unknown/unavailable id is a loud InvalidArgument, never a silent fallback. The provider is FIXED for the session lifetime (reasoning-replay + byte-stable prefix are provider-private); &#34;switch provider&#34; = new session. Keys are NEVER on the wire — only this id. (multi-provider Phase 0, S3.) |
| `model_id` | `string` |  |  | model_id is the opaque model selector within provider_id. Empty =&gt; the provider&#39;s default model. An id ListModels did not advertise is passed through to the provider VERBATIM (a power-user escape hatch for a model the embedded catalog doesn&#39;t yet know). NOT slash-joined with provider_id — two distinct fields by design. Setting model_id WITHOUT provider_id is a loud InvalidArgument (a bare model on an env-derived default provider is ambiguous). |
| `profile` | `string` |  |  | profile selects the session&#39;s TOOL-SURFACE profile. Empty binds the deployment default; &#34;no-fs&#34; explicitly attenuates filesystem access. |
| `reasoning_effort` | `string` |  |  | reasoning_effort sets this session&#39;s reasoning-effort tier, enum-as-string (the profile / Result.stop idiom — NO proto enum, additive values):   - &#34;&#34; or &#34;auto&#34;: UNSET — do not send a reasoning-effort field; the operator default (and ultimately the provider default) applies. - &#34;low&#34; / &#34;medium&#34; / &#34;high&#34; / &#34;xhigh&#34; / &#34;max&#34;: the neutral effort tiers.  The server NORMALISES and per-provider CLAMPS the value (OpenAI supports low/medium/high only, so xhigh/max clamp DOWN to high; Anthropic maps all five) and CAPABILITY-GATES it (a model with no reasoning support drops it). An unknown value falls back to the operator default with a WARN (it never 400s the request). It OUT-RANKS the operator default; unlike model_id it is meaningful WITHOUT a provider_id (it rides the server-default provider). The EFFECTIVE resolved value is echoed back on resolved_model.reasoning_effort. |
| `debug_target_session_id` | `string` |  |  | debug_target_session_id creates a separate diagnostic no-FS session. |
| `debug_mcp_servers` | `string` | repeated |  | debug_mcp_servers explicitly selects already-configured server-global streaming-HTTP MCP servers for this debug session. Legal only with a debug target; names are bounded and unique. No URL, header, or inline MCP config is accepted, and selection never implies publication authority. |
| `mcp_servers` | `McpServerSpec` | repeated |  | mcp_servers are CLIENT-PROVIDED streaming-HTTP MCP servers to mount for the lifetime of this session, via a per-session engine. Empty (the default) is byte-identical to today: the session takes the shared-engine path.  LISTENER-SCOPED (ADR 0237 / ADR 0248): accepting an MCP endpoint plus its auth headers from an API caller combines a remote principal with the server&#39;s ambient outbound network authority, so it is a DEPLOYMENT policy, not an inference from the request. A deployment whose API listeners are all local (a UNIX socket, a disabled HTTP listener) permits this field; a deployment with any network-facing API listener refuses every non-empty value with the typed `client_mcp_unsupported` error (UNIMPLEMENTED / 501) rather than mounting it. The refusal is the SERVER&#39;s, so it holds against a client that never checked. A client discovers whether the field is usable from `mcp_servers_on_create` in GetCompatibilityInfo.features.  Transport is streaming-HTTP ONLY, on EVERY listener and regardless of that policy: a stdio entry and an sse entry are hard-rejected as such (AGENTS.md: &#34;No stdio MCP, ever&#34; — mecatl never spawns an MCP server process). |




#### `mecatl.v1.CreateSessionResponse`

CreateSessionResponse carries the newly-allocated session id.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `session_id` | `string` |  |  | session_id is the id of the created session. |
| `session_capabilities` | `SessionCapabilities` |  |  | session_capabilities reports the multimodal input capability of the provider+model THIS session actually resolved to — the INTERSECTION of the catalog&#39;s per-model modalities and the wired adapter&#39;s transmit capability, computed once in composition (internal/app). Clients gate per-session @-attach UX on THIS value. |
| `resolved_model` | `ResolvedModel` |  |  | resolved_model reports the EFFECTIVE provider+model THIS session actually resolved to, plus its context window — the single value computed once in composition (internal/app), the SAME composition-computed single-source discipline as session_capabilities (NEVER recomputed in a handler). It is echoed VERBATIM: the server owns the resolution (the empty-selector default, an env-derived provider, a passthrough model), so a client must read THIS rather than the model_id it sent (which is empty for a default session and ambiguous for passthrough). The model is FIXED per TURN; it is re-resolved BETWEEN turns when the session&#39;s permission mode changes the effective model (the plan-slot / opusplan pattern, ADR 0030 Layer 3) — re-read it from GetSession after a mode change. Nil/absent from an older server → the client falls back to today&#39;s behavior (no model segment in the header). |
| `placement` | `PlacementMetadata` |  |  | placement is bounded display-only metadata, never reusable authority. |




#### `mecatl.v1.CreateTeamRequest`

CreateTeamRequest creates a team owned by an existing session.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `session_id` | `string` |  |  |  |
| `name` | `string` |  |  | name is an optional human label; the server assigns the canonical id. |
| `members` | `TeammateSpec` | repeated |  | members is the optional initial roster, enrolled atomically: if any member fails to enrol the whole team is abandoned. It may be empty (the client can still SpawnTeammate before RunTeam). |
| `goal` | `string` |  |  | goal is the team&#39;s optional top-level objective. It is threaded into the lead&#39;s round-0 turn and the lead&#39;s synthesis prompt as a TRUSTED instruction by default (framing-neutralised, but not fenced; the server&#39;s TeamGoalUntrusted config opts a relay/multi-tenant deployment back into the UNTRUSTED fence). Empty preserves the historical behaviour (no goal briefing on the gRPC path). |
| `max_team_tokens` | `int32` |  |  | max_team_tokens is the per-request team-wide cumulative token budget (input + output, summed across all members and rounds). TIGHTEN-ONLY: a positive value may only LOWER the server-configured --max-team-tokens ceiling, never raise it; 0 / absent inherits the server&#39;s budget. The fold is lower-of-the-two with 0 meaning unlimited: when the server has NO configured budget (--max-team-tokens unset/0), a positive value therefore IMPOSES a budget rather than being capped to &#34;unlimited&#34;. Crossed at a round boundary (the in-flight round and the lead&#39;s synthesis still complete) → the terminal TeamEvent.outcome frame reports budget_exhausted. Mirrors the in-loop Team tool&#39;s per-call max_team_tokens arg. Issue #36. |




#### `mecatl.v1.CreateTeamResponse`

CreateTeamResponse returns the new team&#39;s id and the enrolled initial roster.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `team_id` | `string` |  |  | team_id identifies the team in subsequent calls. |
| `members` | `TeamMember` | repeated |  | members is the enrolled initial roster in enrolment order, so the caller need not follow up with ListTeam to see who was added. |




#### `mecatl.v1.DecideDreamPlanRequest`



| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `plan_id` | `string` |  |  |  |
| `decision` | `string` |  |  | decision must be exactly &#34;apply&#34; or &#34;dismiss&#34;. |




#### `mecatl.v1.DecideDreamPlanResponse`



| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `receipt` | `DreamReceipt` |  |  |  |




#### `mecatl.v1.DecideLearningProposalRequest`



| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `id` | `string` |  |  |  |
| `expected_version` | `string` |  |  |  |
| `decision` | `string` |  |  |  |
| `reason` | `string` |  |  |  |
| `project` | `string` |  |  |  |




#### `mecatl.v1.DecideLearningProposalResponse`



| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `proposal` | `LearningProposal` |  |  |  |




#### `mecatl.v1.DeleteSessionRequest`



| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `session_id` | `string` |  |  |  |




#### `mecatl.v1.DeleteSessionResponse`



This message has no fields.



#### `mecatl.v1.DiffLearnedSkillVersionsRequest`



| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `project` | `string` |  |  |  |
| `owner_agent` | `string` |  |  |  |
| `id` | `string` |  |  |  |
| `from_version` | `string` |  |  |  |
| `to_version` | `string` |  |  |  |




#### `mecatl.v1.DiffLearnedSkillVersionsResponse`



| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `diff` | `string` |  |  |  |
| `generation` | `uint64` |  |  |  |
| `project` | `string` |  |  |  |
| `skill_id` | `string` |  |  |  |
| `from_version` | `string` |  |  |  |
| `to_version` | `string` |  |  |  |




#### `mecatl.v1.DownloadArtifactRequest`



| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `session_id` | `string` |  |  |  |
| `artifact_id` | `string` |  |  |  |




#### `mecatl.v1.DownloadArtifactResponse`



| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `chunk` | `bytes` |  |  |  |




#### `mecatl.v1.DreamOperation`



| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `kind` | `string` |  |  |  |
| `survivor` | `DreamParticipant` |  |  |  |
| `sources` | `DreamParticipant` | repeated |  |  |
| `replacement` | `DreamReplacement` |  |  |  |
| `reason` | `string` |  |  |  |
| `exact_duplicate_eligible` | `bool` |  |  |  |




#### `mecatl.v1.DreamParticipant`



| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `key` | `string` |  |  |  |
| `value` | `string` |  |  |  |
| `description` | `string` |  |  |  |




#### `mecatl.v1.DreamReceipt`



| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `id` | `string` |  |  |  |
| `target` | `string` |  |  |  |
| `disposition` | `string` |  |  |  |
| `planned_source_count` | `int32` |  |  |  |
| `applied_source_count` | `int32` |  |  |  |
| `conflicted_source_count` | `int32` |  |  |  |
| `skipped_source_count` | `int32` |  |  |  |
| `failed_source_count` | `int32` |  |  |  |




#### `mecatl.v1.DreamReplacement`



| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `value` | `string` |  |  |  |
| `description` | `string` |  |  |  |




#### `mecatl.v1.DreamReviewPlan`



| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `id` | `string` |  |  |  |
| `target` | `string` |  |  |  |
| `expires_at` | `google.protobuf.Timestamp` |  |  |  |
| `planned_operation_count` | `int32` |  |  |  |
| `planned_source_count` | `int32` |  |  |  |
| `operations` | `DreamOperation` | repeated |  |  |




#### `mecatl.v1.DreamTargetCapability`



| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `generate` | `bool` |  |  |  |
| `decide` | `bool` |  |  |  |
| `unavailable_reason` | `string` |  |  |  |




#### `mecatl.v1.Event`

Event mirrors the domain session.Event one-for-one. `type` is the string
event kind; the structured submessages are populated per kind.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `type` | `string` |  |  | type is the event kind (mirrors session.EventType: session.init, turn.start, turn.end, message.delta, reasoning.delta, tool.call, tool.result, permission.ask, hook, compaction, result). |
| `seq` | `int64` |  |  | seq is the monotonically increasing sequence number within a run. |
| `turn` | `int32` |  |  | turn is the 0-based turn index this event belongs to (mirrors session.Event.Turn; clients add 1 for a human-facing &#34;turn N&#34;). |
| `text` | `string` |  |  | text carries streamed or final text where applicable. |
| `tool_call` | `ToolCall` |  |  | tool_call is set on tool.call events. |
| `tool_result` | `ToolResult` |  |  | tool_result is set on tool.result events. |
| `ask` | `PermissionAsk` |  |  | ask is set on permission.ask events; ask_id is echoed in ResumeApproval. |
| `result` | `Result` |  |  | result is set on the terminal result event. |
| `turn_end` | `TurnEnd` |  |  | turn_end is set on turn.end events (this turn&#39;s usage + elapsed time). |
| `hook` | `Hook` |  |  | hook is set on hook events: the structured phase/tool/decision so clients render hook notices distinctly (and colour blocked ones) instead of parsing the free-text `text` field. |
| `subagent` | `Subagent` |  |  | subagent is set on the three subagent.* events (subagent.start / subagent.tool / subagent.end): a REDACTED, metadata-only projection of a Task child run. It NEVER carries child content (no message text, tool args, or result bodies) — only ids, a goal label, tool names/counts, usage, stop, and duration — so the context-isolation guarantee is preserved. |
| `team` | `Team` |  |  | team is set on the three team.* events (team.start / team.member / team.end): a BOUNDED projection of an in-process Team tool run. Unlike subagent (metadata only), it is fuller — team.member carries member message text and CAPPED tool-call/result previews — but it is STILL bounded (every preview is capped) and a member&#39;s permission.ask is DROPPED, never forwarded. The per-member transcripts never enter the parent conversation (only the joined summary does). |
| `parallel` | `Parallel` |  |  | parallel is set on the three parallel.* events (parallel.start / parallel.branch / parallel.end): a REDACTED, metadata-only projection of a Parallel fork-join run. Like subagent it NEVER carries branch content (no message text, tool args, or result bodies) — only ids, a goal label, tool names/counts, usage, stop, duration, the join strategy, and the winner index. Physical fork roots and reusable placement authority are never projected. The branch summaries fold into the parent conversation only via the Parallel tool result. |
| `schedule` | `SchedulePayload` |  |  | schedule.* — the scheduler lifecycle (EvScheduleFired/Skipped/Failed). For v1 delivered to the fire session&#39;s durable EventLog ONLY (pull-only via GetFire/ListFires); a live broadcast stream is a future phase. Emitted from composition (the scheduler), NOT the loop. String-passthrough kind/stop/err, no enum. |
| `approval` | `Approval` |  |  | approval is set on approval events (EvApproval — the verdict half of a permission ask). LOG-ONLY on the live Converse wire (the relay skips them: the client holds its own verdict; this is audit history). StreamSessionEvents (the read-back of the durable log) relays them so a transcript replay shows the verdicts. Metadata-only by construction (gauntlet #7): tool NAME + verdict string + askID, NEVER raw args. |
| `user_prompt` | `UserPrompt` |  |  | user_prompt is set on user_prompt events (EvUserPrompt — the recorded user message). LOG-ONLY on the live Converse wire; StreamSessionEvents relays them so a transcript replay shows what the user asked. |
| `compaction_archive` | `CompactionArchive` |  |  | compaction_archive is set on compaction.archive events (EvCompactionArchive — the pre-compaction conversation). LOG-ONLY on the live Converse wire; StreamSessionEvents relays them so a transcript replay recovers dropped turns. |
| `steer` | `SteerEcho` |  |  | steer is set on steer events (EvSteer — the run&#39;s steer-inbox DRAIN echo): the COMMITTED operator steer just recorded into the conversation as an ordinary user continuation. CLIENT-VISIBLE and AUTHORITATIVE: the engine is the sole authority on which steer won the slot, so the client renders THIS text. Byte-identical to the recorded user message replayed to the model (recorded == streamed == model-view). |
| `steer_outcome` | `SteerAck` |  |  | steer_outcome is set on steer.outcome events: the AUTHORITATIVE ack for a steer / steer_cancel frame the client sent on this stream (accepted / accepted / appended / retracted / none_pending / too_late). CLIENT-VISIBLE, one per steer frame, sequenced in send order. |
| `model_retry` | `ModelRetry` |  |  | model_retry carries the typed failed terminal consumed by a model.retry lifecycle boundary. Clients and event-source hosts must not parse text. |
| `run_id` | `string` |  |  | run_id is the opaque, server-minted identity of the run that emitted this event (ADR 0249). It is stamped by the agent loop, so every event a run emits carries it on both the gRPC and HTTP/SSE surfaces.  It is OPAQUE: it encodes nothing and exists only to be compared for equality. A client uses it to know which run an event belongs to — `seq` is monotonic WITHIN a run and restarts each run, so it cannot distinguish two runs of one session — and to address controls at a specific run rather than &#34;whatever is running now&#34;.  EMPTY IS MEANINGFUL, not missing: it means the event is SESSION-scoped rather than run-scoped. The schedule.* lifecycle events are emitted outside any run and legitimately carry no id. A client following one run filters on an exact match and so never sees them; a client showing a session timeline includes them. |
| `title` | `SessionTitle` |  |  | title is set on session.title events. It is the authoritative, source-free title lifecycle projection after a persisted title state change. |
| `authorization` | `Authorization` |  |  | authorization is set on authorization.required and authorization.resolved events. It is safe durable correlation only; the live presentation URL and private continuation state never enter this payload. |
| `control_refused` | `ControlRefused` |  |  | control_refused is set on control.refused events. It identifies the exact approval ask whose submitted control was rejected and carries only a stable machine category; raw arguments and refusal rationale never enter it. |
| `plan_continuation_failure` | `PlanContinuationFailure` |  |  | plan_continuation_failure is a session-scoped, content-safe indication that an accepted plan allow could not start its proceed run. |




#### `mecatl.v1.ForkSessionRequest`

ForkSessionRequest names the source session to fork from.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `source_session_id` | `string` |  |  |  |
| `title` | `string` |  |  |  |
| `reasoning_effort` | `string` |  |  |  |
| `worktree_selector` | `string` | optional |  |  |
| `provider_id` | `string` |  |  |  |
| `model_id` | `string` |  |  |  |




#### `mecatl.v1.ForkSessionResponse`



| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `session_id` | `string` |  |  |  |
| `placement` | `PlacementMetadata` |  |  |  |




#### `mecatl.v1.GenerateDreamPlanRequest`



| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `target` | `string` |  |  | target must be exactly &#34;project_memory&#34; or &#34;user_model&#34;. |




#### `mecatl.v1.GenerateDreamPlanResponse`



| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `plan` | `DreamReviewPlan` |  |  |  |




#### `mecatl.v1.GetCompatibilityInfoRequest`

GetCompatibilityInfoRequest is empty: the descriptor is deployment-wide and
identical for every caller. It exists so the RPC can grow request fields
additively without a signature change.

This message has no fields.



#### `mecatl.v1.GetCompatibilityInfoResponse`

GetCompatibilityInfoResponse is the deployment&#39;s compatibility descriptor (ADR 0248).

It deliberately carries TWO different vocabularies side by side, because they
answer different questions and conflating them is a bug:

  - `capabilities` answers &#34;what has this OPERATOR enabled?&#34; — it changes with
    operator config. `shell: false` means the Shell tool is unavailable.
  - `features` answers &#34;what does this BUILD implement?&#34; — it changes when
    mecatl is upgraded.

A client that reads a disabled capability as version skew will reject a
correctly-configured deployment; a client that infers protocol support from an
operator toggle will call an RPC the server has never heard of.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `api_major` | `int32` |  |  | api_major is the wire-contract major version. It starts at 1 and bumps ONLY on a genuine break; additive changes are announced through `features` instead. A client gates on api_major + feature identifiers, NEVER on a server/package semver comparison. |
| `capabilities` | `ServerCapabilities` |  |  | capabilities is the canonical deployment-wide operator-enabled feature set. Media (image/audio) here is a SERVER-WIDE hint for UI chrome only: the per-session CreateSessionResponse.session_capabilities value remains authoritative for whether a given session may send media. |
| `features` | `string` | repeated |  | features are the build&#39;s supported feature identifiers as OPEN STRINGS, not an enum — the EvNoProgress/StopBudget string-passthrough discipline. An unrecognised identifier is ignored by an older client; a new one is a minor release, never a wire-compat event. Identifiers are stable once published.  A feature that is only reachable on some listeners is advertised only on a listener that permits it, so this set is &#34;what this build implements AND this listener permits&#34; (see ADR 0237 / ADR 0248). |
| `deployment` | `string` |  |  | deployment is an OPTIONAL, opaque, bounded, operator-set label for this deployment. It is empty by default and is NEVER derived from hostname, pod name, or environment — infrastructure topology is not something an authenticated caller is owed, and a label the operator did not choose is a leak with no consenting author. Set via mecated --deployment-id. |




#### `mecatl.v1.GetGuardrailReviewDetailRequest`



| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `session_id` | `string` |  |  |  |
| `review_id` | `string` |  |  |  |




#### `mecatl.v1.GetGuardrailReviewDetailResponse`



| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `review_id` | `string` |  |  |  |
| `concern` | `string` |  |  |  |
| `source_display` | `string` |  |  |  |
| `next_action` | `string` |  |  |  |




#### `mecatl.v1.GetLearnedSkillRequest`



| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `project` | `string` |  |  |  |
| `owner_agent` | `string` |  |  |  |
| `id` | `string` |  |  |  |
| `version` | `string` |  |  |  |




#### `mecatl.v1.GetLearnedSkillResponse`



| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `skill` | `LearnedSkillVersion` |  |  |  |
| `generation` | `uint64` |  |  |  |
| `project` | `string` |  |  |  |




#### `mecatl.v1.GetLearningAttemptRequest`



| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `id` | `string` |  |  |  |




#### `mecatl.v1.GetLearningAttemptResponse`



| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `attempt` | `LearningAttempt` |  |  |  |




#### `mecatl.v1.GetLearningProposalRequest`



| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `id` | `string` |  |  |  |
| `project` | `string` |  |  |  |




#### `mecatl.v1.GetLearningProposalResponse`



| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `proposal` | `LearningProposal` |  |  |  |




#### `mecatl.v1.GetMcpAuthorizationPresentationRequest`

GetMcpAuthorizationPresentationRequest carries only owner-scoped correlation.
It has no status, OAuth code, credential, private binding, or tool arguments,
so a client cannot assert authorization success.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `session_id` | `string` |  |  |  |
| `authorization_id` | `string` |  |  |  |




#### `mecatl.v1.GetMcpAuthorizationPresentationResponse`

GetMcpAuthorizationPresentationResponse exposes a live browser URL only as the
response to the owner-authorized presentation control. It is never an event
payload and must not be persisted in a session snapshot or event log.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `url` | `string` |  |  |  |




#### `mecatl.v1.GetMcpPromptRequest`

GetMcpPromptRequest expands a named prompt with arguments on a named server.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `server` | `string` |  |  | server is the connected server that owns the prompt. |
| `name` | `string` |  |  | name is the prompt to expand. |
| `arguments` | `GetMcpPromptRequest.ArgumentsEntry` | repeated |  | arguments are the templating values keyed by argument name. |




#### `mecatl.v1.GetMcpPromptRequest.ArgumentsEntry`



| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `key` | `string` |  |  |  |
| `value` | `string` |  |  |  |




#### `mecatl.v1.GetMcpPromptResponse`

GetMcpPromptResponse carries the expanded prompt.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `description` | `string` |  |  | description is the prompt&#39;s optional description. |
| `messages` | `McpPromptMessage` | repeated |  | messages are the rendered, role-tagged messages. |




#### `mecatl.v1.GetServerInfoRequest`

GetServerInfoRequest asks for the sanitized diagnostic display endpoint of the
caller&#39;s already-known active provider. An absent or unknown provider_id leaves
llm_provider_display_endpoint unavailable; the server never infers it from a
default or session state.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `provider_id` | `string` |  |  |  |




#### `mecatl.v1.GetServerInfoResponse`

GetServerInfoResponse contains server build/composition identities and sanitized
diagnostic display projections, never connection instructions.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `build_id` | `string` |  |  |  |
| `server_implementation` | `string` |  |  | server_implementation is the stable composition family only (for example, &#34;mecated&#34;). It never identifies an instance, deployment, topology, configuration, capabilities, or authentication. |
| `llm_provider_display_endpoint` | `string` |  |  | llm_provider_display_endpoint is sanitized diagnostic display data for the requested provider_id only when that provider is already known to composition. It is never connection configuration or a connection instruction. It contains only scheme, host, optional port, and an escaped clean path; userinfo, query, fragment, controls, and invalid or oversized values are omitted. |




#### `mecatl.v1.GetSessionCleanupJobRequest`



| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `job_id` | `string` |  |  |  |




#### `mecatl.v1.GetSessionRequest`

GetSessionRequest looks up an existing session by id.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `session_id` | `string` |  |  | session_id is the id to look up. |




#### `mecatl.v1.GetSessionResponse`

GetSessionResponse wraps the session snapshot returned by GetSession.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `session` | `Session` |  |  | session is the snapshot of the requested session. |




#### `mecatl.v1.GetSessionTranscriptRequest`

GetSessionTranscriptRequest names the owned session whose current aggregate
conversation is requested.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `session_id` | `string` |  |  |  |




#### `mecatl.v1.GetSessionTranscriptResponse`

GetSessionTranscriptResponse is one coherent SessionStore snapshot projection.
messages are the loaded aggregate&#39;s current Conversation.Messages in order.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `session_id` | `string` |  |  | session_id is copied from the same loaded aggregate as messages. |
| `messages` | `ConversationMessage` | repeated |  | messages contains human-displayable message fields. Provider-private replay fields on ConversationMessage are always omitted from this surface. |
| `complete` | `bool` |  |  | complete is true even for a genuinely empty conversation: a successful load is the completeness attestation, never EventLog EOF. |
| `activity` | `ActivityReplayStatus` |  |  | activity reports the separate, optional, non-authoritative replay plane. |
| `kind` | `string` |  |  | kind and relationship come from the same authoritative snapshot as messages. |
| `relationship` | `SessionRelationship` |  |  |  |




#### `mecatl.v1.GetSoulRequest`

GetSoulRequest requests the resolved soul snapshot.

This message has no fields.



#### `mecatl.v1.GetSoulResponse`

GetSoulResponse carries the resolved soul snapshot.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `soul` | `SoulInfo` |  |  | soul is the resolved soul snapshot. Never nil; present=false when no soul was selected. |




#### `mecatl.v1.GetStorageHealthRequest`



This message has no fields.



#### `mecatl.v1.GetStorageHealthResponse`



| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `available` | `bool` |  |  |  |
| `unavailable_reason` | `string` |  |  |  |
| `current_bytes` | `int64` |  |  |  |
| `current_bytes_available` | `bool` |  |  |  |
| `reclaimable_bytes` | `int64` |  |  |  |
| `reclaimable_bytes_available` | `bool` |  |  |  |
| `session_count` | `int64` |  |  |  |
| `file_count` | `int64` |  |  |  |
| `v1_count` | `int64` |  |  |  |
| `v2_count` | `int64` |  |  |  |
| `main_count` | `int64` |  |  |  |
| `child_count` | `int64` |  |  |  |
| `scheduled_count` | `int64` |  |  |  |
| `unknown_count` | `int64` |  |  |  |
| `corrupt_count` | `int64` |  |  |  |
| `policy` | `RetentionPolicy` |  |  |  |
| `last_sweep_unix` | `int64` |  |  |  |
| `last_sweep_available` | `bool` |  |  |  |
| `next_sweep_unix` | `int64` |  |  |  |
| `next_sweep_available` | `bool` |  |  |  |
| `active_job` | `string` |  |  |  |
| `last_failure` | `string` |  |  |  |
| `ownerless_sessions_available` | `bool` |  |  | Content-free ownerless-record preflight for enabling caller ownership. |
| `ownerless_sessions_unavailable_reason` | `string` |  |  |  |
| `ownerless_session_count` | `int64` |  |  |  |
| `ownerless_session_ids` | `string` | repeated |  |  |
| `ownerless_session_ids_truncated` | `bool` |  |  |  |
| `ownerless_schedules_available` | `bool` |  |  |  |
| `ownerless_schedules_unavailable_reason` | `string` |  |  |  |
| `ownerless_schedule_count` | `int64` |  |  |  |
| `ownerless_schedule_names` | `string` | repeated |  |  |
| `ownerless_schedule_names_truncated` | `bool` |  |  |  |




#### `mecatl.v1.GetUserModelRequest`

GetUserModelRequest requests the current user-model index. When key is set,
the server also returns exact-entry detail when the backing store can read it.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `key` | `string` |  |  |  |




#### `mecatl.v1.GetUserModelResponse`

GetUserModelResponse carries the current user-model entries plus aggregate
metadata. The entries reflect the LIVE store index at request time.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `entries` | `UserModelEntry` | repeated |  | entries are the current user-model entries, key-sorted. |
| `size_bytes` | `int64` |  |  | size_bytes is the aggregate byte length of the rendered entries (key + description), an at-a-glance footprint indicator. |
| `sha256` | `string` |  |  | sha256 is the lowercase-hex SHA-256 over the rendered entries, so a client can tell at a glance whether the user model changed between fetches. |
| `detail` | `UserModelDetail` |  |  | detail is populated only when request.key exactly matches an entry. |




#### `mecatl.v1.GuardrailApprovalScope`



| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `review_id` | `string` |  |  |  |
| `kind` | `GuardrailApprovalKind` |  |  |  |
| `grant_digest` | `string` |  |  |  |
| `session_only` | `bool` |  |  |  |
| `repeat_available` | `bool` |  |  |  |




#### `mecatl.v1.GuardrailCoverageEntry`



| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `tool` | `string` |  |  |  |
| `phase` | `string` |  |  |  |
| `job` | `GuardrailJob` |  |  |  |
| `mode` | `string` |  |  |  |
| `rule_id` | `string` |  |  |  |
| `rule_origin` | `string` |  |  |  |
| `inspection` | `GuardrailInspection` |  |  |  |
| `reason` | `string` |  |  |  |




#### `mecatl.v1.GuardrailRef`



| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `ref` | `string` |  |  |  |
| `category` | `string` |  |  |  |




#### `mecatl.v1.GuardrailReview`



| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `review_id` | `string` |  |  |  |
| `job` | `GuardrailJob` |  |  |  |
| `assessment` | `GuardrailAssessment` |  |  |  |
| `inspection` | `GuardrailInspection` |  |  |  |
| `disposition` | `GuardrailDisposition` |  |  |  |
| `reason_code` | `string` |  |  |  |
| `rule_id` | `string` |  |  |  |
| `rule_origin` | `string` |  |  |  |
| `checker_provider_id` | `string` |  |  |  |
| `checker_model_id` | `string` |  |  |  |
| `concerns` | `GuardrailRef` | repeated |  |  |
| `sources` | `GuardrailRef` | repeated |  |  |




#### `mecatl.v1.Hook`

Hook is the structured payload carried by a hook event, in addition to the
human-readable Event.text. Mirrors session.HookPayload.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `phase` | `string` |  |  | phase is the lifecycle point the hook fired at (e.g. &#34;PreToolUse&#34;); empty when not applicable. |
| `tool` | `string` |  |  | tool is the tool the hook relates to for per-tool phases; empty otherwise. |
| `decision` | `HookDecision` |  |  | decision is the hook outcome (info / blocked / modified). |
| `call_id` | `string` |  |  | call_id is the id of the tool call this hook fired against, for the per-tool phases (PreToolUse / PostToolUse); empty otherwise. Lets a client address the hook notice to the originating tool card. Mirrors session.HookPayload.CallID. |
| `guardrail` | `GuardrailReview` |  |  |  |




#### `mecatl.v1.LearnedSkillVersion`

LearnedSkillVersion is a bounded inspect-only projection of an agent-owned
immutable version. Body is capped and repaired to valid UTF-8 by the server.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `id` | `string` |  |  |  |
| `name` | `string` |  |  |  |
| `version` | `string` |  |  |  |
| `revision` | `string` |  |  |  |
| `state` | `string` |  |  |  |
| `owner_agent` | `string` |  |  |  |
| `description` | `string` |  |  |  |
| `body` | `string` |  |  |  |
| `supersedes` | `string` |  |  |  |
| `evidence_count` | `int32` |  |  |  |
| `evidence` | `LearningEvidenceRef` | repeated |  |  |
| `evaluations` | `SkillEvaluation` | repeated |  |  |
| `receipts` | `SkillChangeReceipt` | repeated |  |  |
| `created_at` | `google.protobuf.Timestamp` |  |  |  |
| `updated_at` | `google.protobuf.Timestamp` |  |  |  |
| `inspect_available` | `bool` |  |  |  |
| `undo_available` | `bool` |  |  |  |




#### `mecatl.v1.LearningAttempt`

LearningAttempt is deliberately content-free. It contains only the closed
lifecycle vocabulary, opaque versions/identifiers, bounded generations, and
authorized proposal/skill links from the caller&#39;s private attempt partition.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `id` | `string` |  |  |  |
| `version` | `string` |  |  |  |
| `state` | `string` |  |  |  |
| `outcome` | `string` |  |  |  |
| `failure_code` | `string` |  |  |  |
| `attempt_generation` | `uint64` |  |  |  |
| `claim_generation` | `uint64` |  |  |  |
| `claim_expires_at` | `google.protobuf.Timestamp` |  |  |  |
| `checkpoint_stage` | `string` |  |  |  |
| `proposal_id` | `string` |  |  |  |
| `skill_id` | `string` |  |  |  |
| `created_at` | `google.protobuf.Timestamp` |  |  |  |
| `updated_at` | `google.protobuf.Timestamp` |  |  |  |




#### `mecatl.v1.LearningDecision`



| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `kind` | `string` |  |  |  |
| `actor` | `string` |  |  |  |
| `reason` | `string` |  |  |  |
| `at` | `google.protobuf.Timestamp` |  |  |  |




#### `mecatl.v1.LearningEvidenceRef`



| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `session_id` | `string` |  |  |  |
| `locator` | `string` |  |  |  |
| `ordinal` | `int32` |  |  |  |
| `event_seq` | `int64` |  |  |  |
| `tool_call_id` | `string` |  |  |  |
| `digest` | `string` |  |  |  |
| `available` | `bool` |  |  | available is true only when the authorized source still resolves to this digest. |
| `availability` | `string` |  |  |  |
| `preview` | `string` |  |  | preview is the bounded canonical content re-read after ownership and digest checks. |




#### `mecatl.v1.LearningPromotionReceipt`



| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `memory_key` | `string` |  |  |  |
| `previous_exists` | `bool` |  |  |  |
| `previous_version` | `string` |  |  |  |
| `result_version` | `string` |  |  |  |




#### `mecatl.v1.LearningProposal`



| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `id` | `string` |  |  |  |
| `version` | `string` |  |  |  |
| `status` | `string` |  |  |  |
| `kind` | `string` |  |  |  |
| `key` | `string` |  |  |  |
| `value` | `string` |  |  |  |
| `description` | `string` |  |  |  |
| `title` | `string` |  |  |  |
| `body` | `string` |  |  |  |
| `evidence` | `LearningEvidenceRef` | repeated |  |  |
| `triggers` | `string` | repeated |  |  |
| `decisions` | `LearningDecision` | repeated |  |  |
| `promotion` | `LearningPromotionReceipt` |  |  |  |
| `created_at` | `google.protobuf.Timestamp` |  |  |  |
| `updated_at` | `google.protobuf.Timestamp` |  |  |  |
| `project_scoped` | `bool` |  |  |  |
| `promotion_available` | `bool` |  |  | promotion_available is true when this partition has an exact trusted, convergence-capable memory target. |
| `promotion_unavailable_reason` | `string` |  |  | promotion_unavailable_reason explains why approve/undo are disabled. |
| `learned_skill_id` | `string` |  |  | learned_skill_id links a materialized procedure to its agent-owned skill. |




#### `mecatl.v1.Limits`

Limits are the configured stop conditions for a session. A zero value in
any field disables that particular limit.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `max_turns` | `int32` |  |  | max_turns caps the number of model calls; 0 disables. |
| `max_tool_calls` | `int32` |  |  | max_tool_calls caps the total tool invocations; 0 disables. |
| `max_consecutive_failures` | `int32` |  |  | max_consecutive_failures caps back-to-back tool failures; 0 disables. |




#### `mecatl.v1.ListAgentsRequest`

ListAgentsRequest requests the resolved agent-definition snapshot.

This message has no fields.



#### `mecatl.v1.ListAgentsResponse`

ListAgentsResponse carries the resolved agent definitions.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `agents` | `AgentInfo` | repeated |  | agents are the (possibly empty) resolved agent definitions, name-sorted. |




#### `mecatl.v1.ListCommandsRequest`



| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `session_id` | `string` |  |  |  |




#### `mecatl.v1.ListCommandsResponse`

ListCommandsResponse carries the discovered slash commands.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `commands` | `Command` | repeated |  | commands are the (possibly empty) discovered commands, name-sorted. |




#### `mecatl.v1.ListGuardrailCoverageRequest`



| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `session_id` | `string` |  |  |  |




#### `mecatl.v1.ListGuardrailCoverageResponse`



| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `enabled` | `bool` |  |  |  |
| `checker_provider_id` | `string` |  |  |  |
| `checker_model_id` | `string` |  |  |  |
| `entries` | `GuardrailCoverageEntry` | repeated |  |  |




#### `mecatl.v1.ListLearnedSkillsRequest`



| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `project` | `string` |  |  |  |
| `cursor` | `string` |  |  |  |
| `limit` | `int32` |  |  |  |
| `state` | `string` |  |  |  |
| `owner_agent` | `string` |  |  |  |




#### `mecatl.v1.ListLearnedSkillsResponse`



| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `skills` | `LearnedSkillVersion` | repeated |  |  |
| `next_cursor` | `string` |  |  |  |
| `generation` | `uint64` |  |  |  |
| `project` | `string` |  |  |  |




#### `mecatl.v1.ListLearningAttemptsRequest`



| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `state` | `string` |  |  |  |
| `cursor` | `string` |  |  |  |
| `limit` | `int32` |  |  |  |




#### `mecatl.v1.ListLearningAttemptsResponse`



| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `attempts` | `LearningAttempt` | repeated |  |  |
| `next_cursor` | `string` |  |  |  |




#### `mecatl.v1.ListLearningProposalsRequest`



| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `status` | `string` |  |  |  |
| `cursor` | `string` |  |  |  |
| `limit` | `int32` |  |  |  |
| `project` | `string` |  |  | project selects the project-scoped partition; empty selects operator scope. |




#### `mecatl.v1.ListLearningProposalsResponse`



| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `proposals` | `LearningProposal` | repeated |  |  |
| `next_cursor` | `string` |  |  |  |




#### `mecatl.v1.ListMcpPromptsRequest`

ListMcpPromptsRequest lists prompt snapshots; an empty server returns the
union across every connected server.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `server` | `string` |  |  | server scopes the listing to one connected server; empty returns all. |




#### `mecatl.v1.ListMcpPromptsResponse`

ListMcpPromptsResponse carries the prompt snapshots.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `prompts` | `McpPrompt` | repeated |  | prompts are the (possibly empty) prompt snapshots. |




#### `mecatl.v1.ListMcpResourcesRequest`

ListMcpResourcesRequest lists resource snapshots; an empty server returns the
union across every connected server.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `server` | `string` |  |  | server scopes the listing to one connected server; empty returns all. |




#### `mecatl.v1.ListMcpResourcesResponse`

ListMcpResourcesResponse carries the resource snapshots.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `resources` | `McpResource` | repeated |  | resources are the (possibly empty) resource snapshots. |




#### `mecatl.v1.ListMcpSourcesRequest`

ListMcpSourcesRequest requests the resolved source inventory snapshot.

This message has no fields.



#### `mecatl.v1.ListMcpSourcesResponse`

ListMcpSourcesResponse carries the resolved source inventory.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `sources` | `McpSource` | repeated |  | sources are the (possibly empty) resolved sources. |
| `revision` | `uint64` |  |  | revision is the current published direct-runtime revision. |
| `stale` | `bool` |  |  | stale is true when source or candidate degradation retained an older runtime. |
| `reconciling` | `bool` |  |  | reconciling is true while the shared reconciler is processing a cycle. |




#### `mecatl.v1.ListModelsRequest`

ListModelsRequest requests the selectable-model inventory.

This message has no fields.



#### `mecatl.v1.ListModelsResponse`

ListModelsResponse carries the selectable models across AVAILABLE providers
only (a provider with no resolved credentials is omitted entirely — its
availability is itself sensitive, CWE-200). Sorted by (provider_id, id) for a
deterministic picker. Empty when zero providers are available (the zero-keys
case) — the client renders that as &#34;no models; set a provider key&#34;.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `models` | `ModelInfo` | repeated |  | models are the (possibly empty) selectable models, (provider_id, id)-sorted. |
| `provider_status` | `ProviderStatus` | repeated |  | provider_status surfaces operator-actionable live-inventory outcomes for intent-driven gateways and openai-codex, so a client can render a provider-specific remediation hint even when it contributes zero models. |




#### `mecatl.v1.ListSessionMcpConnectorsRequest`

ListSessionMcpConnectorsRequest names an owned, broker-bound session.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `session_id` | `string` |  |  |  |




#### `mecatl.v1.ListSessionMcpConnectorsResponse`

ListSessionMcpConnectorsResponse reports broker publication, not current health,
credential validity, session installation, persistence or prompt readiness.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `availability` | `string` |  |  | available or unavailable (lost runtime state or binding mismatch). |
| `enrollment_state` | `string` |  |  | not_required, not_started, pending, completed or unknown. |
| `connectors` | `McpConnectorStatus` | repeated |  | Configured order, at most 256 rows, including configured-but-hidden tools. |
| `total_connectors` | `uint32` |  |  |  |
| `truncated` | `bool` |  |  |  |




#### `mecatl.v1.ListSessionsRequest`

ListSessionsRequest asks for one bounded keyset page of stored-session inventory.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `page_size` | `int32` |  |  | page_size is clamped by the server. Zero selects the server default. |
| `cursor` | `string` |  |  | cursor is the opaque continuation token returned by the previous page. |




#### `mecatl.v1.ListSessionsResponse`

ListSessionsResponse carries one bounded stored-session inventory page, sorted
by modified_at descending then session_id ascending.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `sessions` | `SessionSummary` | repeated |  |  |
| `next_cursor` | `string` |  |  |  |
| `total_count` | `int32` |  |  |  |




#### `mecatl.v1.ListSkillChangesRequest`



| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `project` | `string` |  |  |  |
| `cursor` | `string` |  |  |  |
| `limit` | `int32` |  |  |  |




#### `mecatl.v1.ListSkillChangesResponse`



| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `changes` | `SkillChangeReceipt` | repeated |  |  |
| `next_cursor` | `string` |  |  |  |
| `generation` | `uint64` |  |  |  |
| `project` | `string` |  |  |  |




#### `mecatl.v1.ListSkillsRequest`

ListSkillsRequest requests the resolved skills inventory snapshot.

This message has no fields.



#### `mecatl.v1.ListSkillsResponse`

ListSkillsResponse carries the resolved skills inventory.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `skills` | `SkillInfo` | repeated |  | skills are the (possibly empty) discovered skills, name-sorted. |




#### `mecatl.v1.ListTeamRequest`

ListTeamRequest names the team to snapshot.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `team_id` | `string` |  |  | team_id is the team to snapshot. |




#### `mecatl.v1.ListTeamResponse`

ListTeamResponse is the team roster, task list, and completion state.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `members` | `TeamMember` | repeated |  | members is the roster in enrolment order. |
| `tasks` | `TeamTask` | repeated |  | tasks is the shared task list in creation order. |
| `quiescent` | `bool` |  |  | quiescent reports whether the team has reached completion. |




#### `mecatl.v1.ListToolHiveGroupsRequest`

ListToolHiveGroupsRequest requests the distinct ToolHive groups in the
resolved inventory.

This message has no fields.



#### `mecatl.v1.ListToolHiveGroupsResponse`

ListToolHiveGroupsResponse carries the distinct, non-empty ToolHive groups.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `groups` | `string` | repeated |  | groups are the distinct, non-empty ToolHive groups in the inventory. |




#### `mecatl.v1.ListWorktreesRequest`



| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `session_id` | `string` |  |  |  |




#### `mecatl.v1.ListWorktreesResponse`

ListWorktreesResponse carries the discovered worktrees.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `worktrees` | `Worktree` | repeated |  | worktrees are the (possibly empty) discovered worktrees, in `git worktree list` order (the main worktree first). |




#### `mecatl.v1.ManualDreamCapabilities`

ManualDreamCapabilities reports manual generation/decision availability for
each closed target independently.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `project_memory` | `DreamTargetCapability` |  |  |  |
| `user_model` | `DreamTargetCapability` |  |  |  |




#### `mecatl.v1.McpConnectorStatus`

McpConnectorStatus is a safe display row, never a routing handle.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `name` | `string` |  |  | Display name only; control-free and bounded to 128 runes. |
| `catalogue_state` | `string` |  |  | hidden, declared, discovered or unknown. Unknown count is not an empty catalogue. |
| `tool_count` | `uint32` |  |  |  |




#### `mecatl.v1.McpPrompt`

McpPrompt is a prompt template advertised by a connected server. Mirrors
mcp.Prompt.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `server` | `string` |  |  | server is the connected server that owns the prompt. |
| `name` | `string` |  |  | name is the prompt&#39;s programmatic name. |
| `title` | `string` |  |  | title is the prompt&#39;s human-facing title. |
| `description` | `string` |  |  | description summarizes the prompt. |
| `arguments` | `McpPromptArgument` | repeated |  | arguments are the templating arguments the prompt accepts. |




#### `mecatl.v1.McpPromptArgument`

McpPromptArgument describes one templating argument a prompt accepts.
Mirrors mcp.PromptArgument.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `name` | `string` |  |  | name is the argument&#39;s programmatic name. |
| `title` | `string` |  |  | title is the argument&#39;s human-facing title. |
| `description` | `string` |  |  | description summarizes the argument. |
| `required` | `bool` |  |  | required reports whether the argument must be supplied. |




#### `mecatl.v1.McpPromptMessage`

McpPromptMessage is one role-tagged message of an expanded prompt. Mirrors
mcp.PromptMessage.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `role` | `string` |  |  | role is the MCP role string (&#34;user&#34;/&#34;assistant&#34;). |
| `text` | `string` |  |  | text is the flattened textual content of the message. |




#### `mecatl.v1.McpResource`

McpResource is a single resource advertised by a connected MCP server.
Mirrors mcp.Resource.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `server` | `string` |  |  | server is the connected server that owns the resource. |
| `uri` | `string` |  |  | uri identifies the resource on its server. |
| `name` | `string` |  |  | name is the resource&#39;s programmatic name. |
| `title` | `string` |  |  | title is the resource&#39;s human-facing title. |
| `description` | `string` |  |  | description summarizes the resource. |
| `mime_type` | `string` |  |  | mime_type is the resource&#39;s media type. |
| `size` | `int64` |  |  | size is the resource size in bytes (0 when unknown). |
| `read_only` | `bool` |  |  | read_only is always true: reading a resource never mutates the server. |




#### `mecatl.v1.McpResourceContents`

McpResourceContents is one chunk of a read resource&#39;s body. Text holds UTF-8
text; blob holds raw binary bytes. Mirrors mcp.ResourceContents.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `uri` | `string` |  |  | uri identifies the chunk&#39;s resource. |
| `mime_type` | `string` |  |  | mime_type is the chunk&#39;s media type. |
| `text` | `string` |  |  | text is the UTF-8 textual content, when the chunk is textual. |
| `blob` | `bytes` |  |  | blob is the raw binary content, when the chunk is binary. |




#### `mecatl.v1.McpServerInfo`

McpServerInfo is one resolved MCP server candidate within a source. Mirrors
source.ServerInfo.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `name` | `string` |  |  | name is the server&#39;s configured name. |
| `url` | `string` |  |  | url is the server&#39;s proxy/base URL. |
| `transport` | `string` |  |  | transport is the wire transport (e.g. &#34;streamable-http&#34;). |
| `group` | `string` |  |  | group is the ToolHive group (empty for the static source). |




#### `mecatl.v1.McpServerSpec`

McpServerSpec is one client-provided MCP server on CreateSessionRequest. It
mirrors the ACP session/new mcpServers entry shape so both surfaces classify
through the same validator.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `name` | `string` |  |  | name is the short, stable identifier that becomes the &lt;server&gt; segment of every wrapped tool&#39;s namespaced name. |
| `url` | `string` |  |  | url is the server&#39;s streaming-HTTP endpoint. It must be absolute with a host, and https — or http only to an explicit loopback host. |
| `type` | `string` |  |  | type is the transport discriminant: &#34;http&#34; (or empty with a url) is the only accepted value. &#34;stdio&#34; and &#34;sse&#34; are hard-rejected on every listener. |
| `command` | `string` |  |  | command is carried ONLY so a command-shaped entry is classified as stdio and rejected AS stdio. It is never executed — mecatl spawns no MCP process. |
| `headers` | `McpServerSpec.HeadersEntry` | repeated |  | headers are extra HTTP headers sent to this server, typically an Authorization bearer. Values are SECRET-SHAPED: the server never logs them, never projects them into an event, and never includes them in an error. |




#### `mecatl.v1.McpServerSpec.HeadersEntry`



| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `key` | `string` |  |  |  |
| `value` | `string` |  |  |  |




#### `mecatl.v1.McpSource`

McpSource is the inventory for one resolved MCP source. Mirrors
source.SourceInfo.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `name` | `string` |  |  | name is the source&#39;s identity, e.g. &#34;static&#34; or &#34;toolhive(default)&#34;. |
| `kind` | `string` |  |  | kind is the coarse source kind: &#34;static&#34; or &#34;toolhive&#34;. |
| `enabled` | `bool` |  |  | enabled reports whether the source was active in the resolution. |
| `group` | `string` |  |  | group is the ToolHive group (empty for the static source). |
| `servers` | `McpServerInfo` | repeated |  | servers are the configs this source contributed. |
| `diagnostics` | `string` | repeated |  | diagnostics are this source&#39;s per-server skip reasons. |




#### `mecatl.v1.ModelInfo`

ModelInfo is one selectable model in the resolved inventory. It mirrors the
composition-layer projection of the provider registry&#39;s AVAILABLE providers
joined to the embedded models.dev catalog. It carries NO secret material
(no key, no env var name, no base URL) — only public model metadata. The
modality/reasoning bits gate the client picker&#39;s affordances; context_limit
informs the picker&#39;s display only (the server is the cap authority).

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `id` | `string` |  |  | id is the opaque model selector (the value sent as CreateSession.model_id). |
| `provider_id` | `string` |  |  | provider_id is the registry id this model is served by (CreateSession.provider_id). |
| `display_name` | `string` |  |  | display_name is the human-facing model name from the catalog (falls back to id). |
| `image` | `bool` |  |  | image is true when the model accepts image prompt input (catalog modality). |
| `reasoning` | `bool` |  |  | reasoning is true when the model emits reasoning/thinking (catalog flag). |
| `context_limit` | `int64` |  |  | context_limit is the model&#39;s total context window in tokens (0 when unknown). |
| `prompt_cached` | `bool` |  |  | prompt_cached is true when mecatl ASKS the upstream to cache this (provider, model) pair&#39;s conversation prefix (ADR 0346). Decision 1 arms the protocol-native breakpoint on EVERY Responses endpoint, so false means caching is off harness-wide (the server&#39;s --no-prompt-cache), NOT that this endpoint cannot cache. Whether an upstream HONOURS the ask is not statically knowable and this field does not claim it. Clients SHOULD mark a false row rather than hide it; scoping the mark to Anthropic-family ids is the useful convention, since those cache solely on an explicit ask while an implicit cacher may cache regardless. Additive: an older client reads false. |




#### `mecatl.v1.ModelRetry`

ModelRetry identifies the failed-step retry intent being driven.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `retry_disposition` | `RetryDisposition` |  |  |  |
| `stream_progress` | `StreamProgress` |  |  |  |




#### `mecatl.v1.MutateLearnedSkillRequest`



| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `project` | `string` |  |  |  |
| `owner_agent` | `string` |  |  |  |
| `id` | `string` |  |  |  |
| `version` | `string` |  |  |  |
| `expected_revision` | `string` |  |  |  |




#### `mecatl.v1.MutateLearnedSkillResponse`



| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `skill` | `LearnedSkillVersion` |  |  |  |
| `generation` | `uint64` |  |  |  |
| `project` | `string` |  |  |  |
| `publication_status` | `string` |  |  |  |
| `publication_error` | `string` |  |  |  |




#### `mecatl.v1.MutateLearningAttemptRequest`



| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `id` | `string` |  |  |  |
| `expected_version` | `string` |  |  |  |




#### `mecatl.v1.MutateLearningAttemptResponse`



| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `attempt` | `LearningAttempt` |  |  |  |




#### `mecatl.v1.Parallel`

Parallel is the BOUNDED observability projection carried by the three parallel.*
events (parallel.start / parallel.branch / parallel.end). Mirrors
session.ParallelPayload. Like Subagent it now forwards BOUNDED previews (ADR 0079)
on branch_tool events — text is a rune-capped preview of the branch&#39;s message
text, detail a rune-capped preview of a branch tool call&#39;s args or result body —
so a client can render a Parallel fork-join run&#39;s activity (a join strategy, a
grouped roster of branches with tool traces and a winner highlight) without ever
seeing unbounded branch content, preserving context isolation. Physical fork roots,
exact environment refs, and reusable placement authority are never projected.

Unlike the FLAT Subagent fleet, a Parallel run is a GROUP: N branches of ONE call
(keyed by parent_call_id) sharing a join strategy and a single winner. The
per-branch events carry metadata keyed by branch_index.

Which fields are set depends on the event kind:
  - parallel.start:                  parent_call_id, join, branch_count.
  - parallel.branch (branch_start):  parent_call_id, kind, branch_index, branch_label, goal, routed_category, routed_model, model.
  - parallel.branch (branch_tool):   parent_call_id, kind, branch_index, tool_name, is_error, tool_count,
                                     and — when a preview is available — text / detail / inner_kind.
  - parallel.branch (branch_end):    parent_call_id, kind, branch_index, tool_count, stop, usage, duration_ms, failed.
  - parallel.end:                    parent_call_id, join, branch_count, winner, usage (run total), stop.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `parent_call_id` | `string` |  |  | parent_call_id is the parent&#39;s Parallel tool-call id and the GROUP key (one Parallel call = one group); attributes the event to the originating card. |
| `kind` | `string` |  |  | kind discriminates the per-branch lifecycle transition on a parallel.branch event (branch_start / branch_tool / branch_end); empty on the run-level start/end events. |
| `join` | `string` |  |  | join is the normalized join strategy (all / first / judge); set on parallel.start and parallel.end. |
| `branch_count` | `int32` |  |  | branch_count is the number of branches in the run; set on parallel.start and parallel.end. |
| `branch_index` | `int32` |  |  | branch_index is the 0-based stable per-branch group key; set on every parallel.branch kind. |
| `branch_label` | `string` |  |  | branch_label is the humanized 1-based branch label (&#34;branch-1&#34; …); set on branch_start. |
| `goal` | `string` |  |  | goal is a short, plain-text label for the branch task (a truncation of the model-authored branch prompt — the parent&#39;s own instruction, never branch content); set on branch_start. |
| `tool_name` | `string` |  |  | tool_name is the name of a branch&#39;s child tool that just ran (branch_tool) — the name alone, never branch args or result (the bounded previews ride the text / detail fields). |
| `is_error` | `bool` |  |  | is_error reports whether that branch tool call failed (branch_tool). |
| `tool_count` | `int32` |  |  | tool_count is the running (branch_tool) or final (branch_end) child tool-call count for the branch. |
| `failed` | `bool` |  |  | failed reports whether the branch&#39;s child run failed (branch_end). |
| `stop` | `string` |  |  | stop is the branch&#39;s terminal stop reason (branch_end) or the run-level stop (parallel.end; the winner&#39;s stop for first/judge, empty for all). |
| `usage` | `Usage` |  |  | usage is the branch&#39;s cumulative usage (branch_end) or the run TOTAL summed across branches (parallel.end). |
| `duration_ms` | `int64` |  |  | duration_ms is the branch&#39;s wall-clock duration in milliseconds (branch_end). |
| `winner` | `int32` |  |  | winner is the branch index of the selected winner on parallel.end — a real branch_index for first/judge, or -1 for all / none-succeeded. |
| `child_id` | `string` |  |  | child_id is the branch&#39;s child SESSION id (&#34;parallel-&lt;callID&gt;-&lt;i&gt;&#34;) on branch_start/branch_end — the CancelChild handle, carried explicitly so a client never derives ids from the (internal) id grammar. |
| `routed_category` | `string` |  |  | routed_category is the OPT-IN semantic model router&#39;s chosen CATEGORY label for this branch (branch_start only), set when the router was wired AND classified this branch (empty otherwise — no router, a fail-soft miss that inherited the default branch model, or a branch cancelled before it started). BARE METADATA — a label, never the branch prompt or classifier reasoning — so it is context-isolation safe (gauntlet #7: no branch content crosses). Mirrors session.ParallelPayload.RoutedCategory. |
| `routed_model` | `string` |  |  | routed_model is the concrete MODEL id the routed branch was minted on (branch_start only), paired with routed_category. BARE METADATA — a model id, never model-influenced free text — so it is context-isolation safe (gauntlet #7). Mirrors session.ParallelPayload.RoutedModel. |
| `model` | `string` |  |  | model is the concrete MODEL id this branch ACTUALLY ran on (branch_start only), regardless of how it was chosen — inherited default branch model or the opt-in router. BARE METADATA — a model id, never branch content — so it is context-isolation safe (gauntlet #7: no branch content crosses). When the router classified this branch, model == routed_model. Mirrors session.ParallelPayload.Model. |
| `inner_kind` | `string` |  |  | inner_kind discriminates which inner branch event kind the preview came from (message.delta / tool.call / tool.result / result); set on branch_tool alongside text / detail (ADR 0079 bounded previews). A branch&#39;s permission.ask is never projected. STRING passthrough, mirroring Team.inner_kind. |
| `text` | `string` |  |  | text is a BOUNDED preview of the branch&#39;s message/result text (branch_tool, for the message.delta / result inner kinds) — control-byte scrubbed and rune-capped upstream in engine/agent, never the raw, unbounded body. |
| `detail` | `string` |  |  | detail is a BOUNDED preview of a branch tool call&#39;s args (tool.call) or a tool result&#39;s body (tool.result) — control-byte scrubbed and rune-capped upstream, never the raw, unbounded args/result body (branch_tool). |
| `routing_reason` | `string` |  |  | routing_reason is a BOUNDED harness/composition reason string explaining WHY this branch was NOT routed by the semantic model router (branch_start only) — empty on a routed HIT (routed_category/routed_model carry the hit). A short label (e.g. &#34;router-disabled&#34;, &#34;route-target-unavailable&#34;, &#34;aborted&#34;, or a RouterMiss* classifier miss) — BARE METADATA, never the branch prompt or classifier reasoning — so it is context-isolation safe (gauntlet #7: no branch content crosses). Clamped at the emit site. Mirrors session.ParallelPayload.RoutingReason. |
| `routing_decision` | `RoutingDecision` | optional |  | routing_decision is the optional bounded configured-router evidence captured on branch_start. Historical events and deployments without a router omit it. |




#### `mecatl.v1.PermissionAsk`

PermissionAsk describes a permission prompt the loop is blocked on. The
client resolves it by sending ResumeApproval with the matching ask_id.
Mirrors session.PendingAsk.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `ask_id` | `string` |  |  | ask_id correlates the ask with the client&#39;s resolution. |
| `tool` | `string` |  |  | tool is the name of the tool awaiting approval. |
| `args` | `string` |  |  | args is the proposed tool-call argument payload (raw JSON). |
| `reason` | `string` |  |  | reason explains why approval is required. |
| `guardrail` | `GuardrailApprovalScope` |  |  |  |
| `call_id` | `string` | optional |  | call_id is the exact durable tool-call ID for presentation correlation. Absence means no tool-row attachment can be proven. |




#### `mecatl.v1.PlacementMetadata`

PlacementMetadata is safe display data. It never contains a filesystem path,
backend locator, exact EnvironmentRef, or reusable selector.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `kind` | `string` |  |  |  |
| `label` | `string` |  |  |  |
| `branch` | `string` |  |  |  |
| `revision` | `string` |  |  |  |




#### `mecatl.v1.PlanContinuationFailure`

PlanContinuationFailure correlates a known failed server-owned proceed start.
No execution run exists, so Event.run_id is empty.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `plan_run_id` | `string` |  |  |  |
| `ask_id` | `string` |  |  |  |




#### `mecatl.v1.PlanSessionCleanupRequest`



| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `kinds` | `string` | repeated |  |  |




#### `mecatl.v1.PlanSessionCleanupResponse`



| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `confirmation_token` | `string` |  |  |  |
| `available` | `bool` |  |  |  |
| `unavailable_reason` | `string` |  |  |  |
| `generation` | `string` |  |  |  |
| `policy_version` | `string` |  |  |  |
| `eligible` | `CleanupCandidate` | repeated |  |  |
| `protected` | `CleanupCounts` |  |  |  |
| `estimated_bytes` | `int64` |  |  |  |
| `planned_job_id` | `string` |  |  |  |
| `eligible_counts` | `CleanupCounts` |  |  |  |




#### `mecatl.v1.Principal`

Principal is the verified caller an object is attributed to (ADR 0204 — caller
identity, threaded). Identity is the (issuer, subject) PAIR, never subject alone:
two IdPs or realms collide on `sub`. It carries NO scopes, NO credentials and NO
claims map — attribution only; enforcement is the isolation track&#39;s.

An ABSENT principal is an ABSENT message (a nil owner), never a present-but-empty
one — &#34;nobody&#34; must never be renderable as an anonymous somebody.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `issuer` | `string` |  |  | issuer is the IdP that minted the token (the canonical `iss` claim). |
| `subject` | `string` |  |  | subject is the caller id within that issuer (the `sub` claim). |
| `grant_type` | `string` |  |  | grant_type is how the caller authenticated: &#34;user&#34;, &#34;client_credentials&#34; or &#34;system&#34;. A bare string passthrough (no enum) — the domain owns the closed set. |
| `name` | `string` |  |  | name is an optional human-readable display label. It is NEVER part of identity — display only. |




#### `mecatl.v1.Prompt`

Prompt is a normal Converse start frame. It names the session the run
executes against and the user prompt to process. The prompt is text and/or
media: at least one of text/parts must be non-empty. The &#34;text or parts
non-empty&#34; rule is a CROSS-FIELD constraint enforced by the server (the proto
only carries per-field constraints), which is why text no longer carries a
min_len — it is optional when parts is set.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `session_id` | `string` |  |  | session_id is the session this run belongs to. |
| `text` | `string` |  |  | text is the flattened user prompt text; optional when parts is set. |
| `parts` | `Content` | repeated |  | parts carries non-text media (image/audio) alongside the text. |
| `server_owned_plan_continuation` | `bool` |  |  | server_owned_plan_continuation opts this run into exact plan-ask controls and makes the daemon responsible for starting its approved proceed run. |




#### `mecatl.v1.ProviderStatus`

ProviderStatus is one provider&#39;s last live-listing outcome, for client
surfacing (issue #262). state is a STRING passthrough (no enum — the
EvNoProgress/StopBudget discipline): &#34;ok&#34; | &#34;unreachable&#34; | &#34;unauthorized&#34; | &#34;empty&#34;.

Rows are scoped to OPERATOR-ACTIONABLE live-inventory providers: currently
config-detected intent gateways and the manually configured openai-codex
entitlement boundary. A keyed openrouter/anthropic live-listing blip never
grows this list. The additive fields carry 0/false for every deployment
without a surfaced provider — byte-identical to before this wave.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `provider_id` | `string` |  |  |  |
| `state` | `string` |  |  |  |
| `hint` | `string` |  |  | short human remediation, e.g. &#34;start it with `thv llm proxy start`&#34; |
| `default_model_auto_selected` | `bool` |  |  | default_model_auto_selected is true ONLY when provider_id is the DEFAULT provider AND the server AUTO-selected its default model (a first-listed heal/probe pick, issue #262 R2.4) — never true when an operator configured --model/--default-model. A client uses this (rather than a vendor-name check) to label the current model &#34;(auto-selected)&#34; honestly for ANY surfaced provider, present or future. |
| `model_count` | `int32` |  |  | model_count is the count of models this surfaced provider&#39;s last successful live listing returned (derived from the outcome store&#39;s last-known-good snapshot). 0 on empty/unreachable/unrecorded — so a client cannot distinguish &#34;0 models&#34; from &#34;never probed&#34; by this field alone (state disambiguates: state==&#34;empty&#34; with model_count==0 is a genuine empty list; state==&#34;unreachable&#34; with model_count==0 is a probe failure). |
| `available_not_default` | `bool` |  |  | available_not_default is true ONLY when this intent-driven provider is registered, reachable (state == &#34;ok&#34;), AND is NOT the active default provider (a key-driven provider outranks it on the precedence ladder). Named for the CONDITION (vendor-neutral, matching the default_model_auto_selected discipline) so a client can surface &#34;the gateway is available, set a key to use it&#34; for ANY intent-driven provider, present or future — without a vendor-name check. False whenever the provider IS the default (so the sole-provider case does not advertise itself as &#34;available but not default&#34;). |




#### `mecatl.v1.ReadMcpResourceRequest`

ReadMcpResourceRequest reads one resource by URI from a named server.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `server` | `string` |  |  | server is the connected server to read from. |
| `uri` | `string` |  |  | uri is the resource URI to read. |




#### `mecatl.v1.ReadMcpResourceResponse`

ReadMcpResourceResponse carries the read resource&#39;s content chunks.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `contents` | `McpResourceContents` | repeated |  | contents are the resource&#39;s content chunks. |




#### `mecatl.v1.RecheckMcpAuthorizationRequest`

RecheckMcpAuthorizationRequest is one client frame. The mandatory first
frame carries only owner-scoped correlation in session_id/authorization_id;
subsequent frames leave those fields empty and carry one control.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `session_id` | `string` |  |  |  |
| `authorization_id` | `string` |  |  |  |
| `resume_approval` | `ResumeApproval` |  | `control` |  |
| `cancel` | `Cancel` |  | `control` |  |




#### `mecatl.v1.RecheckMcpAuthorizationResponse`

RecheckMcpAuthorizationResponse wraps one authoritative status or continuation event.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `event` | `Event` |  |  |  |




#### `mecatl.v1.ReflectSessionRequest`



| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `session_id` | `string` |  |  |  |




#### `mecatl.v1.ReflectSessionResponse`



| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `receipt` | `ReflectionReceipt` |  |  |  |




#### `mecatl.v1.ReflectionReceipt`



| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `reflection_id` | `string` |  |  |  |
| `disposition` | `string` |  |  |  |
| `queued` | `int32` |  |  |  |
| `abstained` | `bool` |  |  |  |
| `staged` | `int32` |  |  |  |
| `promoted` | `int32` |  |  |  |
| `conflicted` | `int32` |  |  |  |
| `reason` | `string` |  |  | reason is one closed materialization reason when abstained is true. |
| `message` | `string` |  |  | message is stable harness-authored display text for that closed reason. |




#### `mecatl.v1.RefreshMcpSourcesRequest`

RefreshMcpSourcesRequest names the owned session whose direct-name authority
may be widened after reconciliation.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `session_id` | `string` |  |  |  |




#### `mecatl.v1.RefreshMcpSourcesResponse`

RefreshMcpSourcesResponse identifies the request-pinned runtime snapshot.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `revision` | `uint64` |  |  |  |
| `changed` | `bool` |  |  |  |




#### `mecatl.v1.RenameSessionRequest`



| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `session_id` | `string` |  |  |  |
| `title` | `string` |  |  |  |




#### `mecatl.v1.RenameSessionResponse`



| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `session` | `Session` |  |  |  |




#### `mecatl.v1.ResolvePlanAskRequest`

ResolvePlanAskRequest addresses one plan-originated ask on one exact run.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `session_id` | `string` |  |  |  |
| `expected_run_id` | `string` |  |  |  |
| `ask_id` | `string` |  |  |  |
| `verdict` | `ApprovalVerdict` |  |  |  |




#### `mecatl.v1.ResolvePlanAskResponse`

ResolvePlanAskResponse acknowledges only the accepted run/ask correlation.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `run_id` | `string` |  |  |  |
| `ask_id` | `string` |  |  |  |




#### `mecatl.v1.ResolveRunAskRequest`

ResolveRunAskRequest addresses one ordinary permission ask on one exact run.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `session_id` | `string` |  |  |  |
| `expected_run_id` | `string` |  |  |  |
| `ask_id` | `string` |  |  |  |
| `verdict` | `ApprovalVerdict` |  |  |  |
| `review_id` | `string` |  |  | review_id and guardrail_kind acknowledge the purpose shown by a contextual guardrail prompt. Both are required for scoped action/result-release asks and omitted together for ordinary permission asks. |
| `guardrail_kind` | `GuardrailApprovalKind` |  |  |  |




#### `mecatl.v1.ResolveRunAskResponse`

ResolveRunAskResponse acknowledges only the accepted run/ask correlation.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `run_id` | `string` |  |  |  |
| `ask_id` | `string` |  |  |  |




#### `mecatl.v1.ResolvedModel`

ResolvedModel is the per-session EFFECTIVE model: the provider+model id this
session resolved to and its context window. Composition-computed and echoed
verbatim (see CreateSessionResponse.resolved_model); the model is FIXED per TURN
and re-resolved between turns when the permission mode changes the effective
model (plan-slot / opusplan, ADR 0030 Layer 3). The client resolves a human
display name from its ListModels inventory by (provider_id, model_id) — no
display_name is carried here.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `provider_id` | `string` |  |  | provider_id is the resolved provider (e.g. &#34;openai&#34;, &#34;anthropic&#34;). |
| `model_id` | `string` |  |  | model_id is the resolved model id (the opaque id the adapter was given). |
| `context_window` | `int64` |  |  | context_window is the resolved model&#39;s context window in tokens (0 = unknown). |
| `reasoning_effort` | `string` |  |  | reasoning_effort is the EFFECTIVE reasoning-effort tier this session resolved to, enum-as-string (the same neutral vocabulary as CreateSessionRequest.reasoning_effort, NO proto enum): &#34;&#34; when unset (the provider default applies), else the normalised + per-provider-CLAMPED token actually wired into the adapter — e.g. an OpenAI session that requested &#34;max&#34; echoes &#34;high&#34;. The client renders it (e.g. in the model footer) and never recomputes it. |




#### `mecatl.v1.Result`

Result is the terminal payload carried by a result event. Mirrors
session.ResultPayload.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `stop` | `string` |  |  | stop is the reason the run ended (end_turn, max_turns, cancelled, ...). |
| `text` | `string` |  |  | text is the final assistant text, if any. |
| `usage` | `Usage` |  |  | usage is the token accounting accumulated during THIS run; it is not the durable session total and is distinct from per-turn TurnEnd.usage. |
| `error` | `string` |  |  | error carries the failure detail when stop is &#34;error&#34; (empty otherwise). |
| `retry_disposition` | `RetryDisposition` | optional |  | retry_disposition is always present on terminal results from current servers; absence identifies an older server, while explicit UNKNOWN is conservative. |
| `stream_progress` | `StreamProgress` | optional |  | stream_progress is presence-aware for the same old-server distinction. |




#### `mecatl.v1.ResumeApproval`

ResumeApproval releases a paused permission.ask, allowing or denying the
proposed tool call. The ask_id echoes the PermissionAsk.ask_id from the
event stream.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `ask_id` | `string` |  |  | ask_id correlates the resolution with the paused ask. |
| `verdict` | `ApprovalVerdict` |  |  | verdict is the required three-way resolution: deny, allow this call once, or allow always (which additionally learns a per-session allow rule for the matching tool + exact pattern). |
| `expected_run_id` | `string` |  |  | expected_run_id, when set, scopes this control to ONE run: the server refuses it if the session&#39;s current run is a different one, and the newer run is left untouched.  It closes a real race, not a hypothetical one. Controls are addressed at a SESSION, so a control still in flight when a run ends would otherwise land on whatever run started next — approving a tool call the user never saw, or cancelling work they did not ask to stop. `seq` cannot distinguish the two runs (it restarts each run), so before run ids there was no way to express &#34;this one&#34;.  EMPTY is the legacy behaviour exactly: the control applies to whatever run is current. Existing clients are unaffected; a client that knows the run id it is acting on should always send it. |
| `review_id` | `string` |  |  | review_id and guardrail_kind acknowledge the purpose shown by a contextual guardrail prompt. They are mandatory and must exactly echo PermissionAsk.guardrail for EVERY guardrail-scoped ask (both ACTION and RESULT_RELEASE). They are absent only for ordinary permission asks. |
| `guardrail_kind` | `GuardrailApprovalKind` |  |  |  |




#### `mecatl.v1.RetentionPolicy`



| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `main_max_age_seconds` | `int64` |  |  |  |
| `main_max_count` | `int32` |  |  |  |
| `child_max_age_seconds` | `int64` |  |  |  |
| `child_max_count` | `int32` |  |  |  |
| `scheduled_max_age_seconds` | `int64` |  |  |  |
| `scheduled_max_count` | `int32` |  |  |  |
| `sweep_cadence_seconds` | `int64` |  |  |  |




#### `mecatl.v1.RetryStart`



| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `session_id` | `string` |  |  | session_id is the failed session whose exact model step should be retried. |




#### `mecatl.v1.RollbackLearnedSkillRequest`



| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `project` | `string` |  |  |  |
| `owner_agent` | `string` |  |  |  |
| `id` | `string` |  |  |  |
| `target_version` | `string` |  |  |  |
| `expected_revision` | `string` |  |  |  |




#### `mecatl.v1.RoutingDecision`

RoutingDecision is bounded configured-router evidence attached to a delegation
start projection. Existing model/routed_*/routing_reason fields remain authoritative
for the child that actually ran and the final routing result.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `backend` | `string` |  |  |  |
| `classifier_model` | `string` |  |  |  |
| `candidate_category` | `string` |  |  |  |
| `candidate_model` | `string` |  |  |  |
| `confidence` | `double` | optional |  |  |
| `minimum_confidence` | `double` | optional |  |  |
| `outcome` | `string` |  |  |  |
| `consecutive_misses` | `int32` |  |  |  |
| `miss_limit` | `int32` |  |  |  |
| `breaker_open` | `bool` |  |  |  |




#### `mecatl.v1.RunTeamRequest`

RunTeamRequest drives a team to quiescence, streaming member events.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `team_id` | `string` |  |  | team_id is the team to run. |




#### `mecatl.v1.SchedulePayload`

SchedulePayload is the structured detail carried by the schedule.* events
(EvScheduleFired / EvScheduleSkipped / EvScheduleFailed). Mirrors
session.SchedulePayload. Emitted from composition (the scheduler) at fire
time, NOT the agent loop. For v1 delivered to the fire session&#39;s durable
EventLog ONLY (pull-only via GetFire/ListFires); a live broadcast stream is a
future phase. kind / stop / err are STRING passthroughs (the EvNoProgress /
StopBudget discipline — no proto enum). Defined here (not schedule.proto)
because it is the Event.schedule projection — Event lives in harness.proto,
and a cross-file reference back from harness.proto into schedule.proto would
form a circular import (schedule.proto imports harness.proto for the shared
Content/PermissionMode/Limits). Colocating the projection with Event breaks
the cycle cleanly.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `schedule_name` | `string` |  |  | schedule_name is the schedule that fired / was skipped / failed. |
| `fire_id` | `string` |  |  | fire_id is the per-fire session id (the same id ScheduleFire.id / ScheduleFire.session_id carry). |
| `session_id` | `string` |  |  | session_id is the session the fire ran as (== fire_id for a fired fire; empty for a skipped fire). |
| `kind` | `string` |  |  | kind is the event kind: &#34;fired&#34; / &#34;skipped&#34; / &#34;failed&#34;. String passthrough, no proto enum. |
| `stop` | `string` |  |  | stop is the terminal stop reason of the fire&#39;s run (a session.StopReason string passthrough). Empty for a skipped fire. |
| `err` | `string` |  |  | err is the error string if the fire&#39;s run failed, empty otherwise. |




#### `mecatl.v1.SendTeammateMessageRequest`

SendTeammateMessageRequest posts a message into a member&#39;s inbox.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `team_id` | `string` |  |  | team_id is the team the recipient belongs to. |
| `to` | `string` |  |  | to is the recipient member name. |
| `from` | `string` |  |  | from is the sender label (e.g. &#34;operator&#34;); empty defaults to &#34;operator&#34;. |
| `body` | `string` |  |  | body is the message text. |




#### `mecatl.v1.SendTeammateMessageResponse`

SendTeammateMessageResponse is the empty acknowledgement.

This message has no fields.



#### `mecatl.v1.ServerCapabilities`

ServerCapabilities reports which optional features this server has ENABLED,
so a client can present an honest UI (advertise only reachable features, and
explain an empty inventory as &#34;not enabled&#34; vs &#34;enabled but empty&#34;). Each
field reflects the BUILT service state (registered tools / wired seams), never
a static guess — see internal/adapter/server.Service.capabilities(). All
fields default false, so a client talking to an OLDER server (no caps field)
degrades safely to &#34;nothing advertised&#34; rather than over-promising. The set is
designed to grow additively: a new feature adds a new `bool field = N;` that
old clients ignore and new clients reading an old server see as false.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `mcp` | `bool` |  |  | mcp is true when MCP inventory/resources/prompts are available (an MCP provider is wired). Gates ctrl+o / ctrl+r / f8. |
| `slash_commands` | `bool` |  |  | slash_commands is true when slash-command discovery is available (a command lister is wired). Gates the / palette. |
| `memory` | `bool` |  |  | memory is true when cross-session memory tools (Remember/Recall) are registered. Agent-side: no overlay, surfaced as prose only. |
| `skills` | `bool` |  |  | skills is true when the Skill tool is registered. Gates the /skills inventory browser (the read-only ListSkills snapshot). Activation stays the model&#39;s concern — the panel is discovery only. |
| `teams` | `bool` |  |  | teams is true when agent teams are enabled (a member-engine factory is wired). Gates the f6 deep view&#39;s relevance. |
| `shell` | `bool` |  |  | shell reports availability of the canonical Shell tool. False means Shell is not registered (for example, the operator ran --no-shell). |
| `image` | `bool` |  |  | image is true when the wired LLM provider consumes image prompt parts. Gates the @-mention file-attach UX for images (a client refuses to send an image part to a server whose provider cannot read it). |
| `audio` | `bool` |  |  | audio is true when the wired LLM provider consumes audio prompt parts. Gates the @-mention file-attach UX for audio. |
| `agents` | `bool` |  |  | agents is true when the agent-definition registry is resolvable and served (ListAgents returns the resolved snapshot). Independent of `teams` (the run-path member-engine): defs can be browsable without teams enabled. Gates the /agents definition-inventory panel. |
| `soul` | `bool` |  |  | soul is true when a soul source is wired (a soul was selected at startup, so GetSoul returns its snapshot). Gates the /soul read-only inspection panel. |
| `user_model` | `bool` |  |  | user_model is true when the user-model store is wired (GetUserModel reads its live index). Gates the /usermodel read-only inspection panel. |
| `model_selection` | `bool` |  |  | model_selection is true when &gt;=1 provider is available (ListModels would return &gt;0). Gates the client&#39;s /models picker the same way `agents` gates /agents. Additive: an older server leaves it false and the client hides the picker. (multi-provider Phase 0, S3.) |
| `posture` | `string` |  |  | posture is the SERVER-WIDE operator posture-ladder tier (&#34;strict&#34;/&#34;trusted&#34;/ &#34;auto&#34;/&#34;yolo&#34;). It is CHROME ONLY — a client renders an honest &#34;⚠ auto&#34;/&#34;⚠ yolo&#34; badge so an operator can see the daemon&#39;s automation posture at a glance — and is NEVER session state (it does not change per session; permission MODE is the per-session knob). String passthrough, no enum on the wire (the EvNoProgress / StopBudget discipline). Additive: an older server leaves it empty and the client shows no badge (strict/trusted also show no badge). |
| `worktrees` | `bool` |  |  | worktrees is true when a WorktreeLister is wired (ListWorktrees may return a non-empty list for a real git repo). Gates the client&#39;s /worktrees overlay — the first-class operator workflow for binding a session to an EXISTING sibling git worktree (issue #102). Additive: an older server leaves it false and the client hides the overlay. A no-FS/cloud server (no lister wired) also yields false, so the feature is honestly absent there. |
| `scheduling` | `bool` |  |  | scheduling is true when a ScheduleStore is reachable (the schedule RPCs are functional: Create/List/Get/Pause/Resume/Delete/FireNow/GetFire/ListFires). Gates the client&#39;s /schedule overlay. Independent of the scheduler tick loop (--scheduler): the store-backed RPCs work without the tick loop. Additive: an older server leaves it false and the client hides the overlay. |
| `reflection` | `bool` |  |  | reflection is true when explicit completed-session reflection is available. |
| `learning_proposals` | `bool` |  |  | learning_proposals is true when proposal review and promotion are available. |
| `learned_skills` | `bool` |  |  | learned_skills is true when the caller-partitioned lifecycle API is available. |
| `storage_cleanup` | `bool` |  |  | storage_cleanup advertises the authenticated plan/apply/job maintenance API under destructive-mutation exclusion. |
| `manual_dream` | `ManualDreamCapabilities` |  |  | manual_dream is the composition-time availability snapshot for the two deployment-owned manual consolidation targets. An older server leaves it absent; unavailable reasons are bounded, content-free operator categories. |
| `storage_health` | `bool` |  |  | storage_health is true only when the backend implements bounded indexed health and this deployment has a management authorizer. |
| `steer` | `bool` |  |  | steer is true when the server&#39;s engines arm the mid-run steer inbox (steer-while-running, issue #512): a client may then send multimodal `steer` / `steer_cancel` frames on the live Converse stream and render the EvSteer drain echo. When false because steer is disabled in composition, the client keeps its local merge-queue behaviour — a steer frame sent to such a server is promoted to a fresh follow-up run (the engine reports too_late on its disarmed inbox), never silently dropped. |
| `manual_compaction` | `bool` |  |  | manual_compaction is true when the server can apply an out-of-band session compaction pass to an idle owned session. Gates the client&#39;s /compact action. |
| `session_debug` | `bool` |  |  | session_debug is true when this server can create and rehydrate dedicated no-filesystem debug sessions. |
| `debug_mcp` | `bool` |  |  | debug_mcp is true when debug sessions may explicitly borrow selected direct tools from configured server-global streaming-HTTP MCP servers. |
| `workspace_enrollment` | `bool` |  |  | workspace_enrollment is true when protected workspace services must be admitted as one complete bundle before the first prompt. |
| `mcp_connector_status` | `bool` |  |  | mcp_connector_status requires a wired broker inspector, enforced ownership and a verified caller. It does not enable direct MCP resources or prompts. |
| `mcp_refresh` | `bool` |  |  | mcp_refresh is true when direct/global MCP source reconciliation is wired. It is mutually exclusive with workspace_enrollment in a valid deployment. |
| `artifacts` | `bool` |  |  | artifacts is true when this deployment can store and serve private artifacts. |




#### `mecatl.v1.Session`

Session is a snapshot of server-side session state.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `session_id` | `string` |  |  | session_id is the session&#39;s id. |
| `state` | `string` |  |  | state is the lifecycle state (idle/running/awaiting/completed/...). |
| `mode` | `PermissionMode` |  |  | mode is the permission posture. |
| `limits` | `Limits` |  |  | limits are the configured stop conditions. |
| `turns` | `int32` |  |  | turns is the number of model calls begun. |
| `tool_calls` | `int32` |  |  | tool_calls is the total number of tool invocations recorded. |
| `created_at_unix` | `int64` |  |  | created_at_unix is the creation timestamp in Unix seconds. |
| `resolved_model` | `ResolvedModel` |  |  | resolved_model is the EFFECTIVE provider+model this session resolved to (composition-computed, echoed verbatim; FIXED per TURN, re-resolved between turns on a permission-mode change — plan-slot / opusplan, ADR 0030 Layer 3). Mirrors CreateSessionResponse.resolved_model so a snapshot reader (GetSession) sees the same effective model the create response carried — and is the canonical place a client re-reads the model AFTER a mode change. Nil/absent from an older server → client falls back to today&#39;s behavior. |
| `kind` | `string` |  |  | kind and relationship preserve the durable trusted-producer identity. |
| `relationship` | `SessionRelationship` |  |  |  |
| `debug_mcp_servers` | `string` | repeated |  | Selected global server names and the exact creation-time direct-tool ceiling. These contain no URLs, headers, credentials, or inline MCP configuration. |
| `debug_mcp_tools` | `string` | repeated |  |  |
| `placement` | `PlacementMetadata` |  |  |  |
| `title_metadata` | `SessionTitle` |  |  | title_metadata is the canonical bounded, source-free title lifecycle projection. |
| `token_usage` | `Session.TokenUsageEntry` | repeated |  | token_usage is the canonical durable session token accounting. Map keys identify usage types; `main` is ordinary main-session agent usage. Other keys are server-defined and are not enumerated here. |
| `session_capabilities` | `SessionCapabilities` |  |  | session_capabilities is the selected provider+model&#39;s authoritative media input capability. It mirrors CreateSessionResponse.session_capabilities so GetSession, resume, clear, and fork retain accurate media gates. Nil on an older server tells clients to fall back to the server-wide capabilities. |




#### `mecatl.v1.Session.TokenUsageEntry`



| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `key` | `string` |  |  |  |
| `value` | `TokenUsage` |  |  |  |




#### `mecatl.v1.SessionCapabilities`

SessionCapabilities is the per-session resolved input capability: ONLY the
model-varying input bits (image/audio). A dedicated message (not a reuse of
ServerCapabilities) keeps the per-session surface MINIMAL — the other
ServerCapabilities bits (mcp/skills/teams/...) are server-wide and would be
misleading per session. Reasoning is intentionally EXCLUDED: it is discoverable
via ListModels for the selected id and is not a per-session input-gating
concern (no adapter &#34;can replay reasoning&#34; authority bit exists). The value is
the catalog ∩ adapter intersection computed in composition. (Phase 0, S5.)

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `image` | `bool` |  |  | image is true when the resolved provider+model can consume image prompt parts. |
| `audio` | `bool` |  |  | audio is true when the resolved provider+model can consume audio prompt parts. |
| `pdf` | `bool` |  |  | pdf is true only when the selected model and native provider accept PDFs and this deployment has PDF artifact storage enabled. |




#### `mecatl.v1.SessionInventoryActionReasons`

SessionInventoryActionReasons explains each disabled inventory action without
requiring a client to duplicate server eligibility policy.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `public_chat` | `string` |  |  |  |
| `inspect` | `string` |  |  |  |
| `copy_id` | `string` |  |  |  |
| `view_transcript` | `string` |  |  |  |
| `fork` | `string` |  |  |  |
| `rename` | `string` |  |  |  |
| `delete` | `string` |  |  |  |




#### `mecatl.v1.SessionInventoryCapabilities`

SessionInventoryCapabilities is the picker-safe action posture for one row.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `public_chat` | `bool` |  |  |  |
| `inspect` | `bool` |  |  |  |
| `authoritative_transcript` | `bool` |  |  | authoritative_transcript reports snapshot-derived transcript availability. |
| `activity_replay` | `bool` |  |  | activity_replay reports the independent optional EventLog replay plane. |
| `copy_id` | `bool` |  |  |  |
| `view_transcript` | `bool` |  |  |  |
| `fork` | `bool` |  |  |  |
| `rename` | `bool` |  |  |  |
| `delete` | `bool` |  |  |  |
| `reasons` | `SessionInventoryActionReasons` |  |  | reasons carries one closed machine-readable reason per disabled action. |




#### `mecatl.v1.SessionRelationship`

SessionRelationship carries the validated links appropriate to a session kind.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `parent_session_id` | `string` |  |  |  |
| `call_id` | `string` |  |  |  |
| `branch_index` | `int32` | optional |  |  |
| `schedule_name` | `string` |  |  |  |
| `origin_session_id` | `string` |  |  |  |
| `team_id` | `string` |  |  |  |
| `member_name` | `string` |  |  |  |
| `debug_target_session_id` | `string` |  |  |  |




#### `mecatl.v1.SessionSummary`

SessionSummary is one stored session&#39;s picker metadata — id, timestamps, state,
turn count, and the resolved model id. It carries NO conversation content: it is
the cheap row a client renders in an &#34;open existing session&#34; picker (issue #245
Phase 1). `model_id` is a bare opaque string (NOT a full ResolvedModel) to keep
the picker row cheap and provider-neutral — the client resolves a display name
from its ListModels inventory by (provider_id, model_id) if it needs one.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `session_id` | `string` |  |  | session_id is the stored session&#39;s id. |
| `modified_at_unix` | `int64` |  |  | modified_at_unix is the last-write timestamp in Unix seconds (the PrunableStore row mtime; the sort key for the picker). |
| `state` | `string` |  |  | state is the persisted lifecycle state (idle/running/awaiting/completed/...). Empty when the snapshot could not be loaded (a corrupt store row still surfaces its id/mtime). |
| `turns` | `int32` |  |  | turns is the persisted model-call count. Zero when the snapshot could not be loaded. |
| `model_id` | `string` |  |  | model_id is the resolved model id this session ran on (bare string, no provider context). Empty when the session never resolved a model or the snapshot could not be loaded. |
| `created_at_unix` | `int64` |  |  | created_at_unix is the creation timestamp in Unix seconds. Zero when the snapshot could not be loaded. |
| `owner` | `Principal` |  |  | owner is the verified caller the session is attributed to (ADR 0204), stamped write-once at CreateSession from the validated token — never from the request body. UNSET for an ownerless session (a no-auth deployment, or a session persisted before the owner label existed; nothing backfills it). |
| `kind` | `string` |  |  | kind and relationship are the durable trusted-producer taxonomy. |
| `relationship` | `SessionRelationship` |  |  |  |
| `capabilities` | `SessionInventoryCapabilities` |  |  | capabilities state which public actions are valid for this row. |
| `reason_code` | `string` |  |  | reason_code explains why a capability is unavailable. Empty means no denial. |
| `placement` | `PlacementMetadata` |  |  |  |
| `title_metadata` | `SessionTitle` |  |  | title_metadata is the canonical bounded, source-free title lifecycle projection. |
| `token_usage` | `SessionSummary.TokenUsageEntry` | repeated |  | token_usage is the canonical durable session token accounting. Map keys identify usage types; `main` is ordinary main-session agent usage. Other keys are server-defined and are not enumerated here. |
| `activity_state` | `string` |  |  | activity_state is the content-free persisted-history projection. It is present only when this server advertises session_activity_inventory. |




#### `mecatl.v1.SessionSummary.TokenUsageEntry`



| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `key` | `string` |  |  |  |
| `value` | `TokenUsage` |  |  |  |




#### `mecatl.v1.SessionTitle`

SessionTitle is the canonical durable, source-free title lifecycle projection.
It never carries title-source prompts, provider error text, or token usage.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `title` | `string` |  |  | title is the authoritative human-readable session label. |
| `provenance` | `string` |  |  | provenance is &#34;first-prompt&#34;, &#34;operator&#34;, &#34;generated&#34;, or empty for legacy/unknown. |
| `generation_state` | `string` |  |  | generation_state is &#34;disabled&#34;, &#34;pending&#34;, &#34;generated&#34;, or &#34;exhausted&#34;. |
| `latest_attempt` | `TitleAttemptSummary` |  |  | latest_attempt is the latest durable attempt without provider error detail. |
| `revision` | `uint64` |  |  | revision advances on each effective durable title metadata mutation; zero is legacy. |




#### `mecatl.v1.SetModeRequest`

SetModeRequest changes a session&#39;s permission posture.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `session_id` | `string` |  |  | session_id is the session to update. |
| `mode` | `PermissionMode` |  |  | mode is the desired permission posture; unspecified defaults to DEFAULT. |




#### `mecatl.v1.SetModeResponse`

SetModeResponse wraps the updated session snapshot returned by SetMode.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `session` | `Session` |  |  | session is the updated session snapshot. |




#### `mecatl.v1.SkillChangeReceipt`



| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `id` | `string` |  |  |  |
| `skill_id` | `string` |  |  |  |
| `name` | `string` |  |  |  |
| `version` | `string` |  |  |  |
| `operation` | `string` |  |  |  |
| `from_state` | `string` |  |  |  |
| `to_state` | `string` |  |  |  |
| `evidence_count` | `int32` |  |  |  |
| `source_session_ids` | `string` | repeated |  |  |
| `source_tool_call_ids` | `string` | repeated |  |  |
| `verdict` | `string` |  |  |  |
| `fixture_ids` | `string` | repeated |  |  |
| `baseline` | `string` |  |  |  |
| `treatment` | `string` |  |  |  |
| `at` | `google.protobuf.Timestamp` |  |  |  |
| `inspect_available` | `bool` |  |  |  |
| `undo_available` | `bool` |  |  |  |




#### `mecatl.v1.SkillEvaluation`



| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `verdict` | `string` |  |  |  |
| `fixture_ids` | `string` | repeated |  |  |
| `baseline` | `string` |  |  |  |
| `treatment` | `string` |  |  |  |
| `reason` | `string` |  |  |  |
| `at` | `google.protobuf.Timestamp` |  |  |  |




#### `mecatl.v1.SkillInfo`

SkillInfo is one discovered skill&#39;s listing metadata: the activation name and
a short, human-facing description for the inventory panel. It carries NO body
— discovery is metadata-only; activation (which reads the body) is a separate
run-path concern handled by the Skill tool.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `name` | `string` |  |  | name is the skill&#39;s stable activation identifier (the value passed to the Skill tool). |
| `description` | `string` |  |  | description is the one-line frontmatter summary that steers WHEN to activate. |
| `agent_owned` | `bool` |  |  | learned lifecycle metadata is absent for immutable external skills. |
| `owner_agent` | `string` |  |  |  |
| `active_version` | `string` |  |  |  |




#### `mecatl.v1.SoulInfo`

SoulInfo is the BUILD-TIME SNAPSHOT of the resolved soul: the selected soul&#39;s
content plus its provenance/trust/drift metadata. It mirrors the
composition-layer soulMeta + the loaded body. It carries NO write path — the
soul is agent-read-only; this is purely for the /soul inspection panel. When
no soul was selected (present=false) the content/hash are empty but provenance
may still report a DROPPED untrusted project soul (trusted=false).

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `content` | `string` |  |  | content is the selected soul&#39;s clean body (the bytes that reach the prompt), capped server-side at the soul byte ceiling (20 KiB). Empty when none. |
| `size_bytes` | `int64` |  |  | size_bytes is the byte length of content, or 0 for none. |
| `sha256` | `string` |  |  | sha256 is the lowercase-hex SHA-256 of content, or &#34;&#34; for none. |
| `present` | `bool` |  |  | present is true when a soul fragment WILL be contributed this run. |
| `provenance` | `SoulProvenance` |  |  | provenance is where the selected (or dropped) soul came from. |
| `trusted` | `bool` |  |  | trusted reflects whether the soul&#39;s provenance is trusted (a user soul always is; a project soul only with --trust-project). |
| `drifted` | `bool` |  |  | drifted is true when the selected soul&#39;s content hash differs from its recorded baseline (informational; a drifted soul still loads unless strict). |




#### `mecatl.v1.SpawnTeammateRequest`

SpawnTeammateRequest enrols a member in a team before it runs.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `team_id` | `string` |  |  | team_id is the team to add the member to. |
| `name` | `string` |  |  | name is the unique member handle peers address messages to. |
| `agent_type` | `string` |  |  | agent_type is the optional agent-definition name this member adopts. |
| `lead` | `bool` |  |  | lead marks the coordinating member (it is not auto-assigned tasks). |
| `mutating` | `bool` |  |  | mutating requests an isolated forked workspace (the member may edit/write). |
| `initial_prompt` | `string` |  |  | initial_prompt is the member&#39;s first-turn input, run in round 0. |




#### `mecatl.v1.SpawnTeammateResponse`

SpawnTeammateResponse returns the enrolled member.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `member` | `TeamMember` |  |  | member is the newly enrolled member. |




#### `mecatl.v1.Steer`

Steer injects an operator instruction into the IN-FLIGHT run
(steer-while-running, issue #512). It never preempts or aborts the in-flight
model call: the run&#39;s steer inbox parks the text (a SINGLE slot — a second
steer while one is pending APPENDS into the pending bundle, merged with a
blank-line separator; the merged bundle still drains as ONE user message.
Replacing a pending bundle is explicit steer_cancel-then-resend) and the
loop drains it at the next turn boundary, recording it as an ordinary user
continuation. The server echoes the authoritative outcome on the stream
(steer.outcome); when the run&#39;s inbox is closed (the run went terminal
behind the client&#39;s &#34;still running&#34; belief) the text is PROMOTED to a fresh
follow-up run, never silently dropped.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `text` | `string` |  |  | text and parts form the operator instruction to inject at the next turn boundary. At least one must be non-empty. |
| `message_id` | `string` |  |  | message_id is the CLIENT-MINTED correlation key for THIS frame (unique per send; empty means uncorrelated). The server echoes the frame&#39;s own id verbatim on its steer.outcome ack. On the EvSteer drain echo it carries a WATERMARK: the LATEST (tail) contributing send&#39;s id of the bundle that just drained — sends up to and including the watermark drained, sends after it are still pending, so the client splits its ordered queue on it. text is NOT a safe correlation key (two sends can carry the same text). |
| `parts` | `Content` | repeated |  | parts carries image/audio content alongside text, using the same validated wire shape as Prompt.parts. A media-only steer is valid. |
| `expected_run_id` | `string` |  |  | expected_run_id, when set, scopes this control to ONE run: the server refuses it if the session&#39;s current run is a different one, and the newer run is left untouched.  It closes a real race, not a hypothetical one. Controls are addressed at a SESSION, so a control still in flight when a run ends would otherwise land on whatever run started next — approving a tool call the user never saw, or cancelling work they did not ask to stop. `seq` cannot distinguish the two runs (it restarts each run), so before run ids there was no way to express &#34;this one&#34;.  EMPTY is the legacy behaviour exactly: the control applies to whatever run is current. Existing clients are unaffected; a client that knows the run id it is acting on should always send it.  On a steer it is additionally STRICT: supplying it means &#34;steer this run or nothing&#34;. A steer that loses the terminal race is normally PROMOTED into a fresh follow-up run; with expected_run_id set it is refused instead, because a caller naming a specific run did not ask to start a different one. |




#### `mecatl.v1.SteerAck`

SteerAck is the proto projection of a steer-frame outcome: the server&#39;s
authoritative report of what happened to a steer / steer_cancel frame.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `outcome` | `SteerOutcome` |  |  | outcome is the closed-enum result (mirrors agent.SteerOutcome). |
| `text` | `string` |  |  | text echoes the steer text the outcome is about (empty for steer_cancel). |
| `promoted` | `bool` |  |  | promoted is true when a too_late steer was promoted to a fresh follow-up run through the run-entry funnel (the text drives a new run instead of draining into the finished one) — never silently dropped. |
| `message_id` | `string` |  |  | message_id echoes the client-minted id of the Steer / SteerCancel frame this ack answers (empty when the frame supplied none), so the client correlates the authoritative outcome with its send and ignores stale acks. |




#### `mecatl.v1.SteerCancel`

SteerCancel retracts the run&#39;s PENDING (un-drained) steer, if any. It is
idempotent: a cancel with nothing pending reports none_pending. A steer that
already drained at a turn boundary is ordinary recorded history and cannot
be retracted (the cancel reports none_pending then — the drain won).

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `message_id` | `string` |  |  | message_id is the CLIENT-MINTED correlation key, echoed verbatim on the steer.outcome ack (same contract as Steer.message_id). |
| `expected_run_id` | `string` |  |  | expected_run_id, when set, scopes this control to ONE run: the server refuses it if the session&#39;s current run is a different one, and the newer run is left untouched.  It closes a real race, not a hypothetical one. Controls are addressed at a SESSION, so a control still in flight when a run ends would otherwise land on whatever run started next — approving a tool call the user never saw, or cancelling work they did not ask to stop. `seq` cannot distinguish the two runs (it restarts each run), so before run ids there was no way to express &#34;this one&#34;.  EMPTY is the legacy behaviour exactly: the control applies to whatever run is current. Existing clients are unaffected; a client that knows the run id it is acting on should always send it. |




#### `mecatl.v1.SteerEcho`

SteerEcho is the proto projection of session.SteerPayload (EvSteer): the
committed operator steer the turn-boundary drain just recorded. See Event.steer.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `text` | `string` |  |  | text is the committed steer text, byte-identical to the user message recorded into history and replayed to the model. |
| `message_id` | `string` |  |  | message_id carries the WATERMARK of the drained bundle — the client-minted Steer.message_id of the LATEST (tail) send that contributed to this drain (empty when the sender supplied none). The client splits its ordered queue on it: sends up to and including the watermark drained (they land in context here), sends after it are still pending. |
| `parts` | `Content` | repeated |  | parts is the committed media bundle, byte-identical to the user message recorded into history and replayed to the model. |




#### `mecatl.v1.SteerRunRequest`

SteerRunRequest injects text and/or media into one exact live run.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `session_id` | `string` |  |  |  |
| `expected_run_id` | `string` |  |  |  |
| `text` | `string` |  |  |  |
| `parts` | `Content` | repeated |  |  |
| `message_id` | `string` |  |  |  |




#### `mecatl.v1.SteerRunResponse`

SteerRunResponse reports the authoritative exact-run inbox transition.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `outcome` | `SteerOutcome` |  |  |  |
| `run_id` | `string` |  |  |  |
| `message_id` | `string` |  |  |  |




#### `mecatl.v1.StreamSessionEventsRequest`

StreamSessionEventsRequest names the session whose durable event log is replayed.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `session_id` | `string` |  |  | session_id is the id of the session to replay. An unknown id yields an empty stream (absence is data); a server with no durable EventLog returns UNIMPLEMENTED. |




#### `mecatl.v1.StreamSessionLiveRequest`

StreamSessionLiveRequest names the session whose LIVE event stream is opened.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `session_id` | `string` |  |  | session_id is the id of the session to subscribe to. An unknown id yields an empty stream (absence is data — a never-created id is indistinguishable from a session with no live events yet). |




#### `mecatl.v1.Subagent`

Subagent is the REDACTED observability projection carried by the three
subagent.* events. Mirrors session.SubagentPayload. It carries metadata ONLY —
no child message text, tool args, or result bodies — so a client can render a
Task subagent&#39;s activity (a goal title, a trace of tool names, and final stats)
without ever seeing the child&#39;s content, preserving context isolation.

Which fields are set depends on the event kind:
  - subagent.start: parent_call_id, child_id, goal, routed_category, routed_model, model.
  - subagent.tool:  parent_call_id, child_id, tool_name, is_error, tool_count.
  - subagent.end:   parent_call_id, child_id, tool_count, usage, stop, duration_ms.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `parent_call_id` | `string` |  |  | parent_call_id is the parent&#39;s Task tool-call id, attributing the event to the originating Task card. |
| `child_id` | `string` |  |  | child_id is the child session id, distinguishing concurrent subagents. |
| `goal` | `string` |  |  | goal is a short, plain-text label for the delegated task (subagent.start). |
| `tool_name` | `string` |  |  | tool_name is the name of a child tool that just ran (subagent.tool) — the name alone, never the child&#39;s args or result. |
| `is_error` | `bool` |  |  | is_error reports whether that child tool call failed (subagent.tool). |
| `tool_count` | `int32` |  |  | tool_count is the running (subagent.tool) or final (subagent.end) child tool-call count. |
| `usage` | `Usage` |  |  | usage is the child run&#39;s cumulative token accounting (subagent.end). |
| `stop` | `string` |  |  | stop is the child run&#39;s terminal stop reason (subagent.end). |
| `duration_ms` | `int64` |  |  | duration_ms is the child run&#39;s wall-clock duration in milliseconds (subagent.end; best-effort). |
| `background` | `bool` |  |  | background reports a background (detached-delivery) child on subagent.start: the Subagent call returned an immediate started-result and the child keeps working while the parent run continues (its result is collected via the SubagentStatus tool). |
| `routed_category` | `string` |  |  | routed_category is the OPT-IN semantic model router&#39;s chosen CATEGORY label for this delegation (subagent.start only), set when the router was wired AND classified this delegation (empty otherwise — no router, or a fail-soft miss that inherited the default model). BARE METADATA — a label, never the task prompt or classifier reasoning — so it is context-isolation safe (gauntlet #7: no child content crosses). Mirrors session.SubagentPayload.RoutedCategory. |
| `routed_model` | `string` |  |  | routed_model is the concrete MODEL id the routed child was minted on (subagent.start only), paired with routed_category. BARE METADATA — a model id, never model-influenced free text — so it is context-isolation safe (gauntlet #7). Mirrors session.SubagentPayload.RoutedModel. |
| `model` | `string` |  |  | model is the concrete MODEL id this child ACTUALLY ran on (subagent.start only), regardless of how it was chosen — inherited default, agent-def pin, per-call `model` override, or the opt-in router. BARE METADATA — a model id, never child content — so it is context-isolation safe (gauntlet #7: no child content crosses). When the router classified this delegation, model == routed_model. Mirrors session.SubagentPayload.Model. |
| `inner_kind` | `string` |  |  | inner_kind discriminates which inner child event kind the preview came from (message.delta / tool.call / tool.result / result); set on subagent.tool alongside text / detail (ADR 0079 bounded previews). A child&#39;s permission.ask is never projected. STRING passthrough, mirroring Team.inner_kind. |
| `text` | `string` |  |  | text is a BOUNDED preview of the child&#39;s message/result text (subagent.tool, for the message.delta / result inner kinds) — control-byte scrubbed and rune-capped upstream in engine/agent, never the raw, unbounded body. |
| `detail` | `string` |  |  | detail is a BOUNDED preview of a child tool call&#39;s args (tool.call) or a tool result&#39;s body (tool.result) — control-byte scrubbed and rune-capped upstream, never the raw, unbounded args/result body (subagent.tool). |
| `cause` | `string` |  |  | cause is the child run&#39;s FAILURE DETAIL (subagent.end only, and only when stop is &#34;error&#34; — empty otherwise): the harness/provider error the loop recorded on the terminal result. METADATA about how the delegation failed — a transport/loop error string, never child-authored model output — so it is context-isolation safe (gauntlet #7: no child content crosses). Clamped at the emit site. Mirrors session.SubagentPayload.Cause. |
| `routing_reason` | `string` |  |  | routing_reason is a BOUNDED harness/composition reason string explaining WHY this delegation was NOT routed by the semantic model router (subagent.start only) — empty on a routed HIT (routed_category/routed_model carry the hit). A short label (e.g. &#34;pinned-model&#34;, &#34;agent-def-pinned-model&#34;, &#34;resume&#34;, &#34;fork&#34;, &#34;router-disabled&#34;, &#34;route-target-unavailable&#34;, &#34;breaker-open&#34;, &#34;aborted&#34;, &#34;empty-model&#34;, or a RouterMiss* classifier miss) — BARE METADATA, never the task prompt or classifier reasoning — so it is context-isolation safe (gauntlet #7). Clamped at the emit site. Mirrors session.SubagentPayload.RoutingReason. |
| `routing_decision` | `RoutingDecision` | optional |  | routing_decision is the optional bounded configured-router evidence captured on subagent.start. Historical events and deployments without a router omit it. |




#### `mecatl.v1.Team`

Team is the BOUNDED observability projection carried by the three team.* events.
Mirrors session.TeamPayload. Unlike Subagent (metadata only), it is deliberately
FULLER — a team is meant to be watched — so team.member carries the member&#39;s
message text and CAPPED previews of its tool calls/results. It is STILL bounded:
every preview (text/detail) is capped, and a member&#39;s permission.ask is DROPPED
entirely (never projected). The per-member transcripts never enter the parent
conversation — only the Team tool&#39;s joined-summary result does.

Which fields are set depends on the event kind:
  - team.start:  parent_call_id, team_id, roster.
  - team.member: parent_call_id, team_id, member, inner_kind, and the subset of
    &#123;text, tool_name, detail, is_error, usage&#125; relevant to inner_kind.
  - team.end:    parent_call_id, team_id, rounds, stop, usage (cumulative).

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `parent_call_id` | `string` |  |  | parent_call_id is the parent&#39;s Team tool-call id, attributing the event to the originating Team card. |
| `team_id` | `string` |  |  | team_id is the team id, distinguishing concurrent teams. |
| `roster` | `TeamMemberSpec` | repeated |  | roster is the team&#39;s membership as the model formed it (team.start). Metadata only, never member content. |
| `member` | `string` |  |  | member is the name of the member whose activity this event projects (team.member). |
| `inner_kind` | `string` |  |  | inner_kind is the member&#39;s underlying session event kind being projected — e.g. message.delta, tool.call, tool.result, turn.end, result (team.member). permission.ask is never projected. |
| `text` | `string` |  |  | text is the member&#39;s message/result text or a CAPPED preview of it (team.member, for message.delta / result inner kinds). |
| `tool_name` | `string` |  |  | tool_name is the name of a member tool that was called (team.member, for tool.call / tool.result inner kinds). |
| `detail` | `string` |  |  | detail is a CAPPED preview of a member tool call&#39;s args (tool.call) or result body (tool.result) — never the raw, unbounded body (team.member). |
| `is_error` | `bool` |  |  | is_error reports whether a member tool.result failed (team.member). |
| `rounds` | `int32` |  |  | rounds is the number of scheduling rounds that ran work (team.end). |
| `stop` | `string` |  |  | stop is the team run&#39;s terminal stop reason (team.end). |
| `usage` | `Usage` |  |  | usage is the member&#39;s per-event usage (team.member turn.end/result) or, on team.end, the TEAM TOTAL — the sum of every member&#39;s per-turn usage. |
| `context_used` | `int64` |  |  | context_used is the member&#39;s CURRENT context occupancy — the most recent turn&#39;s input-token count (team.member turn.end). It is the numerator of the per-member context meter in the f6 agents overlay; 0 when unknown. |
| `context_window` | `int64` |  |  | context_window is the producing member engine&#39;s context window in tokens — the meter&#39;s denominator (team.member turn.end). 0 when unknown (no meter is drawn in that case). |
| `tasks` | `TeamTask` | repeated |  | tasks is a snapshot of the team&#39;s SHARED TASK LIST in creation order. It is set on a first-class team.tasks event (a team-wide event with no member, emitted on change, de-duped) and on team.end (the terminal snapshot). It feeds the f6 agents task sub-view; it carries only task metadata, never member content. Reuses the existing TeamTask message. |
| `findings` | `TeamFinding` | repeated |  | findings is a snapshot of the team&#39;s SHARED FINDINGS LEDGER in append order. It is set on a first-class team.findings event (a team-wide event with no member, emitted on change, de-duped) and on team.end (the terminal snapshot). Each entry carries the recording member&#39;s name and a CAPPED body preview, never the raw finding. |
| `dispositions` | `TeamMemberDisposition` | repeated |  | dispositions is the per-member TERMINAL disposition snapshot, set ONLY on team.end (parallel to the terminal tasks/findings snapshots). Lets a client render a stopped member distinctly from a clean &#34;done&#34; instead of recomputing it. Closed-enum supervisor verdicts only, never member content. |
| `member_session_id` | `string` |  |  | member_session_id is the member&#39;s child SESSION id (&#34;team-&lt;teamID&gt;-&lt;member&gt;&#34;) on member-tagged (team.member) events — the CancelChild handle, carried explicitly so a client never derives ids from the (internal) id grammar. (17 is taken by dispositions.) |
| `cause` | `string` |  |  | cause is the member run&#39;s per-round FAILURE DETAIL (team.member, result inner kind only) when that round ended in StopError — the harness/provider error string, never member-authored output (gauntlet #7, same footing as stop). LINE-ORIENTED: whitespace-collapsed and rune-clamped at the emit site so a client renders it as-is. Empty on every other team.member inner kind and on team.end (the terminal disposition stays the closed-enum reason). Mirrors Subagent.cause (field 17). |




#### `mecatl.v1.TeamEvent`

TeamEvent is one member&#39;s session Event, tagged with the producing member —
the multiplexed team stream the client observes.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `member` | `string` |  |  | member is the name of the member whose session produced the event. |
| `event` | `Event` |  |  | event is the underlying session event. |
| `outcome` | `TeamOutcome` |  |  | outcome is the team&#39;s terminal outcome, set ONLY on the single final frame of a RunTeam stream (after every member event). On that frame member is empty and event is nil; on every other frame outcome is nil. Issue #36. |




#### `mecatl.v1.TeamFinding`

TeamFinding mirrors a shared findings-ledger entry, projected onto the team event
stream. It carries only the recording member&#39;s name and a CAPPED body preview,
never the raw finding.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `member` | `string` |  |  | member is the name of the member that recorded the finding. |
| `body` | `string` |  |  | body is a CAPPED preview of the finding text. |




#### `mecatl.v1.TeamMember`

TeamMember mirrors a team roster entry.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `name` | `string` |  |  | name is the member handle. |
| `agent_type` | `string` |  |  | agent_type is the adopted agent-definition name, if any. |
| `state` | `string` |  |  | state mirrors team.MemberState: spawning / working / idle / stopped. |
| `session_id` | `string` |  |  | session_id is the backing session&#39;s id. |




#### `mecatl.v1.TeamMemberDisposition`

TeamMemberDisposition is one member&#39;s TERMINAL disposition (team.end). Mirrors
session.TeamMemberDisposition. Closed-enum supervisor verdicts only — no member
content. A bool stopped + enum reason keeps the done/stopped split a closed
two-state (a done member has no reason, so (done, error) is non-representable).

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `name` | `string` |  |  | name matches a roster entry by name. |
| `stopped` | `bool` |  |  | stopped reports whether the member ended non-resumably / budget-exhausted (true) or cleanly (false). |
| `reason` | `TeamMemberStopReason` |  |  | reason is WHY a stopped member stopped; UNSPECIFIED for a done member. |
| `error_rounds` | `int32` |  |  | error_rounds is how many of this member&#39;s rounds ended in a run-level error, whether it was retried through them or finally stopped by them (issue #318). A member&#39;s errored round is now bounded-retried rather than always terminal, so a member can fail a round and still finish stopped=false / reason=UNSPECIFIED — this count is then the ONLY signal a client has that the run was not clean. It is a count of supervisor verdicts, never member content. |




#### `mecatl.v1.TeamMemberSpec`

TeamMemberSpec is one roster entry forwarded on team.start. Mirrors
session.TeamMemberSpec. It carries ONLY model-supplied metadata about the team&#39;s
shape — never any member content (no prompt body, no transcript).

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `name` | `string` |  |  | name is the member&#39;s unique handle. |
| `role` | `string` |  |  | role is the member&#39;s short role label. |
| `mutating` | `bool` |  |  | mutating reports whether the member runs in an isolated fork with workspace-mutating tools (true) or shares the base read-only (false). |
| `lead` | `bool` |  |  | lead marks the coordinating member. |
| `routed_category` | `string` |  |  | routed_category is the OPT-IN semantic model router&#39;s chosen CATEGORY label for this member (team.start roster only), set when the router was wired AND classified this member (empty otherwise — no router, a fail-soft miss that inherited the default member model, or a DEFINED member whose agent def pinned its own model so the router never fired). BARE METADATA — a label, never the member&#39;s role/prompt or classifier reasoning — so it is context-isolation safe (gauntlet #7: no member content crosses). Mirrors session.TeamMemberSpec.RoutedCategory. |
| `routed_model` | `string` |  |  | routed_model is the concrete MODEL id the routed member&#39;s engine was minted on (team.start roster only), paired with routed_category. BARE METADATA — a model id, never model-influenced free text — so it is context-isolation safe (gauntlet #7). Mirrors session.TeamMemberSpec.RoutedModel. |
| `model` | `string` |  |  | model is the concrete MODEL id this member&#39;s engine ACTUALLY runs on (team.start roster only), regardless of how it was chosen — inherited default member model, agent-def pin, or the opt-in router. BARE METADATA — a model id, never member content — so it is context-isolation safe (gauntlet #7: no member content crosses). When the router classified this member, model == routed_model. Mirrors session.TeamMemberSpec.Model. |
| `routing_reason` | `string` |  |  | routing_reason is a BOUNDED harness/composition reason string explaining WHY this member was NOT routed by the semantic model router (team.start roster only) — empty on a routed HIT (routed_category/routed_model carry the hit). A short label (e.g. &#34;agent-def-pinned-model&#34;, &#34;router-disabled&#34;, or a RouterMiss* classifier miss) — BARE METADATA, never the member&#39;s role/prompt or classifier reasoning — so it is context-isolation safe (gauntlet #7: no member content crosses). Clamped at the emit site. Mirrors session.TeamMemberSpec.RoutingReason. |
| `routing_decision` | `RoutingDecision` | optional |  | routing_decision is the optional bounded configured-router evidence captured for this member on team.start. Historical roster entries omit it. |




#### `mecatl.v1.TeamOutcome`

TeamOutcome is the terminal result of a RunTeam drive — the wire projection of
the supervisor&#39;s agent.TeamOutcome, carried on the single final TeamEvent frame
of the RunTeam stream (gRPC and HTTP SSE alike). It reuses the existing Usage /
TeamFinding / TeamMemberDisposition shapes; member content beyond the capped
finding previews never rides it. Issue #36.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `rounds` | `int32` |  |  | rounds is the number of scheduling rounds that ran work. |
| `quiescent` | `bool` |  |  | quiescent reports genuine completion (all tasks done, mailboxes empty, no member still working) versus stopping on a round cap / stuck dependency / budget. |
| `budget_exhausted` | `bool` |  |  | budget_exhausted reports that the team-wide token budget crossed at a round boundary and no further round was scheduled (the in-flight round and the lead&#39;s synthesis still completed). false when no budget was set or it never crossed. |
| `stop` | `string` |  |  | stop is the team&#39;s terminal stop reason — a STRING passthrough of session.StopReason (no enum): &#34;end_turn&#34; for a quiescent team, &#34;budget&#34; for a budget-stopped one, &#34;max_turns&#34; for a round-cap / stuck-dependency stop. |
| `usage` | `Usage` |  |  | usage is the supervisor-accumulated team token total across all members and rounds, synthesis included. |
| `dispositions` | `TeamMemberDisposition` | repeated |  | dispositions holds each member&#39;s terminal disposition in enrolment order. |
| `findings` | `TeamFinding` | repeated |  | findings is the team&#39;s findings ledger at the end of the run, in append order, bodies capped exactly like the event-stream projection. |




#### `mecatl.v1.TeamTask`

TeamTask mirrors a shared task-list entry.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `id` | `string` |  |  | id is the stable task identifier. |
| `description` | `string` |  |  | description is the work to do. |
| `state` | `string` |  |  | state mirrors team.TaskState: pending / in_progress / completed. |
| `assignee` | `string` |  |  | assignee is the member that claimed the task, or empty. |
| `deps` | `string` | repeated |  | deps are the task ids that must complete before this task is claimable. |




#### `mecatl.v1.TeammateSpec`

TeammateSpec describes a member to enrol — the per-member fields shared by
CreateTeamRequest&#39;s initial roster and SpawnTeammateRequest (minus team_id).

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `name` | `string` |  |  | name is the unique member handle peers address messages to. |
| `agent_type` | `string` |  |  | agent_type is the optional agent-definition name this member adopts. |
| `lead` | `bool` |  |  | lead marks the coordinating member (it is not auto-assigned tasks). |
| `mutating` | `bool` |  |  | mutating requests an isolated forked workspace (the member may edit/write). |
| `initial_prompt` | `string` |  |  | initial_prompt is the member&#39;s first-turn input, run in round 0. |




#### `mecatl.v1.TitleAttemptSummary`

TitleAttemptSummary is the latest durable title attempt without provider error detail.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `id` | `string` |  |  | id is a server-minted opaque attempt identifier. |
| `outcome` | `string` |  |  | outcome is &#34;succeeded&#34;, &#34;deferred&#34;, &#34;failed&#34;, &#34;interrupted&#34;, or empty while claimed. |




#### `mecatl.v1.TokenUsage`

TokenUsage is one canonical usage bucket. models maps opaque server-produced
provider/model attribution strings (or &#34;unknown&#34; for legacy data) to Usage;
total always equals the element-wise sum of models. In token_usage, each bucket
is part of the durable session accounting, not a per-run or per-turn report.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `total` | `Usage` |  |  |  |
| `models` | `TokenUsage.ModelsEntry` | repeated |  |  |




#### `mecatl.v1.TokenUsage.ModelsEntry`



| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `key` | `string` |  |  |  |
| `value` | `Usage` |  |  |  |




#### `mecatl.v1.ToolCall`

ToolCall is a request from the model to invoke a named tool. Mirrors
session.ToolCall.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `id` | `string` |  |  | id pairs this call with its tool_result. |
| `name` | `string` |  |  | name is the tool name as registered in the catalog. |
| `args` | `string` |  |  | args is the raw, tool-specific JSON argument payload. |




#### `mecatl.v1.ToolResult`

ToolResult is the outcome of executing a ToolCall. Mirrors
session.ToolResult.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `call_id` | `string` |  |  | call_id is the id of the ToolCall this result answers. |
| `content` | `string` |  |  | content is the legacy/default model-facing string result body, already token-shaped by the tool. |
| `is_error` | `bool` |  |  | is_error reports whether the tool failed. |
| `blocks` | `ContentBlock` | repeated |  | blocks are the typed content blocks (text/image/audio/resource-link/ embedded-resource/structured-content) mirroring session.ToolResult.Parts. Both content and blocks may be present; consumers prefer blocks when non-empty, falling back to content. |
| `structured_content` | `string` |  |  | structured_content is the JSON-stringified structured payload (the TextContent mirror of a structured-content block). |




#### `mecatl.v1.TurnEnd`

TurnEnd is the payload carried by a turn.end event. Mirrors
session.TurnEndPayload. A typed envelope (like Result) so turn.end owns its
own usage semantics and can grow (finish reason, model id, retries) without
overloading the shared Event fields.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `usage` | `Usage` |  |  | usage is THIS turn&#39;s model-call token accounting (not the run total or the durable session total). |
| `duration_ms` | `int64` |  |  | duration_ms is the elapsed milliseconds for the turn&#39;s model call; 0 when the server had no clock. |




#### `mecatl.v1.UndoLearningPromotionRequest`



| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `id` | `string` |  |  |  |
| `expected_version` | `string` |  |  |  |
| `project` | `string` |  |  |  |




#### `mecatl.v1.UndoLearningPromotionResponse`



| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `proposal` | `LearningProposal` |  |  |  |




#### `mecatl.v1.UploadArtifactMetadata`

UploadArtifactMetadata is the first frame of an artifact upload.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `session_id` | `string` |  |  |  |
| `name` | `string` |  |  |  |
| `mime_type` | `string` |  |  |  |




#### `mecatl.v1.UploadArtifactRequest`



| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `metadata` | `UploadArtifactMetadata` |  | `payload` |  |
| `chunk` | `bytes` |  | `payload` |  |




#### `mecatl.v1.UploadArtifactResponse`



| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `artifact_id` | `string` |  |  |  |
| `name` | `string` |  |  |  |
| `size` | `int64` |  |  |  |
| `sha256` | `string` |  |  |  |
| `mime_type` | `string` |  |  |  |




#### `mecatl.v1.Usage`

Usage accounts for the token cost of a model call (or an aggregate).
Mirrors session.Usage.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `input_tokens` | `int64` |  |  | input_tokens is the number of prompt tokens billed (including cache). |
| `output_tokens` | `int64` |  |  | output_tokens is the number of completion tokens generated. |
| `cache_read_tokens` | `int64` |  |  | cache_read_tokens is the number of input tokens served from cache. |
| `cache_write_tokens` | `int64` |  |  | cache_write_tokens is the number of input tokens written into the cache. |
| `reasoning_tokens` | `int64` |  |  | reasoning_tokens is the number of output tokens spent on internal reasoning (a subset of output_tokens; providers bill reasoning as part of the inclusive output total). 0 when the provider reports no breakdown. |




#### `mecatl.v1.UserModelDetail`



| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `current` | `UserModelRevision` |  |  |  |
| `history` | `UserModelRevision` | repeated |  |  |
| `history_available` | `bool` |  |  | false means the store supplied only the current legacy value. |




#### `mecatl.v1.UserModelEntry`

UserModelEntry is one user-model fact&#39;s listing metadata: its key + a one-line
description. The per-entry VALUE is omitted (Recall loads it) — discovery is
metadata only.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `key` | `string` |  |  | key is the entry&#39;s stable key. |
| `description` | `string` |  |  | description is the entry&#39;s one-line description (explicit, else derived). |




#### `mecatl.v1.UserModelRevision`

UserModelRevision is one read-only lifecycle revision for detail inspection.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `key` | `string` |  |  |  |
| `value` | `string` |  |  |  |
| `description` | `string` |  |  |  |
| `version` | `string` |  |  |  |
| `status` | `string` |  |  |  |
| `writer` | `string` |  |  |  |
| `origin` | `string` |  |  |  |
| `source_session_id` | `string` |  |  |  |
| `source_proposal_id` | `string` |  |  |  |
| `updated_at` | `google.protobuf.Timestamp` |  |  |  |




#### `mecatl.v1.UserPrompt`

UserPrompt is the proto projection of session.UserPromptPayload (EvUserPrompt):
the recorded user message (Text + non-text media Parts). It is the durable
record of what the user asked. LOG-ONLY on the live Converse wire; surfaced only
by the StreamSessionEvents replay.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `text` | `string` |  |  | text is the flattened user-message text (the prompt body, or a harness-authored continuation/notice). |
| `parts` | `Content` | repeated |  | parts carries any non-text media (image/audio) that rode alongside the text on the user message; nil for a text-only prompt. |
| `synthetic` | `bool` |  |  | synthetic is true when the harness authored this user-role continuation. False means principal-authored or legacy-unknown. |




#### `mecatl.v1.WatchSessionEventsRequest`

WatchSessionEventsRequest opens a durable replay-then-follow watch (ADR 0250).

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `session_id` | `string` |  |  | session_id is the id of the session to watch. An unknown id yields a watch that replays nothing and then follows — absence is data, and a session created a moment from now is indistinguishable from one that never existed. |
| `cursor` | `string` |  |  | cursor is an OPAQUE resume token from a previous response. EMPTY means the beginning of the log, which is the normal first attachment rather than an edge case.  Hand back a value the server issued, verbatim. The encoding is stateless and therefore inspectable, but it is not a contract: a decoded, hand-edited cursor gets `cursor_expired` or `cursor_malformed`, never silently wrong data. |
| `run_id` | `string` |  |  | run_id, when set, narrows delivery to the events one run emitted (ADR 0249). Empty delivers every run&#39;s events. Gap frames are delivered either way.  A cursor is SCOPED to the run_id it was issued under: resume with the same value you watched with. Changing it — or dropping it — replays from a position that already advanced past the other runs&#39; records, which skips them silently. |




#### `mecatl.v1.WatchSessionEventsResponse`

WatchSessionEventsResponse is ONE delivery envelope: what happened, where the
client now is, and which phase of the watch it arrived in.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `event` | `Event` |  |  | event is the recorded event. It is ABSENT on a phase-only frame — the single replay-to-live transition marker, and every gap frame. |
| `cursor` | `string` |  |  | cursor is the OPAQUE resume token positioned AFTER this envelope. A client that persists it once per processed envelope gets at-least-once delivery across a reconnect: hand the last one back and the watch continues from exactly the next record.  That guarantee is scoped to the run_id the cursor was issued under. Under a run filter the watch&#39;s position advances over the records it dropped, so the cursor sits past events another filter would have delivered; resuming with a different run_id, or none, skips them with no signal. Resume with the same filter, or start from the beginning. |
| `phase` | `string` |  |  | phase is an OPEN STRING: `replay` (already durable when the watch attached), `live` (appended while the watch was following), or `gap` (a position whose append is known to have failed). Tolerate an unknown value rather than treating it as an error — this is deliberately not an enum, so that adding a phase is a minor SDK release rather than a wire-compat event. |




#### `mecatl.v1.WorkspaceEnrollment`

WorkspaceEnrollment is the safe bundle-level client projection. presentation_url
is an ephemeral launch value; clients must not render, persist, or log it.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `enrollment_id` | `string` |  |  |  |
| `status` | `string` |  |  |  |
| `required_services` | `uint32` |  |  |  |
| `presentation_url` | `string` |  |  |  |




#### `mecatl.v1.WorkspaceEnrollmentConnectRequest`

WorkspaceEnrollmentConnectRequest starts or observes the caller-owned bundle.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `session_id` | `string` |  |  |  |




#### `mecatl.v1.WorkspaceEnrollmentControlRequest`

WorkspaceEnrollmentControlRequest targets one exact whole-bundle correlation.
There is deliberately no backend/provider field.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `session_id` | `string` |  |  |  |
| `enrollment_id` | `string` |  |  |  |




#### `mecatl.v1.Worktree`

Worktree is one display-safe eligible placement choice. Selector is opaque,
caller/source scoped, and accepted only by ClearSession or ForkSession.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `selector` | `string` |  |  |  |
| `kind` | `string` |  |  |  |
| `label` | `string` |  |  |  |
| `branch` | `string` |  |  |  |
| `revision` | `string` |  |  |  |
| `bare` | `bool` |  |  |  |




#### `mecatl.v1.ApprovalVerdict`

ApprovalVerdict is the client&#39;s three-way resolution of a permission.ask,
mirroring session.ApprovalVerdict. ALLOW_ALWAYS both permits the current call
and asks the harness to learn a per-session allow rule for the same tool +
exact canonical pattern; it NEVER overrides a deny and NEVER bypasses plan-mode
mutation denial.

| Name | Number | Description |
|---|---|---|
| `APPROVAL_VERDICT_UNSPECIFIED` | `0` | APPROVAL_VERDICT_UNSPECIFIED is invalid at control boundaries. |
| `APPROVAL_VERDICT_DENY` | `1` | APPROVAL_VERDICT_DENY refuses the proposed tool call. |
| `APPROVAL_VERDICT_ALLOW_ONCE` | `2` | APPROVAL_VERDICT_ALLOW_ONCE permits this call only; nothing is learned. |
| `APPROVAL_VERDICT_ALLOW_ALWAYS` | `3` | APPROVAL_VERDICT_ALLOW_ALWAYS permits this call and learns a per-session allow rule for the matching tool + exact pattern. |

#### `mecatl.v1.Content.Kind`

Kind discriminates the media kind of the part.

| Name | Number | Description |
|---|---|---|
| `KIND_UNSPECIFIED` | `0` | KIND_UNSPECIFIED is rejected by the server: every part must name its kind. |
| `KIND_IMAGE` | `1` | KIND_IMAGE is an image part. |
| `KIND_AUDIO` | `2` | KIND_AUDIO is an audio part. |
| `KIND_PDF` | `3` | KIND_PDF is a session-owned PDF artifact reference. |

#### `mecatl.v1.ContentBlock.Kind`

Kind discriminates the block kind.

| Name | Number | Description |
|---|---|---|
| `KIND_UNSPECIFIED` | `0` | KIND_UNSPECIFIED is the zero value; consumers treat it as absent. |
| `KIND_TEXT` | `1` | KIND_TEXT is a text block. |
| `KIND_IMAGE` | `2` | KIND_IMAGE is an inline image block. |
| `KIND_AUDIO` | `3` | KIND_AUDIO is an inline audio block. |
| `KIND_RESOURCE_LINK` | `4` | KIND_RESOURCE_LINK is a reference to an MCP resource by URI. |
| `KIND_EMBEDDED_RESOURCE` | `5` | KIND_EMBEDDED_RESOURCE is an embedded MCP resource (text or blob). |
| `KIND_STRUCTURED_CONTENT` | `6` | KIND_STRUCTURED_CONTENT is a JSON structured-content block. |
| `KIND_ARTIFACT` | `7` | KIND_ARTIFACT is a reference to a session-owned result artifact. |

#### `mecatl.v1.GuardrailApprovalKind`



| Name | Number | Description |
|---|---|---|
| `GUARDRAIL_APPROVAL_KIND_UNSPECIFIED` | `0` |  |
| `GUARDRAIL_APPROVAL_KIND_ACTION` | `1` |  |
| `GUARDRAIL_APPROVAL_KIND_RESULT_RELEASE` | `2` |  |

#### `mecatl.v1.GuardrailAssessment`



| Name | Number | Description |
|---|---|---|
| `GUARDRAIL_ASSESSMENT_UNSPECIFIED` | `0` |  |
| `GUARDRAIL_ASSESSMENT_ACCEPTABLE` | `1` |  |
| `GUARDRAIL_ASSESSMENT_PROHIBITED` | `2` |  |
| `GUARDRAIL_ASSESSMENT_UNRESOLVED` | `3` |  |

#### `mecatl.v1.GuardrailDisposition`



| Name | Number | Description |
|---|---|---|
| `GUARDRAIL_DISPOSITION_UNSPECIFIED` | `0` |  |
| `GUARDRAIL_DISPOSITION_EXECUTE` | `1` |  |
| `GUARDRAIL_DISPOSITION_ASK_ACTION` | `2` |  |
| `GUARDRAIL_DISPOSITION_WITHHOLD_RESULT` | `3` |  |
| `GUARDRAIL_DISPOSITION_RELEASE_RESULT` | `4` |  |
| `GUARDRAIL_DISPOSITION_DENY` | `5` |  |
| `GUARDRAIL_DISPOSITION_PASS_ADVISORY` | `6` |  |
| `GUARDRAIL_DISPOSITION_CONTINUE_WARNING` | `7` |  |

#### `mecatl.v1.GuardrailInspection`



| Name | Number | Description |
|---|---|---|
| `GUARDRAIL_INSPECTION_UNSPECIFIED` | `0` |  |
| `GUARDRAIL_INSPECTION_COMPLETE` | `1` |  |
| `GUARDRAIL_INSPECTION_OPERATIONAL_FAILURE` | `2` |  |

#### `mecatl.v1.GuardrailJob`



| Name | Number | Description |
|---|---|---|
| `GUARDRAIL_JOB_UNSPECIFIED` | `0` |  |
| `GUARDRAIL_JOB_ACTION` | `1` |  |
| `GUARDRAIL_JOB_INBOUND` | `2` |  |
| `GUARDRAIL_JOB_PERMISSION` | `3` |  |

#### `mecatl.v1.HookDecision`

HookDecision is the outcome a hook fire produced. Mirrors
session.HookDecision; lets a client colour/rank a hook notice without parsing
its prose.

| Name | Number | Description |
|---|---|---|
| `HOOK_DECISION_UNSPECIFIED` | `0` | HOOK_DECISION_UNSPECIFIED defaults to INFO at the boundary. |
| `HOOK_DECISION_INFO` | `1` | HOOK_DECISION_INFO is a benign, informational hook notice. |
| `HOOK_DECISION_BLOCKED` | `2` | HOOK_DECISION_BLOCKED means the hook vetoed the action (can abort a run). |
| `HOOK_DECISION_MODIFIED` | `3` | HOOK_DECISION_MODIFIED means the hook rewrote the action&#39;s payload. |
| `HOOK_DECISION_ADVISORY` | `4` | HOOK_DECISION_ADVISORY means the hook flagged content as a finding but did not alter the call/result (advisory guardrail). Client-visible warning, model-invisible. |

#### `mecatl.v1.PermissionMode`

PermissionMode is the session-wide permission posture, mirroring
session.PermissionMode. Plan mode exposes a read-only toolset; acceptEdits
auto-accepts edits.

| Name | Number | Description |
|---|---|---|
| `PERMISSION_MODE_UNSPECIFIED` | `0` | PERMISSION_MODE_UNSPECIFIED defaults to DEFAULT at the server boundary. |
| `PERMISSION_MODE_DEFAULT` | `1` | PERMISSION_MODE_DEFAULT is the standard deny -&gt; ask -&gt; allow posture. |
| `PERMISSION_MODE_PLAN` | `2` | PERMISSION_MODE_PLAN enforces a read-only toolset. |
| `PERMISSION_MODE_ACCEPT_EDITS` | `3` | PERMISSION_MODE_ACCEPT_EDITS auto-accepts edits. |

#### `mecatl.v1.RetryDisposition`

RetryDisposition is the neutral causal retry classification of a failed model
stream. UNKNOWN is an explicit conservative classification, distinct from an
absent optional field sent by an older server.

| Name | Number | Description |
|---|---|---|
| `RETRY_DISPOSITION_UNSPECIFIED` | `0` |  |
| `RETRY_DISPOSITION_UNKNOWN` | `1` |  |
| `RETRY_DISPOSITION_RETRYABLE` | `2` |  |
| `RETRY_DISPOSITION_PERMANENT` | `3` |  |

#### `mecatl.v1.SoulProvenance`

SoulProvenance records WHERE the selected soul originated, mirroring the
composition-layer soulProvenance. It is a read-only attribution for the /soul
inspection panel; it never affects execution.

| Name | Number | Description |
|---|---|---|
| `SOUL_PROVENANCE_UNSPECIFIED` | `0` | SOUL_PROVENANCE_UNSPECIFIED means no soul was selected (none present, an untrusted project soul was dropped, or --no-soul). |
| `SOUL_PROVENANCE_USER` | `1` | SOUL_PROVENANCE_USER is the user-scoped soul (always trusted). |
| `SOUL_PROVENANCE_PROJECT` | `2` | SOUL_PROVENANCE_PROJECT is the project-scoped &lt;workspace&gt;/.mecatl/soul.md (selected only with --trust-project). |
| `SOUL_PROVENANCE_DRIVER` | `3` | SOUL_PROVENANCE_DRIVER is a remote soul-source driver (--soul-source-url; mecatl.driver.v1.SoulSourceService). Operator-configured infrastructure: it occupies the USER slot in the selection precedence and is always trusted. Client-inspection only, NOT model-facing. |

#### `mecatl.v1.SteerOutcome`

SteerOutcome is the AUTHORITATIVE result of a steer inbox transition,
mirroring the engine&#39;s closed-enum agent.SteerOutcome. The client cannot
observe the exact drain moment across stream latency, so the engine reports
what actually happened to the steer rather than letting the client guess.

| Name | Number | Description |
|---|---|---|
| `STEER_OUTCOME_UNSPECIFIED` | `0` | STEER_OUTCOME_UNSPECIFIED is the zero value; the server never sends it. |
| `STEER_OUTCOME_ACCEPTED` | `1` | STEER_OUTCOME_ACCEPTED: the steer parked in the (empty) pending slot; it drains at the next turn boundary unless retracted first. |
| `STEER_OUTCOME_APPENDED` | `2` | STEER_OUTCOME_APPENDED: the steer found the pending slot OCCUPIED and was MERGED into it (append is the default for a second steer): the pending bundle&#39;s text grew by a blank-line separator + text, and it still drains as ONE bundle. Distinguished from ACCEPTED (a NEW pending bundle) so the client can render &#34;merged onto pending&#34; honestly. Replacing the pending bundle is explicit: steer_cancel, then resend with a fresh message_id. |
| `STEER_OUTCOME_RETRACTED` | `3` | STEER_OUTCOME_RETRACTED: a steer_cancel found a pending steer and retracted it; the run drains nothing for it. |
| `STEER_OUTCOME_NONE_PENDING` | `4` | STEER_OUTCOME_NONE_PENDING: a steer_cancel found the slot EMPTY — there was nothing to retract (no steer pending, or the run terminal). |
| `STEER_OUTCOME_TOO_LATE` | `5` | STEER_OUTCOME_TOO_LATE: the steer arrived after the run went terminal (the inbox was closed). The server promoted the text to a fresh follow-up run (promoted=true on the event) rather than dropping it. |

#### `mecatl.v1.StreamProgress`

StreamProgress records the semantic commit boundary reached by a model stream.

| Name | Number | Description |
|---|---|---|
| `STREAM_PROGRESS_UNSPECIFIED` | `0` |  |
| `STREAM_PROGRESS_UNKNOWN` | `1` |  |
| `STREAM_PROGRESS_PRECOMMIT` | `2` |  |
| `STREAM_PROGRESS_VISIBLE` | `3` |  |
| `STREAM_PROGRESS_COMPLETE` | `4` |  |

#### `mecatl.v1.TeamMemberStopReason`

TeamMemberStopReason is WHY a stopped member stopped (team.end disposition snapshot).
Closed enum: a supervisor verdict, never member-authored text.

| Name | Number | Description |
|---|---|---|
| `TEAM_MEMBER_STOP_REASON_UNSPECIFIED` | `0` | a done member, or an unknown/future reason. |
| `TEAM_MEMBER_STOP_REASON_ERROR` | `1` | the member&#39;s run failed or its session could not be re-opened. |
| `TEAM_MEMBER_STOP_REASON_CANCELLED` | `2` | the member was ended by ctx cancellation. |
| `TEAM_MEMBER_STOP_REASON_BUDGET` | `3` | the member exhausted its lifetime turn budget. |

### `mecatl/v1/schedule.proto`




#### `mecatl.v1.CreateScheduleRequest`

CreateScheduleRequest creates a new schedule.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `spec` | `ScheduleSpec` |  |  | spec is the schedule definition to save. The create-seam validates the cross-field rules (trigger XOR, prompt-or-parts non-empty, cron grammar). |




#### `mecatl.v1.CreateScheduleResponse`

CreateScheduleResponse returns the created schedule (spec + state).

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `schedule` | `Schedule` |  |  | schedule is the created aggregate. |




#### `mecatl.v1.DeleteScheduleRequest`

DeleteScheduleRequest names the schedule to delete.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `name` | `string` |  |  | name is the schedule&#39;s unique key. Idempotent: an unknown name succeeds. |




#### `mecatl.v1.DeleteScheduleResponse`

DeleteScheduleResponse is the empty acknowledgement.

This message has no fields.



#### `mecatl.v1.FireNowRequest`

FireNowRequest forces an immediate fire of the named schedule.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `name` | `string` |  |  | name is the schedule to fire. |




#### `mecatl.v1.FireNowResponse`

FireNowResponse returns the per-fire session id. fire_id and session_id are
the SAME value (the per-fire session id is the fire&#39;s id on the wire) — both
are returned so a caller holding either handle can poll GetFire / the session
APIs without a derivation.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `fire_id` | `string` |  |  | fire_id is the per-fire session id (the same id GetFire takes and ScheduleFire.id / ScheduleFire.session_id carry). |
| `session_id` | `string` |  |  | session_id is the session the fire ran as (== fire_id). |




#### `mecatl.v1.GetFireRequest`

GetFireRequest names the fire to look up.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `fire_id` | `string` |  |  | fire_id is the fire&#39;s unique identifier. |




#### `mecatl.v1.GetFireResponse`

GetFireResponse wraps the fire record.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `fire` | `ScheduleFire` |  |  | fire is the requested fire record. |




#### `mecatl.v1.GetScheduleRequest`

GetScheduleRequest names the schedule to look up.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `name` | `string` |  |  | name is the schedule&#39;s unique key. |




#### `mecatl.v1.GetScheduleResponse`

GetScheduleResponse wraps the schedule snapshot.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `schedule` | `Schedule` |  |  | schedule is the requested schedule. |




#### `mecatl.v1.ListFiresRequest`

ListFiresRequest lists the fires for a schedule.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `schedule_name` | `string` |  |  | schedule_name is the schedule whose fires to list. |




#### `mecatl.v1.ListFiresResponse`

ListFiresResponse carries the fire records.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `fires` | `ScheduleFire` | repeated |  | fires is the list, in no guaranteed order. |




#### `mecatl.v1.ListSchedulesRequest`

ListSchedulesRequest lists all schedules. No filtering in v1.

This message has no fields.



#### `mecatl.v1.ListSchedulesResponse`

ListSchedulesResponse carries all stored schedules.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `schedules` | `Schedule` | repeated |  | schedules is the full list, in no guaranteed order. |




#### `mecatl.v1.PauseScheduleRequest`

PauseScheduleRequest disables a schedule without deleting it.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `name` | `string` |  |  | name is the schedule to pause. |




#### `mecatl.v1.PauseScheduleResponse`

PauseScheduleResponse is the empty acknowledgement.

This message has no fields.



#### `mecatl.v1.ResumeScheduleRequest`

ResumeScheduleRequest re-enables a paused schedule.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `name` | `string` |  |  | name is the schedule to resume. |




#### `mecatl.v1.ResumeScheduleResponse`

ResumeScheduleResponse is the empty acknowledgement.

This message has no fields.



#### `mecatl.v1.Schedule`

Schedule is the aggregate value object: the immutable Spec plus the durable
State. Mirrors port.Schedule. Load/List return this.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `spec` | `ScheduleSpec` |  |  | spec is the immutable definition. |
| `state` | `ScheduleState` |  |  | state is the durable firing state. |




#### `mecatl.v1.ScheduleFire`

ScheduleFire is one fire record: the outcome of a single Claim→run→RecordFire
cycle. Mirrors port.ScheduleFire. The fire&#39;s SESSION (the conversation,
usage, tool calls) lives in the SessionStore under session_id; this record is
the schedule-indexed pointer to it plus the terminal stop reason and any
error string.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `id` | `string` |  |  | id is the fire&#39;s unique identifier (caller-assigned at RecordFire time). |
| `schedule_name` | `string` |  |  | schedule_name is the schedule this fire belongs to. |
| `session_id` | `string` |  |  | session_id is the session the fire ran as. |
| `fired_at` | `google.protobuf.Timestamp` |  |  | fired_at is when the fire was Claimed (its start instant). |
| `stop` | `string` |  |  | stop is the terminal stop reason of the fire&#39;s run (a session.StopReason string passthrough). Empty if the fire has not yet completed. |
| `err` | `string` |  |  | err is the error string if the fire&#39;s run failed (stop == &#34;error&#34;), empty otherwise. |
| `started_at` | `google.protobuf.Timestamp` |  |  | started_at is when the fire&#39;s run actually began (RecordFireStart), distinct from fired_at (the Claim instant). A fire written by RecordFireStart is IN-FLIGHT (stop empty, started_at set); a fire written by RecordFire is terminal. Absent on a terminal-only fire. Issue #386. |
| `progress_at` | `google.protobuf.Timestamp` |  |  | progress_at is the last observed progress instant for the fire (RecordFireProgress). Absent means &#34;no progress observed&#34;. Issue #386. |
| `deadline` | `google.protobuf.Timestamp` |  |  | deadline is the fire&#39;s wall-clock deadline (RecordFireStart): start + ScheduleSpec.fire_timeout (or absent when fire_timeout is zero / the deployment default applies). Absent means &#34;no explicit deadline&#34;. Issue #386. |




#### `mecatl.v1.ScheduleProviderSelector`

ScheduleProviderSelector is the provider+model pair a schedule&#39;s fires run
on. Mirrors port.ScheduleProviderSelector. Both empty means &#34;use the
deployment default&#34; (the same opaque-string discipline as a session created
with no selector).

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `provider_id` | `string` |  |  | provider_id is the opaque provider identifier; &#34;&#34; means the deployment default. |
| `model_id` | `string` |  |  | model_id is the opaque model identifier; &#34;&#34; means the deployment default for the provider. |




#### `mecatl.v1.ScheduleSpec`

ScheduleSpec is the immutable definition of a schedule — the &#34;what to run and
when&#34; half. Mirrors port.ScheduleSpec. The durable FIRING state lives in
ScheduleState; the two halves together form a Schedule.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `name` | `string` |  |  | name is the schedule&#39;s unique key (Save is an upsert by name). |
| `prompt` | `string` |  |  | prompt is the free-text user prompt the fire runs with. Either prompt or parts (or both) must be non-empty — a cross-field rule enforced by the server, not the proto. |
| `parts` | `Content` | repeated |  | parts is an OPTIONAL multimodal extension (image/audio content parts), validated by the same session.ValidateMediaParts the wire path uses. |
| `trigger` | `TriggerSpec` |  |  | trigger is the firing trigger (cron XOR one_shot). |
| `selector` | `ScheduleProviderSelector` |  |  | selector selects the provider+model the fires run on. A zero value means the deployment default. |
| `profile` | `string` |  |  | profile is the session tool-surface profile (&#34;&#34; default, &#34;no-fs&#34; file-less). |
| `mode` | `PermissionMode` |  |  | mode is the session permission posture. Reuses the PermissionMode enum from harness.proto (same package — proto allows cross-file refs within a package). |
| `limits` | `Limits` |  |  | limits are the bounded per-fire budgets. Reuses the Limits message from harness.proto. They are PER-FIRE: each fire gets a fresh session with these limits. |
| `mutating` | `bool` |  |  | mutating is the explicit write opt-in. The DEFAULT is false (read-leaning), the same conservative default as a subagent. |
| `max_fires` | `int32` |  |  | max_fires bounds the TOTAL number of fires for a cron schedule (0 = forever). Cron-only: a one-shot fires once by definition and max_fires is ignored. |
| `misfire` | `MisfirePolicy` |  |  | misfire is the misfire policy. Default MISFIRE_FIRE_ONCE_NOW. |
| `singleton` | `bool` |  |  | singleton is whether to skip the next fire if a prior fire is still running (the singleton / skip-overlap guard). |
| `timezone` | `string` |  |  | timezone is the IANA timezone name the cron expression fires in. Empty means UTC. A one-shot trigger ignores it. |
| `created_at` | `google.protobuf.Timestamp` |  |  | created_at is the schedule&#39;s creation timestamp. |
| `one_shot_retry` | `bool` |  |  | one_shot_retry is the opt-in at-least-once retry for a one-shot. Default false (at-most-once). One-shot-only: rejected on a cron trigger. |
| `one_shot_max_retries` | `int32` |  |  | one_shot_max_retries bounds the re-arm budget when one_shot_retry is true. 0 means off (the create-seam applies a default of 3 when one_shot_retry is true and this is 0). |
| `carry_context` | `bool` |  |  | carry_context renders the prior fire&#39;s conversation as a fenced untrusted preamble (NOT seeded history — carried context is untrusted). Default false. |
| `fire_timeout` | `google.protobuf.Duration` |  |  | fire_timeout is the per-fire wall-clock deadline (issue #386, the in-flight scheduled-fire state). A google.protobuf.Duration; zero/absent means &#34;use the deployment default&#34; (which may itself be zero for &#34;no explicit deadline&#34;). When set, RecordFireStart stamps ScheduleState.fire_deadline = start + fire_timeout, and a watchdog reads it to terminate the in-flight run with StopTimeout when it lapses. Per-fire: each fire gets a fresh deadline from its own start instant. The store stores it inertly. |
| `owner` | `Principal` |  |  | owner is the verified caller the schedule is attributed to (ADR 0204 decision 6), captured ONCE at create time from the create SURFACE — the executing session&#39;s owner on the Schedule-tool path, the context principal on an out-of-band REST/CLI create. It is READ-ONLY on the wire: a create or update REQUEST&#39;s owner is IGNORED (a caller can no more name a schedule&#39;s owner than it can name a session&#39;s — CreateSessionRequest has no owner field for the same reason), and the server projects the captured value on every read. Absent = an ownerless schedule, never a fabricated principal. |




#### `mecatl.v1.ScheduleState`

ScheduleState is the durable FIRING state of a schedule — the mutable half
that advances as the schedule fires. Mirrors port.ScheduleState. Updated
atomically by Claim (the claim-before-fire advance) and RecordFire (the
post-fire outcome).

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `next_fire_at` | `google.protobuf.Timestamp` |  |  | next_fire_at is the ground-truth next fire instant. The zero time means &#34;no next fire&#34; (a one-shot that fired, or a cron whose max_fires is exhausted) — the schedule is effectively done. |
| `last_fire_at` | `google.protobuf.Timestamp` |  |  | last_fire_at is the instant of the most recent Claim (the start of the most recent fire), not its completion. |
| `fire_count` | `int32` |  |  | fire_count is the total number of fires that have been Claimed. |
| `enabled` | `bool` |  |  | enabled is whether the schedule is active. A schedule may be disabled without deletion (pause/resume). Disabled schedules are excluded from Due. |
| `last_fire_session_id` | `string` |  |  | last_fire_session_id is the session id of the prior fire. The per-session LEASE on it is the authoritative cross-replica liveness oracle for the singleton check. The port.PendingFireSessionID sentinel (&#34;pending&#34;) marks a Claim that has not yet been RecordFire&#39;d. |
| `one_shot_retry_count` | `int32` |  |  | one_shot_retry_count is the durable counter of one-shot re-arms (one_shot_retry). Incremented atomically on each re-arm; when it exceeds one_shot_max_retries the schedule stays disabled. |
| `last_fire_started_at` | `google.protobuf.Timestamp` |  |  | last_fire_started_at is when the current fire&#39;s RUN actually began (RecordFireStart), distinct from last_fire_at (the Claim instant). The in-flight liveness marker: absent means the current fire has not started its run yet (the crash-after-Claim state). Set by RecordFireStart, cleared by RecordFire. Issue #386. |
| `last_fire_progress_at` | `google.protobuf.Timestamp` |  |  | last_fire_progress_at is the last observed progress instant for the current fire (RecordFireProgress). Absent means &#34;no progress observed yet&#34;. Best- effort liveness: a stale value is a stuck-fire signal. Issue #386. |
| `fire_deadline` | `google.protobuf.Timestamp` |  |  | fire_deadline is the current fire&#39;s wall-clock deadline (RecordFireStart): start + ScheduleSpec.fire_timeout (or absent when fire_timeout is zero / the deployment default applies). A watchdog reads it to decide whether to terminate the in-flight run with StopTimeout. Absent means &#34;no explicit deadline&#34;. Issue #386. |




#### `mecatl.v1.TriggerSpec`

TriggerSpec is the sum type for a schedule&#39;s firing trigger. Exactly ONE of
cron / one_shot is set.

CROSS-FIELD RULE (exactly-one-of, enforced by the server at the create-seam,
NOT a proto rule): cross-field XOR is awkward to express in buf.validate, so
the proto carries both fields optional and composition&#39;s create-seam enforces
the XOR fail-closed (mirroring port.TriggerSpec.Validate). The store is
parser-free — it stores the raw expression verbatim and never interprets it;
the caller computes the next fire.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `cron` | `string` |  |  | cron is the cron expression (5-field or @-macro). Mutually exclusive with one_shot. Empty unless this is a cron trigger. |
| `one_shot` | `google.protobuf.Timestamp` |  |  | one_shot is the single wall-clock instant to fire at. Mutually exclusive with cron. The zero time means &#34;not set&#34;. It MUST be in the future at schedule creation; the store does not re-validate this on save. |




#### `mecatl.v1.UpdateScheduleRequest`

UpdateScheduleRequest updates an existing schedule&#39;s spec. The state half is
preserved on overwrite.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `spec` | `ScheduleSpec` |  |  | spec is the new schedule definition. spec.name names the schedule to update (an upsert by name). |




#### `mecatl.v1.UpdateScheduleResponse`

UpdateScheduleResponse returns the updated schedule.

| Field | Type | Label | Oneof | Description |
|---|---|---|---|---|
| `schedule` | `Schedule` |  |  | schedule is the updated aggregate. |




#### `mecatl.v1.MisfirePolicy`

MisfirePolicy is what to do when a schedule&#39;s NextFireAt is in the past at
tick time — i.e. the scheduler wakes up (or a replica takes over) and finds a
due slot it missed. Mirrors port.MisfirePolicy. The zero value
(MISFIRE_FIRE_ONCE_NOW) is the default.

| Name | Number | Description |
|---|---|---|
| `MISFIRE_POLICY_UNSPECIFIED` | `0` | MISFIRE_POLICY_UNSPECIFIED defaults to FIRE_ONCE_NOW at the server boundary. |
| `MISFIRE_FIRE_ONCE_NOW` | `1` | MISFIRE_FIRE_ONCE_NOW fires the schedule a single time immediately for the missed slot, then resumes the normal cadence. The DEFAULT (zero value). |
| `MISFIRE_SKIP` | `2` | MISFIRE_SKIP skips the missed slot entirely and waits for the next due fire. |
