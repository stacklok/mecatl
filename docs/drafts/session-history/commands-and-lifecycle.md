# Commands and lifecycle

**Review question:** How do shared controls, bounded retries, and catalog decisions remain unambiguous?
This draft covers command admission, shared pending input, and session lifecycle.
It introduces no universal jobs service.

## Imported assumptions

[History and gates](execution-and-recovery.md) distinguish durable acceptance of input from its actual submission to a model.
Every attempt is authorized; [an exact approval](authorization-and-hooks.md) resolves its recorded ask but does not bypass the final eligibility checks before execution.
[Cancellation](cancellation-and-results.md) resolves its target in session order; its acknowledgement confirms acceptance/signaling, not terminal settlement.
[Storage and observation](storage-and-observation.md) supply durable decisions and read-only views without loading an engine or starting work.

## Settled rules and why

A session admits at most one active run, regardless of producer.
Competing prompts conflict with the active run; they are not silently queued or converted to steers.
Clients share controls without an exclusive controller, and disconnection does not cancel server-owned work.
Unsent drafts/responses remain local; submitted pending input and exact asks become shared server state.

A **steer slot** holds one source's replaceable pending input: one shared user slot and one per registered extension, with no extension-defined subkeys.
One operation supports set/replace/retract and revision-specific consumption, optionally constrained to an exact run.
Source identity selects the slot, not targeting semantics.
Unconstrained input requests eligible execution, including a new run when idle; constrained input never silently retargets.
Expected revisions prevent stale overwrites and consuming an old revision cannot erase a newer replacement.
All eligible pending slots enter the next safe input boundary together in deterministic order, with committed insertion/consumption before model submission.
Server-stamped provenance does not elevate extension/tool text to system or permission authority.
Extension hints are small pointers to authorized retrieval tools; coalescing them cannot erase committed outcomes.

API mutations default to synchronous committed typed decisions.
An optional retry identity consists of **both** a nonempty opaque token of at most 128 bytes and its original creation timestamp, or neither.
Compare the token exactly; retries preserve identity and the complete method, target, parameters, and content.
Authorize every attempt and reject conflicting reuse.
Without the pair, durable admission still applies, but ambiguous responses require reconciliation, not blind resend.

A compact **receipt** records the original decision/resolved target and necessary fingerprint/result/work references, atomically with the protected decision.
It is not a full response or transcript and cannot retain unreleased bytes or payload-bearing hook evidence.
Each session and each catalog has its own scope shared across clients and mutation types: at most 256 receipts and identities at most one hour old, shortened under pressure.
Evict by creation time, not LRU, and durably advance a nondecreasing cutoff atomically with affected decisions.
Reject every identity at or before it, including ties and never-seen delayed requests.
Validate age and bounded future skew; reconstruct eligible receipts and cutoff before admitting mutations after ownership change.
Clock rollback and retention changes cannot reopen discarded intervals.
Expired identities fail explicitly, not as fresh commands; receipt expiry does not expire work.

A **catalog** owns strict creation, publication, deletion, and fork-admission decisions when destination history is absent or inaccessible.
Each catalog uses exactly one provider; initially there is one catalog per user within a service/storage authority, not federation.
Listing metadata is derived and may lag; deletion gates cannot.
Logical deletion denies reads/activation/writes, prevents ID reuse with minimal tombstones, and does not imply physical purge.
Deleting a root disables delegated children by default, not independent existing forks.

Genuinely asynchronous work exposes common authorized status/wait/subscription through existing run, extension-operation, or catalog-creation lifecycles.
No universal operations collection or mandatory job per mutation is added.
Every Team is session-owned even through direct API entry; originating run/call correlation is optional, not invented to establish ownership.
Cross-run background execution remains later work.

## Ordinary shared-input sequence

A client submits a user-slot revision; another reloads it and replaces it with the expected revision.
A stale edit conflicts.
At the next eligible model boundary, the owner commits all pending source revisions together.
A later replacement stays pending rather than being cleared by consumption of the old revision.
Normal completion promotes accepted unconsumed unconstrained input to one follow-up; exact-run input instead becomes `not delivered: target ended` if its target finished first.
Failure/shutdown/owner loss preserves input for explicit follow-up-or-retract without silently relaxing constraints.
User cancellation cancels undelivered input from all sources instead of promoting it.

## Failure: retry after a lost cancellation response

Session-current cancellation resolves run R and commits its receipt, then the response is lost.
A successor owner reconstructs the scope.
An authorized eligible retry returns the original R decision, even if another run is now current.
The receipt proves acceptance/signaling; observing R separately establishes terminal settlement.
An unprotected or expired request requires reconciliation instead of a replacement token that might cancel newer work.

## Race: receipt pressure and fork publication

At capacity, a new decision retires the oldest creation-time boundary, including all ties.
A never-seen delayed request at that timestamp is rejected; another client's access to an old receipt cannot keep it alive through LRU behavior.
Other sessions/catalogs retain their independent budgets.

For a large fork, admission pins an eligible inactive main-session head prefix and returns a creating destination/work reference.
The copy is server-owned and the source may subsequently run.
Destination deletion fences late publication; source deletion wins against not-yet-finalized forks.
A fork finalized first becomes independent, but never publishes partial history or inherits live execution/authority implicitly.
Full ordering, orphan reconciliation, and inheritance rules remain in the [fork reference](session-commit-history.md#forks-and-retained-history).

## Open decisions and detail

Open interfaces include deterministic slot order/framing, provenance, conflict payloads and hint caps; command fingerprints/comparison, timestamp precision/skew, and typed work observation; catalog identity/membership mapping and atomic/ordered creation/deletion/fork primitives.
Fork eligibility fields, settings options/omitted-grant reporting, orphan fencing/cleanup, and later rewind controls retain their detailed qualifications.
These do not reopen settled retry bounds or targeting semantics.

Detailed owners: [command/receipt protocol](session-commit-history.md#api-command-identity-receipts-and-work-observation), [catalog](session-commit-history.md#session-catalog), [source slots](session-commit-history.md#source-scoped-steers), [shared asks](session-commit-history.md#shared-pending-controls-not-client-drafts), and [durable steer recovery](session-commit-history.md#durable-steer-recovery).
The [command model](session-command-protocol.modelith.md) and [acceptance cases](session-commit-history-contracts.md#acceptance-scenarios) cover cutoff ties, stale arrivals, rollback, target binding, and capacity boundaries.
