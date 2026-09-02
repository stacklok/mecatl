# Predictable mecatui session handles orchestration

Accumulator: `acc/predictable-session-handles`
Acceptance plan: `docs/acceptance/predictable-session-handles.md` (`landed`)
Decision: `docs/adr/0284-predictable-mecatui-session-handles.md`
Issue: #922

Tasks 01–04 are the historical landed implementation wave. Panel-review repairs resumed with task
05 for resolver/CLI exact semantics, leading-hyphen projection, and API cleanup. Dependent task 06
then restored mecatui layering, relocated the rendered-header transport proof, updated ADR/plan
traceability and operator docs, removed stale digest compatibility aliases, and regenerated docs.
Tasks 05–07 are complete, including the final-panel command-help, title-documentation, and tracker
reconciliation repairs. Git ancestry is authoritative; task status fields are orchestrator-managed.
