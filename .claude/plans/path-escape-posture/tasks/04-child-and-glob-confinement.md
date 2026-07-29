---
id: 04-child-and-glob-confinement
title: Child engines never relax + Glob/Grep confined (all postures)
blocked_by: [02-relaxed-read]
status: done
branch: "plan-path-escape-posture/04-child-and-glob-confinement"
worktree: ""
issue: ""
retries: 0
last_error: ""
accumulator: acc/path-escape-posture
---

# Task brief

Pin the two boundaries the relax must never cross. (1) Child engines never
relax: a Subagent / team member / Parallel branch builds its FS workspace
through the SAME shared `newForkWorkspace` helper, and that helper must never
receive the relaxed options — otherwise a read-only explorer child (which has
Read/Grep/Glob even where its Bash shell is gated) would silently gain the main
session's escape reach. This is a SCOPE boundary, not a trust boundary — it
holds at every posture. (2) Glob/Grep stay workspace-confined in every posture
(patterns are not paths — ADR-0047 point 5).

Read `.claude/agents/tdd-worker.md` first. This task is largely GUARD tests:
- AC5.1 is a behavioural test: a read-only child engine's out-of-root `Read` is
  denied even when the main session runs relaxed at `auto`/`yolo`.
- AC5.2 pins Glob/Grep confinement at every posture incl. `yolo`.
- AC5.3 is a STRUCTURAL proof: assert no call site of the relaxed construction
  option exists under `newForkWorkspace` / `buildSubagentTool` / `buildTeamWiring`
  / `buildMemberEngine` / the parallel child engine builder. (A source-scan or a
  construction-path test that builds each child engine and asserts its workspace
  lacks the relaxed option.)
- AC5.4 pins the zero-value (no relaxed option) workspace denies an out-of-root
  read at every posture.

These tests should FAIL if a later change wires the relaxed option into a child
path — write them against the child-engine construction seams in
internal/app/build.go.

## Acceptance criteria

- AC5.1: a read-only child engine's `Read` of an out-of-root absolute path is
  denied, even when the main session runs relaxed at `auto`/`yolo` — the relax
  does not propagate to the child.
  - verify: `TestPathEscapePosture_Scenario5_ChildReadEscapeDenied`
- AC5.2: `Glob` and `Grep` never serve out-of-root matches at any posture,
  including `yolo`.
  - verify: `TestPathEscapePosture_Scenario5_GlobGrepConfined`
- AC5.3: the relaxed escape options are absent from every child engine
  derivation — proven structurally: no call site of the relaxed construction
  option exists under `newForkWorkspace` / `buildSubagentTool` /
  `buildTeamWiring` / `buildMemberEngine` / the parallel child engine builder.
  - verify: `TestPathEscapePosture_Scenario5_ChildEnginesNeverRelaxed`
- AC5.4: the zero-value workspace construction (no relaxed option) denies an
  out-of-root absolute read at every posture — the default stays deny.
  - verify: `TestPathEscapePosture_DefaultConstructionDeniesEscape`
