---
id: 27-ui-profile-policy-repair
title: Wire mecatui profile selection and enforce one effective daemon policy
blocked_by: []
status: done
branch: "plan-microvm-execution-environments/27-ui-profile-policy-repair"
worktree: ".scratch/task-microvm-27"
issue: "533"
retries: 0
last_error: ""
accumulator: acc/microvm-execution-environments
---

# Second panel repair

Remaining Spec blockers: mecatui production adapter never calls profile-aware session creation; root profile sends partial policy while daemon enforces unrelated global egress/admission and reports root values, allowing enforcement/status drift.

Expose environment-profile selection in mecatui configuration/sessionAdapter and use the profile-aware client. Define one effective-policy contract: either daemon-owned opaque profile ID or fully validated requested policy. Ensure mounts, seccomp, lifecycle, quotas, resources, and egress cannot be dropped or merely echoed; reported status comes from daemon-enforced resolved policy.

Protects AC1.1–AC1.3, AC4.5, AC6.1, AC6.3.

Verification: real mecatui adapter creates selected profile; daemon rejects unknown/mismatched policy; resolved status equals enforced policy; default path unchanged; lint/test/docs/site pass.
