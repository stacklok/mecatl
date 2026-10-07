# Proposal: committed session history and responsive client reload

**Status:** Discussion draft, not an approved contract or description of shipped behavior.
**Scope:** Session persistence, streaming, recovery, and client projections.

The [companion domain model](session-commit-history.modelith.md) captures the
concepts, relationships, invariants, and scenarios being tested in this design
conversation. Its [YAML source](session-commit-history.modelith.yaml) is validated
and rendered with Modelith. This proposal owns rationale, open questions, and
cutover scope; the model describes domain semantics, not implementation status.
Neither document authorizes implementation. This design stands independently of
PR #2078; that draft implementation is not its baseline.

## Goal

A client should be able to leave a session, return later, and see a trustworthy
view of what the server knows. The model should resume from the same committed
session state. During a live run, the client should still see tokens and tool
activity promptly rather than waiting for every storage operation.

These goals need two honest kinds of information: *provisional* observations,
which make a live session responsive, and *committed* facts, which survive
reconnect and server restart. A provisional observation must not silently turn
into a claim about durable state.

The first cutover favors small, auditable boundaries over exhaustive automatic
recovery. It must not duplicate external actions or claim certainty without
evidence; it can report an interrupted run, missing projection, or unknown
outcome and require a later decision. More automatic continuation, plugin
lifecycle policy, and richer recovery can follow without weakening those
honest states. Correctness and a clear path forward matter more than handling
every rare crash interval optimally on day one.

## Why the current arrangement makes this difficult

Today the SessionStore snapshot holds the current conversation and private
continuation state. Compaction replaces its conversation with a shorter one.
The separate EventLog holds activity and pre-compaction archives, but recording
is optional and can fail without stopping a run. Client streams expose selected
live events, including updates that may never reach the log. There is no shared
position that joins a snapshot to a subscription. See the
[persistence architecture](../architecture/observability.md).

As a result, a newly connected client can recover the model-visible transcript
but lose meaningful activity, such as delegation views. Starting a live feed
before reading the snapshot requires buffering and reconciliation. Starting it
afterward can miss changes in between. [Issue #2114](https://github.com/stacklok/mecatl/issues/2114)
tracks the fidelity of the restored client view.

## Proposed model

Treat each session as an ordered history of *committed state changes*. A
commit includes the information needed to rebuild continuation state, even
when some of that information is private. The stored continuation checkpoint
is computed from that history and identifies the last commit it includes.
The client snapshot is a separate authorized view assembled at a committed
position; neither view is an independent source of truth. Conversation,
usage, and other durable session facts derive from committed changes. Summary
fields are derived wherever possible; checkpoints may materialize them at a
known position, not become an independent authority. A compaction commit
describes the resulting conversation; the full pre-compaction committed
history remains retained alongside it for the lifetime of the session.
Compaction reduces model context, not stored history. Full session history
means all admitted model-visible content and private continuation facts, not
unbounded external artifacts that were never part of the agent's context.

A client receives an authorized display projection, never the private
commit record. Individual payloads must remain display-safe even when the
client loads the full available history. Provider replay data, pending
authorization internals, and other private state stay server-side. A client
snapshot reports a committed position C. A subscription after C delivers
the later committed changes visible to that client, in order, with no gap
between snapshot and subscription.
If the position has expired, the client reloads a new snapshot rather than
pretending it has caught up. Server-owned access checks apply to the snapshot
and to every subscription, including after reconnect.

The store assigns commit positions in a session's history. A monotonic counter
is useful for ordering, and an incarnation or writer epoch can distinguish
replaced histories or takeovers. Timestamps can help diagnose a conflict;
they do not fence a stale writer. The authoritative store must reject commits
from a writer that has lost ownership. The exact ID format is not settled here.

## Persistence layer

The server writes private, versioned session records through a commit boundary
that assigns one session-wide position and rejects stale writers. The same
commit records provide the current continuation state and the retained
history; no second best-effort event log is required to decide what is true.
An adapter can divide records across files, tables, or run partitions, but
must offer one ordered view for a session. Storage errors and ambiguous
commits remain explicit outcomes for the caller. The records cover session
creation and controls, run boundaries, completed model turns, tool intents
and outcomes, and compaction replacements. They carry private continuation
material where needed; their types are not the public event taxonomy.
Every session writer, including title generation, approvals, compaction, and
scheduler work, uses this boundary. A backend-enforced head check or fencing
rule orders competing replicas; an in-process mutex alone does not.

### Core records and extensions

Keep the storage and client contracts extensible without making every tool's
lifecycle a core event type. The core envelope owns session commit order,
record identity, root ownership epoch, run and tool-call correlation,
continuation gates, bounded payload admission, and explicit private-versus-
client-safe projection. An extension record carries a stable namespaced type
and schema version inside that envelope; storage preserves its validated
bytes without understanding the extension's domain semantics. A registered
server-side interpreter supplies any continuation reducer and approved client
projection. Extension registration also supplies a versioned private-state
reducer, checkpoint codec, and (when it can outlive a turn) recovery, pause,
resume, and cleanup capabilities. State that affects continuation must be
reconstructible from committed extension records; an extension checkpoint is
a derived acceleration structure at P, never another writable authority.
Subagent, Parallel, and Team are specializations of a generic long-running
tool invocation with optional child-session links; their roster, join, rounds,
tasks, findings, and progress types need not be permanent core storage
concepts. A background operation has its own durable identity linked to
its initiating tool call and run, but scoped to the session: the call may
commit one immediate started-result while later operation progress and
completion retain their own identities. The envelope must be able to
represent work that spans runs without pretending today's run-scoped
background children already do so. Its trusted extension declares how
run end, explicit cancellation, shutdown, owner loss, and restart affect
that operation; core records the resulting obligations without embedding
the extension's private lifecycle. While any background operation remains
active, its writes and execution stay under the root session claim even
without a foreground run. A run ending is not proof the session is unloaded.
The scheduler remains an orthogonal producer of sessions, not an event
interpreter baked into session recovery. The initial cutover uses this
versioned extension path for the built-in Subagent, Parallel, and Team
families, with registered in-process handlers wired by host composition.
Session-scoped operation identity leaves room for work that spans runs, but
cross-run background execution and its completion-driven wakeups remain a later
capability, not part of the first cutover. The extension path
does not merely reserve an unused escape hatch or require a dynamic
plugin loader. Only trusted installed handlers may emit their registered
type/version or declare optionality, obligations, and safe points. Client
input, model output, and tool-result text are data, never extension
registration or authority. Built-ins can be compiled into the initial
server distribution without coupling engine core to host adapters. A future
networked extension transport is a separate authenticated/fenced design, not
part of this cutover. Tests must exercise known, unknown optional, and
missing continuation-critical extension types.

The client API identifies approved extension items by stable type and version,
not by exposing private storage records or arbitrary opaque bytes. Unknown
extension payloads cannot bypass fencing, pairing, authorization, size
limits, or parent-child disclosure merely because storage accepts them.
Persisted extension types required for continuation must have a compatible
interpreter or the session fails closed; display-only unknown types can be
represented as explicitly unavailable history rather than silently omitted.
Persist enough portable safety metadata alongside each extension transition:
type and version, continuation-critical classification, active obligation
and child links, and whether the extension has reported a durable park point.
Those markers are committed with the transition, not independently cached
claims of safety. A missing or incompatible plugin cannot infer quiescence
from opaque bytes or silently start dependent work. When future graceful handoff is enabled, the host tells active extensions
to stop admitting work and seek a quiescent point. An extension may park or
cancel according to its registered policy and report its committed safe
position; the root marker is valid only after every active child and
extension reports one. On definitive lease loss, notification lets the
extension stop or diagnose its work, but cannot override core fencing or
authorize another write. The core still checks permissions and tool-call/
result pairing. Exact registration, compatibility, and wire encoding belong in the interface contract; no runtime plugin loader
is required in the first cutover. Unknown continuation-critical state blocks
execution but not core-only management such as safe status inspection or
logical deletion. No external resource cleanup is inferred from an unloaded
extension; future paired resources need a separate ownership and cleanup
contract rather than an unrequested plugin-management framework now.

For a distributed deployment, provisionally put the authoritative,
renewable ownership claim, writer epoch, and expiry in the same transactional
backend as the session history. A replica can query the claim without taking
ownership: owner identity, epoch, expiry, and owner-reported `loading`, `ready`,
or `draining` state are published together. An explicit release reports
unowned/unloaded; an expired claim makes the last loaded report stale, not
proof that the former pod or an external call stopped. A failed status read
reports unknown rather than unowned. Acquire the claim before loading, report
ready only after loading, and report released/unloaded only after local
execution has stopped and local tree state has been unloaded (with a durable
handoff marker for an active tree).
Acquire and renew on a coarse cadence; each commit atomically checks the
expected head, writer epoch, **and unexpired claim** against the backend's
ownership time. A matching epoch after expiry is not permission to append.
If an owner reacquires a lapsed claim before anyone else does, atomically
advance the epoch; this is a new fenced acquisition, not a retroactive
extension of authority. It may reuse
its loaded engine after verifying the durable head and reconciling uncertain
in-flight transitions; a full rebuild is not intrinsically required. This
rare shortcut must not let a pre-expiry stale append inherit the new claim.
This must not require a Kubernetes API call for every token, event, or state
commit. Backends without
a shared transactional write boundary must fail the distributed contract
rather than silently fall back to a best-effort check. A local JSON/JSONL
adapter may declare an explicitly single-process operating mode with
filesystem-appropriate head checks and write serialization instead of a
cross-host lease. Keep local interprocess protection where available (for
example a stable per-session flock); do not silently drop the existing local
writer guard. Such a mode cannot be configured as a shared multi-replica
store. The adapter owns its failure/partial-write detection and declares
what an accepted append survives, without weakening the committed cursor or
read/replay checks.

A deployment may retain the per-session Kubernetes Lease solely as an
advisory operational mirror and watch signal, or disable it to avoid two
apparent ownership sources. The backend claim is always the sole authority
for writer admission and session owner/status queries; a conflicting or stale
Kubernetes holder cannot authorize work, block a valid backend owner, or
become a public owner answer. Failure to publish the advisory mirror is a
diagnostic, not a second session-write gate. Lease watches can report renewals,
releases, and takeovers; a separate Pod watch can provide earlier evidence of
pod loss. A crashed pod stops renewing, but expiry itself emits no Lease
update: a watcher must also track the deadline or use another signal. A
storage backend may supply an equivalent wakeup, but not all do. Neither
Kubernetes watches nor storage notifications grant ownership, prove an old
external call stopped, or replace backend expiry and atomic fencing checks.
Notifications can be delayed or missed; recovery needs a bounded rescan. In
the first design, a watched Pod failure can wake a worker to inspect state
and schedule takeover at claim expiry, but cannot advance the backend epoch
before expiry. A future accelerated takeover needs its own verified-death
and fencing contract.

An active delegation tree has one ownership domain, rooted at its parent
session. The owner drives its children on the same replica under the tree's
claim; independently transferring child ownership across replicas is outside
this design. Separately cooperating peer sessions would need their own
coordination contract. The owning replica can publish whether the tree is
loaded, draining, or released, but a last-reported loaded state can outlive a
crashed pod; the claim and its expiry determine whether takeover is allowed.
Ownership follows active work, not client connection count: with no
executing run or session-scoped background operation, the host may unload
the tree and release the claim, perhaps after a bounded idle period. A durable
pending HiL ask does not by itself require an engine or lease held in RAM for
hours; an authorized verdict later acquires a claim and rehydrates that
exact ask. Independent runnable background work keeps the root claim active
even while another member waits for approval. Read-only clients can remain
attached to committed history without pinning the owner.

The persistence interface commits and reads records, returning an accepted
position. Read-only adapters can inspect committed history and checkpoints
without acquiring a writer claim or starting a run; learning and analytics
may use that capability under their own authorized access policy and bounded
reads. The application exposes a read-only storage capability; deployments
should give these consumers read-only backend credentials where supported.
Credential layout is not a new requirement on every storage backend. Read
access does not imply a public API for private records, permission to mutate,
or permission to activate the session. The storage port does not serialize
protobuf or publish to clients. An application-level session coordinator owns the transition: it requests the
commit, then projects an authorized update and may wake connected readers.
Committed subscribers resume from the durable history, including across
replicas. A lost notification can delay delivery but cannot erase a commit or
leave a gap; followers must be able to catch up without the original writer.
A commit whose outcome is ambiguous must be resolved against storage before
claiming an acknowledgement or publishing it as confirmed. Each state
transition has a stable identity recorded in history. A mutation first
acquires the tree's claim and loads committed state; the owner serializes
admission and resolves repeated action identities from that state. The
backend atomically checks the expected head and writer epoch on append, so a
stale writer cannot commit. After an ambiguous append, the owner checks
history for its identity before retrying. A derived lookup cache is optional,
not a separate authoritative index of every client ID. Repeating an accepted
transition with the same identity and content returns its original decision
while its receipt is available; conflicting reuse is rejected. These checks
make commit retries safe without pretending that an external tool executes
exactly once. Per-session learned permission grants are also derived from
committed private approval decisions and their originating tool facts on
load, not from an optional activity log. An inherited approval fact on a fork
does not become an active rule unless the fork's explicit inheritance decision
creates one under current authority.

Client mutations need stable action identity across retries. A prompt or
steer can carry a bounded, opaque client-minted ID; approvals and cancellation
may use their exact ask or run identity when that is sufficient. Bind the
identity to the authorized session, operation, and decision rather than
mistaking it for a server-assigned commit position or subscription cursor.
The owning replica reloads enough committed decisions to resolve a retry
after takeover; a cache may accelerate this but cannot authorize new work.
For a steer, retain its deduplication decision for the targeted run's entire
lifetime, however long that run lasts; after it ends, a late retry with that
`expected_run_id` returns any still-available receipt or fails stale rather
than promoting the text into a second run.
This does not require an all-actions, whole-session receipt map. Session
creation is a special case because there is no destination session
to load. Fresh creates and forks carry a client creation ID; a durable
create-or-return decision resolves it to the same destination or rejects
conflicting content. The storage interface for session enumeration and
listing also owns this logical creation/receipt capability. Listing alone
cannot enforce uniqueness against two concurrent replicas. The tool's
invocation identity and its intent/result commit identities remain distinct.
Checking or retrying a storage commit never reruns the tool. An external
tool may support its own idempotency key, but the session protocol cannot
assume every side effect does. Exact ID formats, receipt lifetime, and
conflict responses belong in the interface contract. Provisional streaming
is a separate application path with no durability claim.

A checkpoint is a derived acceleration structure, not a competing authority.
It may lag the committed head. To load at committed position C, read a
checkpoint at P (where P is at or before C) and apply every committed record
in (P, C] in order. The assembled state, including summary facts, must reflect
C before it is returned to the engine or client; a stored checkpoint still
reports P, not C. Checkpoints can be published independently of appends;
a crash during checkpoint publication must not create or erase a committed
change. Replay must detect missing or incompatible records and fail explicitly
rather than silently present partial state. A previously acknowledged commit
or writer epoch disappearing from the authoritative history is a storage
contract failure, not a lagging checkpoint. Stop session writes, loads, and
confirmed client projections; never repair it by writing a local in-memory
copy back over the log or reusing an old position. Recovery of lost committed
history needs a separate explicit operator procedure. Rebuilding from retained
history must
yield the same continuation state and historical facts. Checkpointing reduces
replay cost; it does not authorize discarding earlier log records. The first
design does not trim committed history. A later trimming decision would trade
away information and must define exactly which history becomes unavailable to
clients, audit readers, and forks, as well as how old cursors fail.

### Session snapshots and historical runs

Keep the session summary small: identity, current metadata, derived summary
facts, the committed head, and at most a reference or index into committed
history and its continuation checkpoint. It does not embed a transcript or
maintain another authoritative copy of conversation. The separately stored
checkpoint materializes private continuation state at its own position P;
the summary may point to it but must not report P as the committed head C.
One possible physical organization is run snapshots with stable run IDs,
but the logical shape and physical layout need not be identical.

The engine loads current continuation state by applying committed records
since the checkpoint; it need not load the full pre-compaction conversation.
Older history remains retained.

Run boundaries alone do not bound size. A single run can contain many turns and
large tool results, while compaction can happen within a run and metadata
changes can happen outside one. Run snapshots therefore need bounded detail
pages or references, and session-level facts still need their own home. The
existing [API surface](../architecture/api-surface.md) distinguishes
run-scoped controls from session-scoped reads; neither run identity nor
session identity is a substitute for the session-wide commit position.

A compaction generation could later label which model-continuation checkpoint
is current and which retained display history predates it. A future client
could defer loading older generations, reporting what remains available
without claiming its partial view is the entire transcript. The current
model context, pending work, and unknown tool outcomes must be recoverable
without loading old display history. A generation marks a history boundary;
it does not replace commit ordering or authorize deletion of retained history.

The session catalog supports bounded, paged, authorized listing and exact
creation/status lookup without activating sessions. Its display-safe entry
projects existing trusted `SessionKind` and kind-specific
`SessionRelationship`: scheduled fires show their existing schedule relation
and fire-session identity; main sessions, delegated children, and other kinds
are not all human-initiated. Do not add a second generic origin field now;
future external triggers and schedule-group queries can extend the catalog
contract later. Derived display metadata in the catalog may lag an accepted
session-history commit: each projection identifies the position it reflects,
and a missed update must converge through replay or reconciliation rather
than remain stale forever. The session's committed history, not a stale list
entry, answers authoritative reads. Creation identity and ready/failed
publication, logical deletion, and the writer claim remain strict decisions,
not best-effort catalog decoration. Before acknowledging deletion, durably
suppress the entry and fence further writes; every list, exact lookup, and
history read must honor that tombstone even if derived display metadata lags.
If the adapter cannot prove the deletion gate for a catalog read, fail the
read rather than expose a stale title. The exact atomic or ordered storage
primitive belongs in the interface contract. Catalog-change notifications are
best-effort refresh hints, not a second durable history: clients resync with
bounded catalog queries after missed updates or reconnect, without requiring
the storage layer to provide a watch. Keep user-level settings and state a
separate future design.

### Forks and retained history

The initial cutover preserves head forking: an authorized, inactive main
session forks at its latest eligible committed continuation boundary, yielding
a new idle session. Because the history is now canonical, the fork also
receives its retained historical prefix through that boundary, including
pre-compaction facts; it must not manufacture a "since last compaction"
cutoff. Selecting an older completed main-run boundary, including while the
source has since resumed work, is a future historical-boundary fork. At that
point the fork rebuilds the model-visible continuation and display-history
prefix as they stood at the selected position, not from today's snapshot.
The fork has a new identity and records the source session and exact position
as lineage. Source activity after the boundary remains in the source; forking
neither pauses nor takes ownership of its active delegation tree. The fork
remains self-contained after source deletion, even if the initial storage
implementation copies the full selected prefix. An adapter may optimize that
physical copy later only if the fork's retention and readability no longer
depend on the source.

At admission, pin source position C and capture its metadata and fork policy
under a short source-side boundary. The accepted creation reserves a new
session ID and returns promptly with that ID and **creating** status; it does
not hold a client request open for the entire copy. The entry is visible in
authorized session listings, and exact creation-ID/status lookup reports
`creating`, `ready`, or `failed` (or a non-disclosing `deleted` for an exact
lookup after deletion). Retrying the same ID returns the same creation
decision. The copy is server-owned work after admission, not tied to
the initiating client's connection; a disconnect cannot silently cancel it.
While immutable history through C is copied, the source may continue new
runs. A creating entry exposes only safe metadata and its status; it cannot be activated, forked, or read as complete history.
Deleting the creating destination fences its copy and transitions it to a
terminal deleted outcome; a late creator cannot publish it as ready. Cleanup
of staged bytes may follow asynchronously. Publication does not require a
transaction over the full source and destination histories. After the copy,
a short conditional catalog decision orders each fork against source
deletion; neither new source runs nor other fork copies wait on this copy.
The source execution claim is not held through copy or reacquired merely to
finalize it. Deletion may proceed during any number of staged forks: it wins
against those not yet finalized, which fail and clean up if the source can no
longer be read. A fork whose final decision won first is independent, even
if its ready status becomes visible after the source's deletion response.
A fork request has a stable
client creation identity and an idempotently recoverable result. A crashed
creator may leave the catalog entry **creating** temporarily. A slower
reconciler makes every ready publication conditional on the destination
still being `creating`, even if the source-side fork decision already won.
Destination deletion wins over every later publication or cleanup retry; a
completed source-side decision is not permission to resurrect a deleted
destination.
Otherwise the reconciler completes publication for a finalized fork, or
waits until the creator can no longer finish before marking an incomplete
attempt **failed**. It does not mistake a slow live copy for a dead one or
silently publish partial history. Retrying the same creation ID after a
terminal failure returns that failure; a new attempt needs a new ID. A later
reconciler could finish abandoned copies instead, but must preserve the same
stable outcome contract. Cleanup of incomplete staging is separate from its
visible creation outcome. The exact fencing, status, and cleanup mechanism
belongs in the interface contract.

All fork/clone paths cross one explicit transformation boundary before
creating the new session. It selects inherited historical and continuation
facts, strips source execution state and effective authority by default, and
marks inherited provenance; adapters must not bypass it by cloning a raw
storage namespace. Some record payloads may remain byte-identical after
validation, but this is a semantic fork, not a replay of source ownership,
approvals, or other controls as new commands. A fork inherits conversation,
not execution: no pending asks, queued steers, live delegated work, scheduled
timers, or future background tasks transfer to it. If work eventually spans
run boundaries, the fork must exclude its runnable state and represent any
relevant historical fact without treating a reference as permission to
restart it. The source's workspace and external artifacts are not rolled
back to the selected point; placement and current authorization still need
validation for the new session. Historical rewind is a conversation fork,
not an external-world snapshot. Exact eligibility and inherited fields
belong in the interface contract. The fork copies the approved parent-side
delegation projections in that prefix, not the underlying child sessions.
A deeper child-transcript read from the fork reports unavailable; the
source parent's access to a live child is not inherited. Deep cloning or
point-in-time child references are separate future choices.

The server API owns fork inheritance semantics. By default the fork resolves
its mode and other operational settings from the installation's ordinary
new-session defaults and the verified caller; it does not silently copy the
source mode or session-learned permission grants (a deliberate change from
today's source-mode copy). Explicit per-setting overrides and an
"inherit compatible settings" preset let a caller explore two paths without
bulk approval prompts. If the caller opts into carrying learned grants, the
server re-derives eligible rules from authoritative private source approval
and tool facts, checks caller and placement compatibility under current
policy, and commits new fork-local authority decisions. Historical inherited
approval events remain facts, never active grants by themselves. A Deny or
configured Ask still wins at execution; ineligible grants are omitted rather
than triggering an approval ceremony during fork creation. No inheritance
option transfers pending asks, tool work, timers, or session ownership.
Bindings may offer ergonomic presets, but cannot implement authority cloning
independently of the server. Exact options and what an omitted grant reports
belong in the interface contract.

### Future same-session rewind

A rewind is a new committed branch-selection fact in the same session, not
a new session or deletion of later commits. It selects an earlier **clean run
boundary** for model continuation; an eligible boundary follows a fully
settled run (including a failed or user-cancelled run with valid call/result
pairing), not an arbitrary message or mid-run steer. The current delegation
tree must also be settled, with no pending approval or live child work that
would be stranded. The client offers only eligible boundaries. The next
model request sees the selected conversation path; rewind does not restore
earlier session permissions, unspend reported usage, rewind files or other
external state, or restart scheduled timers and background work. External
side effects on an abandoned path may still have occurred. The session-wide
commit order and positions remain linear and append-only, with one active
continuation path. Abandoned paths remain retained and inspectable, for
example as collapsed branches in the client; their tips stay identifiable
so future reactivation can be designed without reconstructing deleted data.
Rewind controls and branch projection are advanced work, not part of the
initial committed-history delivery.

## Client API

The server projects committed records into authorized, display-safe session
facts and timeline items. The API does not expose persistence records or
require clients to understand their storage layout. Its snapshot, history
pages, and committed subscription share the same session-wide position,
while provisional updates advertise their weaker status separately. In
conceptual terms, the read API returns a session summary, a stable paged
history at its position, and an after-position committed subscription. A
provisional channel supplies low-latency observations and cumulative
commit acknowledgements. The provisional and committed channels may share
a transport; their status and cursor semantics remain distinct. These are
API responsibilities, not a commitment to particular RPC names or physical
tables. Inspecting the summary, paged history, or committed subscription is
a read-only operation: it does not claim ownership, load an engine, or start
execution. Distinguish **observe** from **interactive attach** at the client
boundary. An authorized interactive attach may acquire the tree claim and
load continuation before driving eligible work, even without a new prompt;
it does not change the session's permission mode or confer write authority
by itself. An approval or other authorized mutation may also enter the same
activation funnel without a separate attach round trip. A client connection
or committed subscription alone does not pin an owner; a provisional stream
is available only while there is live owned work and may end when work parks
or completes. A historical read cannot activate execution as a side effect.
The exact API spelling is deferred.

The initial client reload loads the complete authorized display history
across bounded pages; one unbounded response is not required. Every page
belongs to one immutable view at committed position C. Page tokens identify
where to continue in that view, not where to resume live events, and are bound
to the authorized session and view. Only a successful final page attests
that the authorized history through C is complete. If a page expires, fails,
or no longer matches that view, the client discards the incomplete load and
starts a fresh snapshot. A material authorization change invalidates the
previously authorized view and its page tokens; access is checked again on
later page and subscription reads, not conferred by possession of a token.
Initial policy may be coarse; finer caller-specific filtering can follow
without making old tokens permanent grants. A disconnection by itself does
not revoke access. A silent cutoff must not look complete.

Once the pages are complete, the client follows committed changes after C.
Changes committed while it loads must remain resumable from C; otherwise the
server reports an expired position and the client reloads. Page the parts
that grow, including run inventories and conversation/detail records. A
single large tool result or media item needs bounded content reads or an
explicit size failure rather than an oversized page. A later client can
load older pages on demand without changing this read contract.

Bound the canonical session payload before it reaches either the model or the
history commit. Reject oversized submitted media before accepting the input;
for an oversized tool output, apply an explicit limited-result contract and
commit exactly the representation the model receives, with a truncation or
external-reference indicator. Never send full output to the model and save
only a shorter version as if it were the same result. A future file-spillover
adapter may keep large artifacts outside session history: the history retains
the tool command and parameters that produced them, the admitted excerpt
seen by the model, and any authorized reference needed to fetch more. It does
not claim the entire spilled artifact is retained by the session log. Such an
artifact has its own storage, access, and lifetime contract; spillover is not
implemented by this proposal.

### Multiple interactive clients

Multiple authorized clients may attach to the same server-owned run and its
live feed. There is no exclusive controller. Every action is authorized
independently, and the owner orders conflicting actions against committed
session state. Reading history grants neither control rights nor execution
activation. Disconnecting a client does not transfer control or cancel the run.
This differs from the current prompt-started `Converse` stream described in the
[API surface](../architecture/api-surface.md), whose relay cancels on disconnect.

Run admission is shared by every producer, including user prompts and future
extension-triggered work. Only one eligible run starts. A competing prompt
receives an explicit conflict identifying the active run; the server neither
queues it silently nor interprets it as a steer. A client may choose to hide
that race by issuing a separately authorized, exact-run steer. The named run
may have ended by then, so that steer can also fail stale. Admission failure
must not erase an already committed background completion.

### Source-scoped steers

A steer is replaceable pending input, not an accumulating instruction queue.
There is one shared user-steer slot per session and one independent slot per
registered extension. The extension itself is the initial coalescing key;
there are no extension-defined subkeys. All slots have the same set, replace,
retract, and revision-specific consumption semantics. Updating one slot never
changes another. An extension may act through the same authorized session-action
semantics as a client, but controls only its own extension slot and receives no
implicit permission to answer asks or bypass execution gates.

An unqualified steer requests eligible execution: it becomes pending input for
an active or awaiting run, or requests a new run when idle. An exact-run steer
can only target that run and never promotes to another. This preserves the
existing distinction between `Service.Steer` promotion and strict `SteerRun`
controls. Admission and the terminal race must not lose an accepted steer;
recovery and cancellation dispositions are described below.

At the next safe model-input boundary, deliver all pending source slots
**together**, in a deterministic order. This does not interrupt an in-flight
model request or bypass an awaiting approval. Commit the admitted input and
consumption of each delivered revision before sending it to the model. A
replacement admitted after that delivery decision remains pending for a later
boundary; consuming an older revision cannot clear it. The exact ordering
algorithm and framing belong in the interface design.

The server stamps each item's provenance so the model can distinguish user
input from a named extension, tool, or harness source. Text cannot declare its
own trusted source or become an authority-bearing instruction by claiming to
come from the harness. Non-user notification content uses the appropriate
untrusted-data framing; source attribution does not elevate it to system-prompt
or permission authority.

An extension steer contains a small, size-capped hint: enough operation identity,
status, and context for the agent to choose a retrieval tool. Full results belong
in the extension's canonical committed state, and the agent calls an authorized
tool to retrieve results and explore further. Because each extension has only
one pending slot, its replacement hint must cover outstanding work, for example
by pointing to a tool that lists ready results. Coalescing the hint must not
remove operation outcomes. Cross-run background work and completion-triggered
runs use these semantics when that later capability is introduced; defining
them here does not expand the first cutover.

### Shared pending controls, not client drafts

All interactive clients operate on the same user-steer slot. Submitted pending
content, its revision, and its disposition are committed server state, reflected
through authorized reload and subscription projections rather than only an echo
to the submitting connection. A user can submit on one client and inspect,
modify, or retract the pending steer on another. Updates and retractions name
the expected slot revision: a stale client receives a conflict rather than
silently overwriting another client's edit. The exact conflict payload and
receipt lifetime remain interface questions.

Unsent composer text, focus, filters, and other presentation state stay
client-local. Draft persistence and synchronization are outside this design.
Retracting the user steer does not retract an extension steer, or vice versa.

Pending approval asks and committed resolutions are likewise reflected to all
authorized clients. The first valid, authorized **committed** resolution of an
exact ask wins. A later conflicting response receives an already-resolved
outcome with the authoritative decision; it cannot reverse authorization that
may already have allowed execution. A retry of the winning action resolves
through its existing action identity. Unsubmitted approval responses remain
client-local, and no client connection reserves an exclusive responder role.

### One client timeline

Expose the authorized display history as one session-ordered timeline, not a
collection the client must merge run by run. Every committed timeline item has
an order in the session history. Items associated with a run carry its stable
run ID for grouping; session-wide changes also have a place in that same
order. Run snapshots may organize storage, but the public read contract hides
that partitioning. The current session metadata and model-continuation
checkpoint remain distinct from the display timeline.

Prefer the same client-safe item schema for paged historical items and
committed subscription updates. Then the client can apply one projection
path to both, preserving identity, order, and revision across reload. This
is a shared *client projection*, not a proposal to expose private log records.
Provisional streaming uses its own status and correlation identity until a
commit establishes an authoritative item and position. Aggregate facts,
such as current usage and permission mode, may still be served as snapshot
fields so a client need not infer current authority by scanning old activity.
The exact payload families and how they reconcile with provisional updates
belong in the later interface contract.

### What reload restores

Treat a durable, client-visible session fact as recoverable by a new client
process. That includes the conversation and completed human-visible reasoning
summaries (separate from provider-private replay material), tool calls and
committed outcomes, delegation inventories and bounded activity, changed-file
facts, and confirmed session metadata and reported usage. Large details
use bounded reads; privacy and authorization still govern what the client
may receive. [Issue #2114](https://github.com/stacklok/mecatl/issues/2114)
will inventory each item and name any exception rather than letting a
best-effort feed silently determine what survives. Reasoning-specific
representation belongs with [issue #2079](https://github.com/stacklok/mecatl/issues/2079).

A parent's committed history retains the full **admitted** Subagent tool
request and canonical final result, including the prompt and response that
today's `/toolcalls` detail shows. Size enforcement happens before the
request is dispatched or before the result is committed and shown to the
model; a display preview must never masquerade as the complete canonical
result. Fork and reload use those same parent-owned facts, not duplicate
full-prompt/full-result copies in `SubagentPayload`. The parent timeline also
retains stable call and child links, concrete model, status, reported usage,
and selected bounded child tool-call/result activity so Subagents, Parallel,
and Teams views survive a fresh client load. The current `SubagentPayload`
start/tool/end taxonomy is a useful starting shape, not a promise that its
short `Goal` contains the full prompt or that its rune-capped `Text`/`Detail`
previews are secret-redacted. Parallel and Teams retain their approved
inventories, winner facts, bounded activity, tasks, and findings. These are
parent-visible facts, not a copy of
each child's conversation; loading the parent must restore them without
depending on an optional child fetch. Keep the child's full transcript and
private continuation state in its own session. A parent-scoped request for
deeper child detail resolves the child from a verified relationship and
passes a policy seam for the caller, parent, child, and requested detail.
Possessing a child ID or being allowed to read the parent is not a blanket
grant to the child's content.
The same policy must govern a live view and a view restored after reload;
a reconnect cannot widen or narrow access merely because one client's
in-memory fleet was lost. Recheck authority for every detail read, including
reads after a reconnect. If the policy denies access or the child cannot be
read, preserve the parent summary and report detail unavailable rather than
inventing a complete child history. Which policy grants deeper content and
how much of it to expose remain open interface decisions.

Presentation state such as focus, filters, scroll position, and spinners is
client-local. Token deltas and transient progress are provisional and may be
lost. Retain provider-reported usage as committed facts tied to stable
physical-attempt and run identities, including reported usage from failed or
retried attempts that produce no completed turn. A completed model turn also
carries its own outcome and attribution; commit retries must not count the
same attempt twice. Per-turn, per-run, and cumulative session **reported
usage** are derived from recorded facts rather than maintained as independent
authorities; a checkpoint may cache the sums at its position. A new client
can reload the confirmed reported usage and attributed detail without
reconstructing them from a transient stream. Usage reporting is best effort:
a provider may not report it, and a process may die before committing it.
Neither the API nor client should label the sum as guaranteed total spend or
complete consumption. Missing usage is unknown, not an actual reported zero.
When a fork copies usage facts, preserve each physical attempt's original
session and attempt identity as inherited provenance. New attempts on the
fork have fresh identities; its own reported usage begins at zero. Derived
cross-session accounting counts inherited attempts once by origin, including
through fork-of-fork lineage. The UI treatment of inherited versus new usage
and any future `/usage` command remain separate design decisions.

Session history owns usage from work executed as part of its runs, not every
auxiliary operation that happens to inspect it. Reflection and other work
without a session writer need a separate durable state/accounting owner in a
future design, with purpose/type and provenance adequate for a joined usage
view. Do not mutate the source session solely to attribute independent work;
the exact auxiliary accounting contract is outside this proposal.

### Delegation detail budget

"Bounded" has three independent meanings: which child fields may be disclosed
to a parent, how large each preview may be, and how much activity the parent's
current view retains per child or member. The live UI currently keeps twelve
trace entries per lane and forwarded text previews are capped at 200 runes,
but consecutive text deltas can coalesce into one growing entry. Neither
limit by itself caps total retained bytes, and control-character scrubbing
is not secret redaction. The new server projection needs an explicit field
allowlist, per-field and aggregate byte limits, and an honest truncated
indicator, enforced before it persists or serves a parent-visible detail.

Retain every *approved* parent-visible committed item in the parent history;
the compact current view may show only the latest bounded window. Older
approved items remain available through the paged timeline. For the initial
projection, retain only child message and tool snippets already forwarded to
the parent today, subject to explicit per-field and aggregate byte caps; do
not add new child content fields. They may contain sensitive text: making
them mandatory, long-lived history increases retention, and no secret
redaction is promised. The same approved projection governs live and reload;
a short live prefix does not license copying any other child-private content.
The exact existing-field mapping and budgets belong in the interface review.
A reusable redaction subsystem, if wanted across product surfaces, is a
separate future design, not an implicit feature of this proposal.

Fold provisional child message deltas in memory and commit approved parent
activity only at stable child message/turn, tool intent/result, and terminal
boundaries; the live typing animation need not survive reload. Normally the
child commits a settled fact and then the parent commits its bounded
projection before that child advances. A crash between these separate
histories can leave the parent missing a settled child item. Recovery uses
the child's committed facts for execution safety but does not reconstruct or
backfill missing parent snippets. A read-only parent reload needs no child
fetch: without a committed terminal parent projection, it shows the child as
**unresolved / activity unverified**, not as complete, even if the child has
in fact finished. Recovery later inspects the child's committed state and
appends a parent-side activity-gap marker with stable child identity and
bounded evidence before settling the parent view. Reload and forks retain
the gap rather than silently presenting a complete child trace. A
projection gap is not itself an unknown tool outcome; those follow the
child's intent/result evidence. If the child committed a terminal result but
the parent tool result is missing, recovery may commit a known parent result
only from exact durable result bytes or a versioned deterministic derivation
that reproduces them. A status such as "child completed" or a bounded display
summary is insufficient. If that evidence is absent, close the parent's call
with an honest unknown-outcome result; do not rerun the child merely to fill
the gap. The first cutover need not build a general recovery synthesizer.

Leave the disclosure decision at the child-to-parent projection boundary,
before any parent commit or live publish. That leaves room for a future
opaque subagent: its private activity would stay in the child session, while
only explicitly approved parent-visible facts and the tool's intended result
cross to the parent. A read-time filter cannot make content already stored
in a parent's retained history private retroactively. This proposal adds no
opaque mode or new configuration; it preserves the boundary where such a
policy could be applied later.

### Live delivery and acknowledgement

A run sends provisional model text and reasoning summaries as they arrive; it
does not write one record per token. The client may lose an entire uncommitted
model response on reload if the server failed before committing it, even if
the client saw the response live. The server commits the completed assistant
turn when it has an accepted result. Tool progress and early result
previews can also be provisional; tool dispatch and outcome follow the
separate durable boundaries below.
Provisional updates have correlation identities so the client can amend or
discard them; they are not resume cursors. After storage accepts a change, the
server advances a cumulative acknowledgement such as **"persisted through
C"**. Only a committed position is a durable subscription cursor. If a
provisional update differs from the committed result, the client reconciles
to the committed projection. Ownership changes end the old live stream,
including one forwarded through a non-owner; there is no transparent
cross-replica stream migration. The client reconnects through any entry
replica from its last committed position, reloading a snapshot if that cursor
is invalid, and reconciles or discards provisional output. A clean handoff
may report a typed reconnect outcome; a crash may only break the transport.
Neither case proves the run stopped. If the connection drops before an
acknowledgement, the client reloads from committed state; it does not assume
the provisional text survived.

An acknowledgement means the authoritative storage adapter accepted the
record under its declared durability contract, so a mecatl process restart
can recover it; it never means only that the process queued a write or sent
bytes to the client. How storage-node failures affect an accepted record is a
backend deployment guarantee, not one blanket promise of this protocol. An
adapter must not report a volatile write as committed without disclosing that
contract. Provisional delivery must never confer a permission grant or present a tool
outcome as certain when the server cannot substantiate it. The split between
provisional and committed output preserves low-latency streaming without
making the live feed the source of truth.

## Recovery and execution safety

### Distributed recovery and evidence

A committed run-start record with no committed completion proves only that the
outcome is **unresolved**. A lost client connection does not prove the run
stopped: another server may still own and drive it. The protocol should
separate transport connection state from server-confirmed run state and report
what the server can establish: a current owner, a fenced former owner, or
ownership that cannot yet be determined. A client may present these facts
compactly, but it must not manufacture a completed or failed run from a
missing event.

Once the server establishes that no writer can continue the old run, it can
commit an interrupted-run marker. This explains why previously streamed
model text is absent from the recovered conversation. Even after fencing a
writer, an already-started external tool may still have had a side effect;
its outcome remains unknown until verified. If ownership or storage is
unavailable, the protocol reports uncertainty rather than claiming the run
has ended. Committed positions, writer identities, and bounded recovery
status must remain meaningful when the client reconnects to a different
replica. A backend response showing a newer writer epoch is definitive for
that old writer: the server stops starting work and cancels its local run.
Its host may recheck a Kubernetes Lease to diagnose or reconcile ownership,
but that check cannot authorize another old-epoch append; a fresh owner must
acquire the current backend epoch and reload committed state. This remains
server-side coordination, not a Kubernetes dependency in the engine core.

A failed or timed-out claim renewal is uncertainty, not permission to start
another tool. Already-started calls may settle, but their outcomes become
confirmed only through an epoch-checked commit. A narrow speculative exception
can dispatch one next LLM request if its exact input and continuation were
already committed while ownership was valid; its streamed output remains
provisional and cannot trigger a tool, a second model request, or confirmed
usage until its result is committed under a valid epoch and expected head. If
takeover wins, discard that response. This may incur duplicate provider cost;
it is not a generally read-only operation. No new model request can be built
from uncommitted state. If an ordinary append succeeds with the current epoch,
that operation has confirmed its own write authority; if storage cannot
confirm it, do not advance dependent work. Once renewal confirms the same
epoch, the owner reads the committed head, reconciles any in-flight outcomes,
and resumes in place. A newer epoch is a hard stop. Even an apparently read-only tool currently requires a committed intent
before dispatch and a committed result before model continuation. A future
exception for demonstrably side-effect-free reads would need an explicit
purity and privacy contract; tool names or declared read-only classification
alone cannot authorize speculative calls across an uncertain fence.

Crash takeover and agent continuation are separate decisions. In the first
cutover, a crashed active run is **interrupted**, not resumed under the same
run identity. After fencing the former writer, recovery commits the evidence
needed to repair tool pairing: requested calls with no intent were not
started, and intent without an established result has unknown outcome. No
tool is automatically retried. A later authorized interactive attach may
start a new run from the committed conversation. If only an uncommitted LLM
response was lost—no tool intent, pending steer, or approval requiring a
decision—the server issues a new model attempt from the last committed
input on that attach, independent of which client UI initiated it. The provisional response seen before the crash can be
discarded and replaced; the new response may differ, and the old attempt's
usage may be unknown. Read-only inspection never triggers it. Pending
steers and uncertain tool outcomes require explicit post-crash recovery
decisions before new dependent work; no-client automatic agent continuation
is deferred. A durably awaiting exact approval is preserved for an
explicit verdict rather than converted into an interrupted run. Generic
orphan settlement applies to every session kind, including scheduled fires
and delegated children; it neither restarts them nor knows schedule retry
policy. The scheduler independently reconciles its due claims and fire
records from the settled session state, without a competing direct session
settlement path. An unobserved crashed scheduled fire does not replay on
client activation; later due fires proceed. Existing opted-in one-shot retry
remains a schedule policy, not a general same-run crash-resume promise.

A later same-run crash-resume feature may store a per-root-session
continuation policy initialized from a deployment-wide default. Delegation
would follow the root's policy; changing the default would not rewrite
existing sessions. Its conservative default is to park the recovered tree,
while an opt-in could let the agent continue with an explicit unknown-outcome
result in model-visible context. A separate authorized resume would continue
the same run after fencing and reconciliation, never rerun the uncertain
tool. This policy, toggle, and same-run resume action do **not** ship in the
first cutover. Who may change a session's policy and when remain later
decisions; do not create a client toggle now.

A steer is durably accepted into its source slot with a stable action identity
and slot revision before the server acknowledges it. Accepted content,
replacement, retraction, and consumption survive a crash or handoff; acceptance
does not mean input has reached the model. During ordinary execution, the
owner commits the pending revisions' insertion at the next eligible input
boundary (after any required approval verdict) before the model sees them. All
clients reconcile their pending controls against that committed insertion,
not an ephemeral inbox echo. In the first cutover, a crash that interrupts a
run leaves unconsumed steers pending for an explicit post-crash recovery action:
the authorized caller chooses a new follow-up run with that input or retracts
it. The server does not silently promote it, discard it, or require the client
to resubmit accepted text. A later same-run resume could instead consume it at
the resumed run's eligible boundary. Retried steer and recovery commands use
stable identities so the same input is not inserted twice. Durable pending
steers deliberately differ from today's restart-lost in-memory inbox.

The terminal race is resolved by the server, not by making the client wait
for a turn to finish before sending. A steer accepted while a run is active
can be consumed by that run if it continues; if the run becomes terminal
before consuming it, the server must make an explicit durable decision about
its fate. For an unqualified steer on a normally completed run, the intended
behavior is to promote accepted pending input to one follow-up run rather than
return it for client resubmission. Exact-run steers cannot authorize promotion;
their accepted, unconsumed terminal disposition still needs an explicit durable
decision. An interrupted first-phase run leaves pending input for explicit
post-crash disposition; a future same-run crash-parked run is not terminal and
cannot authorize promotion. A server may reject a steer before accepting it,
but once it has acknowledged durable acceptance, returning its text in a terminal response
cannot transfer responsibility back to the client. The choice of recipient
run or durable pending state must be ordered at the committed session head
and replayable by message identity; retries never create a second follow-up.
An explicit user cancellation of the targeted run also cancels its pending,
undelivered user steer. Commit the run's cancellation and that steer disposition
together so a crash cannot leave accepted user input that later starts a run
contrary to the cancellation. The effect of run cancellation on independent
extension slots and future background operations remains an open question;
retracting the user slot alone never affects them. A steer already inserted
into committed
conversation cannot be retroactively cancelled. A terminal response may
include the discarded input as a convenience note, but delivery of that note
is not guaranteed and is not the cancellation record. An internal shutdown
or lease-loss cancellation is not user intent and must preserve pending
steers for fenced crash recovery. If a crash occurs without an accepted user
cancellation, pending steers remain durable for the first-phase explicit
follow-up-or-cancel decision; a later same-run resume could consume them.
A cancellation addressed to an old run cannot cancel a follow-up run already
created by a terminal-race promotion. The broader multi-client cancellation
targeting contract remains to be settled below. If the run reaches a committed failed
terminal outcome before consuming an accepted steer, leave that steer durably
pending rather than automatically promoting it. The caller can inspect the
failure and explicitly choose to start a follow-up run with the pending input
or cancel it; a failed run is not implicitly resumed. This disposition also
survives reconnect and does not ask the client to resubmit accepted text.
No-client autonomous takeover and agent continuation remain later
capabilities; in the first cutover an unobserved interrupted run waits for
an authorized client activation or recovery decision.

When a mutation or provisional stream lands on a non-owner replica, that
replica consults the authoritative ownership claim and forwards it over an
authenticated internal transport to the current owner. The receiving owner
rechecks the caller's authority, tree identity, and current epoch; ingress
affinity metadata is not an authorization grant. Forwarding is bounded to one
hop, with no forwarding loop or private pod address exposed to clients.
On handoff or a stale owner hint, the entry replica refreshes ownership;
an ambiguous mutation keeps its stable request identity so a retry resolves
against the original decision. If ownership or routing cannot be established,
the client receives a retryable unavailable outcome rather than an invented
success. The client retains its committed cursor and queued request across
reconnect. Session affinity may reduce forwarding but is not required for
correctness. Any authorized replica can serve durable snapshot and committed
catch-up reads from the shared history. An owner-aware gateway could later
avoid the forwarding hop; it is not required in this design.

### Compatible-replica active-tree handoff (later phase)

The initial committed-history cutover does not promise turn-boundary pause
and seamless cross-replica continuation of an active delegation tree. That
capability does not ship today. The first phase must still preserve or
explicitly replace today's bounded cancellation and join-before-release,
durable pending root-ask rehydration after takeover, refusal of work before
the ownership boundary, and crash-orphan repair with valid tool-call/result
pairing. It must not describe a cancelled run or a new client-started run as
continuation of the old active tree. The following is the future handoff
contract the history must accommodate, not a first-phase delivery claim.

Drain is a committed pause, not the end of a run. The owner marks the entire
delegation tree as draining and admits no new model or tool calls in any member.
Each member finishes its active calls, commits their outcomes and continuation,
then parks at a turn boundary without starting the next call. A pending
human-in-the-loop approval is already a parkable continuation: its exact ask
and decision state survive handoff without waiting for the human. The parent
retains the delegation tool call and its child links as pending across the
handoff; parking a child is not a tool result. A delegated tool must support
this coordinated pause instead of tying its logical call lifetime to one pod.
An approval that arrives during drain can be accepted against its exact pending
ask and committed with its decision identity, but cannot start dependent work
on the draining owner. The successor consumes that committed decision on
resume; a retry with the same identity returns the original decision. Once
the owner has closed approval admission for final handoff, it returns a typed
reconnect response without accepting the verdict. The client keeps the same
request identity and queued verdict, reconnects, and retries through any
entry replica rather than inventing a new approval. The entry replica
forwards to the successor once it has acquired ownership; until then, a
bounded retry may report temporary unavailability. An ambiguous acceptance
is resolved from the committed history before either replica claims the
verdict was accepted or asks the client to make a new one.

Only after all descendants are parked and no LLM or external tool call is in
flight does the owner commit a root handoff marker containing the parent's
parked continuation and naming each descendant's exact committed parked
position, then unload local tree state and release its exact claim. Child
histories may be stored independently and committed before the root marker; no multi-history
transaction is required. The marker is the publication boundary: a successor
loads and verifies every named position before resuming any member. A missing
or incompatible child position fails closed, never as a partial handoff.
A crash before the marker leaves an incomplete drain for fenced recovery; a
crash after the marker but before release leaves a complete handoff for
successor takeover after claim expiry. Approval admission closes before the
marker, so its recorded positions cannot omit an accepted verdict. The
handoff is durably discoverable by healthy replicas, not dependent on a
client opening the session. Once autonomous takeover is enabled, a background
successor worker claims an eligible tree with a new epoch, loads its complete
parked state, and resumes any runnable member, including a background child
while its parent awaits human approval or a member with a committed verdict.
An undecided ask parks only its dependent continuation; it does not block
independent runnable members. If no member can run, the tree remains unowned
and unloaded until an authorized client explicitly activates it. Inspecting
its committed history alone never claims it.
Competing workers cannot both drive the tree:
claim acquisition and every subsequent child or parent commit are fenced by
the same ownership domain. Missed wakeups cannot strand a released runnable
tree once autonomous takeover is enabled: a bounded scan or equivalent durable
discovery finds it. If that worker is deferred from an initial phase, the
handoff remains durably parked but is not an unattended rolling-upgrade
continuation; that limitation must be explicit rather than promising self-start.
A partially parked tree is not a completed handoff. Child history commits
must be fenced by the tree's ownership claim even when stored under separate
child IDs. The exact marker encoding and recovery checks belong in the later
interface contract; independently releasing child claims is not a substitute.
Drain is per root session and its delegation tree: a slow tree does not hold
back complete handoffs of other sessions. At the shutdown deadline, cancel
and join any remaining in-flight calls where possible. If that tree cannot
establish all outcomes and commit the root marker before shutdown, do not
release it as a graceful handoff. Its claim expires and a successor applies
crash-style recovery, including unknown external outcomes; other completed
trees retain their graceful markers and immediate releases.

### Tool calls and uncertain outcomes

Commit the completed assistant turn with its requested tool-call identities
before dispatching any of them. For each call independently, commit its
authorized intent and private continuation state immediately before dispatch.
The model's tool-call batch is not a storage or execution transaction:
permitted independent calls may overlap under the harness's existing
read-parallel/mutate-serial discipline. A client-safe projection may show an
admitted call in flight. Commit each result under its original call identity
when established. A requested call with **no** committed intent is definitely
not dispatched by this protocol; a committed intent with no committed result
means that call **might have started**. A pending durable ask remains pending,
not a not-started result while it is still actionable.

An interrupted call is not automatically retried. Once recovery has fenced
the former writer and checked for a committed result, it closes an orphaned
requested call with a harness-authored **not-started** result if no intent
was committed, or a distinct **unknown-outcome** result if intent was
committed but its outcome cannot be established. Each uses the original
call identity and completes model-visible call/result pairing. Not-started
asserts the dispatch gate prevented execution; unknown outcome does not claim
the tool failed or had no effect, and the external operation might still be
running. The agent can inspect the situation and decide how to proceed: verify external
state, retry an idempotent operation, or ask for help. Any follow-up remains
subject to normal permissions and tool boundaries. Until recovery commits
that result, the session cannot feed an invented result to the model or start
new dependent work. Correlation prevents duplicate client cards; it does not
make an external side effect exactly once. We still need to decide which
tool-start phases and post-crash recovery facts are meaningful across
implementations.

### Execution gates

The following boundaries distinguish fast observation from permission to
advance the session. "Committed" means the authoritative store has accepted
the record, including its ownership check; an in-process queue is not enough.

| Action | Required before proceeding | What can be provisional |
| --- | --- | --- |
| Start a run or send its next model request | Commit the accepted input, run identity, and the continuation state the request uses. A later request also waits for the preceding turn's committed outcome. | Stream the current model response without persisting each token. |
| Present an approval | Commit the pending ask and exact private continuation state before treating the approval as actionable. | Render an unconfirmed request as waiting, not as an exercisable grant. |
| Dispatch each tool call, including a delegated agent | Commit the completed assistant turn with its requested calls, then commit that call's authorized intent and identity before its dispatch. If its intent write fails, do not dispatch it. | Show that the call is being prepared; progress and result previews after dispatch may be provisional. |
| Continue after a tool returns | Commit its canonical result before feeding it back to the model or claiming a final outcome to the client. If storage cannot establish that result, stop dependent work until recovery commits a known result or a distinct unknown-outcome result for the same call. | Show a clearly provisional preview while the result commit is pending. |
| Use compacted history or changed session controls | Commit the replacement conversation or control decision before using it for another model request or presenting it as confirmed. | Show that the operation is pending. |

A failed result write after dispatch cannot undo a tool's external side effect.
The server must stop dependent work and expose the unresolved call until it
can establish a durable outcome. If an append reports an ambiguous failure,
the server must verify the committed position and call identity before deciding
whether to retry; repeating a tool call is never a storage retry.

## Boundaries and costs

This is a deliberate breaking change to persistence and the client protocol,
not an in-place schema version bump. Use a distinct storage namespace for
committed histories and a versioned format/capability marker within it. New
adapters accept only that format and reject legacy snapshots or activity logs
before reconstructing a session. A legacy session or mixed namespace is not
an empty new session and must not be rewritten by a new writer; namespace
separation does not excuse treating a known legacy session ID as absent. The
exact namespace, marker, and collision detection belong in the interface
contract. Existing bytes remain untouched; except for fail-closed legacy
presence detection, the new server and client do not read, expose, continue,
or migrate old sessions. The initial rollout is a teardown and upgrade, not
a mixed-version rolling upgrade: stop the old service before admitting new
traffic. Keep the same public endpoint, but explicitly negotiate or check
the incompatible API version before any session read, subscription, or
mutation. A new client against an old server and an old client against a new
server fail clearly; neither silently falls back to the other's snapshot,
feed, or storage format. The exact wire mechanism belongs in the interface
contract. Graceful handoff among compatible new replicas is a later runtime
behavior, not a promise that old runs migrate through this cutover. The old
service uses a bounded drain before teardown; remaining legacy runs may be
interrupted and cannot be resumed by the new server. Any already-started
external side effect remains subject to the old protocol's uncertainty, not
retroactively protected by the new history. This is an explicit cutover cost,
not a reason to mutate or delete the legacy namespace. A future offline
export/import would be a separate decision, not a condition
for the first release. This proposal does not authorize deleting old data.
This clean break deliberately ends legacy-session continuation compatibility.
The authoritative committed subscription replaces the best-effort activity
watch as the source of client reload truth. Historical
activity may remain a separate diagnostic view, but it cannot attest session
completeness.

Logical deletion is an irreversible session transition, independent of when
the adapter physically purges bytes. It fences further activation and writes,
prevents reuse of the deleted identity, and denies new reads of that session's
history even if retained records still exist. Deleting a root also logically
disables its delegated children by default: no direct child read or resume
may bypass the root's deletion, including during partial cleanup. A root
deletion boundary can make the whole tree inaccessible before individual
child histories are physically removed; do not require an atomic purge across
all child stores. The storage adapter declares its retention and purge timing,
which may be stricter. The public catalog and history reads scrub a deleted
session completely: no title, prompt, source hint, or descriptive metadata.
A non-enumerable minimal creation-ID/session-ID tombstone may remain solely to
reject late creation retries and reuse of the deleted identity; an exact
creation-ID lookup reports deleted without returning substantive session
data. Creation IDs must be bounded opaque values, not user-authored labels.
The tombstone does not imply the adapter has physically purged all prior
records; retained bytes remain inaccessible through the logical API until
the adapter's declared purge. A future explicit detach action could preserve a child
before root deletion, but is not part of this proposal. Forks are
independent peer sessions with lineage, not children owned by the source:
deleting the source neither disables nor deletes an existing fork. Other
separately created peer sessions are likewise unaffected.

A committed history is more expensive than a best-effort activity log. Storage
failure must stop claims of durability, while already-started external work
may remain uncertain. Checkpoints, storage growth, and access control must
work for long sessions without exposing private records or allowing a cursor
to skip required history. Forks, child sessions, and multiple server replicas
also need ownership and lineage rules. Subpackages should follow these
contracts once defined; relocating the TUI model by itself would not resolve
the persistence gap.

An adapter may eventually batch or buffer writes while preserving these
acknowledgement and ordering guarantees. The initial design assumes no such
relaxation: acknowledging an in-memory buffer as "persisted" would mislead a
client after a crash. Any later buffering proposal must define its durable
boundary, backpressure, failure handling, and cross-replica recovery separately.

This borrows ActiveGraph's [commit-before-projection
rule](https://docs.activegraph.ai/concepts/events/index.md) and [stale-writer
rejection](https://docs.activegraph.ai/guides/operating-in-production/index.md),
not its graph ontology or runtime. This draft and its companion model retain
the unresolved design questions while the discussion continues.

## Questions to resolve before interface review

1. The detailed shared-control contract: exact-run cancellation targeting for
   multiple clients, cancellation's interaction with extension slots and later
   background work, and the disposition of an accepted exact-run steer when
   its run ends before consumption. Shared attachment, prompt conflict,
   per-source coalescing, all-together delivery, and first-committed approval
   resolution are settled directions above. Ordering algorithm, provenance
   encoding, slot-revision conflict responses, and notification byte caps
   remain interface details.
2. The exact #2114 display-fidelity matrix, #2079 reasoning representation,
   and per-field/aggregate budgets for retained client and delegation detail.
   Which client-safe items must commit before progress and which rare
   cross-history gaps are explicitly surfaced?
3. Exact namespace/marker and legacy collision detection, backend-neutral
   claim and append interfaces, local torn-record diagnosis, and client API
   versions, history pages, authorization-view invalidation, and live cursors.
4. Action-identity format, scope, receipt lifetime, conflict/stale responses,
   and the catalog's atomic creation, deletion, and conditional fork decisions.
5. Storage layout and scale: checkpoint/replay bounds, large content reads,
   catalog pagination and reconciliation, and provider-specific durability
   declarations. A run boundary alone does not bound history size.
6. Extension registration and version compatibility, mandatory versus
   display-only state, and the exact generic operation/child-link contract.
   Later work may add active-tree handoff, autonomous run resume, historical
   forks, same-session rewind, and networked extensions; their hooks must not
   be mistaken for first-cutover behavior.

## Related information

- [Companion domain model](session-commit-history.modelith.md)
- [Model source](session-commit-history.modelith.yaml)
- [Implemented persistence and reliability](../architecture/observability.md)
- [Implemented API surface](../architecture/api-surface.md)
- [Draft index](README.md)
