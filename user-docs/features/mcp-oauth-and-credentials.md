---
sidebar_position: 320
title: MCP OAuth and credentials
description: Configure MCP OAuth profiles and protect the credentials they use.
---

# MCP OAuth and credentials

Authenticate configured MCP servers without placing credentials in URLs or
model-visible tool arguments. This page covers authentication profiles, OAuth
login, credential storage, and rotation. For tool behavior, see the
[MCP client guide](/building/what-you-get/mcp-client.md).

The available profile modes are `none`, `static_bearer`, and `oauth`.

## Availability

Global MCP authentication profiles are supported by `mecated`, `mecatequi`,
`mecak8s`, and `mecatui`'s embedded server. They are operator configuration, not
project configuration. A project `.mecatl/settings.yaml` cannot install or
weaken an MCP credential profile.

A server configured with `--mcp-server name=URL` can also use the legacy
`MCP_<NAME>_TOKEN` bearer-token convention. Use operator `mcp.servers` profiles
for OAuth, rotation, or deployment-managed credentials.

## Configure a profile

Put the profile in the operator-tier `settings.yaml`. Secret-bearing fields are
environment-variable names, not secret values. A minimal profile is:

```yaml
mcp:
  servers:
    github:
      url: https://mcp.example.com/github
      auth:
        mode: oauth
        issuer: https://idp.example/realms/operators
        client_id: mecatl
        client_secret_env: MECATL_MCP_CLIENT_SECRET
        scopes: [repo]
        credentials:
          local:
            root: /home/operator/.local/state/mecatl/mcp-credentials
            key_env: MECATL_MCP_CREDENTIAL_KEY
```

Keep the settings file owner-only and validate it before serving. See the
[configuration reference](/reference/configuration.md) for the full schema.

```sh
umask 077
mecated config validate --file "$HOME/.config/mecatl/settings.yaml"
```

The local OAuth store requires an absolute root and a base64-encoded 32-byte
encryption key. Generate the key outside YAML and provide it through the named
environment variable:

```sh
umask 077
mkdir -p "$HOME/.local/state"
chmod 700 "$HOME/.local/state"
openssl rand -base64 32 > "$HOME/.local/state/mecatl-mcp.key"
chmod 600 "$HOME/.local/state/mecatl-mcp.key"
export MECATL_MCP_CREDENTIAL_KEY="$(cat "$HOME/.local/state/mecatl-mcp.key")"
```

Do not put the key, client secret, access token, or refresh token in source
control, a command argument, `settings.yaml`, or a prompt.

## Authorize a local OAuth profile

A mutable local profile is authorized explicitly by the operator:

```sh
mecated mcp login github
mecated mcp login github --no-browser
```

The normal command opens a browser. `--no-browser` prints the authorization URL
for another browser or a headless operator. Use `--permission-config` to select
trusted operator settings files:

```sh
mecated mcp login github \
  --permission-config /etc/mecatl/settings.yaml
```

After login, restart the server and verify that the namespaced `mcp__github__*`
tools appear. Mecatl restores the encrypted credential and refreshes tokens when
needed. Preregistered and CIMD profiles persist refresh-token rotation in the
local store so the next restart remains warm.

### Direct dynamic client registration

A named direct/global profile can use `client: {mode: dcr, dcr: {}}` when no
client was preregistered. It requires exact `scopes: [openid]` (or omission),
`request_refresh_token: false` (or omission), and a mutable local credential
store:

```yaml
mcp:
  mode: global
  servers:
    - name: connector
      url: https://connector-gateway.stacklok.dev/gw/mcp
      auth:
        mode: oauth
        oauth:
          profile: connector
          principal: local-user
          issuer: https://connector-gateway.stacklok.dev
          client: {mode: dcr, dcr: {}}
          scopes: [openid]
          request_refresh_token: false
          credentials:
            mode: local
            local:
              root: /absolute/owner-only/credentials
              key_env: MECATL_MCP_CREDENTIAL_KEY
          network: {additional_origins: [], private_origins: [], max_redirects: 0}
```

Login dynamically registers a public client and then uses the same browser/PKCE
flow. The durable registration and generation-bound access grant are separate;
no client secret or refresh token is requested or accepted.

Ordinary startup reuses the unexpired grant and never launches a browser. On
expiry it returns login-required without attempting refresh. Run
`mecated mcp login SERVER` explicitly to obtain a new access grant while reusing
the valid registration. If registration itself was interrupted, use
`--retry-dcr-registration`; to deliberately replace a valid ready registration
and its grant, use `--reset-dcr-registration`. The flags are mutually exclusive
and valid only for DCR. Ready identity drift is reset-required and may be
replaced with `--reset-dcr-registration`. Pending identity drift is reported as
pending-identity-mismatch; it cannot reset or retry until the matching profile,
principal, canonical resource, and exact issuer configuration is restored,
after which use `--retry-dcr-registration`. Corrupt, noncanonical, unsupported,
or grant-mismatched persisted state remains repair-only: neither flag mutates
it. The flags do not revoke an upstream registration. Complete public-client
refresh remains deferred to
[issue #1355](https://github.com/stacklok/mecatl/issues/1355).

#### Manual local-mecatui qualification

Run this live procedure only with explicit authorization and an isolated owner-only config
and credential root. Do not paste command output into an issue or PR.

1. Configure the DCR profile above and export its 32-byte padded-base64 credential key.
2. Run `mecated mcp login connector`; record whether explicit consent appeared, but never
   record the URL, code, state, registration response, client ID, or token.
3. Start local `mecatui` with the same settings and key. Invoke only one harmless discovered
   read-only tool and record its name and safe success category.
4. Restart `mecatui` before access-token expiry and invoke the same tool without another login.
5. After expiry, reconnect and confirm login-required, no refresh request, and no browser launch.
6. Run `mecated mcp login connector` explicitly, confirm registration reuse, restart `mecatui`,
   and invoke the same harmless tool once more.

Record only:

```text
canonical_resource: <origin/path, no query>
issuer_origin: <origin only>
explicit_consent: true|false
harmless_tool_name: <name only>
initial_result: success|failure:<safe-category>
restart_reuse_before_expiry: true|false
expiry_result: login-required|failure:<safe-category>
refresh_attempted: false
browser_launched_on_startup_or_expiry: false
explicit_relogin_reused_registration: true|false
relogin_result: success|failure:<safe-category>
```

Exclude OAuth and registration secrets, authorization URLs, callback values, raw provider
errors, headers, credential-store contents, and screenshots containing any of them.

If a valid ready DCR profile's intentional registration binding changes — for example its
issuer, principal, scopes, or resource — run `mecated mcp login SERVER
--reset-dcr-registration`; plain login cannot replace that registration. Pending identity drift
is reported as pending-identity-mismatch and cannot reset or retry: restore the matching profile,
principal, canonical resource, and exact issuer before running `--retry-dcr-registration`.

For corrupt, noncanonical, unsupported, or grant-mismatched persisted registration state,
preserve the records and configuration: do not edit, delete, or rename them. Contact the
deployment operator or support team with only the server name and redacted command error.
Never send credential contents, OAuth URLs, client IDs, tokens, keys, or a raw response. Reset
and retry cannot bypass this state. For non-DCR profiles, run login again after intentional
identity changes. To roll back, replace the whole profile with `static_bearer` or `none` and
restart.

## Environment-backed credentials

For Kubernetes and other managed deployments, use an environment-backed OAuth
record. Provision the opaque record and its secret outside Mecatl, inject it
into the process, and restart after rotation. The environment reader is
read-only; `mecated mcp login` cannot populate or update it.

For global OAuth profiles, `mecak8s` does not open a browser and uses a
Kubernetes Secret-backed credential. Broker OAuth instead uses an external
browser to complete a session enrollment; its preregistered client secret, when
needed, is still a Kubernetes Secret. Agent-facing shells receive a scrubbed
environment so MCP/provider credentials are not exposed through Shell. See the
[Kubernetes deployment guide](/building/deployment/mecak8s.md) for the Secret
wiring.

## ToolHive broker OAuth

In `mecak8s` broker mode, each session starts one opaque enrollment, and
ToolHive authorizes the configured protected upstreams sequentially. ToolHive
owns upstream callback state, code exchange, refresh, and provider-specific
injection, so a token for one backend is not used for another. Mecatl discovers
every protected backend and freezes the complete catalog only after successful
enrollment.

Static declarations appear as placeholders before authentication. Calling one
starts authorization, performs authenticated discovery, and retries the parked
call. Live metadata replaces declared placeholders; undeclared tools remain
hidden. A successful pre-prompt enrollment instead loads the complete
authenticated catalog. Start a new session to discover metadata changes.

A process-private secret protects token exchange between Mecatl and ToolHive. It
is excluded from browser flows, logs, snapshots, and upstream calls. Failed
enrollment exposes no partial protected catalog.

A model switch creates a new session and therefore a new broker attachment. The
new session does not inherit the old session's enrollment or authorization;
protected backends may need to be enrolled again.

A protected upstream can use `client.mode: dcr` to let ToolHive register a
client from an HTTPS RFC 8414 discovery document. DCR requires an explicit
OAuth2 upstream, is operator-only, and rejects insecure HTTP and private-IP
overrides. See the [mecak8s deployment guide](/building/deployment/mecak8s.md)
for the Helm values.

Mecatl controls reveal only the enrollment reference, aggregate state, service
count, and temporary presentation URL. They omit upstream names, OAuth codes,
endpoints, and tokens. Providers return to ToolHive at
`/v1/mcp/broker/oauth/callback`; ToolHive then completes at the configured
Mecatl callback path. Route both paths to the same listener. See the
[Kubernetes deployment guide](/building/deployment/mecak8s.md) for Helm
configuration.

Pending broker enrollment is process-local. After a Mecatl restart, start a new
enrollment even if ToolHive retained its upstream tokens in Redis. Broker mode
requires a single replica. The chart enforces `replicaCount: 1` and the
`Recreate` strategy, so broker mode does not provide high availability.

The legacy bearer path is simpler for a server that does not need OAuth:

```sh
export MCP_GITHUB_TOKEN='value-from-your-secret-manager'
mecated serve --mcp-server github=https://mcp.example.com/github
```

The token is read at startup, sent as an Authorization bearer header, and never
logged. Token-bearing URLs must use HTTPS, except for loopback HTTP endpoints.
Server names are case-insensitively unique and must be safe for the derived
environment-variable name.

## Runtime behavior and limitations

- Global-profile serving, ACP, `mecatequi`, and `mecak8s` never open a browser.
  Authorize global profiles locally beforehand or provision an environment
  credential. A browser may instead complete an already-started ToolHive broker
  enrollment externally; `mecak8s` does not launch that browser.
- Direct DCR and ACP cannot provide OAuth profiles or install or drive
  authorization. An operator may configure and authorize a named global
  direct-DCR profile; ACP sessions may then invoke its shared tools under
  ordinary permissions.
- Client-provided per-session MCP and inline agent MCP servers cannot provide
  OAuth profiles. The configured ToolHive broker is the exception: it owns its
  configured multi-upstream OAuth chain, while Mecatl exposes only the aggregate
  enrollment.
- A configured profile that is unreachable or malformed is a deployment error;
  it does not silently become an unauthenticated server.
- OAuth credentials authenticate the MCP connection. They do not grant the model
  permission to call a tool: every namespaced MCP tool still passes through the
  ordinary permission policy and audit path.
- OAuth supports RFC 9728 metadata with one exact resource and authorization
  server, and S256. Preregistered confidential clients remain
  Basic-authenticated; direct DCR clients use the public `none` method with no
  refresh. RFC 9207 issuer validation follows authorization-server metadata: if
  the server advertises `authorization_response_iss_parameter_supported`, its
  callback must include the matching `iss`; otherwise `iss` may be omitted, but
  any supplied issuer must still match.
- A connection drop can trigger one bounded reconnect and retry. A
  server-declared tool failure is not replayed automatically because the call
  may have mutated remote state. The startup tool catalog is retained across
  reconnects; changed remote tool lists take effect after the next Mecatl
  process start.
- Treat the local credential store and configuration backups as sensitive. Keep
  roots owner-only and use your deployment's secret manager for rotation.

For MCP tool discovery, namespacing, permissions, reconnect behavior, resources,
and prompts, see [MCP client](/building/what-you-get/mcp-client.md). For the
complete operator profile rules, see the
[configuration reference](/reference/configuration.md#mcp).

## Next steps

- [Caller identity and OIDC](./caller-identity.md)
- [Mecatl deployment choices](/building/getting-started/deployment-decision.md)
- [MCP client](/building/what-you-get/mcp-client.md)
