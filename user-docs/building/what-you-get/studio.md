---
sidebar_position: 11
title: Using Studio
---

# Using Studio

Studio is mecatl's web workspace: a browser client for the daemon with five
surfaces on one rail — **Chats**, **Scheduled**, **Skills**, **Memory**, and
**Settings**. It is a client like `mecatui`: the daemon owns every record, and
Studio reads and writes the daemon's state rather than keeping its own.

## Starting it

Managed mode (the default) supervises a `mecated` from your checkout:

```sh
task build        # produces bin/mecated
task studio:dev   # controller + web server; open http://localhost:3000
```

The controller spawns `mecated` on a random loopback port with a generated
bearer token, resolves the workspace to the repo root, and restarts the daemon
when you change its configuration. `task studio:stop` tears everything down.

External mode points Studio at a daemon you run elsewhere:

```sh
MECATL_BASE_URL=https://mecated.internal:8081 \
MECATL_AUTH_TOKEN=... \
MECATL_WORKSPACE=/srv/workspace \
npm run start
```

In external mode there is no local controller: provider, model-router, and
MCP-gateway settings show as owned by the deployment.

Studio is **daemon-only**. If the daemon is unreachable you get an offline
banner naming the fix — never simulated content.

Studio talks to the daemon through the [TypeScript SDK](../../reference/typescript-sdk-api/core.md)
(`@stacklok-oss/mecatl-sdk`) over a same-origin proxy that holds the bearer
token on the server; the browser never sees a daemon address or credential.

## What each surface does

- **Chats** — the daemon's session store, live. The sidebar is the session
  inventory (a chat renamed or deleted here is renamed or deleted for every
  client); opening a chat reads its authoritative transcript; a new chat
  creates its daemon session on the first message. Streaming shows tool calls,
  reasoning, delegation badges when the run hands work to subagents or teams,
  and permission asks with three-way verdicts (allow once / always / deny). A
  failed run renders as failed, with a retry.
- **Scheduled** — the schedule registry: create and edit schedules (cron with
  timezone, or one-shot), pause/resume/fire, and audit each schedule's fire
  history down to the per-fire session transcript. Write-capable schedules
  require an explicit opt-in; the default posture is read-only plan mode.
- **Skills** — the daemon's resolved skill inventory (name, summary,
  provenance). Read-only today; authoring is a follow-up.
- **Memory** — the user model: durable facts the agent has stored about you.
  Read-only by design — the agent curates memory through injection-scanned
  tool calls, so Studio never offers an editor.
- **Settings** — appearance and notifications, plus (managed mode) the
  provider status, the semantic model router, and the MCP gateway connection
  (bearer token or OAuth). Credentials are never typed into Studio: `mecated`
  reads them from `~/.config/mecatl/auth.yaml`.

## Environment variables

| Variable | Meaning |
| --- | --- |
| `MECATL_BASE_URL` | External daemon base URL; presence selects external mode |
| `MECATL_AUTH_TOKEN` | Bearer for the external daemon (server-side only) |
| `MECATL_WORKSPACE` | Display-only label of the deployment's workspace in external mode; the daemon assigns session placement itself |
| `MECATL_STUDIO_PUBLIC_ORIGIN` | Comma-separated origins Studio is served from (CSRF gate) |
| `MECATL_STUDIO_ORIGINS` | Controller's Origin allowlist (managed mode) |
| `MECATL_STUDIO_PROVIDER` | Managed provider: `mock`, `openrouter`, or `toolhive` |
| `MECATL_ALLOW_INSECURE_LOOPBACK_MCP` | `1` permits a loopback-HTTP MCP gateway |

## Limits worth knowing

- Studio cannot re-attach live to a run it did not start (a scheduled fire in
  progress, another client's run): the live tail is gRPC-only today. It shows
  the running state and reads the transcript when the run ends.
- Config writes in managed mode restart the daemon, which ends in-flight runs.
- There is no cost display: the daemon accounts tokens, not currency.
