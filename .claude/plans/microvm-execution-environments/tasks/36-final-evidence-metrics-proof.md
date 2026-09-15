---
id: 36-final-evidence-metrics-proof
title: Consume publisher evidence and assert live egress metrics
blocked_by: []
status: done
branch: "plan-microvm-execution-environments/36-final-evidence-metrics-proof"
worktree: ".scratch/task-microvm-36"
issue: "535"
retries: 0
last_error: ""
accumulator: acc/microvm-execution-environments
---

# Final acceptance repair

Two final audit blockers remain. Production E2E re-signs packaged provenance with an ephemeral key rather than consuming publisher-produced release evidence. Live denied-network probes do not query LifecycleMetrics or assert `EgressDenials` before destruction.

Make the release fixture produce the same signed evidence artifact the publisher workflow uploads and make E2E install/consume it unchanged—no test-time re-signing after packaging. Add live metrics retrieval after a denied guest connection and assert the active-generation denial count increases before destroy, then remains delta-correct after final sampling.

Protects AC2.1, AC8.1, AC8.3.

Verification: publisher fixture evidence strict-admits unchanged; tampered evidence fails; local Linux KVM full E2E passes with live pre-destroy metric assertion; action/lint/test/docs/site/ac-trace pass.
