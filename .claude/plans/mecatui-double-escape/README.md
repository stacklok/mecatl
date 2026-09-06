# Mecatui double-Escape orchestration

- **Acceptance plan:** `docs/acceptance/mecatui-double-escape.md`
- **Accumulator:** `acc/mecatui-double-escape`
- **Status:** repair-in-progress
- **Scope:** one focused mecatui UI repair for a fail-closed, physical, non-remappable double-Escape draft-clear gesture: Bubble Tea enhanced event-type reporting, release-qualified repeat filtering, exact generation-tagged expiry, owner precedence, `clearPrompt` cleanup, help/docs, and scoped goldens.

| Task | Status | Dependency |
|---|---|---|
| [01 — Physical non-remappable double-Escape draft clearing](tasks/01-double-escape.md) | done | — |
| [02 — Repair double-Escape safety contract after panel review](tasks/02-panel-repair.md) | pending | 01-double-escape |

The task state in `tasks/` is managed by `/plan-orchestrate`. The acceptance plan and its cited ADRs remain the behavioral contract.
