---
id: 23-attempt-control-api
title: Add authorized manual retry and abandon controls
blocked_by: [22-attempt-inspection-api]
status: done
branch: "plan-cloud-native-learning/23-attempt-control-api-repair"
worktree: ".scratch/worktrees/23-attempt-control-api-repair"
issue: ""
retries: 0
last_error: ""
accumulator: acc/cloud-native-learning
---

# Task brief

Add defined retry and abandon Service/gRPC/HTTP controls over opaque expected versions. Enforce the attempt's private immutable owner before locks or worker signals, reject caller-supplied/system-principal bypasses, and preserve state on stale versions, terminal conflicts, or a live fenced claim. Successful controls perform only repository-defined attempt CAS transitions: manual abandon is non-compensating and promises no proposal, skill, or catalog rollback.

## Acceptance criteria

- AC5.3: Manual retry and abandon perform only defined attempt CAS transitions; abandon is non-compensating and does not promise downstream rollback. Stale versions, terminal conflicts, and a live fenced claim return closed typed errors without changing the attempt. Private owner binding and exact-source delegation are enforced without caller-supplied principal or system-principal bypass.
  - verify: `TestADR_0254_AttemptControlsRequirePrivateOwnerBinding`
