---
id: 08-scheduled-placement
title: Durable owner-scoped exact placement for scheduled fires
blocked_by: [04-exact-reattachment]
status: pending
branch: ""
worktree: ""
issue: ""
retries: 0
last_error: ""
accumulator: acc/server-owned-session-placement
---

# Task brief

Replace schedule workspace/default intent with the durable creator owner, original placement scope, and exact `PlacementRef` revision (or explicit no-FS attenuation). Update schedule stores, conformance, driver schedule mapping, wire schedule messages, and the Schedule tool so no model/public schedule path can provide a filesystem path or request "current default" at fire time. At every fire, invoke exact owner-scoped reauthorization/reattachment before creating the `sched--` session or touching a filesystem; use the captured owner rather than daemon identity. On drift, unavailability, hidden provider, or ref mismatch, create no session and record an operator-visible failed fire without exposing backend locators.

Expected focus: `engine/port/schedule.go`, schedule store/conformance adapters, `engine/agent/scheduletool.go`, `contracts/proto/mecatl/v1/schedule.proto` plus regenerated schedule bindings, `internal/app/scheduler_fire.go`, and schedule server/driver mapping tests. Avoid Harness proto regeneration outside schedule messages and avoid discovery, successors, delegation, ACP, and docs.

## Acceptance criteria

- AC6.1: A schedule persists its durable owner principal, original placement scope, and
exact `PlacementRef` revision (or explicit no-FS attenuation), never “follow current
default” intent or a path.
  - verify: `TestADR_0280_SchedulePersistsOwnerScopeAndExactPlacementRef`
- AC6.2: Each fire reauthorizes and reattaches with that owner and scope, not daemon
identity. Drift, unavailability, or mismatch records an operator-visible failure without
creating a session or accessing a filesystem.
  - verify: `TestInvariant_scheduled_placement_is_reauthorized_at_fire`
