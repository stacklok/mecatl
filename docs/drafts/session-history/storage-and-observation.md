# Storage and observation

**Review question:** What does durable acceptance mean, and how does a client load a complete view and follow it?
This draft covers storage authority, ownership fencing, and authorized reads, not physical schemas or transparent stream migration.

## Why one writer must win

Suppose two server replicas both believe they own the same conversation.
If both could record conflicting next steps, there would no longer be one history from which to recover.
The storage backend therefore decides which writer may append and which history boundary that append must extend.
A client connection, a live event, or a server's local belief is not that authority.

A distributed owner has an expiring claim over the root session and its active delegated children.
Each new ownership generation gets a writer number, called an **epoch**.
The backend accepts an append only if the writer is still the current generation, its claim has not expired, and the append extends the expected end of history, called the **head**.
Those checks happen atomically using the backend's ownership clock.
An old replica is thereby **fenced** from writing, but fencing does not kill an external tool call it already started.
The successor must still use evidence, not assume the old work stopped.

Acceptance also needs a declared durability promise.
A local filesystem adapter and a replicated backend do not necessarily survive the same failures.
The server can confirm a decision only when the adapter's actual acceptance contract has been met, not when a queue or inner transaction has merely buffered it.
If previously acknowledged history is missing, the server stops writes, loads, and confirmed views: it cannot repair the authority from a replica's memory and continue as if nothing happened.
The detailed backend and local-write qualifications appear later.

## Ordinary load while history advances

The server retains three different things for three different purposes.
The private append-only **history** is the authority for accepted decisions and outcomes.
A private **checkpoint** saves derived continuation state so the server can reconstruct it faster.
An authorized **projection** is what a client may see; it can omit private continuation data and apply display and disclosure rules.
Neither a checkpoint nor a client view replaces the history.

For example, suppose a checkpoint covers record 40 and a client wants a complete view through record 70.
The server applies every history record from 41 through 70 to reconstruct continuation, and separately builds the authorized display through 70.
It cannot just label the old checkpoint “70”.
While the client pages through that fixed view, new records can arrive without changing its contents.
Once the view is complete, the client follows changes after record 70.

The fixed snapshot and the “after 70” subscription fit together: paging cannot drift forward and accidentally hide events between the loaded view and the feed.
Only the successful final page establishes that the client has the whole authorized view through 70.
If the server cannot resume the feed from that boundary, it explicitly requires a reload instead of silently skipping changes.
Reading this view does not load an engine, claim the session, or start work.
The cursor, authorization-change, and partial-load rules are collected below.

## Failure: lost notification or owner stream

A tool result commits, but notification is lost.
Authoritative rescan/catch-up finds it; notification failure does not erase the result or justify redispatch.
Owner change ends the old provisional stream, including forwarding streams; the transport connection itself does not migrate between replicas.
Reconnect from the last committed cursor, reload if invalid, and discard/reconcile provisional text.
For a successful [planned handoff](session-commit-history.md#compatible-replica-active-tree-handoff-later-phase), the client handles reconnection and catch-up automatically so the user can remain in the same conversation without manual reload or resume.
That user-visible continuity does not require a gap-free network connection or zero pause.
A transport break alone establishes neither run termination nor durable survival of the last visible tokens.

## Cross-history gap and resource lifetime

Suppose a delegated child finishes work and records it, but the server dies before the parent records the bounded, approved summary that it is allowed to display.
The child's history and the parent's display are different records with different authorization boundaries.
A restart cannot fill the parent's missing display by quietly reading private child snippets.
It must show the gap honestly and recover from evidence through the permitted path.

A missing parent activity summary does not by itself mean that the tool's outcome is unknown.
The server uses the available evidence to establish what happened; it never reruns delegated work merely to fill a display gap.

Registered reconstruction handlers, called **reducers**, apply retained built-in extension facts to restore continuation state.
Missing one that execution depends on is different from missing optional display detail: the former blocks dependent work, while the latter can be shown as unavailable.
Resource lifetime is different again.
An environment reference does not preserve resources: expired winner artifacts remain unavailable while selection history survives, without permission to recreate work.
A retained record that an artifact won a selection is not a promise that the artifact still exists.

## Imported assumptions

The examples assume that accepted private facts and authorized display facts remain distinct, and that replay reconstructs state without reexecuting work.
[History is private and append-only](vocabulary.md); [execution gates](execution-and-recovery.md) wait for storage acceptance rather than trusting a live event.
[Disclosure](cancellation-and-results.md) constrains capture and publication: unreleased output and evidence containing it stay only in process memory.
[Catalog decisions](commands-and-lifecycle.md) are the authority for session creation and deletion; deletion blocks reads even when a listing has not caught up.
[Logical model input](model-continuation.md) is the effective request retained before invocation; together with registered extension facts, it must support reconstruction without reexecuting work.

## Settled rules and why

**Detailed constraints begin here.**
These are the precise ownership, durability, reconstruction, and observation qualifications behind the examples.

### Append acceptance and ownership

The storage adapter accepts immutable transitions, assigns session-wide positions, and enforces expected-head and writer checks.
Distributed appends atomically check the expected head, current writer epoch, and **unexpired root-tree ownership claim** using backend ownership time.
Matching an expired epoch is insufficient.
A root and active delegated descendants share one ownership domain; independent child transfer is outside this design.
Reacquiring a lapsed claim advances the epoch and reconciles the durable head and uncertain transitions.
Fencing rejects stale writes; it does not prove an old process or external call stopped.
Kubernetes leases/watches can be advisory signals, never a second authority.

A single-process local adapter instead declares filesystem-appropriate head checks, serialization, available file-lock protection, and partial-write diagnosis.
It cannot pretend to be a shared multi-replica backend.
Every adapter declares what acceptance survives: Mecatl restart, adapter failure, OS crash, and backend failover are not interchangeable.
A volatile queue or inner savepoint cannot establish outermost acceptance.
Confirmed projections, acknowledgements, and notifications follow that acceptance.
Missing previously acknowledged history is a storage-contract failure: stop writes, loads, and confirmed projections rather than repairing from local memory or reusing a position.

### Checkpoints and authorized display

In the notation below, P is the checkpoint position and C is the selected load position.

A private checkpoint at P accelerates loading at C by replaying every record in (P, C].
It may lag; it never claims C without applying the suffix.
Checkpoint publication cannot create or erase committed changes.
Compaction reduces model context, not retained history; the first cutover trims none.
Summary fields and projection indexes are derived at known positions and cannot attest facts beyond their consumed prefix.

An authorized client projection is not private history or a continuation checkpoint.
The host applies disclosure and registered display rules; clients render safe items rather than interpret private records.
Read-only observation does not claim ownership, load an engine, or start work.
Interactive attachment or an authorized mutation can activate eligible work separately.
Readers do not pin the owning process.

### Pinned pages, authority, and cursors

A complete load uses bounded pages of one immutable authorized view pinned at C.
Page tokens continue that view, not live events; only the final successful page attests completeness through C.
Every later page/subscription/detail read rechecks authority; material authorization changes invalidate the view.
Expired/failed/mismatched pages require discarding the incomplete load and starting again.
After loading, subscribe after C; if changes cannot be resumed, explicitly expire/reload rather than silently skip them.
Invalid, wrong-incarnation, or ahead-of-head cursors are not clamped into apparent continuity.
An incarnation distinguishes one history's lifetime from another; a cursor from the wrong lifetime cannot establish continuity merely because its numeric position looks plausible.
Provisional output has correlation identities, not durable cursors, and must reconcile to committed content.

A checkpoint covers P=40 and a client selects C=70.
The server reconstructs continuation through every suffix record and independently projects authorized pages through 70.
Commits 71 onward can arrive during paging without changing the pinned view.
After the final page, catch-up starts after 70.
No snapshot/subscription race is delegated to the client to solve through an arbitrary event buffer.
Large individual results/media require bounded content reads or explicit size failures, not an oversized page.

### Built-in extension reconstruction and child access

A child commits a settled item, then the process dies before the parent commits its approved bounded projection.
Parent reload shows unresolved/activity-unverified rather than fetching and backfilling private child snippets.
Recovery inspects child evidence and appends a parent gap marker.
A display gap is not an unknown tool outcome: execution certainty follows intent/result evidence.
A known parent result requires exact durable bytes or a versioned deterministic derivation, not merely child-completed status or a preview.
Otherwise settle unknown without rerunning the child.

Registered built-in `Subagent`, `Parallel`, and `Team` reducers reconstruct continuation-critical state; missing/incompatible critical handlers fail closed for dependent work.
Optional display-only detail can be explicitly unavailable while safe core inspection/deletion remains possible.
Parent-owned approved inventories/activity and canonical admitted requests/results reload without optional child reads.
Deeper child access is separately authorized.

### First-cutover boundary

The incompatible first rollout remains teardown/upgrade with separate namespaces: no migration, fallback, dual writes, or mixed-version runtime compatibility.
This is separate from the later compatible-replica handoff described above.

## Open decisions and detail

How should adapters represent history positions, writer claims, and record identities, and diagnose a partially written local record?
How should readers discover that a view or cursor is no longer valid, and what bounds apply to storage and content reads?
How should a client distinguish absent, truncated, and unavailable detail while showing reasoning, notices, delivery, and usage?
How should extension versions and cross-history references remain interpretable after reconstruction or remapping?

Interface details still to specify:

- Open interfaces include positions/cursors/incarnations/record IDs, claim/append/read primitives, local torn-record diagnosis, namespace markers/collision checks, API version negotiation, view invalidation, and bounded storage layout/replay/content/catalog reads.
- Projection work still needs reasoning and notice/delivery representations, inherited/new usage presentation, exact retained-detail field/aggregate budgets, and explicit truncation/absence states.
- Extension registration/compatibility, generic operation/child links, portable dependency-summary versions, and causal-reference scopes/remapping also need interfaces.

Detailed owners: [ownership](session-commit-history.md#ownership-and-writer-fencing), [storage coordination](session-commit-history.md#storage-access-and-commit-coordination), [pinned pages](session-commit-history.md#complete-history-through-a-pinned-position), [reload inventory](session-commit-history.md#what-reload-restores), [delegation budgets/gaps](session-commit-history.md#delegation-detail-budget), and [cutover](session-commit-history.md#breaking-storage-and-protocol-cutover).
Use the [reload matrix](session-commit-history-contracts.md#observable-reload-matrix), [extension evidence inventory](session-commit-history-contracts.md#built-in-extension-reconstruction-boundary), and [acceptance cases](session-commit-history-contracts.md#acceptance-scenarios) for exhaustive review.
