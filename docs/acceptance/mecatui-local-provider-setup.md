# Mecatui local provider setup — acceptance plan

**Contract:** human-reviewed/v2
**Work classification:** Bounded — completes provider-command UX and repairs implementation gaps within the already merged provider, credential-custody, and portable-writer contract; no new durable architecture decision.
**Decision record:** None — ADR 0333 and its merged acceptance plan already own the CLI, schema, custody, platform, and concurrency decisions. This follow-up reuses those boundaries rather than reviving the prototype.
**Phase:** provider setup/status/default follow-up after PR #1448
**Status:** proposed, 2026-09-14. The directing human authorized this reconciliation and stacked preparation before plan merge; PR #1440 remains the human Plan / Interface gate, not an already merged approval.
**Delivery:** Split. Review the remaining behavior in existing Plan PR #1440; prepare it in existing draft Implementation PR #1441.
**Expected tasks:** deferred to orchestration
**Plan PR:** [stacklok/mecatl#1440](https://github.com/stacklok/mecatl/pull/1440)

[PR #1448](https://github.com/stacklok/mecatl/pull/1448), merged at
`fdbd369c50c804ee391457a666d7471f53ec6069`, is the implementation baseline.
[ADR 0333](../adr/0333-unified-provider-configuration-and-mecatui-provider-commands.md)
and the [unified-provider contract](unified-provider-configuration-and-mecatui-provider-commands.md)
supersede the unmerged ADR 0332/prototype. Their authority follows merged ancestry even where
historical metadata still says proposed. This plan replaces its earlier proposal in full;
it is not permission to port the old `setup.go`, Linux-only writers, or removal policy.

## Human decisions

- [x] Keep the unified provider architecture. — Decision: preserve the merged flat commands, schema, custody, Linux/macOS parity, and no-lock writer protocol; do not introduce another ADR or credential store.
- [x] Complete the explicit local journey only. — Decision: enrich existing setup, passive status, and default selection with API-console/custody guidance, explicit credential reuse, bounded hidden entry, and separate save/default consent; shared login also receives the key-entry protections. No automatic startup, first-run invocation, or nested action menu.
- [x] Reuse existing manual Codex credentials. — Decision: retain read-only local status/default reuse through the runtime loader; no login, refresh, import, token entry, removal, or entitlement claim for Codex.
- [x] Preserve the new lifecycle rather than the prototype. — Decision: add normally chains login; logout removes only credentials; remove deletes a custom definition and its managed credentials after combined confirmation, may remove the selected default, and never automatically selects a replacement.
- [x] Continue the two existing PRs without rewriting history. — Decision: the human explicitly permits stacking before plan merge for this work; that exception is not merged-plan approval or permission for an agent to merge either PR to main.

## Interface contract

- **gRPC / protobuf:** None — local operator commands add no remote enrollment surface, message, field, or protocol.
- **Exported Go APIs / interfaces:** None — preserve existing exported signatures, engine/provider-module APIs, and root-internal writer outcome types. Reuse `cliconfig.ResolveProviderCredentials`, `ProviderFlags` loading, `openaicodex.NewCredential`, `app.ResolveDeploymentDefault`, `authfile.UpdateAPIKey`, `permconfig.UpdateDefaults`, and the shared `privatefile` writer. Consolidate private credential validation/projection helpers where needed; do not create a second registry, credential loader, or CLI-only model validator. Any necessary exported signature change requires an explicit plan amendment first.
- **Tool schemas:** None — this is operator-driven, not a model-facing tool or prompt affordance.
- **CLI / config:** Keep exactly `mecatui providers`, `status [PROVIDER]`, `setup [PROVIDER]`, `add PROVIDER [flags] [--no-login]`, `login PROVIDER [--no-browser]`, `logout PROVIDER`, `set-default PROVIDER [MODEL]`, and `remove PROVIDER`. Setup retains numbered capability-labelled provider selection and existing custom-add/login composition; an available API credential offers explicit reuse or replacement, then a separately confirmed optional default action through the existing set-default path. Direct login is explicit credential replacement with save confirmation; direct set-default remains an explicit action, not a new wizard. Provider commands consume configured `credential_store.api_key.file`, not per-command file flags. Startup retains `--api-key-file`; `--auth-file` and every `llm` form stay removed. `providers.NAME.{base_url,default_model,api_flavor,auth}` and `provider_overrides` stay unchanged; custom OIDC is Responses-only under `auth.oidc`, using `credential_store.oidc.{home,key}`.
- **Events / persistence:** None — no new persisted schema, event, store, or outcome vocabulary. Preserve targeted accepted YAML and unrelated credentials, including OAuth records, and current commit states (`no_op`, `not_applied`, `durable`, `replacement_applied_durability_unknown`). Separate key/default operations are not a transaction; a later decline/error must not erase or misreport an earlier committed operation. No automatic retry or rollback after an ambiguous write.
- **Security / authority:** API-key entry uses standard hidden terminal input, followed by explicit save consent, and is rejected if empty before mutation. Explain API charges, consumer-versus-developer access, and owner-only plaintext readability by same-UID processes including permitted agent Shell. Never expose credentials in argv, output, diagnostics, errors, or filenames. Preserve encrypted identity-bound OIDC status/store inspection and ToolHive's external lifecycle. Repair demonstrated writer gaps only to meet ADR 0333's private ownership/mode, symlink/special-file rejection, same-directory replacement, changed-target detection, cancellation, and truthful durability requirements on Linux and macOS; do not reintroduce cooperative locks or universal POSIX CAS claims.
- **Compatibility / migration:** Additive UX and corrective resolution within PR #1448's contract, not another migration. Reject legacy `llm:` input without rewriting it. Matching built-in environment keys beat their file entries; OpenRouter uses its own environment key, then its own file key, then the composition-owned environment-only `OPENAI_API_KEY` fallback. An OpenAI file key is not an OpenRouter fallback; custom keys remain file-only. OIDC, ToolHive, API keys, and manual Codex records never become fallbacks for one another. Startup remains ordinary composition, and remote connect remains remote-authoritative.

## In scope — 4 scenarios, in implementation order

### Scenario 1 — passive status reports effective local facts

Reuse `cmd/mecatui/provider_status.go` and `internal/cliconfig/cliconfig.go`, retaining the
[provider architecture](../architecture/providers.md) authority boundary.

**Acceptance:**
- AC1.1: bare providers and named status render deterministic provider-ID-sorted local facts: provider class/auth method, effective credential provenance, shadowed file presence without values, explicit selected-default marker and configured model selector, and `verification: not checked`. Missing, malformed, and unreadable credential input are not conflated. API-key presence and no-auth configuration never become health or entitlement claims; unavailable selected providers remain discoverable as recovery state.
  - verify: `TestProviderSetupFollowup_Scenario1_StatusProvenance`
- AC1.2: production loading, status, and default validation agree on built-in precedence, OpenRouter's environment-only fallback, custom file-only credentials, and the configured file path. Tests cover shadowing and an OpenAI file key that must not activate OpenRouter. Project configuration cannot redirect custody.
  - verify: `TestProviderSetupFollowup_Scenario1_RuntimeCredentialParity`
- AC1.3: status performs no network requests, refresh, enrollment, browser launch, paid inference, or `app.Build`; preserve the existing OIDC local-store inspection behavior, including unavailable/expired states, without creating a store. ToolHive reflects actual passive configuration intent and external ownership, not assumed authentication. Dynamic output is bounded and control-sequence-safe with no secrets or fingerprints.
  - verify: `TestProviderSetupFollowup_Scenario1_PassiveBoundaries`

### Scenario 2 — guided API-key entry teaches custody and requires consent

Extend the existing `cmd/mecatui/provider_setup.go` and shared key-entry path in
`cmd/mecatui/provider_credential.go`, not the superseded prototype command tree. Preserve
[ADR 0333](../adr/0333-unified-provider-configuration-and-mecatui-provider-commands.md)'s
local operator authority and credential-custody boundaries.

**Acceptance:**
- AC2.1: before hidden entry, supported stock providers present static provider-specific console guidance, API/developer-versus-consumer subscription distinctions, billing and plaintext-custody disclosures. OpenCode identifies the configured Go integration and does not imply a Zen key/subscription or endpoint is interchangeable. Custom providers direct users to their operator/service documentation without guessing a console or changing transport. No browser opens for API-key entry.
  - verify: `TestProviderSetupFollowup_Scenario2_ConsoleAndCustodyGuidance`
- AC2.2: setup explicitly offers reuse of an effective existing credential versus replacement. Reuse never writes or copies an environment credential into the file. Replacement warns when an environment credential will still win, obtains hidden input, rejects blank values, and separately confirms saving before invoking the shared writer. Default selection is independently optional and confirmed; there is no launch step or automatic startup. OIDC/add/no-auth/ToolHive retain their current capability-specific direct actions, not an API-key prompt.
  - verify: `TestProviderSetupFollowup_Scenario2_ReuseAndIndependentConsent`
- AC2.3: hidden API-key entry uses the standard terminal password reader; API-key prompts remain hidden and reader failures are reported without exposing entered values. Ctrl-C follows normal terminal behavior. If an earlier step remains committed, report that fact instead of claiming whole-command rollback; retain add's existing cancellation cleanup when it can establish restoration. Secret sentinels never appear in captured output/errors/diagnostics, including failure paths.
  - verify: `TestInvariant_ProviderSetupFollowup_SecretSafety`

### Scenario 3 — Codex reuse and model defaults share runtime authority

Use `cmd/mecatui/provider_default.go`, `internal/app/default_selection.go`, and the existing
manual credential validator described by [ADR 0215](../adr/0215-openai-subscription-manual-token.md).

**Acceptance:**
- AC3.1: a distinct OpenAI Codex manual-subscription row reports missing, locally usable, or invalid/expired state from the same configured-file loader and validator used by runtime. It discloses no token, account ID, expiry, or fingerprint. Existing locally usable credentials allow setup reuse/default selection without credential writes; other states offer manual guidance, never secret entry or OIDC login. No browser/device login, refresh, import, removal, or new environment alias is introduced.
  - verify: `TestProviderSetupFollowup_Scenario3_CodexLoaderAndReuse`
- AC3.2: setup and direct set-default use `app.ResolveDeploymentDefault` and ordinary startup validation. Resolve known aliases normally; a bare declared custom default must reach the shared model validator rather than fail prematurely as an unknown alias. Unknown/inherit/mismatched selectors still fail with no settings write. Codex has no invented static default or offline entitlement inventory: preserve an existing selector or require explicit model selection under the same validator. No model-health request is made.
  - verify: `TestProviderSetupFollowup_Scenario3_SharedDefaultResolution`

### Scenario 4 — preserve portable safe writes and lifecycle semantics

Audit and fix the shared `internal/adapter/privatefile/update_unix.go` implementation against
[the merged writer contract](unified-provider-configuration-and-mecatui-provider-commands.md)
and [ADR 0333](../adr/0333-unified-provider-configuration-and-mecatui-provider-commands.md),
not ADR 0332's superseded Linux/lock design.

**Acceptance:**
- AC4.1: Linux and macOS regressions cover unsafe symlink/special-file leaves (including FIFO substitution without blocking), canonical-parent replacement during mutation, private ownership/modes, same-directory temporary replacement, cancellation and final snapshot mismatch. Repair any demonstrated bypass of the existing checks at the shared writer; no write or cleanup may escape the validated parent via a substituted link. Conventional home aliases remain supported. No sidecar lock/retry protocol, recursive parent creation, or platform regression is introduced.
  - verify: `TestProviderSetupFollowup_Scenario4_PortablePrivateFileBoundary`
- AC4.2: injected pre/post-rename and parent-directory sync/close failures prove the existing outcome truthfulness, including durability of a newly created conventional parent. Preserve unrelated accepted settings/credential records and no-op bytes; errors disclose no secrets. An ambiguous replacement stops later mutation and directs the operator to passive inspection without claiming rollback or universally race-free CAS.
  - verify: `TestProviderSetupFollowup_Scenario4_TruthfulWriteOutcomes`
- AC4.3: integration guards keep add's default login handoff and `--no-login`, logout's credential-only action, remove's combined custom-definition/credential confirmation, and permitted selected-default removal without replacement. No old `llm` commands, per-command file flags, automatic setup/startup, or Linux-only prototype helpers survive in the implementation diff.
  - verify: `TestProviderSetupFollowup_Scenario4_UnifiedLifecyclePreserved`

## Out of scope

| Item | Defer-to | Decision |
|---|---|---|
| New provider schema, store, protocol, public engine API, or OIDC architecture | separate architectural proposal if needed | ADR 0333 remains authoritative. |
| Consumer sign-in, OpenRouter OAuth/key minting, Codex enrollment/refresh/import, Gemini onboarding | provider-specific future work | This follow-up only reuses existing local credentials. |
| Active health/model-list verification or paid inference | separately authorized verification work | Local facts stay explicitly unverified. |
| First-run setup, automatic startup, nested action menus, remote enrollment | later onboarding design | Existing explicit local commands only. |
| Cooperative locking, generic YAML transactions, arbitrary same-UID CAS | not planned | Retain the merged bounded writer protocol and documented residual race. |

## Definition of done

1. Plan checker, checker regressions, and `task docs` pass for this amendment; no runtime changes in the plan PR.
2. Implementation completes every new named proof through real command/factory paths; old prototype test names or prior review verdicts are not evidence for this contract.
3. `task lint`, `task test`, `task docs`, `task api:check`, `task ac-trace-strict`, and the offline demo pass for implementation; Linux and macOS writer execution requires real platform coverage, not compile-only claims.
4. Update the existing canonical provider/setup guidance in `user-docs/features/choose-models.md` and owning Mecatui pages; preserve current formatting and run `task site:build`.
5. Independent review and PR CI have no untriaged blockers. Record exact proposed/merged baseline honestly; both existing PR identities and human merge gates remain intact.

## Deferred decisions and known risks

- No material behavior decision remains open for this bounded slice; expansion beyond these interfaces stops for amendment.
- The final snapshot-to-rename race against arbitrary non-cooperating same-UID/POSIX writers remains; parent/leaf safety fixes must not advertise universal CAS.
- Passive credential validation cannot establish account entitlement or upstream acceptance.
