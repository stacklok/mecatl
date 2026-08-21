---
sidebar_position: 7
title: Skills, commands, and soul
description: Shape mecatl runs with reusable skills, slash commands, and a durable persona.
---

# Skills, commands, and soul

mecatl has three ways to add reusable guidance to a run:

- **Skills** are progressive-disclosure instruction bundles. Their metadata is
  always available, while the full `SKILL.md` body is loaded only when activated.
- **Slash commands** are templates that expand a prompt before the run. They can
  be file-backed, skill-backed, or supplied by a remote content source.
- **Soul** is a user-scoped, read-only persona fragment that describes the
  agent's identity and style.

These sources steer the model; they do not bypass the normal permission policy.
Treat repository-provided content as untrusted until the workspace is admitted by
project trust.

## Skills

A skill is a directory containing `SKILL.md`:

```text
skills/
└── deploy/
    ├── SKILL.md
    └── references/
        └── api.md
```

The file starts with YAML frontmatter and then contains the instructions:

```markdown
---
name: deploy
description: Deploy a service safely and verify its rollout.
license: Apache-2.0
compatibility: mecatl >= 0.1
metadata:
  owner: platform
allowed-tools: "Read Grep Bash"
---

# Deploy

Explain the deployment procedure here.
```

`name` is the activation key and `description` is the short trigger hint shown
in the `Skill` tool's catalog. `license`, `compatibility`, `metadata`, and
`allowed-tools` are advisory metadata. In particular, `allowed-tools` is **not**
a permission grant: every tool call still passes through the deny-dominant
policy.

### Where skills are discovered

Skills are opt-in. Configure one or more explicit directories with
`--skills-dir` (repeatable), or enable the conventional locations with
`--skills-conventional`:

| Tier | Location | Admission |
| --- | --- | --- |
| Explicit | each `--skills-dir` | operator-configured; always admitted |
| Project | `<workspace>/.mecatl/skills`, `<workspace>/.claude/skills` | requires project trust |
| User | `$XDG_CONFIG_HOME/mecatl/skills`, `~/.claude/skills` | user-owned; always admitted |

Explicit directories have higher precedence than conventional sources. With no
source configured, the `Skill` tool is disabled. A malformed `SKILL.md` is
skipped with a diagnostic; one bad skill does not prevent valid skills from
loading. Skills are resolved once when the server is built, so changing a file
during a process does not change that process's catalog.

Activate a skill through the tool by name. The result contains the body and a
logical inventory of bundled assets. Assets are not filesystem paths and are not
materialized or executable. To read one textual asset, call the tool again with
its logical name:

```json
{"name":"deploy","asset":"references/api.md"}
```

Do not tell a model to use `Read` on a skill directory or to execute a bundled
`scripts/` file. If a workflow needs a real file, it must create or obtain it in
the workspace through an ordinary, permission-governed step.

## Slash commands

Enable file-backed command expansion with `--commands-dir`, or use
`--enable-commands` to enable the conventional directories:

```text
<workspace>/.mecatl/commands
<workspace>/.claude/commands
```

Each command is a `<name>.md` template. Frontmatter is stripped and these
placeholders are substituted:

- `$ARGUMENTS` — the complete argument string;
- `$1`, `$2`, and so on — positional arguments.

For example, `.mecatl/commands/review.md` can be invoked as
`/review src/api.go`, with the path substituted into the template. An unknown
slash command passes through unchanged rather than becoming an empty prompt.

Every discovered skill is also available as `/<skill-name>`. This expands the
skill body directly, using the same placeholder rules as a file-backed command.
The tool path is better when the model needs to inspect the asset inventory; the
slash-command path is convenient when a human wants to start with a named
workflow. Neither path loads asset contents automatically.

Expansion precedence is first-match-wins:

1. local command files;
2. skills;
3. remote command-source templates;
4. MCP prompts.

A local command therefore shadows a same-named skill, and a skill shadows a
same-named remote command. Project command directories are withheld until the
workspace is trusted. An explicit `--commands-dir` is operator-supplied.

## Soul: a read-only persona

The default user soul is `$XDG_CONFIG_HOME/mecatl/soul.md` (normally
`~/.config/mecatl/soul.md`). Use `--soul-file` to select another file, or
`--no-soul` to disable it. The soul is injected as a fenced turn-0 data message;
it is not a tool, and the agent has no write path to it.

Loading is fail-soft. A missing, empty, unreadable, oversized, or
injection-flagged soul contributes no fragment rather than aborting a run. A
user soul wins over a project soul at `<workspace>/.mecatl/soul.md`; the project
soul is loaded only when project trust is enabled.

### Drift protection

mecatl records the SHA-256 of the cleaned soul body in a sidecar next to the
file, for example `~/.config/mecatl/soul.md.sha256`:

- the first load establishes a trust-on-first-use baseline;
- a changed soul logs a warning and still loads by default;
- `--approve-soul` accepts the current content by rewriting the baseline;
- `--soul-strict` withholds a drifted soul until it is approved.

This detects unexpected edits; it does not restore an old copy. The agent cannot
modify either the soul or its baseline.

## Trust and deployment limitations

Project skills, commands, and souls are repository-controlled steering content.
They are withheld from an untrusted workspace. Use project trust only when you
are prepared to admit the repository's instructions and related project-tier
configuration. The same trust decision also controls project rules and named
agent definitions; there is no separate skill-only trust switch.

For a remote deployment, content sources can be supplied by a driver:

```console
mecated serve \
  --skill-source-url 127.0.0.1:7443 \
  --soul-source-url 127.0.0.1:7443 \
  --command-source-url 127.0.0.1:7443
```

A remote skill source replaces local skill discovery and is snapshotted at
startup. A remote soul source occupies the user soul slot and is revalidated
locally. A remote command source composes with local commands and is consulted
live. Configure driver TLS and authentication as described in the
[configuration guide](https://github.com/stacklok/mecatl/blob/main/docs/usage/configuration.md); use only drivers you
trust.

The legacy `SkillDraft`/`mecated skills promote` path is a quarantine workflow,
not automatic publishing. A drafted skill is not active in the writing session.
An operator must review and promote it into an active directory, and it takes
effect after the next server start. Keep the quarantine outside the workspace
and separate from active skill directories.

## Next steps

- [Project instructions and rules](./project-instructions-and-rules.md)
- [Named agents](./named-agents.md)
- [Permissions and posture](./permissions-and-posture.md)
- [Usage: skills, soul, and user model](https://github.com/stacklok/mecatl/blob/main/docs/usage/skills-soul-usermodel.md)
- [Usage: workspace trust](https://github.com/stacklok/mecatl/blob/main/docs/usage/workspace-trust.md)
