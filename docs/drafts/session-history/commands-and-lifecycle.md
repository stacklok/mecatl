# Commands and lifecycle

**Review question:** How do shared controls, bounded retries, and catalog decisions remain unambiguous?
This draft covers command admission, shared pending input, and session lifecycle.
It introduces no universal jobs service.

## Failure: retry after a lost cancellation response

A client asks to cancel the current run, but loses the server's response.
By the time it reconnects, another run may be current.
Blindly sending a new “cancel current” command could stop the wrong work.
The client needs to recover the original decision, not apply the same words to a newer target.

The client can opt into this protection by supplying a retry identity: a token together with its original creation timestamp.
The server retains a small **receipt**, bound to the original command and decision, rather than keeping an entire response forever.
An eligible authorized retry recovers the decision about the original run, not a cancellation of whichever run is current now.
An accepted cancellation means the server accepted and signaled it; the client observes the run separately to learn whether it has ended.
This protection is deliberately bounded in both age and count.
A retry outside those bounds fails explicitly; it does not silently become a fresh command.
Each attempt must still be authorized, even when it only recovers a prior decision.
The exact bounds and retirement rules appear in the detailed constraints below.

## Ordinary shared-input sequence

A user may have two clients open on one conversation, and extensions may also have input to submit.
An unsent draft belongs to its client, but submitted pending input is shared server state.
Other clients can see and edit that pending input without becoming an exclusive controller.
Disconnection does not cancel server-owned work.

For example, a client submits “also check the tests” while the model is busy.
Another client replaces it with “check the focused tests first”.
The replacement must name the revision it saw, so a stale client cannot unknowingly overwrite a newer edit.
Consumption must also name a revision: consuming the old text must not erase a replacement that arrived in the meantime.

A **steer slot** is the server's place for one source's replaceable pending input.
There is one shared user slot and one slot per registered extension.
Source identity answers whose slot it is, not which run it targets.
A submission can optionally require an exact run; otherwise it asks for the next eligible execution, including a new run when idle.
Extension text retains its source and trust level rather than becoming an instruction with system or permission authority.

A client submits a user-slot revision; another reloads it and replaces it with the expected revision.
A stale edit conflicts.
At the next eligible model boundary, the owner commits all pending source revisions together.
A later replacement stays pending rather than being cleared by consumption of the old revision.
Normal completion promotes accepted unconsumed unconstrained input to one follow-up; exact-run input instead becomes `not delivered: target ended` if its target finished first.
Failure/shutdown/owner loss preserves input for explicit follow-up-or-retract without silently relaxing constraints.
User cancellation cancels undelivered input from all sources instead of promoting it.

## Session creation, deletion, and background work

A server needs an authority for deciding whether a session exists even before that session has any history, and for enforcing deletion after its history becomes inaccessible.
That authority is the **catalog**.
It is not just a list of sessions for a UI: the displayed list can lag, but an authoritative deletion decision must already block access.
Logical deletion blocks reads and activation without claiming that all stored bytes have been physically purged.

Delegated children belong to their root session's work, so deleting the root disables them by default.
An already finalized independent fork is a different conversation; deleting its source does not delete that fork.
A Team likewise belongs to a session, including when created directly through an API rather than by a tool call.
An environment reference records which environment was used; it does not extend the lifetime of its resources.
Retained session or Team history is not permission to resurrect expired resources or repeat work.

Most command mutations return a committed typed decision synchronously.
When work really continues asynchronously, a caller observes the existing run, extension-operation, or catalog-creation resource through authorized status, wait, or subscription interfaces.
That does not require a new generic job object for every command.

## Race: receipt pressure and fork publication

For receipt pressure, imagine several clients have submitted commands with the same creation timestamp.
When that oldest time boundary must be retired, all identities at that time retire together, including delayed requests the server has never seen.
Looking up a receipt does not keep it alive indefinitely.
The precise shared budgets and cutoff behavior are below.

A large fork illustrates a different race: copying takes time, so admission and publication are separate decisions.
The server copies the selected source history while the destination is still marked as creating.
If deletion makes that destination invalid before the copy is published, the server must prevent it from becoming available later.
Once finalized, the fork is independent of its source.
This fork behavior is a later capability, not a promise to include it in the first cutover.

## Imported assumptions

The examples distinguish accepting a command, carrying out its work, and displaying its outcome.
[History and gates](execution-and-recovery.md) distinguish durable acceptance of input from its actual submission to a model.
Every attempt is authorized; [an exact approval](authorization-and-hooks.md) resolves its recorded ask but does not bypass the final eligibility checks before execution.
[Cancellation](cancellation-and-results.md) resolves its target in session order; its acknowledgement confirms acceptance/signaling, not terminal settlement.
[Storage and observation](storage-and-observation.md) supply durable decisions and read-only views without loading an engine or starting work.

## Settled rules and why

**Detailed constraints begin here.**
The following inventories retain the exact targeting, retry-retention, and lifecycle qualifications behind the examples.

### Shared runs and source slots

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

### Command identity and bounded receipts

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

At capacity, a new decision retires the oldest creation-time boundary, including all ties.
A never-seen delayed request at that timestamp is rejected; another client's access to an old receipt cannot keep it alive through LRU behavior.
Other sessions/catalogs retain their independent budgets.

Session-current cancellation resolves run R and commits its receipt, then the response is lost.
A successor owner reconstructs the scope.
An authorized eligible retry returns the original R decision, even if another run is now current.
The receipt proves acceptance/signaling; observing R separately establishes terminal settlement.
An unprotected or expired request requires reconciliation instead of a replacement token that might cancel newer work.

### Catalog authority and existing work lifecycles

A **catalog** owns strict creation, publication, deletion, and fork-admission decisions when destination history is absent or inaccessible.
Each catalog uses exactly one provider; initially there is one catalog per user within a service/storage authority, not federation.
Listing metadata is derived and may lag; deletion gates cannot.
Logical deletion denies reads/activation/writes, prevents ID reuse with minimal tombstones, and does not imply physical purge.
Deleting a root disables delegated children by default, not independent existing forks.

Genuinely asynchronous work exposes common authorized status/wait/subscription through existing run, extension-operation, or catalog-creation lifecycles.
No universal operations collection or mandatory job per mutation is added.
Every Team is session-owned even through direct API entry; originating run/tool-call correlation is optional, not invented to establish ownership.
Cross-run background execution remains later work.

For a large fork, admission pins an eligible inactive main-session head prefix and returns a creating destination/work reference.
The copy is server-owned and the source may subsequently run.
Destination deletion fences late publication; source deletion wins against not-yet-finalized forks.
A fork finalized first becomes independent, but never publishes partial history or inherits live execution/authority implicitly.
Full ordering, orphan reconciliation, and inheritance rules remain in the [fork reference](session-commit-history.md#forks-and-retained-history).

## Open decisions and detail

In what order and format should the server combine pending input from different sources, and how should it explain revision conflicts to clients?
How should a client compare commands for retry, represent timestamps, and observe typed background work?
How should catalogs map users and membership to creation/deletion authority?
For later forks, what makes a source eligible, which settings and grants carry over, and how is an abandoned copy fenced and cleaned up?
What controls should later rewind support expose?

Interface details still to specify:

- Open interfaces include deterministic slot order/framing, provenance, conflict payloads and hint caps; command fingerprints/comparison, timestamp precision/skew, and typed work observation; catalog identity/membership mapping and atomic/ordered creation/deletion/fork primitives.
- Fork eligibility fields, settings options/omitted-grant reporting, orphan fencing/cleanup, and later rewind controls retain their detailed qualifications.
- These do not reopen settled retry bounds or targeting semantics.

Detailed owners: [command/receipt protocol](session-commit-history.md#api-command-identity-receipts-and-work-observation), [catalog](session-commit-history.md#session-catalog), [source slots](session-commit-history.md#source-scoped-steers), [shared asks](session-commit-history.md#shared-pending-controls-not-client-drafts), and [durable steer recovery](session-commit-history.md#durable-steer-recovery).
The [command model](session-command-protocol.modelith.md) and [acceptance cases](session-commit-history-contracts.md#acceptance-scenarios) cover cutoff ties, stale arrivals, rollback, target binding, and capacity boundaries.
