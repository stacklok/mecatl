# Acceptance-proof resolution efficiency — acceptance plan

**Contract:** human-reviewed/v2
**Work classification:** Bounded — reduces CI work while preserving the repository-local acceptance-proof resolution contract; no public or durable architecture boundary changes.
**Decision record:** None — this is a repository CI policy and resolver implementation decision, not a durable system architecture decision.
**Phase:** CI validation efficiency
**Status:** in-progress, 2026-09-24. User-authorized direct amendment: retain task-proof registration checks and defer the Vitest batch index.
**Delivery:** Split. Two proof-resolution behaviors change and require separate contract review before implementation.
**Expected tasks:** 1
**Issue:** None — user-authorized bounded CI improvement.
**Plan PR:** [#1831](https://github.com/stacklok/mecatl/pull/1831)
**Approved baseline:** absent until this plan PR merges

Acceptance-plan CI must retain fail-closed proof-reference integrity without re-running unrelated implementation workloads for every plan-only pull request. Task proofs establish that their allowlisted verification target remains registered; their execution remains the responsibility of the target's existing relevant CI job. The change stays inside the existing `Doc validation` runner and does not add jobs or runners.

## Human decisions

- [x] Task proofs validate an allowlisted registered target without executing it during acceptance tracing. — Decision: user confirmed implementation/test success is covered by relevant CI jobs and does not warrant duplicate execution in plan-only CI.
- [x] Vitest batch indexing is deferred. — Decision: its source-discovery, cache-lifecycle, and fixture surface is disproportionate to this bounded change; evaluate batching in ac-trace if later measurement warrants it.

## Interface contract

- **gRPC / protobuf:** None — CI proof resolution does not alter wire contracts.
- **Exported Go APIs / interfaces:** None — the work changes repository scripts and Task recipes only.
- **Tool schemas:** None — no model-facing tool changes.
- **CLI / config:** `task ac-trace`, `task ac-trace-strict`, and `task ac-trace-matrix` retain their names and behavior; no new index command, environment variable, or temporary artifact is introduced.
- **Events / persistence:** None — no durable or temporary index state is added.
- **Security / authority:** Task-proof tokens remain limited to the existing allowlist and must fail closed if the named Task target is absent; existing Vitest path, compiler, and title-resolution boundaries remain unchanged.
- **Compatibility / migration:** Existing proof syntax and Vitest/Playwright resolution remain unchanged. Task proofs change from executing their target to validating the target's registration; source-relevant CI remains the execution authority.

## In scope — 1 scenario, in implementation order

### Scenario 1 — Task proofs resolve without re-running their workloads

The current resolver executes allowlisted `api:check`, `test:engine-standalone`, and `site:build` targets while tracing every acceptance-plan corpus. Those workloads are owned by the API compatibility, engine-standalone, and user-docs CI families respectively; plan-only changes deliberately need not rerun them. The resolver must instead prove each allowlisted target remains registered through Task listing, without executing a target or dependency, while retaining rejection of any unapproved token and the repository’s offline test discipline in [AGENTS.md](../../AGENTS.md). See [the acceptance-proof contract](README.md#the-verification-contract) and [`resolve-task-proof.mjs`](../../scripts/resolve-task-proof.mjs).

**Acceptance:**
- AC1.1: Every current supported task proof (`api:check`, `test:engine-standalone`, and namespaced `site:build`) resolves only when its exact target remains both allowlisted and registered through the Taskfile include graph.
  - verify: `TestResolveTaskProof_ResolvesRegisteredAllowlistedTargets`
- AC1.2: Resolving a supported task proof does not execute the proof target or its dependencies.
  - verify: `TestResolveTaskProof_DoesNotExecuteRegisteredTarget`
- AC1.3: A missing Task target or unsupported proof fails the trace rather than being treated as resolved.
  - verify: `TestResolveTaskProof_RejectsUnregisteredOrUnsupportedTarget`

## Out of scope

| Item | Defer-to | Decision |
|---|---|---|
| Executing implementation tests from acceptance tracing | Existing source-relevant CI jobs | Task proofs become structural registration checks; their owner jobs remain execution authority. |
| Changing ac-trace's command protocol or upstream implementation | Later upstream work | This plan changes only repository-provided resolver commands and Task orchestration. |
| Vitest batch indexing or resolver protocol changes | ac-trace performance work | Defer until measurement justifies a dedicated design. |
| Adding a runner, matrix entry, or asynchronous validation workflow | None | The implementation must reduce work inside the existing Doc validation runner. |
| Changing acceptance-plan status semantics or proof-token syntax | None | Existing contracts and proof syntax remain compatible. |

## Definition of done

1. Focused task-proof resolver tests, `task lint:actions`, and `task test:docs-only-classifier` pass.
2. `task ac-trace-strict` preserves fail-closed resolution for existing landed proofs without executing task-proof workloads.
3. The implementation runs inside the existing Doc validation job with no additional job or runner.
4. The implementation PR links this approved amendment baseline and reports interface conformance.
5. `/panel-review` reports no ship blockers or unwaived reviewer failures.

## Deferred decisions and known risks

- Vitest resolver batching is deferred to a dedicated ac-trace performance decision if later CI measurement still requires it.
