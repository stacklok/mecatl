---
sidebar_position: 2
title: Get started
---

# Get started with mecatui

Build the binaries through the Taskfile:

```sh
task build
```

Launch in the checkout you want the agent to use. `--workspace` defaults to the current directory, but making it explicit avoids surprises:

```sh
OPENAI_API_KEY=sk-... bin/mecatui --workspace "$PWD"
```

Embedded mecatui detects Anthropic, OpenAI, or OpenRouter credentials from the environment. With no API key, use the offline provider deliberately:

```sh
bin/mecatui --mock --workspace "$PWD"
```

## Start with a task

Type a request and press `enter`, or submit one initial task at launch. The TUI stays open for follow-ups.

```sh
bin/mecatui --workspace "$PWD" \
  --prompt "Summarize the failing tests and suggest the smallest fix"
```

For a longer brief, use `--prompt-file path`; it can be combined with `--prompt`. A seed prompt runs once, even if you later change model or clear the conversation.

## Connect to a server

Use `connect` only for a server that is already running:

```sh
bin/mecatui connect 127.0.0.1:8080 --workspace "$PWD"
```

Remote mode does not use local provider keys or embedded-server flags. The workspace path is interpreted by the server, so it must name a workspace available **there**, not necessarily on your terminal host. See [Connect to a server](./remote-servers.md) for ownership boundaries, authenticated and TLS connections, and operator next steps.

## Trust the workspace intentionally

A workspace can contain instructions and files that influence the agent. Start in a repository you recognize, review permission requests before approving them, and treat prompts, tool output, and fetched content as untrusted input. In remote mode, the server's policy and trust configuration control what the agent can access; do not assume local settings apply.

Next, learn how to [work while a run is streaming](./using-the-tui.md), [use everyday keys](./keybindings.md), and [resume the chat later](./sessions.md).
