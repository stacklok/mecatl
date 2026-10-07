---
sidebar_position: 20
title: Run mecated standalone
description: Start a lightweight Mecatl server and connect a terminal client.
---

import ReleaseArchivesAndSource from
'../_partials/release-archives-and-source.mdx';

# Run mecated standalone

`mecated` runs Mecatl as a standalone service over gRPC and HTTP/SSE. It is a
lightweight starting point when you own one server and its local state. For a
shared team, [mecak8s](/operating/mecak8s.md) supplies Redis-backed state and
Kubernetes session coordination.

## Install mecated

Homebrew installs the `mecatui` client and the `mecated` server:

```sh
brew install stacklok/tap/mecatl
mecated --version
```

If you manage your environment with Conda, install the
[Mecatl package on Conda-forge](https://anaconda.org/conda-forge/mecatl) with
your preferred package manager:

|Package manager|Command|
|-|-|
|Conda|`conda install -c conda-forge mecatl`|
|Mamba|`mamba install -c conda-forge mecatl`|
|Pixi|`pixi add mecatl`|

<ReleaseArchivesAndSource />

## Prepare model access

Follow [provider setup](/features/sessions/choose-models.md) under the account
that runs the server. Then choose
[provider custody and storage](/operating/mecated/configure-providers-and-storage.md).

## Start the server

```sh
export ANTHROPIC_API_KEY="<ANTHROPIC_API_KEY>"
mecated serve --default-provider anthropic
```

A command word is required: `mecated serve` for the network daemon,
`mecated acp` for the ACP stdio mode. Bare `mecated` prints the command help and
exits with a usage error.

The minimal invocation starts a loopback-only server with in-memory sessions and
no authentication.

Default addresses:

|Listener|Default|
|-|-|
|gRPC|`127.0.0.1:8080`|
|HTTP/SSE|`127.0.0.1:8081`|
|Prometheus + admin|`127.0.0.1:9090`|

To retain sessions across restarts and authenticate local clients:

```sh
export MECATL_AUTH_TOKEN="<SERVER_BEARER_TOKEN>"
mecated serve \
  --store-dir ~/.local/share/mecatl/sessions \
  --posture auto
```

`--log-level` accepts `debug`, `info` (default), `warn`, or `error`. Startup
logs the binary version with `msg="mecated starting"`.

`--store-dir` enables local JSONL persistence. The path and its ancestors must
be physical directories, not symlinks. On macOS, use `/private/...` instead of a
path through the `/var` symlink. `--auth-token` requires the token on every
request and can also read `MECATL_AUTH_TOKEN`. `--posture auto` permits
unattended calls within configured permission rules while retaining child
prompt-injection protections. Configured Ask and Deny rules still apply; project
trust is a separate grant.

Before binding a non-loopback address, add TLS and caller authentication. See
[Secure and expose mecated](/operating/mecated/secure-and-expose.md).

### Connect and verify

Keep the server running, then open a second terminal:

```sh
mecatui connect 127.0.0.1:8080
```

The TUI header shows the connected address. Send a short request, such as "Reply
with a brief greeting," and confirm a response appears. The loopback connection
uses the server's model access and workspace. To work with project files, start
the server with `--workspace <PROJECT_DIRECTORY>`.

Before exposing this instance to remote clients,
[secure its listeners and caller access](/operating/mecated/secure-and-expose.md).
For a shared team, follow [Try Mecatl on Kubernetes](/operating/kubernetes.md).

## Server-owned session placement

`--workspace` sets the server's default workspace. Clients can request the
default or the `no-fs` profile, but cannot submit a path. `ListWorktrees`
returns short-lived selectors for `ClearSession` and `ForkSession`; selectors
expire when the server restarts. Mecatl stores the exact placement privately and
reattaches it before each run. See
[Execution environments](/features/security-and-execution/execution-environments.md)
for the shared placement and reattachment model.

## Find an operational task

<span id="configure-providers"></span>
<span id="operator-defined-providers"></span>
<span id="context-discovery-recovery"></span>
<span id="a-toolhive-managed-llm-gateway-no-api-key-needed"></span>
<span id="persistence"></span>
<span id="import-from-codex-or-claude-code"></span>

[Configure providers and durable storage](/operating/mecated/configure-providers-and-storage.md).

<span id="flag-reference"></span> <span id="server"></span>
<span id="session-state"></span> <span id="scheduled-tasks"></span>
<span id="llm-resilience"></span> <span id="provider-and-model"></span>
<span id="mcp"></span> <span id="skills"></span>
<span id="observability"></span>

[Look up server flags](/reference/server-cli.md).

<span id="caller-identity-oidc"></span> <span id="posture"></span>
<span id="guardrails"></span> <span id="the-trust-model"></span>
<span id="browsers-and-cors"></span>

[Secure listeners and caller access](/operating/mecated/secure-and-expose.md).

<span id="daemon-config-file-daemonyaml"></span>
<span id="offline-mock-providers-no-credentials"></span>
<span id="multi-replica"></span> <span id="operator-subcommands"></span>
<span id="graceful-shutdown"></span>

[Operate and recover the instance](/operating/mecated/operate-instance.md).

<span id="hosting-a-spawned-daemon"></span>
<span id="add-mcp-servers-per-session"></span>

[Host a private daemon from an application](/building/local-daemon.md).

## Next steps

- [Secure and expose the server](/operating/mecated/secure-and-expose.md).
- [Configure providers and storage](/operating/mecated/configure-providers-and-storage.md).
- [Operate the instance](/operating/mecated/operate-instance.md).

## Related information

- [Server CLI reference](/reference/server-cli.md) lists every registered server
  flag.
