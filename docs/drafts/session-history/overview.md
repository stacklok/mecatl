# Overview: durable sessions beyond one replica

**Status:** Design draft, not shipped behavior.
This page explains the destination and scope; the [reading map](README.md) offers six independent review topics.

## The problem and the change

The goal is a session whose lifetime is independent of any one server replica or client connection.
Authorized clients can reliably reload and share it, failures leave an explicit account of what survived, and planned replica replacement can eventually preserve ongoing work without user intervention.
A reconnecting client should recover the retained conversation and activity, then follow new changes without missing the interval between loading and subscribing.
Work belongs to the server-owned session rather than the client connection that started it.

Today, a mutable session snapshot supplies continuation, an optional activity log records other facts, and live streams have no common durable cursor.
A client can see activity that a later client cannot recover.
Compaction can also make the model's current conversation a poor substitute for the session's full history.
The proposal replaces that split with **one authoritative, private, append-only committed history per session**.
Both model continuation and authorized client views derive from it.
Neither a checkpoint nor a display projection becomes another source of truth.

A session is the durable conversation; a run is one sequence of model turns within it.
Sessions outlive runs and client connections.
Multiple authorized clients can observe and control the same server-owned run without an exclusive controller.
Disconnecting does not cancel it.
Unsent drafts and presentation state remain local to each client.

## What the history makes possible

History retains accepted decisions, effective model inputs, requested tool calls, authorized tool intents, and permitted outcomes.
Required facts must commit **before** dependent model requests, tool dispatch, applicable hooks, or actionable publication.
This is durable orchestration inside Mecatl, not adoption or recreation of a general-purpose workflow platform.
It does not make external execution atomic with a storage append.

These retained facts also identify **safe pause points for planned handoff** between compatible replicas during upgrades or other maintenance.
The owner stops admitting work, lets active model requests and tool calls finish, and commits the outcomes and continuation needed to park the whole active delegation tree.
A successor acquires fenced ownership, verifies that committed handoff, and continues the same run without repeating completed work.
A commit gate alone does not prove the tree is safe to move; no model request or external tool call can remain in flight, and memory-only release holds need explicit settlement or invalidation.

**A successful planned handoff should be transparent to users.**
No new prompt, manual resume, or repeated accepted approval is required because a replica changed.
The client can reconnect and catch up automatically behind the scenes; transparent continuation permits a brief pause, not necessarily an unbroken network connection.
This [later capability](execution-and-recovery.md#planned-maintenance-pause-hand-off-continue) requires coordinated parking and server-side successor discovery in addition to portable history.

A crash can happen outside those safe points.
Consider a tool that changes a file, followed by a process crash before its result commits.
Recovery cannot infer that the change failed, or automatically repeat it to fill the missing result.
It reconstructs the evidence and can report an unknown outcome.
Replaying history never reruns a model, tool, or hook to reproduce a past result.
A separate authorized recovery action is different from replay.

A **commit** is storage acceptance under the adapter's declared durability and ownership guarantees.
A **phase** names work or waiting; entering one does not commit anything.
An **admission** is the eligibility decision to allow work, including the final cancellation and ownership checks before invocation.
These distinctions prevent a committed intent from becoming an irrevocable instruction to execute.
Ready facts can share a commit only when no execution gate separates them.

Live tokens and progress can still arrive promptly as explicitly provisional observations.
They may disappear after failure and cannot authorize work.
Confirmed results and durable subscription cursors follow accepted history.
A client loads complete authorized pages through one pinned position C, then follows changes after C without a silent gap.
Private continuation stays private even when the client requests all available display history.

## Safety includes deliberate absence

Complete capture means the effective logical model request, including request-only transformations and compatible private provider continuation.
It excludes credentials and does not require raw HTTP equality.
Reload must preserve cache-relevant representation, including argument key order, string whitespace, identities, and provider bindings.
It cannot promise a provider cache hit.

Disclosure rules constrain capture itself.
Unreleased output and payload-bearing hook evidence stay only in process memory, not in history, checkpoints, diagnostics, or retry receipts.
Losing that memory produces an explicit unavailable result, never permission to recreate the bytes by rerunning a tool.

Cancellation promptly closes execution admission and signals active work.
Its committed acknowledgement means accepted/signaled, not rollback or proven external cessation.
Bounded terminal settlement is separate again: a terminal run can still have a result awaiting release.
A later release never resumes the canceled run.
Successor admission invalidates remaining holds and late answers, and required predecessor tool-call/result pairing must finish before successor model input.
**Open:** Whether predecessor closure and successor admission share one atomic commit [remains undecided](cancellation-and-results.md#open-decisions-and-detail).

## First cutover versus later destination

The first cutover provides trustworthy reload, shared controls, complete logical input capture, required execution gates, and explicit interrupted-run recovery.
It exercises registered in-process extensions through `Subagent`, `Parallel`, and `Team` rather than leaving an unused abstraction.
Every Team is session-owned, including direct API entry without an originating tool call.
History references do not preserve workspaces or artifacts; those resources retain their own lifecycles.
High fidelity is the default, history is not trimmed, and existing payload bounds still apply.

A crashed active run is settled as interrupted, not automatically continued under its old identity.
Reconstructible pending execution approvals remain resolvable; lost memory-only release holds do not.
Later work includes graceful compatible-replica tree handoff, autonomous continuation, cross-run background execution, historical forks, rewind, networked extensions, and artifact spillover.
Portable history alone does not deliver those capabilities.

The rollout deliberately breaks storage and client compatibility.
Distinct namespaces and fail-closed version checks prevent migration, fallback, dual writes, or mixed-version rollout.
Legacy bytes remain untouched; the service is stopped and upgraded.
Short infrastructure PRs and later explicit runtime integration are development staging, not per-session switching or compatibility.
No new release approver, generic rollback layer, universal prepare/apply protocol, or speculative tool execution is introduced.

For the exact scope, see [delivery boundary](session-commit-history.md#delivery-boundary) and [cutover contract](session-commit-history.md#breaking-storage-and-protocol-cutover).
Choose a [review packet](README.md) for the next decision rather than reading the architecture reference end to end.
