---
sidebar_position: 100
title: Configure Mecatl
sidebar_label: Settings guide
description:
  Choose the right settings file or flag for each way of running Mecatl.
---

# Configure Mecatl

Configure the process that runs the agent. A `mecatui` client connected to a
remote server cannot change that server's providers, permissions, storage, or
other operator settings.

Mecatl separates shared agent policy, credentials, daemon topology, command-line
overrides, and terminal UI preferences. Keep each setting in its own
configuration plane.

## Configuration at a glance

|Plane|Practical location or input|Owner|What it configures|
|-|-|-|-|
|Operator settings|`$XDG_CONFIG_HOME/mecatl/settings.yaml` (usually `~/.config/mecatl/settings.yaml`)|Server operator|Providers and models, permissions, posture, MCP profiles, and other shared agent policy.|
|Provider credentials|`$XDG_CONFIG_HOME/mecatl/auth.yaml` (usually `~/.config/mecatl/auth.yaml`), or `--api-key-file`|Server operator|Provider API keys and the experimental Codex credential snapshot.|
|Daemon topology|A chosen `daemon.yaml`, passed to `mecated serve --config PATH`|`mecated` operator|Listener addresses, TLS material, and rate limits. It is not automatically loaded and never contains the API bearer token.|
|CLI flags and environment|Invocation flags and documented environment variables|Process launcher|Deployment-specific overrides such as workspace, provider, model, store, or transport.|
|mecatui client settings|`$XDG_CONFIG_HOME/mecatui/settings.yaml` (usually `~/.config/mecatui/settings.yaml`)|Local terminal user|Keybindings and status-line presentation only.|

Use `mecated config init` to create `settings.yaml`, and use
`mecated config validate` to check it without starting the server. The generated
[configuration reference](/reference/configuration.md) lists the complete
schema, defaults, and allowed configuration tiers.

## Which settings apply?

|Deployment option|Operator `settings.yaml` and `auth.yaml`|`daemon.yaml`|Agent/server flags|Client-only mecatui settings|Effective defaults|
|-|-|-|-|-|-|
|`mecated serve`|Read at startup.|Available through `mecated serve --config PATH`.|Configure the daemon; applicable explicit flags override file values.|Used only by a separate client.|Daemon defaults, then operator settings, then applicable explicit flags.|
|Bare `mecatui`|Read by the embedded server at startup.|Not used.|Configure the embedded server.|Read locally.|Embedded-server defaults, operator settings, then applicable explicit flags.|
|`mecatui connect ADDRESS`|Read by the remote server.|Owned by the remote operator.|Only connection and client flags apply.|Read locally.|The remote server controls agent behavior; local settings control only the client and connection.|
|`mecatequi`|Read at startup.|Not used.|Configure the single run.|Not used.|Headless defaults, operator settings, then applicable explicit flags.|
|`mecak8s`|Mount `settings.yaml` from a ConfigMap and `auth.yaml` from a read-only Secret. You can also project provider and MCP credentials as documented environment variables.|Not used; Helm and Kubernetes own topology.|Helm values supply arguments and environment variables.|Not used.|Headless `auto` posture, no-filesystem placement, pod listeners, Redis state when configured, and Kubernetes Lease coordination.|

For the daemon and Kubernetes operating details, see
[Run mecated standalone](./mecated.md) and
[Cloud-native k8s with mecak8s](./mecak8s.md). For one-shot CI ownership, see
[Single-shot CI with mecatequi](./mecatequi.md).

## Ownership and precedence

`settings.yaml` is the user-global operator file. Put provider keys in
`auth.yaml`, and use `--api-key-file` to select a non-default credentials file.
The process reads both files at startup, so restart it after changing either
file.

A trusted project can contribute project-tier settings from
`.mecatl/settings.yaml` or `.mecatl/settings.local.yaml`. Operator-only settings
remain user-global. Permission sources have their own ordered scopes, including
explicit `--permission-config` files. See
[Permissions and posture](/features/security-and-execution/permissions-and-posture.md)
for the trust and precedence rules.

An applicable explicit CLI value overrides the corresponding file setting. Flags
and environment variables are command-specific, so check the command's
`--help-all` output before reusing an option with another executable or mode.

`daemon.yaml` uses its own precedence: defaults, then the daemon file, then an
explicit `mecated serve` flag. It controls listener topology, TLS, and rate
limits. Supply the daemon API bearer through `--auth-token` or
`MECATL_AUTH_TOKEN`.

## Configure the command runner

Use `command_runner.shell` to choose the interpreter for built-in local Shell
runners. An explicit `--shell` value overrides this setting, including
`--shell ""` to disable Shell. `--no-shell` always disables Shell.

The built-in main runner removes credential-shaped environment variables by
default. To make a specific external CLI credential available to main-session
Shell commands, list its environment variable name without its value:

```yaml
command_runner:
  shell: /bin/sh
  environment:
    inherit:
      - GH_TOKEN
```

The variable must already be present in the Mecatl process environment. The
setting grants ambient access to every permitted main-session Shell command, so
use a credential limited to the intended service. Shell commands must not
inspect, print, copy, write, or commit credential values.

Mecatl never restores its own provider, web-search, server, driver, MCP, or
other configured credential references. Read-only Subagents, Team members,
Parallel branches, and internal Git operations retain fully scrubbed
environments. A direct-write Subagent uses the main runner and receives the same
deliberate grants. Independent attenuation for direct-write children requires a
future same-workspace child runner; direct-write mode is not a child
credential-isolation boundary.

The setting applies to `mecated`, `mecak8s`, `mecatequi`, and the server
embedded by bare `mecatui`. A custom placement provider owns its complete
execution environment and is unchanged. Project `command_runner` blocks are
ignored with a value-free warning. See the
[configuration reference](/reference/configuration.md#command_runner) for the
complete schema.

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
credential formats without printing values. A missing conventional file is not
an error. A missing explicit `--api-key-file` path produces a warning. On Unix,
Mecatl also warns when group or other users can read the file. Use mode `0600`
on a shared host and restart the process after replacing a credential.

The experimental `openai-codex` provider reads one token snapshot at startup and
has no login or refresh flow. When both the token and file include an account or
expiry claim, the values must agree, and the earlier expiry applies. Mode `0600`
blocks other users, but another process running as the same user can still read
the plaintext file. Use a dedicated operating-system account or a stronger
sandbox when you need isolation from same-user processes.

## Embedded and connected mecatui

Bare `mecatui` embeds a private server. Its local operator settings,
credentials, and embedded-server flags control provider availability, workspace
policy, storage, and permissions.

`mecatui connect ADDRESS` connects to an existing server whose operator controls
those choices. Local connection credentials authenticate the client; they do not
configure the remote server.

The terminal client reads `$XDG_CONFIG_HOME/mecatui/settings.yaml` in both
modes. These UI settings do not alter server behavior. For keymaps, the client
file has the lowest priority and `mecatui --keymap` has the highest priority. See
[Keybindings](/mecatui/keybindings.md),
[Customize mecatui](/mecatui/customization.md), and
[Connect to a server](/mecatui/remote-servers.md) for client and transport
details.

## Next steps

- [Choose models and providers](/features/sessions/choose-models.md) covers
  provider, model, and endpoint selection.
- [Permissions and posture](/features/security-and-execution/permissions-and-posture.md)
  covers permission files, project trust, guardrails, and automation posture.
- [MCP client](/features/security-and-execution/mcp-client.md) covers
  streaming-HTTP MCP servers and their authentication profiles.
- [Connect to a server](/mecatui/remote-servers.md) covers remote transport,
  TLS, and client authentication.
- [Keybindings](/mecatui/keybindings.md) covers the client-owned keymap schema
  and overrides.
- [Configuration reference](/reference/configuration.md) lists every
  `settings.yaml` key, type, default, and allowed tier.
