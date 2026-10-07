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

Place repository-wide instructions in `AGENTS.md` or `CLAUDE.md` at the root of
the selected instruction source. Add instruction files in subdirectories for
conventions that apply to those trees. Keep each file focused on facts the model
needs for work there:

```markdown
# Project instructions

- Run `task test` before declaring a change complete.
- Keep domain packages independent from adapters.
- Do not edit generated files by hand.
```

Treat instructions as untrusted model input. Keep credentials, bearer values,
and other secrets out of them. Use operator configuration, hooks, and permission
policies to enforce requirements; instruction files guide the model.

### Starting and nested guidance

The first model request includes guidance from the selected, admitted source
root through the starting folder, ordered from root to leaf. More specific
instructions apply to files beneath their directory. Sibling directories have
independent guidance.

For example, the default local source selected in `website/` starts at
`website/`. It does not infer a Git root or load `../AGENTS.md`. To include
repository-wide guidance, the operator must select a broader source rooted at
the repository.

After a successful built-in Read, Edit, Write, Copy, Move, or Remove on a file
in another directory, Mecatl discovers that directory's instruction chain for
the **next** model request. A first write to that directory, or another tool
call in the same batch, can finish before the model sees the guidance. Shell,
custom tools, MCP tools, ListDir, Glob, and Grep do not activate nested
instructions. Mentioning a path in chat does not load its guidance.

Scope labels use directories relative to the execution root. Instructions from
selected source ancestors above that root apply across the execution tree and
carry the label `"."`. The label `"nested"` applies only beneath that execution
folder. Guidance discovered in sibling directories stays independent.

### File precedence and source failures

In each directory, a nonblank `AGENTS.md` takes precedence. A missing or blank
`AGENTS.md` falls back to `CLAUDE.md`; a read error does not trigger fallback to
another file or host source.

Combined sources share the same instruction-content budget. In `replace` mode,
the first nonempty source wins as a whole. For example, a higher-priority source
with guidance in one sibling can suppress a lower-priority source's root
guidance even when the agent works in another sibling.

If Mecatl cannot read a later scope, valid guidance loaded earlier in the same
refresh remains available, along with a warning that does not expose instruction
content. A configured-source failure stops that refresh's source chain without
falling back to a lower-priority source. In combine mode, contributions already
admitted from later sources during that run remain available without rereading
those sources.

### Session lifetime and content limits

Mecatl retains automatically loaded guidance for the live session. Editing a
loaded instruction file does not refresh its body, even across messages,
permission approvals, and compaction. Further file operations can discover
additional directories. Restarting or reattaching to a different server process
rediscovers guidance from the current admitted source.

Automatically loaded instruction bodies are not saved in the conversation or
tool results. Explicitly reading an instruction file produces an ordinary saved
tool result. The server can release a settled instruction snapshot that contains
no guidance; a later file operation can still discover a newly created
instruction file.

The operator setting `harness_context.project_instruction_max_bytes` limits
combined retained instruction content to 65,536 bytes by default. To allow more
content, set a positive integer under `harness_context` in operator settings:

```yaml
harness_context:
  project_instruction_max_bytes: 131072
```

Omitting the setting uses the default. A separate metadata budget of the same
size limits retained scope and directory records. When a budget is reached,
Mecatl labels partial or omitted guidance in model context and sends warnings
to the client without exposing instruction content. Tools remain available
under their normal permission policy. The content limit bounds retained
guidance; a source backend can use more memory while reading a file. See the
[configuration reference](/reference/configuration.md).

### Guidance in child agents

Fresh-context children start with an empty instruction snapshot and read current
guidance from the parent's admitted sources. Conversation forks instead copy
loaded guidance, covered targets, and the discovery budget independently. Edits
to loaded files do not replace that guidance on the fork's first request.

An isolated child can discover nested instructions when the server maps its
relative execution paths to the admitted source, including the default
same-workspace source. Without that mapping, the child receives starting
guidance and a mapping notice. It does not infer nested guidance from its
checkout. Starting guidance includes admitted ancestors of the starting folder
when the selected source covers a broader tree.

Children without a filesystem retain independently admitted starting context
and have no file-based nested discovery. Each child's discovery and budget use
are independent of the parent and siblings. A child's checkout does not become
a new instruction authority.

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
examples, see
[Workspace trust](/features/security-and-execution/permissions-and-posture.md).

## Next steps

- [Define named agents](/features/agent-behavior/named-agents.md)
- [Skills, commands, and soul](/features/agent-behavior/skills-commands-and-soul.md)
- [Permissions and posture](/features/security-and-execution/permissions-and-posture.md)
