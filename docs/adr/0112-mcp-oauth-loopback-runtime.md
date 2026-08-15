# ADR 0112 — Host-owned loopback MCP OAuth login

- Status: Accepted
- Date: 2026-08-14
- Scope: MCP OAuth authorization presentation and one-shot composition
- Supersedes: —
- Superseded by: —

## Context

ADR 0220 added an adapter-local OAuth controller but intentionally left presentation to
its embedding. A browser callback is host interaction, not agent-loop behavior: putting it
in `engine/`, ACP, or the long-running daemon would mix local-user presence with remote
session transport. Reimplementing discovery, PKCE, exchange, or persistence around a
callback would also create a second OAuth protocol implementation beside the official SDK.

A useful login operation must prove more than receipt of a callback. It must drive the real
protected MCP connection through authorization, token persistence, initialize, and initial
tool listing. At the same time, this repository has no canonical OAuth server-profile
resolver or credential-store key-acquisition policy, so a command would need a second and
security-sensitive configuration spelling.

## Decision

Provide a stdlib-only, explicitly constructed `mcp/oauthlogin` runtime. Each operation
binds an ephemeral IPv4 loopback listener, creates a random callback path, validates the
SDK-generated state and configured issuer, presents the authorization URL through an
injected browser launcher or host-owned no-browser writer, and deterministically shuts down
its listener and HTTP server. One runtime serializes complete interactions; waiting callers
remain independently cancellable. It returns only code, state, and issuer so the existing
controller and official SDK repeat their own checks and own all protocol work.

Bridge that value in `internal/adapter/mcp/oauth_login.go` without duplicating validation.
Add `internal/app/mcplogin.go` (`LoginMCP`) as a one-shot operation over one already-resolved
OAuth `ServerConfig`: install the generated redirect and presenter on a copy, call the real
`mcp.Connect`, require authenticated initialize and tool listing, then close the temporary
server/controller. The injected credential store is borrowed and remains caller-owned.

Do not construct this runtime by default, add ACP/protobuf surface, or add a `mecated mcp
login` command yet. The command waits for issue #523's shared profile resolver and key
acquisition policy; it must eventually call the same one-shot operation rather than accept
OAuth secrets and endpoints on argv.

## Consequences

Embedding hosts can opt into a bounded local browser flow while headless paths remain
unchanged and fail without interactive presentation. Authorization URLs stay confined to
the selected browser or terminal writer. The callback listener and browser process are
per-operation resources with explicit cleanup; the durable credential remains the existing
controller record.

The runtime supports only callbacks that can reach the same machine and profiles compatible
with random loopback ports and paths. Production readiness remains blocked on the metadata
profile constraints recorded by ADR 0219 and on canonical profile/key wiring. There is no
ACP or default-daemon login claim.

## See also

- [ADR 0219 — Qualify the official MCP SDK authorization-code profile](./0219-mcp-oauth-sdk-profile.md)
- [ADR 0220 — Adapter-local MCP OAuth controller](./0220-mcp-oauth-controller.md)
- [Extensibility architecture](../architecture/extensibility.md)
- [Production readiness](../design/PRODUCTION-READINESS.md)
