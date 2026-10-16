# Implementation and verification

This companion records implementation and verification requirements for the [agent identity and authority design](README.md). Requirements here are not claims about current implementation status or known vulnerabilities. The credential flow follows [credentials and lifecycle](credentials-and-lifecycle.md).

## Session ownership and isolation

Verify that session enumeration, session access, and event replay enforce the authenticated caller's ownership, including on shared storage. Include negative tests proving another user cannot obtain session titles, prompts, or archived conversation content.

Session ownership is keyed by the company identity in the validated AS-1 access token's `sub_id`, not by the token's own `(iss, sub)`, so existing owners need no migration. Test the following:

- mecak8s rejects an AS-1 token without `sub_id`.
- It accepts `sub_id` only from tokens AS-1 issued.
- Two AS-1 subjects can never carry the same company identity.

Verify that scheduled invocations preserve owner attribution without treating saved ownership as fresh user authentication or permission to use a provider connection.

## SPIFFE identifier construction

Use literal, nonempty SPIFFE-safe definition names (`[A-Za-z0-9._-]+`, except `.` and `..`); reject invalid names rather than rewriting them. Agent-instance incarnations use persisted `inc_` values with 26 lowercase base32 characters, or deterministic `legacy_` values for older snapshots.

Apply any issuance limit to the **whole URI**, including child hops, without truncating it. [SPIFFE-ID §2.3](https://github.com/spiffe/spiffe/blob/main/standards/SPIFFE-ID.md#23-maximum-spiffe-id-length) requires support through 2048 bytes and advises against generating longer IDs. The proposed caps are names up to 63 bytes and up to 8 child levels below the root, which keeps the longest subject near 1,400 bytes; registration past either cap is refused. Legacy-name compatibility is also open. The path has no version segment, so incompatible grammar requires migration.

## AS-1 grant and AS-2 user resolution

The flow has four integration points: AS-1 sign-in, the session-open exchange, the per-call ID-JAG, and AS-2 redemption. The original company token never leaves AS-1: AS-1 is an ordinary OIDC client of the company IdP, and nothing downstream receives or exchanges the company token.

**Client authentication.** The broker is one OAuth client, `mecatl`, registered at both AS-1 and AS-2. On every token-endpoint request it authenticates with `private_key_jwt`: a short-lived assertion with `iss` = `sub` = `mecatl`, `aud` = that server's issuer identifier, and a single-use `jti`. ID-JAG §4.4.1 requires the ID-JAG's `client_id` to match the client redeeming it at AS-2, so both servers must use the same client ID. A `client_secret` is not accepted. In external mode the gateway administrator registers the broker's `jwks_uri` at AS-2 once.

**Sign-in.** mecak8s's protected-resource metadata names AS-1. The TUI and Studio sign in there with authorization code and PKCE and receive an AS-1 access token (`typ=at+jwt`, `aud` = the Mecatl API) and their own refresh token. mecak8s validates the access token under RFC 9068 and checks session ownership. An `id_token` is not accepted.

**Session open.** At `OpenSession`, mecak8s passes the user's current AS-1 access token to the broker once, alongside workload authentication. It replaces the harness-reported `human {issuer, subject}`. The broker exchanges it at AS-1 with RFC 8693: `subject_token_type=urn:ietf:params:oauth:token-type:access_token`, `requested_token_type=urn:ietf:params:oauth:token-type:refresh_token`. AS-1 accepts the token only if:

- AS-1 issued it, its `aud` is the Mecatl API, it is still active, and its `jti` has not opened another session;
- the presenter is the authenticated client `mecatl`;
- the request carries no `scope` parameter (`invalid_scope` otherwise);
- the token was issued after the user's last user-wide revocation, if any;
- the company identity in the token's `sub_id` equals the session owner that mecak8s sends with it;
- the broker session has no recorded owner yet, or its recorded owner is that same identity. AS-1 records the owner at the first open, and session IDs are never reused.

Client `mecatl` may convert tokens issued to the TUI and Studio only. A session-open request, using the [main flow](credentials-and-lifecycle.md) values:

```text
POST https://broker.example.com/as/oauth/token
grant_type=urn:ietf:params:oauth:grant-type:token-exchange
subject_token=<Alice's AS-1 access token, aud=https://mecatl.example.com/api>
subject_token_type=urn:ietf:params:oauth:token-type:access_token
requested_token_type=urn:ietf:params:oauth:token-type:refresh_token
authorization_details=[{"type": "mecatl_session", "session": "A42",
  "owner": {"iss": "https://login.example.com", "sub": "alice-204"}}]
client_id=mecatl
client_assertion_type=urn:ietf:params:oauth:client-assertion-type:jwt-bearer
client_assertion=<JWT signed with the broker's private key>
```

Accepting a token whose `aud` is the Mecatl API from a different client is an explicit, narrow AS-1 policy for client `mecatl`, not a general audience bypass. AS-1 issues a broker refresh token, such as `RT-A42`, which is opaque to the broker. Its meaning lives in an AS-1 record:

| Field | Example | Purpose |
| --- | --- | --- |
| Client | `mecatl` | Only the authenticated broker can use it. |
| AS-1 `sub` | `u-alice-91` | Subject of every ID-JAG issued from it. |
| Owner | `(https://login.example.com, alice-204)` | The session owner, recorded at the first open; later opens for another user are refused. |
| Broker session reference | `A42` | The session binding. |
| Host session and incarnation | `hostS42` | Restart and recovery continuity. |
| Source access-token `jti` | | Refuses a second session opened with the same token. |
| Issue time and hard expiry | | Authority horizon. |
| Source schedule grant | | For a scheduled run's session only: revoking the schedule grant revokes this refresh token. |
| Revocation reason | | Set when revoked: `session_closed`, `signed_out`, `upstream_revoked`, or `owner_action_required`. |

The broker supplies the session reference. The `mecatl_session` type requires AS-1 to echo the entry unchanged (RFC 9396 §7); the broker checks it is identical, revokes the refresh token and fails the open if not, and otherwise stores the refresh token with the echoed owner in its shared session storage. AS-1 refuses an open while the session already has a live refresh token. A retry of the same open, with the same `jti`, client, and session, within the source token's `exp`, while the session is open and its refresh token is not revoked, revokes the first refresh token and issues a new one; AS-1 writes the converted-`jti` record, the owner, and the refresh-token record in one transaction. The broker runs at most one open per session at a time, and a retry carries a new client assertion. The refresh token does not rotate; client authentication already binds it to the broker. Session close revokes the token and keeps a closed-session record (ID, owner, closing time) so the session is never reopened; the converted `jti` is kept until the source token's `exp` plus the clock skew. Losing the refresh token for any reason closes the session. The refresh token is not tied to the user's AS-1 login session; [sign-out and revocation](credentials-and-lifecycle.md#sign-out-and-revocation) describes what ends it.

**Per-call ID-JAG.** When no cached `V` exists for (session, instance, gateway, user), the broker asks AS-1 for an ID-JAG ([draft-ietf-oauth-identity-assertion-authz-grant-04](https://datatracker.ietf.org/doc/draft-ietf-oauth-identity-assertion-authz-grant/)) with RFC 8693: the broker refresh token as `subject_token` (`subject_token_type=urn:ietf:params:oauth:token-type:refresh_token`), the agent-instance JWT-SVID as `actor_token` (`actor_token_type=urn:ietf:params:oauth:token-type:jwt`), `requested_token_type=urn:ietf:params:oauth:token-type:id-jag`, and AS-2's issuer as `audience`. AS-1:

1. checks that `audience` is an allowed AS-2 for `mecatl` and `resource` a vMCP configured for it;
2. resolves the refresh token to its record and checks that it is a `mecatl_session` record bound to `mecatl`, that the session is open, and that the hard expiry has not passed, returning the record's revocation reason otherwise;
3. checks the user's upstream refresh token against its recorded expiry and, if the re-check interval has passed, refreshes it, as [upstream re-check](credentials-and-lifecycle.md#upstream-re-check) describes;
4. validates the SVID against its agent trust bundle, registered subject, and a configured audience accepted by both AS-1 and the broker invocation validator, and checks that the instance is registered to this session;
5. authorizes the combination of client, subject, actor, and target AS-2;
6. issues the ID-JAG.

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
resource=https://gateway.example.com/mcp
mecatl_operation=op-pr42-001
client_id=mecatl + client assertion
```

Refusals carry a reason code in `error_description`, listed under [error reasons](credentials-and-lifecycle.md#error-reasons). The ID-JAG has header `typ=oauth-id-jag+jwt`, AS-1 `iss`, the AS-1 `sub` from the refresh token record (never from caller input), AS-2 `aud`, `client_id=mecatl`, `jti`, `iat`, a short `exp`, and a single-level `act`. `act.sub` is an opaque, stable, per-instance identifier for the acting instance, such as `ag_7hq2x9`: a plain string with no colon, never a SPIFFE path or definition name, and `act.iss` is AS-1's issuer, so the identifier stays attributable to AS-1 when AS-2 copies `act` into `V`. The agent-instance issuer creates it at registration only if the SPIFFE ID has none yet, unique in both directions and never reassigned; AS-1 looks it up from the validated `actor_token`, and the full SVID stops at AS-1. The ID-JAG also carries the `resource` the broker requested. There is no nested `act`: the delegation that led to the call stays in Mecatl's harness and broker records. Nesting comes later with the [chained exchange](mcp-authorization.md#chained-exchange). `aud_sub` and `email` are deliberately omitted. AS-1 and AS-2 are separate issuers with separate keys, also in embedded mode; embedded mode is outside the cross-domain scope of §9.3, as [MCP gateway modes](mcp-gateway-modes.md#embedded-mode) explains.

**AS-2 redemption.** The broker redeems the ID-JAG at AS-2 with the RFC 7523 JWT-bearer grant, authenticating again as `mecatl`. AS-2 checks the trusted AS-1 issuer and signature, `typ`, that `aud` is its own issuer, lifetime, and that `client_id` matches the authenticated client (§4.4.1), rejects a reused `jti` (RFC 7523 §3), and checks that the requested `resource` is the ID-JAG's `resource` claim. Refusals carry `consent_required` or `invalid_assertion` in `error_description`. A lost redemption response is recovered with a new ID-JAG. It then maps the AS-1 subject to a native user:

| Mode | Mapping |
| --- | --- |
| Embedded | Users log in to AS-2 through AS-1, so the AS-1 `(iss, sub)` maps directly to the native user. Consent is implicit. |
| External | AS-2 redeems only for an AS-1 `(iss, sub, client_id)` with a consent link to a native user, created once through AS-2's own login. No just-in-time provisioning. |

AS-2 never resolves a user through `sub_id` (§9.5), email, or display name. Unknown subjects fail closed. A native-user match does not authorize use of a provider connection.

AS-2 issues `V`: `typ=at+jwt`, `sub` = native user, the ID-JAG's `act` unchanged, `client_id=mecatl`, `aud` = the vMCP resource, and `jti`. It issues no refresh token (§4.4.3); after expiry the broker obtains a new ID-JAG from the broker refresh token. If the refresh token has expired or been revoked, fail closed until a new session open supplies a fresh AS-1 access token.

**vMCP and the upstream credential.** vMCP validates `V` under RFC 9068 and, if the request uses a `resource` parameter, RFC 8707. It binds its session to the user and `act.sub`. It loads the upstream credential keyed by (gateway, native user, provider). A missing credential answers "needs authorization", which the broker returns as the existing `mcp_authorization_pending` outcome. Consent is checked at redemption, not here. There is one credential record per (user, provider), refreshed as a single row with compare-and-swap; the enterprise connector gateway already stores credentials this way.

The broker must propagate the operation ID, such as `op-pr42-001`, with each MCP request, and vMCP must log it with its decision as reported, unverified trace context. A cached `V` serves several calls, so its `(iss, jti)` cannot identify one operation; with a single-level `act`, the operation ID is also what leads from a gateway record to the delegation in Mecatl's records. The broker sends them in each MCP request's `_meta`, under the proposed keys `dev.mecatl/operation` and `dev.mecatl/attempt`, and the operation ID as the request parameter `mecatl_operation` at AS-1. The persistence ordering of these correlation records is open.

**Open.** How an external gateway reports "needs authorization" or "consent required" to the broker as a structured outcome. No callback or private mapping-response fields are specified. Candidates include an AS-2 login chain through AS-1, MCP URL elicitation, and the connector gateway's connect and status APIs.

## Credential verification

Operator configuration fixes accepted workload and agent issuers, audiences, identities, and trusted signing keys. Token claims do not select verification endpoints, CA trust, or server names. For each call, the broker authenticates the workload and validates the agent-instance JWT-SVID against its configured trust bundle, registered subject, broker audience, expiry, and permitted presenter. JWT-SVID does not require `iss` or `jti`.

Test client authentication at both servers:

- AS-1 and AS-2 reject requests without a valid `private_key_jwt` assertion for `mecatl`, including a wrong `aud`, an expired assertion, a reused `jti`, and a key not registered for `mecatl`.
- A leaked AS-1 access token or broker refresh token presented without the broker's key is rejected.

Test the session-open exchange:

- AS-1 rejects access tokens it did not issue, tokens for another audience, expired or revoked tokens, a second conversion of the same `jti`, and a `scope` parameter.
- AS-1 accepts `typ` `at+jwt` and `application/at+jwt`.
- AS-1 accepts a Mecatl-API-audience token only from client `mecatl`; another client presenting the same token is rejected.
- The refresh token record names the session and its owner; session close revokes it; presenting it at the refresh-token grant returns `invalid_grant`; a retried open with the same `jti`, client, and session revokes the first refresh token and returns a new one; a retry after the session closed, after its refresh token was revoked, past the source token's `exp`, or for another session is refused.
- A crash between writing the converted-`jti` record and the refresh-token record leaves neither.
- The response echoes the `mecatl_session` entry unchanged; an owner that differs from the token's `sub_id` is refused with `invalid_request`, and a session owned by another user or closed is refused with `invalid_authorization_details`.
- An echoed entry that differs from the request makes the broker revoke the refresh token and fail the open.
- A second open of a session that has a live refresh token, with a different `jti`, is refused. Two concurrent opens with one `jti` for two sessions yield one session.
- A broker crash after AS-1 issued the refresh token, before the broker stored it, is recovered by a retry that revokes the orphan.
- A retry after a lost response, with a new client assertion, succeeds; mecak8s's client refreshes and retries at most once.
- Losing the refresh token closes the session, and a new open of the same session ID is refused.

Test ID-JAG issuance:

- AS-1 rejects an expired, revoked, or foreign-client refresh token, and a refresh token whose session is closed or past its hard expiry.
- AS-1 rejects an untrusted or expired SVID, an SVID with an audience not accepted by both AS-1 and the broker, and an instance not registered to the refresh token's session.
- The issued ID-JAG has `typ=oauth-id-jag+jwt`, the `sub` from the refresh token record, `client_id=mecatl`, AS-2 `aud`, a fresh `jti`, and an `act` with exactly one level, whose `sub` is the opaque `ag_` identifier of the validated actor and which has no nested `act` and has `act.iss` set to AS-1's issuer. A caller cannot change `sub` or `act` through request parameters. The ID-JAG carries the requested `resource`.
- Each refusal carries the reason code in `error_description` that [error reasons](credentials-and-lifecycle.md#error-reasons) assigns, and the broker maps it to the listed gRPC status and mecak8s outcome, including `mcp_owner_action_required`.
- An exchange after the upstream token's recorded expiry is refused without calling the IdP.
- A client assertion or ID-JAG replayed inside the clock-skew window after its `exp` is rejected; `jti` stores are shared across replicas.

Test AS-2 redemption:

- AS-2 rejects a wrong `typ`, untrusted issuer, wrong `aud`, expired ID-JAG, reused `jti`, and `client_id` that does not match the authenticated client.
- In external mode, AS-2 rejects a subject with no consent link and a revoked link. A link for another AS-1 issuer or client does not match.
- AS-2 never resolves through `sub_id` or email; a matching `sub_id` without a mapping fails closed.
- `V` carries the native user, the ID-JAG's single-level `act` unchanged, `client_id=mecatl`, and the vMCP audience, with no refresh token.
- After a lost redemption response, the broker mints a new ID-JAG instead of resending the first.
- A requested `resource` other than the ID-JAG's `resource` claim is refused with `invalid_target`.

Test the `V` cache:

- The broker treats `V` as expired a configured skew margin before `exp`.
- On a 401 `invalid_token` from the vMCP, the broker drops `V` and repeats the exchange once within the same call; a second 401 fails the call. A 401 for an older `V` does not drop a newer one another call cached.
- N concurrent calls that miss the cache for one key cause one exchange.
- Startup fails when the skew margin is not shorter than the `V` lifetime.
- The `ag_` identifier in `act` is unchanged after the instance's SVID is renewed.

Test schedule grants:

- Setting up a schedule converts a fresh access token with a `mecatl_schedule` entry into a schedule grant.
- When the schedule fires, the broker exchanges the schedule grant for a session refresh token whose expiry is no later than the horizon; the run's calls then pass steps (6) to (9). Revoking the schedule grant revokes that session's refresh token.
- A schedule grant is refused as the subject token of an ID-JAG exchange, and a session's refresh token is refused for opening a scheduled run.
- A scheduled run whose owner differs from the owner recorded with the schedule grant is refused.
- A scheduled-run open retried with the same schedule grant, client, and session succeeds; a schedule grant past its horizon or revoked is refused with its reason.
- A run's session closes, revoking its refresh token, when the run ends.
- A schedule grant presented at the refresh-token grant returns `invalid_grant`.

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

This table is the ledger of ToolHive changes the design needs. None is filed as a ToolHive issue yet; when one is, link it here. AS-1 and AS-2 are ToolHive authorization server instances, and vMCP is ToolHive's. The capabilities were checked against ToolHive main `ce74cfa37`, and the revocation rows against `9adad0abb`, by reading, not running, the code. Sizes are estimates.

| Capability | Today | Size | When |
| --- | --- | --- | --- |
| Exchange of a self-issued access token presented by another confidential client (the Mecatl-API-audience exception) | Mostly; the requested `aud` must be a subset of the subject token's `aud` | Small to medium | Identity work |
| Refresh token in a token-exchange response, with the session-bound record | No; exchange issues access tokens only | Medium to large | Identity work |
| Issue ID-JAGs (`typ=oauth-id-jag+jwt`, audience = another AS) | No; redemption and an outbound XAA client only. The ID-JAG redemption handler binds the client, consumes `jti`, and copies `act`; the plain JWT-bearer handler drops `act` and replaces the client | Large | Identity work |
| Refresh token as `subject_token` | No | Large | Identity work |
| Single-level `act` from the registered actor input, with `act.iss` set to AS-1's issuer (nesting later, with the chained exchange) | Partly; `buildActClaim` builds `act` from a client or self-issued actor token and nests it | Medium | Identity work |
| ID-JAG redemption keeps `act`, including `act.iss` | Yes, copied unchanged | None; verify only | Identity work |
| AS-2 redeems an ID-JAG from a trusted AS-1 for any subject, resolved through the mapping or consent link, with allowed resources set per issuer. ToolHive forbids wildcards on purpose, so this should be a separate, explicit mode, such as subjects resolved by an identity resolver, not a looser check | No; each issuer's policy requires an exact `subject_bindings` list, each subject with its own allowed resources, and configuration validation rejects an empty list and wildcard subjects or resources (checked at main `9adad0abb`) | Small | Identity work |
| AS-2 maps the AS-1 subject to a native user after ID-JAG redemption, and stops marking the token as having no upstream session | No; emits `iss#sub` and marks no upstream session | Large | Identity work |
| Per-user upstream credential lookup in open-source ToolHive, keyed (gateway, native user, provider) | No; keyed by `tsid`, a per-interactive-login session ID that a delegated `V` lacks, and vMCP loads nothing for a token without one (`pkg/auth/token.go:1253`); the enterprise per-user decorator is build-tagged | Medium | Identity work |
| Consent link at AS-2's own login (external mode only) | No | Not sized | Identity work |
| `private_key_jwt` for client `mecatl` at AS-1 and AS-2 | Yes | Configuration | Identity work |
| SPIFFE client authentication at the authorization server | Yes | None; verify only | Identity work |
| AS-1 and AS-2 audit records following the [AuditEvent profile](audit.md#auditevent-profile), including sign-in, access-token issuance, and refresh-token revocation at AS-1 | No; the authorization server records no audit events. The toolhive-core audit logger and its sink already exist | Not sized | Identity work |
| Revoke a broker refresh token and its record, by an RFC 7009 endpoint or, while the broker runs inside mecak8s, in process | No; storage revokes by refresh-token family, but no endpoint is wired | Small | Identity work |
| Revoke all of a user's grants, including schedule grants, and her client refresh tokens, recording a revocation time and reason | No; no index from user to grants | Medium | Identity work |
| Upstream re-check when issuing an ID-JAG, with the upstream token kept independent of client sign-out, its expiry recorded, and refreshes single-flight per user | No; the refresh grant never contacts the upstream IdP (checked at main `9adad0abb`) | Small to medium | Identity work |
| AS-1 and the vMCP record the operation and attempt IDs the broker passes with each exchange and MCP request, marked as reported | No | Small | Identity work |
| Session-open owner check: compare the session owner the broker passes with the token's `sub_id`, and keep the owner on the refresh-token record | No | Small | Identity work |
| Parse `mecatl_session` and `mecatl_schedule` `authorization_details` at token exchange and echo the granted entry unchanged | No | Small | Identity work |
| Accept a schedule grant as the subject token for opening a scheduled run's session, capping the new refresh token at the schedule's horizon | No | Small | Identity work |
| A reason code in `error_description` for each token-exchange refusal | No | Small | Identity work |
| Carry the requested `resource` into the ID-JAG and check it at redemption | Unknown; verify | Small | Identity work |
| One upstream refresh token per user, with its expiry and an in-flight marker, refreshed under a lease | No | Small to medium | Identity work |
| Accept a client assertion whose `aud` is the issuer identifier alone, as draft-ietf-oauth-rfc7523bis requires | Unknown; verify | None if accepted | Identity work |
| Request a refresh token from the upstream IdP at sign-in (`offline_access`, or the IdP's equivalent) | Unknown; verify per IdP | Configuration | Identity work |
| Reject a token whose upstream session records were deleted | No; the lookup returns an empty credential map instead of failing | Small | Identity work |
| Refresh token bound to the AS-1 login session (`sid`), so that signing out at AS-1 revokes it | No; `tsid` is per authorization request, not per login | Large | Later |
| Receive logout and account events from the company IdP (OIDC Back-Channel Logout, Okta event hooks, Shared Signals) | No | Large | Later |
| RFC 9396 `authorization_details` on the refresh grant, embedded in the access token, and a session-bound check at conversion | Unknown | Not sized | Later |
| End vMCP sessions when the user's grants are revoked | No; sessions bind issuer and subject with no revocation check | Not sized | Later |
| DPoP issuance and validation for `V` | No | Large | Later |
| Scope or `authorization_details` (RFC 9396) narrowing enforced per tool at the gateway | Scope intersection only | Large | Later |
| Cedar at the gateway reading `act` | No | Part of the narrowing work | Later |

The consent link needs the link record, the AS-2 authorization request carrying an AS-1-signed subject hint, the consent screen, revocation, and link-swap defenses. Its creation flow is under [external consent link](#external-consent-link); its requirement is in [MCP gateway modes](mcp-gateway-modes.md#3-jwt-bearer-grant-at-as-2). Whether "Allow Mecatl" and the provider's consent screen can be one redirect chain is open.

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

- Mecatl: `AgentDefSource` stays storage-independent: provenance profiles travel as metadata, not as additions to the core interface. Each profile defines required fields, schema, hashing rules, and revision semantics. `AgentDefSource` resolution returns source evidence; the definition loader produces the [provenance record](agent-definitions.md#definition-provenance) with both digests, rejects profiles the operator has not allowed, and registration carries the record. Prove a locally modified file yields a source digest that does not match its claimed commit's content.
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

## Audit profile checks

Verify the [AuditEvent profile](audit.md#auditevent-profile) through the real toolhive-core logger, reading the configured destination:

- Each field means the same in the JSON record and in the slog output, including `Metadata.Extra`, `Data`, and the occurrence time; the two outputs name some fields differently.
- A caller-supplied `AuditID` and `LoggedAt` are unchanged when the event is sent again.
- Records with a missing identity, a reported-only identity, and an unauthenticated denial carry the right provenance marks, and an unverified identity never appears as `user_id`.
- An unknown outcome, a retry, and a late result produce distinct attempts and events, and the late result references the earlier event.
- Each event type rejects outcomes outside its fixed set.
- `Data` is valid JSON matching its structure version, and `Subjects`, `Target`, and `Data` contain only keys allowlisted for the type.
- No token, assertion, refresh token, authorization header, or token-endpoint request body appears in any record, including errors.
- `DelegationChain` output matches what toolhive-core produces for the same `act` chain.
- A three-level delegation chain, main to reviewer to specialist, can be reconstructed from lineage events after a broker restart and after session cleanup.
- Two instances of the same definition under different parents stay distinct in reconstruction.
- A missing or malformed lineage record makes reconstruction report an incomplete chain, not a shorter one.
- A delivery failure is reported through the logger, as toolhive-core [#343](https://github.com/stacklok/toolhive-core/issues/343) proposes.
- With a lossless sink configured, an unavailable sink blocks dispatch, and no record is dropped under back-pressure or when the sink restarts.
- The broker refuses to dispatch when its decision record cannot be delivered, and dispatches only after the sink accepts it.
- Removing a record or ending a stream without its stop event shows as a sequence gap.
- Each reconciliation check flags an injected mismatch: an AS-2 redemption without AS-1 issuance, a vMCP call without broker dispatch, a dispatch without a vMCP call, and a session-open conversion without AS-1 issuance.
- The broker's lineage check rejects a delegation whose instances are in different session trees or whose parent delegation's receiving run differs.

## Root eligibility checks

- The broker refuses to register a root instance whose definition is not root-eligible for that user, and records the refusal.
- A child instance's definition need not be root-eligible.

## Session binding checks

- AS-1 refuses a session-open conversion when the token's `sub_id` differs from the owner mecak8s sends, and records the refusal.
- After `A42` is opened for Alice, AS-1 refuses to open `A42` again with Bob's token, including after a broker restart.
- A session ID is never reused for a new session.

**Later work for session-bound tokens.** Before adopting [session-bound tokens](credentials-and-lifecycle.md#3-session-open-and-the-broker-refresh-token), check how each client would hold them. The TUI keeps tokens in the OS keyring and Studio in sealed server-side cookies; both would need one session-bound token per open session, refreshed silently. Studio's server holds those tokens for the browser, so check how it stores and selects them per session and whether that changes its cookie and CSRF handling.

## Run owner checks

- The broker refuses a call whose reported run owner differs from the owner recorded with the grant in use, the company identity, and records the denial.
- A child run's owner equals its parent run's owner; the broker refuses a delegation that changes it.
- `ReportDelegation` is refused for an unknown delegating run, an instance not registered to the session, or a delegating run that is not its parent delegation's receiving run. A repeat with the same content succeeds; a repeat with different content returns `ALREADY_EXISTS`.
- A call naming a delegation that was not reported is denied.
- Registration into a closed session, or a session bound to another workload, is refused.
- A call whose SVID instance differs from the run record's instance is denied; a new run naming no delegation from a child instance is denied; two replicas recording the same new run keep one record.
- `ReportDelegation` for a delegating root run with no record yet records it and succeeds.
- Re-registering an instance into a new session for the same host session keeps its incarnation and `ag_` identifier; re-registering it under a different owner is refused; a repeated registration with the same content returns the same reference, with different content `ALREADY_EXISTS`.

Test the checks at each hop:

- mecak8s rejects each failing step (1) check, returning 401 `invalid_token` for checks 1–7 and 403 `not_session_owner` for check 8.
- The broker rejects a legacy ServiceAccount token, a token with the Kubernetes API server's audience, a token whose `exp - iat` exceeds the maximum, and a ServiceAccount not on the allowlist.
- SVID validation rejects a platform SVID with the broker's audience (wrong trust domain and bundle), a header without `kid`, an `aud` with two values, a disallowed `alg`, and an `exp` beyond the maximum lifetime.
- Registration rejects names over 63 bytes, nesting past 8 child levels, `.` and `..`, and a parentless registration of a definition that is not root-eligible.
- An AS-1 503 maps to `UNAVAILABLE` and is reported as a retryable failure, not a denial; `invalid_client` and a bare `invalid_request` map to `INTERNAL`; each reason in [error reasons](credentials-and-lifecycle.md#error-reasons) maps to its gRPC status and mecak8s outcome.
- Two users' calls through the same instance never share a cached `V`, ID-JAG, or vMCP session ID.
- Signing out everywhere revokes only that user's grants and cache entries.

## Sign-out and revocation checks

Verify the behavior in [sign-out and revocation](credentials-and-lifecycle.md#sign-out-and-revocation):

- Signing out of one client revokes only that client's tokens; running agents and schedules continue.
- Signing out everywhere cancels live runs, parked tool calls, and in-flight tool calls; closes the broker sessions, keeping closed-session records; revokes their refresh tokens and every client's AS-1 refresh tokens at AS-1; drops cached ID-JAGs and `V`; and revokes every schedule grant. Transcripts remain.
- After it, session open and schedule setup refuse an access token issued before the revocation time, including one opened concurrently with the revocation.
- It reaches sessions served by another replica.
- After it, an ID-JAG exchange with any revoked refresh token fails, and each schedule's next run ends with "needs owner action".
- When the upstream IdP returns `invalid_grant` before the upstream token's recorded expiry, or when no expiry is recorded, AS-1 runs the sign-out-everywhere path with reason `upstream_revoked`, revoking all the user's grants and client refresh tokens, and the exchange fails.
- When it returns `invalid_grant` at or after the recorded expiry, or the recorded expiry passes, or the IdP issued no refresh token and the re-check is not turned off for it, AS-1 runs the same path with reason `owner_action_required`.
- A later exchange for a revoked session returns the stored reason, not `session_closed`; a grant is never revived by a new sign-in.
- A network error or 5xx fails that exchange and revokes nothing.
- Concurrent exchanges for one user, across replicas, cause one upstream refresh. When the refresh lease times out, waiting exchanges fail with 503 and revoke nothing.
- After a lost IdP response or a crash during a refresh, a rotating IdP's `invalid_grant` on the next refresh revokes with reason `owner_action_required`, not `upstream_revoked`.
- An upstream revocation runs the broker's sign-out-everywhere path: live runs stop, parked and in-flight calls are cancelled, and cached `V` is dropped.
- A second sign-in replaces the user's upstream refresh token. A client's own sign-out does not remove it. Re-checks happen at most once per interval.

## Work breakdown

The [overview](README.md#order-of-work) gives the order of work and its reasons. The full breakdown:

The **identity work** establishes who acts and records it:

- the broker modules: definition loader, agent-instance issuer, AS-1, and broker core;
- root eligibility: the broker refuses to register a root instance whose definition the operator has not made root-eligible;
- definitions loaded with their provenance;
- instance registration, with the incarnation, definition reference, and creation parent;
- agent-instance credentials;
- the credential flow: sign-in at AS-1, the session-bound refresh token, ID-JAG issuance, AS-2 redemption, and per-user provider credentials at the gateway;
- the broker's identity checks: session, workload, and that the acting instance is registered to the broker session;
- the run owner on every call, checked against the grant in use, so that shared sessions can come later without a call using the wrong user's credentials;
- sign-out and revocation: signing out of one client leaves background agents running, so signing out everywhere stops every agent and schedule acting for the user, and AS-1's upstream re-check does the same when the company IdP disables the account;
- audit of who acted, for whom, through which delegation, and what was decided, dispatched, and observed.

The **authority work** decides what each agent may do:

- the definition loader resolving each loaded definition's ceiling into permitted MCP server/tool pairs; a definition the broker did not load gets no ceiling of its own and inherits the ceiling of the instance that creates it;
- each instance's effective ceiling, its definition ceiling narrowed by its parent's;
- Cedar policy generation and a ceiling check on every call;
- grants scoped to a run the user started, replacing the session's standing refresh token, so that sessions of absent users carry no authority;
- task-specific delegation bounds, and user, resource, and argument policy.


## Modulith constraints

While the broker runs as modules inside mecak8s, these constraints keep the later move to a separate service a deployment change rather than a redesign:

- **Fixed issuer URLs.** AS-1 uses its final issuer URL, such as `https://broker.example.com/as`, from the start, and the broker audience is fixed the same way. External consent links are keyed on the AS-1 issuer, so changing it later would break every link.
- **The same contract in-process.** mecak8s calls the broker over the same RPC contract it will use across processes, with real workload authentication, and the broker identifies the caller from authenticated metadata. No in-process shortcut may skip a check.
- **Separate storage.** The broker's records, including refresh tokens, the instance registry, and audit, use their own schema and database credentials, with no joins or shared transactions with mecak8s's session store.
- **One key per role.** AS-1, the broker client `mecatl`, and the agent-instance issuer each use their own key behind a signer interface, so moving a key means moving its permission, not changing code.
- **Real workload identity.** Sessions bind to the real mecak8s workload identity from the start, so the move needs no rebinding of open sessions.
- **No executor access.** Executor Pods have no route to the broker's listener, database, or KMS.

## Parking and recovery: implementation status

Protected MCP calls can park while awaiting provider reauthorization and resume with account, connection, catalogue, and permission checks. Offline fixtures cover credential and enrolled-connection recovery after host and broker replacement.

Parked broker flows remain process-local. Resuming the same pending operation after broker replacement and renewing agent-instance JWT-SVIDs across runs remain unfinished integration work. The existing recovery path does not establish authenticated human ownership. After broker replacement, recovery must restore the broker session and its workload and host binding and find the broker refresh token's AS-1 record still valid; if the refresh token has expired or been revoked, tool calls fail closed until a new session open. Credential recovery does not recover external effects; unknown outcomes remain fenced against automatic replay.
