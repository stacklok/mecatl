# ADR 0059 — Scheduled tasks: durable registry, tick loop, leader-lease, claim-before-fire at-most-once

- Status: Accepted
- Date: 2026-06-29
- Scope: the scheduled-tasks feature (issue #189) — the `port.ScheduleStore` registry (Phase 1a), the `cronparse` parser-only helper (Phase 1c), the in-process `internal/adapter/scheduler` tick loop (Phase 1e), the fresh-session-per-fire execution model, and the composition wiring (Phase 1f, deferred to that phase)
- Supersedes: none
- Superseded by: none

## Context

The harness had no notion of time-driven execution: every run was launched by a
human (or a client) sending a prompt. The recurring ask — from operators running
`mecated` unattended, and from the cloud-native posture ([ADR 0027](./0027-cloud-native.md),
[ADR 0048](./0048-mecak8s.md)) — was a way to fire a prompt on a schedule: a
nightly digest, an hourly heartbeat, a one-shot reminder. The forces at play:

1. **No human at fire time.** A scheduled fire runs without a human to approve
   a tool, re-issue a prompt, or recover a stalled turn. So a fire must be
   bounded (subagent-grade limits), posture-pinned (an explicit mutating opt-in
   vs the read-leaning default), and recoverable (the same run-entry
   recover/reopen/awaiting seams a human-driven run uses).

2. **Multi-replica.** A cloud-native deployment runs ≥2 replicas over one shared
   store (ADR 0048). Two replicas ticking the same schedule store must not both
   fire the same slot — but also must not require a distributed transaction to
   avoid it. The harness already had the pattern: a durable ground-truth store
   polled by a derived in-memory timer, plus a leader-lease for
   single-writer-tick semantics (the same shape as the run-entry session lease,
   ADR 0027 Phase 4).

3. **The cloud-native arc is the substrate.** ADR 0027 shipped the four ports
   (`SessionStore`, `EventLog`, `SessionLease`, `PrunableStore`) and the
   disposable-process posture; ADR 0048 shipped the k8s-native binary over a
   Redis store + k8s lease. Scheduled tasks build directly on both: the
   `ScheduleStore` is a peer port (discovered by type assertion, wired only when
   an operator selects a backend), the fire's session rides the SAME
   `SessionStore` + `EventLog`, and the leader-lease reuses the SAME
   `SessionLease` port keyed on a well-known `__scheduler__` id.

4. **The loop stays storage-agnostic.** The agent loop (`engine/agent`) must not
   import the schedule port — the same discipline as the event log and the
   session lease. The tick loop, the cron parse, the misfire policy, and the
   leader-lease acquisition are all COMPOSITION-layer concerns, exactly as the
   run-entry lease renewer and the event-log persist live in composition.

The design went through ten resolved decisions (the issue thread). This ADR
records them as a frozen point-in-time record; current behaviour lives in
`docs/architecture.md` and the shipped/deferred status in
[Historical readiness tracker](https://github.com/stacklok/mecatl/blob/33a3747d9008691d4d51a872c9e82c050c43fafa/docs/design/PRODUCTION-READINESS.md).

## Decision

1. **At-most-once via claim-before-fire.** A fire's slot is claimed by
   atomically advancing `NextFireAt` BEFORE the fire runs (`ScheduleStore.Claim`).
   A peer replica's `Due` then no longer returns the slot, so a second Claim is
   structurally impossible. A crash mid-fire SKIPS the slot (the advance already
   happened); a recurring schedule self-heals via the `MisfireFireOnceNow`
   policy on the next tick. This is exactly-once-without-distributed-TX: there
   is no owner/claim-holder field (unlike `SessionLease`, whose `Owner` fences
   concurrent writers on the SAME id) — the durable `NextFireAt` advance IS the
   fence.

2. **`ScheduleStore` in `engine/port`.** The registry is a NEW optional port,
   discovered by type assertion exactly like `PrunableStore` / `SessionLease` /
   `EventLog`. A store/backend that does not implement it is simply never
   consulted, and composition wires a scheduler ONLY when an operator selects a
   backend by flag — the default path is byte-identical with no scheduling. The
   loop is storage-agnostic: `engine/agent` NEVER imports this port.

3. **Read-leaning default + `mutating:true`.** A schedule that does not opt in
   to writes (`Spec.Mutating == false`, the default) is treated as read-only
   for posture purposes — the same conservative default as a subagent. A
   schedule that will write (Edit/Write/Bash mutations) MUST set `Mutating`
   true; composition's posture ladder applies. There is no "schedule runs in
   yolo" shortcut.

4. **Operator-configurable `MinInterval`.** The frequency floor (a schedule
   whose cadence is tighter than this is rejected, fail-closed) is an
   operator-tier config knob, NOT a per-schedule field. It lives on the
   scheduler `Config` (documented as not read by the tick loop — the
   create-seam enforces it at Save time). The default is conservative.

5. **Reuse configured store by type-assertion.** A deployment that already wired
   a durable `SessionStore` (jsonlstore, Redis, a gRPC driver) gets scheduling
   over the SAME backend by type-asserting it for `ScheduleStore` — no separate
   `--schedule-store-url` flag by default. An independent override wins, else
   the configured store is type-asserted, else no scheduling (the v1 default).
   This mirrors the lease / event-log / prune discovery pattern.

6. **robfig/cron/v3 parser-only (NOT the daemon).** The cron expression is
   parsed by `cronparse.NextFire` (a parser-only wrapper over robfig/cron/v3's
   `ParseStandard` + `Schedule.Next`). The durable `ScheduleStore` is ground
   truth; the in-memory timer (if a future phase adds one) is a DERIVED
   lookahead over `Due`, never the source of truth. The store is parser-free —
   it stores the raw expression verbatim and never interprets it; the CALLER
   (composition) computes the next fire and hands it to `Claim`.

7. **Per-fire session ids (fire id IS the session id).** Each fire mints a fresh
   top-level session via `Service.CreateSessionWithProfile` +
   `Service.StartRunContent` with subagent-grade `RunOptions`. There is no
   "schedule session" reused across fires — each fire is a fresh, bounded,
   fully-recoverable run. The fire's full conversation/state lives in the
   `SessionStore` under that id; the `ScheduleFire` record is the
   schedule-indexed pointer to it plus the terminal stop reason.
   **Phase-1 caveat:** on the SUCCESS path the id is whatever
   `CreateSessionWithProfile` mints (an ordinary random session id), NOT a
   `sched--<schedule>-<fire>` id. Minting a `sched--`-prefixed id (for the GC
   retention family + at-a-glance provenance) needs a session-id OVERRIDE on
   `CreateSessionWithProfile` — a server-API change deferred to Phase 2. The
   `sched--…` helper (`newFireID`) is used ONLY on the create-failure fallback
   path today, so a fire has a non-empty, unique record key even when no session
   was minted.

8. **Pull-only delivery v1.** A caller polls `LoadFire` (or `List`, future) to
   discover what a fire produced, rather than the store pushing results. The
   fire record carries the terminal stop reason + any error string (a flat
   string, no structured error crosses the store). Push delivery is a later
   phase.

9. **Fail-closed model pinning.** A schedule pins its permission posture at
   creation: `Spec.Mode` (the `session.PermissionMode`), `Spec.Mutating`,
   `Spec.Limits` (subagent-grade caps: `MaxTurns`/`MaxToolCalls`/
   `MaxConsecutiveFailures`), `Spec.Profile` (the no-FS profile is a natural
   fit for a headless fire). A fire's session is created with these pinned;
   there is no "the schedule escalates its own posture mid-run" path.

10. **`MaxFires` not `Repeat`.** A cron schedule bounds its TOTAL number of
    fires via `Spec.MaxFires` (0 = forever); once `FireCount` reaches `MaxFires`
    the schedule is DONE (`Enabled=false`, `NextFireAt` zeroed — the store
    enforces this in `Claim`). A one-shot fires once by definition. There is no
    separate `Repeat` count — `MaxFires` is the single bound, and it is
    cron-only (ignored for a one-shot).

Plus the leader-lease decision: the scheduler acquires a leader-lease on the
well-known `port.SchedulerLeaderLeaseID` (`"__scheduler__"`) so that, in a
multi-replica deployment, at most one replica ticks the schedule store at a
time. It is HYGIENE, not correctness: the at-most-once `Claim` fence is what
actually prevents double-fire; the leader-lease prevents two replicas wasting
cycles ticking (and is the single-writer-tick analogue of the run-entry session
lease). `__scheduler__` is a `session.SessionID` constant; real session ids are
hex, so it cannot collide.

The **misfire policy** (read from `Spec.Misfire` by the tick loop at tick time,
not by the store): the default `MisfireFireOnceNow` fires once immediately for
the missed slot then resumes the cadence (no cascade — a slot missed by an hour
fires once, not sixty times); `MisfireSkip` skips the missed slot entirely. For
`MisfireSkip`, the tick loop still calls `Claim` to advance `NextFireAt` (so the
slot is not re-returned), but does NOT call `Fire` — logged INFO "skipped
misfire".

## Consequences

- **A one-shot can be lost on a mid-fire crash.** The claim-before-fire advance
  already happened, so a retry does not re-fire. This is the documented trade-off
  for exactly-once without distributed TX (decision #1). A recurring schedule
  self-heals via `MisfireFireOnceNow`; a one-shot does not. An operator who
  cannot tolerate a lost one-shot runs a replica set with the leader-lease wired
  (the lease reduces — does not eliminate — the window).

- **The frequency floor is operator-config.** A deployment that wants a tighter
  cadence than the default `MinInterval` sets it via operator-tier config; a
  project-tier file cannot loosen it (the same tighten-only discipline as
  guardrails, ADR 0021).

- **The leader-lease adds a `__scheduler__` lease per replica.** Only the leader
  ticks; non-leaders stand down on `ErrLeaseHeld` (logged INFO, not an error). On
  `ErrLeaseUnsupported` the leader gate is sticky-disabled and the scheduler
  ticks standalone (single-replica by affinity — the honest posture, not a silent
  double-tick). The lease is DERIVED state: a restarted process re-acquires on the
  next `Start`; a crashed holder's lease lapses after the TTL and a survivor
  takes over.

- **A long-running fire delays other schedules' fire latency (Phase-1 limitation).**
  The tick loop drives each due fire to a terminal `EvResult` and awaits the whole
  due batch before re-polling `Due`, so a fire that runs for minutes delays every
  other schedule's fire by up to its duration. This never affects at-most-once
  (the `Claim` advances `NextFireAt` before the fire runs), only fire LATENCY.
  Acceptable for Phase 1's small schedule counts; a later phase decouples the
  `Claim`/advance from the drive (a background fire pool) so the tick keeps polling.

- **`MisfireSkip` skips only slots missed beyond a grace window.** Because `Due`
  returns any slot with `NextFireAt <= now`, a freshly-due slot is essentially
  always slightly late under a polling tick. The skip therefore fires only when a
  slot is late by more than one tick interval (a genuine missed window — the
  process was down), not on ordinary poll jitter; otherwise a `MisfireSkip`
  schedule would never fire at all.

- **The scheduler warns when enabled with no lease backend.** With no lease wired
  the tick loop runs standalone and the store's `Claim` mutex is per-process only,
  so multiple replicas on a shared store double-fire. This is safe on a single
  replica (single-replica by affinity) but a composition-time WARN names the
  multi-replica hazard rather than failing silently.

- **Fresh-context-per-fire v1.** Each fire is a fresh session — no carried
  conversation, no carried memory of the prior fire. A schedule that needs
  context across fires (a digest of yesterday's findings) must persist it itself
  (via the memory store or a file) and re-load it in its prompt. Carried-context
  is a v2 concern, deferred.

- **Phase-2 amendment (one-shot retry):** an opt-in `OneShotRetry` field mitigates
  the one-shot crash-loss trade-off above for one-shots that cannot tolerate loss.
  The tick loop re-arms a crashed one-shot (prior fire ended in `StopError` or
  `LastFireSessionID == PendingFireSessionID`) up to `OneShotMaxRetries`, via the
  optional `ScheduleOneShotReArmer` interface (type-asserted on the store, exactly
  like `PrunableStore`/`SessionLease`; a store that does not implement it degrades
  to the byte-identical at-most-once path). A re-arm re-enables the schedule,
  advances `NextFireAt` with a small backoff, and increments the durable
  `OneShotRetryCount`; once the budget is exhausted the one-shot stays disabled
  (permanently done, not a crash-loop). It is one-shot-ONLY — a cron self-heals
  via misfire already, so the create-seam rejects `OneShotRetry` on a cron trigger.

- **Phase-2 amendment (carried context):** an opt-in `CarryContext` field renders
  the prior fire's conversation as a fenced untrusted preamble (via
  `agent.FenceUntrusted` + `NeutraliseFraming`), NOT as seeded history — carried
  context is untrusted (a prior fire may have been prompt-injected) and must not
  become live instructions. The fence quarantines it so a forged closing marker or
  harness section header in the prior content cannot break out of its block. On
  prior-session-load failure (not found, decode error) the fire degrades to
  fresh-context (WARN, never fails the fire). A re-armed one-shot does NOT carry
  context on the retry — the crashed fire's context is untrusted AND incomplete.

- **The composition wiring (`buildScheduler`) lands in Phase 1f.** This ADR
  records the decision; the `internal/adapter/scheduler` package (Phase 1e)
  ships the storage-agnostic tick loop + the `FireFunc` seam. Phase 1f wires
  the seam to `Service.CreateSessionWithProfile` + `Service.StartRunContent`
  and adds the `--schedule-*` flags. Until then the scheduler is inert (no
  composition wire, byte-identical default).

- **Phase-2 update (fire id + GC retention family).** Decision #7's Phase-1
  caveat is resolved: the fire path now mints a `sched--`-prefixed session id
  via a `WithSessionID` override on `CreateSessionWithProfile` (a variadic
  options pattern, NOT a positional-signature widening), so the fire id IS the
  session id AND the persisted session carries the `sched--` family prefix. A
  distinct `ScheduleFireRetention` GC family sweeps per-fire sessions on their
  OWN schedule (a peer age pass of `MainRetention`/`ChildRetention`, partitioned
  by the `sched--` prefix — never the main or child pass). The
  `--schedule-fire-retention` flag (operator-tier, peer of `--child-retention`)
  defaults to 7d when `--scheduler` is enabled; 0 disables (fire sessions are
  never swept, byte-identical to pre-Phase-2). The fire pass is now SYMMETRIC
  with the main pass: alongside the age horizon it has a GLOBAL count cap,
  `--schedule-fire-retention-max-total` (peer of `--main-retention-max-total`;
  0 disables) — the age horizon bounds the tail, the cap bounds the head (a
  per-minute cron accumulates ~10k sessions/week the horizon never trims from the
  head). The override validates a non-empty id that does not collide with a live
  per-session engine, an in-flight create holding the same id, OR a session
  already persisted under that id.

## See also

- [ADR 0027 — Cloud-native arc](./0027-cloud-native.md) — the four ports +
  the leader-lease pattern this builds on (Phase 5 adds the scheduled-tasks
  rows to List 1 / List 2).
- [ADR 0048 — mecak8s](./0048-mecak8s.md) — the k8s-native binary (Redis store,
  k8s lease, drain gate) that is the first multi-replica deployment target.
- [ADR 0036 — Engine module](./0036-engine-module.md) — the
  `engine/port` boundary the `ScheduleStore` port lives behind.
- [ADR 0037 — Engine stability contract](./0037-engine-stability-contract.md) —
  the API-compat gate that governs changes to the `ScheduleStore` surface.
- [`docs/architecture.md`](../architecture.md) — the living architecture
  reference (a scheduled-tasks section is added when Phase 1f ships).
- [Historical readiness tracker](https://github.com/stacklok/mecatl/blob/33a3747d9008691d4d51a872c9e82c050c43fafa/docs/design/PRODUCTION-READINESS.md) —
  the status tracker (a scheduled-tasks row is added when implementation ships).
