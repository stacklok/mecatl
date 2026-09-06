# Parallelism — fork-join

> Part of the [mecatl architecture guide](../architecture.md).

**What this covers:** the `tool.EnvironmentForker` isolation seam, the `Parallel` fan-out tool (branch isolation, join modes, auto-merge), agent-team member workspace policy (force-copy mutating / worktree read-only / sandboxed runners), the team-wide token budget, and worktree binding (operator-owned existing worktrees).

**Prerequisites:** [subagents & teams](subagents-and-teams.md) — the sibling delegation family (Subagent children, team-member workspaces share the same forker/seam).

**Follow-on:** [deployment & hardening](deployment-and-hardening.md) — workspace trust gates and posture ladder interact with forker isolation.

`tool.EnvironmentForker` (`engine/tool/isolation.go`) is the environment-isolation
seam: `Fork(ctx, base Environment, label)` returns an isolated child
`Environment` (a complete `Workspace` + a `CommandRunner` bound to the child
namespace + a child ref) plus a cleanup func. The default
`internal/adapter/forker` picks its strategy per base —
a **git worktree** (`git worktree add --detach … HEAD`) when the root is inside a
repo, else a **recursive copy** — so a child can never write back into the
parent's tree. `agent.NewParallelTool(childEngine, forker, …)` is the fan-out tool
(catalog name `Parallel`): it runs several isolated child loops on independent
branches and joins their results. It is default-on (`--enable-parallel`, disable
with `--enable-parallel=false`); like Subagent,
the children's intermediate events are drained internally. Each branch's child session
is best-effort persisted (`WithParallelStore`) and the joined result text surfaces a
`branch id:` line per branch (the deterministic `parallel-<callID>-<i>`) so the parent
can pull any branch's bounded transcript via `InspectSubagent` — the same PULL channel as
the Subagent `agentId:` trailer (issue #30; `InspectSubagent`'s gate admits both the
`subagent-` and `parallel-` families, `team-` staying with `InspectMember`).

**Merging a winner back.** Parallel does NOT auto-merge fan-out: for `join=all`
every fork is torn down after the join (the result reports branch ids for
transcript pulls, NOT workspace paths — the forks are gone); for `join=first` /
`join=judge` the winner workspace path is reported. The shared process LRU normally
retains winners until eviction or graceful app shutdown, but a path may already be gone
if shutdown began concurrently. Shutdown drains
both currently retained winners and eviction cleanups detached before closure, without
holding the reaper lock during filesystem work; it does not wait for a later `Preserve`.
A crash remains a residual and does not sweep them at startup. A SINGLE-BRANCH `join=first`/`join=judge` winner is auto-merged back
into the parent workspace BY DEFAULT (no flag; see
[ADR 0039](../adr/0039-parallel-auto-merge.md)): the winner's diff is applied via
`tool.EnvironmentMerger` (the `forker.Merger` adapter — `git diff --no-textconv HEAD`
from the fork piped to `git apply` in the parent, plus untracked-file copy; the
merge refuses `.gitattributes`-touching patches and runs `--no-textconv` to close
attacker-named `diff.*.textconv`/`filter.*.smudge` RCE from an untrusted fork
`.git`). Multi-branch runs and `join=all` NEVER auto-merge (the no-auto-merge
boundary stays for fan-out). On a conflict the merge surfaces a tool error with the
winner workspace path, which is ephemeral and may already be gone if graceful shutdown
began; it never forces. `Parallel.ReadOnly()`
stays `true` — the merge is a POST-RUN step, not a dispatch-time mutation, so
read-parallel / mutate-serial is unaffected.

The same seam serves **agent teams** (`agent.Supervisor`/`TeamTool`) with a
**three-tier** member workspace policy. A Mutating member forks **force-copy** (own
`.git`, via `forker.WithForceCopy`) and gets Edit/Write/Bash; a read-only member the
factory marks `MemberBuild.IsolateReadOnly` forks **worktree** (the forker DEFAULT —
shares the base repo's `.git`, so it sees full history) and gets Read/Grep/Glob +
Bash but never Edit/Write, so it can `git log`/`git show`/build/test confined to a
throwaway checkout; a base-sharing read-only member (no forker wired) gets NO shell.
The Supervisor holds two forkers (`s.forker` force-copy, `s.roForker` worktree); the
mutating-tool backstop gates on base-sharing, exempting any isolated member. A
read-only member's Bash runs through a **sandboxed** runner
(`buildSandboxedCommandRunner`) that neutralises git config-driven code execution in
the shared `.git`. The single neutralizing env lives in the stdlib-only leaf
`internal/adapter/gitenv` (`Scrub`) so the **forker's own git** (whose `git worktree
add` would otherwise fire the base repo's `post-checkout` hook at fork time) and the
member runner share it and can't drift: `Scrub` drops inherited `GIT_*` danger
(`GIT_EXTERNAL_DIFF`/`GIT_SSH_COMMAND`/…) and forces `core.hooksPath=/dev/null`,
`core.pager=cat`, `core.fsmonitor=false`, empty `diff.external`, `GIT_PAGER`/`PAGER=cat`,
`GIT_CONFIG_NOSYSTEM`. The main session keeps its unhardened runner. **The Subagent
subagent ([subagents & teams](subagents-and-teams.md)) shares this exact treatment**: when Bash is configured `SubagentTool` holds
its own worktree forker (`WithChildForker`) and forks each child into a throwaway
worktree, with the SAME `buildSandboxedCommandRunner` + `gitenv` hardening and the
SAME untrusted-`.gitattributes` residual; the workspace-trust gate (issue #40,
shipped — see [subagents & teams](subagents-and-teams.md)) covers both the same way: an untrusted workspace yields no
read-only-member/subagent shell at all.

A worktree forks from the committed `HEAD`, so a plain `git worktree add --detach
HEAD` gives the read-only child a CLEAN tree — `git status`/`git diff` and the file
tools would see no changes even when the operator has uncommitted work, hiding the
in-progress changes an explorer is usually dispatched to review. **The read-only
worktree forkers carry `forker.WithDirtyOverlay()`** (the Subagent child forker and
the team `roForker`; see [ADR 0033](../adr/0033-dirty-aware-readonly-fork.md)) which,
after the worktree is created and only when the parent is dirty (`git status
--porcelain` probe), mirrors the parent's uncommitted state into it: applies `git
diff --no-ext-diff --binary HEAD` (tracked edits + staged + deletions; `--binary`
round-trips binaries) via `git apply`, and copies untracked, non-ignored files
(`git ls-files --others --exclude-standard`, skipping symlinks). It is
`.gitignore`-respecting, best-effort (any failure resets the worktree to pristine
HEAD — a partial overlay is worse than none), a no-op on a clean tree, and its new
`git` calls carry the SAME scrubbed `gitenv` env. The **force-copy** mutating forkers
are untouched: `copyTree` already carries the parent's dirty state verbatim.

A team's **returned deliverable** is the **lead's consolidated synthesis**, not a
concatenation of member `LastText`: after the scheduling loop, `Supervisor.Run` drives
ONE final synthesis turn on the lead whose output is `TeamOutcome.Report` (the Team
tool's `ToolResult`; the gRPC `RunTeam` carries it on the outcome). The synthesis
prompt reads three fenced-UNTRUSTED layers — the **findings ledger** (members append
with the `RecordFinding` tool, the primary channel), a **LastText/completed-task
digest** for non-recording members, and the **lead's inbox** — never the members' full
transcripts (context isolation holds). Member sessions persist to the `port.SessionStore`
under `MemberSessionID(teamID, member)` (`team-<teamID>-<member>`, collision-free across
concurrent teams); the parent catalog's read-only **`InspectMember`** tool pulls ONE
member's bounded transcript on demand (PULL — never auto-injected). A `team.findings`
event projects the ledger onto the stream, mirroring `team.tasks`. The stream projection
is the ADR-0079 tier-2 superset: `team.member` forwards only capped member message/tool
previews (the tier-1 bounded previews Subagent/Parallel now share; never a `permission.ask`),
and the Team-unique structures add task/finding snapshots (capped value types), the mutating
cue, the context meter, and `team.end` aggregate usage plus closed-enum
member dispositions (`done` or `stopped` for `error`/`cancelled`/`budget`).

A team-wide **token budget** is distinct from the independently enforced per-engine
`MaxRunTokens` ceilings ([the agent loop](agent-loop.md)). The configured
`MaxRunTokens` value is inherited by the main engine, every Subagent, every Parallel
branch, every team member, and lead synthesis; each engine checks only its own session
usage. Parent usage excludes child spend, so a delegation tree can exceed that
per-engine ceiling. `Supervisor.WithTeamTokenBudget` (`--max-team-tokens`, **default:
unlimited**; gRPC `CreateTeamRequest.max_team_tokens`; a per-call Team
`max_team_tokens` arg may only tighten it) is instead a supervisor-level aggregate
checked at the ROUND boundary before scheduling. Once crossed it prevents new rounds;
the current round and the lead's synthesis still complete, and members are never
individually stopped. It surfaces via `TeamOutcome.BudgetExhausted` plus a `StopBudget`
team stop.

## Worktree placement — server-owned existing worktrees (ADR 0291)

The fork seam above creates ephemeral internal environments for isolation. Operator-facing
worktree selection is a different, source-session-scoped capability. Clients cannot pass a
path at CreateSession. `ListWorktrees(session_id)` authorizes the owner, exactly reattaches
the source environment, and enumerates only currently eligible worktrees. Results contain
bounded display metadata and an opaque selector, never a root or exact EnvironmentRef.

The local selector is HMAC-SHA256 over provider-private current identity plus caller/source
scope using one random Build-owned key. It is never decoded or retained in a registry/map.
ClearSession/ForkSession re-list and constant-time match it, then atomically bind the complete
successor Environment. Restart invalidates selectors and clients relist. No-FS returns empty
without filesystem discovery and cannot upgrade. Mecatui keeps the source active when relist
or successor creation fails. Internal preserved-fork, delegation, and artifact handles are
distinct typed capabilities and can never be replayed as these selectors.

Trust remains composition/operator policy; osfs confinement and the Environment
Workspace/runner affinity are unchanged.

## Prerequisites

- [Subagents & teams](subagents-and-teams.md) — the sibling delegation families.

## Follow-on reading

- [Deployment & server hardening](deployment-and-hardening.md) — workspace trust and the posture ladder interact with forker isolation.

## Related

- [The agent loop that dispatches branches](agent-loop.md)

---

[← Architecture guide](../architecture.md)
