# Direct MCP onboarding — acceptance plan

**Contract:** human-reviewed/v2
**Work classification:** Architectural — this work changes durable operator CLI and configuration contracts, OAuth issuer trust bootstrap, local credential-key custody, and direct-DCR removal state.
**Decision record:** [ADR 0345](../adr/0345-direct-mcp-onboarding.md)
**Phase:** local direct-MCP onboarding and lifecycle management
**Status:** in-progress, 2026-09-16. Implementation is underway against the merged Plan / Interface baseline.
**Delivery:** Split. The command, configuration, credential, and trust-bootstrap contracts were human-reviewed before implementation began.
**Expected tasks:** deferred to orchestration
**Issue:** [stacklok/mecatl#1613](https://github.com/stacklok/mecatl/issues/1613).
**Plan PR:** [#1626](https://github.com/stacklok/mecatl/pull/1626).
**Approved baseline:** `b9f8cc5da3518e6e8eff726b769f5ebc894a1438`.

Make the currently supported direct-DCR profile a URL-first host operation. One attended command writes a validated operator profile, safely discovers and pins its sole issuer, selects durable credential custody without asking the operator to handle an encryption key, completes browser authorization, and verifies the MCP connection. Matching list, login, and removal commands replace routine YAML surgery.

This plan keeps global direct MCP configuration host-local. Session-scoped client MCP, running-daemon MCP inventory APIs, SDK process APIs, and broker authorization remain separate capabilities. Global changes affect newly started daemons; this plan adds neither hot reload nor a deployment-administration RPC.

## Human decisions

- [x] Scope the work to direct/global streaming-HTTP MCP and exclude the ToolHive broker. — Decision: preserve broker configuration, enrollment, authorization, and status behavior byte-for-byte.
- [x] Keep deployment administration out of `HarnessService`. — Decision: the canonical lifecycle is the host-local `mecated mcp` CLI over reusable root-internal Go operations; session ownership grants no authority to mutate operator MCP settings or credentials.
- [x] Remove issuer knowledge from the common path. — Decision: `mcp add` begins from the MCP resource URL, performs credential-free protected-resource discovery under an intentionally stricter same-resource-origin policy, validates and persists one exact issuer, and then hands off to the existing issuer-aware OAuth transport.
- [x] Keep keyring-first custody in the core work. — Decision: macOS and Linux local onboarding prefer native OS keyring custody for the encrypted store's wrapping key; an attended Linux host without Secret Service may affirmatively choose an owner-only generated key file. Existing environment-key custody remains available for headless and externally managed deployments.
- [x] Keep the existing direct-DCR scope and no-refresh contract. — Decision: onboarding v1 requires exactly `openid`, never requests or accepts `offline_access`, never registers a refresh grant, and rejects an issued refresh token. Generic MCP scope negotiation is a separate protocol-policy change.
- [x] Generate stable OAuth identity without extra flags. — Decision: preserve exact `NAME` for display, generate `profile` as its ASCII-lowercase form, and use literal `local-user` as `principal`. Advanced identities remain hand-authored YAML. Lifecycle record addressing retains the exact configured server name; no case-rename migration is promised.
- [x] Keep settings mutation narrow and atomic. — Decision: mutating commands accept one `--file`, defaulting to the conventional user settings file; mutation rejects any external source that supplies, wins, or would be shadowed for `mcp:`. Preserve semantics and unrelated comments outside the edited subtree, retain ordering/comments for existing MCP entries where the AST supports it, reject duplicate folded names, validate the complete result, recheck its version, and replace it privately and atomically.
- [x] Make removal recoverable without deleting shared key custody. — Decision: genuine lifecycle absence permits profile-only removal; ready DCR transitions through `removing` to a durable `removed` tombstone while deleting the current-generation grant and profile. Pending, uncertain, corrupt, mismatched, or key-unavailable state blocks removal. No force-forget or upstream revocation is claimed.
- [x] Approve the post-MoE minimal CLI cut. — Decision: v1 commands are `mecated mcp add NAME URL [--file PATH] [--credential-store auto|keyring|file]`, `mecated mcp list [--file PATH]`, existing `mecated mcp login NAME [--file PATH]` with its current browser/DCR recovery flags, and `mecated mcp remove NAME [--file PATH]`. Add logs in by default. Defer add-specific `--no-login`, `--no-browser`, `--issuer`, replacement, and machine-readable JSON.
- [x] Approve the post-MoE issuer/discovery cut. — Decision: v1 proceeds only when policy leaves exactly one advertised issuer. Zero or multiple issuers fail clearly without side effects; terminal issuer selection and an explicit issuer selector are deferred. For a pathful MCP resource, v1 intentionally omits the MCP origin-root metadata fallback because adopting the root document's RFC 9728 resource would change the frozen OAuth resource; report this as unsupported policy rather than claiming full MCP discovery conformance.
- [x] Approve the post-MoE SDK/output cut. — Decision: ship no SDK helper, no SDK scenario, and no versioned CLI JSON contract. Document direct CLI onboarding before local SDK `spawn()`; design machine output with a real SDK consumer later.
- [x] Approve legacy/native credential-domain coexistence. — Decision: existing `local.key_env` profiles retain the legacy `mecatl-mcp-oauth` namespace; native `local.key` profiles use `mecatl-mcp-oauth-native/v1`. Native markers bind namespace, backend, and a domain-separated key-locator digest. Commands load only the selected profile, never enumerate, copy, decrypt, or migrate legacy records, and preserve all legacy behavior. An unmarked native keyring account or file key is ambiguous and fails closed: it is never adopted, overwritten, deleted, or migrated.

## Interface contract

- **gRPC / protobuf:** None — `HarnessService` gains no direct-MCP lifecycle or administration RPC. Existing `CreateSessionRequest.mcp_servers` remains transient session-scoped client MCP; `ListMcpSources` and MCP resource/prompt RPCs continue to inspect only the running daemon generation. Broker authorization continuations remain broker-only.
- **Exported Go APIs / interfaces:** None — the importable `engine/` module and generated service interfaces do not change. Root-internal code may add bounded values for strict settings documents, bootstrap results, key custody, progress, and removal state while preserving the existing `credentialstore.Store` CAS interface.
- **Tool schemas:** None — onboarding is host administration, not a model-facing tool. Namespaced global MCP tools, `CallMcpWithQuery`, session client MCP tools, and broker tools retain their existing schemas and permissions.
- **CLI / config:** The v1 surface is `mecated mcp add NAME URL [--file PATH] [--credential-store auto|keyring|file]`, `mecated mcp list [--file PATH]`, `mecated mcp login NAME [--file PATH] [--no-browser] [--reset-dcr-registration|--retry-dcr-registration]`, and `mecated mcp remove NAME [--file PATH]`. Add always performs login and succeeds only after authenticated initialize and initial tool listing. `--file` selects one writable/readable settings document; login retains repeatable `--permission-config` as a deprecated read-only alias mutually exclusive with `--file`. Add rejects query-bearing resource URLs, requires one acceptable discovered issuer, emits direct DCR with exact `[openid]`, preserves exact display `NAME`, generates lowercase `profile` and literal `local-user`, and has no replace operation. New local profiles use `local: {root: ..., key: {mode: keyring}}` or `local: {root: ..., key: {mode: file, file: {path: ...}}}`; the strict `key` union rejects inactive arms. Existing `local: {root: ..., key_env: MECATL_*}` remains valid but cannot coexist with `key`. Defaults are `$XDG_STATE_HOME/mecatl/mcp-credentials` for ciphertext and `$XDG_CONFIG_HOME/mecatl/mcp-credential-key` for file custody.
- **Events / persistence:** No session event changes. Legacy `local.key_env` profiles retain the `mecatl-mcp-oauth` namespace. Native `local.key` profiles use the distinct `mecatl-mcp-oauth-native/v1` namespace, so a shared root and even a same-name legacy record cannot be opened under native custody. New root-scoped `mcp-credential-backend.json` is an owner-only strict document containing `version: 1`, `store_namespace: "mecatl-mcp-oauth-native/v1"`, `backend: keyring|file`, and `locator_sha256`, a domain-separated digest of namespace, backend, and deterministic keyring service/account or physical canonical file-key path. Every native profile sharing the root must match it before secret reads. A missing native marker with an existing keyring account or file key is ambiguous and fails closed; it is never adopted, overwritten, deleted, or migrated. Legacy profiles ignore native markers. Keyring service is `mecatl.mcp.oauth`; its account is derived from SHA-256 over `"mecatl/mcp/oauth-key/v1\x00"` plus the physical canonical root. File mode stores one canonical padded-base64 32-byte key in an owner-owned, single-link regular `0600` file. Direct-DCR lifecycle adds `removing` and terminal `removed` states. Ready removal CAS-transitions to `removing` while retaining identity, generation, and grant key; deletes the current-generation grant; CAS-transitions to `removed`; then removes the profile. The profile is never committed absent before `removed` is durable. Retry resumes `removing` idempotently and a retained profile with `removed` completes only the settings deletion; ambiguous grant, tombstone, and settings commits are reread. `removed` permits a later add to CAS-create a fresh pending generation. The backend marker and shared wrapping key are never removed. Settings mutation validates before a private same-directory file sync, atomic rename, and directory sync; a stale source version refuses commit.
- **Security / authority:** Only the local operator may mutate profiles, select custody, launch browser consent, or remove credentials. Bootstrap makes one initial no-redirect, credential-free request to the exact canonical query-free MCP endpoint and parses all `WWW-Authenticate` fields with RFC 9110 grammar. It selects one Bearer challenge atomically: `resource_metadata` and `scope` cannot be mixed across challenges; exact duplicates are allowed and conflicting acceptable challenges fail closed. A challenge metadata URL takes precedence; otherwise use endpoint-derived RFC 9728 metadata. V1 intentionally omits the MCP origin-root fallback for pathful resources because adopting the root document's RFC 9728 resource would change the frozen OAuth resource; this is reported as unsupported product policy, not full MCP discovery conformance. For a root resource, the root fallback remains valid. Metadata URLs must stay on the exact resource origin; valid cross-origin metadata is likewise unsupported by policy. Metadata GETs may follow only a bounded same-origin HTTPS redirect chain with per-hop public-answer screening and pinned dialing. The exact selected issuer is bound into state/PKCE. RFC 9207 `iss` is required when advertised, every present value is compared byte-for-byte before token exchange, and a mismatch suppresses provider error details. Authorization-server discovery order is RFC 8414 path insertion, OIDC path insertion, then OIDC path append for pathful issuers; root issuers use RFC 8414 then OIDC root. Only 404 advances; malformed or issuer-mismatched success, other status, oversized body, wrong content type, or policy-invalid redirect is terminal. OAuth protocol endpoints remain exact-issuer-origin only. Normal output never contains keys, tokens, client IDs, registration bodies, callback values, or authorization URLs; the existing explicit `mcp login --no-browser` channel remains the sole terminal URL exception. Linux non-TTY `auto` with absent Secret Service fails and instructs explicit `--credential-store=file`; it never silently creates file custody.
- **Compatibility / migration:** Existing hand-authored direct profiles, `key_env`, environment readers, preregistered clients, CIMD clients, direct-DCR `openid` records, login recovery flags, broker profiles, and session client MCP remain readable. ADR 0325's `openid`, no-refresh, registration-response-scope, and exact-name lifecycle rules remain authoritative. Legacy `key_env` profiles retain `mecatl-mcp-oauth`, remain marker-free, resolve only their explicit environment reference, and are never enumerated, copied, re-encrypted, or migrated. Native `key` profiles use `mecatl-mcp-oauth-native/v1`; an unrelated unavailable legacy key never prevents a command from loading a selected native profile, and the inverse also holds. No command silently adopts, replaces, deletes, or migrates an unmarked native key artifact. New lifecycle states extend ADR 0325 only for removal. Global changes affect newly started daemons; hot reload, Windows local onboarding, generic MCP scope negotiation, SDK helpers, machine-readable CLI output, and explicit legacy-record migration are deferred.

## In scope — 6 scenarios, in implementation order

### Scenario 1 — URL-first discovery establishes one exact issuer

The bootstrap extends direct DCR without weakening [ADR 0325](../adr/0325-direct-mcp-dcr.md). It implements the challenge and endpoint-derived portions of the current [MCP authorization discovery requirements](https://modelcontextprotocol.io/specification/2026-07-28/basic/authorization) and [RFC 9728 resource binding](https://www.rfc-editor.org/rfc/rfc9728.html#section-3.3), with documented stricter same-resource-origin, exact-issuer-origin, and pathful origin-root-fallback restrictions.

**Acceptance:**
- AC1.1: Bootstrap freezes one canonical query-free MCP resource, makes one initial credential-free no-redirect request to that endpoint, parses repeated/mixed `WWW-Authenticate` fields, and selects one Bearer challenge atomically. One same-origin `resource_metadata` value takes precedence; otherwise endpoint-derived RFC 9728 metadata is tried. Origin-root fallback is attempted for a root resource and intentionally reported unsupported for a pathful resource.
  - verify: `TestDirectMCPOnboarding_Scenario1_ChallengeAndFallbackOrder`
- AC1.2: Duplicate identical Bearer challenges are accepted; duplicate parameters, quoted-comma parser traps, conflicting metadata URLs or scope sets, insecure/userinfo/fragment/query metadata URLs, cross-origin metadata, and query-bearing MCP resources fail before authority expansion.
  - verify: `TestDirectMCPOnboarding_Scenario1_ChallengeParsingAndMetadataPolicy`
- AC1.3: Every metadata document returns a `resource` byte-for-byte equal to the frozen resource under the applicable RFC 9728 rule. The initial endpoint probe never redirects; metadata GETs remain same-origin HTTPS through the bounded per-hop DNS-screened pinned redirect path.
  - verify: `TestDirectMCPOnboarding_Scenario1_ExactResourceAndRedirectBinding`
- AC1.4: Exactly one advertised HTTPS issuer is required. Its authorization metadata follows the exact pathful/root discovery order; only 404 advances to the next candidate, while every malformed or policy-invalid response is terminal. Every accepted document contains the exact issuer and every registration/authorization/token endpoint remains on that issuer origin.
  - verify: `TestDirectMCPOnboarding_Scenario1_IssuerMetadataMatrix`
- AC1.5: The exact validated issuer is bound to state/PKCE. When metadata advertises authorization-response issuer support, callback `iss` is mandatory; every present `iss` is compared byte-for-byte before token exchange. Missing-required or mismatched issuer aborts without trusting or displaying provider error fields.
  - verify: `TestDirectMCPOnboarding_Scenario1_RFC9207Matrix`
- AC1.6: Missing, malformed, mismatched-resource, rebinding, private/mixed-answer, oversized, unsupported, zero/multiple-issuer, and terminal metadata failures leave settings, key custody, credentials, browser state, and upstream registration unchanged.
  - verify: `TestDirectMCPOnboarding_Scenario1_FailureHasNoSideEffects`
- AC1.7: DCR requests the existing exact public no-refresh profile. Omitted returned scope retains `[openid]`; a returned superset may add only AS-advertised scopes but additions never become authorization/grant authority. Incompatible registration metadata leaves `registration_response_invalid`. An unsolicited token-endpoint refresh token aborts grant persistence with the existing safe login failure while preserving the ready registration.
  - verify: `TestDirectMCPOnboarding_Scenario1_DCRResponseAndTokenMatrix`

### Scenario 2 — Keyring-first custody needs no operator-managed key

The encrypted store still receives an injected key under [ADR 0218](../adr/0218-credential-store.md). Onboarding adds host policy analogous to [ADR 0318](../adr/0318-headless-mecatui-credential-backend-selection.md) without using its plaintext clientauth backend.

**Acceptance:**
- AC2.1: macOS `auto` selects Keychain. Linux `auto` performs the existing bounded read-only no-autostart Secret Service ownership detection: present selects keyring; absence prompts on an attended TTY for file custody; decline, EOF, cancellation, and non-TTY fail without side effects and name explicit `--credential-store=file`; present-but-unusable keyring never falls back. Explicit file bypasses keyring.
  - verify: `TestDirectMCPOnboarding_Scenario2_PlatformSelectionMatrix`
- AC2.2: Selection writes the complete non-secret root marker before OAuth. Keyring creates/reuses the deterministic root account without displaying the key. File creates one random 32-byte key create-only at the canonical path and enforces owner, regular-file, single-link, exact `0600`, canonical base64, and `0700` root invariants.
  - verify: `TestDirectMCPOnboarding_Scenario2_PrivatePinnedCustody`
- AC2.3: Every later generated-profile operation checks marker backend and locator before secret reads. A changed key path, newly available service, unavailable selected service, unsafe marker, or concurrent conflicting first selection fails without probing, fallback, migration, or wrong-key store access.
  - verify: `TestDirectMCPOnboarding_Scenario2_NoFallbackOrLocatorDrift`
- AC2.4: Existing environment-key profiles remain marker-free and external. Missing external keys fail actionably and never generate local custody. Profile removal never deletes the root marker or a wrapping key shared with other or unenumerable records.
  - verify: `TestDirectMCPOnboarding_Scenario2_ExternalAndSharedCustody`

### Scenario 3 — Settings mutation is strict, narrow, and recoverable

The lifecycle extends [ADR 0225](../adr/0225-operator-settings-validation.md) without turning the general fail-soft runtime resolver into a writer.

**Acceptance:**
- AC3.1: Add/list/remove use one `--file` or conventional document. Login also accepts its deprecated repeatable `--permission-config` read sources. Add/remove report one writable target; list/login report every read source and the winning MCP origin. Mutation rejects any supplying, winning, or would-be-shadowed `mcp:` block outside the target, regardless of precedence direction.
  - verify: `TestDirectMCPOnboarding_Scenario3_ReadOriginsAndWriteTarget`
- AC3.2: Unreadable, malformed, duplicated, oversized, unsafe, or schema-invalid selected files fail with path and safe line/field detail; they never degrade to `server is not configured`.
  - verify: `TestDirectMCPOnboarding_Scenario3_ConfigErrorsNameBrokenLayer`
- AC3.3: Narrow AST mutation makes no semantic change outside `mcp:`, retains unrelated comments and existing MCP entry order/comments, validates the full candidate, detects stale source versions, and publishes through private same-directory sync and atomic replacement. Failure leaves the original semantic document and unrelated comments intact.
  - verify: `TestDirectMCPOnboarding_Scenario3_AtomicNarrowMutation`
- AC3.4: Add rejects duplicate folded names and has no replace or implicit merge. Operation order is read/version, credential-free discovery, lock/revalidate, idempotent custody selection, candidate validation/publication, then existing login. Failure before publication may leave only adoptable root custody; after registration may have started, profile and DCR evidence remain for safe retry.
  - verify: `TestDirectMCPOnboarding_Scenario3_AddOrderingAndResiduals`

### Scenario 4 — Add/login are observable and secret-free

Browser consent and DCR recovery remain host-owned under [ADR 0112](../adr/0112-mcp-oauth-loopback-runtime.md) and [ADR 0325](../adr/0325-direct-mcp-dcr.md).

**Acceptance:**
- AC4.1: Add always continues from validated publication into login and reports coarse redacted progress at settings, discovery, credential, browser-wait, and MCP-verification boundaries. It succeeds only after authenticated initialize and initial tool listing; exact prose and internal sub-stages are not compatibility contracts.
  - verify: `TestDirectMCPOnboarding_Scenario4_ProgressAndVerification`
- AC4.2: Browser add never prints the authorization URL. The existing explicit `mcp login --no-browser` writes it only to its dedicated host-owned channel. Progress, diagnostics, ordinary errors, and model-visible content remain free of secrets and opaque OAuth values.
  - verify: `TestDirectMCPOnboarding_Scenario4_OutputConfinement`
- AC4.3: Cancellation or failure closes every operation-owned response body, bootstrap transport idle pool, settings lock, OAuth runtime, temporary MCP server/controller, and credential handle in dependency-safe order. The borrowed Store remains open until the server/controller closes; no resource or browser child survives the command.
  - verify: `TestDirectMCPOnboarding_Scenario4_CleanupEveryTerminalPath`
- AC4.4: Registration uncertainty, response invalidity, ready persistence failure, callback failure, and token failure retain the existing DCR recovery category and never trigger implicit registration retry or rollback of a published profile.
  - verify: `TestDirectMCPOnboarding_Scenario4_DCRRecoveryPreserved`

### Scenario 5 — List is offline and non-presenting

Operator settings and credentials are host desired state; `ListMcpSources` remains running-generation truth under [ADR 0345](../adr/0345-direct-mcp-onboarding.md).

**Acceptance:**
- AC5.1: `mcp list` performs no discovery, refresh, registration, browser launch, MCP initialize, mutation, keyring autostart, unlock, or consent presentation. Non-presenting credential access failure returns `locked`, `unavailable`, or `unknown` rather than prompting.
  - verify: `TestDirectMCPOnboarding_Scenario5_ListIsOfflineAndNonPresenting`
- AC5.2: Each row contains only name, configured URL, auth/client kind, safe credential state (`login required`, `ready`, `recovery required`, `locked`, `unavailable`, or `unknown`), and settings source. One note states that changes affect newly started daemons and existing-daemon activation is unknown.
  - verify: `TestDirectMCPOnboarding_Scenario5_BoundedTruthfulStatus`

### Scenario 6 — Remove is monotonic and crash-recoverable

Removal extends [ADR 0325](../adr/0325-direct-mcp-dcr.md)'s lifecycle without pretending local deletion revokes an upstream client.

**Acceptance:**
- AC6.1: Authoritative lifecycle `ErrNotFound` is sufficient for settings-only profile removal because the existing lifecycle-before-grant invariant makes an independently addressable grant impossible without a lifecycle client ID and generation. It is distinguished from unavailable, corrupt, mismatched, and uncertain state and causes no registration or browser side effect.
  - verify: `TestDirectMCPOnboarding_Scenario6_MissingLifecycleRemovesProfileOnly`
- AC6.2: Ready removal CAS-transitions `ready → removing`, retaining identity, generation, and expected grant key; conditionally deletes the current grant; CAS-transitions to `removed`; then atomically removes the profile. The profile is never absent before the tombstone is durable. Retries resume `removing` or complete settings deletion from `removed`, and crashes or ambiguous commits after each transition are reread before proceeding.
  - verify: `TestDirectMCPOnboarding_Scenario6_RemovalStateMachine`
- AC6.3: Pending, uncertain, corrupt, mismatched, key-unavailable, read-only, and concurrent conflicting state blocks removal without erasing recovery evidence. No force-forget path exists.
  - verify: `TestDirectMCPOnboarding_Scenario6_UnsafeStateBlocksRemoval`
- AC6.4: Removal never deletes the root marker or wrapping key, reports that no upstream client was revoked, and states that existing-daemon activation is unknown. A later add may replace only a valid `removed` tombstone with a fresh pending generation and performs a new upstream registration.
  - verify: `TestDirectMCPOnboarding_Scenario6_TombstonePermitsFreshRegistration`

## Out of scope

| Item | Defer-to | Decision |
|---|---|---|
| ToolHive broker onboarding and status | Existing broker plans | Preserve its independent authority and credentials. |
| Generic MCP scope negotiation | Separate OAuth interoperability plan | V1 preserves direct DCR's exact `openid` no-refresh profile. |
| Multiple-issuer terminal selection and `--issuer` | Later interoperability work | Zero or multiple issuers fail safely in v1. |
| Machine-readable CLI output and SDK wrapper | Future concrete SDK consumer | V1 documents CLI before `spawn()` and adds no SDK symbol. |
| Add without login or add-specific no-browser | Later workflow variants | V1 add is the one-command attended path; existing login retains no-browser. |
| Hot reload and online generation comparison | Future runtime-generation design | Existing-daemon activation remains unknown. |
| Remote administration API | Future administrator-service design | Requires separate authority, audit, custody, and OAuth presentation. |
| Windows local onboarding | Future platform plan | Remote Windows SDK behavior is unchanged. |
| Upstream DCR revocation | Future protocol support | Removal is explicitly local. |
| Wrapping-key garbage collection or backend migration | Future custody plan | V1 never deletes or migrates the selected root key. |
| Per-session and inline-agent MCP OAuth | Future separately authorized capability | Existing header-only scope remains. |

## Definition of done

1. `task lint`, `task test`, `task docs`, `task site:build`, and `task api:check` pass.
2. `task ac-trace-strict` resolves every named proof when the plan becomes `landed`.
3. `go run ./cmd/mecademo` remains green.
4. Offline tests cover every discovery, custody, settings, progress, list, removal, and compatibility criterion without a live model or public network.
5. An explicitly authorized manual qualification adds one disposable direct DCR server from its URL, completes browser consent, invokes one harmless read-only tool before and after daemon restart, lists without network or keyring presentation side effects, and removes with the upstream-not-revoked warning. Evidence records only safe categories.
6. User documentation updates the owning direct MCP OAuth page and CLI/config references; generated configuration reference remains fresh.
7. Any resource outliving one operation is inventoried in ADR 0027 List 1 and, when restart-sensitive, List 2. Operation-local close ordering is still verified.
8. The implementation PR links the Plan / Interface PR and approved commit and reports interface conformance.
9. `/panel-review` reports no ship blockers or unwaived reviewer failures.

## Deferred decisions and known risks

- The file key and encrypted root are readable by the same OS account. Separation limits accidental single-artifact disclosure but does not protect against same-account compromise.
- Same-resource-origin metadata and exact-issuer-origin endpoints intentionally reject some standards-conforming deployments; errors describe policy incompatibility rather than invalid OAuth.
- The initial endpoint probe and protected-resource/authorization-server metadata clients have distinct redirect and authority rules. Shared transport hardening must not collapse those origin sets.
- A `removed` tombstone intentionally retains non-token lifecycle evidence and permits fresh registration. It prevents crash ambiguity but does not revoke or enumerate the old upstream client.
- Narrow AST mutation preserves operator meaning and unrelated comments, not byte-for-byte serialization.
