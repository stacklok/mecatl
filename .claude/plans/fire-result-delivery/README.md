# fire-result-delivery — task index

Plan: `docs/acceptance/fire-result-delivery.md` (ADR 0075). Accumulator: `acc/fire-result-delivery`.

A scheduled fire reports its result back into the originating conversation,
rendered live in the operator's mecatui. 3 waves: engine/store (1–4), embedded
TUI live card (5), remote wire subscription (6).

## Waves

- **Wave 1 (engine/store):** tasks 01–05 — origin capture, fenced render, delivery run, durable queue + drain, edges.
- **Wave 2 (embedded TUI):** tasks 06–07 — in-process session event subscription + the delivery card.
- **Wave 3 (remote wire):** task 08 — the server-streaming subscription RPC.

Tasks run in dependency order; within a wave, independent tasks dispatch in parallel.
