---
sidebar_position: 2
title: Get started with mecatui
sidebar_label: Get started
description: Install mecatui, then launch an offline demo or an embedded local agent session.
---

# Get started with mecatui

## Install

Homebrew installs `mecatui` and the `mecated` server it can embed:

```sh
brew install stacklok/tap/mecatl
```

For signed release archives and their verification, the container images, or a
source build, see [Install Mecatl](/install.md).

### Build from source

A checkout builds every supplied executable into `bin/`:

```sh
task build
```

Run an uninstalled source build with its path prefix — `bin/mecatui --mock` — or
put `mecated` and `mecatui` on your `PATH` with `task install`. The examples below
use the plain command name.

## Launch a local session

Launch in the checkout you want the embedded server to use. `--workspace` is private embedded/operator configuration and defaults to the current directory:

```sh
OPENAI_API_KEY=sk-... mecatui
```

Embedded mecatui detects Anthropic, OpenAI, or OpenRouter credentials from the environment. With no API key, use the offline provider deliberately:

```sh
mecatui --mock
```

## Start with a task

Type a request and press `enter`, or submit one initial task at launch. The TUI stays open for follow-ups.

```sh
mecatui --prompt "Summarize the failing tests and suggest the smallest fix"
```

For a longer brief, use `--prompt-file path`; it can be combined with `--prompt`. A seed prompt runs once, even if you later change model or clear the conversation.

## Connect to a server

Use `connect` only for a server that is already running:

```sh
mecatui connect 127.0.0.1:8080
```

Remote mode does not use local provider keys, embedded-server flags, or a local workspace.
The server owns placement; `connect` rejects `--workspace` even on loopback. New sessions
bind the server default or no-FS, and `/worktrees` selects only server-advertised opaque
choices from the owned source session. See [Connect to a server](./remote-servers.md) for ownership boundaries, authenticated and TLS connections, and operator next steps.

## Trust the workspace intentionally

A workspace can contain instructions and files that influence the agent. Start in a repository you recognize, review permission requests before approving them, and treat prompts, tool output, and fetched content as untrusted input. In remote mode, the server's policy and trust configuration control what the agent can access; do not assume local settings apply.

## Next steps

Learn how to [work while a run is streaming](./using-the-tui.md), [use everyday keys](./keybindings.md), and [resume the chat later](./sessions.md).
