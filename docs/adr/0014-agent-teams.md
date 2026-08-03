# ADR 0014 — Agent teams

- Status: Accepted (substrate shipped)
- Date: 2026
- Scope: the team coordination kernel, supervisor, member lifecycle, workspace isolation tiers, gRPC/HTTP surface, and the lead-synthesis deliverable

## Context

mecatl's existing Subagent and Parallel tools were one-shot and context-isolated; there was no way for two running agents to see a shared task list, message each other, or self-coordinate over time. Claude Code's agent-teams feature demonstrated that a long-lived, multi-member orchestration substrate has real value for complex coding workflows. mecatl is headless (gRPC + HTTP), so the goal was the orchestration substrate exposed over the wire — not a TUI split-pane port.

## Decision

A team is modelled as "Parallel, but long-lived and talking." The coordination kernel (`engine/team`) is a pure domain object — mutex-guarded task list, mailbox, and member lifecycle — with no I/O or goroutines of its own. The supervisor drives member sessions via `Engine.Run` plus the new `session.Reopen` continuation seam, streams member events to the client, and produces a lead-synthesis deliverable. Workspace isolation follows a three-tier model: base-share, read-only worktree, and mutating force-copy fork. The lead synthesis is the team's canonical deliverable, with a three-tier fallback chain.

## Consequences

Teams are linearly more expensive than a single session; a per-engine and per-team token budget bound runaway costs. Join strategies for mutating-member forks and a `TeamStore` for restart durability remain deferred. Current behaviour is described in `docs/architecture.md`; shipped/deferred state is tracked in `docs/design/PRODUCTION-READINESS.md`.

---

The substrate (kernel, supervisor, coordination tools, Team tool, gRPC/HTTP surface, hook
phases, budgets, trust gate) is live; this doc retains the spike rationale plus inline
notes. Companion prototype: `engine/team/` (coordination kernel + tests). Research basis:
live surveys of Claude Code subagents and agent teams, the OpenAI Agents SDK, and Goose recipes (see "Sources" at end).

## 1. What we're trying to add, and why

mecatl today has two delegation tools, both one-shot and context-isolated:

- **`Subagent`** (`engine/agent/subagent.go`) — one read-only explorer child, shared
  workspace, drained internally, returns only final text.
- **`Parallel`** (`engine/agent/parallel.go`) — N parallel children, each in an isolated
  forked workspace, drained internally, joined into one summary, no auto-merge.

Both are **agents-as-tools** (the OpenAI SDK term): the parent calls a child, the
child's noise stays inside, only the result folds back. There is no way for two
running agents to **see a shared work list**, **message each other**, or **self-
coordinate** over time. That is exactly the gap Claude Code's *agent teams*
(experimental, v2.1.32+) fills, and what this spike designs for mecatl.

The key difference between *subagents* and *teams*:

| | Subagents (Subagent/Parallel) | Agent teams |
|---|---|---|
| Lifetime | one-shot | long-lived, multi-message |
| Visibility | drained internally | streamed to the client |
| Coordination | parent orchestrates everything | shared task list + peer mailbox |
| Communication | result only, child→parent | any member ↔ any member |

**Scope note — surface.** Claude Code's agent-teams *value* is largely its
interactive tmux/iTerm split-pane UX. mecatl is **headless** (gRPC + HTTP, no TUI
in core). So we are NOT porting the UX. We are designing the **orchestration
substrate** — shared task list, mailbox, lead/teammate lifecycle — exposed over
the gRPC surface so any client (including `cmd/mecatui` later) can drive and
observe a team. The substrate is the reusable, architecture-aligned part.

## 2. The reframing: a team is "Parallel, but long-lived and talking"

The single most useful realisation from the spike: **`Parallel` is already ~70% of the
plumbing.** It already:

- runs **N concurrent `Engine.Run` loops** (proven concurrency-safe; the Engine
  doc says one Engine backs the whole process and each Run owns its goroutine),
- gives each branch **its own fresh `session.Session`** and **its own workspace**
  via `tool.WorkspaceForker`,
- bounds fan-out and concurrency, fires `SubagentStop` per branch.

What a team adds on top of Parallel:

1. branches are **long-lived** (they don't terminate after one turn),
2. branches **stream to the client** instead of being drained internally,
3. branches share a **task list** and a **mailbox** and can **self-coordinate**.

mecatl also gets a simplification Claude Code cannot have: **teammates are
goroutines in one `mecated` process, not separate OS processes.** Claude Code
needs on-disk task files with **file locking** to coordinate across processes
(`~/.claude/tasks/{team}/`). In mecatl the task list and mailbox are **in-memory,
mutex-guarded domain objects shared by reference** — no file locks, no IPC, no
serialization races. That is the whole reason the coordination kernel (Part 6) is
small and testable.

## 3. The blocking architectural finding: sessions are one-shot

`Engine.drive` (`engine/agent/loop.go`) **always** terminates the session in a
single `Run`: even a clean end-of-turn calls `terminateComplete → sess.Stop()`,
moving the aggregate to `StateCompleted`, which is terminal. `Service.StartRun`
just loads a session and runs it. **There is no in-place "continue this session
with another prompt" seam.** `session.RecordUserPrompt` explicitly rejects
terminal states.

A teammate that must **receive a message at T1, act, go idle, receive another
message at T2, act again** does not fit a one-shot session. The spike needs a
continuation seam. Two options:

- **(A) `session.Reopen()`** — a new intention-revealing method on the aggregate:
  legal only from a non-failed terminal state (`StateCompleted`), it transitions
  back to `StateIdle`, clears `stop`, and preserves `Conversation` + `Counters`.
  The supervisor then re-drives the same session via `Engine.Run` with the
  incoming message as the next user prompt. **Small, guarded, reuses everything**
  (the loop, compaction, permissions, events). It also incidentally unlocks
  ordinary multi-turn chat continuation, which the app layer currently lacks.
- (B) Carry the `Conversation` into a fresh `session.New` + `ReplaceHistory` each
  continuation. Rejected: `ReplaceHistory` is "running only", loses counter/limit
  continuity, and re-runs `SessionStart`/instruction assembly every message.

**Recommendation: (A).** It is the minimal domain change and is generally useful
beyond teams. The state machine becomes
`idle → running → awaiting → … → completed → (Reopen) → idle → …`.

> Invariant to preserve: `Reopen` must be illegal from `StateFailed`/
> `StateCancelled`, matching the spirit of the existing terminal guards. A test
> asserting `Reopen` from each terminal state belongs with the change.
>
> Scope update: a `cancelled` session **is** now recoverable in-process for
> interactive multi-turn — but via a SEPARATE seam, `session.Interrupt()`, not
> `Reopen`. Interrupt is legal only from `StateCancelled`, recovers to `idle`,
> and repairs the interrupted turn's history (closing out orphaned tool calls)
> so the replay stays provider-valid. `Reopen` stays `completed`-only.
>
> Scope update 2 (issue #51): a `failed` session now recovers too — via a THIRD
> separate seam, `session.Recover()` (`failed → idle`, same history repair as
> Interrupt). The spike's invariant is still honored: `Reopen` was never
> widened (it stays `completed`-only, Interrupt stays `cancelled`-only); each
> terminal state has its own narrow seam.

## 4. Layering: where each piece lives

Dependencies point inward only (the project's load-bearing rule). The design slots
in without violating it:

- **`engine/team/` (NEW, DOMAIN leaf).** Pure coordination state + rules: the
  `Team` aggregate, `TaskList`, `Mailbox`, member lifecycle. Imports only
  `engine/session` (for `SessionID`) + stdlib. `session` never imports `team`,
  so no cycle. This is the **prototype delivered with this spike.**
- **`engine/session/`** — add `Reopen()` (Part 3). Domain.
- **`engine/governance/`** — add hook phases `TeammateIdle`, `TaskCreated`,
  `TaskCompleted` (mirror `PhaseSubagentStop`). Domain.
- **`engine/port/`** — optional `TeamStore` port if we want teams to survive a
  restart (mirrors `SessionStore`). v1 can run in-memory and skip this.
- **`engine/agent/` (APPLICATION).** A `TeamSupervisor`: owns the shared `*team.Team`,
  spawns one driver goroutine per member, re-drives each via `Engine.Run` +
  `Reopen` on message/task arrival, fans member event streams into one tagged
  stream, detects quiescence. Plus the member-facing **coordination tools**
  (`SendMessage`, `TaskCreate`, `TaskClaim`, `TaskComplete`, `SpawnTeammate`) as
  `tool.Tool`s backed by the shared `*team.Team`.
- **`internal/adapter/server/`** — new RPCs (Part 7); maps team events to proto.
- **`internal/app/`** — composition: build the supervisor, inject the team tools
  into the lead's / teammates' catalogs (read-only-share / mutating-fork per the
  agreed stance).

This mirrors exactly how `governance` (rules) and `permpolicy` (session-aware
adapter) are split, and how Parallel's child Engine is composed in `internal/app`.

## 5. Mechanics

### 5.1 Members, the lead, and the synthesis deliverable

The **lead** is an ordinary session whose catalog additionally has `SpawnTeammate`
+ the task/mailbox tools. **Teammates** are sessions spawned by the supervisor,
each with the task/mailbox tools (and `SendMessage`) always available even when a
referenced agent-definition restricts other tools (Claude Code does the same: team
tools bypass the allowlist). The lead is **fixed for the team's lifetime** and
**teams do not nest** (a teammate cannot spawn a team) — both match Claude Code's
limitations and our existing no-recursion guard.

**Lead synthesis is the team's deliverable (implemented).** After the scheduling
loop reaches quiescence (or the round/budget cap), `Supervisor.Run` drives ONE final
**synthesis turn** on the lead (`synthesise` → the shared `driveOneTurn` helper). Its
output is `TeamOutcome.Report`, which the Team tool returns as its `ToolResult` and
the gRPC `RunTeam` rides back on the outcome — so both entry points get the
consolidated report for free. The team GOAL is rendered as the lead's (and every
member's) **TRUSTED top-level instruction** — NOT fenced — because its provenance is
the principal (the parent model's tool call from the user's prompt, or the gRPC request
the deployment owns) and no member-facing tool can mutate it (`WithTeamGoal` is the sole
writer). This is the instruction-hierarchy / spotlighting / CaMeL consensus: the
principal's task is trusted; only peer/retrieved data is untrusted. The goal is still
run through `neutraliseFraming` so it cannot forge a fence or a section header
(defang-but-don't-fence). A relay/multi-tenant deployment that interpolates untrusted
end-user text into the goal re-fences it via `agent.WithUntrustedGoal(true)` (the in-loop
Team tool stays always-trusted; the gRPC path flips it through `server.Config.TeamGoalUntrusted`).
Fencing the goal as UNTRUSTED was the original behaviour and caused spurious refusals
(the member was handed its own job inside a "do not obey" block). The synthesis prompt's
remaining source material is assembled in three layers (`buildSynthesisSources`), all
fenced UNTRUSTED:

1. **The findings ledger** (`team.Team.Findings()`) — the PRIMARY, deterministic
   channel: members record conclusions with the `RecordFinding` coordination tool as
   they reach them, so a finding survives even if the member is later cut off at its
   limits. Grouped by member in append order.
2. **A LastText/completed-task digest** for members that recorded NO finding — the
   fallback that rescues a member cut off mid-investigation (root cause: a `LastText`
   that was frequently empty on a limit cutoff).
3. **The lead's drained inbox** — peer messages addressed to the lead, appended last.

A lead stopped purely
by its lifetime turn budget is still resumable: the ONE synthesis turn runs even then
(the report is the deliverable).

**The deliverable resolves through a three-tier chain — never a bare refusal or empty
(`deliverable()` in `teamtool.go`).** `synthesise` is a pure PRODUCER; the QUALITY gate
lives in the Team tool. Tier **1** returns the lead's synthesis when it is usable —
non-empty AND `!isNonDeliverable(report, len(Findings))`. Tier **2** is the ledger-rich
structured fallback (`joinTeamFallback`): the findings ledger grouped by member FIRST,
then per-member disposition + `[STOPPED: reason]` + completed tasks + last text — reached
when the synthesis is empty OR a non-deliverable. Tier **3** is an honest floor ("ran N
rounds, did not converge, M stopped") when even the ledger is empty — structurally
non-empty. `isNonDeliverable` is deliberately CONSERVATIVE: it fires only on
empty/whitespace OR (short `≤ 280 runes` AND a PREFIX-anchored match against the tiny
`refusalPrefixes` set AND `ledgerLen > 0`) — all three together, so a legitimately terse
real report is never discarded and a refusal over an empty ledger is left alone. A
non-convergence header (`convergenceHeader`) is prepended to tiers 2/3 always and to tier
1 when `!Quiescent` (a runaway team's plausible-looking synthesis still carries the "did
NOT converge" banner). The fallback SKIPS the lead's `LastText` (it IS the rejected
synthesis). The data (`TeamOutcome.Findings`, `MemberOutcome.Completed`/`.Lead`) is
snapshotted in `outcome()`, so the gRPC path gets the same rich fallback. Headline guard:
`TestTeamToolRefusalSynthesisFallsBackToLedger`.

> **SHIPPED (4A complete) — per-engine token ceiling AND team-AGGREGATE budget.**
> A SHARED loop-level *per-engine* token ceiling shipped: `agent.Deps.MaxRunTokens`
> (**default: unlimited** — `0` disables the brake), checked at the turn boundary in `Engine.drive` against THAT run's cumulative
> `session.Usage` (input+output, via `Usage.TotalTokens`). When the total crosses the ceiling
> the loop terminates CLEANLY via the completed path with `session.StopBudget` (a non-error
> terminal, Reopen-recoverable, string-passthrough on the wire — mirrors `StopNoProgress`
> exactly), so an in-flight turn always completes (no mid-stream abort → no-replay-after-first-chunk
> holds). It is composition-tunable (`app.Config.MaxRunTokens` → `--max-run-tokens`) and
> INHERITED by EVERY engine — main + Subagent + team member + lead synthesis + Parallel — via
> `engineDepsForProvider`/`childEngineDepsForProvider`. A per-call override may only TIGHTEN it.
> So each individual member run is now bounded, and a budget-stopped member surfaces the
> resilient-deliverable fallback the same way any stopped member does.
>
> **The team-AGGREGATE budget now closes the residual.** `Supervisor.WithTeamTokenBudget`
> (0 = disabled) is a supervisor-level accumulator: each member's per-drive `EvResult.Usage`
> (the run-cumulative figure — NEVER also summed from `turn.end`, which would double-count) is
> folded onto `memberRT.tokensUsed` in the same single-goroutine capture block as `turnsUsed`,
> before `Reopen`; `teamTokensUsed()` sums them on the single Run goroutine between rounds. The
> gate is checked at the ROUND boundary — BEFORE `planRound` (whose `Drain`/`ClaimNext` side
> effects must not fire for a round that never runs) — so the in-flight round always completes
> and the lead's synthesis turn still runs (its usage folds into `TeamOutcome.Usage` but never
> into the GATE — it runs after the loop and structurally cannot trip). Members are NOT
> individually stopped (no new `MemberStopReason`); the team simply stops scheduling.
> `TeamOutcome` gains `BudgetExhausted` + `Usage`; `teamStop` returns `session.StopBudget` when
> `!Quiescent && BudgetExhausted` (so a client distinguishes a budget-stop from a round-cap),
> and the deliverable header + the trusted synthesis-prompt "Team status:" line both state the
> budget stop. It is composition-tunable (`app.Config.MaxTeamTokens` → `--max-team-tokens`;
> `server.Config.TeamTokenBudget` for the gRPC CreateTeam path), and a per-call Team
> `max_team_tokens` may only TIGHTEN it (`tightenLimit`). It is ORTHOGONAL to the per-engine
> `MaxRunTokens` (which bounds ONE member drive and resets on `Reopen` each round) — both
> compose. Guards: `agent.TestTeamTokenBudget*` / `TestTeamToolTokenBudget*` /
> `TestConvergenceHeaderBudgetMatrix`, `server.TestRunTeamBudgetExhaustedOutcome`,
> `app.TestMaxTeamTokensPropagates`, `cmd/mecated.TestAppConfigMapsMaxTeamTokens`.
>
> **Wire surface (now also shipped, issue #36):** `CreateTeamRequest.max_team_tokens`
> (tighten-only against the server-configured budget) plus a terminal `TeamEvent.outcome`
> frame carrying the `TeamOutcome` wire projection (rounds, stop, budget_exhausted, usage)
> on BOTH RunTeam handlers (gRPC `grpc_team.go` + HTTP SSE `http.go`). No proto residual
> remains.

**On-demand member inspection (PULL).** Member sessions are persisted to the injected
`port.SessionStore` under collision-free, team-namespaced ids
(`MemberSessionID(teamID, member)` = `team-<teamID>-<member>`). The parent catalog's
`InspectMember` tool (read-only) loads ONE member's transcript by (team id, member)
and returns a bounded rendering — it does NOT auto-inject; the pulled transcript
enters the parent conversation only as that tool's own `ToolResult` (gauntlet #7's
no-auto-injection property holds).

**The Team ToolResult surfaces the team id (so `InspectMember` is reachable).** The
team id is the published Team call id; it rides `EvTeamStart` (a client-only event the
MODEL never sees). For the parent model to call `InspectMember` it must know that id —
so `renderTeamResult` prepends a `Team id: <id>` line to the Team tool's `ToolResult`
(BOTH the synthesis-report and the `joinTeamFallback` path), rendered VERBATIM so
`MemberSessionID(teamID, member)` reconstructs the saved member id byte-for-byte. Without
it the id was undiscoverable at runtime and `InspectMember` was unusable model-to-model
(the original defect; pinned by `TestParentDiscoversTeamIDFromResultAndInspects`). The
gRPC `RunTeam` consumer already holds the id from `CreateTeam`/`EvTeamStart`, so the
in-result header is the in-process-tool fix only.

**The lead's synthesis turn benefits from the loop's no-progress handler.** A lead whose
synthesis turn comes back empty (a reasoning-only / no-text turn) used to terminate at
once → `synthesise` returned `""` → `joinTeamFallback` skeleton. Because synthesis runs
through the SHARED `Engine.drive` (`driveOneTurn`), the no-progress handler now nudges the
lead up to `MaxNoProgressNudges` times INSIDE that one drive, so an empty first attempt is
driven to a real report before falling back. Pinned by
`TestLeadEmptySynthesisThenNudgedProducesReport`.

### 5.2 Message delivery: turn-boundary, not interrupt

A running `Engine.Run` is turn-based and we must not corrupt an in-flight turn.
So messages are delivered at **turn boundaries**, not as mid-turn interrupts:

```
member driver loop (per teammate, in the supervisor):
  for {
    inbox = team.Drain(me)                 // pending peer/lead messages
    task  = team.ClaimNext(me)             // next unblocked, unclaimed task
    if inbox empty and no task and team has open work elsewhere:
        team.SetMemberState(me, Idle); fire TeammateIdle; wait(signal)   // park
    if team.Quiescent(): break                                           // done
    prompt = render(inbox, task)           // synthesize the next user turn
    run = engine.Run(ctx, sess, ws, prompt)
    stream(run.Events(), tag=me)           // → client, NOT drained
    sess.Reopen()                          // ready for the next message/turn
  }
```

`wait(signal)` blocks on a per-member condition the `Mailbox`/`TaskList` signals
when a message is posted to `me` or a task `me` could claim becomes unblocked. No
busy-polling.

### 5.3 Workspace contention (the THREE-TIER model)

Workspace isolation is the security boundary; **capability flows down from the
parent** (which has Bash). A team member lands in one of three tiers (`AddMember`
picks the tier from `spec.Mutating` and the factory's `MemberBuild.IsolateReadOnly`):

- **base-share, no shell** (fallback when no read-only forker is wired): a read-only
  teammate (`code-reviewer`/`researcher`) shares the base — cheap, no copy — and gets
  Read/Grep/Glob only, NO Edit/Write/Bash, so it cannot corrupt the shared base;
- **read-only worktree, full shell**: a read-only teammate runs in a cheap git
  **worktree** (the forker's default mode — shares the base repo's `.git`, so it sees
  the **full commit history**) with Read/Grep/Glob **+ Bash**, but never Edit/Write.
  It can inspect with a real shell (`git log`/`git show`, `cat`, build, test) confined
  to a throwaway checkout. This is the common case once a shell is configured;
- **mutating copy, full shell**: an `implementer` teammate (Edit/Write) gets its own
  **force-copy** fork (own `.git`) with Edit/Write/Bash — parallel writes are safe
  because isolated.

- **merge/selection stays manual in v1** (consistent with Parallel's no-auto-merge):
  the team reports each mutating teammate's fork path; a later phase can add a
  judge/merge step (the "tournament" join). A read-only worktree is throwaway — never
  merged.

This keeps the read-parallel/mutate-serial invariant intact: nothing mutates the
shared base concurrently. The Supervisor holds **two forkers** — `s.forker`
(force-copy, for mutating members) and `s.roForker` (worktree, for read-only-isolated
members) — and `AddMember`'s mutating-tool backstop gates on **base-sharing**
(`!needFork`), so an isolated member's mutating-classified Bash is exempt (it lands in
the member's own fork/worktree, never the shared base).

> **Workspace-aware Bash (done).** Both forked tiers get **Bash** when a shell is
> configured. `BashTool.Execute` passes the per-member forked `Workspace.Root()` to
> `CommandRunner.Run` as the working directory (the `CommandRunner` contract carries
> an explicit `workdir`; an empty one falls back to the runner's configured root), so a
> forked member's Bash runs in its OWN fork/worktree — its **default cwd is the fork,
> not the shared parent base** — and the bash gate (`SplitCommands`/`ReadOnlyBash`) is
> unchanged. Residual: unlike path-scoped Edit/Write, Bash can still escape its cwd via
> absolute paths or `cd` — the inherent Bash trust model, the same as the main session;
> isolation is the boundary.
>
> **Shared-`.git` hardening (read-only worktree).** A worktree shares the parent
> repo's `.git/config` + `.git/hooks`, so an untrusted repo could run code via
> `core.pager` / `core.hooksPath` / `core.fsmonitor` / an external diff driver — **and
> `git worktree add` fires the base repo's `post-checkout` hook at FORK time, before any
> member runner exists.** The hardening closes these fixed-key + hook vectors but does
> **not** close attacker-named `.gitattributes` driver configs (see *Residual /
> follow-up* below). The single neutralizing environment lives in
> `internal/adapter/gitenv` (a stdlib-only leaf) so two code paths share it and cannot
> drift:
>
> 1. **The forker's own git** (`runGit`/`gitRepoRoot`/`forkWorktree`) sets
>    `cmd.Env = gitenv.Scrub(os.Environ())`, so the fork-time `post-checkout` hook (and
>    any config-driven exec a probe might trigger) is neutralized before the worktree
>    even exists.
> 2. **A read-only member's Bash** runs through a **sandboxed command runner**
>    (`internal/app.buildSandboxedCommandRunner`) given the COMPLETE
>    `gitenv.Scrub(os.Environ())` environment.
>
> `gitenv.Scrub` **DROPS** every inherited `GIT_*` variable (so `GIT_EXTERNAL_DIFF`,
> `GIT_SSH_COMMAND`, `GIT_ALTERNATE_OBJECT_DIRECTORIES`, `GIT_PROXY_COMMAND` etc. cannot
> leak in — an append could not remove these) plus `PAGER`/`LESS`, while keeping
> PATH/HOME so git still works, then **APPENDS** the neutralizing set: `GIT_PAGER=cat`,
> `PAGER=cat`, `GIT_CONFIG_NOSYSTEM=1`, `GIT_CONFIG_GLOBAL=/dev/null`, and
> precedence-winning env-injected config (`GIT_CONFIG_COUNT`/`GIT_CONFIG_KEY_n`/
> `GIT_CONFIG_VALUE_n`) forcing `core.hooksPath=/dev/null`, `core.pager=cat`,
> `core.fsmonitor=false` and an empty `diff.external` over the shared `.git/config`. The
> MAIN session keeps its UNHARDENED runner (operator hooks/pager honoured). A Mutating
> (force-copy) member's fork carries a VERBATIM COPY of the base `.git` — possibly an
> untrusted repo's config/hooks — so its hardened runner (`buildForceCopyRunner`) is
> load-bearing there too, not redundant. The per-command timeout (~30s) and the
> supervisor's concurrency cap (`defaultTeamConcurrency=4`) already bound a team's shell
> usage, so no extra per-member deadline/semaphore is added.
>
> **Residual — now closed by the trust gate (issue #40).** A fixed-key env override
> structurally cannot cover git config keys whose *driver name* is attacker-chosen in a
> tracked `.gitattributes`: `filter.<drv>.smudge` (executes at `git worktree add`
> checkout — fork time), `diff.<drv>.textconv` (executes on `git show` / `git log -p`),
> and `alias.<name>=!sh` if the member invokes that alias by name — the driver name is
> arbitrary, so no fixed `GIT_CONFIG_KEY_n` can pin it to an inert value. These were
> reachable only when the shared `.git` belongs to an **untrusted** repo; for a
> **trusted** repo (the operator's own) the execution is equivalent to the operator
> running git themselves — acceptable. The robust mitigation is **implemented**
> (issue #40): `buildSandboxedCommandRunner` is **gated on workspace trust** — an
> untrusted workspace yields no read-only subagent/member shell at all (the child
> degrades to Read/Grep/Glob, the Subagent Spec carries an honest no-shell note, and a
> read-only member's prompt says so), matching the harness's "untrusted degrades to
> ask-the-human" posture. A **Mutating** member and Parallel branches keep their
> hardened shells (`buildForceCopyRunner`) — ungated NOT because their copied `.git`
> is clean (`copyTree` copies the attacker's config, hooks, and `.gitattributes`
> **verbatim**) but because force-copy fork creation performs **no git invocation**
> (a pure FS copy: no checkout, the smudge filter never fires), unlike
> `git worktree add` — the fork-time auto-firing RCE this gate closes for read-only
> children cannot happen there. Their RUN-time git over the copied untrusted `.git`
> retains the attacker-named-driver residual above, accepted at **main-session
> parity**: the operator's own ungated (and unhardened) loop runs git in the same
> untrusted repo. The env scrub remains as defence-in-depth everywhere.

### 5.3.1 Member permission asks (4-step resolution, not a blanket deny)

A member's Bash permission ask was once auto-DENIED unconditionally, so a read-only member
could never run a command containing substitution/subshell (`$(go list ./...)`, a per-package
coverage loop) — it hard-failed with a misleading "denied by user" even though no user was
asked. Members (and Subagent/Parallel children) now resolve an ask in four steps (`resolveChildAsk`,
threaded by a per-child `childPosture`; the same path Subagent/Parallel share, so it cannot drift):

1. **Read-only substitution (A1, global).** `governance.SubstitutionReadOnly` lets a
   substitution whose every recursively-extracted inner command is read-only AND whose blanked
   outer is read-only resolve normally instead of flooring at Ask. A SEPARATE classifier —
   `ReadOnlyBash`/plan-mode/fuzz Inv-5 are unchanged.
2. **Isolation auto-approve (A2).** An ISOLATED member (worktree or force-copy fork — every
   shell-bearing member is isolated) auto-APPROVES `governance.IsolationApprovable`: read-only ∪
   a MINIMAL worktree-safe `{go test,build,vet,list}`, minus worktree-escape verbs
   (`git push/config/remote/fetch/pull/clone/worktree/submodule`). The git subcommand is
   resolved past leading global flags (a path-bearing `git -C`/`--git-dir`/`--work-tree` escapes
   the worktree ⇒ not approvable), and a worktree-safe `go` verb carrying
   `-exec`/`-toolexec`/`-overlay` (arbitrary external-program execution that FS isolation does not
   contain) is rejected. Fail-safe false on any ambiguity.
3. **Surface to the human.** When the parent run is interactive (the in-loop Team tool threads
   the parent's `parentCaps` into the supervisor), an unresolved member ask is register-then-emit
   SURFACED as a real parent `EvPermissionAsk` (command clamped, framed as a quoted subagent
   request, raw args dropped — gauntlet #7), and the parent's `Run.Approve` routes the verdict
   back to the member by its child-namespaced askID (`team-<teamID>-<member>:…` — NO wire change).
   A member parked awaiting the human blocks ONLY its own errgroup goroutine; peers keep running
   (the supervisor drains members concurrently and the forwarder is a single goroutine over a
   buffered channel — a parked member simply emits nothing until resolved).
4. **Headless auto-deny.** With no interactive parent (the gRPC `RunTeam` direct path, the offline
   demo), the ask auto-denies with the ACCURATE message ("not permitted in a non-interactive
   subagent shell: …") and a correlated operator diagnostic (`LevelInfo`, `agent=<member>`), never
   the misleading "denied by user" and never a parent-stream content leak.

Security: the discarded worktree, the clamped surfaced command, and the gauntlet-#7 content
isolation are all preserved; worktree-escape verbs stay denied even in isolation; a substitution
hiding a non-read-only inner is NOT auto-approved even when isolated (it surfaces or denies).

### 5.4 Quiescence & deadlock

The team is **done** when every member is `Idle`/`Stopped`, no task is
`pending`/`in_progress`, and every inbox is empty (`Team.Quiescent()` in the
kernel). The same predicate distinguishes "done" from "deadlocked waiting on each
other": if members are idle but tasks remain blocked by an unsatisfiable
dependency cycle, the supervisor surfaces it to the lead rather than hanging. A
global wall-clock/turn budget bounds a runaway team. A member's terminal
disposition — and, when it stopped, the closed-enum reason (`error`/`cancelled`/
`budget`) — now reaches the wire on `team.end` (a per-member snapshot parallel to
the terminal tasks/findings snapshots, bridged in `engine/agent`), so the ctrl+a
overlay renders `✗ stopped — <reason>` for a stopped member instead of flipping every
terminal lane to `✓ done`.

### 5.5 Permissions & non-interactivity

Teammates inherit the lead's permission mode at spawn (matches Claude Code:
"permissions set at spawn"). A member's permission asks resolve through the 4-step
model in §5.3.1 (substitution → isolation auto-approve → surface to the HUMAN parent
via `childAskRouter` → accurate headless auto-deny); the original "route to the lead"
idea was not built — the lead never arbitrates member asks. Plan-approval (teammate
plans read-only) still maps onto the existing `WithChildMode(session.ModePlan)`.

## 6. The coordination kernel (delivered prototype)

`engine/team/` implements and unit-tests the riskiest claim — that in-process,
shared-memory coordination is **correct under concurrency** and **testable
offline**. It is pure domain (no I/O, no LLM, no goroutines of its own), guarded
by a single mutex, and exercised under `-race`:

- `Team` aggregate: members (lifecycle states), a `TaskList`, a `Mailbox`.
- `CreateTask(desc, deps)` / `ClaimNext(member)` / `ClaimTask(id, member)` /
  `CompleteTask(id, member)` with **dependency gating** (a task is claimable only
  when all deps are completed) and **race-safe single-claim** (concurrent
  `ClaimNext` from N goroutines never double-assigns).
- `Send(from,to,body)` / `Drain(member)` mailbox with at-most-once delivery.
- `Quiescent()` done/deadlock predicate.

Tests cover: dependency gating, concurrent-claim safety (`-race`, N goroutines),
mailbox delivery semantics, unknown-member/unknown-task errors, and quiescence
transitions. Run: `go test ./engine/team/ -race`.

This kernel is deliberately **decoupled from the Engine** so it can be validated
before any supervisor/gRPC work exists — the essence of a spike.

## 7. gRPC surface (shipped — names as landed)

The RPCs on the existing service (contract is `contracts/proto/mecatl/v1/harness.proto`):

```
CreateTeam(CreateTeamRequest)             -> CreateTeamResponse   // + optional roster, goal, max_team_tokens
SpawnTeammate(SpawnTeammateRequest)       -> SpawnTeammateResponse
SendTeammateMessage(SendTeammateMessageRequest) -> SendTeammateMessageResponse
RunTeam(RunTeamRequest)                   -> stream TeamEvent     // member-tagged; terminal outcome frame
ListTeam(ListTeamRequest)                 -> ListTeamResponse
CleanupTeam(CleanupTeamRequest)           -> CleanupTeamResponse
```

`RunTeam` (the original sketch's `StreamTeamEvents`) is the multiplexed fan-in of
every member's `Run.Events()`, each event tagged with its member name — the headless
analogue of split panes; the sketched `ShutdownTeammate` was not built. The
existing per-session event mapping (`internal/adapter/server/mapper.go`) is reused
per member; only the member tag is new.

## 8. Phased implementation plan

1. **Kernel** *(done in this spike)* — `engine/team/` + tests.
2. **Continuation seam** — `session.Reopen()` + state-machine tests.
3. **Coordination tools** — `SendMessage`/`TaskCreate`/`TaskClaim`/`TaskComplete`
   as `tool.Tool`s over `*team.Team`; offline tests with `mockllm`.
4. **Supervisor** — `engine/agent` driver loop, event fan-in, quiescence,
   read-only-share/mutating-fork workspace policy; offline multi-member test
   (scripted `mockllm` per member) — the new gauntlet-style integration test.
5. **Hook phases** — `TeammateIdle`/`TaskCreated`/`TaskCompleted`.
6. **gRPC** — proto + server adapter + `task generate`.
7. **(Later) Join strategies** — judge/merge for mutating-teammate forks; optional
   `TeamStore` for restart durability.

Phases 1–4 are the substance and are all offline-testable. Phase 6 is the only one
touching the generated contract.

## 8a. Status of earlier gaps / remaining follow-ups

Two gaps flagged in the original spike have since been **closed**:

- **HTTP/SSE parity — DONE.** All six team RPCs now have HTTP routes
  (`http.go`): `POST /v1/teams` (create + optional roster), `POST
  /v1/teams/{id}/members`, `POST /v1/teams/{id}/messages`, `POST /v1/teams/{id}/run`
  (a `text/event-stream` of per-member-tagged `TeamEvent` frames, mirroring the
  `prompt` SSE handler), `GET /v1/teams/{id}`, `DELETE /v1/teams/{id}`. HTTP and gRPC
  share one JSON wire shape (the same `mecatlv1.*` messages); `ErrTeamsDisabled` /
  `ErrTeamRunning` → 412 and `ErrTooManyTeams` → 429 mirror the gRPC status codes.
- **Roster-in-CreateTeam — DONE.** `CreateTeamRequest` carries an optional
  `repeated TeammateSpec members`, enrolled **atomically** at creation (any member
  failure abandons the whole team — never registered, no `MaxTeams` slot consumed);
  `CreateTeamResponse` echoes the enrolled roster. The common path is now a single
  `CreateTeam` → `RunTeam`. `SpawnTeammate` remains for incremental pre-run adds (and
  is still rejected once the team is running, `ErrTeamRunning`).
- **Result aggregation — DONE.** The team's deliverable is now the lead's
  **consolidated synthesis** (§5.1), not a header-only concatenation of member
  `LastText`. Goal-to-lead threading (`WithTeamGoal` + `CreateTeamRequest.goal`), the
  `RecordFinding` ledger channel, the three-layer synthesis source, member-session
  persistence under `MemberSessionID` (`team-<teamID>-<member>`), the PULL
  `InspectMember` tool, and the `EvTeamFindings` event projection (wired through the
  proto + mecatui) all shipped together.

Still deferred (intentional, not oversights):

- **(See §8, phase 7)** join strategies for mutating-teammate forks and a `TeamStore`
  for restart durability remain deferred.
- ~~Per-teammate model/agent-definition selection~~ — since SHIPPED via agent
  definitions: `MemberSpec.AgentType` routes to a per-def `MemberEngine` factory,
  and the model resolves per def (`def.Model` > `--subagent-model` > parent; per-def
  `provider:` too). See `docs/adr/0013-agent-definitions.md`.

## 9. Risks / open questions

- **Determinism in tests.** Free-running member goroutines + a shared mutex are
  race-safe but scheduling is nondeterministic. The supervisor test must assert on
  *outcomes* (all tasks completed, transcript per member) not interleavings, and
  drive `mockllm` so each member's tool calls are scripted. The kernel itself is
  fully deterministic (no goroutines of its own).
- **`Reopen` semantics** — *Decided & implemented:* legal only from
  `StateCompleted` (illegal from failed/cancelled/non-terminal), and it **resets**
  per-run `Counters` so `Limits` keep their single-run meaning (bound each prompt's
  work). A teammate's lifetime budget (total turns across its life) is the
  supervisor's responsibility, enforced separately. See `session.Reopen`.
- **Lead context growth** — the lead must NOT ingest teammates' full transcripts
  (that defeats context isolation). *Implemented:* the lead's synthesis turn reads a
  fenced DIGEST — the findings ledger + a per-member LastText/completed-task summary +
  its drained inbox (`buildSynthesisSources`), all bounded and fenced UNTRUSTED — never
  the members' full transcripts. The mailbox + the findings ledger are the only
  channels through which teammate work reaches the lead; an out-of-band reader uses the
  PULL `InspectMember` tool, which never auto-injects.
- **Token cost** — teams are linearly more expensive than one session (each member
  is a full loop). Worth a config cap on concurrent members + a global budget.

## Sources

- Claude Code — Create custom subagents: https://code.claude.com/docs/en/sub-agents
- Claude Code — Orchestrate teams of Claude Code sessions: https://code.claude.com/docs/en/agent-teams
- OpenAI Agents SDK — Orchestration & handoffs: https://developers.openai.com/api/docs/guides/agents/orchestration
- Goose — Sub-recipes / subagents: https://block.github.io/goose/docs/guides/recipes/sub-recipes/


---

*Part of the [design docs](../design/README.md). Related: [Agent definitions (Tier 1)](0013-agent-definitions.md), [BACKGROUND-SUBAGENTS.md — Background Subagents + Per-Child Cancel over a Shared Child-Run Registry](0015-background-subagents.md).*
