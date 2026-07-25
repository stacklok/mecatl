# ADR 0074 — Many concurrent agent loops per server; the scheduler is single-leader for the tick, pooled (then sharded) for the drive

- Status: Accepted
- Date: 2026-07-24
- Scope: the cloud-native scaling model — how many agent loops (sessions) one
  server process hosts, how that composes with multi-replica deployment, and
  the shape of the scheduler's leader election under it
- Supersedes: none (records a stance previously held only as folklore)
- Superseded by: none

## Context

The cloud-native arc ([ADR 0027](./0027-cloud-native.md), [ADR 0048](./0048-mecak8s.md))
established the *exclusion* primitives — a per-session `SessionLease`
(single-writer per session across replicas) and a single global
`__scheduler__` leader lease (one tick loop per cluster) — but never stated the
*deployment-density* stance: **how many concurrent agent loops may one server
process host?** ADR 0048's "storage-free agent, scale by replicas" implies
horizontal scaling, and ADR 0027 speaks only to single-writer-per-session, so
the operator's actual position — *one server hosts MANY concurrent sessions,
and we scale both vertically (sessions per replica) and horizontally
(replicas)* — was unrecorded. That gap mattered the moment scheduling entered
the picture: the answer to "how does the scheduler's leader election work when
one server runs many loops and there are many such servers" depends on it.

The code already supports the stance. The `Service` is multi-session *by
construction*: the run-entry mutex is keyed per session
(`internal/adapter/server/service.go` `runEntryMu`), the live-run registry
(`runs`), the bounded per-session engine cache (`sessionEngines`, capped by
`MaxSessionEngines`), and the per-session lease + renewer map (`heldLeases`)
are all keyed on the session id. Nothing is one-loop-per-process. What was
missing was the *recorded decision* and the scheduler shape that follows from
it. The forces:

1. **Vertical + horizontal scaling compose.** Per-session leases are the
   exclusion primitive; they scale to any session count per replica AND any
   replica count, because each lease is independent. One process holding many
   sessions is the same problem as many processes each holding some sessions —
   the lease doesn't care which process the owner string maps to.

2. **The scheduler tick is cheap; the fire DRIVE is expensive.** A single
   global leader is correct *for the tick*: polling `Due` and `Claim`-ing is
   O(due schedules) and `Claim` is the at-most-once correctness fence (the
   leader is hygiene, not correctness — ADR 0027 List-1 row 31). The coupling
   that bites at scale is that the leader also *drives every fire's agent loop
   in-process* — as fire throughput grows, the leader replica becomes the
   hotspot while also serving its share of interactive sessions.

3. **Per-session scheduling does not fit the fire model.** A scheduled fire
   mints a FRESH `sched--` session per fire (ADR 0059 decision #7) — there is
   no persistent session to hang a per-session schedule-drive lease on. So
   "each session's schedules are driven by that session's lease-holder" is not
   a coherent model here.

4. **Split-brain is the real risk of any lease.** Every reference
   implementation (Chubby sequencers, etcd create-revisions, Redlock tokens)
   treats the fencing token as *consumed at write time*, non-optional. mecatl's
   `Lease.Token` is plumbed but not yet consulted (CAS-Save deferred in ADR
   0027) — today the grant itself is the only enforcement, so a replica with a
   stalled renewer can still write after losing the lease.

## Decision

1. **One server process hosts MANY concurrent agent loops (sessions).** This is
   the operator's recorded stance. Scale is two-dimensional: vertical (sessions
   per replica) and horizontal (replicas). The per-session `SessionLease` is
   the single-writer exclusion primitive across BOTH dimensions — it is
   owner-string-agnostic (one process owning many sessions is identical to many
   processes each owning some). No new seam is needed for multi-loop-per-server;
   the Service is already built for it.

2. **The scheduler stays a SINGLE GLOBAL LEADER for the tick loop.** The tick
   (poll `Due` + `Claim`) is cheap and `Claim` is the correctness fence; one
   leader per cluster over the well-known `__scheduler__` lease is the right
   shape regardless of session density. The standby-leadership-loop (a
   non-leader serves RPCs and retries, promoting on leader lapse; a definitive
   loss demotes to standby — the ADR 0073 follow-up) is the availability model.

3. **The fire DRIVE is decoupled from the tick, then sharded only if needed.**
   Step 1 (the already-documented Phase-2 decoupling): the tick loop Claims and
   hands the drive to a bounded background pool, removing the "a long fire
   delays all schedules" latency coupling (ADR 0059 consequences). Step 2
   (deferred, Rule of Three): if fire throughput then demands it, shard the
   DRIVE across replicas with per-schedule leases (`__schedule__/<name>` on the
   SAME `port.SessionLease` port — zero new seams, conformance-validated). The
   tick stays single-leader in both steps. Per-session scheduling is REJECTED
   (decision-context #3).

4. **The fencing token WILL be consumed at write time (CAS-Save).** The
   `Lease.Token` advances on every takeover; the `SessionStore.Save` path (and
   the schedule-store write path) will reject a write from a holder presenting
   a stale token. This is the split-brain guard every reference implementation
   treats as load-bearing, and it is the prerequisite for calling the lease
   correctness-grade rather than exclusion-grade. It is recorded here as an
   accepted, not-yet-implemented decision so it is a *decision*, not drift.

5. **Jittered campaign backoff + a readable leader record.** Standby re-acquire
   uses jittered backoff (done). A future `port.SessionLease.Lookup(ctx, id)`
   (read the current election record) lets `ErrNotLeader` name the leader for
   client redirect (deferred — the port stays minimal until a consumer needs it).

## Consequences

- **Easier:** the multi-loop-per-server stance is now explicit, so per-replica
  session density is a first-class tuning knob (`MaxSessionEngines`) rather than
  an accident; the scheduler's scaling path (decouple drive → shard drive) is a
  recorded sequence, not an open question re-litigated per phase.
- **Harder / committed to:** CAS-Save token consumption is now owed work with an
  ADR behind it; until it lands, the leases are exclusion-grade (they prevent
  *concurrent* entry) but not yet split-brain-proof against a stalled-renewer
  writer. Per-schedule sharding adds N lease objects + N renewers per cluster if
  adopted — an etcd-style amortized keepalive (one renew stream, many keys,
  behind the same port) is the recorded mitigation note, not code.
- **The single-leader tick means a leader failover pauses schedule-firing for
  up to the lease TTL** (a standby promotes only after the dead leader's lease
  lapses). Accepted: the leader is hygiene, `Claim` fences correctness, and a
  missed slot self-heals via the misfire policy on the next tick.

## See also

- [ADR 0027 — Cloud-native arc](./0027-cloud-native.md) — the ports +
  lease/scheduler inventory (List 1 rows 30–32); Phase 4 (session lease),
  Phase 5 (scheduled tasks).
- [ADR 0048 — mecak8s](./0048-mecak8s.md) — the storage-free k8s-native agent
  (Redis store + k8s lease + drain gate).
- [ADR 0059 — Scheduled tasks](./0059-scheduled-tasks.md) — the registry, tick
  loop, claim-before-fire, and the fire-drive coupling (consequences).
- [ADR 0073 — Schedule tool](./0073-schedule-tool.md) — the model-facing tool +
  on-by-default scheduler this stance governs.
- [`docs/architecture.md`](../architecture.md) — the living reference (the
  many-loops stance + scheduler shape land in the scheduled-tasks section).
