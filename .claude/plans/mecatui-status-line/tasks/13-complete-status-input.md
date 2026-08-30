---
id: 13-complete-status-input
title: Populate canonical status input
blocked_by: [11-source-naming-settings]
status: done
branch: plan-mecatui-status-line/13-complete-status-input
worktree: ""
issue: ""
retries: 0
last_error: ""
accumulator: acc/mecatui-status-line
---

# Task brief

Populate every declared raw `Input` fact from actual UI state: server transport/target; session/model/effort; usage/cache raw and humanized atoms; context raw/humanized atoms/percent; local/remote/unknown workspace provenance; main-agent state/activity/approval; direct/team/parallel leaf state summaries; geometry; and raw clock. Source may refresh clock autonomously but the canonical submitted input must carry it. Pin complete projection and exclusion tests.

## Acceptance criteria

Protect AC1.1–AC1.3 and the accepted raw command/template input contract.
