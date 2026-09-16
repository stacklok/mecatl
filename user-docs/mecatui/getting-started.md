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
- an API key with API access enabled from Anthropic, OpenAI, OpenRouter, or
  OpenCode; and
- a local project directory that you trust.

The project is your **workspace**. Mecatl limits its file tools and commands to
this directory.

This guided path uses provider API credentials. A ChatGPT Plus or Pro
subscription does not provide an OpenAI API key and cannot be used to sign in
through this setup. Mecatl's OIDC options authenticate to an operator-configured
gateway or remote Mecatl server, not to a consumer provider account. See
[Provider authentication](/features/choose-models.md#authenticate-to-a-model-provider)
for the supported paths.

## Install Mecatl

Homebrew installs the `mecatui` client and the `mecated` server:

```sh
brew install stacklok/tap/mecatl
mecatui --version
```

The version command should print a release tag.

## Set up a provider

Use an API key for this tutorial. Run the interactive setup and select your
provider:

```sh
mecatui providers setup
```

Enter the API key when prompted. `mecatui` saves locally managed credentials
without displaying them. Confirm that your provider reports `ready to use`:

```sh
mecatui providers status
```

For a custom provider or gateway, follow
[Configure a custom provider](/features/choose-models.md#configure-a-custom-provider).
Custom provider IDs use lowercase letters, numbers, and hyphens.

## Start mecatui

```sh
cd <PROJECT_DIRECTORY>
mecatui
```

To submit the first prompt at startup while keeping the session interactive,
pass `--prompt`:

```sh
mecatui --prompt "Summarize the failing tests in this repository"
```

The welcome screen shows your workspace and active model. Keep the default model
until you complete the first request.

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

## Try another model

Enter `/models` to open the models reported by your configured provider. Select
a model and press **Enter**. Model IDs and capabilities are provider-specific,
so start with a model shown in this inventory. Changing models creates a peer
session and carries over the visible conversation.

If a listed model fails, see
[Troubleshoot provider and model setup](./troubleshooting.md#provider-is-not-configured-or-credentials-are-unavailable).

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

## Troubleshooting

If provider setup or the first model request fails, run
`mecatui providers status` and use the recovery action shown for that provider.
For custom gateways, verify the provider ID, API flavor, exact model ID, and
gateway model access in
[Troubleshoot mecatui](./troubleshooting.md#provider-is-not-configured-or-credentials-are-unavailable).
