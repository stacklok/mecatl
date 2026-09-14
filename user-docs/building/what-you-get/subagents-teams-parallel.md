---
sidebar_position: 9
title: Subagents, teams, and parallel
description:
  Delegate work to focused subagents, coordinated teams, and parallel branches.
---

# Subagents, teams, and parallel

Mecatl provides three delegation tools: **Subagent** for focused work,
**Parallel** for isolated writable or competing branches, and **Team** for
specialists that must coordinate. The model selects among them from their tool
descriptions.

This page describes when to use each tool, its call arguments, and how to
inspect or manage a running child. For implementation details, see the
architecture guides for
[subagents and teams](https://github.com/stacklok/mecatl/blob/main/docs/architecture/subagents-and-teams.md)
and
[parallelism](https://github.com/stacklok/mecatl/blob/main/docs/architecture/parallelism.md).

A child's transcript never enters the parent conversation. The parent receives a
bounded result or synthesis and lightweight status events. Use `InspectSubagent`
for a subagent or Parallel branch, or `InspectMember` for a team member, when
you need the child's transcript.

### Watch a delegation in `mecatui`

In `mecatui`, the collapsed Subagent card shows the child's current tool. Press
`ctrl+t` or use the `f6` agents overlay to view bounded previews of child tool
arguments, results, and messages. These previews are client-only and never enter
the parent conversation. A child's permission request is not forwarded. Team
views also show the task board, findings, member dispositions, mutating status,
and context meter.

## Subagent — delegate one task

`Subagent` spawns a single child agent with a fresh, empty context (unless you
ask it to inherit yours — see `fork` below) and hands it a self-contained
instruction. It's the right tool for "go investigate X" or "go make this focused
change" when the task doesn't need your current conversation. For multiple
independent **read-only** investigations, issue one Subagent call per task in
the same assistant turn: eligible calls run concurrently.

By default a subagent is **read-only**: it can Read/Grep/Glob and run
build/test/git commands in a throwaway worktree, but it has no `Edit`/`Write`
tools, and any file changes it makes along the way (e.g. via Shell) are
discarded when the run ends — nothing it does touches your working tree. Ask for
`mode: "read-write"` when you want it to actually change files (see below).

### Key call arguments

|Argument|What it does|
|-|-|
|`prompt`|The self-contained instruction. Unless you set `fork` (below), the child can't see your conversation, so state everything it needs, including the expected output format.|
|`description`|A short label shown wherever the subagent's progress is displayed.|
|`agent`|Route to a named specialist agent definition instead of the default explorer (see [Extension points: agent definitions](/building/extension-points/agent-definitions.md)).|
|`model`|Pin this call to a specific model — a cheaper one for wide fan-out, a stronger one for deep analysis. Omit to inherit the parent's model.|
|`max_turns` / `max_tool_calls`|Tighten-only caps on this call — you can make a subagent stricter than the operator's default, never looser.|
|`max_run_tokens`|A cumulative input+output token budget for this child engine's session, tighten-only. Omit it in almost all cases — the configured default is inherited independently and usually unlimited. This does not cap parent or delegation-tree spend. If you do set one, values below 25,000 are automatically raised to that floor, since the system prompt and project instructions get replayed every turn.|
|`timeout_ms`|A wall-clock deadline; exceeding it cancels the child and returns a time-budget error.|
|`output_schema`|A JSON schema for a structured result, when you need to mechanically consume the answer (e.g. comparing several subagents). The child is given a `SubmitResult` tool and must deliver by calling it; a bad submission gets up to two bounded correction retries before the call gives up.|
|`fork`|Seed the child from a copy of your current conversation instead of an empty one, so it inherits everything you've already established. Runs on your model; not combinable with `model`, `agent`, or `resume`.|
|`resume`|Continue a previous subagent (the `agentId` from its earlier result) with a follow-up prompt. It keeps its conversational memory but runs in a fresh workspace checkout — file changes from its earlier run are gone. Resuming resets the turn and tool-call counters but preserves cumulative token usage, so it does not replenish `max_run_tokens`. A `failed` subagent can be resumed like other terminal states.|
|`background`|Return immediately with the child's `agentId` while it keeps working; collect the result later with `SubagentStatus`.|

Every Subagent result carries an `agentId: <id>` trailer, whatever the outcome
(success, timeout, error). That's your handle for `InspectSubagent`,
`SubagentStatus`, or a later `resume` call.

### `mode: "read-write"` — land edits directly

Set `mode: "read-write"` and the subagent gets `Edit`/`Write` and works
**directly against your real workspace** — no fork, no copy, no merge step. Its
changes land immediately, exactly as if you'd made them yourself. There's no
isolation here: a crash or a bad edit can leave partial changes behind, the same
risk as an interrupted edit of your own. Your git history is the safety net
(`git diff` / `git checkout` / `git stash`).

Because it mutates your workspace directly, a `read-write` call always runs
serially. It never overlaps your other tool calls, even though the same
`Subagent` tool is otherwise read-parallel. `read-write` composes with `fork`,
`resume`, and `output_schema`; it's mutually exclusive with `background` and
with an explicit `agent`+`model` pair. A named specialist with no definition
`model:` can still be selected by the semantic router: the router's model choice
preserves the specialist's direct-write tools and instructions. This is not an
explicit all-three call. Pinned definitions, `fork`, and `resume` bypass
routing; inline-MCP definitions remain unsupported on the writable routed path.

### Background subagents

A `background: true` call immediately returns an `agentId` and keeps the child
running. Completion sends a note with the ID and outcome. Collect the result
with the `SubagentStatus` tool:

- No arguments — see the roster of this run's subagents and their state.
- `agent_id: <id>` — fetch that child's stored result (delivered exactly once).
- `wait_ms` — park for up to 120 seconds waiting on a result instead of polling.

A background subagent still running when your run ends is cancelled; its
transcript persists and can be resumed on a later run.

### Cancelling one child mid-run

Cancel one child without stopping the parent run through the gRPC
`ConverseRequest.cancel_child` field, `POST /v1/sessions/{id}/cancel-child` over
HTTP, or the `x` key in `mecatui`.

## Parallel — isolated writable or competing branches

`Parallel` accepts up to 16 branches per call, with up to 8 executing
concurrently by default. Each branch runs in its own isolated forked workspace
with a fresh context. Use it for isolated writable branches or competing
approaches when you need its built-in join or winner selection — not for
independent read-only investigations (issue separate Subagent calls in the same
assistant turn), or for tasks that need to coordinate or share state as they go
(that's what Team is for; Parallel branches never communicate with each other).

Each branch can implement, not just explore: it has the full read-write toolset
(Edit, Write, Shell) because its changes land only in its own isolated fork,
never in your shared workspace. Since a branch can't see your conversation or
the other branches, describe every task as fully self-contained — use the call's
`shared` field for context that applies to all of them.

### `join` — how the result comes back

|`join` value|Behavior|
|-|-|
|`all` (default)|Every branch's summary comes back so you can pick. Every fork is torn down after the join — copy anything you need out of the summaries, because the forks are gone.|
|`first`|The first branch that succeeds wins; the rest are cancelled. The winner's fork is **preserved** and its path is reported.|
|`judge` (or `best`)|An LLM judge picks the single best branch against your `criteria`. Same preservation as `first`.|

A **single isolated branch** with `join: first` or `join: judge` is the
supported conditional-merge case: its winner is auto-merged back into your
workspace by default. Its diff is applied via `git apply`, and the merge refuses
anything that touches `.gitattributes`. A **multi-branch** run never
auto-merges, even with `join: first`/`judge`. You inspect the preserved winner's
fork path yourself if you want its changes. A conflicting merge is never forced
— the tool error preserves the fork so you can resolve it by hand.

If you want to land a single task's edits without the isolated branch and
conditional merge, `Subagent` with `mode: "read-write"` is the direct tool.

Every branch reports a `branch id:` line; pass it to `InspectSubagent` to pull
that branch's bounded transcript, e.g. to see why a losing or failed branch went
the way it did.

## Team — coordinate specialists

`Team` forms a small crew of subagents that work together under a lead, rather
than in isolation. Reach for it only when a goal genuinely splits into parallel
specialist roles that need to compare notes as they go — say, an investigator
and a fixer, or several role-focused workers sharing a task list. It's the most
expensive of the three tools (several long-lived agents over multiple rounds),
so a typical roster is 2-4 members, and a single focused investigation should
still just be a `Subagent` call.

You specify the roster as a list of members, each with a `name`, a `role` (its
briefing), and whether it's `mutating`. The **first member listed is the
coordinating lead** — its role should describe how to break the goal into tasks,
what each task should produce, and what the final report must answer. Every
other member's role should describe its specialty, where to look, and an
instruction to call `RecordFinding` for each conclusion and message the lead
when done — findings are how the lead builds its consolidated report.

Members are **read-only by default**: full shell access in an isolated throwaway
git worktree, but no `Edit`/`Write`. Set `mutating: true` on a member that must
actually write code — it gets its own self-contained copied workspace (its own
`.git`) so its edits never touch yours. Neither tier is ever merged back: unlike
a Parallel winner, a team member's fork isn't preserved or reported anywhere you
can pull it from.

### The deliverable is the lead's synthesis, not a transcript dump

When the team finishes, `Team`'s result is **one consolidated report** the lead
writes after gathering every member's recorded findings and status — not a
concatenation of each member's raw output. Use that report as the answer; don't
re-derive the same conclusions the team already reached. If you need one
member's full detail — to debug why it reached a particular conclusion — call
`InspectMember` with the team id from the result.

### Team-wide token budget

A team-level token ceiling (operator flag `--max-team-tokens`, default
unlimited) sums usage across every member and round. If a call sets its own
`max_team_tokens`, it can only tighten the operator's default, never loosen it.
Crossing the budget stops the team from scheduling new rounds — the in-flight
round and the lead's final synthesis still complete, so you always get a report,
and it states that the budget stop happened. This is separate from the
per-engine token budget that every individual member inherits and enforces
against its own session usage.

## Automatic model routing

Beyond a flat `--subagent-model` default, an operator can configure a **model
router** that picks a model per delegation automatically, based on what the task
actually needs — a cheap model for a mechanical rename, a stronger one for a
subtle concurrency bug — instead of every delegation using the same one model.

**It's on the moment an operator configures it — there's no separate enable
flag.** A non-empty category taxonomy in the operator's `settings.yaml` _is_ the
switch:

```yaml
models:
  router:
    categories:
      - name: small
        description:
          trivial, mechanical, single-file edits; quick lookups; renames
        model: cheap
      - name: large
        description:
          deep multi-step reasoning, architecture, subtle concurrency bugs
        model: big
```

**It only fills a gap — it never overrides pinned intent.** It's consulted last,
after everything that could already decide the model on its own: your own
per-call `model`, a named specialist agent definition that already has its own
`model:` set, or `fork`/`resume` (which already run on a fixed engine). Once
none of those apply, it can route a plain or `mode: "read-write"` Subagent,
including a named specialist whose definition has no `model:`, an undefined team
member, or a Parallel branch — never the Parallel **judge**, which always stays
on your session's own model, since it's comparing your branches rather than
doing delegated work itself.

:::note[The one gotcha: `model: inherit` is not the same as no `model:` key]

If a named specialist agent definition has no `model:` key at all, it's eligible
for routing. Set `model: inherit` explicitly instead, and you've opted it _out_
— that's a deliberate pin to the session model, not "no preference." Worth
knowing if you maintain agent definitions and expect the router to route them.

:::

**It never blocks a delegation.** A classifier failure or hallucinated category
falls back to the engine the delegation would otherwise use. If the selected
routed model cannot be built, the ordinary model still runs and `routing_reason`
reports `route-target-unavailable`; for a writable named specialist this
fallback keeps its scoped direct-write engine. A run-scoped breaker gives up on
the classifier for the rest of that run after 3 consecutive misses (a hit resets
the count), rather than keep paying for a classifier call that keeps failing.

**You can see _why_ a delegation wasn't routed.** Every delegation-start event
carries a short `routing_reason`: empty when the router picked and successfully
built a model, otherwise a plain label like `router-disabled`, `pinned-model`,
`agent-def-pinned-model`, `route-target-unavailable`, `resume`, `fork`, or
`breaker-open`. mecatui shows it on the delegation's model line as
` · not routed: <reason>`, so you can tell "the router is off" apart from "this
agent pinned its own model" apart from "the classifier kept failing" or "the
selected target was unavailable" at a glance.

See
[Choose models and providers](/features/choose-models.md#configure-aliases-slots-and-task-routing)
for the taxonomy schema. The `--subagent-model-router` flag is a kill switch: it
can turn a configured router off, but it cannot enable a missing taxonomy.

## Configuring the defaults

A few operator-facing knobs shape delegation without any per-call argument:

- **`--subagent-model`** sets the model every Subagent / Parallel branch / team
  member uses when nothing else pins one (no `agent` def, no per-call `model`,
  no router pick). Leave it unset to inherit the parent's model.
- **`--enable-parallel=false`** turns off the `Parallel` tool entirely (on by
  default).
- Delegated children get their own slice of the permission system — see
  [Subagents and the permission model](/building/what-you-get/permissions.md#subagents-and-the-permission-model)
  for what a child is pre-approved to do versus what still surfaces to you as a
  human.
- **`--subagent-ask-reviewer`** (headless deployments only) lets an LLM
  adjudicate a child's permission ask instead of falling back to a blanket
  auto-deny when there's no human to ask. See the same section.

## What's next

- [`docs/architecture/subagents-and-teams.md`](https://github.com/stacklok/mecatl/blob/main/docs/architecture/subagents-and-teams.md)
  and
  [`docs/architecture/parallelism.md`](https://github.com/stacklok/mecatl/blob/main/docs/architecture/parallelism.md)
  — how delegation is actually implemented: workspace isolation, permission
  resolution, and the redaction boundary.
- [Subagents and the permission model](/building/what-you-get/permissions.md#subagents-and-the-permission-model)
  — what a delegated child is pre-approved to do, and how an unresolved ask
  reaches you (or doesn't, headlessly).
- [Extension points: agent definitions](/building/extension-points/agent-definitions.md)
  — define named specialists a `Subagent`/`Team` call can route to.
- [Core tools](/building/what-you-get/core-tools.md) — the rest of the default
  tool catalog.
