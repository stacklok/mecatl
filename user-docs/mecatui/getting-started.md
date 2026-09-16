---
sidebar_position: 2
title: Run your first local session
sidebar_label: Get started
description:
  Run mecatui in a project and use an embedded Mecatl server for your first
  session.
---

# Run your first local session

In this tutorial, you will start `mecatui` in a local project and ask Mecatl to
inspect it. `mecatui` runs a private embedded server, so you do not need to
start a separate service.

## Prerequisites

You need:

- macOS or Linux with [Homebrew](https://brew.sh/);
- an API key for Anthropic, OpenAI, or OpenRouter; and
- a local project directory that you trust.

The project is your **workspace**. Mecatl limits its file tools and commands to
this directory.

## Install Mecatl

Homebrew installs the `mecatui` client and the `mecated` server:

```sh
brew install stacklok/tap/mecatl
mecatui --version
```

The version command should print a release tag. For signed archives and source
builds, see [Install Mecatl](/install.md).

## Start mecatui

Mecatl detects the provider from its environment variable. Set one of
`ANTHROPIC_API_KEY`, `OPENAI_API_KEY`, or `OPENROUTER_API_KEY` in the shell
where you will run `mecatui`:

```sh
cd <PROJECT_DIRECTORY>
export <PROVIDER_API_KEY>="<API_KEY>"
mecatui --workspace "$PWD"
```

Replace `<PROVIDER_API_KEY>` with the variable for your provider.

## Manage embedded providers

`mecatui providers` shows local provider status without revealing credentials.
Start with guided setup:

```sh
mecatui providers setup
```

For a named custom provider, use direct commands when needed:

```sh
mecatui providers add example --no-login
mecatui providers login example
mecatui providers set-default example MODEL
```

Setup does not launch a session; matching environment credentials take precedence
over locally managed ones. Remote `mecatui connect ADDRESS` uses the remote
server's provider configuration. For credential sources, secret safety, custom
providers, and manual Codex support, see [local provider setup](/features/choose-models.md#set-up-a-local-provider).

The welcome screen shows your workspace and active model. To use a different
model, enter `/models`, select one, and press `enter`. A small, low-cost model
is enough for this tutorial.

The header also shows `mode default`, identifying the active permission mode.
With the default permission policy, read-only tools can run without approval and
actions that change the workspace ask first.

## Inspect the project

Enter this request:

```text
Inspect the top-level files and explain what this project does. Cite the files you used.
```

You should see tool cards as Mecatl reads the workspace, followed by an answer
based on your project. Press `ctrl+t` on a tool card to inspect its full input
and result.

You now have a local **session**: the conversation, selected model, workspace,
and tool history that Mecatl keeps together. Exit with `ctrl+c` twice or
`/quit`. To continue the newest stored session, run:

```sh
mecatui --workspace "$PWD" --resume-latest
```

## Add tools from local MCP servers (optional)

MCP servers give agents tools for working with external services and data.
[ToolHive](https://docs.stacklok.com/toolhive/) is Stacklok's open source
runtime for running MCP servers locally.

If ToolHive has MCP servers running in its default group, the embedded server
discovers them at startup. Enter `/mcp` to inspect the available MCP sources and
tools. Mecatl connects to those servers but does not start them.

## Next steps

- [Connect to a separate server](./remote-servers.md) to move the server out of
  the `mecatui` process.
- [Work in the TUI](./using-the-tui.md) to steer runs, review tools, and approve
  actions.
- [Manage sessions](./sessions.md) to resume, inspect, and fork conversations.
