---
id: 37-live-metric-delta-proof
title: Prove live and final egress metric delta correctness
blocked_by: []
status: done
branch: "plan-microvm-execution-environments/37-live-metric-delta-proof"
worktree: ".scratch/task-microvm-37"
issue: "535"
retries: 0
last_error: ""
accumulator: acc/microvm-execution-environments
---

# Final audit repair

The live E2E proves an active denial increase but would still pass if destruction re-added the provider's cumulative total. While the generation remains active, take repeated samples and prove stability without new denials; after one additional denial prove an exact +1 delta; after destroy/final sample prove the exact final value does not double count.

Protects AC8.3.

Verification: local Linux KVM E2E exact deltas, unit delta sampling, lint/test/docs/action/ac-trace.
