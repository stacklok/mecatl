---
id: 04-deployment-release
title: Honest Helm and release supply-chain surfaces
blocked_by: [02-recovery-retention, 03-production-boundary]
status: done
attempt: 1
branch: "plan-singleton-mcp-broker-review-remediation/04-deployment-release-attempt-1"
worktree: "/Users/jakub/devel/mecatl/.worktrees/distributed-broker-contract"
issue: ""
retries: 0
last_error: ""
accumulator: acc/singleton-mcp-broker-review-remediation
---

# Task brief

Make both charts, required CI deployment validation, release workflow, public docs, and the
ADR 0328 resource ledger reflect the real singleton broker. Use one public peer union because
vanilla NetworkPolicy cannot distinguish routes on the shared port.

## Acceptance criteria

- AC4.1: The mecabroker chart schema accepts one honest `networkPolicy.publicFrom` peer union for the multiplexed public port, rejects the ineffective `mecak8sFrom` and `browserCallbackFrom` keys, and documents that vanilla NetworkPolicy cannot provide route-level separation between gRPC and browser callbacks.
  - verify: `TestSingletonBrokerRemediation_Scenario4_NetworkPolicyValuesRenderExactly`
- AC4.2: The mecak8s chart accepts remote-broker address, CA Secret/key, expected DNS name, token audience, and bounded token lifetime only as one all-or-none block; it disables automatic service-account-token mounting where compatible, renders a read-only audience-bound projected token at a fixed path, and passes every required client flag without exposing token data.
  - verify: `TestSingletonBrokerRemediation_Scenario4_Mecak8sRemoteBrokerProjection`
- AC4.3: A running mecak8s remote client rereads the projected workload-token file for every RPC: after atomic rotation the next call uses only the replacement audience-bound token without restart, cached-bearer reuse, static fallback, or anonymous transport.
  - verify: `TestSingletonBrokerRemediation_Scenario4_ProjectedTokenRotation`
- AC4.4: Production rendering for both remote-broker workloads requires a canonical `sha256:` digest with 64 lowercase hexadecimal characters, rejects empty or malformed digests and simultaneous tags, and renders every container image as `repository@digest`.
  - verify: `TestSingletonBrokerRemediation_Scenario4_DigestRequired`
- AC4.5: Every release `setup-ko` step selects the same explicit approved `ko` version, and registry credentials enter shell steps only through step-scoped environment variables quoted into `--password-stdin`.
  - verify: `TestInvariant_singleton_broker_release_supply_chain_hardening`
- AC4.6: A required CI deployment job installs pinned Helm and kubeconform, runs `task deploy:check`, renders every production fixture including remote-broker mecak8s, and runs both semantic chart-test packages in a mode where a missing Helm executable fails rather than skips.
  - verify: `TestSingletonBrokerRemediation_Scenario4_DeploymentGateIsExecutable`
- AC4.7: The ADR 0328 resource-ledger amendment inventories the drain coordinator and propagation waiter, active operations, attachment/lifecycle and Execute receipts, logical-session retention, every sweeper/timer, verifier/readiness resources, ToolHive process, and replacement remote clients, with owner, capacity/retention, close/join order, and restart disposition tied to their constructors and shutdown paths.
  - verify: inspection — resource-inventory completeness requires constructor/shutdown review plus the docs gate
- AC4.8: Complete production chart rendering preserves exactly one `Recreate` broker replica, no PDB/autoscaler/HA surface, a loopback-only administration listener absent from public Services, restrictive workload security, and default-deny ingress/egress with explicit operator egress.
  - verify: `TestSingletonBrokerRemediation_Scenario4_SingletonTopologyAndExposure`
