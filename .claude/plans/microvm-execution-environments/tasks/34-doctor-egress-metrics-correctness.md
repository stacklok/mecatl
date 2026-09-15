---
id: 34-doctor-egress-metrics-correctness
title: Fix doctor process identity and live egress metrics
blocked_by: []
status: done
branch: "plan-microvm-execution-environments/34-doctor-egress-metrics-correctness"
worktree: ".scratch/task-microvm-34"
issue: "535"
retries: 0
last_error: ""
accumulator: acc/microvm-execution-environments
---

# Third panel repair

Final Spec blockers: doctor parses real OS process-start tokens as a synthetic go-microvm string and marks healthy VMs stale; egress denials are sampled only at destruction and cumulative values are treated as increments.

Use the same platform process-identity parser/verifier in runtime/reconcile/doctor. Wire live provider denial events or delta-aware sampling into observer snapshots for active environments, with restart reconstruction and no double count.

Protects AC8.3–AC8.4.

Verification: healthy real identity passes doctor, reused/stale PID fails; live egress denial appears before destruction; repeated sampling is delta-correct; restart remains truthful; lint/test/docs/site/ac-trace pass.
