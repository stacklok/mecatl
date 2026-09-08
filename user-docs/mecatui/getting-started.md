---
sidebar_position: 2
title: Get started with mecatui
sidebar_label: Get started
description: Build and launch mecatui for an offline demo or an embedded local agent session.
---

# Get started with mecatui

Build the binaries through the Taskfile:

```sh
task build
```

Launch in the checkout you want the embedded server to use. `--workspace` is private embedded/operator configuration and defaults to the current directory:

```sh
OPENAI_API_KEY=sk-... bin/mecatui
```

Embedded mecatui detects Anthropic, OpenAI, or OpenRouter credentials from the environment. With no API key, use the offline provider deliberately:

```sh
bin/mecatui --mock
```

## Start with a task

Type a request and press `enter`, or submit one initial task at launch. The TUI stays open for follow-ups.

```sh
bin/mecatui --prompt "Summarize the failing tests and suggest the smallest fix"
```

For a longer brief, use `--prompt-file path`; it can be combined with `--prompt`. A seed prompt runs once, even if you later change model or clear the conversation.

## Connect to a server

Use `connect` only for a server that is already running:

```sh
bin/mecatui connect 127.0.0.1:8080
```

Remote mode does not use local provider keys, embedded-server flags, or a local workspace.
The server owns placement; `connect` rejects `--workspace` even on loopback. New sessions
bind the server default or no-FS, and `/worktrees` selects only server-advertised opaque
choices from the owned source session. See [Connect to a server](./remote-servers.md) for ownership boundaries, authenticated and TLS connections, and operator next steps.

## Trust the workspace intentionally

A workspace can contain instructions and files that influence the agent. Start in a repository you recognize, review permission requests before approving them, and treat prompts, tool output, and fetched content as untrusted input. In remote mode, the server's policy and trust configuration control what the agent can access; do not assume local settings apply.

Next, learn how to [work while a run is streaming](./using-the-tui.md), [use everyday keys](./keybindings.md), and [resume the chat later](./sessions.md).
