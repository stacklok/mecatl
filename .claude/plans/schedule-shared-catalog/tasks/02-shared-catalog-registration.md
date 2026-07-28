---
id: 02-shared-catalog-registration
title: Eager factory bind — the shared catalog carries Schedule/ScheduleQuery
blocked_by: [01-schedule-manager]
status: done
branch: "plan-schedule-shared-catalog/02-shared-catalog-registration"
worktree: ""
issue: ""
retries: 0
last_error: ""
accumulator: acc/schedule-shared-catalog
---

# Task brief

Bind `assets.scheduleManagerFactory` EAGERLY — before `buildEngine` — to the
pre-Service `scheduleManager` built in task 01, so `registerScheduleTool`
(`internal/app/catalog.go:473`) fires on the BUILD-TIME pass and the SHARED
catalog gains `Schedule` (mutating) + `ScheduleQuery` (read-only), exactly like
the six memory tools ([ADR-0073](../adr/0073-schedule-tool.md)).

Delete the late-bind at `internal/app/build.go:1531`
(`assets.scheduleManagerFactory = svc.ScheduleManager`) and the stale
"legitimately has no Schedule tool" comment that justifies it. The factory now
resolves the manager built from the store (nil when the store backs no
`ScheduleStore`) — so it is set BEFORE `buildCatalog` runs inside `buildEngine`,
and every `assembleCatalog` call (build-time shared AND every per-session
factory) reads a bound factory.

`registerScheduleTool` is UNCHANGED (it already no-ops on a nil factory / nil
manager). `scheduleManagerPresent` is UNCHANGED (same gate). A store with no
`ScheduleStore` keeps the tool honestly absent from both the shared and
per-session catalogs (never a stub).

Because the shared and per-session catalogs are now both assembled with a bound
factory, the existing `TestPerSessionCatalogMatchesSharedCatalog` exact
tool-name-set equality (issue #42 anti-drift seam) now covers the schedule
tools. If that test (or any helper) carried a schedule-specific exclusion /
carve-out, REMOVE it — the two catalogs must be exactly equal.

## Acceptance criteria

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
