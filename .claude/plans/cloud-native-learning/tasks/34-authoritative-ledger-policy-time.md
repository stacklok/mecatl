---
id: 34-authoritative-ledger-policy-time
title: Make automatic ledger policy and time backend-authoritative
blocked_by: [31-documentation-generated-integration]
status: in-progress
branch: ""
worktree: ""
issue: ""
retries: 0
last_error: ""
accumulator: acc/cloud-native-learning
---

# Repair brief

Repair finding: move automatic-ledger policy evaluation and current-time authority to the durable
backend. Bind an immutable policy, or a policy revision recorded with each reservation, so clients
cannot enlarge limits by submitting different configuration. Use backend time for budget windows,
cooldowns, expiry, and retention so client clock skew cannot age out charges.

## Protected acceptance criteria

Repair proof only; AC ownership remains with the existing automatic-ledger tasks.

> AC6.2: Concurrent replicas enforce one configured global automatic count/token budget, cooldown, and deduplication window without multiplying spend or durable attempts.
>
> - verify: `TestADR_0259_AutomaticAdmissionControlsAreProcessIndependent`

> AC6.3: Distributed automatic reservation is tied to deterministic attempt identity. Failure-injection covers reserve/create linkage; crashes before and after each boundary; timeout, expiry, reassignment, and abandonment; retained versus reclaimed charge; and proves that retries never exceed the configured global maximum.
>
> - verify: `TestADR_0259_AutomaticReservationsReconcileWithoutExceedingGlobalMaximum`

Add offline adversarial-client tests for enlarged client limits and skewed client clocks.
