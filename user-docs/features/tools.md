---
sidebar_position: 160
title: Core tools
description:
  Understand Mecatl's built-in tools and the read-parallel, mutate-serial
  dispatch model.
---

# Core tools

The tool catalog combines built-in tools, connected MCP tools, and optional
capabilities such as memory and skills. Every tool uses the same `tool.Tool`
interface and permission path.

Two dispatch rules govern execution:

- Read-only tools from the same turn can run concurrently.
- Mutating tools run one at a time.

Each tool declares its behavior through `ReadOnly()`. The dispatcher enforces
the declaration.

## Core tool table

These tools appear in a default session. Use their catalog names in tool specs
and permission rules.

|Catalog name|Purpose|Read-only?|
|-|-|-|
|`Read`|Read a file. Supported images return typed image content when the model accepts images.|Yes|
|`ListDir`|List one directory's sorted immediate children. Local workspaces can list a policy-authorized external absolute directory.|Yes|
|`Write`|Create a file or replace a previously read, unchanged file.|No|
|`Edit`|Replace exact, unique text in a previously read, unchanged file.|No|
|`Copy`|Copy a regular file to a new path without overwriting.|No|
|`Move`|Move a file or directory to a new path without overwriting.|No|
|`Remove`|Remove one file or empty physical directory. It is never recursive.|No|
|`Shell`|Run a permission-controlled shell command, optionally in the background.|No|
|`ShellStatus`|List, inspect, wait for, collect, or cancel this run's background jobs.|Yes|
|`Grep`|Search file contents with a regular expression or literal pattern.|Yes|
|`Glob`|List files that match a glob pattern.|Yes|
|`DiscoverModels`|List resolved provider and model selection handles.|Yes|
|`WebFetch`|Fetch readable text from a public HTTP or HTTPS URL.|Yes|
|`WebSearch`|Search the web.|Yes|
|`ToolSearch`|Load hidden tools by keyword when progressive disclosure is enabled.|Yes|

`Read` detects images by content. Image reads are limited to 10 MiB, do not
support `offset` or `limit`, and return a text fallback when the model cannot
accept images.

### Fetching a page without MCP

`WebFetch` returns readable text from an absolute HTTP or HTTPS URL. Use
`WebSearch` to find a page and `WebFetch` to read a known URL.

It rejects private, loopback, link-local, metadata, and reserved destinations,
pins the validated DNS result for the connection, and repeats those checks after
redirects. It does not use browser cookies, proxy settings, custom headers, or
credentials. Limits include five redirects, 5 MiB response bodies, and 25,000
bytes of model-visible output.

Mecatl removes active HTML, repairs invalid UTF-8, and marks fetched text as
untrusted. The default model-backed guardrails inspect `WebFetch` results when
guardrails are enabled.

### Configure web search

`WebSearch` is enabled by default and uses Exa's public MCP endpoint
anonymously. Set `EXA_API_KEY` to use Exa's paid tier, `BRAVE_API_KEY` to use
Brave Search, or `SEARXNG_URL` to use a self-hosted SearXNG `/search` endpoint.
SearXNG must enable JSON output because Mecatl requests `format=json`.

For another HTTP JSON service, set `--websearch-url` and `WEBSEARCH_API_KEY`.
Results must contain `title`, `url`, and one of `snippet`, `content`, or
`description`. Use `--websearch-auth-header` for a raw-key header or
`--websearch-query-param` to replace the default `q` parameter.

Set `--websearch=off` to disable outbound search. Queries are sent verbatim,
results are marked as untrusted, redirects are refused, and calls have timeout
and concurrency limits. Use permissions or guardrails for additional
exfiltration controls.

:::note[Shell is mutating]

`Shell` is always mutating. Use `ListDir`, `Grep`, or `Glob` for concurrent
read-only work.

:::

An operator can disable shell access for the entire deployment.
`mecated serve --no-shell` removes the `Shell` tool, and `--shell ""` has the
same effect. The user-global `command_runner.shell` setting selects the default
interpreter, while `command_runner.environment.inherit` can grant named external
variables only to built-in main runners. See
[Configure Mecatl](/operating/settings.md#configure-the-command-runner)
for precedence and credential-boundary details. This applies whether or not a
client requests the `no-fs` session profile. See
[Run mecated standalone](/operating/mecated.md#flag-reference).

### Managed temporary storage (Linux and macOS)

On Linux and macOS, Shell uses private managed temporary storage. Mecatl removes
it after normal completion and later reclaims validated, abandoned leases. It
never sweeps unrelated system files. Other platforms must set
`temporary_storage.mode: system` or Mecatl will not start.

Set `temporary_storage.mode: system` in user-global settings to use system
temporary storage. Existing managed leases remain for manual inspection or
removal. A model request for `temp_scope: system` requires both Shell permission
and the `ShellSystemTemp` capability. See the
[configuration reference](/reference/configuration.md#temporary_storage) for
cleanup settings.

### Background commands

`Shell` with `background: true` returns a `bashcmd-<id>` job ID. Call
`ShellStatus` without arguments to list jobs, use `job_id` to inspect or collect
one, use `wait_ms` to wait, or pass an ID to `cancel` to stop it. Background
commands run in the real workspace, so their changes can overlap later tool
calls. Mecatl cancels them when the run ends.

### The no-filesystem session profile

A session created with `profile: "no-fs"` removes
`Read`/`ListDir`/`Write`/`Edit`/`Copy`/`Move`/`Remove`/`Grep`/`Glob`/`Shell`/`ShellStatus`/`Parallel`/`SkillDraft`
from the catalog. `WebFetch`, `WebSearch`, memory, and MCP tools remain.
Children inherit the file-less catalog. Clients choose the profile when creating
the session; the model cannot change it. See
[Execution environments](/features/execution-environments.md) for filesystem
and no-filesystem placement.

---

## Optional tool groups

Configured memory stores add project and user-memory tools. Delegation adds
`Subagent`, `Parallel`, and `Team`. These tools use the same catalog and
permission path as the core tools.

See [Memory and user model](./memory.md) for memory lifecycle operations and
[Subagents, teams, and parallel work](./subagents-and-teams.md) for delegation
behavior.

---

## The tool catalog model

Tools register in `tool.Catalog` with a name, JSON-schema specification, and
`ReadOnly()` behavior. Built-in and MCP tools use the same registration path.

A call that mutates parent state, such as `Subagent` in `mode: read-write`, runs
serially even if the tool is usually read-only.

MCP tools use `mcp__<server>__<tool>` names, which prevent collisions with
built-ins and serve as permission-rule identifiers.

Tools that implement `tool.Disclosable` remain hidden until `ToolSearch` loads
them. Other tools always appear in the model's catalog.

The `Skill` tool loads instruction bundles on demand. See
[Skills, commands, and soul](./skills-commands-and-soul.md) for discovery,
activation, and slash-command behavior.

---

## Next steps

- [MCP client](./mcp-client.md) to connect external MCP servers. Their catalog
  names use the `mcp__<server>__<tool>` format.
- [Tool catalog extension point](/building/extension-points/tool-catalog.md) to
  add custom tools, configure skills, and control what the model can see.
