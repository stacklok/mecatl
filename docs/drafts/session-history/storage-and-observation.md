# Storage and observation

**Review question:** What does durable acceptance mean, and how does a client load a complete view and follow it?
This draft covers storage authority, ownership fencing, and authorized reads, not physical schemas or transparent stream migration.

## Imported assumptions

[History is private and append-only](vocabulary.md); [execution gates](execution-and-recovery.md) wait for storage acceptance rather than trusting a live event.
[Disclosure](cancellation-and-results.md) constrains capture and publication: unreleased output and evidence containing it stay only in process memory.
[Catalog decisions](commands-and-lifecycle.md) are the authority for session creation and deletion; deletion blocks reads even when a listing has not caught up.
[Logical model input](model-continuation.md) is the effective request retained before invocation; together with registered extension facts, it must support reconstruction without reexecuting work.

## Settled rules and why

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

A complete load uses bounded pages of one immutable authorized view pinned at C.
Page tokens continue that view, not live events; only the final successful page attests completeness through C.
Every later page/subscription/detail read rechecks authority; material authorization changes invalidate the view.
Expired/failed/mismatched pages require discarding the incomplete load and starting again.
After loading, subscribe after C; if changes cannot be resumed, explicitly expire/reload rather than silently skip them.
Invalid, wrong-incarnation, or ahead-of-head cursors are not clamped into apparent continuity.
Provisional output has correlation identities, not durable cursors, and must reconcile to committed content.

## Ordinary load while history advances

A checkpoint covers P=40 and a client selects C=70.
The server reconstructs continuation through every suffix record and independently projects authorized pages through 70.
Commits 71 onward can arrive during paging without changing the pinned view.
After the final page, catch-up starts after 70.
No snapshot/subscription race is delegated to the client to solve through an arbitrary event buffer.
Large individual results/media require bounded content reads or explicit size failures, not an oversized page.

## Failure: lost notification or owner stream

A result commits, but notification is lost.
Authoritative rescan/catch-up finds it; notification failure does not erase the result or justify redispatch.
Owner change ends the old provisional stream, including forwarding streams; the transport connection itself does not migrate between replicas.
Reconnect from the last committed cursor, reload if invalid, and discard/reconcile provisional text.
For a successful [planned handoff](session-commit-history.md#compatible-replica-active-tree-handoff-later-phase), the client handles reconnection and catch-up automatically so the user can remain in the same conversation without manual reload or resume.
That user-visible continuity does not require a gap-free network connection or zero pause.
A transport break alone establishes neither run termination nor durable survival of the last visible tokens.

## Cross-history gap and resource lifetime

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
An environment reference does not preserve resources: expired winner artifacts remain unavailable while selection history survives, without permission to recreate work.

## Open decisions and detail

Open interfaces include positions/cursors/incarnations/record IDs, claim/append/read primitives, local torn-record diagnosis, namespace markers/collision checks, API version negotiation, view invalidation, and bounded storage layout/replay/content/catalog reads.
Projection work still needs reasoning and notice/delivery representations, inherited/new usage presentation, exact retained-detail field/aggregate budgets, and explicit truncation/absence states.
Extension registration/compatibility, generic operation/child links, portable dependency-summary versions, and causal-reference scopes/remapping also need interfaces.
The incompatible first rollout remains teardown/upgrade with separate namespaces: no migration, fallback, dual writes, or mixed-version runtime compatibility.

Detailed owners: [ownership](session-commit-history.md#ownership-and-writer-fencing), [storage coordination](session-commit-history.md#storage-access-and-commit-coordination), [pinned pages](session-commit-history.md#complete-history-through-a-pinned-position), [reload inventory](session-commit-history.md#what-reload-restores), [delegation budgets/gaps](session-commit-history.md#delegation-detail-budget), and [cutover](session-commit-history.md#breaking-storage-and-protocol-cutover).
Use the [reload matrix](session-commit-history-contracts.md#observable-reload-matrix), [extension evidence inventory](session-commit-history-contracts.md#built-in-extension-reconstruction-boundary), and [acceptance cases](session-commit-history-contracts.md#acceptance-scenarios) for exhaustive review.
