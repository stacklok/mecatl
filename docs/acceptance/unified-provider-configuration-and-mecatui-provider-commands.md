# Unified provider configuration and Mecatui provider commands — acceptance plan

**Contract:** human-reviewed/v2
**Work classification:** Architectural — replaces a new public CLI/configuration vocabulary, expands custom-provider authentication and credential custody, and supersedes the `llm.endpoints` configuration decision.
**Decision record:** [ADR 0333](../adr/0333-unified-provider-configuration-and-mecatui-provider-commands.md)
**Phase:** replace the prototype provider setup surface with one provider model
**Status:** landed, 2026-09-12. Plan / Interface PR #1444 merged as `db70d83aa844e87afe50a4a9843cbcdac32968d4`; schema amendment PR #1445 merged as `1c21e51b02fe766f169de81a5aaa3c89a63145a5`. Proposed landed transition on this implementation candidate; authoritative on merge.
**Delivery:** Split. The configuration, CLI, credential-custody, platform-write, and migration decisions require a separate human Plan / Interface review before implementation.
**Expected tasks:** deferred to orchestration
**Issue:** [stacklok/mecatl#1443](https://github.com/stacklok/mecatl/issues/1443).
**Plan PR:** [stacklok/mecatl#1444](https://github.com/stacklok/mecatl/pull/1444)
**Approved baseline:** `db70d83aa844e87afe50a4a9843cbcdac32968d4` (merged Plan / Interface commit; schema amendment `1c21e51b02fe766f169de81a5aaa3c89a63145a5`)

This work makes the named **provider** the sole local operator concept for selecting models, configuring a compatible service, and managing that provider's credentials. It replaces the split between `providers:` API-key/no-auth configuration and `llm.endpoints` OIDC configuration, and replaces the confusing `mecatui llm` command group with `mecatui providers`.

The target is not a cosmetic rename. It must preserve current composition-owned provider routing and protected OIDC custody while making custom API-key, OIDC, and no-auth providers coherent. It must also replace PR #1441's Linux-only, line-oriented prototype setup flow with an understandable provider workflow that safely works on Linux and macOS.

## Human decisions

- [x] Adopt provider as the unified user-facing abstraction. — Decision: stock and custom transport origin are independent of authentication; API key, OIDC, no-auth, and external lifecycle are provider capabilities rather than separate user-facing LLM types.
- [x] Use provider name rather than endpoint in user-facing CLI, help, errors, and docs. — Decision: a URL is a network address; a named configured gateway is a provider. Names retain the current lower-case DNS-label-like provider-ID rule; no friendly-name layer is added.
- [x] Remove “Native LLM” as user-facing terminology. — Decision: describe a provider's selected authentication and lifecycle instead.
- [x] Make a clean break from the new `llm.endpoints` and `mecatui llm` surfaces. — Decision: reject any legacy `llm:` configuration at startup, including `endpoints`, `credential_home`, and `credential_key`, with a precise migration error and guidance to `mecatui providers`; remove all `mecatui llm` forms immediately while preserving their functionality through the provider command family.
- [x] Use a flat provider command family. — Decision: bare `mecatui providers` renders passive aggregate status and next steps; there is no initial `list` command. Retain `setup`, `add`, `login`, `logout`, `set-default`, and `remove`; defer `configure`/editing custom definitions.
- [x] Define lifecycle/destructive actions. — Decision: `login` updates/replaces a credential while preserving the provider; `logout` clears locally managed credentials while preserving the provider; `remove` deletes a custom provider definition and its Mecatl-owned credentials after an explicit combined confirmation. Stock providers cannot be removed. Removing the selected default is permitted and leaves the normal no-provider/default startup error.
- [x] Make guided setup a newcomer wizard. — Decision: `setup` composes the direct actions with numbered capability-labelled provider selection. `add` chains to login by default and accepts an explicit `--no-login` opt-out.
- [x] Preserve credential split and current shared OIDC scope. — Decision: use `credential_store.api_key.file` for the configurable scriptable API-key input, defaulting to `auth.yaml`, and `credential_store.oidc` for the existing shared deployment-scoped protected OIDC store. The API-key file temporarily continues to carry the experimental Codex manual-token record; do not move it in this change.
- [x] Fix the exact unified OIDC YAML schema. — Decision: `credential_store.oidc.home` and `.key` replace `llm.credential_home` and `.credential_key`; `providers.NAME.auth.oidc` contains `issuer`, `client_id`, `scopes`, optional `resource_audience`, and required sibling `issuer_trust`/`gateway_trust` mappings. It is required only for `method: oidc`.
- [x] Rename the API-key file flag. — Decision: replace `--auth-file` with `--api-key-file PATH` immediately; no compatibility alias is retained because the existing flag has negligible expected adoption.
- [x] Bound writer concurrency control. — Decision: preserve private ownership/mode and symlink/special-file rejection, same-directory temporary replacement, atomic rename, cancellation handling, and truthful post-rename uncertainty. Remove the cooperative sidecar lock/retry protocol; take a pre-write snapshot and abort with a clear retryable error if the target changes before rename.
- [x] Require Linux and macOS setup parity. — Decision: API-key/default mutations and the provider setup workflow must have real tested support on both platforms; Ctrl-C prints `Cancelled; no changes made.` and exits 130.
- [x] Define OIDC configuration/custody layout. — Decision: `credential_store.api_key.file` selects the file-backed API-key input and defaults to `auth.yaml`; `credential_store.oidc` retains the existing shared protected store configuration. An OIDC provider uses `auth.method: oidc` and `auth.oidc` for issuer, client ID, scopes, optional audience, and issuer/gateway trust.
- [x] Define the closed provider capability matrix. — Decision: stock-provider support remains code-defined; custom `api_key` and `none` support the three existing API flavors; custom `oidc` supports only `openai-responses`; every other custom OIDC flavor is a configuration error.
- [x] Preserve OIDC identity and lifecycle concurrency. — Decision: retain ADR 0329 identity binding, record namespace, provider-scoped lock, login/refresh/logout ordering, local delete before best-effort revocation, and explicit logout/re-enrollment when identity inputs change.
- [x] Preserve removal failure behavior for durable selectors. — Decision: persisted session and schedule selectors retain their existing fail-closed behavior after provider removal/rename; operator remediation is restoring the provider or explicitly changing the selector, not automatic fallback.

## Interface contract

- **gRPC / protobuf:** None — provider configuration, local credential management, and help are operator-local Mecatui behavior; no remote enrollment RPC, wire message, or client event is added.
- **Exported Go APIs / interfaces:** Root-internal composition/adapter APIs will change to represent a custom provider's selected authentication method, including OIDC. The shared protected-store configuration is exactly `credential_store.oidc.home` and `credential_store.oidc.key.{source,key_env}`; a custom OIDC provider's exact identity/trust mapping is `providers.NAME.auth.oidc.{issuer,client_id,scopes,resource_audience?,issuer_trust,gateway_trust}`. No `engine/` export, provider-module public API, or provider-neutral `port.LLMRequest` field changes.
- **Tool schemas:** None — this is an operator CLI and configuration surface, not a model-facing tool or prompt affordance.
- **CLI / config:** Remove `mecatui llm` and add the flat `mecatui providers` command family: bare aggregate status; `status [PROVIDER]`; `setup [PROVIDER]`; `add PROVIDER [--no-login]`; it interactively collects base URL, API flavor, default model, and selected authentication method; an OIDC selection collects its required issuer/client/scopes/audience/trust fields. `login PROVIDER [--no-browser]`; `logout PROVIDER`; `set-default PROVIDER [MODEL]`; and `remove PROVIDER`. Do not add `list` or `configure` in this slice. Rename existing API-key-file uses of `--auth-file` to `--api-key-file PATH` with no alias. Reject legacy `llm.endpoints` at startup with precise migration guidance; replace it with an OIDC-capable custom-provider form under a named `providers.NAME` mapping plus `credential_store.oidc` and `credential_store.api_key.file`. The API-key file continues to default to `auth.yaml` and temporarily contains the experimental Codex manual-token record. Every command/subcommand has dedicated successful `--help` output.
- **Events / persistence:** Existing persisted session `provider_id` values remain provider names and retain their current fail-closed behavior if a configured provider is removed or renamed. `remove` may leave the selected default invalid; ordinary startup then returns the existing no-provider/default recovery error. OIDC protected credential record identity is preserved under the new configuration namespace; no record rehome is planned. No new session event or snapshot field is planned.
- **Security / authority:** Preserve operator-tier-only provider configuration; secrets never enter argv, output, diagnostics, prompts, temp filenames, or errors. API keys retain their documented environment/file precedence and their scriptable `credential_store.api_key.file` input. Mecatl-owned OIDC remains encrypted, identity-bound, embedded-Mecatui-only for interactive enrollment, and deployment-scoped; `credential_store.oidc` configures that shared store without containing secrets. ToolHive credentials/lifecycle remain isolated. Linux and macOS writes retain private ownership/mode and symlink/special-file checks, same-directory atomic replacement, cancellation, pre-rename changed-target detection, and truthful failure reporting, but do not take/retry a cooperative sidecar lock. Ctrl-C prints `Cancelled; no changes made.` and exits 130.
- **Compatibility / migration:** Breaking change by decision: reject any legacy `llm:` configuration, including `endpoints`, `credential_home`, and `credential_key`, at startup with migration guidance. Remove `mecatui llm` forms and `--auth-file` immediately, with replacement functionality available under `mecatui providers` and `--api-key-file`; no aliases or automatic configuration/credential rewrite.

## In scope — 5 scenarios, in implementation order

### Scenario 1 — one provider schema represents custom transport and authentication

The runtime already normalizes `llm.endpoints` into the provider registry. This scenario replaces the split input model without changing the composition-owned registry boundary described in [provider architecture](../architecture/providers.md) and preserves the strict parser/custody posture from [ADR 0238](../adr/0238-operator-defined-llm-providers.md) and [ADR 0329](../adr/0329-native-llm-endpoint-gateway-credentials.md).

**Acceptance:**
- AC1.1: a custom provider definition has one stable provider name, transport/API flavor, default model, and one explicitly selected authentication method (`api_key`, `oidc`, or `none`); stock-provider transport and authentication capabilities are described through the same provider-facing model.
  - verify: `TestProviderUnification_Scenario1_CustomProviderAuthSchema`
- AC1.2: an OIDC provider uses `auth.method: oidc` and the exact `auth.oidc` mapping `{issuer, client_id, scopes, resource_audience?, issuer_trust, gateway_trust}`; `issuer_trust` and `gateway_trust` are independent required mappings. Shared store configuration is exactly `credential_store.oidc.home` and `credential_store.oidc.key.{source,key_env}`. Any legacy `llm:` mapping and malformed, ambiguous, unknown, or incomplete unified input fail closed without treating it as an API-key/no-auth provider.
  - verify: `TestProviderUnification_Scenario1_OIDCProviderSchemaFailsClosed`
- AC1.3: a provider name cannot collide with a stock provider or another configured provider; a persisted selector for a removed/renamed provider fails loudly rather than falling back.
  - verify: `TestProviderUnification_Scenario1_ProviderIdentityCollisionAndSelectorFailure`

### Scenario 2 — provider lifecycle is chosen by provider capability

Mecatui exposes a provider name, not a “native endpoint.” It applies API-key and OIDC custody without conflating definition deletion with credential removal, and it leaves ToolHive's external lifecycle intact, preserving the lifecycle boundaries in [ADR 0329](../adr/0329-native-llm-endpoint-gateway-credentials.md).

**Acceptance:**
- AC2.1: `mecatui providers login PROVIDER` preserves the provider definition and either obtains/replaces an API key through hidden terminal input or runs Mecatl-owned OIDC enrollment according to the selected authentication method; a provider supporting multiple methods presents the configured/selected method clearly before mutation.
  - verify: `TestProviderUnification_Scenario2_LoginUsesSelectedAuthentication`
- AC2.2: `mecatui providers logout PROVIDER` clears only locally managed credentials while preserving the provider definition; API-key removal never claims upstream revocation, and OIDC logout preserves the existing local-delete-before-best-effort-revocation contract.
  - verify: `TestProviderUnification_Scenario2_LogoutPreservesProvider`
- AC2.3: `mecatui providers remove PROVIDER` deletes a custom definition and its Mecatl-owned local credentials only after one explicit combined confirmation; it rejects stock providers. It permits removal of the selected default, leaving ordinary startup to report the no-provider/default recovery error.
  - verify: `TestProviderUnification_Scenario2_RemoveDefinitionAndCredentials`
- AC2.4: `mecatui providers add PROVIDER` interactively collects the custom definition (base URL, API flavor, default model, and selected authentication method; OIDC additionally collects issuer/client/scopes/audience/trust) and creates it and invokes its appropriate `login` flow by default; `--no-login` creates only the definition and prints the exact next login command.
  - verify: `TestProviderUnification_Scenario2_AddChainsLoginUnlessOptedOut`
- AC2.5: ToolHive remains visible as a provider but reports the owning ToolHive lifecycle/handoff rather than copying, migrating, or exposing its credentials.
  - verify: `TestProviderUnification_Scenario2_ToolHiveLifecycleIsolation`

### Scenario 3 — provider discovery, status, setup, and help are coherent

The CLI must teach the provider model rather than require users to infer it from flags. This replaces the prototype's terse `llm` help and typed provider-name menu with a dedicated command family while retaining the composition-owned provider boundary in [provider architecture](../architecture/providers.md).

**Acceptance:**
- AC3.1: bare `mecatui providers` returns a passive aggregate status plus next steps, while `mecatui providers --help` and every documented child command return successful dedicated help explaining provider classes, auth/custody modes, embedded-versus-remote behavior, command-specific side effects, and the no-provider recovery path.
  - verify: `TestProviderUnification_Scenario3_ProviderHelpHierarchy`
- AC3.2: bare `providers` and `status PROVIDER` report deterministic local provider/configuration/enrollment facts without network requests, browser launch, credential-store creation, paid inference, key values, or fingerprints.
  - verify: `TestProviderUnification_Scenario3_AC32_StatusIsPassiveAndNeverPrintsSecrets`
- AC3.3: `mecatui providers setup [PROVIDER]` is a convenience wizard rather than the only configuration mechanism; with no provider argument it offers a numbered, capability-labelled provider selection, uses structured whitespace/output, supports Ctrl-C during all prompts including secret entry, prints `Cancelled; no changes made.`, exits 130, and makes no mutation after cancellation.
  - verify: `TestProviderUnification_Scenario3_AC33_SetupMenuCancelsBeforeMutation`
- AC3.4: starting embedded Mecatui with no configured usable provider reports both the direct setup recovery and remote-server alternative; it never launches setup automatically. Remote `mecatui connect ADDRESS` remains governed by the remote server and does not claim local provider configuration applies.
  - verify: `TestProviderUnification_Scenario3_AC34_NoProviderRecoveryIsLocalOnly`

### Scenario 4 — provider defaults and credentials retain existing precedence

Provider configuration must not invent a second registry, reimplement composition selection, or weaken custody. This scenario preserves the meaningful product behavior behind the vocabulary change and the credential separation in [ADR 0238](../adr/0238-operator-defined-llm-providers.md).

**Acceptance:**
- AC4.1: `mecatui providers set-default PROVIDER [MODEL]` changes only the embedded deployment default and validates the provider/model through the same resolver used at normal startup; aliases and configured default models retain their existing behavior.
  - verify: `TestProviderUnification_Scenario4_DefaultUsesStartupResolver`
- AC4.2: matching environment credentials retain precedence over API-key file entries for stock providers; custom API-key providers retain their defined source rules; status truthfully reports shadowing without values.
  - verify: `TestProviderUnification_Scenario4_CredentialPrecedenceAndSafeStatus`
- AC4.3: API keys remain in `credential_store.api_key.file` (default `auth.yaml`), which temporarily retains the experimental OpenAI Codex manual-token record; protected OIDC credentials, ToolHive credentials, and the Codex route are never copied, migrated, or used as fallbacks for one another.
  - verify: `TestProviderUnification_Scenario4_CredentialStoreIsolation`

### Scenario 5 — safe local setup works on Linux and macOS

The local writer must be portable for the two primary desktop Unix platforms without replacing important credential-file protections with convenience. PR #1441's Linux-only writer is a prototype whose security properties and complexity are superseded by [ADR 0333](../adr/0333-unified-provider-configuration-and-mecatui-provider-commands.md).

**Acceptance:**
- AC5.1: API-key and default configuration operations function on both Linux and macOS with equivalent supported behavior; unsupported platforms fail before any write with manual configuration guidance.
  - verify: `TestProviderUnification_Scenario5_LinuxAndDarwinWriteSupport`
- AC5.2: before replacing an existing credential/configuration file, the writer validates private ownership/mode and rejects symlink or special-file targets; new secret files are private; same-directory atomic replacement and cancellation/error handling prevent partial-file writes. It takes no cooperative sidecar lock; instead, it aborts before rename when the target's final identity/content check differs from its initial snapshot.
  - verify: `TestProviderUnification_Scenario5_PortableSafeWrite`
- AC5.3: a changed-target abort says `Configuration changed while this command was running; no changes were made. Review the file and retry.` The writer reports only outcomes it can establish, preserves unrelated accepted configuration and credential records, and never logs or displays secrets. It does not claim universal concurrent-writer CAS or crash durability it cannot prove.
  - verify: `TestUpdateProviderMap_CreateOnlyConflict`, `TestUpdateProviderMap_RemoveExpectedDefinitionConflict`,
    `TestUpdateProviderMap_RollbackStoreMismatch`, and `TestProviderCredentialLoginReplacesOnlyCustomAPIKey`

## Out of scope

| Item | Defer-to | Decision |
|---|---|---|
| Remote client enrollment/configuration RPC | Later provider-management design | Local operator CLI only; preserve server ownership boundaries. |
| Model-facing provider/credential tools | Later explicit authority design | Credentials must not become model-visible or prompt-driven. |
| OpenRouter OAuth/key minting, Gemini onboarding, and arbitrary consumer subscription flows | Provider-specific future work | Each requires independent authentication/custody contract. |
| ToolHive lifecycle redesign | ToolHive-specific work | Preserve ToolHive ownership and credential isolation. |
| Cobra migration | Reassess after command contract is stable | Framework is not a substitute for command/domain design. |
| Automatic migration/rewrite of existing configuration or secret records | Compatibility amendment if selected | Never rewrite operator config/credentials merely by inspection. |

## Definition of done

1. The final ADR number/path, all Human decisions, and compatibility outcome are resolved; the plan is amended to `proposed` and linked from `docs/acceptance/README.md`.
2. Applicable `task lint`, `task test`, `task docs`, and `task api:check` gates pass.
3. `task ac-trace-strict` resolves every named proof when the plan becomes `landed`.
4. `go run ./cmd/mecademo` remains green for runtime changes.
5. The implementation PR links the merged Plan / Interface PR and approved commit and reports interface conformance.
6. `/panel-review` reports no ship blockers or unwaived reviewer failures.

## Deferred decisions and known risks

- OIDC discovery/DCR from only a provider URL is future work. It must populate the same strict provider configuration and retain the existing protected-store identity boundary; it is not silently introduced by this change.
- macOS write support must have real platform tests or equivalent trusted platform coverage. Compile-only coverage is insufficient for secret-file mutation behavior.
