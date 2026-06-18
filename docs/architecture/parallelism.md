# Parallelism — fork-join

> Part of the [mecatl architecture guide](../architecture.md).

`tool.WorkspaceForker` (`engine/tool/isolation.go`) is the workspace-isolation seam:
`Fork(ctx, base, label)` returns an isolated child `Workspace` plus a cleanup
func. The default `internal/adapter/forker` picks its strategy per base —
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
is intentionally fuller than Subagent/Parallel but structurally bounded: `team.member`
forwards only capped member message/tool previews (never a `permission.ask`), task/finding
snapshots are capped value types, and `team.end` carries aggregate usage plus closed-enum
member dispositions (`done` or `stopped` for `error`/`cancelled`/`budget`).

A team-wide **token budget** complements the per-run one ([the agent loop](agent-loop.md)):
`Supervisor.WithTeamTokenBudget` (`--max-team-tokens`, **default: unlimited**; gRPC
`CreateTeamRequest.max_team_tokens`; a per-call Team `max_team_tokens` arg may
only tighten it) is a supervisor-level ceiling checked at the ROUND boundary
before scheduling — the in-flight round and the lead's synthesis still
complete, and members are never individually stopped — accumulating each
member's per-drive usage and surfacing via `TeamOutcome.BudgetExhausted` plus
a `StopBudget` team stop. It is orthogonal to `--max-run-tokens`, which each
member inherits per-run.

## Worktree binding — operator-owned existing worktrees (issue #102)

The fork seam above creates EPHEMERAL internal worktrees for isolation. A
distinct, operator-facing concern is binding a session to an ALREADY-EXISTING git
worktree so ALL local tools (`Read`/`Edit`/`Write`/`Grep`/`Glob`/`Bash`) are
rooted there. The session workspace is set at `CreateSession` time and the file
tools scope to it; the gap mecatl had was no first-class way to pick a sibling
worktree AFTER launch (issue #102).

The fix (see [ADR 0032](../adr/0032-worktree-binding.md)) is three clear,
separated interfaces:

- **Discovery** — a `ListWorktrees(workspace)` RPC backed by a
  composition-injected, nil-safe `WorktreeLister` port (mirrors `ListCommands`).
  The osfs-backed implementation shells out to `git worktree list --porcelain`
  with the same scrubbed+neutralised git env as `gitSnapshot`, trust-gated,
  fail-soft. A no-FS/cloud deployment leaves it nil ⇒ empty list +
  `ServerCapabilities.worktrees = false`, so the feature is honestly absent
  there (cloud-native compatible).
- **Routing** — a session whose `workspace != Config.DefaultWorkspace` (the
  launch root) routes through the per-session engine factory, which ALREADY
  re-pins the CHILD permission resolver to the session root
  (`childPermResolverFor`). The main policy ALREADY re-resolves
  `.mecatl/settings.yaml` per workspace. When `DefaultWorkspace == ""` (a
  child/member/cloud service) the trigger never fires. The session rehydrates to
  the SAME worktree-rooted engine after a restart (the `needsRehydration`
  widening).
- **Switching** — mecatui's `/worktrees` overlay (a selecting overlay mirroring
  `/models`) lists the worktrees and, on select, closes the old session and
  creates a NEW one rooted at the chosen worktree via
  `CreateSessionInWorkspace` (the restart-now precedent). Operator-driven only;
  the model has no workspace-switch tool; a live session is never mutated.

Trust stays OPERATOR-tier at launch (worktrees share `.git`); osfs path
confinement is unchanged.

## Related

- [Subagents & teams — the sibling delegation families](subagents-and-teams.md)
- [The agent loop that dispatches branches](agent-loop.md)

---

[← Architecture guide](../architecture.md)
