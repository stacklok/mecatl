# Direct MCP Dynamic Client Registration — acceptance plan

**Contract:** human-reviewed/v1
**Phase:** local direct-MCP OAuth durability
**Status:** proposed, 2026-09-09. All human decisions required for implementation are resolved and recorded; the proposed contract awaits human plan/interface review, not approval or landing. Gateway discovery, the standalone public-client DCR/PKCE port probe, and an operator-driven mecatui spike against the deployed gateway passed; refresh and the full failure/recovery contract remain unverified.
**Delivery:** Split. This changes an operator-facing OAuth client union, durable credential identity, registration lifecycle, and the local interactive authorization boundary.
**Expected tasks:** deferred to orchestration
**Issue:** none — scope supplied for acceptance-contract review.
**Plan PR:** <added when opened>
**Approved baseline:** <merged plan commit; absent until approved>

Enable a local mecatui direct (non-broker) MCP profile to use RFC 7591 Dynamic Client Registration (DCR), then the existing authorization-code/PKCE login path, against `https://connector-gateway.stacklok.dev/gw/mcp`. The durable registration and resulting grant must survive a local restart and permit a harmless discovered read tool. This does not change remote mecatui OIDC, the MCP broker, or `CallMcpWithQuery`.

Anonymous discovery established that both `/mcp` and `/gw/mcp` returned a protected-resource challenge naming `https://connector-gateway.stacklok.dev/.well-known/oauth-protected-resource/gw/mcp`; that document bound resource `https://connector-gateway.stacklok.dev/gw/mcp` to the same-origin authorization server. Its authorization-server metadata advertised `/oauth/authorize`, `/oauth/token`, `/oauth/register`, `code`, `authorization_code`, `refresh_token`, token exchange, PKCE `S256`, public (`none`) token authentication, and `openid profile email offline_access`.

With explicit user authorization, a standalone local Python probe registered one public client for one IPv4-loopback callback URI and completed two browser authorizations and S256 PKCE code exchanges using the same client ID/path on two different ports, each different from the registered port. It requested only `openid` and the authorization-code grant, discarded issued tokens, and retained the registration response only in owner-only ignored local state. This proves deployed gateway loopback-port variation for that profile. Subsequently, the operator drove the Go spike through `mecated mcp login` and local mecatui against the deployed gateway, discovered the real connector tool catalog, invoked the harmless Excalidraw `read_me` tool, restarted mecatui without another registration, and invoked it again using the restored encrypted credential. That proves the selected SDK/DCR/PKCE/login/persistence/MCP invocation happy path, but not refresh, different-path rejection, concurrency, or uncertain-outcome recovery. No credential values or authorization URLs belong in this plan.

## Human decisions

- [x] Direct versus broker client shape — Decision: retain `client.mode: dcr` with separate authority-selected direct/broker validation, preserving broker compatibility. Direct requires exact `issuer` and forbids `upstream`; broker retains explicit OAuth2 `upstream` and `discovery_url`. Proposed direct spelling below is `dcr: {}` with no extra knobs.
- [x] Registration versus grant lifetime — Decision: durable registration is separate from the grant; grant reset and failed refresh preserve registration. Replacing registration requires explicit reset. Registration identity is bound to profile, principal, canonical resource, and exact issuer; no client identity is inferred from a token.
- [x] Concurrent first login guarantee — Decision: use the existing credential-store CAS winner-adoption model and accept possible duplicate upstream clients. Exactly-once upstream registration is not promised or required.
- [x] Consent owner — Decision: reuse `mecated mcp login SERVER [--no-browser]` and `internal/app.LoginMCP`; local mecatui consumes saved credentials. No startup, reconnect, model tool, or background refresh may launch a browser or register a client.
- [x] Redirect reuse — Decision: stable registration-bound random callback path with an ephemeral IPv4 loopback port, fresh state and PKCE per authorization, and exact callback path/Host/state validation. The live mecatui spike establishes runtime port variation and registration reuse; offline rejection tests remain required.
- [x] Scope and refresh defaults — Decision: DCR defaults to durable operation: when omitted, `request_refresh_token` is true and scopes resolve to `[openid, offline_access]`. Explicit `request_refresh_token: false` defaults omitted scopes to `[openid]`. Explicit scopes must equal the set selected by the refresh setting; never add `profile` or `email`. These defaults apply only to direct DCR, not preregistered, CIMD, or broker profiles.
- [x] Unknown registration outcome — Decision: no automatic registration POST retry after an unknown outcome; retain durable evidence, report recovery-required, and require explicit operator retry acknowledging possible orphan upstream clients.
- [x] Reset/retry public behavior — Decision: the two mutually exclusive DCR-only login modifiers are `--reset-dcr-registration` (valid registration and its grant) and `--retry-dcr-registration` (valid unresolved attempt only). Each proceeds to login after its conditional local operation; neither deletes or revokes an upstream client. Grant-only reset stays on the existing internal `ResetCredential` seam, with no new CLI modifier.
- [x] Pending-record coordination and recovery — Decision: one identity-keyed record transitions pending → ready by CAS, with no lease, clock-based takeover, lock service, attempt index, or exactly-once claim. A competing caller adopts a ready winner; while pending it stops recovery-required rather than joining or polling. Explicit retry replaces pending with a fresh generation and fences the old process, which may still create an orphan upstream client but cannot publish it. A valid ready winner takes precedence over a late uncertain loser. Only current and immediately previous attempt metadata are retained, not an unbounded audit history.
- [x] Registration binding and corruption recovery — Decision: fingerprint intentional issuer/resource, scopes, authentication method, grants/response types, and redirect policy/path bindings, not current issuer endpoints or cosmetic client name. Revalidate current endpoints under the existing exact issuer-origin and token-credential endpoint checks; endpoint rotation or cosmetic name drift alone never requires replacement registration. Binding changes require explicit registration reset. Corrupt/unsupported registration payloads remain recovery-required and cannot be reset by these commands; generic backend corruption is never a cache miss or a bypass. No corruption-specific schema variant or force-reset path is added.

## Interface contract

- **gRPC / protobuf:** None — direct MCP profile loading and local authorization stay host-local; no mecatui remote-server OIDC, session, or MCP broker wire contract changes.
- **Exported Go APIs / interfaces:** Proposed additions and unchanged signatures are specified below. Reuse the existing `OAuthOptions.CredentialStore`, official SDK handler, host `LoginMCP`, and `oauthlogin.Runtime`; there is no new store port, engine API, or broker interface. Internal registration resolution must preserve kind `dcr` rather than disguise a public client as the existing confidential `Preregistered` variant.
- **Tool schemas:** No schema change: discovered MCP tools retain their existing namespaced wrapper schema. The acceptance demonstration calls one harmless tool advertised read-only by the gateway; DCR itself is never model-callable.
- **CLI / config:** Direct/global DCR is exactly:

  ```yaml
  mcp:
    mode: global
    servers:
      - name: connector
        url: https://connector-gateway.stacklok.dev/gw/mcp
        auth:
          mode: oauth
          oauth:
            profile: connector
            principal: local-user
            issuer: https://connector-gateway.stacklok.dev
            client: {mode: dcr, dcr: {}}
            scopes: [openid, offline_access]
            request_refresh_token: true
            credentials:
              mode: local
              local:
                root: /absolute/owner-only/credentials
                key_env: MECATL_MCP_CREDENTIAL_KEY
            network: {additional_origins: [], private_origins: [], max_redirects: 0}
  ```

  This is a proposed direct/global profile, not a shipped configuration. The key environment reference uses the existing canonical padded-base64 32-byte encryption key, shared by login and local mecatui; it is not a new keyring dependency. Direct DCR defaults omitted `request_refresh_token` to true and omitted `scopes` to `[openid, offline_access]`; operators may spell those defaults explicitly as above. Explicit `request_refresh_token: false` defaults omitted scopes to `[openid]`. When scopes are explicit, compare sets after sorting/deduplication and require exactly the set selected by the effective refresh setting. This tri-state requires preserving field presence during strict profile decoding before resolving the existing runtime boolean; it does not widen `OAuthOptions`. Broker, preregistered, and CIMD defaults and scope rules do not change. Direct DCR rejects other scopes, inconsistent scope/refresh selection, `upstream`, nonempty `dcr.discovery_url`, static credential headers, read-only/environment credentials, non-HTTPS issuer/registration endpoints, and mixed client forms. Strict parsing preserves field presence so broker cannot accept `{}` or direct accept a supplied empty broker field. `permconfig.MCPDCRClientProfile` retains `DiscoveryURL string` with YAML tag `discovery_url`; no new DCR configuration keys are needed. Authority validation belongs in both `ResolveMCPAuthority` and the direct-only `LoadMCPProfiles` entry used by login, which must reject broker authority rather than bypass it. The approved CLI behavior is specified below; its proposed flags are not yet implemented.
- **Events / persistence:** Use the existing encrypted `mecatl-mcp-oauth` namespace and `credentialstore.Store` (`Get`, create-only/version-matched `Put`, version-matched `Delete`). Production direct DCR requires persistent cross-process CAS capabilities, already supplied by the local encrypted-file backend; test memory stores exercise the same semantics. Registration control and grant remain separate records. Exact proposed JSON schemas, hash domains, tombstones, and CAS rules below are review proposals. No registration data enters events, diagnostics, model-visible content, or unencrypted metadata.
- **Security / authority:** DCR creates an upstream-minted public client identity, so it is allowed only for trusted operator direct-MCP profiles and only through explicit host-authorized local browser consent. Preserve the OAuth controller's DNS-pinned, no-proxy, exact-issuer credential-egress, redirect, TLS, and error-redaction rules. Registration discovery/POST is subject to the same hardened client and exact issuer origin. The opaque authorization URL may reach only the explicit host browser launcher or, in explicit no-browser mode, its host-owned writer; this narrow presentation exception never permits logs, model content, or persisted output to contain the URL. Codes/state, tokens, client secrets, registration access material, and raw registration responses stay out of all diagnostic/model output. Public `none` SDK code exchange and refresh require real wire qualification: no Basic header, `client_secret`, or client assertion; S256 and resource binding remain mandatory. This adds a constrained public path only; preregistered confidential clients retain ADR 0219's Basic-only and form-secret-rejection rule. The direct authority never inherits broker DCR's `upstream` semantics.
- **Compatibility / migration:** Additive under this proposed contract. Existing preregistered and CIMD direct profiles remain byte-for-byte compatible; existing broker DCR profiles retain their broker-only validation and runtime behavior. Legacy credential records remain readable, no DCR record is synthesized from a token record, and an orphan token without a ready matching registration fails closed.

## Proposed Go surface and ordering

These are reviewable signatures, not implemented APIs. New helper names are engineering proposals; the resolved Human decisions govern the observable behavior, not arbitrary Go spelling.

```go
// internal/adapter/mcp: additive third arm; other fields unchanged.
type OAuthDCRConfig struct{}
// OAuthClientConfig gains: DCR *OAuthDCRConfig

type OAuthDCRLoginAction uint8
const (
    OAuthDCRLoginReuse OAuthDCRLoginAction = iota
    OAuthDCRLoginResetRegistration
    OAuthDCRLoginRetryRegistration
)

func PrepareOAuthDCRLogin(ctx context.Context, resource string, opts OAuthOptions,
    action OAuthDCRLoginAction) (prepared OAuthOptions, callbackPath string, err error)

// internal/app: existing LoginMCP stays unchanged and delegates with zero options.
type MCPLoginOptions struct { DCRAction mcp.OAuthDCRLoginAction }
func LoginMCPWithOptions(ctx context.Context, cfg mcp.ServerConfig,
    runtime *oauthlogin.Runtime, opts MCPLoginOptions) error

// mcp/oauthlogin: per-call path, not mutable runtime-global configuration.
func (r *Runtime) AuthorizeWithCallbackPath(ctx context.Context, expectedIssuer string,
    callbackPath string, authorize AuthorizeFunc) error
```

`PrepareOAuthDCRLogin` is host-consent-only. It validates/discovers metadata using the existing hardened client and SDK `oauthex` helpers, loads or conditionally prepares the registration record, and returns a copied `OAuthOptions` carrying an **unexported** DCR preparation ticket plus the selected callback path. It does not launch a browser or POST registration. The ticket binds identity, pending record version/generation, metadata, and path; callers cannot configure a public resolved client ID or arbitrary generation through YAML. A saved ready registration can be prepared without mutation.

`LoginMCPWithOptions` invokes preparation before binding the callback and uses `AuthorizeWithCallbackPath`. Inside its existing `AuthorizeFunc`, it sets the actual bound `RedirectURL` and `Presenter` and calls `mcp.Connect` as today. `NewOAuthController(ctx context.Context, resource string, opts OAuthOptions) (*OAuthController, error)` keeps its signature: only a matching prepared pending ticket permits one registration POST, using `oauthex.RegisterClient(ctx, endpoint, metadata, hardenedClient)`. Success must publish ready by CAS before `newOAuthPersistenceCore`. Ordinary controller construction can restore ready state but cannot prepare/POST; missing means `ErrOAuthLoginRequired`, pending/corrupt/mismatch means the new safe sentinel `ErrOAuthDCRRecoveryRequired = errors.New("OAuth DCR recovery required")`. Composition must preserve that category through `loginDiagnostic` and CLI remedies, never the raw underlying response or URL. A prepared ticket is consumed once; no handler/reconnect re-entry can send it twice. Existing single-flight authorization remains in place.

The private `oauthRegistration` carries kind `dcr`, client ID and `sdk: &oauthex.ClientCredentials{ClientID: id, Issuer: exactIssuer}` with nil `ClientSecretAuth`. Do **not** rewrite `OAuthOptions.Client.Preregistered` to achieve this: `validateOAuthRegistration`, `validateConfig`, and `newOAuthHTTPClient` currently use that arm to enforce confidential Basic. The handler's `PreregisteredClient` injection is the SDK reuse seam, not a declaration that the operator selected a confidential client. Its `DynamicClientRegistrationConfig` stays nil.

`OAuthCredentialRecordKey(resource string, opts OAuthOptions) ([]byte, error)` remains signature-compatible and pure. It rejects unresolved DCR options (no network/store side effect and no empty-ID key); internal resolved options carry the registration ID/generation for the DCR-specific key below. Preregistered/CIMD derivation stays unchanged. Registration-key derivation stays private: no new exported key helper is needed because environment single-record readers are forbidden for DCR. Existing `(*OAuthController).ResetCredential(context.Context) error` becomes grant-only tombstoning for DCR; other modes retain their current behavior. The CLI uses preparation's action rather than adding a redundant exported reset function.

`AuthorizeWithCallbackPath` accepts only `/oauth/callback/` plus canonical unpadded base64url of 32 random bytes, matching `randomCallbackPath`; it rejects query, fragment, escaping, alternate hosts, and `Options.RedirectURL` conflicts. It reuses `Runtime.Authorize`'s gate, tcp4 `127.0.0.1:0`, callback flow, request/connection bounds, cancellation and joined shutdown. Existing `Authorize` random-path behavior and `ExactRedirectURL` remain unchanged. Store the path before any POST, register the first actual bound URI, and persist that URI separately from the port-independent fingerprint. Every later authorization binds a fresh ephemeral port with the same path, uses fresh state/S256 verifier, and sends the same current URI on authorization and code exchange. Failure is never remedied by implicit re-registration. A port-policy failure at authorization prevents exchange; a server revealing rejection only at token exchange returns that error, not a fabricated claim that it failed earlier.

## Proposed registration and grant formats

Both payloads use bounded UTF-8 strict JSON (unknown fields, duplicate keys, trailing values, invalid tags/variants, identity mismatch, or size above `credentialstore.MaxValueBytes` fail closed), encrypted by the existing store envelope. Never persist the raw registration response, client secrets, registration management URI/access token, callback state, code, or verifier. Unknown optional upstream response extensions can be ignored only after validating the selected public metadata; reject a returned confidential authentication method or client secret. This is a selected public-client protocol, not arbitrary RFC 7591 lifecycle support.

### Identity-keyed registration control record, version 1

Exact fields and types (all required except those identified as optional):

| JSON field | Type and constraint |
|---|---|
| `schema`, `version` | string `mecatl.mcp.oauth-dcr-registration`, integer `1` |
| `identity` | object with string `profile`, `principal`, `resource`, `issuer`; existing resource canonicalizer and exact configured issuer |
| `generation` | canonical base64url of 32 random bytes, new on initial registration/reset/retry; not a clock or PID |
| `state` | string `pending` or `ready` |
| `attempt_started_at` | UTC RFC3339Nano string, evidence only, never a takeover deadline |
| `metadata` | exact registration-binding metadata object defined below; excludes current endpoints and cosmetic name |
| `metadata_fingerprint` | lowercase hex SHA-256 of canonical registration-binding metadata |
| `previous_attempt` | optional object: `generation` string, `attempt_started_at` string, `reason` enum `explicit_retry` or `explicit_reset`; at most one predecessor |
| `registration` | present only for ready: object with required nonempty string `client_id` and string `registered_redirect_uri`, optional nonnegative integer `client_id_issued_at` (Unix seconds) |

The opaque registration key is `SHA256(domain || frame(profile) || frame(principal) || frame(resource) || frame(issuer))`, domain literal `mecatl/mcp/oauth-dcr-registration-key/v1`. `frame` is uint32 big-endian byte length followed by UTF-8 bytes, matching `oauthCredentialKey` (the domain itself is not length-prefixed). It uses the existing `mecatl-mcp-oauth` namespace; physical filename/encryption framing stays entirely owned by `credentialstore.NewEncryptedFile`. No attempt index or store listing is needed.

Registration-binding `metadata` has exactly these fields in this canonical serialization order: `issuer`, `resource`, `redirect_policy`, `redirect_path`, `token_endpoint_auth_method`, `grant_types`, `response_types`, `scopes`. Strings are JSON-encoded with Go `encoding/json` default escaping, no whitespace/trailing newline; arrays are deduplicated and byte-lexicographically sorted. Values: `redirect_policy:"ipv4-loopback-variable-port/v1"`, `token_endpoint_auth_method:"none"`, `response_types:["code"]`, `grant_types:["authorization_code"]` plus `"refresh_token"` iff requested, and the exact scope set below. The ephemeral port, issuance timestamp, cosmetic client name, and registration/authorization/token endpoints are excluded. The registration request maps these bindings to SDK metadata with the one current `redirect_uris` URI, space-joined `scope`, and current cosmetic `client_name:"mecatl"`; `issuer`/`resource`/fingerprint/policy are local binding fields, not invented registration parameters. There is no registration lifetime/TTL knob; do not invent expiry from access-token expiry.

Current issuer endpoints are freshly discovered and validated separately from durable registration binding; they are not registration identity or fingerprint fields. Changes solely to registration/authorization/token endpoints within the permitted issuer origin, or to the cosmetic name, do not reset generation, overwrite the registration, or trigger another POST. A valid stored binding that differs from the requested scope/auth/grant/response/redirect binding requires explicit registration reset; an identity mismatch or internally inconsistent fingerprint is recovery-required, not permission to adopt another identity. Endpoint validation still applies on every use: discovery must satisfy the exact issuer-origin policy, and stored grant `refresh.token_url` must pass the existing token credential URL/origin checks and hardened credential-egress rules independently. Never blindly rewrite a stored refresh endpoint from discovery or let a matching registration fingerprint bypass token-envelope validation. If endpoint rotation makes an existing grant unusable, fail/reauthorize at the grant layer while preserving registration; registration replacement is not the remedy.

The response must contain a nonempty bounded client ID and explicitly select `token_endpoint_auth_method:"none"`; returned redirect URIs must include exactly the submitted URI, and returned grant/response types and scope, when supplied, must equal the request after canonicalization. Omitted grant/response/scope fields retain the requested effective metadata, not broader defaults. A secret or unsupported method is rejected, never stored or projected. Store only the ready projection after validation. Normal restart uses the saved registered URI as inert handler configuration when no interactive listener exists; it does not bind that old port or invent a callback. Reauthorization uses the new live URI. Reuse verifies the stored registration-binding fingerprint with the persisted path and separately revalidates current issuer endpoints before constructing the handler.

### Generation-bound DCR grant record, version 2

Keep existing preregistered/CIMD `mecatl.mcp.oauth-credential` version 1 and its key byte-for-byte. DCR uses the same schema string with `version:2`, required `identity` (existing six string fields, including `client_kind:"dcr"` and resolved `client_id`), required `registration_generation` string, and required `state` (`active` or `reset`). Active requires existing `token` and `refresh` objects with their current fields; reset forbids both. Existing `token` fields are `access_token`, `token_type`, optional `refresh_token` and RFC3339Nano `expiry`; `refresh` is `token_url`, integer `auth_style`, `redirect_url`, sorted unique `scopes`. DCR requires `auth_style:1` (`oauth2.AuthStyleInParams`) with an empty client secret, not AutoDetect/Basic. Real SDK qualification must prove this and refresh resource handling; if unsupported, delivery blocks pending an official SDK-compatible solution, not local OAuth replacement.

The DCR token key uses domain `mecatl/mcp/oauth-dcr-credential-key/v1` and length-framed profile, principal, canonical resource, exact issuer, literal `dcr`, resolved client ID, and registration generation, in that order. Both identity and generation are validated on restore. No old DCR format exists to migrate; never synthesize registration from a version-1 token or scan orphan keys.

Before authorization, a missing current-generation grant key is create-only initialized to `reset`; the flow captures that record's version **before** the SDK exchange, not at persistence time. Grant reset and `invalid_grant` write a reset tombstone with the observed version, even if already reset (to fence concurrent authorization); never delete the key. New-token and refresh writes use their captured version. Conflict adopts only a valid active current-generation winner; reset/corruption/mismatch means login/recovery-required, never rebase-and-retry the stale token. Generic network refresh failures preserve registration and existing grant evidence, as today; they do not destroy a potentially usable grant or claim refresh succeeded.

Changing registration generation makes all prior-generation grant records unreachable from the current registration. Before using a cached token, reload its current grant record so another process's reset tombstone is honored; generation equality alone does not detect a grant-only reset. Stale writers may finish a write to an old generation's key, but it is never restored/adopted as current: check the registration generation before and after saving and before returning a token to new work. There is no cross-key transaction, and a request already handed to the network before reset may complete; this is local credential invalidation, not upstream revocation. Current-generation grant tombstones close the create-only ABA hole. Old-generation encrypted grants may remain as unreachable bounded-per-generation evidence; garbage collection and secure upstream deletion are not promised. Do not claim a separate generation read plus token `Put` is atomic.

## Proposed pending-state CAS and explicit recovery

1. After successful metadata validation, explicit login reads the identity key. On genuine not-found, it create-only writes pending with random generation/path and timestamp **before** registration POST. Persistence error, including an uncertain save, means no POST. Restart after any confirmed pending write is recovery-required, even if the process crashed before sending; that conservative false-positive is preferable to an automatic duplicate POST.
2. A successful pending write gives only that invocation its private one-POST ticket. There is no persisted lease/owner and no timeout takeover. On create conflict, read the winner once: ready and matching → adopt its client/path; pending → recovery-required with no POST. A fresh ordinary login never treats pending as permission to send. This incidentally suppresses competing POSTs in the normal pending race; it does not guarantee upstream exactly-once.
3. Bind callback, submit one `oauthex.RegisterClient` POST with bounded no-redirect/no-retry transport and no initial access credential, validate the response, then CAS the exact pending version to ready. Transport timeout/cancel, malformed response, or any uncertain POST/save leaves pending evidence. A successful response with a failed durable write never proceeds to browser authorization. If a save error might have committed, re-read: a fully validated matching ready record is usable; still pending/unavailable means recovery-required, with no second POST.
4. On ready-publication conflict, never retry `Put` against a newly read version. Adopt only a matching ready winner in the **same generation**; a new generation or pending winner means this flow is stale and must stop. Do not authorize using a losing client or an old callback path. A late error likewise cannot overwrite a ready winner with pending/unknown. A ready winner is authoritative locally even though another upstream orphan may exist.
5. Explicit `--retry-dcr-registration` reads pending and CAS-replaces it with a new pending generation/path, retaining one predecessor summary; only that invocation may POST. Reject retry against ready or missing state. Explicit registration reset similarly changes a ready record to fresh pending before any new POST, invalidating old generation grants. CAS conflict in either explicit operation stops with a safe conflict category; it does not silently reset a winner. No timestamp, PID, or age can authorize retry.
6. Retry may race the old process between its final version check and upstream POST: both requests can create upstream clients. The old ticket cannot publish into the new generation, even if it completes first. This is the accepted possible-orphan residual, not an exactly-once lock. Bounded predecessor evidence records the local recovery action, not an inventory of upstream clients.
7. Corrupt, unsupported, identity-mismatched, or internally inconsistent registration payloads are recovery-required, even when the authenticated store supplies a readable version. Neither reset nor retry interprets or replaces such payloads. Preserve them for separate operator repair; there is no automated corrupt-payload reset, digest variant, or force flag. Generic backend corruption, wrong key, or unavailability likewise fails closed and can never become not-found/bootstrap or bypass validation merely because a CAS version is available. `previous_attempt.reason` remains exactly `explicit_retry` or `explicit_reset`, with the three fields specified in the schema and no alternate form.

### Proposed CLI surface

```text
mecated mcp login SERVER [--no-browser] [--permission-config PATH ...]
    [--reset-dcr-registration | --retry-dcr-registration]
```

Without a modifier, existing login behavior is preserved. For DCR: registration reset starts a new generation/client/path and subsequent grant acquisition; retry is valid-pending-only and acknowledges unknown upstream outcome. These are the only new CLI modifiers; grant-only reset remains internal and is exercised through `ResetCredential` tests. Modifiers are DCR-only for this delivery, reject repeats/combinations and non-DCR/broker/read-only profiles before mutations or browser launch. Existing `--permission-config` selection and host-only `--no-browser` URL writer remain. Plain login against valid pending state prints a safe recovery-required remedy naming the retry command and possible orphan-client consequence; corrupt state instead requires separate operator repair, never a retry/reset suggestion that bypasses validation. Output does not print client IDs, paths, tokens, endpoints from responses, or attempt payloads. All failed/incomplete operations return nonzero, and success retains the existing login success line only after authenticated initialize/list plus durable active grant. No reset-only command, automatic revocation, remote keyring action, or new mecatui command is introduced.

For a non-DCR profile, `LoginMCPWithOptions` delegates unchanged behavior only for the zero action; other actions and unknown enum values fail configuration validation. For DCR, reset-registration requires a structurally valid ready registration whose identity matches the selected key (its binding may differ from the newly requested binding); retry requires a structurally valid identity-matching pending record; a missing registration admits only ordinary bootstrap. Actions do not silently turn into one another. Corrupt registration/backend state is never admitted by either action. Existing internal grant-only reset semantics and CAS tests remain separate from these CLI actions; no new corrupt-payload interpretation or repair API is introduced.

## Exact selected SDK metadata, scopes, and resource constraints

Reuse the official SDK's discovery and `oauthex.RegisterClient` helpers, not its per-flow DCR hook. Require RFC 9728 metadata (no SDK resource-origin fallback) whose resource equals the configured canonical MCP URL and whose `authorization_servers` is exactly the configured issuer. Require discovered AS `issuer` byte-equality, including trailing slash, rather than the SDK's looser issuer tolerance. Require explicit advertised `code`, `authorization_code`, S256, and token authentication `none`; if refresh requested, require `refresh_token` and AS-advertised `offline_access`. Registration, authorization, and token endpoints are HTTPS on the exact issuer origin; token/registration URLs have no userinfo, query, or fragment. `AdditionalOrigins` never expands DCR credential egress. Redirected registration POSTs are forbidden regardless of the generic redirect bound. Metadata fetches retain existing bounded/DNS-pinned/no-proxy policy.

Requested scopes are exactly the effective set: `{openid}` without refresh and `{openid, offline_access}` with refresh. For direct DCR only, omitted `request_refresh_token` resolves to true; omitted scopes derive from that effective setting. An explicit false derives `{openid}` when scopes are omitted, and explicit scopes must equal the corresponding set. Require PRM and AS to advertise `openid`; `offline_access` need only be AS-advertised (the gateway PRM advertises only `openid`). Pass the admitted scopes through the existing `ScopeFilter`/`RequestRefreshToken` seams; the SDK's automatic offline-access addition must neither bypass configuration nor duplicate/widen the final set. Check the final host-presented authorization query as well: SDK step-up union happens after `ScopeFilter`, so an unexpected challenge/prior scope cannot sneak in `profile`, `email`, or another scope. Missing refresh issuance fails the selected refresh-required login while preserving registration; no unrequested downgrade to non-refresh success.

Authorization and code exchange each carry exactly one `resource` equal to the canonical RFC 9728 URI, not `/mcp`, issuer origin, or a challenge-supplied replacement. Refresh must carry the same bound resource; the existing persisted `oauth2.Config.TokenSource` path is not proof of that behavior. Fixture assertions must inspect actual refresh form parameters. If the current SDK/oauth2 seam cannot preserve that binding without replacing protocol logic, report implementation blockage and seek an official supported seam. DCR has no token exchange, extra audiences, scope step-up beyond the admitted set, or broker/query-agent delegation. Existing confidential `NewHardenedOAuthTokenClient` and preregistered Basic-only paths are not relaxed.

## Named offline regression fixtures

The implementation extends the existing `oauthFixture` in `internal/adapter/mcp/oauth_sdk_fixture_test.go` and `loginFixture` in `internal/app/mcplogin_test.go`; it does not export a new fixture framework. These are planned test names, not claims that DCR tests already exist:

- `internal/cliconfig/mcp_authority_test.go`: `TestDirectMCPDCR_Scenario1_AuthoritySeparatedProfile` covers selected direct/broker authority and direct-only login loading. Extend `internal/adapter/permconfig/mcp_test.go` to preserve strict syntax and the existing broker DCR regression, moving authority-dependent expectations out of syntax-only assertions.
- `internal/cliconfig/mcpprofile_test.go`: `TestADR_0325_DirectDCRProfileScopeAndStorePolicy` covers explicit scope sets, existing key reference, mutable-store-only admission, and no eager POST.
- `internal/adapter/mcp/oauth_sdk_qualification_test.go`: `TestADR_0325_PublicNoneWireQualification` covers real SDK code/refresh forms, resource, exact scope set, nil secret/Basic/assertion, alongside unchanged confidential Basic rejection tests. `TestADR_0325_DirectDCRMetadataAndEgressPolicy` tests exact issuer/resource, no fallback, method/grants/S256, unknown response fields, prohibited credentials, DNS/redirect rejection, and registration-binding fingerprint drift. The same proof rotates issuer-local registration/authorization/token endpoints and changes cosmetic name without changing registration ID/generation or issuing another POST; malformed/off-origin endpoints and invalid stored grant token URLs still fail closed.
- `internal/app/mcplogin_test.go`: `TestDirectMCPDCR_Scenario2_RegistersAuthorizesAndLists`, `TestDirectMCPDCR_Scenario2_RestartRestoresRegistrationGrantAndReadTool`, `TestDirectMCPDCR_Scenario2_ReauthorizationRedirectAndScopeBinding`, and `TestDirectMCPDCR_Scenario2_HostOnlyAuthorizationPresentation` use the real profile loader, encrypted store, `LoginMCP`/`LoginMCPWithOptions`, callback runtime, controller, initialize/list/read and second process-equivalent load. No test reaches the deployed gateway.
- `mcp/oauthlogin/runtime_test.go`: `TestADR_0325_RegistrationBoundCallbackPath` covers two ephemeral ports/same path, fresh state, wrong path/Host/state rejection, conflicting fixed redirect, canceled listener cleanup, and unchanged legacy callback modes.
- `internal/adapter/mcp/oauth_credential_test.go` and `internal/adapter/mcp/oauth_tokensource_test.go`: `TestADR_0325_DirectDCRRegistrationPrecedesTokenIdentity`, `TestADR_0325_DirectDCRRegistrationCASWinnerAdoption`, `TestADR_0325_DirectDCRGrantResetFencesStaleWriters`, and `TestADR_0325_DirectDCRStaleRegistrationLifecycle` cover strict schemas/hash framing, generation isolation, active-versus-reset conflicts, corrupt inner/outer records, and registration preservation on failures.
- `internal/adapter/mcp/oauth_controller_test.go`: `TestADR_0325_DirectDCRUnknownAttemptRecovery` uses deterministic barriers/fault-injected CAS to cover crash before POST, POST timeout, unknown save committed/not committed, pending first-login contention, retry while old POST is in flight, stale ready publication, ready winner versus late error, reset/reauthorization races, and absence of automatic duplicate POST. The proof asserts the resolved pending-contender and bounded evidence-retention policy.
- `cmd/mecated/mcplogin_test.go`: `TestADR_0325_DCRResetAndRetryCLI` covers the two mutually exclusive registration modifiers, rejection of any grant-reset CLI modifier, invalid combinations/modes, nonzero failure, safe remedies, corrupt-state rejection without writes, and zero modifiers doing no implicit reset/retry. `TestInvariant_direct_mcp_dcr_secret_redaction` and `TestDirectMCPDCR_Scenario3_RestartIdentityMismatchFailsClosed` belong to the composition fixture and seed canaries throughout the registration/grant lifecycle.

## Proposed partial-state matrix

The matrix records the resolved recovery policy in this proposed contract. Absence is not corruption. A token cannot reconstruct registration; an opaque non-enumerable store cannot detect arbitrary orphan keys after external deletion. Such orphan records are never usable, but their absence from lookup is not proof that no upstream orphan exists.

| Observed durable state | Explicit login behavior | Ordinary startup/reconnect behavior |
|---|---|---|
| No registration, no grant, no unresolved registration attempt | Bootstrap: record the selected durable attempt evidence, register, persist registration before authorization, then persist grant | Login-required; no registration POST or browser |
| Valid matching registration, no grant (including denied/cancelled login) | Reuse client; authorize only with the stable registered callback path and an ephemeral IPv4 loopback port; never register again | Login-required |
| Valid matching registration and valid grant | Reuse both; no new registration or consent | Restore and invoke |
| Valid registration, expired grant | Refresh only when refresh was requested and issued; persist rotation via existing CAS; otherwise explicit authorization | Refresh if allowed, else login-required; no hidden consent |
| Valid registration, `invalid_grant` or token reset | Preserve registration; CAS the grant to reset tombstone, then explicit authorization may follow | Login-required; stale refresh/authorization cannot replace the tombstone |
| Valid registration, transient refresh network failure | Preserve registration and grant evidence; return a redacted failure | No hidden consent or re-registration; retain existing bounded refresh behavior |
| Missing registration with a known orphaned token record | Recovery-required; never infer client ID or adopt orphaned grant | Fail closed; orphan record is unusable |
| Corrupt/unsupported/identity-mismatched registration | Recovery-required; preserve evidence for separate operator repair; reset/retry cannot bypass validation even with a readable version | Fail closed without bootstrap |
| Valid registration with changed intentional binding | Explicit registration reset required; endpoint/name changes alone are not binding changes | Recovery-required for binding mismatch; preserve registration |
| Valid registration, corrupt/mismatched grant | Recovery-required; preserve evidence and registration for separate operator repair; no new CLI repair path | Fail closed; do not replace client merely because the grant is corrupt |
| Registration POST outcome unknown, or response received but durable save uncertain | Recovery-required; no automatic repeat POST. Only `--retry-dcr-registration` may make a new fenced attempt | Recovery-required; no retry or browser |

Registration-first ordering deliberately permits registration-only state. The registration and token records are not an atomic transaction. CAS winner adoption protects a stored winner, not upstream exactly-once registration. The resolved protocol makes recovery and stale-write fencing concrete without changing that honesty claim.

## In scope — 3 scenarios, in implementation order

### Scenario 1 — Authority-separated direct DCR profile and registration resolver

The direct profile path in `internal/cliconfig/mcpprofile.go` currently selects only preregistered or CIMD credentials, while the shared `dcr` schema is explicitly broker-shaped: it requires an OAuth2 `upstream`, rejects `issuer`, and has no direct resolver. The implementation must make direct DCR an explicit third direct registration form without allowing the broker configuration to leak into direct authority. It must resolve a stored valid registration or create/persist one before the existing `auth.AuthorizationCodeHandler` is built; it must not use the SDK's per-authorization DCR hook that [ADR 0219](../adr/0219-mcp-oauth-sdk-profile.md) excluded from durable use. The direct flow follows the hardened controller and credential-store boundaries in [architecture](../architecture.md#internal-credential-store).

**Acceptance:**
- AC1.1: A valid direct profile for the canonical gateway resource selects DCR only in direct authority, requires the exact configured issuer, defaults omitted refresh/scopes to durable `[openid, offline_access]`, supports explicit no-refresh `[openid]`, and rejects inconsistent explicit selections, `upstream`, broker-only discovery payloads, mixed client forms, static credential headers, and read-only/environment credential sources before registration or browser work.
  - verify: `TestDirectMCPDCR_Scenario1_AuthoritySeparatedProfile`
- AC1.2: The registration resolver validates exact resource/issuer binding and uses only an advertised HTTPS registration endpoint on the exact issuer origin; metadata without `S256`, authorization-code, or public-client support fails closed before browser launch, token exchange, or credential write. Refresh requirements follow the approved `request_refresh_token`/scope policy rather than being a generic DCR prerequisite; reuse verifies the registration-binding fingerprint separately from revalidation of current issuer endpoints. Endpoint rotation or cosmetic name drift alone does not trigger registration replacement; existing token-credential endpoint validation remains mandatory.
  - verify: `TestADR_0325_DirectDCRMetadataAndEgressPolicy`
- AC1.3: A restored valid registration resolves the public client before `newOAuthPersistenceCore` derives the token identity, so the token key contains the resolved client ID rather than an unknown/empty placeholder; preregistered and CIMD key derivation do not change.
  - verify: `TestADR_0325_DirectDCRRegistrationPrecedesTokenIdentity`
- AC1.4: The implementation uses the existing `credentialstore.Store` conditional-write semantics for registration and token records. A CAS conflict validates and adopts the winner; it does not overwrite it or claim that competing first registrations caused exactly one upstream `/register` request.
  - verify: `TestADR_0325_DirectDCRRegistrationCASWinnerAdoption`

### Scenario 2 — Offline durable registration, authorization, and direct tool use

A hermetic loopback fixture models RFC 9728 protected-resource metadata, authorization-server metadata, a public DCR endpoint, PKCE S256 authorization-code exchange, refresh rotation, and one harmless read-only MCP tool. It drives the real direct profile loader, encrypted credential store, registration resolver, existing `LoginMCP` seam, direct `mcp.Connect`, and a second process-equivalent load. It proves the new durable registration layer and existing grant layer compose; it does not use the real gateway or a live browser. The existing local login seam is `internal/app/mcplogin.go` (`LoginMCP`), and the durable-direct-DCR boundary is recorded by [ADR 0325](../adr/0325-direct-mcp-dcr.md).

**Acceptance:**
- AC2.1: The first explicit local login registers a public client once in the uncontended fixture, requests authorization-code plus refresh-token authorization with PKCE S256, completes host-owned callback authorization, persists registration and grant records, and initializes/lists the direct MCP tools.
  - verify: `TestDirectMCPDCR_Scenario2_RegistersAuthorizesAndLists`
- AC2.2: A fresh loader/controller over the same encrypted store restores the registration and grant without browser launch or another registration POST, refreshes a short-lived token through the existing conditional token-rotation path, and calls the fixture's harmless discovered read tool.
  - verify: `TestDirectMCPDCR_Scenario2_RestartRestoresRegistrationGrantAndReadTool`
- AC2.3: Clean bootstrap, registration-only, grant-only, corrupt/mismatched records, token reset, refresh failure, and uncertain registration outcomes follow the partial-state matrix. Stale authorization/refresh results cannot resurrect reset grants; ambiguous registration never triggers an automatic repeat POST or fallback to SDK per-flow DCR.
  - verify: `TestADR_0325_DirectDCRStaleRegistrationLifecycle`
- AC2.4: Registration responses, client IDs, tokens, refresh tokens, authorization URLs/codes/state, and registration access material are absent from diagnostics, errors, model-visible tool content, and persisted non-credential metadata.
  - verify: `TestInvariant_direct_mcp_dcr_secret_redaction`
- AC2.5: Real SDK wire tests requalify public `none` code exchange and refresh: public requests carry no HTTP Basic authorization, `client_secret`, or client assertion; PKCE S256 and resource binding hold. Confidential preregistered tests retain Basic-only and form-secret rejection. An unsupported SDK path blocks delivery instead of prompting a local OAuth-stack replacement.
  - verify: `TestADR_0325_PublicNoneWireQualification`
- AC2.6: A second explicit authorization after a grant reset reuses the registered client/path with a new ephemeral port. Exact-path/Host/state checks remain enforced; a server rejecting port variation fails without silent re-registration (at authorization or exchange, wherever the rejection is observable). Requested scopes and fingerprint drift follow the selected policies, including explicit `offline_access` and missing refresh issuance.
  - verify: `TestDirectMCPDCR_Scenario2_ReauthorizationRedirectAndScopeBinding`
- AC2.7: Only an explicit browser-launcher or no-browser host-writer invocation receives the opaque authorization URL; logs, model content, and persisted output never do. Ordinary mecatui startup consumes saved credentials without presenting a URL.
  - verify: `TestDirectMCPDCR_Scenario2_HostOnlyAuthorizationPresentation`
- AC2.8: Unknown-attempt recovery and its stale-writer/ready-winner races follow the resolved bounded pending CAS protocol; no clock-based or automatic retry is introduced.
  - verify: `TestADR_0325_DirectDCRUnknownAttemptRecovery`
- AC2.9: The two approved registration CLI modifiers reject conflicting flags and corrupt registration/backend state without bypass or mutation. Internal `ResetCredential` remains grant-only for DCR and cannot revive an old authorization or refresh result; it adds no CLI modifier.
  - verify: `TestADR_0325_DCRResetAndRetryCLI`, `TestADR_0325_DirectDCRGrantResetFencesStaleWriters`
- AC2.10: Registration-bound callback path admission and cancellation preserve the existing runtime's Host/state checks, connection bounds, serialization and cleanup.
  - verify: `TestADR_0325_RegistrationBoundCallbackPath`

### Scenario 3 — Manual local mecatui qualification against the connector gateway

After offline tests pass and a human approves the exact profile and registration lifecycle, a user performs a separate manual qualification using the approved shipped `mecated mcp login SERVER` command, then local mecatui with its saved credentials, against canonical `/gw/mcp`: explicit consent opens the browser, the gateway authorizes the registered public client, a harmless discovered read tool succeeds in local mecatui, then a local restart restores registration/grant and repeats the call without re-registration or consent. This is a manual acceptance step, not CI and not permission to register, authorize, or mutate the live gateway during implementation. [ADR 0325](../adr/0325-direct-mcp-dcr.md) does not approve a new mecatui consent action.

**Acceptance:**
- AC3.1: The qualification run records only safe evidence: canonical resource, issuer origin, whether explicit consent occurred, discovered harmless tool name, success/failure category, and whether restart reused persisted registration/grant; it excludes all OAuth and registration secrets.
  - verify: demonstration — human-run local mecatui checklist after offline gates; live registration/login/tool calls are intentionally not automated
- AC3.2: Restart retains the registration and grant only when both encrypted records validate against the same profile/principal/resource/issuer identity; otherwise local mecatui reports a redacted login-required/reset-required category and does not issue a hidden registration or browser request.
  - verify: `TestDirectMCPDCR_Scenario3_RestartIdentityMismatchFailsClosed`

## Out of scope

| Item | Defer-to | Decision |
|---|---|---|
| Remote mecatui server OIDC/keyring | Existing remote-OIDC work | Explicitly excluded; this plan concerns local direct MCP only |
| MCP broker DCR | [MCP broker DCR client plan](mcp-broker-dcr-client.md) | Broker retains its ToolHive-owned DCR lifecycle |
| `CallMcpWithQuery` | Separate MCP query work | No change to its transport or authorization path |
| Live registration, login, refresh, or tool calls during automated tests | Manual qualification after approval | CI remains hermetic and offline |
| Exactly-once registration, leases, attempt index, unbounded audit archive | Follow-on only for a demonstrated requirement | The resolved identity-keyed pending evidence uses existing CAS with explicit retry, not a distributed lock/lease or upstream exactly-once guarantee |
| Registration access-token management, client update/delete, and automatic re-registration | Follow-on after a concrete gateway need | No initial access credential or unbounded lifecycle is introduced |
| Per-session/client/inline-agent OAuth | Existing MCP OAuth boundary | OAuth remains named global static-profile only |

## Definition of done

1. Before implementation orchestration, this plan is human-marked `approved`, ADR 0325 is accepted in the merged baseline, and ADR 0325's supersession metadata explicitly governs the public-`none` and durable-DCR exceptions to ADRs 0219 and 0220.
2. Applicable `task lint`, `task test`, `task docs`, and `task api:check` gates pass.
3. `task ac-trace-strict` resolves every named proof when the plan becomes `landed`.
4. `go run ./cmd/mecademo` remains green for runtime changes.
5. The implementation PR reports interface conformance and a separate human-run gateway qualification using canonical `/gw/mcp`; neither live mutation nor live success is claimed by CI.
6. `/panel-review` reports no ship blockers or unwaived reviewer failures.
7. The implementation updates relevant `user-docs/` pages, generated configuration reference, architecture and usage docs in the same PR; `task site:build` passes. Public docs distinguish the approved consent command, saved-credential mecatui use, callback/scopes policy, and recovery from remote-server OIDC.
8. `docs/adr/0027-cloud-native.md` inventories the durable registration control record, generation-bound grant/reset records, in-process one-use preparation ticket, and any new outlives-a-call client/runtime state in List 1 and, where restart fidelity applies, List 2; review verifies ownership, cleanup, and reattachment decisions rather than relying only on citation lint.

## Deferred decisions and known risks

- All human decisions required for implementation are resolved; this plan is `proposed` pending human plan/interface review, not approved or landed. Go names are not separate human-policy decisions. Registration-binding versus endpoint revalidation and fail-closed corrupt-state handling follow the review corrections above.
- The merged approved baseline must accept ADR 0325 and make its public-`none` and durable-DCR supersession of ADRs 0219/0220 explicit before orchestration; the proposed documents intentionally do not authorize implementation yet.
- Existing DCR schema/types are broker-only despite sharing the `dcr` label. The proposed direct empty payload and authority-selected validation must preserve broker compatibility.
- The official SDK supports public client credentials and `oauthex.RegisterClient`, but per-flow DCR has no durable registration hook ([ADR 0219](../adr/0219-mcp-oauth-sdk-profile.md)). API availability is not wire qualification; persistent refresh resource/auth-style behavior is an explicit implementation gate.
- CAS prevents destructive overwrites, not upstream orphan creation after an uncertain POST or explicit retry racing an old request. Reset is local invalidation, not remote revocation, and no cross-record atomicity is claimed.
- Implementation must inventory the registration control record, generation-bound grants/tombstones, preparation ticket and existing-runtime/client lifetimes in the living resource/fidelity documentation. Do not amend frozen accepted ADRs in this contract-only task.