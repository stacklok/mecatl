---
slug: /features/project-instructions-and-rules
sidebar_position: 220
title: Project instructions and rules
description: Apply trusted project instructions and rules to Mecatl runs.
---

# Project instructions and rules

Add repository guidance so Mecatl follows your project's conventions.
`AGENTS.md` and `CLAUDE.md` provide general instructions; `.mecatl/rules/` and
`.claude/rules/` provide smaller rules. Mecatl loads project guidance only from
a trusted workspace.

## Availability

Project instructions and rules are available in `mecated`, `mecak8s`,
`mecatui`'s embedded server, and engine embeddings that wire the prompt
discovery sources. The sources are discovered at run/build time and assembled
into the model's prompt as fenced data. They do not add tools or bypass
permissions.

User-tier guidance is always available. Project-tier guidance is admitted only
when the workspace has an effective project-trust decision.

## Instruction files

Place repository-wide instructions in `AGENTS.md` or `CLAUDE.md` at the
workspace root or an applicable parent directory. Keep them focused on facts the
model needs for work in that tree:

```markdown
# Project instructions

- Run `task test` before declaring a change complete.
- Keep domain packages independent from adapters.
- Do not edit generated files by hand.
```

Mecatl combines the instruction files that apply to the workspace. A more local
file can refine guidance for its subtree. Instructions do not grant permission:
a tool call still follows the configured allow, ask, and deny rules.

Treat instructions as untrusted model input. Do not put credentials, bearer
values, or secrets in them. Do not use an instruction file as a substitute for
operator configuration, a hook, or a permission policy.

## Project rules

Rules are individual Markdown files under `.claude/rules/` or the Mecatl-native
`.mecatl/rules/` directory. A rule name comes from its filename; it does not
need a `name:` field:

```text
.mecatl/rules/
└── testing.md
```

A rule can be unconditional or use `paths:` frontmatter:

```markdown
---
paths:
  - '**/*_test.go'
---

# Testing rule

Run the focused test and the full package test before reporting success.
```

A rule without `paths:` always applies. For a rule with paths, Mecatl tells the
model to apply it only to matching files. The glob guides the model; it does not
restrict filesystem access.

Rules are discovered from these lanes, in descending precedence:

1. `<workspace>/.mecatl/rules`
2. `<workspace>/.claude/rules`
3. `$XDG_CONFIG_HOME/mecatl/rules` (fallback `~/.config/mecatl/rules`)
4. `~/.claude/rules`

Project rules override a personal rule with the same name. Discovery is
always-on and inert when no directory exists; there is no enable flag.

A rule body is capped at 20 KiB. The combined fragment is capped at 40 KiB and
32 rules. Excess content is dropped with a diagnostic. Missing directories,
unreadable files, malformed frontmatter, and discovery faults fail soft to no
rule fragment rather than aborting a run.

## Project trust

Project trust controls project `allow` rules, instructions, rules, soul, named
agents, slash commands, skills, and the read-only child shell. Project `deny`
and `ask` rules still apply when the workspace is untrusted.

Trust can come from:

- `--trust-project` for the current invocation;
- an exact absolute workspace entry in the user-global `trustedWorkspaces:`
  setting;
- an undrifted remembered trust entry created by `mecatui`; or
- the posture floor on an interactive root, where applicable.

A headless root does not infer trust from its posture. Configure an explicit
trust source when an unattended job must admit project instructions and project
authority. A malformed trust configuration leaves the workspace untrusted.

In `mecatui`'s embedded interactive server, the first encounter with a project
authority set can prompt:

```text
[t]rust (persist) / [o]nce (this run only) / [n]o (default)
```

`trust` stores a machine-owned identity anchor, `once` admits only the current
run, and `no` keeps the repository's project authority withheld. A non-terminal
stdin cannot answer the prompt and remains untrusted unless trust was configured
explicitly.

Remembered trust is tied to the workspace path and its soul, agents, commands,
and skills. Changes to that authority set require another trust decision. Mecatl
stores remembered trust outside the repository, so a repository cannot grant
itself trust.

## What untrusted means

You can still use an untrusted workspace. The built-in tools, your user-tier
soul, user-tier rules/skills/commands/agents, and every deny/ask rule remain
active. The withheld project authority set includes:

- project `allow` rules;
- project soul;
- project-tier `.claude/rules` and `.mecatl/rules`;
- project agent definitions;
- project slash commands and skills; and
- the read-only child/team-member shell that would require trusting the repo's
  Git metadata.

The main agent can still use its own tools, subject to normal permission asks.
Trusting a repository admits its instructions and related steering content; it
never automatically permits a tool that the policy denies.

## Remote and deployment limitations

- The local instruction and rule sources are filesystem discovery mechanisms. A
  remote driver can supply some other content sources, but its trust and
  lifecycle semantics are specific to that source; do not assume a remote source
  is equivalent to a checked-out repository.
- A remote agent, skill, soul, or command source is operator-configured and must
  be authenticated and trusted. Project rules currently remain a local workspace
  source.
- Project instructions are prompt guidance, not an isolation boundary. Use
  permissions, hooks, container boundaries, and workspace separation for
  enforcement.
- Trust does not make a shared filesystem multi-tenant safe. Separate workspace
  namespaces when callers must not see one another's files.

For the full trust precedence, drift behavior, posture matrix, and deployment
examples, see [Workspace trust](/features/security-and-execution/permissions-and-posture.md).

## Next steps

- [Define named agents](/features/agent-behavior/named-agents.md)
- [Skills, commands, and soul](/features/agent-behavior/skills-commands-and-soul.md)
- [Permissions and posture](/features/security-and-execution/permissions-and-posture.md)
