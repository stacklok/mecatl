> **Fixture note (post-A2).** This report records the A2 qualification, which ran
> against a **Dex** fixture and proved the SELF-ISSUED exchange path -- the subject
> token was minted by the same embedded AS that performed the exchange. The fixture
> has since moved to **Keycloak** to exercise the EXTERNAL-issuer path, which Dex
> structurally could not: no `scope`/`scp` on its JWT, `at_hash` on its id_token.
> The contract findings below still hold; the topology described is no longer the
> fixture's. See the [fixture README](../../deploy/mecak8s-vmcp/README.md) for the current flow.
>
> One correction: this report states that the embedded AS derives `scopesSupported`
> from the upstream IdP's discovery document. It does not -- `deriveScopesSupported`
> returns `incomingAuth.oidcConfigRef.scopes` verbatim. The "scope vocabulary is
> OIDC-only" residual was therefore wrong about its cause; an application scope was
> always declarable, and nothing about Dex prevented it.

# A2 contract report — ToolHive RFC 8693 delegation

**Task:** qualify, from the pinned ToolHive runtime and CRDs, whether a confidential
mecak8s client can exchange an authenticated caller token (T1) for a short-lived vMCP
token (T2) to call the Yardstick-backed protected vMCP.

**Classification: supported but needs a bounded fixture change.**

The full chain was demonstrated end to end against the live fixture. It required two
bounded fixture changes, both filed upstream. On the pinned stack *unmodified*, the
configuration is not admissible.

```text
Dex login -> embedded AS mints T1
          -> RFC 8693 exchange as mecak8s-delegate -> T2
          -> vMCP -> Yardstick -> {"output":"a2delegated"}
```

Pinned versions: operator image `ghcr.io/stacklok/toolhive/operator:v0.44.0`; charts
`toolhive-operator-0.44.0` and `toolhive-operator-crds-0.44.0`; `ghcr.io/dexidp/dex:v2.44.0`;
`ghcr.io/stackloklabs/yardstick/yardstick-server:1.1.1`.

Source citations are relative to the pinned ToolHive tree unless marked otherwise.

---

## Correction to the prior analysis

`task01-baseline-evidence.md` and the first revision of
`a2-scope-attenuation-research.md` concluded that scope attenuation was impossible
because Dex emits no `scope`/`scp` claim. The Dex source reading was correct — Dex's
access token *is* its ID token (`server/oauth2.go:304`) and its claim struct
(`:277-298`) has no scope claim — but it described the wrong token.

Dex is an **upstream provider** to vMCP's embedded auth server, not the issuer of the
token ToolHive attenuates. The Dex code is consumed at the AS callback
(`identityFromToken`), `disableUpstreamTokenInjection: true` stops it propagating, and
the caller's bearer token is minted by the embedded fosite AS. That token carries `scp`,
which ToolHive reads via a fallback written for exactly this case
(`pkg/authserver/server/tokenexchange/validator.go:344-357`).

The real blocker was never scopes. It was client class: the calling client was public.

---

## Q1 — Is confidential `delegateClients` / RFC 8693 exchange supported?

**Yes, and demonstrated.**

`VirtualMCPServer.spec.authServerConfig.delegateClients`, type `DelegateClientConfig`
(`cmd/thv-operator/api/v1beta1/mcpexternalauthconfig_types.go:360-389`, on the shared
`EmbeddedAuthServerConfig`). The operator always supplies the token-exchange grant when
converting to the runtime contract (`pkg/authserver/server_impl.go:235-240`); the caller
cannot select grants.

Confirmed live: the AS advertises `urn:ietf:params:oauth:grant-type:token-exchange` in
discovery, registration emits its warn (`server_impl.go:247`), and
`token_endpoint_auth_methods_supported` gains `client_secret_basic` and
`client_secret_post` alongside `none` once a delegate client exists.

Public clients are refused before any token processing:
`invalid_grant: The OAuth 2.0 Client is marked as public and is thus not allowed to use
authorization grant 'token-exchange'`.

---

## Q2 — What HTTPS endpoint/resource topology is required?

Two independent gates sit between a plaintext issuer and `delegateClients`:

1. **Admission (CEL)** — `mcpexternalauthconfig_types.go:495` rejects *every* `http://`
   issuer combined with `delegateClients`, with no exception.
2. **Runtime (Go)** — `ValidateConfidentialClientTransport` refuses a loopback-HTTP
   issuer with confidential clients unless `insecureAllowConfidentialOverLoopbackHTTP`
   is set (`pkg/authserver/config.go:1475-1497`). That flag is a first-class CRD field
   (`:670-683`).

**vMCP cannot satisfy the HTTPS requirement itself.** `pkg/vmcp/server/server.go:752-783`
builds a plain `http.Server` over `net.Listen`; there is no `ListenAndServeTLS`, no CRD
TLS listener field, and no vMCP CLI TLS flag (`cmd/vmcp/app/commands.go:113-116` is
`host`/`port`/`enable-audit`/`session-ttl`). The operator hardcodes an `http://` status
URL (`cmd/thv-operator/controllers/virtualmcpserver_controller.go:2279`).

So a conforming deployment needs a **separate TLS edge** in front of vMCP, carrying the
**whole auth-server origin** — `/.well-known/oauth-authorization-server`,
`/.well-known/jwks.json`, `/oauth/authorize`, `/oauth/token`, `/oauth/register`,
`/oauth/callback`, and `/mcp`. Routing only `/mcp` breaks discovery and issuer validation.

Note the operator-managed Service cannot carry a pinned NodePort:
`VirtualMCPServer.spec.serviceType` is a type enum only
(`cmd/thv-operator/api/v1beta1/virtualmcpserver_types.go:50-54`), so a fixed host port
must belong to an edge Service the operator does not own.

This fixture took the other route — a loopback issuer plus the flag plus a cluster-local
CEL relaxation. See **Fixture changes** below and issue #6423.

---

## Q3 — Which Kubernetes Secret reference shape is accepted?

`clientSecretRef`, a required `*SecretKeyRef` with both `name` and `key` non-empty
(XValidation at `mcpexternalauthconfig_types.go:358`). No inline secret is accepted.
`clientId` requires MinLength=1; `scopes` and `audiences` each require MinItems=1 — a
delegate client cannot inherit every supported scope or audience by omission.

At reconcile the controller validates only Secret **metadata** and key presence, never
reading the value (`cmd/thv-operator/controllers/virtualmcpserver_deployment.go:1138-1145`).
The value reaches the pod as an env var, rendered into the generated config as:

```yaml
delegate_clients:
    - client_id: mecak8s-delegate
      client_secret_env_var: TOOLHIVE_DELEGATE_CLIENT_SECRET_0
      scopes: [openid, email]
      audiences: [<vmcp resource url>]
```

**The secret is compared as exact bytes.** The fixture creates Secrets with
`openssl rand -base64 48 | kubectl create secret --from-file=key=/dev/stdin`, which
stores 65 bytes — 64 base64 characters **plus a trailing newline**. A client that reads
the Secret through shell command substitution silently loses that newline and fails with
`invalid_client`. Any A3 broker must read the secret as raw bytes and must not trim.

---

## Q4 — What T1 conditions are accepted?

Sanitized claim inventory of an observed T1 (embedded-AS access token, ES256):

```text
iss        embedded AS issuer
aud        [ vMCP resource url ]
sub        opaque end-user identifier          (redacted)
scp        [openid, profile, email, offline_access]
client_id  the DCR-registered browser client   (redacted)
email,name upstream-derived identity claims
jti, iat, exp, tsid
```

Accepted conditions, from the pinned validator:

- **Scope source** — `scope` is read first, falling back to `scp`
  (`tokenexchange/validator.go:344-357`). Self-issued tokens carry `scp`.
- **Client binding** — for a configured delegate client presenting a self-issued subject
  token, the original-token `client_id` binding is deliberately relaxed
  (`tokenexchange/handler.go:644-718`). Without a delegate client the subject token must
  have been issued to the calling client.
- **Scope attenuation** — every requested scope must be present in *both* the delegate
  client's registered scopes and the subject token's grant, else `invalid_scope`
  (`handler.go:721-748`). A subject token with no scope claim grants nothing.
- **Audience** — bounded by the subject token; the delegated token cannot target a
  resource the subject token was not valid for (`grantAndBoundAudiences`).
- **Type** — `subject_token_type: urn:ietf:params:oauth:token-type:access_token`.

---

## Q5 — What T2 is emitted, and what must mecak8s validate?

Sanitized claim inventory of an observed T2:

```text
iss        embedded AS issuer                  (same as T1)
aud        [ vMCP resource url ]               (bounded by T1 and by delegate audiences)
sub        end user, carried unchanged from T1 (redacted)
act        { iss: <AS issuer>, sub: "mecak8s-delegate" }
client_id  mecak8s-delegate
scp        [openid, email]                     (narrowed from T1's four)
email,name carried from T1
jti, iat, exp
```

Response metadata: `token_type: bearer`,
`issued_token_type: urn:ietf:params:oauth:token-type:access_token`.

Observed properties:

- **Delegation, not impersonation** — `sub` stays the end user; `act.sub` records the
  delegate client that performed the exchange.
- **Scope narrowed** to the delegate's configured ceiling.
- **Lifetime clamped by the subject token** — T2's `exp` was *identical* to T1's, not
  `now + accessTokenLifespan`. `expires_in` came back 662s because T1 had 662s left. A
  broker cannot extend a caller's session by exchanging.

**mecak8s must later validate:** `iss` equals the expected AS issuer; `aud` contains its
own resource; `exp` unexpired; `act.sub` is the expected delegate client; `scp` carries
the required scopes. `sub` identifies the end user for downstream audit.

---

## Q6 — Demonstrable against Yardstick without provider grants?

**Yes.** With the two fixture changes below:

- `tools/list` with T2 returns `["yardstick_echo"]` (identical to T1's view).
- `tools/call yardstick_echo {"input":"a2delegated"}` returns
  `{"output":"a2delegated"}` with `structuredContent`.

No provider grant was involved. Yardstick receives no user credential: it has no
`externalAuthConfigRef`, `outgoingAuth.source` is `discovered`, and
`disableUpstreamTokenInjection: true`.

---

## Fixture changes required

Both are recorded inline in `deploy/mecak8s-vmcp/vmcp.yaml` and filed upstream.

### 1. Loopback HTTPS admission gate — issue #6423

`insecureAllowConfidentialOverLoopbackHTTP: true` plus a **cluster-local CEL relaxation**
of the rule at `mcpexternalauthconfig_types.go:495`. The Go layer already permits this
combination deliberately; CEL blocks it because it cannot parse a URL, so the supported
runtime path is unreachable through the operator.

The local patch uses a prefix test (`startsWith('http://127.0.0.1')`) which also matches
`http://127.0.0.1.evil.com`. That is acceptable only because it never leaves a disposable
Kind cluster, and is explicitly **not** the proposed upstream fix. It is reverted by any
`helm upgrade`.

### 2. Cedar authorization removed — issue #6424

With an embedded auth server that has upstream providers, the operator auto-derives
`authz.primaryUpstreamProvider: dex` (never set in the spec). Cedar then uses the
**upstream IdP token** as its primary claim source
(`pkg/authz/authorizers/cedar/core.go:607-623`). A delegated token has no
`UpstreamTokens["dex"]`, so claim resolution fails closed and every tool is filtered
before policy evaluation:

```text
WARN admission: tool authorization check failed, skipping
     tool=yardstick_echo error="upstream token for provider \"dex\" not found in identity"
```

The prior policy was `permit(principal, action, resource)` — permit-all, never reached.
Removing `authzConfig` therefore costs no security here and isolates the delegation
contract. Authentication via `incomingAuth` is unaffected.

---

## What this report does NOT establish

- **No mecak8s broker exists.** Per the handover, none was implemented.
- **No in-cluster reachability.** The issuer is `http://127.0.0.1:18080`, reachable only
  via port-forward from the host. A pod cannot resolve it. A3 needs the service-DNS or
  TLS-edge topology described in Q2.
- **No authorization of delegated callers.** Demonstrated with authentication only. That
  vMCP can *authorize* a delegated caller remains open behind #6424. This is a narrower
  claim than the fixture's original permit-all Cedar config implied, and is a deliberate,
  recorded decision rather than a silent consequence.
- **Scope vocabulary is OIDC-only.** The AS derives `scopesSupported` from Dex's
  discovery document, and Dex hardcodes `[openid, email, groups, profile, offline_access]`
  (`server/handlers.go:120`, Dex tree). An application scope such as `mcp:read` is
  unavailable, so `openid`/`email` stand in for application authorization. A2 proves the
  attenuation *mechanism*, not application-semantic scopes.
- **Not exercised:** T2 refresh or revocation, multiple concurrent delegate clients,
  non-loopback issuers, expiry-boundary behaviour, or a real HTTPS edge.

---

## Reproduction

1. `task vmcp-setup` (recreates the `mecatl-dev` Kind cluster — destructive). This now
   applies `crd-loopback-delegate-patch.json` and creates `vmcp-delegate-auth`
   automatically; both are spike scaffolding, removable once #6423 ships.
2. Port-forward `svc/vmcp-vmcp 18080:4483`.
4. Exchange T1 for T2 as `mecak8s-delegate` using `client_secret_post`, with the secret
   URL-encoded from **raw bytes** read straight out of the Secret (see Q3 — shell command
   substitution silently strips the trailing newline and yields `invalid_client`).
5. `initialize` / `notifications/initialized` / `tools/list` / `tools/call` against
   `/mcp` with T2 as bearer.


### Notes for the A3 harness

- **Wait on capability aggregation, not a fixed sleep.** A probe issued during vMCP
  startup returns zero tools for *every* token. This was initially misread as an
  authorization failure and was only caught by keeping T1 as a control. Always probe with
  a known-good token alongside the token under test.
- **The fixture is flaky.** An `etcd request timed out` aborted one apply; the Yardstick
  deployment pod had accumulated 27 restarts. Retry applies.
- **Verify conditions, not `phase`.** `phase: Ready` was reported while
  `AuthServerConfigValidated` was `False` on a later reconcile; `phase: Degraded` appeared
  alongside `"Virtual MCP server is running"` with no genuinely failing condition. Gate on
  `AuthServerConfigValidated` and `observedGeneration == generation`.

---

## Sanitization

No token, client secret, authorization code, refresh token, or Kubernetes Secret value
appears in this report. Claim inventories list claim names and shapes; `sub`, `jti`, and
client identifiers minted per-run are redacted. Secret material was handled via stdin and
curl config files throughout, never in process argv or shell history.

## Upstream issues filed

- **#6423** — delegateClients is unreachable on a loopback HTTP issuer, even though the
  runtime deliberately supports it.
- **#6424** — RFC 8693 delegated tokens are denied all tools when Cedar authz uses an
  upstream provider as its primary claim source.
- **#6417** (pre-existing) — no way to trust a private CA for upstream token endpoints.
  Not blocking; worked around with `SSL_CERT_FILE` in the vMCP pod.

## See also

- [The fixture this was qualified against](../../deploy/mecak8s-vmcp/README.md)
- [Design docs index](README.md)
