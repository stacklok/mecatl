---
id: 32-complete-production-e2e-repair
title: Cover every production acceptance path in the live journey
blocked_by: [27-ui-profile-policy-repair, 28-artifact-release-alignment-repair, 29-streaming-cancellation-repair, 30-restart-merge-serialization-repair, 31-observability-doctor-repair]
status: done
branch: "plan-microvm-execution-environments/32-complete-production-e2e-repair"
worktree: ".scratch/task-microvm-32"
issue: "535"
retries: 0
last_error: ""
accumulator: acc/microvm-execution-environments
---

# Second panel repair

Final Spec blocker: `task e2e:microvm` still omits actual app.Build/mecatui profile selection, ordered host-side streaming/cancellation, daemon restart/reconciliation, engine Subagent/Parallel/Team delegation, deny-all/UDP/port/IPv6 cases, and truthful doctor/metrics. Production E2E must use published-compatible strict evidence and actual daemon boundaries.

Expand live journey to cover all these paths with positive and negative controls. Keep Linux amd64/arm64 live and self-hosted Apple Silicon HVF live; GitHub-hosted macOS remains compile/static only.

Protects AC1.1, AC4.1–AC4.5, AC5.2–AC5.6, AC7.1–AC7.5, AC8.1–AC8.4.

Verification: local Linux KVM full production journey passes; wrong peer/evidence/policy fail; CI contract enforces all required cells and self-hosted HVF preflight; lint/test/docs/site/action/ac-trace pass.
