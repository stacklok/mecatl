---
id: 04-exact-reattachment
title: Exact owner-authorized placement reattachment and safe projection
blocked_by: [02-durable-placement-state, 03-public-placement-contract]
status: pending
branch: ""
worktree: ""
issue: ""
retries: 0
last_error: ""
accumulator: acc/server-owned-session-placement
---

# Task brief

Replace workspace-derived/default-following environment resolution with one exact `Reattach` path used on load and run entry. It must authorize the durable owner, operation, and original placement scope against the exact persisted kind/id/revision, return a complete environment whose ref and bound runner/workspace namespace agree, and fail as `ErrFailedPrecondition` on missing provider/resolver, authorization drift, revision mismatch, nil workspace, or identity mismatch with no fallback. Remove lazy ref stamping and workspace-based engine-rehydration triggers. Audit server inventory, event relay, diagnostics, logs, and errors so only opaque refs or bounded safe metadata can escape; never format private provider locators/roots into failures. Keep per-run environments uncached unless an existing override owns one.

Expected focus: `internal/adapter/server/service.go` run-entry/environment helpers, ownership integration, server projections/mappers/errors and exact-reattach tests. This task centralizes reattachment before parallel consumers; do not implement discovery, successors, delegation, schedules, driver/ACP, or docs.

## Acceptance criteria

- AC2.2: On load and at run entry, `Reattach` requires the exact persisted ref/revision
and returns a complete environment whose identity and bound runner share its namespace.
Missing resolver/provider, authorization drift, revision mismatch, nil workspace, or
identity mismatch is a failed precondition with no fallback.
  - verify: `TestInvariant_persisted_placement_reattaches_exactly`
- AC2.4: Public projections, events, diagnostics, logs, and errors carry only the opaque
ref or safe metadata and never a physical path or secret backend locator.
  - verify: `TestInvariant_physical_placement_paths_never_cross_public_api`
