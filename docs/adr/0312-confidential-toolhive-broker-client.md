# ADR 0312 — Confidential ToolHive broker client credentials

- Status: Accepted
- Date: 2026-09-03
- Scope: the generated OAuth client between mecatl's broker runtime and ToolHive's embedded authorization server
- Supersedes: None
- Superseded by: None

## Context

A protected workspace enrollment causes mecatl to construct a ToolHive process
with an embedded authorization server. Mecatl's server-side callback exchanges
an authorization code there and later refreshes the resulting broker grant.
The generated client had been registered as public, even though it is a
server-confined process identity that can authenticate at the token endpoint.

The browser must never receive a client credential. The same is true of public
enrollment controls, session snapshots, event logs, operator configuration,
and ToolHive upstream requests. ToolHive's Fosite storage expects a hash for a
confidential client credential; storing a raw credential would turn durable
storage into a secret source.

This is unreleased work. No compatibility reader, key migration, or deployment
migration is required for a previously shipped public client registration.

## Decision

Each protected ToolHive process generates one high-entropy broker-client secret
at construction. Its private OAuth target retains that value only in process
memory. ToolHive's client-registration helper receives it to create a
confidential client whose registered token-endpoint method is
`client_secret_basic` and whose persisted value is ToolHive's required hash.

The OAuth route/grant supplies the raw secret only to `oauth2.Config` with
`AuthStyleInHeader`. The authorization-code exchange and refresh therefore
send it in the HTTP Basic `Authorization` header to ToolHive's token endpoint.
They do not put `client_secret` in the form body. The raw value is neither
serialized nor projected through a model, client, server control, deployment
configuration, diagnostic, or upstream call. A broker bearer token remains the
only credential used against ToolHive's vMCP endpoint.

## Consequences

- The embedded authorization server rejects a token exchange or refresh that
  lacks the generated Basic credential.
- The raw secret remains in process memory for the lifetime needed to refresh
  a live grant. Process restart intentionally loses this process-local
  capability and follows the broker's existing deterministic-settlement
  posture.
- Tests must prove both the registration shape/hash and the real embedded
  authorization-code and refresh flows, including absence from the form body
  and public surfaces.
- Replacing an earlier preview deployment may clear its disposable ToolHive
  auth namespace; that operational cleanup is not a product migration.

## See also

- [ADR 0311 — Per-upstream MCP broker OAuth grants](./0311-per-upstream-mcp-broker-oauth-grants.md)
- [ADR 0220 — Adapter-local MCP OAuth controller](./0220-mcp-oauth-controller.md)
- [Multi-upstream OAuth acceptance plan](../acceptance/mcp-broker-multi-upstream-oauth.md)

