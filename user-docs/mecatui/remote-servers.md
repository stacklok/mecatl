---
sidebar_position: 3
title: Connect to a server
description: Connect mecatui to a remote Mecatl server and understand which settings it controls.
---

# Connect to a server

`mecatui` is always a client, but it can supply its own local server or dial one that an operator already runs.

## Credential storage on desktop and headless hosts

Only `mecatui login` accepts `--credential-store=auto|keyring|file` (default `auto`).
On a fresh Linux configuration root, a read-only, no-autostart Secret Service check
has a 500 ms joined deadline: absence or that timeout selects file; a present service
uses normal keyring initialization. Keyring errors never trigger fallback. macOS
`auto` uses keyring; explicit `--credential-store=file` bypasses it on either platform.

Selection is pinned before OAuth in non-secret `clientauth-credential-backend.json`
under `$XDG_CONFIG_HOME/mecatl`, even if login is cancelled. Later login, connect,
refresh, reauthentication, and logout reuse that backend. A conflicting selector
fails rather than switching or migrating. Existing valid legacy enrollments pin
keyring; corrupt state fails closed.

File credentials in `clientauth-plaintext/` are **plaintext at rest**, with 0700
directories and 0600 files. These permissions do not protect against another process
running as your account, root, backups, or snapshots. The first file selection prints
`Using file-backed credential storage (owner-only permissions).` once before OAuth;
connect and refresh do not repeat it. Upgrade **all clients sharing the root** before
using file storage; concurrent older clients are unsupported.

The [Kind qualification guide](https://github.com/stacklok/mecatl/blob/main/deploy/mecak8s-kind/README.md#stored-credential-qualification-manual)
provides isolated-root macOS explicit-file and genuinely headless Linux default-auto
journeys. Those manual login/refresh/logout checks are not part of offline tests and
must be recorded before claiming full E2E qualification. `--no-browser` still uses
PKCE and needs browser connectivity to the loopback callback; it is not device login.

## Choose the connection shape

**Embedded mode** is bare `mecatui`. It starts a private `mecated` in the same process and connects over a private UNIX socket. The TUI process owns the local workspace, provider credentials, session storage, and policy configuration used by that embedded server.

```sh
OPENAI_API_KEY=sk-... mecatui --workspace "$PWD"
```

**Remote mode** is `mecatui connect ADDRESS`. It dials the named, already-running `mecated`; it never starts an embedded server or searches for one. The remote server owns its workspace, provider credentials and model availability, storage, retention, and policy. Local client settings do not configure that server.

```sh
mecated serve &
mecatui connect 127.0.0.1:8080
```

Every connection uses the same path-free contract. `--workspace` is embedded/server
operator configuration and is rejected by `mecatui connect`, including loopback. The
server binds its configured default (or no-FS). `/worktrees` lists eligible alternatives
from the owned source session and switches through an opaque short-lived selector; it
never sends a path or exact environment ref. Selectors expire on server restart, so the
client relists and keeps the current session if relist/switch fails.

## Connect securely

A loopback server can use its local single-user trust model. With no explicit credential,
mecatui preserves an existing saved enrollment; if the local target has never been enrolled,
it connects credential-free without creating login state. If the server requires a bearer
token, pass the token supplied by its operator:

```sh
export MECATL_AUTH_TOKEN="$(cat ~/.mecatl/token)"
mecatui connect 127.0.0.1:8080 \
  --auth-token "$MECATL_AUTH_TOKEN"
```

For a non-loopback endpoint, verified TLS is automatic; `--tls`, `--tls=true`,
and `--tls-ca` also select verified TLS. `--insecure` instead uses encrypted TLS
without certificate verification and is only for controlled testing — it cannot
carry a bearer to a non-loopback server, because an unverified certificate hides
an interceptor that would read the token. `--tls=false`
is the explicit plaintext downgrade; use it only for controlled, non-bearer testing.
Add a CA bundle with `--tls-ca` only when the server uses a private CA.

```sh
mecatui connect mecated.example.internal:443 \
  --tls-ca /path/to/company-ca.pem \
  --auth-token "$MECATL_AUTH_TOKEN"
```

Authentication proves the caller's credential; TLS protects the connection and verifies the server. They are separate settings. Do not use `--insecure` except for controlled testing.

Use connect-only `--anonymous` when you intentionally want no bearer even when saved OIDC
enrollment exists. It bypasses saved credentials and has no environment equivalent. A static
token from `--auth-token` or `MECATL_AUTH_TOKEN` wins if both are present. On a clean enrollment miss, mecatui
already attempts a credential-free connection and lets the server decide whether caller
authentication is required. Remote credential-free transport is never weakened by a private
address or hostname: verified TLS remains the default, and plaintext additionally requires
`--tls=false`.

For example, a Tailscale deployment may deliberately make tailnet membership and ACLs the
shared authority and transport boundary:

```sh
mecatui connect ozzllama:9080 --tls=false
```

Bind the server to one concrete Tailscale address, never a wildcard, and do not enable
Funnel. Treat ACLs as load-bearing, use a dedicated server workspace with least OS
authority, and configure a restrictive rate limit. Run it at `--posture strict` (or
`trusted` only when its project inputs are trusted): `auto` and `yolo` weaken the remaining
approval boundary. The server's prominent non-loopback no-caller-authentication warning is
expected; ordinary TLS would not suppress it because TLS is not caller authentication. A
non-loopback client does not select or upload its local workspace—the server remains the
workspace authority.

Caller identity is attribution, not tenant isolation: authenticated callers can still list and act on other callers' sessions. Do not treat a token-authenticated shared server as a tenancy boundary.

## Remote protected-resource discovery

When the server publishes the optional RFC 9728 profile, `mecatui login` can
accept a bare host or canonical HTTPS resource URL and discover the issuer,
audience, public client hint, and scopes. The first enrollment displays the full
protected-resource details and asks for default-deny confirmation. A subsequent
login skips both only when fresh discovery exactly matches the saved canonical
connection for that resource, including resource, complete identity and scopes,
issuer CA, and issuer-address policy. Any changed value is treated as a new
enrollment and displays the details for confirmation again. It requests an
advertised `scopes_supported` list exactly, or the fixed
`openid,profile,offline_access` baseline when the member is omitted. Discovery rejects
`--scopes`; administrators configure `oidc.scopes` for other scopes. Metadata and issuer
lookup use anonymous verified HTTPS bootstrap; the resulting authenticated gRPC
connection is a separate transport decision. The configured resource is the
service-wide protected-resource base, so every protected API route advertises
its same metadata URL; the server never derives that URL from a request Host or
path. The RFC fields remain distinct from
mecatl extension fields, and ToolHive/ToolHive-Core are implementation
provenance rather than an engine dependency. Existing explicit issuer/client/
audience login remains supported.


Remote enrollment and connecting are separate actions:

```sh
mecatui login mecated.example.internal:443 \
  --issuer https://id.example.internal \
  --client-id mecatui --audience mecatl \
  --tls-ca /path/to/issuer-ca.pem --private-issuer
mecatui connect mecated.example.internal:443 \
  --tls --tls-ca /path/to/server-ca.pem
```

`mecatui login ADDRESS` accepts a bare HTTPS hostname or canonical HTTPS resource URL
when the server publishes RFC 9728 metadata; it otherwise requires explicit `--issuer`,
`--client-id`, and `--audience`, running the public OIDC Authorization Code + PKCE flow —
it is persistent enrollment, never an anonymous-login command. Saved `mecatui connect
ADDRESS` accepts the same confirmed resource alias (including its bare hostname for a
root resource) or the legacy `host:port` target; it never rediscovers metadata. Discovery
is anonymous, redirect-free, timeout-bounded HTTPS bootstrap and remains separate from
the authenticated gRPC transport. Explicit-flow login defaults to a public issuer
verified against the system roots; the example above is a PRIVATE issuer, so it passes
`--private-issuer`, which requires `--tls-ca`. The login `--tls-ca` verifies the issuer's
discovery, token, JWKS, refresh, and revocation endpoints; it does not configure server
transport trust. An explicit issuer CA bundle path/reference, not its contents, is saved as public
target metadata. A later `connect` with saved credentials always uses verified TLS,
including for loopback; only `connect --tls-ca` independently verifies a private-CA
gRPC server. Login saves
public target metadata in the connection registry and stores the credential in a
canonical-root-scoped, keyring-wrapped encrypted store. The credential is bound to the
canonical target and OIDC identity. An old unsuffixed keyring key is copied without
deletion only when that root already contains an actual encrypted credential record; an
empty opened namespace does not trigger migration. A credential enrolled under a legacy zero-padded port spelling
needs one login after upgrade. `mecatui connect ADDRESS` never opens a browser. Credential
selection is explicit token, explicit `--anonymous`, saved enrollment, then a
credential-free attempt for any clean missing enrollment. The server is authoritative: only
an actual `Unauthenticated` RPC opens recovery and recommends `--auth-token` or
`mecatui login ADDRESS`, which is OIDC enrollment. Explicit `--anonymous` deliberately
bypasses saved state and gets rejection wording that says so. `PermissionDenied` remains an
authorization failure, and network/TLS errors never offer authentication recovery. Corrupt
or unreadable registry, keyring, or credential state never falls back anonymously.
Add `--no-browser` to print the authorization URL for you to open yourself, which is
what you want over SSH or on a headless host. Remote login listens at the registered
`http://127.0.0.1:18473/oauth/callback`. Open the printed URL in a browser on your
workstation and forward that fixed callback port to the host running the login:

```sh
ssh -N -L 18473:127.0.0.1:18473 user@login-host
```

This remote flow is Authorization Code + PKCE, not device flow. Wrong-route, wrong-state, and malformed pre-state
probes are unlimited and do not consume the secret state; the deadline and connection
limits still bound the listener. This differs from MCP OAuth's random callback path,
which retains a bounded matching-route attempt count. Only a callback proving the secret
state can terminate on a provider error or semantic rejection. Closed
validator reasons never echo callback values. A provider's OAuth `error` and
`error_description` are the narrow exception and are printable-filtered and bounded.
After enrollment, every RPC demands a currently validated access token. Successful
token demand, not RPC success, gates proactive refresh; refresh, enrollment, and logout
share one per-target interprocess transaction, and rotated credentials are saved with
CAS. Only an exact structured `invalid_grant` code removes a rejected credential;
provider prose does not. The default login request includes `offline_access`, but the
issuer must offer and grant that scope before it can return a refresh token. Without a
refresh token, the initial login can still succeed, but a later access-token expiry
requires `mecatui login ADDRESS` again. A discovered-login caller cannot omit
`offline_access` with `--scopes`; the server controls the advertised set or omission
baseline. Administrators configure `oidc.scopes` when a different discovered-login
profile is intended. Legacy explicit identity login retains `--scopes`, including an
intentional omission of `offline_access`. This managed OIDC mode is refreshed by
mecatui; a static `--auth-token` remains caller-managed and is never
refreshed. The bearer is not placed in UI state, logs, or command arguments.

Remove an enrollment with:

```sh
mecatui logout mecated.example.internal:443
```

Logout conditionally removes the target-bound credential before its public registry
entry under the target transaction lock. It is safe to repeat. A concurrent rotation
is reloaded and retried once; a persistent conflict or re-enrollment retains the
metadata and reports an incomplete logout so the unresolved credential does not become
unreachable. After releasing the lock, it spends one operation-wide fifteen-second budget
on scoped client creation, discovery, and all best-effort RFC 7009 revocation requests.
An unavailable provider never blocks local deletion;
provider-side termination is therefore not guaranteed. The command does not prune a
credential-only orphan because the encrypted store has no enumeration operation.

If a saved credential needs attention, mecatui opens the recoverable `/connect`
chooser and preselects the target. It distinguishes an expired session, an unusable
local credential, credential cleanup that should be retried without a browser, and a
server-rejected bearer (check issuer, audience, or CA rather than repeatedly logging
in). The chooser never opens a browser: re-authentication is confirmed and runs only
after the TUI exits. A same-target re-auth resumes a prior chat only when the server's
ownership-checked session and transcript reads authorize the new caller and the prior
session is at a terminal turn boundary. The recovery action preserves that candidate and
the current server CA path only for the same target. Otherwise it starts a fresh chat;
an interrupted prompt is never replayed automatically.

The `/connect` overlay is a confirmed chooser for saved targets. Choosing a saved
target restarts mecatui into a new remote session; choosing a new target defers to
`mecatui login ADDRESS` first. No session history crosses a target change.
`mecatui llm login` is unrelated: it is the ToolHive LLM gateway login.

For private HTTPS OIDC issuers, the login uses an explicit CA bundle path and the scoped
ToolHive Core-derived private-HTTPS transport. It retains hostname verification,
DNS-pinned address checks, HTTPS-only admission, and redirect refusal. The Kind
remote flow is available after fixture setup using the documented host aliases and
public CA, but it is a live qualification flow rather than ordinary offline-test
coverage. Setup remains confirmation-gated; see [the fixture guide](https://github.com/stacklok/mecatl/blob/main/deploy/mecak8s-kind/README.md).
## Where to go next

Use [Getting started](./getting-started.md) for the local first-run path and [Sessions](./sessions.md) to browse remote or embedded history. Operators configuring a server should use [Run mecated standalone](/building/deployment/mecated.md), [gRPC and HTTP deployment](/building/deployment/grpc-http.md), or [mecak8s](/building/deployment/mecak8s.md), as appropriate.

For the exhaustive transport flag reference, see [`docs/tui.md`](https://github.com/stacklok/mecatl/blob/main/docs/tui.md#transport-commands).
