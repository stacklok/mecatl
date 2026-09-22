---
sidebar_position: 3
title: Connect to a server
description:
  Run mecated separately and connect mecatui to local or remote Mecatl servers.
---

# Connect to a server

`mecatui` can connect to a separately running `mecated` or `mecak8s` server.
This separates the terminal client from the process that owns the workspace,
model access, session storage, and permissions.

Start with both processes on one machine. The same connection model applies when
an operator gives you a remote address and credentials.

## Prerequisites

You need:

- `mecatui` and `mecated` installed;
- an API key for Anthropic, OpenAI, or OpenRouter; and
- a local project directory that you trust.

## Start the server

Open a terminal for the server. Change to the project that it will use as its
workspace. Set `ANTHROPIC_API_KEY`, `OPENAI_API_KEY`, or `OPENROUTER_API_KEY`
for your provider, then start `mecated`:

```sh title="Terminal 1: server"
cd <PROJECT_DIRECTORY>
export <PROVIDER_API_KEY>="<API_KEY>"
mecated serve --workspace "$PWD"
```

Replace `<PROVIDER_API_KEY>` with the variable for your provider.

Keep this terminal open. It displays server logs and continues running until you
stop it with `ctrl+c`.

The server listens for gRPC connections on `127.0.0.1:8080`. Its loopback-only,
single-user defaults do not require TLS or authentication.

## Connect the client

Open a second terminal and connect `mecatui`:

```sh title="Terminal 2: client"
mecatui connect 127.0.0.1:8080
```

The TUI header should show `127.0.0.1:8080`. Enter the project-inspection
request from the [local tutorial](./getting-started.md#inspect-the-project) to
confirm that the server can use its workspace.

In connect mode, the server owns the workspace. `mecatui connect` does not use
your client's current directory or local provider keys, and it rejects
`--workspace`.

## Connect to a remote deployment

Ask the server operator for:

- the server address;
- the required authentication method; and
- a CA bundle if the server uses a private certificate authority.

Verified TLS is automatic for non-loopback addresses. To use a static bearer
token and private CA:

```sh
export MECATL_AUTH_TOKEN="<MECATL_AUTH_TOKEN>"
mecatui connect mecated.example.com:443 \
  --tls-ca <PATH_TO_SERVER_CA> \
  --auth-token "$MECATL_AUTH_TOKEN"
```

Authentication identifies the caller. TLS protects the connection and verifies
the server. A shared authenticated server is not a tenant-isolation boundary;
callers can still access sessions that its authorization policy permits.

## Sign in with OIDC

When the server publishes its sign-in configuration, enroll once and then
connect:

```sh
mecatui login mecated.example.com:443
mecatui connect mecated.example.com:443
```

Login opens an Authorization Code with PKCE flow in your browser and stores the
credentials for that server. `connect` never opens a browser. Use `--no-browser`
with `login` on a headless host, then open the printed URL from a workstation
that can reach port `18473` on the login host, usually through SSH port
forwarding.

Login uses the system keyring on macOS. On Linux, it uses an available Secret
Service or owner-only plaintext files on a headless host. Use
`--credential-store` when you need to choose the storage explicitly.

If the server does not publish discovery metadata, its operator must also give
you the issuer, client ID, and audience:

```sh
mecatui login mecated.example.com:443 \
  --issuer https://id.example.com \
  --client-id mecatui \
  --audience mecatl
```

Run `mecatui logout mecated.example.com:443` to remove the saved enrollment. Use
`/connect` inside the TUI to choose another saved server.

## Manage local provider credentials

Remote enrollment and embedded provider credentials are separate. `mecatui login
ADDRESS` authenticates this client to a remote server; it cannot configure that
server's providers. For an embedded server, use `mecatui providers setup` or the
named provider commands. Custom OIDC login supports `--no-browser` when no
browser is available.

Provider definitions and credential custody are operator configuration. API keys
come from the environment or an operator-managed file; keep secrets out of
command arguments, settings, prompts, and logs. See [Choose models and
providers](/features/sessions/choose-models.md#set-up-a-local-provider) and [Run mecated
standalone](/operating/mecated.md#configure-providers).

ToolHive has a separate external lifecycle and owns its LLM credentials. Use
`thv llm` tooling for ToolHive setup; ToolHive MCP discovery and manual OpenAI
Codex authentication are separate workflows.

## Next steps

- [Try Mecatl on Kubernetes](/operating/kubernetes.md) to connect
  the same client to a local `mecak8s` deployment.
- [Run mecated standalone](/operating/mecated.md) to configure
  persistence, providers, TLS, authentication, and observability.
- [Troubleshoot mecatui](./troubleshooting.md) if startup, login, or connection
  fails.
