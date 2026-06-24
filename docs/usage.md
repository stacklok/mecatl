# mecatl — Usage & Operator Guide

`mecatl` is a headless, agentic coding harness. It owns its own context
window, tool loop, permission policy and lifecycle hooks, and talks to OpenAI
(or any OpenAI-compatible `/v1/responses` endpoint), OpenRouter, or Anthropic
(the native Messages API). The server, `mecated`, exposes one agent run over
**gRPC** and **HTTP/SSE** concurrently — or, opt-in, over **ACP on stdio** for
an editor that spawned it.

> Security, up front: **the `mecated` API is UNAUTHENTICATED by default.** It
> exposes command and file execution against the configured workspace, so out
> of the box it is intended for **localhost, single-user** use — the default
> listen addresses bind the loopback interface (`127.0.0.1`). Binding a
> non-loopback address without protection exposes unauthenticated command/file
> execution to the network. Before any non-loopback bind, enable the built-in
> protections: bearer auth (`--auth-token` / `MECATL_AUTH_TOKEN`), TLS
> (`--tls-cert` / `--tls-key`), mutual TLS (`--client-ca`), and rate limiting
> (`--rate-limit` / `--rate-burst`) — see [mecated](usage/mecated.md).

---

## Table of contents

| Section | File |
| --- | --- |
| 1. Prerequisites & install | [install.md](usage/install.md) |
| 2. The 60-second demo | [quickstart.md](usage/quickstart.md) |
| 3. Running the server (`mecated`) | [mecated.md](usage/mecated.md) |
| 3. Guardrails | [guardrails.md](usage/guardrails.md) |
| 3. Model routing (slots, aliases, router) | [model-routing.md](usage/model-routing.md) |
| 3. Workspace trust & posture | [workspace-trust.md](usage/workspace-trust.md) |
| 3. Skills, soul, user model | [skills-soul-usermodel.md](usage/skills-soul-usermodel.md) |
| 3. mecak8s (Kubernetes-native agent) | [mecak8s.md](usage/mecak8s.md) |
| 4. The gRPC API | [grpc-api.md](usage/grpc-api.md) |
| 5. The HTTP / SSE API | [http-sse-api.md](usage/http-sse-api.md) |
| 6. Configuration (settings, stores, leases) | [configuration.md](usage/configuration.md) |
| 7. Permissions | [permissions-config.md](usage/permissions-config.md) |
| 8. Hooks | [hooks.md](usage/hooks.md) |
| 9. OpenAI & compatible endpoints | [openai-compatible.md](usage/openai-compatible.md) |
| 10. Running mecatequi from GitHub Actions | [mecatequi-ci.md](usage/mecatequi-ci.md) |
| 11. Live e2e suite | [e2e.md](usage/e2e.md) |
| 12. Troubleshooting / FAQ | [troubleshooting.md](usage/troubleshooting.md) |
