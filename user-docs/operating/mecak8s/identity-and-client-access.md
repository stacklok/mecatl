---
title: Secure mecak8s client access
description:
  Configure server TLS, verified caller identity, and global tool access.
sidebar_position: 4
---

# Secure mecak8s client access

Configure authentication and transport together before admitting callers.
Ownership scopes durable resources; shared filesystem isolation remains an
operator responsibility.

## Server TLS

Server TLS is independent of Redis TLS. It is off by default and uses an
operator-created, same-namespace `kubernetes.io/tls` Secret. The
[deployment quick start](/operating/mecak8s.md#quick-start) creates this Secret
and enables these values:

```yaml
tls:
  enabled: true
  secretName: mecak8s-tls
```

Create the Secret before enabling TLS. The chart projects `tls.certKey` and
`tls.keyKey` read-only with mode `0440`; their defaults are `tls.crt` and
`tls.key`. Set those values when your Secret uses different PEM key names. The
container receives the mounted paths `/var/run/secrets/tls/<certKey>` and
`/var/run/secrets/tls/<keyKey>` as `--tls-cert` and `--tls-key`.

TLS protects both gRPC and HTTP/SSE. The chart changes health and readiness
requests to HTTPS, while the Pod-only drain listener remains plaintext HTTP on
port 8082. Enable [OIDC](#enable-oidc) alongside TLS for a real-provider
deployment. For certificate rotation, follow the procedure below.

### Secure an edge-terminated backend

For an operator-owned edge TLS boundary, set
`security.tlsTerminatedUpstream=true` with OIDC. Keep `tls.enabled=true` for
re-encryption, or set it to `false` for a ClusterIP-only plaintext h2c backend
reachable only from the gateway or mesh.

Edge-terminated h2c sends the caller's bearer token across the pod network in
cleartext. Restrict the Service to gateway pods with NetworkPolicy or an mTLS
mesh. The chart does not verify the gateway or provide a general NetworkPolicy.
The gateway must forward the original bearer token and expose only the gRPC
route. Keep `/drain`, `/healthz`, and `/readyz` private.

The chart creates no Gateway, Route, Certificate, or `BackendTLSPolicy`.
Configure those resources or retain in-pod TLS for re-encryption. Move an
existing in-pod TLS release to h2c through a blue-green or maintenance cutover.

## Rotate server TLS certificates

When `--tls-cert` and `--tls-key` point into a Kubernetes projected Secret,
`mecak8s` watches their parent directories and reloads a complete matching pair
after the projection settles. A malformed or mismatched intermediate generation
is rejected and the last valid certificate continues serving. Existing
connections are unaffected; new TLS handshakes use the replacement without a pod
restart. A fixed internal observer emits a bounded warning once for a
certificate generation that becomes expiring or expired; observation does not
disable the last-valid certificate. `--client-ca` is intentionally static and
still requires a pod restart to change the trusted client identities.

## Configure caller identity

Enable OIDC to authenticate every request and isolate sessions, schedules,
teams, and memory by the verified `(issuer, subject)` owner. See
[Caller identity and OIDC](/features/security-and-execution/caller-identity.md)
for the shared behavior and client workflows.

## Check existing data first

Enabling OIDC changes access, not just attribution:

- Callers can act on and list only their own sessions, schedules, teams, memory,
  and event streams. Foreign resources appear absent.
- Different callers can use the same memory keys and schedule names without
  sharing data.
- Existing ownerless records become unavailable. Export or back them up before
  enabling ownership; Mecatl does not adopt them.

OIDC does not isolate the shared pod filesystem. Isolate caller workspaces at
the deployment layer. Raw storage drivers also remain trusted infrastructure, so
keep them inaccessible to tenants.

## Before admitting callers: inventory ownerless records

Stage the OIDC deployment with tenant traffic blocked and configure one exact
`storage_management.principals` issuer/subject pair for the operator. Call the
authenticated `GetStorageHealth` RPC (or `GET /v1/storage/health`) as that
principal and inspect the `ownerless_session_count` / `ownerless_session_ids`
and `ownerless_schedule_count` / `ownerless_schedule_names` fields. The
identifiers are bounded samples (the corresponding `*_truncated` bit says when
the count is larger); no transcript, memory value, or schedule prompt is
returned. If either `*_available` field is false, do not proceed until that
backend exposes the required metadata inventory.

After ownership is enabled, retention and scheduling skip ownerless records.
Disabling enforcement makes them accessible again but does not assign an owner
or replay skipped work.

## Validator and bounded signing-key cache

An invalid OIDC configuration, including an unreachable initial key fetch, fails
closed at startup rather than serving unauthenticated traffic. After a
successful fetch, the last good JWKS can cover a short IdP outage. The chart's
default `oidc.maxJWKSStaleness` sets `--oidc-max-jwks-staleness=1h`: once keys
are older than that, the validator refreshes before deciding and returns **503
Service Unavailable** when it cannot obtain current keys. A bad, expired,
wrong-issuer, or wrong-audience token remains **401**. Set the flag to `0` only
to deliberately accept unbounded cached-key availability and its signing-key
revocation exposure.

This bounds **signing-key** revocation exposure during an IdP outage; it does
not provide per-token revocation before normal token expiry. The JWKS cache is
process-local and unpersisted, so a restarted pod fetches current keys again.

## Enable OIDC

Add the OIDC settings to your Helm values:

```yaml
oidc:
  enabled: true
  issuer: https://idp.example.com/realms/mecatl
  audience: mecatl
```

|Value|Flag|Meaning|
|-|-|-|
|`oidc.issuer`|`--oidc-issuer`|your IdP's issuer URL, compared **byte-exact** against the token's `iss`|
|`oidc.audience`|`--oidc-audience`|the audience this deployment accepts. **Required**. An audience-less verifier accepts tokens minted for a different service|
|`oidc.jwksURI`|`--oidc-jwks-uri`|_optional._ Pin the signing-key endpoint and skip discovery. Only for an air-gapped or pinned-key deployment; leave it out and the issuer's discovery document is used|
|`oidc.maxJWKSStaleness` (default `1h`)|`--oidc-max-jwks-staleness`|Maximum time a last-good JWKS remains trusted when refresh cannot reach the IdP. `0` deliberately disables the bound.|

Use an HTTPS issuer. The validator rejects JWKS endpoints that resolve to
private, loopback, link-local, or metadata addresses unless you set
`oidc.allowPrivateHTTPSIssuer: true` and identify the CA with `oidc.caSecret`
and `oidc.caKey`.

Enabling OIDC creates a narrow NetworkPolicy for pods labelled as raw storage
drivers. It does not create a general workload NetworkPolicy. Keep raw driver
endpoints off tenant-facing Services and route callers through the authenticated
`mecak8s` Service.

## Advertise login metadata

To let remote `mecatui` discover login settings, configure the externally
reachable RFC 9728 resource URL, public client ID, and optional scopes:

```yaml
oidc:
  resource: https://mecatl.example.com
  clientID: mecatui
  scopes: [openid, profile, offline_access]
```

The chart rejects partial profiles and never derives the public resource URL
from pod addresses. Metadata discovery is anonymous HTTPS and remains separate
from authenticated gRPC transport.

## Security boundaries

|Boundary|Operator responsibility|
|-|-|
|**Filesystem isolation**|Ownership does not isolate a shared pod filesystem. Use separate workspaces or deployments for tenants that must not share files.|
|**Raw storage drivers**|Drivers are trusted infrastructure and do not authenticate end users. Restrict them to agent workloads and keep them off public Services.|
|**Existing ownerless data**|Enabling ownership makes it unavailable. Export or back it up before enabling OIDC.|
|**Signing-key revocation**|The default one-hour JWKS staleness bound permits cached signing keys during a short IdP outage. Tokens remain valid until their own expiry.|
|**Rate limits and quotas**|`mecak8s` has no rate-limit flags. Enforce caller limits at the gateway or mesh.|
|**Store confidentiality**|Production Redis requires verified TLS. Client-certificate authentication is not supported.|
|**Identity data**|The token's `name` claim, often an email address, is stored unredacted in session snapshots, schedule records, and durable event actor metadata. Configure retention and deletion accordingly.|
|**Authentication visibility**|Mecatl emits no authentication metric and does not log authentication failures. The caller sees the 401 or 503 response.|

## Connect global MCP servers

Use `mcp.servers` for global streaming-HTTP connections. The chart supports no
authentication, a bearer from a Kubernetes Secret, or an OAuth profile. It does
not accept inline credentials, arbitrary headers, stdio/SSE transports, browser
credentials, or a writable credential store.

Global OAuth profiles enforce an exact-origin network policy. Broker OAuth must
set `additionalOrigins: []`, `privateOrigins: []`, and `maxRedirects: 0`.
Startup performs the final URL, origin, and loopback validation after Helm has
validated the values and Secret references.

```yaml
mcp:
  servers:
    - name: public
      url: https://public-mcp.example/mcp
      auth: { mode: none }
    - name: github
      url: https://github-mcp.example/mcp
      auth:
        mode: staticBearer
        staticBearer:
          secretKeyRef: { name: mecak8s-mcp, key: github-token }
```

The static token is projected as `MCP_GITHUB_TOKEN`; it never appears in Helm
values, arguments, or a ConfigMap. For a browser-based GitHub OAuth App, use
`auth.mode: oauth` with
`upstream: {mode: oauth2, oauth2: {authorizationEndpoint, tokenEndpoint}}`
instead of `issuer`, and optionally declare a static `tools` catalog. OAuth uses
the per-session broker. One enrollment can cover several protected upstreams;
each token is sent only to its configured backend.

Set `mcp.broker.callbackURL` to the final public HTTPS callback. Route the full
`/v1/mcp/broker/` prefix to the `mecak8s` HTTP listener. Broker mode requires
OIDC caller identity.

Declared protected tools appear as placeholders before enrollment. Successful
enrollment discovers every protected backend and atomically replaces the
placeholders with a frozen per-session catalog. Failure exposes no partial
catalog. Keep preregistered client secrets in `SecretKeyRef`; broker metadata
and profiles remain non-secret ConfigMap data.

:::caution[Broker mode is single-replica]

Broker sessions and OAuth state are process-local. The chart requires
`replicaCount: 1` and uses the `Recreate` strategy when `mcp.broker.callbackURL`
is set. Broker mode does not provide high availability or zero-downtime
rollouts.

:::

For a preregistered OAuth client, add this shape to the server entry:

```yaml
auth:
  mode: oauth
  oauth:
    issuer: https://issuer.example
    client:
      mode: preregistered
      preregistered:
        id: mecak8s
        secretKeyRef: { name: mecak8s-mcp-oauth, key: client-secret }
    scopes: [mcp.read]
    requestRefreshToken: true
    network:
      additionalOrigins: []
      privateOrigins: []
      maxRedirects: 0
```

Set `client.mode: cimd` with `cimd.documentURL` for client ID metadata, or
`client.mode: dcr` with an HTTPS RFC 8414 discovery URL for dynamic client
registration. A plain OAuth2 upstream uses explicit `authorizationEndpoint` and
`tokenEndpoint` values instead of `issuer`.

## Maintain MCP network access and credentials

Keep MCP and OAuth endpoints on HTTPS and permit their egress through your
NetworkPolicy or mesh. `insecureHTTP: true` is accepted only for non-loopback,
non-OAuth HTTP servers and allows a bearer to cross the pod network in
cleartext. Use it only for an isolated in-cluster endpoint.

OAuth profile changes trigger a rollout. After changing a static bearer or OAuth
client Secret, roll the Deployment and keep both credentials valid during the
transition. The chart reserves these environment names:
`MECATL_INSTALLATION_ID`, `MECATL_DRIVER_AUTH_TOKEN`, and authentication names
generated by `mcp.servers`. Rendering fails when `extraEnv` collides with one.

Broker authorization storage uses a separate Redis connection that does not
reload Redis CA or ACL files. Restart the pod after rotating those files. The
main session-store connection can remain healthy, so `/readyz` does not detect a
stale broker connection.

## Give clients connection details

Give users the public gRPC address, server CA requirements, and advertised login
metadata. They can then follow [Connect to a server](/mecatui/remote-servers.md)
without access to Kubernetes or server credentials. Broker catalog inspection
also belongs to the connected client workflow.

## Next steps

- [Observe and troubleshoot the service](/operating/mecak8s/observe-and-troubleshoot.md).
- [Scale and recover the deployment](/operating/mecak8s/scale-recover-and-upgrade.md).

<span id="troubleshooting-start-here"></span>

## Troubleshooting

For a first-deployment 401, check the token audience and issuer:

1. **The audience is not in the token.** Keycloak, for example, puts only
   `account` in `aud` by default; your client ID appears only if you attach an
   Audience protocol mapper. Then `--oidc-audience=<CLIENT_ID>` never matches
   and **every** caller gets 401. Inspect `aud` locally without sharing or
   logging the token.
2. **The issuer string does not match.** `iss` is compared byte-exact, and an
   IdP stamps whatever external hostname it is configured to advertise,
   regardless of how your pods reach it. Take `--oidc-issuer` from the IdP's
   `/.well-known/openid-configuration`, never from the in-cluster Service URL.

Beyond that: a **401** means the credential was rejected; a **503** means a
required JWKS refresh could not obtain current keys after the configured
staleness bound. Before that bound, a last-good JWKS can keep validation
available during a short IdP outage. Authn failures are **not currently
logged**, so inspect the caller response's status code.
