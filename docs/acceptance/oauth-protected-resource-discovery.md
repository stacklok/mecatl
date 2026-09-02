# OAuth Protected Resource Discovery — acceptance plan

**Phase:** Remote mecatui OAuth bootstrap
**Status:** in-progress, 2026-09-02. Synthesized from [stacklok/mecatl#1033](https://github.com/stacklok/mecatl/issues/1033) and the Okta, Entra, OAuth, security, and ToolHive reviews.
**Issue:** [stacklok/mecatl#1033](https://github.com/stacklok/mecatl/issues/1033).
**ADR:** [ADR 0290](../adr/0290-oauth-protected-resource-discovery.md) — RFC 9728 profile and resource/transport identity.
**Accumulator branch:** `acc/oauth-protected-resource-discovery`.

The smallest set of work lets a user enroll and reconnect to an OIDC-protected `mecated` or `mecak8s` deployment with `mecatui login mecak8s.staging.stacklok.dev`, without manually supplying issuer, audience, or public client ID. The server publishes RFC 9728 metadata plus a namespaced mecatl profile; mecatui confirms the discovered tuple once, then persists it through the existing PKCE and credential lifecycle.

## Why these scope cuts

- [ADR 0277](../adr/0277-remote-mecatui-oidc.md) retains ownership of PKCE, credentials, refresh, recovery, logout, and CA separation; this plan changes only enrollment discovery.
- [ADR 0287](../adr/0287-target-aware-mecatui-tls.md) remains authoritative for gRPC target TLS.
- [ADR 0204](../adr/0204-caller-identity-threading.md) keeps configured issuer/audience authoritative for server validation.
- ToolHive `v0.40.0` and ToolHive-Core `v0.0.41` are the implementation sources. Their Apache-2.0 code is adapted where direct import is unsuitable; known defects are pinned and fixed rather than copied.
- The plan does not add engine, port, protobuf, or exported API changes.

## In scope — 6 scenarios, in implementation order

### Scenario 1 — Shared server profile

Both composition roots extend the shared OIDC configuration with `--oidc-resource HTTPS_URL`, `--oidc-client-id PUBLIC_ID`, and optional CSV `--oidc-scopes a,b`. Existing `--oidc-issuer` and `--oidc-audience` remain the sole sources for the metadata `authorization_servers[0]` and `com.stacklok.mecatl.audience` projections. The profile is disabled when the new fields are absent; supplying scopes alone, either required profile field alone, or any profile field while OIDC is disabled fails before listeners start. Empty values are equivalent to omitted only where the flag parser has not supplied the field; supplied empty CSV entries fail. Scope validation is one shared, case-sensitive RFC 6749 token grammar for CLI and Helm; duplicates are rejected or deterministically removed by that same parser. This follows the shared caller-identity wiring in [`internal/cliconfig/oidc.go`](../../internal/cliconfig/oidc.go) and the composition boundaries in [`architecture.md`](../architecture.md).

**Acceptance:**
- AC1.1: Existing issuer and audience feed both token validation and metadata projection; no duplicate issuer/audience policy exists.
  - verify: `TestADR_0290_ProfileProjection`
- AC1.2: A complete resource/client profile enables metadata under OIDC; absent profile fields preserve existing behavior.
  - verify: `TestADR_0290_ProfileConfigurationMatrix`
- AC1.3: `--oidc-scopes` accepts CSV, trims, rejects empty/unsafe entries, deduplicates, and produces deterministic metadata order; absence omits `scopes_supported`.
  - verify: `TestADR_0290_ScopeCSV`
- AC1.4: Invalid or partial profile configuration fails before serving.
  - verify: `TestADR_0290_ProfileValidation`

### Scenario 2 — ToolHive-derived metadata and challenge

The public HTTP listener exposes `GET /.well-known/oauth-protected-resource` outside bearer middleware. Root and path-bearing resources use one helper based on ToolHive’s `buildWellKnownURI` and `WellKnownOAuthResourcePath`. The response contains standard RFC 9728 fields plus `com.stacklok.mecatl.audience` and `com.stacklok.mecatl.client_id`. The protected API challenge includes `resource_metadata` when enabled. The adapted code records ToolHive source paths/tags and corrects its prefix routing, method, CORS, default-scope, validation, and header-safety issues. See [ADR 0290](../adr/0290-oauth-protected-resource-discovery.md) and the public listener shape in [`internal/adapter/server/authn.go`](../../internal/adapter/server/authn.go).

**Acceptance:**
- AC2.1: Both binaries return the configured metadata with `200` and `application/json` outside authentication and metrics/admin listeners.
  - verify: `TestADR_0290_ProtectedResourceMetadata`
- AC2.2: Metadata includes standard fields, both mecatl extensions, and optional scopes without secrets, CA paths, or internal listener data.
  - verify: `TestADR_0290_MetadataFields`
- AC2.3: Only the exact well-known path or valid RFC path-specific form is routed; unsupported methods are rejected deliberately.
  - verify: `TestADR_0290_WellKnownRouting`
- AC2.4: One path helper drives metadata routing and challenge URL generation, including path resources.
  - verify: `TestADR_0290_WellKnownPathDerivation`
- AC2.5: Disabled profiles return 404 and static-token-only deployments do not advertise OAuth.
  - verify: `TestADR_0290_MetadataDisabledCompatibility`
- AC2.6: 401 responses carry one safe `resource_metadata` challenge derived only from operator configuration; unrelated routes retain existing behavior.
  - verify: `TestADR_0290_ChallengeMatrix`
- AC2.7: Metadata includes the standard resource and authorization-server fields; ToolHive fixture parity remains unproven by this local serialization test.
  - verify: `TestADR_0290_MetadataStandardFields`

### Scenario 3 — Hardened client discovery

`mecatui` accepts a bare hostname as verified HTTPS on port 443, or an explicit HTTPS resource URL. Existing `host:port` remains legacy gRPC mode. `--grpc-target` overrides only transport. The client fetcher is adapted from ToolHive `FetchResourceMetadata` and ToolHive-Core networking, with anonymous requests, no redirects, public-address/DNS-rebinding protection, bounded headers/body, proper MIME parsing, exact resource equality, exactly one authorization server, and exact issuer matching through OIDC discovery. The selected issuer is still metadata-controlled before confirmation, so its OIDC discovery request uses the same anonymous public-bootstrap transport and cannot use private issuer CA/address exceptions. Unknown profile fields are ignored; required mecatl fields are validated. This preserves the transport and trust-boundary discipline in [ADR 0290](../adr/0290-oauth-protected-resource-discovery.md).

**Acceptance:**
- AC3.1: Bare hostnames and explicit HTTPS URLs produce deterministic resource and gRPC identities; HTTP, userinfo, query, fragment, malformed escapes, and ambiguous authorities fail before I/O.
  - verify: `TestADR_0290_ResourceInputGrammar`
- AC3.2: RFC 9728 path insertion and exact resource matching reject origin-only, prefix, trailing-slash, port, and escaped-path near matches.
  - verify: `TestADR_0290_ExactResourceBinding`
- AC3.3: Fetching is anonymous, redirect-free, TLS-verified, public-address-only, DNS-rebinding resistant, timeout-bounded, and response-size bounded.
  - verify: `TestADR_0290_MetadataFetchSecurity`
- AC3.4: Over-limit and trailing data are rejected and JSON media types are parsed correctly.
  - verify: `TestADR_0290_MetadataBodyBounds`
- AC3.5: Zero/multiple authorization servers, unsafe profile values, and missing required profile extensions fail closed.
  - verify: `TestADR_0290_ProfileDocumentValidation`
- AC3.6: OIDC discovery `issuer` exactly matches the selected authorization server and is fetched before confirmation through the same anonymous public-bootstrap transport, with no private-address or custom-CA exception.
  - verify: `TestADR_0290_IssuerBinding`
- AC3.7: Metadata fetch or issuer discovery failure, timeout, mismatch, or malformed response performs no browser launch, keyring initialization, credential write, or registry write.
  - verify: `TestADR_0290_DiscoveryFailureLeavesNoState`
- AC3.8: Duplicate security-relevant JSON members are rejected, and profile scopes are never silently expanded by a later metadata refresh; unknown non-profile members remain ignorable.
  - verify: `TestADR_0290_MetadataDuplicateFields`
- AC3.9: No bearer, cookie, client certificate, credential-store material, response body, or token appears in discovery requests/errors/diagnostics.
  - verify: `TestInvariant_resource_discovery_is_anonymous`

### Scenario 4 — Shorthand enrollment

`mecatui login HOSTNAME` and `mecatui login HTTPS_URL` discover the profile, compute one immutable validated discovery result, and display resource/metadata URL/issuer/audience/client/scopes/gRPC target for first-use confirmation. The exact confirmed result, without re-fetch or re-canonicalization, is the only input to browser authorization and persistence. The resource, issuer, and gRPC CA policies remain independent. Legacy explicit enrollment remains complete and compatible. V1 does not require RFC 8707 or promise opaque-token support. Metadata, issuer-discovery, confirmation, and enrollment failures never launch a browser or create keyring/credential/registry state. This extends, rather than replaces, the remote enrollment contract in [ADR 0277](../adr/0277-remote-mecatui-oidc.md).

**Acceptance:**
- AC4.1: Shorthand enrollment completes without explicit issuer, audience, or client-ID flags.
  - verify: `TestOAuthProtectedResource_Scenario4_ShorthandEnrollment`
- AC4.2: `--grpc-target` changes only transport and a resource path never becomes a gRPC path.
  - verify: `TestADR_0290_ResourceTargetSeparation`
- AC4.3: Metadata, issuer, and gRPC TLS/CA policies remain separate.
  - verify: `TestInvariant_oauth_three_transport_trust_split`
- AC4.4: Browser launch and persistence occur only after explicit first-use confirmation; the same immutable confirmed tuple is passed unchanged to authorization and enrollment, and rejection/cancellation/EOF leaves no state.
  - verify: `TestADR_0290_DiscoveredIdentityConfirmation`
- AC4.5: The final requested scope set is the confirmed, validated union of the OIDC baseline and profile scopes, with explicit CLI `--scopes` precedence defined and displayed before confirmation.
  - verify: `TestADR_0290_DiscoveredScopeSelection`
- AC4.6: Legacy explicit enrollment, private issuer mode, explicit scopes, callback, and saved connect remain compatible.
  - verify: `TestADR_0277_ExplicitEnrollmentCompatibility`
- AC4.7: V1 does not add an RFC 8707 request parameter and rejects unsupported opaque-token operation rather than guessing.
  - verify: `TestADR_0290_ProviderCompatibility`

### Scenario 5 — Saved resource aliases and reconnect

Public registry metadata gains optional `ResourceURL`, while the encrypted credential identity key remains unchanged. New records store resource URL and gRPC target separately. Lookup accepts exact canonical resource or exact legacy target and fails closed on ambiguity. Legacy records remain readable. Saved `connect` never launches a browser or adopts live metadata drift. Logout, reauthentication, and recovery retain ADR 0277 locking, CAS, and same-identity rules. The target/resource split is the decision recorded in [ADR 0290](../adr/0290-oauth-protected-resource-discovery.md).

**Acceptance:**
- AC5.1: New records store separate resource and gRPC identities without making existing credentials unreachable.
  - verify: `TestADR_0290_AdditiveResourceRegistryIdentity`
- AC5.2: Exact lookup by resource or target succeeds only when unambiguous.
  - verify: `TestADR_0290_RegistryLookupAmbiguity`
- AC5.3: Legacy rows without ResourceURL remain usable by exact target with no eager rewrite.
  - verify: `TestADR_0277_LegacyRegistryCompatibility`
- AC5.4: `mecatui connect RESOURCE` is browser-free and uses only the saved confirmed tuple.
  - verify: `TestOAuthProtectedResource_Scenario5_SavedConnect`
- AC5.5: Metadata drift cannot rewrite saved issuer, audience, client ID, scopes, resource, or target.
  - verify: `TestADR_0290_SavedIdentityDrift`
- AC5.6: Logout, reauthentication, `/connect`, concurrency, and recovery use one alias rule and preserve ADR 0277 transaction/CAS behavior.
  - verify: `TestADR_0290_ResourceAliasLifecycle`
- AC5.7: A legacy record without ResourceURL is not synthesized into a resource alias unless an explicit resource is confirmed; exact target lookup remains the only legacy path.
  - verify: `TestADR_0290_LegacyResourceAliasPolicy`
- AC5.8: Resource-alias and target-alias operations resolve the same record deterministically, and concurrent enrollment/reauthentication cannot mutate a different record.
  - verify: `TestADR_0290_AliasConcurrency`

### Scenario 6 — Helm, documentation, and offline proof

The mecak8s chart adds `oidc.resource`, `oidc.clientID`, and `oidc.scopes`, rendering the shared flags and joining YAML scope lists to CSV. The schema/helper validates complete and partial combinations. A hermetic TLS/OIDC fixture proves both composition roots, discovery, confirmation, PKCE, persistence, authenticated gRPC, and browser-free reconnect. User-facing and generated documentation are updated. The chart topology and storage-free deployment constraints remain those of [ADR 0048](../adr/0048-mecak8s.md), while documentation must follow the lifecycle rules in [`AGENTS.md`](../../AGENTS.md).

**Acceptance:**
- AC6.1: Helm renders `oidc.resource` and `oidc.clientID` as the shared `--oidc-resource` and `--oidc-client-id` flags, joins `oidc.scopes` to the same CSV parser used by the CLI, and rejects partial or malformed values including `oidc.enabled: false` with profile fields set.
  - verify: `TestADR_0290_HelmProtectedResourceProfile`
- AC6.2: The hermetic flow proves metadata, confirmation, PKCE, persistence, authenticated gRPC, and bare-host reconnect.
  - verify: `TestOAuthProtectedResource_Scenario6_EndToEnd`
- AC6.3: Both server composition roots exercise the shared contract.
  - verify: `TestADR_0290_ServerCompositionParity`
- AC6.4: Documentation distinguishes RFC fields, mecatl extensions, existing OIDC projections, transport separation, and ToolHive provenance.
  - verify: inspection — documentation includes protocol, extension, provenance, and compatibility sections
- AC6.5: Generated documentation and site build are current.
  - verify: demonstration — run `task docs` and `task site:build`

## Out of scope

- Multiple authorization servers and issuer-keyed client mappings.
- Dynamic Client Registration.
- Mandatory RFC 8707 resource indicators.
- Opaque-token/introspection support.
- Private protected-resource discovery.
- Metadata-advertised gRPC targets.
- New scope-based server authorization.
- Browser launch from `connect`.
- Engine/protobuf changes.
- Upstream ToolHive changes.

## Sequencing recommendation

Land ADR/profile validation first; adapt the ToolHive handler/path/challenge primitives; wire both server roots; add the hardened client fetcher and shorthand parser; integrate confirmation and existing enrollment; add registry aliases; then Helm, e2e, and docs. `/plan-orchestrate` decomposes these scenarios into worker tasks.

## Definition of done

1. ADR 0290 and this plan are present and indexed.
2. The acceptance-plan checker and `task ac-trace-strict` pass once landed.
3. `task lint`, `task test`, `task api:check`, `task docs`, and `task site:build` pass.
4. Helm tests and the hermetic OAuth end-to-end test pass.
5. `go run ./cmd/mecademo` remains green.
6. Adapted upstream code retains Apache-2.0 attribution.
7. Panel review has no ship blockers.

## Deferred decisions and known risks

- First-use confirmation is required because unauthenticated metadata selects an issuer and public client registration; saved connect remains browser-free.
- Public-resource discovery is V1-only; private deployments retain explicit enrollment.
- Okta RFC 8707 support is not assumed.
- Scope metadata is an acquisition hint, not new authorization policy.
- AC6.2 remains a live qualification gap: the existing offline registry and
  composition tests do not exercise browser PKCE, persistence, authenticated
  gRPC, and reconnect as one flow. They must not be treated as an end-to-end
  proof; the documented Kind qualification path remains required.

## Exit criteria

The plan is satisfied when every acceptance criterion has a resolving proof on the landed accumulator and the Definition of Done is green.
