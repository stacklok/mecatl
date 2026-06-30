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
| 4. Guardrails | [guardrails.md](usage/guardrails.md) |
| 5. Model routing (slots, aliases, router) | [model-routing.md](usage/model-routing.md) |
| 6. Workspace trust & posture | [workspace-trust.md](usage/workspace-trust.md) |
| 7. Skills, soul, user model | [skills-soul-usermodel.md](usage/skills-soul-usermodel.md) |
| 8. mecak8s (Kubernetes-native agent) | [mecak8s.md](usage/mecak8s.md) |
| 9. The gRPC API | [grpc-api.md](usage/grpc-api.md) |
| 10. The HTTP / SSE API | [http-sse-api.md](usage/http-sse-api.md) |
| 11. Configuration (settings, stores, leases) | [configuration.md](usage/configuration.md) |
| 12. Permissions | [permissions-config.md](usage/permissions-config.md) |
| 13. Hooks | [hooks.md](usage/hooks.md) |
| 14. OpenAI & compatible endpoints | [openai-compatible.md](usage/openai-compatible.md) |
| 15. Running mecatequi from GitHub Actions | [mecatequi-ci.md](usage/mecatequi-ci.md) |
| 16. Live e2e suite | [e2e.md](usage/e2e.md) |
| 17. Troubleshooting / FAQ | [troubleshooting.md](usage/troubleshooting.md) |

## Scheduled tasks

`mecated` and `mecak8s` can run scheduled agent fires autonomously (issue #189,
[ADR 0059](adr/0059-scheduled-tasks.md)) behind `--scheduler`:

```sh
mecated --store-dir ./state --scheduler --scheduler-tick-interval 30s
mecak8s --redis-url redis://... --scheduler   # multi-replica
```

Flags:

| Flag | Default | Description |
|---|---|---|
| `--scheduler` | false | Enable the in-process scheduler tick loop. Requires a store that exposes a `ScheduleStore` (jsonlstore via `--store-dir`, or redisstore via `--redis-url`). |
| `--scheduler-tick-interval` | 30s | How often the tick loop polls `ScheduleStore.Due`. |
| `--scheduler-min-interval` | 0 (off) | The frequency floor enforced at schedule-save time (a schedule tighter than this is rejected). |
| `--scheduler-max-concurrent-fires` | 4 | Bounds the per-tick fire fan-out. |

Schedules are saved via the `ScheduleService` gRPC/HTTP API (Phase 2). A YAML
`schedules:` block in `settings.yaml` (Phase 2b) and a `mecatui /schedule`
command (Phase 3) are planned. For now, schedules are seeded programmatically
(e.g. by a test or an operator script writing to the store).

A scheduled fire mints a fresh `sched--` top-level session per fire with
subagent-grade defaults (bounded turn/token budgets, read-leaning posture unless
`mutating: true` is set on the schedule, headless ask model). The at-most-once
firing semantics mean a crash mid-fire skips the slot — a recurring schedule
self-heals via the fire-once-now misfire policy; a one-shot can be lost.
