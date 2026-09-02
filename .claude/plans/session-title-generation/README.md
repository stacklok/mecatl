# Session-title-generation execution plan

Accumulator: `acc/session-title-generation`

Tasks are dependency-ordered. Each task owns the cited acceptance criteria from `docs/acceptance/session-title-generation.md`; all work follows ADR 0284. Workers branch from the accumulator, use offline TDD, run Taskfile gates relevant to their scope, and do not push.
