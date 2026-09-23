# Changelog — `github.com/stacklok/mecatl/engine`

All notable changes to the engine module's public API are recorded here. The
format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/); changes
are classified per [COMPATIBILITY.md](./COMPATIBILITY.md) (Added = minor;
Changed/Deprecated/Removed = breaking, pre-v1 a minor bump).

The covered surface is the eight core packages (`session`, `governance`, `learning`, `tool`,
`prompt`, `port`, `team`, `agent`); their committed API snapshots live in
[`engine/api/`](./api/).

## [Unreleased]

### Added

- **Auxiliary provider/model identity** — adds `session.ProviderModelID`, the
  opaque server-selected provider/model identity for an auxiliary model call.
  It deliberately carries no selector/default, context-window, reasoning-effort,
  provider-instance, or credential semantics. Added (minor).

- **Purpose-attributed auxiliary token usage foundation** — adds
  `session.AuxiliaryUsage` with owned-copy merging and the recognized
  `compaction`, `reflection`, `router`, `ask_reviewer`, `guardrail`, and
  `parallel_judge` usage-kind constants. Canonical ledgers preserve non-empty
  opaque kinds for forward-compatible persistence; only the separate router
  bucket joins main usage in the internal `MaxRunTokens` spend bound. Added
  (minor).

- **Delegated-model routing decision evidence** — adds `agent.ModelRouteResult`,
  `agent.SubagentModelRouter`, and `session.RoutingDecision`, with optional decision
  snapshots on Subagent, Parallel, and Team-member start payloads. Added (minor).

- **Backend-neutral router outcomes** — adds `agent.RouterMissTimeout`,
  `RouterMissLowConfidence`, `RouterMissInputOverLimit`, and
  `RouterMissCapacityTimeout` to the engine-owned closed classifier-outcome taxonomy.
  Added (minor).

- **Authoritative negotiated session-store capabilities** — adds
  `port.SessionCapabilitySupport` and the `SupportsSessionCreate`,
  `SupportsSessionMetadataPaging`, and `SupportsSessionLineage` probes. Adapters
  that retain optional interfaces for compatibility can now report negotiated
  false capabilities without consumers selecting unusable operations. Added (minor).

- **Idempotent team-supervisor teardown** — adds `agent.Supervisor.Close`, a concurrent-safe, repeatable lifecycle endpoint that cancels an active run and waits until enrolled members stop and their resources are released exactly once. Event callbacks drain independently: `Close` may be called from a callback, while `Run` still waits for every ordered callback before returning. Member factories and cleanup callbacks must not synchronously re-enter `Close`. `AddMember` after run start or close returns `agent.ErrSupervisorClosed`. Added (minor).

- **Request-manifest schema-byte evidence** — adds `session.RequestManifestPayload.AdvertisedToolSchemaBytes` and exposes it through the target-bound debugger manifest view. Adds catalog registration-key metadata accessors so manifest enumeration does not refresh live tool specifications. Added (minor).

- **Atomic ordinary permission-ask resolution** — adds `agent.AskResolution`,
  its closed not-pending/resolved/plan-originated outcomes, and
  `agent.Run.ResolveOrdinaryAsk`. Embedders can now resolve a root or surfaced
  child policy/hook ask exactly once while leaving plan-originated asks pending
  for the dedicated plan-resolution choreography. `Run.Approve` remains the
  all-origin compatibility path. Added (minor).

- **`session.Session.GrantToolAuthority`** — stable-unions bounded, control-free tool names into an idle or completed session's existing name authority without reopening it or changing unrelated aggregate state. Added (minor).

- **`session.NoProgressNudgeText` / `session.NoProgressExtractiveNudgeText`** — exported the no-progress nudge literals that `engine/agent` authors and `session.IsGenuineUserPrompt` classifies against, so both sides reference one owned copy instead of duplicating the text (mirrors the existing `CompactionSummaryMarker`/`Tier4SummaryMarker` precedent). Added (minor).

- **Event-follow capacity classification** — adds
  `port.ErrEventFollowCapacity`, allowing `CursorEventLog` backends to reject a
  follow iterator before storage work begins when follower capacity is full.
  Added (minor).

- **Draft-aware session activity classification** — adds `session.ActivityState`, the `ActivityUnknown`/`ActivityDraft`/`ActivityActive` constants, and pure `session.ActivityOf`, using `IsGenuineUserPrompt` so empty and multimodal genuine user history is active while harness compaction summaries remain drafts. `port.SessionDiscoveryMeta.Activity` carries the atomically written, content-free activity projection; unavailable, corrupt, legacy, and unsupported discovery metadata normalizes to unknown. Adds `session.ValidActivity` (the one fail-closed validity clamp for the type, replacing three adapters' independent inline checks) and `port.SupportsActivityProjection` (the one capability-probe rule for `SessionActivityProjectionPager`, replacing three duplicated type-assertion call sites). Added (minor).

- **Authoritative synthetic user-prompt origin** — adds `session.UserPromptPayload.Synthetic` so durable user-prompt events distinguish harness-authored continuations from principal and legacy prompts without changing folded conversation messages. Added (minor).

- **Session-load failure classification** — adds `port.SessionLoadFailureClass`,
  `SessionLoadFailureError`, `ErrSessionLoadFailure`, `NewSessionLoadFailure`, and
  `ClassifySessionLoadFailure`. Snapshot-backed stores can distinguish bounded
  retrieval (`store`) from decode/validation (`snapshot`) failures through
  `errors.Is`/`errors.As` without exposing backend error text. Added (minor).

- **`tool.WorkspaceNamespace`** — an additive POSIX-like namespace-operation extension (`ReadDir`/`Remove`/`Rename`/`CopyFile`) implemented by workspaces that support listing and mutating path names beyond content replacement (memfs, osfs, redisstore, remoteenv; ACP implements it with `Remove`/`Rename` returning `tool.ErrFileOperationUnsupported`). Adds `tool.ErrDirectoryNotEmpty` and `tool.ErrFileOperationUnsupported` sentinels. `Workspace` itself is unchanged; a consumer type-asserts for the new capability. Added (minor).

- **`port.AppendHTTPErrorDisplay`** ([ADR 0309](../docs/adr/0299-safe-http-rejection-display-evidence.md)) — a stdlib-only helper for independently versioned provider modules to append only a validated HTTP(S) request target and bounded opaque correlation ID to a structured HTTP rejection. Added (minor).
- **Versioned bounded reflection evidence materialization (ADR 0300)** — adds the storage-neutral `learning.MaterializeEvidence` protocol, immutable aggregate manifests, distinct selected-local and durable source coordinates, and closed no-work outcomes. Added (minor).

- **Exact durable-lineage edge selection** — adds optional `RecordID` and `RecordIncarnation` fields to `port.SessionLineageQuery`. Stores return at most the selected direct-edge record without exposing or enumerating sibling records, allowing callers to prove each ancestry hop against both the child's self record and its parent's direct-edge partition. Added (minor).

- **Session placement authority repair** — removes the orphan exported `session.PlacementSelector` protocol, adds persisted display-only `session.PlacementMetadata`, requires a valid `EnvironmentRef` at aggregate construction, and rejects direct engine runs whose live environment does not match the session identity. Changed (breaking, pre-v1 minor).

- `port.RunAwareToolCallRecorder`: an optional `ToolCallRecorder` extension
  that additionally receives the calling run's `RunID`, letting a consumer
  correlate a tool call to the run that made it. Purely additive — no
  existing `ToolCallRecorder` implementer is affected. Added (minor).

- **Delegation artifact boundary (ADR 0288)** — adds the distinct `agent.ArtifactHandle` type, changes `agent.PreservedForkStore.Preserve` to key retained forks by that opaque handle rather than a physical root, and removes `Workspace`/`WinnerWorkspace` from `session.ParallelPayload`. Parallel results now expose an opaque preserved-artifact handle while physical fork roots remain private orchestration state. Changed (breaking, pre-v1 minor).

- **Unified environment and placement identity** — adds `Revision` and `Valid` to `session.EnvironmentRef`, makes that exact `{Kind, ID, Revision}` value the runtime and durable placement identity, and removes the short-lived duplicate `session.PlacementRef`/`PlacementKind` types. Engine-created Subagent, Parallel, and Team child sessions now persist the identity carried by their `tool.Environment`; `port.ScheduleSpec` and `port.SessionDiscoveryMeta` replace workspace paths with the exact private environment identity, with schedules also retaining their trusted placement scope. Changed (breaking, pre-v1 minor).

- **`agent.Run.RetractPermissionAsk`** ([ADR 0294](../docs/adr/0294-session-correlation-and-affinity.md)) — lets a lease-owning host atomically withdraw one still-pending local permission ask without resolving it, emitting the matching retraction before cancellation while leaving an already-durable awaiting snapshot untouched for successor handoff. Added (minor).
- **Title metadata revisions** — adds `Session.TitleRevision` and
  `TitlePayload.Revision`: a durable, title-specific monotonic revision that
  advances only for effective title metadata mutations. Added (minor).
- **Canonical token-usage buckets and title lifecycle projection** — adds
  `session.UsageKind`/`TokenUsage`, `Session.TokenUsageSnapshot`, and
  `Session.UsageFor`, with canonical token usage kinds and opaque model attribution
  maps. Each total is normalized to the sum of its model entries. Current snapshots
  persist only this canonical ledger. `SessionTitle` is the source-free canonical
  title lifecycle projection; its nested usage is removed. Added (minor); the
  removed compatibility projections are classified below.

- **Reversible external-authorization claims** — `session.Session.RestoreAuthorizationClaim` compensates a claimed continuation that could not be registered, returning the aggregate to the exact durable `authorizing` state instead of abandoning unresolved tool calls in `running`. Added (minor).

- **Terminal external-authorization resolution value** — `session.AuthorizationResolution` and `session.NewAuthorizationResolution` make pending and unknown statuses unrepresentable at the continuation boundary while preserving `AuthorizationStatus` as the complete event-lifecycle vocabulary. Added (minor).

- **`tool.TemporaryScope`, `tool.CommandTemporaryScopeRunner`, and `tool.CommandTemporaryScopeStreamer`** ([ADR 0281](../docs/adr/0281-managed-temporary-command-leases.md)) — optional bound-runner capabilities for the closed managed/system temporary-storage scope selection. The capability carries no path or environment value and preserves the existing `CommandRunner` fallback for runners that do not manage temporary storage. Added (minor).

- **`tool.CommandEnvironmentOverlay`, `tool.CommandEnvironmentRunner`, and `tool.CommandEnvironmentStreamer`** ([ADR 0281](../docs/adr/0281-managed-temporary-command-leases.md)) — an optional, per-invocation command-environment overlay for host-owned runtime values such as managed temporary storage. The optional capability preserves the existing bound-runner API and namespace affinity: callers that require an overlay must decline honestly when a runner does not implement it, never interpolate environment values into shell text or fall back to an unoverlayed call. Added (minor).

- **Session title-generation domain metadata and lifecycle event** — adds generated title provenance,
  durable title-generation lifecycle/source/attempt records, canonical title-model
  token usage, and the source-free `EvSessionTitle` / `TitlePayload` event
  projection. Title lifecycle attempts retain only identity and outcome;
  title usage is intentionally separate from `Session.Usage`, normal run budgets,
  result usage, and conversation. Snapshot and event-source metadata round-trip
  the title-specific state. Added (minor).

- **Workspace-enrollment broker proof and atomic authority replacement** —
  `session.Session.CompleteWorkspaceEnrollment` accepts the complete pending
  correlation plus only a validated exact tool-name set, then clones the bound
  authority before replacing its tools so present and future non-tool axes cannot
  be caller-supplied. `session.ValidWorkspaceEnrollmentToolNames` provides the
  enrollment-only bounded framing and duplicate check without tightening legacy
  `Authority.Valid` inputs. The optional neutral
  `internal/mcpbroker.WorkspaceEnrollmentAttachment` boundary exposes broker-owned
  begin/observe/cancel operations, closed statuses, ephemeral presentation URLs,
  and a connected-only immutable frozen catalogue result without adding enrollment
  methods to anonymous broker attachments. Added (minor).

- **Pre-prompt workspace-enrollment correlation** —
  `session.PendingWorkspaceEnrollment` and the `Session` begin/read/abort methods
  add a bounded, authority-bound aggregate contract that persists only an opaque
  enrollment id, required-service count, and expiry through `sessnap.Snapshot`.
  It is an exclusive pre-prompt gate while remaining storage-independent from
  tool authorization and agent-loop pending state. Added (minor).

- **Prepared external-authorization continuations** — `agent.PreparedRun`,
  `Engine.PrepareAuthorizationContinuation`, and
  `Engine.PrepareAfterAuthorization` let a host fully initialize and
  register an inert continuation, atomically install the effective pending-call
  result set, and inspect the closed `PreparedRunTransition` outcome through
  `Start` or `Abort`. Added (minor).

- **Opaque external runtime binding** — `session.Session.ExternalBinding` stores a
  composition-issued logical-session binding without interpreting broker topology;
  `sessnap.Snapshot` persists it for exact reattachment checks. Added (minor).

- **Generic authorization dispatch parking** — `agent.RunRequest.CanPresentAuthorization`
  opt-in permits an attached main run to durably park a
  `tool.AuthorizationRequester` call after permission and `PreToolUse` gates;
  `agent.ResumeApprovalWith` carries the same explicit capability on restored
  permission continuations. `agent.Run.Outcome` reports `RunOutcomeAuthorizationPending` for this
  nonterminal close; no `session.StopReason` or `EvResult` is emitted. Added
  (minor).

- **Safe external-authorization events** — `session.EvAuthorizationRequired`,
  `session.EvAuthorizationResolved`, `session.AuthorizationStatus`, and
  `session.AuthorizationPayload` add a provider-neutral durable lifecycle grammar;
  `AuthorizationPayload.Valid` centralizes its bounded identifier, optional
  human-facing authority/service label, expiry, and status validation.
  The payload otherwise carries only an authorization id, tool-call id, expiry,
  and closed status; it omits private continuation state, routing/configuration
  labels, URLs, arguments, and credentials. `session.ValidAuthorizationID` exports the canonical
  bounded correlation grammar for transport trust boundaries.
  `eventsource.Fold` reconstructs fully resolved historical lifecycles, rejects
  malformed ordering, and requires private state only while a well-formed
  lifecycle remains open. Added (minor).

- **Generic external authorization continuation** — `session.ExternalAuthorization`,
  `session.PendingAuthorization`, and `session.StateAuthorizing` add a durable,
  provider-neutral aggregate park/claim/abort lifecycle with exact tool-pairing
  validation, effective-call ownership, and deep-copy isolation.
  `tool.AuthorizationRequester` receives that effective call explicitly and aborts
  with the complete private `ExternalAuthorization` correlation; aggregate abort
  reasons are closed harness tokens rendered as fixed messages. The private
  `sessnap.Snapshot.PendingAuthorization` DTO base64-encodes private continuation
  argument bytes for exact round-trip fidelity without changing the existing
  `sessnap.RestoreState` signature. Added (minor).

- **`tool.DispatchSerial`** — optional static marker for read-only tools whose
  sibling call must form a run-local dispatch barrier. It preserves
  `Tool.ReadOnly` semantics and tool advertisement while making the dispatcher
  flush concurrent read batches before and after the marked call. The guarantee
  applies only within one run/dispatch; shared adapter state reached by concurrent
  runs still requires its own synchronization. Added (minor).

- **`port.CursorEventLog`, `port.Cursor`, `port.EncodeCursor`/`DecodeCursor`, `port.LogRecord`/`LogRecordKind`, `port.ReadOptions`, `port.ErrCursorMalformed`/`ErrCursorExpired`** (issue #821, [ADR 0250](../docs/adr/0250-durable-cursors-and-watch.md)) — durable positions over the event log: an append reports WHERE the record landed, and a read resumes from a position rather than always from the start.

  It exists because the two read paths the engine shipped cannot express replay-then-follow as one operation. `port.EventLog.Read` is a complete, ordered, durable replay with no position and no follow — it reads the whole log and stops — so catching up and then watching means reading everything and THEN subscribing, and any event appended between those two steps is silently lost. `CursorEventLog.ReadAfter` closes that window: `ReadOptions.Follow` keeps the iterator open at the tail, and `LogRecord.Live` reports the replay/live boundary a follower needs in order to tell a caller it is caught up.

  **`port.EventLog` is untouched.** `CursorEventLog` EMBEDS it, so every cursor backend is usable anywhere an `EventLog` is expected and no existing consumer changes; a backend opts in by also implementing the cursor half, and one that does not is reported as unsupported rather than silently degraded to replaying the whole log each time.

  A `Cursor` is opaque, stateless, and scoped **twice** — to the session it was issued for, and to the log's generation. `DecodeCursor` therefore takes the `session.SessionID` alongside the current generation, and a cursor presented against a different session is `ErrCursorMalformed`. Session scoping is enforced in `port` rather than left to each backend so a backend cannot forget it and a new backend inherits it; it also closes a case the generation check structurally cannot, since a LEGACY log predating generations reports the EMPTY generation, so every legacy log in a deployment shares one basis value and a cross-session cursor would decode cleanly and resolve to a real but WRONG record (raised in review on #868 and reproduced against both shipped backends before the fix). The generation is the second axis: a positional cursor into a rebuilt log does not fail, it resolves happily to a real record that is not the one the client last saw, so the only symptom is wrong data much later. A mismatch is `ErrCursorExpired` — loud and recoverable — and a token that cannot be decoded at all is the DISTINCT `ErrCursorMalformed`, because an expired cursor is retryable from the beginning while a malformed one indicates a bug or tampering that restarting would hide. Neither is ever coerced to a position. `EncodeCursor`/`DecodeCursor` live in `port` so the envelope and its tamper rejection have ONE implementation rather than one per backend. The **zero `Cursor` means the beginning of the log**, not the latest: a first attach has no cursor, and replaying a session's history is the common case rather than an edge.

  `ReadOptions` carries two contract terms worth stating because the natural reading of each is the wrong one. A POSITIVE `Limit` is a TOTAL budget for the call, not a per-wake-up one: reaching it ends the read even with `Follow` set, since a follower wanting an unbounded tail can say so with `Limit: 0` whereas under the per-wake-up reading a total cap would be inexpressible. And `Limit == 0` bounds the SEQUENCE, not the FETCH — it means "yield until the log ends or the context is cancelled", NOT permission to pull an unbounded response out of storage in one round trip; a backend may, and for a large log should, page internally at whatever size it likes while continuing to yield, a choice invisible through the iterator and therefore a backend decision rather than a contract term (both raised in review on #868).

  `LogRecordGap` is a log-record ENVELOPE variant, never a `session.Event`. A gap is a fact about delivery rather than something that happened in the run, so `session.Event`, the proto `Event` message, and the event kind-parity surface all gain nothing; a gap occupies a real append position so cursors advance past it correctly, and the legacy `EventLog.Read` SKIPS it, preserving that port's contract of returning only events.

  All additions are **Added = minor**: new types and functions alongside an untouched `EventLog`, with no existing signature changed.
- **Cryptographic session incarnations and incarnation-bound lineage** — adds the
  opaque `session.IncarnationID`, a 128-bit `crypto/rand` identity minted by every
  `session.New`, persisted by `sessnap.Snapshot` and `eventsource.SessionMeta`, plus a
  prefix-disjoint deterministic identity for legacy snapshots. Related-session
  constructors and `SessionRelationship` now carry the parent/origin/target
  incarnation; delegation events carry internal child incarnations; and
  `port.SessionLineageQuery` requires the root incarnation. The constructor and query
  signature changes are breaking (pre-v1 minor); the new identity APIs are Added.

- **Durable selected debug MCP ceiling and target incarnation** — adds
  `session.Session.DebugMCPServers`, `DebugMCPTools`, and
  `DebugTargetFingerprint` plus their `sessnap.Snapshot` and `eventsource.SessionMeta`
  fields, and adds `session.IncarnationFingerprint` /
  `DebugTargetFingerprint`. The MCP fields persist only bounded configured server names
  and exact model-facing direct-tool names; the non-projectable fingerprint binds target
  ID, cryptographic incarnation, and owner scope. Added (minor).

- **Content-safe request manifests** — adds `session.EvRequestManifest`,
  `RequestManifestPayload` and its closed prompt/tool metadata (including
  catalog/overlay/MCP source labels), plus
  `prompt.AssembleWithManifest`/`InstructionManifest`, and the opt-in
  `agent.Deps.EnableDurableEvidence` gate. The loop emits the log-only manifest
  from the final provider-neutral request without retaining prompt, message, tool-spec, or
  provider-private bodies. Hosts enable it only when their relay has durable EventLog
  retention; zero/default Deps skip all manifest construction. Built-in instruction assemblers report provenance; custom
  `InstructionAssembler` implementations remain compatible as `custom`/`unknown`. Added
  (minor).

- **Durable session lineage port** — adds the optional `port.SessionLineageReader`,
  bounded `SessionLineageQuery`/`SessionLineageResult`, content-free
  `SessionLineageRecord`, and closed retained/pruned `SessionLineageState`. Store
  adapters can expose authoritative direct relationships and deletion tombstones
  without widening the minimal `SessionStore` or loading transcript content.
  Added (minor).

- **`agent.Run.RunID()`** (issue #821, [ADR 0249](../docs/adr/0249-durable-run-identity.md)) — reports the run's host-minted identity, or `""` when none was supplied.

  It exists so a caller holding a `*Run` can ASK which run it holds instead of inferring it from the session aggregate, and that distinction is load-bearing for stale-control refusal: a control addressed at a specific run must be compared against the run it would ACTUALLY affect, and the aggregate names the session's CURRENT run — which, after a terminal race, is precisely the run the caller did NOT mean.

- **Durable run identity: `session.Event.RunID`, `session.Session.BeginRun`/`RunID`, `agent.RunRequest.RunID`, `sessnap.Snapshot.RunID`** (issue #821, [ADR 0249](../docs/adr/0249-durable-run-identity.md)) — a run now carries an opaque, host-minted identity that survives restart.

  `agent.RunRequest.RunID` is how a host supplies it. The loop stamps every event it emits with that value at `Run.emit`/`emitOrAbort`, beside the existing `Seq` stamp — so `session.Event.RunID` is populated on every path an event can leave a run by, with no relay, transport, or persistence site able to omit it. `Seq` is monotonic WITHIN a run and restarts each run, so it cannot distinguish two runs of one session; `RunID` is what makes an event attributable to a specific run.

  When `RunRequest.AskIDDiscriminator` is empty, `RunID` also SUPPLIES the ask discriminator. That is the arrangement [ADR 0044](../docs/adr/0044-host-supplied-askid-discriminator.md) described in terms of a run id that did not then exist ("a durable host passes its own RunID"): a durable host now sets ONE field and gets both a stamped identity and cross-process-reconstructable askIDs. `AskIDDiscriminator` is retained and still wins when set explicitly, so the derivation is a default, not a constraint.

  `Session.BeginRun`/`RunID` store the value on the aggregate and `sessnap.Snapshot.RunID` persists it (`omitempty`, additive, no format-tag bump — the `Profile`/`ProviderID` precedent). That is what makes an awaiting-approval resume continue THE SAME run across a process restart: `Engine.ResumeApproval` reads the id back off the session rather than minting a new one. The fallback is confined to that seam — a prompt entry never reads it off the session, because a reused session still carries the id of the run that just ended.

  **No behaviour change when unset.** A host that supplies no `RunID` emits events with an empty one and asks fall back to the process-global serial, byte-identical to before. An EMPTY `RunID` is meaningful rather than missing: it marks an event as session-scoped rather than run-scoped (the scheduler's `schedule.*` lifecycle events are emitted outside any loop).

  All four additions are **Added = minor**. `Event` and `RunRequest` gain a field, which breaks external UNKEYED struct literals — but both are already routinely constructed keyed, and `Event` is a wide event-payload struct nobody builds positionally.

- **`tool.ReadLedger`, `tool.ErrLedgerUnavailable`** ([ADR 0298](../docs/adr/0298-persistent-read-before-write-ledgers.md)) — the storage-independent, context-aware read-before-write evidence capability selected independently of file content and carried by `tool.Environment`. `RecordRead(ctx, key, version) error` and `RecordedVersion(ctx, key) (version, ok, err)` distinguish a valid recorded token, ordinary absence, and an unavailable/corrupt lookup (`err != nil`). `engine/adapter/memledger` is the in-memory reference implementation; `engine/adapter/ledgerconformance` is the shared behavioral suite every implementation runs. Both additions are **Added = minor**.

- **`tool.EncodeFileVersion`, `tool.DecodeFileVersion`, `tool.ErrInvalidFileVersion`** ([ADR 0298](../docs/adr/0298-persistent-read-before-write-ledgers.md), repair-wave task 05) — a narrow persistence/transport codec for opaque `FileVersion` values. It round-trips valid empty and non-empty tokens byte-exactly while rejecting the invalid zero value. **Added = minor**.

- **`agent.WithSubagentReadLedgerFactory`, `agent.WithTeamReadLedgerFactory`, `agent.WithTeamToolReadLedgerFactory`** (repair-wave task 05) — inject factories that mint a fresh ledger for every child environment without importing a concrete adapter into `engine/agent`. **Added = minor**.

### Fixed

- **`port.SessionLease.Renew` doc comment narrowed (issue #1333)** — clarifies that bare expiry of the caller's own owner/token, with nothing else having taken the lease over, is not by itself one of the definitive-loss conditions `ErrLeaseHeld` documents; loss is specifically a holder or token change. An implementation that can prove no one else could have raced it (e.g. a single-host backend re-checking its own durable record under its stable transition lock) may reclaim instead of declaring loss — `internal/adapter/flocklease.Lease.Renew` now does exactly this. This narrows, never widens, when `ErrLeaseHeld` may be returned, so it is a documentation clarification, not a contract change; no exported signature changed. No `task api:update` needed.

### Changed

- **Exact Team parent-call correlation** — Team-tool member relationships now
  populate the existing `session.SessionRelationship.CallID`; validation permits
  that optional value only with a valid parent lifetime. The exported
  `session.NewTeamMember` signature and relationship shape are unchanged, and
  historical relationships without a call ID remain loadable but uncorrelated.
  Older engine binaries may reject newly populated Team-member relationships because
  their validation forbade this existing field for that kind. Changed (breaking,
  pre-v1 minor).

- **Delegated-model router callback** — changes `agent.Deps.SubagentModelRouter`
  from the callback tuple to `*agent.SubagentModelRouter`, preserving configured
  classifier metadata on skipped decisions and candidate evidence on rejected
  decisions. Changed (breaking, pre-v1 minor).

- **Model-router deadline outcome** — `agent.RunModelRouter` now reports an observed
  caller or operation deadline as `RouterMissTimeout` instead of conflating it with
  `RouterMissCancelled`; cancellation remains `RouterMissCancelled`. Changed
  (observable behavior, pre-v1 minor).

- **Mandatory versioned memory lifecycle/CAS** — consolidates `tool.MemoryStore`
  around `Remember`, `Inspect`, `Recall`, `List`, `Index`, `Search`, `Forget`, and
  `Undo`. Every mutation now requires either an explicit absent-state expectation
  or the exact opaque current version; the optional lifecycle/convergence
  interfaces and unconditional mutation methods are removed. Changed/breaking
  (pre-v1 minor).

- **Canonical session accounting, retry metadata, and snapshot restore** — adds
  `session.RetryMetadata` and `Session.UsageFor`, changes failure/retry-pending APIs
  to pass that value object instead of positional tuples, and changes
  `sessnap.RestoreState` to accept adapter-owned `sessnap.RestoreData`. Snapshot
  decoding validates canonical fields, ignores unknown fields, and treats an omitted
  empty token ledger as its zero value. Changed/breaking (pre-v1 minor).

- **Go compatibility floor** - the engine module requires Go 1.27. The root,
  provider, and authentication modules use the same floor. Changed (breaking,
  pre-v1 minor).

- **Canonical Shell command tool** — replaces the exported `BashTool` / `NewBashTool`, `BashStatusTool` / `NewBashStatusTool`, and `tool.BashToolName` APIs with their Shell-named counterparts. The model-facing catalog names are `Shell` and `ShellStatus`; `ServerCapabilities.bash` and Go `Capabilities.Bash` remain stable shell-availability indicators. Changed (breaking, pre-v1 minor).

- **Durable main-usage budget baseline** — `Session.Usage` is now permanently the
  deprecated lifetime mirror of `TokenUsage[UsageKindMain].Total`. `MaxRunTokens`
  measures usage since an immutable internal run baseline; ordinary runs use zero, while
  team-lead synthesis and exactly one `StopBudget` free-text Subagent cleanup capture
  their current main total without mutating session accounting. Auxiliary usage remains
  outside this budget and `EvResult.Usage`.

- **Simplified title-model token accounting** — removes the title-specific
  `session.AuxiliaryUsage`/`AuxiliaryOperation` API and its per-attempt ledger.
  `Session.RecordTokenUsage` records canonical usage by kind and opaque selected-model
  attribution, and `RestoreTitleMetadata` now atomically restores the complete durable
  title metadata, including its revision. Removed APIs and the changed restore signature
  are breaking; pre-v1 this is a minor compatibility classification.

- **Title-attempt timestamps** — `session.TitleAttempt.CreatedAt` is removed.
  Attempt identity and outcome remain sufficient for the lifecycle and UI failure
  deduplication. Removed is breaking (pre-v1 minor) per COMPATIBILITY.md.

- **`session.Session.ResetUsage`** — the externally callable accounting reset is
  removed. It could discard durable lifetime usage to grant a synthesis allowance;
  the internal run baseline now provides that allowance without a reset. Removed is
  breaking (pre-v1 minor) per COMPATIBILITY.md.

- **`learning.AttemptRepository.DiscoverWork`, `learning.AttemptWork{List,Page,Cursor}`, and `learning.MaxAttemptWorkBatch`** ([ADR 0259](../docs/adr/0259-cloud-native-learning.md)) — adds bounded, cursor-paged, storage-neutral discovery of queued attempts and running attempts with expired claims across opaque owner partitions. The cursor is only a disposable scan position and grants no workflow authority. This makes the repository, including a remote driver, the sole worker authority for work admitted after startup and claim-expiry reassignment. Extending the interface is Changed/breaking (pre-v1 a minor bump).

- **`tool.Workspace` loses `RecordRead`/`RecordedVersion`; `tool.Environment` gains a mandatory `ReadLedger()`; `tool.NewEnvironment`/`MustEnvironment` take a new required `ledger ReadLedger` parameter** ([ADR 0298](../docs/adr/0298-persistent-read-before-write-ledgers.md), repair-wave task 05) — completes the read-ledger/content-backend separation the prior entry started: `Workspace` is now a pure content/search/versioned-mutation seam with no read-evidence capability of its own, and the read ledger is instead an independently-selected, mandatory second capability carried on `Environment` alongside `Workspace`. `NewEnvironment(ref, ws, ledger, runner)` / `MustEnvironment(ref, ws, ledger, runner)` replace the three-argument forms (`ledger` inserted before `runner`); a nil `ledger` is rejected with the new `ErrEnvironmentNoReadLedger`, mirroring the existing nil-`Workspace` rejection. The built-in Read/Edit/Write tools now record/consult evidence via `env.ReadLedger()` (keyed with the existing I/O-free `tool.LedgerKey(ws.Root(), path)`), not through the Workspace. Changed/breaking (pre-v1 a minor bump). Every in-tree `Workspace` implementation (osfs, memfs, nofs, the ACP fs-delegation workspace, remoteenv) drops its ledger methods; every `Environment` construction site now supplies an explicit ledger (a fresh `memledger.New()` for a session's default environment; a forked/direct-write child environment gets its OWN fresh ledger over the SAME content backend it was handed, never the parent's).

- **`FileVersion.Token`** ([ADR 0298](../docs/adr/0298-persistent-read-before-write-ledgers.md), repair-wave task 05) — removed because it exposed an interpretation API for an opaque token. Persistence callers migrate to the added `tool.EncodeFileVersion`/`DecodeFileVersion` codec above. Changed/breaking (pre-v1 a minor bump).

- **Child workspace-view options** (repair-wave task 05) — `agent.WithSharedChildWorkspace`, `agent.WithTeamSharedBaseWorkspace`, and `agent.WithTeamToolSharedBaseWorkspace` now accept `func(tool.Workspace) tool.Workspace` rather than a root-to-Workspace factory. Base-sharing/direct-write children therefore retain the exact parent content backend through a potentially stricter authority view instead of reconstructing storage from `Workspace.Root()`; composition uses that view to preserve child path-escape containment while the independent read-ledger factory supplies fresh evidence. Changed/breaking (pre-v1 a minor bump).

- **Versioned bounded reflection evidence materialization (ADR 0300)** — `learning.Input` now carries its aggregate `MaterializationManifest`; `learning.EvidenceRef` now carries the explicit protocol, manifest index, durable original coordinate, and aggregate digest needed to distinguish `reflection-evidence/v1` references from historical input-local `reflection-evidence/legacy-v0` ordinals; and `learning.MaterializeEvidence` now accepts a `context.Context` plus an optional streaming event source so full message/event scans are cancellable and selected-event limits apply after source ranking. `learning.AdmissionPolicy.Decide` now accepts a context and borrowed `Trajectory` request rather than a full `Input`, allowing cancellation without constructing an unbounded admission input. Adding fields to these exported structs and changing the function and interface signatures break external callers and unkeyed literals, so this is Changed/breaking (pre-v1 a minor bump).

- **`Engine.PrepareAuthorizationContinuation` and `Engine.PrepareAfterAuthorization` require `session.AuthorizationResolution` and return an error** — callers must construct a validated terminal outcome before preparing a continuation; pending, zero, and unknown statuses are rejected before run construction or session mutation. Changed/breaking (pre-v1 a minor bump).

- **`session.Session.ExternalBinding` is now the named `session.ExternalBinding` type** — the opaque process-external session identity can no longer be accidentally interchanged with an arbitrary runtime string or the distinct per-authorization `AuthorizationBinding`. Its JSON representation remains an unchanged string. Changed/breaking (pre-v1 a minor bump).

- **`agent.Deps.EnableDurableEvidence`** — adds the explicit opt-in gate for
  debugger-only request-manifest construction/emission and sanitized network-attempt capture. The zero value preserves the allocation-sensitive
  default loop; composition enables it only alongside durable EventLog retention. Adding a field
  to an exported struct breaks external unkeyed literals, so this is Changed/breaking (pre-v1 a
  minor bump).

- **`session.Event.Authorization`** — adds the safe external-authorization payload
  arm to the domain event envelope. The field is required for durable authorization
  lifecycle reconstruction and intentionally carries no binding, route, URL,
  arguments, or credentials. Adding a field breaks external unkeyed `Event` struct
  literals. Changed/breaking (pre-v1 a minor bump).

- **`agent.Run.EnqueueSteer`** (issue #861, [ADR 0251](../docs/adr/0251-multimodal-steer.md)) — changes from `EnqueueSteer(text string)` to `EnqueueSteer(text string, parts []session.Content)`, making one canonical text, media, or mixed steer entry point. Changed/breaking (pre-v1 a minor bump).

- **`session.SteerPayload.Parts`** (issue #861, [ADR 0251](../docs/adr/0251-multimodal-steer.md)) — adds the committed media parts to the steer echo. Adding a field to an exported struct breaks external unkeyed literals, so this is Changed/breaking (pre-v1 a minor bump).

- **`agent.Run.EnqueueSteerWithMessageID`**
  ([ADR 0232](../docs/adr/0232-steer-while-running.md)) adds an enqueue entry
  point that stores the client-minted correlation id atomically with the pending
  steer bundle. `EnqueueSteer` remains the id-less compatibility entry point.
  Added (minor).

- **`session.SteerPayload.MessageID`**
  ([ADR 0232](../docs/adr/0232-steer-while-running.md)) carries the latest
  contributing steer id as the committed bundle's positional watermark. Adding
  a field to an exported struct breaks external unkeyed literals, so this is
  Changed/breaking (pre-v1 a minor bump).

- **`session.Event.RequestManifest`** — adds the log-only, content-safe final-request
  manifest payload. Adding a field to an exported struct breaks external unkeyed literals,
  so this is Changed/breaking (pre-v1 a minor bump).

- **`session.Event.NetworkAttempt`** — adds the log-only sanitized provider-attempt
  payload used by the dedicated debugger. Adding a field to an exported struct breaks
  external unkeyed literals, so this is Changed/breaking (pre-v1 a minor bump).

- **`session.SessionRelationship.DebugTargetID`** — adds the durable target link
  for dedicated debug sessions. Adding a field to an exported struct breaks
  external unkeyed literals, so this is Changed/breaking (pre-v1 a minor bump).

- **`agent.AgentMeta.WritableAuthorityCeiling`** (issue #517, [ADR 0242](../docs/adr/0242-route-unpinned-writable-named-specialists.md)) — adds the exported mode-specific managed-authority ceiling used when a fresh named specialist runs with direct write. Adding a field to an exported struct breaks external unkeyed literals, so this is Changed/breaking (pre-v1 a minor bump).

- **`agent.SteerOutcome` enum: superseded/slot_full dropped, appended added**
  (issue #512, the landed steer-while-running contract). The round-2/task-13
  rework replaced `SteerSuperseded` with `SteerSlotFull`, and the round-3
  append-default rework then removed BOTH from the enum and added
  `SteerAppended` (`"appended"`): a second steer on the occupied slot merges
  into the pending bundle (`pending += "\n\n" + text`) instead of rejecting
  or replacing. The shipped enum is `accepted` / `appended` / `retracted` /
  `none_pending` / `too_late`. Because neither intermediate value ever shipped
  in a tagged release, the net public change over the pre-steer baseline is a
  breaking `Changed` only for the enum vocabulary (pre-v1 a minor bump).

- **`session.ResultPayload` semantic retry fields** (issue #409) — adds exported
  `RetryDisposition` and `StreamProgress` fields. Struct field additions are
  breaking under `COMPATIBILITY.md` (pre-v1 a minor bump).

### Removed

- **`port.RetryDisposition`, `port.StreamProgress`, and their constants** — removes
  the temporary source-compatibility aliases. Providers and decorators use the
  canonical `session.RetryDisposition` and `session.StreamProgress` vocabularies;
  the classifier interfaces remain port-owned. Removed/breaking (pre-v1 minor).

- **Legacy session usage/permanence projections** — removes exported
  `Session.Usage`, `Event.Usage`, `ResultPayload.Permanent`,
  `Session.RecordFailurePermanence`, `Session.FailurePermanence`, and
  `port.PermanentError`. Canonical accounting is available through
  `UsageFor`/`TokenUsageSnapshot`; terminal retry classification is carried only
  by `RetryMetadata` and `port.RetryDispositionError`. Removed/breaking (pre-v1 minor).

- **Subagent `max_tokens` tool argument** — removed the deprecated alias for
  `max_run_tokens` from the `Subagent` tool schema. Both named the SAME cumulative
  input+output RUN budget, and the shorter name read as a provider single-response
  output ceiling it never was. `max_run_tokens` is now the only per-call budget arg;
  `resolveMaxRunTokens` no longer folds two aliases, so the "set only one of
  max_run_tokens or the deprecated max_tokens" conflict error is gone. No exported
  identifier changes (`subagentArgs` is unexported), so the public-API baselines are
  unaffected — but a caller whose model emits `max_tokens` now has that key IGNORED
  and the child inherits the operator/engine budget instead. Model-facing behaviour
  change, no Go API change.

- **Agent untrusted-content fencing APIs** (issue #380,
  [ADR 0241](../docs/adr/0241-governance-fence-ownership.md)) — removed
  `agent.UntrustedFence`, `agent.WriteUntrustedBlock`, `agent.FenceUntrusted`, and
  `agent.NeutraliseFraming` as part of their clean relocation to governance. This
  is a breaking API change (pre-v1 a minor bump); callers must use the governance
  equivalents listed below. `agent.StripLoneCodeFence` remains in agent.

### Added

- **Sanitized provider-attempt observation** — adds `session.EvNetworkAttempt`,
  `session.NetworkAttemptPayload`, `session.CanonicalNetworkAttempt`,
  `session.NetworkCorrelationDigest`, `port.AttemptObserver`, `port.WithAttemptObserver`,
  and `port.ObserveAttempt`. The run-local observer lets provider decorators return
  typed evidence to the loop, whose canonicalization rejects invalid observations,
  binds trusted run correlation, omits provider codes, and retains correlation values
  only as fixed domain-separated SHA-256 digests. Added (minor).

- **`agent.LRUForkReaper.Close`** — releases all retained Parallel winner-fork
  workspaces during graceful composition shutdown. Added (minor).

- **Manual session compaction core** — `session.ReplaceHistoryAtBoundary` provides
  the pairing-validated, non-active aggregate rewrite seam; `agent.Engine.CompactSession`
  and `agent.ManualCompactionResult` run the configured compactor once and expose the
  archive/summary needed by a durable service operation. Added (minor).

- **Debug session identity** — adds `session.SessionKindDebug` and
  `session.NewDebug`, creating a separate empty-workspace session bound to one
  target session without copying target conversation state. Added (minor).

- **`agent.WithAgentWritableModelEngineFactory`** (issue #517, [ADR 0242](../docs/adr/0242-route-unpinned-writable-named-specialists.md)) — a `SubagentOption` factory that rebuilds an unpinned named `mode:"read-write"` specialist on the semantic router's selected model while preserving its specialist scope, direct-write environment, same-provider boundary, and per-definition limits. A declined target falls back to the ordinary writable specialist. Added (minor).

- **Canonical governance untrusted-content fencing** (issue #380,
  [ADR 0241](../docs/adr/0241-governance-fence-ownership.md)) —
  `governance.UntrustedFence`, `governance.WriteUntrustedBlock`,
  `governance.FenceUntrusted`, `governance.NeutraliseFraming`, and
  `governance.NeutraliseDelegationResult` are the five canonical public APIs for
  byte-identical framing and neutralisation of untrusted model-visible content.
  `NeutraliseDelegationResult` is exported so delegation result renderers can use
  the canonical neutralisation policy without duplicating the marker logic. This
  addition is paired with the breaking removal of the former agent APIs above.

- **Run-owned attempt correlation context** (issue #409) —
  `port.WithRunAttemptContext` installs one independent session/run/turn carrier
  and `port.SetAttemptTurnIndex` updates its atomic turn across an engine run,
  without changing the immutable derivation semantics of the public
  `WithRunSerial` and `WithTurnIndex` helpers. Added (minor).

- **`session.EvModelRetry` and `session.ModelRetryPayload`** (issue #409) — mark a failed-step retry after `session.init` with typed disposition/progress for event-source reconstruction plus client-visible advisory text.

- **Structured provider attempt-error metadata** (issue #409) —
  `port.ProviderErrorMetadataError` lets provider failures expose HTTP status,
  in-band status, provider code, and correlation kind/ID through primitive getter
  methods and `errors.As` without widening `LLMRequest`. The structural shape lets
  independently released provider modules compile against older engine versions;
  root composition validates the closed correlation vocabulary and bounded tokens
  before logging. Added (minor).

- **Failed-step retry entry** (issue #409) —
  `agent.Engine.RetryFailedStep` resumes an aggregate carrying durably prepared
  retry intent through the normal run lifecycle and turn loop without recording
  another user prompt. Persisted conversation/tool state is reused while live
  instruction and system-prompt sources are re-resolved. Added (minor).

- **Typed semantic retry metadata and persisted intent** (issue #409) —
  `session.RetryDisposition` and `session.StreamProgress` classify failed model
  streams and expose closed-vocabulary `Valid` checks; aggregate methods prepare,
  inspect and restore snapshot-persisted failed-step retry intent. `port` exposes
  aliases, `errors.As` interfaces, and process-local run/turn context helpers for
  attempt diagnostics without widening `LLMRequest`. New types and methods are
  Added (minor).

- **`session.Principal.IdentityWellFramed`** (issue #368) — the EXPORTED form of
  the owner-key delimiter-safety rule: reports whether a principal's
  authority-bearing components are free of the reserved NUL separator. Owner
  scope keys are derived as `hash(issuer + NUL + subject)`, so a NUL inside
  either component makes distinct principals collide on one namespace. Exported
  so the request edge re-states the rule instead of hand-rolling it; a nil
  principal is not well framed.

- **`agent.WithTeamOwner`** (issue #368, [ADR 0212](../docs/adr/0212-caller-ownership-enforcement.md)) —
  a `SupervisorOption` attributing the members of a DIRECTLY server-created team
  to the verified caller that created it. It is ignored for a team created from
  a parent run, where `caps.owner` carries the authoritative inheritance — the
  same split `WithRootAuthority` already documents.

  It exists because the gRPC `CreateTeam` path deliberately runs with zero
  parent caps (no ask surfacing, no child-ask adjudicator) and owner attribution
  rode in that same struct, so member sessions were published with `Owner ==
  nil`. The option supplies the OWNER ONLY and must not become a general caps
  channel; the zero-caps posture is otherwise preserved.

- **Atomic session first publication** (issue #368, [ADR 0212](../docs/adr/0212-caller-ownership-enforcement.md)) —
  `port.SessionCreator` is an optional backend capability for atomic create-once
  publication without widening `SessionStore`; collisions wrap
  `port.ErrSessionAlreadyExists` and leave the existing session family unchanged.
  Added (minor).

- **Engine-child lifecycle exclusion** ([ADR 0027](../docs/adr/0027-cloud-native.md)) —
  `port.SessionLiveness`, `agent.Deps.SessionLiveness`, and
  `agent.WithMemberLiveness` let a host protect engine-owned Subagent, Parallel,
  and Team session lifecycles (including direct `RunTeam` supervisors) from destructive
  maintenance. Composition now backs that inward port with the configured
  `port.SessionLease`, so queued, running, between-round, and synthesis children hold
  distributed exclusion until teardown. Added (minor); the final `Register` contract
  change is classified below.

- **Context-bound session-migration ownership** ([ADR 0230](../docs/adr/0230-redis-migration-atomic-ownership-and-coverage.md)) —
  `port.SessionMigrationStore.AcquireSessionMigrationJob` binds the exact
  ownership-checking acquisition to a context required by mutations and durable
  checkpoints. Redis renews and atomically fences that acquisition; jsonlstore
  binds its stable flock through the same coherent port contract. Added (minor).
- **Resumable session-storage migration** (issue #589, [ADR 0226](../docs/adr/0226-session-storage-maintenance.md)) —
  `port.SessionMigrationStore` and its plan/family/job value objects define an
  optional server-side v1-to-v2 physical-maintenance capability with durable bounded
  progress, sanitized item errors, and a stable job-scoped cross-process exclusion
  around each mutating load-to-checkpoint sequence, without widening `SessionStore`.
  Added (minor).
- **Retention byte estimates and atomic cleanup** (issue #590, [ADR 0226](../docs/adr/0226-session-storage-maintenance.md)) —
  `port.SessionDiscoveryMeta.EstimatedBytes` lets indexed adapters project a
  content-free deletion estimate to the shared cleanup planner. The optional
  `port.ConditionalPrunableStore` and `SessionDiscoveryMetaEqual` keep durable
  metadata revalidation under the backend family exclusion through deletion.
  The field addition is classified Changed below for unkeyed external literals.
  The reference `memstore.WithDeleteFailure` option scripts deterministic offline
  maintenance failures for conformance tests. Added (minor).

- **Bounded session-storage health** (issue #592, [ADR 0226](../docs/adr/0226-session-storage-maintenance.md)) —
  `port.SessionStorageHealth`, `SessionStorageHealthProvider`, and explicit
  availability fields let optional backends expose content-free indexed aggregate
  status without widening `SessionStore` or fabricating zero values. Added (minor).
- **Legacy-session adoption audit labels** (issue #593, [ADR 0226](../docs/adr/0226-session-storage-maintenance.md)) —
  optional `session.Session.Adoption` metadata persists the source relationship and
  caller/source/request-bound retry proof on explicitly adopted main sessions.
  Existing sessions leave the pointer nil. `session.AdoptionMetadata` and its
  nil-preserving `Clone` method are Added (minor); replacing the two inline Session
  fields with the pointer is classified Changed below.

- **Generation-bound session metadata continuation** (issue #587, [ADR 0226](../docs/adr/0226-session-storage-maintenance.md)) —
  `port.ErrSessionMetadataCursorRestart` makes stale/filter-mismatched continuation
  explicit, and `PaginateSessionMetadataBound(rows, request, generation string)`
  gives scan-based adapters the same generation and ownership-scope contract as
  indexed stores. The caller supplies its own cheap monotonic generation signal
  (e.g. a counter bumped on mutation) rather than the helper deriving one by
  JSON-encoding and SHA-256-hashing the full filtered row set on every call — a
  large constant-factor cost removed from every page after the first on
  memstore, the one adapter that used this helper (the row copy/sort itself stays O(rows) per call either way, so this is not an asymptotic change).
  Added (minor).

- **Shared session-metadata ordering and owner-scope hashing** ([ADR 0226](../docs/adr/0226-session-storage-maintenance.md)) —
  `port.CompareSessionMetadataOrder` is the one comparator for the pagination
  ordering (`ModifiedAt` DESC, `ID` ASC) that `port`, jsonlstore, and redisstore
  already had to agree on independently; `session.PrincipalScopeHash` is the raw
  `sha256(Issuer + "\x00" + Subject)` primitive `port`, jsonlstore, and redisstore
  build their own prefixed/truncated owner scope keys on top of. Both are
  extractions of pre-existing, unchanged behavior — no on-disk or wire format changed.
  Added (minor).

- **Physical authority resource identity (ADR 0234)** — `tool.AuthorityResourceResolver` is an optional Workspace extension that derives a physical, workspace-confined local resource identity for authority evaluation. New identifier: Added (minor).

- **Optional authority-owner requirement (ADR 0234)** — `port.AuthorityOwnerRequirement` lets an authority evaluator explicitly require a verified owner identity while preserving ownerless operation for evaluators that do not need one. Added (minor).

- **Delegation authority tightening (ADR 0234)** — `agent.DelegationTightening` and the additive `AgentMeta` authority-ceiling fields let composition supply an explicit specialist ceiling and callers request only narrower child authority. Added (minor).

- **Direct-team authority root (ADR 0234)** — `agent.WithRootAuthority` lets composition stamp a pre-minted root capability set on members of a directly server-created team while intentionally leaving parent-driven child derivation to its dedicated seam. Added (minor).

- **Authority evaluator port (ADR 0234)** — `governance.CapabilitySet` provides pure monotone narrowing and delegation-hop consumption, while `port.AuthorityEvaluator` carries a provider-neutral authority request and decision contract. `port.AuthorityResource` provides a normalized, workspace-bound local target derived at the execution boundary without forwarding raw arguments. The noop and local set-check reference adapters are available for explicit composition choices. New identifiers are Added (minor).

- **Validated automatic learned-skill activation ([ADR 0224](../docs/adr/0224-validated-automatic-skill-activation.md))** —
  `learning.SkillActivationPolicy` adds the closed validated/evaluated assurance vocabulary and
  `learning.ValidatedSkillActivator` adds an optional atomic repository capability for
  evidence-backed accepted/exact ABSTAIN versions, and `learning.EvaluationError` is the durable,
  non-activatable evaluator-infrastructure marker. New identifiers are Added (minor); the standard
  app's Auto default changes independently while the engine pipeline zero remains evaluated.

- **Configurable learning-trigger policy ([ADR 0114](../docs/adr/0114-configurable-learning-trigger-policy.md))** —
  `learning` adds strict sensitivity, scoped signal detection, closed admission
  request/decision/class/reason contracts, pure threshold/always/never policies,
  host-requested provenance, message spans, content-free activity metrics, and
  `agent.EvidenceReflector.RequestTokenEstimate` for exact bounded-request reservation.
  New identifiers are Added (minor); the additive `Trajectory` fields are classified
  Changed below.

- **Remote session-store capability negotiation** (issue #557) —
  `port.SessionDeleteSupport` lets a compatibility-shaped `PrunableStore` report
  that its negotiated backend cannot delete sessions, so inventory consumers
  fail closed without removing the existing `List`/`Delete` operation surface.
  The new optional interface is Added (minor).

- **Session discovery taxonomy, title provenance, and bounded metadata paging** (issues #471 and #557,
  [ADR 0217](../docs/adr/0217-session-discovery-continuation.md)) —
  `session.SessionKind` / `session.SessionRelationship` define the validated
  main, scheduled, Subagent, Parallel-branch, team-member, and legacy-unknown
  vocabulary. `session.TitleProvenance` records legacy/unknown, first-prompt, or
  operator authorship, and `session.(*Session).RenameTitle` applies a bounded explicit
  rename. `port.SessionDiscoveryMeta`, `SessionMetadataPager`, cursor/page
  values, and reference conformance add optional bounded discovery without
  changing the required `SessionStore` Save/Load interface. New identifiers are
  Added (minor); additions to existing exported structs are classified Changed below.

- **Per-model-call session identity context** (issue #543) —
  `port.WithSessionID` and `port.SessionIDFromContext` carry the exact active
  session identity through the existing provider call context without widening
  `LLMRequest`. New exported functions: Added (a minor bump).
- **Evaluated, versioned agent-owned skills (issue #510, [ADR 0110](../docs/adr/0110-evaluated-agent-owned-skills.md))** — `learning` adds the concrete body-only skill lifecycle, bounded provenance/evaluation/receipt values, owner/partition-aware `SkillRepository` CAS seam (including explicit reject), `SkillDraftInput`, `SkillValidator`, `SkillEvaluator`, and the optional bounded `SkillReceiptRepository` pagination seam with opaque stale-cursor errors. `memskill`, `skillconformance`, and `skillvalidation` provide reference proofs and logical admission checks; `skillmaterialize` provides recoverable proposal-to-draft linkage; `skilllifecycle.Pipeline` applies off/review/auto policy synchronously and its optional `Quarantiner` fail-closes a committed publication error; and `skillfs.AtomicCatalog` publishes immutable live generations without changing the existing snapshot source contracts. Proposal records can explicitly link a deferred procedure to a draft without a memory receipt. New identifiers are Added (minor); existing struct/interface changes are classified below.

- **Verified evidence previews (issue #509)** — `learning.EvidencePreview` exposes the
  bounded canonical projection of a digest-verified message or event while omitting
  reasoning, raw tool/permission arguments, binary bytes, controls, and secret-shaped
  content. Added (minor).

- **Durable staged learning proposals and atomic memory convergence (issue #509,
  [ADR 0109](../docs/adr/0109-staged-learning-proposals.md))** — `engine/learning`
  adds bounded proposal lifecycle values and the narrow `ProposalRepository` CAS
  seam. `engine/adapter/memproposal` plus shared conformance provide the reference
  store, and `engine/adapter/memorypromotion` provides conservative fact policy,
  per-candidate presence/version CAS, crash reconciliation, and compensating undo.
  `tool.MemoryConvergenceStore` adds an optional absence-aware CAS capability beside
  the compatible lifecycle interface. New identifiers are Added (minor); existing
  struct additions are classified Changed below.

- **Evidence-grounded reflection core (issue #509,
  [ADR 0109](../docs/adr/0109-staged-learning-proposals.md))** — `engine/learning`
  adds closed candidate/outcome/signal values, bounded input/outcome validation,
  content-addressed canonical message/event evidence projections, structural signal
  detection, and the storage-neutral `Reflector` seam. `engine/agent` adds
  `EvidenceReflector`, a conservative one-turn provider-neutral implementation with
  zero tools and strict bounded JSON output/evidence resolution. These are new
  exported identifiers and are classified Added (minor); persistence and promotion
  remain host/composition concerns, and skill promotion is out of scope.

- **Operator-profile core dependency slice** (issue #508) — `engine/tool`
  adds remotely representable memory lifecycle values, copy-safe attribution
  context helpers, typed validation/conflict errors, and the optional
  `MemoryLifecycleStore` capability without changing the six-method `MemoryStore`.
  `engine/adapter/memmemory` supplies the in-memory reference implementation and
  shared conformance coverage; `engine/adapter/memorytools` supplies portable
  project/user Remember, Recall, Search, Inspect, Forget, and Undo bodies, adding
  lifecycle tools only when the store advertises that capability. `engine/prompt`
  adds the bounded volatile `OperatorProfileSource`/renderer contract;
  `agent.Deps.OperatorProfileSource` refreshes it before each request with a
  run-local last-good fallback. Memory writes receive run/session attribution,
  with the automatic reviewer preserving the completed parent trajectory as
  origin. `tool.CanonicalMemoryText` is the shared normalize-before-classify
  representation for write validation and every memory render boundary. New standalone
  types, constants, functions, and interfaces are Added
  (minor); additions to existing public structs are classified Changed below.

- **Optional completed-trajectory learning seam** (issue #507,
  [ADR 0106](../docs/adr/0106-optional-learning-seam.md)) — the new importable
  `engine/learning` package provides the strict `Mode` vocabulary (`off`, `review`,
  `auto`; zero is `off`), an owned `Trajectory` snapshot, and synchronous `Observer`.
  `agent.Deps` gains additive `LearningMode` and `LearningObserver` fields, and
  `agent.UserModelReviewer` implements the observer over snapshot history, with
  `agent.NewUserModelObserver` constructing the storage-free automatic path; its
  legacy ID-based `Review` path retains the SessionStore-backed constructor. All are
  new exported identifiers/fields and therefore Added (minor).
- **Steer-while-running: in-flight operator steer injection** (issue #512,
  [ADR 0232](../docs/adr/0232-steer-while-running.md)) — a `Run`-scoped,
  single-slot, append-default mutex inbox that the agent loop drains at the
  Step 2a turn boundary (the same provider-legal seam
  `injectBackgroundNotice`/`drainPendingDelivery` use) and records as an
  ordinary harness-authored user continuation (`recordContinuation`:
  `RecordUserPrompt` + the log-only `EvUserPrompt`), so a drained steer
  replays to the model as an ordinary user turn and flows through compaction /
  `session.ValidateToolPairing` / ADR-0038 rehydration unchanged. Steer text is
  UTF-8-repaired at ingress (`session.ToValidUTF8`) so recorded history ==
  `EvSteer` echo == model view. A pending (un-drained) steer is in-memory and
  lost with its run (crash/cancel/Abandon) — never persisted (reset-by-design,
  ADR 0027 rows 60/37). A parked steer blocks a clean terminal until it drains
  (the clean-exit continue-run rule; never-drop holds engine-internally), and
  both terminate paths drain-then-close so a parked steer is recorded into
  durable history before the inbox closes. All Added (a minor bump):
  - `agent.Deps.EnableSteer` — the opt-in knob (default false = strict no-op,
    byte-identical to the pre-steer posture). Composition plumbs it to
    `ServerCapabilities.steer`; no `port` interface is widened.
  - `(*agent.Run).EnqueueSteer` — the exported, wire-facing steer entry point
    the Service routes a live run's operator steer through: `SteerAccepted on
    an empty slot, `SteerAppended` on an occupied slot (the bundle grows by
    `"\n\n"` + text), `SteerTooLate` (plain value, never an error) past the
    terminal close the Service promotes on.
  - `(*agent.Run).CancelSteer` — the wire-facing steer-cancel entry point
    (retracts the run's PENDING, un-drained steer; reports `SteerRetracted` /
    `SteerNonePending`).
  - `(*agent.Engine).SteerEnabled` — the read-only seam composition reads to
    advertise the capability from the SAME wired knob (single-source, never
    recomputed per sink).
  - `agent.SteerOutcome` + the closed enum vocabulary `SteerAccepted` /
    `SteerAppended` / `SteerRetracted` / `SteerNonePending` / `SteerTooLate` —
    the authoritative result of an enqueue/cancel transition (an ENUM, not
    stacked booleans).
  - `session.EvSteer` (`"steer"`) + `session.SteerPayload` + the
    `session.Event.Steer` field — the CLIENT-VISIBLE drain echo carrying the
    COMMITTED merged bundle text (the version actually drained). The recorded
    == streamed == model-view invariant holds: the echoed text is byte-identical
    to the user message recorded into history and replayed to the model. Wire
    string passthrough (no proto enum).

- **OpenRouter downstream-provider routing echo** (issue #480) — two new
  constants that surface which DOWNSTREAM inference provider OpenRouter routed a
  request to (mecatl's "provider" stays the wire adapter):
  - `port.ChunkProviderRoute` — a `ChunkKind` carrying the downstream provider
    slug on `Chunk.Text`, emitted by the OpenAI adapter only for the openrouter
    registry entry at the terminal `response.completed` event. Display-only and
    provider-private in contents (the same discipline as `ChunkPhase`): never
    branched on, never replayed, absent on a cache hit.
  - `session.EvProviderRoute` (`"provider.route"`) — the `EventType` the loop
    relays the slug onto for clients. Advisory + metadata-only, a string
    passthrough on the wire (no proto enum). Both are Added (a minor bump).

- **`port.ScheduleCreator` and `port.ErrScheduleAlreadyExists`** — an OPTIONAL
  atomic create-only seam for `port.ScheduleStore` (mirroring
  `ScheduleOneShotReArmer`'s type-assertion discovery pattern), closing a
  check-then-Save TOCTOU window where two concurrent same-name schedule
  creates could both observe absence and one silently overwrite the other.
  `memschedulestore`, `jsonlstore`, and `redisstore` all implement it; a store
  that does not degrades to the pre-existing check-then-Save fallback. New
  exported interface + sentinel: Added (a minor bump).

- **`agent.WithSubagentOwnershipEnforced`** — wires the verified request edge's
  ownership policy into `Subagent` resume authorization. When enabled, a resume
  requires the caller to match the persisted child owner, including when the
  request context is unexpectedly missing a principal; disabled deployments
  preserve legacy ownerless compatibility. New exported function: Added (a
  minor bump).

- **`agent.NewInspectMemberToolWithOwnership` and
  `agent.NewInspectSubagentToolWithOwnership`** — construct persisted-transcript
  inspection tools with the verified request edge's ownership policy. When enabled,
  access requires the full `(Issuer, Subject)` identity pair; when disabled, the
  legacy no-verifier behavior is retained. New exported functions: Added (a minor
  bump).

- **`session.PrincipalFromClaims` and `session.GrantTypeFromClaims`**
  ([ADR 0206](../docs/adr/0206-oidc-authn-module.md)) — stdlib-only projection
  helpers for embedders that verify credentials outside the engine. Projection
  requires non-empty string `iss` and `sub`, preserves strings byte-exactly,
  never fabricates anonymous identity or derives the `system` grant, and keeps
  JWT/OIDC dependencies out of the engine. New exported functions: Added (a
  minor bump).
- **`session.(*Principal).SameIdentity`** (issue #368,
  [ADR 0212](../docs/adr/0212-caller-ownership-enforcement.md)) — compares the
  immutable `(Issuer, Subject)` owner identity without treating presentation or
  grant metadata as authority. Ownership enforcement uses this single
  projection for callers, resource owners, and explicitly classified system
  actors. A new exported method: Added (a minor bump).

- **Caller-identity labels on the session aggregate** (issue #367,
  [ADR 0204](../docs/adr/0204-caller-identity-threading.md)) — the joint
  field-prep addition for the caller-identity track (`Owner`) and Track C
  (`Authority`), landed together so the generated-surface regeneration is paid
  once:
  - `session.Principal` — the verified-caller value object
    (`{Issuer, Subject, GrantType, Name}`). Identity is the `(Issuer, Subject)`
    PAIR, never `Subject` alone (two IdPs collide on `sub`). It carries no
    scopes, no authority, no credentials, no claims map. Absent identity is a
    nil `*Principal`, never a fabricated anonymous one.
  - `session.GrantType` + `GrantTypeUser` / `GrantTypeClientCredentials` /
    `GrantTypeSystem` + `GrantType.Valid` — the closed three-value enum; the
    zero value is deliberately not a member.
  - `session.Authority` — Track C's label, shipped INERT (nothing reads or
    writes it beyond the snapshot round-trip). Zero value means unset.
  - `session.Session.Owner` / `session.Session.Authority` — the additive labels,
    stamped through the new WRITE-ONCE `Session.RestoreLabels(owner, authority)`
    (a different owner over a set one returns the new
    `session.ErrOwnerAlreadySet`; a nil owner is the ownerless no-auth path and
    does not burn the slot). `sessnap.Snapshot` gains matching `owner` /
    `authority` fields, both `omitempty`, restored through that aggregate method
    — `sessnap.RestoreState`'s signature is deliberately UNCHANGED (a trailing
    parameter would be Changed/breaking; a direct-assignment field is
    Added/minor). A pre-ship snapshot with no `owner`/`authority` key restores
    to a nil owner and a zero authority.

  All of the above are new exported identifiers and new struct fields —
  classified Added per COMPATIBILITY.md (a minor bump). (issue #367)

- **`session.(*Principal).Clone`** (issue #367) — the ONE place the "copy a
  `*Principal` across a boundary, nil stays nil" rule lives. It is
  nil-receiver-safe (a nil principal clones to nil), and every site that hands a
  principal out of, or into, a structure it does not own routes through it
  instead of hand-rolling the nil check and the deref. `Principal` is all-strings
  today so a shallow copy IS a deep copy; the method exists so that the day it
  gains a slice or map field, every site stays correct together rather than
  silently becoming an aliasing bug. A new exported method: Added (a minor bump).

- **`port.SessionMeta.Owner`** (issue #367,
  [ADR 0204](../docs/adr/0204-caller-identity-threading.md) decision 4) — the
  session owner on the cheap picker projection, so a `MetaLister` listing (which
  skips `Load` entirely) renders the owner column IDENTICALLY to the
  `Load`-per-row fallback instead of leaving it empty on the fast path. Nil for
  an ownerless session — a store that cannot decode an owner renders it as
  unowned, never as somebody else. A new struct field: Added (a minor bump).

- **`session.Event.Actor`** (issue #367,
  [ADR 0204](../docs/adr/0204-caller-identity-threading.md) decision 5) — the
  verified caller a durable-log event is attributed to, so an event read in
  isolation names its actor. It is LOG-ONLY and DERIVE-AT-APPEND: every emit
  site — the agent loop included — leaves it nil (the loop is storage- and
  identity-agnostic), and the server relay's single `appendEvent` chokepoint
  stamps it from the LOADED SESSION'S OWNER just before the durable
  `port.EventLog.Append`. It never reaches the client wire (no proto field maps
  it) and it is NOT a reconstruction input: `eventsource.Fold` ignores it, so a
  folded session keeps the owner its caller restored from the snapshot. The
  session owner stays the identity of record; an ownerless (pre-ship) session
  records a nil actor, never a fabricated one. A new struct field: Added (a
  minor bump).

- **`port.ScheduleSpec.Owner`** (issue #367,
  [ADR 0204](../docs/adr/0204-caller-identity-threading.md) decision 6) — the
  verified caller a schedule is attributed to, captured ONCE at create time and
  never derived at fire time (retention sweeps the origin session while the
  schedule lives on, so a fire-time lookup would read a session that no longer
  exists). The store is identity-blind: it round-trips the value verbatim on
  every transport, which the schedule conformance suite now pins along with
  pointer isolation (a loaded spec must not alias the stored record). Nil means
  an ownerless schedule — never a fabricated principal. A new struct field:
  Added (a minor bump).

- **`session.WithPrincipal` / `session.PrincipalFromContext`** (issue #367,
  [ADR 0204](../docs/adr/0204-caller-identity-threading.md) decision 2) — the
  context seam the verified caller rides on. No port interface gains a principal
  parameter; the principal travels in the `context.Context` under an unexported
  empty-struct key. Absent identity reads back as a nil `*Principal`, never a
  fabricated anonymous one — and `WithPrincipal(ctx, nil)` returns `ctx`
  unchanged, so "no identity" can never be laundered into a present-but-empty
  principal. The stored principal is a copy, so a caller cannot mutate what the
  context reports. Added (a minor bump).
- **`session.Session.Abandon`** (issue #475) — a 4th terminal-recovery seam,
  sibling of `Reopen`/`Interrupt`/`Recover`, for a session left `StateRunning`
  by a process that exited mid-turn (a crash-orphaned snapshot never reaches a
  terminal state, so it reports "in progress" forever). Legal only from
  `StateRunning`; it closes out any trailing unanswered `tool_use` via the
  shared `closeOutInterruptedTurn` repair with a new, abandonment-accurate
  close-out message (never claiming a cancellation or a run failure, neither
  of which was observed), then `resetToIdle()`s exactly like the other three
  seams (`Counters` reset, `Usage` preserved). Classified Added (minor).

- **Version-aware file/Edit foundation (ADR 0208, issue #462)** — the
  `tool.Workspace` surface gains explicit, unambiguous mutation operations and
  a version-bearing read, so the agent-facing Read/Edit/Write tools never
  silently clobber a concurrent change:
  - `tool.FileVersion` is an opaque, comparable content version with no
    wildcard or unversioned sentinel. `NewFileVersion` mints it, `Equal`
    compares it, and `Token() (string, bool)` provides transport-safe
    serialization while preserving the zero-value-invalid contract.
  - `tool.LedgerKey(root, path)` is the single stdlib-only, I/O-free lexical
    normalization used by osfs and ACP read ledgers. Ordinary relative and
    in-root absolute forms converge; physical symlink aliases may conservatively
    miss and require another read.
  - `tool.VersionMismatchError` identifies a conditional replace that lost a
    race without exposing opaque versions.
  - `tool.Workspace.ReadVersion` returns content plus its authoritative version;
    `CreateFile` is atomic create-only; `ReplaceFile` conditionally replaces by
    version; `RecordedVersion` is the I/O-free read-ledger lookup replacing
    `WasReadUnchanged`.
  New exported identifiers are Added per COMPATIBILITY.md; the interface method
  changes and `FileSystem.Write` removal are the breaking half recorded below.

- **In-process `tool.Environment` seam** (ADR 0211, issue #462 phase 2) — the
  concrete, immutable execution environment a `Tool.Execute` runs against,
  replacing the per-call `tool.Workspace` + (for Bash) the construction-time
  `CommandRunner` with a single bound seam:
  - `session.EnvironmentRef{Kind, ID}` — the cycle-safe, stdlib-only identity
    value (a backend-family `Kind` + an opaque `ID`) an Environment carries;
    `EnvironmentKind` (`EnvKindLocal`/`EnvKindMem`/`EnvKindNoFS`) is new in the
    session package (so the ref can ride the snapshot/event log without pulling
    tool types in). It is an IN-PROCESS identity in phase 2 — not yet a snapshot
    field (persistence/remote transport deferred to phase 3).
  - `tool.Environment` — the immutable `{Ref, Workspace, CommandRunner}` bundle
    with `NewEnvironment`/`MustEnvironment` constructors and `Ref()`/`Workspace()`
    /`CommandRunner()` accessors. The Workspace is non-nil; the
    runner is optional (nil → Bash surfaces `ErrNoShell`).
  - `tool.EnvironmentForker` replaces `tool.WorkspaceForker`: `Fork` returns a
    COMPLETE child `Environment` (Workspace + a runner bound to the child
    namespace + ref), so a forked child's Bash observes the SAME child
    namespace its Read/Write do.
  - `tool.EnvironmentMerger` replaces `tool.ForkMerger`: `Merge` receives the
    child and parent `Environment`s (no `forkRoot` string crosses the core
    interface).
  New exported identifiers are Added per COMPATIBILITY.md; the `Tool.Execute`,
  `CommandRunner`/`CommandStreamer`, `Engine.Run`/`ResumeApproval`, and forker/
  merger signature changes are the breaking half recorded below. The removed
  `WorkspaceForker`/`ForkMerger` interfaces are recorded under Removed.

- **`session.Session.EnvironmentRef` persisted** (ADR 0214, issue #462 phase 3)
  — the resolved execution-environment identity is now a durable, inert exported
  field on `session.Session` (the same write-once-label posture as
  `Profile`/`ProviderID`/`ModelID`):
  - `session.Session.EnvironmentRef` (`session.EnvironmentRef`, added in phase 2)
    is now persisted: `sessnap.Snapshot.EnvironmentRef` round-trips it through
    `Of`/`Restore` with Go 1.26's `omitzero` (NOT `omitempty`, which never omits a
    non-empty struct) so a default/local session with no remote ref stays
    byte-identical to a pre-phase-3 snapshot — purely additive, no format-tag
    bump. A legacy snapshot with no `environment_ref` key restores the zero ref
    and remains backward-compatible (composition stamps a fresh ref from the
    first resolved live Environment on the next ordinary save; no migration
    sweep). The session aggregate stores it and never interprets it; reattachment
    to a live `tool.Environment` for a non-in-tree Kind lives in composition.
  This is an additive field on an already-Added type (phase 2 added
  `EnvironmentRef`); the persisted snapshot field is Added per COMPATIBILITY.md
  (a minor bump). The COMPATIBILITY.md "Session reconstruction contract" classifies
  `EnvironmentRef` as a not-event-carried identity label (like `Owner`/`Authority`).
  A remote transport itself remains deferred — the ref is durable identity, not a
  transport contract. See ADR 0214.

- **`session.ToValidUTF8` and `session.RepairToolResult`** (issue #402) — the
  UTF-8 repair primitives that close the Converse-stream kill. A tool can hand
  back arbitrary bytes (a command's stdout, a file's contents, an MCP server's
  text), and a protobuf string field REJECTS invalid UTF-8 at marshal time,
  which terminated the live gRPC stream with `codes.Internal`. `ToValidUTF8`
  applies the SAME U+FFFD repair `encoding/json` uses, so the durable event
  log, the model view, and the wire agree byte-for-byte. `RepairToolResult`
  returns a copy of a `ToolResult` with `Content` and the textual fields of
  every `Parts` block normalized (`Text`/`Name`/`Title`/`Description`/`URL`/
  `LastModified`/`Audience`), leaving binary `Data` and the `MIMEType` token
  byte-exact. It is applied at the loop's effective-payload choke point
  (`engine/agent` `execute`, after PostToolUse, before recorder/emit/record);
  the protobuf mapper keeps its own backstop for producers that never pass
  through the loop. Classified Added per COMPATIBILITY.md (new exported funcs
  are a minor bump). (issue #402)

- **`prompt.CommandPostExpansionSource`** — optional additive command-source
  extension consumed by `prompt.SourceExpander`: it supplies activation-derived
  text after the source's raw template has undergone the ordinary frontmatter
  stripping and placeholder substitution. This prevents non-template metadata
  from being interpreted as `$1` or `$ARGUMENTS`; sources that implement only
  `CommandSource` remain byte-identical. Classified Added (minor).

- **Advisory skill frontmatter fields** (#419) — the optional `license`,
  `compatibility`, `metadata`, and `allowed-tools` agentskills.io frontmatter
  fields are now parsed and carried through to the port as advisory/observability
  metadata (never trust-bearing, never a gate):
  - `tool.SkillMeta.License`, `tool.SkillMeta.Compatibility` (string),
    `tool.SkillMeta.Metadata` (`map[string]string`), and
    `tool.SkillMeta.AllowedTools` (`[]string`) — name/description-adjacent
    advisory fields on the always-in-context metadata layer. The parser
    (skillfs.ParseSkill) clamps `License`/`Compatibility` to ≤1024 bytes
    (rune-safe), caps `Metadata` at ≤32 entries with each value ≤4096 bytes
    (dropping the whole map to nil on overflow), and splits `allowed-tools`
    (space-separated string or YAML list form) into ≤64 names each ≤64 chars
    — all with a non-fatal warning note on overflow. A SKILL.md without them
    parses exactly as before.
  - `allowed-tools` (agentskills.io, Experimental) is ADVISORY ONLY: it names
    the tools a skill EXPECTS to use, surfaced as a note on activation that
    explicitly states calls still follow normal permission rules. It is NEVER a
    permission grant — the permission evaluator never reads it, and a call
    still resolves through the normal deny-dominant policy at every posture
    (including yolo).
  - The driver protocol `SkillMeta` message gains `license` (field 5),
    `compatibility` (field 6), `metadata` (field 7, `map<string,string>`), and
    `allowed_tools` (field 8, `repeated string`) — all optional, documented
    advisory; the grpcdriver client/server round-trip them with the SAME
    defensive clamps the parser uses.

- **Skill activation enumerates bundled-asset logical names** (#419) — the
  `Activation` struct (skillfs adapter) gains an `Assets []tool.SkillAsset`
  field populated by both activators (`snapshotActivator` and
  `sourceActivator`) via `ListSkillAssets`. The Skill tool's `Execute` now
  renders a `Bundled files:` block listing each asset's logical name (sorted,
  indented) after the base-directory guidance, so the model discovers bundled
  scripts/references without eagerly reading them (agentskills.io "should
  enumerate but must not eagerly read"). An asset-less skill renders
  byte-identically to before (the enumeration is gated on `len(Assets) > 0`).
  No port change — `tool.SkillSource.ListSkillAssets` already existed.

- **Skill name validation + dir-name match** (#419) — discovery now validates
  the frontmatter `name` against the ONE shared grammar
  `^[a-z0-9][a-z0-9_-]{0,63}$` (skillfs.ValidSkillName, re-exported via
  `internal/adapter/skills.ValidSkillName`), the LAXER agentskills-style form
  with the underscore DELIBERATELY allowed so existing drafted and discovered
  skills keep validating. `ParseSkill` rejects a name that fails the grammar
  with a fatal SkipError reason (fail-soft: the skill is excluded, the scan
  continues), and `DirSource.Skills` additionally enforces the agentskills.io
  dir-name-match rule: the frontmatter `name` must EQUAL the parent directory
  name or the skill is skipped with a mismatch SkipError. The draft write path
  (`internal/adapter/skills/drafter.go`) routes through the SAME
  `ValidSkillName` (behavior unchanged — it already used this exact regex) so
  read and write paths share the single source of truth.

- **Permanent provider-error signal** (#346) — a neutral, fail-open way to tell
  "transient — retry may work" from "permanent — this request shape is rejected":
  - `port.PermanentError` — an interface (`error` + `Permanent() bool`) the
    provider adapters and `llmresilience` implement on their terminal provider
    errors. It rides the error chain (reachable via `errors.As`), NOT a
    `port.LLMRequest` field, so no provider-specific type crosses into
    `engine/agent` and `LLMRequest` stays provider-neutral. Fail-open: an error
    that doesn't implement it is treated as not permanent.
  - `session.ResultPayload.Permanent` — the loop sets it on `EvResult` when the
    run's terminating error is permanent, so relays/clients render "retry won't
    help" instead of an identical raw error wall on every retry.
  - `Session.RecordFailurePermanence(bool)` / `Session.FailurePermanence()` —
    the aggregate persists the permanence of a `failed` state (cleared on
    `Recover`/`resetToIdle`), so the run-entry funnel can surface an honest
    advisory before burning a provider call on a doomed retry.
  - `session.EvRecoverNotice` (`"recover_notice"`) — a transient string
    passthrough event (the `EvNoProgress` pattern) the service layer emits once
    per recovery when a permanently-failed session is re-entered.
  - `sessnap.Snapshot.Permanent` + `sessnap.RestoreState` gains a `permanent
    bool` param — the flag round-trips the snapshot (additive, `omitempty`).

- **`session.Session.RecordLastError` / `Session.LastError`** (#332) — the
  aggregate persists the terminal failure CAUSE of a `failed` state (the loop's
  `session.ResultPayload.Error`, normalised to one line and clamped to 400 runes
  — mirroring the event-side `subagentCausePayload` so the snapshot and
  `subagent.end` event agree byte-for-byte), cleared on `Recover`/`resetToIdle`.
  `sessnap.Snapshot.LastError` + `sessnap.RestoreState` gains a trailing
  `lastError string` param (additive, `omitempty`). A delegation's failure cause
  now survives on the CHILD snapshot independent of the parent's `subagent.end`
  emit — a background child's end-emit can lose the race with the run-end seal
  (`drainChildren`'s `abortEmits`), so the snapshot is the single durable cause
  source. `engine/agent` records the cause on the child before `persistChild` on
  BOTH the foreground and background paths (belt-and-suspenders for the former,
  load-bearing for the latter). No proto change — the cause already rides
  `SubagentPayload.Cause`/`TeamPayload.Cause` on the wire. Classified Added
  (minor) per COMPATIBILITY.md.

- **`session.Message.ReasoningItemID` + `port.Chunk.ReasoningItemID`** (Responses
  replay fix) — the fourth neutral reasoning carrier (joining `Message.Reasoning`
  / `Message.ProviderPhase` / `ToolCall.ItemID`): a single opaque string field per
  message, replayed verbatim, never interpreted or validated, structure neutral /
  contents provider-private. Fixes the Responses-API item misattribution where a
  reasoning summary for item `rs_*` could be attributed to a message item `msg_*`
  because the item id was dropped at the SSE boundary; the openai adapter now
  stamps `Chunk.ReasoningItemID` from the reasoning item's own `item_id` and
  replays it as `id` on rebuild. `session.StripProviderState` clears it alongside
  the other three carriers. Added (minor).

- **`engine/adapter/search` graduated** (#363) — the WebSearch tool body
  (`WebSearchTool` / `NewWebSearchTool`), the Exa/HTTP/SearXNG search providers
  (`ExaProvider` / `HTTPProvider` / `BackendDown`), and the offline `Fake` /
  `Unavailable` SearchProvider now travel with the importable engine module under
  `engine/adapter/search/` (mirroring the #269 fstools precedent). The
  `internal/adapter/tools` package re-exports the tool body via a type/function
  alias so existing callers compile unchanged.

- **`agent.BashTool` / `agent.NewBashTool`** (background-Bash feature, tasks 1–2) —
  the agent-loop `Bash` tool: the foreground half is byte-identical to the fstools
  Bash body's orchestration (same arg validation, timeout ctx, `runner.Run`,
  combined-output shaping, the 25 000-byte cap, the exit-code error), and
  `background: true` detaches the command as a run-scoped background job on the
  parent run's child registry through the `childCapableTool` seam
  (`ExecuteWithParent`) — the job returns immediately with a `bashcmd-<callID>` id
  and runs detached in the REAL workspace (no isolation), cancelled+joined by the
  run-end drain if still live. It lives in `engine/agent` (not the fstools adapter)
  because the background half needs the run's child registry, an agent-package type;
  and it registers under the literal name `"Bash"` (`tool.BashToolName`) because the
  permission evaluator special-cases that name — a second tool name would silently
  bypass the bash gate. Background calls additionally require the runner to
  implement `tool.CommandStreamer` (they decline honestly when it does not); a
  caps-less plain-`Execute` background call is likewise an honest error, never a
  silent foreground fallback. The composition root registers it everywhere Bash
  appears (main catalogs + child surfaces), so a child backgrounds a command against
  its OWN run's registry. Classified Added per COMPATIBILITY.md (a new exported
  type + constructor is a minor bump; no existing identifier changed).

- **`agent.BashCmdJobPrefix`** (background-Bash feature) — the `"bashcmd-"` id
  prefix for background-Bash job ids (`bashcmd-<callID>`). Unlike the three
  delegation prefixes it names NO session (the job is a bare process, not a child
  loop), so the InspectSubagent prefix gate and the child-session retention GC must
  not learn it — it exists so the job-id spelling has one source. Classified Added
  per COMPATIBILITY.md (a new exported constant is a minor bump).

- **`tool.BashToolName`** (background-Bash feature) — the catalog name of the Bash
  tool (`"Bash"`), moved to the port package as the single authority every Bash
  implementation registers under (the permission evaluator special-cases the
  literal, so the constant lives where both the fstools adapter AND `engine/agent`'s
  background-capable `BashTool` can import it). `fstools.BashToolName` now aliases
  it, so existing consumers are unaffected. Classified Added per COMPATIBILITY.md
  (a new exported constant is a minor bump; the alias keeps the old reference
  compiling unchanged).

- **`agent.BashStatusTool` / `agent.NewBashStatusTool`** (background-Bash feature,
  tasks 5–6) — the live background-Bash job tool ("BashStatus"), the registry-backed
  companion of the agent `BashTool`'s `background: true` flag. It reads the parent
  run's child registry through the same `childCapableTool`/parentCaps seam as
  `SubagentStatusTool`, projected over the `bash-cmd` family ONLY (the registry is
  shared; `SubagentStatus` now projects the three delegation families only — the
  two projections are disjoint, so a bash job id is never mislabeled "a subagent"
  and a delegation id is never collected through `BashStatus`). With no args it
  renders the bash-job roster (ids + state + stop labels ONLY — the command text is
  model-authored untrusted and never rides a bulk roster); `job_id` is the per-job
  detail view (a LIVE job: state + command + the current retained-output tail
  snapshot, a peek that is NOT a delivery; a DONE job: the stored result through the
  registry's exactly-once collect machinery, the error bit riding along); `wait_ms`
  (same 120s cap as `SubagentStatus`, the shared `waitForChild` discipline) parks on
  the job's doneCh or the registry's terminal generation; `cancel: "<job_id>"`
  signals the job's per-call context (the tool stays read-only — it only signals).
  The caps-less plain-`Execute` path is the same honest "no live registry" error
  shape `SubagentStatus` uses. Registered in the composition root iff Bash is, never
  in child catalogs. Classified Added per COMPATIBILITY.md (a new exported type +
  constructor is a minor bump; no existing identifier changed).

- **`tool.CommandStreamer`** (background-Bash feature, task 3) — a new OPTIONAL
  `CommandRunner` capability interface in `engine/tool`. `RunStreaming(ctx,
  command, workdir, out io.Writer) (exitCode int, err error)` runs the command
  under the same shell/workdir/timeout/cancel rules as `CommandRunner.Run` but
  streams stdout+stderr INTERLEAVED into a caller-owned `io.Writer` (the caller
  owns bounding — e.g. a bounded tail ring for a background command's recent
  output — so the runner must not also buffer or head-cap the stream). The
  `exitCode` return replaces `CommandResult` for this path: a non-zero exit is
  reported there, not as an error. It is discovered by type assertion; a runner
  that does not implement it simply declines, and the caller fails soft with an
  honest "not supported by this runner" rather than falling back to `Run` and
  silently losing the tail. The in-tree `osfs` runner implements it (sharing one
  private spawn/wait tail with `Run` so the two cannot drift); `memfs` and the
  engine's test fakes do not (and need not — it is optional). Classified Added
  per COMPATIBILITY.md (a new exported interface is a minor bump; no existing
  identifier changed).

- **`prompt.Rule`, `prompt.RulesSource`, `prompt.RuleOrigin`, `prompt.RulesAssembler`, and `prompt.MaxRuleBytes`** (issue #329, task 1) — the port, value-object, and turn-0 assembler for `.claude/rules` / project-rules discovery. `Rule` is a pure value object (name, body, path globs, origin tier label — no path/dir/root concept, matching the `SoulSource`/`CommandSource` discipline). `RulesSource` is a consumer-defined `ListRules` port satisfied structurally by the adapter at the composition root. `RuleOrigin` (`project`/`user`/`driver`) is a tier label never a location, the third parallel closed label set after `SkillOrigin`/`AgentOrigin`. `MaxRuleBytes` (20 KiB) is the one canonical per-rule body cap all sources share, mirroring the soul body cap. `RulesAssembler` renders the discovered rules as a single user-role message, fenced, with combined byte (40 KiB) and count (32) caps, and a dropped-footer when truncated — fail-soft throughout. `RulesHeader()` exposes the shared header string so tests can assert the exact emitted text. The assembler sits naturally after `RootAssembler` and before `SoulAssembler` in the turn-0 ordering (project context → persona → saved facts → operator model). `IsInjectedTurn0Fragment` recognises the rules header. See ADR 0081 (to be written in task 3). Classified Added per COMPATIBILITY.md (new types, const, func, and assembler are a minor bump).
- **`agent.WithTeamSharedBaseWorkspace` + `agent.WithTeamToolSharedBaseWorkspace`**
  (path-escape-posture task 06, Scenario 5 AC5.1d) — the team-supervisor
  analogue of `WithSharedChildWorkspace`: a `SupervisorOption` (plus its
  `TeamOption` counterpart the in-loop Team tool threads through) injecting the
  NON-relaxed workspace view a BASE-SHARING (shell-less) read-only team member
  runs against. The composition root wires it whenever the team base may carry
  out-of-root relaxation (the auto/yolo main-session relax): the supervisor's
  base-share fallback otherwise hands the member the base VERBATIM, silently
  giving the shell-less member the main session's escape reach — the same
  child-never-relaxes leak task 05 closed on the Subagent nil-forker path. The
  closure receives the base workspace root and returns the member's workspace;
  a nil return falls back to the verbatim base. The two FORKED tiers (Mutating
  force-copy, read-only worktree) never consult it — their forks already land
  in a non-relaxed constructor. nil (the default) is byte-identical to the
  pre-option behaviour. Classified Added per COMPATIBILITY.md (new exported
  option funcs are a minor bump). (path-escape-posture plan, task 06)

- **`agent.WithSharedChildWorkspace`** (path-escape-posture task 05, Scenario 5
  AC5.1b) — a new `SubagentOption` injecting the NON-relaxed workspace view a
  BASE-SHARING child runs against. The composition root wires it whenever the
  parent workspace may carry out-of-root relaxation (the auto/yolo main-session
  relax): a nil-forker read-only child (no shell wired) and the
  `mode:"read-write"` direct-write child (ADR 0041) both run against the parent
  base, and must see it WITHOUT the relax — the relax is main-session-only. The
  closure receives the parent workspace root and returns the child's workspace;
  a nil return falls back to the parent ws unchanged. A FORKED child
  (`WithChildForker` wired) never consults it. nil (the default) is
  byte-identical to the pre-option behaviour. Classified Added per
  COMPATIBILITY.md (a new exported option func is a minor bump).
  (path-escape-posture plan, task 05)
- **`session.SubagentPayload.Text` / `.Detail` / `.InnerKind` and
  `session.ParallelPayload.Text` / `.Detail` / `.InnerKind`** (ADR 0079,
  delegation-observability-convergence) — the two delegation payloads gain the
  same bounded-preview fields `TeamPayload` already carried: `Text` (a clamped
  child message/result-text preview), `Detail` (a clamped tool-call-args or
  tool-result-body preview), and `InnerKind` (the inner event kind the preview
  was projected from). Every preview is fed only through `clampPreview`
  (control-byte scrub + rune cap) at the single `drainChildObserved`
  chokepoint, is client-only, and never enters the parent's `Conversation`
  (gauntlet #7 unchanged). Classified Added per COMPATIBILITY.md (new struct
  fields are a minor bump). (delegation-observability-convergence plan,
  tasks 01–02)
 (chore(engine): api snapshot + CHANGELOG for the ADR-0079 payload widening)

- **`agent.WithMemberErrorRetries` + `agent.MemberOutcome.ErrorRounds` +
  `session.TeamMemberDisposition.ErrorRounds`** (issue #318, ADR 0200) —
  the bounded MEMBER RETRY that closes #318's last acceptance bullet ("a team
  member that hits one transient stall still participates in later rounds").
  ADR 0200 made a failed member's session RECOVERABLE, which rescued the lead's
  synthesis turn but still descheduled the member for the rest of the run.
  `WithMemberErrorRetries(n)` is a new `SupervisorOption` (default **1**) capping
  how many `StopError` rounds a member is retried through before it is benched with
  the same disposition it received before this release (`stopped` +
  `StopReasonError`, tasks released). `WithMemberErrorRetries(0)` restores
  bench-on-the-first-errored-round — the SCHEDULING half only. It does NOT restore
  the pre-ADR-0200 behaviour of the same round: a member whose round ends in
  `StopError` still has its session RECOVERED (`session.Session.Recover`) rather than
  latched `nonResumable`, so a failed LEAD still runs its synthesis turn either way.
  That half has no knob — see the "A FAILED delegated child is now resumable" entry
  under **Changed** for the one lever (`agent.WithSubagentStore`) and its scope.
  `MemberOutcome.ErrorRounds`
  and its `session.TeamMemberDisposition.ErrorRounds` mirror are new `int` COUNTS of
  a member's run-level failed rounds — the disposition-honesty signal, since a
  retried-then-finished member is `DispositionDone` with no `Reason` and would
  otherwise be indistinguishable from one that never failed. No enum gained a value
  (`MemberDisposition` is closed and mirrored on the proto wire) and no exported
  signature changed. Classified Added per COMPATIBILITY.md (a new option constructor
  plus two struct fields are a minor bump).

  **BEHAVIOUR CHANGE for library consumers on the default configuration:** a team
  member whose round ends in `session.StopError` is no longer benched on the first
  failure. It stays schedulable, its in-progress task claim is RELEASED back to
  pending (so it or a peer can re-claim), it is force-scheduled for one retry turn
  carrying a supervisor-authored retry note, and it is benched only once its errored
  rounds EXCEED the cap. The counter is per-member and NEVER reset, so at the default
  cap of 1 the exposure is exactly **one extra scheduled round per member over the whole
  team run** — not one per failure: a permanently-failing member runs `cap+1` = 2 rounds
  in total instead of 1, which is what bounds it and the reason the default is 1.
  A failed RECOVERY (`nonResumable`), a CANCELLED member, and a turn-budget-exhausted
  member are never retried. Pin `WithMemberErrorRetries(0)` to restore the previous
  release's SCHEDULING behaviour (see the scope note above — the session recovery is
  not covered by it).

  **OPERATORS of `mecated`/`mecatui`/`mecak8s` cannot reach `WithMemberErrorRetries`, and
  that is deliberate.** There is no `--max-member-error-retries` flag and no
  `app.Config` field: this is a per-member BEHAVIOURAL bound, and the repo's line is that
  those stay engine-only defaults while team-wide RESOURCE ceilings get flags. The
  precedent is the sibling `WithMemberTurnBudget` (default 200 turns per member), which is
  likewise engine-only with no operator flag — not `WithTeamTokenBudget`, which is
  team-wide and is wired to `--max-team-tokens`. An operator's control over the extra spend
  is therefore the existing token ceilings: `--max-team-tokens` (team-wide, summed across
  all members and rounds) and `--max-run-tokens` (per run, inherited by every member
  drive), on top of the built-in round cap and per-member turn budget. A LIBRARY consumer
  that wants fail-fast passes `WithMemberErrorRetries(0)`. (issue #318)

- **`session.SubagentPayload.Cause`** (issue #319) — a new `string` field on the
  redacted `subagent.*` observability projection carrying the child run's FAILURE
  DETAIL (the loop's `session.ResultPayload.Error`) when `Stop` is `StopError`;
  empty on every other terminal, and set on `EvSubagentEnd` ONLY. It is
  harness/provider metadata (a transport or loop error string), never
  child-authored model output, so it is gauntlet-#7 safe on the same footing as
  `Stop`/`Usage`. It is LINE-ORIENTED by contract — normalised at the emit site
  (whitespace collapsed to single spaces, then rune-clamped), so a consumer renders
  it as-is rather than re-deriving the collapse for its own single-line surface. It
  rides the proto/client wire end-to-end (`Subagent.cause` = field 17). Classified
  Added per COMPATIBILITY.md (a new struct field on an existing payload is a minor
  bump). Behaviour note for library consumers: the model-facing `Subagent` tool result
  on a `StopError` terminal now LEADS with this cause and demotes the child's last
  assistant text to clamped "Last activity before the failure" context — the same
  change applies to a failed `Parallel` branch's reported reason. Both halves of that
  body are framing-NEUTRALISED (`agent.NeutraliseFraming`) before composition, since
  neither is harness-authored: a provider error string or a child's prose could
  otherwise forge the `agentId:` trailer or a bracketed harness note it is composed
  next to. No exported signature changed. (issue #319)

- **`session.TeamPayload.Cause`** (issue #331) — a new `string` field on the redacted
  `team.*` observability projection carrying the member run's per-round FAILURE DETAIL
  (the loop's `session.ResultPayload.Error`) when the round's `EvResult.Stop` is
  `StopError`; empty otherwise. Set on `EvTeamMember` with `InnerKind=EvResult` ONLY,
  and ONLY when the round failed; empty on every other inner kind and on the terminal
  `EvTeamEnd` disposition snapshot (which stays the closed-enum reason). It is
  harness/provider metadata (a transport or loop error string), never member-authored
  model output, so it is gauntlet-#7 safe on the same footing as `Stop`/`Usage`. It is
  LINE-ORIENTED by contract — normalised through the shared `subagentCausePayload`
  chokepoint (`engine/agent/subagent.go`, the one place Subagent and Team both collapse
  a provider error body: whitespace collapsed to single spaces, then rune-clamped to
  `maxSubagentCausePreview` = 400), so a consumer renders it as-is. It is CLIENT-ONLY
  (events + mecatui + ACP), NOT model-visible. It rides the proto/client wire end-to-end
  (`Team.cause` = field 19). A retried member's failed rounds each surface their own
  cause. Classified Added per COMPATIBILITY.md (a new struct field on an existing payload
  is a minor bump). No exported signature changed. (issue #331)

- **`agent.SessionOriginScheduleManager` + `agent.NewSessionOriginScheduleManager` +
  `agent.OriginBinder` + `agent.Deps.OriginBinder`** (ADR 0075,
  fire-result-delivery task 02) — a new `port.ScheduleManager` wrapper that
  holds the current origin session id in a `sync/atomic.Pointer[session.SessionID]`
  and stamps every `CreateSchedule` call's `OriginSessionID` from the bound id
  (empty if unbound) before delegating. The `OriginBinder` interface (single
  method `BindSessionOrigin(session.SessionID)`) is an OPTIONAL `Deps` field
  the engine calls in `startRun` so the executing session's id is captured at
  the run boundary. Composition wraps the real manager with
  `NewSessionOriginScheduleManager` and wires it as both the tool's manager and
  the engine's `OriginBinder`. The model-supplied `origin` arg is never in the
  tool schema and is ignored if present — the bound id always wins. Classified
  Added per COMPATIBILITY.md (a new exported type + constructor + interface +
  Deps field are a minor bump). (fire-result-delivery plan, task 02)

- **`port.ScheduleSpec.OriginSessionID`** (ADR 0075, fire-result-delivery Phase 1) —
  a new `session.SessionID` field on `ScheduleSpec` carrying the session whose
  terminal result delivery should receive the fire's outcome. Empty means no
  delivery (the v1 pre-delivery posture). Non-empty values are validated at the
  create-seam: a non-existent session is rejected fail-closed with
  `ErrInvalidArgument`. The field is METADATA-ONLY — it is never rendered into a
  prompt, never surfaced to the model, and never appears in any model-visible
  surface. It is an infrastructure-level routing key the fire path reads to route
  the outcome. An empty OriginSessionID in out-of-band creates (REST/gRPC, no
  conversation) persists empty — additive, no existing behavior changed.
  Classified Added per COMPATIBILITY.md (a new struct field is a minor bump).
  (fire-result-delivery plan, task 01)

- **`port.DeliveryQueue` + `port.DeliveryNote` + `port.NopDeliveryQueue`**
  (ADR 0075, fire-result-delivery Scenario 4 / ADR 0027 List 1+2) — a new
  DURABLE per-session pending-delivery queue port for scheduled-task fire
  results, keyed on the ORIGIN session id (not a per-Run registry). The queue
  holds the opaque rendered note text (from the delivery renderer) plus a
  monotonic per-session sequence that is the exactly-once ledger key: a note
  queued during one run drains on the origin's next run-entry if the current
  run ends first (the ledger is session-scoped). A durable backing (the same
  durability the session snapshot has) survives a process restart so a note
  queued before a restart drains after it; `NopDeliveryQueue` is the
  byte-identical no-delivery default (a deployment with delivery unwired sees
  nothing). The loop stays storage-agnostic: the fire path (composition) ENQUEUEs,
  the loop's turn-boundary drain + the run-entry funnel DEQUEUE via this port.
  Classified Added per COMPATIBILITY.md (a new interface + value type + no-op
  default are a minor bump). (fire-result-delivery plan, task 04)

- **`agent.ScheduleQueryTool` + `agent.NewScheduleQueryTool` +
  `agent.ScheduleQueryToolName` (ADR 0073, the AC1.4 read-parallel/mutate-serial
  partition)** — the scheduled-task surface is now TWO catalog entries over the
  ONE injected `port.ScheduleManager`: a read-only query tool (list/inspect,
  catalog name `"ScheduleQuery"`, `ReadOnly()==true` so list/inspect join the
  read-parallel batch) and the mutating `agent.ScheduleTool`
  (create/pause/resume/delete/fire, `ReadOnly()==false` so every mutating call
  serialises). The split realises the per-verb ReadOnly partition a single
  tool's no-arg `ReadOnly()` cannot express: a mutating verb can never fan out
  into the read-parallel batch. Classified Added per COMPATIBILITY.md (a new
  exported tool + constructor + name are a minor bump). (schedule-tool plan,
  task 06 repair)

- **In-flight scheduled-fire state model (#386, Phase 1)** — Added, a minor bump.
  Two new `session.StopReason` / `port.ScheduleStore` surface additions and three
  new struct fields modelling an in-flight fire between a Claim and a terminal
  RecordFire:
  - `session.StopTimeout StopReason = "timeout"` — a per-fire wall-clock deadline
    expired; a CLEAN, recoverable terminal (like `StopBudget`: a budget
    exhaustion, not a fault/cancel), routed through the completed path so the
    session ends `completed` and stays Reopen-recoverable. A string passthrough on
    the wire (no proto enum). Added to `agent.isEmptyTerminalStop`'s allow-set
    (a clean bounded terminal); it remains subject to the carried token budget
    during a salvage drive.
  - `port.ScheduleStore.RecordFireStart(ctx, name, fire)` — persists the IN-FLIGHT
    fire (id/SessionID/StartedAt/Deadline, Stop empty) and stamps
    `ScheduleState.LastFireSessionID` to the real session id +
    `LastFireStartedAt` (+ seeds `LastFireProgressAt`) + `FireDeadline`.
    Idempotent per fire id.
  - `port.ScheduleStore.RecordFireProgress(ctx, name, fireID, at)` — advances
    `ScheduleState.LastFireProgressAt` and the in-flight fire record's
    `ProgressAt` (monotonic: an earlier `at` is ignored). The `fireID` parameter
    targets the single in-flight fire record by its known key (no scan of the fire
    keyspace), and the write is a no-op on an already-terminal record (a terminal
    fire is never reverted to in-flight). Best-effort/idempotent. (The `fireID`
    parameter was widened into this unreleased signature by the M1/M2 review fix.)
  - `port.ScheduleState` gains `LastFireStartedAt`, `LastFireProgressAt`,
    `FireDeadline` (set at fire-start, cleared at RecordFire).
  - `port.ScheduleFire` gains `StartedAt`, `ProgressAt`, `Deadline` (an in-flight
    fire has Stop empty + StartedAt set; a terminal fire has Stop set).
  - `port.ScheduleSpec` gains `FireTimeout time.Duration` (per-fire wall-clock
    deadline; zero = deployment default).
  The reference `engine/adapter/memschedulestore` implements the two methods
  (Claim/ClaimNow zero the in-flight fields; RecordFireStart sets them; RecordFire
  clears them + writes the terminal record), pinned by new
  `engine/adapter/scheduleconformance` subtests. The root-module stores
  (`internal/adapter/store/jsonlstore`, `redisstore`) and composition wiring are
  Phase 2/3 (not in this phase).
- **`session.SubagentPayload.RoutingReason` / `session.ParallelPayload.RoutingReason` / `session.TeamMemberSpec.RoutingReason`, the `session.RoutingReason*` gate constants, and `agent.WithPinnedAgents`** (issue #397) — the three delegation-start events now carry a bounded, bare-metadata reason WHY the OPT-IN semantic model router did not classify a delegation: EMPTY on a routed hit, otherwise one of the gate constants (`RoutingReasonPinnedModel` / `RoutingReasonAgentDefPinned` / `RoutingReasonResume` / `RoutingReasonFork` / `RoutingReasonRouterDisabled` / `RoutingReasonTargetUnavailable` / `RoutingReasonBreakerOpen` / `RoutingReasonAborted`) or a static classifier/composition miss code (`RouterMiss*`, `empty-model`, `category-selector-empty`, …). This lets a UI distinguish router-absent from pinned-model from agent-def-pinned from classifier-failure from breaker-open — previously every miss/gate collapsed to empty `routed_*`. `WithPinnedAgents` carries the composition-computed model-pin set separately from the routable set, so provider-switched and inline-MCP defs are not falsely attributed as model-pinned. If a routed engine factory declines its target, routed fields are cleared and `RoutingReasonTargetUnavailable` records the fallback while `Model` names the engine that actually ran. The Subagent gate attributes the explicit choice gates (resume / fork / per-call `model` / agent-def pin) ahead of the router-absent gate, so a pinned delegation is never mislabeled `router-disabled`. The reason is clamped at the emit site (`routingReasonPayload`, 200-rune cap) AND confined to an event-safe allowlist (`routingReasonEventSafe`): because the missReason channel is open to external engine compositions via the exported `Deps.SubagentModelRouter`, known detailed composition reasons are reduced to their static code and every other non-allowlisted reason (a provider error body, classifier output, a task excerpt) is substituted with the generic `routing-miss` label on the wire while the verbatim text stays in operator diagnostics — gauntlet #7. Classified Added per COMPATIBILITY.md (new struct fields, constants, and option constructor are a minor bump). See ADR 0083.

### Changed

- **`port.SessionLiveness.Register` lifecycle acquisition** ([ADR 0027](../docs/adr/0027-cloud-native.md)) —
  `Register` now accepts the lifecycle context and cancellation function and returns
  an error, allowing a host to acquire distributed exclusion before a child becomes
  runnable and cancel it on renewal loss. This breaks external implementations and
  is classified Changed for a pre-v1 minor bump.

- **Session migration job exclusion** (issue #589, [ADR 0226](../docs/adr/0226-session-storage-maintenance.md)) —
  `port.SessionMigrationStore` adds `LockSessionMigrationJob`, requiring optional
  migration adapters to hold stable cross-process job exclusion around every mutating
  load-to-checkpoint sequence. The interface addition is breaking for external
  implementations and is classified Changed for a pre-v1 minor bump.

- **Session discovery byte estimate** (issue #590, [ADR 0226](../docs/adr/0226-session-storage-maintenance.md)) —
  `port.SessionDiscoveryMeta` adds `EstimatedBytes`. Keyed literals remain source
  compatible; external unkeyed literals are breaking, so this is Changed for a
  pre-v1 minor bump.

- **Optional adoption audit metadata on `session.Session`** (issue #593) —
  the inline `AdoptionSourceID` and `AdoptionRequestDigest` fields are replaced by
  `Adoption *AdoptionMetadata`, keeping ordinary sessions on the pre-adoption hot-path
  layout while preserving the same persisted labels. The field replacement is
  breaking for external literals and classified Changed for a pre-v1 minor bump.

- **Opaque session metadata continuation** (issue #587, [ADR 0226](../docs/adr/0226-session-storage-maintenance.md)) —
  `port.SessionMetadataCursor` retains neutral ordering, generation, and ownership-scope
  bindings while replacing the storage-specific numeric position with an opaque
  pager-owned `Continuation`. Jsonlstore privately encodes and validates its direct
  byte continuation; other adapters neither expose nor interpret that representation.
  The field change is breaking for external literals and classified Changed for a
  pre-v1 minor bump; `port.SessionStore` remains unchanged.
- **Session-liveness dependency** — `agent.Deps` adds the optional
  `SessionLiveness port.SessionLiveness` field for continuity-aware runs. Adding a
  field to an exported struct is source-breaking for unkeyed literals and is
  classified Changed (pre-v1 minor).

- **Authority evaluator principal identity (ADR 0234)** — `port.AuthorityPrincipal`
  replaces its ambiguous composite `Owner` field with exact `OwnerIssuer` and
  `OwnerSubject` fields. This changes an existing exported struct and is
  source-breaking, classified Changed (pre-v1 minor).

- **Authority evaluator resource actions (ADR 0234)** — `port.AuthorityRequest`
  adds `Action`, distinct from the capability-selected `ToolName`, so policy adapters
  receive the real resource meta-operation while retaining the carried capability
  precheck. Adding a field to an exported struct is source-breaking for unkeyed
  literals and is classified Changed (pre-v1 minor).

- **Authority evaluator wiring (ADR 0234)** — `agent.Deps.AuthorityEvaluator`
  adds the optional execution-time authority evaluator dependency. The added field
  is source-breaking for external unkeyed `Deps` literals and is therefore
  classified Changed (pre-v1 minor).

- **Durable authority payload (ADR 0234)** — `session.Authority` replaces the
  inert string placeholder with the plain carried `governance.CapabilitySet`,
  provenance, and definition identity payload; `Session.BindAuthority` and
  `BoundAuthority` make binding explicit and preserve the legacy-unbound state.
  This changes the existing exported type and `RestoreLabels` argument, so it is
  breaking under the compatibility contract.

- **Learning trajectory current-run metadata ([ADR 0114](../docs/adr/0114-configurable-learning-trigger-policy.md))** —
  `learning.Trajectory` adds `Kind`, `Counters`, and `Current`. The fields are
  additive for keyed literals but breaking for external unkeyed literals; classified
  Changed for a pre-v1 minor bump.

- **Session taxonomy and title-provenance fields on existing structs** (issues #471 and #557,
  [ADR 0217](../docs/adr/0217-session-discovery-continuation.md)) —
  `session.Session.Kind`, `session.Session.Relationship`, and
  `session.Session.TitleProvenance` and `port.SessionDiscoveryMeta.TitleProvenance` are
  additive for keyed literals but breaking for external unkeyed literals. The required
  `port.SessionStore` interface and existing `port.SessionMeta` shape remain unchanged. Classified Changed/breaking for a pre-v1
  minor bump.

- **Learned-skill linkage and durable admission on existing learning structs (issue #510)** — `learning.Candidate.Name` preserves historical title/body procedure decoding while naming new materializable skills; `learning.ProposalRecord.SkillID` links an explicitly materialized draft; `learning.SkillProvenance.Origin` represents the explicit unevidenced legacy-model import case; `SkillProvenance.ValidationDisposition` and `SkillVersion.Disposition` preserve similarity admission across exact retries/restarts; and `ProposalRepository.LinkSkillDraft` extends the existing repository interface. Struct additions are breaking for unkeyed literals and the interface method addition is breaking for external implementations, so these changes are classified Changed under the compatibility contract.

- **Completed trajectory owner attribution** (issue #509, ADR 0204) —
  `learning.Trajectory.Principal` adds the copied verified session owner used by
  host coordinators for principal-partitioned durable proposals. The field is
  additive for keyed literals but breaking for external unkeyed literals under
  the compatibility contract.

- **Proposal linkage on existing memory provenance** (issue #509) —
  `tool.MemorySource.ProposalID` extends an existing exported struct so promoted
  revisions can reconcile after a crash. Missing values remain normal for legacy
  files and drivers. The additive driver field and convergence capability preserve
  old-driver negotiation, but the Go struct field addition is Changed/breaking for
  external unkeyed literals under the compatibility contract.

- **Operator-profile fields on existing public structs** (issue #508) —
  `prompt.Config.OperatorProfile` and `agent.Deps.OperatorProfileSource` extend
  existing exported structs. Although keyed literals remain source-compatible,
  external unkeyed literals no longer compile, so these field additions are
  Changed/breaking under the engine compatibility contract (a pre-v1 minor bump),
  not merely Added. `prompt.OperatorProfileConfig` itself is a new type.

- **`Tool.Execute`, the observed/parent seams, `Engine.Run`/`ResumeApproval`,
  and `CommandRunner`/`CommandStreamer` now take `tool.Environment`** (ADR 0211,
  issue #462 phase 2) — the per-call `tool.Workspace` parameter is replaced by
  the bound `tool.Environment` on:
  - `tool.Tool.Execute(ctx, in, env tool.Environment)` (was `ws tool.Workspace`);
  - the agent-internal `observableTool`/`childCapableTool` seams
    (`ExecuteObserved`/`ExecuteWithParent`) and the loop/dispatch/delegation
    call sites;
  - `agent.Engine.Run`/`Engine.ResumeApproval` (the `ws tool.Workspace`
    parameter is now `env tool.Environment`);
  - `tool.CommandRunner.Run`/`CommandStreamer.RunStreaming` LOSE the per-call
    `workdir string` parameter — a runner is now BOUND to one namespace at
    construction, so the command's cwd always matches the workspace the tool
    executes against. `ErrNoShell` is surfaced honestly when a shell-less
    Environment's Bash is called.
  This is a breaking signature change on the exported engine/tool surface (the
  `Tool.Execute`, `CommandRunner.Run`, `CommandStreamer.RunStreaming`,
  `Engine.Run`, `Engine.ResumeApproval`, `NewBashTool`, `NewParallelTool`,
  `NewSupervisor`, and the forker/merger/option constructors), classified
  Changed per COMPATIBILITY.md; it is a parameter-type consolidation only —
  every existing behaviour and invariant (read-parallel/mutate-serial,
  envscrub/gitenv, trust gates, merge serialization, no-fs, path-escape,
  skill read roots) is preserved. The in-repo callers and tests are updated.

- **`tool.EnvironmentForker` and `tool.EnvironmentMerger` replace the removed
  `tool.WorkspaceForker`/`tool.ForkMerger`** (ADR 0211, issue #462 phase 2) —
  the two isolation seams are REPLACED (not maintained in parallel):
  `WorkspaceForker.Fork(ctx, base Workspace, ...) (child Workspace, ...)` becomes
  `EnvironmentForker.Fork(ctx, base Environment, ...) (child Environment, ...)`;
  `ForkMerger.Merge(ctx, forkRoot string, parentWS Workspace)` becomes
  `EnvironmentMerger.Merge(ctx, child, parent Environment)`. The forker now
  returns a complete child Environment whose Workspace and runner share the
  child namespace. The OLD interfaces are removed (see Removed); the new
  interfaces are Added above.

- **`tool.FileSystem.Write` removed** (ADR 0208, issue #462) — the
  unconditional-mutation seam is deleted from the `tool.FileSystem` interface.
  The agent-facing tools never used it: they go through the version-bearing
  `Workspace.CreateFile`/`ReplaceFile` pair. Concrete adapter Workspaces retain
  unconditional bootstrap writers only for test/demo setup, outside both
  `tool.FileSystem` and `tool.Workspace`; `osfs.FileSystem.Write` is removed
  entirely because it had no live callers and used a divergent resolver.
  Removing a method from an exported interface is breaking for implementors;
  under the pre-v1 policy it ships in a minor bump. See ADR 0208.

- **Schedule-origin binding is now run-context attribution**
  ([ADR 0209](../docs/adr/0209-schedule-origin-run-context.md)) — removed the
  exported `agent.OriginBinder` interface, `agent.Deps.OriginBinder` field,
  `agent.SessionOriginScheduleManager` (its constructor and all ten methods), and
  the `BindSessionOrigin` method that type carried. `Engine.Run` stamps the
  executing session id onto the run context, and `ScheduleTool.create` reads it
  there when it builds the `ScheduleSpec` — so the origin is set at the single
  site that constructs the spec, and there is no wrapper a composition root can
  forget to apply. An unbound context yields the empty origin (no delivery),
  never a borrowed one. Migrate custom engine composition by deleting the
  `Deps.OriginBinder` assignment and passing the `port.ScheduleManager` straight
  to `agent.NewScheduleTool` / `agent.NewScheduleQueryTool`; no replacement call
  is needed. Nothing exported replaces the removed surface: an origin is acquired
  by running under `Engine.Run`, and an out-of-band create is originless by
  design (ADR 0075 decision #1). These removals are breaking pre-v1 changes,
  classified Changed per COMPATIBILITY.md; the engine's exported surface NET
  SHRINKS by fourteen symbols and gains none.

- **`agent.Engine.Run` consolidated to a single request-struct entry point** (issue #461) — the three exported prompt entry points `Engine.Run(ctx, sess, ws, userText string)`, `Engine.RunContent(ctx, sess, ws, userText string, parts []session.Content)`, and `Engine.RunContentWith(ctx, sess, ws, userText string, parts []session.Content, opts RunOptions)` are REMOVED and replaced by exactly one normal entry point: `Engine.Run(ctx context.Context, sess *session.Session, ws tool.Workspace, req RunRequest) *Run`. The per-run override type `agent.RunOptions` is REMOVED; its fields (`MaxRunTokensOverride`, `ExtraTools`, `AskIDDiscriminator`) move onto the new `agent.RunRequest` alongside the prompt (`Text string`, `Parts []session.Content`). No aliases or deprecated wrappers are retained. `Engine.ResumeApproval` keeps its public signature unchanged (it is the awaiting-only run-entry seam, not a prompt entry) and internally starts with a zero `RunRequest`. This is a breaking signature change on the exported engine prompt surface (the three removed methods + the removed type), classified Changed per COMPATIBILITY.md; it is an API consolidation only — every existing behaviour and invariant is preserved. The single in-repo production caller (`internal/adapter/server.Service.StartRunContent`) and all adapter/demo/perf/test callers are updated. See issue #461.

- **`agent.Supervisor.MemberRouting`** (issue #397) — widened from `(category, model string)` to `(category, model, reason string)` so the Team tool can read back the routing reason alongside the routed category/model for the EvTeamStart roster. This is a signature change on an exported method (breaking pre-v1, classified Changed per COMPATIBILITY.md). The single in-repo caller (the Team tool roster loop) is updated; the read-back is metadata-only.

- **A text-bearing `StopError` turn now carries a terminal CAUSE** (issue #319
  review-audit follow-up) — BEHAVIOUR only; no exported signature moved and
  `engine/api/*.txt` is unaffected. `Engine.terminateComplete` gains an internal
  `errMsg` parameter (it is unexported), and the loop's `finishTurnNoTools`
  synthesises the cause for a text-bearing turn that ended on a terminal stop
  CHUNK (`max_tokens` / `refusal` / `incomplete` / `failed` → `StopError`,
  relayed by both adapters' `mapStop` on the `ChunkDone` stop, NOT as a Go
  error) via the new unexported `stopTerminalCause`. Before this, such a turn
  emitted an EMPTY `session.ResultPayload.Error`, so a delegation's
  `subagentErrorBody` rendered the child's truncated/refused text AS the failure
  — the exact #319 presentation on the `terminateComplete` path (the empty-turn
  shape already routed through `terminate` with a cause). The synthesised cause
  is harness-authored metadata (a stop label + a "TRUNCATED or refused" shape
  note), never model text, so it is gauntlet-#7 safe, and it is `StopError`-only
  (a clean limit / cancellation keeps an empty cause, honouring the
  `SubagentPayload.Cause` "empty on every other terminal" contract). Consumers
  asserting that a text-bearing `StopError` delegation result led with the
  child's last text must adjust. No new exported symbol.

- **Model-facing next-action wording on the Subagent failure terminals** (issue #319 /
  #318 review round) — BEHAVIOUR/COPY only; no exported signature moved and
  `engine/api/*.txt` is unaffected. Three strings a consumer might be matching on
  changed, all in the same direction (the harness must not assert something the next
  turn contradicts):
  1. The `StopError` resume hint no longer says "continue where it left off" — it now
     states that the CONVERSATION is preserved but the WORKSPACE is not (the failed
     child ran in a throwaway checkout, so files it wrote are gone), which is what
     `resumeStalenessNote` tells the resumed child.
  2. The PER-CALL TIME-BUDGET terminal (`timeout_ms`) now carries a next action — a
     timed-out child lands `StateCancelled` and has always been resumable, but the
     terminal named no recovery, and a `mode:"read-write"` timeout gets the single
     combined resume-or-discard decision instead of a bare partial-edits warning.
  3. The last-resort recovered-digest prefix no longer restates the stop-reason note's
     next action; it states provenance + partial-ness only (the two rendered
     back-to-back and duplicated the clause byte-for-byte).
  Consumers asserting on the old copy must adjust. No new exported symbol.

- **A FAILED delegated child is now resumable** (ADR 0200, issue #318) — a
  BEHAVIOUR change with NO exported signature change, so it is classified Changed
  (behaviour only; `engine/api/*.txt` is unaffected). The `Subagent` tool's
  `resume: <agentId>` used to hard-refuse a child persisted in
  `session.StateFailed` ("ended in a failed state and is not resumable"); it now
  recovers it through the existing exported `session.Session.Recover`, matching the
  service layer's `loadAndReopen` discipline (all three terminals recover). A
  genuinely non-resumable state — e.g. a snapshot still recorded `running` — is
  still a model-addressable tool error. Two model-facing strings changed with it: a
  `StopError` Subagent result now carries a resume hint after its `agentId:`
  trailer, and a resumed `mode:"read-write"` child is told its earlier edits SURVIVED
  (it never forked) instead of receiving the read-only fresh-checkout staleness note.
  In `agent.Supervisor`, a team member whose round left its session failed is
  likewise recovered rather than benched, so a failed LEAD still reaches its final
  synthesis. `MemberOutcome.Stopped`/`Reason` for a failed round changed too — see
  the `agent.WithMemberErrorRetries` entry under **Added**: on the default
  configuration a member whose round ends in `StopError` is now retried, so it
  finishes `Stopped == false` / `DispositionDone` / `Reason == ""` rather than
  `stopped`/`error`. Read the two entries together; the disposition a consumer sees
  is the retry entry's, not this one's. Consumers relying on a failed child being permanently
  unresumable — or matching on the old refusal copy — must adjust. There is no knob
  that restores the old refusal; the ONE lever is the precondition `resume` has always
  had — it requires a wired session store, so a consumer that must forbid resuming a
  failed child does not pass `agent.WithSubagentStore` (which disables `resume:`
  wholesale, not just for the failed state, and also stops `InspectSubagent` from
  loading persisted child transcripts). A `StopError` result built without a store
  correspondingly omits the resume hint, so the model is never told about a path that
  deployment cannot serve. (issue #318)

- **`agent.NewPlanAwareScheduleTool(base tool.Tool, mgr port.ScheduleManager)`**
  — the AC4.3 plan-mode gate now also denies the `fire` of a MUTATING schedule
  (the plan-mode hard-deny on mutations: a `mutating: true` schedule's fire
  writes the workspace). It reads the schedule's create-time `Mutating` posture
  from the injected `mgr` (the fire verb carries no mutating flag of its own),
  so the constructor takes the manager as a second argument. The mutating
  `ScheduleTool` now carries ONLY the mutating verbs (list/inspect moved to
  `agent.ScheduleQueryTool`); its schema and unknown-verb message reflect that.
  Classified Changed per COMPATIBILITY.md (a widened constructor signature +
  narrowed verb surface; pre-v1 a minor bump). (schedule-tool plan, task 06
  repair)

### Removed

- **BREAKING:** `tool.WorkspaceForker` and `tool.ForkMerger` — removed (ADR 0211,
  issue #462 phase 2). The two isolation seams are REPLACED by
  `tool.EnvironmentForker` and `tool.EnvironmentMerger` (see Added); the old
  interfaces are not maintained in parallel.
  `WorkspaceForker.Fork(ctx, base Workspace, ...) (child Workspace, ...)` becomes
  `EnvironmentForker.Fork(ctx, base Environment, ...) (child Environment, ...)`;
  `ForkMerger.Merge(ctx, forkRoot string, parentWS Workspace)` becomes
  `EnvironmentMerger.Merge(ctx, child, parent Environment)`. Removing exported
  interfaces is breaking for implementors; under the pre-v1 policy it ships in a
  minor bump. See ADR 0211.

### Added

- **`agent.NewPlanAwareScheduleTool`** (ADR 0073, the AC4.3 plan-mode gate) —
  wraps the MUTATING Schedule tool for a PLAN-MODE session's catalog. The default
  mutating tool reports `ReadOnly()==false`, so the plan-mode catalog projection
  (`engine/tool/catalog.go` `Available(ModePlan)`) would hide the WHOLE tool —
  including the read-leaning create plan mode must keep (a schedule CREATE does
  not itself mutate the workspace; the FIRE's posture is pinned at create-time
  by the Mutating/Mode invariant). The plan-aware variant reports
  `ReadOnly()==true` (so the projection advertises it) and hard-denies the
  mutating shapes (a `mutating: true` create; the fire of a mutating schedule)
  per call with the plan-mode deny reason BEFORE the base tool runs; the
  read-leaning create (`mutating:false`) and the fire of a read-leaning
  schedule drive through unchanged. Composition registers it only for a
  plan-mode session's catalog; every non-plan engine keeps the DEFAULT tool
  (the read/mutate serialization contract is unchanged). Classified Added per
  COMPATIBILITY.md (a new exported constructor is a minor bump). (schedule-tool
  plan, task 05; the fire-gate extension + manager arg landed in task 06 repair)

- **`port.ScheduleManager` + `port.ErrFireNowOverlap` + `agent.ScheduleTool`
  (ADR 0073, the model-facing Schedule tool)** — a new consumer-local
  `port.ScheduleManager` interface (the create/inspect/list/update/pause/
  resume/delete/fire/list-fires verbs the Schedule tools need, satisfied by
  composition with the server Service's schedule methods — the SAME validated
  create-seam the REST/gRPC handlers ride, never a second path), the
  port-level `ErrFireNowOverlap` sentinel a `FireNow` overlap returns (so a
  layer that may not import the scheduler adapter — the tool — matches the
  singleton rejection via `errors.Is`), and the MUTATING `agent.ScheduleTool`
  itself (`agent.NewScheduleTool(port.ScheduleManager)`, catalog name
  `agent.ScheduleToolName` = `"Schedule"`). The tool carries the mutating verbs
  (create/pause/resume/delete/fire) and reports `ReadOnly()==false` (every
  mutating call serialises so it never overlaps a sibling read); the read-only
  list/inspect verbs live on the `agent.ScheduleQueryTool` (task 06 repair).
  Classified Added per COMPATIBILITY.md (a new interface + sentinel + tool are
  a minor bump). (schedule-tool plan, task 01)

- **`session.StripProviderState`** — provider-neutral history for cross-provider
  model-switch carryover (minor): a pure function returning a copy of a message
  slice with every provider-private replay blob cleared (`Message.Reasoning`,
  `Message.ProviderPhase`, each `ToolCall.ItemID`), preserving the
  provider-neutral fields (`Role`, `Text`, `ToolCall` ID/Name/Args,
  `ToolResult` incl. its `Parts`, `Message.Parts`) verbatim. Both provider
  adapters treat an empty blob as "no blob" and omit it on the wire, so a
  stripped history replays safely to ANY provider — the new request's
  reasoning config is derived from the NEW model. Neither the input slice nor
  any shared `ToolCalls` backing array is mutated; nil/empty input passes
  through. Classified Added per COMPATIBILITY.md (a new exported function is a
  minor bump).

- **`session.PendingAsk.PlanOriginated` + `session.AskOrigin` enum +
  `session.PendingAsk.Origin()`** (issue #206, Wave 1) — a new serialized `bool`
  field (`json:"plan_originated,omitempty"`, sibling of `HookOriginated`) marking an
  ask that arose from the plan-approval gate (an operator was asked to approve a
  presented plan), NOT from the permission policy or a hook. It is CROSS-PROCESS
  LOAD-BEARING: the awaiting-resume path (`Engine.ResumeApproval` →
  `resolvePendingCall`) runs in a FRESH process and keys the plan-flip branch on it
  (an Allow flips the session out of plan mode and drives the turn through the
  completed path with `StopPlanApproved`). The two serialized bools
  (`HookOriginated`, `PlanOriginated`) remain the on-disk contract; the new
  `AskOrigin` enum (`AskOriginNone`/`AskOriginHook`/`AskOriginPlan`) and the
  `Origin()` read accessor are a convenience derivation over those bools, NOT a
  stored field (Hook takes precedence if both were incorrectly set).
  `ConfiguredAsk`/`FlooredConfiguredAllow` stay untouched (run-scoped, never
  serialized). Classified Added per COMPATIBILITY.md (new struct field + enum type +
  consts + method are minor bumps; the serialized two-bool contract is additive — a
  pre-#206 snapshot deserializes to `PlanOriginated=false`, `omitempty` keeps the
  key absent when false). (#206)

- **`session.StopPlanApproved`** (issue #206, Wave 1) — a new `StopReason = "plan_approved"`
  const, the CLEAN non-error terminal emitted when an operator approves a presented
  plan. Like `StopNoProgress` / `StopBudget` / `StopStructuredOutput` it is routed
  through the completed path (session ends COMPLETED, Reopen-recoverable), NOT
  `StopError`. It maps to the proto stop string verbatim (no proto enum; the wire
  stop field is a string passthrough, exactly like its clean-terminal siblings).
  Classified Added per COMPATIBILITY.md (a new exported const is a minor bump). (#206)

- **`agent.NewPresentPlanTool() tool.Tool`** (issue #206, Wave 1) — the
  plan-approval gate's signalling affordance. In plan mode, once the model has
  presented a complete plan in its preceding assistant text, it calls `PresentPlan`
  to hand control to the operator. The tool is read-only / signaling-only
  (`ReadOnly() == true`, dispatches read-parallel); `Execute` is VESTIGIAL — it
  returns `session.NewToolResult(call.ID, "PresentPlan: awaiting operator approval.")`
  and exists only for honesty on a misroute, since the dispatcher (Wave 2) intercepts
  a `PresentPlan` call by name in plan mode before execution. No marker interface is
  introduced (single implementation; the dispatcher name-checks `c.Name ==
  "PresentPlan"`). Classified Added per COMPATIBILITY.md (a new exported constructor
  is a minor bump; the `presentPlanTool` struct is unexported). (#206)

- **`agent.PlanApprovedProceedText`** (issue #206, Wave 2) — an exported `const string`
  ("Plan approved by operator. Proceed with execution.") carrying the harness-framed
  proceed message the composition/service layer (Wave 4's ApprovePlan seam) injects as
  ordinary recorded history when an operator approves a presented plan, signalling the
  model to begin execution. It is event-silent (a recorded user message, NOT a
  diagnostics line — the loop's "exactly THREE lines" invariant holds). Exported so the
  service layer references the exact text without re-stringing it; the text is a stable
  contract the model reads as the proceed signal. Classified Added per COMPATIBILITY.md
  (a new exported const is a minor bump). Wave 2 also lands the agent-loop
  plan-approval seam itself (the run-scoped `Run.planApprovedTarget` field, the
  `surfacePlanAsk` dispatcher sibling of `askHookApproval`, the `runReadBatch`/`runOne`
  name+mode interception, the `runLoop` `StopPlanApproved` termination, the
  `terminateComplete` mode flip, and the `resolvePendingCall` `PlanOriginated`
  cross-process resume branch) — all unexported, so no further API surface changes. (#206)

- **`tool.PlanOnly` interface** (issue #206, Wave 3) — a new OPTIONAL capability
  interface a `Tool` MAY implement to declare itself a plan-mode signalling tool:
  registered everywhere (so the shared and per-session catalog name-sets stay equal —
  guarded by `TestPerSessionCatalogMatchesSharedCatalog`) but advertised/callable
  ONLY in `ModePlan`. The catalog's mode projection (`Catalog.Available` / `Specs` /
  `AdvertisedSpecs`) EXCLUDES a `PlanOnly` tool from every non-plan mode, so it is
  never offered to the model in default/acceptEdits. This is the projection gate for
  `PresentPlan` (Wave 1): the dispatcher's name+mode check (`sess.Mode == ModePlan &&
  c.Name == "PresentPlan"`) is defense-in-depth ON TOP of this gate, not the sole
  gate. A tool that does NOT implement `PlanOnly` is advertised in every mode it is
  otherwise eligible for, so the default catalog view is unchanged for every
  non-plan-signalling tool. Classified Added per COMPATIBILITY.md (a new exported
  interface with a marker method is a minor bump; additive — existing tools are
  unaffected). (#206)

- **`agent.Deps.PlanModeAutoApprove`** (issue #206, Wave 6a) — a new `bool` field
  on `Deps` that, when true, loosens the `surfacePlanAsk` headless guard so a
  plan-approval ask (PresentPlan) is SURFACED (EvPermissionAsk emitted, run parks)
  even when the engine is headless (`Interactive=false`). This enables the
  composition layer's auto-approve observer (`Service.MaybeAutoApprovePlan`) to
  resolve the parked ask without a human operator. It is an OPT-IN, OPERATOR-TIER-
  ONLY, DEFAULT-OFF flag: when false (the default) the existing headless auto-deny
  is byte-identical. The engine NEVER auto-approves on its own — it only surfaces
  the ask; the composition Service layer delivers the verdict. Classified Added per
  COMPATIBILITY.md (a new exported struct field with a false zero-value is a minor
  bump; additive — existing code is unaffected). (#206)

- **`session.StopPlanIterate`** (issue #206) — a new `StopReason = "plan_iterate"`
  const, the CLEAN non-error terminal emitted when an operator chooses to iterate
  on a presented plan (a deny verdict). It is the iterate sibling of
  `StopPlanApproved`: a deny of a plan ask TERMINATES the plan run CLEANLY instead
  of continuing in-turn (the old behaviour kept the model iterating with NO
  operator input), so the run ends and the operator's next typed prompt drives the
  revision. Like `StopPlanApproved` it is routed through the completed path (session
  ends COMPLETED, Reopen-recoverable), NOT `StopError`, and the session stays
  `ModePlan` (no mode flip). It maps to the proto stop string verbatim (no proto
  enum; the wire stop field is a string passthrough, exactly like `StopPlanApproved`).
  Classified Added per COMPATIBILITY.md (a new exported const is a minor bump). (#206)

### Changed

- **`agent.RunModelRouter` + `agent.Deps.SubagentModelRouter` gain a miss-reason
  return** (issue #287) — both now return an extra `missReason string` before the
  terminal `ok bool`: `RunModelRouter` returns `(category string, usage
  session.Usage, missReason string, ok bool)` and the `Deps.SubagentModelRouter`
  closure type is `func(ctx, taskPrompt string) (category, model string, usage
  session.Usage, missReason string, ok bool)`. `missReason` is `""` on a hit and one
  of the new `RouterMiss*` constants on a miss, so the dispatch chokepoint can log a
  per-miss INFO naming WHY a plain delegation fell through to the inherited default
  model (metadata only — never the task prompt or classifier output; gauntlet #7).
  Classified Changed (breaking; pre-v1 minor bump) per COMPATIBILITY.md — a widened
  return signature. A composition consumer adds one return value at the
  `buildModelRouterTask` closure and the `RunModelRouter` call site. (#287)

### Added

- **`agent.WithRoutableAgents(names []string) SubagentOption`** — injects the
  composition-computed SET of agent-def names eligible for the OPT-IN model router
  (issue #286): a def that expressed NO model intent (absent `model:`) is CLASSIFIED and
  its scoped engine rebuilt on the routed model (via the agent+model factory), while a def
  that pinned ANY model (incl. explicit `inherit`) is honoured verbatim. nil/empty (the
  default) means no def routes — byte-identical to pre-#286. Layering-clean (def NAME
  strings only). Classified Added per COMPATIBILITY.md (a new exported option constructor is
  a minor bump; the `SubagentTool` struct is unexported). (#286)

- **`agent.WithWritableEngineFactory(f func(model string) (*Engine, bool))
  SubagentOption`** — injects the composition factory that mints a WRITABLE explorer
  child engine on a per-call OVERRIDE model for a mode:"read-write" Subagent call with
  no `agent` (issue #285), so a writable subagent honours the per-call `model` and the
  OPT-IN router pick instead of always running on its default model. nil (the default,
  and always on the no-FS path) leaves the writable-explorer-per-model path unwired (a
  read-write+`model` call then errors from validateMode — never a silent inherit).
  Classified Added per COMPATIBILITY.md (a new exported option constructor is a minor
  bump; the `SubagentTool` struct is unexported). (#285)

- **`agent.RouterMiss*` constants** — `RouterMissDegenerateInput`,
  `RouterMissClassifierError`, `RouterMissCancelled`, `RouterMissBadVerdict`, and
  `RouterMissUnknownCategory`: the string reason a model-router classification did
  not yield a routed model, returned by `RunModelRouter`/`Deps.SubagentModelRouter`
  (see Changed above). Classified Added per COMPATIBILITY.md (new exported
  identifiers are a minor bump). (#287)

### Fixed

- **`port.RouteToolResultParts` now drops empty-render text-summarised blocks**
  (behavioral fix, no API change). A text-summarised block (`BlockText`,
  `BlockStructuredContent`, `BlockEmbeddedResource`, `BlockResourceLink`) whose
  `session.ToolBlockText(b)` renders to the empty string is now dropped from the
  projection (image/audio gating is unchanged). If every block drops, the existing
  `len(out)==0 → nil` return routes the caller to the single-string `Content`
  fallback. WHY: strict providers reject an empty text content block on the wire —
  Moonshot via OpenRouter (POST /responses) 400s the whole request with "Invalid
  request: text content is empty", and the Anthropic Messages API rejects "text
  content blocks must be non-empty". Because the LLM adapters replay full history
  statelessly, a single empty-text tool-result block (e.g. an MCP fetch past the
  end of a document returning one empty text block) poisons every subsequent
  request and permanently bricks the session. The fix is request-time
  (serialization), never a history rewrite, so an already-poisoned persisted
  session heals on replay. No exported signature change, so `engine/api/*.txt` is
  unchanged. Classified as a behavioral bug fix per COMPATIBILITY.md (no
  guarded-surface change). CALLER CONTRACT: when the projection returns nil and a
  caller degrades to `tr.Content`, and that string is itself empty, the caller
  must substitute a non-empty deterministic placeholder — an empty string on the
  wire reproduces the strict-provider rejection this fix prevents. The provider
  adapters (`internal/adapter/openai`, `internal/adapter/anthropic`) carry matching
  belt-and-suspenders guards that substitute a deterministic
  `"(tool returned no output)"` placeholder for an empty single-string tool
  output.

## [0.4.0] - 2026-06-30

### Added

- **`session.Usage.ReasoningTokens`** — a new `int` field (after
  `CacheWriteTokens`) carrying the provider's reasoning-token spend for a model
  call. OpenAI surfaces `output_tokens_details.reasoning_tokens`; Anthropic
  surfaces `output_tokens_details.thinking_tokens`. It is a SUBSET of
  `OutputTokens` (providers bill reasoning as part of the inclusive output
  total), so `TotalTokens()` is UNCHANGED (`InputTokens + OutputTokens`) — the
  budget brake is not affected, and the field is additive observability only.
  Classified Added per COMPATIBILITY.md (a new struct field is a minor bump).
  (#213)

## [0.5.0] - 2026-07-16

### Added

- **`port.ScheduleSpec.OneShotRetry` + `OneShotMaxRetries` +
  `port.ScheduleState.OneShotRetryCount` + `port.ScheduleOneShotReArmer`** —
  an OPT-IN at-least-once retry for one-shot schedules that cannot tolerate
  the at-most-once crash-loss trade-off (ADR 0059 consequence). `OneShotRetry`
  (default false) enables the retry; `OneShotMaxRetries` (default 0 = off;
  the create-seam applies 3 when `OneShotRetry=true` and the field is 0)
  bounds the retry count; `OneShotRetryCount` is the durable counter. The
  optional `ScheduleOneShotReArmer` interface (type-asserted on the store,
  the same pattern as `PrunableStore`) provides the atomic re-arm
  (`ReArmOneShot` re-enables + advances `NextFireAt` + increments the count).
  Rejected on cron triggers (a cron self-heals via misfire). Classified Added
  per COMPATIBILITY.md (new struct fields + a new optional interface are a
  minor bump; `ScheduleStore` itself is unchanged). (#236)

- **`port.ScheduleSpec.CarryContext`** — an OPT-IN carried-context toggle
  (default false = fresh per fire, unchanged). When true, the fire path loads
  the prior fire's session and renders its conversation as a FENCED untrusted
  preamble prepended to the fire's prompt (via `agent.FenceUntrusted` +
  `NeutraliseFraming`), NOT as seeded history — carried context is untrusted
  (a prior fire may have been prompt-injected) and must not become live
  instructions. A re-armed one-shot does NOT carry context on the retry.
  Classified Added per COMPATIBILITY.md (a new struct field is a minor bump).
  (#236)

- **`port.MetaLister` + `port.SessionMeta`** — a new OPTIONAL cheap-listing seam
  a `SessionStore` adapter may additionally implement, discovered by type
  assertion (the same pattern as `PrunableStore`). `MetaList(ctx) ([]SessionMeta, error)`
  returns every stored session's picker metadata (id/state/turns/model id/title/
  created_at/last-modified) by reading ONLY the last snapshot line of each and
  decoding into a small struct that SKIPS the `messages` array — so listing N
  sessions is O(N × last-line-read) rather than O(N × filesize) for large
  histories. `Service.ListSessions` prefers `MetaLister` when the store
  implements it (jsonlstore does, via a tail-read `readLastLine` helper) and
  falls back to the Load-per-row path otherwise (memstore/redisstore/grpcdriver).
  `SessionMeta` is a port-owned struct (the server adapter references the shape
  without importing any concrete store). Classified Added per COMPATIBILITY.md
  (a new interface + struct are a minor bump; `SessionStore` itself is unchanged).
  (#245)

- **`session.Session.Title`** — a new `string` field (after `ReasoningEffort`)
  carrying a human-readable session label seeded ONCE from the first genuine
  user prompt, clamped to 120 runes (`maxTitleRunes`). It is set via the new
  `session.Session.SetTitle(text string)` (set-once: only the first non-empty
  prompt sticks; subsequent prompts do not overwrite), called by the loop from
  `recordPrompt` (the genuine prompt site) only — never the synthetic
  continuation/nudge site — so a no-progress nudge never seeds or overwrites
  the title. It is persisted in the snapshot (an inert stored label, like
  `Profile`; `omitempty` keeps a pre-Title snapshot decoding to `""` — additive,
  no format-tag bump) and projected on the wire (`Session.title`,
  `SessionSummary.title`). Two read-time consumers derive it lazily without
  write-on-read when empty: `ListSessions`/`GetSession` (the server lazy
  fallback, `deriveTitle`) and the event-sourced `eventsource.Fold`. The new
  `session.IsGenuineUserPrompt(m Message) bool`,
  `session.IsSynthesisedSummary(text string) bool`, and
  `session.ClampTitle(s string) string` (plus the exported markers
  `session.CompactionSummaryMarker` / `session.Tier4SummaryMarker`, promoted
  from the unexported `engine/agent` constants) back those consumers — the
  domain leaf owns the genuine-vs-synthesised distinction it needs without
  importing `engine/agent`/`engine/prompt`. `IsGenuineUserPrompt` deliberately
  does NOT check `prompt.IsInjectedTurn0Fragment` (ADR 0043 makes turn-0
  fragments ephemeral, so they never appear in persisted history); the loop's
  own `isGenuineUserTurn` keeps that arm as defense-in-depth and delegates its
  synthesised-summary arm to the new domain export. Classified Added per
  COMPATIBILITY.md (new struct field + methods + consts are a minor bump). (#247)

- **scheduled-tasks Phase 2a: `port.ScheduleStore.ClaimNow`.** A new
  `ClaimNow(ctx, name, now, nextFire) (Schedule, error)` method — the FireNow
  primitive. It performs the SAME atomic advance as `Claim` (LastFireAt=now,
  NextFireAt=nextFire, FireCount++, LastFireSessionID=PendingFireSessionID,
  disable on zero nextFire) but does NOT enforce the `NextFireAt <= now`
  due-check — it claims the slot regardless of whether it is due (a manual
  trigger bypasses the cadence but still claims atomically for at-most-once).
  The `Enabled` + `MaxFires` checks STILL apply (a disabled or exhausted
  schedule cannot be force-fired). The at-most-once fence (no due-check) is
  `LastFireAt == now`: a second `ClaimNow` at the same `now` is rejected (the
  advance already happened), and a later `ClaimNow` at a new `now` succeeds
  (crash-recoverable — a hard crash between ClaimNow and RecordFire does not
  wedge the schedule, unlike a pending-sentinel fence). The tick loop's
  `fireOne` KEEPS using `Claim` (the due-check is correct for the poll loop);
  only `FireNow` uses `ClaimNow`. Classified Added per COMPATIBILITY.md (a new
  interface method is a minor bump). (#189)

- **scheduled-tasks Phase 2a: `port.ScheduleStore.SetEnabled`.** A new
  `SetEnabled(ctx, name, enabled) error` method — the pause/resume primitive.
  It atomically sets the schedule's `Enabled` flag WITHOUT touching any other
  State field, which `Save` CANNOT do: `Save` preserves the existing State half
  on a Spec overwrite (so a Load→set Enabled→Save is inert, the Enabled flip
  does not persist). `PauseSchedule` sets `Enabled=false`; `ResumeSchedule`
  sets `Enabled=true`. The not-found case wraps `ErrScheduleNotFound`.
  Classified Added per COMPATIBILITY.md (a new interface method is a minor
  bump). (#232)

- **scheduled-tasks Phase 2a: `port.ScheduleStore.ListFires`.** A new
  `ListFires(ctx, scheduleName) ([]ScheduleFire, error)` method — the list
  companion to `LoadFire`. It returns the fire records for a schedule, in no
  guaranteed order; the not-found case for the SCHEDULE wraps
  `ErrScheduleNotFound`, and an empty fire list for an existing schedule is a
  successful empty slice (not an error). Classified Added per COMPATIBILITY.md
  (a new interface method is a minor bump). (#232)

- **scheduled-tasks Phase 2a: `session.EvScheduleFired` / `EvScheduleSkipped` /
  `EvScheduleFailed` events + `session.SchedulePayload`.** Three new
  `EventType` string consts (`schedule.fired` / `schedule.skipped` /
  `schedule.failed`) and a new `SchedulePayload` value object
  (`ScheduleName`/`FireID`/`SessionID`/`Kind`/`Stop`/`Err`), plus a new
  `Event.Schedule *SchedulePayload` field. They are the CLIENT-VISIBLE
  scheduler-lifecycle projection (unlike the log-only `EvApproval` /
  `EvCompactionArchive` / `EvUserPrompt`), emitted from COMPOSITION (the
  scheduler) at fire time — NOT the agent loop (`engine/agent` never imports
  `port.ScheduleStore`). `Kind`/`Stop`/`Err` are STRING passthroughs (the
  `EvNoProgress`/`StopBudget` discipline — no proto enum). Classified Added per
  COMPATIBILITY.md (new exported consts + a new struct field are minor bumps).
  (#232)

- **`port.ScheduleStore` + value objects** (`Schedule`, `ScheduleSpec`, `TriggerSpec`,
  `ScheduleState`, `ScheduleFire`, `ScheduleProviderSelector`, `ErrScheduleNotFound`,
  `ErrScheduleUnsupported`, `SchedulerLeaderLeaseID`, `TriggerKind`/`TriggerCron`/
  `TriggerOneShot`/`TriggerNone`, `MisfirePolicy`/`MisfireFireOnceNow`/`MisfireSkip`,
  `PendingFireSessionID`).
  A new OPTIONAL durable schedule registry port — a peer of `port.SessionLease` /
  `port.EventLog` — defining the durable registry for the scheduled-tasks feature
  (issue #189, Phase 1a: contract + value objects, no implementation yet). The port
  carries the FULL at-most-once claim-before-fire contract in its doc-comments:
  `Claim` is the atomic advance (NextFireAt + LastFireAt + FireCount + a
  sentinel-pending LastFireSessionID) that gives exactly-once firing across
  replicas; the store is parser-free (the caller computes the next cron fire);
  `engine/agent` NEVER imports this port — the tick loop, cron parsing, misfire
  policy, and leader-lease acquisition all live in composition. `TriggerSpec`
  carries an XOR (Cron | OneShot) with a `Validate()` + `Kind()`; `MisfirePolicy`
  is the fire-once-now (default) / skip enum; `SchedulerLeaderLeaseID` is the
  well-known leader-lease id; `PendingFireSessionID` is the single-source sentinel
  string a `Claim` stamps onto `LastFireSessionID` and the scheduler reads back.
  No implementation ships yet — adapters are a later
  phase. Classified Added per COMPATIBILITY.md (new exported identifiers in
  `engine/port`). (#189)
- **`port.ScheduleSpec.Trigger` field added** (`port.TriggerSpec`). Phase 1a
  landed `TriggerSpec` (the Cron XOR OneShot sum type) but did not wire it onto
  `ScheduleSpec`; Phase 1b's conformance suite + reference adapter need the
  field to save/load schedules with a trigger. The field is the sole carrier of
  the firing trigger on a spec (there is no top-level Cron/OneShot). Classified
  Added per COMPATIBILITY.md (a new struct field in a pre-v1 port value object
  is a minor addition; the zero `TriggerSpec` is `TriggerNone`, which the
  create-seam's `Validate` rejects fail-closed — no schedule can be saved with
  an unset trigger). (#189)

- **`session.ToolResult.Parts`** — a new `[]Content` field (additive; zero-value
  = string-only, byte-identical to the pre-#223 shape) carrying typed tool-result
  blocks (text/image/audio/resource-link/embedded-resource/structured-content)
  alongside the legacy model-facing `Content` string. The 2-arg
  `NewToolResult`/`NewToolError` constructors are preserved (~200 call sites); the
  new path uses `NewToolResultWithParts`. Consumers prefer `Parts` when non-empty,
  falling back to `Content`. Typed content rides `EvToolResult.ToolResult`, so
  `engine/adapter/eventsource` (`Fold`) reconstructs it from the durable log with
  no relay sidecar. Classified Added per COMPATIBILITY.md (a new struct field is a
  minor bump). (#223, [ADR 0078](../docs/adr/0078-mcp-typed-tool-results.md))

- **`session.Content` block-kind generalization** — the existing `Content` gains a
  `BlockKind` discriminator plus block variants (`BlockText`/`BlockImage`/
  `BlockAudio`/`BlockResourceLink`/`BlockEmbeddedResource`/`BlockStructuredContent`)
  and validating constructors (`NewContent`/`ValidateMediaParts`, the SINGLE choke
  point the ACP adapter already uses for prompt media — no second validation path).
  A legacy media part (`BlockKind == ""`, the user-message media shape) is distinct
  from a tool-result block. Classified Added per COMPATIBILITY.md (additive fields
  + consts + constructors). (#223,
  [ADR 0078](../docs/adr/0078-mcp-typed-tool-results.md))

- **`port.RouteToolResultParts`** — a pure composition-driven projection that
  returns the capability-gated subset of a recorded `session.ToolResult.Parts`
  for the model-facing request: image blocks survive iff `caps.Image`, audio
  blocks iff `caps.Audio`, and text / resource-link / embedded-resource /
  structured-content blocks always survive. It lives in `engine/port` so the
  provider adapters (T7) can call it from their `RoleTool` case without importing
  composition, and so composition can pass the SINGLE computed
  `port.ProviderCapabilities` intersection (catalog ∩ adapter). It is a
  READ-ONLY PROJECTION: it builds a fresh slice and never mutates the recorded
  `*session.ToolResult`, preserving the recorded-history == client-stream ==
  model-view guarantee. `Content.Audience` (untrusted server self-attestation,
  CWE-345) is advisory display routing ONLY and is NOT consulted — a
  `["user"]`-audience block still passes to the model. Nil/empty `Parts` (or a
  projection that drops every block) returns nil so the caller degrades to the
  recorded model-facing `Content` string. Classified Added per COMPATIBILITY.md
  (a new exported function is a minor bump). (#223,
  [ADR 0078](../docs/adr/0078-mcp-typed-tool-results.md))

- **`FetchMcpResource` tool** (registered in `internal/adapter/tools`, NOT an
  `engine/` exported API — recorded here for completeness) — the model-facing
  affordance to fetch the contents of an `https://` `resource_link` URI an MCP
  tool result surfaced as a typed block. Only `https://` is client-fetched,
  re-validated for SSRF on every redirect via `session.ValidateMediaURL` (no
  internal/private/metadata IPs, no cross-origin credentials); non-`https` schemes
  stay server-readonly via `ReadMcpResource`. Output is capped at
  `toolkit.MaxOutputBytes`; binary content is summarized. The model sees the
  `resource_link` REFERENCE, never auto-fetched raw bytes. No `engine/` exported
  surface change. (#223,
  [ADR 0078](../docs/adr/0078-mcp-typed-tool-results.md))

- **Wire: `ContentBlock` proto message + `ToolResult.blocks`/`structured_content`
  fields.** The gRPC `ToolResult` proto mirrors the additive domain `Parts` field
  as `blocks` (`repeated ContentBlock`) plus a `structured_content` scalar; the
  `ContentBlock` message carries the block-kind discriminator and variant fields.
  Additive — legacy clients/sessions round-trip with empty `blocks`. (This is a
  `contracts/gen` wire change, not an `engine/` exported-API change; recorded
  here as the wire half of #223.) (#223,
  [ADR 0078](../docs/adr/0078-mcp-typed-tool-results.md))

- **`session.ValidateResolvedIP`** — a new exported func
  (`func ValidateResolvedIP(ip net.IP) error`) re-exporting the dial-layer SSRF
  IP predicate (`isGlobalUnicast`, the same one `ValidateMediaURL` uses for
  literal-IP hosts) as the single screening definition shared between the
  URL-string path and the dial-IP path. It returns nil for a routable public IP
  and a non-nil error naming the rejection for internal IPs (loopback/private/
  link-local/CGNAT/metadata/unspecified/multicast). It is the dial-layer backstop
  a harness fetch tool (`FetchMcpResource`) installs as its `http.Transport`'s
  `DialContext` to close the DNS-rebinding window `ValidateMediaURL`'s hostname
  check cannot (an attacker-controlled resolver answers the hostname check with a
  public IP, then returns 169.254.169.254 at dial time). Classified Added per
  COMPATIBILITY.md (a new exported function is a minor bump). (#223)

- **`session.ToolBlockText`** — a new exported func
  (`func ToolBlockText(b Content) string`) extracted from the byte-identical
  `toolBlockText` helper duplicated in `internal/adapter/openai/request.go` and
  `internal/adapter/anthropic/request.go`. It renders a non-image tool-result
  block to its model-facing text form (resource-link URI+name/title, an
  embedded-resource pointer when no inline `Text`, else `Text` verbatim). Both
  adapters now call the shared domain projection instead of carrying their own
  copy, removing a drift risk. Classified Added per COMPATIBILITY.md (a new
  exported function is a minor bump). (PR #226 review)

### Notes

- **`robfig/cron/v3` added to the engine dep closure (parser-only).** The
  scheduled-tasks feature (issue #189, Phase 1c) needs a cron → next-fire
  helper at the composition layer: `TriggerSpec.Cron` (Phase 1a) is the raw
  expression the durable `ScheduleStore` stores verbatim and never interprets,
  and the caller computes the next fire to hand to `Claim`. Per decision #6 the
  durable store is ground truth and the in-memory timer is a derived lookahead,
  so mecatl uses `robfig/cron/v3`'s PARSER only (`cron.ParseStandard` +
  `Schedule.Next`) — NOT the `cron.New()` daemon. The dependency is a single
  module with a zero-dependency `go.mod`, mirroring how `doublestar` and
  `x/sync` already travel with the importable core (ADR 0036); the
  `task test:engine-standalone` hygiene proof confirms the standalone closure
  stays self-contained. The helper itself (`engine/adapter/cronparse.NextFire`)
  lives under `engine/adapter/*`, which COMPATIBILITY.md EXCLUDES from the
  stability surface (like `memlease.New` / `eventsource.Fold`), so it is NOT in
  `engine/api/*.txt` and is NOT an api-compat-gated addition. No new exported
  core symbol; no public-API change. (#189)

## [0.3.0] - 2026-06-30

### Added

- **Guardrail approve-once seam (ADR 0062).** Three additive surfaces let a
  PreToolUse hook BLOCK be refined into an interactive approval ask (reusing the
  existing permission-ask machinery) instead of a permanent dead-end:
  - **`governance.HookOutcome.AskApproval`** — a new `bool` field (after
    `Mutated`). Meaningful only on a PreToolUse outcome with `Block==true`: an
    interactive engine (`Deps.Interactive`) surfaces the block as a permission ask
    (PauseForApproval → StateAwaiting → EvPermissionAsk → Approve); a headless
    engine IGNORES it and the block stands (byte-identical fail-safe).
  - **`session.PendingAsk.HookOriginated`** — a new `bool` field
    (`json:"hook_originated,omitempty"`, SERIALIZED). Marks an ask that arose from
    a hook block; the cross-process awaiting-resume path keys on it to execute the
    approved call WITHOUT re-running the PreToolUse hook (which would re-block).
  - **`port.HookApprovalLearner`** — a new OPTIONAL interface
    (`LearnHookApproval(ctx, governance.HookEvent)`). The engine type-asserts it on
    `Deps.Hooks` and calls it ONLY on a `VerdictAllowAlways` verdict for a
    hook-originated ask, so a consumer (the guardrails adapter) can arm a
    session-scoped "Allow & don't ask again" waiver. No method was added to
    `HookRunner` (that would be breaking).

  All three are classified Added per COMPATIBILITY.md (a new struct field is a
  minor bump; a new optional interface is a minor bump). The engine stays generic —
  the new surfaces carry NO guardrail vocabulary. See
  [ADR 0062](../docs/adr/0062-guardrails-approve-once.md). (guardrail-approve-once)

## [0.2.0] - 2026-06-25

### Added

- **`session.Session.ReasoningEffort`.** A new write-once opaque creation label
  on the `Session` aggregate (a neutral reasoning-effort token, `""` = unset),
  sitting next to `Profile`/`ProviderID`/`ModelID` and carrying the same
  inert-label posture (the domain stores it but never interprets it — the neutral
  vocabulary, normalisation, per-provider clamp, and adapter re-mint all live in
  composition). It is round-tripped by the `sessnap` snapshot and the
  `eventsource` fold so a restarted process re-mints the same-effort per-session
  engine. Classified Added per COMPATIBILITY.md (a new exported struct field).
  See [ADR 0055](../docs/adr/0055-reasoning-effort.md). (reasoning-effort)

- **`session.HookAdvisory`.** A new `HookDecision` value (`"advisory"`) for an
  `EvHook` carrying an advisory guardrail finding — client-visible (rendered as
  a warning notice), model-invisible (the tool result is byte-unchanged). The
  advisory arm of `modelhook.enforce` now returns a `HookOutcome{Message:...}`
  (instead of an empty outcome), and dispatch recognises the
  `Message!="" && !Block && len(Mutated)==0` shape as an advisory outcome,
  emitting an `EvHook` with `HookAdvisory`. Classified Added per
  COMPATIBILITY.md (a new exported const + a new wire `HookDecision` enum
  value). (#170)

- **`agent.WithAgentModelEngineFactory`.** A new `SubagentOption` injecting a
  composition-supplied factory `func(agentName, model string) (*Engine, bool)`
  that rebuilds a named specialist's scoped engine on a per-call override model,
  so a Subagent call may now set BOTH `agent` and `model` (previously rejected).
  The override model runs on the def's resolved provider; the specialist's
  catalog/prompt/skills/hooks/memory are preserved (NOT the generic explorer
  set); the provider-closing Deps are re-derived for the override model via the
  contamination-safe per-provider path. A def with inline MCP servers is
  declined on the agent+model path (v1 scope limit); reference-only MCP is
  supported. Classified Added per COMPATIBILITY.md (a new exported Option).
- **`governance.BashCommandFromArgs`.** A new exported helper
  `func(args json.RawMessage) (string, bool)` extracting the Bash tool-call command
  string from its args JSON (the `command`/`cmd` field pair), with fail-safe
  semantics: a parse error or a missing/whitespace-only command returns `("", false)`
  so a caller gating a safety decision INSPECTS rather than skips. It consolidates
  three prior private copies (`governance.bashCommand`, `agent.bashCmdFromArgs`,
  `modelhook.bashCmdFromArgs`) into the single source of truth for the Bash args
  schema, so a schema change lands in one place. The two agent/modelhook wrappers
  now delegate to it. Classified Added per COMPATIBILITY.md (a new exported func).

- **`agent.WithAgentWritableEngineFactory`.** A new `SubagentOption` injecting a
  composition-supplied factory `func(agentName string) (*Engine, bool)` that rebuilds
  a named specialist's scoped engine WRITABLE (`allowMutating=true` — Edit/Write survive
  scoping) on the def's resolved provider/model, using the MAIN session's command runner
  (direct-write parity, ADR 0077/0058). A Subagent call may now set BOTH `mode:"read-write"`
  and `agent` (previously rejected); the specialist runs with its prompt/skills/catalog +
  Edit/Write against the real parent workspace, dispatch-serial via `MutatesParent`,
  `isolated:false` (A2 auto-approve does not apply). A def with inline MCP servers is
  declined on the writable-agent path (v1 scope limit); reference-only MCP is supported.
  `mode:"read-write"`+`agent`+`model` (all three) stays rejected (v1 scope limit — the
  writable specialist runs on its own resolved model). Classified Added per
  COMPATIBILITY.md (a new exported Option). (#204)

### Changed

- **`tool.MaxAgentDescriptionBytes` raised 800 → 2000; `tool.MaxAgentBodyBytes`
  raised 8192 → 32768 (32 KiB).** The two agent-def caps got more headroom, with an
  asymmetric rationale: the DESCRIPTION rides `Subagent`'s `Spec().Description` on
  every request, summed across all registered agents, and is part of the byte-stable
  prompt-cache prefix, so it stays conservative (2000 B); the BODY is in-context only
  for that one specialist engine's own turns, so it can afford 32 KiB. The constant
  identifiers and types are unchanged — only their VALUES move. Classified Changed
  per COMPATIBILITY.md (an exported const value is part of the API snapshot): an
  external consumer relying on the exact old numbers, or on the old truncation point,
  must re-baseline. Discovery stays deterministic; the only runtime effect is a
  one-time prompt-cache-prefix invalidation. (#156)

## [0.1.0] - 2026-06-22

### Changed

- **Behaviour (no API change): turn-0 instruction fragments are now EPHEMERAL.** The
  soul / project-instruction / memory-index / user-model fragments produced by the
  `prompt.InstructionAssembler` chain are no longer persisted into
  `session.Conversation.Messages` at turn 0. The agent loop now assembles them ONCE per
  run and PREPENDS them to the per-request `port.LLMRequest.Messages` on every turn
  (including resume), never writing them into the conversation, event-carrying them, or
  snapshotting them. The genuine user prompt is still recorded and event-carried
  unchanged. This fixes resume-time history bloat (resumed runs no longer re-append the
  fragments), keeps the persisted conversation clean (so compaction anchors on the
  genuine first instruction), and converges the snapshot + `eventsource.Fold`
  rehydration paths fragment-free. No exported surface changes:
  `RecordUserPromptWithParts` keeps its `instr` parameter (now called with nil) and
  `prompt.IsInjectedTurn0Fragment` is retained (defense-in-depth for legacy persisted
  history). See `docs/adr/0043-ephemeral-turn0-instruction-fragments.md`.

### Added

- `session.PendingAsk.Call` (`session.ToolCallID`) — the opaque gated tool-call id on
  the ask REQUEST half, mirroring `session.ApprovalPayload.Call` on the verdict half.
  It round-trips in the sessnap snapshot, giving a host durable, grammar-free
  correlation of a pending ask back to its tool call (no askID grammar parse). It is
  an opaque identifier, not secret content (it is already implicitly encoded inside the
  askID), so surfacing it opens no new leak surface. (#148)
- `agent.RunOptions.AskIDDiscriminator` (`string`) — an opt-in, host-supplied trailing
  askID component that REPLACES the process-global `"r<serial>"` suffix, making the
  askID `"<sessionID>:<n>:<callID>:<discriminator>"` reconstructable across processes
  from persisted state. The host contract requires it to be unique-per-attempt,
  stable-per-attempt-across-processes, and colon-free (a colon-containing value is
  ignored with a WARN and falls back to the serial). Empty (the zero value) preserves
  the `"r<serial>"` fallback with no behaviour change. See
  `docs/adr/0044-host-supplied-askid-discriminator.md`. (ADR-0044, #117)
- `prompt.IsInjectedTurn0Fragment(text string) bool` — reports whether a string is
  the body of a harness-injected turn-0 context fragment (project instructions /
  soul / memory index / user model) rather than a genuine user instruction. The four
  turn-0 `InstructionAssembler`s record their output as `RoleUser` messages, so a
  consumer that must anchor on "the user's genuine first instruction" (the compaction
  first-user pin; the resume re-injection guard) calls this to skip them. It
  recognises each fragment by the header its renderer prepends (the shared
  source-of-truth constants in `engine/prompt/turn0.go`), so a header reword is
  reflected automatically. Additive function in `engine/prompt`. See ADR 0012.
- `prompt.DefaultTone() string` — returns the built-in tone/style block `Build`
  uses when `Config.Tone` is empty. Mirrors `DefaultRole()`; lets the composition
  layer compose an output-economy tier delta (ADR 0041: the "terse" posture
  appends an answer-length clause) onto the SAME default tone the prompt package
  uses, rather than carrying a private verbatim copy that could silently diverge.
  Additive; no frozen domain type changes.

### Removed

- **BREAKING:** `agent.WithWritableChildForker(f tool.WorkspaceForker) SubagentOption`
  and `agent.WithSubagentAutoMerge(m tool.ForkMerger) SubagentOption` — removed. The
  writable Subagent (`mode:"read-write"`) no longer forks the workspace or merges a
  diff back: it now writes DIRECTLY to the real parent workspace, exactly as the main
  agent does, and git is the rollback layer (ADR 0077, which supersedes the
  writable-subagent decision in ADR 0040). `agent.WithWritableChildEngine` is RETAINED
  (a read-write call still selects a separate Edit/Write-bearing engine); it now runs
  against the parent workspace with the main session's command runner, no forker, no
  merger. The dispatcher still runs a read-write call mutate-serial — `SubagentTool.
  MutatesParent` is now decoupled from any merger (true whenever the writable engine is
  wired and the call is `mode:"read-write"`). Composition no longer wires a forker or
  merger into the Subagent tool; the shared `tool.ForkMerger` remains for Parallel's
  single-branch auto-merge only. See `docs/adr/0077-direct-write-subagent.md`.

## [0.0.4] - 2026-06-21

### Added

- `tool.ForkMerger` — an OPTIONAL port (`Merge(ctx, forkRoot, parentWS) error`)
  by which a preserved winning fork's changes are merged BACK into the parent
  workspace. Additive interface in `engine/tool` (next to `WorkspaceForker`); no
  frozen domain type changes. See `docs/adr/0039-parallel-auto-merge.md`.
- `agent.WithAutoMerge(m tool.ForkMerger) ParallelOption` — wires a merger into
  the Parallel tool. When set AND a `join=first` or `join=judge` run has exactly
  one branch with a successful winner, the winner's diff is auto-merged into the
  parent workspace after the run. nil (the default for the option itself) keeps
  the no-auto-merge behaviour for that tool instance; composition wires a merger
  unconditionally (default-on — see `docs/adr/0039-parallel-auto-merge.md`).
  Multi-branch runs never auto-merge. The merge is a POST-RUN step, so
  `ParallelTool.ReadOnly()` stays `true`. On a conflict the tool returns an error
  naming the conflict + the preserved fork path; it never forces. The `forker.Merger`
  adapter runs `git diff --no-textconv` and refuses `.gitattributes`-touching
  patches (closes attacker-named `diff.*.textconv`/`filter.*.smudge` RCE from an
  untrusted fork `.git`). See `docs/adr/0039-parallel-auto-merge.md`.
- `agent.WithWritableChildEngine(e *Engine) SubagentOption`,
  `agent.WithWritableChildForker(f tool.WorkspaceForker) SubagentOption`, and
  `agent.WithSubagentAutoMerge(m tool.ForkMerger) SubagentOption` — wire the
  WRITABLE-subagent path (the `mode:"read-write"` Subagent arg). A read-write call
  runs the writable child engine (an Edit/Write-bearing explorer) in a force-copy
  fork; its working-tree diff is merged back into the parent workspace after the
  run via the injected merger (the SAME composition-owned, process-wide serialized
  `tool.ForkMerger` Parallel's `WithAutoMerge` uses). The merge is a POST-RUN step,
  so `SubagentTool.ReadOnly()` stays `true`. On a conflict the tool returns an error
  naming the preserved fork and does not force; nil options leave the writable path
  unwired (a `mode:"read-write"` arg then surfaces a model-addressable "not
  supported" error). `mode:"read-write"` is rejected with `background`/`agent` and
  composes with `fork`/`model`/`resume`/`output_schema`.
- `agent.(*SubagentTool).MutatesParent(call session.ToolCall) bool` and
  `agent.(*ParallelTool).MutatesParent(call session.ToolCall) bool` — implement an
  internal optional `parentMutatingCaller` seam the dispatcher consults: a `ReadOnly()`
  tool stays read-only for fan-out, but a CALL that will merge a fork diff back into
  the parent workspace (a `mode:"read-write"` Subagent, or a single-branch
  `join=first`/`judge` auto-merging Parallel) reports `true` and is excluded from the
  concurrent read batch (dispatch-serial), so its post-run merge never overlaps a
  sibling parent read. Returns `false` for read-only fan-out and for malformed args.

## [0.0.3] - 2026-06-21

### Hygiene

- `go.mod`: the `go` directive is now the minor version `go 1.26`, not the patch
  `go 1.26.4`. A library's `go` directive sets the language version it requires,
  and Go raises a consumer's own directive to match the highest one in its module
  graph — so a patch-level directive forces every consumer to a patch directive
  too. The engine uses no Go 1.26.4-specific language feature, so `go 1.26` is the
  correct floor. This unblocks consumers (e.g. a downstream consumer) whose CI forbids a
  patch-level `go` directive. No public-API change. (A `toolchain` directive, if
  ever added, may stay patch-pinned — only the `go` language directive must be
  minor.)

## [0.0.2] - 2026-06-20

### Added

- `prompt`: `Builder` func type (`func(Config) Layered`) — the system-prompt
  assembly seam. `agent.Deps.PromptBuilder` (optional; nil → `prompt.Build`,
  byte-identical to v0.0.1) lets a host embedding the engine for a non-coding
  agent fully own the system prompt (role, tone, safety, tool inventory) with no
  coding-agent defaults and no `Available tools:` block. Only the MAIN loop's
  `buildRequest` routes through it; the compaction summarizer (`cascade.go`)
  builds its own `prompt.Layered` directly and is unaffected. (#127)

## [0.0.1] - 2026-06-19

This is the **initial baseline** (tagged `engine/v0.0.1`). The entries below record the establishment of
the module and its compatibility contract, not a change to a previously-published
surface.

### Added

- The engine became its own Go module, `github.com/stacklok/mecatl/engine`,
  importable independently of the host repo (monorepo via `go.work`). (#113,
  [ADR 0036](../docs/adr/0036-engine-module.md))
- A public API stability contract for the seven core packages: this CHANGELOG,
  [COMPATIBILITY.md](./COMPATIBILITY.md), the committed text snapshots under
  [`engine/api/`](./api/), and the `api-compat` freshness gate that fails CI on
  an unflagged change to the exported surface. (#114,
  [ADR 0037](../docs/adr/0037-engine-stability-contract.md))
- `engine/adapter/eventsource` reference fold (event-sourced `SessionStore.Load`
  rehydration): `Fold` reconstructs a `*session.Session` from a `port.EventLog`
  stream plus out-of-band creation metadata (`SessionMeta`), for hosts whose system
  of record is an append-only event log. It is an EXCLUDED reference adapter (no
  guarded-surface change) and ships with the documented reconstruction contract in
  [COMPATIBILITY.md](./COMPATIBILITY.md) (the only residual limitation is the
  provider-private `Reasoning`/`ProviderPhase`/`ItemID` replay fields). (#115,
  [ADR 0038](../docs/adr/0038-event-sourced-rehydration.md))
- `session`: `EvUserPrompt` event (+ `UserPromptPayload` + `Event.UserPrompt`). The
  durable event log now records the user-role messages the loop adds — the genuine
  client prompt and the harness-authored synthetic continuations (nudges/notices) —
  so an event-sourced fold reconstructs user-role turns (closing the "the log can't
  show what the user asked" gap, ADR 0027 row 11). It is LOG-ONLY: the relay appends
  it and skips it on the live client wire (the EvApproval/EvCompactionArchive
  precedent; wire `type` string passthrough, no proto enum). (#115,
  [ADR 0038](../docs/adr/0038-event-sourced-rehydration.md))

### Hygiene

- CI now runs `govulncheck` on the engine module (a reachable-vulnerability scan
  of its own dependency closure, separate from the root module's), and
  `.github/dependabot.yml` keeps the engine `go.mod` current — supply-chain
  hygiene for the engine library's consumers. No public-API change. (#118)
