# ADR 0343 — Operator-configured command runners

- Status: Proposed
- Date: 2026-09-15
- Scope: operator settings schema, command-root configuration folding, agent-facing command-runner environment construction, and Shell affordance documentation
- Supersedes: ADR 0028 section 6 only — the absolute-deny posture for explicitly operator-granted external CLI credentials
- Superseded by: none

## Context

Mecatl invokes an agent-facing Shell through a command runner. Command roots currently select `/bin/sh` by default and may override it with `--shell`; `--no-shell` disables the tool. The runner intentionally starts from the process environment and removes harness credentials plus conservative secret-shaped names before the shell sees them. This prevents a permitted or prompt-injected command from reading model-provider, server, and other credentials through `$NAME` or `/proc/self/environ`.

That protection also removes credentials an operator intentionally uses for an external CLI. A common example is `GH_TOKEN`: the operator's `gh` command works in their terminal, but the agent-facing Shell receives no token and reports an unauthenticated CLI. There is no operator configuration surface to make that ambient capability deliberate, nor a model-visible statement distinguishing a scrubbed environment from an operator who has not authenticated.

The current `temporary_storage:` section is related to command execution, but it owns more than an individual runner. ADR 0281 defines a shared managed resource root for command/job leases. ADR 0282 adds cross-command scratch caches and ADR 0283 adds delegation forks, all under the same lifecycle, retention, and cleanup authority. Nesting that section under a runner would misrepresent its shared ownership.

## Decision

Add a strict, operator-only `command_runner:` section to the user-global operator settings file. Its initial surface is:

```yaml
command_runner:
  shell: /bin/sh
  environment:
    inherit:
      - GH_TOKEN
```

`command_runner.shell` is the durable settings counterpart of `--shell`, including its empty-string shell-less mode. Resolution is: built-in `/bin/sh` default, then operator settings, then an explicitly supplied `--shell`; command roots must track explicit flag presence rather than compare its value with the default. `--no-shell` always disables Shell. The resolved shell retains the existing `Config.Shell` scope, including agent-facing runners, hooks, and internal Git helpers. Project-local and shared `.mecatl/settings.yaml` files cannot select a command interpreter.

`command_runner.environment.inherit` is a deduplicated list of portable environment-variable names, not values or references. It defaults to empty. The runner continues to inherit the ordinary process environment and applies the existing secret scrub, except that a configured, present permitted name may be retained only by the built-in main and local alternate-placement main Shell runners. A custom placement provider owns its complete `tool.Environment` and is not rewritten by this policy.

The following remain non-overridable and are always scrubbed: every `MECATL_*` variable; every canonical variable Mecatl reads for provider, web-search, server, or driver authentication; and every environment-variable name supplied as an MCP or other Mecatl credential reference. Composition resolves this complete reserved-name set before runner construction so a future configurable credential reader cannot be restored merely because its name matches a general secret pattern. The exception permits only external credential-shaped names such as `GH_TOKEN`; it never permits a harness credential. Names, never values, may appear in startup diagnostics. They are not serialized into sessions, events, API responses, or model prompt content.

Hardened runners for read-only Subagents, Team members, and Parallel branches retain a completely scrubbed, Git-neutralized environment. Fork-time Git, dirty-overlay Git, Git snapshots, and worktree discovery also retain unconditional scrubbing. A direct-write Subagent intentionally uses the parent environment and therefore receives the main runner's configured grant in this first version. It is not an isolation guarantee. A future change that attenuates direct-write children must introduce an independently constructed same-workspace child runner and a separate child environment-policy contract.

Keep `temporary_storage:` as a peer operator-only section. It continues to own shared managed-root, reaping, scratch-cache, and fork-lifecycle configuration.

The Shell specification must state that credential-shaped variables are scrubbed by default, that an authentication failure does not establish that an operator is unauthenticated, and that a credential available to a command must never be inspected, echoed, copied, written, or committed.

## Consequences

Operators gain an explicit, reviewable way to make a narrowly chosen external CLI credential available to main-shell work without patching the binary or weakening the default scrub. A persistent shell selection becomes part of the operator configuration model and remains overridable at deployment invocation time.

The configuration creates ambient authority, not a command-specific credential capability: any main-shell command that permissions allow can access an inherited value. This ADR does not claim that a shell permission rule scopes the variable to a particular executable or protects same-user files and processes. Operators needing stronger isolation must use a dedicated OS identity, container/VM sandbox, or a future brokered credential design.

Existing deployments preserve current behavior because the configuration is absent and the inherited-name list is empty by default. The operator-only parser and test matrix must prove that project configuration cannot restore a secret and that all ungranted secrets remain scrubbed. The direct-write child exception is explicit technical debt, protected by regression coverage until a separately reviewed attenuation design replaces it.

## See also

- [Issue #1495](https://github.com/stacklok/mecatl/issues/1495)
- [ADR 0028 — mecatequi](./0028-mecatequi.md) — the original secret-scrub rationale
- [ADR 0281 — Managed temporary command leases](./0281-managed-temporary-command-leases.md)
- [ADR 0282 — Managed workspace scratch cache](./0282-managed-workspace-scratch-cache.md)
- [ADR 0283 — Managed delegation-fork lifecycle](./0283-managed-delegation-fork-lifecycle.md)
- [ADR 0317 — Canonical Shell command tool](./0317-canonical-shell-command-tool.md)
- [Operator-configured command runners acceptance plan](../acceptance/operator-configured-command-runners.md)
