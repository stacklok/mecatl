---
id: 17-helm-affinity-neutrality
title: Helm external-boundary neutrality
blocked_by: []
status: done
branch: "plan-session-affinity-and-handoff/17-helm-affinity-neutrality"
worktree: ".scratch/task-session-affinity-17"
issue: ""
retries: 0
last_error: ""
accumulator: acc/session-affinity-and-handoff
---

# Task brief

Strengthen the Helm chart's existing external-boundary guard so every fixture rejects `BackendTrafficPolicy` alongside Gateway/Route/Certificate resources, and add a schema/source guard proving no affinity values subtree exists.

**Likely scope:** `deploy/helm/mecak8s/chart_test.go`, chart fixtures/schema tests, and deployment checks only. Do not add any chart value, template, Gateway API object, affinity policy, or general NetworkPolicy.

**Invariants:** ADR-0278 remains the boundary; the existing narrow raw-driver NetworkPolicy exception is unchanged; the chart is infrastructure-neutral. Start with the named failing guard, use local Helm rendering only, and run Helm plus `task deploy:check` offline.

## Acceptance criteria

- AC8.1: Every chart fixture renders no `Gateway`, `HTTPRoute`, `GRPCRoute`,
  `TLSRoute`, `Route`, `Certificate`, or `BackendTrafficPolicy`; the existing narrow
  chart-owned NetworkPolicy exception is unchanged.
  - verify: `TestMecak8sHelmChart_EdgeFixtureRendersNoExternalBoundaryResources`

- AC8.2: Helm tests and schema/lint gates pass without adding a gateway or affinity
  values subtree to this chart.
  - verify: `TestADR_0291_HelmHasNoAffinityPolicySurface`
