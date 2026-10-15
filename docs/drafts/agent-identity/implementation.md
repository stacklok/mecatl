# Implementation and verification

This companion records implementation and verification requirements for the [agent identity and authority design](README.md). Requirements here are not claims about current implementation status or known vulnerabilities. The credential flow follows [credentials and lifecycle](credentials-and-lifecycle.md); its decisions are recorded in the [credential flow rework](credential-flow-rework.md).

## Session ownership and isolation

Verify that session enumeration, session access, and event replay enforce the authenticated caller's ownership, including on shared storage. Include negative tests proving another user cannot obtain session titles, prompts, or archived conversation content.

Session ownership is keyed by the company identity in the validated AS-1 access token's `sub_id`, not by the token's own `(iss, sub)`, so existing owners need no migration. Test the following:

- mecak8s rejects an AS-1 token without `sub_id`.
- It accepts `sub_id` only from tokens AS-1 issued.
- Two AS-1 subjects can never carry the same company identity.

Verify that scheduled invocations preserve owner attribution without treating saved ownership as fresh user authentication or permission to use a provider connection.

## SPIFFE identifier construction

Use literal, nonempty SPIFFE-safe definition names (`[A-Za-z0-9._-]+`, except `.` and `..`); reject invalid names rather than rewriting them. Agent-instance incarnations use persisted `inc_` values with 26 lowercase base32 characters, or deterministic `legacy_` values for older snapshots.

Apply any issuance limit to the **whole URI**, including child hops, without truncating it. [SPIFFE-ID §2.3](https://github.com/spiffe/spiffe/blob/main/standards/SPIFFE-ID.md#23-maximum-spiffe-id-length) requires support through 2048 bytes and advises against generating longer IDs; a Mecatl cap and compatible name/depth bounds remain undecided. Legacy-name compatibility is also open. The path has no version segment, so incompatible grammar requires migration.

## AS-1 grant and AS-2 user resolution

The flow has four integration points: AS-1 sign-in, the session-open exchange, the per-call ID-JAG, and AS-2 redemption. The original company token never leaves AS-1: AS-1 is an ordinary OIDC client of the company IdP, and nothing downstream receives or exchanges the company token.

**Client authentication.** The broker is one OAuth client, `mecatl`, registered at both AS-1 and AS-2. On every token-endpoint request it authenticates with `private_key_jwt`: a short-lived assertion with `iss` = `sub` = `mecatl`, `aud` = that server's issuer identifier, and a single-use `jti`. ID-JAG §4.4.1 requires the ID-JAG's `client_id` to match the client redeeming it at AS-2, so both servers must use the same client ID. A `client_secret` is not accepted. In external mode the gateway administrator registers the broker's `jwks_uri` at AS-2 once.

**Sign-in.** mecak8s's protected-resource metadata names AS-1. The TUI and Studio sign in there with authorization code and PKCE and receive an AS-1 access token (`typ=at+jwt`, `aud` = the Mecatl API) and their own refresh token. mecak8s validates the access token under RFC 9068 and checks session ownership. An `id_token` is not accepted.

**Session open.** At `OpenSession`, mecak8s passes the user's current AS-1 access token to the broker once, alongside workload authentication. It replaces the harness-reported `human {issuer, subject}`. The broker exchanges it at AS-1 with RFC 8693: `subject_token_type=urn:ietf:params:oauth:token-type:access_token`, `requested_token_type=urn:ietf:params:oauth:token-type:refresh_token`. AS-1 accepts the token only if:

- AS-1 issued it, its `aud` is the Mecatl API, it is still active, and its `jti` has not been converted before;
- the presenter is the authenticated client `mecatl`;
- the request asks for no wider scope than the original grant.

Client `mecatl` may convert tokens issued to the TUI and Studio only. A session-open request, using the [main flow](credentials-and-lifecycle.md) values:

```text
POST https://broker.example.com/as/oauth/token
grant_type=urn:ietf:params:oauth:grant-type:token-exchange
subject_token=<Alice's AS-1 access token, aud=https://mecatl.example.com/api>
subject_token_type=urn:ietf:params:oauth:token-type:access_token
requested_token_type=urn:ietf:params:oauth:token-type:refresh_token
client_id=mecatl
client_assertion_type=urn:ietf:params:oauth:client-assertion-type:jwt-bearer
client_assertion=<JWT signed with the broker's private key>
```

Accepting a token whose `aud` is the Mecatl API from a different client is an explicit, narrow AS-1 policy for client `mecatl`, not a general audience bypass. AS-1 issues a broker refresh token, such as `RT-A42`, which is opaque to the broker. Its meaning lives in an AS-1 record:

| Field | Example | Purpose |
| --- | --- | --- |
| Client | `mecatl` | Only the authenticated broker can use it. |
| AS-1 `sub` | `u-alice-91` | Subject of every ID-JAG issued from it. |
| Broker session reference | `A42` | The session binding. |
| Host session and incarnation | `hostS42` | Restart and recovery continuity. |
| Source access-token `jti` | | One conversion per session. |
| Issue time and hard expiry | | Authority horizon. |
| Rotation chain | | Reuse detection. |

The broker supplies the session reference. Rotation inherits the record and does not extend the hard expiry. Session close revokes the token and deletes the record. For now, the refresh token is not tied to the user's AS-1 login session.

**Per-call ID-JAG.** When no cached `V` exists for (session, instance, gateway), the broker asks AS-1 for an ID-JAG ([draft-ietf-oauth-identity-assertion-authz-grant-04](https://datatracker.ietf.org/doc/draft-ietf-oauth-identity-assertion-authz-grant/)) with RFC 8693: the broker refresh token as `subject_token` (`subject_token_type=urn:ietf:params:oauth:token-type:refresh_token`), the agent-instance JWT-SVID as `actor_token` (`actor_token_type=urn:ietf:params:oauth:token-type:jwt`), `requested_token_type=urn:ietf:params:oauth:token-type:id-jag`, and AS-2's issuer as `audience`. AS-1:

1. resolves the refresh token to its record and checks that it is bound to `mecatl`, that the session is open, and that the hard expiry has not passed;
2. validates the SVID against its agent trust bundle, registered subject, and a configured audience accepted by both AS-1 and the broker invocation validator, and checks that the instance is registered to this session;
3. authorizes the combination of client, subject, actor, and target AS-2;
4. issues the ID-JAG.

The request, for the reviewer instance in the main flow:

```text
POST https://broker.example.com/as/oauth/token
grant_type=urn:ietf:params:oauth:grant-type:token-exchange
subject_token=RT-A42
subject_token_type=urn:ietf:params:oauth:token-type:refresh_token
actor_token=reviewerSVID
actor_token_type=urn:ietf:params:oauth:token-type:jwt
requested_token_type=urn:ietf:params:oauth:token-type:id-jag
audience=https://gateway.example.com
client_id=mecatl + client assertion
```

The ID-JAG has header `typ=oauth-id-jag+jwt`, AS-1 `iss`, the AS-1 `sub` from the refresh token record (never from caller input), AS-2 `aud`, `client_id=mecatl`, `jti`, `iat`, a short `exp`, and a single-level `act`. `act.sub` is an opaque, stable, per-instance `agent:` identifier for the acting instance, never a SPIFFE path or definition name, and `act.iss` is omitted. AS-1 derives it from the validated `actor_token`; the full SVID stops at AS-1. There is no nested `act`: the delegation that led to the call stays in Mecatl's harness and broker records. Nesting comes later with the [chained exchange](credential-flow-rework.md#d4-delegation-visibility-at-the-gateway). `aud_sub` and `email` are deliberately omitted. AS-1 and AS-2 are separate issuers with separate keys, also in embedded mode (§9.3).

**AS-2 redemption.** The broker redeems the ID-JAG at AS-2 with the RFC 7523 JWT-bearer grant, authenticating again as `mecatl`. AS-2 checks the trusted AS-1 issuer and signature, `typ`, that `aud` is its own issuer, lifetime, that `client_id` matches the authenticated client, and that `jti` is unused (§4.4.1). It then maps the AS-1 subject to a native user:

| Mode | Mapping |
| --- | --- |
| Embedded | Users log in to AS-2 through AS-1, so the AS-1 `(iss, sub)` maps directly to the native user. Consent is implicit. |
| External | AS-2 redeems only for an AS-1 `(iss, sub, client_id)` with a consent link to a native user, created once through AS-2's own login. No just-in-time provisioning. |

AS-2 never resolves a user through `sub_id` (§9.5), email, or display name. Unknown subjects fail closed. A native-user match does not authorize use of a provider connection.

AS-2 issues `V`: `typ=at+jwt`, `sub` = native user, the ID-JAG's `act` unchanged, `client_id=mecatl`, `aud` = the vMCP resource, and `jti`. It issues no refresh token (§4.4.3); after expiry the broker obtains a new ID-JAG from the broker refresh token. If the refresh token has expired or been revoked, fail closed until a new session open supplies a fresh AS-1 access token.

**vMCP and the upstream credential.** vMCP validates `V` under RFC 9068 and, if the request uses a `resource` parameter, RFC 8707. It binds its session to the user and `act.sub`. It loads the upstream credential keyed by (gateway, native user, provider). A missing credential answers "needs authorization", which the broker returns as the existing `mcp_authorization_pending` outcome. Consent is checked at redemption, not here. There is one credential record per (user, provider), refreshed as a single row with compare-and-swap; the enterprise connector gateway already stores credentials this way.

The broker must propagate the operation ID, such as `op-pr42-001`, with each MCP request, and vMCP must log it with its decision as reported, unverified trace context. A cached `V` serves several calls, so its `(iss, jti)` cannot identify one operation; with a single-level `act`, the operation ID is also what leads from a gateway record to the delegation in Mecatl's records. The mechanism, a trace header or MCP request `_meta`, is open, and so is the persistence ordering of these correlation records.

**Open.** How an external gateway reports "needs authorization" or "consent required" to the broker as a structured outcome. No callback or private mapping-response fields are specified. Candidates include an AS-2 login chain through AS-1, MCP URL elicitation, and the connector gateway's connect and status APIs.

## Credential verification

Operator configuration fixes accepted workload and agent issuers, audiences, identities, and trusted signing keys. Token claims do not select verification endpoints, CA trust, or server names. For each call, the broker authenticates the workload and validates the agent-instance JWT-SVID against its configured trust bundle, registered subject, broker audience, expiry, and permitted presenter. JWT-SVID does not require `iss` or `jti`.

Test client authentication at both servers:

- AS-1 and AS-2 reject requests without a valid `private_key_jwt` assertion for `mecatl`, including a wrong `aud`, an expired assertion, a reused `jti`, and a key not registered for `mecatl`.
- A leaked AS-1 access token or broker refresh token presented without the broker's key is rejected.

Test the session-open exchange:

- AS-1 rejects access tokens it did not issue, tokens for another audience, expired or revoked tokens, a second conversion of the same `jti`, and requests for wider scope.
- AS-1 accepts a Mecatl-API-audience token only from client `mecatl`; another client presenting the same token is rejected.
- The refresh token record names the session; rotation keeps the hard expiry; reuse of a rotated token is detected; session close revokes it.

Test ID-JAG issuance:

- AS-1 rejects an expired, revoked, or foreign-client refresh token, and a refresh token whose session is closed or past its hard expiry.
- AS-1 rejects an untrusted or expired SVID, an SVID with an audience not accepted by both AS-1 and the broker, and an instance not registered to the refresh token's session.
- The issued ID-JAG has `typ=oauth-id-jag+jwt`, the `sub` from the refresh token record, `client_id=mecatl`, AS-2 `aud`, a fresh `jti`, and an `act` with exactly one level, whose `sub` is the opaque `agent:` identifier of the validated actor and which has no nested `act` and no `act.iss`. A caller cannot change `sub` or `act` through request parameters.

Test AS-2 redemption:

- AS-2 rejects a wrong `typ`, untrusted issuer, wrong `aud`, expired ID-JAG, reused `jti`, and `client_id` that does not match the authenticated client.
- In external mode, AS-2 rejects a subject with no consent link and a revoked link. A link for another AS-1 issuer or client does not match.
- AS-2 never resolves through `sub_id` or email; a matching `sub_id` without a mapping fails closed.
- `V` carries the native user, the ID-JAG's single-level `act` unchanged, `client_id=mecatl`, and the vMCP audience, with no refresh token.

Test vMCP and the upstream credential:

- vMCP rejects a `V` with the wrong issuer, audience, `typ`, or expiry.
- vMCP loads the credential for (gateway, native user, provider) of a delegated `V` that carries no `tsid`, and returns "needs authorization" when none exists. One user's `V` never loads another user's credential.
- Two agent instances acting for the same user get separate vMCP sessions.
- vMCP records the operation ID the broker propagated with each request, marked as reported. Two calls served by one cached `V` log different operation IDs.

Verify that preliminary broker denial, AS-1 denial, AS-2 denial, and vMCP denial each prevent provider dispatch, and that a missing consent link or upstream credential surfaces as `mcp_authorization_pending`. Verify that AS-1 access tokens, broker refresh tokens, ID-JAGs, `V`, and client assertions never reach model or tool content, transcripts, logs, environment variables, or audit records, including errors and diagnostics. The server side never stores the AS-1 access token durably. An expired or revoked broker refresh token denies new ID-JAG issuance; it must not be replaced by a reported issuer/subject pair or a fabricated continuation.

Verify issuance rejects a known definition without a trusted particular-instance registration, an unauthorized workload, mismatched ancestry, or a stale agent-instance incarnation. These are proposed checks under the [workload-authenticated mecak8s lifecycle registration](agent-definitions.md#registering-instances); the exact wire API, durable schema, and protection from same-process substitution remain unspecified.

Verify [identity continuity](agent-definitions.md#identity-continuity-across-runs-and-restarts) preserves the instance identity, fixed definition selection, and original bounds. Test [attachment recovery](credentials-and-lifecycle.md#pauses-and-restart) with a replacement process in the same authenticated workload and deployment partition; reject a different workload or possession of a noncredential broker session reference alone.

Test user resolution and connection entitlement separately: neither an OAuth success nor a native-user match grants access to a provider connection.

## ToolHive work list

AS-1 and AS-2 are ToolHive authorization server instances, and vMCP is ToolHive's. The rework checked these capabilities against ToolHive main `ce74cfa37` by reading, not running, the code. Sizes are the rework's estimates.

| Capability | Today | Size | When |
| --- | --- | --- | --- |
| Exchange of a self-issued access token presented by another confidential client (the Mecatl-API-audience exception) | Mostly; the requested `aud` must be a subset of the subject token's `aud` | Small to medium | Identity work |
| Refresh token in a token-exchange response, with the session-bound record | No; exchange issues access tokens only | Medium to large | Identity work |
| Issue ID-JAGs (`typ=oauth-id-jag+jwt`, audience = another AS) | No; redemption and an outbound XAA client only. The ID-JAG redemption handler binds the client, consumes `jti`, and copies `act`; the plain JWT-bearer handler drops `act` and replaces the client | Large | Identity work |
| Refresh token as `subject_token` | No | Large | Identity work |
| Single-level `act` from the registered actor input (nesting later, with the chained exchange) | Partly; `buildActClaim` builds `act` from a client or self-issued actor token and nests it | Medium | Identity work |
| ID-JAG redemption keeps `act` | Yes, copied unchanged | None; verify only | Identity work |
| AS-2 maps the AS-1 subject to a native user after ID-JAG redemption, and stops marking the token as having no upstream session | No; emits `iss#sub` and marks no upstream session | Large | Identity work |
| Per-user upstream credential lookup in open-source ToolHive, keyed (gateway, native user, provider) | No; keyed by `tsid`, a per-interactive-login session ID that a delegated `V` lacks, and vMCP loads nothing for a token without one (`pkg/auth/token.go:1253`); the enterprise per-user decorator is build-tagged | Medium | Identity work |
| Consent link at AS-2's own login (external mode only) | No | Not sized | Identity work |
| `private_key_jwt` for client `mecatl` at AS-1 and AS-2 | Yes | Configuration | Identity work |
| SPIFFE client authentication at the authorization server | Yes | None; verify only | Identity work |
| AS-1 and AS-2 [audit records](audit.md) | No; the authorization server records no audit events. The plan reuses ToolHive's audit event recorder library, which emits JSON records for collection with fluentd or fluent-bit | Not sized | Identity work |
| Refresh token bound to the AS-1 login session (`sid`), so logout revokes it | No; only `tsid` exists | Large | Later |
| DPoP issuance and validation for `V` | No | Large | Later |
| Scope or `authorization_details` (RFC 9396) narrowing enforced per tool at the gateway | Scope intersection only | Large | Later |
| Cedar at the gateway reading `act` | No | Part of the narrowing work | Later |

The consent link needs the link record, the AS-2 authorization request carrying an AS-1-signed subject hint, the consent screen, revocation, and link-swap defenses. Its creation flow is under [external consent link](#external-consent-link); its requirement is in the [rework record](credential-flow-rework.md#d2-grant-redeemed-at-as-2). Whether "Allow Mecatl" and the provider's consent screen can be one redirect chain is open.

Until the narrowing work lands, the broker alone enforces the agent ceiling; the gateway enforces user and connection.

## External consent link

In external mode the consent link maps AS-1's subject to AS-2's native user and is the user's first-hand statement at the gateway that Mecatl may act for them. The mechanisms below are candidates, not decisions.

**Record:** (native user, AS-1 issuer, AS-1 `sub`, client `mecatl`, granted at, scope or providers). AS-2 redeems an ID-JAG only when its `iss`, `sub`, and `client_id` match a link; the link supplies the native user.

**Creation, once per user and gateway:**

1. The first call fails with "link required", returned through the existing `mcp_authorization_pending` path.
2. Mecatl starts an authorization request at AS-2 carrying an AS-1-signed hint that names the AS-1 subject, such as a `login_hint_token` or a signed request object.
3. The user authenticates through AS-2's own login and approves "Allow Mecatl (as alice@acme) to use your connections".
4. AS-2 records the link and redirects back; the run resumes.

**Link swapping.** An attacker starts a link flow with their own AS-1 subject and tricks the victim into completing it. The victim's gateway account is then linked to the attacker's AS-1 subject, and the attacker's Mecatl sessions use the victim's upstream credentials. Defenses:

- PKCE and `state` bind the flow to the browser that started it;
- the consent screen shows which Mecatl account is being linked;
- where possible, the identity consistency check below.

**Shared upstream IdP.** When AS-1 and AS-2 federate to the same company IdP:

- **Consistency check:** at link time, AS-2 compares the IdP identity the user just authenticated with against the upstream IdP identity in AS-1's signed hint, and refuses on mismatch. This closes link swapping.
- **Silent step:** AS-2's login is silent through single sign-on, and the gateway administrator may pre-approve the consent screen for client `mecatl`. The link becomes a one-time redirect bounce without UI, which Mecatl can run right after the user's first sign-in. Pre-approval is safe here, unlike blanket admin pre-consent, because the IdP and AS-2 still witness the identity first-hand.
- **The bounce is still required.** A shared IdP does not let AS-2 accept AS-1's `sub_id = (IdP, user)` directly: AS-1, not the IdP, signs that claim (§9.5), so a compromised AS-1 could name any user.

**Removing the link entirely** has two routes, each with a stated cost:

- **Risk decision:** the gateway administrator trusts AS-1 to assert IdP identities for all users, optionally limited to a group. A compromised AS-1 then reaches every such user's gateway credentials, which is embedded-mode trust applied to an external gateway.
- **Future enterprise path:** the company IdP itself issues the ID-JAG for AS-2 (XAA at the IdP), with AS-1 as the IdP's client using its stored upstream refresh token. It needs IdP support and loses `act` at the gateway.

## Definition provenance

- Mecatl: `AgentDefSource` stays storage-independent: provenance profiles travel as metadata, not as additions to the core interface. Each profile defines required fields, schema, hashing rules, and revision semantics. `AgentDefSource` resolution returns source evidence; the trusted resolver produces the [provenance record](agent-definitions.md#definition-provenance) with both digests, rejects profiles the operator has not allowed, and registration carries the record. Prove a locally modified file yields a source digest that does not match its claimed commit's content.
- Broker: validate the record against its profile contract and the operator's evidence requirements, retain it with the instance record, and mark verified versus asserted facts in audit.

## Workload-specific verification

### Kubernetes ServiceAccount bearer

The broker validates the configured Kubernetes issuer, audience, and ServiceAccount allowlist. A stolen ServiceAccount token remains a bearer credential until expiry.

### SPIRE workload JWT-SVID

`mecak8s` obtains a platform JWT-SVID for the broker's audience through the SPIFFE Workload API and presents it on issuance and invocation requests. The broker checks signature, audience, expiry, and allowed workload SPIFFE ID. Platform credential renewal and trust-bundle refresh are separate from the broker issuer's key rotation; platform identity neither replaces agent-instance issuance nor creates federation.

### SPIRE workload X.509-SVID

`mecak8s` obtains an X.509-SVID and key through the Workload API and proves possession during mTLS to both broker endpoints. The broker checks platform trust and permitted workload IDs. A TLS-terminating ingress needs explicitly trusted identity propagation; a forwarded header alone is insufficient. Rotation, bundle refresh, and connection renewal must preserve mTLS checks without bearer fallback.

Workload mTLS does not bind an OAuth access token to its certificate, prove user consent, or isolate agents sharing a harness process.

## Cedar authorization and audit verification

The ceiling checks below belong to the [authority work](README.md#order-of-work), which follows the identity work; the audit checks belong to the identity work. Cedar is the selected engine for the effective MCP server/tool ceiling. Policy representation is an implementation choice; user, resource, argument, and dynamic delegation policies are deferred. Required focused checks are:

- Resolve `Tools`, `DisallowedTools`, and `MCPServers` with existing defaults, selection, and exclusion semantics into registered server/tool pairs. Reject ambiguous or unresolved grants; the same tool name on another server must not inherit access.
- Prove deterministic generation from fixed admitted definition inputs, with retained source, definition revision, and generated policy revision. Prose and model output must not affect grants; source edits and restoration must not silently replace the admitted ceiling.
- Select the actor's policy through trusted registration. Reject caller substitution of actor, definition, revision, or policy. Require an explicit resolved main ceiling, without name-based privilege.
- Compute each instance's effective ceiling at registration as its definition ceiling intersected with its parent's effective ceiling, and store it with the incarnation. Property-test `effective(child) ⊆ definition(child)` and `effective(child) ⊆ effective(parent)`, and cross-check against the harness's delegated-authority intersection. Prove a `deployer` child of a read-only reviewer is denied merge. Reject registration under an unregistered, revoked, or other-session parent; an empty intersection registers an instance whose MCP calls all deny. Revoking an instance revokes its descendants. Renewal and restoration reload the stored set and never recompute or widen it.
- Evaluate each call, including calls using cached credentials and retries after enrollment. Default deny; generation/loading failures, evaluation failures, and any evaluation diagnostic errors deny, even if the nominal Cedar result is allow.
- Check the exact dispatched server/tool pair and recheck changed requests. Prove the reviewer read passes its ceiling while excluded merge fails; PR 42 and PR 43 reads on the same pair have the same ceiling result. Do not claim this proves PR-specific delegation.
- Preserve existing local checks, AS-1 and AS-2 decisions, and vMCP checks. A ceiling permit must not bypass their denials or substitute for user entitlement. The gateway does not evaluate the agent ceiling; Cedar reading `act` there is later work.
- Verify audit provenance, definition and policy revision correlation, safe reasons and policy IDs, and distinct decision/dispatch/outcome evidence per operation and attempt. Diagnostic errors must yield an effective deny in the record. Redaction must exclude secrets and unrestricted payloads. Test unknown outcomes without automatic replay and durability across restart once storage, ordering, and failure handling are specified; no transactional ledger is selected here.

## Parking and recovery: implementation status

Protected MCP calls can park while awaiting provider reauthorization and resume with account, connection, catalogue, and permission checks. Offline fixtures cover credential and enrolled-connection recovery after host and broker replacement.

Parked broker flows remain process-local. Resuming the same pending operation after broker replacement and renewing agent-instance JWT-SVIDs across runs remain unfinished integration work. The existing recovery path does not establish authenticated human ownership. After broker replacement, recovery must restore the broker session and its workload and host binding and find the broker refresh token's AS-1 record still valid; if the refresh token has expired or been revoked, tool calls fail closed until a new session open. Credential recovery does not recover external effects; unknown outcomes remain fenced against automatic replay.
