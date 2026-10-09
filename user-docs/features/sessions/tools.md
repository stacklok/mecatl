---
slug: /features/tools
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

`WebSearch` is enabled by default. Operators can select a backend in their
user-global `$XDG_CONFIG_HOME/mecatl/settings.yaml`. The `websearch` block is
strict and operator-only, so project settings cannot configure it:

```yaml title="settings.yaml"
websearch:
  url: https://search.example.com/api/search
  auth_header: X-API-Key
  query_param: query
```

`url` selects a generic HTTP JSON endpoint. `auth_header` names the header for
its optional credential, and `query_param` replaces the default `q` parameter.
With the default `Authorization` header, Mecatl sends the credential as a
Bearer token. Results must contain `title`, `url`, and one of `snippet`,
`content`, or `description`. Endpoints must be absolute HTTP(S) URLs without a
query string, userinfo, or fragment.

Set `providers.websearch.api_key` in the operator-owned `auth.yaml`, or set
`WEBSEARCH_API_KEY` in the process environment, to provide that credential.
`WEBSEARCH_API_KEY` takes precedence over the file entry. The credential never
appears in the query string. See [Configure mecated providers and
storage](/operating/mecated/configure-providers-and-storage.md#model-and-search-credential-file)
for the credential-file path and ownership requirements.

Use `searxng_url` instead of `url` to select a self-hosted SearXNG `/search`
endpoint. SearXNG must enable JSON output because Mecatl requests
`format=json`.

For `mecated`, the first matching item in this order selects the backend:

1. `--websearch=off` disables outbound search.
2. A nonempty `--websearch-url` selects the generic HTTP endpoint.
3. `websearch.enabled: false` disables outbound search when no explicit URL is
   set.
4. `websearch.url` selects the generic HTTP endpoint.
5. `websearch.searxng_url` selects SearXNG.
6. `SEARXNG_URL` selects SearXNG.
7. `BRAVE_API_KEY` or `providers.brave.api_key` selects Brave Search.
8. Exa is the default. `EXA_API_KEY` or `providers.exa.api_key` selects its
   authenticated paid tier; otherwise, Exa runs anonymously.

For API keys, the matching environment variable takes precedence over
`auth.yaml`. `--websearch-auth-header` and `--websearch-query-param` override
the generic endpoint's corresponding `settings.yaml` values. The generated
[server CLI reference](/reference/server-cli.md#mecated-serve) lists the
`mecated` flags, and the [configuration
reference](/reference/configuration.md#websearch) lists every `websearch`
field.

Bare `mecatui` uses its embedded server's operator `settings.yaml` and
`auth.yaml`, including `websearch.url` and `websearch.searxng_url`. It does not
read `SEARXNG_URL` or accept the `mecated` web-search flags. `mecatui connect`
uses the remote server's web-search configuration.

Exa and Brave are search credentials, not model providers. They do not satisfy
Mecatl's requirement for an LLM credential. Queries are sent verbatim, results
are marked as untrusted, redirects are refused, and calls have timeout and
concurrency limits. Use permissions or guardrails for additional exfiltration
controls.

:::note[Shell is mutating]

`Shell` is always mutating. Use `ListDir`, `Grep`, or `Glob` for concurrent
read-only work.

:::

An operator can disable shell access for the entire deployment.
`mecated serve --no-shell` removes the `Shell` tool, and `--shell ""` has the
same effect. The user-global `command_runner.shell` setting selects the default
interpreter, while `command_runner.environment.inherit` can grant named external
variables only to built-in main runners. See
[Configure Mecatl](/operating/settings.md#configure-the-command-runner) for
precedence and credential-boundary details. This applies whether or not a client
requests the `no-fs` session profile. See
[Run mecated standalone](/reference/server-cli.md#mecated-serve).

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
[Execution environments](/features/security-and-execution/execution-environments.md)
for filesystem and no-filesystem placement.

### Execution placement providers

Set the operator-tier `execution.default_placement` to `microvm-local` to run
filesystem and Shell tools in a local VM. An explicit `mecated serve` flag takes
precedence for that invocation. `mecatui` uses this setting for its embedded
server; `mecatui connect` uses the remote server's placement.

The server applies the configured placement to ordinary session creation.
Clients receive bounded `PlacementMetadata` and can select `profile: "no-fs"` to
remove filesystem access, but cannot submit a placement alias, workspace path,
or exact environment reference. The daemon controls images, resources, egress,
lifecycle, and attestation. If placement is unavailable, session creation fails
without falling back to the host.

Guest IPv4 access is permissive by default; external IPv6 is unrouted. Operators
can restrict IPv4 with `execution.microvm.guest_egress.mode: deny-all`, or use
`allowlist` with `allow: [HOST:PORT/tcp|udp]`. Explicit `mecated serve` flags
override settings for one invocation. HTTP/gRPC clients and project settings
cannot weaken this policy. See
[Local microVM environments](/operating/microvm-environments.md) for setup and
artifact requirements.

## Optional tool groups

Configured memory stores add project and user-memory tools. Delegation adds
`Subagent`, `Parallel`, and `Team`. These tools use the same catalog and
permission path as the core tools.

See [Memory and user model](/features/agent-behavior/memory.md) for memory
lifecycle operations and
[Subagents, teams, and parallel work](/features/agent-behavior/subagents-and-teams.md)
for delegation behavior.

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
[Skills, commands, and soul](/features/agent-behavior/skills-commands-and-soul.md)
for discovery, activation, and slash-command behavior.

## Next steps

- [MCP client](/features/security-and-execution/mcp-client.md) to connect
  external MCP servers. Their catalog names use the `mcp__<server>__<tool>`
  format.
- [Tool catalog extension point](/building/go/extension-points/tool-catalog.md)
  to add custom tools, configure skills, and control what the model can see.
