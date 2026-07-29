---
id: 05-shared-workspace-child-relax-leak
title: Shell-less (nil-forker) child must not inherit the relaxed parent workspace
blocked_by: [04-child-and-glob-confinement]
status: done
branch: "plan-path-escape-posture/05-shared-workspace-child-relax-leak"
worktree: ""
issue: ""
retries: 0
last_error: ""
accumulator: acc/path-escape-posture
---

# Task brief

Task 04 surfaced a real leak: when Bash is disabled (nil runner) the Subagent
tool wires NO forker, so the read-only child runs directly against the PARENT's
base workspace (`internal/app/build.go:5140-5142` — "no forker is wired and the
child stays a base-sharing read-only explorer"). If that parent workspace is
relaxed (`WithRelaxedReads`/`WithRelaxedWrites`), a shell-less child inherits the
main session's out-of-root reach — defeating the child-never-relaxes boundary
AC5.1 pins for the forker path. The same applies to any child engine that shares
rather than forks the relaxed main workspace.

Read `.claude/agents/tdd-worker.md` first. Fix: a child that shares the parent's
workspace must get a NON-relaxed view of it. Options (pick the minimal one that
keeps the layering rule):
- Build the shared-base child workspace WITHOUT the relaxed options (a distinct
  construction for the base-sharing child path), OR
- Give osfs a read-only-non-relaxed "view" wrapper the base-sharing child uses.
Do NOT relax the child; do NOT change the main session's relaxed behaviour. The
forker path (worktree/force-copy) is already correct — leave it.

## Acceptance criteria

- AC5.1b: a SHELL-LESS (nil-forker) read-only child running against the parent's
  relaxed base workspace has its out-of-root `Read`/`Write` denied — it does not
  inherit the relax.
  - verify: `TestPathEscapePosture_Scenario5_SharedWorkspaceChildNotRelaxed`
- AC5.1c: the forker-path child behaviour is unchanged (worktree/force-copy
  children still deny the escape; the main session still relaxes).
  - verify: `TestPathEscapePosture_Scenario5_ChildReadEscapeDenied` (existing —
    must stay green)
