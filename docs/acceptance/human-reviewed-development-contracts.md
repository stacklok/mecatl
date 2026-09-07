# Human-reviewed development contracts — acceptance plan

**Phase:** development-process contract
**Status:** landed, 2026-09-06. Candidate transition after checker fixtures, plan checks, documentation gates, and diff validation passed; authoritative only when this Combined PR merges.
**Delivery:** Combined. This focused change has no runtime/public/operator/persistence/trust-boundary interface; the process skills and documents are themselves the complete interface being reviewed.
**Expected tasks:** 1
**Combined rationale:** The process contract and its repository documentation are the complete deliverable, so a separate plan-review PR followed by an implementation PR would review the same indivisible change twice and add no review value.
**ADR:** [ADR 0304](../adr/0304-human-reviewed-development-contracts.md) — human contract review precedes autonomous implementation.

The smallest change that makes behavioral and exact-interface review a durable checkpoint
before autonomous implementation, while preserving isolated TDD and final code review.
This proposal PR enacts the process interface itself; no runtime code implementation
follows.

## Human decisions

None — the process contract contains no unresolved choice.

## Interface contract

- **gRPC / protobuf:** None — repository development workflow only; no wire contract changes.
- **Exported Go APIs / interfaces:** None — no Go source or engine API changes.
- **Tool schemas:** None — no model-facing runtime tool changes.
- **CLI / config:** None — no binary flags, settings keys, defaults, or precedence changes.
- **Events / persistence:** None — no runtime events, snapshots, stores, or migrations change.
- **Security / authority:** None — no runtime trust or permission boundary changes; process authority is strengthened because interface-bearing work requires a merged Plan / Interface PR before autonomous implementation, material drift requires another human-reviewed amendment, and every implementation still ends at a human merge.
- **Compatibility / migration:** Existing historical `.claude/plans/**` files remain untouched. Future orchestration state moves to ignored `.scratch/orchestrate/{slug}/`; durable acceptance plans retain their paths and gain the five-state lifecycle and mandatory interface declaration.

## In scope — 1 scenario, in implementation order

### Scenario 1 — A contributor follows two reviewable contracts

A contributor can follow one consistent flow from proposed contract through approved
baseline, isolated TDD implementation, panel review, and final human merge. The living
workflow, skills, PR template, and checker agree with [ADR 0304](../adr/0304-human-reviewed-development-contracts.md)
and the repository's [agent contract](../../AGENTS.md).

**Acceptance:**
- AC1.1: Every new acceptance plan declares an allowed status prefix, Split or Combined
  delivery, and non-placeholder effects under all seven exact canonical category labels;
  Combined additionally declares exact `**Expected tasks:** 1` metadata and a non-placeholder
  `**Combined rationale:**` explaining why separate plan review adds no value. The bundled
  checker rejects omissions, missing categories, `TBD`/`<...>` placeholders, bare `None`,
  and `None` without a hyphen/en dash/em dash plus rationale.
  - verify: `.claude/skills/to-acceptance-plan/scripts/check-acceptance-plan-test.sh`
- AC1.2: Split delivery opens a dedicated Plan / Interface PR and stops. Combined is the
  compact one-task exception, prepares the plan on the eventual combined branch without a
  separate plan PR, and requires explicit orchestration to add implementation and open the
  sole PR. This workflow-only meta-change reviews process documents and skills as its
  declared interface in that Combined PR.
  - verify: inspection — compare ADR 0304, development process, onboarding, both spine skills, acceptance guide/template, and PR template.
- AC1.3: Plan PR issue references are non-closing; only a fully completing final
  implementation PR uses a closing keyword, and commits never do.
  - verify: inspection — review the plan skill, orchestrator, development process, and PR template.
- AC1.4: Future task/attempt/retry state is ignored run-local data under
  `.scratch/orchestrate/<slug>/`, while historical tracked `.claude/plans/**` remain.
  - verify: inspection — review orchestrator state instructions and `git diff --name-status` for deletions.
- AC1.5: Autonomous implementation retains isolated worktrees, strict TDD, aggregate
  Taskfile gates, strict acceptance tracing, panel review, retained failed attempts, and
  human-only merge authority.
  - verify: inspection — review `plan-orchestrate` and `tdd-worker` contracts.
- AC1.6: Every plan has a non-empty, non-placeholder `## Human decisions` section. An
  unchecked checklist decision is accepted only while status is `draft`; `proposed` and all
  later statuses require either `None — <rationale>` or fully checked decisions that record
  `— Decision: <decision>`.
  - verify: `.claude/skills/to-acceptance-plan/scripts/check-acceptance-plan-test.sh`

## Out of scope

| Item | Defer-to | Decision |
|---|---|---|
| Runtime product behavior or API changes | future approved plans | This proposal changes repository workflow only. |
| Deleting or migrating historical `.claude/plans/**` | never in this proposal | Preserve historical records. |
| Importing third-party workflow scripts/hooks | not planned | External practices are influences, not dependencies. |

## Definition of done

1. The bundled checker and its committed fixture tests pass; fixtures prove the human-decision
   status gate plus rejection of missing sections/categories, invalid status/delivery,
   placeholders, bare `None`, and missing or invalid Combined task-count/rationale metadata.
2. `task docs` passes.
3. ADR/index, living process, acceptance guide/index, skills, worker contract, and PR template agree.
4. No historical `.claude/plans/**` file is deleted.
5. This proposal receives human review as the process/interface change itself.

## Deferred decisions and known risks

- None — the material process interfaces and human decisions are recorded above; only
  non-material implementation detail may appear here, and any newly discovered material
  choice returns the plan to draft and belongs in `## Human decisions`.
