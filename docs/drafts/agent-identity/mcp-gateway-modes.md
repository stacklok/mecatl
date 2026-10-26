# MCP gateway modes

*This chapter describes the intended architecture, not current implementation status.*

Mecatl can reach protected MCP tools through a vMCP gateway in two ways. In **embedded mode**, the vMCP and its authorization server run inside the Mecatl deployment. In **external mode**, Mecatl points at an external gateway. Both modes use the same protocol, roles, and token formats; they differ in how AS-2 resolves and authorizes the user. Co-location does not create an isolation boundary.

This chapter covers the protocol between AS-1 and AS-2 and how the two modes differ. The full walkthrough, from Alice's sign-in through session open to the vMCP call, is in [credentials and lifecycle](credentials-and-lifecycle.md#interactive-execution).

## Who holds what

| | Embedded mode | External mode |
| --- | --- | --- |
| Broker authorization and audit | Mecatl | Mecatl |
| Agent-instance identities | Mecatl (separate agent issuer) | Mecatl (separate agent issuer) |
| Login and ID-JAG issuer (AS-1) | Mecatl, inside the broker | Mecatl, inside the broker |
| Gateway authorization server (AS-2) | Mecatl, separate issuer and key | Gateway operator |
| Provider credentials | Mecatl's gateway store, keyed by (gateway, native user, provider) | Gateway operator, keyed the same way |
| Trust in AS-1 | Local configuration; AS-2 trusts AS-1 as an ID-JAG issuer, and both sign users in through the same company IdP | Gateway administrator registers AS-1 as an assertion issuer only |
| User approval | Implicit | One-time approval at AS-2's own login |

The design works in two deployments: standalone Mecatl, with no Stacklok Enterprise Platform, Directory, or connector gateway, and enterprise deployments that integrate with them. Three roles are therefore pluggable: the **identity resolver**, which maps an AS-1 subject to a native user; the **consent store**; and the **credential store**. Standalone Mecatl provides local implementations, and enterprise deployments back them with Directory and the connector gateway. Nothing in the flow presumes Directory.

## Two authorization servers

Mecatl uses two authorization servers so that it can work with an MCP gateway it does not operate, such as the connector gateway in the Stacklok Enterprise Platform. That gateway holds users' provider connections, such as their GitHub or Google tokens, under its own administration, and its vMCP accepts only access tokens from the gateway's own authorization server; Mecatl can neither mint those tokens nor hold the credentials behind them. So AS-1, Mecatl's server, issues a grant naming the user and the acting agent, and AS-2, the gateway's server, verifies the grant, decides whether to honor it, and issues its own access token. When Mecatl runs the gateway itself (embedded mode), it uses the same two servers and the same protocol, so there is one code path; there the split is a protocol boundary rather than a security boundary, although the two servers still keep separate keys.

**AS-1** is Mecatl's login authorization server, running inside the broker. Alice signs in through AS-1, which redirects her to the company IdP as an ordinary OIDC client. Her client receives an AS-1 access token for the Mecatl API. At `OpenSession`, mecak8s passes that token to the broker once. The broker exchanges it at AS-1 with RFC 8693 for a session-bound broker refresh token, `RT-A42`. The [session-open step](credentials-and-lifecycle.md#3-session-open-and-the-broker-refresh-token) describes that exchange and the AS-1 record behind `RT-A42`.

**AS-2** is the gateway's authorization server. The vMCP accepts only access tokens AS-2 issued. On each MCP request, the vMCP validates the token and loads the user's provider credential.

Between them, per call and on a cache miss, the broker runs two steps, detailed in [credentials and lifecycle](credentials-and-lifecycle.md#7-id-jag-from-as-1):

1. It exchanges `RT-A42` at AS-1 (RFC 8693), with the agent-instance JWT-SVID as `actor_token`, and receives an **ID-JAG** ([draft-ietf-oauth-identity-assertion-authz-grant-04](https://datatracker.ietf.org/doc/draft-ietf-oauth-identity-assertion-authz-grant/)) addressed to AS-2.
2. It redeems the ID-JAG at AS-2 with the RFC 7523 JWT-bearer grant and receives the AS-2 access token `V`.

The broker authenticates at both servers as one OAuth client, `mecatl`, with `private_key_jwt`. ID-JAG §4.4.1 requires the ID-JAG's `client_id` to match the client that redeems it, so one client ID is used at both. `mecatl` is the broker's client registration, not an identity of the acting agent. The [broker client authentication step](credentials-and-lifecycle.md#5-broker-client-authentication) shows the client assertion.

AS-2 receives neither Alice's AS-1 access token, `RT-A42`, nor the SVID. It receives only the ID-JAG.

AS-1 and AS-2 have separate issuer identities, keys, and storage, also in embedded mode. ID-JAG §9.3 says the profile is intended for parties in different trust domains, in the OAuth sense, which this design calls [administrative domains](credentials-and-lifecycle.md#7-id-jag-from-as-1); and that an IdP must not issue access tokens in response to an ID-JAG it issued itself in the same domain, because that could broaden authorization. External mode fits that scope. Embedded mode does not: AS-1, AS-2, and the credential store share one administrative domain, and separate keys do not create a boundary. See [embedded mode](#embedded-mode). The agent-instance issuer is a third identity role. RFC 8693 standardizes token-exchange input and output in [§§2.1 and 2.2.1](https://www.rfc-editor.org/rfc/rfc8693.html#section-2), token-type identifiers in [§3](https://www.rfc-editor.org/rfc/rfc8693.html#section-3), and `act` in [§4.1](https://www.rfc-editor.org/rfc/rfc8693.html#section-4.1). ID-JAG is a profile of [identity chaining across domains, revision 17](https://www.ietf.org/archive/id/draft-ietf-oauth-identity-chaining-17.html): it reuses that draft's exchange, RFC 7523 redemption, and no-refresh-token rule, and adds an explicit `typ`, required `client_id`, `jti`, `iat`, and `aud`, client-binding redemption checks, and security rules. Both are Internet-Drafts, not RFCs.

## How AS-2 resolves the user

The per-call flow, with every request, token, and check, is in [credentials and lifecycle](credentials-and-lifecycle.md#interactive-execution), steps (7) to (9). AS-1, the ID-JAG, the redemption, and the vMCP's checks are the same in both modes. They differ at one point: how AS-2, after [validating the ID-JAG](credentials-and-lifecycle.md#8-as-2-access-token), finds the native user.

| Mode | How AS-2 resolves and authorizes the user |
| --- | --- |
| Embedded | ID-JAG as specified. AS-1 and AS-2 both sign users in through the same company IdP, so AS-2 trusts that IdP for sign-in and resolves the subject through it (§4.1): it finds the local user from the ID-JAG's `upstream_subject`. `u-alice-91` resolves to native user `usr-alice-7c1d`. Approval is implicit, set by deployment configuration: AS-1, AS-2, and the credential store share one administrative domain. AS-1 and AS-2 are distinct issuers with distinct keys, so no issuer redeems its own ID-JAG. |
| External | Identity chaining across administrative domains ([chaining §§2.1, 2.5](https://www.ietf.org/archive/id/draft-ietf-oauth-identity-chaining-17.html#section-2.1)) using the ID-JAG token profile. AS-2's users log in through the gateway's own IdP, so the §4.1 precondition, that the resource AS already trusts the IdP for SSO, does not hold, and its consent waiver does not apply. AS-2 trusts AS-1 only as an assertion issuer. It redeems only when the local user found from the ID-JAG's `upstream_subject` holds an unrevoked approval for its `client_id` and the actor's issuer (chaining §2.2, deployment-specific resolution). No just-in-time provisioning. |

Two separate facts decide redemption. The first is which native user the AS-1 subject is (mapping). The second is whether client `mecatl` may use that user's connections at this gateway (approval). A valid mapping is not an approval. In embedded mode, both follow from configuration: AS-2 trusts AS-1 as its SSO IdP, and approval is implicit. In external mode, AS-2 looks up both in one approval record:

```text
(gateway, local user, client, actor_iss, revoked)
```

There is one row per (gateway, local user, client, `actor_iss`). On each redemption, AS-2 finds the local user from the ID-JAG's `upstream_subject`. It uses the shared login or, under administrator pre-approval, the directory-synced mapping. AS-2 does not store AS-1's subject. Mecatl's approvals name AS-1 as `actor_iss`, so one approval covers any Mecatl agent. Two ways create an approval:

- **Alice approves (the default).** Alice opens an approval link, signs in at AS-2, and approves "Allow Mecatl (any Mecatl agent) to use your connections". AS-2 activates the approval only if her company IdP identity matches the ID-JAG's. So a compromised AS-1 can act only for users who approved. See the [implementation companion](implementation.md#external-consent-link).
- **An administrator pre-approves (opt-in).** The gateway administrator explicitly approves client `mecatl` for a group. The mapping from AS-1 subjects to native users is created on first use from AS-1's `upstream_subject`, or imported by directory sync. This rests on AS-1's word, so a compromised AS-1 reaches every covered user, whether or not the user used Mecatl. This is embedded-mode trust ([implementation companion](implementation.md#external-consent-link)).

The approval flow and the link-swap attack it must resist are in the [implementation companion](implementation.md#external-consent-link). The gateway UI, or Mecatl, can revoke an approval; a client may revoke only approvals that name it.

An unresolved or unapproved subject fails closed, with reason `consent_required` in external mode. AS-2 must not create a user as a side effect of lookup. Resolving the user does not establish that a provider connection exists; the vMCP's credential lookup remains a separate check.

## Gateway administrator configuration

In external mode, the gateway administrator configures AS-2 with:

- AS-1's issuer and signing keys, trusted as an ID-JAG issuer only, not as an SSO IdP, for any subject AS-1 names; the mapping or consent link, not a list of subjects, decides who is redeemed;
- the `mecatl` OAuth client, with the broker's `jwks_uri` registered once for `private_key_jwt`;
- the consent-link flow and its revocation in the gateway UI;
- permitted actors and allowed vMCP resources;
- ID-JAG lifetime, and single use for AS-1's issuer (recommended; Mecatl never re-submits an ID-JAG).

Mecatl's operator configures AS-1 with the `mecatl` client, the accepted agent trust domain and audience, and the allowed AS-2 targets. Embedded mode uses the same roles and checks with local configuration, and AS-2 trusts AS-1 as its SSO IdP. Co-location does not create isolation.

## Sessions at the vMCP

Each agent instance has its own vMCP session per user, bound to `V`'s `iss`, `sub`, and `act.sub`; see [sessions at the vMCP](credentials-and-lifecycle.md#sessions-at-the-vmcp).

## Embedded mode

Embedded mode runs AS-1, AS-2, and the vMCP in one deployment. It uses the same ID-JAG exchange at AS-1 and RFC 7523 redemption at AS-2, with the same token formats, validation, and client authentication as external mode. Only approval differs: it is implicit. AS-2 resolves the user as in external mode, from the ID-JAG's `upstream_subject` through the shared company IdP. AS-1 is not AS-2's sign-in provider: ToolHive's authorization server issues no ID tokens and has no userinfo endpoint, so it cannot sign users in to another server. Implementations can co-locate protocol handlers or reuse them as libraries; the design does not require a separate network service or a private issuance interface. The two authorization servers keep separate issuer URLs, signing keys, and storage namespaces.

This keeps one protocol path for both modes. It does not add isolation: anyone who compromises the process holds both keys.

Embedded mode is outside the cross-domain scope that ID-JAG §9.3 states, because all three parties share one administrative domain. It reuses the ID-JAG format so both modes share one protocol, not to claim a trust boundary. The section's requirement still holds: AS-1 never issues an access token from its own ID-JAG, because AS-2 issues `V`. The scope broadening §9.3 guards against is bounded because AS-2 issues `V` only for the vMCP resource and issues no refresh token. Distinct issuers, audiences, and `typ` values keep the ID-JAG from being used as an AS-2 access token.

## Outside this chapter

- **Revocation.** Sign-out and account revocation are covered in [credentials and lifecycle](credentials-and-lifecycle.md#sign-out-and-revocation). Issued ID-JAGs and access tokens are not revoked; they expire.
- **Structured gateway outcomes.** How an external gateway reports "needs authorization" or "consent required" to the broker as a structured outcome remains open. So does whether the "Allow Mecatl" step and the provider's consent screen can be one redirect chain.
- **Gateway authorization on `act`**, such as Cedar policies that consider the acting agent. Until the agent ceiling is carried in the ID-JAG and `V` as scope or `authorization_details` (RFC 9396), the broker alone enforces it; the gateway enforces user and connection.

## Integration requirements and evidence limits

AS-1 must accept `RT-A42` as `subject_token` and the registered agent-instance JWT-SVID as `actor_token` from client `mecatl` authenticated with `private_key_jwt`. It resolves the subject only from the refresh token's record, checks session state and hard expiry, re-checks the user's upstream refresh token when due, checks the actor's registration to the broker session, authorizes client, subject, actor, and AS-2 target together, and returns an ID-JAG whose single-level `act` holds the actor's opaque `ag_` identifier.

AS-2 must authenticate `mecatl`, validate the ID-JAG under ID-JAG §4.4.1, apply the issuer's ID-JAG reuse policy, and resolve the native user: directly in embedded mode, and only for an approved user in external mode. It must issue an access token for the native user that preserves `act` and `client_id`, and no refresh token. Unresolved or unapproved subjects fail closed.

The vMCP must validate `V`, bind its session to `iss`, `sub`, `client_id`, `act.iss`, `act.sub`, and the delegated-access marker, and load provider credentials only for a marked `V`, keyed by (gateway, native user, routed provider). Do not add a callback or response fields that expose AS-2's private native mappings to the broker. The design does not claim a backend-to-SaaS audit trail.

Embedded and external deployments implement the same protocol roles and token formats. Embedded wiring uses local configuration, separate issuer keys and storage namespaces, and implicit consent; external wiring uses gateway-administrator trust and the consent link. Co-location is not isolation.
