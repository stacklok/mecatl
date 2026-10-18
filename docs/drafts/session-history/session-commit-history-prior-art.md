# Session-history prior art

**Status:** Research companion and recommendations for discussion, not an approved design contract.
**Scope:** Persistence, client reload, recovery, extension-owned session data, and supplemental API-command and cancellation comparisons.
**Initial survey date:** October 11, 2026. Supplemental comparisons identify their source revisions separately.

The most useful references address different parts of the [committed-session-history proposal](session-commit-history.md).
OpenCode supplies a transactional projection and replay example; OpenHands V1 supplies a concrete reconnect protocol; Codex separates canonical history from query indexes behind a storage interface; Pi gives extensions explicit durable records within session history.
ActiveGraph and LangGraph illuminate provenance and execution replay, respectively, without serving as interchangeable harness designs.
The [durable-execution and recovery survey](session-commit-history-durable-execution-prior-art.md) complements this artifact with recovery literature, workflow runtimes, and durable-agent integrations; it distinguishes retry policy from crash re-execution and tests whether an established execution model fits.

## Evidence and scope

This survey distinguishes **documented** behavior, **source-traced** behavior, and **tested** behavior.
Source-traced means the cited implementation was inspected, including relevant upstream tests where noted; it does not mean those tests were executed or failure behavior was reproduced.
No runtime experiments were performed by this survey's source-review pass.
The [supplemental cache investigation](#provider-cache-stability-across-session-reload) separately attributes operator-supplied live results and states their evidence limits.
An unverified capability is an open question, not evidence that a project lacks it.

The source traces use these revisions:

| Project | Revision | Evidence |
| --- | --- | --- |
| OpenCode | `707de5832656e89cc0f04436aa1cd5f4f91c5763` | Source and tests on the default `dev` branch |
| OpenHands V1 SDK/server | `3934349da2db0433238621542173a00a8af7439c` | Source and tests; separate from the legacy application |
| Codex | `9f3d25588b3f0bdf452d4eda919f2c2365a89bba` | Source, interface documentation, and tests |
| Pi | `04566631c2b51fb688486bc1f040f60d1518c126` | Source, session/extension documentation, and tests; coding-agent JSONL path |
| ActiveGraph | `50d8405923b79e4e823a73a3045bb969436a872c` | Source, events/operator documentation, and tests |
| LangGraph | `799c2f04fd3a56186e24e9fb8bbf3568e543cfbe` | Source, checkpointer documentation, and tests |

[Ozz's comment on #2179](https://github.com/stacklok/mecatl/pull/2179#issuecomment-6057540127) asks for pluggable storage drivers and portable histories that preserve semantics across deployment types.
It also identifies OpenCode's transaction boundary, OpenHands V1's reconnect sequence, and ActiveGraph's provenance as specific leads.
Expected-head checks alone do not establish the proposal's stronger atomic head, ownership-epoch, and unexpired-claim contract.

## Comparison at a glance

| System | Persisted authority | Client/reconstruction pattern | Extension data |
| --- | --- | --- | --- |
| OpenCode | SQLite session/message projections; retained event payloads are configurable | Per-aggregate sequenced durable log; separate volatile global feed | Session metadata, plugin KV state, standard prompt/tool content mutations |
| OpenHands V1 | Immutable event files plus separate agent/runtime snapshot | Subscribe, buffer, capture boundary, replay, deduplicate, follow | Tool schemas/results, agent state, installed SDK event subclasses |
| Codex | Local canonical JSONL rollouts; derived SQLite metadata/indexes | Continuation replay and separately paginated display | Standard tool outputs, validated history injection; host-defined durable record types |
| Pi | Versioned JSONL entry tree | Active-branch context reconstruction; compaction retains original entries | Explicit custom data records, custom context messages, tool details, context edits |
| ActiveGraph | Authoritative event log behind an EventStore | Commit before projection; ordinary load reconstructs without behaviors | Custom JSON events with actor and causal references |
| LangGraph | State checkpoints plus task-level pending writes | Resume graph execution from saved state | Application-defined channels/reducers, serializers, and storage drivers |

Portability has several meanings here.
A readable export can preserve a transcript while omitting plugin state, runnable obligations, or the original event log.
A storage interface can support several backends without proving identical locking, durability, or external-execution semantics.
The survey therefore treats transcript transfer, continuation transfer, and execution-resource transfer separately.

## Graceful server upgrade and ownership handoff

The inspected paths do not establish a complete equivalent to the proposal's future active-tree handoff contract.
This is a bounded research conclusion, not a claim that every product or deployment built on these projects lacks graceful upgrade.
Persistence, restart recovery, workspace pause/resume, and client reconnect each solve part of the problem; none alone establishes safe transfer of an active server-owned delegation tree.

For this comparison, a complete graceful handoff would establish:

- Coordinated drain of the root session, descendants, and extension-owned work to committed safe positions.
- Preservation of pending asks, accepted decisions, steers, and continuation-critical obligations.
- Publication of a complete parked-tree marker, release of the old claim, and fenced acquisition under a new epoch.
- Reconstruction by a compatible successor without replaying effects or treating unknown outcomes as failures.
- Durable discovery of released runnable work without waiting for a client to open the session.
- Client catch-up from committed positions independently of the former owner.

The proposal's future graceful path waits for active calls to settle before publishing the tree marker.
Remote execution reattachment is a separate tool-owned recovery capability; a persisted task ID or workspace does not prove an outcome or authorize transfer of in-flight effects.
A shutdown deadline that prevents complete parking falls back to explicit crash-style recovery, rather than publishing a partial graceful handoff.

### Closest references and missing contracts

| Reference | Closest aspect | Gap relative to complete active-tree handoff |
| --- | --- | --- |
| OpenHands V1 | Closest overall operational shape: separate agent server/workspace, persisted runtime state, generation leases, and client reconnect | Split event/snapshot authority; local guarded writes omit expiry checks; no verified whole-tree drain/park/publication protocol or authoritative external-outcome recovery |
| Codex | Closest storage-interface reference: writer acquisition, canonical history, continuation replay, and separately paginated display | Inspected local locking does not provide distributed epoch/expiry fencing; active-work transfer was not established |
| ActiveGraph | Closest acceptance/reconstruction reference: commit before projection, expected-head checks, and load without behavior execution | Explicitly excludes a distributed runtime; custom events do not automatically reconstruct custom continuation state |
| OpenCode | Transactional projections and retained-sequence catch-up when retention is enabled | Local coordination/wakeups and restart settlement do not establish graceful ownership transfer; an aborted call does not prove its external effects stopped |
| Pi | Extension state retained within history and reconstructible across reload/branching | Inspected JSONL path has no corresponding server-ownership or coordinated graceful-transfer contract |
| LangGraph | Checkpointed task results, interrupts, and extensible continuation state | Resume can restart node code; checkpoint recovery does not itself establish whole-tree ownership transfer or safe external-effect continuation |

OpenHands is therefore the nearest operational comparison, while Codex and ActiveGraph provide narrower architectural references.
No single example supplies a handoff design to adopt wholesale.
The per-project sections below contain the source evidence and limitations behind these comparisons.

### Persistence is necessary, but not the handoff protocol

Committed history establishes what the previous owner accepted.
It does not establish that a remote process stopped, that an extension's transient resources are transferable, or that another server has compatible interpreters and execution access.
Graceful upgrade also needs engine/tool/extension lifecycle contracts, writer fencing, complete parked-state publication, and successor discovery.
The proposal's park points, obligation metadata, root-tree ownership, and atomic epoch/expiry checks address these distinct requirements.
Their presence in a history format is not proof that lifecycle coordination is implemented or qualified.

The [first cutover](session-commit-history.md#delivery-boundary) establishes trustworthy reload and explicit interrupted-run recovery.
It does not promise mixed-version rolling deployment or seamless transfer of active work.
The [compatible-replica handoff section](session-commit-history.md#compatible-replica-active-tree-handoff-later-phase) describes a later contract that the persistence foundation must accommodate.
The prior-art comparison supports retaining that scope distinction, rather than expanding first-cutover delivery or claiming a complete upgrade protocol has been validated.

## OpenCode

### Persistence and delivery

**Source-traced:** the [event bus](https://github.com/anomalyco/opencode/blob/707de5832656e89cc0f04436aa1cd5f4f91c5763/packages/core/src/bus.ts#L182-L908) commits registered database projections, sequence advancement, and retained event payloads in one publication transaction.
Normal publication invokes live listeners after that transaction returns.
[Bus tests](https://github.com/anomalyco/opencode/blob/707de5832656e89cc0f04436aa1cd5f4f91c5763/packages/core/test/bus.test.ts#L240-L664) exercise rollback, notification ordering, batches, and retention-disabled projection.
These tests were inspected, not run.

Ozz's transaction description needs two qualifications.
Event payload retention defaults off in ordinary CLI/server composition; projections and sequence numbers can persist without a replayable event row.
The [CLI composition](https://github.com/anomalyco/opencode/blob/707de5832656e89cc0f04436aa1cd5f4f91c5763/packages/cli/src/server-process.ts#L94-L133) leaves retention unset, while the workerd host explicitly enables it.
An enclosing transaction can make the publication transaction a savepoint, so returning from that inner transaction is not necessarily final database commit; see the [SQLite transaction adapter](https://github.com/anomalyco/opencode/blob/707de5832656e89cc0f04436aa1cd5f4f91c5763/packages/core/src/database/drizzle/effect-sqlite/session.ts#L140-L225).

The durable `log` reader follows an aggregate from an exclusive sequence cursor and subscribes to wakeups before capturing a watermark.
Wakeups lead to further storage reads, rather than acting as the history payload.
The global SSE feed remains volatile and has no equivalent retained cursor guarantee.
The inspected follow wakeups are process-local and have no periodic storage rescan; a different bus/process can commit without waking an existing follower.
A fresh reconnect can discover retained rows, but a follower does not establish cross-replica convergence by notification alone.
Node/Bun [database configuration](https://github.com/anomalyco/opencode/blob/707de5832656e89cc0f04436aa1cd5f4f91c5763/packages/core/src/database/database.ts#L39-L44) uses SQLite WAL with `synchronous=NORMAL`.
Under [SQLite's documented guarantee](https://www.sqlite.org/pragma.html#pragma_synchronous), this is weaker against OS crash or power loss than a per-transaction synchronized commit.
Infrastructure durability for the separate workerd storage path was not established.

**Representative tool/crash sequence:** the runner [publishes `Tool.Called` before execution](https://github.com/anomalyco/opencode/blob/707de5832656e89cc0f04436aa1cd5f4f91c5763/packages/core/src/session/runner/step.ts#L107-L157), but trusted rewrites, validation, and authorization follow that call marker.
That marker is not an authorized effective intent.
After execution, result hooks and normalization precede canonical `Tool.Success` publication.
A crash after an external effect but before result publication leaves uncertain effects.
[Restart handling](https://github.com/anomalyco/opencode/blob/707de5832656e89cc0f04436aa1cd5f4f91c5763/packages/core/src/session/runner/llm.ts#L329-L350) settles stale calls as aborted failures and permits model continuation; this does not prove those effects failed or prevent the model from choosing similar work again.
[Runner tests](https://github.com/anomalyco/opencode/blob/707de5832656e89cc0f04436aa1cd5f4f91c5763/packages/core/test/session-runner.test.ts#L3670-L3698) exercise result settlement before continuation, while [stale-call tests](https://github.com/anomalyco/opencode/blob/707de5832656e89cc0f04436aa1cd5f4f91c5763/packages/core/test/session-runner.test.ts#L4478-L4523) cover the aborted result.
A crash after database commit but before notification can be recovered through retained rows when retention is enabled; CLI defaults instead reload the projected state.

### Extensions and transfer

The [plugin host](https://github.com/anomalyco/opencode/blob/707de5832656e89cc0f04436aa1cd5f4f91c5763/packages/core/src/plugin/host.ts#L534-L635) exposes durable plugin-namespaced JSON storage and session metadata.
Plugin KV state is separate from session history and is not automatically exported or copied into a fork.
Session metadata is opaque to core and travels with ordinary session transfer; sanitized export redacts it.

[Session hooks](https://github.com/anomalyco/opencode/blob/707de5832656e89cc0f04436aa1cd5f4f91c5763/packages/plugin/src/effect/session.ts#L14-L172) can change admitted prompt content and request-local model context.
These differ: an admitted prompt becomes history, while request-local context mutation does not by itself rewrite history.
Tool-result hooks can replace content and metadata before the runner publishes the canonical result.
The public plugin event interface offers subscription, rather than arbitrary transactional projector registration or custom durable event publication.
A narrower reconstruction asymmetry appears in compaction metadata: the [compaction hook](https://github.com/anomalyco/opencode/blob/707de5832656e89cc0f04436aa1cd5f4f91c5763/packages/core/src/session/compaction.ts#L594-L655) supplies event-envelope metadata, which the live updater applies to projections, but the event-row serializer omits that envelope metadata.
That annotation is therefore not established as reconstructible from the retained event log alone.
This is a source-derived finding, not a reproduced crash.

[Transfer](https://github.com/anomalyco/opencode/blob/707de5832656e89cc0f04436aa1cd5f4f91c5763/packages/core/src/session/transfer.ts#L57-L205) moves settled projected transcripts, rather than the full original event log.
Forks copy settled projections and assign new message IDs.
This is a useful transcript-transfer reference, but not evidence of a self-contained copy of every continuation-critical extension record.

**Lesson:** preserve the event/projection atomicity where useful, but make retained authoritative history unconditional in Mecatl's proposed storage contract.
Keep notification delivery and durable catch-up distinct.

## OpenHands V1

### Persistence and reconnect

**Source-traced:** the SDK's [EventLog](https://github.com/OpenHands/software-agent-sdk/blob/3934349da2db0433238621542173a00a8af7439c/openhands-sdk/openhands/sdk/conversation/event_store.py#L188-L238) appends indexed immutable event files under a FileStore lock.
[LocalConversation](https://github.com/OpenHands/software-agent-sdk/blob/3934349da2db0433238621542173a00a8af7439c/openhands-sdk/openhands/sdk/conversation/impl/local_conversation.py#L424-L447) persists normal events before invoking subscribers.
A separate [base-state snapshot](https://github.com/OpenHands/software-agent-sdk/blob/3934349da2db0433238621542173a00a8af7439c/openhands-sdk/openhands/sdk/conversation/state.py#L430-L568) supplies agent/runtime continuation state.
Events and this snapshot do not commit as one transaction.

Ozz's reconnect description is confirmed by the [session socket](https://github.com/OpenHands/software-agent-sdk/blob/3934349da2db0433238621542173a00a8af7439c/openhands-agent-server/openhands/agent_server/session_socket.py#L173-L417): subscribe and buffer first, capture `len(events)-1`, replay after the client's cursor through that boundary, discard buffered persisted events at or below it, then follow live.
An [upstream test](https://github.com/OpenHands/software-agent-sdk/blob/3934349da2db0433238621542173a00a8af7439c/tests/agent_server/test_session_socket.py#L140-L200) inserts an event during replay and checks duplicate prevention.

[Streaming token/progress events](https://github.com/OpenHands/software-agent-sdk/blob/3934349da2db0433238621542173a00a8af7439c/openhands-agent-server/openhands/agent_server/event_service.py#L1212-L1242) bypass durable conversation events.
The socket also carries transient state snapshots, so it is not exclusively a durable stream.
Durable sequence position, rather than generic event-ID matching, defines the replay/live join.

### Recovery and extensions

A crash can separate an appended event from saved snapshot HEAD.
[Recovery](https://github.com/OpenHands/software-agent-sdk/blob/3934349da2db0433238621542173a00a8af7439c/openhands-agent-server/openhands/agent_server/event_service.py#L1328-L1371) scans history for unmatched actions and records an error instead of establishing a successful outcome.
Workspace reconnect and retained workspace files do not by themselves establish exactly-once tool execution.
The [conversation lease](https://github.com/OpenHands/software-agent-sdk/blob/3934349da2db0433238621542173a00a8af7439c/openhands-agent-server/openhands/agent_server/conversation_lease.py#L101-L232) uses a local JSON file and file lock with owner, generation, and expiry.
Guarded writes compare owner and generation, but do not check expiry; an expired owner can still write until takeover changes the generation.
The SDK explicitly cautions against relying on its filesystem locking on NFS/network filesystems.
This is a local coordination reference, not the proposal's backend-atomic unexpired-claim fence.

The replay/live join also has limits at this revision.
A cursor beyond the captured head is not rejected or reset, and the replay handoff buffer is an unbounded list.
The live socket writer has a separate 16 MiB pending-byte cap and drops a wedged connection.
The [socket tests](https://github.com/OpenHands/software-agent-sdk/blob/3934349da2db0433238621542173a00a8af7439c/tests/agent_server/test_session_socket.py#L140-L280) cover replay/live deduplication and a wedged live writer, but do not establish invalid-cursor handling or a bounded replay buffer.

**Representative crash/reconnect sequence:** an action is appended at sequence 41, then the process dies before snapshot HEAD advances.
Recovery loads the older snapshot plus full event log and appends a synthetic error for the unmatched action.
A reconnecting client at sequence 40 buffers live publication, replays through the captured head including the recovery event, deduplicates, then follows live.
This prevents treating the action as fresh work, but the synthetic error does not prove whether a remote side effect occurred.
A [recovery test](https://github.com/OpenHands/software-agent-sdk/blob/3934349da2db0433238621542173a00a8af7439c/tests/sdk/conversation/test_get_unmatched_actions.py#L227-L260) checks duplicate-execution prevention.
Pending confirmation state and actions are persisted, but this trace did not establish a crash/reload test for pending approvals or a durable exact-ask decision identity.

Supported custom tools persist actions and observations in ordinary event types.
[Client-tool specifications](https://github.com/OpenHands/software-agent-sdk/blob/3934349da2db0433238621542173a00a8af7439c/openhands-sdk/openhands/sdk/tool/client_tool.py#L99-L149) persist in tool parameters and re-register schemas during restore.
The serializable `agent_state` dictionary supplies flexible snapshot continuation state.

Installed SDK/agent code can define custom `Event` subclasses using a runtime `kind` discriminator.
This is deeper than live observation, but the remote event-write endpoint accepts only specified user/control events.
An unknown `kind` [fails decoding](https://github.com/OpenHands/software-agent-sdk/blob/3934349da2db0433238621542173a00a8af7439c/openhands-sdk/openhands/sdk/utils/models.py#L351-L360).
A custom event contributes to model context only through the [LLM conversion interface](https://github.com/OpenHands/software-agent-sdk/blob/3934349da2db0433238621542173a00a8af7439c/openhands-sdk/openhands/sdk/context/view/view.py#L111-L160).

**Lesson:** use its replay/live join as a concrete race-test reference.
Its event/snapshot split also illustrates why Mecatl makes continuation checkpoints derived from one committed authority.

## Codex

### Authority and storage interface

**Source-traced:** [ThreadStore](https://github.com/openai/codex/blob/9f3d25588b3f0bdf452d4eda919f2c2365a89bba/codex-rs/thread-store/src/store.rs#L96-L207) provides storage-neutral create, resume, append, flush, and history loading operations.
The [local storage contract](https://github.com/openai/codex/blob/9f3d25588b3f0bdf452d4eda919f2c2365a89bba/codex-rs/thread-store/README.md#L1-L30) makes JSONL rollout data canonical and SQLite metadata queryable derived state.
The live-thread layer applies persistence policy before append.
A storage interface alone therefore does not establish that every live observation is a retained fact.

The [history index](https://github.com/openai/codex/blob/9f3d25588b3f0bdf452d4eda919f2c2365a89bba/codex-rs/thread-store/src/local/thread_history.rs#L103-L160) tracks its materialized rollout position and remains behind when it cannot materialize data.
[Thread protocol](https://github.com/openai/codex/blob/9f3d25588b3f0bdf452d4eda919f2c2365a89bba/codex-rs/app-server-protocol/src/protocol/v2/thread.rs#L340-L567) separates continuation resume from paginated turn/item display and supports persisted-history forks.

The local [writer](https://github.com/openai/codex/blob/9f3d25588b3f0bdf452d4eda919f2c2365a89bba/codex-rs/thread-store/src/local/live_writer.rs#L327-L383) serializes append operations, writes rollout data, and awaits a recorder flush before derived materialization.
The normal [recorder append](https://github.com/openai/codex/blob/9f3d25588b3f0bdf452d4eda919f2c2365a89bba/codex-rs/rollout/src/recorder.rs#L1908-L1917) uses file flush, without an explicit `sync_data` or `sync_all` in that path.
The inspected contract therefore establishes successful writes to the OS, not an explicit power-loss-safe fsync guarantee.
Core commonly orders persistence before presentation, but [append failures](https://github.com/openai/codex/blob/9f3d25588b3f0bdf452d4eda919f2c2365a89bba/codex-rs/core/src/session/mod.rs#L4628-L4636) can be logged without uniformly stopping subsequent delivery.
Ordering an attempted save before an event is weaker than a required successful commit gate.

**Representative crash/load sequence:** JSONL contains a complete appended prefix while SQLite is behind.
Resume acquires a local [OS writer lock](https://github.com/openai/codex/blob/9f3d25588b3f0bdf452d4eda919f2c2365a89bba/codex-rs/rollout/src/writer_lock.rs#L42-L85), loads canonical history, and resumes [index materialization](https://github.com/openai/codex/blob/9f3d25588b3f0bdf452d4eda919f2c2365a89bba/codex-rs/thread-store/src/local/thread_history_materialization.rs#L40-L145) from its saved byte offset/ordinal.
Only complete newline-terminated records enter this materialization path.
[Writer-lock tests](https://github.com/openai/codex/blob/9f3d25588b3f0bdf452d4eda919f2c2365a89bba/codex-rs/rollout/src/writer_lock_tests.rs#L13-L82) cover competing local writers and stale lock cleanup; this is not distributed epoch fencing.
Paginated fork preparation materializes source lineage into SQLite to resolve the chosen boundary.
A failed optional index can therefore block that fork path even though history is canonical elsewhere.

### Extension limits

MCP results become host-defined tool-output records.
[History injection](https://github.com/openai/codex/blob/9f3d25588b3f0bdf452d4eda919f2c2365a89bba/codex-rs/app-server-protocol/src/protocol/v2/thread.rs#L1725-L1737) accepts validated Responses API items; [tests](https://github.com/openai/codex/blob/9f3d25588b3f0bdf452d4eda919f2c2365a89bba/codex-rs/app-server/tests/suite/v2/thread_inject_items.rs#L147-L180) inspect their presence after resume.
The [rollout vocabulary](https://github.com/openai/codex/blob/9f3d25588b3f0bdf452d4eda919f2c2365a89bba/codex-rs/history/src/lib.rs#L213-L231) is host-defined, rather than a public plugin-defined record/reducer registry.
An alternative ThreadStore implementation is a host embedding capability.

Hook context can become durable standard history.
The [hook runtime](https://github.com/openai/codex/blob/9f3d25588b3f0bdf452d4eda919f2c2365a89bba/codex-rs/core/src/hook_runtime.rs#L775-L817) converts context entries into additional input; stop-hook continuation fragments likewise become response items.
[Hook tests](https://github.com/openai/codex/blob/9f3d25588b3f0bdf452d4eda919f2c2365a89bba/codex-rs/core/tests/suite/hooks.rs#L2671-L2738) check that resulting prompts are retained in rollout history and survive resume.
This supports durable hook contributions without establishing arbitrary hook-owned record types or reducers.

[Thread attachments](https://github.com/openai/codex/blob/9f3d25588b3f0bdf452d4eda919f2c2365a89bba/codex-rs/thread-store/src/thread_attachments.rs#L7-L40) provide separate durable annotations with client-defined type, identity, and bounded JSON payload.
They are separate from conversation history, and fork copies are best-effort: attachment-copy failure does not fail the conversation fork.
This is a useful distinction between optional annotations and continuation-critical records whose omission must fail a fork.

The [typed ResponseItem representation](https://github.com/openai/codex/blob/92caad782887cde063572fc0a0efbf5b1317520d/codex-rs/protocol/src/models.rs#L1036-L1122), inspected at the separate revision `92caad782887cde063572fc0a0efbf5b1317520d`, also supplies a replay-fidelity reference.
It keeps message content as separate items with optional per-message phases, reasoning items with their IDs and encrypted content, and function-call item IDs distinct from `call_id`.
This source establishes richer item representation, not a guarantee about storage acceptance ordering or partial-response recovery.
Mecatl's [provider replay contract](session-commit-history.md#versioned-provider-replay-and-richer-response-items) therefore reserves a versioned opaque ordered representation and occurrence-specific bindings alongside canonical history, rather than flattening private fidelity into display text.
It does not adopt Codex's incremental completed-item acceptance, partial-failure behavior, or WebSocket continuation, and it does not change the no-migration cutover.

**Lesson:** adopt an explicit storage contract and materialization watermark, while separately specifying append acceptance, durability, and live-notification behavior.

### Cancellation and result settlement

**Supplemental source trace at `16e578dd1ddaf4f5db9cd1f3dbfea0958fd74339`:** Codex distinguishes interruption of a turn from settlement of its tool calls, but does not guarantee that already-produced output finishes post-tool processing.
The [dispatch cancellation race](https://github.com/openai/codex/blob/16e578dd1ddaf4f5db9cd1f3dbfea0958fd74339/codex-rs/core/src/tools/parallel.rs#L258-L317) aborts unfinished dispatch and can synthesize an aborted result.
Once dispatch reaches its terminal-outcome boundary it waits for completion instead; that boundary is later than the handler merely returning output.
[Post-tool processing](https://github.com/openai/codex/blob/16e578dd1ddaf4f5db9cd1f3dbfea0958fd74339/codex-rs/core/src/tools/registry.rs#L783-L889) remains cancellation-sensitive.
An [integration test](https://github.com/openai/codex/blob/16e578dd1ddaf4f5db9cd1f3dbfea0958fd74339/codex-rs/core/tests/suite/dynamic_tool_cancellation.rs#L54-L170) interrupts a call while its post-hook is blocked and expects an aborted lifecycle outcome.

The canceled [sampling path drains tool results into history before returning](https://github.com/openai/codex/blob/16e578dd1ddaf4f5db9cd1f3dbfea0958fd74339/codex-rs/core/src/session/turn.rs#L3280-L3300), rather than continuing that inference from the drained result.
A [follow-up-turn test](https://github.com/openai/codex/blob/16e578dd1ddaf4f5db9cd1f3dbfea0958fd74339/codex-rs/core/tests/suite/abort_tasks.rs#L271-L350) checks the original shell call and synthetic aborted output in the next request.
This settlement is bounded best effort: [turn interruption](https://github.com/openai/codex/blob/16e578dd1ddaf4f5db9cd1f3dbfea0958fd74339/codex-rs/core/src/tasks/mod.rs#L944-L1014) allows a short graceful interval before force-aborting.
Independent pending-work scheduling can start other eligible work; this is not evidence of a global explicit-user-start-only policy.

Tool cancellation is also implementation-specific.
A [unified-exec test](https://github.com/openai/codex/blob/16e578dd1ddaf4f5db9cd1f3dbfea0958fd74339/codex-rs/core/tests/suite/unified_exec.rs#L3010-L3093) expects a resumable process to survive turn interruption until separate cleanup.
The [one-shot path](https://github.com/openai/codex/blob/16e578dd1ddaf4f5db9cd1f3dbfea0958fd74339/codex-rs/core/src/unified_exec/oneshot.rs#L41-L97) instead terminates its process handle and discards the execution result on cancellation.
Streamed output does not establish what reached canonical model history or completed disclosure checks.
Tests were inspected, not executed.

**Design divergence:** Mecatl chooses bounded completion of the normal result-processing and disclosure pipeline for already-produced output after cancellation, without resuming the canceled agent.
The inspected Codex path does not establish that stronger guarantee.
Nor do Codex hook records establish Mecatl's memory-only pre-release confidentiality contract: their purposes and persistence paths differ.
This choice changes result lifecycle handling, not guardrail verdict semantics or approval authority.

## Pi

**Documented at the pinned revision:** the [session format](https://github.com/earendil-works/pi/blob/04566631c2b51fb688486bc1f040f60d1518c126/packages/coding-agent/docs/session-format.md) stores a versioned JSONL tree with entry IDs and parent IDs.
The selected path supplies model context; other branches remain in the file.
Compaction records summaries and retained boundaries while keeping original entries.
A separate-file fork or clone differs from navigation within one session tree.

Pi makes extension-owned persistence explicit:

| Mechanism | Durable contribution | Model-context effect |
| --- | --- | --- |
| `appendEntry()` / `custom` | Namespaced custom JSON state in session history | Excluded |
| `sendMessage()` / `custom_message` | Custom content plus optional structured details | Content included; details excluded |
| Tool-result `details` | Structured tool state used for rendering/reconstruction | Separate from model-facing content |
| `context_edit` | Append-only replacement/omission instruction for an earlier entry | Changes future context; raw history stays intact |
| Custom entry/message renderer | Interpretation of stored records | Presentation only |

[Extension documentation](https://github.com/earendil-works/pi/blob/04566631c2b51fb688486bc1f040f60d1518c126/packages/coding-agent/docs/extensions.md) tells extensions to reconstruct branch-sensitive state from the active branch.
Reading every entry would merge state from mutually exclusive histories.
It also distinguishes request-local context transformations from persisted entries.

**Source-traced:** the [JSONL write path](https://github.com/earendil-works/pi/blob/04566631c2b51fb688486bc1f040f60d1518c126/packages/coding-agent/src/core/session-manager.ts#L1124-L1213) updates memory and the current leaf before file persistence.
It delays initial creation until a user or assistant message exists, then writes accumulated entries using create-only `wx`; later entries use synchronous file append without explicit fsync or a commit receipt.
An exception can leave memory ahead of disk.
The [loader](https://github.com/earendil-works/pi/blob/04566631c2b51fb688486bc1f040f60d1518c126/packages/coding-agent/src/core/session-manager.ts#L616-L669) skips malformed lines, including interior lines; a valid unterminated record is accepted, while malformed tail fragments are retained physically but ignored.
[File-operation tests](https://github.com/earendil-works/pi/blob/04566631c2b51fb688486bc1f040f60d1518c126/packages/coding-agent/test/session-manager/file-operations.test.ts#L83-L123) cover tail handling, not power-loss durability.
This recovery policy is weaker than Mecatl's proposed refusal to silently lose acknowledged history.

The assistant call-bearing message is awaited before tool dispatch, but [session event dispatch](https://github.com/earendil-works/pi/blob/04566631c2b51fb688486bc1f040f60d1518c126/packages/coding-agent/src/core/agent-session.ts#L1139-L1163) precedes message persistence.
Listeners can observe a completed message before its write succeeds.
The [tool loop](https://github.com/earendil-works/pi/blob/04566631c2b51fb688486bc1f040f60d1518c126/packages/agent/src/agent-loop.ts#L530-L573) runs before hooks and validation, executes, applies result hooks, emits completion, and persists the result message through the session listener.
There is no separate persisted authorized-dispatch intent in this path.

Custom records rely on JSON serialization rather than a registered versioned schema/criticality contract.
Missing extensions leave the bytes loadable: custom messages retain model-context content and fall back to generic rendering; [plain custom entries](https://github.com/earendil-works/pi/blob/04566631c2b51fb688486bc1f040f60d1518c126/packages/coding-agent/src/modes/interactive/interactive-mode.ts#L3858-L3866) disappear from chat when no renderer exists.
There is no missing-continuation-interpreter gate equivalent to the proposal.

**Representative extension/reload/fork sequence:** the [todo extension](https://github.com/earendil-works/pi/blob/04566631c2b51fb688486bc1f040f60d1518c126/packages/coding-agent/examples/extensions/todo.ts#L105-L168) mutates its in-memory list and returns a full snapshot in result `details`.
After result persistence, reload resets RAM and scans raw active-branch tool results to reconstruct the list.
Compaction changes model context while preserving this evidence.
A [selected-path fork](https://github.com/earendil-works/pi/blob/04566631c2b51fb688486bc1f040f60d1518c126/packages/coding-agent/src/core/session-manager.ts#L1632-L1726) copies the ancestral path and can restore that point's snapshot; a separate `forkFrom` path copies all non-header entries, including alternate branches.
This sequence is traced, not an end-to-end todo experiment.
[Head-clone tests](https://github.com/earendil-works/pi/blob/04566631c2b51fb688486bc1f040f60d1518c126/packages/coding-agent/test/suite/agent-session-runtime.test.ts#L391-L429) cover active-branch copying, and [context-edit tests](https://github.com/earendil-works/pi/blob/04566631c2b51fb688486bc1f040f60d1518c126/packages/coding-agent/test/session-context-edit.test.ts#L119-L159) cover branch-relative restoration.
Without the todo extension, the stored evidence survives but its stateful behavior is unavailable.

Structured prompt sections and tool declarations also replay from system-message deltas and compaction checkpoints.
Forced `before_agent_start` system-prompt text is intentionally request-local; [tests](https://github.com/earendil-works/pi/blob/04566631c2b51fb688486bc1f040f60d1518c126/packages/coding-agent/test/system-prompt-updates.test.ts#L112-L167) verify non-persistence.
Retained transcript therefore does not always reproduce the exact historical provider input.

**Lesson:** separate persisted extension data, model-context contribution, and display interpretation.
A custom record's existence should not implicitly give it model visibility or execution authority.
Preserve raw recovery evidence even when the model-context projection compacts or omits it.

## Adjacent runtimes

### ActiveGraph

**Documented (site consulted October 11, 2026):** [events](https://docs.activegraph.ai/concepts/events/index.md) are authoritative append-only facts with JSON payload, actor, and `caused_by` reference.
With a durable store, acceptance validates and canonicalizes, appends, then projects and notifies.
User-defined event types share this representation.
An external event reference combines run ID and event ID.

The [operator contract](https://docs.activegraph.ai/guides/operating-in-production/index.md) describes SQLite and PostgreSQL behind the same EventStore interface, explicit migration, expected-head conflict checks, and ordinary reconstruction without rerunning behaviors.
It explicitly excludes a distributed runtime and built-in client transport.
These are useful storage/provenance contracts, rather than evidence for distributed ownership or multi-client reload.

**Source-traced:** [acceptance](https://github.com/yoheinakajima/activegraph/blob/50d8405923b79e4e823a73a3045bb969436a872c/activegraph/core/graph.py#L629-L670) canonicalizes and validates, appends to the EventStore, then updates in-memory history/projection and calls observers.
[Integrity tests](https://github.com/yoheinakajima/activegraph/blob/50d8405923b79e4e823a73a3045bb969436a872c/tests/test_event_integrity.py#L92-L149) exercise append failure and stale writers.
A legacy listener can throw after acceptance; the [listener test](https://github.com/yoheinakajima/activegraph/blob/50d8405923b79e4e823a73a3045bb969436a872c/tests/test_event_sinks.py#L441-L465) demonstrates that caller failure does not imply no commit.

Ordinary [load](https://github.com/yoheinakajima/activegraph/blob/50d8405923b79e4e823a73a3045bb969436a872c/activegraph/runtime/runtime.py#L3308-L3319) applies recorded events without persisting them again or calling live listeners.
[Replay tests](https://github.com/yoheinakajima/activegraph/blob/50d8405923b79e4e823a73a3045bb969436a872c/tests/test_replay.py#L21-L128) check no behavior execution and no duplicate writes.
Strict verification replay is a separate mode that reruns behaviors.
Custom events persist, but the [built-in projector](https://github.com/yoheinakajima/activegraph/blob/50d8405923b79e4e823a73a3045bb969436a872c/activegraph/core/graph.py#L1071-L1149) restores specified object/relation/patch mutations rather than arbitrary custom continuation state.
Custom event storage is therefore not equivalent to an extension reducer contract.

**Representative acceptance/load sequence:** append succeeds, projection updates, then a listener throws.
The caller must resolve acceptance from history before retrying rather than infer failure from the exception.
Ordinary load reconstructs built-in mutations without executing the listener or behavior again.
Started-but-incomplete behavior work is not automatically recovered by that reconstruction.
Expected-head checks reject a stale store handle, but neither backend's append checks an ownership epoch or expiry.

[Causal inspection](https://github.com/yoheinakajima/activegraph/blob/50d8405923b79e4e823a73a3045bb969436a872c/activegraph/trace/causal.py#L22-L116) joins request/result and parent references.
It stops at missing parents; causal labels supply provenance, not authorization.
A source-derived portability concern remains: [migration](https://github.com/yoheinakajima/activegraph/blob/50d8405923b79e4e823a73a3045bb969436a872c/activegraph/observability/migration.py#L178-L220) copies `iter_events()`, whereas compacted runs also depend on an archive and snapshot sidecar.
The inspected migration writers do not copy those dependencies, suggesting a compacted-run transfer gap to a fresh destination.
This gap has not been reproduced experimentally and should not be treated as a qualified upstream defect.

**Lesson:** preserve causal inspection through ordinary IDs and references, without importing graph ontology or treating reconstruction as new execution.
Test portability against complete reconstruction dependencies, not only ordinary event rows.

### LangGraph

**Documented (site consulted October 11, 2026):** [checkpointers](https://docs.langchain.com/oss/python/langgraph/checkpointers) persist thread-scoped graph state at super-step boundaries and task-level pending writes within a step.
Application-defined state channels and reducers determine what is retained.
Storage drivers and serializers are replaceable; a separate store holds cross-thread data.

Durability is configurable: `sync` saves before the next step, `async` overlaps persistence with execution, and `exit` saves on execution exit.
Time-travel replay executes nodes after the selected checkpoint, including LLM/API requests and interrupts.
Pending writes let successful sibling tasks avoid rerunning during failure recovery.

**Source-traced:** a [CheckpointTuple](https://github.com/langchain-ai/langgraph/blob/799c2f04fd3a56186e24e9fb8bbf3568e543cfbe/libs/checkpoint/langgraph/checkpoint/base/__init__.py#L93-L147) combines channel state/version tracking with pending task writes.
The [sync barrier](https://github.com/langchain-ai/langgraph/blob/799c2f04fd3a56186e24e9fb8bbf3568e543cfbe/libs/langgraph/langgraph/pregel/main.py#L3050-L3079) waits before the next step but follows streamed output; even `sync` does not classify every earlier observation as committed.
Controlled interrupt/error exit saves checkpoint and pending writes, but abrupt process death cannot rely on that exit path.

**Representative recovery sequence:** parallel task A succeeds and its output persists; sibling B fails before the full super-step completes.
Resume [reapplies successful writes](https://github.com/langchain-ai/langgraph/blob/799c2f04fd3a56186e24e9fb8bbf3568e543cfbe/libs/langgraph/langgraph/pregel/_loop.py#L798-L815), so A need not rerun while B retries.
[Pending-write tests](https://github.com/langchain-ai/langgraph/blob/799c2f04fd3a56186e24e9fb8bbf3568e543cfbe/libs/langgraph/tests/test_pregel.py#L891-L992) establish those call counts.
If A's external effect occurs before its output persists, that evidence is absent; task-result caching does not close the uncertain-effect interval.

[Interrupt resume](https://github.com/langchain-ai/langgraph/blob/799c2f04fd3a56186e24e9fb8bbf3568e543cfbe/libs/langgraph/langgraph/types.py#L1019-L1046) restarts the interrupted node and consumes task-local answers by interrupt call index.
Code before the interrupt can run again.
Historical replay drops cached answers and refires interrupts; [time-travel tests](https://github.com/langchain-ai/langgraph/blob/799c2f04fd3a56186e24e9fb8bbf3568e543cfbe/libs/langgraph/tests/test_time_travel.py#L226-L282) distinguish these paths.
This differs from a committed decision bound to one exact durable ask.

[Custom channels](https://github.com/langchain-ai/langgraph/blob/799c2f04fd3a56186e24e9fb8bbf3568e543cfbe/libs/langgraph/langgraph/channels/base.py#L19-L99) define update/checkpoint restoration, while serializers and checkpointers remain injected dependencies.
[Reducer round-trip tests](https://github.com/langchain-ai/langgraph/blob/799c2f04fd3a56186e24e9fb8bbf3568e543cfbe/libs/langgraph/tests/test_channels.py#L94-L107) provide a concrete extension reconstruction example.
Compatible interpreter code is required to give persisted values their continuation meaning.

**Lesson:** distinguish state reconstruction from an execution-resume policy.
A checkpoint and a reducer describe state, but do not by themselves prove an external action safe to repeat or provide a complete retained display transcript.

## API command identity and asynchronous work

These references address the API command layer, not a requirement imposed by append-only session history.
They support separating an immutable submission decision from the current state or completion of the work it affects.
They do not establish exactly-once external tool effects.

### Google long-running operations and Temporal

**Documented:** Google's [AIP-155 request identification](https://google.aip.dev/155) describes client request IDs for duplicate recognition; [AIP-151](https://google.aip.dev/151) describes independently identified long-running work and typed progress/result contracts.
The [operations protobuf at `886464f687986b8b38392d83957a5a2ead4dcf71`](https://github.com/googleapis/googleapis/blob/886464f687986b8b38392d83957a5a2ead4dcf71/google/longrunning/operations.proto#L55-L155) separates the initiating request from an operation's name and terminal response/error.
`CancelOperation` requests cancellation; its acknowledgement does not prove that the work stopped.
`DeleteOperation` forgets the operation result rather than canceling the work.
[AIP-194](https://google.aip.dev/194) treats automatic retry as a separate method-safety decision, not an automatic consequence of using gRPC.
The AIP pages are documentation references rather than pinned implementation experiments.

**Source-traced:** Temporal's [start request and response at `168d38fbe1c84c06e0a5fba2b0af1a27c812e2ef`](https://github.com/temporalio/api/blob/168d38fbe1c84c06e0a5fba2b0af1a27c812e2ef/temporal/api/workflowservice/v1/request_response.proto#L148-L248) separate the request ID, workflow identity, and returned run identity.
Its [cancellation contract](https://github.com/temporalio/api/blob/168d38fbe1c84c06e0a5fba2b0af1a27c812e2ef/temporal/api/workflowservice/v1/service.proto#L446-L465) records a cancellation request rather than promising terminal cancellation.
[Update requests and responses](https://github.com/temporalio/api/blob/168d38fbe1c84c06e0a5fba2b0af1a27c812e2ef/temporal/api/workflowservice/v1/request_response.proto#L1839-L1885) support waiting to a lifecycle stage and returning a reference before the outcome exists.
These are useful identity and observation patterns, not a reason to import Temporal's workflow orchestration model.

The GCE operations-collection experience discussed during design highlights the cost of independently tracking every mutation's workflow, particularly interacting mutations and operation enumeration.
Kubernetes-style reconciliation avoids some of that bookkeeping, but current resource state alone cannot reliably answer whether a particular earlier command completed or was superseded.
The proposal therefore keeps compact command receipts and existing domain lifecycle facts, with common observation conventions rather than a universal enumerable operations collection.
Synchronous mutations are the default; asynchronous work must justify its lifecycle.
Exact protobuf adoption remains an interface decision.

### Bounded repeatable requests

**Documented in the OASIS Committee Specification/Public Review Draft 01:** [Repeatable Requests, sections 3.1.2 and 5](https://docs.oasis-open.org/odata/repeatable-requests/v1.0/csprd01/repeatable-requests-v1.0-csprd01.html) pairs a client-generated request ID with the unchanged original creation time.
The time identifies requests outside the server's remembered interval; those requests fail without execution, including when their individual receipts are gone.
An opaque ID alone cannot distinguish a new request from an old duplicate after eviction.
This is HTTP-oriented prior art whose identity/expiry principle can inform a gRPC command envelope; it does not prescribe Mecatl's wire format.

Mecatl adopts the token-plus-time principle with optional bounded opaque tokens rather than requiring UUID syntax.
It adds independent session/catalog capacities and a durable creation-time cutoff that can shorten the one-hour maximum horizon.
Cutoff advancement rejects every timestamp tie, survives ownership changes, and cannot reopen an evicted interval after clock rollback.
Client timestamps require explicit skew validation, and callers must preserve the entire identity and command on retry.
Changing the timestamp or token is a new submission, not recovery of an ambiguous old outcome.
This protocol cooperates with clients; request identity is neither authorization nor protection against an authorized caller deliberately issuing another command.

## Provider-cache stability across session reload

**Supplemental evidence, October 18, 2026:** An operator-supplied investigation reports 42 successful synthetic requests through configured Sprout proxy routes using Claude Sonnet 5 (`claude-sonnet-5`) and GPT-5.6 Terra (`gpt-5.6-terra`).
This session inspected the local report at `/home/jbeda/src/mecatl-2/.scratch/cache-live/report.txt`; it did not rerun the live probes or the other checkout's tests.
The local path is evidence provenance, not a portable reproduction artifact or a dependency of the contract.

Warm/repeat controls and cache breakpoints after the changed argument location established reuse beyond that location, rather than merely a hit on the unchanged earlier prefix.
The reported results cover the complete proxy/model paths:

| Request change | Both tested routes |
| --- | --- |
| Outer HTTP JSON formatting | Full cache hit. |
| Whitespace between tool-argument JSON tokens | Full cache hit. |
| JSON HTML escaping introduced by snapshot serialization | Full cache hit after reload. |
| Tool-argument object-key reordering | Cache miss on the changed request; hit on identical repetition. |
| Argument value change | Cache miss on the changed request; hit on identical repetition. |

Returning to the original request also hit.
Neither raw transport byte equality nor arbitrary decoded-JSON semantic equality describes the observed cache behavior.
The investigation did not isolate normalization in Sprout from normalization upstream and does not establish identical behavior on other models, routes, TTLs, or cache configurations.

The actual reload experiment followed canonical session state through `ProjectMessages` and a real provider adapter, then through `sessnap.Marshal`/`Unmarshal`, `ProjectMessages`, and a newly constructed adapter.
Captured before/after request bodies were then sent through the proxy with matched controls.
Anthropic bodies remained byte-identical in the tested cases.
OpenAI argument strings changed through JSON compaction/HTML escaping but retained key order and still hit after reload.
The suspected whitespace/escaping cache regression was not reproduced; no production serialization fix was made.

The investigation reports adding `TestProjectMessagesSnapshotRoundTripPreservesPairingAndArgumentOrder` in `engine/port/messages_test.go` in the other checkout.
It exercises actual snapshot restoration with stored/replay-free history, nested keys, string whitespace, and escaping, comparing projections without weakening the assertion to decoded-map equality.
Focused normal/race and engine-standalone tests reportedly passed there; this is not a claim that the test is present or was run on this design branch.
Live probe scripts spend provider credits, use credentials only in-process without logging them, and are not offline CI tests.

**Design implication:** Preserve nested argument key order, string content, historical IDs, provider pairing/item bindings, and ordered replay across storage and reconstruction.
The [cache-stability contract](session-commit-history.md#cache-stable-reconstruction-across-reload) distinguishes stable input reconstruction from provider cache availability.
System-prompt rebuilding, tool definitions/order, model settings, cache-key configuration, breakpoint placement, TTL, and routing need separate checks; the live investigation did not establish their reload stability.

## Suggestions for the current proposal

The [consolidated proposal](session-commit-history.md#architectural-narrative) incorporates the clarifications below as discussion-design requirements, not an approved or implemented contract.
The per-project sections remain the evidence; this section explains how the lessons apply to Mecatl.
Concrete interfaces and remaining recovery details remain open; short-PR foundation staging is settled, and the first cutover does not expand into autonomous recovery, execution migration, or a general plugin platform.

### Make the storage contract executable

The proposal already requires one authority, explicit adapter durability, atomic distributed fencing, and independent checkpoints.
Add a shared adapter conformance suite that exercises accepted append recovery, partial writes, ambiguous outcomes resolved by transition identity, stale-head/epoch/expiry rejection, and checkpoint loss without history loss.
Codex's storage interface and ActiveGraph's same-semantics backend contract provide useful references.
Cross-backend equivalence should compare reconstructed continuation and authorized display at the same selected boundary, rather than compare physical files or indexes.

**Concrete clarification:** describe accepted-append guarantees for process crash, host/OS crash, and backend failover separately.
A method called `flush` must not imply fsync or replicated durability that the implementation does not provide.
Keep durability policy at the adapter/deployment boundary rather than adding an implicit best-effort mode to required engine gates.
Notifications must follow successful outermost commit; either forbid nested publication or defer notification until the enclosing transaction succeeds.
Test outer rollback and visibility from an independent reader, not just reads through the writer's transaction.

### Specify the replay/live join and its limits

The proposal already promises complete pages at C followed by committed updates after C.
Specify duplicate handling, invalid/history-incarnation cursors, a bounded replay buffer or restart policy, and catch-up after a notification is lost or the original writer disappears.
Use OpenHands' subscribe/buffer/boundary/deduplicate sequence as a race-test example, without requiring every backend to use that implementation.
OpenCode's process-local wakeups show why a cross-replica follower needs bounded storage rescan or another independently reliable catch-up mechanism.

**Concrete clarification:** reject or require reload for a cursor beyond the head, from another history incarnation, or outside the retained range.
Bound replay-join buffering in bytes as well as record count.
If the buffer fills, end the attempt with an explicit retry/reload outcome; do not silently drop committed updates.
Every derived index/projection reports its applied position and cannot advance past the accepted prefix.
An optional display index must not become the only way to determine an authoritative fork boundary.

### Separate extension data roles

The proposal already distinguishes private continuation from client-safe projection and critical from display-only records.
Make model-context contribution explicit as well: a record can persist without contributing to the next model input, and a display projection never grants that contribution.
Pi's custom data/message distinction makes this concrete.
Require rebuild tests for extension state through compaction, checkpoint loss, and a sanitized independent fork, including missing or incompatible handlers.
Extension-owned state needed for continuation must remain in committed history; a plugin KV side store cannot silently become another authority.

**Concrete clarification:** test full-history reduction against checkpoint-plus-suffix reduction at the same position.
Run the comparison with pre-compaction records and opaque display-only records, then remove a continuation-critical interpreter and require refusal without activating tools.
Reducers consume retained facts rather than the shortened model-context projection.
Avoid adding arbitrary context-edit or dynamic extension-loading APIs just because Pi exposes them.

### Define portable dependencies without adding execution migration

Ozz's portability request needs a dependency contract as well as a byte format.
Record enough type/version and environment/resource references to explain why another deployment can inspect but cannot safely continue a session.
OpenHands' missing event types and Pi's extension state illustrate why readable JSON is insufficient.
Source writer epochs, approvals, runnable work, and external resources must not become destination authority through implicit transfer.
A general export/import feature and cross-run resource handoff remain separate scope decisions.

**Concrete addition:** specify a compatibility summary of required extension type/schema versions and continuation-critical referenced artifacts.
It can be derived from retained records, rather than maintained as another authority.
Use it to report exact missing dependencies before claiming a session can continue.
Qualify any future transfer against compacted history and required blobs/checkpoints, not only plain event rows.
For first-cutover forks, continuation-critical copy failure must fail publication; optional annotations need an explicit copy/omit policy.
This is a completeness rule, not authorization to add legacy migration.

### Preserve effective inputs and causal inspection

OpenCode's persisted prompt/tool mutations and Pi's request-local transforms expose a distinction worth making explicit in the tool/hook state machine: retained original data, accepted effective content, and request-only decoration have different replay roles.
The proposal requires [complete logical input capture](session-commit-history.md#complete-logical-model-inputs-and-transformation-evidence): effective instructions, ordered messages/media, declarations, model selection and behavior-affecting parameters, and relevant private continuation material.
Retained values or immutable references must reconstruct that input without current templates or configuration.
Raw HTTP bodies, transport framing, credentials, and token-stream fragments are outside that guarantee.
Hook transformation evidence retains input, parameters, output or failure, and relevant identity/version/configuration, under scrubbing and withholding or destruction policy.
Required evidence commits before dependent model execution or tool dispatch; tool intent describes the effective authorized invocation after trusted rewrites rather than only the earlier model request.

The [hook contract](session-commit-history.md#hook-execution-and-recovery) uses at-least-once execution with stable harness-supplied logical invocation IDs, which can derive from durable action/tool/turn anchors, phase, registration identity, and occurrence.
Committed completion is reused; authorized recovery can repeat an invocation with missing completion under the same identity only when its exact permitted inputs remain reconstructible, allowing external integrations to deduplicate effects.
Unreleased sensitive result inputs and payload-bearing hook evidence stay memory-only; process loss makes those invocations unavailable rather than authorizing retention or tool reexecution.
This exception does not authorize rerunning an uncertain ordinary tool.
Completed transformations may batch with the effective request or authorized intent; logical records do not imply one physical write per hook stage.
Pre-intent hooks can have effects even when the tool was never dispatched.
Concrete ID formats and the hook failure/recovery state machine remain open.

Use bounded causal references to run, call, operation, child, and fork position for inspection, following ActiveGraph's provenance idea.
Specify reference scope and fork-remapping rules; unresolved or redacted links should remain explicit.
Actor labels and causal links are provenance, never permissions.
This strengthens the proposal's existing correlations without adding a graph domain model.

### Turn recovery gates into fault-injection scenarios

The proposal distinguishes request, intent, result, and unknown outcomes, and does not authorize automatic uncertain ordinary-tool retry.
Add a failure matrix around those transitions: intent append rejected, intent accepted but acknowledgement lost, effect occurred but result append failed, and result committed but projection/notification failed.
Assert no dispatch before authorized intent, no dependent model continuation before accepted result, and no side-effecting behavior during ordinary history reconstruction.
A synthetic failure is not evidence that an external effect failed.
Test storage-commit retry separately from tool-execution retry, and bind recovered approval decisions to exact durable asks rather than call positions within restarted code.

## Remaining research

The source pass covers six pinned implementations, representative recovery sequences, and selected upstream tests.
The next qualification step is controlled offline fault injection, especially outer-transaction rollback, lost cross-process wakeups, failed result writes, extension reconstruction, and complete compacted-history transfer.
These experiments have not been performed; source-derived hazards are not reproduced defects.
Current authorization revocation, held-result asks, source-deletion/fork races, and remote executor outcome lookup remain incomplete comparisons.
The survey does not claim exhaustive coverage of every extension API or deployment backend.

## Related information

- [Design proposal](session-commit-history.md).
- [Session-history domain model](session-commit-history.modelith.md).
- [Catalog and command-protocol domain model](session-command-protocol.modelith.md).
- [Draft index](../README.md).
