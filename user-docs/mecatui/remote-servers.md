---
sidebar_position: 3
title: Connect to a server
---

# Connect to a server

`mecatui` is always a client, but it can supply its own local server or dial one that an operator already runs.

## Choose the connection shape

**Embedded mode** is bare `mecatui`. It starts a private `mecated` in the same process and connects over a private UNIX socket. The TUI process owns the local workspace, provider credentials, session storage, and policy configuration used by that embedded server.

```sh
OPENAI_API_KEY=sk-... bin/mecatui --workspace "$PWD"
```

**Remote mode** is `mecatui connect ADDRESS`. It dials the named, already-running `mecated`; it never starts an embedded server or searches for one. The remote server owns its workspace, provider credentials and model availability, storage, retention, and policy. Local client settings do not configure that server.

```sh
bin/mecated serve &
bin/mecatui connect 127.0.0.1:8080 --workspace "$PWD"
```

For loopback connections, the client-selected workspace is evaluated on the server host
and must be an absolute path available there. For a non-loopback target, mecatui sends
an empty workspace and rejects `--workspace`; the remote server's listener authority
chooses its configured root (or its no-FS profile). A client path is never a way to
select a checkout inside a remote container or pod.

## Connect securely

A loopback server can use its local single-user trust model. If the server requires a bearer token, pass the token supplied by its operator:

```sh
export MECATL_AUTH_TOKEN="$(cat ~/.mecatl/token)"
bin/mecatui connect 127.0.0.1:8080 \
  --auth-token "$MECATL_AUTH_TOKEN" --workspace "$PWD"
```

For a non-loopback endpoint, use TLS when sending a bearer. Add a CA bundle only when the server uses a private CA:

```sh
bin/mecatui connect mecated.example.internal:443 \
  --tls --tls-ca /path/to/company-ca.pem \
  --auth-token "$MECATL_AUTH_TOKEN"
```

Authentication proves the caller's credential; TLS protects the connection and verifies the server. They are separate settings. Do not use `--insecure` except for controlled testing.

Caller identity is attribution, not tenant isolation: authenticated callers can still list and act on other callers' sessions. Do not treat a token-authenticated shared server as a tenancy boundary.

## Remote OIDC login

Remote enrollment and connecting are separate actions:

```sh
bin/mecatui login mecated.example.internal:443 \
  --issuer https://id.example.internal \
  --client-id mecatui --audience mecatl \
  --tls-ca /path/to/issuer-ca.pem --private-issuer
bin/mecatui connect mecated.example.internal:443 \
  --tls --tls-ca /path/to/server-ca.pem
```

`mecatui login ADDRESS` runs the public OIDC Authorization Code + PKCE flow. It
requires `--issuer`, `--client-id`, and `--audience`. It defaults to a public issuer
verified against the system roots; the example above is a PRIVATE issuer, so it passes
`--private-issuer`, which requires `--tls-ca`. The login `--tls-ca` verifies the issuer's discovery,
token, JWKS, refresh, and revocation endpoints; it does not configure server transport
trust. An explicit issuer CA bundle path/reference, not its contents, is saved as public
target metadata; the later
`connect --tls-ca` independently verifies the gRPC server. Login saves
public target metadata in the connection registry and stores the credential in a
canonical-root-scoped, keyring-wrapped encrypted store. The credential is bound to the
canonical target and OIDC identity. An old unsuffixed keyring key is copied without
deletion only when that root already contains an actual encrypted credential record; an
empty opened namespace does not trigger migration. A credential enrolled under a legacy zero-padded port spelling
needs one login after upgrade. `mecatui connect ADDRESS` never opens a browser; an
unenrolled target is rejected before it dials and tells you to run `mecatui login
ADDRESS` first.
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
requires `mecatui login ADDRESS` again. Omit `offline_access` explicitly with
`--scopes` only when that re-login behavior is intended. This managed OIDC mode is
refreshed by mecatui; a static `--auth-token` remains caller-managed and is never
refreshed. The bearer is not placed in UI state, logs, or command arguments.

Remove an enrollment with:

```sh
bin/mecatui logout mecated.example.internal:443
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
