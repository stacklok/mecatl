# Mecatui live-feed reconnect orchestration

- **Acceptance plan:** `docs/acceptance/mecatui-live-reconnect.md`
- **Issue:** `#779`
- **Accumulator:** `acc/mecatui-live-reconnect`
- **Status:** complete
- **Scope:** one offline regression-test task for already-landed live-feed reconnect behavior.

The task state in `tasks/` is managed by `/plan-orchestrate`. The acceptance plan and ADR-0096 remain the behavioral contract. Production edits are prohibited unless a named failing acceptance test proves that the current code does not satisfy that contract. No protocol, API, ADR, or catch-up work belongs in this orchestration.
