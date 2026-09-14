# ADR 0333 — Unified provider configuration and Mecatui provider commands

- Status: Proposed
- Date: 2026-09-12
- Scope: operator provider configuration, provider credential lifecycle, and Mecatui local provider CLI
- Supersedes: ADR 0329's `llm.endpoints` configuration façade; the authentication-schema portion of ADR 0238
- Superseded proposal: ADR 0332, if this decision is accepted before PR #1441 merges

## Context

Mecatl has one runtime provider registry, but presents two incompatible configuration concepts:

- `providers.<name>` defines a custom provider using `none` or `api_key` authentication.
- `llm.endpoints.<name>` defines an OIDC-backed Responses gateway and is normalized into the same provider registry.

The Mecatui commands mirror that split: `mecatui llm setup` manages API-key/default setup while `mecatui llm config set ENDPOINT`, `login ENDPOINT`, `status [ENDPOINT]`, and `logout ENDPOINT` manage OIDC gateway enrollment. ToolHive is a stock provider/integration with its own OIDC-capable lifecycle. The overloaded terms "Native LLM" and "endpoint" do not convey the distinction: an endpoint name is actually a provider name, not a network address.

This is a new public surface. `llm.endpoints` was introduced on 2026-09-10 and its Mecatui lifecycle was completed in PR #1388 on 2026-09-12. PR #1441 is a prototype of local setup, not the desired long-term provider model.

## Decision

Use **provider** as the sole user-facing and configuration abstraction for a named source of models. Provider names retain the existing stable, lower-case DNS-label-like identifier rule; no friendly-name layer is added. A provider has a transport/API flavor, default model, authentication capability, selected authentication method, credential custody, and lifecycle owner.

Transport origin and authentication are independent:

- A stock provider may support API-key, OIDC, no-auth, or external lifecycle authentication as its integration permits; code defines its supported methods and configuration may select only one of them.
- A custom provider has one configured authentication method: `api_key`, `oidc`, or `none`. A second custom provider name is the intentional escape hatch when an operator needs a second configuration/authentication method for the same upstream service.
- The selected authentication method determines credential setup, renewal, inspection, and removal. It does not create a distinct user-facing species of provider.

Replace `llm.endpoints` with an OIDC authentication form under `providers.<provider-name>`. Use `credential_store.api_key.file` for the configurable file-backed API-key input (optional; default `$XDG_CONFIG_HOME/mecatl/auth.yaml`). The shared protected OIDC store is exactly `credential_store.oidc.home` plus `credential_store.oidc.key` (`source: keyring|environment`; `key_env` required only for `environment`), retaining the current deployment-scoped XDG state/keyring defaults. The API-key file temporarily also carries the experimental OpenAI Codex manual-token record; this exception is tracked separately and does not change the API-key-oriented command/flag vocabulary. The per-provider OIDC shape is `auth.method: oidc` plus a required `auth.oidc` mapping: `issuer`, `client_id`, `scopes`, optional `resource_audience`, and required independent `issuer_trust` and `gateway_trust` mappings. `auth.oidc` is forbidden for `api_key` and `none`. Custom OIDC remains valid only for `openai-responses`. Any legacy `llm:` mapping, including `endpoints`, `credential_home`, and `credential_key`, is rejected with migration guidance; no automatic configuration or credential rewrite is introduced. Strict parsing, bounded credential custody, and identity binding remain unchanged. Retain `provider_overrides:` unchanged to keep this work bounded. Future OIDC discovery/DCR may populate this same provider configuration from a URL, but is not implemented or implied here.

Rename the Mecatui provider command group to `mecatui providers`. Use **provider name** for named arguments; reserve URL/address for actual network locations. Remove "Native LLM" from normal CLI copy and user documentation.

The intended flat command family is:

```text
mecatui providers
mecatui providers status [PROVIDER]
mecatui providers setup [PROVIDER]
mecatui providers add PROVIDER [flags] [--no-login]
mecatui providers login PROVIDER [--no-browser]
mecatui providers logout PROVIDER
mecatui providers set-default PROVIDER [MODEL]
mecatui providers remove PROVIDER
```

Bare `providers` produces passive aggregate status and next steps; `providers --help` is the comprehensive conceptual reference. Do not add `list` initially. `setup` is a newcomer wizard that composes direct actions and offers numbered, capability-labelled selection. `add` creates a custom definition and chains to `login` by default; `--no-login` explicitly suppresses the handoff. Editing an existing custom definition is deferred to manual `settings.yaml` changes.

`login` makes an existing provider usable or replaces its local credential: it prompts for an API key for an API-key provider and runs enrollment for a Mecatl-owned OIDC provider. `logout` clears only locally managed credentials while retaining the provider definition. `remove` deletes a custom provider definition and its Mecatl-owned credentials after a clear combined confirmation; stock providers cannot be removed. Removal of the selected default is permitted and the resulting no-provider/default startup failure is ordinary recovery state, not an exceptional lockout.

Make a deliberate clean break: legacy `llm.endpoints` causes startup to fail with a precise migration error and a `mecatui providers --help` recovery path, and all `mecatui llm` commands disappear immediately. This removes no capability: provider configuration, API-key setup, OIDC enrollment/status/logout, defaults, and ToolHive handoff continue through the new command family.

ToolHive remains a provider/integration but continues to own its lifecycle. Mecatui describes and delegates that lifecycle rather than copying ToolHive credentials or inventing generic actions it cannot perform.

## Consequences

The provider registry remains composition-owned and provider-neutral at the engine port. No client/server protocol, model-facing tool schema, or engine public API is required merely to consolidate local operator configuration and CLI language.

The provider schema expands beyond ADR 0238's `none|api_key` enum. This is a durable authentication/custody decision, not a CLI rename. OIDC must retain protected encrypted credential storage, identity binding, no secret disclosure, local embedded-Mecatui enrollment only, and the existing deployment-scoped authority boundaries from ADR 0329.

`mecatui providers` becomes the authoritative help tree for local embedded provider setup. Top-level `mecatui login ADDRESS` and `mecatui logout ADDRESS` remain remote-server authentication commands and must be clearly distinguished.

A portable setup writer is required for Linux and macOS. It may simplify the prototype's cooperative locking and highly granular crash-durability reporting, but it must retain secret-boundary protections: private ownership/modes, no unsafe symlink following, same-directory atomic replacement, error/cancellation handling, and no secret output. It must not claim unsupported durability or silently weaken safety to gain platform coverage.

## Alternatives considered

### Keep `llm.endpoints` and improve the help only

Rejected as the working direction. It retains two configuration concepts for one runtime provider and permanently teaches users an implementation-specific "native endpoint" category.

### Make OIDC providers a separate user-facing class

Rejected. OIDC is an authentication method. A provider such as a corporate gateway can legitimately offer API-key and OIDC authentication without being modeled as two unrelated providers.

### Merge PR #1441 unchanged, then refactor

Rejected. The prototype exposes the terminology and Linux-only persistence behavior this decision replaces. Merging it first would immediately create a deprecation burden.

### Move to Cobra before designing the hierarchy

Rejected. A command framework cannot resolve the domain model or vocabulary. Reconsider a framework only after the command contract is settled and if command growth, shell completion, or generated documentation justifies it.

## Decision completeness

The provider configuration is intentionally bounded: custom OIDC is valid only for `openai-responses`; every other custom OIDC flavor is a configuration error. The existing ADR 0329 identity binding, record namespace, provider-scoped lock, refresh/login/logout ordering, and explicit re-enrollment requirement on an identity change remain unchanged. The portable writer retains safe private-file handling and final changed-target detection but deliberately omits the cooperative sidecar lock/retry protocol. Future URL-only OIDC discovery or DCR may populate the same configuration records under a separate decision.

## See also

- [ADR 0016](0016-multi-provider.md) — composition-owned multi-provider registry
- [ADR 0238](0238-operator-defined-llm-providers.md) — original custom provider schema
- [ADR 0329](0329-native-llm-endpoint-gateway-credentials.md) — OIDC gateway custody and lifecycle to preserve
- [Provider architecture](../architecture/providers.md)
