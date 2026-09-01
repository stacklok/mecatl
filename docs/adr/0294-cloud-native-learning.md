# ADR 0294 — Cloud-native learning uses durable, authoritative attempts

- Status: Proposed
- Date: 2026-08-31
- Scope: learning admission, durable attempt orchestration, source evidence, distributed proposal/skill authority, and attempt APIs
- Supersedes: ADR 0109's reset-by-design reflection coordinator/receipt authority decision; ADR 0114's process-local automatic admission accounting decision, where cloud-native learning v2 is wired
- Superseded by: none

## Context

The existing reflection path has durable proposals and skills but treats queued/running work,
completion receipts, deduplication, cooldowns, and automatic budgets as process-local coordinator
state. A restart cancels work without catch-up; multiple replicas multiply automatic capacity. That
was an explicit, acceptable staging decision in [ADR 0109](./0109-staged-learning-proposals.md)
and [ADR 0114](./0114-configurable-learning-trigger-policy.md), not a distributed learning
contract.

It also has a narrower correctness defect: the hard-intent detector must reliably recognize a
principal's genuine current imperative request to create, make, build, or turn a procedure into a
skill, alongside established learn/save forms. It must never let negation, a capability question,
or assistant/tool/web/repository/historical/synthetic content manufacture this authority.

A status endpoint or a durable `EventLog` record does not solve either problem. `EventLog` is
append-only relay persistence and may have an honestly documented append gap; it is not a
compare-and-swap workflow store. Conversely, persisting transcript/tool content in a job record
would duplicate sensitive content, evade the existing compaction/archive and caller-ownership
controls, and create a second retention system. Durable run identity is the exact missing handle
for binding an attempt to one source execution, while cursor/watch is a separate in-progress
server contract ([ADR 0249](./0249-durable-run-identity.md),
[ADR 0250](./0250-durable-cursors-and-watch.md)).

Finally, a durable attempt alone is insufficient: a remote worker must be able to use distributed
proposal and skill repositories without weakening their existing CAS, provenance, evaluation,
activation, partition, and catalog-publication rules. A skill recorded Active in one store but not
hydrated by another replica is not a cloud-native learned skill.

## Decision

### 1. Fix explicit procedure intent before durable orchestration

Make deterministic hard admission recognize only a verified, current, genuine
principal-authored main-session prompt that contains an affirmative imperative to create/make/build
a skill, turn a workflow/procedure into a skill, or an established learn/save request. It remains
independent of weighted scoring and accepts only ADR-0114's exact hard-stop set: `end_turn`,
max-turn, max-tool-call, and run-budget.

Failed, cancelled, awaiting, no-progress, timeout, and structured-output terminals are not hard
admission stops. Negations, questions about capability or policy, assistant text, tool results, fetched/web/MCP
content, repository material, historical text, synthetic harness text, delegated sessions, and
compacted or otherwise unverifiable spans fail closed. This changes admission only; direct
`SkillDraft` remains an inactive direct draft and never becomes active because of detected intent.

### 2. Make an admitted attempt the authoritative durable workflow

Add an `engine/learning` `AttemptRepository` port and closed learning-domain values. Landed
ADR-0249 durable RunID is a hard prerequisite: absent, zero, or non-durable RunID refuses
admission before `queued` is reported and creates no attempt. An attempt is created durably before
the service says `queued`; that promise therefore survives response loss, process death, and
replica change. The repository supplies deterministic attempt IDs partitioned by caller, source
session, durable run ID, and canonical input digest; opaque CAS versions; closed `queued`,
`running`, and terminal states; expiring, fenced worker claims; bounded retry and reconciliation;
retention and deletion.

At admission, the repository also stores immutable, content-free provenance: an admission class and
the exact current-principal-prompt, RunID, and canonical-digest binding. This binding, not
replayed user-role text, is the only evidence of explicit authority available to a worker.

Attempt records and projections retain only bounded safe metadata: opaque identifiers/digests,
state, timestamps, attempt/claim generations, closed safe codes, and authorized proposal/skill
links. They never retain raw prompts, conversation/tool/event/archive content, filesystem paths,
principal values, credentials, tokens, headers, secret-shaped values, driver-error text,
diagnostics, metrics, watch envelopes, or optional EventLog projections. Non-admitted and skipped completions remain immediate
status plus content-free metrics and create no durable learning history.

The attempt repository, not `EventLog`, a process-local queue, or a receipt cache, is the source of
truth for lifecycle state. `EventLog` may later carry content-free activity/notification projections
from a committed attempt transition, but cannot reconstruct, authorize, or transition an attempt.
The engine remains storage-agnostic: composition/service workers own repository access and worker
lifetime.

### 3. Reference source evidence; do not copy it

An admitted attempt carries a private immutable owner binding plus the exact caller-authorized
session ID, durable `RunID`, and canonical digest. A worker gets only exact-source delegated access;
it cannot accept a caller-supplied principal or system-principal bypass, and foreign/missing source
evidence has the same absence-style failure. It reconstructs required reflection/materialization
input from the persisted session and durable event-log material, including compaction archives where
necessary, only through the existing bounded, secret-safe canonical learning projection. At every
remote or restarted model boundary it wraps that projection with the governance untrusted fence;
raw transcript, archive, tool, or event text never crosses that boundary. It then verifies ownership,
run identity, ordering, and digest before any downstream write.

Missing, gap-marked, compacted-without-archive, deleted, unauthorized, or mismatched evidence is a
safe terminal `evidence_unavailable`-class result. `abstained`/`no_candidate` is a separate valid
terminal, distinct from evidence failure, evaluation rejection, and publication failure. Neither is
replaced by best-effort current history or a copied raw transcript. Reconciliation may retry an incomplete durable boundary, but
must converge through deterministic IDs and opaque CAS rather than report an unobserved result as
success.

### 4. Keep attempt authority separate from downstream convergence

Provide distributed implementations/drivers for `ProposalRepository`, `SkillRepository`, and the
new `AttemptRepository`. Each preserves its current conformance contracts: opaque CAS,
provenance/evaluation/activation guards, caller/project partitioning, deterministic identifiers,
and idempotent crash reconciliation. `AttemptRepository` alone fences the attempt-owned
`Renew`, `Checkpoint`, `Release`, and `Finalize` transitions. An expired, released, superseded,
retried, or abandoned claim cannot revive, checkpoint, release, or finalize that attempt, cannot
finalize a successor, and cannot rewrite an abandoned attempt to success.

Proposal and skill commits remain independent deterministic, CAS-protected durable boundaries;
there is no cross-store transaction and no claim-generation guard on them. A stale worker can win
an independently valid downstream commit race after losing its claim, but that effect never proves
attempt success and cannot overwrite the attempt. A fresh claimant re-reads the authoritative
repositories and may adopt only a compatible deterministic artifact; otherwise it leaves
inactive/unlinked residue or reaches a safe non-success outcome. Manual abandon is
non-compensating: it changes only the attempt and promises no downstream rollback.

Catalogs are derived per-partition monotonic-generation caches. Publish, hydrate, and invalidate
compare authoritative partition generations: a delayed old publish or invalidate cannot replace or
revoke a newer generation. This guarantees convergence rather than instant claim-driven
invalidation. A stale or uncertain partition fails closed without clearing or exposing another
partition, while external-skill precedence and path-free bundles remain intact. Strict universal
prevention of late downstream writes would require a future unified linearizable learning authority
and is deliberately out of scope.

### 5. Stage delivery: explicit first, automatic distributed controls last

The first end-to-end vertical slice is an explicit user-directed procedure request through durable
attempt, recovery, and downstream proposal/skill linkage. Weighted automatic work later enters the
same attempt lifecycle; it does not get a second queue.

Until a durable distributed reservation/deduplication ledger is implemented, automatic admission
retains ADR 0114's explicit per-process limitation and documentation must not claim global
count/token budgets, cooldowns, or duplicate suppression. The final automatic wave makes these
controls process-independent and atomically fenced across replicas before making that claim. Its
reservation identity is the deterministic attempt ID: reserve/create linkage and every crash boundary
are reconciled explicitly. Charges are retained or reclaimed deterministically on crashes before and
after reservation/create, timeout, expiry, reassignment, and abandonment; replacements never exceed
the configured global maximum.

### 6. Expose bounded, authorized attempt state; defer attempt watch

Expose caller-authorized get, list, and defined retry/abandon operations over bounded projections,
opaque versions, and bounded pagination/cursors. Apply the ADR-0212/0213 ownership discipline:
private immutable owner binding at creation, exact-source worker access, no caller-supplied principal
or system-principal bypass, and absence-style failure for foreign or missing data. Perform that
non-disclosure check before locks, signals, diagnostics, and pagination/count/cursor construction,
so the two cases have no testable timing or existence oracle. Responses use closed safe failure codes
and only link proposal/skill IDs a caller may read.

Driver-backed learning ultimately requires workload-authenticated claims, separate caller and infrastructure
RPC surfaces, opaque workload-asserted project namespaces rather than raw workspace paths, and
fail-closed startup when configured learning drivers cannot enforce ownership. In the current
implementation stage, the raw repository RPCs have none of ADR-0213's authentication middleware,
private durable owner registry, or separately authenticated maintenance surface. They are therefore
available only as explicitly trusted single-tenant infrastructure; application ownership enforcement
always rejects `--learning-store-url`, even when a driver self-advertises `enforced`. That advertised
value is not proof until the cryptographic boundary lands.

Attempt watch is deferred to a separately specified follow-up. ADR-0250's session `EventLog` watch
is not an attempt watch feed. That follow-up must define a durable attempt-change feed with cursors
bound to caller, query/attempt scope, and generation; tampered, expired, and foreign cursors must
fail as absence without leakage. Notifications are advisory only: clients re-read AttemptRepository
under authority rather than treating a feed as workflow state.

## Consequences

**Benefits.** A `queued` response now has durable meaning. Workers can fail, move, expire, and
reconcile without losing the fact that learning was admitted. Exact source evidence remains under
existing session/log ownership and compaction rules instead of becoming a second transcript store.
Replica-safe downstream repositories and hydration make a committed Active skill usable outside the
worker's process. The explicit vertical slice delivered first; the as-built final wave now also
selects a durable automatic ledger before advertising global automatic controls.

**Costs and limits.** This adds an exported engine port, domain values, adapters/drivers,
conformance suites, API surface, repository migrations, attempt claim fencing, durable retention
policy, and ADR 0027 resource/fidelity inventory rows. The as-built raw learning driver is restricted
to explicitly trusted single-tenant infrastructure and fails closed whenever application ownership
enforcement is active. Multi-tenant driver-backed learning remains blocked on ADR-0213 workload
authentication, its private owner registry, and separated maintenance surface; a driver's
self-advertised `enforced` value cannot waive that boundary. It requires `task api:update` and
a compatibility changelog entry when the port lands. It also introduces operational work: claim
expiry, abandoned-attempt reconciliation, evidence retention limits, and replica-safe generation
convergence must be observable through safe metadata without leaking model content.

There is deliberately no distributed transaction across attempt, proposal, skill, session, and
event stores. A downstream effect never proves attempt success. Every boundary must be
idempotent and reconcilable: a fresh claimant may adopt only a compatible deterministic artifact,
otherwise it leaves inactive/unlinked residue or records safe non-success. Manual abandon is
non-compensating and does not roll back downstream state. Catalogs converge by authoritative
per-partition monotonic generation; delayed old publication or invalidation cannot replace or
revoke a newer generation, but instant claim-driven invalidation is not promised. Strict universal
prevention of late downstream writes is a deferred design requiring a separate unified
linearizable learning authority. Unavailable evidence fails honestly. EventLog's documented gap
behavior remains a reason to fail evidence verification, never a reason to infer completion. The
as-built final automatic-controls wave uses the durable ledger for process-independent accounting;
an embedding that does not wire it retains ADR-0114's process-local limitation and must not claim
global bounds.

This ADR supersedes only the identified reset-by-design coordinator/receipt authority in ADR 0109
and process-local automatic admission accounting in ADR 0114 when v2 is wired. Their durable
proposal/skill policy, admission safety, and existing behavior outside this capability remain
unchanged; the frozen ADRs are not edited.

## See also

- [Cloud-native learning acceptance plan](../acceptance/cloud-native-learning.md)
- [ADR 0027 — Cloud-native arc](./0027-cloud-native.md)
- [ADR 0109 — Evidence-backed reflection and durable staged learning](./0109-staged-learning-proposals.md)
- [ADR 0111 — Hardened publication and recovery for agent-owned skills](./0111-hardened-agent-owned-skill-publication.md)
- [ADR 0114 — Configurable learning-trigger policy](./0114-configurable-learning-trigger-policy.md)
- [ADR 0212 — Enforce caller ownership at every application access path](./0212-caller-ownership-enforcement.md)
- [ADR 0213 — Enforce caller ownership at remote driver boundaries](./0213-driver-caller-ownership.md)
- [ADR 0249 — Durable run identity](./0249-durable-run-identity.md)
- [ADR 0250 — Durable cursors and the session watch transport](./0250-durable-cursors-and-watch.md)
- [Architecture: evidence-backed reflection](../architecture.md#evidence-backed-reflection)
