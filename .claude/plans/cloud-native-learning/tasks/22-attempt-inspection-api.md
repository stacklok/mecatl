---
id: 22-attempt-inspection-api
title: Expose authorized bounded attempt get and list APIs
blocked_by: [21-partition-publication-isolation]
status: done
branch: "plan-cloud-native-learning/22-attempt-inspection-api"
worktree: ".scratch/worktrees/22-attempt-inspection-api"
issue: ""
retries: 0
last_error: ""
accumulator: acc/cloud-native-learning
---

# Task brief

Add transport-neutral Service get/list operations and gRPC/HTTP projections for attempts. Resolve the private owner binding before repository locks, diagnostics, count/page/cursor construction, or identifier linkage. Foreign and missing attempts must be absence-equivalent. Bound pages and opaque cursors; expose only closed state/timestamps/safe codes and proposal/skill links the caller may read. Add structural UTF-8/content-free guards.

## Acceptance criteria

- AC5.1: The owner can get and page through attempt projections, while another caller cannot infer existence, metadata, proposal IDs, skill IDs, counts, cursors, timing, or diagnostics. This non-disclosure holds before locks/signals/diagnostics and pagination/count/cursor construction; foreign and missing requests have the same absence-style result.
  - verify: `TestADR_0295_AttemptControlsAreNonDisclosingBeforeSideEffects`
- AC5.2: Attempt API projections expose only bounded state, timestamps, safe codes, and authorized identifiers; they never expose transcript, tool output, provider text, paths, principal values, credentials, tokens, headers, secret-shaped values, driver errors, diagnostics, metrics, watch envelopes, or optional EventLog projections.
  - verify: `TestADR_0295_AttemptAPIIsContentFree`
