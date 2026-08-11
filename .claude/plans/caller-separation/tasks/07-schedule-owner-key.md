---
id: 07-schedule-owner-key
title: Owner-namespace the schedule creation key
blocked_by: [01-ownership-core, 02-persisted-resources]
status: done
branch: "plan-caller-separation/07-schedule-owner-key"
worktree: ""
issue: "368"
retries: 0
last_error: ""
accumulator: acc/caller-separation
---

# Owner-namespace the schedule creation key

A live-cluster probe against a real kind deployment (two real Dex-authenticated
callers, Alice and Bob) found that `CreateSchedule`'s collision guard
(`internal/adapter/server/schedule_manager.go`) checks the globally-keyed schedule
`Name` before the create-time owner is resolved, so a name already used by a
*different* owner is rejected with a distinguishing "already exists" error instead of
the absence-style behavior ADR-0102 decision 1 requires for every other cross-owner
collision in this plan. The store's unconditional upsert-by-name (`Save` in both
`internal/adapter/redisstore/schedulestore.go` and
`internal/adapter/store/jsonlstore/schedulestore.go`) makes this a latent cross-tenant
clobber risk too, not just an existence leak: the collision guard's blanket rejection
is the only thing preventing a same-named create from a different owner from
overwriting the original.

An architectural review (dispatched against this exact finding) confirmed the fix is
scoped narrowly: schedule `Name` is a caller-chosen, human-readable key exactly like a
memory key (already correctly owner-namespaced in task 03's `memory.CallerStore`), not
a system-generated identifier like a session ID, team ID, or event-log key (which are
correctly handled by today's authorize-on-load pattern and need no change). Session
leasing (both the local `flocklease` and k8s `k8slease`/coordination.k8s.io backends)
was also reviewed and found to need no change — a lease is only ever reached after the
Service has already authorized the session, and is never a caller-addressed resource
in its own right.

## Design (from the architectural review — adjust if implementation surfaces new
constraints, but stay within this shape)

- Compute the owner-derived physical schedule key ONCE, in composition
  (`internal/adapter/server/schedule_manager.go`), mirroring `memory.CallerStore`'s
  owner-digest scheme (a digest of the verified `(Issuer, Subject)` pair). Do NOT
  change `port.ScheduleStore`'s interface or either backend
  (`redisstore`/`jsonlstore`) — they continue to see and store an opaque `name
  string`; only the manager decides which physical string that is.
- `Schedule.Spec.Name` keeps carrying the literal, caller-visible name the caller
  supplied — for display, events, tool-result text, and every place a schedule name is
  echoed back. Only the store-facing physical key changes. Audit every place
  `Spec.Name` crosses into a proto/event/log/error string to confirm it is always the
  literal unprefixed value, never the physical key.
- The tick loop (`internal/adapter/scheduler/scheduler.go`'s `Due`/`Claim`/fire path)
  stays untouched: it already treats the store's `name` as an opaque scan result and
  round-trips it into `Claim`/`RecordFire`. As long as `Due` and `Claim` agree on
  which string addresses the physical record, the loop does not need to know about
  owners at all.
- Ownerless (non-OIDC / no verifier wired) schedule creation and collision detection
  must stay byte-identical to today: a single flat namespace. Use a reserved sentinel
  namespace segment for the nil-owner case rather than skipping the prefix entirely,
  so `List`/`Due`'s scan pattern and the collision check both have one well-defined
  namespace to operate over regardless of whether OIDC is enabled.
- No migration: this is a breaking, no-migration change, consistent with this plan's
  existing "no adoption of ownerless historical records" posture (already documented
  in `docs/acceptance/caller-separation.md`'s Out of scope table and ADR-0102's
  deferred decisions).
- Product decision (confirmed, breaking change accepted): two different owners MAY use
  the identical schedule name without collision — full parity with memory's "equal
  logical keys do not collapse the stores" (AC2.2). The only way to make a same-name
  create truly indistinguishable from "no conflict" is for it not to conflict.

## Acceptance criteria

- AC6.1: A create using a schedule name already used by a *different* owner succeeds
  and creates the caller's own independent schedule; it does not overwrite, adopt, or
  block on the other owner's use of that name, and the two schedules are independently
  loadable, updatable, and deletable by their respective owners.
  - verify: `TestCallerSeparation_Scenario6_SameNameDifferentOwnersDoNotCollide`
- AC6.2: A create using a schedule name already used by the *same* owner is still
  rejected as a same-owner collision, unchanged from today's behavior.
  - verify: `TestCallerSeparation_Scenario6_SameOwnerCollisionStillRejected`
- AC6.3: No create response, error, or timing distinguishes "this name is already used
  by a different owner" from "this name is available" — a caller cannot learn that
  another caller already has a schedule with a given name.
  - verify: `TestCallerSeparation_Scenario6_CollisionProbeDoesNotLeakOtherOwner`
- AC6.4: With no verifier wired (ownerless/non-OIDC deployment), schedule creation and
  same-name collision detection remain byte-identical to today: a single flat
  namespace, unaffected by the owner-prefixed key change.
  - verify: `TestCallerSeparation_Scenario6_OwnerlessNamespaceUnchanged`

## Also fix while here

- `cmd/mecak8s`'s `appConfig` (`cmd/mecak8s/flags.go`) was found, live, to never set
  `OwnershipEnforced` at all (unlike `cmd/mecated`, which wires
  `OwnershipEnforced: cfg.oidc.Enabled()`) — the entire caller-separation feature was
  inert for the actual k8s deployment binary despite the OIDC verifier correctly
  attributing ownership. This has ALREADY been fixed and live-verified against a real
  kind cluster (Bob denied absence-style access to Alice's session across
  list/get/prompt/fork after the fix); the fix is sitting as an uncommitted change on
  the worktree at `cmd/mecak8s/flags.go`. Include it in this task's commit (it is the
  same "a binary silently shipped without the enforcement wired" class of bug this
  task's schedule fix belongs to) rather than losing it — do not revert or redo it,
  just verify it's still present and fold it into this task's commit.
