---
sidebar_position: 2
title: Core tools
description: Understand Mecatl's built-in tools and the read-parallel, mutate-serial dispatch model.
---

# Core tools

The **tool catalog** is the set of tools the model can invoke during a session. Mecatl assembles it at startup from the core built-ins, any configured MCP servers, and opt-in features (skills, memory). Every tool — built-in or remote — is the same `tool.Tool` interface, so the model and the dispatch layer see one uniform surface.

Two dispatch rules govern execution:

- **Read-only tools** run concurrently. Multiple read-only tool calls from one model turn execute in parallel.
- **Mutating tools** are serialized. The dispatcher never runs two mutating tools at the same time.

Each tool declares its own read-only status via `ReadOnly()`. This is enforced mechanically by the dispatcher — you do not configure it.

---

## Core tool table

These tools are always present in a default session (no extra configuration required). The catalog name is what appears in tool specs and permission rules.

| Catalog name | Purpose | Read-only? |
|---|---|---|
| `Read` | Read a file from the workspace by path. The primary way the model loads source code, config, and data files. | Yes |
| `ListDir` | List one directory's immediate children. Results are sorted and directories carry a trailing `/`; virtual workspaces may derive directories from file paths and omit empty directories. | Yes |
| `Write` | Create a missing file or conditionally replace an existing file in the workspace. A replacement requires a prior Read and an unchanged version; concurrent changes and creations surface as model-visible conflicts rather than being silently clobbered. | No |
| `Edit` | Apply an exact-string replacement to a file. Enforces read-before-edit, exact match, and uniqueness (or `replace_all`). The file must be unchanged since it was read; a concurrent change or deletion since the read surfaces as a model-visible refusal to re-read and retry. Safer than Write for targeted changes. | No |
| `Copy` | Copy one regular file to a new path. The destination must not exist; directories and overwrites are refused. | No |
| `Move` | Move a file or directory to a new path. The destination must not exist, so an existing path is never silently replaced. | No |
| `Remove` | Remove one file or empty physical directory. Removal is never recursive; non-empty and virtual derived directories are refused. | No |
| `Bash` | Execute a shell command. The model's general-purpose escape hatch for tasks no other tool covers. Subject to permission rules. Supports `background: true` for long-running commands (see below). | No |
| `BashStatus` | Check on the background commands `Bash` started in this run: poll a job's output tail, collect a finished job's result, or cancel a job. Registered wherever `Bash` is. | Yes |
| `Grep` | Search file contents for a pattern (regex or literal) across the workspace. Returns matching lines with context. Supports `**` recursive globs when scoping the search to a subtree; broad unscoped searches have a safety budget, so supply `path` for large workspaces. | Yes |
| `Glob` | List files matching a glob pattern. Useful for discovering which files exist before reading them. Supports `**` for recursive matching across any number of directory levels. | Yes |
| `DiscoverModels` | Inspect the server's resolved model inventory through a bounded, safe projection. Each returned `provider_id` + `model_id` pair is an exact selection handle. Present in default and no-filesystem profiles. | Yes |
| `WebFetch` | Fetch readable text from a public HTTP(S) URL. Present in both default and no-filesystem session profiles. | Yes |
| `WebSearch` | Run a web search and return results. Present in both default and no-filesystem session profiles. | Yes |
| `ToolSearch` | Search the catalog for hidden (progressively-disclosed) tools by keyword and hydrate them into the session. Only registered when progressive tool disclosure is enabled. | Yes |

### Fetching a page without MCP

`WebFetch` works out of the box. Give it one absolute `http://` or `https://` URL and it returns readable text for HTML, Markdown, plain text, CSV, JSON, XML, RSS, or Atom content. Use `WebSearch` when you still need to find the URL; use `WebFetch` once you know what to read.

The fetcher does not use browser cookies, proxy settings, custom headers, or credentials. It rejects private, loopback, link-local, metadata, and reserved destinations, pins the DNS result used for the connection, and repeats those checks on every redirect. URLs are capped at 8 KiB, redirects at five hops, downloads and decompressed bodies at 5 MiB, and model-visible output at 25,000 bytes. Binary files and unsupported content types are rejected.

Fetched text is external input. Mecatl strips active HTML elements, converts the remaining page to text, repairs invalid UTF-8, and wraps the result in the same untrusted-content fence used by WebSearch. If you enable model-backed guardrails, their default rules inspect `WebFetch` results before the model sees them.

:::note[Bash is mutating]

`Bash` is always classified as mutating regardless of what the command does. If you need the model to run read-only shell commands concurrently, use `ListDir`, `Grep`, and `Glob` instead — they are purpose-built read-only tools that run in parallel.

:::

### Managed temporary storage (Linux and macOS)

Bash uses a private managed temporary lease by default. On normal completion the
harness removes that lease; a bounded maintenance worker later reclaims only
validated, unlocked abandoned command/job leases after the operator-configured
TTL. It never sweeps arbitrary system temporary files and never blocks command
allocation. The lifecycle is available on Linux and macOS; other platforms must
use system mode.

An operator can set `temporary_storage.mode: system` in user-global
`~/.config/mecatl/settings.yaml` to restore system temporary storage. This rollback
mode creates no new managed leases, runs no reaper, and leaves existing managed
storage untouched for explicit inspection or removal. A model's `temp_scope:
system` request remains permission-gated; it needs both the normal Bash decision
and the separate `BashSystemTemp` capability. In managed mode, `managed_root`,
`system_temp_dir`, `command_reap_after`, `reap_interval`, `reap_timeout` (default
five minutes), and `shutdown_reap_timeout` (default one minute) configure storage
and cleanup.

### Background commands

A `Bash` call with `background: true` returns immediately with a `bashcmd-<id>` job id and keeps the command running while the model continues — the pattern for a dev server, a watch loop, or a slow build. The permission ask happens once, at start, exactly as for a foreground command. The read-only `BashStatus` tool is the only channel back: no arguments lists this run's jobs (id, running/done, stop reason), `job_id` shows the command and its retained output tail (or collects a finished job's result, delivered once), `wait_ms` waits for a finish, and `cancel` stops a job. A background command runs in the **real workspace with no isolation** — its effects can interleave with the model's own file changes — keeps only a bounded tail of recent output, and is **cancelled automatically if it is still running when the run ends** (a job lives for one run, never across sessions).

### The no-filesystem session profile

A session can be created with `profile: "no-fs"` — for a workspace that has no real filesystem to speak of, or a deployment that never wants one in reach. It removes `Read`/`ListDir`/`Write`/`Edit`/`Copy`/`Move`/`Remove`/`Grep`/`Glob`/`Bash`/`BashStatus`/`Parallel`/`SkillDraft` from the catalog entirely; `WebFetch`, `WebSearch`, the memory tools, and any MCP tools stay. A `Subagent`/`Team` child spawned from a no-fs session gets the equivalent file-less catalog, not the default one. This is a session-creation choice the client makes, not something the model can flip mid-session — see [Engine & session model](engine-and-session.md) for how a session is created.

---

## Memory tools

Six base memory tools are registered when the two stores are configured. A lifecycle-capable store adds `InspectMemory`, `ForgetMemory`, and `UndoMemory` for project scope plus `InspectUserMemory`, `ForgetUserMemory`, and `UndoUserMemory` for user scope. Remember/Recall/Search/Inspect/Undo are **floor-scoped allows**; Forget is a **floor-scoped ask**. Forget and Undo require an expected_version copied exactly from an exact Recall result, Inspect result, or mutation receipt in the same scope. If none is available or it may be stale, use InspectMemory (project scope) or InspectUserMemory (user/user-model scope) first. The token is opaque, so never guess or interpret it. Every default is operator-overridable.

| Catalog name | Scope | What it does |
|---|---|---|
| `Remember` | Per-project | Stores a named fact in the current project's memory store. |
| `Recall` | Per-project | Retrieves a named fact from the current project's memory store. |
| `SearchMemory` | Per-project | BM25-indexed search over the project's memory entries. |
| `RememberUser` | Cross-project (user model) | Stores a durable operator fact under the `user/` namespace, shared across all projects. Requires the user-model store to be configured. |
| `RecallUser` | Cross-project (user model) | Retrieves a named fact from the cross-project user model. |
| `SearchUserModel` | Cross-project (user model) | Searches the cross-project user model by query. |
| `InspectMemory` / `InspectUserMemory` | Both | Reads exact version, provenance, timestamps, and bounded history. |
| `ForgetMemory` / `ForgetUserMemory` | Both | Writes a reversible tombstone after an approval by default. |
| `UndoMemory` / `UndoUserMemory` | Both | Appends a compensating revision restoring the previous state. |

The project tools require project memory to be enabled; the cross-project tools use `--user-model-dir` or its conventional XDG location and disappear with `--no-user-model`.

For the full memory architecture — live operator profile, lifecycle history, dream consolidation, and secret-safe structured rendering — see [Memory & knowledge](memory.md).

---

## Subagent, Parallel, and Team tools

Three delegation tools are always registered in a default session. They let the model decompose work across child agents running in parallel or in coordination.

| Catalog name | What it enables |
|---|---|
| `Subagent` | Spawn a child agent to handle a subtask. Supports background mode, structured output schemas, fork-from-parent history, named specialist agent definitions, and per-call model overrides. |
| `Parallel` | Fan out a set of tasks to isolated branches, then join all results or select a winner. Branches run concurrently; a single-branch winner is merged back into the parent by default, multi-branch runs never auto-merge. |
| `Team` | Coordinate a named crew of specialist members under a lead. The lead synthesizes a consolidated report from member findings. |

`Subagent` and `Parallel` are read-parallel by default — each isolates its child's
writes in its own workspace, so the dispatcher can run them alongside other
read-only tool calls. `Team` is mutating and serialized. Plan mode still permits
`Subagent` and `Parallel` where their calls are read-only; `Team` is unavailable
because it mutates team state. Child policies and any call-level mutation rules
still apply. For depth on how delegation works — child permissions, session
persistence, token budgets, and structured output — see [Subagents, teams, and
parallel](subagents-teams-parallel.md).

---

## The tool catalog model

**Registration.** Tools are added to `tool.Catalog` at startup. Each tool has a name, a spec (description + JSON schema for its parameters), and a `ReadOnly()` predicate. The catalog is the single registration seam — there is no separate registration path for MCP tools or built-ins.

**ReadOnly dispatch.** When the model returns a turn with multiple tool calls, the dispatcher sorts them into two groups: read-only calls fan out concurrently, mutating calls run one at a time. A tool call that would mutate the parent session state (such as `Subagent` in `mode: read-write`) is also treated as mutating even if its `ReadOnly()` returns true for the general case.

**MCP namespacing.** Tools discovered from MCP servers are registered with the prefix `mcp__<server>__<tool>`. This prevents any remote tool from colliding with or shadowing a built-in. For example, a tool named `search` on an MCP server named `github` registers as `mcp__github__search`. Permission rules use the same namespaced name.

**Progressive disclosure.** A tool may implement `tool.Disclosable`, which hides it from the default tool list until the model invokes `ToolSearch` to surface it. This keeps the model's context window lean when the catalog is large. Tools that do not implement `Disclosable` are always listed.

**Skills.** Skills are not tools in the traditional sense — they are progressive-disclosure instruction bundles. A single `Skill` tool exposes a catalog of `SKILL.md` files; the model calls it with a skill name to load that skill's full instructions into context. Each discovered skill is **also invocable as a slash command**: `/<skill-name>` injects the skill body directly as the expanded prompt (Claude-Code skill-as-command semantics). Skills are opt-in (`--skills-dir` or `--skills-conventional`). See [Extension points: tool catalog](/building/extension-points/tool-catalog.md) for details on wiring skills.

**Skills can be learned, under lifecycle control.** Direct `SkillDraft` derives the verified caller and exact workspace and creates only an inactive legacy draft; it is never an automatic-activation shortcut. Completed-trajectory reflection can evaluate evidence-backed procedures: review mode stages PASS/ABSTAIN and rejects FAIL. In Auto, PASS activates; the stock `validated` policy may also activate a structurally accepted, evidence-backed ABSTAIN, while `activation: evaluated` retains PASS-only assurance. Similar candidates, external collisions, and unpublishable caller/project partitions remain staged; evaluator infrastructure failures persist only a generic ERROR verdict and remain rejected. Publication is recoverable and reports pending status instead of hiding a committed change. Activation changes only the body returned by the existing `Skill` tool: learned skills cannot add tools, assets, scripts, paths, permissions, or workspace roots. Operator/project/user/driver skills keep precedence and remain immutable. See the [skills and learning guide](https://github.com/stacklok/mecatl/blob/main/docs/usage/skills-soul-usermodel.md). The old quarantine plus `mecated skills promote` workflow remains a deprecated compatibility path.

**Slash commands.** These aren't tools either — they're your own reusable prompt templates. Drop a `<name>.md` file in `.mecatl/commands/` (or `.claude/commands/`, if you're used to that convention) and typing `/<name>` expands it into the prompt before it's sent, no network or trust cost involved since it never leaves your machine. `mecatui`'s embedded server picks these up automatically (opt out with `--no-commands`); a standalone `mecated` needs `--enable-commands` (or `--commands-dir` for a non-conventional location). `mecatui` merges these into its command palette alongside the built-ins, with a built-in winning any name collision.

---

## What's next

- [MCP client](mcp-client.md) — connect external MCP servers; their tools land in the catalog automatically with `mcp__<server>__<tool>` names.
- [Extension points: tool catalog](/building/extension-points/tool-catalog.md) — add custom tools, configure skills, and control what the model can see.
