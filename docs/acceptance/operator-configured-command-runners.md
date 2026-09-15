# Operator-configured command runners — acceptance plan

**Contract:** human-reviewed/v2
**Work classification:** Architectural — establishes a durable operator-owned configuration and security boundary for selecting agent command interpreters and selectively inheriting ambient credentials.
**Decision record:** [ADR 0343](../adr/0343-operator-configured-command-runners.md)
**Phase:** command-runner configuration
**Status:** proposed, 2026-09-15. Drafted from issue #1495 and the operator decisions recorded during planning.
**Delivery:** Split. The operator-facing configuration and credential-authority contract need human review before implementation.
**Expected tasks:** deferred to orchestration
**Issue:** [stacklok/mecatl#1495](https://github.com/stacklok/mecatl/issues/1495).
**Plan PR:** pending
**Approved baseline:** absent until approved

Mecatl will give the operator a strict `command_runner:` configuration section in the user-global settings file. It makes the command interpreter a durable settings default and permits an explicit list of non-harness environment-variable names to pass through the existing secret scrub to built-in main-session command runners. The default remains a scrubbed environment with `/bin/sh` selected by command roots; an explicit CLI `--shell` remains higher precedence and `--no-shell` remains an unconditional disable. As the settings twin of `--shell`, the configured interpreter applies wherever the existing `Config.Shell` is consumed, including agent-facing runners, hooks, and internal Git; only the environment grant is limited to agent-facing main runners.

The grant is ambient authority for a main-session Shell: every permitted main-shell command can read it. It is operator-only, value-free, and visible to the operator and model as a constrained capability. Initial scope deliberately preserves current hardened child and internal-Git scrubbing. Direct-write children retain main-runner parity until a later attenuation design provides a same-workspace child runner with independent environment policy.

## Human decisions

None — the operator chose an operator-global `command_runner:` namespace, `command_runner.shell` as the persistent twin of `--shell`, and a staged main-runner-only environment grant; the existing `temporary_storage:` namespace remains separate because it owns shared command, scratch-cache, and fork lifecycle policy.

## Interface contract

- **gRPC / protobuf:** None — session and wire clients neither provide environment-variable names nor receive credential values.
- **Exported Go APIs / interfaces:** None — configuration resolves in host composition and the existing bound `tool.CommandRunner` seam remains unchanged.
- **Tool schemas:** None — `Shell` arguments and results remain unchanged.
- **CLI / config:** Add strict operator-only `command_runner.shell` and `command_runner.environment.inherit` to user-global `settings.yaml`; `shell` mirrors `--shell` (`""` disables Shell), and `inherit` is a deduplicated list of valid portable environment names. `command_runner.shell` overrides the built-in `/bin/sh` default; an explicitly supplied `--shell` overrides it through command-root flag-presence tracking; `--no-shell` disables Shell regardless. The `inherit` default is empty. The documented command roots apply the same settings/CLI precedence, including embedded mecatui. Project `.mecatl/settings.yaml` and `.mecatl/settings.local.yaml` blocks are ignored with a value-free warning; explicit `--permission-config` files remain an operator-tier source according to resolver precedence.
- **Events / persistence:** None — grants are process-start configuration, are not serialized into sessions/events, and variable values are never persisted.
- **Security / authority:** The ordinary agent environment remains `os.Environ()` minus secret-shaped names. `environment.inherit` may restore only named, present, external variables that are not reserved by the harness. `MECATL_*`; canonical provider, web-search, server, and driver credential names; and every environment name configured as an MCP or other Mecatl credential reference stay unconditionally removed. The composition-resolved reserved-name set is the single source for these dynamically configured references. The grant applies only to the built-in main and built-in local alternate-placement runners; a custom `PlacementProvider` owns its complete environment and is not rewritten. Hardened read-only/mutating-fork child runners and all internal Git/snapshot/fork commands remain fully scrubbed. Direct-write children currently use the main runner and therefore share its explicit grant; independently attenuating that runner is deferred.
- **Compatibility / migration:** Added configuration is backward compatible: absent `command_runner` preserves all current defaults and complete secret scrubbing. Existing `--shell` and `--no-shell` remain supported. `temporary_storage` is not moved or aliased.

## In scope — 3 scenarios, in implementation order

### Scenario 1 — Operator command-runner defaults resolve without project control

The resolver gains a strict operator-only command-runner section following the established operator-only settings pattern. The interpreter setting belongs with runner construction, while managed temporary storage remains a peer because it owns a shared lifecycle namespace spanning command leases, scratchpads, and forks. See [ADR 0343](../adr/0343-operator-configured-command-runners.md) and [ADR 0281](../adr/0281-managed-temporary-command-leases.md).

**Acceptance:**
- AC1.1: A user-global `command_runner.shell` supplies the shell path when no explicit `--shell` flag is set.
  - verify: `TestCommandRunnerConfig_Scenario1_SettingsShellDefault`
- AC1.2: An explicitly supplied `--shell` overrides the settings value through a tracked flag-presence bit, `--no-shell` prevents runner creation regardless of either setting, and the effective configured shell is used by every existing `Config.Shell` consumer.
  - verify: `TestCommandRunnerConfig_Scenario1_CLIAndDisablePrecedence`
- AC1.3: Unknown keys, malformed environment names, duplicate names, and project-tier `command_runner` blocks fail closed or are ignored under the documented operator-tier policy without leaking values; `command_runner.shell: ""` retains the existing shell-less `--shell ""` behavior.
  - verify: `TestCommandRunnerConfig_Scenario1_StrictOperatorOnlyValidation`

### Scenario 2 — Explicit main-shell credential inheritance is constrained and attributable

The secret scrub remains denylist-based for toolchain compatibility. A narrowly validated operator name exception restores only a configured, present external variable to the built-in main and local alternate-placement runners; it never becomes a credential-value configuration mechanism. The composition must calculate a single reserved-name set containing every harness credential and every configured environment credential reference before allowing restoration. The baseline scrub and runner wiring are described in [ADR 0028](../adr/0028-mecatequi.md) and [ADR 0343](../adr/0343-operator-configured-command-runners.md).

**Acceptance:**
- AC2.1: With an empty `command_runner.environment.inherit`, built-in main and local alternate-placement Shell runners omit every existing secret variable exactly as before while preserving ordinary toolchain variables.
  - verify: `TestCommandRunnerEnvironment_Scenario2_DefaultScrub`
- AC2.2: A configured, present allowed name such as `GH_TOKEN` reaches the built-in main and local alternate-placement Shell runners, while its value is never placed in settings, diagnostics, events, persistence, or prompt text; custom placement-provider environments remain unchanged.
  - verify: `TestCommandRunnerEnvironment_Scenario2_MainGrant`
- AC2.3: A configured harness credential name, every dynamically configured Mecatl credential-reference name, and every unconfigured secret-shaped name remain absent even when another name is granted; absent configured names are reported only by name to the operator.
  - verify: `TestCommandRunnerEnvironment_Scenario2_NonOverridableAndAbsent`
- AC2.4: The Shell model-visible specification explains that credentials are scrubbed by default, authentication failures do not prove the operator is unauthenticated, and credentials must not be inspected or exposed.
  - verify: `TestCommandRunnerEnvironment_Scenario2_ShellPromptContract`

### Scenario 3 — Child and internal execution retain their existing secret boundary

The initial grant intentionally does not weaken hardened child or internal Git execution. Direct-write Subagents are the documented exception because they intentionally use the real parent runner; a later design must introduce a separately constructed same-workspace child runner before changing that behavior. See [ADR 0343](../adr/0343-operator-configured-command-runners.md).

**Acceptance:**
- AC3.1: Read-only Subagents, Team members, and Parallel branches continue to run with a fully scrubbed and Git-neutralized environment even when the main runner has an explicit inherited variable.
  - verify: `TestCommandRunnerEnvironment_Scenario3_HardenedChildrenStayScrubbed`
- AC3.2: Fork-time Git, dirty-overlay Git, workspace snapshots, and worktree discovery never receive an inherited credential grant.
  - verify: `TestCommandRunnerEnvironment_Scenario3_InternalGitStaysScrubbed`
- AC3.3: Direct-write Subagent behavior is covered explicitly: it uses the main runner and therefore receives only the main runner's deliberate grant; the documentation identifies this as an attenuation follow-up rather than child isolation.
  - verify: `TestCommandRunnerEnvironment_Scenario3_DirectWriteParity`

## Out of scope

| Item | Defer-to | Decision |
|---|---|---|
| Per-command credential custody, an allowlisted executable broker, or shell sandboxing | future security design | A variable inherited by `shell -c` is ambient authority; this plan does not claim command-scoped isolation. |
| Independent direct-write child credential attenuation | follow-up acceptance plan | Requires a same-workspace child runner and a distinct child environment-policy contract. |
| `command_runner` settings twins for all other command flags | incremental configuration-parity work | This plan establishes the namespace only for `--shell` and credential inheritance. |
| Moving `temporary_storage` below `command_runner` | none currently | ADRs 0281–0283 define it as a shared managed-resource lifecycle namespace, not runner-local configuration. |

## Definition of done

1. Applicable `task lint`, `task test`, `task docs`, and `task api:check` gates pass.
2. `task ac-trace-strict` resolves every named proof when the plan becomes `landed`.
3. `go run ./cmd/mecademo` remains green for runtime changes.
4. The implementation PR links the Plan / Interface PR and approved commit and reports interface conformance.
5. `/panel-review` reports no ship blockers or unwaived reviewer failures.

## Deferred decisions and known risks

- Main-shell inheritance is deliberately a bounded improvement, not a sandbox. Operators should prefer credentials limited to the intended external service and use a dedicated OS account or stronger sandbox where same-user command access is unacceptable.
- A later child attenuation design must not silently turn `environment.inherit` into a child-wide grant.
