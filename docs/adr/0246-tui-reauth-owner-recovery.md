# ADR 0246 — Recover remote TUI authentication without replaying ownership-ambiguous work

- Status: Accepted
- Date: 2026-08-26
- Scope: `mecatui` remote OIDC transport and session recovery
- Supersedes: None
- Superseded by: ADR 0244

## Context

A saved remote credential can expire or become locally unusable while a TUI is
starting or while a Converse stream is active. Treating every `Unauthenticated`
response as a login request loops an operator through a browser when the server
rejects issuer, audience, or CA configuration. Resuming a session after re-login is
also unsafe if the new token identifies a different caller, or if the interrupted
turn may still be running.

## Decision

Use a closed, proto-free client auth-reason contract: `not_enrolled`,
`session_expired`, `credential_unusable`, `credential_cleanup`, and `rejected`.
Local causes cross the per-RPC credential seam only through typed errors; no error
text is inferred. An anonymous server `Unauthenticated` is `not_enrolled`; a
bearer-backed server `Unauthenticated` is `rejected`.

Route classified startup, create-session, and Converse failures to the existing
`/connect` recovery overlay. It preselects the failed target and never opens a
browser. Expiry requires confirmed re-auth; unusable credentials state that login
may not repair local storage; cleanup retries the connection without login; rejected
bearers offer no re-login for that target.

A same-target re-auth may adopt a prior session only after the existing
ownership-enforced `GetSession` and transcript reads authorize the caller. Missing,
mismatched, ownerless, active, awaiting, and infrastructure-ambiguous candidates
start fresh. Only completed, cancelled, or failed terminal boundaries are adopted;
an in-flight turn is never replayed automatically. Credentials and principals remain
outside UI and restart state.

## Consequences

Recovery is actionable without weakening server ownership checks or placing OAuth
material in the alternate-screen process state. An operator may lose an interrupted
turn or need to begin a fresh chat, which is preferable to duplicate mutations or
cross-caller disclosure. The client must keep typed auth wrapping and its reason
mapping synchronized with `clientauth`.

## See also

- [ADR 0248](./0248-remote-mecatui-oidc-login.md)
- [ADR 0245](./0245-activity-gated-remote-oidc-refresh.md)
- [TUI guide](../tui.md)
- `user-docs/mecatui/remote-servers.md`
