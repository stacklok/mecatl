# ADR 0200 — A failed delegated child is resumable (Recover, not refuse)

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
`mode:"read-write"` children (direct-write, [ADR 0077](./0077-direct-write-subagent.md))
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
failed — and is set only then. `stopped` keeps its MEANING: a member BENCHED by its errors is
descheduled, releases its claimed tasks, and reports `StopReasonError`. But a member still
UNDER the retry cap is no longer benched at all — it is rescheduled with its tasks released
and no stop reason set (see *The team member's bounded retry* below, part of this same
decision). **The recovered lead therefore reaches `synthesise`**, which gates on
`nonResumable`, so the team's deliverable survives one transient failure.

**The model is told.** `renderSubagentResult`'s `StopError` arm stamps
`subagentErrorResumeHint` after the agentId trailer, and the `resume` argument's schema
description says a failed subagent can be resumed. A recovery capability the model is
never told about is a capability it cannot use — the model-visible-affordance rule of
[ADR 0070](./0070-model-visible-affordance-gate.md). The hint deliberately lives in
`renderSubagentResult`, **not** in the `subagentErrorBody` helper it shares with the
Parallel branch-failure path: a Parallel branch id is `parallel-<callID>-<n>`, which
`validateResume`'s prefix gate rejects, so emitting the hint there would instruct the
model to take an action that cannot succeed. `subagentResumeHint` is the one gate, and the
same rule gives it two silent cases: no wired session store — `validateResume`'s FIRST
precondition — so a `SubagentTool` built without `WithSubagentStore` (a supported
construction for an engine-module consumer, ADR 0036) stays silent rather than advertising
a `resume:` it will refuse; and the WRITABLE arm, which owns a single combined
next-action instead (below).

**A failed DIRECT-WRITE child gets ONE decision, not two.** "Its edits may be PARTIAL,
undo them with `git checkout`" and "resume it to continue where it left off" are both true
of a failed `mode:"read-write"` child, and as two independent imperatives a model can
follow both — discarding the edits, then resuming a child that is immediately told its
edits are still in place. `renderWritableSubagentResult` therefore carries
`writableSubagentFailedNote`, which states the two options as mutually exclusive and ends
in an explicit "Do not do both", and the generic hint is suppressed for that arm. A
store-less deployment keeps the plain review-or-undo note, offering no resume at all.

**A resumed child's note is a function of TWO independent axes, and all three reachable
cells exist.** WHERE the child runs follows THIS call's `mode`; WHAT survived follows the
EARLIER run's. `validateMode` lets `mode` compose with `resume`, so the two genuinely
disagree, and `resumePosture.note()` (`engine/agent/subagent.go`) enumerates the space in one
place rather than leaving a cell to fall through:

| this call's `mode` | earlier run's edits on disk | note |
|---|---|---|
| read-only | never (a read-only call forks) | `resumeStalenessNote` — fresh checkout, earlier work GONE |
| `read-write` | yes (the earlier run was direct-write too) | `resumeWritableNote` — real tree, edits STILL IN PLACE |
| `read-write` | no (the earlier run was read-only) | `resumeWritableFreshNote` — real tree, earlier work GONE |

The edits axis is keyed on the resumed snapshot's PERSISTED workspace matching the real
parent root, **not** on the current call's `mode`, because a previously read-only child's
worktree is gone: telling it otherwise is the worse falsehood (a read-only child has no
Edit/Write but does have Bash in that worktree, so it may really have applied edits, and a
child that trusts absent edits builds on nothing). The third cell exists because the two
notes that came first covered the edits axis on both cells and the WORKSPACE axis on
neither: a previously read-only child resumed `read-write` — exactly the call
`writableSubagentFailedNote` now tells the parent to make — was told it was "running in a
FRESH workspace checkout" while holding Edit/Write on the operator's real repository, and a
child that believes it is in a scratch checkout may rewrite or delete files to "start clean".
The tool schema's `resume` description states the same two axes separately, and
`TestResumeNoteMatrixCoversBothAxes` asserts the whole cartesian product per axis rather than
one cell's string.

**The PER-CALL TIME-BUDGET terminal gets its own next action.** A `timeout_ms` expiry was
the last failure path naming no recovery at all, while every neighbouring terminal
(`StopError`, the limit stops, `StopNoProgress`, the no-summary floor) named one — and left
silent it reads as *the* exception, teaching the model that this particular delegation is
simply dead. A timed-out child lands `StateCancelled`, which `resolveResumeSession` has
always recovered, so it is the SAME affordance. Its wording is its own pair rather than a
reuse of the `StopError` notes for two reasons: the child ran out of clock, not competence
(so "if the failure looks transient" would misdescribe it), and the knob that fixes a
genuine overrun is nameable — a larger `timeout_ms` on the resuming call. `subagentTimeoutNote`
is the one gate and mirrors `subagentResumeHint`'s shape exactly, including its two silent
cases, so the timeout path cannot drift from the `StopError` path's policy.

**Both resume-offering DIRECT-WRITE notes name `mode:"read-write"` explicitly.** `writable`
is derived from the CURRENT call's `mode` only; `run()` never infers it from the loaded
session. So the obvious follow-up — `Subagent{resume: id, prompt: "finish it"}` — returns
the READ-ONLY explorer: no Edit/Write, a fresh worktree fork off committed HEAD (the partial
edits are not even visible to it), and the staleness note telling the child its changes are
gone, while the operator's real tree still holds the half-finished work. "Resume it to
finish on top of them" without the argument that makes it true is the same
harness-asserts-a-falsehood class this ADR exists to close, on the most expensive failure
path in the tool.

**Neither half of a failed child's model-facing body is harness-authored, so both are
framing-neutralised.** The CAUSE is a provider/transport error string verbatim (the
anthropic adapter returns the upstream `error.message` unquoted, so real newlines survive
it) and `final` is child-authored prose; both are composed into the parent's persisted
conversation immediately beside the harness's own imperatives — the `agentId:` trailer the
model resumes by and the resume hint. `subagentErrorBody`, being the ONE composer, is where
neutralisation is applied, and `framingHeader` gained the headers this result emits so
an error string echoed from a hostile MCP server cannot forge one of them (CWE-1427 /
OWASP LLM01).

That treatment is not confined to the failed arm. EVERY delegation-result arm wraps the same
harness markers around model-influenced text — the success arm stamps the same `agentId:`
trailer and the same bracketed notes around child prose, and a read-only child has
WebFetch/WebSearch/Read, so a hostile page it summarises reaches the COMMON path, not only the
failure path. `neutraliseChildText` is therefore applied once at the top of
`renderSubagentResult` and at the one point a Parallel branch's summary is assigned, and
`framingHeader` covers the Parallel join report's own scaffolding (`branch id:`,
`=== branch-N [OK|FAILED|WINNER] ===`, the winner-workspace paths, the judge rationale) as
well as the Subagent result's. Three mechanics make that hold rather than merely claim it:
matching NORMALISES the line first (line terminators folded, Unicode `Cf`/`Cc` stripped), so
one invisible character or a bare CR no longer hides a forged header from a prefix match;
whole-line redaction has a FLOOR (`neutraliseChildText` returns the `%q`-quoted original when
neutralisation left nothing informative), because a redaction that erases a genuine one-line
provider error reintroduces exactly the opaque failure this ADR set out to abolish; and the
coverage is asserted by feeding each renderer's OWN output back through it as a forged body
(`TestDelegationResultMarkersCannotBeForged`, `TestParallelJoinMarkersCannotBeForged`), so a
marker added to a renderer without a `framingHeader` entry fails a test instead of shipping.
The accepted residual is homoglyph substitution, which is strictly more work for an attacker
than one invisible character and is documented at `canonLine`.

Two things the first cut of that got wrong, both fixed with the mechanism rather than the
symptom. **The marker list is SURFACE-TAGGED** (`framingSurface`): applying every entry to
every result meant ten fenced-PROMPT headers (`Policy:`, `Category:`, `Recorded findings:`, …)
were matched against delegation results, where they cannot be forged because nothing emits
them there — and where they ARE how a review subagent heads each finding, so two lines of
every finding came back to the orchestrating model as a redaction token on the SUCCESS arm.
`neutraliseChildText` evaluates the result surface only; the fence still runs the full list on
every prompt body, which is where those headers exist. It stays ONE enumeration with a tag per
entry, not two lists. The judge's rationale line is `Judge rationale:` and not the bare
`Rationale:` for the same reason: a marker has to be specific enough to be both matched and
harmless. **And the model-influenced INPUTS are what the forgery oracles must carry** — the
judge rationale shipped un-neutralised because the join oracle fed its forgery only through
the branch summaries, so a value with a listed marker and no neutralisation was invisible to
it by construction.

**The contract is Recover's, unchanged: retry becomes POSSIBLE, not guaranteed.** A child
whose cause is permanent (bad credentials, a poisoned history the pairing repair cannot
fix) re-fails cleanly on the next drive, and the parent still holds the conversation. A
genuinely non-resumable state — a snapshot still recorded `running`, the shape a process
that died mid-turn leaves — remains a model-addressable tool error via the `default:` arm.

### The team member's bounded retry

Recovery alone rescued only the lead's SYNTHESIS turn: `stopped` still descheduled the
member for the rest of the run, so #318's last acceptance bullet ("a team member that hits
one transient stall still participates in later rounds") needed three more things — a
bounded retry, a task release, and disposition honesty. All three are part of this decision.

**Bounded retry.** `engine/agent/teamsupervisor.go` (`runTurn`) does not bench a member
whose round ended `StopError` when the recovery seam SUCCEEDED and the member is still under
its cap: it stays schedulable. The cap is `Supervisor.memberErrorRetries`
(`WithMemberErrorRetries`, default `defaultMemberErrorRetries` = 1 — one retry survives a
network hiccup while capping the wasted spend of a permanently-failing member; 0 benches on
the first errored round, the SCHEDULING behaviour of the release before this one). The
counter, `memberRT.errorRounds`, is MONOTONIC and never reset, which is what makes the retry
provably terminating: a member that always fails runs `cap+1` rounds and is then benched
with the unchanged disposition, and the round loop still reaches quiescence far short of
`WithMaxRounds`. Three shapes are never retried: a FAILED RECOVERY (`nonResumable` — the
session cannot be driven at all), a CANCELLED member (a kill is not a transient failure, and
D5's disposition must hold), and a member that exhausted its LIFETIME TURN BUDGET (the
ceiling exists precisely to stop rescheduling it).

**Task release.** A retried member releases its in-progress claim through the existing
`engine/team/team.go` (`ReleaseTasks`) and returns to `team.MemberIdle`, following the
shape of the idle-between-rounds cancel path in `planRound`. Without it the work would be
stranded: `InProgressFor` short-circuits `planRound`'s auto-claim, so neither the member
nor a peer could pick the task up again.

`planRound` additionally FORCE-SCHEDULES a retried member for exactly one turn
(`memberRT.retryPending`, cleared on schedule). "Not stopped" is not sufficient to be
rescheduled — `planRound` plans only a member that drained a message or claimed a task,
and the commonest stall shape is a member dying on its first long exploration turn, before
any task exists — so without the one-shot the retry would be a silent no-op in
precisely the case #318 reported. The retry turn carries `retryTurnNote`: a
supervisor-authored line stating that the previous turn failed mid-flight, that the
member's own transcript above is the context to continue from, and that its task claim was
released. It is harness metadata, nothing quoted from the failed turn, so it renders
TRUSTED like the roster — and its literal header joins `framingHeader`, so a peer message
body in the same prompt cannot forge a second copy of it.

**Disposition honesty, in both directions.** `MemberDisposition` gains NO value — it is a
closed enum mirrored on the proto wire, and "done" is still the honest terminal for a
member that finished. Instead `agent.MemberOutcome` gains an additive count `ErrorRounds`,
mirrored on `engine/session/event.go` (`TeamMemberDisposition`) and on
`contracts/proto/mecatl/v1/harness.proto` (`TeamMemberDisposition.error_rounds`, field 4),
so `team.end` carries it: a retried-then-finished member is otherwise byte-identical on the
wire to one that never failed. `cmd/mecatui` renders such a lane `done (retried)` rather
than a bare `done`. The count is a LIFETIME count and therefore INDEPENDENT of the
terminal — a member that failed a round, was retried, and was then cancelled reports Reason
`cancelled` with `ErrorRounds` 1 — so a consumer reads the two together and derives neither
from the other. And the LEAD is told through the EXISTING trusted status section —
`buildSynthesisSources` → `writeTeamStatus` (renamed from `writeStoppedMemberStatus`) —
which names the retried members and their failed-round counts alongside the stopped
ones. A silently-retried member is a coordination lie in the opposite direction from the
one the stopped line closes: the lead re-plans and reports on what it believes members did.
The line is supervisor-authored metadata carrying only a count, so the gauntlet-#7 footing
is unchanged — with one hardening: the member NAMES it interpolates come from the parent
model's `Team` call args, so they are `NeutraliseFraming`'d like every sibling
interpolation in that file, or a crafted name could splice a forged section into that
trusted, unfenced region.

## Consequences

**Easier.** A long-running direct-write delegation survives a transient provider failure:
the parent resumes by agentId and the child continues against the real tree, on top of its
own partial edits. A team survives one member (or lead) failure with its deliverable
intact instead of degrading to the labelled fallback, AND that member keeps working in
later rounds rather than sitting benched. The `resume:` policy is now one
rule — "recover whatever terminal you find" — instead of a per-state exception list that
had to be re-justified at every seam. Every failure terminal `resume` can RECOVER now names
a next action, so none of those reads as a dead end — `StopStructuredOutput` included. That
terminal (the child never produced a schema-valid payload within its correction budget)
surfaces the last validation error, which is the actionable half, and now names what to do
with it: fix the schema or the instruction and delegate again, or — where a session store is
wired — resume by agentId passing the SAME `output_schema`. The reason first recorded here for
leaving it bare, that "a resume would need the same `output_schema` passed again", was not an
obstacle: `resume` composes with `output_schema` (`validateResume` rejects only `agent`/`model`,
and the submit tool is built from the argument unconditionally), and the schema is the parent's
own argument. The genuine caveat is weaker and different — a child that failed validation
through its whole correction budget may fail a fourth time — so the resume is offered SECOND
and says so. For a DIRECT-WRITE child that terminal also carries the honest PARTIAL-edits note
rather than the benign clean-finish one.

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
- **A permanently-failing team member costs `cap+1` rounds of provider spend** instead of
  one — at the default cap, two rounds rather than one, i.e. exactly ONE extra scheduled
  round per member over the whole team run (the counter is never reset, so it is not one
  extra per failure). Bounded, and the reason the default cap is 1 rather than higher. It is
  the same trade as the `resume:` bullet above: bounded waste in exchange for never
  stranding recoverable work.
- **The cap has NO operator flag, deliberately.** It is a library option
  (`WithMemberErrorRetries`), and the repo's line is that **team-wide resource ceilings get
  operator flags while per-member behavioural bounds stay engine-only defaults**. The
  precedent is the sibling `WithMemberTurnBudget` (default 200 turns per member), which is
  likewise unwired at every composition root — NOT `WithTeamTokenBudget`, whose
  `--max-team-tokens` flag exists because it is a team-wide ceiling. An operator's control
  over the extra spend is therefore the token ceilings that already bind it,
  `--max-team-tokens` and the per-member CUMULATIVE `--max-run-tokens` (it lives on the
  member's session aggregate and `resetToIdle` preserves `Usage`, so it accumulates across
  rounds including the retry round — it binds tighter than a per-drive reading suggests), plus
  the built-in round cap and per-member turn budget. The cost of the choice is that an operator who wants fail-fast
  cannot get it without a code change; the cost of the alternative is a flag for every
  per-member default, which is the surface this line exists to hold.
- **Two writable-resume edges the notes do not cover, neither reachable in-tree.** A
  deployment that wires `WithAgentWritableEngineFactory` but NOT `WithWritableChildEngine`
  can render `writableSubagentFailedNote` (a writable specialist failed) and then refuse the
  very call it advertised, because `resume` rejects `agent` and `validateMode` answers
  "`mode:"read-write"` (writable subagent) is not supported in this deployment". And a
  resumed writable SPECIALIST silently loses its specialist scoping (resume forces the
  generic writable explorer), so the note's "resume *it*" is approximate — the edits still
  get finished, by a less specialised child. `app.Build` wires both seams, so both edges are
  engine-consumer-only; if one ever needs closing, `resumeSupported()` has a natural sibling
  (`writableResumeSupported() = t.writableChildEngine != nil`) to gate the two writable notes
  on. Recorded rather than fixed: minting a constant for an unreachable cell is the surface
  this ADR's note matrix exists to hold down.
- **A retried member re-drives a round whose side effects may already have happened.** A
  tool call that landed before the stream died is not undone, so `retryTurnNote` tells the
  member to CONTINUE rather than start over. The harness cannot know which side effects
  survived, so that is honest guidance, not a guarantee.
- **`ErrorRounds` does not follow from the disposition.** It is a monotonic lifetime count
  (the retry cap's termination proof), so a cancelled or budget-stopped member can carry a
  non-zero count. A consumer that reads "clean terminal ⇒ 0 errored rounds" is wrong; the
  two fields are read together.

## See also

- [ADR 0077](./0077-direct-write-subagent.md) — the direct-write child whose applied
  mutations make a discarded failed child expensive.
- [ADR 0070](./0070-model-visible-affordance-gate.md) — the model-visible-affordance rule
  the resume hint satisfies.
- [ADR 0014](./0014-agent-teams.md) — the team supervisor whose per-round recovery this
  changes.
- [ADR 0027](./0027-cloud-native.md) — the run-entry recovery discipline
  (`loadAndReopen`) the child path now matches.
- [docs/design/IMPLEMENTATION-NOTES.md](https://github.com/stacklok/mecatl/blob/773c6c4220c6cc8afa9e80976eb2e739efdce367/docs/design/IMPLEMENTATION-NOTES.md) — the living
  per-subsystem reference for the resume path.
