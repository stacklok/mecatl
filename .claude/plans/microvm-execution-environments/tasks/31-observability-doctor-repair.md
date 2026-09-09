---
id: 31-observability-doctor-repair
title: Make production observability and doctor state truthful
blocked_by: []
status: done
branch: "plan-microvm-execution-environments/31-observability-doctor-repair"
worktree: ".scratch/task-microvm-31"
issue: "535"
retries: 0
last_error: ""
accumulator: acc/microvm-execution-environments
---

# Second panel repair

Spec blockers: production never records egress denials, successful deletion does not decrement active/resource gauges, restart resets metrics despite live records, and doctor checks declarations instead of actual signature/network/profile/stale readiness.

Wire observer calls at every production egress, verification, create/ready, exec, admission, detach/delete, cleanup/reconcile transition; prevent double-count and reconstruct active/resource gauges from durable state at startup. Make doctor run real artifact verification, network-provider readiness, profile availability/policy consistency, control peer checks, hypervisor preflight, and stale-generation detection with actionable results.

Protects AC8.3–AC8.4.

Verification: lifecycle transition table keeps gauges/counters truthful, egress denial increments, restart reconstructs gauges, doctor detects corrupt evidence/unavailable network/profile/stale ready and passes healthy case; lint/test/docs/site pass.
