---
id: 05-vertical-compatibility
title: Verified-tool vertical proof and no-lifecycle compatibility
blocked_by: [03-typed-verifier]
status: in-progress
branch: ""
worktree: ""
issue: "367"
retries: 0
last_error: ""
accumulator: acc/logical-agent-identity-projection
---

# Task brief

**Worker gate scope:** run only focused tests for packages/files you change and any directly dependent focused tests. Do **not** run `task lint`, `task test`, `task docs`, or other repository-wide gates; the orchestrator runs those once after all tasks merge.

Add the focused vertical proof that independently verified I2 tools—not a hand-built substitute, subject string, or unrelated policy floor—are what reach the existing authority evaluator. Keep the proof root-internal/test-only and do not introduce B4 lifecycle integration. Add a structural/regression test proving I2 does not add app/server/agent invocation or new persisted token fields, while existing snapshot/event bytes remain unchanged.

## Acceptance criteria

- AC6.1: The exact independently verified reviewer tools authorize `Read` and deny a deploy tool through the existing evaluator; the token, verifier result, and evaluated capability set agree byte-for-byte.
  - verify: `TestLogicalAgentIdentityProjection_Scenario6_ReviewerCannotDeploy`
- AC6.2: A legitimate deploy-capable source and token pass the same mint→verify→evaluate path and authorize deploy, proving the denial is caused by signed authority rather than an unrelated policy floor.
  - verify: `TestADR_0252_DeployPositiveControl`
- AC6.3: Adding deploy to the compact payload after signing fails verification before the evaluator is invoked; prior tokens, logical subject, instance, or unrelated claims are never unioned into current authority.
  - verify: `TestADR_0252_VerifierGrantsCurrentToolsOnly`
- AC6.4: I2 adds no production B4 lifecycle caller or dormant spawn hook; an architecture sentinel proves `internal/app`, server, and `engine/agent` remain outside I2 invocation, while existing session-snapshot and event golden bytes remain unchanged.
  - verify: `TestADR_0252_NoLifecycleWiringOrPersistenceDrift`
