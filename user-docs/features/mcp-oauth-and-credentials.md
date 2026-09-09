---
sidebar_position: 5
title: MCP OAuth and credentials
description: Configure MCP OAuth profiles and protect the credentials they use.
---

# MCP OAuth and credentials

Mecatl can authenticate configured MCP servers with no credential in the server
URL or in model-visible tool arguments. This page covers operator authentication
profiles, OAuth login, credential storage, and rotation. For MCP transport,
tool discovery, namespacing, reconnects, resources, prompts, and typed results,
see the [MCP client guide](/building/what-you-get/mcp-client.md).

MCP profiles are operator configuration, not project configuration. A project
`.mecatl/settings.yaml` cannot install or weaken a credential profile. The
available profile modes are `none`, `static_bearer`, and `oauth`.

## Availability

Global MCP authentication profiles are supported by `mecated`, `mecatequi`,
`mecak8s`, and mecatui's embedded server. They are operator configuration, not
project configuration. A project `.mecatl/settings.yaml` cannot install or
weaken an MCP credential profile.

A server configured with `--mcp-server name=URL` can also use the legacy
`MCP_<NAME>_TOKEN` bearer-token convention. Operator `mcp.servers` profiles are
the preferred path when OAuth, rotation, or deployment-managed credentials are
needed.

## Configure a profile

Put the profile in the operator-tier `settings.yaml`. Secret-bearing fields are
environment-variable names, not secret values. A minimal shape is:

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

Use the generated [configuration reference](/reference/configuration.md)
for the complete strict schema and exact field names. Keep the settings file
owner-only and validate it before serving:

```console
umask 077
mecated config validate --file "$HOME/.config/mecatl/settings.yaml"
```

The local OAuth store requires an absolute root and a canonical base64-encoded
32-byte encryption key. Generate the key outside YAML and provide it through the
named environment variable:

```console
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

```console
mecated mcp login github
mecated mcp login github --no-browser
```

The normal command opens the authorization flow. `--no-browser` prints the
authorization URL for a separate browser or for a headless operator. Use
`--permission-config` to select trusted operator settings files; it does not
carry an OAuth value:

```console
mecated mcp login github \
  --permission-config /etc/mecatl/settings.yaml
```

After login, start or restart the server and verify that the namespaced
`mcp__github__*` tools appear. Serving restores the encrypted credential without
another browser interaction. Access-token refresh is lazy, and refresh-token
rotation is persisted by the local store so the next restart remains warm.

If the profile's identity metadata changes — issuer, client, principal, scopes,
or resource — run login again. To roll back, replace the whole profile with
`static_bearer` or `none` and restart.

## Environment-backed credentials

For Kubernetes and other managed deployments, use an environment-backed OAuth
record. Provision the opaque record and its secret outside Mecatl, inject it
into the process, and restart after rotation. The environment reader is
read-only; `mecated mcp login` cannot populate or update it.

For global OAuth profiles, `mecak8s` does not open a browser and uses a Kubernetes
Secret-backed credential. Broker OAuth instead uses an external browser to complete a
session enrollment; its preregistered client secret, when needed, is still a Kubernetes
Secret. Agent-facing shells receive a scrubbed environment so MCP/provider credentials are
not exposed through Shell. See the [Kubernetes deployment guide](/building/deployment/mecak8s.md)
for the Secret wiring.

## ToolHive broker OAuth

For `mecak8s` broker mode, OAuth is not an environment-backed per-server credential.
One session starts one opaque enrollment, and ToolHive drives the configured protected
upstreams sequentially. ToolHive owns upstream callback state, code exchange, refresh, and
provider-specific injection; a token for one backend is not used for another. Mecatl retains
the session and pre-prompt enrollment boundary, then strictly discovers every protected backend
and freezes the complete catalogue only after success. Static declarations are visible before
connection as pre-authentication placeholders: calling one starts the same opaque ToolHive bundle
authorization and, after success, makes the declared tools usable in that session. This lazy path
does not discover undeclared tools, including others on the same backend. Successful pre-prompt
enrollment instead replaces every placeholder with that session's complete authenticated
catalogue: live discovery controls membership, descriptions, input schemas, and read-only hints,
so an omitted declaration disappears and a newly discovered tool appears. That catalogue remains
fixed for the session; start a fresh session to discover changed metadata. The generated client
between mecatl and ToolHive is confidential: ToolHive stores
only its hash, while mecatl uses the process-private raw secret solely for HTTP-Basic token
exchange and refresh. It is never included in the browser flow, controls, logs, snapshots, or
upstream calls. A failed enrollment exposes no partial protected catalogue.

A model switch creates a new session and therefore a new broker attachment. The
new session does not inherit the old session's enrollment or authorization;
protected backends may need to be enrolled again.

A protected broker upstream may instead use `client.mode: dcr`. This asks
ToolHive to dynamically register the upstream client from the configured HTTPS
RFC 8414 discovery document, so no preregistered client secret or client-ID
metadata document is needed. The upstream authorization server mints that
client identity and ToolHive persists its registration state in the broker
store. DCR requires an explicit OAuth2 upstream, remains operator-only
configuration, and does not permit an insecure-HTTP or private-IP override.
See the [mecak8s deployment guide](/building/deployment/mecak8s.md) for the
Helm value shape.

Mecatl controls reveal only the enrollment reference, aggregate state, configured-service count,
and temporary presentation URL. They never reveal an upstream name, OAuth state/code, endpoint,
or access/refresh token. The broker origin serves two callback roles: providers return to
ToolHive's fixed `/v1/mcp/broker/oauth/callback` route, then ToolHive completes at the configured
mecatl callback URL. Route the complete `/v1/mcp/broker/` prefix and the final callback path to
the same listener. See the [Kubernetes deployment guide](/building/deployment/mecak8s.md) for
Helm configuration.

Broker session attachments and outer callback correlation remain process-local. ToolHive's
configured Redis storage can preserve its inner upstream authorization/token records, but a
mecatl restart cannot correlate a persisted pending aggregate back to that inner operation: it
discards the old pending correlation and starts a fresh enrollment rather than recovering it.
The mode is not safe behind the default multi-replica `mecak8s` Service until affinity or durable
outer broker routing is available.

The legacy bearer path is simpler for a server that does not need OAuth:

```console
export MCP_GITHUB_TOKEN='value-from-your-secret-manager'
mecated serve --mcp-server github=https://mcp.example.com/github
```

The token is read at startup, sent as an Authorization bearer header, and never
logged. Token-bearing URLs must use HTTPS, except for loopback HTTP endpoints.
Server names are case-insensitively unique and must be safe for the derived
environment-variable name.

## Runtime behavior and limitations

- Global-profile serving, ACP, `mecatequi`, and `mecak8s` never open a browser.
  Authorize global profiles locally beforehand or provision an environment credential.
  A browser may instead complete an already-started ToolHive broker enrollment externally;
  `mecak8s` does not launch that browser.
- DCR and ACP cannot provide OAuth profiles or install/drive authorization. After
  an operator authorizes a global profile, ACP sessions may invoke its shared
  tools under ordinary permissions.
- Client-provided per-session MCP and inline agent MCP servers cannot provide OAuth
  profiles. The configured ToolHive broker is the exception: it owns its configured
  multi-upstream OAuth chain, while mecatl exposes only the aggregate enrollment.
- A configured profile that is unreachable or malformed is a deployment error;
  it does not silently become an unauthenticated server.
- OAuth credentials authenticate the MCP connection. They do not grant the
  model permission to call a tool: every namespaced MCP tool still passes through
  the ordinary permission policy and audit path.
- OAuth remains constrained to RFC 9728 metadata with one exact
  resource/authorization server, S256, and Basic-authenticated confidential clients.
  RFC 9207 issuer validation follows authorization-server metadata: if the server
  advertises `authorization_response_iss_parameter_supported`, its callback must
  include the matching `iss`; otherwise `iss` may be omitted, but any supplied
  issuer must still match.
- A connection drop can trigger one bounded reconnect and retry. A server-declared
  tool failure is not replayed automatically because the call may have mutated
  remote state. The startup tool catalog is retained across reconnects; changed
  remote tool lists take effect after the next Mecatl process start.
- Treat the local credential store and configuration backups as sensitive. Keep
  roots owner-only and use your deployment's secret manager for rotation.

For MCP tool discovery, namespacing, permissions, reconnect behavior, resources,
and prompts, see [MCP client](/building/what-you-get/mcp-client.md). For the complete
operator profile rules, see the [configuration reference](/reference/configuration.md#mcp).

## Next steps

- [Caller identity and OIDC](./caller-identity.md)
- [Mecatl deployment choices](/building/getting-started/deployment-decision.md)
- [MCP client](/building/what-you-get/mcp-client.md)
- [Capability and deployment matrix](./capability-matrix.md)
