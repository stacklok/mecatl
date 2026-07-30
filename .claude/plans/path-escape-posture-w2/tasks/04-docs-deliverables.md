---
id: 04-docs-deliverables
title: Docs deliverables (IMPLEMENTATION-NOTES section + user-docs note)
blocked_by: [01-strict-trusted-escape-ask]
status: done
branch: "plan-path-escape-posture-w2/04-docs-deliverables"
worktree: ""
issue: ""
retries: 0
last_error: ""
accumulator: acc/path-escape-posture-w2
---

# Task brief

Land the two documentation deliverables the plan names, now that the behaviour
is finished (Wave 1 + Scenario 4). Per AGENTS.md: `user-docs/` updates go in the
SAME PR as the behaviour, and `docs/design/IMPLEMENTATION-NOTES.md` is the dense
living per-subsystem companion.

Read `.claude/agents/tdd-worker.md` first. Cite code per `docs/design/README.md`
(repo-root-relative, symbols as `pkg/file.go` (`Symbol`), no line numbers).

1. **`docs/design/IMPLEMENTATION-NOTES.md`** — add a "Path-escape posture"
   section: the posture→escape-decision table, the decision-in-composition /
   serving-in-osfs split (`internal/app/escapepolicy.go`, `escapeclassifier.go`,
   osfs `WithRelaxedReads`/`WithRelaxedWrites`), the pseudo-fs never-relaxed
   rule (the `/proc/self/environ` rationale), the child-never-relax boundary,
   and the `*os.Root` containment guarantee. Dense, per the file's house style.

2. **`user-docs/`** — a short operator-facing note (lean: the posture table +
   the consent model + a link out to the full `docs/usage/`/`docs/architecture/`
   reference), placed in an existing page that covers permissions/posture if one
   fits, else a short section. Then run `task site:build` to catch any broken
   link before CI does.

3. Run `task docs` (llms.txt regen + matlatl strict link gate) — both new docs
   must be reachable or the gate fails.

## Acceptance criteria

- AC-W2-D1: `docs/design/IMPLEMENTATION-NOTES.md` carries a "Path-escape
  posture" section with citations that resolve (the docs/lint `CheckCitations`
  gate passes).
  - verify: inspection — `task docs` + the docs citation gate green.
- AC-W2-D2: a `user-docs/` note covers the escape relax (posture table +
  consent model); `task site:build` passes.
  - verify: inspection — `task site:build` green.
