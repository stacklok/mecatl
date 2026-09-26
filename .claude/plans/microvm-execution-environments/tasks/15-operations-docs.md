---
id: 15-operations-docs
title: Bounded observability, doctor diagnostics, and user docs
blocked_by: [02-profile-paths, 03-artifact-verification, 09-network-egress, 11-lifecycle-reconcile]
status: done
branch: "plan-microvm-execution-environments/15-operations-docs"
worktree: ".scratch/task-microvm-15"
issue: "535"
retries: 0
last_error: ""
accumulator: acc/microvm-execution-environments
---

# Task brief

Add low-cardinality metrics/diagnostics, operator readiness/doctor reporting, architecture/usage/user docs, configuration reference, and resource inventories. Follow the Diagnostics rule: events own model/client facts; operator diagnostics own facts with no event.

## Acceptance criteria

- AC8.3: Metrics and diagnostics expose boot latency, active/booting VMs, bounded resource
  use, execs, egress denials, artifact verification, cleanup/reconciliation, and quota
  rejection without command content, credentials, or unbounded labels.
  - verify: `TestMicroVMEnvironments_Scenario8_ObservabilityIsBoundedAndSecretFree`
- AC8.4: An operator-facing doctor/diagnostic path reports hypervisor access, runtime and
  firmware verification, control-socket authentication, network-provider readiness,
  profile availability, and stale resources with actionable remediation.
  - verify: `TestMicroVMEnvironments_Scenario8_DoctorReportsActionableReadiness`
- AC8.5: User documentation explains installation, source/worktree/guest paths, artifact
  trust, guest-versus-host egress, session lifetime, retention/deletion, platform
  prerequisites, and recovery without claiming schedules or remote/multi-user support.
  - verify: inspection — `task docs` and `task site:build` prove the documented surface is linked and buildable; content accuracy is reviewed against ADR-0108
