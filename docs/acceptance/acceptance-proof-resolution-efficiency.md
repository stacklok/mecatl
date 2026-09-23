# Acceptance-proof resolution efficiency — acceptance plan

**Contract:** human-reviewed/v2
**Work classification:** Bounded — reduces CI work while preserving the repository-local acceptance-proof resolution contract; no public or durable architecture boundary changes.
**Decision record:** None — this is a repository CI policy and resolver implementation decision, not a durable system architecture decision.
**Phase:** CI validation efficiency
**Status:** proposed, 2026-09-23. User-authorized Split Plan / Interface candidate.
**Delivery:** Split. Two proof-resolution behaviors change and require separate contract review before implementation.
**Expected tasks:** 2
**Issue:** None — user-authorized bounded CI improvement.
**Plan PR:** [#1831](https://github.com/stacklok/mecatl/pull/1831)
**Approved baseline:** absent until this plan PR merges

Acceptance-plan CI must retain fail-closed proof-reference integrity without re-running unrelated implementation workloads for every plan-only pull request. Task proofs establish that their allowlisted verification target remains registered; their execution remains the responsibility of the target's existing relevant CI job.

Vitest proof references remain exact and AST-backed, but their titles are indexed once per trace invocation rather than reparsed in a fresh Node and TypeScript process for every reference. The change stays inside the existing `Doc validation` runner and does not add jobs or runners.

## Human decisions

- [x] Task proofs validate an allowlisted registered target without executing it during acceptance tracing. — Decision: user confirmed implementation/test success is covered by relevant CI jobs and does not warrant duplicate execution in plan-only CI.
- [x] Vitest proof references retain exact AST-backed title matching through a per-invocation batch index. — Decision: preserve proof-reference integrity while reducing repeated resolver startup and parsing.

## Interface contract

- **gRPC / protobuf:** None — CI proof resolution does not alter wire contracts.
- **Exported Go APIs / interfaces:** None — the work changes repository scripts and Task recipes only.
- **Tool schemas:** None — no model-facing tool changes.
- **CLI / config:** `task ac-trace`, `task ac-trace-strict`, and `task ac-trace-matrix` retain their names and strict/report-only behavior; each depends on one private ignored index at `.scratch/ac-trace/vitest-title-index.json`, sets `ACTRACE_VITEST_INDEX` for resolver subprocesses, and then invokes `actrace`.
- **Events / persistence:** None — the temporary index is invocation-local CI/workspace state and is not a durable artifact.
- **Security / authority:** Task-proof tokens remain limited to the existing allowlist and must fail closed if the named Task target is absent. The index key is `(workspace, repository-relative test path, title)`; Vitest paths remain constrained to the SDK or Studio workspaces, retain their current compiler/TSX selection, and malformed, missing, or duplicate titles fail closed.
- **Compatibility / migration:** Existing `api:`, `test:`, `site:`, `playwright:`, and `vitest:` proof syntax remains valid. Task proofs change from executing their target to validating the target's registration; source-relevant CI remains the execution authority.

## In scope — 2 scenarios, in implementation order

### Scenario 1 — Task proofs resolve without re-running their workloads

The current resolver executes allowlisted `api:check`, `test:engine-standalone`, and `site:build` targets while tracing every acceptance-plan corpus. Those workloads are owned by the API compatibility, engine-standalone, and user-docs CI families respectively; plan-only changes deliberately need not rerun them. The resolver must instead prove each allowlisted target remains registered through Task listing, without executing a target or dependency, while retaining rejection of any unapproved token and the repository’s offline test discipline in [AGENTS.md](../../AGENTS.md). See [the acceptance-proof contract](README.md#the-verification-contract) and [`resolve-task-proof.mjs`](../../scripts/resolve-task-proof.mjs).

**Acceptance:**
- AC1.1: Every current supported task proof (`api:check`, `test:engine-standalone`, and namespaced `site:build`) resolves only when its exact target remains both allowlisted and registered through the Taskfile include graph.
  - verify: `TestResolveTaskProof_ResolvesRegisteredAllowlistedTargets`
- AC1.2: Resolving a supported task proof does not execute the proof target or its dependencies.
  - verify: `TestResolveTaskProof_DoesNotExecuteRegisteredTarget`
- AC1.3: A missing Task target or unsupported proof fails the trace rather than being treated as resolved.
  - verify: `TestResolveTaskProof_RejectsUnregisteredOrUnsupportedTarget`

### Scenario 2 — Vitest proofs share a batch-generated exact title index

The current resolver starts a fresh Node process, imports TypeScript, and reparses a test file for each Vitest proof. Before each trace entry point, a repository script must enumerate only Git-tracked SDK and Studio `*.test.ts`, `*.test.tsx`, `*.e2e.test.ts`, and `*.e2e.test.tsx` source paths, reject symlink/path escapes, and parse each current source once with its existing workspace compiler and TSX rules before writing the private index. Individual resolver invocations use `ACTRACE_VITEST_INDEX` and the `(workspace, repository-relative test path, title)` key while retaining the offline test discipline in [AGENTS.md](../../AGENTS.md). See [`resolve-vitest-proof.mjs`](../../scripts/resolve-vitest-proof.mjs) and [the acceptance-proof contract](README.md#the-verification-contract).

**Acceptance:**
- AC2.1: Every `ac-trace` entry point builds one private index from Git-tracked SDK and Studio Vitest source paths before resolving Vitest proof tokens, parsing each discovered source at most once and excluding dependencies, generated paths, and symlink escapes.
  - verify: `TestVitestProofIndex_Scenario2_IndexesTrackedSourcesOnce`
- AC2.2: Every valid Vitest proof resolves only when its `(workspace, repository-relative test path, title)` key identifies exactly one `test` or `it` title under the existing AST semantics.
  - verify: `TestVitestProofIndex_Scenario2_PreservesPathScopedExactTitleResolution`
- AC2.3: Identical titles in distinct files or workspaces remain independently resolvable, and SDK/Studio compiler selection plus TSX parsing remain unchanged.
  - verify: `TestVitestProofIndex_Scenario2_PreservesWorkspaceAndTSXSemantics`
- AC2.4: Missing indexes, malformed tokens, out-of-workspace paths, missing files, and duplicate or absent path-scoped titles fail closed.
  - verify: `TestVitestProofIndex_Scenario2_FailsClosed`
- AC2.5: Playwright proof resolution retains its existing resolver and is outside the Vitest index scope.
  - verify: `TestVitestProofIndex_Scenario2_DoesNotChangePlaywrightResolution`
- AC2.6: The batch index and resolver run within the existing Doc validation job and do not introduce a new CI job or runner.
  - verify: inspection — `.github/workflows/ci.yml` retains a single `docs` job and no new workflow job consumes the index.

## Out of scope

| Item | Defer-to | Decision |
|---|---|---|
| Executing implementation tests from acceptance tracing | Existing source-relevant CI jobs | Task proofs become structural registration checks; their owner jobs remain execution authority. |
| Changing ac-trace's command protocol or upstream implementation | Later upstream work | This plan changes only repository-provided resolver commands and Task orchestration. |
| Changing Playwright proof resolution | Later resolver optimization | This plan batches only `vitest:` proofs; Playwright retains its existing resolver. |
| Adding a runner, matrix entry, or asynchronous validation workflow | None | The implementation must reduce work inside the existing Doc validation runner. |
| Changing acceptance-plan status semantics or proof-token syntax | None | Existing contracts and proof syntax remain compatible. |

## Definition of done

1. Focused resolver/index tests, `task lint:actions`, and `task test:docs-only-classifier` pass.
2. `task ac-trace-strict` preserves fail-closed resolution for existing landed proofs without executing task-proof workloads.
3. The implementation runs inside the existing Doc validation job with no additional job or runner.
4. The implementation PR links this approved Plan / Interface PR and reports interface conformance.
5. `/panel-review` reports no ship blockers or unwaived reviewer failures.

## Deferred decisions and known risks

- The exact private index serialization format is an implementation detail, provided it is regenerated for every trace invocation and all lookup failures remain fail closed.
