# schedule-tool — task index

Plan: [`docs/acceptance/schedule-tool.md`](../../../docs/acceptance/schedule-tool.md)
ADR: [`docs/adr/0073-schedule-tool.md`](../../../docs/adr/0073-schedule-tool.md)
Accumulator: `acc/schedule-tool` (off `main`).

In-chat scheduled tasks: a model-facing `Schedule` tool, the scheduler on by
default, and the clean removal of the `settings.yaml` `schedules:` block + the
`mecated schedules` CLI.

| Task | Title | blocked_by |
|---|---|---|
| 01 | Schedule tool core (engine/agent + engine/port.ScheduleManager) | — |
| 02 | create-seam validation parity | 01 |
| 03 | scheduler on by default + cadence floor + selector validation + flag flip | 01 |
| 04 | remove settings `schedules:` block + `mecated schedules` CLI | 01 |
| 05 | model-visible affordance + floor/posture gates + full in-chat flow | 01, 02, 03, 04 |

Wave 1: 01. Wave 2: 02, 03, 04 (parallel). Wave 3: 05.
