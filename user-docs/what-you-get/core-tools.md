---
sidebar_position: 2
title: Core tools
---

# Core tools

The **tool catalog** is the set of tools the model can invoke during a session. mecatl assembles it at startup from the core built-ins, any configured MCP servers, and opt-in features (skills, memory). Every tool — built-in or remote — is the same `tool.Tool` interface, so the model and the dispatch layer see one uniform surface.

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
| `Write` | Write a file to the workspace (create or overwrite). | No |
| `Edit` | Apply an exact-string replacement to a file. Enforces read-before-edit, exact match, and uniqueness (or `replace_all`). Safer than Write for targeted changes. | No |
| `Bash` | Execute a shell command. The model's general-purpose escape hatch for tasks no other tool covers. Subject to permission rules. | No |
| `Grep` | Search file contents for a pattern (regex or literal) across the workspace. Returns matching lines with context. | Yes |
| `Glob` | List files matching a glob pattern. Useful for discovering which files exist before reading them. | Yes |
| `WebFetch` | Fetch the content of an HTTP URL. Present in both default and no-filesystem session profiles. | Yes |
| `WebSearch` | Run a web search and return results. Present in both default and no-filesystem session profiles. | Yes |
| `ToolSearch` | Search the catalog for hidden (progressively-disclosed) tools by keyword and hydrate them into the session. Only registered when progressive tool disclosure is enabled. | Yes |

:::note[Bash is mutating]

`Bash` is always classified as mutating regardless of what the command does. If you need the model to run read-only shell commands concurrently, use `Grep` and `Glob` instead — they are purpose-built read-only tools that run in parallel.

:::

---

## Memory tools

Six memory tools are registered when the memory store is configured (on by default). They are **floor-scoped allows** — pre-approved without requiring an explicit permission rule, but operator-overridable.

| Catalog name | Scope | What it does |
|---|---|---|
| `Remember` | Per-project | Stores a named fact in the current project's memory store. |
| `Recall` | Per-project | Retrieves a named fact from the current project's memory store. |
| `SearchMemory` | Per-project | BM25-indexed search over the project's memory entries. |
| `RememberUser` | Cross-project (user model) | Stores a durable operator fact under the `user/` namespace, shared across all projects. Requires the user-model store to be configured. |
| `RecallUser` | Cross-project (user model) | Retrieves a named fact from the cross-project user model. |
| `SearchUserModel` | Cross-project (user model) | Searches the cross-project user model by query. |

The per-project tools (`Remember`/`Recall`/`SearchMemory`) are on by default; the cross-project tools (`RememberUser`/`RecallUser`/`SearchUserModel`) require `--user-model-store` or the conventional path to be present.

For the full memory architecture — dream consolidation, the memory index turn-0 block, write-time injection scanning — see [Memory & knowledge](memory.md).

---

## Subagent, Parallel, and Team tools

Three delegation tools are always registered in a default session. They let the model decompose work across child agents running in parallel or in coordination.

| Catalog name | What it enables |
|---|---|
| `Subagent` | Spawn a child agent to handle a subtask. Supports background mode, structured output schemas, fork-from-parent history, named specialist agent definitions, and per-call model overrides. |
| `Parallel` | Fan out a set of tasks to isolated branches, then join or select a winner. Branches run concurrently; results are merged back into the parent. |
| `Team` | Coordinate a named crew of specialist members under a lead. The lead synthesizes a consolidated report from member findings. |

These tools are mutating (the parent serializes their dispatch) and are not available in plan mode. For depth on how delegation works — child permissions, session persistence, token budgets, structured output — see the subagents and teams section (coming soon).

---

## The tool catalog model

**Registration.** Tools are added to `tool.Catalog` at startup. Each tool has a name, a spec (description + JSON schema for its parameters), and a `ReadOnly()` predicate. The catalog is the single registration seam — there is no separate registration path for MCP tools or built-ins.

**ReadOnly dispatch.** When the model returns a turn with multiple tool calls, the dispatcher sorts them into two groups: read-only calls fan out concurrently, mutating calls run one at a time. A tool call that would mutate the parent session state (such as `Subagent` in `mode: read-write`) is also treated as mutating even if its `ReadOnly()` returns true for the general case.

**MCP namespacing.** Tools discovered from MCP servers are registered with the prefix `mcp__<server>__<tool>`. This prevents any remote tool from colliding with or shadowing a built-in. For example, a tool named `search` on an MCP server named `github` registers as `mcp__github__search`. Permission rules use the same namespaced name.

**Progressive disclosure.** A tool may implement `tool.Disclosable`, which hides it from the default tool list until the model invokes `ToolSearch` to surface it. This keeps the model's context window lean when the catalog is large. Tools that do not implement `Disclosable` are always listed.

**Skills.** Skills are not tools in the traditional sense — they are progressive-disclosure instruction bundles. A single `Skill` tool exposes a catalog of `SKILL.md` files; the model calls it with a skill name to load that skill's full instructions into context. Skills are opt-in (`--skills-dir` or `--skills-conventional`). See [Extension points: tool catalog](/extension-points/tool-catalog.md) for details on wiring skills.

---

## What's next

- [MCP client](mcp-client.md) — connect external MCP servers; their tools land in the catalog automatically with `mcp__<server>__<tool>` names.
- [Extension points: tool catalog](/extension-points/tool-catalog.md) — add custom tools, configure skills, and control what the model can see.
