# ADR 0277 — Remote mecatui OIDC client authentication

- Status: Accepted
- Date: 2026-08-28
- Scope: `mecatui` remote OIDC enrollment, credential lifecycle, connection recovery, and logout
- Supersedes: ADR 0270, ADR 0271, ADR 0272, ADR 0273
- Superseded by: ADR 0274 (logout provider budget only)

## Context

A remote `mecated` can require OIDC caller identity. Asking operators to copy
short-lived bearer values into a flag makes refresh their problem and risks exposing
credentials in command history. Remote enrollment therefore needs a public-client
Authorization Code + PKCE flow, durable target metadata, protected OAuth material,
and refresh without placing tokens in the TUI, logs, or command arguments.

Several boundaries must remain explicit. `mecatui llm login` authenticates the
ToolHive LLM gateway, while remote server login authenticates a caller to one
`mecated` target. Connecting must not unexpectedly open a browser or infer issuer
configuration. A private issuer needs explicit CA roots and private-address admission
without weakening TLS, hostname verification, DNS pinning, or redirect refusal.

The client also has two related durability problems. Public connection metadata must
remain listable without opening the secret store, while OAuth material belongs in a
keyring-wrapped encrypted store. Login, refresh, recovery, and logout can run in
separate mecatui processes, so live cross-process updates must not overwrite the
wrapping key, delete a concurrent enrollment, or leave a returned-error path with an
unreachable credential.

Provider lifetime behavior is not uniform. A provider can maintain separate browser
SSO, refresh-token inactivity, refresh-token absolute, and access-token lifetimes.
OIDC discovery does not expose a portable rule saying that refresh-token use resets a
browser SSO idle clock. The client can prevent its own background refresher from
sustaining itself, but it cannot promise when an external browser session expires.

## Decision

### Commands, identity, and storage

Keep the command taxonomy explicit:

- `mecatui llm login` remains the ToolHive LLM gateway flow.
- `mecatui login ADDRESS` performs remote public-client Authorization Code + PKCE
  enrollment and requires an explicit issuer, client ID, audience, and issuer CA.
  Its callback wait is bounded to five minutes by default and accepts a positive
  `--callback-timeout` override. Its `--tls-ca` is issuer trust only.
- `mecatui connect ADDRESS` only connects. It never implicitly opens a browser or
  guesses missing OIDC settings. Its optional `--tls-ca` is server trust only.
- `mecatui logout ADDRESS` removes the saved target locally, then makes bounded
  best-effort provider revocation attempts.

Bind credentials to the canonical target and complete public OIDC identity. Target
canonicalization rewrites numeric ports to their ordinary decimal spelling. Credentials
created by the pre-canonicalization client with a zero-padded port therefore have a
different record key and require one login after upgrade. Keep public connection metadata
in an owner-only registry and OAuth material in a keyring-wrapped encrypted credential
store. Never place tokens in the registry, TUI state, restart intents, logs, or command
arguments.

Treat each canonical clientauth store root as an independent credential store. Scope
the OS-keyring account to that root and serialize first creation with a stable
root-local cross-process lock. On upgrade, copy the existing unsuffixed key into
the root-scoped account only when the encrypted namespace contains an actual credential
record; merely opening an empty legacy namespace is not migration evidence. Retain the
legacy entry.

Serialize refresh, enrollment, logout, and superseded-credential cleanup for one
canonical target with the same stable canonical-root-plus-target cross-process transaction
lock. Enrollment acquires it only after a new token is ready to persist. If a credential
save reports an ambiguous post-rename error, reread it under that lock and accept only the
intended token. If registry commit reports an error, reread the registry to distinguish a
committed atomic rename from a pre-commit failure, then restore or delete only the
credential version written by that operation. Both refresh and compensation remain
versioned by credential CAS. Reauthentication may replace a current corrupt credential
only while the target lock is held, the registry still names that exact identity, and the
credential backend atomically proves the record remains corrupt; operationally unreadable
records are never overwritten. If the following registry metadata rewrite fails, retain the
repaired credential because the prior registry entry still makes it reachable.
Persist registry updates with a private unique temporary file, file sync, atomic rename,
and directory sync. A process killed between the two stores can still leave
partial local state; a missing credential requires login rather than adding a durable
transaction journal, and a credential-only orphan cannot be enumerated.

### OIDC transport and loopback callback

Use the scoped private-HTTPS transport for discovery, token exchange, JWKS, and
revocation: explicit CA roots, HTTPS-only endpoint admission, a positive private,
loopback, or link-local address allowlist, DNS-pinned dialing with re-resolution,
TLS/hostname verification, same-origin endpoint validation, and redirect refusal.
Keep issuer trust roots separate from the gRPC server's optional CA roots. Resolve the
issuer CA reference to an absolute cleaned path at enrollment, reject relative persisted
references, and save only that path as public target metadata, never the CA contents.

Use the registered fixed loopback redirect
`http://127.0.0.1:18473/oauth/callback` for remote mecatui. In fixed callback mode,
wrong-route, wrong-state, and malformed pre-state requests are unlimited: they return
errors but do not spend a terminal attempt budget; the callback deadline, concurrent-connection cap, and
HTTP timeouts bound that public route. Once the secret state matches, a provider error
or semantic callback rejection is terminal. The existing random-path MCP OAuth mode
retains its bounded matching-route attempt policy because the random path itself is the
callback capability.

### Refresh and error classification

Use activity-gated proactive refresh. Provider-neutral activity is an
application-facing `Token` demand that obtains a bearer, including one satisfied by an
already-valid access token; it is not a successful RPC signal. The background refresher
is not activity and may refresh only when application token demand occurred since the
previous refresh and the access token is inside the refresh-ahead window.
This prevents a background refresh from arming another background refresh. It does
not claim to preserve or reset a provider's browser SSO lifetime.

Serialize public and proactive refresh work first through the per-target transaction
lock and then through the source mutex; persist token rotation with credential CAS. Match
OAuth `invalid_grant` only as a structured `oauth2.RetrieveError` whose exact
`ErrorCode` is `invalid_grant` before conditionally deleting the rejected credential;
provider prose and nested descriptions never classify it. Preserve `ErrLoginRequired` as
the compatibility sentinel with safe typed causes for not-enrolled, expired, and unusable
credentials. Translate those adapter-specific causes at the mecatui composition boundary
into the client's closed, proto-free auth-reason contract. Unknown discovery, exchange,
validation, infrastructure, and cancellation errors remain unclassified.

Never include access tokens, refresh tokens, authorization codes, provider response
bodies, or provider-controlled discovery endpoint values in errors, diagnostics, or UI
state. A provider's OAuth callback `error` and `error_description` are the narrow
exception: each is independently restricted to the RFC 6749 printable subset and
bounded before it may be returned to the operator. Callback-validator failures instead
expose only a closed, harness-authored reason; they never echo hostile callback fields.

### Connect and authentication recovery

Represent restart intent with a closed action: connect a saved target without a
browser, explicitly reauthenticate an expired or unusable same target, retry after
credential cleanup without a browser, or add a target. Preserve the current server CA
path only while restarting the same target; never reuse it for a target switch. Ordinary
saved-target selection and every actual target switch start a fresh remote session. No
session history or resume candidate crosses a target switch.

During same-target authentication recovery only, retain the interrupted session ID as
a candidate. Adopt it only after ownership-enforced session and transcript reads
succeed and show a completed, cancelled, or failed terminal boundary. Missing,
ownership-hidden, active, awaiting, and infrastructure-ambiguous candidates are
discarded and the authenticated connection starts fresh. A bearer-backed server
`Unauthenticated` response is `rejected` and offers no identical browser login loop.

### Logout

Under the target transaction lock, remove local credential material and then the
matching registry snapshot using their versioned expectations. A persistent conflict,
unreadable credential, unavailable store, or registry race reports incomplete logout
rather than deleting a concurrent enrollment. Repeating logout for an absent target
succeeds. Logout opens the encrypted store without creating keyring state.

After local cleanup, spend at most one operation-wide five-second budget on provider
communication for the target. The budget begins before HTTP-client construction and is
shared by discovery and refresh/access-token RFC 7009 revocation across every retained
legacy entry; a shorter caller deadline wins. Provider failure never restores local
state. Credential-only orphans with no registry entry remain outside logout's reach
because the store exposes no enumeration contract.

## Consequences

Remote users get repeatable enrollment, target-bound protected credentials, automatic
refresh for an actively used managed bearer source, explicit recovery, and idempotent
local logout. A static `--auth-token` bearer remains caller-supplied and unmanaged:
mecatui neither obtains nor refreshes it. Opening mecatui while idle cannot make the
managed background refresher sustain itself, but provider-specific SSO and refresh-token
lifetime policy still determines whether a later refresh succeeds.

The client owns small process-lifetime resources: a refresh poller, OS-keyring entries,
root/target lock sentinels, and the connection registry. Callers close the refresh
source before closing its credential store. The transaction lock serializes live local
processes but deliberately does not provide crash-atomic commit across the registry and
encrypted credential files.

Private deployments may use distinct issuer and gRPC server trust roots. The fixed
loopback callback is more discoverable than MCP OAuth's random path, so only possession
of the state secret can make its callback decision terminal.

Live Kind qualification remains environment-dependent and separate from ordinary
offline tests.

## See also

- [ADR 0218 — Internal encrypted credential-store substrate](./0218-credential-store.md)
- [ADR 0235 — Scoped private HTTPS OIDC transport](./0235-scoped-private-https-oidc-transport.md)
- [ADR 0236 — Private HTTPS OIDC issuer](./0236-private-https-oidc-issuer.md)
- [ADR 0112 — Host-owned loopback MCP OAuth login](./0112-mcp-oauth-loopback-runtime.md)
- [TUI guide](../tui.md)
- [Architecture guide](../architecture.md)
- [Production readiness](../design/PRODUCTION-READINESS.md)
- `user-docs/mecatui/remote-servers.md`

---
