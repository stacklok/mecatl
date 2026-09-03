---
sidebar_position: 6
title: Caller identity and OIDC
description: Configure authenticated caller identity and ownership boundaries for mecatl sessions.
---

# Caller identity and OIDC

Mecatl can require an OIDC bearer token on every gRPC and HTTP request and use
the verified identity to isolate application resources. This is more than
request authentication: sessions, schedules, teams, memory entries, and
persisted child sessions are owned by the caller that created them.

## Availability

Caller identity is an opt-in server feature for `mecated` and `mecak8s`. It
protects both wire surfaces with the same validator and ownership rules. `mecatui`
can either send an operator-supplied static bearer with `--auth-token`, or enroll a
remote target with `mecatui login` and obtain, validate, and refresh its own OIDC
credential. With no explicit credential, an existing saved enrollment is preserved; any
clean enrollment miss is attempted credential-free and the server decides whether caller
authentication is required. Use connect-only `--anonymous` to intentionally bypass saved
credentials when no static token is selected. A token supplied through `--auth-token` or
`MECATL_AUTH_TOKEN` wins if both are present; `--anonymous` has no environment equivalent. Remote
targets use verified TLS automatically; a static bearer is never
sent over explicit non-loopback plaintext, and saved OIDC authentication always
requires verified TLS. Those are distinct client modes; the server still only validates the
bearer presented on each request.

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

`issuer` and `subject` are the identity key. `name` is only a cosmetic snapshot
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

Ownership is not the same as event actor attribution. A scheduled system action
can act on a resource while the resource remains owned by its user. The actor
stamp is durable event-log metadata, not a client-visible authorization field.

## Configure OIDC

The essential server settings are:

| Flag | Purpose |
| --- | --- |
| `--oidc-issuer` | HTTPS issuer URL, compared byte-for-byte with the token's `iss`; setting it enables caller identity. |
| `--oidc-audience` | Required accepted `aud` value; prevents accepting tokens minted for another service. |
| `--oidc-jwks-uri` | Optional pinned JWKS endpoint; otherwise discovery obtains it from the issuer. |
| `--oidc-max-jwks-staleness` | Maximum age of a last-good signing-key cache during an IdP outage. `0` deliberately removes the bound. |
| `--auth-token` | Separate static bearer authentication option when OIDC identity is not needed. |

A typical configuration uses the issuer and audience together:

```console
mecated serve \
  --oidc-issuer https://idp.example.com/realms/operators \
  --oidc-audience mecatl \
  --workspace /srv/mecatl/workspace
```

`--oidc-jwks-uri` is useful for air-gapped or pinned-key deployments. Use a
real HTTPS IdP in production. The validator rejects an HTTP issuer and refuses
JWKS endpoints resolving to private, loopback, link-local, or metadata
addresses. Initial configuration or key-fetch failure fails closed rather than
starting an unauthenticated service.

The production validator is a delegated, maintained OIDC/JWT library; mecatl
does not hand-roll signature verification. A successful JWKS fetch is cached in
process. During a short IdP outage, the last-good keys may continue to work
until the staleness limit; after that the service returns `503` until it can
refresh. A malformed, expired, wrong-issuer, or wrong-audience token returns
`401`. The cache is not persisted, so a restarted process fetches keys again.

## Use an authenticated client

For a static bearer, obtain a token through your identity provider and pass it to
mecatui or another client. Keep it out of shell history where possible. This mode does
not refresh the token:

```console
export MECATL_AUTH_TOKEN="$(your-oidc-cli print-access-token)"
bin/mecatui connect 127.0.0.1:8080 --auth-token "$MECATL_AUTH_TOKEN" \
  --workspace /srv/mecatl/workspace
```

For managed remote OIDC, enroll once and then connect without putting a bearer on the
command line:

```console
bin/mecatui login mecated.example.internal:443 \
  --issuer https://idp.example.internal \
  --client-id mecatui --audience mecatl \
  --tls-ca /path/to/issuer-ca.pem --private-issuer
bin/mecatui connect mecated.example.internal:443 \
  --tls --tls-ca /path/to/server-ca.pem
```

The issuer CA bundle path/reference—not the CA contents—verifies issuer endpoints and is
saved with the enrollment; the optional
connect CA independently verifies the gRPC server. Managed credentials live in a
keyring-wrapped encrypted store and refresh on later token demand. `connect` never opens
a browser implicitly.

For non-loopback connections, mecatui refuses to send a bearer over cleartext. Use TLS
(and `connect --tls-ca` for a private server CA). A gRPC dial can succeed before the first request
is authenticated; verify that an unauthenticated first request fails before
producing a model response.

### Credential-free private-network connection

`--anonymous` means “bypass saved OIDC enrollment and send no bearer”; it is not a login
mode and creates no saved state. A clean enrollment miss already gets a credential-free
attempt. For remote use, verified TLS remains the default. If a Tailscale deployment
deliberately uses the tailnet as shared authority and transport, explicitly select plaintext:

```console
mecatui connect ozzllama:9080 --tls=false
```

Add `--anonymous` only to override an existing saved enrollment.

Bind `mecated` to one concrete Tailscale address rather than a wildcard, do not enable
Funnel, and make tailnet ACLs the load-bearing reachability boundary. Use a dedicated
server workspace, run with the least OS authority that can access it, and configure a
restrictive rate limit. The server's non-loopback no-caller-authentication warning is
expected: ordinary TLS does not authenticate callers. Never infer this posture from a
private IP, hostname, or Tailscale-like name. A non-loopback client's current directory
is not the workspace; the server selects and governs its workspace.

For a non-loopback target, the server's listener policy selects the workspace or no-FS
profile: mecatui sends no local cwd and rejects `--workspace`. A loopback connection may
still select a server-host path. In Kubernetes, isolate tenant workspaces separately:
ownership does not make a shared pod filesystem a security boundary.

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

This distinction is intentional:

- **authentication** answers “who made this request?”;
- **ownership** answers “whose resource is this?”;
- **management authority** answers “may this operator perform storage-wide
  maintenance?”; and
- **session leases** answer “does this process exclusively own the mutation?”

## Deployment limitations

- OIDC configuration is supported on the server roots, not as an in-process
  token issuer. An embedding must provide its own authenticated boundary and
  owner propagation if it needs multi-user isolation.
- The static-bearer/unmanaged mecatui path obtains no token and performs no refresh.
  Managed remote OIDC instead uses `mecatui login` and refreshes the encrypted,
  target-bound credential on later application token demand.
- `mecatequi` and scheduled/headless jobs do not open a browser. Pre-provision a
  short-lived identity or use the deployment's non-interactive credential path.
- Raw remote store and memory drivers are trusted infrastructure; caller
  ownership is enforced at the mecatl service edge, not by an unauthenticated
  driver endpoint.
- A workspace path is not an ownership boundary. Use separate namespaces,
  containers, or operating-system permissions when tenants must not share files.
- Turning identity on is an isolation cutover. Export or migrate resources from
  an ownerless deployment before enabling it; there is no automatic adoption.
- Bearer authentication and OIDC caller identity are different modes. A static
  `--auth-token` authenticates possession of one shared secret but cannot provide
  per-caller ownership.

For the full Kubernetes overlay, JWKS cache behavior, and troubleshooting steps,
see [Caller identity and ownership isolation](/building/deployment/mecak8s.md#multi-user-caller-identity-and-ownership-isolation-opt-in).

## Next steps

- [MCP OAuth and credentials](./mcp-oauth-and-credentials.md)
- [Session continuity](./session-continuity.md)
- [Drive via gRPC / HTTP](/building/deployment/grpc-http.md)
- [Capability and deployment matrix](./capability-matrix.md)
