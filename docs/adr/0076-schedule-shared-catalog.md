# ADR 0076 — The schedule manager is store-shaped and pre-Service; the shared catalog carries the Schedule tool

- Status: Accepted
- Date: 2026-07-28
- Scope: the schedule capability's composition shape — where `port.ScheduleManager` lives, when it is resolvable, and which catalogs register the `Schedule` tool.
- Supersedes: none — it **repairs** an implementation gap in [ADR 0073](./0073-schedule-tool.md) (whose decisions stand) by fixing the wiring that failed to honour them.

## Context

ADR 0073 decision 1 registers the model-facing `Schedule` tool "for every
session that has a backing `ScheduleStore`, exactly like the six memory tools."
The shipped implementation broke that contract for the most common session: the
**shared-engine fast path** (a default-profile, zero-selector session — what a
plain `mecatui` embedded-server launch creates).

The shared engine's catalog is assembled inside `buildEngine` → `buildCatalog`
→ `assembleCatalog`, but the `port.ScheduleManager` the tool drives was only
late-bound onto the catalog assets **after** `server.NewService`
(`internal/app/build.go:1531` — a self-described "chicken-and-egg"). A covering
comment claimed "any schedule-capable session routes through a per-session
engine," but nothing enforced it: `sessionNeedsPerFactory` has no schedule arm,
and `needsRehydration` restores a restarted default-profile session onto the
shared engine. The result: on the default store-backed session there was no
`Schedule` tool to call and no prompt note mentioning one — the on-by-default
feature was unreachable from the default session.

The root cause was a **shape error**: schedule capability was treated as
Service-shaped (resolvable only after `NewService`, because the manager
happened to be implemented as `*server.Service` methods) when it is in fact
**store-shaped** — every input it needs (the `ScheduleStore` type-asserted off
the session store, a now-func, the cadence floor, the event-log, diagnostics)
is available before `buildEngine`. The only genuinely late inputs (the
in-process scheduler for `FireNow`, the live model inventory for selector
validation) are late-*set*, not late-*constructed*, and can be atomic fields.
The memory tools the ADR cites as the model never had this problem precisely
because their stores are built eagerly inside `buildCatalog` with no Service
dependency.

## Decision

**Schedule capability is store-shaped, not Service-shaped.** Construct the
schedule manager as a standalone value (an `internal/adapter/server`
`scheduleManager`) **before** `buildEngine`, from the store + now-func, with
the scheduler / model-inventory as late-bound atomic fields. `*server.Service`
**delegates** its nine `port.ScheduleManager` methods + `EmitScheduleEvent` to
that manager — the RPC surface is byte-identical; the create-seam
(`validateScheduleSpec` + `applyScheduleDefaults` + origin/selector/cadence
checks) moves verbatim, one seam, one truth.

Bind `assets.scheduleManagerFactory` **eagerly** (before `buildEngine`), so
`registerScheduleTool` registers `Schedule` + `ScheduleQuery` into the **shared
catalog** on the build-time pass — the same conditional-registration shape as
the six memory tools, present exactly when the store backs a `ScheduleStore`.
The late-bound assignment and the "shared catalog legitimately has no Schedule
tool" comment are deleted. The shared engine's `OriginBinder` / `DeliveryQueue`
already ride the assets (populated by `registerScheduleTool` once the manager is
bound), so delivery needs no new wiring; the shared engine additionally gets the
`applySchedulePosture` prompt note the per-session factory applies, so the model
is told the tool exists ([ADR 0070](./0070-model-visible-affordance-gate.md)).

**The shared-engine fast path stays.** The rejected alternative — delete the
shared engine and route every store-backed session through the per-session
factory — honours the ADR but re-prices the common case (a per-session engine +
a `MaxSessionEngines` slot per session, changed restart semantics for
deployments that never asked for scheduling) to preserve what was only a
build-order accident. That convergence, if ever wanted, is a separate decision
with its own cost/benefit, not a side effect of this repair.

## Consequences

- **The ADR-0073 contract is honoured for the default session.** A
  store-backed session has the tool whether it rides the shared engine or a
  per-session engine, on create and on restart-rehydration. mecatui,
  mecated, and mecak8s (all of which compose the same `app.Build`) inherit the
  fix with no flag.
- **The chicken-and-egg is gone, not routed around.** There is one manager,
  constructed once, consumed by both the catalog tools and the Service's RPC
  delegation — no second create path to drift.
- **`TestPerSessionCatalogMatchesSharedCatalog` now covers the schedule
  tools** with no carve-out — the ONE-registration-path invariant extends to
  them.
- **Cost: a small struct extraction.** The schedule methods move off
  `*Service` onto `scheduleManager`, and `SetScheduler` /
  `SetScheduleMinInterval` / the model-inventory reference move onto the
  manager. The Service keeps thin delegating wrappers, so no caller (gRPC,
  REST, the mecatui overlay) changes.
- **The shared engine now carries a mutating tool.** `Schedule` is
  `ReadOnly()==false` and floor-scoped (`ScopeBuiltinDefault` Allow,
  overridable) — the same posture it already had on per-session engines, so no
  new permission surface; the dispatch read/mutate serialization already
  applies on the shared engine for other mutating tools.

## See also

- [ADR 0073](./0073-schedule-tool.md) — the contract this repairs (model-facing
  tool, on-by-default scheduler).
- [ADR 0070](./0070-model-visible-affordance-gate.md) — the affordance gate: a
  tool exists only if the model is told it exists (the prompt-note half of the
  fix).
- [ADR 0075](./0075-fire-result-delivery.md) — origin capture + delivery, wired
  onto the shared engine by the same change.
- [ADR 0002](./0002-documentation-lifecycle.md) — the ADR lifecycle.
