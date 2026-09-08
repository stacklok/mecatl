# ADR 0305 — OAuth protected-resource discovery for remote mecatui

- Status: Accepted
- Date: 2026-09-02
- Scope: RFC 9728 metadata served by mecated/mecak8s and remote mecatui enrollment
- Supersedes: The explicit-configuration portion of ADR 0277 only
- Superseded by: ADR 0316 (scope-selection clauses only)

## Context

Remote `mecatui login ADDRESS` currently requires the operator to provide an
OIDC issuer, public client ID, and access-token audience even though the remote
server already knows its configured issuer and audience. The current target
model is a gRPC `host:port`, while OAuth protected-resource discovery needs a
canonical externally reachable HTTPS resource URL. These are often the same
authority behind an ingress, but may be different when gRPC and HTTP use
separate listeners or an ingress translates between them.

RFC 9728 standardizes protected-resource metadata, including the resource URL
and authorization-server issuer, but does not standardize an OAuth client ID or
the resource server's configured audience. ToolHive v0.40.0 contains the
closest server and client implementations (`pkg/auth/token.go` <!-- lint:not-a-citation: path inside the pinned ToolHive dependency, not repo file -->,
`pkg/auth/well_known.go` <!-- lint:not-a-citation: path inside the pinned ToolHive dependency, not repo file -->, `pkg/auth/discovery/discovery.go` <!-- lint:not-a-citation: path inside the pinned ToolHive dependency, not repo file -->, and
`pkg/oauthproto/constants.go` <!-- lint:not-a-citation: path inside the pinned ToolHive dependency, not repo file -->). ToolHive-Core v0.0.41 contains reusable
networking and robust challenge parsing primitives. Their behavior is useful,
but the fixed metadata type cannot carry mecatl extensions and several helpers
need stricter routing, validation, and disclosure behavior for this boundary.

## Decision

Add an optional protected-resource profile to the existing shared OIDC
configuration. Existing `--oidc-issuer` and `--oidc-audience` remain the sole
authoritative values for token validation and are projected into metadata as
`authorization_servers[0]` and `com.stacklok.mecatl.audience` respectively.

Add only these new server settings:

- `--oidc-resource HTTPS_URL` — the operator-controlled canonical external
  protected-resource URL;
- `--oidc-client-id PUBLIC_ID` — a public mecatui client-registration hint;
- `--oidc-scopes SCOPE[,SCOPE...]` — optional CSV API scopes to advertise.

The profile is disabled when the new fields are absent. Supplying one of the
required profile values without the others, or without OIDC, is a startup
configuration error. An absent scope list is valid and omits `scopes_supported`.
The configured scope list is a narrow operator allowlist for this public-client
profile, not server authorization policy. `mecatui` requests exactly the confirmed
configured set (or an explicitly selected subset); it never adds baseline scopes
or expands a saved enrollment from later metadata.

When enabled, mecated and mecak8s serve `GET
/.well-known/oauth-protected-resource` on the public HTTP API listener outside
bearer authentication. Path-bearing resources use RFC 9728 path insertion.
The response contains standard `resource`, `authorization_servers`, and
`bearer_methods_supported: ["header"]` metadata plus:

```json
{
  "com.stacklok.mecatl.audience": "api://mecak8s",
  "com.stacklok.mecatl.client_id": "0oa..."
}
```

These are explicitly mecatl application-profile fields, not standard RFC 9728
fields. The client ID is public and never a secret. V1 requires exactly one
authorization server; multi-issuer metadata is rejected rather than selected
by array order.

Unauthenticated HTTP API bearer challenges remain generic `Bearer`. The configured
resource is a service-wide base identity, while API requests are subordinate paths
and the request `Host` is untrusted, so the middleware cannot prove that a 401
request is for the exact RFC 9728 resource. The configured metadata URL is served
only at its direct well-known endpoint; it is never inferred from request headers
or paths.

Remote mecatui accepts either a bare DNS hostname or an explicit HTTPS resource
URL for discovery. A bare hostname means HTTPS and port 443. The derived gRPC
target is the resource authority, unless `--grpc-target` supplies an explicit
transport target. Existing `host:port` direct-gRPC enrollment remains
compatible. Resource URL and gRPC target are stored separately in public
connection metadata; the existing encrypted credential key remains stable.

Discovery is anonymous, HTTPS-only, redirect-free, bounded, and protected by
resolved-address checks. Both protected-resource metadata and the subsequent
issuer OIDC-discovery request use the same public-bootstrap transport before
confirmation: neither may use private-address admission, custom issuer CA
roots, bearer/cookie/client-certificate credentials, or environment switches
that disable validation. The returned `resource` must exactly match the
canonical requested resource, and the selected issuer must exactly match its
OIDC discovery document. First-use discovery produces one immutable validated
tuple, displays it, and requires confirmation before browser launch or
persistence. The exact confirmed tuple is passed unchanged to authorization
and enrollment; saved `connect` never launches a browser or silently adopts
changed metadata. Discovery, issuer lookup, timeout, mismatch, cancellation,
and confirmation failures leave no keyring, credential, or registry state.

The implementation must reuse or minimally adapt the pinned ToolHive/ToolHive-
Core Apache-2.0 code with attribution. Dynamic client registration,
private-resource discovery, and opaque-token support remain outside this profile.

## Consequences

Users can enroll with a resource hostname rather than copying three OAuth
values. Deployments must publish one canonical external resource URL and a
public mecatui client ID; the server cannot safely infer the former from a bind
address or request host, and the latter is not part of standard RFC 9728.

The existing issuer and audience policy has one source of truth. The logical
API audience can remain stable as `api://mecak8s` while the deployment-specific
RFC 9728 resource is an HTTPS URL. Resource and gRPC transport identities remain
honest for ingress and split-listener deployments.

The public client hint is convenient but is bootstrap data, not a trust anchor.
First-use confirmation and exact issuer/resource checks reduce silent
client/issuer substitution. Public-resource discovery does not inherit private
issuer CA or address exceptions; private deployments retain explicit login.

A new optional registry field is additive and does not invalidate existing
credential keys. Exact lookup must reject ambiguous resource/target matches.
The mecatl extensions are not interoperable with generic RFC 9728 clients. The
confirmed resource URL is bootstrap and registry metadata, not a token-request
compatibility promise.

## See also

- [ADR 0277 — Remote mecatui OIDC](0277-remote-mecatui-oidc.md)
- [ADR 0287 — Target-aware mecatui TLS](0287-target-aware-mecatui-tls.md)
- [ADR 0204 — Caller identity threading](0204-caller-identity-threading.md)
- [Architecture guide](../architecture.md)
- [OAuth protected-resource acceptance plan](../acceptance/oauth-protected-resource-discovery.md)
- ToolHive v0.40.0 `pkg/auth/token.go`, `pkg/auth/well_known.go`, `pkg/auth/discovery/discovery.go` <!-- lint:not-a-citation: paths inside the pinned ToolHive dependency, not repo files -->
- ToolHive-Core v0.0.41 `networking/http_client.go`, `networking/fetch.go` <!-- lint:not-a-citation: paths inside the pinned ToolHive-Core dependency, not repo files -->

---
