---
id: 19-daemon-lifecycle-rpc
title: Concrete daemon lifecycle RPC and preboot guest configuration
blocked_by: [04-control-protocol, 09-network-egress, 10-lifecycle-create, 11-lifecycle-reconcile]
status: done
branch: "plan-microvm-execution-environments/19-daemon-lifecycle-rpc"
worktree: ".scratch/task-microvm-19"
issue: "532"
retries: 0
last_error: ""
accumulator: acc/microvm-execution-environments
---

# Task brief

Repair the concrete-runtime gap: define and implement the local microvmd management RPC for create, resolve/attach, detach, inspect, and delete over the authenticated control socket. Bind every operation to owner/session/ref/generation and the durable lifecycle registry. Add an explicit preboot guest configuration artifact/rootfs hook carrying IPv6-disablement and guest-agent endpoint/capability material so network policy is installed before guest workload execution. Keep hypervisor creation behind the VMRuntime port for offline tests; task 17 supplies the concrete go-microvm implementation.

This enabling task owns no new numbered plan AC. It strengthens AC2.4, AC4.4/AC4.5, and AC5.1–AC5.6 so the existing lifecycle is reachable through a real daemon surface.

## Verification obligations

- `TestMicroVMDaemon_LifecycleRPCIsOwnerAndGenerationBound`
- `TestMicroVMDaemon_PrebootConfigDisablesIPv6BeforeWorkload`
- Existing task-09 through task-11 named tests remain green.
- `task test:microvm-standalone`, `task lint`, `task test`, and docs gates pass.
