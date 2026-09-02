# Predictable mecatui session handles orchestration

Accumulator: `acc/predictable-session-handles`
Acceptance plan: `docs/acceptance/predictable-session-handles.md` (`landed`)
Decision: `docs/adr/0284-predictable-mecatui-session-handles.md`
Issue: #922

Tasks 01–04 are the historical landed implementation wave. Tasks 05–07 repaired projection,
layering, traceability, and debugger help. Task 08 applies the operator's final one-`TARGET` UX:
exact IDs and displayed handles share `CreateDebugSession`, exact equality wins, and inventory
failure or zero matches falls through to server exact-ID authority. Tasks 01–08 are complete and
the acceptance plan is landed. Git ancestry is authoritative; task status fields are
orchestrator-managed.
