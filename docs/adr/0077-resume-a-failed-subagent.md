# ADR 0077 — A failed delegated child is resumable (Recover, not refuse)

- Status: Accepted
- Date: 2026-07-29
- Scope: The Subagent `resume:` path and the team supervisor's per-round session recovery
  (`engine/agent/subagent.go`, `engine/agent/teamsupervisor.go`)
- Supersedes: none (it reverses an undocumented in-code policy, not a prior ADR)

## Context

Issue #51 gave a MAIN session a third terminal-recovery seam:
`engine/session/session.go` (`Recover`), the failed→idle sibling of `Reopen`
(completed→idle) and `Interrupt` (cancelled→idle). Its own doc-comment names the
motivating case: "a transient provider failure (an upstream 5xx that exhausted the
resilience layer's retries) degrades to *retryable* instead of permanently bricking the
session".

Delegated children were deliberately excluded. `engine/agent/subagent.go`
(`resolveResumeSession`) kept its own per-state switch — completed→`Reopen`,
cancelled→`Interrupt`, idle→as-is — and hard-refused `StateFailed` with the rationale:

> A subagent is a one-shot delegated task: **a failed child carries no
> accumulated-user-context cost**, so the parent re-delegates instead of retrying a broken
> transcript.

The team supervisor encoded the same policy differently: it skipped `Reopen` entirely for
a `StopError` round and set `memberRT.nonResumable`, permanently benching the member.

**Issue #318 falsified the premise with production evidence.** Two independent
`mode:"read-write"` children (direct-write, [ADR 0041](./0041-direct-write-subagent.md))
died on the terminal 180s stream-idle stall after 58 and 52 turns, ~5M and ~3.5M
cumulative input tokens, and ~15 minutes of wall clock — with **mutations already applied
to the real workspace**. The accumulated cost was not "no cost"; it was strictly larger
than a main session's transcript, because it included applied side effects a fresh
delegation would have to rediscover in a half-edited tree. Both parents attempted
`resume:` and were refused.

Two further forces made the refusal indefensible rather than merely conservative:

1. **The history was clean.** Both snapshots died on the *next* provider call after a
   normal tool result, so the transcript was fully paired — the "broken transcript" the
   rationale worried about did not exist. And where it does, `Recover` already repairs it:
   `closeOutInterruptedTurn` closes every orphaned tool call with a synthetic error result
   carrying the FAILURE-accurate wording, so the replay stays provider-valid.
2. **The taxonomy inverted the penalty.** An operator-configured `timeout_ms` expiry
   cancels the run ctx, lands in `StateCancelled`, and was *already* resumable via
   `Interrupt`. A network stall becomes `StopError` — `internal/adapter/llmresilience/llmresilience.go`
   synthesizes a terminal `StreamIdleError` that unwraps to `context.DeadlineExceeded`,
   not `context.Canceled`, and the run ctx is never cancelled, so the loop's cancellation
   guard misses it and it falls through to `Fail()`. A network hiccup was therefore
   punished *harder* than a deliberate operator deadline.

## Decision

**All three terminal states recover on a child resume, matching the service layer's
`loadAndReopen` discipline.** `resolveResumeSession` gains
`case session.StateFailed:` → `loaded.Recover()`, wrapped in the same
"not in a resumable state" tool error as the `Reopen`/`Interrupt` arms. Everything
downstream is unchanged: tighten-only limits, the fresh fork, `Rehome`, the in-flight
guard, and the RESUME-START persist. Nothing is added to the domain.

**The team supervisor dispatches its per-round recovery on the session's STATE, not on the
run's stop reason.** `engine/agent/teamsupervisor.go` (`runTurn`) now calls `Recover` when
`m.sess.State == session.StateFailed` and `Reopen` otherwise. Keying on state rather than
`stop == StopError` is load-bearing: a `StopError` run does **not** imply a failed session
— the loop's `terminate` path calls `Fail`, but `terminateComplete` (which a text-bearing
turn carrying a terminal error stop chunk goes through) calls `Stop` and lands in
`StateCompleted`. Only the failed shape changes behaviour; every other state, deliberately
including a CANCELLED member, keeps its exact prior disposition.

`memberRT.nonResumable` now means what its name says — the recovery transition itself
failed — and is set only then. `stopped` is unchanged: a member whose round errored is
still descheduled, still releases its claimed tasks, and still reports
`StopReasonError`. **The recovered lead therefore reaches `synthesise`**, which gates on
`nonResumable`, so the team's deliverable survives one transient failure.

**The model is told.** `renderSubagentResult`'s `StopError` arm stamps
`subagentErrorResumeHint` after the agentId trailer, and the `resume` argument's schema
description says a failed subagent can be resumed. A recovery capability the model is
never told about is a capability it cannot use — the model-visible-affordance rule of
[ADR 0070](./0070-model-visible-affordance-gate.md). The hint deliberately lives in
`renderSubagentResult`, **not** in the `subagentErrorBody` helper it shares with the
Parallel branch-failure path: a Parallel branch id is `parallel-<callID>-<n>`, which
`validateResume`'s prefix gate rejects, so emitting the hint there would instruct the
model to take an action that cannot succeed.

**A resumed WRITABLE child is told its edits survived.** `resumeStalenessNote` ("file
changes … are GONE") is true only for the read-only path, whose worktree really was torn
down. A writable child never forks, so its earlier edits are still in the real tree;
`resumeWritableNote` states that instead. Shipping #318 while telling the recovered child
its work was lost would have contradicted the fix at the one layer that matters.

**The contract is Recover's, unchanged: retry becomes POSSIBLE, not guaranteed.** A child
whose cause is permanent (bad credentials, a poisoned history the pairing repair cannot
fix) re-fails cleanly on the next drive, and the parent still holds the conversation. A
genuinely non-resumable state — a snapshot still recorded `running`, the shape a process
that died mid-turn leaves — remains a model-addressable tool error via the `default:` arm.

## Consequences

**Easier.** A long-running direct-write delegation survives a transient provider failure:
the parent resumes by agentId and the child continues against the real tree, on top of its
own partial edits. A team survives one member (or lead) failure with its deliverable
intact instead of degrading to the labelled fallback. The `resume:` policy is now one
rule — "recover whatever terminal you find" — instead of a per-state exception list that
had to be re-justified at every seam.

**Harder / accepted costs.**

- **A permanent-cause child can be retried repeatedly.** Nothing here bounds how many
  times a parent resumes a child that will always fail; the brakes are the existing ones
  (`MaxRunTokens`, `MaxTurns`, the team round cap, the token budget). This is the
  deliberate trade: a bounded waste of provider calls in exchange for never stranding
  recoverable work. The hint's wording ("if the failure looks transient … or start a fresh
  subagent") is the model-facing mitigation.
- **The synthetic close-out enters the replayed history.** A child whose failed turn
  orphaned a tool call replays with a fabricated error result. It is accurate (it says the
  run failed before the result was recorded, never that a user cancelled) but it is still
  a message the child did not produce.
- **`nonResumable`'s reachable surface shrank.** With the current state machine, `Recover`
  from `StateFailed` and `Reopen` from `StateCompleted` cannot fail, so the supervisor's
  `nonResumable` branch is now reached only by a CANCELLED member (whose `Reopen` is
  illegal by design). The flag and its WARN are kept as the honest fail-closed path for a
  future state, not deleted.
- **One acceptance bullet of #318 is NOT delivered.** "A team member that hits one
  transient stall still participates in later rounds" needs more than recovery: `planRound`
  only schedules a member that has drained messages or a claimable task, and an unbenched
  failed member keeps its in-progress claim, so it would be neither rescheduled nor
  released — and `outcome` would report it `DispositionDone`, hiding the failure from the
  lead. Genuine rescheduling needs a task-release + bounded-retry + disposition-honesty
  design, which is a separate change; recovery for the SYNTHESIS path is what this ADR
  commits to.

## See also

- [ADR 0041](./0041-direct-write-subagent.md) — the direct-write child whose applied
  mutations make a discarded failed child expensive.
- [ADR 0070](./0070-model-visible-affordance-gate.md) — the model-visible-affordance rule
  the resume hint satisfies.
- [ADR 0014](./0014-agent-teams.md) — the team supervisor whose per-round recovery this
  changes.
- [ADR 0027](./0027-cloud-native.md) — the run-entry recovery discipline
  (`loadAndReopen`) the child path now matches.
- [docs/design/IMPLEMENTATION-NOTES.md](../design/IMPLEMENTATION-NOTES.md) — the living
  per-subsystem reference for the resume path.
