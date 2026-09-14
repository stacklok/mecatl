---
sidebar_position: 9
title: Subagents, teams, and parallel work
description:
  Choose focused subagents, isolated parallel branches, or coordinated teams for
  delegated work.
---

# Subagents, teams, and parallel work

Choose the smallest delegation tool that fits the work:

|Tool|Use it for|Workspace behavior|
|-|-|-|
|`Subagent`|One focused task|Read-only isolation by default, or direct writes with `mode: "read-write"`|
|`Parallel`|Isolated implementations or competing approaches|One temporary writable workspace per branch|
|`Team`|Specialists that must coordinate|One isolated workspace per member|

A child's transcript stays out of the parent context. The parent receives a
bounded result and status events. Use `InspectSubagent` for a Subagent or
Parallel branch, and `InspectMember` for a team member.

### Watch a delegation in `mecatui`

In `mecatui`, press `ctrl+t` or use the `f6` agents overlay to inspect bounded
child messages, tool arguments, and results. These previews do not enter the
parent's model context. Interactive clients can surface child permission
requests with an attributed, bounded summary; raw child arguments are withheld.

## Subagent

`Subagent` runs one self-contained task in a fresh context. Use it for a focused
investigation or change that does not require coordination with other workers.
Issue several read-only Subagent calls in one model turn to run them
concurrently.

By default, a child can read files and run commands in a temporary worktree. It
cannot use `Edit` or `Write`, and Mecatl discards any changes made through
Shell.

|Argument|Purpose|
|-|-|
|`prompt`|Provide the complete task and expected result. The child cannot see the parent conversation unless you use `fork`.|
|`description`|Label the work in status views.|
|`agent`|Use a named specialist definition.|
|`model`|Override the inherited model.|
|`max_turns`, `max_tool_calls`|Tighten the configured limits.|
|`max_run_tokens`|Tighten the child's cumulative token limit. Values below 25,000 are raised to that floor.|
|`timeout_ms`|Set a wall-clock deadline.|
|`output_schema`|Require a JSON result through `SubmitResult`. Invalid submissions receive up to two correction attempts.|
|`fork`|Copy the parent conversation into the child. This cannot be combined with `model`, `agent`, or `resume`.|
|`resume`|Continue a previous child in a fresh workspace while preserving its conversation and cumulative token use.|
|`background`|Return the child ID immediately and collect the result with `SubagentStatus`.|

Every result includes an `agentId`, including errors and timeouts. Use it with
`InspectSubagent`, `SubagentStatus`, or `resume`.

### Direct-write subagents

`mode: "read-write"` gives the child `Edit` and `Write` access to the parent's
real workspace. Changes appear immediately, with no fork or merge. An
interrupted child can leave partial edits, so use Git to inspect or recover
them.

Direct-write calls run serially and never overlap sibling tool calls. They work
with `fork`, `resume`, and `output_schema`, but not `background`. See
[Agent definitions](/building/extension-points/agent-definitions.md) for routing
restrictions on explicit agent and model combinations.

### Background subagents

`background: true` returns immediately while the child continues. Use
`SubagentStatus` with:

- No arguments to list this run's children.
- `agent_id` to collect one stored result. A result is delivered once.
- `wait_ms` to wait for up to 120 seconds instead of polling.

Mecatl cancels a running background child when the parent run ends. Its
transcript persists and can be resumed later. To cancel it sooner, use the gRPC
`cancel_child` field, `POST /v1/sessions/{id}/cancel-child`, or `x` in
`mecatui`.

## Parallel

`Parallel` runs up to 16 self-contained branches, with eight active by default.
Each branch has a writable isolated workspace and cannot communicate with other
branches.

Use Parallel for competing approaches or isolated implementation branches. For
independent read-only research, use concurrent Subagent calls. For workers that
must exchange findings, use Team.

|`join` value|Result|
|-|-|
|`all`|Return every summary, then remove all branch workspaces. This is the default.|
|`first`|Return the first successful branch and preserve its workspace.|
|`judge` or `best`|Use the parent model to choose one branch against `criteria` and preserve it.|

A single branch with `join: first` or `join: judge` merges its change into the
parent by default. Mecatl applies the diff with `git apply` and refuses changes
to `.gitattributes`. Multi-branch calls never merge automatically. A merge
conflict preserves the winning workspace for manual recovery.

Each branch result includes a branch ID for `InspectSubagent`.

## Team

`Team` coordinates specialists that need to share tasks and findings. It costs
more than an independent Subagent or Parallel call, so use it only when ongoing
coordination matters.

Each member has a `name`, `role`, and optional `mutating` flag. The first member
is the lead. Its role should explain how to divide the goal and what the final
report must answer. Other members should record each conclusion with
`RecordFinding` and notify the lead when finished.

Members are read-only by default. A mutating member gets a private writable copy
of the workspace. Team workspaces are never merged or preserved, so the durable
deliverable is the lead's consolidated report. Use `InspectMember` when you need
one member's detail, passing the ID from the result's `Team id:` line.

`--max-team-tokens` sets a cumulative team budget. A call can tighten that value
with `max_team_tokens`. When the team crosses the limit, it stops scheduling
rounds but lets the current round and lead synthesis finish.

## Automatic model routing

An operator can route delegation tasks to model aliases by defining categories
in user-global settings:

```yaml
models:
  router:
    categories:
      - name: small
        description: mechanical edits and quick lookups
        model: cheap
      - name: large
        description: architecture and subtle concurrency analysis
        model: big
```

A non-empty category list enables routing. The router fills only an unset model
choice. It does not override a per-call model, a specialist's pinned model,
`fork`, or `resume`. The Parallel judge also stays on the parent model.

In an agent definition, `model: inherit` is an explicit pin to the session
model. Omit the `model` key to allow routing.

Classification or model-build failures fall back to the model the child would
otherwise use. After three consecutive misses in one run, the router stops
classifying for that run. Delegation events report why routing was skipped or
why a target was unavailable.

See
[Choose models and providers](/features/choose-models.md#configure-aliases-slots-and-task-routing)
for the routing schema. `--subagent-model-router=false` disables a configured
router.

## Operator defaults

- `--subagent-model` sets the fallback model for children that do not have a
  pinned or routed model.
- `--enable-parallel=false` removes the Parallel tool.
- Child permission rules determine which delegated calls run automatically. See
  [Subagent permissions](./permissions.md#subagents-and-the-permission-model).
- `--subagent-ask-reviewer` lets a headless model review eligible child
  permission requests.

## What's next

- [Subagent permissions](./permissions.md#subagents-and-the-permission-model)
  for delegated approval behavior.
- [Agent definitions](/building/extension-points/agent-definitions.md) to define
  named specialists.
- [Core tools](./core-tools.md) for the rest of the default catalog.
