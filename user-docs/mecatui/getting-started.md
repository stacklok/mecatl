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
- an Anthropic API key; and
- a local project directory that you trust.

The project is your **workspace**. Mecatl uses it as the root for file tools and
as the starting directory for commands. Shell access follows the configured
permission policy; a workspace alone does not provide OS isolation.

## Install Mecatl

Homebrew installs the `mecatui` client and the `mecated` server:

```sh
brew install stacklok/tap/mecatl
mecatui --version
```

The version command should print a release tag. For Conda-forge, release
archives, verification, and source builds, see
[Install mecatui](./installation.md).

## Start mecatui

This tutorial uses Anthropic. Set your API key in the terminal where you will
run `mecatui`, then start the client in your project:

```sh
cd <PROJECT_DIRECTORY>
export ANTHROPIC_API_KEY="<ANTHROPIC_API_KEY>"
mecatui --workspace "$PWD" --default-provider anthropic
```

For another provider or guided credential setup, follow
[local provider setup](/features/sessions/choose-models.md#set-up-a-local-provider)
before returning to this step.

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

## Next steps

- [Connect to a separate server](./remote-servers.md) to move the server out of
  the `mecatui` process.
- [Work in the TUI](./using-the-tui.md) to steer runs, review tools, and approve
  actions.
- [Manage sessions](./sessions.md) to resume, inspect, and fork conversations.

## Related information

<span id="manage-embedded-providers" />

- [Set up local providers](/features/sessions/choose-models.md#set-up-a-local-provider)
  for guided setup and custom models.

<span id="add-tools-from-local-mcp-servers-optional" />

- [Connect MCP tools](/features/security-and-execution/mcp-client.md) to use
  local ToolHive services.
