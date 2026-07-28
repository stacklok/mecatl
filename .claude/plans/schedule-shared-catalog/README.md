# Plan: schedule-shared-catalog

The `Schedule` tool on every schedule-capable session — repair the ADR-0073
contract for the shared-engine fast path (the default mecatui session) by
hoisting the schedule seam into a store-shaped pre-Service `scheduleManager`
the Service delegates to.

- **Plan:** [docs/acceptance/schedule-shared-catalog.md](../../../docs/acceptance/schedule-shared-catalog.md)
- **ADR:** [docs/adr/0076-schedule-shared-catalog.md](../../../docs/adr/0076-schedule-shared-catalog.md)
- **Accumulator:** `acc/schedule-shared-catalog` (off `main`)

## Tasks (strictly ordered 1 → 2 → 3)

| # | Task | blocked_by |
|---|---|---|
| 01 | [Pre-Service scheduleManager + Service delegation](tasks/01-schedule-manager.md) | — |
| 02 | [Eager factory bind — shared catalog carries the tools](tasks/02-shared-catalog-registration.md) | 01 |
| 03 | [Shared-engine posture note + default-session e2e](tasks/03-shared-engine-posture-note.md) | 02 |
