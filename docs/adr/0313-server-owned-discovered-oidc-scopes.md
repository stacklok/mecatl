# ADR 0313 — Server-owned scopes for discovered mecatui login

- Status: Proposed
- Date: 2026-09-08
- Scope: protected-resource scope selection for `mecatui login ADDRESS`
- Supersedes: ADR 0305's scope-selection clauses only
- Superseded by: None

## Context

ADR 0305 made `oidc.scopes` an optional server profile field and correctly
kept it separate from server-side authorization policy. Its acceptance record,
however, stated that a protected-resource response omitting `scopes_supported`
uses the existing OIDC baseline. The ADR instead said that discovered login
never adds baseline scopes. The implementation followed the latter wording:
an omitted member produces an empty scope slice, which then fails the strict
first-use confirmation display validation.

Scope selection is also an authorization-consent decision. Letting a user
supply an arbitrary `--scopes` list during discovery would let the client
broaden or change the scope request independently of the operator's public
client profile. That is undesirable even though the authorization server
remains the final authority. It also makes an otherwise simple
`mecatui login ADDRESS` flow require users to understand deployment-specific
scope names.

The pre-discovery explicit OIDC login route has no protected-resource profile.
Its existing `--scopes` override remains necessary and is not changed here.

## Decision

For protected-resource discovery only, make the remote server authoritative
for the requested scope set:

1. When metadata contains `scopes_supported`, request exactly its validated
   configured list. The list remains the operator's narrow public-client
   request allowlist.
2. When metadata omits `scopes_supported`, request exactly the fixed
   compatibility baseline `openid,profile,offline_access`. Omission does not
   mean an empty request or permission for arbitrary caller-selected scopes.
3. Reject `--scopes` on `mecatui login ADDRESS` before discovery or browser
   authorization. Administrators select non-baseline discovered-login scopes
   by configuring `oidc.scopes` on the server.
4. Preserve `--scopes` for legacy explicit identity login, which supplies
   `--issuer`, `--client-id`, and `--audience` and has no discovered profile.
5. Keep strict terminal-safe confirmation display validation. The resolved
   discovery scope set is non-empty, so no empty-value exception is added.

This supersedes only ADR 0305's conflicting scope-selection statements. ADR
0305 remains authoritative for the protected-resource profile, anonymous
bootstrap, resource and issuer binding, confirmation, and transport
separation.

## Consequences

A user can enroll through discovery without supplying scope configuration:
`mecatui login server.example` follows the server's advertised list or a
stable compatibility baseline. Deployments needing scopes other than the
baseline must set `oidc.scopes`; they cannot rely on a user command-line
override.

Existing deployments which omit `scopes_supported` become enrollable using the
baseline. A caller using `--scopes` in discovery mode receives a deliberate
rejection instead of an apparent but insecure or ineffective override. The
baseline contains `offline_access`; an issuer may refuse it or decline to
return a refresh token, in which case initial login can still work but a later
access-token expiry requires another login.

The metadata parser must preserve the distinction between an absent member and
a present malformed or empty list. Treating every empty decoded list as
omission could turn invalid metadata into a baseline authorization request.

## See also

- [ADR 0305 — OAuth protected-resource discovery](./0305-oauth-protected-resource-discovery.md)
- [ADR 0277 — Remote mecatui OIDC client authentication](./0277-remote-mecatui-oidc.md)
- [Mecatui server-owned discovery scopes acceptance plan](../acceptance/mecatui-server-owned-discovery-scopes.md)
- [Architecture guide](../architecture.md)
- `user-docs/mecatui/remote-servers.md` — public scope and refresh guidance

