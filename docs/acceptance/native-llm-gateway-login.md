# Native LLM-gateway OIDC login — acceptance plan

**Contract:** human-reviewed/v1
**Phase:** Native organizational LLM-gateway authentication
**Status:** landed, 2026-09-10. Implementation is complete and becomes authoritative when the Implementation PR merges.
**Delivery:** Split. This changes operator CLI/configuration, provider credential custody, durable secret lifecycle, and the trust boundary between mecatl, ToolHive, and remote callers, so the interface must be approved separately from implementation.
**Expected tasks:** deferred to orchestration

Mecatl will authenticate directly to explicitly configured organizational LLM endpoints without requiring ToolHive to own the credential. “LLM endpoint” is the user-facing term; internally each endpoint is normalized once into the existing composition-owned provider-definition/registry pipeline and uses the existing durable `(provider_id, model_id)` contract. OAuth remains outside `engine/` and no second provider registry is permitted.

V1 native endpoints use one deployment-scoped gateway credential and one deployment-wide resolved inventory. OIDC on `mecated` remains caller authentication and session ownership; it is not provider entitlement. Every caller admitted to a standalone `mecated` can use every configured usable native endpoint, and consequently shares that endpoint’s gateway identity, quota, gateway-side audit/retention posture, and model availability. Operators should configure a dedicated deployment/service gateway identity, never a personal user credential. Deployments needing mutually untrusted or per-user upstream authorization must use separate deployments or await a later design.

Phase 1 retains the existing `toolhive` provider identity, modes/defaults, MCP discovery, and persisted sessions. Native and ToolHive credentials remain wholly separate and neither route falls back to the other. Existing composition and credential seams are described by [ADR 0016](../adr/0016-multi-provider.md), [ADR 0218](../adr/0218-credential-store.md), [ADR 0238](../adr/0238-operator-defined-llm-providers.md), and [ADR 0277](../adr/0277-remote-mecatui-oidc.md).

## Human decisions

- [x] Approve the endpoint configuration and durable identity model. — Decision: The operator UX is the strict operator-tier-only `llm.endpoints.ID` configuration facade and calls entries “LLM endpoints,” never generic providers. It normalizes exactly once into the existing composition-owned provider-definition/registry pipeline; there is no second registry. IDs use the existing provider-ID grammar and reservations, including reserved `toolhive`; a duplicate ID across `llm.endpoints` and legacy `providers` is a configuration error. V1 supports only `protocol: openai-responses`, has no legacy `providers.auth.method` ambiguity, and maps the endpoint ID to existing durable `provider_id`/`model_id` state. Removal, rename, or identity-changing edits make persisted sessions and schedules fail closed without fallback.
- [x] Approve deployment-scoped endpoint access. — Decision: Native endpoint availability is deployment-wide, not principal-sensitive. OIDC-authenticated caller identity continues to own/authenticate sessions and schedules but neither admits nor filters an endpoint. There is no `allowed_principals` schema, composition admission seam, embedded-local capability, per-principal inventory, or persisted authorization claim. Every caller admitted to a standalone `mecated` shares each configured usable endpoint’s gateway identity, quota, gateway-side audit/retention posture, and model availability. Operators use a dedicated deployment/service gateway identity; mutually untrusted or per-user upstream authorization requires separate deployments or a later design.
- [x] Approve endpoint inventory and missing-credential behavior. — Decision: Build performs no authenticated endpoint model listing or probe. `default_model` is configuration inventory floor. The resolved registry and inventory are deployment-wide and consistent across `ListModels`, `DiscoverModels`, capabilities, schedule selector validation, session routing, and picker. A valid optional endpoint with no cached credential starts as `not-enrolled`/unavailable. If it is the configured effective default, startup fails; explicit selection fails with an actionable not-enrolled/unavailable error and never falls back. Once a usable credential exists, live listing remains global and non-principal-specific.
- [x] Approve native credential identity and storage. — Decision: `mecated` and embedded `mecatui` share one explicit operator-configured canonical `llm.credential_home` and the distinct encrypted, keyring-backed namespace `mecatl/provider-oidc/v1`. Record identity binds schema version, endpoint ID, canonical gateway origin/base path, discovery-verified issuer, client ID, resource audience, sorted/deduplicated scopes, exact redirect URI, and separately represented issuer/gateway trust identities including policy and loaded CA-bundle digest. Any change means not enrolled. Unknown schema, unavailable keyring/store, or corrupt identity fails closed. There is no plaintext, `auth.yaml`, environment, memory-only persistence, automatic home/root/backend discovery, or migration fallback; transient access/refresh tokens exist only in process memory as needed.
- [x] Approve lifecycle concurrency, status, and logout. — Decision: A context-aware, hashed, owner-only cross-process transaction lock distinct from record CAS serializes load, enrollment, refresh, logout, and rejected-credential cleanup through token exchange and durable CAS. Rotated refresh material is persisted before a bearer is returned; an omitted replacement refresh token retains the prior one; stale `invalid_grant` cleanup deletes only the exact loaded version; ambiguous post-commit errors are reconciled by exact reread. A crash after provider-side rotation but before durable persistence may require fresh login. Status performs bounded local metadata/record-shape validation only, with no browser, enrollment, refresh, network, keyring/store initialization, or secret disclosure. Logout requires an explicit endpoint, makes local deletion authoritative, then attempts bounded best-effort RFC 7009 revocation without restoring local state. ToolHive status/logout return a specific ToolHive-owned-lifecycle message directing operators to ToolHive tooling.
- [x] Approve command grammar and output transition. — Decision: Commands are `mecatui llm login ENDPOINT`, `mecatui llm status [ENDPOINT]`, and `mecatui llm logout ENDPOINT`; `ENDPOINT` is the exact configured ID. `mecatui llm login toolhive` is explicit. For one release, bare `mecatui llm login` remains a ToolHive-only compatibility alias: it warns on stderr, successfully invokes ToolHive login, and is never reinterpreted as native; after that release it errors for missing `ENDPOINT`. Native and normal ToolHive login print secret-free success on stderr, never tokens. Token-stdout consumers must move to ToolHive’s explicit tooling; mecatl provides no token-print escape hatch. Lifecycle commands never select or change a default. Documentation separates remote `mecatui login ADDRESS`, ToolHive MCP discovery, and manual Codex authentication.
- [x] Approve supported hosts and caller-token boundary. — Decision: Only embedded local `mecatui` performs interactive browser/loopback enrollment. Standalone `mecated` consumes and refreshes an existing record under the same configured credential home and OS/keyring identity and never launches a browser. There is no remote token transfer or login RPC. Current `mecated` drops the raw caller bearer at the authentication boundary and retains only the verified principal; v1 never forwards or retains caller credentials. Forwarded caller access-token mode requires an explicit shared-resource/gateway contract and can apply only to synchronous caller-bound work. OAuth token exchange requires a separate RFC 8693-style contract. `mecak8s` and `mecatequi` support are deferred.
- [x] Approve the OIDC and gateway credential profile. — Decision: V1 uses exact-issuer OIDC discovery and Authorization Code + PKCE S256 only; discovery must advertise S256. It uses the exact ToolHive-compatible fixed loopback redirect URI and route `http://localhost:8666/callback`, with bounded listener, connections, attempts, and timeouts; only one valid terminal callback completes, wrong traffic does not consume the legitimate transaction, and the authorization URL is local-only. Remote `mecatui login ADDRESS` retains its separate `http://127.0.0.1:18473/oauth/callback` contract. Native gateway login uses only the OAuth authorization-code exchange’s `access_token` result and never substitutes an `id_token`; no inbound `mecated` bearer is used. The configured resource audience and the gateway profile’s validated credential requirements apply to that access token. A shared audience alone never makes an ID token an LLM bearer: the access token must be explicitly issued for the gateway resource/contract. V1 does not promise generic ID-token detection or generic RFC 9068 `at+jwt`/`typ` heuristics; opaque access-token support is an explicit future provider profile, never an accidental fallback. A nonempty refresh token is required. Device flow, DCR, client secrets, and TLS skipping are forbidden. Target-independent pieces are extracted from `internal/adapter/clientauth` into a host-internal OIDC client while directly reusing `mcp/oauthlogin`, `authn/oidc.Validator`, `internal/adapter/credentialstore`, and `golang.org/x/oauth2`; neither the MCP OAuth controller nor ToolHive token source is reused. Issuer and gateway network/CA policies remain separate. Gateway transport accepts only its configured canonical HTTPS origin/base path under gateway trust roots and rejects redirects/off-origin requests before token retrieval. A gateway 401 permits at most one synchronized safe refresh-and-retry before the first response chunk and never after streaming begins.
- [x] Approve ToolHive compatibility and isolation. — Decision: ToolHive retains its existing provider identity, modes/defaults, MCP discovery, and persisted sessions. A native endpoint is a distinct explicit identity with no provider fallback. Native and ToolHive code never opens, copies, modifies, aliases, migrates, or deletes the other authority’s configuration, secrets, or references. User documentation covers both ToolHive’s normal token-output hardening and the transition for stdout consumers.

## Interface contract

- **gRPC / protobuf:** No new login, token-transfer, caller-token-forwarding, token-exchange, authorization-capability, or credential-status RPC. Remote requests continue to carry bounded provider/model metadata only. Authorization URLs, codes, tokens, credential references, and exact credential-home/record identities never cross the wire. `mecated` drops raw inbound bearer credentials after authentication and retains only its verified principal for ownership.
- **Exported Go APIs / interfaces:** No `engine/` API, `port.LLMRequest`, provider-module API, or domain widening. Host-internal composition may extend `app.Config` and factor target-independent primitives from `internal/adapter/clientauth` into `internal/adapter/oidcclient`; it directly reuses `mcp/oauthlogin`, `authn/oidc.Validator`, `internal/adapter/credentialstore`, and `x/oauth2`. `app.Build` owns endpoint lifecycle and deployment-wide registry reminting through the existing provider-definition, registry, and per-session paths ([`internal/app/build.go`](../../internal/app/build.go)); no provider-entitlement or principal-filtering interface is added.
- **Tool schemas:** None — endpoint login/status/logout are operator actions, never model-callable tools. The model receives no credential-management affordance or secret-bearing result.
- **CLI / config:** The exact commands are `mecatui llm login ENDPOINT`, `mecatui llm status [ENDPOINT]`, and `mecatui llm logout ENDPOINT`; lifecycle commands never alter selection/defaults. `llm.credential_home` is required when native endpoints are configured and is canonicalized once; no default location is searched. `llm.endpoints.ID` is strict and operator-tier only. Each entry has required `protocol: openai-responses`, `url`, `default_model`, `oidc.{issuer,client_id,resource_audience,scopes}`, `issuer_trust.{policy,ca_bundle}`, and `gateway_trust.{policy,ca_bundle}`. There is no `allowed_principals` field. Trust `policy` is `public` or `private-ca`; `ca_bundle` is forbidden for `public` and required for `private-ca`. Unknown/duplicate fields, empty required values, invalid/reserved IDs, or cross-facade ID collisions are errors. Project-tier `llm` blocks are ignored with a value-free warning. Native endpoints do not extend legacy `providers.auth.method`.
- **Events / persistence:** The namespace is exactly `mecatl/provider-oidc/v1` under the explicit canonical credential home. The versioned record key and authenticated record identity bind endpoint ID; canonical gateway origin/base path; exact discovery-verified issuer; client ID; resource audience; normalized scopes; exact redirect URI; and issuer/gateway trust policy plus loaded CA digest. Unknown schema or any identity drift is not-enrolled. Existing `provider_id`/`model_id`, schedule owner/scope, and session metadata remain the only durable selection identity; no endpoint authorization decisions or local admission capabilities exist or are persisted. No event or snapshot gains OAuth material.
- **Security / authority:** Gateway credentials are deployment-scoped, encrypted, and keyring-backed; access/refresh tokens remain transient process memory. All admitted `mecated` callers share a usable configured endpoint’s gateway identity, quota, gateway-side audit/retention posture, and model availability; caller OIDC remains authentication and ownership only. Operator documentation requires dedicated deployment/service gateway credentials. Issuer and gateway clients have separate trust roots/policies. Value-free diagnostics and status are mandatory. Request transport rejects redirect, off-origin, and base-path escape before token retrieval and allows one pre-stream 401 refresh retry only. V1 uses only the authorization-code exchange `access_token`, never an `id_token` or inbound caller bearer; opaque-token support, forwarded caller access tokens, and token exchange are separate future profiles/contracts.
- **Compatibility / migration:** The exact endpoint ID maps to existing provider identity state. Removing, renaming, or changing its bound identity makes old sessions/schedules fail closed; no default/native/ToolHive fallback or rebinding occurs. Existing `provider_id: toolhive` sessions and ToolHive modes/defaults/MCP discovery remain unchanged. Bare ToolHive login is a one-release alias with stderr warning, then missing-`ENDPOINT` error. Both normal login paths stop printing tokens; stdout consumers move to explicit ToolHive tooling. No credential migration or auto-discovery exists.

### Canonicalization and lock contract

- A gateway `url` is an absolute HTTPS URL with no userinfo, query, or fragment. The scheme and DNS host are lowercase; an explicit default port `443` is removed and any other port is preserved. Percent escapes must be valid. The path is decoded segment-by-segment, rejects decoded `/`, `\`, NUL/control bytes, `.` and `..`, removes empty/repeated separators, re-escapes each segment canonically, and is stored as `/` for the root or a leading-slash path with no trailing slash. Issuer canonicalization follows the exact-issuer rules of the reused validator and discovery response equality is byte-exact to that configured canonical issuer.
- Adapter request joining treats the adapter path as relative, rejects scheme/host/userinfo and literal or encoded traversal/separators, appends canonical escaped segments beneath the configured base path, and verifies the resulting origin and segment-boundary prefix before token retrieval. Query parameters may be added only after that check; redirects are disabled.
- Scopes are nonempty, case-sensitive OAuth scope tokens: each must contain only RFC 6749 `NQCHAR` bytes (`0x21`, `0x23`–`0x5B`, `0x5D`–`0x7E`), with no whitespace, quote, or backslash. They are deduplicated byte-for-byte and sorted lexicographically before identity hashing and requests.
- Lifecycle ordering is: resolve current operator config; derive canonical identity/lock key; acquire the context-aware owner-only transaction lock; open existing keyring/store handles; load/validate; perform enrollment or token exchange if needed; commit/delete with store CAS; reconcile an ambiguous commit by exact reread; release store-internal operation state and then the transaction lock. Provider construction, gateway calls, `Service`/run locks, and browser launch are never entered while holding unrelated mecatl locks; the lifecycle lock is never acquired while holding a session/run/registry lock. Bearer acquisition releases it before the gateway request. Status may use only already configured existing read-only handles and the same lock; it must not create a key, keyring entry, store, network client, or enrollment.

## In scope — 8 scenarios, in implementation order

### Scenario 1 — Operator configuration becomes one deployment-wide endpoint registry input

The facade feeds the single registry and remint path required by [ADR 0016](../adr/0016-multi-provider.md) and the operator-only rules of [ADR 0238](../adr/0238-operator-defined-llm-providers.md).

**Acceptance:**
- AC1.1: Strict operator `llm.endpoints` entries with the exact schema normalize once to existing provider definitions; endpoint IDs obey existing grammar/reservations, `toolhive` is reserved, cross-facade collisions error, and only `openai-responses` is accepted.
  - verify: `TestInvariant_native_llm_endpoint_facade_normalizes_once`
- AC1.2: Project-tier endpoint or credential-home configuration is ignored with a value-free warning; malformed URLs, trust, or scopes and unknown fields fail closed without credential access; `allowed_principals` is rejected as unknown.
  - verify: `TestNativeLLMGatewayLogin_Scenario1_StrictOperatorTierOnly`
- AC1.3: Canonical URL and request joining obey the segment, escaping, origin, base-path, and trailing-slash contract and reject every literal/encoded escape before token retrieval.
  - verify: `TestNativeLLMGatewayLogin_Scenario1_CanonicalGatewayURLAndJoin`
- AC1.4: Endpoint removal, rename, identity drift, or ID collision makes old session and schedule identities unavailable without fallback or rebinding.
  - verify: `TestNativeLLMGatewayLogin_Scenario1_DurableIdentityFailsClosed`

### Scenario 2 — Caller OIDC remains ownership-only; endpoint availability is deployment-wide

The existing caller-identity/ownership boundary is retained without introducing a second authorization policy, consistent with [`AGENTS.md`](../../AGENTS.md).

**Acceptance:**
- AC2.1: No endpoint principal ACL, composition admission seam, embedded-local admission capability, per-principal filtering, authorization claim, or authorization cache/persistence is introduced.
  - verify: `TestNativeLLMGatewayLogin_Scenario2_NoProviderEntitlementSurface`
- AC2.2: Any caller already admitted to standalone `mecated` can select each configured usable native endpoint; session and schedule caller ownership remain unchanged and do not affect endpoint availability.
  - verify: `TestNativeLLMGatewayLogin_Scenario2_DeploymentWideEndpointAvailability`
- AC2.3: Documentation and operator diagnostics state that users share the configured gateway identity, quota, gateway-side audit/retention posture, and model availability, and recommend a dedicated deployment/service identity.
  - verify: `TestNativeLLMGatewayLogin_Scenario2_SharedGatewayIdentityDocumentation`

### Scenario 3 — One deployment-wide inventory is consistent without Build-time probing

Capability truth remains composition-computed as required by [`AGENTS.md`](../../AGENTS.md); in v1 the registry and resolved inventory are deployment-wide rather than principal-sensitive.

**Acceptance:**
- AC3.1: Build makes no authenticated endpoint-list request or probe; configured `default_model` values are the inventory floor.
  - verify: `TestNativeLLMGatewayLogin_Scenario3_NoCredentialedBuildDiscovery`
- AC3.2: The same deployment-wide resolved inventory/provider registry drives `ListModels`, `DiscoverModels`, capabilities, schedule selector validation, session routing, and picker; no filtering or authorization policy is introduced.
  - verify: `TestNativeLLMGatewayLogin_Scenario3_DeploymentWideInventoryConsistency`
- AC3.3: Once a usable credential exists, live listing is global and non-principal-specific and updates the same deployment-wide inventory consistently.
  - verify: `TestNativeLLMGatewayLogin_Scenario3_GlobalLiveInventory`
- AC3.4: A valid optional endpoint without a cached credential permits startup and has `not-enrolled`/unavailable status; an effective configured default without a usable credential fails startup, while explicit selection returns actionable not-enrolled/unavailable and never falls back.
  - verify: `TestNativeLLMGatewayLogin_Scenario3_MissingCredentialBehavior`

### Scenario 4 — Embedded mecatui enrolls a strongly bound native credential

The host-owned callback pattern comes from [ADR 0277](../adr/0277-remote-mecatui-oidc.md), with target-independent primitives reused rather than its remote-target records.

**Acceptance:**
- AC4.1: Embedded local mecatui alone performs exact-issuer discovery and Auth Code + PKCE S256; lack of advertised S256, wrong issuer, callback state/issuer/route, excess traffic, timeout, or cancellation fails without consuming a later legitimate callback or writing a record.
  - verify: `TestNativeLLMGatewayLogin_Scenario4_PKCEAndBoundedCallback`
- AC4.2: Enrollment uses only the authorization-code exchange `access_token`, requires `token_type=Bearer`, a nonempty refresh token, configured resource audience, and the gateway profile’s validated credential requirements. It never substitutes an `id_token`, promises no generic ID-token detection or RFC 9068 `at+jwt`/`typ` heuristic, and cannot accidentally accept opaque access tokens.
  - verify: `TestNativeLLMGatewayLogin_Scenario4_AccessTokenOnlyProfile`
- AC4.3: The protected record uses the exact namespace and full bound identity; unknown schema, identity/trust/CA-digest change, corrupt record, unavailable keyring, or unavailable store is not-enrolled and never falls back to plaintext/env/memory, auto-discovery, or migration.
  - verify: `TestNativeLLMGatewayLogin_Scenario4_CredentialIdentityAndStorage`
- AC4.4: Standalone mecated uses/refreshes an existing record under the identical configured home and OS/keyring identity but never opens a browser; remote clients cannot upload tokens/codes/records and no login RPC exists.
  - verify: `TestNativeLLMGatewayLogin_Scenario4_HostOwnershipBoundary`
- AC4.5: Successful native login emits only secret-free stderr confirmation; tokens, codes, URLs, record keys, raw provider bodies, and trust paths never enter stdout, diagnostics, errors, events, snapshots, model context, or RPCs.
  - verify: `TestNativeLLMGatewayLogin_Scenario4_SecretCanaryNonDisclosure`

### Scenario 5 — Refresh, request retry, status, and logout are one race-safe lifecycle

CAS semantics follow [ADR 0218](../adr/0218-credential-store.md); lifecycle resources and restart treatment follow [ADR 0027](../adr/0027-cloud-native.md).

**Acceptance:**
- AC5.1: The hashed owner-only context-aware cross-process transaction lock and record CAS follow the stated ordering and serialize load/enroll/refresh/logout/rejected cleanup through exchange/commit across processes without holding session/run/registry locks.
  - verify: `TestNativeLLMGatewayLogin_Scenario5_TransactionLockOrderingAndConcurrency`
- AC5.2: Refresh persists rotated material before returning a bearer, retains the prior refresh token only when omitted, exact-version-deletes stale `invalid_grant`, reconciles ambiguous post-commit results by reread, and documents that a crash in the provider-rotation/persistence gap can require login.
  - verify: `TestNativeLLMGatewayLogin_Scenario5_RotationAndAmbiguousCommit`
- AC5.3: Gateway requests are same-origin/base-confined under gateway trust, redirect-disabled, and obtain a token only after validation. A 401 receives at most one synchronized refresh/retry before any response chunk and none after streaming begins.
  - verify: `TestNativeLLMGatewayLogin_Scenario5_BearerBoundaryAndSingleRetry`
- AC5.4: Status with zero or one endpoint reports only bounded states `usable`, `not-enrolled`, `expired`, `corrupt`, `storage-unavailable`, or `rejected`; it performs no network/browser/enrollment/refresh/keyring initialization and discloses no secret or exact local identity. Zero endpoints enumerates configured native endpoint IDs in the local operator configuration.
  - verify: `TestNativeLLMGatewayLogin_Scenario5_LocalStatusIsPassive`
- AC5.5: Logout requires one native endpoint, conditionally deletes its exact local version first, then bounded-best-effort RFC 7009 revokes retained in-memory material; failure never restores local state and concurrent replacement survives.
  - verify: `TestNativeLLMGatewayLogin_Scenario5_LogoutDeleteThenRevoke`
- AC5.6: ADR 0027 List 1 gains explicit rows for the endpoint token source, JWKS cache, separate issuer and gateway HTTP clients, keyring/store handles, and lifecycle lock registry/handles, with owner/scope/cleanup/reattach. List 2 covers token/refresh state via durable record, JWKS reset/refetch, handles re-opened from explicit configuration, and locks reset/reacquired after restart.
  - verify: inspection — implementation updates [ADR 0027](../adr/0027-cloud-native.md) List 1 and List 2 with repo-root-relative code citations; `task docs` validates them.

### Scenario 6 — CLI grammar is explicit and never prints a token

Operator actions stay separate from model/runtime affordances under [`AGENTS.md`](../../AGENTS.md).

**Acceptance:**
- AC6.1: Native commands implement exact `ENDPOINT` grammar, never infer hostname/model/default, never alter provider selection, and distinguish remote `mecatui login ADDRESS`, ToolHive MCP discovery, and manual Codex authentication in help/remediation.
  - verify: `TestNativeLLMGatewayLogin_Scenario6_CommandGrammarAndCopy`
- AC6.2: `mecatui llm login toolhive` invokes ToolHive; for one release bare login warns on stderr and still succeeds as ToolHive-only, then becomes missing-`ENDPOINT`; it is never native.
  - verify: `TestNativeLLMGatewayLogin_Scenario6_ToolHiveCompatibilityAlias`
- AC6.3: Native and normal ToolHive login print secret-free stderr success with no mecatl token-output escape hatch; documented stdout consumers are directed to ToolHive’s explicit token tooling.
  - verify: `TestNativeLLMGatewayLogin_Scenario6_TokenOutputHardening`
- AC6.4: ToolHive status/logout fail with a specific ToolHive-owned-lifecycle message naming ToolHive tooling instead of inspecting or modifying either native or ToolHive state.
  - verify: `TestNativeLLMGatewayLogin_Scenario6_ToolHiveLifecycleMessage`

### Scenario 7 — ToolHive and native endpoint authorities remain disjoint

Existing ToolHive behavior is frozen by [ADR 0064](../adr/0064-toolhive-llm-gateway-provider.md) and [ADR 0102](../adr/0102-toolhive-direct-mode.md).

**Acceptance:**
- AC7.1: ToolHive retains provider identity, `auto|proxy|direct` modes/defaults, MCP discovery, and restored `provider_id: toolhive` sessions; matching URLs/models never rebind them.
  - verify: `TestNativeLLMGatewayLogin_Scenario7_ToolHiveIdentityCompatibility`
- AC7.2: Native operation succeeds without ToolHive and each lifecycle proves it never opens, copies, modifies, aliases, migrates, cleans, or references the other authority’s config/store/keyring material.
  - verify: `TestNativeLLMGatewayLogin_Scenario7_CredentialAuthorityIsolation`
- AC7.3: Native failure and ToolHive failure never trigger cross-provider fallback, default mutation, or credential import.
  - verify: `TestNativeLLMGatewayLogin_Scenario7_NoCrossProviderFallback`

### Scenario 8 — Documentation and ADRs make the boundary operable

Behavior changes update living docs while new rationale is recorded in a new ADR, following the documentation lifecycle in [`AGENTS.md`](../../AGENTS.md).

**Acceptance:**
- AC8.1: A new ADR supersedes only the affected native ownership/configuration portions of ADR 0238 and clarifies—not replaces—ToolHive ADRs 0064/0102; it records facade normalization, deployment-wide inventory, shared gateway identity, host/storage identity, lifecycle/locking, OIDC/network profile, compatibility, and deferred upstream-authorization patterns. Existing frozen ADRs are not edited for rationale.
  - verify: `task docs` and inspection of ADR status/supersession links
- AC8.2: `docs/architecture.md`, provider architecture, `docs/design/IMPLEMENTATION-NOTES.md`, `docs/usage.md`, `docs/tui.md`, generated configuration reference, troubleshooting, and relevant `user-docs/` deployment/provider pages describe exact schema/commands, deployment-wide shared gateway identity and inventory, missing-credential behavior, passive status, storage, host limits, no-token output, ToolHive/MCP/Codex distinctions, and stdout transition.
  - verify: `task docs` and `task site:build`
- AC8.3: Documentation states that v1 drops raw inbound caller bearers at authentication, retains only verified principals for ownership, and never forwards/retains caller credentials; separate deployments are required for mutually untrusted/per-user upstream authorization pending future forwarded-token and RFC 8693-style token-exchange contracts.
  - verify: `TestNativeLLMGatewayLogin_Scenario8_UpstreamAuthorizationBoundary`
- AC8.4: Docs and CLI consistently call configured entries “LLM endpoints”; “provider” appears only where naming the existing internal/durable provider machinery or ToolHive provider compatibility.
  - verify: `TestNativeLLMGatewayLogin_Scenario8_UserFacingEndpointVocabulary`

## Out of scope

| Item | Defer-to | Decision |
|---|---|---|
| `mecak8s` and `mecatequi` native endpoint credentials | Separate host-specific acceptance plan | V1 supports embedded mecatui enrollment and standalone mecated reuse only. |
| Remote login/token transfer RPC | Not planned under this trust model | Enrollment occurs only in the provider-calling host/operator domain. |
| Forwarded inbound caller access-token mode | Future shared-resource/gateway contract | It would apply only to synchronous caller-bound work; v1 drops raw caller bearers at authentication. |
| OAuth token exchange | Separate RFC 8693-style contract | No exchange implementation surface is introduced in v1. |
| Device flow, DCR, client secrets, workload identity, or opaque access-token support | Separate auth/provider profile | V1 is Authorization Code + PKCE S256 using only the exchange `access_token`; opaque support must be explicit, never fallback. |
| ID-token substitution or generic ID-token/RFC 9068 token-shape heuristics | Not planned | Shared audience alone never authorizes an ID token as an LLM bearer. |
| Vendor subscription login (OpenAI/ChatGPT/Codex/Anthropic) | Provider-specific contracts | Organizational endpoint login must not imply vendor-account semantics. |
| ToolHive secret import/export/migration or mecatl token-print mode | Not planned | Authorities and token-output tooling remain separate. |
| New wire protocol, OAuth fields in `port.LLMRequest`, or model-callable credential tools | Separate API decision | V1 reuses `openai-responses` beneath composition. |
| Automatic migration from legacy `providers` or discovered credential homes/backends | Not planned | Configuration collisions error and enrollment identity is explicit. |
| Per-principal endpoint ACLs or inventory filtering | Later design | V1 is deployment-wide; callers admitted to one mecated share its configured endpoint availability. |

## Definition of done

1. The Split Plan / Interface PR is human-approved and merged before implementation starts; this plan then moves from `proposed` to `approved` with the merged baseline.
2. A new ADR records the decisions in AC8.1, and ADR 0027 List 1/List 2 add the token source, JWKS cache, issuer/gateway HTTP clients, keyring/store handles, and lifecycle locks with exact ownership and restart treatment.
3. Every registry consistency path, missing-credential behavior, canonicalization rule, lifecycle transition, lock/CAS race, passive status, host boundary, access-token-only profile, caller-bearer drop, and ToolHive isolation requirement has hermetic offline tests; value canaries prove no secret reaches output, logs, errors, events, snapshots, RPCs, or model context.
4. `task lint`, `task test`, `task docs`, `task site:build`, `task api:check`, and `go run ./cmd/mecademo` pass; no test contacts a live issuer, keyring service, browser, or gateway.
5. `task ac-trace-strict` resolves every named proof when the plan becomes `landed`; the implementation PR links the Plan / Interface PR and approved commit and reports interface conformance.
6. `/panel-review` reports no ship blockers or unwaived reviewer failures, including security review of deployment-scoped credential custody, URL confinement, access-token-only profile, lock/CAS ordering, retry-before-stream, caller-bearer drop, and cross-authority isolation.

## Deferred decisions and known risks

- No implementation-shaping human decision remains open. Internal type names and file splits may vary only if the observable interface, ownership, and single deployment-wide normalization/registry seam remain exact.
- V1 intentionally shares a gateway identity, quota, gateway-side audit/retention posture, and model availability among all callers admitted to a standalone `mecated`. A deployment that cannot accept that boundary must use separate deployments or defer adoption pending a later per-user upstream design.
- Current `mecated` drops raw caller bearer credentials at authentication and retains only verified principals. Forwarded caller access tokens need an explicit shared-resource/gateway contract and are limited to synchronous caller-bound work; RFC 8693-style token exchange is a separate future contract.
- Provider-side refresh-token rotation and local durable commit cannot be made atomic. A crash in that gap may invalidate the last durable refresh token and require fresh interactive login; the implementation and remediation must say so plainly.
- Browser enrollment holds the endpoint lifecycle transaction across the authorization exchange, so a bounded timeout and context-aware lock are required to avoid indefinite cross-process exclusion.
- Removing normal ToolHive token stdout is an intentional security hardening with compatibility impact; the release notes and user docs must direct machine consumers to ToolHive’s explicit tooling without adding a mecatl escape hatch.
