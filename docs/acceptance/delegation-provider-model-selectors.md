# Delegation provider/model selectors — acceptance plan

**Contract:** human-reviewed/v2
**Work classification:** Architectural — introduces a durable delegation-selection grammar, reserves a virtual provider namespace, expands tool/protobuf/event contracts, and changes the model-visible routing policy across delegation families.
**Decision record:** [ADR 0369](../adr/0369-delegation-provider-model-selectors.md)
**Phase:** explicit delegation selection
**Status:** proposed, 2026-09-26. Directing-human decisions recorded in this conversation.
**Delivery:** Split. The cross-provider child factory, model-facing schemas, discovery result, and durable event projections need interface review before implementation.
**Expected tasks:** deferred to orchestration

This plan makes provider/model selection explicit and uniform for delegated work. The default remains to omit a selector, preserving operator-controlled automatic routing and the inherited child model. An agent can discover an exact provider/model pair, or an operator-defined router category, before selecting a child intentionally.

The owning current-behavior pages are [providers](../architecture/providers.md) and [subagents and teams](../architecture/subagents-and-teams.md). Public model-picking behavior remains unchanged in this plan: `ListModels` and root-session selection do not publish router categories.

## Human decisions

- [x] Use a two-field delegation selector — Decision: `provider` and `model` are optional siblings on Subagent, Parallel, and each Team member; omit both by default, reject provider without model, and retain a bare model as a same-parent-provider override.
- [x] Reserve a delegation-only router provider — Decision: `provider: "model-router"` plus an exact category name selects an enabled operator router category for the parent provider; it bypasses classification and fails rather than silently inheriting when unavailable.
- [x] Keep router categories out of root-session inventory — Decision: add virtual router-category rows only to `DiscoverModels`; `ListModels`, `/models`, and `CreateSession` remain inference-provider-only.
- [x] Expose bounded category descriptions — Decision: `DiscoverModels` returns `description` for every row when available; router rows use the operator category description and search includes it.
- [x] Make omission the model-visible default — Decision: the factory-built discovery posture and tool specification tell agents to omit selectors unless the user requests a selection or a concrete capability need justifies it.
- [x] Render an explicit router request distinctly — Decision: Mecatui displays `selected: model-router/{category} → {provider}/{model}` on Subagent cards, Parallel branches, and Team rosters; automatic classifier selections retain `routed:`.
- [x] Set named-agent definitions aside — Decision: this plan changes def-less runtime delegation surfaces only; a call that names `agent` rejects `provider` (including `model-router`) while retaining existing bare-model behavior, and definition-frontmatter grammar/discovery are a separate follow-up.

## Interface contract

- **gRPC / protobuf:** Do not change `ListModels`, `ModelInfo`, or root-session selection. Add concrete `provider` plus `explicit_router_category` fields to delegation event projections: `Subagent.provider = 20`, `Subagent.explicit_router_category = 21`; `TeamMemberSpec.provider = 10`, `TeamMemberSpec.explicit_router_category = 11`; `Parallel.provider = 27`, `Parallel.explicit_router_category = 28`. They contain the provider that actually ran and the requested category only for an explicit router selection; generated contracts preserve existing fields and JSON.
- **Exported Go APIs / interfaces:** Extend the pre-v1 engine selection seams for Subagent, Parallel, and Team from model-only factory input to a provider/model selector value. Composition resolves a direct explicit pair through a fresh provider-specific child-engine factory; `model-router` resolves only through the existing parent-provider category mapping. No `port.LLMRequest` widening or registry exposure reaches `engine`.
- **Tool schemas:** Add optional `provider` and `model` to Subagent and Parallel, and optional `provider` and `model` to each Team member. `provider` without `model` is invalid. Bare non-empty `model` keeps same-provider semantics. A named Subagent call rejects `provider` (including `model-router`) but retains its existing bare-model behavior. A returned direct `(provider_id, model_id)` maps to `{provider, model}`. `provider:"model-router"` accepts only exact discovered category names. `DiscoverModels` adds bounded optional `description` (at most 512 UTF-8 bytes after control-byte rejection) to rows. Its one canonical projection includes enabled router-category rows under `provider_id:"model-router"`; descriptions participate in literal search, ordering, cursor digest, pagination, output bounds, and stale-cursor detection. The initial provider facet includes `model-router` only when those virtual rows are present. No `alias` field, separate discovery tool, provider probe, selection side effect, or `ListModels` row is added.
- **CLI / config:** None — existing operator-only `models.router.categories` configuration, taxonomy enablement, and kill switch remain authoritative. No new flag, project authority, or settings key is introduced.
- **Events / persistence:** Add the resolved concrete provider and optional `explicit_router_category` to `session.SubagentPayload`, `session.ParallelPayload`, and `session.TeamMemberSpec`, mapped only through the server event mapper. A router-category explicit selection sets that category field with the actual provider/model, makes no classifier call, and leaves `routed_category`, `routed_model`, `routing_reason`, and `routing_decision` absent. Mecatui renders that exact provenance as `selected: model-router/{category} → {provider}/{model}` on every delegation surface, while classifier-originated routing retains the existing `routed:` treatment. Existing durable events retain additive fields; session snapshots retain no requested selector or router category.
- **Security / authority:** `model-router` is a reserved delegation selector, never a registry provider or credential boundary; registry/configuration admission rejects a built-in or operator-defined provider of that exact ID. Router rows reveal only bounded operator category names/descriptions already supplied to the configured router; they omit category targets, aliases, endpoints, credentials, unavailable providers, errors, and inventory topology. Project configuration cannot create router rows. Ordinary provider/model pairs are validated by composition; unknown/unavailable direct providers and unknown router categories fail before a child starts. Fork/resume remain provider-safe and reject selectors.
- **Compatibility / migration:** Additive tool/protobuf fields preserve older clients. Existing bare Subagent `model` calls remain same-provider. Parallel and Team gain optional selectors without changing omitted behavior. `DiscoverModels` adds optional descriptions and router rows only when enabled; clients that ignore descriptions retain current inference discovery. The stable discovery posture is revised in place; no `DiscoverModelsV2` or `ListProviders` is introduced.

## In scope — 4 scenarios, in implementation order

### Scenario 1 — discover direct and router delegation choices

`DiscoverModels` remains the agent-facing bounded projection of the resolved inventory under [ADR 0369](../adr/0369-delegation-provider-model-selectors.md) and the [provider architecture](../architecture/providers.md). Router categories are operator policy, not a root-session provider.

**Acceptance:**
- AC1.1: an enabled router taxonomy contributes bounded rows with `provider_id:"model-router"`, exact category names, and their configured descriptions to unfiltered `DiscoverModels`; an empty or disabled taxonomy contributes none, and provider registration/configuration rejects the reserved `model-router` ID.
  - verify: `TestADR_0369_Scenario1_RouterDiscovery`
- AC1.2: literal search and exact filters cover router category names and descriptions; descriptions participate in canonical order, cursor digests, pagination, output bounds, and stale-cursor restart; direct provider rows retain their safe metadata and no discovery request probes, refreshes, selects, or switches a model.
  - verify: `TestADR_0369_Scenario1_DiscoveryBounds`
- AC1.3: `ListModels`, `/models`, and `CreateSession` do not publish or accept `model-router` as a provider.
  - verify: `TestADR_0369_Scenario1_RootSelectionUnaffected`

### Scenario 2 — Subagent resolves explicit selectors safely

The existing precedence ladder from [ADR 0031](../adr/0031-subagent-model-router.md) retains omitted-selector automatic routing. Explicit selector intent takes precedence over the classifier.

**Acceptance:**
- AC2.1: a bare `model` retains same-parent-provider behavior; a concrete `{provider, model}` pair mints a fresh child engine with that provider/model's dependencies; omitting both leaves the selector unset and permits normal automatic routing.
  - verify: `TestADR_0369_Scenario2_SubagentDirectSelectors`
- AC2.2: `{provider:"model-router", model:{discovered-category}}` resolves the parent-provider category target with no classifier call; it sets only explicit-selector provenance plus the resolved concrete provider/model, never classifier-routing evidence, and runs the child on that target.
  - verify: `TestADR_0369_Scenario2_SubagentRouterSelector`
- AC2.3: provider without model, a named Subagent call with provider, unknown/unavailable provider, absent/disabled/unknown/unresolvable router category, fork with a selector, and resume with a selector each return bounded tool errors and start no child.
  - verify: `TestADR_0369_Scenario2_InvalidSelectors`

### Scenario 3 — Parallel and Team use the same selector language

[ADR 0034](../adr/0034-team-parallel-model-routing.md) already routes every delegation family. This scenario supplies an explicit selector to those families without changing their existing lifetime rules.

**Acceptance:**
- AC3.1: one explicit Parallel selector applies to every branch; the judge remains on the parent model, and omitted fields retain per-branch automatic routing.
  - verify: `TestADR_0369_Scenario3_ParallelSelector`
- AC3.2: each Team member independently accepts an explicit selector; the member resolves once at `AddMember` and retains its engine across rounds, while omitted fields retain current routing/default behavior.
  - verify: `TestADR_0369_Scenario3_TeamMemberSelector`
- AC3.3: all three delegation families emit the actual concrete provider/model, retain classifier category/model evidence only for classifier-originated routing, and carry an explicit router category separately without exposing task content or configuration secrets; Mecatui renders the explicit form as `selected: model-router/{category} → {provider}/{model}`.
  - verify: `TestADR_0369_Scenario3_DelegationEvidence`

### Scenario 4 — the model is guided to preserve operator routing by default

The existing discovery posture is appended to the factory-built system role when `DiscoverModels` is present (`internal/app/build.go`), as required by [ADR 0369](../adr/0369-delegation-provider-model-selectors.md). It must teach the workflow without embedding current inventory.

**Acceptance:**
- AC4.1: the real factory-built prompt tells the model to omit delegation provider/model selectors by default, use `DiscoverModels` before a justified explicit choice, and explains that `model-router` rows are delegation categories rather than session-selection targets.
  - verify: `TestADR_0369_Scenario4_ModelVisibleWorkflow`
- AC4.2: the `DiscoverModels` specification accurately describes router-category rows, descriptions, search, bounded results, and the no-probe/no-selection boundary; it does not embed configured categories in static prompt text.
  - verify: `TestADR_0369_Scenario4_ToolSpecification`

## Out of scope

| Item | Defer-to | Decision |
|---|---|---|
| Router rows in `ListModels`, `/models`, or `CreateSession` | Separate root-session model-selection proposal | A category has no root-session task or inference endpoint. |
| Named-agent definition provider/model/router grammar | Focused named-agent contract | The runtime selection vocabulary is intentionally separated from definition format. |
| Per-Parallel-branch selectors | A future branch-task object schema | This plan applies one selector to all branches and preserves `tasks: []string`. |
| New router categories, target aliases, cross-provider automatic routing, or classifier behavior | Existing router configuration/ADR follow-up | This plan makes existing categories explicit; it does not change operator routing policy. |

## Definition of done

1. Focused engine, composition, server mapper, and client-contract tests pass; `task generate`, `task lint`, `task test:race`, `task api:check`, `task docs`, and `task site:build` pass on the implementation candidate.
2. `task ac-trace-strict` resolves every named proof when the plan becomes `landed`.
3. `go run ./cmd/mecademo` still demonstrates a tool call, permission ask/approval, and result.
4. The implementation PR links the merged Plan / Interface PR and reports schema, event, and prompt-factory conformance.
5. `/panel-review` reports no ship blockers or unwaived reviewer failures.

## Deferred decisions and known risks

- A direct provider/model child may be unavailable despite successful discovery; provider access and live listing remain independent, so the tool call must fail honestly.
- Category descriptions are operator-authored model-visible text and must be bounded and rendered through the existing safe display paths.
