# ADR 0245 — Safe build diagnostics

- Status: Accepted
- Date: 2026-08-29
- Scope: shipped binaries, server identity API, and mecatui diagnostics

## Context

Bug reports need a build identity without copying connection details, credentials, workspace paths, session data, or model prompts. Operators also need an identity check that does not initialise a server or inspect durable state.

## Decision

Use one linker-stamped `internal/buildinfo.BuildID` for every shipped executable, with `dev` as its source-build default. Each executable accepts only exact top-level `--version` and exits before normal startup.

Expose the composed server build identity through authenticated `GetServerInfo` and `GET /v1/info`. The response carries `build_id`, `server_implementation`, and an optional `llm_provider_display_endpoint`. `server_implementation` is a stable composition-family label — currently `mecated`, `mecak8s`, or embedded `mecatui`; generic embeddings safely report `unknown`. It identifies neither a binary instance nor a deployment.

`GetServerInfoRequest.provider_id` and the HTTP `provider_id` query selector identify the caller's already-known active provider. Composition resolves that exact ID only through its already-built registry. Empty, repeated HTTP, or unknown selectors return no provider display endpoint; the API never falls back to a default provider, inspects sessions, discovers providers, or re-reads configuration. A provider with no configured endpoint (including an SDK default or mock) also reports unavailable.

The only sanctioned display-endpoint components are scheme, host, optional port, and an escaped, cleaned path. Userinfo, query, fragment, invalid UTF-8, control data, malformed values, and oversized values are omitted. These values are diagnostic display projections only, never reconnect targets or connection configuration. Mecatui separately projects its current remote connection endpoint from its dial target under the same rule; its embedded UNIX socket has no URL host and therefore reports unavailable unless a future embedded/local URL endpoint is already known without lookup.

This is an explicit privacy boundary: the identity response must never carry addresses except the sanctioned display-endpoint components above; it must never carry connection state, topology, arbitrary configuration, capabilities, authentication or TLS details, workspace paths, durable state, session data, prompts, credentials, or raw errors. Mecatui may produce a sanitized diagnostics snapshot from its local state plus this response; remote failures are represented only by fixed safe categories.

## Consequences

Release and image build paths must stamp the shared variable. Clients can identify mixed builds and composition families without learning deployment topology or secrets. The identity APIs are additive: an older gRPC server returns `UNIMPLEMENTED`, and an older HTTP server has no route (`404`); clients must treat absent `server_implementation` from an otherwise successful newer response as `unknown`. The provider selector is additive, so older servers safely return no provider display endpoint. Unavailable servers degrade to a safe category rather than an error body.

## See also

[Architecture overview](../architecture.md), [usage guide](../usage.md), and [ADR 0002](./0002-documentation-lifecycle.md).
