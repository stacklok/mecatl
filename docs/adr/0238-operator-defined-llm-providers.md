# ADR 0238 — Operator-defined LLM providers

- Status: Accepted
- Date: 2026-08-25
- Scope: operator-local LLM provider definitions, built-in provider endpoint overrides, and `auth.yaml` API-key selection

## Context

Mecatl's provider registry has distinct provider identities but currently constructs only
hard-coded entries. Compatible gateways therefore require per-process base-URL flags and
must be presented as a built-in provider even when they are an operator's own service.
That loses the service's identity in session selectors, model routing, and diagnostics.

A gateway can expose multiple known wire protocols under one host. The runtime needs an
explicit wire flavor rather than guessing from a URL. Credentials must remain outside
shareable `settings.yaml`; the existing operator-local `auth.yaml` is the appropriate
v1 source. Provider credentials must be composition-owned so future refreshable OAuth
credentials can add lifecycle resources without moving ownership across every command
root. The composition-only provider registry and provider-neutral engine port from
[ADR 0016](./0016-multi-provider.md) must remain intact.

## Decision

Add an operator-tier-only `providers:` settings section for custom LLM-provider entries
and a separate `provider_overrides:` section for the base URLs of eligible built-ins.
Custom entries have a stable, lower-case DNS-label-like ID, one HTTPS base URL with no
userinfo, query, or fragment, one required default model, and a closed `api_flavor` enum:

- `openai-responses`
- `openai-chat-completions`
- `anthropic-messages`

Their closed v1 `auth.method` enum is `none` or `api_key`; unknown fields and values fail
strict parsing. `api_key` reads exactly one opaque `providers.<id>.api_key` record from
the existing operator-local `auth.yaml`. The flavor owns the established HTTP
authentication mechanics; arbitrary headers, multiple credentials, OAuth, client
certificates, proxy settings, and custom CA bundles are not configuration features in
this decision.

The injected credential loader derives permitted API-key IDs by adding custom definition IDs to
the fixed built-in set, then reads `auth.yaml` once into an immutable `ProviderCredentials`
snapshot. No registry package reads configuration, auth files, or the process environment directly,
and custom credentials have no environment fallback.

Every command root injects a shared `ProviderCredentialLoader` into declarative `app.Config`.
`app.Build` resolves the operator provider definitions once, invokes that loader once before
provider default selection and registry construction, applies its immutable credentials, and
owns/closes any returned `ProviderCredentialLifecycle`. CLI endpoint flags are non-secret command
configuration: roots project them as a small endpoint-override map, and Build merges that map over
settings-derived `provider_overrides` before constructing the registry. The API-key auth-file
implementation returns no closer; the seam is ready for future OAuth/token-source implementations
without implementing interactive login, refresh, or new credential storage now. Tests inject a
deterministic fake loader rather than bypassing provider loading through command-specific helpers.

Custom provider IDs are first-class persisted `provider_id` values. They may share a URL
and flavor, but may not collide with a built-in ID or another custom entry. A configured
API-key provider is available only when its matching auth-file record resolves; `none`
providers are available from configuration alone. Missing, renamed, or removed custom
providers fail an explicitly persisted session selector loudly rather than falling back.

The composition-local provider entry owns the custom default model. It is the source for
empty-model session selection, deployment-default validation, the synchronous model
inventory floor, and fallback after live listing. Explicit session model selectors
continue to win. Static per-provider model declarations are not part of v1.

Custom entries use a flavor-specific live lister only when the existing adapter can list
at the configured base URL. Listing requests use the resolved auth method, a bounded
body/time envelope, and a redirect-refusing HTTP client. A failure, timeout, malformed
payload, or empty result never disables an otherwise configured provider: its default
model remains its sole inventory floor. Sparse/unknown live metadata is conservative and
never grants unsupported modalities or limits.

`provider_overrides` supports only `openai`, `openrouter`, `anthropic`, and `opencode`.
Its `base_url` setting is the persistent equivalent of their existing `--*-base-url`
flags. CLI flags win over operator settings, which win over built-in defaults. An override
preserves the matching flag's existing inventory and private-option behavior; it does not
silently convert a built-in into a custom provider. Codex and ToolHive deliberately remain
outside this section because their endpoint and credential policies have distinct security
contracts.

All parsing, auth resolution, registry construction, lister selection, and adapter
options remain in composition and command wiring. No domain, engine-port, agent-loop,
or wire/proto surface is widened.

## Consequences

Operators can name their gateways truthfully, use several gateway credentials without
credential sharing, and persist endpoints/default-provider selection across mecated,
embedded mecatui, mecatequi, and mecak8s. The configuration has a deliberately small,
reviewable attack surface and retains existing secret-redaction and strict-auth-file
rules.

A custom provider must name a default model even if its gateway has a model endpoint;
this makes zero-selector requests deterministic. Provider IDs become durable operator
configuration: removing or renaming one can prevent selected sessions from rehydrating
until the configuration is restored or the session is explicitly migrated. Supporting
new auth systems or transport controls requires a new ADR rather than widening this
schema ad hoc.

## See also

- [ADR 0016](./0016-multi-provider.md) — composition-only multi-provider registry
- [ADR 0067](./0067-openai-chat-completions-adapter.md) — Chat Completions wire adapter
- [ADR 0093](./0093-provider-modules.md) — provider module boundary
- [ADR 0215](./0215-openai-subscription-manual-token.md) — Codex's distinct endpoint policy
- [Architecture: providers](../architecture/providers.md)
