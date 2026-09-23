---
sidebar_position: 310
title: Caller identity and OIDC
description:
  Configure authenticated caller identity and ownership boundaries for Mecatl
  sessions.
---

# Caller identity and OIDC

Require OIDC bearer authentication for gRPC and HTTP requests to isolate each
caller's sessions, schedules, teams, memory, and persisted child sessions.

## Availability

Caller identity is an opt-in server feature for `mecated` and `mecak8s`. It
protects gRPC and HTTP with the same validator and ownership rules. `mecatui`
can send an operator-supplied static bearer with `--auth-token` or enroll a
remote target with `mecatui login` and manage its own OIDC credential.

Without an explicit credential, `mecatui` uses a saved enrollment or attempts a
credential-free connection. Use `--anonymous` to bypass a saved enrollment.
`--auth-token` and `MECATL_AUTH_TOKEN` take precedence over `--anonymous`.

Remote targets use verified TLS automatically. `mecatui` refuses to send a
static bearer over explicit non-loopback plaintext, and saved OIDC
authentication always requires verified TLS. The server validates the bearer
presented on each request in either mode.

With OIDC disabled, the server preserves the single-shared-deployment behavior:
there is no caller subject and anyone who can reach the API is treated as the
same caller. Do not expose that posture as a multi-user endpoint.

## What identity means

The durable owner is the verified `(issuer, subject)` pair from the token:

```json
{
  "owner": {
    "issuer": "https://idp.example.com",
    "subject": "opaque-user-id",
    "grant_type": "user",
    "name": "Alice"
  }
}
```

`issuer` and `subject` are the identity key. `name` is a display-only snapshot
of the token's name claim and can change later. The owner is written at resource
creation from the verified request context, never from a request-body field.

With ownership enforcement enabled:

- callers list only their own sessions, schedules, teams, and fires;
- a caller cannot prompt, resume, cancel, fork, or delete another caller's
  session or persisted child;
- live event streams and durable event-log reads enforce the same owner check;
- identical logical keys, such as a memory key or schedule name, can exist for
  different owners without collision; and
- foreign or ownerless resources look like `not found`, preventing resource
  enumeration.

Subagents, Parallel branches, Team members, and scheduled fires inherit the
appropriate owner. A fork inherits the source session's owner and still checks
that the caller owns the source. Resources created before identity was enabled
are ownerless and become unavailable once enforcement is turned on; there is no
automatic first-reader adoption.

Ownership and event attribution are separate. A scheduled action can act on a
user-owned resource; the event log records that action's actor.

## Configure OIDC

The essential server settings are:

|Flag|Purpose|
|-|-|
|`--oidc-issuer`|HTTPS issuer URL, compared byte-for-byte with the token's `iss`; setting it enables caller identity.|
|`--oidc-audience`|Required accepted `aud` value; prevents accepting tokens minted for another service.|
|`--oidc-jwks-uri`|Optional pinned JWKS endpoint; otherwise discovery obtains it from the issuer.|
|`--oidc-max-jwks-staleness`|Maximum age of a last-good signing-key cache during an IdP outage. `0` deliberately removes the bound.|
|`--oidc-resource`|Optional canonical external HTTPS protected-resource URL (RFC 9728).|
|`--oidc-client-id`|Optional public `mecatui` client-registration hint; a Mecatl extension, not an RFC 9728 field.|
|`--oidc-scopes`|Optional CSV scope list advertised as `scopes_supported`. It is a narrow operator-configured public-client request allowlist, not server authorization policy. Discovered login requests an advertised list exactly; when omitted, it requests the fixed `openid,profile,offline_access` baseline.|

A typical configuration uses the issuer and audience together:

```sh
mecated serve \
  --oidc-issuer https://idp.example.com/realms/operators \
  --oidc-audience mecatl \
  --workspace /srv/mecatl/workspace
```

Use `--oidc-jwks-uri` for air-gapped or pinned-key deployments. The validator
requires an HTTPS issuer and rejects JWKS endpoints that resolve to private,
loopback, link-local, or metadata addresses. Configuration or initial key-fetch
failure prevents the service from starting.

The validator caches a successful JWKS fetch in process. During a short IdP
outage, the last-good keys may continue to work until the staleness limit; after
that the service returns `503` until it can refresh. A malformed, expired,
wrong-issuer, or wrong-audience token returns `401`. The cache is not persisted,
so a restarted process fetches keys again.

## Protected-resource discovery (RFC 9728)

A deployment can publish an HTTPS resource profile with `--oidc-resource`,
`--oidc-client-id`, and optional `--oidc-scopes`. The issuer and audience remain
the validator's source of truth. `scopes_supported` controls what the public
client requests; it does not grant server authorization. When omitted, login
requests `openid`, `profile`, and `offline_access`.

Mecatl publishes the audience and client ID as `com.stacklok.mecatl.audience`
and `com.stacklok.mecatl.client_id` extension members.

Discovery rejects a `--scopes` override. Configure advertised scopes through
`oidc.scopes`; explicit `mecatui login --issuer ...` still accepts `--scopes`.

With a published profile, `mecatui login ADDRESS` discovers and confirms the
public configuration. Use explicit `mecatui login --issuer ...` for deployments
without a profile or with a private issuer. Discovery uses anonymous HTTPS and
does not inherit private-issuer CA exceptions from authenticated gRPC.

For a static bearer, obtain a token through your identity provider and pass it
to `mecatui` or another client. Keep it out of shell history where possible.
This mode does not refresh the token:

```sh
export MECATL_AUTH_TOKEN="$(your-oidc-cli print-access-token)"
mecatui connect 127.0.0.1:8080 --auth-token "$MECATL_AUTH_TOKEN"
```

For managed remote OIDC, enroll once and then connect without putting a bearer
on the command line:

```sh
mecatui login mecated.example.internal:443 \
  --issuer https://idp.example.internal \
  --client-id mecatui --audience mecatl \
  --tls-ca /path/to/issuer-ca.pem --private-issuer
mecatui connect mecated.example.internal:443 \
  --tls --tls-ca /path/to/server-ca.pem
```

The enrollment saves the issuer CA path, not its contents. A separate connect CA
verifies the gRPC server. Managed credentials use a keyring-wrapped encrypted
store and refresh when needed. `connect` never opens a browser.

For non-loopback connections, `mecatui` refuses to send a bearer over cleartext.
Use TLS (and `connect --tls-ca` for a private server CA). A gRPC dial can
succeed before the first request is authenticated; verify that an
unauthenticated first request fails before producing a model response.

### Credential-free private-network connection

`--anonymous` means "bypass saved OIDC enrollment and send no bearer." It is a
connection option and creates no saved state. A clean enrollment miss already
gets a credential-free attempt. For remote use, verified TLS remains the
default. If a Tailscale deployment deliberately uses the tailnet as shared
authority and transport, explicitly select plaintext:

```sh
mecatui connect ozzllama:9080 --tls=false
```

Add `--anonymous` only when you need to override a saved enrollment.

Bind `mecated` to one concrete Tailscale address rather than a wildcard, do not
enable Funnel, and make tailnet ACLs the load-bearing reachability boundary. Use
a dedicated server workspace, run with the least OS authority that can access
it, and configure a restrictive rate limit. The server's non-loopback
no-caller-authentication warning is expected: ordinary TLS does not authenticate
callers. Never infer this posture from a private IP, hostname, or Tailscale-like
name. A non-loopback client's current directory is not the workspace; the server
selects and governs its workspace.

For a non-loopback target, the server's listener policy selects the workspace or
no-FS profile: `mecatui` sends no local cwd and rejects `--workspace`. A
loopback connection may still select a server-host path. In Kubernetes, isolate
tenant workspaces separately: ownership does not make a shared pod filesystem a
security boundary.

## Management is separate from authentication

A valid OIDC token does not automatically grant storage-management authority.
Remote cleanup, migration, and retention management require an explicit
operator-tier allowlist of exact issuer/subject pairs:

```yaml
storage_management:
  version: 1
  principals:
    - issuer: https://idp.example.com/realms/operators
      subject: storage-admin
```

An absent or empty list fails closed. Project settings, request owner claims,
display names, grant types, and system-principal status cannot grant this
authority. Destructive management also requires a working cross-process session
lease; management permission alone is not a single-writer proof.

These controls answer different questions:

- **Authentication:** Who made this request?
- **Ownership:** Whose resource is this?
- **Management authority:** May this operator perform storage-wide maintenance?
- **Session leases:** Does this process exclusively own the mutation?

## Deployment limitations

- OIDC configuration is supported on the server roots, not as an in-process
  token issuer. An embedding must provide its own authenticated boundary and
  owner propagation if it needs multi-user isolation.
- A static bearer does not refresh. Managed OIDC uses `mecatui login` and
  refreshes the encrypted, target-bound credential when needed.
- `mecatequi` and scheduled/headless jobs do not open a browser. Pre-provision a
  short-lived identity or use the deployment's non-interactive credential path.
- Raw remote store and memory drivers are trusted infrastructure; caller
  ownership is enforced at the Mecatl service edge, not by an unauthenticated
  driver endpoint.
- A workspace path is not an ownership boundary. Use separate namespaces,
  containers, or operating-system permissions when tenants must not share files.
- Enabling identity makes ownerless resources unavailable. Export or migrate
  them first; Mecatl does not adopt them automatically.
- Bearer authentication and OIDC caller identity are different modes. A static
  `--auth-token` authenticates possession of one shared secret but cannot
  provide per-caller ownership.

For the full Kubernetes overlay, JWKS cache behavior, and troubleshooting steps,
see
[Configure caller identity](/operating/mecak8s.md#configure-caller-identity).

## Next steps

- [MCP OAuth and credentials](./mcp-oauth-and-credentials.md)
- [Session continuity](./session-continuity.md)
- [Drive via gRPC / HTTP](/operating/grpc-http.md)
