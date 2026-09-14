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
- an API key from Anthropic, OpenAI, OpenRouter, or OpenCode; and
- a local project directory that you trust.

The project is your **workspace**. Mecatl limits its file tools and commands to
this directory.

## Install Mecatl

Homebrew installs the `mecatui` client and the `mecated` server:

```sh
brew install stacklok/tap/mecatl
mecatui --version
```

The version command should print a release tag.

## Set up a provider

For most people, the fastest way to get started is to use an API key. Run the
interactive setup and select your provider:

```sh
mecatui providers setup
```

Enter the API key when prompted. `mecatui` saves locally managed credentials
without displaying them. You can check the configured provider later with
`mecatui providers status`.

Gateway and OAuth-based provider setups are available when your organization
requires them, but they are not needed for a typical first local session. See
[Choose models and providers](/features/choose-models.md) when you need those
options.

## Start mecatui

```sh
cd <PROJECT_DIRECTORY>
mecatui
```

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
mecatui --resume-latest
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
