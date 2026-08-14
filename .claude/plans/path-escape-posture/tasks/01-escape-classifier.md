---
id: 01-escape-classifier
title: Escape-classification seam (no behaviour change)
blocked_by: []
status: done
branch: "plan-path-escape-posture/01-escape-classifier"
worktree: ""
issue: ""
retries: 0
last_error: ""
accumulator: acc/path-escape-posture
---

# Task brief

Land the composition-layer escape classifier — a pure function answering "is
this FS-tool call an out-of-root escape?" — reusing osfs canonicalization so it
can never disagree with the tool body. No behaviour change in this wave: the
classifier is consumed by a wrapping policy later, but here it only needs to
exist and be proven correct against `resolveInRoot`.

Read `.claude/agents/tdd-worker.md` first. Layering: the classifier is
COMPOSITION (`internal/app`), not domain — `engine/tool` keeps `FileSystem`/
`Workspace` (the port↔tool cycle gotcha). You will likely need a canonicalize-
only helper exported from `internal/adapter/osfs` that reports the
in-root/escape verdict WITHOUT opening an `*os.Root` (sharing
`resolveInRoot`'s stat-based ancestor resolution). Pseudo-filesystem paths
(`/proc`, `/sys`, `/dev`) classify as a distinct never-relaxed category. Cite
ADR-0047 (docs/adr/0047-absolute-path-resolution.md)
— it governs `resolveInRoot`, the ledger key normalization, and the Glob/Grep
no-change rule.

## Acceptance criteria

- AC1.1: a relative path and an absolute path that canonicalize inside the
  workspace root classify as in-root; a `".."` traversal and an absolute path
  outside classify as escape — matching `resolveInRoot`'s verdict on the same
  inputs.
  - verify: `TestPathEscapePosture_Scenario1_ClassifierMatchesResolveInRoot`
- AC1.3: a symlink inside the workspace whose target escapes classifies as
  escape (the canonicalize-then-reject path is exercised, not bypassed).
  - verify: `TestPathEscapePosture_Scenario1_SymlinkEscapeIsEscape`
- AC1.4: the classification is a pure function with no `*os.Root` open and no
  I/O beyond the canonicalization stat syscalls `resolveInRoot` already performs.
  - verify: inspection — the classifier shares `resolveInRoot`'s stat-based
    ancestor resolution and opens no root; confirmed by review.
- AC1.5: a path under `/proc`, `/sys`, or `/dev` classifies as the never-relaxed
  pseudo-fs category (distinct from a regular escape), at every posture.
  - verify: `TestPathEscapePosture_Scenario1_PseudoFsClassification`
