# Session-history detailed architecture reference

**Status:** Discussion draft, not an approved interface or description of shipped behavior.
**Scope:** Specialized architecture contracts, exhaustive lifecycle detail, delivery boundaries, and costs.

Start with the [overview](overview.md), then choose a [bounded review packet](README.md).
This reference is for lookup, not a required end-to-end reading path.
Its detailed sections own specialized requirements; packets own the review explanations and relocated worked sequences, and link here rather than creating a parallel specification.
The [contract reference](session-commit-history-contracts.md) retains exhaustive lifecycle phases, gates, failure/reload matrices, and acceptance cases.

The unchanged [history/execution model](session-commit-history.modelith.md) ([YAML](session-commit-history.modelith.yaml)) and [catalog/command model](session-command-protocol.modelith.md) ([YAML](session-command-protocol.modelith.yaml)) express domain semantics, not delivery status or final API types.
Neither these drafts nor the models authorize production implementation.
The [durable-execution survey](session-commit-history-durable-execution-prior-art.md) supplies foundations and runtime comparisons, not a selected dependency.

## Architectural narrative

The narrative now lives in the [overview](overview.md).
These small sections preserve existing incoming anchors.

### One durable authority

See [the proposed change](overview.md#the-problem-and-the-change) and [storage/observation](storage-and-observation.md).

### Execution follows committed decisions

See [execution and recovery](execution-and-recovery.md).

### Clients share server-owned work

See [commands and lifecycle](commands-and-lifecycle.md).

### Registered extensions share the contracts

See [capability composition and its different extension points](implementation/components.md#extensions-compose-capabilities-through-different-seams) for delegation and learning.
The [extension reconstruction evidence](session-commit-history-contracts.md#built-in-extension-reconstruction-boundary) and [core records/extensions](#core-records-and-extensions) specify the durable session-record seam.

### Ownership eventually moves with the tree

See the [later handoff contract](#compatible-replica-active-tree-handoff-later-phase).

## Delivery boundary

**First cutover:** trustworthy reload, shared controls, and explicit interrupted-run recovery.
It includes required commit gates, complete logical input capture, retained authorized history, and the registered built-in extension path.
High-fidelity private capture is the default, with no history trimming in this cutover and no relaxation of current payload admission bounds.
The first cutover favors small, auditable boundaries over exhaustive automatic recovery.
It must not automatically duplicate uncertain ordinary tool actions or claim certainty without evidence; it can report an interrupted run, missing projection, or unknown outcome and require a later decision.
Correctness and a clear path forward matter more than handling every rare crash interval optimally on day one.
A crashed active run is settled as interrupted, not automatically resumed; authorized recovery or activation controls subsequent work.

**Later capabilities:** graceful compatible-replica active-tree handoff, autonomous takeover and continuation, and background work spanning runs.
More automatic continuation, plugin lifecycle policy, and richer recovery can follow without weakening those honest states.
The first cutover targets trustworthy reload and explicit recovery, not seamless execution through every upgrade or crash.
Historical forks, same-session rewind, networked extensions, and large-artifact spillover also remain later work under the contracts below.
The initial catalog arrangement is one catalog per user within a configured storage/service authority, with exactly one storage provider per catalog.
Multiple catalogs per user, shared/team catalogs, and catalog search remain later capabilities; no multi-storage catalog is proposed.
This work introduces no new guardrail decision semantics, hook-criticality taxonomy, or autonomous result-release approver.

**Breaking rollout:** the new storage and client protocol do not migrate legacy sessions.
The initial service rollout is teardown and upgrade, not mixed-version rolling deployment.

**Runtime isolation:** explicit [kernel/driver interfaces](implementation/kernel.md#interfaces-that-prevent-coupled-changes) separate capability behavior from history integration.
An [isolated harness around the real kernel](implementation/kernel.md#runtime-isolation-and-reconstruction-evidence) can exercise acceptance and reconstruction without activating it through server/client composition.
An engine-selection flag chooses the old or new runtime per server instance, including an embedded server started by a client.
It is not a per-session hot switch: old sessions do not work in the new runtime, and new sessions do not work in the old runtime.
Separate namespaces and fail-closed version checks prevent dual ownership or accidental fallback.
Development coexistence does not add legacy migration, dual-writing, or mixed-version runtime compatibility.
Concrete package boundaries, flag spelling, interfaces, and implementation tasks remain to be specified.

## Detailed contracts

### Why the current arrangement makes this difficult

Today the `SessionStore` snapshot holds the current conversation and private continuation state.
Compaction replaces its conversation with a shorter one.
The separate `EventLog` holds activity and pre-compaction archives, but recording is optional and can fail without stopping a run.

Client streams expose selected live events, including updates that may never reach the log.
There is no shared position that joins a snapshot to a subscription.
See the [persistence architecture](../../architecture/observability.md).

As a result, a newly connected client can recover the model-visible transcript but lose meaningful activity, such as delegation views.
Starting a live feed before reading the snapshot requires buffering and reconciliation.
Starting it afterward can miss changes in between.
[Issue #2114](https://github.com/stacklok/mecatl/issues/2114) tracks the fidelity of the restored client view.

### Proposed model

Foundational terms now live in the [shared vocabulary](vocabulary.md).
A reducer applies retained records in order; a delegation tree is a root session with active delegated descendants.
The detailed responsibility and derived-view contracts follow.

#### Design layers and responsibilities

The proposal now includes API command and catalog contracts in addition to session-storage redesign.
They are related obligations, not one persistence abstraction:

| Layer | Responsibility | Authoritative facts |
| --- | --- | --- |
| API command protocol | Authorization, optional retry identity, bounded receipts, and consistent synchronous/asynchronous responses and work observation. | A request's committed admission decision, distinct from the work it targets. |
| Session catalog | Identified ownership/authorization scope, bounded discovery, and session creation, publication, and deletion. | Strict catalog lifecycle decisions; listing and possible search indexes are derived. |
| Session execution | Run admission, shared controls, tool/hook processing, extensions, cancellation, and required execution gates. | Ordered session decisions and established outcomes. |
| History and storage | Atomic durable acceptance, history reads, ownership fencing, and reconstruction support under declared backend guarantees. | Accepted session history and catalog decisions; checkpoints and indexes accelerate them. |

API retry protection is opt-in and lives above storage.
Its receipt must nevertheless commit atomically with the mutation decision it protects; a transport-local deduplication cache cannot survive ownership changes.
Internal transition identities remain required independently of whether a caller supplies a retry token.
A common async observation contract does not create a universal operations collection or a separately orchestrated job for every mutation.

The design retains the codebase's broad separation between domain behavior, persistence adapters, host composition, and client presentation.
Established durable-execution systems are references for semantics and failure handling, not required runtime dependencies.
The design uses narrowly scoped coordination within Mecatl rather than adopting or recreating a general-purpose workflow platform.
Dependencies must justify their deployment and maintenance cost, preserve straightforward open-source distribution and licensing, and fit the harness contracts without forcing different execution semantics.
The table distinguishes those existing boundaries from changed responsibilities and new mechanisms; it does not describe the proposed commit system as already implemented.

| Component | Existing code and responsibility | Responsibility in this design |
| --- | --- | --- |
| Core engine | `engine/agent.Engine` and `Run` drive turns and tools, mutate `engine/session.Session`, and save snapshots through `port.SessionStore`. | Retain execution and domain invariants; make required commits execution gates rather than best-effort saves, and use registered reducers for extension state. |
| Storage adapter | Implementations of `engine/port.SessionStore`, `EventLog`, and `SessionLease` provide distinct snapshot, activity-history, and ownership contracts. | Replace the split authority with committed history; implement durable reads and append, assign history positions, and enforce atomic head and ownership checks under the declared operating mode. |
| Host/server | `internal/app` wires dependencies; `internal/adapter/server.Service` manages sessions, run entry, controls, and leases, while server recorders write activity history. | Retain composition, authentication, and API authorization; compose kernel coordination with storage adapters and authorized projections, register extension handlers, forward to the owning replica, and initiate recovery. Execution becomes independent of the initiating client stream. |
| Extensions | `Subagent`, `Parallel`, and `Team` are built-in tools in `engine/agent`, with team domain behavior in `engine/team`, registered by host composition. They do not currently use the proposed generic extension record/state mechanism. | Introduce versioned records, private-state reducers, checkpoint serialization, lifecycle handlers, and approved display-detail projections within core constraints. |
| Clients | `cmd/mecatui/client` receives server-produced transcripts and events; the TUI maintains local display state and submits controls. | Retain local rendering and control submission; consume complete authorized history and committed updates joined by position, reconcile provisional output, and reflect shared pending controls. |

Today, persistence has several callers rather than one application coordinator.
The engine's [`save`](../../../engine/agent/loop.go) writes snapshots best-effort, and [`SubagentTool.persistChild`](../../../engine/agent/subagent.go) has its own child-save path.
Server [`RunEventRecorder`](../../../internal/adapter/server/event_recorder.go) records activity separately.
All of these writers must enter the required commit boundary in the redesign; the table does not move every write into the host or make the engine depend on concrete storage adapters.

Here, *host* or *server* means the application layer that wires the core engine to adapters and exposes the API.
The kernel comprises the coupled `loop`, `coordinator`, and `session` responsibilities in the [shared vocabulary](vocabulary.md#kernel-and-driver-vocabulary).
The coordinator orders transitions and controls and arranges commits with the loop; host composition owns its installation and lifetime, not a separate implementation of execution correctness.
It is not an existing type, a storage-port implementation, or a public custom-coordinator extension point.
One active coordination authority governs each session history under the root-tree ownership claim; object counts and exact package paths remain open.

The *owning replica* is the host process holding the backend-authoritative right to drive a session and its delegation tree.
That right is an `OwnershipClaim`, with an owner, writer epoch, and expiry.
A writer epoch distinguishes successive ownership acquisitions so the adapter can reject an old writer; it is not a history position.
Client attachment is a connection/interaction relationship, not an ownership claim.

These are responsibility boundaries, not final interface names or a decision to place all coordination in one package.
The exact allocation of commit APIs between the loop and coupled kernel coordinator remains an interface question.

#### Derived views and their owners

A `ContinuationCheckpoint` materializes private continuation state through a known committed position.
The engine and registered extension reducers reconstruct that state from history.
The kernel coordinator arranges checkpoint construction and any persistence through the storage adapter to accelerate later loading.
A checkpoint is optional acceleration, not another authority.

A `ClientProjection` is the server-produced, authorized display view of committed session facts.
Server-produced display views already exist: [`SessionTranscript`](../../../cmd/mecatui/client/transcript.go) represents the authoritative snapshot-derived conversation alongside separately classified optional activity replay.
The redesign changes the completeness and subscription contract by deriving retained display items from committed history and joining loads to updates with a committed position; it does not newly move rendering or all projection work to the server.
The host applies access and disclosure rules, using approved extension projections for tool-specific detail, and sends projected snapshots, history items, and committed updates to clients.
The name describes a read contract; it does not require a separately persisted projection object.

Each client builds its own rendering state from those projected items.
It does not interpret private history, decide which private fields are safe to disclose, or construct the engine's continuation state.
A projected API snapshot and a persisted private checkpoint therefore serve different purposes, even though both may summarize state at a position.

Neither derived view is an independent source of truth.
A derived projection's applied position cannot exceed the committed prefix it has actually consumed.
An optional projection index accelerates lookup but cannot be the sole authority for a fork boundary or attest facts beyond that prefix.
Summary fields are derived wherever possible; a checkpoint may cache them at its known position.

Compaction reduces model context, not stored history.
The engine uses the replacement conversation described by a compaction commit, while the adapter retains full earlier `CommittedHistory` for the session's lifetime.
Full history includes all admitted model-visible content and private continuation facts.
It does not include unbounded external artifacts that never entered the agent's context.

#### Authorized views and commit positions

A `Client` receives a display-safe `ClientProjection`, never private commit records.
Provider replay data, pending authorization internals, and other private state stay server-side, even when the client loads the full available history.

A *committed position* identifies a boundary in a session's ordered history.
The *head* is its latest committed position; C denotes the position selected for a particular load, which may be older than the current head by the time the load finishes.
A checkpoint reports P, the last position it includes, at or before the selected load position.
A *subscription cursor* tells the server which committed position the client has reached so it can deliver changes after that boundary.
A *record identity* identifies a particular committed transition for correlation and retry resolution; it is not interchangeable with its position in history.

The server reports C in the projected snapshot and serves later authorized committed changes after C in order, with no gap between snapshot and subscription.
If the cursor at C has expired, the client reloads a snapshot rather than claiming to have caught up.
Invalid cursors, cursors for the wrong history incarnation, and cursors ahead of the authoritative head produce an explicit error or reload outcome; they are never silently clamped into apparent continuity.
The server checks access to the snapshot and every subscription, including after reconnect.

The storage adapter assigns history positions and rejects commits from a writer that has lost ownership.
A *history incarnation* distinguishes distinct histories, rather than successive writers of the same history.
Ordering records, distinguishing history incarnations, and fencing successive writers are separate requirements; a monotonic counter may help with ordering, but neither timestamps nor position alone authorize a writer.
The representations of committed positions and cursors, record identities, history incarnation, and writer epochs remain interface choices.
This proposal does not assume that those values share one ID or encoding.

### Persistence layer

The engine and host submit private, versioned transitions through a commit boundary; the storage adapter assigns one session-wide position and rejects stale writers.
`CommittedHistory` supplies both continuation state and retained history; no second best-effort event log decides what is true.
A storage adapter may partition records across files, tables, or runs, but exposes one ordered view per `Session`.
Storage errors and ambiguous commits remain explicit caller outcomes; the coordinator resolves ambiguity against history before reporting confirmation.

Records cover session creation and controls, run boundaries, completed model turns, tool intents and outcomes, and compaction replacements.
A tool *request* records the model-requested tool call; an *intent* records the effective authorized tool call after trusted rewrites and readiness to dispatch it; a *result* records its established outcome or an explicit recovery outcome.
These are distinct transitions, with dispatch gated by intent and dependent model continuation gated by result.
Records include private continuation material where needed; record types are distinct from public event types.
Every writer, including title generation, approvals, compaction, and scheduler work, uses this boundary.
The adapter's backend head checks or fencing order competing replicas; an in-process mutex alone cannot.

#### Complete logical model inputs and transformation evidence

A `LogicalModelInput` is the complete effective logical request submitted to a model.
Private history retains that high-resolution input, not merely the conversation from which a request might later be assembled.
For each request it preserves the effective system prompt, ordered model-facing messages and media, tool declarations, model selection and behavior-affecting parameters, and relevant private provider continuation state.
Changed context may use durable references to immutable prior records, provided the complete logical request remains reconstructible.
Reconstruction must not depend on today's prompt template, hook configuration, or mutable external content.
Request-only transformations, including ones that do not modify stored conversation, must not silently escape capture.
Capture is required before the dependent model request or tool dispatch, not optional diagnostic logging.

The history also retains canonical model outputs, tool requests and results, and reported usage under their respective commit gates.
It preserves logical values rather than raw HTTP requests, transport framing, token chunks, or credentials.
An exact historical raw transport-request guarantee is outside scope; complete effective logical inputs are a settled requirement.
Normal secret scrubbing, disclosure policy, and result-withholding or destruction rules still apply.
A hook trace must never stash denied or held result bytes that the applicable policy withholds or destroys.
Retain safe evidence of that disposition; the [memory-only held-result contract](#memory-only-held-results) excludes unreleased payloads from durable capture.

Transformation evidence distinguishes original and effective logical values and records reconstructible hook input, parameters, output or failure, and meaningful provenance.
Stable hook identity, version, and configuration are retained when relevant to interpreting that evidence.
Evidence remains subject to the same scrubbing and withholding rules as canonical payloads, not an alternative route around them.
The [hook execution contract](#hook-execution-and-recovery) defines invocation identity, retries, and reuse of committed outputs.

High fidelity is the default and the first cutover does not trim history.
Future filtering or downsampling needs explicit retention semantics identifying what can no longer be reconstructed and how affected reads, forks, and cursors behave.
This is not permission to admit unbounded payloads: current large-payload bounds and the [payload admission contract](#payload-admission-and-external-artifacts) still apply.

#### Cache-stable reconstruction across reload

For unchanged history, effective configuration, and a compatible provider codec, session reload must preserve the cache-relevant representation of historical input.
Semantic equality of decoded JSON objects is insufficient: object-key order in serialized tool arguments can affect provider-prefix reuse.
This is a reconstruction requirement, not a promise that a provider cache entry exists or will hit.

- Preserve tool-argument object-key order, including nested objects. Do not decode retained arguments into unordered maps and re-encode them merely to normalize JSON.
- Preserve whitespace inside string values exactly; it is content, not JSON formatting.
- Preserve historical message and tool-call/result order, text, provider pairing tokens, provider item IDs, and opaque replay payloads under the relevant provider contract.
- Restore retained canonical IDs and provider bindings rather than minting replacements on load. Where replay-free history requires fallback pairing tokens, derive them deterministically from retained identities and occurrence context.
- Check rebuilt system prompts, tool definitions and their order, model settings, cache-key configuration, and cache-breakpoint placement separately. Reload must not introduce incidental changes; deliberate configuration, context, or provider changes are not equivalent-input reloads.

Logical-input fidelity does not impose raw HTTP byte equality, and successful JSON parsing does not establish cache equivalence.
Compaction of whitespace between JSON tokens and equivalent HTML escaping were tolerated on the two [tested proxy/model routes](session-commit-history-prior-art.md#provider-cache-stability-across-session-reload); that evidence does not authorize arbitrary normalization or establish the same tolerance for other routes.
Cache TTL, eviction, provider routing, model selection, and legitimate prefix changes can still prevent a hit even when reconstruction is stable.
Caching requirements never justify retaining credentials or unreleased result bytes, or bypassing authorization or disclosure checks.

#### Versioned provider replay and richer response items

The storage contract must retain a private, versioned `ProviderReplay` representation alongside canonical conversation facts without making it another authority.
It can represent an ordered sequence of provider output items, including kinds the canonical conversation does not render or execute.
One canonical assistant message need not equal one provider message, one text string, or one reasoning payload.
Item order, content boundaries, and occurrence-specific metadata must survive capture rather than being collapsed into last-wins fields.
The LLM boundary owns the representation, codecs, and compatibility decisions; storage preserves accepted opaque payloads and core does not interpret signed or encrypted contents.

Canonical `ToolCallID` is globally unique and engine-owned.
Provider pairing keys, output-item IDs, and engine tool-call identity are separate namespaces, even when a provider supplies similar-looking strings.
Provider metadata stays behind the LLM boundary rather than being added to executable domain `ToolCall` or `ToolResult` objects.
Bindings identify the producing session, model-request/turn occurrence, and relevant provider item so repeated provider keys in completed turns cannot alias another tool call or result.
Canonical identity must never be recovered by treating an old provider-ID key as an engine ID.

For example, an OpenAI Responses replay representation can preserve separate assistant message items and their phases, reasoning-item IDs bound to their exact encrypted payloads, and function-call item IDs distinct from both provider `call_id` and engine `ToolCallID`.
These are illustrative adapter-owned details, not a fixed core schema or an exhaustive list of response kinds.
A text-only client projection or event fold cannot reconstruct private metadata that was never captured.

Canonical accepted output, replay bindings, and any required replay payload share a consistent acceptance boundary.
Do not accept a canonical tool call with missing or mismatched required replay state, or dispatch from a partially accepted replay bundle.
Retain producer/version provenance and immutable ownership of payloads and bindings; no mutable aliases can change accepted history.
Restart, compaction, forks, and provider switches must preserve the relevant occurrence bindings and apply LLM-boundary compatibility and fork-transformation rules.
Unsupported or incompatible continuation-critical state fails explicitly; core cannot guess, silently drop required metadata, or replay it through an incompatible adapter.
Capture and disclosure policies still apply, and accepted opaque signed/encrypted values remain byte-exact rather than being interpreted or rewritten by core.

This reserves storage capacity and semantics for richer replay without authorizing fuller provider replay implementation in this change.
It does not adopt incremental acceptance of completed output items, partial-response failure/retry behavior, or WebSocket continuation; those need separate execution contracts.
Versioning and explicit compatibility must not silently reinterpret old IDs or payloads, and the no-legacy-migration cutover remains unchanged.

#### Core records and extensions

This section specifies the durable session-record extension point, one of the [capability-composition seams](implementation/components.md#extensions-compose-capabilities-through-different-seams).
Learning can use other extension points without implementing this protocol unless it introduces state that requires it.
An `Extension` is a trusted, registered interpreter for namespaced, versioned session records and their derived state.
Keep tool-specific lifecycles in these records rather than making each one a core event type.
The core envelope is the shared record structure, not a storage implementation or client payload.
It captures the information needed to enforce:

- Session commit order, record identity, and root ownership epoch.
- Run and tool-call correlation, continuation gates, and bounded payload admission.
- The distinction between private records and client-safe projections.

The engine and coordinator enforce domain transitions using that information; the adapter enforces atomic append and ownership checks.
Bounded causal references can correlate session, run, call, operation, child, and fork facts without turning the history into a graph ontology.
Each reference has explicit scope and, where copying changes identity, remapping semantics.
Unresolved or redacted references remain explicitly so rather than implying a missing fact exists or is readable.
References convey neither permissions nor authority to restart work.
Concrete reference formats remain interface choices.
An `Extension` record carries a stable namespaced type and schema version inside the envelope.
The storage adapter preserves its validated bytes without interpreting its domain semantics.
Storage, private context reconstruction, and display projection are separate roles; preserving a payload does not establish its execution meaning or authorize its disclosure.
The host registers the extension's continuation reducers and approved display projection.
Registration also supplies a versioned private-state reducer and checkpoint codec, which encodes and decodes materialized extension state, plus recovery, pause, resume, and cleanup capabilities when work can outlive a turn.

Continuation-relevant extension state must be reconstructible from committed records.
Reducers use retained facts, not the compacted model context as a substitute for history.
An extension checkpoint is a derived acceleration structure at P, never another writable authority.
Tests compare full replay with checkpoint-plus-suffix reconstruction and reject missing continuation-critical handlers before dependent execution.

##### Operation identity and lifecycle

`Subagent`, `Parallel`, and `Team` specialize a generic long-running tool invocation with optional child-session links.
Their rosters, joins, rounds, tasks, findings, and progress need not become permanent core storage concepts.

An `Operation` has a durable session-scoped identity, with initiating tool-call and run correlation where its entry point supplies them.
Every Team has a session owner, including the direct Team API; the contract must not invent an absent parent tool call to establish that ownership.
Session-independent teams are a separate feature.
The call may commit an immediate started-result; later operation progress and completion retain their own identities.
For a long-running external background job, use this split rather than keeping the launch tool call live until the process exits: later monitoring and cancellation are separate tool calls against the job identity.
Job completion does not replace the launch call's canonical started-result, and a completed launch does not establish that its driver's outstanding job obligations are safe to hand off.
This lets the envelope represent work that spans runs without implying that today's run-scoped background children do so.

The trusted `Extension` declares how run end, explicit cancellation, shutdown, owner loss, and restart affect its `Operation`.
The engine and coordinator record the resulting obligations without embedding the extension's private lifecycle.
Active background execution and writes remain under the root `OwnershipClaim`, even without a foreground `Run`.
A run ending does not prove the session is unloaded.
The host's scheduler independently produces sessions; it is not an event interpreter within session recovery.

##### Initial extension support and trust

The first cutover uses registered in-process handlers for the built-in `Subagent`, `Parallel`, and `Team` families, wired by host composition.
It ships this extension path rather than reserving an unused mechanism.
It requires no dynamic plugin loader and keeps engine core independent of host adapters.

Session-scoped `Operation` identity leaves room for cross-run work.
Cross-run background execution and completion-driven wakeups remain later capabilities, outside the first cutover.
A future networked extension transport needs a separate authenticated and fenced design.

Only trusted installed handlers may emit their registered type/version or declare optionality, obligations, and safe points.
Client input, model output, and tool-result text are data, never registration or authority.
Tests must exercise known, unknown optional, and missing continuation-critical extension types.

##### Compatibility and portable safety metadata

The client API identifies approved extension items by stable type and version without exposing private records or arbitrary opaque bytes.
Storage acceptance does not let unknown payloads bypass fencing, pairing, authorization, size limits, or parent-child disclosure rules.

An extension type is *continuation-critical* if the engine needs its meaning to reconstruct state or safely continue work.
A display-only type affects presentation but carries no such execution requirement.
The engine and registered reducers require a compatible interpreter for continuation-critical types; otherwise execution fails closed.
The server can project unknown display-only types as explicitly unavailable history instead of silently omitting them.
The host can still perform core-only management, such as safe status inspection and logical deletion, when continuation-critical state is unsupported.

The extension supplies portable safety metadata, which the coordinator commits with each transition:

- Type, version, and continuation-critical classification.
- Active obligations and child links.
- Whether the extension has reported a durable park point.

These markers are committed facts, not independent cached claims of safety.
A derived compatibility/dependency summary may identify the versions and artifacts required to interpret retained state.
It must be rebuildable from history at a known position, not a second authority or an implicit export/import feature.
A missing or incompatible handler cannot infer quiescence from opaque bytes or silently start dependent work.

For future graceful handoff, the host tells active extensions to stop admitting work and seek a quiescent point.
An extension parks or cancels under its registered policy and reports its committed safe position.
The root marker is valid only after every active child and extension reports one.

Definitive lease-loss notification lets an extension stop or diagnose work, but cannot override fencing or authorize a write.
Core still checks permissions and tool-call/result pairing.
Exact registration, compatibility, and wire encoding remain interface questions.

An unloaded extension is not evidence that external resources were cleaned up.
Future paired resources need a separate ownership and cleanup contract, not a plugin-management framework added implicitly here.

#### Ownership and writer fencing

For distributed deployments, provisionally keep the renewable `OwnershipClaim`, writer epoch, and expiry in the same transactional backend as `CommittedHistory`.
A replica can query the `OwnershipClaim` without taking ownership.
The backend publishes owner identity, epoch, expiry, and owner-reported `loading`, `ready`, or `draining` state together.
An explicit release reports unowned/unloaded.
An expired claim makes the last loaded report stale; it does not prove the former pod or an external call stopped.
A failed status read reports unknown rather than unowned.

The owning host acquires the claim through the storage adapter before loading continuation and reports ready only after loading.
It reports released/unloaded only after local execution stops and local tree state is unloaded, with a durable handoff marker for an active tree.

The host acquires and renews the claim on a coarse cadence.
On every append, the storage adapter atomically checks the expected head, writer epoch, **and unexpired claim** against the backend's ownership time.
A matching epoch after expiry is not permission to append.

If an owner reacquires a lapsed claim before anyone else, it atomically advances the epoch.
This is a new fenced acquisition, not a retroactive extension of authority.
The owner may reuse its loaded engine after verifying the durable head and reconciling uncertain in-flight transitions; a full rebuild is not inherently required.
A pre-expiry stale append must not inherit the new claim.

These checks must not require a Kubernetes API call for every token, event, or commit.
Backends without a shared transactional write boundary fail the distributed contract rather than falling back to best-effort checks.

##### Single-process local storage

A local JSON/JSONL adapter may declare an explicitly single-process mode with filesystem-appropriate head checks and write serialization instead of a cross-host lease.
Keep available interprocess protection, such as a stable per-session `flock`; do not drop the existing local writer guard.
This mode cannot serve as a shared multi-replica store.

The adapter detects failures and partial writes and declares what an accepted append survives.
Its operating mode must preserve the committed-cursor and read/replay checks.

##### Advisory Kubernetes signals

A deployment may retain the per-session Kubernetes Lease solely as an advisory mirror and watch signal, or disable it to avoid two apparent ownership sources.
The backend claim is always the sole authority for writer admission and session owner/status queries; a conflicting or stale Kubernetes holder cannot authorize work, block a valid backend owner, or become a public owner answer.
Failure to publish the advisory mirror is a diagnostic, not a second session-write gate.

Lease watches can report renewals, releases, and takeovers; a separate Pod watch can provide earlier evidence of pod loss.
A crashed pod stops renewing, but expiry itself emits no Lease update: a watcher must also track the deadline or use another signal.
A storage backend may supply an equivalent wakeup, but not all do.
Neither Kubernetes watches nor storage notifications grant ownership, prove an old external call stopped, or replace backend expiry and atomic fencing checks.
Notifications can be delayed or missed; recovery needs a bounded rescan.

In the first design, a watched Pod failure can wake a worker to inspect state and schedule takeover at claim expiry, but cannot advance the backend epoch before expiry.
A future accelerated takeover needs its own verified-death and fencing contract.

##### Delegation-tree ownership

An active delegation tree has one ownership domain, rooted at its parent `Session`.
The owner drives its children on the same replica under the tree's `OwnershipClaim`; independently transferring child ownership across replicas is outside this design.
Separately cooperating peer sessions need their own coordination contract.

The owning replica can report the tree as loaded, draining, or released.
A last-reported loaded state can outlive a crashed pod; the claim and its expiry determine whether takeover is allowed.

Ownership follows active work, not client connection count.
With no executing `Run` or session-scoped background `Operation`, the host may unload the tree and release the claim, perhaps after a bounded idle period.
A `PermissionAsk` is a durable request for an exact authorized decision controlling execution or disclosure associated with a run.
A pending execution ask with reconstructible continuation does not require an engine or lease held in RAM for hours; on an authorized verdict, the host acquires a claim and rehydrates that exact continuation.
A result-release ask instead depends on live memory-only bytes.
Unloading or owner movement cannot reconstruct those bytes from durable ask metadata; if the hold is lost, close the ask as unavailable without tool reexecution.
Independent runnable background work keeps the root claim active while another member waits for approval.
Read-only clients can remain attached to committed history without pinning the owner.

#### Storage access and commit coordination

The persistence interface commits and reads records, returning an accepted position.
Read-only adapters can inspect committed history and checkpoints without acquiring a writer claim or starting a run; learning and analytics may use that capability under their own authorized access policy and bounded reads.
The application exposes a read-only storage capability; deployments should give these consumers read-only backend credentials where supported.
Credential layout is not a new requirement on every storage backend.
Read access does not imply a public API for private records, permission to mutate, or permission to activate the session.

The storage port does not serialize protobuf or publish to clients.
The kernel coordinator orders the semantic transition and requests its commit; after acceptance, host projection/delivery builds an authorized update and may wake connected readers.
The outermost durable transaction must commit successfully before confirmed projection, acknowledgement, or notification; a nested savepoint or successful inner write is insufficient.
Extension handlers supply approved tool-specific detail; the host remains responsible for caller authorization and delivery.
Clients apply those projected updates to local rendering state, not to private continuation state.
Committed subscribers resume from the durable history, including across replicas.
A lost notification can delay delivery but cannot erase a commit or leave a gap; followers must be able to catch up without the original writer.
Bounded authoritative rescans recover missed wakeups, rather than depending on the writer to publish a notification again.
Catch-up and replay buffering have explicit byte and item-count bounds and backpressure; overflow produces an explicit reload or unavailability outcome, not silent loss.
A commit whose outcome is ambiguous must be resolved against storage before claiming an acknowledgement or publishing it as confirmed.

Each state transition has a stable identity recorded in history.
A mutation first acquires the tree's claim and loads committed state; the owner serializes admission and resolves repeated action identities from that state.
The backend atomically checks the expected head and writer epoch on append, so a stale writer cannot commit.
After an ambiguous append, the owner checks history for its identity before retrying.
A rebuildable lookup index can accelerate internal transition resolution without becoming a separate authority.
Repeating an accepted internal transition with the same identity and content resolves to its committed decision; conflicting reuse is rejected.
These checks make commit retries safe without pretending that an external tool executes exactly once.
The optional, bounded API receipt protocol below is a separate contract for client submissions, not a replacement for internal append identity.

Per-session learned permission grants are also derived from committed private approval decisions and their originating tool facts on load, not from an optional activity log.
An inherited approval fact on a fork does not become an active rule unless the fork's explicit inheritance decision creates one under current authority.

#### API command identity, receipts, and work observation

Synchronous committed mutations are the default.
Use asynchronous completion only when actual independently progressing work justifies it, as with a large fork copy.
Returning before work ends requires a durable way to track that work, not a new generic job for every command.

An `APIRequest` can opt into retry protection with a client-generated opaque token and its original creation timestamp.
Both are present or both absent; a partial pair is invalid.
A token is nonempty, at most 128 bytes, and compared exactly without normalization or prescribed UUID syntax.
Tokens share one namespace across mutation types and concurrent clients within the affected session; catalog mutations use the catalog's separate scope.
Each retry preserves the token, timestamp, and complete command, including method, target, parameters, and content.
Conflicting reuse is rejected, and every attempt is authorized independently: a token grants neither access nor control authority.

Without the pair, an accepted mutation still follows normal durable admission, but has no general retry receipt or replay guarantee.
After an ambiguous response the caller reconciles state rather than blindly resending.
Exact-ask resolution and expected-revision checks retain their own semantics independently of retry protection.
This protocol defines safe application retries; it does not assume that gRPC or reconnect logic automatically replays mutations.

A compact `RequestReceipt` records the admission decision, command fingerprint, original resolved target, and necessary durable result or work references.
It commits atomically with the decision it protects, without a separate receipt commit gate.
It is not a copy of the full response or transcript and never retains unreleased tool bytes or payload-bearing hook evidence.
A valid retry returns that original decision; retrying session-current cancellation cannot resolve against a newly started run.
Later replacement, consumption, or cancellation of a steer does not rewrite the fact that its submission was accepted.
Accepted input and actual model consumption remain distinct facts.

##### Bounded retry scopes

Each session retains at most 256 receipts and accepts retry identities at most one hour old.
Capacity pressure can shorten this horizon; one hour is a maximum, not a guaranteed retry window.
Each `SessionCatalog` has a separate 256-receipt scope and cutoff for creation, fork admission, and deletion.
Catalog capacity belongs to the catalog rather than an individual caller, so another catalog's traffic cannot evict its receipts.
A busy client can shorten the horizon for other clients in the same scope; clients need not coordinate a sequence.

Each scope has a durable, nondecreasing creation-time rejection cutoff.
Receipt eviction is ordered by request creation time, not LRU access, and advances that cutoff atomically with affected mutation and receipt decisions.
Reject all identities at or before the cutoff, including every timestamp tie and never-seen delayed requests.
A missing receipt behind the cutoff is expired, never a fresh mutation.
The cutoff is monotonic; client timestamps and network arrival order need not be.
Validate the age and a bounded future-clock allowance before admission so a client cannot unboundedly extend retention or advance the cutoff with a future timestamp.
The exact skew allowance and time representation remain interface decisions.

A new owner reconstructs the cutoff and every still-admissible receipt before accepting mutations.
Clock rollback, restart, and retention changes must not reopen a discarded interval.
Historical records can retain request evidence after active retry lookup expires, but neither a checkpoint nor a request lookup needs an unbounded receipt map or an arbitrary full-history scan.
Expiry is explicit and does not establish whether an earlier request executed; callers reconcile rather than automatically minting a replacement identity.
Work and its observation handle can outlive the initiating receipt.

Creation has no destination history yet, and deletion must remain retryable when that history is inaccessible.
Their receipt and cutoff decisions therefore belong to the catalog's lifecycle authority, with the same atomicity and bounded-window rules.
A protected creation retry returns the same destination while eligible; a deletion retry never recreates a session.
Minimal deletion tombstones have a separate identity-safety purpose and do not expire merely because a receipt does.

##### Common observation without universal operations

Request identity and work identity remain conceptually separate.
A synchronous mutation returns its typed committed decision.
Genuinely asynchronous work additionally returns a `WorkReference` usable through common status, wait, and subscription conventions, regardless of whether the caller supplied a retry token.
Generic clients can distinguish pending and terminal state, typed progress and outcomes, structured errors, and expired or unavailable references without knowing each work type's implementation.
Authorization applies to every observation.
Exact method names, protobuf types, and waiting behavior remain interface work.

The observation contract projects existing authoritative lifecycles: run state, extension operation state, or catalog creation state.
Create a separate operation only when existing resource state cannot express genuinely independent work.
There is no mandatory enumerable operations collection or universal operation state machine coordinating all mutations.
Cancellation illustrates the distinction: its receipt establishes acceptance and signaling against a resolved run; observing that run establishes terminal settlement, not necessarily proven cessation of external effects.

The tool's invocation identity and its intent/result commit identities remain distinct from API request identity, work references, positions, cursors, and writer epochs.
An external tool may support its own idempotency key, but local request deduplication does not make external effects exactly once.
Checking or retrying a storage commit never reruns the tool.
The [API prior-art comparison](session-commit-history-prior-art.md#api-command-identity-and-asynchronous-work) explains the useful Google, Temporal, and repeatable-request patterns and their limits.

#### Checkpoints, replay, and missing history

A `ContinuationCheckpoint` accelerates loading but may lag the committed head.
To load at position C, the host reads a checkpoint at P through the adapter, where P is at or before C, and the engine's registered reducers apply every record in (P, C] in order.
The assembled continuation and summary facts must reflect C before execution uses them; the host derives the authorized client view at C separately.
The stored checkpoint still reports P, not C.

The host may publish checkpoints through the adapter independently of history appends.
A crash during checkpoint publication must not create or erase a committed change.
The loading/replay path detects missing or incompatible records and fails explicitly rather than presenting partial state.

A previously acknowledged commit or writer epoch disappearing is a storage contract failure, not a lagging checkpoint.
Stop session writes, loads, and confirmed client projections.
Never repair the loss by writing local memory over history or reusing an old position.
Recovery of lost committed history needs a separate explicit operator procedure.

Rebuilding from retained history must yield the same continuation state and historical facts.
Checkpointing reduces replay cost; it does not authorize discarding earlier records.
The first design does not trim `CommittedHistory`.
A later trimming decision must specify which facts become unavailable to clients, audit readers, and forks, and how old cursors fail.

#### Session snapshots and historical runs

Keep the `Session` summary small: identity, current metadata, derived summary facts, committed head, and at most a reference or index into history and its `ContinuationCheckpoint`.
It contains no transcript or second authoritative conversation copy.

The separately stored checkpoint materializes private state at its own position P, which may lag the head.
The summary may point to it, but must not substitute checkpoint position P for the committed head.
Run snapshots with stable run IDs are one possible storage layout; the logical model need not match that layout.

The engine loads current continuation state by applying committed records since the checkpoint; it need not load the full pre-compaction conversation.
Older history remains retained.

Run boundaries alone do not bound size.
A single run can contain many turns and large tool results, while compaction can happen within a run and metadata changes can happen outside one.
Run snapshots therefore need bounded detail pages or references, and session-level facts still need their own home.
The existing [API surface](../../architecture/api-surface.md) distinguishes run-scoped controls from session-scoped reads; neither run identity nor session identity is a substitute for the session-wide commit position.

A compaction generation could later label which model-continuation checkpoint is current and which retained display history predates it.
A future client could defer loading older generations, reporting what remains available without claiming its partial view is the entire transcript.
The current model context, pending work, and unknown tool outcomes must be recoverable without loading old display history.
A generation marks a history boundary; it does not replace commit ordering or authorize deletion of retained history.

#### Session catalog

A `SessionCatalog` is a stably identified ownership and authorization scope for session listing and lifecycle decisions.
Each session belongs to one catalog, and each catalog uses exactly one configured `StorageProvider`; several catalogs can use the same provider.
Catalog identity survives connection, replica, and display-name changes.
The initial arrangement is one catalog per user within a storage/service authority, not a global registry spanning installations.
Multiple catalogs and shared membership are extension points; federation across storage providers is not part of this design.

The host exposes bounded, paged, authorized listing and exact creation/status lookup without activating sessions.
A catalog is not a transcript or another continuation authority.
Its strict creation, publication, deletion, receipt, and cutoff decisions are distinct from derived listing metadata.
Its separate bounded retry budget prevents another catalog's traffic from evicting receipts or advancing its cutoff; this is isolation of retry state, not complete shared-resource DoS protection.
The host projects display-safe entries from existing trusted `SessionKind` and kind-specific `SessionRelationship` values.
The storage adapter provides the listing and lifecycle-decision capabilities backing that API.
Scheduled fires show their schedule relation and fire-session identity.
Main sessions, delegated children, and other kinds are not all human-initiated.
Do not add a second generic origin field now; future external triggers and schedule-group queries can extend the catalog later.

The host's catalog/reconciliation path maintains derived display metadata, which may lag a committed change.
A future search index would be another authorized derived view within that catalog, not a second lifecycle authority or a reason to add multi-storage catalogs.
Each projection identifies the position it reflects, and missed updates converge through replay or reconciliation.
`CommittedHistory`, not a stale list entry, answers authoritative session reads.

Creation identity, ready/failed publication, logical deletion, and writer claims remain strict decisions.
The host acknowledges deletion only after the adapter durably suppresses the entry and fences further writes.
A *tombstone* is the minimal retained identity marker preventing retries or ID reuse from resurrecting a deleted session.
The host and adapter honor that deletion gate on every list, exact lookup, and history read even if display metadata lags.
If an adapter cannot prove the deletion gate for a catalog read, it fails that read rather than exposing a stale title.
The exact atomic or ordered storage primitive remains an interface question.

Catalog notifications are best-effort refresh hints, not a second durable history.
Clients resync through bounded queries after missed updates or reconnect; storage need not provide a watch.
User-level settings and state remain a separate future design.

#### Forks and retained history

A *fork* is a new independent session initialized from an eligible retained source prefix, not a transfer of source execution.
The host admits a first-cutover head fork only from an authorized, inactive main `Session`, at its latest eligible committed continuation boundary; the destination becomes a new idle session.
The host's copy worker uses the adapter to copy the entire retained prefix through that boundary, including pre-compaction facts, subject to the fork transformation below.
It must not manufacture a "since last compaction" cutoff.

Forking at an older completed main-run boundary is future work, including when the source has resumed execution.
Such a fork rebuilds continuation and display history as they stood at the selected position, not from today's snapshot.

The fork has a new identity and records its source `Session` and exact position as lineage.
Later source activity stays in the source; a fork neither pauses nor takes ownership of its active delegation tree.
The fork remains self-contained after source deletion.
An adapter may optimize the initial full-prefix copy only if retention and readability remain independent of the source.

##### Asynchronous creation

At admission, the host pins source position C and captures its metadata and fork policy under a short source-side boundary.
The adapter's durable catalog creation decision reserves a new session ID; the host returns promptly with that ID, a common work reference, and **creating** status rather than holding the request open for the entire copy.
The entry is visible in authorized session listings, and exact destination/status lookup reports `creating`, `ready`, or `failed` (or a non-disclosing `deleted` after deletion).
An optional retry token and original timestamp resolve to the same admitted destination while their catalog receipt remains eligible; an expired request is rejected, not executed as a second creation.
The destination/work identity remains usable independently of receipt expiry.

The copy is server-owned work after admission, not tied to the initiating client's connection; a disconnect cannot silently cancel it.
While immutable history through C is copied, the source may continue new runs.
A creating entry exposes only safe metadata and its status; it cannot be activated, forked, or read as complete history.

##### Ordering copy publication against deletion

Deleting the creating destination fences its copy and transitions it to a terminal deleted outcome; a late creator cannot publish it as ready.
Cleanup of staged bytes may follow asynchronously.

Publication does not require a transaction over the full source and destination histories.
After copying, the host requests a short conditional catalog decision from the adapter to order the fork against source deletion.
New source runs and other fork copies do not wait on this copy.
The source execution claim is neither held through the copy nor reacquired merely to finalize it.

Source deletion can proceed while forks are staged.
It wins against forks not yet finalized; those attempts fail and clean up if the source can no longer be read.
A fork whose final decision won first is independent, even if its ready status appears after the source deletion response.

##### Recovering an orphaned creation

A fork has a durable destination/creation identity independent of the optional client retry token.
Its admitted work and terminal status remain recoverable even after the API receipt expires.
A crashed creator may leave the catalog entry **creating** temporarily.
A slower host reconciler makes every ready publication conditional on the destination still being `creating`, even if the source-side fork decision already won.
Destination deletion wins over every later publication or cleanup retry; a completed source-side decision is not permission to resurrect a deleted destination.

Otherwise the reconciler completes publication for a finalized fork.
For an incomplete attempt, it waits until the creator can no longer finish before marking it **failed**.
It must distinguish a slow live copy from a dead one and never publish partial history.

Exact destination/work lookup after terminal failure reports that failure.
An eligible protected API retry returns its original admission decision and destination reference; an expired identity fails explicitly.
A new copy attempt requires a fresh submission rather than replaying the failed work.
A later reconciler could finish abandoned copies instead, but must preserve the same stable outcome contract.
Cleanup of incomplete staging is separate from its visible creation outcome.
The exact fencing, status, and cleanup mechanism belongs in the interface contract.

##### Fork transformation and inherited history

The host sends all fork/clone paths through one explicit transformation boundary before creating the new `Session`.
That boundary selects inherited historical and continuation facts, strips source execution state and effective authority by default, and marks inherited provenance.
Storage adapters must not bypass it by cloning a raw storage namespace.

Some payloads may remain byte-identical after validation.
This is still a semantic fork, not a replay of source ownership, approvals, or controls as new commands.
A fork inherits conversation, not execution: no pending asks, steers, live delegated work, scheduled timers, or future background tasks transfer.
If work eventually spans runs, exclude its runnable state and retain relevant historical facts without treating references as permission to restart it.

The source workspace and external artifacts are not rolled back to the selected point.
Placement and current authorization still need validation for the new session.
Historical rewind is a conversation fork, not an external-world snapshot.
Exact eligibility and inherited fields remain interface questions.

The fork copies approved parent-side delegation projections in the prefix, not child sessions.
A deeper child-transcript read from the fork reports unavailable; it does not inherit the source parent's access to a live child.
Deep cloning and point-in-time child references remain future choices.

##### Operational settings and permission grants

The server API owns fork inheritance semantics.
By default, a fork resolves its mode and operational settings from the installation's new-session defaults and verified caller.
It does not copy the source mode or session-learned grants implicitly, unlike today's source-mode copy.
Per-setting overrides and an "inherit compatible settings" preset let callers explore two paths without bulk approval prompts.

If the caller opts into learned grants, the server re-derives eligible rules from authoritative private source approval and tool facts.
It checks caller and placement compatibility under current policy, then commits new fork-local authority decisions.
Inherited historical approvals remain facts, not active grants.
A Deny or configured Ask still wins at execution.
Ineligible grants are omitted rather than triggering an approval ceremony during creation.

No option transfers pending asks, tool work, timers, or ownership.
Client bindings may offer convenient presets but cannot clone authority independently of the server.
Exact options and reporting of omitted grants remain interface questions.

#### Future same-session rewind

A rewind commits a new branch selection within the same `Session`.
It neither creates another session nor deletes later commits.
Eligible continuation points are **clean run boundaries** after fully settled runs, including failed or user-cancelled runs with valid tool-call/result pairing.
An arbitrary message or mid-run steer is not eligible.
The current delegation tree must also be settled, with no pending approval or live child work to strand.
The client offers only eligible boundaries.

The next model request sees the selected conversation path.
Rewind does not restore old permissions, unspend reported usage, roll back files or external state, or restart timers and background work.
External side effects on an abandoned path may still have occurred.

Commit order and positions remain linear and append-only, with one active continuation path.
Abandoned paths stay retained and inspectable, for example as collapsed client branches.
Their tips remain identifiable so a future reactivation design need not reconstruct deleted data.
Rewind controls and branch projection are outside the initial committed-history delivery.

### Client API

The server builds a `ClientProjection` from committed history and returns authorized, display-safe session facts and timeline items.
Clients consume those projected items to maintain their own local display state; they do not interpret private records or their storage layout.
The snapshot, history pages, and committed subscription share one session-wide position.
Provisional updates advertise their weaker status separately.

Conceptually, the read API provides:

- A session summary and stable paged history at its position.
- A committed subscription after that position.
- A provisional channel for low-latency observations and cumulative commit acknowledgements.

Provisional and committed channels may share a transport, but retain distinct status and cursor semantics.
These responsibilities do not specify RPC names or physical tables.

#### Observation and activation

**Observe** is read-only: the server serves summaries, paged history, or committed subscriptions without claiming ownership, loading an engine, or starting execution.
On an authorized **interactive attach**, the host may acquire the tree's `OwnershipClaim` and load continuation to drive eligible work, even without a new prompt.
Attach does not change permission mode or confer write authority by itself.
An approval or other authorized mutation can enter this same activation path without a separate attach round trip.

A connection or committed subscription alone does not pin an owner.
A provisional stream exists only while there is live owned work and may end when work parks or completes.
Historical reads never activate execution as a side effect.
Exact API spelling remains open.

#### Complete history through a pinned position

On initial reload, the client loads the complete authorized display history through bounded pages, not one unbounded response.
The server constructs each page for one immutable authorized view pinned at C for that load.
Page tokens identify where to continue within that view, not where to resume live events, and the server binds them to the authorized session and view.
Only a successful final page attests that authorized history through C is complete.

If a page expires, fails, or no longer matches the view, the client discards its incomplete load and starts a fresh snapshot.
A material authorization change invalidates the view and its tokens.
The server rechecks access on later page and subscription reads; a token is not a grant.
Initial policy may be coarse, with finer caller-specific filtering later.
A disconnection alone does not revoke access, and a silent cutoff must not appear complete.

After loading all pages, subscribe after C to follow committed changes.
Changes committed during loading must remain resumable from C; otherwise the server reports expiration and the client reloads.
Page growing collections, including run inventories and conversation/detail records.
A large individual tool result or media item needs bounded content reads or an explicit size failure, not an oversized page.
A later client can load older pages on demand under this same contract.

#### Payload admission and external artifacts

Bound canonical session payloads before they reach the model or `CommittedHistory`.
Reject oversized submitted media before accepting input.
For oversized tool output, apply an explicit limited-result contract and commit exactly what the model receives, with a truncation or external-reference indicator.
Never give the model full output while retaining a shorter version as though it were the same result.

A future spillover adapter may keep large artifacts outside history.
History would retain the producing command and parameters, the admitted excerpt seen by the model, and any authorized retrieval reference.
It would not claim to retain the entire artifact.
External artifacts need their own storage, access, and lifetime contract; spillover is outside this proposal.

#### Multiple interactive clients

Multiple authorized `Client` callers may attach to the same server-owned `Run` and live feed.
There is no exclusive controller.
Every action is authorized independently, and the owner orders conflicting actions against committed session state.
Reading history grants neither control rights nor execution activation.

Disconnecting a client does not transfer control or cancel the run.
This differs from the current prompt-started `Converse` stream described in the [API surface](../../architecture/api-surface.md), whose relay cancels on disconnect.

Every producer shares run admission, including user prompts and future extension-triggered work.
Only one eligible `Run` starts.
A competing prompt receives a conflict identifying the active run; the server neither queues it silently nor reinterprets it as a steer.

A client may hide that race by issuing a separately authorized, exact-run steer.
That steer can fail stale if the named run has ended.
Admission failure must not erase a committed background completion.

#### Source-scoped steers

A steer is submitted input for the agent, distinct from unsent client-local composer text.
A `SteerSlot` holds one source's replaceable pending input, not an accumulating instruction queue.
A `SteerRevision` identifies a committed version of that slot's content and disposition.
Each `Session` has one shared user slot and one independent slot per registered `Extension`.
The extension itself is the initial coalescing key; there are no extension-defined subkeys.
All slots support the same set, replace, retract, and revision-specific consumption semantics.
Updating one slot never changes another.

An `Extension` uses the same authorized session-action semantics as a client but controls only its own slot.
Submitting a steer grants no implicit permission to answer asks or bypass execution gates.

One steer operation updates a source slot with an optional exact-run constraint; it does not introduce separate steer entities or source-specific APIs.
Without that constraint, input requests eligible execution: pending input for an active or awaiting `Run`, or a new run when idle under common admission rules.
With the constraint, admission and consumption target only the named run and never silently promote to another.
Source and targeting are independent: clients and extensions use the same operation, and either supplies a run constraint only when its input depends on that run.
The existing `Service.Steer` and `SteerRun` distinction motivates these behaviors, but their current method split is not prescribed for the new API.
Admission and the terminal race must not lose accepted input; recovery and cancellation dispositions are described below.

##### Delivery and provenance

A *safe model-input boundary* is a point before the next model request where the engine can incorporate pending input without interrupting an in-flight request or bypassing required approvals and committed outcomes.
The engine incorporates all pending slots **together** at that boundary, in deterministic order.
Through the coordinator's commit path, it commits the admitted input and consumption of each delivered `SteerRevision` before sending the request to the model.

A replacement admitted after the delivery decision remains pending for a later boundary.
Consuming an older revision cannot clear it.
The ordering algorithm and framing remain interface questions.

The server stamps provenance so the model can distinguish user input from a named extension, tool, or harness source.
Text cannot claim its own trusted source or gain instruction authority by claiming to come from the harness.
Non-user notifications use appropriate untrusted-data framing; attribution does not elevate them to system-prompt or permission authority.

##### Small hints, retrievable results

An extension steer contains a size-capped hint with enough `Operation` identity, status, and context to choose a retrieval tool.
Full results stay in the extension's canonical committed state.
The agent calls an authorized tool to retrieve them and explore further.

Each extension has only one pending slot, so a replacement hint must cover outstanding work, for example by pointing to a tool that lists ready results.
Coalescing a hint must not erase operation outcomes.
Future cross-run background work and completion-triggered runs use these semantics; they remain outside the first cutover.

#### Shared pending controls, not client drafts

All interactive clients share the user `SteerSlot`.
Submitted pending content, revision, and disposition are committed server state.
Authorized reload and subscription projections reflect that state to every client, not just the submitting connection.
A user can submit on one client and inspect, modify, or retract the pending steer on another.

Updates and retractions name the expected slot revision.
A stale client receives a conflict rather than overwriting another client's edit.
Conflict payloads remain interface questions; retry receipts follow the bounded optional command protocol above.

Unsent composer text, focus, filters, and other presentation state stay client-local.
Draft persistence and synchronization are outside this design.
Retracting the user slot never retracts an extension slot, or vice versa.

##### Shared approval resolution

The server reflects pending `PermissionAsk` state and committed resolutions to authorized clients.
The host validates each response and submits its resolution through the same ordered commit path.
The first valid, authorized **committed** resolution of an exact ask wins.
Later conflicting answers receive the authoritative already-resolved decision.
They cannot reverse authorization that may already have allowed execution.

A retry-protected winning action returns its retained receipt while eligible; expired retries fail explicitly, and exact-ask resolution prevents a later answer from reversing the committed decision.
Unsubmitted responses stay client-local, and a connection reserves no exclusive responder role.

#### One client timeline

The server exposes the authorized `ClientProjection` as one session-ordered timeline, not separate run histories for the client to merge.
Each committed item has a place in `CommittedHistory` order.
Run-associated items carry stable run IDs for grouping; session-wide changes share that order.

Run snapshots may organize storage, but the public read contract hides that partitioning.
Current metadata and the `ContinuationCheckpoint` remain separate from the display timeline.

Prefer one client-safe item schema for historical pages and committed updates.
The server applies the same authorized projection rules to both delivery paths; clients apply the resulting items through one local display-update path.
That preserves item identity, order, and revision across reload without giving clients private log records or disclosure responsibility.

Provisional streaming uses separate status and correlation identity until commitment establishes an authoritative item and position.
Aggregate usage and permission mode may still appear as snapshot fields, avoiding the need to infer current authority from old activity.
Payload families and provisional reconciliation remain interface questions.

#### What reload restores

A new client process must recover durable, client-visible session facts:

- Conversation and completed human-visible reasoning summaries, separate from provider-private replay material.
- Tool calls and committed outcomes.
- Delegation inventories and bounded activity.
- Tool-reported changed-file evidence, confirmed metadata, and reported usage.

Changed-file history distinguishes reported effects from requested-but-unconfirmed edits.
An Edit/Write target alone is intent, not a successful change; Shell/external modifications are not exhaustively inferred, and current workspace/Git diff is a separate read.
Execution environments own artifact/workspace lifetime under their own lifecycle contracts.
Retained selection facts or artifact handles do not extend that lifetime; expired or unavailable resources remain explicit without erasing history or authorizing recreation.

Large details use bounded reads under the same privacy and authorization rules.
[Issue #2114](https://github.com/stacklok/mecatl/issues/2114) inventories each item and names exceptions; a best-effort feed must not decide what survives.
Reasoning representation belongs with [issue #2079](https://github.com/stacklok/mecatl/issues/2079).

##### Parent-owned delegation facts

The parent's `CommittedHistory` retains the full **admitted** `Subagent` request and canonical final result, including the prompt and response shown in today's `/toolcalls` detail.
Enforce size limits before dispatching the request or committing and showing the result to the model.
A display preview must never masquerade as the complete canonical result.
Fork and reload use these parent-owned facts without duplicate full-prompt/full-result copies in `SubagentPayload`.

The parent timeline also retains stable call and child links, concrete model, status, reported usage, and selected bounded child tool-call/result activity.
These facts restore `Subagent`, `Parallel`, and `Team` views on a fresh load.
The current `SubagentPayload` start/tool/end taxonomy is a useful starting point.
The `Goal` field does not promise the full prompt, and its rune-capped `Text`/`Detail` previews are not promised to be secret-redacted.
`Parallel` and `Team` retain approved inventories, winner facts, bounded activity, tasks, and findings.

Parent-visible facts must reload without an optional child fetch.
The child's full transcript and private continuation stay in its own `Session`.

##### Deeper child access

The server resolves a parent-scoped detail request from a verified child relationship and checks policy for the caller, parent, child, and requested detail.
Possession of a child ID or access to the parent is not blanket access to the child.
Live views and reloaded views use the same policy.
Losing a client's in-memory fleet on reconnect must neither widen nor narrow access.

Recheck authority on every detail read, including after reconnect.
If policy denies access or the child cannot be read, preserve the parent summary and report detail unavailable.
Do not invent a complete child history.
The policy for deeper content and its disclosure limits remain interface questions.

##### Reported usage and inherited attribution

Presentation state, including focus, filters, scroll position, and spinners, remains client-local.
Token deltas and transient progress are provisional and may be lost.
Retain provider-reported usage as committed facts tied to stable physical-attempt and run identities.
This includes reported usage from failed or retried attempts with no completed turn.
A completed model turn carries its own outcome and attribution, and commit retries must not count an attempt twice.

Per-turn, per-run, and cumulative session **reported usage** derive from recorded facts.
A checkpoint may cache sums at its position, but they are not independent authorities.
Clients reload confirmed usage and attribution without reconstructing them from transient streams.

Reporting is best effort: a provider may omit usage, or a process may die before committing it.
The API and UI must not describe the sum as guaranteed total spend or complete consumption.
Missing usage is unknown, not reported zero.

Forks preserve each copied physical attempt's original session and attempt identity as inherited provenance.
New fork attempts have fresh identities; the fork's own reported usage begins at zero.
Cross-session accounting counts inherited attempts once by origin, including through fork-of-fork lineage.
UI treatment of inherited versus new usage and a future `/usage` command remain separate decisions.

Session history owns usage from work executed as part of its runs, not every auxiliary operation that happens to inspect it.
Reflection and other work without a session writer need a separate durable state/accounting owner in a future design, with purpose/type and provenance adequate for a joined usage view.
Do not mutate the source session solely to attribute independent work; the exact auxiliary accounting contract is outside this proposal.

#### Delegation detail budget

"Bounded" has three distinct meanings:

- Which child fields may be disclosed to a parent.
- How large each preview may be.
- How much activity the parent's current view retains per child or member.

The live UI keeps twelve trace entries per lane, and forwarded text previews are capped at 200 runes.
Consecutive text deltas can coalesce into one growing entry, so neither limit alone caps retained bytes.
Control-character scrubbing is not secret redaction.
The server projection needs a field allowlist, per-field and aggregate byte limits, and an honest truncation indicator before persisting or serving parent-visible detail.

##### Retained history versus the current window

Retain every *approved* parent-visible committed item in parent history; the compact current view may show only the latest bounded window.
Older approved items remain available through the paged timeline.
For the initial projection, retain only child message and tool snippets already forwarded to the parent today, subject to explicit per-field and aggregate byte caps; do not add new child content fields.
They may contain sensitive text: making them mandatory, long-lived history increases retention, and no secret redaction is promised.
The same approved projection governs live and reload; a short live prefix does not license copying any other child-private content.
The exact existing-field mapping and budgets belong in the interface review.
A reusable redaction subsystem, if wanted across product surfaces, is a separate future design, not an implicit feature of this proposal.

##### Cross-history projection gaps

The extension folds provisional child message deltas in memory and supplies approved parent activity at stable child message/turn, tool intent/result, and terminal boundaries.
The coordinator commits that activity; the live typing animation need not survive reload.
Normally the child commits a settled fact through the coordinator, then the parent commits its bounded projection before that child advances.

A crash between the two commits can leave parent history missing a settled child item.
Recovery uses child facts for execution safety but never reconstructs or backfills missing parent snippets.
Read-only parent reload needs no child fetch.
Without a committed terminal parent projection, it shows **unresolved / activity unverified**, even if the child has finished.

Recovery later inspects committed child state and appends a parent-side activity-gap marker with stable child identity and bounded evidence before settling the parent view.
Reload and forks retain that gap rather than presenting a complete trace.
A projection gap is not an unknown tool outcome; tool certainty follows the child's intent/result evidence.

If a child committed its terminal result but the parent tool result is missing, recovery may commit a known parent result only from exact durable result bytes or a versioned deterministic derivation that reproduces them.
"Child completed" status or a bounded display summary is insufficient.
Without that evidence, close the parent call with an unknown-outcome result; never rerun the child merely to fill the gap.
The first cutover need not build a general recovery synthesizer.

##### Disclosure before publication

The host applies disclosure policy to extension-supplied child-to-parent detail before any parent commit or live publication.
That leaves room for a future opaque subagent: its private activity would stay in the child session, while only explicitly approved parent-visible facts and the tool's intended result cross to the parent.
A read-time filter cannot make content already stored in a parent's retained history private retroactively.
This proposal adds no opaque mode or new configuration; it preserves the boundary where such a policy could be applied later.

#### Live delivery and acknowledgement

The engine produces provisional model text and reasoning summaries as they arrive; the server streams them to clients without a record per token.
If the host fails before committing a model response, reload may lose that entire response even when the client saw it live.
The engine's completed assistant turn crosses the coordinator's commit boundary once it has an accepted result.
Tool progress and early result previews can also be provisional; dispatch and outcomes have separate durable boundaries below.

Provisional updates have correlation identities for amendment or discard, not resume cursors.
After storage accepts a change, the server advances a cumulative acknowledgement such as **"persisted through C"**.
Only a committed position is a durable subscription cursor.
If provisional output differs from the committed result, the client reconciles to the `ClientProjection`.

##### Reconnecting after owner changes

An ownership change ends the old live stream, including one forwarded through a non-owner.
The transport connection itself does not migrate between replicas.
Reconnect through any entry replica from the last committed position, reloading a snapshot if that cursor is invalid.
Reconcile or discard provisional output.
For successful planned handoff between compatible replicas, client reconnection, catch-up, and eligible command reconciliation happen automatically; the user need not reload, resubmit the prompt, or manually resume the run.
This is user-visible continuity, not a promise of an uninterrupted connection or zero pause.

A clean handoff may report a typed reconnect outcome; a crash may only break the transport.
Neither proves the `Run` stopped.
If the connection drops before acknowledgement, reload committed state rather than assuming provisional text survived.

##### What acknowledgement guarantees

An acknowledgement means the authoritative adapter accepted the record under its declared durability contract, allowing recovery after a mecatl process restart.
The adapter explicitly declares whether accepted commits survive adapter-process failure, OS crash, and backend failover, rather than treating those as equivalent guarantees.
Queueing a write in memory or sending bytes to the client is not commitment.
Storage-node failure guarantees depend on the backend deployment, not one blanket protocol promise.
An adapter cannot report volatile writes as committed without disclosing that contract.

Provisional delivery never grants permission or establishes a certain tool outcome without evidence.
This distinction preserves low-latency streaming while keeping `CommittedHistory` authoritative.

### Recovery and execution safety

#### Distributed recovery and evidence

A committed run-start record without completion establishes only an **unresolved** outcome.
A lost connection does not prove the run stopped; another server may own and drive it.
Report transport state separately from server-confirmed run state:

- A current owner.
- A fenced former owner.
- Ownership that cannot yet be determined.

The client may present these facts compactly, but cannot infer completion or failure from a missing event.

The kernel coordinator commits an interrupted-run marker only after establishing that no writer can continue the old `Run`.
That marker explains why streamed model text is absent from recovered conversation.
Even after fencing, a started external tool may have had side effects; its outcome remains unknown until verified.
If ownership or storage is unavailable, report uncertainty rather than claiming the run ended.
Positions, writer identities, and bounded recovery status must remain meaningful across replica reconnects.

When the host observes a newer backend writer epoch, the old writer is definitively fenced: the host stops admitting work and cancels its local engine run.
The host may inspect a Kubernetes Lease for diagnosis, but it cannot authorize an old-epoch append.
A fresh owning host acquires the current backend claim through the adapter and reloads committed state using engine and extension reducers.
This coordination stays server-side, without a Kubernetes dependency in engine core.

##### Renewal uncertainty and limited speculation

A failed or timed-out claim renewal is uncertainty, not permission to start another tool.
Started model requests and tool calls may settle, but only an epoch-checked commit confirms their outcomes.

One narrow exception permits a next LLM request whose complete logical input and continuation were committed while ownership was valid.
It reads that already committed request without adding new uncommitted transformations or hook outputs.
Its streamed output stays provisional until the model response commits under a valid epoch and expected head.
Before that commit, it cannot trigger a tool, a second model request, or confirmed usage.
Discard the response if takeover wins.
This exception may duplicate provider cost; it is not a generally read-only operation.
Never build a new model request from uncommitted state.

A successful ordinary append with the current epoch confirms that operation's write authority.
If storage cannot confirm the append, stop dependent work.
After renewal confirms the same epoch, the owner reads the head, reconciles in-flight outcomes, and resumes in place.
A newer epoch is a hard stop.

Even apparently read-only tools require committed intent before dispatch and committed results before continuation.
A future side-effect-free read exception needs an explicit purity and privacy contract.
Tool names or declared read-only classification alone cannot justify speculative calls across an uncertain fence.

##### First-cutover crash settlement

Crash takeover and agent continuation are separate decisions.
A crashed active `Run` is **interrupted**, not resumed under the same run identity in the first cutover.
After fencing the former writer, the kernel coordinator uses committed request, intent, and result evidence to repair tool pairing, with registered extension handlers for tool-specific state.
Requested calls with no intent were not dispatched as tools; intent without an established result has unknown outcome.
Pre-intent hooks may nevertheless have caused external effects, as described in the [hook execution contract](#hook-execution-and-recovery).
No ordinary tool is automatically retried.

A later authorized interactive attach may start a new `Run` from committed conversation.
If only an uncommitted LLM response was lost, the server issues a new model attempt from the last committed input on that attach, regardless of the initiating UI.
This is allowed only when no tool intent, pending steer, or approval requires a decision.
The new response may replace the discarded provisional response and differ from it; the old attempt's usage may be unknown.
Read-only inspection never triggers the request.

Pending steers and uncertain tools need explicit post-crash recovery decisions before new dependent work.
No-client automatic agent continuation is deferred.
A durable pending execution `PermissionAsk` remains resolvable by explicit verdict rather than becoming an interrupted run.
A result-release ask is different: its unreleased bytes are memory-only, so process loss closes it as unavailable under the original tool-call identity without rerunning the tool.

The host's generic orphan-settlement worker covers every session kind, including scheduled fires and delegated children.
It neither restarts them nor knows schedule retry policy.
The host's scheduler separately reconciles due claims and fire records from settled session state, without a competing direct session settlement path.
An unobserved crashed fire does not replay on client activation; later due fires proceed.
Opted-in one-shot retry remains schedule policy, not a general same-run crash-resume guarantee.

##### Later same-run continuation policy

A later same-run crash-resume feature may store a per-root-session continuation policy initialized from a deployment-wide default.
Delegation would follow the root's policy; changing the default would not rewrite existing sessions.
Its conservative default is to park the recovered tree, while an opt-in could let the agent continue with an explicit unknown-outcome result in model-visible context.
A separate authorized resume would continue the same run after fencing and reconciliation, never rerun the uncertain tool.
This policy, toggle, and same-run resume action do **not** ship in the first cutover.
Who may change a session's policy and when remain later decisions; do not create a client toggle now.

#### Durable steer recovery

The kernel coordinator commits a steer's admission to the source `SteerSlot`, including stable action identity and `SteerRevision`, before acknowledging it.
Content, replacement, retraction, and consumption survive crash or handoff.
Acceptance does not mean the model has received the input.

In ordinary execution, commit pending revisions' insertion at the next eligible input boundary, after any required approval verdict.
Clients reconcile pending controls against that committed insertion rather than an inbox echo.

In the first cutover, an interrupted run leaves unconsumed steers pending for explicit recovery.
The authorized caller chooses a new follow-up run with the input or retracts it.
The server neither silently promotes nor discards accepted text, nor requires resubmission.
A later same-run resume could consume it at the resumed run's eligible boundary.
Stable internal action and revision identities prevent recovery from inserting the same accepted input twice; optional API retries have the separate bounded receipt guarantee above.
This durability differs from today's restart-lost in-memory inbox.

##### Terminal-race disposition

The server resolves the terminal race; clients need not wait for a turn to finish before sending.
An active `Run` may consume an accepted steer if it continues.
If it ends first, the server commits an explicit disposition:

- For an unqualified steer on normal completion, promote pending input to one follow-up run rather than asking for resubmission.
- For input with an exact-run constraint whose target completes normally before consumption, commit **not delivered: target ended**, retain the accepted text as history, and clear runnable pending input without promotion.
- An interrupted first-phase run leaves input pending for explicit recovery.
- A future same-run crash-parked run is not terminal and cannot authorize promotion.

The server may reject a steer before accepting it.
After acknowledging durable acceptance, it cannot return responsibility to the client merely by echoing text in a terminal response.
Order the recipient-run or pending-state decision at the committed session head and make it replayable by message identity.
A retry-protected submission cannot create a second follow-up while eligible; expiration rejects the old request rather than promoting it as fresh input.

##### User cancellation and failed runs

The ordinary Cancel action is session-scoped: the owner resolves the active run when it orders the command, without requiring a client reload or a fresh run ID.
A follow-up admitted before that boundary is the cancellation target; if cancellation wins first, pending steers cannot promote it.
Explicitly run-qualified cancellation remains exact and never retargets a successor.
For a retry-protected cancellation, the receipt preserves the originally resolved target rather than looking up whichever run is current on retry.

On observing cancellation, promptly close local execution admission and signal active work, descendants, and extensions.
Local interruption must not wait for a storage write or ordinary batching delay; confirmed acceptance and cancellation disposition still require durable commitment.
Cancel pending, undelivered steers from every source, and fence late extension notifications from restarting the canceled work.
Commit those dispositions together so a crash cannot revive pending input contrary to accepted cancellation.
Extensions receive the signal and stop admission/scheduling; recorded operation outcomes remain history, not runnable hints.
A steer already inserted into committed conversation cannot be canceled retroactively.
An authorized fresh submission ordered after cancellation may request new execution once bounded settlement allows admission.
This first-cutover contract adds no independent cross-run background work to cancel.

Cancellation acknowledgement means accepted and signaled, not that every process or external effect has stopped.
Tools need not implement in-flight interruption; a short invocation can finish normally after the signal, with its execution evidence and permitted output preserved through normal settlement.
Lack of interruption support does not bypass pre-admission cancellation or bounded shutdown, and a deadline does not prove the external work stopped.
Expose canceling separately from terminal settlement through the common work-observation contract.
Finish bounded execution shutdown and [result settlement](#result-settlement-after-cancellation) before admitting a new run, but do not wait for optional result-release approval.
At the deadline, record unresolved external activity honestly; cancellation neither proves rollback nor makes an uncertain ordinary tool safe to repeat.
The client should surface unresolved activity before later work can conflict with it.
Exact deadlines remain interface/operational choices.

Internal shutdown or lease loss is not user cancellation.
Preserve unconsumed input for fenced crash recovery and explicit follow-up-or-retract decisions.
If a run commits a failed terminal outcome before consuming a steer, also preserve the input rather than promoting it automatically.
The caller can inspect the failure and choose a follow-up or retract it without resubmitting the accepted text.
Retracting one source slot alone still does not cancel other slots or execution.

No-client autonomous takeover and continuation remain later capabilities.
An unobserved interrupted run waits for authorized client activation or recovery in the first cutover.

#### Routing to the owning replica

A non-owning host replica receiving a mutation or provisional-stream request queries the authoritative `OwnershipClaim` through the storage adapter and forwards over authenticated internal transport.
The owning host rechecks caller authority, tree identity, and current epoch; ingress affinity metadata grants no authority.
Forward at most one hop, without loops or exposing private pod addresses.

On handoff or a stale owner hint, refresh ownership.
An ambiguous retry-protected mutation retains its token and original timestamp so an eligible retry resolves against the original decision.
An expired identity fails explicitly; an unprotected mutation requires state reconciliation rather than automatic replay.
If ownership or routing cannot be established, return unavailability rather than invented success; replay safety depends on the command contract, not the transport status alone.
The client keeps its committed cursor and any eligible protected request across reconnect.

Affinity can reduce forwarding but is not required for correctness.
Any authorized replica can serve durable snapshots and committed catch-up reads from shared history.
An owner-aware gateway could later avoid forwarding; it is not required here.

#### Compatible-replica active-tree handoff (later phase)

Planned upgrades and other maintenance should be able to replace a replica without requiring users to restart or manually resume their work.
A successful graceful handoff preserves the same run, delegation tree, committed conversation, and pending execution-approval identities; a replica change alone must not cause repeated work or another prompt for an already-accepted verdict.
Clients reconnect and catch up automatically while a server-side successor discovers and resumes runnable work without waiting for a client to open the session.
A brief pause or connection replacement is compatible with this user-transparent behavior.
This contract applies to compatible replicas and a complete parked continuation, not arbitrary process loss, an unresolved external call, or the incompatible initial cutover.

The first cutover does not promise turn-boundary pause or seamless cross-replica continuation of an active delegation tree.
That capability is not implemented today.
It must still preserve or explicitly replace these existing guarantees:

- Bounded cancellation and join-before-release.
- Durable pending root-ask rehydration after takeover.
- Refusal of work before the ownership boundary.
- Crash-orphan repair with valid tool-call/result pairing.

A cancelled run or new client-started run is not continuation of the old tree.
The following future contract describes what history must accommodate, not what the first phase delivers.
The [prior-art handoff comparison](session-commit-history-prior-art.md#graceful-server-upgrade-and-ownership-handoff) identifies the closest partial references; no complete equivalent was established in the inspected paths.
A portable history is necessary for this contract, but tool/extension parking, fenced ownership transfer, and successor discovery require separate lifecycle coordination.

##### Parking the tree and closing approval admission

Drain is a committed pause, not the end of a `Run`.
The owning host marks the whole tree draining and admits no new model requests or tool calls in any member.
It coordinates engine and extension handlers so each member finishes active model requests and tool calls, commits outcomes and continuation, then parks at a turn boundary.

A pending execution `PermissionAsk` with reconstructible continuation is already parkable.
Its exact ask and decision state survive handoff without waiting for the human.
A release ask backed by memory-only bytes is not portable merely because its metadata committed; the later handoff protocol must explicitly settle or invalidate that hold before claiming a complete parked continuation.
The parent retains its delegation call and child links as pending; parking a child is not a tool result.
Delegated tools must support coordinated pause rather than tying logical call lifetime to one pod.

During drain, an approval may commit against its exact ask and decision identity, but cannot start dependent work on the draining owner.
The successor consumes the decision on resume; API retries use the same optional identity and remain subject to the bounded receipt/cutoff contract.

After closing approval admission for final handoff, the owner returns a typed reconnect response without accepting new verdicts.
The client keeps any eligible retry identity and queued verdict, reconnects through an entry replica, and retries only under the protected command contract; unprotected or expired requests require reconciliation.
The entry forwards to the successor after acquisition; bounded retries may report temporary unavailability until then.
Resolve ambiguous acceptance from history before claiming success or asking for a new verdict.

##### Publishing a complete handoff

The owner commits a root marker only after every descendant is parked and no LLM request or external tool call is in flight.
The marker contains the parent's parked continuation and names each descendant's exact committed parked position.
The owner then unloads local tree state and releases its exact `OwnershipClaim`.

Child histories may commit independently before the marker; no multi-history transaction is required.
The marker is the publication boundary.
A successor loads and verifies every named position before resuming any member; missing or incompatible positions fail closed, never as a partial handoff.
Approval admission closes before the marker so recorded positions cannot omit an accepted verdict.

A crash before the marker leaves an incomplete drain for fenced recovery.
A crash after the marker but before release leaves a complete handoff for takeover after claim expiry.
That marker establishes a safe parked continuation; it does not promise uninterrupted delivery or eliminate the wait for ownership expiry.
The handoff is durably discoverable by healthy replicas without a client opening the session.

##### Later autonomous takeover

Once autonomous takeover is enabled, a background successor claims an eligible tree with a new epoch and loads its complete parked state.
It resumes any runnable member, including a background child whose parent awaits approval or a member with a committed verdict.
An undecided ask parks only dependent continuation, not independent runnable members.

If no member can run, the tree remains unowned and unloaded until authorized client activation.
Reading history alone never claims it.
Claim acquisition and every parent or child commit share one fenced ownership domain, so competing workers cannot both drive the tree.
Child histories use that claim even when stored under separate child IDs; independently releasing child claims is not a substitute.

With autonomous takeover enabled, bounded scans or equivalent durable discovery prevent missed wakeups from stranding a released runnable tree.
Without that worker, the tree remains durably parked but cannot promise unattended rolling-upgrade continuation.
A partially parked tree is not a completed handoff.
Exact marker encoding and recovery checks remain later interface questions.

##### Shutdown deadlines

Drain is per root `Session` and tree; a slow tree does not delay other complete handoffs.
At the deadline, cancel and join remaining in-flight model requests and tool calls where possible.
If a tree cannot establish all outcomes and commit its marker before shutdown, do not release it as a graceful handoff.
Its claim expires, and a successor uses crash-style recovery with unknown external outcomes.
Other completed trees keep their graceful markers and immediate releases.

#### Hook execution and recovery

A `HookInvocation` is one logical hook execution anchored to a durable session action, model turn, or tool call.
Hooks execute **at least once** under authorized admission and recovery.
A crash can lose completion evidence after a hook has affected an external system.
Hooks integrating with external systems receive a stable logical invocation ID from the harness and use it for deduplication where their integration supports that.
This does not promise exactly-once external effects.

The ID may be derived from a durable session/tool-call or other action/turn anchor, the phase, stable registration identity, and occurrence within that anchored action.
The anchor may already exist in a committed tool request or admitted input; a separate durable hook-start transition is not required.
Before a side-effecting invocation that is to be recoverably retried, its anchored definition and inputs must be reconstructible, using immutable references where retention permits.
Sensitive unreleased result inputs are an explicit exception to durable capture: they remain memory-only, and process loss ends the ability to retry that invocation rather than justifying persistence.
If recovery cannot establish the same definition, input, and parameters, it must not reuse that identity for a guessed invocation.
Do not derive identity from an ephemeral execution index or the current configuration.
Retries of the same logical input and parameters keep the same ID; a changed invocation has a new ID.
The exact encoding and definition of occurrence remain interface questions.

Retain the reconstructible invocation definition, input, and parameters, plus completed output or failure and meaningful transformation provenance, subject to scrubbing and withholding policy.
During recovery, reuse a committed completed output rather than invoking the hook again.
Missing completion evidence permits an authorized retry with the same logical ID only while the same definition, input, and parameters remain reconstructible, even though prior external effects may exist.
Cancellation and recovery policy still control admission; missing evidence does not create an autonomous retry loop or first-cutover automatic run resume.
Hook retry is explicitly not permission to reexecute an uncertain ordinary tool.

Logical records and physical storage transactions are different boundaries.
Completed transformations may batch with the effective authorized tool intent or logical model request, but that commit must succeed before dependent tool dispatch or model execution.
An incomplete attempt can leave no committed output; an authorized retry repeats the hook with its stable ID only if its exact permitted inputs remain available.
The contract requires neither one write per hook or phase, nor separately persisted started/completed transitions for every attempt, nor a mandatory attempt-ID field.

Side-effecting hooks can run before the tool-intent gate.
No-intent evidence therefore establishes that the **tool** was not dispatched, not that hooks caused no effects.
The intent records the effective authorized tool call after trusted rewrites, not the model's originally requested tool call.
Execution evidence, hook-processing evidence, and disclosure disposition are separate facts.
A pre-dispatch hook failure does not prove that its own effects were absent; a post-execution hook failure must not erase an established tool outcome or make the tool appear safe to rerun.
The exact stage encoding and recovery transition APIs remain interface work.

##### Result settlement after cancellation

Cancellation stops new agent execution, but already-produced output still follows the normal post-tool hook and disclosure pipeline under a separate, bounded settlement context.
Hooks are trusted integrations expected to contain or process agent actions; this design does not add required/optional hook classifications or infer intent from arbitrary hook code.
Existing processing and guardrail failure semantics remain in force, and required disclosure checks cannot be bypassed by cancellation, error, or timeout.
Do not introduce a new autonomous result-release reviewer or extend the child-permission reviewer to approve held results.

Permitted canonical results commit under their original call identities and become available to a later explicitly started run.
Settlement never sends another model request or dispatches dependent work in the canceled run.
A release decision can remain actionable while its in-memory result survives, but clients choose whether to present post-cancel review controls; the server does not require a new interrupting prompt.
Starting a new run discards unreleased bytes, expires their release decisions, and fences late responses from every client.
Late processing from the old run cannot create a new actionable release ask after that boundary.

This deliberately differs from the inspected Codex cancellation path: Codex can interrupt post-tool processing and substitute an aborted result before dispatch reaches its terminal-outcome boundary.
The [pinned cancellation comparison](session-commit-history-prior-art.md#cancellation-and-result-settlement) records that evidence; bounded completion here is a chosen contract, not behavior established by that prior art.

##### Memory-only held results

Potentially sensitive unreleased output and its payload-bearing hook inputs, outputs, and traces remain in process memory.
The primary disclosure boundary is the agent; client feeds, diagnostics, audit, checkpoints, provider replay, and retry receipts must not become alternate persistence or disclosure paths.
Retain safe identity, failure, processing, and disposition evidence without retaining the held payload.
Approved content can enter ordinary retained history only after the normal processing and exact release requirements are satisfied.
There is no durable quarantine store or new retention-policy matrix in the first cutover.

Denial discards held bytes without undoing execution or automatically repeating the tool.
Process loss makes the held result unavailable even when local files or remote storage remain intact.
Recovery closes its release ask and commits a synthetic withheld/unavailable result under the original tool-call identity, retaining whatever execution evidence is established.
Unavailable output is not proof of tool failure, no effects, or safe retry.
Do not recover the bytes through hook traces or recreate them by reexecuting the tool.
A pending execution approval whose continuation is durably reconstructible remains a different case.


#### Tool calls and uncertain outcomes

The engine waits for the completed assistant turn and requested tool-call identities to commit through the coordinator before dispatching any call.
For each call, it commits the effective authorized intent after trusted rewrites, required transformation evidence, and private continuation immediately before dispatch.
The storage adapter checks ownership and expected head at those boundaries; it neither authorizes the tool nor dispatches it.
The model's batch is not a storage or execution transaction.
Permitted independent calls may overlap under the existing read-parallel/mutate-serial discipline.

The server's client-safe projection may show an admitted call in flight.
The engine submits each established result for commit under its original tool-call identity before dependent continuation.
The durable evidence distinguishes three states:

| Evidence | What the server can establish |
| --- | --- |
| Requested call, no committed intent | The protocol did not dispatch it. |
| Committed intent, no committed result | The call might have started. |
| Actionable durable execution ask | Approval is still pending; do not close the call as not-started. A release ask additionally requires its live memory-only payload and is closed unavailable if that payload was lost. |

After fencing the former writer and checking for a committed result, the kernel coordinator's recovery path closes an orphaned requested tool call with a harness-authored tool result, using extension handlers where its semantics require them:

- **Not-started** if no intent was committed: the dispatch gate prevented tool execution, though pre-intent hooks may have had effects.
- **Unknown-outcome** if intent committed but the outcome cannot be established: the tool may have had effects and may still be running.

Both retain the original tool-call identity and complete model-visible tool-call/result pairing.
Unknown outcome asserts neither failure nor absence of effects.
Recovery never automatically retries the tool.
The agent can then verify external state, retry an idempotent operation, or ask for help, under normal permissions and tool boundaries.
Until recovery commits the result, the session cannot feed an invented outcome to the model or start dependent work.

Correlation prevents duplicate client cards; it does not make external effects exactly once.
Which tool-start phases and post-crash facts are meaningful across implementations remains open.

#### Execution gates

The following boundaries distinguish fast observation from permission to advance the session.
The loop enforces execution gates, the kernel coordinator orders and submits transitions, and the storage adapter verifies and accepts their durable writes.
The server exposes only authorized projections of accepted facts; clients may display provisional observations but cannot use them to authorize work.
"Committed" means the authoritative adapter has accepted the record under its operating-mode and ownership contract; an in-process queue is not enough.

| Action | Required before proceeding | What can be provisional |
| --- | --- | --- |
| Start a run or send its next model request | Commit the accepted input, run identity, complete effective logical model input including required transformation evidence, and the continuation state the request uses. A later request also waits for the preceding turn's committed outcome. The narrow renewal-uncertainty exception uses only already committed input. | Stream the current model response without persisting each token. |
| Present an approval | Commit the pending ask and exact permitted continuation before treating approval as actionable. A result-release ask additionally requires its live memory-only payload; its durable metadata cannot restore the bytes after process loss. | Render an unconfirmed request as waiting, not as an exercisable grant. |
| Dispatch each tool call, including a delegated agent | Commit the completed assistant turn with its requested tool calls, then commit that tool call's effective authorized intent after trusted rewrites, identity, and required transformation evidence before its dispatch. If its intent write fails, do not dispatch it. | Show that the call is being prepared; progress and result previews after dispatch may be provisional. |
| Continue after a tool returns | Commit its permitted canonical result before feeding it to the model or claiming a confirmed outcome. Withheld bytes stay memory-only and cannot appear in previews. If result commitment is unresolved, stop dependent work until recovery establishes a known, withheld/unavailable, or unknown outcome for the same call. | Show only a disclosure-approved provisional preview while its result commit is pending. |
| Use compacted history or changed session controls | Commit the replacement conversation or control decision before using it for another model request or presenting it as confirmed. | Show that the operation is pending. |

A failed result write after dispatch cannot undo a tool's external side effect.
The server must stop dependent work and expose the unresolved call until it can establish a durable outcome.
If an append reports an ambiguous failure, the server must verify the committed position and call identity before deciding whether to retry; repeating a tool call is never a storage retry.

#### Shared conformance and fault-injection scenarios

Storage adapters and execution integrations share scenarios that establish the same commit, replay, and no-dependent-work guarantees across operating modes.
The scenarios cover append denied, a committed append whose acknowledgement is lost, an external effect whose result is lost, and commit or notification failure.
A denied or unresolved input/intent commit permits no dependent model request or tool dispatch; a missing result commit permits no dependent continuation.
A lost acknowledgement is resolved by durable identity without duplicating the transition, usage, or ordinary tool execution.
A lost notification converges through authoritative rescan and replay without the original writer.
Tests distinguish an effect with unknown outcome from a verified result, including possible pre-intent hook effects and reuse or authorized retry of hook transformations.
They also exercise bounded replay buffering, invalid cursors, full replay versus checkpoint suffixes, and missing continuation-critical handlers.

### Boundaries and costs

#### Breaking storage and protocol cutover

This deliberately breaks persistence and the client protocol rather than bumping the existing schema in place.
Use a distinct namespace for `CommittedHistory` with a versioned format/capability marker.
New adapters accept only that format and reject legacy snapshots or activity logs before reconstructing a `Session`.

A legacy session or mixed namespace is not an empty new session.
New writers must not rewrite it or treat a known legacy session ID as absent merely because namespaces differ.
Exact namespace, marker, and collision detection remain interface questions.

Legacy bytes remain untouched.
Apart from fail-closed presence detection, the new server and client do not read, expose, continue, or migrate old sessions.
This proposal does not authorize deleting old data.
A future offline export/import is a separate decision, not a first-release condition.

##### Service upgrade and client compatibility

Stop the old service before admitting new traffic: the initial rollout is teardown and upgrade, not mixed-version rolling deployment.
Keep the public endpoint, but check or negotiate the incompatible API version before any session read, subscription, or mutation.
Old/new client-server mismatches fail clearly without fallback to the other's snapshot, feed, or format.
Exact wire negotiation remains an interface question.

The old service uses bounded drain before teardown.
Remaining legacy runs may be interrupted and cannot resume on the new server.
Started external effects remain subject to the old protocol's uncertainty; the new history cannot protect them retroactively.
This cutover cost does not justify mutating or deleting legacy storage.
Graceful handoff between compatible new replicas is later runtime behavior, not migration of old runs through the cutover.

The clean break ends legacy-session continuation compatibility.
The committed subscription replaces best-effort activity watching as client reload authority.
Historical activity may remain a diagnostic view, but cannot attest session completeness.

#### Logical deletion and retained bytes

The host coordinates logical deletion; the adapter durably enforces the deletion gate on activation, writes, and reads.
Logical deletion is irreversible, independently of physical purge timing.
It fences activation and writes, prevents identity reuse, and denies new history reads even while records remain stored.
Deleting a root `Session` also logically disables delegated children by default.
No direct child read or resume bypasses that deletion, including during partial cleanup.

A root deletion boundary can make the entire tree inaccessible before physical child cleanup.
No atomic purge across child stores is required.
The adapter declares retention and purge timing, which may be stricter.

Catalog and history reads expose no deleted-session title, prompt, source hint, or descriptive metadata.
Only minimal, non-enumerable creation/destination and session identity tombstones may remain to prevent resurrection and identity reuse.
Exact identity lookup reports deleted without substantive data; eligible protected request retries use the catalog's bounded receipt contract.
Server-assigned creation/session identities are distinct from optional client retry tokens, and receipt expiry cannot remove the deletion gate.

A tombstone does not prove physical purge.
Retained bytes remain inaccessible through the logical API until the adapter's declared purge.
A future explicit detach could preserve a child before root deletion; it is outside this proposal.

Forks are independent peer sessions with lineage, not source-owned children.
Deleting a source neither disables nor deletes its existing forks or other separately created peers.

#### Commit budget and batching

`CommittedHistory` costs more than a best-effort activity log.
Track logical records, atomic commit gates, and physical backend writes separately: several records can share a commit, and one accepted append can require several physical writes.
The following budget makes the ordinary tool-call path explicit without fixing an adapter's storage layout.

| Stage | Durable boundary | Commit-gate budget |
| --- | --- | --- |
| Accept a completed model response | Requested tool calls, tool-call identities, and required provider replay. | One per model response, shared across its tool calls. |
| Prepare a recoverably retryable side-effecting hook | Reconstructible definition and permitted inputs, unless already durably anchored. | Zero if captured; otherwise an additional gate. Sensitive unreleased result inputs stay memory-only instead. |
| Admit dispatch | Effective authorized intent and completed pre-dispatch transformation evidence. | One per call before dispatch. |
| Execute the tool | No separate tool-start record is mandatory. | Zero. |
| Settle an ordinary result | Permitted canonical outcome and processing evidence. | One per call before dependent continuation. |
| Hold for release instead | Pending ask and safe disposition/recovery evidence, excluding held bytes. | One pending-ask gate, then one resolution/result gate; one more than ordinary settlement. |

Without additional approval or hook-input gates, a response requesting N tools therefore has a baseline of `1 + 2N` commit gates before eligible grouping.
Pre-execution approval adds a pending-ask gate; its decision can share the effective intent commit where ordering permits.
Completed hook phases do not each require a write.
Optional API receipts and capacity-driven cutoff changes share their protected decision's commit rather than adding another gate.
Run admission, ownership renewal, cancellation, checkpoints, and large-payload storage have separate costs; this is not a whole-run physical-write estimate.
Lost memory-only holds settle unavailable and never cause a tool rerun to recover output.

The kernel coordinator owns tunable maximum batching delay and batch-size bounds, outside individual loops and above storage acceptance.
Use per-session ordered queues with shared scheduling; preserve each session's head, claim, and execution gates.
Do not require a timer or goroutine per session, cross-session atomic transactions, or one stalled session to block unrelated sessions.
A storage adapter can aggregate physical writes across ready sessions where supported, but does not introduce a second independent application batching policy or acknowledge a volatile queue as durable.
Waiting briefly to reduce write load is an accepted tradeoff; queueing cannot postpone acceptance indefinitely under continuous traffic.
Cancellation signaling bypasses the ordinary coalescing delay, while its confirmed decision still requires acceptance.

Grouping may combine transitions that are already ready; it cannot cross an execution dependency by running a tool or model before its required earlier commit.
Measure append count, committed bytes, and acceptance latency through ordinary calls, rewrites, approvals, failures, and crash intervals before fixing batch defaults or interfaces.
Storage failure stops claims of durability, while already-started external work can remain uncertain.
Checkpoints, storage growth, and access control must support long compacted sessions without unbounded active receipt indexes, exposing private records, or skipping required history.

**Possible future storage direction:** Start with a conventional backend and bounded batching, then measure write load and acceptance latency before adding another storage layer.
A future adapter could acknowledge history appends after replication to memory on several machines, for example three replicas, and flush them to disk asynchronously.
This would require an explicit durability contract covering correlated loss of volatile copies, ordered acceptance, ownership fencing, and safe recovery/failover; replication alone does not supply those guarantees.
It could reduce synchronous disk-flush cost without removing execution commit gates, but is not part of the current implementation direction.
It would not permit replication or persistence of unreleased result payloads.

This borrows ActiveGraph's [commit-before-projection rule](https://docs.activegraph.ai/concepts/events/index.md) and [stale-writer rejection](https://docs.activegraph.ai/guides/operating-in-production/index.md), not its graph ontology or runtime.
The companion models express the domain rules; package allocation, wire types, and the remaining choices below still require interface review.

## Remaining contract and interface decisions

Open decisions now live with their bounded review topics, not in a second architecture backlog:

- [Execution representation, assembly, batching, and conformance](execution-and-recovery.md#open-decisions-and-detail).
- [Result lifetimes, settlement bounds, and predecessor/successor atomic bundling](cancellation-and-results.md#open-decisions-and-detail).
- [Authorization binding, hook identity, and safe evidence](authorization-and-hooks.md#open-decisions-and-detail).
- [Logical input and provider codecs/bindings](model-continuation.md#open-decisions-and-detail).
- [Shared controls, receipts, catalog/fork decisions, and work observation](commands-and-lifecycle.md#open-decisions-and-detail).
- [Storage/read interfaces, projection budgets, extension compatibility, and cutover encodings](storage-and-observation.md#open-decisions-and-detail).

Development staging remains specified in the [delivery boundary](#delivery-boundary).
Planning readiness is scoped to an implementation slice: settle and review the contracts that slice depends on, explicitly recording unresolved dependencies and what must remain unbound.
Other open decisions need not block independent preparatory or foundation work; runtime integration and cutover remain gated on their required contracts.

## Vocabulary reference

See the small [shared vocabulary](vocabulary.md) for foundational distinctions.
Specialized terms are introduced at their detailed sections and in the [history](session-commit-history.modelith.md) and [command](session-command-protocol.modelith.md) entity references rather than repeated in another glossary.

## Related information

- [Contract-design sequences and acceptance matrix](session-commit-history-contracts.md)
- [Session-history domain model](session-commit-history.modelith.md) and [YAML source](session-commit-history.modelith.yaml)
- [Catalog and command-protocol domain model](session-command-protocol.modelith.md) and [YAML source](session-command-protocol.modelith.yaml)
- [Prior-art survey and design recommendations](session-commit-history-prior-art.md)
- [Implemented persistence and reliability](../../architecture/observability.md)
- [Implemented API surface](../../architecture/api-surface.md)
- [Draft index](../README.md)
