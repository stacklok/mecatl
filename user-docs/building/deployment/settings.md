---
sidebar_position: 100
title: Configure Mecatl
sidebar_label: Settings guide
description:
  Choose the right settings file or flag for each Mecatl deployment shape.
---

# Configure Mecatl

Mecatl has separate configuration planes for server behavior, secrets, daemon
listeners, command-line overrides, and the `mecatui` client. Choose the plane
owned by the process that runs the agent; a terminal client connected to another
server cannot reconfigure that server.

## Configuration at a glance

|Plane|Practical location or input|Owner|What it configures|
|-|-|-|-|
|Operator settings|`$XDG_CONFIG_HOME/mecatl/settings.yaml` (normally `~/.config/mecatl/settings.yaml`)|Server operator|Providers and models, permissions, posture, MCP profiles, and other shared agent policy.|
|Secret authentication|`$XDG_CONFIG_HOME/mecatl/auth.yaml` (normally `~/.config/mecatl/auth.yaml`), or `--api-key-file`|Server operator|Provider API keys and the experimental Codex credential snapshot. Keep secrets out of `settings.yaml`.|
|Daemon topology|A chosen `daemon.yaml`, passed to `mecated serve --config PATH`|`mecated` operator|Listener addresses, TLS material, and rate limits. It is not automatically loaded and never contains the API bearer token.|
|CLI flags and environment|Invocation flags and documented environment variables|The process launcher|A deployment-specific override or one-run choice, such as workspace, provider/model, store, or transport.|
|mecatui client settings|`$XDG_CONFIG_HOME/mecatui/settings.yaml` (normally `~/.config/mecatui/settings.yaml`)|Local terminal user|Keybindings and status-line presentation only.|

The generated [configuration reference](/reference/configuration.md) is the
exhaustive schema, defaults, and tier table for the shared operator
`settings.yaml`. Use `mecated config init` to scaffold it and
`mecated config validate` to validate it without starting a server.

## Which settings apply?

|Running shape|Operator `settings.yaml` and `auth.yaml`|`daemon.yaml`|Agent/server flags|Client-only mecatui settings|Effective defaults|
|-|-|-|-|-|-|
|`mecated serve`|Read by the daemon at startup.|Available through `mecated serve --config PATH`.|Configure this daemon; applicable explicit flags override file values.|Not used unless a separate mecatui client connects.|Daemon defaults, then operator settings, then applicable explicit flags.|
|Bare `mecatui` (embedded server)|Read by the in-process server at startup.|Not used; it does not start a network daemon.|Embedded-server flags configure that local server.|Read locally for UI behavior.|Embedded-server defaults apply before operator settings; applicable embedded flags then override.|
|`mecatui connect ADDRESS`|Owned and read by the remote server, not the client.|Owned by the remote `mecated` operator.|Only connection/client flags apply; embedded-server flags are rejected.|Read locally for UI behavior.|The remote server is authoritative; local defaults and settings affect only the client UI and connection.|
|`mecatequi`|Read by the one-shot process at startup.|Not used; it has no listeners.|Configure that one run.|Not used.|Headless by default; use its own flags and operator settings for the single run.|
|`mecak8s`|Mount `settings.yaml` from an operator-controlled ConfigMap and `auth.yaml` from a read-only Secret; use documented Secret-projected environment variables for provider or MCP credentials.|Not used; Helm and Kubernetes own listener topology.|Helm values supply command arguments and environment to the `mecak8s` process.|Not used.|Kubernetes-native defaults: headless/`auto` posture, no-FS placement, pod listeners, Redis-backed durable state when `redis.endpoint` is configured, and Kubernetes Lease coordination. [Use the mecak8s deployment guide.](./mecak8s.md)|

For the daemon and Kubernetes operating details, see
[Run mecated standalone](./mecated.md) and
[Cloud-native k8s with mecak8s](./mecak8s.md). For one-shot CI ownership, see
[Single-shot CI with mecatequi](./mecatequi.md).

## Ownership and precedence

`settings.yaml` is the normal user-global operator file. It is not a secret
store: put custom-provider keys in `auth.yaml`, whose default path can be
replaced with `--api-key-file`. Both are read when the process starts, so
restart the server or one-shot runner after changing them.

A project can contribute only the settings that are allowed at project tier,
from `.mecatl/settings.yaml` or `.mecatl/settings.local.yaml` in its workspace.
Operator-only settings remain user-global; project authority is also subject to
the trust gate. Permission sources have their own ordered scopes, including
explicit `--permission-config` files. See
[Permissions and posture](/features/permissions-and-posture.md) for the
applicable trust and precedence rules.

For settings with a matching flag, an explicit applicable CLI value overrides
the file value. Environment variables and flags are command/root-specific: a
variable or flag documented for one executable or mode may be ignored or
rejected by another. `daemon.yaml` has a narrower, independent rule: defaults <
daemon file < explicit `mecated serve` flag. It controls topology only; use
`--auth-token` or `MECATL_AUTH_TOKEN` for the daemon API bearer. Consult the
command's `--help-all` and the generated reference for a setting's exact
precedence rather than assuming every flag applies to every binary.

## Configure provider credentials

Keep provider credentials in the process environment or in an owner-readable
`auth.yaml` file:

```yaml
providers:
  anthropic:
    api_key: <ANTHROPIC_API_KEY>
  openai:
    api_key: <OPENAI_API_KEY>
  openai-codex:
    oauth:
      access_token: <CHATGPT_CODEX_ACCESS_TOKEN>
      account_id: <ACCOUNT_ID> # Optional when present in the token.
      expires_at: 2026-09-30T12:00:00Z # Optional when present in the token.
  openrouter:
    api_key: <OPENROUTER_API_KEY>
  opencode:
    api_key: <OPENCODE_API_KEY>
```

For API-key providers, a matching environment variable takes precedence over the
file entry. `openai-codex` is file-only and accepts only the `oauth` mapping
shown above. The default path is `$XDG_CONFIG_HOME/mecatl/auth.yaml`, normally
`~/.config/mecatl/auth.yaml`; `--api-key-file` selects another path.

The parser reports unknown providers, fields, duplicate keys, and invalid
credential shapes without printing values. A missing conventional file is not an
error. A missing explicit `--api-key-file` path produces a warning. On Unix,
Mecatl also warns when group or other users can read the file. Use mode `0600`
on a shared host and restart the process after replacing a credential.

The experimental `openai-codex` provider captures one immutable token snapshot
at startup. It has no login or refresh flow. When both the token and file carry
an account or expiry claim, the values must agree and the earlier expiry wins.
Mode `0600` prevents access by other users, but another process running as the
same user can still read a known plaintext path. Use a dedicated
operating-system identity or a stronger sandbox when that residual risk is
unacceptable.

## Embedded and connected mecatui

Bare `mecatui` embeds a private server, so its local operator settings,
`auth.yaml`, and embedded-server flags determine provider availability,
workspace policy, storage, and permissions. `mecatui connect ADDRESS` only dials
an existing server: that remote operator remains authoritative for all of those
choices. Local settings cannot set a remote workspace, provider, model, or
policy; connection credentials authenticate to the server but do not configure
it.

The terminal client still reads its own `$XDG_CONFIG_HOME/mecatui/settings.yaml`
in both modes. Its UI settings do not alter server behavior. For keymaps, the
legacy `keymap:` in the shared Mecatl settings file is lowest priority, the
client file is next, and `mecatui --keymap` wins for that action. See
[Keybindings](/mecatui/keybindings.md),
[Customize mecatui](/mecatui/customization.md), and
[Connect to a server](/mecatui/remote-servers.md) for client and transport
details.

## Configure a specific concern

- [Choose models and providers](/features/choose-models.md) covers provider,
  model, and endpoint selection.
- [Permissions and posture](/features/permissions-and-posture.md) covers
  permission files, project trust, guardrails, and automation posture.
- [MCP client](/building/what-you-get/mcp-client.md) covers streaming-HTTP MCP
  servers and their authentication profiles.
- [Connect to a server](/mecatui/remote-servers.md) covers remote transport,
  TLS, and client authentication.
- [Keybindings](/mecatui/keybindings.md) covers the client-owned keymap schema
  and overrides.
- [Configuration reference](/reference/configuration.md) lists every
  `settings.yaml` key, type, default, and allowed tier.
