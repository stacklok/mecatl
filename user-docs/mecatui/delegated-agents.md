---
sidebar_position: 5.5
title: Work with delegated agents
description:
  Delegate investigations, implementation, and reviews from mecatui. Describe
  the outcome, steer delegation when needed, and supervise the work.
---

# Work with delegated agents

Describe your task in an ordinary prompt: the work, the files involved, the
result you need, and whether agents can make changes. Mecatl can choose how to
delegate using the tools available in your session. You can request a specific
mode when you want control over how the work runs. Use the live **Agents**
overlay to follow delegated work.

Start a [local session](./getting-started.md) or
[connect to your team's server](./remote-servers.md) with the project you want
to work on. In a remote session, file paths refer to the server's workspace.
The connected server's capabilities and permission policy determine which
delegation modes and tools are available.

## Understand how work runs

Use this comparison to understand Mecatl's choice or request a particular
approach. A task can stay in the main session when delegation adds little value.

|Your task|Choose|What to ask for|
|-|-|-|
|A small change or a sequence of dependent steps|Main session|Keep the work in the current conversation.|
|A bounded investigation or an independent review|Subagents|Give each child a specific question and ask for evidence. Independent read-only investigations can run concurrently.|
|A focused change directly in your working tree|A direct-write Subagent|Ask one child to implement the change in your workspace.|
|Implementation that should stay isolated until it succeeds, or competing implementations|Parallel|Give each branch a self-contained task and specify how to select the result.|
|A review or investigation where specialists need to exchange findings|Teams|Assign roles and ask the lead for a consolidated report.|

For lifecycle, workspace isolation, direct writes, result selection, and merge
behavior, see
[Subagents, teams, and parallel work](/features/agent-behavior/subagents-and-teams.md).

If you want help choosing, replace `<TASK>` and send this first.

**Copy this prompt into Mecatl:**

```text
I want to accomplish: <TASK>

Recommend the main session, Subagents, Parallel, or a Team using the tools
available in this session. Explain why, outline the work and expected result,
and identify whether it would change my working tree. You may inspect the
relevant files to make the recommendation. Do not edit files or start delegated
work until I approve the approach.
```

The recommendation appears in your main conversation. Once you approve it and
delegated work starts, use the controls below.

## Follow and stop delegated work

After delegation begins, the footer shows running and completed counts. Press
`f6` to open the **Agents** overlay. From its roster, use `tab` to switch among
**Subagents**, **Parallel**, and **Teams**, then select a child to inspect its
activity. `/team` opens the same overlay on the **Teams** tab when the server
supports it.

Press `ctrl+t` from the conversation to inspect top-level tool calls. A
Subagent call's detail includes bounded summaries of child tool activity; the
**Agents** overlay provides activity across all three delegation modes. These
previews are partial views, so use the main agent's final report to collect the
outcome. During a permission request, `ctrl+t` opens that request's details.

To stop a running background child, select it in the **Agents** overlay and
press `x`. Use `esc` to return from a child's detail to the roster and then to
the conversation. From the conversation, `esc` cancels the whole active run.
See [Work in the TUI](./using-the-tui.md) for tool inspection and run steering.

`/agents` opens the inventory of named agent definitions. Use `f6` to monitor
live delegated work. Opening either view does not start a delegation.

## Investigate a failure

Ask for a diagnosis and evidence before requesting a fix. Replace
`<TEST_COMMAND>` with the focused test command and `<RELEVANT_PATHS>` with the
code or test paths.

**Copy this prompt into Mecatl:**

```text
Investigate why this test fails:
<TEST_COMMAND>

Start with <RELEVANT_PATHS>. Reproduce the failure if the available tools and
permissions allow it, trace the likely cause, and return file and line
references, the relevant test output, and a suggested fix. Do not edit files.
If you cannot reproduce it, distinguish observed facts from hypotheses.
Summarize the evidence and next action in this conversation. If you delegate,
collect the findings before giving your diagnosis.
```

To request a separate investigation explicitly, add: "Use one read-only
background Subagent and collect its result." Open `f6` and select the child on
the **Subagents** tab. Use `x` there to stop it if the investigation is no
longer needed. If Mecatl works directly, follow its tool calls with `ctrl+t`
and cancel from the conversation with `esc`.

When it finishes, check that the report includes evidence and a proposed next
action before asking for an implementation.

For separate questions, such as tracing a request handler and checking its
tests, ask for independent read-only Subagents in the same prompt. Use a Team
when the investigators need to discuss findings as they work.

## Implement a focused fix in isolation

Ask for isolation when an implementation should run separately from your
working tree until it succeeds. For example, fix a CLI command that ignores its
configured timeout. Replace the placeholders with the command's paths, expected
behavior, and focused tests.

**Copy this prompt into Mecatl:**

```text
Implement this fix: <COMMAND> must honor its configured timeout and report a
timeout error when the operation exceeds it. Keep implementation isolated from
my working tree until it succeeds. If the available tools cannot provide that
isolation, explain the limitation before making changes.

Start with <RELEVANT_PATHS>. Keep the change focused, add regression coverage,
and run <TEST_COMMAND>. Report the files changed, the test
result, and any remaining limitations. Treat a failing regression test as
unfinished work.

Bring the successful change into my working tree through the supported merge
path, then summarize the resulting diff and test evidence. If implementation
or merge fails, report the failure and recovery options. Do not commit or push.
```

To select the mechanism explicitly, add: "Use Parallel with one isolated
writable branch and join: first." Follow the branch on the **Parallel** tab
under `f6`. Select a running branch and press `x` to stop it. If Mecatl cannot
meet the isolation requirement, review its explanation before agreeing to a
different approach. After completion, inspect the resulting diff and test
report before committing.

For competing approaches, ask for multiple branches and state the criteria
Mecatl should use to choose a result. Check the canonical
[Parallel result-selection and merge behavior](/features/agent-behavior/subagents-and-teams.md#parallel)
before choosing that workflow. Temporary branch workspaces are managed by
Mecatl; plan to collect the result during the delegation. For a focused fix
that can edit your working tree immediately, ask for a
[direct-write Subagent](/features/agent-behavior/subagents-and-teams.md#direct-write-subagents).

## Review a change across specialties

Ask for specialist perspectives when a change must work across several systems.
For example, review an authentication change for compatibility with Microsoft
Entra ID, Okta, and Keycloak and compliance with OAuth. Give each specialty
relevant sources, such as provider documentation or local reference repositories.

Finish your edits first and keep the review target stable while the review
runs. Replace `<REVIEW_SCOPE>` with a precise target, such as the current
uncommitted diff or a diff against a specific branch, and name the relevant
paths. Replace `<REFERENCE_SOURCES>` with the sources available to this session.

**Copy this prompt into Mecatl:**

```text
Review the authentication change in <REVIEW_SCOPE> for compatibility with
Microsoft Entra ID, Okta, and Keycloak and compliance with OAuth.

Use <REFERENCE_SOURCES>. Use existing specialist definitions where available,
and identify any missing sources or expertise. Compare findings across the
providers and reconcile disagreements against the evidence.

All reviewers must assess the same change, including the relevant uncommitted
files when that is the target. Confirm what diff is visible before reviewing;
report any inability to access it instead of reviewing a different version.

Keep the review read-only; do not edit files or apply fixes.

Return a consolidated list of actionable findings with severity, file and line
references, impact, and suggested verification. State any coverage gaps. If
there are no actionable findings, say so and identify what was checked.
```

When you want the reviewers to coordinate explicitly, add: "Form a read-only
Team. Make the first member the lead responsible for OAuth compliance and the
consolidated report. Assign the other members to Entra ID, Okta, and Keycloak.
Have them share evidence and resolve overlapping or conflicting findings with
the lead."

Use `/team` or `f6` and the **Teams** tab to follow a Team's activity, tasks,
and findings. Select a running member and press `x` to stop that child; cancel
the whole run from the conversation if you want to end the review. Read the
consolidated report in the main conversation before deciding which fixes to
request.

For independent reviews that only need a final comparison, ask for concurrent
read-only Subagents instead and follow them on the **Subagents** tab. Team
coordination is useful when reviewers need to resolve questions together. For a
disputed proposal, you can ask one reviewer to argue for it, another to argue
against it, and the lead to reconcile the evidence.

[Named agent definitions](/features/agent-behavior/named-agents.md) let you reuse
specialist instructions, tools, and sources for later tasks. Reusing a definition
does not keep its workers running between tasks. The deliverable from a Team
review is its consolidated report.

## If delegation is unavailable

Ask Mecatl which delegation tools are available in the current session. A
missing `/team` command or an unavailable tool can reflect the connected
server's configuration or policy. For a shared service, ask its operator about
availability and permissions; local client settings do not enable server tools.
You can continue the task in the main session while resolving access.

## Next steps

- [Inspect tools, steer runs, and handle approvals](./using-the-tui.md).
- [Understand delegation lifecycle and workspace behavior](/features/agent-behavior/subagents-and-teams.md).
- [Use named specialist definitions](/features/agent-behavior/named-agents.md).
