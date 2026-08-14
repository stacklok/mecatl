# ADR 0109 — Evidence-backed reflection and durable staged learning

- Status: Accepted
- Date: 2026-08-14
- Scope: reflection, proposal lifecycle, scheduling, server review, and memory promotion
- Supersedes: none
- Superseded by: none

## Context

Evidence reflection produces bounded candidate facts and procedures, but directly writing those
model-derived values would couple extraction to authority and make retries unsafe. A process can crash
between changing memory and recording the proposal result. Concurrent explicit, consolidation, and
promotion writers can also observe the same old value and silently overwrite one another if absence is
represented by the lifecycle API's empty expected version, because that existing API deliberately means
“unconditional write.” Procedures need a separate design and are not safe to turn into skills in this
slice.

The engine must remain importable. Durable host filesystem policy stays in composition. Standard
composition needs a bounded, Build-owned coordinator rather than a goroutine per completion, and review
surfaces must preserve caller ownership and compare-and-swap authority.

## Decision

Add closed candidate, outcome, signal, evidence, input, and reflector values to `engine/learning`.
Every bounded candidate cites content-addressed message or event evidence resolved against the exact
input. Canonical projection omits binary data, reasoning, actor identity, raw tool/permission arguments,
and credentials. The pure signal detector admits only defensible single-trajectory signals; cross-session
contradiction/repetition is caller-supplied. `agent.EvidenceReflector` makes at most one provider-neutral,
tool-less, timeout-bounded call on the session-selected provider/model (with the reflection model slot),
strictly parses one complete JSON result, and treats abstention as success.

Add a narrow `learning.ProposalRepository` lifecycle. A bounded record contains a canonical candidate,
content-addressed evidence references and signals, bounded decision history, and an optional promotion
receipt; it never stores transcript excerpts. Deterministic proposal IDs hash the principal/project
partition, input digest, candidate, and evidence digests. Stage is batch-idempotent, while decision,
promotion claim, finalization, reconciliation, and undo use opaque compare-and-swap versions. Status is
closed over staged, promoting, promoted, rejected, deferred-unsupported, conflicted, and undone.

Ship `engine/adapter/memproposal` with shared conformance and keep durable host persistence in
`internal/adapter/reflectionstore`. The durable adapter hashes principal/project values into partition
bucket keys inside one bounded JSON document and uses a stable flock plus temp-file fsync, rename, and
directory fsync for every load-mutate-save CAS. Limits cover proposal count, all candidate/evidence
fields, page size, decision count, and encoded document size.

Add the optional `tool.MemoryConvergenceStore` capability beside the compatible lifecycle interface.
Its `MemoryCurrent{Exists, Version}` distinguishes absence CAS from the old empty-version unconditional
operation. Local, in-memory, and negotiated remote stores implement it. `MemorySource.ProposalID` and
the additive driver field link a resulting memory revision to its proposal; missing values remain the
normal old-file/old-driver case.

The standard importable promotion adapter evaluates and mutates one candidate at a time. It rejects
non-canonical, secret, directive, transient, or malformed facts; defers procedures; recognizes exact
current duplicates; stages ambiguous duplicates for review; and never overwrites a conflicting,
newer, or user-explicit fact. The standard promotion policy requires provenance, not merely a model-selected handle. Operator facts auto-promote only when their evidence includes the same genuine principal-authored message that triggered an explicit remember signal. Tool, WebFetch/WebSearch/MCP, repository, event-only, and assistant-only evidence remains staged. Trusted project facts use a separate narrow rule: principal-authored evidence, the root-aware trust decision, and an exact match between the session workspace and configured project-memory root. Project candidates from an admitted non-launch root remain staged but cannot auto-promote, approve, or undo until a safe exact-root lifecycle store exists, and reflection does not read launch-root project memory for those inputs; untrusted project material is not ingested. An already-existing project partition remains listable/rejectable. A successful claim writes with presence-and-version
CAS and then finalizes a receipt. A promoting record whose current memory revision has the same proposal
id is finalized during reconciliation without another write.

Undo is compensating, not destructive: it succeeds only while the receipt's linked resulting memory
version remains current, appends a versioned undo, and marks the proposal undone afterward. A newer
revision makes the proposal conflicted and remains untouched. Each candidate is an independent atomic
unit, so a heterogeneous batch may partially promote and reports that state honestly. Lifecycle-aware
dream consolidation uses the same CAS discipline; genuinely base-only stores retain their compatible
legacy path.

Standard composition owns one coordinator in every mode with conservative configurable bounds: a global queue,
per-principal FIFO queues serviced fairly across principals, one worker by default, per-job timeout,
principal+session+trajectory-digest singleflight, bounded per-job and aggregate queued bytes, and bounded receipts. Construction is dormant: workers start only after the first admitted job. The effective receipt bound is at least queue capacity plus workers, and a live receipt is reserved before admission, so no accepted job can lose completion. `Built.Close` stops admission, publishes closed receipts for queued synchronous waiters, cancels active work, clears pending state, and joins any started workers.
`off` installs no automatic observer, starts no worker, and opens no proposal repository/flock; explicit reflection lazily opens the Build-owned repository, admits through the same bounded coordinator, and waits synchronously without a public async option.
`review` stages valid candidates without memory writes. `auto` stages first and promotes only eligible,
non-conflicting facts under current trust/policy. Procedures always become `deferred_unsupported` with a
visible reason; skill evaluation/promotion remains #510.

The synchronous engine observer only detects signals and enqueues. Explicit reflection reloads the completed session's persisted provider/model, keeps any reflection-slot model on that same provider, and enriches input from a bounded read of the caller-owned session's event log before calling the reflector; the reflector never imports `EventLog`. Evidence availability re-reads the same owned session/message or event-log record and verifies its canonical digest and event sequence. Missing, compacted, deleted, or mismatched material reports unavailable rather than substituting content. Automatic
trajectory-only reflection may cite message evidence. No hidden cross-session index or retrospective
restart sweep exists.

The additive gRPC/HTTP capability exposes explicit session reflection, bounded proposal pagination,
get, approve/reject with expected version, and promotion undo with expected version. Approval re-verifies every message/event evidence digest, event sequence, and tool-call binding against the caller-owned source; unavailable or changed evidence fails precondition before promotion. Missing dependencies
return Unimplemented. Session/evidence ownership is checked at the Service boundary, proposal partitions
use the current principal, project promotion requires project admission, and every producer string is
UTF-8 repaired on projection. The existing legacy review flag aliases `auto`; dream consolidation remains
a separate CAS writer.

## Consequences

Retries and concurrent workers converge without duplicate writes or silent overwrites. A crash cannot
make a committed promoted value indistinguishable from an unrelated value. Exact duplicates avoid a
new revision, and operator-authored facts remain dominant. Cross-candidate all-or-nothing behavior is
not promised, avoiding a distributed transaction between proposal and memory stores.

The standard path now stages eligible completed trajectories automatically and exposes transport review.
Transient queue/receipt state resets on restart while durable proposals converge idempotently. Explicit
reflection remains available when automatic observation is off. Skill evaluation and promotion are not
implemented.

## See also

- [Optional learning seam](0106-optional-learning-seam.md)
- [Memory lifecycle](0107-operator-profile-memory-lifecycle.md)
- [Memory architecture](../architecture/memory.md)
