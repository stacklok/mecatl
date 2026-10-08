---
sidebar_position: 3
title: Connect to a remote deployment
description: Connect mecatui to a Mecatl service using details from your operator.
---

# Connect to a remote deployment

Use `mecatui` to work with an agent on a service your operator runs. The server
owns the workspace, model access, session storage, and permissions. Your client
provides the terminal interface. This separation is part of Mecatl's
[cloud-native architecture](/cloud-native-harness.md).

## Before you connect

[Install `mecatui`](./installation.md), then ask your operator for the server
address, authentication method, and a CA bundle if the service uses a private
certificate authority. For OIDC sign-in without server discovery, also obtain
the issuer, client ID, and audience.

In connect mode, the server uses its own workspace and provider credentials.
Your client's current directory and local API keys do not configure the server;
`mecatui connect` rejects `--workspace`. The operator must grant the access your
work needs on the server.

## Connect with a bearer token

Verified TLS is automatic for non-loopback addresses. For a service with a
static bearer token and private CA, run:

```sh
export MECATL_AUTH_TOKEN="<MECATL_AUTH_TOKEN>"
mecatui connect mecated.example.com:443 \
  --tls-ca <PATH_TO_SERVER_CA> \
  --auth-token "$MECATL_AUTH_TOKEN"
```

Omit `--tls-ca` when the certificate uses a CA already trusted by your system.
The TUI header shows the connected address. Send a short request to confirm the
service is available, then select or resume a session for your work.

Authentication identifies the caller and TLS verifies the service and protects
the connection. Shared deployments are intended for trusted teams; the server's
[ownership and authorization policy](/features/security-and-execution/caller-identity.md)
determines which sessions you can access.

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

If the gRPC server uses a private CA, pass `--server-tls-ca <PATH>` during login
to save that server-trust reference for later connects. This is separate from
login's issuer-only `--tls-ca`; an explicit `connect --tls-ca <PATH>` overrides
the saved server CA for that invocation.

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

## Work with the connected server

Use `/connect` to choose a saved server, `/models` to inspect its available
models, and the [session controls](./sessions.md) to return to earlier work.
The server advertises which capabilities are available. Ask your operator about
missing tools, workspace access, or deployment policy.

Local provider setup belongs to the [embedded-server workflow](/features/sessions/choose-models.md#set-up-a-local-provider).
Signing in to a remote deployment authenticates your client to that service.

## Next steps

- [Work in the TUI](./using-the-tui.md).
- [Manage sessions](./sessions.md).
- [Troubleshoot connections](./troubleshooting.md).

To run your own service, follow [Run mecated](/operating/mecated.md), or
[try Mecatl on Kubernetes](/operating/kubernetes.md).
