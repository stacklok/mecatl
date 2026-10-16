---
sidebar_position: 5.5
title: Work with delegated agents
description:
  Delegate investigations, implementation, and reviews from mecatui. Choose a
  mode, prompt Mecatl, and supervise the work.
---

# Work with delegated agents

Ask Mecatl to delegate through an ordinary prompt in your session. Describe the
work, the files involved, the result you need, and whether agents can make
changes. Use the live **Agents** overlay to follow their progress.

Start a [local session](./getting-started.md) or
[connect to your team's server](./remote-servers.md) with the project you want
to work on. In a remote session, file paths refer to the server's workspace.
The connected server's capabilities and permission policy determine which
delegation modes and tools are available.

## Choose a mode

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

## Investigate a failure with a Subagent

Use a read-only Subagent to trace a failing test while keeping the main
conversation focused on the decision. Replace `<TEST_COMMAND>` with the
focused test command and `<RELEVANT_PATHS>` with the code or test paths.

**Copy this prompt into Mecatl:**

```text
Use one read-only background Subagent to investigate why this test fails:
<TEST_COMMAND>

Start with <RELEVANT_PATHS>. Reproduce the failure if the available tools and
permissions allow it, trace the likely cause, and return file and line
references, the relevant test output, and a suggested fix. Do not edit files.
If you cannot reproduce it, distinguish observed facts from hypotheses.
Collect the child's result and summarize the next action in this conversation.
```

Open `f6` and select the child on the **Subagents** tab. Use `x` there to stop
it if the investigation is no longer needed. When it finishes, check that the
main agent's report includes evidence and a proposed next action before asking
for an implementation.

For separate questions, such as tracing a request handler and checking its
tests, ask for independent read-only Subagents in the same prompt. Use a Team
when the investigators need to discuss findings as they work.

## Implement a focused fix in isolation

Use Parallel when you want an implementation to run separately from your
working tree until it succeeds. For example, give one branch a specific fix
for a CLI command that ignores its configured timeout. Replace the placeholders
with the command's paths, expected behavior, and focused tests.

**Copy this prompt into Mecatl:**

```text
Use Parallel with one isolated writable branch and join: first to implement
this fix: <COMMAND> must honor its configured timeout and report a timeout
error when the operation exceeds it.

Start with <RELEVANT_PATHS>. Keep the change focused, add regression coverage,
and run <TEST_COMMAND>. The branch must report the files changed, the test
result, and any remaining limitations. Treat a failing regression test as
unfinished work.

Bring the successful change into my working tree through the supported merge
path, then summarize the resulting diff and test evidence. If implementation
or merge fails, report the failure and recovery options. Do not commit or push.
```

Follow the branch on the **Parallel** tab under `f6`. Select a running branch
and press `x` to stop it. After completion, inspect the resulting diff and the
main agent's test report before committing.

For competing approaches, ask for multiple branches and state the criteria
Mecatl should use to choose a result. Check the canonical
[Parallel result-selection and merge behavior](/features/agent-behavior/subagents-and-teams.md#parallel)
before choosing that workflow. Temporary branch workspaces are managed by
Mecatl; plan to collect the result during the delegation. For a focused fix
that can edit your working tree immediately, ask for a
[direct-write Subagent](/features/agent-behavior/subagents-and-teams.md#direct-write-subagents).

## Review a shared change with a Team

Use a read-only Team when reviewers need to compare evidence and reconcile
findings. For example, review an authentication change for correctness,
security, and test coverage before committing it. Finish your edits first and
keep the review target stable while the Team runs. Replace `<REVIEW_SCOPE>`
with a precise target, such as the current uncommitted diff or a diff against a
specific branch, and name the relevant paths.

**Copy this prompt into Mecatl:**

```text
Use a read-only Team to review the authentication change in <REVIEW_SCOPE>.
All reviewers must assess the same change, including the relevant uncommitted
files when that is the target. Confirm what diff is visible before reviewing;
report any inability to access it instead of reviewing a different version.

Make the first member the lead, responsible for reconciling findings and
producing the final report. Assign reviewers to correctness, security, and test
coverage. Have them share evidence and discuss overlapping or conflicting
findings with the lead. Keep all members read-only; do not edit files or apply
fixes.

Return a consolidated list of actionable findings with severity, file and line
references, impact, and suggested verification. State any coverage gaps. If
there are no actionable findings, say so and identify what was checked.
```

Use `/team` or `f6` and the **Teams** tab to follow the members' activity,
tasks, and findings. Select a running member and press `x` to stop that child;
cancel the whole run from the conversation if you want to end the review.
Read the lead's consolidated report in the main conversation before deciding
which fixes to request.

For independent reviews that only need a final comparison, ask for concurrent
read-only Subagents instead. Team coordination is useful when reviewers need
to resolve questions together.

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
