---
sidebar_position: 11
title: Using Mecatl Studio
---

# Using Mecatl Studio

Mecatl Studio is the local **web** client for mecatl, alongside [mecatui](./mecatui.md) in the terminal. It runs on your machine, drives a `mecated` in your own checkout, and adds the surfaces that are awkward in a terminal: a chat transcript with tool-call and approval cards, and panels for the provider, MCP gateway, model routing, skills, memory, and scheduled tasks.

It is a client, not a second harness — it speaks the same HTTP/SSE API any external client uses, so nothing it shows is Studio-specific behavior.

## Starting it

Studio drives the `mecated` binary from this checkout, so build first:

```sh
task build
task studio:dev
```

That starts two processes — the web app on `http://localhost:3000` and a local controller on `127.0.0.1:8788` that spawns and supervises `mecated` — and opens a session whose workspace is the repo root. The controller chooses a free daemon port and protects it with a generated bearer token. `task studio:stop` stops all three; `task studio:status` reports whether it is up.

To keep the UI local while using an existing daemon, start Studio in external mode:

```sh
MECATL_BASE_URL=https://mecated.example.com \
MECATL_AUTH_TOKEN="$TOKEN" \
MECATL_WORKSPACE=/workspace \
MECATL_STUDIO_PUBLIC_ORIGIN=https://studio.example.com \
task studio:dev
```

External mode does not start or supervise `mecated`. Provider, model-router, and MCP configuration remain owned by that deployment, so Studio disables those mutation controls.

## Choosing a provider

Studio never accepts provider credentials in the browser. In managed mode, `mecated` reads its conventional `auth.yaml` file (normally `~/.config/mecatl/auth.yaml`). Select a provider before starting Studio:

```sh
MECATL_STUDIO_PROVIDER=openrouter task studio:dev
```

Put the key under `providers.openrouter.api_key` in `auth.yaml`. If the selected provider has no usable credential, startup fails with the exact auth-file path instead of silently falling back. With no explicit selection, Studio prefers a reachable ToolHive LLM gateway and otherwise uses the offline mock. External mode uses the provider already configured on the remote daemon.

:::note
A reachable gateway is not the same as a usable one. Studio's readiness probe asks the gateway for its model list, which can succeed while individual models fail — if turns error out with a provider 500, check that the specific model you pinned is served by a backend that answers, not merely that it appears in the model list.
:::

## The panels

| Panel | What it does |
| --- | --- |
| **Model Router** | Define 2–8 semantic categories, each with a description and a model. A small classifier picks the category for every unpinned delegation. When an operator policy is imported, editing is locked so router edits cannot clobber aliases, slots, and guardrails. |
| **MCP Gateway** | Connect a remote MCP gateway by browser OAuth or an existing token. Sign-in is bounded to 10 minutes, so a closed popup fails cleanly instead of hanging. |
| **Skills** | Lists the skills discovered in the workspace's `.mecatl/skills`. Discovery is project-scoped by design and never widens to your user-global skills. |
| **Memory** | Read-only view of the cross-project user-model index (keys and descriptions, never values), plus whether project memory is enabled. The daemon does not yet expose a project-memory listing endpoint. See [Memory](./memory.md). |
| **Schedules** | The oversight surface for [scheduled tasks](./scheduled-tasks.md): arm a schedule, edit one, see what is armed and when it next fires, read each past fire's outcome, and pause / resume / run-now / delete. |

The memory panel is deliberately read-only. Mecatl's memory tool calls are injection-scanned; a value typed into a text box would reach the model's turn-0 context without passing that check. Ask the agent to remember or forget something instead.

## Working in a task

- **Approvals** appear inline as cards — allow once, allow always, or deny.
- **Plan mode** is the toggle next to the composer, for thinking before edits.
- **Model** can be pinned per task, or left on the server default.
- **CSV attachments** up to 256 KB ride along with a prompt and are fenced as untrusted data, not instructions.
- **Session routing** shows which model each delegation ran on and why the classifier chose it — including when no route was recorded.

A schedule that fires while you are watching shows up as its own `sched--` session, so unattended work is visible in the same place as your own.

## Scheduling work

The Schedules panel manages the registry directly, so a schedule does not have to be authored in chat:

- **New schedule** takes a name, the prompt the unattended run is given, and either a cron cadence (with an IANA timezone and an optional total-fire cap) or a single future time. Mecatl's own `Schedule` tool writes to the same registry, so anything it arms appears in the list too.
- **Edit** changes a stored schedule in place and keeps its firing history — the fire count, the next fire, and past fires all survive. Settings the form has no control for, such as a provider selector set from the CLI, are preserved rather than dropped. The name is fixed: a schedule is addressed by name, so renaming would mean arming a second one and deleting the first.
- **Write access** is an explicit opt-in. A schedule that has not opted in runs in plan mode, and the daemon rejects a non-mutating schedule that asks for anything wider. Nobody is at the keyboard to answer an approval, so a fire in the ask-before-writing posture stops at the first prompt policy cannot resolve.
- **History** lists each fire with its stop reason, its error when it failed, and the id of the `sched--` session it ran as. A fire that has not reported a stop is still in flight and can be re-read on its own.

The daemon rejects a cadence tighter than its frequency floor (one minute by default), an invalid cron expression, a one-shot in the past, and a provider or model the deployment does not serve — the panel shows those refusals verbatim rather than guessing at them.

## See also

- [Using mecatui](./mecatui.md) — the terminal client.
- [MCP client](./mcp-client.md) — how mecatl discovers and calls MCP servers.
- [Scheduled tasks](./scheduled-tasks.md) — the durable, exactly-once scheduler behind the Schedules panel.
