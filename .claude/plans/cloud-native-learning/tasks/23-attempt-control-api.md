---
id: 23-attempt-control-api
title: Add authorized manual retry and abandon controls
blocked_by: [22-attempt-inspection-api]
status: pending
branch: ""
worktree: ""
issue: ""
retries: 0
last_error: ""
accumulator: acc/cloud-native-learning
---

# Task brief

Add defined retry and abandon Service/gRPC/HTTP controls over opaque expected versions. Enforce the attempt's private immutable owner before locks or worker signals, reject caller-supplied/system-principal bypasses, and preserve state on stale versions, terminal conflicts, or a live fenced claim. Successful controls must perform only repository-defined CAS transitions.

## Acceptance criteria

- AC5.3: Manual retry and abandon perform only defined CAS transitions; stale versions, terminal conflicts, and a live fenced claim return closed typed errors without changing the attempt. Private owner binding and exact-source delegation are enforced without caller-supplied principal or system-principal bypass.
  - verify: `TestADR_0254_AttemptControlsRequirePrivateOwnerBinding`
