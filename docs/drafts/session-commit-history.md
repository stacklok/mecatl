# Proposal: committed session history and responsive client reload

**Status:** Discussion draft, not an approved contract or description of shipped behavior.
**Scope:** Session persistence, streaming, recovery, and client projections.

The [companion domain model](session-commit-history.modelith.md) names the concepts, relationships, invariants, and scenarios used in this discussion.
Its [YAML source](session-commit-history.modelith.yaml) is validated and rendered with Modelith.
This proposal explains the rationale, open questions, and cutover scope.
The companion model describes domain semantics, not implementation status.
Neither document authorizes implementation.

## Goal

A client should be able to leave a session, return later, and see a trustworthy view of what the server knows.
The model should resume from the same committed session state.
During a live run, the client should still see tokens and tool activity promptly rather than waiting for every storage operation.

These goals need two honest kinds of information: *provisional* observations, which make a live session responsive, and *committed* facts, which survive reconnect and server restart.
A provisional observation must not silently turn into a claim about durable state.

The first cutover favors small, auditable boundaries over exhaustive automatic recovery.
It must not duplicate external actions or claim certainty without evidence; it can report an interrupted run, missing projection, or unknown outcome and require a later decision.
More automatic continuation, plugin lifecycle policy, and richer recovery can follow without weakening those honest states.
Correctness and a clear path forward matter more than handling every rare crash interval optimally on day one.

## Why the current arrangement makes this difficult

Today the `SessionStore` snapshot holds the current conversation and private continuation state.
Compaction replaces its conversation with a shorter one.
The separate `EventLog` holds activity and pre-compaction archives, but recording is optional and can fail without stopping a run.

Client streams expose selected live events, including updates that may never reach the log.
There is no shared position that joins a snapshot to a subscription.
See the [persistence architecture](../architecture/observability.md).

As a result, a newly connected client can recover the model-visible transcript but lose meaningful activity, such as delegation views.
Starting a live feed before reading the snapshot requires buffering and reconciliation.
Starting it afterward can miss changes in between.
[Issue #2114](https://github.com/stacklok/mecatl/issues/2114) tracks the fidelity of the restored client view.

## Proposed model

A `Session` has one authoritative, ordered `CommittedHistory` of state changes.
Each commit includes the information needed to rebuild continuation state, including private facts.
Conversation, usage, and other durable session facts derive from these commits.

Two derived views serve different readers:

- A `ContinuationCheckpoint` materializes private continuation state and identifies the last commit it includes.
- A `ClientProjection` presents an authorized display view at a committed position.

Neither view is an independent source of truth.
Summary fields are derived wherever possible; a checkpoint may cache them at its known position.

Compaction reduces model context, not stored history.
A compaction commit describes the resulting conversation, while the full earlier `CommittedHistory` remains retained for the session's lifetime.
Full history includes all admitted model-visible content and private continuation facts.
It does not include unbounded external artifacts that never entered the agent's context.

### Authorized views and commit positions

A `Client` receives a display-safe `ClientProjection`, never private commit records.
Provider replay data, pending authorization internals, and other private state stay server-side, even when the client loads the full available history.

A client snapshot reports C as its committed position.
A subscription after C delivers later authorized committed changes in order, with no gap between snapshot and subscription.
If C has expired, the client reloads a snapshot rather than claiming to have caught up.
Server-owned access checks apply to the snapshot and every subscription, including after reconnect.

The store assigns positions in a session's history.
A monotonic counter is useful for ordering; an incarnation or writer epoch can distinguish replaced histories or takeovers.
Timestamps help diagnose conflicts but cannot fence stale writers.
The authoritative store must reject commits from a writer that has lost ownership.
The exact ID format remains open.

## Persistence layer

The server writes private, versioned records through a commit boundary that assigns one session-wide position and rejects stale writers.
`CommittedHistory` supplies both continuation state and retained history; no second best-effort event log decides what is true.
An adapter may partition records across files, tables, or runs, but exposes one ordered view per `Session`.
Storage errors and ambiguous commits remain explicit caller outcomes.

Records cover session creation and controls, run boundaries, completed model turns, tool intents and outcomes, and compaction replacements.
They include private continuation material where needed; record types are distinct from public event types.
Every writer, including title generation, approvals, compaction, and scheduler work, uses this boundary.
Backend head checks or fencing order competing replicas; an in-process mutex alone cannot.

### Core records and extensions

Keep tool-specific lifecycles in versioned `Extension` records rather than making each one a core event type.
The core envelope owns:

- Session commit order, record identity, and root ownership epoch.
- Run and tool-call correlation, continuation gates, and bounded payload admission.
- The distinction between private records and client-safe projections.

An `Extension` record carries a stable namespaced type and schema version inside that envelope.
Storage preserves its validated bytes without interpreting its domain semantics.
A registered server-side interpreter supplies any required continuation reducer and approved client projection.
Registration also supplies a versioned private-state reducer and checkpoint codec, plus recovery, pause, resume, and cleanup capabilities when work can outlive a turn.

Continuation-relevant extension state must be reconstructible from committed records.
An extension checkpoint is a derived acceleration structure at P, never another writable authority.

#### Operation identity and lifecycle

`Subagent`, `Parallel`, and `Team` specialize a generic long-running tool invocation with optional child-session links.
Their rosters, joins, rounds, tasks, findings, and progress need not become permanent core storage concepts.

An `Operation` has a durable session-scoped identity linked to its initiating tool call and `Run`.
The call may commit an immediate started-result; later operation progress and completion retain their own identities.
This lets the envelope represent work that spans runs without implying that today's run-scoped background children do so.

The trusted `Extension` declares how run end, explicit cancellation, shutdown, owner loss, and restart affect its `Operation`.
Core records the resulting obligations without embedding the extension's private lifecycle.
Active background execution and writes remain under the root `OwnershipClaim`, even without a foreground `Run`.
A run ending does not prove the session is unloaded.
The scheduler independently produces sessions; it is not an event interpreter within session recovery.

#### Initial extension support and trust

The first cutover uses registered in-process handlers for the built-in `Subagent`, `Parallel`, and `Team` families, wired by host composition.
It ships this extension path rather than reserving an unused mechanism.
It requires no dynamic plugin loader and keeps engine core independent of host adapters.

Session-scoped `Operation` identity leaves room for cross-run work.
Cross-run background execution and completion-driven wakeups remain later capabilities, outside the first cutover.
A future networked extension transport needs a separate authenticated and fenced design.

Only trusted installed handlers may emit their registered type/version or declare optionality, obligations, and safe points.
Client input, model output, and tool-result text are data, never registration or authority.
Tests must exercise known, unknown optional, and missing continuation-critical extension types.

#### Compatibility and portable safety metadata

The client API identifies approved extension items by stable type and version without exposing private records or arbitrary opaque bytes.
Storage acceptance does not let unknown payloads bypass fencing, pairing, authorization, size limits, or parent-child disclosure rules.

Continuation-critical extension types require a compatible interpreter; otherwise execution fails closed.
Unknown display-only types can appear as explicitly unavailable history instead of being silently omitted.
Core-only management, such as safe status inspection and logical deletion, remains possible when continuation-critical state is unsupported.

Commit portable safety metadata with each extension transition:

- Type, version, and continuation-critical classification.
- Active obligations and child links.
- Whether the extension has reported a durable park point.

These markers are committed facts, not independent cached claims of safety.
A missing or incompatible handler cannot infer quiescence from opaque bytes or silently start dependent work.

For future graceful handoff, the host tells active extensions to stop admitting work and seek a quiescent point.
An extension parks or cancels under its registered policy and reports its committed safe position.
The root marker is valid only after every active child and extension reports one.

Definitive lease-loss notification lets an extension stop or diagnose work, but cannot override fencing or authorize a write.
Core still checks permissions and tool-call/result pairing.
Exact registration, compatibility, and wire encoding remain interface questions.

An unloaded extension is not evidence that external resources were cleaned up.
Future paired resources need a separate ownership and cleanup contract, not a plugin-management framework added implicitly here.

### Ownership and writer fencing

For distributed deployments, provisionally keep the renewable `OwnershipClaim`, writer epoch, and expiry in the same transactional backend as `CommittedHistory`.
A replica can query the `OwnershipClaim` without taking ownership.
The backend publishes owner identity, epoch, expiry, and owner-reported `loading`, `ready`, or `draining` state together.
An explicit release reports unowned/unloaded.
An expired claim makes the last loaded report stale; it does not prove the former pod or an external call stopped.
A failed status read reports unknown rather than unowned.

The owner acquires the claim before loading and reports ready only after loading.
It reports released/unloaded only after local execution stops and local tree state is unloaded, with a durable handoff marker for an active tree.

Acquire and renew on a coarse cadence.
Each commit atomically checks the expected head, writer epoch, **and unexpired claim** against the backend's ownership time.
A matching epoch after expiry is not permission to append.

If an owner reacquires a lapsed claim before anyone else, it atomically advances the epoch.
This is a new fenced acquisition, not a retroactive extension of authority.
The owner may reuse its loaded engine after verifying the durable head and reconciling uncertain in-flight transitions; a full rebuild is not inherently required.
A pre-expiry stale append must not inherit the new claim.

These checks must not require a Kubernetes API call for every token, event, or commit.
Backends without a shared transactional write boundary fail the distributed contract rather than falling back to best-effort checks.

#### Single-process local storage

A local JSON/JSONL adapter may declare an explicitly single-process mode with filesystem-appropriate head checks and write serialization instead of a cross-host lease.
Keep available interprocess protection, such as a stable per-session `flock`; do not drop the existing local writer guard.
This mode cannot serve as a shared multi-replica store.

The adapter detects failures and partial writes and declares what an accepted append survives.
Its operating mode must preserve the committed-cursor and read/replay checks.

#### Advisory Kubernetes signals

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

#### Delegation-tree ownership

An active delegation tree has one ownership domain, rooted at its parent `Session`.
The owner drives its children on the same replica under the tree's `OwnershipClaim`; independently transferring child ownership across replicas is outside this design.
Separately cooperating peer sessions need their own coordination contract.

The owning replica can report the tree as loaded, draining, or released.
A last-reported loaded state can outlive a crashed pod; the claim and its expiry determine whether takeover is allowed.

Ownership follows active work, not client connection count.
With no executing `Run` or session-scoped background `Operation`, the host may unload the tree and release the claim, perhaps after a bounded idle period.
A durable pending `PermissionAsk` does not require an engine or lease held in RAM for hours.
An authorized verdict later acquires a claim and rehydrates that exact ask.
Independent runnable background work keeps the root claim active while another member waits for approval.
Read-only clients can remain attached to committed history without pinning the owner.

### Storage access and commit coordination

The persistence interface commits and reads records, returning an accepted position.
Read-only adapters can inspect committed history and checkpoints without acquiring a writer claim or starting a run; learning and analytics may use that capability under their own authorized access policy and bounded reads.
The application exposes a read-only storage capability; deployments should give these consumers read-only backend credentials where supported.
Credential layout is not a new requirement on every storage backend.
Read access does not imply a public API for private records, permission to mutate, or permission to activate the session.

The storage port does not serialize protobuf or publish to clients.
An application-level session coordinator owns the transition: it requests the commit, then projects an authorized update and may wake connected readers.
Committed subscribers resume from the durable history, including across replicas.
A lost notification can delay delivery but cannot erase a commit or leave a gap; followers must be able to catch up without the original writer.
A commit whose outcome is ambiguous must be resolved against storage before claiming an acknowledgement or publishing it as confirmed.

Each state transition has a stable identity recorded in history.
A mutation first acquires the tree's claim and loads committed state; the owner serializes admission and resolves repeated action identities from that state.
The backend atomically checks the expected head and writer epoch on append, so a stale writer cannot commit.
After an ambiguous append, the owner checks history for its identity before retrying.
A derived lookup cache is optional, not a separate authoritative index of every client ID.
Repeating an accepted transition with the same identity and content returns its original decision while its receipt is available; conflicting reuse is rejected.
These checks make commit retries safe without pretending that an external tool executes exactly once.

Per-session learned permission grants are also derived from committed private approval decisions and their originating tool facts on load, not from an optional activity log.
An inherited approval fact on a fork does not become an active rule unless the fork's explicit inheritance decision creates one under current authority.

### Retry identity and receipts

Client mutations need stable action identity across retries.
A prompt or steer can carry a bounded, opaque client-minted ID; approvals and cancellation may use their exact ask or run identity when that is sufficient.
Bind the identity to the authorized session, operation, and decision rather than mistaking it for a server-assigned commit position or subscription cursor.
The owning replica reloads enough committed decisions to resolve a retry after takeover; a cache may accelerate this but cannot authorize new work.

Retain a steer's deduplication decision for its targeted `Run`'s entire lifetime, however long the run lasts.
After the run ends, a late retry naming that `expected_run_id` returns an available receipt or fails stale.
It never promotes the same text into a second run.
This does not require a whole-session receipt map for every action.

Session creation is a special case because there is no destination session to load.
Fresh creates and forks carry a client creation ID; a durable create-or-return decision resolves it to the same destination or rejects conflicting content.
The storage interface for session enumeration and listing also owns this logical creation/receipt capability.
Listing alone cannot enforce uniqueness against two concurrent replicas.

The tool's invocation identity and its intent/result commit identities remain distinct.
Checking or retrying a storage commit never reruns the tool.
An external tool may support its own idempotency key, but the session protocol cannot assume every side effect does.
Exact ID formats, receipt lifetime, and conflict responses belong in the interface contract.
Provisional streaming is a separate application path with no durability claim.

### Checkpoints, replay, and missing history

A `ContinuationCheckpoint` accelerates loading but may lag the committed head.
To load at position C, read a checkpoint at P, where P is at or before C, and apply every record in (P, C] in order.
The assembled state, including summary facts, must reflect C before the engine or client receives it.
The stored checkpoint still reports P, not C.

Checkpoints can be published independently of appends.
A crash during checkpoint publication must not create or erase a committed change.
Replay detects missing or incompatible records and fails explicitly rather than presenting partial state.

A previously acknowledged commit or writer epoch disappearing is a storage contract failure, not a lagging checkpoint.
Stop session writes, loads, and confirmed client projections.
Never repair the loss by writing local memory over history or reusing an old position.
Recovery of lost committed history needs a separate explicit operator procedure.

Rebuilding from retained history must yield the same continuation state and historical facts.
Checkpointing reduces replay cost; it does not authorize discarding earlier records.
The first design does not trim `CommittedHistory`.
A later trimming decision must specify which facts become unavailable to clients, audit readers, and forks, and how old cursors fail.

### Session snapshots and historical runs

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
The existing [API surface](../architecture/api-surface.md) distinguishes run-scoped controls from session-scoped reads; neither run identity nor session identity is a substitute for the session-wide commit position.

A compaction generation could later label which model-continuation checkpoint is current and which retained display history predates it.
A future client could defer loading older generations, reporting what remains available without claiming its partial view is the entire transcript.
The current model context, pending work, and unknown tool outcomes must be recoverable without loading old display history.
A generation marks a history boundary; it does not replace commit ordering or authorize deletion of retained history.

### Session catalog

The catalog supports bounded, paged, authorized listing and exact creation/status lookup without activating sessions.
Its display-safe entries project existing trusted `SessionKind` and kind-specific `SessionRelationship` values.
Scheduled fires show their schedule relation and fire-session identity.
Main sessions, delegated children, and other kinds are not all human-initiated.
Do not add a second generic origin field now; future external triggers and schedule-group queries can extend the catalog later.

Derived catalog metadata may lag a committed change.
Each projection identifies the position it reflects, and missed updates converge through replay or reconciliation.
`CommittedHistory`, not a stale list entry, answers authoritative session reads.

Creation identity, ready/failed publication, logical deletion, and writer claims remain strict decisions.
Before acknowledging deletion, durably suppress the entry and fence further writes.
Every list, exact lookup, and history read honors the tombstone even if display metadata lags.
If an adapter cannot prove the deletion gate for a catalog read, it fails that read rather than exposing a stale title.
The exact atomic or ordered storage primitive remains an interface question.

Catalog notifications are best-effort refresh hints, not a second durable history.
Clients resync through bounded queries after missed updates or reconnect; storage need not provide a watch.
User-level settings and state remain a separate future design.

### Forks and retained history

The initial cutover preserves head forking: an authorized, inactive main `Session` forks at its latest eligible committed continuation boundary into a new idle session.
The fork receives the entire retained prefix through that boundary, including pre-compaction facts.
It must not manufacture a "since last compaction" cutoff.

Forking at an older completed main-run boundary is future work, including when the source has resumed execution.
Such a fork rebuilds continuation and display history as they stood at the selected position, not from today's snapshot.

The fork has a new identity and records its source `Session` and exact position as lineage.
Later source activity stays in the source; a fork neither pauses nor takes ownership of its active delegation tree.
The fork remains self-contained after source deletion.
An adapter may optimize the initial full-prefix copy only if retention and readability remain independent of the source.

#### Asynchronous creation

At admission, pin source position C and capture its metadata and fork policy under a short source-side boundary.
The accepted creation reserves a new session ID and returns promptly with that ID and **creating** status; it does not hold a client request open for the entire copy.
The entry is visible in authorized session listings, and exact creation-ID/status lookup reports `creating`, `ready`, or `failed` (or a non-disclosing `deleted` for an exact lookup after deletion).
Retrying the same ID returns the same creation decision.

The copy is server-owned work after admission, not tied to the initiating client's connection; a disconnect cannot silently cancel it.
While immutable history through C is copied, the source may continue new runs.
A creating entry exposes only safe metadata and its status; it cannot be activated, forked, or read as complete history.

#### Ordering copy publication against deletion

Deleting the creating destination fences its copy and transitions it to a terminal deleted outcome; a late creator cannot publish it as ready.
Cleanup of staged bytes may follow asynchronously.

Publication does not require a transaction over the full source and destination histories.
After copying, a short conditional catalog decision orders the fork against source deletion.
New source runs and other fork copies do not wait on this copy.
The source execution claim is neither held through the copy nor reacquired merely to finalize it.

Source deletion can proceed while forks are staged.
It wins against forks not yet finalized; those attempts fail and clean up if the source can no longer be read.
A fork whose final decision won first is independent, even if its ready status appears after the source deletion response.

#### Recovering an orphaned creation

A fork request has a stable client creation identity and an idempotently recoverable result.
A crashed creator may leave the catalog entry **creating** temporarily.
A slower reconciler makes every ready publication conditional on the destination still being `creating`, even if the source-side fork decision already won.
Destination deletion wins over every later publication or cleanup retry; a completed source-side decision is not permission to resurrect a deleted destination.

Otherwise the reconciler completes publication for a finalized fork.
For an incomplete attempt, it waits until the creator can no longer finish before marking it **failed**.
It must distinguish a slow live copy from a dead one and never publish partial history.

Retrying the same creation ID after a terminal failure returns that failure; a new attempt needs a new ID.
A later reconciler could finish abandoned copies instead, but must preserve the same stable outcome contract.
Cleanup of incomplete staging is separate from its visible creation outcome.
The exact fencing, status, and cleanup mechanism belongs in the interface contract.

#### Fork transformation and inherited history

All fork/clone paths cross one explicit transformation boundary before creating the new `Session`.
It selects inherited historical and continuation facts, strips source execution state and effective authority by default, and marks inherited provenance.
Adapters must not bypass it by cloning a raw storage namespace.

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

#### Operational settings and permission grants

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

### Future same-session rewind

A rewind commits a new branch selection within the same `Session`.
It neither creates another session nor deletes later commits.
Eligible continuation points are **clean run boundaries** after fully settled runs, including failed or user-cancelled runs with valid call/result pairing.
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

## Client API

The server returns authorized, display-safe session facts and timeline items as a `ClientProjection`.
Clients need not understand private records or their storage layout.
The snapshot, history pages, and committed subscription share one session-wide position.
Provisional updates advertise their weaker status separately.

Conceptually, the read API provides:

- A session summary and stable paged history at its position.
- A committed subscription after that position.
- A provisional channel for low-latency observations and cumulative commit acknowledgements.

Provisional and committed channels may share a transport, but retain distinct status and cursor semantics.
These responsibilities do not specify RPC names or physical tables.

### Observation and activation

**Observe** is read-only: inspecting summaries, paged history, or committed subscriptions does not claim ownership, load an engine, or start execution.
An authorized **interactive attach** may acquire the tree's `OwnershipClaim` and load continuation to drive eligible work, even without a new prompt.
Attach does not change permission mode or confer write authority by itself.
An approval or other authorized mutation can enter this same activation path without a separate attach round trip.

A connection or committed subscription alone does not pin an owner.
A provisional stream exists only while there is live owned work and may end when work parks or completes.
Historical reads never activate execution as a side effect.
Exact API spelling remains open.

### Complete history through a pinned position

Initial reload loads the complete authorized display history through bounded pages, not one unbounded response.
Each page belongs to one immutable view pinned at C for the load.
Page tokens identify where to continue within that view, not where to resume live events, and are bound to the authorized session and view.
Only a successful final page attests that authorized history through C is complete.

If a page expires, fails, or no longer matches the view, discard the incomplete load and start a fresh snapshot.
A material authorization change invalidates the view and its tokens.
Later page and subscription reads recheck access; a token is not a grant.
Initial policy may be coarse, with finer caller-specific filtering later.
A disconnection alone does not revoke access, and a silent cutoff must not appear complete.

After loading all pages, subscribe after C to follow committed changes.
Changes committed during loading must remain resumable from C; otherwise the server reports expiration and the client reloads.
Page growing collections, including run inventories and conversation/detail records.
A large individual tool result or media item needs bounded content reads or an explicit size failure, not an oversized page.
A later client can load older pages on demand under this same contract.

### Payload admission and external artifacts

Bound canonical session payloads before they reach the model or `CommittedHistory`.
Reject oversized submitted media before accepting input.
For oversized tool output, apply an explicit limited-result contract and commit exactly what the model receives, with a truncation or external-reference indicator.
Never give the model full output while retaining a shorter version as though it were the same result.

A future spillover adapter may keep large artifacts outside history.
History would retain the producing command and parameters, the admitted excerpt seen by the model, and any authorized retrieval reference.
It would not claim to retain the entire artifact.
External artifacts need their own storage, access, and lifetime contract; spillover is outside this proposal.

### Multiple interactive clients

Multiple authorized `Client` callers may attach to the same server-owned `Run` and live feed.
There is no exclusive controller.
Every action is authorized independently, and the owner orders conflicting actions against committed session state.
Reading history grants neither control rights nor execution activation.

Disconnecting a client does not transfer control or cancel the run.
This differs from the current prompt-started `Converse` stream described in the [API surface](../architecture/api-surface.md), whose relay cancels on disconnect.

Every producer shares run admission, including user prompts and future extension-triggered work.
Only one eligible `Run` starts.
A competing prompt receives a conflict identifying the active run; the server neither queues it silently nor reinterprets it as a steer.

A client may hide that race by issuing a separately authorized, exact-run steer.
That steer can fail stale if the named run has ended.
Admission failure must not erase a committed background completion.

### Source-scoped steers

A `SteerSlot` holds one source's replaceable pending input, not an accumulating instruction queue.
Each `Session` has one shared user slot and one independent slot per registered `Extension`.
The extension itself is the initial coalescing key; there are no extension-defined subkeys.
All slots support the same set, replace, retract, and revision-specific consumption semantics.
Updating one slot never changes another.

An `Extension` uses the same authorized session-action semantics as a client but controls only its own slot.
Submitting a steer grants no implicit permission to answer asks or bypass execution gates.

An unqualified steer requests eligible execution: pending input for an active or awaiting `Run`, or a new run when idle.
An exact-run steer targets only the named run and never promotes to another.
This preserves the existing distinction between `Service.Steer` promotion and strict `SteerRun` controls.
Admission and the terminal race must not lose accepted input; recovery and cancellation dispositions are described below.

#### Delivery and provenance

Deliver all pending slots **together** at the next safe model-input boundary, in deterministic order.
Delivery neither interrupts an in-flight request nor bypasses an awaiting approval.
Commit the admitted input and consumption of each delivered `SteerRevision` before sending it to the model.

A replacement admitted after the delivery decision remains pending for a later boundary.
Consuming an older revision cannot clear it.
The ordering algorithm and framing remain interface questions.

The server stamps provenance so the model can distinguish user input from a named extension, tool, or harness source.
Text cannot claim its own trusted source or gain instruction authority by claiming to come from the harness.
Non-user notifications use appropriate untrusted-data framing; attribution does not elevate them to system-prompt or permission authority.

#### Small hints, retrievable results

An extension steer contains a size-capped hint with enough `Operation` identity, status, and context to choose a retrieval tool.
Full results stay in the extension's canonical committed state.
The agent calls an authorized tool to retrieve them and explore further.

Each extension has only one pending slot, so a replacement hint must cover outstanding work, for example by pointing to a tool that lists ready results.
Coalescing a hint must not erase operation outcomes.
Future cross-run background work and completion-triggered runs use these semantics; they remain outside the first cutover.

### Shared pending controls, not client drafts

All interactive clients share the user `SteerSlot`.
Submitted pending content, revision, and disposition are committed server state.
Authorized reload and subscription projections reflect that state to every client, not just the submitting connection.
A user can submit on one client and inspect, modify, or retract the pending steer on another.

Updates and retractions name the expected slot revision.
A stale client receives a conflict rather than overwriting another client's edit.
Conflict payloads and receipt lifetimes remain interface questions.

Unsent composer text, focus, filters, and other presentation state stay client-local.
Draft persistence and synchronization are outside this design.
Retracting the user slot never retracts an extension slot, or vice versa.

#### Shared approval resolution

Authorized clients also see pending `PermissionAsk` records and committed resolutions.
The first valid, authorized **committed** resolution of an exact ask wins.
Later conflicting answers receive the authoritative already-resolved decision.
They cannot reverse authorization that may already have allowed execution.

A retry of the winning action resolves through its existing action identity.
Unsubmitted responses stay client-local, and a connection reserves no exclusive responder role.

### One client timeline

Expose the authorized `ClientProjection` as one session-ordered timeline, not separate run histories for the client to merge.
Each committed item has a place in `CommittedHistory` order.
Run-associated items carry stable run IDs for grouping; session-wide changes share that order.

Run snapshots may organize storage, but the public read contract hides that partitioning.
Current metadata and the `ContinuationCheckpoint` remain separate from the display timeline.

Prefer one client-safe item schema for historical pages and committed updates.
Clients can then preserve identity, order, and revision through one projection path across reload.
This shares display projection, not private log records.

Provisional streaming uses separate status and correlation identity until commitment establishes an authoritative item and position.
Aggregate usage and permission mode may still appear as snapshot fields, avoiding the need to infer current authority from old activity.
Payload families and provisional reconciliation remain interface questions.

### What reload restores

A new client process must recover durable, client-visible session facts:

- Conversation and completed human-visible reasoning summaries, separate from provider-private replay material.
- Tool calls and committed outcomes.
- Delegation inventories and bounded activity.
- Changed-file facts, confirmed metadata, and reported usage.

Large details use bounded reads under the same privacy and authorization rules.
[Issue #2114](https://github.com/stacklok/mecatl/issues/2114) inventories each item and names exceptions; a best-effort feed must not decide what survives.
Reasoning representation belongs with [issue #2079](https://github.com/stacklok/mecatl/issues/2079).

#### Parent-owned delegation facts

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

#### Deeper child access

A parent-scoped detail request resolves the child from a verified relationship and checks policy for the caller, parent, child, and requested detail.
Possession of a child ID or access to the parent is not blanket access to the child.
Live views and reloaded views use the same policy.
Losing a client's in-memory fleet on reconnect must neither widen nor narrow access.

Recheck authority on every detail read, including after reconnect.
If policy denies access or the child cannot be read, preserve the parent summary and report detail unavailable.
Do not invent a complete child history.
The policy for deeper content and its disclosure limits remain interface questions.

#### Reported usage and inherited attribution

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

### Delegation detail budget

"Bounded" has three distinct meanings:

- Which child fields may be disclosed to a parent.
- How large each preview may be.
- How much activity the parent's current view retains per child or member.

The live UI keeps twelve trace entries per lane, and forwarded text previews are capped at 200 runes.
Consecutive text deltas can coalesce into one growing entry, so neither limit alone caps retained bytes.
Control-character scrubbing is not secret redaction.
The server projection needs a field allowlist, per-field and aggregate byte limits, and an honest truncation indicator before persisting or serving parent-visible detail.

#### Retained history versus the current window

Retain every *approved* parent-visible committed item in parent history; the compact current view may show only the latest bounded window.
Older approved items remain available through the paged timeline.
For the initial projection, retain only child message and tool snippets already forwarded to the parent today, subject to explicit per-field and aggregate byte caps; do not add new child content fields.
They may contain sensitive text: making them mandatory, long-lived history increases retention, and no secret redaction is promised.
The same approved projection governs live and reload; a short live prefix does not license copying any other child-private content.
The exact existing-field mapping and budgets belong in the interface review.
A reusable redaction subsystem, if wanted across product surfaces, is a separate future design, not an implicit feature of this proposal.

#### Cross-history projection gaps

Fold provisional child message deltas in memory.
Commit approved parent activity only at stable child message/turn, tool intent/result, and terminal boundaries; the live typing animation need not survive reload.
Normally the child commits a settled fact, then the parent commits its bounded projection before that child advances.

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

#### Disclosure before publication

Leave the disclosure decision at the child-to-parent projection boundary, before any parent commit or live publish.
That leaves room for a future opaque subagent: its private activity would stay in the child session, while only explicitly approved parent-visible facts and the tool's intended result cross to the parent.
A read-time filter cannot make content already stored in a parent's retained history private retroactively.
This proposal adds no opaque mode or new configuration; it preserves the boundary where such a policy could be applied later.

### Live delivery and acknowledgement

A `Run` streams provisional model text and reasoning summaries as they arrive, without a record per token.
If the server fails before committing a model response, reload may lose that entire response even when the client saw it live.
The server commits the completed assistant turn when it has an accepted result.
Tool progress and early result previews can also be provisional; dispatch and outcomes have separate durable boundaries below.

Provisional updates have correlation identities for amendment or discard, not resume cursors.
After storage accepts a change, the server advances a cumulative acknowledgement such as **"persisted through C"**.
Only a committed position is a durable subscription cursor.
If provisional output differs from the committed result, the client reconciles to the `ClientProjection`.

#### Reconnecting after owner changes

An ownership change ends the old live stream, including one forwarded through a non-owner.
There is no transparent cross-replica stream migration.
Reconnect through any entry replica from the last committed position, reloading a snapshot if that cursor is invalid.
Reconcile or discard provisional output.

A clean handoff may report a typed reconnect outcome; a crash may only break the transport.
Neither proves the `Run` stopped.
If the connection drops before acknowledgement, reload committed state rather than assuming provisional text survived.

#### What acknowledgement guarantees

An acknowledgement means the authoritative adapter accepted the record under its declared durability contract, allowing recovery after a mecatl process restart.
Queueing a write in memory or sending bytes to the client is not commitment.
Storage-node failure guarantees depend on the backend deployment, not one blanket protocol promise.
An adapter cannot report volatile writes as committed without disclosing that contract.

Provisional delivery never grants permission or establishes a certain tool outcome without evidence.
This distinction preserves low-latency streaming while keeping `CommittedHistory` authoritative.

## Recovery and execution safety

### Distributed recovery and evidence

A committed run-start record without completion establishes only an **unresolved** outcome.
A lost connection does not prove the run stopped; another server may own and drive it.
Report transport state separately from server-confirmed run state:

- A current owner.
- A fenced former owner.
- Ownership that cannot yet be determined.

The client may present these facts compactly, but cannot infer completion or failure from a missing event.

Once the server establishes that no writer can continue the old `Run`, it can commit an interrupted-run marker.
That marker explains why streamed model text is absent from recovered conversation.
Even after fencing, a started external tool may have had side effects; its outcome remains unknown until verified.
If ownership or storage is unavailable, report uncertainty rather than claiming the run ended.
Positions, writer identities, and bounded recovery status must remain meaningful across replica reconnects.

A newer backend writer epoch definitively fences the old writer: stop starting work and cancel the local run.
The host may inspect a Kubernetes Lease for diagnosis, but it cannot authorize an old-epoch append.
A fresh owner acquires the current backend epoch and reloads committed state.
This coordination stays server-side, without a Kubernetes dependency in engine core.

#### Renewal uncertainty and limited speculation

A failed or timed-out claim renewal is uncertainty, not permission to start another tool.
Started calls may settle, but only an epoch-checked commit confirms their outcomes.

One narrow exception permits a next LLM request whose exact input and continuation were committed while ownership was valid.
Its streamed output stays provisional until the result commits under a valid epoch and expected head.
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

#### First-cutover crash settlement

Crash takeover and agent continuation are separate decisions.
A crashed active `Run` is **interrupted**, not resumed under the same run identity in the first cutover.
After fencing the former writer, recovery commits evidence to repair tool pairing.
Requested calls with no intent were not started; intent without an established result has unknown outcome.
No tool is automatically retried.

A later authorized interactive attach may start a new `Run` from committed conversation.
If only an uncommitted LLM response was lost, the server issues a new model attempt from the last committed input on that attach, regardless of the initiating UI.
This is allowed only when no tool intent, pending steer, or approval requires a decision.
The new response may replace the discarded provisional response and differ from it; the old attempt's usage may be unknown.
Read-only inspection never triggers the request.

Pending steers and uncertain tools need explicit post-crash recovery decisions before new dependent work.
No-client automatic agent continuation is deferred.
A durably awaiting exact `PermissionAsk` remains resolvable by explicit verdict rather than becoming an interrupted run.

Generic orphan settlement covers every session kind, including scheduled fires and delegated children.
It neither restarts them nor knows schedule retry policy.
The scheduler separately reconciles due claims and fire records from settled session state, without a competing direct session settlement path.
An unobserved crashed fire does not replay on client activation; later due fires proceed.
Opted-in one-shot retry remains schedule policy, not a general same-run crash-resume guarantee.

#### Later same-run continuation policy

A later same-run crash-resume feature may store a per-root-session continuation policy initialized from a deployment-wide default.
Delegation would follow the root's policy; changing the default would not rewrite existing sessions.
Its conservative default is to park the recovered tree, while an opt-in could let the agent continue with an explicit unknown-outcome result in model-visible context.
A separate authorized resume would continue the same run after fencing and reconciliation, never rerun the uncertain tool.
This policy, toggle, and same-run resume action do **not** ship in the first cutover.
Who may change a session's policy and when remain later decisions; do not create a client toggle now.

### Durable steer recovery

Before acknowledging a steer, commit its admission to the source `SteerSlot`, including stable action identity and `SteerRevision`.
Content, replacement, retraction, and consumption survive crash or handoff.
Acceptance does not mean the model has received the input.

In ordinary execution, commit pending revisions' insertion at the next eligible input boundary, after any required approval verdict.
Clients reconcile pending controls against that committed insertion rather than an inbox echo.

In the first cutover, an interrupted run leaves unconsumed steers pending for explicit recovery.
The authorized caller chooses a new follow-up run with the input or retracts it.
The server neither silently promotes nor discards accepted text, nor requires resubmission.
A later same-run resume could consume it at the resumed run's eligible boundary.
Stable identities prevent retried steer and recovery commands from inserting input twice.
This durability differs from today's restart-lost in-memory inbox.

#### Terminal-race disposition

The server resolves the terminal race; clients need not wait for a turn to finish before sending.
An active `Run` may consume an accepted steer if it continues.
If it ends first, the server commits an explicit disposition:

- For an unqualified steer on normal completion, promote pending input to one follow-up run rather than asking for resubmission.
- An exact-run steer never authorizes promotion; its unconsumed terminal disposition remains to be decided.
- An interrupted first-phase run leaves input pending for explicit recovery.
- A future same-run crash-parked run is not terminal and cannot authorize promotion.

The server may reject a steer before accepting it.
After acknowledging durable acceptance, it cannot return responsibility to the client merely by echoing text in a terminal response.
Order the recipient-run or pending-state decision at the committed session head and make it replayable by message identity.
Retries never create a second follow-up.

#### User cancellation and failed runs

Explicit user cancellation of the targeted `Run` also cancels its pending, undelivered user steer.
Commit both dispositions together so a crash cannot leave input that starts a run contrary to cancellation.
A steer already inserted into committed conversation cannot be cancelled retroactively.

Run cancellation's effect on independent extension slots and future background work remains open.
Retracting the user slot alone never affects them.
A terminal response may include discarded input as a convenience, but that note is neither guaranteed delivery nor the cancellation record.

Internal shutdown or lease-loss cancellation is not user intent.
Preserve pending steers for fenced crash recovery.
Without accepted user cancellation, a crash leaves them pending for the first-phase explicit follow-up-or-cancel decision; later same-run resume could consume them.

A cancellation naming an old run cannot cancel a follow-up already created by terminal-race promotion.
The broader multi-client cancellation targeting contract remains open below.

If a run commits a failed terminal outcome before consuming a steer, keep the input pending rather than promoting it automatically.
The caller can inspect the failure and choose a follow-up with that input or cancel it.
A failed run is not implicitly resumed.
The disposition survives reconnect without requiring resubmission.

No-client autonomous takeover and continuation remain later capabilities.
An unobserved interrupted run waits for authorized client activation or recovery in the first cutover.

### Routing to the owning replica

A non-owner receiving a mutation or provisional-stream request consults the authoritative `OwnershipClaim` and forwards over authenticated internal transport.
The owner rechecks caller authority, tree identity, and current epoch; ingress affinity metadata grants no authority.
Forward at most one hop, without loops or exposing private pod addresses.

On handoff or a stale owner hint, refresh ownership.
An ambiguous mutation retains its request identity so a retry resolves against the original decision.
If ownership or routing cannot be established, return retryable unavailability rather than invented success.
The client keeps its committed cursor and queued request across reconnect.

Affinity can reduce forwarding but is not required for correctness.
Any authorized replica can serve durable snapshots and committed catch-up reads from shared history.
An owner-aware gateway could later avoid forwarding; it is not required here.

### Compatible-replica active-tree handoff (later phase)

The first cutover does not promise turn-boundary pause or seamless cross-replica continuation of an active delegation tree.
That capability is not implemented today.
It must still preserve or explicitly replace these existing guarantees:

- Bounded cancellation and join-before-release.
- Durable pending root-ask rehydration after takeover.
- Refusal of work before the ownership boundary.
- Crash-orphan repair with valid tool-call/result pairing.

A cancelled run or new client-started run is not continuation of the old tree.
The following future contract describes what history must accommodate, not what the first phase delivers.

#### Parking the tree and closing approval admission

Drain is a committed pause, not the end of a `Run`.
The owner marks the whole tree draining and admits no new model or tool calls in any member.
Each member finishes active calls, commits outcomes and continuation, then parks at a turn boundary.

A pending `PermissionAsk` is already a parkable continuation.
Its exact ask and decision state survive handoff without waiting for the human.
The parent retains its delegation call and child links as pending; parking a child is not a tool result.
Delegated tools must support coordinated pause rather than tying logical call lifetime to one pod.

During drain, an approval may commit against its exact ask and decision identity, but cannot start dependent work on the draining owner.
The successor consumes the decision on resume; same-identity retries return the original decision.

After closing approval admission for final handoff, the owner returns a typed reconnect response without accepting new verdicts.
The client keeps its request identity and queued verdict, reconnects through any entry replica, and retries.
The entry forwards to the successor after acquisition; bounded retries may report temporary unavailability until then.
Resolve ambiguous acceptance from history before claiming success or asking for a new verdict.

#### Publishing a complete handoff

The owner commits a root marker only after every descendant is parked and no LLM or external tool call is in flight.
The marker contains the parent's parked continuation and names each descendant's exact committed parked position.
The owner then unloads local tree state and releases its exact `OwnershipClaim`.

Child histories may commit independently before the marker; no multi-history transaction is required.
The marker is the publication boundary.
A successor loads and verifies every named position before resuming any member; missing or incompatible positions fail closed, never as a partial handoff.
Approval admission closes before the marker so recorded positions cannot omit an accepted verdict.

A crash before the marker leaves an incomplete drain for fenced recovery.
A crash after the marker but before release leaves a complete handoff for takeover after claim expiry.
The handoff is durably discoverable by healthy replicas without a client opening the session.

#### Later autonomous takeover

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

#### Shutdown deadlines

Drain is per root `Session` and tree; a slow tree does not delay other complete handoffs.
At the deadline, cancel and join remaining in-flight calls where possible.
If a tree cannot establish all outcomes and commit its marker before shutdown, do not release it as a graceful handoff.
Its claim expires, and a successor uses crash-style recovery with unknown external outcomes.
Other completed trees keep their graceful markers and immediate releases.

### Tool calls and uncertain outcomes

Commit the completed assistant turn and requested tool-call identities before dispatching any call.
For each call, commit its authorized intent and private continuation immediately before dispatch.
The model's batch is not a storage or execution transaction.
Permitted independent calls may overlap under the existing read-parallel/mutate-serial discipline.

A client-safe projection may show an admitted call in flight.
Commit each established result under its original call identity.
The durable evidence distinguishes three states:

| Evidence | What the server can establish |
| --- | --- |
| Requested call, no committed intent | The protocol did not dispatch it. |
| Committed intent, no committed result | The call might have started. |
| Actionable durable ask | Approval is still pending; do not close the call as not-started. |

After fencing the former writer and checking for a committed result, recovery closes an orphaned requested call with a harness-authored result:

- **Not-started** if no intent was committed: the dispatch gate prevented execution.
- **Unknown-outcome** if intent committed but the outcome cannot be established: the tool may have had effects and may still be running.

Both retain the original call identity and complete model-visible call/result pairing.
Unknown outcome asserts neither failure nor absence of effects.
Recovery never automatically retries the tool.
The agent can then verify external state, retry an idempotent operation, or ask for help, under normal permissions and tool boundaries.
Until recovery commits the result, the session cannot feed an invented outcome to the model or start dependent work.

Correlation prevents duplicate client cards; it does not make external effects exactly once.
Which tool-start phases and post-crash facts are meaningful across implementations remains open.

### Execution gates

The following boundaries distinguish fast observation from permission to advance the session.
"Committed" means the authoritative store has accepted the record, including its ownership check; an in-process queue is not enough.

| Action | Required before proceeding | What can be provisional |
| --- | --- | --- |
| Start a run or send its next model request | Commit the accepted input, run identity, and the continuation state the request uses. A later request also waits for the preceding turn's committed outcome. | Stream the current model response without persisting each token. |
| Present an approval | Commit the pending ask and exact private continuation state before treating the approval as actionable. | Render an unconfirmed request as waiting, not as an exercisable grant. |
| Dispatch each tool call, including a delegated agent | Commit the completed assistant turn with its requested calls, then commit that call's authorized intent and identity before its dispatch. If its intent write fails, do not dispatch it. | Show that the call is being prepared; progress and result previews after dispatch may be provisional. |
| Continue after a tool returns | Commit its canonical result before feeding it back to the model or claiming a final outcome to the client. If storage cannot establish that result, stop dependent work until recovery commits a known result or a distinct unknown-outcome result for the same call. | Show a clearly provisional preview while the result commit is pending. |
| Use compacted history or changed session controls | Commit the replacement conversation or control decision before using it for another model request or presenting it as confirmed. | Show that the operation is pending. |

A failed result write after dispatch cannot undo a tool's external side effect.
The server must stop dependent work and expose the unresolved call until it can establish a durable outcome.
If an append reports an ambiguous failure, the server must verify the committed position and call identity before deciding whether to retry; repeating a tool call is never a storage retry.

## Boundaries and costs

### Breaking storage and protocol cutover

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

#### Service upgrade and client compatibility

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

### Logical deletion and retained bytes

Logical deletion is irreversible, independently of physical purge timing.
It fences activation and writes, prevents identity reuse, and denies new history reads even while records remain stored.
Deleting a root `Session` also logically disables delegated children by default.
No direct child read or resume bypasses that deletion, including during partial cleanup.

A root deletion boundary can make the entire tree inaccessible before physical child cleanup.
No atomic purge across child stores is required.
The adapter declares retention and purge timing, which may be stricter.

Catalog and history reads expose no deleted-session title, prompt, source hint, or descriptive metadata.
Only a minimal, non-enumerable creation-ID/session-ID tombstone may remain to reject late creation retries and identity reuse.
Exact creation-ID lookup reports deleted without substantive data.
Creation IDs are bounded opaque values, not user-authored labels.

A tombstone does not prove physical purge.
Retained bytes remain inaccessible through the logical API until the adapter's declared purge.
A future explicit detach could preserve a child before root deletion; it is outside this proposal.

Forks are independent peer sessions with lineage, not source-owned children.
Deleting a source neither disables nor deletes its existing forks or other separately created peers.

### Storage costs and later buffering

`CommittedHistory` costs more than a best-effort activity log.
Storage failure must stop claims of durability, while already-started external work may remain uncertain.
Checkpoints, storage growth, and access control must work for long sessions without exposing private records or allowing a cursor to skip required history.
Forks, child sessions, and multiple server replicas also need ownership and lineage rules.
Subpackages should follow these contracts once defined; relocating the TUI model by itself would not resolve the persistence gap.

An adapter may eventually batch or buffer writes while preserving these acknowledgement and ordering guarantees.
The initial design assumes no such relaxation: acknowledging an in-memory buffer as "persisted" would mislead a client after a crash.
Any later buffering proposal must define its durable boundary, backpressure, failure handling, and cross-replica recovery separately.

This borrows ActiveGraph's [commit-before-projection rule](https://docs.activegraph.ai/concepts/events/index.md) and [stale-writer rejection](https://docs.activegraph.ai/guides/operating-in-production/index.md), not its graph ontology or runtime.
This draft and its companion model retain the unresolved design questions while the discussion continues.

## Questions to resolve before interface review

1. The detailed shared-control contract: exact-run cancellation targeting for multiple clients, cancellation's interaction with extension slots and later background work, and the disposition of an accepted exact-run steer when its run ends before consumption.
   Shared attachment, prompt conflict, per-source coalescing, all-together delivery, and first-committed approval resolution are settled directions above.
   Ordering algorithm, provenance encoding, slot-revision conflict responses, and notification byte caps remain interface details.
2. The exact #2114 display-fidelity matrix, #2079 reasoning representation, and per-field/aggregate budgets for retained client and delegation detail.
   Which client-safe items must commit before progress and which rare cross-history gaps are explicitly surfaced?
3. Exact namespace/marker and legacy collision detection, backend-neutral claim and append interfaces, local torn-record diagnosis, and client API versions, history pages, authorization-view invalidation, and live cursors.
4. Action-identity format, scope, receipt lifetime, conflict/stale responses, and the catalog's atomic creation, deletion, and conditional fork decisions.
5. Storage layout and scale: checkpoint/replay bounds, large content reads, catalog pagination and reconciliation, and provider-specific durability declarations.
   A run boundary alone does not bound history size.
6. Extension registration and version compatibility, mandatory versus display-only state, and the exact generic operation/child-link contract.
   Later work may add active-tree handoff, autonomous run resume, historical forks, same-session rewind, and networked extensions; their hooks must not be mistaken for first-cutover behavior.

## Related information

- [Companion domain model](session-commit-history.modelith.md)
- [Model source](session-commit-history.modelith.yaml)
- [Implemented persistence and reliability](../architecture/observability.md)
- [Implemented API surface](../architecture/api-surface.md)
- [Draft index](README.md)
