---
id: 10-documentation-and-path-audit
title: Path-surface guard, lifecycle re-audit, and public documentation
blocked_by: [05-session-scoped-discovery, 06-clear-and-fork-successors, 07-delegation-and-artifact-boundary, 08-scheduled-placement, 09-driver-and-acp-boundary]
status: pending
branch: ""
worktree: ""
issue: ""
retries: 0
last_error: ""
accumulator: acc/server-owned-session-placement
---

# Task brief

Audit the assembled implementation's complete placement/path surface and add a structural guard classifying: public Harness/HTTP fields; durable aggregate/snapshot/driver/event-source fields; private runtime adapter and trusted operator-composition roots; and ACP cwd assertions. The first two classes must contain no physical path, while only the latter two narrowly permit one. Re-audit ADR 0027 Lists 1 and 2 against the implementation and record the explicit clean V1 verdict: no placement registry, signer, cache, process-local placement map, or other new outlives-a-call/restart-losable state; add inventory rows only if the actual code disproves that premise. Update living architecture, usage, implementation notes, AGENTS.md invariants, public `user-docs/`, compatibility/operator/deployment guidance, and generated `llms.txt` for the breaking path-free protocol, exact reattachment, discovery, successors, delegation, schedules, driver, and ACP behavior. Run docs/site generation and strict links.

Expected focus: a repository-wide structural path-surface test, `docs/architecture.md`, `docs/usage.md`, `docs/design/IMPLEMENTATION-NOTES.md`, `docs/adr/0027-cloud-native.md`, `AGENTS.md`, relevant `user-docs/`, and generated `llms.txt`. Do not refactor implementation unless the audit uncovers a direct acceptance violation; report a mis-decomposition if a substantial code repair is required.

## Acceptance criteria

- AC8.1: The complete path-surface inventory classifies public Harness/HTTP fields,
durable aggregate/snapshot/driver fields, runtime adapter/operator-composition paths,
and ACP cwd assertions. The first two are removed; only the last two are allowed.
  - verify: `TestADR_0280_PathSurfaceInventoryHasNoPublicOrDurableWorkspacePath`
- AC8.3: The ADR 0027 List 1/List 2 re-audit records any actual added resource or durable
state. V1 introduces none of a registry, signer, cache, or process-local placement map.
  - verify: `TestADR_0280_PlacementReauditFindsNoV1RegistryOrState`
