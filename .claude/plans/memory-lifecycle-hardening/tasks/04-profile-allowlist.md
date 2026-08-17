---
id: 04-profile-allowlist
title: Fail closed when selecting child operator-profile access
blocked_by: []
status: done
branch: "plan-memory-lifecycle-hardening/04-profile-allowlist"
worktree: ""
issue: ""
retries: 1
last_error: ""
accumulator: acc/memory-lifecycle-hardening
---

# Task brief

Replace `childOperatorProfileSource`'s internal-role deny-list with an explicit allow-list: main (empty role), `task` and `task:` model variants, `member:` roles, and `parallel` and `parallel:` model variants. All other roles, including every judge variant, must get nil. Pin the behavior through the real composition path.

## Acceptance criteria

- AC4.1: The main engine, a Subagent child (both the plain `"task"` role and the `"task:model="` override role), a team member, and a Parallel branch (both the plain `"parallel"` role and the `"parallel:model="` override role) each receive a non-nil `OperatorProfileSource`.
  - verify: `TestADR_0226_OperatorProfileAllowListIncludesFirstClassRoles`
- AC4.2: `guardrail-checker`, `ask-reviewer`, `model-router`, `usermodel-review`, `parallel-judge` (and every other judge variant), and a synthetic role invented by the test that matches nothing in the allow-list all receive a `nil` `OperatorProfileSource`.
  - verify: `TestADR_0226_OperatorProfileAllowListDefaultExcludes` (supersedes `TestInternalPurposeChildRolesExcludeOperatorProfile`'s deny-list assertion)
