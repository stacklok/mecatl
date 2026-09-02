# ADR 0286 — Public and private OIDC issuers are two transports, not one policy

- Status: Accepted
- Date: 2026-09-02
- Scope: `mecatui` remote OIDC issuer transport and saved connection metadata
- Supersedes: ADR 0277 (private-only issuer transport clauses), ADR 0284 (its implicit CA-presence mode selection and unhardened public client)
- Superseded by:

## Context

`mecatui login` and the saved-credential refresh path hardcoded `PrivateHTTPS:
true`, forcing every issuer through `authn/oidc/scopedhttps` — a transport built
for the kind/Keycloak fixture, which refuses any address that is not private,
loopback, or link-local. No flag could override it, so Okta, Auth0, Google, or
any production IdP was rejected outright, with `--tls-ca` making no difference
because the gate is on the resolved IP, not on certificate trust (issue #942).

That boolean conflated two orthogonal questions: *is the CA private* and *must
the issuer resolve to a private address*. A public CA at a private address and a
private CA at a public one are both real configurations.

ADR 0284 made `--tls-ca` optional and selected the mode implicitly: a saved
connection naming an issuer CA gets the scoped private transport, one without
gets a bare `http.Client`. That unblocks public issuers, but keeps the very
conflation this issue named, now keyed on the CA's presence rather than a
boolean, and leaves the public path on an unhardened client.

A second attempt added an address policy to the scoped transport, keeping one
transport for both modes. That does not hold
up. Applied to a public issuer, the scoped machinery defends against DNS
rebinding — but a rebound dial sends a ClientHello for the issuer hostname to a
host that cannot present a chain for it, and the response never reaches the
attacker. The issuer URL is operator-supplied at enrollment, not
attacker-influenced input. Ordinary certificate verification is already the
control.

It also cost real behaviour:

- The issuer-only authority allowlist rejects any provider publishing its token
  or JWKS endpoint on another host. Google serves `oauth2.googleapis.com` and
  `www.googleapis.com` from an `accounts.google.com` issuer; the exchange failed
  inside the transport and surfaced as an opaque `ErrTokenExchange`.
- Construction-time DNS pinning is never refreshed, so a CDN-fronted issuer
  rotates past its pinned answers and signs the user out mid-session.
- Choosing a per-authority root pool at dial time forces `InsecureSkipVerify`
  plus a hand-rolled chain verification — a bespoke replacement for a stdlib
  control, one careless edit from a silent no-op.

A private issuer is a genuinely different problem. There the operator supplies
an internal CA that legitimately signs many internal services, so TLS alone
cannot say which one answered. Authority scoping and per-authority pools supply
the control that widened trust removed.

## Decision

Persist the closed `issuer_address_policy` as `public` or `private` alongside
each saved connection. A missing legacy policy means private; a malformed one
quarantines the row. `mecatui login` defaults to public and accepts optional
`--tls-ca`, which REPLACES system roots. `--private-issuer` requires that CA and
selects private address admission.

Treat the two policies as two transports.

A public issuer gets an ordinary `http.Client`: system roots, standard TLS
hostname verification by `crypto/tls`, a TLS 1.2 floor, refused redirects, and a
scheme/userinfo check on every request so a discovery document cannot downgrade
a later hop. No authority allowlist, no DNS pinning, no per-authority pools, no
`InsecureSkipVerify`. Defence in depth stays at the dialer: a
`net.Dialer.Control` hook refuses non-public resolved addresses, running after
resolution on every new connection rather than pinning one answer set, and
delegating to `session.ValidateResolvedIP` — the engine's single dial-layer
predicate — instead of keeping a local range list.

A private issuer keeps `scopedhttps` unchanged, with its mandatory CA. Its one
tightening: a host resolving to a mix of private and public addresses now fails
the whole lookup instead of dialing the private subset, that being the answer
shape a rebinding attempt produces.

`clientauth.IssuerHTTPClient` is the single mapping from a persisted
`IssuerAddressPolicy` to a transport, shared by login, refresh, and revocation.
Selection fails closed: a `LoginConfig` carries either a caller-supplied
`HTTPClient` (test fixtures) or a valid policy, never neither. The former
zero-value fallback returned a bare client with system roots and no address
screening, a silent downgrade rather than an error.

Both registry write paths share one `normalizeConnection`.

## Consequences

Public providers work, including split-origin ones. Operators must explicitly
opt into private issuer addressing, and legacy enrollments retain their safer
historic private posture until re-enrolled. No hand-rolled chain verification
and no bespoke address-range list are introduced.

The dial screen inherits `ValidateResolvedIP`'s gaps: it admits the Azure
platform VIP, RFC 5737 documentation ranges, and RFC 1112 reserved space. This
is accepted deliberately — those are not internal services worth probing, TLS is
the control on this path, and the right fix is to close them in the shared
predicate so `webfetch` and the MCP OAuth path benefit too, rather than growing a
third list here.

The private-path tightening also binds `mecated`'s
`--oidc-allow-private-https-issuer` validator, which shares the transport.

Verified against a real public IdP: `mecatui login mecak8s.stacklok.dev:443`
with an Okta custom authorization server
(`https://stacklok.okta.com/oauth2/aus26lv49q56ue2981d8`) and no `--tls-ca`
completed discovery, PKCE authorization, token exchange, and JWKS validation,
persisting `issuer_address_policy: public` with no CA reference alongside an
untouched `private` fixture row. That same command previously failed with
`host has no private address`.

Split-origin providers remain covered only by the mutation-tested transport
guard. An Okta custom authorization server publishes its token and JWKS
endpoints on the issuer's own host, so the live check above would have passed
even with the authority allowlist still in place; a provider that splits them
(Google) has not been exercised end to end, because the offline suite cannot
reach one.

## See also

- [ADR 0277](./0277-remote-mecatui-oidc.md)
- [Architecture](../architecture.md)
- [TUI guide](../tui.md)
