---
sidebar_position: 2
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
[Configure Mecatl](/building/deployment/settings.md#configure-the-command-runner)
for precedence and credential-boundary details. This applies whether or not a
client requests the `no-fs` session profile. See
[Run mecated standalone](/building/deployment/mecated.md#flag-reference).

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
and no-filesystem placement, and [Engine and session model](engine-and-session.md)
for session creation.

### Execution placement providers

`microvm-local` is a trusted deployment default selected through the strict operator-tier
`execution.default_placement` setting (or a higher-precedence explicit mecated serve flag).
Bare mecatui consumes that setting for its embedded server; mecatui connect remains remote-only.
Ordinary session creation then uses that default;
clients cannot submit a placement alias, workspace path, or exact environment ref. Public
session data contains bounded `PlacementMetadata` only. `profile: "no-fs"` remains the one
client-selected attenuation. The daemon owns image, resource, egress, lifecycle, and
attestation policy, and unavailable placement fails without host fallback.

Guest IPv4 is permissive by default, with external IPv6 unrouted. The operator can tighten
it with `execution.microvm.guest_egress.mode: deny-all`, or `allowlist` plus
`allow: [HOST:PORT/tcp|udp]`. Explicit mecated serve flags override settings for one run.
HTTP/gRPC requests and project config cannot
select or weaken placement or egress policy.

---

## Memory tools

When memory stores are configured, Mecatl registers project and user memory
tools. Stores with versioned history also expose inspect, forget, and undo
operations.

|Catalog name|Scope|What it does|
|-|-|-|
|`Remember`|Per-project|Stores a named fact in the current project's memory store.|
|`Recall`|Per-project|Retrieves a named fact from the current project's memory store.|
|`SearchMemory`|Per-project|BM25-indexed search over the project's memory entries.|
|`RememberUser`|Cross-project (user model)|Stores a durable operator fact under the `user/` namespace, shared across all projects. Requires the user-model store to be configured.|
|`RecallUser`|Cross-project (user model)|Retrieves a named fact from the cross-project user model.|
|`SearchUserModel`|Cross-project (user model)|Searches the cross-project user model by query.|
|`InspectMemory` / `InspectUserMemory`|Both|Reads exact version, provenance, timestamps, and bounded history.|
|`ForgetMemory` / `ForgetUserMemory`|Both|Writes a reversible tombstone after an approval by default.|
|`UndoMemory` / `UndoUserMemory`|Both|Appends a compensating revision restoring the previous state.|

`Forget` and `Undo` require the exact `expected_version` from a same-scope
`Recall`, `Inspect`, or mutation result. Inspect first if the version might be
stale. The token is opaque.

For persistence, lifecycle history, and consolidation, see
[Memory and knowledge](memory.md).

---

## Subagent, Parallel, and Team tools

Default sessions include three delegation tools:

|Catalog name|What it enables|
|-|-|
|`Subagent`|Run one focused task in a child agent.|
|`Parallel`|Fan out a set of tasks to isolated branches, then join all results or select a winner. Branches run concurrently; a single-branch winner is merged back into the parent by default, multi-branch runs never auto-merge.|
|`Team`|Coordinate specialists and return the lead's synthesis.|

`Subagent` and `Parallel` are read-parallel by default because child changes are
isolated. `Team` is mutating and serialized. Plan mode permits read-only
Subagent and Parallel calls but not Team. For child permissions, persistence,
and budgets, see [Subagents, teams, and parallel](subagents-teams-parallel.md).

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

The `Skill` tool loads instruction bundles on demand. Each skill is also
available as `/<skill-name>`. Enable discovery with `--skills-dir` or
`--skills-conventional`. See
[Extension points: tool catalog](/building/extension-points/tool-catalog.md) for
details on wiring skills.

Learned skills follow the lifecycle in the
[skills and learning guide](/features/skills-commands-and-soul.md). Activation
can change only the instructions returned by `Skill`; learned content cannot add
tools, executable assets, paths, permissions, or workspace roots. Explicitly
configured skills keep precedence.

Slash commands expand local prompt templates from `.mecatl/commands/` or
`.claude/commands/`. Embedded `mecatui` enables them by default. For `mecated`,
use `--enable-commands` or `--commands-dir`. Built-in commands win name
collisions.

---

## What's next

- [MCP client](mcp-client.md) to connect external MCP servers. Their catalog
  names use the `mcp__<server>__<tool>` format.
- [Tool catalog extension point](/building/extension-points/tool-catalog.md) to
  add custom tools, configure skills, and control what the model can see.
