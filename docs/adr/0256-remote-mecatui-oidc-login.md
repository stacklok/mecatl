# ADR 0256 — Remote mecatui OIDC client login

- Status: Accepted
- Date: 2026-08-24
- Scope: `mecatui` remote connection enrollment, OIDC credentials, and the Kind remote-client qualification flow
- Supersedes: None
- Superseded by: ADR 0260

## Context

`mecatui connect ADDRESS` is a client-only dial action, while remote servers may
require caller identity. Requiring users to copy short-lived bearer values into a
flag is inconvenient and makes refresh the caller's problem. At the same time,
connecting must not surprise an operator with a browser, guess an issuer, or reuse a
credential for a different target. The client also needs to support the private HTTPS
issuer used by the disposable Kind qualification fixture without weakening TLS or
address validation.

## Decision

Keep the command taxonomy explicit:

- `mecatui llm login` remains the ToolHive LLM gateway login.
- `mecatui login ADDRESS` performs remote public-client OIDC Authorization Code + PKCE
  enrollment and requires explicit issuer, client ID, audience, and CA options.
- `mecatui connect ADDRESS` only connects. It never implicitly opens a browser; an
  unenrolled target directs the operator to `login ADDRESS`.

Bind saved credentials to the canonical target and the complete public OIDC identity.
Store public connection metadata in a durable registry, but store OAuth material in a
keyring-wrapped encrypted credential store. Never place tokens in the registry, UI
restart intent, logs, or command arguments. A saved connection uses a dynamic bearer
source that validates an access token per RPC, refreshes it when necessary, and
conditionally persists rotation.

Use the shared ToolHive Core-derived scoped private-HTTPS transport for OIDC
discovery, token exchange, and JWKS: explicit CA roots, HTTPS-only admission,
DNS-pinned private addresses, TLS/hostname verification, and redirect refusal remain
mandatory. `/connect` is a confirmation-gated saved-target chooser. Selecting a
saved target restarts mecatui into a fresh remote session; selecting a new target
defers to the CLI login flow. No session history crosses a target switch.

The Kind remote flow is a confirmation-gated live qualification path after setup,
using host aliases, the public fixture CA, and the `mecatui-kind` public client. It is
not part of ordinary offline tests.

## Consequences

Remote users get a repeatable login and automatic token refresh without exposing
credential material to the TUI. A first connection needs explicit OIDC deployment
metadata and a CA file, and keyring availability is required. Saved targets are
portable only as metadata: the credential encryption key remains in the local OS
keyring. Target changes intentionally discard the active client session and require a
new remote session.

## See also

- [TUI transport and login](../tui.md)
- `user-docs/mecatui/remote-servers.md` — the remote server user guide
- [Kind vMCP fixture](../../deploy/mecak8s-vmcp/README.md)
- [Private HTTPS OIDC issuer](./0236-private-https-oidc-issuer.md)
- [ToolHive direct mode](./0102-toolhive-direct-mode.md)

---
