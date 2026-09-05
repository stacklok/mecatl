---
id: 03-review-repair
title: Align the target-free diagnostics contract
blocked_by: [02-review-repair]
status: in-progress
attempt: 1
branch: "plan-session-load-observability/03-review-repair-attempt-1"
worktree: ".scratch/worker-session-load-observability-03-review-repair-attempt-1"
issue: "1125"
retries: 0
last_error: ""
accumulator: acc/session-load-observability
---

# Task brief

Resolve the second panel's spec/test blocker without inventing an impossible guarantee over arbitrary operator-supplied `port.Diagnostics` implementations. The service is a singleton configured with a trusted root diagnostics sink, but the interface has no operation to strip attributes an embedder deliberately pre-bound before injection. Tighten the acceptance contract to what the implementation can and must guarantee: `Service.GetSession` uses a detached clean context and adds only the closed `class` plus constant `ownership` fields; it never adds request target/principal/path/cause/blob data. Explicitly state that attributes deliberately pre-bound by the operator-supplied sink are outside this producer's control. Preserve and, if necessary, clarify the test proving request-context baggage and direct fields do not leak. Also align the plan's prose with the actual stable field names `class` and `ownership=enforced`. Do not change caller behavior, metric behavior, adapters, or scope.

## Protected acceptance criteria

Keep AC1.1, AC1.2, and AC1.4 unchanged. Correct AC1.3 so it is precise, testable, and matches the actual diagnostics port boundary while preserving the target-free request guarantee.
