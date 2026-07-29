---
id: 06-base-sharing-member-relax-leak
title: Base-sharing (shell-less) read-only team member must not inherit the relaxed base
blocked_by: [05-shared-workspace-child-relax-leak]
status: done
branch: "plan-path-escape-posture/06-base-sharing-member-relax-leak"
worktree: ""
issue: ""
retries: 0
last_error: ""
accumulator: acc/path-escape-posture
---

# Task brief

Task 05 fixed the Subagent nil-forker leak but flagged a sibling it didn't
cover: a base-sharing read-only TEAM MEMBER. Per `engine/agent/teamsupervisor.go`
(the "Workspace policy" doc), a read-only member that the factory CANNOT isolate
(no read-only forker / runner wired) shares the base workspace with no shell.
When that base is the relaxed main workspace (`WithRelaxedReads`/
`WithRelaxedWrites` via `escapeWorkspace`), a shell-less read-only member
inherits the out-of-root reach — the same child-never-relaxes leak as task 05,
through the supervisor's base-share fallback rather than the Subagent nil-forker
path.

Read `.claude/agents/tdd-worker.md` first. Fix: a base-sharing read-only member
must run against a NON-relaxed workspace. The task-05 mechanism
(`agent.WithSharedChildWorkspace`) only reaches the Subagent nil-forker branch;
the team supervisor needs its own equivalent. Options (minimal, layering-safe):
- Wire a shared-child-workspace constructor into the team supervisor's
  base-share fallback (composition: `buildTeamWiring`), so a base-sharing member
  gets `newForkWorkspace(skillReadRoots)` (same root, no relaxed options), OR
- An equivalent supervisor option mirroring `WithSharedChildWorkspace`.
Do NOT change the isolated-member tiers (worktree IsolateReadOnly, Mutating
force-copy — already correct), and do NOT change the main session's relax.

## Acceptance criteria

- AC5.1d: a base-sharing (shell-less) read-only team member's out-of-root
  `Read` is denied when the main session runs relaxed at `auto`/`yolo`.
  - verify: `TestPathEscapePosture_Scenario5_BaseSharingMemberNotRelaxed`
- AC5.1e: the isolated-member tiers are unchanged (worktree IsolateReadOnly and
  Mutating force-copy members still deny the escape; the main session still
  relaxes).
  - verify: `TestPathEscapePosture_Scenario5_IsolatedMembersUnchanged`
