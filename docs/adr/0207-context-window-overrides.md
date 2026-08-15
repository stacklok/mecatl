# ADR 0207 — Operator-owned exact context-window overrides

- Status: Accepted
- Date: 2026-08-12
- Scope: `internal/adapter/permconfig` (`ModelsSection.ContextWindows`), `internal/app` (`providerRegistry.resolveWindowCore`), `cmd/mecatui`

## Context

Provider context-window metadata can be absent, stale, or wrong for private gateways and
opaque routing identifiers. The existing global `--context-window-override` is useful for
emergencies and testing, but it applies one value to every provider and model. Operators
need a durable way to correct individual final routing IDs without changing provider
adapters or relying on fuzzy catalog matching.

A correction affects more than compaction. The engine limit, session echo, model picker,
per-session engine, and provider-bound child engines must report and consume the same
number. Allowing project configuration to set that number would also let repository-owned
configuration change deployment resource policy.

## Decision

Add operator-tier-only `models.context_windows` settings with the exact shape
`provider ID -> final model/routing ID -> positive token count`. Parse the subtree
strictly, reject empty keys and values outside the runtime-representable range and the
same sane upper bound used for live metadata, and report invalid entries by their full
provider/model path. Ignore project-tier values with an operator-visible warning.

Resolve a context window only after alias and slot resolution, by exact provider and exact
final model ID. Do not perform fuzzy, reverse-alias, or cross-provider lookup. Use this
precedence:

1. global CLI `ContextWindowOverride`;
2. exact configured provider/model value;
3. positive live metadata;
4. exact embedded models.dev catalog value;
5. the 128K fallback.

Keep the map composition-owned and route all consumers through
`providerRegistry.resolveWindowCore`. The engine and echo resolvers may continue to differ
only for the existing provisional-zero terminal case while live refresh is unsettled.
Expose the existing global override in embedded mecatui mode; reject it in connect mode,
where no embedded server owns the setting.

## Consequences

Operators can correct private and opaque model routes independently, while an absent map
preserves existing behavior. Configuration is intentionally exact and therefore requires
an entry update when a final routing ID changes. Central resolution prevents compaction,
echoes, model lists, and child engines from drifting, at the cost of threading the
composition-owned map into model-list projection. Project repositories cannot raise or
lower these deployment limits.

## See also

- [Context management and compaction](../architecture/context-and-compaction.md)
- [Model routing and provider selection](../usage/model-routing.md)
- [Configuration reference](../configuration-reference.md)
- [ADR 0016 — Multi-provider architecture](./0016-multi-provider.md)
- [ADR 0002 — Documentation lifecycle](./0002-documentation-lifecycle.md)
