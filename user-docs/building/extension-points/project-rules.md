---
sidebar_position: 9
title: Project rules
description: Implement the rules source that discovers trusted, path-scoped project guidance.
---

# Project rules

This is the builder reference for the `RulesSource` discovery seam. Project rules
are per-file Markdown companions to `AGENTS.md`/`CLAUDE.md`, discovered from
`.claude/rules/` and `.mecatl/rules/` and optionally scoped with `paths:`
frontmatter.

For the user-facing explanation of project content and trust admission, see
[Project instructions and rules](/features/project-instructions-and-rules.md).

Rules are the pattern-2 instance of scoped context assembly: the same trust class
as `AGENTS.md`/`CLAUDE.md` and the soul/persona. See
[Skills, commands, and soul](/features/skills-commands-and-soul.md)
for the user-facing discovery and trust behavior, and
[ADR 0081](https://github.com/stacklok/mecatl/blob/main/docs/adr/0081-rules-source-port.md) for the design.

## Discovery

Conventional discovery is **always on** — there is no flag, and it is inert when
no directory exists, exactly like `AGENTS.md`/`CLAUDE.md`. The conventional
lanes, in descending precedence:

1. `<workspace>/.mecatl/rules`
2. `<workspace>/.claude/rules`
3. `$XDG_CONFIG_HOME/mecatl/rules` (fallback `~/.config/mecatl/rules`)
4. `~/.claude/rules`

A project rule overrides a personal rule of the same name; the first-discovered
name wins on collisions within the project or user tiers.

## File format

A rule file is markdown with optional YAML frontmatter:

```markdown
---
paths:
  - "**/*_test.go"
---
# Testing rule
When answering about Go tests, always run them before declaring done.
```

- **Name** — derived from the filename stem (`testing.md` → `testing`). There is
  no `name:` frontmatter field.
- **`paths:`** — a YAML sequence of glob patterns (a single scalar or
  comma-separated form is also accepted). A rule with no `paths:` is
  **unconditional** (always applies); a rule with `paths:` is scoped, and the
  model is told to apply the glob itself.

## Trust gate

The **project tier** (the `<workspace>/*` lanes) is **trust-gated**: rules under
`<workspace>/.claude/rules` in an untrusted workspace are withheld until you trust
the repo (`--trust-project` or `trustedWorkspaces`). The user-tier lanes
(`$XDG_CONFIG_HOME/...`, `~/.claude/rules`) are never gated — your own rules
always apply.

## Caps and fail-soft

A single rule body is capped at 20 KiB; the combined rule fragment is capped at
40 KiB across 32 rules. Rules beyond the cap are dropped with a footer and a
WARN. The whole load is fail-soft: a missing dir, an unreadable file, malformed
frontmatter, or a discovery fault degrades to no fragment — never an error that
aborts a run.
