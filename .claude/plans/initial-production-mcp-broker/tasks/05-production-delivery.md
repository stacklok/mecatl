---
id: 05-production-delivery
title: Standalone broker command, one-replica deployment, and operator documentation
blocked_by: [04-mecak8s-continuation]
status: pending
attempt: 0
branch: ""
worktree: ""
issue: ""
retries: 0
last_error: ""
accumulator: acc/initial-production-mcp-broker
---

# Task brief

Deliver the production surface: `cmd/mecabroker`, build/release image wiring, dedicated one-replica Recreate Helm chart, isolated admin health/readiness/drain listener, bounded shared admission/drain coordinator, topology ADR with resource ledger, architecture/usage/public docs. The chart must not create HA, PDB, scaling, outer Redis, or fictitious FQDN NetworkPolicy enforcement. Use Taskfile builds and operator-provided concrete egress policy. Preserve no-HA/restart interruption wording everywhere.

## Acceptance criteria

- AC5.1: `task build` produces `bin/mecabroker`, and the release image configuration includes the binary without changing existing image entrypoints.
  - verify: `TestInitialProductionMCPBroker_Scenario5_BuildSurface`
- AC5.2: The deployment runs exactly one broker replica with explicit CPU/memory requests and limits, non-root, seccomp, read-only-root-filesystem, dropped-capability, ServiceAccount-isolation, TLS/identity material, readiness/liveness, and pre-stop. Its NetworkPolicy ingress admits only documented mecak8s gRPC and browser callback traffic; egress is operator-supplied CIDR/namespace/pod policy for OIDC/JWKS, upstream OAuth, and MCP destinations, with dynamic endpoint limitations documented rather than fictional FQDN enforcement.
  - verify: `TestInitialProductionMCPBroker_Scenario5_DeploymentSecurity`
- AC5.3: Readiness remains false until TLS identity, bounded OIDC verifier health, route/profile configuration, ToolHive construction, anonymous discovery, and static protected-route validation succeed; it performs no user login or tool execution. Each failed prerequisite makes it false, and readiness is never used as an ownership or stale-worker fence.
  - verify: `TestInitialProductionMCPBroker_Scenario5_Readiness`
- AC5.4: Drain atomically rejects new gRPC and callback work, waits a configured non-negative endpoint-propagation interval, allows active work until a finite configured drain deadline, then cancels/settles remaining operations, stops listeners, and closes ToolHive/local resources without leaking secrets.
  - verify: `TestInitialProductionMCPBroker_Scenario5_Drain`
- AC5.6: Architecture, usage, public user docs, deployment examples, and the planned topology ADR/resource ledger describe the one-replica availability boundary and provide no outer-broker Redis/HA setup for this slice; ADR 0027 remains frozen historical context.
  - verify: inspection — documentation and resource-inventory correctness require review plus `task docs`/`task site:build`
