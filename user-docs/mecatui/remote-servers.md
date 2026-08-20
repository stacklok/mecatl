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

The workspace path in remote mode is evaluated on the server host. It must be an absolute path available to that server—for example, a path in its container or pod—not necessarily a checkout on the computer running the TUI.

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
  --auth-token "$MECATL_AUTH_TOKEN" --workspace /workspace/project
```

Authentication proves the caller's credential; TLS protects the connection and verifies the server. They are separate settings. Do not use `--insecure` except for controlled testing.

Caller identity is attribution, not tenant isolation: authenticated callers can still list and act on other callers' sessions. Do not treat a token-authenticated shared server as a tenancy boundary.

## Where to go next

Use [Getting started](./getting-started.md) for the local first-run path and [Sessions](./sessions.md) to browse remote or embedded history. Operators configuring a server should use [Run mecated standalone](/building/deployment/mecated.md), [gRPC and HTTP deployment](/building/deployment/grpc-http.md), or [mecak8s](/building/deployment/mecak8s.md), as appropriate.

For the exhaustive transport flag reference, see [`docs/tui.md`](https://github.com/stacklok/mecatl/blob/main/docs/tui.md#transport-commands).
