# Predictable mecatui session handles orchestration

Accumulator: `acc/predictable-session-handles`
Acceptance plan: `docs/acceptance/predictable-session-handles.md` (`in-progress`)
Decision: `docs/adr/0284-predictable-mecatui-session-handles.md`
Issue: #922

Tasks 01–04 are the historical landed implementation wave. Panel-review repairs resume with task
05 for resolver/CLI exact semantics, leading-hyphen projection, and API cleanup. Dependent task 06
then restores mecatui layering, relocates the rendered-header transport proof, updates ADR/plan
traceability and operator docs, removes stale digest compatibility aliases, and regenerates docs.
Git ancestry is authoritative; task status fields are orchestrator-managed.
