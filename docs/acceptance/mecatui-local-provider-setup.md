# Mecatui local provider setup — acceptance plan

**Contract:** human-reviewed/v2
**Work classification:** Architectural — guided credential persistence establishes a durable operator-custody, partial-commit, and security contract across the CLI, `auth.yaml`, and provider defaults.
**Decision record:** [ADR 0332](../adr/0332-mecatui-local-provider-enrollment.md)
**Phase:** first usable local provider setup and passive status
**Status:** proposed, 2026-09-12. High-level direction, custody, and split sequencing were approved by the directing human; the exact contract below remains subject to Plan / Interface review.
**Delivery:** Split. Credential-writing and partial-commit behavior require contract review before implementation.
**Expected tasks:** deferred to orchestration

This slice makes a fresh local `mecatui` installation usable without hand-editing YAML. A
line-oriented `mecatui llm setup` flow enrolls an already-supported keyed provider, can select
its starting model and deployment default, and persists only operator-confirmed fields. The
existing `mecatui llm status [ENDPOINT]` command becomes a passive aggregate when no endpoint
is named while preserving endpoint-specific native and ToolHive behavior. The provider engine,
adapters, and model-selection pipeline remain composition-owned as described by
[provider architecture](../architecture/providers.md).

## Human decisions

- [x] Use a guided, line-oriented local mecatui provider flow while retaining endpoint-specific native behavior and ToolHive compatibility. — Decision: add only `mecatui llm setup` and enrich the existing `mecatui llm status`; keep bare `mecatui llm login` as the ToolHive alias.
- [x] Keep current credential custody and precedence. — Decision: API keys remain owner-only plaintext in `auth.yaml`, environment variables retain their exact current precedence (including OpenRouter's existing convention), custom providers remain file-only, and native OIDC records remain identity-bound and encrypted with no plaintext fallback.
- [x] Deliver the first usable local setup/status and safe-persistence slice before broader onboarding. — Decision: use Split delivery; defer first-run offers, OpenRouter OAuth, Gemini, and proprietary consumer sign-in until this plan is merged and implemented.

## Interface contract

- **gRPC / protobuf:** None — setup and status are local operator CLI operations; no remote enrollment RPC, message, field, or wire status is added.
- **Exported Go APIs / interfaces:** `internal/adapter/authfile` adds `type APIKeyUpdate struct { Provider string; APIKey *string }`, where non-nil sets/replaces and nil removes only the named provider's `api_key`; `type CommitState string` with stable values `CommitNoop = "no_op"`, `CommitNotApplied = "not_applied"`, `CommitDurable = "durable"`, and `CommitReplacementAppliedDurabilityUnknown = "replacement_applied_durability_unknown"`; and `func UpdateAPIKey(ctx context.Context, path string, update APIKeyUpdate) (CommitState, error)`. `CommitNoop` and `CommitDurable` return nil; every failure before successful rename, including cancellation, lock timeout, validation failure, or target re-read mismatch, returns `CommitNotApplied` with an error; a failure after successful rename returns `CommitReplacementAppliedDurabilityUnknown` with an error. The caller must stop on either error-bearing state. The settings writer adopts the same states. These are root-internal APIs, not engine compatibility surface; no `engine/`, provider-module, or public embedding API changes.
- **Tool schemas:** None — setup is operator-driven CLI code and no model-facing tool or prompt affordance is introduced.
- **CLI / config:** Add `mecatui llm setup [--auth-file PATH]`; enrich no-argument `mecatui llm status` with optional `--auth-file PATH`. `status --auth-file PATH ENDPOINT` parses the option before or after the single endpoint, but rejects it for endpoint-specific native or ToolHive status because those stores do not consume `auth.yaml`; duplicate options, unknown options, or excess positionals are usage errors. `setup` accepts no positional argument or remote target. It requires both terminal stdin and terminal stdout and offers `Add or replace provider`, `Choose default`, `Remove saved key`, or `Exit`. Key mutation is closed to `openai`, `anthropic`, `openrouter`, `opencode`, and already-configured `providers:` entries whose resolved `auth.method` is exactly `api_key`; the writer also validates provider-ID syntax. `auth.method: none`, configured native `llm.endpoints`, ToolHive, `openai-codex`, and all OAuth-shaped records are excluded from API-key mutation and preserved byte-for-byte outside the targeted scalar. A native selection is an explicit-consent handoff to the exact existing in-process host operation behind `mecatui llm login ENDPOINT`, not merely printed instructions and never a subprocess; an active `--auth-file` override rejects that handoff with a command to run native login separately, so the plaintext file cannot be mistaken for or carried into the encrypted store. ToolHive remains an instruction-only handoff to its existing lifecycle.
- **Events / persistence:** Add/replace orders credential first, then independently confirmed settings. Removal that would strand the selected default is the explicit exception: the confirmed replacement settings commit must be `CommitDurable` first, then key removal; direct removal is allowed when it does not strand the default. There is no portable two-file transaction. Reports name the sanitized provider and operation and project each operation's returned state: no-op; invocation did not commit; durable; or replacement applied with crash durability unknown. They never generalize one operation into “the default is unchanged,” claim observed post-error content, retry, or roll back. Any mismatch or durability ambiguity stops all subsequent mutation and optional startup. The remedy is passive `status`/re-read after the operator resolves the filesystem condition. If removal fails after a durable replacement-default commit, the report states that the default moved and the old key may remain; if removal is durability-unknown, it states that the replacement default is durable and key removal may have applied. Settings mutation changes only `models.default_provider` and `models.default`, preserving unrelated mappings.
- **Security / authority:** Setup is local operator authority only and never drives remote enrollment. Before hidden entry it prints static provider-console guidance, states that an API/developer key—not a consumer subscription—is required, discloses owner-only plaintext readability by same-UID processes and agent Shell, and warns that API usage may incur charges; it never opens a browser for API-key setup. It uses the existing `golang.org/x/term.ReadPassword` terminal-state implementation, which restores echo on every return supported by that API. Acquisition itself is honestly library-bounded only by available memory; setup rejects an accepted key over 8 KiB immediately after the read and before confirmation or mutation. Key values never enter argv, output, diagnostics, prompts, temp names, or operator-visible/log error text; synthetic test sentinels may be retained only inside fixtures used to prove non-disclosure. Exit/menu cancellation and declined confirmations return success with no new mutation; no-TTY, EOF, signal cancellation, terminal read failure, oversized input, and usage errors return non-zero with no new mutation before confirmation.
- **Compatibility / migration:** Startup credential precedence and alias/model resolution remain the existing `internal/cliconfig` and composition behavior; setup does not reimplement them. OpenRouter retains its own-key order and environment-only `OPENAI_API_KEY` fallback; custom IDs gain no environment inference. Bare `mecatui llm login` remains the ToolHive alias and existing `login/status/logout ENDPOINT` native lifecycle remains available. Missing conventional `auth.yaml` remains ordinary absence. An explicit missing `--auth-file` is distinguished in status and, in setup, may be created only after a path-specific confirmation when its existing parent passes the credential-directory gate; malformed or unreadable files never degrade to missing. Optional startup carries the selected auth path into the ordinary embedded startup, which re-reads normal composition state and resolves the saved default/aliases through the actual resolver; it does not reuse a stale wizard snapshot or silently fall back to the conventional auth path. Existing valid files are not rewritten by inspection. The startup no-provider error gains only `run mecatui llm setup`; it never invokes setup automatically.

## In scope — 4 scenarios, in implementation order

### Scenario 1 — status reports configuration and credential facts without pretending health

No-argument status combines existing operator provider definitions, effective credential sources,
configured native endpoints, ToolHive detection, and model defaults without constructing the
provider registry. It follows `internal/cliconfig/cliconfig.go` (`ProviderFlags.resolve`) rather
than recreating precedence and follows the native passive-status rule in
[ADR 0329](../adr/0329-native-llm-endpoint-gateway-credentials.md).

**Acceptance:**
- AC1.1: aggregate status is human-oriented and deterministic: sections are fixed, provider rows are provider-ID sorted, and each keyed row reports sanitized provider ID, configured/builtin state, effective credential source, shadowed file-source presence without values or fingerprints, selected-default state, model selector, and `verification: not checked`. `none required`, native enrollment, and ToolHive are separate classifications rather than writable credential rows; OpenRouter exposes its established source precedence without probing a mythical single-endpoint keyed status.
  - verify: `TestMecatuiLocalProviderSetup_Scenario1_PassiveAggregateStatus`
- AC1.2: aggregate status performs zero network calls, refreshes, browser launches, `app.Build` calls, paid inference, or credential-store initialization; native rows direct the operator to endpoint-specific status for the existing local-store result.
  - verify: `TestMecatuiLocalProviderSetup_Scenario1_StatusHasNoActiveSideEffects`
- AC1.3: endpoint-specific native and ToolHive status retain existing target semantics; `--auth-file` is rejected rather than ignored there. Missing explicit, missing conventional, malformed, and unreadable auth-file states remain distinct. Output contains no credential value and dynamic provider/path/model metadata uses existing sanitized, control-sequence-free projections.
  - verify: `TestMecatuiLocalProviderSetup_Scenario1_EndpointCompatibility`

### Scenario 2 — a newcomer confirms custody, credential, model, and start separately

The flow reuses supported definitions and defaults rather than creating another registry. API-key
custody follows [ADR 0332](../adr/0332-mecatui-local-provider-enrollment.md) and the documented
same-UID boundary in [provider credential documentation](https://mecatl.dev/docs/building/deployment/settings#configure-provider-credentials).

**Acceptance:**
- AC2.1: Add/replace presents only the four keyed built-ins and configured custom providers whose effective auth method is `api_key`. Native selection requires consent before calling the existing host login operation, ToolHive points to its lifecycle, and neither path can mutate `auth.yaml`; `none`, `openai-codex`, arbitrary custom creation, and proprietary consumer login are not key choices.
  - verify: `TestMecatuiLocalProviderSetup_Scenario2_ProviderChoicesAndLifecycleHandoffs`
- AC2.2: both input and prompt output must be terminals. Before entry, setup prints the static console link and API-key/subscription/cost/custody disclosures. Hidden input is no-echo, accepted at no more than 8 KiB, echo-restored on every terminal outcome, and absent from every operator-visible/log projection; all pre-confirmation failure/cancellation paths leave files untouched.
  - verify: `TestInvariant_mecatui_setup_secret_never_observable`
- AC2.3: existing environment or matching file credentials are reusable with `CommitNoop`; custom providers never infer environment variables, replacing a shadowed built-in file key warns without values, and unrelated OAuth records survive targeted writes.
  - verify: `TestMecatuiLocalProviderSetup_Scenario2_ExistingCredentialPrecedence`
- AC2.4: built-ins show the existing default plus at most four stable-ID-sorted catalog suggestions with unchanged bounded friendly metadata. The manual non-empty selector is always available and labelled unverified, including when catalog suggestions exist or are stale. Native/custom defaults are not claimed tool-capable. Input aliases are resolved by the actual resolver before a default plan is confirmed. Credential/default confirmations remain separate, and explicit `y` startup re-reads the chosen auth file and normal settings through ordinary composition with no network verification.
  - verify: `TestMecatuiLocalProviderSetup_Scenario2_IndependentConfirmationsAndStart`

### Scenario 3 — API-key persistence is narrow, preserving, and fail-closed

The writer sits beside the bounded strict reader in `internal/adapter/authfile/authfile.go` and
borrows focused lock/AST/root-handle patterns already used by mecatl; it does not create a generic
YAML-writing or filesystem-containment framework. This is the narrow writer and residual boundary
proposed by [ADR 0332](../adr/0332-mecatui-local-provider-enrollment.md).

**Acceptance:**
- AC3.1: `UpdateAPIKey` accepts at most the reader's 16 KiB document, validates provider-ID syntax, preserves comments, unrelated accepted schema, and OAuth-shaped records, and changes only one eligible caller-selected provider's `api_key`; malformed YAML, duplicates, aliases/ambiguous anchors, unknown schema, ambiguous target mapping, or oversized output returns `CommitNotApplied` before rename.
  - verify: `TestADR_0332_AuthFileTargetedPreservation`
- AC3.2: canonicalization accepts a conventional home reached through a platform alias such as `/home` → `/var/home`; ordinary existing ancestors (including root-owned `/` and `/home`) need not be current-UID `0700`. The canonical credential parent itself must be a current-UID, non-link directory at `0700`, and auth/lock leaves must be current-UID, non-link regular files at `0600`. Operations are descriptor-anchored to that canonical parent with no-follow leaf opens. The conventional `mecatl` parent may be created at `0700` beneath the existing canonical user config directory; an explicit auth path may create only its missing leaf after confirmation and never recursively creates its parent. No existing mode is tightened and no backup is made. Platforms unable to prove these properties reject mutation.
  - verify: `TestADR_0332_AuthFileOwnershipAndModeGate`
- AC3.3: the context-cancelable cooperative lock has a five-second maximum wait. Under lock the writer re-reads latest bytes, records target identity/content, writes and syncs a same-directory `0600` temp, then immediately before rename re-reads and compares target identity/content. Mismatch or inability to compare returns `CommitNotApplied`; successful rename followed by successful directory sync returns `CommitDurable`; directory-sync/close failure after rename returns `CommitReplacementAppliedDurabilityUnknown`. The guard detects outside changes only through that comparison and is not CAS against arbitrary POSIX writers; a same-UID non-cooperator can still race after comparison and before rename. No universal race-free claim or API is introduced.
  - verify: `TestADR_0332_AuthFileCommitProtocol`
- AC3.4: preflight resolves auth and settings to canonical physical files and rejects equality before prompting. `CommitNoop` covers exact-value reuse and absent-key removal without rewriting. Every state/error pair is tested, errors contain operation/provider facts but no secret or unsanitized control sequence, and no backup exists.
  - verify: `TestMecatuiLocalProviderSetup_Scenario3_UnchangedAndSecretSafe`

### Scenario 4 — partial commits and removal remain truthful

Settings retain the preserving writer and exact `models.default_provider` / `models.default`
distinction in `internal/adapter/permconfig/schema.go` (`ModelsSection`). This slice adds the same
outcome vocabulary and directory durability without replacing the models tree, consistent with
[ADR 0332](../adr/0332-mecatui-local-provider-enrollment.md).

**Acceptance:**
- AC4.1: add/replace preflights both documents, commits approved key first, and attempts settings only after `CommitDurable`/`CommitNoop`; every pre/post-rename injected fault produces the exact state/report, and ambiguity stops startup and later writes with passive status as the remedy.
  - verify: `TestMecatuiLocalProviderSetup_Scenario4_TruthfulPartialCommit`
- AC4.2: default mutation preserves aliases, slots, router, allowlist, OpenRouter routes, comments, and unrelated root keys while changing only the two intended scalars coherently; target mismatch is not-applied and post-rename sync failure is replacement-applied/durability-unknown, never “unchanged.”
  - verify: `TestMecatuiLocalProviderSetup_Scenario4_NarrowDefaultMutation`
- AC4.3: removal explains delete-is-not-revoke and reports a remaining environment source. If removal would strand the active default, replacement settings must become durable before key deletion. Tests cover replacement failure/no write, replacement durable plus removal not-applied, and replacement durable plus removal durability-unknown without retry or rollback.
  - verify: `TestMecatuiLocalProviderSetup_Scenario4_RemoveWithoutSilentDefaultReset`
- AC4.4: the no-provider startup error points to setup, but startup never invokes the interactive flow and connect mode remains remote-authoritative.
  - verify: `TestMecatuiLocalProviderSetup_Scenario4_StartupPointerOnly`

## Out of scope

| Item | Defer-to | Decision |
|---|---|---|
| Automatic first-run setup offer | subsequent onboarding slice | Keep this slice explicit; startup gains only an error pointer. |
| OpenRouter OAuth/key minting | provider-specific slice with its own PKCE, callback, and copy-code security gates | This slice supports the existing API-key convention only. |
| Gemini novice choice or adapter changes | compatibility slice after Chat Completions thought-signature replay coverage | Separate from OpenRouter enrollment; do not advertise static model IDs before replay semantics are proven. |
| Claude.ai, ChatGPT, or other proprietary consumer sign-in | not planned | Anthropic/API OpenAI use developer credentials; native `openai-codex` login already exists but is not an API-key record. |
| Creating arbitrary custom provider definitions | later guided advanced configuration slice | Setup activates only accepted `providers:` definitions; it does not invent protocol/auth choices. |
| Active provider/model health or model-list checks | later optional verification slice | This slice reports local facts as unverified and performs no paid/network inference. |
| Remote enrollment, TUI secret dialogs, or LLM-driven setup | separate decisions | Local line-oriented operator CLI only. |
| Generic YAML transaction framework, arbitrary filesystem containment, or portable two-file atomicity | not planned | Two narrow writers, descriptor-anchored credential leaves, and truthful ordered partial commits are the contract. |

## Definition of done

1. `task lint`, `task test`, `task docs`, and `task api:check` pass for implementation.
2. `task ac-trace-strict` resolves every named proof when the plan becomes `landed`.
3. `go run ./cmd/mecademo` remains green.
4. The owning canonical `user-docs/` provider/setup page is updated in the implementation PR and `task site:build` passes.
5. The implementation PR links the merged Plan / Interface PR and approved commit and reports interface conformance.
6. `/panel-review` reports no ship blockers or unwaived reviewer failures.

## Deferred decisions and known risks

- The cooperative lock and pre-rename comparison do not exclude arbitrary same-UID/POSIX writers; the final comparison-to-rename race is an inherited local-filesystem boundary accepted only when the Plan / Interface PR merges.
- A successful rename with failed directory sync leaves replacement content possibly visible but crash durability unknown. The operator must inspect passive status rather than rely on a claimed re-read or automatic retry.
- Status cannot prove a credential is accepted without network activity. `not checked` is intentional.
- Provider model catalogs can be stale or unavailable; manual selection remains available and is a selector, not an authentication or tool-capability test.
