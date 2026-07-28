# Schedule shared catalog — acceptance plan

**Phase:** capability — the `Schedule` tool on every schedule-capable session
**Status:** landed, 2026-07-28. Follow-up to the landed [schedule-tool plan](schedule-tool.md): the ADR-0073 contract ("registered in the catalog for **every** session that has a backing `ScheduleStore`") is broken for the shared-engine fast path.
**ADR:** [ADR-0073](../adr/0073-schedule-tool.md) — the model-facing `Schedule` tool, on-by-default scheduler. [ADR-0070](../adr/0070-model-visible-affordance-gate.md) — the model-visible affordance gate. [ADR-0075](../adr/0075-fire-result-delivery.md) — origin capture + fire-result delivery. New: [ADR-0076](../adr/0076-schedule-shared-catalog.md) — the pre-Service schedule manager (schedule capability is store-shaped, not Service-shaped).
**Accumulator branch:** `acc/schedule-shared-catalog` (off `main`).

The smallest set of work that makes in-chat scheduling actually reachable from
the default session: a store-backed session that takes the **shared-engine fast
path** (the plain mecatui embedded-server default) gets the `Schedule` /
`ScheduleQuery` tools in its catalog and the schedule posture note in its
system prompt — the same posture a per-session engine already has.

The doc is organized scenario-first because acceptance is about what the
running harness can demonstrate, not which packages exist on disk.

## The defect this plan closes

ADR 0073 decision 1 registers the `Schedule` tool "for every session that has a
backing `ScheduleStore`, exactly like the six memory tools." The implementation
broke that contract for the most common session: the shared engine's catalog is
assembled inside `buildEngine` → `buildCatalog` → `assembleCatalog` **before**
`server.NewService` exists, and the `port.ScheduleManager` the tool drives was
only late-bound onto the assets *after* `NewService`
(`internal/app/build.go:1531` — the "chicken-and-egg" the late-bound factory
papers over). The covering comment ("any schedule-capable session routes
through a per-session engine") is false: `sessionNeedsPerFactory`
(`internal/adapter/server/service.go:2325`) has no schedule arm, and
`needsRehydration` (`service.go:2270`) restores a restarted default-profile
session onto the shared engine. Result: on the default store-backed mecatui
session, "schedule a check for new issues in 5 minutes" has no `Schedule` tool
to call and no prompt note telling the model one exists.

The root cause is a **shape error**: schedule capability was treated as
Service-shaped (resolvable only after `NewService`) when it is store-shaped.
The memory tools the ADR cites as the model never had this problem because
their stores are built eagerly inside `buildCatalog` — no Service dependency.

## Why these scope cuts

- [ADR-0073](../adr/0073-schedule-tool.md) — the contract being honoured:
  every schedule-capable session carries the tool; the tool rides the ONE
  validated create-seam, never a second path.
- [ADR-0076](../adr/0076-schedule-shared-catalog.md) (new) — the decision this
  plan captures: schedule capability is store-shaped, so the manager is
  constructed **before** `buildEngine` and the Service **delegates** to it.
  The rejected alternative (route every store-backed session to the per-session
  factory) is recorded there with its costs.
- **The shared engine stays.** Deleting the shared-engine fast path (converge
  every session on the per-session factory) is a much bigger behavioural
  contract change — per-session engine cost + `MaxSessionEngines` slot for the
  common case, changed restart semantics for deployments that never asked for
  scheduling — and is NOT required to honour the ADR. It remains a separate,
  future decision.
- **No proto / wire change.** `Service.ScheduleManager()` keeps its signature;
  the gRPC/REST schedule surface is byte-identical (delegation, not a new API).

## In scope — 3 scenarios, in implementation order

### Scenario 1 — the pre-Service schedule manager

The schedule create/read/update/fire seam stops living on `*server.Service` as
methods that reach into `s.cfg` and becomes a standalone **`scheduleManager`**
value (new file `internal/adapter/server/schedule_manager.go`) constructed from
plain inputs available before `buildEngine`: the `port.SessionStore` (from
which the `ScheduleStore` is type-asserted — the existing
`scheduleStoreProvider` accessor), a now-func, the cadence floor, the
event-log, and diagnostics. The late-set collaborators (the in-process
scheduler for `FireNow`, the model inventory for selector validation) become
atomic / late-bound **fields on the manager** (the `SetScheduler` /
`SetScheduleMinInterval` / `SetModels` setters move onto it), never a reach
back into the Service. `*server.Service` **delegates** its nine
`port.ScheduleManager` methods + `EmitScheduleEvent` to the embedded manager,
so the RPC surface (`grpc_schedule.go`, the REST `/v1/schedules` handlers, the
mecatui `/schedule` overlay) is byte-identical. The fail-closed create-seam
(`validateScheduleSpec` + `applyScheduleDefaults` + the origin/selector/cadence
checks) moves verbatim — one seam, one truth, per
[ADR-0073](../adr/0073-schedule-tool.md) and the layering rule in
[`AGENTS.md`](../../AGENTS.md).

**Work:**
- adapters (`internal/adapter/server`): the `scheduleManager` struct +
  constructor; move the nine methods, `validateScheduleSpec`,
  `validateCronTrigger`, `validateScheduleOrigin`, `validateScheduleSelector`,
  `scheduleMinInterval`, `EmitScheduleEvent`, and the `SetScheduler` /
  `SetScheduleMinInterval` / model-inventory setters off `*Service` onto it;
  `Service` keeps thin delegating wrappers.
- composition (`internal/app`): `Build` constructs the manager from the store
  (before `buildEngine`), hands it to `server.Config` (Service consumes it,
  no longer self-discovers the store), and binds
  `assets.scheduleManagerFactory` to return it (nil when the store backs no
  `ScheduleStore`).

**Acceptance:**
- AC1.1: The manager is constructable from a `port.SessionStore` + now-func
  alone — no `*server.Service` value is required. A store with no
  `ScheduleStore` yields a nil/absent manager (the honest no-scheduling path),
  matching `ServerCapabilities.Scheduling` ([ADR-0073](../adr/0073-schedule-tool.md)).
  - verify: `TestScheduleSharedCatalog_Scenario1_ManagerIsStoreShaped`
- AC1.2: The create-seam is byte-identical after the move: a valid cron saves
  ENABLED with the cronparse-computed first fire; an invalid cron, a
  non-plan non-mutating mode, an empty workspace on a default-profile
  schedule, a `one_shot_retry` cron, and an unknown provider+model selector
  are all rejected fail-closed with `ErrInvalidArgument` and nothing saved.
  - verify: `TestScheduleTool_CreateValidatesLikeRESTSeam`, `TestScheduleTool_CreateEnforcesPhase2FieldRules` (existing — must stay green against the moved seam)
- AC1.3: The Service's RPC surface delegates without behaviour change:
  `Service.ScheduleManager()` returns non-nil exactly when the store backs a
  `ScheduleStore`, and `CreateSchedule`/`GetSchedule`/`ListSchedules`/
  `UpdateSchedule`/`DeleteSchedule`/`PauseSchedule`/`ResumeSchedule`/`FireNow`/
  `ListFires`/`EmitScheduleEvent` behave identically through the delegating
  wrapper.
  - verify: `TestScheduleSharedCatalog_Scenario1_ServiceDelegates`
- AC1.4: `FireNow` still distinguishes its three states after the move —
  `ErrNoScheduleStore` (no store), `ErrSchedulerNotRunning` (store present, no
  scheduler wired), and the mapped scheduler sentinels — with the scheduler
  late-set onto the manager, not the Service.
  - verify: `TestScheduleSharedCatalog_Scenario1_FireNowStatesPreserved`

---

### Scenario 2 — the shared catalog carries the tool

With the manager resolvable before `buildEngine`,
`assets.scheduleManagerFactory` is bound **eagerly** — so `registerScheduleTool`
(`internal/app/catalog.go:473`) fires on the build-time pass and the **shared**
catalog gains `Schedule` (mutating) + `ScheduleQuery` (read-only), exactly like
the six memory tools ([ADR-0073](../adr/0073-schedule-tool.md): "the same
conditional-registration shape as the six memory tools"). The late-bound
assignment at `internal/app/build.go:1531` and the stale "legitimately has no
Schedule tool" comment are deleted. Because the shared and per-session catalogs
are now both assembled with a bound factory, the existing
`TestPerSessionCatalogMatchesSharedCatalog` exact tool-name-set equality
([issue #42](../../AGENTS.md) — the anti-drift seam) extends to the schedule
tools with **no carve-out**. A store with no `ScheduleStore` keeps the tool
honestly absent from both (never a stub).

**Work:**
- composition (`internal/app`): bind `assets.scheduleManagerFactory` before
  `buildEngine`; delete the late-bind + comment; `registerScheduleTool` is
  unchanged (it already no-ops on a nil factory / nil manager).

**Acceptance:**
- AC2.1: The build-time shared catalog of a store-backed `Build` contains both
  `Schedule` and `ScheduleQuery`.
  - verify: `TestScheduleSharedCatalog_Scenario2_SharedCatalogHasScheduleTools`
- AC2.2: A store with no `ScheduleStore` (memstore) has neither tool in the
  shared catalog nor in any per-session catalog — the honest-absence posture,
  never a stub.
  - verify: `TestScheduleTool_RegisteredOnlyWhenStoreBacked` (existing — must stay green)
- AC2.3: `TestPerSessionCatalogMatchesSharedCatalog` passes with the schedule
  tools present in **both** catalogs (no schedule-specific exclusion in the
  assertion), proving the ONE-registration-path invariant
  ([`AGENTS.md` — per-session catalog assembly](../../AGENTS.md)) now covers
  them.
  - verify: `TestPerSessionCatalogMatchesSharedCatalog` (existing — must stay green, with any schedule carve-out removed)

---

### Scenario 3 — the default mecatui session can schedule

The observable end-to-end: a default-profile, zero-selector, store-backed
session (the shared-engine fast path — what a plain `mecatui` launch creates)
has the tools callable **and** the model told about them. The shared engine's
Deps **already** wire `OriginBinder` and `DeliveryQueue` from the assets
(`internal/app/build.go:2529` + `:2535`) — both are populated by
`registerScheduleTool` once Scenario 2 binds the manager, so the fire-result
delivery plumbing ([ADR-0075](../adr/0075-fire-result-delivery.md)) needs no new
wiring. What is missing is the **prompt note**: the shared engine's PromptConfig
never gets `applySchedulePosture`, which today is applied only in the per-session
factory (`build.go:1941`). Apply it to the shared engine under the SAME gate
(`scheduleManagerPresent(assets)`), per the
[ADR-0070](../adr/0070-model-visible-affordance-gate.md) affordance gate — the
tool exists only if the model is told it exists. A restarted default-profile
session restored onto the shared engine by `needsRehydration` keeps the tool
(the rehydration no longer bounces it onto a schedule-less engine).

**Work:**
- composition (`internal/app`): the shared-engine deps builder applies
  `applySchedulePosture` with the SAME gate the factory uses
  (`scheduleManagerPresent(assets)`). `OriginBinder` / `DeliveryQueue` are
  already wired at `build.go:2529`/`:2535` and become live as a consequence of
  Scenario 2 — no change to those lines.

**Acceptance:**
- AC3.1: A default-profile, zero-selector session on a store-backed `Build`
  resolves the `Schedule` tool from its engine's catalog (the shared-engine
  fast path — no per-session factory involved).
  - verify: `TestScheduleSharedCatalog_Scenario3_DefaultSessionHasTool`
- AC3.2: That session's built system prompt contains the schedule posture note
  (the `schedulePostureNote` "You have a Schedule tool…" suffix on the Role /
  StablePrefix layer) — the [ADR-0070](../adr/0070-model-visible-affordance-gate.md)
  gate, asserted against the layer the instruction owns.
  - verify: `TestScheduleSharedCatalog_Scenario3_SystemPromptCarriesScheduleNote`
- AC3.3: The shared engine's `OriginBinder` + `DeliveryQueue` (wired at
  `build.go:2529`/`:2535`) are live once the manager is bound, so a schedule
  created from a shared-engine session stamps its `OriginSessionID` and its
  fire's result is delivered back into that chat
  ([ADR-0075](../adr/0075-fire-result-delivery.md)).
  - verify: `TestScheduleSharedCatalog_Scenario3_OriginAndDeliveryWired`
- AC3.4: A default-profile session persisted, then reloaded via
  `needsRehydration` (the restart path), still resolves the `Schedule` tool —
  the rehydration no longer lands it on a schedule-less shared engine.
  - verify: `TestScheduleSharedCatalog_Scenario3_RehydratedSessionKeepsTool`

## Out of scope

| Item | Defer-to | ADR / decision |
|---|---|---|
| Deleting the shared-engine fast path (converge all sessions on the per-session factory) | a separate future ADR | [ADR-0076](../adr/0076-schedule-shared-catalog.md) — rejected alternative, recorded with costs |
| mecak8s wiring change | this plan touches only `internal/app` + `internal/adapter/server` | mecak8s composes the same `app.Build`; it inherits the fix with no flag — verified by AC3.x building green, no mecak8s-specific code |
| Any proto / wire change | never needed | delegation keeps the RPC surface byte-identical |
| Delivery-channel semantics (the fire→chat loop itself) | landed | [ADR-0075](../adr/0075-fire-result-delivery.md) |

## Cross-cutting deliverables

- **New ADR-0076** (`docs/adr/0076-schedule-shared-catalog.md`): schedule
  capability is store-shaped; the manager is pre-Service; the Service
  delegates. Records the rejected "route everything to the per-session factory"
  alternative.
- **`docs/design/IMPLEMENTATION-NOTES.md`**: update the schedule-tool section
  to describe the pre-Service manager + the shared-catalog registration
  (replacing the late-bound-factory description).
- **`AGENTS.md`**: the "per-session catalog" gotcha currently says the shared
  catalog legitimately lacks the Schedule tool — correct it to state both
  catalogs carry it on a schedule-capable store.
- Regenerate `llms.txt` (`task docs`).

## Sequencing recommendation

Scenario 1 first (the manager exists and the Service delegates — pure refactor,
all existing schedule tests must stay green), then Scenario 2 (bind the factory
early — the catalog gains the tools), then Scenario 3 (wire the shared-engine
Deps + prompt). 1 → 2 → 3 is strictly ordered: each is independently green, but
later scenarios depend on the earlier seam.

## Named tests landing in this plan

- `TestScheduleSharedCatalog_Scenario1_ManagerIsStoreShaped`
- `TestScheduleSharedCatalog_Scenario1_ServiceDelegates`
- `TestScheduleSharedCatalog_Scenario1_FireNowStatesPreserved`
- `TestScheduleSharedCatalog_Scenario2_SharedCatalogHasScheduleTools`
- `TestScheduleSharedCatalog_Scenario3_DefaultSessionHasTool`
- `TestScheduleSharedCatalog_Scenario3_SystemPromptCarriesScheduleNote`
- `TestScheduleSharedCatalog_Scenario3_OriginAndDeliveryWired`
- `TestScheduleSharedCatalog_Scenario3_RehydratedSessionKeepsTool`

Plus the existing guards that must stay green:
`TestScheduleTool_CreateValidatesLikeRESTSeam`,
`TestScheduleTool_CreateEnforcesPhase2FieldRules`,
`TestScheduleTool_RegisteredOnlyWhenStoreBacked`,
`TestPerSessionCatalogMatchesSharedCatalog`.

## Definition of done

1. `task lint` and `task test` pass (both modules, `-race`).
2. `task docs` — `llms.txt` regenerated and the matlatl strict link gate green.
3. `task api:check` passes (no engine exported-surface change is anticipated —
   the manager is an `internal/adapter/server` type; if any engine surface
   moves, `task api:update` + an `engine/CHANGELOG.md` note).
4. `task ac-trace-strict` — every AC's `verify:` proof resolves (this plan is
   `landed`).
5. The named tests above are green and grep-locatable by their identifiers.
6. `go run ./cmd/mecademo` still prints a full offline session (mecademo runs
   the in-memory store, so it stays on the no-scheduling path — byte-identical).
7. A default-profile store-backed session (the mecatui default) can call the
   `Schedule` tool — the reported bug is closed.

## Deferred decisions and known risks

- **Selector validation reads the live model inventory.** The manager holds a
  late-bound models reference (the same atomic snapshot `SetModels` swaps);
  a create that races the first live-catalog refresh validates against the
  seeded embedded snapshot — unchanged from today.
- **The shared engine now carries a mutating tool.** `Schedule` is
  `ReadOnly()==false` and floor-scoped (`ScopeBuiltinDefault` Allow,
  overridable), the same posture it already had on per-session engines — no
  new permission surface, but the dispatch read/mutate serialization now
  applies on the shared engine too (it already did for other mutating tools).
- **mecak8s / driver-backed stores.** A driver store that does not expose the
  `ScheduleStore()` accessor yields a nil manager and the honest absent-tool
  path, byte-identical to today.

## Exit criteria

When every point under *Definition of done* holds on the accumulator, this
plan is satisfied.
