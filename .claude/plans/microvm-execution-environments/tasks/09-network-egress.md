---
id: 09-network-egress
title: Explicit guest networking and egress enforcement
blocked_by: [03-artifact-verification, 04-control-protocol]
status: done
branch: "plan-microvm-execution-environments/09-network-egress"
worktree: ".scratch/task-microvm-09"
issue: "531"
retries: 0
last_error: ""
accumulator: acc/microvm-execution-environments
---

# Task brief

Always configure go-microvm's selected network provider and implement the guest-only deny/allow policy with IPv6 parity or explicit disablement. Surface honest separate guest and host egress status. Ordinary tests use deterministic network fakes; live bypass coverage lands in task 16.

## Acceptance criteria

- AC4.4: VM creation always configures the selected network provider. Failure to start
  or enforce it aborts creation and never selects implicit or allow-all networking.
  - verify: `TestADR_0108_ExplicitNetworkProviderNeverDegrades`
- AC4.5: Deny-all and configured hostname/port/protocol allowlists work from a real
  guest; IPv6 is either equivalently filtered or disabled and demonstrably unavailable.
  - verify: `TestMicroVMEnvironments_Scenario4_GuestEgressIsFailClosed`
- AC4.6: Session/UI status and documentation separately report guest-process egress and
  host-service egress; guest deny-all never claims to constrain LLM providers,
  WebFetch, WebSearch, MCP, hooks, OCI pulls, or telemetry.
  - verify: `TestMicroVMEnvironments_Scenario4_EgressScopeIsHonest`
