# Subagents, teams, and parallel work

> Part of the [Mecatl architecture guide](../architecture.md).

The parent agent hands work to child loops through three tools. Each child runs
[the agent loop](agent-loop.md) on an engine that `internal/app` builds with a narrower catalog.
The tools live in `engine/agent`; team coordination state lives in `engine/team`. For arguments
and flags, see [the user guide](../../user-docs/features/agent-behavior/subagents-and-teams.md).

| Tool | Intent | Child workspace | Returns to the parent |
| --- | --- | --- | --- |
| `Subagent` | One focused task | Read-only worktree, or the real tree in direct-write mode | One summary |
| `Parallel` | Competing or independent implementations | A writable full copy per branch | Joined summaries |
| `Team` | Specialists that must coordinate | An isolated workspace per member | The lead's synthesis |

## What every child shares

**A fresh, bounded session, and no nesting.** Each child gets its own `session.Session` and
limits (`defaultChildLimits`, tighter than a main session's); per-call caps only tighten. Child
catalogs never contain `Subagent`, `Parallel`, `Team`, or `ToolSearch`, so a child cannot delegate.

**Narrowed authority.** All three families derive the child's capability set through
`deriveDelegatedAuthority`. It intersects the parent's set with the candidate and any specialist
ceiling, applies caller tightening, and consumes one delegation hop. It runs before the tool
acquires an engine, workspace, or session, so a refusal leaves nothing to clean up. A resume
consumes no hop; it only checks that the persisted authority still fits inside the current
parent's. The child inherits the owner recorded on the parent session, never the current caller.

**Context isolation.** `Subagent` and `Parallel` drain the child's event stream inside the tool
call (`drainChildObserved`) and return only the final text. No child transcript enters the parent
conversation. Clients see a separate projection of bounded, scrubbed previews, and a child's
`permission.ask` is dropped from it because the reason can carry secrets. Team members stream
bounded previews instead, because a team is meant to be watched. Child text that reaches the
parent model is framing-neutralized, so a child cannot forge a harness fence or header. Child
sessions persist under disjoint ID prefixes (`subagent-`, `parallel-`, `team-<teamID>-<member>`),
and the parent pulls a transcript on demand with `InspectSubagent` or `InspectMember`.

**One registry per parent run.** Every child, plus background `Shell` jobs, registers in the run's
child registry (`childregistry.go`). `Run.CancelChild` uses it to cancel one child without stopping
the run, retracting any permission ask that child had parked.

**Permissions.** Children use an allow-all floor plus subagent-scoped rules, and never learn rules.
An interactive parent surfaces an unresolved ask and routes the verdict back by the child's
namespaced ask ID. Headless, an optional reviewer may approve one call (never a configured Ask);
otherwise the ask is denied with the real cause. [Governance](governance.md) has the details.

## Subagent

### Provider and model selectors

Calls may supply optional `provider` and `model` selectors. Omitting both preserves
inherited defaults and automatic routing. A literal model or unbound scalar alias,
including a CLI scalar alias, uses the parent provider. Operator-settings scalar
aliases bind to `models.default_provider` (or `--default-provider`) when configured;
otherwise they remain parent-relative. A pair alias or explicit provider/model pair
builds a fresh child through the provider-specific factory.
`provider: "model-router"` plus an exact discovered category
selects that operator category without classification. Explicit selection fails before
child construction rather than falling back.

A named specialist rejects provider-bearing call-level selectors. A read-only model-only
override rebuilds the specialist while preserving its scope; direct-write rejects the
explicit `agent` plus `model` combination. Unpinned named specialists remain eligible for
automatic routing with their prompt, tools, skills, limits, and authority preserved.
`fork` and `resume` reject selectors. A def-less child uses the global `--subagent-model`
default before inheriting the parent target; a provider-aware alias preserves its pair.

Parallel applies one optional pair to every branch, while its judge stays on the parent
model. Team members select their own pairs; the whole roster is validated before adding
any member, and resolved engines are retained across rounds. Named Team members accept
a model-only override but reject provider-bearing call-level selectors.

### Read-only explorer by default

Isolation, not the absence of mutating tools, is the security boundary. With `Shell` available,
the default child gets Read, Grep, Glob, and `Shell` in a throwaway git worktree that shares the
parent `.git` (full history). The forker mirrors uncommitted changes into it so the child sees
what the operator sees; if that fails, the worktree resets to clean `HEAD` and the child is told.
Because `.git` is shared, the shell runs with an environment that `internal/adapter/gitenv` scrubs
of config-driven code execution (hooks, pager, fsmonitor, external diff). The forker's own
`git worktree add` runs under the same scrub, so the base repository's `post-checkout`
hook doesn't fire at fork time. An untrusted workspace
gets no worktree shell, because creating the worktree runs a checkout a hostile repository could
abuse. A failed fork is a tool error, never a fallback to the shared tree. Without a shell, the
child reads the parent tree through a confined view without the main session's out-of-root reads.

### Modes, by intent

- **Start clean** (default). The child sees only its prompt.
- **Continue from here** (`fork`). The child starts from a deep copy of the parent conversation,
  with the dangling fork call stripped so tool pairing stays valid. History is copied verbatim,
  not re-fenced, to keep the provider prompt-cache prefix stable. A fork runs on the parent's
  engine, so it excludes `model`, `agent`, and `resume`.
- **Land edits** (`mode: "read-write"`). See [the next section](#direct-write-children-and-the-barrier).
- **Keep working meanwhile** (`background`). The call returns at once. Taking a concurrency slot
  (`defaultMaxConcurrentChildren`) fails fast instead of blocking, because a background child
  holds its slot across turns and a blocked call could deadlock the model against itself. The
  result arrives once, through `SubagentStatus`; a turn-boundary notice and a single nudge keep it
  from being lost. At run end, live background children are cancelled, joined, and persisted.
- **Pick up a previous child** (`resume`). The tool reloads the child and recovers it from
  completed, cancelled, or failed, repairing history as needed. The conversation survives but the
  workspace does not, so the child gets a fresh fork and a staleness note. Only a writable resume
  whose persisted environment ref exactly equals the parent's, revision included, is told its
  edits survived. A guard rejects a second concurrent run on the same ID. Resume remints
  the provider/model recorded on the child session and fails closed when unavailable;
  legacy unlabeled children use the default explorer. Requested aliases and category names
  are not the persisted identity.

### Direct-write children and the barrier

A `mode: "read-write"` child runs against the real parent environment with Edit, Write, and
`Shell`. Nothing is forked or merged: edits land in place and git is the rollback. Its shell uses
the main session's command runner, because it acts on the real repository and must resolve
exactly as the main session does. For the same reason it is not treated as isolated when its
permission asks are resolved.

`SubagentTool.ReadOnly()` stays `true` so read-only delegations batch concurrently. A direct-write
call instead reports `MutatesParent(call) == true` (`parentMutatingCaller` in `dispatch.go`). The
dispatcher then pulls it out of the concurrent read batch and runs it alone, behind the same
barrier as any mutating tool. Otherwise a sibling Read or Grep in the batch could observe a
half-written tree. Direct-write also refuses `background`, so it cannot race the parent's edits.
A crashed child can leave partial edits; its failure result offers a resume-or-discard choice
that names `mode: "read-write"`, since a bare resume returns a read-only explorer.

## Parallel: fork-join

`Parallel` forks one branch per task (16 maximum, 8 at once) and joins the results. Each branch
gets a force-copy of the workspace with its own `.git`, plus Edit, Write, and `Shell`, so
concurrent writes are safe. Force-copy forking runs no git, so the trust gate does not apply,
but the branch shell is still hardened because git later runs over the copied `.git`.

- `all` (default) returns every summary and removes every fork.
- `first` returns the first successful branch and cancels the rest.
- `judge` (alias `best`) has a judge on the session model pick one branch from the summaries only.
  A misbehaving judge falls back to the first success.

A `first` or `judge` winner is kept in a process-wide LRU (`forkreaper.go`) and reported as an
opaque artifact handle until eviction or graceful shutdown removes it. A single-branch `first`
or `judge` call merges the winner into the parent by default through
`tool.EnvironmentMerger`. The merger refuses patches that touch `.gitattributes`, diffs without
textconv so the fork's git config cannot run code in the parent, never forces on conflict, and is
serialized process-wide. Such a call reports `MutatesParent`, so it also runs alone. Multi-branch
calls never merge.

## Teams

`engine/team` is a pure, in-memory aggregate behind one mutex: a roster, a dependency-aware task
list, per-member mailboxes, and a findings ledger, each capped. Messages come only from roster
members or the reserved operator sender, so neither can impersonate the other.

`agent.Supervisor` drives members in rounds. Each round it plans which members have work (a
pending message, or a claimable task for non-leads), runs those turns concurrently, then reopens
each session, so messages arrive at turn boundaries and never mid-turn. The team ends when a round
plans no work; a round cap and a per-member lifetime turn budget bound a team that never
converges. Members coordinate through `SendMessage`, `AddTask`, `ClaimTask`, `CompleteTask`,
`ListTasks`, and `RecordFinding`, and peer messages and task text are fenced as untrusted. The
first member is the lead. Its final synthesis turn reads the findings ledger, a digest of member
output, and its inbox, all fenced and never full transcripts; that report is the deliverable.

| Member | Workspace | Tools |
| --- | --- | --- |
| Mutating | Force-copy fork with its own `.git` | Edit, Write, `Shell` |
| Read-only, trusted workspace | Git worktree with uncommitted changes mirrored | Read tools, hardened `Shell` |
| Read-only, no worktree shell | Shares the base tree | Read tools only |

No member workspace is merged back, and the supervisor rejects a base-sharing member whose catalog
holds a mutating tool. A team token budget sums all members and is checked between rounds: once
crossed, no new round starts, but the current round and the synthesis finish. It is separate from
the per-engine run-token ceiling, which counts only that engine's own session, so a delegation
tree can exceed that ceiling in total.

Teams run through the `Team` tool and the headless gRPC `CreateTeam`, `RunTeam`, and
`CancelTeammate` calls. Both paths share one member-engine factory, so they cannot drift.

## Where child workspaces come from

`tool.EnvironmentForker` (`engine/tool/isolation.go`) is the isolation seam. A fork returns a
complete child `Environment` (workspace, runner bound to the child namespace, fresh read ledger)
that never writes back to the base. `forker.KindRouter` (`internal/adapter/forker`) picks a
forker by the parent environment's kind; the microVM backend plugs in here. An unknown remote
kind fails instead of falling back to the host filesystem, which would split workspace and runner.

Delegation never accepts placement input. The model-facing schemas have no workspace, path, or
selector argument, and an `engine/agent` test enforces that. `Subagent` and `Parallel` share
or server-fork the parent environment, and `Team` derives member environments from the owning
session. Results and events carry opaque handles, never fork roots or exact environment refs, and
those handles cannot be replayed as the worktree selectors that session placement uses.

The no-filesystem profile keeps `Subagent` and `Team` but gives children a file-less catalog
(memory, web fetch, MCP) with no forker, no shell, and no direct-write mode, and drops `Parallel`.
The remote-execution profile omits all three tools.

## Related

- [The agent loop](agent-loop.md): dispatch and the read-parallel, mutate-serial barrier.
- [Governance](governance.md): child permission resolution, workspace trust, and fences.
- [Providers](providers.md): child model selection and the semantic router.
- [MicroVM environments](microvm-environments.md): server-owned placement and child worktrees.
- [Subagents, teams, and parallel work](../../user-docs/features/agent-behavior/subagents-and-teams.md): user-facing arguments and flags.
