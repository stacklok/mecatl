# Configurable Mecatl commit co-author guidance — acceptance plan

**Contract:** human-reviewed/v2
**Work classification:** Architectural — adds an operator-owned, default-on configuration contract that changes standard root-prompt behavior across main and delegated engines.
**Decision record:** [ADR 0362](../adr/0362-configurable-commit-coauthor-guidance.md)
**Phase:** prompt provenance
**Status:** proposed, 2026-09-25. Derived from [stacklok/mecatl#1595](https://github.com/stacklok/mecatl/issues/1595).
**Delivery:** Split. The operator configuration, prompt-composition, and delegated-agent inheritance contract need review before implementation.
**Expected tasks:** deferred to orchestration
**Issue:** [stacklok/mecatl#1595](https://github.com/stacklok/mecatl/issues/1595).
**Plan PR:** pending
**Approved baseline:** absent until approved

Mecatl's standard system prompt will direct an agent that creates a commit to append the canonical Mecatl trailer. The direction is enabled unless the operator globally opts out. The implementation must deliver the same standard guidance to root and delegated engines through their shared prompt composition path.

The setting is an operator-owned policy rather than repository content: a project cannot remove the guidance. This plan intentionally introduces only this boolean; it does not define arbitrary system-prompt replacement or a general prompt-customization language.

## Human decisions

None — issue #1595 defines the exact trailer, default-on behavior, and operator-only opt-out; this plan records those decisions as the durable interface contract.

## Interface contract

- **gRPC / protobuf:** None — the setting is process composition input and changes no RPC, HTTP, or generated contract.
- **Exported Go APIs / interfaces:** `engine/prompt.Config` gains `CommitCoauthor *bool`; `nil` and `true` include the standard guidance, while `false` omits it. This additive engine API change follows `engine/COMPATIBILITY.md`, including `task api:update` and a classified `engine/CHANGELOG.md` entry. A host-supplied `agent.Deps.PromptBuilder` remains a complete replacement of the standard builder.
- **Tool schemas:** None — no tool input/output schema changes.
- **CLI / config:** Add strict operator-only `system_prompt.commit_coauthor` to `$XDG_CONFIG_HOME/mecatl/settings.yaml` and explicit `--permission-config` files (the existing highest-precedence operator configuration source); explicit files override the conventional global file. Absent means `true`, `false` omits the standard guidance. Project `.mecatl/settings.yaml` and `.mecatl/settings.local.yaml` values are ignored with the existing operator-tier warning behavior. No dedicated CLI flag or environment variable is added.
- **Events / persistence:** None — the resolved setting and the guidance are not persisted in sessions, events, or operator-profile storage.
- **Security / authority:** Project content cannot weaken the default prompt guidance. The setting does not change tool permissions, commit execution authority, or treatment of externally authored trailers.
- **Compatibility / migration:** Existing configurations preserve enabled guidance because absence resolves to `true`. Operators who require no Mecatl trailer set `system_prompt.commit_coauthor: false`; no stored data migration is required.

## In scope — 3 scenarios, in implementation order

### Scenario 1 — Default standard prompt directs canonical attribution

The standard prompt builder emits the canonical commit-trailer direction in its stable system-prompt prefix when the setting is absent or enabled. It does not add a second commit-message construction path. [ADR 0362](../adr/0362-configurable-commit-coauthor-guidance.md) records this placement against the standard system-prompt path described by [the agent loop](../architecture/agent-loop.md).

**Acceptance:**
- AC1.1: An enabled standard prompt instructs a committing agent to append exactly `Co-authored-by: Mecatl <noreply@mecatl.dev>`.
  - verify: `TestConfigurableCommitCoauthorGuidance_Scenario1_DefaultGuidance`
- AC1.2: The guidance is owned by standard prompt composition rather than project turn-0 instructions, skills, or caller-specific commit logic.
  - verify: inspection — `engine/prompt.Build` is the shared system-prompt builder and is the owning layer.

### Scenario 2 — Operator opt-out is strict and repository-safe

The user-global settings schema recognizes the `system_prompt` subtree strictly and resolves its default as enabled. Explicit `--permission-config` files are the existing highest-precedence operator input; project files cannot disable the guidance. [ADR 0362](../adr/0362-configurable-commit-coauthor-guidance.md) makes the operator/project authority boundary durable.

**Acceptance:**
- AC2.1: `system_prompt.commit_coauthor: false` in either the operator-global settings file or an explicit `--permission-config` file removes the standard guidance; an explicit file takes precedence over the conventional global file.
  - verify: `TestConfigurableCommitCoauthorGuidance_Scenario2_OperatorOptOut`
- AC2.2: An unknown key under `system_prompt` fails settings validation rather than silently changing prompt policy.
  - verify: `TestConfigurableCommitCoauthorGuidance_Scenario2_StrictSystemPromptConfig`
- AC2.3: Project-tier `system_prompt` configuration cannot disable the guidance and produces the established value-free operator-tier warning.
  - verify: `TestConfigurableCommitCoauthorGuidance_Scenario2_ProjectConfigIgnored`

### Scenario 3 — Shared composition covers main and delegated engines

Composition supplies the resolved setting to all engines that use the standard builder, so a delegated agent sees the same default guidance as its parent. A host that supplies its own complete `PromptBuilder` retains ownership of its prompt. This preserves the engine dependency and composition boundary in [AGENTS.md](../../AGENTS.md) and is recorded by [ADR 0362](../adr/0362-configurable-commit-coauthor-guidance.md).

**Acceptance:**
- AC3.1: A real composed main engine receives the enabled standard guidance.
  - verify: `TestConfigurableCommitCoauthorGuidance_Scenario3_MainEngine`
- AC3.2: A real delegated child engine receives the same enabled standard guidance.
  - verify: `TestConfigurableCommitCoauthorGuidance_Scenario3_DelegatedEngine`
- AC3.3: A host-supplied complete prompt builder is not modified by this setting.
  - verify: `TestConfigurableCommitCoauthorGuidance_Scenario3_CustomPromptBuilder`

## Out of scope

| Item | Defer-to | Decision |
|---|---|---|
| Arbitrary system-prompt replacement or free-form YAML prompt text | A separately reviewed prompt-customization proposal | This change reserves `system_prompt` for a narrow, typed option and preserves immutable safety/tool composition. |
| CLI or environment-variable aliases for the opt-out | A demonstrated operator workflow need | The durable operator settings file is the sole interface for this initial boolean. |
| Rewriting existing commits or trailers authored outside Mecatl | Never part of this feature | The prompt guidance affects only new agent-created commits. |

## Definition of done

1. Applicable `task lint`, `task test:race`, `task docs`, and `task api:check` gates pass on the final candidate.
2. `task ac-trace-strict` resolves every named proof when the plan becomes `landed`.
3. `go run ./cmd/mecademo` remains green for runtime changes.
4. The implementation PR links the Plan / Interface PR and approved commit and reports interface conformance.
5. `/panel-review` reports no ship blockers or unwaived reviewer failures.

## Deferred decisions and known risks

- The exact internal field and resolver accessor names are implementation details, provided they preserve the configuration and authority contract above.
- The standard prompt is not an enforcement mechanism: the existing permission and authority checks continue to govern whether a commit command can execute.
