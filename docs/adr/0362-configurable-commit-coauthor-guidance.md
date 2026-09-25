# ADR 0362 — Operator-configurable standard commit co-author guidance

- Status: Proposed
- Date: 2026-09-25
- Scope: operator settings schema and standard root-prompt composition for main and delegated engines
- Supersedes: —
- Superseded by: —

## Context

Mecatl agents can create commits, but the standard prompt does not give them a canonical
Mecatl attribution trailer. This has produced provider-branded trailers rather than a stable
harness identity ([issue #1595](https://github.com/stacklok/mecatl/issues/1595)). The desired
default is a model-visible instruction to append exactly:

```text
Co-authored-by: Mecatl <noreply@mecatl.dev>
```

The direction must flow through standard system-prompt composition, which is shared by root
and delegated engines. Putting it in repository instructions, a skill, or another commit-message
path would make the behavior conditional or duplicate ownership.

Operators may need to suppress the attribution. The setting therefore needs a durable
operator-owned configuration surface. Repository-local configuration cannot decide it: repository
content is not an authority to weaken deployment behavior, and prompt guidance must not become
an arbitrary user- or project-provided system-prompt injection surface.

## Decision

Add a strict, operator-only `system_prompt:` settings section with this initial key:

```yaml
system_prompt:
  commit_coauthor: false
```

`commit_coauthor` defaults to `true` when the section or key is absent. When true, the standard
system-prompt builder includes guidance that an applicable committing agent append exactly
`Co-authored-by: Mecatl <noreply@mecatl.dev>`. When false, it omits that guidance. The setting is
honoured from the user-global operator settings file and explicit `--permission-config` files,
which retain their existing higher precedence; a project `.mecatl/settings.yaml` or
`.mecatl/settings.local.yaml` value is ignored with the established value-free warning for
operator-only settings.

The standard composition path supplies the resolved setting to main and delegated engines. `engine/prompt.Config` gains `CommitCoauthor *bool`: `nil` and `true` include the guidance, and `false` omits it. The instruction belongs in the stable standard-prompt prefix, alongside other cache-stable guidance. This additive engine API change follows the compatibility process, including an API snapshot update and classified changelog entry. It does not alter provider request contracts, tool schemas, permissions, or commit execution.

This ADR does not create arbitrary prompt replacement or text-append configuration. The
`system_prompt` namespace is typed and strict: unknown nested keys are configuration errors. A
host that provides `agent.Deps.PromptBuilder` continues to own its complete replacement prompt;
the setting does not inject text into host-owned builders.

No CLI flag or ambient environment variable is added. The operator-global settings file is the
single configuration interface for this initial opt-out.

## Consequences

Mecatl has a stable default attribution identity while retaining an explicit, reviewable
operator opt-out. Existing deployments adopt the default without configuration, and an operator
can suppress it without modifying a repository or replacing the entire prompt.

The narrow typed setting keeps safety, tool guidance, and cache structure under standard
composition ownership. It costs a schema/resolver/test/documentation path for a small boolean
and establishes `system_prompt` as a namespace whose future additions require the same
operator-authority and strict-schema review; it is not permission to add free-form prompt text.

Prompt text alone cannot ensure a trailer is used. Existing permission and authority controls
continue to determine whether an agent can create a commit, and externally authored commit
trailers remain out of scope.

## See also

- [Issue #1595](https://github.com/stacklok/mecatl/issues/1595)
- [Acceptance plan: Configurable Mecatl commit co-author guidance](../acceptance/configurable-commit-coauthor-guidance.md)
- [The agent loop](../architecture/agent-loop.md)
- [ADR 0023 — Workspace trust](./0023-workspace-trust.md)
- [ADR 0043 — Ephemeral turn-0 instruction fragments](./0043-ephemeral-turn0-instruction-fragments.md)
- [ADR 0306 — Human-reviewed development contracts](./0306-human-reviewed-development-contracts.md)
