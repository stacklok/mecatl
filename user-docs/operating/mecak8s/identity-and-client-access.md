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
Configure those resources or retain in-pod TLS for re-encryption.

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

<span id="check-existing-data-first"></span>

## Ownership and storage safety

With OIDC enabled, callers can act on and list only their own sessions,
schedules, teams, memory, and event streams. Foreign resources appear absent.
Different callers can use the same memory keys and schedule names without
sharing data. Ownerless records are inaccessible through caller APIs and are
excluded from retention and scheduling; Mecatl does not adopt them.

OIDC does not isolate the shared pod filesystem. Isolate caller workspaces at
the deployment layer. Raw storage drivers also remain trusted infrastructure, so
keep them inaccessible to tenants.

<span id="before-admitting-callers-inventory-ownerless-records"></span>

### Investigate ownerless records

Configure one exact `storage_management.principals` issuer/subject pair for the
operator. Call the authenticated `GetStorageHealth` RPC (or
`GET /v1/storage/health`) as that principal and inspect the
`ownerless_session_count` / `ownerless_session_ids` and
`ownerless_schedule_count` / `ownerless_schedule_names` fields. The identifiers
are bounded samples (the corresponding `*_truncated` bit says when the count is
larger); no transcript, memory value, or schedule prompt is returned. If either
`*_available` field is false, the inventory is incomplete: verify that backend's
metadata-inventory support before making storage decisions.

Preserve backups or an authorized export of ownerless data while investigating.
Do not disable ownership enforcement to recover access on a tenant-facing
service: that exposes ownerless records without assigning an owner and does not
replay skipped work. Keep any direct storage recovery isolated from tenant
traffic and restricted to authorized operators.

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

## Own sessions by the company identity

When users sign in through the authorization server that issues the identity
broker's user tokens (AS-1), point OIDC at that server and key owners by the
company identity in each token's `sub_id` claim:

```yaml
oidc:
  enabled: true
  issuer: https://auth.example.com/as
  audience: https://mecatl.example.com/api
  ownerIdentity: sub_id
  acceptedClientIDs: [mecatui, studio]
```

`oidc.ownerIdentity` sets `--oidc-owner-identity` and accepts only `sub_id` or
the default empty string. `oidc.acceptedClientIDs` sets
`--oidc-accepted-client-ids`; the chart rejects one without the other. The
experimental identity broker requires this setting: with
`--enable-identity-broker` and OIDC enabled, `mecak8s` refuses to start without
it, and the chart refuses `identityBroker.enabled` without it. When `oidc.clientID` is set, the chart requires it in
`oidc.acceptedClientIDs`. Existing owners and principal settings keep matching
only if AS-1's upstream IdP returns each user's same issuer and `sub`, which
`mecak8s` cannot check at startup. For that precondition, the token checks, and
the requirements on AS-1, see
[Own resources by the `sub_id` identity](/features/security-and-execution/caller-identity.md#own-resources-by-the-sub_id-identity).

## Host AS-1 in mecak8s

`mecak8s` can host AS-1, Mecatl's own sign-in server, in front of your company
IdP. `mecatui` users sign in at AS-1, AS-1 signs them in at the company IdP,
and `mecak8s` validates the access tokens AS-1 issues, keyed by the company
identity in their `sub_id` claim. These values turn it on:

```yaml
replicaCount: 1
extraArgs: [--no-shell]
oidc:
  enabled: true
  audience: https://mecatl.example.com/api
  resource: https://mecatl.example.com
  ownerIdentity: sub_id
identityBroker:
  enabled: true
  subjectClaim: oid
  as1:
    issuer: https://mecatl.example.com/as1
    tuiClientID: mecatui
    signingKey:
      secretName: mecak8s-as1
      key: signing-key.pem
    upstream:
      issuer: https://login.microsoftonline.com/<TENANT_ID>/v2.0
      clientID: <AS1_CLIENT_ID>
      clientSecret:
        secretName: mecak8s-as1
        key: upstream-client-secret
```

|Value|Flag|Meaning|
|-|-|-|
|`identityBroker.enabled`|`--enable-identity-broker`|Starts the identity broker. Requires `replicaCount: 1`, `oidc.ownerIdentity: sub_id` when OIDC is enabled, and no shell in the process: `--no-shell` in `extraArgs`, or `--shell=` with `execution.enabled` to keep the remote Shell. The chart refuses to render without one. The Deployment rolls out with `Recreate`.|
|`identityBroker.subjectClaim` (default `sub`)|`--identity-broker-subject-claim`|The company IdP's ID-token claim that names each user. Requires `identityBroker.as1.issuer`.|
|`identityBroker.as1.issuer`|`--identity-broker-as1-issuer`|AS-1's HTTPS issuer URL. Setting it hosts AS-1 and requires `oidc.enabled`, `oidc.audience`, and `oidc.resource`.|
|`identityBroker.as1.tuiClientID`|`--identity-broker-as1-tui-client-id`|The client ID `mecatui` uses at AS-1.|
|`identityBroker.as1.signingKey`|`--identity-broker-as1-signing-key-file`|_Optional._ Secret key holding AS-1's PEM signing key (EC P-256 or RSA).|
|`identityBroker.as1.upstream.issuer`|`--identity-broker-as1-upstream-issuer`|The company IdP's HTTPS issuer URL.|
|`identityBroker.as1.upstream.clientID`|`--identity-broker-as1-upstream-client-id`|AS-1's client ID at the company IdP.|
|`identityBroker.as1.upstream.clientSecret`|`--identity-broker-as1-upstream-client-secret-file`|_Optional._ Secret key holding AS-1's client secret at the company IdP. Without it, AS-1 is a public client there.|

Create the Secret before installing; the chart mounts each key read-only. Leave
`oidc.issuer`, `oidc.clientID`, and `oidc.acceptedClientIDs` empty: the chart
sets them to AS-1's issuer and `tuiClientID`. It refuses values that differ,
any `oidc.jwksURI` (AS-1's discovery document supplies its keys),
`oidc.allowPrivateHTTPSIssuer`, and identity broker flags in `extraArgs`.

### Choose the issuer

AS-1 serves its sign-in endpoints and discovery documents under the issuer
URL's path on the `mecak8s` HTTP listener. The issuer can be a dedicated
hostname, such as `https://signin.example.com`, or a path on the `mecak8s`
host, such as `https://mecatl.example.com/as1`. Route that hostname or path
prefix to the HTTP listener so browsers reach it.

AS-1's sign-in endpoints are anonymous and `mecak8s` rate limits them per
client address, which behind an ingress is the ingress itself, so also
configure a per-client rate limit for `<ISSUER>/oauth/*` at your ingress.

Keep the issuer once users have signed in. It is in every token, in each user's
saved `mecatui` login, and in the redirect URI registered at the company IdP.

### Register AS-1 at the company IdP

Ask the IdP administrator to register one web application for AS-1 with the
redirect URI `<ISSUER>/oauth/callback`. This registration replaces the separate
`mecatui` and Studio registrations: every client signs in through AS-1, so
per-application IdP policies can no longer tell those clients apart. AS-1
requests the `offline_access` scope with `prompt=consent`.

Set `subjectClaim` to a claim that is the same for a user across applications.
For Microsoft Entra ID, use `oid`, because Entra ID issues a different `sub` to
each application. Keep the default `sub` for an IdP whose `sub` does not depend
on the application. The claim names the user to MCP gateways; session
ownership still keys on the access token's `sub_id`.

### Sign-in and restarts

Users approve a consent page at AS-1 when they sign in. AS-1 keeps its users,
pending sign-ins, and refresh tokens in memory, so every pod restart or rollout
signs all users out of AS-1 and they run `mecatui login` again. With
`signingKey`, access tokens issued before a restart keep verifying until they
expire. Without it, AS-1 generates a key at startup and those tokens stop
verifying immediately.

Studio does not support signing in through AS-1 yet. Turning on
`oidc.ownerIdentity: sub_id` for a deployment with existing sessions leaves
those sessions with their previous owners; see
[Own resources by the `sub_id` identity](/features/security-and-execution/caller-identity.md#own-resources-by-the-sub_id-identity)
before switching.

## Security boundaries

|Boundary|Operator responsibility|
|-|-|
|**Filesystem isolation**|Ownership does not isolate a shared pod filesystem. Use separate workspaces or deployments for tenants that must not share files.|
|**Raw storage drivers**|Drivers are trusted infrastructure and do not authenticate end users. Restrict them to agent workloads and keep them off public Services.|
|**Ownerless data**|Caller APIs, retention, and scheduling exclude it. Preserve backups and use the [ownerless-record investigation](#investigate-ownerless-records); do not disable ownership on a tenant-facing service.|
|**Signing-key revocation**|The default one-hour JWKS staleness bound permits cached signing keys during a short IdP outage. Tokens remain valid until their own expiry.|
|**Rate limits and quotas**|`mecak8s` has no rate-limit flags. Enforce caller limits at the gateway or mesh.|
|**Identity broker and AS-1**|The identity broker and AS-1 run inside the `mecak8s` process and keep their state in its memory, so there is no separate broker listener or store for executor Pods to reach. AS-1's sign-in endpoints are public by design. Keep executor workloads on the `mecatl-execution` default-deny network policy, and do not give a workload profile egress to the `mecak8s` Service.|
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

:::caution[Snapshots without provenance need a fresh session]

A session snapshot without workspace broker provenance cannot identify which
broker tools its authority covered. Mecatl cannot infer or repair that provenance.
Start a fresh session before using `/tools-connect`.

:::

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

Beyond that: a **401** means the credential was rejected (see the
[authentication errors](/reference/http-sse-api.md#authentication-errors)
reference for its challenge). A **503** means a
required JWKS refresh could not obtain current keys after the configured
staleness bound. Before that bound, a last-good JWKS can keep validation
available during a short IdP outage. Authn failures are **not currently
logged**, so inspect the caller response's status code.
