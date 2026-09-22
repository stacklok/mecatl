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

The first request includes guidance from the selected, admitted source root through
its starting folder, ordered from root to leaf. More specific instructions apply
to files beneath their directory; guidance from a sibling directory does not apply.
For example, a default local source selected in `website/` starts at `website/`:
it does not infer a Git root or load `../AGENTS.md`. If the operator selects a
broader source rooted at the repository, its root guidance also applies to work
in `website/`.

After a successful built-in Read, Edit, Write, Copy, Move, or Remove on a file in
another directory, Mecatl discovers that directory's chain for the **next** model
request. A first-touch write, or another tool call in the same batch, can finish
before the new guidance is visible. Shell, custom tools, MCP tools, ListDir, Glob,
and Grep do not activate nested instructions. Mentioning a path in chat does not
load its guidance.

In each directory, nonblank `AGENTS.md` wins; missing or blank `AGENTS.md` falls
back to `CLAUDE.md`. A read fault does not authorize fallback to another file or
host source. Scope labels refer to execution-relative directories: selected
source ancestors above the execution root apply across the execution tree (`"."`),
while `"nested"` applies only beneath that execution folder. Encountered siblings
remain independent. When sources are combined, their contributions share the
same content budget. In `replace` mode the first nonempty source wins as a whole:
a higher-priority source with guidance in one sibling can suppress a lower-priority
source's root guidance even when working in another sibling.

If a later scope cannot be read, valid guidance loaded earlier in the same refresh
remains available, alongside a content-safe warning. A configured-source failure
stops that refresh's source chain rather than authorizing a lower-source fallback.
In combine mode, contributions already admitted from later sources in that run
remain available without rereading those sources.

Mecatl retains automatically loaded guidance for the live session. Edits to an
already loaded instruction file do not refresh its body during that session,
including across messages, permission approvals, and compaction. New covered
file operations can discover additional scopes; restart or reattachment to a
different server process rediscovers guidance from the current admitted source.
Automatic instruction bodies are not saved in conversation or tool results;
explicitly reading a file still produces an ordinary saved tool result. A
settled, guidance-free server snapshot may be released; a later covered file
operation can still discover a newly created instruction file.

The operator setting `harness_context.project_instruction_max_bytes` limits the
combined retained instruction content to 65,536 bytes by default. For example,
set `project_instruction_max_bytes: 131072` under `harness_context` in the
operator settings to allow more guidance. The value must be a positive integer;
omitting it uses the default. A separate, equally sized metadata budget bounds
retained scope and directory records. When the budget is reached, Mecatl labels
partial or omitted guidance in model context and sends content-safe warnings to
the client. Tools remain available under their normal permission policy. This
limit applies to retained guidance, not necessarily to the memory a source
backend uses while reading a file. See the [configuration reference](/reference/configuration.md).

Fresh-context children start with an empty instruction snapshot and read current
guidance from the parent's admitted sources. Conversation forks copy already
loaded guidance, covered targets, and the discovery budget independently: edits
to loaded files do not replace that guidance on the fork's first request. An
isolated child can discover nested scopes when the server has mapped its relative
execution paths to the admitted source (including the default same-workspace
source). Without that mapping it receives starting guidance and a mapping notice,
not nested scopes inferred from its checkout. Starting guidance includes admitted
ancestors of the starting folder in a broader selected source. No-filesystem
children retain independently admitted starting context, but have no file-based
nested discovery. Child discovery and budget use do not change the parent or a
sibling's snapshot. The child's checkout does not become a new instruction authority.

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
