# Provider-aware model aliases and delegation selectors — acceptance plan

**Contract:** human-reviewed/v2
**Work classification:** Architectural — changes the durable alias and project-configuration policy, introduces cross-provider model targets across existing consumers, reserves a virtual provider namespace, expands tool/protobuf/event contracts, and changes model-visible delegation routing.
**Decision record:** [ADR 0369](../adr/0369-delegation-provider-model-selectors.md)
**Phase:** provider-aware aliases and explicit delegation selection
**Status:** landed in this implementation candidate, 2026-09-27; authoritative on merge. Directing-human decisions and direct-amendment authorization recorded in this conversation.
**Delivery:** Split, with a directing-human-authorized direct contract amendment included in the sole Implementation PR #2019. Alias resolution, configuration compatibility, the cross-provider child factory, model-facing schemas, discovery results, and durable event projections remain one implementation candidate.
**Expected tasks:** deferred to orchestration

This plan first makes a model alias an optional provider/model target everywhere aliases are consumed. Scalar aliases retain contextual same-provider behavior; an operator YAML object binds an atomic provider/model pair, and a companion CLI flag supplies the provider without parsing opaque model IDs. Project-level model policy is removed: a repository cannot influence model or provider selection even when trusted.

It then makes provider/model selection explicit and uniform for delegated work. The default remains to omit a selector, preserving operator-controlled automatic routing and the inherited child model. An agent can discover an exact provider/model pair, or an operator-defined router category, before selecting a child intentionally.

The owning current-behavior pages are [providers](../architecture/providers.md) and [subagents and teams](../architecture/subagents-and-teams.md). They remain unchanged until implementation. Public root-session model picking remains unchanged in this plan: `ListModels` and root-session selection do not publish router categories.

## Human decisions

- [x] Make aliases provider-aware everywhere — Decision: an alias is either the existing scalar model selector, resolved on the consumer's contextual provider, or a strict operator-YAML `{provider, model}` object resolved as one atomic target by every alias consumer. A separately supplied provider must match an alias target's provider or resolution fails; no consumer may discard or reinterpret the alias provider.
- [x] Preserve provider-bound session replay — Decision: a session's persisted provider remains fixed. A `plan` slot resolving to another provider is incompatible and follows the existing fail-soft slot posture: keep plan permission mode but run on the session's persisted provider and ordinary non-plan model, emit a bounded warning, make no request to the alias target, and never rebase the alias model onto the session provider.
- [x] Preserve consumer failure postures — Decision: pair aliases naming an unknown or unavailable provider fail during Build. Explicit delegation target failures start no child. Existing auxiliary-slot and automatic-router fail-soft behavior remains unchanged for runtime resolution failures; named-agent definition fallback remains unchanged. No alias resolution probes live model inventory or rejects an opaque model ID merely because inventory cannot prove it.
- [x] Preserve named specialists — Decision: existing read-only Subagent `agent` plus bare `model` behavior continues to rebuild the named specialist without losing its prompt, catalog, limits, permission/filesystem posture, skills, or MCP configuration. Named Team members gain the same model-only override behavior. A call-level provider-bearing selector, including a pair alias, is rejected for any named specialist; pair aliases declared through the definition's own model field remain valid. Team selectors validate atomically before any member is added, and Parallel validates before fan-out.
- [x] Preserve CLI alias compatibility without parsing model IDs — Decision: retain repeatable `--model-alias name=model-id` and add repeatable `--model-alias-provider name=provider-id`; a matching provider entry upgrades that CLI alias to a pair, while a provider entry without a matching model is a startup error. No delimiter is reserved inside opaque model IDs.
- [x] Remove repository model policy — Decision: ignore the entire project-tier `models:` block with a bounded warning regardless of project trust; it must not affect defaults, aliases, slots, routing, provider choice, or any other model behavior and must not block startup.
- [x] Retire the project allowlist fail-softly — Decision: operator `models.allowlist` remains accepted for compatibility but has no effect and emits a bounded deprecation/no-effect warning; removal is deferred to a later compatibility cleanup.
- [x] Use a two-field delegation selector — Decision: `provider` and `model` are optional siblings on Subagent, Parallel, and each Team member; omit both by default, reject provider without model, and retain a bare literal model as a same-parent-provider override. A configured provider-aware alias supplied as `model` carries its atomic pair.
- [x] Reserve a delegation-only router provider — Decision: `provider: "model-router"` plus an exact category name selects an enabled operator router category. A category target resolved through a provider-aware alias carries that pair; scalar targets retain parent-provider behavior. Explicit selection bypasses classification and fails rather than silently inheriting when unavailable.
- [x] Keep router categories out of root-session inventory — Decision: add virtual router-category rows only to `DiscoverModels`; `ListModels`, `/models`, and `CreateSession` remain inference-provider-only.
- [x] Expose bounded category descriptions — Decision: `DiscoverModels` returns `description` for every row when available; router rows use the operator category description and search includes it.
- [x] Make omission the model-visible default — Decision: the factory-built discovery posture and tool specification tell agents to omit selectors unless the user requests a selection or a concrete capability need justifies it.
- [x] Render an explicit router request distinctly — Decision: Mecatui displays `selected: model-router/{category} → {provider}/{model}` on Subagent cards, Parallel branches, and Team rosters; automatic classifier selections retain `routed:`.
- [x] Set named-agent definition grammar aside — Decision: this plan does not add provider/model/router fields to definition frontmatter. Existing definition fields consume provider-aware aliases under the universal alias rule; runtime named-specialist restrictions are defined above.

## Interface contract

- **gRPC / protobuf:** Do not change `ListModels`, `ModelInfo`, or root-session selection. Add concrete `provider` plus `explicit_router_category` fields to delegation event projections: `Subagent.provider = 20`, `Subagent.explicit_router_category = 21`; `TeamMemberSpec.provider = 10`, `TeamMemberSpec.explicit_router_category = 11`; `Parallel.provider = 27`, `Parallel.explicit_router_category = 28`. They contain the provider that actually ran and the requested category only for an explicit router selection; generated contracts preserve existing fields and JSON.
- **Exported Go APIs / interfaces:** Replace composition's model-only alias value with a provider/model target that can distinguish contextual-provider scalars from explicit pairs, and make every alias consumer preserve that target. Extend the pre-v1 engine selection seams for Subagent, Parallel, and Team from model-only factory input to the same provider/model selector value. Composition resolves direct and aliased pairs through fresh provider-specific engine/dependency factories; `model-router` resolves category targets through the universal alias resolver. The session engine factory keeps the persisted provider fixed and rejects a cross-provider plan target into the defined fail-soft fallback. No `port.LLMRequest` widening or registry exposure reaches `engine`. These exported `engine/agent` signature and `engine/session` payload changes require compatibility classification, `task api:update`, updated API snapshots, and a classified `engine/CHANGELOG.md` entry.
- **Tool schemas:** Add optional `provider` and `model` to Subagent and Parallel, and optional `provider` and `model` to each Team member. `provider` without `model` is invalid. A bare literal model keeps same-parent-provider semantics; a configured provider-aware alias carries its pair. An ordinary explicit provider plus a pair alias must match exactly. A named Subagent or Team member rejects a call-level provider-bearing selector, including `model-router` or a pair alias, but accepts the established model-only specialist rebuild without losing specialist scope. Team validates every selector before adding any member; Parallel validates its shared selector before fan-out. A returned direct `(provider_id, model_id)` maps to `{provider, model}`. `provider:"model-router"` accepts only exact discovered category names. `DiscoverModels` adds bounded optional `description` (at most 512 UTF-8 bytes after control-byte rejection) to rows. Its one canonical projection includes enabled router-category rows under `provider_id:"model-router"`; descriptions participate in literal search, ordering, cursor digest, pagination, output bounds, and stale-cursor detection. The initial provider facet includes `model-router` only when those virtual rows are present. No alias field, separate discovery tool, provider probe, selection side effect, or `ListModels` row is added.
- **CLI / config:** `models.aliases.NAME` accepts either the existing scalar model selector or exactly `{provider: PROVIDER_ID, model: MODEL_ID}` with both non-empty fields and no unknown keys. Alias lookup is single-hop; the target model is opaque and is not recursively alias-resolved. The object is operator-tier only and `model-router` is not a valid concrete target. Retain repeatable `--model-alias name=model-id`; add repeatable `--model-alias-provider name=provider-id` to mecated, embedded mecatui, mecak8s, and their shared flag plumbing. A CLI model entry replaces the complete lower-tier target for its name and never inherits a YAML provider; a provider entry pairs only with a same-tier CLI model entry, independent of argument order, or startup fails. CLI remains highest precedence per alias name. After syntactic decoding of a project settings document, its entire `models:` node is opaque ignored content: nested keys and values are not model-schema-validated, resolved, canonicalized, merged, or probed, and one source-bounded value-free warning is emitted regardless of trust. Operator `models.allowlist` remains parseable but inert and warns. No provider/model delimiter grammar is introduced.
- **Events / persistence:** Add the resolved concrete provider and optional `explicit_router_category` to `session.SubagentPayload`, `session.ParallelPayload`, and `session.TeamMemberSpec`, mapped only through the server event mapper. A router-category explicit selection sets that category field with the actual provider/model, makes no classifier call, and leaves `routed_category`, `routed_model`, `routing_reason`, and `routing_decision` absent. Mecatui renders that exact provenance as `selected: model-router/{category} → {provider}/{model}` on every delegation surface, while classifier-originated selections retain the existing `routed:` treatment. Existing durable events retain additive fields; session snapshots retain no requested delegation selector or router category. Existing persisted session provider/model selection is not rewritten merely because an alias definition changes.
- **Security / authority:** Model aliases and every effective `models:` binding are operator-controlled. Project configuration cannot influence model/provider selection under trust, posture, or allowlist; ignored project content is not resolved or probed. `model-router` is a reserved delegation selector, never a registry provider, credential boundary, or alias target; registry/configuration admission rejects a built-in or operator-defined provider of that exact ID. Router rows reveal only bounded operator category names/descriptions already supplied to the configured router; they omit category targets, aliases, endpoints, credentials, unavailable providers, errors, and inventory topology. Ordinary provider/model pairs are validated by composition; conflicting alias/provider intent, unknown/unavailable direct providers, and unknown or unresolvable router categories fail before a dependent engine or child starts. Fork/resume remain provider-safe and reject selectors.
- **Compatibility / migration:** Existing scalar YAML aliases and `--model-alias name=model-id` retain contextual same-provider behavior. Provider-aware YAML objects and the companion CLI flag are additive. Project-level `models:` bindings intentionally stop taking effect; startup continues with one bounded warning rather than an error. Operator `models.allowlist` remains accepted but becomes inert and warns so existing settings do not block launch. Existing bare Subagent `model` literals remain same-provider; a configured pair alias now carries its provider universally. Parallel and Team gain optional selectors without changing omitted behavior. `DiscoverModels` adds optional descriptions and router rows only when enabled; clients that ignore descriptions retain current inference discovery. The stable discovery posture is revised in place; no `DiscoverModelsV2` or `ListProviders` is introduced.

## In scope — 5 scenarios, in implementation order

### Scenario 1 — aliases resolve to stable provider/model targets

Alias resolution remains composition-owned under [ADR 0369](../adr/0369-delegation-provider-model-selectors.md) and the existing [provider architecture](../architecture/providers.md). The same target grammar must reach every consumer without exposing the provider registry to engine core.

**Acceptance:**
- AC1.1: operator YAML accepts existing scalar aliases and strict `{provider, model}` alias objects; alias lookup is single-hop; CLI model/provider entries form a same-tier, order-independent pair across mecated, embedded mecatui, and mecak8s; a CLI model replaces the whole lower-tier target; malformed objects, missing or empty fields, reserved or unknown/unavailable providers, unmatched CLI provider entries, and conflicting provider intent fail with bounded startup errors without probing a provider or validating opaque model IDs against live inventory.
  - verify: `TestADR_0369_Scenario1_AliasGrammar`
- AC1.2: deployment defaults, auxiliary call slots, the global subagent default, agent definitions, automatic router categories, and explicit delegation selectors all preserve the same resolved target; pair-aware consumers re-derive provider/model-dependent dependencies through their real factories, scalar aliases retain contextual-provider behavior, existing per-consumer fail-hard/fail-soft postures remain intact, and no consumer mutates a parent engine or silently drops an explicit alias provider.
  - verify: `TestADR_0369_Scenario1_UniversalAliasTargets`
- AC1.3: a session persists one provider; a same-provider plan target rebuilds on its resolved model, while a cross-provider plan target emits a bounded warning and runs the plan-mode turn on the persisted provider and ordinary non-plan model without requesting the alias target, rebasing its model, changing permission mode, or changing the reported actual provider/model.
  - verify: `TestADR_0369_Scenario1_ProviderBoundPlanFallback`
- AC1.4: after whole-document syntax decoding, every project-tier `models:` node, including malformed or unknown nested model content, is ignored before model-schema validation with one source-bounded value-free warning regardless of trust and cannot affect provider/model behavior or block startup; a populated operator `models.allowlist` remains accepted, has no effect, and warns.
  - verify: `TestADR_0369_Scenario1_ProjectModelsDisabled`

### Scenario 2 — discover direct and router delegation choices

`DiscoverModels` remains the agent-facing bounded projection of the resolved inventory under [ADR 0369](../adr/0369-delegation-provider-model-selectors.md) and the [provider architecture](../architecture/providers.md). Router categories are operator policy, not a root-session provider.

**Acceptance:**
- AC2.1: an enabled router taxonomy contributes bounded rows with `provider_id:"model-router"`, exact category names, and their configured descriptions to unfiltered `DiscoverModels`; an empty or disabled taxonomy contributes none, and provider registration/configuration rejects the reserved `model-router` ID.
  - verify: `TestADR_0369_Scenario2_RouterDiscovery`
- AC2.2: literal search and exact filters cover router category names and descriptions; descriptions participate in canonical order, cursor digests, pagination, output bounds, and stale-cursor restart; direct provider rows retain their safe metadata and no discovery request probes, refreshes, selects, or switches a model.
  - verify: `TestADR_0369_Scenario2_DiscoveryBounds`
- AC2.3: `ListModels`, `/models`, and `CreateSession` do not publish or accept `model-router` as a provider.
  - verify: `TestADR_0369_Scenario2_RootSelectionUnaffected`

### Scenario 3 — Subagent resolves explicit selectors safely

The existing precedence ladder from [ADR 0031](../adr/0031-subagent-model-router.md) retains omitted-selector automatic routing. Explicit selector intent takes precedence over the classifier.

**Acceptance:**
- AC3.1: a bare literal `model` retains same-parent-provider behavior, a bare provider-aware alias carries its pair, and a concrete `{provider, model}` pair mints a fresh child engine with that target's dependencies; omitting both leaves the selector unset and permits normal automatic routing.
  - verify: `TestADR_0369_Scenario3_SubagentDirectSelectors`
- AC3.2: `{provider:"model-router", model:{discovered-category}}` resolves the category's scalar or provider-aware alias target with no classifier call; it sets only explicit-selector provenance plus the resolved concrete provider/model, never classifier-routing evidence, and runs the child on that target.
  - verify: `TestADR_0369_Scenario3_SubagentRouterSelector`
- AC3.3: provider without model, conflicting explicit and alias providers, a named Subagent call with any provider-bearing call-level selector, unknown/unavailable provider, absent/disabled/unknown/unresolvable router category, fork with a selector, and resume with a selector each return bounded tool errors and start no child; an existing read-only named-specialist plus model-only override still rebuilds and runs that specialist with its full scope.
  - verify: `TestADR_0369_Scenario3_InvalidSelectors`

### Scenario 4 — Parallel and Team use the same selector language

[ADR 0034](../adr/0034-team-parallel-model-routing.md) already routes every delegation family. This scenario supplies an explicit selector to those families without changing their existing lifetime rules.

**Acceptance:**
- AC4.1: one explicit Parallel selector is validated before fan-out and applies to every branch; an invalid selector starts no branch, the judge remains on the parent model, and omitted fields retain per-branch automatic routing.
  - verify: `TestADR_0369_Scenario4_ParallelSelector`
- AC4.2: every Team member selector is validated before any member is added; one invalid selector leaves no partial team or child start. Each valid member resolves once at `AddMember` and retains its engine across rounds; a named member accepts only a model-only override and preserves the specialist's complete scope, while omitted fields retain current routing/default behavior.
  - verify: `TestADR_0369_Scenario4_TeamMemberSelector`
- AC4.3: all three delegation families emit the actual concrete provider/model, retain classifier category/model evidence only for classifier-originated routing, and carry an explicit router category separately without exposing task content, alias names, or configuration secrets; Mecatui renders the explicit form as `selected: model-router/{category} → {provider}/{model}`.
  - verify: `TestADR_0369_Scenario4_DelegationEvidence`

### Scenario 5 — the model is guided to preserve operator routing by default

The existing discovery posture is appended to the factory-built system role when `DiscoverModels` is present (`internal/app/build.go`), as required by [ADR 0369](../adr/0369-delegation-provider-model-selectors.md). It must teach the workflow without embedding current inventory or aliases.

**Acceptance:**
- AC5.1: the real factory-built prompt tells the model to omit delegation provider/model selectors by default, use `DiscoverModels` before a justified explicit choice, and explains that `model-router` rows are delegation categories rather than session-selection targets.
  - verify: `TestADR_0369_Scenario5_ModelVisibleWorkflow`
- AC5.2: the `DiscoverModels` specification accurately describes router-category rows, descriptions, search, bounded results, and the no-probe/no-selection boundary; it does not embed configured categories or aliases in static prompt text.
  - verify: `TestADR_0369_Scenario5_ToolSpecification`

## Out of scope

| Item | Defer-to | Decision |
|---|---|---|
| Router rows in `ListModels`, `/models`, or `CreateSession` | Separate root-session model-selection proposal | A category has no root-session task or inference endpoint. |
| New named-agent provider/model/router fields | Focused named-agent contract | Existing fields consume universal aliases, but this plan does not widen definition grammar or discovery. |
| Per-Parallel-branch selectors | A future branch-task object schema | This plan applies one selector to all branches and preserves `tasks: []string`. |
| A compact single-flag provider/model encoding | Future CLI compatibility proposal | Opaque model IDs have no reserved delimiter; the companion flag avoids inventing one. |
| New router categories or classifier behavior | Existing router configuration/ADR follow-up | This plan changes category target resolution, not taxonomy or classification policy. |

## Definition of done

1. Focused engine, composition, configuration, server mapper, and client-contract tests pass; `task generate`, `task api:update`, `task lint`, `task test:race`, `task api:check`, `task docs`, and `task site:build` pass on the implementation candidate, with API snapshots and the classified `engine/CHANGELOG.md` entry committed.
2. `task ac-trace-strict` resolves every named proof when the plan becomes `landed`.
3. `go run ./cmd/mecademo` still demonstrates a tool call, permission ask/approval, and result.
4. The implementation PR links the merged Plan / Interface PR and reports schema, event, and prompt-factory conformance.
5. `/panel-review` reports no ship blockers or unwaived reviewer failures.

## Deferred decisions and known risks

- Universal pair aliases widen provider switching beyond delegation: new-session defaults, isolated auxiliary call slots, named-agent definition resolution, and automatic router targets must re-derive provider-dependent state rather than swapping only a model string; existing sessions remain provider-bound and cross-provider plan targets must take the defined fallback.
- Disabling project `models:` is intentionally breaking. The warning must identify the ignored project source without echoing operator-authored model IDs or creating a startup failure.
- The companion CLI flag is deliberately non-atomic at argument-parse time; composition must validate the completed alias maps before building engines so argument order cannot change behavior.
- A direct provider/model child may be unavailable despite successful discovery; provider access and live listing remain independent, so the tool call must fail honestly.
- Category descriptions are operator-authored model-visible text and must be bounded and rendered through the existing safe display paths.
