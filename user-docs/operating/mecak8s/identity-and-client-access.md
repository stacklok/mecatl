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
from pod addresses. With hosted AS-1, a `scopes` list must include
`offline_access`, or `mecak8s` refuses to start: `mecatui` requests the listed
scopes exactly, and the identity broker can convert only a sign-in that
includes `offline_access`. Metadata discovery is anonymous HTTPS and remains separate
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
  acceptedClientIDs: [mecatui]
```

`oidc.ownerIdentity` sets `--oidc-owner-identity` and accepts only `sub_id` or
the default empty string. `oidc.acceptedClientIDs` sets
`--oidc-accepted-client-ids`; the chart rejects one without the other, except
that a [hosted AS-1](#host-as-1-in-mecak8s) supplies its TUI client ID when
`oidc.acceptedClientIDs` is unset. Studio does not support this setting; see
[Mecatl Studio](/operating/studio.md). The
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
identity in their `sub_id` claim.

Enabling AS-1 makes it the only issuer `mecak8s` accepts. Access tokens the
company IdP issues directly stop working, so every client must sign in through
AS-1. Studio does not support AS-1 yet and stops working until it does.

These values turn it on:

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
  trustDomain: agents.example.com
  audience: https://mecatl.example.com/identity-broker
  keys:
    agentIssuer:
      secretName: mecak8s-identity-broker
      key: agent-issuer.pem
    brokerClient:
      secretName: mecak8s-identity-broker
      key: broker-client.pem
      keySet:
        secretName: mecak8s-identity-broker
        key: broker-client-jwks.json
    as1Signing:
      secretName: mecak8s-as1
      key: signing-key.pem
  subjectClaim: oid
  as1:
    issuer: https://mecatl.example.com/as1
    tuiClientID: mecatui
    upstream:
      issuer: https://login.microsoftonline.com/<TENANT_ID>/v2.0
      clientID: <AS1_CLIENT_ID>
      clientSecret:
        secretName: mecak8s-as1
        key: upstream-client-secret
```

|Value|Flag|Meaning|
|-|-|-|
|`identityBroker.enabled`|`--enable-identity-broker`|Starts the identity broker. Requires `trustDomain`, `audience`, and `keys.agentIssuer` below. Also requires `replicaCount: 1`, `oidc.ownerIdentity: sub_id` when OIDC is enabled, an empty `workspace` (see [Keep the broker's keys out of sessions](#keep-the-brokers-keys-out-of-sessions)), and no shell in the process: `--no-shell` in `extraArgs`, or `--shell=` with `execution.enabled` to keep the remote Shell. The chart refuses to render without one. The Deployment rolls out with `Recreate`. This value is the only way to start the broker: the chart refuses `--enable-identity-broker` in `extraArgs`. With protected MCP servers configured, adding or removing a [gateway entry](#declare-the-embedded-gateway) changes the audience of the MCP gateway's tokens, so those servers must be [enrolled](/features/security-and-execution/mcp-oauth-and-credentials.md) again. Each session's main agent gets its own agent identity, and so does each [subagent and team member](/features/agent-behavior/subagents-and-teams.md) it starts. Agents call the MCP gateway's tools under their own identity. A helper that runs no named agent gets all of them. A helper running a named agent, and the main agent of a session created with one (`agent_definition_name`), get the tools of each server its `mcpServers` names by reference, minus each tool its `disallowedTools` names as `mcp__<server>__<tool>`; an inline server entry and the `tools` list add no gateway tools. These agents never get another MCP broker tool, and team members of such a session get no gateway tools. A session created with a named agent cannot [enroll](/features/security-and-execution/mcp-oauth-and-credentials.md), so its main agent gets only the tools a server declares in `mcp.servers[].auth.oauth.tools`. The broker refuses a gateway call outside an agent's limit: the tools its agent allows, as above, narrowed by the limit of the agent that started it. These gateway calls need a gateway the broker routes, which the chart doesn't configure yet, so for now agents get no gateway tools. The broker accepts only built-in agents or a [named agent](/features/agent-behavior/named-agents.md#choose-where-definitions-are-discovered) from a directory passed with `--agents-dir` in `extraArgs` and mounted with `extraVolumes` and `extraVolumeMounts`. A session that uses a named agent from any other location cannot run, a subagent or team member that names one cannot start, and a `--agents-dir` directory that does not exist stops startup. With an operator [`harness_context`](/operating/settings.md#select-harness-context-sources) policy, which can let another source win an agent name, only built-in agents can run.|
|`identityBroker.trustDomain`|`--identity-broker-config`|The agent trust domain, such as `agents.example.com`. It names every agent the broker vouches for, so keep it once agents act.|
|`identityBroker.audience`|`--identity-broker-config`|The broker audience every agent credential names.|
|`identityBroker.keys.agentIssuer`|`--identity-broker-config`|Secret key holding the PEM EC P-256 private key that signs agent credentials.|
|`identityBroker.keys.brokerClient`|`--identity-broker-config`|Secret key holding the PEM EC P-256 private key the broker authenticates with at AS-1. Its `keySet` names the Secret key holding the matching public JWK Set, whose `kid` is the key's RFC 7638 thumbprint. Every key in the set must be EC P-256. Required with `identityBroker.as1.issuer` or an `identityBroker.gateways` entry, which also registers the key at the MCP gateway.|
|`identityBroker.keys.as1Signing`|`--identity-broker-config`|Secret key holding AS-1's PEM signing key (EC P-256 or RSA). Required with `identityBroker.as1.issuer`, and allowed only with it.|
|`identityBroker.keys.agentIssuer.fallbacks`, `identityBroker.keys.as1Signing.fallbacks`|`--identity-broker-config`|_Optional._ Lists of `{secretName, key}` naming earlier signing keys during a [rotation](#rotate-keys-with-the-chart). Each requires the key it backs.|
|`identityBroker.settings`|`--identity-broker-config`|_Optional._ [Lifetimes, clock skew, and timeouts](#lifetimes-clock-skew-and-timeouts). Each value is empty by default, which keeps `mecak8s`'s default.|
|`identityBroker.subjectClaim`|`--identity-broker-subject-claim`|The company IdP's ID-token claim that names each user. Required with `identityBroker.as1.issuer`, and allowed only with it.|
|`identityBroker.as1.issuer`|`--identity-broker-as1-issuer`|AS-1's HTTPS issuer URL. Setting it hosts AS-1 and requires `oidc.enabled`, `oidc.audience`, and `oidc.resource`. AS-1 accepts only an absolute `http` or `https` URL with a host and no fragment as the audience, such as `https://mecatl.example.com/api`.|
|`identityBroker.as1.tuiClientID`|`--identity-broker-as1-tui-client-id`|The client ID `mecatui` uses at AS-1.|
|`identityBroker.as1.upstream.issuer`|`--identity-broker-as1-upstream-issuer`|The company IdP's HTTPS issuer URL.|
|`identityBroker.as1.upstream.clientID`|`--identity-broker-as1-upstream-client-id`|AS-1's client ID at the company IdP.|
|`identityBroker.as1.upstream.clientSecret`|`--identity-broker-as1-upstream-client-secret-file`|_Optional._ Secret key holding AS-1's client secret at the company IdP. Without it, AS-1 is a public client there.|
|`identityBroker.as1.upstream.caSecret`|`--identity-broker-as1-upstream-ca-file`|_Optional._ Secret key holding a PEM CA bundle AS-1 trusts for the company IdP's certificate, beside the system roots. It does not admit a private address. Required with `identityBroker.as1.upstream.allowPrivateAddresses`.|
|`identityBroker.as1.upstream.allowPrivateAddresses`|`--identity-broker-as1-upstream-allow-private-addresses`|_Optional._ Lets AS-1 reach a company IdP at a private or link-local address, such as an in-cluster Service. It applies to every request AS-1 makes to the company IdP, including discovery, the endpoints discovery returns, and redirects, not just the issuer host. Requires `identityBroker.as1.upstream.caSecret`.|

Create the Secrets before installing; the chart mounts each key read-only and
writes the trust domain, audience, and key paths to the identity broker's
configuration file, a ConfigMap. `mecak8s` refuses to start when a required key
is missing, when one key serves two roles, or when the key set lacks the broker
client's public key under its `kid`; the
[configuration file](#identity-broker-configuration-file) section lists every
rule.

The Deployment rolls out with `Recreate`, so the running pod stops before the
new one starts. A Secret or key that does not exist leaves the new pod in
`ContainerCreating` and the broker down. Before you install or upgrade, run
`kubectl describe secret <SECRET_NAME>` for each Secret to confirm it lists the
key the chart mounts; the command shows key names and sizes, not values. Run
`helm upgrade` with `--wait`: without it, Helm reports success while the new pod
is still starting.

Leave
`oidc.issuer`, `oidc.clientID`, and `oidc.acceptedClientIDs` empty: the chart
sets them to AS-1's issuer and `tuiClientID`. It refuses values that differ,
any `oidc.jwksURI` (AS-1's discovery document supplies its keys),
`oidc.allowPrivateHTTPSIssuer`, and identity broker flags in `extraArgs`.

### Create the broker's keys

Each role needs its own key. The broker client's key set is a JWK Set holding
the public half of `keys.brokerClient` under a `kid` equal to the key's RFC
7638 thumbprint. The repository's
[`broker-client-key-set.sh`](https://github.com/stacklok/mecatl/blob/main/deploy/mecak8s-idbroker/broker-client-key-set.sh)
derives it from the private key with `openssl`. From a checkout, these commands
create the Secret the example values name:

```sh
openssl ecparam -name prime256v1 -genkey -noout -out agent-issuer.pem
openssl ecparam -name prime256v1 -genkey -noout -out broker-client.pem
deploy/mecak8s-idbroker/broker-client-key-set.sh < broker-client.pem > broker-client-jwks.json
kubectl create secret generic mecak8s-identity-broker \
  --from-file=agent-issuer.pem --from-file=broker-client.pem --from-file=broker-client-jwks.json
```

Generate AS-1's signing key the same way, as `signing-key.pem` in the
`mecak8s-as1` Secret, beside `upstream-client-secret`. Delete the private key
files once the Secrets exist.

### Identity broker configuration file

`mecak8s` reads the identity broker's trust domain, audience, and key paths
from the JSON file `--identity-broker-config` names, which
`--enable-identity-broker` requires. The chart renders this file from the
`identityBroker` values and mounts it; without the chart, write it yourself:

```json
{
  "trust_domain": "agents.example.com",
  "audience": "https://mecatl.example.com/identity-broker",
  "keys": {
    "agent_issuer": {
      "key_file": "/var/run/secrets/identity-broker/agent-issuer/key.pem",
      "fallback_key_files": ["/var/run/secrets/identity-broker/agent-issuer-old/public.pem"]
    },
    "broker_client": {
      "key_file": "/var/run/secrets/identity-broker/broker-client/key.pem",
      "key_set_file": "/var/run/secrets/identity-broker/broker-client/jwks.json"
    },
    "as1_signing": {
      "key_file": "/var/run/secrets/identity-broker/as1-signing/signing-key.pem"
    }
  }
}
```

|Field|Chart value|Meaning|
|-|-|-|
|`trust_domain`|`identityBroker.trustDomain`|The agent trust domain: lowercase letters, digits, `.`, `-`, and `_`. Required.|
|`audience`|`identityBroker.audience`|The broker audience every agent credential names. Required.|
|`keys.agent_issuer.key_file`|`identityBroker.keys.agentIssuer`|EC P-256 private key that signs agent credentials. Required.|
|`keys.agent_issuer.fallback_key_files`|`identityBroker.keys.agentIssuer.fallbacks`|_Optional._ Earlier agent issuer keys. Credentials they signed keep verifying while they are listed.|
|`keys.broker_client.key_file`, `key_set_file`|`identityBroker.keys.brokerClient` and its `keySet`|EC P-256 private key the broker authenticates with at AS-1, and at the MCP gateway's authorization server when the file declares a gateway, and the public JWK Set those servers register for it. Every key in the set must be EC P-256. Required together, required with `--identity-broker-as1-issuer`, and required with a gateway entry.|
|`keys.as1_signing.key_file`|`identityBroker.keys.as1Signing`|AS-1's EC P-256 or RSA private key, which signs access tokens. Required with `--identity-broker-as1-issuer`, and refused without it.|
|`keys.as1_signing.fallback_key_files`|`identityBroker.keys.as1Signing.fallbacks`|_Optional._ Earlier AS-1 signing keys. AS-1 publishes them in its key set, so tokens they signed keep verifying, and never signs with them. Requires `key_file`.|

The chart mounts each key, and each fallback, in its own directory under
`/var/run/secrets/identity-broker` and writes those paths into the file. It
writes a fallback list only when the list is non-empty.

Every path is absolute. A private key is a PEM file in PKCS #1
(`RSA PRIVATE KEY`), SEC 1 (`EC PRIVATE KEY`, as `openssl ecparam -genkey`
writes), or PKCS #8 (`PRIVATE KEY`) form; only its first PEM block is read.
`mecak8s` refuses an unknown field, so a misspelled field fails startup rather
than being ignored. The error names the file and the field.

Signing keys rotate through their fallbacks and the broker client key through
its key set; [Rotate keys with the chart](#rotate-keys-with-the-chart) gives
the steps, which apply to the file's fields as well.

`mecak8s` reads the file and the keys only at startup. The chart rolls the pod
when the rendered file changes, such as a new key path. After changing a key
inside a Secret, restart the Deployment yourself, for example for a release
named `mecak8s` in the `mecatl` namespace:

```sh
kubectl rollout restart deployment/mecak8s-mecak8s --namespace mecatl
```

AS-1's signing key is part of this file: `mecak8s` refuses to start with the
removed `--identity-broker-as1-signing-key-file` flag, so set
`keys.as1_signing.key_file` instead.

#### Lifetimes, clock skew, and timeouts

The file can also set how long the broker's sessions and tokens last, how much
clock difference it tolerates, and how long it waits for the authorization
servers it calls in process. Each field is optional and takes a Go duration
string, such as `"30s"` or `"12h"`. With the chart, set them under
`identityBroker.settings`; the chart writes a field only when its value is
set, so an empty value keeps the default:

```yaml
identityBroker:
  settings:
    credentialLifetime: 30m
    clockSkew: 10s
```

Without the chart, add the fields to the file:

```json
"id_jag_max_lifetime": "12h",
"id_jag_lifetime": "60s",
"credential_lifetime": "1h",
"client_assertion_lifetime": "60s",
"clock_skew": "30s",
"as1_request_timeout": "10s",
"as2_request_timeout": "10s"
```

|Field|Chart value|Default|Allowed values|What it bounds|
|-|-|-|-|-|
|`id_jag_max_lifetime`|`identityBroker.settings.idJAGMaxLifetime`|`12h`|`1s` to `720h` in whole seconds, and at least ten times `clock_skew`|Every broker session's hard expiry: AS-1 grants the broker access for each session for this long after the session opens, and does not extend it. It caps how long agents act for a user without a fresh sign-in. A session whose agents keep running past it loses gateway access until the user's next request reopens it. Requires `--identity-broker-as1-issuer`.|
|`id_jag_lifetime`|`identityBroker.settings.idJAGLifetime`|`60s`|`1s` to `300s` in whole seconds|How long each grant AS-1 issues for a gateway call (an ID-JAG) stays valid, and so the longest the gateway token issued for it lasts. It applies to the servers a gateway entry routes. Requires `--identity-broker-as1-issuer`.|
|`credential_lifetime`|`identityBroker.settings.credentialLifetime`|`1h`|More than `0s`, up to `1h`|How long an agent credential, and so a stolen one, stays usable.|
|`client_assertion_lifetime`|`identityBroker.settings.clientAssertionLifetime`|`60s`|`1s` to `60s` in whole seconds|How long each client assertion the broker signs to authenticate at AS-1 and the gateway's authorization server stays valid. Each is accepted once.|
|`clock_skew`|`identityBroker.settings.clockSkew`|`30s`|`0s` to `60s`|How far the broker's clock may differ from other servers'. The broker accepts an agent credential for this long past its expiry, ends each session this long before its hard expiry, and remembers each user access token it converted into a session, refusing its reuse, for this long past the token's expiry. A larger value keeps an expired credential working longer and shortens every session. The maximum is `60s` because AS-1 tells an agent to renew its credential only within 60 seconds of its expiry.|
|`as1_request_timeout`|`identityBroker.settings.as1RequestTimeout`|`10s`|`1s` to `30s`|How long the broker waits for each request to AS-1. Other requests that open the same session wait while a session open makes up to two of them.|
|`as2_request_timeout`|`identityBroker.settings.as2RequestTimeout`|`10s`|`1s` to `1m`|How long a gateway call waits for each request to the gateway's authorization server.|

`mecak8s` refuses to start when a value is not a duration or is outside its
allowed values, when `clock_skew` is more than a tenth of
`id_jag_max_lifetime`, or when the file sets `id_jag_max_lifetime` or
`id_jag_lifetime` without `--identity-broker-as1-issuer`. The chart refuses
to render a value that is not a Go duration, any setting with the broker
off, and `idJAGMaxLifetime` or `idJAGLifetime` without
`identityBroker.as1.issuer`. It leaves the allowed values to `mecak8s`, so a
value out of range renders and the pod then fails to start.

### Declare the embedded gateway

The configuration file can list the MCP gateway the broker authorizes agent
calls through. The list is optional and takes at most one entry, which
describes the embedded gateway `mecak8s` runs for MCP servers with
`auth.mode: oauth`. With the chart, set `identityBroker.gateways`:

```yaml
identityBroker:
  gateways:
    - kind: embedded
      as2Issuer: https://mecatl.example.com/v1/mcp/broker
      tokenEndpoint: https://mecatl.example.com/v1/mcp/broker/oauth/token
      resource: https://mecatl.example.com/v1/mcp/broker/mcp
      servers: []
```

Without the chart, add `gateways` to the file. The fields below use the
file's names; the chart names `as2_issuer` and `token_endpoint` as
`as2Issuer` and `tokenEndpoint`.

```json
"gateways": [{
  "kind": "embedded",
  "as2_issuer": "https://mecatl.example.com/v1/mcp/broker",
  "token_endpoint": "https://mecatl.example.com/v1/mcp/broker/oauth/token",
  "resource": "https://mecatl.example.com/v1/mcp/broker/mcp",
  "servers": []
}]
```

|Field|Meaning|
|-|-|
|`kind`|`embedded`. `mecak8s` refuses `external`: external gateways are not supported yet.|
|`as2_issuer`|The issuer of the gateway's authorization server: the scheme and host of `mcp.broker.callback_url` followed by `/v1/mcp/broker`. It must differ from AS-1's issuer, comparing the path too, because the broker authenticates to both with the same key.|
|`token_endpoint`|That issuer followed by `/oauth/token`, as the authorization server advertises it.|
|`resource`|That issuer followed by `/mcp`.|
|`servers`|MCP server names, matched exactly, each with `auth.mode: oauth`. Leave it empty for now.|

`mecak8s` derives the embedded gateway's issuer, token endpoint, and resource
from `mcp.broker.callback_url` and refuses to start when the entry names
others, when it runs no embedded gateway, or when the file names no broker
client key. An entry also requires the gateway's own signing key, below.
Without an entry, the MCP broker runs as it does with the identity broker off.

The entry also decides the audience of the tokens the MCP gateway issues:
without an entry it is the gateway's issuer, and with one it is the entry's
`resource`. Adding or removing the entry therefore changes the audience, and
users must [enroll](/features/security-and-execution/mcp-oauth-and-credentials.md)
their protected MCP servers again.

An entry that lists servers is refused until the gateway's authorization
server accepts AS-1's tokens, which is not available yet. An entry with no
servers routes no calls through the gateway: it registers the broker as a
client of the gateway's authorization server and keeps that server's signing
key across restarts.

#### Set the gateway's signing key

The embedded gateway's authorization server signs its tokens with the key in
the `mcp.broker.signing_key_file` setting. Earlier keys listed in
`mcp.broker.fallback_key_files` keep verifying the tokens they signed, so the
key can rotate the same way as the broker's keys. Both take absolute paths to
PEM private keys. `mecak8s` requires the key with the identity broker on and a
gateway entry, refuses it otherwise, and refuses a key, or a fallback, that is
also one of the broker's keys. With the chart, name an existing Secret key in
`mcp.broker.signingKey` and its fallbacks in `mcp.broker.signingKey.fallbacks`;
the chart projects them read-only and renders the setting. See the
[configuration reference](/reference/configuration.md) for the setting.

`mecak8s` reads the key at startup, so restart the Deployment after changing it
in a Secret.

### Keep the broker's keys out of sessions

The broker reads its keys from files in the `mecak8s` pod, so it runs only
where sessions cannot read that filesystem. With `identityBroker.enabled`, the
chart refuses a `workspace` and `mecak8s` refuses `--workspace`. Sessions are
then file-less. To give them files, use the
[Redis virtual workspace](state-and-execution.md#redis-virtual-workspace)
(`redis.filesystem.enabled`) or [Kubernetes execution](native-execution.md)
(`execution.enabled`); both keep session files outside the pod's filesystem.

### Collect the broker's audit records

`mecak8s` writes the identity broker's audit records to standard output, one
JSON audit event per line in the toolhive-core audit format, which your log
collector picks up with the pod's other output. Each records who acted and for
whom: session opens and closes, agent registrations, delegations from one
agent to another, ID-JAG issuance, and each gateway call's decision. When
protected MCP servers are served through the
[embedded gateway entry](#declare-the-embedded-gateway), the MCP gateway adds
an event for each request it serves, naming the acting agent. Records carry
credentials only by issuer and token ID, never their values. Keep registration
and delegation events at least as long as any event that refers to them:
later events name them by their `audit_id`.

The broker waits for a record before it acts on some events: it issues no
gateway token until the call's decision is written, and opens no session until
its open is written. When the destination fails or takes longer than 10
seconds to accept a record, the operation fails as unavailable. Closes and
refusals take effect without waiting; the broker sends their records in the
background.

A log shipper reading standard output can drop records under load or when it
restarts. Where no record may be lost, pass
`--identity-broker-audit-file=<ABSOLUTE_PATH>` in `extraArgs` and mount a
volume at that path. The broker syncs each record to the file before it acts,
so a decision is on disk before its call is sent. Point the path at storage a
collector ships from and `mecak8s` cannot rewrite.

### Rotate keys with the chart

`mecak8s` reads every key only at startup, so each rotation step ends with a
restart. A `helm upgrade` that changes a key's Secret or key name rolls the pod
by itself; after changing a key inside an existing Secret, run
`kubectl rollout restart deployment/<RELEASE>-mecak8s`.

AS-1, the embedded gateway's authorization server (AS-2), and the broker run
in one `mecak8s` process with one replica and the `Recreate` strategy, so
every restart is a brief outage. Each restart drops the in-memory broker
sessions, agent registrations, and AS-1's sign-ins and refresh tokens, and
users sign in again, as [Sign-in and restarts](#sign-in-and-restarts)
describes. Plan rotations for a maintenance window.

#### Rotate the broker client key

AS-1 and AS-2 accept the broker client's assertions signed by any key in
`identityBroker.keys.brokerClient.keySet`, so the key set holds both keys
while the broker switches:

1. Generate the next key and its key set, and merge the old and new key sets:

   ```sh
   openssl ecparam -name prime256v1 -genkey -noout -out broker-client-next.pem
   deploy/mecak8s-idbroker/broker-client-key-set.sh < broker-client-next.pem > broker-client-next-jwks.json
   jq -s '{keys: (.[0].keys + .[1].keys)}' broker-client-jwks.json broker-client-next-jwks.json > broker-client-both-jwks.json
   ```

2. Put the merged key set in the Secret key `keySet` names, and restart.
3. Put the next private key in the Secret key `brokerClient` names, or point
   `brokerClient.key` at a new Secret key holding it, and restart.
4. Put the next key's key set alone in the `keySet` Secret key, and restart.

Because every server that checks the broker client's key restarts with the
broker, the overlap prevents no refused request in this layout. It matters
for a server outside the process, such as an external gateway, which reads
the key set on its own schedule.

#### Rotate a signing key

The agent issuer, AS-1, and AS-2 signing keys rotate the same way: make the
new key current with the old one as a fallback, restart, and remove the
fallback once everything the old key signed has expired.

|Key|Chart values|Keep the fallback for|
|-|-|-|
|Agent issuer|`identityBroker.keys.agentIssuer` and its `fallbacks`|`credential_lifetime` plus `clock_skew`: one hour and 30 seconds by default.|
|AS-1|`identityBroker.keys.as1Signing` and its `fallbacks`|AS-1's access token lifetime: one hour.|
|AS-2|`mcp.broker.signingKey` and its `fallbacks`|AS-2's token lifetime: one hour.|

For example, to rotate AS-1's key, add the new key to the `mecak8s-as1`
Secret as `signing-key-next.pem` and run `helm upgrade` with:

```yaml
identityBroker:
  keys:
    as1Signing:
      secretName: mecak8s-as1
      key: signing-key-next.pem
      fallbacks:
        - secretName: mecak8s-as1
          key: signing-key.pem
```

An hour after the pod restarts, set `fallbacks: []`, run `helm upgrade`
again, and delete the old key from the Secret.

AS-1's and AS-2's fallbacks must be private keys, because their servers load
every key file as a signing key and publish its public key. The broker only
verifies with an agent issuer fallback, so name its public key, in PKIX
(`PUBLIC KEY`) form, and the retired private key leaves the pod:

```sh
openssl ec -in agent-issuer.pem -pubout -out agent-issuer-old-public.pem
```

The restart that makes a new agent issuer key current also drops every agent
registration, and the broker refuses a credential issued before it whichever
key signed it. The agent issuer fallback therefore has no effect while the
broker runs in the `mecak8s` process, but the same procedure applies.

Fallbacks serve a planned rotation only. Remove a compromised key at once
instead of keeping it as a fallback.

### Keep keys in a secret manager

The chart mounts the broker's keys from Kubernetes Secrets. To hold them in
a secret manager such as HashiCorp Vault or AWS Secrets Manager, sync them
into the Secrets the `identityBroker.keys` and `mcp.broker.signingKey` values
name, for example with the External Secrets Operator or the Secrets Store CSI
Driver's Secret sync. The broker reads its keys only at startup, so restart
the Deployment after a sync changes a key. `mecak8s` cannot read keys from a
secret manager or use a signing service directly.

`mecak8s` refuses to start when the broker client's key set does not hold the
broker client key's public key. If the two sync at different times, a
restart between the syncs, such as one caused by a node drain, leaves the pod
failing to start until both arrive. Keep the broker client key and its key
set in one Secret that syncs both together, pointing
`identityBroker.keys.brokerClient` and `identityBroker.keys.brokerClient.keySet`
at it. During a rotation, sync the key set that holds both public keys before
the new private key.

### Choose the issuer

AS-1 serves its sign-in endpoints and discovery documents under the issuer
URL's path on the `mecak8s` HTTP listener. The issuer can be a dedicated
hostname, such as `https://signin.example.com`, or a path on the `mecak8s`
host, such as `https://mecatl.example.com/as1`. Route that hostname or path
prefix to the HTTP listener so browsers reach it.

AS-1's sign-in endpoints are anonymous. Behind an ingress, `mecak8s` cannot
tell users apart, so its rate limits are global: 50 requests per second, with
bursts of 200, for the browser sign-in endpoints, and a separate budget of the
same size for `<ISSUER>/oauth/token`, so a sign-in flood does not block token
refresh. One client can still use up the budget for everyone, so configure a
per-client rate limit for `<ISSUER>/oauth/*` at your ingress.

AS-1 also holds at most 10,000 sign-ins that have not been approved yet. Past
that, new sign-ins fail with a `server_error` until pending ones complete or
expire, within 15 minutes.

Keep the issuer once users have signed in. It is in every token, in each user's
saved `mecatui` login, and in the redirect URI registered at the company IdP.

### Register AS-1 at the company IdP

Ask the IdP administrator to register one web application for AS-1 with the
redirect URI `<ISSUER>/oauth/callback`. This registration replaces the separate
`mecatui` and Studio registrations: every client signs in through AS-1, so
per-application IdP policies can no longer tell those clients apart. AS-1
requests the `offline_access` scope with `prompt=consent`.

Set `subjectClaim` to a claim that is the same for a user across applications;
it has no default. For Microsoft Entra ID, use `oid`, because Entra ID issues a
different `sub` to each application. Use `sub` for an IdP whose `sub` does not
depend on the application. The claim names the user to MCP gateways; session
ownership still keys on the access token's `sub_id`.

### Sign-in and restarts

Users approve a consent page at AS-1 when they sign in. AS-1 keeps its users,
pending sign-ins, and refresh tokens in memory, so every pod restart or rollout
signs all users out of AS-1 and they run `mecatui login` again. The broker's
sessions live in the same memory, so a restart drops them too, and each
signed-in user's next request opens a new broker session. AS-1's signing key
comes from a Secret, so the access tokens AS-1 issued before a restart keep
verifying until they expire. AS-1 accepts each access token for one session
open, but it forgets which tokens it has accepted when the pod restarts. A token
used before a restart can open one more session after it, until the token
expires. Without a gateway entry, the MCP gateway's embedded authorization
server generates its own signing key at each start, so the tokens it issued
stop verifying after a restart; with one, it signs with
[the key you supply](#set-the-gateways-signing-key).

Turning on `oidc.ownerIdentity: sub_id` for a deployment with existing sessions leaves
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
