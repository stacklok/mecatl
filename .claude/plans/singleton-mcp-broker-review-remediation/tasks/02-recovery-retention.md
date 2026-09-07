---
id: 02-recovery-retention
title: Fresh pre-prompt recovery and bounded broker state
blocked_by: [01-transport-contract]
status: done
attempt: 1
branch: "plan-singleton-mcp-broker-review-remediation/02-recovery-retention-attempt-1"
worktree: "/Users/jakub/devel/mecatl/.worktrees/distributed-broker-contract"
issue: ""
retries: 0
last_error: ""
accumulator: acc/singleton-mcp-broker-review-remediation
---

# Task brief

Add composition-owned generation replacement used only by pre-prompt recovery, with exact
connection ownership. Bound broker-owned logical sessions, handles, receipts, and pending
control state without evicting active authority; ensure every cleanup worker joins.

## Acceptance criteria

- AC2.1: Confirmed state loss before a prompt closes the stale remote client, obtains a fresh composition-owned client, and can enroll against a replacement incarnation.
  - verify: `TestSingletonBrokerRemediation_Scenario2_FreshClientPrePromptRecovery`
- AC2.2: A live or restored protected-call continuation never invokes the fresh-client factory and settles state loss deterministically without reattachment.
  - verify: `TestSingletonBrokerRemediation_Scenario2_ProtectedContinuationNeverRebinds`
- AC2.3: Empty, malformed, and overlong logical session IDs are rejected before allocation. Configured limits for logical sessions, attachment handles, Execute receipts, and broker-owned pending enrollment/control entries reject new admission with a structured capacity reason rather than evicting, overwriting, or extending active, attached, or parked authority; each registry has absolute expiry and bounded cleanup work.
  - verify: `TestSingletonBrokerRemediation_Scenario2_BoundedAdmissionAcrossBrokerRegistries`
- AC2.4: Unattached logical sessions expire after their absolute configured retention, while attached or active sessions remain protected; shutdown joins every sweeper and closes every owned client exactly once.
  - verify: `TestSingletonBrokerRemediation_Scenario2_RetentionAndOwnership`
